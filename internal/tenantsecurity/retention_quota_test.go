package tenantsecurity

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// quotaPolicy returns equal tenant/project ceilings for deterministic guard tests.
func quotaPolicy(limit, burst uint64) QuotaPolicySnapshot {
	dimensions := []string{QuotaWorker, QuotaModelCall, QuotaModelToken, QuotaEnvironment, QuotaInfrastructure, QuotaObject, QuotaStorageByte, QuotaQueueClaim}
	tenant, project := map[string]QuotaLimit{}, map[string]QuotaLimit{}
	for _, dimension := range dimensions {
		tenant[dimension] = QuotaLimit{Limit: limit, Burst: burst, Window: time.Hour}
		project[dimension] = QuotaLimit{Limit: limit, Burst: burst, Window: time.Hour}
	}
	return QuotaPolicySnapshot{Version: 1, Tenant: tenant, Project: project, TenantOutstanding: uint64(MaxItems), ProjectOutstanding: uint64(MaxItems), ReservationTTL: time.Hour}
}

// durableCapability creates an authorized capability backed by the returned durable fixture.
func durableCapability(t *testing.T, action Action, resource ResourceClass) (*testDurable, *fakeClock, Capability) {
	t.Helper()
	service, memory, clock, principal, identity := fixture(t)
	capability, err := service.Authorize(context.Background(), principal, action, resource, identity)
	if err != nil {
		t.Fatal(err)
	}
	return &testDurable{snapshot: memory.Snapshot()}, clock, capability
}

// TestRetentionPolicyEligibilityFreezesBoundary covers floors, missing classes, live refs and protected data.
func TestRetentionPolicyEligibilityFreezesBoundary(t *testing.T) {
	now := time.Date(2026, 2, 1, 0, 0, 0, 0, time.UTC)
	floors, periods := map[RetentionClass]time.Duration{}, map[RetentionClass]time.Duration{}
	for _, class := range retentionClasses() {
		floors[class], periods[class] = 24*time.Hour, 48*time.Hour
	}
	policy := RetentionPolicy{OrganizationID: "org-a", Version: 7, Floors: floors, Periods: periods, ProtectedRecords: map[RetentionClass][]string{RetentionEvidence: {"legal-hold"}}, BatchLimit: 10, GraphLimit: 20}
	compiled, err := CompileRetention(policy)
	if err != nil || compiled.Digest == "" {
		t.Fatalf("compile=%v", err)
	}
	expansive := policy
	expansive.Periods = map[RetentionClass]time.Duration{}
	for class, period := range periods {
		expansive.Periods[class] = period
	}
	expansive.Periods[RetentionRun] = time.Hour
	if _, err = CompileRetention(expansive); !IsCategory(err, CategoryInvalid) {
		t.Fatalf("floor expansion=%v", err)
	}
	missing := policy
	missing.Periods = map[RetentionClass]time.Duration{RetentionRun: 48 * time.Hour}
	if _, err = CompileRetention(missing); !IsCategory(err, CategoryInvalid) {
		t.Fatalf("missing class=%v", err)
	}
	candidate := RetentionCandidate{ID: "evidence-a", Class: RetentionEvidence, TerminalAt: now.Add(-72 * time.Hour)}
	boundary, err := EvaluateRetention(policy, candidate, now)
	if err != nil || boundary.PolicyVersion != 7 || boundary.PolicyDigest != compiled.Digest || boundary.CandidateDigest == "" {
		t.Fatalf("boundary=%+v err=%v", boundary, err)
	}
	for name, blocked := range map[string]RetentionCandidate{
		"active":          {ID: "blocked-active", Class: RetentionRun, TerminalAt: candidate.TerminalAt, Active: true},
		"live-reference":  {ID: "blocked-reference", Class: RetentionRun, TerminalAt: candidate.TerminalAt, LiveReferences: 1},
		"pending-job":     {ID: "blocked-job", Class: RetentionRun, TerminalAt: candidate.TerminalAt, PendingJobs: 1},
		"pending-outbox":  {ID: "blocked-outbox", Class: RetentionRun, TerminalAt: candidate.TerminalAt, PendingOutbox: 1},
		"pending-cleanup": {ID: "blocked-cleanup", Class: RetentionRun, TerminalAt: candidate.TerminalAt, PendingCleanup: 1},
		"active-secret":   {ID: "blocked-secret", Class: RetentionSecret, TerminalAt: candidate.TerminalAt, ActiveSecret: true},
		"protected":       {ID: "legal-hold", Class: RetentionEvidence, TerminalAt: candidate.TerminalAt},
	} {
		t.Run(name, func(t *testing.T) {
			if _, blockedErr := EvaluateRetention(policy, blocked, now); !IsCategory(blockedErr, CategoryConflict) {
				t.Fatalf("blocked=%v", blockedErr)
			}
		})
	}
}

