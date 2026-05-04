package source

import (
	"errors"
	"fmt"
	"sort"
	"sync"
)

// ErrPluginAlreadyRegistered is returned by Registry.Register when a plugin
// with the same name is already present. Distinct from ErrPluginNotFound so
// a caller doing "register if missing" can tell the two apart without
// parsing strings.
var ErrPluginAlreadyRegistered = errors.New("source: plugin already registered")

// ErrCapabilityMismatch is returned by ValidateAgainstManifest when the
// plugin's interface implementations disagree with the manifest's declared
// capabilities. We surface this as a typed error so the registry's startup
// hook can refuse to register the plugin and abort `marunage discover`
// before it tries to dispatch a method that would crash.
var ErrCapabilityMismatch = errors.New("source: plugin capability mismatch")

// Registry is the in-memory map of (name -> Plugin). Phase 1 only registers
// the built-in markdown plugin (and any future built-ins added to internal/
// source/*); Phase 4 will walk ~/.marunage/sources/*/plugin.toml and
// register dynamically loaded entries here. Either way the lookup surface
// is identical: callers go through Get, never through the underlying map.
type Registry struct {
	mu      sync.RWMutex
	plugins map[string]Plugin
}

// NewRegistry returns an empty Registry. The package does not export a
// global default registry on purpose: every test that registers a plugin
// would otherwise leak state into the next test, and passing a *Registry
// explicitly makes the dependency obvious in stack traces.
func NewRegistry() *Registry {
	return &Registry{plugins: map[string]Plugin{}}
}

// Register stores p under p.Name(). Registering twice is rejected so a
// caller copy-pasting a registration block (a real risk once plugin loading
// becomes a `for _, p := range builtins` loop) sees the bug immediately
// instead of silently overwriting.
func (r *Registry) Register(p Plugin) error {
	if p == nil {
		return fmt.Errorf("source: cannot register nil plugin")
	}
	name := p.Name()
	if name == "" {
		return fmt.Errorf("source: plugin name is empty")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.plugins[name]; ok {
		return fmt.Errorf("%w: %q", ErrPluginAlreadyRegistered, name)
	}
	r.plugins[name] = p
	return nil
}

// Get returns the plugin registered under name. ErrPluginNotFound on miss.
func (r *Registry) Get(name string) (Plugin, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	p, ok := r.plugins[name]
	if !ok {
		return nil, fmt.Errorf("%w: %q", ErrPluginNotFound, name)
	}
	return p, nil
}

// Names returns the registered plugin names sorted lexicographically. The
// CLI prints this in `marunage discover --help`-style listings, so a
// stable order keeps the output diffable across runs.
func (r *Registry) Names() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]string, 0, len(r.plugins))
	for n := range r.plugins {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}

// ValidateAgainstManifest cross-checks p's runtime interface implementations
// against m's declared capabilities. The brief calls this out as the
// "capability ↔ interface" guard: a manifest that promises `add` must back
// it with an Adder implementation; otherwise startup fails loudly instead
// of letting the dispatch loop crash on a missing method later.
//
// The check is one-way: optional capabilities a plugin implements but does
// NOT declare in the manifest are tolerated. The manifest is the
// authoritative "what the source says it can do"; over-implementing is
// harmless, under-implementing is a contract violation.
//
// Dispatch-side enforcement (PR-71): the scheduler that turns plugin
// output into queue rows must consult Manifest.HasCapability before
// invoking Adder/Completer/Deleter — never reach for the runtime type
// assertion alone — so the manifest stays the single source of truth
// for "is this source allowed to mutate upstream?". Documenting the
// rule here is the easiest place a future PR-71 reviewer will see it.
func ValidateAgainstManifest(p Plugin, m *Manifest) error {
	if p == nil || m == nil {
		return fmt.Errorf("%w: nil plugin or manifest", ErrCapabilityMismatch)
	}
	if p.Name() != m.Name {
		return fmt.Errorf("%w: plugin name %q does not match manifest name %q",
			ErrCapabilityMismatch, p.Name(), m.Name)
	}

	checks := []struct {
		capability Capability
		ok         bool
		hint       string
	}{
		{CapSince, implementsSincer(p), "Sincer"},
		{CapAdd, implementsAdder(p), "Adder"},
		{CapComplete, implementsCompleter(p), "Completer"},
		{CapDelete, implementsDeleter(p), "Deleter"},
		{CapUpdate, implementsUpdater(p), "Updater"},
	}
	for _, c := range checks {
		if !m.HasCapability(c.capability) {
			continue
		}
		if !c.ok {
			return fmt.Errorf("%w: manifest declares %q but plugin does not implement %s",
				ErrCapabilityMismatch, c.capability, c.hint)
		}
	}
	return nil
}

// implementsSincer / Adder / Completer / Deleter are tiny helpers so the
// validator's intent reads top-to-bottom. Inlining the type assertion would
// work but the named helpers double as documentation that "Sincer" maps to
// "since" capability.
func implementsSincer(p Plugin) bool    { _, ok := p.(Sincer); return ok }
func implementsAdder(p Plugin) bool     { _, ok := p.(Adder); return ok }
func implementsCompleter(p Plugin) bool { _, ok := p.(Completer); return ok }
func implementsDeleter(p Plugin) bool   { _, ok := p.(Deleter); return ok }
func implementsUpdater(p Plugin) bool   { _, ok := p.(Updater); return ok }
