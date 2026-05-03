// Package secrets owns marunage's per-source token storage. The package
// exports a single Store interface that downstream source plugins call to
// look up, persist, and revoke tokens, plus an Open entry point that
// resolves configuration into a concrete backend.
//
// Why this exists: docs/requirement.md "シークレット保存 — クロスプラット
// フォーム前提" (around lines 463-491) requires that marunage runs the same
// way on macOS, Linux GUI, Linux headless, WSL, Docker, and CI without the
// user thinking about which native key store is available. The Store
// interface is the seam that makes that promise enforceable, and Open is
// the auto-select algorithm that picks a usable backend per environment.
//
// Auto-select algorithm (cfg.Backend == "auto"):
//
//	1. keyring  — OS-native (macOS Keychain / Secret Service / Windows CM)
//	2. pass     — UNIX `pass` password store (Linux headless servers)
//	3. age      — passphrase-protected age file (no GUI, no pass)
//	4. file     — 0600 plaintext fallback (~/.marunage/secrets/<name>.json)
//	5. (env is intentionally NOT probed here — see below)
//
// The first backend whose probeAvailable returns nil wins. env is excluded
// from "auto" because reading process environment is the documented opt-in
// for CI / Docker only (`marunage setup --headless`); silently picking it
// would mask a misconfigured workstation as a working install.
//
// Backends marked Stub here (pass, age) return ErrUnsupported until PR-31
// lands the real implementations; the auto-select loop treats
// ErrUnsupported as "skip and try the next one" rather than as a fatal
// error so a workstation without `pass` installed still resolves to the
// keyring or file backend.
//
// Explicit cfg.Backend overrides ("keyring" / "pass" / "age" / "file" /
// "env") bypass the probe entirely and surface ErrUnsupported as a real
// error — the user asked for a specific backend and we must not silently
// substitute a different one.
package secrets

import (
	"errors"
	"fmt"

	"github.com/haruotsu/marunage/internal/config"
)

// Store is the read/write interface every secrets backend implements.
// Get is required to distinguish "not present" (ok=false, err=nil) from
// "backend failure" (err != nil) so callers can decide whether to prompt
// for setup or surface a hard error.
type Store interface {
	// Get returns the stored value for name. ok is false (with a nil
	// error) when the secret simply has not been set yet; a non-nil
	// error means the backend itself failed.
	Get(name string) (value string, ok bool, err error)
	// Set persists value under name, overwriting any existing entry.
	Set(name, value string) error
	// Delete removes name. Deleting a missing entry is not an error so
	// the operation is idempotent for cleanup paths.
	Delete(name string) error
	// List returns the names of every entry currently stored.
	List() ([]string, error)
	// Backend returns the short identifier of the concrete backend
	// ("keyring" / "file" / etc.). `marunage setup --list` prints this so
	// the user always knows where their tokens live.
	Backend() string
}

// Config selects which backend Open should return. The struct mirrors the
// [secrets] table in config.toml so callers can pass cfg.Secrets directly
// without unpacking fields.
type Config struct {
	// Backend is "auto" or one of keyring/pass/age/file/env. Validated
	// upstream by config.Validate; Open re-checks for defense in depth.
	Backend string
	// HomeDir overrides the directory used by file/age backends. Empty
	// means "use the user's HOME". Tests pass a t.TempDir() here so they
	// never touch ~/.marunage.
	HomeDir string
}

// ErrUnsupported is returned by backends that are recognised by name but
// cannot run in the current environment (missing binary, stub awaiting a
// later PR, etc.). Auto-select treats this as "skip and try the next
// backend"; an explicit Backend selection surfaces it as a hard error.
var ErrUnsupported = errors.New("secrets backend not available in this environment")

// ErrReadOnly is returned by Set / Delete on backends that can only read
// (currently env). Callers should surface this as "configure the source
// elsewhere"; mutating a process environment from inside the binary is
// out of scope and would be lost on restart anyway.
var ErrReadOnly = errors.New("secrets backend is read-only")

