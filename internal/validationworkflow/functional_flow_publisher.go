package validationworkflow

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"

	"github.com/asierhr/featureProof/internal/fixture"
	"github.com/asierhr/featureProof/internal/functionalflow"
)

// FunctionalFlowEvidenceManifestSource returns authenticated safe evidence metadata for one staged primary result.
type FunctionalFlowEvidenceManifestSource interface {
	Records(context.Context, string, functionalflow.Result) ([]functionalflow.EvidenceRecord, error)
}

// LocalFunctionalFlowPublisher owns staged primary proof and one idempotent post-cleanup package per workflow.
type LocalFunctionalFlowPublisher struct {
	root      string
	Artifacts FunctionalFlowArtifactStore
	Evidence  FunctionalFlowEvidenceManifestSource
	Journal   functionalflow.EventJournal
}

// OpenLocalFunctionalFlowPublisher creates the package root without publishing any result.
func OpenLocalFunctionalFlowPublisher(root string, artifacts FunctionalFlowArtifactStore, evidence FunctionalFlowEvidenceManifestSource, journal ...functionalflow.EventJournal) (*LocalFunctionalFlowPublisher, error) {
	absolute, err := filepath.Abs(root)
	if err != nil || root == "" || artifacts == nil {
		return nil, &Error{Code: CodeInvalid, Operation: "functional_flow_publisher"}
	}
	if err = os.MkdirAll(absolute, 0o700); err != nil {
		return nil, &Error{Code: CodeUnavailable, Operation: "functional_flow_publisher"}
	}
	var causal functionalflow.EventJournal
	if len(journal) > 0 {
		causal = journal[0]
	}
	return &LocalFunctionalFlowPublisher{root: absolute, Artifacts: artifacts, Evidence: evidence, Journal: causal}, nil
}

// Stage persists immutable primary proof while deliberately omitting cleanup and public package completion.
func (p *LocalFunctionalFlowPublisher) Stage(ctx context.Context, handle string, artifact functionalflow.Artifact, result functionalflow.Result) error {
	if ctx.Err() != nil || !safeID.MatchString(handle) || functionalflow.ValidateArtifact(artifact) != nil || result.ArtifactDigest != artifact.ArtifactDigest {
		return &Error{Code: CodeInvalid, Operation: "functional_flow_stage"}
	}
	return writeImmutableJSON(filepath.Join(p.root, handle+".primary.json"), struct {
		Artifact functionalflow.Artifact `json:"artifact"`
		Primary  functionalflow.Result   `json:"primary"`
	}{artifact, result}, "functional_flow_stage")
}

