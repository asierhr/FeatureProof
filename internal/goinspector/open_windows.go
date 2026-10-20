//go:build windows

package goinspector

import (
	"errors"
	"os"
	"path/filepath"

	"golang.org/x/sys/windows"
)

// openNoFollow opens and validates the same Windows handle while refusing every reparse point.
func openNoFollow(path string, directory bool) (*os.File, error) {
	pointer, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return nil, err
	}
	handle, err := windows.CreateFile(pointer, windows.GENERIC_READ, windows.FILE_SHARE_READ, nil, windows.OPEN_EXISTING, windows.FILE_FLAG_OPEN_REPARSE_POINT|windows.FILE_FLAG_BACKUP_SEMANTICS, 0)
	if err != nil {
		return nil, err
	}
	var info windows.ByHandleFileInformation
	if err = windows.GetFileInformationByHandle(handle, &info); err != nil || info.FileAttributes&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0 || directory != (info.FileAttributes&windows.FILE_ATTRIBUTE_DIRECTORY != 0) {
		_ = windows.CloseHandle(handle)
		if err != nil {
			return nil, err
		}
		return nil, errors.New("repository handle is not an authorized plain object")
	}
	return os.NewFile(uintptr(handle), path), nil
}

// openChildNoFollow opens a Windows child while retained ancestor handles prevent replacement or deletion.
func openChildNoFollow(parent *os.File, name string, directory bool) (*os.File, error) {
	return openNoFollow(filepath.Join(parent.Name(), name), directory)
}
