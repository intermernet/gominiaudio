package gominiaudio

import (
	"math"
	"sync"
	"sync/atomic"
)

// DefaultPeriodSizeInMillisecondsLowLatency mirrors
// MA_DEFAULT_PERIOD_SIZE_IN_MILLISECONDS_LOW_LATENCY.
const (
	DefaultPeriodSizeInMillisecondsLowLatency   = 10
	DefaultPeriodSizeInMillisecondsConservative = 100
	DefaultPeriods                              = 3
	DefaultFormat                               = FormatF32
	DefaultChannels                             = 2
	DefaultSampleRate                           = 48000
)

// DeviceNotificationType mirrors ma_device_notification_type.
type DeviceNotificationType uint32

const (
	DeviceNotificationTypeStarted           DeviceNotificationType = 0
	DeviceNotificationTypeStopped           DeviceNotificationType = 1
	DeviceNotificationTypeRerouted          DeviceNotificationType = 2
	DeviceNotificationTypeInterruptionBegan DeviceNotificationType = 3
	DeviceNotificationTypeInterruptionEnded DeviceNotificationType = 4
	DeviceNotificationTypeUnlocked          DeviceNotificationType = 5
)

// DeviceNotification mirrors ma_device_notification.
type DeviceNotification struct {
	Device *Device
	Type   DeviceNotificationType
}

// DeviceCallbacks mirrors the function pointers of ma_device_config, shaped
// like malgo's DeviceCallbacks.
type DeviceCallbacks struct {
	// Data is the data callback, mirroring ma_device_data_proc. For playback
	// devices write to outputSamples; for capture devices read from
	// inputSamples; duplex devices use both. Buffers are in the device's
	// client format. Never block in this callback and never allocate.
	Data func(device *Device, outputSamples, inputSamples []byte, frameCount uint32)

	// Stop mirrors ma_stop_proc.
	Stop func(device *Device)

	// Notification mirrors ma_device_notification_proc.
	Notification func(notification DeviceNotification)
}

// DeviceSubConfig mirrors the playback/capture sub-structs of
// ma_device_config.
type DeviceSubConfig struct {
	DeviceID  *DeviceID // nil for the default device.
	Format    Format    // FormatUnknown to use the device's native format.
	Channels  uint32    // 0 to use the device's native channel count.
	ChannelMap []Channel
	ChannelMixMode ChannelMixMode
	CalculateLFEFromSpatialChannels bool
	ShareMode ShareMode
}

// WASAPIDeviceConfig mirrors the wasapi sub-struct of ma_device_config.
type WASAPIDeviceConfig struct {
	NoAutoConvertSRC     bool // When set to true, disables the use of AUDCLNT_STREAMFLAGS_AUTOCONVERTPCM.
	NoDefaultQualitySRC  bool // When set to true, disables the use of AUDCLNT_STREAMFLAGS_SRC_DEFAULT_QUALITY.
	NoAutoStreamRouting  bool // Disables automatic stream routing.
	NoHardwareOffloading bool // Disables WASAPI's hardware offloading feature.
	Usage                uint32
}

// CoreAudioDeviceConfig mirrors the coreaudio sub-struct of ma_device_config.
type CoreAudioDeviceConfig struct {
	AllowNominalSampleRateChange bool // Desktop only. When enabled, allows changing of the sample rate at the operating system level.
}

// PipeWireDeviceConfig holds PipeWire-specific options (replaces miniaudio's
// pulse options for the Linux backend).
type PipeWireDeviceConfig struct {
	StreamNamePlayback string
	StreamNameCapture  string
}

// DeviceConfig mirrors ma_device_config.
type DeviceConfig struct {
	DeviceType               DeviceType
	SampleRate               uint32 // 0 to use the device's native rate.
	PeriodSizeInFrames       uint32
	PeriodSizeInMilliseconds uint32
	Periods                  uint32
	PerformanceProfile       PerformanceProfile
	NoPreSilencedOutputBuffer bool
	NoClip                   bool
	NoFixedSizedCallback     bool // When true the data callback is passed whatever frame count the backend produces.

	// DuplexPreSeekSizeInFrames controls how much silence is inserted
	// between the capture and playback sides of a duplex device to absorb
	// scheduling jitter. It adds this many frames of latency. 0 uses the
	// default of two capture periods (miniaudio's behavior); one period is
	// the practical minimum when both directions run on the same clock.
	DuplexPreSeekSizeInFrames uint32
	Playback                 DeviceSubConfig
	Capture                  DeviceSubConfig
	Resampling               struct {
		Algorithm ResampleAlgorithm
		Linear    struct {
			LPFOrder uint32
		}
	}
	WASAPI    WASAPIDeviceConfig
	CoreAudio CoreAudioDeviceConfig
	PipeWire  PipeWireDeviceConfig
}

