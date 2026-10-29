package validationworkflow

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"time"

	"github.com/asierhr/featureProof/internal/evidence"
	"github.com/asierhr/featureProof/internal/fixture"
	"github.com/asierhr/featureProof/internal/functionalflow"
	"github.com/asierhr/featureProof/internal/harness"
	"github.com/asierhr/featureProof/internal/planning"
	"github.com/asierhr/featureProof/internal/testidentity"
)

// LocalFunctionalFlowRuntimeConfig contains trusted local ports and finite authority required to compose the productive runtime.
type LocalFunctionalFlowRuntimeConfig struct {
	Root, PublicIdentity string
	AttemptJournalRoot   string
	AllowedManifestRoots []string
	Aliases              map[string]string
	Limits               functionalflow.Limits
	Validation           functionalflow.ValidationAuthority
	Binding              functionalflow.BindingAuthority
	Generated            functionalflow.GeneratedAuthority
	Execution            functionalflow.ExecutionConfig
	LeaseTTL             time.Duration
	Tool                 harness.Tool
	PolicyFactory        func() harness.Policy
	Sanitizer            evidence.Sanitizer
	Evaluator            planning.StepAssertionEvaluator
	Identities           *testidentity.Catalog
	Clock                functionalflow.Clock
	IDs                  harness.IDGenerator
	Waiter               functionalflow.Waiter
	Fixtures             functionalflow.FixtureCaptureStore
	State                functionalflow.IntentStore
	Journal              functionalflow.EventJournal
	Artifacts            FunctionalFlowArtifactStore
	Evidence             functionalflow.EvidenceStore
	EvidenceManifest     FunctionalFlowEvidenceManifestSource
	Publisher            FunctionalFlowPublisher
	Catalog              FunctionalFlowCatalog
	AttemptSink          FunctionalIdentityAttemptSink
}

// FunctionalFlowCatalog combines immutable document loading with CLI alias resolution without prescribing a filesystem backend.
type FunctionalFlowCatalog interface {
	ProvidedFlowCatalog
	LocalFlowManifestResolver
}

// FunctionalIdentityAttempt is the bounded credential-free fact emitted for one authenticated tool attempt.
type FunctionalIdentityAttempt struct {
	RunID, IdentityID, OperationBindingSHA256, OutcomeCategory string
	Attempt                                                    int
}

// FunctionalIdentityAttemptSink persists authenticated-attempt audit facts without exposing request, response, or credential material.
type FunctionalIdentityAttemptSink interface {
	AppendIdentityAttempt(context.Context, FunctionalIdentityAttempt) error
}

