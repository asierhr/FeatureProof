package revisionbinding

import (
	"context"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
)

// GitResolver resolves one configured local repository without fetching or checking out.
type GitResolver struct{ Root, RepositoryID string }

var gitRef = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._/-]{0,255}$`)

// Resolve fixes a branch or complete OID to a commit and rejects relevant dirty state.
func (g GitResolver) Resolve(ctx context.Context, repositoryID, ref string) (Revision, error) {
	if err := ctx.Err(); err != nil {
		return Revision{}, &Error{Cancelled, "git"}
	}
	if repositoryID != g.RepositoryID || !ValidID(repositoryID) || g.Root == "" || !gitRef.MatchString(ref) || strings.Contains(ref, "..") || strings.Contains(ref, "//") || strings.HasSuffix(ref, ".lock") {
		return Revision{}, &Error{InvalidRevision, "git"}
	}
	root, err := filepath.Abs(g.Root)
	if err != nil {
		return Revision{}, &Error{InvalidRevision, "git"}
	}
	root, err = filepath.EvalSymlinks(root)
	if err != nil {
		return Revision{}, &Error{InvalidRevision, "git"}
	}
	output, err := g.command(ctx, root, "rev-parse", "--show-toplevel")
	if err != nil {
		return Revision{}, err
	}
	top, err := filepath.EvalSymlinks(strings.TrimSpace(output))
	if err != nil || !samePath(root, top) {
		return Revision{}, &Error{InvalidRevision, "git"}
	}
	format, err := g.command(ctx, root, "rev-parse", "--show-object-format")
	if err != nil {
		return Revision{}, err
	}
	wantLength := 40
	if strings.TrimSpace(format) == "sha256" {
		wantLength = 64
	} else if strings.TrimSpace(format) != "sha1" {
		return Revision{}, &Error{InvalidRevision, "git"}
	}
	if allHex(ref) && len(ref) != wantLength {
		return Revision{}, &Error{InvalidRevision, "git"}
	}
	if !allHex(ref) && ref != "HEAD" && !strings.HasPrefix(ref, "refs/") {
		matches := 0
		for _, prefix := range []string{"refs/heads/", "refs/tags/", "refs/remotes/"} {
			_, err := g.command(ctx, root, "show-ref", "--verify", "--quiet", prefix+ref)
			if IsCategory(err, Cancelled) {
				return Revision{}, err
			}
			if err == nil {
				matches++
			}
		}
		if matches > 1 {
			return Revision{}, &Error{InvalidRevision, "git"}
		}
	}
	resolved, err := g.command(ctx, root, "rev-parse", "--verify", "--end-of-options", ref+"^{commit}")
	if err != nil {
		return Revision{}, err
	}
	commit := strings.TrimSpace(resolved)
	if len(commit) != wantLength || !ValidOID(commit) {
		return Revision{}, &Error{InvalidRevision, "git"}
	}
	// Porcelain includes tracked, staged and untracked changes and excludes ignored files.
	dirty, err := g.dirty(ctx, root)
	if err != nil {
		return Revision{}, err
	}
	return Revision{RepositoryID: repositoryID, CommitOID: commit, Dirty: dirty}, nil
}

// command runs a fixed read-only Git operation and discards all untrusted diagnostics.
func (g GitResolver) command(ctx context.Context, root string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", append([]string{"-c", "core.fsmonitor=false"}, args...)...)
	cmd.Dir = root
	cmd.Env = gitEnvironment()
	out, err := cmd.Output()
	if ctx.Err() != nil {
		return "", &Error{Cancelled, "git"}
	}
	if err != nil {
		return "", &Error{UnknownCommit, "git"}
	}
	if len(out) > 4096 {
		return "", &Error{InvalidRevision, "git"}
	}
	return string(out), nil
}

// dirty streams status output without persisting repository paths or unbounded output.
func (g GitResolver) dirty(ctx context.Context, root string) (bool, error) {
	cmd := exec.CommandContext(ctx, "git", "-c", "core.fsmonitor=false", "status", "--porcelain=v1", "--untracked-files=all", "--ignore-submodules=none")
	cmd.Dir = root
	cmd.Env = gitEnvironment()
	seen := &statusWriter{}
	cmd.Stdout = seen
	cmd.Stderr = io.Discard
	err := cmd.Run()
	if ctx.Err() != nil {
		return false, &Error{Cancelled, "git"}
	}
	if err != nil {
		return false, &Error{InvalidRevision, "git"}
	}
	return seen.dirty, nil
}

// gitEnvironment drops every inherited Git control variable before selecting
// the configured repository. In particular, callers cannot redirect object
// storage, the worktree, ref namespace, or injected config to another repo.
func gitEnvironment() []string {
	env := make([]string, 0, len(os.Environ())+4)
	for _, entry := range os.Environ() {
		name, _, _ := strings.Cut(entry, "=")
		if !strings.HasPrefix(strings.ToUpper(name), "GIT_") {
			env = append(env, entry)
		}
	}
	return append(env, "GIT_OPTIONAL_LOCKS=0", "GIT_NO_REPLACE_OBJECTS=1", "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL="+os.DevNull)
}

// statusWriter remembers only whether Git emitted a changed-path record.
type statusWriter struct{ dirty bool }

// Write discards potentially sensitive path bytes while retaining dirty state.
func (w *statusWriter) Write(p []byte) (int, error) {
	if len(p) > 0 {
		w.dirty = true
	}
	return len(p), nil
}

// samePath accounts for case-insensitive path identity on Windows.
func samePath(left, right string) bool {
	return strings.EqualFold(filepath.Clean(left), filepath.Clean(right))
}

// allHex distinguishes abbreviated OIDs from symbolic branch names.
func allHex(value string) bool {
	if value == "" {
		return false
	}
	for _, c := range value {
		if !strings.ContainsRune("0123456789abcdefABCDEF", c) {
			return false
		}
	}
	return true
}
