//go:build !windows

package functionalflow

import "os"

// replaceFlowFile atomically replaces one durable state on Unix-compatible filesystems.
func replaceFlowFile(source, target string) error { return os.Rename(source, target) }
