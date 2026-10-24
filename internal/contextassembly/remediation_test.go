package contextassembly

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/asierhr/featureProof/internal/projectmemory"
)

// testProducerRegistry supplies explicitly selected current producers to exercise composition-owned restart resolution.
type testProducerRegistry struct {
	producers []Producer
	err       error
}

// Resolve returns the test's current producer set without deriving it from the historical descriptor.
func (r testProducerRegistry) Resolve(ctx context.Context, _ string, _ RegistryDescriptor, _ AuthorityBinding) ([]Producer, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if r.err != nil {
		return nil, r.err
	}
	return append([]Producer(nil), r.producers...), nil
}

// registryProducersFromSnapshot creates test current bindings while keeping the production resolver contract explicit.
func registryProducersFromSnapshot(snapshot Snapshot) []Producer {
	producers := make([]Producer, len(snapshot.SourceBindings))
	for index, binding := range snapshot.SourceBindings {
		producers[index] = &StaticProducer{Source: binding.Kind, Projection: Projection{Binding: binding}, Current: binding}
	}
	return producers
}

// adversarialProducer simulates a collaborator attempting to choose a stronger trust contract.
type adversarialProducer struct {
	*StaticProducer
	declaredTrust TrustClass
}

// TrustClass returns the attacker-selected producer contract for composition rejection tests.
func (p adversarialProducer) TrustClass() TrustClass { return p.declaredTrust }

// recordingCounter captures the exact bytes presented to the provider tokenizer boundary.
type recordingCounter struct {
	payloads [][]byte
	flip     bool
}

// deadlineProducer blocks until the effective assembly deadline proves context propagation and join.
type deadlineProducer struct{ Producer }

// Produce waits for cancellation and returns only after observing the assembly deadline.
func (p deadlineProducer) Produce(ctx context.Context, _ Request) (Projection, error) {
	<-ctx.Done()
	return Projection{}, ctx.Err()
}

// deadlinePhaseProbe blocks one selected collaborator boundary and records its effective deadline.
type deadlinePhaseProbe struct {
	phase    string
	deadline time.Time
	calls    int
	records  []Record
	store    *MemoryStore
	gate     *CanaryGate
}

// block records and consumes the selected phase deadline so post-boundary cancellation is observable.
func (p *deadlinePhaseProbe) block(ctx context.Context, phase string) error {
	if p.phase != phase {
		return nil
	}
	p.deadline, _ = ctx.Deadline()
	<-ctx.Done()
	return ctx.Err()
}

// Check probes the secret-filter boundary before delegating ordinary values.
func (p *deadlinePhaseProbe) Check(ctx context.Context, item Item) error {
	if err := p.block(ctx, "filter"); err != nil {
		return err
	}
	return p.gate.Check(ctx, item)
}

// Rank probes semantic ranking and otherwise returns the supplied order.
func (p *deadlinePhaseProbe) Rank(ctx context.Context, _ Role, inputs []RankInput, _ int) ([]RankedID, error) {
	if err := p.block(ctx, "rank"); err != nil {
		return nil, err
	}
	result := make([]RankedID, len(inputs))
	for index, input := range inputs {
		result[index] = RankedID{ID: input.ID, Score: float64(len(inputs) - index)}
	}
	return result, nil
}

// Count probes the provider tokenizer over the final canonical envelope.
func (p *deadlinePhaseProbe) Count(ctx context.Context, _, _ string, payload []byte) (int, error) {
	if err := p.block(ctx, "tokenizer"); err != nil {
		return 0, err
	}
	return (len(payload) + 3) / 4, nil
}

// CommitSnapshot probes durable publication and otherwise delegates to the in-memory store.
func (p *deadlinePhaseProbe) CommitSnapshot(ctx context.Context, key string, request Request, snapshot Snapshot) (Snapshot, bool, error) {
	if err := p.block(ctx, "store"); err != nil {
		return Snapshot{}, false, err
	}
	return p.store.CommitSnapshot(ctx, key, request, snapshot)
}

// LoadSnapshot delegates immutable snapshot retrieval for the phase probe.
func (p *deadlinePhaseProbe) LoadSnapshot(ctx context.Context, digest string) (Snapshot, error) {
	return p.store.LoadSnapshot(ctx, digest)
}

// CommitBinding delegates content-free binding publication for the phase probe.
func (p *deadlinePhaseProbe) CommitBinding(ctx context.Context, key string, binding ContextBinding) (ContextBinding, bool, error) {
	return p.store.CommitBinding(ctx, key, binding)
}

