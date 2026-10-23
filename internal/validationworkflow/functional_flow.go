package validationworkflow

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"

	"github.com/asierhr/featureProof/internal/agentplanning"
	"github.com/asierhr/featureProof/internal/fixture"
	"github.com/asierhr/featureProof/internal/functionalflow"
	"github.com/asierhr/featureProof/internal/planapproval"
	"github.com/asierhr/featureProof/internal/planning"
)

// RecoverableFunctionalFlowError marks an interrupted durable execution whose primary result is still pending.
type RecoverableFunctionalFlowError struct{ cause error }

// Error exposes a stable payload-free workflow diagnostic.
func (e *RecoverableFunctionalFlowError) Error() string { return "functional flow execution pending" }

// Unwrap preserves the safe typed runtime category for internal recovery decisions.
func (e *RecoverableFunctionalFlowError) Unwrap() error { return e.cause }

// IsRecoverableFunctionalFlowError reports whether cleanup and packaging must wait for executor recovery.
func IsRecoverableFunctionalFlowError(err error) bool {
	var pending *RecoverableFunctionalFlowError
	return errors.As(err, &pending)
}

// FunctionalFlowExecutionPort extends the existing durable workflow execution phase with canonical functional flows.
type FunctionalFlowExecutionPort interface {
	InspectProvided(context.Context, ExecutionRequest) (functionalflow.Flow, error)
	Admit(context.Context, ExecutionRequest, agentplanning.PreparedSnapshot) (planapproval.FunctionalFlowSnapshot, error)
	RecordPreparation(context.Context, string, fixture.PreparationStatus) (fixture.PreparationStatus, error)
	Execute(context.Context, ExecutionRequest, agentplanning.PreparedSnapshot) (Outcome, error)
	Finalize(context.Context, string, Outcome, fixture.FixtureSummary) (PackageProjection, error)
}

// FunctionalFlowComposer rebuilds and seals one canonical flow from already approved workflow material.
type FunctionalFlowComposer interface {
	Compose(context.Context, ExecutionRequest, agentplanning.PreparedSnapshot) (functionalflow.ValidatedFlow, functionalflow.Artifact, error)
}

// ProvidedFlowCatalog resolves only composition-authorized alias or artifact IDs into bounded canonical documents.
type ProvidedFlowCatalog interface {
	Load(context.Context, FunctionalFlowSelection) ([]byte, error)
}

// CanonicalFunctionalFlowComposer normalizes generated or authorized provided sources through one validation and binding pipeline.
type CanonicalFunctionalFlowComposer struct {
	Limits     functionalflow.Limits
	Validation functionalflow.ValidationAuthority
	Binding    functionalflow.BindingAuthority
	Generated  functionalflow.GeneratedAuthority
	Provided   ProvidedFlowCatalog
}

// Compose produces the exact immutable flow and safe artifact consumed by admission and recovery.
func (c CanonicalFunctionalFlowComposer) Compose(ctx context.Context, request ExecutionRequest, snapshot agentplanning.PreparedSnapshot) (functionalflow.ValidatedFlow, functionalflow.Artifact, error) {
	var flow functionalflow.Flow
	var err error
	if request.FunctionalFlow == nil {
		flow, err = functionalflow.NormalizeGeneratedExecution(snapshot, request.Plans, c.Generated)
	} else {
		if c.Provided == nil {
			return functionalflow.ValidatedFlow{}, functionalflow.Artifact{}, &Error{Code: CodeInvalid, Operation: "functional_flow_catalog"}
		}
		document, loadErr := c.Provided.Load(ctx, *request.FunctionalFlow)
		if loadErr != nil {
			return functionalflow.ValidatedFlow{}, functionalflow.Artifact{}, &Error{Code: CodeUnauthorized, Operation: "functional_flow_catalog"}
		}
		flow, err = functionalflow.DecodeProvided(document, c.Limits)
	}
	if err != nil {
		return functionalflow.ValidatedFlow{}, functionalflow.Artifact{}, err
	}
	validated, err := functionalflow.Validate(flow, c.Limits, c.Validation)
	if err != nil {
		return functionalflow.ValidatedFlow{}, functionalflow.Artifact{}, err
	}
	artifact, err := functionalflow.Bind(validated, c.Binding)
	if err != nil {
		return functionalflow.ValidatedFlow{}, functionalflow.Artifact{}, err
	}
	return validated, artifact, nil
}

