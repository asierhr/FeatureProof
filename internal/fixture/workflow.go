package fixture

import (
	"context"

	"github.com/asierhr/featureProof/internal/contractcatalog"
	"github.com/asierhr/featureProof/internal/testidentity"
)

// BoundSetAuthority reconstructs and revalidates the sealed fixture set from trusted workflow composition on every delivery.
type BoundSetAuthority interface {
	Resolve(context.Context, string) (BoundSet, error)
}

// ReducedAuthority rebinds each delivery to immutable product configuration and the admitted retained contract.
type ReducedAuthority struct {
	Set          Set
	TargetID     string
	RepositoryID string
	Revision     string
	Contract     contractcatalog.ReducedContext
	Identities   *testidentity.Catalog
	Idempotency  IdempotencyPolicy
}

// Resolve recomputes the complete canonical seal for one workflow-owned run handle.
func (a ReducedAuthority) Resolve(ctx context.Context, handle string) (BoundSet, error) {
	if err := ctx.Err(); err != nil {
		return BoundSet{}, err
	}
	return BindReduced(a.Set, ReducedBindRequest{RunID: handle, TargetID: a.TargetID, RepositoryID: a.RepositoryID, Revision: a.Revision, Contract: a.Contract, Identities: a.Identities, Idempotency: a.Idempotency})
}

// WorkflowAdapter exposes the shared lifecycle to validationworkflow without allowing adapter-supplied fixture declarations.
type WorkflowAdapter struct {
	Lifecycle *Lifecycle
	Authority BoundSetAuthority
}

// Prepare resolves current authority and delegates deterministic fixture preparation for one workflow handle.
func (a WorkflowAdapter) Prepare(ctx context.Context, handle string) (FixtureSummary, error) {
	if a.Lifecycle == nil || a.Authority == nil {
		return FixtureSummary{}, &Error{Category: CategoryInvalid, Phase: "workflow_composition"}
	}
	bound, err := a.Authority.Resolve(ctx, handle)
	if err != nil {
		return FixtureSummary{}, errFor(ctx, CategoryDenied, "workflow_binding", "", err)
	}
	if bound.RunID != handle {
		return FixtureSummary{}, &Error{Category: CategoryInvalid, Phase: "workflow_binding"}
	}
	return a.Lifecycle.Prepare(ctx, bound)
}

// Cleanup re-resolves current sealed authority and invokes finite cleanup without inheriting a cancelled run context.
func (a WorkflowAdapter) Cleanup(ctx context.Context, handle string) (CleanupReport, error) {
	if a.Lifecycle == nil || a.Authority == nil {
		return CleanupReport{Status: CleanupFailed}, &Error{Category: CategoryInvalid, Phase: "workflow_composition"}
	}
	bound, err := a.Authority.Resolve(ctx, handle)
	if err != nil || bound.RunID != handle {
		return CleanupReport{Status: CleanupFailed}, &Error{Category: CategoryDenied, Phase: "workflow_binding", Cause: err}
	}
	return a.Lifecycle.Cleanup(ctx, bound)
}
