package externalagent

import (
	"context"
	"errors"
	"sync"
	"time"
)

// ErrWorkerMissing means this process does not own the requested worker.
var ErrWorkerMissing = errors.New("worker missing")

// worker owns one cancel function and one completion signal.
type worker struct {
	cancel context.CancelFunc
	done   chan struct{}
}

// Supervisor owns every goroutine it starts and joins them on cancellation or close.
type Supervisor struct {
	mu      sync.Mutex
	service *Service
	workers map[string]*worker
	root    context.Context
	cancel  context.CancelFunc
}

// NewSupervisor creates an explicit process-lifetime context without starting work.
func NewSupervisor(service *Service) *Supervisor {
	root, cancel := context.WithCancel(context.Background())
	return &Supervisor{service: service, workers: map[string]*worker{}, root: root, cancel: cancel}
}

// Start registers ownership before launching exactly one execution goroutine.
func (s *Supervisor) Start(record RunRecord, selection Selection) {
	s.mu.Lock()
	if _, exists := s.workers[record.Handle]; exists {
		s.mu.Unlock()
		return
	}
	ctx, cancel := context.WithTimeout(s.root, s.service.deps.RunTimeout)
	entry := &worker{cancel: cancel, done: make(chan struct{})}
	s.workers[record.Handle] = entry
	s.mu.Unlock()
	go s.run(ctx, entry, record, selection)
}

// run executes transitions without retaining the supervisor mutex across callbacks or disk I/O.
func (s *Supervisor) run(ctx context.Context, entry *worker, record RunRecord, selection Selection) {
	defer func() {
		s.finish(record.Handle, entry)
		s.service.deps.Registry.ReleaseOwner(record.Handle)
		close(entry.done)
	}()
	defer entry.cancel()
	current, err := s.service.deps.Registry.Transition(ctx, record.Handle, PhaseQueued, PhaseRunning, "", "", s.service.deps.Owner, s.service.deps.Clock.Now())
	if err != nil {
		finalCtx, cancel := context.WithTimeout(context.Background(), s.service.deps.RunTimeout)
		defer cancel()
		if _, terminalErr := s.service.deps.Registry.Transition(finalCtx, record.Handle, PhaseQueued, PhaseTerminal, OutcomeError, "", "", s.service.deps.Clock.Now()); terminalErr != nil {
			return
		}
		return
	}
	monitorCtx, stopMonitor := context.WithCancel(ctx)
	monitorDone := make(chan struct{})
	go s.watchCancellation(monitorCtx, record.Handle, entry.cancel, monitorDone)
	result, executionErr := s.service.deps.Executor.Execute(ctx, selection, current.InternalID)
	stopMonitor()
	<-monitorDone
	status := result.Status
	if executionErr != nil {
		status = OutcomeError
	}
	if ctx.Err() != nil {
		status = OutcomeCancelled
	}
	if !validOutcome(status) {
		status = OutcomeError
	}
	finalCtx, cancel := context.WithTimeout(context.Background(), s.service.deps.RunTimeout)
	defer cancel()
	if errors.Is(executionErr, ErrPublicationFailed) {
		if markErr := s.service.deps.Registry.MarkPublicationFailure(finalCtx, record.Handle, s.service.deps.Clock.Now()); markErr != nil {
			result.PackageDir = ""
		}
	}
	if _, err = s.service.deps.Registry.Transition(finalCtx, record.Handle, PhaseRunning, PhaseFinalizing, "", "", s.service.deps.Owner, s.service.deps.Clock.Now()); err != nil {
		return
	}
	if _, err = s.service.deps.Registry.Transition(finalCtx, record.Handle, PhaseFinalizing, PhaseTerminal, status, result.PackageDir, "", s.service.deps.Clock.Now()); err != nil {
		return
	}
}

// watchCancellation turns a durable cross-process request into child-context cancellation.
func (s *Supervisor) watchCancellation(ctx context.Context, handle string, cancel context.CancelFunc, done chan<- struct{}) {
	defer close(done)
	for {
		record, err := s.service.deps.Registry.Get(ctx, handle)
		if err == nil && record.CancelKeyHash != "" {
			cancel()
			return
		}
		timer := time.NewTimer(10 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
	}
}

// finish removes only the worker instance that completed.
func (s *Supervisor) finish(handle string, completed *worker) {
	s.mu.Lock()
	if s.workers[handle] == completed {
		delete(s.workers, handle)
	}
	s.mu.Unlock()
}

// Wait observes completion without taking ownership of the worker channel.
func (s *Supervisor) Wait(ctx context.Context, handle string) error {
	s.mu.Lock()
	entry := s.workers[handle]
	s.mu.Unlock()
	if entry == nil {
		return nil
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-entry.done:
		return nil
	}
}

// Cancel signals one worker and waits until all child work has returned.
func (s *Supervisor) Cancel(ctx context.Context, handle string) error {
	s.mu.Lock()
	entry := s.workers[handle]
	s.mu.Unlock()
	if entry == nil {
		return ErrWorkerMissing
	}
	entry.cancel()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-entry.done:
		return nil
	}
}

// Close cancels the supervisor root and joins a stable snapshot of every worker.
func (s *Supervisor) Close(ctx context.Context) error {
	s.cancel()
	s.mu.Lock()
	workers := make([]*worker, 0, len(s.workers))
	for _, entry := range s.workers {
		workers = append(workers, entry)
	}
	s.mu.Unlock()
	for _, entry := range workers {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-entry.done:
		}
	}
	return nil
}
