//go:build windows

package cli

import "syscall"

// detachAttrs is a no-op on Windows: there is no controlling-terminal
// session model to break out of, and the spawned process already has
// its own stdin/stdout/stderr handles redirected to the log file. A
// future PR may swap this for CREATE_NEW_PROCESS_GROUP if the daemon
// needs to survive Ctrl+C in cmd.exe more aggressively.
func detachAttrs() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{}
}
