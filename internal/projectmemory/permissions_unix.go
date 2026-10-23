//go:build !windows

package projectmemory

// secureMemoryPermissions is unnecessary on Unix because mode validation is applied to created directories.
func secureMemoryPermissions(string) error { return nil }
