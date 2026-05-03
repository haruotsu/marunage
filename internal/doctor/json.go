package doctor

import "encoding/json"

// MarshalJSON serialises rep into the stable shape consumed by the Web UI
// and CI integrations. The shape is fully captured by the json tags on
// Report and CheckOutcome so this function is intentionally one line; the
// dedicated entry point exists to give callers a single place to swap in
// indentation or framing if the contract evolves.
func MarshalJSON(rep Report) ([]byte, error) {
	return json.MarshalIndent(rep, "", "  ")
}
