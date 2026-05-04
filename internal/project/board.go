// Package project implements the Project Mode for marunage (PR-101).
// It reads a GitHub Projects board and dispatches tasks one-by-one in
// phase × date order, pausing on [human] tasks until they are marked Done
// on the board.
package project

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// Typed sentinel errors. Callers branch on errors.Is.
var (
	// ErrInvalidBoardURL is returned when the supplied URL does not match
	// the expected GitHub Projects shape.
	ErrInvalidBoardURL = errors.New("project: invalid board URL")

	// ErrInvalidResponse is returned when gh produces output that cannot be
	// parsed as the expected JSON shape.
	ErrInvalidResponse = errors.New("project: invalid gh response")
)

// ParsedURL holds the parsed components of a GitHub Projects board URL.
type ParsedURL struct {
	Owner     string // org or user name
	Number    int    // project number (>= 1)
	OwnerKind string // "orgs" or "users"
}

// BoardItem represents one item on a GitHub Projects board.
type BoardItem struct {
	ID        string
	Title     string
	Status    string
	UpdatedAt time.Time
}

// rawProjectListResponse mirrors the subset of gh project item-list
// --format json output that this package consumes. Extra fields are
// silently ignored so a future gh release that adds new fields cannot
// destabilise the parser.
type rawProjectListResponse struct {
	Items      []rawBoardItem `json:"items"`
	TotalCount int            `json:"totalCount"`
}

type rawBoardItem struct {
	ID        string `json:"id"`
	Title     string `json:"title"`
	Status    string `json:"status"`
	UpdatedAt string `json:"updatedAt"`
}

// ParseBoardURL parses a GitHub Projects board URL into its owner, project
// number, and owner-kind components. Accepted shapes:
//
//	https://github.com/orgs/<org>/projects/<number>
//	https://github.com/users/<user>/projects/<number>
//
// Returns ErrInvalidBoardURL for any deviation from the above, including
// a non-numeric or non-positive project number, so a tampered value
// cannot smuggle additional path segments into the gh invocation.
func ParseBoardURL(rawURL string) (ParsedURL, error) {
	if rawURL == "" {
		return ParsedURL{}, fmt.Errorf("%w: empty URL", ErrInvalidBoardURL)
	}
	u, err := url.Parse(rawURL)
	if err != nil {
		return ParsedURL{}, fmt.Errorf("%w: %v", ErrInvalidBoardURL, err)
	}
	// Expected path: /orgs/<owner>/projects/<number>
	//             or /users/<owner>/projects/<number>
	parts := strings.Split(strings.TrimPrefix(u.Path, "/"), "/")
	if len(parts) < 4 {
		return ParsedURL{}, fmt.Errorf("%w: %q (want /orgs|users/<owner>/projects/<n>)", ErrInvalidBoardURL, rawURL)
	}
	kind := parts[0]
	if kind != "orgs" && kind != "users" {
		return ParsedURL{}, fmt.Errorf("%w: %q (owner kind must be orgs or users, got %q)", ErrInvalidBoardURL, rawURL, kind)
	}
	if parts[2] != "projects" {
		return ParsedURL{}, fmt.Errorf("%w: %q (missing 'projects' path segment)", ErrInvalidBoardURL, rawURL)
	}
	n, parseErr := strconv.Atoi(parts[3])
	if parseErr != nil || n <= 0 {
		return ParsedURL{}, fmt.Errorf("%w: %q (project number must be a positive integer, got %q)", ErrInvalidBoardURL, rawURL, parts[3])
	}
	return ParsedURL{
		Owner:     parts[1],
		Number:    n,
		OwnerKind: kind,
	}, nil
}

// FetchItems retrieves all items from the GitHub Projects board identified
// by parsed, using `gh project item-list` via runner.
func FetchItems(ctx context.Context, runner Runner, parsed ParsedURL) ([]BoardItem, error) {
	stdout, _, err := runner.Run(ctx, "gh",
		"project", "item-list",
		strconv.Itoa(parsed.Number),
		"--owner", parsed.Owner,
		"--format", "json",
	)
	if err != nil {
		return nil, fmt.Errorf("gh project item-list: %w", err)
	}
	var resp rawProjectListResponse
	if jsonErr := json.Unmarshal(stdout, &resp); jsonErr != nil {
		return nil, fmt.Errorf("%w: %w", ErrInvalidResponse, jsonErr)
	}
	items := make([]BoardItem, 0, len(resp.Items))
	for _, raw := range resp.Items {
		var updatedAt time.Time
		if raw.UpdatedAt != "" {
			if t, parseErr := time.Parse(time.RFC3339, raw.UpdatedAt); parseErr == nil {
				updatedAt = t
			}
		}
		items = append(items, BoardItem{
			ID:        raw.ID,
			Title:     raw.Title,
			Status:    raw.Status,
			UpdatedAt: updatedAt,
		})
	}
	return items, nil
}