// DeviceConfigInit mirrors ma_device_config_init.
func DeviceConfigInit(deviceType DeviceType) DeviceConfig {
	cfg := DeviceConfig{DeviceType: deviceType}
	cfg.Resampling.Algorithm = ResampleAlgorithmLinear
	cfg.Resampling.Linear.LPFOrder = min(DefaultResamplerLPFOrder, MaxFilterOrder)
	return cfg
}

// deviceSide holds the state for one direction (playback or capture) of a
// device: the client-visible format, the backend's native format, and the
// converter between them.
type deviceSide struct {
	// Client format: what the data callback sees.
	format     Format
	channels   uint32
	channelMap []Channel

	// Internal (native) format: what the backend device uses.
	internalFormat             Format
	internalChannels           uint32
	internalSampleRate         uint32
	internalChannelMap         []Channel
	internalPeriodSizeInFrames uint32
	internalPeriods            uint32

	id        *DeviceID
	name      string
	shareMode ShareMode

	converter *DataConverter // nil when client == native.

	// Fixed-size callback intermediation, client format.
	fifo    []byte // Capacity: one client period.
	fifoPos uint32 // Read position in frames (playback) / write position (capture).
	fifoLen uint32 // Valid frames.

	scratch []byte // Variable-size conversion staging buffer.
}

// Device mirrors ma_device.
type Device struct {
	ctx      *Context
	backend  deviceBackend
	config   DeviceConfig
	callbacks DeviceCallbacks

	deviceType DeviceType
	sampleRate uint32 // Client sample rate.

	state atomic.Uint32 // DeviceState

	playback deviceSide
	capture  deviceSide

	masterVolume atomic.Uint32 // math.Float32bits; applied to playback output.

	duplexRB       *DuplexRB // Client-format capture->playback bridge for duplex devices.
	duplexInputBuf []byte    // Playback-thread staging buffer for duplex input. Must not be shared with the capture thread.

	stateMu sync.Mutex // Serializes Start/Stop/Uninit.
}

// InitDevice mirrors ma_device_init, shaped like malgo.InitDevice.
func InitDevice(ctx *Context, config DeviceConfig, callbacks DeviceCallbacks) (*Device, error) {
	if ctx == nil {
		return nil, ErrInvalidArgs
	}
	switch config.DeviceType {
	case DeviceTypePlayback, DeviceTypeCapture, DeviceTypeDuplex, DeviceTypeLoopback:
	default:
		return nil, ErrInvalidDeviceConfig
	}
	if config.Periods == 0 {
		config.Periods = DefaultPeriods
	}

	d := &Device{
		ctx:        ctx,
		config:     config,
		callbacks:  callbacks,
		deviceType: config.DeviceType,
	}
	d.playback.id = config.Playback.DeviceID
	d.capture.id = config.Capture.DeviceID
	d.playback.shareMode = config.Playback.ShareMode
	d.capture.shareMode = config.Capture.ShareMode
	d.masterVolume.Store(math.Float32bits(1))

	backend, err := ctx.backend.newDevice(d, &config)
	if err != nil {
		return nil, err
	}
	d.backend = backend

	// The backend has filled in the internal formats via
	// setInternalFormat. Resolve the client formats and build converters.
	if err := d.finalizeFormats(); err != nil {
		backend.uninit()
		return nil, err
	}

	ctx.registerDevice(d)
	d.state.Store(uint32(DeviceStateStopped))
	return d, nil
}

// setInternalFormat is called by backends after negotiating the native
// device format for one direction.
func (d *Device) setInternalFormat(deviceType DeviceType, format Format, channels, sampleRate uint32, channelMap []Channel, periodSizeInFrames, periods uint32, name string) {
	side := &d.playback
	if deviceType == DeviceTypeCapture || deviceType == DeviceTypeLoopback {
		side = &d.capture
	}
	side.internalFormat = format
	side.internalChannels = channels
	side.internalSampleRate = sampleRate
	side.internalChannelMap = channelMap
	side.internalPeriodSizeInFrames = periodSizeInFrames
	side.internalPeriods = periods
	side.name = name
}

