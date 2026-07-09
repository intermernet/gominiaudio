package gominiaudio

import (
	"math"
	"sync"
	"sync/atomic"
)

// This file implements the node graph, mirroring ma_node_graph. Nodes
// process 32-bit float interleaved frames. Each output bus can be attached
// to at most one input bus (use a SplitterNode for fan-out), exactly like
// miniaudio.
//
// Concurrency model: node state, state times, volumes and clocks are
// atomics, so control-plane reads and writes never block the audio thread.
// Topology changes (attach/detach) are enqueued as commands and applied by
// the reading thread at chunk boundaries; the attachment lists themselves
// are only ever touched by the reader. A control-plane goroutine that gets
// preempted can therefore never stall audio processing (no shared lock is
// held across DSP work).

// NodeState mirrors ma_node_state.
type NodeState uint32

const (
	NodeStateStarted NodeState = 0
	NodeStateStopped NodeState = 1
)

// NodeFlags mirrors ma_node_flags.
type NodeFlags uint32

const (
	// NodeFlagPassthrough: the node just passes data through, output bus 0
	// mirrors input bus 0.
	NodeFlagPassthrough NodeFlags = 1 << 0
	// NodeFlagContinuousProcessing: process even when no data is available
	// from inputs.
	NodeFlagContinuousProcessing NodeFlags = 1 << 1
	// NodeFlagAllowNullInput: the processor accepts nil input buffers.
	NodeFlagAllowNullInput NodeFlags = 1 << 2
	// NodeFlagDifferentProcessingRates: input and output rates differ (e.g.
	// resampling nodes).
	NodeFlagDifferentProcessingRates NodeFlags = 1 << 3
	// NodeFlagSilentOutput: the node's output should not contribute to the
	// mix (e.g. analysis taps).
	NodeFlagSilentOutput NodeFlags = 1 << 4
)

// nodeCacheCapInFrames mirrors MA_DEFAULT_NODE_CACHE_CAP_IN_FRAMES_PER_BUS.
const nodeCacheCapInFrames = 500

// NodeProcessor is the processing callback, mirroring
// ma_node_vtable.onProcess. framesIn holds one interleaved f32 slice per
// input bus (nil when no data); framesOut one per output bus. It returns the
// consumed input frame count and produced output frame count.
type NodeProcessor interface {
	ProcessNode(framesIn [][]float32, frameCountIn uint32, framesOut [][]float32, frameCountOut uint32) (consumed, produced uint32)
}

// Node is implemented by anything that can live in a node graph. Concrete
// node types embed NodeBase.
type Node interface {
	nodeBase() *NodeBase
}

// nodeOutputBus mirrors ma_node_output_bus.
type nodeOutputBus struct {
	node     *NodeBase
	busIndex uint32
	channels uint32
	volume   atomic.Uint32 // math.Float32bits; written by the control plane, read by the audio thread.

	attachedTo *nodeInputBus // The single input bus this output feeds, or nil. Reader-owned.

	// Cached processed frames not yet consumed by the reader.
	cache    []float32
	cacheLen uint32 // Frames.
	cachePos uint32 // Frames.
}

// nodeInputBus mirrors ma_node_input_bus. The attachments list is only
// mutated by the reading thread (via graph commands).
type nodeInputBus struct {
	node        *NodeBase
	busIndex    uint32
	channels    uint32
	attachments []*nodeOutputBus
}

// NodeConfig mirrors ma_node_config.
type NodeConfig struct {
	Processor      NodeProcessor
	Flags          NodeFlags
	InputBusCount  uint32
	OutputBusCount uint32
	InputChannels  []uint32 // One entry per input bus.
	OutputChannels []uint32 // One entry per output bus.
	InitialState   NodeState
}

// NodeConfigInit mirrors ma_node_config_init.
func NodeConfigInit() NodeConfig {
	return NodeConfig{InitialState: NodeStateStarted}
}

// NodeBase mirrors ma_node_base. Custom nodes embed this and are
// initialized with NewNodeBase / (*NodeGraph).NewNode.
type NodeBase struct {
	graph       *NodeGraph
	processor   NodeProcessor
	flags       NodeFlags
	inputBuses  []nodeInputBus
	outputBuses []nodeOutputBus

	state      atomic.Uint32    // NodeState.
	stateTimes [2]atomic.Uint64 // Global time at which to start/stop; indexed by NodeState.
	localTime  atomic.Uint64

	// Preallocated slice headers for the processor call, avoiding per-chunk
	// allocations on the audio path.
	framesIn  [][]float32
	framesOut [][]float32

	self Node // The concrete node, for callers that need it.
}

func (n *NodeBase) nodeBase() *NodeBase { return n }

