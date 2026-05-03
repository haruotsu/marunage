package secrets

func newEnvBackend(_ Config) (Store, error) {
	return nil, ErrUnsupported
}
