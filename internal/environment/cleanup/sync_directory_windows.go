//go:build windows

package cleanup

// syncCleanupDirectory relies on synced files and atomic rename because Windows rejects directory FlushFileBuffers.
func syncCleanupDirectory(string) error { return nil }
