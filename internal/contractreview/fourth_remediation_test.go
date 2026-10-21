package contractreview

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/asierhr/featureProof/internal/contractcatalog"
)

// TestLocalStoreConcurrentCreateHasOneImmutableWinner proves distinct idempotency keys cannot race the same deterministic review identity.
func TestLocalStoreConcurrentCreateHasOneImmutableWinner(t *testing.T) {
	root := filepath.Join(t.TempDir(), "reviews")
	storeA, err := NewLocalStore(root)
	if err != nil {
		t.Fatal(err)
	}
	defer storeA.Close()
	storeB, err := NewLocalStore(root)
	if err != nil {
		t.Fatal(err)
	}
	defer storeB.Close()
	catalog := fixtureCatalog(t, "a-source", nil, contractcatalog.CompletenessComplete, nil, contractcatalog.OmissionCounts{})
	artifact, err := CreateArtifact(context.Background(), candidateFor(catalog), baselineFor(catalog), reviewLimits())
	if err != nil {
		t.Fatal(err)
	}
	base := ReviewRecord{Artifact: artifact, State: StateCreated, Events: []AuditEvent{{Kind: "review_created", ReviewID: artifact.ID}}}
	start := make(chan struct{})
	errorsByCall := make([]error, 2)
	var wait sync.WaitGroup
	for index, store := range []*LocalStore{storeA, storeB} {
		wait.Add(1)
		go func(index int, store *LocalStore) {
			defer wait.Done()
			<-start
			_, errorsByCall[index] = store.Create(context.Background(), "actor", []string{"key-a", "key-b"}[index], strings.Repeat(string(rune('a'+index)), 64), base.Clone())
		}(index, store)
	}
	close(start)
	wait.Wait()
	winners := 0
	for _, createErr := range errorsByCall {
		if createErr == nil {
			winners++
		} else if errorCategoryOf(createErr) != CategoryConflict {
			t.Fatalf("unexpected create result: %v", createErr)
		}
	}
	if winners != 1 {
		t.Fatalf("immutable creation winners=%d errors=%v", winners, errorsByCall)
	}
	loaded, err := storeA.Load(context.Background(), artifact.ID)
	if err != nil || len(loaded.Idempotency) != 1 {
		t.Fatalf("winner did not leave one complete aggregate: bindings=%d err=%v", len(loaded.Idempotency), err)
	}
}

// TestLocalStoreAllowsRecoveryWhileMutationIOIsPaused proves durable transition reservation replaces process locks around journal and checkpoint I/O.
func TestLocalStoreAllowsRecoveryWhileMutationIOIsPaused(t *testing.T) {
	root := filepath.Join(t.TempDir(), "reviews")
	storeA, err := NewLocalStore(root)
	if err != nil {
		t.Fatal(err)
	}
	defer storeA.Close()
	storeB, err := NewLocalStore(root)
	if err != nil {
		t.Fatal(err)
	}
	defer storeB.Close()
	catalog := fixtureCatalog(t, "a-source", nil, contractcatalog.CompletenessComplete, nil, contractcatalog.OmissionCounts{})
	artifact, err := CreateArtifact(context.Background(), candidateFor(catalog), baselineFor(catalog), reviewLimits())
	if err != nil {
		t.Fatal(err)
	}
	created := ReviewRecord{Artifact: artifact, State: StateCreated, Events: []AuditEvent{{Kind: "review_created", ReviewID: artifact.ID}}}
	created, err = storeA.Create(context.Background(), "actor", "create-no-lock", strings.Repeat("a", 64), created)
	if err != nil {
		t.Fatal(err)
	}
	next := created.Clone()
	next.Revision = 1
	next.Events = append(next.Events, AuditEvent{Kind: "decision_recorded", ReviewID: artifact.ID, Revision: 1})
	started, release := make(chan struct{}), make(chan struct{})
	storeA.checkpoint = func(phase string) {
		if phase == "checkpoint_directory_opened" {
			storeA.checkpoint = nil
			close(started)
			<-release
		}
	}
	result := make(chan error, 1)
	go func() {
		_, commitErr := storeA.CompareAndSwap(context.Background(), "actor", 0, "decide", "decision-no-lock", strings.Repeat("b", 64), next)
		result <- commitErr
	}()
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("mutation did not reach paused checkpoint I/O")
	}
	loaded := make(chan ReviewRecord, 1)
	loadErrors := make(chan error, 1)
	go func() {
		record, loadErr := storeB.Load(context.Background(), artifact.ID)
		if loadErr != nil {
			loadErrors <- loadErr
			return
		}
		loaded <- record
	}()
	select {
	case record := <-loaded:
		if record.Revision != 1 {
			t.Fatalf("recovery exposed revision %d", record.Revision)
		}
	case loadErr := <-loadErrors:
		t.Fatal(loadErr)
	case <-time.After(5 * time.Second):
		t.Fatal("independent recovery blocked behind mutation filesystem I/O")
	}
	close(release)
	if err = <-result; err != nil {
		t.Fatal(err)
	}
}

