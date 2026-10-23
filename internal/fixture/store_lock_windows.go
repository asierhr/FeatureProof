//go:build windows

package fixture

import (
	"context"
	"errors"
	"os"
	"time"

	"golang.org/x/sys/windows"
)

// acquireProcessLock waits cancelably for a process-lifetime byte lock released automatically when its owner exits.
func acquireProcessLock(ctx context.Context, path string) (func(), error) {
	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	for {
		overlapped := new(windows.Overlapped)
		err = windows.LockFileEx(windows.Handle(file.Fd()), windows.LOCKFILE_EXCLUSIVE_LOCK|windows.LOCKFILE_FAIL_IMMEDIATELY, 0, 1, 0, overlapped)
		if err == nil {
			return func() {
				unlock := new(windows.Overlapped)
				_ = windows.UnlockFileEx(windows.Handle(file.Fd()), 0, 1, 0, unlock)
				_ = file.Close()
			}, nil
		}
		if !errors.Is(err, windows.ERROR_LOCK_VIOLATION) && !errors.Is(err, windows.ERROR_IO_PENDING) {
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