// scriptedRetentionConsumer exposes deterministic existence, reference and delete outcomes.
type scriptedRetentionConsumer struct {
	store        DurableStore
	scope        TenantScope
	id           string
	observation  DeletionObservation
	deleteStatus EffectStatus
	deleteErr    error
	deletes      atomic.Int32
}

// Observe returns the current consumer-owned liveness observation.
func (consumer *scriptedRetentionConsumer) Observe(context.Context, DeletionCommand) (DeletionObservation, error) {
	return consumer.observation, nil
}

// Delete asserts intent-before-effect and records the irreversible call count.
func (consumer *scriptedRetentionConsumer) Delete(ctx context.Context, command DeletionCommand) (EffectStatus, error) {
	snapshot, err := consumer.store.Snapshot(ctx)
	if err != nil {
		return EffectRejected, err
	}
	intent := snapshot.Deletions[deletionKey(consumer.scope, consumer.id)]
	if intent.State != DeletionDeleting || intent.Fence != command.Fence || intent.DeleteDigest != command.DeleteDigest {
		return EffectRejected, errors.New("delete before durable fenced intent")
	}
	consumer.deletes.Add(1)
	return consumer.deleteStatus, consumer.deleteErr
}

// TestRetentionControllerConvergesUnknownDelete proves restart reconciliation, fencing and tombstones.
func TestRetentionControllerConvergesUnknownDelete(t *testing.T) {
	store, clock, capability := durableCapability(t, ActionRetentionExecute, ResourceEvidence)
	floors, periods := map[RetentionClass]time.Duration{}, map[RetentionClass]time.Duration{}
	for _, class := range retentionClasses() {
		floors[class], periods[class] = time.Hour, time.Hour
	}
	eligibility, err := EvaluateRetention(RetentionPolicy{OrganizationID: capability.Scope.OrganizationID, Version: 1, Floors: floors, Periods: periods, ProtectedRecords: map[RetentionClass][]string{}, BatchLimit: 10, GraphLimit: 10}, RetentionCandidate{ID: "evidence-delete", Class: RetentionEvidence, TerminalAt: clock.now.Add(-2 * time.Hour)}, clock.now)
	if err != nil {
		t.Fatal(err)
	}
	consumer := &scriptedRetentionConsumer{store: store, scope: capability.Scope, id: eligibility.CandidateID, observation: DeletionObservation{Exists: true}, deleteStatus: EffectUnknown, deleteErr: errors.New("connection lost")}
	controller, err := NewRetentionController(store, clock, map[ResourceClass]RetentionConsumer{ResourceEvidence: consumer})
	if err != nil {
		t.Fatal(err)
	}
	intent, err := controller.Plan(context.Background(), DeletionPlan{Capability: capability, OperationID: "plan-delete", Eligibility: eligibility, Resource: ResourceEvidence})
	if err != nil || intent.State != DeletionPlanned {
		t.Fatalf("plan=%+v err=%v", intent, err)
	}
	if err = controller.Converge(context.Background(), capability, eligibility.CandidateID, "delete-first"); err == nil {
		t.Fatal("unknown delete reported success")
	}
	uncertain, err := controller.loadIntent(context.Background(), capability.Scope, eligibility.CandidateID)
	if err != nil || uncertain.State != DeletionUncertain || uncertain.Fence != 2 || consumer.deletes.Load() != 1 {
		t.Fatalf("uncertain=%+v deletes=%d err=%v", uncertain, consumer.deletes.Load(), err)
	}
	consumer.observation = DeletionObservation{Exists: false}
	restarted, _ := NewRetentionController(store, clock, map[ResourceClass]RetentionConsumer{ResourceEvidence: consumer})
	if err = restarted.Converge(context.Background(), capability, eligibility.CandidateID, "delete-restart"); err != nil {
		t.Fatal(err)
	}
	tombstone, err := restarted.loadIntent(context.Background(), capability.Scope, eligibility.CandidateID)
	if err != nil || tombstone.State != DeletionTombstone || tombstone.TombstoneDigest == "" || consumer.deletes.Load() != 1 {
		t.Fatalf("tombstone=%+v deletes=%d err=%v", tombstone, consumer.deletes.Load(), err)
	}
	if err = restarted.Converge(context.Background(), capability, eligibility.CandidateID, "delete-again"); err != nil || consumer.deletes.Load() != 1 {
		t.Fatalf("idempotent=%v deletes=%d", err, consumer.deletes.Load())
	}
}

