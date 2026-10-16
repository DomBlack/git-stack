//go:build unix

package app

import (
	"context"
	"errors"
	"os"
	"syscall"
	"time"

	"github.com/DomBlack/git-stack/pkg/stack"
)

// lockFile takes an exclusive flock on path, waiting up to wait. The
// returned function releases it.
func lockFile(ctx context.Context, path string, wait time.Duration) (func(), error) {
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
			return nil, stack.New(stack.KindLocked, "the undo history is locked by another git stack command").
				WithSteps("wait for it to finish and retry")
		}
		select {
		case <-ctx.Done():
			_ = f.Close()
			return nil, ctx.Err()
		case <-time.After(50 * time.Millisecond):
		}
	}
}
