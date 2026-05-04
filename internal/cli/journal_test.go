package cli

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestJournalCmdHelp verifies the command tree is wired correctly.
func TestJournalCmdHelp(t *testing.T) {
	t.Parallel()
	var out bytes.Buffer
	code := Execute([]string{"journal", "--help"}, &out, &out)
	if code != 0 {
		t.Fatalf("journal --help exit %d: %s", code, out.String())
	}
	for _, want := range []string{"start", "export"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("journal --help missing subcommand %q, got:\n%s", want, out.String())
		}
	}
}

func TestJournalStartHelpFlag(t *testing.T) {
	t.Parallel()
	var out bytes.Buffer
	code := Execute([]string{"journal", "start", "--help"}, &out, &out)
	if code != 0 {
		t.Fatalf("journal start --help exit %d: %s", code, out.String())
	}
	if !strings.Contains(out.String(), "--once") {
		t.Errorf("journal start --help missing --once flag, got:\n%s", out.String())
	}
}

func TestJournalExportNoJournal(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	// Point DB path to temp dir so journal dir is resolved relative to it.
	cfgPath := writeMinimalConfig(t, dir)

	var out bytes.Buffer
	code := Execute([]string{"--config", cfgPath, "journal", "export", "--date", "2099-01-01"}, &out, &out)
	if code != 0 {
		t.Fatalf("journal export exit %d: %s", code, out.String())
	}
	if !strings.Contains(out.String(), "no journal for 2099-01-01") {
		t.Errorf("expected 'no journal' message, got: %s", out.String())
	}
}

func TestJournalExportReadsFile(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	cfgPath := writeMinimalConfig(t, dir)

	// Pre-create a journal file.
	journalDir := filepath.Join(dir, "journal")
	if err := os.MkdirAll(journalDir, 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	content := "## 2026-05-04 14:30\n\n### Git Activity\n- feat: test\n\n"
	if err := os.WriteFile(filepath.Join(journalDir, "2026-05-04.md"), []byte(content), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	var out bytes.Buffer
	code := Execute([]string{"--config", cfgPath, "journal", "export", "--date", "2026-05-04"}, &out, &out)
	if code != 0 {
		t.Fatalf("journal export exit %d: %s", code, out.String())
	}
	if !strings.Contains(out.String(), "feat: test") {
		t.Errorf("expected journal content, got: %s", out.String())
	}
}

func TestJournalStartOnceDisabledConfig(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	// Write config with journal.enabled = false.
	cfgContent := "[core]\ndb_path = \"" + filepath.Join(dir, "tasks.db") + "\"\nmax_parallel = 1\nlog_level = \"info\"\n[secrets]\nbackend = \"auto\"\n[discovery]\ninterval = \"10m\"\n[execution]\npermission_mode = \"bypass\"\nclaude_command = \"claude --dangerously-skip-permissions\"\nstartup_timeout = 60\non_unknown_permission = \"escalate\"\nhuman_wait_timeout = \"30m\"\nreaper_stuck_threshold = \"24h\"\n[journal]\nenabled = false\ninterval = \"30m\"\n[web]\nbind = \"127.0.0.1\"\nport = 7777\n"
	cfgPath := filepath.Join(dir, "config.toml")
	if err := os.WriteFile(cfgPath, []byte(cfgContent), 0o600); err != nil {
		t.Fatalf("WriteFile config: %v", err)
	}

	var out bytes.Buffer
	ctx := context.Background()
	code := executeForTest(ctx, []string{"--config", cfgPath, "journal", "start", "--once"}, &out, &out)
	if code != 0 {
		t.Fatalf("journal start --once exit %d: %s", code, out.String())
	}
	if !strings.Contains(out.String(), "journal.enabled = false") {
		t.Errorf("expected disabled message, got: %s", out.String())
	}
}

func TestJournalExportInvalidDate(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	cfgPath := writeMinimalConfig(t, dir)

	var out bytes.Buffer
	code := Execute([]string{"--config", cfgPath, "journal", "export", "--date", "../../etc/passwd"}, &out, &out)
	if code == 0 {
		t.Fatalf("journal export with path traversal should fail, got exit 0: %s", out.String())
	}
}

func TestJournalExportInvalidDateFormat(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	cfgPath := writeMinimalConfig(t, dir)

	var out bytes.Buffer
	code := Execute([]string{"--config", cfgPath, "journal", "export", "--date", "not-a-date"}, &out, &out)
	if code == 0 {
		t.Fatalf("journal export with invalid date should fail, got exit 0: %s", out.String())
	}
}

// writeMinimalConfig writes a minimal valid config.toml pointing db_path to
// dir/tasks.db and returns the config file path. Extracted so multiple tests
// share the same setup without repeating the TOML boilerplate.
func writeMinimalConfig(t *testing.T, dir string) string {
	t.Helper()
	cfgContent := "[core]\ndb_path = \"" + filepath.Join(dir, "tasks.db") + "\"\nmax_parallel = 1\nlog_level = \"info\"\n[secrets]\nbackend = \"auto\"\n[discovery]\ninterval = \"10m\"\n[execution]\npermission_mode = \"bypass\"\nclaude_command = \"claude --dangerously-skip-permissions\"\nstartup_timeout = 60\non_unknown_permission = \"escalate\"\nhuman_wait_timeout = \"30m\"\nreaper_stuck_threshold = \"24h\"\n[journal]\nenabled = true\ninterval = \"30m\"\n[web]\nbind = \"127.0.0.1\"\nport = 7777\n"
	cfgPath := filepath.Join(dir, "config.toml")
	if err := os.WriteFile(cfgPath, []byte(cfgContent), 0o600); err != nil {
		t.Fatalf("WriteFile config: %v", err)
	}
	return cfgPath
}
