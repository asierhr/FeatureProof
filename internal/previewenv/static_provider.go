package previewenv

import (
	"context"
	"regexp"
	"sync"
)

// staticReservationKey accepts only coordinator-produced SHA-256 hex identities.
var staticReservationKey = regexp.MustCompile(`^[a-f0-9]{64}$`)

// StaticProvider observes trusted configured existing previews and manages only logical local reservations.
type StaticProvider struct {
	mu           sync.Mutex
	providerID   string
	environments map[string]Snapshot
	reservations map[string]Snapshot
	released     map[string]bool
}

// NewStaticProvider validates configured observations without acquiring or modifying infrastructure.
func NewStaticProvider(providerID string, environments map[string]Snapshot) (*StaticProvider, error) {
	if !safeID.MatchString(providerID) || len(environments) == 0 {
		return nil, &Error{Code: CodeInvalid, Operation: "static_provider"}
	}
	copied := make(map[string]Snapshot, len(environments))
	for reference, snapshot := range environments {
		if !safeID.MatchString(reference) || snapshot.ProviderID != providerID {
			return nil, &Error{Code: CodeInvalid, Operation: "static_provider"}
		}
		sealed, err := SealSnapshot(snapshot)
		if err != nil {
			return nil, err
		}
		copied[reference] = sealed
	}
	return &StaticProvider{providerID: providerID, environments: copied, reservations: map[string]Snapshot{}, released: map[string]bool{}}, nil
}

// Reserve returns one configured observation under a stable logical reservation key.
func (p *StaticProvider) Reserve(ctx context.Context, request ReserveRequest) (Snapshot, error) {
	if err := ctx.Err(); err != nil {
		return Snapshot{}, err
	}
	if request.Selection.ProviderID != p.providerID || !staticReservationKey.MatchString(request.ReservationKey) || !fullOID.MatchString(request.Expected.CommitOID) || !safeID.MatchString(request.Expected.RepositoryID) {
		return Snapshot{}, &Error{Code: CodeInvalid, Operation: "reserve"}
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if prior, ok := p.reservations[request.ReservationKey]; ok {
		return prior, nil
	}
	snapshot, ok := p.environments[request.Selection.EnvironmentRef]
	if !ok {
		return Snapshot{}, &Error{Code: CodeUnavailable, Operation: "reserve"}
	}
	snapshot.ReservationID = "reservation-" + request.ReservationKey[:32]
	snapshot, _ = SealSnapshot(snapshot)
	p.reservations[request.ReservationKey] = snapshot
	return snapshot, nil
}

// Reconcile recovers the same logical reservation without allocating another.
func (p *StaticProvider) Reconcile(ctx context.Context, request ReserveRequest) (Snapshot, error) {
	return p.Reserve(ctx, request)
}

// Readiness re-observes the configured immutable existing preview.
func (p *StaticProvider) Readiness(ctx context.Context, environmentID, reservationID string) (Snapshot, error) {
	if err := ctx.Err(); err != nil {
		return Snapshot{}, err
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, snapshot := range p.reservations {
		if snapshot.EnvironmentID == environmentID && snapshot.ReservationID == reservationID && !p.released[reservationID] {
			return snapshot, nil
		}
	}
	return Snapshot{}, &Error{Code: CodeUnavailable, Operation: "readiness"}
}

// Release relinquishes only the local logical reservation and is idempotent.
func (p *StaticProvider) Release(ctx context.Context, request ReleaseRequest) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if request.ProviderID != p.providerID || !staticReservationKey.MatchString(request.ReleaseKey) {
		return &Error{Code: CodeInvalid, Operation: "release"}
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, snapshot := range p.reservations {
		if snapshot.EnvironmentID == request.EnvironmentID && snapshot.ReservationID == request.ReservationID {
			p.released[request.ReservationID] = true
			return nil
		}
	}
	return &Error{Code: CodeUnavailable, Operation: "release"}
}