// NewNode mirrors ma_node_init: creates a standalone node for the graph.
func (g *NodeGraph) NewNode(config NodeConfig) (*NodeBase, error) {
	if config.InputBusCount > 0 && len(config.InputChannels) < int(config.InputBusCount) {
		return nil, ErrInvalidArgs
	}
	if config.OutputBusCount > 0 && len(config.OutputChannels) < int(config.OutputBusCount) {
		return nil, ErrInvalidArgs
	}
	n := &NodeBase{
		graph:     g,
		processor: config.Processor,
		flags:     config.Flags,
	}
	n.state.Store(uint32(config.InitialState))
	n.stateTimes[NodeStateStarted].Store(0)
	n.stateTimes[NodeStateStopped].Store(^uint64(0))
	n.self = n

	n.inputBuses = make([]nodeInputBus, config.InputBusCount)
	for i := range n.inputBuses {
		n.inputBuses[i] = nodeInputBus{node: n, busIndex: uint32(i), channels: config.InputChannels[i]}
	}
	n.outputBuses = make([]nodeOutputBus, config.OutputBusCount)
	for i := range n.outputBuses {
		b := &n.outputBuses[i]
		b.node = n
		b.busIndex = uint32(i)
		b.channels = config.OutputChannels[i]
		b.volume.Store(math.Float32bits(1))
		b.cache = make([]float32, nodeCacheCapInFrames*int(config.OutputChannels[i]))
	}
	n.framesIn = make([][]float32, config.InputBusCount)
	n.framesOut = make([][]float32, config.OutputBusCount)
	return n, nil
}

// InputBusCount mirrors ma_node_get_input_bus_count.
func (n *NodeBase) InputBusCount() uint32 { return uint32(len(n.inputBuses)) }

// OutputBusCount mirrors ma_node_get_output_bus_count.
func (n *NodeBase) OutputBusCount() uint32 { return uint32(len(n.outputBuses)) }

// InputChannels mirrors ma_node_get_input_channels.
func (n *NodeBase) InputChannels(busIndex uint32) uint32 {
	if int(busIndex) >= len(n.inputBuses) {
		return 0
	}
	return n.inputBuses[busIndex].channels
}

// OutputChannels mirrors ma_node_get_output_channels.
func (n *NodeBase) OutputChannels(busIndex uint32) uint32 {
	if int(busIndex) >= len(n.outputBuses) {
		return 0
	}
	return n.outputBuses[busIndex].channels
}

// AttachOutputBus mirrors ma_node_attach_output_bus: attaches this node's
// output bus to another node's input bus. An output bus can only be attached
// to one input bus at a time; attaching detaches any previous attachment.
// The change is validated immediately but takes effect at the next chunk
// boundary of the reading thread; this call never blocks audio processing.
func (n *NodeBase) AttachOutputBus(outputBusIndex uint32, other Node, otherInputBusIndex uint32) error {
	if other == nil {
		return ErrInvalidArgs
	}
	dst := other.nodeBase()
	if int(outputBusIndex) >= len(n.outputBuses) || int(otherInputBusIndex) >= len(dst.inputBuses) {
		return ErrInvalidArgs
	}
	if n.graph != dst.graph {
		return ErrInvalidOperation
	}
	if n.outputBuses[outputBusIndex].channels != dst.inputBuses[otherInputBusIndex].channels {
		return ErrInvalidArgs
	}

	n.graph.enqueueCommand(graphCommand{
		op:  graphCmdAttach,
		out: &n.outputBuses[outputBusIndex],
		in:  &dst.inputBuses[otherInputBusIndex],
	})
	return nil
}

// DetachOutputBus mirrors ma_node_detach_output_bus. Takes effect at the
// next chunk boundary of the reading thread.
func (n *NodeBase) DetachOutputBus(outputBusIndex uint32) error {
	if int(outputBusIndex) >= len(n.outputBuses) {
		return ErrInvalidArgs
	}
	n.graph.enqueueCommand(graphCommand{op: graphCmdDetach, out: &n.outputBuses[outputBusIndex]})
	return nil
}

// DetachAllOutputBuses mirrors ma_node_detach_all_output_buses. Takes
// effect at the next chunk boundary of the reading thread.
func (n *NodeBase) DetachAllOutputBuses() error {
	for i := range n.outputBuses {
		n.graph.enqueueCommand(graphCommand{op: graphCmdDetach, out: &n.outputBuses[i]})
	}
	return nil
}

// detachOutput removes an output bus from its input bus. Reader-thread only.
func (g *NodeGraph) detachOutput(out *nodeOutputBus) {
	in := out.attachedTo
	if in == nil {
		return
	}
	for i, a := range in.attachments {
		if a == out {
			in.attachments = append(in.attachments[:i], in.attachments[i+1:]...)
			break
		}
	}
	out.attachedTo = nil
}

