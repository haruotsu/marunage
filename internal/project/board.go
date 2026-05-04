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
	"regexp"
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

// validOwnerName matches GitHub's org/user naming rules: starts with an
// alphanumeric character, followed by alphanumerics or hyphens. Accepts
// up to 39 characters (GitHub's documented limit). The leading character
// cannot be a hyphen, which prevents values that gh's flag parser could
// misinterpret as an unknown flag.
var validOwnerName = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9\-]{0,38}$`)

// Board status values as returned by gh project item-list. Defined as
// constants so callers use symbolic names rather than string literals —
// a typo in a comparison is a compile-time error rather than a silent
// logic bug.
const (
	StatusDone       = "Done"
	StatusInProgress = "In Progress"
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
// Returns ErrInvalidBoardURL for any deviation, including:
//   - a host other than github.com (SSRF guard)
//   - an owner name that does not match GitHub's naming rules
//   - a non-numeric or non-positive project number
func ParseBoardURL(rawURL string) (ParsedURL, error) {
	if rawURL == "" {
		return ParsedURL{}, fmt.Errorf("%w: empty URL", ErrInvalidBoardURL)
	}
	u, err := url.Parse(rawURL)
	if err != nil {
		return ParsedURL{}, fmt.Errorf("%w: %v", ErrInvalidBoardURL, err)
	}
	// Reject any host that is not github.com to prevent SSRF: a crafted URL
	// with an attacker-controlled host would still pass the path check and
	// could redirect future API calls to an arbitrary server.
	if u.Host != "github.com" {
		return ParsedURL{}, fmt.Errorf("%w: %q (only github.com is supported, got %q)", ErrInvalidBoardURL, rawURL, u.Host)
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
	owner := parts[1]
	if !validOwnerName.MatchString(owner) {
		return ParsedURL{}, fmt.Errorf("%w: %q (invalid owner name %q)", ErrInvalidBoardURL, rawURL, owner)
	}
	n, parseErr := strconv.Atoi(parts[3])
	if parseErr != nil || n <= 0 {
		return ParsedURL{}, fmt.Errorf("%w: %q (project number must be a positive integer, got %q)", ErrInvalidBoardURL, rawURL, parts[3])
	}
	return ParsedURL{
		Owner:     owner,
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
