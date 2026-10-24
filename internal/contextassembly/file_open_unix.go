//go:build !windows

package contextassembly

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"syscall"

	"golang.org/x/sys/unix"
)

// openRegularNoFollow opens an immutable object by handle and rejects symlinks and hardlinks on Unix.
func openRegularNoFollow(path string) (*os.File, error) {
	fd, err := unix.Open(path, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		return nil, err
	}
	file := os.NewFile(uintptr(fd), path)
	var stat unix.Stat_t
	if unix.Fstat(fd, &stat) != nil || stat.Mode&unix.S_IFMT != unix.S_IFREG || stat.Nlink != 1 || stat.Mode&0o077 != 0 {
		_ = file.Close()
		return nil, os.ErrPermission
	}
	return file, nil
}

// directoryHandleIdentity returns the device/inode identity of the already pinned directory.
func directoryHandleIdentity(directory *os.File) (string, error) {
	var stat unix.Stat_t
	if err := unix.Fstat(int(directory.Fd()), &stat); err != nil || stat.Mode&unix.S_IFMT != unix.S_IFDIR {
		return "", err
	}
	return fmt.Sprintf("%d:%d", stat.Dev, stat.Ino), nil
}

// openDirectoryAt pins one direct child directory without resolving through a mutable pathname.
func openDirectoryAt(parent *os.File, name string) (*os.File, error) {
	if filepath.Base(name) != name || name == "." || name == ".." {
		return nil, syscall.EINVAL
	}
	fd, err := unix.Openat(int(parent.Fd()), name, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		return nil, err
	}
	return os.NewFile(uintptr(fd), name), nil
}

// createTempAt creates one private child relative to a pinned directory without resolving its pathname again.
func createTempAt(directory *os.File) (*os.File, string, error) {
	for attempt := 0; attempt < 100; attempt++ {
		random := make([]byte, 8)
		if _, err := rand.Read(random); err != nil {
			return nil, "", err
		}
		name := ".context-" + hex.EncodeToString(random) + ".tmp"
		fd, err := unix.Openat(int(directory.Fd()), name, unix.O_RDWR|unix.O_CREAT|unix.O_EXCL|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0o600)
		if err == unix.EEXIST {
			continue
		}
		if err != nil {
			return nil, "", err
		}
		return os.NewFile(uintptr(fd), name), name, nil
	}
	return nil, "", os.ErrExist
}

// commitNoReplaceAt publishes the already-open temporary inode without trusting its mutable directory name.
func commitNoReplaceAt(directory, temporaryFile *os.File, temporary, target string) error {
	if err := linkOpenFileAt(directory, temporaryFile, temporary, target); err != nil {
		return err
	}
	return unix.Unlinkat(int(directory.Fd()), temporary, 0)
}

// linkNamedOpenFileAt authenticates a named Unix fallback before and after linking the already-open inode.
func linkNamedOpenFileAt(directory, temporaryFile *os.File, temporary, target string) error {
	var opened, named unix.Stat_t
	if err := unix.Fstat(int(temporaryFile.Fd()), &opened); err != nil {
		return err
	}
	if err := unix.Fstatat(int(directory.Fd()), temporary, &named, unix.AT_SYMLINK_NOFOLLOW); err != nil || opened.Dev != named.Dev || opened.Ino != named.Ino {
		return syscall.EPERM
	}
	if err := unix.Linkat(int(directory.Fd()), temporary, int(directory.Fd()), target, 0); err != nil {
		return err
	}
	var published unix.Stat_t
	if err := unix.Fstatat(int(directory.Fd()), target, &published, unix.AT_SYMLINK_NOFOLLOW); err != nil || opened.Dev != published.Dev || opened.Ino != published.Ino {
		_ = unix.Unlinkat(int(directory.Fd()), target, 0)
		return syscall.EPERM
	}
	return nil
}

// removeAt deletes one exact child relative to the pinned directory.
func removeAt(directory *os.File, name string) error {
	return unix.Unlinkat(int(directory.Fd()), name, 0)
}

// readNamesAt enumerates children through a duplicate of the pinned directory handle.
func readNamesAt(directory *os.File) ([]string, error) {
	fd, err := unix.Dup(int(directory.Fd()))
	if err != nil {
		return nil, err
	}
	duplicate := os.NewFile(uintptr(fd), directory.Name())
	defer duplicate.Close()
	entries, err := duplicate.ReadDir(-1)
	if err != nil {
		return nil, err
	}
	names := make([]string, len(entries))
	for index, entry := range entries {
		names[index] = entry.Name()
	}
	return names, nil
}

// isNotExistAt normalizes descriptor-relative absence on Unix.
func isNotExistAt(err error) bool { return os.IsNotExist(err) }

// openRegularAt opens one immutable child relative to a pinned Unix directory handle.
func openRegularAt(directory *os.File, name string) (*os.File, error) {
	fd, err := unix.Openat(int(directory.Fd()), name, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		return nil, err
	}
	file := os.NewFile(uintptr(fd), name)
	var stat unix.Stat_t
	if unix.Fstat(fd, &stat) != nil || stat.Mode&unix.S_IFMT != unix.S_IFREG || stat.Nlink != 1 || stat.Mode&0o077 != 0 {
		_ = file.Close()
		return nil, os.ErrPermission
	}
	return file, nil
}

// openDirectoryNoFollow pins a real directory handle without traversing a symlink on Unix.
func openDirectoryNoFollow(path string) (*os.File, error) {
	fd, err := unix.Open(path, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW|unix.O_DIRECTORY, 0)
	if err != nil {
		return nil, err
	}
	return os.NewFile(uintptr(fd), path), nil
}

// syncDirectory durably orders directory metadata publication on Unix filesystems.
func syncDirectory(file *os.File) error { return file.Sync() }