// FunctionalFlowExecutor is the consumer-owned deterministic business-step runtime boundary.
type FunctionalFlowExecutor interface {
	Execute(context.Context, string, functionalflow.ValidatedFlow, functionalflow.Artifact) (functionalflow.Result, error)
}

// FunctionalFlowCompletedRecoverer reconstructs a fully journaled primary without re-entering tool execution.
type FunctionalFlowCompletedRecoverer interface {
	RecoverCompleted(context.Context, string, functionalflow.ValidatedFlow, functionalflow.Artifact) (functionalflow.Result, bool, error)
}

// FunctionalFlowArtifactStore owns the exact admitted artifact before fixture preparation and reloads it for execution.
type FunctionalFlowArtifactStore interface {
	Commit(context.Context, string, functionalflow.Artifact) error
	Load(context.Context, string) (functionalflow.Artifact, error)
}

// FunctionalFlowPublisher stages immutable primary proof and seals the owned package only after cleanup.
type FunctionalFlowPublisher interface {
	Stage(context.Context, string, functionalflow.Artifact, functionalflow.Result) error
	Finalize(context.Context, string, Outcome, fixture.FixtureSummary) (PackageProjection, error)
}

// FunctionalFlowExecutionAdapter plugs canonical flow composition and runtime into Service's existing execution phase.
type FunctionalFlowExecutionAdapter struct {
	Composer  FunctionalFlowComposer
	Executor  FunctionalFlowExecutor
	Artifacts FunctionalFlowArtifactStore
	Publisher FunctionalFlowPublisher
	Journal   functionalflow.EventJournal
}

// InspectProvided validates and binds an explicitly selected flow without invoking the planning model.
func (a FunctionalFlowExecutionAdapter) InspectProvided(ctx context.Context, request ExecutionRequest) (functionalflow.Flow, error) {
	if request.FunctionalFlow == nil || a.Composer == nil {
		return functionalflow.Flow{}, &Error{Code: CodeInvalid, Operation: "functional_flow_provided"}
	}
	validated, artifact, err := a.Composer.Compose(ctx, request, agentplanning.PreparedSnapshot{})
	if err != nil || functionalflow.ValidateArtifactAgainst(validated, artifact) != nil {
		return functionalflow.Flow{}, &Error{Code: CodeConflict, Operation: "functional_flow_provided"}
	}
	return validated.Flow(), nil
}

// Admit composes, verifies, and persists the exact flow artifact before any fixture preparation can begin.
func (a FunctionalFlowExecutionAdapter) Admit(ctx context.Context, request ExecutionRequest, snapshot agentplanning.PreparedSnapshot) (planapproval.FunctionalFlowSnapshot, error) {
	if a.Composer == nil || a.Executor == nil || a.Artifacts == nil || a.Publisher == nil || request.Handle == "" {
		return planapproval.FunctionalFlowSnapshot{}, &Error{Code: CodeInvalid, Operation: "functional_flow_composition"}
	}
	validated, artifact, err := a.Composer.Compose(ctx, request, snapshot)
	if err != nil || functionalflow.ValidateArtifactAgainst(validated, artifact) != nil || artifact.FlowID == "" {
		return planapproval.FunctionalFlowSnapshot{}, &Error{Code: CodeConflict, Operation: "functional_flow_artifact"}
	}
	if err = a.Artifacts.Commit(ctx, request.Handle, artifact); err != nil {
		return planapproval.FunctionalFlowSnapshot{}, &Error{Code: CodeUnavailable, Operation: "functional_flow_artifact_commit"}
	}
	if a.Journal != nil {
		if _, err = a.Journal.AppendEvent(ctx, functionalflow.Event{Type: "flow_artifact_committed", RunID: request.Handle, FlowID: artifact.FlowID, Revision: artifact.Revision, ArtifactDigest: artifact.ArtifactDigest}); err != nil {
			return planapproval.FunctionalFlowSnapshot{}, &Error{Code: CodeUnavailable, Operation: "functional_flow_journal"}
		}
	}
	return projectFlowApproval(artifact), nil
}