// SetOutputBusVolume mirrors ma_node_set_output_bus_volume. Lock-free.
func (n *NodeBase) SetOutputBusVolume(busIndex uint32, volume float32) error {
	if int(busIndex) >= len(n.outputBuses) {
		return ErrInvalidArgs
	}
	n.outputBuses[busIndex].volume.Store(math.Float32bits(volume))
	return nil
}

// OutputBusVolume mirrors ma_node_get_output_bus_volume.
func (n *NodeBase) OutputBusVolume(busIndex uint32) float32 {
	if int(busIndex) >= len(n.outputBuses) {
		return 0
	}
	return math.Float32frombits(n.outputBuses[busIndex].volume.Load())
}

// SetState mirrors ma_node_set_state. Lock-free.
func (n *NodeBase) SetState(state NodeState) error {
	n.state.Store(uint32(state))
	return nil
}

// State mirrors ma_node_get_state.
func (n *NodeBase) State() NodeState {
	return NodeState(n.state.Load())
}

// SetStateTime mirrors ma_node_set_state_time: the global time at which the
// state change takes effect. Lock-free.
func (n *NodeBase) SetStateTime(state NodeState, globalTime uint64) error {
	if state != NodeStateStarted && state != NodeStateStopped {
		return ErrInvalidArgs
	}
	n.stateTimes[state].Store(globalTime)
	return nil
}

// StateTime mirrors ma_node_get_state_time.
func (n *NodeBase) StateTime(state NodeState) uint64 {
	if state != NodeStateStarted && state != NodeStateStopped {
		return 0
	}
	return n.stateTimes[state].Load()
}

// StateByTime mirrors ma_node_get_state_by_time.
func (n *NodeBase) StateByTime(globalTime uint64) NodeState {
	return n.stateByTimeRange(globalTime, globalTime)
}

func (n *NodeBase) stateByTimeRange(globalTimeBeg, globalTimeEnd uint64) NodeState {
	if NodeState(n.state.Load()) == NodeStateStopped {
		return NodeStateStopped
	}
	if globalTimeBeg < n.stateTimes[NodeStateStarted].Load() {
		return NodeStateStopped
	}
	if globalTimeBeg >= n.stateTimes[NodeStateStopped].Load() {
		return NodeStateStopped
	}
	return NodeStateStarted
}

// Time mirrors ma_node_get_time: the node's local time in frames.
func (n *NodeBase) Time() uint64 {
	return n.localTime.Load()
}

// SetTime mirrors ma_node_set_time.
func (n *NodeBase) SetTime(localTime uint64) error {
	n.localTime.Store(localTime)
	return nil
}

/**************************************************************************
Node graph
**************************************************************************/

// NodeGraphConfig mirrors ma_node_graph_config.
type NodeGraphConfig struct {
	Channels               uint32
	ProcessingSizeInFrames uint32 // 0 = default.
}

// NodeGraphConfigInit mirrors ma_node_graph_config_init.
func NodeGraphConfigInit(channels uint32) NodeGraphConfig {
	return NodeGraphConfig{Channels: channels}
}

// endpointProcessor is the endpoint's passthrough processor.
type endpointProcessor struct{}

func (endpointProcessor) ProcessNode(framesIn [][]float32, frameCountIn uint32, framesOut [][]float32, frameCountOut uint32) (uint32, uint32) {
	n := min(frameCountIn, frameCountOut)
	if len(framesIn) > 0 && framesIn[0] != nil && len(framesOut) > 0 && framesOut[0] != nil {
		copy(framesOut[0][:len(framesIn[0])], framesIn[0])
	}
	return n, n
}

// graphCommandOp enumerates the deferred topology operations.
type graphCommandOp uint8

const (
	graphCmdAttach graphCommandOp = iota
	graphCmdDetach
)

// graphCommand is one deferred topology change, applied by the reading
// thread at a chunk boundary.
type graphCommand struct {
	op  graphCommandOp
	out *nodeOutputBus
	in  *nodeInputBus // Attach target; nil for detach.
}

// NodeGraph mirrors ma_node_graph.
type NodeGraph struct {
	channels uint32
	endpoint *NodeBase

	globalTime atomic.Uint64

	// readMu serializes concurrent readers. Control-plane calls never take
	// it, so they cannot stall the audio thread.
	readMu sync.Mutex

	// Deferred topology commands. Enqueued under cmdMu by the control
	// plane; drained by the reader with TryLock so a preempted enqueuer can
	// delay a change but never block processing.
	cmdMu         sync.Mutex
	commands      []graphCommand
	spareCommands []graphCommand

	// Pool of mix/read scratch buffers. Graph reads recurse through the
	// node tree, so buffers are acquired and released stack-like. Steady
	// state performs no allocations.
	bufPool [][]float32
}

// enqueueCommand queues a topology change for the reading thread.
func (g *NodeGraph) enqueueCommand(c graphCommand) {
	g.cmdMu.Lock()
	g.commands = append(g.commands, c)
	g.cmdMu.Unlock()
}

