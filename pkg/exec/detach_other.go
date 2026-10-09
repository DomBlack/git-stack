//go:build !unix

package exec

import "syscall"

// detached is a no-op where sessions don't exist; there is no /dev/tty to
// keep a child away from either.
func detached() *syscall.SysProcAttr {
	return nil
}
