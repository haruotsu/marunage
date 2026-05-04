package notion

import (
	"context"
	"strings"
)

// fakeClient is the in-memory Client every test in this package uses. It
// lives in a _test.go file (not the production client.go) so the binary
// shipped to users does not carry test fixtures or test-only fields.
// Adapter / builtin / auth tests in sibling _test.go files can still
// reference it because Go compiles all *_test.go files in the same
// package together for `go test`.
type fakeClient struct {
	pages       []Page
	queryErr    error
	usersMeErr  error
	createPage  Page
	createErr   error
	updateErr   error
	updateCalls []updateCall
	createCalls []createCall
}

type updateCall struct {
	pageID   string
	archived bool
}

type createCall struct {
	databaseID string
	title      string
}

func (c *fakeClient) QueryDatabase(_ context.Context, _ string, opts QueryOptions) ([]Page, error) {
	if c.queryErr != nil {
		return nil, c.queryErr
	}
	if opts.OnOrAfter == "" {
		out := make([]Page, len(c.pages))
		copy(out, c.pages)
		return out, nil
	}
	var out []Page
	for _, p := range c.pages {
		// Lex compare on fixed-width ISO-8601 — same property Plugin's
		// checkpoint comparison relies on.
		if strings.Compare(p.LastEditedTime, opts.OnOrAfter) >= 0 {
			out = append(out, p)
		}
	}
	return out, nil
}

func (c *fakeClient) UsersMe(_ context.Context) error { return c.usersMeErr }

func (c *fakeClient) CreatePage(_ context.Context, databaseID, title string) (Page, error) {
	c.createCalls = append(c.createCalls, createCall{databaseID: databaseID, title: title})
	if c.createErr != nil {
		return Page{}, c.createErr
	}
	if c.createPage.ID != "" {
		return c.createPage, nil
	}
	return Page{ID: "fake-" + title, Title: title, DatabaseID: databaseID}, nil
}

func (c *fakeClient) UpdatePage(_ context.Context, pageID string, archived bool) error {
	c.updateCalls = append(c.updateCalls, updateCall{pageID: pageID, archived: archived})
	return c.updateErr
}
