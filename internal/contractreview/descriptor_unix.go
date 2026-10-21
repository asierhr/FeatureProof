//go:build !windows

package contractreview

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"syscall"

	"golang.org/x/sys/unix"
)

// descriptorIsNotExist normalizes descriptor-relative absence for store idempotency semantics.
func descriptorIsNotExist(err error) bool { return errors.Is(err, os.ErrNotExist) }

// descriptorIsExist normalizes descriptor-relative exclusive-create collisions.
func descriptorIsExist(err error) bool { return errors.Is(err, os.ErrExist) }

// openRegularInDirectory opens one final regular file without following its name.
func openRegularInDirectory(parent *os.File, name string) (*os.File, error) {
	fd, err := unix.Openat(int(parent.Fd()), name, unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	file := os.NewFile(uintptr(fd), name)
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() {
		_ = file.Close()
		if err != nil {
			return nil, err
		}
		return nil, os.ErrInvalid
	}
	return file, nil
}

// createExclusiveInDirectory creates one immutable index name relative to its directory.
func createExclusiveInDirectory(parent *os.File, name string) (*os.File, error) {
	fd, err := unix.Openat(int(parent.Fd()), name, unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0600)
	if err != nil {
		return nil, err
	}
	return os.NewFile(uintptr(fd), name), nil
}

// removeInDirectory unlinks one known child relative to its retained parent descriptor.
func removeInDirectory(parent *os.File, name string) error {
	return unix.Unlinkat(int(parent.Fd()), name, 0)
}

// fileLinkCountHandle reports aliasing from the already-open no-follow file descriptor.
func fileLinkCountHandle(file *os.File) (uint64, error) {
	info, err := file.Stat()
	if err != nil {
		return 0, err
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return 0, os.ErrInvalid
	}
	return uint64(stat.Nlink), nil
}

// createTempInDirectory creates a private file by name relative to the retained parent descriptor.
func createTempInDirectory(parent *os.File, parentPath, prefix string) (*os.File, error) {
	for attempt := 0; attempt < 100; attempt++ {
		random := make([]byte, 8)
		if _, err := rand.Read(random); err != nil {
			return nil, err
		}
		name := prefix + hex.EncodeToString(random)
		fd, err := unix.Openat(int(parent.Fd()), name, unix.O_RDWR|unix.O_CREAT|unix.O_EXCL|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0600)
		if errors.Is(err, unix.EEXIST) {
			continue
		}
		if err != nil {
			return nil, err
		}
		return os.NewFile(uintptr(fd), filepath.Join(parentPath, name)), nil
	}
	return nil, os.ErrExist
}

// resetFileInDirectory reopens one known private temp without following links and truncates partial crash bytes.
func resetFileInDirectory(parent *os.File, parentPath, name string) (*os.File, error) {
	descriptor, err := unix.Openat(int(parent.Fd()), name, unix.O_RDWR|unix.O_TRUNC|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0600)
	if err != nil {
		return nil, err
	}
	return os.NewFile(uintptr(descriptor), filepath.Join(parentPath, name)), nil
}

// openRelativeDirectory walks from a retained root descriptor and never follows an ancestor link.
func openRelativeDirectory(root *os.File, relative string, create bool) (*os.File, error) {
	if _, err := root.Stat(); err != nil {
		return nil, err
	}
	fd, err := unix.Dup(int(root.Fd()))
	if err != nil {
		return nil, err
	}
	current := os.NewFile(uintptr(fd), root.Name())
	for _, component := range strings.Split(filepath.Clean(relative), string(os.PathSeparator)) {
		if component == "" || component == "." {
			continue
		}
		next, openErr := unix.Openat(int(current.Fd()), component, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
		if errors.Is(openErr, unix.ENOENT) && create {
			if makeErr := unix.Mkdirat(int(current.Fd()), component, 0700); makeErr != nil && !errors.Is(makeErr, unix.EEXIST) {
				_ = current.Close()
				return nil, makeErr
			}
			next, openErr = unix.Openat(int(current.Fd()), component, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
		}
		if openErr != nil {
			_ = current.Close()
			return nil, openErr
		}
		_ = current.Close()
		current = os.NewFile(uintptr(next), component)
	}
	return current, nil
}
