//go:build linux && !android

package gominiaudio

import (
	"fmt"
	"runtime"
	"sync"

	"golang.org/x/sys/unix"
)

// PipeWire backend. Talks the PipeWire native protocol directly over the
// daemon's Unix socket (no libpipewire): device enumeration through the
// registry, audio streaming through a client-node with memfd-backed buffers
// and eventfd scheduling.

type pipewireContext struct{}

func (c *pipewireContext) init(*ContextConfig) error {
	// Probe the daemon socket.
	core, err := pwConnect("gominiaudio-probe")
	if err != nil {
		return ErrFailedToInitBackend
	}
	core.close()
	return nil
}

func (c *pipewireContext) uninit() error      { return nil }
func (c *pipewireContext) backendID() Backend { return BackendPipeWire }

func (c *pipewireContext) enumerateDevices() ([]DeviceInfo, []DeviceInfo, error) {
	core, err := pwConnect("gominiaudio-enum")
	if err != nil {
		return nil, nil, err
	}
	defer core.close()

	if err := core.getRegistry(); err != nil {
		return nil, nil, err
	}

	var playback, capture []DeviceInfo
	core.mu.Lock()
	defer core.mu.Unlock()
	for _, g := range core.globals {
		if g.Type != "PipeWire:Interface:Node" {
			continue
		}
		class := g.Props["media.class"]
		if class != "Audio/Sink" && class != "Audio/Source" {
			continue
		}
		name := g.Props["node.description"]
		if name == "" {
			name = g.Props["node.nick"]
		}
		if name == "" {
			name = g.Props["node.name"]
		}
		info := DeviceInfo{
			ID:   deviceIDFromString(g.Props["node.name"]),
			Name: name,
			Formats: []DeviceNativeDataFormat{{
				Format: FormatF32, Channels: 0, SampleRate: 0,
			}},
		}
		if class == "Audio/Sink" {
			playback = append(playback, info)
		} else {
			capture = append(capture, info)
		}
	}
	return playback, capture, nil
}

func (c *pipewireContext) deviceInfo(deviceType DeviceType, id *DeviceID) (DeviceInfo, error) {
	playback, capture, err := c.enumerateDevices()
	if err != nil {
		return DeviceInfo{}, err
	}
	list := playback
	if deviceType == DeviceTypeCapture || deviceType == DeviceTypeLoopback {
		list = capture
	}
	if id == nil || id.IsZero() {
		if len(list) == 0 {
			return DeviceInfo{}, ErrNoDevice
		}
		return list[0], nil
	}
	for _, d := range list {
		if d.ID == *id {
			return d, nil
		}
	}
	return DeviceInfo{}, ErrNoDevice
}

/**************************************************************************
Stream
**************************************************************************/

// pwBufferData is one mapped data plane of a buffer.
type pwBufferData struct {
	data     []byte
	mapping  *pwMapping
	chunkOff int // Offset of this data's spa_chunk within the buffer region.
}

// pwBuffer is one negotiated buffer.
type pwBuffer struct {
	region *pwMapping // The buffer's meta/chunk region.
	datas  []pwBufferData
}

// pwTarget is a peer activation record to signal after processing.
type pwTarget struct {
	fd      int
	mapping *pwMapping
}

// pwStream is one client-node streaming to/from the PipeWire graph.
type pwStream struct {
	device    *Device
	core      *pwCore
	proxyID   uint32
	isCapture bool

	mu sync.Mutex

	channels uint32
	rate     uint32
	stride   int
	period   uint32

	readFd  int
	writeFd int

	activation *pwMapping
	targets    map[uint32]*pwTarget

	ioBuffers *pwMapping // spa_io_buffers.
	position  *pwMapping // spa_io_position (owned by the driver).

	buffers []pwBuffer
	nextBuf uint32

	active   bool
	started  bool
	formatSet bool

	procDone chan struct{}
	scratch  []byte
}

