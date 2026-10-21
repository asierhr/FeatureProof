//go:build !windows

package contractreview

import (
	"errors"
	"os"

	"golang.org/x/sys/unix"
)

// tryAttemptLock opens the durable attempt owner file and acquires its process-lifetime advisory lock without blocking.
func tryAttemptLock(directory *os.File, name string) (*os.File, bool, error) {
	fd, err := unix.Openat(int(directory.Fd()), name, unix.O_RDWR|unix.O_CREAT|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0600)
	if err != nil {
		return nil, false, err
	}
	file := os.NewFile(uintptr(fd), name)
	if err = unix.Flock(fd, unix.LOCK_EX|unix.LOCK_NB); err != nil {
		_ = file.Close()
		if errors.Is(err, unix.EWOULDBLOCK) || errors.Is(err, unix.EAGAIN) {
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
	err := unix.Flock(int(file.Fd()), unix.LOCK_UN)
	closeErr := file.Close()
	if err != nil {
		return err
	}
	return closeErr
}