// allowedBackends is the validated set of cfg.Backend values. Kept
// in-sync with config.allowedSecretsBackends — the config package owns
// the schema-level validation, this list is a defensive second check so
// Open never trusts an unvalidated string from a hand-edited file.
var allowedBackends = map[string]struct{}{
	"auto":    {},
	"keyring": {},
	"pass":    {},
	"age":     {},
	"file":    {},
	"env":     {},
}

// autoOrder is the deterministic probe order for cfg.Backend == "auto".
// env is intentionally absent — see the package doc comment.
var autoOrder = []string{"keyring", "pass", "age", "file"}

// backendFactory builds a concrete Store for one named backend. Splitting
// out a function map (rather than a giant switch in Open) makes it
// straightforward for tests to swap factories via withFactories.
type backendFactory func(cfg Config) (Store, error)

// defaultFactories returns the production factory map. Tests construct
// their own map and call openWithFactories to inject fakes for the
// auto-select algorithm without touching the OS keychain.
func defaultFactories() map[string]backendFactory {
	return map[string]backendFactory{
		"keyring": newKeyringBackend,
		"pass":    newPassBackend,
		"age":     newAgeBackend,
		"file":    newFileBackend,
		"env":     newEnvBackend,
	}
}

// Open resolves cfg into a usable Store. Validation happens before any
// backend is constructed so a typo in config.toml fails before the file
// backend creates ~/.marunage/secrets/.
func Open(cfg Config) (Store, error) {
	return openWithFactories(cfg, defaultFactories())
}

// OpenWithAuditor is Open plus an Auditor that receives one AuditEvent
// per Set / Delete. Pass internal/logging.AuditLog (or any other
// config.Auditor) so secrets mutations land in audit.log alongside the
// existing config.* events. Get / List intentionally do not audit -
// reads are the hot path and would drown the log.
//
// Passing a nil Auditor degrades to NopAuditor so callers in early CLI
// glue can wire OpenWithAuditor unconditionally.
func OpenWithAuditor(cfg Config, auditor config.Auditor) (Store, error) {
	store, err := Open(cfg)
	if err != nil {
		return nil, err
	}
	if auditor == nil {
		auditor = config.NopAuditor{}
	}
	return &auditingStore{inner: store, auditor: auditor}, nil
}

// openWithFactories is the testable core of Open: tests pass in fakes
// for individual backends to drive the auto-select algorithm without
// hitting the real OS keychain.
func openWithFactories(cfg Config, factories map[string]backendFactory) (Store, error) {
	if cfg.Backend == "" {
		cfg.Backend = "auto"
	}
	if _, ok := allowedBackends[cfg.Backend]; !ok {
		return nil, fmt.Errorf("secrets: unknown backend %q (want one of auto/keyring/pass/age/file/env)", cfg.Backend)
	}

	if cfg.Backend != "auto" {
		factory, ok := factories[cfg.Backend]
		if !ok {
			return nil, fmt.Errorf("secrets: backend %q has no factory registered", cfg.Backend)
		}
		store, err := factory(cfg)
		if err != nil {
			return nil, fmt.Errorf("secrets: open %s: %w", cfg.Backend, err)
		}
		return store, nil
	}

	// Auto: probe in documented order, skip ErrUnsupported, surface any
	// other error so a corrupt keyring keychain (for example) does not
	// silently fall through to the plaintext file backend.
	var probeErrs []string
	for _, name := range autoOrder {
		factory, ok := factories[name]
		if !ok {
			continue
		}
		store, err := factory(cfg)
		if err == nil {
			return store, nil
		}
		if errors.Is(err, ErrUnsupported) {
			continue
		}
		probeErrs = append(probeErrs, fmt.Sprintf("%s: %v", name, err))
	}
	if len(probeErrs) > 0 {
		return nil, fmt.Errorf("secrets: no usable backend (errors: %v)", probeErrs)
	}
	return nil, errors.New("secrets: no usable backend in auto mode (none of keyring/pass/age/file probed successfully)")
}

// FromConfig is a convenience wrapper that maps a parsed config.Config
// into the secrets.Config shape. Callers that already have a *config.Config
// in hand can use this rather than re-typing field names.
func FromConfig(c config.Config, homeDir string) Config {
	return Config{
		Backend: c.Secrets.Backend,
		HomeDir: homeDir,
	}
}
