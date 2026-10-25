//go:build windows

package environment

// syncEnvironmentDirectory relies on synced candidate files and write-through link/rename semantics on Windows.
func syncEnvironmentDirectory(string) error { return nil }
