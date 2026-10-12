//go:build darwin

package localvalidation

import "golang.org/x/sys/unix"

// commitNoReplace uses renameatx_np so a concurrent destination can never be overwritten.
func commitNoReplace(staging, final string) error {
	return unix.RenameatxNp(unix.AT_FDCWD, staging, unix.AT_FDCWD, final, unix.RENAME_EXCL)
}
