package githubpr

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"
)

// advancingRecoveryGitHub coordinates two startup supervisors while logical time advances inside each recovery call.
type advancingRecoveryGitHub struct {
	*fakeGitHub
	clock           *fakeClock
	store           Store
	firstStarted    chan struct{}
	releaseFirst    chan struct{}
	mu              sync.Mutex
	calls           map[string]int
	associations    map[string]string
	first           bool
	expiredObserved bool
}

// FindChecks advances recovery time, verifies the active lease, and returns the one pre-existing owned effect.
func (g *advancingRecoveryGitHub) FindChecks(ctx context.Context, _ int64, _ int64, headOID, externalID string, _ int) ([]CheckRecord, error) {
	g.clock.Advance(2 * time.Second)
	association, err := g.store.GetAssociation(ctx, g.associations[externalID])
	if err != nil || !association.LeaseUntil.After(g.clock.Now()) {
		g.mu.Lock()
		g.expiredObserved = true
		g.mu.Unlock()
	}
	g.mu.Lock()
	g.calls[externalID]++
	first := !g.first
	g.first = true
	g.mu.Unlock()
	if first {
		close(g.firstStarted)
		select {
		case <-g.releaseFirst:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	return []CheckRecord{{ID: int64(len(externalID)) + 1, AppID: 10, HeadOID: headOID, ExternalID: externalID}}, nil
}

// Advance moves the deterministic test clock without introducing wall-clock sleeps.
func (f *fakeClock) Advance(duration time.Duration) {
	f.mu.Lock()
	f.now = f.now.Add(duration)
	f.mu.Unlock()
}

// TestCoordinatorRecoversEachBoundedBatchBeforeClaimingMore closes REV-22 with two concurrent startup supervisors.
func TestCoordinatorRecoversEachBoundedBatchBeforeClaimingMore(t *testing.T) {
	dir := t.TempDir()
	storeA, err := OpenLocalStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	const total = 4
	associationByExternalID := make(map[string]string, total)
	for index := 0; index < total; index++ {
		id := fmt.Sprintf("association-%02d", index)
		association, _ := NewAssociation(id, fmt.Sprintf("delivery-%02d", index), "default", 4, testSnapshot(t))
		association = bindTestAssociation(t, association, fmt.Sprintf("run-%02d", index))
		association.Lifecycle = "terminal"
		association, _ = SealAssociation(association)
		association, _, err = storeA.ReserveAssociation(context.Background(), association)
		if err != nil {
			t.Fatal(err)
		}
		payload := CheckPublication{Name: "FeatureProof validation", HeadOID: association.Snapshot.HeadOID, ExternalID: association.ExternalCheckID, Status: "completed", Conclusion: "failure", Title: "failed", Summary: strings.Repeat("x", 300_000)}
		digest, _ := CanonicalDigest(payload)
		if _, _, err = storeA.PrepareIntent(context.Background(), PublicationIntent{AssociationID: id, Kind: "check", PayloadSHA256: digest, ExpectedID: association.ExternalCheckID, Check: &payload}); err != nil {
			t.Fatal(err)
		}
		associationByExternalID[association.ExternalCheckID] = id
	}
	storeB, err := OpenLocalStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	clock := &fakeClock{now: time.Unix(100, 0)}
	gateway := &advancingRecoveryGitHub{fakeGitHub: &fakeGitHub{snapshot: testSnapshot(t)}, clock: clock, store: storeA, firstStarted: make(chan struct{}), releaseFirst: make(chan struct{}), calls: map[string]int{}, associations: associationByExternalID}
	retry := RetryPolicy{Config: RetryConfig{MaxAttempts: 1, MaxDuration: 10 * time.Second, MaxWait: 0, InitialBackoff: time.Millisecond, MaxBackoff: time.Millisecond}, Clock: clock, Jitter: fixedJitter{}}
	dependencies := func(store Store, owner string) CoordinatorDependencies {
		publisher := Publisher{GitHub: gateway, Store: store, Retry: retry, AppID: 10, BotID: 20, MaxPages: 2, Owner: owner, Clock: clock, LeaseTTL: 20 * time.Second}
		return CoordinatorDependencies{GitHub: gateway, Authorizer: StaticAuthorizer{Config: AuthorizationConfig{Installations: map[int64]map[int64]bool{1: {2: true}}, Actors: map[int64]bool{4: true}, Selections: map[string]Selection{"default": {Alias: "default"}}}}, Validation: &fakeValidation{}, Store: store, Publisher: publisher, Projector: Projector{}, Retry: retry, IDs: &sequenceIDs{}, Clock: clock, Owner: owner, RunTimeout: 10 * time.Second, CloseTimeout: 10 * time.Second, LeaseTTL: 20 * time.Second, PollInterval: time.Millisecond}
	}
	type startupResult struct {
		coordinator *Coordinator
		err         error
	}
	resultA := make(chan startupResult, 1)
	go func() {
		coordinator, startupErr := NewCoordinator(context.Background(), dependencies(storeA, "owner-a"))
		resultA <- startupResult{coordinator: coordinator, err: startupErr}
	}()
	waitCtx, cancelWait := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancelWait()
	select {
	case <-gateway.firstStarted:
	case <-waitCtx.Done():
		t.Fatal("first recovery did not reach the controlled boundary")
	}
	coordinatorB, err := NewCoordinator(context.Background(), dependencies(storeB, "owner-b"))
	if err != nil {
		t.Fatal(err)
	}
	close(gateway.releaseFirst)
	var startupA startupResult
	select {
	case startupA = <-resultA:
	case <-waitCtx.Done():
		t.Fatal("first coordinator did not finish startup")
	}
	if startupA.err != nil {
		t.Fatal(startupA.err)
	}
	if err = coordinatorB.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err = startupA.coordinator.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	gateway.mu.Lock()
	if len(gateway.calls) != total {
		t.Fatalf("recovered effects=%d", len(gateway.calls))
	}
	if gateway.expiredObserved {
		t.Fatal("a recovery crossed its freshly claimed lease")
	}
	for externalID, calls := range gateway.calls {
		if calls != 1 {
			t.Fatalf("effect %s recovered %d times", externalID, calls)
		}
	}
	gateway.mu.Unlock()
	reopened, err := OpenLocalStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	events, err := reopened.Events(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	outstandingByOwner := map[string]bool{}
	claimBatches := 0
	for _, event := range events {
		if event.Claims != nil {
			claimBatches++
			if len(event.Claims.Associations) > 1 {
				t.Fatalf("unbounded associations in one startup batch: %d", len(event.Claims.Associations))
			}
			owner := ""
			if len(event.Claims.Associations) == 1 {
				owner = event.Claims.Associations[0].Owner
			} else if len(event.Claims.Intents) > 0 {
				owner = event.Claims.Intents[0].Owner
			}
			if outstandingByOwner[owner] {
				t.Fatalf("owner %s claimed another batch before confirming the prior recovery", owner)
			}
			outstandingByOwner[owner] = len(event.Claims.Intents) > 0
		}
		if event.Kind == "publication_confirmed" && event.Intent != nil {
			outstandingByOwner[event.Intent.Owner] = false
		}
	}
	if claimBatches < 2 || outstandingByOwner["owner-a"] || outstandingByOwner["owner-b"] {
		t.Fatalf("claim batches=%d outstanding=%v", claimBatches, outstandingByOwner)
	}
	remainingAssociations, remainingIntents, err := reopened.ClaimPending(context.Background(), "owner-c", clock.Now(), time.Second)
	if err != nil || len(remainingAssociations) != 0 || len(remainingIntents) != 0 {
		t.Fatalf("remaining associations=%d intents=%d err=%v", len(remainingAssociations), len(remainingIntents), err)
	}
}

// TestCoordinatorRecoversIntentFromItsAlreadyOwnedLiveAssociation covers restart after the association claim committed before its intent claim.
func TestCoordinatorRecoversIntentFromItsAlreadyOwnedLiveAssociation(t *testing.T) {
	store := testStore(t)
	clock := &fakeClock{now: time.Unix(100, 0)}
	association, _ := NewAssociation("association", "delivery", "default", 4, testSnapshot(t))
	association = bindTestAssociation(t, association, "run")
	association.Lifecycle = "terminal"
	association.Owner, association.LeaseUntil = "owner", clock.Now().Add(2*time.Minute)
	association, _ = SealAssociation(association)
	association, _, err := store.ReserveAssociation(context.Background(), association)
	if err != nil {
		t.Fatal(err)
	}
	payload := CheckPublication{Name: "FeatureProof validation", HeadOID: association.Snapshot.HeadOID, ExternalID: association.ExternalCheckID, Status: "completed", Conclusion: "failure", Title: "failed", Summary: "failed"}
	digest, _ := CanonicalDigest(payload)
	if _, _, err = store.PrepareIntent(context.Background(), PublicationIntent{AssociationID: association.ID, Kind: "check", PayloadSHA256: digest, ExpectedID: association.ExternalCheckID, Check: &payload}); err != nil {
		t.Fatal(err)
	}
	gateway := &fakeGitHub{snapshot: association.Snapshot, checks: []CheckRecord{{ID: 7, AppID: 10, HeadOID: association.Snapshot.HeadOID, ExternalID: association.ExternalCheckID}}}
	retry := testRetryPolicy(clock)
	publisher := Publisher{GitHub: gateway, Store: store, Retry: retry, AppID: 10, BotID: 20, MaxPages: 2, Owner: "owner", Clock: clock, LeaseTTL: 2 * time.Minute}
	coordinator, err := NewCoordinator(context.Background(), CoordinatorDependencies{GitHub: gateway, Authorizer: StaticAuthorizer{Config: AuthorizationConfig{Installations: map[int64]map[int64]bool{1: {2: true}}, Actors: map[int64]bool{4: true}, Selections: map[string]Selection{"default": {Alias: "default"}}}}, Validation: &fakeValidation{}, Store: store, Publisher: publisher, Projector: Projector{}, Retry: retry, IDs: &sequenceIDs{}, Clock: clock, Owner: "owner", RunTimeout: time.Minute, CloseTimeout: time.Second, LeaseTTL: 2 * time.Minute, PollInterval: time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	if err = coordinator.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	remainingAssociations, remainingIntents, err := store.ClaimPending(context.Background(), "other", clock.Now(), time.Minute)
	if err != nil || len(remainingAssociations) != 0 || len(remainingIntents) != 0 {
		t.Fatalf("remaining associations=%d intents=%d err=%v", len(remainingAssociations), len(remainingIntents), err)
	}
}
