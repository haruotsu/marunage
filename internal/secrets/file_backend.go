package secrets

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/haruotsu/marunage/internal/fsutil"
)

// fileBackend persists each secret to ~/.marunage/secrets/<name>.json with
// 0700 dir + 0600 file. It is the always-available fallback when no
// keyring / pass / age backend is usable.
//
// Why JSON-per-file rather than one combined secrets.json: each source
// plugin (gmail, slack, github...) can be authenticated independently,
// and per-file granularity keeps a half-completed setup step from
// corrupting unrelated tokens. The shape is intentionally minimal so a
// future PR that adds rotation metadata only has to add fields.
type fileBackend struct {
	dir string // <home>/.marunage/secrets
}

// secretFile is the on-disk shape. Pinning a struct (rather than just
// writing the raw string) leaves room for "createdAt" / "rotatedAt" /
// "scopes" without breaking the file format the next time we touch it.
type secretFile struct {
	Value string `json:"value"`
}

func newFileBackend(cfg Config) (Store, error) {
	home := cfg.HomeDir
	if home == "" {
		h, err := os.UserHomeDir()
		if err != nil {
			return nil, fmt.Errorf("resolve home dir: %w", err)
		}
		home = h
	}
	// Tighten ~/.marunage itself before descending into secrets/. A
	// pre-existing ~/.marunage at 0755 (created by `marunage init` with a
	// wide umask, for example) would otherwise leave the umbrella dir
	// world-readable even though tasks.db and secrets/<name>.json are
	// individually 0600. Mirrors the same parent-tighten pattern that
	// internal/store/store.go applies.
	marunageDir := filepath.Join(home, ".marunage")
	if err := os.MkdirAll(marunageDir, 0o700); err != nil {
		return nil, fmt.Errorf("mkdir %s: %w", marunageDir, err)
	}
	if err := os.Chmod(marunageDir, 0o700); err != nil {
		return nil, fmt.Errorf("chmod %s: %w", marunageDir, err)
	}
	dir := filepath.Join(marunageDir, "secrets")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("mkdir %s: %w", dir, err)
	}
	// MkdirAll does not narrow an existing directory's mode, so retighten
	// in case a previous run (or the user's umask) left it world-readable.
	// Mirrors the same pattern used in internal/store/store.go.
	if err := os.Chmod(dir, 0o700); err != nil {
		return nil, fmt.Errorf("chmod %s: %w", dir, err)
	}
	return &fileBackend{dir: dir}, nil
}

func (f *fileBackend) Backend() string { return "file" }

func (f *fileBackend) path(name string) string {
	return filepath.Join(f.dir, name+".json")
}

func (f *fileBackend) Get(name string) (string, bool, error) {
	if err := validateName(name); err != nil {
		return "", false, err
	}
	body, err := os.ReadFile(f.path(name))
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return "", false, nil
		}
		return "", false, fmt.Errorf("read secret %q: %w", name, err)
	}
	var s secretFile
	if err := json.Unmarshal(body, &s); err != nil {
		return "", false, fmt.Errorf("parse secret %q: %w", name, err)
	}
	return s.Value, true, nil
}

func (f *fileBackend) Set(name, value string) error {
	if err := validateName(name); err != nil {
		return err
	}
	body, err := json.Marshal(secretFile{Value: value})
	if err != nil {
		return fmt.Errorf("marshal secret %q: %w", name, err)
	}
	// Atomic-write via the shared helper: tmp file in the same directory,
	// chmod 0600 before rename so a reader that races us never sees a >0600
	// file.
	if err := fsutil.AtomicWrite(f.path(name), body, 0o600); err != nil {
		return fmt.Errorf("write secret %q: %w", name, err)
	}
	return nil
}

func (f *fileBackend) Delete(name string) error {
	if err := validateName(name); err != nil {
		return err
	}
	if err := os.Remove(f.path(name)); err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		return fmt.Errorf("delete secret %q: %w", name, err)
	}
	return nil
}

func (f *fileBackend) List() ([]string, error) {
	entries, err := os.ReadDir(f.dir)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("list %s: %w", f.dir, err)
	}
	var out []string
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if !strings.HasSuffix(name, ".json") {
			continue
		}
		out = append(out, strings.TrimSuffix(name, ".json"))
	}
	sort.Strings(out)
	return out, nil
}

// validateName guards against names that would escape the secrets dir
// (path separators, "..") or pollute it (empty, dot-prefixed). Source
// plugins use short identifiers like "gmail" / "slack" so this is a safe
// filter; loosening it would invite directory-traversal bugs.
func validateName(name string) error {
	if name == "" {
		return errors.New("secret name must not be empty")
	}
	if strings.ContainsAny(name, "/\\") {
		return fmt.Errorf("secret name %q must not contain path separators", name)
	}
	if strings.HasPrefix(name, ".") {
		return fmt.Errorf("secret name %q must not start with a dot", name)
	}
	if name == "." || name == ".." {
		return fmt.Errorf("secret name %q is reserved", name)
	}
	return nil
}
