package cli

import (
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	"github.com/haruotsu/marunage/internal/config"
	"github.com/haruotsu/marunage/internal/journal"
	"github.com/haruotsu/marunage/internal/store"
)

func newJournalCmd(configPath *string) *cobra.Command {
	cmd := &cobra.Command{
		Use:          "journal",
		Short:        "Work journal commands.",
		SilenceUsage: true,
	}
	cmd.AddCommand(newJournalStartCmd(configPath))
	cmd.AddCommand(newJournalExportCmd(configPath))
	return cmd
}

func newJournalStartCmd(configPath *string) *cobra.Command {
	var (
		once     bool
		interval time.Duration
	)
	cmd := &cobra.Command{
		Use:   "start",
		Short: "Start the work journal daemon (collects activity every 30m).",
		Long: "marunage journal start runs a background loop that collects activity\n" +
			"from git, marunage tasks, GitHub, and other configured sources every\n" +
			"interval and appends entries to ~/.marunage/journal/YYYY-MM-DD.md.\n" +
			"\n" +
			"--once runs a single collection and exits.",
		SilenceUsage: true,
		Args:         cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			cfg, err := config.Load(*configPath)
			if err != nil {
				return fmt.Errorf("load %s: %w", *configPath, err)
			}
			if !cfg.Journal.Enabled {
				fmt.Fprintln(cmd.OutOrStdout(), "journal.enabled = false in config; nothing to do")
				return nil
			}

			dbPath, err := expandHome(cfg.Core.DBPath)
			if err != nil {
				return fmt.Errorf("resolve core.db_path: %w", err)
			}
			db, err := store.Open(dbPath)
			if err != nil {
				return fmt.Errorf("open %s: %w", dbPath, err)
			}
			defer func() { _ = db.Close() }()
			repo := store.NewTaskRepo(db)

			journalDir := filepath.Join(filepath.Dir(dbPath), "journal")
			w := journal.NewWriter(journalDir)

			iv := interval
			if !cmd.Flags().Changed("interval") {
				iv, err = time.ParseDuration(cfg.Journal.Interval)
				if err != nil {
					return fmt.Errorf("parse journal.interval %q: %w", cfg.Journal.Interval, err)
				}
			}

			collectors := buildCollectors(cfg.Journal.Sources, repo)
			j := journal.New(w,
				journal.WithCollectors(collectors...),
				journal.WithInterval(iv),
			)

			if once {
				return j.Tick(cmd.Context())
			}

			ctx, stop := signal.NotifyContext(cmd.Context(), syscall.SIGINT, syscall.SIGTERM)
			defer stop()
			return j.Run(ctx, iv)
		},
	}
	cmd.Flags().BoolVar(&once, "once", false, "Run a single collection and exit.")
	cmd.Flags().DurationVar(&interval, "interval", 0, "Collection interval (defaults to journal.interval in config).")
	return cmd
}

// buildCollectors constructs Collector instances for the named sources.
// Sources not yet implemented (slack, calendar) are skipped with a WARN log.
// Unrecognised names are also warned and skipped.
func buildCollectors(sources []string, repo *store.TaskRepo) []journal.Collector {
	var out []journal.Collector
	for _, src := range sources {
		switch src {
		case "git":
			out = append(out, journal.NewGitCollector())
		case "marunage":
			out = append(out, journal.NewTaskCollector(repo))
		case "github":
			out = append(out, journal.NewGitHubCollector())
		case "slack", "calendar":
			slog.Warn("journal: source not yet implemented, skipping", "source", src)
		default:
			slog.Warn("journal: unknown source in config, skipping", "source", src)
		}
	}
	return out
}

func newJournalExportCmd(configPath *string) *cobra.Command {
	var date string
	cmd := &cobra.Command{
		Use:          "export",
		Short:        "Print the work journal for a given date.",
		SilenceUsage: true,
		Args:         cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			cfg, err := config.Load(*configPath)
			if err != nil {
				return fmt.Errorf("load %s: %w", *configPath, err)
			}
			dbPath, err := expandHome(cfg.Core.DBPath)
			if err != nil {
				return fmt.Errorf("resolve core.db_path: %w", err)
			}
			journalDir := filepath.Join(filepath.Dir(dbPath), "journal")

			if date == "" {
				date = time.Now().UTC().Format("2006-01-02")
			}
			// Validate date before building the path to prevent path traversal.
			if _, parseErr := time.Parse("2006-01-02", date); parseErr != nil {
				return fmt.Errorf("invalid date %q: must be YYYY-MM-DD", date)
			}
			path := filepath.Join(journalDir, date+".md")
			data, readErr := os.ReadFile(path)
			if readErr != nil {
				if os.IsNotExist(readErr) {
					fmt.Fprintf(cmd.OutOrStdout(), "no journal for %s\n", date)
					return nil
				}
				return fmt.Errorf("read journal: %w", readErr)
			}
			fmt.Fprint(cmd.OutOrStdout(), string(data))
			return nil
		},
	}
	cmd.Flags().StringVar(&date, "date", "", "Date to export (YYYY-MM-DD, defaults to today)")
	return cmd
}
