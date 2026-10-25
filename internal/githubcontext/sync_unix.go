//go:build !windows

package githubcontext

import "os"

// syncDirectory durably orders an immutable link before its receipt on Unix filesystems.
func syncDirectory(directory string) error {
	parent, err := os.Open(directory)
	if err != nil {
		return err
	}
	defer parent.Close()
	return parent.Sync()
}
