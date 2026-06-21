package collect_test

import (
	"testing"

	"github.com/haruotsu/marunage/internal/collect"
)

func TestVerdictConstantStrings(t *testing.T) {
	cases := []struct {
		got  collect.Verdict
		want string
	}{
		{collect.VerdictReady, "ready"},
		{collect.VerdictHold, "hold"},
		{collect.VerdictDefer, "defer"},
		{collect.VerdictNeedsHuman, "needs-human"},
		{collect.VerdictDrop, "drop"},
	}
	for _, c := range cases {
		if string(c.got) != c.want {
			t.Errorf("verdict = %q; want %q", string(c.got), c.want)
		}
	}
}

// The empty Verdict is the "undecided" sentinel a Candidate carries
// before any layer has classified it; it must not be mistaken for a
// known verdict so the manage layer can tell "not yet judged" apart
// from a deliberate label.
func TestVerdictKnown(t *testing.T) {
	for _, v := range []collect.Verdict{
		collect.VerdictReady, collect.VerdictHold, collect.VerdictDefer,
		collect.VerdictNeedsHuman, collect.VerdictDrop,
	} {
		if !v.Known() {
			t.Errorf("Known(%q) = false; want true", v)
		}
	}
	for _, v := range []collect.Verdict{"", "bogus", "delegate"} {
		if v.Known() {
			t.Errorf("Known(%q) = true; want false", v)
		}
	}
}
