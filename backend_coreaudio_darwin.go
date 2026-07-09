package gominiaudio

import (
	"math/bits"
	"runtime"
	"sync/atomic"
	"unsafe"
)

// CoreAudio backend. Device enumeration goes through the HAL
// (AudioObjectGetPropertyData); streaming uses AUHAL output units. The
// real-time render callbacks are implemented in assembly
// (coreaudio_callbacks_darwin_*.s): they copy through a lock-free ring
// buffer and signal a mach semaphore, so native code never calls into Go.

type coreaudioContext struct{}

func (c *coreaudioContext) init(*ContextConfig) error {
	if _, err := caDefaultDevice(false); err != nil {
		// A machine with no output device is unusual but not fatal for
		// context init; only fail when the HAL itself is unavailable.
		if _, err2 := caDefaultDevice(true); err2 != nil {
			return ErrFailedToInitBackend
		}
	}
	return nil
}

func (c *coreaudioContext) uninit() error      { return nil }
func (c *coreaudioContext) backendID() Backend { return BackendCoreAudio }

// caDefaultDevice returns the default output (or input) AudioObjectID.
func caDefaultDevice(capture bool) (uint32, error) {
	sel := kAudioHardwarePropertyDefaultOutputDevice
	if capture {
		sel = kAudioHardwarePropertyDefaultInputDevice
	}
	addr := audioObjectPropertyAddress{Selector: sel, Scope: kAudioObjectPropertyScopeGlobal, Element: kAudioObjectPropertyElementMain}
	var dev uint32
	size := uint32(4)
	if status := caGetPropertyData(kAudioObjectSystemObject, &addr, &size, unsafe.Pointer(&dev)); status != 0 || dev == 0 {
		return 0, ErrNoDevice
	}
	return dev, nil
}

// caDeviceChannels returns the channel count of a device for a scope.
func caDeviceChannels(devID uint32, capture bool) uint32 {
	scope := kAudioObjectPropertyScopeOutput
	if capture {
		scope = kAudioObjectPropertyScopeInput
	}
	addr := audioObjectPropertyAddress{Selector: kAudioDevicePropertyStreamConfiguration, Scope: scope, Element: kAudioObjectPropertyElementMain}
	var size uint32
	if caGetPropertyDataSize(devID, &addr, &size) != 0 || size == 0 {
		return 0
	}
	buf := make([]byte, size)
	if caGetPropertyData(devID, &addr, &size, unsafe.Pointer(&buf[0])) != 0 {
		return 0
	}
	// AudioBufferList: mNumberBuffers @0, buffers at @8, each 16 bytes with
	// mNumberChannels @+0.
	if len(buf) < 8 {
		return 0
	}
	n := loadU32(buf, 0)
	var channels uint32
	for i := uint32(0); i < n; i++ {
		off := 8 + int(i)*16
		if off+4 > len(buf) {
			break
		}
		channels += loadU32(buf, off)
	}
	return channels
}

// caDeviceName returns the device's display name.
func caDeviceName(devID uint32) string {
	addr := audioObjectPropertyAddress{Selector: kAudioObjectPropertyName, Scope: kAudioObjectPropertyScopeGlobal, Element: kAudioObjectPropertyElementMain}
	var cfStr uintptr
	size := uint32(8)
	if caGetPropertyData(devID, &addr, &size, unsafe.Pointer(&cfStr)) != 0 {
		return ""
	}
	return cfStringToGo(cfStr)
}

// caDeviceNominalRate returns the device's nominal sample rate.
func caDeviceNominalRate(devID uint32) uint32 {
	addr := audioObjectPropertyAddress{Selector: kAudioDevicePropertyNominalSampleRate, Scope: kAudioObjectPropertyScopeGlobal, Element: kAudioObjectPropertyElementMain}
	var rate float64
	size := uint32(8)
	if caGetPropertyData(devID, &addr, &size, unsafe.Pointer(&rate)) != 0 {
		return 0
	}
	return uint32(rate)
}

