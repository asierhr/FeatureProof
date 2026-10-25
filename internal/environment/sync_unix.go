//go:build !windows

package environment

import "os"

// syncEnvironmentDirectory durably orders canonical links and head renames on Unix filesystems.
func syncEnvironmentDirectory(path string) error {
	directory, err := os.Open(path)
	if err != nil {
		return err
	}
	defer func() { _ = directory.Close() }()
	return directory.Sync()
}