// pwOpenStream creates and configures a client-node for one direction.
func pwOpenStream(d *Device, cfg *DeviceConfig, isCapture, isLoopback bool) (*pwStream, error) {
	channels := cfg.Playback.Channels
	targetID := cfg.Playback.DeviceID
	if isCapture {
		channels = cfg.Capture.Channels
		targetID = cfg.Capture.DeviceID
	}
	if channels == 0 {
		channels = DefaultChannels
	}
	rate := cfg.SampleRate
	if rate == 0 {
		rate = DefaultSampleRate
	}
	period := d.calculatePeriodSizeInFrames(rate)

	s := &pwStream{
		device:    d,
		isCapture: isCapture,
		channels:  channels,
		rate:      rate,
		stride:    int(channels) * 4, // F32.
		period:    period,
		readFd:    -1,
		writeFd:   -1,
		targets:   make(map[uint32]*pwTarget),
		procDone:  make(chan struct{}),
	}

	core, err := pwConnect("gominiaudio")
	if err != nil {
		return nil, err
	}
	s.core = core

	mediaClass := "Stream/Output/Audio"
	category := "Playback"
	if isCapture {
		mediaClass = "Stream/Input/Audio"
		category = "Capture"
	}
	streamName := cfg.PipeWire.StreamNamePlayback
	if isCapture {
		streamName = cfg.PipeWire.StreamNameCapture
	}
	if streamName == "" {
		streamName = "gominiaudio"
	}

	props := pwDict{
		{"media.type", "Audio"},
		{"media.category", category},
		{"media.role", "Music"},
		{"media.class", mediaClass},
		{"node.name", streamName},
		{"node.autoconnect", "true"},
		{"node.want-driver", "true"},
		{"node.latency", fmt.Sprintf("%d/%d", period, rate)},
		{"node.rate", fmt.Sprintf("1/%d", rate)},
	}
	if targetID != nil && !targetID.IsZero() {
		props = append(props, [2]string{"target.object", targetID.String()})
	}
	if isLoopback {
		props = append(props, [2]string{"stream.capture.sink", "true"})
	}

	proxyID, err := core.createClientNode(props)
	if err != nil {
		core.close()
		return nil, err
	}
	s.proxyID = proxyID
	core.setHandler(proxyID, s.handleEvent)

	// Describe the node and its single port.
	if err := s.sendNodeUpdate(); err != nil {
		core.close()
		return nil, err
	}
	if err := s.sendPortUpdate(false); err != nil {
		core.close()
		return nil, err
	}
	if err := s.sendSetActive(false); err != nil {
		core.close()
		return nil, err
	}
	if err := core.roundtrip(); err != nil {
		core.close()
		return nil, err
	}

	go s.processLoop()
	return s, nil
}

// direction returns the SPA direction of the stream's port.
func (s *pwStream) direction() int32 {
	if s.isCapture {
		return spaDirectionInput
	}
	return spaDirectionOutput
}

func (s *pwStream) sendNodeUpdate() error {
	var b podBuilder
	f := b.pushStruct()
	b.addInt(pwClientNodeUpdateInfo)
	b.addInt(0) // n_params
	fi := b.pushStruct()
	maxIn, maxOut := int32(0), int32(1)
	if s.isCapture {
		maxIn, maxOut = 1, 0
	}
	b.addInt(maxIn)
	b.addInt(maxOut)
	b.addLong(spaNodeChangeMaskFlags | spaNodeChangeMaskProps)
	b.addLong(0) // flags
	b.addDict(pwDict{
		{"node.name", "gominiaudio"},
	})
	b.addInt(0) // n_params (param infos)
	b.pop(fi)
	b.pop(f)
	return s.core.conn.send(s.proxyID, pwClientNodeMethodUpdate, b.bytes(), nil)
}

