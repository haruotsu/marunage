package skills

import (
	"bytes"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"testing"
	"testing/fstest"
)

// TestEmbeddedFS_ContainsRequiredSkills pins that the //go:embed bundle
// actually shipped the three skills marunage's Phase 1 design depends on.
// Without this guard, a refactor that renamed the embed path could quietly
// produce a binary that "installs" zero skills.
func TestEmbeddedFS_ContainsRequiredSkills(t *testing.T) {
	root := EmbeddedFS()
	for _, name := range []string{"marunage-triage", "marunage-execute", "marunage-reflect"} {
		path := name + "/SKILL.md"
		f, err := root.Open(path)
		if err != nil {
			t.Errorf("embedded %s: %v", path, err)
			continue
		}
		_ = f.Close()
	}
}

// TestEmbeddedSkills_PassRequiredSectionValidation pins that the OSS-
// shipped triage SKILL.md satisfies the contract installer enforces. If
// future edits drop `## 判定ロジック` or `## 出力フォーマット`, every
// fresh install would fail, so we catch it here at unit-test time.
func TestEmbeddedSkills_PassRequiredSectionValidation(t *testing.T) {
	root := EmbeddedFS()
	body, err := fs.ReadFile(root, "marunage-triage/SKILL.md")
	if err != nil {
		t.Fatalf("read embedded triage: %v", err)
	}
	if err := ValidateRequiredSections(body, RequiredTriageSections); err != nil {
		t.Errorf("embedded marunage-triage SKILL.md: %v", err)
	}
}

// TestInstall_FreshTarget_CopiesAllSkillsFromEmbed pins the happy path:
// no existing ~/.claude/skills, install writes every embedded skill, and
// the result lists each one under Installed (not Skipped or Updated).
func TestInstall_FreshTarget_CopiesAllSkillsFromEmbed(t *testing.T) {
	target := filepath.Join(t.TempDir(), ".claude", "skills")

	res, err := Install(InstallOptions{
		Target: target,
		Source: EmbeddedFS(),
	})
	if err != nil {
		t.Fatalf("Install: %v", err)
	}

	got := names(res.Installed)
	want := []string{"marunage-execute", "marunage-reflect", "marunage-triage"}
	if !equalSorted(got, want) {
		t.Errorf("Installed names = %v; want %v", got, want)
	}
	if len(res.Skipped) != 0 {
		t.Errorf("Skipped should be empty on fresh install; got %v", names(res.Skipped))
	}
	if len(res.Updated) != 0 {
		t.Errorf("Updated should be empty on fresh install; got %v", names(res.Updated))
	}

	// Each skill's SKILL.md must exist on disk after the install.
	for _, name := range want {
		path := filepath.Join(target, name, "SKILL.md")
		if _, err := os.Stat(path); err != nil {
			t.Errorf("expected %s to exist: %v", path, err)
		}
	}
}

// TestInstall_CreatesParentDirectory pins the convenience invariant:
// users running `marunage setup --skills` on a brand-new machine should
// not have to mkdir ~/.claude/skills/ themselves. Install must materialise
// every missing parent.
func TestInstall_CreatesParentDirectory(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, ".claude", "skills")

	if _, err := os.Stat(target); !os.IsNotExist(err) {
		t.Fatalf("test setup error: %s should not exist yet", target)
	}

	if _, err := Install(InstallOptions{Target: target, Source: EmbeddedFS()}); err != nil {
		t.Fatalf("Install: %v", err)
	}

	if _, err := os.Stat(target); err != nil {
		t.Errorf("Install did not create %s: %v", target, err)
	}
}

// TestInstall_Permissions pins the per-user secret hygiene: SKILL.md
// files are 0600 and skill directories are 0700, mirroring how
// internal/initialize provisions ~/.marunage. Users on shared hosts
// should never have to re-chmod after install.
func TestInstall_Permissions(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("posix permissions not enforced on windows")
	}
	target := filepath.Join(t.TempDir(), ".claude", "skills")

	if _, err := Install(InstallOptions{Target: target, Source: EmbeddedFS()}); err != nil {
		t.Fatalf("Install: %v", err)
	}

	dir := filepath.Join(target, "marunage-triage")
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

