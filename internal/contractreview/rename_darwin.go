//go:build darwin

package contractreview

import (
	"os"

	"golang.org/x/sys/unix"
)

// atomicCommitInDirectory uses linkat as Darwin's atomic create-no-replace primitive and renameat for guarded replacement.
func atomicCommitInDirectory(parentHandle *os.File, _ string, source, target string, replacing bool) error {
	descriptor := int(parentHandle.Fd())
	var err error
	if replacing {
		return unix.Renameat(descriptor, source, descriptor, target)
	}
	if err = unix.Linkat(descriptor, source, descriptor, target, 0); err != nil {
		return err
	}
	return unix.Unlinkat(descriptor, source, 0)
}
