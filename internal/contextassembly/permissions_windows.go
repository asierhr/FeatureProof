//go:build windows

package contextassembly

import (
	"fmt"
	"unsafe"

	"golang.org/x/sys/windows"
)

// secureStorePermissions installs a protected owner, SYSTEM and administrators DACL on a new directory.
func secureStorePermissions(path string) error {
	token, err := windows.OpenCurrentProcessToken()
	if err != nil {
		return err
	}
	defer token.Close()
	user, err := token.GetTokenUser()
	if err != nil || user == nil || user.User.Sid == nil {
		return err
	}
	descriptor, err := windows.SecurityDescriptorFromString(fmt.Sprintf("D:P(A;OICI;FA;;;%s)(A;OICI;FA;;;SY)(A;OICI;FA;;;BA)", user.User.Sid.String()))
	if err != nil {
		return err
	}
	dacl, _, err := descriptor.DACL()
	if err != nil {
		return err
	}
	return windows.SetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION, nil, nil, dacl, nil)
}

// verifyStorePathPermissions rejects writable Windows ACEs granted outside the owner, SYSTEM or administrators.
func verifyStorePathPermissions(path string) error {
	descriptor, err := windows.GetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION|windows.OWNER_SECURITY_INFORMATION)
	if err != nil {
		return err
	}
	return verifyStoreDescriptor(descriptor)
}

// verifyStoreDescriptor enforces the allowlist shared by durable directories and opened object handles.
func verifyStoreDescriptor(descriptor *windows.SECURITY_DESCRIPTOR) error {
	dacl, _, err := descriptor.DACL()
	owner, _, ownerErr := descriptor.Owner()
	if err != nil || ownerErr != nil || dacl == nil || owner == nil {
		return windows.ERROR_ACCESS_DENIED
	}
	system, systemErr := windows.CreateWellKnownSid(windows.WinLocalSystemSid)
	administrators, adminErr := windows.CreateWellKnownSid(windows.WinBuiltinAdministratorsSid)
	if systemErr != nil || adminErr != nil {
		return windows.ERROR_ACCESS_DENIED
	}
	allowed := map[string]bool{owner.String(): true, system.String(): true, administrators.String(): true}
	const writeMask = windows.GENERIC_WRITE | windows.GENERIC_ALL | windows.WRITE_DAC | windows.WRITE_OWNER | windows.DELETE | windows.FILE_WRITE_DATA | windows.FILE_APPEND_DATA | windows.FILE_WRITE_ATTRIBUTES | windows.FILE_WRITE_EA
	for index := uint32(0); index < uint32(dacl.AceCount); index++ {
		var ace *windows.ACCESS_ALLOWED_ACE
		if err = windows.GetAce(dacl, index, &ace); err != nil || ace == nil {
			return windows.ERROR_ACCESS_DENIED
		}
		if ace.Header.AceType != windows.ACCESS_ALLOWED_ACE_TYPE || uint32(ace.Mask)&writeMask == 0 {
			continue
		}
		sid := (*windows.SID)(unsafe.Pointer(&ace.SidStart))
		if !sid.IsValid() || !allowed[sid.String()] {
			return windows.ERROR_ACCESS_DENIED
		}
	}
	return nil
}
