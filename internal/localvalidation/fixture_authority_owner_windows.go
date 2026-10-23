//go:build windows

package localvalidation

import (
	"os"
	"unsafe"

	"golang.org/x/sys/windows"
)

// fixtureAuthorityFullControl is the canonical file-specific expansion of GENERIC_ALL stored in an authority DACL.
const fixtureAuthorityFullControl windows.ACCESS_MASK = windows.STANDARD_RIGHTS_REQUIRED | windows.SYNCHRONIZE | 0x1ff

// protectFixtureAuthorityFile installs a non-inherited DACL limited to owner, LocalSystem and Administrators before content is written.
func protectFixtureAuthorityFile(path string) error {
	sids, err := trustedFixtureAuthoritySIDs()
	if err != nil {
		return err
	}
	entries := make([]windows.EXPLICIT_ACCESS, 0, len(sids))
	for _, sid := range sids {
		entries = append(entries, windows.EXPLICIT_ACCESS{
			AccessPermissions: fixtureAuthorityFullControl,
			AccessMode:        windows.SET_ACCESS,
			Inheritance:       windows.NO_INHERITANCE,
			Trustee: windows.TRUSTEE{
				TrusteeForm:  windows.TRUSTEE_IS_SID,
				TrusteeType:  windows.TRUSTEE_IS_WELL_KNOWN_GROUP,
				TrusteeValue: windows.TrusteeValueFromSID(sid),
			},
		})
	}
	entries[0].Trustee.TrusteeType = windows.TRUSTEE_IS_USER
	acl, err := windows.ACLFromEntries(entries, nil)
	if err != nil {
		return err
	}
	return windows.SetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION, nil, nil, acl, nil)
}

// fixtureAuthorityOwnerOK accepts only the exact protected DACL created for owner, LocalSystem and Administrators.
func fixtureAuthorityOwnerOK(path string, _ os.FileInfo) bool {
	descriptor, err := windows.GetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, windows.OWNER_SECURITY_INFORMATION|windows.DACL_SECURITY_INFORMATION)
	if err != nil || descriptor == nil || !descriptor.IsValid() {
		return false
	}
	owner, ownerDefaulted, ownerErr := descriptor.Owner()
	dacl, daclDefaulted, daclErr := descriptor.DACL()
	control, _, controlErr := descriptor.Control()
	trusted, trustedErr := trustedFixtureAuthoritySIDs()
	if ownerErr != nil || owner == nil || ownerDefaulted || daclErr != nil || daclDefaulted || dacl == nil || controlErr != nil || control&windows.SE_DACL_PROTECTED == 0 || control&windows.SE_DACL_PRESENT == 0 || trustedErr != nil || !owner.Equals(trusted[0]) || int(dacl.AceCount) != len(trusted) {
		return false
	}
	seen := make([]bool, len(trusted))
	for index := uint32(0); index < uint32(dacl.AceCount); index++ {
		var ace *windows.ACCESS_ALLOWED_ACE
		if windows.GetAce(dacl, index, &ace) != nil || ace == nil || ace.Header.AceType != windows.ACCESS_ALLOWED_ACE_TYPE || ace.Header.AceFlags&windows.INHERITED_ACE != 0 || ace.Mask != fixtureAuthorityFullControl {
			return false
		}
		sid := (*windows.SID)(unsafe.Pointer(&ace.SidStart))
		matched := -1
		for candidate, allowed := range trusted {
			if sid.Equals(allowed) {
				matched = candidate
				break
			}
		}
		if matched < 0 || seen[matched] {
			return false
		}
		seen[matched] = true
	}
	return true
}

// trustedFixtureAuthoritySIDs returns the deduplicated principals allowed to control persistent audit authority.
func trustedFixtureAuthoritySIDs() ([]*windows.SID, error) {
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		return nil, err
	}
	system, err := windows.CreateWellKnownSid(windows.WinLocalSystemSid)
	if err != nil {
		return nil, err
	}
	administrators, err := windows.CreateWellKnownSid(windows.WinBuiltinAdministratorsSid)
	if err != nil {
		return nil, err
	}
	result := []*windows.SID{user.User.Sid}
	for _, candidate := range []*windows.SID{system, administrators} {
		duplicate := false
		for _, existing := range result {
			if candidate.Equals(existing) {
				duplicate = true
				break
			}
		}
		if !duplicate {
			result = append(result, candidate)
		}
	}
	return result, nil
}
