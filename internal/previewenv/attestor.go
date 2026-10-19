package previewenv

import (
	"context"
	"sync"
	"time"

	"github.com/asierhr/featureProof/internal/revisionbinding"
)

// AttestorBridge converts only the latest verified reservation snapshot to revisionbinding evidence.
type AttestorBridge struct {
	mu       sync.RWMutex
	snapshot Snapshot
	expected ExpectedRevision
	now      func() time.Time
}

// RefreshingAttestor re-observes the provider before every revisionbinding recheck.
type RefreshingAttestor struct {
	Provider Provider
	Bridge   *AttestorBridge
	Timeout  time.Duration
}

// Attest refreshes readiness and identity before returning the bound proof.
func (a *RefreshingAttestor) Attest(ctx context.Context, environmentID string) (revisionbinding.Attestation, error) {
	if a.Provider == nil || a.Bridge == nil || a.Timeout <= 0 {
		return revisionbinding.Attestation{}, &Error{Code: CodeInvalid, Operation: "refresh_attestor"}
	}
	a.Bridge.mu.RLock()
	current := a.Bridge.snapshot
	a.Bridge.mu.RUnlock()
	bounded, cancel := context.WithTimeout(ctx, a.Timeout)
	defer cancel()
	observed, err := a.Provider.Readiness(bounded, current.EnvironmentID, current.ReservationID)
	if err != nil {
		return revisionbinding.Attestation{}, classifyContext(err, "refresh_attestor")
	}
	sealed, err := SealSnapshot(observed)
	if err != nil || sealed.Availability != AvailabilityReady || a.Bridge.Refresh(sealed) != nil {
		return revisionbinding.Attestation{}, &Error{Code: CodeIntegrity, Operation: "refresh_attestor"}
	}
	return a.Bridge.Attest(ctx, environmentID)
}

// NewAttestorBridge validates the admitted snapshot before exposing an attestor.
func NewAttestorBridge(snapshot Snapshot, expected ExpectedRevision, now func() time.Time) (*AttestorBridge, error) {
	if now == nil || VerifySnapshot(snapshot, expected, now()) != nil {
		return nil, &Error{Code: CodeIntegrity, Operation: "attestor"}
	}
	return &AttestorBridge{snapshot: snapshot, expected: expected, now: now}, nil
}

// Attest returns the safe revision proof for exactly the admitted environment.
func (b *AttestorBridge) Attest(ctx context.Context, environmentID string) (revisionbinding.Attestation, error) {
	if err := ctx.Err(); err != nil {
		return revisionbinding.Attestation{}, &Error{Code: CodeCancelled, Operation: "attest"}
	}
	b.mu.RLock()
	snapshot := b.snapshot
	b.mu.RUnlock()
	if snapshot.EnvironmentID != environmentID || VerifySnapshot(snapshot, b.expected, b.now()) != nil {
		return revisionbinding.Attestation{}, &Error{Code: CodeIntegrity, Operation: "attest"}
	}
	return revisionbinding.Attestation{EnvironmentID: snapshot.EnvironmentID, RepositoryID: snapshot.RepositoryID, CommitOID: snapshot.CommitOID, Reference: snapshot.AttestationRef, ValidUntil: snapshot.AttestationValidUntil, Verified: true}, nil
}

// Refresh replaces the proof only when the complete reservation identity remains stable.
func (b *AttestorBridge) Refresh(snapshot Snapshot) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if VerifySnapshot(snapshot, b.expected, b.now()) != nil || !SameReservation(b.snapshot, snapshot) {
		return &Error{Code: CodeIntegrity, Operation: "refresh"}
	}
	b.snapshot = snapshot
	return nil
}