// ComposeLocalFunctionalFlowRuntime builds one shared canonical composer, durable executor, artifact store, evidence store, and post-cleanup publisher.
func ComposeLocalFunctionalFlowRuntime(config LocalFunctionalFlowRuntimeConfig) (FunctionalFlowExecutionAdapter, FunctionalFlowCatalog, error) {
	if config.Root == "" || config.Tool == nil || config.PolicyFactory == nil || config.Evaluator == nil || config.Clock == nil || config.IDs == nil || config.Waiter == nil || config.LeaseTTL <= 0 {
		return FunctionalFlowExecutionAdapter{}, nil, &Error{Code: CodeInvalid, Operation: "functional_flow_runtime"}
	}
	catalog := config.Catalog
	var err error
	if catalog == nil {
		catalog, err = OpenLocalProvidedFlowCatalog(filepath.Join(config.Root, "provided"), config.AllowedManifestRoots, config.Aliases, config.Limits)
		if err != nil {
			return FunctionalFlowExecutionAdapter{}, nil, err
		}
	}
	artifacts := config.Artifacts
	if artifacts == nil {
		artifacts, err = OpenLocalFunctionalFlowArtifactStore(filepath.Join(config.Root, "artifacts"))
		if err != nil {
			return FunctionalFlowExecutionAdapter{}, nil, err
		}
	}
	confirmed := config.Evidence
	manifest := config.EvidenceManifest
	if confirmed == nil || manifest == nil {
		if confirmed != nil || manifest != nil {
			return FunctionalFlowExecutionAdapter{}, nil, &Error{Code: CodeInvalid, Operation: "functional_flow_evidence_persistence"}
		}
		localEvidence, openErr := OpenLocalFunctionalFlowEvidenceStore(filepath.Join(config.Root, "evidence"))
		if openErr != nil {
			return FunctionalFlowExecutionAdapter{}, nil, openErr
		}
		confirmed, manifest = localEvidence, localEvidence
	}
	state, journal := config.State, config.Journal
	if (state == nil) != (journal == nil) {
		return FunctionalFlowExecutionAdapter{}, nil, &Error{Code: CodeInvalid, Operation: "functional_flow_persistence"}
	}
	if state == nil || journal == nil {
		localState, stateErr := functionalflow.NewLocalStore(filepath.Join(config.Root, "state"), config.Clock, config.LeaseTTL)
		if stateErr != nil {
			return FunctionalFlowExecutionAdapter{}, nil, stateErr
		}
		state, journal = localState, localState
	}
	publisher := config.Publisher
	if publisher == nil {
		publisher, err = OpenLocalFunctionalFlowPublisher(filepath.Join(config.Root, "packages"), artifacts, manifest, journal)
		if err != nil {
			return FunctionalFlowExecutionAdapter{}, nil, err
		}
	}
	generated := config.Generated
	generated.PublicIdentity = config.PublicIdentity
	journalRoot := ""
	if config.AttemptSink == nil {
		journalRoot = config.AttemptJournalRoot
		if journalRoot == "" {
			journalRoot = filepath.Join(config.Root, "attempts")
		}
		journalRoot, err = filepath.Abs(journalRoot)
		if err != nil || os.MkdirAll(journalRoot, 0o700) != nil {
			return FunctionalFlowExecutionAdapter{}, nil, &Error{Code: CodeUnavailable, Operation: "functional_flow_journal"}
		}
	}
	executor := &functionalflow.Executor{Authority: config.Binding, Clock: config.Clock, Policy: &functionalFlowProductPolicy{factory: config.PolicyFactory, limits: config.Execution}, Revalidator: functionalFlowIdentityRevalidator{catalog: config.Identities, publicIdentity: config.PublicIdentity}, Tool: functionalFlowToolAdapter{tool: config.Tool, sanitizer: config.Sanitizer, ids: config.IDs, publicIdentity: config.PublicIdentity, journalRoot: journalRoot, journalMu: &sync.Mutex{}, attemptSink: config.AttemptSink}, Evidence: confirmed, Fixtures: config.Fixtures, Assertions: functionalFlowAssertionAdapter{evaluator: config.Evaluator}, State: state, Journal: journal, Keys: functionalFlowKeyFactory{}, Waiter: config.Waiter, Config: config.Execution}
	composer := CanonicalFunctionalFlowComposer{Limits: config.Limits, Validation: config.Validation, Binding: config.Binding, Generated: generated, Provided: catalog}
	return FunctionalFlowExecutionAdapter{Composer: composer, Executor: executor, Artifacts: artifacts, Publisher: publisher, Journal: journal}, catalog, nil
}

// LocalFunctionalFlowEvidenceStore owns sanitized evidence payloads privately and exports only authenticated manifest records.
type LocalFunctionalFlowEvidenceStore struct{ root string }

// privateFunctionalEvidenceRecord is the only private-at-rest shape permitted to retain sanitized output outside public artifacts.
type privateFunctionalEvidenceRecord struct {
	ID, RunID, StepID, BindingDigest, ResourceBindingDigest, Digest string
	Revision                                                        uint64
	Output                                                          any
	Terminal, Truncated                                             bool
}

// FunctionalFixtureCaptureAdapter resolves only same-run confirmed captures from the existing fixture store.
type FunctionalFixtureCaptureAdapter struct{ Store fixture.Store }

// Resolve returns a detached private capture value and its confirmed digest without exposing it to durable flow state.
func (a FunctionalFixtureCaptureAdapter) Resolve(ctx context.Context, runID string, reference functionalflow.FixtureReference) (any, string, error) {
	if a.Store == nil || runID == "" || reference.FixtureID == "" || reference.Output == "" {
		return nil, "", &Error{Code: CodeInvalid, Operation: "functional_flow_fixture"}
	}
	state, err := a.Store.Load(ctx, runID)
	if err != nil {
		return nil, "", &Error{Code: CodeUnavailable, Operation: "functional_flow_fixture"}
	}
	capture, ok := state.Captures[reference.FixtureID+"\x00"+reference.Output]
	if !ok || capture.RunID != runID || capture.FixtureID != reference.FixtureID || capture.Name != reference.Output || capture.Digest == "" {
		return nil, "", &Error{Code: CodeConflict, Operation: "functional_flow_fixture"}
	}
	raw, err := json.Marshal(capture.Value)
	if err != nil {
		return nil, "", &Error{Code: CodeConflict, Operation: "functional_flow_fixture"}
	}
	var detached any
	if json.Unmarshal(raw, &detached) != nil {
		return nil, "", &Error{Code: CodeConflict, Operation: "functional_flow_fixture"}
	}
	return detached, capture.Digest, nil
}

