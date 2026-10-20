//go:build !windows

package goinspector

import (
	"errors"
	"os"
	"path/filepath"

	"golang.org/x/sys/unix"
)

// openNoFollow opens the final Unix path component with O_NOFOLLOW and validates the retained descriptor.
func openNoFollow(path string, directory bool) (*os.File, error) {
	flags := unix.O_RDONLY | unix.O_CLOEXEC | unix.O_NOFOLLOW
	if directory {
		flags |= unix.O_DIRECTORY
	}
	descriptor, err := unix.Open(path, flags, 0)
	if err != nil {
		return nil, err
	}
	file := os.NewFile(uintptr(descriptor), path)
	info, err := file.Stat()
	if err != nil || directory != info.IsDir() || (!directory && !info.Mode().IsRegular()) {
		_ = file.Close()
		if err != nil {
			return nil, err
		}
		return nil, errors.New("repository descriptor is not an authorized plain object")
	}
	return file, nil
}

// openChildNoFollow resolves one Unix child relative to its already validated parent descriptor.
func openChildNoFollow(parent *os.File, name string, directory bool) (*os.File, error) {
	flags := unix.O_RDONLY | unix.O_CLOEXEC | unix.O_NOFOLLOW
	if directory {
		flags |= unix.O_DIRECTORY
	}
	descriptor, err := unix.Openat(int(parent.Fd()), name, flags, 0)
	if err != nil {
		return nil, err
	}
	file := os.NewFile(uintptr(descriptor), filepath.Join(parent.Name(), name))
	info, err := file.Stat()
	if err != nil || directory != info.IsDir() || (!directory && !info.Mode().IsRegular()) {
		_ = file.Close()
		if err != nil {
			return nil, err
		}
		return nil, errors.New("repository descriptor is not an authorized plain object")
	}
	return file, nil
}
