package secrets

// ageBackend will persist secrets to ~/.marunage/secrets.age, encrypted
// with a passphrase, so GUI-less servers without `pass` still get a
// usable backend. PR-30 ships a stub returning ErrUnsupported; PR-31
// owns the passphrase prompt, file format, and key derivation choices.
//
// TODO(PR-31): replace this stub with a real backend.

func newAgeBackend(_ Config) (Store, error) {
	return nil, ErrUnsupported
}
