//go:build windows

package contractreview

import "os"

// syncOpenDirectory documents Windows durability, whose descriptor-relative rename is the commit boundary.
func syncOpenDirectory(*os.File) error { return nil }
