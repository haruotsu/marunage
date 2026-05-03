package logging

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"
)

// RotatingFile is an io.Writer that swaps the underlying file once its size
// would exceed MaxBytes on the next write. Rotated files are renamed to
// <path>.<UTC timestamp> next to the active file, and PruneBackups keeps at
// most MaxBackups of them. The implementation is intentionally tiny — a
// daemon log writer that depends on a third-party rotation library would
// pull a transitive supply chain for one screen of code.
type RotatingFile struct {
	mu         sync.Mutex
	path       string
	maxBytes   int64
	maxBackups int
	f          *os.File
	size       int64
}

// NewRotatingFile opens or creates path in append mode and tracks its
// current size so the next Write can decide whether to rotate first.
func NewRotatingFile(path string, maxBytes int64, maxBackups int) (*RotatingFile, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, fmt.Errorf("mkdir %s: %w", filepath.Dir(path), err)
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", path, err)
	}
	info, err := f.Stat()
	if err != nil {
		_ = f.Close()
		return nil, fmt.Errorf("stat %s: %w", path, err)
	}
	return &RotatingFile{
		path:       path,
		maxBytes:   maxBytes,
		maxBackups: maxBackups,
		f:          f,
		size:       info.Size(),
	}, nil
}

// Write rotates first if the incoming payload would push the file past
// MaxBytes, then forwards the bytes to the active file. The "rotate before
// write" order keeps the on-disk file at-or-below the configured limit at
// every observable moment.
func (r *RotatingFile) Write(p []byte) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.f == nil {
		return 0, fmt.Errorf("rotating file %s is closed", r.path)
	}
	if r.size > 0 && r.size+int64(len(p)) > r.maxBytes {
		if err := r.rotate(); err != nil {
			return 0, err
		}
	}
	n, err := r.f.Write(p)
	r.size += int64(n)
	return n, err
}

// Close flushes and closes the active file. After Close, Write returns an
// error so accidental late writes surface instead of silently disappearing.
func (r *RotatingFile) Close() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.f == nil {
		return nil
	}
	err := r.f.Close()
	r.f = nil
	return err
}

func (r *RotatingFile) rotate() error {
	if err := r.f.Close(); err != nil {
		return fmt.Errorf("close before rotate: %w", err)
	}
	r.f = nil
	r.size = 0

	ts := time.Now().UTC().Format("20060102T150405.000000000Z")
	rotated := fmt.Sprintf("%s.%s", r.path, ts)
	if err := os.Rename(r.path, rotated); err != nil {
		return fmt.Errorf("rename %s -> %s: %w", r.path, rotated, err)
	}

	f, err := os.OpenFile(r.path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return fmt.Errorf("reopen %s: %w", r.path, err)
	}
	r.f = f
	return r.pruneBackupsLocked()
}

func (r *RotatingFile) pruneBackupsLocked() error {
	if r.maxBackups <= 0 {
		return nil
	}
	matches, err := filepath.Glob(r.path + ".*")
	if err != nil {
		return fmt.Errorf("glob backups: %w", err)
	}
	if len(matches) <= r.maxBackups {
		return nil
	}
	// Lex order matches chronological order because the timestamp is fixed-width.
	sort.Strings(matches)
	for _, old := range matches[:len(matches)-r.maxBackups] {
		if err := os.Remove(old); err != nil {
			return fmt.Errorf("remove %s: %w", old, err)
		}
	}
	return nil
}