// TestLocalStoreAwaitReservedReviewObservesAtomicCreate covers bounded recovery of an idempotency winner that is still publishing.
func TestLocalStoreAwaitReservedReviewObservesAtomicCreate(t *testing.T) {
	store, err := NewLocalStore(filepath.Join(t.TempDir(), "reviews"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	catalog := fixtureCatalog(t, "a-source", nil, contractcatalog.CompletenessComplete, nil, contractcatalog.OmissionCounts{})
	artifact, err := CreateArtifact(context.Background(), candidateFor(catalog), baselineFor(catalog), reviewLimits())
	if err != nil {
		t.Fatal(err)
	}
	record := ReviewRecord{Artifact: artifact, State: StateCreated, Events: []AuditEvent{{Kind: "review_created", ReviewID: artifact.ID}}}
	created := make(chan error, 1)
	go func() {
		time.Sleep(10 * time.Millisecond)
		_, createErr := store.Create(context.Background(), "winner", "reserved", strings.Repeat("c", 64), record)
		created <- createErr
	}()
	loaded, err := store.awaitReservedReview(context.Background(), artifact.ID)
	if err != nil || loaded.Artifact.ID != artifact.ID {
		t.Fatalf("reserved create was not observed: id=%s err=%v", loaded.Artifact.ID, err)
	}
	if err = <-created; err != nil {
		t.Fatal(err)
	}
}

// TestDurableTransitionFailsClosedBeforeIO covers cancellation, malformed bounds and foreign revision reservations.
func TestDurableTransitionFailsClosedBeforeIO(t *testing.T) {
	store, err := NewLocalStore(filepath.Join(t.TempDir(), "reviews"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err = store.awaitReservedReview(ctx, strings.Repeat("a", 24)); errorCategoryOf(err) != CategoryCancelled {
		t.Fatalf("cancelled reservation wait returned %v", err)
	}
	if _, err = store.commitTransition(ctx, store.root, durableTransition{}); errorCategoryOf(err) != CategoryCancelled {
		t.Fatalf("cancelled transition returned %v", err)
	}
	invalid := durableTransition{Next: ReviewRecord{Artifact: ReviewArtifact{Limits: Limits{}}}}
	if _, err = store.applyTransition(context.Background(), store.root, invalid); errorCategoryOf(err) != CategoryLimitExceeded {
		t.Fatalf("invalid transition bounds returned %v", err)
	}
	if _, err = store.openStoreDirectory(filepath.Join(store.root, "..", "escape"), false); errorCategoryOf(err) != CategoryInvalidInput {
		t.Fatalf("store escape returned %v", err)
	}
	file, err := openRootDescriptor(store.root)
	if err != nil {
		t.Fatal(err)
	}
	_ = file.Close()
	if _, err = digestOpenFile(file); errorCategoryOf(err) != CategoryUnavailable {
		t.Fatalf("closed descriptor digest returned %v", err)
	}
	directory, err := openRelativeDirectory(store.rootHandle, ".", false)
	if err != nil {
		t.Fatal(err)
	}
	corrupt, err := createExclusiveInDirectory(directory, "creation.json")
	if err != nil {
		t.Fatal(err)
	}
	_, _ = corrupt.Write([]byte("{"))
	_ = corrupt.Close()
	_ = directory.Close()
	valid := ReviewRecord{Artifact: ReviewArtifact{Limits: reviewLimits()}}
	valid.Integrity = recordIntegrity(valid)
	if _, err = store.commitCreation(context.Background(), store.root, durableTransition{Principal: "actor", Operation: "create", Key: "key", RequestDigest: strings.Repeat("d", 64), Next: valid}); errorCategoryOf(err) != CategoryInternal {
		t.Fatalf("corrupt creation intent returned %v", err)
	}
}

// TestLocalStoreCompletesItsOwnExistingReservation covers the exact same-artifact retry branch without subprocess coverage indirection.
func TestLocalStoreCompletesItsOwnExistingReservation(t *testing.T) {
	store, err := NewLocalStore(filepath.Join(t.TempDir(), "reviews"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	catalog := fixtureCatalog(t, "a-source", nil, contractcatalog.CompletenessComplete, nil, contractcatalog.OmissionCounts{})
	artifact, err := CreateArtifact(context.Background(), candidateFor(catalog), baselineFor(catalog), reviewLimits())
	if err != nil {
		t.Fatal(err)
	}
	record := ReviewRecord{Artifact: artifact, State: StateCreated, Events: []AuditEvent{{Kind: "review_created", ReviewID: artifact.ID}}}
	digest := strings.Repeat("d", 64)
	if err = store.saveIdempotency(idempotentResult{"actor", "create", "same", digest, artifact.ID}); err != nil {
		t.Fatal(err)
	}
	created, err := store.Create(context.Background(), "actor", "same", digest, record)
	if err != nil || created.Artifact.ID != artifact.ID || created.Integrity == "" || len(created.Idempotency) != 1 {
		t.Fatalf("same reservation result=%#v err=%v", created, err)
	}
}

// TestPublicationIntentValidationAndCleanup covers immutable duplicate intent, mismatch rejection, absence, and corrupt durable bytes.
func TestPublicationIntentValidationAndCleanup(t *testing.T) {
	root := t.TempDir()
	publisher, err := NewLocalPublisher(root)
	if err != nil {
		t.Fatal(err)
	}
	defer publisher.Close()
	attempt := strings.Repeat("a", 64)
	request := PublishRequest{Target: "contract.json", AttemptID: attempt}
	if _, found, readErr := publisher.readPublicationIntent(attempt); readErr != nil || found {
		t.Fatalf("absent intent found=%v err=%v", found, readErr)
	}
	intent := publicationIntent{AttemptID: attempt, Target: request.Target, OutputDigest: strings.Repeat("b", 64), Temp: ".featureproof-contract-aaaaaaaaaaaa-temp"}
	if err = publisher.writePublicationIntent(intent); err != nil {
		t.Fatal(err)
	}
	if err = publisher.writePublicationIntent(intent); err != nil {
		t.Fatalf("identical intent was not idempotent: %v", err)
	}
	conflicting := intent
	conflicting.Temp = ".featureproof-contract-aaaaaaaaaaaa-other"
	if err = publisher.writePublicationIntent(conflicting); errorCategoryOf(err) != CategoryConflict {
		t.Fatalf("conflicting intent returned %v", err)
	}
	if _, _, err = publisher.recoverAbandonedAttempt(".", request, "different.json", intent.OutputDigest); errorCategoryOf(err) != CategoryConflict {
		t.Fatalf("mismatched intent returned %v", err)
	}
	if err = publisher.removePublicationIntent(attempt); err != nil {
		t.Fatal(err)
	}
	if err = publisher.removePublicationIntent(attempt); err != nil {
		t.Fatalf("absent intent cleanup failed: %v", err)
	}
	recoveryAttempt := strings.Repeat("c", 64)
	recoveryRaw := []byte("recovered")
	recoveryRequest := PublishRequest{Target: "recovered.json", AttemptID: recoveryAttempt}
	missingTemp := publicationIntent{AttemptID: recoveryAttempt, Target: recoveryRequest.Target, OutputDigest: digestBytes(recoveryRaw), Temp: ".featureproof-contract-cccccccccccc-missing"}
	if err = publisher.writePublicationIntent(missingTemp); err != nil {
		t.Fatal(err)
	}
	if _, err = publisher.Publish(context.Background(), recoveryRequest, recoveryRaw, digestBytes(recoveryRaw)); err != nil {
		t.Fatalf("missing intended temp was not recreated: %v", err)
	}
	corrupt := filepath.Join(root, ".featureproof-publication-markers", attempt+".intent.json")
	if err = os.WriteFile(corrupt, []byte("{"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, _, err = publisher.readPublicationIntent(attempt); errorCategoryOf(err) != CategoryInternal {
		t.Fatalf("corrupt intent returned %v", err)
	}
	if err = publisher.removePublicationIntent(attempt); err != nil {
		t.Fatal(err)
	}
	if err = publisher.Close(); err != nil {
		t.Fatal(err)
	}
	if err = publisher.removePublicationIntent(attempt); errorCategoryOf(err) != CategoryUnavailable {
		t.Fatalf("closed publisher intent cleanup returned %v", err)
	}
}