// RecordPreparation appends the actual fixture admission terminal before any business tool can run.
func (a FunctionalFlowExecutionAdapter) RecordPreparation(ctx context.Context, handle string, status fixture.PreparationStatus) (fixture.PreparationStatus, error) {
	if a.Journal == nil || !validFlowPreparationStatus(status) {
		return "", &Error{Code: CodeInvalid, Operation: "functional_flow_prepare_journal"}
	}
	artifact, err := a.Artifacts.Load(ctx, handle)
	if err != nil {
		return "", &Error{Code: CodeUnavailable, Operation: "functional_flow_prepare_journal"}
	}
	events, err := a.Journal.Events(ctx, handle)
	if err != nil {
		return "", &Error{Code: CodeUnavailable, Operation: "functional_flow_prepare_journal"}
	}
	if len(events) == 0 || events[0].Type != "flow_artifact_committed" || events[0].ArtifactDigest != artifact.ArtifactDigest {
		return "", &Error{Code: CodeConflict, Operation: "functional_flow_prepare_journal"}
	}
	for _, event := range events {
		if event.Type == "fixtures_terminal" {
			actual := fixture.PreparationStatus(event.Status)
			if !validFlowPreparationStatus(actual) {
				return "", &Error{Code: CodeConflict, Operation: "functional_flow_prepare_journal"}
			}
			return actual, nil
		}
	}
	if len(events) != 1 {
		return "", &Error{Code: CodeConflict, Operation: "functional_flow_prepare_journal"}
	}
	_, err = a.Journal.AppendEvent(ctx, functionalflow.Event{Type: "fixtures_terminal", Status: string(status), RunID: handle, FlowID: artifact.FlowID, Revision: artifact.Revision, ArtifactDigest: artifact.ArtifactDigest})
	if err != nil {
		return "", &Error{Code: CodeUnavailable, Operation: "functional_flow_prepare_journal"}
	}
	return status, nil
}

// validFlowPreparationStatus closes the journal vocabulary shared by workflow and package verification.
func validFlowPreparationStatus(status fixture.PreparationStatus) bool {
	switch status {
	case fixture.PreparationPrepared, fixture.PreparationFailed, fixture.PreparationCancelled, fixture.PreparationNotRequired, fixture.PreparationRejected:
		return true
	default:
		return false
	}
}

// projectFlowApproval exposes only the immutable safe authority a person must approve before execution.
func projectFlowApproval(artifact functionalflow.Artifact) planapproval.FunctionalFlowSnapshot {
	projection := planapproval.FunctionalFlowSnapshot{FlowID: artifact.FlowID, Source: string(artifact.Source), Revision: artifact.Revision, ArtifactDigest: artifact.ArtifactDigest, SemanticDigest: artifact.SemanticDigest, Steps: make([]planapproval.FunctionalFlowStepSnapshot, len(artifact.Steps))}
	for i, bound := range artifact.Steps {
		projection.Steps[i] = planapproval.FunctionalFlowStepSnapshot{ID: bound.Step.ID, Kind: string(bound.Step.Kind), ConstraintID: bound.Step.ConstraintID, IdentityID: bound.Step.IdentityID, Capability: bound.Operation.Reference.Capability, TargetID: bound.Operation.Reference.TargetID, Method: bound.Operation.Reference.Method, Path: bound.Operation.Reference.Path, BindingDigest: bound.Operation.BindingDigest, RequirementsDigest: bound.RequirementsDigest, Effectful: bound.Operation.Effectful}
	}
	return projection
}

