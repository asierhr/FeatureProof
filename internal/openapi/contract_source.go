package openapi

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"

	"github.com/asierhr/featureProof/internal/contractcatalog"
)

// ContractSourceAdapter projects an already validated OpenAPI contract into the neutral catalog boundary.
type ContractSourceAdapter struct{ AdapterVersion string }

// Load translates only the normalized Contract snapshot and never reparses bytes or resolves new references.
func (a ContractSourceAdapter) Load(ctx context.Context, request contractcatalog.SourceRequest) (contractcatalog.SourceSnapshot, error) {
	if err := ctx.Err(); err != nil {
		return contractcatalog.SourceSnapshot{}, compatibilityCancellation("openapi.adapter", err)
	}
	contract, ok := request.Input().(*Contract)
	if !ok || contract == nil || a.AdapterVersion == "" {
		return contractcatalog.SourceSnapshot{}, &contractcatalog.Error{Category: contractcatalog.CategoryInvalidSource, Location: "openapi.contract"}
	}
	if err := request.Projection().ValidateExcludedValues(append([]string(nil), contract.excluded...)); err != nil {
		return contractcatalog.SourceSnapshot{}, err
	}
	catalog := contract.Catalog()
	allSchemes := map[string]contractcatalog.SecurityScheme{}
	for _, scheme := range catalog.SecuritySchemes {
		var neutral contractcatalog.SecurityScheme
		if err := translateJSON(ctx, scheme, &neutral, "openapi.security"); err != nil {
			return contractcatalog.SourceSnapshot{}, err
		}
		allSchemes[scheme.Name] = neutral
	}
	operations := make([]contractcatalog.OperationDescriptor, len(catalog.Operations))
	for i, operation := range catalog.Operations {
		if err := ctx.Err(); err != nil {
			return contractcatalog.SourceSnapshot{}, compatibilityCancellation("openapi.adapter.operations", err)
		}
		if err := translateJSON(ctx, operation, &operations[i], "openapi.operation"); err != nil {
			return contractcatalog.SourceSnapshot{}, err
		}
		operations[i].LogicalID = operation.OperationID
		operations[i].Confidence = request.Confidence()
		operations[i].Completeness = request.Completeness()
		operations[i].Classes = []contractcatalog.SourceClass{request.Class()}
		operations[i].SecuritySchemes = map[string]contractcatalog.SecurityScheme{}
		for _, requirement := range operations[i].Security {
			for name := range requirement.Schemes {
				if scheme, ok := allSchemes[name]; ok {
					operations[i].SecuritySchemes[name] = scheme
				}
			}
		}
		operations[i].Provenance = []contractcatalog.Provenance{{SourceID: request.ID(), Class: request.Class(), SourceDigest: request.ExpectedDigest(), AdapterVersion: a.AdapterVersion, SafeLocation: fmt.Sprintf("catalog/operations/%d", i), Confidence: request.Confidence(), Completeness: request.Completeness()}}
		operations[i].Claims = contractcatalog.ClaimsForOperation(operations[i], operations[i].Provenance[0])
	}
	reachable := map[string]contractcatalog.SecurityScheme{}
	for _, operation := range operations {
		for name, scheme := range operation.SecuritySchemes {
			reachable[name] = scheme
		}
	}
	names := make([]string, 0, len(reachable))
	for name := range reachable {
		names = append(names, name)
	}
	sort.Strings(names)
	schemes := make([]contractcatalog.SecurityScheme, 0, len(names))
	for _, name := range names {
		schemes = append(schemes, reachable[name])
	}
	omitted := contract.omitted
	return contractcatalog.SourceSnapshot{Version: contractcatalog.VersionV1, Source: contractcatalog.SourceSummary{ID: request.ID(), Kind: request.Kind(), Class: request.Class(), Digest: request.ExpectedDigest(), AdapterVersion: a.AdapterVersion, Confidence: request.Confidence(), Completeness: request.Completeness()}, Operations: operations, SecuritySchemes: schemes, Omissions: contractcatalog.OmissionCounts{Examples: omitted.Examples, Extensions: omitted.Extensions, Documentation: omitted.Documentation, Unselected: omitted.Unselected, SizeLimit: omitted.SizeLimit}}, nil
}

