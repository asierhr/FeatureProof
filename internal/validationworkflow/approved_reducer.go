package validationworkflow

import (
	"context"

	"github.com/asierhr/featureProof/internal/contractcatalog"
	"github.com/asierhr/featureProof/internal/specification"
)

// ApprovedReviewReducer uses a current reviewed SourceSnapshot as the sole catalog input and applies a fixed sufficiency policy.
type ApprovedReviewReducer struct {
	Authority        ApprovedContractAuthority
	Fallback         ContractReducer
	Policy           contractcatalog.Policy
	CatalogLimits    contractcatalog.Limits
	ReductionLimits  contractcatalog.ReductionLimits
	ProjectionPolicy contractcatalog.ProjectionPolicy
	Selections       map[string][]contractcatalog.OperationKey
}

// Reduce preserves the configured non-review reducer for workflows without an ApprovedContractReference.
func (r ApprovedReviewReducer) Reduce(ctx context.Context, reference string, spec specification.Specification) (contractcatalog.ReducedContext, error) {
	if r.Fallback == nil {
		return contractcatalog.ReducedContext{}, &Error{Code: CodeInvalid, Operation: "contract_review_reducer"}
	}
	return r.Fallback.Reduce(ctx, reference, spec)
}

// ReduceApproved resolves fresh authority, rebuilds the neutral catalog and reduces only configured operations.
func (r ApprovedReviewReducer) ReduceApproved(ctx context.Context, command StartCommand, spec specification.Specification) (contractcatalog.ReducedContext, error) {
	if r.Authority == nil || command.ApprovedContract == nil {
		return contractcatalog.ReducedContext{}, &Error{Code: CodeInvalid, Operation: "contract_review_reducer"}
	}
	snapshot, err := r.Authority.ResolveApproved(ctx, command.Principal, *command.ApprovedContract)
	if err != nil {
		return contractcatalog.ReducedContext{}, err
	}
	registry, err := contractcatalog.NewRegistry(map[string]contractcatalog.ContractSource{snapshot.Source.Kind: &contractcatalog.StaticSource{Snapshot: snapshot}})
	if err != nil {
		return contractcatalog.ReducedContext{}, err
	}
	loaded, err := registry.Load(ctx, []contractcatalog.SourceRequest{contractcatalog.NewSourceRequest(contractcatalog.SourceDescriptor{Kind: snapshot.Source.Kind, ID: snapshot.Source.ID, Class: snapshot.Source.Class, Confidence: snapshot.Source.Confidence, Completeness: snapshot.Source.Completeness, ExpectedDigest: snapshot.Source.Digest, AdapterVersion: snapshot.Source.AdapterVersion, Limits: r.CatalogLimits, Projection: r.ProjectionPolicy})}, r.CatalogLimits)
	if err != nil {
		return contractcatalog.ReducedContext{}, err
	}
	catalog, err := contractcatalog.Build(ctx, loaded, r.CatalogLimits)
	if err != nil {
		return contractcatalog.ReducedContext{}, err
	}
	selections := make([]contractcatalog.ConstraintSelection, len(spec.Constraints))
	for i, constraint := range spec.Constraints {
		selections[i] = contractcatalog.ConstraintSelection{ConstraintID: constraint.ID, Operations: append([]contractcatalog.OperationKey(nil), r.Selections[constraint.ID]...)}
	}
	return contractcatalog.BuildContext(ctx, catalog, r.Policy, selections, r.ReductionLimits)
}