// applyPendingCommands drains and executes queued topology changes.
// Reader-thread only (under readMu). Uses TryLock: if the control plane is
// mid-enqueue the commands simply apply on the next chunk.
func (g *NodeGraph) applyPendingCommands() {
	if !g.cmdMu.TryLock() {
		return
	}
	cmds := g.commands
	g.commands = g.spareCommands[:0]
	g.cmdMu.Unlock()

	for i := range cmds {
		c := &cmds[i]
		switch c.op {
		case graphCmdAttach:
			g.detachOutput(c.out)
			c.in.attachments = append(c.in.attachments, c.out)
			c.out.attachedTo = c.in
		case graphCmdDetach:
			g.detachOutput(c.out)
		}
		c.out, c.in = nil, nil // Drop references so the spare buffer doesn't pin nodes.
	}
	g.spareCommands = cmds[:0]
}

// NewNodeGraph mirrors ma_node_graph_init.
func NewNodeGraph(config NodeGraphConfig) (*NodeGraph, error) {
	if config.Channels == 0 {
		return nil, ErrInvalidArgs
	}
	g := &NodeGraph{channels: config.Channels}
	endpoint, err := g.NewNode(NodeConfig{
		Processor:      endpointProcessor{},
		Flags:          NodeFlagPassthrough | NodeFlagAllowNullInput,
		InputBusCount:  1,
		OutputBusCount: 1,
		InputChannels:  []uint32{config.Channels},
		OutputChannels: []uint32{config.Channels},
		InitialState:   NodeStateStarted,
	})
	if err != nil {
		return nil, err
	}
	g.endpoint = endpoint
	return g, nil
}

// Endpoint mirrors ma_node_graph_get_endpoint.
func (g *NodeGraph) Endpoint() *NodeBase { return g.endpoint }

// Channels mirrors ma_node_graph_get_channels.
func (g *NodeGraph) Channels() uint32 { return g.channels }

// Time mirrors ma_node_graph_get_time.
func (g *NodeGraph) Time() uint64 {
	return g.globalTime.Load()
}

// SetTime mirrors ma_node_graph_set_time.
func (g *NodeGraph) SetTime(globalTime uint64) error {
	g.globalTime.Store(globalTime)
	return nil
}

// ReadPCMFrames mirrors ma_node_graph_read_pcm_frames: pulls frameCount f32
// frames from the endpoint into dst. Pending topology changes are applied
// at each chunk boundary.
func (g *NodeGraph) ReadPCMFrames(dst []float32, frameCount uint64) (uint64, error) {
	ch := int(g.channels)
	if len(dst) < int(frameCount)*ch {
		return 0, ErrInvalidArgs
	}
	g.readMu.Lock()
	defer g.readMu.Unlock()

	var total uint64
	for total < frameCount {
		g.applyPendingCommands()
		chunk := uint32(min(frameCount-total, nodeCacheCapInFrames))
		out := dst[int(total)*ch : (int(total)+int(chunk))*ch]
		read := g.readOutputBus(&g.endpoint.outputBuses[0], out, chunk)
		if read == 0 {
			// The endpoint produced nothing; output silence to keep the
			// stream going (the graph is a continuous source).
			for i := range out {
				out[i] = 0
			}
			read = chunk
		}
		total += uint64(read)
		g.globalTime.Add(uint64(read))
	}
	return total, nil
}

// readOutputBus reads up to frameCount frames from an output bus,
// processing the node as needed. Returns frames read. Volume is applied.
// Reader-thread only.
func (g *NodeGraph) readOutputBus(out *nodeOutputBus, dst []float32, frameCount uint32) uint32 {
	node := out.node
	ch := int(out.channels)

	if NodeState(node.state.Load()) != NodeStateStarted {
		for i := 0; i < int(frameCount)*ch; i++ {
			dst[i] = 0
		}
		return frameCount
	}

	var total uint32
	for total < frameCount {
		// Serve from the cache first.
		if out.cacheLen > out.cachePos {
			n := min(frameCount-total, out.cacheLen-out.cachePos)
			copy(dst[int(total)*ch:], out.cache[int(out.cachePos)*ch : int(out.cacheLen)*ch][:int(n)*ch])
			out.cachePos += n
			total += n
			continue
		}
		// Cache exhausted: process another chunk into all output bus caches.
		if !g.processNode(node) {
			break
		}
		if out.cacheLen == out.cachePos {
			break // The node produced nothing.
		}
	}

	// Apply the output bus volume through the shared helper so that the SIMD
	// path (when available) is used consistently.
	if volume := math.Float32frombits(out.volume.Load()); volume != 1 {
		ApplyVolumeFactorF32(dst[:int(total)*ch], uint64(int(total)*ch), volume)
	}

	// Pad with silence if the node ran dry.
	for i := int(total) * ch; i < int(frameCount)*ch; i++ {
		dst[i] = 0
	}
	if total < frameCount && node.flags&NodeFlagContinuousProcessing != 0 {
		total = frameCount
	}
	return total
}

