//go:build !windows

package projectmemory

import (
	"os"
	"syscall"
)

// verifyFileIdentity rejects mutable files with additional hardlink aliases on Unix hosts.
func verifyFileIdentity(path string) error {
	info, err := os.Lstat(path)
	if err != nil {
		return fieldError("path", "store.identity", "file", err)
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || stat.Nlink != 1 {
		return fieldError("path", "store.identity", "hardlink", nil)
	}
	return nil
}
