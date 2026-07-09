package gominiaudio

import (
	"runtime"
	"unsafe"

	"golang.org/x/sys/windows"
)

// WASAPI backend. Shared mode only, event-driven, with IAudioClient3
// small-period negotiation for the lowest achievable shared-mode latency.

type wasapiContext struct{}

func (c *wasapiContext) init(*ContextConfig) error {
	coInitializeEx()
	// Verify that the device enumerator is creatable.
	enum, err := coCreateInstance(&clsidMMDeviceEnumerator, &iidIMMDeviceEnumerator)
	if err != nil {
		return ErrFailedToInitBackend
	}
	enum.Release()
	return nil
}

func (c *wasapiContext) uninit() error      { return nil }
func (c *wasapiContext) backendID() Backend { return BackendWASAPI }

func (c *wasapiContext) enumerateDevices() ([]DeviceInfo, []DeviceInfo, error) {
	enum, err := coCreateInstance(&clsidMMDeviceEnumerator, &iidIMMDeviceEnumerator)
	if err != nil {
		return nil, nil, err
	}
	defer enum.Release()

	playback, err := wasapiEnumerateFlow(enum, eRender)
	if err != nil {
		return nil, nil, err
	}
	capture, err := wasapiEnumerateFlow(enum, eCapture)
	if err != nil {
		return nil, nil, err
	}
	return playback, capture, nil
}

func wasapiEnumerateFlow(enum comObject, dataFlow uint32) ([]DeviceInfo, error) {
	var defaultID string
	if dev, err := enum.getDefaultAudioEndpoint(dataFlow, eConsole); err == nil {
		defaultID, _ = dev.getID()
		dev.Release()
	}

	coll, err := enum.enumAudioEndpoints(dataFlow, deviceStateActive)
	if err != nil {
		return nil, err
	}
	defer coll.Release()

	count, err := coll.getCount()
	if err != nil {
		return nil, err
	}

	infos := make([]DeviceInfo, 0, count)
	for i := uint32(0); i < count; i++ {
		dev, err := coll.item(i)
		if err != nil {
			continue
		}
		info, err := wasapiDeviceInfo(dev, defaultID)
		dev.Release()
		if err != nil {
			continue
		}
		infos = append(infos, info)
	}
	return infos, nil
}

func wasapiDeviceInfo(dev comObject, defaultID string) (DeviceInfo, error) {
	var info DeviceInfo

	id, err := dev.getID()
	if err != nil {
		return info, err
	}
	info.ID = deviceIDFromString(id)
	info.IsDefault = id == defaultID

	if ps, err := dev.openPropertyStore(); err == nil {
		if name, err := ps.getStringValue(&pkeyDeviceFriendlyName); err == nil {
			info.Name = name
		}
		ps.Release()
	}

	// Report the mix format as the native format.
	if client, err := dev.activate(&iidIAudioClient); err == nil {
		if wf, err := client.getMixFormat(); err == nil {
			info.Formats = []DeviceNativeDataFormat{{
				Format:     wasapiFormatToNative(wf),
				Channels:   uint32(wf.Channels),
				SampleRate: wf.SamplesPerSec,
			}}
			coTaskMemFree(unsafe.Pointer(wf))
		}
		client.Release()
	}
	return info, nil
}

func (c *wasapiContext) deviceInfo(deviceType DeviceType, id *DeviceID) (DeviceInfo, error) {
	enum, err := coCreateInstance(&clsidMMDeviceEnumerator, &iidIMMDeviceEnumerator)
	if err != nil {
		return DeviceInfo{}, err
	}
	defer enum.Release()

	dataFlow := uint32(eRender)
	if deviceType == DeviceTypeCapture {
		dataFlow = eCapture
	}

	dev, err := wasapiOpenDevice(enum, dataFlow, id)
	if err != nil {
		return DeviceInfo{}, err
	}
	defer dev.Release()

	var defaultID string
	if dflt, derr := enum.getDefaultAudioEndpoint(dataFlow, eConsole); derr == nil {
		defaultID, _ = dflt.getID()
		dflt.Release()
	}
	return wasapiDeviceInfo(dev, defaultID)
}