// OpenLocalFunctionalFlowEvidenceStore creates the restrictive private evidence directory.
func OpenLocalFunctionalFlowEvidenceStore(root string) (*LocalFunctionalFlowEvidenceStore, error) {
	absolute, err := filepath.Abs(root)
	if err != nil || root == "" || os.MkdirAll(absolute, 0o700) != nil {
		return nil, &Error{Code: CodeUnavailable, Operation: "functional_flow_evidence"}
	}
	return &LocalFunctionalFlowEvidenceStore{root: absolute}, nil
}

// Confirm persists one content-sealed terminal evidence object without permitting replacement.
func (s *LocalFunctionalFlowEvidenceStore) Confirm(ctx context.Context, item functionalflow.Evidence) (functionalflow.Evidence, error) {
	if ctx.Err() != nil || !safeID.MatchString(item.ID) || functionalflow.SealEvidence(item).Digest != item.Digest {
		return functionalflow.Evidence{}, &Error{Code: CodeInvalid, Operation: "functional_flow_evidence"}
	}
	record := privateEvidenceFrom(item)
	if err := writeImmutableJSON(filepath.Join(s.root, item.ID+".json"), record, "functional_flow_evidence"); err != nil {
		return functionalflow.Evidence{}, err
	}
	return s.Load(ctx, item.ID)
}

// Load authenticates and detaches one private evidence record before deterministic evaluation or recovery.
func (s *LocalFunctionalFlowEvidenceStore) Load(ctx context.Context, evidenceID string) (functionalflow.Evidence, error) {
	if ctx.Err() != nil || !safeID.MatchString(evidenceID) {
		return functionalflow.Evidence{}, &Error{Code: CodeInvalid, Operation: "functional_flow_evidence"}
	}
	raw, err := os.ReadFile(filepath.Join(s.root, evidenceID+".json"))
	if errors.Is(err, os.ErrNotExist) {
		return functionalflow.Evidence{}, ErrNotFound
	}
	var record privateFunctionalEvidenceRecord
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	decodeErr := decoder.Decode(&record)
	item := record.evidence()
	if err != nil || decodeErr != nil || item.ID != evidenceID || functionalflow.SealEvidence(item).Digest != item.Digest {
		return functionalflow.Evidence{}, &Error{Code: CodeConflict, Operation: "functional_flow_evidence"}
	}
	return item, nil
}

// privateEvidenceFrom copies one sanitized record into the non-public persistence shape.
func privateEvidenceFrom(item functionalflow.Evidence) privateFunctionalEvidenceRecord {
	return privateFunctionalEvidenceRecord{ID: item.ID, RunID: item.RunID, Revision: item.Revision, StepID: item.StepID, BindingDigest: item.BindingDigest, ResourceBindingDigest: item.ResourceBindingDigest, Output: item.Output, Terminal: item.Terminal, Truncated: item.Truncated, Digest: item.Digest}
}

// evidence restores one private record for integrity validation inside the evidence boundary.
func (r privateFunctionalEvidenceRecord) evidence() functionalflow.Evidence {
	return functionalflow.Evidence{ID: r.ID, RunID: r.RunID, Revision: r.Revision, StepID: r.StepID, BindingDigest: r.BindingDigest, ResourceBindingDigest: r.ResourceBindingDigest, Output: r.Output, Terminal: r.Terminal, Truncated: r.Truncated, Digest: r.Digest}
}

// Records returns one unique authenticated manifest for every evidence ID reachable from the immutable primary ledger.
func (s *LocalFunctionalFlowEvidenceStore) Records(ctx context.Context, runID string, result functionalflow.Result) ([]functionalflow.EvidenceRecord, error) {
	seen := map[string]bool{}
	records := []functionalflow.EvidenceRecord{}
	for _, entry := range result.Ledger {
		for _, evidenceID := range entry.EvidenceIDs {
			if seen[evidenceID] {
				continue
			}
			item, err := s.Load(ctx, evidenceID)
			if err != nil || item.RunID != runID {
				return nil, &Error{Code: CodeConflict, Operation: "functional_flow_evidence"}
			}
			manifest, err := functionalflow.SealEvidenceRecord(item)
			if err != nil {
				return nil, &Error{Code: CodeConflict, Operation: "functional_flow_evidence"}
			}
			records = append(records, manifest)
			seen[evidenceID] = true
		}
	}
	return records, nil
}

