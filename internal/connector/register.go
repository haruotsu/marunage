package connector

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/haruotsu/marunage/internal/source"
)

// RegisterFromDir walks connectorDir and registers each subdirectory that
// contains a connector.toml as a source.Plugin in r. Subdirectories without
// connector.toml are silently skipped. A non-existent connectorDir is treated
// as empty and returns nil.
func RegisterFromDir(r *source.Registry, connectorDir string) error {
	entries, err := os.ReadDir(connectorDir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return fmt.Errorf("read connector dir %s: %w", connectorDir, err)
	}

	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		tomlPath := filepath.Join(connectorDir, entry.Name(), "connector.toml")
		if _, err := os.Stat(tomlPath); errors.Is(err, os.ErrNotExist) {
			continue
		}

		cfg, err := LoadConfig(tomlPath)
		if err != nil {
			return fmt.Errorf("load connector %s: %w", tomlPath, err)
		}

		adapter, err := NewHTTPAdapter(cfg)
		if err != nil {
			return fmt.Errorf("create adapter for %s: %w", tomlPath, err)
		}

		if err := r.Register(adapter); err != nil {
			return fmt.Errorf("register connector %q: %w", cfg.Connector.Name, err)
		}
	}
	return nil
}