// wasapiOpenDevice opens an IMMDevice by ID, or the default endpoint when id
// is nil/zero.
func wasapiOpenDevice(enum comObject, dataFlow uint32, id *DeviceID) (comObject, error) {
	if id == nil || id.IsZero() {
		return enum.getDefaultAudioEndpoint(dataFlow, eConsole)
	}
	wide, err := windows.UTF16PtrFromString(id.String())
	if err != nil {
		return nil, ErrInvalidArgs
	}
	return enum.getDevice(wide)
}

/**************************************************************************
Device
**************************************************************************/

// wasapiStream is one direction (render or capture) of a WASAPI device.
type wasapiStream struct {
	client        comObject // IAudioClient / IAudioClient3
	service       comObject // IAudioRenderClient or IAudioCaptureClient
	event         windows.Handle
	bufferFrames  uint32
	periodFrames  uint32
	format        Format
	channels      uint32
	sampleRate    uint32
	bpf           int
	isLoopback    bool
	exclusive     bool
}

type wasapiDevice struct {
	device *Device
	config DeviceConfig

	render  *wasapiStream // Playback side.
	capture *wasapiStream // Capture side.

	stopEvent windows.Handle
	doneCh    chan struct{}
}

func (c *wasapiContext) newDevice(d *Device, config *DeviceConfig) (deviceBackend, error) {
	if d.deviceType == DeviceTypeLoopback && config.Capture.ShareMode == ShareModeExclusive {
		return nil, ErrShareModeNotSupported // Loopback capture is shared-mode only.
	}

	wd := &wasapiDevice{device: d, config: *config}

	enum, err := coCreateInstance(&clsidMMDeviceEnumerator, &iidIMMDeviceEnumerator)
	if err != nil {
		return nil, ErrFailedToOpenBackendDevice
	}
	defer enum.Release()

	cleanup := func() {
		if wd.render != nil {
			wd.render.close()
		}
		if wd.capture != nil {
			wd.capture.close()
		}
	}

	if d.deviceType&DeviceTypePlayback != 0 {
		s, name, err := wd.initStream(enum, eRender, config.Playback.DeviceID, false)
		if err != nil {
			return nil, err
		}
		wd.render = s
		d.setInternalFormat(DeviceTypePlayback, s.format, s.channels, s.sampleRate, nil, s.periodFrames, config.Periods, name)
	}

	if d.deviceType&DeviceTypeCapture != 0 || d.deviceType == DeviceTypeLoopback {
		flow := uint32(eCapture)
		loopback := d.deviceType == DeviceTypeLoopback
		if loopback {
			flow = eRender // Loopback captures from a render endpoint.
		}
		s, name, err := wd.initStream(enum, flow, config.Capture.DeviceID, loopback)
		if err != nil {
			cleanup()
			return nil, err
		}
		wd.capture = s
		d.setInternalFormat(DeviceTypeCapture, s.format, s.channels, s.sampleRate, nil, s.periodFrames, config.Periods, name)
	}

	stopEvent, err := windows.CreateEvent(nil, 1 /* manual reset */, 0, nil)
	if err != nil {
		cleanup()
		return nil, ErrFailedToOpenBackendDevice
	}
	wd.stopEvent = stopEvent
	return wd, nil
}