// Execute reloads the admitted artifact, recomposes its private canonical snapshot, and stages immutable primary proof.
func (a FunctionalFlowExecutionAdapter) Execute(ctx context.Context, request ExecutionRequest, snapshot agentplanning.PreparedSnapshot) (Outcome, error) {
	if a.Composer == nil || a.Executor == nil || a.Artifacts == nil || a.Publisher == nil || request.Handle == "" {
		return "", &Error{Code: CodeInvalid, Operation: "functional_flow_composition"}
	}
	durableCtx := ctx
	if ctx.Err() != nil {
		durableCtx = context.WithoutCancel(ctx)
	}
	validated, artifact, err := a.Composer.Compose(durableCtx, request, snapshot)
	committed, loadErr := a.Artifacts.Load(durableCtx, request.Handle)
	if err != nil || loadErr != nil || functionalflow.ValidateArtifactAgainst(validated, artifact) != nil || committed.ArtifactDigest != artifact.ArtifactDigest || request.FunctionalFlowDigest == "" || request.FunctionalFlowDigest != committed.ArtifactDigest {
		return "", &Error{Code: CodeConflict, Operation: "functional_flow_artifact"}
	}
	lineage, err := functionalflow.NewRevisionSet(artifact, 1)
	if err != nil {
		return "", &Error{Code: CodeConflict, Operation: "functional_flow_revision"}
	}
	var primary functionalflow.Result
	complete := false
	if recoverer, ok := a.Executor.(FunctionalFlowCompletedRecoverer); ok {
		primary, complete, err = recoverer.RecoverCompleted(durableCtx, request.Handle, validated, artifact)
		if err != nil {
			return "", &RecoverableFunctionalFlowError{cause: err}
		}
	}
	if !complete {
		primary, err = a.Executor.Execute(ctx, request.Handle, validated, artifact)
	}
	if err != nil {
		if recoverableFunctionalRuntimeError(err) {
			return "", &RecoverableFunctionalFlowError{cause: err}
		}
		primary = terminalFunctionalFlowFailure(request.Handle, artifact)
		if journalErr := appendTerminalFailureEvents(durableCtx, a.Journal, artifact, primary); journalErr != nil {
			return "", &RecoverableFunctionalFlowError{cause: journalErr}
		}
	}
	if _, err = lineage.Confirm(primary); err != nil {
		return "", &Error{Code: CodeConflict, Operation: "functional_flow_revision"}
	}
	if err = a.Publisher.Stage(durableCtx, request.Handle, artifact, primary); err != nil {
		return "", &Error{Code: CodeUnavailable, Operation: "functional_flow_publish"}
	}
	return workflowOutcome(primary.Outcome), nil
}

// recoverableFunctionalRuntimeError keeps durable post-tool checkpoints resumable while rejecting contract corruption.
func recoverableFunctionalRuntimeError(err error) bool {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return true
	}
	var flowErr *functionalflow.Error
	if !errors.As(err, &flowErr) {
		return false
	}
	switch flowErr.Category {
	case functionalflow.CategoryUncertain, functionalflow.CategoryCancelled, functionalflow.CategoryInternal:
		return true
	case functionalflow.CategoryAuthority:
		return flowErr.Path == "state.claim_renew" || flowErr.Path == "state.claim"
	case functionalflow.CategoryIntegrity:
		return flowErr.Path == "evidence" || flowErr.Path == "assertions" || strings.HasPrefix(flowErr.Path, "state.") || strings.HasPrefix(flowErr.Path, "intent.") || strings.HasPrefix(flowErr.Path, "journal.")
	default:
		return false
	}
}

