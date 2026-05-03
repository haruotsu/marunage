package secrets

// keyringBackend wraps github.com/zalando/go-keyring so the OS-native
// secret store (macOS Keychain / freedesktop Secret Service /
// Windows Credential Manager) is the first thing auto-select tries.
//
// The wiring lands in a follow-up commit alongside the go-keyring
// dependency; for now Open is a stub that yields ErrUnsupported so the
// auto-select algorithm is testable without pulling the dep.

func newKeyringBackend(_ Config) (Store, error) {
	// Real implementation lands in the next commit on this branch.
	return nil, ErrUnsupported
}
