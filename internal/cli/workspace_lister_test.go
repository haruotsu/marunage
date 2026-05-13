package cli

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/haruotsu/marunage/internal/workspace"
)

// fakeWorkspaceClient is the workspace.Client stub clientWorkspaceLister
// tests inject. Only ListWorkspaces matters here; the other methods
// panic so a regression that calls them surfaces immediately.
type fakeWorkspaceClient struct {
	listResp []workspace.Workspace
	listErr  error
}

func (f *fakeWorkspaceClient) NewWorkspace(_ context.Context, _ workspace.NewWorkspaceOptions) (workspace.Workspace, error) {
	panic("fakeWorkspaceClient: NewWorkspace must not be called by workspaceLister tests")
}
func (f *fakeWorkspaceClient) WaitReady(_ context.Context, _ workspace.Workspace) error {
	panic("fakeWorkspaceClient: WaitReady must not be called by workspaceLister tests")
}
func (f *fakeWorkspaceClient) Send(_ context.Context, _ workspace.Workspace, _ string) error {
	panic("fakeWorkspaceClient: Send must not be called by workspaceLister tests")
}
func (f *fakeWorkspaceClient) ListWorkspaces(_ context.Context) ([]workspace.Workspace, error) {
	return f.listResp, f.listErr
}
func (f *fakeWorkspaceClient) ReadOutput(_ context.Context, _ workspace.Workspace) (string, error) {
	panic("fakeWorkspaceClient: ReadOutput must not be called by workspaceLister tests")
}

// clientWorkspaceLister extracts the ws.ID from each entry returned by
// the underlying Client.ListWorkspaces. The Workspace.Name field is
// not part of the orphan-diff contract, so the adapter drops it.
func TestClientWorkspaceLister_ProjectsWorkspaceIDs(t *testing.T) {
	fc := &fakeWorkspaceClient{
		listResp: []workspace.Workspace{
			{ID: "workspace:1", Name: "feature/x"},
			{ID: "workspace:2", Name: "feature/y"},
			{ID: "workspace:42", Name: "hotfix"},
		},
	}
	l := clientWorkspaceLister{client: fc}
	ids, err := l.ListWorkspaceIDs(context.Background())
	if err != nil {
		t.Fatalf("ListWorkspaceIDs: %v", err)
	}
	want := []string{"workspace:1", "workspace:2", "workspace:42"}
	if !reflect.DeepEqual(ids, want) {
		t.Errorf("ids = %v; want %v", ids, want)
	}
}

// An empty live set returns a non-nil empty slice so callers can rely
// on len() without a nil check and `range` over the result is uniform
// with the populated case.
func TestClientWorkspaceLister_EmptyReturnsNonNilEmptySlice(t *testing.T) {
	fc := &fakeWorkspaceClient{listResp: []workspace.Workspace{}}
	l := clientWorkspaceLister{client: fc}
	ids, err := l.ListWorkspaceIDs(context.Background())
	if err != nil {
		t.Fatalf("ListWorkspaceIDs: %v", err)
	}
	if ids == nil {
		t.Fatalf("ids = nil; want non-nil empty slice")
	}
	if len(ids) != 0 {
		t.Errorf("len(ids) = %d; want 0", len(ids))
	}
}

// Errors from the underlying client propagate verbatim so the clean
// command can errors.Is against backend-specific sentinels (cmux.
// ErrCmuxNotFound, herdr.ErrHerdrNotFound) without the adapter
// rewrapping them.
func TestClientWorkspaceLister_PropagatesClientErrors(t *testing.T) {
	want := errors.New("backend exploded")
	fc := &fakeWorkspaceClient{listErr: want}
	l := clientWorkspaceLister{client: fc}
	_, err := l.ListWorkspaceIDs(context.Background())
	if !errors.Is(err, want) {
		t.Errorf("err = %v; want errors.Is(err, %v)", err, want)
	}
}
