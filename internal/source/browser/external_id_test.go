package browser

import "testing"

// TestComputeExternalIDIsStable nails the must-not-change contract: the
// hash of the same (URL, key) pair across two calls must be byte-identical,
// otherwise re-scraping the same item would create a second queue row
// every cycle.
func TestComputeExternalIDIsStable(t *testing.T) {
	t.Parallel()

	a := computeExternalID("https://example.com/", "item-1")
	b := computeExternalID("https://example.com/", "item-1")
	if a != b {
		t.Fatalf("ExternalID drifted: %q vs %q", a, b)
	}
	if len(a) != externalIDPrefixLen {
		t.Errorf("len = %d, want %d", len(a), externalIDPrefixLen)
	}
}

// TestComputeExternalIDDistinguishesURL guards against a naive concat
// implementation: two pages whose DOM keys collide must still produce
// distinct ExternalIDs because the URL participates in the hash.
func TestComputeExternalIDDistinguishesURL(t *testing.T) {
	t.Parallel()

	a := computeExternalID("https://workspace-a.slack.com/saved", "msg-1")
	b := computeExternalID("https://workspace-b.slack.com/saved", "msg-1")
	if a == b {
		t.Fatalf("ExternalID collided across URLs: %q", a)
	}
}

// TestComputeExternalIDDistinguishesKey is the symmetric guard: same URL,
// different DOM key, different hash.
func TestComputeExternalIDDistinguishesKey(t *testing.T) {
	t.Parallel()

	a := computeExternalID("https://example.com/", "item-1")
	b := computeExternalID("https://example.com/", "item-2")
	if a == b {
		t.Fatalf("ExternalID collided across DOM keys: %q", a)
	}
}

// TestComputeExternalIDSeparatorPreventsBoundaryAlias guards the `\x00`
// separator: without it ("ab", "") and ("a", "b") would hash identically,
// and a config emitting empty keys for some items would silently alias.
func TestComputeExternalIDSeparatorPreventsBoundaryAlias(t *testing.T) {
	t.Parallel()

	a := computeExternalID("ab", "")
	b := computeExternalID("a", "b")
	if a == b {
		t.Fatalf("boundary alias not prevented: %q", a)
	}
}