// LoadBinding delegates content-free binding retrieval for the phase probe.
func (p *deadlinePhaseProbe) LoadBinding(ctx context.Context, digest string) (ContextBinding, error) {
	return p.store.LoadBinding(ctx, digest)
}

// Observe records exactly one terminal outcome and probes its reserved detached finalization deadline.
func (p *deadlinePhaseProbe) Observe(ctx context.Context, record Record) error {
	p.calls++
	p.records = append(p.records, record)
	return p.block(ctx, "observer")
}

// deadlineProbeProducer blocks one registered producer without changing its composition-owned contract.
type deadlineProbeProducer struct {
	Producer
	probe *deadlinePhaseProbe
}

// Produce probes source acquisition and otherwise delegates the neutral projection.
func (p deadlineProbeProducer) Produce(ctx context.Context, request Request) (Projection, error) {
	if err := p.probe.block(ctx, "producer"); err != nil {
		return Projection{}, err
	}
	return p.Producer.Produce(ctx, request)
}

// Count records model-facing bytes and optionally produces a non-convergent adversarial result.
func (c *recordingCounter) Count(ctx context.Context, _, _ string, payload []byte) (int, error) {
	if ctx.Err() != nil {
		return 0, ctx.Err()
	}
	c.payloads = append(c.payloads, append([]byte(nil), payload...))
	if c.flip {
		return len(payload) + len(c.payloads)%2, nil
	}
	return len(payload), nil
}

// TestCompositionOwnsEveryTrustClass exercises every source and authority against both escalation paths.
func TestCompositionOwnsEveryTrustClass(t *testing.T) {
	profiles, err := DefaultProfiles()
	if err != nil {
		t.Fatal(err)
	}
	allKinds := []SourceKind{SourceConstraints, SourceContract, SourceDiff, SourceConfig, SourceMemory, SourceHistory, SourcePlan, SourceDiagnostic, SourceEvidence, SourceCapability}
	authorities := []Authority{AuthorityComposition, AuthorityApproved, AuthorityVerified, AuthorityObserved}
	for _, kind := range allKinds {
		base := testProducer(t, kind, testItem("candidate", kind, "inert"))
		escalated := adversarialProducer{StaticProducer: base, declaredTrust: TrustInstructions}
		if _, composeErr := NewAssembler(Config{ObserverTimeout: time.Second}, Dependencies{Profiles: profiles, Producers: []Producer{escalated}, Secrets: NewCanaryGate(nil), Tokens: ByteTokenCounter{Selections: map[string]bool{"provider/model": true}}, Store: NewMemoryStore(), Observer: &testObserver{}, Clock: &testClock{}}); !IsCategory(composeErr, CategoryInvalid) {
			t.Fatalf("kind %s registered instructions producer: %v", kind, composeErr)
		}
		for _, authority := range authorities {
			item := testItem("candidate", kind, "inert")
			item.Trust, item.Authority = TrustInstructions, authority
			projection, projectionErr := NewProjection(kind, "adversary", "v1", CompletenessComplete, []Item{item})
			if projectionErr != nil {
				t.Fatal(projectionErr)
			}
			if _, validationErr := validateProjection(projection, testRequest(), func() Profile { p, _ := profiles.Profile(RolePlanner); return p }(), kind); !IsCategory(validationErr, CategoryInvalid) {
				t.Fatalf("kind=%s authority=%s elevated trust: %v", kind, authority, validationErr)
			}
		}
	}
}

