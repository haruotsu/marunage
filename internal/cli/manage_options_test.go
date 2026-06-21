package cli

import (
	"testing"

	"github.com/haruotsu/marunage/internal/config"
)

// TestManageOptions_LLMScoringTogglesScorer pins the core PR-R06 switch: with
// llm_scoring on, manageOptions injects exactly one additional planner option
// (the LLM scorer); with it off, only the deterministic rule/registry options
// are returned. Deleting the `if cfg.Manage.LLMScoring` wiring would regress
// this without surfacing anywhere else (the loop factory is bypassed in tests).
func TestManageOptions_LLMScoringTogglesScorer(t *testing.T) {
	cfg := config.Default()

	cfg.Manage.LLMScoring = false
	off, err := manageOptions(cfg, nil, "")
	if err != nil {
		t.Fatalf("manageOptions (off): %v", err)
	}

	cfg.Manage.LLMScoring = true
	on, err := manageOptions(cfg, nil, "")
	if err != nil {
		t.Fatalf("manageOptions (on): %v", err)
	}

	if len(on) != len(off)+1 {
		t.Fatalf("llm_scoring on returned %d options, off returned %d; want on = off + 1 (the scorer)",
			len(on), len(off))
	}
}

// TestManageOptions_InvalidRulesError pins that a malformed [manage.rules]
// duration surfaces as an error rather than being silently dropped.
func TestManageOptions_InvalidRulesError(t *testing.T) {
	cfg := config.Default()
	cfg.Manage.Rules.BoostIfDueWithin = "not-a-duration"
	if _, err := manageOptions(cfg, nil, ""); err == nil {
		t.Fatal("manageOptions must error on an unparseable boost_if_due_within")
	}
}
