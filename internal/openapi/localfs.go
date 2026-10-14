package openapi

import (
	"context"
	"io"
	"os"
	"path/filepath"
)

// SourceFS is the replaceable local-file boundary consumed by the inspector.
type SourceFS interface {
	Canonicalize(context.Context, string) (string, error)
	ReadFile(context.Context, string, int64) ([]byte, error)
}

// LocalFS implements SourceFS without network access or ambient configuration.
type LocalFS struct{}

// Canonicalize resolves absolute paths and symlinks before scope checks.
func (LocalFS) Canonicalize(ctx context.Context, path string) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	return filepath.EvalSymlinks(abs)
}

// ReadFile reads at most max bytes and observes cancellation before and after I/O.
func (LocalFS) ReadFile(ctx context.Context, path string, max int64) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if info.Size() > max {
		return nil, &Error{Category: LimitExceeded, Location: "document", Cause: os.ErrInvalid}
	}
	b := make([]byte, info.Size())
	n, err := io.ReadFull(f, b)
	if err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return b[:n], nil
}
