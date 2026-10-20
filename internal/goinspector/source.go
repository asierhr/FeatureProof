package goinspector

import (
	"context"
	"errors"
	"reflect"

	"github.com/asierhr/featureProof/internal/contractcatalog"
)

// DerivedSource adapts the hermetic inspector to the source-neutral contract registry.
type DerivedSource struct{ Inspector Inspector }

// Load validates all trusted bindings before parsing and returns no partial snapshot on failure.
func (d DerivedSource) Load(ctx context.Context, request contractcatalog.SourceRequest) (contractcatalog.SourceSnapshot, error) {
	input, ok := request.Input().(Input)
	if !ok || request.Kind() != SourceKind || request.Class() != contractcatalog.ClassDerived || request.AdapterVersion() != AdapterVersion || request.ExpectedDigest() != input.Digest() || request.Confidence() != input.config.Confidence || request.Completeness() != input.config.Completeness || !reflect.DeepEqual(request.Projection(), input.config.Projection) {
		return contractcatalog.SourceSnapshot{}, &contractcatalog.Error{Category: contractcatalog.CategoryInvalidSource, Location: "go_inspector"}
	}
	result, err := d.Inspector.Inspect(ctx, input, request.ID())
	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return contractcatalog.SourceSnapshot{}, &contractcatalog.Error{Category: contractcatalog.CategoryCancelled, Location: "go_inspector", Cause: err}
		}
		var inspection *Error
		if errors.As(err, &inspection) {
			category := contractcatalog.CategoryInvalidSource
			switch inspection.Category {
			case CategoryInsufficientContext:
				category = contractcatalog.CategoryInsufficient
			case CategoryLimitExceeded:
				category = contractcatalog.CategoryLimitExceeded
			case CategoryCancelled:
				category = contractcatalog.CategoryCancelled
			}
			return contractcatalog.SourceSnapshot{}, &contractcatalog.Error{Category: category, Location: "go_inspector", Cause: inspection.Cause}
		}
		return contractcatalog.SourceSnapshot{}, &contractcatalog.Error{Category: contractcatalog.CategoryInternal, Location: "go_inspector"}
	}
	return result.Snapshot, nil
}

// SourceRequest constructs the exact trusted registry descriptor for a previously sealed input.
func SourceRequest(sourceID string, input Input, limits contractcatalog.Limits, projection contractcatalog.ProjectionPolicy) contractcatalog.SourceRequest {
	return contractcatalog.NewSourceRequest(contractcatalog.SourceDescriptor{Kind: SourceKind, ID: sourceID, Class: contractcatalog.ClassDerived, Confidence: input.config.Confidence, Completeness: input.config.Completeness, ExpectedDigest: input.Digest(), AdapterVersion: AdapterVersion, Input: input, Limits: limits, Projection: projection})
}
