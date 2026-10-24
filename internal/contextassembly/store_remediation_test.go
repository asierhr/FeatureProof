package contextassembly

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/asierhr/featureProof/internal/projectmemory"
)

// reconciliationWitness injects response-loss and read ambiguity around one otherwise linearizable authority.
type reconciliationWitness struct {
	base      *testTailWitness
	mu        sync.Mutex
	mode      string
	readError bool
}

// blockingCASWitness pauses one external commit so tests can prove confirmed reads never wait behind writer I/O.
type blockingCASWitness struct {
	base    *testTailWitness
	entered chan struct{}
	release chan struct{}
	mu      sync.Mutex
	blocked bool
}

// blockingReadWitness holds constructor authority reads until the caller context is cancelled.
type blockingReadWitness struct{ entered chan struct{} }

// threeWriterWitness pauses the first and third CAS calls so one winner can advance between two deterministic contenders.
type threeWriterWitness struct {
	base         *testTailWitness
	calls        atomic.Int32
	firstEntered chan struct{}
	firstRelease chan struct{}
	thirdEntered chan struct{}
	thirdRelease chan struct{}
}

// Read delegates authoritative reconciliation without coupling it to paused CAS calls.
func (w *threeWriterWitness) Read(ctx context.Context, stream string) (projectmemory.WitnessState, error) {
	return w.base.Read(ctx, stream)
}

// Bootstrap delegates initialization to the composition-owned witness.
func (w *threeWriterWitness) Bootstrap(ctx context.Context, stream string) (projectmemory.WitnessState, error) {
	return w.base.Bootstrap(ctx, stream)
}

