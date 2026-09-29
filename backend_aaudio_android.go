//go:build android

package gominiaudio

/*
#cgo LDFLAGS: -laaudio
#include <stdlib.h>
#include <aaudio/AAudio.h>
#include "aaudio_bridge_android.h"
*/
import "C"

import (
	"sync"
	"unsafe"

	"golang.org/x/sys/unix"
)

// AAudio backend for Android (API 26+).
//
// The realtime data callback is pure C (aaudio_bridge_android.c): it moves
// bytes between AAudio's buffer and an SPSC ring and signals an eventfd. Go
// feeds or drains that ring from a normal goroutine, so the platform's
// realtime thread never enters the Go runtime.
//
// AAudio has no device enumeration; that lives in Java's AudioManager. The
// backend therefore reports a single default playback and capture device and
// lets the platform route them, which is also what miniaudio's AAudio
// backend does.

type aaudioContext struct{}

func (c *aaudioContext) init(*ContextConfig) error {
	// AAudio is part of the platform from API 26 on. Creating and deleting
	// a builder is the cheapest way to confirm it is actually usable.
	var builder *C.AAudioStreamBuilder
	if res := C.AAudio_createStreamBuilder(&builder); res != C.AAUDIO_OK {
		return ErrFailedToInitBackend
	}
	C.AAudioStreamBuilder_delete(builder)
	return nil
}

func (c *aaudioContext) uninit() error      { return nil }
func (c *aaudioContext) backendID() Backend { return BackendAAudio }

func (c *aaudioContext) enumerateDevices() ([]DeviceInfo, []DeviceInfo, error) {
	playback := []DeviceInfo{{
		ID:        deviceIDFromString("aaudio-default-playback"),
		Name:      "Default Playback Device",
		IsDefault: true,
		Formats:   []DeviceNativeDataFormat{{Format: FormatUnknown, Channels: 0, SampleRate: 0}},
	}}
	capture := []DeviceInfo{{
		ID:        deviceIDFromString("aaudio-default-capture"),
		Name:      "Default Capture Device",
		IsDefault: true,
		Formats:   []DeviceNativeDataFormat{{Format: FormatUnknown, Channels: 0, SampleRate: 0}},
	}}
	return playback, capture, nil
}

func (c *aaudioContext) deviceInfo(deviceType DeviceType, id *DeviceID) (DeviceInfo, error) {
	playback, capture, _ := c.enumerateDevices()
	if deviceType == DeviceTypeCapture || deviceType == DeviceTypeLoopback {
		return capture[0], nil
	}
	return playback[0], nil
}

// aaudioFormatFromMA maps a library format onto AAudio's.
//
// Only PCM_I16 and PCM_FLOAT are used. The 24 and 32 bit integer formats
// only arrived in API 31, so naming them would break the build on older
// NDK headers and fail to open on older devices for no benefit: float is
// already the highest quality path AAudio offers, and the device's data
// converter bridges whatever the caller asked for. u8 has no AAudio
// equivalent at all and goes the same way.
func aaudioFormatFromMA(f Format) (C.aaudio_format_t, Format) {
	switch f {
	case FormatS16, FormatU8:
		return C.AAUDIO_FORMAT_PCM_I16, FormatS16
	}
	return C.AAUDIO_FORMAT_PCM_FLOAT, FormatF32
}

// aaudioFormatToMA maps an AAudio format back onto the library's.
func aaudioFormatToMA(f C.aaudio_format_t) Format {
	switch f {
	case C.AAUDIO_FORMAT_PCM_I16:
		return FormatS16
	case C.AAUDIO_FORMAT_PCM_FLOAT:
		return FormatF32
	}
	return FormatUnknown
}

// aaudioStream is one playback or capture stream plus its ring.
type aaudioStream struct {
	stream  *C.AAudioStream
	ring    *C.gma_ring
	ringBuf unsafe.Pointer
	evfd    int
	capture bool

	device   *Device
	format   Format
	channels uint32
	rate     uint32
	bpf      int
	period   uint32

	mu      sync.Mutex
	running bool
	stopCh  chan struct{}
	doneCh  chan struct{}

	scratch []byte
}

