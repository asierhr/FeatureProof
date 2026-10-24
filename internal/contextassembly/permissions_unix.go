//go:build !windows

package contextassembly

import "os"

// secureStorePermissions restricts a newly created durable directory to its owner on Unix.
func secureStorePermissions(path string) error { return os.Chmod(path, 0o700) }

// verifyStorePathPermissions rejects durable directories writable or readable by another Unix principal.
func verifyStorePathPermissions(path string) error {
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm()&0o077 != 0 {
		return os.ErrPermission
	}
	return nil
}
