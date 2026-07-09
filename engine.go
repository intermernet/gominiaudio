package gominiaudio

import (
	"math"
	"sync"
)

// EngineConfig mirrors ma_engine_config.
type EngineConfig struct {
	Context                  *Context  // Optional. When nil and NoDevice is false, a context is created and owned by the engine.
	PlaybackDeviceID         *DeviceID // Optional. The playback device to use; nil for the default.
	Channels                 uint32    // 0 = use the device's native channel count.
	SampleRate               uint32    // 0 = use the device's native sample rate.
	PeriodSizeInFrames       uint32
	PeriodSizeInMilliseconds uint32
	GainSmoothTimeInFrames   uint32 // Smoothing time for volume changes. Defaults to 8ms.
	ListenerCount            uint32 // 1..MaxListeners. Defaults to 1.
	NoAutoStart              bool   // When true, the engine does not start the device on init.
	NoDevice                 bool   // When true, no device is created; pull audio with ReadPCMFrames.
}

// MaxListeners mirrors MA_ENGINE_MAX_LISTENERS.
const MaxListeners = 4

// EngineConfigInit mirrors ma_engine_config_init.
func EngineConfigInit() EngineConfig {
	return EngineConfig{ListenerCount: 1}
}

// Engine mirrors ma_engine: a high-level API built on the node graph. The
// engine is itself a node graph; its endpoint feeds the playback device.
type Engine struct {
	*NodeGraph

	mu                     sync.Mutex
	ctx                    *Context
	device                 *Device
	ownsContext            bool
	sampleRate             uint32
	gainSmoothTimeInFrames uint32

	listeners []*SpatializerListener

	// Fire-and-forget sounds from Play(), reaped when finished.
	inlinedSounds []*Sound
}

// NewEngine mirrors ma_engine_init. Pass nil for a default configuration.
func NewEngine(config *EngineConfig) (*Engine, error) {
	cfg := EngineConfigInit()
	if config != nil {
		cfg = *config
		if cfg.ListenerCount == 0 {
			cfg.ListenerCount = 1
		}
	}
	if cfg.ListenerCount > MaxListeners {
		return nil, ErrInvalidArgs
	}

	e := &Engine{}

	channels := cfg.Channels
	sampleRate := cfg.SampleRate

	if !cfg.NoDevice {
		ctx := cfg.Context
		if ctx == nil {
			var err error
			ctx, err = InitContext(nil, nil)
			if err != nil {
				return nil, err
			}
			e.ownsContext = true
		}
		e.ctx = ctx

		devCfg := DeviceConfigInit(DeviceTypePlayback)
		devCfg.Playback.DeviceID = cfg.PlaybackDeviceID
		devCfg.Playback.Format = FormatF32
		devCfg.Playback.Channels = channels
		devCfg.SampleRate = sampleRate
		devCfg.PeriodSizeInFrames = cfg.PeriodSizeInFrames
		devCfg.PeriodSizeInMilliseconds = cfg.PeriodSizeInMilliseconds

		dev, err := InitDevice(ctx, devCfg, DeviceCallbacks{
			Data: func(_ *Device, out, _ []byte, frames uint32) {
				e.readCallback(out, frames)
			},
		})
		if err != nil {
			if e.ownsContext {
				ctx.Uninit()
			}
			return nil, err
		}
		e.device = dev
		_, channels = dev.PlaybackFormat()
		sampleRate = dev.SampleRate()
	}

	if channels == 0 {
		channels = DefaultChannels
	}
	if sampleRate == 0 {
		sampleRate = DefaultSampleRate
	}
	e.sampleRate = sampleRate

	if cfg.GainSmoothTimeInFrames == 0 {
		cfg.GainSmoothTimeInFrames = sampleRate / 125 // 8ms.
	}
	e.gainSmoothTimeInFrames = cfg.GainSmoothTimeInFrames

	graph, err := NewNodeGraph(NodeGraphConfigInit(channels))
	if err != nil {
		e.teardownDevice()
		return nil, err
	}
	e.NodeGraph = graph

	for i := uint32(0); i < cfg.ListenerCount; i++ {
		l, err := NewSpatializerListener(SpatializerListenerConfigInit(channels))
		if err != nil {
			e.teardownDevice()
			return nil, err
		}
		e.listeners = append(e.listeners, l)
	}

	if !cfg.NoDevice && !cfg.NoAutoStart {
		if err := e.Start(); err != nil {
			e.teardownDevice()
			return nil, err
		}
	}
	return e, nil
}

func (e *Engine) teardownDevice() {
	if e.device != nil {
		e.device.Uninit()
		e.device = nil
	}
	if e.ownsContext && e.ctx != nil {
		e.ctx.Uninit()
		e.ctx = nil
	}
}

