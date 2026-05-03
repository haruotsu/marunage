package googletasks

import (
	"testing"

	"github.com/haruotsu/marunage/internal/source"
)

// TestPluginName pins the plugin's canonical identifier. The registry,
// manifest, and Task.Source must all read "googletasks"; if any of them
// drifts the cross-source dispatcher would silently address the wrong
// plugin.
func TestPluginName(t *testing.T) {
	t.Parallel()

	p := New()
	if p.Name() != "googletasks" {
		t.Fatalf("Name() = %q, want googletasks", p.Name())
	}
}

// TestPluginImplementsContract is the compile-time witness that *Plugin
// satisfies the mandatory source.Plugin interface plus the three optional
// capabilities the manifest declares (Adder / Completer / Deleter). If a
// method goes missing, this test fails to compile.
func TestPluginImplementsContract(t *testing.T) {
	t.Parallel()

	var _ source.Plugin = (*Plugin)(nil)
	var _ source.Adder = (*Plugin)(nil)
	var _ source.Completer = (*Plugin)(nil)
	var _ source.Deleter = (*Plugin)(nil)
}
