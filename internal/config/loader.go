package config

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"time"

	"github.com/pelletier/go-toml/v2"

	"github.com/haruotsu/marunage/internal/fsutil"
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
		prev, err := os.ReadFile(path)
		if err != nil {
			return fmt.Errorf("read existing config for backup: %w", err)
		}
		if _, err := WriteBackup(path, prev); err != nil {
			return err
		}
	}

	// Atomic write: tmp file in the same directory, then rename. This keeps
	// readers from seeing a partial file even if the process is killed mid-
	// write. config.toml is 0o600 since it may hold secret references.
	if err := fsutil.AtomicWrite(path, body, 0o600); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}

	auditor.Record(AuditEvent{Action: "config.save", Path: path})
	return nil
}

// BackupPath returns the timestamped sidecar backup path for a config file,
// e.g. "<path>.bak.20060102T150405Z". Save and `config edit` both snapshot the
// pre-write file there so a bad change stays recoverable.
func BackupPath(path string, t time.Time) string {
	return fmt.Sprintf("%s.bak.%s", path, t.UTC().Format("20060102T150405Z"))
}

// WriteBackup snapshots content to BackupPath(path, now) with 0o600 and returns
// the backup path written. The 0o600 mode matches config.toml's own, since a
// backup carries the same (possibly secret-referencing) contents.
func WriteBackup(path string, content []byte) (string, error) {
	backup := BackupPath(path, time.Now())
	if err := os.WriteFile(backup, content, 0o600); err != nil {
		return "", fmt.Errorf("write backup %s: %w", backup, err)
	}
	return backup, nil
}
