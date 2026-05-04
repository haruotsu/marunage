//go:build !darwin && !windows

package cli

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"text/template"
)

const systemdUnitName = "marunage.service"

const systemdTmpl = `[Unit]
Description=marunage background loop daemon
After=network.target

[Service]
ExecStart={{.ExePath}} --config {{.ConfigPath}} loop
Restart=always
StandardOutput=append:{{.LogPath}}
StandardError=append:{{.LogPath}}

[Install]
WantedBy=default.target
`

type systemdVars struct {
	ExePath, ConfigPath, LogPath string
}

// systemdInstaller registers marunage as a systemd user service.
type systemdInstaller struct {
	unitPath string
	runctl   func(args ...string) error
}

func (s *systemdInstaller) Install(exePath, configPath, logPath string) error {
	vars := systemdVars{
		ExePath:    exePath,
		ConfigPath: configPath,
		LogPath:    logPath,
	}
	tmpl, err := template.New("service").Parse(systemdTmpl)
	if err != nil {
		return fmt.Errorf("daemon install: parse service template: %w", err)
	}
	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, vars); err != nil {
		return fmt.Errorf("daemon install: render service: %w", err)
	}
	want := buf.Bytes()

	// Idempotent: skip write if file already has identical content.
	if got, err := os.ReadFile(s.unitPath); err == nil && bytes.Equal(got, want) {
		return nil
	}

	if err := os.MkdirAll(filepath.Dir(s.unitPath), 0o755); err != nil {
		return fmt.Errorf("daemon install: mkdir %s: %w", filepath.Dir(s.unitPath), err)
	}
	if err := os.WriteFile(s.unitPath, want, 0o644); err != nil {
		return fmt.Errorf("daemon install: write service: %w", err)
	}
	_ = s.runctl("--user", "daemon-reload")
	_ = s.runctl("--user", "enable", systemdUnitName)
	return nil
}

func (s *systemdInstaller) Uninstall() error {
	_ = s.runctl("--user", "disable", systemdUnitName)
	if err := os.Remove(s.unitPath); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("daemon uninstall: remove %s: %w", s.unitPath, err)
	}
	return nil
}

// productionDaemonInstaller returns the systemd user-service installer.
func productionDaemonInstaller(_ string) (daemonInstaller, error) {
	unitPath, err := expandHome("~/.config/systemd/user/" + systemdUnitName)
	if err != nil {
		return nil, fmt.Errorf("daemon install: resolve unit path: %w", err)
	}
	return &systemdInstaller{
		unitPath: unitPath,
		runctl: func(args ...string) error {
			return exec.Command("systemctl", args...).Run()
		},
	}, nil
}
