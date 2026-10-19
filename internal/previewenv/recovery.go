package previewenv

import (
	"context"
	"sync"
	"time"
)

// RecoverySupervisor schedules reclaim when a crashed owner's durable lease expires.
type RecoverySupervisor struct {
	coordinator *Coordinator
	cancel      context.CancelFunc
	done        chan struct{}
	once        sync.Once
}

// StartRecoverySupervisor starts one owned recovery loop for a composed coordinator.
func StartRecoverySupervisor(coordinator *Coordinator) (*RecoverySupervisor, error) {
	if coordinator == nil {
		return nil, &Error{Code: CodeInvalid, Operation: "recovery_supervisor"}
	}
	ctx, cancel := context.WithCancel(context.Background())
	supervisor := &RecoverySupervisor{coordinator: coordinator, cancel: cancel, done: make(chan struct{})}
	go supervisor.run(ctx)
	return supervisor, nil
}

// run retries recovery immediately and again at the earliest live lease boundary.
func (s *RecoverySupervisor) run(ctx context.Context) {
	defer close(s.done)
	for {
		_ = s.coordinator.Recover(ctx)
		delay := s.coordinator.config.ReadinessWait
		records, err := s.coordinator.deps.Store.Pending(ctx)
		if err == nil {
			now := s.coordinator.deps.Clock.Now()
			for _, record := range records {
				remaining := record.LeaseUntil.Sub(now)
				if remaining > 0 && (delay <= 0 || remaining < delay) {
					delay = remaining
				}
			}
		}
		if delay <= 0 {
			delay = time.Millisecond
		}
		if waitErr := s.coordinator.deps.Clock.Wait(ctx, delay); waitErr != nil {
			return
		}
	}
}

// Close cancels and joins the recovery loop within the caller deadline.
func (s *RecoverySupervisor) Close(ctx context.Context) error {
	if s == nil {
		return nil
	}
	s.once.Do(s.cancel)
	select {
	case <-s.done:
		return nil
	case <-ctx.Done():
		return &Error{Code: CodeTimeout, Operation: "recovery_shutdown"}
	}
}
