package secrets

// Test-only re-exports keep the public API minimal while still letting
// internal/secrets/*_test.go (external _test package) drive the
// auto-select algorithm with fake backends.

// BackendFactory is the same type as the unexported backendFactory but
// re-exported for tests so they can assemble a custom factory map.
type BackendFactory = backendFactory

// OpenWithFactories is the test-only entry point for openWithFactories.
// Production callers must use Open; this seam is the only way the
// auto-select algorithm can be exercised without touching the real OS
// keychain or shelling out to `pass`.
func OpenWithFactories(cfg Config, factories map[string]BackendFactory) (Store, error) {
	return openWithFactories(cfg, factories)
}

// AutoOrder returns the documented probe order so tests can pin the
// "keyring before file" expectation without re-typing the slice.
func AutoOrder() []string {
	out := make([]string, len(autoOrder))
	copy(out, autoOrder)
	return out
}

// ResetPassphraseCacheForTest empties the process-wide passphrase cache
// so each test starts from a clean slate. The cache is keyed by absolute
// vault path, so cross-test pollution is unlikely (t.TempDir paths are
// unique), but resetting defensively keeps a flake from "leaking" a
// passphrase between table-driven cases that share a HomeDir.
func ResetPassphraseCacheForTest() {
	processPassphraseCacheMu.Lock()
	defer processPassphraseCacheMu.Unlock()
	processPassphraseCache = map[string]string{}
}

// SetTTYPassphrasePrompterForTest swaps in a deterministic prompter so
// tests can drive the env-empty / TTY-empty / mismatch / confirm code
// paths without a real terminal. Returns a restore func() the test
// defers so prompter swaps cannot leak between tests.
func SetTTYPassphrasePrompterForTest(p func(needConfirm bool) (string, error)) func() {
	prev := ttyPassphrasePrompter
	ttyPassphrasePrompter = p
	return func() { ttyPassphrasePrompter = prev }
}

// SetAgeScryptLogNForTest lowers the scrypt work factor so the suite
// runs in seconds rather than minutes under -race -count=2. Production
// keeps the default (15) which is already a 32k-iteration KDF; tests
// drop to a low factor (10) because the goal is correctness, not
// brute-force resistance against a test fixture passphrase.
func SetAgeScryptLogNForTest(n int) func() {
	prev := ageScryptLogN
	ageScryptLogN = n
	return func() { ageScryptLogN = prev }
}

// SetTTYHooksForTest swaps the IsTerminal and ReadPassword hooks the
// production prompter calls, so passphrase_test.go can drive the
// no-TTY / mismatch / match / no-confirm branches without a real
// terminal. Returns a restore func() the test defers.
func SetTTYHooksForTest(
	isTerminal func(fd int) bool,
	readPassword func(fd int) ([]byte, error),
) func() {
	prevIsTerm := isTerminalFunc
	prevReadPw := readPasswordFunc
	isTerminalFunc = isTerminal
	readPasswordFunc = readPassword
	return func() {
		isTerminalFunc = prevIsTerm
		readPasswordFunc = prevReadPw
	}
}

// ReadPassphraseFromTTYForTest exposes the unexported
// readPassphraseFromTTY function so its branches (IsTerminal=false,
// confirm-mismatch, confirm-match, no-confirm) can be exercised
// directly. Without this seam every code path inside passphrase.go
// would have to be reached transitively through ageBackend, which
// makes the no-TTY and mismatch branches awkward to isolate.
func ReadPassphraseFromTTYForTest(needConfirm bool) (string, error) {
	return readPassphraseFromTTY(needConfirm)
}
