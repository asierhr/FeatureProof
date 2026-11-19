package projectconfig

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestLocalResourceArtifactStoreSurvivesRestartAndRejectsMutableBacking proves historical bytes do not depend on current paths or process memory.
func TestLocalResourceArtifactStoreSurvivesRestartAndRejectsMutableBacking(t *testing.T) {
	root := filepath.Join(t.TempDir(), "artifacts")
	key, _ := NewProjectKey("project", "repository")
	oldDescriptor := ResourceDescriptor{Kind: ResourceContract, ID: "contract", Version: 1, Digest: strings.Repeat("a", 64), ProjectKey: key}
	newDescriptor := oldDescriptor
	newDescriptor.Version = 2
	newDescriptor.Digest = strings.Repeat("b", 64)
	first, err := NewLocalResourceArtifactStore(root)
	if err != nil {
		t.Fatal(err)
	}
	if err = first.Put(context.Background(), oldDescriptor, []byte("old backing")); err != nil {
		t.Fatal(err)
	}
	second, err := NewLocalResourceArtifactStore(root)
	if err != nil {
		t.Fatal(err)
	}
	if err = second.Put(context.Background(), newDescriptor, []byte("new backing")); err != nil {
		t.Fatal(err)
	}
	old, err := second.Get(context.Background(), oldDescriptor)
	if err != nil || string(old) != "old backing" {
		t.Fatalf("historical artifact = %q, %v", old, err)
	}
	path := second.path(oldDescriptorKey(t, oldDescriptor))
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	raw[len(raw)/2] ^= 1
	if err = os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err = second.Get(context.Background(), oldDescriptor); !IsCategory(err, CategoryIntegrity) {
		t.Fatalf("corruption = %v", err)
	}
}

