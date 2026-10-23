//go:build !windows

package projectmemory

import "os"

// atomicReplace publishes one same-filesystem temporary over its target atomically on Unix.
func atomicReplace(source, target string) error { return os.Rename(source, target) }