// CompareAndAdvance exposes exact first/third publication boundaries before delegating the authoritative CAS.
func (w *threeWriterWitness) CompareAndAdvance(ctx context.Context, expected, next projectmemory.WitnessState) error {
	switch w.calls.Add(1) {
	case 1:
		close(w.firstEntered)
		select {
		case <-w.firstRelease:
		case <-ctx.Done():
			return ctx.Err()
		}
	case 3:
		close(w.thirdEntered)
		select {
		case <-w.thirdRelease:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return w.base.CompareAndAdvance(ctx, expected, next)
}

// Read waits only on the supplied caller context and signals that the boundary was reached.
func (w blockingReadWitness) Read(ctx context.Context, _ string) (projectmemory.WitnessState, error) {
	select {
	case w.entered <- struct{}{}:
	default:
	}
	<-ctx.Done()
	return projectmemory.WitnessState{}, ctx.Err()
}

// Bootstrap is unavailable because constructor cancellation must stop before initialization.
func (blockingReadWitness) Bootstrap(context.Context, string) (projectmemory.WitnessState, error) {
	return projectmemory.WitnessState{}, errors.New("unexpected bootstrap")
}

// CompareAndAdvance is unavailable because constructor cancellation must stop before publication.
func (blockingReadWitness) CompareAndAdvance(context.Context, projectmemory.WitnessState, projectmemory.WitnessState) error {
	return errors.New("unexpected compare and advance")
}

// Read delegates current authority reads without waiting for the blocked writer.
func (w *blockingCASWitness) Read(ctx context.Context, stream string) (projectmemory.WitnessState, error) {
	return w.base.Read(ctx, stream)
}

// Bootstrap delegates initialization to the underlying witness.
func (w *blockingCASWitness) Bootstrap(ctx context.Context, stream string) (projectmemory.WitnessState, error) {
	return w.base.Bootstrap(ctx, stream)
}

// CompareAndAdvance blocks at the witness boundary until the test releases it or the caller cancels.
func (w *blockingCASWitness) CompareAndAdvance(ctx context.Context, expected, next projectmemory.WitnessState) error {
	w.mu.Lock()
	shouldBlock := !w.blocked
	w.blocked = true
	w.mu.Unlock()
	if !shouldBlock {
		return w.base.CompareAndAdvance(ctx, expected, next)
	}
	select {
	case w.entered <- struct{}{}:
	default:
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-w.release:
		return w.base.CompareAndAdvance(ctx, expected, next)
	}
}

// Read returns the durable authority state unless the test is exercising an unavailable reconciliation read.
func (w *reconciliationWitness) Read(ctx context.Context, stream string) (projectmemory.WitnessState, error) {
	w.mu.Lock()
	fail := w.readError
	w.mu.Unlock()
	if fail {
		return projectmemory.WitnessState{}, errors.New("ambiguous witness read")
	}
	return w.base.Read(ctx, stream)
}

// Bootstrap delegates initialization to the underlying authority.
func (w *reconciliationWitness) Bootstrap(ctx context.Context, stream string) (projectmemory.WitnessState, error) {
	return w.base.Bootstrap(ctx, stream)
}

// CompareAndAdvance injects a failure before or after the durable compare-and-advance commit point.
func (w *reconciliationWitness) CompareAndAdvance(ctx context.Context, expected, next projectmemory.WitnessState) error {
	w.mu.Lock()
	mode := w.mode
	w.mu.Unlock()
	if mode == "before" {
		return errors.New("failure before cas")
	}
	if err := w.base.CompareAndAdvance(ctx, expected, next); err != nil {
		return err
	}
	if mode == "after" {
		return errors.New("response lost after commit")
	}
	return nil
}

// setMode changes only the next reconciliation behavior after initial store composition has completed.
func (w *reconciliationWitness) setMode(mode string, readError bool) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.mode, w.readError = mode, readError
}

// durableAssembly creates one fully committed snapshot and its private store for corruption tests.
func durableAssembly(t *testing.T) (*LocalStore, Snapshot) {
	t.Helper()
	store, err := newWitnessedStore(filepath.Join(t.TempDir(), "store"))
	if err != nil {
		t.Fatal(err)
	}
	assembler, _ := testAssembler(t, store, testProducer(t, SourceConstraints, testItem("constraint", SourceConstraints, "journal-canary")), testProducer(t, SourceContract, testItem("contract", SourceContract, "x")), testProducer(t, SourceConfig, func() Item {
		item := testItem("config", SourceConfig, "x")
		item.Authority, item.State = AuthorityApproved, "approved"
		return item
	}()))
	snapshot, err := assembler.Assemble(context.Background(), testRequest(SourceConstraints, SourceContract, SourceConfig))
	if err != nil {
		t.Fatal(err)
	}
	return store, snapshot
}

// TestBlockedWitnessDoesNotBlockConfirmedReadsAndConcurrentWritersConverge proves an unrelated writer can win while one witness call is blocked and all writers later converge.
func TestBlockedWitnessDoesNotBlockConfirmedReadsAndConcurrentWritersConverge(t *testing.T) {
	store, first := durableAssembly(t)
	base := store.witness.(*testTailWitness)
	blocked := &blockingCASWitness{base: base, entered: make(chan struct{}, 1), release: make(chan struct{})}
	store.witness = blocked
	secondAssembler, _ := testAssembler(t, store, testProducer(t, SourceConstraints, testItem("constraint-blocked", SourceConstraints, "blocked")), testProducer(t, SourceContract, testItem("contract-blocked", SourceContract, "x")), testProducer(t, SourceConfig, func() Item {
		item := testItem("config-blocked", SourceConfig, "x")
		item.Authority, item.State = AuthorityApproved, "approved"
		return item
	}()))
	secondRequest := testRequest(SourceConstraints, SourceContract, SourceConfig)
	secondRequest.IdempotencyKey = "blocked-writer"
	secondDone := make(chan error, 1)
	go func() {
		_, err := secondAssembler.Assemble(context.Background(), secondRequest)
		secondDone <- err
	}()
	select {
	case <-blocked.entered:
	case <-time.After(time.Second):
		t.Fatal("writer did not reach witness")
	}
	readCtx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	loaded, err := store.LoadSnapshot(readCtx, first.Digest)
	cancel()
	if err != nil || loaded.Digest != first.Digest {
		t.Fatalf("confirmed read waited behind writer: digest=%s err=%v", loaded.Digest, err)
	}
	contender, _ := testAssembler(t, store, testProducer(t, SourceConstraints, testItem("constraint-cancelled-writer", SourceConstraints, "x")), testProducer(t, SourceContract, testItem("contract-cancelled-writer", SourceContract, "x")), testProducer(t, SourceConfig, func() Item {
		item := testItem("config-cancelled-writer", SourceConfig, "x")
		item.Authority, item.State = AuthorityApproved, "approved"
		return item
	}()))
	contenderRequest := testRequest(SourceConstraints, SourceContract, SourceConfig)
	contenderRequest.IdempotencyKey = "cancelled-contending-writer"
	contenderCtx, cancelContender := context.WithTimeout(context.Background(), time.Second)
	contenderSnapshot, contenderErr := contender.Assemble(contenderCtx, contenderRequest)
	cancelContender()
	if contenderErr != nil || contenderSnapshot.Digest == "" {
		t.Fatalf("independent writer did not progress while witness call was blocked: digest=%s err=%v", contenderSnapshot.Digest, contenderErr)
	}
	close(blocked.release)
	if err = <-secondDone; err != nil {
		t.Fatalf("blocked writer did not finish: %v", err)
	}
	store.witness = base
	var wait sync.WaitGroup
	errorsOut := make(chan error, 2)
	for index := 0; index < 2; index++ {
		index := index
		wait.Add(1)
		go func() {
			defer wait.Done()
			assembler, _ := testAssembler(t, store, testProducer(t, SourceConstraints, testItem(fmt.Sprintf("constraint-concurrent-%d", index), SourceConstraints, "x")), testProducer(t, SourceContract, testItem(fmt.Sprintf("contract-concurrent-%d", index), SourceContract, "x")), testProducer(t, SourceConfig, func() Item {
				item := testItem(fmt.Sprintf("config-concurrent-%d", index), SourceConfig, "x")
				item.Authority, item.State = AuthorityApproved, "approved"
				return item
			}()))
			request := testRequest(SourceConstraints, SourceContract, SourceConfig)
			request.IdempotencyKey = fmt.Sprintf("concurrent-writer-%d", index)
			_, assembleErr := assembler.Assemble(context.Background(), request)
			errorsOut <- assembleErr
		}()
	}
	wait.Wait()
	close(errorsOut)
	for writeErr := range errorsOut {
		if writeErr != nil {
			t.Fatalf("concurrent writer failed: %v", writeErr)
		}
	}
}

// TestPrecommitObjectsRemainUnaddressable proves snapshots and bindings cannot be loaded until their witness-selected receipt is published.
func TestPrecommitObjectsRemainUnaddressable(t *testing.T) {
	store, first := durableAssembly(t)
	base := store.witness.(*testTailWitness)
	blockedSnapshot := &blockingCASWitness{base: base, entered: make(chan struct{}, 1), release: make(chan struct{})}
	store.witness = blockedSnapshot
	assembler, _ := testAssembler(t, store, testProducer(t, SourceConstraints, testItem("precommit-constraint", SourceConstraints, "new")), testProducer(t, SourceContract, testItem("precommit-contract", SourceContract, "new")), testProducer(t, SourceConfig, func() Item {
		item := testItem("precommit-config", SourceConfig, "new")
		item.Authority, item.State = AuthorityApproved, "approved"
		return item
	}()))
	request := testRequest(SourceConstraints, SourceContract, SourceConfig)
	request.IdempotencyKey = "precommit-snapshot"
	snapshotDone := make(chan error, 1)
	go func() {
		_, err := assembler.Assemble(context.Background(), request)
		snapshotDone <- err
	}()
	<-blockedSnapshot.entered
	objectNames, err := store.listJSON("snapshots")
	if err != nil {
		t.Fatal(err)
	}
	var unconfirmedDigest string
	for _, name := range objectNames {
		digest := stringsTrimSuffix(name, ".json")
		if digest != first.Digest {
			unconfirmedDigest = digest
		}
	}
	if unconfirmedDigest == "" {
		t.Fatal("precommit snapshot was not staged")
	}
	if _, err = store.LoadSnapshot(context.Background(), unconfirmedDigest); !IsCategory(err, CategoryPersistence) {
		t.Fatalf("precommit snapshot became addressable: %v", err)
	}
	close(blockedSnapshot.release)
	if err = <-snapshotDone; err != nil {
		t.Fatal(err)
	}

	binding := ContextBinding{Version: BindingVersion, RunID: first.RunID, Role: first.Role, PlanID: first.PlanID, PreparedPlanDigest: strings.Repeat("c", 64), ContextDigest: first.Digest, Authority: first.Authority, SourceBindings: append([]SourceBinding(nil), first.SourceBindings...), Registry: cloneRegistryDescriptor(first.Registry)}
	binding.Digest, err = bindingDigest(binding)
	if err != nil {
		t.Fatal(err)
	}
	blockedBinding := &blockingCASWitness{base: base, entered: make(chan struct{}, 1), release: make(chan struct{})}
	store.witness = blockedBinding
	bindingDone := make(chan error, 1)
	go func() {
		_, _, commitErr := store.CommitBinding(context.Background(), "precommit-binding", binding)
		bindingDone <- commitErr
	}()
	<-blockedBinding.entered
	if _, err = store.LoadBinding(context.Background(), binding.Digest); !IsCategory(err, CategoryPersistence) {
		t.Fatalf("precommit binding became addressable: %v", err)
	}
	close(blockedBinding.release)
	if err = <-bindingDone; err != nil {
		t.Fatal(err)
	}
	if _, err = store.LoadBinding(context.Background(), binding.Digest); err != nil {
		t.Fatalf("confirmed binding remained hidden: %v", err)
	}
}

// TestConcurrentSameIdentityConverges proves duplicate owners publish one object, journal entry and receipt when their CAS calls overlap.
func TestConcurrentSameIdentityConverges(t *testing.T) {
	root := filepath.Join(t.TempDir(), "store")
	store, base := newTestLocalStore(t, root)
	blocked := &blockingCASWitness{base: base, entered: make(chan struct{}, 1), release: make(chan struct{})}
	store.witness = blocked
	producers := []Producer{testProducer(t, SourceConstraints, testItem("same-constraint", SourceConstraints, "same")), testProducer(t, SourceContract, testItem("same-contract", SourceContract, "same")), testProducer(t, SourceConfig, func() Item {
		item := testItem("same-config", SourceConfig, "same")
		item.Authority, item.State = AuthorityApproved, "approved"
		return item
	}())}
	request := testRequest(SourceConstraints, SourceContract, SourceConfig)
	request.IdempotencyKey = "same-identity"
	results := make(chan error, 2)
	firstAssembler, _ := testAssembler(t, store, producers...)
	go func() {
		_, err := firstAssembler.Assemble(context.Background(), request)
		results <- err
	}()
	<-blocked.entered
	secondAssembler, _ := testAssembler(t, store, producers...)
	go func() {
		_, err := secondAssembler.Assemble(context.Background(), request)
		results <- err
	}()
	select {
	case err := <-results:
		if err != nil {
			t.Fatalf("duplicate contender failed: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("duplicate contender did not progress")
	}
	close(blocked.release)
	if err := <-results; err != nil {
		t.Fatalf("blocked duplicate did not converge: %v", err)
	}
	for _, directory := range []string{"snapshots", "journal", "receipts"} {
		entries, err := os.ReadDir(filepath.Join(root, directory))
		if err != nil || len(entries) != 1 {
			t.Fatalf("same identity duplicated %s: entries=%d err=%v", directory, len(entries), err)
		}
	}
}

// runSharedObjectInterleaving coordinates two losers around one winner and inserts the third staging reference after the first cleanup boundary.
func runSharedObjectInterleaving(t *testing.T, store *LocalStore, base *testTailWitness, directory, digest string, commit func(context.Context, string) error) {
	t.Helper()
	coordinator := &threeWriterWitness{base: base, firstEntered: make(chan struct{}), firstRelease: make(chan struct{}), thirdEntered: make(chan struct{}), thirdRelease: make(chan struct{})}
	store.witness = coordinator
	winnerAdvanced := make(chan struct{})
	winnerRelease := make(chan struct{})
	var advanceOnce sync.Once
	base.afterAdvance = func() {
		advanceOnce.Do(func() {
			close(winnerAdvanced)
			<-winnerRelease
		})
	}
	creatorRetained := make(chan struct{})
	var retentionOnce sync.Once
	store.boundaryHook = func(stage string) error {
		if stage == "retain_object" {
			retentionOnce.Do(func() { close(creatorRetained) })
		}
		return nil
	}
	results := make(chan error, 3)
	go func() { results <- commit(context.Background(), "shared-creator") }()
	<-coordinator.firstEntered
	go func() { results <- commit(context.Background(), "shared-winner") }()
	<-winnerAdvanced
	close(coordinator.firstRelease)
	<-creatorRetained
	go func() { results <- commit(context.Background(), "shared-third") }()
	<-coordinator.thirdEntered
	staged, err := os.ReadDir(filepath.Join(store.root, "staging"))
	if err != nil || len(staged) != 2 {
		t.Fatalf("third writer was not staged beside the winner: entries=%d err=%v", len(staged), err)
	}
	if _, err := os.Stat(filepath.Join(store.root, directory, digest+".json")); err != nil {
		t.Fatalf("retained cleanup removed the third writer's shared object: %v", err)
	}
	close(coordinator.thirdRelease)
	close(winnerRelease)
	for index := 0; index < 3; index++ {
		if err := <-results; err != nil {
			t.Fatalf("shared object commit failed: %v", err)
		}
	}
	store.boundaryHook = nil
	base.mu.Lock()
	base.afterAdvance = nil
	base.mu.Unlock()
	objects, err := os.ReadDir(filepath.Join(store.root, directory))
	if err != nil || len(objects) != 1 {
		t.Fatalf("shared object did not converge: objects=%d err=%v", len(objects), err)
	}
}

// TestSharedDigestWritersPreserveWinnerObject covers three-writer snapshot and binding interleavings without transactional object deletion.
func TestSharedDigestWritersPreserveWinnerObject(t *testing.T) {
	t.Run("snapshot", func(t *testing.T) {
		store, base := newTestLocalStore(t, filepath.Join(t.TempDir(), "snapshot-store"))
		memory := NewMemoryStore()
		assembler, _ := testAssembler(t, memory, testProducer(t, SourceConstraints, testItem("shared-constraint", SourceConstraints, "shared")), testProducer(t, SourceContract, testItem("shared-contract", SourceContract, "shared")), testProducer(t, SourceConfig, func() Item {
			item := testItem("shared-config", SourceConfig, "shared")
			item.Authority, item.State = AuthorityApproved, "approved"
			return item
		}()))
		request := testRequest(SourceConstraints, SourceContract, SourceConfig)
		snapshot, err := assembler.Assemble(context.Background(), request)
		if err != nil {
			t.Fatal(err)
		}
		runSharedObjectInterleaving(t, store, base, "snapshots", snapshot.Digest, func(ctx context.Context, key string) error {
			_, _, commitErr := store.CommitSnapshot(ctx, key, request, snapshot)
			return commitErr
		})
		if _, err = store.LoadSnapshot(context.Background(), snapshot.Digest); err != nil {
			t.Fatalf("shared snapshot unavailable: %v", err)
		}
	})
	t.Run("binding", func(t *testing.T) {
		store, base := newTestLocalStore(t, filepath.Join(t.TempDir(), "binding-store"))
		binding := ContextBinding{Version: BindingVersion, RunID: "shared-run", Role: RolePlanner, PlanID: "shared-plan", PreparedPlanDigest: strings.Repeat("a", 64), ContextDigest: strings.Repeat("b", 64), Authority: testAuthority()}
		binding.Digest, _ = bindingDigest(binding)
		runSharedObjectInterleaving(t, store, base, "bindings", binding.Digest, func(ctx context.Context, key string) error {
			_, _, commitErr := store.CommitBinding(ctx, key, binding)
			return commitErr
		})
		if _, err := store.LoadBinding(context.Background(), binding.Digest); err != nil {
			t.Fatalf("shared binding unavailable: %v", err)
		}
	})
}

// TestLocalStoreRejectsAlreadyCancelledConstruction classifies cancellation before filesystem validation or witness work.
func TestLocalStoreRejectsAlreadyCancelledConstruction(t *testing.T) {
	cause := errors.New("authorized stop sentinel")
	ctx, cancel := context.WithCancelCause(context.Background())
	cancel(cause)
	_, err := NewLocalStore(ctx, "relative-invalid-root", "cancelled-open", blockingReadWitness{entered: make(chan struct{}, 1)})
	if !IsCategory(err, CategoryCancelled) || !errors.Is(err, cause) || strings.Contains(err.Error(), cause.Error()) {
		t.Fatalf("already-cancelled constructor=%v", err)
	}
}

// TestLocalStoreConstructionClassifiesRealDeadline preserves the standard deadline sentinel and reports timeout separately from cancellation.
func TestLocalStoreConstructionClassifiesRealDeadline(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	_, err := NewLocalStore(ctx, filepath.Join(t.TempDir(), "deadline-open"), "deadline-open", blockingReadWitness{entered: make(chan struct{}, 1)})
	if !IsCategory(err, CategoryTimeout) || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("deadline constructor=%v", err)
	}
}

// TestReceiptRecoveryRejectsNonTailAndWitnessMismatch proves missing receipts are repaired only for the exact externally confirmed tail.
func TestReceiptRecoveryRejectsNonTailAndWitnessMismatch(t *testing.T) {
	t.Run("non-tail", func(t *testing.T) {
		store, first := durableAssembly(t)
		assembler, _ := testAssembler(t, store, testProducer(t, SourceConstraints, testItem("later-constraint", SourceConstraints, "later")), testProducer(t, SourceContract, testItem("later-contract", SourceContract, "later")), testProducer(t, SourceConfig, func() Item {
			item := testItem("later-config", SourceConfig, "later")
			item.Authority, item.State = AuthorityApproved, "approved"
			return item
		}()))
		laterRequest := testRequest(SourceConstraints, SourceContract, SourceConfig)
		laterRequest.IdempotencyKey = "later-receipt"
		if _, err := assembler.Assemble(context.Background(), laterRequest); err != nil {
			t.Fatal(err)
		}
		firstReceipt := filepath.Join(store.root, "receipts", receiptFileName("snapshot", hashText("assembly-key")))
		if err := os.Remove(firstReceipt); err != nil {
			t.Fatal(err)
		}
		request := testRequest(SourceConstraints, SourceContract, SourceConfig)
		profiles, _ := DefaultProfiles()
		planner, _ := profiles.Profile(RolePlanner)
		request.Limits, _ = effectiveBudgets(request.Limits, planner.Budgets)
		if _, _, err := store.CommitSnapshot(context.Background(), request.IdempotencyKey, request, first); !IsCategory(err, CategoryPersistence) {
			t.Fatalf("non-tail receipt was reconstructed: %v", err)
		}
	})
	t.Run("witness-mismatch", func(t *testing.T) {
		store, snapshot := durableAssembly(t)
		receipts, _ := filepath.Glob(filepath.Join(store.root, "receipts", "*.json"))
		if len(receipts) != 1 || os.Remove(receipts[0]) != nil {
			t.Fatal("receipt setup failed")
		}
		witness := store.witness.(*testTailWitness)
		witness.mu.Lock()
		witness.states[store.witnessStream] = InitialWitnessState(store.witnessStream)
		witness.mu.Unlock()
		request := testRequest(SourceConstraints, SourceContract, SourceConfig)
		profiles, _ := DefaultProfiles()
		planner, _ := profiles.Profile(RolePlanner)
		request.Limits, _ = effectiveBudgets(request.Limits, planner.Budgets)
		if _, _, err := store.CommitSnapshot(context.Background(), request.IdempotencyKey, request, snapshot); !IsCategory(err, CategoryUncertain) {
			t.Fatalf("witness mismatch repaired receipt: %v", err)
		}
	})
}

// TestCompetingWinnerWaitHonorsCancellation proves retry coordination waits without a lock and terminates with the caller category.
func TestCompetingWinnerWaitHonorsCancellation(t *testing.T) {
	store, _ := newTestLocalStore(t, filepath.Join(t.TempDir(), "store"))
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	target := InitialWitnessState(store.witnessStream)
	target.Generation = 9
	if err := store.waitForWitnessState(ctx, target); !IsCategory(err, CategoryCancelled) || !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled winner wait=%v", err)
	}
}

// TestUnconfirmedCleanupPreservesReferencedObject proves transactional cleanup retains immutable bytes for authoritative garbage collection.
func TestUnconfirmedCleanupPreservesReferencedObject(t *testing.T) {
	store, snapshot := durableAssembly(t)
	if err := store.retainContentObject("snapshots", snapshot.Digest); err != nil {
		t.Fatal(err)
	}
	if _, err := store.LoadSnapshot(context.Background(), snapshot.Digest); err != nil {
		t.Fatalf("confirmed object was removed by losing cleanup: %v", err)
	}
}

// TestPrivateTemporaryGrammarFailsClosed proves only exact live store temporaries are retried and that persistent or forged names block metadata scans.
func TestPrivateTemporaryGrammarFailsClosed(t *testing.T) {
	if !storeTemporaryName(".context-0123456789abcdef.tmp") {
		t.Fatal("exact private temporary name was not recognized")
	}
	if storeTemporaryName(".context-0123456789abcdeG.tmp") {
		t.Fatal("forged private temporary name was accepted")
	}

	store, _ := newTestLocalStore(t, filepath.Join(t.TempDir(), "store"))
	if _, _, err := store.listJSONAttempt("missing"); err == nil {
		t.Fatalf("missing store directory was not rejected: %v", err)
	}
	name := ".context-0123456789abcdef.tmp"
	if err := os.WriteFile(filepath.Join(store.root, "staging", name), []byte("in-flight"), 0o600); err != nil {
		t.Fatal(err)
	}
	started := time.Now()
	if _, err := store.listJSON("staging"); !IsCategory(err, CategoryPersistence) {
		t.Fatalf("persistent private temporary did not fail closed: %v", err)
	}
	if time.Since(started) < witnessReconcileTimeout {
		t.Fatal("persistent private temporary bypassed bounded reconciliation")
	}
}

// TestRestartDiscardsUnconfirmedStaging proves a crash before CAS cannot make a staged snapshot addressable after reopening.
func TestRestartDiscardsUnconfirmedStaging(t *testing.T) {
	root := filepath.Join(t.TempDir(), "store")
	store, witness := newTestLocalStore(t, root)
	memory := NewMemoryStore()
	assembler, _ := testAssembler(t, memory, testProducer(t, SourceConstraints, testItem("restart-constraint", SourceConstraints, "restart")), testProducer(t, SourceContract, testItem("restart-contract", SourceContract, "restart")), testProducer(t, SourceConfig, func() Item {
		item := testItem("restart-config", SourceConfig, "restart")
		item.Authority, item.State = AuthorityApproved, "approved"
		return item
	}()))
	request := testRequest(SourceConstraints, SourceContract, SourceConfig)
	request.IdempotencyKey = "restart-precommit"
	snapshot, err := assembler.Assemble(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(snapshot)
	if err = store.atomicWrite(context.Background(), filepath.Join(root, "snapshots", snapshot.Digest+".json"), raw); err != nil {
		t.Fatal(err)
	}
	_, tail, err := store.validateJournal(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err = store.stageJournalCandidate(context.Background(), tail, "snapshot", hashText(request.IdempotencyKey), strings.Repeat("d", 64), snapshot.Digest); err != nil {
		t.Fatal(err)
	}
	reopened, err := NewLocalStore(context.Background(), root, store.witnessStream, witness)
	if err != nil {
		t.Fatalf("precommit restart did not roll back: %v", err)
	}
	if _, err = reopened.LoadSnapshot(context.Background(), snapshot.Digest); !IsCategory(err, CategoryPersistence) {
		t.Fatalf("rolled-back snapshot became addressable: %v", err)
	}
	for directory, expected := range map[string]int{"snapshots": 1, "staging": 0, "journal": 0, "receipts": 0} {
		entries, readErr := os.ReadDir(filepath.Join(root, directory))
		if readErr != nil || len(entries) != expected {
			t.Fatalf("restart retention mismatch for %s: entries=%d expected=%d err=%v", directory, len(entries), expected, readErr)
		}
	}
}

// TestRestartRejectsConfirmedCandidateWithoutObject proves witness confirmation cannot repair a missing staged payload.
func TestRestartRejectsConfirmedCandidateWithoutObject(t *testing.T) {
	root := filepath.Join(t.TempDir(), "store")
	store, witness := newTestLocalStore(t, root)
	_, tail, err := store.validateJournal(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	entry, _, err := store.stageJournalCandidate(context.Background(), tail, "snapshot", strings.Repeat("a", 64), strings.Repeat("b", 64), strings.Repeat("c", 64))
	if err != nil {
		t.Fatal(err)
	}
	value := receipt{Kind: entry.Kind, KeyDigest: entry.KeyDigest, RequestDigest: entry.RequestDigest, ObjectDigest: entry.ObjectDigest, JournalDigest: entry.Digest}
	next, err := store.localWitnessState(context.Background(), entry, &value)
	if err != nil {
		t.Fatal(err)
	}
	witness.mu.Lock()
	witness.states[store.witnessStream] = next
	witness.mu.Unlock()
	if _, err = NewLocalStore(context.Background(), root, store.witnessStream, witness); !IsCategory(err, CategoryPersistence) {
		t.Fatalf("confirmed candidate without object reopened: %v", err)
	}
}

// TestStoreEntryBoundariesPreserveCancellationAndLayoutFailure proves every public store operation rejects stale caller or filesystem authority before publication.
func TestStoreEntryBoundariesPreserveCancellationAndLayoutFailure(t *testing.T) {
	if _, err := NewLocalStore(nil, "ignored", "ignored", nil); !IsCategory(err, CategoryInvalid) {
		t.Fatalf("nil context accepted: %v", err)
	}
	if _, err := NewLocalStore(context.Background(), filepath.Join(t.TempDir(), "store"), "valid-stream", nil); !IsCategory(err, CategoryInvalid) {
		t.Fatalf("nil witness accepted: %v", err)
	}
	store, snapshot := durableAssembly(t)
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := store.LoadSnapshot(cancelled, snapshot.Digest); !IsCategory(err, CategoryCancelled) {
		t.Fatalf("snapshot load ignored cancellation: %v", err)
	}
	if _, err := store.LoadBinding(cancelled, strings.Repeat("a", 64)); !IsCategory(err, CategoryCancelled) {
		t.Fatalf("binding load ignored cancellation: %v", err)
	}
	request := testRequest(SourceConstraints, SourceContract, SourceConfig)
	if _, _, err := store.CommitSnapshot(cancelled, "cancelled-commit", request, snapshot); !IsCategory(err, CategoryCancelled) {
		t.Fatalf("snapshot commit ignored cancellation: %v", err)
	}
	binding := ContextBinding{Digest: strings.Repeat("b", 64), ContextDigest: snapshot.Digest}
	if _, _, err := store.CommitBinding(cancelled, "cancelled-binding", binding); !IsCategory(err, CategoryCancelled) {
		t.Fatalf("binding commit ignored cancellation: %v", err)
	}
	if err := os.Rename(filepath.Join(store.root, "staging"), filepath.Join(store.root, "staging-away")); err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.CommitSnapshot(context.Background(), "layout-snapshot", request, snapshot); !IsCategory(err, CategoryPersistence) {
		t.Fatalf("snapshot commit accepted replaced layout: %v", err)
	}
	if _, _, err := store.CommitBinding(context.Background(), "layout-binding", binding); !IsCategory(err, CategoryPersistence) {
		t.Fatalf("binding commit accepted replaced layout: %v", err)
	}
	if _, err := store.LoadSnapshot(context.Background(), snapshot.Digest); !IsCategory(err, CategoryPersistence) {
		t.Fatalf("snapshot load accepted replaced layout: %v", err)
	}
	if _, err := store.LoadBinding(context.Background(), binding.Digest); !IsCategory(err, CategoryPersistence) {
		t.Fatalf("binding load accepted replaced layout: %v", err)
	}
}

// TestLocalStoreConstructionPropagatesCallerCancellationToWitnessRead proves startup never detaches HTTP authority work.
func TestLocalStoreConstructionPropagatesCallerCancellationToWitnessRead(t *testing.T) {
	entered := make(chan struct{}, 1)
	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan error, 1)
	root := filepath.Join(t.TempDir(), "cancelled-open")
	go func() {
		_, err := NewLocalStore(ctx, root, "cancelled-open", blockingReadWitness{entered: entered})
		result <- err
	}()
	select {
	case <-entered:
	case <-time.After(time.Second):
		cancel()
		t.Fatal("store did not reach witness read")
	}
	cancel()
	select {
	case err := <-result:
		if !IsCategory(err, CategoryCancelled) || !errors.Is(err, context.Canceled) {
			t.Fatalf("cancelled constructor=%v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("cancelled witness constructor leaked")
	}
}

// TestLocalStoreStrictCanonicalLoads rejects alternate JSON spellings even when typed values are equivalent.
func TestLocalStoreStrictCanonicalLoads(t *testing.T) {
	mutations := []struct {
		name   string
		mutate func([]byte) []byte
	}{
		{name: "whitespace", mutate: func(raw []byte) []byte { return append([]byte(" "), raw...) }},
		{name: "unknown", mutate: func(raw []byte) []byte {
			return bytes.Replace(raw, []byte(`{"version":`), []byte(`{"unknown":true,"version":`), 1)
		}},
		{name: "duplicate", mutate: func(raw []byte) []byte {
			return bytes.Replace(raw, []byte(`{"version":`), []byte(`{"version":"`+SnapshotVersion+`","version":`), 1)
		}},
		{name: "truncated", mutate: func(raw []byte) []byte { return raw[:len(raw)/2] }},
	}
	for _, test := range mutations {
		t.Run(test.name, func(t *testing.T) {
			store, snapshot := durableAssembly(t)
			path := filepath.Join(store.root, "snapshots", snapshot.Digest+".json")
			raw, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if err = os.WriteFile(path, test.mutate(raw), 0o600); err != nil {
				t.Fatal(err)
			}
			if _, err = store.LoadSnapshot(context.Background(), snapshot.Digest); !IsCategory(err, CategoryPersistence) {
				t.Fatalf("non-canonical object accepted: %v", err)
			}
		})
	}
}

// TestJournalChainDetectsTamperingRollbackAndContentLeak authenticates append-only metadata across reopen.
func TestJournalChainDetectsTamperingRollbackAndContentLeak(t *testing.T) {
	store, snapshot := durableAssembly(t)
	assembler, _ := testAssembler(t, store, testProducer(t, SourceConstraints, testItem("constraint-2", SourceConstraints, "other")), testProducer(t, SourceContract, testItem("contract", SourceContract, "x")), testProducer(t, SourceConfig, func() Item {
		item := testItem("config", SourceConfig, "x")
		item.Authority, item.State = AuthorityApproved, "approved"
		return item
	}()))
	request := testRequest(SourceConstraints, SourceContract, SourceConfig)
	request.IdempotencyKey = "second-journal-entry"
	if _, err := assembler.Assemble(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	paths, _ := filepath.Glob(filepath.Join(store.root, "journal", "*.json"))
	if len(paths) != 2 {
		t.Fatalf("journal entries=%d", len(paths))
	}
	for _, path := range paths {
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if bytes.Contains(raw, []byte("journal-canary")) || bytes.Contains(raw, []byte(`"sections"`)) {
			t.Fatalf("journal contains source content: %s", raw)
		}
	}
	if err := os.Remove(paths[1]); err != nil {
		t.Fatal(err)
	}
	if _, err := newWitnessedStore(store.root); !IsCategory(err, CategoryPersistence) {
		t.Fatalf("journal rollback accepted: %v snapshot=%s", err, snapshot.Digest)
	}

	reordered, _ := durableAssembly(t)
	journalPaths, _ := filepath.Glob(filepath.Join(reordered.root, "journal", "*.json"))
	raw, _ := os.ReadFile(journalPaths[0])
	var entry journalEntry
	if err := json.Unmarshal(raw, &entry); err != nil {
		t.Fatal(err)
	}
	entry.Sequence = 2
	entry.Digest, _ = journalEntryDigest(entry)
	changed, _ := json.Marshal(entry)
	if err := os.Remove(journalPaths[0]); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(reordered.root, "journal", entry.Digest+".json"), changed, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := newWitnessedStore(reordered.root); !IsCategory(err, CategoryPersistence) {
		t.Fatalf("reordered journal accepted: %v", err)
	}
}

// TestJournalStrictSchemaRejectsAlternateRepresentations covers authenticated-log truncation and schema attacks.
func TestJournalStrictSchemaRejectsAlternateRepresentations(t *testing.T) {
	mutations := []struct {
		name   string
		mutate func([]byte) []byte
	}{
		{name: "unknown", mutate: func(raw []byte) []byte {
			return bytes.Replace(raw, []byte(`{"Version":`), []byte(`{"Unknown":true,"Version":`), 1)
		}},
		{name: "duplicate", mutate: func(raw []byte) []byte {
			return bytes.Replace(raw, []byte(`{"Version":`), []byte(`{"Version":"context-journal/v1","Version":`), 1)
		}},
		{name: "truncated", mutate: func(raw []byte) []byte { return raw[:len(raw)-1] }},
	}
	for _, test := range mutations {
		t.Run(test.name, func(t *testing.T) {
			store, _ := durableAssembly(t)
			paths, _ := filepath.Glob(filepath.Join(store.root, "journal", "*.json"))
			raw, err := os.ReadFile(paths[0])
			if err != nil {
				t.Fatal(err)
			}
			if err = os.WriteFile(paths[0], test.mutate(raw), 0o600); err != nil {
				t.Fatal(err)
			}
			if _, err = newWitnessedStore(store.root); !IsCategory(err, CategoryPersistence) {
				t.Fatalf("journal representation accepted: %v", err)
			}
		})
	}
}

// TestAtomicWriteBoundaryCancellationAndIdentitySwaps exercises every checked publication boundary deterministically.
func TestAtomicWriteBoundaryCancellationAndIdentitySwaps(t *testing.T) {
	stages := []string{"preflight", "temporary", "write", "file_sync", "close", "link", "remove", "parent_open", "parent_sync", "parent_close"}
	for _, stage := range stages {
		t.Run(stage, func(t *testing.T) {
			store, err := newWitnessedStore(filepath.Join(t.TempDir(), "store"))
			if err != nil {
				t.Fatal(err)
			}
			injected := errors.New("injected boundary failure")
			store.boundaryHook = func(current string) error {
				if current == stage {
					return injected
				}
				return nil
			}
			path := filepath.Join(store.root, "snapshots", strings.Repeat("a", 64)+".json")
			if err = store.atomicWrite(context.Background(), path, []byte(`{}`)); !errors.Is(err, injected) {
				if stage != "parent_close" {
					t.Fatalf("stage %s failure not propagated: %v", stage, err)
				}
			}
			_, targetErr := os.Stat(path)
			if stage == "parent_close" && targetErr != nil {
				t.Fatalf("post-commit close removed confirmed target: %v", targetErr)
			}
			if stage != "parent_close" && !errors.Is(targetErr, os.ErrNotExist) {
				t.Fatalf("pre-commit stage %s retained target: %v", stage, targetErr)
			}
			entries, _ := filepath.Glob(filepath.Join(store.root, "snapshots", ".context-*.tmp"))
			if len(entries) != 0 {
				t.Fatalf("stage %s leaked temporaries: %v", stage, entries)
			}
		})
	}
	store, err := newWitnessedStore(filepath.Join(t.TempDir(), "store"))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	store.boundaryHook = func(stage string) error {
		if stage == "write" {
			cancel()
		}
		return nil
	}
	path := filepath.Join(store.root, "snapshots", strings.Repeat("b", 64)+".json")
	if err = store.atomicWrite(ctx, path, []byte(`{}`)); !IsCategory(err, CategoryCancelled) {
		t.Fatalf("mid-publication cancellation ignored: %v", err)
	}
	if _, statErr := os.Stat(path); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("cancelled publication retained target: %v", statErr)
	}
}

// TestJournalRedeliveryAfterLostReceipt reuses the authenticated entry instead of duplicating history.
func TestJournalRedeliveryAfterLostReceipt(t *testing.T) {
	store, snapshot := durableAssembly(t)
	receipts, _ := filepath.Glob(filepath.Join(store.root, "receipts", "*.json"))
	if len(receipts) != 1 || os.Remove(receipts[0]) != nil {
		t.Fatalf("receipt setup=%v", receipts)
	}
	request := testRequest(SourceConstraints, SourceContract, SourceConfig)
	profiles, _ := DefaultProfiles()
	planner, _ := profiles.Profile(RolePlanner)
	request.Limits, _ = effectiveBudgets(request.Limits, planner.Budgets)
	redelivered, created, err := store.CommitSnapshot(context.Background(), request.IdempotencyKey, request, snapshot)
	if err != nil || !created || redelivered.Digest != snapshot.Digest {
		t.Fatalf("lost-receipt recovery=%#v created=%t err=%v", redelivered, created, err)
	}
	journal, _ := filepath.Glob(filepath.Join(store.root, "journal", "*.json"))
	if len(journal) != 1 {
		for _, path := range journal {
			raw, _ := os.ReadFile(path)
			t.Logf("journal=%s", raw)
		}
		t.Fatalf("redelivery duplicated journal: %v", journal)
	}
	var value receipt
	canonical, _ := json.Marshal(receipt{Kind: "snapshot"})
	if strictCanonicalDecode(append(canonical, []byte(` {}`)...), &value) == nil {
		t.Fatal("trailing receipt JSON accepted")
	}
}

// TestExternalWitnessRejectsCoordinatedRollbackAndRecoversConfirmedReceipt proves freshness remains outside all local files.
func TestExternalWitnessRejectsCoordinatedRollbackAndRecoversConfirmedReceipt(t *testing.T) {
	root := filepath.Join(t.TempDir(), "store")
	store, witness := newTestLocalStore(t, root)
	assembler, _ := testAssembler(t, store, testProducer(t, SourceConstraints, testItem("constraint", SourceConstraints, "x")), testProducer(t, SourceContract, testItem("contract", SourceContract, "x")), testProducer(t, SourceConfig, func() Item {
		item := testItem("config", SourceConfig, "x")
		item.Authority, item.State = AuthorityApproved, "approved"
		return item
	}()))
	first, err := assembler.Assemble(context.Background(), testRequest(SourceConstraints, SourceContract, SourceConfig))
	if err != nil {
		t.Fatal(err)
	}
	receipts, _ := filepath.Glob(filepath.Join(root, "receipts", "*.json"))
	if len(receipts) != 1 || os.Remove(receipts[0]) != nil {
		t.Fatalf("receipt removal setup=%v", receipts)
	}
	if recovered := reopenTestLocalStore(t, root, witness); recovered == nil {
		t.Fatal("witness-confirmed receipt was not recovered")
	}
	secondRequest := testRequest(SourceConstraints, SourceContract, SourceConfig)
	secondRequest.IdempotencyKey = "second-witness-commit"
	second, err := assembler.Assemble(context.Background(), secondRequest)
	if err != nil {
		t.Fatal(err)
	}
	journals, _ := filepath.Glob(filepath.Join(root, "journal", "*.json"))
	receipts, _ = filepath.Glob(filepath.Join(root, "receipts", "*.json"))
	if len(journals) != 2 || len(receipts) != 2 {
		t.Fatalf("expected two confirmed commits journals=%d receipts=%d", len(journals), len(receipts))
	}
	var tail journalEntry
	var tailPath string
	for _, path := range journals {
		raw, _ := os.ReadFile(path)
		var entry journalEntry
		if json.Unmarshal(raw, &entry) == nil && entry.Sequence > tail.Sequence {
			tail, tailPath = entry, path
		}
	}
	if os.Remove(tailPath) != nil || os.Remove(filepath.Join(root, tail.Kind+"s", tail.ObjectDigest+".json")) != nil || os.Remove(filepath.Join(root, "receipts", receiptFileName(tail.Kind, tail.KeyDigest))) != nil {
		t.Fatal("coordinated rollback setup failed")
	}
	before, _ := witness.Read(context.Background(), "context-test")
	if _, err = NewLocalStore(context.Background(), root, "context-test", witness); !IsCategory(err, CategoryPersistence) {
		t.Fatalf("coordinated rollback accepted: %v", err)
	}
	after, _ := witness.Read(context.Background(), "context-test")
	if after != before || first.Digest == "" || second.Digest == "" {
		t.Fatalf("external authority changed during rejected rollback: before=%#v after=%#v", before, after)
	}
}

// TestWitnessCommitPointFinishesAfterCallerCancellation proves cancellation cannot make an irreversible commit ambiguous.
func TestWitnessCommitPointFinishesAfterCallerCancellation(t *testing.T) {
	root := filepath.Join(t.TempDir(), "store")
	store, witness := newTestLocalStore(t, root)
	assembler, _ := testAssembler(t, store, testProducer(t, SourceConstraints, testItem("constraint", SourceConstraints, "x")), testProducer(t, SourceContract, testItem("contract", SourceContract, "x")), testProducer(t, SourceConfig, func() Item {
		item := testItem("config", SourceConfig, "x")
		item.Authority, item.State = AuthorityApproved, "approved"
		return item
	}()))
	ctx, cancel := context.WithCancel(context.Background())
	witness.afterAdvance = cancel
	snapshot, err := assembler.Assemble(ctx, testRequest(SourceConstraints, SourceContract, SourceConfig))
	if err != nil || snapshot.Digest == "" {
		t.Fatalf("post-commit cancellation was reported ambiguously: snapshot=%#v err=%v", snapshot, err)
	}
	receipts, _ := filepath.Glob(filepath.Join(root, "receipts", "*.json"))
	if len(receipts) != 1 {
		t.Fatalf("post-commit receipt count=%d", len(receipts))
	}
	if _, err = NewLocalStore(context.Background(), root, "context-test", witness); err != nil {
		t.Fatalf("confirmed store did not reopen: %v", err)
	}
}

// TestStoreRejectsSubdirectorySwapDuringEveryPublicationBoundary proves identity checks surround all descriptor-relative writes.
func TestStoreRejectsSubdirectorySwapDuringEveryPublicationBoundary(t *testing.T) {
	for _, stage := range []string{"preflight", "temporary", "write", "file_sync", "close", "link", "remove", "parent_open", "parent_sync", "parent_close"} {
		t.Run(stage, func(t *testing.T) {
			root := filepath.Join(t.TempDir(), "store")
			store, err := newWitnessedStore(root)
			if err != nil {
				t.Fatal(err)
			}
			original := filepath.Join(root, "snapshots")
			moved := filepath.Join(root, "snapshots-original")
			store.boundaryHook = func(current string) error {
				if current != stage {
					return nil
				}
				if renameErr := os.Rename(original, moved); renameErr != nil {
					return renameErr
				}
				return os.Mkdir(original, 0o700)
			}
			target := filepath.Join(original, strings.Repeat("c", 64)+".json")
			err = store.atomicWrite(context.Background(), target, []byte(`{"safe":true}`))
			if err == nil && stage != "parent_close" {
				t.Fatalf("stage %s accepted a directory identity swap", stage)
			}
			if stage == "parent_close" && store.validateLayout() == nil {
				t.Fatal("post-commit directory swap remained trusted")
			}
			if _, statErr := os.Stat(target); !errors.Is(statErr, os.ErrNotExist) {
				t.Fatalf("stage %s published into attacker replacement: %v", stage, statErr)
			}
		})
	}
}

// TestOpenTemporaryHandleDefeatsNameSwap proves publication follows the authenticated inode rather than an attacker-controlled name.
func TestOpenTemporaryHandleDefeatsNameSwap(t *testing.T) {
	root := filepath.Join(t.TempDir(), "store")
	store, err := newWitnessedStore(root)
	if err != nil {
		t.Fatal(err)
	}
	directory := filepath.Join(root, "snapshots")
	store.boundaryHook = func(stage string) error {
		if stage != "close" {
			return nil
		}
		matches, _ := filepath.Glob(filepath.Join(directory, ".context-*.tmp"))
		if len(matches) != 1 {
			return errors.New("temporary handle not found")
		}
		if renameErr := os.Rename(matches[0], matches[0]+".stolen"); renameErr != nil {
			return renameErr
		}
		return os.WriteFile(matches[0], []byte(`{"attacker":true}`), 0o600)
	}
	target := filepath.Join(directory, strings.Repeat("d", 64)+".json")
	if err = store.atomicWrite(context.Background(), target, []byte(`{"safe":true}`)); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(target)
	if err != nil || string(raw) != `{"safe":true}` {
		t.Fatalf("temporary name swap changed published bytes: %s err=%v", raw, err)
	}
}

// TestWitnessConflictRollsBackEveryPrecommitFile proves failed external CAS leaves no locally publishable history while retaining immutable orphan bytes.
func TestWitnessConflictRollsBackEveryPrecommitFile(t *testing.T) {
	root := filepath.Join(t.TempDir(), "store")
	store, witness := newTestLocalStore(t, root)
	witness.rejectAdvance = true
	assembler, _ := testAssembler(t, store, testProducer(t, SourceConstraints, testItem("constraint", SourceConstraints, "x")), testProducer(t, SourceContract, testItem("contract", SourceContract, "x")), testProducer(t, SourceConfig, func() Item {
		item := testItem("config", SourceConfig, "x")
		item.Authority, item.State = AuthorityApproved, "approved"
		return item
	}()))
	if _, err := assembler.Assemble(context.Background(), testRequest(SourceConstraints, SourceContract, SourceConfig)); !IsCategory(err, CategoryPersistence) {
		t.Fatalf("witness conflict accepted: %v", err)
	}
	for directory, expected := range map[string]int{"snapshots": 1, "journal": 0, "receipts": 0} {
		entries, readErr := os.ReadDir(filepath.Join(root, directory))
		if readErr != nil || len(entries) != expected {
			t.Fatalf("witness conflict retention mismatch for %s: entries=%v expected=%d err=%v", directory, entries, expected, readErr)
		}
	}
	state, _ := witness.Read(context.Background(), "context-test")
	if state != InitialWitnessState("context-test") {
		t.Fatalf("rejected witness advanced: %#v", state)
	}
}

// TestConfirmedTailRecoveryRequiresExactObjectAndReceiptName proves recovery cannot repair around missing payloads or aliased receipts.
func TestConfirmedTailRecoveryRequiresExactObjectAndReceiptName(t *testing.T) {
	makeCommitted := func(t *testing.T) (string, *testTailWitness, Snapshot) {
		t.Helper()
		root := filepath.Join(t.TempDir(), "store")
		store, witness := newTestLocalStore(t, root)
		assembler, _ := testAssembler(t, store, testProducer(t, SourceConstraints, testItem("constraint", SourceConstraints, "x")), testProducer(t, SourceContract, testItem("contract", SourceContract, "x")), testProducer(t, SourceConfig, func() Item {
			item := testItem("config", SourceConfig, "x")
			item.Authority, item.State = AuthorityApproved, "approved"
			return item
		}()))
		snapshot, err := assembler.Assemble(context.Background(), testRequest(SourceConstraints, SourceContract, SourceConfig))
		if err != nil {
			t.Fatal(err)
		}
		return root, witness, snapshot
	}
	t.Run("missing-object", func(t *testing.T) {
		root, witness, snapshot := makeCommitted(t)
		receipts, _ := filepath.Glob(filepath.Join(root, "receipts", "*.json"))
		if len(receipts) != 1 || os.Remove(receipts[0]) != nil || os.Remove(filepath.Join(root, "snapshots", snapshot.Digest+".json")) != nil {
			t.Fatal("missing object setup failed")
		}
		if _, err := NewLocalStore(context.Background(), root, "context-test", witness); !IsCategory(err, CategoryPersistence) {
			t.Fatalf("missing confirmed object was repaired: %v", err)
		}
	})
	t.Run("receipt-alias", func(t *testing.T) {
		root, witness, _ := makeCommitted(t)
		receipts, _ := filepath.Glob(filepath.Join(root, "receipts", "*.json"))
		alias := filepath.Join(root, "receipts", strings.Repeat("f", 64)+".json")
		if len(receipts) != 1 || os.Rename(receipts[0], alias) != nil {
			t.Fatal("receipt alias setup failed")
		}
		if _, err := NewLocalStore(context.Background(), root, "context-test", witness); !IsCategory(err, CategoryPersistence) {
			t.Fatalf("aliased receipt accepted: %v", err)
		}
	})
}

// TestPostCommitReceiptRetriesReturnConfirmed proves a transient receipt failure after witness CAS never becomes an ambiguous caller result.
func TestPostCommitReceiptRetriesReturnConfirmed(t *testing.T) {
	root := filepath.Join(t.TempDir(), "store")
	store, witness := newTestLocalStore(t, root)
	preflights := 0
	injected := errors.New("transient receipt publication")
	store.boundaryHook = func(stage string) error {
		if stage != "preflight" {
			return nil
		}
		preflights++
		if preflights == 3 {
			return injected
		}
		return nil
	}
	assembler, _ := testAssembler(t, store, testProducer(t, SourceConstraints, testItem("constraint", SourceConstraints, "x")), testProducer(t, SourceContract, testItem("contract", SourceContract, "x")), testProducer(t, SourceConfig, func() Item {
		item := testItem("config", SourceConfig, "x")
		item.Authority, item.State = AuthorityApproved, "approved"
		return item
	}()))
	snapshot, err := assembler.Assemble(context.Background(), testRequest(SourceConstraints, SourceContract, SourceConfig))
	if err != nil || snapshot.Digest == "" || preflights < 4 {
		t.Fatalf("post-commit retry was not confirmed: digest=%s preflights=%d err=%v", snapshot.Digest, preflights, err)
	}
	if _, err = NewLocalStore(context.Background(), root, "context-test", witness); err != nil {
		t.Fatalf("retried receipt did not reopen: %v", err)
	}
}

// TestWitnessCASReconciliationConvergesAcrossResponseLossRestartAndRedelivery proves every ambiguous CAS result is classified by durable authority.
func TestWitnessCASReconciliationConvergesAcrossResponseLossRestartAndRedelivery(t *testing.T) {
	newStore := func(t *testing.T) (string, *LocalStore, *reconciliationWitness) {
		t.Helper()
		root := filepath.Join(t.TempDir(), "store")
		base := &testTailWitness{states: map[string]projectmemory.WitnessState{"context-reconcile": InitialWitnessState("context-reconcile")}}
		witness := &reconciliationWitness{base: base}
		store, err := NewLocalStore(context.Background(), root, "context-reconcile", witness)
		if err != nil {
			t.Fatal(err)
		}
		return root, store, witness
	}
	producers := func(t *testing.T) []Producer {
		t.Helper()
		config := testItem("config", SourceConfig, "x")
		config.Authority, config.State = AuthorityApproved, "approved"
		return []Producer{testProducer(t, SourceConstraints, testItem("constraint", SourceConstraints, "x")), testProducer(t, SourceContract, testItem("contract", SourceContract, "x")), testProducer(t, SourceConfig, config)}
	}
	t.Run("confirmed-response-lost", func(t *testing.T) {
		root, store, witness := newStore(t)
		witness.setMode("after", false)
		assembler, _ := testAssembler(t, store, producers(t)...)
		request := testRequest(SourceConstraints, SourceContract, SourceConfig)
		snapshot, err := assembler.Assemble(context.Background(), request)
		if err != nil || snapshot.Digest == "" {
			t.Fatalf("confirmed response loss was not reconciled: %#v %v", snapshot, err)
		}
		reopened, err := NewLocalStore(context.Background(), root, "context-reconcile", witness)
		if err != nil {
			t.Fatalf("confirmed response loss did not reopen: %v", err)
		}
		replayAssembler, _ := testAssembler(t, reopened, producers(t)...)
		replayed, err := replayAssembler.Assemble(context.Background(), request)
		if err != nil || replayed.Digest != snapshot.Digest {
			t.Fatalf("confirmed redelivery diverged: %#v err=%v", replayed, err)
		}
	})
	t.Run("before-cas", func(t *testing.T) {
		root, store, witness := newStore(t)
		witness.setMode("before", false)
		assembler, _ := testAssembler(t, store, producers(t)...)
		if _, err := assembler.Assemble(context.Background(), testRequest(SourceConstraints, SourceContract, SourceConfig)); !IsCategory(err, CategoryPersistence) {
			t.Fatalf("pre-cas failure was not rolled back: %v", err)
		}
		for directory, expected := range map[string]int{"snapshots": 1, "journal": 0, "receipts": 0} {
			entries, readErr := os.ReadDir(filepath.Join(root, directory))
			if readErr != nil || len(entries) != expected {
				t.Fatalf("pre-cas retention mismatch for %s: entries=%v expected=%d err=%v", directory, entries, expected, readErr)
			}
		}
	})
	t.Run("ambiguous-read", func(t *testing.T) {
		root, store, witness := newStore(t)
		witness.setMode("after", true)
		assembler, _ := testAssembler(t, store, producers(t)...)
		request := testRequest(SourceConstraints, SourceContract, SourceConfig)
		if _, err := assembler.Assemble(context.Background(), request); !IsCategory(err, CategoryUncertain) {
			t.Fatalf("ambiguous witness state was not reported uncertain: %v", err)
		}
		journals, _ := os.ReadDir(filepath.Join(root, "staging"))
		objects, _ := os.ReadDir(filepath.Join(root, "snapshots"))
		if len(journals) != 1 || len(objects) != 1 {
			t.Fatalf("ambiguous state was destructively rolled back: journals=%d objects=%d", len(journals), len(objects))
		}
		candidateRaw, readErr := os.ReadFile(filepath.Join(root, "staging", journals[0].Name()))
		var candidate journalEntry
		if readErr != nil || json.Unmarshal(candidateRaw, &candidate) != nil {
			t.Fatalf("ambiguous candidate unreadable: %v", readErr)
		}
		if _, loadErr := store.LoadSnapshot(context.Background(), candidate.ObjectDigest); !IsCategory(loadErr, CategoryPersistence) {
			t.Fatalf("ambiguous snapshot became addressable before reconciliation: %v", loadErr)
		}
		witness.setMode("", false)
		reopened, err := NewLocalStore(context.Background(), root, "context-reconcile", witness)
		if err != nil {
			t.Fatalf("restart did not reconcile ambiguous confirmed state: %v", err)
		}
		receipts, _ := os.ReadDir(filepath.Join(root, "receipts"))
		if len(receipts) != 1 {
			t.Fatalf("restart did not complete confirmed receipt: %d", len(receipts))
		}
		if _, loadErr := reopened.LoadSnapshot(context.Background(), candidate.ObjectDigest); loadErr != nil {
			t.Fatalf("reconciled snapshot remained hidden: %v", loadErr)
		}
		replayAssembler, _ := testAssembler(t, reopened, producers(t)...)
		replayed, err := replayAssembler.Assemble(context.Background(), request)
		if err != nil || replayed.Digest == "" {
			t.Fatalf("ambiguous redelivery did not converge: %#v %v", replayed, err)
		}
		receipts, _ = os.ReadDir(filepath.Join(root, "receipts"))
		if len(receipts) != 1 {
			t.Fatalf("ambiguous redelivery duplicated receipt: %d", len(receipts))
		}
	})
	t.Run("ambiguous-before-cas-same-process", func(t *testing.T) {
		root, store, witness := newStore(t)
		witness.setMode("before", true)
		assembler, _ := testAssembler(t, store, producers(t)...)
		request := testRequest(SourceConstraints, SourceContract, SourceConfig)
		if _, err := assembler.Assemble(context.Background(), request); !IsCategory(err, CategoryUncertain) {
			t.Fatalf("ambiguous pre-cas witness state was not preserved: %v", err)
		}
		witness.setMode("", false)
		snapshot, err := assembler.Assemble(context.Background(), request)
		if err != nil || snapshot.Digest == "" {
			t.Fatalf("same-process reconciliation did not rollback and retry: %#v %v", snapshot, err)
		}
		for _, directory := range []string{"journal", "receipts"} {
			entries, readErr := os.ReadDir(filepath.Join(root, directory))
			if readErr != nil || len(entries) != 1 {
				t.Fatalf("same-process reconciliation duplicated %s: %#v %v", directory, entries, readErr)
			}
		}
	})
	t.Run("ambiguous-after-cas-same-process", func(t *testing.T) {
		root, store, witness := newStore(t)
		witness.setMode("after", true)
		assembler, _ := testAssembler(t, store, producers(t)...)
		request := testRequest(SourceConstraints, SourceContract, SourceConfig)
		if _, err := assembler.Assemble(context.Background(), request); !IsCategory(err, CategoryUncertain) {
			t.Fatalf("lost confirmed response was not initially uncertain: %v", err)
		}
		witness.setMode("", false)
		snapshot, err := assembler.Assemble(context.Background(), request)
		if err != nil || snapshot.Digest == "" {
			t.Fatalf("same-process confirmed reconciliation failed: %#v %v", snapshot, err)
		}
		for _, directory := range []string{"journal", "receipts", "snapshots"} {
			entries, readErr := os.ReadDir(filepath.Join(root, directory))
			if readErr != nil || len(entries) != 1 {
				t.Fatalf("confirmed reconciliation duplicated %s: %#v %v", directory, entries, readErr)
			}
		}
	})
}
