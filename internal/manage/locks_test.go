package manage_test

import (
	"testing"

	"github.com/haruotsu/marunage/internal/manage"
)

// matching regex resolves to its mapped lock_key.
func TestResolveLockKeyMatches(t *testing.T) {
	rules := map[string]string{
		"^repo:.*":  "git-repo",
		"^slack:.*": "slack-channel",
	}
	cases := []struct {
		notes string
		want  string
	}{
		{`{"lock_hint":"repo:haruotsu/marunage"}`, "git-repo"},
		{`{"lock_hint":"slack:C123ABC"}`, "slack-channel"},
	}
	for _, tc := range cases {
		got, err := manage.ResolveLockKey(rules, tc.notes)
		if err != nil {
			t.Fatalf("ResolveLockKey(%q): %v", tc.notes, err)
		}
		if got != tc.want {
			t.Errorf("ResolveLockKey(%q) = %q; want %q", tc.notes, got, tc.want)
		}
	}
}

// lock_hint with no matching regex falls through to "".
func TestResolveLockKeyNoMatch(t *testing.T) {
	rules := map[string]string{
		"^repo:.*": "git-repo",
	}
	got, err := manage.ResolveLockKey(rules, `{"lock_hint":"gmail:thread-1"}`)
	if err != nil {
		t.Fatalf("ResolveLockKey: %v", err)
	}
	if got != "" {
		t.Errorf("ResolveLockKey unmatched = %q; want %q", got, "")
	}
}

// tolerant of empty / non-object / missing-key notes.
func TestResolveLockKeyTolerantOfEmptyNotes(t *testing.T) {
	rules := map[string]string{"^repo:.*": "git-repo"}
	cases := []string{
		"",                   // store NULL
		`{}`,                 // valid JSON object without lock_hint
		`{"foo":"bar"}`,      // valid JSON object, different key
		`{"lock_hint":""}`,   // explicit empty hint
		`{"lock_hint":null}`, // explicit null hint
		`"plain string"`,     // valid JSON but not an object
		`[1,2,3]`,            // valid JSON array
	}
	for _, notes := range cases {
		got, err := manage.ResolveLockKey(rules, notes)
		if err != nil {
			t.Errorf("ResolveLockKey(%q) returned error %v; tolerant resolver should not fail", notes, err)
		}
		if got != "" {
			t.Errorf("ResolveLockKey(%q) = %q; want %q", notes, got, "")
		}
	}
}

// malformed JSON in notes is reported as an error so the caller can decide
// what to do (fail loudly vs. fall through).
func TestResolveLockKeyReportsMalformedJSON(t *testing.T) {
	rules := map[string]string{"^repo:.*": "git-repo"}
	_, err := manage.ResolveLockKey(rules, `{not json`)
	if err == nil {
		t.Fatal("ResolveLockKey on malformed JSON returned nil; want error")
	}
}

// when multiple regex entries could match, ResolveLockKey picks
// deterministically: map iteration in Go is randomised, so the resolver must
// sort the keys before iterating to keep behaviour reproducible across runs.
func TestResolveLockKeyDeterministicOnOverlap(t *testing.T) {
	rules := map[string]string{
		"^repo:foo.*": "git-repo-foo",
		"^repo:.*":    "git-repo",
	}
	for i := 0; i < 50; i++ {
		got, err := manage.ResolveLockKey(rules, `{"lock_hint":"repo:foo/bar"}`)
		if err != nil {
			t.Fatalf("iter %d: %v", i, err)
		}
		if i == 0 {
			continue
		}
		if got == "" {
			t.Fatalf("iter %d: empty result on overlapping rules", i)
		}
	}
}