func (c *coreaudioContext) enumerateDevices() ([]DeviceInfo, []DeviceInfo, error) {
	addr := audioObjectPropertyAddress{Selector: kAudioHardwarePropertyDevices, Scope: kAudioObjectPropertyScopeGlobal, Element: kAudioObjectPropertyElementMain}
	var size uint32
	if caGetPropertyDataSize(kAudioObjectSystemObject, &addr, &size) != 0 {
		return nil, nil, ErrorGeneric
	}
	count := int(size / 4)
	ids := make([]uint32, count)
	if count > 0 {
		if caGetPropertyData(kAudioObjectSystemObject, &addr, &size, unsafe.Pointer(&ids[0])) != 0 {
			return nil, nil, ErrorGeneric
		}
	}

	defaultOut, _ := caDefaultDevice(false)
	defaultIn, _ := caDefaultDevice(true)

	var playback, capture []DeviceInfo
	for _, id := range ids {
		name := caDeviceName(id)
		rate := caDeviceNominalRate(id)
		if outCh := caDeviceChannels(id, false); outCh > 0 {
			playback = append(playback, DeviceInfo{
				ID:        deviceIDFromU32(id),
				Name:      name,
				IsDefault: id == defaultOut,
				Formats:   []DeviceNativeDataFormat{{Format: FormatF32, Channels: outCh, SampleRate: rate}},
			})
		}
		if inCh := caDeviceChannels(id, true); inCh > 0 {
			capture = append(capture, DeviceInfo{
				ID:        deviceIDFromU32(id),
				Name:      name,
				IsDefault: id == defaultIn,
				Formats:   []DeviceNativeDataFormat{{Format: FormatF32, Channels: inCh, SampleRate: rate}},
			})
		}
	}
	return playback, capture, nil
}

func (c *coreaudioContext) deviceInfo(deviceType DeviceType, id *DeviceID) (DeviceInfo, error) {
	playback, capture, err := c.enumerateDevices()
	if err != nil {
		return DeviceInfo{}, err
	}
	list := playback
	if deviceType == DeviceTypeCapture {
		list = capture
	}
	if id == nil || id.IsZero() {
		for _, d := range list {
			if d.IsDefault {
				return d, nil
			}
		}
		if len(list) > 0 {
			return list[0], nil
		}
		return DeviceInfo{}, ErrNoDevice
	}
	for _, d := range list {
		if d.ID.u32() == id.u32() {
			return d, nil
		}
	}
	return DeviceInfo{}, ErrNoDevice
}

/**************************************************************************
Streams
**************************************************************************/

// caAudioBufferList matches the memory layout of a single-buffer
// AudioBufferList.
type caAudioBufferList struct {
	numberBuffers uint32
	_             uint32
	channels      uint32
	byteSize      uint32
	data          uintptr
}

// caStream is one direction of a CoreAudio device.
type caStream struct {
	device    *Device
	unit      uintptr
	isCapture bool

	channels uint32
	rate     uint32
	bpf      int
	period   uint32

	ctrl   *caRingControl
	ring   []byte
	bounce []byte            // Capture render target.
	abl    *caAudioBufferList

	sem     uint32
	stopped atomic.Bool
	done    chan struct{}
	scratch []byte
}

// nextPow2 rounds up to a power of two.
func nextPow2(v int) int {
	if v <= 1 {
		return 1
	}
	return 1 << bits.Len(uint(v-1))
}

