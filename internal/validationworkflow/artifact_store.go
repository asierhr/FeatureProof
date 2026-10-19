package validationworkflow

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
)

// LocalArtifactStore owns immutable canonical plan artifacts below one trusted root.
type LocalArtifactStore struct{ root string }

// OpenLocalArtifactStore validates and creates the private artifact root.
func OpenLocalArtifactStore(root string) (*LocalArtifactStore, error) {
	absolute, err := filepath.Abs(root)
	if err != nil || root == "" {
		return nil, &Error{Code: CodeInvalid, Operation: "artifact_store"}
	}
	if err = os.MkdirAll(absolute, 0o700); err != nil {
		return nil, &Error{Code: CodeUnavailable, Operation: "artifact_store"}
	}
	return &LocalArtifactStore{root: absolute}, nil
}

// Commit publishes one verified artifact exactly once using fsync and atomic rename.
func (s *LocalArtifactStore) Commit(ctx context.Context, artifact PlanArtifact) error {
	if ctx.Err() != nil {
		return &Error{Code: CodeCancelled, Operation: "artifact_commit"}
	}
	if verifyArtifact(artifact, artifact.WorkflowID) != nil {
		return &Error{Code: CodeInvalid, Operation: "artifact_commit"}
	}
	destination := filepath.Join(s.root, artifact.WorkflowID+".plan.json")
	if raw, err := os.ReadFile(destination); err == nil {
		var current PlanArtifact
		if json.Unmarshal(raw, &current) != nil || current.ArtifactSHA256 != artifact.ArtifactSHA256 {
			return &Error{Code: CodeConflict, Operation: "artifact_commit"}
		}
		return nil
	}
	raw, err := json.Marshal(artifact)
	if err != nil {
		return &Error{Code: CodeInternal, Operation: "artifact_commit"}
	}
	temporary, err := os.CreateTemp(s.root, ".plan-*.tmp")
	if err != nil {
		return &Error{Code: CodeUnavailable, Operation: "artifact_commit"}
	}
	name := temporary.Name()
	defer os.Remove(name)
	if err = temporary.Chmod(0o600); err == nil {
		_, err = temporary.Write(raw)
	}
	if err == nil {
		err = temporary.Sync()
	}
	closeErr := temporary.Close()
	if err == nil {
		err = closeErr
	}
	if err == nil {
		err = os.Rename(name, destination)
	}
	if err != nil {
		return &Error{Code: CodeUnavailable, Operation: "artifact_commit"}
	}
	return nil
}

// Load reads and verifies one immutable artifact without accepting caller-controlled paths.
func (s *LocalArtifactStore) Load(ctx context.Context, handle string) (PlanArtifact, error) {
	if ctx.Err() != nil {
		return PlanArtifact{}, &Error{Code: CodeCancelled, Operation: "artifact_load"}
	}
	if !safeID.MatchString(handle) {
		return PlanArtifact{}, &Error{Code: CodeInvalid, Operation: "artifact_load"}
	}
	raw, err := os.ReadFile(filepath.Join(s.root, handle+".plan.json"))
	if errors.Is(err, os.ErrNotExist) {
		return PlanArtifact{}, ErrNotFound
	}
	if err != nil {
		return PlanArtifact{}, &Error{Code: CodeUnavailable, Operation: "artifact_load"}
	}
	var artifact PlanArtifact
	if json.Unmarshal(raw, &artifact) != nil || verifyArtifact(artifact, handle) != nil {
		return PlanArtifact{}, &Error{Code: CodeConflict, Operation: "artifact_integrity"}
	}
	return artifact, nil
}
