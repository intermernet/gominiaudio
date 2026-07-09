package gominiaudio

import (
	"sync"
	"unsafe"
)

// DeviceID mirrors ma_device_id: an opaque backend-specific identifier.
// WASAPI uses UTF-16 endpoint ID strings, PipeWire uses node serials,
// CoreAudio uses AudioObjectIDs; all are encoded into the byte array.
type DeviceID [MaxDeviceIDLength]byte

// String returns a printable form of the ID (backend specific).
func (id DeviceID) String() string {
	// Trim trailing zeros.
	n := len(id)
	for n > 0 && id[n-1] == 0 {
		n--
	}
	return string(id[:n])
}

// IsZero reports whether the ID is the zero value (i.e. "use default").
func (id DeviceID) IsZero() bool {
	for _, b := range id {
		if b != 0 {
			return false
		}
	}
	return true
}

// deviceIDFromString builds a DeviceID from a string identifier.
func deviceIDFromString(s string) DeviceID {
	var id DeviceID
	copy(id[:], s)
	return id
}

// deviceIDFromU32 builds a DeviceID from a numeric identifier.
func deviceIDFromU32(v uint32) DeviceID {
	var id DeviceID
	*(*uint32)(unsafe.Pointer(&id[0])) = v
	return id
}

func (id DeviceID) u32() uint32 {
	return *(*uint32)(unsafe.Pointer(&id[0]))
}

// DeviceNativeDataFormat mirrors the nativeDataFormats element of
// ma_device_info: one format configuration natively supported by a device.
type DeviceNativeDataFormat struct {
	Format     Format // Sample format. FormatUnknown means all formats are supported.
	Channels   uint32 // 0 means all channel counts are supported.
	SampleRate uint32 // 0 means all sample rates are supported.
	Flags      uint32
}

// DeviceInfo mirrors ma_device_info.
type DeviceInfo struct {
	ID        DeviceID
	Name      string
	IsDefault bool
	Formats   []DeviceNativeDataFormat
}

// ThreadPriority mirrors ma_thread_priority.
type ThreadPriority int32

const (
	ThreadPriorityIdle     ThreadPriority = -5
	ThreadPriorityLowest   ThreadPriority = -4
	ThreadPriorityLow      ThreadPriority = -3
	ThreadPriorityNormal   ThreadPriority = -2
	ThreadPriorityHigh     ThreadPriority = -1
	ThreadPriorityHighest  ThreadPriority = 0
	ThreadPriorityRealtime ThreadPriority = 1
	ThreadPriorityDefault  ThreadPriority = 0
)

// ContextConfig mirrors ma_context_config, limited to the options relevant
// to the implemented backends.
type ContextConfig struct {
	ThreadPriority ThreadPriority
}

// contextBackend is the interface a platform backend implements for device
// enumeration and device creation.
type contextBackend interface {
	// init prepares the backend. Returns ErrNoBackend or a failure code if
	// the backend is unavailable on this system.
	init(config *ContextConfig) error
	uninit() error
	backendID() Backend

	// enumerateDevices returns the available playback and capture devices.
	enumerateDevices() (playback, capture []DeviceInfo, err error)

	// deviceInfo returns detailed information about a device. A nil id means
	// the default device.
	deviceInfo(deviceType DeviceType, id *DeviceID) (DeviceInfo, error)

	// newDevice creates (but does not start) a backend device. The backend
	// must fill in the device's internal (native) format descriptors by
	// calling d.setInternalFormat…
	newDevice(d *Device, config *DeviceConfig) (deviceBackend, error)
}

// deviceBackend is the per-device half of the backend interface.
type deviceBackend interface {
	start() error
	stop() error
	uninit() error
}

// Context mirrors ma_context. It is used for device enumeration and as the
// factory for devices.
type Context struct {
	mu       sync.Mutex
	backend  contextBackend
	config   ContextConfig
	devices  map[*Device]struct{}
	uninited bool
}

// InitContext mirrors ma_context_init. backends lists the backends to try
// in priority order; nil means the platform default order. Mirrors malgo's
// InitContext shape.
func InitContext(backends []Backend, config *ContextConfig) (*Context, error) {
	cfg := ContextConfig{}
	if config != nil {
		cfg = *config
	}
	if backends == nil {
		backends = defaultBackends()
	}

	var firstErr error
	for _, b := range backends {
		cb, err := newContextBackend(b)
		if err != nil {
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		if err := cb.init(&cfg); err != nil {
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		return &Context{
			backend: cb,
			config:  cfg,
			devices: make(map[*Device]struct{}),
		}, nil
	}
	if firstErr == nil {
		firstErr = ErrNoBackend
	}
	return nil, firstErr
}

// Uninit mirrors ma_context_uninit. All devices created from the context
// must be uninitialized first.
func (ctx *Context) Uninit() error {
	ctx.mu.Lock()
	defer ctx.mu.Unlock()
	if ctx.uninited {
		return ErrInvalidOperation
	}
	if len(ctx.devices) != 0 {
		return ErrInvalidOperation
	}
	ctx.uninited = true
	return ctx.backend.uninit()
}

// Backend mirrors ma_context_get_backend.
func (ctx *Context) Backend() Backend { return ctx.backend.backendID() }

// Devices returns the devices of the given type (DeviceTypePlayback or
// DeviceTypeCapture), mirroring malgo's Context.Devices and
// ma_context_get_devices.
func (ctx *Context) Devices(deviceType DeviceType) ([]DeviceInfo, error) {
	playback, capture, err := ctx.backend.enumerateDevices()
	if err != nil {
		return nil, err
	}
	switch deviceType {
	case DeviceTypePlayback:
		return playback, nil
	case DeviceTypeCapture, DeviceTypeLoopback:
		return capture, nil
	default:
		return nil, ErrInvalidArgs
	}
}

// DeviceInfo mirrors ma_context_get_device_info. Pass nil for the default
// device of the given type.
func (ctx *Context) DeviceInfo(deviceType DeviceType, id *DeviceID) (DeviceInfo, error) {
	return ctx.backend.deviceInfo(deviceType, id)
}

func (ctx *Context) registerDevice(d *Device) {
	ctx.mu.Lock()
	ctx.devices[d] = struct{}{}
	ctx.mu.Unlock()
}

func (ctx *Context) unregisterDevice(d *Device) {
	ctx.mu.Lock()
	delete(ctx.devices, d)
	ctx.mu.Unlock()
}
