package localvalidation

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/asierhr/featureProof/internal/planmanifest"
)

// faultFile injects durable-file failures without changing the underlying temporary layout.
type faultFile struct {
	SyncFile
	failWrite bool
	failSync  bool
}

// Write fails at the byte-persistence boundary selected by a publication test.
func (f *faultFile) Write(value []byte) (int, error) {
	if f.failWrite {
		return 0, errors.New("injected write failure")
	}
	return f.SyncFile.Write(value)
}

// Sync fails at either the artifact or directory durability boundary selected by a test.
func (f *faultFile) Sync() error {
	if f.failSync {
		return errors.New("injected sync failure")
	}
	return f.SyncFile.Sync()
}

// faultFileSystem wraps the real isolated filesystem and fails one named publication phase.
type faultFileSystem struct {
	base FileSystem
	fail string
}

// MkdirAll injects failure while preparing the runs parent.
func (f *faultFileSystem) MkdirAll(path string, mode os.FileMode) error {
	if f.fail == "mkdir_all" {
		return errors.New("injected mkdir all failure")
	}
	return f.base.MkdirAll(path, mode)
}

// Mkdir injects failure while reserving staging.
func (f *faultFileSystem) Mkdir(path string, mode os.FileMode) error {
	if f.fail == "mkdir" {
		return errors.New("injected mkdir failure")
	}
	return f.base.Mkdir(path, mode)
}

// Lstat delegates collision observation to the isolated operating-system filesystem.
func (f *faultFileSystem) Lstat(path string) (os.FileInfo, error) { return f.base.Lstat(path) }

// CreateTemp injects creation, write, or file-sync failures for one artifact.
func (f *faultFileSystem) CreateTemp(dir, pattern string) (SyncFile, error) {
	if f.fail == "create_temp" {
		return nil, errors.New("injected create failure")
	}
	file, err := f.base.CreateTemp(dir, pattern)
	if err != nil {
		return nil, err
	}
	return &faultFile{SyncFile: file, failWrite: f.fail == "write", failSync: f.fail == "file_sync"}, nil
}

// Open injects directory-open or directory-sync failures before final commit.
func (f *faultFileSystem) Open(path string) (SyncFile, error) {
	if f.fail == "open_dir" {
		return nil, errors.New("injected open failure")
	}
	file, err := f.base.Open(path)
	if err != nil {
		return nil, err
	}
	return &faultFile{SyncFile: file, failSync: f.fail == "dir_sync"}, nil
}

// Remove delegates cleanup of writer-owned temporary files.
func (f *faultFileSystem) Remove(path string) error { return f.base.Remove(path) }

// CommitNoReplace injects artifact or final rename failures while preserving staging.
func (f *faultFileSystem) CommitNoReplace(source, target string) error {
	final := strings.HasSuffix(source, ".incomplete")
	if (f.fail == "artifact_rename" && !final) || (f.fail == "final_rename" && final) {
		return errors.New("injected rename failure")
	}
	return f.base.CommitNoReplace(source, target)
}

// TestPublisherFailureMatrixNeverExposesFinalBundle traces every probable R8 filesystem phase.
func TestPublisherFailureMatrixNeverExposesFinalBundle(t *testing.T) {
	for _, phase := range []string{"mkdir_all", "mkdir", "create_temp", "write", "file_sync", "artifact_rename", "open_dir", "dir_sync", "final_rename"} {
		t.Run(phase, func(t *testing.T) {
			root := t.TempDir()
			fs := &faultFileSystem{base: OSFileSystem{}, fail: phase}
			publisher, err := NewPublisherWithFilesystem(root, "run", fs)
			if phase == "mkdir_all" || phase == "mkdir" {
				if err == nil {
					t.Fatal("setup failure ignored")
				}
				for _, name := range []string{"run", "run.incomplete"} {
					if _, statErr := os.Stat(filepath.Join(root, ".featureproof", "runs", name)); !os.IsNotExist(statErr) {
						t.Fatalf("%s exposed after setup failure: %v", name, statErr)
					}
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			snapshot := RunSnapshot{ID: "run", Status: ResultPassed}
			if err = publisher.Publish(t.Context(), snapshot, nil, planmanifest.Manifest{}, nil); err == nil {
				t.Fatal("publication failure ignored")
			}
			if _, statErr := os.Stat(filepath.Join(root, ".featureproof", "runs", "run")); !os.IsNotExist(statErr) {
				t.Fatalf("final exposed: %v", statErr)
			}
			if _, statErr := os.Stat(filepath.Join(root, ".featureproof", "runs", "run.incomplete")); statErr != nil {
				t.Fatalf("staging missing: %v", statErr)
			}
		})
	}
}

// TestCommitNoReplaceAllowsOneConcurrentPublisher proves a final bundle cannot be overwritten by an in-process race.
func TestCommitNoReplaceAllowsOneConcurrentPublisher(t *testing.T) {
	root := t.TempDir()
	first, err := NewPublisher(root, "first")
	if err != nil {
		t.Fatal(err)
	}
	second, err := NewPublisher(root, "second")
	if err != nil {
		t.Fatal(err)
	}
	second.final = first.final
	start := make(chan struct{})
	results := make(chan error, 2)
	var group sync.WaitGroup
	group.Add(2)
	publish := func(p *Publisher, id string) {
		defer group.Done()
		<-start
		results <- p.Publish(t.Context(), RunSnapshot{ID: id, Status: ResultPassed}, nil, planmanifest.Manifest{}, nil)
	}
	go publish(first, "first")
	go publish(second, "second")
	close(start)
	group.Wait()
	close(results)
	success, failures := 0, 0
	for err := range results {
		if err == nil {
			success++
		} else {
			failures++
		}
	}
	if success != 1 || failures != 1 {
		t.Fatalf("success=%d failures=%d", success, failures)
	}
	raw, err := os.ReadFile(filepath.Join(root, ".featureproof", "runs", "first", "run.json"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), `"id": "first"`) && !strings.Contains(string(raw), `"id": "second"`) {
		t.Fatalf("invalid committed bundle %s", raw)
	}
}
