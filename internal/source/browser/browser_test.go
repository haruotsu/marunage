package browser

import (
	"context"
	"errors"
	"testing"
)

// fakeDriver is the in-memory BrowserDriver used by every browser_test.
// Each Scrape call records the target it received (for assertion) and
// returns a canned per-URL response. Errors keyed by URL let a single
// fake serve both happy-path and error-path tests.
type fakeDriver struct {
	responses map[string][]ScrapedItem
	errors    map[string]error
	calls     []ScrapeTarget
}

func (f *fakeDriver) Scrape(_ context.Context, target ScrapeTarget) ([]ScrapedItem, error) {
	f.calls = append(f.calls, target)
	if err, ok := f.errors[target.URL]; ok {
		return nil, err
	}
	return f.responses[target.URL], nil
}

// helperFakeDriver constructs a fakeDriver pre-populated for the standard
// "two sites" scenario most tests need. Pulled out so each test can focus
// on the behaviour under examination, not the fake plumbing.
func helperFakeDriver() *fakeDriver {
	return &fakeDriver{
		responses: map[string][]ScrapedItem{
			"https://app.slack.com/saved": {
				{Fields: map[string]string{"id": "msg-1", "title": "Slack ping", "body": "see also..."}},
				{Fields: map[string]string{"id": "msg-2", "title": "Another", "body": ""}},
			},
		},
	}
}

// helperConfig is the matching SiteConfig for helperFakeDriver. Defined
// next to the fake so a future selector change touches both at once.
func helperConfig() *Config {
	return &Config{
		Sites: []SiteConfig{
			{
				Name:         "slack-saved",
				URL:          "https://app.slack.com/saved",
				ItemSelector: ".p-saved_msg",
				KeyField:     "id",
				Fields: map[string]FieldRule{
					"id":    {Selector: "[data-id]", Attr: "data-id"},
					"title": {Selector: ".title"},
					"body":  {Selector: ".body"},
				},
			},
		},
	}
}

// TestNewRequiresDriver guards a foot-gun: a Plugin built without a
// driver would panic on first List. We surface the misconfiguration as
// a typed error at construction time.
func TestNewRequiresDriver(t *testing.T) {
	t.Parallel()

	_, err := New(WithConfig(helperConfig()))
	if !errors.Is(err, ErrInvalidPlugin) {
		t.Fatalf("err = %v, want ErrInvalidPlugin", err)
	}
}

// TestNewRequiresConfig is the symmetric check: a driver with no sites
// to scrape is also a misconfiguration, not a silent empty list.
func TestNewRequiresConfig(t *testing.T) {
	t.Parallel()

	_, err := New(WithDriver(helperFakeDriver()))
	if !errors.Is(err, ErrInvalidPlugin) {
		t.Fatalf("err = %v, want ErrInvalidPlugin", err)
	}
}

// TestListReturnsScrapedItems asserts the driver -> source.Task pipeline
// produces one Task per scraped item with stable ExternalIDs and the
// configured field values surfaced into Title / Body.
func TestListReturnsScrapedItems(t *testing.T) {
	t.Parallel()

	p, err := New(WithDriver(helperFakeDriver()), WithConfig(helperConfig()))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	got, err := p.List(context.Background())
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("len = %d", len(got))
	}
	if got[0].Title != "Slack ping" || got[0].Body != "see also..." {
		t.Errorf("task[0] = %+v", got[0])
	}
	if got[1].Title != "Another" || got[1].Body != "" {
		t.Errorf("task[1] = %+v", got[1])
	}
	if got[0].ExternalID == got[1].ExternalID {
		t.Errorf("ExternalIDs collide: %q", got[0].ExternalID)
	}
	if got[0].SourcePath != "https://app.slack.com/saved" {
		t.Errorf("SourcePath = %q", got[0].SourcePath)
	}
}

// TestListExternalIDIsStableAcrossCalls asserts re-scraping the same DOM
// returns the same ExternalID — the load-bearing contract that makes the
// queue's UNIQUE (source, external_id) index work.
func TestListExternalIDIsStableAcrossCalls(t *testing.T) {
	t.Parallel()

	p, err := New(WithDriver(helperFakeDriver()), WithConfig(helperConfig()))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	first, err := p.List(context.Background())
	if err != nil {
		t.Fatalf("List#1: %v", err)
	}
	second, err := p.List(context.Background())
	if err != nil {
		t.Fatalf("List#2: %v", err)
	}
	if len(first) != len(second) {
		t.Fatalf("len drift: %d vs %d", len(first), len(second))
	}
	for i := range first {
		if first[i].ExternalID != second[i].ExternalID {
			t.Errorf("ExternalID drift at %d: %q vs %q", i, first[i].ExternalID, second[i].ExternalID)
		}
	}
}