// readCallback is the device data callback: it pulls from the node graph.
func (e *Engine) readCallback(out []byte, frames uint32) {
	_, _ = e.ReadPCMFrames(bytesToF32(out), uint64(frames))
	e.reapInlinedSounds()
}

// reapInlinedSounds removes finished fire-and-forget sounds.
// It swaps the slice under the lock, then does the cleanup work outside the
// lock so the audio thread is not blocked for the full iteration.
func (e *Engine) reapInlinedSounds() {
	e.mu.Lock()
	toReap := e.inlinedSounds
	e.inlinedSounds = e.inlinedSounds[:0]
	e.mu.Unlock()

	kept := toReap[:0]
	for _, s := range toReap {
		if s.AtEnd() {
			_ = s.node.DetachAllOutputBuses()
			s.close()
		} else {
			kept = append(kept, s)
		}
	}

	if len(kept) > 0 {
		e.mu.Lock()
		e.inlinedSounds = append(kept, e.inlinedSounds...)
		e.mu.Unlock()
	}
}

// Uninit mirrors ma_engine_uninit.
func (e *Engine) Uninit() {
	e.teardownDevice()
}

// Start mirrors ma_engine_start.
func (e *Engine) Start() error {
	if e.device == nil {
		return ErrInvalidOperation
	}
	return e.device.Start()
}

// Stop mirrors ma_engine_stop.
func (e *Engine) Stop() error {
	if e.device == nil {
		return ErrInvalidOperation
	}
	return e.device.Stop()
}

// Device mirrors ma_engine_get_device.
func (e *Engine) Device() *Device { return e.device }

// SampleRate mirrors ma_engine_get_sample_rate.
func (e *Engine) SampleRate() uint32 { return e.sampleRate }

// SetVolume mirrors ma_engine_set_volume.
func (e *Engine) SetVolume(volume float32) error {
	return e.Endpoint().SetOutputBusVolume(0, volume)
}

// Volume mirrors ma_engine_get_volume.
func (e *Engine) Volume() float32 {
	return e.Endpoint().OutputBusVolume(0)
}

// SetGainDB mirrors ma_engine_set_gain_db.
func (e *Engine) SetGainDB(gainDB float32) error {
	return e.SetVolume(VolumeDBToLinear(gainDB))
}

// GainDB mirrors ma_engine_get_gain_db.
func (e *Engine) GainDB() float32 { return VolumeLinearToDB(e.Volume()) }

// ListenerCount mirrors ma_engine_get_listener_count.
func (e *Engine) ListenerCount() uint32 { return uint32(len(e.listeners)) }

// Listener returns the given listener for direct manipulation.
func (e *Engine) Listener(index uint32) *SpatializerListener {
	if int(index) >= len(e.listeners) {
		return nil
	}
	return e.listeners[index]
}

// FindClosestListener mirrors ma_engine_find_closest_listener.
func (e *Engine) FindClosestListener(x, y, z float32) uint32 {
	best := uint32(0)
	bestDist := float32(math.MaxFloat32)
	p := Vec3{x, y, z}
	for i, l := range e.listeners {
		if !l.IsEnabled() {
			continue
		}
		d := l.Position().sub(p).len()
		if d < bestDist {
			bestDist = d
			best = uint32(i)
		}
	}
	return best
}

// Listener manipulation, mirroring ma_engine_listener_*.

func (e *Engine) SetListenerPosition(index uint32, x, y, z float32) {
	if l := e.Listener(index); l != nil {
		l.SetPosition(x, y, z)
	}
}
func (e *Engine) SetListenerDirection(index uint32, x, y, z float32) {
	if l := e.Listener(index); l != nil {
		l.SetDirection(x, y, z)
	}
}
func (e *Engine) SetListenerVelocity(index uint32, x, y, z float32) {
	if l := e.Listener(index); l != nil {
		l.SetVelocity(x, y, z)
	}
}
func (e *Engine) SetListenerCone(index uint32, innerAngleInRadians, outerAngleInRadians, outerGain float32) {
	if l := e.Listener(index); l != nil {
		l.SetCone(innerAngleInRadians, outerAngleInRadians, outerGain)
	}
}
func (e *Engine) SetListenerWorldUp(index uint32, x, y, z float32) {
	if l := e.Listener(index); l != nil {
		l.SetWorldUp(x, y, z)
	}
}
func (e *Engine) SetListenerEnabled(index uint32, enabled bool) {
	if l := e.Listener(index); l != nil {
		l.SetEnabled(enabled)
	}
}

// Time mirrors ma_engine_get_time_in_pcm_frames (via the node graph global
// time).