// buildFormatPod builds a Format/EnumFormat object pod for the stream.
func (s *pwStream) buildFormatPod(paramID uint32) []byte {
	var b podBuilder
	f := b.pushObject(spaTypeObjectFormat, paramID)
	b.addProp(spaFormatMediaType, 0)
	b.addID(spaMediaTypeAudio)
	b.addProp(spaFormatMediaSubtype, 0)
	b.addID(spaMediaSubtypeRaw)
	b.addProp(spaFormatAudioFormat, 0)
	b.addID(spaAudioFormatF32LE)
	b.addProp(spaFormatAudioRate, 0)
	b.addInt(int32(s.rate))
	b.addProp(spaFormatAudioChannels, 0)
	b.addInt(int32(s.channels))
	// Channel positions.
	b.addProp(spaFormatAudioPosition, 0)
	positions := pwChannelPositions(s.channels)
	af := podFrame{offset: len(b.buf)}
	b.writeHeader(0, spaTypeArray)
	var hdr [8]byte
	putU32 := func(off int, v uint32) {
		hdr[off] = byte(v)
		hdr[off+1] = byte(v >> 8)
		hdr[off+2] = byte(v >> 16)
		hdr[off+3] = byte(v >> 24)
	}
	putU32(0, 4)         // child size
	putU32(4, spaTypeID) // child type
	b.buf = append(b.buf, hdr[:]...)
	for _, p := range positions {
		var v [4]byte
		v[0] = byte(p)
		v[1] = byte(p >> 8)
		v[2] = byte(p >> 16)
		v[3] = byte(p >> 24)
		b.buf = append(b.buf, v[:]...)
	}
	b.pop(af)
	b.pad(0)
	b.pop(f)
	return b.bytes()
}

func pwChannelPositions(channels uint32) []uint32 {
	switch channels {
	case 1:
		return []uint32{spaAudioChannelMono}
	case 2:
		return []uint32{spaAudioChannelFL, spaAudioChannelFR}
	case 4:
		return []uint32{spaAudioChannelFL, spaAudioChannelFR, spaAudioChannelRL, spaAudioChannelRR}
	case 6:
		return []uint32{spaAudioChannelFL, spaAudioChannelFR, spaAudioChannelFC, spaAudioChannelLFE, spaAudioChannelRL, spaAudioChannelRR}
	default:
		out := make([]uint32, channels)
		for i := range out {
			out[i] = spaAudioChannelMono + uint32(i)
		}
		return out
	}
}

// buildBuffersPod builds the ParamBuffers object.
func (s *pwStream) buildBuffersPod() []byte {
	var b podBuilder
	f := b.pushObject(spaTypeObjectParamBuffers, spaParamBuffers)
	b.addProp(spaParamBuffersBuffers, 0)
	b.addChoiceRangeInt(2, 2, 16)
	b.addProp(spaParamBuffersBlocks, 0)
	b.addInt(1)
	b.addProp(spaParamBuffersSize, 0)
	sz := int32(s.period) * int32(s.stride)
	b.addChoiceRangeInt(sz, sz, sz*8)
	b.addProp(spaParamBuffersStride, 0)
	b.addInt(int32(s.stride))
	b.pop(f)
	return b.bytes()
}

// buildMetaPod builds the ParamMeta object for the header meta.
func (s *pwStream) buildMetaPod() []byte {
	var b podBuilder
	f := b.pushObject(spaTypeObjectParamMeta, spaParamMeta)
	b.addProp(spaParamMetaType, 0)
	b.addID(spaMetaHeader)
	b.addProp(spaParamMetaSize, 0)
	b.addInt(32) // sizeof(spa_meta_header)
	b.pop(f)
	return b.bytes()
}

// buildIOPod builds the ParamIO object for io buffers.
func (s *pwStream) buildIOPod() []byte {
	var b podBuilder
	f := b.pushObject(spaTypeObjectParamIO, spaParamIO)
	b.addProp(spaParamIOID, 0)
	b.addID(spaIOBuffers)
	b.addProp(spaParamIOSize, 0)
	b.addInt(8) // sizeof(spa_io_buffers)
	b.pop(f)
	return b.bytes()
}

