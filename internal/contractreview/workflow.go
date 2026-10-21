package contractreview

import (
	"context"

	"github.com/asierhr/featureProof/internal/contractcatalog"
	"github.com/asierhr/featureProof/internal/validationworkflow"
)

// WorkflowAuthority adapts the review service to validationworkflow's pre-approval and pre-tool checkpoint port.
type WorkflowAuthority struct{ Service Service }

// ResolveApproved converts the workflow's neutral durable reference into the review service's authority-bearing reference.
func (a WorkflowAuthority) ResolveApproved(ctx context.Context, principal string, reference validationworkflow.ApprovedContractReference) (contractcatalog.SourceSnapshot, error) {
	return a.Service.ResolveApproved(ctx, ApprovedReference{PrincipalID: principal, ReviewID: reference.ReviewID, ExpectedRevision: reference.ExpectedRevision, ProjectionDigest: reference.ProjectionDigest})
}