// nextPow2 rounds n up to a power of two, which the ring's mask requires.
func nextPow2(n int) int {
	p := 1
	for p < n {
		p <<= 1
	}
	return p
}

// aaudioOpenStream builds, opens and configures one stream.
func aaudioOpenStream(d *Device, cfg *DeviceConfig, capture bool) (*aaudioStream, error) {
	format := cfg.Playback.Format
	channels := cfg.Playback.Channels
	if capture {
		format = cfg.Capture.Format
		channels = cfg.Capture.Channels
	}
	if format == FormatUnknown {
		format = DefaultFormat
	}
	if channels == 0 {
		channels = DefaultChannels
	}
	rate := cfg.SampleRate
	if rate == 0 {
		rate = DefaultSampleRate
	}
	aaFormat, maFormat := aaudioFormatFromMA(format)
	period := d.calculatePeriodSizeInFrames(rate)

	var builder *C.AAudioStreamBuilder
	if res := C.AAudio_createStreamBuilder(&builder); res != C.AAUDIO_OK {
		return nil, ErrFailedToOpenBackendDevice
	}
	defer C.AAudioStreamBuilder_delete(builder)

	direction := C.aaudio_direction_t(C.AAUDIO_DIRECTION_OUTPUT)
	if capture {
		direction = C.AAUDIO_DIRECTION_INPUT
	}
	C.AAudioStreamBuilder_setDirection(builder, direction)
	C.AAudioStreamBuilder_setSampleRate(builder, C.int32_t(rate))
	C.AAudioStreamBuilder_setChannelCount(builder, C.int32_t(channels))
	C.AAudioStreamBuilder_setFormat(builder, aaFormat)
	C.AAudioStreamBuilder_setSharingMode(builder, C.AAUDIO_SHARING_MODE_SHARED)

	// The low latency path is what makes AAudio worth using; the
	// conservative profile opts out of it.
	if cfg.PerformanceProfile == PerformanceProfileConservative {
		C.AAudioStreamBuilder_setPerformanceMode(builder, C.AAUDIO_PERFORMANCE_MODE_NONE)
	} else {
		C.AAudioStreamBuilder_setPerformanceMode(builder, C.AAUDIO_PERFORMANCE_MODE_LOW_LATENCY)
	}

	s := &aaudioStream{capture: capture, device: d, evfd: -1}

	// The ring is the handoff point with the realtime callback, so it is
	// allocated with malloc rather than in Go memory: cgo forbids passing
	// Go pointers that themselves contain Go pointers, and the callback
	// retains this one for the stream's lifetime.
	s.ring = (*C.gma_ring)(C.calloc(1, C.size_t(unsafe.Sizeof(C.gma_ring{}))))
	if s.ring == nil {
		return nil, ErrOutOfMemory
	}

	bpf := FrameSizeInBytes(maFormat, channels)
	periods := cfg.Periods
	if periods == 0 {
		periods = DefaultPeriods
	}
	capacity := nextPow2(int(period) * bpf * int(periods))
	s.ringBuf = C.calloc(1, C.size_t(capacity))
	if s.ringBuf == nil {
		s.freeRing()
		return nil, ErrOutOfMemory
	}
	s.ring.buf = (*C.uint8_t)(s.ringBuf)
	s.ring.capacity = C.int32_t(capacity)
	s.ring.mask = C.int32_t(capacity - 1)
	s.ring.frame_size = C.int32_t(bpf)

	// The callback signals this eventfd; the feeder goroutine blocks on it
	// with an ordinary read syscall, so waking Go costs no cgo transition.
	evfd, err := unix.Eventfd(0, unix.EFD_CLOEXEC|unix.EFD_NONBLOCK)
	if err != nil {
		s.freeRing()
		return nil, ErrFailedToOpenBackendDevice
	}
	s.evfd = evfd
	s.ring.evfd = C.int(evfd)

	captureFlag := C.int(0)
	if capture {
		captureFlag = 1
	}
	C.gma_set_stream_callbacks(builder, s.ring, captureFlag)

	var stream *C.AAudioStream
	if res := C.AAudioStreamBuilder_openStream(builder, &stream); res != C.AAUDIO_OK {
		s.cleanup()
		return nil, ErrFailedToOpenBackendDevice
	}
	s.stream = stream

	// Take the format the platform actually granted, which may differ from
	// the request; the device's converter reconciles it with the caller.
	s.format = aaudioFormatToMA(C.AAudioStream_getFormat(stream))
	if s.format == FormatUnknown {
		s.format = maFormat
	}
	s.channels = uint32(C.AAudioStream_getChannelCount(stream))
	s.rate = uint32(C.AAudioStream_getSampleRate(stream))
	s.bpf = FrameSizeInBytes(s.format, s.channels)
	s.ring.frame_size = C.int32_t(s.bpf)

	// Align the reported period with the hardware burst so the callback and
	// the caller's callback cadence agree.
	burst := uint32(C.AAudioStream_getFramesPerBurst(stream))
	if burst > 0 {
		s.period = burst
	} else {
		s.period = period
	}

	// Size the stream buffer at a small multiple of the burst: enough to
	// absorb scheduling jitter without adding needless latency.
	C.AAudioStream_setBufferSizeInFrames(stream, C.int32_t(s.period*periods))

	return s, nil
}

