//go:build !windows

package localvalidation

import "os"

// syncFixtureAuthorityDirectory durably records authority directory entries on Unix.
func syncFixtureAuthorityDirectory(path string) error {
	directory, err := os.Open(path)
	if err != nil {
		return err
	}
	defer directory.Close()
	return directory.Sync()
}
