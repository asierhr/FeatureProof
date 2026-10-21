//go:build windows

package contractreview

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"unsafe"

	"golang.org/x/sys/windows"
)

// descriptorIsNotExist normalizes NT relative-open absence for store idempotency semantics.
func descriptorIsNotExist(err error) bool {
	return errors.Is(err, windows.STATUS_OBJECT_NAME_NOT_FOUND) || errors.Is(err, windows.STATUS_OBJECT_PATH_NOT_FOUND) || errors.Is(err, windows.ERROR_FILE_NOT_FOUND) || errors.Is(err, windows.ERROR_PATH_NOT_FOUND)
}

// descriptorIsExist normalizes NT exclusive-create collisions.
func descriptorIsExist(err error) bool {
	return errors.Is(err, windows.STATUS_OBJECT_NAME_COLLISION) || errors.Is(err, windows.ERROR_FILE_EXISTS) || errors.Is(err, windows.ERROR_ALREADY_EXISTS)
}

// openRegularInDirectory opens one final file through its parent handle and refuses reparse traversal.
func openRegularInDirectory(parent *os.File, name string) (*os.File, error) {
	objectName, err := windows.NewNTUnicodeString(name)
	if err != nil {
		return nil, err
	}
	attributes := &windows.OBJECT_ATTRIBUTES{RootDirectory: windows.Handle(parent.Fd()), ObjectName: objectName}
	attributes.Length = uint32(unsafe.Sizeof(*attributes))
	var handle windows.Handle
	var status windows.IO_STATUS_BLOCK
	err = windows.NtCreateFile(&handle, windows.FILE_GENERIC_READ, attributes, &status, nil, windows.FILE_ATTRIBUTE_NORMAL, windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE, windows.FILE_OPEN, windows.FILE_NON_DIRECTORY_FILE|windows.FILE_OPEN_REPARSE_POINT|windows.FILE_SYNCHRONOUS_IO_NONALERT, 0, 0)
	if err != nil {
		return nil, err
	}
	var info windows.ByHandleFileInformation
	if err = windows.GetFileInformationByHandle(handle, &info); err != nil || info.FileAttributes&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0 {
		_ = windows.CloseHandle(handle)
		if err != nil {
			return nil, err
		}
		return nil, os.ErrInvalid
	}
	return os.NewFile(uintptr(handle), name), nil
}

// openMutableFileInDirectory opens or creates one non-reparse child relative to its retained parent.
func openMutableFileInDirectory(parent *os.File, name string, access, disposition uint32) (*os.File, error) {
	objectName, err := windows.NewNTUnicodeString(name)
	if err != nil {
		return nil, err
	}
	attributes := &windows.OBJECT_ATTRIBUTES{RootDirectory: windows.Handle(parent.Fd()), ObjectName: objectName}
	attributes.Length = uint32(unsafe.Sizeof(*attributes))
	var handle windows.Handle
	var status windows.IO_STATUS_BLOCK
	err = windows.NtCreateFile(&handle, access|windows.SYNCHRONIZE, attributes, &status, nil, windows.FILE_ATTRIBUTE_NORMAL, windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE, disposition, windows.FILE_NON_DIRECTORY_FILE|windows.FILE_OPEN_REPARSE_POINT|windows.FILE_SYNCHRONOUS_IO_NONALERT, 0, 0)
	if err != nil {
		return nil, err
	}
	var info windows.ByHandleFileInformation
	if err = windows.GetFileInformationByHandle(handle, &info); err != nil || info.FileAttributes&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0 {
		_ = windows.CloseHandle(handle)
		if err != nil {
			return nil, err
		}
		return nil, os.ErrInvalid
	}
	return os.NewFile(uintptr(handle), name), nil
}

// createExclusiveInDirectory creates one immutable index name relative to its directory.
func createExclusiveInDirectory(parent *os.File, name string) (*os.File, error) {
	return openMutableFileInDirectory(parent, name, windows.FILE_GENERIC_READ|windows.FILE_GENERIC_WRITE|windows.DELETE, windows.FILE_CREATE)
}

// removeInDirectory marks one no-follow child for deletion through its parent-relative handle.
func removeInDirectory(parent *os.File, name string) error {
	file, err := openMutableFileInDirectory(parent, name, windows.DELETE|windows.FILE_READ_ATTRIBUTES, windows.FILE_OPEN)
	if err != nil {
		return err
	}
	deleteFile := byte(1)
	err = windows.SetFileInformationByHandle(windows.Handle(file.Fd()), windows.FileDispositionInfo, &deleteFile, 1)
	closeErr := file.Close()
	if err == nil {
		err = closeErr
	}
	return err
}

// fileLinkCountHandle reports aliasing from the already-open no-follow Windows handle.
func fileLinkCountHandle(file *os.File) (uint64, error) {
	var info windows.ByHandleFileInformation
	if err := windows.GetFileInformationByHandle(windows.Handle(file.Fd()), &info); err != nil {
		return 0, err
	}
	if info.FileAttributes&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0 {
		return 0, os.ErrInvalid
	}
	return uint64(info.NumberOfLinks), nil
}

