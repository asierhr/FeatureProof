//go:build !windows

package contractreview

import "os"

// openRootDescriptor retains the directory inode used to reject root replacement during one runtime.
func openRootDescriptor(path string) (*os.File, error) {
	return os.Open(path)
}