// initStream opens and initializes one direction of the device.
func (wd *wasapiDevice) initStream(enum comObject, dataFlow uint32, id *DeviceID, loopback bool) (*wasapiStream, string, error) {
	isCaptureSide := dataFlow == eCapture || loopback
	sub := &wd.config.Playback
	if isCaptureSide {
		sub = &wd.config.Capture
	}

	dev, err := wasapiOpenDevice(enum, dataFlow, id)
	if err != nil {
		return nil, "", ErrNoDevice
	}
	defer dev.Release()

	name := ""
	if ps, perr := dev.openPropertyStore(); perr == nil {
		name, _ = ps.getStringValue(&pkeyDeviceFriendlyName)
		ps.Release()
	}

	if sub.ShareMode == ShareModeExclusive {
		return wd.initStreamExclusive(dev, sub, isCaptureSide, name)
	}

	// Prefer IAudioClient3 for small shared-mode periods.
	isClient3 := true
	client, err := dev.activate(&iidIAudioClient3)
	if err != nil {
		isClient3 = false
		client, err = dev.activate(&iidIAudioClient)
		if err != nil {
			return nil, name, ErrFailedToOpenBackendDevice
		}
	}

	mixFormat, err := client.getMixFormat()
	if err != nil {
		client.Release()
		return nil, name, ErrFailedToOpenBackendDevice
	}
	// Copy the mix format and free the COM allocation.
	wf := *mixFormat
	coTaskMemFree(unsafe.Pointer(mixFormat))

	format := wasapiFormatToNative(&wf)
	if format == FormatUnknown {
		client.Release()
		return nil, name, ErrFormatNotSupported
	}

	s := &wasapiStream{
		client:     client,
		format:     format,
		channels:   uint32(wf.Channels),
		sampleRate: wf.SamplesPerSec,
		bpf:        FrameSizeInBytes(format, uint32(wf.Channels)),
		isLoopback: loopback,
	}

	requestedPeriod := wd.device.calculatePeriodSizeInFrames(s.sampleRate)

	streamFlags := uint32(audclntStreamFlagsEventCallback)
	if loopback {
		streamFlags |= audclntStreamFlagsLoopback
	}

	initialized := false
	// IAudioClient3 small-period path. Not applicable to loopback, which
	// does not support InitializeSharedAudioStream with loopback flags on
	// all systems; fall through on failure.
	if isClient3 && !loopback {
		if _, _, minPeriod, maxPeriod, err := client.getSharedModeEnginePeriod(&wf); err == nil {
			period := requestedPeriod
			// When no explicit period was requested and the low-latency
			// profile is active, use the engine's minimum period (typically
			// 2-3ms) rather than the generic 10ms default.
			if wd.config.PeriodSizeInFrames == 0 && wd.config.PeriodSizeInMilliseconds == 0 &&
				wd.config.PerformanceProfile == PerformanceProfileLowLatency {
				period = minPeriod
			}
			if period < minPeriod {
				period = minPeriod
			}
			if period > maxPeriod {
				period = maxPeriod
			}
			if err := client.initializeSharedAudioStream(streamFlags, period, &wf); err == nil {
				initialized = true
				s.periodFrames = period
			}
		}
	}

	if !initialized {
		// Classic shared-mode initialization. Buffer duration in 100ns
		// units; use periods * requested period.
		if !wd.config.WASAPI.NoAutoConvertSRC {
			streamFlags |= audclntStreamFlagsAutoConvertPCM
		}
		if !wd.config.WASAPI.NoDefaultQualitySRC {
			streamFlags |= audclntStreamFlagsSrcDefaultQuality
		}
		periods := wd.config.Periods
		if periods == 0 {
			periods = DefaultPeriods
		}
		bufferDuration := int64(uint64(requestedPeriod) * uint64(periods) * 10000000 / uint64(s.sampleRate))
		if err := client.initialize(audclntShareModeShared, streamFlags, bufferDuration, 0, &wf); err != nil {
			client.Release()
			return nil, name, ErrFailedToOpenBackendDevice
		}
		s.periodFrames = requestedPeriod
	}

	if err := s.attachEventAndService(dataFlow == eCapture || loopback); err != nil {
		return nil, name, err
	}
	return s, name, nil
}

// attachEventAndService performs the common post-Initialize setup: reads
// the buffer size, creates and attaches the event handle, and acquires the
// render/capture service. On failure the stream's client is released.
func (s *wasapiStream) attachEventAndService(isCapture bool) error {
	bufferFrames, err := s.client.getBufferSize()
	if err != nil {
		s.client.Release()
		s.client = nil
		return ErrFailedToOpenBackendDevice
	}
	s.bufferFrames = bufferFrames
	if s.periodFrames == 0 || s.periodFrames > bufferFrames {
		s.periodFrames = bufferFrames
	}

	event, err := windows.CreateEvent(nil, 0 /* auto reset */, 0, nil)
	if err != nil {
		s.client.Release()
		s.client = nil
		return ErrFailedToOpenBackendDevice
	}
	if err := s.client.setEventHandle(event); err != nil {
		windows.CloseHandle(event)
		s.client.Release()
		s.client = nil
		return ErrFailedToOpenBackendDevice
	}
	s.event = event

	serviceIID := &iidIAudioRenderClient
	if isCapture {
		serviceIID = &iidIAudioCaptureClient
	}
	service, err := s.client.getService(serviceIID)
	if err != nil {
		windows.CloseHandle(event)
		s.event = 0
		s.client.Release()
		s.client = nil
		return ErrFailedToOpenBackendDevice
	}
	s.service = service
	return nil
}

