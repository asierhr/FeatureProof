package controlplane

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strings"
	"testing"
	"time"
)

// fixedClock exposes controlled scheduler time.
type fixedClock struct{ now time.Time }

// Now returns the test-owned instant.
func (clock fixedClock) Now() time.Time { return clock.now }

// noopWaiter makes idle loops observable without sleeping.
type noopWaiter struct{}

// Wait returns cancellation immediately for deterministic loop termination.
func (noopWaiter) Wait(context.Context, time.Duration) error { return context.Canceled }

// blockingRuntimeWaiter exposes scheduler idle entry and waits only for caller cancellation.
type blockingRuntimeWaiter struct{ entered chan struct{} }

// Wait signals readiness once and transfers shutdown entirely to the caller context.
func (waiter blockingRuntimeWaiter) Wait(ctx context.Context, _ time.Duration) error {
	select {
	case waiter.entered <- struct{}{}:
	default:
	}
	<-ctx.Done()
	return ctx.Err()
}

// TestSafeIDUsesTheSharedBoundedAlphabet proves exported composition checks
// cannot diverge from persistence and authority identifier validation.
func TestSafeIDUsesTheSharedBoundedAlphabet(t *testing.T) {
	if !SafeID("project:run-1") || SafeID("") || SafeID("bad/path") || SafeID(strings.Repeat("a", 129)) {
		t.Fatal("SafeID diverged from the control-plane identifier grammar")
	}
}

// failingHandler injects one classified worker failure.
type failingHandler struct{ err error }

// Handle returns the configured failure without external I/O.
func (handler failingHandler) Handle(context.Context, Job) error { return handler.err }

// fixedClassifier returns one bounded retry policy.
type fixedClassifier struct{ decision RetryDecision }

// Classify maps any injected failure to the controlled decision.
func (classifier fixedClassifier) Classify(error) RetryDecision { return classifier.decision }

// successHandler completes jobs without effects for terminal scheduler tests.
type successHandler struct{}

// Handle reports successful deterministic processing.
func (successHandler) Handle(context.Context, Job) error { return nil }

// nonCooperativeHandler exposes entry but deliberately ignores cancellation until the test releases it.
type nonCooperativeHandler struct {
	entered chan struct{}
	release chan struct{}
}

// Handle models a dependency that does not honor its context while keeping test termination caller-controlled.
func (handler nonCooperativeHandler) Handle(context.Context, Job) error {
	close(handler.entered)
	<-handler.release
	return nil
}

// recordingRetention captures bounded deletion arguments.
type recordingRetention struct {
	called bool
	before int64
	limit  int
}

// DeleteTerminalBefore records the explicit retention policy.
func (store *recordingRetention) DeleteTerminalBefore(_ context.Context, before int64, limit int) (int, error) {
	store.called = true
	store.before = before
	store.limit = limit
	return 2, nil
}

// fixedDigest returns a syntactically valid digest for boundary fixtures.
func fixedDigest(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}

// fixtureJob creates one valid due queue record for contract tests.
func fixtureJob(now time.Time) Job {
	job, _ := SealJob(Job{JobID: "job-1", ProjectID: "project-1", RunID: "run-1", BackendEpoch: 1, BindingDigest: fixedDigest("binding"), Phase: PhaseExecute, State: JobQueued, InputDigest: fixedDigest("input"), CreatedAt: now, AvailableAt: now, Deadline: now.Add(time.Hour)})
	return job
}

// TestModelSealsAndDetectsTamper exercises binding, job and strict-document integrity.
func TestModelSealsAndDetectsTamper(t *testing.T) {
	now := time.Date(2026, 1, 2, 3, 4, 5, 0, time.FixedZone("x", 3600))
	epoch, err := SealBackendEpoch(BackendEpoch{RunID: "run-1", ProjectID: "project-1", Epoch: 1, Mode: BackendLocal, BackendIdentity: "local-1", SchemaVersion: 1, CreatedAt: now})
	if err != nil || epoch.CreatedAt.Location() != time.UTC {
		t.Fatalf("seal epoch: %#v %v", epoch, err)
	}
	head, _ := SealBindingHead(BindingHead{RunID: epoch.RunID, ProjectID: epoch.ProjectID, Epoch: 1, EpochDigest: epoch.Digest, Revision: 1})
	if VerifyBindingHead(head, epoch) != nil {
		t.Fatal("valid binding rejected")
	}
	epoch.ProjectID = "other"
	if !errors.Is(VerifyBackendEpoch(epoch), ErrIntegrity) {
		t.Fatal("tamper accepted")
	}
	var target Job
	if !errors.Is(StrictDecode([]byte(`{"job_id":"x","unknown":1}`), &target), ErrInvalid) || !errors.Is(StrictDecode([]byte(`null`), &target), ErrInvalid) {
		t.Fatal("strict decode accepted invalid input")
	}
}

// TestMemoryQueueFencesAndRecovers proves single live ownership and stale result rejection.
func TestMemoryQueueFencesAndRecovers(t *testing.T) {
	now := time.Date(2026, 2, 1, 0, 0, 0, 0, time.UTC)
	store := NewMemoryStore()
	job := fixtureJob(now)
	if _, err := store.Enqueue(context.Background(), job); err != nil {
		t.Fatal(err)
	}
	claimed, err := store.Claim(context.Background(), ClaimRequest{ProjectID: job.ProjectID, WorkerID: "worker-1", Now: now, Lease: time.Minute, Limit: 1})
	if err != nil || len(claimed) != 1 {
		t.Fatalf("claim %v %v", claimed, err)
	}
	second, _ := store.Claim(context.Background(), ClaimRequest{ProjectID: job.ProjectID, WorkerID: "worker-2", Now: now, Lease: time.Minute, Limit: 1})
	if len(second) != 0 {
		t.Fatal("double lease")
	}
	if _, err = store.Complete(context.Background(), job.JobID, "worker-2", claimed[0].Fence, claimed[0].CancelRevision, now, JobSucceeded, ""); !errors.Is(err, ErrConflict) {
		t.Fatal("stale owner accepted")
	}
	if count, err := store.Recover(context.Background(), now.Add(time.Minute), 1); err != nil || count != 1 {
		t.Fatalf("recover %d %v", count, err)
	}
	again, _ := store.Claim(context.Background(), ClaimRequest{ProjectID: job.ProjectID, WorkerID: "worker-2", Now: now.Add(time.Minute), Lease: time.Minute, Limit: 1})
	if len(again) != 1 || again[0].Fence <= claimed[0].Fence {
		t.Fatal("fence did not advance")
	}
}

// TestBackendCutoverCAS proves append-only epochs and exactly one winning head.
func TestBackendCutoverCAS(t *testing.T) {
	store := NewMemoryStore()
	now := time.Now().UTC()
	first, _ := SealBackendEpoch(BackendEpoch{RunID: "run-1", ProjectID: "project-1", Epoch: 1, Mode: BackendLocal, BackendIdentity: "local-1", SchemaVersion: 1, CreatedAt: now})
	head, err := store.ReserveInitial(context.Background(), first)
	if err != nil {
		t.Fatal(err)
	}
	next := BackendEpoch{RunID: "run-1", ProjectID: "project-1", Epoch: 2, Mode: BackendPostgres, BackendIdentity: "pg-1", SchemaVersion: 1, CreatedAt: now.Add(time.Second), PreviousDigest: first.Digest}
	newHead, err := store.Cutover(context.Background(), head, next)
	if err != nil || newHead.Epoch != 2 {
		t.Fatalf("cutover %#v %v", newHead, err)
	}
	if _, err = store.Cutover(context.Background(), head, next); !errors.Is(err, ErrConflict) {
		t.Fatal("stale head accepted")
	}
	loaded, epoch, err := store.Load(context.Background(), "run-1")
	if err != nil || loaded.Digest != newHead.Digest || epoch.Mode != BackendPostgres {
		t.Fatal("active backend mismatch")
	}
}

// recordingResolver observes whether authority rejection reaches secret resolution.
type recordingResolver struct{ called bool }

// Resolve records invocation while returning no secret values to persistence.
func (resolver *recordingResolver) Resolve(context.Context, []string) (map[string]string, error) {
	resolver.called = true
	return map[string]string{"secret-1": "value"}, nil
}

