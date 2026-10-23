//go:build !windows

package localvalidation

import (
	"os"
	"syscall"
)

// protectFixtureAuthorityFile restricts a newly created Unix authority file to its owner before content is written.
func protectFixtureAuthorityFile(path string) error { return os.Chmod(path, 0o600) }

// fixtureAuthorityOwnerOK requires the private authority and pin to belong to the current Unix uid.
func fixtureAuthorityOwnerOK(_ string, info os.FileInfo) bool {
	stat, ok := info.Sys().(*syscall.Stat_t)
	return ok && stat.Uid == uint32(os.Getuid()) && info.Mode().Perm()&0o077 == 0
}
