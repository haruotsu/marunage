package logging_test

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/haruotsu/marunage/internal/config"
	"github.com/haruotsu/marunage/internal/logging"
)

// auditLine is the on-disk shape of one audit.log entry. Pinning it as a test
// type keeps the file format part of the spec rather than an implementation
// detail.
type auditLine struct {
	Time    string `json:"time"`
	Action  string `json:"action"`
	Path    string `json:"path"`
	Key     string `json:"key"`
	Value   string `json:"value"`
	Backend string `json:"backend"`
	Name    string `json:"name"`
}

func readAuditLines(t *testing.T, path string) []auditLine {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatalf("open %s: %v", path, err)
	}
	defer func() { _ = f.Close() }()

	var out []auditLine
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		var l auditLine
		if err := json.Unmarshal(scanner.Bytes(), &l); err != nil {
			t.Fatalf("unmarshal line %q: %v", scanner.Text(), err)
		}
		out = append(out, l)
	}
	if err := scanner.Err(); err != nil {
		t.Fatalf("scan: %v", err)
	}
	return out
}

// TestAuditLogAppendsJSONLines pins the smallest possible contract: one
// Record call writes exactly one JSON object on its own line, and the field
// names match the documented audit format (time/action/path/key/value).
func TestAuditLogAppendsJSONLines(t *testing.T) {
	path := filepath.Join(t.TempDir(), "logs", "audit.log")

	a, err := logging.NewAuditLog(path)
	if err != nil {
		t.Fatalf("NewAuditLog: %v", err)
	}
	t.Cleanup(func() { _ = a.Close() })

	a.Record(config.AuditEvent{
		Action: "config.set",
		Path:   "/tmp/config.toml",
		Key:    "core.max_parallel",
		Value:  "5",
	})
	if err := a.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	lines := readAuditLines(t, path)
	if len(lines) != 1 {
		t.Fatalf("lines = %d; want 1", len(lines))
	}
	got := lines[0]
	if got.Action != "config.set" {
		t.Errorf("Action = %q; want %q", got.Action, "config.set")
	}
	if got.Path != "/tmp/config.toml" {
		t.Errorf("Path = %q; want %q", got.Path, "/tmp/config.toml")
	}
	if got.Key != "core.max_parallel" {
		t.Errorf("Key = %q; want %q", got.Key, "core.max_parallel")
	}
	if got.Value != "5" {
		t.Errorf("Value = %q; want %q", got.Value, "5")
	}
	if got.Time == "" {
		t.Errorf("Time is empty; expected RFC3339 timestamp")
	}
}

// TestAuditLogCreatesParentDirAnd0600 verifies that the writer is forgiving
// about a fresh ~/.marunage/logs/ tree (PR-04 ships before any explicit
// init(8) creates that directory) and that the file is private to the user
// since audit lines may carry sensitive config values.
func TestAuditLogCreatesParentDirAnd0600(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "nested", "logs", "audit.log")

	a, err := logging.NewAuditLog(path)
	if err != nil {
		t.Fatalf("NewAuditLog: %v", err)
	}
	t.Cleanup(func() { _ = a.Close() })

	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("perm = %o; want 0600", perm)
	}
}

// TestAuditLogAppendsAcrossReopens guarantees the "append-only" invariant
// across process restarts: opening an existing audit.log must not truncate
// prior history. Tests with two NewAuditLog calls because that is the actual
// production lifecycle (each marunage invocation reopens the file).
func TestAuditLogAppendsAcrossReopens(t *testing.T) {
	path := filepath.Join(t.TempDir(), "audit.log")

	first, err := logging.NewAuditLog(path)
	if err != nil {
		t.Fatalf("NewAuditLog #1: %v", err)
	}
	first.Record(config.AuditEvent{Action: "config.save", Path: "p1"})
	if err := first.Close(); err != nil {
		t.Fatalf("Close #1: %v", err)
	}

	second, err := logging.NewAuditLog(path)
	if err != nil {
		t.Fatalf("NewAuditLog #2: %v", err)
	}
	second.Record(config.AuditEvent{Action: "config.set", Path: "p2", Key: "k", Value: "v"})
	if err := second.Close(); err != nil {
		t.Fatalf("Close #2: %v", err)
	}

	lines := readAuditLines(t, path)
	if len(lines) != 2 {
		t.Fatalf("lines = %d; want 2 (history preserved across reopen)", len(lines))
	}
	if lines[0].Action != "config.save" || lines[1].Action != "config.set" {
		t.Errorf("ordering wrong: %+v", lines)
	}
}

