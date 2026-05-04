// driver.go declares the BrowserDriver abstraction the plugin scrapes
// through. The abstraction is intentionally narrow — Scrape takes a
// fully-resolved ScrapeTarget and returns ScrapedItems — because the
// matching DOM walk lives inside the driver implementation. Tests build a
// fake driver from a literal slice; production wires the cmux-browser
// driver (cmux_driver.go).
package browser

import "context"

// BrowserDriver is the seam between the source plugin and the underlying
// browser stack. One driver instance is shared across every site in the
// configured scrape rule set; drivers must therefore be safe for
// concurrent Scrape calls.
//
// The interface is named BrowserDriver (not Driver) per the PR-200 brief
// in docs/pr_split_plan.md so a future caller searching for the seam in
// grep finds an unambiguous symbol.
type BrowserDriver interface {
	// Scrape navigates to target.URL, locates target.ItemSelector matches,
	// and returns one ScrapedItem per match with target.Fields evaluated
	// against each match. Order MUST mirror DOM order so the plugin's
	// downstream consumers see a stable sequence across runs.
	//
	// Implementations honour ctx (cancel + deadline) so a wedged page does
	// not block the discovery loop indefinitely.
	Scrape(ctx context.Context, target ScrapeTarget) ([]ScrapedItem, error)
}

// FieldRule is the per-field extraction recipe baked into a SiteConfig.
// Selector is evaluated relative to the matched item element. When Attr
// is non-empty the rule reads that DOM attribute; otherwise the element's
// text content is used. A rule with an empty Selector reads from the item
// element itself (handy when the item-selector already targets the value-
// bearing node).
type FieldRule struct {
	Selector string
	Attr     string
}

// ScrapeTarget is the resolved input the driver receives. The plugin
// builds one of these per configured site before each Scrape call so the
// driver does not need to know about TOML, registries, or external IDs.
type ScrapeTarget struct {
	URL          string
	ItemSelector string
	// Fields is keyed by the logical field name ("title", "body", ...).
	// Drivers MUST emit ScrapedItem.Fields with the same key set so the
	// plugin can index by field name without a schema lookup.
	Fields map[string]FieldRule
}

// ScrapedItem is one DOM match's worth of extracted data. Fields is
// keyed by the logical field name from ScrapeTarget.Fields. A field
// whose selector did not match must still appear in the map with an
// empty string value so callers (and tests) do not have to distinguish
// "selector missed" from "selector matched an empty string"; the
// distinction never affects downstream behaviour.
type ScrapedItem struct {
	Fields map[string]string
}