// terminalFunctionalFlowFailure builds an explicit evidence-free primary for an unrecoverable runtime failure.
func terminalFunctionalFlowFailure(runID string, artifact functionalflow.Artifact) functionalflow.Result {
	flow := functionalflow.Flow{Version: functionalflow.VersionV1, ID: artifact.FlowID, Revision: artifact.Revision, RequiredEvidence: append([]string(nil), artifact.RequiredEvidence...), Steps: make([]functionalflow.Step, len(artifact.Steps))}
	records := make([]functionalflow.StepRecord, len(artifact.Steps))
	ledger := functionalflow.Ledger{}
	comparisons := []functionalflow.ComparisonResult{}
	for index, bound := range artifact.Steps {
		flow.Steps[index] = bound.Step
		records[index] = functionalflow.StepRecord{RunID: runID, Revision: artifact.Revision, StepID: bound.Step.ID, Status: planning.StepInconclusive, Category: functionalflow.CategoryIntegrity}
		for _, assertion := range bound.Step.Assertions {
			result := planning.AssertionResult{DeclarationID: assertion.ID, NodePath: assertion.ID, Status: planning.AssertionInconclusive}
			ledger, _ = ledger.Append(functionalflow.LedgerEntry{Revision: artifact.Revision, StepID: bound.Step.ID, ConstraintID: bound.Step.ConstraintID, Result: result})
			records[index].Assertions = append(records[index].Assertions, result)
		}
		for _, comparison := range bound.Step.Comparisons {
			result := planning.AssertionResult{DeclarationID: comparison.ID, NodePath: comparison.ID, Operator: string(comparison.Operator), Status: planning.AssertionInconclusive}
			ledger, _ = ledger.Append(functionalflow.LedgerEntry{Revision: artifact.Revision, StepID: bound.Step.ID, ConstraintID: bound.Step.ConstraintID, Result: result})
			comparisons = append(comparisons, functionalflow.ComparisonResult{StepID: bound.Step.ID, ID: comparison.ID, Status: planning.AssertionInconclusive})
		}
	}
	constraints, outcome := ledger.Aggregate(flow, records, false)
	return functionalflow.Result{FlowID: artifact.FlowID, Revision: artifact.Revision, Steps: records, Ledger: ledger.Entries(), Comparisons: comparisons, Constraints: constraints, Outcome: outcome, ArtifactDigest: artifact.ArtifactDigest}
}

// appendTerminalFailureEvents records actual no-evidence step terminals before an unrecoverable primary is staged.
func appendTerminalFailureEvents(ctx context.Context, journal functionalflow.EventJournal, artifact functionalflow.Artifact, result functionalflow.Result) error {
	if journal == nil {
		return nil
	}
	for _, record := range result.Steps {
		if _, err := journal.AppendEvent(ctx, functionalflow.Event{Type: "step_terminal", StepID: record.StepID, Status: string(record.Status), Category: record.Category, RunID: record.RunID, FlowID: artifact.FlowID, Revision: artifact.Revision, ArtifactDigest: artifact.ArtifactDigest}); err != nil {
			return err
		}
	}
	return nil
}

// Finalize seals one idempotent final package after cleanup and preserves the previously staged primary outcome.
func (a FunctionalFlowExecutionAdapter) Finalize(ctx context.Context, handle string, outcome Outcome, summary fixture.FixtureSummary) (PackageProjection, error) {
	if a.Publisher == nil {
		return PackageProjection{}, &Error{Code: CodeInvalid, Operation: "functional_flow_finalize"}
	}
	projection, err := a.Publisher.Finalize(ctx, handle, outcome, summary)
	if err != nil || projection.Outcome != outcome {
		return PackageProjection{}, &Error{Code: CodeUnavailable, Operation: "functional_flow_finalize"}
	}
	return projection, nil
}

// workflowOutcome maps the functional-flow primary vocabulary without making it more favorable.
func workflowOutcome(outcome functionalflow.PrimaryOutcome) Outcome {
	switch outcome {
	case functionalflow.OutcomePassed:
		return OutcomePassed
	case functionalflow.OutcomeFailed:
		return OutcomeFailed
	case functionalflow.OutcomeCancelled:
		return OutcomeCancelled
	case functionalflow.OutcomeError:
		return OutcomeError
	default:
		return OutcomeInconclusive
	}
}

