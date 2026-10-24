//go:build windows

package contextassembly

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/sys/windows"
)

// TestWindowsStoreRejectsBroadWritableDACL proves reopening fails before a hostile directory can be used.
func TestWindowsStoreRejectsBroadWritableDACL(t *testing.T) {
	root := filepath.Join(t.TempDir(), "protected-context")
	store, err := newWitnessedStore(root)
	if err != nil {
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
	if _, err = newWitnessedStore(root); err == nil {
		t.Fatal("broad writable Windows DACL accepted")
	}
	if _, err = store.LoadSnapshot(context.Background(), strings.Repeat("a", 64)); err == nil {
		t.Fatal("runtime DACL mutation accepted")
	}
}
