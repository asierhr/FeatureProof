package previewenv

import (
	"context"
	"sync"
)

// FakeProvider is a programmable auditable provider with no network or infrastructure operations.
type FakeProvider struct {
	mu                                            sync.Mutex
	ReserveSnapshots, ReadinessSnapshots          []Snapshot
	ReserveErrors, ReadinessErrors, ReleaseErrors []error
	Reservations                                  map[string]ReserveRequest
	Calls                                         []string
}

// Reserve returns the programmed existing-preview observation and enforces key consistency.
func (f *FakeProvider) Reserve(ctx context.Context, request ReserveRequest) (Snapshot, error) {
	return f.reserve(ctx, request, "reserve")
}

// Reconcile returns the same logical reservation for a previously used stable key.
func (f *FakeProvider) Reconcile(ctx context.Context, request ReserveRequest) (Snapshot, error) {
	return f.reserve(ctx, request, "reconcile")
}

// reserve implements idempotent fake reservation without provisioning behavior.
func (f *FakeProvider) reserve(ctx context.Context, request ReserveRequest, operation string) (Snapshot, error) {
	if err := ctx.Err(); err != nil {
		return Snapshot{}, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.Calls = append(f.Calls, operation)
	if f.Reservations == nil {
		f.Reservations = map[string]ReserveRequest{}
	}
	if existing, ok := f.Reservations[request.ReservationKey]; ok && (existing.Selection != request.Selection || existing.Expected != request.Expected) {
		return Snapshot{}, &Error{Code: CodeConflict, Operation: operation}
	}
	f.Reservations[request.ReservationKey] = request
	if len(f.ReserveErrors) > 0 {
		err := f.ReserveErrors[0]
		f.ReserveErrors = f.ReserveErrors[1:]
		if err != nil {
			return Snapshot{}, err
		}
	}
	if len(f.ReserveSnapshots) == 0 {
		return Snapshot{}, &Error{Code: CodeUnavailable, Operation: operation}
	}
	if len(f.ReserveSnapshots) == 1 {
		return f.ReserveSnapshots[0], nil
	}
	value := f.ReserveSnapshots[0]
	f.ReserveSnapshots = f.ReserveSnapshots[1:]
	return value, nil
}

// Readiness returns the next programmed safe observation.
func (f *FakeProvider) Readiness(ctx context.Context, _, _ string) (Snapshot, error) {
	if err := ctx.Err(); err != nil {
		return Snapshot{}, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.Calls = append(f.Calls, "readiness")
	if len(f.ReadinessErrors) > 0 {
		err := f.ReadinessErrors[0]
		f.ReadinessErrors = f.ReadinessErrors[1:]
		if err != nil {
			return Snapshot{}, err
		}
	}
	if len(f.ReadinessSnapshots) == 0 {
		return Snapshot{}, &Error{Code: CodeUnavailable, Operation: "readiness"}
	}
	if len(f.ReadinessSnapshots) == 1 {
		return f.ReadinessSnapshots[0], nil
	}
	value := f.ReadinessSnapshots[0]
	f.ReadinessSnapshots = f.ReadinessSnapshots[1:]
	return value, nil
}

// Release records logical release and returns the next programmed result.
func (f *FakeProvider) Release(ctx context.Context, _ ReleaseRequest) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.Calls = append(f.Calls, "release")
	if len(f.ReleaseErrors) == 0 {
		return nil
	}
	err := f.ReleaseErrors[0]
	f.ReleaseErrors = f.ReleaseErrors[1:]
	return err
}

// Audit returns a defensive copy of observed contract operations.
func (f *FakeProvider) Audit() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.Calls...)
}
