package journal

import (
	"strings"
	"testing"
	"time"
)

func TestFormatHeader(t *testing.T) {
	t.Parallel()
	e := Entry{At: time.Date(2026, 5, 4, 14, 30, 0, 0, time.UTC)}
	got := Format(e)
	if !strings.Contains(got, "## 2026-05-04 14:30") {
		t.Errorf("Format missing header, got:\n%s", got)
	}
}

func TestFormatSectionsRendered(t *testing.T) {
	t.Parallel()
	e := Entry{
		At: time.Date(2026, 5, 4, 14, 30, 0, 0, time.UTC),
		Sections: []Section{
			{Title: "Completed Tasks", Items: []Item{{Text: "#42 Fix bug"}, {Text: "#43 Review PR"}}},
			{Title: "Git Activity", Items: []Item{{Text: "feat: add thing"}}},
		},
	}
	got := Format(e)
	for _, want := range []string{
		"### Completed Tasks",
		"- #42 Fix bug",
		"- #43 Review PR",
		"### Git Activity",
		"- feat: add thing",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("Format missing %q, got:\n%s", want, got)
		}
	}
}

func TestFormatEmptySectionsOmitted(t *testing.T) {
	t.Parallel()
	e := Entry{
		At: time.Date(2026, 5, 4, 14, 30, 0, 0, time.UTC),
		Sections: []Section{
			{Title: "Empty", Items: nil},
		},
	}
	got := Format(e)
	if strings.Contains(got, "### Empty") {
		t.Errorf("empty section should be omitted, got:\n%s", got)
	}
}

func TestFormatTrailingNewline(t *testing.T) {
	t.Parallel()
	e := Entry{At: time.Date(2026, 5, 4, 14, 30, 0, 0, time.UTC)}
	got := Format(e)
	if !strings.HasSuffix(got, "\n") {
		t.Errorf("Format result should end with newline")
	}
}
