package validationworkflow

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"

	"github.com/asierhr/featureProof/internal/functionalflow"
)

// LocalFunctionalFlowArtifactStore owns immutable FlowArtifact/v1 documents below one trusted root.
type LocalFunctionalFlowArtifactStore struct{ root string }

// OpenLocalFunctionalFlowArtifactStore creates the private artifact root used before fixture preparation.
func OpenLocalFunctionalFlowArtifactStore(root string) (*LocalFunctionalFlowArtifactStore, error) {
	absolute, err := filepath.Abs(root)
	if err != nil || root == "" {
		return nil, &Error{Code: CodeInvalid, Operation: "functional_flow_store"}
	}
	if err = os.MkdirAll(absolute, 0o700); err != nil {
		return nil, &Error{Code: CodeUnavailable, Operation: "functional_flow_store"}
	}
	return &LocalFunctionalFlowArtifactStore{root: absolute}, nil
}

// Commit durably publishes one exact validated artifact before any fixture or business operation.
func (s *LocalFunctionalFlowArtifactStore) Commit(ctx context.Context, handle string, artifact functionalflow.Artifact) error {
	if ctx.Err() != nil || !safeID.MatchString(handle) || functionalflow.ValidateArtifact(artifact) != nil {
		return &Error{Code: CodeInvalid, Operation: "functional_flow_commit"}
	}
	destination := filepath.Join(s.root, handle+".flow.json")
	return writeImmutableJSON(destination, artifact, "functional_flow_commit")
}

// Load verifies the immutable artifact digest before execution or recovery uses its authority.
func (s *LocalFunctionalFlowArtifactStore) Load(ctx context.Context, handle string) (functionalflow.Artifact, error) {
	if ctx.Err() != nil || !safeID.MatchString(handle) {
		return functionalflow.Artifact{}, &Error{Code: CodeInvalid, Operation: "functional_flow_load"}
	}
	raw, err := os.ReadFile(filepath.Join(s.root, handle+".flow.json"))
	if errors.Is(err, os.ErrNotExist) {
		return functionalflow.Artifact{}, ErrNotFound
	}
	if err != nil {
		return functionalflow.Artifact{}, &Error{Code: CodeUnavailable, Operation: "functional_flow_load"}
	}
	var artifact functionalflow.Artifact
	if json.Unmarshal(raw, &artifact) != nil || functionalflow.ValidateArtifact(artifact) != nil {
		return functionalflow.Artifact{}, &Error{Code: CodeConflict, Operation: "functional_flow_integrity"}
	}
	return artifact, nil
}

// writeImmutableJSON atomically links one fully synced file and accepts only byte-identical concurrent redelivery.
func writeImmutableJSON(destination string, value any, operation string) error {
	raw, err := json.Marshal(value)
	if err != nil {
		return &Error{Code: CodeInternal, Operation: operation}
	}
	if prior, readErr := os.ReadFile(destination); readErr == nil {
		if string(prior) == string(raw) {
			return nil
		}
		return &Error{Code: CodeConflict, Operation: operation}
	} else if !errors.Is(readErr, os.ErrNotExist) {
		return &Error{Code: CodeUnavailable, Operation: operation}
	}
	temporary, err := os.CreateTemp(filepath.Dir(destination), ".flow-*.tmp")
	if err != nil {
		return &Error{Code: CodeUnavailable, Operation: operation}
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
		err = os.Link(name, destination)
	}
	if err == nil {
		return nil
	}
	if prior, readErr := os.ReadFile(destination); readErr == nil {
		if string(prior) == string(raw) {
			return nil
		}
		return &Error{Code: CodeConflict, Operation: operation}
	}
	return &Error{Code: CodeUnavailable, Operation: operation}
}
