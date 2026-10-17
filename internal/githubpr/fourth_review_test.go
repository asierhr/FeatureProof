package githubpr

import (
	"context"
	"sync"
	"testing"
	"time"
)

// deadlineSubmitValidation blocks submit until cancellation and records whether the run budget already exists.
type deadlineSubmitValidation struct {
	mu       sync.Mutex
	submits  int
	entered  chan struct{}
	finished chan struct{}
	deadline chan time.Time
	state    RunState
}

// Submit records the inherited run deadline and remains joinable through context cancellation.
func (v *deadlineSubmitValidation) Submit(ctx context.Context, _ Selection, snapshot PullRequestSnapshot, _ string) (RunState, error) {
	v.mu.Lock()
	v.submits++
	v.mu.Unlock()
	if deadline, ok := ctx.Deadline(); ok && v.deadline != nil {
		v.deadline <- deadline
	}
	if v.entered != nil {
		select {
		case v.entered <- struct{}{}:
		default:
		}
	}
	if v.finished == nil {
		state := v.state
		state.RepositoryID, state.HeadOID = snapshot.RepositoryID, snapshot.HeadOID
		if state.BindingRunID == "" {
			state.BindingRunID = state.PackageRunID
		}
		return state, nil
	}
	<-ctx.Done()
	close(v.finished)
	return RunState{}, ctx.Err()
}

// ResolveExisting is unused by the submit lease regression and fails closed.
func (*deadlineSubmitValidation) ResolveExisting(context.Context, ExistingRunLookup) (RunState, error) {
	return RunState{}, &Error{Code: CodeInternal, Operation: "unused_existing"}
}

// Status returns the configured terminal state for the recovery owner.
func (v *deadlineSubmitValidation) Status(context.Context, string) (RunState, error) {
	return v.state, nil
}

// Cancel is an idempotent no-op for the controlled submit regression.
func (*deadlineSubmitValidation) Cancel(context.Context, string, string) error { return nil }

// submitCount reads the synchronized number of calls observed by one validation gateway.
func (v *deadlineSubmitValidation) submitCount() int {
	v.mu.Lock()
	defer v.mu.Unlock()
	return v.submits
}

// coordinatorTestDependencies builds the same bounded lease and authorization policy for independent process fixtures.
func coordinatorTestDependencies(gateway PullRequestGateway, validation ValidationGateway, store Store, clock Clock, owner string) CoordinatorDependencies {
	authorizer := StaticAuthorizer{Config: AuthorizationConfig{Installations: map[int64]map[int64]bool{1: {2: true}}, Actors: map[int64]bool{4: true}, Selections: map[string]Selection{"default": {Alias: "default"}}, Forks: map[int64]map[int64]bool{}}}
	return CoordinatorDependencies{GitHub: gateway, Authorizer: authorizer, Validation: validation, Store: store, Publisher: testPublisher(gateway, store, 0), Projector: Projector{}, Retry: testRetryPolicy(clock), IDs: &sequenceIDs{}, Clock: clock, Owner: owner, RunTimeout: time.Minute, CloseTimeout: time.Second, LeaseTTL: 2 * time.Minute, PollInterval: time.Millisecond}
}

// TestSubmitBudgetPrecedesExternalCallAndLeaseExcludesSecondCoordinator closes REV-18 with two journal processes.
func TestSubmitBudgetPrecedesExternalCallAndLeaseExcludesSecondCoordinator(t *testing.T) {
	dir := t.TempDir()
	storeA, err := OpenLocalStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	storeB, err := OpenLocalStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	association, _ := NewAssociation("association", "delivery", "default", 4, testSnapshot(t))
	if _, _, err = storeA.ReserveAssociation(context.Background(), association); err != nil {
		t.Fatal(err)
	}
	clock := &fakeClock{now: time.Unix(100, 0)}
	gateway := &fakeGitHub{snapshot: association.Snapshot}
	validationA := &deadlineSubmitValidation{entered: make(chan struct{}, 1), finished: make(chan struct{}), deadline: make(chan time.Time, 1)}
	coordinatorA, err := NewCoordinator(context.Background(), coordinatorTestDependencies(gateway, validationA, storeA, clock, "owner-a"))
	if err != nil {
		t.Fatal(err)
	}
	<-validationA.entered
	select {
	case deadline := <-validationA.deadline:
		if !deadline.After(time.Now()) {
			t.Fatal("submit received an expired run deadline")
		}
	default:
		t.Fatal("submit began without the run deadline")
	}
	validationB := &deadlineSubmitValidation{entered: make(chan struct{}, 1), state: RunState{RunHandle: "run-b", PackageRunID: "package-b", BindingSHA256: "binding-b", Outcome: "passed"}}
	coordinatorB, err := NewCoordinator(context.Background(), coordinatorTestDependencies(gateway, validationB, storeB, clock, "owner-b"))
	if err != nil {
		t.Fatal(err)
	}
	if validationB.submitCount() != 0 {
		t.Fatal("second coordinator entered submit under the live lease")
	}
	closeCtx, closeCancel := context.WithTimeout(context.Background(), time.Second)
	defer closeCancel()
	if err = coordinatorA.Close(closeCtx); err != nil {
		t.Fatal(err)
	}
	select {
	case <-validationA.finished:
	default:
		t.Fatal("cancel did not join the blocked submit")
	}
	if err = coordinatorB.Close(closeCtx); err != nil {
		t.Fatal(err)
	}
	coordinatorB, err = NewCoordinator(context.Background(), coordinatorTestDependencies(gateway, validationB, storeB, clock, "owner-b"))
	if err != nil {
		t.Fatal(err)
	}
	<-validationB.entered
	if validationB.submitCount() != 1 {
		t.Fatalf("post-release submits=%d", validationB.submitCount())
	}
	if err = coordinatorB.Close(closeCtx); err != nil {
		t.Fatal(err)
	}
}

