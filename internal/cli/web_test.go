package cli

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

// TestWeb_FactoryReceivesEffectiveAddress pins the flag-precedence
// contract: explicit --bind / --port flags override the [web] section
// loaded from --config.  The test factory captures the inputs without
// opening a real listener.
func TestWeb_FactoryReceivesEffectiveAddress(t *testing.T) {
	cfgPath := writeMinimalWebConfig(t, "10.0.0.1", 8080)

	var captured WebFactoryOptions
	withWebFactory(t, func(_ context.Context, opts WebFactoryOptions) (webRunner, error) {
		captured = opts
		return immediateExitWebRunner{}, nil
	})

	var stdout, stderr bytes.Buffer
	code := Execute([]string{"--config", cfgPath, "web",
		"--bind", "127.0.0.1",
		"--port", "0"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("web exit=%d; stderr=%q", code, stderr.String())
	}
	if captured.Addr != "127.0.0.1:0" {
		t.Errorf("Addr = %q; want 127.0.0.1:0 (CLI flags must override [web])", captured.Addr)
	}
	if captured.Remote {
		t.Errorf("Remote = true; want false when --remote not set")
	}
}

// TestWeb_DefaultsFromConfig pins that, with no flag overrides, the
// command picks up whatever [web] declares.
func TestWeb_DefaultsFromConfig(t *testing.T) {
	cfgPath := writeMinimalWebConfig(t, "10.0.0.1", 8080)

	var captured WebFactoryOptions
	withWebFactory(t, func(_ context.Context, opts WebFactoryOptions) (webRunner, error) {
		captured = opts
		return immediateExitWebRunner{}, nil
	})

	var stdout, stderr bytes.Buffer
	code := Execute([]string{"--config", cfgPath, "web"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("web exit=%d; stderr=%q", code, stderr.String())
	}
	if captured.Addr != "10.0.0.1:8080" {
		t.Errorf("Addr = %q; want 10.0.0.1:8080", captured.Addr)
	}
}

// TestWeb_RemoteBindsToAllInterfaces pins the --remote opt-in: the
// brief explicitly requires that external publishing only happens
// when the user opts in.
func TestWeb_RemoteBindsToAllInterfaces(t *testing.T) {
	cfgPath := writeMinimalWebConfig(t, "127.0.0.1", 7777)

	var captured WebFactoryOptions
	withWebFactory(t, func(_ context.Context, opts WebFactoryOptions) (webRunner, error) {
		captured = opts
		return immediateExitWebRunner{}, nil
	})

	var stdout, stderr bytes.Buffer
	code := Execute([]string{"--config", cfgPath, "web", "--remote"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("web exit=%d; stderr=%q", code, stderr.String())
	}
	if captured.Addr != "0.0.0.0:7777" {
		t.Errorf("Addr = %q; want 0.0.0.0:7777 when --remote", captured.Addr)
	}
	if !captured.Remote {
		t.Errorf("Remote = false; want true")
	}
}

// TestWeb_StubRemoved confirms the leaf stub for `web` is gone — the
// command must run real logic, not return notImplementedError.
// Without this assertion a regression that re-adds the stub would slip
// through because root_test.go's leaf-stub list is hand-curated.
func TestWeb_StubRemoved(t *testing.T) {
	for _, sub := range leafStubSubcommands {
		if sub == "web" {
			t.Fatalf("web is still in leafStubSubcommands; remove it from root_test.go")
		}
	}
}

// TestWeb_FactoryError_ExitsNonZero pins that a factory failure
// (e.g. invalid bind address) bubbles up as a non-zero exit code with
// the failure message on stderr.
func TestWeb_FactoryError_ExitsNonZero(t *testing.T) {
	cfgPath := writeMinimalWebConfig(t, "127.0.0.1", 7777)

	withWebFactory(t, func(_ context.Context, _ WebFactoryOptions) (webRunner, error) {
		return nil, fmt.Errorf("factory: bind would fail")
	})

	var stdout, stderr bytes.Buffer
	code := Execute([]string{"--config", cfgPath, "web"}, &stdout, &stderr)
	if code == 0 {
		t.Fatalf("expected non-zero exit when factory fails")
	}
	if !bytes.Contains(stderr.Bytes(), []byte("factory: bind would fail")) {
		t.Errorf("stderr = %q; want substring 'factory: bind would fail'", stderr.String())
	}
}

// immediateExitWebRunner is the test stub that lets factory-side
// assertions complete without actually binding a port.
type immediateExitWebRunner struct{}

func (immediateExitWebRunner) Run(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return nil
}

// writeMinimalWebConfig writes a config.toml with just enough fields
// to satisfy Validate, plus a [web] section for the test to assert
// against.  Returning the path keeps the call sites compact.
func writeMinimalWebConfig(t *testing.T, bind string, port int) string {
	t.Helper()
	cfgPath := configPathInsideHome(t)
	if err := os.MkdirAll(filepath.Dir(cfgPath), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	body := fmt.Sprintf(`[core]
db_path = "~/.marunage/tasks.db"
max_parallel = 1
log_level = "info"

[execution]
permission_mode = "bypass"
claude_command = "claude --dangerously-skip-permissions"
startup_timeout = 60
on_unknown_permission = "escalate"
human_wait_timeout = "30m"
reaper_stuck_threshold = "24h"

[discovery]
interval = "10m"

[web]
bind = %q
port = %d
`, bind, port)
	if err := os.WriteFile(cfgPath, []byte(body), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	return cfgPath
}