// processNode runs one processing chunk for the node, filling the caches of
// all its output buses. Returns false if the node cannot make progress.
// Reader-thread only.
func (g *NodeGraph) processNode(node *NodeBase) bool {
	chunk := uint32(nodeCacheCapInFrames)

	// Gather inputs.
	acquired := node.framesIn[:0]
	var frameCountIn uint32
	anyInput := false
	for i := range node.inputBuses {
		in := &node.inputBuses[i]
		buf := g.acquireBuffer(int(chunk) * int(in.channels))
		read := g.readInputBus(in, buf, chunk)
		if read > 0 {
			anyInput = true
		}
		if read > frameCountIn {
			frameCountIn = read
		}
		acquired = append(acquired, buf)
		node.framesIn[i] = buf[:int(read)*int(in.channels)]
	}

	if !anyInput && len(node.inputBuses) > 0 {
		if node.flags&(NodeFlagContinuousProcessing|NodeFlagAllowNullInput) == 0 {
			for _, buf := range acquired {
				g.releaseBuffer(buf)
			}
			return false // Nothing to process.
		}
		if node.flags&NodeFlagAllowNullInput != 0 {
			for i := range node.framesIn {
				node.framesIn[i] = nil
			}
		} else {
			// Continuous processing: feed silence.
			frameCountIn = chunk
			for i := range node.inputBuses {
				in := &node.inputBuses[i]
				buf := acquired[i][:int(chunk)*int(in.channels)]
				for j := range buf {
					buf[j] = 0
				}
				node.framesIn[i] = buf
			}
		}
	}

	// Prepare output cache space.
	for i := range node.outputBuses {
		out := &node.outputBuses[i]
		out.cachePos = 0
		out.cacheLen = 0
		node.framesOut[i] = out.cache[:int(chunk)*int(out.channels)]
	}

	var produced uint32
	if node.processor != nil {
		_, produced = node.processor.ProcessNode(node.framesIn, frameCountIn, node.framesOut, chunk)
	} else if node.flags&NodeFlagPassthrough != 0 && len(node.framesIn) > 0 && node.framesIn[0] != nil {
		copy(node.framesOut[0], node.framesIn[0])
		produced = frameCountIn
	}

	for _, buf := range acquired {
		g.releaseBuffer(buf)
	}

	for i := range node.outputBuses {
		node.outputBuses[i].cacheLen = produced
		node.outputBuses[i].cachePos = 0
	}
	node.localTime.Add(uint64(produced))
	return produced > 0
}

// readInputBus mixes all attachments of an input bus into dst.
// Reader-thread only.
func (g *NodeGraph) readInputBus(in *nodeInputBus, dst []float32, frameCount uint32) uint32 {
	ch := int(in.channels)

	// Fast path: a single audible attachment (the dominant case) reads
	// straight into dst, skipping the zero-fill and mix-accumulate passes.
	// readOutputBus fully defines dst (it pads the tail with silence).
	if len(in.attachments) == 1 && in.attachments[0].node.flags&NodeFlagSilentOutput == 0 {
		return g.readOutputBus(in.attachments[0], dst, frameCount)
	}

	for i := 0; i < int(frameCount)*ch; i++ {
		dst[i] = 0
	}
	if len(in.attachments) == 0 {
		return 0
	}

	var maxRead uint32
	tmp := g.acquireBuffer(int(frameCount) * ch)
	for _, src := range in.attachments {
		read := g.readOutputBus(src, tmp, frameCount)
		if src.node.flags&NodeFlagSilentOutput != 0 {
			continue
		}
		// Route through MixPCMFramesF32 so the SIMD path is used when available.
		_ = MixPCMFramesF32(dst, tmp, uint64(read), uint32(ch), 1)
		if read > maxRead {
			maxRead = read
		}
	}
	g.releaseBuffer(tmp)
	return maxRead
}

// acquireBuffer pops a scratch buffer from the pool, growing it only when
// the graph shape demands a larger buffer than seen before. Reads recurse
// through the node tree, so usage is stack-like and the pool converges to
// the graph depth after the first read cycle.
func (g *NodeGraph) acquireBuffer(samples int) []float32 {
	for i := len(g.bufPool) - 1; i >= 0; i-- {
		if cap(g.bufPool[i]) >= samples {
			buf := g.bufPool[i][:samples]
			g.bufPool = append(g.bufPool[:i], g.bufPool[i+1:]...)
			return buf
		}
	}
	return make([]float32, samples)
}

func (g *NodeGraph) releaseBuffer(buf []float32) {
	g.bufPool = append(g.bufPool, buf)
}

