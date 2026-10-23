package validationworkflow

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"go/ast"
	"go/parser"
	"go/token"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/asierhr/featureProof/internal/agentplanning"
	"github.com/asierhr/featureProof/internal/contractcatalog"
	"github.com/asierhr/featureProof/internal/evidence"
	"github.com/asierhr/featureProof/internal/fixture"
	"github.com/asierhr/featureProof/internal/githubpr"
	"github.com/asierhr/featureProof/internal/harness"
	"github.com/asierhr/featureProof/internal/localvalidation"
	"github.com/asierhr/featureProof/internal/planapproval"
	"github.com/asierhr/featureProof/internal/planmanifest"
	"github.com/asierhr/featureProof/internal/planning"
	"github.com/asierhr/featureProof/internal/revisionbinding"
	"github.com/asierhr/featureProof/internal/specification"
)

// TestMCPServeExposesDiscoveryAndSharedStatus proves the production stdio entrypoint is versioned and delegates to Service.
func TestMCPServeExposesDiscoveryAndSharedStatus(t *testing.T) {
	service, _, _, _, _ := workflowFixture(t, validPlanResponse())
	view, err := service.Start(context.Background(), validCommand())
	if err != nil {
		t.Fatal(err)
	}
	requests := strings.Join([]string{
		`{"jsonrpc":"2.0","id":1,"method":"server/discover","params":{"protocolVersion":"` + MCPProtocolVersion + `"}}`,
		`{"jsonrpc":"2.0","id":2,"method":"tools/list","params":{"protocolVersion":"` + MCPProtocolVersion + `"}}`,
		`{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"protocolVersion":"` + MCPProtocolVersion + `","name":"featureproof.workflow.status","arguments":{"handle":"` + view.Handle + `"}}}`,
	}, "\n") + "\n"
	var output bytes.Buffer
	server := MCPServer{Service: service, Principal: "user-1"}
	if err := server.Serve(context.Background(), strings.NewReader(requests), &output); err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(output.Bytes(), []byte(MCPProtocolVersion)) || !bytes.Contains(output.Bytes(), []byte("featureproof.workflow.start")) || !bytes.Contains(output.Bytes(), []byte(view.Handle)) {
		t.Fatalf("incomplete MCP output: %s", output.String())
	}
}

// TestMCPServeRejectsMalformedAndOversizedRequests proves untrusted stdio is bounded before application dispatch.
func TestMCPServeRejectsMalformedAndOversizedRequests(t *testing.T) {
	service, _, _, _, _ := workflowFixture(t, validPlanResponse())
	server := MCPServer{Service: service, Principal: "user-1"}
	var output bytes.Buffer
	if err := server.Serve(context.Background(), strings.NewReader("{}\n"), &output); err != nil || !strings.Contains(output.String(), "Invalid Request") {
		t.Fatalf("malformed request result: %v %s", err, output.String())
	}
	oversized := strings.NewReader(strings.Repeat("x", mcpMaxRequestBytes+1))
	if err := server.Serve(context.Background(), oversized, io.Discard); err == nil {
		t.Fatal("oversized MCP request accepted")
	}
}

// TestMCPRPCRejectsProtocolAndMethodVariants covers fail-closed JSON-RPC correlation and dispatch branches.
func TestMCPRPCRejectsProtocolAndMethodVariants(t *testing.T) {
	service, _, _, _, _ := workflowFixture(t, validPlanResponse())
	server := MCPServer{Service: service, Principal: "user-1"}
	for _, raw := range [][]byte{
		[]byte(`{"jsonrpc":"2.0","id":null,"method":"tools/list","params":{"protocolVersion":"` + MCPProtocolVersion + `"}}`),
		[]byte(`{"jsonrpc":"2.0","id":{},"method":"tools/list","params":{"protocolVersion":"` + MCPProtocolVersion + `"}}`),
		[]byte(`{"jsonrpc":"2.0","id":"x","method":"tools/list","params":{"protocolVersion":"wrong"}}`),
	} {
		if response := server.handleRPC(context.Background(), raw); response.Error == nil {
			t.Fatalf("invalid RPC accepted: %s", raw)
		}
	}
	unknownTool := []byte(`{"jsonrpc":"2.0","id":"x","method":"tools/call","params":{"protocolVersion":"` + MCPProtocolVersion + `","name":"featureproof.workflow.unknown","arguments":{}}}`)
	if response := server.handleRPC(context.Background(), unknownTool); response.Error == nil || response.Error.Code != -32601 {
		t.Fatal("unknown tool accepted")
	}
	unknownMethod := []byte(`{"jsonrpc":"2.0","id":"x","method":"unknown","params":{"protocolVersion":"` + MCPProtocolVersion + `"}}`)
	if response := server.handleRPC(context.Background(), unknownMethod); response.Error == nil || response.Error.Code != -32601 {
		t.Fatal("unknown method accepted")
	}
	if _, ok := workflowToolOperation("featureproof.workflow.unknown"); ok {
		t.Fatal("unknown operation mapped")
	}
}

// TestRewriteReferencesClonesNestedInputs proves namespacing reaches object, array and path-bearing references.
func TestRewriteReferencesClonesNestedInputs(t *testing.T) {
	field := "status"
	reference := planning.InputValue{Reference: &planning.OutputReference{StepID: "source", Path: []planning.PathSegment{{Field: &field}}, ValueType: planning.ValueString}}
	input := map[string]planning.InputValue{"object": {Object: map[string]planning.InputValue{"nested": reference}}, "array": {Array: []planning.InputValue{reference}}}
	rewritten := rewriteReferences(input, map[string]string{"source": "check-source"})
	if rewritten["object"].Object["nested"].Reference.StepID != "check-source" || rewritten["array"].Array[0].Reference.StepID != "check-source" {
		t.Fatalf("nested references were not rewritten: %#v", rewritten)
	}
	if input["object"].Object["nested"].Reference.StepID != "source" || input["array"].Array[0].Reference.StepID != "source" {
		t.Fatal("rewrite mutated prepared input")
	}
}

// testClock supplies deterministic workflow lease and event time.
type testClock struct {
	mu  sync.Mutex
	now time.Time
}

// controlledWaiter exposes deterministic heartbeat boundaries to lease tests.
type controlledWaiter struct {
	ready chan struct{}
	tick  chan struct{}
}

