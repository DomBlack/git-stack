//go:build !unix

package app

import (
	"context"
	"os"
	"time"
)

// lockFile on platforms without flock only makes sure the lock file exists;
// we don't ship for these platforms.
func lockFile(_ context.Context, path string, _ time.Duration) (func(), error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return nil, err
	}
	return func() { _ = f.Close() }, nil
}
