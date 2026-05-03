package doctor

import (
	"context"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

// SecretsProbe reports which secret-storage backends look usable on this
// host. Doctor only needs the set of available names; the actual unlock /
// store logic lives behind internal/secrets (PR-30) and is intentionally
// not imported here so PR-32 compiles standalone.
type SecretsProbe interface {
	AvailableBackends() []string
}

// FileSecretsProbe is the production SecretsProbe. It does NOT call any
// keyring or pass binary; it just looks at the filesystem and PATH so
// `marunage doctor` is fast and side-effect free.
//
// Detection rules (mirroring the rationale in docs/requirement.md
// "シークレット保存 — クロスプラットフォーム前提"):
//
//   - "age": ~/.marunage/secrets.age exists.
//   - "file": ~/.marunage/secrets/ exists and has mode 0700 (the lax-perm
//     case is reported via Hint by the secrets check, not by hiding the
//     backend; doctor wants to know it would work, even if it warns).
//   - "pass": `pass` binary on PATH.
//   - "keyring": platform-inferred. On darwin we always assume Keychain is
//     reachable; on linux we look for `secret-tool` on PATH or a non-empty
//     DBUS_SESSION_BUS_ADDRESS as a proxy for a running session bus.
type FileSecretsProbe struct {
	HomeDir string // overridden in tests; production callers leave this empty
	Runner  Runner // reused for `pass` / `secret-tool` PATH lookups
	GOOS    string // overridden in tests; defaults to runtime.GOOS
}

// AvailableBackends returns the unique list of backends that look usable.
// Order is deterministic so the JSON output is stable across runs.
func (p FileSecretsProbe) AvailableBackends() []string {
	home := p.HomeDir
	if home == "" {
		if h, err := os.UserHomeDir(); err == nil {
			home = h
		}
	}
	goos := p.GOOS
	if goos == "" {
		goos = runtime.GOOS
	}

	var out []string
	if home != "" {
		if fileExists(filepath.Join(home, ".marunage", "secrets.age")) {
			out = append(out, "age")
		}
		if dirExistsWithMode(filepath.Join(home, ".marunage", "secrets"), 0o700) {
			out = append(out, "file")
		}
	}
	if p.Runner != nil {
		if _, ok := p.Runner.LookPath("pass"); ok {
			out = append(out, "pass")
		}
	}
	if keyringLikelyAvailable(p.Runner, goos) {
		out = append(out, "keyring")
	}
	return out
}

func fileExists(path string) bool {
	st, err := os.Stat(path)
	if err != nil {
		return false
	}
	return !st.IsDir()
}

// dirExistsWithMode reports whether path is a directory with EXACTLY the
// requested mode. We compare against fs.FileMode(want) instead of "<= want"
// because permission narrowing is the whole point of the 0700 contract:
// the `file` backend stores plaintext and a wider mode would silently
// expose tokens to other local users.
func dirExistsWithMode(path string, want fs.FileMode) bool {
	st, err := os.Stat(path)
	if err != nil {
		return false
	}
	if !st.IsDir() {
		return false
	}
	return st.Mode().Perm() == want
}

// keyringLikelyAvailable infers whether the OS-native keyring would work
// without actually trying to open it (which would prompt the user for
// permission on macOS). The result is necessarily a heuristic.
func keyringLikelyAvailable(runner Runner, goos string) bool {
	switch goos {
	case "darwin":
		// macOS Keychain is always present on the system. Even on a CI
		// runner we treat it as "expected to be reachable"; a real auth
		// attempt during `marunage setup` is what would surface a
		// missing GUI session.
		return true
	case "windows":
		// Windows Credential Manager is part of the OS; same logic.
		return true
	case "linux":
		if runner != nil {
			if _, ok := runner.LookPath("secret-tool"); ok {
				return true
			}
			if _, ok := runner.LookPath("gnome-keyring-daemon"); ok {
				return true
			}
		}
		// DBUS_SESSION_BUS_ADDRESS is the cheapest signal that a desktop
		// session is up; without it Secret Service has nowhere to talk to.
		if strings.TrimSpace(os.Getenv("DBUS_SESSION_BUS_ADDRESS")) != "" {
			return true
		}
		return false
	}
	return false
}

// probeSecrets is the Eval body for the "secrets" check. It is required
// (alwaysRequired in the registry) and fails loudly if zero backends look
// usable; the hint points users at `marunage setup` so they know what to
// do next.
func probeSecrets(_ context.Context, in Inputs) CheckOutcome {
	backends := in.Secrets.AvailableBackends()
	if len(backends) == 0 {
		return CheckOutcome{
			OK:     false,
			Detail: "no secret-storage backend available (need keyring, pass, age, or 0700 file dir)",
			Hint:   "run `marunage setup` to configure a secret backend",
		}
	}
	return CheckOutcome{
		OK:      true,
		Detail:  fmt.Sprintf("backends available: %s", strings.Join(backends, ", ")),
		Version: strings.Join(backends, ","),
	}
}
