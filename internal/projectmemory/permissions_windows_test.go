//go:build windows

package projectmemory

import (
	"path/filepath"
	"testing"

	"golang.org/x/sys/windows"
)

// TestWindowsDACLRejectsBroadWriteAccess proves writable Everyone entries fail before storage use.
func TestWindowsDACLRejectsBroadWriteAccess(t *testing.T) {
	root := filepath.Join(t.TempDir(), "protected-memory")
	if err := secureMkdirAll(root); err != nil {
		t.Fatal(err)
	}
	descriptor, err := windows.SecurityDescriptorFromString("D:P(A;;FA;;;WD)")
	if err != nil {
		t.Fatal(err)
	}
	dacl, _, err := descriptor.DACL()
	if err != nil {
		t.Fatal(err)
	}
	if err = windows.SetNamedSecurityInfo(root, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION, nil, nil, dacl, nil); err != nil {
		t.Fatal(err)
	}
	if _, err = memoryDirectoryIdentity(root); err == nil {
		t.Fatal("broad writable Windows DACL accepted")
	}
}
