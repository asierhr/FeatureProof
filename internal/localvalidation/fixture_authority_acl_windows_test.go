//go:build windows

package localvalidation

import (
	"path/filepath"
	"testing"

	"golang.org/x/sys/windows"
)

// TestFixtureAuthorityWindowsDACLRejectsUntrustedAccess proves reload accepts only the protected product ACL on key and pin.
func TestFixtureAuthorityWindowsDACLRejectsUntrustedAccess(t *testing.T) {
	t.Run("valid_protected_acl_reloads", func(t *testing.T) {
		root := t.TempDir()
		key, pin := filepath.Join(root, "private", "key"), filepath.Join(root, "trust", "pin")
		_, first, err := OpenFixtureAuditAuthority(key, pin)
		if err != nil {
			t.Fatal(err)
		}
		_, second, err := OpenFixtureAuditAuthority(key, pin)
		if err != nil || first.KeyID != second.KeyID {
			t.Fatalf("protected ACL did not survive reload: first=%s second=%s err=%v", first.KeyID, second.KeyID, err)
		}
	})
	principalTypes := []windows.WELL_KNOWN_SID_TYPE{windows.WinWorldSid, windows.WinBuiltinUsersSid, windows.WinAuthenticatedUserSid}
	cases := []struct {
		name        string
		pin         bool
		permissions windows.ACCESS_MASK
	}{
		{name: "private_everyone_read", permissions: windows.FILE_GENERIC_READ},
		{name: "private_users_write_delete", permissions: windows.FILE_GENERIC_WRITE | windows.DELETE},
		{name: "private_authenticated_full_control", permissions: fixtureAuthorityFullControl},
		{name: "pin_everyone_full_control", pin: true, permissions: fixtureAuthorityFullControl},
		{name: "pin_users_write_delete", pin: true, permissions: windows.FILE_GENERIC_WRITE | windows.DELETE},
		{name: "pin_authenticated_change_control", pin: true, permissions: windows.WRITE_DAC | windows.WRITE_OWNER},
	}
	for index, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			key, pin := filepath.Join(root, "private", "key"), filepath.Join(root, "trust", "pin")
			if _, _, err := OpenFixtureAuditAuthority(key, pin); err != nil {
				t.Fatal(err)
			}
			path := key
			if test.pin {
				path = pin
			}
			principal, err := windows.CreateWellKnownSid(principalTypes[index%len(principalTypes)])
			if err != nil {
				t.Fatal(err)
			}
			if err := setFixtureAuthorityTestACL(path, principal, test.permissions, true); err != nil {
				t.Fatal(err)
			}
			if _, _, err := OpenFixtureAuditAuthority(key, pin); err == nil {
				t.Fatal("hostile authority ACL accepted")
			}
		})
	}
	t.Run("unprotected_acl", func(t *testing.T) {
		root := t.TempDir()
		key, pin := filepath.Join(root, "private", "key"), filepath.Join(root, "trust", "pin")
		if _, _, err := OpenFixtureAuditAuthority(key, pin); err != nil {
			t.Fatal(err)
		}
		if err := setFixtureAuthorityTestACL(key, nil, 0, false); err != nil {
			t.Fatal(err)
		}
		if _, _, err := OpenFixtureAuditAuthority(key, pin); err == nil {
			t.Fatal("unprotected or inherited authority ACL accepted")
		}
	})
	t.Run("missing_descriptor", func(t *testing.T) {
		if fixtureAuthorityOwnerOK(filepath.Join(t.TempDir(), "missing"), nil) {
			t.Fatal("missing security descriptor accepted")
		}
	})
	t.Run("trusted_sid_with_reduced_mask", func(t *testing.T) {
		root := t.TempDir()
		key, pin := filepath.Join(root, "private", "key"), filepath.Join(root, "trust", "pin")
		if _, _, err := OpenFixtureAuditAuthority(key, pin); err != nil {
			t.Fatal(err)
		}
		trusted, _ := trustedFixtureAuthoritySIDs()
		masks := make([]windows.ACCESS_MASK, len(trusted))
		for index := range masks {
			masks[index] = fixtureAuthorityFullControl
		}
		masks[0] = windows.FILE_GENERIC_READ
		if err := setFixtureAuthorityExactTestACL(key, trusted, masks, true); err != nil {
			t.Fatal(err)
		}
		if _, _, err := OpenFixtureAuditAuthority(key, pin); err == nil {
			t.Fatal("non-canonical trusted ACE accepted")
		}
	})
	t.Run("unknown_principal_replaces_trusted_sid", func(t *testing.T) {
		root := t.TempDir()
		key, pin := filepath.Join(root, "private", "key"), filepath.Join(root, "trust", "pin")
		if _, _, err := OpenFixtureAuditAuthority(key, pin); err != nil {
			t.Fatal(err)
		}
		trusted, _ := trustedFixtureAuthoritySIDs()
		world, _ := windows.CreateWellKnownSid(windows.WinWorldSid)
		trusted[len(trusted)-1] = world
		masks := make([]windows.ACCESS_MASK, len(trusted))
		for index := range masks {
			masks[index] = fixtureAuthorityFullControl
		}
		if err := setFixtureAuthorityExactTestACL(key, trusted, masks, true); err != nil {
			t.Fatal(err)
		}
		if _, _, err := OpenFixtureAuditAuthority(key, pin); err == nil {
			t.Fatal("unknown principal replacing trusted SID accepted")
		}
	})
}