// calculatePeriodSizeInFrames resolves the requested period size for a given
// native sample rate, honoring the performance profile. Used by backends.
func (d *Device) calculatePeriodSizeInFrames(nativeSampleRate uint32) uint32 {
	cfg := &d.config
	if cfg.PeriodSizeInFrames != 0 {
		return cfg.PeriodSizeInFrames
	}
	ms := cfg.PeriodSizeInMilliseconds
	if ms == 0 {
		if cfg.PerformanceProfile == PerformanceProfileConservative {
			ms = DefaultPeriodSizeInMillisecondsConservative
		} else {
			ms = DefaultPeriodSizeInMillisecondsLowLatency
		}
	}
	return uint32(uint64(ms) * uint64(nativeSampleRate) / 1000)
}

func (d *Device) finalizeFormats() error {
	cfg := &d.config

	resolveSide := func(side *deviceSide, sub *DeviceSubConfig, isCapture bool) error {
		side.format = sub.Format
		side.channels = sub.Channels
		side.channelMap = sub.ChannelMap
		if side.format == FormatUnknown {
			side.format = side.internalFormat
			if side.format == FormatUnknown {
				side.format = DefaultFormat
			}
		}
		if side.channels == 0 {
			side.channels = side.internalChannels
			if side.channels == 0 {
				side.channels = DefaultChannels
			}
		}
		if d.sampleRate == 0 {
			d.sampleRate = cfg.SampleRate
			if d.sampleRate == 0 {
				d.sampleRate = side.internalSampleRate
				if d.sampleRate == 0 {
					d.sampleRate = DefaultSampleRate
				}
			}
		}

		needsConvert := side.format != side.internalFormat ||
			side.channels != side.internalChannels ||
			d.sampleRate != side.internalSampleRate ||
			!ChannelMapIsEqual(side.channelMap, side.internalChannelMap, side.channels)

		if needsConvert {
			var dcCfg DataConverterConfig
			if isCapture {
				dcCfg = DataConverterConfigInit(side.internalFormat, side.format, side.internalChannels, side.channels, side.internalSampleRate, d.sampleRate)
				dcCfg.ChannelMapIn = side.internalChannelMap
				dcCfg.ChannelMapOut = side.channelMap
			} else {
				dcCfg = DataConverterConfigInit(side.format, side.internalFormat, side.channels, side.internalChannels, d.sampleRate, side.internalSampleRate)
				dcCfg.ChannelMapIn = side.channelMap
				dcCfg.ChannelMapOut = side.internalChannelMap
			}
			dcCfg.ChannelMixMode = sub.ChannelMixMode
			dcCfg.CalculateLFEFromSpatialChannels = sub.CalculateLFEFromSpatialChannels
			dcCfg.Resampling.Algorithm = cfg.Resampling.Algorithm
			dcCfg.Resampling.Linear.LPFOrder = cfg.Resampling.Linear.LPFOrder
			conv, err := NewDataConverter(dcCfg)
			if err != nil {
				return err
			}
			side.converter = conv
		}

		// Preallocate the fixed-size FIFO (one client period) and scratch.
		clientPeriod := d.clientPeriodSizeInFrames(side)
		bpf := FrameSizeInBytes(side.format, side.channels)
		side.fifo = make([]byte, int(clientPeriod)*bpf)
		side.scratch = make([]byte, int(clientPeriod+16)*bpf*2)
		return nil
	}

	if d.deviceType&DeviceTypePlayback != 0 {
		if err := resolveSide(&d.playback, &cfg.Playback, false); err != nil {
			return err
		}
	}
	if d.deviceType&DeviceTypeCapture != 0 || d.deviceType == DeviceTypeLoopback {
		if err := resolveSide(&d.capture, &cfg.Capture, true); err != nil {
			return err
		}
	}

	if d.deviceType == DeviceTypeDuplex {
		capturePeriod := d.clientPeriodSizeInFrames(&d.capture)
		preSeek := cfg.DuplexPreSeekSizeInFrames
		if preSeek == 0 {
			preSeek = capturePeriod * 2
		}
		rb, err := NewDuplexRBEx(d.capture.format, d.capture.channels, d.sampleRate, capturePeriod, preSeek)
		if err != nil {
			return err
		}
		d.duplexRB = rb
		bpf := FrameSizeInBytes(d.capture.format, d.capture.channels)
		d.duplexInputBuf = make([]byte, int(d.clientPeriodSizeInFrames(&d.playback))*bpf)
	}
	return nil
}