// functionalFlowToolAdapter executes the controlled tool, sanitizes its bounded output, and assigns a system evidence ID.
type functionalFlowToolAdapter struct {
	tool           harness.Tool
	sanitizer      evidence.Sanitizer
	ids            harness.IDGenerator
	publicIdentity string
	journalRoot    string
	journalMu      *sync.Mutex
	attemptSink    FunctionalIdentityAttemptSink
}

// functionalIdentityAttemptEvent is the payload-free compatibility fact retained for authenticated attempts.
type functionalIdentityAttemptEvent struct {
	Type                   harness.EventType `json:"type"`
	IdentityID             string            `json:"identity_id"`
	OperationBindingSHA256 string            `json:"operation_binding_sha256"`
	Attempt                int               `json:"attempt"`
	OutcomeCategory        string            `json:"outcome_category"`
}

// Execute crosses the single controlled tool boundary with only exact bound operation and ephemeral input authority.
func (a functionalFlowToolAdapter) Execute(ctx context.Context, request functionalflow.ExecutionRequest) (functionalflow.ExecutionResponse, error) {
	input, inputErr := functionalOperationInput(request)
	if inputErr != nil {
		return functionalflow.ExecutionResponse{}, inputErr
	}
	binding := &request.Operation.Identity
	result, err := a.tool.Execute(ctx, harness.ToolCall{ID: request.StepID, Tool: request.Operation.Reference.Capability, RunID: request.RunID, Attempt: request.Attempt, Input: input, IdempotencyKey: request.IdempotencyKey, IdentityBinding: binding})
	if !binding.Anonymous {
		if journalErr := a.appendIdentityAttempt(ctx, request, functionalIdentityOutcome(ctx, result, err)); journalErr != nil {
			return functionalflow.ExecutionResponse{CrossedNetwork: true}, journalErr
		}
	}
	response := functionalflow.ExecutionResponse{CrossedNetwork: true, Transient: harness.IsTransient(err)}
	if err != nil {
		return response, err
	}
	safe, sanitizeErr := a.sanitizer.Sanitize(ctx, result.Output)
	if sanitizeErr != nil {
		return response, sanitizeErr
	}
	canonical, canonicalErr := json.Marshal(safe.Value())
	if canonicalErr != nil {
		return response, canonicalErr
	}
	decoder := json.NewDecoder(bytes.NewReader(canonical))
	decoder.UseNumber()
	if decoder.Decode(&response.Output) != nil {
		return response, &Error{Code: CodeInternal, Operation: "functional_flow_output"}
	}
	response.EvidenceID = a.ids.Next()
	response.ResourceBindingDigest = functionalResourceDigest(request.Operation, input)
	if response.EvidenceID == "" || !safeID.MatchString(response.EvidenceID) {
		return functionalflow.ExecutionResponse{}, &Error{Code: CodeInternal, Operation: "functional_flow_evidence_id"}
	}
	if output, ok := response.Output.(map[string]any); ok {
		response.Truncated, _ = output["body_truncated"].(bool)
		response.FunctionalFailure = functionalHTTPFailure(output["status"])
	}
	return response, nil
}

