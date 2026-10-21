//go:build windows

package contractreview

import "os"

// atomicPublisherCommit delegates Windows' descriptor-relative commit and retains a uniform crash-checkpoint contract.
func atomicPublisherCommit(parentHandle *os.File, parent, source, target string, replacing bool, _ func(string)) error {
	return atomicCommitInDirectory(parentHandle, parent, source, target, replacing)
}