// Finalize seals the same staged primary with the confirmed cleanup summary and converges repeated recovery calls.
func (p *LocalFunctionalFlowPublisher) Finalize(ctx context.Context, handle string, outcome Outcome, summary fixture.FixtureSummary) (PackageProjection, error) {
	if ctx.Err() != nil || !safeID.MatchString(handle) {
		return PackageProjection{}, &Error{Code: CodeInvalid, Operation: "functional_flow_finalize"}
	}
	directory := filepath.Join(p.root, handle)
	destination := filepath.Join(directory, "functional-flow-package.json")
	if raw, err := os.ReadFile(destination); err == nil {
		var pkg functionalflow.Package
		if json.Unmarshal(raw, &pkg) != nil || functionalflow.VerifyPackage(pkg) != nil || workflowOutcome(pkg.Primary.Outcome) != outcome || pkg.Cleanup.Status != summary.Cleanup.Status || pkg.Fixtures.PreparationStatus != summary.PreparationStatus {
			return PackageProjection{}, &Error{Code: CodeConflict, Operation: "functional_flow_package"}
		}
		return PackageProjection{RunID: handle, BindingSHA256: pkg.Artifact.ArtifactDigest, PackageDir: directory, Outcome: outcome, Fixtures: &summary}, nil
	}
	artifact, err := p.Artifacts.Load(ctx, handle)
	if err != nil {
		return PackageProjection{}, err
	}
	if !validFlowPreparationStatus(summary.PreparationStatus) {
		return PackageProjection{}, &Error{Code: CodeConflict, Operation: "functional_flow_preparation"}
	}
	primary, err := p.loadPrimary(handle)
	if errors.Is(err, os.ErrNotExist) {
		if summary.PreparationStatus != fixture.PreparationFailed && summary.PreparationStatus != fixture.PreparationCancelled && summary.PreparationStatus != fixture.PreparationRejected {
			return PackageProjection{}, &Error{Code: CodeUnavailable, Operation: "functional_flow_primary_pending"}
		}
		primary = abortedFunctionalPrimary(handle, artifact, outcome)
	} else if err != nil {
		return PackageProjection{}, &Error{Code: CodeConflict, Operation: "functional_flow_primary"}
	}
	if workflowOutcome(primary.Outcome) != outcome {
		return PackageProjection{}, &Error{Code: CodeConflict, Operation: "functional_flow_primary"}
	}
	if p.Journal == nil {
		return PackageProjection{}, &Error{Code: CodeUnavailable, Operation: "functional_flow_journal"}
	}
	records := []functionalflow.EvidenceRecord{}
	hasEvidence := false
	for _, entry := range primary.Ledger {
		hasEvidence = hasEvidence || len(entry.EvidenceIDs) > 0
	}
	if hasEvidence {
		if p.Evidence == nil {
			return PackageProjection{}, &Error{Code: CodeUnavailable, Operation: "functional_flow_evidence"}
		}
		records, err = p.Evidence.Records(ctx, handle, primary)
		if err != nil {
			return PackageProjection{}, &Error{Code: CodeUnavailable, Operation: "functional_flow_evidence"}
		}
	}
	events, err := p.Journal.Events(ctx, handle)
	if err != nil {
		return PackageProjection{}, &Error{Code: CodeUnavailable, Operation: "functional_flow_journal"}
	}
	if len(events) < 2 || events[1].Type != "fixtures_terminal" || events[1].Status != string(summary.PreparationStatus) {
		return PackageProjection{}, &Error{Code: CodeConflict, Operation: "functional_flow_preparation"}
	}
	events, err = p.Journal.Events(ctx, handle)
	if err != nil {
		return PackageProjection{}, &Error{Code: CodeUnavailable, Operation: "functional_flow_journal"}
	}
	allEvidence := []string{}
	for _, entry := range primary.Ledger {
		allEvidence = append(allEvidence, entry.EvidenceIDs...)
	}
	lastType := events[len(events)-1].Type
	if lastType != "primary_terminal" && lastType != "cleanup_terminal" {
		if _, err = p.Journal.AppendEvent(ctx, functionalflow.Event{Type: "primary_terminal", Outcome: primary.Outcome, EvidenceIDs: allEvidence, RunID: handle, FlowID: artifact.FlowID, Revision: artifact.Revision, ArtifactDigest: artifact.ArtifactDigest}); err != nil {
			return PackageProjection{}, &Error{Code: CodeUnavailable, Operation: "functional_flow_journal"}
		}
	} else {
		primaryIndex := len(events) - 1
		if lastType == "cleanup_terminal" {
			primaryIndex--
		}
		if primaryIndex < 0 || events[primaryIndex].Type != "primary_terminal" || events[primaryIndex].Outcome != primary.Outcome {
			return PackageProjection{}, &Error{Code: CodeConflict, Operation: "functional_flow_journal"}
		}
	}
	if lastType != "cleanup_terminal" {
		if _, err = p.Journal.AppendEvent(ctx, functionalflow.Event{Type: "cleanup_terminal", Status: string(summary.Cleanup.Status), RunID: handle, FlowID: artifact.FlowID, Revision: artifact.Revision, ArtifactDigest: artifact.ArtifactDigest}); err != nil {
			return PackageProjection{}, &Error{Code: CodeUnavailable, Operation: "functional_flow_journal"}
		}
	} else if events[len(events)-1].Status != string(summary.Cleanup.Status) {
		return PackageProjection{}, &Error{Code: CodeConflict, Operation: "functional_flow_journal"}
	}
	events, err = p.Journal.Events(ctx, handle)
	if err != nil {
		return PackageProjection{}, &Error{Code: CodeUnavailable, Operation: "functional_flow_journal"}
	}
	pkg, err := functionalflow.BuildPackage(handle, artifact, records, events, primary, summary, summary.Cleanup)
	if err != nil {
		return PackageProjection{}, &Error{Code: CodeConflict, Operation: "functional_flow_package"}
	}
	if err = os.MkdirAll(directory, 0o700); err != nil {
		return PackageProjection{}, &Error{Code: CodeUnavailable, Operation: "functional_flow_package"}
	}
	if err = writeImmutableJSON(destination, pkg, "functional_flow_package"); err != nil {
		return PackageProjection{}, err
	}
	return PackageProjection{RunID: handle, BindingSHA256: artifact.ArtifactDigest, PackageDir: directory, Outcome: outcome, Fixtures: &summary}, nil
}

// loadPrimary reloads the staged primary while preserving its exact byte-derived semantics.
func (p *LocalFunctionalFlowPublisher) loadPrimary(handle string) (functionalflow.Result, error) {
	raw, err := os.ReadFile(filepath.Join(p.root, handle+".primary.json"))
	if err != nil {
		return functionalflow.Result{}, err
	}
	var staged struct {
		Artifact functionalflow.Artifact `json:"artifact"`
		Primary  functionalflow.Result   `json:"primary"`
	}
	if json.Unmarshal(raw, &staged) != nil || functionalflow.ValidateArtifact(staged.Artifact) != nil || staged.Primary.ArtifactDigest != staged.Artifact.ArtifactDigest {
		return functionalflow.Result{}, &Error{Code: CodeConflict, Operation: "functional_flow_primary"}
	}
	return staged.Primary, nil
}

// abortedFunctionalPrimary records a conservative no-tool primary without inventing step terminals or evidence.
func abortedFunctionalPrimary(runID string, artifact functionalflow.Artifact, outcome Outcome) functionalflow.Result {
	primary := functionalflow.OutcomeInconclusive
	switch outcome {
	case OutcomeCancelled:
		primary = functionalflow.OutcomeCancelled
	case OutcomeError:
		primary = functionalflow.OutcomeError
	}
	return functionalflow.Result{FlowID: artifact.FlowID, Revision: artifact.Revision, Outcome: primary, ArtifactDigest: artifact.ArtifactDigest}
}
