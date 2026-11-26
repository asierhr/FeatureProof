package environment

import (
	"context"
	"errors"
	"testing"
)

// denyingAcquireQuota proves provider acquisition is unreachable after quota denial.
type denyingAcquireQuota struct{ err error }

// ReserveAcquire returns the configured environment-capacity denial.
func (quota denyingAcquireQuota) ReserveAcquire(context.Context, AcquisitionRequest) (AcquireQuotaReceipt, error) {
	return AcquireQuotaReceipt{}, quota.err
}

// ResolveAcquire is unreachable without a reservation.
func (denyingAcquireQuota) ResolveAcquire(context.Context, AcquireQuotaReceipt, Observation, bool) error {
	return nil
}

// ReleaseAcquire is unreachable without a reservation.
func (denyingAcquireQuota) ReleaseAcquire(context.Context, ReleaseRequest, bool) error { return nil }

// countingEnvironmentProvider records external provider calls made by the quota wrapper.
type countingEnvironmentProvider struct{ calls int }

// Acquire records one provider acquisition.
func (provider *countingEnvironmentProvider) Acquire(context.Context, AcquisitionRequest) (Observation, error) {
	provider.calls++
	return Observation{}, nil
}

// Reconcile returns an empty safe observation for interface completeness.
func (*countingEnvironmentProvider) Reconcile(context.Context, AcquisitionRequest) (Observation, error) {
	return Observation{}, nil
}

// Observe returns an empty safe observation for interface completeness.
func (*countingEnvironmentProvider) Observe(context.Context, ObservationRequest) (Observation, error) {
	return Observation{}, nil
}

// Release confirms no-op cleanup for interface completeness.
func (*countingEnvironmentProvider) Release(context.Context, ReleaseRequest) error { return nil }

// recordingAcquireQuota captures resolution ordering and uncertainty.
type recordingAcquireQuota struct {
	resolveErr       error
	releaseErr       error
	resolved         bool
	uncertain        bool
	released         bool
	releaseUncertain bool
}

// ReserveAcquire confirms one stable reservation.
func (*recordingAcquireQuota) ReserveAcquire(context.Context, AcquisitionRequest) (AcquireQuotaReceipt, error) {
	return AcquireQuotaReceipt{ID: "receipt", Digest: "digest"}, nil
}

// ResolveAcquire records whether provider failure was conservatively retained.
func (quota *recordingAcquireQuota) ResolveAcquire(_ context.Context, _ AcquireQuotaReceipt, _ Observation, uncertain bool) error {
	quota.resolved, quota.uncertain = true, uncertain
	return quota.resolveErr
}

// ReleaseAcquire records terminal cleanup reconciliation separately from acquisition.
func (quota *recordingAcquireQuota) ReleaseAcquire(_ context.Context, _ ReleaseRequest, uncertain bool) error {
	quota.released, quota.releaseUncertain = true, uncertain
	return quota.releaseErr
}

// scriptedEnvironmentProvider returns explicit provider outcomes and records forwarding.
type scriptedEnvironmentProvider struct {
	acquireErr error
	calls      int
}

// Acquire returns the scripted effect outcome.
func (provider *scriptedEnvironmentProvider) Acquire(context.Context, AcquisitionRequest) (Observation, error) {
	provider.calls++
	return Observation{}, provider.acquireErr
}

// Reconcile records lifecycle repair forwarding after admitted acquisition.
func (provider *scriptedEnvironmentProvider) Reconcile(context.Context, AcquisitionRequest) (Observation, error) {
	provider.calls++
	return Observation{}, nil
}

// Observe records read-only provider forwarding without another reservation.
func (provider *scriptedEnvironmentProvider) Observe(context.Context, ObservationRequest) (Observation, error) {
	provider.calls++
	return Observation{}, nil
}

// Release records cleanup forwarding without allocating new quota.
func (provider *scriptedEnvironmentProvider) Release(context.Context, ReleaseRequest) error {
	provider.calls++
	return nil
}

// TestQuotaEnvironmentProviderRequiresGuardAndDeniesBeforeAcquire covers mandatory provider quota composition.
func TestQuotaEnvironmentProviderRequiresGuardAndDeniesBeforeAcquire(t *testing.T) {
	next := &countingEnvironmentProvider{}
	if _, err := NewQuotaEnvironmentProvider(next, nil); !IsCategory(err, CategoryInvalid) {
		t.Fatalf("missing quota=%v", err)
	}
	denial := errors.New("environment quota denied")
	provider, _ := NewQuotaEnvironmentProvider(next, denyingAcquireQuota{err: denial})
	if _, err := provider.Acquire(context.Background(), AcquisitionRequest{}); !errors.Is(err, denial) || next.calls != 0 {
		t.Fatalf("acquire=%v calls=%d", err, next.calls)
	}
}

// TestQuotaEnvironmentProviderResolvesEffectsAndDelegatesReads covers both
// terminal and uncertain acquisition plus the previously admitted lifecycle.
func TestQuotaEnvironmentProviderResolvesEffectsAndDelegatesReads(t *testing.T) {
	next := &scriptedEnvironmentProvider{}
	quota := &recordingAcquireQuota{}
	provider, err := NewQuotaEnvironmentProvider(next, quota)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = provider.Acquire(context.Background(), AcquisitionRequest{}); err != nil || !quota.resolved || quota.uncertain {
		t.Fatalf("successful acquire quota=%+v err=%v", quota, err)
	}
	providerFailure := errors.New("provider unavailable")
	next.acquireErr = providerFailure
	quota.resolved = false
	if _, err = provider.Acquire(context.Background(), AcquisitionRequest{}); !errors.Is(err, providerFailure) || !quota.resolved || !quota.uncertain {
		t.Fatalf("failed acquire quota=%+v err=%v", quota, err)
	}
	resolveFailure := errors.New("quota resolution unavailable")
	quota.resolveErr = resolveFailure
	if _, err = provider.Acquire(context.Background(), AcquisitionRequest{}); !errors.Is(err, resolveFailure) {
		t.Fatalf("resolution precedence=%v", err)
	}
	_, _ = provider.Reconcile(context.Background(), AcquisitionRequest{})
	_, _ = provider.Observe(context.Background(), ObservationRequest{})
	_ = provider.Release(context.Background(), ReleaseRequest{})
	if next.calls != 6 || !quota.released || quota.releaseUncertain {
		t.Fatalf("provider calls=%d", next.calls)
	}
}
