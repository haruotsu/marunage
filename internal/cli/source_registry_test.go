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
	r := buildWebSourceRegistry([]string{"nonexistent-source"}, config.Config{})
	if names := r.Names(); len(names) != 0 {
		t.Fatalf("expected empty registry, got %v", names)
	}
}

// Test 9–14: Phase 2+ built-in sources register without error
func TestRegisterBuiltin_Gmail(t *testing.T) {
	t.Parallel()
	r := source.NewRegistry()
	if err := registerBuiltin(r, "gmail", config.Config{}, nil, false); err != nil {
		t.Fatalf("expected nil, got %v", err)
	}
	if _, err := r.Get("gmail"); err != nil {
		t.Fatalf("expected gmail plugin, got %v", err)
	}
}

func TestRegisterBuiltin_GitHub(t *testing.T) {
	t.Parallel()
	r := source.NewRegistry()
	if err := registerBuiltin(r, "github", config.Config{}, nil, false); err != nil {
		t.Fatalf("expected nil, got %v", err)
	}
	if _, err := r.Get("github"); err != nil {
		t.Fatalf("expected github plugin, got %v", err)
	}
}

func TestRegisterBuiltin_Calendar(t *testing.T) {
	t.Parallel()
	r := source.NewRegistry()
	if err := registerBuiltin(r, "calendar", config.Config{}, nil, false); err != nil {
		t.Fatalf("expected nil, got %v", err)
	}
	if _, err := r.Get("calendar"); err != nil {
		t.Fatalf("expected calendar plugin, got %v", err)
	}
}

func TestRegisterBuiltin_GoogleTasks(t *testing.T) {
	t.Parallel()
	r := source.NewRegistry()
	if err := registerBuiltin(r, "googletasks", config.Config{}, nil, false); err != nil {
		t.Fatalf("expected nil, got %v", err)
	}
	if _, err := r.Get("googletasks"); err != nil {
		t.Fatalf("expected googletasks plugin, got %v", err)
	}
}

func TestRegisterBuiltin_Notion(t *testing.T) {
	t.Parallel()
	r := source.NewRegistry()
	if err := registerBuiltin(r, "notion", config.Config{}, nil, false); err != nil {
		t.Fatalf("expected nil, got %v", err)
	}
	if _, err := r.Get("notion"); err != nil {
		t.Fatalf("expected notion plugin, got %v", err)
	}
}

// Test 15: every entry in knownBuiltinNames registers without error (knownBuiltinNames ↔ switch sync guard)
func TestRegisterBuiltin_AllKnownNamesSucceed(t *testing.T) {
	t.Parallel()
	for _, name := range knownBuiltinNames {
		name := name
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			r := source.NewRegistry()
			if err := registerBuiltin(r, name, config.Config{}, nil, false); err != nil {
				t.Fatalf("%s: %v", name, err)
			}
		})
	}
}

// Test 16: buildWebSourceRegistry registers a known source correctly (lenient mode)
func TestBuildWebSourceRegistry_KnownSourceRegistered(t *testing.T) {
	t.Parallel()
	r := buildWebSourceRegistry([]string{"slack"}, config.Config{})
	if _, err := r.Get("slack"); err != nil {
		t.Fatalf("expected slack plugin in web registry, got %v", err)
	}
}

// Test 17: buildWebSourceRegistry passes cfg to slack:reaction (no zero-value config leak)
func TestBuildWebSourceRegistry_SlackReactionUsesConfig(t *testing.T) {
	t.Parallel()
	cfg := config.Config{
		Discovery: config.DiscoveryConfig{
			SourcesEnabled: []string{"slack:reaction"},
			Slack: config.DiscoverySlack{
				ReactionTrigger: config.SlackReactionTriggerConfig{
					Reactions: []string{"eyes", "rocket"},
				},
			},
		},
	}
	r := buildWebSourceRegistry(cfg.Discovery.SourcesEnabled, cfg)
	if _, err := r.Get("slack:reaction"); err != nil {
		t.Fatalf("expected slack:reaction plugin with real config, got %v", err)
	}
}