// TestLocalResourceArtifactStoreFailClosedBranches covers bounded input, redelivery, absence, cancellation and integrity failures.
func TestLocalResourceArtifactStoreFailClosedBranches(t *testing.T) {
	key, _ := NewProjectKey("project", "repository")
	descriptor := ResourceDescriptor{Kind: ResourceContract, ID: "contract", Version: 1, Digest: strings.Repeat("a", 64), ProjectKey: key}
	if _, err := NewLocalResourceArtifactStore("relative"); !IsCategory(err, CategoryInvalid) {
		t.Fatalf("relative root = %v", err)
	}
	blockedParent := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(blockedParent, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := NewLocalResourceArtifactStore(filepath.Join(blockedParent, "child")); !IsCategory(err, CategoryUnavailable) {
		t.Fatalf("blocked root = %v", err)
	}
	store, err := NewLocalResourceArtifactStore(filepath.Join(t.TempDir(), "store"))
	if err != nil {
		t.Fatal(err)
	}
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	if err = store.Put(cancelled, descriptor, []byte("payload")); !IsCategory(err, CategoryCancelled) {
		t.Fatalf("cancelled put = %v", err)
	}
	if _, err = store.Get(cancelled, descriptor); !IsCategory(err, CategoryCancelled) {
		t.Fatalf("cancelled get = %v", err)
	}
	invalid := descriptor
	invalid.Version = 0
	if _, err = SealResourceArtifact(invalid, nil); !IsCategory(err, CategoryInvalid) {
		t.Fatalf("invalid seal = %v", err)
	}
	if _, err = store.Get(context.Background(), invalid); !IsCategory(err, CategoryInvalid) {
		t.Fatalf("invalid get = %v", err)
	}
	if _, err = SealResourceArtifact(descriptor, make([]byte, MaxCanonicalBytes+1)); !IsCategory(err, CategoryInvalid) {
		t.Fatalf("oversized seal = %v", err)
	}
	if _, err = store.Get(context.Background(), descriptor); !IsCategory(err, CategoryMissingReference) {
		t.Fatalf("missing get = %v", err)
	}
	if err = store.Put(context.Background(), descriptor, []byte("payload")); err != nil {
		t.Fatal(err)
	}
	if err = store.Put(context.Background(), descriptor, []byte("payload")); err != nil {
		t.Fatalf("exact redelivery = %v", err)
	}
	if err = store.Put(context.Background(), descriptor, []byte("different")); !IsCategory(err, CategoryIntegrity) {
		t.Fatalf("divergent redelivery = %v", err)
	}
	path := store.path(oldDescriptorKey(t, descriptor))
	artifact, err := SealResourceArtifact(descriptor, []byte("payload"))
	if err != nil {
		t.Fatal(err)
	}
	artifact.Payload = []byte("tampered")
	raw, _ := json.Marshal(artifact)
	if err = os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err = store.Get(context.Background(), descriptor); !IsCategory(err, CategoryIntegrity) {
		t.Fatalf("payload digest corruption = %v", err)
	}
	if err = os.WriteFile(path, []byte(`{"key":{},"payload":"","payload_digest":"","unexpected":true}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err = store.Get(context.Background(), descriptor); !IsCategory(err, CategoryIntegrity) {
		t.Fatalf("unknown field corruption = %v", err)
	}
	if err = os.WriteFile(path, make([]byte, 2*MaxCanonicalBytes+1), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err = store.Get(context.Background(), descriptor); !IsCategory(err, CategoryUnavailable) {
		t.Fatalf("oversized stored artifact = %v", err)
	}
	invalidKind := descriptor
	invalidKind.Kind = ResourceKind("unknown")
	if _, err = SealResourceArtifact(invalidKind, nil); !IsCategory(err, CategoryInvalid) {
		t.Fatalf("invalid kind = %v", err)
	}
	t.Run("stored path is directory", func(t *testing.T) {
		root := filepath.Join(t.TempDir(), "directory-path")
		candidate, createErr := NewLocalResourceArtifactStore(root)
		if createErr != nil {
			t.Fatal(createErr)
		}
		path := candidate.path(oldDescriptorKey(t, descriptor))
		if createErr = os.MkdirAll(path, 0o700); createErr != nil {
			t.Fatal(createErr)
		}
		if putErr := candidate.Put(context.Background(), descriptor, []byte("payload")); !IsCategory(putErr, CategoryUnavailable) {
			t.Fatalf("directory read failure = %v", putErr)
		}
	})
	t.Run("parent is file", func(t *testing.T) {
		root := filepath.Join(t.TempDir(), "file-parent")
		candidate, createErr := NewLocalResourceArtifactStore(root)
		if createErr != nil {
			t.Fatal(createErr)
		}
		if createErr = os.MkdirAll(filepath.Join(root, descriptor.ProjectKey.ProjectID, descriptor.ProjectKey.RepositoryID), 0o700); createErr != nil {
			t.Fatal(createErr)
		}
		if createErr = os.WriteFile(filepath.Join(root, descriptor.ProjectKey.ProjectID, descriptor.ProjectKey.RepositoryID, string(descriptor.Kind)), []byte("blocked"), 0o600); createErr != nil {
			t.Fatal(createErr)
		}
		if putErr := candidate.Put(context.Background(), descriptor, []byte("payload")); !IsCategory(putErr, CategoryUnavailable) {
			t.Fatalf("parent creation failure = %v", putErr)
		}
	})
	t.Run("staging path is directory", func(t *testing.T) {
		root := filepath.Join(t.TempDir(), "directory-stage")
		candidate, createErr := NewLocalResourceArtifactStore(root)
		if createErr != nil {
			t.Fatal(createErr)
		}
		path := candidate.path(oldDescriptorKey(t, descriptor))
		if createErr = os.MkdirAll(filepath.Dir(path), 0o700); createErr != nil {
			t.Fatal(createErr)
		}
		if createErr = os.Mkdir(path+".tmp", 0o700); createErr != nil {
			t.Fatal(createErr)
		}
		if putErr := candidate.Put(context.Background(), descriptor, []byte("payload")); !IsCategory(putErr, CategoryUnavailable) {
			t.Fatalf("staging write failure = %v", putErr)
		}
	})
}

// TestLocalResourceArtifactStoreRejectsLegacyUnscopedEnvelope keeps pre-project identities fail-closed instead of aliasing them.
func TestLocalResourceArtifactStoreRejectsLegacyUnscopedEnvelope(t *testing.T) {
	root := filepath.Join(t.TempDir(), "legacy")
	store, err := NewLocalResourceArtifactStore(root)
	if err != nil {
		t.Fatal(err)
	}
	key, _ := NewProjectKey("project", "repository")
	descriptor := ResourceDescriptor{Kind: ResourceContract, ID: "contract", Version: 1, Digest: strings.Repeat("a", 64), ProjectKey: key}
	artifact, err := SealResourceArtifact(descriptor, []byte("legacy"))
	if err != nil {
		t.Fatal(err)
	}
	artifact.Key.ProjectKey = ProjectKey{}
	raw, err := json.Marshal(artifact)
	if err != nil {
		t.Fatal(err)
	}
	path := store.path(oldDescriptorKey(t, descriptor))
	if err = os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err = store.Get(context.Background(), descriptor); !IsCategory(err, CategoryIntegrity) {
		t.Fatalf("legacy unscoped envelope accepted: %v", err)
	}
	legacyPath := filepath.Join(root, string(descriptor.Kind), descriptor.ID, "1", descriptor.Digest+".json")
	if err = os.MkdirAll(filepath.Dir(legacyPath), 0o700); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(legacyPath, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	if err = os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if _, err = store.Get(context.Background(), descriptor); !IsCategory(err, CategoryMissingReference) {
		t.Fatalf("legacy unscoped path aliased into project scope: %v", err)
	}
}

// oldDescriptorKey keeps corruption assertions tied to the same validated production path derivation.
func oldDescriptorKey(t *testing.T, descriptor ResourceDescriptor) ResourceArtifactKey {
	t.Helper()
	key, err := artifactKey(descriptor)
	if err != nil {
		t.Fatal(err)
	}
	return key
}
