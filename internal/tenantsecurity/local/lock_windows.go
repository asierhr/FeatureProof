//go:build windows

package local

import (
	"context"
	"errors"
	"golang.org/x/sys/windows"
	"os"
	"time"
)

// acquireLock obtains a crash-released exclusive aggregate lock.
func acquireLock(ctx context.Context, path string) (func(), error) {
	f, e := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0600)
	if e != nil {
		return nil, e
	}
	o := new(windows.Overlapped)
	for {
		e = windows.LockFileEx(windows.Handle(f.Fd()), windows.LOCKFILE_EXCLUSIVE_LOCK|windows.LOCKFILE_FAIL_IMMEDIATELY, 0, 1, 0, o)
		if e == nil {
			return func() { _ = windows.UnlockFileEx(windows.Handle(f.Fd()), 0, 1, 0, o); _ = f.Close() }, nil
		}
		if !errors.Is(e, windows.ERROR_LOCK_VIOLATION) {
			_ = f.Close()
			return nil, e
		}
		timer := time.NewTimer(5 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			_ = f.Close()
			return nil, ctx.Err()
		case <-timer.C:
		}
	}
}