// appendIdentityAttempt synchronously appends one bounded terminal fact without request, response, or credential material.
func (a functionalFlowToolAdapter) appendIdentityAttempt(ctx context.Context, request functionalflow.ExecutionRequest, category string) error {
	if a.attemptSink != nil {
		return a.attemptSink.AppendIdentityAttempt(ctx, FunctionalIdentityAttempt{RunID: request.RunID, IdentityID: request.Operation.Identity.IdentityID, OperationBindingSHA256: request.Operation.Identity.Digest, Attempt: request.Attempt, OutcomeCategory: category})
	}
	if a.journalRoot == "" || a.journalMu == nil || !safeID.MatchString(request.RunID) || request.Attempt <= 0 || request.Operation.Identity.IdentityID == "" || request.Operation.Identity.Digest == "" {
		return &Error{Code: CodeInternal, Operation: "functional_flow_journal"}
	}
	event := functionalIdentityAttemptEvent{Type: harness.EventIdentityAttemptTerminal, IdentityID: request.Operation.Identity.IdentityID, OperationBindingSHA256: request.Operation.Identity.Digest, Attempt: request.Attempt, OutcomeCategory: category}
	encoded, err := json.Marshal(event)
	if err != nil {
		return &Error{Code: CodeInternal, Operation: "functional_flow_journal"}
	}
	a.journalMu.Lock()
	defer a.journalMu.Unlock()
	runRoot := filepath.Join(a.journalRoot, request.RunID)
	if err = os.MkdirAll(runRoot, 0o700); err != nil {
		return &Error{Code: CodeUnavailable, Operation: "functional_flow_journal"}
	}
	file, err := os.OpenFile(filepath.Join(runRoot, "events.jsonl"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return &Error{Code: CodeUnavailable, Operation: "functional_flow_journal"}
	}
	_, writeErr := file.Write(append(encoded, '\n'))
	syncErr := file.Sync()
	closeErr := file.Close()
	if writeErr != nil || syncErr != nil || closeErr != nil {
		return &Error{Code: CodeUnavailable, Operation: "functional_flow_journal"}
	}
	return nil
}

// functionalIdentityOutcome reduces one authenticated attempt to the established closed audit vocabulary.
func functionalIdentityOutcome(ctx context.Context, result harness.ToolResult, err error) string {
	if errors.Is(ctx.Err(), context.Canceled) || errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return "cancelled"
	}
	if err == nil && result.ErrorClass == harness.ErrorNone {
		return "success"
	}
	if failure, ok := result.Output["error"].(map[string]any); ok {
		if category, ok := failure["category"].(string); ok && len(category) > len("identity_") && category[:len("identity_")] == "identity_" {
			return "credential_rejected"
		}
	}
	if result.ErrorClass == harness.ErrorTransient || harness.IsTransient(err) {
		return "transient"
	}
	return "permanent"
}

