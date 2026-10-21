//go:build windows

package contractreview

import (
	"errors"
	"os"

	"golang.org/x/sys/windows"
)

// tryAttemptLock opens the durable attempt owner file and acquires its process-lifetime byte-range lock without blocking.
func tryAttemptLock(directory *os.File, name string) (*os.File, bool, error) {
	file, err := openMutableFileInDirectory(directory, name, windows.FILE_GENERIC_READ|windows.FILE_GENERIC_WRITE, windows.FILE_OPEN_IF)
	if err != nil {
		return nil, false, err
	}
	overlapped := new(windows.Overlapped)
	err = windows.LockFileEx(windows.Handle(file.Fd()), windows.LOCKFILE_EXCLUSIVE_LOCK|windows.LOCKFILE_FAIL_IMMEDIATELY, 0, 1, 0, overlapped)
	if err != nil {
		_ = file.Close()
		if errors.Is(err, windows.ERROR_LOCK_VIOLATION) || errors.Is(err, windows.ERROR_IO_PENDING) {
			return nil, false, nil
		}
		return nil, false, err
	}
	return file, true, nil
}

// releaseAttemptLock drops ownership before closing the descriptor so a waiting process can prove the prior owner exited.
func releaseAttemptLock(file *os.File) error {
	if file == nil {
		return nil
	}
	overlapped := new(windows.Overlapped)
	err := windows.UnlockFileEx(windows.Handle(file.Fd()), 0, 1, 0, overlapped)
	closeErr := file.Close()
	if err != nil {
		return err
	}
	return closeErr
}
