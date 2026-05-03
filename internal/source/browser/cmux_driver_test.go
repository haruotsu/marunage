package browser

import (
	"context"
	"encoding/json"
	"errors"
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

func sliceContains(xs []string, want string) bool {
	for _, x := range xs {
		if x == want {
			return true
		}
	}
	return false
}