// TestProfileFieldsAndRequiredCompleteness covers least-privilege narrowing and post-pack source policy.
func TestProfileFieldsAndRequiredCompleteness(t *testing.T) {
	constraint := testProducer(t, SourceConstraints, testItem("constraint", SourceConstraints, "required"))
	contract := testProducer(t, SourceContract, testItem("contract", SourceContract, "required"))
	configItem := testItem("config", SourceConfig, "required")
	configItem.Authority, configItem.State = AuthorityApproved, "approved"
	config := testProducer(t, SourceConfig, configItem)
	memoryLeft, memoryRight := testItem("memory-left", SourceMemory, "a"), testItem("memory-right", SourceMemory, "b")
	memoryLeft.Authority, memoryRight.Authority = AuthorityApproved, AuthorityApproved
	memoryLeft.LogicalKey, memoryRight.LogicalKey = "optional", "optional"
	memoryLeft.ValueDigest, memoryRight.ValueDigest = strings.Repeat("1", 64), strings.Repeat("2", 64)
	memory := testProducer(t, SourceMemory, memoryLeft, memoryRight)
	assembler, _ := testAssembler(t, NewMemoryStore(), constraint, contract, config, memory)
	request := testRequest(SourceConstraints, SourceContract, SourceConfig, SourceMemory)
	request.Fields = map[SourceKind][]string{SourceConstraints: {"value"}}
	snapshot, err := assembler.Assemble(context.Background(), request)
	if err != nil || snapshot.Completeness != CompletenessPartial || len(snapshot.Conflicts) != 1 {
		t.Fatalf("optional conflict policy=%#v err=%v", snapshot, err)
	}
	widened := request
	widened.IdempotencyKey = "field-widening"
	widened.Fields = map[SourceKind][]string{SourceConstraints: {"secret_field"}}
	if _, err = assembler.Assemble(context.Background(), widened); !IsCategory(err, CategoryUnauthorized) {
		t.Fatalf("field widening accepted: %v", err)
	}
	filtered := testItem("required-filtered", SourceConstraints, "required")
	filtered.ProjectID = "foreign"
	filteredProducer := testProducer(t, SourceConstraints, filtered)
	assembler, _ = testAssembler(t, NewMemoryStore(), filteredProducer, contract, config)
	missing, err := assembler.Assemble(context.Background(), testRequest(SourceConstraints, SourceContract, SourceConfig))
	if err != nil || missing.Completeness != CompletenessInsufficient {
		t.Fatalf("fully filtered required source remained usable: %#v %v", missing, err)
	}
}

// TestModelFacingCountsUseFinalEnvelope proves exact-limit acceptance and non-monotonic counter rejection.
func TestModelFacingCountsUseFinalEnvelope(t *testing.T) {
	producers := []Producer{testProducer(t, SourceConstraints, testItem("constraint", SourceConstraints, "x")), testProducer(t, SourceContract, testItem("contract", SourceContract, "x")), testProducer(t, SourceConfig, func() Item {
		item := testItem("config", SourceConfig, "x")
		item.Authority, item.State = AuthorityApproved, "approved"
		return item
	}())}
	profiles, _ := DefaultProfiles()
	counter := &recordingCounter{}
	assembler, err := NewAssembler(Config{ObserverTimeout: time.Second}, Dependencies{Profiles: profiles, Producers: producers, Secrets: NewCanaryGate(nil), Tokens: counter, Store: NewMemoryStore(), Observer: &testObserver{}, Clock: &testClock{}})
	if err != nil {
		t.Fatal(err)
	}
	request := testRequest(SourceConstraints, SourceContract, SourceConfig)
	snapshot, err := assembler.Assemble(context.Background(), request)
	if err != nil || len(counter.payloads) == 0 || snapshot.Counts.Bytes != len(counter.payloads[len(counter.payloads)-1]) || snapshot.Counts.Tokens != len(counter.payloads[len(counter.payloads)-1]) {
		t.Fatalf("counts do not describe tokenizer payload: %#v err=%v", snapshot.Counts, err)
	}
	exactCounter := &recordingCounter{}
	exactAssembler, _ := NewAssembler(Config{ObserverTimeout: time.Second}, Dependencies{Profiles: profiles, Producers: producers, Secrets: NewCanaryGate(nil), Tokens: exactCounter, Store: NewMemoryStore(), Observer: &testObserver{}, Clock: &testClock{}})
	exact := request
	exact.IdempotencyKey = "exact-envelope-limit"
	exact.Limits.MaxBytes, exact.Limits.MaxTokens = snapshot.Counts.Bytes, snapshot.Counts.Tokens
	if result, exactErr := exactAssembler.Assemble(context.Background(), exact); exactErr != nil || result.Completeness != CompletenessComplete || result.Counts.Bytes != snapshot.Counts.Bytes {
		t.Fatalf("exact model boundary rejected: %#v %v", result.Counts, exactErr)
	}
	flipping := &recordingCounter{flip: true}
	unstable, _ := NewAssembler(Config{ObserverTimeout: time.Second}, Dependencies{Profiles: profiles, Producers: producers, Secrets: NewCanaryGate(nil), Tokens: flipping, Store: NewMemoryStore(), Observer: &testObserver{}, Clock: &testClock{}})
	request.IdempotencyKey = "unstable-counter"
	if _, err = unstable.Assemble(context.Background(), request); !IsCategory(err, CategoryInvalid) {
		t.Fatalf("non-convergent counter accepted: %v", err)
	}
}

