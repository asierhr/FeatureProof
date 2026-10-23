package localvalidation

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"

	"github.com/asierhr/featureProof/internal/fixture"
	"github.com/asierhr/featureProof/internal/planning"
	"github.com/asierhr/featureProof/internal/revisionbinding"
)

// PackageVersion identifies the closed, local-directory evidence package format.
const PackageVersion = 1

const (
	maxPackageEntries = 10000
	maxArtifactBytes  = 64 << 20
	maxPackageBytes   = 256 << 20
	maxManifestBytes  = 1 << 20
	maxValueBytes     = 64 << 10
)

// PackageError contains a stable category and relative path, never rejected bytes.
type PackageError struct {
	Category string
	Path     string
}

func (e *PackageError) Error() string { return fmt.Sprintf("package %s: %s", e.Category, e.Path) }

// IsPackageError reports whether an error belongs to the offline package contract.
func IsPackageError(err error, category string) bool {
	var target *PackageError
	return errors.As(err, &target) && target.Category == category
}

// decodePackageJSON rejects unknown fields, duplicate keys and trailing values.
// Duplicate detection is recursive so a second spelling cannot replace a trusted field.
func decodePackageJSON(raw []byte, path string, target any) error {
	check := json.NewDecoder(bytes.NewReader(raw))
	check.UseNumber()
	if err := checkJSONValue(check, 0); err != nil {
		return &PackageError{Category: "invalid_schema", Path: path}
	}
	var trailing any
	if err := check.Decode(&trailing); err != io.EOF {
		return &PackageError{Category: "invalid_schema", Path: path}
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	decoder.UseNumber()
	if err := decoder.Decode(target); err != nil {
		return &PackageError{Category: "invalid_schema", Path: path}
	}
	return nil
}

func checkJSONValue(d *json.Decoder, depth int) error {
	if depth > 64 {
		return errors.New("nesting limit")
	}
	token, err := d.Token()
	if err != nil {
		return err
	}
	delim, ok := token.(json.Delim)
	if !ok {
		return nil
	}
	switch delim {
	case '{':
		seen := map[string]bool{}
		for d.More() {
			key, err := d.Token()
			if err != nil {
				return err
			}
			name, ok := key.(string)
			if !ok || seen[name] {
				return errors.New("duplicate key")
			}
			seen[name] = true
			if err := checkJSONValue(d, depth+1); err != nil {
				return err
			}
		}
	case '[':
		for d.More() {
			if err := checkJSONValue(d, depth+1); err != nil {
				return err
			}
		}
	default:
		return errors.New("unexpected delimiter")
	}
	closing, err := d.Token()
	want := json.Delim('}')
	if delim == '[' {
		want = ']'
	}
	if err != nil || closing != want {
		return errors.New("invalid closing delimiter")
	}
	return nil
}

// PackageManifest is the closed byte inventory. manifest.json does not list itself.
type PackageManifest struct {
	Version       int               `json:"version"`
	RunID         string            `json:"run_id"`
	BindingSHA256 string            `json:"binding_sha256"`
	Artifacts     []PackageArtifact `json:"artifacts"`
}

// PackageArtifact hashes the exact bytes of one canonical relative file path.
type PackageArtifact struct {
	Path      string `json:"path"`
	SizeBytes int64  `json:"size_bytes"`
	SHA256    string `json:"sha256"`
}

// SafeValue distinguishes absence, redaction and truncation from JSON null.
type SafeValue struct {
	State string `json:"state"`
	Value any    `json:"value,omitempty"`
}

// PackageIndex is the semantic, payload-bounded index of confirmed outcomes.
type PackageIndex struct {
	Version      int                           `json:"version"`
	RunID        string                        `json:"run_id"`
	Binding      revisionbinding.Binding       `json:"binding"`
	PlanBindings []revisionbinding.PlanBinding `json:"plan_bindings"`
	Status       ResultStatus                  `json:"status"`
	Constraints  []PackageConstraint           `json:"constraints"`
	Failures     []PackageFailure              `json:"failures"`
	Facts        []PackageFact                 `json:"facts"`
	Evidence     []PackageEvidence             `json:"evidence"`
	Reproduction []PackageReproduction         `json:"reproduction"`
	Fixtures     *fixture.FixtureSummary       `json:"fixtures,omitempty"`
}

type PackageConstraint struct {
	ConstraintID string       `json:"constraint_id"`
	Status       ResultStatus `json:"status"`
	EvidenceIDs  []string     `json:"evidence_ids"`
}

type PackageFailure struct {
	ConstraintID  string    `json:"constraint_id"`
	Scenario      string    `json:"scenario"`
	BatchID       string    `json:"batch_id,omitempty"`
	CheckID       string    `json:"check_id,omitempty"`
	StepID        string    `json:"step_id"`
	DeclarationID string    `json:"declaration_id"`
	NodePath      string    `json:"node_path"`
	Operator      string    `json:"operator"`
	Status        string    `json:"status"`
	Expected      SafeValue `json:"expected"`
	Observed      SafeValue `json:"observed"`
	EvidenceIDs   []string  `json:"evidence_ids"`
}

type PackageFact struct {
	Kind          string   `json:"kind"`
	ConstraintID  string   `json:"constraint_id"`
	PlanID        string   `json:"plan_id,omitempty"`
	StepID        string   `json:"step_id"`
	DeclarationID string   `json:"declaration_id,omitempty"`
	NodePath      string   `json:"node_path,omitempty"`
	Status        string   `json:"status"`
	EvidenceIDs   []string `json:"evidence_ids"`
}

type PackageEvidence struct {
	ID   string `json:"id"`
	Path string `json:"path"`
}

// PackageReproduction is declarative; a reader never executes it.
type PackageReproduction struct {
	ConstraintID        string   `json:"constraint_id"`
	Scenario            string   `json:"scenario"`
	StepIDs             []string `json:"step_ids"`
	EvidenceIDs         []string `json:"evidence_ids"`
	CommitOID           string   `json:"commit_oid"`
	RepositoryID        string   `json:"repository_id"`
	EnvironmentID       string   `json:"environment_id"`
	AttestationRef      string   `json:"attestation_ref"`
	ConstraintsSHA256   string   `json:"constraints_sha256"`
	ContractSHA256      string   `json:"contract_sha256"`
	ConfigurationSHA256 string   `json:"configuration_sha256"`
	PlanSHA256          []string `json:"plan_sha256"`
	Prerequisite        string   `json:"prerequisite"`
	Replayability       string   `json:"replayability"`
	CredentialRequired  bool     `json:"credential_required"`
}

func decodePackageIndex(raw []byte) (PackageIndex, error) {
	var index PackageIndex
	if err := decodePackageJSON(raw, "package.json", &index); err != nil {
		return PackageIndex{}, err
	}
	if index.Version != PackageVersion {
		return PackageIndex{}, &PackageError{Category: "unsupported_version", Path: "package.json"}
	}
	if index.RunID == "" || index.PlanBindings == nil || index.Constraints == nil || index.Failures == nil || index.Facts == nil || index.Evidence == nil || index.Reproduction == nil {
		return PackageIndex{}, &PackageError{Category: "invalid_schema", Path: "package.json"}
	}
	if len(index.Constraints) > maxPackageEntries || len(index.Failures) > maxPackageEntries || len(index.Facts) > maxPackageEntries || len(index.Evidence) > maxPackageEntries || len(index.Reproduction) > maxPackageEntries {
		return PackageIndex{}, &PackageError{Category: "limit_exceeded", Path: "package.json"}
	}
	return index, nil
}

// buildPackageIndex copies only deterministic, payload-free run outcomes.
func buildPackageIndex(ctx context.Context, snapshot RunSnapshot) (PackageIndex, error) {
	if err := ctx.Err(); err != nil {
		return PackageIndex{}, err
	}
	if snapshot.Binding == nil || snapshot.ID != snapshot.Binding.RunID {
		return PackageIndex{}, &PackageError{Category: "binding_integrity", Path: "package.json"}
	}
	if err := snapshot.Binding.Verify(ctx); err != nil {
		return PackageIndex{}, &PackageError{Category: "binding_integrity", Path: "package.json"}
	}
	if err := revisionbinding.VerifyPlanChain(ctx, *snapshot.Binding, snapshot.PlanBindings); err != nil {
		return PackageIndex{}, &PackageError{Category: "binding_integrity", Path: "package.json"}
	}
	if !validPackageStatus(snapshot.Status) {
		return PackageIndex{}, &PackageError{Category: "invalid_schema", Path: "package.json"}
	}
	index := PackageIndex{Version: PackageVersion, RunID: snapshot.ID, Binding: *snapshot.Binding, PlanBindings: append([]revisionbinding.PlanBinding{}, snapshot.PlanBindings...), Status: snapshot.Status, Constraints: []PackageConstraint{}, Failures: []PackageFailure{}, Facts: []PackageFact{}, Evidence: []PackageEvidence{}, Reproduction: []PackageReproduction{}}
	seen := map[string]bool{}
	for _, constraint := range snapshot.Constraints {
		if err := ctx.Err(); err != nil {
			return PackageIndex{}, err
		}
		if !revisionbinding.ValidID(constraint.ConstraintID) || seen[constraint.ConstraintID] || !validConstraintStatus(constraint.Status) {
			return PackageIndex{}, &PackageError{Category: "invalid_schema", Path: "package.json"}
		}
		if constraint.Status == ResultPassed && len(constraint.EvidenceIDs) == 0 {
			return PackageIndex{}, &PackageError{Category: "broken_reference", Path: "package.json"}
		}
		if constraint.Status == ResultFailed && len(constraint.packageFailures) == 0 {
			return PackageIndex{}, &PackageError{Category: "broken_reference", Path: "package.json"}
		}
		seen[constraint.ConstraintID] = true
		index.Constraints = append(index.Constraints, PackageConstraint{ConstraintID: constraint.ConstraintID, Status: constraint.Status, EvidenceIDs: append([]string{}, constraint.EvidenceIDs...)})
		index.Failures = append(index.Failures, constraint.packageFailures...)
		var appendAssertions func(string, []planning.AssertionResult)
		appendAssertions = func(stepID string, assertions []planning.AssertionResult) {
			for _, assertion := range assertions {
				index.Facts = append(index.Facts, PackageFact{Kind: "assertion", ConstraintID: constraint.ConstraintID, StepID: stepID, DeclarationID: assertion.DeclarationID, NodePath: assertion.NodePath, Status: string(assertion.Status), EvidenceIDs: append([]string{}, assertion.EvidenceIDs...)})
				appendAssertions(stepID, assertion.Children)
			}
		}
		for _, step := range constraint.Steps {
			index.Facts = append(index.Facts, PackageFact{Kind: "step", ConstraintID: constraint.ConstraintID, StepID: step.StepID, Status: string(step.Status), EvidenceIDs: append([]string{}, step.EvidenceIDs...)})
			appendAssertions(step.StepID, step.AssertionResults)
		}
	}
	if snapshot.Status == ResultPassed || snapshot.Status == ResultFailed {
		if aggregateStatus(snapshot.Constraints) != snapshot.Status {
			return PackageIndex{}, &PackageError{Category: "invalid_schema", Path: "package.json"}
		}
	}
	return index, nil
}

func validPackageStatus(status ResultStatus) bool {
	switch status {
	case ResultPassed, ResultFailed, ResultInconclusive, ResultCancelled, ResultError:
		return true
	}
	return false
}

func validConstraintStatus(status ResultStatus) bool {
	switch status {
	case ResultPassed, ResultFailed, ResultInconclusive:
		return true
	}
	return false
}
