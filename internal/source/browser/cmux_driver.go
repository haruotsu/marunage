// cmux_driver.go is the production BrowserDriver implementation that
// shells out to `cmux browser` (see CLAUDE.local.md). The driver runs in
// two steps per Scrape:
//
//  1. `cmux browser goto <url>` — point the existing browser pane at the
//     site we want to extract from.
//  2. `cmux browser eval <js>`  — run a small extraction script that
//     walks the DOM with the configured selectors and prints the
//     resulting items as JSON to stdout.
//
// The driver decodes that JSON into ScrapedItems and lets the plugin
// take over. Tests inject a scripted cmux.Runner so we can assert the
// goto/eval call shapes without spawning a real cmux.
package browser

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/haruotsu/marunage/internal/cmux"
)

// ErrUnparseableEval is returned by CmuxDriver.Scrape when the eval step
// emitted stdout that is not the expected JSON array. Surfaced as a
// typed sentinel so callers can distinguish "site changed shape" from
// "cmux blew up" in metrics / logs.
var ErrUnparseableEval = errors.New("browser: unparseable cmux eval output")

// CmuxDriver is the production BrowserDriver. It is constructed via
// NewCmuxDriver and is safe for concurrent Scrape calls because every
// invocation builds its own per-call argv from the input target.
type CmuxDriver struct {
	runner cmux.Runner
}

// CmuxOption mutates CmuxDriver construction. Mirrors cmux.Option /
// browser.Option so the codebase has one consistent option pattern.
type CmuxOption func(*CmuxDriver)

// WithCmuxRunner injects the cmux.Runner used to invoke `cmux`. Tests
// inject scripted runners; production code lets the default
// cmux.ExecRunner shell out for real.
func WithCmuxRunner(r cmux.Runner) CmuxOption {
	return func(d *CmuxDriver) { d.runner = r }
}

// NewCmuxDriver returns a CmuxDriver wired to cmux.ExecRunner by
// default — production callers can pass it straight to WithDriver and
// scrape against a real cmux browser pane.
func NewCmuxDriver(opts ...CmuxOption) *CmuxDriver {
	d := &CmuxDriver{runner: cmux.ExecRunner{}}
	for _, o := range opts {
		o(d)
	}
	return d
}

// Scrape performs the goto+eval round-trip described in the file
// comment. The eval JS uses document.querySelectorAll to walk every
// item match, then for each one extracts every configured field's text
// or attribute value. The output JSON shape is
// `[ {"id": "...", "title": "..."}, ... ]` keyed by the logical field
// name from the SiteConfig.
func (d *CmuxDriver) Scrape(ctx context.Context, target ScrapeTarget) ([]ScrapedItem, error) {
	if _, _, err := d.runner.Run(ctx, "cmux", "browser", "goto", target.URL); err != nil {
		return nil, fmt.Errorf("cmux browser goto %s: %w", target.URL, err)
	}
	// Re-check ctx between the two steps so a slow goto + concurrent
	// shutdown does not leak a stray eval after the discovery loop has
	// signalled cancellation. The Runner itself honours ctx for the
	// per-call deadline, but only checking inside the runner would
	// still let us issue the second exec call after the parent already
	// gave up.
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	js := buildExtractionJS(target)
	stdout, _, err := d.runner.Run(ctx, "cmux", "browser", "eval", js)
	if err != nil {
		return nil, fmt.Errorf("cmux browser eval (%s): %w", target.URL, err)
	}

	var raw []map[string]string
	if err := json.Unmarshal(stdout, &raw); err != nil {
		return nil, fmt.Errorf("%w: %v: stdout=%q",
			ErrUnparseableEval, err, strings.TrimSpace(string(stdout)))
	}

	out := make([]ScrapedItem, len(raw))
	for i, m := range raw {
		out[i] = ScrapedItem{Fields: m}
	}
	return out, nil
}

// buildExtractionJS assembles the extraction script the eval step runs.
// Field iteration is sorted so the same target produces byte-identical
// JS across calls — diffing logged commands stays stable, and the test
// that asserts every selector appears in the JS does not rely on map
// iteration order.
//
// jsString quotes a Go string into a JS string literal. We deliberately
// re-encode via encoding/json (which is JSON-string-compatible with
// the JS string literal grammar for our inputs: BMP code points,
// straight-quoted) rather than concatenating raw input, so a selector
// containing `"` or `\` cannot break out of the quoted form.
func buildExtractionJS(target ScrapeTarget) string {
	keys := make([]string, 0, len(target.Fields))
	for k := range target.Fields {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	var fieldExprs []string
	for _, k := range keys {
		rule := target.Fields[k]
		var expr string
		switch {
		case rule.Selector == "" && rule.Attr == "":
			expr = "(item.textContent || '').trim()"
		case rule.Selector == "" && rule.Attr != "":
			expr = "(item.getAttribute(" + jsString(rule.Attr) + ") || '')"
		case rule.Attr == "":
			expr = "((item.querySelector(" + jsString(rule.Selector) + ") || {textContent: ''}).textContent || '').trim()"
		default:
			expr = "((item.querySelector(" + jsString(rule.Selector) + ") || {getAttribute: () => ''}).getAttribute(" + jsString(rule.Attr) + ") || '')"
		}
		fieldExprs = append(fieldExprs, jsString(k)+": "+expr)
	}

	return "JSON.stringify(Array.from(document.querySelectorAll(" +
		jsString(target.ItemSelector) + ")).map(function(item) { return {" +
		strings.Join(fieldExprs, ", ") + "}; }))"
}

// jsString returns s as a JS string literal. encoding/json's string
// encoder produces valid JS for our inputs (CSS selectors and short
// attribute names), and crucially escapes `"`, `\`, and control bytes
// so a maliciously-crafted browser.toml cannot inject arbitrary JS.
func jsString(s string) string {
	b, err := json.Marshal(s)
	if err != nil {
		// json.Marshal of a string never fails for valid UTF-8; the
		// fallback is defence in depth.
		return `""`
	}
	return string(b)
}
