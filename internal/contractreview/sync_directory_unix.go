//go:build !windows

package contractreview

import "os"

// syncOpenDirectory durably flushes metadata through an already-open Unix directory descriptor.
func syncOpenDirectory(directory *os.File) error { return directory.Sync() }
