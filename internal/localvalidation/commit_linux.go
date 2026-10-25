//go:build linux

package localvalidation

import (
	"errors"
	"os"
	"path/filepath"

	"golang.org/x/sys/unix"
)

// commitNoReplace uses renameat2 or a guarded bundle-directory rename so a concurrent completed destination is never overwritten.
func commitNoReplace(staging, final string) error {
	return commitNoReplaceWith(staging, final, func() error {
		return unix.Renameat2(unix.AT_FDCWD, staging, unix.AT_FDCWD, final, unix.RENAME_NOREPLACE)
	})
}

// commitNoReplaceWith preserves no-clobber publication for files and non-empty bundle directories when an older Linux kernel lacks renameat2.
func commitNoReplaceWith(staging, final string, rename func() error) error {
	err := rename()
	if !errors.Is(err, unix.ENOSYS) {
		return err
	}
	info, err := os.Lstat(staging)
	if errors.Is(err, os.ErrNotExist) {
		if _, finalErr := os.Lstat(final); finalErr == nil {
			return nil
		}
	}
	if err != nil {
		return err
	}
	if info.Mode().IsRegular() {
		return linkFileNoReplace(staging, final, nil)
	}
	if !info.IsDir() {
		return errors.New("no-replace fallback rejects non-regular staging objects")
	}
	entries, err := os.ReadDir(staging)
	if err != nil || len(entries) == 0 {
		return errors.New("no-replace fallback requires a non-empty bundle directory")
	}
	return commitDirectoryNoReplace(staging, final, nil, nil)
}

// linkFileNoReplace converges response loss by recognizing two names for the same immutable inode.
func linkFileNoReplace(staging, final string, afterLink func() error) error {
	err := os.Link(staging, final)
	if errors.Is(err, os.ErrExist) {
		stagingInfo, stagingErr := os.Stat(staging)
		finalInfo, finalErr := os.Stat(final)
		if stagingErr == nil && finalErr == nil && os.SameFile(stagingInfo, finalInfo) {
			return os.Remove(staging)
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
	return os.Remove(staging)
}

// commitDirectoryNoReplace serializes descriptor-relative no-replace decisions and survives process-loss boundaries.
func commitDirectoryNoReplace(staging, final string, beforeRename, afterRename func() error) error {
	parent := filepath.Dir(final)
	if filepath.Dir(staging) != parent {
		return errors.New("no-replace fallback requires one parent directory")
	}
	directory, err := unix.Open(parent, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return err
	}
	defer unix.Close(directory)
	claim, err := unix.Openat(directory, filepath.Base(final)+".claim", unix.O_CREAT|unix.O_RDWR|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0o600)
	if err != nil {
		return err
	}
	defer unix.Close(claim)
	if err = unix.Flock(claim, unix.LOCK_EX); err != nil {
		return err
	}
	defer unix.Flock(claim, unix.LOCK_UN)
	var stagingStat, finalStat unix.Stat_t
	stagingErr := unix.Fstatat(directory, filepath.Base(staging), &stagingStat, unix.AT_SYMLINK_NOFOLLOW)
	finalErr := unix.Fstatat(directory, filepath.Base(final), &finalStat, unix.AT_SYMLINK_NOFOLLOW)
	if errors.Is(stagingErr, unix.ENOENT) && finalErr == nil {
		return nil
	}
	if stagingErr != nil {
		return stagingErr
	}
	if finalErr == nil {
		return os.ErrExist
	}
	if !errors.Is(finalErr, unix.ENOENT) {
		return finalErr
	}
	if beforeRename != nil {
		if err = beforeRename(); err != nil {
			return err
		}
	}
	if err = unix.Renameat(directory, filepath.Base(staging), directory, filepath.Base(final)); err != nil {
		return err
	}
	if afterRename != nil {
		return afterRename()
	}
	return nil
}