/**************************************************************************
Built-in nodes
**************************************************************************/

// DataSourceNodeConfig mirrors ma_data_source_node_config.
type DataSourceNodeConfig struct {
	NodeConfig
	DataSource DataSource
	Looping    bool
}

// DataSourceNodeConfigInit mirrors ma_data_source_node_config_init.
func DataSourceNodeConfigInit(ds DataSource) DataSourceNodeConfig {
	return DataSourceNodeConfig{NodeConfig: NodeConfigInit(), DataSource: ds}
}

// DataSourceNode mirrors ma_data_source_node: a node that reads from a
// DataSource. The data source must produce f32 frames.
type DataSourceNode struct {
	*NodeBase
	ds       DataSource
	ctrl     *DataSourceController
	channels uint32
}

// NewDataSourceNode mirrors ma_data_source_node_init.
func (g *NodeGraph) NewDataSourceNode(config DataSourceNodeConfig) (*DataSourceNode, error) {
	if config.DataSource == nil {
		return nil, ErrInvalidArgs
	}
	format, channels, _, _, err := config.DataSource.DataFormat()
	if err != nil {
		return nil, err
	}
	if format != FormatF32 {
		return nil, ErrInvalidArgs // Mirrors miniaudio: data source nodes require f32.
	}

	n := &DataSourceNode{ds: config.DataSource, channels: channels}
	n.ctrl = NewDataSourceController(config.DataSource)
	_ = n.ctrl.SetLooping(config.Looping)

	base, err := g.NewNode(NodeConfig{
		Processor:      n,
		Flags:          NodeFlagAllowNullInput,
		InputBusCount:  0,
		OutputBusCount: 1,
		OutputChannels: []uint32{channels},
		InitialState:   config.InitialState,
	})
	if err != nil {
		return nil, err
	}
	n.NodeBase = base
	base.self = n
	return n, nil
}

// ProcessNode implements NodeProcessor.
func (n *DataSourceNode) ProcessNode(_ [][]float32, _ uint32, framesOut [][]float32, frameCountOut uint32) (uint32, uint32) {
	if len(framesOut) == 0 || framesOut[0] == nil {
		return 0, 0
	}
	read, _ := n.ctrl.Read(f32ToBytes(framesOut[0]), uint64(frameCountOut))
	return 0, uint32(read)
}

// SetLooping mirrors ma_data_source_node_set_looping.
func (n *DataSourceNode) SetLooping(looping bool) error { return n.ctrl.SetLooping(looping) }

// IsLooping mirrors ma_data_source_node_is_looping.
func (n *DataSourceNode) IsLooping() bool { return n.ctrl.IsLooping() }

// SplitterNodeConfig mirrors ma_splitter_node_config.
type SplitterNodeConfig struct {
	NodeConfig
	Channels       uint32
	OutputBusCount uint32
}

// SplitterNodeConfigInit mirrors ma_splitter_node_config_init.
func SplitterNodeConfigInit(channels uint32) SplitterNodeConfig {
	return SplitterNodeConfig{NodeConfig: NodeConfigInit(), Channels: channels, OutputBusCount: 2}
}

// SplitterNode mirrors ma_splitter_node: copies its input to multiple
// outputs.
type SplitterNode struct {
	*NodeBase
	channels uint32
}

// NewSplitterNode mirrors ma_splitter_node_init.
func (g *NodeGraph) NewSplitterNode(config SplitterNodeConfig) (*SplitterNode, error) {
	if config.Channels == 0 || config.OutputBusCount == 0 {
		return nil, ErrInvalidArgs
	}
	n := &SplitterNode{channels: config.Channels}
	outCh := make([]uint32, config.OutputBusCount)
	for i := range outCh {
		outCh[i] = config.Channels
	}
	base, err := g.NewNode(NodeConfig{
		Processor:      n,
		InputBusCount:  1,
		OutputBusCount: config.OutputBusCount,
		InputChannels:  []uint32{config.Channels},
		OutputChannels: outCh,
		InitialState:   config.InitialState,
	})
	if err != nil {
		return nil, err
	}
	n.NodeBase = base
	base.self = n
	return n, nil
}

// ProcessNode implements NodeProcessor.
func (n *SplitterNode) ProcessNode(framesIn [][]float32, frameCountIn uint32, framesOut [][]float32, frameCountOut uint32) (uint32, uint32) {
	if len(framesIn) == 0 || framesIn[0] == nil {
		return 0, 0
	}
	count := min(frameCountIn, frameCountOut)
	for _, out := range framesOut {
		copy(out[:int(count)*int(n.channels)], framesIn[0])
	}
	return count, count
}

// filterNode wraps a byte-oriented filter processor into a 1-in/1-out node.
type filterNode struct {
	*NodeBase
	process  func(dst, src []float32, frameCount uint64) error
	channels uint32
}