// TestRecoveryFailureReleasesEveryStartupClaim closes REV-19 after one successful and one invalid intent.
func TestRecoveryFailureReleasesEveryStartupClaim(t *testing.T) {
	store := testStore(t)
	snapshot := testSnapshot(t)
	makeAssociation := func(id, delivery, run string, terminal bool) Association {
		t.Helper()
		association, _ := NewAssociation(id, delivery, "default", 4, snapshot)
		if run != "" {
			association = bindTestAssociation(t, association, run)
		}
		if terminal {
			association.Lifecycle = "terminal"
			association, _ = SealAssociation(association)
		}
		association, _, _ = store.ReserveAssociation(context.Background(), association)
		return association
	}
	first := makeAssociation("a-first", "delivery-a", "run-a", true)
	second := makeAssociation("b-invalid", "delivery-b", "run-b", true)
	unrelated := makeAssociation("c-unrelated", "delivery-c", "", false)
	valid := CheckPublication{Name: "FeatureProof validation", HeadOID: snapshot.HeadOID, ExternalID: first.ExternalCheckID, Status: "completed", Conclusion: "success", Title: "passed", Summary: "safe"}
	validDigest, _ := CanonicalDigest(valid)
	invalid := CheckPublication{Name: "Foreign check", HeadOID: snapshot.HeadOID, ExternalID: second.ExternalCheckID, Status: "completed", Conclusion: "success", Title: "passed", Summary: "safe"}
	invalidDigest, _ := CanonicalDigest(invalid)
	old := time.Unix(10, 0)
	if _, _, err := store.PrepareIntent(context.Background(), PublicationIntent{AssociationID: first.ID, Kind: "check", PayloadSHA256: validDigest, ExpectedID: first.ExternalCheckID, Check: &valid, Owner: "crashed", LeaseStartedAt: old, LeaseUntil: old.Add(time.Second)}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.PrepareIntent(context.Background(), PublicationIntent{AssociationID: second.ID, Kind: "check", PayloadSHA256: invalidDigest, ExpectedID: second.ExternalCheckID, Check: &invalid, Owner: "crashed", LeaseStartedAt: old, LeaseUntil: old.Add(time.Second)}); err != nil {
		t.Fatal(err)
	}
	gateway := &fakeGitHub{snapshot: snapshot, checks: []CheckRecord{{ID: 7, AppID: 10, HeadOID: snapshot.HeadOID, ExternalID: first.ExternalCheckID}}}
	clock := &fakeClock{now: time.Unix(20, 0)}
	deps := coordinatorTestDependencies(gateway, &fakeValidation{}, store, clock, "recovery")
	deps.Publisher = testPublisher(gateway, store, 0)
	deps.Publisher.Owner = "recovery"
	if _, err := NewCoordinator(context.Background(), deps); !IsCode(err, CodeIntegrity) {
		t.Fatalf("recovery err=%v", err)
	}
	for _, association := range []Association{first, second, unrelated} {
		got, getErr := store.GetAssociation(context.Background(), association.ID)
		if getErr != nil || got.Owner != "" || !got.LeaseUntil.IsZero() {
			t.Fatalf("association %s remained claimed: %#v err=%v", association.ID, got, getErr)
		}
	}
	events, err := store.Events(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	latestOwner := "missing"
	for _, event := range events {
		if event.Intent != nil && event.Intent.AssociationID == second.ID && event.Intent.Kind == "check" {
			latestOwner = event.Intent.Owner
		}
	}
	if latestOwner != "" {
		t.Fatalf("invalid intent remained claimed by %q", latestOwner)
	}
	_, reclaimed, err := store.ClaimPending(context.Background(), "next-owner", clock.Now(), time.Minute)
	if err != nil || len(reclaimed) != 1 || reclaimed[0].AssociationID != second.ID {
		t.Fatalf("released intent was not immediately reclaimable: %#v err=%v", reclaimed, err)
	}
	if err = store.ReleaseIntent(context.Background(), second.ID, "check", "foreign", clock.Now()); !IsCode(err, CodeConflict) {
		t.Fatalf("foreign release err=%v", err)
	}
	if err = store.ReleaseIntent(context.Background(), second.ID, "check", "next-owner", clock.Now()); err != nil {
		t.Fatal(err)
	}
	if err = store.ReleaseIntent(context.Background(), "missing", "check", "next-owner", clock.Now()); err != ErrNotFound {
		t.Fatalf("missing release err=%v", err)
	}
}

// TestRecoverIntentUsesExactDurablePayloadAcrossConfigurationChange closes REV-20 for present and absent effects.
func TestRecoverIntentUsesExactDurablePayloadAcrossConfigurationChange(t *testing.T) {
	for _, remotePresent := range []bool{false, true} {
		t.Run(map[bool]string{false: "absent", true: "present"}[remotePresent], func(t *testing.T) {
			store := testStore(t)
			association, _ := NewAssociation("association", "delivery", "default", 4, testSnapshot(t))
			association = bindTestAssociation(t, association, "run")
			association.Owner, association.LeaseUntil = "runtime", time.Unix(100, 0).Add(2*time.Minute)
			association, _ = SealAssociation(association)
			association, _, _ = store.ReserveAssociation(context.Background(), association)
			check := CheckPublication{Name: "FeatureProof validation", HeadOID: association.Snapshot.HeadOID, ExternalID: association.ExternalCheckID, Status: "completed", Conclusion: "success", Title: "original", Summary: "original", DetailsURL: "https://old.example/runs/1"}
			checkDigest, _ := CanonicalDigest(check)
			comment := CommentPublication{Body: SummaryMarker + "\noriginal"}
			commentDigest, _ := CanonicalDigest(comment)
			preparedCheck, _, err := store.PrepareIntent(context.Background(), PublicationIntent{AssociationID: association.ID, Kind: "check", PayloadSHA256: checkDigest, ExpectedID: association.ExternalCheckID, Check: &check, Owner: "runtime", LeaseStartedAt: time.Unix(100, 0), LeaseUntil: time.Unix(100, 0).Add(2 * time.Minute)})
			if err != nil {
				t.Fatal(err)
			}
			preparedComment, _, err := store.PrepareIntent(context.Background(), PublicationIntent{AssociationID: association.ID, Kind: "comment", PayloadSHA256: commentDigest, ExpectedID: "20", Comment: &comment, Owner: "runtime", LeaseStartedAt: time.Unix(100, 0), LeaseUntil: time.Unix(100, 0).Add(2 * time.Minute)})
			if err != nil {
				t.Fatal(err)
			}
			gateway := &fakeGitHub{snapshot: association.Snapshot}
			if remotePresent {
				gateway.checks = []CheckRecord{{ID: 7, AppID: 10, HeadOID: association.Snapshot.HeadOID, ExternalID: association.ExternalCheckID}}
				gateway.comments = []CommentRecord{{ID: 8, AuthorID: 20, Body: comment.Body}}
			}
			publisher := testPublisher(gateway, store, 20)
			publisher.Owner, publisher.DetailsURL = "runtime", "https://new.example/runs/1"
			association, err = publisher.RecoverIntent(context.Background(), association, preparedCheck)
			if err != nil {
				t.Fatal(err)
			}
			association, err = publisher.RecoverIntent(context.Background(), association, preparedComment)
			if err != nil {
				t.Fatal(err)
			}
			if remotePresent {
				if len(gateway.checkPayloads) != 0 || len(gateway.commentPayloads) != 0 {
					t.Fatalf("present effects mutated: checks=%#v comments=%#v", gateway.checkPayloads, gateway.commentPayloads)
				}
				return
			}
			if len(gateway.checkPayloads) != 1 || gateway.checkPayloads[0] != check || len(gateway.commentPayloads) != 1 || gateway.commentPayloads[0] != comment {
				t.Fatalf("recovered payloads checks=%#v comments=%#v", gateway.checkPayloads, gateway.commentPayloads)
			}
		})
	}
}