// translateJSON copies structurally compatible normalized public values while checking cancellation at serialization boundaries.
func translateJSON(ctx context.Context, input, output any, location string) error {
	if err := ctx.Err(); err != nil {
		return compatibilityCancellation(location, err)
	}
	raw, err := json.Marshal(input)
	if err != nil {
		return &contractcatalog.Error{Category: contractcatalog.CategoryInternal, Location: location}
	}
	if err = ctx.Err(); err != nil {
		return compatibilityCancellation(location, err)
	}
	if err = json.Unmarshal(raw, output); err != nil {
		return &contractcatalog.Error{Category: contractcatalog.CategoryInternal, Location: location}
	}
	if err = ctx.Err(); err != nil {
		return compatibilityCancellation(location, err)
	}
	return nil
}

// compatibilityCancellation wraps context termination in the neutral catalog's safe closed error union.
func compatibilityCancellation(location string, cause error) error {
	return &contractcatalog.Error{Category: contractcatalog.CategoryCancelled, Location: location, Cause: cause}
}

// NeutralizeReducedContext is the cancelable explicit v1 compatibility adapter for already reduced OpenAPI fixtures.
func NeutralizeReducedContext(ctx context.Context, reduced ReducedContext, sourceID, sourceDigest, policyDigest string) (contractcatalog.ReducedContext, error) {
	if err := ctx.Err(); err != nil {
		return contractcatalog.ReducedContext{}, compatibilityCancellation("openapi.compatibility", err)
	}
	retained := reduced.RetainedOperations()
	schemes := reduced.SecuritySchemes()
	neutralSchemes := map[string]contractcatalog.SecurityScheme{}
	for _, scheme := range schemes {
		if err := ctx.Err(); err != nil {
			return contractcatalog.ReducedContext{}, compatibilityCancellation("openapi.compatibility.security", err)
		}
		var neutral contractcatalog.SecurityScheme
		if err := translateJSON(ctx, scheme, &neutral, "openapi.compatibility.security"); err != nil {
			return contractcatalog.ReducedContext{}, err
		}
		neutralSchemes[scheme.Name] = neutral
	}
	neutral := make([]contractcatalog.RetainedOperation, len(retained))
	for i, item := range retained {
		if err := ctx.Err(); err != nil {
			return contractcatalog.ReducedContext{}, compatibilityCancellation("openapi.compatibility.operations", err)
		}
		var operation contractcatalog.OperationDescriptor
		if err := translateJSON(ctx, item.Operation, &operation, "openapi.compatibility.operations"); err != nil {
			return contractcatalog.ReducedContext{}, err
		}
		operation.LogicalID = item.Operation.OperationID
		operation.SecuritySchemes = map[string]contractcatalog.SecurityScheme{}
		for _, requirement := range operation.Security {
			for name := range requirement.Schemes {
				if scheme, exists := neutralSchemes[name]; exists {
					operation.SecuritySchemes[name] = scheme
				}
			}
		}
		operation.Provenance = []contractcatalog.Provenance{{SourceID: sourceID, Class: contractcatalog.ClassAuthoritative, SourceDigest: sourceDigest, AdapterVersion: "openapi-compat-v1", SafeLocation: fmt.Sprintf("catalog/operations/%d", i), Confidence: contractcatalog.ConfidenceHigh, Completeness: contractcatalog.CompletenessComplete}}
		operation.Claims = contractcatalog.ClaimsForOperation(operation, operation.Provenance[0])
		operation.Confidence, operation.Completeness, operation.Classes = contractcatalog.ConfidenceHigh, contractcatalog.CompletenessComplete, []contractcatalog.SourceClass{contractcatalog.ClassAuthoritative}
		neutral[i] = contractcatalog.RetainedOperation{ConstraintID: item.ConstraintID, Operation: operation}
	}
	limits := contractcatalog.Limits{MaxSources: 1, MaxOperations: 4096, MaxClaims: 65536, MaxBytes: 4 << 20, MaxDepth: 64, MaxDiagnostics: 4096, MaxLocationBytes: 256}
	projection := contractcatalog.ProjectionPolicy{MaxStringBytes: 4096, AllowedURLSchemes: []string{"https"}, SensitiveNames: []string{}, SensitivePaths: []string{}, SensitiveHeaderNames: []string{"authorization", "cookie", "set-cookie", "x-api-key"}, ForbiddenValues: []string{}}
	snapshot := contractcatalog.SourceSnapshot{Version: contractcatalog.VersionV1, Source: contractcatalog.SourceSummary{ID: sourceID, Kind: "openapi-compat", Class: contractcatalog.ClassAuthoritative, Digest: sourceDigest, AdapterVersion: "openapi-compat-v1", Confidence: contractcatalog.ConfidenceHigh, Completeness: contractcatalog.CompletenessComplete}, Operations: make([]contractcatalog.OperationDescriptor, len(neutral))}
	for i := range neutral {
		snapshot.Operations[i] = neutral[i].Operation
	}
	for _, scheme := range neutralSchemes {
		snapshot.SecuritySchemes = append(snapshot.SecuritySchemes, scheme)
	}
	sort.Slice(snapshot.SecuritySchemes, func(i, j int) bool { return snapshot.SecuritySchemes[i].Name < snapshot.SecuritySchemes[j].Name })
	registry, err := contractcatalog.NewRegistry(map[string]contractcatalog.ContractSource{"openapi-compat": &contractcatalog.StaticSource{Snapshot: snapshot}})
	if err != nil {
		return contractcatalog.ReducedContext{}, err
	}
	loaded, err := registry.Load(ctx, []contractcatalog.SourceRequest{contractcatalog.NewSourceRequest(contractcatalog.SourceDescriptor{Kind: "openapi-compat", ID: sourceID, Class: contractcatalog.ClassAuthoritative, Confidence: contractcatalog.ConfidenceHigh, Completeness: contractcatalog.CompletenessComplete, ExpectedDigest: sourceDigest, AdapterVersion: "openapi-compat-v1", Limits: limits, Projection: projection})}, limits)
	if err != nil {
		return contractcatalog.ReducedContext{}, err
	}
	catalog, err := contractcatalog.Build(ctx, loaded, limits)
	if err != nil {
		return contractcatalog.ReducedContext{}, err
	}
	rules := map[contractcatalog.ClaimKind]contractcatalog.ClaimRule{}
	for _, kind := range []contractcatalog.ClaimKind{contractcatalog.ClaimOperationIdentity, contractcatalog.ClaimSchema, contractcatalog.ClaimSecurity, contractcatalog.ClaimResponse} {
		rules[kind] = contractcatalog.ClaimRule{AllowedClasses: []contractcatalog.SourceClass{contractcatalog.ClassAuthoritative}, MinimumConfidence: map[contractcatalog.SourceClass]contractcatalog.Confidence{contractcatalog.ClassAuthoritative: contractcatalog.ConfidenceHigh}, RequiredCompleteness: contractcatalog.CompletenessComplete, Required: true}
	}
	policy, err := contractcatalog.NewPolicy(contractcatalog.SufficiencyPolicy{ID: policyDigest, Version: contractcatalog.VersionV1, Claims: rules, ConflictTreatment: contractcatalog.ConflictDeny})
	if err != nil {
		return contractcatalog.ReducedContext{}, err
	}
	selections := make([]contractcatalog.ConstraintSelection, len(neutral))
	for i, item := range neutral {
		selections[i] = contractcatalog.ConstraintSelection{ConstraintID: item.ConstraintID, Operations: []contractcatalog.OperationKey{item.Operation.Key}}
	}
	return contractcatalog.BuildContext(ctx, catalog, policy, selections, contractcatalog.ReductionLimits{MaxBytes: 4 << 20, MaxOperations: 4096, MaxClaims: 65536, MaxDepth: 64, MaxDiagnostics: 4096})
}
