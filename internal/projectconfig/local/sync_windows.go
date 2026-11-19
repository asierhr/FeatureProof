//go:build windows

package local

// syncDirectory relies on the synced candidate and atomic Windows rename boundary.
func syncDirectory(string) error { return nil }
