package validationworkflow

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/asierhr/featureProof/internal/agentplanning"
	"github.com/asierhr/featureProof/internal/evidence"
	"github.com/asierhr/featureProof/internal/fixture"
	"github.com/asierhr/featureProof/internal/functionalflow"
	"github.com/asierhr/featureProof/internal/githubpr"
	"github.com/asierhr/featureProof/internal/harness"
	"github.com/asierhr/featureProof/internal/planapproval"
	"github.com/asierhr/featureProof/internal/planning"
	"github.com/asierhr/featureProof/internal/policy"
	"github.com/asierhr/featureProof/internal/projectmemory"
	"github.com/asierhr/featureProof/internal/testidentity"
	"github.com/asierhr/featureProof/internal/tools"
)

// workflowFlowFixture creates a minimal valid immutable flow and exact binding artifact.
func workflowFlowFixture(t *testing.T) (functionalflow.ValidatedFlow, functionalflow.Artifact) {
	t.Helper()
	digest := strings.Repeat("a", 64)
	literal := planning.LiteralValue{Value: "1"}
	operation := functionalflow.OperationReference{Capability: "http.request", TargetID: "preview", Method: "GET", Path: "/health", ContractDigest: digest}
	flow := functionalflow.Flow{Version: functionalflow.VersionV1, ID: "flow", Source: functionalflow.SourceGenerated, Revision: 1, ContextDigest: digest, RequiredEvidence: []string{"a"}, Steps: []functionalflow.Step{{ID: "observe", Kind: functionalflow.StepObserve, ConstraintID: "c", DependsOn: []string{}, Operation: operation, IdentityID: "reader", Input: map[string]functionalflow.InputValue{"id": {Literal: &literal}}, Assertions: []planning.AssertionDeclaration{{ID: "a", Definition: map[string]any{"operator": "exists"}}}, Comparisons: []functionalflow.ObservationComparison{}, RequiredEvidence: []string{"a"}}}}
	limits := functionalflow.Limits{MaxDocumentBytes: 1024, MaxSteps: 3, MaxAssertions: 3, MaxComparisons: 3, MaxReferences: 3, MaxDepth: 3, MaxResolvedBytes: 1024, MaxToolCalls: 3, MaxRetries: 1, MaxWork: 30, MaxRevisions: 2, StepTimeout: time.Second, RunTimeout: time.Second}
	validated, err := functionalflow.Validate(flow, limits, functionalflow.ValidationAuthority{Constraints: map[string]bool{"c": true}, FixtureCaptures: map[string]planning.ValueType{}})
	if err != nil {
		t.Fatal(err)
	}
	artifact, err := functionalflow.Bind(validated, functionalflow.BindingAuthority{Operations: []functionalflow.OperationAuthority{{Reference: operation}}, Identities: []testidentity.Descriptor{{ID: "reader", Mechanism: testidentity.MechanismBearer, Operations: []testidentity.Operation{{Capability: operation.Capability, TargetID: operation.TargetID, Method: operation.Method, Path: operation.Path}}}}, Bindings: functionalflow.ArtifactBindings{Specification: digest, Revision: digest, Target: digest, Contract: digest, Identities: digest, Policy: digest, Fixtures: digest, Capabilities: digest, Limits: digest}})
	if err != nil {
		t.Fatal(err)
	}
	return validated, artifact
}