// clientPeriodSizeInFrames converts the native period size to the client
// sample rate.
func (d *Device) clientPeriodSizeInFrames(side *deviceSide) uint32 {
	if side.internalSampleRate == 0 || side.internalPeriodSizeInFrames == 0 {
		return d.calculatePeriodSizeInFrames(d.sampleRate)
	}
	if d.sampleRate == side.internalSampleRate {
		return side.internalPeriodSizeInFrames
	}
	return uint32(uint64(side.internalPeriodSizeInFrames) * uint64(d.sampleRate) / uint64(side.internalSampleRate))
}

// Start mirrors ma_device_start.
func (d *Device) Start() error {
	d.stateMu.Lock()
	defer d.stateMu.Unlock()

	switch DeviceState(d.state.Load()) {
	case DeviceStateUninitialized:
		return ErrDeviceNotInitialized
	case DeviceStateStarted:
		return nil // Mirrors miniaudio: starting a started device is a no-op with success.
	case DeviceStateStopped:
	default:
		return ErrBusy
	}

	d.state.Store(uint32(DeviceStateStarting))
	if err := d.backend.start(); err != nil {
		d.state.Store(uint32(DeviceStateStopped))
		return err
	}
	d.state.Store(uint32(DeviceStateStarted))
	d.notify(DeviceNotificationTypeStarted)
	return nil
}

// Stop mirrors ma_device_stop.
func (d *Device) Stop() error {
	d.stateMu.Lock()
	defer d.stateMu.Unlock()

	switch DeviceState(d.state.Load()) {
	case DeviceStateUninitialized:
		return ErrDeviceNotInitialized
	case DeviceStateStopped:
		return nil
	case DeviceStateStarted:
	default:
		return ErrBusy
	}

	d.state.Store(uint32(DeviceStateStopping))
	err := d.backend.stop()
	d.state.Store(uint32(DeviceStateStopped))
	if d.callbacks.Stop != nil {
		d.callbacks.Stop(d)
	}
	d.notify(DeviceNotificationTypeStopped)
	return err
}

// Uninit mirrors ma_device_uninit.
func (d *Device) Uninit() {
	if DeviceState(d.state.Load()) == DeviceStateStarted {
		_ = d.Stop()
	}
	d.stateMu.Lock()
	defer d.stateMu.Unlock()
	if DeviceState(d.state.Load()) == DeviceStateUninitialized {
		return
	}
	_ = d.backend.uninit()
	d.ctx.unregisterDevice(d)
	d.state.Store(uint32(DeviceStateUninitialized))
}

// State mirrors ma_device_get_state.
func (d *Device) State() DeviceState { return DeviceState(d.state.Load()) }

// IsStarted mirrors ma_device_is_started.
func (d *Device) IsStarted() bool { return d.State() == DeviceStateStarted }

// Context returns the owning context.
func (d *Device) Context() *Context { return d.ctx }

// Type returns the device type.
func (d *Device) Type() DeviceType { return d.deviceType }

// SampleRate returns the client sample rate.
func (d *Device) SampleRate() uint32 { return d.sampleRate }

// PlaybackFormat returns the client-side playback format.
func (d *Device) PlaybackFormat() (Format, uint32) { return d.playback.format, d.playback.channels }

// CaptureFormat returns the client-side capture format.
func (d *Device) CaptureFormat() (Format, uint32) { return d.capture.format, d.capture.channels }

// PlaybackInternalFormat returns the native playback format negotiated with
// the backend.
func (d *Device) PlaybackInternalFormat() (Format, uint32, uint32) {
	return d.playback.internalFormat, d.playback.internalChannels, d.playback.internalSampleRate
}

// CaptureInternalFormat returns the native capture format negotiated with
// the backend.
func (d *Device) CaptureInternalFormat() (Format, uint32, uint32) {
	return d.capture.internalFormat, d.capture.internalChannels, d.capture.internalSampleRate
}

