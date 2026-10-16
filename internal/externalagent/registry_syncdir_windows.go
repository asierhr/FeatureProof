//go:build windows

package externalagent

// syncRegistryDir is a no-op because Windows does not expose portable directory fsync.
func syncRegistryDir(string) error { return nil }
