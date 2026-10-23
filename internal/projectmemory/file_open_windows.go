//go:build windows

package projectmemory

import (
	"fmt"
	"os"
	"unsafe"

	"golang.org/x/sys/windows"
)

// openMemoryRead opens the effective journal handle while refusing final-component reparse traversal.
func openMemoryRead(path string) (*os.File, error) {
	return openMemoryWindows(path, windows.GENERIC_READ, windows.OPEN_EXISTING)
}

// openMemoryAppend opens the effective journal handle for append while refusing final-component reparse traversal.
func openMemoryAppend(path string) (*os.File, error) {
	return openMemoryWindows(path, windows.FILE_APPEND_DATA|windows.SYNCHRONIZE|windows.READ_CONTROL, windows.OPEN_ALWAYS)
}

// openMemoryLock creates the exact lock handle while refusing reparse points and delete sharing.
func openMemoryLock(path string) (*os.File, error) {
	pointer, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return nil, err
	}
	handle, err := windows.CreateFile(pointer, windows.GENERIC_READ|windows.GENERIC_WRITE, windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE, nil, windows.OPEN_ALWAYS, windows.FILE_ATTRIBUTE_NORMAL|windows.FILE_FLAG_OPEN_REPARSE_POINT, 0)
	if err != nil {
		return nil, err
	}
	file := os.NewFile(uintptr(handle), path)
	var info windows.ByHandleFileInformation
	if err = windows.GetFileInformationByHandle(handle, &info); err != nil || info.FileAttributes&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0 || info.NumberOfLinks != 1 || info.FileAttributes&windows.FILE_ATTRIBUTE_DIRECTORY != 0 {
		_ = file.Close()
		return nil, fieldError("path", "store.identity", "lock", err)
	}
	if err = verifyWindowsACL(handle); err != nil {
		_ = file.Close()
		return nil, err
	}
	return file, nil
}

// openMemoryExclusive creates an owned temporary and refuses any pre-existing alias.
func openMemoryExclusive(path string) (*os.File, error) {
	pointer, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return nil, err
	}
	handle, err := windows.CreateFile(pointer, windows.GENERIC_READ|windows.GENERIC_WRITE|windows.READ_CONTROL|windows.DELETE, windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE, nil, windows.CREATE_NEW, windows.FILE_ATTRIBUTE_NORMAL|windows.FILE_FLAG_OPEN_REPARSE_POINT, 0)
	if err != nil {
		return nil, err
	}
	file := os.NewFile(uintptr(handle), path)
	if _, err = memoryOpenFileIdentity(file); err != nil || verifyWindowsACL(handle) != nil {
		_ = file.Close()
		return nil, fieldError("path", "store.identity", "temporary", err)
	}
	return file, nil
}

// openMemoryWindows creates an owned Go file from the exact validated Windows kernel handle.
func openMemoryWindows(path string, access uint32, disposition uint32) (*os.File, error) {
	pointer, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return nil, err
	}
	handle, err := windows.CreateFile(pointer, access, windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE, nil, disposition, windows.FILE_ATTRIBUTE_NORMAL|windows.FILE_FLAG_OPEN_REPARSE_POINT, 0)
	if err != nil {
		return nil, err
	}
	file := os.NewFile(uintptr(handle), path)
	var info windows.ByHandleFileInformation
	if err = windows.GetFileInformationByHandle(handle, &info); err != nil || info.FileAttributes&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0 || info.NumberOfLinks != 1 || info.FileAttributes&windows.FILE_ATTRIBUTE_DIRECTORY != 0 {
		_ = file.Close()
		return nil, fieldError("path", "store.identity", "handle", err)
	}
	if err = verifyWindowsACL(handle); err != nil {
		_ = file.Close()
		return nil, err
	}
	return file, nil
}