// initStreamExclusive initializes one direction in exclusive mode:
// negotiate a natively supported format with IsFormatSupported, then
// initialize event-driven with equal buffer duration and periodicity,
// handling WASAPI's aligned-buffer re-initialization requirement.
func (wd *wasapiDevice) initStreamExclusive(dev comObject, sub *DeviceSubConfig, isCapture bool, name string) (*wasapiStream, string, error) {
	client, err := dev.activate(&iidIAudioClient)
	if err != nil {
		return nil, name, ErrFailedToOpenBackendDevice
	}

	// The device's physical format is the strongest exclusive candidate.
	var physical waveFormatExtensible
	havePhysical := false
	if ps, perr := dev.openPropertyStore(); perr == nil {
		if blob, berr := ps.getBlobValue(&pkeyAudioEngineDeviceFormat); berr == nil {
			physical, havePhysical = parseWaveFormatBlob(blob)
		}
		ps.Release()
	}

	wf, ok := wasapiFindExclusiveFormat(client, sub, wd.config.SampleRate, &physical, havePhysical)
	if !ok {
		client.Release()
		return nil, name, ErrFormatNotSupported
	}

	format := wasapiFormatToNative(&wf)
	if format == FormatUnknown {
		client.Release()
		return nil, name, ErrFormatNotSupported
	}

	// Event-driven exclusive mode requires bufferDuration == periodicity.
	defaultPeriod, minPeriod, perr := client.getDevicePeriod()
	rate := int64(wf.SamplesPerSec)
	requestedFrames := wd.device.calculatePeriodSizeInFrames(wf.SamplesPerSec)
	periodicity := (int64(requestedFrames)*10000000 + rate/2) / rate
	if perr == nil && periodicity < minPeriod {
		periodicity = minPeriod
	}

	const streamFlags = uint32(audclntStreamFlagsEventCallback)
	hr := client.initializeRaw(audclntShareModeExclusive, streamFlags, periodicity, periodicity, &wf)

	if uint32(hr) == audclntEBufferSizeNotAligned {
		// The driver wants an aligned buffer: it reports the aligned frame
		// count through GetBufferSize. The client must be re-created before
		// initializing again.
		if aligned, aerr := client.getBufferSize(); aerr == nil && aligned > 0 {
			periodicity = (int64(aligned)*10000000 + rate/2) / rate
		}
		client.Release()
		client, err = dev.activate(&iidIAudioClient)
		if err != nil {
			return nil, name, ErrFailedToOpenBackendDevice
		}
		hr = client.initializeRaw(audclntShareModeExclusive, streamFlags, periodicity, periodicity, &wf)
	}

	if uint32(hr) == audclntEInvalidDevicePeriod {
		// The requested period is out of the driver's range: retry with the
		// device's default period.
		client.Release()
		client, err = dev.activate(&iidIAudioClient)
		if err != nil {
			return nil, name, ErrFailedToOpenBackendDevice
		}
		p := defaultPeriod
		if p < minPeriod {
			p = minPeriod
		}
		hr = client.initializeRaw(audclntShareModeExclusive, streamFlags, p, p, &wf)
	}

	if hrFailed(hr) {
		client.Release()
		if r := hresultToResult(hr); r == ErrAlreadyInUse || r == ErrShareModeNotSupported {
			return nil, name, r
		}
		return nil, name, ErrFailedToOpenBackendDevice
	}

	s := &wasapiStream{
		client:     client,
		format:     format,
		channels:   uint32(wf.Channels),
		sampleRate: wf.SamplesPerSec,
		bpf:        FrameSizeInBytes(format, uint32(wf.Channels)),
		exclusive:  true,
	}
	if err := s.attachEventAndService(isCapture); err != nil {
		return nil, name, err
	}
	// In event-driven exclusive mode every event covers a full buffer.
	s.periodFrames = s.bufferFrames
	return s, name, nil
}