// freeRing releases the ring and its backing buffer.
func (s *aaudioStream) freeRing() {
	if s.ringBuf != nil {
		C.free(s.ringBuf)
		s.ringBuf = nil
	}
	if s.ring != nil {
		C.free(unsafe.Pointer(s.ring))
		s.ring = nil
	}
}

// cleanup releases everything the stream owns.
func (s *aaudioStream) cleanup() {
	if s.stream != nil {
		C.AAudioStream_close(s.stream)
		s.stream = nil
	}
	if s.evfd >= 0 {
		unix.Close(s.evfd)
		s.evfd = -1
	}
	s.freeRing()
}

// start begins the stream and its transfer goroutine.
func (s *aaudioStream) start() error {
	s.mu.Lock()
	if s.running {
		s.mu.Unlock()
		return nil
	}
	s.running = true
	s.stopCh = make(chan struct{})
	s.doneCh = make(chan struct{})
	stop, done := s.stopCh, s.doneCh
	s.mu.Unlock()

	if !s.capture {
		// Prime the ring so the first callbacks have audio to take; a cold
		// start would otherwise underrun straight away.
		s.fill()
	}

	if res := C.AAudioStream_requestStart(s.stream); res != C.AAUDIO_OK {
		s.mu.Lock()
		s.running = false
		s.mu.Unlock()
		close(done)
		return ErrFailedToStartBackendDevice
	}

	go s.transferLoop(stop, done)
	return nil
}

// stop halts the stream and waits for the transfer goroutine.
func (s *aaudioStream) stop() error {
	s.mu.Lock()
	if !s.running {
		s.mu.Unlock()
		return nil
	}
	s.running = false
	stop, done := s.stopCh, s.doneCh
	s.mu.Unlock()

	close(stop)
	// Nudge the eventfd so a blocked feeder notices the stop promptly.
	s.wake()
	<-done

	if res := C.AAudioStream_requestStop(s.stream); res != C.AAUDIO_OK {
		return ErrFailedToStopBackendDevice
	}
	return nil
}

// wake posts to the eventfd from the Go side.
func (s *aaudioStream) wake() {
	if s.evfd < 0 {
		return
	}
	var one [8]byte
	one[7] = 1
	unix.Write(s.evfd, one[:])
}

// waitForCallback blocks until the callback signals, draining the counter.
// It returns false when the caller should stop.
func (s *aaudioStream) waitForCallback(stop chan struct{}) bool {
	var buf [8]byte
	fds := []unix.PollFd{{Fd: int32(s.evfd), Events: unix.POLLIN}}
	for {
		select {
		case <-stop:
			return false
		default:
		}
		// The timeout bounds the wait so a stalled stream still rechecks
		// the stop channel and the error flag.
		n, err := unix.Poll(fds, 100)
		if err != nil {
			if err == unix.EINTR {
				continue // A signal, not a failure.
			}
			return false
		}
		if n > 0 {
			// Draining is best effort: the counter only gates wakeups.
			unix.Read(s.evfd, buf[:])
		}
		return true
	}
}

