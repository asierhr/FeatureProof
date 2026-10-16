package revisionbinding

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// gitRun executes a local-only fixture command and reports setup failures directly.
func gitRun(t *testing.T, root string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = root
	cmd.Env = append(cmd.Environ(), "GIT_CONFIG_GLOBAL=NUL", "GIT_CONFIG_NOSYSTEM=1", "GIT_OPTIONAL_LOCKS=0")
	raw, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v: %s", args, err, raw)
	}
	return strings.TrimSpace(string(raw))
}

// gitFixture creates a local commit with no remote or credentials.
func gitFixture(t *testing.T) (string, string, GitResolver) {
	t.Helper()
	root := t.TempDir()
	gitRun(t, root, "init", "-q")
	gitRun(t, root, "config", "user.email", "fixture@example.invalid")
	gitRun(t, root, "config", "user.name", "Fixture")
	if err := os.WriteFile(filepath.Join(root, "tracked.txt"), []byte("first"), 0600); err != nil {
		t.Fatal(err)
	}
	gitRun(t, root, "add", "tracked.txt")
	gitRun(t, root, "commit", "-qm", "first")
	oid := gitRun(t, root, "rev-parse", "HEAD")
	return root, oid, GitResolver{Root: root, RepositoryID: "repo-main"}
}

// TestResolveFullCommitAndFreezeMovedRef covers R1's immutable OID snapshot.
func TestResolveFullCommitAndFreezeMovedRef(t *testing.T) {
	root, first, resolver := gitFixture(t)
	ctx := context.Background()
	got, err := resolver.Resolve(ctx, "repo-main", "HEAD")
	if err != nil || got.CommitOID != first || got.Dirty {
		t.Fatalf("initial %+v %v", got, err)
	}
	if err := os.WriteFile(filepath.Join(root, "tracked.txt"), []byte("second"), 0600); err != nil {
		t.Fatal(err)
	}
	gitRun(t, root, "add", "tracked.txt")
	gitRun(t, root, "commit", "-qm", "second")
	newer, err := resolver.Resolve(ctx, "repo-main", "HEAD")
	if err != nil || newer.CommitOID == first || got.CommitOID != first {
		t.Fatalf("mutable ref altered snapshot %+v %+v %v", got, newer, err)
	}
}

// TestRejectMissingAmbiguousOrForeignCommit covers R1 and R8 rejected source objects.
func TestRejectMissingAmbiguousOrForeignCommit(t *testing.T) {
	root, commit, resolver := gitFixture(t)
	_ = root
	for _, ref := range []string{"missing", commit[:8], "HEAD~1", "--help", strings.Repeat("f", 40)} {
		if _, err := resolver.Resolve(context.Background(), "repo-main", ref); err == nil {
			t.Fatalf("accepted %q", ref)
		}
	}
	if _, err := resolver.Resolve(context.Background(), "other-repo", "HEAD"); err == nil {
		t.Fatal("foreign repository accepted")
	}
	if _, err := resolver.Resolve(context.Background(), "repo-main", commit); err != nil {
		t.Fatalf("full oid rejected: %v", err)
	}
	blob := gitRun(t, root, "hash-object", "tracked.txt")
	if _, err := resolver.Resolve(context.Background(), "repo-main", blob); err == nil {
		t.Fatal("blob accepted as commit")
	}
	gitRun(t, root, "branch", "collision")
	gitRun(t, root, "tag", "collision")
	if _, err := resolver.Resolve(context.Background(), "repo-main", "collision"); !IsCategory(err, InvalidRevision) {
		t.Fatalf("ambiguous ref accepted: %v", err)
	}
}

// TestDirtyWorktreeNeverAttributedToHEAD covers R2's tracked, staged and untracked states.
func TestDirtyWorktreeNeverAttributedToHEAD(t *testing.T) {
	for _, variant := range []string{"tracked", "staged", "untracked", "ignored"} {
		t.Run(variant, func(t *testing.T) {
			root, _, resolver := gitFixture(t)
			switch variant {
			case "tracked":
				_ = os.WriteFile(filepath.Join(root, "tracked.txt"), []byte("changed"), 0600)
			case "staged":
				_ = os.WriteFile(filepath.Join(root, "tracked.txt"), []byte("changed"), 0600)
				gitRun(t, root, "add", "tracked.txt")
			case "untracked":
				_ = os.WriteFile(filepath.Join(root, "new.txt"), []byte("new"), 0600)
			case "ignored":
				_ = os.WriteFile(filepath.Join(root, ".gitignore"), []byte("ignored.txt\n"), 0600)
				gitRun(t, root, "add", ".gitignore")
				gitRun(t, root, "commit", "-qm", "ignore")
				_ = os.WriteFile(filepath.Join(root, "ignored.txt"), []byte("ignored"), 0600)
			}
			got, err := resolver.Resolve(context.Background(), "repo-main", "HEAD")
			if err != nil {
				t.Fatal(err)
			}
			if got.Dirty != (variant != "ignored") {
				t.Fatalf("dirty=%v for %s", got.Dirty, variant)
			}
		})
	}
}

