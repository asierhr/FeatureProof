//go:build !windows && !linux

package contextassembly

import (
	"os"
)

// linkOpenFileAt publishes by name on Unix variants lacking AT_EMPTY_PATH and authenticates both sides against the open inode.
func linkOpenFileAt(directory, temporaryFile *os.File, temporary, target string) error {
	return linkNamedOpenFileAt(directory, temporaryFile, temporary, target)
}
