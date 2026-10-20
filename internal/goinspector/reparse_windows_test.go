//go:build windows

package goinspector

import (
	"context"
	"encoding/binary"
	"os"
	"path/filepath"
	"testing"
	"unicode/utf16"

	"golang.org/x/sys/windows"
)

const (
	// testMountPointTag identifies a Windows directory junction without requiring symbolic-link privilege.
	testMountPointTag = uint32(0xA0000003)
	// testSetReparsePoint applies the prepared junction payload to the already opened directory handle.
	testSetReparsePoint = uint32(0x000900A4)
)

// TestRepositoryDescriptorChainRejectsWindowsJunction verifies a non-privileged reparse root cannot escape the authorized repository.
func TestRepositoryDescriptorChainRejectsWindowsJunction(t *testing.T) {
	repository := t.TempDir()
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(repository, "go.mod"), []byte("module example.test/repository\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(outside, "main.go"), []byte("package outside\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	junction := filepath.Join(repository, "app")
	if err := createTestJunction(junction, outside); err != nil {
		t.Fatalf("create non-privileged junction: %v", err)
	}
	if snapshot, err := freezeWithRepositoryFS(context.Background(), LocalRepositoryFS{}, repository, "repository", testRevision, []string{"app"}, testLimits()); err == nil || snapshot.digest != "" {
		t.Fatalf("junction escaped descriptor chain: %#v %v", snapshot, err)
	}
}

// createTestJunction builds a mount-point reparse payload directly so the regression never depends on symlink privilege or a command process.
func createTestJunction(link, target string) error {
	if err := os.Mkdir(link, 0o700); err != nil {
		return err
	}
	pointer, err := windows.UTF16PtrFromString(link)
	if err != nil {
		return err
	}
	handle, err := windows.CreateFile(pointer, windows.GENERIC_WRITE, windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE, nil, windows.OPEN_EXISTING, windows.FILE_FLAG_OPEN_REPARSE_POINT|windows.FILE_FLAG_BACKUP_SEMANTICS, 0)
	if err != nil {
		return err
	}
	defer windows.CloseHandle(handle)
	payload := testJunctionPayload(target)
	var returned uint32
	return windows.DeviceIoControl(handle, testSetReparsePoint, &payload[0], uint32(len(payload)), nil, 0, &returned, nil)
}

// testJunctionPayload encodes the documented mount-point buffer with an NT substitute name and display name.
func testJunctionPayload(target string) []byte {
	substitute := utf16.Encode([]rune(`\??\` + filepath.Clean(target)))
	display := utf16.Encode([]rune(filepath.Clean(target)))
	pathUnits := append(append(append(append([]uint16{}, substitute...), 0), display...), 0)
	pathBytes := make([]byte, len(pathUnits)*2)
	for index, unit := range pathUnits {
		binary.LittleEndian.PutUint16(pathBytes[index*2:], unit)
	}
	payload := make([]byte, 16+len(pathBytes))
	binary.LittleEndian.PutUint32(payload[0:], testMountPointTag)
	binary.LittleEndian.PutUint16(payload[4:], uint16(8+len(pathBytes)))
	binary.LittleEndian.PutUint16(payload[8:], 0)
	binary.LittleEndian.PutUint16(payload[10:], uint16(len(substitute)*2))
	binary.LittleEndian.PutUint16(payload[12:], uint16((len(substitute)+1)*2))
	binary.LittleEndian.PutUint16(payload[14:], uint16(len(display)*2))
	copy(payload[16:], pathBytes)
	return payload
}
