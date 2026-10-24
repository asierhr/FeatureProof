//go:build !windows

package contextassembly

import (
	"fmt"
	"os"
	"syscall"
)

// filesystemIdentity returns the device/inode identity without following a terminal link.
func filesystemIdentity(path string) (string, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return "", err
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return "", os.ErrInvalid
	}
	return fmt.Sprintf("%d:%d", stat.Dev, stat.Ino), nil
}
