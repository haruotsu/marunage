//go:build windows

package cli

import (
	"fmt"
	"os/exec"
)

const schedTaskName = "marunage-daemon"

// schedTaskInstaller registers marunage as a Windows Scheduled Task.
type schedTaskInstaller struct {
	exePath    string
	configPath string
	runSchtask func(args ...string) error
}

func (s *schedTaskInstaller) Install(exePath, configPath, _ string) error {
	// /f overwrites an existing task (idempotent), /sc ONLOGON runs at user login.
	args := []string{
		"/create", "/f",
		"/tn", schedTaskName,
		"/tr", fmt.Sprintf(`"%s" --config "%s" loop`, exePath, configPath),
		"/sc", "ONLOGON",
		"/rl", "LIMITED",
	}
	if err := s.runSchtask(args...); err != nil {
		return fmt.Errorf("daemon install: schtasks: %w", err)
	}
	return nil
}

func (s *schedTaskInstaller) Uninstall() error {
	if err := s.runSchtask("/delete", "/f", "/tn", schedTaskName); err != nil {
		return fmt.Errorf("daemon uninstall: schtasks: %w", err)
	}
	return nil
}

// productionDaemonInstaller returns the Windows Task Scheduler installer.
func productionDaemonInstaller(_ string) (daemonInstaller, error) {
	return &schedTaskInstaller{
		runSchtask: func(args ...string) error {
			return exec.Command("schtasks", args...).Run()
		},
	}, nil
}
