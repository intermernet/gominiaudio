package gominiaudio

// defaultBackends returns the platform's backend priority order.
func defaultBackends() []Backend {
	return []Backend{BackendWASAPI, BackendNull}
}

// newContextBackend creates the context backend for the given backend ID.
func newContextBackend(b Backend) (contextBackend, error) {
	switch b {
	case BackendWASAPI:
		return &wasapiContext{}, nil
	case BackendNull:
		return &nullContext{}, nil
	default:
		return nil, ErrBackendNotEnabled
	}
}