// TimeInMilliseconds mirrors ma_engine_get_time_in_milliseconds.
func (e *Engine) TimeInMilliseconds() uint64 {
	return e.Time() * 1000 / uint64(e.sampleRate)
}

// Play mirrors ma_engine_play_sound: fire-and-forget playback of a file.
func (e *Engine) Play(path string, group *SoundGroup) error {
	s, err := e.NewSoundFromFile(path, 0, group)
	if err != nil {
		return err
	}
	if err := s.Start(); err != nil {
		s.close()
		return err
	}
	e.mu.Lock()
	e.inlinedSounds = append(e.inlinedSounds, s)
	e.mu.Unlock()
	return nil
}

/**************************************************************************
Engine node (shared by Sound and SoundGroup)
**************************************************************************/

// SoundFlags mirrors ma_sound_flags.
type SoundFlags uint32

const (
	// SoundFlagStream streams the sound from its source instead of fully
	// decoding it into memory.
	SoundFlagStream SoundFlags = 1 << 0
	// SoundFlagDecode fully decodes the sound into memory at load time.
	SoundFlagDecode SoundFlags = 1 << 1
	// SoundFlagAsync is accepted for API parity; loading is synchronous in
	// this implementation.
	SoundFlagAsync SoundFlags = 1 << 2
	// SoundFlagNoDefaultAttachment leaves the sound unattached; attach its
	// node manually.
	SoundFlagNoDefaultAttachment SoundFlags = 1 << 12
	// SoundFlagNoPitch disables pitching for a small performance gain.
	SoundFlagNoPitch SoundFlags = 1 << 13
	// SoundFlagNoSpatialization disables 3D spatialization.
	SoundFlagNoSpatialization SoundFlags = 1 << 14
)

// engineNode is the processing core shared by sounds and groups, mirroring
// ma_engine_node.
type engineNode struct {
	*NodeBase
	engine *Engine

	mu sync.Mutex

	fader  *Fader
	panner *Panner
	spat   *Spatializer
	gainer *Gainer

	pitch          float32
	pitchResampler *LinearResampler // nil when pitching is disabled.
	dopplerPitch   float32

	isSpatial           bool
	pinnedListenerIndex int32 // -1 = closest.

	channelsIn  uint32
	channelsOut uint32

	// For sounds: the data source; nil for groups.
	ds *DataSourceController

	atEnd      bool
	endFn      func(*Sound)
	endFnOwner *Sound

	// Scratch buffers, preallocated/grown on the audio path only when the
	// graph shape changes.
	srcScratch []float32
	midScratch []float32
}

func (e *Engine) newEngineNode(channelsIn uint32, flags SoundFlags, ds *DataSourceController, inputBusCount uint32) (*engineNode, error) {
	channelsOut := e.Channels()
	if channelsIn == 0 {
		channelsIn = channelsOut
	}

	n := &engineNode{
		engine:              e,
		pitch:               1,
		dopplerPitch:        1,
		channelsIn:          channelsIn,
		channelsOut:         channelsOut,
		ds:                  ds,
		pinnedListenerIndex: -1,
		isSpatial:           flags&SoundFlagNoSpatialization == 0,
	}

	var err error
	if n.fader, err = NewFader(FaderConfigInit(FormatF32, channelsOut, e.sampleRate)); err != nil {
		return nil, err
	}
	if n.panner, err = NewPanner(PannerConfigInit(FormatF32, channelsOut)); err != nil {
		return nil, err
	}
	if n.gainer, err = NewGainer(GainerConfigInit(channelsOut, e.gainSmoothTimeInFrames)); err != nil {
		return nil, err
	}
	spatCfg := SpatializerConfigInit(channelsIn, channelsOut)
	spatCfg.GainSmoothTimeInFrames = e.gainSmoothTimeInFrames
	if !n.isSpatial {
		spatCfg.AttenuationModel = AttenuationModelNone
	}
	if n.spat, err = NewSpatializer(spatCfg); err != nil {
		return nil, err
	}
	if flags&SoundFlagNoPitch == 0 {
		rc := LinearResamplerConfigInit(FormatF32, channelsIn, e.sampleRate, e.sampleRate)
		rc.LPFOrder = 0 // Pitching uses raw linear interpolation, like miniaudio.
		if n.pitchResampler, err = NewLinearResampler(rc); err != nil {
			return nil, err
		}
	}

	// Pre-allocate scratch buffers to cover typical period sizes (~85 ms at
	// 48 kHz).  growScratch still enlarges them on demand, but this
	// eliminates GC pressure during audio-thread warmup.
	const preAllocFrames = 4096
	n.srcScratch = make([]float32, preAllocFrames*int(channelsIn))
	n.midScratch = make([]float32, preAllocFrames*int(channelsIn))

	inputChannels := []uint32(nil)
	if inputBusCount > 0 {
		inputChannels = []uint32{channelsIn}
	}
	base, err := e.NewNode(NodeConfig{
		Processor:      n,
		Flags:          NodeFlagContinuousProcessing,
		InputBusCount:  inputBusCount,
		OutputBusCount: 1,
		InputChannels:  inputChannels,
		OutputChannels: []uint32{channelsOut},
		InitialState:   NodeStateStopped,
	})
	if err != nil {
		return nil, err
	}
	n.NodeBase = base
	base.self = n
	return n, nil
}