// fixedGrantStore returns one persisted composition-owned grant to authority derivation tests.
type fixedGrantStore struct{ grant AuthorityGrant }

// LoadAuthorityGrant returns the sealed fixture only for its exact run and phase.
func (store fixedGrantStore) LoadAuthorityGrant(_ context.Context, runID string, phase Phase) (AuthorityGrant, error) {
	if runID != store.grant.RunID || phase != store.grant.Phase {
		return AuthorityGrant{}, ErrNotFound
	}
	return store.grant, nil
}

// rejectingLiveVerifier models a stale durable fence and records the JIT boundary invocation.
type rejectingLiveVerifier struct{ called bool }

// VerifyLiveAuthority rejects before secret resolution to prove persisted revalidation is mandatory.
func (verifier *rejectingLiveVerifier) VerifyLiveAuthority(context.Context, AuthorityEnvelope, string, time.Time) error {
	verifier.called = true
	return ErrConflict
}

// acceptingLiveVerifier models a durable head/fence check that authorizes one JIT resolution.
type acceptingLiveVerifier struct{ called bool }

// VerifyLiveAuthority records the successful durable authority check.
func (verifier *acceptingLiveVerifier) VerifyLiveAuthority(context.Context, AuthorityEnvelope, string, time.Time) error {
	verifier.called = true
	return nil
}

// TestAuthorityMinimizesAndRejectsStaleFence checks phase restrictions and resolve-after-verify.
func TestAuthorityMinimizesAndRejectsStaleFence(t *testing.T) {
	now := time.Now().UTC()
	store := NewMemoryStore()
	job := fixtureJob(now)
	store.Enqueue(context.Background(), job)
	claimed, _ := store.Claim(context.Background(), ClaimRequest{ProjectID: job.ProjectID, WorkerID: "worker-1", Now: now, Lease: time.Minute, Limit: 1})
	leased := claimed[0]
	envelope, err := BuildAuthority(leased, AuthorityRequest{EnvelopeID: "env-1", WorkerID: "worker-1", ExpiresAt: now.Add(30 * time.Second), Capabilities: []string{"http.execute", "http.execute"}, TargetBindingRef: "target-1", TargetBindingDigest: fixedDigest("target"), SecretReferenceIDs: []string{"secret-1"}}, now)
	if err != nil || len(envelope.AllowedCapabilities) != 1 {
		t.Fatalf("authority %#v %v", envelope, err)
	}
	resolver := &recordingResolver{}
	tampered := leased
	tampered.Fence++
	tampered, _ = SealJob(tampered)
	if _, err = ResolveAuthorizedSecrets(context.Background(), envelope, tampered, "worker-1", now, resolver); !errors.Is(err, ErrUnauthorized) || resolver.called {
		t.Fatal("stale authority reached resolver")
	}
	if _, err = BuildAuthority(leased, AuthorityRequest{EnvelopeID: "env-2", WorkerID: "worker-1", ExpiresAt: now.Add(time.Second), SecretReferenceIDs: []string{"secret-1"}}, now); !errors.Is(err, ErrUnauthorized) {
		t.Fatal("secret authority without a target was accepted")
	}
}