// TestInstall_ExistingSameVersion_IsIdempotent pins the documented "do
// not overwrite by default" invariant alongside the version-comparison
// short-circuit: re-running install with no changes neither rewrites the
// file nor reports it as Updated.
func TestInstall_ExistingSameVersion_IsIdempotent(t *testing.T) {
	target := filepath.Join(t.TempDir(), ".claude", "skills")

	// First install seeds the on-disk copies.
	if _, err := Install(InstallOptions{Target: target, Source: EmbeddedFS()}); err != nil {
		t.Fatalf("first Install: %v", err)
	}
	skillPath := filepath.Join(target, "marunage-triage", "SKILL.md")
	infoBefore, err := os.Stat(skillPath)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}

	// Second install should leave the file untouched.
	res, err := Install(InstallOptions{Target: target, Source: EmbeddedFS()})
	if err != nil {
		t.Fatalf("second Install: %v", err)
	}
	if len(res.Installed) != 0 {
		t.Errorf("second Install reported Installed=%v; want empty", names(res.Installed))
	}
	if len(res.Updated) != 0 {
		t.Errorf("second Install reported Updated=%v; want empty (same version)", names(res.Updated))
	}
	got := names(res.Skipped)
	want := []string{"marunage-execute", "marunage-reflect", "marunage-triage"}
	if !equalSorted(got, want) {
		t.Errorf("Skipped = %v; want %v", got, want)
	}

	infoAfter, err := os.Stat(skillPath)
	if err != nil {
		t.Fatalf("stat after: %v", err)
	}
	if !infoAfter.ModTime().Equal(infoBefore.ModTime()) {
		t.Errorf("SKILL.md mtime changed despite same-version skip: before=%v after=%v",
			infoBefore.ModTime(), infoAfter.ModTime())
	}
}

// TestInstall_ExistingEdited_NoForce_PreservesEdit pins the "users can
// customise" promise: an on-disk SKILL.md the user has hand-edited
// (different content, same version) must be preserved without --force.
func TestInstall_ExistingEdited_NoForce_PreservesEdit(t *testing.T) {
	target := filepath.Join(t.TempDir(), ".claude", "skills")
	if _, err := Install(InstallOptions{Target: target, Source: EmbeddedFS()}); err != nil {
		t.Fatalf("seed Install: %v", err)
	}
	skillPath := filepath.Join(target, "marunage-triage", "SKILL.md")
	userBody := []byte("<!-- version: 0.1.0 -->\n# user-edited\n\n## 判定ロジック\nmine\n\n## 出力フォーマット\nmine\n")
	if err := os.WriteFile(skillPath, userBody, 0o600); err != nil {
		t.Fatalf("seed user edit: %v", err)
	}

	res, err := Install(InstallOptions{Target: target, Source: EmbeddedFS()})
	if err != nil {
		t.Fatalf("Install: %v", err)
	}
	if !contains(names(res.Skipped), "marunage-triage") {
		t.Errorf("Skipped missing marunage-triage; got %v", names(res.Skipped))
	}

	got, err := os.ReadFile(skillPath)
	if err != nil {
		t.Fatalf("read skill: %v", err)
	}
	if !bytes.Equal(got, userBody) {
		t.Errorf("user edit was overwritten without --force\nbefore=%q\nafter =%q", userBody, got)
	}
}

// TestInstall_Force_OverwritesExisting pins the --force escape hatch:
// when the user explicitly asks, even an edited SKILL.md is replaced.
func TestInstall_Force_OverwritesExisting(t *testing.T) {
	target := filepath.Join(t.TempDir(), ".claude", "skills")
	if _, err := Install(InstallOptions{Target: target, Source: EmbeddedFS()}); err != nil {
		t.Fatalf("seed Install: %v", err)
	}
	skillPath := filepath.Join(target, "marunage-triage", "SKILL.md")
	if err := os.WriteFile(skillPath, []byte("old\n"), 0o600); err != nil {
		t.Fatalf("seed user edit: %v", err)
	}

	res, err := Install(InstallOptions{Target: target, Source: EmbeddedFS(), Force: true})
	if err != nil {
		t.Fatalf("Install --force: %v", err)
	}
	if !contains(names(res.Updated), "marunage-triage") {
		t.Errorf("Updated missing marunage-triage; got %v", names(res.Updated))
	}

	got, err := os.ReadFile(skillPath)
	if err != nil {
		t.Fatalf("read after force: %v", err)
	}
	if bytes.Equal(got, []byte("old\n")) {
		t.Errorf("--force did not overwrite the on-disk SKILL.md")
	}
}