// FunctionalFlowProjection is the common allowlisted result shape returned by every external adapter.
type FunctionalFlowProjection struct {
	Primary functionalflow.Result `json:"primary"`
	Cleanup fixture.CleanupReport `json:"cleanup"`
}

// FunctionalFlowAPI projects verified flow results for the in-process API surface.
type FunctionalFlowAPI struct{}

// Result projects one verified package through the API allowlist.
func (FunctionalFlowAPI) Result(pkg functionalflow.Package) (FunctionalFlowProjection, error) {
	return ProjectFunctionalFlow(pkg)
}

// FunctionalFlowResult projects one verified package through the local CLI allowlist.
func (c CLI) FunctionalFlowResult(pkg functionalflow.Package) (FunctionalFlowProjection, error) {
	return ProjectFunctionalFlow(pkg)
}

// FunctionalFlowResult projects one verified package through the MCP allowlist.
func (m MCPServer) FunctionalFlowResult(pkg functionalflow.Package) (FunctionalFlowProjection, error) {
	return ProjectFunctionalFlow(pkg)
}

// FunctionalFlowResult projects one verified package through the GitHub allowlist.
func (g GitHubGateway) FunctionalFlowResult(pkg functionalflow.Package) (FunctionalFlowProjection, error) {
	return ProjectFunctionalFlow(pkg)
}

// RemoteFlowSelection is the closed alias-or-artifact choice accepted by API, MCP, and GitHub.
type RemoteFlowSelection struct {
	Alias      string `json:"alias,omitempty"`
	ArtifactID string `json:"artifact_id,omitempty"`
}

// DecodeRemoteFlowSelection strictly rejects inline manifests, unknown fields, trailing JSON, and oversized input.
func DecodeRemoteFlowSelection(document []byte, maxBytes int) (RemoteFlowSelection, error) {
	if maxBytes <= 0 || len(document) == 0 || len(document) > maxBytes || !uniqueRemoteSelectionKeys(document) {
		return RemoteFlowSelection{}, &Error{Code: CodeInvalid, Operation: "functional_flow_selection"}
	}
	decoder := json.NewDecoder(bytes.NewReader(document))
	decoder.DisallowUnknownFields()
	var selection RemoteFlowSelection
	if err := decoder.Decode(&selection); err != nil {
		return RemoteFlowSelection{}, &Error{Code: CodeInvalid, Operation: "functional_flow_selection"}
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) || (selection.Alias == "") == (selection.ArtifactID == "") {
		return RemoteFlowSelection{}, &Error{Code: CodeInvalid, Operation: "functional_flow_selection"}
	}
	return selection, nil
}

// uniqueRemoteSelectionKeys rejects duplicate top-level authority selectors before struct decoding.
func uniqueRemoteSelectionKeys(document []byte) bool {
	decoder := json.NewDecoder(bytes.NewReader(document))
	token, err := decoder.Token()
	if err != nil || token != json.Delim('{') {
		return false
	}
	seen := map[string]bool{}
	for decoder.More() {
		token, err = decoder.Token()
		key, ok := token.(string)
		if err != nil || !ok || seen[key] {
			return false
		}
		seen[key] = true
		var value json.RawMessage
		if decoder.Decode(&value) != nil {
			return false
		}
	}
	token, err = decoder.Token()
	return err == nil && token == json.Delim('}')
}

// ProjectFunctionalFlow verifies the package then returns the shared safe primary and cleanup view.
func ProjectFunctionalFlow(pkg functionalflow.Package) (FunctionalFlowProjection, error) {
	primary, cleanup, err := functionalflow.SafeProjection(pkg)
	if err != nil {
		return FunctionalFlowProjection{}, err
	}
	return FunctionalFlowProjection{Primary: primary, Cleanup: cleanup}, nil
}
