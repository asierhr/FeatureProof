package goinspector

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// SnapshotRequest is the complete bounded authority passed to a repository filesystem capability.
type SnapshotRequest struct {
	Root   string
	Roots  []string
	Limits Limits
}

// RepositoryFS is the sole passive capability allowed to materialize repository bytes.
type RepositoryFS interface {
	ReadSnapshot(context.Context, SnapshotRequest) (map[string][]byte, error)
}

// Clock supplies the single trusted instant used to seal an absolute inspection deadline.
type Clock interface{ Now() time.Time }

// systemClock supplies wall time only at the outer orchestration boundary.
type systemClock struct{}

// Now returns the current instant for production inspection composition.
func (systemClock) Now() time.Time { return time.Now() }

// LocalRepositoryFS reads one checkout through a retained descriptor chain; BeforeOpen is a test-only race hook.
type LocalRepositoryFS struct{ BeforeOpen func(stage, relative string) }

// freezeRepository creates an immutable authorized snapshot while rejecting links and every root escape.
func freezeRepository(ctx context.Context, root, repositoryID, revision string, roots []string, limits Limits) (Snapshot, error) {
	return freezeWithRepositoryFS(ctx, LocalRepositoryFS{}, root, repositoryID, revision, roots, limits)
}

// freezeWithRepositoryFS seals bytes returned by the only authorized passive capability.
func freezeWithRepositoryFS(ctx context.Context, repositoryFS RepositoryFS, root, repositoryID, revision string, roots []string, limits Limits) (Snapshot, error) {
	if repositoryFS == nil || root == "" || !safeToken(repositoryID, 128) || !validRevision(revision) || !validLimits(limits) || len(roots) == 0 {
		return Snapshot{}, typedError(CategoryInvalidInput, "filesystem", nil)
	}
	for _, requested := range roots {
		if clean, ok := safeRelativePath(requested); !ok || clean != requested {
			return Snapshot{}, typedError(CategoryInvalidInput, "filesystem.root", nil)
		}
	}
	bounded, cancel := inspectionContext(ctx, limits.MaxDuration)
	defer cancel()
	files, err := repositoryFS.ReadSnapshot(bounded, SnapshotRequest{Root: root, Roots: append([]string(nil), roots...), Limits: limits})
	if err != nil {
		return Snapshot{}, err
	}
	if err := bounded.Err(); err != nil {
		return Snapshot{}, cancelled("filesystem", err)
	}
	return NewSnapshot(repositoryID, revision, files)
}

// FreezeConfiguredRepository validates the complete authority before invoking the repository capability and returns its sealed input.
func FreezeConfiguredRepository(ctx context.Context, repositoryFS RepositoryFS, root string, config Configuration) (Input, error) {
	return FreezeConfiguredRepositoryWithClock(ctx, repositoryFS, root, config, systemClock{})
}

// FreezeConfiguredRepositoryWithClock seals one absolute budget shared by freeze and every later inspection phase.
func FreezeConfiguredRepositoryWithClock(ctx context.Context, repositoryFS RepositoryFS, root string, config Configuration, clock Clock) (Input, error) {
	if err := validateConfiguration(config); err != nil {
		return Input{}, err
	}
	if clock == nil {
		return Input{}, typedError(CategoryInvalidInput, "input.clock", nil)
	}
	now := clock.Now()
	bounded, cancel := inspectionContextAt(ctx, config.Limits.MaxDuration, now)
	defer cancel()
	deadlineAt, _ := bounded.Deadline()
	snapshot, err := freezeWithRepositoryFS(bounded, repositoryFS, root, config.RepositoryID, config.Revision, config.Roots, config.Limits)
	if err != nil {
		return Input{}, err
	}
	if err := bounded.Err(); err != nil {
		return Input{}, cancelled("input", err)
	}
	input, err := newInput(snapshot, config, deadlineAt)
	if err != nil {
		return Input{}, err
	}
	if err := bounded.Err(); err != nil {
		return Input{}, cancelled("input", err)
	}
	return input, nil
}

// inspectionContext installs the shorter of the caller deadline and the configured inspection duration.
func inspectionContext(ctx context.Context, maximum time.Duration) (context.Context, context.CancelFunc) {
	return inspectionContextAt(ctx, maximum, time.Now())
}

// inspectionContextAt computes the deadline from an explicit clock instant so boundary tests never depend on sleeps.
func inspectionContextAt(ctx context.Context, maximum time.Duration, now time.Time) (context.Context, context.CancelFunc) {
	candidate := now.Add(maximum)
	if parent, exists := ctx.Deadline(); exists && !candidate.Before(parent) {
		return context.WithCancel(ctx)
	}
	return context.WithDeadline(ctx, candidate)
}

// repositoryReadState owns retained directory handles and the all-or-nothing byte budget.
type repositoryReadState struct {
	ctx      context.Context
	fs       LocalRepositoryFS
	limits   Limits
	files    map[string][]byte
	held     []*os.File
	listings map[*os.File][]os.DirEntry
	total    int
}