// TestGitResolverRejectsSubdirectoryAndCancellation covers R1/R8 source boundaries.
func TestGitResolverRejectsSubdirectoryAndCancellation(t *testing.T) {
	root, _, resolver := gitFixture(t)
	child := filepath.Join(root, "child")
	if err := os.Mkdir(child, 0700); err != nil {
		t.Fatal(err)
	}
	resolver.Root = child
	if _, err := resolver.Resolve(context.Background(), "repo-main", "HEAD"); !IsCategory(err, InvalidRevision) {
		t.Fatalf("subdirectory accepted: %v", err)
	}
	resolver.Root = root
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := resolver.Resolve(ctx, "repo-main", "HEAD"); !IsCategory(err, Cancelled) {
		t.Fatalf("cancelled Git read continued: %v", err)
	}
}

// TestResolveSHA256CommitPreservesFullObjectFormat covers R1's algorithm-specific OID length.
func TestResolveSHA256CommitPreservesFullObjectFormat(t *testing.T) {
	root := t.TempDir()
	cmd := exec.Command("git", "init", "-q", "--object-format=sha256")
	cmd.Dir = root
	cmd.Env = append(cmd.Environ(), "GIT_CONFIG_GLOBAL="+os.DevNull, "GIT_CONFIG_NOSYSTEM=1")
	if raw, err := cmd.CombinedOutput(); err != nil {
		t.Skipf("local Git lacks SHA-256 object format: %s", raw)
	}
	gitRun(t, root, "config", "user.email", "fixture@example.invalid")
	gitRun(t, root, "config", "user.name", "Fixture")
	if err := os.WriteFile(filepath.Join(root, "tracked.txt"), []byte("sha256"), 0600); err != nil {
		t.Fatal(err)
	}
	gitRun(t, root, "add", "tracked.txt")
	gitRun(t, root, "commit", "-qm", "sha256")
	resolved, err := (GitResolver{Root: root, RepositoryID: "repo-sha256"}).Resolve(context.Background(), "repo-sha256", "HEAD")
	if err != nil || len(resolved.CommitOID) != 64 || !ValidOID(resolved.CommitOID) {
		t.Fatalf("SHA-256 resolution: %+v %v", resolved, err)
	}
}

// TestGitResolverIgnoresHostileGitEnvironment proves source identity is
// selected by the configured root, even when the parent process selects a
// foreign Git dir, worktree, object directory, or injected config.
func TestGitResolverIgnoresHostileGitEnvironment(t *testing.T) {
	root, ownOID, resolver := gitFixture(t)
	foreignRoot, _, _ := gitFixture(t)
	if err := os.WriteFile(filepath.Join(foreignRoot, "tracked.txt"), []byte("foreign"), 0600); err != nil {
		t.Fatal(err)
	}
	gitRun(t, foreignRoot, "add", "tracked.txt")
	gitRun(t, foreignRoot, "commit", "-qm", "foreign")
	foreignOID := gitRun(t, foreignRoot, "rev-parse", "HEAD")
	if ownOID == foreignOID {
		t.Fatal("fixtures unexpectedly share a commit")
	}
	t.Setenv("GIT_DIR", filepath.Join(foreignRoot, ".git"))
	t.Setenv("GIT_WORK_TREE", root)
	t.Setenv("GIT_OBJECT_DIRECTORY", filepath.Join(foreignRoot, ".git", "objects"))
	t.Setenv("GIT_ALTERNATE_OBJECT_DIRECTORIES", filepath.Join(foreignRoot, ".git", "objects"))
	t.Setenv("GIT_INDEX_FILE", filepath.Join(foreignRoot, ".git", "index"))
	t.Setenv("GIT_CONFIG_COUNT", "1")
	t.Setenv("GIT_CONFIG_KEY_0", "core.bare")
	t.Setenv("GIT_CONFIG_VALUE_0", "true")
	resolved, err := resolver.Resolve(context.Background(), "repo-main", "HEAD")
	if err != nil || resolved.CommitOID != ownOID || resolved.Dirty {
		t.Fatalf("hostile Git environment changed source: %+v %v", resolved, err)
	}
	if _, err := resolver.Resolve(context.Background(), "repo-main", foreignOID); err == nil {
		t.Fatal("foreign object accepted through inherited Git environment")
	}
}