// TestInstall_Diff_PrintsDiffWithoutWriting pins the --diff dry-run
// behaviour: the embedded vs on-disk delta is rendered, but nothing on
// disk changes.
func TestInstall_Diff_PrintsDiffWithoutWriting(t *testing.T) {
	target := filepath.Join(t.TempDir(), ".claude", "skills")
	if _, err := Install(InstallOptions{Target: target, Source: EmbeddedFS()}); err != nil {
		t.Fatalf("seed Install: %v", err)
	}
	skillPath := filepath.Join(target, "marunage-triage", "SKILL.md")
	userBody := []byte("locally edited\n")
	if err := os.WriteFile(skillPath, userBody, 0o600); err != nil {
		t.Fatalf("seed edit: %v", err)
	}

	var buf bytes.Buffer
	res, err := Install(InstallOptions{
		Target: target,
		Source: EmbeddedFS(),
		Diff:   true,
		Out:    &buf,
	})
	if err != nil {
		t.Fatalf("Install --diff: %v", err)
	}
	if len(res.Updated) != 0 || len(res.Installed) != 0 {
		t.Errorf("--diff should not mutate; got Installed=%v Updated=%v",
			names(res.Installed), names(res.Updated))
	}

	got, err := os.ReadFile(skillPath)
	if err != nil {
		t.Fatalf("read after diff: %v", err)
	}
	if !bytes.Equal(got, userBody) {
		t.Errorf("--diff mutated the file; want %q got %q", userBody, got)
	}
	if !strings.Contains(buf.String(), "marunage-triage") {
		t.Errorf("--diff output missing skill name; got %q", buf.String())
	}
	if !strings.Contains(buf.String(), "locally edited") {
		t.Errorf("--diff output missing on-disk content marker; got %q", buf.String())
	}
}