// ReadSnapshot opens every path component relative to its validated parent handle and never returns partial bytes.
func (capability LocalRepositoryFS) ReadSnapshot(ctx context.Context, request SnapshotRequest) (map[string][]byte, error) {
	if !validLimits(request.Limits) || request.Root == "" || len(request.Roots) == 0 {
		return nil, typedError(CategoryInvalidInput, "filesystem", nil)
	}
	absRoot, err := filepath.Abs(request.Root)
	if err != nil {
		return nil, typedError(CategoryInvalidInput, "filesystem", nil)
	}
	state := &repositoryReadState{ctx: ctx, fs: capability, limits: request.Limits, files: map[string][]byte{}, listings: map[*os.File][]os.DirEntry{}}
	defer func() {
		for i := len(state.held) - 1; i >= 0; i-- {
			_ = state.held[i].Close()
		}
	}()
	rootHandle, err := state.openAbsoluteChain(absRoot)
	if err != nil {
		return nil, typedError(CategoryInvalidInput, "filesystem.root", nil)
	}
	cleanRoots := append([]string(nil), request.Roots...)
	sort.Strings(cleanRoots)
	for _, requested := range cleanRoots {
		clean, ok := safeRelativePath(requested)
		if !ok {
			return nil, typedError(CategoryInvalidInput, "filesystem.root", nil)
		}
		directory, openErr := state.openDirectoryChain(rootHandle, clean, "root")
		if openErr != nil {
			return nil, openErr
		}
		if err := state.walk(directory, clean, 0); err != nil {
			return nil, err
		}
	}
	if _, included := state.files["go.mod"]; !included {
		if err := state.readOptional(rootHandle, "go.mod", "go.mod"); err != nil {
			return nil, err
		}
	}
	return state.files, nil
}

// openAbsoluteChain anchors the repository root at the volume root and retains every component handle.
func (s *repositoryReadState) openAbsoluteChain(absolute string) (*os.File, error) {
	volume := filepath.VolumeName(absolute)
	anchor := string(filepath.Separator)
	remainder := strings.TrimPrefix(absolute, anchor)
	if volume != "" {
		anchor = volume + string(filepath.Separator)
		remainder = strings.TrimPrefix(strings.TrimPrefix(absolute, volume), string(filepath.Separator))
	}
	current, err := openNoFollow(anchor, true)
	if err != nil {
		return nil, err
	}
	s.held = append(s.held, current)
	for _, component := range strings.FieldsFunc(remainder, func(r rune) bool { return r == '/' || r == '\\' }) {
		current, err = s.openAnchoredDirectory(current, component, "ancestor", component)
		if err != nil {
			return nil, err
		}
	}
	return current, nil
}

// openDirectoryChain opens authorized relative root components beneath the anchored repository handle.
func (s *repositoryReadState) openDirectoryChain(parent *os.File, relative, stage string) (*os.File, error) {
	current := parent
	for _, component := range strings.Split(filepath.ToSlash(relative), "/") {
		if err := s.ctx.Err(); err != nil {
			return nil, cancelled("filesystem", err)
		}
		child, err := s.openAnchoredDirectory(current, component, stage, relative)
		if err != nil {
			return nil, typedError(CategoryInvalidInput, "filesystem.root", nil)
		}
		current = child
	}
	return current, nil
}

// openAnchoredDirectory captures child identity from the parent handle before a race hook and validates the opened handle.
func (s *repositoryReadState) openAnchoredDirectory(parent *os.File, name, stage, relative string) (*os.File, error) {
	entries, err := s.directoryEntries(parent)
	if err != nil {
		return nil, err
	}
	var expected os.FileInfo
	for _, entry := range entries {
		matches := entry.Name() == name
		if os.PathSeparator == '\\' {
			matches = strings.EqualFold(entry.Name(), name)
		}
		if matches {
			expected, err = entry.Info()
			break
		}
	}
	if err != nil || expected == nil || !expected.IsDir() {
		return nil, os.ErrNotExist
	}
	s.hook(stage, relative)
	child, err := openChildNoFollow(parent, name, true)
	if err != nil {
		return nil, err
	}
	actual, err := child.Stat()
	if err != nil || !os.SameFile(expected, actual) {
		_ = child.Close()
		return nil, errors.New("repository directory changed")
	}
	s.held = append(s.held, child)
	return child, nil
}

