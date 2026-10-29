package functionalflow

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/asierhr/featureProof/internal/fixture"
	"github.com/asierhr/featureProof/internal/planning"
	"github.com/asierhr/featureProof/internal/testidentity"
)

// memoryIntents emulates CAS claims and durable intent/terminal checkpoints under concurrent owners.
type memoryIntents struct {
	mu        sync.Mutex
	claims    map[string]string
	intents   map[string]Intent
	terminals map[string]StepRecord
}

// fixedFlowClock supplies one deterministic instant for lease-validity checks.
type fixedFlowClock struct{ now time.Time }

// Now returns the immutable test instant.
func (c fixedFlowClock) Now() time.Time { return c.now }

// advancingFlowClock moves deterministic lease time only when a barrier test explicitly advances it.
type advancingFlowClock struct {
	mu  sync.Mutex
	now time.Time
}

// Now returns the controlled lease instant.
func (c *advancingFlowClock) Now() time.Time { c.mu.Lock(); defer c.mu.Unlock(); return c.now }

// Advance moves the controlled lease instant without wall-clock sleeps.
func (c *advancingFlowClock) Advance(duration time.Duration) {
	c.mu.Lock()
	c.now = c.now.Add(duration)
	c.mu.Unlock()
}

// boundedLeaseIntents overrides claim expiry so retry-window validation can be tested without sleeping.
type boundedLeaseIntents struct {
	*memoryIntents
	expires time.Time
}

// renewalFailureIntents injects one durable renewal failure at an exact post-tool boundary.
type renewalFailureIntents struct {
	*memoryIntents
	failAt, calls int
}

// selectiveJournal fails one named causal checkpoint while accepting every
// earlier append, allowing each executor boundary to be tested independently.
type selectiveJournal struct{ failType string }

// AppendEvent rejects only the configured event type.
func (journal selectiveJournal) AppendEvent(_ context.Context, event Event) (Event, error) {
	if event.Type == journal.failType {
		return Event{}, errors.New("journal unavailable")
	}
	return event, nil
}

// Events returns no prior terminals for fresh fault-injection executions.
func (selectiveJournal) Events(context.Context, string) ([]Event, error) { return nil, nil }

// rejectingBinding injects authority loss immediately before tool execution.
type rejectingBinding struct{}

// Revalidate rejects the bound operation without opening external I/O.
func (rejectingBinding) Revalidate(context.Context, string, BoundStep) error {
	return errors.New("binding revoked")
}

// rejectingAssertions injects deterministic evaluator failure after evidence persistence.
type rejectingAssertions struct{}

// Evaluate rejects confirmed evidence to exercise the integrity boundary.
func (rejectingAssertions) Evaluate(context.Context, []planning.AssertionDeclaration, Evidence) ([]planning.AssertionResult, error) {
	return nil, errors.New("assertion evaluator unavailable")
}

// rejectingEvidence injects either a persistence error or an invalid returned record.
type rejectingEvidence struct{ invalid bool }

// Confirm returns the selected evidence failure mode.
func (sink rejectingEvidence) Confirm(_ context.Context, evidence Evidence) (Evidence, error) {
	if sink.invalid {
		evidence.Digest = "invalid"
		return evidence, nil
	}
	return Evidence{}, errors.New("evidence unavailable")
}

// Load is unreachable because both confirmation modes stop the executor first.
func (rejectingEvidence) Load(context.Context, string) (Evidence, error) {
	return Evidence{}, errors.New("unexpected evidence load")
}

// rejectingProgressStore fails the selected started or evidence intent transition.
type rejectingProgressStore struct {
	*memoryIntents
	failEvidence bool
}

// rejectingTerminalStore injects a fenced terminal CAS failure after input resolution.
type rejectingTerminalStore struct{ *memoryIntents }

// ConfirmTerminal refuses the terminal while preserving the active claim.
func (rejectingTerminalStore) ConfirmTerminal(context.Context, StepRecord) error {
	return errors.New("terminal unavailable")
}

// ConfirmIntent rejects only the selected causal action progress checkpoint.
func (store *rejectingProgressStore) ConfirmIntent(ctx context.Context, intent Intent) error {
	if intent.EffectStarted && (intent.EvidenceID != "") == store.failEvidence {
		return errors.New("intent progress unavailable")
	}
	return store.memoryIntents.ConfirmIntent(ctx, intent)
}

// rejectingWaiter injects cancellation between retry attempts.
type rejectingWaiter struct{}

// Wait refuses the retry delay without sleeping.
func (rejectingWaiter) Wait(context.Context, time.Duration) error {
	return context.Canceled
}

// tracedLeaseStore wraps the real durable store and rejects any executor call
// that presents an older lease snapshot after a successful renewal.
type tracedLeaseStore struct {
	IntentStore
	clock  *advancingFlowClock
	mu     sync.Mutex
	claims map[string]Claim
	trace  []string
}

// Claim records the exact token and expiry returned by the production store.
func (s *tracedLeaseStore) Claim(ctx context.Context, runID string, revision uint64, stepID, owner string) (Claim, bool, error) {
	claim, acquired, err := s.IntentStore.Claim(ctx, runID, revision, stepID, owner)
	if err == nil && acquired {
		s.mu.Lock()
		s.claims[stateKey(runID, revision, stepID)] = claim
		s.trace = append(s.trace, "claim:"+stepID+":"+claim.Token)
		s.mu.Unlock()
	}
	return claim, acquired, err
}

// RenewClaim enforces propagation of the latest expiry while preserving the
// immutable token across a deterministic clock advance.
func (s *tracedLeaseStore) RenewClaim(ctx context.Context, runID string, revision uint64, stepID string, claim Claim) (Claim, error) {
	key := stateKey(runID, revision, stepID)
	s.mu.Lock()
	current := s.claims[key]
	s.mu.Unlock()
	if claim != current {
		return Claim{}, errors.New("stale lease snapshot")
	}
	s.clock.Advance(time.Second)
	renewed, err := s.IntentStore.RenewClaim(ctx, runID, revision, stepID, claim)
	if err != nil {
		return Claim{}, err
	}
	if renewed.Token != claim.Token || renewed.Owner != claim.Owner || !renewed.ExpiresAt.After(claim.ExpiresAt) {
		return Claim{}, errors.New("renewal changed authority or failed to extend expiry")
	}
	s.mu.Lock()
	s.claims[key] = renewed
	s.trace = append(s.trace, "renew:"+stepID+":"+renewed.Token)
	s.mu.Unlock()
	return renewed, nil
}

// ConfirmIntent proves each prepared or progressed action uses the currently
// live claim token before delegating the durable CAS.
func (s *tracedLeaseStore) ConfirmIntent(ctx context.Context, intent Intent) error {
	key := stateKey(intent.RunID, intent.Revision, intent.StepID)
	s.mu.Lock()
	current := s.claims[key]
	phase := "prepared"
	if intent.EvidenceID != "" {
		phase = "evidence"
	} else if intent.EffectStarted {
		phase = "started"
	}
	s.trace = append(s.trace, "intent:"+intent.StepID+":"+phase+":"+intent.ClaimToken)
	s.mu.Unlock()
	if intent.ClaimToken != current.Token {
		return errors.New("intent used stale claim token")
	}
	return s.IntentStore.ConfirmIntent(ctx, intent)
}

// ConfirmTerminal requires the newest live claim snapshot's token and records
// the terminal boundary before releasing the wrapper's ownership view.
func (s *tracedLeaseStore) ConfirmTerminal(ctx context.Context, record StepRecord) error {
	key := stateKey(record.RunID, record.Revision, record.StepID)
	s.mu.Lock()
	current := s.claims[key]
	s.trace = append(s.trace, "terminal:"+record.StepID+":"+record.ClaimToken)
	s.mu.Unlock()
	if record.ClaimToken != current.Token {
		return errors.New("terminal used stale claim token")
	}
	if err := s.IntentStore.ConfirmTerminal(ctx, record); err != nil {
		return err
	}
	s.mu.Lock()
	delete(s.claims, key)
	s.mu.Unlock()
	return nil
}

// RenewClaim fails only the selected call and delegates every other renewal.
func (s *renewalFailureIntents) RenewClaim(ctx context.Context, runID string, revision uint64, stepID string, claim Claim) (Claim, error) {
	s.calls++
	if s.calls == s.failAt {
		return Claim{}, errors.New("renewal unavailable")
	}
	return s.memoryIntents.RenewClaim(ctx, runID, revision, stepID, claim)
}

// Claim delegates ownership and then returns the controlled durable expiry.
func (s *boundedLeaseIntents) Claim(ctx context.Context, runID string, revision uint64, stepID, owner string) (Claim, bool, error) {
	claim, ok, err := s.memoryIntents.Claim(ctx, runID, revision, stepID, owner)
	claim.ExpiresAt = s.expires
	return claim, ok, err
}

// RenewClaim preserves the deliberately bounded expiry used by lease admission tests.
func (s *boundedLeaseIntents) RenewClaim(context.Context, string, uint64, string, Claim) (Claim, error) {
	return Claim{ExpiresAt: s.expires}, nil
}

// stateKey identifies one revision step without embedding private values.
func stateKey(runID string, revision uint64, stepID string) string {
	return runID + "\x00" + itoa(int(revision)) + "\x00" + stepID
}

// Claim grants exactly one owner per step until a terminal is confirmed.
func (m *memoryIntents) Claim(_ context.Context, runID string, revision uint64, stepID, owner string) (Claim, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	key := stateKey(runID, revision, stepID)
	if _, terminal := m.terminals[key]; terminal {
		return Claim{}, false, nil
	}
	if _, claimed := m.claims[key]; claimed {
		return Claim{}, false, nil
	}
	m.claims[key] = owner
	return Claim{Owner: owner, Token: "claim:" + key, ExpiresAt: time.Unix(10_000, 0)}, true, nil
}