// TestInstall_CheckUpdates_ListsDriftAndDoesNotWrite pins the bookkeeping
// behaviour of --check-updates: it reports versions side-by-side without
// touching the file system.
func TestInstall_CheckUpdates_ListsDriftAndDoesNotWrite(t *testing.T) {
	target := filepath.Join(t.TempDir(), ".claude", "skills")
	// Seed an "older" install by writing a SKILL.md with version 0.0.1.
	triageDir := filepath.Join(target, "marunage-triage")
	if err := os.MkdirAll(triageDir, 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	old := []byte("<!-- version: 0.0.1 -->\n# old\n## 判定ロジック\n## 出力フォーマット\n")
	if err := os.WriteFile(filepath.Join(triageDir, "SKILL.md"), old, 0o600); err != nil {
		t.Fatalf("seed: %v", err)
	}

	var buf bytes.Buffer
	res, err := Install(InstallOptions{
		Target:       target,
		Source:       EmbeddedFS(),
		CheckUpdates: true,
		Out:          &buf,
	})
	if err != nil {
		t.Fatalf("Install --check-updates: %v", err)
	}
	if len(res.Installed) != 0 || len(res.Updated) != 0 {
		t.Errorf("--check-updates must not write; got Installed=%v Updated=%v",
			names(res.Installed), names(res.Updated))
	}
	out := buf.String()
	if !strings.Contains(out, "marunage-triage") {
		t.Errorf("--check-updates output missing marunage-triage; got %q", out)
	}
	if !strings.Contains(out, "0.0.1") || !strings.Contains(out, "0.1.0") {
		t.Errorf("--check-updates output missing version pair; got %q", out)
	}

	// On-disk file remains the seeded content.
	got, err := os.ReadFile(filepath.Join(triageDir, "SKILL.md"))
	if err != nil {
		t.Fatalf("read after check: %v", err)
	}
	if !bytes.Equal(got, old) {
		t.Errorf("--check-updates mutated the file; want %q got %q", old, got)
	}
}

// TestInstall_FromCustomFS proves the Source plumbing: --from-dir is
// modelled by passing an arbitrary fs.FS, so tests inject an in-memory
// FS without juggling tempdirs for "what if the user supplied their
// own skill directory".
func TestInstall_FromCustomFS(t *testing.T) {
	src := fstest.MapFS{
		"marunage-triage/SKILL.md": &fstest.MapFile{
			Data: []byte("<!-- version: 9.9.9 -->\n# custom\n## 判定ロジック\nlocal\n## 出力フォーマット\nlocal\n"),
		},
	}
	target := filepath.Join(t.TempDir(), ".claude", "skills")

	res, err := Install(InstallOptions{Target: target, Source: src})
	if err != nil {
		t.Fatalf("Install: %v", err)
	}
	if !equalSorted(names(res.Installed), []string{"marunage-triage"}) {
		t.Errorf("Installed = %v; want [marunage-triage]", names(res.Installed))
	}

	got, err := os.ReadFile(filepath.Join(target, "marunage-triage", "SKILL.md"))
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if !strings.Contains(string(got), "version: 9.9.9") {
		t.Errorf("custom FS body did not land on disk; got %q", got)
	}
}

// TestInstall_TriageMissingRequiredSection_Fails pins the documented
// "validate after install" requirement: if the triage SKILL.md the user
// or a third-party source supplies lacks 判定ロジック / 出力フォーマット,
// install must fail loudly so the user does not silently end up with a
// broken Orient phase.
func TestInstall_TriageMissingRequiredSection_Fails(t *testing.T) {
	src := fstest.MapFS{
		"marunage-triage/SKILL.md": &fstest.MapFile{
			Data: []byte("<!-- version: 0.1.0 -->\n# broken triage\n## 判定ロジック\nonly half\n"),
		},
	}
	target := filepath.Join(t.TempDir(), ".claude", "skills")

	_, err := Install(InstallOptions{Target: target, Source: src})
	if !errors.Is(err, ErrMissingSection) {
		t.Errorf("err = %v; want errors.Is(_, ErrMissingSection)", err)
	}
}

// TestInstall_Merge_OverwriteChoice pins the simplest --merge contract:
// for each conflicting skill the operator is offered a choice; selecting
// "o"verwrite produces the same end state as --force for that skill
// alone, while leaving other (un-prompted, identical) skills untouched.
func TestInstall_Merge_OverwriteChoice(t *testing.T) {
	target := filepath.Join(t.TempDir(), ".claude", "skills")
	if _, err := Install(InstallOptions{Target: target, Source: EmbeddedFS()}); err != nil {
		t.Fatalf("seed Install: %v", err)
	}
	skillPath := filepath.Join(target, "marunage-triage", "SKILL.md")
	if err := os.WriteFile(skillPath, []byte("locally edited\n"), 0o600); err != nil {
		t.Fatalf("seed edit: %v", err)
	}

	var stdout bytes.Buffer
	res, err := Install(InstallOptions{
		Target: target,
		Source: EmbeddedFS(),
		Merge:  true,
		Out:    &stdout,
		In:     strings.NewReader("o\n"),
	})
	if err != nil {
		t.Fatalf("Install --merge: %v", err)
	}
	if !contains(names(res.Updated), "marunage-triage") {
		t.Errorf("Updated missing marunage-triage; got %v", names(res.Updated))
	}
	got, err := os.ReadFile(skillPath)
	if err != nil {
		t.Fatalf("read after merge: %v", err)
	}
	if bytes.Contains(got, []byte("locally edited")) {
		t.Errorf("merge 'o' did not overwrite the on-disk SKILL.md; got %q", got)
	}
}

// TestInstall_Merge_DiffThenOverwrite pins the third merge branch:
// the "d" choice prints a diff and re-prompts. Sending "d\no\n" should
// dump a diff to Out and then end up overwriting (the second answer).
func TestInstall_Merge_DiffThenOverwrite(t *testing.T) {
	target := filepath.Join(t.TempDir(), ".claude", "skills")
	if _, err := Install(InstallOptions{Target: target, Source: EmbeddedFS()}); err != nil {
		t.Fatalf("seed Install: %v", err)
	}
	skillPath := filepath.Join(target, "marunage-triage", "SKILL.md")
	if err := os.WriteFile(skillPath, []byte("locally edited triage\n"), 0o600); err != nil {
		t.Fatalf("seed edit: %v", err)
	}

	var stdout bytes.Buffer
	res, err := Install(InstallOptions{
		Target: target,
		Source: EmbeddedFS(),
		Merge:  true,
		Out:    &stdout,
		In:     strings.NewReader("d\no\n"),
	})
	if err != nil {
		t.Fatalf("Install --merge d->o: %v", err)
	}
	if !contains(names(res.Updated), "marunage-triage") {
		t.Errorf("Updated missing marunage-triage; got %v", names(res.Updated))
	}
	if !strings.Contains(stdout.String(), "locally edited triage") {
		t.Errorf("merge 'd' did not render a diff containing the on-disk body; got %q", stdout.String())
	}
}

// TestInstall_Merge_RejectsUnknownChoice pins the input-validation
// branch: an unknown answer must trigger a re-prompt rather than be
// silently treated as skip-or-overwrite. We send "x\ns\n" and assert
// the "unknown choice" message lands and the file is preserved.
func TestInstall_Merge_RejectsUnknownChoice(t *testing.T) {
	target := filepath.Join(t.TempDir(), ".claude", "skills")
	if _, err := Install(InstallOptions{Target: target, Source: EmbeddedFS()}); err != nil {
		t.Fatalf("seed Install: %v", err)
	}
	skillPath := filepath.Join(target, "marunage-triage", "SKILL.md")
	userBody := []byte("---\nname: marunage-triage\ndescription: edit\n---\n<!-- version: 0.1.0 -->\n# edit\n## 判定ロジック\nx\n## 出力フォーマット\nx\n")
	if err := os.WriteFile(skillPath, userBody, 0o600); err != nil {
		t.Fatalf("seed edit: %v", err)
	}

	var stdout bytes.Buffer
	if _, err := Install(InstallOptions{
		Target: target,
		Source: EmbeddedFS(),
		Merge:  true,
		Out:    &stdout,
		In:     strings.NewReader("xyz\ns\n"),
	}); err != nil {
		t.Fatalf("Install --merge xyz->s: %v", err)
	}
	if !strings.Contains(stdout.String(), "unknown choice") {
		t.Errorf("unknown answer did not surface 'unknown choice'; got %q", stdout.String())
	}
}

// TestInstall_Merge_SkipChoice pins that "s" preserves the on-disk
// content, mirroring how a no-flag run handles drift. We seed the user
// edit with a structurally valid SKILL.md (matching required sections)
// so the post-install validator does not turn a "skip on purpose" into
// an unrelated failure.
func TestInstall_Merge_SkipChoice(t *testing.T) {
	target := filepath.Join(t.TempDir(), ".claude", "skills")
	if _, err := Install(InstallOptions{Target: target, Source: EmbeddedFS()}); err != nil {
		t.Fatalf("seed Install: %v", err)
	}
	skillPath := filepath.Join(target, "marunage-triage", "SKILL.md")
	userBody := []byte("<!-- version: 0.1.0 -->\n# user edit\n\n## 判定ロジック\nmine\n\n## 出力フォーマット\nmine\n")
	if err := os.WriteFile(skillPath, userBody, 0o600); err != nil {
		t.Fatalf("seed edit: %v", err)
	}

	var stdout bytes.Buffer
	res, err := Install(InstallOptions{
		Target: target,
		Source: EmbeddedFS(),
		Merge:  true,
		Out:    &stdout,
		In:     strings.NewReader("s\n"),
	})
	if err != nil {
		t.Fatalf("Install --merge skip: %v", err)
	}
	if !contains(names(res.Skipped), "marunage-triage") {
		t.Errorf("Skipped missing marunage-triage; got %v", names(res.Skipped))
	}
	got, err := os.ReadFile(skillPath)
	if err != nil {
		t.Fatalf("read after merge: %v", err)
	}
	if !bytes.Equal(got, userBody) {
		t.Errorf("merge 's' overwrote the file; want %q got %q", userBody, got)
	}
}

// TestInstall_RejectsSymlinkedSkillFile pins the read-side symlink
// defense: if an attacker (or a confused user) has placed
// `~/.claude/skills/marunage-triage/SKILL.md` as a symlink pointing at
// an arbitrary file, neither --diff nor a normal install must follow
// it. --diff would otherwise dump the linked file's contents to stdout
// (information disclosure); a normal install would silently see "same
// content? no" and trigger a write decision based on a third-party
// file. We reject the install instead and surface the symlink so the
// user can sort out their tree.
func TestInstall_RejectsSymlinkedSkillFile(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink semantics differ on windows")
	}
	target := filepath.Join(t.TempDir(), ".claude", "skills")
	if err := os.MkdirAll(filepath.Join(target, "marunage-triage"), 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	// Plant a secret file the symlink will point at, then aim the
	// SKILL.md path at it.
	secret := filepath.Join(t.TempDir(), "secret.txt")
	if err := os.WriteFile(secret, []byte("SUPER SECRET TOKEN"), 0o600); err != nil {
		t.Fatalf("seed secret: %v", err)
	}
	skillPath := filepath.Join(target, "marunage-triage", "SKILL.md")
	if err := os.Symlink(secret, skillPath); err != nil {
		t.Fatalf("symlink: %v", err)
	}

	var buf bytes.Buffer
	_, err := Install(InstallOptions{
		Target: target,
		Source: EmbeddedFS(),
		Diff:   true,
		Out:    &buf,
	})
	if err == nil {
		t.Fatalf("Install --diff over a symlinked SKILL.md exit=nil; want error")
	}
	if !errors.Is(err, ErrUnsafePath) {
		t.Errorf("err = %v; want errors.Is(_, ErrUnsafePath)", err)
	}
	if bytes.Contains(buf.Bytes(), []byte("SUPER SECRET TOKEN")) {
		t.Errorf("--diff leaked the symlinked file's contents to stdout: %q", buf.String())
	}
}

// TestInstall_RejectsSymlinkedSkillDir pins the write-side symlink
// defense: a symlinked skill directory would let writeSkill's
// MkdirAll/CreateTemp/Rename land the SKILL.md at an attacker-chosen
// path. Reject it and surface ErrUnsafePath instead.
func TestInstall_RejectsSymlinkedSkillDir(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink semantics differ on windows")
	}
	target := filepath.Join(t.TempDir(), ".claude", "skills")
	if err := os.MkdirAll(target, 0o700); err != nil {
		t.Fatalf("mkdir target: %v", err)
	}
	// The "victim" dir is where an attacker wants writes to land.
	victim := filepath.Join(t.TempDir(), "victim")
	if err := os.MkdirAll(victim, 0o700); err != nil {
		t.Fatalf("mkdir victim: %v", err)
	}
	if err := os.Symlink(victim, filepath.Join(target, "marunage-triage")); err != nil {
		t.Fatalf("symlink: %v", err)
	}

	_, err := Install(InstallOptions{Target: target, Source: EmbeddedFS()})
	if err == nil {
		t.Fatalf("Install over a symlinked skill dir exit=nil; want error")
	}
	if !errors.Is(err, ErrUnsafePath) {
		t.Errorf("err = %v; want errors.Is(_, ErrUnsafePath)", err)
	}
	// And the victim dir must NOT have received a SKILL.md.
	if _, err := os.Stat(filepath.Join(victim, "SKILL.md")); err == nil {
		t.Errorf("write traversed the symlink: SKILL.md landed in victim/")
	}
}

