//go:build linux

package localvalidation

import "golang.org/x/sys/unix"

// commitNoReplace uses renameat2 so a concurrent destination can never be overwritten.
func commitNoReplace(staging, final string) error {
	return unix.Renameat2(unix.AT_FDCWD, staging, unix.AT_FDCWD, final, unix.RENAME_NOREPLACE)
}
