package cli

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
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
}

// TestWeb_RemoteFlagOverridesConfigTrue pins the bidirectional
// precedence: when [web].remote=true, an explicit --remote=false on
// the CLI must flip the binary back to loopback.  Without
// cmd.Flags().Changed("remote") the boolean OR would lock the
// process into 0.0.0.0 once the config opted in.
func TestWeb_RemoteFlagOverridesConfigTrue(t *testing.T) {
	cfgPath := writeMinimalWebConfig(t, "127.0.0.1", 7777)
	if err := os.WriteFile(cfgPath, []byte(`[core]
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
bind = "127.0.0.1"
port = 7777
remote = true
`), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}

	var captured WebFactoryOptions
	withWebFactory(t, func(_ context.Context, opts WebFactoryOptions) (webRunner, error) {
		captured = opts
		return immediateExitWebRunner{}, nil
	})

	var stdout, stderr bytes.Buffer
	code := Execute([]string{"--config", cfgPath, "web", "--remote=false"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("web exit=%d; stderr=%q", code, stderr.String())
	}
	if captured.Addr != "127.0.0.1:7777" {
		t.Errorf("Addr = %q; want 127.0.0.1:7777 (--remote=false must override config)", captured.Addr)
	}
	if strings.Contains(stderr.String(), "WARNING") {
		t.Errorf("stderr unexpectedly carries WARNING when --remote=false; got %q", stderr.String())
	}
}

// TestWeb_RemotePrintsAuthlessWarning pins the operator-awareness
// contract: --remote opts out of the loopback default but the binary
// currently ships no auth, so the command must print a loud warning
// to stderr before listening.  Without this guard an operator could
// expose the dashboard to the world unaware that auth lands in a
// later PR.
func TestWeb_RemotePrintsAuthlessWarning(t *testing.T) {
	cfgPath := writeMinimalWebConfig(t, "127.0.0.1", 7777)

	withWebFactory(t, func(_ context.Context, _ WebFactoryOptions) (webRunner, error) {
		return immediateExitWebRunner{}, nil
	})

	var stdout, stderr bytes.Buffer
	code := Execute([]string{"--config", cfgPath, "web", "--remote"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("web exit=%d; stderr=%q", code, stderr.String())
	}
	combined := stderr.String()
	if !strings.Contains(combined, "WARNING") {
		t.Errorf("stderr missing WARNING marker for --remote; got %q", combined)
	}
	if !strings.Contains(combined, "no authentication") {
		t.Errorf("stderr missing 'no authentication' notice; got %q", combined)
	}
}

// TestWeb_NonRemoteDoesNotWarn pins the negative side: the loopback
// default must not emit the scary banner, otherwise the warning loses
// its signal value.
func TestWeb_NonRemoteDoesNotWarn(t *testing.T) {
	cfgPath := writeMinimalWebConfig(t, "127.0.0.1", 7777)

	withWebFactory(t, func(_ context.Context, _ WebFactoryOptions) (webRunner, error) {
		return immediateExitWebRunner{}, nil
	})

	var stdout, stderr bytes.Buffer
	code := Execute([]string{"--config", cfgPath, "web"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("web exit=%d; stderr=%q", code, stderr.String())
	}
	if strings.Contains(stderr.String(), "WARNING") {
		t.Errorf("stderr unexpectedly carries WARNING for loopback bind; got %q", stderr.String())
	}
}

// TestWeb_ProductionFactory_RealListenAndShutdown exercises
// productionWebFactory end-to-end so a regression in the real wiring
// (net.Listen → web.NewServer → serverRunner.Run → graceful shutdown)
// surfaces here rather than in production.  The other tests inject a
// stub factory and would silently miss any breakage of the real path.
func TestWeb_ProductionFactory_RealListenAndShutdown(t *testing.T) {
	addr := freeLoopbackAddr(t)

	runner, err := productionWebFactory(context.Background(), WebFactoryOptions{Addr: addr})
	if err != nil {
		t.Fatalf("productionWebFactory: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	runErr := make(chan error, 1)
	go func() { runErr <- runner.Run(ctx) }()

	if err := pollHealthz(t, "http://"+addr+"/healthz", 3*time.Second); err != nil {
		cancel()
		<-runErr
		t.Fatalf("server never became ready: %v", err)
	}

	cancel()
	select {
	case err := <-runErr:
		if err != nil {
			t.Fatalf("Run returned %v; want nil on graceful shutdown", err)
		}
	case <-time.After(6 * time.Second):
		t.Fatal("Run did not return within 6s of cancel; expected graceful shutdown within 5s budget")
	}
}

// freeLoopbackAddr binds + immediately closes a kernel-assigned port,
// returning the chosen 127.0.0.1:<port> string so the caller can hand
// it to the production factory.  There is a small TOCTOU window
// before the production listener re-binds; in practice the kernel
// reuses the port for the next bind on the same loopback.
func freeLoopbackAddr(t *testing.T) string {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("net.Listen: %v", err)
	}
	addr := listener.Addr().String()
	if err := listener.Close(); err != nil {
		t.Fatalf("close ephemeral listener: %v", err)
	}
	return addr
}

func pollHealthz(t *testing.T, url string, timeout time.Duration) error {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		resp, err := http.Get(url)
		if err == nil {
			body, _ := io.ReadAll(resp.Body)
			_ = resp.Body.Close()
			if resp.StatusCode == http.StatusOK && strings.TrimSpace(string(body)) == "ok" {
				return nil
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	return fmt.Errorf("timeout waiting for %s", url)
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
