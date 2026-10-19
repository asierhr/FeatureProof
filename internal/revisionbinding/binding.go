// Package revisionbinding fixes a validation to a verified source and deployment revision.
package revisionbinding

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"regexp"
	"time"
)

// Category is a stable, value-free rejection reason at the revision boundary.
type Category string

const (
	InvalidRevision       Category = "invalid_revision"
	UnknownCommit         Category = "unknown_commit"
	DirtyWorktree         Category = "dirty_worktree"
	UnverifiedEnvironment Category = "unverified_environment"
	RevisionMismatch      Category = "revision_mismatch"
	BindingIntegrity      Category = "binding_integrity"
	Cancelled             Category = "cancelled"
)

// Error carries only a safe category and phase; untrusted command output is never wrapped.
type Error struct {
	Category Category
	Phase    string
}

// Error renders the category and phase without repository paths or attestation payloads.
func (e *Error) Error() string { return string(e.Category) + ": " + e.Phase }

// Revision is the source identity observed by a trusted resolver.
type Revision struct {
	RepositoryID, CommitOID string
	Dirty                   bool
}

// Attestation is the deployment identity returned by a configured trusted source.
type Attestation struct {
	EnvironmentID, RepositoryID, CommitOID, Reference string
	ValidUntil                                        time.Time
	Verified                                          bool
}

// RevisionResolver resolves a selected local repository and ref without mutating either.
type RevisionResolver interface {
	Resolve(context.Context, string, string) (Revision, error)
}

// EnvironmentAttestor proves the selected environment's deployed revision.
type EnvironmentAttestor interface {
	Attest(context.Context, string) (Attestation, error)
}

// Selection fixes explicit source, ref and environment choices for admission.
type Selection struct{ RepositoryID, Ref, EnvironmentID string }

// Binding is an immutable value snapshot. Callers receive copies, never mutable maps or slices.
type Binding struct {
	Version             int    `json:"version"`
	RunID               string `json:"run_id"`
	RepositoryID        string `json:"repository_id"`
	CommitOID           string `json:"commit_oid"`
	EnvironmentID       string `json:"environment_id"`
	AttestationRef      string `json:"attestation_ref"`
	ConstraintsSHA256   string `json:"constraints_sha256"`
	ContractSHA256      string `json:"contract_sha256"`
	ConfigurationSHA256 string `json:"configuration_sha256"`
	BindingSHA256       string `json:"binding_sha256"`
}

// PlanBinding records an approved or approval-free batch against one base binding.
type PlanBinding struct {
	Version           int    `json:"version"`
	BindingSHA256     string `json:"binding_sha256"`
	Batch             int    `json:"batch"`
	PlanSHA256        string `json:"plan_sha256"`
	Mode              string `json:"mode"`
	ApprovalReference string `json:"approval_reference,omitempty"`
}