func (g *NodeGraph) newFilterNode(channels uint32, initialState NodeState, process func(dst, src []float32, frameCount uint64) error) (*filterNode, error) {
	n := &filterNode{process: process, channels: channels}
	base, err := g.NewNode(NodeConfig{
		Processor:      n,
		InputBusCount:  1,
		OutputBusCount: 1,
		InputChannels:  []uint32{channels},
		OutputChannels: []uint32{channels},
		InitialState:   initialState,
	})
	if err != nil {
		return nil, err
	}
	n.NodeBase = base
	base.self = n
	return n, nil
}

// ProcessNode implements NodeProcessor.
func (n *filterNode) ProcessNode(framesIn [][]float32, frameCountIn uint32, framesOut [][]float32, frameCountOut uint32) (uint32, uint32) {
	if len(framesIn) == 0 || framesIn[0] == nil || len(framesOut) == 0 {
		return 0, 0
	}
	count := min(frameCountIn, frameCountOut)
	if err := n.process(framesOut[0], framesIn[0], uint64(count)); err != nil {
		return 0, 0
	}
	return count, count
}

// BiquadNode mirrors ma_biquad_node.
type BiquadNode struct {
	*filterNode
	Filter *Biquad
}

// BiquadNodeConfig mirrors ma_biquad_node_config.
type BiquadNodeConfig struct {
	NodeConfig
	Biquad BiquadConfig
}

// BiquadNodeConfigInit mirrors ma_biquad_node_config_init.
func BiquadNodeConfigInit(channels uint32, b0, b1, b2, a0, a1, a2 float64) BiquadNodeConfig {
	return BiquadNodeConfig{
		NodeConfig: NodeConfigInit(),
		Biquad:     BiquadConfigInit(FormatF32, channels, b0, b1, b2, a0, a1, a2),
	}
}

// NewBiquadNode mirrors ma_biquad_node_init.
func (g *NodeGraph) NewBiquadNode(config BiquadNodeConfig) (*BiquadNode, error) {
	bq, err := NewBiquad(config.Biquad)
	if err != nil {
		return nil, err
	}
	fn, err := g.newFilterNode(config.Biquad.Channels, config.InitialState, bq.ProcessF32)
	if err != nil {
		return nil, err
	}
	return &BiquadNode{filterNode: fn, Filter: bq}, nil
}

// LPFNode mirrors ma_lpf_node.
type LPFNode struct {
	*filterNode
	Filter *LPF
}

// LPFNodeConfig mirrors ma_lpf_node_config.
type LPFNodeConfig struct {
	NodeConfig
	LPF LPFConfig
}

// LPFNodeConfigInit mirrors ma_lpf_node_config_init.
func LPFNodeConfigInit(channels, sampleRate uint32, cutoffFrequency float64, order uint32) LPFNodeConfig {
	return LPFNodeConfig{
		NodeConfig: NodeConfigInit(),
		LPF:        LPFConfigInit(FormatF32, channels, sampleRate, cutoffFrequency, order),
	}
}

// NewLPFNode mirrors ma_lpf_node_init.
func (g *NodeGraph) NewLPFNode(config LPFNodeConfig) (*LPFNode, error) {
	f, err := NewLPF(config.LPF)
	if err != nil {
		return nil, err
	}
	fn, err := g.newFilterNode(config.LPF.Channels, config.InitialState, f.ProcessF32)
	if err != nil {
		return nil, err
	}
	return &LPFNode{filterNode: fn, Filter: f}, nil
}

// HPFNode mirrors ma_hpf_node.
type HPFNode struct {
	*filterNode
	Filter *HPF
}

// HPFNodeConfig mirrors ma_hpf_node_config.
type HPFNodeConfig struct {
	NodeConfig
	HPF HPFConfig
}

// HPFNodeConfigInit mirrors ma_hpf_node_config_init.
func HPFNodeConfigInit(channels, sampleRate uint32, cutoffFrequency float64, order uint32) HPFNodeConfig {
	return HPFNodeConfig{
		NodeConfig: NodeConfigInit(),
		HPF:        HPFConfigInit(FormatF32, channels, sampleRate, cutoffFrequency, order),
	}
}

// NewHPFNode mirrors ma_hpf_node_init.
func (g *NodeGraph) NewHPFNode(config HPFNodeConfig) (*HPFNode, error) {
	f, err := NewHPF(config.HPF)
	if err != nil {
		return nil, err
	}
	fn, err := g.newFilterNode(config.HPF.Channels, config.InitialState, f.ProcessF32)
	if err != nil {
		return nil, err
	}
	return &HPFNode{filterNode: fn, Filter: f}, nil
}

// BPFNode mirrors ma_bpf_node.
type BPFNode struct {
	*filterNode
	Filter *BPF
}

