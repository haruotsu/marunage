package markdown

import (
	"os"
	"path/filepath"
	"testing"
)

func TestRenderTaskLine(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		in   taskLine
		want string
	}{
		{
			name: "unchecked plain",
			in:   taskLine{Indent: "", Title: "foo", Done: false},
			want: "- [ ] foo",
		},
		{
			name: "checked plain",
			in:   taskLine{Indent: "", Title: "bar", Done: true},
			want: "- [x] bar",
		},
		{
			name: "with marker id only",
			in:   taskLine{Indent: "", Title: "foo", Marker: marker{Present: true, ID: "abc"}},
			want: "- [ ] foo <!-- marunage:id=abc -->",
		},
		{
			name: "with marker id and source",
			in: taskLine{
				Indent: "",
				Title:  "foo",
				Marker: marker{Present: true, ID: "abc", Source: "markdown"},
			},
			want: "- [ ] foo <!-- marunage:id=abc source=markdown -->",
		},
		{
			name: "preserves indent",
			in:   taskLine{Indent: "  ", Title: "sub", Done: false},
			want: "  - [ ] sub",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := renderTaskLine(tc.in)
			if got != tc.want {
				t.Fatalf("renderTaskLine = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestAtomicWriteFile(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	path := filepath.Join(dir, "f.md")
	want := []byte("hello\n")
	if err := atomicWriteFile(path, want, 0o600); err != nil {
		t.Fatalf("atomicWriteFile: %v", err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if string(got) != string(want) {
		t.Fatalf("content mismatch: got=%q want=%q", got, want)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	// Confirm 0600 actually landed (the tmp file must have been chmod'd
	// before rename so a racing reader cannot see a wider mode).
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Fatalf("perm = %v, want 0600", perm)
	}
}

func TestAtomicWriteFileLeavesNoTmpOnSuccess(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	path := filepath.Join(dir, "f.md")
	if err := atomicWriteFile(path, []byte("x"), 0o600); err != nil {
		t.Fatalf("atomicWriteFile: %v", err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("readdir: %v", err)
	}
	// Exactly one entry (the target). A leftover tmp file would mean
	// the rename half of tmp->rename failed silently.
	if len(entries) != 1 {
		t.Fatalf("want 1 entry, got %d: %v", len(entries), entries)
	}
}