// sendPortUpdate describes the port. When withFormat is true, the current
// format plus buffer requirements are included (sent after the server sets
// the format).
func (s *pwStream) sendPortUpdate(withFormat bool) error {
	var params [][]byte
	params = append(params, s.buildFormatPod(spaParamEnumFormat))
	if withFormat {
		params = append(params, s.buildFormatPod(spaParamFormat))
		params = append(params, s.buildBuffersPod())
		params = append(params, s.buildMetaPod())
		params = append(params, s.buildIOPod())
	}

	var b podBuilder
	f := b.pushStruct()
	b.addInt(s.direction())
	b.addInt(0) // port_id
	b.addInt(pwClientNodeUpdateParams | pwClientNodeUpdateInfo)
	b.addInt(int32(len(params)))
	for _, p := range params {
		b.addPod(p)
	}
	fi := b.pushStruct()
	b.addLong(spaPortChangeMaskFlags | spaPortChangeMaskProps | spaPortChangeMaskParams)
	b.addLong(0)          // flags
	b.addInt(1)           // rate num
	b.addInt(int32(s.rate)) // rate denom
	b.addDict(pwDict{
		{"format.dsp", "32 bit float mono audio"},
	})
	// Param infos.
	type paramInfo struct {
		id    uint32
		flags uint32
	}
	infos := []paramInfo{
		{spaParamEnumFormat, spaParamInfoReadWrite},
		{spaParamMeta, spaParamInfoRead},
		{spaParamIO, spaParamInfoRead},
		{spaParamFormat, spaParamInfoReadWrite},
		{spaParamBuffers, spaParamInfoRead},
	}
	b.addInt(int32(len(infos)))
	for _, pi := range infos {
		b.addID(pi.id)
		b.addInt(int32(pi.flags))
	}
	b.pop(fi)
	b.pop(f)
	return s.core.conn.send(s.proxyID, pwClientNodeMethodPortUpdate, b.bytes(), nil)
}

func (s *pwStream) sendSetActive(active bool) error {
	var b podBuilder
	f := b.pushStruct()
	b.addBool(active)
	b.pop(f)
	return s.core.conn.send(s.proxyID, pwClientNodeMethodSetActive, b.bytes(), nil)
}

/**************************************************************************
Event handling
**************************************************************************/

func (s *pwStream) handleEvent(opcode uint8, msg *pwMessage) {
	switch opcode {
	case pwClientNodeEventTransport:
		s.onTransport(msg)
	case pwClientNodeEventSetIO:
		s.onSetIO(msg)
	case pwClientNodeEventPortSetParam:
		s.onPortSetParam(msg)
	case pwClientNodeEventUseBuffers:
		s.onUseBuffers(msg)
	case pwClientNodeEventPortSetIO:
		s.onPortSetIO(msg)
	case pwClientNodeEventSetActivation:
		s.onSetActivation(msg)
	case pwClientNodeEventCommand:
		s.onCommand(msg)
	default:
		for _, fd := range msg.Fds {
			unix.Close(fd)
		}
	}
}

func (s *pwStream) onTransport(msg *pwMessage) {
	v, _, ok := parsePod(msg.Data)
	if !ok {
		return
	}
	p := v.structParser()
	readIdx, _ := p.nextFd()
	writeIdx, _ := p.nextFd()
	memID, _ := p.nextInt()
	offset, _ := p.nextInt()
	size, _ := p.nextInt()

	// Map outside the lock: mmap syscalls on the socket thread must not
	// stall the real-time process loop, which contends on s.mu.
	mapping, mapErr := s.core.mapMemory(uint32(memID), offset, size, true)

	s.mu.Lock()
	if int(readIdx) < len(msg.Fds) {
		s.readFd = msg.Fds[readIdx]
	}
	if int(writeIdx) < len(msg.Fds) {
		s.writeFd = msg.Fds[writeIdx]
	}
	var old *pwMapping
	if mapErr == nil {
		old = s.activation
		s.activation = mapping
	}
	s.mu.Unlock()

	if old != nil {
		old.unmap()
	}
}

func (s *pwStream) onSetIO(msg *pwMessage) {
	v, _, ok := parsePod(msg.Data)
	if !ok {
		return
	}
	p := v.structParser()
	id, _ := p.nextInt()
	memID, _ := p.nextInt()
	offset, _ := p.nextInt()
	size, _ := p.nextInt()

	if uint32(id) != spaIOPosition {
		return
	}

	// Map outside the lock; swap under it; unmap the old region after.
	var mapping *pwMapping
	if uint32(memID) != 0xFFFFFFFF {
		mapping, _ = s.core.mapMemory(uint32(memID), offset, size, false)
	}

	s.mu.Lock()
	old := s.position
	s.position = mapping
	s.mu.Unlock()

	if old != nil {
		old.unmap()
	}
}

