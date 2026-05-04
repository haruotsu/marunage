package cli

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	"github.com/haruotsu/marunage/internal/config"
)

// daemonControl is the narrow control surface the start / stop / status
// subcommands use against the background process. Production wires
// fileBackedDaemon (pidfile + signal); tests inject a fake so the test
// suite never spawns a real `marunage loop` subprocess.
//
// The interface is small on purpose: the daemon's responsibilities are
// "manage one long-running background `marunage loop` invocation",
// nothing more. Anything richer (LaunchAgent / systemd unit generation,
// log rotation policy, etc.) belongs in a follow-up PR rather than
// growing this surface.
type daemonControl interface {
	Start(args []string) (int, error)
	Stop(timeout time.Duration) (int, error)
	Status() (daemonStatus, error)
}

// daemonStatus captures a single status probe. Running == false with a
// non-zero PID means "stale pidfile present"; the caller surfaces that
// distinction so the operator knows whether to delete the file.
type daemonStatus struct {
	Running bool
	PID     int
	Path    string // pidfile path
}

// daemonControlFactory builds a daemonControl from the resolved
// configPath. Mirrors the other CLI factory shapes so tests inject
// fakes via withDaemonControl without spinning up subprocesses.
type daemonControlFactory func(configPath string) (daemonControl, error)

var daemonControlHook daemonControlFactory

func withDaemonControl(t interface{ Cleanup(func()) }, f daemonControlFactory) {
	prev := daemonControlHook
	daemonControlHook = f
	t.Cleanup(func() { daemonControlHook = prev })
}

func activeDaemonControl() daemonControlFactory {
	if daemonControlHook != nil {
		return daemonControlHook
	}
	return productionDaemonControl
}

// productionDaemonControl resolves ~/.marunage/daemon.pid +
// ~/.marunage/logs/daemon.log from the loaded config, then returns a
// fileBackedDaemon. The CLI shells the executable through os/exec when
// Start is called so the spawned binary is always whichever
// `marunage` currently resolves on PATH (or the absolute path the
// operator invoked, when an absolute argv[0] is available).
func productionDaemonControl(configPath string) (daemonControl, error) {
	cfg, err := config.Load(configPath)
	if err != nil {
		return nil, fmt.Errorf("load %s: %w", configPath, err)
	}
	dbPath, err := expandHome(cfg.Core.DBPath)
	if err != nil {
		return nil, fmt.Errorf("resolve core.db_path %q: %w", cfg.Core.DBPath, err)
	}
	root := filepath.Dir(dbPath)
	pidPath := filepath.Join(root, "daemon.pid")
	logPath := filepath.Join(root, "logs", "daemon.log")
	exePath, err := os.Executable()
	if err != nil {
		// os.Executable resolves the running binary on every platform we
		// target; falling back to the literal argv[0] the operator typed
		// keeps `daemon start` usable when the resolution failed (e.g.
		// stripped binary on a minimal container).
		exePath = "marunage"
	}
	return &fileBackedDaemon{
		pidPath: pidPath,
		logPath: logPath,
		exe:     exePath,
		// configPath rides along so the spawned `marunage loop`
		// inherits the same --config the parent saw.
		configPath: configPath,
	}, nil
}

// fileBackedDaemon is the production daemonControl. State lives in a
// single pidfile (~/.marunage/daemon.pid). Start spawns the child with
// stdout/stderr redirected to the rotating daemon.log; Stop reads the
// pid, sends SIGTERM, and polls until the process exits or the timeout
// expires (after which it sends SIGKILL).
type fileBackedDaemon struct {
	pidPath    string
	logPath    string
	exe        string
	configPath string
}

func (d *fileBackedDaemon) Status() (daemonStatus, error) {
	st := daemonStatus{Path: d.pidPath}
	pid, err := readPID(d.pidPath)
	if errors.Is(err, os.ErrNotExist) {
		return st, nil
	}
	if err != nil {
		return st, err
	}
	st.PID = pid
	st.Running = processAlive(pid)
	return st, nil
}

