package manage

import (
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
)

// ResolveLockKey looks up the soft-lock key a row should AcquireLock on, by
// extracting notes."lock_hint" and matching it against the regex map loaded
// from [execution.lock_keys] in config.toml. Returns "" when no rule matches;
// callers skip AcquireLock in that case.
//
// It lives here, in the management layer, rather than in dispatch: lock
// contention is a management-layer concern (redesign §3.2 "lock 競合", §7 folds
// the lock concern into manage), and the planner's lock-conflict rule resolves
// the same key the dispatcher later acquires. Keeping one resolver upstream of
// dispatch makes the dependency one-directional (collect→manage→exec/dispatch)
// instead of dispatch and manage each owning a copy, or manage reaching back
// down into dispatch.
//
// rules has the shape {regex -> lock_key}. The resolver sorts the regex keys
// lexicographically before iterating so that two runs against the same row
// always pick the same lock_key — Go map iteration order is intentionally
// randomised, and a non-deterministic resolver would let two parallel marunage
// instances race against each other for the same row's lock.
//
// Errors:
//   - invalid notes JSON: returned as-is so a Discovery plugin bug fails
//     loud rather than silently dropping the lock_hint.
//   - invalid regex in rules: returned as-is, naming the offending
//     pattern so the user can fix config.toml.
//
// Tolerances (intentionally not errors):
//   - empty notes ("" or NULL on the wire) -> ""
//   - notes is not a JSON object (string, array, ...) -> ""
//   - notes is an object without a lock_hint key -> ""
//   - lock_hint is null or "" -> ""
func ResolveLockKey(rules map[string]string, notes string) (string, error) {
	if notes == "" {
		return "", nil
	}

	// Decode into a generic value first so we can tell "not an object"
	// (legitimate fall-through) apart from "malformed JSON" (an error).
	var raw any
	if err := json.Unmarshal([]byte(notes), &raw); err != nil {
		return "", fmt.Errorf("manage: notes is not valid JSON: %w", err)
	}
	obj, ok := raw.(map[string]any)
	if !ok {
		return "", nil
	}
	hintRaw, ok := obj["lock_hint"]
	if !ok {
		return "", nil
	}
	hint, ok := hintRaw.(string)
	if !ok || hint == "" {
		return "", nil
	}

	patterns := make([]string, 0, len(rules))
	for p := range rules {
		patterns = append(patterns, p)
	}
	sort.Strings(patterns)

	for _, pattern := range patterns {
		re, err := regexp.Compile(pattern)
		if err != nil {
			return "", fmt.Errorf("manage: invalid lock_key regex %q: %w", pattern, err)
		}
		if re.MatchString(hint) {
			return rules[pattern], nil
		}
	}
	return "", nil
}