func (s *pwStream) onPortSetParam(msg *pwMessage) {
	v, _, ok := parsePod(msg.Data)
	if !ok {
		return
	}
	p := v.structParser()
	p.nextInt() // direction
	p.nextInt() // port_id
	id, _ := p.nextInt()
	p.nextInt() // flags
	param, hasParam := p.next()

	if uint32(id) != spaParamFormat {
		return
	}

	s.mu.Lock()
	if hasParam && param.Type == spaTypeObject {
		// Extract the negotiated rate/channels.
		_, _, props, ok := param.parseObject()
		if ok {
			for _, prop := range props {
				switch prop.Key {
				case spaFormatAudioRate:
					if r, ok := prop.Value.valueInt(); ok && r > 0 {
						s.rate = uint32(r)
					}
				case spaFormatAudioChannels:
					if c, ok := prop.Value.valueInt(); ok && c > 0 {
						s.channels = uint32(c)
						s.stride = int(c) * 4
					}
				}
			}
		}
		s.formatSet = true
	} else {
		s.formatSet = false
	}
	s.mu.Unlock()

	// Publish the format and buffer requirements.
	_ = s.sendPortUpdate(s.formatSet)
}

func (s *pwStream) onUseBuffers(msg *pwMessage) {
	v, _, ok := parsePod(msg.Data)
	if !ok {
		return
	}
	p := v.structParser()
	p.nextInt() // direction
	p.nextInt() // port_id
	p.nextInt() // mix_id
	p.nextInt() // flags
	nBuffers, _ := p.nextInt()

	// Parse and map the entire new buffer set outside the lock, then swap
	// it in with a brief critical section. Buffer renegotiation performs
	// many mmap syscalls; holding s.mu across them would stall the
	// real-time process loop.
	var newBuffers []pwBuffer
	for i := int32(0); i < nBuffers; i++ {
		memID, _ := p.nextInt()
		offset, _ := p.nextInt()
		size, _ := p.nextInt()
		var buf pwBuffer
		region, err := s.core.mapMemory(uint32(memID), offset, size, true)
		if err != nil {
			releaseBuffers(newBuffers)
			return
		}
		buf.region = region

		metaOff := 0
		nMetas, _ := p.nextInt()
		for m := int32(0); m < nMetas; m++ {
			p.nextInt() // meta type
			msize, _ := p.nextInt()
			metaOff += align8(int(msize))
		}
		nDatas, _ := p.nextInt()
		chunkBase := metaOff
		for dIdx := int32(0); dIdx < nDatas; dIdx++ {
			dtype, _ := p.nextInt()
			dataRef, _ := p.nextInt()
			p.nextInt() // flags
			mapOffset, _ := p.nextInt()
			maxSize, _ := p.nextInt()

			bd := pwBufferData{chunkOff: chunkBase + int(dIdx)*16}
			switch uint32(dtype) {
			case spaDataMemID:
				if m, err := s.core.mapMemory(uint32(dataRef), mapOffset, maxSize, !s.isCapture); err == nil {
					bd.mapping = m
					bd.data = m.data
				}
			case spaDataMemFd:
				if int(dataRef) < len(msg.Fds) {
					prot := unix.PROT_READ
					if !s.isCapture {
						prot |= unix.PROT_WRITE
					}
					if data, err := unix.Mmap(msg.Fds[dataRef], int64(mapOffset), int(maxSize), prot, unix.MAP_SHARED); err == nil {
						bd.mapping = &pwMapping{data: data}
						bd.data = data
					}
				}
			}
			buf.datas = append(buf.datas, bd)
		}
		newBuffers = append(newBuffers, buf)
	}

	s.mu.Lock()
	old := s.buffers
	s.buffers = newBuffers
	s.nextBuf = 0
	s.mu.Unlock()

	releaseBuffers(old)
}

