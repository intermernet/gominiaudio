package gominiaudio

// defaultBackends returns the platform's backend priority order.
func defaultBackends() []Backend {
	return []Backend{BackendCoreAudio, BackendNull}
}

// newContextBackend creates the context backend for the given backend ID.
func newContextBackend(b Backend) (contextBackend, error) {
	switch b {
	case BackendCoreAudio:
		return &coreaudioContext{}, nil
	case BackendNull:
		return &nullContext{}, nil
	default:
		return nil, ErrBackendNotEnabled
	}
}
