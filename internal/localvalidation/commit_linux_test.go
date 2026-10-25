//go:build linux

package localvalidation

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/sys/unix"
)

// TestCommitNoReplaceLinuxFallbackPreservesNoClobber proves ENOSYS never becomes an overwrite-capable rename.
func TestCommitNoReplaceLinuxFallbackPreservesNoClobber(t *testing.T) {
	root := t.TempDir()
	staging := filepath.Join(root, "staging")
	final := filepath.Join(root, "final")
	if err := os.Mkdir(staging, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(staging, "run.json"), []byte("candidate"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := commitNoReplaceWith(staging, final, func() error { return unix.ENOSYS }); err != nil {
		t.Fatal(err)
	}
	if raw, err := os.ReadFile(filepath.Join(final, "run.json")); err != nil || string(raw) != "candidate" {
		t.Fatalf("fallback publication mismatch: %q %v", raw, err)
	}
	if _, err := os.Stat(staging); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("staging must be consumed: %v", err)
	}
	contender := filepath.Join(root, "contender")
	if err := os.Mkdir(contender, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(contender, "run.json"), []byte("replacement"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := commitNoReplaceWith(contender, final, func() error { return unix.ENOSYS }); !errors.Is(err, os.ErrExist) {
		t.Fatalf("existing destination must fail closed: %v", err)
	}
	if raw, err := os.ReadFile(filepath.Join(final, "run.json")); err != nil || string(raw) != "candidate" {
		t.Fatalf("existing destination was overwritten: %q %v", raw, err)
	}
	fileStaging := filepath.Join(root, "artifact.incomplete")
	fileFinal := filepath.Join(root, "artifact.json")
	if err := os.WriteFile(fileStaging, []byte("artifact"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := commitNoReplaceWith(fileStaging, fileFinal, func() error { return unix.ENOSYS }); err != nil {
		t.Fatal(err)
	}
	if raw, err := os.ReadFile(fileFinal); err != nil || string(raw) != "artifact" {
		t.Fatalf("file fallback publication mismatch: %q %v", raw, err)
	}
}

// TestCommitDirectoryNoReplaceCoordinatesRaceAndCrash proves the persistent claim serializes contenders and restart.
func TestCommitDirectoryNoReplaceCoordinatesRaceAndCrash(t *testing.T) {
	root := t.TempDir()
	makeBundle := func(name string) string {
		path := filepath.Join(root, name)
		if err := os.Mkdir(path, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(path, "run.json"), []byte(name), 0o600); err != nil {
			t.Fatal(err)
		}
		return path
	}
	crashBefore := makeBundle("crash-before")
	finalBefore := filepath.Join(root, "final-before")
	crash := errors.New("crash boundary")
	if err := commitDirectoryNoReplace(crashBefore, finalBefore, func() error { return crash }, nil); !errors.Is(err, crash) {
		t.Fatalf("before-rename crash missing: %v", err)
	}
	if err := commitDirectoryNoReplace(crashBefore, finalBefore, nil, nil); err != nil {
		t.Fatalf("before-rename recovery failed: %v", err)
	}
	crashAfter := makeBundle("crash-after")
	finalAfter := filepath.Join(root, "final-after")
	if err := commitDirectoryNoReplace(crashAfter, finalAfter, nil, func() error { return crash }); !errors.Is(err, crash) {
		t.Fatalf("after-rename crash missing: %v", err)
	}
	if err := commitDirectoryNoReplace(crashAfter, finalAfter, nil, nil); err != nil {
		t.Fatalf("after-rename recovery failed: %v", err)
	}
	left, right, winner := makeBundle("left"), makeBundle("right"), filepath.Join(root, "winner")
	start := make(chan struct{})
	results := make(chan error, 2)
	for _, source := range []string{left, right} {
		go func(candidate string) {
			<-start
			results <- commitDirectoryNoReplace(candidate, winner, nil, nil)
		}(source)
	}
	close(start)
	first, second := <-results, <-results
	if (first == nil) == (second == nil) {
		t.Fatalf("expected exactly one bundle winner: %v / %v", first, second)
	}
}
