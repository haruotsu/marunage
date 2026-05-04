package journal

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestWriterCreatesFileOnFirstAppend(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	w := NewWriter(dir)

	e := Entry{
		At: time.Date(2026, 5, 4, 14, 30, 0, 0, time.UTC),
		Sections: []Section{
			{Title: "Git Activity", Items: []Item{{Text: "feat: add thing"}}},
		},
	}
	if err := w.Append(e); err != nil {
		t.Fatalf("Append: %v", err)
	}

	data, err := os.ReadFile(filepath.Join(dir, "2026-05-04.md"))
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	content := string(data)
	if !strings.Contains(content, "## 2026-05-04 14:30") {
		t.Errorf("missing header in file, got:\n%s", content)
	}
	if !strings.Contains(content, "- feat: add thing") {
		t.Errorf("missing item in file, got:\n%s", content)
	}
}

func TestWriterAppendsToExistingFile(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	w := NewWriter(dir)

	e1 := Entry{At: time.Date(2026, 5, 4, 14, 0, 0, 0, time.UTC)}
	e2 := Entry{At: time.Date(2026, 5, 4, 14, 30, 0, 0, time.UTC)}

	if err := w.Append(e1); err != nil {
		t.Fatalf("Append e1: %v", err)
	}
	if err := w.Append(e2); err != nil {
		t.Fatalf("Append e2: %v", err)
	}

	data, err := os.ReadFile(filepath.Join(dir, "2026-05-04.md"))
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	content := string(data)
	if !strings.Contains(content, "## 2026-05-04 14:00") {
		t.Errorf("missing first entry in file")
	}
	if !strings.Contains(content, "## 2026-05-04 14:30") {
		t.Errorf("missing second entry in file")
	}
	// Both entries must be present (not overwritten).
	first := strings.Index(content, "## 2026-05-04 14:00")
	second := strings.Index(content, "## 2026-05-04 14:30")
	if first < 0 || second < 0 || first >= second {
		t.Errorf("entries out of order or missing: first=%d second=%d", first, second)
	}
}

func TestWriterUsesDateFromEntry(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	w := NewWriter(dir)

	e := Entry{At: time.Date(2026, 1, 15, 9, 0, 0, 0, time.UTC)}
	if err := w.Append(e); err != nil {
		t.Fatalf("Append: %v", err)
	}

	if _, err := os.Stat(filepath.Join(dir, "2026-01-15.md")); err != nil {
		t.Errorf("expected file 2026-01-15.md, got err: %v", err)
	}
}

func TestWriterCheckpointMissingReturnsError(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	w := NewWriter(dir)

	_, err := w.LastCheckpoint()
	if err == nil {
		t.Fatal("expected error for missing checkpoint, got nil")
	}
	if !errors.Is(err, ErrNoCheckpoint) {
		t.Errorf("error = %v, want ErrNoCheckpoint", err)
	}
}

func TestWriterCheckpointRoundTrip(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	w := NewWriter(dir)

	ts := time.Date(2026, 5, 4, 14, 30, 0, 0, time.UTC)
	if err := w.UpdateCheckpoint(ts); err != nil {
		t.Fatalf("UpdateCheckpoint: %v", err)
	}

	got, err := w.LastCheckpoint()
	if err != nil {
		t.Fatalf("LastCheckpoint: %v", err)
	}
	if !got.Equal(ts) {
		t.Errorf("LastCheckpoint() = %v, want %v", got, ts)
	}
}

func TestWriterCheckpointOverwrite(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	w := NewWriter(dir)

	t1 := time.Date(2026, 5, 4, 14, 0, 0, 0, time.UTC)
	t2 := time.Date(2026, 5, 4, 14, 30, 0, 0, time.UTC)

	if err := w.UpdateCheckpoint(t1); err != nil {
		t.Fatalf("UpdateCheckpoint t1: %v", err)
	}
	if err := w.UpdateCheckpoint(t2); err != nil {
		t.Fatalf("UpdateCheckpoint t2: %v", err)
	}

	got, err := w.LastCheckpoint()
	if err != nil {
		t.Fatalf("LastCheckpoint: %v", err)
	}
	if !got.Equal(t2) {
		t.Errorf("LastCheckpoint() = %v, want %v", got, t2)
	}
}

func TestWriterCheckpointNoTmpFileLeft(t *testing.T) {
	// After UpdateCheckpoint, no .checkpoint.tmp file should remain.
	t.Parallel()
	dir := t.TempDir()
	w := NewWriter(dir)

	ts := time.Date(2026, 5, 4, 14, 30, 0, 0, time.UTC)
	if err := w.UpdateCheckpoint(ts); err != nil {
		t.Fatalf("UpdateCheckpoint: %v", err)
	}

	tmpPath := filepath.Join(dir, ".checkpoint.tmp")
	if _, err := os.Stat(tmpPath); !os.IsNotExist(err) {
		t.Errorf(".checkpoint.tmp should not exist after UpdateCheckpoint, stat err: %v", err)
	}
}
