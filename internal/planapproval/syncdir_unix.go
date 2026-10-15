//go:build !windows

package planapproval

import "os"

// SyncDir persists the directory entries used to publish, roll back or recover a confirmed prefix.
func (osJournalFS) SyncDir(path string) error {
	directory, err := os.Open(path)
	if err != nil {
		return err
	}
	if err = directory.Sync(); err != nil {
		_ = directory.Close()
		return err
	}
	return directory.Close()
}
