//go:build !unix

package ghstack

import (
	"context"
	"os"
	"time"
)

// lock on platforms without flock only makes sure the lock file exists;
// gh stack's own locking is advisory and we don't ship for these platforms.
func lock(_ context.Context, path string, _ time.Duration) (func(), error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return nil, err
	}
	return func() { _ = f.Close() }, nil
}
