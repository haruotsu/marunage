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

// TestSetup_Skills_DiffCLI pins the --diff arm end-to-end: `marunage
// setup --skills --diff` must surface a diff for an edited skill and
// must not write to disk. This is the CLI mirror of
// TestInstall_Diff_PrintsDiffWithoutWriting.
func TestSetup_Skills_DiffCLI(t *testing.T) {
	home := t.TempDir()
	withHomeDir(t, home)

	var b1, b2 bytes.Buffer
	if code := Execute([]string{"setup", "--skills"}, &b1, &b2); code != 0 {
		t.Fatalf("seed: %v", b2.String())
	}
	skillPath := filepath.Join(home, ".claude", "skills", "marunage-triage", "SKILL.md")
	userBody := []byte("locally edited via CLI\n")
	if err := os.WriteFile(skillPath, userBody, 0o600); err != nil {
		t.Fatalf("seed edit: %v", err)
	}

	var stdout, stderr bytes.Buffer
	if code := Execute([]string{"setup", "--skills", "--diff"}, &stdout, &stderr); code != 0 {
		t.Fatalf("setup --diff exit=%d; stderr=%q", code, stderr.String())
	}
	if !bytes.Contains(stdout.Bytes(), []byte("locally edited via CLI")) {
		t.Errorf("--diff output missing on-disk content marker; stdout=%q", stdout.String())
	}
	got, err := os.ReadFile(skillPath)
	if err != nil {
		t.Fatalf("read after --diff: %v", err)
	}
	if !bytes.Equal(got, userBody) {
		t.Errorf("--diff mutated the file; want %q got %q", userBody, got)
	}
}

// TestSetup_Skills_ForceCLI pins the --force arm end-to-end: a hand-
// edited skill is overwritten with the embedded body when the user
// passes --force.
func TestSetup_Skills_ForceCLI(t *testing.T) {
	home := t.TempDir()
	withHomeDir(t, home)

	var b1, b2 bytes.Buffer
	if code := Execute([]string{"setup", "--skills"}, &b1, &b2); code != 0 {
		t.Fatalf("seed: %v", b2.String())
	}
	skillPath := filepath.Join(home, ".claude", "skills", "marunage-triage", "SKILL.md")
	userBody := []byte("---\nname: marunage-triage\ndescription: edited\n---\n<!-- version: 0.1.0 -->\n# edit\n## 判定ロジック\nx\n## 出力フォーマット\nx\n")
	if err := os.WriteFile(skillPath, userBody, 0o600); err != nil {
		t.Fatalf("seed edit: %v", err)
	}

	var stdout, stderr bytes.Buffer
	if code := Execute([]string{"setup", "--skills", "--force"}, &stdout, &stderr); code != 0 {
		t.Fatalf("setup --force exit=%d; stderr=%q", code, stderr.String())
	}
	got, err := os.ReadFile(skillPath)
	if err != nil {
		t.Fatalf("read after --force: %v", err)
	}
	if bytes.Equal(got, userBody) {
		t.Errorf("--force did not overwrite the on-disk SKILL.md")
	}
	if !strings.Contains(stdout.String(), "Updated") {
		t.Errorf("--force did not surface 'Updated:' in summary; stdout=%q", stdout.String())
	}
}