// currentListener resolves the listener to spatialize against.
func (n *engineNode) currentListener() *SpatializerListener {
	if n.pinnedListenerIndex >= 0 {
		return n.engine.Listener(uint32(n.pinnedListenerIndex))
	}
	p := n.spat.Position()
	return n.engine.Listener(n.engine.FindClosestListener(p.X, p.Y, p.Z))
}

// growScratch ensures the scratch buffers can hold the given sample counts.
func (n *engineNode) growScratch(srcSamples, midSamples int) {
	if cap(n.srcScratch) < srcSamples {
		n.srcScratch = make([]float32, srcSamples)
	}
	if cap(n.midScratch) < midSamples {
		n.midScratch = make([]float32, midSamples)
	}
}

// readSourceFrames reads frameCount frames (channelsIn) from the data
// source with pitch applied. Returns frames produced.
func (n *engineNode) readSourceFrames(dst []float32, frameCount uint32) uint32 {
	pitch := n.pitch * n.dopplerPitch
	if n.pitchResampler == nil || pitch == 1 {
		read, err := n.ds.Read(f32ToBytes(dst[:int(frameCount)*int(n.channelsIn)]), uint64(frameCount))
		if err == ErrAtEnd {
			n.atEnd = true
		}
		return uint32(read)
	}

	// Pitched read: resample from the source through the pitch resampler.
	if pitch <= 0 {
		pitch = 0.001
	}
	rateIn := uint32(float32(n.engine.sampleRate) * pitch)
	if rateIn == 0 {
		rateIn = 1
	}
	_ = n.pitchResampler.SetRate(rateIn, n.engine.sampleRate)

	required := n.pitchResampler.RequiredInputFrameCount(uint64(frameCount))
	n.growScratch(int(required)*int(n.channelsIn), 0)
	src := n.srcScratch[:int(required)*int(n.channelsIn)]
	read, err := n.ds.Read(f32ToBytes(src), required)
	if err == ErrAtEnd {
		n.atEnd = true
	}
	if read == 0 {
		return 0
	}
	_, produced, _ := n.pitchResampler.ProcessF32(dst, src, read, uint64(frameCount))
	return uint32(produced)
}

// ProcessNode implements NodeProcessor for both sounds (no inputs) and
// groups (one input bus).
func (n *engineNode) ProcessNode(framesIn [][]float32, frameCountIn uint32, framesOut [][]float32, frameCountOut uint32) (uint32, uint32) {
	if len(framesOut) == 0 || framesOut[0] == nil {
		return 0, 0
	}
	out := framesOut[0]

	n.mu.Lock()
	defer n.mu.Unlock()

	cin := int(n.channelsIn)
	cout := int(n.channelsOut)

	// Stage 1: acquire input frames at channelsIn.
	var input []float32
	var frames uint32
	if n.ds != nil {
		n.growScratch(int(frameCountOut)*cin, int(frameCountOut)*cin)
		input = n.midScratch[:int(frameCountOut)*cin]
		frames = n.readSourceFrames(input, frameCountOut)
		if frames == 0 {
			return frameCountIn, 0
		}
		input = input[:int(frames)*cin]
	} else {
		if len(framesIn) == 0 || framesIn[0] == nil || frameCountIn == 0 {
			return 0, 0
		}
		input = framesIn[0]
		frames = frameCountIn
		if frames > frameCountOut {
			frames = frameCountOut
		}
	}

	// Stage 2: spatialize (or plain channel-convert) into the output.
	listener := n.currentListener()
	if err := n.spat.Process(listener, out[:int(frames)*cout], input, uint64(frames)); err != nil {
		return frames, 0
	}
	n.dopplerPitch = n.spat.DopplerPitch()

	// Stage 3: pan, fade, volume smoothing.
	_ = n.panner.ProcessF32(out, out, uint64(frames))
	_ = n.fader.ProcessF32(out, out, uint64(frames))
	_ = n.gainer.Process(out, out, uint64(frames))

	// End-of-sound bookkeeping.
	if n.atEnd && n.endFn != nil {
		fn, owner := n.endFn, n.endFnOwner
		n.endFn = nil
		go fn(owner)
	}
	return frames, frames
}