// TestInstall_RejectsSymlinkedTriageOnRead exercises the read path the
// installer hits BEFORE the merge / force / skip decision: even when
// the install would otherwise be a no-op (same content, no flag), a
// symlinked SKILL.md must be reported as unsafe rather than silently
// compared against attacker-controlled content.
func TestInstall_RejectsSymlinkedTriageOnRead(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink semantics differ on windows")
	}
	target := filepath.Join(t.TempDir(), ".claude", "skills")
	if err := os.MkdirAll(filepath.Join(target, "marunage-triage"), 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	secret := filepath.Join(t.TempDir(), "secret.txt")
	if err := os.WriteFile(secret, []byte("attacker controlled"), 0o600); err != nil {
		t.Fatalf("seed secret: %v", err)
	}
	if err := os.Symlink(secret, filepath.Join(target, "marunage-triage", "SKILL.md")); err != nil {
		t.Fatalf("symlink: %v", err)
	}

	_, err := Install(InstallOptions{Target: target, Source: EmbeddedFS()})
	if !errors.Is(err, ErrUnsafePath) {
		t.Errorf("err = %v; want errors.Is(_, ErrUnsafePath)", err)
	}
}

// TestInstall_NoTmpLeftoverOnSuccess pins the tidiness half of the
// tmp+rename pattern: a successful install must not leave `.tmp`
// siblings in the skill directory (they would leak 0600 scratch files
// the user has no way to associate with marunage).
func TestInstall_NoTmpLeftoverOnSuccess(t *testing.T) {
	target := filepath.Join(t.TempDir(), ".claude", "skills")
	if _, err := Install(InstallOptions{Target: target, Source: EmbeddedFS()}); err != nil {
		t.Fatalf("seed Install: %v", err)
	}

	entries, err := os.ReadDir(filepath.Join(target, "marunage-triage"))
	if err != nil {
		t.Fatalf("readdir: %v", err)
	}
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".tmp") {
			t.Errorf("leftover tmp file: %s", e.Name())
		}
	}
}

