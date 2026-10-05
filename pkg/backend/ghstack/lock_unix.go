//go:build unix

package ghstack

import (
	"context"
	"errors"
	"os"
	"syscall"
	"time"

	"github.com/DomBlack/git-stack/pkg/stack"
)

// lock takes an exclusive flock on path, the way gh stack guards its file,
// waiting up to wait. The returned function releases it.
func lock(ctx context.Context, path string, wait time.Duration) (func(), error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return nil, err
	}
	deadline := time.Now().Add(wait)
	for {
		err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
		if err == nil {
			return func() {
				_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
				_ = f.Close()
			}, nil
		}
		if !errors.Is(err, syscall.EWOULDBLOCK) {
			_ = f.Close()
			return nil, err
		}
		if time.Now().After(deadline) {
			_ = f.Close()
			return nil, stack.New(stack.KindLocked, "the stack metadata is locked by another process").
				WithSteps("wait for the other gh stack or git stack command to finish and retry")
		}
		select {
		case <-ctx.Done():
			_ = f.Close()
			return nil, ctx.Err()
		case <-time.After(50 * time.Millisecond):
		}
	}
}
