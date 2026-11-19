//go:build !windows

package local

import "os"

// syncDirectory durably orders snapshot rename metadata on Unix filesystems.
func syncDirectory(path string) error {
	directory, err := os.Open(path)
	if err != nil {
		return err
	}
	defer directory.Close()
	return directory.Sync()
}
