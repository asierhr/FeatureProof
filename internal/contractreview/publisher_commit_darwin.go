//go:build darwin

package contractreview

import (
	"os"

	"golang.org/x/sys/unix"
)

// atomicPublisherCommit exposes Darwin's link/unlink durability boundary to crash recovery tests without weakening descriptor-relative operations.
func atomicPublisherCommit(parentHandle *os.File, _ string, source, target string, replacing bool, checkpoint func(string)) error {
	descriptor := int(parentHandle.Fd())
	if replacing {
		return unix.Renameat(descriptor, source, descriptor, target)
	}
	if err := unix.Linkat(descriptor, source, descriptor, target, 0); err != nil {
		return err
	}
	if checkpoint != nil {
		checkpoint("target_linked")
	}
	return unix.Unlinkat(descriptor, source, 0)
}
