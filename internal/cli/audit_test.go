package cli

import (
	"bufio"
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type cliAuditLine struct {
	Time   string `json:"time"`
	Action string `json:"action"`
	Path   string `json:"path"`
	Key    string `json:"key"`
	Value  string `json:"value"`
}

func readCLIAuditLines(t *testing.T, path string) []cliAuditLine {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatalf("open %s: %v", path, err)
	}
	defer f.Close()

	var out []cliAuditLine
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		var l cliAuditLine
		if err := json.Unmarshal(sc.Bytes(), &l); err != nil {
			t.Fatalf("unmarshal line %q: %v", sc.Text(), err)
		}
		out = append(out, l)
	}
	if err := sc.Err(); err != nil {
		t.Fatalf("scan: %v", err)
	}
	return out
}

// auditLogFor returns the path the CLI is expected to write audit lines to
// when --config points at <root>/config.toml. The contract is:
// "<dirname(--config)>/logs/audit.log".
func auditLogFor(configPath string) string {
	return filepath.Join(filepath.Dir(configPath), "logs", "audit.log")
}

// TestConfigSet_RecordsAuditLine pins the documented invariant: every
// successful `config set` must leave a trace in audit.log so the operator
// can audit who changed what when. Two lines are expected — one for the
// key-level config.set event with key+value, one for the file-level
// config.save event from internal/config.Save.
func TestConfigSet_RecordsAuditLine(t *testing.T) {
	path := configPathFlag(t)

	var stdout, stderr bytes.Buffer
	code := Execute([]string{"--config", path, "config", "set", "core.max_parallel", "5"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("config set exit=%d; stderr=%q", code, stderr.String())
	}

	auditPath := auditLogFor(path)
	if _, err := os.Stat(auditPath); err != nil {
		t.Fatalf("audit.log not created at %s: %v", auditPath, err)
	}

	lines := readCLIAuditLines(t, auditPath)
	if len(lines) != 2 {
		t.Fatalf("audit lines = %d; want 2 (config.set + config.save)\nlines=%+v", len(lines), lines)
	}

	var sawSet, sawSave bool
	for _, l := range lines {
		switch l.Action {
		case "config.set":
			sawSet = true
			if l.Key != "core.max_parallel" {
				t.Errorf("config.set line Key = %q; want %q", l.Key, "core.max_parallel")
			}
			if l.Value != "5" {
				t.Errorf("config.set line Value = %q; want %q", l.Value, "5")
			}
			if l.Path != path {
				t.Errorf("config.set line Path = %q; want %q", l.Path, path)
			}
		case "config.save":
			sawSave = true
			if l.Path != path {
				t.Errorf("config.save line Path = %q; want %q", l.Path, path)
			}
		default:
			t.Errorf("unexpected audit Action %q in %+v", l.Action, l)
		}
		if l.Time == "" {
			t.Errorf("audit line missing Time: %+v", l)
		}
	}
	if !sawSet || !sawSave {
		t.Errorf("missing expected actions: sawSet=%v sawSave=%v", sawSet, sawSave)
	}
}

// TestConfigSet_AuditAppendsAcrossInvocations: a second `config set` from a
// fresh process must extend audit.log rather than overwrite it. This is the
// "append-only" invariant exercised end-to-end through the CLI rather than
// at the writer level.
func TestConfigSet_AuditAppendsAcrossInvocations(t *testing.T) {
	path := configPathFlag(t)

	var stdout, stderr bytes.Buffer
	if code := Execute([]string{"--config", path, "config", "set", "core.max_parallel", "2"}, &stdout, &stderr); code != 0 {
		t.Fatalf("first set exit=%d; stderr=%q", code, stderr.String())
	}
	stdout.Reset()
	stderr.Reset()
	if code := Execute([]string{"--config", path, "config", "set", "core.max_parallel", "9"}, &stdout, &stderr); code != 0 {
		t.Fatalf("second set exit=%d; stderr=%q", code, stderr.String())
	}

	lines := readCLIAuditLines(t, auditLogFor(path))
	if len(lines) != 4 {
		t.Fatalf("audit lines = %d; want 4 (2 invocations × {set, save})", len(lines))
	}
}

// TestConfigGet_DoesNotWriteAudit: read-only operations must not pollute
// audit.log. The whole point of the audit trail is that every line maps to
// a real mutation.
func TestConfigGet_DoesNotWriteAudit(t *testing.T) {
	path := configPathFlag(t)

	var stdout, stderr bytes.Buffer
	if code := Execute([]string{"--config", path, "config", "get", "execution.permission_mode"}, &stdout, &stderr); code != 0 {
		t.Fatalf("config get exit=%d; stderr=%q", code, stderr.String())
	}

	if _, err := os.Stat(auditLogFor(path)); !os.IsNotExist(err) {
		t.Errorf("audit.log should not exist after read-only `config get`; stat err=%v", err)
	}
}

// TestConfigSet_AuditLogIs0600: defense-in-depth on file mode. Even though
// the AuditLog writer creates the file with 0600 itself, pin it from the
// CLI side so a future refactor that touches the wiring cannot silently
// loosen permissions.
func TestConfigSet_AuditLogIs0600(t *testing.T) {
	path := configPathFlag(t)

	var stdout, stderr bytes.Buffer
	if code := Execute([]string{"--config", path, "config", "set", "core.max_parallel", "5"}, &stdout, &stderr); code != 0 {
		t.Fatalf("config set exit=%d; stderr=%q", code, stderr.String())
	}

	info, err := os.Stat(auditLogFor(path))
	if err != nil {
		t.Fatalf("Stat audit.log: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("audit.log perm = %o; want 0600", perm)
	}
}

// TestConfigSet_InvalidValueLeavesAuditEmpty: a rejected set must NOT
// produce a config.set line, otherwise the audit trail would record changes
// that never happened. Validation runs before any audit emission.
func TestConfigSet_InvalidValueLeavesAuditEmpty(t *testing.T) {
	path := configPathFlag(t)

	var stdout, stderr bytes.Buffer
	if code := Execute([]string{"--config", path, "config", "set", "execution.permission_mode", "yolo"}, &stdout, &stderr); code == 0 {
		t.Fatalf("invalid set exit=0; want non-zero")
	}

	auditPath := auditLogFor(path)
	body, err := os.ReadFile(auditPath)
	if err != nil {
		// Non-existent is also fine — the writer was never opened.
		if os.IsNotExist(err) {
			return
		}
		t.Fatalf("ReadFile audit.log: %v", err)
	}
	if strings.TrimSpace(string(body)) != "" {
		t.Errorf("audit.log should be empty after rejected set; got:\n%s", body)
	}
}
