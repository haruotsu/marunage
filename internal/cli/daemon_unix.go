//go:build !windows

package cli

import "syscall"

// detachAttrs returns the SysProcAttr that severs the spawned daemon
// from the launching CLI's controlling terminal so a subsequent shell
// exit (or Ctrl+C against the parent) does not also kill the daemon.
// Setsid creates a new session — the documented Unix way to detach.
func detachAttrs() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{Setsid: true}
}
