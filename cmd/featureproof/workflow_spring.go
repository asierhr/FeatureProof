package main

import (
	"context"
	"errors"
	"strings"

	"github.com/asierhr/featureProof/internal/contractcatalog"
	"github.com/asierhr/featureProof/internal/goinspector"
	"github.com/asierhr/featureProof/internal/specification"
	"github.com/asierhr/featureProof/internal/springinspector"
	"github.com/asierhr/featureProof/internal/validationworkflow"
)

// workflowSpringRepository reuses the hardened descriptor-based repository reader while exposing only Spring's passive byte capability.
type workflowSpringRepository struct{ delegate goinspector.LocalRepositoryFS }

// ReadSnapshot translates finite budgets and roots without giving the Java parser filesystem access.
func (r workflowSpringRepository) ReadSnapshot(ctx context.Context, request springinspector.SnapshotRequest) (map[string][]byte, error) {
	roots := make([]string, len(request.SourceSets))
	for index, sourceSet := range request.SourceSets {
		roots[index] = sourceSet.Root
	}
	limits := goinspector.Limits{MaxFiles: request.Limits.MaxFiles, MaxBytes: request.Limits.MaxBytes, MaxPackages: request.Limits.MaxUnits, MaxASTNodes: request.Limits.MaxTokens, MaxDepth: request.Limits.MaxDepth, MaxTypes: request.Limits.MaxTypes, MaxOperations: request.Limits.MaxOperations, MaxDiagnostics: request.Limits.MaxDiagnostics, MaxDuration: request.Limits.MaxDuration}
	return r.delegate.ReadSnapshot(ctx, goinspector.SnapshotRequest{Root: request.Root, Roots: roots, Limits: limits})
}

// workflowSpringContractReducer freezes one authorized Java checkout and reduces policy-eligible Spring operations.
type workflowSpringContractReducer struct {
	root, authorizedRef, repositoryID, revision string
	config                                      workflowSpringInspectorConfig
	selections                                  map[string][]workflowOperationSelection
	projection                                  contractcatalog.ProjectionPolicy
}

// Reduce binds revision, source sets, Java release, catalog and policy before model inference can start.
func (r workflowSpringContractReducer) Reduce(ctx context.Context, ref string, spec specification.Specification) (contractcatalog.ReducedContext, error) {
	if ref != r.authorizedRef {
		return contractcatalog.ReducedContext{}, errors.New("unauthorized contract")
	}
	effective := r.config
	if authority, ok := validationworkflow.ContractAuthority(ctx); ok {
		if authority.RepositoryID != r.repositoryID || authority.CommitOID != r.revision {
			return contractcatalog.ReducedContext{}, errors.New("contract authority drift")
		}
		if len(authority.Roots) > 0 {
			allowed := map[string]springinspector.SourceSet{}
			for _, set := range r.config.SourceSets {
				allowed[set.Root] = set
			}
			effective.SourceSets = nil
			for _, root := range authority.Roots {
				set, exists := allowed[root]
				if !exists {
					return contractcatalog.ReducedContext{}, errors.New("contract authority drift")
				}
				effective.SourceSets = append(effective.SourceSets, set)
			}
		}
		effective.ChangedPaths = append([]string{}, authority.ChangedPaths...)
	}
	input, err := springinspector.FreezeConfiguredRepository(ctx, workflowSpringRepository{}, r.root, springinspector.Configuration{RepositoryID: r.repositoryID, Revision: r.revision, RootIdentity: r.config.RootIdentity, FrameworkIdentities: append([]string(nil), r.config.FrameworkIdentities...), SourceSets: append([]springinspector.SourceSet{}, effective.SourceSets...), ChangedPaths: append([]string{}, effective.ChangedPaths...), JavaRelease: r.config.JavaRelease, Generated: r.config.Generated, Limits: r.config.Limits, Confidence: contractcatalog.ConfidenceMedium, Completeness: contractcatalog.CompletenessPartial, Projection: r.projection})
	if err != nil {
		return contractcatalog.ReducedContext{}, err
	}
	limits := contractcatalog.Limits{MaxSources: 1, MaxOperations: r.config.Limits.MaxOperations, MaxClaims: r.config.Limits.MaxOperations * 256, MaxBytes: r.config.Limits.MaxBytes, MaxDepth: r.config.Limits.MaxDepth + 32, MaxDiagnostics: r.config.Limits.MaxDiagnostics, MaxLocationBytes: 1024}
	registry, err := contractcatalog.NewRegistry(map[string]contractcatalog.ContractSource{springinspector.SourceKind: springinspector.DerivedSource{}})
	if err != nil {
		return contractcatalog.ReducedContext{}, err
	}
	loaded, err := registry.Load(ctx, []contractcatalog.SourceRequest{springinspector.SourceRequest("configured-spring", input, limits, r.projection)}, limits)
	if err != nil {
		return contractcatalog.ReducedContext{}, err
	}
	catalog, err := contractcatalog.Build(ctx, loaded, limits)
	if err != nil {
		return contractcatalog.ReducedContext{}, err
	}
	rules := map[contractcatalog.ClaimKind]contractcatalog.ClaimRule{}
	for _, kind := range []contractcatalog.ClaimKind{contractcatalog.ClaimOperationIdentity, contractcatalog.ClaimSchema, contractcatalog.ClaimSecurity, contractcatalog.ClaimResponse} {
		rules[kind] = contractcatalog.ClaimRule{AllowedClasses: []contractcatalog.SourceClass{contractcatalog.ClassDerived}, MinimumConfidence: map[contractcatalog.SourceClass]contractcatalog.Confidence{contractcatalog.ClassDerived: contractcatalog.ConfidenceMedium}, RequiredCompleteness: contractcatalog.CompletenessPartial, Required: true}
	}
	policy, err := contractcatalog.NewPolicy(contractcatalog.SufficiencyPolicy{ID: "workflow-spring-static-v1", Version: contractcatalog.VersionV1, Claims: rules, ConflictTreatment: contractcatalog.ConflictDeny})
	if err != nil {
		return contractcatalog.ReducedContext{}, err
	}
	selections := make([]contractcatalog.ConstraintSelection, len(spec.Constraints))
	for index, constraint := range spec.Constraints {
		selections[index].ConstraintID = constraint.ID
		for _, operation := range r.selections[constraint.ID] {
			selections[index].Operations = append(selections[index].Operations, contractcatalog.OperationKey{Method: strings.ToUpper(operation.Method), Path: operation.Path})
		}
	}
	return contractcatalog.BuildContext(ctx, catalog, policy, selections, contractcatalog.ReductionLimits{MaxBytes: 1 << 20, MaxOperations: limits.MaxOperations, MaxClaims: limits.MaxClaims, MaxDepth: limits.MaxDepth, MaxDiagnostics: limits.MaxDiagnostics})
}
