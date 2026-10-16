//go:build !windows

package externalagent

import "os"

// syncRegistryDir confirms the atomic directory entry on Unix filesystems.
func syncRegistryDir(path string) error {
	directory, err := os.Open(path)
	if err != nil {
		return err
	}
	defer directory.Close()
	return directory.Sync()
}