// memoryOpenFileIdentity returns the volume/file identity of the exact effective file handle.
func memoryOpenFileIdentity(file *os.File) (string, error) {
	if file == nil {
		return "", fieldError("path", "store.identity", "file_handle", nil)
	}
	var info windows.ByHandleFileInformation
	if err := windows.GetFileInformationByHandle(windows.Handle(file.Fd()), &info); err != nil || info.FileAttributes&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0 || info.NumberOfLinks != 1 || info.FileAttributes&windows.FILE_ATTRIBUTE_DIRECTORY != 0 {
		return "", fieldError("path", "store.identity", "file_handle", err)
	}
	return fmt.Sprintf("%d:%d:%d", info.VolumeSerialNumber, info.FileIndexHigh, info.FileIndexLow), nil
}

// memoryDirectoryIdentity returns a stable volume/file identity from a no-follow root handle.
func memoryDirectoryIdentity(path string) (string, error) {
	pointer, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return "", err
	}
	handle, err := windows.CreateFile(pointer, windows.FILE_READ_ATTRIBUTES|windows.READ_CONTROL, windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE, nil, windows.OPEN_EXISTING, windows.FILE_FLAG_BACKUP_SEMANTICS|windows.FILE_FLAG_OPEN_REPARSE_POINT, 0)
	if err != nil {
		return "", err
	}
	defer windows.CloseHandle(handle)
	var info windows.ByHandleFileInformation
	if err = windows.GetFileInformationByHandle(handle, &info); err != nil || info.FileAttributes&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0 || info.FileAttributes&windows.FILE_ATTRIBUTE_DIRECTORY == 0 {
		return "", fieldError("path", "store.identity", "root", err)
	}
	if err = verifyWindowsACL(handle); err != nil {
		return "", err
	}
	return fmt.Sprintf("%d:%d:%d", info.VolumeSerialNumber, info.FileIndexHigh, info.FileIndexLow), nil
}

// verifyWindowsACL rejects writable access-control entries for principals outside the owner, SYSTEM, and administrators.
func verifyWindowsACL(handle windows.Handle) error {
	descriptor, err := windows.GetSecurityInfo(handle, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION|windows.OWNER_SECURITY_INFORMATION)
	if err != nil {
		return fieldError("path", "store.permissions", "acl", err)
	}
	dacl, _, err := descriptor.DACL()
	owner, _, ownerErr := descriptor.Owner()
	if err != nil || ownerErr != nil || dacl == nil || owner == nil {
		return fieldError("path", "store.permissions", "acl", err)
	}
	system, systemErr := windows.CreateWellKnownSid(windows.WinLocalSystemSid)
	administrators, adminErr := windows.CreateWellKnownSid(windows.WinBuiltinAdministratorsSid)
	if systemErr != nil || adminErr != nil {
		return fieldError("path", "store.permissions", "acl", systemErr)
	}
	allowed := map[string]bool{owner.String(): true, system.String(): true, administrators.String(): true}
	if token, tokenErr := windows.OpenCurrentProcessToken(); tokenErr == nil {
		if user, userErr := token.GetTokenUser(); userErr == nil && user != nil && user.User.Sid != nil {
			allowed[user.User.Sid.String()] = true
		}
		_ = token.Close()
	}
	const writeMask = windows.GENERIC_WRITE | windows.GENERIC_ALL | windows.WRITE_DAC | windows.WRITE_OWNER | windows.DELETE | windows.FILE_WRITE_DATA | windows.FILE_APPEND_DATA | windows.FILE_WRITE_ATTRIBUTES | windows.FILE_WRITE_EA
	for index := uint32(0); index < uint32(dacl.AceCount); index++ {
		var ace *windows.ACCESS_ALLOWED_ACE
		if err = windows.GetAce(dacl, index, &ace); err != nil || ace == nil {
			return fieldError("path", "store.permissions", "acl", err)
		}
		if ace.Header.AceType != windows.ACCESS_ALLOWED_ACE_TYPE || uint32(ace.Mask)&writeMask == 0 {
			continue
		}
		sid := (*windows.SID)(unsafe.Pointer(&ace.SidStart))
		if !sid.IsValid() || !allowed[sid.String()] {
			return fieldError("path", "store.permissions", "acl", nil)
		}
	}
	return nil
}
