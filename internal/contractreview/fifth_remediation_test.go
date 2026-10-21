package contractreview

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/asierhr/featureProof/internal/contractcatalog"
)

// readCheckpointRevision reads the raw checkpoint without invoking recovery so cancellation tests can locate the exact durable boundary.
func readCheckpointRevision(t *testing.T, root, reviewID string) uint64 {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(root, reviewID, "checkpoint.json"))
	if err != nil {
		t.Fatal(err)
	}
	var record ReviewRecord
	if err = json.Unmarshal(raw, &record); err != nil {
		t.Fatal(err)
	}
	return record.Revision
}

// TestLocalPublisherOwnershipCancellation releases a cancelled follower and lets a cancelled owner be reclaimed without touching a live temp.
func TestLocalPublisherOwnershipCancellation(t *testing.T) {
	root := t.TempDir()
	publisher, err := NewLocalPublisher(root)
	if err != nil {
		t.Fatal(err)
	}
	defer publisher.Close()
	raw := []byte("approved")
	request := PublishRequest{Target: "contract.json", AttemptID: strings.Repeat("f", 64)}
	ownerReady := make(chan struct{})
	followerWaiting := make(chan struct{})
	releaseOwner := make(chan struct{})
	var ownerOnce, waiterOnce, releaseOnce sync.Once
	defer releaseOnce.Do(func() { close(releaseOwner) })
	publisher.checkpoint = func(phase string) {
		if phase == "intent_synced" {
			ownerOnce.Do(func() {
				close(ownerReady)
				<-releaseOwner
			})
		}
		if phase == "ownership_wait" {
			waiterOnce.Do(func() { close(followerWaiting) })
		}
	}
	ownerResult := make(chan error, 1)
	go func() {
		_, publishErr := publisher.Publish(context.Background(), request, raw, digestBytes(raw))
		ownerResult <- publishErr
	}()
	select {
	case <-ownerReady:
	case <-time.After(3 * time.Second):
		t.Fatal("owner did not reach intent")
	}
	followerCtx, cancelFollower := context.WithCancel(context.Background())
	followerResult := make(chan error, 1)
	go func() {
		_, publishErr := publisher.Publish(followerCtx, request, raw, digestBytes(raw))
		followerResult <- publishErr
	}()
	select {
	case <-followerWaiting:
	case <-time.After(3 * time.Second):
		t.Fatal("follower did not wait")
	}
	cancelFollower()
	select {
	case followerErr := <-followerResult:
		if errorCategoryOf(followerErr) != CategoryCancelled || !errors.Is(followerErr, context.Canceled) {
			t.Fatalf("follower category=%s err=%v", errorCategoryOf(followerErr), followerErr)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("cancelled follower did not terminate")
	}
	releaseOnce.Do(func() { close(releaseOwner) })
	if ownerErr := <-ownerResult; ownerErr != nil {
		t.Fatal(ownerErr)
	}

	cancelledRequest := PublishRequest{Target: "reclaimed.json", AttemptID: strings.Repeat("9", 64)}
	winnerCtx, cancelWinner := context.WithCancel(context.Background())
	var cancelOnce sync.Once
	publisher.checkpoint = func(phase string) {
		if phase == "temp_partial" {
			cancelOnce.Do(cancelWinner)
		}
	}
	_, err = publisher.Publish(winnerCtx, cancelledRequest, raw, digestBytes(raw))
	if errorCategoryOf(err) != CategoryCancelled || !errors.Is(err, context.Canceled) {
		t.Fatalf("winner category=%s err=%v", errorCategoryOf(err), err)
	}
	publisher.checkpoint = nil
	target, err := publisher.Publish(context.Background(), cancelledRequest, raw, digestBytes(raw))
	if err != nil || target != cancelledRequest.Target {
		t.Fatalf("reclaim target=%q err=%v", target, err)
	}
	temps, _ := filepath.Glob(filepath.Join(root, ".featureproof-contract-"+cancelledRequest.AttemptID[:12]+"-*"))
	intents, _ := filepath.Glob(filepath.Join(root, ".featureproof-publication-markers", "*.intent.json"))
	if len(temps) != 0 || len(intents) != 0 {
		t.Fatalf("temps=%v intents=%v", temps, intents)
	}
}

// TestDurableCollisionReconciliation covers identical helper publication, cancellation, exhaustion and private-temp cleanup without timing races.
func TestDurableCollisionReconciliation(t *testing.T) {
	root := t.TempDir()
	store, err := NewLocalStore(root)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	expected := []byte("durable")
	if err = os.WriteFile(filepath.Join(root, "checkpoint.json"), expected, 0600); err != nil {
		t.Fatal(err)
	}
	if err = store.commitReplaceOrObserve(context.Background(), store.rootHandle, root, "missing-temp", "checkpoint.json", expected, len(expected), "store.checkpoint"); err != nil {
		t.Fatalf("identical helper generation failed: %v", err)
	}
	store.wait = func(context.Context, time.Duration) error {
		return typedError(CategoryCancelled, "test.wait", context.Canceled)
	}
	err = store.commitReplaceOrObserve(context.Background(), store.rootHandle, root, "missing-temp", "checkpoint.json", []byte("different"), 32, "store.checkpoint")
	if errorCategoryOf(err) != CategoryCancelled {
		t.Fatalf("collision cancellation category=%s err=%v", errorCategoryOf(err), err)
	}
	store.wait = func(context.Context, time.Duration) error { return nil }
	store.lockTimeout = time.Millisecond
	err = store.commitReplaceOrObserve(context.Background(), store.rootHandle, root, "missing-temp", "absent.json", expected, len(expected), "store.checkpoint")
	if errorCategoryOf(err) != CategoryUnavailable {
		t.Fatalf("collision exhaustion category=%s err=%v", errorCategoryOf(err), err)
	}

	publishRoot := t.TempDir()
	publisher, err := NewLocalPublisher(publishRoot)
	if err != nil {
		t.Fatal(err)
	}
	defer publisher.Close()
	if err = os.WriteFile(filepath.Join(publishRoot, "private-temp"), expected, 0600); err != nil {
		t.Fatal(err)
	}
	if err = publisher.cleanupAttemptTemp(publisher.rootHandle, "private-temp"); err != nil {
		t.Fatal(err)
	}
	if err = publisher.cleanupAttemptTemp(publisher.rootHandle, "private-temp"); err != nil {
		t.Fatal(err)
	}
	if err = releaseAttemptLock(nil); err != nil {
		t.Fatal(err)
	}
	closedDirectory, err := os.Open(publishRoot)
	if err != nil {
		t.Fatal(err)
	}
	_ = closedDirectory.Close()
	if _, acquired, lockErr := tryAttemptLock(closedDirectory, "closed.owner.lock"); lockErr == nil || acquired {
		t.Fatalf("closed directory acquired=%v err=%v", acquired, lockErr)
	}
}

// TestLocalStoreCancellationAfterCreationReservationRecovers proves a cancelled creator stops before journal and a retry completes the durable reservation.
func TestLocalStoreCancellationAfterCreationReservationRecovers(t *testing.T) {
	root := filepath.Join(t.TempDir(), "reviews")
	store, err := NewLocalStore(root)
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
	ctx, cancel := context.WithCancel(context.Background())
	store.checkpoint = func(phase string) {
		if phase == "creation_reserved" {
			cancel()
		}
	}
	if _, err = store.Create(ctx, "actor", "create-cancel", strings.Repeat("a", 64), record); errorCategoryOf(err) != CategoryCancelled {
		t.Fatalf("cancelled creation returned %v", err)
	}
	if _, err = os.Stat(filepath.Join(root, artifact.ID, "events.jsonl")); !os.IsNotExist(err) {
		t.Fatalf("cancelled creation continued to journal: %v", err)
	}
	store.checkpoint = nil
	retryCtx, cancelRetry := context.WithCancel(context.Background())
	store.wait = func(context.Context, time.Duration) error {
		cancelRetry()
		return nil
	}
	if _, err = store.Create(retryCtx, "actor", "create-cancel", strings.Repeat("a", 64), record); errorCategoryOf(err) != CategoryCancelled {
		t.Fatalf("cancelled reservation observation returned %v", err)
	}
	store.wait = func(context.Context, time.Duration) error { return nil }
	created, err := store.Create(context.Background(), "actor", "create-cancel", strings.Repeat("a", 64), record)
	if err != nil || created.Artifact.ID != artifact.ID || created.Integrity == "" {
		t.Fatalf("creation retry did not converge: %#v err=%v", created, err)
	}
}

// TestLocalStoreCancellationAtDurableCASBoundaries proves cancellation stops the next phase and the same transition is safely recoverable.
func TestLocalStoreCancellationAtDurableCASBoundaries(t *testing.T) {
	for _, phase := range []string{"transition_reserved", "journal_encoding", "journal_encoded", "journal_serialized", "journal_written", "journal_synced", "journal_committed", "checkpoint_serializing", "checkpoint_serialized", "checkpoint_directory_opened", "checkpoint_written", "checkpoint_synced"} {
		t.Run(phase, func(t *testing.T) {
			root := filepath.Join(t.TempDir(), "reviews")
			store, err := NewLocalStore(root)
			if err != nil {
				t.Fatal(err)
			}
			defer store.Close()
			catalog := fixtureCatalog(t, "a-source", nil, contractcatalog.CompletenessComplete, nil, contractcatalog.OmissionCounts{})
			artifact, err := CreateArtifact(context.Background(), candidateFor(catalog), baselineFor(catalog), reviewLimits())
			if err != nil {
				t.Fatal(err)
			}
			current := ReviewRecord{Artifact: artifact, State: StateCreated, Events: []AuditEvent{{Kind: "review_created", ReviewID: artifact.ID}}}
			current, err = store.Create(context.Background(), "actor", "create", strings.Repeat("a", 64), current)
			if err != nil {
				t.Fatal(err)
			}
			next := current.Clone()
			next.Revision = 1
			next.Events = append(next.Events, AuditEvent{Kind: "decision_recorded", ReviewID: artifact.ID, Revision: 1})
			ctx, cancel := context.WithCancel(context.Background())
			var once sync.Once
			store.checkpoint = func(observed string) {
				if observed == phase {
					once.Do(cancel)
				}
			}
			if _, err = store.CompareAndSwap(ctx, "actor", 0, "decide", "cancelled", strings.Repeat("b", 64), next); errorCategoryOf(err) != CategoryCancelled || !errors.Is(err, context.Canceled) {
				t.Fatalf("cancel at %s returned %v", phase, err)
			}
			if revision := readCheckpointRevision(t, root, artifact.ID); revision != 0 {
				t.Fatalf("cancel at %s committed checkpoint revision %d", phase, revision)
			}
			store.checkpoint = nil
			store.wait = func(context.Context, time.Duration) error { return nil }
			recovered, retryErr := store.CompareAndSwap(context.Background(), "actor", 0, "decide", "cancelled", strings.Repeat("b", 64), next)
			store.wait = nil
			if retryErr != nil || recovered.Revision != 1 || len(recovered.Idempotency) != 2 {
				t.Fatalf("retry after %s did not converge: revision=%d bindings=%d err=%v", phase, recovered.Revision, len(recovered.Idempotency), retryErr)
			}
		})
	}
}

// TestLocalPublisherConcurrentIdenticalAttemptConverges holds the owner beyond the former timeout and proves the follower only proceeds after OS ownership release.
func TestLocalPublisherConcurrentIdenticalAttemptConverges(t *testing.T) {
	root := t.TempDir()
	publisher, err := NewLocalPublisher(root)
	if err != nil {
		t.Fatal(err)
	}
	defer publisher.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	ownerReady := make(chan struct{})
	followerWaiting := make(chan struct{})
	releaseOwner := make(chan struct{})
	var ownerOnce, waiterOnce, releaseOnce sync.Once
	defer releaseOnce.Do(func() { close(releaseOwner) })
	publisher.checkpoint = func(phase string) {
		if phase == "intent_synced" {
			ownerOnce.Do(func() {
				close(ownerReady)
				<-releaseOwner
			})
		}
		if phase == "ownership_wait" {
			waiterOnce.Do(func() { close(followerWaiting) })
		}
	}
	raw := []byte("approved")
	request := PublishRequest{Target: "contract.json", AttemptID: strings.Repeat("a", 64)}
	type outcome struct {
		target string
		err    error
	}
	results := make(chan outcome, 2)
	go func() {
		target, publishErr := publisher.Publish(ctx, request, raw, digestBytes(raw))
		results <- outcome{target, publishErr}
	}()
	select {
	case <-ownerReady:
	case <-ctx.Done():
		t.Fatal("owner did not reach durable intent")
	}
	go func() {
		target, publishErr := publisher.Publish(ctx, request, raw, digestBytes(raw))
		results <- outcome{target, publishErr}
	}()
	select {
	case <-followerWaiting:
	case <-ctx.Done():
		t.Fatal("follower did not observe live ownership")
	}
	timer := time.NewTimer(350 * time.Millisecond)
	select {
	case early := <-results:
		timer.Stop()
		t.Fatalf("publisher completed while owner was live: %#v", early)
	case <-timer.C:
	}
	releaseOnce.Do(func() { close(releaseOwner) })
	for index := 0; index < 2; index++ {
		select {
		case result := <-results:
			if result.err != nil || result.target != "contract.json" {
				t.Fatalf("result=%#v", result)
			}
		case <-ctx.Done():
			t.Fatal("publishers did not terminate")
		}
	}
	if _, digest, inspectErr := publisher.Inspect(ctx, request.Target, request.AttemptID); inspectErr != nil || digest != digestBytes(raw) {
		t.Fatalf("inspect digest=%s err=%v", digest, inspectErr)
	}
	temps, _ := filepath.Glob(filepath.Join(root, ".featureproof-contract-"+request.AttemptID[:12]+"-*"))
	intents, _ := filepath.Glob(filepath.Join(root, ".featureproof-publication-markers", "*.intent.json"))
	markers, _ := filepath.Glob(filepath.Join(root, ".featureproof-publication-markers", request.AttemptID+".json"))
	if len(temps) != 0 || len(intents) != 0 || len(markers) != 1 {
		t.Fatalf("temps=%v intents=%v markers=%v", temps, intents, markers)
	}
}

// TestServiceConcurrentIdenticalExportsReturnOneReceipt bounds both callers and proves the slow owner and follower converge through the full service transaction.
func TestServiceConcurrentIdenticalExportsReturnOneReceipt(t *testing.T) {
	root := t.TempDir()
	store, err := NewLocalStore(filepath.Join(root, "reviews"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err = os.MkdirAll(filepath.Join(root, "exports"), 0700); err != nil {
		t.Fatal(err)
	}
	publisher, err := NewLocalPublisher(filepath.Join(root, "exports"))
	if err != nil {
		t.Fatal(err)
	}
	defer publisher.Close()
	service, _, _, _ := serviceFixture(t, store)
	service.Publisher = publisher
	view := createServiceReview(t, service)
	approved, err := service.Decide(context.Background(), DecisionCommand{PrincipalID: "actor", ReviewID: view.ID, Decision: DecisionInput{ExpectedRevision: 0, Whole: WholeAccept, IdempotencyKey: "approve"}})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	ownerReady := make(chan struct{})
	followerWaiting := make(chan struct{})
	releaseOwner := make(chan struct{})
	var ownerOnce, waiterOnce, releaseOnce sync.Once
	defer releaseOnce.Do(func() { close(releaseOwner) })
	publisher.checkpoint = func(phase string) {
		if phase == "intent_synced" {
			ownerOnce.Do(func() {
				close(ownerReady)
				<-releaseOwner
			})
		}
		if phase == "ownership_wait" {
			waiterOnce.Do(func() { close(followerWaiting) })
		}
	}
	command := ExportCommand{PrincipalID: "actor", ReviewID: view.ID, IdempotencyKey: "same-export", ExpectedRevision: approved.Revision, Target: PublishRequest{Target: "contract.json"}, Info: ExportInfo{Title: "contract", Version: "1"}}
	type exportOutcome struct {
		receipt ExportReceipt
		err     error
	}
	results := make(chan exportOutcome, 2)
	go func() {
		receipt, exportErr := service.Export(ctx, command)
		results <- exportOutcome{receipt, exportErr}
	}()
	select {
	case <-ownerReady:
	case <-ctx.Done():
		t.Fatal("service owner did not reach durable intent")
	}
	go func() {
		receipt, exportErr := service.Export(ctx, command)
		results <- exportOutcome{receipt, exportErr}
	}()
	select {
	case <-followerWaiting:
	case <-ctx.Done():
		t.Fatal("service follower did not wait on ownership")
	}
	releaseOnce.Do(func() { close(releaseOwner) })
	outcomes := make([]exportOutcome, 0, 2)
	for len(outcomes) < 2 {
		select {
		case result := <-results:
			outcomes = append(outcomes, result)
		case <-ctx.Done():
			t.Fatal("service exports did not terminate")
		}
	}
	if outcomes[0].err != nil || outcomes[1].err != nil || !reflect.DeepEqual(outcomes[0].receipt, outcomes[1].receipt) || outcomes[0].receipt.OutputDigest == "" {
		t.Fatalf("receipts=%#v errors=[%v %v] causes=[%v %v]", []ExportReceipt{outcomes[0].receipt, outcomes[1].receipt}, outcomes[0].err, outcomes[1].err, errors.Unwrap(outcomes[0].err), errors.Unwrap(outcomes[1].err))
	}
	record, err := store.Load(ctx, view.ID)
	if err != nil || record.PendingExport != nil || len(record.Receipts) != 1 || record.Receipts[0] != outcomes[0].receipt {
		t.Fatalf("stored receipts=%#v pending=%#v err=%v", record.Receipts, record.PendingExport, err)
	}
	dir, _ := store.reviewDir(view.ID)
	transition := durableTransition{Principal: "actor", Operation: "export.commit", Key: "same-export", RequestDigest: outcomes[0].receipt.RequestDigest, Next: record}
	observedRecord, observed, observeErr := store.observeReservedTransition(ctx, dir, transition, time.Millisecond)
	if observeErr != nil || !observed || observedRecord.Integrity != record.Integrity {
		t.Fatalf("observed=%v integrity=%s err=%v", observed, observedRecord.Integrity, observeErr)
	}
}

// TestPreparedRecoveryWaitsForConcurrentRename deterministically covers the helper window where another process owns the marker and rename.
func TestPreparedRecoveryWaitsForConcurrentRename(t *testing.T) {
	root := t.TempDir()
	publisher, err := NewLocalPublisher(root)
	if err != nil {
		t.Fatal(err)
	}
	defer publisher.Close()
	raw := []byte("approved")
	expected := digestBytes(raw)
	waits := 0
	publisher.wait = func(context.Context, time.Duration) error {
		waits++
		if waits == 1 {
			return os.WriteFile(filepath.Join(root, "contract.json"), raw, 0600)
		}
		return nil
	}
	committed, err := publisher.awaitCommittedTarget(context.Background(), publisher.rootHandle, "contract.json", expected)
	if err != nil || !committed || waits != 1 {
		t.Fatalf("committed=%v waits=%d err=%v", committed, waits, err)
	}

	marker := publicationMarker{AttemptID: strings.Repeat("a", 64), Target: "other.json", OutputDigest: expected, Temp: "missing-temp"}
	publisher.wait = func(context.Context, time.Duration) error {
		return typedError(CategoryCancelled, "test.wait", context.Canceled)
	}
	_, err = publisher.recoverPrepared(context.Background(), publisher.rootHandle, root, filepath.Join(root, "other.json"), "other.json", expected, marker)
	if errorCategoryOf(err) != CategoryCancelled {
		t.Fatalf("missing temp recovery category=%s err=%v", errorCategoryOf(err), err)
	}
	publisher.visibilityWait = time.Millisecond
	publisher.wait = func(context.Context, time.Duration) error { return nil }
	committed, err = publisher.awaitCommittedTarget(context.Background(), publisher.rootHandle, "absent.json", expected)
	if err != nil || committed {
		t.Fatalf("absent target committed=%v err=%v", committed, err)
	}
}

// TestPreparedRecoveryCommitsBoundTemp exercises the durable-marker helper's descriptor-relative success path.
func TestPreparedRecoveryCommitsBoundTemp(t *testing.T) {
	root := t.TempDir()
	publisher, err := NewLocalPublisher(root)
	if err != nil {
		t.Fatal(err)
	}
	defer publisher.Close()
	raw := []byte("approved")
	tempName := ".featureproof-contract-recovery"
	if err = os.WriteFile(filepath.Join(root, tempName), raw, 0600); err != nil {
		t.Fatal(err)
	}
	marker := publicationMarker{AttemptID: strings.Repeat("b", 64), Target: "contract.json", OutputDigest: digestBytes(raw), Temp: tempName}
	committed, err := publisher.recoverPrepared(context.Background(), publisher.rootHandle, root, filepath.Join(root, "contract.json"), "contract.json", digestBytes(raw), marker)
	if err != nil || committed != "contract.json" {
		t.Fatalf("committed=%q err=%v", committed, err)
	}
	stored, readErr := os.ReadFile(filepath.Join(root, "contract.json"))
	if readErr != nil || !reflect.DeepEqual(stored, raw) {
		t.Fatalf("stored=%q err=%v", stored, readErr)
	}
	committed, err = publisher.recoverPrepared(context.Background(), publisher.rootHandle, root, filepath.Join(root, "contract.json"), "contract.json", digestBytes(raw), marker)
	if err != nil || committed != "contract.json" {
		t.Fatalf("idempotent recovery committed=%q err=%v", committed, err)
	}
	invalidMarker := marker
	invalidMarker.Target = "different.json"
	if _, err = publisher.recoverPrepared(context.Background(), publisher.rootHandle, root, filepath.Join(root, "contract.json"), "contract.json", digestBytes(raw), invalidMarker); errorCategoryOf(err) != CategoryConflict {
		t.Fatalf("invalid marker category=%s err=%v", errorCategoryOf(err), err)
	}
	replacement := []byte("replacement")
	replacementTemp := ".featureproof-contract-replacement"
	if err = os.WriteFile(filepath.Join(root, replacementTemp), replacement, 0600); err != nil {
		t.Fatal(err)
	}
	replaceMarker := publicationMarker{AttemptID: strings.Repeat("c", 64), Target: "contract.json", OutputDigest: digestBytes(replacement), Temp: replacementTemp, Replace: true, PreviousDigest: digestBytes(raw)}
	committed, err = publisher.recoverPrepared(context.Background(), publisher.rootHandle, root, filepath.Join(root, "contract.json"), "contract.json", digestBytes(replacement), replaceMarker)
	if err != nil || committed != "contract.json" {
		t.Fatalf("replacement recovery committed=%q err=%v", committed, err)
	}
	other := []byte("other")
	existingMarker := publicationMarker{AttemptID: strings.Repeat("d", 64), Target: "contract.json", OutputDigest: digestBytes(other), Temp: "missing-temp"}
	if _, err = publisher.recoverPrepared(context.Background(), publisher.rootHandle, root, filepath.Join(root, "contract.json"), "contract.json", digestBytes(other), existingMarker); errorCategoryOf(err) != CategoryTargetExists {
		t.Fatalf("existing target category=%s err=%v", errorCategoryOf(err), err)
	}
	if err = os.WriteFile(filepath.Join(root, "bad-temp"), []byte("wrong"), 0600); err != nil {
		t.Fatal(err)
	}
	badTempMarker := publicationMarker{AttemptID: strings.Repeat("e", 64), Target: "new.json", OutputDigest: digestBytes(other), Temp: "bad-temp"}
	if _, err = publisher.recoverPrepared(context.Background(), publisher.rootHandle, root, filepath.Join(root, "new.json"), "new.json", digestBytes(other), badTempMarker); errorCategoryOf(err) != CategoryConflict {
		t.Fatalf("bad temp category=%s err=%v", errorCategoryOf(err), err)
	}
	escapeMarker := badTempMarker
	escapeMarker.Temp = "../escape"
	if _, err = publisher.recoverPrepared(context.Background(), publisher.rootHandle, root, filepath.Join(root, "new.json"), "new.json", digestBytes(other), escapeMarker); errorCategoryOf(err) != CategoryConflict {
		t.Fatalf("escaped temp category=%s err=%v", errorCategoryOf(err), err)
	}
	if _, _, err = publisher.Inspect(context.Background(), "missing.json", ""); errorCategoryOf(err) != CategoryInvalidInput {
		t.Fatalf("missing inspect category=%s err=%v", errorCategoryOf(err), err)
	}
	if _, _, err = publisher.Inspect(context.Background(), "contract.json", "invalid-attempt"); errorCategoryOf(err) != CategoryConflict {
		t.Fatalf("marker inspect category=%s err=%v", errorCategoryOf(err), err)
	}
	if _, _, err = publisher.Inspect(context.Background(), "../escape", ""); errorCategoryOf(err) != CategoryInvalidInput {
		t.Fatalf("escaped inspect category=%s err=%v", errorCategoryOf(err), err)
	}
}
