//go:build windows

package githubcontext

// syncDirectory relies on the flushed candidate file and atomic hard-link publication on Windows.
func syncDirectory(string) error { return nil }
