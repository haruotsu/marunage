package journal

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// ErrNoCheckpoint is returned by LastCheckpoint when no checkpoint file exists yet.
var ErrNoCheckpoint = errors.New("journal: no checkpoint")

// Writer handles appending journal entries to date-partitioned files and
// maintaining a checkpoint that records the last successful write time.
type Writer struct {
	dir string
}

// NewWriter returns a Writer that stores journal files under dir.
func NewWriter(dir string) *Writer { return &Writer{dir: dir} }

// Dir returns the journal directory.
func (w *Writer) Dir() string { return w.dir }

// Append formats entry and appends it to dir/YYYY-MM-DD.md, creating the
// file (and directory) if necessary.
func (w *Writer) Append(e Entry) error {
	if err := os.MkdirAll(w.dir, 0o700); err != nil {
		return fmt.Errorf("mkdir journal dir: %w", err)
	}
	date := e.At.UTC().Format("2006-01-02")
	path := filepath.Join(w.dir, date+".md")
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return fmt.Errorf("open journal file: %w", err)
	}
	defer func() { _ = f.Close() }()
	if _, err = fmt.Fprint(f, Format(e)); err != nil {
		return fmt.Errorf("write journal: %w", err)
	}
	return nil
}

func (w *Writer) checkpointPath() string {
	return filepath.Join(w.dir, ".checkpoint")
}

// LastCheckpoint reads the timestamp of the last successful Append.
// Returns ErrNoCheckpoint when no checkpoint file exists yet.
func (w *Writer) LastCheckpoint() (time.Time, error) {
	data, err := os.ReadFile(w.checkpointPath())
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return time.Time{}, ErrNoCheckpoint
		}
		return time.Time{}, fmt.Errorf("read checkpoint: %w", err)
	}
	t, err := time.Parse(time.RFC3339Nano, strings.TrimSpace(string(data)))
	if err != nil {
		return time.Time{}, fmt.Errorf("parse checkpoint: %w", err)
	}
	return t, nil
}

// UpdateCheckpoint atomically writes t as the new checkpoint using a
// tmp-file + rename pattern so a mid-write crash cannot corrupt the file.
// The directory must already exist (Append creates it).
func (w *Writer) UpdateCheckpoint(t time.Time) error {
	tmp := w.checkpointPath() + ".tmp"
	if err := os.WriteFile(tmp, []byte(t.UTC().Format(time.RFC3339Nano)+"\n"), 0o600); err != nil {
		return fmt.Errorf("write checkpoint tmp: %w", err)
	}
	if err := os.Rename(tmp, w.checkpointPath()); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("rename checkpoint: %w", err)
	}
	return nil
}
