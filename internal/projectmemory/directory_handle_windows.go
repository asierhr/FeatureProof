//go:build windows

package projectmemory

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/sys/windows"
)

// memoryDirectoryHandle holds a directory without delete sharing so it cannot be swapped during publication.
type memoryDirectoryHandle struct {
	handle       windows.Handle
	ancestors    []windows.Handle
	path         string
	rootIdentity string
	components   []string
}

// RootIdentity reports the initially opened root identity used for relative traversal.
func (directory *memoryDirectoryHandle) RootIdentity() string { return directory.rootIdentity }

// ComponentIdentities returns detached identities for each traversed descendant component.
func (directory *memoryDirectoryHandle) ComponentIdentities() []string {
	return append([]string(nil), directory.components...)
}

// Identity returns the volume/file identity of the fixed effective directory.
func (directory *memoryDirectoryHandle) Identity() (string, error) {
	var info windows.ByHandleFileInformation
	if err := windows.GetFileInformationByHandle(directory.handle, &info); err != nil || info.FileAttributes&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0 || info.FileAttributes&windows.FILE_ATTRIBUTE_DIRECTORY == 0 {
		return "", fieldError("path", "store.identity", "directory_handle", err)
	}
	return fmt.Sprintf("%d:%d:%d", info.VolumeSerialNumber, info.FileIndexHigh, info.FileIndexLow), nil
}

// openMemoryDirectoryTree holds every ancestor against rename while traversing each no-reparse component.
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
	path := root
	for _, component := range strings.Split(clean, string(filepath.Separator)) {
		if component == "" || component == "." || component == ".." {
			_ = current.Close()
			return nil, fieldError("path", "store.path", "component", nil)
		}
		path = filepath.Join(path, component)
		if create {
			if mkdirErr := os.Mkdir(path, 0700); mkdirErr != nil && !errors.Is(mkdirErr, os.ErrExist) {
				_ = current.Close()
				return nil, mkdirErr
			}
			if permissionErr := secureMemoryPermissions(path); permissionErr != nil {
				_ = current.Close()
				return nil, permissionErr
			}
		}
		child, openErr := openMemoryDirectoryHandle(path)
		if openErr != nil {
			_ = current.Close()
			return nil, openErr
		}
		child.ancestors = append(append(child.ancestors, current.ancestors...), current.handle)
		child.rootIdentity = current.rootIdentity
		child.components = append([]string(nil), current.components...)
		current.ancestors = nil
		current.handle = 0
		identity, identityErr := child.Identity()
		if identityErr != nil {
			_ = child.Close()
			return nil, identityErr
		}
		child.components = append(child.components, identity)
		current = child
	}
	return current, nil
}

// openMemoryDirectoryHandle opens the directory itself with no reparse traversal and denies rename/delete sharing.
func openMemoryDirectoryHandle(path string) (*memoryDirectoryHandle, error) {
	pointer, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return nil, err
	}
	handle, err := windows.CreateFile(pointer, windows.FILE_READ_ATTRIBUTES|windows.FILE_LIST_DIRECTORY|windows.READ_CONTROL, windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE, nil, windows.OPEN_EXISTING, windows.FILE_FLAG_BACKUP_SEMANTICS|windows.FILE_FLAG_OPEN_REPARSE_POINT, 0)
	if err != nil {
		return nil, err
	}
	var info windows.ByHandleFileInformation
	if err = windows.GetFileInformationByHandle(handle, &info); err != nil || info.FileAttributes&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0 || info.FileAttributes&windows.FILE_ATTRIBUTE_DIRECTORY == 0 || verifyWindowsACL(handle) != nil {
		_ = windows.CloseHandle(handle)
		return nil, fieldError("path", "store.identity", "directory_handle", err)
	}
	directory := &memoryDirectoryHandle{handle: handle, path: path}
	identity, identityErr := directory.Identity()
	if identityErr != nil {
		_ = directory.Close()
		return nil, identityErr
	}
	directory.rootIdentity = identity
	return directory, nil
}

// CreateExclusive creates a private temporary while the parent directory is fixed against swaps.
func (directory *memoryDirectoryHandle) CreateExclusive(name string) (*os.File, error) {
	return openMemoryExclusive(filepath.Join(directory.path, name))
}

// OpenRead opens one child no-follow while the parent directory cannot be swapped.
func (directory *memoryDirectoryHandle) OpenRead(name string) (*os.File, error) {
	return openMemoryRead(filepath.Join(directory.path, name))
}

// OpenAppend opens one append-only child while the parent directory cannot be swapped.
func (directory *memoryDirectoryHandle) OpenAppend(name string) (*os.File, error) {
	return openMemoryAppend(filepath.Join(directory.path, name))
}

// OpenLock opens one lock child while the parent directory cannot be swapped.
func (directory *memoryDirectoryHandle) OpenLock(name string) (*os.File, error) {
	return openMemoryLock(filepath.Join(directory.path, name))
}

// ReadDirNames enumerates children from a duplicate of the fixed directory handle.
func (directory *memoryDirectoryHandle) ReadDirNames() ([]string, error) {
	var duplicate windows.Handle
	if err := windows.DuplicateHandle(windows.CurrentProcess(), directory.handle, windows.CurrentProcess(), &duplicate, 0, false, windows.DUPLICATE_SAME_ACCESS); err != nil {
		return nil, err
	}
	file := os.NewFile(uintptr(duplicate), directory.path)
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

// Replace atomically publishes inside the directory held against rename.
func (directory *memoryDirectoryHandle) Replace(source, target string) error {
	return atomicReplace(filepath.Join(directory.path, source), filepath.Join(directory.path, target))
}

// Remove removes only the owned temporary while its directory remains fixed.
func (directory *memoryDirectoryHandle) Remove(name string) error {
	return os.Remove(filepath.Join(directory.path, name))
}

// Sync is satisfied by MoveFileEx WRITE_THROUGH and the held Windows directory handle.
func (directory *memoryDirectoryHandle) Sync() error { return nil }

// Close releases the directory handle after publication completes.
func (directory *memoryDirectoryHandle) Close() error {
	var result error
	if directory.handle != 0 {
		result = windows.CloseHandle(directory.handle)
	}
	for _, handle := range directory.ancestors {
		if err := windows.CloseHandle(handle); result == nil {
			result = err
		}
	}
	directory.handle, directory.ancestors = 0, nil
	return result
}