// releaseBuffers unmaps a buffer set. Must not be called while the process
// loop can still reference the buffers.
func releaseBuffers(buffers []pwBuffer) {
	for i := range buffers {
		for j := range buffers[i].datas {
			if buffers[i].datas[j].mapping != nil {
				buffers[i].datas[j].mapping.unmap()
			}
		}
		if buffers[i].region != nil {
			buffers[i].region.unmap()
		}
	}
}

func (s *pwStream) onPortSetIO(msg *pwMessage) {
	v, _, ok := parsePod(msg.Data)
	if !ok {
		return
	}
	p := v.structParser()
	p.nextInt() // direction
	p.nextInt() // port_id
	p.nextInt() // mix_id
	id, _ := p.nextInt()
	memID, _ := p.nextInt()
	offset, _ := p.nextInt()
	size, _ := p.nextInt()

	if uint32(id) != spaIOBuffers {
		return
	}

	// Map outside the lock; swap under it; unmap the old region after.
	var mapping *pwMapping
	if uint32(memID) != 0xFFFFFFFF {
		mapping, _ = s.core.mapMemory(uint32(memID), offset, size, true)
	}

	s.mu.Lock()
	old := s.ioBuffers
	s.ioBuffers = mapping
	s.mu.Unlock()

	if old != nil {
		old.unmap()
	}
}

func (s *pwStream) onSetActivation(msg *pwMessage) {
	v, _, ok := parsePod(msg.Data)
	if !ok {
		return
	}
	p := v.structParser()
	nodeID, _ := p.nextInt()
	sigIdx, _ := p.nextFd()
	memID, _ := p.nextInt()
	offset, _ := p.nextInt()
	size, _ := p.nextInt()

	if uint32(memID) == 0xFFFFFFFF {
		// Removal: detach under the lock, release resources outside it.
		s.mu.Lock()
		t := s.targets[uint32(nodeID)]
		delete(s.targets, uint32(nodeID))
		s.mu.Unlock()
		if t != nil {
			if t.mapping != nil {
				t.mapping.unmap()
			}
			unix.Close(t.fd)
		}
		return
	}
	if int(sigIdx) >= len(msg.Fds) {
		return
	}

	// Map outside the lock.
	m, err := s.core.mapMemory(uint32(memID), offset, size, true)
	if err != nil {
		unix.Close(msg.Fds[sigIdx])
		return
	}

	s.mu.Lock()
	old := s.targets[uint32(nodeID)]
	s.targets[uint32(nodeID)] = &pwTarget{fd: msg.Fds[sigIdx], mapping: m}
	s.mu.Unlock()

	if old != nil {
		if old.mapping != nil {
			old.mapping.unmap()
		}
		unix.Close(old.fd)
	}
}

func (s *pwStream) onCommand(msg *pwMessage) {
	v, _, ok := parsePod(msg.Data)
	if !ok {
		return
	}
	p := v.structParser()
	cmd, ok := p.next()
	if !ok || cmd.Type != spaTypeObject {
		return
	}
	_, cmdID, _, _ := cmd.parseObject()
	s.mu.Lock()
	switch cmdID {
	case spaNodeCommandStart:
		s.started = true
	case spaNodeCommandPause, spaNodeCommandSuspend:
		s.started = false
	}
	s.mu.Unlock()
}

/**************************************************************************
Real-time processing
**************************************************************************/

// quantumFrames reads the current cycle size from the driver's position.
func (s *pwStream) quantumFrames() uint32 {
	if s.position != nil && len(s.position.data) >= clockOffDuration+8 {
		if d := loadU64(s.position.data, clockOffDuration); d > 0 && d < 1<<20 {
			return uint32(d)
		}
	}
	return s.period
}

