//go:build !windows

package fixture

import "os"

// replaceFile atomically replaces one durable state on Unix-compatible filesystems.
func replaceFile(source, target string) error { return os.Rename(source, target) }
