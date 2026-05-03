package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// withHomeDir points HOME at a temp dir so `marunage setup --skills`
// installs into the throwaway location rather than the developer's
// real ~/.claude/skills/. Mirrors the env-shim pattern used elsewhere.
func withHomeDir(t *testing.T, dir string) {
	t.Helper()
	prev, hadPrev := os.LookupEnv("HOME")
	t.Setenv("HOME", dir)
	t.Cleanup(func() {
		if hadPrev {
			_ = os.Setenv("HOME", prev)
		} else {
			_ = os.Unsetenv("HOME")
		}
	})
}

// TestSetup_NoLongerLeafStub pins the migration: invoking `marunage
// setup --skills` no longer routes through the not-yet-implemented
// stub. Mirrors TestDoctor_NoLongerLeafStub.
func TestSetup_NoLongerLeafStub(t *testing.T) {
	home := t.TempDir()
	withHomeDir(t, home)

	var stdout, stderr bytes.Buffer
	_ = Execute([]string{"setup", "--skills"}, &stdout, &stderr)

	combined := stdout.String() + stderr.String()
	if strings.Contains(combined, "not yet implemented") {
		t.Errorf("setup --skills still routed to the not-yet-implemented stub\noutput:\n%s", combined)
	}
}

// TestSetup_Skills_InstallsBundledSkillsUnderHome pins the documented
// install location: `~/.claude/skills/marunage-*/SKILL.md`.
func TestSetup_Skills_InstallsBundledSkillsUnderHome(t *testing.T) {
	home := t.TempDir()
	withHomeDir(t, home)

	var stdout, stderr bytes.Buffer
	code := Execute([]string{"setup", "--skills"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("setup --skills exit=%d; stderr=%q", code, stderr.String())
	}

	for _, name := range []string{"marunage-triage", "marunage-execute", "marunage-reflect"} {
		path := filepath.Join(home, ".claude", "skills", name, "SKILL.md")
		if _, err := os.Stat(path); err != nil {
			t.Errorf("expected %s to exist: %v", path, err)
		}
	}

	if !strings.Contains(stdout.String(), "marunage-triage") {
		t.Errorf("setup --skills should report installed skills; stdout=%q", stdout.String())
	}
}

// TestSetup_Skills_RequiresSkillsFlag pins the documented surface:
// `marunage setup` without `--skills` (and without other future
// sub-flags) is currently a no-op-with-help so a user who fat-fingers
// the flag does not silently get the wrong behaviour.
func TestSetup_Skills_RequiresSkillsFlag(t *testing.T) {
	home := t.TempDir()
	withHomeDir(t, home)

	var stdout, stderr bytes.Buffer
	code := Execute([]string{"setup"}, &stdout, &stderr)
	if code == 0 {
		t.Fatalf("setup with no flags exit=0; want non-zero (must specify a sub-flag)")
	}
	combined := stdout.String() + stderr.String()
	if !strings.Contains(combined, "--skills") {
		t.Errorf("error should mention --skills as the available sub-flag; got %q", combined)
	}

	// And no skills should have been installed.
	if _, err := os.Stat(filepath.Join(home, ".claude", "skills")); err == nil {
		t.Errorf("setup with no flags should not provision the skills directory")
	}
}

// TestSetup_Skills_FromDir wires the --from-dir flag. We seed a tiny
// custom skills tree so the test does not depend on the embedded body.
func TestSetup_Skills_FromDir(t *testing.T) {
	home := t.TempDir()
	withHomeDir(t, home)

	src := filepath.Join(t.TempDir(), "my-skills")
	if err := os.MkdirAll(filepath.Join(src, "marunage-triage"), 0o755); err != nil {
		t.Fatalf("seed src: %v", err)
	}
	body := []byte("<!-- version: 7.0.0 -->\n# custom\n## 判定ロジック\nx\n## 出力フォーマット\nx\n")
	if err := os.WriteFile(filepath.Join(src, "marunage-triage", "SKILL.md"), body, 0o644); err != nil {
		t.Fatalf("seed src triage: %v", err)
	}

	var stdout, stderr bytes.Buffer
	code := Execute([]string{"setup", "--skills", "--from-dir", src}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("setup --from-dir exit=%d; stderr=%q", code, stderr.String())
	}

	got, err := os.ReadFile(filepath.Join(home, ".claude", "skills", "marunage-triage", "SKILL.md"))
	if err != nil {
		t.Fatalf("read installed: %v", err)
	}
	if !strings.Contains(string(got), "version: 7.0.0") {
		t.Errorf("custom from-dir body did not land on disk; got %q", got)
	}
}

// TestSetup_Skills_CheckUpdates routes through the no-write
// `--check-updates` mode and asserts the version pair appears in stdout.
func TestSetup_Skills_CheckUpdates(t *testing.T) {
	home := t.TempDir()
	withHomeDir(t, home)

	// Seed an older on-disk triage so check-updates has something to
	// compare against.
	dir := filepath.Join(home, ".claude", "skills", "marunage-triage")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatalf("seed dir: %v", err)
	}
	old := []byte("<!-- version: 0.0.1 -->\n# old\n## 判定ロジック\n## 出力フォーマット\n")
	if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), old, 0o600); err != nil {
		t.Fatalf("seed: %v", err)
	}

	var stdout, stderr bytes.Buffer
	code := Execute([]string{"setup", "--skills", "--check-updates"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("setup --check-updates exit=%d; stderr=%q", code, stderr.String())
	}
	out := stdout.String()
	if !strings.Contains(out, "0.0.1") || !strings.Contains(out, "0.1.0") {
		t.Errorf("--check-updates output missing version pair; got %q", out)
	}

	// And the on-disk SKILL.md must be untouched.
	got, err := os.ReadFile(filepath.Join(dir, "SKILL.md"))
	if err != nil {
		t.Fatalf("read after check: %v", err)
	}
	if !bytes.Equal(got, old) {
		t.Errorf("--check-updates mutated the file")
	}
}

