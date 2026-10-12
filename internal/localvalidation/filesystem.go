package localvalidation

import (
	"io"
	"os"
)

// SyncFile is the smallest durable-file boundary required by bundle publication.
type SyncFile interface {
	io.Writer
	Name() string
	Chmod(os.FileMode) error
	Sync() error
	Close() error
}

// FileSystem owns every filesystem effect used before the final bundle becomes visible.
type FileSystem interface {
	MkdirAll(string, os.FileMode) error
	Mkdir(string, os.FileMode) error
	Lstat(string) (os.FileInfo, error)
	CreateTemp(string, string) (SyncFile, error)
	Open(string) (SyncFile, error)
	Remove(string) error
	CommitNoReplace(string, string) error
}

// OSFileSystem maps the publication boundary to local operating-system primitives.
type OSFileSystem struct{}

// MkdirAll creates the run parent without broadening its requested permissions.
func (OSFileSystem) MkdirAll(path string, mode os.FileMode) error { return os.MkdirAll(path, mode) }

// Mkdir reserves one staging path and fails when it already exists.
func (OSFileSystem) Mkdir(path string, mode os.FileMode) error { return os.Mkdir(path, mode) }

// Lstat observes bundle names without following a replacement symlink.
func (OSFileSystem) Lstat(path string) (os.FileInfo, error) { return os.Lstat(path) }

// CreateTemp creates an artifact temporary file inside the staging filesystem.
func (OSFileSystem) CreateTemp(dir, pattern string) (SyncFile, error) {
	return os.CreateTemp(dir, pattern)
}

// Open obtains a directory handle used to sync staging metadata where supported.
func (OSFileSystem) Open(path string) (SyncFile, error) { return os.Open(path) }

// Remove deletes only temporary files owned by an unfinished artifact write.
func (OSFileSystem) Remove(path string) error { return os.Remove(path) }

// CommitNoReplace delegates visibility to an operating-system primitive that atomically rejects an existing destination.
func (OSFileSystem) CommitNoReplace(staging, final string) error {
	return commitNoReplace(staging, final)
}