// wasapiFindExclusiveFormat probes formats against the device until one is
// accepted in exclusive mode. Priority: the caller's explicit request, the
// device's physical format, then a grid of common formats.
func wasapiFindExclusiveFormat(client comObject, sub *DeviceSubConfig, cfgRate uint32, physical *waveFormatExtensible, havePhysical bool) (waveFormatExtensible, bool) {
	physFormat := FormatUnknown
	physChannels := uint32(0)
	physRate := uint32(0)
	if havePhysical {
		physFormat = wasapiFormatToNative(physical)
		physChannels = uint32(physical.Channels)
		physRate = physical.SamplesPerSec
	}

	pick := func(v, fallback1, fallback2 uint32) uint32 {
		if v != 0 {
			return v
		}
		if fallback1 != 0 {
			return fallback1
		}
		return fallback2
	}

	var candidates []waveFormatExtensible

	// 1. The explicit request, with unspecified fields filled from the
	// physical format.
	if sub.Format != FormatUnknown || sub.Channels != 0 || cfgRate != 0 {
		f := sub.Format
		if f == FormatUnknown {
			f = physFormat
			if f == FormatUnknown {
				f = FormatS16
			}
		}
		candidates = append(candidates, nativeFormatToWasapi(
			f,
			pick(sub.Channels, physChannels, 2),
			pick(cfgRate, physRate, 48000),
		))
	}

	// 2. The physical format verbatim (preserves valid-bits subtleties like
	// 24-in-32 containers).
	if havePhysical {
		candidates = append(candidates, *physical)
	}

	// 3. Common exclusive formats.
	rates := []uint32{physRate, cfgRate, 48000, 44100, 96000}
	chans := []uint32{physChannels, sub.Channels, 2}
	for _, f := range []Format{FormatS24, FormatS16, FormatS32, FormatF32} {
		for _, r := range rates {
			if r == 0 {
				continue
			}
			for _, ch := range chans {
				if ch == 0 {
					continue
				}
				candidates = append(candidates, nativeFormatToWasapi(f, ch, r))
			}
		}
	}

	seen := make(map[[4]uint32]bool)
	for i := range candidates {
		c := &candidates[i]
		key := [4]uint32{uint32(c.FormatTag)<<16 | uint32(c.BitsPerSample), uint32(c.Channels), c.SamplesPerSec, c.SubFormat.Data1}
		if seen[key] {
			continue
		}
		seen[key] = true
		if client.isFormatSupported(audclntShareModeExclusive, c) {
			return *c, true
		}
	}
	return waveFormatExtensible{}, false
}

func (s *wasapiStream) close() {
	if s == nil {
		return
	}
	if s.service != nil {
		s.service.Release()
		s.service = nil
	}
	if s.client != nil {
		s.client.Release()
		s.client = nil
	}
	if s.event != 0 {
		windows.CloseHandle(s.event)
		s.event = 0
	}
}

func (wd *wasapiDevice) start() error {
	windows.ResetEvent(wd.stopEvent)

	// Pre-fill the render buffer to avoid an initial glitch.
	if wd.render != nil {
		if err := wd.prefillRender(); err != nil {
			return ErrFailedToStartBackendDevice
		}
		if err := wd.render.client.startClient(); err != nil {
			return ErrFailedToStartBackendDevice
		}
	}
	if wd.capture != nil {
		if err := wd.capture.client.startClient(); err != nil {
			if wd.render != nil {
				wd.render.client.stopClient()
			}
			return ErrFailedToStartBackendDevice
		}
	}

	wd.doneCh = make(chan struct{})
	go wd.audioThread(wd.doneCh)
	return nil
}

func (wd *wasapiDevice) prefillRender() error {
	s := wd.render
	// Shared mode: pre-queue two periods rather than the whole buffer
	// (classic-path buffers are three periods; filling all of it adds a
	// period of startup latency for no benefit since the event loop refills
	// every period). Exclusive mode fills its whole (single-period) buffer.
	frames := s.bufferFrames
	if !s.exclusive && s.periodFrames*2 < frames {
		frames = s.periodFrames * 2
	}
	buf, err := s.service.getRenderBuffer(frames)
	if err != nil {
		return err
	}
	out := unsafe.Slice(buf, int(frames)*s.bpf)
	wd.device.handlePlayback(out, frames)
	return s.service.releaseRenderBuffer(frames, 0)
}

func (wd *wasapiDevice) stop() error {
	windows.SetEvent(wd.stopEvent)
	if wd.doneCh != nil {
		<-wd.doneCh
		wd.doneCh = nil
	}
	var err error
	if wd.render != nil {
		if e := wd.render.client.stopClient(); e != nil {
			err = e
		}
		wd.render.client.resetClient()
	}
	if wd.capture != nil {
		if e := wd.capture.client.stopClient(); e != nil {
			err = e
		}
		wd.capture.client.resetClient()
	}
	return err
}

