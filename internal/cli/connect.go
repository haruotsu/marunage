package cli

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"time"

	"github.com/spf13/cobra"

	"github.com/haruotsu/marunage/internal/connector"
)

func newConnectCmd() *cobra.Command {
	return &cobra.Command{
		Use:           "connect <url-or-path>",
		Short:         "Register an HTTP connector from a URL or local connector.toml path.",
		Args:          cobra.ExactArgs(1),
		SilenceUsage:  true,
		SilenceErrors: false,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runConnect(cmd, args[0])
		},
	}
}

func runConnect(cmd *cobra.Command, target string) error {
	body, err := fetchConnectorTOML(target)
	if err != nil {
		return fmt.Errorf("fetch connector: %w", err)
	}

	cfg, err := connector.LoadConfigFromBytes(body)
	if err != nil {
		return fmt.Errorf("invalid connector.toml: %w", err)
	}

	dest, err := connectorStorePath(cfg.Connector.Name)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		return fmt.Errorf("create connector dir: %w", err)
	}
	if err := os.WriteFile(dest, body, 0o644); err != nil {
		return fmt.Errorf("write connector.toml: %w", err)
	}

	fmt.Fprintf(cmd.OutOrStdout(), "connector %q registered at %s\n", cfg.Connector.Name, dest)
	return nil
}

func fetchConnectorTOML(target string) ([]byte, error) {
	if isHTTPURL(target) {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
		if err != nil {
			return nil, fmt.Errorf("build request: %w", err)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			return nil, fmt.Errorf("GET %s: %w", target, err)
		}
		defer func() { _ = resp.Body.Close() }()
		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			return nil, fmt.Errorf("GET %s: status %d", target, resp.StatusCode)
		}
		return io.ReadAll(resp.Body)
	}
	return os.ReadFile(target)
}

func isHTTPURL(s string) bool {
	u, err := url.Parse(s)
	return err == nil && (u.Scheme == "http" || u.Scheme == "https")
}

func connectorStorePath(name string) (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("home dir: %w", err)
	}
	return filepath.Join(home, ".marunage", "connectors", name, "connector.toml"), nil
}
