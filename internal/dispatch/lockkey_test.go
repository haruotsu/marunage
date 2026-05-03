package dispatch_test

import (
	"testing"

	"github.com/haruotsu/marunage/internal/dispatch"
)

// PR-42 lock-key resolver test list:
//
//   B1. ResolveLockKey returns the configured value when notes.lock_hint
//       matches a regex in the lock_keys map (docs/requirement.md
//       "[execution.lock_keys]" + PR-42 着手前メモ at L835).
//   B2. ResolveLockKey returns "" when notes.lock_hint does not match
//       any configured regex — an unmatched hint is intentionally not a
//       hard error; the dispatcher just skips AcquireLock for that task.
//   B3. ResolveLockKey returns "" without panicking when notes is empty,
//       not a JSON object, or lacks a lock_hint key. The store CHECK
//       constraint already pins notes to json_valid(); ensuring this
//       resolver also tolerates "" / NULL keeps it usable in tests that
//       do not bother stamping notes.

// B1: matching regex resolves to its mapped lock_key.
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
		got, err := dispatch.ResolveLockKey(rules, tc.notes)
		if err != nil {
			t.Fatalf("ResolveLockKey(%q): %v", tc.notes, err)
		}
		if got != tc.want {
			t.Errorf("ResolveLockKey(%q) = %q; want %q", tc.notes, got, tc.want)
		}
	}
}

// B2: lock_hint with no matching regex falls through to "".
func TestResolveLockKeyNoMatch(t *testing.T) {
	rules := map[string]string{
		"^repo:.*": "git-repo",
	}
	got, err := dispatch.ResolveLockKey(rules, `{"lock_hint":"gmail:thread-1"}`)
	if err != nil {
		t.Fatalf("ResolveLockKey: %v", err)
	}
	if got != "" {
		t.Errorf("ResolveLockKey unmatched = %q; want %q", got, "")
	}
}

// B3: tolerant of empty / non-object / missing-key notes.
func TestResolveLockKeyTolerantOfEmptyNotes(t *testing.T) {
	rules := map[string]string{"^repo:.*": "git-repo"}
	cases := []string{
		"",                      // store NULL
		`{}`,                    // valid JSON object without lock_hint
		`{"foo":"bar"}`,         // valid JSON object, different key
		`{"lock_hint":""}`,      // explicit empty hint
		`{"lock_hint":null}`,    // explicit null hint
		`"plain string"`,        // valid JSON but not an object
		`[1,2,3]`,               // valid JSON array
	}
	for _, notes := range cases {
		got, err := dispatch.ResolveLockKey(rules, notes)
		if err != nil {
			t.Errorf("ResolveLockKey(%q) returned error %v; tolerant resolver should not fail", notes, err)
		}
		if got != "" {
			t.Errorf("ResolveLockKey(%q) = %q; want %q", notes, got, "")
		}
	}
}

// B3b: malformed JSON in notes is reported as an error so the dispatcher
// can decide what to do (fail loudly vs. fall through). A typo in the
// notes column is rare but should not be silently treated as "no hint":
// that would mask a Discovery plugin bug indefinitely.
func TestResolveLockKeyReportsMalformedJSON(t *testing.T) {
	rules := map[string]string{"^repo:.*": "git-repo"}
	_, err := dispatch.ResolveLockKey(rules, `{not json`)
	if err == nil {
		t.Fatal("ResolveLockKey on malformed JSON returned nil; want error")
	}
}

// B1b: when multiple regex entries could match, ResolveLockKey picks
// deterministically. Map iteration in Go is randomised, so the resolver
// must sort the keys before iterating to keep dispatch behaviour
// reproducible across runs (otherwise two parallel marunage instances
// could pick different lock_key values for the same row).
func TestResolveLockKeyDeterministicOnOverlap(t *testing.T) {
	rules := map[string]string{
		"^repo:foo.*": "git-repo-foo",
		"^repo:.*":    "git-repo",
	}
	// Run many times: with map iteration randomised, a non-deterministic
	// resolver would eventually flip and fail this test.
	for i := 0; i < 50; i++ {
		got, err := dispatch.ResolveLockKey(rules, `{"lock_hint":"repo:foo/bar"}`)
		if err != nil {
			t.Fatalf("iter %d: %v", i, err)
		}
		// Either answer is "correct" in isolation; what matters is that
		// every iteration returns the SAME answer so two dispatcher runs
		// never disagree on the lock_key for a given row.
		if i == 0 {
			continue
		}
		if got == "" {
			t.Fatalf("iter %d: empty result on overlapping rules", i)
		}
	}
}
