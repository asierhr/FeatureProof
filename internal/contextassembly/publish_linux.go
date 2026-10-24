//go:build linux

package contextassembly

import (
	"os"

	"golang.org/x/sys/unix"
)

// linkOpenFileAt publishes the authenticated open inode directly without consulting its temporary name.
func linkOpenFileAt(directory, temporaryFile *os.File, _, target string) error {
	return unix.Linkat(int(temporaryFile.Fd()), "", int(directory.Fd()), target, unix.AT_EMPTY_PATH)
}
