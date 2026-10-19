package validationworkflow

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
)

// LocalExecutableStore owns private unredacted plans needed to resume an already-confirmed generation.
type LocalExecutableStore struct{ root string }

// OpenLocalExecutableStore creates a private directory outside public artifacts and packages.
func OpenLocalExecutableStore(root string) (*LocalExecutableStore, error) {
	abs, err := filepath.Abs(root)
	if err != nil || root == "" || os.MkdirAll(abs, 0o700) != nil {
		return nil, &Error{Code: CodeInvalid, Operation: "executable_store"}
	}
	return &LocalExecutableStore{root: abs}, nil
}

// Commit atomically stores one immutable executable snapshot with owner-only permissions.
func (s *LocalExecutableStore) Commit(ctx context.Context, handle string, material ExecutableMaterial) error {
	if ctx.Err() != nil || !validExecutableMaterial(handle, material) {
		return &Error{Code: CodeInvalid, Operation: "executable_commit"}
	}
	raw, err := json.Marshal(material)
	if err != nil {
		return &Error{Code: CodeInternal, Operation: "executable_commit"}
	}
	path := filepath.Join(s.root, handle+".private.json")
	if existing, readErr := os.ReadFile(path); readErr == nil {
		if !bytes.Equal(existing, raw) {
			return &Error{Code: CodeConflict, Operation: "executable_commit"}
		}
		return nil
	}
	tmp, err := os.CreateTemp(s.root, ".executable-*.tmp")
	if err != nil {
		return &Error{Code: CodeUnavailable, Operation: "executable_commit"}
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if err = tmp.Chmod(0o600); err == nil {
		_, err = tmp.Write(raw)
	}
	if err == nil {
		err = tmp.Sync()
	}
	if closeErr := tmp.Close(); err == nil {
		err = closeErr
	}
	if err == nil {
		err = os.Rename(tmpName, path)
	}
	if err != nil {
		return &Error{Code: CodeUnavailable, Operation: "executable_commit"}
	}
	return nil
}

// Load returns a detached private snapshot for mandatory revalidation by agentplanning.
func (s *LocalExecutableStore) Load(ctx context.Context, handle string) (ExecutableMaterial, error) {
	if ctx.Err() != nil || !safeID.MatchString(handle) {
		return ExecutableMaterial{}, &Error{Code: CodeInvalid, Operation: "executable_load"}
	}
	raw, err := os.ReadFile(filepath.Join(s.root, handle+".private.json"))
	if err != nil {
		return ExecutableMaterial{}, &Error{Code: CodeUnavailable, Operation: "executable_load"}
	}
	var material ExecutableMaterial
	if json.Unmarshal(raw, &material) != nil || !validExecutableMaterial(handle, material) {
		return ExecutableMaterial{}, &Error{Code: CodeConflict, Operation: "executable_integrity"}
	}
	material.CommitmentKey = append([]byte(nil), material.CommitmentKey...)
	return material, nil
}

// Delete retires private executable values idempotently after terminal cleanup is confirmed.
func (s *LocalExecutableStore) Delete(ctx context.Context, handle string) error {
	if ctx.Err() != nil || !safeID.MatchString(handle) {
		return &Error{Code: CodeInvalid, Operation: "executable_delete"}
	}
	err := os.Remove(filepath.Join(s.root, handle+".private.json"))
	if err != nil && !os.IsNotExist(err) {
		return &Error{Code: CodeUnavailable, Operation: "executable_delete"}
	}
	return nil
}

// validExecutableMaterial validates the closed private envelope without exposing its values.
func validExecutableMaterial(handle string, material ExecutableMaterial) bool {
	return material.Version == ArtifactVersion && material.WorkflowID == handle && len(material.ArtifactSHA256) == 64 && len(material.ContextDigest) == 64 && len(material.RevisionDigest) == 64 && len(material.TargetDigest) == 64 && material.Snapshot.ID != "" && len(material.Snapshot.Checks) > 0 && len(material.CommitmentKey) == 32
}
