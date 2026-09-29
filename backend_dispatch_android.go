//go:build android

package gominiaudio

// Android builds satisfy the "linux" build tag as well, so the PipeWire and
// PulseAudio backends are excluded by "!android" on their files and this
// dispatch stands in for backend_dispatch_linux.go.

// defaultBackends returns the platform's backend priority order.
func defaultBackends() []Backend {
	return []Backend{BackendAAudio, BackendNull}
}

// newContextBackend creates the context backend for the given backend ID.
func newContextBackend(b Backend) (contextBackend, error) {
	switch b {
	case BackendAAudio:
		return &aaudioContext{}, nil
	case BackendNull:
		return &nullContext{}, nil
	default:
		return nil, ErrBackendNotEnabled
	}
}