func (d *fileBackedDaemon) Start(args []string) (int, error) {
	cur, err := d.Status()
	if err != nil {
		return 0, err
	}
	if cur.Running {
		return cur.PID, fmt.Errorf("daemon already running (pid=%d, pidfile=%s)", cur.PID, cur.Path)
	}
	if cur.PID != 0 {
		// Stale pidfile from a crashed prior daemon; clear it so the
		// new write is unambiguous.
		_ = os.Remove(d.pidPath)
	}

	if err := os.MkdirAll(filepath.Dir(d.logPath), 0o700); err != nil {
		return 0, fmt.Errorf("daemon: mkdir log dir: %w", err)
	}
	logFile, err := os.OpenFile(d.logPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return 0, fmt.Errorf("daemon: open log: %w", err)
	}
	defer func() { _ = logFile.Close() }()

	full := append([]string{"--config", d.configPath, "loop"}, args...)
	cmd := exec.Command(d.exe, full...)
	cmd.Stdout = logFile
	cmd.Stderr = logFile
	cmd.Stdin = nil
	cmd.SysProcAttr = detachAttrs()
	if err := cmd.Start(); err != nil {
		return 0, fmt.Errorf("daemon: spawn: %w", err)
	}
	pid := cmd.Process.Pid
	// Release the child so it keeps running after this CLI invocation
	// exits. The pidfile records the pid so daemon stop / status can
	// re-find it on the next CLI invocation.
	if err := cmd.Process.Release(); err != nil {
		return pid, fmt.Errorf("daemon: release child: %w", err)
	}
	if err := writePID(d.pidPath, pid); err != nil {
		return pid, fmt.Errorf("daemon: write pidfile: %w", err)
	}
	return pid, nil
}

func (d *fileBackedDaemon) Stop(timeout time.Duration) (int, error) {
	st, err := d.Status()
	if err != nil {
		return 0, err
	}
	if st.PID == 0 {
		return 0, fmt.Errorf("daemon: no pidfile at %s", d.pidPath)
	}
	if !st.Running {
		_ = os.Remove(d.pidPath)
		return st.PID, fmt.Errorf("daemon: stale pidfile (pid=%d not running); removed %s", st.PID, d.pidPath)
	}
	proc, err := os.FindProcess(st.PID)
	if err != nil {
		return st.PID, fmt.Errorf("daemon: find process %d: %w", st.PID, err)
	}
	if err := proc.Signal(syscall.SIGTERM); err != nil {
		return st.PID, fmt.Errorf("daemon: signal SIGTERM pid=%d: %w", st.PID, err)
	}
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if !processAlive(st.PID) {
			_ = os.Remove(d.pidPath)
			return st.PID, nil
		}
		time.Sleep(50 * time.Millisecond)
	}
	// Hard kill on timeout — no point leaving a hung daemon for the
	// operator to chase manually.
	_ = proc.Signal(syscall.SIGKILL)
	for i := 0; i < 20; i++ {
		if !processAlive(st.PID) {
			_ = os.Remove(d.pidPath)
			return st.PID, fmt.Errorf("daemon: SIGTERM timed out after %s; sent SIGKILL", timeout)
		}
		time.Sleep(50 * time.Millisecond)
	}
	return st.PID, fmt.Errorf("daemon: process %d still alive after SIGKILL", st.PID)
}

// readPID reads the pidfile. Returns (0, os.ErrNotExist) when the file
// is missing so callers can branch on "no daemon ever started".
func readPID(path string) (int, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return 0, err
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil {
		return 0, fmt.Errorf("daemon: malformed pidfile %s: %w", path, err)
	}
	if pid <= 0 {
		return 0, fmt.Errorf("daemon: pidfile %s contains non-positive pid %d", path, pid)
	}
	return pid, nil
}

// writePID atomically rewrites the pidfile via tmp + rename so a
// concurrent reader never sees a half-written value.
func writePID(path string, pid int) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), filepath.Base(path)+".tmp.*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	cleanup := func() { _ = os.Remove(tmpName) }
	if _, err := fmt.Fprintf(tmp, "%d\n", pid); err != nil {
		_ = tmp.Close()
		cleanup()
		return err
	}
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		cleanup()
		return err
	}
	if err := tmp.Close(); err != nil {
		cleanup()
		return err
	}
	if err := os.Rename(tmpName, path); err != nil {
		cleanup()
		return err
	}
	return nil
}

