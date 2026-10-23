//go:build windows

package localvalidation

// syncFixtureAuthorityDirectory relies on write-through file Sync because Go cannot fsync directories on Windows.
func syncFixtureAuthorityDirectory(string) error { return nil }
