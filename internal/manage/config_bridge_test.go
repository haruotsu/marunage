package manage

import (
	"context"
	"testing"
	"time"

	"github.com/haruotsu/marunage/internal/collect"
	"github.com/haruotsu/marunage/internal/config"
	"github.com/haruotsu/marunage/internal/store"
)

func TestRulesFromConfigParsesDuration(t *testing.T) {
	got, err := RulesFromConfig(config.ManageRulesConfig{
		BlockIfDepsIncomplete: true,
		EscalateIfBodyEmpty:   false,
		DropIfCwdViolation:    true,
		BoostIfDueWithin:      "12h",
	})
	if err != nil {
		t.Fatalf("RulesFromConfig: %v", err)
	}
	if !got.BlockIfDepsIncomplete || got.EscalateIfBodyEmpty || !got.DropIfCwdViolation {
		t.Errorf("toggles = %+v; want block+drop on, escalate off", got)
	}
	if got.BoostIfDueWithin != 12*time.Hour {
		t.Errorf("BoostIfDueWithin = %v; want 12h", got.BoostIfDueWithin)
	}
}

func TestRulesFromConfigEmptyDurationDisablesBoost(t *testing.T) {
	got, err := RulesFromConfig(config.ManageRulesConfig{BoostIfDueWithin: ""})
	if err != nil {
		t.Fatalf("RulesFromConfig: %v", err)
	}
	if got.BoostIfDueWithin != 0 {
		t.Errorf("BoostIfDueWithin = %v; want 0", got.BoostIfDueWithin)
	}
}

func TestRulesFromConfigRejectsBadDuration(t *testing.T) {
	_, err := RulesFromConfig(config.ManageRulesConfig{BoostIfDueWithin: "nope"})
	if err == nil {
		t.Fatal("RulesFromConfig: want error for bad duration")
	}
}

func TestVerdictPoliciesFromConfigMapsUnderscoreKeys(t *testing.T) {
	reg := VerdictPoliciesFromConfig(map[string]config.VerdictPolicy{
		"needs_human": {Status: store.StatusWaitingHuman, Dispatchable: false, Notify: true},
		"ready":       {Status: store.StatusPending, Dispatchable: true},
	})
	if got := reg.policyFor(collect.VerdictNeedsHuman); !got.Notify || got.Status != store.StatusWaitingHuman {
		t.Errorf("needs-human policy = %+v; want waiting_human+notify (underscore key mapped)", got)
	}
	if got := reg.policyFor(collect.VerdictReady); !got.Dispatchable {
		t.Errorf("ready policy = %+v; want dispatchable", got)
	}
}

// A partial config map still yields a complete registry: missing verdicts
// fall back to the built-in defaults (原則2: the map need not be exhaustive).
func TestVerdictPoliciesFromConfigFillsDefaults(t *testing.T) {
	reg := VerdictPoliciesFromConfig(map[string]config.VerdictPolicy{
		"ready": {Status: store.StatusPending, Dispatchable: true},
	})
	if got := reg.policyFor(collect.VerdictDrop); got.Status != store.StatusSkipped {
		t.Errorf("drop policy = %+v; want default skipped status", got)
	}
}

// The config-driven registry plugs into Plan via WithRegistry: a verdict the
// config marks non-dispatchable keeps that candidate out of the ready列.
func TestVerdictPoliciesDrivePlanDispatchability(t *testing.T) {
	reg := VerdictPoliciesFromConfig(map[string]config.VerdictPolicy{
		"ready": {Status: store.StatusPending, Dispatchable: false},
	})
	st := &fakeStore{}
	cands := []collect.Candidate{{Title: "t", Body: "b", Priority: "5"}}
	plan, err := Plan(context.Background(), cands, st, WithRegistry(reg))
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if len(plan.Ready) != 0 {
		t.Errorf("Ready = %d; want 0 (config marked ready non-dispatchable)", len(plan.Ready))
	}
}
