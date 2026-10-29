package controlplane

import (
	"context"
	"time"
)

// AuthorityRequest contains composition-owned references allowed for one phase.
type AuthorityRequest struct {
	EnvelopeID          string
	WorkerID            string
	ExpiresAt           time.Time
	InputReferences     []string
	Capabilities        []string
	TargetBindingRef    string
	TargetBindingDigest string
	SecretReferenceIDs  []string
}

// BuildAuthority minimizes and seals worker authority from a live leased job.
func BuildAuthority(job Job, request AuthorityRequest, now time.Time) (AuthorityEnvelope, error) {
	if VerifyJob(job) != nil || job.State != JobLeased || request.WorkerID != job.Owner || request.ExpiresAt.After(job.LeaseUntil) {
		return AuthorityEnvelope{}, ErrUnauthorized
	}
	grant, err := SealAuthorityGrant(AuthorityGrant{ProjectID: job.ProjectID, RunID: job.RunID, BackendEpoch: job.BackendEpoch, BindingDigest: job.BindingDigest, Phase: job.Phase, InputReferences: request.InputReferences, AllowedCapabilities: request.Capabilities, TargetBindingRef: request.TargetBindingRef, TargetBindingDigest: request.TargetBindingDigest, SecretReferenceIDs: request.SecretReferenceIDs})
	if err != nil {
		return AuthorityEnvelope{}, ErrUnauthorized
	}
	value := AuthorityEnvelope{Version: ModelVersion, EnvelopeID: request.EnvelopeID, ProjectID: job.ProjectID, RunID: job.RunID, JobID: job.JobID, WorkerID: request.WorkerID, BackendEpoch: job.BackendEpoch, BindingDigest: job.BindingDigest, Phase: job.Phase, Attempt: job.Attempt, Fence: job.Fence, ExpiresAt: request.ExpiresAt, InputReferences: grant.InputReferences, AllowedCapabilities: grant.AllowedCapabilities, TargetBindingRef: grant.TargetBindingRef, TargetBindingDigest: grant.TargetBindingDigest, SecretReferenceIDs: grant.SecretReferenceIDs}
	return SealAuthority(value, now)
}

// DeriveAuthority loads persisted composition authority and never accepts capability lists from a worker caller.
func DeriveAuthority(ctx context.Context, grants AuthorityGrantStore, job Job, envelopeID, worker string, expiresAt, now time.Time) (AuthorityEnvelope, error) {
	if grants == nil || worker != job.Owner {
		return AuthorityEnvelope{}, ErrUnauthorized
	}
	grant, err := grants.LoadAuthorityGrant(ctx, job.RunID, job.Phase)
	if err != nil || VerifyAuthorityGrant(grant) != nil || grant.ProjectID != job.ProjectID || grant.RunID != job.RunID || grant.BackendEpoch != job.BackendEpoch || grant.BindingDigest != job.BindingDigest || grant.Phase != job.Phase {
		return AuthorityEnvelope{}, ErrUnauthorized
	}
	return BuildAuthority(job, AuthorityRequest{EnvelopeID: envelopeID, WorkerID: worker, ExpiresAt: expiresAt, InputReferences: grant.InputReferences, Capabilities: grant.AllowedCapabilities, TargetBindingRef: grant.TargetBindingRef, TargetBindingDigest: grant.TargetBindingDigest, SecretReferenceIDs: grant.SecretReferenceIDs}, now)
}

// ResolveAuthorizedSecrets validates every fence before invoking the private resolver.
func ResolveAuthorizedSecrets(ctx context.Context, envelope AuthorityEnvelope, job Job, worker string, now time.Time, resolver SecretResolver) (map[string]string, error) {
	if resolver == nil {
		return nil, ErrInvalid
	}
	if err := VerifyAuthority(envelope, job, worker, now); err != nil {
		return nil, err
	}
	return resolver.Resolve(ctx, append([]string(nil), envelope.SecretReferenceIDs...))
}

// ResolvePersistedSecrets performs a fresh durable authority check before invoking the private JIT resolver.
func ResolvePersistedSecrets(ctx context.Context, verifier LiveAuthorityVerifier, envelope AuthorityEnvelope, worker string, now time.Time, resolver SecretResolver) (map[string]string, error) {
	if verifier == nil || resolver == nil || len(envelope.SecretReferenceIDs) == 0 {
		return nil, ErrUnauthorized
	}
	if err := verifier.VerifyLiveAuthority(ctx, envelope, worker, now); err != nil {
		return nil, err
	}
	return resolver.Resolve(ctx, append([]string(nil), envelope.SecretReferenceIDs...))
}
