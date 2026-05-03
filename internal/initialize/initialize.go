// Package initialize creates the ~/.marunage/ on-disk layout that
// `marunage init` (PR-33) is responsible for. Splitting the side-effecting
// work out of the CLI command keeps cobra-free unit tests on top of the
// real filesystem and lets future callers (Web UI, tests for downstream
// commands) provision a fresh marunage home without re-implementing the
// invariants here.
package initialize

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/haruotsu/marunage/internal/config"
)

// Options is the inject-the-world struct Run takes. Home is the only
// required field in production; tests pass it as t.TempDir() to avoid
// touching the developer's real ~/.marunage. Mode lets the CLI relay the
// user's permission-mode choice without coupling this package to cobra;
// an empty Mode resolves to the documented default ("bypass").
//
// Auditor is optional and omitted by callers that genuinely have no audit
// log yet (single-test scenarios). Production wires in *logging.AuditLog so
// the "No silent execution" invariant from docs/requirement.md applies to
// init too.
type Options struct {
	Home    string
	Mode    string
	Auditor config.Auditor
}

// ErrInvalidMode is returned when Options.Mode is set to a value outside
// the documented permission-mode whitelist. Exposing it as a sentinel lets
// the CLI layer match with errors.Is and surface a non-zero exit code
// without re-validating the input itself.
var ErrInvalidMode = errors.New("invalid permission mode")

// allowedModes mirrors docs/requirement.md 187-195 and the validator in
// internal/config. Centralising the list here lets Run reject bad input
// before any filesystem mutation, which is what TestRun_InvalidMode pins.
var allowedModes = []string{"bypass", "default", "acceptEdits", "plan", "custom"}

// defaultMode is the recommended choice from docs/requirement.md: the
// sandboxed-personal-machine path that PR-33 is the first-run UX for.
const defaultMode = "bypass"

// Result tells the caller what happened so the CLI can render an
// idempotency-aware human message ("created" vs "already initialized")
// without the package re-deciding by re-stat-ing the same files.
type Result struct {
	ConfigPath    string
	ConfigCreated bool
	DirsCreated   []string
}

// ResolveMode applies the same defaulting + whitelist check Run does,
// returned as a separate step so callers (the CLI) can validate user
// input *before* opening side-effecting writers like the audit log.
// Empty input resolves to the documented default ("bypass"); anything
// outside the whitelist returns ErrInvalidMode.
func ResolveMode(mode string) (string, error) {
	if mode == "" {
		mode = defaultMode
	}
	if !contains(allowedModes, mode) {
		return "", fmt.Errorf("%w: %q (allowed: %v)", ErrInvalidMode, mode, allowedModes)
	}
	return mode, nil
}

// Run is the entry point: ensure the marunage home layout exists, write a
// default config.toml the first time, and report what changed.
func Run(opts Options) (Result, error) {
	if opts.Home == "" {
		return Result{}, errors.New("Options.Home: must be a non-empty path")
	}

	mode, err := ResolveMode(opts.Mode)
	if err != nil {
		return Result{}, err
	}

	root := filepath.Join(opts.Home, ".marunage")
	cfgPath := filepath.Join(root, "config.toml")

	dirs := []string{root, filepath.Join(root, "logs"), filepath.Join(root, "sources")}
	created, err := ensureDirs(dirs)
	if err != nil {
		return Result{}, err
	}

	cfgCreated, err := ensureConfig(cfgPath, mode)
	if err != nil {
		return Result{}, err
	}

	auditor := opts.Auditor
	if auditor == nil {
		auditor = config.NopAuditor{}
	}
	if cfgCreated {
		auditor.Record(config.AuditEvent{Action: "init.create", Path: cfgPath})
	} else {
		auditor.Record(config.AuditEvent{Action: "init.skip", Path: cfgPath})
	}

	return Result{
		ConfigPath:    cfgPath,
		ConfigCreated: cfgCreated,
		DirsCreated:   created,
	}, nil
}

func contains(set []string, v string) bool {
	for _, s := range set {
		if s == v {
			return true
		}
	}
	return false
}

// ensureDirs MkdirAlls each path with 0700 and returns the subset that did
// not previously exist. The "did not exist" list lets the CLI surface
// idempotency in its message ("created logs/, sources/") without a second
// stat round-trip.
func ensureDirs(paths []string) ([]string, error) {
	var created []string
	for _, p := range paths {
		_, err := os.Stat(p)
		switch {
		case err == nil:
			// Already there; nothing to record.
		case errors.Is(err, fs.ErrNotExist):
			created = append(created, p)
		default:
			return nil, fmt.Errorf("stat %s: %w", p, err)
		}
		if err := os.MkdirAll(p, 0o700); err != nil {
			return nil, fmt.Errorf("mkdir %s: %w", p, err)
		}
	}
	return created, nil
}

// ensureConfig writes the default config.toml only when the file does not
// already exist. We never overwrite a user-edited config; the CLI surface
// instead nudges the user toward `marunage config set` for changes.
//
// mode is plumbed through so first-run users can pick a permission mode
// up-front without a subsequent `marunage config set`.
func ensureConfig(path, mode string) (bool, error) {
	if _, err := os.Stat(path); err == nil {
		return false, nil
	} else if !errors.Is(err, fs.ErrNotExist) {
		return false, fmt.Errorf("stat %s: %w", path, err)
	}

	cfg := config.Default()
	cfg.Execution.PermissionMode = mode
	if cmd := config.ClaudeCommandFor(mode); cmd != "" {
		cfg.Execution.ClaudeCommand = cmd
	} else {
		// "custom" returns "" from ClaudeCommandFor and config.Validate
		// forbids custom + empty. We need a non-empty placeholder, but
		// must NOT silently keep Default()'s bypass command — a user who
		// picked "custom" specifically to avoid bypass would otherwise end
		// up with --dangerously-skip-permissions still wired in. Plain
		// `claude` (the default-mode command) is the conservative choice:
		// every tool call prompts until the user replaces it.
		cfg.Execution.ClaudeCommand = config.ClaudeCommandFor("default")
	}

	if err := config.Save(path, cfg, nil); err != nil {
		return false, fmt.Errorf("write default config: %w", err)
	}
	return true, nil
}
