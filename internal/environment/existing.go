package environment

import (
	"context"
)

// ExistingBinding is the complete safe projection returned by the existing preview lifecycle.
type ExistingBinding struct {
	ProviderID, ProviderVersion, EnvironmentID, LifecycleHandle string
	EnvironmentRef, ProfileID, ReservationID, Fingerprint       string
	Endpoint, ObservedVersion, ObservationDigest                string
	Availability                                                Availability
	Attestation                                                 Attestation
	Network                                                     NetworkPolicy
	Capabilities                                                []string
}

// ExistingLifecycle is the narrow consumer interface over feature 19 preview coordination.
type ExistingLifecycle interface {
	AcquireExisting(context.Context, string, Selection, string, string) (ExistingBinding, error)
	RestoreExisting(context.Context, string, Selection, string, string) (ExistingBinding, error)
	ReleaseExisting(context.Context, string) error
}

// ExistingURLProvider adapts non-destructive preview coordination to EnvironmentProvider.
type ExistingURLProvider struct {
	Lifecycle ExistingLifecycle
}

// NewExistingURLProvider validates the delegated lifecycle without provisioning anything.
func NewExistingURLProvider(lifecycle ExistingLifecycle) (*ExistingURLProvider, error) {
	if lifecycle == nil {
		return nil, &Error{Category: CategoryInvalid, Operation: "existing_provider"}
	}
	return &ExistingURLProvider{Lifecycle: lifecycle}, nil
}

// Acquire delegates exactly one non-destructive acquire to the existing coordinator.
func (p *ExistingURLProvider) Acquire(ctx context.Context, request AcquisitionRequest) (Observation, error) {
	if err := ctx.Err(); err != nil {
		return Observation{}, contextFailure(ctx, "existing_acquire", CategoryCancelled, err)
	}
	if !validExistingSelection(request.Selection) {
		return Observation{}, &Error{Category: CategoryUnauthorized, Operation: "existing_kind"}
	}
	binding, err := p.Lifecycle.AcquireExisting(ctx, request.RunID, request.Selection, request.RepositoryID, request.CommitOID)
	if err != nil {
		return Observation{}, err
	}
	if binding.ProviderID != request.Selection.ProviderID || binding.ProviderVersion != request.Selection.ProviderVersion {
		return Observation{}, &Error{Category: CategoryIntegrity, Operation: "existing_identity"}
	}
	return existingObservation(binding), nil
}

// Reconcile restores the same durable preview instead of reserving a second target.
func (p *ExistingURLProvider) Reconcile(ctx context.Context, request AcquisitionRequest) (Observation, error) {
	if err := ctx.Err(); err != nil {
		return Observation{}, contextFailure(ctx, "existing_reconcile", CategoryCancelled, err)
	}
	if !validExistingSelection(request.Selection) {
		return Observation{}, &Error{Category: CategoryUnauthorized, Operation: "existing_reconcile"}
	}
	binding, err := p.Lifecycle.RestoreExisting(ctx, request.RunID, request.Selection, request.RepositoryID, request.CommitOID)
	if err != nil {
		return Observation{}, err
	}
	if binding.ProviderID != request.Selection.ProviderID || binding.ProviderVersion != request.Selection.ProviderVersion {
		return Observation{}, &Error{Category: CategoryIntegrity, Operation: "existing_identity"}
	}
	return existingObservation(binding), nil
}

// Observe restores current authority so restart and drift never trust caller state.
func (p *ExistingURLProvider) Observe(ctx context.Context, request ObservationRequest) (Observation, error) {
	if err := ctx.Err(); err != nil {
		return Observation{}, contextFailure(ctx, "existing_observe", CategoryCancelled, err)
	}
	if !validExistingSelection(request.Selection) {
		return Observation{}, &Error{Category: CategoryUnauthorized, Operation: "existing_observe"}
	}
	binding, err := p.Lifecycle.RestoreExisting(ctx, request.RunID, request.Selection, request.RepositoryID, request.CommitOID)
	if err != nil {
		return Observation{}, err
	}
	observation := existingObservation(binding)
	if binding.ProviderID != request.Selection.ProviderID || binding.ProviderVersion != request.Selection.ProviderVersion || observation.EnvironmentID != request.EnvironmentID || lifecycleDigest(request.Selection, observation) != request.LifecycleDigest {
		return Observation{}, &Error{Category: CategoryIntegrity, Operation: "existing_observation"}
	}
	return observation, nil
}

// Release performs only the existing coordinator's logical idempotent release.
func (p *ExistingURLProvider) Release(ctx context.Context, request ReleaseRequest) error {
	if err := ctx.Err(); err != nil {
		return contextFailure(ctx, "existing_release", CategoryCancelled, err)
	}
	if !validExistingSelection(request.Selection) || !digestPattern.MatchString(request.AcquisitionKey) || !digestPattern.MatchString(request.IdempotencyKey) || !digestPattern.MatchString(request.LifecycleDigest) || request.ProjectID != request.Selection.ProjectID || request.RepositoryID != request.Selection.RepositoryID || !fullOID.MatchString(request.CommitOID) || request.EnvironmentID == "" || request.LifecycleHandle == "" {
		return &Error{Category: CategoryUnauthorized, Operation: "existing_release"}
	}
	return p.Lifecycle.ReleaseExisting(ctx, request.RunID)
}

// validExistingSelection verifies exact sealed existing-URL authority on every adapter operation.
func validExistingSelection(selection Selection) bool {
	sealed, err := SealSelection(selection)
	return err == nil && sealed.Digest == selection.Digest && selection.Kind == KindExistingURL
}

// existingObservation translates the feature 19 binding without widening authority.
func existingObservation(value ExistingBinding) Observation {
	return Observation{ProviderID: value.ProviderID, ProviderVersion: value.ProviderVersion, EnvironmentID: value.EnvironmentID, EnvironmentRef: value.EnvironmentRef, ProfileID: value.ProfileID, ReservationID: value.ReservationID, Fingerprint: value.Fingerprint, Endpoint: value.Endpoint, LifecycleHandle: value.LifecycleHandle, ObservedVersion: value.ObservedVersion, ObservationDigest: value.ObservationDigest, Availability: value.Availability, Attestation: value.Attestation, Network: value.Network, Capabilities: append([]string(nil), value.Capabilities...)}
}

// cloneExisting detaches all collection authority stored for in-process observation.
func cloneExisting(value ExistingBinding) ExistingBinding {
	value.Capabilities = append([]string(nil), value.Capabilities...)
	value.Network.Schemes = append([]string(nil), value.Network.Schemes...)
	value.Network.Hosts = append([]string(nil), value.Network.Hosts...)
	value.Network.Ports = append([]int(nil), value.Network.Ports...)
	value.Network.BasePaths = append([]string(nil), value.Network.BasePaths...)
	return value
}
