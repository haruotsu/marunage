package markdown

import (
	"errors"
	"reflect"
	"testing"
)

func TestParse(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		in   string
		want []parsedTask
	}{
		{
			name: "single unchecked",
			in:   "- [ ] foo\n",
			want: []parsedTask{
				{Title: "foo", Done: false, LineNumber: 1, SourcePath: "f.md"},
			},
		},
		{
			name: "single checked, lowercase x",
			in:   "- [x] bar\n",
			want: []parsedTask{
				{Title: "bar", Done: true, LineNumber: 1, SourcePath: "f.md"},
			},
		},
		{
			name: "single checked, uppercase X",
			in:   "- [X] bar\n",
			want: []parsedTask{
				{Title: "bar", Done: true, LineNumber: 1, SourcePath: "f.md"},
			},
		},
		{
			name: "multiple lines preserve order",
			in:   "- [ ] a\n- [x] b\n- [ ] c\n",
			want: []parsedTask{
				{Title: "a", Done: false, LineNumber: 1, SourcePath: "f.md"},
				{Title: "b", Done: true, LineNumber: 2, SourcePath: "f.md"},
				{Title: "c", Done: false, LineNumber: 3, SourcePath: "f.md"},
			},
		},
		{
			name: "non-checklist lines ignored",
			in:   "# heading\n\n- [ ] a\nrandom prose\n- [x] b\n",
			want: []parsedTask{
				{Title: "a", Done: false, LineNumber: 3, SourcePath: "f.md"},
				{Title: "b", Done: true, LineNumber: 5, SourcePath: "f.md"},
			},
		},
		{
			name: "marker comment is parsed and stripped from title",
			in:   "- [ ] foo <!-- marunage:id=abc123 source=markdown -->\n",
			want: []parsedTask{
				{
					Title:      "foo",
					Done:       false,
					LineNumber: 1,
					SourcePath: "f.md",
					Marker: marker{
						Present: true,
						ID:      "abc123",
						Source:  "markdown",
						Extra:   map[string]string{},
					},
				},
			},
		},
		{
			name: "non-marker html comment is left in title",
			in:   "- [ ] foo <!-- regular comment -->\n",
			want: []parsedTask{
				{
					Title:      "foo <!-- regular comment -->",
					Done:       false,
					LineNumber: 1,
					SourcePath: "f.md",
				},
			},
		},
		{
			name: "marker with external_id alias",
			in:   "- [x] done <!-- marunage:id=zz external_id=upstream-1 -->\n",
			want: []parsedTask{
				{
					Title:      "done",
					Done:       true,
					LineNumber: 1,
					SourcePath: "f.md",
					Marker: marker{
						Present:    true,
						ID:         "zz",
						ExternalID: "upstream-1",
						Extra:      map[string]string{},
					},
				},
			},
		},
		{
			name: "indented checklist line",
			in:   "  - [ ] sub task\n",
			want: []parsedTask{
				{Title: "sub task", Done: false, LineNumber: 1, SourcePath: "f.md"},
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, err := parse("f.md", []byte(tc.in))
			if err != nil {
				t.Fatalf("parse: %v", err)
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("parse mismatch:\n got=%+v\nwant=%+v", got, tc.want)
			}
		})
	}
}

func TestParseInvalidMarker(t *testing.T) {
	t.Parallel()

	// Token with no '=' violates the documented key=value shape and
	// should fail loudly rather than be silently dropped.
	_, err := parse("f.md", []byte("- [ ] foo <!-- marunage:bogus -->\n"))
	if !errors.Is(err, ErrInvalidMarker) {
		t.Fatalf("want ErrInvalidMarker, got %v", err)
	}
}