// RenewClaim extends only the currently owned in-memory lease and preserves its fencing token for executor tests.
func (m *memoryIntents) RenewClaim(_ context.Context, runID string, revision uint64, stepID string, claim Claim) (Claim, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	key := stateKey(runID, revision, stepID)
	if m.claims[key] != claim.Owner || claim.Token != "claim:"+key {
		return Claim{}, errors.New("claim mismatch")
	}
	claim.ExpiresAt = time.Unix(10_000, 0)
	return claim, nil
}

// ConfirmIntent persists one exact pre-I/O action intent idempotently.
func (m *memoryIntents) ConfirmIntent(_ context.Context, intent Intent) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	key := stateKey(intent.RunID, intent.Revision, intent.StepID)
	if m.claims[key] == "" || intent.ClaimToken != "claim:"+key {
		return errors.New("claim mismatch")
	}
	if prior, exists := m.intents[key]; exists {
		if !sameIntent(prior, intent) || prior.EffectStarted && !intent.EffectStarted || prior.EvidenceID != "" && prior.EvidenceID != intent.EvidenceID || intent.EvidenceID != "" && !intent.EffectStarted {
			return errors.New("intent conflict")
		}
		if prior.EffectStarted == intent.EffectStarted && prior.EvidenceID == intent.EvidenceID {
			return nil
		}
	}
	intent.ClaimToken = ""
	m.intents[key] = intent
	return nil
}

// Intent loads one previously confirmed action intent.
func (m *memoryIntents) Intent(_ context.Context, runID string, revision uint64, stepID string) (Intent, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	value, ok := m.intents[stateKey(runID, revision, stepID)]
	return value, ok, nil
}

// ConfirmTerminal converges identical deliveries and rejects contradictory durable terminals.
func (m *memoryIntents) ConfirmTerminal(_ context.Context, record StepRecord) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	key := stateKey(record.RunID, record.Revision, record.StepID)
	if owner := m.claims[key]; owner != "" && record.ClaimToken != "claim:"+key {
		return errors.New("claim mismatch")
	}
	record.ClaimToken = ""
	if prior, exists := m.terminals[key]; exists && digestValue(prior) != digestValue(record) {
		return errors.New("terminal conflict")
	}
	m.terminals[key] = record
	return nil
}

// Terminal returns one detached durable terminal record.
func (m *memoryIntents) Terminal(_ context.Context, runID string, revision uint64, stepID string) (StepRecord, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	value, ok := m.terminals[stateKey(runID, revision, stepID)]
	return value, ok, nil
}

// allowPolicy records guard ordering and can deny without exposing arbitrary reasons.
type allowPolicy struct {
	trace *[]string
	deny  string
}

// Evaluate confirms or denies runtime policy for one exact bound step.
func (p allowPolicy) Evaluate(_ context.Context, _ string, step BoundStep) (PolicyDecision, error) {
	if p.trace != nil {
		*p.trace = append(*p.trace, "policy:"+step.Step.ID)
	}
	if p.deny == step.Step.ID {
		return PolicyDenied, nil
	}
	return PolicyAllowed, nil
}

// allowBinding records exact authority revalidation before each tool attempt.
type allowBinding struct{ trace *[]string }

// Revalidate accepts sealed test bindings while recording guard order.
func (r allowBinding) Revalidate(_ context.Context, _ string, step BoundStep) error {
	if r.trace != nil {
		*r.trace = append(*r.trace, "binding:"+step.Step.ID)
	}
	return nil
}

// scriptedTool returns bounded deterministic outputs and exposes call/order counters to tests.
type scriptedTool struct {
	mu                 sync.Mutex
	trace              *[]string
	calls              map[string]int
	outputs            map[string]any
	transient          map[string]int
	functionalFailures map[string]bool
	keys               map[string][]string
	started            chan struct{}
	release            chan struct{}
}

// finalAttemptBarrierTool makes the last retry cross a deterministic I/O barrier for ownership races.
type finalAttemptBarrierTool struct {
	mu      sync.Mutex
	calls   map[string]int
	started chan struct{}
	release chan struct{}
}

// confirmationBarrierEvidence blocks one selected evidence confirmation after the executor has renewed ownership.
type confirmationBarrierEvidence struct {
	mu       sync.Mutex
	values   map[string]Evidence
	stepID   string
	started  chan struct{}
	release  chan struct{}
	deadline chan bool
}

// Confirm exposes the confirmation deadline and holds the selected step at a deterministic boundary.
func (b *confirmationBarrierEvidence) Confirm(ctx context.Context, value Evidence) (Evidence, error) {
	if value.StepID == b.stepID {
		_, bounded := ctx.Deadline()
		b.deadline <- bounded
		b.started <- struct{}{}
		select {
		case <-b.release:
		case <-ctx.Done():
			return Evidence{}, ctx.Err()
		}
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if prior, ok := b.values[value.ID]; ok && digestValue(prior) != digestValue(value) {
		return Evidence{}, errors.New("evidence conflict")
	}
	b.values[value.ID] = value
	return value, nil
}

// Load returns one detached confirmed value for comparison and recovery.
func (b *confirmationBarrierEvidence) Load(_ context.Context, id string) (Evidence, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	value, ok := b.values[id]
	if !ok {
		return Evidence{}, errors.New("evidence missing")
	}
	return value, nil
}

// confirmationBarrierAssertions blocks one selected assertion evaluation under the confirmation deadline.
type confirmationBarrierAssertions struct {
	stepID   string
	started  chan struct{}
	release  chan struct{}
	deadline chan bool
}

// firstToolBarrier holds the first selected tool call so lease time can advance before post-tool renewal.
type firstToolBarrier struct {
	mu               sync.Mutex
	stepID           string
	started, release chan struct{}
	calls            map[string]int
}

// Execute blocks only the first selected call and returns deterministic observation evidence.
func (b *firstToolBarrier) Execute(ctx context.Context, request ExecutionRequest) (ExecutionResponse, error) {
	b.mu.Lock()
	if b.calls == nil {
		b.calls = map[string]int{}
	}
	b.calls[request.StepID]++
	call := b.calls[request.StepID]
	b.mu.Unlock()
	if request.StepID == b.stepID && call == 1 {
		b.started <- struct{}{}
		select {
		case <-b.release:
		case <-ctx.Done():
			return ExecutionResponse{}, ctx.Err()
		}
	}
	value := "new"
	if request.StepID == "before" {
		value = "old"
	}
	return ExecutionResponse{Output: map[string]any{"ok": true, "value": value}, EvidenceID: "e-" + request.StepID, ResourceBindingDigest: "resource", CrossedNetwork: true}, nil
}

// callCount returns the number of controlled network boundaries crossed for one step.
func (b *firstToolBarrier) callCount(stepID string) int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.calls[stepID]
}

// Evaluate exposes the finite context and then delegates deterministic assertion semantics.
func (b *confirmationBarrierAssertions) Evaluate(ctx context.Context, declarations []planning.AssertionDeclaration, evidence Evidence) ([]planning.AssertionResult, error) {
	if evidence.StepID == b.stepID {
		_, bounded := ctx.Deadline()
		b.deadline <- bounded
		b.started <- struct{}{}
		select {
		case <-b.release:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	return (deterministicAssertions{}).Evaluate(ctx, declarations, evidence)
}

// Execute returns two transient failures then holds the final before-step attempt at the network boundary.
func (t *finalAttemptBarrierTool) Execute(ctx context.Context, request ExecutionRequest) (ExecutionResponse, error) {
	t.mu.Lock()
	if t.calls == nil {
		t.calls = map[string]int{}
	}
	t.calls[request.StepID]++
	t.mu.Unlock()
	if request.StepID == "before" && request.Attempt < 3 {
		return ExecutionResponse{CrossedNetwork: true, Transient: true}, errors.New("retry canary")
	}
	if request.StepID == "before" && request.Attempt == 3 {
		select {
		case t.started <- struct{}{}:
		default:
		}
		select {
		case <-t.release:
		case <-ctx.Done():
			return ExecutionResponse{}, ctx.Err()
		}
	}
	value := "new"
	if request.StepID == "before" {
		value = "old"
	}
	return ExecutionResponse{Output: map[string]any{"ok": true, "value": value}, EvidenceID: "e-" + request.StepID, ResourceBindingDigest: "resource", CrossedNetwork: true}, nil
}

// callCount returns synchronized attempts observed for one step.
func (t *finalAttemptBarrierTool) callCount(stepID string) int {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.calls[stepID]
}

// Execute simulates the controlled network boundary and optional barrier-driven concurrency.
func (s *scriptedTool) Execute(ctx context.Context, request ExecutionRequest) (ExecutionResponse, error) {
	s.mu.Lock()
	if s.calls == nil {
		s.calls = map[string]int{}
	}
	s.calls[request.StepID]++
	if s.keys == nil {
		s.keys = map[string][]string{}
	}
	s.keys[request.StepID] = append(s.keys[request.StepID], request.IdempotencyKey)
	call := s.calls[request.StepID]
	if s.trace != nil {
		*s.trace = append(*s.trace, "tool:"+request.StepID)
	}
	s.mu.Unlock()
	if s.started != nil {
		select {
		case s.started <- struct{}{}:
		default:
		}
		select {
		case <-s.release:
		case <-ctx.Done():
			return ExecutionResponse{}, ctx.Err()
		}
	}
	if call <= s.transient[request.StepID] {
		return ExecutionResponse{CrossedNetwork: true, Transient: true}, errors.New("external canary")
	}
	output := s.outputs[request.StepID]
	return ExecutionResponse{Output: output, EvidenceID: "e-" + request.StepID, ResourceBindingDigest: "resource", CrossedNetwork: true, FunctionalFailure: s.functionalFailures[request.StepID]}, nil
}

// callCount returns a synchronized per-step tool invocation count.
func (s *scriptedTool) callCount(stepID string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.calls[stepID]
}

// keysFor returns detached idempotency keys observed at the controlled tool boundary.
func (s *scriptedTool) keysFor(stepID string) []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.keys[stepID]...)
}

