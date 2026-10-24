//go:build !windows

package contextassembly

import (
	"errors"
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

// TestLinkNamedOpenFileAtAuthenticatesBothSides verifies the portable Unix fallback publishes only the inode held open by the store.
func TestLinkNamedOpenFileAtAuthenticatesBothSides(t *testing.T) {
	// directory is the pinned directory handle used for descriptor-relative publication.
	directory, err := openDirectoryNoFollow(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer directory.Close()

	// safeFile and safeName identify the private inode that the fallback must authenticate.
	safeFile, safeName, err := createTempAt(directory)
	if err != nil {
		t.Fatal(err)
	}
	defer safeFile.Close()
	if _, err := safeFile.WriteString("safe"); err != nil {
		t.Fatal(err)
	}
	if err := linkNamedOpenFileAt(directory, safeFile, safeName, "published"); err != nil {
		t.Fatal(err)
	}
	// published is the resulting object content used to prove the linked inode is the authenticated one.
	published, err := os.ReadFile(filepath.Join(directory.Name(), "published"))
	if err != nil || string(published) != "safe" {
		t.Fatalf("unexpected publication content %q: %v", published, err)
	}

	// swappedFile and swappedName model an attacker replacing the directory entry after the trusted inode is open.
	swappedFile, swappedName, err := createTempAt(directory)
	if err != nil {
		t.Fatal(err)
	}
	defer swappedFile.Close()
	if err := removeAt(directory, swappedName); err != nil {
		t.Fatal(err)
	}
	// attacker is a distinct inode installed under the original temporary name.
	attacker, err := os.OpenFile(filepath.Join(directory.Name(), swappedName), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if err := attacker.Close(); err != nil {
		t.Fatal(err)
	}
	if err := linkNamedOpenFileAt(directory, swappedFile, swappedName, "rejected"); !errors.Is(err, syscall.EPERM) {
		t.Fatalf("expected identity rejection, got %v", err)
	}
	if _, err := os.Stat(filepath.Join(directory.Name(), "rejected")); !os.IsNotExist(err) {
		t.Fatalf("rejected publication exists: %v", err)
	}
}
