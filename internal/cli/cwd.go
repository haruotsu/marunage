package cli

import (
	"os/exec"
	"strings"

	"github.com/haruotsu/marunage/internal/config"
)

// cwdResolution turns the [execution] cwd knobs into the (strategy, ghqRoot)
// pair the dispatcher's WithCwdStrategy / WithGhqRoot options expect.
//
// An empty cwd_strategy is normalised to "ghq" so configs written before the
// key existed still get repo-aware resolution (it degrades to default_cwd when
// nothing matches, so this is safe even without ghq installed). For the ghq
// strategy the root comes from [execution].ghq_root, or, when that is empty,
// from `ghq root`; a failure there yields an empty root, which disables ghq
// resolution and falls every task back to default_cwd.
func cwdResolution(cfg config.Config) (strategy, ghqRoot string) {
	strategy = cfg.Execution.CwdStrategy
	if strategy == "" {
		strategy = "ghq"
	}
	if strategy != "ghq" {
		return strategy, ""
	}
	if root := cfg.Execution.GhqRoot; root != "" {
		if exp, err := expandHome(root); err == nil {
			return strategy, exp
		}
		return strategy, root
	}
	out, err := exec.Command("ghq", "root").Output()
	if err != nil {
		return strategy, ""
	}
	return strategy, strings.TrimSpace(string(out))
}