// TestLocalProvidedFlowCatalogRestrictsAndAuthenticatesDocuments proves local paths become immutable safe IDs and remote selectors cannot escape the catalog.
func TestLocalProvidedFlowCatalogRestrictsAndAuthenticatesDocuments(t *testing.T) {
	validated, _ := workflowFlowFixture(t)
	flow := validated.Flow()
	flow.Source = functionalflow.SourceProvided
	allowed := t.TempDir()
	manifest := filepath.Join(allowed, "flow.json")
	raw, _ := json.Marshal(flow)
	if err := os.WriteFile(manifest, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	limits := functionalflow.Limits{MaxDocumentBytes: 1 << 20}
	catalog, err := OpenLocalProvidedFlowCatalog(t.TempDir(), []string{allowed}, nil, limits)
	if err != nil {
		t.Fatal(err)
	}
	artifactID, err := catalog.Resolve(context.Background(), manifest)
	if err != nil || len(artifactID) != 64 {
		t.Fatalf("id=%q err=%v", artifactID, err)
	}
	document, err := catalog.Load(context.Background(), FunctionalFlowSelection{ArtifactID: artifactID})
	if err != nil {
		t.Fatal(err)
	}
	loaded, err := functionalflow.DecodeProvided(document, limits)
	if err != nil || loaded.ID != flow.ID {
		t.Fatalf("loaded=%#v err=%v", loaded, err)
	}
	aliasCatalog, err := OpenLocalProvidedFlowCatalog(catalog.root, []string{allowed}, map[string]string{"approved": artifactID}, limits)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = aliasCatalog.Load(context.Background(), FunctionalFlowSelection{Alias: "approved"}); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(t.TempDir(), "outside.json")
	_ = os.WriteFile(outside, raw, 0o600)
	if _, err = catalog.Resolve(context.Background(), outside); !IsCode(err, CodeUnauthorized) {
		t.Fatalf("outside err=%v", err)
	}
	stored := filepath.Join(catalog.root, artifactID+".json")
	if err = os.WriteFile(stored, []byte(`{}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err = catalog.Load(context.Background(), FunctionalFlowSelection{ArtifactID: artifactID}); !IsCode(err, CodeConflict) {
		t.Fatalf("tamper err=%v", err)
	}
}

// TestLocalFunctionalFlowArtifactsAndPackageConvergeAfterCleanup proves concurrent admission and recovery preserve one artifact, primary, and final cleanup package.
func TestLocalFunctionalFlowArtifactsAndPackageConvergeAfterCleanup(t *testing.T) {
	validated, artifact := workflowFlowFixture(t)
	artifacts, err := OpenLocalFunctionalFlowArtifactStore(filepath.Join(t.TempDir(), "artifacts"))
	if err != nil {
		t.Fatal(err)
	}
	var group sync.WaitGroup
	errorsSeen := make(chan error, 2)
	for range 2 {
		group.Add(1)
		go func() {
			defer group.Done()
			errorsSeen <- artifacts.Commit(context.Background(), "run", artifact)
		}()
	}
	group.Wait()
	close(errorsSeen)
	for commitErr := range errorsSeen {
		if commitErr != nil {
			t.Fatal(commitErr)
		}
	}
	loaded, err := artifacts.Load(context.Background(), "run")
	if err != nil || loaded.ArtifactDigest != artifact.ArtifactDigest {
		t.Fatalf("loaded=%#v err=%v", loaded, err)
	}
	steps := []functionalflow.StepRecord{{RunID: "run", Revision: 1, StepID: "observe", Status: planning.StepInconclusive, Category: functionalflow.CategoryUncertain}}
	constraints, outcome := (functionalflow.Ledger{}).Aggregate(validated.Flow(), steps, false)
	primary := functionalflow.Result{FlowID: artifact.FlowID, Revision: artifact.Revision, Steps: steps, Constraints: constraints, Outcome: outcome, ArtifactDigest: artifact.ArtifactDigest}
	causalJournal, _ := functionalflow.NewLocalStore(filepath.Join(t.TempDir(), "journal"), planningClock{}, time.Hour)
	publisher, err := OpenLocalFunctionalFlowPublisher(filepath.Join(t.TempDir(), "packages"), artifacts, nil, causalJournal)
	if err != nil {
		t.Fatal(err)
	}
	if err = publisher.Stage(context.Background(), "run", artifact, primary); err != nil {
		t.Fatal(err)
	}
	appendFlowTestEvent(t, causalJournal, "run", artifact, functionalflow.Event{Type: "flow_artifact_committed"})
	appendFlowTestEvent(t, causalJournal, "run", artifact, functionalflow.Event{Type: "fixtures_terminal", Status: string(fixture.PreparationPrepared)})
	appendFlowTestEvent(t, causalJournal, "run", artifact, functionalflow.Event{Type: "step_terminal", StepID: "observe", Status: string(planning.StepInconclusive), Category: functionalflow.CategoryUncertain})
	summary := fixture.FixtureSummary{PreparationStatus: fixture.PreparationPrepared, Cleanup: fixture.CleanupReport{Status: fixture.CleanupPartial}}
	first, err := publisher.Finalize(context.Background(), "run", OutcomeInconclusive, summary)
	if err != nil {
		t.Fatal(err)
	}
	second, err := publisher.Finalize(context.Background(), "run", OutcomeInconclusive, summary)
	if err != nil || first.PackageDir != second.PackageDir {
		t.Fatalf("first=%#v second=%#v err=%v", first, second, err)
	}
	packagePath := filepath.Join(first.PackageDir, "functional-flow-package.json")
	if err = os.Remove(packagePath); err != nil {
		t.Fatal(err)
	}
	if _, err = publisher.Finalize(context.Background(), "run", OutcomeInconclusive, summary); err != nil {
		t.Fatalf("journal-backed package recovery failed: %v", err)
	}
	packageRaw, err := os.ReadFile(packagePath)
	if err != nil {
		t.Fatal(err)
	}
	importRaw, importErr := ExportPackageImportSnapshot(context.Background(), filepath.Dir(first.PackageDir))
	var imported PackageImportSnapshot
	if importErr != nil || json.Unmarshal(importRaw, &imported) != nil || len(imported.Items) != 1 || imported.Items[0].RunID != "run" || imported.Items[0].ArtifactDigest != artifact.ArtifactDigest {
		t.Fatalf("package import=%#v err=%v", imported, importErr)
	}
	var pkg functionalflow.Package
	if json.Unmarshal(packageRaw, &pkg) != nil || functionalflow.VerifyPackage(pkg) != nil || pkg.Cleanup.Status != fixture.CleanupPartial || pkg.Primary.Outcome != functionalflow.OutcomeInconclusive {
		t.Fatalf("invalid package %#v", pkg)
	}
	changed := summary
	changed.Cleanup.Status = fixture.CleanupSucceeded
	if _, err = publisher.Finalize(context.Background(), "run", OutcomeInconclusive, changed); !IsCode(err, CodeConflict) {
		t.Fatalf("cleanup rewrite err=%v", err)
	}
	changed = summary
	changed.PreparationStatus = fixture.PreparationRejected
	if _, err = publisher.Finalize(context.Background(), "run", OutcomeInconclusive, changed); !IsCode(err, CodeConflict) {
		t.Fatalf("preparation rewrite err=%v", err)
	}
	cancelledContext, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err = publisher.Finalize(cancelledContext, "run", OutcomeInconclusive, summary); err == nil {
		t.Fatal("cancelled finalization was accepted")
	}
	if err = artifacts.Commit(context.Background(), "missing-preparation", artifact); err != nil {
		t.Fatal(err)
	}
	appendFlowTestEvent(t, causalJournal, "missing-preparation", artifact, functionalflow.Event{Type: "flow_artifact_committed"})
	missingSummary := fixture.FixtureSummary{PreparationStatus: fixture.PreparationRejected, Cleanup: fixture.CleanupReport{Status: fixture.CleanupNotRequired}}
	if _, err = publisher.Finalize(context.Background(), "missing-preparation", OutcomePassed, missingSummary); !IsCode(err, CodeConflict) {
		t.Fatalf("unstaged passed outcome err=%v", err)
	}
	if _, err = publisher.Finalize(context.Background(), "missing-preparation", OutcomeInconclusive, fixture.FixtureSummary{Cleanup: fixture.CleanupReport{Status: fixture.CleanupNotRequired}}); !IsCode(err, CodeConflict) {
		t.Fatalf("open preparation status err=%v", err)
	}
	if _, err = publisher.Finalize(context.Background(), "missing-preparation", OutcomeInconclusive, missingSummary); !IsCode(err, CodeConflict) {
		t.Fatalf("missing preparation err=%v", err)
	}
	if err = artifacts.Commit(context.Background(), "prepared-primary-pending", artifact); err != nil {
		t.Fatal(err)
	}
	appendFlowTestEvent(t, causalJournal, "prepared-primary-pending", artifact, functionalflow.Event{Type: "flow_artifact_committed"})
	appendFlowTestEvent(t, causalJournal, "prepared-primary-pending", artifact, functionalflow.Event{Type: "fixtures_terminal", Status: string(fixture.PreparationPrepared)})
	if _, err = publisher.Finalize(context.Background(), "prepared-primary-pending", OutcomeInconclusive, fixture.FixtureSummary{PreparationStatus: fixture.PreparationPrepared, Cleanup: fixture.CleanupReport{Status: fixture.CleanupNotRequired}}); !IsCode(err, CodeUnavailable) {
		t.Fatalf("prepared run without primary was finalized: %v", err)
	}
	if err = artifacts.Commit(context.Background(), "primary-recovery", artifact); err != nil {
		t.Fatal(err)
	}
	appendFlowTestEvent(t, causalJournal, "primary-recovery", artifact, functionalflow.Event{Type: "flow_artifact_committed"})
	appendFlowTestEvent(t, causalJournal, "primary-recovery", artifact, functionalflow.Event{Type: "fixtures_terminal", Status: "rejected"})
	appendFlowTestEvent(t, causalJournal, "primary-recovery", artifact, functionalflow.Event{Type: "primary_terminal", Outcome: functionalflow.OutcomeInconclusive})
	if recovered, recoverErr := publisher.Finalize(context.Background(), "primary-recovery", OutcomeInconclusive, fixture.FixtureSummary{PreparationStatus: fixture.PreparationRejected, Cleanup: fixture.CleanupReport{Status: fixture.CleanupNotRequired}}); recoverErr != nil || recovered.Outcome != OutcomeInconclusive {
		t.Fatalf("primary-boundary recovery=%#v err=%v", recovered, recoverErr)
	}
}

// flowComposerFake supplies exact approved material to the execution adapter.
type flowComposerFake struct {
	validated functionalflow.ValidatedFlow
	artifact  functionalflow.Artifact
	calls     int
}

// Compose returns immutable test authority and records composition count.
func (f *flowComposerFake) Compose(context.Context, ExecutionRequest, agentplanning.PreparedSnapshot) (functionalflow.ValidatedFlow, functionalflow.Artifact, error) {
	f.calls++
	return f.validated, f.artifact, nil
}

// flowExecutorFake returns one deterministic primary result and records business execution count.
type flowExecutorFake struct {
	calls  int
	result functionalflow.Result
}

// recoveringFlowExecutor injects one runtime boundary failure before returning a reconstructible primary without tool replay.
type recoveringFlowExecutor struct {
	mu      sync.Mutex
	calls   int
	effects int
	failure error
	journal functionalflow.EventJournal
}

// checkpointFlowTool is the controlled business boundary used to prove that primary promotion never replays I/O.
type checkpointFlowTool struct {
	mu    sync.Mutex
	calls int
}

// Execute returns one stable confirmed observation and counts actual business I/O crossings.
func (t *checkpointFlowTool) Execute(context.Context, functionalflow.ExecutionRequest) (functionalflow.ExecutionResponse, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.calls++
	return functionalflow.ExecutionResponse{Output: map[string]any{"status": 200}, EvidenceID: "checkpoint-evidence", ResourceBindingDigest: strings.Repeat("c", 64)}, nil
}

// callCount returns the synchronized number of business I/O crossings.
func (t *checkpointFlowTool) callCount() int {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.calls
}

// checkpointFlowPolicy authorizes the already-bound operation in the real executor crash-window tests.
type checkpointFlowPolicy struct{}

// Evaluate allows only the sealed step supplied by the executor.
func (checkpointFlowPolicy) Evaluate(context.Context, string, functionalflow.BoundStep) (functionalflow.PolicyDecision, error) {
	return functionalflow.PolicyAllowed, nil
}

// checkpointFlowRevalidator confirms the immutable binding at the controlled runtime boundary.
type checkpointFlowRevalidator struct{}

// Revalidate accepts the pre-sealed test binding without contacting an external identity provider.
func (checkpointFlowRevalidator) Revalidate(context.Context, string, functionalflow.BoundStep) error {
	return nil
}

// checkpointAssertionEvaluator deterministically confirms every declared assertion from sealed evidence.
type checkpointAssertionEvaluator struct{}

// Evaluate returns one passed result per declaration without retaining the private evidence output.
func (checkpointAssertionEvaluator) Evaluate(_ context.Context, declarations []planning.AssertionDeclaration, _ functionalflow.Evidence) ([]planning.AssertionResult, error) {
	results := make([]planning.AssertionResult, len(declarations))
	for index, declaration := range declarations {
		results[index] = planning.AssertionResult{DeclarationID: declaration.ID, NodePath: declaration.ID, Status: planning.AssertionPassed}
	}
	return results, nil
}

// checkpointExecutorProbe delegates to the real executor while distinguishing execution from completed-primary recovery.
type checkpointExecutorProbe struct {
	inner                    *functionalflow.Executor
	mu                       sync.Mutex
	executeCalls, recoveries int
}

// Execute records actual scheduler entry and delegates every runtime boundary to the real executor.
func (p *checkpointExecutorProbe) Execute(ctx context.Context, runID string, flow functionalflow.ValidatedFlow, artifact functionalflow.Artifact) (functionalflow.Result, error) {
	p.mu.Lock()
	p.executeCalls++
	p.mu.Unlock()
	return p.inner.Execute(ctx, runID, flow, artifact)
}

// RecoverCompleted records checkpoint inspection and delegates reconstruction to the real executor.
func (p *checkpointExecutorProbe) RecoverCompleted(ctx context.Context, runID string, flow functionalflow.ValidatedFlow, artifact functionalflow.Artifact) (functionalflow.Result, bool, error) {
	p.mu.Lock()
	p.recoveries++
	p.mu.Unlock()
	return p.inner.RecoverCompleted(ctx, runID, flow, artifact)
}

// counts returns scheduler and checkpoint-inspection counts atomically.
func (p *checkpointExecutorProbe) counts() (int, int) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.executeCalls, p.recoveries
}

// crashStagePublisher injects one crash immediately before or after the real immutable Stage boundary.
type crashStagePublisher struct {
	inner            *LocalFunctionalFlowPublisher
	mode             string
	mu               sync.Mutex
	fired            bool
	attempts, writes int
}

// Stage crashes once at the selected boundary and otherwise delegates idempotently to the real publisher.
func (p *crashStagePublisher) Stage(ctx context.Context, handle string, artifact functionalflow.Artifact, result functionalflow.Result) error {
	p.mu.Lock()
	p.attempts++
	crash := !p.fired && (p.mode == "before" || p.mode == "after")
	if crash {
		p.fired = true
	}
	mode := p.mode
	p.mu.Unlock()
	if crash && mode == "before" {
		return errors.New("crash before stage")
	}
	if err := p.inner.Stage(ctx, handle, artifact, result); err != nil {
		return err
	}
	p.mu.Lock()
	p.writes++
	p.mu.Unlock()
	if crash && mode == "after" {
		return errors.New("crash after stage")
	}
	return nil
}

// counts returns attempted and durable primary publications under the publisher lock.
func (p *crashStagePublisher) counts() (int, int) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.attempts, p.writes
}

// Finalize delegates post-cleanup package sealing to the real publisher.
func (p *crashStagePublisher) Finalize(ctx context.Context, handle string, outcome Outcome, summary fixture.FixtureSummary) (PackageProjection, error) {
	return p.inner.Finalize(ctx, handle, outcome, summary)
}

// faultEventJournal injects one append interruption while preserving the authenticated LocalStore as journal authority.
type faultEventJournal struct {
	inner    functionalflow.EventJournal
	failType string
	mu       sync.Mutex
	fired    bool
}

// droppingTerminalState simulates a crash where the authenticated terminal journal survives but the state projection does not.
type droppingTerminalState struct {
	inner      *functionalflow.LocalStore
	mu         sync.Mutex
	drop       bool
	reconciles int
}

// Claim delegates exclusive ownership to the production LocalStore.
func (s *droppingTerminalState) Claim(ctx context.Context, runID string, revision uint64, stepID, owner string) (functionalflow.Claim, bool, error) {
	return s.inner.Claim(ctx, runID, revision, stepID, owner)
}

// RenewClaim delegates durable lease renewal to the production LocalStore.
func (s *droppingTerminalState) RenewClaim(ctx context.Context, runID string, revision uint64, stepID string, claim functionalflow.Claim) (functionalflow.Claim, error) {
	return s.inner.RenewClaim(ctx, runID, revision, stepID, claim)
}

// ConfirmIntent delegates effect checkpoints to the production LocalStore.
func (s *droppingTerminalState) ConfirmIntent(ctx context.Context, intent functionalflow.Intent) error {
	return s.inner.ConfirmIntent(ctx, intent)
}

// Intent delegates immutable intent lookup to the production LocalStore.
func (s *droppingTerminalState) Intent(ctx context.Context, runID string, revision uint64, stepID string) (functionalflow.Intent, bool, error) {
	return s.inner.Intent(ctx, runID, revision, stepID)
}

// ConfirmTerminal acknowledges one injected lost-state write while leaving the real journal path intact.
func (s *droppingTerminalState) ConfirmTerminal(ctx context.Context, record functionalflow.StepRecord) error {
	s.mu.Lock()
	drop := s.drop
	s.mu.Unlock()
	if drop {
		return nil
	}
	return s.inner.ConfirmTerminal(ctx, record)
}

// Terminal reads the production LocalStore projection.
func (s *droppingTerminalState) Terminal(ctx context.Context, runID string, revision uint64, stepID string) (functionalflow.StepRecord, bool, error) {
	return s.inner.Terminal(ctx, runID, revision, stepID)
}

// ReconcileTerminal counts and performs the authenticated CAS promotion into LocalStore.
func (s *droppingTerminalState) ReconcileTerminal(ctx context.Context, record functionalflow.StepRecord) error {
	s.mu.Lock()
	s.reconciles++
	s.drop = false
	s.mu.Unlock()
	return s.inner.ReconcileTerminal(ctx, record)
}

// reconcileCount returns the number of authenticated state promotions.
func (s *droppingTerminalState) reconcileCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.reconciles
}

// AppendEvent fails once on the selected event and delegates every other authenticated append.
func (j *faultEventJournal) AppendEvent(ctx context.Context, event functionalflow.Event) (functionalflow.Event, error) {
	j.mu.Lock()
	fail := !j.fired && event.Type == j.failType
	if fail {
		j.fired = true
	}
	j.mu.Unlock()
	if fail {
		return functionalflow.Event{}, errors.New("injected journal crash")
	}
	return j.inner.AppendEvent(ctx, event)
}

// Events returns the same authenticated history owned by the underlying LocalStore.
func (j *faultEventJournal) Events(ctx context.Context, runID string) ([]functionalflow.Event, error) {
	return j.inner.Events(ctx, runID)
}

// Execute fails once at the requested durable boundary and then reconstructs one evidence-free terminal result.
func (f *recoveringFlowExecutor) Execute(ctx context.Context, runID string, validated functionalflow.ValidatedFlow, artifact functionalflow.Artifact) (functionalflow.Result, error) {
	f.mu.Lock()
	f.calls++
	call := f.calls
	f.mu.Unlock()
	if call == 1 && f.failure != nil {
		var flowErr *functionalflow.Error
		if errors.As(f.failure, &flowErr) && flowErr.Path != "state.claim" && flowErr.Path != "intent.confirm" {
			f.mu.Lock()
			f.effects++
			f.mu.Unlock()
		}
		return functionalflow.Result{}, f.failure
	}
	steps := make([]functionalflow.StepRecord, len(artifact.Steps))
	for index, bound := range artifact.Steps {
		steps[index] = functionalflow.StepRecord{RunID: runID, Revision: artifact.Revision, StepID: bound.Step.ID, Status: planning.StepInconclusive, Category: functionalflow.CategoryUncertain}
		if f.journal != nil {
			if _, err := f.journal.AppendEvent(context.WithoutCancel(ctx), functionalflow.Event{Type: "step_terminal", StepID: bound.Step.ID, Status: string(planning.StepInconclusive), Category: functionalflow.CategoryUncertain, RunID: runID, FlowID: artifact.FlowID, Revision: artifact.Revision, ArtifactDigest: artifact.ArtifactDigest}); err != nil {
				return functionalflow.Result{}, err
			}
		}
	}
	constraints, outcome := (functionalflow.Ledger{}).Aggregate(validated.Flow(), steps, ctx.Err() != nil)
	return functionalflow.Result{FlowID: artifact.FlowID, Revision: artifact.Revision, Steps: steps, Constraints: constraints, Outcome: outcome, ArtifactDigest: artifact.ArtifactDigest}, nil
}

// callCount returns the synchronized number of executor resumptions.
func (f *recoveringFlowExecutor) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

// effectCount returns the number of simulated I/O crossings across all redeliveries.
func (f *recoveringFlowExecutor) effectCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.effects
}

// Execute simulates deterministic functional execution after the shared workflow prepared fixtures.
func (f *flowExecutorFake) Execute(_ context.Context, _ string, _ functionalflow.ValidatedFlow, artifact functionalflow.Artifact) (functionalflow.Result, error) {
	f.calls++
	result := f.result
	result.FlowID, result.Revision, result.ArtifactDigest = artifact.FlowID, artifact.Revision, artifact.ArtifactDigest
	return result, nil
}

// flowPublisherFake records the primary result location consumed by the existing package reader.
type flowPublisherFake struct {
	stageCalls, finalizeCalls int
	location                  string
}

// Stage records immutable primary proof without publishing a package before cleanup.
func (f *flowPublisherFake) Stage(context.Context, string, functionalflow.Artifact, functionalflow.Result) error {
	f.stageCalls++
	return nil
}

// Finalize returns one safe package projection after cleanup.
func (f *flowPublisherFake) Finalize(_ context.Context, runID string, outcome Outcome, _ fixture.FixtureSummary) (PackageProjection, error) {
	f.finalizeCalls++
	return PackageProjection{RunID: runID, PackageDir: f.location, Outcome: outcome}, nil
}

// flowArtifactStoreFake persists the exact admitted artifact for execution recovery tests.
type flowArtifactStoreFake struct{ artifact functionalflow.Artifact }

// failingFlowJournal exposes deterministic durable-journal failures to adapter boundary tests.
type failingFlowJournal struct{}

// AppendEvent rejects causal persistence without leaking an external error payload.
func (failingFlowJournal) AppendEvent(context.Context, functionalflow.Event) (functionalflow.Event, error) {
	return functionalflow.Event{}, errors.New("journal canary")
}

// Events rejects causal reload without returning partial history.
func (failingFlowJournal) Events(context.Context, string) ([]functionalflow.Event, error) {
	return nil, errors.New("journal canary")
}

// Commit retains one detached artifact under the workflow handle.
func (f *flowArtifactStoreFake) Commit(_ context.Context, _ string, artifact functionalflow.Artifact) error {
	f.artifact = artifact
	return nil
}

// Load returns the artifact committed before fixture preparation.
func (f *flowArtifactStoreFake) Load(context.Context, string) (functionalflow.Artifact, error) {
	return f.artifact, nil
}

// flowExecutionPortFake records when Service reaches its existing functional execution phase.
type flowExecutionPortFake struct {
	admitCalls, calls, finalizeCalls int
	trace                            *[]string
	outcome                          Outcome
	admitErr                         error
	finalized                        []fixture.FixtureSummary
	selections                       []*FunctionalFlowSelection
	approval                         planapproval.FunctionalFlowSnapshot
	preparations                     []fixture.PreparationStatus
}

// forbiddenGeneration records an unexpected model call on a provided-flow path.
type forbiddenGeneration struct{ calls int }

// Generate fails if provided-flow orchestration crosses the independent generation boundary.
func (f *forbiddenGeneration) Generate(context.Context, AdmissionSnapshot) (agentplanning.Result, ModelUsage, error) {
	f.calls++
	return agentplanning.Result{}, ModelUsage{}, errors.New("generation forbidden")
}

// InspectProvided supplies no generated substitute in tests that do not select a provided flow.
func (f *flowExecutionPortFake) InspectProvided(context.Context, ExecutionRequest) (functionalflow.Flow, error) {
	return functionalflow.Flow{Version: functionalflow.VersionV1, ID: "provided", Source: functionalflow.SourceProvided, Revision: 1, ContextDigest: strings.Repeat("a", 64), RequiredEvidence: []string{"6:step-1:assert-1"}, Steps: []functionalflow.Step{{ID: "step-1", Kind: functionalflow.StepObserve, ConstraintID: "c1", Operation: functionalflow.OperationReference{Capability: "probe", TargetID: "target-1", Method: "GET", Path: "/pets", ContractDigest: strings.Repeat("2", 64)}, IdentityID: "public", Input: map[string]functionalflow.InputValue{}, Assertions: []planning.AssertionDeclaration{{ID: "assert-1", Definition: map[string]any{"operator": "exists"}}}, RequiredEvidence: []string{"assert-1"}}}}, nil
}

// Admit records exact flow admission before the shared fixture lifecycle starts.
func (f *flowExecutionPortFake) Admit(_ context.Context, request ExecutionRequest, _ agentplanning.PreparedSnapshot) (planapproval.FunctionalFlowSnapshot, error) {
	f.admitCalls++
	f.selections = append(f.selections, cloneFunctionalFlowSelection(request.FunctionalFlow))
	if f.trace != nil {
		*f.trace = append(*f.trace, "admit")
	}
	if f.approval.ArtifactDigest == "" {
		digest := strings.Repeat("a", 64)
		source := "generated"
		if request.FunctionalFlow != nil {
			source = "provided"
		}
		f.approval = planapproval.FunctionalFlowSnapshot{FlowID: "flow", Source: source, Revision: 1, ArtifactDigest: digest, SemanticDigest: digest, Steps: []planapproval.FunctionalFlowStepSnapshot{{ID: "step", Kind: "observe", ConstraintID: "c1", IdentityID: "reader", Capability: "http.request", TargetID: "target", Method: "GET", Path: "/health", BindingDigest: digest, RequirementsDigest: digest}}}
	}
	return f.approval, f.admitErr
}

// RecordPreparation captures the fixture terminal used by service orchestration tests.
func (f *flowExecutionPortFake) RecordPreparation(_ context.Context, _ string, status fixture.PreparationStatus) (fixture.PreparationStatus, error) {
	f.preparations = append(f.preparations, status)
	return status, nil
}

// Execute returns the package location expected by the existing package reader.
func (f *flowExecutionPortFake) Execute(context.Context, ExecutionRequest, agentplanning.PreparedSnapshot) (FunctionalFlowPrimary, error) {
	f.calls++
	if f.trace != nil {
		*f.trace = append(*f.trace, "flow")
	}
	if f.outcome == "" {
		return FunctionalFlowPrimary{Outcome: OutcomePassed, runID: "fake"}, nil
	}
	return FunctionalFlowPrimary{Outcome: f.outcome, runID: "fake"}, nil
}

// Promote records that primary visibility occurred after the service checkpoint.
func (f *flowExecutionPortFake) Promote(context.Context, FunctionalFlowPrimary) error {
	if f.trace != nil {
		*f.trace = append(*f.trace, "promote")
	}
	return nil
}

// Finalize records post-cleanup package completion and returns the immutable primary outcome.
func (f *flowExecutionPortFake) Finalize(_ context.Context, handle string, outcome Outcome, summary fixture.FixtureSummary) (PackageProjection, error) {
	return f.finalize(handle, outcome, summary)
}

// finalize records cleanup-aware packaging inputs and returns the controlled package projection used by service tests.
func (f *flowExecutionPortFake) finalize(handle string, outcome Outcome, summary fixture.FixtureSummary) (PackageProjection, error) {
	f.finalizeCalls++
	f.finalized = append(f.finalized, summary)
	if f.trace != nil {
		*f.trace = append(*f.trace, "finalize")
	}
	return PackageProjection{RunID: handle, PackageDir: "package-safe", Outcome: outcome}, nil
}

// orderedFlowFixtures records lifecycle ordering while returning only safe feature-26 projections.
type orderedFlowFixtures struct {
	trace                      *[]string
	status                     fixture.PreparationStatus
	cleanupStatus              fixture.CleanupStatus
	prepareErr, cleanupErr     error
	prepareCalls, cleanupCalls int
}

// rejectedFlowHarness exposes only the counters and stores needed to prove pre-prepare terminal convergence.
type rejectedFlowHarness struct {
	service   *Service
	executor  *flowExecutorFake
	fixtures  *orderedFlowFixtures
	publisher *LocalFunctionalFlowPublisher
}

// newRejectedFlowHarness composes a real artifact journal and package publisher behind the shared workflow service.
func newRejectedFlowHarness(t *testing.T) rejectedFlowHarness {
	t.Helper()
	service, _, _, _, _ := workflowFixture(t, validPlanResponse())
	service.config.FinalizeTimeout = 5 * time.Second
	validated, artifact := workflowFlowFixture(t)
	artifacts, err := OpenLocalFunctionalFlowArtifactStore(filepath.Join(t.TempDir(), "artifacts"))
	if err != nil {
		t.Fatal(err)
	}
	journal, err := functionalflow.NewLocalStore(filepath.Join(t.TempDir(), "journal"), planningClock{}, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	publisher, err := OpenLocalFunctionalFlowPublisher(filepath.Join(t.TempDir(), "packages"), artifacts, nil, journal)
	if err != nil {
		t.Fatal(err)
	}
	executor := &flowExecutorFake{}
	service.deps.FunctionalFlow = FunctionalFlowExecutionAdapter{Composer: &flowComposerFake{validated: validated, artifact: artifact}, Executor: executor, Artifacts: artifacts, Publisher: publisher, Journal: journal}
	fixtures := &orderedFlowFixtures{status: fixture.PreparationPrepared}
	service.deps.Fixtures = fixtures
	return rejectedFlowHarness{service: service, executor: executor, fixtures: fixtures, publisher: publisher}
}

// newRecoveringFlowService composes the real workflow adapter, artifact store, journal and publisher around one restartable executor.
func newRecoveringFlowService(t *testing.T, failure error) (*Service, *recoveringFlowExecutor, *orderedFlowFixtures, *testClock) {
	t.Helper()
	service, _, _, _, clock := workflowFixture(t, validPlanResponse())
	service.config.FinalizeTimeout = 5 * time.Second
	validated, artifact := workflowFlowFixture(t)
	artifacts, err := OpenLocalFunctionalFlowArtifactStore(filepath.Join(t.TempDir(), "artifacts"))
	if err != nil {
		t.Fatal(err)
	}
	journal, err := functionalflow.NewLocalStore(filepath.Join(t.TempDir(), "journal"), planningClock{}, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	publisher, err := OpenLocalFunctionalFlowPublisher(filepath.Join(t.TempDir(), "packages"), artifacts, nil, journal)
	if err != nil {
		t.Fatal(err)
	}
	executor := &recoveringFlowExecutor{failure: failure, journal: journal}
	service.deps.FunctionalFlow = FunctionalFlowExecutionAdapter{Composer: &flowComposerFake{validated: validated, artifact: artifact}, Executor: executor, Artifacts: artifacts, Publisher: publisher, Journal: journal}
	fixtures := &orderedFlowFixtures{status: fixture.PreparationPrepared}
	service.deps.Fixtures = fixtures
	return service, executor, fixtures, clock
}

// checkpointFlowHarness exposes the real executor and durable authorities needed for REV-20 vertical crash tests.
type checkpointFlowHarness struct {
	service                *Service
	clock                  *testClock
	tool                   *checkpointFlowTool
	executor               *checkpointExecutorProbe
	journal                *functionalflow.LocalStore
	state                  *functionalflow.LocalStore
	evidence               *LocalFunctionalFlowEvidenceStore
	fixtures               *orderedFlowFixtures
	publisher              *crashStagePublisher
	reconciler             *droppingTerminalState
	stateRoot, journalRoot string
}

// newCheckpointFlowHarness composes a real executor, LocalStore journal/state, evidence store and publisher behind Service.
func newCheckpointFlowHarness(t *testing.T, stageCrash, journalCrash string, dropTerminal ...bool) checkpointFlowHarness {
	t.Helper()
	service, _, _, _, clock := workflowFixture(t, validPlanResponse())
	service.config.FinalizeTimeout = 5 * time.Second
	validated, artifact := workflowFlowFixture(t)
	artifacts, err := OpenLocalFunctionalFlowArtifactStore(filepath.Join(t.TempDir(), "artifacts"))
	if err != nil {
		t.Fatal(err)
	}
	evidenceStore, err := OpenLocalFunctionalFlowEvidenceStore(filepath.Join(t.TempDir(), "evidence"))
	if err != nil {
		t.Fatal(err)
	}
	stateRoot := filepath.Join(t.TempDir(), "state")
	state, err := functionalflow.NewLocalStore(stateRoot, clock, 30*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	journalRoot := filepath.Join(t.TempDir(), "journal")
	journalStore, err := functionalflow.NewLocalStore(journalRoot, clock, 30*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	journal := functionalflow.EventJournal(journalStore)
	if journalCrash != "" {
		journal = &faultEventJournal{inner: journalStore, failType: journalCrash}
	}
	tool := &checkpointFlowTool{}
	bound := artifact.Steps[0]
	authority := functionalflow.BindingAuthority{Operations: []functionalflow.OperationAuthority{{Reference: bound.Operation.Reference, Effectful: bound.Operation.Effectful, Idempotent: bound.Operation.Idempotent, ReconciliationSafe: bound.Operation.ReconciliationSafe}}, Identities: []testidentity.Descriptor{{ID: bound.Step.IdentityID, Mechanism: testidentity.MechanismBearer, Operations: []testidentity.Operation{bound.Operation.Identity.Operation}}}, Bindings: artifact.Bindings}
	var statePort functionalflow.IntentStore = state
	var reconciler *droppingTerminalState
	if len(dropTerminal) > 0 && dropTerminal[0] {
		reconciler = &droppingTerminalState{inner: state, drop: true}
		statePort = reconciler
	}
	realExecutor := &functionalflow.Executor{Authority: authority, Clock: clock, Policy: checkpointFlowPolicy{}, Revalidator: checkpointFlowRevalidator{}, Tool: tool, Evidence: evidenceStore, Assertions: checkpointAssertionEvaluator{}, State: statePort, Journal: journal, Keys: functionalFlowKeyFactory{}, Waiter: clock, Config: functionalflow.ExecutionConfig{Owner: "functional-owner", MaxCalls: 2, MaxRetries: 0, MaxWork: 10, StepTimeout: 5 * time.Second, RunTimeout: 30 * time.Second, ConfirmationTimeout: 5 * time.Second}}
	executor := &checkpointExecutorProbe{inner: realExecutor}
	realPublisher, err := OpenLocalFunctionalFlowPublisher(filepath.Join(t.TempDir(), "packages"), artifacts, evidenceStore, journalStore)
	if err != nil {
		t.Fatal(err)
	}
	publisher := &crashStagePublisher{inner: realPublisher, mode: stageCrash}
	service.deps.FunctionalFlow = FunctionalFlowExecutionAdapter{Composer: &flowComposerFake{validated: validated, artifact: artifact}, Executor: executor, Artifacts: artifacts, Publisher: publisher, Journal: journal}
	fixtures := &orderedFlowFixtures{status: fixture.PreparationPrepared}
	service.deps.Fixtures = fixtures
	return checkpointFlowHarness{service: service, clock: clock, tool: tool, executor: executor, journal: journalStore, state: state, evidence: evidenceStore, fixtures: fixtures, publisher: publisher, reconciler: reconciler, stateRoot: stateRoot, journalRoot: journalRoot}
}

// restart creates a second Service owner over the same durable stores without copying local workers.
func (h checkpointFlowHarness) restart(t *testing.T, owner string) *Service {
	t.Helper()
	deps := h.service.deps
	deps.Owner = owner
	restarted, err := NewService(h.service.config, deps)
	if err != nil {
		t.Fatal(err)
	}
	return restarted
}

// installRecoveryAuthorities replaces only read authorities used by a restarted recovery probe.
func (h checkpointFlowHarness) installRecoveryAuthorities(journal functionalflow.EventJournal, evidence functionalflow.EvidenceStore) {
	h.executor.inner.Journal = journal
	h.executor.inner.Evidence = evidence
	adapter := h.service.deps.FunctionalFlow.(FunctionalFlowExecutionAdapter)
	adapter.Journal = journal
	h.service.deps.FunctionalFlow = adapter
}

// rechainCheckpointJournal persists a semantically mutated but cryptographically valid journal in a fresh LocalStore.
func rechainCheckpointJournal(t *testing.T, harness checkpointFlowHarness, runID string, mutate func(*functionalflow.Event)) *functionalflow.LocalStore {
	t.Helper()
	events, err := harness.journal.Events(context.Background(), runID)
	if err != nil {
		t.Fatal(err)
	}
	mutated := false
	for index := range events {
		if !mutated {
			before := events[index]
			mutate(&events[index])
			mutated = !reflect.DeepEqual(before, events[index])
		}
		events[index].Sequence = 0
		events[index].PreviousDigest = ""
		events[index].Digest = ""
	}
	if !mutated {
		t.Fatal("journal mutation did not match an event")
	}
	store, err := functionalflow.NewLocalStore(filepath.Join(t.TempDir(), "authenticated-journal"), harness.clock, 30*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	for _, event := range events {
		if _, err = store.AppendEvent(context.Background(), event); err != nil {
			t.Fatal(err)
		}
	}
	return store
}

// mutateCheckpointEvidence persists one independently sealed divergent evidence object in a real evidence store.
func mutateCheckpointEvidence(t *testing.T, harness checkpointFlowHarness, mutate func(*functionalflow.Evidence)) *LocalFunctionalFlowEvidenceStore {
	t.Helper()
	item, err := harness.evidence.Load(context.Background(), "checkpoint-evidence")
	if err != nil {
		t.Fatal(err)
	}
	mutate(&item)
	item = functionalflow.SealEvidence(item)
	store, err := OpenLocalFunctionalFlowEvidenceStore(filepath.Join(t.TempDir(), "authenticated-evidence"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = store.Confirm(context.Background(), item); err != nil {
		t.Fatal(err)
	}
	return store
}

// loadFunctionalPackage reads one package produced by the real publisher and revalidates it offline.
func loadFunctionalPackage(t *testing.T, projection *PackageProjection) functionalflow.Package {
	t.Helper()
	if projection == nil {
		t.Fatal("missing package projection")
	}
	raw, err := os.ReadFile(filepath.Join(projection.PackageDir, "functional-flow-package.json"))
	if err != nil {
		t.Fatal(err)
	}
	var pkg functionalflow.Package
	if json.Unmarshal(raw, &pkg) != nil || functionalflow.VerifyPackage(pkg) != nil {
		t.Fatalf("invalid package %s", raw)
	}
	return pkg
}

// Prepare proves fixture preparation precedes the functional execution port.
func (f *orderedFlowFixtures) Prepare(context.Context, string) (fixture.FixtureSummary, error) {
	f.prepareCalls++
	if f.trace != nil {
		*f.trace = append(*f.trace, "prepare")
	}
	return fixture.FixtureSummary{PreparationStatus: f.status}, f.prepareErr
}

// Cleanup proves the shared terminal path owns finite cleanup after package verification.
func (f *orderedFlowFixtures) Cleanup(context.Context, string) (fixture.CleanupReport, error) {
	f.cleanupCalls++
	if f.trace != nil {
		*f.trace = append(*f.trace, "cleanup")
	}
	status := f.cleanupStatus
	if status == "" {
		status = fixture.CleanupSucceeded
	}
	return fixture.CleanupReport{Status: status}, f.cleanupErr
}

// TestFunctionalFlowExecutionAdapterUsesApprovedMaterial proves the adapter composes, executes, and publishes in order.
func TestFunctionalFlowExecutionAdapterUsesApprovedMaterial(t *testing.T) {
	validated, artifact := workflowFlowFixture(t)
	composer := &flowComposerFake{validated: validated, artifact: artifact}
	executor := &flowExecutorFake{result: functionalflow.Result{Outcome: functionalflow.OutcomePassed}}
	publisher := &flowPublisherFake{location: "package-safe"}
	artifacts := &flowArtifactStoreFake{}
	journal, journalErr := functionalflow.NewLocalStore(t.TempDir(), planningClock{}, time.Hour)
	if journalErr != nil {
		t.Fatal(journalErr)
	}
	adapter := FunctionalFlowExecutionAdapter{Composer: composer, Executor: executor, Artifacts: artifacts, Publisher: publisher, Journal: journal}
	request, snapshot := ExecutionRequest{Handle: "run"}, agentplanning.PreparedSnapshot{ID: "plan"}
	providedRequest := request
	providedRequest.FunctionalFlow = &FunctionalFlowSelection{ArtifactID: "provided"}
	if inspected, inspectErr := adapter.InspectProvided(context.Background(), providedRequest); inspectErr != nil || inspected.ID != validated.Flow().ID {
		t.Fatalf("inspected=%#v err=%v", inspected, inspectErr)
	}
	approval, err := adapter.Admit(context.Background(), request, snapshot)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = adapter.RecordPreparation(context.Background(), request.Handle, fixture.PreparationNotRequired); err != nil {
		t.Fatal(err)
	}
	if actual, repeatErr := adapter.RecordPreparation(context.Background(), request.Handle, fixture.PreparationRejected); repeatErr != nil || actual != fixture.PreparationNotRequired {
		t.Fatalf("durable preparation=%s err=%v", actual, repeatErr)
	}
	request.FunctionalFlowDigest = approval.ArtifactDigest
	wrong := request
	wrong.FunctionalFlowDigest = strings.Repeat("f", 64)
	if _, wrongErr := adapter.Execute(context.Background(), wrong, snapshot); wrongErr == nil || executor.calls != 0 {
		t.Fatalf("approval for artifact A executed artifact B: calls=%d err=%v", executor.calls, wrongErr)
	}
	outcome, err := adapter.Execute(context.Background(), request, snapshot)
	if err == nil && publisher.stageCalls != 0 {
		t.Fatalf("functional primary visible before environment checkpoint: %d", publisher.stageCalls)
	}
	if err == nil {
		err = adapter.Promote(context.Background(), outcome)
	}
	projection, finalizeErr := adapter.Finalize(context.Background(), "run", outcome.Outcome, fixture.FixtureSummary{})
	if err != nil || finalizeErr != nil || projection.PackageDir != "package-safe" || composer.calls != 4 || executor.calls != 1 || publisher.stageCalls != 1 || publisher.finalizeCalls != 1 {
		t.Fatalf("projection=%#v calls=%d/%d/%d/%d err=%v/%v", projection, composer.calls, executor.calls, publisher.stageCalls, publisher.finalizeCalls, err, finalizeErr)
	}
}

// TestProvidedCompatibilityCoversTypedInputsAndHTTPAuthority proves the non-model bridge preserves input types and exact identity selection.
func TestProvidedCompatibilityCoversTypedInputsAndHTTPAuthority(t *testing.T) {
	field := "value"
	values := []functionalflow.InputValue{
		{Literal: &planning.LiteralValue{Value: "literal"}},
		{Object: map[string]functionalflow.InputValue{"nested": {Literal: &planning.LiteralValue{Value: true}}}},
		{Array: []functionalflow.InputValue{{Literal: &planning.LiteralValue{Value: 1}}}},
		{Reference: &functionalflow.Reference{Step: &functionalflow.StepReference{StepID: "prior", Path: []planning.PathSegment{{Field: &field}}, ValueType: planning.ValueNumber}}},
		{Reference: &functionalflow.Reference{Fixture: &functionalflow.FixtureReference{FixtureID: "fixture", Output: "flag", ValueType: planning.ValueBoolean}}},
	}
	for _, value := range values {
		if converted := providedPlanningInput(value); converted.Literal == nil && converted.Object == nil && converted.Array == nil {
			t.Fatalf("input was not converted: %#v", value)
		}
	}
	for _, valueType := range []planning.ValueType{planning.ValueString, planning.ValueNumber, planning.ValueBoolean, planning.ValueObject, planning.ValueArray} {
		if zeroProvidedValue(valueType) == nil {
			t.Fatalf("missing zero for %s", valueType)
		}
	}
	pathInput := map[string]planning.InputValue{"id": {Literal: &planning.LiteralValue{Value: "42"}}}
	if params := providedPathParams("/pets/{id}", pathInput); params["id"] != "42" || len(providedPathParams("/pets/{missing", pathInput)) != 0 {
		t.Fatalf("path params=%#v", params)
	}
	snapshot := validSnapshot()
	snapshot.Generation.RunID, snapshot.Generation.PlanID = "provided-run", "provided-plan"
	snapshot.Generation.Capabilities = []agentplanning.CapabilityDescriptor{{Name: "http.request", Class: "read", Validate: func(map[string]planning.InputValue) error { return nil }}}
	snapshot.Generation.Identities = []testidentity.Descriptor{{ID: "public", Mechanism: testidentity.MechanismAnonymous, Operations: []testidentity.Operation{{Capability: "http.request", TargetID: "target-1", Method: "GET", Path: "/pets"}}}}
	flow := functionalflow.Flow{Steps: []functionalflow.Step{{ID: "step", ConstraintID: "c1", Operation: functionalflow.OperationReference{Capability: "http.request", TargetID: "target-1", Method: "GET", Path: "/pets"}, IdentityID: "public", Input: map[string]functionalflow.InputValue{"method": {Literal: &planning.LiteralValue{Value: "GET"}}, "path": {Literal: &planning.LiteralValue{Value: "/pets"}}}, Assertions: []planning.AssertionDeclaration{{ID: "assert", Definition: map[string]any{"operator": "exists"}}}}}}
	prepared, err := (PreparationAdapter{Assertions: fakeAssertions{}, Policy: fakePolicy{}}).PrepareProvided(context.Background(), snapshot, flow)
	if err != nil || len(prepared.Checks()) != 1 {
		t.Fatalf("prepared=%#v err=%v", prepared, err)
	}
}

// TestProvidedFlowNeverInvokesModelGeneration proves explicit provided authority prepares a compatibility artifact independently.
func TestProvidedFlowNeverInvokesModelGeneration(t *testing.T) {
	service, _, _, _, _ := workflowFixture(t, validPlanResponse())
	flow := &flowExecutionPortFake{}
	forbidden := &forbiddenGeneration{}
	service.deps.FunctionalFlow, service.deps.Generation = flow, forbidden
	command := validCommand()
	command.IdempotencyKey = "provided-without-model"
	command.FunctionalFlow = &FunctionalFlowSelection{ArtifactID: "approved-flow"}
	view, err := service.Start(context.Background(), command)
	if err != nil || view.Phase != PhaseAwaitingApproval || forbidden.calls != 0 || flow.admitCalls != 1 {
		t.Fatalf("view=%#v generation=%d admission=%d err=%v", view, forbidden.calls, flow.admitCalls, err)
	}
	approval, err := service.ApprovalView(context.Background(), view.Handle)
	if err != nil || approval.FunctionalFlow == nil || approval.FlowArtifactDigest != flow.approval.ArtifactDigest || approval.FunctionalFlow.Source != "provided" || len(approval.FunctionalFlow.Steps) != 1 {
		t.Fatalf("approval=%#v expected=%#v err=%v", approval, flow.approval, err)
	}
}

// TestFunctionalFlowAdapterJournalFailuresStayClosed covers invalid composition and preparation persistence boundaries.
func TestFunctionalFlowAdapterJournalFailuresStayClosed(t *testing.T) {
	if _, err := (FunctionalFlowExecutionAdapter{}).InspectProvided(context.Background(), ExecutionRequest{}); err == nil {
		t.Fatal("inspection accepted missing provided authority")
	}
	validated, artifact := workflowFlowFixture(t)
	composer := &flowComposerFake{validated: validated, artifact: artifact}
	store := &flowArtifactStoreFake{artifact: artifact}
	adapter := FunctionalFlowExecutionAdapter{Composer: composer, Executor: &flowExecutorFake{}, Artifacts: store, Publisher: &flowPublisherFake{}, Journal: failingFlowJournal{}}
	if _, err := adapter.InspectProvided(context.Background(), ExecutionRequest{FunctionalFlow: &FunctionalFlowSelection{ArtifactID: "approved"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := adapter.RecordPreparation(context.Background(), "run", ""); err == nil {
		t.Fatal("preparation accepted empty status")
	}
	if _, err := adapter.RecordPreparation(context.Background(), "run", fixture.PreparationStatus("unknown")); err == nil {
		t.Fatal("preparation accepted open status")
	}
	if _, err := adapter.RecordPreparation(context.Background(), "run", fixture.PreparationPrepared); err == nil {
		t.Fatal("preparation ignored journal failure")
	}
	if _, err := adapter.Admit(context.Background(), ExecutionRequest{Handle: "run"}, agentplanning.PreparedSnapshot{}); err == nil {
		t.Fatal("admission ignored journal failure")
	}
}

// TestRecordPreparationRejectsMissingDriftedAndOpenJournalStates proves the durable preparation union cannot be backfilled over invalid history.
func TestRecordPreparationRejectsMissingDriftedAndOpenJournalStates(t *testing.T) {
	_, artifact := workflowFlowFixture(t)
	newAdapter := func(t *testing.T, events []functionalflow.Event) FunctionalFlowExecutionAdapter {
		t.Helper()
		journal, err := functionalflow.NewLocalStore(t.TempDir(), planningClock{}, time.Hour)
		if err != nil {
			t.Fatal(err)
		}
		for _, event := range events {
			if event.RunID == "" {
				event.RunID, event.FlowID, event.Revision, event.ArtifactDigest = "run", artifact.FlowID, artifact.Revision, artifact.ArtifactDigest
			}
			if _, err = journal.AppendEvent(context.Background(), event); err != nil {
				t.Fatal(err)
			}
		}
		return FunctionalFlowExecutionAdapter{Artifacts: &flowArtifactStoreFake{artifact: artifact}, Journal: journal}
	}
	cases := []FunctionalFlowExecutionAdapter{
		newAdapter(t, nil),
		newAdapter(t, []functionalflow.Event{{Type: "flow_artifact_committed", RunID: "run", FlowID: artifact.FlowID, Revision: artifact.Revision, ArtifactDigest: strings.Repeat("f", 64)}}),
		newAdapter(t, []functionalflow.Event{{Type: "flow_artifact_committed"}, {Type: "primary_terminal", Outcome: functionalflow.OutcomeInconclusive}}),
		newAdapter(t, []functionalflow.Event{{Type: "flow_artifact_committed"}, {Type: "fixtures_terminal", Status: "unknown"}}),
	}
	for i, adapter := range cases {
		if _, err := adapter.RecordPreparation(context.Background(), "run", fixture.PreparationRejected); err == nil {
			t.Fatalf("journal case %d was accepted", i)
		}
	}
}

// TestFixturesPrepareBeforeAnyBusinessStep proves the existing Service owns prepare, flow execution, and cleanup ordering.
func TestFixturesPrepareBeforeAnyBusinessStep(t *testing.T) {
	service, _, oldExecution, _, _ := workflowFixture(t, validPlanResponse())
	trace := []string{}
	flowPort := &flowExecutionPortFake{trace: &trace}
	fixtures := &orderedFlowFixtures{trace: &trace, status: fixture.PreparationPrepared}
	service.deps.FunctionalFlow, service.deps.Fixtures, service.deps.FixturePackages = flowPort, fixtures, fakePackages{}
	view, err := service.Start(context.Background(), validCommand())
	if err != nil {
		t.Fatal(err)
	}
	result, err := service.Decide(context.Background(), DecisionCommand{Principal: "user-1", Handle: view.Handle, Revision: view.ApprovalRevision, DecisionID: "flow-decision", Action: DecisionApprove})
	if err != nil || result.Outcome == nil || *result.Outcome != OutcomePassed || oldExecution.callCount() != 0 || flowPort.admitCalls != 1 || flowPort.calls != 1 || flowPort.finalizeCalls != 1 || strings.Join(trace, ",") != "admit,prepare,flow,promote,cleanup,finalize" {
		t.Fatalf("result=%#v trace=%v old=%d flow=%d err=%v", result, trace, oldExecution.callCount(), flowPort.calls, err)
	}
}

// TestPreparationFailureBlocksFlowAndStillCleans proves preparation failure never invokes the functional execution port.
func TestPreparationFailureBlocksFlowAndStillCleans(t *testing.T) {
	service, _, _, _, _ := workflowFixture(t, validPlanResponse())
	flowPort := &flowExecutionPortFake{}
	fixtures := &orderedFlowFixtures{status: fixture.PreparationFailed, prepareErr: errors.New("preparation unavailable")}
	service.deps.FunctionalFlow, service.deps.Fixtures, service.deps.FixturePackages = flowPort, fixtures, fakePackages{}
	view, err := service.Start(context.Background(), validCommand())
	if err != nil {
		t.Fatal(err)
	}
	result, err := service.Decide(context.Background(), DecisionCommand{Principal: "user-1", Handle: view.Handle, Revision: view.ApprovalRevision, DecisionID: "flow-failure", Action: DecisionApprove})
	if err != nil || result.Outcome == nil || *result.Outcome != OutcomeInconclusive || flowPort.admitCalls != 1 || flowPort.calls != 0 || flowPort.finalizeCalls != 1 || fixtures.cleanupCalls != 1 {
		t.Fatalf("result=%#v flow=%d cleanup=%d err=%v", result, flowPort.calls, fixtures.cleanupCalls, err)
	}
}

// TestPrePrepareTerminalsPublishRejectedLifecycleWithoutInventedWork proves reject, cancel, and auto-denial converge after admission.
func TestPrePrepareTerminalsPublishRejectedLifecycleWithoutInventedWork(t *testing.T) {
	type scenario struct {
		name    string
		outcome Outcome
		finish  func(*testing.T, rejectedFlowHarness, WorkflowView) WorkflowView
	}
	scenarios := []scenario{
		{name: "human-reject", outcome: OutcomeCancelled, finish: func(t *testing.T, harness rejectedFlowHarness, view WorkflowView) WorkflowView {
			terminal, err := harness.service.Decide(context.Background(), DecisionCommand{Principal: "user-1", Handle: view.Handle, Revision: view.ApprovalRevision, DecisionID: "reject-flow", Action: DecisionReject})
			if err != nil {
				t.Fatal(err)
			}
			return terminal
		}},
		{name: "cancel", outcome: OutcomeCancelled, finish: func(t *testing.T, harness rejectedFlowHarness, view WorkflowView) WorkflowView {
			terminal, err := harness.service.Cancel(context.Background(), "user-1", view.Handle)
			if err != nil {
				t.Fatal(err)
			}
			return terminal
		}},
	}
	for _, item := range scenarios {
		t.Run(item.name, func(t *testing.T) {
			harness := newRejectedFlowHarness(t)
			command := validCommand()
			command.IdempotencyKey = "pre-prepare-" + item.name
			view, err := harness.service.Start(context.Background(), command)
			if err != nil || view.Phase != PhaseAwaitingApproval {
				t.Fatalf("view=%#v err=%v", view, err)
			}
			terminal := item.finish(t, harness, view)
			if terminal.Outcome == nil || *terminal.Outcome != item.outcome {
				t.Fatalf("terminal=%#v", terminal)
			}
			result, err := harness.service.Result(context.Background(), view.Handle)
			if err != nil || result.Package == nil {
				t.Fatalf("result=%#v err=%v", result, err)
			}
			pkg := loadFunctionalPackage(t, result.Package)
			if pkg.Events[1].Status != string(fixture.PreparationRejected) || pkg.Fixtures.PreparationStatus != fixture.PreparationRejected || len(pkg.Events) != 4 || len(pkg.Primary.Steps) != 0 || len(pkg.Evidence) != 0 || harness.executor.calls != 0 || harness.fixtures.prepareCalls != 0 || harness.fixtures.cleanupCalls != 0 {
				t.Fatalf("package=%#v executor=%d fixtures=%d/%d", pkg, harness.executor.calls, harness.fixtures.prepareCalls, harness.fixtures.cleanupCalls)
			}
			packagePath := filepath.Join(result.Package.PackageDir, "functional-flow-package.json")
			if os.Remove(packagePath) != nil {
				t.Fatal("could not remove package at recovery boundary")
			}
			if recovered, recoverErr := harness.publisher.Finalize(context.Background(), view.Handle, item.outcome, *result.Package.Fixtures); recoverErr != nil || recovered.PackageDir != result.Package.PackageDir {
				t.Fatalf("package recovery=%#v err=%v", recovered, recoverErr)
			}
			_ = loadFunctionalPackage(t, result.Package)
			if second, repeatErr := harness.service.Cancel(context.Background(), "user-1", view.Handle); repeatErr != nil || second.Outcome == nil || *second.Outcome != item.outcome {
				t.Fatalf("idempotent terminal=%#v err=%v", second, repeatErr)
			}
		})
	}
	t.Run("automatic-denial", func(t *testing.T) {
		harness := newRejectedFlowHarness(t)
		harness.service.deps.Authorization = fakeAuto{err: errors.New("denied")}
		command := validCommand()
		command.IdempotencyKey = "pre-prepare-auto-denial"
		command.Approval = ApprovalDisabled
		terminal, err := harness.service.Start(context.Background(), command)
		if err != nil || terminal.Outcome == nil || *terminal.Outcome != OutcomeInconclusive {
			t.Fatalf("terminal=%#v err=%v", terminal, err)
		}
		result, err := harness.service.Result(context.Background(), terminal.Handle)
		if err != nil || result.Package == nil {
			t.Fatalf("result=%#v err=%v", result, err)
		}
		pkg := loadFunctionalPackage(t, result.Package)
		if pkg.Events[1].Status != string(fixture.PreparationRejected) || len(pkg.Primary.Steps) != 0 || harness.executor.calls != 0 || harness.fixtures.prepareCalls != 0 || harness.fixtures.cleanupCalls != 0 {
			t.Fatalf("package=%#v executor=%d fixtures=%d/%d", pkg, harness.executor.calls, harness.fixtures.prepareCalls, harness.fixtures.cleanupCalls)
		}
	})
}

// TestRejectedResultParityAcrossFourSurfaces proves all real result adapters expose the same post-cleanup package.
func TestRejectedResultParityAcrossFourSurfaces(t *testing.T) {
	harness := newRejectedFlowHarness(t)
	command := validCommand()
	command.IdempotencyKey = "rejected-result-parity"
	view, err := harness.service.Start(context.Background(), command)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = harness.service.Decide(context.Background(), DecisionCommand{Principal: "user-1", Handle: view.Handle, Revision: view.ApprovalRevision, DecisionID: "reject-parity", Action: DecisionReject}); err != nil {
		t.Fatal(err)
	}
	apiResult, err := harness.service.Result(context.Background(), view.Handle)
	if err != nil {
		t.Fatal(err)
	}
	cli := CLI{Service: harness.service, Principal: "user-1"}
	var cliOutput strings.Builder
	if code := cli.Run(context.Background(), []string{"result", view.Handle}, &cliOutput); code != 0 {
		t.Fatalf("cli code=%d output=%s", code, cliOutput.String())
	}
	var cliResult WorkflowResult
	if json.Unmarshal([]byte(cliOutput.String()), &cliResult) != nil {
		t.Fatal(cliOutput.String())
	}
	mcpResult := (MCPServer{Service: harness.service, Principal: "user-1"}).Handle(context.Background(), MCPRequest{Version: ContractVersion, Operation: "workflow.result", Arguments: json.RawMessage(`{"handle":"` + view.Handle + `"}`)})
	mcpValue, ok := mcpResult.Value.(WorkflowResult)
	if mcpResult.Error != "" || !ok {
		t.Fatalf("mcp=%#v", mcpResult)
	}
	githubResult, err := (GitHubGateway{Service: harness.service, Principal: "user-1"}).Result(context.Background(), view.Handle)
	if err != nil {
		t.Fatal(err)
	}
	for name, result := range map[string]WorkflowResult{"cli": cliResult, "mcp": mcpValue, "github": githubResult} {
		if result.Package == nil || apiResult.Package == nil || result.Package.PackageDir != apiResult.Package.PackageDir || result.Package.Outcome != apiResult.Package.Outcome || result.View.Phase != PhaseTerminal {
			t.Fatalf("%s result=%#v api=%#v", name, result, apiResult)
		}
	}
}

// TestFlowAdmissionFailureCreatesNoFixtureOrBusinessEffect proves normalization/binding failure precedes fixture preparation.
func TestFlowAdmissionFailureCreatesNoFixtureOrBusinessEffect(t *testing.T) {
	service, _, oldExecution, _, _ := workflowFixture(t, validPlanResponse())
	flowPort := &flowExecutionPortFake{admitErr: errors.New("invalid graph")}
	fixtures := &orderedFlowFixtures{status: fixture.PreparationPrepared}
	service.deps.FunctionalFlow, service.deps.Fixtures, service.deps.FixturePackages = flowPort, fixtures, fakePackages{}
	view, err := service.Start(context.Background(), validCommand())
	if err != nil || view.Outcome == nil || *view.Outcome != OutcomeInconclusive || flowPort.admitCalls != 1 || flowPort.calls != 0 || fixtures.prepareCalls != 0 || fixtures.cleanupCalls != 0 || oldExecution.callCount() != 0 {
		t.Fatalf("terminal=%#v flow=%d/%d fixture=%d/%d old=%d err=%v", view, flowPort.admitCalls, flowPort.calls, fixtures.prepareCalls, fixtures.cleanupCalls, oldExecution.callCount(), err)
	}
}

// TestCleanupRunsAfterEveryPrimaryOutcomeWithoutChangingVerdict proves cleanup cannot overwrite any primary terminal outcome.
func TestCleanupRunsAfterEveryPrimaryOutcomeWithoutChangingVerdict(t *testing.T) {
	for _, outcome := range []Outcome{OutcomePassed, OutcomeFailed, OutcomeInconclusive, OutcomeCancelled, OutcomeError} {
		t.Run(string(outcome), func(t *testing.T) {
			service, _, _, _, _ := workflowFixture(t, validPlanResponse())
			flowPort := &flowExecutionPortFake{outcome: outcome}
			fixtures := &orderedFlowFixtures{status: fixture.PreparationPrepared, cleanupStatus: fixture.CleanupPartial}
			service.deps.FunctionalFlow = flowPort
			service.deps.Fixtures = fixtures
			service.deps.FixturePackages = fakePackages{}
			service.deps.Packages = fakePackages{projection: PackageProjection{Outcome: outcome}}
			view, err := service.Start(context.Background(), validCommand())
			if err != nil {
				t.Fatal(err)
			}
			terminal, err := service.Decide(context.Background(), DecisionCommand{Principal: "user-1", Handle: view.Handle, Revision: view.ApprovalRevision, DecisionID: "cleanup-" + string(outcome), Action: DecisionApprove})
			if err != nil || terminal.Outcome == nil || *terminal.Outcome != outcome || flowPort.calls != 1 || fixtures.cleanupCalls != 1 || terminal.Fixtures == nil || terminal.Fixtures.Cleanup.Status != fixture.CleanupPartial {
				t.Fatalf("terminal=%#v flow=%d cleanup=%d err=%v", terminal, flowPort.calls, fixtures.cleanupCalls, err)
			}
		})
	}
}

// TestCleanupRecoveryConvergesSeparately proves recovery retries cleanup without replaying the functional flow.
func TestCleanupRecoveryConvergesSeparately(t *testing.T) {
	service, _, _, _, clock := workflowFixture(t, validPlanResponse())
	flowPort := &flowExecutionPortFake{}
	fixtures := &orderedFlowFixtures{status: fixture.PreparationPrepared, cleanupStatus: fixture.CleanupFailed, cleanupErr: errors.New("cleanup unavailable")}
	service.deps.FunctionalFlow, service.deps.Fixtures, service.deps.FixturePackages = flowPort, fixtures, fakePackages{}
	view, _ := service.Start(context.Background(), validCommand())
	_, err := service.Decide(context.Background(), DecisionCommand{Principal: "user-1", Handle: view.Handle, Revision: view.ApprovalRevision, DecisionID: "cleanup", Action: DecisionApprove})
	if !IsCode(err, CodeUnavailable) || flowPort.calls != 1 || fixtures.cleanupCalls != 1 {
		t.Fatalf("flow=%d cleanup=%d err=%v", flowPort.calls, fixtures.cleanupCalls, err)
	}
	fixtures.cleanupErr = nil
	fixtures.cleanupStatus = fixture.CleanupSucceeded
	clock.advance(2 * service.config.LeaseTTL)
	if err = service.Recover(context.Background()); err != nil {
		t.Fatal(err)
	}
	terminal, err := service.Result(context.Background(), view.Handle)
	if err != nil || terminal.View.Outcome == nil || *terminal.View.Outcome != OutcomePassed || flowPort.calls != 1 || flowPort.finalizeCalls != 1 || len(flowPort.finalized) != 1 || flowPort.finalized[0].Cleanup.Status != fixture.CleanupSucceeded || fixtures.cleanupCalls != 2 {
		t.Fatalf("terminal=%#v flow=%d cleanup=%d err=%v", terminal, flowPort.calls, fixtures.cleanupCalls, err)
	}
}

// TestServiceRecoversEveryPostToolBoundaryBeforeCleanup proves restart and a new owner resume the primary before cleanup or packaging.
func TestServiceRecoversEveryPostToolBoundaryBeforeCleanup(t *testing.T) {
	boundaries := []struct {
		name, path string
	}{
		{name: "claim-before-intent", path: "state.claim"},
		{name: "intent-before-io", path: "intent.confirm"},
		{name: "evidence-confirm", path: "evidence"},
		{name: "journal", path: "journal.append"},
		{name: "action-progress", path: "intent.progress"},
		{name: "assertions", path: "assertions"},
		{name: "terminal", path: "state.confirm"},
	}
	for _, boundary := range boundaries {
		t.Run(boundary.name, func(t *testing.T) {
			service, executor, fixtures, clock := newRecoveringFlowService(t, &functionalflow.Error{Category: functionalflow.CategoryIntegrity, Path: boundary.path})
			command := validCommand()
			command.IdempotencyKey = "recover-" + boundary.name
			view, err := service.Start(context.Background(), command)
			if err != nil {
				t.Fatal(err)
			}
			_, err = service.Decide(context.Background(), DecisionCommand{Principal: "user-1", Handle: view.Handle, Revision: view.ApprovalRevision, DecisionID: "decision-" + boundary.name, Action: DecisionApprove})
			if !IsRecoverableFunctionalFlowError(err) {
				t.Fatalf("expected recoverable boundary, got %v", err)
			}
			pending, err := service.deps.Store.Load(context.Background(), view.Handle)
			if err != nil || pending.Phase != PhaseExecuting || !pending.FunctionalFlowExecutionPending || pending.FunctionalFlowPrimaryDurable || pending.CleanupPending || pending.Package != nil || fixtures.prepareCalls != 1 || fixtures.cleanupCalls != 0 {
				t.Fatalf("pending=%#v prepare=%d cleanup=%d err=%v", pending, fixtures.prepareCalls, fixtures.cleanupCalls, err)
			}
			clock.advance(2 * service.config.LeaseTTL)
			deps := service.deps
			deps.Owner = "restart-owner"
			restarted, err := NewService(service.config, deps)
			if err != nil {
				t.Fatal(err)
			}
			if err = restarted.Recover(context.Background()); err != nil {
				t.Fatal(err)
			}
			result, err := restarted.Result(context.Background(), view.Handle)
			if err != nil || result.Package == nil || result.View.Outcome == nil || *result.View.Outcome != OutcomeInconclusive || executor.callCount() != 2 || executor.effectCount() > 1 || fixtures.prepareCalls != 1 || fixtures.cleanupCalls != 1 {
				t.Fatalf("result=%#v calls=%d effects=%d prepare=%d cleanup=%d err=%v", result, executor.callCount(), executor.effectCount(), fixtures.prepareCalls, fixtures.cleanupCalls, err)
			}
			pkg := loadFunctionalPackage(t, result.Package)
			if pkg.Primary.Outcome != functionalflow.OutcomeInconclusive || functionalflow.VerifyPackage(pkg) != nil {
				t.Fatalf("package=%#v", pkg)
			}
			cli := CLI{Service: restarted, Principal: "user-1"}
			var output strings.Builder
			if cli.Run(context.Background(), []string{"result", view.Handle}, &output) != 0 {
				t.Fatal(output.String())
			}
			mcp := (MCPServer{Service: restarted, Principal: "user-1"}).Handle(context.Background(), MCPRequest{Version: ContractVersion, Operation: "workflow.result", Arguments: json.RawMessage(`{"handle":"` + view.Handle + `"}`)})
			github, githubErr := (GitHubGateway{Service: restarted, Principal: "user-1"}).Result(context.Background(), view.Handle)
			if mcp.Error != "" || githubErr != nil || github.Package == nil || github.Package.PackageDir != result.Package.PackageDir {
				t.Fatalf("mcp=%#v github=%#v err=%v", mcp, github, githubErr)
			}
		})
	}
}

// TestServiceTerminalizesUnrecoverableFunctionalFailureWithDurablePrimary proves cleanup starts only after a real error primary is staged.
func TestServiceTerminalizesUnrecoverableFunctionalFailureWithDurablePrimary(t *testing.T) {
	service, executor, fixtures, _ := newRecoveringFlowService(t, &functionalflow.Error{Category: functionalflow.CategoryInvalid, Path: "executor.contract"})
	command := validCommand()
	command.IdempotencyKey = "unrecoverable-flow"
	view, err := service.Start(context.Background(), command)
	if err != nil {
		t.Fatal(err)
	}
	terminal, err := service.Decide(context.Background(), DecisionCommand{Principal: "user-1", Handle: view.Handle, Revision: view.ApprovalRevision, DecisionID: "unrecoverable-decision", Action: DecisionApprove})
	if err != nil || terminal.Outcome == nil || *terminal.Outcome != OutcomeError || executor.callCount() != 1 || fixtures.prepareCalls != 1 || fixtures.cleanupCalls != 1 {
		adapter := service.deps.FunctionalFlow.(FunctionalFlowExecutionAdapter)
		events, _ := adapter.Journal.Events(context.Background(), view.Handle)
		primary, _ := adapter.Publisher.(*LocalFunctionalFlowPublisher).loadPrimary(view.Handle)
		t.Fatalf("terminal=%#v calls=%d prepare=%d cleanup=%d primary=%#v events=%#v err=%v", terminal, executor.callCount(), fixtures.prepareCalls, fixtures.cleanupCalls, primary, events, err)
	}
	result, err := service.Result(context.Background(), view.Handle)
	if err != nil || result.Package == nil || loadFunctionalPackage(t, result.Package).Primary.Outcome != functionalflow.OutcomeError {
		t.Fatalf("result=%#v err=%v", result, err)
	}
}

// TestCancelResumesPendingFlowToACancelledPrimary proves cancellation cannot skip primary recovery and jump to cleanup.
func TestCancelResumesPendingFlowToACancelledPrimary(t *testing.T) {
	service, executor, fixtures, _ := newRecoveringFlowService(t, &functionalflow.Error{Category: functionalflow.CategoryIntegrity, Path: "evidence"})
	command := validCommand()
	command.IdempotencyKey = "cancel-pending-flow"
	view, err := service.Start(context.Background(), command)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = service.Decide(context.Background(), DecisionCommand{Principal: "user-1", Handle: view.Handle, Revision: view.ApprovalRevision, DecisionID: "cancel-pending-decision", Action: DecisionApprove}); !IsRecoverableFunctionalFlowError(err) {
		t.Fatalf("expected pending execution, got %v", err)
	}
	terminal, err := service.Cancel(context.Background(), "user-1", view.Handle)
	if err != nil || terminal.Outcome == nil || *terminal.Outcome != OutcomeCancelled || executor.callCount() != 2 || executor.effectCount() != 1 || fixtures.prepareCalls != 1 || fixtures.cleanupCalls != 1 {
		t.Fatalf("terminal=%#v calls=%d effects=%d fixtures=%d/%d err=%v", terminal, executor.callCount(), executor.effectCount(), fixtures.prepareCalls, fixtures.cleanupCalls, err)
	}
	result, err := service.Result(context.Background(), view.Handle)
	if err != nil || result.Package == nil || loadFunctionalPackage(t, result.Package).Primary.Outcome != functionalflow.OutcomeCancelled {
		t.Fatalf("result=%#v err=%v", result, err)
	}
}

// TestFunctionalRuntimeErrorClassificationClosesRecoveryVocabulary proves only durable runtime interruptions defer cleanup.
func TestFunctionalRuntimeErrorClassificationClosesRecoveryVocabulary(t *testing.T) {
	recoverable := []error{
		context.Canceled,
		context.DeadlineExceeded,
		&functionalflow.Error{Category: functionalflow.CategoryUncertain, Path: "tool"},
		&functionalflow.Error{Category: functionalflow.CategoryCancelled, Path: "run"},
		&functionalflow.Error{Category: functionalflow.CategoryInternal, Path: "retry.waiter"},
		&functionalflow.Error{Category: functionalflow.CategoryAuthority, Path: "state.claim_renew"},
		&functionalflow.Error{Category: functionalflow.CategoryIntegrity, Path: "state.confirm"},
		&functionalflow.Error{Category: functionalflow.CategoryIntegrity, Path: "intent.progress"},
		&functionalflow.Error{Category: functionalflow.CategoryIntegrity, Path: "journal.append"},
	}
	for _, err := range recoverable {
		if !recoverableFunctionalRuntimeError(err) {
			t.Fatalf("recoverable error rejected: %v", err)
		}
	}
	terminal := []error{
		errors.New("foreign"),
		&functionalflow.Error{Category: functionalflow.CategoryAuthority, Path: "artifact.binding"},
		&functionalflow.Error{Category: functionalflow.CategoryIntegrity, Path: "artifact.digest"},
		&functionalflow.Error{Category: functionalflow.CategoryInvalid, Path: "flow"},
	}
	for _, err := range terminal {
		if recoverableFunctionalRuntimeError(err) {
			t.Fatalf("terminal error deferred: %v", err)
		}
	}
	if appendTerminalFailureEvents(context.Background(), nil, functionalflow.Artifact{}, functionalflow.Result{}) != nil {
		t.Fatal("nil journal rejected an empty terminal append")
	}
	if IsRecoverableFunctionalFlowError(errors.New("foreign")) {
		t.Fatal("foreign error acquired recovery authority")
	}
}

// TestCompletedPrimaryCrashWindowsPromoteWithoutExecutorReplay proves pre/post Stage crashes use authenticated promotion only.
func TestCompletedPrimaryCrashWindowsPromoteWithoutExecutorReplay(t *testing.T) {
	for _, mode := range []string{"before", "after"} {
		t.Run(mode+"-stage", func(t *testing.T) {
			harness := newCheckpointFlowHarness(t, mode, "")
			command := validCommand()
			command.IdempotencyKey = "primary-crash-" + mode
			view, err := harness.service.Start(context.Background(), command)
			if err != nil {
				t.Fatal(err)
			}
			_, err = harness.service.Decide(context.Background(), DecisionCommand{Principal: "user-1", Handle: view.Handle, Revision: view.ApprovalRevision, DecisionID: "primary-decision-" + mode, Action: DecisionApprove})
			if err == nil {
				t.Fatal("stage crash did not interrupt workflow")
			}
			pending, loadErr := harness.service.deps.Store.Load(context.Background(), view.Handle)
			before, journalErr := harness.journal.Events(context.Background(), view.Handle)
			if loadErr != nil || journalErr != nil || pending.Phase != PhaseExecuting || !pending.FunctionalFlowExecutionPending || pending.FunctionalFlowPrimaryDurable || pending.CleanupPending || pending.Package != nil || harness.tool.callCount() != 1 {
				t.Fatalf("pending=%#v events=%#v tool=%d load=%v journal=%v", pending, before, harness.tool.callCount(), loadErr, journalErr)
			}
			if countFlowEvents(before, "step_terminal") != 1 || countFlowEvents(before, "recovery_resumed") != 0 || countFlowEvents(before, "primary_terminal") != 0 {
				t.Fatalf("pre-recovery journal=%#v", before)
			}
			harness.clock.advance(2 * time.Minute)
			first, second := harness.restart(t, "checkpoint-owner-a"), harness.restart(t, "checkpoint-owner-b")
			errorsSeen := make(chan error, 2)
			var group sync.WaitGroup
			for _, service := range []*Service{first, second} {
				group.Add(1)
				go func(candidate *Service) {
					defer group.Done()
					errorsSeen <- candidate.Recover(context.Background())
				}(service)
			}
			group.Wait()
			close(errorsSeen)
			for recoverErr := range errorsSeen {
				if recoverErr != nil && !IsCode(recoverErr, CodeConflict) {
					t.Fatal(recoverErr)
				}
			}
			result, err := first.Result(context.Background(), view.Handle)
			if err != nil || result.Package == nil || result.View.Outcome == nil || *result.View.Outcome != OutcomePassed || harness.tool.callCount() != 1 || harness.fixtures.prepareCalls != 1 || harness.fixtures.cleanupCalls != 1 {
				t.Fatalf("result=%#v tool=%d fixtures=%d/%d err=%v", result, harness.tool.callCount(), harness.fixtures.prepareCalls, harness.fixtures.cleanupCalls, err)
			}
			executions, recoveries := harness.executor.counts()
			after, err := harness.journal.Events(context.Background(), view.Handle)
			if err != nil || executions != 1 || recoveries < 2 || len(after) != len(before)+2 || countFlowEvents(after, "step_terminal") != 1 || countFlowEvents(after, "recovery_resumed") != 0 || countFlowEvents(after, "primary_terminal") != 1 || countFlowEvents(after, "cleanup_terminal") != 1 {
				t.Fatalf("execute=%d recover=%d before=%d after=%#v err=%v", executions, recoveries, len(before), after, err)
			}
			pkg := loadFunctionalPackage(t, result.Package)
			if pkg.Primary.Outcome != functionalflow.OutcomePassed {
				t.Fatalf("package=%#v", pkg)
			}
			assertWorkflowResultParity(t, first, view.Handle, result)
		})
	}
}

// TestPartialPrimaryCrashResumesRealExecutor proves an incomplete journal re-enters the scheduler but executes the tool once.
func TestPartialPrimaryCrashResumesRealExecutor(t *testing.T) {
	harness := newCheckpointFlowHarness(t, "", "step_claimed")
	command := validCommand()
	command.IdempotencyKey = "partial-primary-crash"
	view, err := harness.service.Start(context.Background(), command)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = harness.service.Decide(context.Background(), DecisionCommand{Principal: "user-1", Handle: view.Handle, Revision: view.ApprovalRevision, DecisionID: "partial-primary-decision", Action: DecisionApprove}); !IsRecoverableFunctionalFlowError(err) {
		t.Fatalf("partial crash was not recoverable: %v", err)
	}
	if harness.tool.callCount() != 0 {
		t.Fatalf("tool crossed before durable claim event: %d", harness.tool.callCount())
	}
	harness.clock.advance(2 * time.Minute)
	restarted := harness.restart(t, "partial-owner")
	if err = restarted.Recover(context.Background()); err != nil {
		t.Fatal(err)
	}
	result, err := restarted.Result(context.Background(), view.Handle)
	executions, _ := harness.executor.counts()
	if err != nil || result.Package == nil || result.View.Outcome == nil || *result.View.Outcome != OutcomePassed || executions != 2 || harness.tool.callCount() != 1 {
		t.Fatalf("result=%#v executions=%d tools=%d err=%v", result, executions, harness.tool.callCount(), err)
	}
	_ = loadFunctionalPackage(t, result.Package)
}

// TestCompletedJournalReconcilesMissingStateWithoutExecutorReplay proves journal authority promotes a lost terminal projection by CAS.
func TestCompletedJournalReconcilesMissingStateWithoutExecutorReplay(t *testing.T) {
	harness := newCheckpointFlowHarness(t, "before", "", true)
	command := validCommand()
	command.IdempotencyKey = "reconcile-primary-state"
	view, err := harness.service.Start(context.Background(), command)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = harness.service.Decide(context.Background(), DecisionCommand{Principal: "user-1", Handle: view.Handle, Revision: view.ApprovalRevision, DecisionID: "reconcile-primary-decision", Action: DecisionApprove}); err == nil {
		t.Fatal("stage crash did not interrupt workflow")
	}
	harness.clock.advance(2 * time.Minute)
	restarted := harness.restart(t, "reconcile-owner")
	if err = restarted.Recover(context.Background()); err != nil {
		t.Fatal(err)
	}
	result, err := restarted.Result(context.Background(), view.Handle)
	executions, _ := harness.executor.counts()
	if err != nil || result.Package == nil || executions != 1 || harness.tool.callCount() != 1 || harness.reconciler == nil || harness.reconciler.reconcileCount() != 1 {
		t.Fatalf("result=%#v execute=%d tool=%d reconciles=%d err=%v", result, executions, harness.tool.callCount(), harness.reconciler.reconcileCount(), err)
	}
	_ = loadFunctionalPackage(t, result.Package)
}

// TestCompletedJournalTamperingFailsClosedBeforeStageOrCleanup proves corrupted proof cannot be promoted or retried as business I/O.
func TestCompletedJournalTamperingFailsClosedBeforeStageOrCleanup(t *testing.T) {
	harness := newCheckpointFlowHarness(t, "before", "")
	command := validCommand()
	command.IdempotencyKey = "tampered-primary-journal"
	view, err := harness.service.Start(context.Background(), command)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = harness.service.Decide(context.Background(), DecisionCommand{Principal: "user-1", Handle: view.Handle, Revision: view.ApprovalRevision, DecisionID: "tampered-primary-decision", Action: DecisionApprove}); err == nil {
		t.Fatal("stage crash did not interrupt workflow")
	}
	statePath := filepath.Join(harness.journalRoot, view.Handle+".flow.json")
	raw, err := os.ReadFile(statePath)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(statePath, bytes.Replace(raw, []byte(`"step_terminal"`), []byte(`"step_tampered"`), 1), 0o600); err != nil {
		t.Fatal(err)
	}
	harness.clock.advance(2 * time.Minute)
	restarted := harness.restart(t, "tampered-owner")
	if err = restarted.Recover(context.Background()); err == nil {
		t.Fatal("tampered journal was promoted")
	}
	pending, loadErr := harness.service.deps.Store.Load(context.Background(), view.Handle)
	executions, _ := harness.executor.counts()
	if loadErr != nil || !pending.FunctionalFlowExecutionPending || pending.FunctionalFlowPrimaryDurable || pending.CleanupPending || pending.Package != nil || executions != 1 || harness.tool.callCount() != 1 || harness.fixtures.cleanupCalls != 0 {
		t.Fatalf("pending=%#v execute=%d tools=%d cleanup=%d load=%v recover=%v", pending, executions, harness.tool.callCount(), harness.fixtures.cleanupCalls, loadErr, err)
	}
}

// TestAuthenticatedSemanticDivergenceFailsBeforeStage proves MAC-valid journal/evidence forks never unlock publication or cleanup.
func TestAuthenticatedSemanticDivergenceFailsBeforeStage(t *testing.T) {
	type scenario struct {
		name           string
		mutateEvent    func(*functionalflow.Event)
		mutateEvidence func(*functionalflow.Evidence)
	}
	scenarios := []scenario{
		{name: "evidence-id", mutateEvent: func(event *functionalflow.Event) {
			if event.Type == "evidence_confirmed" {
				event.EvidenceIDs = []string{"foreign-evidence"}
			}
		}},
		{name: "assertion-reference", mutateEvent: func(event *functionalflow.Event) {
			if event.Type == "assertions_confirmed" {
				event.EvidenceIDs = []string{"foreign-evidence"}
			}
		}},
		{name: "record-digest", mutateEvent: func(event *functionalflow.Event) {
			if event.Type == "evidence_confirmed" {
				event.RecordDigest = strings.Repeat("f", 64)
			}
		}},
		{name: "terminal-state", mutateEvent: func(event *functionalflow.Event) {
			if event.Type == "step_terminal" {
				event.Status = string(planning.StepFailed)
			}
		}},
		{name: "evidence-binding", mutateEvidence: func(item *functionalflow.Evidence) { item.BindingDigest = strings.Repeat("e", 64) }},
		{name: "evidence-content", mutateEvidence: func(item *functionalflow.Evidence) { item.Output = map[string]any{"status": 201} }},
		{name: "evidence-step", mutateEvidence: func(item *functionalflow.Evidence) { item.StepID = "foreign-step" }},
		{name: "evidence-revision", mutateEvidence: func(item *functionalflow.Evidence) { item.Revision++ }},
	}
	for _, item := range scenarios {
		t.Run(item.name, func(t *testing.T) {
			harness := newCheckpointFlowHarness(t, "before", "")
			command := validCommand()
			command.IdempotencyKey = "semantic-divergence-" + item.name
			view, err := harness.service.Start(context.Background(), command)
			if err != nil {
				t.Fatal(err)
			}
			if _, err = harness.service.Decide(context.Background(), DecisionCommand{Principal: "user-1", Handle: view.Handle, Revision: view.ApprovalRevision, DecisionID: "semantic-decision-" + item.name, Action: DecisionApprove}); err == nil {
				t.Fatal("stage crash did not create the completed-primary recovery window")
			}
			journal := functionalflow.EventJournal(harness.journal)
			evidence := functionalflow.EvidenceStore(harness.evidence)
			if item.mutateEvent != nil {
				journal = rechainCheckpointJournal(t, harness, view.Handle, item.mutateEvent)
			}
			if item.mutateEvidence != nil {
				evidence = mutateCheckpointEvidence(t, harness, item.mutateEvidence)
			}
			harness.installRecoveryAuthorities(journal, evidence)
			harness.clock.advance(2 * time.Minute)
			restarted := harness.restart(t, "semantic-owner")
			if err = restarted.Recover(context.Background()); err == nil {
				t.Fatal("authenticated semantic divergence was promoted")
			}
			pending, loadErr := harness.service.deps.Store.Load(context.Background(), view.Handle)
			executions, _ := harness.executor.counts()
			attempts, writes := harness.publisher.counts()
			if loadErr != nil || !pending.FunctionalFlowExecutionPending || pending.FunctionalFlowPrimaryDurable || pending.CleanupPending || pending.Package != nil || executions != 1 || harness.tool.callCount() != 1 || attempts != 1 || writes != 0 || harness.fixtures.cleanupCalls != 0 {
				t.Fatalf("pending=%#v execute=%d tool=%d stage=%d/%d cleanup=%d load=%v recover=%v", pending, executions, harness.tool.callCount(), attempts, writes, harness.fixtures.cleanupCalls, loadErr, err)
			}
		})
	}
}

// countFlowEvents counts one authenticated event type without interpreting payloads.
func countFlowEvents(events []functionalflow.Event, eventType string) int {
	count := 0
	for _, event := range events {
		if event.Type == eventType {
			count++
		}
	}
	return count
}

// assertWorkflowResultParity proves API, CLI, MCP and GitHub read the same final package after primary promotion.
func assertWorkflowResultParity(t *testing.T, service *Service, handle string, expected WorkflowResult) {
	t.Helper()
	cli := CLI{Service: service, Principal: "user-1"}
	var output strings.Builder
	if cli.Run(context.Background(), []string{"result", handle}, &output) != 0 {
		t.Fatal(output.String())
	}
	var cliResult WorkflowResult
	if json.Unmarshal([]byte(output.String()), &cliResult) != nil {
		t.Fatal(output.String())
	}
	mcp := (MCPServer{Service: service, Principal: "user-1"}).Handle(context.Background(), MCPRequest{Version: ContractVersion, Operation: "workflow.result", Arguments: json.RawMessage(`{"handle":"` + handle + `"}`)})
	mcpResult, ok := mcp.Value.(WorkflowResult)
	github, githubErr := (GitHubGateway{Service: service, Principal: "user-1"}).Result(context.Background(), handle)
	if mcp.Error != "" || !ok || githubErr != nil || expected.Package == nil || cliResult.Package == nil || mcpResult.Package == nil || github.Package == nil || cliResult.Package.PackageDir != expected.Package.PackageDir || mcpResult.Package.PackageDir != expected.Package.PackageDir || github.Package.PackageDir != expected.Package.PackageDir {
		t.Fatalf("api=%#v cli=%#v mcp=%#v github=%#v githubErr=%v", expected, cliResult, mcp, github, githubErr)
	}
}

// TestFunctionalFlowParityAcrossCLIAPIMCPAndGitHub proves every surface applies the same verified package projector.
func TestFunctionalFlowParityAcrossCLIAPIMCPAndGitHub(t *testing.T) {
	validated, artifact := workflowFlowFixture(t)
	steps := []functionalflow.StepRecord{{RunID: "run", Revision: 1, StepID: "observe", Status: planning.StepInconclusive}}
	constraints, outcome := (functionalflow.Ledger{}).Aggregate(validated.Flow(), steps, false)
	result := functionalflow.Result{FlowID: artifact.FlowID, Revision: artifact.Revision, Steps: steps, Constraints: constraints, Outcome: outcome, ArtifactDigest: artifact.ArtifactDigest}
	events := functionalflow.BuildLifecycleEvents("run", artifact, result)
	pkg, err := functionalflow.BuildPackage("run", artifact, []functionalflow.EvidenceRecord{}, events, result, fixture.FixtureSummary{}, fixture.CleanupReport{Status: fixture.CleanupPartial})
	if err != nil {
		t.Fatal(err)
	}
	views := make([]FunctionalFlowProjection, 4)
	views[0], err = (CLI{}).FunctionalFlowResult(pkg)
	if err != nil {
		t.Fatal(err)
	}
	views[1], err = (FunctionalFlowAPI{}).Result(pkg)
	if err != nil {
		t.Fatal(err)
	}
	views[2], err = (MCPServer{}).FunctionalFlowResult(pkg)
	if err != nil {
		t.Fatal(err)
	}
	views[3], err = (GitHubGateway{}).FunctionalFlowResult(pkg)
	if err != nil {
		t.Fatal(err)
	}
	for i := 1; i < len(views); i++ {
		if views[i].Primary.ArtifactDigest != views[0].Primary.ArtifactDigest || views[i].Cleanup.Status != views[0].Cleanup.Status {
			t.Fatal("surface drift")
		}
	}
	for _, raw := range [][]byte{[]byte(`{"manifest_path":"secret"}`), []byte(`{"alias":"a","artifact_id":"b"}`), []byte(`{"alias":"a","alias":"b"}`), []byte(`{"alias":"a"}{}`)} {
		if _, err := DecodeRemoteFlowSelection(raw, 1024); err == nil {
			t.Fatalf("accepted remote authority %s", raw)
		}
	}
	if selection, err := DecodeRemoteFlowSelection([]byte(`{"alias":"approved"}`), 1024); err != nil || selection.Alias != "approved" {
		t.Fatalf("selection=%#v err=%v", selection, err)
	}
}

// TestFunctionalFlowVerticalParityAcrossRealWorkflowSurfaces proves every adapter starts, approves, observes, and returns the same shared workflow lifecycle.
func TestFunctionalFlowVerticalParityAcrossRealWorkflowSurfaces(t *testing.T) {
	const artifactID = "approved-flow"
	approve := func(t *testing.T, service *Service, view WorkflowView, decisionID string) WorkflowResult {
		t.Helper()
		terminal, err := service.Decide(context.Background(), DecisionCommand{Principal: "user-1", Handle: view.Handle, Revision: view.ApprovalRevision, DecisionID: decisionID, Action: DecisionApprove})
		if err != nil || terminal.Outcome == nil || *terminal.Outcome != OutcomePassed {
			t.Fatalf("terminal=%#v err=%v", terminal, err)
		}
		result, err := service.Result(context.Background(), view.Handle)
		if err != nil || result.Package == nil || result.Package.Outcome != OutcomePassed {
			t.Fatalf("result=%#v err=%v", result, err)
		}
		return result
	}
	newSurface := func(t *testing.T) (*Service, *flowExecutionPortFake, *workflowMemoryObserver) {
		t.Helper()
		service, _, _, _, _ := workflowFixture(t, validPlanResponse())
		flow := &flowExecutionPortFake{}
		binding := projectmemory.MemoryBinding{Version: projectmemory.ContractVersion, ProjectID: "project-1", RepositoryID: "repo-1", CommitOID: strings.Repeat("a", 40), QueryDigest: strings.Repeat("b", 64), Digest: strings.Repeat("c", 64)}
		authority := projectmemory.QueryAuthority{ProjectID: "project-1", Scope: projectmemory.Scope{Kind: projectmemory.ScopeRevision, RepositoryID: "repo-1", CommitOID: strings.Repeat("a", 40)}}
		memory := &workflowMemoryPort{valid: true, selection: ProjectMemorySelection{Binding: binding, Authority: authority}}
		observer := &workflowMemoryObserver{}
		service.deps.FunctionalFlow = flow
		service.deps.Memory, service.deps.MemoryObserver = memory, observer
		return service, flow, observer
	}
	results := make([]WorkflowResult, 0, 4)
	t.Run("api", func(t *testing.T) {
		service, flow, observer := newSurface(t)
		command := validCommand()
		command.IdempotencyKey = "flow-api"
		command.FunctionalFlow = &FunctionalFlowSelection{ArtifactID: artifactID}
		view, err := service.Start(context.Background(), command)
		if err != nil {
			t.Fatal(err)
		}
		if status, err := service.Status(context.Background(), view.Handle); err != nil || status.Phase != PhaseAwaitingApproval {
			t.Fatalf("status=%#v err=%v", status, err)
		}
		results = append(results, approve(t, service, view, "flow-api-decision"))
		if len(flow.selections) != 1 || flow.selections[0].ArtifactID != artifactID {
			t.Fatalf("selection=%#v", flow.selections)
		}
		if observer.calls != 1 {
			t.Fatalf("terminal memory observations=%d", observer.calls)
		}
	})
	t.Run("cli", func(t *testing.T) {
		service, flow, observer := newSurface(t)
		direct := validCommand().Target.Direct
		cli := CLI{Service: service, Principal: "user-1", AuthProfiles: map[string]string{"profile": strings.Repeat("8", 64)}, DirectTargets: map[string]DirectTarget{"target-1": *direct}}
		args := []string{"start", "--spec", "spec-1", "--contract", "contract-1", "--provider", "fake", "--model", "selected", "--profile", "profile", "--runtime-profile", "runtime-1", "--repository", "repo-1", "--revision", strings.Repeat("a", 40), "--approval", "required", "--idempotency-key", "flow-cli", "--target", "target-1", "--flow-artifact", artifactID}
		var output strings.Builder
		if code := cli.Run(context.Background(), args, &output); code != 0 {
			t.Fatalf("code=%d output=%s", code, output.String())
		}
		var view WorkflowView
		if json.Unmarshal([]byte(output.String()), &view) != nil {
			t.Fatal(output.String())
		}
		results = append(results, approve(t, service, view, "flow-cli-decision"))
		output.Reset()
		if code := cli.Run(context.Background(), []string{"result", view.Handle}, &output); code != 0 {
			t.Fatalf("result code=%d", code)
		}
		if len(flow.selections) != 1 || flow.selections[0].ArtifactID != artifactID {
			t.Fatalf("selection=%#v", flow.selections)
		}
		if observer.calls != 1 {
			t.Fatalf("terminal memory observations=%d", observer.calls)
		}
	})
	t.Run("mcp", func(t *testing.T) {
		service, flow, observer := newSurface(t)
		direct := validCommand().Target.Direct
		server := MCPServer{Service: service, Principal: "user-1", AuthProfiles: map[string]string{"profile": strings.Repeat("8", 64)}, DirectTargets: map[string]DirectTarget{"target-1": *direct}, FlowAliases: map[string]string{"approved": artifactID}}
		arguments := `{"idempotency_key":"flow-mcp","specification_ref":"spec-1","contract_ref":"contract-1","provider":"fake","model":"selected","auth_profile":"profile","runtime_profile":"runtime-1","repository_id":"repo-1","revision":"` + strings.Repeat("a", 40) + `","approval":"required","target_alias":"target-1","flow_alias":"approved"}`
		response := server.Handle(context.Background(), MCPRequest{Version: ContractVersion, Operation: "workflow.start", Arguments: json.RawMessage(arguments)})
		view, ok := response.Value.(WorkflowView)
		if response.Error != "" || !ok {
			t.Fatalf("response=%#v", response)
		}
		results = append(results, approve(t, service, view, "flow-mcp-decision"))
		resultResponse := server.Handle(context.Background(), MCPRequest{Version: ContractVersion, Operation: "workflow.result", Arguments: json.RawMessage(`{"handle":"` + view.Handle + `"}`)})
		if resultResponse.Error != "" {
			t.Fatalf("result=%#v", resultResponse)
		}
		if len(flow.selections) != 1 || flow.selections[0].ArtifactID != artifactID {
			t.Fatalf("selection=%#v", flow.selections)
		}
		if observer.calls != 1 {
			t.Fatalf("terminal memory observations=%d", observer.calls)
		}
	})
	t.Run("github", func(t *testing.T) {
		service, flow, observer := newSurface(t)
		command := validCommand()
		command.IdempotencyKey = "template"
		command.FunctionalFlow = &FunctionalFlowSelection{ArtifactID: artifactID}
		gateway := GitHubGateway{Service: service, Principal: "user-1", Commands: map[string]StartCommand{"approved": command}}
		snapshot, err := githubpr.SealSnapshot(githubpr.PullRequestSnapshot{Version: 1, InstallationID: 1, RepositoryID: 7, Number: 2, AuthorID: 9, State: "open", HeadOID: strings.Repeat("a", 40), HeadRepositoryID: 7})
		if err != nil {
			t.Fatal(err)
		}
		state, err := gateway.Submit(context.Background(), githubpr.Selection{Alias: "approved"}, snapshot, "flow-github")
		if err != nil {
			t.Fatal(err)
		}
		view, err := service.Status(context.Background(), state.RunHandle)
		if err != nil {
			t.Fatal(err)
		}
		results = append(results, approve(t, service, view, "flow-github-decision"))
		status, err := gateway.Status(context.Background(), view.Handle)
		if err != nil || status.Package != "package-safe" || status.Outcome != string(OutcomePassed) {
			t.Fatalf("status=%#v err=%v", status, err)
		}
		githubResult, resultErr := gateway.Result(context.Background(), view.Handle)
		if resultErr != nil || githubResult.Package == nil || githubResult.Package.Outcome != OutcomePassed {
			t.Fatalf("github result=%#v err=%v", githubResult, resultErr)
		}
		if len(flow.selections) != 1 || flow.selections[0].ArtifactID != artifactID {
			t.Fatalf("selection=%#v", flow.selections)
		}
		if observer.calls != 1 {
			t.Fatalf("terminal memory observations=%d", observer.calls)
		}
	})
	if len(results) != 4 {
		t.Fatalf("results=%d", len(results))
	}
	for _, result := range results[1:] {
		if result.View.Phase != results[0].View.Phase || result.Package.Outcome != results[0].Package.Outcome || result.Package.PackageDir != results[0].Package.PackageDir {
			t.Fatalf("surface drift %#v vs %#v", result, results[0])
		}
	}
}

// functionalFixtureStoreStub supplies one private fixture snapshot to the productive capture adapter.
type functionalFixtureStoreStub struct {
	state fixture.FixtureState
	err   error
}

// Load returns the configured private fixture snapshot without granting mutation authority.
func (s functionalFixtureStoreStub) Load(context.Context, string) (fixture.FixtureState, error) {
	return s.state, s.err
}

// Save is intentionally unavailable because flow input resolution is read-only.
func (functionalFixtureStoreStub) Save(context.Context, string, uint64, fixture.FixtureState) (fixture.FixtureState, error) {
	return fixture.FixtureState{}, errors.New("read-only fixture store")
}

// functionalRuntimeIDs assigns bounded evidence identifiers to direct runtime adapter tests.
type functionalRuntimeIDs struct{ next int }

// compositionHarnessTool satisfies the productive tool port without crossing I/O during composition tests.
type compositionHarnessTool struct{}

// Execute is unreachable because the test verifies only durable runtime wiring.
func (compositionHarnessTool) Execute(context.Context, harness.ToolCall) (harness.ToolResult, error) {
	return harness.ToolResult{}, errors.New("unexpected composition tool execution")
}

// compositionPlanningEvaluator satisfies the deterministic assertion port without evaluating during composition.
type compositionPlanningEvaluator struct{}

// Evaluate is unreachable until a composed workflow is explicitly executed.
func (compositionPlanningEvaluator) Evaluate(context.Context, planning.PlanStep, any, []string) (planning.StepEvaluation, error) {
	return planning.StepEvaluation{}, errors.New("unexpected composition assertion evaluation")
}

// compositionWaiter makes the local runtime's retry dependency explicit without sleeping.
type compositionWaiter struct{}

// Wait observes cancellation and otherwise completes immediately.
func (compositionWaiter) Wait(ctx context.Context, _ time.Duration) error { return ctx.Err() }

// rejectingIdentityAttemptSink simulates a durable audit boundary that refuses
// an authenticated attempt before any evidence can be returned.
type rejectingIdentityAttemptSink struct{}

// AppendIdentityAttempt rejects the bounded identity fact without retaining request material.
func (rejectingIdentityAttemptSink) AppendIdentityAttempt(context.Context, FunctionalIdentityAttempt) error {
	return errors.New("identity attempt journal unavailable")
}

// Next returns one deterministic safe identifier without depending on external randomness.
func (i *functionalRuntimeIDs) Next() string {
	i.next++
	return "flow-evidence-" + itoa(uint64(i.next))
}

// TestComposeLocalFunctionalFlowRuntimeOwnsEveryDefaultStore proves the local
// product path composes its catalog, artifacts, evidence, state and publisher.
func TestComposeLocalFunctionalFlowRuntimeOwnsEveryDefaultStore(t *testing.T) {
	ids := &functionalRuntimeIDs{}
	config := LocalFunctionalFlowRuntimeConfig{Root: t.TempDir(), PublicIdentity: "public", AllowedManifestRoots: []string{t.TempDir()}, Limits: functionalflow.Limits{MaxDocumentBytes: 1024, MaxSteps: 2, MaxAssertions: 2, MaxComparisons: 1, MaxReferences: 2, MaxDepth: 3, MaxResolvedBytes: 1024, MaxToolCalls: 2, MaxRetries: 1, MaxWork: 10, MaxRevisions: 1, StepTimeout: time.Second, RunTimeout: 2 * time.Second}, Tool: compositionHarnessTool{}, PolicyFactory: func() harness.Policy { return nil }, Evaluator: compositionPlanningEvaluator{}, Clock: planningClock{}, IDs: ids, Waiter: compositionWaiter{}, LeaseTTL: time.Minute, Execution: functionalflow.ExecutionConfig{Owner: "composition-owner", MaxCalls: 2, MaxRetries: 1, MaxWork: 10, StepTimeout: time.Second, RunTimeout: 2 * time.Second, ConfirmationTimeout: time.Second}}
	adapter, catalog, err := ComposeLocalFunctionalFlowRuntime(config)
	if err != nil || catalog == nil || adapter.Executor == nil || adapter.Artifacts == nil || adapter.Publisher == nil || adapter.Journal == nil {
		t.Fatalf("adapter=%#v catalog=%#v err=%v", adapter, catalog, err)
	}
	partialEvidence := config
	evidenceStore, openErr := OpenLocalFunctionalFlowEvidenceStore(filepath.Join(t.TempDir(), "evidence"))
	if openErr != nil {
		t.Fatal(openErr)
	}
	partialEvidence.Evidence = evidenceStore
	if _, _, err = ComposeLocalFunctionalFlowRuntime(partialEvidence); err == nil {
		t.Fatal("partial evidence persistence accepted")
	}
	manifestOnly := config
	manifestOnly.EvidenceManifest = evidenceStore
	if _, _, err = ComposeLocalFunctionalFlowRuntime(manifestOnly); err == nil {
		t.Fatal("manifest-only evidence persistence accepted")
	}
	partialState := config
	stateStore, stateErr := functionalflow.NewLocalStore(filepath.Join(t.TempDir(), "state"), planningClock{}, time.Minute)
	if stateErr != nil {
		t.Fatal(stateErr)
	}
	partialState.State = stateStore
	if _, _, err = ComposeLocalFunctionalFlowRuntime(partialState); err == nil {
		t.Fatal("partial execution persistence accepted")
	}
	journalOnly := config
	journalOnly.Journal = stateStore
	if _, _, err = ComposeLocalFunctionalFlowRuntime(journalOnly); err == nil {
		t.Fatal("journal-only execution persistence accepted")
	}
	blockedRoot := filepath.Join(t.TempDir(), "blocked-runtime-root")
	if err = os.WriteFile(blockedRoot, []byte("not-a-directory"), 0o600); err != nil {
		t.Fatal(err)
	}
	failing := config
	failing.Root = blockedRoot
	if _, _, err = ComposeLocalFunctionalFlowRuntime(failing); err == nil {
		t.Fatal("catalog open failure hidden")
	}
	failing.Catalog = catalog
	if _, _, err = ComposeLocalFunctionalFlowRuntime(failing); err == nil {
		t.Fatal("artifact open failure hidden")
	}
	failing.Artifacts = adapter.Artifacts
	if _, _, err = ComposeLocalFunctionalFlowRuntime(failing); err == nil {
		t.Fatal("evidence open failure hidden")
	}
	failing.Evidence, failing.EvidenceManifest = evidenceStore, evidenceStore
	if _, _, err = ComposeLocalFunctionalFlowRuntime(failing); err == nil {
		t.Fatal("intent-store open failure hidden")
	}
	failing.State, failing.Journal = stateStore, stateStore
	if _, _, err = ComposeLocalFunctionalFlowRuntime(failing); err == nil {
		t.Fatal("publisher open failure hidden")
	}
	failing.Publisher = adapter.Publisher
	if _, _, err = ComposeLocalFunctionalFlowRuntime(failing); err == nil {
		t.Fatal("attempt journal open failure hidden")
	}
}

// providedFlowCatalogStub injects one bounded provided-flow load result into composer boundary tests.
type providedFlowCatalogStub struct {
	document []byte
	err      error
}

// TestCanonicalComposerRejectsEveryProvidedBoundary proves catalog, decoding, validation, and binding failures stay distinct and closed.
func TestCanonicalComposerRejectsEveryProvidedBoundary(t *testing.T) {
	validated, _ := workflowFlowFixture(t)
	flow := validated.Flow()
	flow.Source = functionalflow.SourceProvided
	document, err := json.Marshal(flow)
	if err != nil {
		t.Fatal(err)
	}
	selection := &FunctionalFlowSelection{ArtifactID: "approved"}
	request := ExecutionRequest{FunctionalFlow: selection}
	limits := functionalflow.Limits{MaxDocumentBytes: 1024, MaxSteps: 3, MaxAssertions: 3, MaxComparisons: 3, MaxReferences: 3, MaxDepth: 3, MaxResolvedBytes: 1024, MaxToolCalls: 3, MaxRetries: 1, MaxWork: 30, MaxRevisions: 2, StepTimeout: time.Second, RunTimeout: time.Second}
	cases := []CanonicalFunctionalFlowComposer{
		{Limits: limits},
		{Limits: limits, Provided: providedFlowCatalogStub{err: errors.New("unavailable")}},
		{Limits: limits, Provided: providedFlowCatalogStub{document: []byte(`{"bad":true}`)}},
		{Limits: limits, Provided: providedFlowCatalogStub{document: document}},
		{Limits: limits, Provided: providedFlowCatalogStub{document: document}, Validation: functionalflow.ValidationAuthority{Constraints: map[string]bool{"c": true}}},
	}
	for i, composer := range cases {
		if _, _, composeErr := composer.Compose(context.Background(), request, agentplanning.PreparedSnapshot{}); composeErr == nil {
			t.Fatalf("composer boundary %d was accepted", i)
		}
	}
}

// appendFlowTestEvent seals one causal event through the same durable journal used by production composition.
func appendFlowTestEvent(t *testing.T, journal functionalflow.EventJournal, runID string, artifact functionalflow.Artifact, event functionalflow.Event) {
	t.Helper()
	event.RunID, event.FlowID, event.Revision, event.ArtifactDigest = runID, artifact.FlowID, artifact.Revision, artifact.ArtifactDigest
	if _, err := journal.AppendEvent(context.Background(), event); err != nil {
		t.Fatal(err)
	}
}

// Load returns the configured detached provided document or controlled catalog failure.
func (s providedFlowCatalogStub) Load(context.Context, FunctionalFlowSelection) ([]byte, error) {
	return append([]byte(nil), s.document...), s.err
}

// TestFunctionalRuntimePrivateBoundariesAndConservativeFinalization exercises private captures, evidence, audit, and pre-execution cancellation through real local adapters.
func TestFunctionalRuntimePrivateBoundariesAndConservativeFinalization(t *testing.T) {
	validated, artifact := workflowFlowFixture(t)
	digest := strings.Repeat("b", 64)
	capture := fixture.Capture{RunID: "run", FixtureID: "account", Name: "id", ValueType: "string", Value: "private-value", Bytes: 13, Digest: digest, BindingDigest: digest}
	fixtureAdapter := FunctionalFixtureCaptureAdapter{Store: functionalFixtureStoreStub{state: fixture.FixtureState{Captures: map[string]fixture.Capture{"account\x00id": capture}}}}
	value, resolvedDigest, err := fixtureAdapter.Resolve(context.Background(), "run", functionalflow.FixtureReference{FixtureID: "account", Output: "id", ValueType: planning.ValueString})
	if err != nil || value != "private-value" || resolvedDigest != digest {
		t.Fatalf("capture value=%v digest=%s err=%v", value, resolvedDigest, err)
	}
	if _, _, err = fixtureAdapter.Resolve(context.Background(), "run", functionalflow.FixtureReference{FixtureID: "missing", Output: "id"}); err == nil {
		t.Fatal("missing private capture was accepted")
	}
	if _, _, err = (FunctionalFixtureCaptureAdapter{}).Resolve(context.Background(), "run", functionalflow.FixtureReference{FixtureID: "account", Output: "id"}); err == nil {
		t.Fatal("nil private store was accepted")
	}

	evidenceStore, err := OpenLocalFunctionalFlowEvidenceStore(filepath.Join(t.TempDir(), "evidence"))
	if err != nil {
		t.Fatal(err)
	}
	item := functionalflow.SealEvidence(functionalflow.Evidence{ID: "evidence-1", RunID: "run", Revision: 1, StepID: "observe", BindingDigest: artifact.Steps[0].Operation.BindingDigest, ResourceBindingDigest: digest, Output: map[string]any{"status": json.Number("200")}, Terminal: true})
	confirmed, err := evidenceStore.Confirm(context.Background(), item)
	if err != nil || confirmed.Digest != item.Digest {
		t.Fatalf("confirmed=%#v err=%v", confirmed, err)
	}
	ledger := []functionalflow.LedgerEntry{{Revision: 1, StepID: "observe", ConstraintID: "c", Result: planning.AssertionResult{DeclarationID: "a", Status: planning.AssertionPassed}, EvidenceIDs: []string{item.ID}}}
	records, err := evidenceStore.Records(context.Background(), "run", functionalflow.Result{Ledger: ledger})
	if err != nil || len(records) != 1 || records[0].ContentDigest != item.Digest {
		t.Fatalf("records=%#v err=%v", records, err)
	}
	if _, err = evidenceStore.Load(context.Background(), "missing"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing evidence err=%v", err)
	}
	if err = os.WriteFile(filepath.Join(evidenceStore.root, item.ID+".json"), []byte(`{}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err = evidenceStore.Load(context.Background(), item.ID); !IsCode(err, CodeConflict) {
		t.Fatalf("tampered evidence err=%v", err)
	}

	artifactStore, err := OpenLocalFunctionalFlowArtifactStore(filepath.Join(t.TempDir(), "artifacts"))
	if err != nil {
		t.Fatal(err)
	}
	for _, handle := range []string{"cancelled-run", "error-run"} {
		if err = artifactStore.Commit(context.Background(), handle, artifact); err != nil {
			t.Fatal(err)
		}
	}
	causalJournal, _ := functionalflow.NewLocalStore(filepath.Join(t.TempDir(), "journal"), planningClock{}, time.Hour)
	publisher, err := OpenLocalFunctionalFlowPublisher(filepath.Join(t.TempDir(), "packages"), artifactStore, nil, causalJournal)
	if err != nil {
		t.Fatal(err)
	}
	for _, handle := range []string{"cancelled-run", "error-run"} {
		appendFlowTestEvent(t, causalJournal, handle, artifact, functionalflow.Event{Type: "flow_artifact_committed"})
		appendFlowTestEvent(t, causalJournal, handle, artifact, functionalflow.Event{Type: "fixtures_terminal", Status: string(fixture.PreparationRejected)})
	}
	cancelled, err := publisher.Finalize(context.Background(), "cancelled-run", OutcomeCancelled, fixture.FixtureSummary{PreparationStatus: fixture.PreparationRejected, Cleanup: fixture.CleanupReport{Status: fixture.CleanupNotRequired}})
	if err != nil || cancelled.Outcome != OutcomeCancelled {
		t.Fatalf("cancelled projection=%#v err=%v", cancelled, err)
	}
	failed, err := publisher.Finalize(context.Background(), "error-run", OutcomeError, fixture.FixtureSummary{PreparationStatus: fixture.PreparationRejected, Cleanup: fixture.CleanupReport{Status: fixture.CleanupFailed}})
	if err != nil || failed.Outcome != OutcomeError {
		t.Fatalf("error projection=%#v err=%v", failed, err)
	}
	if _, err = OpenLocalFunctionalFlowPublisher(t.TempDir(), nil, nil); err == nil {
		t.Fatal("publisher accepted missing artifact authority")
	}

	redaction, err := evidence.NewPolicy(evidence.Config{MaxBodyBytes: 1024})
	if err != nil {
		t.Fatal(err)
	}
	journalRoot := t.TempDir()
	tool := &tools.Fake{Results: []harness.ToolResult{{Output: map[string]any{"status": 200}}}}
	adapter := functionalFlowToolAdapter{tool: tool, sanitizer: evidence.NewSanitizer(redaction), ids: &functionalRuntimeIDs{}, journalRoot: journalRoot, journalMu: &sync.Mutex{}}
	response, err := adapter.Execute(context.Background(), functionalflow.ExecutionRequest{RunID: "run", Revision: 1, StepID: "observe", Operation: artifact.Steps[0].Operation, Input: map[string]any{}, Attempt: 1})
	if err != nil || response.EvidenceID == "" || functionalHTTPFailure(response.Output.(map[string]any)["status"]) {
		t.Fatalf("response=%#v err=%v", response, err)
	}
	journal, err := os.ReadFile(filepath.Join(journalRoot, "run", "events.jsonl"))
	if err != nil || !strings.Contains(string(journal), `"outcome_category":"success"`) || strings.Contains(string(journal), "private-value") {
		t.Fatalf("journal=%s err=%v", journal, err)
	}
	rejectedAuditAdapter := adapter
	rejectedAuditAdapter.tool = &tools.Fake{Results: []harness.ToolResult{{Output: map[string]any{"status": 200}}}}
	rejectedAuditAdapter.attemptSink = rejectingIdentityAttemptSink{}
	if _, err = rejectedAuditAdapter.Execute(context.Background(), functionalflow.ExecutionRequest{RunID: "audit-rejected", Revision: 1, StepID: "observe", Operation: artifact.Steps[0].Operation, Input: map[string]any{}, Attempt: 1}); err == nil {
		t.Fatal("failed identity attempt journal was ignored")
	}
	failedTool := &tools.Fake{Results: []harness.ToolResult{{Output: map[string]any{"error": map[string]any{"category": "identity_credential_unavailable"}}, ErrorClass: harness.ErrorPermanent}}, Errors: []error{&harness.OperationError{Class: harness.ErrorPermanent, Err: errors.New("external canary")}}}
	adapter.tool = failedTool
	if _, err = adapter.Execute(context.Background(), functionalflow.ExecutionRequest{RunID: "rejected", Revision: 1, StepID: "observe", Operation: artifact.Steps[0].Operation, Input: map[string]any{}, Attempt: 1}); err == nil {
		t.Fatal("credential failure was accepted")
	}
	if raw, readErr := os.ReadFile(filepath.Join(journalRoot, "rejected", "events.jsonl")); readErr != nil || !strings.Contains(string(raw), `"outcome_category":"credential_rejected"`) || strings.Contains(string(raw), "external canary") {
		t.Fatalf("rejection journal=%s err=%v", raw, readErr)
	}

	cancelledContext, cancel := context.WithCancel(context.Background())
	cancel()
	if functionalIdentityOutcome(cancelledContext, harness.ToolResult{}, context.Canceled) != "cancelled" || functionalIdentityOutcome(context.Background(), harness.ToolResult{ErrorClass: harness.ErrorTransient}, errors.New("x")) != "transient" || functionalIdentityOutcome(context.Background(), harness.ToolResult{ErrorClass: harness.ErrorPermanent}, errors.New("x")) != "permanent" {
		t.Fatal("identity audit vocabulary drift")
	}
	for _, status := range []any{500, int64(500), float64(500), json.Number("500")} {
		if !functionalHTTPFailure(status) {
			t.Fatalf("status %T was not classified", status)
		}
	}
	if functionalHTTPFailure(nil) || !functionalHTTPFailure(199) || functionalHTTPFailure(200) {
		t.Fatal("HTTP functional classification drift")
	}
	if key := (functionalFlowKeyFactory{}).Key("run", 1, "observe"); len(key) != 64 {
		t.Fatalf("key=%q", key)
	}
	public := artifact.Steps[0]
	public.Operation.Identity, err = testidentity.BindDescriptor(testidentity.Descriptor{ID: "public", Mechanism: testidentity.MechanismAnonymous, Operations: []testidentity.Operation{public.Operation.Identity.Operation}}, public.Operation.Identity.Operation)
	if err != nil {
		t.Fatal(err)
	}
	if err = (functionalFlowIdentityRevalidator{publicIdentity: "public"}).Revalidate(context.Background(), "run", public); err != nil {
		t.Fatal(err)
	}
	public.Operation.Identity.Operation.Path = "/drift"
	if err = (functionalFlowIdentityRevalidator{publicIdentity: "public"}).Revalidate(context.Background(), "run", public); err == nil {
		t.Fatal("public identity drift was accepted")
	}
	allowedPolicy := &policy.Static{Decisions: map[string]harness.PolicyDecision{"http.request": {Allowed: true}}}
	productPolicy := &functionalFlowProductPolicy{factory: func() harness.Policy { return allowedPolicy }, limits: functionalflow.ExecutionConfig{MaxCalls: 2, MaxRetries: 1, MaxWork: 2}}
	if decision, policyErr := productPolicy.Evaluate(context.Background(), "run", artifact.Steps[0]); policyErr != nil || decision != functionalflow.PolicyAllowed {
		t.Fatalf("policy=%s err=%v", decision, policyErr)
	}
	deniedPolicy := &functionalFlowProductPolicy{factory: func() harness.Policy { return nil }, limits: functionalflow.ExecutionConfig{MaxCalls: 1, MaxWork: 1}}
	if decision, policyErr := deniedPolicy.Evaluate(context.Background(), "run", artifact.Steps[0]); policyErr == nil || decision != functionalflow.PolicyDenied {
		t.Fatalf("denied policy=%s err=%v", decision, policyErr)
	}
	if _, _, err = ComposeLocalFunctionalFlowRuntime(LocalFunctionalFlowRuntimeConfig{}); err == nil {
		t.Fatal("runtime accepted missing trusted dependencies")
	}
	_ = validated
}

// TestFunctionalToolRejectsOperationDriftAndSeparatesConcreteResources proves sealed authority governs I/O and private selectors govern comparisons.
func TestFunctionalToolRejectsOperationDriftAndSeparatesConcreteResources(t *testing.T) {
	_, artifact := workflowFlowFixture(t)
	bound := artifact.Steps[0].Operation
	bound.Reference.Path = "/items/{id}"
	bound.Identity.Operation.Path = bound.Reference.Path
	bound.Identity, _ = testidentity.BindDescriptor(testidentity.Descriptor{ID: "public", Mechanism: testidentity.MechanismAnonymous, Operations: []testidentity.Operation{bound.Identity.Operation}}, bound.Identity.Operation)
	redaction, err := evidence.NewPolicy(evidence.Config{MaxBodyBytes: 1024})
	if err != nil {
		t.Fatal(err)
	}
	tool := &tools.Fake{Results: []harness.ToolResult{{Output: map[string]any{"status": 200}}, {Output: map[string]any{"status": 200}}}}
	adapter := functionalFlowToolAdapter{tool: tool, sanitizer: evidence.NewSanitizer(redaction), ids: &functionalRuntimeIDs{}, journalRoot: t.TempDir(), journalMu: &sync.Mutex{}}
	bad := functionalflow.ExecutionRequest{RunID: "run", Revision: 1, StepID: "observe", Operation: bound, Input: map[string]any{"method": "POST", "path": "/items/1"}, Attempt: 1}
	if _, err = adapter.Execute(context.Background(), bad); err == nil || tool.CallCount() != 0 {
		t.Fatalf("drift reached tool: calls=%d err=%v", tool.CallCount(), err)
	}
	first := bad
	first.Input = map[string]any{"method": bound.Reference.Method, "path": "/items/1", "query": map[string]any{"view": "one"}}
	firstResponse, err := adapter.Execute(context.Background(), first)
	if err != nil {
		t.Fatal(err)
	}
	second := first
	second.Input = map[string]any{"method": bound.Reference.Method, "path": "/items/2", "query": map[string]any{"view": "one"}}
	secondResponse, err := adapter.Execute(context.Background(), second)
	if err != nil {
		t.Fatal(err)
	}
	if firstResponse.ResourceBindingDigest == secondResponse.ResourceBindingDigest || tool.CallCount() != 2 {
		t.Fatalf("resource selectors aliased: %s %s", firstResponse.ResourceBindingDigest, secondResponse.ResourceBindingDigest)
	}
}

// TestFunctionalLocalAdaptersFailClosedAtPersistenceBoundaries verifies malformed coordinates, cancellation, corruption, and missing authority never reach execution.
func TestFunctionalLocalAdaptersFailClosedAtPersistenceBoundaries(t *testing.T) {
	validated, artifact := workflowFlowFixture(t)
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := OpenLocalFunctionalFlowArtifactStore(""); err == nil {
		t.Fatal("empty artifact root was accepted")
	}
	artifacts, err := OpenLocalFunctionalFlowArtifactStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err = artifacts.Commit(cancelled, "run", artifact); err == nil {
		t.Fatal("cancelled artifact commit was accepted")
	}
	if _, err = artifacts.Load(context.Background(), "missing"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing artifact err=%v", err)
	}
	if _, err = artifacts.Load(context.Background(), "bad/path"); err == nil {
		t.Fatal("unsafe artifact handle was accepted")
	}
	if err = artifacts.Commit(context.Background(), "run", artifact); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(artifacts.root, "run.flow.json"), []byte(`{}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err = artifacts.Load(context.Background(), "run"); !IsCode(err, CodeConflict) {
		t.Fatalf("corrupt artifact err=%v", err)
	}

	if _, err = OpenLocalFunctionalFlowEvidenceStore(""); err == nil {
		t.Fatal("empty evidence root was accepted")
	}
	evidenceStore, err := OpenLocalFunctionalFlowEvidenceStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	invalidEvidence := functionalflow.Evidence{ID: "evidence", RunID: "run", Revision: 1, StepID: "observe", BindingDigest: artifact.Steps[0].Operation.BindingDigest, ResourceBindingDigest: strings.Repeat("c", 64), Terminal: true}
	if _, err = evidenceStore.Confirm(context.Background(), invalidEvidence); err == nil {
		t.Fatal("unsealed evidence was accepted")
	}
	if _, err = evidenceStore.Load(cancelled, "evidence"); err == nil {
		t.Fatal("cancelled evidence load was accepted")
	}
	sealed := functionalflow.SealEvidence(invalidEvidence)
	if _, err = evidenceStore.Confirm(context.Background(), sealed); err != nil {
		t.Fatal(err)
	}
	if _, err = evidenceStore.Records(context.Background(), "foreign", functionalflow.Result{Ledger: []functionalflow.LedgerEntry{{EvidenceIDs: []string{sealed.ID}}}}); err == nil {
		t.Fatal("foreign evidence manifest was accepted")
	}

	limits := functionalflow.Limits{MaxDocumentBytes: 1024}
	if _, err = OpenLocalProvidedFlowCatalog("", []string{t.TempDir()}, nil, limits); err == nil {
		t.Fatal("empty catalog root was accepted")
	}
	if _, err = OpenLocalProvidedFlowCatalog(t.TempDir(), []string{t.TempDir()}, map[string]string{"bad/path": "artifact"}, limits); err == nil {
		t.Fatal("unsafe catalog alias was accepted")
	}
	allowed := t.TempDir()
	catalog, err := OpenLocalProvidedFlowCatalog(t.TempDir(), []string{allowed}, nil, limits)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = catalog.Resolve(context.Background(), "https://example.test/flow"); err == nil {
		t.Fatal("remote manifest path was accepted")
	}
	if _, err = catalog.Resolve(context.Background(), allowed); err == nil {
		t.Fatal("manifest directory was accepted")
	}
	invalidManifest := filepath.Join(allowed, "invalid.json")
	if err = os.WriteFile(invalidManifest, []byte(`{`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err = catalog.Resolve(context.Background(), invalidManifest); err == nil {
		t.Fatal("invalid manifest document was accepted")
	}
	oversizedManifest := filepath.Join(allowed, "oversized.json")
	if err = os.WriteFile(oversizedManifest, make([]byte, limits.MaxDocumentBytes+1), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err = catalog.Resolve(context.Background(), oversizedManifest); err == nil {
		t.Fatal("oversized manifest document was accepted")
	}
	if _, err = catalog.Resolve(cancelled, invalidManifest); err == nil {
		t.Fatal("cancelled manifest import was accepted")
	}
	if _, err = catalog.Load(context.Background(), FunctionalFlowSelection{}); err == nil {
		t.Fatal("empty remote selection was accepted")
	}
	if _, err = catalog.Load(context.Background(), FunctionalFlowSelection{ArtifactID: "missing"}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing provided artifact err=%v", err)
	}
	if _, err = catalog.Load(context.Background(), FunctionalFlowSelection{Alias: "unknown"}); err == nil {
		t.Fatal("unknown provided alias was accepted")
	}

	badComposer := CanonicalFunctionalFlowComposer{Limits: limits, Provided: providedFlowCatalogStub{err: errors.New("catalog unavailable")}}
	if _, _, err = badComposer.Compose(context.Background(), ExecutionRequest{FunctionalFlow: &FunctionalFlowSelection{ArtifactID: "approved"}}, agentplanning.PreparedSnapshot{}); !IsCode(err, CodeUnauthorized) {
		t.Fatalf("catalog failure err=%v", err)
	}
	badComposer.Provided = nil
	if _, _, err = badComposer.Compose(context.Background(), ExecutionRequest{FunctionalFlow: &FunctionalFlowSelection{ArtifactID: "approved"}}, agentplanning.PreparedSnapshot{}); !IsCode(err, CodeInvalid) {
		t.Fatalf("missing catalog err=%v", err)
	}

	adapter := FunctionalFlowExecutionAdapter{}
	if _, err = adapter.Admit(context.Background(), ExecutionRequest{Handle: "run"}, agentplanning.PreparedSnapshot{}); err == nil {
		t.Fatal("admission accepted missing ports")
	}
	if _, err = adapter.Execute(context.Background(), ExecutionRequest{Handle: "run"}, agentplanning.PreparedSnapshot{}); err == nil {
		t.Fatal("execution accepted missing ports")
	}
	if _, err = adapter.Finalize(context.Background(), "run", OutcomePassed, fixture.FixtureSummary{}); err == nil {
		t.Fatal("finalization accepted missing publisher")
	}
	publisherArtifacts, err := OpenLocalFunctionalFlowArtifactStore(filepath.Join(t.TempDir(), "publisher-artifacts"))
	if err != nil {
		t.Fatal(err)
	}
	publisher, err := OpenLocalFunctionalFlowPublisher(filepath.Join(t.TempDir(), "publisher"), publisherArtifacts, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err = publisher.Stage(cancelled, "run", artifact, functionalflow.Result{ArtifactDigest: artifact.ArtifactDigest}); err == nil {
		t.Fatal("cancelled primary stage was accepted")
	}
	if _, err = publisher.Finalize(context.Background(), "missing", OutcomeInconclusive, fixture.FixtureSummary{}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing publisher artifact err=%v", err)
	}
	for primary, expected := range map[functionalflow.PrimaryOutcome]Outcome{functionalflow.OutcomePassed: OutcomePassed, functionalflow.OutcomeFailed: OutcomeFailed, functionalflow.OutcomeCancelled: OutcomeCancelled, functionalflow.OutcomeError: OutcomeError, functionalflow.OutcomeInconclusive: OutcomeInconclusive} {
		if workflowOutcome(primary) != expected {
			t.Fatalf("outcome %s mapped to %s", primary, workflowOutcome(primary))
		}
	}
	if _, err = ProjectFunctionalFlow(functionalflow.Package{}); err == nil {
		t.Fatal("invalid package projection was accepted")
	}
	_ = validated
}