// TestRetentionControllerRechecksReferencesAndCancellation proves no delete crosses an unsafe boundary.
func TestRetentionControllerRechecksReferencesAndCancellation(t *testing.T) {
	store, clock, capability := durableCapability(t, ActionRetentionExecute, ResourceEvidence)
	floors, periods := map[RetentionClass]time.Duration{}, map[RetentionClass]time.Duration{}
	for _, class := range retentionClasses() {
		floors[class], periods[class] = time.Hour, time.Hour
	}
	eligibility, _ := EvaluateRetention(RetentionPolicy{OrganizationID: capability.Scope.OrganizationID, Version: 1, Floors: floors, Periods: periods, ProtectedRecords: map[RetentionClass][]string{}, BatchLimit: 2, GraphLimit: 2}, RetentionCandidate{ID: "reference-race", Class: RetentionEvidence, TerminalAt: clock.now.Add(-2 * time.Hour)}, clock.now)
	consumer := &scriptedRetentionConsumer{store: store, scope: capability.Scope, id: eligibility.CandidateID, observation: DeletionObservation{Exists: true, LiveReferences: 1}, deleteStatus: EffectCommitted}
	controller, _ := NewRetentionController(store, clock, map[ResourceClass]RetentionConsumer{ResourceEvidence: consumer})
	_, _ = controller.Plan(context.Background(), DeletionPlan{Capability: capability, OperationID: "plan-race", Eligibility: eligibility, Resource: ResourceEvidence})
	if err := controller.Converge(context.Background(), capability, eligibility.CandidateID, "race"); !IsCategory(err, CategoryConflict) || consumer.deletes.Load() != 0 {
		t.Fatalf("reference race=%v deletes=%d", err, consumer.deletes.Load())
	}
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	consumer.observation = DeletionObservation{Exists: true}
	if err := controller.Converge(cancelled, capability, eligibility.CandidateID, "cancel"); !IsCategory(err, CategoryCancelled) || consumer.deletes.Load() != 0 {
		t.Fatalf("cancel=%v deletes=%d", err, consumer.deletes.Load())
	}
}

