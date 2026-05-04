// Package autoreply implements the permission boundary and configuration
// for the marunage-autoreply skill. Config is loaded from
// ~/.marunage/autoreply.toml and kept separate from the main config.toml
// so the auto-reply trust boundary remains independently auditable.
package autoreply

import (
	"errors"
	"fmt"
	"io/fs"
	"os"

	"github.com/pelletier/go-toml/v2"
)

// Config holds the parsed contents of ~/.marunage/autoreply.toml.
type Config struct {
	Permissions Permissions `toml:"permissions"`
	DraftMode   DraftMode   `toml:"draft_mode"`
}

// Permissions defines which message categories can and cannot be auto-replied.
type Permissions struct {
	Allow []string `toml:"allow"`
	Deny  []string `toml:"deny"`
}

// DraftMode controls whether replies are sent or only saved as drafts.
type DraftMode struct {
	Enabled bool `toml:"enabled"`
}

// DefaultConfig returns the built-in safe defaults.
func DefaultConfig() Config {
	return Config{
		Permissions: Permissions{
			Allow: []string{
				"schedule_adjustment",
				"information_sharing",
				"known_questions",
			},
			Deny: []string{
				"personal_information",
				"contracts",
				"financial_decisions",
				"personnel_matters",
			},
		},
		DraftMode: DraftMode{
			Enabled: false,
		},
	}
}

// Load reads the autoreply config from path. A missing file returns DefaultConfig
// so callers work out of the box without requiring manual setup.
func Load(path string) (Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return DefaultConfig(), nil
		}
		return Config{}, fmt.Errorf("read %s: %w", path, err)
	}

	c := DefaultConfig()
	if err := toml.Unmarshal(data, &c); err != nil {
		return Config{}, fmt.Errorf("parse %s: %w", path, err)
	}
	return c, nil
}