// processLoop waits on the node eventfd and services one cycle per wake.
func (s *pwStream) processLoop() {
	defer close(s.procDone)
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	pwTryRealtime()

	var eventBuf [8]byte
	for {
		s.mu.Lock()
		fd := s.readFd
		s.mu.Unlock()
		if fd < 0 {
			// Transport not yet established; poll for it.
			if s.core.closed.Load() {
				return
			}
			var ts unix.Timespec
			ts.Sec = 0
			ts.Nsec = int64(10 * 1000 * 1000)
			unix.Nanosleep(&ts, nil)
			continue
		}
		n, err := unix.Read(fd, eventBuf[:])
		if err == unix.EINTR {
			continue
		}
		if err != nil || n == 0 {
			return
		}
		s.processCycle()
	}
}

func (s *pwStream) processCycle() {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.activation != nil && len(s.activation.data) >= activationOffAwakeTime+8 {
		atomicStoreU32(s.activation.data, activationOffStatus, pwNodeActivationAwake)
		atomicStoreU64(s.activation.data, activationOffAwakeTime, uint64(nanotime()))
	}

	if s.started && s.active && s.ioBuffers != nil && len(s.buffers) > 0 {
		io := s.ioBuffers.data
		status := atomicLoadI32(io, 0)
		bufferID := atomicLoadU32(io, 4)

		if s.isCapture {
			if status == spaStatusHaveData && bufferID < uint32(len(s.buffers)) {
				s.consumeBuffer(bufferID)
				atomicStoreU32(io, 4, bufferID)
				atomicStoreI32(io, 0, spaStatusNeedData)
			}
		} else {
			if status != spaStatusHaveData {
				id := bufferID
				if id >= uint32(len(s.buffers)) {
					id = s.nextBuf
					s.nextBuf = (s.nextBuf + 1) % uint32(len(s.buffers))
				}
				s.fillBuffer(id)
				atomicStoreU32(io, 4, id)
				atomicStoreI32(io, 0, spaStatusHaveData)
			}
		}
	}

	if s.activation != nil && len(s.activation.data) >= activationOffFinishTime+8 {
		atomicStoreU32(s.activation.data, activationOffStatus, pwNodeActivationFinished)
		atomicStoreU64(s.activation.data, activationOffFinishTime, uint64(nanotime()))
	}

	// Signal peers: decrement their pending counters and wake those that
	// reach zero.
	one := [8]byte{1}
	for _, t := range s.targets {
		if t.mapping == nil || len(t.mapping.data) < activationOffState0Pending+4 {
			continue
		}
		if atomicAddI32(t.mapping.data, activationOffState0Pending, -1) == 0 {
			unix.Write(t.fd, one[:])
		}
	}
}

// fillBuffer produces one cycle of playback audio into the buffer.
func (s *pwStream) fillBuffer(id uint32) {
	buf := &s.buffers[id]
	if len(buf.datas) == 0 || buf.datas[0].data == nil {
		return
	}
	frames := s.quantumFrames()
	maxFrames := uint32(len(buf.datas[0].data) / s.stride)
	if frames > maxFrames {
		frames = maxFrames
	}
	out := buf.datas[0].data[:int(frames)*s.stride]
	s.device.handlePlayback(out, frames)

	// Write the chunk: offset, size, stride, flags.
	if buf.region != nil {
		chunk := buf.region.data[buf.datas[0].chunkOff:]
		if len(chunk) >= 16 {
			atomicStoreU32(chunk, 0, 0)
			atomicStoreU32(chunk, 4, frames*uint32(s.stride))
			atomicStoreI32(chunk, 8, int32(s.stride))
			atomicStoreI32(chunk, 12, 0)
		}
	}
}

// consumeBuffer delivers one cycle of captured audio to the device layer.
func (s *pwStream) consumeBuffer(id uint32) {
	buf := &s.buffers[id]
	if len(buf.datas) == 0 || buf.datas[0].data == nil || buf.region == nil {
		return
	}
	chunk := buf.region.data[buf.datas[0].chunkOff:]
	if len(chunk) < 16 {
		return
	}
	offset := loadU32(chunk, 0)
	size := loadU32(chunk, 4)
	if size == 0 {
		return
	}
	if int(offset)+int(size) > len(buf.datas[0].data) {
		size = uint32(len(buf.datas[0].data)) - offset
	}
	frames := size / uint32(s.stride)
	s.device.handleCapture(buf.datas[0].data[offset:offset+frames*uint32(s.stride)], frames)
}

