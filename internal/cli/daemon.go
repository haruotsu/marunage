package cli

import (
	"errors"
	"fmt"
	"io"
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

// ErrAlreadyRunning is returned (wrapped) by fileBackedDaemon.Start when a
// live process is already registered in the pidfile. Callers that want to
// distinguish "already running" from other start failures can use
// errors.Is(err, ErrAlreadyRunning).
var ErrAlreadyRunning = errors.New("daemon: already running")

// daemonControl is the narrow control surface the start / stop / status
// subcommands use against the background process. Production wires
// fileBackedDaemon (pidfile + signal); tests inject a fake so the test
// suite never spawns a real `marunage loop` subprocess.
type daemonControl interface {
	Start(args []string) (int, error)
	Stop(timeout time.Duration) (int, error)
	Status() (daemonStatus, error)
	LogPath() string
}

// daemonInstaller manages OS-level service registration (LaunchAgent /
// systemd unit / Windows Task Scheduler). Install is idempotent: if the
// service file already exists with identical content it returns nil
// immediately. Uninstall removes the registration.
type daemonInstaller interface {
	Install(exePath, configPath, logPath string) error
	Uninstall() error
}

// daemonInstallerFactory builds a daemonInstaller for the given configPath.
type daemonInstallerFactory func(configPath string) (daemonInstaller, error)

var daemonInstallerHook daemonInstallerFactory

func withDaemonInstaller(t interface{ Cleanup(func()) }, f daemonInstallerFactory) {
	prev := daemonInstallerHook
	daemonInstallerHook = f
	t.Cleanup(func() { daemonInstallerHook = prev })
}

func activeDaemonInstaller() daemonInstallerFactory {
	if daemonInstallerHook != nil {
		return daemonInstallerHook
	}
	return productionDaemonInstaller
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
		// Fail closed rather than falling back to the bare name
		// "marunage" + PATH lookup: a hostile $PATH entry on a CI
		// runner / unprivileged container could shadow the real
		// binary and have `daemon start` spawn an attacker-controlled
		// process with the operator's --config. The user can recover
		// by re-invoking from an absolute path or fixing the
		// environment that broke os.Executable.
		return nil, fmt.Errorf("daemon: resolve executable path: %w (refuse to fall back to PATH lookup; invoke marunage by absolute path)", err)
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

func (d *fileBackedDaemon) LogPath() string { return d.logPath }

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
		// Status only ever fails when readPID hits a malformed
		// pidfile (parse error) or a non-positive pid. Either case is
		// "garbage on disk we cannot trust"; treat it like a stale
		// pidfile rather than refusing to start. Without this branch
		// a single typo in /usr/local/etc/marunage/daemon.pid would
		// permanently wedge `daemon start` until the operator deleted
		// the file by hand.
		_ = os.Remove(d.pidPath)
		cur = daemonStatus{Path: d.pidPath}
	}
	if cur.Running {
		return cur.PID, fmt.Errorf("daemon already running (pid=%d, pidfile=%s): %w", cur.PID, cur.Path, ErrAlreadyRunning)
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
	// Persist the pidfile BEFORE releasing the child handle so a
	// writePID failure can still cleanly kill the just-spawned daemon
	// (Release()'d processes lose their *os.Process Kill() target on
	// some platforms, leaving an orphan that the operator now has no
	// recorded pid for). On success Release() detaches normally so the
	// CLI can exit without becoming the daemon's parent.
	if err := writePID(d.pidPath, pid); err != nil {
		// Best-effort cleanup: kill the child + reap so the operator
		// is not left with a zombie or an unkilled detached process
		// they cannot find via pidfile.
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		return pid, fmt.Errorf("daemon: write pidfile: %w", err)
	}
	if err := cmd.Process.Release(); err != nil {
		// Pidfile is already written; the operator can `daemon stop`
		// even if Release() failed, so surface the error but do not
		// roll back.
		return pid, fmt.Errorf("daemon: release child: %w", err)
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

// newDaemonCmd builds `marunage daemon {start|stop|status|restart|install|uninstall|logs}`.
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
	cmd.AddCommand(newDaemonRestartCmd(configPath))
	cmd.AddCommand(newDaemonInstallCmd(configPath))
	cmd.AddCommand(newDaemonUninstallCmd(configPath))
	cmd.AddCommand(newDaemonLogsCmd(configPath))
	return cmd
}

func newDaemonRestartCmd(configPath *string) *cobra.Command {
	return &cobra.Command{
		Use:          "restart",
		Short:        "Restart the marunage daemon (stop then start).",
		SilenceUsage: true,
		Args:         cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctl, err := activeDaemonControl()(*configPath)
			if err != nil {
				return err
			}
			oldPID, err := ctl.Stop(daemonStopTimeout)
			if err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "daemon stopped (pid=%d)\n", oldPID)
			newPID, err := ctl.Start(nil)
			if err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "daemon started (pid=%d)\n", newPID)
			return nil
		},
	}
}

func newDaemonInstallCmd(configPath *string) *cobra.Command {
	return &cobra.Command{
		Use:          "install",
		Short:        "Register marunage as an OS service (LaunchAgent / systemd / Task Scheduler).",
		SilenceUsage: true,
		Args:         cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctl, err := activeDaemonControl()(*configPath)
			if err != nil {
				return err
			}
			inst, err := activeDaemonInstaller()(*configPath)
			if err != nil {
				return err
			}
			exe, err := os.Executable()
			if err != nil {
				return fmt.Errorf("install: resolve executable: %w", err)
			}
			if err := inst.Install(exe, *configPath, ctl.LogPath()); err != nil {
				return err
			}
			fmt.Fprintln(cmd.OutOrStdout(), "daemon service installed")
			return nil
		},
	}
}

func newDaemonUninstallCmd(configPath *string) *cobra.Command {
	return &cobra.Command{
		Use:          "uninstall",
		Short:        "Remove the marunage OS service registration.",
		SilenceUsage: true,
		Args:         cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			inst, err := activeDaemonInstaller()(*configPath)
			if err != nil {
				return err
			}
			if err := inst.Uninstall(); err != nil {
				return err
			}
			fmt.Fprintln(cmd.OutOrStdout(), "daemon service uninstalled")
			return nil
		},
	}
}

func newDaemonLogsCmd(configPath *string) *cobra.Command {
	var follow bool
	cmd := &cobra.Command{
		Use:          "logs",
		Short:        "Show the daemon log (~/.marunage/logs/daemon.log).",
		SilenceUsage: true,
		Args:         cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctl, err := activeDaemonControl()(*configPath)
			if err != nil {
				return err
			}
			return streamLog(ctl.LogPath(), follow, cmd.OutOrStdout())
		},
	}
	cmd.Flags().BoolVarP(&follow, "follow", "f", false, "Stream log output continuously.")
	return cmd
}

// streamLog reads (and optionally tails) the log file at path into w.
func streamLog(path string, follow bool, w io.Writer) error {
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return fmt.Errorf("daemon log not found at %s", path)
		}
		return fmt.Errorf("daemon: open log: %w", err)
	}
	defer func() { _ = f.Close() }()

	if _, err := io.Copy(w, f); err != nil {
		return err
	}
	if !follow {
		return nil
	}
	for {
		time.Sleep(200 * time.Millisecond)
		if _, err := io.Copy(w, f); err != nil {
			return err
		}
	}
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
