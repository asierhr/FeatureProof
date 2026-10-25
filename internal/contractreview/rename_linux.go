//go:build linux

package contractreview

import (
	"errors"
	"os"

	"golang.org/x/sys/unix"
)

// atomicCommitInDirectory keeps a no-follow directory descriptor and preserves atomic replace/no-replace on older kernels.
func atomicCommitInDirectory(parentHandle *os.File, _ string, source, target string, replacing bool) error {
	descriptor := int(parentHandle.Fd())
	flags := uint(0)
	if !replacing {
		flags = unix.RENAME_NOREPLACE
	}
	err := unix.Renameat2(descriptor, source, descriptor, target, flags)
	if !errors.Is(err, unix.ENOSYS) {
		return err
	}
	return atomicCommitInDirectoryFallback(descriptor, source, target, replacing, nil)
}

// atomicCommitInDirectoryFallback uses linkat for no-clobber publication and renameat only for explicit replacement.
func atomicCommitInDirectoryFallback(descriptor int, source, target string, replacing bool, afterLink func() error) error {
	if replacing {
		return unix.Renameat(descriptor, source, descriptor, target)
	}
	err := unix.Linkat(descriptor, source, descriptor, target, 0)
	if errors.Is(err, unix.EEXIST) {
		var sourceStat, targetStat unix.Stat_t
		if unix.Fstatat(descriptor, source, &sourceStat, unix.AT_SYMLINK_NOFOLLOW) == nil && unix.Fstatat(descriptor, target, &targetStat, unix.AT_SYMLINK_NOFOLLOW) == nil && sourceStat.Dev == targetStat.Dev && sourceStat.Ino == targetStat.Ino {
			return unix.Unlinkat(descriptor, source, 0)
		}
	}
	if err != nil {
		return err
	}
	if afterLink != nil {
		if err = afterLink(); err != nil {
			return err
		}
	}
	return unix.Unlinkat(descriptor, source, 0)
}
