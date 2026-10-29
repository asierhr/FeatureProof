package controlplane

import (
	"context"
	"errors"
	"sync"
	"time"
)

// Scheduler is a caller-owned durable worker loop with no hidden goroutines.
type Scheduler struct {
	queue       JobQueue
	clock       Clock
	waiter      Waiter
	handler     JobHandler
	classifier  ErrorClassifier
	config      SchedulerConfig
	handlerMu   sync.Mutex
	handlers    int
	handlerDone chan struct{}
}

// NewScheduler validates and composes a bounded worker without starting it.
func NewScheduler(queue JobQueue, clock Clock, waiter Waiter, handler JobHandler, classifier ErrorClassifier, config SchedulerConfig) (*Scheduler, error) {
	if queue == nil || clock == nil || waiter == nil || handler == nil || classifier == nil || !safeID.MatchString(config.ProjectID) || !safeID.MatchString(config.WorkerID) || config.Lease <= 0 || config.BatchSize <= 0 || config.IdleWait < 0 {
		return nil, ErrInvalid
	}
	if config.HeartbeatInterval == 0 {
		config.HeartbeatInterval = config.Lease / 2
	}
	if config.HeartbeatInterval <= 0 || config.HeartbeatInterval >= config.Lease {
		return nil, ErrInvalid
	}
	handlerDone := make(chan struct{})
	close(handlerDone)
	return &Scheduler{queue: queue, clock: clock, waiter: waiter, handler: handler, classifier: classifier, config: config, handlerDone: handlerDone}, nil
}

// RunOnce recovers expired work, claims one bounded page and persists every outcome.
func (scheduler *Scheduler) RunOnce(ctx context.Context) (int, error) {
	now := scheduler.clock.Now().UTC()
	if _, err := scheduler.queue.Recover(ctx, now, scheduler.config.BatchSize); err != nil {
		return 0, err
	}
	jobs, err := scheduler.queue.Claim(ctx, ClaimRequest{ProjectID: scheduler.config.ProjectID, WorkerID: scheduler.config.WorkerID, Now: now, Lease: scheduler.config.Lease, Limit: scheduler.config.BatchSize})
	if err != nil {
		return 0, err
	}
	for _, job := range jobs {
		handleErr := scheduler.handleWithHeartbeat(ctx, job)
		if handleErr == nil {
			if _, err := scheduler.queue.Complete(ctx, job.JobID, scheduler.config.WorkerID, job.Fence, job.CancelRevision, scheduler.clock.Now(), JobSucceeded, ""); err != nil {
				return len(jobs), err
			}
			continue
		}
		if errors.Is(handleErr, context.Canceled) || errors.Is(handleErr, context.DeadlineExceeded) {
			return len(jobs), handleErr
		}
		decision := scheduler.classifier.Classify(handleErr)
		available := scheduler.clock.Now().UTC().Add(decision.Backoff)
		if _, err := scheduler.queue.Retry(ctx, job.JobID, scheduler.config.WorkerID, job.Fence, job.CancelRevision, now, decision, available); err != nil {
			return len(jobs), err
		}
	}
	return len(jobs), nil
}

// handleWithHeartbeat owns the handler goroutine, renews its lease and joins it before returning any outcome.
func (scheduler *Scheduler) handleWithHeartbeat(ctx context.Context, job Job) error {
	handlerContext, cancel := context.WithCancel(ctx)
	defer cancel()
	result := make(chan error, 1)
	handledJob := job
	scheduler.beginHandler()
	go func() {
		defer scheduler.endHandler()
		result <- scheduler.handler.Handle(handlerContext, handledJob)
	}()
	ticker := time.NewTicker(scheduler.config.HeartbeatInterval)
	defer ticker.Stop()
	for {
		select {
		case err := <-result:
			return err
		case <-ticker.C:
			now := scheduler.clock.Now().UTC()
			updated, err := scheduler.queue.Heartbeat(ctx, job.JobID, scheduler.config.WorkerID, job.Fence, job.CancelRevision, now, scheduler.config.Lease)
			if err != nil {
				cancel()
				return err
			}
			job = updated
		case <-ctx.Done():
			cancel()
			return ctx.Err()
		}
	}
}

