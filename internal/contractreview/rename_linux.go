//go:build linux

package contractreview

import (
	"os"

	"golang.org/x/sys/unix"
)

// atomicCommitInDirectory keeps a no-follow directory descriptor through the descriptor-relative rename.
func atomicCommitInDirectory(parentHandle *os.File, _ string, source, target string, replacing bool) error {
	descriptor := int(parentHandle.Fd())
	flags := uint(0)
	if !replacing {
		flags = unix.RENAME_NOREPLACE
	}
	return unix.Renameat2(descriptor, source, descriptor, target, flags)
}