// PlaybackInternalPeriodSizeInFrames returns the native playback period size.
func (d *Device) PlaybackInternalPeriodSizeInFrames() uint32 {
	return d.playback.internalPeriodSizeInFrames
}

// CaptureInternalPeriodSizeInFrames returns the native capture period size.
func (d *Device) CaptureInternalPeriodSizeInFrames() uint32 {
	return d.capture.internalPeriodSizeInFrames
}

// PlaybackName returns the name of the playback device.
func (d *Device) PlaybackName() string { return d.playback.name }

// CaptureName returns the name of the capture device.
func (d *Device) CaptureName() string { return d.capture.name }

// SetMasterVolume mirrors ma_device_set_master_volume.
func (d *Device) SetMasterVolume(volume float32) error {
	if volume < 0 {
		return ErrInvalidArgs
	}
	d.masterVolume.Store(math.Float32bits(volume))
	return nil
}

// MasterVolume mirrors ma_device_get_master_volume.
func (d *Device) MasterVolume() (float32, error) {
	return math.Float32frombits(d.masterVolume.Load()), nil
}

// SetMasterVolumeDB mirrors ma_device_set_master_volume_db.
func (d *Device) SetMasterVolumeDB(gainDB float32) error {
	return d.SetMasterVolume(VolumeDBToLinear(gainDB))
}

// MasterVolumeDB mirrors ma_device_get_master_volume_db.
func (d *Device) MasterVolumeDB() (float32, error) {
	v, err := d.MasterVolume()
	if err != nil {
		return 0, err
	}
	return VolumeLinearToDB(v), nil
}

func (d *Device) notify(t DeviceNotificationType) {
	if d.callbacks.Notification != nil {
		d.callbacks.Notification(DeviceNotification{Device: d, Type: t})
	}
}

/**************************************************************************
Data pumps. These are called from the backend's audio thread.
**************************************************************************/

// invokeDataCallback calls the user data callback with duplex input wiring.
func (d *Device) invokeDataCallback(output, input []byte, frameCount uint32) {
	if d.callbacks.Data != nil {
		d.callbacks.Data(d, output, input, frameCount)
	} else if output != nil {
		SilencePCMFrames(output, uint64(frameCount), d.playback.format, d.playback.channels)
	}
}

// handlePlayback fills nativeOut with frameCount frames in the playback
// side's internal format. Called by backends when the device needs audio.
func (d *Device) handlePlayback(nativeOut []byte, frameCount uint32) {
	side := &d.playback
	nativeBpf := FrameSizeInBytes(side.internalFormat, side.internalChannels)
	clientBpf := FrameSizeInBytes(side.format, side.channels)

	if !d.config.NoPreSilencedOutputBuffer {
		SilencePCMFrames(nativeOut, uint64(frameCount), side.internalFormat, side.internalChannels)
	}

	// Fast path: no conversion, flexible callback size.
	if side.converter == nil && d.config.NoFixedSizedCallback && d.deviceType != DeviceTypeDuplex {
		d.invokeDataCallback(nativeOut[:int(frameCount)*nativeBpf], nil, frameCount)
		d.postProcessPlayback(nativeOut, frameCount)
		return
	}

	clientPeriod := uint32(len(side.fifo) / clientBpf)
	var written uint32
	for written < frameCount {
		if side.fifoLen == 0 {
			// Produce one client period from the user callback.
			out := side.fifo
			var in []byte
			if d.deviceType == DeviceTypeDuplex {
				in = d.readDuplexInput(clientPeriod)
			}
			d.invokeDataCallback(out, in, clientPeriod)
			d.applyMasterVolumeClient(out, clientPeriod)
			side.fifoPos = 0
			side.fifoLen = clientPeriod
		}

		dst := nativeOut[int(written)*nativeBpf:]
		if side.converter == nil {
			n := min(frameCount-written, side.fifoLen)
			copy(dst[:int(n)*nativeBpf], side.fifo[int(side.fifoPos)*clientBpf:])
			side.fifoPos += n
			side.fifoLen -= n
			written += n
		} else {
			src := side.fifo[int(side.fifoPos)*clientBpf:]
			consumed, produced, err := side.converter.Process(dst, src, uint64(side.fifoLen), uint64(frameCount-written))
			if err != nil {
				break
			}
			side.fifoPos += uint32(consumed)
			side.fifoLen -= uint32(consumed)
			written += uint32(produced)
			if consumed == 0 && produced == 0 {
				break // Safety: no progress.
			}
		}
	}

	d.postProcessPlayback(nativeOut, frameCount)
}

