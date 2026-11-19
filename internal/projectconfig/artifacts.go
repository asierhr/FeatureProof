package projectconfig

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"sync"
)

// ResourceArtifactKey is the immutable catalog identity used for restart-safe consumer reconstruction.
type ResourceArtifactKey struct {
	ProjectKey ProjectKey   `json:"project_key"`
	Kind       ResourceKind `json:"kind"`
	ID         string       `json:"id"`
	Version    uint64       `json:"version"`
	Digest     string       `json:"digest"`
}

// ResourceArtifact contains bounded non-secret composition bytes and their independently verified content digest.
type ResourceArtifact struct {
	Key           ResourceArtifactKey `json:"key"`
	Payload       []byte              `json:"payload"`
	PayloadDigest string              `json:"payload_digest"`
}

// ResourceArtifactStore is the consumer-owned durable port shared by local and PostgreSQL composition.
type ResourceArtifactStore interface {
	Put(context.Context, ResourceDescriptor, []byte) error
	PutIfAbsent(context.Context, ResourceDescriptor, []byte) (bool, error)
	Get(context.Context, ResourceDescriptor) ([]byte, error)
	DeleteExact(context.Context, ResourceDescriptor, []byte) error
}

// SealResourceArtifact creates the integrity envelope persisted by every artifact backend.
func SealResourceArtifact(descriptor ResourceDescriptor, payload []byte) (ResourceArtifact, error) {
	key, err := artifactKey(descriptor)
	if err != nil || len(payload) > MaxCanonicalBytes {
		return ResourceArtifact{}, invalid("artifact.seal", "descriptor")
	}
	digest := sha256.Sum256(payload)
	return ResourceArtifact{Key: key, Payload: append([]byte(nil), payload...), PayloadDigest: hex.EncodeToString(digest[:])}, nil
}

// VerifyResourceArtifact authenticates catalog identity and bytes before any consumer is constructed.
func VerifyResourceArtifact(descriptor ResourceDescriptor, artifact ResourceArtifact) ([]byte, error) {
	key, err := artifactKey(descriptor)
	if err != nil || artifact.Key != key || !validDigest(artifact.PayloadDigest) || len(artifact.Payload) > MaxCanonicalBytes {
		return nil, &Error{Category: CategoryIntegrity, Operation: "artifact.verify"}
	}
	digest := sha256.Sum256(artifact.Payload)
	if hex.EncodeToString(digest[:]) != artifact.PayloadDigest {
		return nil, &Error{Category: CategoryIntegrity, Operation: "artifact.verify"}
	}
	return append([]byte(nil), artifact.Payload...), nil
}

// LocalResourceArtifactStore persists immutable artifacts by project and descriptor identity, not by mutable selector paths.
type LocalResourceArtifactStore struct {
	root string
	mu   sync.Mutex
}

// NewLocalResourceArtifactStore prepares a private durable root used across process restarts.
func NewLocalResourceArtifactStore(root string) (*LocalResourceArtifactStore, error) {
	if !filepath.IsAbs(root) {
		return nil, invalid("artifact.compose", "root")
	}
	if err := os.MkdirAll(root, 0o700); err != nil {
		return nil, &Error{Category: CategoryUnavailable, Operation: "artifact.compose", Cause: err}
	}
	return &LocalResourceArtifactStore{root: root}, nil
}

// Put publishes one artifact atomically and rejects divergent reuse of an immutable catalog identity.
func (store *LocalResourceArtifactStore) Put(ctx context.Context, descriptor ResourceDescriptor, payload []byte) error {
	_, err := store.PutIfAbsent(ctx, descriptor, payload)
	return err
}