// deterministicAssertions converts the test output's ok boolean to one required deterministic result.
type deterministicAssertions struct{}

// Evaluate emits one result per declaration without consulting model text.
func (deterministicAssertions) Evaluate(_ context.Context, declarations []planning.AssertionDeclaration, evidence Evidence) ([]planning.AssertionResult, error) {
	object, ok := evidence.Output.(map[string]any)
	if !ok {
		return nil, errors.New("invalid evidence")
	}
	passed, _ := object["ok"].(bool)
	status := planning.AssertionFailed
	if passed {
		status = planning.AssertionPassed
	}
	out := make([]planning.AssertionResult, len(declarations))
	for i, declaration := range declarations {
		out[i] = planning.AssertionResult{DeclarationID: declaration.ID, NodePath: "root", Operator: "exists", Status: status}
	}
	return out, nil
}

// stableKeys generates repeatable system-owned test keys outside source input.
type stableKeys struct{}

// Key derives one opaque key per run/revision/step.
func (stableKeys) Key(runID string, revision uint64, stepID string) string {
	return "key:" + stateKey(runID, revision, stepID)
}

// immediateWaiter makes retries deterministic while still observing cancellation.
type immediateWaiter struct{}

// Wait returns immediately unless its context is already cancelled.
func (immediateWaiter) Wait(ctx context.Context, _ time.Duration) error { return ctx.Err() }

// fixedEventJournal exposes a detached authenticated-shape history to recovery causal validation tests.
type fixedEventJournal struct{ events []Event }

// AppendEvent rejects writes because recovery probes must remain read-only.
func (fixedEventJournal) AppendEvent(context.Context, Event) (Event, error) {
	return Event{}, errors.New("read-only journal")
}

// Events returns one detached pre-sealed event chain.
func (j fixedEventJournal) Events(context.Context, string) ([]Event, error) {
	return cloneRecoveryEvents(j.events), nil
}

// actionFlow builds a before/action/after graph with two explicit identities and exact effect classification.
func actionFlow() (Flow, BindingAuthority) {
	read := OperationReference{Capability: "http.request", TargetID: "preview", Method: "GET", Path: "/items/{id}", ContractDigest: digestValue("contract")}
	write := OperationReference{Capability: "http.request", TargetID: "preview", Method: "POST", Path: "/items/{id}", ContractDigest: digestValue("contract")}
	flow := Flow{Version: VersionV1, ID: "action-flow", Source: SourceProvided, Revision: 1, ContextDigest: digestValue("context"), RequiredEvidence: []string{"before-ok", "act-ok", "after-ok", "changed"}, Steps: []Step{
		{ID: "before", Kind: StepObserve, ConstraintID: "c1", DependsOn: []string{}, Operation: read, IdentityID: "reader", Input: map[string]InputValue{"id": literalNode("1")}, Assertions: []planning.AssertionDeclaration{assertionDecl("before-ok")}, Comparisons: []ObservationComparison{}, RequiredEvidence: []string{"before-ok"}},
		{ID: "act", Kind: StepAct, ConstraintID: "c1", DependsOn: []string{"before"}, Operation: write, IdentityID: "writer", Input: map[string]InputValue{"id": literalNode("1")}, Assertions: []planning.AssertionDeclaration{assertionDecl("act-ok")}, Comparisons: []ObservationComparison{}, RequiredEvidence: []string{"act-ok"}},
		{ID: "after", Kind: StepObserve, ConstraintID: "c1", DependsOn: []string{"act"}, Operation: read, IdentityID: "reader", Input: map[string]InputValue{"id": literalNode("1")}, Assertions: []planning.AssertionDeclaration{assertionDecl("after-ok")}, Comparisons: []ObservationComparison{{ID: "changed", BeforeStep: "before", BeforePath: stringPath("value"), AfterStep: "after", AfterPath: stringPath("value"), ValueType: planning.ValueString, Operator: CompareNotEqual}}, RequiredEvidence: []string{"after-ok", "changed"}},
	}}
	authority := BindingAuthority{Operations: []OperationAuthority{{Reference: read}, {Reference: write, Effectful: true, Idempotent: true}}, Identities: []testidentity.Descriptor{{ID: "reader", Mechanism: testidentity.MechanismBearer, Operations: []testidentity.Operation{{Capability: read.Capability, TargetID: read.TargetID, Method: read.Method, Path: read.Path}}}, {ID: "writer", Mechanism: testidentity.MechanismBearer, Operations: []testidentity.Operation{{Capability: write.Capability, TargetID: write.TargetID, Method: write.Method, Path: write.Path}}}}, Bindings: ArtifactBindings{Specification: digestValue("s"), Revision: digestValue("r"), Target: digestValue("t"), Contract: digestValue("c"), Identities: digestValue("i"), Policy: digestValue("p"), Fixtures: digestValue("f"), Capabilities: digestValue("cap"), Limits: digestValue("l")}}
	return flow, authority
}

// executorFixture composes a fully controlled executor and immutable action artifact.
func executorFixture(t *testing.T, tool *scriptedTool, policy allowPolicy) (*Executor, ValidatedFlow, Artifact, *memoryIntents, *memoryEvidence) {
	t.Helper()
	flow, authority := actionFlow()
	validated, err := Validate(flow, testLimits(), testAuthority())
	if err != nil {
		t.Fatal(err)
	}
	artifact, err := Bind(validated, authority)
	if err != nil {
		t.Fatal(err)
	}
	state := &memoryIntents{claims: map[string]string{}, intents: map[string]Intent{}, terminals: map[string]StepRecord{}}
	evidence := &memoryEvidence{values: map[string]Evidence{}}
	executor := &Executor{Authority: authority, Clock: fixedFlowClock{now: time.Unix(100, 0)}, Policy: policy, Revalidator: allowBinding{trace: policy.trace}, Tool: tool, Evidence: evidence, Assertions: deterministicAssertions{}, State: state, Keys: stableKeys{}, Waiter: immediateWaiter{}, Config: ExecutionConfig{Owner: "owner", MaxCalls: 10, MaxRetries: 2, MaxWork: 100, StepTimeout: time.Second, RunTimeout: 3 * time.Second, ConfirmationTimeout: time.Second}}
	return executor, validated, artifact, state, evidence
}

// TestEveryStepRechecksControlsInRequiredOrder proves binding and policy precede each tool boundary.
func TestEveryStepRechecksControlsInRequiredOrder(t *testing.T) {
	trace := []string{}
	tool := &scriptedTool{trace: &trace, outputs: map[string]any{"before": map[string]any{"ok": true, "value": "old"}, "act": map[string]any{"ok": true}, "after": map[string]any{"ok": true, "value": "new"}}, transient: map[string]int{}}
	executor, validated, artifact, _, _ := executorFixture(t, tool, allowPolicy{trace: &trace})
	result, err := executor.Execute(context.Background(), "run", validated, artifact)
	if err != nil || result.Outcome != OutcomePassed {
		t.Fatalf("result=%#v err=%v", result, err)
	}
	want := "binding:before,policy:before,tool:before,binding:act,policy:act,tool:act,binding:after,policy:after,tool:after"
	if got := strings.Join(trace, ","); got != want {
		t.Fatalf("got %s", got)
	}
}

