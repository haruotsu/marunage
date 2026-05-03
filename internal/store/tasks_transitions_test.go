package store_test

import (
	"context"
	"errors"
	"testing"

	"github.com/haruotsu/marunage/internal/store"
)

// PR-21 introduces two new repo entry points layered on top of UpdateStatus
// and a typed sentinel:
//
//   - TransitionStatus(id, newStatus) is the policy-aware sibling of
//     UpdateStatus. It enforces the matrix in docs/pr_split_plan.md PR-21
//     (done/fail/promote/reopen) so the CLI cannot accept an illegal
//     transition by mistake. Lifecycle moves owned by other PRs (PR-42
//     pending->running, PR-41 *->waiting_human) stay outside this method's
//     domain and continue to call UpdateStatus directly.
//   - Delete(id) drops the row regardless of status; `marunage rm <id>`
//     wraps it. Missing id surfaces ErrNotFound.
//
// ErrInvalidTransition is the new sentinel TransitionStatus returns when
// the (from, to) pair is not in the allowed set.

// TransitionStatus accepts each documented legal transition.
func TestTaskRepoTransitionStatusAllowsLegal(t *testing.T) {
	cases := []struct {
		name string
		from string
		to   string
	}{
		{"pending->done", store.StatusPending, store.StatusDone},
		{"pending->failed", store.StatusPending, store.StatusFailed},
		{"running->done", store.StatusRunning, store.StatusDone},
		{"running->failed", store.StatusRunning, store.StatusFailed},
		{"waiting_human->done", store.StatusWaitingHuman, store.StatusDone},
		{"waiting_human->failed", store.StatusWaitingHuman, store.StatusFailed},
		{"done->pending (reopen)", store.StatusDone, store.StatusPending},
		{"failed->pending (reopen)", store.StatusFailed, store.StatusPending},
		{"skipped->pending (promote)", store.StatusSkipped, store.StatusPending},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newRepoFixture(t)
			id, err := f.repo.Insert(f.ctx, store.Task{
				Source: "manual",
				Title:  "t",
				Status: tc.from,
			})
			if err != nil {
				t.Fatalf("Insert: %v", err)
			}
			if err := f.repo.TransitionStatus(f.ctx, id, tc.to); err != nil {
				t.Fatalf("TransitionStatus(%s -> %s): %v", tc.from, tc.to, err)
			}
			got, err := f.repo.Get(f.ctx, id)
			if err != nil {
				t.Fatalf("Get: %v", err)
			}
			if got.Status != tc.to {
				t.Errorf("status = %q; want %q", got.Status, tc.to)
			}
		})
	}
}

// TransitionStatus rejects the transitions PR-21 explicitly leaves to other
// PRs (pending->running is PR-42; *->waiting_human is PR-41) and outright
// nonsense moves like done->running.
func TestTaskRepoTransitionStatusRejectsIllegal(t *testing.T) {
	cases := []struct {
		name string
		from string
		to   string
	}{
		{"pending->pending self-noop", store.StatusPending, store.StatusPending},
		{"pending->running (PR-42)", store.StatusPending, store.StatusRunning},
		{"pending->waiting_human (PR-41)", store.StatusPending, store.StatusWaitingHuman},
		{"running->running self-noop", store.StatusRunning, store.StatusRunning},
		{"running->pending", store.StatusRunning, store.StatusPending},
		{"done->running", store.StatusDone, store.StatusRunning},
		{"done->failed", store.StatusDone, store.StatusFailed},
		{"failed->running", store.StatusFailed, store.StatusRunning},
		{"failed->done", store.StatusFailed, store.StatusDone},
		{"skipped->done", store.StatusSkipped, store.StatusDone},
		{"skipped->failed", store.StatusSkipped, store.StatusFailed},
		{"waiting_human->pending", store.StatusWaitingHuman, store.StatusPending},
		{"waiting_human->running", store.StatusWaitingHuman, store.StatusRunning},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newRepoFixture(t)
			id, err := f.repo.Insert(f.ctx, store.Task{
				Source: "manual",
				Title:  "t",
				Status: tc.from,
			})
			if err != nil {
				t.Fatalf("Insert: %v", err)
			}
			err = f.repo.TransitionStatus(f.ctx, id, tc.to)
			if !errors.Is(err, store.ErrInvalidTransition) {
				t.Fatalf("TransitionStatus(%s -> %s): err = %v; want ErrInvalidTransition",
					tc.from, tc.to, err)
			}
			got, err := f.repo.Get(f.ctx, id)
			if err != nil {
				t.Fatalf("Get: %v", err)
			}
			if got.Status != tc.from {
				t.Errorf("status drifted after rejected transition: got %q; want %q",
					got.Status, tc.from)
			}
		})
	}
}

// TransitionStatus on a missing id returns ErrNotFound rather than the
// generic ErrInvalidTransition. The CLI uses this to print a friendly
// "Task #<id> not found." message.
func TestTaskRepoTransitionStatusMissingReturnsErrNotFound(t *testing.T) {
	f := newRepoFixture(t)
	err := f.repo.TransitionStatus(f.ctx, 99999, store.StatusDone)
	if !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("TransitionStatus(missing): err = %v; want ErrNotFound", err)
	}
}

// TransitionStatus rejects unknown status values before reaching policy
// validation so the CLI sees ErrInvalidStatus rather than
// ErrInvalidTransition for typos like "complete".
func TestTaskRepoTransitionStatusRejectsUnknownStatus(t *testing.T) {
	f := newRepoFixture(t)
	id, err := f.repo.Insert(f.ctx, store.Task{Source: "manual", Title: "t"})
	if err != nil {
		t.Fatalf("Insert: %v", err)
	}
	if err := f.repo.TransitionStatus(f.ctx, id, "complete"); !errors.Is(err, store.ErrInvalidStatus) {
		t.Fatalf("TransitionStatus(invalid): err = %v; want ErrInvalidStatus", err)
	}
}

// Delete removes the row regardless of current status. We exercise both an
// active (pending) row and a terminal (done) row so a future "soft delete"
// refactor cannot silently leave done rows behind.
func TestTaskRepoDeleteRemovesRow(t *testing.T) {
	cases := []string{store.StatusPending, store.StatusDone, store.StatusFailed, store.StatusSkipped}
	for _, status := range cases {
		t.Run(status, func(t *testing.T) {
			f := newRepoFixture(t)
			id, err := f.repo.Insert(f.ctx, store.Task{
				Source: "manual",
				Title:  "delete me",
				Status: status,
			})
			if err != nil {
				t.Fatalf("Insert: %v", err)
			}
			if err := f.repo.Delete(f.ctx, id); err != nil {
				t.Fatalf("Delete: %v", err)
			}
			if _, err := f.repo.Get(f.ctx, id); !errors.Is(err, store.ErrNotFound) {
				t.Fatalf("Get after Delete: err = %v; want ErrNotFound", err)
			}
		})
	}
}

// Delete on a missing id returns ErrNotFound. Mirrors UpdateStatus /
// SetWorkspace so a stale id cannot silently no-op the deletion path.
func TestTaskRepoDeleteMissingReturnsErrNotFound(t *testing.T) {
	f := newRepoFixture(t)
	if err := f.repo.Delete(context.Background(), 99999); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("Delete(missing): err = %v; want ErrNotFound", err)
	}
}