// TestRedactionStateAndSensitiveTaxonomyFailClosed covers the complete prohibited-content families.
func TestRedactionStateAndSensitiveTaxonomyFailClosed(t *testing.T) {
	gate := NewCanaryGate([][]byte{[]byte("nested-canary")})
	patterns := []any{
		map[string]any{"metadata": map[string]string{"note": "nested-canary"}},
		map[string]any{"metadata": []any{map[string]string{"token": "nested-token"}}},
		map[string]any{"metadata": map[string]any{"credential": map[string]string{"value": "nested-credential"}}},
		map[string]string{"authorization": "Basic abcdefgh"},
		map[string]string{"client_secret": "secretabcdefgh"},
		map[string]string{"access_token": "tokenabcdefgh"},
		map[string]string{"api_key": "keyabcdefgh"},
		map[string]string{"password": "passwordabcdefgh"},
		map[string]string{"cookie": "sessionabcdefgh"},
		map[string]string{"set_cookie": "sessionabcdefgh"},
		map[string]string{"connection_string": "postgres://user:pass@host/db"},
		map[string]string{"database_url": "postgres://user:pass@host/db"},
		map[string]string{"aws_secret_access_key": "awssecretabcdefgh"},
		map[string]string{"private_token": "privateabcdefgh"},
		map[string]string{"raw_body": "unredacted"},
		map[string]string{"request_body": "unredacted"},
		map[string]string{"response_body": "unredacted"},
		map[string]string{"prompt": "hidden prompt"},
		map[string]string{"chain_of_thought": "private reasoning"},
	}
	for index, value := range patterns {
		if err := gate.Check(context.Background(), Item{Payload: SafeJSON(value)}); !IsCategory(err, CategorySecret) {
			t.Fatalf("sensitive family %d accepted: %v", index, err)
		}
	}
	unknown := testItem("unknown-redaction", SourceConstraints, "safe")
	unknown.Redaction = ""
	projection, err := NewProjection(SourceConstraints, "producer", "v1", CompletenessComplete, []Item{unknown})
	if err != nil {
		t.Fatal(err)
	}
	profiles, _ := DefaultProfiles()
	planner, _ := profiles.Profile(RolePlanner)
	if _, err = validateProjection(projection, testRequest(), planner, SourceConstraints); !IsCategory(err, CategoryInvalid) {
		t.Fatalf("unknown redaction accepted: %v", err)
	}
}

// TestEnvelopeCarriesPlanAndTypedChannels ensures the final canonical envelope cannot collapse trust sections.
func TestEnvelopeCarriesPlanAndTypedChannels(t *testing.T) {
	assembler, _ := testAssembler(t, NewMemoryStore(), testProducer(t, SourceConstraints, testItem("constraint", SourceConstraints, "x")), testProducer(t, SourceContract, testItem("contract", SourceContract, "x")), testProducer(t, SourceConfig, func() Item {
		item := testItem("config", SourceConfig, "x")
		item.Authority, item.State = AuthorityApproved, "approved"
		return item
	}()))
	snapshot, err := assembler.Assemble(context.Background(), testRequest(SourceConstraints, SourceContract, SourceConfig))
	if err != nil {
		t.Fatal(err)
	}
	envelope, err := Envelope(snapshot)
	if err != nil || envelope.PlanID != snapshot.PlanID || envelope.Instructions == "" || len(envelope.Configuration) != 1 || len(envelope.Untrusted) != 2 {
		t.Fatalf("typed envelope invalid: %#v %v", envelope, err)
	}
	raw, _ := json.Marshal(envelope)
	if strings.Contains(string(raw), envelope.Instructions) || !strings.Contains(string(raw), snapshot.PlanID) {
		t.Fatalf("instructions leaked into untrusted envelope: %s", raw)
	}
}

