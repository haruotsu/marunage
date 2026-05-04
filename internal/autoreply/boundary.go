package autoreply

// hardcodedDenyCategories are NG categories that MUST NEVER be auto-replied
// regardless of what the user writes in autoreply.toml. The SKILL.md contract
// says "NEVER auto-reply, regardless of configuration", and this slice is the
// compile-time enforcement of that promise.
var hardcodedDenyCategories = []string{
	"personal_information",
	"contracts",
	"financial_decisions",
	"personnel_matters",
}

// Boundary enforces the auto-reply permission rules from a Config.
// Deny always takes precedence over Allow; unknown categories are denied.
type Boundary struct {
	cfg Config
}

// NewBoundary creates a Boundary from the given Config.
func NewBoundary(cfg Config) *Boundary {
	return &Boundary{cfg: cfg}
}

// IsAllowed returns true only when category is in the Allow list and NOT in the
// Deny list. Hardcoded NG categories are always denied regardless of config.
// Deny always wins over Allow; unknown categories default to denied.
func (b *Boundary) IsAllowed(category string) bool {
	for _, d := range hardcodedDenyCategories {
		if d == category {
			return false
		}
	}
	for _, d := range b.cfg.Permissions.Deny {
		if d == category {
			return false
		}
	}
	for _, a := range b.cfg.Permissions.Allow {
		if a == category {
			return true
		}
	}
	return false
}

// IsDraftOnly returns true when draft_mode.enabled is set in the config.
func (b *Boundary) IsDraftOnly() bool {
	return b.cfg.DraftMode.Enabled
}