func caOpenStream(d *Device, cfg *DeviceConfig, isCapture bool) (*caStream, error) {
	channels := cfg.Playback.Channels
	subID := cfg.Playback.DeviceID
	if isCapture {
		channels = cfg.Capture.Channels
		subID = cfg.Capture.DeviceID
	}

	var devID uint32
	if subID != nil && !subID.IsZero() {
		devID = subID.u32()
	} else {
		var err error
		devID, err = caDefaultDevice(isCapture)
		if err != nil {
			return nil, ErrNoDevice
		}
	}

	if channels == 0 {
		channels = caDeviceChannels(devID, isCapture)
		if channels == 0 {
			channels = DefaultChannels
		}
		if channels > 8 {
			channels = 8
		}
	}
	rate := cfg.SampleRate
	if rate == 0 {
		rate = caDeviceNominalRate(devID)
		if rate == 0 {
			rate = DefaultSampleRate
		}
	}

	s := &caStream{
		device:    d,
		isCapture: isCapture,
		channels:  channels,
		rate:      rate,
		bpf:       int(channels) * 4,
		done:      make(chan struct{}),
	}
	s.period = d.calculatePeriodSizeInFrames(rate)

	// Locate the HAL output audio unit.
	desc := audioComponentDescription{
		Type:         kAudioUnitType_Output,
		SubType:      kAudioUnitSubType_HALOutput,
		Manufacturer: kAudioUnitManufacturer_Apple,
	}
	comp := caComponentFindNext(0, &desc)
	if comp == 0 {
		return nil, ErrFailedToOpenBackendDevice
	}
	if caComponentInstanceNew(comp, &s.unit) != 0 {
		return nil, ErrFailedToOpenBackendDevice
	}

	fail := func() (*caStream, error) {
		caComponentInstanceDispose(s.unit)
		return nil, ErrFailedToOpenBackendDevice
	}

	// Enable/disable IO per direction. Element 0 = output, 1 = input.
	enable := func(scope uint32, element uint32, on uint32) int32 {
		return caUnitSetProperty(s.unit, kAudioOutputUnitProperty_EnableIO, scope, element, unsafe.Pointer(&on), 4)
	}
	if isCapture {
		if enable(kAudioUnitScope_Input, 1, 1) != 0 {
			return fail()
		}
		if enable(kAudioUnitScope_Output, 0, 0) != 0 {
			return fail()
		}
	} else {
		if enable(kAudioUnitScope_Output, 0, 1) != 0 {
			return fail()
		}
		enable(kAudioUnitScope_Input, 1, 0)
	}

	// Bind the device.
	if caUnitSetProperty(s.unit, kAudioOutputUnitProperty_CurrentDevice, kAudioUnitScope_Global, 0, unsafe.Pointer(&devID), 4) != 0 {
		return fail()
	}

	// Ask the device for our period size.
	bufAddr := audioObjectPropertyAddress{Selector: kAudioDevicePropertyBufferFrameSize, Scope: kAudioObjectPropertyScopeGlobal, Element: kAudioObjectPropertyElementMain}
	period := s.period
	caSetPropertyData(devID, &bufAddr, 4, unsafe.Pointer(&period))

	// Client stream format: f32, interleaved, native endian.
	asbd := audioStreamBasicDescription{
		SampleRate:       float64(rate),
		FormatID:         kAudioFormatLinearPCM,
		FormatFlags:      kAudioFormatFlagIsFloat | kAudioFormatFlagIsPacked,
		BytesPerPacket:   channels * 4,
		FramesPerPacket:  1,
		BytesPerFrame:    channels * 4,
		ChannelsPerFrame: channels,
		BitsPerChannel:   32,
	}
	if isCapture {
		// Our format on the output side of the input element.
		if caUnitSetProperty(s.unit, kAudioUnitProperty_StreamFormat, kAudioUnitScope_Output, 1, unsafe.Pointer(&asbd), uint32(unsafe.Sizeof(asbd))) != 0 {
			return fail()
		}
	} else {
		// Our format on the input side of the output element.
		if caUnitSetProperty(s.unit, kAudioUnitProperty_StreamFormat, kAudioUnitScope_Input, 0, unsafe.Pointer(&asbd), uint32(unsafe.Sizeof(asbd))) != 0 {
			return fail()
		}
	}

	maxFrames := uint32(4096)
	caUnitSetProperty(s.unit, kAudioUnitProperty_MaximumFramesPerSlice, kAudioUnitScope_Global, 0, unsafe.Pointer(&maxFrames), 4)

	// Shared ring: 4 periods of capacity, power-of-two bytes. The playback
	// feeder only ever queues fillTargetPeriods of audio (see feeder), so
	// the extra capacity is headroom, not latency.
	ringBytes := nextPow2(int(s.period) * s.bpf * 4)
	s.ring = make([]byte, ringBytes)
	s.scratch = make([]byte, int(s.period)*s.bpf)

	sem, kr := semaphoreCreate()
	if kr != 0 {
		return fail()
	}
	s.sem = sem

	s.ctrl = &caRingControl{
		ringBase:  uintptr(unsafe.Pointer(&s.ring[0])),
		ringSize:  uint64(ringBytes),
		sem:       sem,
		semSignal: libc_semaphore_signal_addr,
		bpf:       uint32(s.bpf),
	}

	if isCapture {
		s.bounce = make([]byte, 4096*s.bpf)
		s.abl = &caAudioBufferList{
			numberBuffers: 1,
			channels:      channels,
			byteSize:      uint32(len(s.bounce)),
			data:          uintptr(unsafe.Pointer(&s.bounce[0])),
		}
		s.ctrl.renderFn = libc_AudioUnitRender_addr
		s.ctrl.audioUnit = s.unit
		s.ctrl.bounceABL = uintptr(unsafe.Pointer(s.abl))

		cb := auRenderCallbackStruct{Proc: caCaptureCallbackPtr, RefCon: uintptr(unsafe.Pointer(s.ctrl))}
		if caUnitSetProperty(s.unit, kAudioOutputUnitProperty_SetInputCallback, kAudioUnitScope_Global, 0, unsafe.Pointer(&cb), uint32(unsafe.Sizeof(cb))) != 0 {
			semaphoreDestroy(sem)
			return fail()
		}
	} else {
		cb := auRenderCallbackStruct{Proc: caPlaybackCallbackPtr, RefCon: uintptr(unsafe.Pointer(s.ctrl))}
		if caUnitSetProperty(s.unit, kAudioUnitProperty_SetRenderCallback, kAudioUnitScope_Input, 0, unsafe.Pointer(&cb), uint32(unsafe.Sizeof(cb))) != 0 {
			semaphoreDestroy(sem)
			return fail()
		}
	}

	if caUnitInitialize(s.unit) != 0 {
		semaphoreDestroy(sem)
		return fail()
	}

	go s.feeder()
	return s, nil
}