// TestSetup_Skills_FailsOnMissingRequiredSection wires the error path:
// an invalid --from-dir (triage missing a required H2) must surface a
// non-zero exit code so CI catches the misconfiguration immediately.
func TestSetup_Skills_FailsOnMissingRequiredSection(t *testing.T) {
	home := t.TempDir()
	withHomeDir(t, home)

	src := filepath.Join(t.TempDir(), "broken-skills")
	if err := os.MkdirAll(filepath.Join(src, "marunage-triage"), 0o755); err != nil {
		t.Fatalf("seed src: %v", err)
	}
	body := []byte("<!-- version: 0.1.0 -->\n# broken\n## 判定ロジック\nonly half\n")
	if err := os.WriteFile(filepath.Join(src, "marunage-triage", "SKILL.md"), body, 0o644); err != nil {
		t.Fatalf("seed src triage: %v", err)
	}

	var stdout, stderr bytes.Buffer
	code := Execute([]string{"setup", "--skills", "--from-dir", src}, &stdout, &stderr)
	if code == 0 {
		t.Fatalf("setup with broken triage exit=0; want non-zero; stdout=%q", stdout.String())
	}
	if !strings.Contains(stderr.String()+stdout.String(), "出力フォーマット") {
		t.Errorf("error message should name the missing section; got stderr=%q stdout=%q",
			stderr.String(), stdout.String())
	}
}

// TestSetup_Skills_HelpDescribesFlags pins the --help surface so cobra's
// generator stays wired and the user-facing description mentions the
// distinguishing flags.
func TestSetup_Skills_HelpDescribesFlags(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := Execute([]string{"setup", "--help"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("setup --help exit=%d; stderr=%q", code, stderr.String())
	}
	out := stdout.String()
	for _, want := range []string{"--skills", "--diff", "--force", "--check-updates", "--from-dir"} {
		if !strings.Contains(out, want) {
			t.Errorf("setup --help missing %q\nfull output:\n%s", want, out)
		}
	}
}

// TestSetup_Skills_PermissionsOnDisk pins per-user secret hygiene at
// the CLI layer so a regression in `internal/skills.writeSkill` would
// surface in CI even if no one runs the unit tests directly.
func TestSetup_Skills_PermissionsOnDisk(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("posix permissions not enforced on windows")
	}
	home := t.TempDir()
	withHomeDir(t, home)

	var stdout, stderr bytes.Buffer
	if code := Execute([]string{"setup", "--skills"}, &stdout, &stderr); code != 0 {
		t.Fatalf("setup --skills exit=%d; stderr=%q", code, stderr.String())
	}

	dir := filepath.Join(home, ".claude", "skills", "marunage-triage")
	dirInfo, err := os.Stat(dir)
	if err != nil {
		t.Fatalf("stat skill dir: %v", err)
	}
	if perm := dirInfo.Mode().Perm(); perm != 0o700 {
		t.Errorf("skill dir perm = %o; want 0700", perm)
	}
	fileInfo, err := os.Stat(filepath.Join(dir, "SKILL.md"))
	if err != nil {
		t.Fatalf("stat SKILL.md: %v", err)
	}
	if perm := fileInfo.Mode().Perm(); perm != 0o600 {
		t.Errorf("SKILL.md perm = %o; want 0600", perm)
	}
}
