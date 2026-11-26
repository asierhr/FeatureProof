package environment

import "context"

// AcquireQuotaReceipt is the opaque environment/infrastructure reservation created before provider I/O.
type AcquireQuotaReceipt struct{ ID, Digest string }

// AcquireQuotaGate owns environment, infrastructure, object and storage reservations for one acquisition.
type AcquireQuotaGate interface {
	ReserveAcquire(context.Context, AcquisitionRequest) (AcquireQuotaReceipt, error)
	ResolveAcquire(context.Context, AcquireQuotaReceipt, Observation, bool) error
	ReleaseAcquire(context.Context, ReleaseRequest, bool) error
}

// QuotaEnvironmentProvider prevents an environment provider call without durable quota admission.
type QuotaEnvironmentProvider struct {
	next  EnvironmentProvider
	quota AcquireQuotaGate
}

// NewQuotaEnvironmentProvider rejects provider composition that removes its quota guard.
func NewQuotaEnvironmentProvider(next EnvironmentProvider, quota AcquireQuotaGate) (*QuotaEnvironmentProvider, error) {
	if next == nil || quota == nil {
		return nil, &Error{Category: CategoryInvalid, Operation: "quota_provider_compose"}
	}
	return &QuotaEnvironmentProvider{next: next, quota: quota}, nil
}

// Acquire reserves all declared capacity before provider I/O and retains uncertainty on failure.
func (provider *QuotaEnvironmentProvider) Acquire(ctx context.Context, request AcquisitionRequest) (Observation, error) {
	receipt, err := provider.quota.ReserveAcquire(ctx, request)
	if err != nil {
		return Observation{}, err
	}
	observation, acquireErr := provider.next.Acquire(ctx, request)
	if err = provider.quota.ResolveAcquire(ctx, receipt, observation, acquireErr != nil); err != nil {
		return Observation{}, err
	}
	return observation, acquireErr
}

// Reconcile delegates observation of a previously admitted provider attempt.
func (provider *QuotaEnvironmentProvider) Reconcile(ctx context.Context, request AcquisitionRequest) (Observation, error) {
	return provider.next.Reconcile(ctx, request)
}

// Observe delegates read-only lifecycle observation.
func (provider *QuotaEnvironmentProvider) Observe(ctx context.Context, request ObservationRequest) (Observation, error) {
	return provider.next.Observe(ctx, request)
}

// Release delegates provider cleanup and releases quota only after confirmed terminal cleanup.
func (provider *QuotaEnvironmentProvider) Release(ctx context.Context, request ReleaseRequest) error {
	releaseErr := provider.next.Release(ctx, request)
	if err := provider.quota.ReleaseAcquire(ctx, request, releaseErr != nil); err != nil {
		return err
	}
	return releaseErr
}

var _ EnvironmentProvider = (*QuotaEnvironmentProvider)(nil)
