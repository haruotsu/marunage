package registry

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// makeTarball builds an in-memory gzip-tar from name->body pairs so
// tests can pin the extractor's behaviour without checking in
// fixtures.
func makeTarball(t *testing.T, files map[string]string) []byte {
	t.Helper()
	var raw bytes.Buffer
	gz := gzip.NewWriter(&raw)
	tw := tar.NewWriter(gz)
	for name, body := range files {
		hdr := &tar.Header{
			Name:     name,
			Mode:     0o600,
			Size:     int64(len(body)),
			Typeflag: tar.TypeReg,
		}
		if strings.HasSuffix(name, "/") {
			hdr.Typeflag = tar.TypeDir
			hdr.Size = 0
			body = ""
		}
		if err := tw.WriteHeader(hdr); err != nil {
			t.Fatalf("tar header: %v", err)
		}
		if _, err := tw.Write([]byte(body)); err != nil {
			t.Fatalf("tar write: %v", err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatalf("tar close: %v", err)
	}
	if err := gz.Close(); err != nil {
		t.Fatalf("gz close: %v", err)
	}
	return raw.Bytes()
}

// TestExtractTarball_StripsSkillNamePrefix pins the documented
// publisher convention: the tarball ships a single
// `<skill-name>/SKILL.md` top-level dir, and the extractor flattens
// it under Dest so the on-disk layout is `<Dest>/SKILL.md`.
func TestExtractTarball_StripsSkillNamePrefix(t *testing.T) {
	tarball := makeTarball(t, map[string]string{
		"marunage-source-jira/":         "",
		"marunage-source-jira/SKILL.md": "<!-- version: 1.0.0 -->\n# jira\n",
	})

	dest := filepath.Join(t.TempDir(), "marunage-source-jira")
	if err := ExtractTarball(tarball, ExtractOptions{Dest: dest, SkillName: "marunage-source-jira"}); err != nil {
		t.Fatalf("ExtractTarball: %v", err)
	}

	body, err := os.ReadFile(filepath.Join(dest, "SKILL.md"))
	if err != nil {
		t.Fatalf("read SKILL.md: %v", err)
	}
	if !strings.Contains(string(body), "version: 1.0.0") {
		t.Errorf("SKILL.md body wrong: %q", body)
	}
}

// TestExtractTarball_RejectsTraversal pins the path-escape defence:
// a tarball with `../../etc/passwd` inside must not write outside
// Dest.
func TestExtractTarball_RejectsTraversal(t *testing.T) {
	tarball := makeTarball(t, map[string]string{
		"../escape.txt": "pwned",
	})

	dest := filepath.Join(t.TempDir(), "x")
	err := ExtractTarball(tarball, ExtractOptions{Dest: dest})
	if !errors.Is(err, ErrUnsafeTarPath) {
		t.Errorf("err = %v; want errors.Is(_, ErrUnsafeTarPath)", err)
	}
}

// TestExtractTarball_RejectsAbsolutePath pins that an absolute path
// in a tar header is refused.
func TestExtractTarball_RejectsAbsolutePath(t *testing.T) {
	tarball := makeTarball(t, map[string]string{
		"/etc/passwd": "pwned",
	})

	dest := filepath.Join(t.TempDir(), "x")
	err := ExtractTarball(tarball, ExtractOptions{Dest: dest})
	if !errors.Is(err, ErrUnsafeTarPath) {
		t.Errorf("err = %v; want errors.Is(_, ErrUnsafeTarPath)", err)
	}
}

// TestExtractTarball_RejectsSymlinkEntry pins that a symlink type
// header is refused — symlinks would let a publisher trick the
// installer into writing to an attacker-chosen path.
func TestExtractTarball_RejectsSymlinkEntry(t *testing.T) {
	var raw bytes.Buffer
	gz := gzip.NewWriter(&raw)
	tw := tar.NewWriter(gz)
	if err := tw.WriteHeader(&tar.Header{
		Name:     "link",
		Linkname: "/etc/passwd",
		Mode:     0o777,
		Typeflag: tar.TypeSymlink,
	}); err != nil {
		t.Fatalf("tar header: %v", err)
	}
	if err := tw.Close(); err != nil {
		t.Fatalf("tar close: %v", err)
	}
	if err := gz.Close(); err != nil {
		t.Fatalf("gz close: %v", err)
	}

	dest := filepath.Join(t.TempDir(), "x")
	err := ExtractTarball(raw.Bytes(), ExtractOptions{Dest: dest})
	if !errors.Is(err, ErrUnsafeTarPath) {
		t.Errorf("err = %v; want errors.Is(_, ErrUnsafeTarPath)", err)
	}
}

// TestExtractTarball_ReplacesExistingTree pins the upgrade path: an
// existing skill directory is replaced atomically.
func TestExtractTarball_ReplacesExistingTree(t *testing.T) {
	dest := filepath.Join(t.TempDir(), "marunage-source-jira")
	if err := os.MkdirAll(dest, 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dest, "SKILL.md"), []byte("old"), 0o600); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dest, "stale.txt"), []byte("garbage"), 0o600); err != nil {
		t.Fatalf("seed stale: %v", err)
	}

	tarball := makeTarball(t, map[string]string{
		"marunage-source-jira/SKILL.md": "<!-- version: 2.0.0 -->\n# new\n",
	})
	if err := ExtractTarball(tarball, ExtractOptions{Dest: dest, SkillName: "marunage-source-jira"}); err != nil {
		t.Fatalf("ExtractTarball: %v", err)
	}

	got, err := os.ReadFile(filepath.Join(dest, "SKILL.md"))
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if !strings.Contains(string(got), "2.0.0") {
		t.Errorf("SKILL.md not replaced; got %q", got)
	}
	if _, err := os.Stat(filepath.Join(dest, "stale.txt")); err == nil {
		t.Errorf("stale.txt survived the replacement")
	}
}