// TestAuthorityResolvesSecretsOnlyAfterBothLiveChecks covers the successful ephemeral resolution boundaries and nil dependencies.
func TestAuthorityResolvesSecretsOnlyAfterBothLiveChecks(t *testing.T) {
	now := time.Now().UTC()
	store := NewMemoryStore()
	job := fixtureJob(now)
	job.Phase = PhaseExecute
	job.Digest = ""
	if _, err := store.Enqueue(context.Background(), job); err != nil {
		t.Fatal(err)
	}
	claimed, err := store.Claim(context.Background(), ClaimRequest{ProjectID: job.ProjectID, WorkerID: "worker-1", Now: now, Lease: time.Minute, Limit: 1})
	if err != nil || len(claimed) != 1 {
		t.Fatal("claim", err)
	}
	leased := claimed[0]
	grant, err := SealAuthorityGrant(AuthorityGrant{ProjectID: leased.ProjectID, RunID: leased.RunID, BackendEpoch: leased.BackendEpoch, BindingDigest: leased.BindingDigest, Phase: leased.Phase, AllowedCapabilities: []string{"http.execute"}, TargetBindingRef: "target-1", TargetBindingDigest: fixedDigest("target"), SecretReferenceIDs: []string{"secret-1"}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = DeriveAuthority(context.Background(), nil, leased, "envelope-nil", leased.Owner, now.Add(30*time.Second), now); !errors.Is(err, ErrUnauthorized) {
		t.Fatal("nil grant store accepted", err)
	}
	envelope, err := DeriveAuthority(context.Background(), fixedGrantStore{grant: grant}, leased, "envelope-success", leased.Owner, now.Add(30*time.Second), now)
	if err != nil {
		t.Fatal(err)
	}
	resolver := &recordingResolver{}
	secrets, err := ResolveAuthorizedSecrets(context.Background(), envelope, leased, leased.Owner, now, resolver)
	if err != nil || !resolver.called || secrets["secret-1"] != "value" {
		t.Fatal("live job resolution", err)
	}
	resolver = &recordingResolver{}
	verifier := &acceptingLiveVerifier{}
	secrets, err = ResolvePersistedSecrets(context.Background(), verifier, envelope, leased.Owner, now, resolver)
	if err != nil || !verifier.called || !resolver.called || secrets["secret-1"] != "value" {
		t.Fatal("persisted resolution", err)
	}
	if _, err = ResolveAuthorizedSecrets(context.Background(), envelope, leased, leased.Owner, now, nil); !errors.Is(err, ErrInvalid) {
		t.Fatal("nil resolver accepted", err)
	}
	withoutSecrets := envelope
	withoutSecrets.SecretReferenceIDs = nil
	if _, err = ResolvePersistedSecrets(context.Background(), verifier, withoutSecrets, leased.Owner, now, resolver); !errors.Is(err, ErrUnauthorized) {
		t.Fatal("empty secret authority accepted", err)
	}
}

// TestDerivedAuthorityUsesPersistedClosedMatrix proves workers cannot supply capabilities and stale durable state blocks JIT secrets.
func TestDerivedAuthorityUsesPersistedClosedMatrix(t *testing.T) {
	now := time.Now().UTC()
	store := NewMemoryStore()
	job := fixtureJob(now)
	store.Enqueue(context.Background(), job)
	claimed, _ := store.Claim(context.Background(), ClaimRequest{ProjectID: job.ProjectID, WorkerID: "worker-1", Now: now, Lease: time.Minute, Limit: 1})
	leased := claimed[0]
	grant, err := SealAuthorityGrant(AuthorityGrant{ProjectID: leased.ProjectID, RunID: leased.RunID, BackendEpoch: leased.BackendEpoch, BindingDigest: leased.BindingDigest, Phase: PhaseExecute, InputReferences: []string{"input-1"}, AllowedCapabilities: []string{"http.execute"}, TargetBindingRef: "target-1", TargetBindingDigest: fixedDigest("target"), SecretReferenceIDs: []string{"secret-1"}})
	if err != nil {
		t.Fatal(err)
	}
	envelope, err := DeriveAuthority(context.Background(), fixedGrantStore{grant: grant}, leased, "envelope-1", leased.Owner, now.Add(30*time.Second), now)
	if err != nil || len(envelope.AllowedCapabilities) != 1 || envelope.AllowedCapabilities[0] != "http.execute" {
		t.Fatal("persisted grant was not derived exactly")
	}
	if _, err = SealAuthorityGrant(AuthorityGrant{ProjectID: leased.ProjectID, RunID: leased.RunID, Phase: PhasePlan, AllowedCapabilities: []string{"http.execute"}}); !errors.Is(err, ErrInvalid) {
		t.Fatal("cross-phase capability was accepted")
	}
	verifier := &rejectingLiveVerifier{}
	resolver := &recordingResolver{}
	if _, err = ResolvePersistedSecrets(context.Background(), verifier, envelope, leased.Owner, now, resolver); !errors.Is(err, ErrConflict) || !verifier.called || resolver.called {
		t.Fatal("stale durable authority reached secret resolution")
	}
}

// TestInboxAndEffectsEnforceStableIdentity checks redelivery and intent-before-confirm.
func TestInboxAndEffectsEnforceStableIdentity(t *testing.T) {
	store := NewMemoryStore()
	now := time.Now().UTC()
	job := fixtureJob(now)
	in := InboxEnvelope{Source: "api", ExternalID: "request-1", IdempotencyKey: "key-1", ProjectID: job.ProjectID, RequestDigest: fixedDigest("request"), CommandKind: "validate"}
	created, err := store.Accept(context.Background(), in, job)
	if err != nil || !created {
		t.Fatal("first ingress failed")
	}
	created, err = store.Accept(context.Background(), in, job)
	if err != nil || created {
		t.Fatal("redelivery not idempotent")
	}
	in.RequestDigest = fixedDigest("different")
	if _, err = store.Accept(context.Background(), in, job); !errors.Is(err, ErrConflict) {
		t.Fatal("digest conflict accepted")
	}
	intent := EffectIntent{OperationID: "op-1", IdempotencyKey: "effect-1", ProjectID: job.ProjectID, RunID: job.RunID, Phase: PhasePublish, BindingDigest: job.BindingDigest, InputDigest: job.InputDigest, ReconcileMethod: "poll"}
	if _, err = store.Prepare(context.Background(), intent); err != nil {
		t.Fatal(err)
	}
	if err = store.Confirm(context.Background(), intent.OperationID, fixedDigest("receipt")); err != nil {
		t.Fatal(err)
	}
	if err = store.Confirm(context.Background(), intent.OperationID, fixedDigest("other")); !errors.Is(err, ErrConflict) {
		t.Fatal("conflicting receipt accepted")
	}
}

// TestMemoryAdmissionPublishesAndReloadsOneLocalAuthority exercises the fake's atomic ingress contract and detached reconstruction.
func TestMemoryAdmissionPublishesAndReloadsOneLocalAuthority(t *testing.T) {
	ctx := context.Background()
	now := time.Now().UTC()
	store := NewMemoryStore()
	epoch, err := SealBackendEpoch(BackendEpoch{RunID: "admission-run", ProjectID: "project-1", Epoch: 1, Mode: BackendLocal, BackendIdentity: "local-admission", SchemaVersion: 1, CreatedAt: now})
	if err != nil {
		t.Fatal(err)
	}
	job := fixtureJob(now)
	job.JobID = "admission-job"
	job.RunID = epoch.RunID
	job.BackendEpoch = epoch.Epoch
	job.BindingDigest = epoch.Digest
	job.Digest = ""
	envelope := InboxEnvelope{Source: "api", ExternalID: "admission-request", IdempotencyKey: "admission-key", ProjectID: epoch.ProjectID, RequestDigest: fixedDigest("admission-request"), CommandKind: "validate", CommandFields: map[string]string{"profile": "smoke"}}
	admission := IngressAdmission{Envelope: envelope, Epoch: epoch, Job: job}
	created, err := store.Admit(ctx, admission)
	if err != nil || !created {
		t.Fatal("admission", err)
	}
	created, err = store.Admit(ctx, admission)
	if err != nil || created {
		t.Fatal("admission redelivery", err)
	}
	loaded, err := store.LoadIngress(ctx, epoch.RunID)
	if err != nil || loaded.Envelope.CommandFields["profile"] != "smoke" {
		t.Fatal("load ingress", err)
	}
	loaded.Envelope.CommandFields["profile"] = "mutated"
	reloaded, err := store.LoadIngress(ctx, epoch.RunID)
	if err != nil || reloaded.Envelope.CommandFields["profile"] != "smoke" {
		t.Fatal("ingress alias escaped", err)
	}
	head, active, err := store.Load(ctx, epoch.RunID)
	if err != nil || head.EpochDigest != epoch.Digest || active.Digest != epoch.Digest {
		t.Fatal("atomic authority missing", err)
	}
	claimed, err := store.Claim(ctx, ClaimRequest{ProjectID: epoch.ProjectID, WorkerID: "admission-worker", Now: now, Lease: time.Minute, Limit: 1})
	if err != nil || len(claimed) != 1 || claimed[0].RunID != epoch.RunID {
		t.Fatal("atomic job missing", err)
	}
	if _, err = store.LoadIngress(ctx, "missing-run"); !errors.Is(err, ErrNotFound) {
		t.Fatal("missing ingress accepted", err)
	}
	conflict := admission
	conflict.Envelope.RequestDigest = fixedDigest("other-request")
	if _, err = store.Admit(ctx, conflict); !errors.Is(err, ErrConflict) {
		t.Fatal("conflicting redelivery accepted", err)
	}
}

// TestMemoryAdmissionRejectsInvalidAndCompetingAuthority covers fail-closed atomic admission boundaries without a database.
func TestMemoryAdmissionRejectsInvalidAndCompetingAuthority(t *testing.T) {
	ctx := context.Background()
	now := time.Now().UTC()
	store := NewMemoryStore()
	epoch, err := SealBackendEpoch(BackendEpoch{RunID: "competing-run", ProjectID: "project-1", Epoch: 1, Mode: BackendLocal, BackendIdentity: "local-competing", SchemaVersion: 1, CreatedAt: now})
	if err != nil {
		t.Fatal(err)
	}
	job := fixtureJob(now)
	job.JobID, job.RunID, job.BackendEpoch, job.BindingDigest, job.Digest = "competing-job", epoch.RunID, epoch.Epoch, epoch.Digest, ""
	envelope := InboxEnvelope{Source: "api", ExternalID: "competing-request", IdempotencyKey: "competing-key", ProjectID: epoch.ProjectID, RequestDigest: fixedDigest("competing-request"), CommandKind: "validate"}
	admission := IngressAdmission{Envelope: envelope, Epoch: epoch, Job: job}
	invalidEpoch := admission
	invalidEpoch.Epoch.Mode, invalidEpoch.Epoch.Digest = BackendPostgres, ""
	if _, err = store.Admit(ctx, invalidEpoch); !errors.Is(err, ErrInvalid) {
		t.Fatal("non-local initial epoch accepted", err)
	}
	invalidJob := admission
	invalidJob.Job.RunID = "different-run"
	if _, err = store.Admit(ctx, invalidJob); !errors.Is(err, ErrInvalid) {
		t.Fatal("cross-run job accepted", err)
	}
	invalidEnvelope := admission
	invalidEnvelope.Envelope.ProjectID = "project-2"
	if _, err = store.Admit(ctx, invalidEnvelope); !errors.Is(err, ErrInvalid) {
		t.Fatal("cross-project envelope accepted", err)
	}
	if _, err = store.ReserveInitial(ctx, epoch); err != nil {
		t.Fatal(err)
	}
	if _, err = store.Admit(ctx, admission); !errors.Is(err, ErrConflict) {
		t.Fatal("competing authority accepted", err)
	}
	store.heads[epoch.RunID] = BindingHead{RunID: epoch.RunID}
	if _, _, err = store.Load(ctx, epoch.RunID); !errors.Is(err, ErrIntegrity) {
		t.Fatal("corrupt head accepted", err)
	}
}

// TestCursorIsProjectScoped validates signed keyset continuation boundaries.
func TestCursorIsProjectScoped(t *testing.T) {
	now := time.Now().UTC()
	codec, err := NewCursorCodec([]byte("0123456789abcdef0123456789abcdef"))
	if err != nil {
		t.Fatal(err)
	}
	token, err := codec.Seal(PageCursor{ProjectID: "project-1", Filter: "jobs", LastKey: "job-9", SnapshotUnixNano: now.UnixNano(), LastUnixNano: now.UnixNano(), ExpiresAtUnixNano: now.Add(time.Hour).UnixNano()})
	if err != nil {
		t.Fatal(err)
	}
	cursor, err := codec.Open(token, "project-1", "jobs", now)
	if err != nil || cursor.LastKey != "job-9" {
		t.Fatal("cursor roundtrip")
	}
	if _, err = codec.Open(token, "project-2", "jobs", now); !errors.Is(err, ErrUnauthorized) {
		t.Fatal("cross-project cursor accepted")
	}
	if _, err = codec.Open(token, "project-1", "other", now); !errors.Is(err, ErrUnauthorized) {
		t.Fatal("cross-filter cursor accepted")
	}
	tamperedBytes := []byte(token)
	if tamperedBytes[10] == 'A' {
		tamperedBytes[10] = 'B'
	} else {
		tamperedBytes[10] = 'A'
	}
	tampered := string(tamperedBytes)
	if _, err = codec.Open(tampered, "project-1", "jobs", now); !errors.Is(err, ErrIntegrity) {
		t.Fatal("tampered cursor accepted")
	}
	if _, err = codec.Open(token, "project-1", "jobs", now.Add(2*time.Hour)); !errors.Is(err, ErrDeadline) {
		t.Fatal("expired cursor accepted")
	}
	if (RetentionPolicy{}).Validate() == nil {
		t.Fatal("implicit retention accepted")
	}
}

// TestSchedulerPersistsRetry verifies failures become durable before the lease is released.
func TestSchedulerPersistsRetry(t *testing.T) {
	now := time.Now().UTC()
	store := NewMemoryStore()
	job := fixtureJob(now)
	store.Enqueue(context.Background(), job)
	scheduler, err := NewScheduler(store, fixedClock{now: now}, noopWaiter{}, failingHandler{err: errors.New("transient")}, fixedClassifier{decision: RetryDecision{Category: "transient", Backoff: time.Minute, MaxAttempts: 3}}, SchedulerConfig{ProjectID: job.ProjectID, WorkerID: "worker-1", Lease: time.Minute, BatchSize: 1, IdleWait: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	count, err := scheduler.RunOnce(context.Background())
	if err != nil || count != 1 {
		t.Fatalf("run once %d %v", count, err)
	}
	claimed, _ := store.Claim(context.Background(), ClaimRequest{ProjectID: job.ProjectID, WorkerID: "worker-2", Now: now, Lease: time.Minute, Limit: 1})
	if len(claimed) != 0 {
		t.Fatal("retry was available before persisted backoff")
	}
	claimed, _ = store.Claim(context.Background(), ClaimRequest{ProjectID: job.ProjectID, WorkerID: "worker-2", Now: now.Add(time.Minute), Lease: time.Minute, Limit: 1})
	if len(claimed) != 1 || claimed[0].Fence != 2 {
		t.Fatal("durable retry missing")
	}
}

// TestImportManifestDetachesMaps verifies offline snapshot identity cannot mutate by alias.
func TestImportManifestDetachesMaps(t *testing.T) {
	counts := map[string]uint64{"runs": 1}
	digests := map[string]string{"runs": fixedDigest("runs")}
	value, err := SealImportManifest(ImportManifest{ImportID: "import-1", RunID: "run-1", ProjectID: "project-1", SourceIdentity: "local-1", ExpectedHeadDigest: fixedDigest("head"), Counts: counts, StreamDigests: digests, State: ImportFrozen, CreatedAt: time.Now().UTC()})
	if err != nil {
		t.Fatal(err)
	}
	counts["runs"] = 99
	digests["runs"] = fixedDigest("tamper")
	if value.Counts["runs"] != 1 || value.StreamDigests["runs"] == digests["runs"] {
		t.Fatal("manifest retained caller aliases")
	}
}

// TestMemoryQueueLifecycle covers heartbeat, completion, cancellation and exhaustion boundaries.
func TestMemoryQueueLifecycle(t *testing.T) {
	ctx := context.Background()
	now := time.Now().UTC()
	store := NewMemoryStore()
	first := fixtureJob(now)
	store.Enqueue(ctx, first)
	claimed, _ := store.Claim(ctx, ClaimRequest{ProjectID: first.ProjectID, WorkerID: "worker-1", Now: now, Lease: time.Minute, Limit: 1})
	leased := claimed[0]
	if _, err := store.Heartbeat(ctx, leased.JobID, leased.Owner, leased.Fence, leased.CancelRevision, now, time.Minute); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Complete(ctx, leased.JobID, leased.Owner, leased.Fence, leased.CancelRevision, now, JobSucceeded, ""); err != nil {
		t.Fatal(err)
	}
	if !IsTerminal(JobSucceeded) || IsTerminal(JobLeased) {
		t.Fatal("terminal matrix")
	}
	second := fixtureJob(now)
	second.JobID = "job-2"
	second.Digest = ""
	second, _ = SealJob(second)
	store.Enqueue(ctx, second)
	cancelled, err := store.Cancel(ctx, second.JobID, 1)
	if err != nil || cancelled.State != JobCancelled {
		t.Fatal("cancel")
	}
	same, err := store.Cancel(ctx, second.JobID, 1)
	if err != nil || same.CancelRevision != 1 {
		t.Fatal("idempotent cancel")
	}
	third := fixtureJob(now)
	third.JobID = "job-3"
	third.Digest = ""
	third, _ = SealJob(third)
	store.Enqueue(ctx, third)
	claimed, _ = store.Claim(ctx, ClaimRequest{ProjectID: third.ProjectID, WorkerID: "worker-1", Now: now, Lease: time.Minute, Limit: 1})
	dead, err := store.Retry(ctx, third.JobID, "worker-1", claimed[0].Fence, claimed[0].CancelRevision, now, RetryDecision{Category: "permanent", MaxAttempts: 1}, now)
	if err != nil || dead.State != JobDead {
		t.Fatal("retry exhaustion")
	}
}

// TestMemoryQueueRejectsCancelledExpiredAndMisorderedOwners exercises the same adversarial ownership contract as PostgreSQL.
func TestMemoryQueueRejectsCancelledExpiredAndMisorderedOwners(t *testing.T) {
	ctx := context.Background()
	now := time.Now().UTC()
	store := NewMemoryStore()
	available := now.Add(2 * time.Second)
	late := fixtureJob(now)
	late.JobID, late.CreatedAt, late.AvailableAt, late.Digest = "job-a", now.Add(time.Second), available, ""
	late, _ = SealJob(late)
	early := fixtureJob(now)
	early.JobID, early.CreatedAt, early.AvailableAt, early.Digest = "job-z", now, available, ""
	early, _ = SealJob(early)
	store.Enqueue(ctx, late)
	store.Enqueue(ctx, early)
	claimed, _ := store.Claim(ctx, ClaimRequest{ProjectID: early.ProjectID, WorkerID: "worker-1", Now: available, Lease: time.Minute, Limit: 2})
	if len(claimed) != 2 || claimed[0].JobID != early.JobID {
		t.Fatal("queue ignored created_at ordering")
	}
	leased := claimed[0]
	if _, err := store.Cancel(ctx, leased.JobID, 1); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Complete(ctx, leased.JobID, leased.Owner, leased.Fence, leased.CancelRevision, now.Add(time.Second), JobSucceeded, ""); !errors.Is(err, ErrConflict) {
		t.Fatal("cancelled owner confirmed a terminal")
	}
	expired := claimed[1]
	if _, err := store.Complete(ctx, expired.JobID, expired.Owner, expired.Fence, expired.CancelRevision, expired.LeaseUntil, JobSucceeded, ""); !errors.Is(err, ErrConflict) {
		t.Fatal("expired owner confirmed a terminal")
	}
}

// TestSelectionRetentionAndSchedulerSuccess covers explicit backend and policy composition.
func TestSelectionRetentionAndSchedulerSuccess(t *testing.T) {
	store := NewMemoryStore()
	set, err := SelectBackend(BackendSet{Mode: BackendLocal, Identity: "local-1", Bindings: store, Queue: store, Ingress: store, Effects: store})
	if err != nil || set.Mode != BackendLocal {
		t.Fatal("backend selection")
	}
	if _, err = SelectBackend(BackendSet{}); !errors.Is(err, ErrInvalid) {
		t.Fatal("invalid backend selected")
	}
	retention := &recordingRetention{}
	if count, err := RunRetention(context.Background(), retention, nil); err != nil || count != 0 || retention.called {
		t.Fatal("implicit retention ran")
	}
	policy := &RetentionPolicy{BeforeUnix: 100, BatchSize: 10}
	if count, err := RunRetention(context.Background(), retention, policy); err != nil || count != 2 || !retention.called {
		t.Fatal("explicit retention failed")
	}
	now := time.Now().UTC()
	job := fixtureJob(now)
	store.Enqueue(context.Background(), job)
	scheduler, _ := NewScheduler(store, fixedClock{now}, noopWaiter{}, successHandler{}, fixedClassifier{}, SchedulerConfig{ProjectID: job.ProjectID, WorkerID: "worker-1", Lease: time.Minute, BatchSize: 1})
	if count, err := scheduler.RunOnce(context.Background()); err != nil || count != 1 {
		t.Fatal("scheduler success")
	}
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	if err := (TimerWaiter{}).Wait(cancelled, time.Hour); !errors.Is(err, context.Canceled) {
		t.Fatal("wait cancellation")
	}
}

// TestClosedErrorsAndImportTransitions covers the complete redacted taxonomy and offline matrix.
func TestClosedErrorsAndImportTransitions(t *testing.T) {
	var nilError *Error
	if nilError.Error() == "" || nilError.Unwrap() != nil {
		t.Fatal("nil error contract")
	}
	categories := []struct {
		category ErrorCategory
		target   error
	}{{CategoryInvalid, ErrInvalid}, {CategoryNotFound, ErrNotFound}, {CategoryConflict, ErrConflict}, {CategoryUnauthorized, ErrUnauthorized}, {CategoryIntegrity, ErrIntegrity}, {CategoryCancelled, ErrCancelled}, {CategoryDeadline, ErrDeadline}, {CategoryTransient, ErrTransient}, {CategoryUnavailable, ErrUnavailable}, {CategoryExhausted, ErrExhausted}, {CategoryInternal, ErrInternal}}
	for _, item := range categories {
		value := &Error{Category: item.category, Operation: "test"}
		if value.Error() == "" || !errors.Is(value, item.target) {
			t.Fatalf("category %s", item.category)
		}
	}
	manifest, _ := SealImportManifest(ImportManifest{ImportID: "import-1", RunID: "run-1", ProjectID: "project-1", SourceIdentity: "local-1", ExpectedHeadDigest: fixedDigest("head"), State: ImportFrozen, CreatedAt: time.Now().UTC()})
	candidate, err := AdvanceImport(manifest, ImportCandidate)
	if err != nil {
		t.Fatal(err)
	}
	verified, err := AdvanceImport(candidate, ImportVerified)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = AdvanceImport(verified, ImportCutover); err != nil {
		t.Fatal(err)
	}
	if _, err = AdvanceImport(manifest, ImportCutover); !errors.Is(err, ErrConflict) {
		t.Fatal("invalid import transition")
	}
}

// TestMemoryStoreRejectsConflicts exercises fail-closed fake branches used by contract suites.
func TestMemoryStoreRejectsConflicts(t *testing.T) {
	ctx := context.Background()
	now := time.Now().UTC()
	store := NewMemoryStore()
	job := fixtureJob(now)
	stored, _ := store.Enqueue(ctx, job)
	if duplicate, err := store.Enqueue(ctx, job); err != nil || duplicate.Digest != stored.Digest {
		t.Fatal("enqueue idempotency")
	}
	conflict := job
	conflict.Deadline = conflict.Deadline.Add(time.Minute)
	conflict.Digest = ""
	conflict, _ = SealJob(conflict)
	if _, err := store.Enqueue(ctx, conflict); !errors.Is(err, ErrConflict) {
		t.Fatal("enqueue conflict")
	}
	if _, err := store.Heartbeat(ctx, "missing", "worker", 1, 0, now, time.Minute); !errors.Is(err, ErrNotFound) {
		t.Fatal("missing heartbeat")
	}
	if _, err := store.Complete(ctx, job.JobID, "worker", 1, 0, now, JobQueued, ""); !errors.Is(err, ErrInvalid) {
		t.Fatal("invalid terminal")
	}
	if _, err := store.Retry(ctx, job.JobID, "worker", 1, 0, now, RetryDecision{}, now); !errors.Is(err, ErrInvalid) {
		t.Fatal("invalid retry")
	}
	if _, err := store.Cancel(ctx, "missing", 1); !errors.Is(err, ErrNotFound) {
		t.Fatal("missing cancel")
	}
	if _, err := store.Recover(ctx, now, 0); !errors.Is(err, ErrInvalid) {
		t.Fatal("invalid recovery")
	}
	if _, _, err := store.Load(ctx, "missing"); !errors.Is(err, ErrNotFound) {
		t.Fatal("missing binding")
	}
	epoch, _ := SealBackendEpoch(BackendEpoch{RunID: "run-1", ProjectID: "project-1", Epoch: 1, Mode: BackendLocal, BackendIdentity: "local-1", SchemaVersion: 1, CreatedAt: now})
	if _, err := store.ReserveInitial(ctx, epoch); err != nil {
		t.Fatal(err)
	}
	if _, err := store.ReserveInitial(ctx, epoch); err != nil {
		t.Fatal("idempotent reserve")
	}
	other := epoch
	other.BackendIdentity = "local-2"
	other.Digest = ""
	if _, err := store.ReserveInitial(ctx, other); !errors.Is(err, ErrConflict) {
		t.Fatal("binding conflict")
	}
	intent := EffectIntent{OperationID: "op-1", IdempotencyKey: "key-1", ProjectID: "project-1", RunID: "run-1", Phase: PhasePublish, BindingDigest: fixedDigest("binding"), InputDigest: fixedDigest("input")}
	if _, err := store.Prepare(ctx, intent); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Prepare(ctx, intent); err != nil {
		t.Fatal("intent idempotency")
	}
	otherIntent := intent
	otherIntent.InputDigest = fixedDigest("other")
	if _, err := store.Prepare(ctx, otherIntent); !errors.Is(err, ErrConflict) {
		t.Fatal("intent conflict")
	}
	if err := store.Confirm(ctx, "missing", fixedDigest("receipt")); !errors.Is(err, ErrNotFound) {
		t.Fatal("missing confirmation")
	}
}

// TestMemoryStoreFailClosedBoundaryMatrix exercises invalid, absent, expired and stale branches shared with durable adapters.
func TestMemoryStoreFailClosedBoundaryMatrix(t *testing.T) {
	ctx := context.Background()
	now := time.Now().UTC()
	store := NewMemoryStore()
	if _, err := store.Enqueue(ctx, Job{}); !errors.Is(err, ErrInvalid) {
		t.Fatal("invalid enqueue accepted", err)
	}
	if _, err := store.Claim(ctx, ClaimRequest{ProjectID: "project-1", WorkerID: "worker-1", Now: now, Lease: 0, Limit: 1}); !errors.Is(err, ErrInvalid) {
		t.Fatal("invalid claim accepted", err)
	}
	if _, err := store.Complete(ctx, "missing", "worker-1", 1, 0, now, JobSucceeded, ""); !errors.Is(err, ErrNotFound) {
		t.Fatal("missing completion accepted", err)
	}
	if _, err := store.Retry(ctx, "missing", "worker-1", 1, 0, now, RetryDecision{Category: "transient", MaxAttempts: 1}, now); !errors.Is(err, ErrNotFound) {
		t.Fatal("missing retry accepted", err)
	}
	if _, err := store.ReserveInitial(ctx, BackendEpoch{}); !errors.Is(err, ErrInvalid) {
		t.Fatal("invalid epoch accepted", err)
	}
	if _, err := store.Cutover(ctx, BindingHead{RunID: "missing-run"}, BackendEpoch{}); !errors.Is(err, ErrInvalid) {
		t.Fatal("invalid cutover epoch accepted", err)
	}
	next, err := SealBackendEpoch(BackendEpoch{RunID: "missing-run", ProjectID: "project-1", Epoch: 2, Mode: BackendPostgres, BackendIdentity: "postgres-one", SchemaVersion: 1, CreatedAt: now, PreviousDigest: fixedDigest("previous")})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = store.Cutover(ctx, BindingHead{RunID: next.RunID}, next); !errors.Is(err, ErrNotFound) {
		t.Fatal("missing cutover head accepted", err)
	}
	job := fixtureJob(now)
	if _, err = store.Accept(ctx, InboxEnvelope{}, job); !errors.Is(err, ErrInvalid) {
		t.Fatal("invalid inbox accepted", err)
	}
	envelope := InboxEnvelope{Source: "api", ExternalID: "boundary-request", IdempotencyKey: "boundary-key", ProjectID: job.ProjectID, RequestDigest: fixedDigest("boundary"), CommandKind: "validate"}
	if _, err = store.Accept(ctx, envelope, Job{}); !errors.Is(err, ErrInvalid) {
		t.Fatal("invalid inbox job accepted", err)
	}
	if _, err = store.Enqueue(ctx, job); err != nil {
		t.Fatal(err)
	}
	if _, err = store.Accept(ctx, envelope, job); !errors.Is(err, ErrConflict) {
		t.Fatal("existing job accepted through inbox", err)
	}
	if _, err = store.Prepare(ctx, EffectIntent{}); !errors.Is(err, ErrInvalid) {
		t.Fatal("invalid effect accepted", err)
	}
	if err = store.Confirm(ctx, "missing", "bad-digest"); !errors.Is(err, ErrInvalid) {
		t.Fatal("invalid receipt accepted", err)
	}
	claimed, err := store.Claim(ctx, ClaimRequest{ProjectID: job.ProjectID, WorkerID: "worker-1", Now: now, Lease: time.Second, Limit: 1})
	if err != nil || len(claimed) != 1 {
		t.Fatal("claim", err)
	}
	leased := claimed[0]
	corrupt := leased
	corrupt.Deadline = now
	corrupt.LeaseUntil = now.Add(time.Second)
	store.jobs[corrupt.JobID] = corrupt
	if _, err = store.Heartbeat(ctx, corrupt.JobID, corrupt.Owner, corrupt.Fence, corrupt.CancelRevision, now, time.Second); !errors.Is(err, ErrDeadline) {
		t.Fatal("deadline-bounded heartbeat accepted", err)
	}
	store.jobs[leased.JobID] = leased
	if _, err = store.Heartbeat(ctx, leased.JobID, leased.Owner, leased.Fence, leased.CancelRevision, leased.Deadline, time.Second); !errors.Is(err, ErrConflict) {
		t.Fatal("expired heartbeat accepted", err)
	}
	if _, err = store.Complete(ctx, leased.JobID, "other-worker", leased.Fence, leased.CancelRevision, now, JobSucceeded, ""); !errors.Is(err, ErrConflict) {
		t.Fatal("stale completion accepted", err)
	}
	if _, err = store.Retry(ctx, leased.JobID, "other-worker", leased.Fence, leased.CancelRevision, now, RetryDecision{Category: "transient", MaxAttempts: 2}, now.Add(time.Second)); !errors.Is(err, ErrConflict) {
		t.Fatal("stale retry accepted", err)
	}
	if _, err = store.Cancel(ctx, leased.JobID, 2); err != nil {
		t.Fatal(err)
	}
	if _, err = store.Cancel(ctx, leased.JobID, 1); !errors.Is(err, ErrConflict) {
		t.Fatal("stale cancellation accepted", err)
	}
}

// TestMemoryStorePropagatesCancellationAcrossContracts proves every fake port
// honors caller cancellation before observing or mutating durable state.
func TestMemoryStorePropagatesCancellationAcrossContracts(t *testing.T) {
	store := NewMemoryStore()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	now := time.Now().UTC()
	job := fixtureJob(now)
	if _, err := store.Enqueue(ctx, job); !errors.Is(err, context.Canceled) {
		t.Fatal("enqueue cancellation", err)
	}
	if _, err := store.Claim(ctx, ClaimRequest{}); !errors.Is(err, context.Canceled) {
		t.Fatal("claim cancellation", err)
	}
	if _, err := store.Heartbeat(ctx, job.JobID, "worker", 1, 0, now, time.Minute); !errors.Is(err, context.Canceled) {
		t.Fatal("heartbeat cancellation", err)
	}
	if _, err := store.Complete(ctx, job.JobID, "worker", 1, 0, now, JobSucceeded, ""); !errors.Is(err, context.Canceled) {
		t.Fatal("complete cancellation", err)
	}
	if _, err := store.Retry(ctx, job.JobID, "worker", 1, 0, now, RetryDecision{}, now); !errors.Is(err, context.Canceled) {
		t.Fatal("retry cancellation", err)
	}
	if _, err := store.Cancel(ctx, job.JobID, 1); !errors.Is(err, context.Canceled) {
		t.Fatal("cancel cancellation", err)
	}
	if _, err := store.Recover(ctx, now, 1); !errors.Is(err, context.Canceled) {
		t.Fatal("recover cancellation", err)
	}
	if _, err := store.ReserveInitial(ctx, BackendEpoch{}); !errors.Is(err, context.Canceled) {
		t.Fatal("reserve cancellation", err)
	}
	if _, _, err := store.Load(ctx, "run"); !errors.Is(err, context.Canceled) {
		t.Fatal("load cancellation", err)
	}
	if _, err := store.Cutover(ctx, BindingHead{}, BackendEpoch{}); !errors.Is(err, context.Canceled) {
		t.Fatal("cutover cancellation", err)
	}
	if _, err := store.Accept(ctx, InboxEnvelope{}, Job{}); !errors.Is(err, context.Canceled) {
		t.Fatal("accept cancellation", err)
	}
	if _, err := store.Admit(ctx, IngressAdmission{}); !errors.Is(err, context.Canceled) {
		t.Fatal("admit cancellation", err)
	}
	if _, err := store.LoadIngress(ctx, "run"); !errors.Is(err, context.Canceled) {
		t.Fatal("ingress cancellation", err)
	}
	if _, err := store.Prepare(ctx, EffectIntent{}); !errors.Is(err, context.Canceled) {
		t.Fatal("prepare cancellation", err)
	}
	if err := store.Confirm(ctx, "operation", fixedDigest("receipt")); !errors.Is(err, context.Canceled) {
		t.Fatal("confirm cancellation", err)
	}
}

// heartbeatQueue observes scheduler lease renewal while reusing the contractual in-memory queue.
type heartbeatQueue struct {
	*MemoryStore
	beats chan struct{}
	fail  bool
}

// Heartbeat records one scheduler renewal and optionally simulates a lost fence.
func (queue *heartbeatQueue) Heartbeat(ctx context.Context, id, owner string, fence, cancelRevision uint64, now time.Time, lease time.Duration) (Job, error) {
	queue.beats <- struct{}{}
	if queue.fail {
		return Job{}, ErrConflict
	}
	return queue.MemoryStore.Heartbeat(ctx, id, owner, fence, cancelRevision, now, lease)
}

// channelHandler blocks product work on test-owned channels so heartbeat interleavings need no sleeps.
type channelHandler struct {
	entered chan struct{}
	release chan struct{}
}

// schedulerFaultQueue injects one selected durable queue failure while retaining the remaining memory contract.
type schedulerFaultQueue struct {
	*MemoryStore
	failOperation string
}

// Recover injects the scheduler's pre-claim recovery failure boundary.
func (queue schedulerFaultQueue) Recover(ctx context.Context, now time.Time, limit int) (int, error) {
	if queue.failOperation == "recover" {
		return 0, ErrUnavailable
	}
	return queue.MemoryStore.Recover(ctx, now, limit)
}

// Claim injects the scheduler's bounded claim failure boundary.
func (queue schedulerFaultQueue) Claim(ctx context.Context, request ClaimRequest) ([]Job, error) {
	if queue.failOperation == "claim" {
		return nil, ErrUnavailable
	}
	return queue.MemoryStore.Claim(ctx, request)
}

// Complete injects the scheduler's success persistence failure boundary.
func (queue schedulerFaultQueue) Complete(ctx context.Context, id, owner string, fence, cancelRevision uint64, now time.Time, state JobState, category string) (Job, error) {
	if queue.failOperation == "complete" {
		return Job{}, ErrUnavailable
	}
	return queue.MemoryStore.Complete(ctx, id, owner, fence, cancelRevision, now, state, category)
}

// Retry injects the scheduler's classified outcome persistence failure boundary.
func (queue schedulerFaultQueue) Retry(ctx context.Context, id, owner string, fence, cancelRevision uint64, now time.Time, decision RetryDecision, available time.Time) (Job, error) {
	if queue.failOperation == "retry" {
		return Job{}, ErrUnavailable
	}
	return queue.MemoryStore.Retry(ctx, id, owner, fence, cancelRevision, now, decision, available)
}

// Handle announces ownership then exits on explicit release or caller cancellation.
func (handler channelHandler) Handle(ctx context.Context, _ Job) error {
	handler.entered <- struct{}{}
	select {
	case <-handler.release:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// TestSchedulerHeartbeatSuccessAndFenceLoss proves the worker joins handlers before publishing either lease outcome.
func TestSchedulerHeartbeatSuccessAndFenceLoss(t *testing.T) {
	for _, fail := range []bool{false, true} {
		t.Run(map[bool]string{false: "success", true: "fence_loss"}[fail], func(t *testing.T) {
			now := time.Now().UTC()
			queue := &heartbeatQueue{MemoryStore: NewMemoryStore(), beats: make(chan struct{}, 1), fail: fail}
			job := fixtureJob(now)
			if _, err := queue.Enqueue(context.Background(), job); err != nil {
				t.Fatal(err)
			}
			handler := channelHandler{entered: make(chan struct{}, 1), release: make(chan struct{})}
			scheduler, err := NewScheduler(queue, SystemClock{}, noopWaiter{}, handler, fixedClassifier{decision: RetryDecision{Category: "heartbeat", Backoff: time.Millisecond, MaxAttempts: 3}}, SchedulerConfig{ProjectID: job.ProjectID, WorkerID: "heartbeat-worker", Lease: 200 * time.Millisecond, HeartbeatInterval: 10 * time.Millisecond, BatchSize: 1})
			if err != nil {
				t.Fatal(err)
			}
			done := make(chan error, 1)
			go func() {
				_, runErr := scheduler.RunOnce(context.Background())
				done <- runErr
			}()
			<-handler.entered
			<-queue.beats
			if !fail {
				close(handler.release)
			}
			runErr := <-done
			if runErr != nil {
				t.Fatal("heartbeat outcome", runErr)
			}
		})
	}
	if err := (TimerWaiter{}).Wait(context.Background(), 0); err != nil {
		t.Fatal(err)
	}
	if _, err := NewWorkerRuntime(nil); !errors.Is(err, ErrInvalid) {
		t.Fatal("nil runtime accepted")
	}
}

// TestSchedulerPropagatesEveryDurableBoundaryFailure proves no failed queue write is reported as progress.
func TestSchedulerPropagatesEveryDurableBoundaryFailure(t *testing.T) {
	now := time.Now().UTC()
	for _, operation := range []string{"recover", "claim", "complete", "retry"} {
		t.Run(operation, func(t *testing.T) {
			memory := NewMemoryStore()
			if operation == "complete" || operation == "retry" {
				if _, err := memory.Enqueue(context.Background(), fixtureJob(now)); err != nil {
					t.Fatal(err)
				}
			}
			handler := JobHandler(successHandler{})
			if operation == "retry" {
				handler = failingHandler{err: errors.New("classified")}
			}
			scheduler, err := NewScheduler(schedulerFaultQueue{MemoryStore: memory, failOperation: operation}, fixedClock{now}, noopWaiter{}, handler, fixedClassifier{decision: RetryDecision{Category: "transient", Backoff: time.Second, MaxAttempts: 2}}, SchedulerConfig{ProjectID: "project-1", WorkerID: "fault-worker", Lease: time.Minute, BatchSize: 1})
			if err != nil {
				t.Fatal(err)
			}
			if _, err = scheduler.RunOnce(context.Background()); !errors.Is(err, ErrUnavailable) {
				t.Fatal("durable failure not propagated", err)
			}
		})
	}
	if _, err := NewScheduler(NewMemoryStore(), fixedClock{now}, noopWaiter{}, successHandler{}, fixedClassifier{}, SchedulerConfig{ProjectID: "project-1", WorkerID: "worker-1", Lease: time.Minute, HeartbeatInterval: time.Minute, BatchSize: 1}); !errors.Is(err, ErrInvalid) {
		t.Fatal("invalid heartbeat interval accepted", err)
	}
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	if err := (TimerWaiter{}).Wait(cancelled, time.Hour); !errors.Is(err, context.Canceled) {
		t.Fatal("timer waiter ignored cancellation", err)
	}
}

// TestSchedulerCancellationAndLoopProgress covers handler cancellation, nonempty loop continuation and runtime error joins.
func TestSchedulerCancellationAndLoopProgress(t *testing.T) {
	now := time.Now().UTC()
	memory := NewMemoryStore()
	if _, err := memory.Enqueue(context.Background(), fixtureJob(now)); err != nil {
		t.Fatal(err)
	}
	handler := channelHandler{entered: make(chan struct{}, 1), release: make(chan struct{})}
	scheduler, err := NewScheduler(memory, fixedClock{now}, noopWaiter{}, handler, fixedClassifier{}, SchedulerConfig{ProjectID: "project-1", WorkerID: "cancel-worker", Lease: time.Minute, HeartbeatInterval: 30 * time.Second, BatchSize: 1})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, runErr := scheduler.RunOnce(ctx)
		done <- runErr
	}()
	<-handler.entered
	cancel()
	if err = <-done; !errors.Is(err, context.Canceled) {
		t.Fatal("handler cancellation not propagated", err)
	}
	progressStore := NewMemoryStore()
	if _, err = progressStore.Enqueue(context.Background(), fixtureJob(now)); err != nil {
		t.Fatal(err)
	}
	progressScheduler, err := NewScheduler(progressStore, fixedClock{now}, noopWaiter{}, successHandler{}, fixedClassifier{}, SchedulerConfig{ProjectID: "project-1", WorkerID: "progress-worker", Lease: time.Minute, BatchSize: 1})
	if err != nil {
		t.Fatal(err)
	}
	if err = progressScheduler.Run(context.Background()); !errors.Is(err, context.Canceled) {
		t.Fatal("nonempty loop did not continue to waiter", err)
	}
	faultScheduler, err := NewScheduler(schedulerFaultQueue{MemoryStore: NewMemoryStore(), failOperation: "recover"}, fixedClock{now}, noopWaiter{}, successHandler{}, fixedClassifier{}, SchedulerConfig{ProjectID: "project-1", WorkerID: "runtime-fault-worker", Lease: time.Minute, BatchSize: 1})
	if err != nil {
		t.Fatal(err)
	}
	runtime, err := NewWorkerRuntime(faultScheduler)
	if err != nil {
		t.Fatal(err)
	}
	if err = runtime.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err = runtime.Join(); !errors.Is(err, ErrUnavailable) {
		t.Fatal("runtime swallowed worker failure", err)
	}
}

// TestSchedulerLoopReturnsCallerCancellation verifies no hidden process lifecycle.
func TestSchedulerLoopReturnsCallerCancellation(t *testing.T) {
	store := NewMemoryStore()
	now := time.Now().UTC()
	scheduler, _ := NewScheduler(store, fixedClock{now}, noopWaiter{}, successHandler{}, fixedClassifier{}, SchedulerConfig{ProjectID: "project-1", WorkerID: "worker-1", Lease: time.Minute, BatchSize: 1})
	if err := scheduler.Run(context.Background()); !errors.Is(err, context.Canceled) {
		t.Fatal("caller cancellation not returned")
	}
	if (SystemClock{}).Now().Location() != time.UTC {
		t.Fatal("system clock not UTC")
	}
}

// TestWorkerRuntimeIsExplicitlyStartedStoppedAndJoined proves composition owns every scheduler goroutine.
func TestWorkerRuntimeIsExplicitlyStartedStoppedAndJoined(t *testing.T) {
	entered := make(chan struct{}, 1)
	scheduler, err := NewScheduler(NewMemoryStore(), fixedClock{time.Now().UTC()}, blockingRuntimeWaiter{entered: entered}, successHandler{}, fixedClassifier{}, SchedulerConfig{ProjectID: "project-1", WorkerID: "worker-1", Lease: time.Minute, BatchSize: 1, IdleWait: time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	runtime, err := NewWorkerRuntime(scheduler)
	if err != nil {
		t.Fatal(err)
	}
	if err = runtime.Join(); !errors.Is(err, ErrInvalid) {
		t.Fatal("unstarted runtime joined", err)
	}
	if err = runtime.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err = runtime.Start(context.Background()); !errors.Is(err, ErrConflict) {
		t.Fatal("runtime started twice", err)
	}
	<-entered
	runtime.Stop()
	if err = runtime.Join(); err != nil {
		t.Fatal(err)
	}
}

// TestWorkerRuntimeJoinContextBoundsNonCooperativeHandler proves shutdown authority does not wait forever after cancellation crosses the handler boundary.
func TestWorkerRuntimeJoinContextBoundsNonCooperativeHandler(t *testing.T) {
	queue := NewMemoryStore()
	clock := fixedClock{now: time.Unix(100, 0).UTC()}
	job, err := SealJob(Job{JobID: "blocked-job", ProjectID: "project", RunID: "run", BackendEpoch: 1, BindingDigest: fixedDigest("binding"), Phase: PhaseExecute, State: JobQueued, InputDigest: fixedDigest("input"), CreatedAt: clock.Now(), AvailableAt: clock.Now(), Deadline: clock.Now().Add(time.Hour)})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = queue.Enqueue(context.Background(), job); err != nil {
		t.Fatal(err)
	}
	entered := make(chan struct{})
	release := make(chan struct{})
	handler := nonCooperativeHandler{entered: entered, release: release}
	scheduler, err := NewScheduler(queue, clock, noopWaiter{}, handler, fixedClassifier{}, SchedulerConfig{ProjectID: "project", WorkerID: "worker", Lease: time.Minute, HeartbeatInterval: 20 * time.Second, BatchSize: 1, IdleWait: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	runtime, _ := NewWorkerRuntime(scheduler)
	parent, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err = runtime.Start(parent); err != nil {
		t.Fatal(err)
	}
	<-entered
	runtime.Stop()
	joinCtx, stopJoin := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer stopJoin()
	if err = runtime.JoinContext(joinCtx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("join claimed completion while handler remained owned: %v", err)
	}
	close(release)
	completed, stopCompleted := context.WithTimeout(context.Background(), time.Second)
	defer stopCompleted()
	if err = runtime.JoinContext(completed); err != nil {
		t.Fatalf("later join did not observe real handler termination: %v", err)
	}
}

// TestValidationMatrices exercises malformed, expired and cross-phase authority boundaries.
func TestValidationMatrices(t *testing.T) {
	now := time.Now().UTC()
	if _, err := SealBackendEpoch(BackendEpoch{}); !errors.Is(err, ErrInvalid) {
		t.Fatal("invalid epoch")
	}
	if _, err := SealBindingHead(BindingHead{}); !errors.Is(err, ErrInvalid) {
		t.Fatal("invalid head")
	}
	if _, err := SealJob(Job{}); !errors.Is(err, ErrInvalid) {
		t.Fatal("invalid job")
	}
	if _, err := SealAuthority(AuthorityEnvelope{}, now); !errors.Is(err, ErrInvalid) {
		t.Fatal("invalid authority")
	}
	if _, err := SealInboxEnvelope(InboxEnvelope{}); !errors.Is(err, ErrInvalid) {
		t.Fatal("invalid inbox envelope")
	}
	if err := VerifyJob(Job{}); !errors.Is(err, ErrIntegrity) {
		t.Fatal("invalid job verified")
	}
	if err := VerifyAuthorityGrant(AuthorityGrant{}); !errors.Is(err, ErrIntegrity) {
		t.Fatal("invalid authority grant verified")
	}
	if _, err := SealAuthorityGrant(AuthorityGrant{ProjectID: "project-1", RunID: "run-1", Phase: PhaseExecute, InputReferences: []string{"unsafe value"}, TargetBindingRef: "target", TargetBindingDigest: fixedDigest("target")}); !errors.Is(err, ErrInvalid) {
		t.Fatal("unsafe authority reference accepted")
	}
	if _, err := SealImportManifest(ImportManifest{}); !errors.Is(err, ErrInvalid) {
		t.Fatal("invalid import manifest")
	}
	if _, err := SealImportDocument(ImportDocument{}); !errors.Is(err, ErrInvalid) {
		t.Fatal("invalid import document")
	}
	if _, err := SealImportDocument(ImportDocument{Stream: "runs", RelationKey: "run-1", Document: []byte(`{}`), Digest: fixedDigest("wrong")}); !errors.Is(err, ErrIntegrity) {
		t.Fatal("tampered import document")
	}
	if err := StrictDecode([]byte(`{} {}`), &Job{}); !errors.Is(err, ErrInvalid) {
		t.Fatal("trailing document")
	}
	if err := StrictDecode(make([]byte, MaxCanonicalBytes+1), &Job{}); !errors.Is(err, ErrInvalid) {
		t.Fatal("oversize document")
	}
	if _, err := NewCursorCodec([]byte("short")); !errors.Is(err, ErrInvalid) {
		t.Fatal("invalid cursor")
	}
	codec, _ := NewCursorCodec([]byte("0123456789abcdef0123456789abcdef"))
	if _, err := (*CursorCodec)(nil).Seal(PageCursor{}); !errors.Is(err, ErrInvalid) {
		t.Fatal("nil cursor codec accepted")
	}
	if _, err := codec.Seal(PageCursor{}); !errors.Is(err, ErrInvalid) {
		t.Fatal("invalid cursor payload accepted")
	}
	if _, err := codec.Open("not-base64", "project-1", "jobs", now); !errors.Is(err, ErrInvalid) {
		t.Fatal("invalid token")
	}
	if _, err := RunRetention(context.Background(), nil, &RetentionPolicy{BeforeUnix: 1, BatchSize: 1}); !errors.Is(err, ErrInvalid) {
		t.Fatal("nil retention")
	}
	if _, err := NewScheduler(nil, nil, nil, nil, nil, SchedulerConfig{}); !errors.Is(err, ErrInvalid) {
		t.Fatal("invalid scheduler")
	}
	store := NewMemoryStore()
	job := fixtureJob(now)
	store.Enqueue(context.Background(), job)
	claimed, _ := store.Claim(context.Background(), ClaimRequest{ProjectID: job.ProjectID, WorkerID: "worker-1", Now: now, Lease: time.Minute, Limit: 1})
	leased := claimed[0]
	leased.Phase = PhaseEvaluate
	leased.Digest = ""
	leased, _ = SealJob(leased)
	if _, err := BuildAuthority(leased, AuthorityRequest{EnvelopeID: "env-1", WorkerID: "worker-1", ExpiresAt: now.Add(time.Second), Capabilities: []string{"tool"}}, now); !errors.Is(err, ErrUnauthorized) {
		t.Fatal("evaluation capability")
	}
	leased.Phase = PhasePlan
	leased.Digest = ""
	leased, _ = SealJob(leased)
	if _, err := BuildAuthority(leased, AuthorityRequest{EnvelopeID: "env-2", WorkerID: "worker-1", ExpiresAt: now.Add(time.Second), TargetBindingRef: "target", TargetBindingDigest: fixedDigest("target")}, now); !errors.Is(err, ErrUnauthorized) {
		t.Fatal("planning target")
	}
	resolver := &recordingResolver{}
	if _, err := ResolveAuthorizedSecrets(context.Background(), AuthorityEnvelope{}, leased, "worker-1", now, resolver); err == nil || resolver.called {
		t.Fatal("invalid authority resolved")
	}
}

// TestCancelledMemoryOperations proves caller cancellation wins before fake mutation.
func TestCancelledMemoryOperations(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	store := NewMemoryStore()
	now := time.Now().UTC()
	job := fixtureJob(now)
	if _, err := store.Enqueue(ctx, job); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if _, err := store.Claim(ctx, ClaimRequest{}); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if _, err := store.Heartbeat(ctx, "x", "x", 1, 0, now, time.Minute); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if _, err := store.Complete(ctx, "x", "x", 1, 0, now, JobSucceeded, ""); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if _, err := store.Retry(ctx, "x", "x", 1, 0, now, RetryDecision{}, now); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if _, err := store.Cancel(ctx, "x", 1); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if _, err := store.Recover(ctx, now, 1); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	epoch := BackendEpoch{}
	if _, err := store.ReserveInitial(ctx, epoch); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if _, _, err := store.Load(ctx, "x"); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if _, err := store.Cutover(ctx, BindingHead{}, epoch); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if _, err := store.Accept(ctx, InboxEnvelope{}, job); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if _, err := store.Prepare(ctx, EffectIntent{}); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if err := store.Confirm(ctx, "x", fixedDigest("x")); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}