// TestProjectDeletionBlocksAdmission keeps retention authority while denying all new project work.
func TestProjectDeletionBlocksAdmission(t *testing.T) {
	service, memory, clock, principal, identity := fixture(t)
	deleteCapability, err := service.Authorize(context.Background(), principal, ActionRetentionExecute, ResourceTenant, identity)
	if err != nil {
		t.Fatal(err)
	}
	admitCapability, err := service.Authorize(context.Background(), principal, ActionRunAdmit, ResourceRun, identity)
	if err != nil {
		t.Fatal(err)
	}
	store := &testDurable{snapshot: memory.Snapshot()}
	controller, err := NewRetentionController(store, clock, map[ResourceClass]RetentionConsumer{})
	if err != nil {
		t.Fatal(err)
	}
	if err = controller.BeginProjectDeletion(context.Background(), deleteCapability, "project-delete"); err != nil {
		t.Fatal(err)
	}
	snapshot, err := store.Snapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Projects[projectKey(identity.OrganizationID, identity.ProjectID)].State != StateSuspended {
		t.Fatal("project admission remained active")
	}
	if err = ValidateCapabilitySnapshot(snapshot, admitCapability, ActionRunAdmit, ResourceRun, clock.now); !IsCategory(err, CategoryUnauthorized) {
		t.Fatalf("admission after deletion=%v", err)
	}
	if err = ValidateCapabilitySnapshot(snapshot, deleteCapability, ActionRetentionExecute, ResourceTenant, clock.now); err != nil {
		t.Fatalf("retention authority lost=%v", err)
	}
}

// TestDurableQuotaHierarchyLastSlotReplayAndRelease covers concurrent CAS, idempotency and exact release.
func TestDurableQuotaHierarchyLastSlotReplayAndRelease(t *testing.T) {
	store, clock, capability := durableCapability(t, ActionQuotaReserve, ResourceQuota)
	quotas, _ := NewDurableQuotaService(store, clock)
	policy := quotaPolicy(1, 0)
	requests := []QuotaRequest{
		{Capability: capability, OperationID: "worker-a", Dimension: QuotaWorker, Units: 1, Policy: policy},
		{Capability: capability, OperationID: "worker-b", Dimension: QuotaWorker, Units: 1, Policy: policy},
	}
	var wg sync.WaitGroup
	var successes atomic.Int32
	for _, request := range requests {
		wg.Add(1)
		go func(request QuotaRequest) {
			defer wg.Done()
			if _, err := quotas.Reserve(context.Background(), request); err == nil {
				successes.Add(1)
			} else if !IsCategory(err, CategoryQuotaExhausted) {
				t.Errorf("reserve=%v", err)
			}
		}(request)
	}
	wg.Wait()
	if successes.Load() != 1 {
		t.Fatalf("last slot successes=%d", successes.Load())
	}
	snapshot, _ := store.Snapshot(context.Background())
	var winner Reservation
	for _, reservation := range snapshot.Reservations {
		winner = reservation
	}
	replay, err := quotas.Reserve(context.Background(), QuotaRequest{Capability: capability, OperationID: winner.ID, Dimension: winner.Dimension, Units: winner.Units, Policy: policy})
	if err != nil || replay.Digest != winner.Digest {
		t.Fatalf("replay=%+v err=%v", replay, err)
	}
	if _, err = quotas.Reserve(context.Background(), QuotaRequest{Capability: capability, OperationID: winner.ID, Dimension: winner.Dimension, Units: 2, Policy: policy}); !IsCategory(err, CategoryConflict) {
		t.Fatalf("divergent replay=%v", err)
	}
	if err = quotas.Release(context.Background(), capability, winner.ID, "release-winner"); err != nil {
		t.Fatal(err)
	}
	if err = quotas.Release(context.Background(), capability, winner.ID, "release-winner-again"); err != nil {
		t.Fatal(err)
	}
	after, _ := store.Snapshot(context.Background())
	for _, key := range winner.CounterKeys {
		if after.Quotas[key].Reserved != 0 {
			t.Fatalf("counter not released: %+v", after.Quotas[key])
		}
	}
	expiryPolicy := quotaPolicy(2, 0)
	expiryPolicy.ReservationTTL = time.Minute
	expiring, err := quotas.Reserve(context.Background(), QuotaRequest{Capability: capability, OperationID: "worker-expiry", Dimension: QuotaWorker, Units: 1, Policy: expiryPolicy})
	if err != nil {
		t.Fatal(err)
	}
	if err = quotas.ReleaseExpired(context.Background(), capability, expiring.ID, "expiry-too-soon"); !IsCategory(err, CategoryConflict) {
		t.Fatalf("early expiry=%v", err)
	}
	clock.now = clock.now.Add(2 * time.Minute)
	if err = quotas.ReleaseExpired(context.Background(), capability, expiring.ID, "expiry-confirmed"); err != nil {
		t.Fatal(err)
	}
	outstandingPolicy := quotaPolicy(10, 0)
	outstandingPolicy.TenantOutstanding, outstandingPolicy.ProjectOutstanding = 1, 1
	if _, err = quotas.Reserve(context.Background(), QuotaRequest{Capability: capability, OperationID: "outstanding-a", Dimension: QuotaModelToken, Units: 1, Policy: outstandingPolicy}); err != nil {
		t.Fatal(err)
	}
	if _, err = quotas.Reserve(context.Background(), QuotaRequest{Capability: capability, OperationID: "outstanding-b", Dimension: QuotaModelToken, Units: 1, Policy: outstandingPolicy}); !IsCategory(err, CategoryQuotaExhausted) {
		t.Fatalf("outstanding=%v", err)
	}
}