/**************************************************************************
Sound
**************************************************************************/

// SoundConfig mirrors ma_sound_config.
type SoundConfig struct {
	FilePath                       string
	DataSource                     DataSource
	InitialAttachment              Node // Defaults to the engine endpoint (or the group).
	InitialAttachmentInputBusIndex uint32
	ChannelsIn                     uint32
	Flags                          SoundFlags
	InitialVolume                  float32
	Looping                        bool
	EndCallback                    func(*Sound)
}

// SoundConfigInit mirrors ma_sound_config_init.
func SoundConfigInit() SoundConfig {
	return SoundConfig{InitialVolume: 1}
}

// Sound mirrors ma_sound.
type Sound struct {
	engine *Engine
	node   *engineNode
	dec    *Decoder     // Owned decoder for file-backed sounds, closed on close().
	async  *asyncSource // Background streamer for file-backed sounds; stopped before dec is closed.
}

// NewSound mirrors ma_sound_init_ex.
func (e *Engine) NewSound(config SoundConfig) (*Sound, error) {
	var ds DataSource
	var dec *Decoder
	var async *asyncSource

	switch {
	case config.DataSource != nil:
		ds = config.DataSource
	case config.FilePath != "":
		decCfg := DecoderConfigInit(FormatF32, 0, e.sampleRate)
		d, err := NewDecoderFile(config.FilePath, &decCfg)
		if err != nil {
			return nil, err
		}
		if config.Flags&SoundFlagDecode != 0 {
			// Fully decode into memory.
			buf, frames, derr := decodeAll(d)
			d.Close()
			if derr != nil {
				return nil, derr
			}
			_, ch, _, _, _ := d.DataFormat()
			ab, aerr := NewAudioBuffer(AudioBufferConfigInit(FormatF32, ch, frames, buf))
			if aerr != nil {
				return nil, aerr
			}
			ab.SetSampleRate(e.sampleRate)
			ds = ab
		} else {
			// Streaming: decode on a background goroutine through a ring
			// buffer so the audio thread never performs file I/O. Note:
			// loop points/ranges are not supported on streamed sounds; use
			// SoundFlagDecode for those.
			as, aerr := newAsyncSource(d)
			if aerr != nil {
				d.Close()
				return nil, aerr
			}
			ds = as
			async = as
			dec = d
		}
	default:
		return nil, ErrInvalidArgs
	}

	format, channels, _, _, err := ds.DataFormat()
	if err != nil {
		return nil, err
	}
	if format != FormatF32 {
		return nil, ErrInvalidArgs // Engine sounds must produce f32.
	}

	ctrl := NewDataSourceController(ds)
	_ = ctrl.SetLooping(config.Looping)

	node, err := e.newEngineNode(channels, config.Flags, ctrl, 0)
	if err != nil {
		if dec != nil {
			dec.Close()
		}
		return nil, err
	}

	s := &Sound{engine: e, node: node, dec: dec, async: async}
	node.endFn = config.EndCallback
	node.endFnOwner = s

	if config.InitialVolume != 1 {
		_ = s.SetVolume(config.InitialVolume)
	}

	if config.Flags&SoundFlagNoDefaultAttachment == 0 {
		target := config.InitialAttachment
		if target == nil {
			target = e.Endpoint()
		}
		if err := node.AttachOutputBus(0, target, config.InitialAttachmentInputBusIndex); err != nil {
			if dec != nil {
				dec.Close()
			}
			return nil, err
		}
	}
	return s, nil
}

// decodeAll reads an entire decoder into memory, returning the raw f32
// frames and the frame count.
func decodeAll(d *Decoder) ([]byte, uint64, error) {
	_, channels, _, _, err := d.DataFormat()
	if err != nil {
		return nil, 0, err
	}
	bpf := FrameSizeInBytes(FormatF32, channels)
	var out []byte
	var total uint64
	chunk := make([]byte, 16384*bpf)
	for {
		read, err := d.Read(chunk, 16384)
		if read > 0 {
			out = append(out, chunk[:int(read)*bpf]...)
			total += read
		}
		if err == ErrAtEnd {
			break
		}
		if err != nil {
			return nil, 0, err
		}
		if read == 0 {
			break
		}
	}
	return out, total, nil
}

// NewSoundFromFile mirrors ma_sound_init_from_file.
func (e *Engine) NewSoundFromFile(path string, flags SoundFlags, group *SoundGroup) (*Sound, error) {
	cfg := SoundConfigInit()
	cfg.FilePath = path
	cfg.Flags = flags
	if group != nil {
		cfg.InitialAttachment = group.node
	}
	return e.NewSound(cfg)
}

