package gominiaudio

import "golang.org/x/sys/unix"

// defaultBackends returns the platform's backend priority order.
func defaultBackends() []Backend {
	return []Backend{BackendPipeWire, BackendNull}
}

// newContextBackend creates the context backend for the given backend ID.
func newContextBackend(b Backend) (contextBackend, error) {
	switch b {
	case BackendPipeWire:
		return &pipewireContext{}, nil
	case BackendNull:
		return &nullContext{}, nil
	default:
		return nil, ErrBackendNotEnabled
	}
}

// nanotime returns the monotonic clock in nanoseconds, used for activation
// profiling timestamps.
func nanotime() int64 {
	var ts unix.Timespec
	if err := unix.ClockGettime(unix.CLOCK_MONOTONIC, &ts); err != nil {
		return 0
	}
	return ts.Nano()
}
