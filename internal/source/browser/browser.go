package browser

import (
	"context"
	"errors"
	"fmt"

	"github.com/haruotsu/marunage/internal/source"
)

// ErrInvalidPlugin is the typed sentinel returned by New when a required
// option (Driver / Config) is missing. Surfaced as a sentinel so callers
// composing the plugin in a startup hook can branch on errors.Is rather
// than parsing strings.
var ErrInvalidPlugin = errors.New("browser: invalid plugin configuration")

// pluginName is the canonical Source prefix every Task carries. The
// per-site name is appended ("browser:slack-saved") so a downstream UI
// can route per-site without a manifest lookup.
const pluginName = "browser"

// Plugin is the source-side view of one or more configured DOM scrape
// targets. Construct one with New(opts...) and reuse it across List
// calls: the struct is stateless apart from the immutable driver and
// config references, so it is naturally safe for concurrent use.
type Plugin struct {
	driver BrowserDriver
	config *Config
}

// Option mutates Plugin construction. The functional-option shape mirrors
// internal/cmux and internal/source/markdown so callers see a consistent
// style across the source ecosystem.
type Option func(*Plugin)

// WithDriver injects the BrowserDriver. Tests pass a fake; production
// wires NewCmuxDriver (cmux_driver.go).
func WithDriver(d BrowserDriver) Option {
	return func(p *Plugin) { p.driver = d }
}

// WithConfig injects the parsed scrape-rule config. Typically loaded via
// LoadConfig at startup; tests build a literal *Config in-memory.
func WithConfig(c *Config) Option {
	return func(p *Plugin) { p.config = c }
}

// New constructs a Plugin and rejects any combination missing a
// mandatory option. We validate at construction (rather than first List)
// so a misconfiguration shows up in main()'s wiring code, not three
// minutes into a discovery cycle.
func New(opts ...Option) (*Plugin, error) {
	p := &Plugin{}
	for _, o := range opts {
		o(p)
	}
	if p.driver == nil {
		return nil, fmt.Errorf("%w: missing BrowserDriver (WithDriver)", ErrInvalidPlugin)
	}
	if p.config == nil || len(p.config.Sites) == 0 {
		return nil, fmt.Errorf("%w: missing or empty Config (WithConfig)", ErrInvalidPlugin)
	}
	return p, nil
}

// List scrapes every configured site through the injected driver and
// returns one source.Task per item. Order is (site declaration order,
// DOM order within site). Items whose DOM key extraction came back empty
// are dropped — a task with no stable ExternalID would defeat the
// queue's UNIQUE index, so emitting one would be worse than skipping it.
//
// On any per-site driver error the whole call returns; we deliberately
// do NOT swallow per-site failures because the operator wants to know a
// site is down, not silently see a smaller task list than yesterday.
func (p *Plugin) List(ctx context.Context) ([]source.Task, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	var out []source.Task
	for _, site := range p.config.Sites {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		target := ScrapeTarget{
			URL:          site.URL,
			ItemSelector: site.ItemSelector,
			Fields:       site.Fields,
		}
		items, err := p.driver.Scrape(ctx, target)
		if err != nil {
			return nil, fmt.Errorf("scrape %s (%s): %w", site.Name, site.URL, err)
		}
		for _, item := range items {
			key := item.Fields[site.KeyField]
			if key == "" {
				continue
			}
			task := source.Task{
				Source:     pluginName + ":" + site.Name,
				ExternalID: computeExternalID(site.URL, key),
				Title:      item.Fields["title"],
				Body:       item.Fields["body"],
				SourcePath: site.URL,
				RawMetadata: map[string]any{
					"site":    site.Name,
					"dom_key": key,
					// origin tags every browser-sourced task as untrusted
					// external input. Downstream LLM / Memory layers MUST
					// branch on this so attacker-controlled DOM text
					// (Title / Body) cannot be confused with user-authored
					// task data — the time-delayed prompt-injection
					// surface OpenClaw §11.1-8 calls out.
					"origin": "external/browser/" + site.Name,
				},
			}
			out = append(out, task)
		}
	}
	return out, nil
}

// Setup is a no-op for the browser source: the on-disk browser.toml is
// the source of truth for "what to scrape", and the driver itself does
// not carry a credential the plugin can mint. Returning nil keeps the
// `marunage discover --setup` flow uniform across all sources.
func (p *Plugin) Setup(_ context.Context) error {
	return nil
}

// AuthStatus is constant for the browser source. The cmux driver
// inherits cmux's own auth posture (a logged-in browser session managed
// outside marunage); the plugin itself has no notion of credential
// expiry. Returning anything other than authenticated would force
// callers to re-run setup for what is fundamentally a session-state
// problem outside our control.
func (p *Plugin) AuthStatus(_ context.Context) (source.AuthStatus, error) {
	return source.AuthAuthenticated, nil
}
