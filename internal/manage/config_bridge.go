package manage

import (
	"fmt"
	"strings"
	"time"

	"github.com/haruotsu/marunage/internal/collect"
	"github.com/haruotsu/marunage/internal/config"
)

// RulesFromConfig builds the rule-engine toggles from the [manage.rules]
// config table, parsing the boost window string into a duration once so Plan
// need not re-parse it per candidate. An empty boost_if_due_within disables
// the boost; a malformed one is a config error surfaced to the caller (the
// cmd wiring, PR-R05) rather than silently dropped.
func RulesFromConfig(c config.ManageRulesConfig) (Rules, error) {
	r := Rules{
		BlockIfDepsIncomplete: c.BlockIfDepsIncomplete,
		EscalateIfBodyEmpty:   c.EscalateIfBodyEmpty,
		DropIfCwdViolation:    c.DropIfCwdViolation,
	}
	if c.BoostIfDueWithin != "" {
		d, err := time.ParseDuration(c.BoostIfDueWithin)
		if err != nil {
			return Rules{}, fmt.Errorf("manage: boost_if_due_within: %w", err)
		}
		r.BoostIfDueWithin = d
	}
	return r, nil
}

// VerdictPoliciesFromConfig builds a Registry from the [manage.verdicts]
// config table (原則1: the verdict→status mapping lives in config, not code).
// It starts from DefaultRegistry so a partial config still yields a complete
// registry — the map need not list every verdict (原則2) — then overlays each
// configured entry.
//
// TOML keys use the underscore form ("needs_human") because a hyphen is not a
// bare-key character; the Verdict vocabulary uses hyphens ("needs-human").
// Underscores are rewritten to hyphens so the two forms line up, which also
// lets a future verdict key map without a code change.
func VerdictPoliciesFromConfig(m map[string]config.VerdictPolicy) Registry {
	reg := DefaultRegistry()
	for key, p := range m {
		v := collect.Verdict(strings.ReplaceAll(key, "_", "-"))
		reg[v] = VerdictPolicy{
			Status:       p.Status,
			Dispatchable: p.Dispatchable,
			Notify:       p.Notify,
		}
	}
	return reg
}