// feeder is the Go side of the ring: it produces (playback) or consumes
// (capture) audio whenever the assembly callback signals the semaphore.
func (s *caStream) feeder() {
	defer close(s.done)
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()

	mask := uint64(len(s.ring) - 1)
	periodBytes := uint64(s.period) * uint64(s.bpf)

	for {
		if semaphoreWait(s.sem) != 0 {
			return // Semaphore destroyed: shutting down.
		}
		if s.stopped.Load() {
			return
		}

		if s.isCapture {
			for {
				write := atomic.LoadUint64(&s.ctrl.writePos)
				read := atomic.LoadUint64(&s.ctrl.readPos)
				avail := write - read
				if avail == 0 {
					break
				}
				chunk := avail
				if chunk > uint64(len(s.scratch)) {
					chunk = uint64(len(s.scratch))
				}
				chunk -= chunk % uint64(s.bpf)
				if chunk == 0 {
					break
				}
				s.copyOut(read&mask, s.scratch[:chunk])
				atomic.StoreUint64(&s.ctrl.readPos, read+chunk)
				s.device.handleCapture(s.scratch[:chunk], uint32(chunk/uint64(s.bpf)))
			}
		} else {
			// Keep at most fillTargetPeriods queued. Filling the whole ring
			// would add its full capacity (~4 periods) to the output latency;
			// two periods absorbs scheduling jitter without that cost.
			target := caFillTargetPeriods * periodBytes
			for {
				write := atomic.LoadUint64(&s.ctrl.writePos)
				read := atomic.LoadUint64(&s.ctrl.readPos)
				queued := write - read
				if queued+periodBytes > target {
					break
				}
				s.device.handlePlayback(s.scratch[:periodBytes], s.period)
				s.copyIn(write&mask, s.scratch[:periodBytes])
				atomic.StoreUint64(&s.ctrl.writePos, write+periodBytes)
			}
		}
	}
}

// copyIn writes into the ring at the given offset with wraparound.
func (s *caStream) copyIn(off uint64, src []byte) {
	n := copy(s.ring[off:], src)
	if n < len(src) {
		copy(s.ring, src[n:])
	}
}

