package slack

import "testing"

// TestCompareTSBoundaryCases pins the documented contract of compareTS
// — empty-as-min, equal, integer-width-vs-lex, fractional padding —
// against hand-written cases the design memo (G section) calls out.
// Without these the only signal we get if the comparator regresses is
// "Since persisted the wrong checkpoint", which is two layers removed
// from the actual bug.
func TestCompareTSBoundaryCases(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		a, b string
		want int
	}{
		{"both empty", "", "", 0},
		{"empty is smallest, lhs", "", "1.0", -1},
		{"empty is smallest, rhs", "1.0", "", 1},
		{"equal canonical width", "1700000000.000100", "1700000000.000100", 0},
		{"integer part differs by length", "999.0", "1000.0", -1},
		{"integer part lex with same width", "1700000000.0", "1700000001.0", -1},
		{"fractional widths differ but value equal", "1.0", "1.00", 0},
		{"fractional padding: 0.0001 > 0.00009", "1700000000.0001", "1700000000.00009", 1},
		{"integer-only smaller than decimal", "1700000000", "1700000000.000001", -1},
		{"integer-only equal to decimal with zero fraction", "1700000000", "1700000000.000000", 0},
		{"trailing zeros in fractional are equal", "1.10", "1.1", 0},
		{"different fractional values", "1.5", "1.4", 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := compareTS(tc.a, tc.b)
			if got != tc.want {
				t.Errorf("compareTS(%q, %q) = %d, want %d", tc.a, tc.b, got, tc.want)
			}
			// Antisymmetry: swapping the args must flip the sign.
			if mirror := compareTS(tc.b, tc.a); mirror != -tc.want {
				t.Errorf("compareTS(%q, %q) = %d, want %d (antisymmetry)", tc.b, tc.a, mirror, -tc.want)
			}
		})
	}
}

// FuzzCompareTSAntisymmetry is a tiny property test confirming the
// comparator is antisymmetric on whatever digit-shaped ts strings the
// fuzzer can dream up. Catches future implementations that, say, return
// 1 in both directions for the same pair.
func FuzzCompareTSAntisymmetry(f *testing.F) {
	seeds := [][2]string{
		{"", ""},
		{"1.0", "1.0"},
		{"1700000000.000100", "1700000000.000200"},
		{"1700000000", "1700000000.0"},
	}
	for _, s := range seeds {
		f.Add(s[0], s[1])
	}
	f.Fuzz(func(t *testing.T, a, b string) {
		if !validTSCandidate(a) || !validTSCandidate(b) {
			t.Skip()
		}
		ab := compareTS(a, b)
		ba := compareTS(b, a)
		if ab+ba != 0 {
			t.Fatalf("antisymmetry violated: compareTS(%q,%q)=%d, compareTS(%q,%q)=%d", a, b, ab, b, a, ba)
		}
	})
}

// validTSCandidate filters the fuzz corpus to digit-and-dot strings the
// production code is expected to receive — anything else (e.g. NaN,
// scientific notation) is out of contract and not under test.
func validTSCandidate(s string) bool {
	dots := 0
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c >= '0' && c <= '9':
		case c == '.':
			dots++
			if dots > 1 {
				return false
			}
		default:
			return false
		}
	}
	return true
}
