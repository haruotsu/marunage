package registry

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"
)

// MaxTarBytes caps the total uncompressed bytes ExtractTarball will
// write. Together with FetchTarball's MaxBodyBytes it bounds the
// resource a malicious publisher can force the CLI to spend.
const MaxTarBytes = int64(64 << 20) // 64 MiB

// ErrUnsafeTarPath is returned when a tar entry's name escapes the
// destination root (`..`, absolute paths, symlinks). The typed
// sentinel keeps the install path's "abort and clean up" branch
// explicit.
var ErrUnsafeTarPath = errors.New("registry: unsafe tar entry path")

// ErrTarTooLarge is returned when the cumulative uncompressed bytes
// in the tarball exceed MaxTarBytes (or the per-call override).
var ErrTarTooLarge = errors.New("registry: tar exceeds size cap")

// ExtractOptions controls how ExtractTarball lays the skill out on
// disk. The zero value installs into Dest with the default cap.
type ExtractOptions struct {
	// Dest is the absolute path to the skill directory we are about
	// to materialise — e.g. `<HOME>/.claude/skills/<name>`. The tar
	// is allowed to ship a single top-level directory whose entries
	// are flattened under Dest, OR ship paths directly. Either way,
	// every entry must resolve under Dest.
	Dest string

	// SkillName is the canonical name we expect at the top level of
	// the tarball. When non-empty, ExtractTarball strips the
	// `<SkillName>/` prefix from each entry so the on-disk layout
	// matches `~/.claude/skills/<SkillName>/<file>` regardless of
	// whether the publisher tarred the parent directory or not.
	SkillName string

	// MaxBytes overrides MaxTarBytes; zero defaults to the package
	// constant.
	MaxBytes int64
}

// ExtractTarball decompresses body (gzip-wrapped tar), validates each
// entry's path, and writes regular files under opts.Dest. It refuses
// symlinks, hardlinks, devices, and any path that would escape Dest.
//
// On any error the partially-written tree is cleaned up so the caller
// is not left with a half-installed skill.
func ExtractTarball(body []byte, opts ExtractOptions) error {
	if strings.TrimSpace(opts.Dest) == "" {
		return fmt.Errorf("registry: ExtractTarball: Dest is empty")
	}
	maxBytes := opts.MaxBytes
	if maxBytes <= 0 {
		maxBytes = MaxTarBytes
	}

	gz, err := gzip.NewReader(bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("registry: gzip reader: %w", err)
	}
	defer func() { _ = gz.Close() }()

	dest, err := filepath.Abs(opts.Dest)
	if err != nil {
		return fmt.Errorf("registry: abs %s: %w", opts.Dest, err)
	}

	// We materialise everything into a sibling tmp dir first so a
	// failure mid-extract doesn't leave a half-installed tree under
	// dest. On success, we move tmp into place (replacing whatever
	// dest contained).
	tmpRoot, err := os.MkdirTemp(filepath.Dir(dest), ".skill-extract-*")
	if err != nil {
		return fmt.Errorf("registry: mktemp: %w", err)
	}
	cleanup := func() { _ = os.RemoveAll(tmpRoot) }

	tr := tar.NewReader(gz)
	var written int64
	for {
		h, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			cleanup()
			return fmt.Errorf("registry: read tar header: %w", err)
		}

		rel, err := safeTarPath(h.Name, opts.SkillName)
		if err != nil {
			cleanup()
			return err
		}
		// A header that resolves to "" after stripping the skill
		// name (e.g. the bare top-level directory) is a no-op.
		if rel == "" {
			continue
		}

		switch h.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(filepath.Join(tmpRoot, rel), 0o700); err != nil {
				cleanup()
				return fmt.Errorf("registry: mkdir %s: %w", rel, err)
			}
		case tar.TypeReg:
			full := filepath.Join(tmpRoot, rel)
			if err := os.MkdirAll(filepath.Dir(full), 0o700); err != nil {
				cleanup()
				return fmt.Errorf("registry: mkdir parent %s: %w", rel, err)
			}
			n, err := writeRegular(full, tr, maxBytes-written)
			if err != nil {
				cleanup()
				return err
			}
			written += n
		default:
			cleanup()
			return fmt.Errorf("%w: type %c at %s", ErrUnsafeTarPath, h.Typeflag, h.Name)
		}
	}

	if err := replaceTree(tmpRoot, dest); err != nil {
		cleanup()
		return err
	}
	return nil
}

// safeTarPath turns a raw tar entry name into a slash-separated
// relative path under the destination, applying the SkillName prefix
// strip and rejecting any traversal attempt.
func safeTarPath(raw, skillName string) (string, error) {
	if raw == "" {
		return "", nil
	}
	clean := path.Clean(strings.TrimPrefix(raw, "./"))
	if clean == "." {
		return "", nil
	}
	if path.IsAbs(clean) || strings.HasPrefix(clean, "../") || clean == ".." {
		return "", fmt.Errorf("%w: %s", ErrUnsafeTarPath, raw)
	}
	if skillName != "" {
		prefix := skillName + "/"
		switch {
		case clean == skillName:
			return "", nil
		case strings.HasPrefix(clean, prefix):
			clean = strings.TrimPrefix(clean, prefix)
		}
	}
	if strings.Contains(clean, "..") {
		// Belt-and-braces: even after Clean a literal `..` segment
		// must never survive the strip-and-rejoin.
		for _, seg := range strings.Split(clean, "/") {
			if seg == ".." {
				return "", fmt.Errorf("%w: %s", ErrUnsafeTarPath, raw)
			}
		}
	}
	return clean, nil
}

func writeRegular(path string, src io.Reader, remaining int64) (int64, error) {
	if remaining <= 0 {
		return 0, ErrTarTooLarge
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_EXCL, 0o600)
	if err != nil {
		return 0, fmt.Errorf("registry: create %s: %w", path, err)
	}
	defer func() { _ = f.Close() }()

	limited := io.LimitReader(src, remaining+1)
	n, err := io.Copy(f, limited)
	if err != nil {
		return n, fmt.Errorf("registry: write %s: %w", path, err)
	}
	if n > remaining {
		return n, ErrTarTooLarge
	}
	return n, nil
}

// replaceTree moves src to dst, overwriting any existing dst tree.
// We RemoveAll(dst) before Rename so the operation works even when
// dst already contains a previous install — the embedded skill
// installer guarantees Dest's parent exists, but Dest itself may
// already hold an older copy.
func replaceTree(src, dst string) error {
	if err := os.MkdirAll(filepath.Dir(dst), 0o700); err != nil {
		return fmt.Errorf("registry: mkdir parent %s: %w", dst, err)
	}
	// If a non-symlink dst exists, drop it; we have already
	// validated above that we are not crossing a symlink boundary
	// because the caller resolves Dest with filepath.Abs.
	if info, err := os.Lstat(dst); err == nil {
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("%w: dest %s is a symlink", ErrUnsafeTarPath, dst)
		}
		if err := os.RemoveAll(dst); err != nil {
			return fmt.Errorf("registry: remove existing %s: %w", dst, err)
		}
	} else if !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("registry: lstat %s: %w", dst, err)
	}
	if err := os.Rename(src, dst); err != nil {
		return fmt.Errorf("registry: rename %s -> %s: %w", src, dst, err)
	}
	if err := os.Chmod(dst, 0o700); err != nil {
		return fmt.Errorf("registry: chmod %s: %w", dst, err)
	}
	return nil
}
