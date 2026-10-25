//go:build linux

package contractreview

import (
	"errors"
	"os"
	"testing"

	"golang.org/x/sys/unix"
)

// TestAtomicCommitInDirectoryLinuxFallbackPreservesModes proves the fallback distinguishes no-clobber from replacement.
func TestAtomicCommitInDirectoryLinuxFallbackPreservesModes(t *testing.T) {
	root := t.TempDir()
	directory, err := os.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	defer directory.Close()
	if err = os.WriteFile(root+"/source", []byte("candidate"), 0o600); err != nil {
		t.Fatal(err)
	}
	descriptor := int(directory.Fd())
	if err = atomicCommitInDirectoryFallback(descriptor, "source", "target", false, nil); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(root+"/contender", []byte("contender"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err = atomicCommitInDirectoryFallback(descriptor, "contender", "target", false, nil); !errors.Is(err, os.ErrExist) {
		t.Fatalf("no-clobber fallback must reject target: %v", err)
	}
	if err = atomicCommitInDirectoryFallback(descriptor, "contender", "target", true, nil); err != nil {
		t.Fatal(err)
	}
	if raw, readErr := os.ReadFile(root + "/target"); readErr != nil || string(raw) != "contender" {
		t.Fatalf("explicit replacement mismatch: %q %v", raw, readErr)
	}
	if _, statErr := os.Stat(root + "/contender"); !errors.Is(statErr, unix.ENOENT) {
		t.Fatalf("replacement source must be consumed: %v", statErr)
	}
}

// TestAtomicCommitInDirectoryLinuxFallbackConvergesLinkCrash proves response loss and contenders retain one inode winner.
func TestAtomicCommitInDirectoryLinuxFallbackConvergesLinkCrash(t *testing.T) {
	root := t.TempDir()
	directory, err := os.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	defer directory.Close()
	descriptor := int(directory.Fd())
	if err = os.WriteFile(root+"/source", []byte("candidate"), 0o600); err != nil {
		t.Fatal(err)
	}
	crash := errors.New("crash after link")
	if err = atomicCommitInDirectoryFallback(descriptor, "source", "target", false, func() error { return crash }); !errors.Is(err, crash) {
		t.Fatalf("crash boundary not surfaced: %v", err)
	}
	if err = atomicCommitInDirectoryFallback(descriptor, "source", "target", false, nil); err != nil {
		t.Fatalf("redelivery did not converge: %v", err)
	}
	for _, name := range []string{"left", "right"} {
		if err = os.WriteFile(root+"/"+name, []byte(name), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	start := make(chan struct{})
	results := make(chan error, 2)
	for _, name := range []string{"left", "right"} {
		go func(source string) {
			<-start
			results <- atomicCommitInDirectoryFallback(descriptor, source, "winner", false, nil)
		}(name)
	}
	close(start)
	first, second := <-results, <-results
	if (first == nil) == (second == nil) {
		t.Fatalf("expected exactly one winner: %v / %v", first, second)
	}
}
