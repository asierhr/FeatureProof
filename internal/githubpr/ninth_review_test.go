package githubpr

import (
	"context"
	"testing"
	"time"
)

// TestCloseHonorsCallerCancellationAndConfirmedJoin covers deterministic join priority and cancellation category.
func TestCloseHonorsCallerCancellationAndConfirmedJoin(t *testing.T) {
	base, cancelBase := context.WithCancel(context.Background())
	joined := &Coordinator{ctx: base, cancel: cancelBase, deps: CoordinatorDependencies{CloseTimeout: time.Second}}
	cancelled, cancelCaller := context.WithCancel(context.Background())
	cancelCaller()
	if err := joined.Close(cancelled); !IsCode(err, CodeCancelled) {
		t.Fatalf("caller cancellation classification lost after empty join: %v", err)
	}

	base, cancelBase = context.WithCancel(context.Background())
	blocked := &Coordinator{ctx: base, cancel: cancelBase, deps: CoordinatorDependencies{CloseTimeout: time.Second}, workers: map[string]context.CancelFunc{"blocked": func() {}}}
	release := make(chan struct{})
	workerDone := make(chan struct{})
	finalizing := make(chan struct{})
	blocked.wg.Add(1)
	go func() {
		defer blocked.wg.Done()
		defer close(workerDone)
		<-base.Done()
		close(finalizing)
		<-release
	}()
	cancelled, cancelCaller = context.WithCancel(context.Background())
	cancelCaller()
	closed := make(chan error, 1)
	go func() { closed <- blocked.Close(cancelled) }()
	<-finalizing
	select {
	case err := <-closed:
		t.Fatalf("Close abandoned worker: %v", err)
	default:
	}
	close(release)
	<-workerDone
	if err := <-closed; !IsCode(err, CodeCancelled) {
		t.Fatalf("caller cancellation category=%v", err)
	}
}

// TestCloseInternalBudgetCancelsCleanupAndStillJoins proves timeout classification never abandons cleanup ownership.
func TestCloseInternalBudgetCancelsCleanupAndStillJoins(t *testing.T) {
	base, cancelBase := context.WithCancel(context.Background())
	coordinator := &Coordinator{ctx: base, cancel: cancelBase, deps: CoordinatorDependencies{CloseTimeout: time.Millisecond}, workers: map[string]context.CancelFunc{"worker": func() {}}}
	cleanupStarted, cleanupReleased := make(chan struct{}), make(chan struct{})
	coordinator.wg.Add(1)
	go func() {
		defer coordinator.wg.Done()
		<-base.Done()
		cleanup, cancel := coordinator.cleanupContext(base)
		defer cancel()
		close(cleanupStarted)
		<-cleanup.Done()
		close(cleanupReleased)
	}()
	err := coordinator.Close(context.Background())
	if !IsCode(err, CodeCancelled) {
		t.Fatalf("internal timeout category=%v", err)
	}
	select {
	case <-cleanupStarted:
	default:
		t.Fatal("cleanup never started")
	}
	select {
	case <-cleanupReleased:
	default:
		t.Fatal("Close returned before cleanup release and join")
	}
}
