//go:build windows

package localvalidation

import "os"

// commitNoReplace uses Windows rename semantics, which reject an existing destination atomically.
func commitNoReplace(staging, final string) error { return os.Rename(staging, final) }
