//go:build unix

package exec

import "syscall"

// detached returns the process attributes that start a child in its own
// session, so it has no controlling terminal: opening /dev/tty fails
// instead of reaching the terminal this process was started from.
func detached() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{Setsid: true}
}
