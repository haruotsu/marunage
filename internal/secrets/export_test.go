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
