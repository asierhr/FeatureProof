package githubpr

import (
	"context"
	"sync"
	"testing"
	"time"
)

// releaseBarrierStore exposes the exact interval around one worker lease release without changing store semantics.
type releaseBarrierStore struct {
	Store
	owner         string
	beforeRelease chan struct{}
	allowRelease  chan struct{}
	afterRelease  chan struct{}
	once          sync.Once
}

// ReleaseAssociation blocks the selected owner's first release at deterministic before/after barriers.
func (s *releaseBarrierStore) ReleaseAssociation(ctx context.Context, id, owner string, now time.Time) error {
	if owner != s.owner {
		return s.Store.ReleaseAssociation(ctx, id, owner, now)
	}
	blocked := false
	s.once.Do(func() {
		blocked = true
		close(s.beforeRelease)
	})
	if blocked {
		select {
		case <-s.allowRelease:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	err := s.Store.ReleaseAssociation(ctx, id, owner, now)
	if blocked {
		close(s.afterRelease)
	}
	return err
}

// blockingRecoveryGitHub holds one recovered check effect while another worker crosses its release boundary.
type blockingRecoveryGitHub struct {
	*fakeGitHub
	entered chan struct{}
	release chan struct{}
	once    sync.Once
}

// FindChecks pauses the first reconciliation and then delegates to the deterministic GitHub fake.
func (g *blockingRecoveryGitHub) FindChecks(ctx context.Context, installationID, repositoryID int64, headOID, externalID string, pages int) ([]CheckRecord, error) {
	blocked := false
	g.once.Do(func() {
		blocked = true
		close(g.entered)
	})
	if blocked {
		select {
		case <-g.release:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	return g.fakeGitHub.FindChecks(ctx, installationID, repositoryID, headOID, externalID, pages)
}

// handoffValidation controls a submit failure or a joinable submit barrier and records exact handoff count.
type handoffValidation struct {
	mu      sync.Mutex
	submits int
	entered chan struct{}
	once    sync.Once
	fail    bool
}

// Submit records one worker handoff, then either fails immediately or waits for supervisor cancellation.
func (v *handoffValidation) Submit(ctx context.Context, _ Selection, _ PullRequestSnapshot, _ string) (RunState, error) {
	v.mu.Lock()
	v.submits++
	v.mu.Unlock()
	v.once.Do(func() { close(v.entered) })
	if v.fail {
		return RunState{}, &Error{Code: CodeTransient, Operation: "controlled_submit"}
	}
	<-ctx.Done()
	return RunState{}, ctx.Err()
}

// ResolveExisting is unused by the startup handoff regression.
func (v *handoffValidation) ResolveExisting(context.Context, ExistingRunLookup) (RunState, error) {
	return RunState{}, ErrNotFound
}

// Status is unused while the controlled submit remains at its boundary.
func (v *handoffValidation) Status(context.Context, string) (RunState, error) {
	return RunState{}, ErrNotFound
}

// Cancel accepts supervisor retirement in the controlled handoff regression.
func (v *handoffValidation) Cancel(context.Context, string, string) error { return nil }

// submitCount returns the synchronized number of worker handoffs.
func (v *handoffValidation) submitCount() int {
	v.mu.Lock()
	defer v.mu.Unlock()
	return v.submits
}

// TestStartupDrainExcludesDeliveredWorkerUntilIndependentHandoff closes REV-23 across release barriers and two coordinators.
func TestStartupDrainExcludesDeliveredWorkerUntilIndependentHandoff(t *testing.T) {
	dir := t.TempDir()
	baseA, err := OpenLocalStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	snapshot := testSnapshot(t)
	worker, _ := NewAssociation("a-worker", "delivery-worker", "default", 4, snapshot)
	worker, _, err = baseA.ReserveAssociation(context.Background(), worker)
	if err != nil {
		t.Fatal(err)
	}
	recovery, _ := NewAssociation("b-recovery", "delivery-recovery", "default", 4, snapshot)
	recovery = bindTestAssociation(t, recovery, "run-recovery")
	recovery.Lifecycle = "terminal"
	recovery, _ = SealAssociation(recovery)
	recovery, _, err = baseA.ReserveAssociation(context.Background(), recovery)
	if err != nil {
		t.Fatal(err)
	}
	payload := CheckPublication{Name: "FeatureProof validation", HeadOID: snapshot.HeadOID, ExternalID: recovery.ExternalCheckID, Status: "completed", Conclusion: "success", Title: "passed", Summary: "passed"}
	digest, _ := CanonicalDigest(payload)
	if _, _, err = baseA.PrepareIntent(context.Background(), PublicationIntent{AssociationID: recovery.ID, Kind: "check", PayloadSHA256: digest, ExpectedID: recovery.ExternalCheckID, Check: &payload}); err != nil {
		t.Fatal(err)
	}
	storeA := &releaseBarrierStore{Store: baseA, owner: "owner-a", beforeRelease: make(chan struct{}), allowRelease: make(chan struct{}), afterRelease: make(chan struct{})}
	storeB, err := OpenLocalStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	gateway := &blockingRecoveryGitHub{fakeGitHub: &fakeGitHub{snapshot: snapshot, checks: []CheckRecord{{ID: 7, AppID: 10, HeadOID: snapshot.HeadOID, ExternalID: recovery.ExternalCheckID}}}, entered: make(chan struct{}), release: make(chan struct{})}
	clock := &fakeClock{now: time.Unix(100, 0)}
	validationA := &handoffValidation{entered: make(chan struct{}), fail: true}
	validationB := &handoffValidation{entered: make(chan struct{})}
	authorizer := StaticAuthorizer{Config: AuthorizationConfig{Installations: map[int64]map[int64]bool{1: {2: true}}, Actors: map[int64]bool{4: true}, Selections: map[string]Selection{"default": {Alias: "default"}}}}
	dependencies := func(store Store, validation ValidationGateway, owner string) CoordinatorDependencies {
		retry := testRetryPolicy(clock)
		publisher := Publisher{GitHub: gateway, Store: store, Retry: retry, AppID: 10, BotID: 20, MaxPages: 2, Owner: owner, Clock: clock, LeaseTTL: 2 * time.Minute}
		return CoordinatorDependencies{GitHub: gateway, Authorizer: authorizer, Validation: validation, Store: store, Publisher: publisher, Projector: Projector{}, Retry: retry, IDs: &sequenceIDs{}, Clock: clock, Owner: owner, RunTimeout: time.Minute, CloseTimeout: time.Second, LeaseTTL: 2 * time.Minute, PollInterval: time.Millisecond}
	}
	type coordinatorResult struct {
		coordinator *Coordinator
		err         error
	}
	resultA := make(chan coordinatorResult, 1)
	go func() {
		coordinator, startErr := NewCoordinator(context.Background(), dependencies(storeA, validationA, "owner-a"))
		resultA <- coordinatorResult{coordinator: coordinator, err: startErr}
	}()
	waitCtx, cancelWait := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancelWait()
	for _, barrier := range []chan struct{}{validationA.entered, storeA.beforeRelease, gateway.entered} {
		select {
		case <-barrier:
		case <-waitCtx.Done():
			t.Fatal("startup did not reach every controlled pre-release boundary")
		}
	}
	probeAssociations, probeIntents, err := storeB.ClaimPendingExcept(context.Background(), "owner-b", clock.Now(), time.Minute, nil)
	if err != nil || len(probeAssociations) != 0 || len(probeIntents) != 0 {
		t.Fatalf("work was claimable before release: associations=%d intents=%d err=%v", len(probeAssociations), len(probeIntents), err)
	}
	close(storeA.allowRelease)
	select {
	case <-storeA.afterRelease:
	case <-waitCtx.Done():
		t.Fatal("failed worker did not durably release its lease")
	}
	coordinatorB, err := NewCoordinator(context.Background(), dependencies(storeB, validationB, "owner-b"))
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-validationB.entered:
	case <-waitCtx.Done():
		t.Fatal("released work was not handed to the second coordinator")
	}
	close(gateway.release)
	var startedA coordinatorResult
	select {
	case startedA = <-resultA:
	case <-waitCtx.Done():
		t.Fatal("first coordinator did not finish the independent recovery")
	}
	if startedA.err != nil {
		t.Fatal(startedA.err)
	}
	if validationA.submitCount() != 1 || validationB.submitCount() != 1 {
		t.Fatalf("handoffs owner-a=%d owner-b=%d", validationA.submitCount(), validationB.submitCount())
	}
	if err = coordinatorB.Close(waitCtx); err != nil {
		t.Fatal(err)
	}
	if err = startedA.coordinator.Close(waitCtx); err != nil {
		t.Fatal(err)
	}
}
