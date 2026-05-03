package config

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"time"

	"github.com/pelletier/go-toml/v2"
)

// Load reads path and returns the parsed Config. A missing file yields the
// documented defaults so first-run callers (e.g. `marunage list` before
// `marunage init`) still get a usable Config rather than an error.
//
// Schema validation runs on the parsed result so a hand-edited file with
// invalid values fails loudly here, not deep inside a downstream package.
func Load(path string) (Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return Default(), nil
		}
		return Config{}, fmt.Errorf("read %s: %w", path, err)
	}

	c := Default()
	if err := toml.Unmarshal(data, &c); err != nil {
		return Config{}, fmt.Errorf("parse %s: %w", path, err)
	}
	if err := c.Validate(); err != nil {
		return Config{}, fmt.Errorf("validate %s: %w", path, err)
	}
	return c, nil
}

// Save validates c, snapshots the existing file to config.toml.bak.<ts>, and
// writes the new TOML atomically. If validation fails we never touch the
// file. A nil Auditor is treated as NopAuditor so callers in early PRs can
// pass nil without ceremony.
func Save(path string, c Config, auditor Auditor) error {
	if err := c.Validate(); err != nil {
		return fmt.Errorf("validate: %w", err)
	}
	if auditor == nil {
		auditor = NopAuditor{}
	}

	body, err := toml.Marshal(c)
	if err != nil {
		return fmt.Errorf("marshal: %w", err)
	}

	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("mkdir: %w", err)
	}

	// Snapshot the prior file before touching it; if anything below fails we
	// still have a recoverable copy on disk.
	if _, err := os.Stat(path); err == nil {
		ts := time.Now().UTC().Format("20060102T150405Z")
		backup := fmt.Sprintf("%s.bak.%s", path, ts)
		prev, err := os.ReadFile(path)
		if err != nil {
			return fmt.Errorf("read existing config for backup: %w", err)
		}
		if err := os.WriteFile(backup, prev, 0o600); err != nil {
			return fmt.Errorf("write backup %s: %w", backup, err)
		}
	}

	// Atomic write: tmp file in the same directory, then rename. This keeps
	// readers from seeing a partial file even if the process is killed mid-
	// write.
	tmp, err := os.CreateTemp(filepath.Dir(path), filepath.Base(path)+".tmp.*")
	if err != nil {
		return fmt.Errorf("create tmp: %w", err)
	}
	tmpName := tmp.Name()
	cleanupTmp := func() { _ = os.Remove(tmpName) }
	if _, err := tmp.Write(body); err != nil {
		_ = tmp.Close()
		cleanupTmp()
		return fmt.Errorf("write tmp: %w", err)
	}
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		cleanupTmp()
		return fmt.Errorf("chmod tmp: %w", err)
	}
	if err := tmp.Close(); err != nil {
		cleanupTmp()
		return fmt.Errorf("close tmp: %w", err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		cleanupTmp()
		return fmt.Errorf("rename %s -> %s: %w", tmpName, path, err)
	}

	auditor.Record(AuditEvent{Action: "config.save", Path: path})
	return nil
}
