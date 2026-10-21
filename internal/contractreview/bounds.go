package contractreview

import (
	"context"

	"github.com/asierhr/featureProof/internal/contractcatalog"
)

// validateCatalogBounds applies aggregate source, diagnostic, provenance and schema budgets without truncation.
func validateCatalogBounds(ctx context.Context, catalogs []contractcatalog.OperationCatalog, limits Limits) error {
	sources, diagnostics, nodes, provenance := 0, 0, 0, 0
	for _, catalog := range catalogs {
		if err := cancellation(ctx, "bounds.catalog"); err != nil {
			return err
		}
		sources += len(catalog.Sources())
		diagnostics += len(catalog.Diagnostics())
		for _, operation := range catalog.Operations() {
			provenance += len(operation.Provenance)
			for _, claim := range operation.Claims {
				provenance += len(claim.Provenance)
			}
			for _, constraint := range operation.DeclarativeConstraints {
				provenance += len(constraint.Provenance)
			}
			for _, parameter := range operation.Parameters {
				if err := walkSchemaBounded(ctx, parameter.Schema, 1, limits, &nodes); err != nil {
					return err
				}
				for _, media := range parameter.Content {
					if err := walkSchemaBounded(ctx, media.Schema, 1, limits, &nodes); err != nil {
						return err
					}
				}
			}
			if operation.RequestBody != nil {
				for _, media := range operation.RequestBody.Content {
					if err := walkSchemaBounded(ctx, media.Schema, 1, limits, &nodes); err != nil {
						return err
					}
				}
			}
			for _, response := range operation.Responses {
				for _, header := range response.Headers {
					if err := walkSchemaBounded(ctx, header.Schema, 1, limits, &nodes); err != nil {
						return err
					}
				}
				for _, media := range response.Content {
					if err := walkSchemaBounded(ctx, media.Schema, 1, limits, &nodes); err != nil {
						return err
					}
				}
			}
		}
	}
	if sources > limits.MaxSources {
		return typedError(CategoryLimitExceeded, "sources", nil)
	}
	if diagnostics > limits.MaxDiagnostics {
		return typedError(CategoryLimitExceeded, "diagnostics", nil)
	}
	if provenance > limits.MaxProvenance {
		return typedError(CategoryLimitExceeded, "provenance", nil)
	}
	return nil
}

// walkSchemaBounded traverses one schema tree with cancellation and exact depth/node accounting.
func walkSchemaBounded(ctx context.Context, schema *contractcatalog.Schema, depth int, limits Limits, nodes *int) error {
	if schema == nil {
		return nil
	}
	if err := cancellation(ctx, "bounds.schema"); err != nil {
		return err
	}
	*nodes++
	if depth > limits.MaxSchemaDepth {
		return typedError(CategoryLimitExceeded, "schema.depth", nil)
	}
	if *nodes > limits.MaxSchemaNodes {
		return typedError(CategoryLimitExceeded, "schema.nodes", nil)
	}
	for _, child := range schema.Properties {
		if err := walkSchemaBounded(ctx, child, depth+1, limits, nodes); err != nil {
			return err
		}
	}
	if err := walkSchemaBounded(ctx, schema.Items, depth+1, limits, nodes); err != nil {
		return err
	}
	for _, child := range schema.AllOf {
		if err := walkSchemaBounded(ctx, child, depth+1, limits, nodes); err != nil {
			return err
		}
	}
	return nil
}

// validateProjectionBounds applies the same budgets to approved content before serialization or publication.
func validateProjectionBounds(ctx context.Context, projection ApprovedProjection, limits Limits) error {
	if len(projection.Sources) > limits.MaxSources {
		return typedError(CategoryLimitExceeded, "sources", nil)
	}
	if len(projection.Diagnostics) > limits.MaxDiagnostics {
		return typedError(CategoryLimitExceeded, "diagnostics", nil)
	}
	nodes, provenance := 0, 0
	for _, operation := range projection.Operations {
		if err := cancellation(ctx, "bounds.projection"); err != nil {
			return err
		}
		provenance += len(operation.Provenance)
		for _, claim := range operation.Claims {
			provenance += len(claim.Provenance)
		}
		for _, constraint := range operation.DeclarativeConstraints {
			provenance += len(constraint.Provenance)
		}
		for _, parameter := range operation.Parameters {
			if err := walkSchemaBounded(ctx, parameter.Schema, 1, limits, &nodes); err != nil {
				return err
			}
			for _, media := range parameter.Content {
				if err := walkSchemaBounded(ctx, media.Schema, 1, limits, &nodes); err != nil {
					return err
				}
			}
		}
		if operation.RequestBody != nil {
			for _, media := range operation.RequestBody.Content {
				if err := walkSchemaBounded(ctx, media.Schema, 1, limits, &nodes); err != nil {
					return err
				}
			}
		}
		for _, response := range operation.Responses {
			for _, header := range response.Headers {
				if err := walkSchemaBounded(ctx, header.Schema, 1, limits, &nodes); err != nil {
					return err
				}
			}
			for _, media := range response.Content {
				if err := walkSchemaBounded(ctx, media.Schema, 1, limits, &nodes); err != nil {
					return err
				}
			}
		}
	}
	if provenance > limits.MaxProvenance {
		return typedError(CategoryLimitExceeded, "provenance", nil)
	}
	return nil
}
