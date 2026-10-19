package validationworkflow

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"

	"github.com/asierhr/featureProof/internal/localvalidation"
)

// LocalPackageReader verifies the existing evidence package and its exact generated plan artifact.
type LocalPackageReader struct{ Artifacts PlanArtifactStore }

// Read accepts functional status only after package lineage, artifact digest and plan bytes all match.
func (r LocalPackageReader) Read(ctx context.Context, dir, handle, artifactDigest string) (PackageProjection, error) {
	if r.Artifacts == nil {
		return PackageProjection{}, &Error{Code: CodeInternal, Operation: "package_reader"}
	}
	index, err := localvalidation.ReadPackage(ctx, dir)
	if err != nil {
		return PackageProjection{}, &Error{Code: CodeUnavailable, Operation: "package_verify"}
	}
	artifact, err := r.Artifacts.Load(ctx, handle)
	if err != nil || artifact.ArtifactSHA256 != artifactDigest || index.RunID != handle || index.Binding.RunID != handle || index.Binding.BindingSHA256 == "" {
		return PackageProjection{}, &Error{Code: CodeConflict, Operation: "package_lineage"}
	}
	raw, err := os.ReadFile(filepath.Join(dir, "plan.json"))
	if err != nil {
		return PackageProjection{}, &Error{Code: CodeUnavailable, Operation: "package_plan"}
	}
	var document struct {
		PlanArtifactSHA256 string                           `json:"plan_artifact_sha256"`
		Plans              []localvalidation.PlanProjection `json:"plans"`
	}
	if json.Unmarshal(raw, &document) != nil {
		return PackageProjection{}, &Error{Code: CodeConflict, Operation: "package_plan"}
	}
	if document.PlanArtifactSHA256 != artifactDigest || !reflect.DeepEqual(artifact.Plans, document.Plans) {
		return PackageProjection{}, &Error{Code: CodeConflict, Operation: "package_plan"}
	}
	outcome := packageOutcome(index.Status)
	return PackageProjection{RunID: index.RunID, BindingSHA256: index.Binding.BindingSHA256, PackageDir: dir, Outcome: outcome}, nil
}

// packageOutcome maps the deterministic runner vocabulary without making outcomes more favorable.
func packageOutcome(status localvalidation.ResultStatus) Outcome {
	switch status {
	case localvalidation.ResultPassed:
		return OutcomePassed
	case localvalidation.ResultFailed:
		return OutcomeFailed
	case localvalidation.ResultCancelled:
		return OutcomeCancelled
	case localvalidation.ResultError:
		return OutcomeError
	default:
		return OutcomeInconclusive
	}
}
