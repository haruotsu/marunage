package project

import (
	"context"
	"errors"
	"testing"
	"time"
)

// fakeRunner captures gh invocations and returns canned output. Shared
// by board_test and project_test via the same package.
type fakeRunner struct {
	stdout []byte
	err    error
	// recorded holds the last argv so tests can assert what was called.
	recorded []string
}

func (f *fakeRunner) Run(_ context.Context, name string, args ...string) ([]byte, []byte, error) {
	f.recorded = append([]string{name}, args...)
	return f.stdout, nil, f.err
}

func TestParseBoardURL(t *testing.T) {
	tests := []struct {
		name      string
		rawURL    string
		wantOwner string
		wantNum   int
		wantKind  string
		wantErr   bool
	}{
		{
			name:      "org project URL",
			rawURL:    "https://github.com/orgs/myorg/projects/5",
			wantOwner: "myorg",
			wantNum:   5,
			wantKind:  "orgs",
		},
		{
			name:      "user project URL",
			rawURL:    "https://github.com/users/alice/projects/2",
			wantOwner: "alice",
			wantNum:   2,
			wantKind:  "users",
		},
		{
			name:    "no projects segment",
			rawURL:  "https://github.com/myorg/repo",
			wantErr: true,
		},
		{
			name:    "non-numeric project number",
			rawURL:  "https://github.com/orgs/myorg/projects/abc",
			wantErr: true,
		},
		{
			name:    "zero project number",
			rawURL:  "https://github.com/orgs/myorg/projects/0",
			wantErr: true,
		},
		{
			name:    "wrong owner kind",
			rawURL:  "https://github.com/repos/myorg/projects/5",
			wantErr: true,
		},
		{
			name:    "empty URL",
			rawURL:  "",
			wantErr: true,
		},
		{
			name:    "non-github.com host (SSRF guard)",
			rawURL:  "https://attacker.example.com/orgs/evil/projects/1",
			wantErr: true,
		},
		{
			name:    "http scheme rejected",
			rawURL:  "http://github.com/orgs/myorg/projects/5",
			wantErr: true,
		},
		{
			name:    "javascript scheme rejected",
			rawURL:  "javascript://github.com/orgs/evil/projects/1",
			wantErr: true,
		},
		{
			name:    "no scheme",
			rawURL:  "github.com/orgs/myorg/projects/5",
			wantErr: true,
		},
		{
			name:    "owner name with special characters",
			rawURL:  "https://github.com/orgs/evil; rm -rf/projects/1",
			wantErr: true,
		},
		{
			name:    "owner name starting with hyphen",
			rawURL:  "https://github.com/orgs/-invalid/projects/1",
			wantErr: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ParseBoardURL(tc.rawURL)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("ParseBoardURL(%q) = nil error, want error", tc.rawURL)
				}
				if !errors.Is(err, ErrInvalidBoardURL) {
					t.Errorf("ParseBoardURL(%q) error = %v; want ErrInvalidBoardURL", tc.rawURL, err)
				}
				return
			}
			if err != nil {
				t.Fatalf("ParseBoardURL(%q) = %v, want nil", tc.rawURL, err)
			}
			if got.Owner != tc.wantOwner {
				t.Errorf("Owner = %q, want %q", got.Owner, tc.wantOwner)
			}
			if got.Number != tc.wantNum {
				t.Errorf("Number = %d, want %d", got.Number, tc.wantNum)
			}
			if got.OwnerKind != tc.wantKind {
				t.Errorf("OwnerKind = %q, want %q", got.OwnerKind, tc.wantKind)
			}
		})
	}
}

func TestFetchItems(t *testing.T) {
	const sampleJSON = `{
		"items": [
			{
				"id": "PVTI_1",
				"title": "Task A",
				"status": "Todo",
				"updatedAt": "2024-01-15T10:00:00Z"
			},
			{
				"id": "PVTI_2",
				"title": "Task B [human]",
				"status": "In Progress",
				"updatedAt": "2024-01-16T10:00:00Z"
			}
		],
		"totalCount": 2
	}`

	runner := &fakeRunner{stdout: []byte(sampleJSON)}
	parsed := ParsedURL{Owner: "myorg", Number: 5, OwnerKind: "orgs"}
	items, err := FetchItems(context.Background(), runner, parsed)
	if err != nil {
		t.Fatalf("FetchItems: %v", err)
	}
	if len(items) != 2 {
		t.Fatalf("len(items) = %d, want 2", len(items))
	}

	if items[0].ID != "PVTI_1" {
		t.Errorf("items[0].ID = %q, want PVTI_1", items[0].ID)
	}
	if items[0].Title != "Task A" {
		t.Errorf("items[0].Title = %q, want Task A", items[0].Title)
	}
	if items[0].Status != "Todo" {
		t.Errorf("items[0].Status = %q, want Todo", items[0].Status)
	}
	wantTime := time.Date(2024, 1, 15, 10, 0, 0, 0, time.UTC)
	if !items[0].UpdatedAt.Equal(wantTime) {
		t.Errorf("items[0].UpdatedAt = %v, want %v", items[0].UpdatedAt, wantTime)
	}

	// Verify the gh command was called correctly.
	if len(runner.recorded) < 2 {
		t.Fatalf("runner.recorded = %v, want at least gh + project", runner.recorded)
	}
	if runner.recorded[0] != "gh" {
		t.Errorf("runner.recorded[0] = %q, want gh", runner.recorded[0])
	}
}

func TestFetchItemsRunnerError(t *testing.T) {
	runner := &fakeRunner{err: errors.New("gh: not found")}
	parsed := ParsedURL{Owner: "myorg", Number: 5, OwnerKind: "orgs"}
	_, err := FetchItems(context.Background(), runner, parsed)
	if err == nil {
		t.Fatal("FetchItems with runner error: want error, got nil")
	}
}

func TestFetchItemsInvalidJSON(t *testing.T) {
	runner := &fakeRunner{stdout: []byte("not-json")}
	parsed := ParsedURL{Owner: "myorg", Number: 5, OwnerKind: "orgs"}
	_, err := FetchItems(context.Background(), runner, parsed)
	if !errors.Is(err, ErrInvalidResponse) {
		t.Errorf("FetchItems invalid JSON: got %v, want wrapping ErrInvalidResponse", err)
	}
}

func TestFetchItemsEmptyBoard(t *testing.T) {
	runner := &fakeRunner{stdout: []byte(`{"items":[],"totalCount":0}`)}
	parsed := ParsedURL{Owner: "myorg", Number: 1, OwnerKind: "users"}
	items, err := FetchItems(context.Background(), runner, parsed)
	if err != nil {
		t.Fatalf("FetchItems empty board: %v", err)
	}
	if len(items) != 0 {
		t.Errorf("len(items) = %d, want 0", len(items))
	}
}
