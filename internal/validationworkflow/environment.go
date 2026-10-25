package validationworkflow

import (
	"context"
	"errors"

	"github.com/asierhr/featureProof/internal/environment"
)

// classifyEnvironmentFailure maps provider-neutral operational categories without inventing functional failures.
func classifyEnvironmentFailure(ctx context.Context, err error) (Outcome, string) {
	if environment.IsCategory(err, environment.CategoryCancelled) || errors.Is(context.Cause(ctx), context.Canceled) {
		return OutcomeCancelled, string(environment.CategoryCancelled)
	}
	if environment.IsCategory(err, environment.CategoryTimeout) {
		return OutcomeInconclusive, string(environment.CategoryTimeout)
	}
	for _, category := range []environment.ErrorCategory{environment.CategoryUnavailable, environment.CategoryTransient, environment.CategoryRateLimited, environment.CategoryIntegrity, environment.CategoryUnauthorized, environment.CategoryCleanupPending} {
		if environment.IsCategory(err, category) {
			return OutcomeInconclusive, string(category)
		}
	}
	for _, category := range []environment.ErrorCategory{environment.CategoryInvalid, environment.CategoryInternal, environment.CategoryPersistence} {
		if environment.IsCategory(err, category) {
			return OutcomeError, string(category)
		}
	}
	if err != nil {
		return OutcomeInconclusive, string(environment.CategoryUnavailable)
	}
	return OutcomeInconclusive, string(environment.CategoryIntegrity)
}

// EnvironmentLifecycle is the provider-neutral use case consumed by validationworkflow.
type EnvironmentLifecycle interface {
	Acquire(context.Context, environment.Request) (environment.Acquisition, error)
	Restore(context.Context, environment.Request) (environment.Acquisition, error)
	Release(context.Context, string) error
}

// LifecycleEnvironmentPort adapts the canonical environment lifecycle to the workflow target projection.
type LifecycleEnvironmentPort struct {
	Lifecycle EnvironmentLifecycle
	Projects  map[string]string
}

// Acquire resolves the trusted alias through the lifecycle before planning can begin.
func (p LifecycleEnvironmentPort) Acquire(ctx context.Context, handle string, command StartCommand) (EnvironmentBinding, error) {
	request, err := p.request(handle, command)
	if err != nil {
		return EnvironmentBinding{}, err
	}
	acquisition, err := p.Lifecycle.Acquire(ctx, request)
	if err != nil {
		return EnvironmentBinding{}, err
	}
	return workflowEnvironmentBinding(acquisition, command), nil
}

// Restore revalidates provider, catalog, attestation and policy through current composition authority.
func (p LifecycleEnvironmentPort) Restore(ctx context.Context, handle string, command StartCommand) (EnvironmentBinding, error) {
	request, err := p.request(handle, command)
	if err != nil {
		return EnvironmentBinding{}, err
	}
	acquisition, err := p.Lifecycle.Restore(ctx, request)
	if err != nil {
		return EnvironmentBinding{}, err
	}
	return workflowEnvironmentBinding(acquisition, command), nil
}

// Revalidate compares the historical safe projection with authority reconstructed by the current composition.
func (p LifecycleEnvironmentPort) Revalidate(ctx context.Context, handle string, command StartCommand, _ EnvironmentBinding) (EnvironmentBinding, error) {
	current, err := p.Restore(ctx, handle, command)
	if err != nil {
		return EnvironmentBinding{}, err
	}
	return current, nil
}

// Release delegates the single durable cleanup intent to the provider-neutral lifecycle.
func (p LifecycleEnvironmentPort) Release(ctx context.Context, handle string) error {
	if p.Lifecycle == nil {
		return errors.New("environment lifecycle unavailable")
	}
	return p.Lifecycle.Release(ctx, handle)
}

// request projects only run/revision and the composition-owned alias into environment authority.
func (p LifecycleEnvironmentPort) request(handle string, command StartCommand) (environment.Request, error) {
	alias := ""
	if command.Target.Direct != nil {
		alias = command.Target.Direct.Alias
	} else if command.Target.Preview != nil {
		alias = command.Target.Preview.Alias
	}
	projectID := p.Projects[alias]
	if p.Lifecycle == nil || alias == "" || projectID == "" {
		return environment.Request{}, errors.New("environment selection unavailable")
	}
	return environment.Request{RunID: handle, SelectionAlias: alias, ProjectID: projectID, RepositoryID: command.RepositoryID, CommitOID: command.Revision, IdempotencyKey: command.IdempotencyKey}, nil
}

// workflowEnvironmentBinding projects the canonical target without exposing its raw lifecycle handle.
func workflowEnvironmentBinding(acquisition environment.Acquisition, command StartCommand) EnvironmentBinding {
	target := acquisition.Target
	kind, alias := "", ""
	if command.Target.Direct != nil {
		kind, alias = "direct", command.Target.Direct.Alias
	} else if command.Target.Preview != nil {
		kind, alias = "preview", command.Target.Preview.Alias
	}
	return EnvironmentBinding{Target: TargetContext{Kind: kind, Alias: alias, URL: target.Endpoint, SelectionDigest: target.SelectionDigest, ProviderID: target.ProviderID, ProviderVersion: target.ProviderVersion, EnvironmentRef: target.EnvironmentRef, ProfileID: target.ProfileID, EnvironmentID: target.EnvironmentID, ReservationID: target.ReservationID, ObservedVersion: target.ObservedVersion, Fingerprint: target.Fingerprint, LifecycleDigest: target.LifecycleDigest, Capabilities: append([]string(nil), target.Capabilities...), Attestation: AttestationContext{EnvironmentID: target.Attestation.EnvironmentID, RepositoryID: target.Attestation.RepositoryID, CommitOID: target.Attestation.CommitOID, Reference: target.Attestation.Reference, ValidUntil: target.Attestation.ValidUntil, Verified: target.Attestation.Verified}, Network: NetworkContext{PolicyID: target.Network.PolicyID, Schemes: append([]string(nil), target.Network.Schemes...), Hosts: append([]string(nil), target.Network.Hosts...), Ports: append([]int(nil), target.Network.Ports...), BasePaths: append([]string(nil), target.Network.BasePaths...), MaxURLBytes: target.Network.MaxURLBytes}}}
}
