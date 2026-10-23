//go:build !windows

package projectmemory

import (
	"fmt"
	"os"

	"golang.org/x/sys/unix"
)

// verifyUnixHandle binds type, link count, and private permissions to the handle actually used for I/O.
func verifyUnixHandle(descriptor int) error {
	var info unix.Stat_t
	if err := unix.Fstat(descriptor, &info); err != nil {
		return fieldError("path", "store.identity", "handle", err)
	}
	if info.Mode&unix.S_IFMT != unix.S_IFREG || info.Nlink != 1 || info.Mode&0022 != 0 {
		return fieldError("path", "store.identity", "handle", nil)
	}
	return nil
}

// memoryOpenFileIdentity returns the inode identity of the exact effective file handle.
func memoryOpenFileIdentity(file *os.File) (string, error) {
	var info unix.Stat_t
	if file == nil {
		return "", fieldError("path", "store.identity", "file_handle", nil)
	}
	if err := unix.Fstat(int(file.Fd()), &info); err != nil || info.Mode&unix.S_IFMT != unix.S_IFREG || info.Nlink != 1 {
		return "", fieldError("path", "store.identity", "file_handle", err)
	}
	return fmt.Sprintf("%d:%d", info.Dev, info.Ino), nil
}

// memoryDirectoryIdentity returns a stable device/inode identity for root replacement checks.
func memoryDirectoryIdentity(path string) (string, error) {
	descriptor, err := unix.Open(path, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		return "", err
	}
	defer unix.Close(descriptor)
	var info unix.Stat_t
	if err = unix.Fstat(descriptor, &info); err != nil || info.Mode&unix.S_IFMT != unix.S_IFDIR {
		return "", fieldError("path", "store.identity", "root", err)
	}
	return fmt.Sprintf("%d:%d", info.Dev, info.Ino), nil
}
