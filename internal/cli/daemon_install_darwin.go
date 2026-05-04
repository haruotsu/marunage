package cli

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"text/template"
)

const plistLabel = "com.haruotsu.marunage"

const plistTmpl = `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN"
    "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
    <key>Label</key>
    <string>{{.Label}}</string>
    <key>ProgramArguments</key>
    <array>
        <string>{{.ExePath}}</string>
        <string>--config</string>
        <string>{{.ConfigPath}}</string>
        <string>loop</string>
    </array>
    <key>RunAtLoad</key>
    <true/>
    <key>KeepAlive</key>
    <true/>
    <key>StandardOutPath</key>
    <string>{{.LogPath}}</string>
    <key>StandardErrorPath</key>
    <string>{{.LogPath}}</string>
</dict>
</plist>
`

type plistVars struct {
	Label, ExePath, ConfigPath, LogPath string
}

// launchAgentInstaller registers marunage as a macOS LaunchAgent.
type launchAgentInstaller struct {
	plistPath string
	runctl    func(args ...string) error
}

func (l *launchAgentInstaller) Install(exePath, configPath, logPath string) error {
	vars := plistVars{
		Label:      plistLabel,
		ExePath:    exePath,
		ConfigPath: configPath,
		LogPath:    logPath,
	}
	tmpl, err := template.New("plist").Parse(plistTmpl)
	if err != nil {
		return fmt.Errorf("daemon install: parse plist template: %w", err)
	}
	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, vars); err != nil {
		return fmt.Errorf("daemon install: render plist: %w", err)
	}
	want := buf.Bytes()

	// Idempotent: skip write if file already has identical content.
	if got, err := os.ReadFile(l.plistPath); err == nil && bytes.Equal(got, want) {
		return nil
	}

	if err := os.MkdirAll(filepath.Dir(l.plistPath), 0o755); err != nil {
		return fmt.Errorf("daemon install: mkdir %s: %w", filepath.Dir(l.plistPath), err)
	}
	if err := os.WriteFile(l.plistPath, want, 0o644); err != nil {
		return fmt.Errorf("daemon install: write plist: %w", err)
	}
	// Load the agent so it takes effect immediately without a re-login.
	_ = l.runctl("load", l.plistPath)
	return nil
}

func (l *launchAgentInstaller) Uninstall() error {
	// Unload before removing so launchd stops tracking the service.
	_ = l.runctl("unload", l.plistPath)
	if err := os.Remove(l.plistPath); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("daemon uninstall: remove %s: %w", l.plistPath, err)
	}
	return nil
}

// productionDaemonInstaller returns the macOS LaunchAgent installer.
func productionDaemonInstaller(_ string) (daemonInstaller, error) {
	plistPath, err := expandHome("~/Library/LaunchAgents/" + plistLabel + ".plist")
	if err != nil {
		return nil, fmt.Errorf("daemon install: resolve plist path: %w", err)
	}
	return &launchAgentInstaller{
		plistPath: plistPath,
		runctl: func(args ...string) error {
			return exec.Command("launchctl", args...).Run()
		},
	}, nil
}
