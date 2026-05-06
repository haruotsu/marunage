package cli

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/haruotsu/marunage/internal/config"
	"github.com/haruotsu/marunage/internal/source"
)

// Test 1: registerBuiltin with "markdown" + files → registry has "markdown" plugin
func TestRegisterBuiltin_Markdown(t *testing.T) {
	t.Parallel()
	tmpFile := filepath.Join(t.TempDir(), "tasks.md")
	if err := os.WriteFile(tmpFile, []byte(""), 0o600); err != nil {
		t.Fatal(err)
	}
	r := source.NewRegistry()
	err := registerBuiltin(r, "markdown", config.Config{}, []string{tmpFile}, false)
	if err != nil {
		t.Fatalf("expected nil, got %v", err)
	}
	if _, err := r.Get("markdown"); err != nil {
		t.Fatalf("expected markdown plugin to be registered, got %v", err)
	}
}

// Test 2: registerBuiltin with "slack" → registry has "slack" plugin
func TestRegisterBuiltin_Slack(t *testing.T) {
	t.Parallel()
	r := source.NewRegistry()
	err := registerBuiltin(r, "slack", config.Config{}, nil, false)
	if err != nil {
		t.Fatalf("expected nil, got %v", err)
	}
	if _, err := r.Get("slack"); err != nil {
		t.Fatalf("expected slack plugin to be registered, got %v", err)
	}
}

// Test 3: registerBuiltin with "slack:reaction" → registry has "slack:reaction" plugin
func TestRegisterBuiltin_SlackReaction(t *testing.T) {
	t.Parallel()
	r := source.NewRegistry()
	err := registerBuiltin(r, "slack:reaction", config.Config{}, nil, false)
	if err != nil {
		t.Fatalf("expected nil, got %v", err)
	}
	if _, err := r.Get("slack:reaction"); err != nil {
		t.Fatalf("expected slack:reaction plugin to be registered, got %v", err)
	}
}

// Test 4: registerBuiltin with unknown name, lenient=false → returns error
func TestRegisterBuiltin_UnknownNonLenient(t *testing.T) {
	t.Parallel()
	r := source.NewRegistry()
	err := registerBuiltin(r, "nonexistent", config.Config{}, nil, false)
	if err == nil {
		t.Fatal("expected error for unknown source with lenient=false, got nil")
	}
}

// Test 5: registerBuiltin with unknown name, lenient=true → returns nil, registry is empty
func TestRegisterBuiltin_UnknownLenient(t *testing.T) {
	t.Parallel()
	r := source.NewRegistry()
	err := registerBuiltin(r, "nonexistent", config.Config{}, nil, true)
	if err != nil {
		t.Fatalf("expected nil for unknown source with lenient=true, got %v", err)
	}
	if names := r.Names(); len(names) != 0 {
		t.Fatalf("expected empty registry, got %v", names)
	}
}

// Test 6: registerBuiltin called twice with same name → ErrPluginAlreadyRegistered (non-lenient)
func TestRegisterBuiltin_DuplicateNonLenient(t *testing.T) {
	t.Parallel()
	tmpFile := filepath.Join(t.TempDir(), "tasks.md")
	if err := os.WriteFile(tmpFile, []byte(""), 0o600); err != nil {
		t.Fatal(err)
	}
	r := source.NewRegistry()
	_ = registerBuiltin(r, "markdown", config.Config{}, []string{tmpFile}, false)
	err := registerBuiltin(r, "markdown", config.Config{}, []string{tmpFile}, false)
	if err == nil {
		t.Fatal("expected error on duplicate registration, got nil")
	}
	if !errors.Is(err, source.ErrPluginAlreadyRegistered) {
		t.Fatalf("expected ErrPluginAlreadyRegistered, got %v", err)
	}
}

// Test 7: registerEnabledSources called with enabled=["markdown", "slack:reaction"] → both registered
func TestRegisterEnabledSources_MarkdownAndReaction(t *testing.T) {
	t.Parallel()
	cfg := config.Config{
		Discovery: config.DiscoveryConfig{
			SourcesEnabled: []string{"markdown", "slack:reaction"},
		},
	}
	r := source.NewRegistry()
	err := registerEnabledSources(r, cfg)
	if err != nil {
		t.Fatalf("expected nil, got %v", err)
	}
	if _, err := r.Get("markdown"); err != nil {
		t.Fatalf("expected markdown plugin, got %v", err)
	}
	if _, err := r.Get("slack:reaction"); err != nil {
		t.Fatalf("expected slack:reaction plugin, got %v", err)
	}
}

// Test 8: buildWebSourceRegistry with unknown name → empty registry (silent skip)
func TestBuildWebSourceRegistry_UnknownSkipped(t *testing.T) {
	t.Parallel()
	r := buildWebSourceRegistry([]string{"nonexistent-source"})
	if names := r.Names(); len(names) != 0 {
		t.Fatalf("expected empty registry, got %v", names)
	}
}
