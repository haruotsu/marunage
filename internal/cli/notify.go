package cli

import (
	"context"
	"fmt"
	"io"

	"github.com/spf13/cobra"

	"github.com/haruotsu/marunage/internal/config"
	"github.com/haruotsu/marunage/internal/store"
)

// Notifier delivers one task notification. The default implementation writes
// to stdout; a real channel (Slack DM, etc.) is injected via notifierHook.
type Notifier interface {
	Notify(ctx context.Context, message string) error
}

// notifierHook lets tests (and a future channel-backed delivery) substitute
// the default stdout notifier.
var notifierHook Notifier

func activeNotifier(out io.Writer) Notifier {
	if notifierHook != nil {
		return notifierHook
	}
	return writerNotifier{w: out}
}

// writerNotifier is the default delivery: it prints the notification so
// `marunage notify` is useful out of the box even before a Slack/email channel
// is wired. (No universal DM target exists across sources, so a real channel
// needs per-task routing — tracked as follow-up.)
type writerNotifier struct{ w io.Writer }

func (n writerNotifier) Notify(_ context.Context, message string) error {
	_, err := fmt.Fprintln(n.w, message)
	return err
}

// newNotifyCmd builds `marunage notify`: surface every task in a terminal
// state the operator asked to hear about. Toggled by [notify].on_complete /
// on_failure; waiting_human is always surfaced because it needs a human.
func newNotifyCmd(configPath *string) *cobra.Command {
	return &cobra.Command{
		Use:          "notify",
		Short:        "Send completion / failure / waiting_human notifications.",
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runNotify(cmd.Context(), *configPath, cmd.OutOrStdout())
		},
	}
}

func runNotify(ctx context.Context, configPath string, out io.Writer) error {
	cfg, err := config.Load(configPath)
	if err != nil {
		return fmt.Errorf("load %s: %w", configPath, err)
	}
	statuses := []string{store.StatusWaitingHuman}
	if cfg.Notify.OnComplete {
		statuses = append(statuses, store.StatusDone)
	}
	if cfg.Notify.OnFailure {
		statuses = append(statuses, store.StatusFailed)
	}

	repo, closer, err := activeTaskRepoFactory()(ctx, configPath)
	if err != nil {
		return err
	}
	defer func() { _ = closer() }()

	rows, err := repo.List(ctx, store.ListFilter{Statuses: statuses})
	if err != nil {
		return translateRepoError(err)
	}

	n := activeNotifier(out)
	for _, t := range rows {
		if err := n.Notify(ctx, fmt.Sprintf("[%s] #%d %s", t.Status, t.ID, t.Title)); err != nil {
			return fmt.Errorf("notify task #%d: %w", t.ID, err)
		}
	}
	_, _ = fmt.Fprintf(out, "Sent %d notification(s).\n", len(rows))
	return nil
}