func (s *pwStream) setActive(active bool) error {
	s.mu.Lock()
	s.active = active
	s.mu.Unlock()
	return s.sendSetActive(active)
}

func (s *pwStream) close() {
	s.core.close()
	<-s.procDone

	s.mu.Lock()
	defer s.mu.Unlock()
	if s.readFd >= 0 {
		unix.Close(s.readFd)
		s.readFd = -1
	}
	if s.writeFd >= 0 && s.writeFd != s.readFd {
		unix.Close(s.writeFd)
		s.writeFd = -1
	}
	for _, t := range s.targets {
		if t.mapping != nil {
			t.mapping.unmap()
		}
		unix.Close(t.fd)
	}
	s.targets = nil
	for i := range s.buffers {
		for j := range s.buffers[i].datas {
			if s.buffers[i].datas[j].mapping != nil {
				s.buffers[i].datas[j].mapping.unmap()
			}
		}
		if s.buffers[i].region != nil {
			s.buffers[i].region.unmap()
		}
	}
	s.buffers = nil
	if s.ioBuffers != nil {
		s.ioBuffers.unmap()
		s.ioBuffers = nil
	}
	if s.position != nil {
		s.position.unmap()
		s.position = nil
	}
	if s.activation != nil {
		s.activation.unmap()
		s.activation = nil
	}
}

// pwTryRealtime attempts to enable realtime scheduling for the calling
// thread. Failure is fine: it requires elevated limits or rtkit.
func pwTryRealtime() {
	attr := unix.SchedAttr{
		Size:     unix.SizeofSchedAttr,
		Policy:   unix.SCHED_RR,
		Priority: 20,
	}
	_ = unix.SchedSetAttr(0, &attr, 0)
}

/**************************************************************************
Device backend
**************************************************************************/

type pipewireDevice struct {
	device   *Device
	playback *pwStream
	capture  *pwStream
}

func (c *pipewireContext) newDevice(d *Device, config *DeviceConfig) (deviceBackend, error) {
	pd := &pipewireDevice{device: d}

	cleanup := func() {
		if pd.playback != nil {
			pd.playback.close()
		}
		if pd.capture != nil {
			pd.capture.close()
		}
	}

	if d.deviceType&DeviceTypePlayback != 0 {
		s, err := pwOpenStream(d, config, false, false)
		if err != nil {
			return nil, err
		}
		pd.playback = s
		d.setInternalFormat(DeviceTypePlayback, FormatF32, s.channels, s.rate, nil, s.period, config.Periods, "PipeWire Playback")
	}
	if d.deviceType&DeviceTypeCapture != 0 || d.deviceType == DeviceTypeLoopback {
		s, err := pwOpenStream(d, config, true, d.deviceType == DeviceTypeLoopback)
		if err != nil {
			cleanup()
			return nil, err
		}
		pd.capture = s
		d.setInternalFormat(DeviceTypeCapture, FormatF32, s.channels, s.rate, nil, s.period, config.Periods, "PipeWire Capture")
	}
	return pd, nil
}

func (pd *pipewireDevice) start() error {
	if pd.capture != nil {
		if err := pd.capture.setActive(true); err != nil {
			return ErrFailedToStartBackendDevice
		}
	}
	if pd.playback != nil {
		if err := pd.playback.setActive(true); err != nil {
			return ErrFailedToStartBackendDevice
		}
	}
	return nil
}

func (pd *pipewireDevice) stop() error {
	var err error
	if pd.playback != nil {
		if e := pd.playback.setActive(false); e != nil {
			err = e
		}
	}
	if pd.capture != nil {
		if e := pd.capture.setActive(false); e != nil {
			err = e
		}
	}
	return err
}

func (pd *pipewireDevice) uninit() error {
	if pd.playback != nil {
		pd.playback.close()
		pd.playback = nil
	}
	if pd.capture != nil {
		pd.capture.close()
		pd.capture = nil
	}
	return nil
}