func (wd *wasapiDevice) uninit() error {
	if wd.doneCh != nil {
		windows.SetEvent(wd.stopEvent)
		<-wd.doneCh
		wd.doneCh = nil
	}
	if wd.render != nil {
		wd.render.close()
		wd.render = nil
	}
	if wd.capture != nil {
		wd.capture.close()
		wd.capture = nil
	}
	if wd.stopEvent != 0 {
		windows.CloseHandle(wd.stopEvent)
		wd.stopEvent = 0
	}
	return nil
}

// audioThread is the real-time loop servicing render and capture events.
func (wd *wasapiDevice) audioThread(done chan struct{}) {
	defer close(done)

	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	coInitializeEx()

	avrtHandle := enableProAudioThreadPriority()
	defer revertThreadPriority(avrtHandle)

	handles := make([]windows.Handle, 0, 3)
	handles = append(handles, wd.stopEvent)
	if wd.capture != nil {
		handles = append(handles, wd.capture.event)
	}
	if wd.render != nil {
		handles = append(handles, wd.render.event)
	}

	for {
		idx, err := windows.WaitForMultipleObjects(handles, false, windows.INFINITE)
		if err != nil {
			return
		}
		which := int(idx - windows.WAIT_OBJECT_0)
		if which < 0 || which >= len(handles) {
			return
		}
		if handles[which] == wd.stopEvent {
			return
		}

		if wd.capture != nil && handles[which] == wd.capture.event {
			if !wd.serviceCapture() {
				wd.deviceLost()
				return
			}
		}
		if wd.render != nil && handles[which] == wd.render.event {
			if !wd.serviceRender() {
				wd.deviceLost()
				return
			}
		}
	}
}

// serviceRender fills the available space in the render buffer.
func (wd *wasapiDevice) serviceRender() bool {
	s := wd.render
	var available uint32
	if s.exclusive {
		// Event-driven exclusive mode: each event hands us one full buffer.
		// GetCurrentPadding is not meaningful here.
		available = s.bufferFrames
	} else {
		padding, err := s.client.getCurrentPadding()
		if err != nil {
			return err != ErrNoDevice
		}
		available = s.bufferFrames - padding
	}
	if available == 0 {
		return true
	}
	buf, err := s.service.getRenderBuffer(available)
	if err != nil {
		// A spurious event in exclusive mode can leave no buffer to fill;
		// only a lost device is fatal.
		return err != ErrNoDevice
	}
	out := unsafe.Slice(buf, int(available)*s.bpf)
	wd.device.handlePlayback(out, available)
	if err := s.service.releaseRenderBuffer(available, 0); err != nil {
		return err != ErrNoDevice
	}
	return true
}

// serviceCapture drains all pending capture packets.
func (wd *wasapiDevice) serviceCapture() bool {
	s := wd.capture

	if s.exclusive {
		// GetNextPacketSize is shared-mode only; in event-driven exclusive
		// mode each event delivers one buffer via GetBuffer.
		data, frames, flags, err := s.service.getCaptureBuffer()
		if err != nil {
			return err != ErrNoDevice
		}
		if frames == 0 {
			return true
		}
		in := unsafe.Slice(data, int(frames)*s.bpf)
		if flags&audclntBufferFlagsSilent != 0 {
			SilencePCMFrames(in, uint64(frames), s.format, s.channels)
		}
		wd.device.handleCapture(in, frames)
		return s.service.releaseCaptureBuffer(frames) != ErrNoDevice
	}

	for {
		packet, err := s.service.getNextPacketSize()
		if err != nil {
			return err != ErrNoDevice
		}
		if packet == 0 {
			return true
		}
		data, frames, flags, err := s.service.getCaptureBuffer()
		if err != nil {
			return err != ErrNoDevice
		}
		if frames == 0 {
			return true
		}
		in := unsafe.Slice(data, int(frames)*s.bpf)
		if flags&audclntBufferFlagsSilent != 0 {
			SilencePCMFrames(in, uint64(frames), s.format, s.channels)
		}
		wd.device.handleCapture(in, frames)
		if err := s.service.releaseCaptureBuffer(frames); err != nil {
			return err != ErrNoDevice
		}
	}
}

// deviceLost transitions the device to stopped when the endpoint disappears
// (e.g. it was unplugged).
func (wd *wasapiDevice) deviceLost() {
	d := wd.device
	d.state.Store(uint32(DeviceStateStopped))
	if d.callbacks.Stop != nil {
		d.callbacks.Stop(d)
	}
	d.notify(DeviceNotificationTypeStopped)
}
