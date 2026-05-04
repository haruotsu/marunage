package notion

import (
	"context"
	"errors"
	"testing"
)

// TestFakeClientReturnsConfiguredPages locks in the contract the Plugin tests
// build on: a fake Client returns whatever pages the test seeded, in order,
// so List/Since assertions stay decoupled from real HTTP.
func TestFakeClientReturnsConfiguredPages(t *testing.T) {
	t.Parallel()

	pages := []Page{
		{ID: "page-1", Title: "alpha"},
		{ID: "page-2", Title: "beta"},
	}
	c := &fakeClient{pages: pages}

	got, err := c.QueryDatabase(context.Background(), "db-1", QueryOptions{})
	if err != nil {
		t.Fatalf("QueryDatabase: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("len = %d", len(got))
	}
	if got[0].ID != "page-1" || got[1].ID != "page-2" {
		t.Errorf("order broken: %+v", got)
	}
}

// TestFakeClientReturnsConfiguredError lets List/Since tests assert error
// propagation without spinning up an httptest.Server. The fake forwards a
// pre-loaded error verbatim so callers can branch on errors.Is.
func TestFakeClientReturnsConfiguredError(t *testing.T) {
	t.Parallel()

	want := errors.New("boom")
	c := &fakeClient{queryErr: want}

	_, err := c.QueryDatabase(context.Background(), "db", QueryOptions{})
	if !errors.Is(err, want) {
		t.Fatalf("err = %v, want %v", err, want)
	}
}

// TestFakeClientUsersMeReportsAuth lets AuthStatus tests stub the Notion
// "/v1/users/me" smoke probe. Three cases: success, ErrUnauthorized (token
// revoked), ErrTokenExpired (OAuth expiry).
func TestFakeClientUsersMeReportsAuth(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		set  func(c *fakeClient)
		want error
	}{
		{name: "ok", set: func(c *fakeClient) {}, want: nil},
		{name: "revoked", set: func(c *fakeClient) { c.usersMeErr = ErrUnauthorized }, want: ErrUnauthorized},
		{name: "expired", set: func(c *fakeClient) { c.usersMeErr = ErrTokenExpired }, want: ErrTokenExpired},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			c := &fakeClient{}
			tc.set(c)
			err := c.UsersMe(context.Background())
			if !errors.Is(err, tc.want) {
				t.Fatalf("err = %v, want %v", err, tc.want)
			}
		})
	}
}

// TestFakeClientFiltersByOnOrAfter exercises the Since-side filter: when
// QueryOptions.OnOrAfter is set, the fake returns only pages whose
// LastEditedTime is at or after the bound. Decoupling this from the real
// API keeps Since tests deterministic — we are not asserting Notion's filter
// behaviour, we are asserting the Plugin asks for the right window.
func TestFakeClientFiltersByOnOrAfter(t *testing.T) {
	t.Parallel()

	pages := []Page{
		{ID: "old", LastEditedTime: "2024-01-01T00:00:00.000Z"},
		{ID: "mid", LastEditedTime: "2024-06-01T00:00:00.000Z"},
		{ID: "new", LastEditedTime: "2025-01-01T00:00:00.000Z"},
	}
	c := &fakeClient{pages: pages}

	got, err := c.QueryDatabase(context.Background(), "db", QueryOptions{OnOrAfter: "2024-06-01T00:00:00.000Z"})
	if err != nil {
		t.Fatalf("QueryDatabase: %v", err)
	}
	if len(got) != 2 || got[0].ID != "mid" || got[1].ID != "new" {
		t.Fatalf("filter returned %+v", got)
	}
}
