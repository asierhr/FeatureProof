//go:build !windows

package projectmemory

import (
	"context"
	"errors"
	"time"

	"golang.org/x/sys/unix"
)

// acquireMemoryProcessLock waits cancelably for an advisory lock that remains safe with stale lock files.
func acquireMemoryProcessLock(ctx context.Context, directory *memoryDirectoryHandle, name string) (func(), error) {
	file, err := directory.OpenLock(name)
	if err != nil {
		return nil, err
	}
	for {
		if err = ctx.Err(); err != nil {
			_ = file.Close()
			return nil, err
		}
		err = unix.Flock(int(file.Fd()), unix.LOCK_EX|unix.LOCK_NB)
		if err == nil {
			return func() { _ = unix.Flock(int(file.Fd()), unix.LOCK_UN); _ = file.Close() }, nil
		}
		if !errors.Is(err, unix.EWOULDBLOCK) && !errors.Is(err, unix.EAGAIN) {
			_ = file.Close()
			return nil, err
		}
		select {
		case <-ctx.Done():
			_ = file.Close()
			return nil, ctx.Err()
		case <-time.After(time.Millisecond):
		}
	}
}
