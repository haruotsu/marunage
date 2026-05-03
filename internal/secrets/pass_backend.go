package secrets

// passBackend will wrap the UNIX `pass` password store for headless Linux
// servers. This file is intentionally a stub on PR-30: the full
// implementation (probe `pass` binary, GPG passphrase prompt, store/list
// under password-store/marunage/) lands in PR-31 alongside age.
//
// TODO(PR-31): replace this stub with a real backend.

func newPassBackend(_ Config) (Store, error) {
	return nil, ErrUnsupported
}
