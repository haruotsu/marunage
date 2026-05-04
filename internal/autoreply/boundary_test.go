package autoreply_test

import (
	"testing"

	"github.com/haruotsu/marunage/internal/autoreply"
)

func TestIsAllowed_KnownOKCategory(t *testing.T) {
	cfg := autoreply.Config{
		Permissions: autoreply.Permissions{
			Allow: []string{"schedule_adjustment", "known_questions"},
			Deny:  []string{"personal_information"},
		},
	}
	b := autoreply.NewBoundary(cfg)
	if !b.IsAllowed("schedule_adjustment") {
		t.Error("schedule_adjustment should be allowed")
	}
	if !b.IsAllowed("known_questions") {
		t.Error("known_questions should be allowed")
	}
}

func TestIsAllowed_NGCategory_ReturnsFalse(t *testing.T) {
	cfg := autoreply.Config{
		Permissions: autoreply.Permissions{
			Allow: []string{"schedule_adjustment"},
			Deny:  []string{"personal_information", "financial_decisions"},
		},
	}
	b := autoreply.NewBoundary(cfg)
	if b.IsAllowed("personal_information") {
		t.Error("personal_information must not be allowed")
	}
	if b.IsAllowed("financial_decisions") {
		t.Error("financial_decisions must not be allowed")
	}
}

func TestIsAllowed_DenyTakesPrecedenceOverAllow(t *testing.T) {
	cfg := autoreply.Config{
		Permissions: autoreply.Permissions{
			Allow: []string{"schedule_adjustment", "personal_information"},
			Deny:  []string{"personal_information"},
		},
	}
	b := autoreply.NewBoundary(cfg)
	if b.IsAllowed("personal_information") {
		t.Error("deny must take precedence over allow")
	}
}

func TestIsAllowed_UnknownCategory_ReturnsFalse(t *testing.T) {
	cfg := autoreply.Config{
		Permissions: autoreply.Permissions{
			Allow: []string{"schedule_adjustment"},
			Deny:  []string{"personal_information"},
		},
	}
	b := autoreply.NewBoundary(cfg)
	if b.IsAllowed("completely_unknown_category") {
		t.Error("unknown category must be denied by default")
	}
}

func TestIsDraftOnly_WhenEnabled(t *testing.T) {
	cfg := autoreply.Config{
		DraftMode: autoreply.DraftMode{Enabled: true},
	}
	b := autoreply.NewBoundary(cfg)
	if !b.IsDraftOnly() {
		t.Error("IsDraftOnly should return true when DraftMode.Enabled=true")
	}
}

func TestIsDraftOnly_WhenDisabled(t *testing.T) {
	cfg := autoreply.Config{
		DraftMode: autoreply.DraftMode{Enabled: false},
	}
	b := autoreply.NewBoundary(cfg)
	if b.IsDraftOnly() {
		t.Error("IsDraftOnly should return false when DraftMode.Enabled=false")
	}
}