// NewSoundFromDataSource mirrors ma_sound_init_from_data_source.
func (e *Engine) NewSoundFromDataSource(ds DataSource, flags SoundFlags, group *SoundGroup) (*Sound, error) {
	cfg := SoundConfigInit()
	cfg.DataSource = ds
	cfg.Flags = flags
	if group != nil {
		cfg.InitialAttachment = group.node
	}
	return e.NewSound(cfg)
}

func (s *Sound) close() {
	_ = s.node.DetachAllOutputBuses()
	if s.async != nil {
		s.async.stop() // The feeder must be stopped before the decoder is closed.
		s.async = nil
	}
	if s.dec != nil {
		s.dec.Close()
		s.dec = nil
	}
}

// Uninit mirrors ma_sound_uninit.
func (s *Sound) Uninit() { s.close() }

// Node returns the sound's node for manual graph wiring.
func (s *Sound) Node() *NodeBase { return s.node.NodeBase }

// Engine mirrors ma_sound_get_engine.
func (s *Sound) Engine() *Engine { return s.engine }

// Start mirrors ma_sound_start.
func (s *Sound) Start() error {
	s.node.mu.Lock()
	s.node.atEnd = false
	s.node.mu.Unlock()
	return s.node.SetState(NodeStateStarted)
}

// Stop mirrors ma_sound_stop.
func (s *Sound) Stop() error { return s.node.SetState(NodeStateStopped) }

// IsPlaying mirrors ma_sound_is_playing.
func (s *Sound) IsPlaying() bool { return s.node.State() == NodeStateStarted && !s.AtEnd() }

// AtEnd mirrors ma_sound_at_end.
func (s *Sound) AtEnd() bool {
	s.node.mu.Lock()
	defer s.node.mu.Unlock()
	return s.node.atEnd
}

// SetVolume mirrors ma_sound_set_volume.
func (s *Sound) SetVolume(volume float32) error { return s.node.gainer.SetGain(volume) }

// Volume mirrors ma_sound_get_volume.
func (s *Sound) Volume() float32 { return s.node.gainer.newGains[0] }

// SetPan mirrors ma_sound_set_pan.
func (s *Sound) SetPan(pan float32) { s.node.panner.SetPan(pan) }

// Pan mirrors ma_sound_get_pan.
func (s *Sound) Pan() float32 { return s.node.panner.Pan() }

// SetPanMode mirrors ma_sound_set_pan_mode.
func (s *Sound) SetPanMode(mode PanMode) { s.node.panner.SetMode(mode) }

// PanMode mirrors ma_sound_get_pan_mode.
func (s *Sound) PanMode() PanMode { return s.node.panner.Mode() }

// SetPitch mirrors ma_sound_set_pitch.
func (s *Sound) SetPitch(pitch float32) {
	if pitch <= 0 {
		return
	}
	s.node.mu.Lock()
	s.node.pitch = pitch
	s.node.mu.Unlock()
}

// Pitch mirrors ma_sound_get_pitch.
func (s *Sound) Pitch() float32 {
	s.node.mu.Lock()
	defer s.node.mu.Unlock()
	return s.node.pitch
}

// SetLooping mirrors ma_sound_set_looping.
func (s *Sound) SetLooping(looping bool) { _ = s.node.ds.SetLooping(looping) }

// IsLooping mirrors ma_sound_is_looping.
func (s *Sound) IsLooping() bool { return s.node.ds.IsLooping() }

// SeekToPCMFrame mirrors ma_sound_seek_to_pcm_frame.
func (s *Sound) SeekToPCMFrame(frameIndex uint64) error {
	s.node.mu.Lock()
	defer s.node.mu.Unlock()
	s.node.atEnd = false
	return s.node.ds.Seek(frameIndex)
}

// SeekToSecond mirrors ma_sound_seek_to_second.
func (s *Sound) SeekToSecond(seconds float32) error {
	s.node.mu.Lock()
	defer s.node.mu.Unlock()
	s.node.atEnd = false
	return s.node.ds.SeekSeconds(seconds)
}

// Cursor mirrors ma_sound_get_cursor_in_pcm_frames.
func (s *Sound) Cursor() (uint64, error) { return s.node.ds.Cursor() }

// CursorInSeconds mirrors ma_sound_get_cursor_in_seconds.
func (s *Sound) CursorInSeconds() (float32, error) { return s.node.ds.CursorInSeconds() }

// Length mirrors ma_sound_get_length_in_pcm_frames.
func (s *Sound) Length() (uint64, error) { return s.node.ds.Length() }