// createTempInDirectory creates a private file through an NT path relative to the retained parent handle.
func createTempInDirectory(parent *os.File, parentPath, prefix string) (*os.File, error) {
	for attempt := 0; attempt < 100; attempt++ {
		random := make([]byte, 8)
		if _, err := rand.Read(random); err != nil {
			return nil, err
		}
		name := prefix + hex.EncodeToString(random)
		objectName, err := windows.NewNTUnicodeString(name)
		if err != nil {
			return nil, err
		}
		attributes := &windows.OBJECT_ATTRIBUTES{RootDirectory: windows.Handle(parent.Fd()), ObjectName: objectName}
		attributes.Length = uint32(unsafe.Sizeof(*attributes))
		var handle windows.Handle
		var status windows.IO_STATUS_BLOCK
		err = windows.NtCreateFile(&handle, windows.FILE_GENERIC_READ|windows.FILE_GENERIC_WRITE|windows.DELETE, attributes, &status, nil, windows.FILE_ATTRIBUTE_NORMAL, windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE, windows.FILE_CREATE, windows.FILE_NON_DIRECTORY_FILE|windows.FILE_OPEN_REPARSE_POINT|windows.FILE_SYNCHRONOUS_IO_NONALERT, 0, 0)
		if errors.Is(err, windows.STATUS_OBJECT_NAME_COLLISION) {
			continue
		}
		if err != nil {
			return nil, err
		}
		return os.NewFile(uintptr(handle), filepath.Join(parentPath, name)), nil
	}
	return nil, os.ErrExist
}

// resetFileInDirectory reopens one known private temp through its parent handle and truncates partial crash bytes.
func resetFileInDirectory(parent *os.File, parentPath, name string) (*os.File, error) {
	objectName, err := windows.NewNTUnicodeString(name)
	if err != nil {
		return nil, err
	}
	attributes := &windows.OBJECT_ATTRIBUTES{RootDirectory: windows.Handle(parent.Fd()), ObjectName: objectName}
	attributes.Length = uint32(unsafe.Sizeof(*attributes))
	var handle windows.Handle
	var status windows.IO_STATUS_BLOCK
	err = windows.NtCreateFile(&handle, windows.FILE_GENERIC_READ|windows.FILE_GENERIC_WRITE|windows.DELETE|windows.SYNCHRONIZE, attributes, &status, nil, windows.FILE_ATTRIBUTE_NORMAL, windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE, windows.FILE_OVERWRITE, windows.FILE_NON_DIRECTORY_FILE|windows.FILE_OPEN_REPARSE_POINT|windows.FILE_SYNCHRONOUS_IO_NONALERT, 0, 0)
	if err != nil {
		return nil, err
	}
	return os.NewFile(uintptr(handle), filepath.Join(parentPath, name)), nil
}

// duplicateDirectoryHandle gives callers ownership of a stable copy of the configured root handle.
func duplicateDirectoryHandle(root *os.File) (*os.File, error) {
	if _, err := root.Stat(); err != nil {
		return nil, err
	}
	process := windows.CurrentProcess()
	var duplicate windows.Handle
	err := windows.DuplicateHandle(process, windows.Handle(root.Fd()), process, &duplicate, 0, false, windows.DUPLICATE_SAME_ACCESS)
	if err != nil {
		return nil, err
	}
	return os.NewFile(uintptr(duplicate), root.Name()), nil
}

// openRelativeDirectory walks from a retained root handle with FILE_OPEN_REPARSE_POINT on every component.
func openRelativeDirectory(root *os.File, relative string, create bool) (*os.File, error) {
	current, err := duplicateDirectoryHandle(root)
	if err != nil {
		return nil, err
	}
	for _, component := range strings.Split(filepath.Clean(relative), string(os.PathSeparator)) {
		if component == "" || component == "." {
			continue
		}
		name, nameErr := windows.NewNTUnicodeString(component)
		if nameErr != nil {
			_ = current.Close()
			return nil, nameErr
		}
		attributes := &windows.OBJECT_ATTRIBUTES{RootDirectory: windows.Handle(current.Fd()), ObjectName: name}
		attributes.Length = uint32(unsafe.Sizeof(*attributes))
		var handle windows.Handle
		var status windows.IO_STATUS_BLOCK
		disposition := uint32(windows.FILE_OPEN)
		if create {
			disposition = windows.FILE_OPEN_IF
		}
		openErr := windows.NtCreateFile(&handle, windows.FILE_GENERIC_READ|windows.FILE_GENERIC_WRITE, attributes, &status, nil, windows.FILE_ATTRIBUTE_NORMAL, windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE, disposition, windows.FILE_DIRECTORY_FILE|windows.FILE_OPEN_REPARSE_POINT|windows.FILE_SYNCHRONOUS_IO_NONALERT, 0, 0)
		if openErr != nil {
			_ = current.Close()
			return nil, openErr
		}
		_ = current.Close()
		current = os.NewFile(uintptr(handle), component)
		info, statErr := current.Stat()
		if statErr != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			_ = current.Close()
			if statErr != nil {
				return nil, statErr
			}
			return nil, windows.ERROR_REPARSE_TAG_INVALID
		}
	}
	return current, nil
}