// TestListMultipleSitesPreservesOrder asserts site declaration order is
// the global ordering of the returned task slice.
func TestListMultipleSitesPreservesOrder(t *testing.T) {
	t.Parallel()

	driver := &fakeDriver{
		responses: map[string][]ScrapedItem{
			"https://a/": {{Fields: map[string]string{"id": "a-1", "title": "A1"}}},
			"https://b/": {{Fields: map[string]string{"id": "b-1", "title": "B1"}}},
		},
	}
	cfg := &Config{Sites: []SiteConfig{
		{Name: "alpha", URL: "https://a/", ItemSelector: ".x", KeyField: "id",
			Fields: map[string]FieldRule{"id": {Selector: "[data-id]", Attr: "data-id"}, "title": {Selector: ".t"}}},
		{Name: "beta", URL: "https://b/", ItemSelector: ".x", KeyField: "id",
			Fields: map[string]FieldRule{"id": {Selector: "[data-id]", Attr: "data-id"}, "title": {Selector: ".t"}}},
	}}
	p, err := New(WithDriver(driver), WithConfig(cfg))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	got, err := p.List(context.Background())
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("len = %d", len(got))
	}
	if got[0].Title != "A1" || got[1].Title != "B1" {
		t.Errorf("order: %q, %q", got[0].Title, got[1].Title)
	}
}

// TestListSourceFieldNamesSite asserts the per-task Source field carries
// "browser:<site-name>" so a downstream UI can route Slack-saved tasks
// differently from GitHub-bookmark tasks even though both ride the same
// plugin.
func TestListSourceFieldNamesSite(t *testing.T) {
	t.Parallel()

	p, err := New(WithDriver(helperFakeDriver()), WithConfig(helperConfig()))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	got, err := p.List(context.Background())
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	for i, tk := range got {
		if tk.Source != "browser:slack-saved" {
			t.Errorf("task[%d].Source = %q", i, tk.Source)
		}
	}
}

// TestListSurfaceSiteAndKeyInRawMetadata asserts the per-task
// RawMetadata carries the site name and the raw DOM key so downstream
// debuggers / `marunage show` can render a "where did this come from"
// link without re-scraping.
func TestListSurfaceSiteAndKeyInRawMetadata(t *testing.T) {
	t.Parallel()

	p, err := New(WithDriver(helperFakeDriver()), WithConfig(helperConfig()))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	got, err := p.List(context.Background())
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(got) == 0 {
		t.Fatal("no tasks")
	}
	meta := got[0].RawMetadata
	if meta["site"] != "slack-saved" {
		t.Errorf("RawMetadata[site] = %v", meta["site"])
	}
	if meta["dom_key"] != "msg-1" {
		t.Errorf("RawMetadata[dom_key] = %v", meta["dom_key"])
	}
}

// TestListStampsOriginTag is the design-review 🔴 #2 fix: every task
// emitted by the browser plugin MUST carry an `origin` tag of the form
// "external/browser/<site>" so downstream LLM/Memory layers can apply
// the SOUL.md "ユーザ指示と区別" guard. Title and Body are attacker-
// controlled DOM text and would otherwise be indistinguishable from
// trusted user-authored task data.
func TestListStampsOriginTag(t *testing.T) {
	t.Parallel()

	p, err := New(WithDriver(helperFakeDriver()), WithConfig(helperConfig()))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	got, err := p.List(context.Background())
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(got) == 0 {
		t.Fatal("no tasks")
	}
	for i, tk := range got {
		if tk.RawMetadata["origin"] != "external/browser/slack-saved" {
			t.Errorf("task[%d].RawMetadata[origin] = %v, want external/browser/slack-saved",
				i, tk.RawMetadata["origin"])
		}
	}
}

// TestListMultiSitePartialFailureReturnsNothing pins the design judgment
// §H: when one of several sites fails, the whole List call returns
// (nil, err) — successful sites are NOT partially returned, so a
// transient failure does not silently produce a smaller-than-expected
// task list. The single-site error test (TestListDriverErrorPropagates)
// covers the trivial case; this one guards the multi-site invariant
// against a future "return what we have" refactor.
func TestListMultiSitePartialFailureReturnsNothing(t *testing.T) {
	t.Parallel()

	wantErr := errors.New("site B is wedged")
	driver := &fakeDriver{
		responses: map[string][]ScrapedItem{
			"https://a/": {{Fields: map[string]string{"id": "a-1", "title": "A"}}},
		},
		errors: map[string]error{"https://b/": wantErr},
	}
	cfg := &Config{Sites: []SiteConfig{
		{Name: "alpha", URL: "https://a/", ItemSelector: ".x", KeyField: "id",
			Fields: map[string]FieldRule{"id": {Selector: "[data-id]", Attr: "data-id"}}},
		{Name: "beta", URL: "https://b/", ItemSelector: ".x", KeyField: "id",
			Fields: map[string]FieldRule{"id": {Selector: "[data-id]", Attr: "data-id"}}},
	}}
	p, err := New(WithDriver(driver), WithConfig(cfg))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	got, err := p.List(context.Background())
	if !errors.Is(err, wantErr) {
		t.Fatalf("err = %v, want wraps %v", err, wantErr)
	}
	if got != nil {
		t.Errorf("partial result leaked: %+v", got)
	}
}