// TestDurableQuotaResourceLifecycleSurvivesRestartAndPartialCompletion proves
// a claimed batch remains charged until every durable resource is terminal,
// while uncertainty and replay never release capacity early.
func TestDurableQuotaResourceLifecycleSurvivesRestartAndPartialCompletion(t *testing.T) {
	store, clock, capability := durableCapability(t, ActionQuotaReserve, ResourceQuota)
	quotas, _ := NewDurableQuotaService(store, clock)
	policy := quotaPolicy(4, 0)
	reservation, err := quotas.Reserve(context.Background(), QuotaRequest{Capability: capability, OperationID: "worker-batch", Dimension: QuotaWorker, Units: 2, Policy: policy})
	if err != nil {
		t.Fatal(err)
	}
	if err = quotas.BindResources(context.Background(), capability, reservation.ID, "bind-empty", nil); !IsCategory(err, CategoryInvalid) {
		t.Fatalf("empty binding=%v", err)
	}
	if err = quotas.BindResources(context.Background(), capability, reservation.ID, "bind-invalid", []string{"bad/id"}); !IsCategory(err, CategoryInvalid) {
		t.Fatalf("invalid binding=%v", err)
	}
	if err = quotas.BindResources(context.Background(), capability, reservation.ID, "bind-batch", []string{"job-b", "job-a"}); err != nil {
		t.Fatal(err)
	}
	if err = quotas.BindResources(context.Background(), capability, reservation.ID, "bind-replay", []string{"job-a", "job-b"}); err != nil {
		t.Fatalf("sorted replay=%v", err)
	}
	if err = quotas.BindResources(context.Background(), capability, reservation.ID, "bind-conflict", []string{"job-c"}); !IsCategory(err, CategoryConflict) {
		t.Fatalf("divergent binding=%v", err)
	}
	if err = quotas.ReleaseResource(context.Background(), capability, "missing-job", "release-missing", false); !IsCategory(err, CategoryNotFoundHidden) {
		t.Fatalf("missing job=%v", err)
	}
	if err = quotas.ReleaseResource(context.Background(), capability, "bad/id", "release-invalid", false); !IsCategory(err, CategoryInvalid) {
		t.Fatalf("invalid job=%v", err)
	}
	if err = quotas.ReleaseResource(context.Background(), capability, "job-a", "release-uncertain", true); err != nil {
		t.Fatal(err)
	}
	restarted, _ := NewDurableQuotaService(store, clock)
	if err = restarted.ReleaseResource(context.Background(), capability, "job-a", "release-a", false); err != nil {
		t.Fatal(err)
	}
	midway, _ := store.Snapshot(context.Background())
	active := midway.Reservations[quotaReservationKey(capability.Scope, reservation.ID)]
	if active.Released || len(active.CompletedIDs) != 1 || !active.Uncertain {
		t.Fatalf("partial completion released capacity: %+v", active)
	}
	if err = restarted.ReleaseResource(context.Background(), capability, "job-a", "release-a-replay", false); err != nil {
		t.Fatalf("resource replay=%v", err)
	}
	if err = restarted.ReleaseResource(context.Background(), capability, "job-b", "release-b", false); err != nil {
		t.Fatal(err)
	}
	finished, _ := store.Snapshot(context.Background())
	terminal := finished.Reservations[quotaReservationKey(capability.Scope, reservation.ID)]
	if !terminal.Released || len(terminal.CompletedIDs) != 2 {
		t.Fatalf("terminal batch not released: %+v", terminal)
	}
	for _, key := range terminal.CounterKeys {
		if finished.Quotas[key].Reserved != 0 {
			t.Fatalf("counter %s retained %+v", key, finished.Quotas[key])
		}
	}
}