// setFixtureAuthorityTestACL installs the trusted entries plus one optional hostile ACE for Windows boundary tests.
func setFixtureAuthorityTestACL(path string, hostile *windows.SID, permissions windows.ACCESS_MASK, protected bool) error {
	trusted, err := trustedFixtureAuthoritySIDs()
	if err != nil {
		return err
	}
	permissionsBySID := make([]windows.ACCESS_MASK, len(trusted))
	for index := range permissionsBySID {
		permissionsBySID[index] = fixtureAuthorityFullControl
	}
	if hostile != nil {
		trusted = append(trusted, hostile)
		permissionsBySID = append(permissionsBySID, permissions)
	}
	return setFixtureAuthorityExactTestACL(path, trusted, permissionsBySID, protected)
}

// setFixtureAuthorityExactTestACL writes an exact protected or inheritable ACL for adversarial reload cases.
func setFixtureAuthorityExactTestACL(path string, principals []*windows.SID, permissions []windows.ACCESS_MASK, protected bool) error {
	entries := make([]windows.EXPLICIT_ACCESS, 0, len(principals))
	for index, sid := range principals {
		trusteeType := windows.TRUSTEE_TYPE(windows.TRUSTEE_IS_WELL_KNOWN_GROUP)
		if index == 0 {
			trusteeType = windows.TRUSTEE_IS_USER
		}
		entries = append(entries, windows.EXPLICIT_ACCESS{AccessPermissions: permissions[index], AccessMode: windows.SET_ACCESS, Inheritance: windows.NO_INHERITANCE, Trustee: windows.TRUSTEE{TrusteeForm: windows.TRUSTEE_IS_SID, TrusteeType: trusteeType, TrusteeValue: windows.TrusteeValueFromSID(sid)}})
	}
	acl, err := windows.ACLFromEntries(entries, nil)
	if err != nil {
		return err
	}
	securityInformation := windows.SECURITY_INFORMATION(windows.DACL_SECURITY_INFORMATION | windows.UNPROTECTED_DACL_SECURITY_INFORMATION)
	if protected {
		securityInformation = windows.DACL_SECURITY_INFORMATION | windows.PROTECTED_DACL_SECURITY_INFORMATION
	}
	return windows.SetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, securityInformation, nil, nil, acl, nil)
}