// TestPreMaterializationWorkBoundsRejectOversizedSources covers finite source, candidate, payload and duration work.
func TestPreMaterializationWorkBoundsRejectOversizedSources(t *testing.T) {
	constraint := testProducer(t, SourceConstraints, testItem("constraint", SourceConstraints, "x"))
	contract := testProducer(t, SourceContract, testItem("contract", SourceContract, "x"))
	configItem := testItem("config", SourceConfig, "x")
	configItem.Authority, configItem.State = AuthorityApproved, "approved"
	config := testProducer(t, SourceConfig, configItem)
	assembler, _ := testAssembler(t, NewMemoryStore(), constraint, contract, config)
	sourceBound := testRequest(SourceConstraints, SourceContract, SourceConfig)
	sourceBound.Limits.MaxSources = 2
	if _, err := assembler.Assemble(context.Background(), sourceBound); !IsCategory(err, CategoryUnauthorized) {
		t.Fatalf("source bound ignored: %v", err)
	}
	many := testProducer(t, SourceConstraints, testItem("one", SourceConstraints, "x"), testItem("two", SourceConstraints, "x"), testItem("three", SourceConstraints, "x"))
	assembler, _ = testAssembler(t, NewMemoryStore(), many, contract, config)
	candidateBound := testRequest(SourceConstraints, SourceContract, SourceConfig)
	candidateBound.IdempotencyKey = "candidate-bound"
	candidateBound.Limits.MaxCandidates = 2
	if _, err := assembler.Assemble(context.Background(), candidateBound); !IsCategory(err, CategoryInvalid) {
		t.Fatalf("candidate bound ignored: %v", err)
	}
	large := testProducer(t, SourceConstraints, testItem("large", SourceConstraints, strings.Repeat("x", 128)))
	assembler, _ = testAssembler(t, NewMemoryStore(), large, contract, config)
	payloadBound := testRequest(SourceConstraints, SourceContract, SourceConfig)
	payloadBound.IdempotencyKey = "payload-bound"
	payloadBound.Limits.MaxPayloadBytes = 32
	if _, err := assembler.Assemble(context.Background(), payloadBound); !IsCategory(err, CategoryInvalid) {
		t.Fatalf("payload bound ignored: %v", err)
	}
	durationClock := &testClock{now: time.Unix(1, 0)}
	profiles, _ := DefaultProfiles()
	durationAssembler, _ := NewAssembler(Config{ObserverTimeout: time.Second}, Dependencies{Profiles: profiles, Producers: []Producer{deadlineProducer{Producer: constraint}, contract, config}, Secrets: NewCanaryGate(nil), Tokens: ByteTokenCounter{Selections: map[string]bool{"provider/model": true}}, Store: NewMemoryStore(), Observer: &testObserver{}, Clock: durationClock})
	durationBound := testRequest(SourceConstraints, SourceContract, SourceConfig)
	durationBound.IdempotencyKey = "duration-bound"
	durationBound.Limits.MaxDurationMS = 1
	if _, err := durationAssembler.Assemble(context.Background(), durationBound); !IsCategory(err, CategoryTimeout) || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("duration bound ignored: %v", err)
	}
}

// TestEffectiveDeadlinePropagatesAcrossEveryCollaboratorBoundary proves caller/profile minima and joined cancellation.
func TestEffectiveDeadlinePropagatesAcrossEveryCollaboratorBoundary(t *testing.T) {
	for _, phase := range []string{"producer", "filter", "rank", "tokenizer", "store", "observer"} {
		t.Run(phase, func(t *testing.T) {
			profiles, err := DefaultProfiles()
			if err != nil {
				t.Fatal(err)
			}
			probe := &deadlinePhaseProbe{phase: phase, store: NewMemoryStore(), gate: NewCanaryGate(nil)}
			constraint := testProducer(t, SourceConstraints, testItem("constraint", SourceConstraints, "x"))
			contract := testProducer(t, SourceContract, testItem("contract", SourceContract, "x"))
			configItem := testItem("config", SourceConfig, "x")
			configItem.Authority, configItem.State = AuthorityApproved, "approved"
			config := testProducer(t, SourceConfig, configItem)
			producers := []Producer{deadlineProbeProducer{Producer: constraint, probe: probe}, contract, config}
			assembler, err := NewAssembler(Config{ObserverTimeout: time.Second}, Dependencies{Profiles: profiles, Producers: producers, Secrets: probe, Ranker: probe, Tokens: probe, Store: probe, Observer: probe, Clock: &testClock{now: time.Unix(1, 0)}})
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
			callerDeadline, _ := ctx.Deadline()
			defer cancel()
			request := testRequest(SourceConstraints, SourceContract, SourceConfig)
			request.IdempotencyKey = "deadline-" + phase
			request.Limits.MaxDurationMS = 500
			if _, err = assembler.Assemble(ctx, request); !IsCategory(err, CategoryTimeout) || !errors.Is(err, context.DeadlineExceeded) {
				t.Fatalf("phase %s did not fail closed on deadline: %v", phase, err)
			}
			if probe.calls != 1 || len(probe.records) != 1 {
				t.Fatalf("phase %s terminal records=%d values=%#v", phase, probe.calls, probe.records)
			}
			expectedOutcome := OutcomeCancelled
			if phase == "observer" {
				expectedOutcome = OutcomeAssembled
			}
			if probe.records[0].Outcome != expectedOutcome {
				t.Fatalf("phase %s terminal outcome=%s want=%s", phase, probe.records[0].Outcome, expectedOutcome)
			}
			if phase == "observer" {
				if probe.deadline.IsZero() || !probe.deadline.After(callerDeadline) {
					t.Fatalf("observer did not receive detached reserved deadline: caller=%s received=%s", callerDeadline, probe.deadline)
				}
			} else if probe.deadline.IsZero() || probe.deadline.After(callerDeadline.Add(time.Millisecond)) {
				t.Fatalf("phase %s received widened work deadline: caller=%s received=%s", phase, callerDeadline, probe.deadline)
			}
		})
	}
}