// TestExtractTarball_RejectsTooManyFiles pins the inode-exhaustion
// guard: a publisher cannot ship millions of zero-byte files even if
// the cumulative byte count stays under MaxBytes.
func TestExtractTarball_RejectsTooManyFiles(t *testing.T) {
	files := map[string]string{}
	for i := 0; i < 5; i++ {
		files["marunage-x/"+string(rune('a'+i))+".txt"] = "x"
	}
	tarball := makeTarball(t, files)
	dest := filepath.Join(t.TempDir(), "marunage-x")
	err := ExtractTarball(tarball, ExtractOptions{Dest: dest, SkillName: "marunage-x", MaxFiles: 2})
	if !errors.Is(err, ErrTarTooManyFiles) {
		t.Errorf("err = %v; want errors.Is(_, ErrTarTooManyFiles)", err)
	}
	if _, err := os.Stat(dest); err == nil {
		t.Errorf("file-count overrun should leave no destination behind")
	}
}

// TestExtractTarball_AcceptsFilesAtCap pins the boundary: exactly
// MaxFiles regular entries should succeed (the cap is "more than"
// not "at least"). With MaxFiles=2 and two entries we expect
// extraction to succeed.
func TestExtractTarball_AcceptsFilesAtCap(t *testing.T) {
	tarball := makeTarball(t, map[string]string{
		"marunage-x/a.md": "<!-- version: 1.0.0 -->\n",
		"marunage-x/b.md": "extra",
	})
	dest := filepath.Join(t.TempDir(), "marunage-x")
	if err := ExtractTarball(tarball, ExtractOptions{
		Dest: dest, SkillName: "marunage-x", MaxFiles: 2,
	}); err != nil {
		t.Errorf("ExtractTarball at MaxFiles boundary: %v", err)
	}
}

// TestExtractTarball_RejectsOversize pins that a publisher cannot
// force the CLI to write more than MaxBytes uncompressed bytes.
func TestExtractTarball_RejectsOversize(t *testing.T) {
	tarball := makeTarball(t, map[string]string{
		"marunage-x/SKILL.md": strings.Repeat("a", 2048),
	})
	dest := filepath.Join(t.TempDir(), "marunage-x")
	err := ExtractTarball(tarball, ExtractOptions{Dest: dest, SkillName: "marunage-x", MaxBytes: 16})
	if !errors.Is(err, ErrTarTooLarge) {
		t.Errorf("err = %v; want errors.Is(_, ErrTarTooLarge)", err)
	}
	if _, err := os.Stat(dest); err == nil {
		t.Errorf("oversized extract should have left no destination behind")
	}
}

// TestExtractTarball_RefusesSymlinkDest pins the dest-side defence:
// if Dest itself is a symlink, the rename would land at the link
// target — refuse instead.
func TestExtractTarball_RefusesSymlinkDest(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink semantics differ on windows")
	}
	root := t.TempDir()
	victim := filepath.Join(root, "victim")
	if err := os.MkdirAll(victim, 0o700); err != nil {
		t.Fatalf("mkdir victim: %v", err)
	}
	dest := filepath.Join(root, "marunage-x")
	if err := os.Symlink(victim, dest); err != nil {
		t.Fatalf("symlink: %v", err)
	}

	tarball := makeTarball(t, map[string]string{
		"marunage-x/SKILL.md": "<!-- version: 1.0.0 -->\n",
	})
	err := ExtractTarball(tarball, ExtractOptions{Dest: dest, SkillName: "marunage-x"})
	if !errors.Is(err, ErrUnsafeTarPath) {
		t.Errorf("err = %v; want errors.Is(_, ErrUnsafeTarPath)", err)
	}
	if _, err := os.Stat(filepath.Join(victim, "SKILL.md")); err == nil {
		t.Errorf("write traversed the symlink: SKILL.md landed in victim/")
	}
}
