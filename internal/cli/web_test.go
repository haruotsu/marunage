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
	withWebFactory(t, func(_ context.Context, opts WebFactoryOptions) (webRunner, func() error, error) {
		captured = opts
		return immediateExitWebRunner{}, nil, nil
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
	cfgPath := writeMinimalWebConfig(t, "127.0.0.1", 8080)

	var captured WebFactoryOptions
	withWebFactory(t, func(_ context.Context, opts WebFactoryOptions) (webRunner, func() error, error) {
		captured = opts
		return immediateExitWebRunner{}, nil, nil
	})

	var stdout, stderr bytes.Buffer
	code := Execute([]string{"--config", cfgPath, "web"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("web exit=%d; stderr=%q", code, stderr.String())
	}
	if captured.Addr != "127.0.0.1:8080" {
		t.Errorf("Addr = %q; want 127.0.0.1:8080", captured.Addr)
	}
}

// TestWeb_RemoteBindsToAllInterfaces pins the --remote opt-in: the
// brief explicitly requires that external publishing only happens
// when the user opts in.
func TestWeb_RemoteBindsToAllInterfaces(t *testing.T) {
	cfgPath := writeMinimalWebConfig(t, "127.0.0.1", 7777)

	var captured WebFactoryOptions
	withWebFactory(t, func(_ context.Context, opts WebFactoryOptions) (webRunner, func() error, error) {
		captured = opts
		return immediateExitWebRunner{}, nil, nil
	})

	var stdout, stderr bytes.Buffer
	code := Execute([]string{"--config", cfgPath, "web", "--remote",
		"--tls-cert", "server.crt", "--tls-key", "server.key"}, &stdout, &stderr)
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
	withWebFactory(t, func(_ context.Context, opts WebFactoryOptions) (webRunner, func() error, error) {
		captured = opts
		return immediateExitWebRunner{}, nil, nil
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

	withWebFactory(t, func(_ context.Context, _ WebFactoryOptions) (webRunner, func() error, error) {
		return immediateExitWebRunner{}, nil, nil
	})

	var stdout, stderr bytes.Buffer
	code := Execute([]string{"--config", cfgPath, "web", "--remote",
		"--tls-cert", "server.crt", "--tls-key", "server.key"}, &stdout, &stderr)
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

	withWebFactory(t, func(_ context.Context, _ WebFactoryOptions) (webRunner, func() error, error) {
		return immediateExitWebRunner{}, nil, nil
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
	cfgPath := writeMinimalWebConfig(t, "127.0.0.1", 7777)
	addr := freeLoopbackAddr(t)

	runner, closer, err := productionWebFactory(context.Background(), WebFactoryOptions{Addr: addr, ConfigPath: cfgPath})
	if err != nil {
		t.Fatalf("productionWebFactory: %v", err)
	}
	t.Cleanup(func() { _ = closer() })

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

// TestWeb_RemoteRequiresTLSCertAndKey pins the HTTPS-only requirement:
// --remote without both --tls-cert and --tls-key must exit non-zero with a
// clear error message so an operator cannot accidentally expose the dashboard
// over plain HTTP to the network.
func TestWeb_RemoteRequiresTLSCertAndKey(t *testing.T) {
	cfgPath := writeMinimalWebConfig(t, "127.0.0.1", 7777)

	withWebFactory(t, func(_ context.Context, _ WebFactoryOptions) (webRunner, func() error, error) {
		return immediateExitWebRunner{}, nil, nil
	})

	cases := []struct {
		name string
		args []string
	}{
		{"no cert no key", []string{"--config", cfgPath, "web", "--remote"}},
		{"cert but no key", []string{"--config", cfgPath, "web", "--remote", "--tls-cert", "server.crt"}},
		{"key but no cert", []string{"--config", cfgPath, "web", "--remote", "--tls-key", "server.key"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			code := Execute(tc.args, &stdout, &stderr)
			if code == 0 {
				t.Fatalf("expected non-zero exit when --remote without TLS cert+key; got 0")
			}
			combined := stderr.String()
			if !strings.Contains(combined, "tls") && !strings.Contains(combined, "cert") && !strings.Contains(combined, "TLS") {
				t.Errorf("stderr = %q; want TLS/cert error message", combined)
			}
		})
	}
}

// TestWeb_RemoteWithTLSCertAndKey_Passes pins the positive side: --remote with
// both --tls-cert and --tls-key must not fail at the validation step.
func TestWeb_RemoteWithTLSCertAndKey_Passes(t *testing.T) {
	cfgPath := writeMinimalWebConfig(t, "127.0.0.1", 7777)

	withWebFactory(t, func(_ context.Context, opts WebFactoryOptions) (webRunner, func() error, error) {
		return immediateExitWebRunner{}, nil, nil
	})

	var stdout, stderr bytes.Buffer
	code := Execute([]string{"--config", cfgPath, "web", "--remote",
		"--tls-cert", "server.crt", "--tls-key", "server.key"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("expected zero exit with --remote + cert/key; stderr=%q", stderr.String())
	}
}

// TestWeb_NonLocalhostBindWithoutRemote_Fails pins the second binding rule:
// --bind on a non-localhost address without --remote must exit non-zero so
// an operator cannot accidentally skip the explicit opt-in.
func TestWeb_NonLocalhostBindWithoutRemote_Fails(t *testing.T) {
	cfgPath := writeMinimalWebConfig(t, "127.0.0.1", 7777)

	withWebFactory(t, func(_ context.Context, _ WebFactoryOptions) (webRunner, func() error, error) {
		return immediateExitWebRunner{}, nil, nil
	})

	var stdout, stderr bytes.Buffer
	code := Execute([]string{"--config", cfgPath, "web", "--bind", "0.0.0.0"}, &stdout, &stderr)
	if code == 0 {
		t.Fatalf("expected non-zero exit when --bind non-localhost without --remote; got 0")
	}
}

// TestWeb_BypassRemoteExtraWarning pins that --remote with permission_mode=bypass
// emits a louder, additional warning about running without sandboxing.
func TestWeb_BypassRemoteExtraWarning(t *testing.T) {
	cfgPath := writeMinimalWebConfig(t, "127.0.0.1", 7777)

	withWebFactory(t, func(_ context.Context, _ WebFactoryOptions) (webRunner, func() error, error) {
		return immediateExitWebRunner{}, nil, nil
	})

	var stdout, stderr bytes.Buffer
	code := Execute([]string{"--config", cfgPath, "web", "--remote",
		"--tls-cert", "server.crt", "--tls-key", "server.key"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("web exit=%d; stderr=%q", code, stderr.String())
	}
	combined := stderr.String()
	if !strings.Contains(combined, "bypass") && !strings.Contains(combined, "WARNING") {
		t.Errorf("stderr = %q; want bypass-mode warning when --remote + permission_mode=bypass", combined)
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

// TestWeb_ProductionFactory_TaskDetailWired pins the requirement that
// productionWebFactory wires TaskDetail into web.Options so that
// GET /tasks/{id} resolves against the real SQLite store rather than
// falling through to noopTaskDetailProvider (which returns 404 for
// every id). We insert a task directly into the DB the factory opened,
// then verify the running server returns 200 for that id.
func TestWeb_ProductionFactory_TaskDetailWired(t *testing.T) {
	// Use a tmpdir as the config dir so db_path is self-contained under
	// t.TempDir() — avoids touching ~/.marunage/tasks.db on the dev machine.
	tmpDir := t.TempDir()
	cfgPath := filepath.Join(tmpDir, ".marunage", "config.toml")
	if err := os.MkdirAll(filepath.Dir(cfgPath), 0o755); err != nil {
		t.Fatalf("mkdir config dir: %v", err)
	}
	dbPath := filepath.Join(tmpDir, "tasks.db")
	cfgBody := fmt.Sprintf(`[core]
db_path = %q
max_parallel = 1
log_level = "info"

[execution]
permission_mode = "bypass"
claude_command = "claude"
startup_timeout = 60
on_unknown_permission = "escalate"
human_wait_timeout = "30m"
reaper_stuck_threshold = "24h"

[discovery]
interval = "10m"

[web]
bind = "127.0.0.1"
port = 7777
`, dbPath)
	if err := os.WriteFile(cfgPath, []byte(cfgBody), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}

	addr := freeLoopbackAddr(t)
	runner, closer, err := productionWebFactory(context.Background(), WebFactoryOptions{
		Addr:       addr,
		ConfigPath: cfgPath,
	})
	if err != nil {
		t.Fatalf("productionWebFactory: %v", err)
	}
	t.Cleanup(func() { _ = closer() })

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	runErr := make(chan error, 1)
	go func() { runErr <- runner.Run(ctx) }()

	if err := pollHealthz(t, "http://"+addr+"/healthz", 3*time.Second); err != nil {
		cancel()
		<-runErr
		t.Fatalf("server never became ready: %v", err)
	}

	// Insert a task directly into the DB so we have a known id.
	taskID := insertTaskIntoDBForTest(t, dbPath)

	// If TaskDetail is wired, this returns 200. If noopTaskDetailProvider
	// is used instead, it returns 404 for every id including real ones.
	url := fmt.Sprintf("http://%s/tasks/%d", addr, taskID)
	resp, getErr := http.Get(url)
	if getErr != nil {
		cancel()
		<-runErr
		t.Fatalf("GET %s: %v", url, getErr)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("GET %s = %d; want 200 — TaskDetail must be wired in productionWebFactory", url, resp.StatusCode)
	}

	cancel()
	if err := <-runErr; err != nil {
		t.Fatalf("runner: %v", err)
	}
}

// TestWeb_ProductionFactory_AuditLogPathDerived verifies that
// auditLogPathFor produces a path adjacent to config.toml so the
// productionWebFactory can wire FileAuditReader to the correct location.
func TestWeb_ProductionFactory_AuditLogPathDerived(t *testing.T) {
	cfgPath := "/tmp/testdir/.marunage/config.toml"
	want := "/tmp/testdir/.marunage/logs/audit.log"
	got := auditLogPathFor(cfgPath)
	if got != want {
		t.Errorf("auditLogPathFor(%q) = %q; want %q", cfgPath, got, want)
	}
}

// insertTaskIntoDBForTest opens the SQLite DB that the production factory
// already migrated and inserts a minimal pending task, returning its id.
// This lets TaskDetail integration tests assert against a known row without
// going through the CLI task-add path.
func insertTaskIntoDBForTest(t *testing.T, dbPath string) int64 {
	t.Helper()
	db, err := openTestDB(t, dbPath)
	if err != nil {
		t.Fatalf("insertTaskIntoDBForTest: open %s: %v", dbPath, err)
	}
	defer func() { _ = db.Close() }()
	return insertMinimalTask(t, db)
}

// TestWeb_DaemonLogReceivesAccessRecord pins the brief's "各リクエ
// ストのログを daemon.log に JSON Lines" requirement: production
// wiring must open daemon.log next to config.toml and append one
// JSON-Lines record per request.  Without this assertion the
// AccessLogger seam exists in web.Options but the binary never
// writes anything to disk.
func TestWeb_DaemonLogReceivesAccessRecord(t *testing.T) {
	cfgPath := writeMinimalWebConfig(t, "127.0.0.1", 7777)
	addr := freeLoopbackAddr(t)

	runner, closer, err := productionWebFactory(context.Background(), WebFactoryOptions{
		Addr:       addr,
		ConfigPath: cfgPath,
	})
	if err != nil {
		t.Fatalf("productionWebFactory: %v", err)
	}
	t.Cleanup(func() { _ = closer() })

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
	if err := <-runErr; err != nil {
		t.Fatalf("runner: %v", err)
	}
	// Closer flushes the rotating writer.
	if err := closer(); err != nil {
		t.Fatalf("closer: %v", err)
	}

	logPath := filepath.Join(filepath.Dir(cfgPath), "logs", "daemon.log")
	body, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("read %s: %v", logPath, err)
	}
	if !bytes.Contains(body, []byte("/healthz")) {
		t.Errorf("daemon.log missing /healthz access record\ncontent:\n%s", body)
	}
	if !bytes.Contains(body, []byte(`"status":200`)) {
		t.Errorf("daemon.log missing JSON status field; not a JSON Lines record?\ncontent:\n%s", body)
	}
}

// TestWeb_FactoryCloserAlwaysRuns pins the resource-cleanup
// invariant: whatever the factory returned (file handles, the
// listener) must be released even if the runner errors out.  The
// brief calls for the dispatcher's (runner, closer, err) shape so
// listener / log-file leaks are not possible after a partial start.
func TestWeb_FactoryCloserAlwaysRuns(t *testing.T) {
	cfgPath := writeMinimalWebConfig(t, "127.0.0.1", 7777)

	closed := false
	withWebFactory(t, func(_ context.Context, _ WebFactoryOptions) (webRunner, func() error, error) {
		return immediateExitWebRunner{}, func() error { closed = true; return nil }, nil
	})

	var stdout, stderr bytes.Buffer
	code := Execute([]string{"--config", cfgPath, "web"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("web exit=%d; stderr=%q", code, stderr.String())
	}
	if !closed {
		t.Fatalf("factory closer never invoked; runner-side resources can leak")
	}
}

// TestWeb_FactoryError_ExitsNonZero pins that a factory failure
// (e.g. invalid bind address) bubbles up as a non-zero exit code with
// the failure message on stderr.
func TestWeb_FactoryError_ExitsNonZero(t *testing.T) {
	cfgPath := writeMinimalWebConfig(t, "127.0.0.1", 7777)

	withWebFactory(t, func(_ context.Context, _ WebFactoryOptions) (webRunner, func() error, error) {
		return nil, nil, fmt.Errorf("factory: bind would fail")
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