// TestDurableJournalReconstructsRealExecution proves package causality comes from persisted runtime boundaries rather than a synthesized Result history.
func TestDurableJournalReconstructsRealExecution(t *testing.T) {
	flow, authority := actionFlow()
	validated, err := Validate(flow, testLimits(), testAuthority())
	if err != nil {
		t.Fatal(err)
	}
	artifact, err := Bind(validated, authority)
	if err != nil {
		t.Fatal(err)
	}
	clock := fixedFlowClock{now: time.Unix(100, 0)}
	state, err := NewLocalStore(t.TempDir(), clock, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	appendEvent := func(event Event) {
		event.RunID, event.FlowID, event.Revision, event.ArtifactDigest = "journal-run", artifact.FlowID, artifact.Revision, artifact.ArtifactDigest
		if _, appendErr := state.AppendEvent(context.Background(), event); appendErr != nil {
			t.Fatal(appendErr)
		}
	}
	appendEvent(Event{Type: "flow_artifact_committed"})
	appendEvent(Event{Type: "fixtures_terminal", Status: string(fixture.PreparationPrepared)})
	evidence := &memoryEvidence{values: map[string]Evidence{}}
	tool := &scriptedTool{outputs: map[string]any{"before": map[string]any{"ok": true, "value": "old"}, "act": map[string]any{"ok": true}, "after": map[string]any{"ok": true, "value": "new"}}, transient: map[string]int{}}
	config := ExecutionConfig{Owner: "owner", MaxCalls: 10, MaxRetries: 0, MaxWork: 100, StepTimeout: 5 * time.Second, RunTimeout: 60 * time.Second, ConfirmationTimeout: 10 * time.Second}
	executor := &Executor{Authority: authority, Clock: clock, Policy: allowPolicy{}, Revalidator: allowBinding{}, Tool: tool, Evidence: evidence, Assertions: deterministicAssertions{}, State: state, Journal: state, Keys: stableKeys{}, Waiter: immediateWaiter{}, Config: config}
	if _, complete, recoverErr := executor.RecoverCompleted(context.Background(), "journal-run", validated, artifact); recoverErr != nil || complete {
		t.Fatalf("partial journal complete=%t err=%v", complete, recoverErr)
	}
	result, err := executor.Execute(context.Background(), "journal-run", validated, artifact)
	if err != nil || result.Outcome != OutcomePassed {
		t.Fatalf("result=%#v err=%v", result, err)
	}
	callsAfterFirst := tool.callCount("before") + tool.callCount("act") + tool.callCount("after")
	replayed, replayErr := executor.Execute(context.Background(), "journal-run", validated, artifact)
	if replayErr != nil || digestValue(replayed) != digestValue(result) || tool.callCount("before")+tool.callCount("act")+tool.callCount("after") != callsAfterFirst {
		t.Fatalf("terminal replay=%#v calls=%d err=%v", replayed, callsAfterFirst, replayErr)
	}
	recovered, complete, recoverErr := executor.RecoverCompleted(context.Background(), "journal-run", validated, artifact)
	if recoverErr != nil || !complete || digestValue(recovered) != digestValue(result) {
		t.Fatalf("recovered=%#v complete=%t err=%v", recovered, complete, recoverErr)
	}
	manifest := []EvidenceRecord{}
	seen := map[string]bool{}
	for _, entry := range result.Ledger {
		for _, id := range entry.EvidenceIDs {
			if seen[id] {
				continue
			}
			seen[id] = true
			record, sealErr := SealEvidenceRecord(evidence.values[id])
			if sealErr != nil {
				t.Fatal(sealErr)
			}
			manifest = append(manifest, record)
		}
	}
	allEvidence := []string{}
	for _, entry := range result.Ledger {
		allEvidence = append(allEvidence, entry.EvidenceIDs...)
	}
	appendEvent(Event{Type: "primary_terminal", Outcome: result.Outcome, EvidenceIDs: allEvidence})
	appendEvent(Event{Type: "cleanup_terminal", Status: string(fixture.CleanupSucceeded)})
	sealedRecovery, complete, recoverErr := executor.RecoverCompleted(context.Background(), "journal-run", validated, artifact)
	if recoverErr != nil || !complete || digestValue(sealedRecovery) != digestValue(result) {
		t.Fatalf("sealed recovery=%#v complete=%t err=%v", sealedRecovery, complete, recoverErr)
	}
	broken := *executor
	broken.Journal = nil
	if _, _, recoverErr = broken.RecoverCompleted(context.Background(), "journal-run", validated, artifact); recoverErr == nil {
		t.Fatal("completed recovery accepted missing journal authority")
	}
	if terminalMatchesEvent(StepRecord{}, Event{}, "journal-run", 1) == nil {
		t.Fatal("empty terminal matched authenticated event")
	}
	if _, recoverErr = executor.recordFromTerminalEvent(context.Background(), "journal-run", 1, validated.Flow().Steps[0], Event{Status: "unknown"}, map[string]StepRecord{}); recoverErr == nil {
		t.Fatal("open terminal status was reconstructed")
	}
	events, err := state.Events(context.Background(), "journal-run")
	if err != nil {
		t.Fatal(err)
	}
	pkg, err := BuildPackage("journal-run", artifact, manifest, events, result, fixture.FixtureSummary{PreparationStatus: fixture.PreparationPrepared, Cleanup: fixture.CleanupReport{Status: fixture.CleanupSucceeded}}, fixture.CleanupReport{Status: fixture.CleanupSucceeded})
	if err != nil || VerifyPackage(pkg) != nil {
		t.Fatalf("events=%#v err=%v", events, err)
	}
	cloneAndReseal := func(mutate func(*Package)) Package {
		raw, _ := json.Marshal(pkg)
		var changed Package
		_ = json.Unmarshal(raw, &changed)
		mutate(&changed)
		for i := range changed.Events {
			changed.Events[i].Sequence = uint64(i + 1)
			changed.Events[i].PreviousDigest, changed.Events[i].Digest = "", ""
			if i > 0 {
				changed.Events[i].PreviousDigest = changed.Events[i-1].Digest
			}
			changed.Events[i].Digest = eventDigest(changed.Events[i])
		}
		changed.PackageDigest = packageDigest(changed)
		return changed
	}
	mutateEvent := func(eventType string, mutate func(*Event)) func(*Package) {
		return func(changed *Package) {
			for i := range changed.Events {
				if changed.Events[i].Type == eventType {
					mutate(&changed.Events[i])
					return
				}
			}
		}
	}
	attacks := []func(*Package){
		mutateEvent("step_claimed", func(event *Event) { event.OwnerDigest = "" }),
		mutateEvent("intent_confirmed", func(event *Event) { event.BindingDigest = digestValue("foreign") }),
		mutateEvent("attempt_started", func(event *Event) { event.Attempt++ }),
		mutateEvent("evidence_confirmed", func(event *Event) { event.EvidenceIDs = []string{"foreign"} }),
		mutateEvent("assertions_confirmed", func(event *Event) { event.EvidenceIDs = []string{} }),
		mutateEvent("comparisons_confirmed", func(event *Event) { event.DeclarationID = "foreign" }),
		mutateEvent("step_terminal", func(event *Event) { event.StepID = "foreign" }),
		mutateEvent("attempt_started", func(event *Event) { event.Type = "foreign" }),
	}
	for i, attack := range attacks {
		if VerifyPackage(cloneAndReseal(attack)) == nil {
			t.Fatalf("causal journal attack %d was accepted", i)
		}
	}
	recoveryAttacks := []func(*Package){
		mutateEvent("step_claimed", func(event *Event) { event.OwnerDigest = "" }),
		mutateEvent("intent_confirmed", func(event *Event) { event.BindingDigest = digestValue("foreign") }),
		mutateEvent("attempt_started", func(event *Event) { event.Attempt++ }),
		mutateEvent("assertions_confirmed", func(event *Event) { event.EvidenceIDs = nil }),
		mutateEvent("step_terminal", func(event *Event) { event.StepID = "foreign" }),
		mutateEvent("attempt_started", func(event *Event) { event.Type = "foreign" }),
		mutateEvent("step_claimed", func(event *Event) { event.Type = "retry_scheduled" }),
		mutateEvent("step_claimed", func(event *Event) { event.Type = "recovery_resumed" }),
		mutateEvent("attempt_started", func(event *Event) { event.Type = "intent_confirmed" }),
		mutateEvent("attempt_started", func(event *Event) { event.Type = "evidence_confirmed"; event.EvidenceIDs = []string{"evidence"} }),
		mutateEvent("evidence_confirmed", func(event *Event) { event.Type = "assertions_confirmed" }),
		func(changed *Package) { changed.Events[0].Type = "foreign" },
		func(changed *Package) { changed.Events[1].Type = "foreign" },
		mutateEvent("cleanup_terminal", func(event *Event) { event.Type = "primary_terminal" }),
		mutateEvent("primary_terminal", func(event *Event) { event.Type = "cleanup_terminal" }),
	}
	for i, attack := range recoveryAttacks {
		changed := cloneAndReseal(attack)
		if _, _, _, _, recoverErr := completedRuntimeEvents(changed.Events, "journal-run", artifact, validated); recoverErr == nil {
			t.Fatalf("completed recovery attack %d was accepted", i)
		}
	}
	semanticAttacks := []func(*Package){
		mutateEvent("evidence_confirmed", func(event *Event) { event.EvidenceIDs = []string{"e-act"} }),
		mutateEvent("assertions_confirmed", func(event *Event) { event.EvidenceIDs = []string{"e-act"} }),
		mutateEvent("comparisons_confirmed", func(event *Event) { event.EvidenceIDs = []string{"e-act", "e-after"} }),
		mutateEvent("step_terminal", func(event *Event) { event.EvidenceIDs = []string{"e-act"} }),
		mutateEvent("primary_terminal", func(event *Event) { event.EvidenceIDs = []string{"e-act"} }),
	}
	for i, attack := range semanticAttacks {
		changed := cloneAndReseal(attack)
		probe := *executor
		probe.Journal = fixedEventJournal{events: changed.Events}
		if _, complete, recoverErr := probe.RecoverCompleted(context.Background(), "journal-run", validated, artifact); recoverErr == nil || complete {
			t.Fatalf("semantic recovery attack %d complete=%t err=%v", i, complete, recoverErr)
		}
	}
	for name, mutate := range map[string]func(*Evidence){
		"binding":  func(item *Evidence) { item.BindingDigest = digestValue("foreign-binding") },
		"step":     func(item *Evidence) { item.StepID = "act" },
		"revision": func(item *Evidence) { item.Revision++ },
	} {
		t.Run("recovery-evidence-"+name, func(t *testing.T) {
			changedEvidence := &memoryEvidence{values: map[string]Evidence{}}
			for id, item := range evidence.values {
				changedEvidence.values[id] = item
			}
			item := changedEvidence.values["e-before"]
			mutate(&item)
			changedEvidence.values["e-before"] = SealEvidence(item)
			probe := *executor
			probe.Evidence = changedEvidence
			if _, complete, recoverErr := probe.RecoverCompleted(context.Background(), "journal-run", validated, artifact); recoverErr == nil || complete {
				t.Fatalf("divergent evidence complete=%t err=%v", complete, recoverErr)
			}
		})
	}
}

// TestExecutorPropagatesLatestLiveClaimAcrossEveryCausalCAS proves that an
// action's progress, renewals and terminal all retain one token while callers
// propagate each newly extended expiry instead of reusing the initial lease.
func TestExecutorPropagatesLatestLiveClaimAcrossEveryCausalCAS(t *testing.T) {
	clock := &advancingFlowClock{now: time.Unix(100, 0)}
	durable, err := NewLocalStore(t.TempDir(), clock, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	state := &tracedLeaseStore{IntentStore: durable, clock: clock, claims: map[string]Claim{}}
	tool := &scriptedTool{outputs: map[string]any{"before": map[string]any{"ok": true, "value": "old"}, "act": map[string]any{"ok": true}, "after": map[string]any{"ok": true, "value": "new"}}, transient: map[string]int{}}
	executor, validated, artifact, _, _ := executorFixture(t, tool, allowPolicy{})
	executor.Clock = clock
	executor.State = state
	executor.Config.Owner = "causal-owner"
	executor.Config.RunTimeout = 30 * time.Second
	executor.Config.StepTimeout = 10 * time.Second
	executor.Config.ConfirmationTimeout = 10 * time.Second
	result, err := executor.Execute(context.Background(), "causal-run", validated, artifact)
	if err != nil || result.Outcome != OutcomePassed {
		t.Fatalf("result=%#v err=%v trace=%v", result, err, state.trace)
	}

	state.mu.Lock()
	trace := append([]string(nil), state.trace...)
	state.mu.Unlock()
	claimPrefix := "claim:act:"
	claimIndex, evidenceIndex, renewalAfterEvidence, terminalIndex := -1, -1, -1, -1
	claimToken := ""
	for index, entry := range trace {
		if strings.HasPrefix(entry, claimPrefix) {
			claimIndex = index
			claimToken = strings.TrimPrefix(entry, claimPrefix)
		}
		if strings.HasPrefix(entry, "intent:act:evidence:") {
			evidenceIndex = index
		}
		if evidenceIndex >= 0 && index > evidenceIndex && strings.HasPrefix(entry, "renew:act:") && renewalAfterEvidence < 0 {
			renewalAfterEvidence = index
		}
		if strings.HasPrefix(entry, "terminal:act:") {
			terminalIndex = index
		}
		if strings.Contains(entry, ":act:") && claimToken != "" && !strings.HasSuffix(entry, claimToken) {
			t.Fatalf("action boundary changed claim token: %q trace=%v", entry, trace)
		}
	}
	if claimIndex < 0 || evidenceIndex <= claimIndex || renewalAfterEvidence <= evidenceIndex || terminalIndex <= renewalAfterEvidence {
		t.Fatalf("causal action order missing: %v", trace)
	}
}

// TestEventJournalRejectsInvalidBindingsAndConvergesDuplicateAppend proves causal persistence is idempotent and run-bound.
func TestEventJournalRejectsInvalidBindingsAndConvergesDuplicateAppend(t *testing.T) {
	store, err := NewLocalStore(t.TempDir(), fixedFlowClock{now: time.Unix(100, 0)}, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = store.AppendEvent(context.Background(), Event{}); err == nil {
		t.Fatal("invalid event was accepted")
	}
	event := Event{Type: "flow_artifact_committed", RunID: "event-run", FlowID: "flow", Revision: 1, ArtifactDigest: strings.Repeat("a", 64)}
	first, err := store.AppendEvent(context.Background(), event)
	if err != nil {
		t.Fatal(err)
	}
	duplicate, err := store.AppendEvent(context.Background(), event)
	if err != nil || duplicate.Digest != first.Digest || duplicate.Sequence != first.Sequence {
		t.Fatalf("duplicate=%#v first=%#v err=%v", duplicate, first, err)
	}
	foreign := event
	foreign.FlowID = "foreign"
	foreign.Type = "fixtures_terminal"
	if _, err = store.AppendEvent(context.Background(), foreign); err == nil {
		t.Fatal("foreign journal binding was accepted")
	}
	events, err := store.Events(context.Background(), event.RunID)
	if err != nil || len(events) != 1 || events[0].Digest != first.Digest {
		t.Fatalf("events=%#v err=%v", events, err)
	}
	if _, err = store.Events(context.Background(), "bad/path"); err == nil {
		t.Fatal("invalid journal coordinate was accepted")
	}
	state, err := store.load(event.RunID)
	if err != nil {
		t.Fatal(err)
	}
	state.Events[0].Digest = strings.Repeat("b", 64)
	if err = store.save(event.RunID, state); err != nil {
		t.Fatal(err)
	}
	if _, err = store.Events(context.Background(), event.RunID); err == nil {
		t.Fatal("tampered causal chain was accepted")
	}
}

// TestRequiredComparisonControlsEndToEndVerdict proves a failed required comparison prevents a passed flow.
func TestRequiredComparisonControlsEndToEndVerdict(t *testing.T) {
	tool := &scriptedTool{outputs: map[string]any{"before": map[string]any{"ok": true, "value": "same"}, "act": map[string]any{"ok": true}, "after": map[string]any{"ok": true, "value": "same"}}, transient: map[string]int{}}
	executor, validated, artifact, _, _ := executorFixture(t, tool, allowPolicy{})
	result, err := executor.Execute(context.Background(), "comparison-run", validated, artifact)
	if err != nil || result.Outcome != OutcomeFailed || len(result.Comparisons) != 1 || result.Comparisons[0].Status != planning.AssertionFailed {
		t.Fatalf("result=%#v err=%v", result, err)
	}
}

// TestFunctionalStepFailureControlsVerdict proves a confirmed functional tool failure cannot aggregate as passed.
func TestFunctionalStepFailureControlsVerdict(t *testing.T) {
	tool := &scriptedTool{outputs: map[string]any{"before": map[string]any{"ok": true, "value": "old"}, "act": map[string]any{"ok": true}}, transient: map[string]int{}, functionalFailures: map[string]bool{"act": true}}
	executor, validated, artifact, _, _ := executorFixture(t, tool, allowPolicy{})
	result, err := executor.Execute(context.Background(), "failure-run", validated, artifact)
	if err != nil || result.Outcome != OutcomeFailed {
		t.Fatalf("result=%#v err=%v", result, err)
	}
}

// TestPerStepDenialAndExhaustionOpenNoNetwork proves pre-I/O denials consume no tool calls.
func TestPerStepDenialAndExhaustionOpenNoNetwork(t *testing.T) {
	tool := &scriptedTool{outputs: map[string]any{"before": map[string]any{"ok": true}}, transient: map[string]int{}}
	executor, validated, artifact, _, _ := executorFixture(t, tool, allowPolicy{deny: "before"})
	result, err := executor.Execute(context.Background(), "run", validated, artifact)
	if err != nil || tool.callCount("before") != 0 || result.Outcome != OutcomeInconclusive {
		t.Fatalf("result=%#v calls=%d err=%v", result, tool.callCount("before"), err)
	}
}

// TestBlockedStepsRecordAllCausesAndIndependentBranchesContinue proves scheduler blocking is causal and branch-local.
func TestBlockedStepsRecordAllCausesAndIndependentBranchesContinue(t *testing.T) {
	flow, authority := actionFlow()
	independent := cloneStep(flow.Steps[0])
	independent.ID, independent.Assertions[0].ID, independent.RequiredEvidence[0] = "independent", "independent-ok", "independent-ok"
	flow.Steps = append(flow.Steps, independent)
	flow.RequiredEvidence = append(flow.RequiredEvidence, "independent-ok")
	validated, err := Validate(flow, testLimits(), testAuthority())
	if err != nil {
		t.Fatal(err)
	}
	artifact, err := Bind(validated, authority)
	if err != nil {
		t.Fatal(err)
	}
	tool := &scriptedTool{outputs: map[string]any{"before": map[string]any{"ok": false}, "independent": map[string]any{"ok": true}}, transient: map[string]int{}}
	state := &memoryIntents{claims: map[string]string{}, intents: map[string]Intent{}, terminals: map[string]StepRecord{}}
	executor := &Executor{Authority: authority, Clock: fixedFlowClock{now: time.Unix(100, 0)}, Policy: allowPolicy{}, Revalidator: allowBinding{}, Tool: tool, Evidence: &memoryEvidence{values: map[string]Evidence{}}, Assertions: deterministicAssertions{}, State: state, Keys: stableKeys{}, Waiter: immediateWaiter{}, Config: ExecutionConfig{Owner: "owner", MaxCalls: 10, MaxRetries: 0, MaxWork: 100, StepTimeout: time.Second, RunTimeout: 3 * time.Second, ConfirmationTimeout: time.Second}}
	result, err := executor.Execute(context.Background(), "run", validated, artifact)
	if err != nil || tool.callCount("act") != 0 || tool.callCount("independent") != 1 || result.Outcome != OutcomeFailed {
		t.Fatalf("result=%#v err=%v", result, err)
	}
}

// TestCancellationStopsEveryUnstartedBranch proves caller cancellation prevents later tool starts.
func TestCancellationStopsEveryUnstartedBranch(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	tool := &scriptedTool{outputs: map[string]any{}, transient: map[string]int{}}
	executor, validated, artifact, _, _ := executorFixture(t, tool, allowPolicy{})
	result, err := executor.Execute(ctx, "run", validated, artifact)
	if err != nil || tool.callCount("before") != 0 || result.Outcome != OutcomeCancelled {
		t.Fatalf("result=%#v err=%v", result, err)
	}
}

// TestActionIntentPrecedesNetworkAndUncertainEffectsAreNotDuplicated proves action intent durability and conservative redelivery.
func TestActionIntentPrecedesNetworkAndUncertainEffectsAreNotDuplicated(t *testing.T) {
	tool := &scriptedTool{outputs: map[string]any{"before": map[string]any{"ok": true, "value": "old"}, "act": map[string]any{"ok": true}, "after": map[string]any{"ok": true, "value": "new"}}, transient: map[string]int{}}
	executor, validated, artifact, state, _ := executorFixture(t, tool, allowPolicy{})
	result, err := executor.Execute(context.Background(), "run", validated, artifact)
	if err != nil || result.Outcome != OutcomePassed {
		t.Fatal(err, result.Outcome)
	}
	if _, ok, _ := state.Intent(context.Background(), "run", 1, "act"); !ok {
		t.Fatal("action intent missing")
	}
	_, err = executor.Execute(context.Background(), "run", validated, artifact)
	if err != nil || tool.callCount("act") != 1 {
		t.Fatalf("duplicate action calls=%d err=%v", tool.callCount("act"), err)
	}
}

// TestOnlyVerifiedTransientOperationsRetryWithStableKey proves bounded retries require contractual safety.
func TestOnlyVerifiedTransientOperationsRetryWithStableKey(t *testing.T) {
	tool := &scriptedTool{outputs: map[string]any{"before": map[string]any{"ok": true, "value": "old"}, "act": map[string]any{"ok": true}, "after": map[string]any{"ok": true, "value": "new"}}, transient: map[string]int{"act": 1}}
	executor, validated, artifact, _, _ := executorFixture(t, tool, allowPolicy{})
	result, err := executor.Execute(context.Background(), "run", validated, artifact)
	if err != nil || result.Outcome != OutcomePassed || tool.callCount("act") != 2 {
		t.Fatalf("result=%#v calls=%d err=%v", result, tool.callCount("act"), err)
	}
}

// TestConcurrentOwnersAndRedeliveryDoNotDuplicateEffects proves durable claims serialize two owners without sleeps.
func TestConcurrentOwnersAndRedeliveryDoNotDuplicateEffects(t *testing.T) {
	tool := &scriptedTool{outputs: map[string]any{"before": map[string]any{"ok": true, "value": "old"}, "act": map[string]any{"ok": true}, "after": map[string]any{"ok": true, "value": "new"}}, transient: map[string]int{}, started: make(chan struct{}, 1), release: make(chan struct{})}
	first, validated, artifact, state, evidence := executorFixture(t, tool, allowPolicy{})
	second := *first
	second.Config.Owner = "owner-2"
	firstDone := make(chan error, 1)
	go func() { _, err := first.Execute(context.Background(), "run", validated, artifact); firstDone <- err }()
	<-tool.started
	if _, err := second.Execute(context.Background(), "run", validated, artifact); err == nil {
		t.Fatal("second owner should converge only after terminal")
	}
	close(tool.release)
	if err := <-firstDone; err != nil {
		t.Fatal(err)
	}
	_ = state
	_ = evidence
	if tool.callCount("act") != 1 {
		t.Fatalf("action calls=%d", tool.callCount("act"))
	}
}

// TestCrashAndCancellationMatrixConvergesAndJoins exercises confirmed-terminal recovery and cancellation checkpoints.
func TestCrashAndCancellationMatrixConvergesAndJoins(t *testing.T) {
	tool := &scriptedTool{outputs: map[string]any{"before": map[string]any{"ok": true, "value": "old"}, "act": map[string]any{"ok": true}, "after": map[string]any{"ok": true, "value": "new"}}, transient: map[string]int{}}
	executor, validated, artifact, _, _ := executorFixture(t, tool, allowPolicy{})
	first, err := executor.Execute(context.Background(), "run", validated, artifact)
	if err != nil {
		t.Fatal(err)
	}
	second, err := executor.Execute(context.Background(), "run", validated, artifact)
	if err != nil || digestValue(first) != digestValue(second) {
		t.Fatalf("recovery diverged err=%v", err)
	}
}

// TestActionCrashMatrixRecoversWithoutUnsafeReplay covers prepared, effect-started, and evidence-confirmed checkpoints.
func TestActionCrashMatrixRecoversWithoutUnsafeReplay(t *testing.T) {
	for _, checkpoint := range []string{"intent", "effect", "evidence"} {
		t.Run(checkpoint, func(t *testing.T) {
			flow, authority := actionFlow()
			authority.Operations[1].Idempotent = false
			validated, err := Validate(flow, testLimits(), testAuthority())
			if err != nil {
				t.Fatal(err)
			}
			artifact, err := Bind(validated, authority)
			if err != nil {
				t.Fatal(err)
			}
			state := &memoryIntents{claims: map[string]string{}, intents: map[string]Intent{}, terminals: map[string]StepRecord{}}
			evidence := &memoryEvidence{values: map[string]Evidence{}}
			beforeEvidence := SealEvidence(Evidence{ID: "e-before", RunID: "crash-run", Revision: 1, StepID: "before", BindingDigest: artifact.Steps[0].Operation.BindingDigest, ResourceBindingDigest: "resource", Output: map[string]any{"ok": true, "value": "old"}})
			evidence.values[beforeEvidence.ID] = beforeEvidence
			beforeAssertion := planning.AssertionResult{DeclarationID: "before-ok", NodePath: "root", Operator: "exists", Status: planning.AssertionPassed, EvidenceIDs: []string{beforeEvidence.ID}}
			state.terminals[stateKey("crash-run", 1, "before")] = StepRecord{RunID: "crash-run", Revision: 1, StepID: "before", Status: planning.StepPassed, EvidenceID: beforeEvidence.ID, Assertions: []planning.AssertionResult{beforeAssertion}}
			intent := Intent{RunID: "crash-run", Revision: 1, StepID: "act", BindingDigest: artifact.Steps[1].Operation.BindingDigest}
			if checkpoint != "intent" {
				intent.EffectStarted = true
			}
			if checkpoint == "evidence" {
				confirmed := SealEvidence(Evidence{ID: "e-act", RunID: "crash-run", Revision: 1, StepID: "act", BindingDigest: artifact.Steps[1].Operation.BindingDigest, ResourceBindingDigest: "resource", Output: map[string]any{"ok": true}})
				evidence.values[confirmed.ID] = confirmed
				intent.EvidenceID = confirmed.ID
			}
			state.intents[stateKey("crash-run", 1, "act")] = intent
			tool := &scriptedTool{outputs: map[string]any{"act": map[string]any{"ok": true}, "after": map[string]any{"ok": true, "value": "new"}}, transient: map[string]int{}}
			executor := &Executor{Authority: authority, Clock: fixedFlowClock{now: time.Unix(100, 0)}, Policy: allowPolicy{}, Revalidator: allowBinding{}, Tool: tool, Evidence: evidence, Assertions: deterministicAssertions{}, State: state, Keys: stableKeys{}, Waiter: immediateWaiter{}, Config: ExecutionConfig{Owner: "new-owner", MaxCalls: 10, MaxRetries: 0, MaxWork: 100, StepTimeout: time.Second, RunTimeout: 3 * time.Second, ConfirmationTimeout: time.Second}}
			result, err := executor.Execute(context.Background(), "crash-run", validated, artifact)
			if err != nil {
				t.Fatal(err)
			}
			switch checkpoint {
			case "intent":
				if tool.callCount("act") != 1 || len(tool.keysFor("act")) != 1 || tool.keysFor("act")[0] != "" {
					t.Fatalf("unsafe initial effect key/calls=%v/%d", tool.keysFor("act"), tool.callCount("act"))
				}
			case "effect":
				if tool.callCount("act") != 0 || result.Outcome == OutcomePassed {
					t.Fatalf("uncertain effect replayed/result=%#v", result)
				}
			case "evidence":
				if tool.callCount("act") != 0 || result.Outcome != OutcomePassed {
					t.Fatalf("confirmed evidence replayed/result=%#v", result)
				}
			}
		})
	}
}

// TestInputResolutionFailureConfirmsOneFencedTerminal proves a claimed step
// cannot escape ownership merely because its private fixture is unavailable.
func TestInputResolutionFailureConfirmsOneFencedTerminal(t *testing.T) {
	flow, authority := actionFlow()
	flow.Steps[0].Input["id"] = InputValue{Reference: &Reference{Fixture: &FixtureReference{FixtureID: "seed", Output: "id", ValueType: planning.ValueString}}}
	validated, err := Validate(flow, testLimits(), testAuthority())
	if err != nil {
		t.Fatal(err)
	}
	artifact, err := Bind(validated, authority)
	if err != nil {
		t.Fatal(err)
	}
	tool := &scriptedTool{outputs: map[string]any{}, transient: map[string]int{}}
	state := &memoryIntents{claims: map[string]string{}, intents: map[string]Intent{}, terminals: map[string]StepRecord{}}
	executor := &Executor{Authority: authority, Clock: fixedFlowClock{now: time.Unix(100, 0)}, Policy: allowPolicy{}, Revalidator: allowBinding{}, Tool: tool, Evidence: &memoryEvidence{values: map[string]Evidence{}}, Assertions: deterministicAssertions{}, State: state, Keys: stableKeys{}, Waiter: immediateWaiter{}, Config: ExecutionConfig{Owner: "owner", MaxCalls: 10, MaxRetries: 0, MaxWork: 100, StepTimeout: time.Second, RunTimeout: 3 * time.Second, ConfirmationTimeout: time.Second}}
	result, executeErr := executor.Execute(context.Background(), "resolution-run", validated, artifact)
	if executeErr != nil || len(result.Steps) != len(flow.Steps) || result.Steps[0].Category != CategoryResolution || tool.callCount("before") != 0 {
		t.Fatalf("result=%#v calls=%d err=%v", result, tool.callCount("before"), executeErr)
	}
	terminal, found, err := state.Terminal(context.Background(), "resolution-run", 1, "before")
	if err != nil || !found || terminal.Category != CategoryResolution {
		t.Fatalf("terminal=%#v found=%t err=%v", terminal, found, err)
	}
	renewalState := &memoryIntents{claims: map[string]string{}, intents: map[string]Intent{}, terminals: map[string]StepRecord{}}
	renewalExecutor := *executor
	renewalExecutor.State = &renewalFailureIntents{memoryIntents: renewalState, failAt: 1}
	if _, err = renewalExecutor.Execute(context.Background(), "resolution-renewal", validated, artifact); err == nil {
		t.Fatal("resolution terminal ignored renewal failure")
	}
	terminalState := &memoryIntents{claims: map[string]string{}, intents: map[string]Intent{}, terminals: map[string]StepRecord{}}
	terminalExecutor := *executor
	terminalExecutor.State = rejectingTerminalStore{memoryIntents: terminalState}
	if _, err = terminalExecutor.Execute(context.Background(), "resolution-terminal", validated, artifact); err == nil {
		t.Fatal("resolution terminal CAS failure was ignored")
	}
	journalState := &memoryIntents{claims: map[string]string{}, intents: map[string]Intent{}, terminals: map[string]StepRecord{}}
	journalExecutor := *executor
	journalExecutor.State = journalState
	journalExecutor.Journal = selectiveJournal{failType: "step_terminal"}
	if _, err = journalExecutor.Execute(context.Background(), "resolution-journal", validated, artifact); err == nil {
		t.Fatal("resolution terminal journal failure was ignored")
	}
}

// TestLeaseMustCoverStepAndConfirmationBoundaries proves a short lease opens no tool boundary.
func TestLeaseMustCoverStepAndConfirmationBoundaries(t *testing.T) {
	tool := &scriptedTool{outputs: map[string]any{}, transient: map[string]int{}}
	executor, validated, artifact, _, _ := executorFixture(t, tool, allowPolicy{})
	executor.Clock = fixedFlowClock{now: time.Unix(9_999, 0)}
	if _, err := executor.Execute(context.Background(), "short-lease", validated, artifact); err == nil || tool.callCount("before") != 0 {
		t.Fatalf("err=%v calls=%d", err, tool.callCount("before"))
	}
}

// TestLeaseMustCoverAllRetriesBackoffAndConfirmation rejects ownership that expires during the final permitted attempt.
func TestLeaseMustCoverAllRetriesBackoffAndConfirmation(t *testing.T) {
	tool := &scriptedTool{outputs: map[string]any{}, transient: map[string]int{}}
	executor, validated, artifact, _, _ := executorFixture(t, tool, allowPolicy{})
	now := time.Unix(100, 0)
	executor.Clock = fixedFlowClock{now: now}
	executor.Config.MaxRetries = 2
	executor.Config.StepTimeout = 3 * time.Second
	executor.Config.RetryDelay = time.Second
	executor.Config.ConfirmationTimeout = 2 * time.Second
	executor.State = &boundedLeaseIntents{memoryIntents: &memoryIntents{claims: map[string]string{}, intents: map[string]Intent{}, terminals: map[string]StepRecord{}}, expires: now.Add(10 * time.Second)}
	if _, err := executor.Execute(context.Background(), "retry-lease", validated, artifact); err == nil || tool.callCount("before") != 0 {
		t.Fatalf("err=%v calls=%d", err, tool.callCount("before"))
	}
}

// TestTotalClaimWindowRejectsArithmeticOverflow proves malicious retry and timeout budgets cannot shorten ownership.
func TestTotalClaimWindowRejectsArithmeticOverflow(t *testing.T) {
	maxDuration := time.Duration(1<<63 - 1)
	cases := []ExecutionConfig{
		{MaxRetries: int(^uint(0) >> 1), StepTimeout: time.Second, ConfirmationTimeout: time.Second},
		{MaxRetries: 1, StepTimeout: maxDuration, ConfirmationTimeout: time.Second},
		{MaxRetries: 2, StepTimeout: time.Second, RetryDelay: maxDuration, ConfirmationTimeout: time.Second},
		{MaxRetries: 0, StepTimeout: maxDuration, ConfirmationTimeout: time.Second},
	}
	for i, config := range cases {
		if _, ok := totalClaimWindow(config); ok {
			t.Fatalf("overflow case %d was accepted", i)
		}
	}
}

// TestFinalAttemptLeaseCannotBeReclaimedBySameOrDifferentOwner proves total-window ownership survives the last I/O barrier under race.
func TestFinalAttemptLeaseCannotBeReclaimedBySameOrDifferentOwner(t *testing.T) {
	flow, authority := actionFlow()
	validated, err := Validate(flow, testLimits(), testAuthority())
	if err != nil {
		t.Fatal(err)
	}
	artifact, err := Bind(validated, authority)
	if err != nil {
		t.Fatal(err)
	}
	clock := &advancingFlowClock{now: time.Unix(100, 0)}
	state, err := NewLocalStore(t.TempDir(), clock, 7*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	tool := &finalAttemptBarrierTool{started: make(chan struct{}, 1), release: make(chan struct{})}
	newExecutor := func(owner string) *Executor {
		return &Executor{Authority: authority, Clock: clock, Policy: allowPolicy{}, Revalidator: allowBinding{}, Tool: tool, Evidence: &memoryEvidence{values: map[string]Evidence{}}, Assertions: deterministicAssertions{}, State: state, Keys: stableKeys{}, Waiter: immediateWaiter{}, Config: ExecutionConfig{Owner: owner, MaxCalls: 10, MaxRetries: 2, MaxWork: 100, StepTimeout: time.Second, RunTimeout: 10 * time.Second, ConfirmationTimeout: time.Second, RetryDelay: time.Second}}
	}
	firstResult := make(chan error, 1)
	go func() {
		_, runErr := newExecutor("owner-a").Execute(context.Background(), "lease-race", validated, artifact)
		firstResult <- runErr
	}()
	<-tool.started
	clock.Advance(6 * time.Second)
	for _, owner := range []string{"owner-a", "owner-b"} {
		if _, contenderErr := newExecutor(owner).Execute(context.Background(), "lease-race", validated, artifact); contenderErr == nil {
			t.Fatalf("owner %s reclaimed an active final attempt", owner)
		}
	}
	if calls := tool.callCount("before"); calls != 3 {
		t.Fatalf("duplicate network attempt count=%d", calls)
	}
	close(tool.release)
	if err = <-firstResult; err != nil {
		t.Fatal(err)
	}
}

// TestPostToolOwnershipRenewsAcrossEvidenceAssertionsAndTerminal proves confirmation work remains exclusively owned past the original expiry.
func TestPostToolOwnershipRenewsAcrossEvidenceAssertionsAndTerminal(t *testing.T) {
	flow := baseFlow()
	validated, err := Validate(flow, testLimits(), testAuthority())
	if err != nil {
		t.Fatal(err)
	}
	authority := testBindingAuthority(flow)
	artifact, err := Bind(validated, authority)
	if err != nil {
		t.Fatal(err)
	}
	clock := &advancingFlowClock{now: time.Unix(100, 0)}
	state, err := NewLocalStore(t.TempDir(), clock, 4*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	tool := &firstToolBarrier{stepID: "before", started: make(chan struct{}, 1), release: make(chan struct{}), calls: map[string]int{}}
	evidence := &confirmationBarrierEvidence{values: map[string]Evidence{}, stepID: "before", started: make(chan struct{}, 1), release: make(chan struct{}), deadline: make(chan bool, 1)}
	assertions := &confirmationBarrierAssertions{stepID: "before", started: make(chan struct{}, 1), release: make(chan struct{}), deadline: make(chan bool, 1)}
	newExecutor := func(owner string) *Executor {
		return &Executor{Authority: authority, Clock: clock, Policy: allowPolicy{}, Revalidator: allowBinding{}, Tool: tool, Evidence: evidence, Assertions: assertions, State: state, Keys: stableKeys{}, Waiter: immediateWaiter{}, Config: ExecutionConfig{Owner: owner, MaxCalls: 10, MaxRetries: 0, MaxWork: 100, StepTimeout: time.Second, RunTimeout: 20 * time.Second, ConfirmationTimeout: 2 * time.Second}}
	}
	finished := make(chan error, 1)
	go func() {
		_, runErr := newExecutor("owner-a").Execute(context.Background(), "post-tool-renewal", validated, artifact)
		finished <- runErr
	}()
	<-tool.started
	clock.Advance(3 * time.Second)
	close(tool.release)
	<-evidence.started
	if !<-evidence.deadline {
		t.Fatal("evidence confirmation received no ConfirmationTimeout")
	}
	clock.Advance(2 * time.Second)
	for _, owner := range []string{"owner-a", "owner-b"} {
		if _, contenderErr := newExecutor(owner).Execute(context.Background(), "post-tool-renewal", validated, artifact); contenderErr == nil {
			t.Fatalf("owner %s reclaimed during evidence confirmation", owner)
		}
	}
	close(evidence.release)
	<-assertions.started
	if !<-assertions.deadline {
		t.Fatal("assertion evaluation received no ConfirmationTimeout")
	}
	clock.Advance(3 * time.Second)
	for _, owner := range []string{"owner-a", "owner-b"} {
		if _, contenderErr := newExecutor(owner).Execute(context.Background(), "post-tool-renewal", validated, artifact); contenderErr == nil {
			t.Fatalf("owner %s reclaimed during assertion evaluation", owner)
		}
	}
	close(assertions.release)
	if err = <-finished; err != nil {
		t.Fatal(err)
	}
	if tool.callCount("before") != 1 || tool.callCount("after") != 1 {
		t.Fatalf("duplicate tools before=%d after=%d", tool.callCount("before"), tool.callCount("after"))
	}
}

// TestPostToolRenewalFailureStopsBeforeEvidenceAndAllowsSafeObserveRecovery proves expired ownership fails closed without a terminal forgery.
func TestPostToolRenewalFailureStopsBeforeEvidenceAndAllowsSafeObserveRecovery(t *testing.T) {
	flow := baseFlow()
	validated, err := Validate(flow, testLimits(), testAuthority())
	if err != nil {
		t.Fatal(err)
	}
	authority := testBindingAuthority(flow)
	artifact, err := Bind(validated, authority)
	if err != nil {
		t.Fatal(err)
	}
	clock := &advancingFlowClock{now: time.Unix(100, 0)}
	state, err := NewLocalStore(t.TempDir(), clock, 4*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	tool := &firstToolBarrier{stepID: "before", started: make(chan struct{}, 1), release: make(chan struct{}), calls: map[string]int{}}
	evidence := &confirmationBarrierEvidence{values: map[string]Evidence{}, stepID: "", started: make(chan struct{}, 1), release: make(chan struct{}), deadline: make(chan bool, 1)}
	newExecutor := func(owner string) *Executor {
		return &Executor{Authority: authority, Clock: clock, Policy: allowPolicy{}, Revalidator: allowBinding{}, Tool: tool, Evidence: evidence, Assertions: deterministicAssertions{}, State: state, Keys: stableKeys{}, Waiter: immediateWaiter{}, Config: ExecutionConfig{Owner: owner, MaxCalls: 10, MaxRetries: 0, MaxWork: 100, StepTimeout: time.Second, RunTimeout: 20 * time.Second, ConfirmationTimeout: 2 * time.Second}}
	}
	failed := make(chan error, 1)
	go func() {
		_, runErr := newExecutor("owner-a").Execute(context.Background(), "renewal-failure", validated, artifact)
		failed <- runErr
	}()
	<-tool.started
	clock.Advance(5 * time.Second)
	close(tool.release)
	if err = <-failed; err == nil {
		t.Fatal("expired owner crossed evidence confirmation")
	}
	if _, ok, terminalErr := state.Terminal(context.Background(), "renewal-failure", 1, "before"); terminalErr != nil || ok {
		t.Fatalf("forged terminal ok=%v err=%v", ok, terminalErr)
	}
	result, err := newExecutor("owner-b").Execute(context.Background(), "renewal-failure", validated, artifact)
	if err != nil || result.Outcome != OutcomePassed || tool.callCount("before") != 2 {
		t.Fatalf("recovery result=%#v calls=%d err=%v", result, tool.callCount("before"), err)
	}
}

// TestEveryPostToolRenewalFailureFailsClosed covers evidence, journal, assertions, and terminal ownership checkpoints.
func TestEveryPostToolRenewalFailureFailsClosed(t *testing.T) {
	for _, failAt := range []int{1, 2, 3, 4, 5, 8} {
		t.Run(itoa(failAt), func(t *testing.T) {
			tool := &scriptedTool{outputs: map[string]any{"before": map[string]any{"ok": true, "value": "old"}}, transient: map[string]int{}}
			executor, validated, artifact, state, _ := executorFixture(t, tool, allowPolicy{})
			executor.State = &renewalFailureIntents{memoryIntents: state, failAt: failAt}
			if _, err := executor.Execute(context.Background(), "renewal-boundary", validated, artifact); err == nil {
				t.Fatalf("renewal boundary %d was ignored", failAt)
			}
			if tool.callCount("before") != 1 || tool.callCount("after") != 0 {
				t.Fatalf("boundary=%d calls=%d/%d", failAt, tool.callCount("before"), tool.callCount("after"))
			}
		})
	}
}

// TestExecutorFaultBoundariesFailClosed exercises every causal error boundary
// introduced between attempt start, durable evidence and assertion journaling.
func TestExecutorFaultBoundariesFailClosed(t *testing.T) {
	cases := []struct {
		name      string
		configure func(*Executor, *memoryIntents, *scriptedTool)
	}{
		{name: "attempt journal", configure: func(executor *Executor, _ *memoryIntents, _ *scriptedTool) {
			executor.Journal = selectiveJournal{failType: "attempt_started"}
		}},
		{name: "binding revalidation", configure: func(executor *Executor, _ *memoryIntents, _ *scriptedTool) { executor.Revalidator = rejectingBinding{} }},
		{name: "call budget on retry", configure: func(executor *Executor, _ *memoryIntents, tool *scriptedTool) {
			executor.Config.MaxCalls = 1
			tool.transient["before"] = 1
		}},
		{name: "retry journal", configure: func(executor *Executor, _ *memoryIntents, tool *scriptedTool) {
			executor.Journal = selectiveJournal{failType: "retry_scheduled"}
			tool.transient["before"] = 1
		}},
		{name: "missing retry waiter", configure: func(executor *Executor, _ *memoryIntents, tool *scriptedTool) {
			executor.Waiter = nil
			tool.transient["before"] = 1
		}},
		{name: "cancelled retry waiter", configure: func(executor *Executor, _ *memoryIntents, tool *scriptedTool) {
			executor.Waiter = rejectingWaiter{}
			tool.transient["before"] = 1
		}},
		{name: "evidence persistence", configure: func(executor *Executor, _ *memoryIntents, _ *scriptedTool) { executor.Evidence = rejectingEvidence{} }},
		{name: "invalid evidence receipt", configure: func(executor *Executor, _ *memoryIntents, _ *scriptedTool) {
			executor.Evidence = rejectingEvidence{invalid: true}
		}},
		{name: "evidence journal", configure: func(executor *Executor, _ *memoryIntents, _ *scriptedTool) {
			executor.Journal = selectiveJournal{failType: "evidence_confirmed"}
		}},
		{name: "assertion evaluator", configure: func(executor *Executor, _ *memoryIntents, _ *scriptedTool) {
			executor.Assertions = rejectingAssertions{}
		}},
		{name: "assertion journal", configure: func(executor *Executor, _ *memoryIntents, _ *scriptedTool) {
			executor.Journal = selectiveJournal{failType: "assertions_confirmed"}
		}},
		{name: "terminal journal", configure: func(executor *Executor, _ *memoryIntents, _ *scriptedTool) {
			executor.Journal = selectiveJournal{failType: "step_terminal"}
		}},
		{name: "functional failure", configure: func(_ *Executor, _ *memoryIntents, tool *scriptedTool) {
			tool.functionalFailures = map[string]bool{"before": true}
		}},
		{name: "started intent", configure: func(executor *Executor, state *memoryIntents, _ *scriptedTool) {
			executor.State = &rejectingProgressStore{memoryIntents: state}
		}},
		{name: "evidence intent", configure: func(executor *Executor, state *memoryIntents, _ *scriptedTool) {
			executor.State = &rejectingProgressStore{memoryIntents: state, failEvidence: true}
		}},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			tool := &scriptedTool{outputs: map[string]any{"before": map[string]any{"ok": true, "value": "old"}, "act": map[string]any{"ok": true}, "after": map[string]any{"ok": true, "value": "new"}}, transient: map[string]int{}}
			executor, validated, artifact, state, _ := executorFixture(t, tool, allowPolicy{})
			testCase.configure(executor, state, tool)
			result, err := executor.Execute(context.Background(), "fault-run", validated, artifact)
			if err == nil && result.Outcome == OutcomePassed {
				t.Fatalf("fault %s produced a passing result", testCase.name)
			}
			if tool.callCount("after") != 0 {
				t.Fatalf("fault %s crossed a later tool boundary", testCase.name)
			}
		})
	}
}

// TestFlowCanariesNeverCrossDurableModelOrExternalBoundaries proves execution serialization omits inputs and idempotency keys.
func TestFlowCanariesNeverCrossDurableModelOrExternalBoundaries(t *testing.T) {
	request := ExecutionRequest{RunID: "run", Revision: 1, StepID: "step", Input: map[string]any{"secret": "RAW-CANARY"}, IdempotencyKey: "KEY-CANARY"}
	raw, err := json.Marshal(request)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "CANARY") {
		t.Fatalf("leak: %s", raw)
	}
	flow := baseFlow()
	flow.Steps[0].Input["secret"] = literalNode("RAW-CANARY")
	validated, err := Validate(flow, testLimits(), testAuthority())
	if err != nil {
		t.Fatal(err)
	}
	artifact, err := Bind(validated, testBindingAuthority(flow))
	if err != nil {
		t.Fatal(err)
	}
	raw, _ = json.Marshal(artifact)
	if strings.Contains(string(raw), "RAW-CANARY") {
		t.Fatalf("artifact leak: %s", raw)
	}
}

// TestUntrustedDataCannotCreateAuthority proves only the binder can produce a digest-valid artifact.
func TestUntrustedDataCannotCreateAuthority(t *testing.T) {
	artifact := Artifact{Version: "FlowArtifact/v1", FlowID: "flow", Revision: 1, SemanticDigest: "producer", ArtifactDigest: "forged", Steps: []BoundStep{{Step: Step{ID: "step", IdentityID: "admin"}, Operation: BoundOperation{BindingDigest: "forged", Identity: testidentity.Binding{IdentityID: "admin"}}}}}
	if err := ValidateArtifact(artifact); err == nil {
		t.Fatal("accepted forged authority")
	}
}