// TestListDriverErrorPropagates ensures we do not swallow driver errors:
// a wedged Slack page must surface as a List error, not as silent data
// loss in the discovery loop.
func TestListDriverErrorPropagates(t *testing.T) {
	t.Parallel()

	wantErr := errors.New("boom")
	driver := &fakeDriver{errors: map[string]error{"https://a/": wantErr}}
	cfg := &Config{Sites: []SiteConfig{
		{Name: "alpha", URL: "https://a/", ItemSelector: ".x", KeyField: "id",
			Fields: map[string]FieldRule{"id": {Selector: "[data-id]", Attr: "data-id"}}},
	}}
	p, err := New(WithDriver(driver), WithConfig(cfg))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	_, err = p.List(context.Background())
	if !errors.Is(err, wantErr) {
		t.Fatalf("err = %v, want wraps boom", err)
	}
}

// TestListSkipsItemMissingKeyField asserts an item the driver returned
// without a value for the configured key_field is dropped (not failed
// hard) so a single oddball DOM row does not blow up the whole sync.
// The skip is logged at debug elsewhere; the contract here is "do not
// emit a task with empty ExternalID".
func TestListSkipsItemMissingKeyField(t *testing.T) {
	t.Parallel()

	driver := &fakeDriver{
		responses: map[string][]ScrapedItem{
			"https://a/": {
				{Fields: map[string]string{"id": "ok-1", "title": "good"}},
				{Fields: map[string]string{"id": "", "title": "nokey"}},
			},
		},
	}
	cfg := &Config{Sites: []SiteConfig{
		{Name: "alpha", URL: "https://a/", ItemSelector: ".x", KeyField: "id",
			Fields: map[string]FieldRule{"id": {Selector: "[data-id]", Attr: "data-id"}, "title": {Selector: ".t"}}},
	}}
	p, err := New(WithDriver(driver), WithConfig(cfg))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	got, err := p.List(context.Background())
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("len = %d, want 1", len(got))
	}
	if got[0].Title != "good" {
		t.Errorf("kept the wrong item: %+v", got[0])
	}
}

// TestListPassesScrapeTargetToDriver asserts the per-site config flows
// into the driver call verbatim — without this, the driver could be
// reading the wrong URL or selectors for one of the sites and we would
// not notice in the higher-level tests.
func TestListPassesScrapeTargetToDriver(t *testing.T) {
	t.Parallel()

	driver := helperFakeDriver()
	p, err := New(WithDriver(driver), WithConfig(helperConfig()))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if _, err := p.List(context.Background()); err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(driver.calls) != 1 {
		t.Fatalf("calls = %d", len(driver.calls))
	}
	got := driver.calls[0]
	if got.URL != "https://app.slack.com/saved" {
		t.Errorf("URL = %q", got.URL)
	}
	if got.ItemSelector != ".p-saved_msg" {
		t.Errorf("ItemSelector = %q", got.ItemSelector)
	}
	if got.Fields["id"].Attr != "data-id" {
		t.Errorf("Fields[id] = %+v", got.Fields["id"])
	}
}

// TestSetupIsNoop asserts Setup returns nil for an in-memory config —
// the brief positions the browser source as "configure once, scrape
// many" and there is no upstream credential to register.
func TestSetupIsNoop(t *testing.T) {
	t.Parallel()

	p, err := New(WithDriver(helperFakeDriver()), WithConfig(helperConfig()))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := p.Setup(context.Background()); err != nil {
		t.Errorf("Setup: %v", err)
	}
}

// TestContextCancellation asserts a cancelled context aborts the scrape
// before invoking the driver — a wedged List call must respect the
// cancellation signal the discovery loop sends on shutdown.
func TestContextCancellation(t *testing.T) {
	t.Parallel()

	driver := helperFakeDriver()
	p, err := New(WithDriver(driver), WithConfig(helperConfig()))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := p.List(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
	if len(driver.calls) != 0 {
		t.Errorf("driver should not be called after cancel: %d", len(driver.calls))
	}
}
