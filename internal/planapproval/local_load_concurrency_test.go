package planapproval

import (
	"context"
	"errors"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"
)

// localLoadResult carries one asynchronous LocalStore read outcome across a coordinated test boundary.
type localLoadResult struct {
	events []ApprovalEvent
	err    error
}

// waitLocalLoad bounds a coordinated load assertion so a lock regression fails instead of hanging the suite.
func waitLocalLoad(t *testing.T, result <-chan localLoadResult, operation string) localLoadResult {
	t.Helper()
	select {
	case value := <-result:
		return value
	case <-time.After(2 * time.Second):
		t.Fatalf("%s blocked", operation)
		return localLoadResult{}
	}
}

// asyncLocalEvents starts one observable journal load without accessing production targets or credentials.
func asyncLocalEvents(store *LocalStore, ctx context.Context, runID string) <-chan localLoadResult {
	result := make(chan localLoadResult, 1)
	go func() {
		events, err := store.Events(ctx, runID)
		result <- localLoadResult{events: events, err: err}
	}()
	return result
}

// TestLocalLoadsForDifferentRunsProgressIndependently proves a blocked owner cannot serialize another run at the same root.
func TestLocalLoadsForDifferentRunsProgressIndependently(t *testing.T) {
	root := t.TempDir()
	store, err := NewLocalStore(root)
	if err != nil {
		t.Fatal(err)
	}
	entered, release := make(chan struct{}), make(chan struct{})
	store.load = func(_ context.Context, path string) ([]ApprovalEvent, error) {
		if filepath.Base(path) == "run-a.jsonl" {
			close(entered)
			<-release
		}
		return []ApprovalEvent{}, nil
	}
	second, err := NewLocalStore(root)
	if err != nil {
		t.Fatal(err)
	}
	second.load = store.load
	runA := asyncLocalEvents(store, context.Background(), "run-a")
	waitSignal(t, entered, "run A load")
	runB := waitLocalLoad(t, asyncLocalEvents(second, context.Background(), "run-b"), "run B load")
	if runB.err != nil || len(runB.events) != 0 {
		t.Fatalf("run B failed behind run A: events=%d err=%v", len(runB.events), runB.err)
	}
	close(release)
	if result := waitLocalLoad(t, runA, "run A completion"); result.err != nil {
		t.Fatal(result.err)
	}
}

// TestSameRunCallersShareOneLocalLoad proves concurrent readers observe one owner and one atomically published prefix.
func TestSameRunCallersShareOneLocalLoad(t *testing.T) {
	root := t.TempDir()
	store, err := NewLocalStore(root)
	if err != nil {
		t.Fatal(err)
	}
	entered, release := make(chan struct{}), make(chan struct{})
	var calls atomic.Int32
	event := validRequestedEvent("shared-run")
	store.load = func(context.Context, string) ([]ApprovalEvent, error) {
		calls.Add(1)
		close(entered)
		<-release
		return []ApprovalEvent{event}, nil
	}
	waiterStore, err := NewLocalStore(root)
	if err != nil {
		t.Fatal(err)
	}
	waiterStore.load = store.load
	owner := asyncLocalEvents(store, context.Background(), "shared-run")
	waitSignal(t, entered, "shared owner")
	waiter := asyncLocalEvents(waiterStore, context.Background(), "shared-run")
	close(release)
	for index, result := range []localLoadResult{waitLocalLoad(t, owner, "shared owner completion"), waitLocalLoad(t, waiter, "shared waiter completion")} {
		if result.err != nil || len(result.events) != 1 || result.events[0].EventID != event.EventID {
			t.Fatalf("caller %d saw a different prefix: events=%#v err=%v", index, result.events, result.err)
		}
	}
	if calls.Load() != 1 {
		t.Fatalf("same run loaded %d times", calls.Load())
	}
}

// TestCancelledLocalLoadWaiterLeavesOwnerAndRetryStateUntouched proves waiting is cancellation-aware without stealing ownership.
func TestCancelledLocalLoadWaiterLeavesOwnerAndRetryStateUntouched(t *testing.T) {
	store, err := NewLocalStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	entered, release := make(chan struct{}), make(chan struct{})
	var calls atomic.Int32
	store.load = func(context.Context, string) ([]ApprovalEvent, error) {
		calls.Add(1)
		close(entered)
		<-release
		return []ApprovalEvent{}, nil
	}
	owner := asyncLocalEvents(store, context.Background(), "cancel-waiter")
	waitSignal(t, entered, "waiter owner")
	waiterContext, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := store.Events(waiterContext, "cancel-waiter"); !errors.Is(err, context.Canceled) {
		t.Fatalf("waiter cancellation lost: %v", err)
	}
	if calls.Load() != 1 {
		t.Fatalf("cancelled waiter started %d loads", calls.Load())
	}
	close(release)
	if result := waitLocalLoad(t, owner, "owner after waiter cancellation"); result.err != nil {
		t.Fatal(result.err)
	}
}

// TestLocalLoadOwnerFailuresPermitCleanRetry proves both injected errors and owner cancellation discard temporary prefixes.
func TestLocalLoadOwnerFailuresPermitCleanRetry(t *testing.T) {
	for _, scenario := range []struct {
		name       string
		firstError error
		cancel     bool
	}{
		{name: "error", firstError: typedError(CategoryStorageFailure, "load_store", "injected", nil)},
		{name: "cancellation", cancel: true},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			store, err := NewLocalStore(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			entered := make(chan struct{})
			var calls atomic.Int32
			event := validRequestedEvent("retry-run")
			store.load = func(ctx context.Context, _ string) ([]ApprovalEvent, error) {
				if calls.Add(1) == 1 {
					close(entered)
					if scenario.cancel {
						<-ctx.Done()
						return []ApprovalEvent{event}, contextFailure("load_store", ctx.Err())
					}
					return []ApprovalEvent{event}, scenario.firstError
				}
				return []ApprovalEvent{event}, nil
			}
			firstContext, cancel := context.WithCancel(context.Background())
			defer cancel()
			first := asyncLocalEvents(store, firstContext, "retry-run")
			waitSignal(t, entered, "failed owner entry")
			if scenario.cancel {
				cancel()
			}
			failed := waitLocalLoad(t, first, "failed owner completion")
			if failed.err == nil || (scenario.cancel && !errors.Is(failed.err, context.Canceled)) {
				t.Fatalf("owner failure lost: %v", failed.err)
			}
			retried, err := store.Events(context.Background(), "retry-run")
			if err != nil || len(retried) != 1 || retried[0].EventID != event.EventID || calls.Load() != 2 {
				t.Fatalf("clean retry failed: calls=%d events=%#v err=%v", calls.Load(), retried, err)
			}
			confirmed, err := store.backend.memory.Events(context.Background(), "retry-run")
			if err != nil || len(confirmed) != 1 {
				t.Fatalf("retry published a duplicate or partial prefix: events=%d err=%v", len(confirmed), err)
			}
		})
	}
}
