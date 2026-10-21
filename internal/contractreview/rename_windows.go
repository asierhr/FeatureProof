//go:build windows

package contractreview

import (
	"os"
	"unsafe"

	"golang.org/x/sys/windows"
)

// fileRenameInformation describes one descriptor-relative NT rename request.
type fileRenameInformation struct {
	ReplaceIfExists uint32
	RootDirectory   windows.Handle
	FileNameLength  uint32
	FileName        [1]uint16
}

// atomicCommitInDirectory retains a no-delete-share parent handle and rejects reparse directories through commit.
func atomicCommitInDirectory(parentHandle *os.File, _ string, sourceName, targetName string, replacing bool) error {
	directory := windows.Handle(parentHandle.Fd())
	var directoryInfo windows.ByHandleFileInformation
	if err := windows.GetFileInformationByHandle(directory, &directoryInfo); err != nil {
		return err
	}
	if directoryInfo.FileAttributes&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0 {
		return os.ErrInvalid
	}
	objectName, err := windows.NewNTUnicodeString(sourceName)
	if err != nil {
		return err
	}
	attributes := &windows.OBJECT_ATTRIBUTES{RootDirectory: directory, ObjectName: objectName}
	attributes.Length = uint32(unsafe.Sizeof(*attributes))
	var source windows.Handle
	var status windows.IO_STATUS_BLOCK
	if err = windows.NtCreateFile(&source, windows.DELETE|windows.SYNCHRONIZE, attributes, &status, nil, windows.FILE_ATTRIBUTE_NORMAL, windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE, windows.FILE_OPEN, windows.FILE_NON_DIRECTORY_FILE|windows.FILE_OPEN_REPARSE_POINT|windows.FILE_SYNCHRONOUS_IO_NONALERT, 0, 0); err != nil {
		return err
	}
	defer windows.CloseHandle(source)
	target, err := windows.UTF16FromString(targetName)
	if err != nil {
		return err
	}
	fileNameLength := len(target)*2 - 2
	var shape fileRenameInformation
	buffer := make([]byte, int(unsafe.Offsetof(shape.FileName))+fileNameLength)
	information := (*fileRenameInformation)(unsafe.Pointer(&buffer[0]))
	information.RootDirectory = directory
	if replacing {
		information.ReplaceIfExists = windows.FILE_RENAME_REPLACE_IF_EXISTS | windows.FILE_RENAME_POSIX_SEMANTICS
	}
	information.FileNameLength = uint32(fileNameLength)
	copy((*[windows.MAX_LONG_PATH]uint16)(unsafe.Pointer(&information.FileName[0]))[:fileNameLength/2:fileNameLength/2], target)
	return windows.NtSetInformationFile(source, &status, &buffer[0], uint32(len(buffer)), windows.FileRenameInformation)
}