// transferLoop moves audio between the device callback and the ring.
func (s *aaudioStream) transferLoop(stop, done chan struct{}) {
	defer close(done)
	for {
		select {
		case <-stop:
			return
		default:
		}
		if C.gma_ring_error(s.ring) != 0 {
			s.device.notify(DeviceNotificationTypeStopped)
			return
		}
		if s.capture {
			s.drain()
		} else {
			s.fill()
		}
		if !s.waitForCallback(stop) {
			return
		}
	}
}

// fill tops the playback ring up from the device callback.
func (s *aaudioStream) fill() {
	periodBytes := int(s.period) * s.bpf
	if cap(s.scratch) < periodBytes {
		s.scratch = make([]byte, periodBytes)
	}
	for {
		writable := int(C.gma_ring_writable(s.ring))
		if writable < periodBytes {
			return
		}
		buf := s.scratch[:periodBytes]
		s.device.handlePlayback(buf, s.period)
		C.gma_ring_write(s.ring, (*C.uint8_t)(unsafe.Pointer(&buf[0])), C.int32_t(len(buf)))
	}
}

// drain hands captured audio from the ring to the device callback.
func (s *aaudioStream) drain() {
	periodBytes := int(s.period) * s.bpf
	if cap(s.scratch) < periodBytes {
		s.scratch = make([]byte, periodBytes)
	}
	for {
		if int(C.gma_ring_readable(s.ring)) < periodBytes {
			return
		}
		buf := s.scratch[:periodBytes]
		C.gma_ring_read(s.ring, (*C.uint8_t)(unsafe.Pointer(&buf[0])), C.int32_t(len(buf)))
		s.device.handleCapture(buf, s.period)
	}
}

// close stops and releases the stream.
func (s *aaudioStream) close() {
	s.stop()
	s.cleanup()
}

// aaudioDevice is the deviceBackend for AAudio.
type aaudioDevice struct {
	playback *aaudioStream
	capture  *aaudioStream
}

func (c *aaudioContext) newDevice(d *Device, config *DeviceConfig) (deviceBackend, error) {
	dev := &aaudioDevice{}

	if d.deviceType&DeviceTypePlayback != 0 {
		s, err := aaudioOpenStream(d, config, false)
		if err != nil {
			return nil, err
		}
		dev.playback = s
		d.setInternalFormat(DeviceTypePlayback, s.format, s.channels, s.rate, nil,
			s.period, config.Periods, "AAudio Playback")
	}
	if d.deviceType&DeviceTypeCapture != 0 || d.deviceType == DeviceTypeLoopback {
		s, err := aaudioOpenStream(d, config, true)
		if err != nil {
			if dev.playback != nil {
				dev.playback.close()
			}
			return nil, err
		}
		dev.capture = s
		d.setInternalFormat(DeviceTypeCapture, s.format, s.channels, s.rate, nil,
			s.period, config.Periods, "AAudio Capture")
	}
	return dev, nil
}

func (ad *aaudioDevice) start() error {
	if ad.capture != nil {
		if err := ad.capture.start(); err != nil {
			return err
		}
	}
	if ad.playback != nil {
		if err := ad.playback.start(); err != nil {
			if ad.capture != nil {
				ad.capture.stop()
			}
			return err
		}
	}
	return nil
}

func (ad *aaudioDevice) stop() error {
	var firstErr error
	if ad.playback != nil {
		if err := ad.playback.stop(); err != nil {
			firstErr = err
		}
	}
	if ad.capture != nil {
		if err := ad.capture.stop(); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

func (ad *aaudioDevice) uninit() error {
	if ad.playback != nil {
		ad.playback.close()
		ad.playback = nil
	}
	if ad.capture != nil {
		ad.capture.close()
		ad.capture = nil
	}
	return nil
}