// PutIfAbsent publishes one artifact and reports whether this call created the immutable entry.
func (store *LocalResourceArtifactStore) PutIfAbsent(ctx context.Context, descriptor ResourceDescriptor, payload []byte) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, classifyContext(ctx, err, "artifact.put")
	}
	artifact, err := SealResourceArtifact(descriptor, payload)
	if err != nil {
		return false, err
	}
	key := artifact.Key
	store.mu.Lock()
	defer store.mu.Unlock()
	raw, err := json.Marshal(artifact)
	if err != nil {
		return false, &Error{Category: CategoryUnavailable, Operation: "artifact.put", Cause: err}
	}
	path := store.path(key)
	if current, readErr := os.ReadFile(path); readErr == nil {
		if bytes.Equal(current, raw) {
			return false, nil
		}
		return false, &Error{Category: CategoryIntegrity, Operation: "artifact.put"}
	} else if !errors.Is(readErr, os.ErrNotExist) {
		return false, &Error{Category: CategoryUnavailable, Operation: "artifact.put", Cause: readErr}
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return false, &Error{Category: CategoryUnavailable, Operation: "artifact.put", Cause: err}
	}
	temporary := path + ".tmp"
	if err := os.WriteFile(temporary, raw, 0o600); err != nil {
		return false, &Error{Category: CategoryUnavailable, Operation: "artifact.put", Cause: err}
	}
	if err := os.Rename(temporary, path); err != nil {
		_ = os.Remove(temporary)
		return false, &Error{Category: CategoryUnavailable, Operation: "artifact.put", Cause: err}
	}
	return true, nil
}

// DeleteExact compensates only bytes created by an uncommitted admission and rejects divergent replacement.
func (store *LocalResourceArtifactStore) DeleteExact(ctx context.Context, descriptor ResourceDescriptor, payload []byte) error {
	if err := ctx.Err(); err != nil {
		return classifyContext(ctx, err, "artifact.delete")
	}
	artifact, err := SealResourceArtifact(descriptor, payload)
	if err != nil {
		return err
	}
	raw, err := json.Marshal(artifact)
	if err != nil {
		return &Error{Category: CategoryUnavailable, Operation: "artifact.delete", Cause: err}
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	path := store.path(artifact.Key)
	current, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return &Error{Category: CategoryUnavailable, Operation: "artifact.delete", Cause: err}
	}
	if !bytes.Equal(current, raw) {
		return &Error{Category: CategoryIntegrity, Operation: "artifact.delete"}
	}
	if err = os.Remove(path); err != nil {
		return &Error{Category: CategoryUnavailable, Operation: "artifact.delete", Cause: err}
	}
	return nil
}

// Get reloads an exact historical artifact and verifies both catalog identity and payload digest before returning bytes.
func (store *LocalResourceArtifactStore) Get(ctx context.Context, descriptor ResourceDescriptor) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, classifyContext(ctx, err, "artifact.get")
	}
	key, err := artifactKey(descriptor)
	if err != nil {
		return nil, invalid("artifact.get", "descriptor")
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	raw, err := os.ReadFile(store.path(key))
	if errors.Is(err, os.ErrNotExist) {
		return nil, &Error{Category: CategoryMissingReference, Operation: "artifact.get"}
	}
	if err != nil || len(raw) > 2*MaxCanonicalBytes {
		return nil, &Error{Category: CategoryUnavailable, Operation: "artifact.get", Cause: err}
	}
	var artifact ResourceArtifact
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&artifact) != nil {
		return nil, &Error{Category: CategoryIntegrity, Operation: "artifact.get"}
	}
	return VerifyResourceArtifact(descriptor, artifact)
}

// artifactKey admits only descriptor identities safe for durable path derivation and closed catalog lookup.
func artifactKey(descriptor ResourceDescriptor) (ResourceArtifactKey, error) {
	if descriptor.ProjectKey.Validate() != nil || !canonicalID.MatchString(descriptor.ID) || descriptor.Version == 0 || !validDigest(descriptor.Digest) {
		return ResourceArtifactKey{}, errors.New("invalid artifact identity")
	}
	switch descriptor.Kind {
	case ResourceContract, ResourceEnvironment, ResourceProvider, ResourceIdentity, ResourceFixture, ResourceRuntimeProfile, ResourceModelProfile, ResourceAuthProfile, ResourcePolicy:
	default:
		return ResourceArtifactKey{}, errors.New("invalid artifact kind")
	}
	return ResourceArtifactKey{ProjectKey: descriptor.ProjectKey, Kind: descriptor.Kind, ID: descriptor.ID, Version: descriptor.Version, Digest: descriptor.Digest}, nil
}

// path maps one validated immutable identity to its unique non-overlapping durable artifact location.
func (store *LocalResourceArtifactStore) path(key ResourceArtifactKey) string {
	return filepath.Join(store.root, key.ProjectKey.ProjectID, key.ProjectKey.RepositoryID, string(key.Kind), key.ID, strconv.FormatUint(key.Version, 10), fmt.Sprintf("%s.json", key.Digest))
}