// LengthInSeconds mirrors ma_sound_get_length_in_seconds.
func (s *Sound) LengthInSeconds() (float32, error) { return s.node.ds.LengthInSeconds() }

// DataSource mirrors ma_sound_get_data_source.
func (s *Sound) DataSource() DataSource { return s.node.ds.Source() }

// Spatialization controls, mirroring ma_sound_set_*/get_*.

func (s *Sound) SetSpatializationEnabled(enabled bool) {
	s.node.mu.Lock()
	s.node.isSpatial = enabled
	if enabled {
		s.node.spat.SetAttenuationModel(AttenuationModelInverse)
	} else {
		s.node.spat.SetAttenuationModel(AttenuationModelNone)
	}
	s.node.mu.Unlock()
}
func (s *Sound) IsSpatializationEnabled() bool {
	s.node.mu.Lock()
	defer s.node.mu.Unlock()
	return s.node.isSpatial
}
func (s *Sound) SetPinnedListenerIndex(index uint32) {
	s.node.mu.Lock()
	s.node.pinnedListenerIndex = int32(index)
	s.node.mu.Unlock()
}
func (s *Sound) PinnedListenerIndex() uint32 {
	s.node.mu.Lock()
	defer s.node.mu.Unlock()
	if s.node.pinnedListenerIndex < 0 {
		return 0
	}
	return uint32(s.node.pinnedListenerIndex)
}
func (s *Sound) SetPosition(x, y, z float32)            { s.node.spat.SetPosition(x, y, z) }
func (s *Sound) Position() Vec3                         { return s.node.spat.Position() }
func (s *Sound) SetDirection(x, y, z float32)           { s.node.spat.SetDirection(x, y, z) }
func (s *Sound) Direction() Vec3                        { return s.node.spat.Direction() }
func (s *Sound) SetVelocity(x, y, z float32)            { s.node.spat.SetVelocity(x, y, z) }
func (s *Sound) Velocity() Vec3                         { return s.node.spat.Velocity() }
func (s *Sound) SetAttenuationModel(m AttenuationModel) { s.node.spat.SetAttenuationModel(m) }
func (s *Sound) AttenuationModel() AttenuationModel     { return s.node.spat.AttenuationModel() }
func (s *Sound) SetPositioning(p Positioning)           { s.node.spat.SetPositioning(p) }
func (s *Sound) Positioning() Positioning               { return s.node.spat.Positioning() }
func (s *Sound) SetRolloff(v float32)                   { s.node.spat.SetRolloff(v) }
func (s *Sound) Rolloff() float32                       { return s.node.spat.Rolloff() }
func (s *Sound) SetMinGain(v float32)                   { s.node.spat.SetMinGain(v) }
func (s *Sound) MinGain() float32                       { return s.node.spat.MinGain() }
func (s *Sound) SetMaxGain(v float32)                   { s.node.spat.SetMaxGain(v) }
func (s *Sound) MaxGain() float32                       { return s.node.spat.MaxGain() }
func (s *Sound) SetMinDistance(v float32)               { s.node.spat.SetMinDistance(v) }
func (s *Sound) MinDistance() float32                   { return s.node.spat.MinDistance() }
func (s *Sound) SetMaxDistance(v float32)               { s.node.spat.SetMaxDistance(v) }
func (s *Sound) MaxDistance() float32                   { return s.node.spat.MaxDistance() }
func (s *Sound) SetCone(innerAngleInRadians, outerAngleInRadians, outerGain float32) {
	s.node.spat.SetCone(innerAngleInRadians, outerAngleInRadians, outerGain)
}
func (s *Sound) Cone() (float32, float32, float32) { return s.node.spat.Cone() }
func (s *Sound) SetDopplerFactor(v float32)        { s.node.spat.SetDopplerFactor(v) }
func (s *Sound) DopplerFactor() float32            { return s.node.spat.DopplerFactor() }
func (s *Sound) SetDirectionalAttenuationFactor(v float32) {
	s.node.spat.SetDirectionalAttenuationFactor(v)
}
func (s *Sound) DirectionalAttenuationFactor() float32 {
	return s.node.spat.DirectionalAttenuationFactor()
}

// DirectionToListener mirrors ma_sound_get_direction_to_listener.
func (s *Sound) DirectionToListener() Vec3 {
	listener := s.node.currentListener()
	if listener == nil {
		return Vec3{0, 0, -1}
	}
	return listener.Position().sub(s.node.spat.Position()).normalize()
}

// Fading, mirroring ma_sound_set_fade_*.

