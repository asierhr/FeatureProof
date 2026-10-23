//go:build windows

package functionalflow

import (
	"context"
	"errors"
	"os"
	"time"

	"golang.org/x/sys/windows"
)

// acquireFlowProcessLock waits cancelably for an exclusive byte lock released automatically after owner death.
func acquireFlowProcessLock(ctx context.Context, path string) (func(), error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	for {
		if err = ctx.Err(); err != nil {
			_ = file.Close()
			return nil, err
		}
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
