// Package fsutil holds small filesystem helpers shared across packages.
package fsutil

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
)

// AtomicWrite writes body to path via a sibling tmp file + rename, so readers
// never observe a partial file even if the process is killed mid-write, and
// chmods the result to mode before the rename publishes it. The parent
// directory must already exist; AtomicWrite does not create it (callers that
// need that should MkdirAll first), so a missing directory surfaces as an
// error rather than a silent mkdir with an unexpected mode.
//
// This is the single atomic-write implementation; package-local helpers
// (internal/source/markdown, internal/cli) and config.Save delegate here.
func AtomicWrite(path string, body []byte, mode fs.FileMode) error {
	dir := filepath.Dir(path)
	base := filepath.Base(path)
	tmp, err := os.CreateTemp(dir, base+".tmp.*")
	if err != nil {
		return fmt.Errorf("create tmp: %w", err)
	}
	tmpName := tmp.Name()
	cleanup := func() { _ = os.Remove(tmpName) }
	if _, err := tmp.Write(body); err != nil {
		_ = tmp.Close()
		cleanup()
		return fmt.Errorf("write tmp: %w", err)
	}
	if err := tmp.Chmod(mode); err != nil {
		_ = tmp.Close()
		cleanup()
		return fmt.Errorf("chmod tmp: %w", err)
	}
	if err := tmp.Close(); err != nil {
		cleanup()
		return fmt.Errorf("close tmp: %w", err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		cleanup()
		return fmt.Errorf("rename tmp: %w", err)
	}
	return nil
}