// Wait announces a pending heartbeat delay and completes only when the test advances it or cancels work.
func (w *controlledWaiter) Wait(ctx context.Context, _ time.Duration) error {
	select {
	case w.ready <- struct{}{}:
	case <-ctx.Done():
		return ctx.Err()
	}
	select {
	case <-w.tick:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Now returns the controlled UTC instant.
func (c *testClock) Now() time.Time { c.mu.Lock(); defer c.mu.Unlock(); return c.now }

// Wait blocks until cancellation so ordinary tests never depend on wall-clock heartbeat ticks.
func (c *testClock) Wait(ctx context.Context, _ time.Duration) error { <-ctx.Done(); return ctx.Err() }

// advance moves deterministic time without wall-clock sleeps.
func (c *testClock) advance(duration time.Duration) {
	c.mu.Lock()
	c.now = c.now.Add(duration)
	c.mu.Unlock()
}

// testIDs returns stable unique identifiers for workflow tests.
type testIDs struct {
	mu   sync.Mutex
	next int
}

// Next returns the next safe deterministic identity.
func (i *testIDs) Next() (string, error) {
	i.mu.Lock()
	defer i.mu.Unlock()
	i.next++
	return "workflow-" + itoa(uint64(i.next)), nil
}

// fakeAdmission counts preflight calls and returns one isolated trusted snapshot.
type fakeAdmission struct {
	mu       sync.Mutex
	calls    int
	snapshot AdmissionSnapshot
	err      error
}

// failingIDs simulates identity-source failure before durable admission.
type failingIDs struct{}

// Next returns one controlled identity failure.
func (failingIDs) Next() (string, error) { return "", errors.New("identity unavailable") }

// invalidIDs returns a malformed identity without a transport error.
type invalidIDs struct{}

// Next returns a path-like value rejected by admission.
func (invalidIDs) Next() (string, error) { return "bad/path", nil }

// Build records one side-effect boundary crossing.
func (f *fakeAdmission) Build(ctx context.Context, command StartCommand) (AdmissionSnapshot, error) {
	if ctx.Err() != nil {
		return AdmissionSnapshot{}, ctx.Err()
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	if f.calls == 1 {
		f.snapshot.Binding.Revision.RepositoryID, f.snapshot.Binding.Revision.CommitOID = command.RepositoryID, command.Revision
		f.snapshot.Binding.Target.Attestation.RepositoryID, f.snapshot.Binding.Target.Attestation.CommitOID = command.RepositoryID, command.Revision
		if command.Target.Preview != nil {
			f.snapshot.Binding.Target.Kind, f.snapshot.Binding.Target.Alias = "preview", command.Target.Preview.Alias
		} else if command.Target.Direct != nil {
			f.snapshot.Binding.Target.Kind, f.snapshot.Binding.Target.Alias = "direct", command.Target.Direct.Alias
		}
		f.snapshot.RevisionDigest, f.snapshot.TargetDigest, _ = bindingDigests(f.snapshot.Binding)
	}
	snapshot := f.snapshot.Clone()
	snapshot.Provider, snapshot.Model = command.Provider, command.Model
	return snapshot, f.err
}

// callCount reports preflight invocations safely.
func (f *fakeAdmission) callCount() int { f.mu.Lock(); defer f.mu.Unlock(); return f.calls }

// fakeModel returns one strict protocol response and counts inference.
type fakeModel struct {
	mu    sync.Mutex
	raw   []byte
	calls int
}

// countingGenerationPort records any forbidden inference attempt after a pre-admission contract decision.
type countingGenerationPort struct{ calls atomic.Int32 }

// Generate fails the test boundary if admission incorrectly permits model inference.
func (p *countingGenerationPort) Generate(context.Context, AdmissionSnapshot) (agentplanning.Result, ModelUsage, error) {
	p.calls.Add(1)
	return agentplanning.Result{}, ModelUsage{}, errors.New("generation should not run")
}

// admissionBuilderFunc adapts one test function to the side-effect-free admission port.
type admissionBuilderFunc func(context.Context, StartCommand) (AdmissionSnapshot, error)

// Build delegates a controlled pre-admission outcome.
func (f admissionBuilderFunc) Build(ctx context.Context, command StartCommand) (AdmissionSnapshot, error) {
	return f(ctx, command)
}

// contractReducerFunc adapts a closure into the neutral contract reducer port for authority-boundary tests.
type contractReducerFunc func(context.Context, string, specification.Specification) (contractcatalog.ReducedContext, error)

// Reduce delegates to the test closure while preserving the production port signature.
func (f contractReducerFunc) Reduce(ctx context.Context, ref string, spec specification.Specification) (contractcatalog.ReducedContext, error) {
	return f(ctx, ref, spec)
}

// Generate implements agentplanning.PlanningModel without network access.
func (f *fakeModel) Generate(ctx context.Context, request agentplanning.StructuredRequest) (agentplanning.ModelResponse, error) {
	if ctx.Err() != nil {
		return agentplanning.ModelResponse{}, ctx.Err()
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	raw := strings.ReplaceAll(string(f.raw), "workflow-1-plan", request.RunID+"-plan")
	return agentplanning.ModelResponse{JSON: []byte(raw), Provider: "fake", Model: "selected"}, nil
}

// fakeAssertions accepts structurally present test declarations.
type fakeAssertions struct{}

// ValidateDefinition keeps workflow tests focused on orchestration.
func (fakeAssertions) ValidateDefinition(planning.AssertionDeclaration, string) error { return nil }

// fakePolicy authorizes every descriptor-only planning intent.
type fakePolicy struct{}

// Evaluate returns a deterministic allowed generation decision.
func (fakePolicy) Evaluate(context.Context, agentplanning.PlanIntent) (agentplanning.PolicyDecision, error) {
	return agentplanning.PolicyDecision{Allowed: true}, nil
}

// fakeObserver confirms safe generation telemetry without retaining payloads.
type fakeObserver struct{}

// Observe accepts one terminal planning record.
func (fakeObserver) Observe(context.Context, agentplanning.GenerationRecord) error { return nil }

// planningClock supplies deterministic duration accounting to agentplanning.
type planningClock struct{}

// Now returns a stable planning timestamp.
func (planningClock) Now() time.Time { return time.Unix(0, 0).UTC() }

// fakeExecution records canonical execution requests and returns a package path.
type fakeExecution struct {
	mu      sync.Mutex
	calls   int
	request ExecutionRequest
	err     error
}

// blockingExecution exposes deterministic start and stop barriers for cancellation tests.
type blockingExecution struct {
	started chan struct{}
	stopped chan struct{}
}

// blockingGeneration exposes deterministic inference start and stop barriers for cancellation tests.
type blockingGeneration struct {
	started chan struct{}
	stopped chan struct{}
}

// driftingGeneration changes trusted contract authority only after a valid model result is produced.
type driftingGeneration struct {
	inner     GenerationPort
	admission *fakeAdmission
}

// Generate simulates catalog drift during inference without adding wall-clock races.
func (d driftingGeneration) Generate(ctx context.Context, snapshot AdmissionSnapshot) (agentplanning.Result, ModelUsage, error) {
	result, usage, err := d.inner.Generate(ctx, snapshot)
	d.admission.mu.Lock()
	d.admission.snapshot.Generation.Contract = workflowContract("policy-v2")
	d.admission.snapshot.ContractPolicyDigest = d.admission.snapshot.Generation.Contract.PolicyDigest()
	d.admission.mu.Unlock()
	return result, usage, err
}

// Generate waits for cancellation and never returns a partial model decision.
func (b *blockingGeneration) Generate(ctx context.Context, _ AdmissionSnapshot) (agentplanning.Result, ModelUsage, error) {
	close(b.started)
	<-ctx.Done()
	close(b.stopped)
	return agentplanning.Result{}, ModelUsage{}, ctx.Err()
}

// Execute blocks until the worker context is cancelled and confirms it observed the signal.
func (b *blockingExecution) Execute(ctx context.Context, _ ExecutionRequest) (string, error) {
	close(b.started)
	<-ctx.Done()
	close(b.stopped)
	return "", ctx.Err()
}

// Execute captures one authorized runner invocation.
func (f *fakeExecution) Execute(ctx context.Context, request ExecutionRequest) (string, error) {
	if ctx.Err() != nil {
		return "", ctx.Err()
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	f.request = request
	return "package-safe", f.err
}

// callCount reports execution invocations safely.
func (f *fakeExecution) callCount() int { f.mu.Lock(); defer f.mu.Unlock(); return f.calls }

// fakePackages returns a configured verified package projection.
type fakePackages struct {
	projection PackageProjection
	err        error
}

// Read proves functional outcome crosses only the package reader boundary.
func (f fakePackages) Read(context.Context, string, string, string) (PackageProjection, error) {
	return f.projection, f.err
}

// AttachFixtures simulates atomic package resealing for workflow lifecycle tests.
func (f fakePackages) AttachFixtures(context.Context, string, fixture.FixtureSummary) error {
	return f.err
}

// fakeAuto explicitly controls disabled-approval authorization.
type fakeAuto struct{ err error }

// Authorize returns the configured trusted composition policy result.
func (f fakeAuto) Authorize(context.Context, StartCommand, PlanArtifact) error { return f.err }

// fakeFinalizer records idempotent cleanup requests.
type fakeFinalizer struct {
	mu    sync.Mutex
	calls int
	err   error
}

// fakePreview records workflow-owned preview acquisition and release without provisioning infrastructure.
type fakePreview struct {
	mu                     sync.Mutex
	digest, alias          string
	acquires, releases     int
	acquireErr, releaseErr error
}

// Acquire returns one programmed complete sealed target object.
func (f *fakePreview) Acquire(_ context.Context, _ string, command StartCommand) (PreviewBinding, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.acquires++
	if command.Target.Preview != nil {
		f.alias = command.Target.Preview.Alias
	}
	return f.binding(), f.acquireErr
}

// Restore reconstructs the fake preview target for recovery and cross-owner tests.
func (f *fakePreview) Restore(_ context.Context, _ string, command StartCommand) (PreviewBinding, error) {
	if command.Target.Preview != nil {
		f.mu.Lock()
		f.alias = command.Target.Preview.Alias
		f.mu.Unlock()
	}
	return f.binding(), f.acquireErr
}

// binding builds one safe preview observation whose fingerprint remains independently mutable.
func (f *fakePreview) binding() PreviewBinding {
	alias := f.alias
	if alias == "" {
		alias = "target-1"
	}
	return PreviewBinding{Target: TargetContext{Kind: "preview", Alias: alias, URL: "https://preview.example/", ProviderID: "fake-provider", EnvironmentRef: "fake-ref", ProfileID: "runtime-1", EnvironmentID: "environment-1", ReservationID: "reservation-1", ObservedVersion: "v1", Fingerprint: f.digest, Attestation: AttestationContext{EnvironmentID: "environment-1", RepositoryID: "repo-1", CommitOID: strings.Repeat("a", 40), Reference: "proof-1", ValidUntil: time.Unix(10000, 0).UTC(), Verified: true}, Network: NetworkContext{PolicyID: "network-1", Schemes: []string{"https"}, Hosts: []string{"preview.example"}, Ports: []int{443}, BasePaths: []string{"/"}, MaxURLBytes: 2048}}}
}

// expectedDigest derives the fake preview identity from the same complete projection as production.
func (f *fakePreview) expectedDigest() string {
	digest, _ := TargetContextDigest(f.binding().Target)
	return digest
}

// Release records one idempotent logical cleanup attempt.
func (f *fakePreview) Release(context.Context, string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.releases++
	return f.releaseErr
}

// testApprovalSanitizer returns already-bounded fixture values for planapproval views.
type testApprovalSanitizer struct{}

// blockingRestoreStore observes cancellation propagated through approval restoration.
type blockingRestoreStore struct {
	planapproval.ApprovalStore
	observed chan error
}

// RestoreRun blocks until the caller's context is cancelled and reports the exact cancellation.
func (s blockingRestoreStore) RestoreRun(ctx context.Context, _ string) (*harness.Run, error) {
	<-ctx.Done()
	s.observed <- ctx.Err()
	return nil, ctx.Err()
}

// Sanitize isolates approval tests from product redaction while retaining structured values.
func (testApprovalSanitizer) Sanitize(_ context.Context, value any) (any, error) {
	raw, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	var isolated any
	err = json.Unmarshal(raw, &isolated)
	return isolated, err
}

// fakeSpecifications returns one validated in-memory specification.
type fakeSpecifications struct {
	spec  specification.Specification
	calls int
	err   error
}

// Load counts and returns the configured specification without filesystem access.
func (f *fakeSpecifications) Load(context.Context, string) (specification.Specification, error) {
	f.calls++
	return f.spec, f.err
}

// fakeContracts returns one bounded reduced contract.
type fakeContracts struct {
	reduced contractcatalog.ReducedContext
	calls   int
	err     error
}

// Reduce counts and returns the configured contract context.
func (f *fakeContracts) Reduce(context.Context, string, specification.Specification) (contractcatalog.ReducedContext, error) {
	f.calls++
	return f.reduced, f.err
}

// fakeBindings returns exact effective source and target objects.
type fakeBindings struct {
	binding BindingContext
	calls   int
	err     error
}

// fakeAuthorityBindings exposes sealed source authority before the later full binding verification boundary.
type fakeAuthorityBindings struct {
	*fakeBindings
	authority RevisionContext
	err       error
}

// ContractAuthority returns the configured trusted source authority or deterministic boundary failure.
func (f *fakeAuthorityBindings) ContractAuthority(context.Context, StartCommand) (RevisionContext, error) {
	return f.authority, f.err
}

// Verify counts and returns the safe effective binding context.
func (f *fakeBindings) Verify(context.Context, StartCommand) (BindingContext, error) {
	f.calls++
	return f.binding, f.err
}

// fakeCatalog returns descriptor-only capabilities and one runtime digest.
type fakeCatalog struct {
	descriptors []agentplanning.CapabilityDescriptor
	runtime     RuntimeContext
	calls       int
	err         error
}

// testRevisionResolver returns one exact trusted source revision.
type testRevisionResolver struct{ revision revisionbinding.Revision }

// Resolve returns the configured immutable revision.
func (r testRevisionResolver) Resolve(context.Context, string, string) (revisionbinding.Revision, error) {
	return r.revision, nil
}

// testEnvironmentAttestor returns one exact trusted deployment attestation.
type testEnvironmentAttestor struct{ attestation revisionbinding.Attestation }

// Attest returns the configured immutable deployment observation.
func (a testEnvironmentAttestor) Attest(context.Context, string) (revisionbinding.Attestation, error) {
	return a.attestation, nil
}

// testSafeProjector classifies the fixed vertical fixture without retaining secrets.
type testSafeProjector struct{}

// Project returns complete safe semantic projections for binding hashes.
func (testSafeProjector) Project(context.Context, specification.Specification, planmanifest.Manifest, []planning.ValidatedPlan) (localvalidation.SafeProjections, error) {
	value := revisionbinding.PublicString("vertical-fixture")
	return localvalidation.SafeProjections{Constraints: value, Contract: value, Configuration: value, Plans: []revisionbinding.SafeValue{value}}, nil
}

// Descriptors counts and returns isolated planning descriptors.
func (f *fakeCatalog) Descriptors(context.Context, string, TargetSelection) ([]agentplanning.CapabilityDescriptor, RuntimeContext, error) {
	f.calls++
	return append([]agentplanning.CapabilityDescriptor(nil), f.descriptors...), f.runtime, f.err
}

// Release records one bounded cleanup attempt.
func (f *fakeFinalizer) Release(context.Context, string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	return f.err
}

// workflowFixture composes one hermetic service with the real agentplanning validator.
func workflowFixture(t *testing.T, raw []byte) (*Service, *fakeAdmission, *fakeExecution, *LocalStore, *testClock) {
	t.Helper()
	clock := &testClock{now: time.Unix(100, 0).UTC()}
	model := &fakeModel{raw: raw}
	generator, err := agentplanning.NewGenerator(model, fakeAssertions{}, fakePolicy{}, fakeObserver{}, planningClock{})
	if err != nil {
		t.Fatal(err)
	}
	admission := &fakeAdmission{snapshot: validSnapshot()}
	execution := &fakeExecution{}
	store, err := OpenLocalStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	artifacts, err := OpenLocalArtifactStore(filepath.Join(store.root, "plans"))
	if err != nil {
		t.Fatal(err)
	}
	executables, err := OpenLocalExecutableStore(filepath.Join(store.root, "private"))
	if err != nil {
		t.Fatal(err)
	}
	approval := &PlanApprovalBridge{Store: planapproval.NewMemoryStore(), Sanitizer: testApprovalSanitizer{}, Clock: clock, Limits: planapproval.Limits{MaxBytes: 1 << 20, MaxDepth: 32, MaxEvents: 100, MaxConstraints: 10, MaxChecks: 10, MaxSteps: 100, MaxAssertions: 100, MaxEvidence: 100}}
	service, err := NewService(Config{LeaseTTL: time.Minute, HeartbeatInterval: 10 * time.Second, FinalizeTimeout: time.Second, ApprovalTTL: time.Hour}, Dependencies{Admission: admission, Generation: GeneratorAdapter{Generator: generator}, Preparation: PreparationAdapter{Assertions: fakeAssertions{}, Policy: fakePolicy{}}, Approval: approval, Execution: execution, Packages: fakePackages{projection: PackageProjection{RunID: "workflow-1", BindingSHA256: strings.Repeat("a", 64), PackageDir: "package-safe", Outcome: OutcomePassed}}, Authorization: fakeAuto{}, Finalizer: &fakeFinalizer{}, Store: store, Artifacts: artifacts, Executables: executables, Clock: clock, Waiter: clock, IDs: &testIDs{}, Owner: "owner-1"})
	if err != nil {
		t.Fatal(err)
	}
	return service, admission, execution, store, clock
}

// validSnapshot creates a complete safe preflight result with descriptor-only capabilities.
func validSnapshot() AdmissionSnapshot {
	binding := BindingContext{Revision: RevisionContext{RepositoryID: "repo-1", CommitOID: strings.Repeat("a", 40)}, Target: TargetContext{Kind: "direct", Alias: "target-1", URL: "https://target.example/", EnvironmentID: "environment-1", Attestation: AttestationContext{EnvironmentID: "environment-1", RepositoryID: "repo-1", CommitOID: strings.Repeat("a", 40), Reference: "proof-1", ValidUntil: time.Unix(10000, 0).UTC(), Verified: true}, Network: NetworkContext{PolicyID: "network-1", Schemes: []string{"https"}, Hosts: []string{"target.example"}, Ports: []int{443}, BasePaths: []string{"/"}, MaxURLBytes: 2048}}}
	runtime := RuntimeContext{Profile: "runtime-1", ManifestVersion: 1, HTTP: planmanifest.HTTPConfig{AllowedMethods: []string{"GET"}, AllowedRequestHeaders: []string{}, AllowedResponseHeaders: []string{}, FollowRedirectsForMethods: []string{}, MaxRedirects: 0, TimeoutMS: 1000, MaxURLBytes: 2048, MaxRequestHeaderBytes: 1024, MaxRequestBodyBytes: 1024, MaxResponseHeaderBytes: 1024, MaxResponseBodyBytes: 1024}, Redaction: evidence.Config{MaxBodyBytes: 1024, AdditionalHeaders: []string{}, SensitiveJSONPaths: []string{}, ModelSensitiveJSONPaths: []string{}}, Registry: RegistryContext{Version: "v1", Tools: []string{"probe"}}, Schema: SchemaContext{Version: "v1", ReferenceVersion: "v1", Fields: []string{"input"}}, Validator: ValidatorContext{Tool: "probe", ToolVersion: "v1", AssertionEngine: "fake", AssertionVersion: "v1"}, Policy: PolicyContext{ID: "policy-1", AllowedTools: []string{"probe"}, Reason: "test_allowlist"}, Network: binding.Target.Network}
	revisionDigest, targetDigest, _ := bindingDigests(binding)
	runtimeDigest, _ := runtimeContextDigest(runtime)
	contract := workflowContract("policy-v1")
	return AdmissionSnapshot{
		SpecificationDigest: strings.Repeat("1", 64), ContractDigest: strings.Repeat("2", 64), CatalogDigest: contract.CatalogDigest(), ContractPolicyDigest: contract.PolicyDigest(),
		RevisionDigest: revisionDigest, TargetDigest: targetDigest, RuntimeDigest: runtimeDigest, Binding: binding, Runtime: runtime,
		Generation: agentplanning.GenerationRequest{
			Constraints:  []agentplanning.Constraint{{ID: "c1", Description: "observable"}},
			Contract:     contract,
			Capabilities: []agentplanning.CapabilityDescriptor{{Name: "probe", Class: "read", Validate: func(map[string]planning.InputValue) error { return nil }}},
			Limits:       agentplanning.Limits{MaxInputBytes: 1 << 20, MaxOutputBytes: 1 << 20, MaxConstraints: 5, MaxCapabilities: 5, MaxChecks: 5, MaxStepsPerCheck: 5, MaxAssertionsPerStep: 5, MaxMissingItems: 5, MaxValidationWork: 100},
		},
	}
}

// workflowContract creates a verified immutable contract fixture for workflow admission and drift tests.
func workflowContract(policyID string) contractcatalog.ReducedContext {
	sum := sha256.Sum256([]byte(policyID))
	digest := hex.EncodeToString(sum[:])
	provenance := contractcatalog.Provenance{SourceID: "fixture", Class: contractcatalog.ClassAuthoritative, SourceDigest: digest, AdapterVersion: "test-v1", SafeLocation: "catalog/operations/0", Confidence: contractcatalog.ConfidenceHigh, Completeness: contractcatalog.CompletenessComplete}
	operation := contractcatalog.OperationDescriptor{Key: contractcatalog.OperationKey{Method: "GET", Path: "/pets"}, LogicalID: "fixture", Responses: map[string]contractcatalog.Response{"200": {}}, Security: []contractcatalog.SecurityRequirement{}, Provenance: []contractcatalog.Provenance{provenance}, Confidence: contractcatalog.ConfidenceHigh, Completeness: contractcatalog.CompletenessComplete, Classes: []contractcatalog.SourceClass{contractcatalog.ClassAuthoritative}}
	operation.Claims = contractcatalog.ClaimsForOperation(operation, provenance)
	snapshot := contractcatalog.SourceSnapshot{Version: contractcatalog.VersionV1, Source: contractcatalog.SourceSummary{ID: "fixture", Kind: "fixture", Class: contractcatalog.ClassAuthoritative, Digest: digest, AdapterVersion: "test-v1", Confidence: contractcatalog.ConfidenceHigh, Completeness: contractcatalog.CompletenessComplete}, Operations: []contractcatalog.OperationDescriptor{operation}}
	limits := contractcatalog.Limits{MaxSources: 1, MaxOperations: 8, MaxClaims: 128, MaxBytes: 1 << 20, MaxDepth: 32, MaxDiagnostics: 32, MaxLocationBytes: 256}
	projection := contractcatalog.ProjectionPolicy{MaxStringBytes: 512, AllowedURLSchemes: []string{"https"}, SensitiveNames: []string{}, SensitivePaths: []string{}, SensitiveHeaderNames: []string{"authorization", "cookie"}, ForbiddenValues: []string{}}
	registry, err := contractcatalog.NewRegistry(map[string]contractcatalog.ContractSource{"fixture": &contractcatalog.StaticSource{Snapshot: snapshot}})
	if err != nil {
		panic(err)
	}
	loaded, err := registry.Load(context.Background(), []contractcatalog.SourceRequest{contractcatalog.NewSourceRequest(contractcatalog.SourceDescriptor{Kind: "fixture", ID: "fixture", Class: contractcatalog.ClassAuthoritative, Confidence: contractcatalog.ConfidenceHigh, Completeness: contractcatalog.CompletenessComplete, ExpectedDigest: digest, AdapterVersion: "test-v1", Limits: limits, Projection: projection})}, limits)
	if err != nil {
		panic(err)
	}
	catalog, err := contractcatalog.Build(context.Background(), loaded, limits)
	if err != nil {
		panic(err)
	}
	rules := map[contractcatalog.ClaimKind]contractcatalog.ClaimRule{}
	for _, kind := range []contractcatalog.ClaimKind{contractcatalog.ClaimOperationIdentity, contractcatalog.ClaimSchema, contractcatalog.ClaimSecurity, contractcatalog.ClaimResponse} {
		rules[kind] = contractcatalog.ClaimRule{AllowedClasses: []contractcatalog.SourceClass{contractcatalog.ClassAuthoritative}, MinimumConfidence: map[contractcatalog.SourceClass]contractcatalog.Confidence{contractcatalog.ClassAuthoritative: contractcatalog.ConfidenceHigh}, RequiredCompleteness: contractcatalog.CompletenessComplete, Required: true}
	}
	policy, err := contractcatalog.NewPolicy(contractcatalog.SufficiencyPolicy{ID: policyID, Version: contractcatalog.VersionV1, Claims: rules, ConflictTreatment: contractcatalog.ConflictDeny})
	if err != nil {
		panic(err)
	}
	reduced, err := contractcatalog.BuildContext(context.Background(), catalog, policy, []contractcatalog.ConstraintSelection{{ConstraintID: "c1", Operations: []contractcatalog.OperationKey{{Method: "GET", Path: "/pets"}}}}, contractcatalog.ReductionLimits{MaxBytes: 1 << 20, MaxOperations: 8, MaxClaims: 128, MaxDepth: 32, MaxDiagnostics: 32})
	if err != nil {
		panic(err)
	}
	return reduced
}

// validCommand creates all explicit workflow selections with required approval.
func validCommand() StartCommand {
	return StartCommand{Principal: "user-1", IdempotencyKey: "request-1", SpecificationRef: "spec-1", ContractRef: "contract-1", Provider: "fake", Model: "selected", CompositionRef: strings.Repeat("8", 64), RuntimeProfile: "runtime-1", RepositoryID: "repo-1", Revision: strings.Repeat("a", 40), Approval: ApprovalRequired, Target: TargetSelection{Direct: &DirectTarget{Alias: "target-1", Digest: validSnapshot().TargetDigest}}}
}

// validPlanResponse returns one complete protocol-v1 candidate plan.
func validPlanResponse() []byte {
	return []byte(`{"version":1,"kind":"plan","plan":{"id":"workflow-1-plan","scenarios":[{"constraint_id":"c1","scenario":"positive","status":"planned"},{"constraint_id":"c1","scenario":"negative","status":"not_applicable","reason":"not retained"},{"constraint_id":"c1","scenario":"error","status":"not_applicable","reason":"not retained"}],"checks":[{"id":"check-1","constraint_id":"c1","scenario":"positive","capability":"probe","steps":[{"id":"step-1","tool":"probe","input":{},"assertions":[{"id":"assert-1","definition":{"operator":"exists"}}]}],"evidence":[{"step_id":"step-1","assertion_ids":["assert-1"]}]}]}}`)
}

// missingResponse returns one valid bounded insufficient-context result.
func missingResponse() []byte {
	return []byte(`{"version":1,"kind":"insufficient_context","missing_context":{"items":[{"kind":"question","constraint_ids":["c1"],"request":"Which observable status is required?"}]}}`)
}

// TestStartRequiresEveryExplicitSelectionAndPreservesPreAdmissionBoundary proves invalid input creates no durable or inference effects.
func TestStartRequiresEveryExplicitSelectionAndPreservesPreAdmissionBoundary(t *testing.T) {
	service, admission, execution, _, _ := workflowFixture(t, validPlanResponse())
	command := validCommand()
	command.Model = ""
	view, err := service.Start(context.Background(), command)
	if !IsCode(err, CodeInvalid) || view.Handle != "" || admission.callCount() != 0 || execution.callCount() != 0 {
		t.Fatalf("pre-admission boundary crossed: %#v %v", view, err)
	}
}

// TestAdmissionValidatesBindingContractPolicyAndBudgetsBeforeModel proves preflight produces a complete immutable descriptor-only snapshot.
func TestAdmissionValidatesBindingContractPolicyAndBudgetsBeforeModel(t *testing.T) {
	command := validCommand()
	specifications := &fakeSpecifications{spec: specification.Specification{Version: 1, Feature: specification.Feature{ID: "feature-1"}, Constraints: []specification.Constraint{{ID: "c1", Description: "observable", TargetID: "target-1"}}, Limits: specification.Limits{MaxSteps: 2, MaxToolCalls: 2}}}
	contracts := &fakeContracts{reduced: validSnapshot().Generation.Contract}
	bindings := &fakeBindings{binding: validSnapshot().Binding}
	catalog := &fakeCatalog{descriptors: validSnapshot().Generation.Capabilities, runtime: validSnapshot().Runtime}
	builder := TrustedAdmissionBuilder{Specifications: specifications, Contracts: contracts, Bindings: bindings, Catalog: catalog, Config: AdmissionConfig{Limits: validSnapshot().Generation.Limits, Provider: command.Provider, Model: command.Model, CompositionRef: command.CompositionRef}}
	snapshot, err := builder.Build(context.Background(), command)
	if err != nil || snapshot.Provider != command.Provider || snapshot.Model != command.Model || len(snapshot.Generation.Constraints) != 1 || len(snapshot.Generation.Capabilities) != 1 || specifications.calls != 1 || contracts.calls != 1 || bindings.calls != 1 || catalog.calls != 1 {
		t.Fatalf("invalid admission: %#v %v", snapshot, err)
	}
	snapshot.Generation.Constraints[0].ID = "changed"
	again, err := builder.Build(context.Background(), command)
	if err != nil || again.Generation.Constraints[0].ID != "c1" {
		t.Fatal("admission snapshot aliased caller state")
	}
	command.Target.Direct.Digest = strings.Repeat("9", 64)
	if _, err = builder.Build(context.Background(), command); !IsCode(err, CodeInvalid) {
		t.Fatalf("target drift accepted: %v", err)
	}
}

// TestAdmissionCarriesNeutralContractAuthority verifies canonical copies reach the reducer and provider failure stops all later authority ports.
func TestAdmissionCarriesNeutralContractAuthority(t *testing.T) {
	command := validCommand()
	base := validSnapshot()
	specifications := &fakeSpecifications{spec: specification.Specification{Version: 1, Feature: specification.Feature{ID: "feature-1"}, Constraints: []specification.Constraint{{ID: "c1", Description: "observable", TargetID: "target-1"}}, Limits: specification.Limits{MaxSteps: 2, MaxToolCalls: 2}}}
	observed := RevisionContext{}
	contracts := &fakeContracts{reduced: base.Generation.Contract}
	contractPort := contractReducerFunc(func(ctx context.Context, _ string, _ specification.Specification) (contractcatalog.ReducedContext, error) {
		observed, _ = ContractAuthority(ctx)
		return contracts.reduced, nil
	})
	authority := RevisionContext{RepositoryID: command.RepositoryID, CommitOID: command.Revision, Roots: []string{"app"}, ChangedPaths: []string{"app/main.go"}}
	bindings := &fakeAuthorityBindings{fakeBindings: &fakeBindings{binding: base.Binding}, authority: authority}
	catalog := &fakeCatalog{descriptors: base.Generation.Capabilities, runtime: base.Runtime}
	builder := TrustedAdmissionBuilder{Specifications: specifications, Contracts: contractPort, Bindings: bindings, Catalog: catalog, Config: AdmissionConfig{Limits: base.Generation.Limits, Provider: command.Provider, Model: command.Model, CompositionRef: command.CompositionRef}}
	if _, err := builder.Build(context.Background(), command); err != nil || len(observed.Roots) != 1 || len(observed.ChangedPaths) != 1 {
		t.Fatalf("neutral authority missing: %+v %v", observed, err)
	}
	authority.Roots[0] = "changed"
	if observed.Roots[0] != "app" {
		t.Fatal("authority context aliased caller state")
	}
	bindings.err = errors.New("authority drift")
	observed = RevisionContext{}
	if _, err := builder.Build(context.Background(), command); !IsCode(err, CodeUnauthorized) || observed.RepositoryID != "" || bindings.calls != 1 || catalog.calls != 1 {
		t.Fatalf("authority failure crossed later ports: %+v %v calls=%d/%d", observed, err, bindings.calls, catalog.calls)
	}
	if _, ok := ContractAuthority(context.Background()); ok {
		t.Fatal("ambient context unexpectedly carried authority")
	}
}

// TestTypedContractDecisionStopsAdmissionBeforeLaterAuthorityPorts proves insufficient and review outcomes survive the workflow boundary.
func TestTypedContractDecisionStopsAdmissionBeforeLaterAuthorityPorts(t *testing.T) {
	command := validCommand()
	base := validSnapshot()
	spec := specification.Specification{Version: 1, Feature: specification.Feature{ID: "feature", Description: "feature"}, Target: specification.Target{ID: "target", BaseURL: "https://target.example"}, Constraints: []specification.Constraint{{ID: "c1", Description: "observable", TargetID: "target"}}, Limits: specification.Limits{MaxSteps: 1, MaxToolCalls: 1, MaxRetries: 0}}
	for _, category := range []contractcatalog.ErrorCategory{contractcatalog.CategoryInsufficient, contractcatalog.CategoryReviewRequired} {
		contracts := &fakeContracts{err: &contractcatalog.DecisionError{Category: category, MissingKinds: []string{"operation_selection"}, Operations: []contractcatalog.OperationKey{{Method: "GET", Path: "/pets"}}, ConflictIDs: []string{"conflict-1"}}}
		bindings := &fakeBindings{binding: base.Binding}
		catalog := &fakeCatalog{descriptors: base.Generation.Capabilities, runtime: base.Runtime}
		builder := TrustedAdmissionBuilder{Specifications: &fakeSpecifications{spec: spec}, Contracts: contracts, Bindings: bindings, Catalog: catalog, Config: AdmissionConfig{Limits: base.Generation.Limits, Provider: command.Provider, Model: command.Model, CompositionRef: command.CompositionRef}}
		_, err := builder.Build(context.Background(), command)
		var typed *Error
		if !errors.As(err, &typed) || typed.Decision == nil || len(typed.Decision.Operations) != 1 || len(typed.Decision.ConflictIDs) != 1 || bindings.calls != 0 || catalog.calls != 0 {
			t.Fatalf("decision lost or later port called: %#v %v %d/%d", typed, err, bindings.calls, catalog.calls)
		}
		if category == contractcatalog.CategoryInsufficient && typed.Code != CodeInsufficientContext {
			t.Fatal("insufficient category changed")
		}
		if category == contractcatalog.CategoryReviewRequired && typed.Code != CodeReviewRequired {
			t.Fatal("review category changed")
		}
		projected := errorDecision(err)
		projected.Operations[0].Path = "/changed"
		if typed.Decision.Operations[0].Path != "/pets" {
			t.Fatal("adapter decision leaked mutable state")
		}
	}
	service, _, execution, store, _ := workflowFixture(t, validPlanResponse())
	modelSpy := &countingGenerationPort{}
	service.deps.Generation = modelSpy
	service.deps.Admission = admissionBuilderFunc(func(context.Context, StartCommand) (AdmissionSnapshot, error) {
		return AdmissionSnapshot{}, &Error{Code: CodeInsufficientContext, Operation: "contract", Decision: &ContextDecision{MissingKinds: []string{"operation_selection"}}}
	})
	view, err := service.Start(context.Background(), command)
	if !IsCode(err, CodeInsufficientContext) || errorDecision(err) == nil || view.Handle != "" || modelSpy.calls.Load() != 0 || execution.callCount() != 0 {
		t.Fatalf("pre-admission effects occurred: %#v %v model=%d tools=%d", view, err, modelSpy.calls.Load(), execution.callCount())
	}
	if events, eventsErr := store.Events(context.Background(), "workflow-1"); eventsErr == nil || len(events) != 0 {
		t.Fatalf("approval/durable state created: %#v %v", events, eventsErr)
	}
}

// TestAdmissionFailsClosedAtEveryBoundary proves dependency failures never yield a partial snapshot.
func TestAdmissionFailsClosedAtEveryBoundary(t *testing.T) {
	command := validCommand()
	baseSpec := specification.Specification{Version: 1, Feature: specification.Feature{ID: "f"}, Constraints: []specification.Constraint{{ID: "c", Description: "d"}}, Limits: specification.Limits{MaxSteps: 1, MaxToolCalls: 1}}
	makeBuilder := func() (TrustedAdmissionBuilder, *fakeSpecifications, *fakeContracts, *fakeBindings, *fakeCatalog) {
		specifications := &fakeSpecifications{spec: baseSpec}
		contracts := &fakeContracts{reduced: validSnapshot().Generation.Contract}
		bindings := &fakeBindings{binding: validSnapshot().Binding}
		catalog := &fakeCatalog{descriptors: validSnapshot().Generation.Capabilities, runtime: validSnapshot().Runtime}
		return TrustedAdmissionBuilder{Specifications: specifications, Contracts: contracts, Bindings: bindings, Catalog: catalog, Config: AdmissionConfig{Limits: validSnapshot().Generation.Limits, Provider: command.Provider, Model: command.Model, CompositionRef: command.CompositionRef}}, specifications, contracts, bindings, catalog
	}
	builder, specs, _, _, _ := makeBuilder()
	specs.err = errors.New("read")
	if _, err := builder.Build(context.Background(), command); !IsCode(err, CodeInvalid) {
		t.Fatal(err)
	}
	builder, _, contracts, _, _ := makeBuilder()
	contracts.err = errors.New("read")
	if _, err := builder.Build(context.Background(), command); !IsCode(err, CodeInvalid) {
		t.Fatal(err)
	}
	builder, _, contracts, _, _ = makeBuilder()
	contracts.reduced = contractcatalog.ReducedContext{}
	if _, err := builder.Build(context.Background(), command); !IsCode(err, CodeInvalid) {
		t.Fatalf("missing catalog binding accepted: %v", err)
	}
	builder, _, contracts, _, _ = makeBuilder()
	contracts.reduced = contractcatalog.ReducedContext{}
	if _, err := builder.Build(context.Background(), command); !IsCode(err, CodeInvalid) {
		t.Fatalf("missing policy binding accepted: %v", err)
	}
	builder, _, _, bindings, _ := makeBuilder()
	bindings.err = errors.New("attest")
	if _, err := builder.Build(context.Background(), command); !IsCode(err, CodeUnauthorized) {
		t.Fatal(err)
	}
	builder, _, _, _, catalog := makeBuilder()
	catalog.err = errors.New("catalog")
	if _, err := builder.Build(context.Background(), command); !IsCode(err, CodeUnauthorized) {
		t.Fatal(err)
	}
	builder, specs, _, _, _ = makeBuilder()
	specs.spec.Version = 4
	if _, err := builder.Build(context.Background(), command); !IsCode(err, CodeInvalid) {
		t.Fatal(err)
	}
	builder.Config.Limits = agentplanning.Limits{}
	if _, err := builder.Build(context.Background(), command); !IsCode(err, CodeInvalid) {
		t.Fatal(err)
	}
}

// TestRequiredApprovalBindsExactCurrentHashesAndOneUseGrant proves no execution precedes exact approval.
func TestRequiredApprovalBindsExactCurrentHashesAndOneUseGrant(t *testing.T) {
	service, _, execution, _, _ := workflowFixture(t, validPlanResponse())
	view, err := service.Start(context.Background(), validCommand())
	if err != nil || view.Phase != PhaseAwaitingApproval || execution.callCount() != 0 {
		t.Fatalf("unexpected start: %#v %v", view, err)
	}
	approval, err := service.ApprovalView(context.Background(), view.Handle)
	if err != nil || approval.Revision != 1 || approval.ArtifactSHA256 == "" {
		t.Fatalf("unsafe view: %#v %v", approval, err)
	}
	decided, err := service.Decide(context.Background(), DecisionCommand{Principal: "user-1", Handle: view.Handle, Revision: approval.Revision, DecisionID: "decision-1", Action: DecisionApprove})
	if err != nil || decided.Phase != PhaseTerminal || decided.Outcome == nil || *decided.Outcome != OutcomePassed || execution.callCount() != 1 {
		t.Fatalf("approval failed: %#v %v", decided, err)
	}
	repeated, err := service.Decide(context.Background(), DecisionCommand{Principal: "user-1", Handle: view.Handle, Revision: approval.Revision, DecisionID: "decision-1", Action: DecisionApprove})
	if err != nil || repeated.Phase != PhaseTerminal || execution.callCount() != 1 {
		t.Fatalf("decision not idempotent: %#v %v", repeated, err)
	}
}

// TestSeparateServiceOwnersApproveAndRejectDurableWork proves passive approval commands cross process-local ownership safely.
func TestSeparateServiceOwnersApproveAndRejectDurableWork(t *testing.T) {
	first, _, _, store, _ := workflowFixture(t, validPlanResponse())
	approvalRoot := filepath.Join(store.root, "approvals")
	firstApprovalStore, err := planapproval.NewLocalStore(approvalRoot)
	if err != nil {
		t.Fatal(err)
	}
	first.deps.Approval = &PlanApprovalBridge{Store: firstApprovalStore, Sanitizer: testApprovalSanitizer{}, Clock: first.deps.Clock, Limits: planapproval.Limits{MaxBytes: 1 << 20, MaxDepth: 32, MaxEvents: 100, MaxConstraints: 10, MaxChecks: 10, MaxSteps: 100, MaxAssertions: 100, MaxEvidence: 100}}
	view, err := first.Start(context.Background(), validCommand())
	if err != nil || view.Phase != PhaseAwaitingApproval {
		t.Fatalf("start: %#v %v", view, err)
	}
	secondStore, err := OpenLocalStore(store.root)
	if err != nil {
		t.Fatal(err)
	}
	secondApprovalStore, err := planapproval.NewLocalStore(approvalRoot)
	if err != nil {
		t.Fatal(err)
	}
	secondDeps := first.deps
	secondDeps.Owner, secondDeps.Store = "owner-2", secondStore
	secondDeps.Approval = &PlanApprovalBridge{Store: secondApprovalStore, Sanitizer: testApprovalSanitizer{}, Clock: secondDeps.Clock, Limits: planapproval.Limits{MaxBytes: 1 << 20, MaxDepth: 32, MaxEvents: 100, MaxConstraints: 10, MaxChecks: 10, MaxSteps: 100, MaxAssertions: 100, MaxEvidence: 100}}
	second, err := NewService(first.config, secondDeps)
	if err != nil {
		t.Fatal(err)
	}
	approvalView, err := second.ApprovalView(context.Background(), view.Handle)
	if err != nil {
		t.Fatal(err)
	}
	decided, err := second.Decide(context.Background(), DecisionCommand{Principal: "user-1", Handle: view.Handle, DecisionID: "decision-cross-owner", Revision: approvalView.Revision, Action: DecisionReject})
	if err != nil || decided.Phase != PhaseTerminal || decided.Outcome == nil || *decided.Outcome != OutcomeCancelled {
		t.Fatalf("decision: %#v %v", decided, err)
	}
	if _, err = second.Decide(context.Background(), DecisionCommand{Principal: "user-1", Handle: view.Handle, DecisionID: "decision-cross-owner", Revision: approvalView.Revision, Action: DecisionApprove}); !IsCode(err, CodeConflict) {
		t.Fatalf("changed replay accepted: %v", err)
	}
	approveCommand := validCommand()
	approveCommand.IdempotencyKey = "cross-approve"
	approveView, err := first.Start(context.Background(), approveCommand)
	if err != nil {
		t.Fatal(err)
	}
	approveRevision, err := second.ApprovalView(context.Background(), approveView.Handle)
	if err != nil {
		t.Fatal(err)
	}
	approved, err := second.Decide(context.Background(), DecisionCommand{Principal: "user-1", Handle: approveView.Handle, DecisionID: "decision-approve-owner-2", Revision: approveRevision.Revision, Action: DecisionApprove})
	if err != nil || approved.Phase != PhaseTerminal || approved.Outcome == nil || *approved.Outcome != OutcomePassed {
		t.Fatalf("cross-owner approve: %#v %v", approved, err)
	}
	cancelCommand := validCommand()
	cancelCommand.IdempotencyKey = "cross-cancel"
	cancelView, err := first.Start(context.Background(), cancelCommand)
	if err != nil {
		t.Fatal(err)
	}
	cancelled, err := second.Cancel(context.Background(), "user-1", cancelView.Handle)
	if err != nil || cancelled.Phase != PhaseTerminal || cancelled.Outcome == nil || *cancelled.Outcome != OutcomeCancelled {
		t.Fatalf("cross-owner cancel: %#v %v", cancelled, err)
	}
}

// TestPreviewApprovalRestoresSealedAuthorizationAcrossOwners proves preview approval executes both locally and after composition restart.
func TestPreviewApprovalRestoresSealedAuthorizationAcrossOwners(t *testing.T) {
	for _, crossOwner := range []bool{false, true} {
		t.Run(map[bool]string{false: "same_owner", true: "cross_owner"}[crossOwner], func(t *testing.T) {
			first, _, execution, store, _ := workflowFixture(t, validPlanResponse())
			preview := &fakePreview{digest: strings.Repeat("9", 64)}
			first.deps.Preview = preview
			approvalRoot := filepath.Join(store.root, "preview-approvals")
			approvalStore, err := planapproval.NewLocalStore(approvalRoot)
			if err != nil {
				t.Fatal(err)
			}
			first.deps.Approval = &PlanApprovalBridge{Store: approvalStore, Sanitizer: testApprovalSanitizer{}, Clock: first.deps.Clock, Limits: planapproval.Limits{MaxBytes: 1 << 20, MaxDepth: 32, MaxEvents: 100, MaxConstraints: 10, MaxChecks: 10, MaxSteps: 100, MaxAssertions: 100, MaxEvidence: 100}}
			command := validCommand()
			command.Target.Direct = nil
			command.Target.Preview = &PreviewTarget{Alias: "preview-1", Digest: strings.Repeat("8", 64)}
			view, err := first.Start(context.Background(), command)
			if err != nil {
				t.Fatal(err)
			}
			active := first
			if crossOwner {
				secondStore, openErr := OpenLocalStore(store.root)
				if openErr != nil {
					t.Fatal(openErr)
				}
				secondApproval, openErr := planapproval.NewLocalStore(approvalRoot)
				if openErr != nil {
					t.Fatal(openErr)
				}
				deps := first.deps
				deps.Owner, deps.Store = "preview-owner-2", secondStore
				deps.Approval = &PlanApprovalBridge{Store: secondApproval, Sanitizer: testApprovalSanitizer{}, Clock: deps.Clock, Limits: planapproval.Limits{MaxBytes: 1 << 20, MaxDepth: 32, MaxEvents: 100, MaxConstraints: 10, MaxChecks: 10, MaxSteps: 100, MaxAssertions: 100, MaxEvidence: 100}}
				active, err = NewService(first.config, deps)
				if err != nil {
					t.Fatal(err)
				}
			}
			approvalView, err := active.ApprovalView(context.Background(), view.Handle)
			if err != nil {
				t.Fatal(err)
			}
			decided, err := active.Decide(context.Background(), DecisionCommand{Principal: command.Principal, Handle: view.Handle, DecisionID: "preview-decision", Revision: approvalView.Revision, Action: DecisionApprove})
			if err != nil || decided.Outcome == nil || *decided.Outcome != OutcomePassed || execution.callCount() != 1 {
				t.Fatalf("preview approval: %#v %v calls=%d", decided, err, execution.callCount())
			}
			if execution.request.Snapshot.TargetDigest != preview.expectedDigest() {
				t.Fatal("runner did not receive the restored sealed preview digest")
			}
		})
	}
}

// TestPrivateExecutableTamperingIsRejectedBeforeExecution proves another schema-valid value cannot escape artifact commitments.
func TestPrivateExecutableTamperingIsRejectedBeforeExecution(t *testing.T) {
	for _, approvalDisabled := range []bool{false, true} {
		t.Run(map[bool]string{false: "required", true: "disabled_recovery"}[approvalDisabled], func(t *testing.T) {
			service, _, execution, store, clock := workflowFixture(t, validPlanResponse())
			view, err := service.Start(context.Background(), validCommand())
			if err != nil {
				t.Fatal(err)
			}
			material, err := service.deps.Executables.Load(context.Background(), view.Handle)
			if err != nil {
				t.Fatal(err)
			}
			material.Snapshot.Checks[0].Steps[0].Input = map[string]planning.InputValue{"method": {Literal: &planning.LiteralValue{Value: "another-valid-value"}}}
			raw, _ := json.Marshal(material)
			if err = os.WriteFile(filepath.Join(service.deps.Executables.(*LocalExecutableStore).root, view.Handle+".private.json"), raw, 0o600); err != nil {
				t.Fatal(err)
			}
			if !approvalDisabled {
				_, err = service.Decide(context.Background(), DecisionCommand{Principal: "user-1", Handle: view.Handle, DecisionID: "tamper-decision", Revision: 1, Action: DecisionApprove})
				if !IsCode(err, CodeConflict) || execution.callCount() != 0 {
					t.Fatalf("tampered required plan reached execution: %v", err)
				}
				return
			}
			record, _ := store.Load(context.Background(), view.Handle)
			record.Command.Approval, record.Phase, record.LeaseUntil = ApprovalDisabled, PhaseAuthorized, clock.Now().Add(-time.Second)
			if err = store.saveUnlocked(record); err != nil {
				t.Fatal(err)
			}
			clock.advance(2 * time.Second)
			if err = service.Recover(context.Background()); err != nil {
				t.Fatal(err)
			}
			recovered, _ := service.Status(context.Background(), view.Handle)
			if recovered.Outcome == nil || *recovered.Outcome != OutcomeInconclusive || execution.callCount() != 0 {
				t.Fatalf("tampered disabled plan reached execution: %#v", recovered)
			}
		})
	}
}

// TestAdmissionContextDriftIsRejectedAcrossOwners proves every canonical context dimension is rebound before approval or recovery execution.
func TestAdmissionContextDriftIsRejectedAcrossOwners(t *testing.T) {
	drifts := []struct {
		name   string
		mutate func(*AdmissionSnapshot)
	}{
		{name: "specification", mutate: func(snapshot *AdmissionSnapshot) { snapshot.SpecificationDigest = strings.Repeat("a", 64) }},
		{name: "contract", mutate: func(snapshot *AdmissionSnapshot) { snapshot.ContractDigest = strings.Repeat("b", 64) }},
		{name: "catalog", mutate: func(snapshot *AdmissionSnapshot) { snapshot.CatalogDigest = "changed-catalog" }},
		{name: "contract_policy", mutate: func(snapshot *AdmissionSnapshot) { snapshot.ContractPolicyDigest = "changed-policy" }},
		{name: "contract_context", mutate: func(snapshot *AdmissionSnapshot) { snapshot.Generation.Contract = workflowContract("changed-policy") }},
		{name: "runtime_policy", mutate: func(snapshot *AdmissionSnapshot) { snapshot.RuntimeDigest = strings.Repeat("c", 64) }},
		{name: "target_url_same_alias", mutate: func(snapshot *AdmissionSnapshot) { snapshot.Binding.Target.URL = "https://changed.example/" }},
		{name: "environment_same_alias", mutate: func(snapshot *AdmissionSnapshot) {
			snapshot.Binding.Target.EnvironmentID, snapshot.Binding.Target.Attestation.EnvironmentID = "environment-2", "environment-2"
		}},
		{name: "attestation_same_reference_kind", mutate: func(snapshot *AdmissionSnapshot) { snapshot.Binding.Target.Attestation.Reference = "proof-2" }},
		{name: "attestation_valid_until", mutate: func(snapshot *AdmissionSnapshot) {
			snapshot.Binding.Target.Attestation.ValidUntil = snapshot.Binding.Target.Attestation.ValidUntil.Add(time.Hour)
		}},
		{name: "manifest_http_same_profile", mutate: func(snapshot *AdmissionSnapshot) { snapshot.Runtime.HTTP.MaxResponseBodyBytes++ }},
		{name: "redaction_same_profile", mutate: func(snapshot *AdmissionSnapshot) { snapshot.Runtime.Redaction.MaxBodyBytes++ }},
		{name: "registry_same_profile", mutate: func(snapshot *AdmissionSnapshot) {
			snapshot.Runtime.Registry.Tools = append(snapshot.Runtime.Registry.Tools, "other")
		}},
		{name: "schema_same_version", mutate: func(snapshot *AdmissionSnapshot) {
			snapshot.Runtime.Schema.Fields = append(snapshot.Runtime.Schema.Fields, "other")
		}},
		{name: "validator_same_tool", mutate: func(snapshot *AdmissionSnapshot) { snapshot.Runtime.Validator.ToolVersion = "v2" }},
		{name: "policy_same_id", mutate: func(snapshot *AdmissionSnapshot) {
			snapshot.Runtime.Policy.AllowedTools = append(snapshot.Runtime.Policy.AllowedTools, "other")
		}},
		{name: "runtime_network_same_id", mutate: func(snapshot *AdmissionSnapshot) { snapshot.Runtime.Network.Hosts = []string{"other.example"} }},
		{name: "target_network_same_id", mutate: func(snapshot *AdmissionSnapshot) { snapshot.Binding.Target.Network.Hosts = []string{"other.example"} }},
		{name: "capability", mutate: func(snapshot *AdmissionSnapshot) {
			snapshot.Generation.Capabilities[0].Description = "changed policy metadata"
		}},
		{name: "limits", mutate: func(snapshot *AdmissionSnapshot) { snapshot.Generation.Limits.MaxChecks++ }},
		{name: "constraint", mutate: func(snapshot *AdmissionSnapshot) {
			snapshot.Generation.Constraints[0].Description = "changed specification constraint"
		}},
	}
	for _, drift := range drifts {
		for _, disabledRecovery := range []bool{false, true} {
			name := drift.name + map[bool]string{false: "_required", true: "_disabled_recovery"}[disabledRecovery]
			t.Run(name, func(t *testing.T) {
				first, admission, execution, store, clock := workflowFixture(t, validPlanResponse())
				view, err := first.Start(context.Background(), validCommand())
				if err != nil {
					t.Fatal(err)
				}
				mutateAdmissionSnapshot(admission, drift.mutate)
				if disabledRecovery {
					record, loadErr := store.Load(context.Background(), view.Handle)
					if loadErr != nil {
						t.Fatal(loadErr)
					}
					record.Command.Approval, record.Phase, record.LeaseUntil = ApprovalDisabled, PhaseAuthorized, clock.Now().Add(-time.Second)
					if err = store.saveUnlocked(record); err != nil {
						t.Fatal(err)
					}
					clock.advance(2 * time.Second)
				}
				secondStore, openErr := OpenLocalStore(store.root)
				if openErr != nil {
					t.Fatal(openErr)
				}
				deps := first.deps
				deps.Owner, deps.Store = "context-owner-2", secondStore
				second, composeErr := NewService(first.config, deps)
				if composeErr != nil {
					t.Fatal(composeErr)
				}
				if disabledRecovery {
					if err = second.Recover(context.Background()); err != nil {
						t.Fatal(err)
					}
					recovered, _ := second.Status(context.Background(), view.Handle)
					if recovered.Outcome == nil || *recovered.Outcome != OutcomeInconclusive || execution.callCount() != 0 {
						t.Fatalf("drift executed during disabled recovery: %#v", recovered)
					}
					return
				}
				_, err = second.Decide(context.Background(), DecisionCommand{Principal: "user-1", Handle: view.Handle, DecisionID: "context-drift", Revision: 1, Action: DecisionApprove})
				if !IsCode(err, CodeConflict) || execution.callCount() != 0 {
					t.Fatalf("drift reached required approval execution: %v", err)
				}
			})
		}
	}
}

// TestContractPolicyDriftDuringInferenceStopsBeforeApprovalAndExecution proves the post-model sufficiency checkpoint.
func TestContractPolicyDriftDuringInferenceStopsBeforeApprovalAndExecution(t *testing.T) {
	service, admission, execution, _, _ := workflowFixture(t, validPlanResponse())
	service.deps.Generation = driftingGeneration{inner: service.deps.Generation, admission: admission}
	view, err := service.Start(context.Background(), validCommand())
	if err != nil || view.Phase != PhaseTerminal || view.Outcome == nil || *view.Outcome != OutcomeInconclusive || execution.callCount() != 0 {
		t.Fatalf("drift crossed checkpoint: %#v %v", view, err)
	}
	if _, approvalErr := service.ApprovalView(context.Background(), view.Handle); !IsCode(approvalErr, CodeConflict) {
		t.Fatalf("approval staged after drift: %v", approvalErr)
	}
}

// TestDurableApprovalRevalidatesContractImmediatelyBeforeToolExecution closes the approval-wait mutation window with zero tool calls.
func TestDurableApprovalRevalidatesContractImmediatelyBeforeToolExecution(t *testing.T) {
	service, admission, execution, _, _ := workflowFixture(t, validPlanResponse())
	view, err := service.Start(context.Background(), validCommand())
	if err != nil || view.Phase != PhaseAwaitingApproval {
		t.Fatalf("approval was not staged: %#v %v", view, err)
	}
	mutateAdmissionSnapshot(admission, func(snapshot *AdmissionSnapshot) {
		snapshot.ContractDigest = strings.Repeat("b", 64)
	})
	_, err = service.Decide(context.Background(), DecisionCommand{Principal: "user-1", Handle: view.Handle, DecisionID: "source-mutated", Revision: view.ApprovalRevision, Action: DecisionApprove})
	if !IsCode(err, CodeConflict) || execution.callCount() != 0 {
		t.Fatalf("mutated source reached tools: %v calls=%d", err, execution.callCount())
	}
}

// mutateAdmissionSnapshot changes one trusted recomposition dimension under the fake boundary lock.
func mutateAdmissionSnapshot(admission *fakeAdmission, mutate func(*AdmissionSnapshot)) {
	admission.mu.Lock()
	defer admission.mu.Unlock()
	mutate(&admission.snapshot)
}

// TestPlanReadyContextDriftNeverStagesApproval proves recovery validates current context before creating a human request.
func TestPlanReadyContextDriftNeverStagesApproval(t *testing.T) {
	for _, drift := range []struct {
		name   string
		mutate func(*AdmissionSnapshot)
	}{
		{name: "specification", mutate: func(snapshot *AdmissionSnapshot) { snapshot.SpecificationDigest = strings.Repeat("a", 64) }},
		{name: "contract", mutate: func(snapshot *AdmissionSnapshot) { snapshot.ContractDigest = strings.Repeat("b", 64) }},
		{name: "catalog", mutate: func(snapshot *AdmissionSnapshot) { snapshot.CatalogDigest = "changed-catalog" }},
		{name: "contract_policy", mutate: func(snapshot *AdmissionSnapshot) { snapshot.ContractPolicyDigest = "changed-policy" }},
		{name: "runtime", mutate: func(snapshot *AdmissionSnapshot) { snapshot.RuntimeDigest = strings.Repeat("c", 64) }},
		{name: "environment_object", mutate: func(snapshot *AdmissionSnapshot) {
			snapshot.Binding.Target.EnvironmentID, snapshot.Binding.Target.Attestation.EnvironmentID = "environment-2", "environment-2"
		}},
		{name: "attestation_object", mutate: func(snapshot *AdmissionSnapshot) { snapshot.Binding.Target.Attestation.Reference = "proof-2" }},
		{name: "attestation_deadline", mutate: func(snapshot *AdmissionSnapshot) {
			snapshot.Binding.Target.Attestation.ValidUntil = snapshot.Binding.Target.Attestation.ValidUntil.Add(time.Hour)
		}},
		{name: "manifest_object", mutate: func(snapshot *AdmissionSnapshot) { snapshot.Runtime.HTTP.TimeoutMS++ }},
		{name: "redaction_object", mutate: func(snapshot *AdmissionSnapshot) { snapshot.Runtime.Redaction.MaxBodyBytes++ }},
		{name: "schema_object", mutate: func(snapshot *AdmissionSnapshot) {
			snapshot.Runtime.Schema.Fields = append(snapshot.Runtime.Schema.Fields, "other")
		}},
		{name: "validator_object", mutate: func(snapshot *AdmissionSnapshot) { snapshot.Runtime.Validator.AssertionVersion = "v2" }},
		{name: "policy_object_same_id", mutate: func(snapshot *AdmissionSnapshot) { snapshot.Runtime.Policy.Reason = "changed" }},
		{name: "network_object_same_id", mutate: func(snapshot *AdmissionSnapshot) { snapshot.Runtime.Network.Ports = []int{8443} }},
	} {
		t.Run(drift.name, func(t *testing.T) {
			first, admission, execution, store, clock := workflowFixture(t, validPlanResponse())
			view, err := first.Start(context.Background(), validCommand())
			if err != nil {
				t.Fatal(err)
			}
			mutateAdmissionSnapshot(admission, drift.mutate)
			record, _ := store.Load(context.Background(), view.Handle)
			record.Phase, record.LeaseUntil, record.ApprovalRevision, record.ApprovalDeadline = PhasePlanReady, clock.Now().Add(-time.Second), 0, time.Time{}
			if err = store.saveUnlocked(record); err != nil {
				t.Fatal(err)
			}
			clock.advance(2 * time.Second)
			secondStore, err := OpenLocalStore(store.root)
			if err != nil {
				t.Fatal(err)
			}
			approvalStore := planapproval.NewMemoryStore()
			deps := first.deps
			deps.Owner, deps.Store = "context-stage-owner-2", secondStore
			deps.Approval = &PlanApprovalBridge{Store: approvalStore, Sanitizer: testApprovalSanitizer{}, Clock: clock, Limits: planapproval.Limits{MaxBytes: 1 << 20, MaxDepth: 32, MaxEvents: 100, MaxConstraints: 10, MaxChecks: 10, MaxSteps: 100, MaxAssertions: 100, MaxEvidence: 100}}
			second, err := NewService(first.config, deps)
			if err != nil {
				t.Fatal(err)
			}
			if err = second.Recover(context.Background()); !IsCode(err, CodeConflict) {
				t.Fatalf("drift recovery error=%v", err)
			}
			events, eventsErr := approvalStore.Events(context.Background(), view.Handle)
			if eventsErr != nil || len(events) != 0 || execution.callCount() != 0 {
				t.Fatalf("drift staged or executed: events=%d err=%v calls=%d", len(events), eventsErr, execution.callCount())
			}
		})
	}
}

// TestPlanReadyRecoveryRestagesOrPreservesDisabledMode proves the checkpoint reconciles the configured approval branch.
func TestPlanReadyRecoveryRestagesOrPreservesDisabledMode(t *testing.T) {
	for _, disabled := range []bool{false, true} {
		t.Run(map[bool]string{false: "required_restage", true: "disabled_execute"}[disabled], func(t *testing.T) {
			service, _, execution, store, clock := workflowFixture(t, validPlanResponse())
			view, err := service.Start(context.Background(), validCommand())
			if err != nil {
				t.Fatal(err)
			}
			record, _ := store.Load(context.Background(), view.Handle)
			record.Phase, record.LeaseUntil, record.ApprovalRevision, record.ApprovalDeadline = PhasePlanReady, clock.Now().Add(-time.Second), 0, time.Time{}
			if disabled {
				record.Command.Approval = ApprovalDisabled
			}
			if err = store.saveUnlocked(record); err != nil {
				t.Fatal(err)
			}
			clock.advance(2 * time.Second)
			secondStore, err := OpenLocalStore(store.root)
			if err != nil {
				t.Fatal(err)
			}
			deps := service.deps
			deps.Owner, deps.Store = "plan-ready-owner-2", secondStore
			if !disabled {
				deps.Approval = &PlanApprovalBridge{Store: planapproval.NewMemoryStore(), Sanitizer: testApprovalSanitizer{}, Clock: clock, Limits: planapproval.Limits{MaxBytes: 1 << 20, MaxDepth: 32, MaxEvents: 100, MaxConstraints: 10, MaxChecks: 10, MaxSteps: 100, MaxAssertions: 100, MaxEvidence: 100}}
			}
			second, err := NewService(service.config, deps)
			if err != nil {
				t.Fatal(err)
			}
			if err = second.Recover(context.Background()); err != nil {
				t.Fatal(err)
			}
			recovered, _ := second.Status(context.Background(), view.Handle)
			if disabled && (recovered.Outcome == nil || *recovered.Outcome != OutcomePassed || execution.callCount() != 1) {
				t.Fatalf("disabled mode changed during recovery: %#v", recovered)
			}
			if !disabled && (recovered.Phase != PhaseAwaitingApproval || recovered.ApprovalRevision != 1 || execution.callCount() != 0) {
				t.Fatalf("approval was not restaged: %#v", recovered)
			}
		})
	}
}

// TestPlanReadyRecoveryReconcilesAlreadyStagedApprovalAcrossProcesses proves a crash after approval persistence does not create a new revision.
func TestPlanReadyRecoveryReconcilesAlreadyStagedApprovalAcrossProcesses(t *testing.T) {
	first, _, execution, store, clock := workflowFixture(t, validPlanResponse())
	approvalRoot := filepath.Join(store.root, "plan-ready-approval")
	firstApproval, err := planapproval.NewLocalStore(approvalRoot)
	if err != nil {
		t.Fatal(err)
	}
	limits := planapproval.Limits{MaxBytes: 1 << 20, MaxDepth: 32, MaxEvents: 100, MaxConstraints: 10, MaxChecks: 10, MaxSteps: 100, MaxAssertions: 100, MaxEvidence: 100}
	first.deps.Approval = &PlanApprovalBridge{Store: firstApproval, Sanitizer: testApprovalSanitizer{}, Clock: clock, Limits: limits}
	view, err := first.Start(context.Background(), validCommand())
	if err != nil {
		t.Fatal(err)
	}
	record, _ := store.Load(context.Background(), view.Handle)
	record.Phase, record.LeaseUntil, record.ApprovalRevision, record.ApprovalDeadline = PhasePlanReady, clock.Now().Add(-time.Second), 0, time.Time{}
	if err = store.saveUnlocked(record); err != nil {
		t.Fatal(err)
	}
	clock.advance(2 * time.Second)
	secondStore, err := OpenLocalStore(store.root)
	if err != nil {
		t.Fatal(err)
	}
	secondApproval, err := planapproval.NewLocalStore(approvalRoot)
	if err != nil {
		t.Fatal(err)
	}
	deps := first.deps
	deps.Owner, deps.Store = "restage-owner-2", secondStore
	deps.Approval = &PlanApprovalBridge{Store: secondApproval, Sanitizer: testApprovalSanitizer{}, Clock: clock, Limits: limits}
	second, err := NewService(first.config, deps)
	if err != nil {
		t.Fatal(err)
	}
	if err = second.Recover(context.Background()); err != nil {
		t.Fatal(err)
	}
	recovered, _ := second.Status(context.Background(), view.Handle)
	if recovered.Phase != PhaseAwaitingApproval || recovered.ApprovalRevision != 1 || execution.callCount() != 0 {
		t.Fatalf("already staged approval was not reconciled exactly: %#v", recovered)
	}
}

// TestApprovalDeadlineExpiresDurablyAcrossOwners proves abandoned approval is cancelled at the inclusive boundary.
func TestApprovalDeadlineExpiresDurablyAcrossOwners(t *testing.T) {
	first, _, execution, store, clock := workflowFixture(t, validPlanResponse())
	view, err := first.Start(context.Background(), validCommand())
	if err != nil {
		t.Fatal(err)
	}
	record, _ := store.Load(context.Background(), view.Handle)
	clock.advance(first.config.ApprovalTTL)
	secondStore, err := OpenLocalStore(store.root)
	if err != nil {
		t.Fatal(err)
	}
	deps := first.deps
	deps.Owner, deps.Store = "deadline-owner-2", secondStore
	second, err := NewService(first.config, deps)
	if err != nil {
		t.Fatal(err)
	}
	if clock.Now().Before(record.ApprovalDeadline) {
		t.Fatal("fake clock did not reach durable deadline")
	}
	if err = second.Recover(context.Background()); err != nil {
		t.Fatal(err)
	}
	expired, _ := second.Status(context.Background(), view.Handle)
	if expired.Outcome == nil || *expired.Outcome != OutcomeCancelled || execution.callCount() != 0 {
		t.Fatalf("approval deadline did not converge: %#v", expired)
	}
	decisionService, _, decisionExecution, _, decisionClock := workflowFixture(t, validPlanResponse())
	decisionView, err := decisionService.Start(context.Background(), validCommand())
	if err != nil {
		t.Fatal(err)
	}
	decisionClock.advance(decisionService.config.ApprovalTTL)
	decisionExpired, err := decisionService.Decide(context.Background(), DecisionCommand{Principal: "user-1", Handle: decisionView.Handle, DecisionID: "expired-decision", Revision: 1, Action: DecisionApprove})
	if err != nil || decisionExpired.Outcome == nil || *decisionExpired.Outcome != OutcomeCancelled || decisionExecution.callCount() != 0 {
		t.Fatalf("decision at deadline executed: %#v %v", decisionExpired, err)
	}
}

// TestApprovalRestorePropagatesCancellation proves RestoreRun receives the operation context instead of a background context.
func TestApprovalRestorePropagatesCancellation(t *testing.T) {
	observed := make(chan error, 1)
	bridge := &PlanApprovalBridge{Store: blockingRestoreStore{ApprovalStore: planapproval.NewMemoryStore(), observed: observed}, Sanitizer: testApprovalSanitizer{}, Clock: &testClock{now: time.Unix(1, 0)}, Limits: planapproval.Limits{MaxBytes: 1 << 20, MaxDepth: 32, MaxEvents: 100, MaxConstraints: 10, MaxChecks: 10, MaxSteps: 100, MaxAssertions: 100, MaxEvidence: 100}}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := bridge.View(ctx, "workflow-cancel")
	if err == nil || !errors.Is(<-observed, context.Canceled) {
		t.Fatalf("restore did not observe cancellation: %v", err)
	}
}

// TestStaleRejectedAndDisabledApprovalPaths covers stale CAS, rejection and explicit no-approval policy.
func TestStaleRejectedAndDisabledApprovalPaths(t *testing.T) {
	service, _, execution, _, _ := workflowFixture(t, validPlanResponse())
	view, _ := service.Start(context.Background(), validCommand())
	_, err := service.Decide(context.Background(), DecisionCommand{Principal: "user-1", Handle: view.Handle, Revision: 2, DecisionID: "decision-stale", Action: DecisionApprove})
	if !IsCode(err, CodeConflict) || execution.callCount() != 0 {
		t.Fatal("stale approval executed")
	}
	rejected, err := service.Decide(context.Background(), DecisionCommand{Principal: "user-1", Handle: view.Handle, Revision: 1, DecisionID: "decision-reject", Action: DecisionReject})
	if err != nil || rejected.Outcome == nil || *rejected.Outcome != OutcomeCancelled || execution.callCount() != 0 {
		t.Fatal("rejection did not stop")
	}
	autoService, _, autoExecution, _, _ := workflowFixture(t, validPlanResponse())
	command := validCommand()
	command.Approval = ApprovalDisabled
	auto, err := autoService.Start(context.Background(), command)
	if err != nil || auto.Outcome == nil || *auto.Outcome != OutcomePassed || autoExecution.callCount() != 1 {
		t.Fatalf("disabled approval policy failed: %#v %v", auto, err)
	}
}

// TestInsufficientContextIsSafeDistinctTerminalWithoutPackageOrTools proves missing context cannot trigger execution.
func TestInsufficientContextIsSafeDistinctTerminalWithoutPackageOrTools(t *testing.T) {
	service, _, execution, _, _ := workflowFixture(t, missingResponse())
	view, err := service.Start(context.Background(), validCommand())
	if err != nil || view.Outcome == nil || *view.Outcome != OutcomeInsufficientContext || execution.callCount() != 0 {
		t.Fatalf("unexpected missing result: %#v %v", view, err)
	}
	result, err := service.Result(context.Background(), view.Handle)
	if err != nil || result.Missing == nil || result.Package != nil {
		t.Fatalf("invalid terminal union: %#v %v", result, err)
	}
}

// TestConcurrentStartAndRecoveryNeverDuplicateWork proves cross-instance idempotency and expired generation recovery.
func TestConcurrentStartAndRecoveryNeverDuplicateWork(t *testing.T) {
	service, _, _, store, clock := workflowFixture(t, validPlanResponse())
	command := validCommand()
	var wg sync.WaitGroup
	views := make(chan WorkflowView, 2)
	for range 2 {
		wg.Add(1)
		go func() { defer wg.Done(); view, _ := service.Start(context.Background(), command); views <- view }()
	}
	wg.Wait()
	close(views)
	first := ""
	for view := range views {
		if first == "" {
			first = view.Handle
		}
		if view.Handle != first {
			t.Fatalf("duplicate handles: %s %s", first, view.Handle)
		}
	}
	record, err := store.Load(context.Background(), first)
	if err != nil {
		t.Fatal(err)
	}
	if record.Phase != PhaseAwaitingApproval {
		t.Fatal(record.Phase)
	}
	record.Phase, record.LeaseUntil = PhaseGenerating, clock.Now().Add(-time.Second)
	if err = store.saveUnlocked(record); err != nil {
		t.Fatal(err)
	}
	clock.advance(2 * time.Second)
	if err = service.Recover(context.Background()); err != nil {
		t.Fatal(err)
	}
	recovered, _ := service.Status(context.Background(), first)
	if recovered.Outcome == nil || *recovered.Outcome != OutcomeInconclusive {
		t.Fatalf("uncertain generation repeated: %#v", recovered)
	}
}

// TestHeartbeatPreventsOverlapAndReclaimRequiresExpiry proves leases renew during work and become claimable only after the worker joins.
func TestHeartbeatPreventsOverlapAndReclaimRequiresExpiry(t *testing.T) {
	service, _, _, store, clock := workflowFixture(t, validPlanResponse())
	waiter := &controlledWaiter{ready: make(chan struct{}), tick: make(chan struct{})}
	service.deps.Waiter, service.config.HeartbeatInterval = waiter, 10*time.Second
	record := Record{Version: 1, Handle: "workflow-heartbeat", Principal: "user-1", IdempotencyKey: "heartbeat", RequestDigest: strings.Repeat("a", 64), Phase: PhaseAdmitted}
	record, _, err := store.Reserve(context.Background(), record, "owner-1", clock.Now(), service.config.LeaseTTL)
	if err != nil {
		t.Fatal(err)
	}
	_, worker, err := service.beginWork(context.Background(), record.Handle)
	if err != nil {
		t.Fatal(err)
	}
	<-waiter.ready
	clock.advance(50 * time.Second)
	waiter.tick <- struct{}{}
	<-waiter.ready
	if _, claimed, claimErr := store.Claim(context.Background(), record.Handle, "owner-2", clock.Now(), time.Minute); claimErr != nil || claimed {
		t.Fatalf("renewed lease reclaimed: %v %v", claimed, claimErr)
	}
	service.endWork(record.Handle, worker)
	clock.advance(61 * time.Second)
	if _, claimed, claimErr := store.Claim(context.Background(), record.Handle, "owner-2", clock.Now(), time.Minute); claimErr != nil || !claimed {
		t.Fatalf("expired lease not reclaimed: %v %v", claimed, claimErr)
	}
}

// TestCrashAtEveryCheckpointConvergesSafely exercises the closed recovery matrix without repeating uncertain inference or tools.
func TestCrashAtEveryCheckpointConvergesSafely(t *testing.T) {
	for _, scenario := range []struct {
		phase         Phase
		expectedPhase Phase
		expected      Outcome
	}{
		{PhaseAdmitted, PhaseTerminal, OutcomeInconclusive}, {PhaseGenerating, PhaseTerminal, OutcomeInconclusive},
		{PhasePlanReady, PhaseAwaitingApproval, ""}, {PhaseAuthorized, PhaseTerminal, OutcomeInconclusive},
		{PhaseExecuting, PhaseTerminal, OutcomeInconclusive}, {PhaseFinalizing, PhaseTerminal, OutcomeError},
	} {
		t.Run(string(scenario.phase), func(t *testing.T) {
			service, _, _, store, clock := workflowFixture(t, validPlanResponse())
			view, err := service.Start(context.Background(), validCommand())
			if err != nil {
				t.Fatal(err)
			}
			record, _ := store.Load(context.Background(), view.Handle)
			record.Phase, record.LeaseUntil = scenario.phase, clock.Now().Add(-time.Second)
			record.Outcome = nil
			if err = store.saveUnlocked(record); err != nil {
				t.Fatal(err)
			}
			if err = service.Recover(context.Background()); err != nil {
				t.Fatal(err)
			}
			recovered, err := service.Status(context.Background(), view.Handle)
			if err != nil || recovered.Phase != scenario.expectedPhase {
				t.Fatalf("phase=%s view=%#v err=%v", scenario.phase, recovered, err)
			}
			if scenario.expected != "" && (recovered.Outcome == nil || *recovered.Outcome != scenario.expected) {
				t.Fatalf("outcome=%#v", recovered)
			}
		})
	}
	service, _, _, store, _ := workflowFixture(t, validPlanResponse())
	view, _ := service.Start(context.Background(), validCommand())
	before, _ := store.Load(context.Background(), view.Handle)
	if err := service.Recover(context.Background()); err != nil {
		t.Fatal(err)
	}
	after, _ := store.Load(context.Background(), view.Handle)
	if before.Phase != after.Phase || after.Owner != "" {
		t.Fatal("passive approval state changed")
	}
}

// TestCancellationAtEveryPhaseStopsFinalizesAndIsIdempotent proves cancellation is durable and repeatable.
func TestCancellationAtEveryPhaseStopsFinalizesAndIsIdempotent(t *testing.T) {
	service, _, execution, _, _ := workflowFixture(t, validPlanResponse())
	view, _ := service.Start(context.Background(), validCommand())
	cancelled, err := service.Cancel(context.Background(), "user-1", view.Handle)
	if err != nil || cancelled.Outcome == nil || *cancelled.Outcome != OutcomeCancelled || execution.callCount() != 0 {
		t.Fatalf("cancel failed: %#v %v", cancelled, err)
	}
	repeated, err := service.Cancel(context.Background(), "user-1", view.Handle)
	if err != nil || repeated.Outcome == nil || *repeated.Outcome != OutcomeCancelled {
		t.Fatal("cancel not idempotent")
	}
}

// TestCancelPersistsSignalsAndJoinsExecutingWorker proves cancellation returns only after active execution stops.
func TestCancelPersistsSignalsAndJoinsExecutingWorker(t *testing.T) {
	service, _, _, _, _ := workflowFixture(t, validPlanResponse())
	view, err := service.Start(context.Background(), validCommand())
	if err != nil {
		t.Fatal(err)
	}
	blocking := &blockingExecution{started: make(chan struct{}), stopped: make(chan struct{})}
	service.deps.Execution = blocking
	decisionDone := make(chan error, 1)
	go func() {
		_, decisionErr := service.Decide(context.Background(), DecisionCommand{Principal: "user-1", Handle: view.Handle, DecisionID: "cancel-race", Revision: 1, Action: DecisionApprove})
		decisionDone <- decisionErr
	}()
	<-blocking.started
	cancelled, err := service.Cancel(context.Background(), "user-1", view.Handle)
	if err != nil || cancelled.Outcome == nil || *cancelled.Outcome != OutcomeCancelled {
		t.Fatalf("cancel failed: %#v %v", cancelled, err)
	}
	select {
	case <-blocking.stopped:
	default:
		t.Fatal("cancel returned before execution stopped")
	}
	if decisionErr := <-decisionDone; decisionErr == nil {
		t.Fatal("cancelled decision unexpectedly reported successful execution")
	}
}

// TestCancelPersistsSignalsAndJoinsGeneratingWorker proves inference observes cancellation without a hidden retry.
func TestCancelPersistsSignalsAndJoinsGeneratingWorker(t *testing.T) {
	service, _, _, _, _ := workflowFixture(t, validPlanResponse())
	blocking := &blockingGeneration{started: make(chan struct{}), stopped: make(chan struct{})}
	service.deps.Generation = blocking
	startDone := make(chan error, 1)
	go func() {
		_, startErr := service.Start(context.Background(), validCommand())
		startDone <- startErr
	}()
	<-blocking.started
	cancelled, err := service.Cancel(context.Background(), "user-1", "workflow-1")
	if err != nil || cancelled.Outcome == nil || *cancelled.Outcome != OutcomeCancelled {
		t.Fatalf("cancel failed: %#v %v", cancelled, err)
	}
	select {
	case <-blocking.stopped:
	default:
		t.Fatal("cancel returned before generation stopped")
	}
	if startErr := <-startDone; startErr == nil {
		t.Fatal("cancelled generation unexpectedly completed")
	}
}

// TestPlanArtifactIsCanonicalBoundAndTamperDetected proves approval and execution reject changed plan bytes.
func TestPlanArtifactIsCanonicalBoundAndTamperDetected(t *testing.T) {
	service, _, execution, _, _ := workflowFixture(t, validPlanResponse())
	view, _ := service.Start(context.Background(), validCommand())
	artifacts := service.deps.Artifacts.(*LocalArtifactStore)
	artifact, _ := artifacts.Load(context.Background(), view.Handle)
	artifact.Plans[0].ID = "changed"
	raw, _ := json.Marshal(artifact)
	if err := os.WriteFile(filepath.Join(artifacts.root, view.Handle+".plan.json"), raw, 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := service.ApprovalView(context.Background(), view.Handle)
	if !IsCode(err, CodeConflict) || execution.callCount() != 0 {
		t.Fatal("tampered artifact accepted")
	}
}

// TestPlanArtifactMergesScenarioChecksWithoutStepCollisions proves all prepared scenarios reach the historical runner contract.
func TestPlanArtifactMergesScenarioChecksWithoutStepCollisions(t *testing.T) {
	response := []byte(`{"version":1,"kind":"plan","plan":{"id":"workflow-1-plan","scenarios":[{"constraint_id":"c1","scenario":"positive","status":"planned"},{"constraint_id":"c1","scenario":"negative","status":"planned"},{"constraint_id":"c1","scenario":"error","status":"not_applicable","reason":"not retained"}],"checks":[{"id":"check-a","constraint_id":"c1","scenario":"positive","capability":"probe","steps":[{"id":"step","tool":"probe","input":{},"assertions":[{"id":"assert-a","definition":{"operator":"exists"}}]}],"evidence":[{"step_id":"step","assertion_ids":["assert-a"]}]},{"id":"check-b","constraint_id":"c1","scenario":"negative","capability":"probe","steps":[{"id":"step","tool":"probe","input":{},"assertions":[{"id":"assert-b","definition":{"operator":"exists"}}]}],"evidence":[{"step_id":"step","assertion_ids":["assert-b"]}]}]}}`)
	service, _, _, _, _ := workflowFixture(t, response)
	view, err := service.Start(context.Background(), validCommand())
	if err != nil {
		t.Fatal(err)
	}
	artifact, err := service.deps.Artifacts.Load(context.Background(), view.Handle)
	if err != nil {
		t.Fatal(err)
	}
	if len(artifact.Plans) != 1 || len(artifact.Plans[0].Steps) != 2 || artifact.Plans[0].Steps[0].ID != "check-a-step" || artifact.Plans[0].Steps[1].ID != "check-b-step" {
		t.Fatalf("scenario checks were not merged safely: %#v", artifact.Plans)
	}
}

// TestWorkflowCLIAPIAndMCPContractParity proves adapters expose the same durable status projection.
func TestWorkflowCLIAPIAndMCPContractParity(t *testing.T) {
	service, _, _, _, _ := workflowFixture(t, validPlanResponse())
	view, _ := service.Start(context.Background(), validCommand())
	expected, _ := service.Status(context.Background(), view.Handle)
	var output strings.Builder
	if code := (CLI{Service: service, Principal: "user-1"}).Run(context.Background(), []string{"status", view.Handle}, &output); code != 0 {
		t.Fatal(code)
	}
	var cliView WorkflowView
	if json.Unmarshal([]byte(output.String()), &cliView) != nil || cliView.Handle != expected.Handle || cliView.Phase != expected.Phase {
		t.Fatal(output.String())
	}
	response := (MCPServer{Service: service, Principal: "user-1"}).Handle(context.Background(), MCPRequest{Version: ContractVersion, Operation: "workflow.status", Arguments: json.RawMessage(`{"handle":"` + view.Handle + `"}`)})
	projected, ok := response.Value.(WorkflowView)
	if !ok || projected.Handle != expected.Handle || projected.Phase != expected.Phase {
		t.Fatalf("MCP mismatch: %#v", response)
	}
}

// TestWorkflowCLICommandsAndExactStartParser covers every CLI operation and rejects ambiguous aliases.
func TestWorkflowCLICommandsAndExactStartParser(t *testing.T) {
	direct := DirectTarget{Alias: "target-1", Digest: strings.Repeat("6", 64)}
	service, _, _, _, _ := workflowFixture(t, validPlanResponse())
	cli := CLI{Service: service, Principal: "user-1", AuthProfiles: map[string]string{"profile-1": strings.Repeat("8", 64)}, DirectTargets: map[string]DirectTarget{"target-1": direct}, PreviewTargets: map[string]PreviewTarget{}}
	start := []string{"start", "--spec", "spec-1", "--contract", "contract-1", "--provider", "fake", "--model", "selected", "--profile", "profile-1", "--runtime-profile", "runtime-1", "--repository", "repo-1", "--revision", strings.Repeat("a", 40), "--approval", "required", "--idempotency-key", "request-cli", "--target", "target-1"}
	var output strings.Builder
	if code := cli.Run(context.Background(), start, &output); code != 0 {
		t.Fatalf("start code=%d %s", code, output.String())
	}
	var view WorkflowView
	if json.Unmarshal([]byte(output.String()), &view) != nil || view.Phase != PhaseAwaitingApproval {
		t.Fatal(output.String())
	}
	for _, args := range [][]string{{"status", view.Handle}, {"view", view.Handle}} {
		output.Reset()
		if code := cli.Run(context.Background(), args, &output); code != 0 {
			t.Fatalf("%v code=%d", args, code)
		}
	}
	output.Reset()
	if code := cli.Run(context.Background(), []string{"approve", view.Handle, "1", "decision-cli"}, &output); code != 0 {
		t.Fatal(output.String())
	}
	output.Reset()
	if code := cli.Run(context.Background(), []string{"result", view.Handle}, &output); code != 0 {
		t.Fatal(output.String())
	}
	output.Reset()
	if code := cli.Run(context.Background(), []string{"cancel", view.Handle}, &output); code != 0 {
		t.Fatal(output.String())
	}
	if code := cli.Run(context.Background(), []string{"unknown"}, io.Discard); code != 2 {
		t.Fatal(code)
	}
	if _, err := parseStartCLI(append(start[1:], "--preview", "missing"), map[string]string{"profile-1": strings.Repeat("8", 64)}, map[string]DirectTarget{"target-1": direct}, map[string]PreviewTarget{"missing": {Alias: "missing", Digest: strings.Repeat("7", 64)}}); !IsCode(err, CodeInvalid) {
		t.Fatal("ambiguous target accepted")
	}
	if _, ok := parseUint("18446744073709551616"); ok {
		t.Fatal("overflow revision accepted")
	}
}

// TestMCPWorkflowOperationsShareService covers the versioned tool surface and safe errors.
func TestMCPWorkflowOperationsShareService(t *testing.T) {
	service, _, _, _, _ := workflowFixture(t, validPlanResponse())
	view, _ := service.Start(context.Background(), validCommand())
	server := MCPServer{Service: service, Principal: "user-1"}
	for _, operation := range []struct{ name, arguments string }{
		{"workflow.status", `{"handle":"` + view.Handle + `"}`}, {"workflow.view", `{"handle":"` + view.Handle + `"}`},
	} {
		response := server.Handle(context.Background(), MCPRequest{Version: ContractVersion, Operation: operation.name, Arguments: json.RawMessage(operation.arguments)})
		if response.Error != "" || response.Value == nil {
			t.Fatalf("%s: %#v", operation.name, response)
		}
	}
	decision := server.Handle(context.Background(), MCPRequest{Version: ContractVersion, Operation: "workflow.decide", Arguments: json.RawMessage(`{"handle":"` + view.Handle + `","decision_id":"mcp-decision","revision":1,"action":"approve"}`)})
	if decision.Error != "" {
		t.Fatalf("decide: %#v", decision)
	}
	for _, name := range []string{"workflow.result", "workflow.cancel"} {
		response := server.Handle(context.Background(), MCPRequest{Version: ContractVersion, Operation: name, Arguments: json.RawMessage(`{"handle":"` + view.Handle + `"}`)})
		if response.Error != "" {
			t.Fatalf("%s: %#v", name, response)
		}
	}
	if response := server.Handle(context.Background(), MCPRequest{Version: "wrong", Operation: "workflow.status"}); response.Error != CodeInvalid {
		t.Fatal(response)
	}
	if response := server.Handle(context.Background(), MCPRequest{Version: ContractVersion, Operation: "unknown", Arguments: json.RawMessage(`{}`)}); response.Error != CodeInvalid {
		t.Fatal(response)
	}
}

// TestAdapterRejectsMalformedCommands covers exact arity, revisions, payloads and service failures.
func TestAdapterRejectsMalformedCommands(t *testing.T) {
	service, _, _, _, _ := workflowFixture(t, validPlanResponse())
	cli := CLI{Service: service, Principal: "user-1"}
	for _, args := range [][]string{{}, {"status"}, {"approve", "h", "bad", "d"}, {"result"}, {"start", "--bad", "x"}} {
		if code := cli.Run(context.Background(), args, io.Discard); code != 2 {
			t.Fatalf("args=%v code=%d", args, code)
		}
	}
	if code := (CLI{}).Run(context.Background(), []string{"status", "h"}, io.Discard); code != 2 {
		t.Fatal(code)
	}
	server := MCPServer{Service: service, Principal: "user-1"}
	for _, request := range []MCPRequest{{Version: ContractVersion, Operation: "workflow.status", Arguments: json.RawMessage(`{bad`)}, {Version: ContractVersion, Operation: "workflow.decide", Arguments: json.RawMessage(`{}`)}, {Version: ContractVersion, Operation: "workflow.result", Arguments: json.RawMessage(`{}`)}} {
		if response := server.Handle(context.Background(), request); response.Error == "" {
			t.Fatalf("accepted %#v", request)
		}
	}
	if decodeStrict([]byte(`{} {}`), &struct{}{}) == nil || decodeStrict([]byte(`{"extra":1}`), &struct{}{}) == nil {
		t.Fatal("strict decoder widened")
	}
	if _, ok := parseUint(""); ok {
		t.Fatal("empty revision")
	}
}

// TestMCPStartUsesTrustedAliasAndRejectsCallerTargetValues proves remote callers cannot supply raw target digests.
func TestMCPStartUsesTrustedAliasAndRejectsCallerTargetValues(t *testing.T) {
	service, _, _, _, _ := workflowFixture(t, validPlanResponse())
	server := MCPServer{Service: service, Principal: "user-1", AuthProfiles: map[string]string{"profile-1": strings.Repeat("8", 64)}, DirectTargets: map[string]DirectTarget{"target-1": {Alias: "target-1", Digest: strings.Repeat("6", 64)}}}
	request := map[string]any{"idempotency_key": "request-mcp-start", "specification_ref": "spec-1", "contract_ref": "contract-1", "provider": "fake", "model": "selected", "auth_profile": "profile-1", "runtime_profile": "runtime-1", "repository_id": "repo-1", "revision": strings.Repeat("a", 40), "approval": "required", "target_alias": "target-1"}
	raw, _ := json.Marshal(request)
	response := server.Handle(context.Background(), MCPRequest{Version: ContractVersion, Operation: "workflow.start", Arguments: raw})
	if response.Error != "" {
		t.Fatalf("trusted alias rejected: %#v", response)
	}
	request["target"] = map[string]any{"Direct": map[string]any{"Alias": "evil", "Digest": strings.Repeat("9", 64)}}
	raw, _ = json.Marshal(request)
	response = server.Handle(context.Background(), MCPRequest{Version: ContractVersion, Operation: "workflow.start", Arguments: raw})
	if response.Error != CodeInvalid {
		t.Fatal("caller target accepted")
	}
}

// TestGitHubWorkflowStatusResolveAndCancel cover the complete gateway projection after admission.
func TestGitHubWorkflowStatusResolveAndCancel(t *testing.T) {
	service, _, _, _, _ := workflowFixture(t, validPlanResponse())
	command := validCommand()
	command.IdempotencyKey = "template"
	snapshot, _ := githubpr.SealSnapshot(githubpr.PullRequestSnapshot{Version: 1, InstallationID: 1, RepositoryID: 7, Number: 2, AuthorID: 9, State: "open", HeadOID: strings.Repeat("a", 40), HeadRepositoryID: 7})
	gateway := GitHubGateway{Service: service, Principal: "user-1", Commands: map[string]StartCommand{"default": command}}
	state, err := gateway.Submit(context.Background(), githubpr.Selection{Alias: "default"}, snapshot, "github-request-2")
	if err != nil {
		t.Fatal(err)
	}
	if status, err := gateway.Status(context.Background(), state.RunHandle); err != nil || status.Phase != "running" {
		t.Fatalf("status: %#v %v", status, err)
	}
	if resolved, err := gateway.ResolveExisting(context.Background(), githubpr.ExistingRunLookup{RunHandle: state.RunHandle, RepositoryID: 7, HeadOID: snapshot.HeadOID}); err != nil || resolved.RunHandle != state.RunHandle {
		t.Fatalf("resolve: %#v %v", resolved, err)
	}
	if err := gateway.Cancel(context.Background(), state.RunHandle, "ignored"); err != nil {
		t.Fatal(err)
	}
	if status, _ := gateway.Status(context.Background(), state.RunHandle); status.Phase != "terminal" || status.Outcome != string(OutcomeCancelled) {
		t.Fatal(status)
	}
}

// TestGitHubGatewaySealsHeadAndChangedPathsBeforeAdmission verifies PR authority is internal context rather than payload data.
func TestGitHubGatewaySealsHeadAndChangedPathsBeforeAdmission(t *testing.T) {
	service, _, _, _, _ := workflowFixture(t, validPlanResponse())
	observed := RevisionContext{}
	service.deps.Admission = admissionBuilderFunc(func(ctx context.Context, command StartCommand) (AdmissionSnapshot, error) {
		observed, _ = GitHubContractAuthority(ctx)
		return AdmissionSnapshot{}, errors.New("stop")
	})
	command := validCommand()
	command.Approval = ApprovalRequired
	snapshot, _ := githubpr.SealSnapshot(githubpr.PullRequestSnapshot{Version: 1, InstallationID: 1, RepositoryID: 7, Number: 2, AuthorID: 9, State: "open", HeadOID: strings.Repeat("a", 40), HeadRepositoryID: 7, ChangedPaths: []string{"app/z.go", "app/a.go"}})
	gateway := GitHubGateway{Service: service, Principal: "user-1", Commands: map[string]StartCommand{"default": command}}
	_, _ = gateway.Submit(context.Background(), githubpr.Selection{Alias: "default"}, snapshot, "github-authority")
	if observed.RepositoryID != "7" || observed.CommitOID != snapshot.HeadOID || !reflect.DeepEqual(observed.ChangedPaths, []string{"app/a.go", "app/z.go"}) {
		t.Fatalf("GitHub authority not sealed: %+v", observed)
	}
}

// TestGitHubGatewayFailsClosedForUnknownSelectionsAndHandles covers adapter error projections.
func TestGitHubGatewayFailsClosedForUnknownSelectionsAndHandles(t *testing.T) {
	service, _, _, _, _ := workflowFixture(t, validPlanResponse())
	gateway := GitHubGateway{Service: service, Principal: "user-1", Commands: map[string]StartCommand{}}
	snapshot, _ := githubpr.SealSnapshot(githubpr.PullRequestSnapshot{Version: 1, InstallationID: 1, RepositoryID: 7, Number: 2, AuthorID: 9, State: "open", HeadOID: strings.Repeat("a", 40), HeadRepositoryID: 7})
	if _, err := gateway.Submit(context.Background(), githubpr.Selection{Alias: "missing"}, snapshot, "request"); !githubpr.IsCode(err, githubpr.CodeUnauthorized) {
		t.Fatal(err)
	}
	if _, err := gateway.Status(context.Background(), "missing"); !githubpr.IsCode(err, githubpr.CodeUnauthorized) {
		t.Fatal(err)
	}
	if _, err := gateway.ResolveExisting(context.Background(), githubpr.ExistingRunLookup{RunHandle: "missing"}); !githubpr.IsCode(err, githubpr.CodeUnauthorized) {
		t.Fatal(err)
	}
}

// TestGitHubUsesConfiguredWorkflowAndDoesNotAutoApprove proves the start comment only reaches awaiting approval.
func TestGitHubUsesConfiguredWorkflowAndDoesNotAutoApprove(t *testing.T) {
	service, _, execution, _, _ := workflowFixture(t, validPlanResponse())
	command := validCommand()
	command.IdempotencyKey = "template"
	snapshot, err := githubpr.SealSnapshot(githubpr.PullRequestSnapshot{Version: 1, InstallationID: 1, RepositoryID: 7, Number: 2, AuthorID: 9, State: "open", HeadOID: strings.Repeat("a", 40), HeadRepositoryID: 7})
	if err != nil {
		t.Fatal(err)
	}
	gateway := GitHubGateway{Service: service, Principal: "user-1", Commands: map[string]StartCommand{"default": command}}
	state, err := gateway.Submit(context.Background(), githubpr.Selection{Alias: "default"}, snapshot, "request-github")
	if err != nil || state.Phase != "running" || execution.callCount() != 0 {
		t.Fatalf("GitHub auto-approved: %#v %v", state, err)
	}
	status, _ := service.Status(context.Background(), state.RunHandle)
	if status.Phase != PhaseAwaitingApproval {
		t.Fatal(status.Phase)
	}
}

// TestGitHubTerminalStateCarriesVerifiedPackageLineage proves check publication receives the shared workflow package.
func TestGitHubTerminalStateCarriesVerifiedPackageLineage(t *testing.T) {
	service, _, _, _, _ := workflowFixture(t, validPlanResponse())
	command := validCommand()
	command.IdempotencyKey = "template"
	snapshot, _ := githubpr.SealSnapshot(githubpr.PullRequestSnapshot{Version: 1, InstallationID: 1, RepositoryID: 7, Number: 2, AuthorID: 9, State: "open", HeadOID: strings.Repeat("a", 40), HeadRepositoryID: 7})
	gateway := GitHubGateway{Service: service, Principal: "user-1", Commands: map[string]StartCommand{"default": command}}
	state, err := gateway.Submit(context.Background(), githubpr.Selection{Alias: "default"}, snapshot, "github-terminal")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = service.Decide(context.Background(), DecisionCommand{Principal: "user-1", Handle: state.RunHandle, DecisionID: "github-approve", Revision: 1, Action: DecisionApprove}); err != nil {
		t.Fatal(err)
	}
	terminal, err := gateway.Status(context.Background(), state.RunHandle)
	if err != nil || terminal.Phase != "terminal" || terminal.Package != "package-safe" || terminal.PackageRunID != state.RunHandle || terminal.BindingSHA256 == "" || terminal.RepositoryID != 7 || terminal.HeadOID != snapshot.HeadOID {
		t.Fatalf("incomplete terminal state: %#v %v", terminal, err)
	}
	if _, err = gateway.ResolveExisting(context.Background(), githubpr.ExistingRunLookup{RunHandle: state.RunHandle, RepositoryID: 8, HeadOID: snapshot.HeadOID}); err == nil {
		t.Fatal("mismatched GitHub lineage resolved")
	}
}

// TestArtifactAndJournalExcludeCanaries proves profile and credential-like values never enter durable public files.
func TestArtifactAndJournalExcludeCanaries(t *testing.T) {
	const canary = "SECRET_MODEL_HEADER_ASSERTION_OPENAPI"
	raw := strings.Replace(string(validPlanResponse()), `"input":{}`, `"input":{"headers":{"object":{"Authorization":{"literal":{"value":"`+canary+`"}}}}}`, 1)
	raw = strings.Replace(raw, `"definition":{"operator":"exists"}`, `"definition":{"operator":"eq","path":"/token","expected":"`+canary+`"}`, 1)
	service, _, _, store, _ := workflowFixture(t, []byte(raw))
	cli := CLI{Service: service, Principal: "user-1", AuthProfiles: map[string]string{"profile-canary": strings.Repeat("8", 64)}, DirectTargets: map[string]DirectTarget{"target-1": {Alias: "target-1", Digest: strings.Repeat("6", 64)}}}
	args := []string{"start", "--spec", "spec-1", "--contract", "contract-1", "--provider", "fake", "--model", "selected", "--profile", "profile-canary", "--runtime-profile", "runtime-1", "--repository", "repo-1", "--revision", strings.Repeat("a", 40), "--approval", "required", "--idempotency-key", "request-canary", "--target", "target-1"}
	var output strings.Builder
	if code := cli.Run(context.Background(), args, &output); code != 0 {
		t.Fatalf("start code=%d %s", code, output.String())
	}
	entries, _ := os.ReadDir(store.root)
	for _, entry := range entries {
		raw, _ := os.ReadFile(filepath.Join(store.root, entry.Name()))
		if strings.Contains(string(raw), "profile-canary") {
			t.Fatalf("secret reference persisted in %s", entry.Name())
		}
	}
	var view WorkflowView
	if json.Unmarshal([]byte(output.String()), &view) != nil || view.Handle == "" {
		t.Fatal("missing handle")
	}
	artifact, err := service.deps.Artifacts.Load(context.Background(), view.Handle)
	var publicArtifact []byte
	if err == nil {
		publicArtifact, _ = json.Marshal(artifact)
	} else if !errors.Is(err, ErrNotFound) {
		t.Fatal(err)
	}
	state, _ := os.ReadFile(filepath.Join(store.root, view.Handle+".state.json"))
	if bytes.Contains(publicArtifact, []byte(canary)) || bytes.Contains(state, []byte(canary)) || strings.Contains(output.String(), canary) {
		t.Fatal("sensitive literal escaped public workflow surfaces")
	}
}

// TestCanonicalPlanProjectionRedactsNestedLiterals proves artifact and package projection retain structure without executable values.
func TestCanonicalPlanProjectionRedactsNestedLiterals(t *testing.T) {
	const canary = "opaque-sensitive-value-123"
	field := "token"
	plan := planning.ValidationPlan{ID: "plan", ConstraintID: "constraint", Steps: []planning.PlanStep{{ID: "first", Tool: "probe", Input: map[string]planning.InputValue{"body": {Object: map[string]planning.InputValue{"nested": {Array: []planning.InputValue{{Literal: &planning.LiteralValue{Value: canary}}}}}}}, Assertions: []planning.AssertionDeclaration{{ID: "assert", Definition: map[string]any{"operator": "eq", "path": "/token", "expected": canary}}}}, {ID: "second", Tool: "probe", DependsOn: []string{"first"}, Input: map[string]planning.InputValue{"value": {Reference: &planning.OutputReference{StepID: "first", Path: []planning.PathSegment{{Field: &field}}, ValueType: planning.ValueString}}}}}}
	projected := localvalidation.ProjectPlans([]planning.ValidationPlan{plan})
	raw, err := json.Marshal(projected)
	if err != nil || bytes.Contains(raw, []byte(canary)) || !bytes.Contains(raw, []byte(evidence.Redacted)) || !bytes.Contains(raw, []byte(`"depends_on":["first"]`)) || !bytes.Contains(raw, []byte(`"step_id":"first"`)) {
		t.Fatalf("unsafe or incomplete projection: %s %v", raw, err)
	}
}

// TestValidationWorkflowSystemicCommentsCoverAllChangedSymbols enforces systemic comments for package and test symbols.
func TestValidationWorkflowSystemicCommentsCoverAllChangedSymbols(t *testing.T) {
	_, file, _, _ := runtime.Caller(0)
	root := filepath.Dir(file)
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatal(err)
	}
	files := token.NewFileSet()
	for _, entry := range entries {
		if filepath.Ext(entry.Name()) != ".go" {
			continue
		}
		parsed, err := parser.ParseFile(files, filepath.Join(root, entry.Name()), nil, parser.ParseComments)
		if err != nil {
			t.Fatal(err)
		}
		for _, declaration := range parsed.Decls {
			switch node := declaration.(type) {
			case *ast.FuncDecl:
				if node.Doc == nil {
					t.Errorf("missing comment: %s", node.Name.Name)
				}
			case *ast.GenDecl:
				if node.Tok != token.TYPE && node.Tok != token.VAR && node.Tok != token.CONST {
					continue
				}
				for _, spec := range node.Specs {
					switch item := spec.(type) {
					case *ast.TypeSpec:
						if node.Doc == nil && item.Doc == nil {
							t.Errorf("missing comment: %s", item.Name.Name)
						}
					case *ast.ValueSpec:
						if node.Doc == nil && item.Doc == nil {
							for _, name := range item.Names {
								t.Errorf("missing comment: %s", name.Name)
							}
						}
					}
				}
			}
		}
	}
}

// TestStoreRejectsIdempotencyConflictAndCorruptJournal covers durable conflict and integrity branches.
func TestStoreRejectsIdempotencyConflictAndCorruptJournal(t *testing.T) {
	service, _, _, store, _ := workflowFixture(t, validPlanResponse())
	first, err := service.Start(context.Background(), validCommand())
	if err != nil {
		t.Fatal(err)
	}
	changed := validCommand()
	changed.Model = "other"
	_, err = service.Start(context.Background(), changed)
	if !IsCode(err, CodeConflict) {
		t.Fatalf("expected conflict: %v", err)
	}
	if err = os.WriteFile(filepath.Join(store.root, first.Handle+".state.json"), []byte(`bad`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err = store.Events(context.Background(), first.Handle); !IsCode(err, CodeConflict) {
		t.Fatalf("corrupt journal accepted: %v", err)
	}
}

// TestCheckpointFailureInjectionNeverSplitsProjectionAndJournal proves fsync and rename failures expose either complete old or complete new state.
func TestCheckpointFailureInjectionNeverSplitsProjectionAndJournal(t *testing.T) {
	for _, stage := range []string{"temp_sync", "rename", "directory_sync"} {
		t.Run(stage, func(t *testing.T) {
			store, err := OpenLocalStore(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			now := time.Unix(100, 0).UTC()
			record := Record{Version: 1, Handle: "workflow-atomic", Principal: "user-1", IdempotencyKey: "atomic", RequestDigest: strings.Repeat("a", 64), Phase: PhaseAdmitted}
			record, _, err = store.Reserve(context.Background(), record, "owner-1", now, time.Minute)
			if err != nil {
				t.Fatal(err)
			}
			store.fault = func(current string) error {
				if current == stage {
					return errors.New("injected")
				}
				return nil
			}
			next := record
			next.Phase = PhaseGenerating
			_, err = store.SaveOwned(context.Background(), next, "owner-1", record.VersionCAS, now, time.Minute, Event{Type: "generation_started"})
			if !IsCode(err, CodeUnavailable) {
				t.Fatalf("fault not surfaced: %v", err)
			}
			store.fault = nil
			loaded, loadErr := store.Load(context.Background(), record.Handle)
			events, eventsErr := store.Events(context.Background(), record.Handle)
			if loadErr != nil || eventsErr != nil || loaded.Sequence != uint64(len(events)) {
				t.Fatalf("split checkpoint: %#v %d %v %v", loaded, len(events), loadErr, eventsErr)
			}
			if stage == "directory_sync" && loaded.Phase != PhaseGenerating {
				t.Fatalf("renamed checkpoint lost: %#v", loaded)
			}
			if stage != "directory_sync" && loaded.Phase != PhaseAdmitted {
				t.Fatalf("old checkpoint replaced: %#v", loaded)
			}
		})
	}
}

// TestClosedErrorBranchesRemainSafe covers authorization, state, composition and dependency failures.
func TestClosedErrorBranchesRemainSafe(t *testing.T) {
	if _, err := NewService(Config{}, Dependencies{}); !IsCode(err, CodeInvalid) {
		t.Fatal(err)
	}
	service, _, execution, _, _ := workflowFixture(t, validPlanResponse())
	view, _ := service.Start(context.Background(), validCommand())
	if _, err := service.Decide(context.Background(), DecisionCommand{}); !IsCode(err, CodeInvalid) {
		t.Fatal(err)
	}
	if _, err := service.Decide(context.Background(), DecisionCommand{Principal: "other", Handle: view.Handle, Revision: 1, DecisionID: "decision-other", Action: DecisionApprove}); !IsCode(err, CodeUnauthorized) {
		t.Fatal(err)
	}
	if _, err := service.Result(context.Background(), view.Handle); !IsCode(err, CodeConflict) {
		t.Fatal(err)
	}
	if _, err := service.Cancel(context.Background(), "other", view.Handle); !IsCode(err, CodeUnauthorized) {
		t.Fatal(err)
	}
	if _, err := service.Status(context.Background(), "unknown"); !IsCode(err, CodeUnauthorized) {
		t.Fatal(err)
	}
	service.deps.Execution = &fakeExecution{err: errors.New("runner failed")}
	decided, err := service.Decide(context.Background(), DecisionCommand{Principal: "user-1", Handle: view.Handle, Revision: 1, DecisionID: "decision-failure", Action: DecisionApprove})
	if err != nil || decided.Outcome == nil || *decided.Outcome != OutcomeInconclusive || execution.callCount() != 0 {
		t.Fatalf("execution failure unsafe: %#v %v", decided, err)
	}
	invalid, _, invalidExecution, _, _ := workflowFixture(t, []byte(`{"version":1,"kind":"bad"}`))
	invalidView, err := invalid.Start(context.Background(), func() StartCommand {
		command := validCommand()
		command.IdempotencyKey = "invalid-generation"
		return command
	}())
	if err != nil || invalidView.Outcome == nil || *invalidView.Outcome != OutcomeInconclusive || invalidExecution.callCount() != 0 {
		t.Fatalf("invalid generation escaped: %#v %v", invalidView, err)
	}
}

// TestStartDependencyFailuresBecomeClosedOutcomes covers pre-admission, identity, authorization and invalid-result branches.
func TestStartDependencyFailuresBecomeClosedOutcomes(t *testing.T) {
	service, admission, _, _, _ := workflowFixture(t, validPlanResponse())
	admission.err = errors.New("CANARY-REMOTE")
	if view, err := service.Start(context.Background(), validCommand()); !IsCode(err, CodeInvalid) || view.Handle != "" || strings.Contains(err.Error(), "CANARY") {
		t.Fatalf("unsafe admission error: %#v %v", view, err)
	}
	service, admission, _, _, _ = workflowFixture(t, validPlanResponse())
	admission.err = context.Canceled
	if _, err := service.Start(context.Background(), validCommand()); !IsCode(err, CodeCancelled) {
		t.Fatal(err)
	}
	service, _, _, _, _ = workflowFixture(t, validPlanResponse())
	service.deps.IDs = failingIDs{}
	if _, err := service.Start(context.Background(), validCommand()); !IsCode(err, CodeInternal) {
		t.Fatal(err)
	}
	service, _, _, _, _ = workflowFixture(t, validPlanResponse())
	service.deps.IDs = invalidIDs{}
	if _, err := service.Start(context.Background(), validCommand()); !IsCode(err, CodeInternal) {
		t.Fatal(err)
	}
	if _, _, err := (GeneratorAdapter{}).Generate(context.Background(), validSnapshot()); !IsCode(err, CodeInternal) {
		t.Fatal(err)
	}
	service, admission, _, _, _ = workflowFixture(t, validPlanResponse())
	admission.snapshot.ContractDigest = "short"
	if _, err := service.Start(context.Background(), validCommand()); !IsCode(err, CodeInvalid) {
		t.Fatal(err)
	}
	service, _, execution, _, _ := workflowFixture(t, validPlanResponse())
	service.deps.Authorization = fakeAuto{err: errors.New("denied")}
	command := validCommand()
	command.Approval = ApprovalDisabled
	denied, err := service.Start(context.Background(), command)
	if err != nil || denied.Outcome == nil || *denied.Outcome != OutcomeInconclusive || execution.callCount() != 0 {
		t.Fatalf("auto denial unsafe: %#v %v", denied, err)
	}
	if (&Error{Code: CodeInvalid, Operation: "test"}).Error() == "" || errorCode(errors.New("unknown")) != CodeInternal {
		t.Fatal("unsafe error mapping")
	}
	for workflowCode, githubCode := range map[ErrorCode]githubpr.ErrorCode{CodeInvalid: githubpr.CodeInvalid, CodeUnauthorized: githubpr.CodeUnauthorized, CodeConflict: githubpr.CodeConflict, CodeCancelled: githubpr.CodeCancelled, CodeUnavailable: githubpr.CodeInternal} {
		mapped := githubError(&Error{Code: workflowCode, Operation: "test"})
		if !githubpr.IsCode(mapped, githubCode) {
			t.Fatalf("mapping %s: %v", workflowCode, mapped)
		}
	}
	if githubError(nil) != nil {
		t.Fatal("nil mapping")
	}
}

// TestStateAndSelectionValidatorsCoverClosedVocabulary rejects malformed unions and illegal phase edges.
func TestStateAndSelectionValidatorsCoverClosedVocabulary(t *testing.T) {
	for _, mutate := range []func(*StartCommand){func(c *StartCommand) { c.Target = TargetSelection{} }, func(c *StartCommand) { c.Target.Direct.Alias = "bad/path" }, func(c *StartCommand) { c.Approval = "maybe" }, func(c *StartCommand) { c.Provider = "" }} {
		command := validCommand()
		mutate(&command)
		if err := validateStart(command); !IsCode(err, CodeInvalid) {
			t.Fatal(err)
		}
	}
	for _, edge := range [][2]Phase{{PhaseAdmitted, PhaseGenerating}, {PhaseGenerating, PhasePlanReady}, {PhasePlanReady, PhaseAuthorized}, {PhaseAwaitingApproval, PhaseAuthorized}, {PhaseAuthorized, PhaseExecuting}, {PhaseExecuting, PhaseFinalizing}, {PhaseFinalizing, PhaseTerminal}} {
		if !allowedTransition(edge[0], edge[1]) {
			t.Fatal(edge)
		}
	}
	if allowedTransition(PhaseTerminal, PhaseAdmitted) || allowedTransition(PhaseAdmitted, PhaseExecuting) {
		t.Fatal("illegal transition")
	}
	if _, err := (LocalExecutionPort{}).Execute(context.Background(), ExecutionRequest{Snapshot: AdmissionSnapshot{RuntimeDigest: "missing"}}); !IsCode(err, CodeUnauthorized) {
		t.Fatal(err)
	}
	service, _, _, store, _ := workflowFixture(t, validPlanResponse())
	view, _ := service.Start(context.Background(), validCommand())
	record, _ := store.Load(context.Background(), view.Handle)
	if _, err := service.transition(context.Background(), record, PhaseExecuting, "illegal", ""); !IsCode(err, CodeConflict) {
		t.Fatal(err)
	}
	terminal, _ := service.Cancel(context.Background(), "user-1", view.Handle)
	loaded, _ := store.Load(context.Background(), terminal.Handle)
	if _, err := service.terminal(context.Background(), loaded, OutcomeError, "again"); err != nil {
		t.Fatal(err)
	}
}

// TestArtifactStoreAndStoreRejectMalformedOperations covers immutable publication and lease validation branches.
func TestArtifactStoreAndStoreRejectMalformedOperations(t *testing.T) {
	root := t.TempDir()
	artifacts, err := OpenLocalArtifactStore(root)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = OpenLocalArtifactStore(""); !IsCode(err, CodeInvalid) {
		t.Fatal(err)
	}
	if _, err = artifacts.Load(context.Background(), "bad/path"); !IsCode(err, CodeInvalid) {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err = artifacts.Load(ctx, "valid"); !IsCode(err, CodeCancelled) {
		t.Fatal(err)
	}
	service, _, _, store, clock := workflowFixture(t, validPlanResponse())
	view, _ := service.Start(context.Background(), validCommand())
	record, _ := store.Load(context.Background(), view.Handle)
	if _, _, err = store.Claim(context.Background(), view.Handle, "owner-2", clock.Now(), time.Minute); err != nil {
		t.Fatal(err)
	}
	if _, err = store.SaveOwned(context.Background(), record, "wrong", record.VersionCAS, clock.Now(), time.Minute, Event{Type: "bad"}); !IsCode(err, CodeConflict) {
		t.Fatal(err)
	}
	if _, _, err = store.Reserve(context.Background(), Record{}, "owner", clock.Now(), time.Minute); !IsCode(err, CodeInvalid) {
		t.Fatal(err)
	}
	if _, err = store.Load(context.Background(), "bad/path"); !IsCode(err, CodeInvalid) {
		t.Fatal(err)
	}
	for status, expected := range map[localvalidation.ResultStatus]Outcome{localvalidation.ResultPassed: OutcomePassed, localvalidation.ResultFailed: OutcomeFailed, localvalidation.ResultCancelled: OutcomeCancelled, localvalidation.ResultError: OutcomeError, localvalidation.ResultInconclusive: OutcomeInconclusive} {
		if packageOutcome(status) != expected {
			t.Fatal(status)
		}
	}
}

// TestStoreCancellationAndCorruptionBranches remain bounded and never accept incomplete files.
func TestStoreCancellationAndCorruptionBranches(t *testing.T) {
	store, err := OpenLocalStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if _, err = OpenLocalStore(""); !IsCode(err, CodeInvalid) {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err = store.Load(ctx, "valid"); !IsCode(err, CodeCancelled) {
		t.Fatal(err)
	}
	if classifyStoreError(context.Canceled, "x").(*Error).Code != CodeCancelled {
		t.Fatal("cancel classification")
	}
	if _, err = store.Events(context.Background(), "bad/path"); !IsCode(err, CodeInvalid) {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(store.root, "corrupt.state.json"), []byte(`bad`), 0o600); err != nil {
		t.Fatal(err)
	}
	record := Record{Version: 1, Handle: "h-1", Principal: "p-1", IdempotencyKey: "k-1", RequestDigest: strings.Repeat("a", 64), Phase: PhaseAdmitted}
	if _, _, err = store.Reserve(context.Background(), record, "owner-1", time.Now(), time.Minute); !IsCode(err, CodeConflict) {
		t.Fatal(err)
	}
}

// TestStoreTerminalCancelAndInvalidClaim cover no-op terminal mutation and malformed lease requests.
func TestStoreTerminalCancelAndInvalidClaim(t *testing.T) {
	service, _, _, store, clock := workflowFixture(t, validPlanResponse())
	view, _ := service.Start(context.Background(), validCommand())
	_, _ = service.Cancel(context.Background(), "user-1", view.Handle)
	before, _ := store.Load(context.Background(), view.Handle)
	after, err := store.RequestCancel(context.Background(), view.Handle, clock.Now(), Event{Type: "again"})
	if err != nil || before.VersionCAS != after.VersionCAS {
		t.Fatalf("terminal cancel mutated: %#v %v", after, err)
	}
	if _, _, err = store.Claim(context.Background(), "bad/path", "owner", clock.Now(), time.Minute); !IsCode(err, CodeInvalid) {
		t.Fatal(err)
	}
	if pending, err := store.Pending(context.Background(), clock.Now()); err != nil || len(pending) != 0 {
		t.Fatalf("terminal pending: %#v %v", pending, err)
	}
}

// TestPackageReaderRejectsMissingPackageAndAssertionTopologyDrift covers fail-closed package and final topology branches.
func TestPackageReaderRejectsMissingPackageAndAssertionTopologyDrift(t *testing.T) {
	artifacts, _ := OpenLocalArtifactStore(t.TempDir())
	reader := LocalPackageReader{Artifacts: artifacts}
	if _, err := reader.Read(context.Background(), filepath.Join(t.TempDir(), "missing"), "workflow", "digest"); !IsCode(err, CodeUnavailable) {
		t.Fatal(err)
	}
	expected := localvalidation.ProjectPlans([]planning.ValidationPlan{{ID: "p", ConstraintID: "c", Steps: []planning.PlanStep{{ID: "s", Tool: "t", Assertions: []planning.AssertionDeclaration{{ID: "a"}}}}}})
	actual := localvalidation.ProjectPlans([]planning.ValidationPlan{{ID: "p", ConstraintID: "c", Steps: []planning.PlanStep{{ID: "s", Tool: "t", Assertions: []planning.AssertionDeclaration{{ID: "other"}}}}}})
	if reflect.DeepEqual(expected, actual) {
		t.Fatal("assertion drift accepted")
	}
	actual = localvalidation.ProjectPlans([]planning.ValidationPlan{{ID: "p", ConstraintID: "c", Steps: []planning.PlanStep{{ID: "s", Tool: "t", Assertions: []planning.AssertionDeclaration{{ID: "a"}, {ID: "b"}}}}}})
	if reflect.DeepEqual(expected, actual) {
		t.Fatal("assertion count drift accepted")
	}
	if verifyArtifact(PlanArtifact{}, "workflow") == nil {
		t.Fatal("empty artifact accepted")
	}
}

// TestArtifactStoreIsImmutableAndPackageTopologyRejectsDrift covers repeat publication and topology integrity.
func TestArtifactStoreIsImmutableAndPackageTopologyRejectsDrift(t *testing.T) {
	store, err := OpenLocalArtifactStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	artifact := PlanArtifact{Version: ArtifactVersion, WorkflowID: "workflow-artifact", PlanID: "plan", Plans: localvalidation.ProjectPlans([]planning.ValidationPlan{{ID: "plan", ConstraintID: "c", Steps: []planning.PlanStep{{ID: "s", Tool: "probe", Assertions: []planning.AssertionDeclaration{{ID: "a"}}}}}}), ContextDigest: strings.Repeat("1", 64), RevisionDigest: strings.Repeat("2", 64), TargetDigest: strings.Repeat("3", 64), RuntimeDigest: strings.Repeat("4", 64)}
	artifact.ArtifactSHA256, _ = artifactDigest(artifact)
	if err = store.Commit(context.Background(), artifact); err != nil {
		t.Fatal(err)
	}
	if _, err = store.Load(context.Background(), "missing"); !errors.Is(err, ErrNotFound) {
		t.Fatal(err)
	}
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	if err = store.Commit(cancelled, artifact); !IsCode(err, CodeCancelled) {
		t.Fatal(err)
	}
	if err = store.Commit(context.Background(), artifact); err != nil {
		t.Fatal(err)
	}
	changed := artifact
	changed.ArtifactSHA256 = strings.Repeat("f", 64)
	if err = store.Commit(context.Background(), changed); !IsCode(err, CodeInvalid) {
		t.Fatalf("changed artifact accepted: %v", err)
	}
	if reflect.DeepEqual(artifact.Plans, nil) || reflect.DeepEqual(artifact.Plans, localvalidation.ProjectPlans([]planning.ValidationPlan{{ID: "other"}})) || reflect.DeepEqual(artifact.Plans, localvalidation.ProjectPlans([]planning.ValidationPlan{{ID: "plan", ConstraintID: "c", Steps: []planning.PlanStep{{ID: "other"}}}})) {
		t.Fatal("topology drift accepted")
	}
	reader := LocalPackageReader{}
	if _, err = reader.Read(context.Background(), "none", "workflow-artifact", artifact.ArtifactSHA256); !IsCode(err, CodeInternal) {
		t.Fatal(err)
	}
	if selectedTargetDigest(TargetSelection{Preview: &PreviewTarget{Digest: "preview"}}) != "preview" || selectedTargetDigest(TargetSelection{}) != "" || itoaSigned(0) != "" {
		t.Fatal("closed union helpers")
	}
}

// TestPackageIsOnlyFunctionalResultAuthority proves execution completion cannot fabricate passed status.
func TestPackageIsOnlyFunctionalResultAuthority(t *testing.T) {
	service, _, _, _, _ := workflowFixture(t, validPlanResponse())
	service.deps.Packages = fakePackages{err: errors.New("corrupt")}
	command := validCommand()
	command.Approval = ApprovalDisabled
	view, err := service.Start(context.Background(), command)
	if err != nil || view.Outcome == nil || *view.Outcome != OutcomeError {
		t.Fatalf("package failure became functional result: %#v %v", view, err)
	}
}

// TestCleanupIntentRecoversBeforeTerminal proves release failure remains durable cleanup_pending and recovery confirms cleanup before terminal status.
func TestCleanupIntentRecoversBeforeTerminal(t *testing.T) {
	service, _, _, store, clock := workflowFixture(t, validPlanResponse())
	finalizer := &fakeFinalizer{err: errors.New("release unavailable")}
	service.deps.Finalizer = finalizer
	command := validCommand()
	command.Approval = ApprovalDisabled
	view, err := service.Start(context.Background(), command)
	if !IsCode(err, CodeUnavailable) {
		t.Fatalf("release failure hidden: %#v %v", view, err)
	}
	record, loadErr := store.Load(context.Background(), "workflow-1")
	if loadErr != nil || !record.CleanupPending || record.Phase == PhaseTerminal || record.CleanupIntentID == "" {
		t.Fatalf("cleanup intent missing: %#v %v", record, loadErr)
	}
	finalizer.mu.Lock()
	finalizer.err = nil
	finalizer.mu.Unlock()
	clock.advance(2 * time.Minute)
	if err = service.Recover(context.Background()); err != nil {
		t.Fatal(err)
	}
	record, err = store.Load(context.Background(), "workflow-1")
	if err != nil || record.Phase != PhaseTerminal || record.CleanupPending {
		t.Fatalf("cleanup did not converge: %#v %v", record, err)
	}
}

// TestPreviewLifecycleBindsArtifactAndCleanup proves preview acquisition precedes inference and release is confirmed before terminal cancellation.
func TestPreviewLifecycleBindsArtifactAndCleanup(t *testing.T) {
	service, _, _, _, _ := workflowFixture(t, validPlanResponse())
	preview := &fakePreview{digest: strings.Repeat("9", 64)}
	service.deps.Preview = preview
	command := validCommand()
	command.Target.Direct = nil
	command.Target.Preview = &PreviewTarget{Alias: "preview-1", Digest: strings.Repeat("8", 64)}
	view, err := service.Start(context.Background(), command)
	if err != nil || view.Phase != PhaseAwaitingApproval {
		t.Fatalf("preview start: %#v %v", view, err)
	}
	artifact, err := service.deps.Artifacts.Load(context.Background(), view.Handle)
	if err != nil || artifact.TargetDigest != preview.expectedDigest() {
		t.Fatalf("preview digest not bound: %#v %v", artifact, err)
	}
	cancelled, err := service.Cancel(context.Background(), command.Principal, view.Handle)
	if err != nil || cancelled.Phase != PhaseTerminal {
		t.Fatalf("preview cancel: %#v %v", cancelled, err)
	}
	preview.mu.Lock()
	acquires, releases := preview.acquires, preview.releases
	preview.mu.Unlock()
	if acquires != 1 || releases != 1 {
		t.Fatalf("preview lifecycle calls acquire=%d release=%d", acquires, releases)
	}
}

// TestPrivateStoreAndLeaseCorrectionBranches cover malformed private snapshots and rejected lease ownership operations.
func TestPrivateStoreAndLeaseCorrectionBranches(t *testing.T) {
	if _, err := OpenLocalExecutableStore(""); !IsCode(err, CodeInvalid) {
		t.Fatal(err)
	}
	private, err := OpenLocalExecutableStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err = private.Commit(context.Background(), "bad/path", ExecutableMaterial{}); !IsCode(err, CodeInvalid) {
		t.Fatal(err)
	}
	if _, err = private.Load(context.Background(), "missing"); !IsCode(err, CodeUnavailable) {
		t.Fatal(err)
	}
	cancelledContext, cancelPrivate := context.WithCancel(context.Background())
	cancelPrivate()
	if err = private.Commit(cancelledContext, "private", ExecutableMaterial{}); !IsCode(err, CodeInvalid) {
		t.Fatal(err)
	}
	if _, err = private.Load(cancelledContext, "private"); !IsCode(err, CodeInvalid) {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(private.root, "corrupt.private.json"), []byte(`bad`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err = private.Load(context.Background(), "corrupt"); !IsCode(err, CodeConflict) {
		t.Fatal(err)
	}
	service, _, _, _, _ := workflowFixture(t, validPlanResponse())
	view, err := service.Start(context.Background(), validCommand())
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := service.deps.Executables.Load(context.Background(), view.Handle)
	if err != nil {
		t.Fatal(err)
	}
	snapshot.WorkflowID = "valid-private"
	if err = private.Commit(context.Background(), "valid-private", snapshot); err != nil {
		t.Fatal(err)
	}
	if err = private.Commit(context.Background(), "valid-private", snapshot); err != nil {
		t.Fatal(err)
	}
	changed := snapshot
	changed.Snapshot.ID = "changed"
	if err = private.Commit(context.Background(), "valid-private", changed); !IsCode(err, CodeConflict) {
		t.Fatal(err)
	}
	store, err := OpenLocalStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if _, err = store.Renew(context.Background(), "bad/path", "owner", time.Now(), time.Minute); !IsCode(err, CodeInvalid) {
		t.Fatal(err)
	}
	if _, err = store.ReleaseOwner(context.Background(), "bad/path", "owner", 1, time.Now()); !IsCode(err, CodeInvalid) {
		t.Fatal(err)
	}
	now := time.Unix(10, 0).UTC()
	record := Record{Version: 1, Handle: "lease-correction", Principal: "user-1", IdempotencyKey: "lease", RequestDigest: strings.Repeat("a", 64), Phase: PhaseAdmitted}
	record, _, err = store.Reserve(context.Background(), record, "owner-1", now, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = store.Renew(context.Background(), record.Handle, "owner-2", now, time.Minute); !IsCode(err, CodeConflict) {
		t.Fatal(err)
	}
	if _, err = store.ReleaseOwner(context.Background(), record.Handle, "owner-1", record.VersionCAS+1, now); !IsCode(err, CodeConflict) {
		t.Fatal(err)
	}
	if approvalError(nil, "approval") != nil || !IsCode(approvalError(errors.New("opaque"), "approval"), CodeConflict) || !IsCode(approvalError(&planapproval.Error{Category: planapproval.CategoryInvalidState, Field: "revision"}, "approval"), CodeConflict) {
		t.Fatal("approval error mapping")
	}
}

// TestPreviewAcquisitionFailureBranches close missing composition, provider error and malformed digest outcomes without inference.
func TestPreviewAcquisitionFailureBranches(t *testing.T) {
	for _, scenario := range []struct {
		name      string
		preview   PreviewPort
		wantError bool
	}{
		{name: "missing", preview: nil, wantError: true},
		{name: "provider", preview: &fakePreview{digest: strings.Repeat("9", 64), acquireErr: errors.New("unavailable")}},
		{name: "digest", preview: &fakePreview{digest: "short"}},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			service, _, execution, _, _ := workflowFixture(t, validPlanResponse())
			service.deps.Preview = scenario.preview
			command := validCommand()
			command.Target.Direct = nil
			command.Target.Preview = &PreviewTarget{Alias: "preview-1", Digest: strings.Repeat("8", 64)}
			view, err := service.Start(context.Background(), command)
			if scenario.wantError {
				if !IsCode(err, CodeInvalid) {
					t.Fatalf("missing preview accepted: %v", err)
				}
				return
			}
			if err != nil || view.Phase != PhaseTerminal || view.Outcome == nil || *view.Outcome != OutcomeInconclusive || execution.callCount() != 0 {
				t.Fatalf("preview failure: %#v %v", view, err)
			}
		})
	}
}

// TestPreviewReleasePendingRecovers proves preview cleanup failures remain nonterminal until the coordinator confirms release.
func TestPreviewReleasePendingRecovers(t *testing.T) {
	service, _, _, store, clock := workflowFixture(t, validPlanResponse())
	preview := &fakePreview{digest: strings.Repeat("9", 64), releaseErr: errors.New("release pending")}
	service.deps.Preview = preview
	command := validCommand()
	command.Target.Direct = nil
	command.Target.Preview = &PreviewTarget{Alias: "preview-1", Digest: strings.Repeat("8", 64)}
	view, err := service.Start(context.Background(), command)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = service.Cancel(context.Background(), command.Principal, view.Handle); !IsCode(err, CodeUnavailable) {
		t.Fatalf("preview release failure hidden: %v", err)
	}
	record, err := store.Load(context.Background(), view.Handle)
	if err != nil || !record.CleanupPending || record.Phase == PhaseTerminal {
		t.Fatalf("cleanup not pending: %#v %v", record, err)
	}
	preview.mu.Lock()
	preview.releaseErr = nil
	preview.mu.Unlock()
	clock.advance(2 * time.Minute)
	if err = service.Recover(context.Background()); err != nil {
		t.Fatal(err)
	}
	record, err = store.Load(context.Background(), view.Handle)
	if err != nil || record.Phase != PhaseTerminal {
		t.Fatalf("preview cleanup recovery: %#v %v", record, err)
	}
}

// TestAuthorizedGeneratedPlanUsesExistingRunnerAndPackage executes a generated plan through the historical runner and verified package reader.
func TestAuthorizedGeneratedPlanUsesExistingRunnerAndPackage(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write([]byte(`{"ok":true}`))
	}))
	defer server.Close()
	root := t.TempDir()
	method, path := "GET", "/"
	plan := planning.ValidationPlan{
		ID: "health-plan", ConstraintID: "health",
		Steps: []planning.PlanStep{{
			ID: "request", Tool: "http.request",
			Input: map[string]planning.InputValue{
				"method":  {Literal: &planning.LiteralValue{Value: method}},
				"path":    {Literal: &planning.LiteralValue{Value: path}},
				"headers": {Object: map[string]planning.InputValue{}},
			},
			Assertions: []planning.AssertionDeclaration{{ID: "status", Definition: map[string]any{"operator": "eq", "path": "/status", "expected": 200}}},
		}},
	}
	spec := specification.Specification{Version: 1, Feature: specification.Feature{ID: "feature", Description: "vertical"}, Target: specification.Target{ID: "deployed", BaseURL: server.URL}, Constraints: []specification.Constraint{{ID: "health", Description: "responds", TargetID: "deployed"}}, Limits: specification.Limits{MaxSteps: 2, MaxToolCalls: 2}}
	manifest := planmanifest.Manifest{Version: 1, HTTP: planmanifest.HTTPConfig{AllowedMethods: []string{"GET"}, AllowedRequestHeaders: []string{}, AllowedResponseHeaders: []string{"Content-Type"}, FollowRedirectsForMethods: []string{}, MaxRedirects: 0, TimeoutMS: 1000, MaxURLBytes: 2048, MaxRequestHeaderBytes: 8192, MaxRequestBodyBytes: 1024, MaxResponseHeaderBytes: 8192, MaxResponseBodyBytes: 8192}, Redaction: evidence.Config{MaxBodyBytes: 8192, AdditionalHeaders: []string{}, SensitiveJSONPaths: []string{}, ModelSensitiveJSONPaths: []string{}}, Plans: []planning.ValidationPlan{plan}}
	commit := strings.Repeat("a", 40)
	var diagnostic bytes.Buffer
	deps := localvalidation.DefaultDependencies(root, &diagnostic, &diagnostic)
	deps.RevisionResolver = testRevisionResolver{revisionbinding.Revision{RepositoryID: "repo", CommitOID: commit}}
	deps.EnvironmentAttestor = testEnvironmentAttestor{revisionbinding.Attestation{EnvironmentID: "preview", RepositoryID: "repo", CommitOID: commit, Reference: "proof", ValidUntil: time.Now().Add(time.Hour), Verified: true}}
	deps.SafeProjector = testSafeProjector{}
	runtimeDigest := strings.Repeat("5", 64)
	commitmentKey := []byte(strings.Repeat("k", 32))
	port := LocalExecutionPort{Configurations: map[string]LocalRunnerConfiguration{runtimeDigest: {Digest: runtimeDigest, Request: localvalidation.PreparedRequest{Specification: spec, Runtime: manifest, Target: server.URL, RepositoryID: "repo", Revision: "main", EnvironmentID: "preview"}, Dependencies: deps}}}
	artifact := PlanArtifact{Version: ArtifactVersion, WorkflowID: "workflow-vertical", PlanID: "generated", Provider: "fake", Model: "selected", PromptVersion: "v1", ProtocolVersion: "v1", Plans: localvalidation.ProjectPlans([]planning.ValidationPlan{plan}, commitmentKey), ContextDigest: strings.Repeat("1", 64), RevisionDigest: strings.Repeat("2", 64), TargetDigest: strings.Repeat("3", 64), RuntimeDigest: runtimeDigest}
	artifact.ArtifactSHA256, _ = artifactDigest(artifact)
	packageDir, err := port.Execute(context.Background(), ExecutionRequest{Handle: "workflow-vertical", Plans: []planning.ValidationPlan{plan}, Snapshot: AdmissionSnapshot{RuntimeDigest: runtimeDigest}, ArtifactSHA256: artifact.ArtifactSHA256, CommitmentKey: commitmentKey})
	if err != nil {
		t.Fatalf("%v: %s", err, diagnostic.String())
	}
	artifacts, err := OpenLocalArtifactStore(filepath.Join(root, ".featureproof", "workflow-plans"))
	if err != nil {
		t.Fatal(err)
	}
	if err = artifacts.Commit(context.Background(), artifact); err != nil {
		t.Fatal(err)
	}
	projection, err := (LocalPackageReader{Artifacts: artifacts}).Read(context.Background(), packageDir, "workflow-vertical", artifact.ArtifactSHA256)
	if err != nil || projection.Outcome != OutcomePassed || projection.RunID != "workflow-vertical" {
		t.Fatalf("verified package missing: %#v %v", projection, err)
	}
	planDocument, err := os.ReadFile(filepath.Join(packageDir, "plan.json"))
	if err != nil || bytes.Contains(planDocument, commitmentKey) || bytes.Contains(planDocument, []byte(base64.StdEncoding.EncodeToString(commitmentKey))) {
		t.Fatalf("private commitment key reached package: %v %s", err, planDocument)
	}
}
