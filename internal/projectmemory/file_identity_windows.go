//go:build windows

package projectmemory

import (
	"os"

	"golang.org/x/sys/windows"
)

// verifyFileIdentity rejects reparse points and mutable files with additional hardlink aliases on Windows hosts.
func verifyFileIdentity(path string) error {
	file, err := os.Open(path)
	if err != nil {
		return fieldError("path", "store.identity", "file", err)
	}
	defer file.Close()
	var identity windows.ByHandleFileInformation
	if err = windows.GetFileInformationByHandle(windows.Handle(file.Fd()), &identity); err != nil {
		return fieldError("path", "store.identity", "handle", err)
	}
	if identity.FileAttributes&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0 || identity.NumberOfLinks != 1 {
		return fieldError("path", "store.identity", "alias", nil)
	}
	return nil
}
