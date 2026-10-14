//go:build !windows

package oauth

import "os"

// protectCredentialFile restricts a Unix credential transaction to its owner.
func protectCredentialFile(_ string, file *os.File) error { return file.Chmod(0o600) }

// replaceCredentialFile atomically renames a completed Unix credential transaction.
func replaceCredentialFile(from, to string) error { return os.Rename(from, to) }