var safeID = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]{0,127}$`)
var digest = regexp.MustCompile(`^[0-9a-f]{64}$`)
var oid = regexp.MustCompile(`^(?:[0-9a-f]{40}|[0-9a-f]{64})$`)

// ValidID limits publicly persisted identities to opaque, non-path strings.
func ValidID(value string) bool { return safeID.MatchString(value) }

// ValidOID accepts complete SHA-1 or SHA-256 Git object IDs only.
func ValidOID(value string) bool { return oid.MatchString(value) }

// Admit verifies source and deployment before any functional work or run creation.
func Admit(ctx context.Context, selected Selection, resolver RevisionResolver, attestor EnvironmentAttestor, now time.Time) (Revision, Attestation, error) {
	if err := ctx.Err(); err != nil {
		return Revision{}, Attestation{}, &Error{Cancelled, "admission"}
	}
	if !ValidID(selected.RepositoryID) || !ValidID(selected.EnvironmentID) || selected.Ref == "" || resolver == nil || attestor == nil {
		return Revision{}, Attestation{}, &Error{InvalidRevision, "admission"}
	}
	revision, err := resolver.Resolve(ctx, selected.RepositoryID, selected.Ref)
	if err != nil {
		return Revision{}, Attestation{}, err
	}
	if err := ctx.Err(); err != nil {
		return Revision{}, Attestation{}, &Error{Cancelled, "revision"}
	}
	if revision.RepositoryID != selected.RepositoryID || !ValidOID(revision.CommitOID) {
		return Revision{}, Attestation{}, &Error{InvalidRevision, "revision"}
	}
	if revision.Dirty {
		return Revision{}, Attestation{}, &Error{DirtyWorktree, "revision"}
	}
	attestation, err := attestor.Attest(ctx, selected.EnvironmentID)
	if err != nil {
		return Revision{}, Attestation{}, &Error{UnverifiedEnvironment, "attestation"}
	}
	if err := ctx.Err(); err != nil {
		return Revision{}, Attestation{}, &Error{Cancelled, "attestation"}
	}
	if !attestation.Verified || !ValidID(attestation.EnvironmentID) || !ValidID(attestation.Reference) || !ValidID(attestation.RepositoryID) || !ValidOID(attestation.CommitOID) || !attestation.ValidUntil.After(now) || attestation.EnvironmentID != selected.EnvironmentID {
		return Revision{}, Attestation{}, &Error{UnverifiedEnvironment, "attestation"}
	}
	if attestation.RepositoryID != revision.RepositoryID || attestation.CommitOID != revision.CommitOID {
		return Revision{}, Attestation{}, &Error{RevisionMismatch, "attestation"}
	}
	return revision, attestation, nil
}

// Recheck fails closed when source or deployment drift from an already sealed run.
func Recheck(ctx context.Context, selected Selection, resolver RevisionResolver, attestor EnvironmentAttestor, now time.Time, base Binding) error {
	if err := base.Verify(ctx); err != nil {
		return err
	}
	revision, attestation, err := Admit(ctx, selected, resolver, attestor, now)
	if err != nil {
		return err
	}
	if revision.RepositoryID != base.RepositoryID || revision.CommitOID != base.CommitOID || attestation.EnvironmentID != base.EnvironmentID || attestation.Reference != base.AttestationRef {
		return &Error{RevisionMismatch, "recheck"}
	}
	return nil
}

// NewBinding seals an admitted identity and three already-projected safe digests.
func NewBinding(ctx context.Context, runID string, revision Revision, attestation Attestation, constraints, contract, config string) (Binding, error) {
	if err := ctx.Err(); err != nil {
		return Binding{}, &Error{Cancelled, "binding"}
	}
	if !ValidID(runID) || !ValidID(revision.RepositoryID) || !ValidOID(revision.CommitOID) || revision.Dirty || !attestation.Verified || !ValidID(attestation.EnvironmentID) || !ValidID(attestation.Reference) || attestation.RepositoryID != revision.RepositoryID || attestation.CommitOID != revision.CommitOID || !digest.MatchString(constraints) || !digest.MatchString(contract) || !digest.MatchString(config) {
		return Binding{}, &Error{BindingIntegrity, "binding"}
	}
	b := Binding{Version: 1, RunID: runID, RepositoryID: revision.RepositoryID, CommitOID: revision.CommitOID, EnvironmentID: attestation.EnvironmentID, AttestationRef: attestation.Reference, ConstraintsSHA256: constraints, ContractSHA256: contract, ConfigurationSHA256: config}
	raw, err := json.Marshal(b)
	if err != nil {
		return Binding{}, &Error{BindingIntegrity, "binding"}
	}
	sum := sha256.Sum256(raw)
	b.BindingSHA256 = hex.EncodeToString(sum[:])
	return b, nil
}

// Verify detects persisted identity or digest tampering without trusting a stored hash.
func (b Binding) Verify(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return &Error{Cancelled, "verify"}
	}
	if b.Version != 1 || !digest.MatchString(b.BindingSHA256) {
		return &Error{BindingIntegrity, "verify"}
	}
	copy := b
	copy.BindingSHA256 = ""
	canonical, err := json.Marshal(copy)
	if err != nil {
		return &Error{BindingIntegrity, "verify"}
	}
	sum := sha256.Sum256(canonical)
	if hex.EncodeToString(sum[:]) != b.BindingSHA256 || !ValidID(b.RunID) || !ValidID(b.RepositoryID) || !ValidOID(b.CommitOID) || !ValidID(b.EnvironmentID) || !ValidID(b.AttestationRef) || !digest.MatchString(b.ConstraintsSHA256) || !digest.MatchString(b.ContractSHA256) || !digest.MatchString(b.ConfigurationSHA256) {
		return &Error{BindingIntegrity, "verify"}
	}
	return nil
}

// NewPlanBinding seals one ordered batch only after its exact plan authorization is confirmed.
func NewPlanBinding(ctx context.Context, base Binding, batch int, planDigest, mode string, authorized bool) (PlanBinding, error) {
	if err := base.Verify(ctx); err != nil {
		return PlanBinding{}, err
	}
	if batch < 1 || !digest.MatchString(planDigest) || (mode != "approved" && mode != "approval_not_required") || (mode == "approved" && !authorized) {
		return PlanBinding{}, &Error{BindingIntegrity, "plan"}
	}
	return PlanBinding{Version: 1, BindingSHA256: base.BindingSHA256, Batch: batch, PlanSHA256: planDigest, Mode: mode}, nil
}

// NewApprovedPlanBinding seals an approved batch together with its opaque consumed-grant reference.
func NewApprovedPlanBinding(ctx context.Context, base Binding, batch int, planDigest, approvalReference string) (PlanBinding, error) {
	binding, err := NewPlanBinding(ctx, base, batch, planDigest, "approved", true)
	if err != nil || !safeID.MatchString(approvalReference) {
		return PlanBinding{}, &Error{BindingIntegrity, "plan_approval"}
	}
	binding.ApprovalReference = approvalReference
	return binding, nil
}

// VerifyPlanChain rejects foreign, reordered or malformed batch bindings before publication.
func VerifyPlanChain(ctx context.Context, base Binding, plans []PlanBinding) error {
	if err := base.Verify(ctx); err != nil {
		return err
	}
	for index, plan := range plans {
		if err := ctx.Err(); err != nil {
			return &Error{Cancelled, "plan_chain"}
		}
		if plan.Version != 1 || plan.BindingSHA256 != base.BindingSHA256 || plan.Batch != index+1 || !digest.MatchString(plan.PlanSHA256) || (plan.Mode != "approved" && plan.Mode != "approval_not_required") {
			return &Error{BindingIntegrity, "plan_chain"}
		}
	}
	return nil
}

// IsCategory checks typed revision errors without inspecting untrusted message text.
func IsCategory(err error, category Category) bool {
	var typed *Error
	return errors.As(err, &typed) && typed.Category == category
}