// processAlive returns true when the process named by pid is still
// running. Implemented via Signal(0): on POSIX the kernel returns ESRCH
// (no such process) for a dead pid and EPERM for a running one we lack
// permission to signal — both mean "we observed the kernel's pid table
// but cannot send SIGTERM". For the daemon use case "running" is the
// only signal the CLI cares about.
func processAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	proc, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	err = proc.Signal(syscall.Signal(0))
	if err == nil {
		return true
	}
	if errors.Is(err, syscall.ESRCH) {
		return false
	}
	if errors.Is(err, syscall.EPERM) {
		return true
	}
	return false
}

// daemonStopTimeout caps how long stop waits for a SIGTERM'd daemon
// before escalating to SIGKILL. 10s matches the systemd default and
// gives the loop time to finish its in-flight discover / dispatch /
// render iteration before being killed.
const daemonStopTimeout = 10 * time.Second

// newDaemonCmd builds `marunage daemon {start|stop|status}`. The three
// subcommands share productionDaemonControl behind activeDaemonControl
// so tests can swap in a fake without touching real pidfiles.
func newDaemonCmd(configPath *string) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "daemon",
		Short: "Manage the marunage background loop daemon.",
		Long: "marunage daemon manages a long-running background `marunage loop`\n" +
			"process via a pidfile under ~/.marunage/daemon.pid. The child's\n" +
			"stdout/stderr stream into ~/.marunage/logs/daemon.log; SIGTERM /\n" +
			"SIGKILL escalation handles graceful and forced shutdown.",
	}
	cmd.AddCommand(newDaemonStartCmd(configPath))
	cmd.AddCommand(newDaemonStopCmd(configPath))
	cmd.AddCommand(newDaemonStatusCmd(configPath))
	return cmd
}

func newDaemonStartCmd(configPath *string) *cobra.Command {
	var interval time.Duration
	cmd := &cobra.Command{
		Use:          "start",
		Short:        "Start the marunage daemon (background `marunage loop`).",
		SilenceUsage: true,
		Args:         cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctl, err := activeDaemonControl()(*configPath)
			if err != nil {
				return err
			}
			args := []string{}
			if cmd.Flags().Changed("interval") {
				args = append(args, "--interval", interval.String())
			}
			pid, err := ctl.Start(args)
			if err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "daemon started (pid=%d)\n", pid)
			return nil
		},
	}
	cmd.Flags().DurationVar(&interval, "interval", 0, "Pass --interval D through to `marunage loop`.")
	return cmd
}

func newDaemonStopCmd(configPath *string) *cobra.Command {
	return &cobra.Command{
		Use:          "stop",
		Short:        "Stop the marunage daemon (SIGTERM, escalates to SIGKILL after 10s).",
		SilenceUsage: true,
		Args:         cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctl, err := activeDaemonControl()(*configPath)
			if err != nil {
				return err
			}
			pid, err := ctl.Stop(daemonStopTimeout)
			if err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "daemon stopped (pid=%d)\n", pid)
			return nil
		},
	}
}

func newDaemonStatusCmd(configPath *string) *cobra.Command {
	return &cobra.Command{
		Use:          "status",
		Short:        "Report whether the marunage daemon is running.",
		SilenceUsage: true,
		Args:         cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctl, err := activeDaemonControl()(*configPath)
			if err != nil {
				return err
			}
			st, err := ctl.Status()
			if err != nil {
				return err
			}
			switch {
			case st.Running:
				fmt.Fprintf(cmd.OutOrStdout(), "running (pid=%d, pidfile=%s)\n", st.PID, st.Path)
			case st.PID != 0:
				fmt.Fprintf(cmd.OutOrStdout(), "stopped (stale pidfile pid=%d at %s)\n", st.PID, st.Path)
			default:
				fmt.Fprintf(cmd.OutOrStdout(), "stopped (no pidfile at %s)\n", st.Path)
			}
			return nil
		},
	}
}