// beginHandler records ownership before a product goroutine can become visible to shutdown.
func (scheduler *Scheduler) beginHandler() {
	scheduler.handlerMu.Lock()
	defer scheduler.handlerMu.Unlock()
	if scheduler.handlers == 0 {
		scheduler.handlerDone = make(chan struct{})
	}
	scheduler.handlers++
}

// endHandler releases ownership and publishes completion only after the product goroutine actually returns.
func (scheduler *Scheduler) endHandler() {
	scheduler.handlerMu.Lock()
	defer scheduler.handlerMu.Unlock()
	scheduler.handlers--
	if scheduler.handlers == 0 {
		close(scheduler.handlerDone)
	}
}

// handlerCompletion returns the generation channel that closes when every already-started handler has terminated.
func (scheduler *Scheduler) handlerCompletion() <-chan struct{} {
	scheduler.handlerMu.Lock()
	defer scheduler.handlerMu.Unlock()
	return scheduler.handlerDone
}

// Run repeatedly executes durable pages until caller cancellation.
func (scheduler *Scheduler) Run(ctx context.Context) error {
	for {
		count, err := scheduler.RunOnce(ctx)
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			return err
		}
		if count > 0 {
			continue
		}
		if err := scheduler.waiter.Wait(ctx, scheduler.config.IdleWait); err != nil {
			return err
		}
	}
}

// SystemClock supplies wall time only in production composition.
type SystemClock struct{}

// Now returns the current UTC scheduling instant.
func (SystemClock) Now() time.Time { return time.Now().UTC() }

// TimerWaiter performs a caller-cancelable idle wait without retaining a goroutine.
type TimerWaiter struct{}

// Wait blocks for one bounded interval or context cancellation.
func (TimerWaiter) Wait(ctx context.Context, duration time.Duration) error {
	timer := time.NewTimer(duration)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

// WorkerRuntime gives executable composition explicit start, stop and join ownership for one scheduler loop.
type WorkerRuntime struct {
	scheduler *Scheduler
	mu        sync.Mutex
	cancel    context.CancelFunc
	done      chan struct{}
	runErr    error
}

// NewWorkerRuntime constructs an inert lifecycle; no goroutine starts during composition.
func NewWorkerRuntime(scheduler *Scheduler) (*WorkerRuntime, error) {
	if scheduler == nil {
		return nil, ErrInvalid
	}
	return &WorkerRuntime{scheduler: scheduler}, nil
}

// Start launches exactly one caller-owned scheduler loop.
func (runtime *WorkerRuntime) Start(parent context.Context) error {
	runtime.mu.Lock()
	defer runtime.mu.Unlock()
	if runtime.cancel != nil || runtime.done != nil {
		return ErrConflict
	}
	ctx, cancel := context.WithCancel(parent)
	done := make(chan struct{})
	runtime.cancel, runtime.done = cancel, done
	go func() {
		runErr := runtime.scheduler.Run(ctx)
		runtime.mu.Lock()
		runtime.runErr = runErr
		close(done)
		runtime.mu.Unlock()
	}()
	return nil
}

// Stop prevents new claims and cancels waits or cooperative handler I/O.
func (runtime *WorkerRuntime) Stop() {
	runtime.mu.Lock()
	cancel := runtime.cancel
	runtime.mu.Unlock()
	if cancel != nil {
		cancel()
	}
}

// Join waits for the owned loop and normalizes caller cancellation as a clean shutdown.
func (runtime *WorkerRuntime) Join() error {
	return runtime.JoinContext(context.Background())
}

// JoinContext waits only within caller shutdown authority and cannot be held forever by a non-cooperative handler.
func (runtime *WorkerRuntime) JoinContext(ctx context.Context) error {
	runtime.mu.Lock()
	done := runtime.done
	runtime.mu.Unlock()
	if done == nil {
		return ErrInvalid
	}
	select {
	case <-done:
	case <-ctx.Done():
		return ctx.Err()
	}
	handlers := runtime.scheduler.handlerCompletion()
	select {
	case <-handlers:
	case <-ctx.Done():
		return ctx.Err()
	}
	runtime.mu.Lock()
	err := runtime.runErr
	runtime.mu.Unlock()
	if errors.Is(err, context.Canceled) {
		return nil
	}
	return err
}
