package githubcontext

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"testing"
)

// storedFixture acquires one valid context used to exercise durable publication.
func storedFixture(t *testing.T) (Context, Binding, string) {
	t.Helper()
	policy := testPolicy(t)
	metadata := testMetadata()
	gateway := &fakeGateway{metadata: []RemoteMetadata{metadata, metadata}, pages: map[int]FilePage{1: {}}}
	acquirer, _ := testAcquirer(t, gateway, &fakeGate{})
	value, binding, err := acquirer.Refresh(context.Background(), testRequest(policy), policy)
	if err != nil {
		t.Fatal(err)
	}
	fingerprint, _ := digestValue(testRequest(policy))
	return value, binding, fingerprint
}

// TestLocalStoreReopensAndRejectsConflicts verifies addressability begins only at an immutable receipt.
func TestLocalStoreReopensAndRejectsConflicts(t *testing.T) {
	root := t.TempDir()
	store, err := OpenLocalStore(root)
	if err != nil {
		t.Fatal(err)
	}
	value, binding, fingerprint := storedFixture(t)
	if err = store.Commit(context.Background(), "key", fingerprint, value, binding); err != nil {
		t.Fatal(err)
	}
	reopened, err := OpenLocalStore(root)
	if err != nil {
		t.Fatal(err)
	}
	gotFingerprint, got, gotBinding, found, err := reopened.Load(context.Background(), "key")
	if err != nil || !found || gotFingerprint != fingerprint || got.Digest != value.Digest || gotBinding.Digest != binding.Digest {
		t.Fatalf("reopen: %v %t %#v", err, found, got)
	}
	if err = reopened.Commit(context.Background(), "key", fingerprint, value, binding); err != nil {
		t.Fatal(err)
	}
	if err = reopened.Commit(context.Background(), "key", digestBytes([]byte("different")), value, binding); !IsCategory(err, CategoryConflict) {
		t.Fatalf("conflict accepted: %v", err)
	}
}

// TestLocalStoreConcurrentPublicationConverges verifies immutable link claims under contention.
func TestLocalStoreConcurrentPublicationConverges(t *testing.T) {
	store, err := OpenLocalStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	value, binding, fingerprint := storedFixture(t)
	var wg sync.WaitGroup
	errorsOut := make(chan error, 20)
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			errorsOut <- store.Commit(context.Background(), "same-key", fingerprint, value, binding)
		}()
	}
	wg.Wait()
	close(errorsOut)
	for commitErr := range errorsOut {
		if commitErr != nil {
			t.Fatal(commitErr)
		}
	}
	_, got, _, found, err := store.Load(context.Background(), "same-key")
	if err != nil || !found || got.Digest != value.Digest {
		t.Fatalf("convergence: %v %t", err, found)
	}
}

// TestLocalStoreFailsClosedForCorruptionAndCancellation verifies durable and lifecycle boundaries.
func TestLocalStoreFailsClosedForCorruptionAndCancellation(t *testing.T) {
	root := t.TempDir()
	store, err := OpenLocalStore(root)
	if err != nil {
		t.Fatal(err)
	}
	value, binding, fingerprint := storedFixture(t)
	if err = store.Commit(context.Background(), "key", fingerprint, value, binding); err != nil {
		t.Fatal(err)
	}
	receiptPath := filepath.Join(root, "receipts", digestBytes([]byte("key"))+".json")
	if err = os.WriteFile(receiptPath, []byte(`{"version":"bad"}`), 0600); err != nil {
		t.Fatal(err)
	}
	if _, _, _, _, err = store.Load(context.Background(), "key"); !IsCategory(err, CategoryIntegrity) {
		t.Fatalf("corruption accepted: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, _, _, _, err = store.Load(ctx, "other"); !IsCategory(err, CategoryCancelled) {
		t.Fatalf("cancel ignored: %v", err)
	}
}