// testQuotaEffect records whether denial invoked a consumer and returns a configured outcome.
type testQuotaEffect struct {
	calls  atomic.Int32
	status EffectStatus
	err    error
}

// Execute records one post-reservation effect call.
func (effect *testQuotaEffect) Execute(context.Context, Reservation) (EffectStatus, error) {
	effect.calls.Add(1)
	return effect.status, effect.err
}

// TestQuotaGuardOrdersEffectsAndRetainsUnknownReservation covers denial and reconcile-safe uncertainty.
func TestQuotaGuardOrdersEffectsAndRetainsUnknownReservation(t *testing.T) {
	store, clock, capability := durableCapability(t, ActionQuotaReserve, ResourceQuota)
	quotas, _ := NewDurableQuotaService(store, clock)
	guard, err := NewQuotaGuard(quotas)
	if err != nil {
		t.Fatal(err)
	}
	unknown := &testQuotaEffect{status: EffectUnknown, err: errors.New("timeout")}
	request := QuotaRequest{Capability: capability, OperationID: "model-unknown", Dimension: QuotaModelCall, Units: 1, Policy: quotaPolicy(1, 0)}
	reservation, err := guard.Execute(context.Background(), request, unknown)
	if err == nil || unknown.calls.Load() != 1 {
		t.Fatalf("unknown err=%v calls=%d", err, unknown.calls.Load())
	}
	snapshot, _ := store.Snapshot(context.Background())
	retained := snapshot.Reservations[quotaReservationKey(capability.Scope, reservation.ID)]
	if !retained.Uncertain || retained.Released {
		t.Fatalf("unknown capacity not retained: %+v", retained)
	}
	denied := &testQuotaEffect{status: EffectCommitted}
	_, err = guard.Execute(context.Background(), QuotaRequest{Capability: capability, OperationID: "model-denied", Dimension: QuotaModelCall, Units: 1, Policy: quotaPolicy(1, 0)}, denied)
	if !IsCategory(err, CategoryQuotaExhausted) || denied.calls.Load() != 0 {
		t.Fatalf("denial=%v calls=%d", err, denied.calls.Load())
	}
	overflow := quotaPolicy(^uint64(0), 1)
	if _, err = CompileQuotaPolicy(overflow); !IsCategory(err, CategoryInvalid) {
		t.Fatalf("overflow=%v", err)
	}
	if _, err = guard.Execute(context.Background(), request, nil); !IsCategory(err, CategoryInvalid) {
		t.Fatalf("nil effect=%v", err)
	}
}