// functionalResourceDigest binds comparisons to the same target, capability, method, and structural resource selector.
func functionalResourceDigest(operation functionalflow.BoundOperation, input map[string]any) string {
	raw, _ := json.Marshal(struct {
		Operation functionalflow.OperationReference `json:"operation"`
		Selector  map[string]any                    `json:"selector"`
	}{operation.Reference, input})
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

// functionalOperationInput derives the concrete controlled request from sealed authority and rejects producer drift before network.
func functionalOperationInput(request functionalflow.ExecutionRequest) (map[string]any, error) {
	raw, err := json.Marshal(request.Input)
	if err != nil {
		return nil, &Error{Code: CodeInvalid, Operation: "functional_flow_input"}
	}
	input := map[string]any{}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if decoder.Decode(&input) != nil {
		return nil, &Error{Code: CodeInvalid, Operation: "functional_flow_input"}
	}
	reference := request.Operation.Reference
	method, methodExists := input["method"]
	if methodExists && method != reference.Method {
		return nil, &Error{Code: CodeUnauthorized, Operation: "functional_flow_operation"}
	}
	input["method"] = reference.Method
	path, pathExists := input["path"].(string)
	if !pathExists {
		path = reference.Path
		input["path"] = path
	}
	for key, expected := range map[string]string{"target_id": reference.TargetID, "capability": reference.Capability} {
		if value, exists := input[key]; exists && value != expected {
			return nil, &Error{Code: CodeUnauthorized, Operation: "functional_flow_operation"}
		}
		delete(input, key)
	}
	concrete := testidentity.Operation{Capability: reference.Capability, TargetID: reference.TargetID, Method: reference.Method, Path: path}
	if request.Operation.Identity.Anonymous {
		err = testidentity.RevalidateAnonymous(request.Operation.Identity, concrete)
	} else if request.Operation.Identity.Operation != concrete && request.Operation.Identity.Operation.Path == concrete.Path {
		err = &Error{Code: CodeUnauthorized, Operation: "functional_flow_operation"}
	} else {
		_, err = testidentity.BindDescriptor(testidentity.Descriptor{ID: request.Operation.Identity.IdentityID, Mechanism: testidentity.MechanismBearer, Operations: []testidentity.Operation{request.Operation.Identity.Operation}}, concrete)
	}
	if err != nil {
		return nil, &Error{Code: CodeUnauthorized, Operation: "functional_flow_operation"}
	}
	return input, nil
}

// functionalHTTPFailure classifies a confirmed non-success HTTP status as functional evidence rather than transport failure.
func functionalHTTPFailure(value any) bool {
	status := int64(0)
	switch current := value.(type) {
	case int:
		status = int64(current)
	case int64:
		status = current
	case float64:
		status = int64(current)
	case json.Number:
		status, _ = strconv.ParseInt(string(current), 10, 64)
	}
	if status == 0 {
		return false
	}
	return status < 200 || status >= 400
}

// functionalFlowAssertionAdapter delegates local declarations to the existing deterministic assertion engine.
type functionalFlowAssertionAdapter struct {
	evaluator planning.StepAssertionEvaluator
}

// Evaluate converts one canonical flow step into the historical assertion boundary without changing its definitions.
func (a functionalFlowAssertionAdapter) Evaluate(ctx context.Context, declarations []planning.AssertionDeclaration, item functionalflow.Evidence) ([]planning.AssertionResult, error) {
	evaluation, err := a.evaluator.Evaluate(ctx, planning.PlanStep{ID: item.StepID, Assertions: declarations}, item.Output, []string{item.ID})
	return evaluation.Results, err
}

// functionalFlowIdentityRevalidator proves the sealed identity still authorizes the exact operation immediately before policy and I/O.
type functionalFlowIdentityRevalidator struct {
	catalog        *testidentity.Catalog
	publicIdentity string
}

// Revalidate rejects identity drift while allowing the explicit composition-owned public identity without credential injection.
func (r functionalFlowIdentityRevalidator) Revalidate(_ context.Context, _ string, step functionalflow.BoundStep) error {
	operation := testidentity.Operation{Capability: step.Operation.Reference.Capability, TargetID: step.Operation.Reference.TargetID, Method: step.Operation.Reference.Method, Path: step.Operation.Reference.Path}
	if step.Operation.Identity.IdentityID == r.publicIdentity {
		if r.publicIdentity == "" || !step.Operation.Identity.Anonymous || testidentity.RevalidateAnonymous(step.Operation.Identity, operation) != nil {
			return &Error{Code: CodeUnauthorized, Operation: "functional_flow_identity"}
		}
		return nil
	}
	if r.catalog == nil {
		return &Error{Code: CodeUnauthorized, Operation: "functional_flow_identity"}
	}
	return r.catalog.Revalidate(step.Operation.Identity, operation)
}

// functionalFlowProductPolicy reuses the configured runtime policy while isolating finite run aggregates by workflow handle.
type functionalFlowProductPolicy struct {
	factory func() harness.Policy
	limits  functionalflow.ExecutionConfig
	runs    map[string]*harness.Run
	mu      sync.Mutex
}

// Evaluate applies runtime policy to the exact already-bound call without granting additional operation authority.
func (p *functionalFlowProductPolicy) Evaluate(ctx context.Context, runID string, step functionalflow.BoundStep) (functionalflow.PolicyDecision, error) {
	p.mu.Lock()
	if p.runs == nil {
		p.runs = map[string]*harness.Run{}
	}
	run := p.runs[runID]
	if run == nil {
		run, _ = harness.NewRun(runID, "functional-flow", step.Step.ConstraintID, harness.Limits{MaxSteps: p.limits.MaxWork, MaxToolCalls: p.limits.MaxCalls, MaxRetries: p.limits.MaxRetries})
		p.runs[runID] = run
	}
	p.mu.Unlock()
	policy := p.factory()
	if policy == nil || run == nil {
		return functionalflow.PolicyDenied, &Error{Code: CodeUnauthorized, Operation: "functional_flow_policy"}
	}
	decision, err := policy.Evaluate(ctx, run, harness.ToolCall{ID: step.Step.ID, Tool: step.Operation.Reference.Capability, RunID: runID, Input: map[string]any{}})
	if err != nil || !decision.Allowed {
		return functionalflow.PolicyDenied, err
	}
	return functionalflow.PolicyAllowed, nil
}

// functionalFlowKeyFactory derives opaque stable action keys without serializing them into any artifact or package.
type functionalFlowKeyFactory struct{}

// Key binds one retry-safe action key to its run, revision, and step coordinates.
func (functionalFlowKeyFactory) Key(runID string, revision uint64, stepID string) string {
	raw, _ := json.Marshal(struct {
		RunID, StepID string
		Revision      uint64
	}{runID, stepID, revision})
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}