// TestTerminalObserverExactlyOnceForEveryOutcome proves every closed terminal vocabulary value crosses one bounded observation boundary.
func TestTerminalObserverExactlyOnceForEveryOutcome(t *testing.T) {
	newAssembly := func(t *testing.T) (*Assembler, *testObserver, Request) {
		t.Helper()
		config := testItem("config", SourceConfig, "x")
		config.Authority, config.State = AuthorityApproved, "approved"
		assembler, observer := testAssembler(t, NewMemoryStore(), testProducer(t, SourceConstraints, testItem("constraint", SourceConstraints, "x")), testProducer(t, SourceContract, testItem("contract", SourceContract, "x")), testProducer(t, SourceConfig, config))
		return assembler, observer, testRequest(SourceConstraints, SourceContract, SourceConfig)
	}
	tests := []struct {
		name string
		want Outcome
		run  func(*testing.T, *Assembler, Request)
	}{
		{name: "assembled", want: OutcomeAssembled, run: func(t *testing.T, assembler *Assembler, request Request) {
			_, _ = assembler.Assemble(context.Background(), request)
		}},
		{name: "insufficient", want: OutcomeInsufficient, run: func(t *testing.T, assembler *Assembler, request Request) {
			request.IdempotencyKey, request.Limits.MaxBytes, request.Limits.MaxTokens = "observer-insufficient", 1, 1
			_, _ = assembler.Assemble(context.Background(), request)
		}},
		{name: "failed", want: OutcomeFailed, run: func(t *testing.T, assembler *Assembler, request Request) {
			request.Version = "unknown"
			_, _ = assembler.Assemble(context.Background(), request)
		}},
		{name: "cancelled", want: OutcomeCancelled, run: func(t *testing.T, assembler *Assembler, request Request) {
			ctx, cancel := context.WithCancel(context.Background())
			cancel()
			_, _ = assembler.Assemble(ctx, request)
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			assembler, observer, request := newAssembly(t)
			test.run(t, assembler, request)
			if len(observer.records) != 1 || observer.records[0].Outcome != test.want {
				t.Fatalf("terminal records=%#v want=%s", observer.records, test.want)
			}
		})
	}
	assembler, observer, request := newAssembly(t)
	snapshot, err := assembler.Assemble(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	binding, err := assembler.Bind(context.Background(), "observer-outcome-binding", request.PlanID, strings.Repeat("d", 64), snapshot.Digest)
	if err != nil {
		t.Fatal(err)
	}
	before := len(observer.records)
	result, err := assembler.Revalidate(context.Background(), binding)
	if err != nil || result.Outcome != OutcomeValid || len(observer.records) != before+1 || observer.records[before].Outcome != OutcomeValid {
		t.Fatalf("valid terminal mismatch: %#v records=%#v err=%v", result, observer.records, err)
	}
	current := registryProducersFromSnapshot(snapshot)
	producers, err := RevalidationProducers(context.Background(), snapshot, testProducerRegistry{producers: current})
	if err != nil {
		t.Fatal(err)
	}
	producers[0].(*StaticProducer).Current.Digest = strings.Repeat("e", 64)
	staleAssembler, err := NewAssembler(assembler.config, Dependencies{Profiles: assembler.deps.Profiles, Producers: producers, Secrets: assembler.deps.Secrets, Ranker: assembler.deps.Ranker, Tokens: assembler.deps.Tokens, Store: assembler.deps.Store, Observer: observer, Clock: assembler.deps.Clock})
	if err != nil {
		t.Fatal(err)
	}
	before = len(observer.records)
	result, err = staleAssembler.Revalidate(context.Background(), binding)
	if err != nil || result.Outcome != OutcomeStale || len(observer.records) != before+1 || observer.records[before].Outcome != OutcomeStale {
		t.Fatalf("stale terminal mismatch: %#v records=%#v err=%v", result, observer.records, err)
	}
}

// TestDurableRegistryRejectsProfileAuthorityAndSourceSubstitution proves restart reconstruction accepts only the sealed composition descriptor.
func TestDurableRegistryRejectsProfileAuthorityAndSourceSubstitution(t *testing.T) {
	assembler, _ := testAssembler(t, NewMemoryStore(), testProducer(t, SourceConstraints, testItem("constraint", SourceConstraints, "x")), testProducer(t, SourceContract, testItem("contract", SourceContract, "x")), testProducer(t, SourceConfig, func() Item {
		item := testItem("config", SourceConfig, "x")
		item.Authority, item.State = AuthorityApproved, "approved"
		return item
	}()))
	snapshot, err := assembler.Assemble(context.Background(), testRequest(SourceConstraints, SourceContract, SourceConfig))
	if err != nil {
		t.Fatal(err)
	}
	mutations := []struct {
		name   string
		mutate func(*Snapshot)
	}{
		{name: "profile", mutate: func(value *Snapshot) { value.Registry.ProfileVersion = "substituted" }},
		{name: "instruction", mutate: func(value *Snapshot) { value.Registry.InstructionVersion = "substituted" }},
		{name: "authority", mutate: func(value *Snapshot) { value.Registry.AuthorityID = strings.Repeat("f", 64) }},
		{name: "source", mutate: func(value *Snapshot) { value.Registry.Sources[0].Producer = "substituted" }},
		{name: "missing-source", mutate: func(value *Snapshot) { value.Registry.Sources = value.Registry.Sources[:1] }},
	}
	for _, test := range mutations {
		t.Run(test.name, func(t *testing.T) {
			candidate := cloneSnapshot(snapshot)
			test.mutate(&candidate)
			if _, err := RevalidationProducers(context.Background(), candidate, testProducerRegistry{producers: registryProducersFromSnapshot(snapshot)}); !IsCategory(err, CategoryPersistence) {
				t.Fatalf("registry substitution accepted: %v", err)
			}
		})
	}
}

// TestCurrentRegistryRejectsMissingAndVersionMismatchedProducers proves durable IDs cannot manufacture historical adapters.
func TestCurrentRegistryRejectsMissingAndVersionMismatchedProducers(t *testing.T) {
	assembler, _ := testAssembler(t, NewMemoryStore(), testProducer(t, SourceConstraints, testItem("constraint", SourceConstraints, "x")), testProducer(t, SourceContract, testItem("contract", SourceContract, "x")), testProducer(t, SourceConfig, func() Item {
		item := testItem("config", SourceConfig, "x")
		item.Authority, item.State = AuthorityApproved, "approved"
		return item
	}()))
	snapshot, err := assembler.Assemble(context.Background(), testRequest(SourceConstraints, SourceContract, SourceConfig))
	if err != nil {
		t.Fatal(err)
	}
	current := registryProducersFromSnapshot(snapshot)
	if _, err = RevalidationProducers(context.Background(), snapshot, testProducerRegistry{err: errors.New("registry unavailable")}); err == nil || err.Error() != "registry unavailable" {
		t.Fatalf("registry resolution error was not preserved: %v", err)
	}
	withNil := append([]Producer(nil), current...)
	withNil[0] = nil
	if _, err = RevalidationProducers(context.Background(), snapshot, testProducerRegistry{producers: withNil}); !IsCategory(err, CategoryConflict) {
		t.Fatalf("nil current producer accepted: %v", err)
	}
	duplicate := append([]Producer(nil), current...)
	duplicate[1] = duplicate[0]
	if _, err = RevalidationProducers(context.Background(), snapshot, testProducerRegistry{producers: duplicate}); !IsCategory(err, CategoryConflict) {
		t.Fatalf("duplicate current producer accepted: %v", err)
	}
	if _, err = RevalidationProducers(context.Background(), snapshot, testProducerRegistry{producers: current[:len(current)-1]}); !IsCategory(err, CategoryConflict) {
		t.Fatalf("missing current producer accepted: %v", err)
	}
	mismatched := registryProducersFromSnapshot(snapshot)
	producer := mismatched[0].(*StaticProducer)
	producer.Projection.Binding.ProducerVersion = "v2"
	if _, err = RevalidationProducers(context.Background(), snapshot, testProducerRegistry{producers: mismatched}); !IsCategory(err, CategoryConflict) {
		t.Fatalf("producer version mismatch accepted: %v", err)
	}
	wrongKind := registryProducersFromSnapshot(snapshot)
	replacement := wrongKind[0].(*StaticProducer)
	replacement.Source = SourceEvidence
	wrongKind[0] = replacement
	if _, err = RevalidationProducers(context.Background(), snapshot, testProducerRegistry{producers: wrongKind}); !IsCategory(err, CategoryConflict) {
		t.Fatalf("missing sealed kind accepted: %v", err)
	}
}

// TestPackingReservesMandatoryGuardsBeforeReportingInsufficient proves tiny budgets never drop trusted instructions to fit content.
func TestPackingReservesMandatoryGuardsBeforeReportingInsufficient(t *testing.T) {
	configItem := testItem("config", SourceConfig, "x")
	configItem.Authority, configItem.State = AuthorityApproved, "approved"
	assembler, _ := testAssembler(t, NewMemoryStore(), testProducer(t, SourceConstraints, testItem("constraint", SourceConstraints, "x")), testProducer(t, SourceContract, testItem("contract", SourceContract, "x")), testProducer(t, SourceConfig, configItem))
	request := testRequest(SourceConstraints, SourceContract, SourceConfig)
	request.IdempotencyKey = "mandatory-guard-budget"
	request.Limits.MaxBytes, request.Limits.MaxTokens = 1, 1
	snapshot, err := assembler.Assemble(context.Background(), request)
	if err != nil || snapshot.Completeness != CompletenessInsufficient {
		t.Fatalf("mandatory guards were not preserved as insufficient: %#v err=%v", snapshot, err)
	}
	if len(snapshot.Sections) == 0 || snapshot.Sections[0].Trust != TrustInstructions || len(snapshot.Sections[0].Items) != 1 {
		t.Fatalf("trusted instructions were dropped: %#v", snapshot.Sections)
	}
}

// TestAdapterAndEnvelopeClosedErrorMatrix proves malformed domain projections and trust sections fail before model use.
func TestAdapterAndEnvelopeClosedErrorMatrix(t *testing.T) {
	authority := testAuthority()
	if _, err := NewProjection(SourcePlan, "producer", "v1", CompletenessComplete, []Item{{Payload: json.RawMessage(`{"broken":`)}}); err == nil {
		t.Fatal("malformed producer payload accepted")
	}
	if _, err := DomainProjectionProducer(SourceConfig, "producer", "v1", CompletenessComplete, nil); !IsCategory(err, CategoryInvalid) {
		t.Fatalf("forbidden domain producer accepted: %v", err)
	}
	if _, err := DomainProjectionProducer(SourcePlan, "producer", "v1", CompletenessComplete, []Item{{Payload: json.RawMessage(`{"broken":`)}}); err == nil {
		t.Fatal("malformed domain projection accepted")
	}
	emptyMemory, err := MemoryProducer(authority, nil, projectmemory.MemoryBinding{})
	if err != nil || emptyMemory.Projection.Binding.Completeness != CompletenessPartial {
		t.Fatalf("empty memory completeness=%#v err=%v", emptyMemory, err)
	}
	configItem := testItem("config", SourceConfig, "x")
	configItem.Authority, configItem.State = AuthorityApproved, "approved"
	assembler, _ := testAssembler(t, NewMemoryStore(), testProducer(t, SourceConstraints, testItem("constraint", SourceConstraints, "x")), testProducer(t, SourceContract, testItem("contract", SourceContract, "x")), testProducer(t, SourceConfig, configItem))
	snapshot, err := assembler.Assemble(context.Background(), testRequest(SourceConstraints, SourceContract, SourceConfig))
	if err != nil {
		t.Fatal(err)
	}
	badDigest := cloneSnapshot(snapshot)
	badDigest.Digest = strings.Repeat("f", 64)
	if _, err = Envelope(badDigest); !IsCategory(err, CategoryInvalid) {
		t.Fatalf("invalid envelope digest accepted: %v", err)
	}
	badInstructions := cloneSnapshot(snapshot)
	badInstructions.Sections[0].Items = append(badInstructions.Sections[0].Items, badInstructions.Sections[0].Items[0])
	badInstructions.Digest = ""
	badInstructions.Digest, _ = digestValue(badInstructions)
	if _, err = Envelope(badInstructions); !IsCategory(err, CategoryInvalid) {
		t.Fatalf("multiple instruction items accepted: %v", err)
	}
	badTrust := cloneSnapshot(snapshot)
	badTrust.Sections[0].Trust = TrustClass("foreign")
	badTrust.Digest = ""
	badTrust.Digest, _ = digestValue(badTrust)
	if _, err = Envelope(badTrust); !IsCategory(err, CategoryInvalid) {
		t.Fatalf("unknown envelope trust accepted: %v", err)
	}
}