// SetFadeInPCMFrames mirrors ma_sound_set_fade_in_pcm_frames. Use -1 for
// volumeBeg to fade from the current volume.
func (s *Sound) SetFadeInPCMFrames(volumeBeg, volumeEnd float32, fadeLengthInFrames uint64) {
	if volumeBeg < 0 {
		volumeBeg = s.node.fader.CurrentVolume()
	}
	s.node.fader.SetFade(volumeBeg, volumeEnd, fadeLengthInFrames)
}

// SetFadeInMilliseconds mirrors ma_sound_set_fade_in_milliseconds.
func (s *Sound) SetFadeInMilliseconds(volumeBeg, volumeEnd float32, fadeLengthInMilliseconds uint64) {
	s.SetFadeInPCMFrames(volumeBeg, volumeEnd, fadeLengthInMilliseconds*uint64(s.engine.sampleRate)/1000)
}

// CurrentFadeVolume mirrors ma_sound_get_current_fade_volume.
func (s *Sound) CurrentFadeVolume() float32 { return s.node.fader.CurrentVolume() }

// Scheduled start/stop, mirroring ma_sound_set_start_time_* and stop_time_*.

// SetStartTimeInPCMFrames mirrors ma_sound_set_start_time_in_pcm_frames.
func (s *Sound) SetStartTimeInPCMFrames(absoluteGlobalTimeInFrames uint64) {
	_ = s.node.SetStateTime(NodeStateStarted, absoluteGlobalTimeInFrames)
}

// SetStartTimeInMilliseconds mirrors ma_sound_set_start_time_in_milliseconds.
func (s *Sound) SetStartTimeInMilliseconds(ms uint64) {
	s.SetStartTimeInPCMFrames(ms * uint64(s.engine.sampleRate) / 1000)
}

// SetStopTimeInPCMFrames mirrors ma_sound_set_stop_time_in_pcm_frames.
func (s *Sound) SetStopTimeInPCMFrames(absoluteGlobalTimeInFrames uint64) {
	_ = s.node.SetStateTime(NodeStateStopped, absoluteGlobalTimeInFrames)
}

// SetStopTimeInMilliseconds mirrors ma_sound_set_stop_time_in_milliseconds.
func (s *Sound) SetStopTimeInMilliseconds(ms uint64) {
	s.SetStopTimeInPCMFrames(ms * uint64(s.engine.sampleRate) / 1000)
}

// SetEndCallback mirrors ma_sound_set_end_callback.
func (s *Sound) SetEndCallback(fn func(*Sound)) {
	s.node.mu.Lock()
	s.node.endFn = fn
	s.node.endFnOwner = s
	s.node.mu.Unlock()
}

/**************************************************************************
Sound group
**************************************************************************/

// SoundGroup mirrors ma_sound_group: a mixing bus that sounds can be routed
// through.
type SoundGroup struct {
	engine *Engine
	node   *engineNode
}

// NewSoundGroup mirrors ma_sound_group_init.
func (e *Engine) NewSoundGroup(flags SoundFlags, parent *SoundGroup) (*SoundGroup, error) {
	node, err := e.newEngineNode(e.Channels(), flags|SoundFlagNoSpatialization, nil, 1)
	if err != nil {
		return nil, err
	}
	g := &SoundGroup{engine: e, node: node}

	target := Node(e.Endpoint())
	if parent != nil {
		target = parent.node
	}
	if err := node.AttachOutputBus(0, target, 0); err != nil {
		return nil, err
	}
	_ = node.SetState(NodeStateStarted)
	return g, nil
}

// Uninit mirrors ma_sound_group_uninit.
func (g *SoundGroup) Uninit() { _ = g.node.DetachAllOutputBuses() }

// Node returns the group's node for manual wiring.
func (g *SoundGroup) Node() *NodeBase { return g.node.NodeBase }

// Start mirrors ma_sound_group_start.
func (g *SoundGroup) Start() error { return g.node.SetState(NodeStateStarted) }

// Stop mirrors ma_sound_group_stop.
func (g *SoundGroup) Stop() error { return g.node.SetState(NodeStateStopped) }

// SetVolume mirrors ma_sound_group_set_volume.
func (g *SoundGroup) SetVolume(volume float32) error { return g.node.gainer.SetGain(volume) }

// SetPan mirrors ma_sound_group_set_pan.
func (g *SoundGroup) SetPan(pan float32) { g.node.panner.SetPan(pan) }

// SetPitch mirrors ma_sound_group_set_pitch. Note: group pitching applies to
// sounds routed through the group only when they are pitched individually in
// this implementation.
func (g *SoundGroup) SetPitch(pitch float32) {
	g.node.mu.Lock()
	g.node.pitch = pitch
	g.node.mu.Unlock()
}
