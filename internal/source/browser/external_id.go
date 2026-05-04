// Package browser implements the Browser Adapter source plugin promised in
// docs/pr_split_plan.md PR-200. It scrapes DOM-driven sources (Slack saved
// later, GitHub bookmarks, RSS-less news pages, ...) by talking to a
// pluggable BrowserDriver — the cmux-environment driver shells out to
// `cmux browser`, while tests inject a fake driver so the unit tests never
// touch a real browser.
//
// external_id.go owns the (URL, DOM key) -> ExternalID hash. ExternalID
// must be (a) stable across re-scrapes so the queue's UNIQUE
// (source, external_id) index can deduplicate and (b) collision-resistant
// across sites so two pages that happen to share a DOM key string do not
// alias each other.
package browser

import (
	"crypto/sha256"
	"encoding/hex"
)

// externalIDPrefixLen is the number of hex characters of the SHA-256 digest
// we surface as the ExternalID. Keeping it short (16 hex = 64 bits) leaves
// the marker eyeball-readable while still giving the queue's UNIQUE index
// well over 2^32 distinct ids before a birthday collision becomes plausible.
const externalIDPrefixLen = 16

// computeExternalID returns the stable identifier for a scraped item. Both
// inputs participate in the digest so a configuration that scrapes two URLs
// that emit the same DOM key (e.g. two Slack workspaces both surfacing
// item id "1") cannot collide. A `\x00` separator prevents
// (url="ab", key="c") and (url="a", key="bc") from hashing to the same
// digest.
func computeExternalID(siteURL, domKey string) string {
	h := sha256.New()
	h.Write([]byte(siteURL))
	h.Write([]byte{0})
	h.Write([]byte(domKey))
	sum := h.Sum(nil)
	return hex.EncodeToString(sum)[:externalIDPrefixLen]
}