// BPFNodeConfig mirrors ma_bpf_node_config.
type BPFNodeConfig struct {
	NodeConfig
	BPF BPFConfig
}

// BPFNodeConfigInit mirrors ma_bpf_node_config_init.
func BPFNodeConfigInit(channels, sampleRate uint32, cutoffFrequency float64, order uint32) BPFNodeConfig {
	return BPFNodeConfig{
		NodeConfig: NodeConfigInit(),
		BPF:        BPFConfigInit(FormatF32, channels, sampleRate, cutoffFrequency, order),
	}
}

// NewBPFNode mirrors ma_bpf_node_init.
func (g *NodeGraph) NewBPFNode(config BPFNodeConfig) (*BPFNode, error) {
	f, err := NewBPF(config.BPF)
	if err != nil {
		return nil, err
	}
	fn, err := g.newFilterNode(config.BPF.Channels, config.InitialState, f.ProcessF32)
	if err != nil {
		return nil, err
	}
	return &BPFNode{filterNode: fn, Filter: f}, nil
}

// NotchNode mirrors ma_notch_node.
type NotchNode struct {
	*filterNode
	Filter *Notch2
}

// NotchNodeConfigInit mirrors ma_notch_node_config_init.
func NotchNodeConfigInit(channels, sampleRate uint32, q, frequency float64) NotchConfig {
	return Notch2ConfigInit(FormatF32, channels, sampleRate, q, frequency)
}

// NewNotchNode mirrors ma_notch_node_init.
func (g *NodeGraph) NewNotchNode(config NotchConfig) (*NotchNode, error) {
	f, err := NewNotch2(config)
	if err != nil {
		return nil, err
	}
	fn, err := g.newFilterNode(config.Channels, NodeStateStarted, f.ProcessF32)
	if err != nil {
		return nil, err
	}
	return &NotchNode{filterNode: fn, Filter: f}, nil
}

// PeakNode mirrors ma_peak_node.
type PeakNode struct {
	*filterNode
	Filter *Peak2
}

// PeakNodeConfigInit mirrors ma_peak_node_config_init.
func PeakNodeConfigInit(channels, sampleRate uint32, gainDB, q, frequency float64) PeakConfig {
	return Peak2ConfigInit(FormatF32, channels, sampleRate, gainDB, q, frequency)
}

// NewPeakNode mirrors ma_peak_node_init.
func (g *NodeGraph) NewPeakNode(config PeakConfig) (*PeakNode, error) {
	f, err := NewPeak2(config)
	if err != nil {
		return nil, err
	}
	fn, err := g.newFilterNode(config.Channels, NodeStateStarted, f.ProcessF32)
	if err != nil {
		return nil, err
	}
	return &PeakNode{filterNode: fn, Filter: f}, nil
}

// LoshelfNode mirrors ma_loshelf_node.
type LoshelfNode struct {
	*filterNode
	Filter *Loshelf2
}

// NewLoshelfNode mirrors ma_loshelf_node_init.
func (g *NodeGraph) NewLoshelfNode(config ShelfConfig) (*LoshelfNode, error) {
	f, err := NewLoshelf2(config)
	if err != nil {
		return nil, err
	}
	fn, err := g.newFilterNode(config.Channels, NodeStateStarted, f.ProcessF32)
	if err != nil {
		return nil, err
	}
	return &LoshelfNode{filterNode: fn, Filter: f}, nil
}

// HishelfNode mirrors ma_hishelf_node.
type HishelfNode struct {
	*filterNode
	Filter *Hishelf2
}

// NewHishelfNode mirrors ma_hishelf_node_init.
func (g *NodeGraph) NewHishelfNode(config ShelfConfig) (*HishelfNode, error) {
	f, err := NewHishelf2(config)
	if err != nil {
		return nil, err
	}
	fn, err := g.newFilterNode(config.Channels, NodeStateStarted, f.ProcessF32)
	if err != nil {
		return nil, err
	}
	return &HishelfNode{filterNode: fn, Filter: f}, nil
}

// DelayNode mirrors ma_delay_node.
type DelayNode struct {
	*filterNode
	Effect *Delay
}

// DelayNodeConfigInit mirrors ma_delay_node_config_init.
func DelayNodeConfigInit(channels, sampleRate, delayInFrames uint32, decay float32) DelayConfig {
	return DelayConfigInit(channels, sampleRate, delayInFrames, decay)
}

// NewDelayNode mirrors ma_delay_node_init.
func (g *NodeGraph) NewDelayNode(config DelayConfig) (*DelayNode, error) {
	e, err := NewDelay(config)
	if err != nil {
		return nil, err
	}
	fn, err := g.newFilterNode(config.Channels, NodeStateStarted, e.Process)
	if err != nil {
		return nil, err
	}
	return &DelayNode{filterNode: fn, Effect: e}, nil
}