// TestInstall_AtomicWrite_PreservesPreviousOnWriteFailure pins the
// atomic invariant under real failure injection: if writeSkill cannot
// place the new SKILL.md (here we drop the parent dir to 0500 so
// CreateTemp returns EACCES), the previously-installed SKILL.md must
// remain byte-identical and no `.tmp` scratch file may be left behind.
//
// The previous test (now NoTmpLeftoverOnSuccess) only verified the
// happy path's tidiness; it could not detect a regression where a
// failed mid-write left a half-written or zero-byte file.
func TestInstall_AtomicWrite_PreservesPreviousOnWriteFailure(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("posix permissions not enforced on windows")
	}
	if os.Geteuid() == 0 {
		t.Skip("root bypasses 0500 directory writes; cannot inject failure")
	}
	target := filepath.Join(t.TempDir(), ".claude", "skills")
	// Seed a successful install so we have a "previous" SKILL.md.
	if _, err := Install(InstallOptions{Target: target, Source: EmbeddedFS()}); err != nil {
		t.Fatalf("seed Install: %v", err)
	}
	triageDir := filepath.Join(target, "marunage-triage")
	skillPath := filepath.Join(triageDir, "SKILL.md")

	// Replace the on-disk body with a structurally valid but
	// content-different SKILL.md so --force will actually want to
	// write (otherwise bytes.Equal short-circuits to Skip).
	prev := []byte("<!-- version: 0.1.0 -->\n# previous on-disk\n\n## 判定ロジック\nold\n\n## 出力フォーマット\nold\n")
	if err := os.WriteFile(skillPath, prev, 0o600); err != nil {
		t.Fatalf("seed prev: %v", err)
	}

	// Inject a write failure: drop the skill dir to 0500 so
	// os.CreateTemp inside it returns EACCES.
	if err := os.Chmod(triageDir, 0o500); err != nil {
		t.Fatalf("chmod 0500: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(triageDir, 0o700) })

	_, err := Install(InstallOptions{Target: target, Source: EmbeddedFS(), Force: true})
	if err == nil {
		t.Fatalf("Install --force into 0500 dir exit=nil; want failure")
	}

	// Restore so we can read.
	if err := os.Chmod(triageDir, 0o700); err != nil {
		t.Fatalf("restore chmod: %v", err)
	}
	got, err := os.ReadFile(skillPath)
	if err != nil {
		t.Fatalf("read after failed install: %v", err)
	}
	if !bytes.Equal(got, prev) {
		t.Errorf("previous SKILL.md was mutated by a failed write\nbefore=%q\nafter =%q", prev, got)
	}

	// And no .tmp scratch file may remain.
	entries, err := os.ReadDir(triageDir)
	if err != nil {
		t.Fatalf("readdir: %v", err)
	}
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".tmp") {
			t.Errorf("failed write left a tmp scratch file: %s", e.Name())
		}
	}
}

// names lifts a []SkillReport into the bare list of skill names so test
// assertions stay readable.
func names(rs []SkillReport) []string {
	out := make([]string, len(rs))
	for i, r := range rs {
		out[i] = r.Name
	}
	sort.Strings(out)
	return out
}

func equalSorted(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	x := append([]string(nil), a...)
	y := append([]string(nil), b...)
	sort.Strings(x)
	sort.Strings(y)
	for i := range x {
		if x[i] != y[i] {
			return false
		}
	}
	return true
}