// TestSetup_Skills_FromDirTildeExpand pins that `--from-dir ~/foo` is
// expanded relative to $HOME so script invocations that single-quote
// the path still work. Without this, `marunage setup --skills
// --from-dir '~/my-skills'` would fail with a literal `~/...` Stat.
func TestSetup_Skills_FromDirTildeExpand(t *testing.T) {
	home := t.TempDir()
	withHomeDir(t, home)

	src := filepath.Join(home, "my-skills")
	if err := os.MkdirAll(filepath.Join(src, "marunage-triage"), 0o755); err != nil {
		t.Fatalf("seed src: %v", err)
	}
	body := []byte("---\nname: marunage-triage\ndescription: x\n---\n<!-- version: 5.0.0 -->\n# t\n## 判定ロジック\nx\n## 出力フォーマット\nx\n")
	if err := os.WriteFile(filepath.Join(src, "marunage-triage", "SKILL.md"), body, 0o644); err != nil {
		t.Fatalf("seed: %v", err)
	}

	var stdout, stderr bytes.Buffer
	code := Execute([]string{"setup", "--skills", "--from-dir", "~/my-skills"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("setup --from-dir ~/my-skills exit=%d; stderr=%q", code, stderr.String())
	}
	got, err := os.ReadFile(filepath.Join(home, ".claude", "skills", "marunage-triage", "SKILL.md"))
	if err != nil {
		t.Fatalf("read installed: %v", err)
	}
	if !strings.Contains(string(got), "version: 5.0.0") {
		t.Errorf("tilde-expanded body not landed; got %q", got)
	}
}

// TestSetup_Skills_MergeCLI pins the --merge arm end-to-end: the CLI
// must thread activeStdinReader through to the installer's prompt loop
// so an "o\n" answer overwrites a hand-edited skill. The unit-level
// merge tests in internal/skills already pin the prompt branches; this
// guards against the wiring (`In: activeStdinReader()`) silently
// breaking.
func TestSetup_Skills_MergeCLI(t *testing.T) {
	home := t.TempDir()
	withHomeDir(t, home)
	withStdinReader(t, strings.NewReader("o\n"))

	var b1, b2 bytes.Buffer
	if code := Execute([]string{"setup", "--skills"}, &b1, &b2); code != 0 {
		t.Fatalf("seed: %v", b2.String())
	}
	skillPath := filepath.Join(home, ".claude", "skills", "marunage-triage", "SKILL.md")
	if err := os.WriteFile(skillPath, []byte("locally edited via CLI\n"), 0o600); err != nil {
		t.Fatalf("seed edit: %v", err)
	}

	var stdout, stderr bytes.Buffer
	if code := Execute([]string{"setup", "--skills", "--merge"}, &stdout, &stderr); code != 0 {
		t.Fatalf("setup --merge exit=%d; stderr=%q", code, stderr.String())
	}
	got, err := os.ReadFile(skillPath)
	if err != nil {
		t.Fatalf("read after --merge: %v", err)
	}
	if bytes.Contains(got, []byte("locally edited via CLI")) {
		t.Errorf("merge 'o' did not overwrite via CLI; SKILL.md still has user edit: %q", got)
	}
	if !strings.Contains(stdout.String(), "Updated") {
		t.Errorf("--merge 'o' did not surface 'Updated:' in summary; stdout=%q", stdout.String())
	}
}

// TestSetup_Skills_RecordsAuditLines pins the "No silent execution"
// invariant for setup --skills: every install / update / skip
// classification leaves a typed line in
// ~/.marunage/logs/audit.log so an operator can audit when each Skill
// was provisioned. The precedent is `init.create` / `init.skip` from
// internal/initialize.
func TestSetup_Skills_RecordsAuditLines(t *testing.T) {
	home := t.TempDir()
	withHomeDir(t, home)

	var stdout, stderr bytes.Buffer
	if code := Execute([]string{"setup", "--skills"}, &stdout, &stderr); code != 0 {
		t.Fatalf("setup --skills exit=%d; stderr=%q", code, stderr.String())
	}

	auditPath := filepath.Join(home, ".marunage", "logs", "audit.log")
	lines := readCLIAuditLines(t, auditPath)

	var installs []cliAuditLine
	for _, l := range lines {
		if l.Action == "setup.skills.install" {
			installs = append(installs, l)
		}
	}
	if len(installs) != 3 {
		t.Fatalf("setup.skills.install count = %d; want 3 (one per bundled skill); lines=%+v",
			len(installs), lines)
	}

	wantNames := map[string]bool{"marunage-execute": true, "marunage-reflect": true, "marunage-triage": true}
	for _, l := range installs {
		if !wantNames[l.Name] {
			t.Errorf("unexpected install Name=%q (want one of marunage-execute/-reflect/-triage)", l.Name)
		}
		want := filepath.Join(home, ".claude", "skills", l.Name, "SKILL.md")
		if l.Path != want {
			t.Errorf("install Path = %q; want %q", l.Path, want)
		}
	}
}

// TestSetup_Skills_AuditOnSkipAndUpdate pins that the rerun / force
// classifications also land on disk, so an audit trail can distinguish
// "marunage left this skill alone" from "marunage rewrote it" later.
func TestSetup_Skills_AuditOnSkipAndUpdate(t *testing.T) {
	home := t.TempDir()
	withHomeDir(t, home)

	// Seed.
	var stdout, stderr bytes.Buffer
	if code := Execute([]string{"setup", "--skills"}, &stdout, &stderr); code != 0 {
		t.Fatalf("seed: %v", stderr.String())
	}
	// Re-run: same content -> 3 skips.
	stdout.Reset()
	stderr.Reset()
	if code := Execute([]string{"setup", "--skills"}, &stdout, &stderr); code != 0 {
		t.Fatalf("rerun: %v", stderr.String())
	}
	// Hand-edit triage to differ, then --force on all (triage updates,
	// the rest stay byte-equal and skip).
	skillPath := filepath.Join(home, ".claude", "skills", "marunage-triage", "SKILL.md")
	edited := []byte("---\nname: marunage-triage\ndescription: edited\n---\n<!-- version: 0.1.0 -->\n# edit\n\n## 判定ロジック\nmine\n\n## 出力フォーマット\nmine\n")
	if err := os.WriteFile(skillPath, edited, 0o600); err != nil {
		t.Fatalf("seed edit: %v", err)
	}
	stdout.Reset()
	stderr.Reset()
	if code := Execute([]string{"setup", "--skills", "--force"}, &stdout, &stderr); code != 0 {
		t.Fatalf("force: %v", stderr.String())
	}

	auditPath := filepath.Join(home, ".marunage", "logs", "audit.log")
	lines := readCLIAuditLines(t, auditPath)

	var installs, skips, updates int
	for _, l := range lines {
		switch l.Action {
		case "setup.skills.install":
			installs++
		case "setup.skills.skip":
			skips++
		case "setup.skills.update":
			updates++
		}
	}
	if installs != 3 {
		t.Errorf("install count = %d; want 3 (initial seed)", installs)
	}
	// rerun: 3 skip; force: 2 skip (execute/reflect unchanged) + 1 update (triage).
	if skips != 5 {
		t.Errorf("skip count = %d; want 5 (3 rerun + 2 force-on-unchanged)", skips)
	}
	if updates != 1 {
		t.Errorf("update count = %d; want 1 (only edited triage)", updates)
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