// postProcessPlayback applies clipping to f32 native output.
func (d *Device) postProcessPlayback(nativeOut []byte, frameCount uint32) {
	if !d.config.NoClip && d.playback.internalFormat == FormatF32 {
		ClipSamplesF32(bytesToF32(nativeOut), bytesToF32(nativeOut), uint64(frameCount)*uint64(d.playback.internalChannels))
	}
}

// applyMasterVolumeClient applies the master volume to client-format frames.
func (d *Device) applyMasterVolumeClient(frames []byte, frameCount uint32) {
	vol := math.Float32frombits(d.masterVolume.Load())
	if vol == 1 {
		return
	}
	_ = ApplyVolumeFactorPCMFrames(frames, uint64(frameCount), d.playback.format, d.playback.channels, vol)
}

// readDuplexInput reads one client period of captured input from the duplex
// ring buffer, padding with silence on underrun. The returned slice is valid
// until the next call.
func (d *Device) readDuplexInput(frameCount uint32) []byte {
	side := &d.capture
	bpf := FrameSizeInBytes(side.format, side.channels)
	need := int(frameCount) * bpf
	if cap(d.duplexInputBuf) < need {
		d.duplexInputBuf = make([]byte, need)
	}
	buf := d.duplexInputBuf[:need]

	var got int
	rb := d.duplexRB.RB()
	for got < need {
		chunk := rb.AcquireRead(uint32((need - got) / bpf))
		if len(chunk) == 0 {
			break
		}
		copy(buf[got:], chunk)
		_ = rb.CommitRead(uint32(len(chunk) / bpf))
		got += len(chunk)
	}
	if got < need {
		SilencePCMFrames(buf[got:], uint64((need-got)/bpf), side.format, side.channels)
	}
	return buf
}

// handleCapture consumes frameCount frames of native-format captured audio.
// Called by backends when data arrives.
func (d *Device) handleCapture(nativeIn []byte, frameCount uint32) {
	side := &d.capture
	clientBpf := FrameSizeInBytes(side.format, side.channels)

	// Convert native -> client into scratch.
	var client []byte
	var clientFrames uint32
	if side.converter == nil {
		client = nativeIn
		clientFrames = frameCount
	} else {
		// Worst-case output frames for this input.
		outCap := side.converter.ExpectedOutputFrameCount(uint64(frameCount)) + 16
		need := int(outCap) * clientBpf
		if cap(side.scratch) < need {
			side.scratch = make([]byte, need)
		}
		_, produced, err := side.converter.Process(side.scratch[:need], nativeIn, uint64(frameCount), outCap)
		if err != nil {
			return
		}
		client = side.scratch
		clientFrames = uint32(produced)
	}

	if d.deviceType == DeviceTypeDuplex {
		// Feed the duplex ring buffer; the playback side consumes it.
		rb := d.duplexRB.RB()
		bpf := clientBpf
		remaining := int(clientFrames) * bpf
		off := 0
		for remaining > 0 {
			chunk := rb.AcquireWrite(uint32(remaining / bpf))
			if len(chunk) == 0 {
				break // Overrun: drop.
			}
			copy(chunk, client[off:off+len(chunk)])
			_ = rb.CommitWrite(uint32(len(chunk) / bpf))
			off += len(chunk)
			remaining -= len(chunk)
		}
		return
	}

	if d.config.NoFixedSizedCallback {
		d.invokeDataCallback(nil, client[:int(clientFrames)*clientBpf], clientFrames)
		return
	}

	// Fixed-size delivery: accumulate into the FIFO and deliver in period
	// sized chunks.
	clientPeriod := uint32(len(side.fifo) / clientBpf)
	var consumed uint32
	for consumed < clientFrames {
		n := min(clientFrames-consumed, clientPeriod-side.fifoLen)
		copy(side.fifo[int(side.fifoLen)*clientBpf:], client[int(consumed)*clientBpf:int(consumed+n)*clientBpf])
		side.fifoLen += n
		consumed += n
		if side.fifoLen == clientPeriod {
			d.invokeDataCallback(nil, side.fifo, clientPeriod)
			side.fifoLen = 0
		}
	}
}