// TestQuotaGuardReleasesRejectedEffectsAndRejectsUnknownStatuses proves a
// consumer's authoritative no-effect outcome frees capacity, while an invalid
// outcome cannot be interpreted as either success or safe release.
func TestQuotaGuardReleasesRejectedEffectsAndRejectsUnknownStatuses(t *testing.T) {
	store, clock, capability := durableCapability(t, ActionQuotaReserve, ResourceQuota)
	quotas, _ := NewDurableQuotaService(store, clock)
	guard, _ := NewQuotaGuard(quotas)
	rejection := errors.New("provider rejected before effect")
	rejected := &testQuotaEffect{status: EffectRejected, err: rejection}
	request := QuotaRequest{Capability: capability, OperationID: "environment-rejected", Dimension: QuotaEnvironment, Units: 1, Policy: quotaPolicy(2, 0)}
	reservation, err := guard.Execute(context.Background(), request, rejected)
	if !errors.Is(err, rejection) || rejected.calls.Load() != 1 {
		t.Fatalf("rejected reservation=%+v calls=%d err=%v", reservation, rejected.calls.Load(), err)
	}
	snapshot, _ := store.Snapshot(context.Background())
	if !snapshot.Reservations[quotaReservationKey(capability.Scope, reservation.ID)].Released {
		t.Fatalf("rejected effect retained capacity: %+v", snapshot.Reservations)
	}
	invalid := &testQuotaEffect{status: EffectStatus("invalid")}
	request.OperationID = "environment-invalid-status"
	if _, err = guard.Execute(context.Background(), request, invalid); !IsCategory(err, CategoryIntegrity) || invalid.calls.Load() != 1 {
		t.Fatalf("invalid status calls=%d err=%v", invalid.calls.Load(), err)
	}
}

// TestQuotaScopedRepositoryReservesObjectAndStorageBeforePut covers the productive object-store consumer.
func TestQuotaScopedRepositoryReservesObjectAndStorageBeforePut(t *testing.T) {
	service, memory, clock, principal, identity := fixture(t)
	quotaCapability, err := service.Authorize(context.Background(), principal, ActionQuotaReserve, ResourceQuota, identity)
	if err != nil {
		t.Fatal(err)
	}
	recordCapability, err := service.Authorize(context.Background(), principal, ActionEvidenceRead, ResourceEvidence, identity)
	if err != nil {
		t.Fatal(err)
	}
	store := &testDurable{snapshot: memory.Snapshot()}
	quotas, _ := NewDurableQuotaService(store, clock)
	guard, _ := NewQuotaGuard(quotas)
	next, _ := NewScopedRepository(store, clock)
	if _, err = NewQuotaScopedRepository(next, nil); !IsCategory(err, CategoryInvalid) {
		t.Fatalf("missing guard=%v", err)
	}
	repository, _ := NewQuotaScopedRepository(next, guard)
	policy := QuotaPolicySnapshot{Version: 1, Tenant: map[string]QuotaLimit{QuotaObject: {Limit: 2, Window: time.Hour}, QuotaStorageByte: {Limit: 1024, Window: time.Hour}}, Project: map[string]QuotaLimit{QuotaObject: {Limit: 2, Window: time.Hour}, QuotaStorageByte: {Limit: 1024, Window: time.Hour}}, TenantOutstanding: 4, ProjectOutstanding: 4, ReservationTTL: time.Hour}
	record, receipt, err := repository.Put(context.Background(), quotaCapability, recordCapability, "evidence-put", EntityEvidence, "evidence-object", digestStrings("payload"), 0, 128, policy)
	if err != nil || record.ID != "evidence-object" || receipt.OperationID != "evidence-put" {
		t.Fatalf("put=%+v receipt=%+v err=%v", record, receipt, err)
	}
	snapshot, _ := store.Snapshot(context.Background())
	if len(snapshot.Reservations) != 2 || len(snapshot.Records) != 1 {
		t.Fatalf("reservations=%d records=%d", len(snapshot.Reservations), len(snapshot.Records))
	}
}
