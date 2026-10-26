//go:build !windows

package cleanup

import "os"

// syncCleanupDirectory persists receipt and head directory entries on Unix filesystems.
func syncCleanupDirectory(path string) error {
	directory, err := os.Open(path)
	if err != nil {
		return err
	}
	err = directory.Sync()
	closeErr := directory.Close()
	if err == nil {
		err = closeErr
	}
	return err
}