// TestAuditLogConcurrentRecord is the safety net for the daemon use case:
// dispatcher and config CLI may both write events at the same time. The
// per-line atomicity of audit.log is part of the audit invariant — partial
// or interleaved JSON would defeat the "No silent execution" guarantee.
func TestAuditLogConcurrentRecord(t *testing.T) {
	path := filepath.Join(t.TempDir(), "audit.log")

	a, err := logging.NewAuditLog(path)
	if err != nil {
		t.Fatalf("NewAuditLog: %v", err)
	}
	t.Cleanup(func() { _ = a.Close() })

	const writers = 8
	const each = 25
	var wg sync.WaitGroup
	for w := 0; w < writers; w++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			for i := 0; i < each; i++ {
				a.Record(config.AuditEvent{Action: "config.set", Key: "k", Value: "v"})
			}
		}(w)
	}
	wg.Wait()
	if err := a.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	lines := readAuditLines(t, path)
	if got, want := len(lines), writers*each; got != want {
		t.Fatalf("lines = %d; want %d (no lost or merged writes under concurrency)", got, want)
	}
	for i, l := range lines {
		if l.Action != "config.set" {
			t.Fatalf("line %d Action = %q; want %q (interleaved write?)", i, l.Action, "config.set")
		}
	}
}

// TestAuditLogSatisfiesAuditor pins the wiring contract: the file-backed
// AuditLog must be assignable to config.Auditor so config.Save can take it
// directly. A compile-time interface assertion would also catch this, but a
// dedicated test makes the intent visible alongside the runtime behavior.
func TestAuditLogSatisfiesAuditor(t *testing.T) {
	path := filepath.Join(t.TempDir(), "audit.log")
	a, err := logging.NewAuditLog(path)
	if err != nil {
		t.Fatalf("NewAuditLog: %v", err)
	}
	t.Cleanup(func() { _ = a.Close() })

	var _ config.Auditor = a
}

// TestAuditLogSerializesSecretsFields pins the on-disk shape for
// secrets.set / secrets.delete events so the file format and the
// internal/secrets decorator agree on what reaches audit.log. The most
// important guarantee is that no Value field is written - the decorator
// never sets it, but a regression that wrote it as "" would still be a
// downgrade because operators might start expecting it.
func TestAuditLogSerializesSecretsFields(t *testing.T) {
	path := filepath.Join(t.TempDir(), "audit.log")

	a, err := logging.NewAuditLog(path)
	if err != nil {
		t.Fatalf("NewAuditLog: %v", err)
	}
	a.Record(config.AuditEvent{
		Action:  "secrets.set",
		Backend: "file",
		Name:    "gmail",
	})
	if err := a.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	raw := string(body)
	if !strings.Contains(raw, `"backend":"file"`) {
		t.Errorf("audit line missing backend field: %s", raw)
	}
	if !strings.Contains(raw, `"name":"gmail"`) {
		t.Errorf("audit line missing name field: %s", raw)
	}
	if strings.Contains(raw, `"value":`) {
		t.Errorf("audit line carries a value field for secrets.set: %s", raw)
	}

	lines := readAuditLines(t, path)
	if len(lines) != 1 {
		t.Fatalf("lines = %d; want 1", len(lines))
	}
	got := lines[0]
	if got.Action != "secrets.set" || got.Backend != "file" || got.Name != "gmail" {
		t.Errorf("decoded line = %+v; want secrets.set/file/gmail", got)
	}
	if got.Value != "" {
		t.Errorf("Value = %q; secrets events must never carry a value", got.Value)
	}
}

// TestAuditLogRecordAfterClose: closing the writer must not panic later
// callers. Production callers wrap NewAuditLog in defer Close; if a stray
// goroutine fires Record after the deferred Close runs we want a quiet
// no-op, not a process crash that takes down the daemon.
func TestAuditLogRecordAfterClose(t *testing.T) {
	path := filepath.Join(t.TempDir(), "audit.log")

	a, err := logging.NewAuditLog(path)
	if err != nil {
		t.Fatalf("NewAuditLog: %v", err)
	}
	if err := a.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("Record after Close panicked: %v", r)
		}
	}()
	a.Record(config.AuditEvent{Action: "config.set"})

	// Sanity-check: nothing slipped through to disk.
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if strings.TrimSpace(string(body)) != "" {
		t.Errorf("file mutated after Close: %q", string(body))
	}
}