// copyOut reads from the ring at the given offset with wraparound.
func (s *caStream) copyOut(off uint64, dst []byte) {
	n := copy(dst, s.ring[off:])
	if n < len(dst) {
		copy(dst[n:], s.ring)
	}
}

// caFillTargetPeriods is how many periods of playback audio the feeder
// keeps queued in the ring. This bounds the added output latency to
// target*period while the ring's larger capacity remains as overrun
// headroom.
const caFillTargetPeriods = 2

// prefill primes the playback ring before starting the unit.
func (s *caStream) prefill() {
	if s.isCapture {
		return
	}
	periodBytes := uint64(s.period) * uint64(s.bpf)
	mask := uint64(len(s.ring) - 1)
	for i := 0; i < caFillTargetPeriods; i++ {
		write := atomic.LoadUint64(&s.ctrl.writePos)
		s.device.handlePlayback(s.scratch[:periodBytes], s.period)
		s.copyIn(write&mask, s.scratch[:periodBytes])
		atomic.StoreUint64(&s.ctrl.writePos, write+periodBytes)
	}
}

func (s *caStream) start() error {
	if caOutputUnitStart(s.unit) != 0 {
		return ErrFailedToStartBackendDevice
	}
	return nil
}

func (s *caStream) stop() error {
	if caOutputUnitStop(s.unit) != 0 {
		return ErrFailedToStopBackendDevice
	}
	return nil
}

func (s *caStream) close() {
	s.stopped.Store(true)
	caOutputUnitStop(s.unit)
	caUnitUninitialize(s.unit)
	caComponentInstanceDispose(s.unit)
	semaphoreDestroy(s.sem) // Wakes the feeder with an error.
	<-s.done
	runtime.KeepAlive(s.ctrl)
	runtime.KeepAlive(s.ring)
	runtime.KeepAlive(s.bounce)
	runtime.KeepAlive(s.abl)
}

/**************************************************************************
Device backend
**************************************************************************/

type coreaudioDevice struct {
	device   *Device
	playback *caStream
	capture  *caStream
}

func (c *coreaudioContext) newDevice(d *Device, config *DeviceConfig) (deviceBackend, error) {
	if d.deviceType == DeviceTypeLoopback {
		return nil, ErrDeviceTypeNotSupported // CoreAudio has no native loopback.
	}
	cd := &coreaudioDevice{device: d}

	if d.deviceType&DeviceTypePlayback != 0 {
		s, err := caOpenStream(d, config, false)
		if err != nil {
			return nil, err
		}
		cd.playback = s
		d.setInternalFormat(DeviceTypePlayback, FormatF32, s.channels, s.rate, nil, s.period, config.Periods, "CoreAudio Playback")
	}
	if d.deviceType&DeviceTypeCapture != 0 {
		s, err := caOpenStream(d, config, true)
		if err != nil {
			if cd.playback != nil {
				cd.playback.close()
			}
			return nil, err
		}
		cd.capture = s
		d.setInternalFormat(DeviceTypeCapture, FormatF32, s.channels, s.rate, nil, s.period, config.Periods, "CoreAudio Capture")
	}
	return cd, nil
}

func (cd *coreaudioDevice) start() error {
	if cd.playback != nil {
		cd.playback.prefill()
		if err := cd.playback.start(); err != nil {
			return err
		}
	}
	if cd.capture != nil {
		if err := cd.capture.start(); err != nil {
			if cd.playback != nil {
				cd.playback.stop()
			}
			return err
		}
	}
	return nil
}

func (cd *coreaudioDevice) stop() error {
	var err error
	if cd.playback != nil {
		if e := cd.playback.stop(); e != nil {
			err = e
		}
	}
	if cd.capture != nil {
		if e := cd.capture.stop(); e != nil {
			err = e
		}
	}
	return err
}

func (cd *coreaudioDevice) uninit() error {
	if cd.playback != nil {
		cd.playback.close()
		cd.playback = nil
	}
	if cd.capture != nil {
		cd.capture.close()
		cd.capture = nil
	}
	return nil
}
