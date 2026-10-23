//go:build !windows

package projectmemory

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/sys/unix"
)

// memoryDirectoryHandle fixes publication operations to one verified directory inode.
type memoryDirectoryHandle struct {
	descriptor   int
	path         string
	rootIdentity string
	components   []string
}

// Identity returns the device/inode identity of the fixed effective directory.
func (directory *memoryDirectoryHandle) Identity() (string, error) {
	var info unix.Stat_t
	if err := unix.Fstat(directory.descriptor, &info); err != nil || info.Mode&unix.S_IFMT != unix.S_IFDIR {
		return "", fieldError("path", "store.identity", "directory_handle", err)
	}
	return fmt.Sprintf("%d:%d", info.Dev, info.Ino), nil
}

// openMemoryDirectoryHandle opens a no-follow dirfd used by all relative publication calls.
func openMemoryDirectoryHandle(path string) (*memoryDirectoryHandle, error) {
	descriptor, err := unix.Open(path, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		return nil, err
	}
	handle := &memoryDirectoryHandle{descriptor: descriptor, path: path}
	identity, identityErr := handle.Identity()
	if identityErr != nil {
		_ = handle.Close()
		return nil, identityErr
	}
	handle.rootIdentity = identity
	return handle, nil
}

// RootIdentity reports the initially opened root identity used for relative traversal.
func (directory *memoryDirectoryHandle) RootIdentity() string { return directory.rootIdentity }

// ComponentIdentities returns detached identities for each traversed descendant component.
func (directory *memoryDirectoryHandle) ComponentIdentities() []string {
	return append([]string(nil), directory.components...)
}

// openMemoryDirectoryTree fixes the root then traverses every descendant through no-follow dirfds.
func openMemoryDirectoryTree(root, relative string, create bool) (*memoryDirectoryHandle, error) {
	clean := filepath.Clean(relative)
	if clean == ".." || filepath.IsAbs(clean) || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return nil, fieldError("path", "store.path", "escape", nil)
	}
	current, err := openMemoryDirectoryHandle(root)
	if err != nil {
		return nil, err
	}
	if clean == "." || clean == "" {
		return current, nil
	}
	for _, component := range strings.Split(clean, string(filepath.Separator)) {
		if component == "" || component == "." || component == ".." {
			_ = current.Close()
			return nil, fieldError("path", "store.path", "component", nil)
		}
		if create {
			if mkdirErr := unix.Mkdirat(current.descriptor, component, 0700); mkdirErr != nil && !errors.Is(mkdirErr, unix.EEXIST) {
				_ = current.Close()
				return nil, mkdirErr
			}
		}
		descriptor, openErr := unix.Openat(current.descriptor, component, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
		if openErr != nil {
			_ = current.Close()
			return nil, openErr
		}
		var info unix.Stat_t
		if statErr := unix.Fstat(descriptor, &info); statErr != nil || info.Mode&unix.S_IFMT != unix.S_IFDIR || info.Mode&0022 != 0 {
			_ = unix.Close(descriptor)
			_ = current.Close()
			return nil, fieldError("path", "store.identity", "directory_handle", statErr)
		}
		child := &memoryDirectoryHandle{descriptor: descriptor, path: filepath.Join(current.path, component), rootIdentity: current.rootIdentity, components: append([]string(nil), current.components...)}
		identity, identityErr := child.Identity()
		if identityErr != nil {
			_ = child.Close()
			_ = current.Close()
			return nil, identityErr
		}
		child.components = append(child.components, identity)
		_ = current.Close()
		current = child
	}
	return current, nil
}

// CreateExclusive creates a private temporary relative to the fixed dirfd.
func (directory *memoryDirectoryHandle) CreateExclusive(name string) (*os.File, error) {
	descriptor, err := unix.Openat(directory.descriptor, name, unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0600)
	if err != nil {
		return nil, err
	}
	if err = verifyUnixHandle(descriptor); err != nil {
		_ = unix.Close(descriptor)
		return nil, err
	}
	return os.NewFile(uintptr(descriptor), name), nil
}

// OpenRead opens one child relative to the fixed dirfd without following its final component.
func (directory *memoryDirectoryHandle) OpenRead(name string) (*os.File, error) {
	descriptor, err := unix.Openat(directory.descriptor, name, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		return nil, err
	}
	if err = verifyUnixHandle(descriptor); err != nil {
		_ = unix.Close(descriptor)
		return nil, err
	}
	return os.NewFile(uintptr(descriptor), name), nil
}

// OpenAppend opens or creates one append-only child relative to the fixed dirfd.
func (directory *memoryDirectoryHandle) OpenAppend(name string) (*os.File, error) {
	descriptor, err := unix.Openat(directory.descriptor, name, unix.O_WRONLY|unix.O_APPEND|unix.O_CREAT|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0600)
	if err != nil {
		return nil, err
	}
	if err = verifyUnixHandle(descriptor); err != nil {
		_ = unix.Close(descriptor)
		return nil, err
	}
	return os.NewFile(uintptr(descriptor), name), nil
}

// OpenLock opens one lock child relative to the fixed dirfd.
func (directory *memoryDirectoryHandle) OpenLock(name string) (*os.File, error) {
	descriptor, err := unix.Openat(directory.descriptor, name, unix.O_RDWR|unix.O_CREAT|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0600)
	if err != nil {
		return nil, err
	}
	if err = verifyUnixHandle(descriptor); err != nil {
		_ = unix.Close(descriptor)
		return nil, err
	}
	return os.NewFile(uintptr(descriptor), name), nil
}

// ReadDirNames enumerates children from the fixed directory handle without resolving its pathname again.
func (directory *memoryDirectoryHandle) ReadDirNames() ([]string, error) {
	descriptor, err := unix.Dup(directory.descriptor)
	if err != nil {
		return nil, err
	}
	file := os.NewFile(uintptr(descriptor), directory.path)
	defer file.Close()
	entries, err := file.ReadDir(-1)
	if err != nil {
		return nil, err
	}
	names := make([]string, len(entries))
	for index, entry := range entries {
		if !entry.IsDir() || entry.Type()&os.ModeSymlink != 0 {
			return nil, fieldError("integrity", "store.receipts", "project", nil)
		}
		names[index] = entry.Name()
	}
	return names, nil
}

// Replace atomically renames one child over another within the same fixed dirfd.
func (directory *memoryDirectoryHandle) Replace(source, target string) error {
	return unix.Renameat(directory.descriptor, source, directory.descriptor, target)
}

// Remove removes only the owned temporary from the fixed dirfd.
func (directory *memoryDirectoryHandle) Remove(name string) error {
	return unix.Unlinkat(directory.descriptor, name, 0)
}

// Sync confirms directory metadata durability.
func (directory *memoryDirectoryHandle) Sync() error { return unix.Fsync(directory.descriptor) }

// Close releases the publication dirfd.
func (directory *memoryDirectoryHandle) Close() error { return unix.Close(directory.descriptor) }
