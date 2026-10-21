package contractreview

import (
	"context"

	"github.com/asierhr/featureProof/internal/contractcatalog"
)

// ApprovedSource adapts only a freshly revalidated human approval into the neutral catalog registry.
type ApprovedSource struct{ Service *Service }

// Load resolves the sealed approved reference and verifies its source identity against registry authority.
func (s ApprovedSource) Load(ctx context.Context, request contractcatalog.SourceRequest) (contractcatalog.SourceSnapshot, error) {
	reference, ok := request.Input().(ApprovedReference)
	if !ok || s.Service == nil {
		return contractcatalog.SourceSnapshot{}, typedError(CategoryInvalidInput, "source.input", nil)
	}
	snapshot, err := s.Service.ResolveApproved(ctx, reference)
	if err != nil {
		return contractcatalog.SourceSnapshot{}, err
	}
	if snapshot.Source.ID != request.ID() || snapshot.Source.Digest != request.ExpectedDigest() || snapshot.Source.Kind != request.Kind() || snapshot.Source.AdapterVersion != request.AdapterVersion() {
		return contractcatalog.SourceSnapshot{}, typedError(CategoryInvalidated, "source.binding", nil)
	}
	return snapshot, nil
}

// ProposalSource exposes an unapproved derived catalog only to policies that explicitly accept proposal data.
type ProposalSource struct {
	Snapshot contractcatalog.SourceSnapshot
}

// Load returns an isolated proposal snapshot without adding authoritative review provenance.
func (s ProposalSource) Load(ctx context.Context, _ contractcatalog.SourceRequest) (contractcatalog.SourceSnapshot, error) {
	if err := cancellation(ctx, "proposal"); err != nil {
		return contractcatalog.SourceSnapshot{}, err
	}
	return deepClone(s.Snapshot), nil
}