// walk enumerates a validated directory handle and recursively opens children relative to that handle.
func (s *repositoryReadState) walk(directory *os.File, relative string, depth int) error {
	if depth > s.limits.MaxDepth {
		return typedError(CategoryLimitExceeded, "filesystem.depth", nil)
	}
	if err := s.ctx.Err(); err != nil {
		return cancelled("filesystem", err)
	}
	entries, err := s.directoryEntries(directory)
	if err != nil {
		return typedError(CategoryInvalidInput, "filesystem.read", nil)
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })
	for _, entry := range entries {
		name := entry.Name()
		childRelative := filepath.ToSlash(filepath.Join(relative, name))
		expected, infoErr := entry.Info()
		if infoErr != nil {
			return typedError(CategoryInvalidInput, "filesystem.read", nil)
		}
		s.hook("entry", childRelative)
		if entry.IsDir() {
			child, openErr := openChildNoFollow(directory, name, true)
			if openErr != nil {
				return typedError(CategoryInvalidInput, "filesystem.changed", nil)
			}
			actual, statErr := child.Stat()
			if statErr != nil || !os.SameFile(expected, actual) {
				_ = child.Close()
				return typedError(CategoryInvalidInput, "filesystem.changed", nil)
			}
			s.held = append(s.held, child)
			if err := s.walk(child, childRelative, depth+1); err != nil {
				return err
			}
			continue
		}
		if err := s.readRequired(directory, name, childRelative, expected); err != nil {
			return err
		}
	}
	return nil
}

// directoryEntries caches deterministic enumeration from one retained directory handle for nested authorized roots.
func (s *repositoryReadState) directoryEntries(directory *os.File) ([]os.DirEntry, error) {
	if values, ok := s.listings[directory]; ok {
		return values, nil
	}
	values, err := directory.ReadDir(-1)
	if err != nil {
		return nil, err
	}
	s.listings[directory] = values
	return values, nil
}

// readRequired reads one regular child from the same validated handle and reserves budgets before retention.
func (s *repositoryReadState) readRequired(parent *os.File, name, relative string, expected os.FileInfo) error {
	if len(s.files) >= s.limits.MaxFiles {
		return typedError(CategoryLimitExceeded, "files", nil)
	}
	s.hook("file", relative)
	file, err := openChildNoFollow(parent, name, false)
	if err != nil {
		return typedError(CategoryInvalidInput, "filesystem.changed", nil)
	}
	defer file.Close()
	before, err := file.Stat()
	if err != nil || !before.Mode().IsRegular() || expected != nil && (!os.SameFile(expected, before) || expected.Size() != before.Size() || !expected.ModTime().Equal(before.ModTime())) {
		return typedError(CategoryInvalidInput, "filesystem.file", nil)
	}
	s.hook("opened", relative)
	content, err := io.ReadAll(io.LimitReader(file, int64(s.limits.MaxBytes-s.total+1)))
	if err != nil {
		return typedError(CategoryInvalidInput, "filesystem.read", nil)
	}
	if s.total+len(content) > s.limits.MaxBytes {
		return typedError(CategoryLimitExceeded, "bytes", nil)
	}
	after, err := file.Stat()
	if err != nil || !stableFileMetadata(before, after) || int64(len(content)) != after.Size() {
		return typedError(CategoryInvalidInput, "filesystem.changed", nil)
	}
	s.hook("read", relative)
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return typedError(CategoryInvalidInput, "filesystem.read", nil)
	}
	if err := s.ctx.Err(); err != nil {
		return cancelled("filesystem.read", err)
	}
	verification, err := io.ReadAll(io.LimitReader(file, int64(len(content)+1)))
	if err != nil || !bytes.Equal(content, verification) {
		return typedError(CategoryInvalidInput, "filesystem.changed", nil)
	}
	verified, err := file.Stat()
	if err != nil || !stableFileMetadata(after, verified) || !stableFileMetadata(before, verified) {
		return typedError(CategoryInvalidInput, "filesystem.changed", nil)
	}
	s.total += len(content)
	s.files[relative] = content
	return nil
}

// stableFileMetadata proves identity, kind, size and last-write metadata stayed unchanged on the retained handle.
func stableFileMetadata(before, after os.FileInfo) bool {
	return before != nil && after != nil && os.SameFile(before, after) && before.Mode() == after.Mode() && before.Size() == after.Size() && before.ModTime().Equal(after.ModTime())
}

// readOptional opens root go.mod relative to the anchored root and ignores only true absence.
func (s *repositoryReadState) readOptional(parent *os.File, name, relative string) error {
	entries, listingErr := s.directoryEntries(parent)
	if listingErr != nil {
		return typedError(CategoryInvalidInput, "filesystem.module", nil)
	}
	var expected os.FileInfo
	for _, entry := range entries {
		if entry.Name() == name {
			expected, listingErr = entry.Info()
			break
		}
	}
	if listingErr != nil {
		return typedError(CategoryInvalidInput, "filesystem.module", nil)
	}
	if expected == nil {
		return nil
	}
	s.hook("module", relative)
	return s.readRequired(parent, name, relative, expected)
}

// hook invokes a deterministic filesystem race boundary without granting source-controlled behavior.
func (s *repositoryReadState) hook(stage, relative string) {
	if s.fs.BeforeOpen != nil {
		s.fs.BeforeOpen(stage, relative)
	}
}

// cancelled maps context causes to a typed failure while retaining errors.Is behavior.
func cancelled(stage string, cause error) error {
	return &Error{Category: CategoryCancelled, Stage: stage, Cause: cause}
}
