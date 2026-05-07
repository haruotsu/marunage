package cli

import (
	"context"
	"errors"
	"fmt"
	"strconv"

	"github.com/spf13/cobra"

	"github.com/haruotsu/marunage/internal/store"
)

// parseTaskID parses the positional `<id>` argument every transition
// subcommand takes. Rejecting non-numeric / non-positive ids before
// touching the DB lets the user see the diagnostic before any side effect.
func parseTaskID(arg string) (int64, error) {
	id, err := strconv.ParseInt(arg, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("id %q: must be an integer", arg)
	}
	if id <= 0 {
		return 0, fmt.Errorf("id %q: must be a positive integer", arg)
	}
	return id, nil
}

// errTaskCommandFailed is the sentinel returned after the friendly
// "Task #<id> not found." or similar message has already been printed.
// SilenceErrors stays on so cobra does not double-print the line; the
// non-nil return still drives exit 1.
var errTaskCommandFailed = errors.New("task command failed")

// printNotFoundAndExit prints the "Task #<id> not found." line on stderr
// and silences cobra's own error banner. The caller should return
// errTaskCommandFailed afterwards so cobra still exits 1.
func printNotFoundAndExit(cmd *cobra.Command, id int64) {
	cmd.SilenceErrors = true
	fmt.Fprintf(cmd.ErrOrStderr(), "Task #%d not found.\n", id)
}

// mirrorHook is the function shape transitionRunner takes for "what to do
// after the local store mutation succeeds". The runner loads the post-
// mutation row and passes it to the hook so each command can fire the
// right Mirror entry point.
type mirrorHook func(ctx context.Context, mirror Mirror, t store.Task) error

// transitionRunner is the shared implementation of the four transition
// subcommands (done / fail / promote / reopen). It:
//
//  1. Parses the id positional.
//  2. Opens the repo + mirror via the package-private factories so tests
//     can inject fakes.
//  3. Loads the row, checks the current status against allowedFrom (an
//     empty set means "rely on the store-side matrix only"), and rejects
//     with ErrInvalidTransition when the command's narrower precondition
//     fails. This is how `promote` and `reopen` differentiate even though
//     both ultimately drive the row to pending.
//  4. Calls TransitionStatus(id, target). Translates ErrNotFound into the
//     friendly "Task #<id> not found." message and ErrInvalidTransition
//     into the "cannot transition: from X to Y" diagnostic.
//  5. Reloads the row (now carrying the new status) and fires the mirror
//     hook. Mirror errors are surfaced as a non-zero exit but do NOT roll
//     back the local mutation; the local store is the source of truth and
//     mirror sync is best-effort.
//  6. Prints a confirmation line on stdout.
//
// rm is implemented separately because it deletes rather than transitions
// and needs the pre-delete snapshot for the mirror hook.
func transitionRunner(
	cmd *cobra.Command,
	args []string,
	configPath string,
	target string,
	allowedFrom map[string]bool,
	hook mirrorHook,
) error {
	id, err := parseTaskID(args[0])
	if err != nil {
		return err
	}

	repo, closer, err := activeTaskRepoFactory()(cmd.Context(), configPath)
	if err != nil {
		return err
	}
	defer func() { _ = closer() }()

	if len(allowedFrom) > 0 {
		current, err := repo.Get(cmd.Context(), id)
		if err != nil {
			if errors.Is(err, store.ErrNotFound) {
				printNotFoundAndExit(cmd, id)
				return errTaskCommandFailed
			}
			return translateRepoError(err)
		}
		if !allowedFrom[current.Status] {
			return fmt.Errorf("cannot transition: %w: from %q to %q",
				store.ErrInvalidTransition, current.Status, target)
		}
	}

	if err := repo.TransitionStatus(cmd.Context(), id, target); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			printNotFoundAndExit(cmd, id)
			return errTaskCommandFailed
		}
		return translateRepoError(err)
	}

	// When a row returns to pending (reopen / promote), clear the stale
	// workspace reference so the next dispatch can call ClaimWorkspace.
	// ClaimWorkspace requires ws IS NULL or '', so leaving workspace:NNN
	// here would silently block every subsequent dispatch attempt.
	if target == store.StatusPending {
		_ = repo.SetWorkspace(cmd.Context(), id, "")
	}

	task, err := repo.Get(cmd.Context(), id)
	if err != nil {
		return translateRepoError(err)
	}

	mirror, err := activeMirrorFactory()(cmd.Context(), configPath)
	if err != nil {
		return fmt.Errorf("mirror: %w", err)
	}
	if err := hook(cmd.Context(), mirror, task); err != nil {
		// The local transition has already happened. Surface the mirror
		// error so the operator knows the upstream is stale, but keep the
		// local store consistent (no rollback).
		return fmt.Errorf("mirror sync: %w", err)
	}

	fmt.Fprintf(cmd.OutOrStdout(), "Task #%d -> %s\n", id, task.Status)
	return nil
}
