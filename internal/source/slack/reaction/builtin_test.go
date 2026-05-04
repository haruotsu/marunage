package reaction_test

import (
	"testing"

	"github.com/haruotsu/marunage/internal/source"
	"github.com/haruotsu/marunage/internal/source/slack/reaction"
)

func TestManifestParsesWithoutError(t *testing.T) {
	t.Parallel()
	m, err := reaction.Manifest()
	if err != nil {
		t.Fatalf("Manifest: %v", err)
	}
	if m == nil {
		t.Fatal("Manifest returned nil")
	}
}

func TestRegisterBuiltinAddsPluginToRegistry(t *testing.T) {
	t.Parallel()
	r := source.NewRegistry()
	if err := reaction.RegisterBuiltin(r); err != nil {
		t.Fatalf("RegisterBuiltin: %v", err)
	}
	p, err := r.Get("slack:reaction")
	if err != nil {
		t.Fatalf("Get slack:reaction: %v", err)
	}
	if p.Name() != "slack:reaction" {
		t.Errorf("Name() = %q, want slack:reaction", p.Name())
	}
}

func TestRegisterBuiltinRejectsNilRegistry(t *testing.T) {
	t.Parallel()
	if err := reaction.RegisterBuiltin(nil); err == nil {
		t.Fatal("expected error for nil registry, got nil")
	}
}

func TestRegisterBuiltinRejectsDuplicateRegistration(t *testing.T) {
	t.Parallel()
	r := source.NewRegistry()
	if err := reaction.RegisterBuiltin(r); err != nil {
		t.Fatalf("first RegisterBuiltin: %v", err)
	}
	err := reaction.RegisterBuiltin(r)
	if err == nil {
		t.Fatal("expected error on duplicate registration, got nil")
	}
}

// Compile-time check: *Adapter satisfies source.Plugin, source.Sincer, and
// source.Completer. A build failure here means a method signature drifted.
var (
	_ source.Plugin    = (*reaction.Adapter)(nil)
	_ source.Sincer    = (*reaction.Adapter)(nil)
	_ source.Completer = (*reaction.Adapter)(nil)
)
