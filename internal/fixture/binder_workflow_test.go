package fixture

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"testing"

	"github.com/asierhr/featureProof/internal/contractcatalog"
	"github.com/asierhr/featureProof/internal/specification"
	"github.com/asierhr/featureProof/internal/testidentity"
)

// allowIdempotency is trusted test policy that marks complete fixture operations retry-safe.
type allowIdempotency bool

// Allows returns the configured contractual idempotency decision.
func (a allowIdempotency) Allows(contractcatalog.OperationDescriptor) bool { return bool(a) }

// sourceDigest creates a valid stable source digest for the controlled catalog adapter.
func sourceDigest(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}

// approvedCatalog builds a complete authoritative catalog through the public registry validation path.
func approvedCatalog(t *testing.T, class contractcatalog.SourceClass) contractcatalog.OperationCatalog {
	t.Helper()
	digest := sourceDigest("fixture-source")
	provenance := contractcatalog.Provenance{SourceID: "fixture-source", RepositoryID: "repo", Revision: "rev", Class: class, SourceDigest: digest, AdapterVersion: "fixture-test-v1", SafeLocation: "operations", Confidence: contractcatalog.ConfidenceHigh, Completeness: contractcatalog.CompletenessComplete}
	operations := []contractcatalog.OperationDescriptor{}
	for _, key := range []contractcatalog.OperationKey{{Method: "POST", Path: "/accounts"}, {Method: "DELETE", Path: "/accounts/{id}"}, {Method: "POST", Path: "/profiles"}, {Method: "DELETE", Path: "/profiles/{id}"}} {
		operation := contractcatalog.OperationDescriptor{Key: key, Responses: map[string]contractcatalog.Response{"200": {}}, Provenance: []contractcatalog.Provenance{provenance}, Confidence: contractcatalog.ConfidenceHigh, Completeness: contractcatalog.CompletenessComplete, Classes: []contractcatalog.SourceClass{class}}
		operation.Claims = contractcatalog.ClaimsForOperation(operation, provenance)
		operations = append(operations, operation)
	}
	snapshot := contractcatalog.SourceSnapshot{Version: contractcatalog.VersionV1, Source: contractcatalog.SourceSummary{ID: "fixture-source", RepositoryID: "repo", Revision: "rev", Kind: "fixture-test", Class: class, Digest: digest, AdapterVersion: "fixture-test-v1", Confidence: contractcatalog.ConfidenceHigh, Completeness: contractcatalog.CompletenessComplete}, Operations: operations}
	limits := contractcatalog.Limits{MaxSources: 2, MaxOperations: 16, MaxClaims: 128, MaxBytes: 1 << 20, MaxDepth: 16, MaxDiagnostics: 16, MaxLocationBytes: 128}
	projection := contractcatalog.ProjectionPolicy{MaxStringBytes: 256, AllowedURLSchemes: []string{"https"}, SensitiveNames: []string{}, SensitivePaths: []string{}, SensitiveHeaderNames: []string{"authorization"}, ForbiddenValues: []string{}}
	registry, err := contractcatalog.NewRegistry(map[string]contractcatalog.ContractSource{"fixture-test": &contractcatalog.StaticSource{Snapshot: snapshot}})
	if err != nil {
		t.Fatal(err)
	}
	loaded, err := registry.Load(context.Background(), []contractcatalog.SourceRequest{contractcatalog.NewSourceRequest(contractcatalog.SourceDescriptor{Kind: "fixture-test", ID: "fixture-source", Class: class, Confidence: contractcatalog.ConfidenceHigh, Completeness: contractcatalog.CompletenessComplete, ExpectedDigest: digest, AdapterVersion: "fixture-test-v1", Input: struct{}{}, Limits: limits, Projection: projection})}, limits)
	if err != nil {
		t.Fatal(err)
	}
	catalog, err := contractcatalog.Build(context.Background(), loaded, limits)
	if err != nil {
		t.Fatal(err)
	}
	return catalog
}

// identityCatalog builds explicit create and cleanup identities with no credential resolution.
func identityCatalog(t *testing.T) *testidentity.Catalog {
	t.Helper()
	identities := []specification.TestIdentity{{ID: "writer", TargetID: "deployed", CredentialRef: "writer_ref", Mechanism: specification.AuthenticationMechanism{Type: "bearer"}, Operations: []specification.AuthorizedOperation{{Capability: "http.request", Method: "POST", Path: "/accounts"}, {Capability: "http.request", Method: "POST", Path: "/profiles"}}}, {ID: "cleaner", TargetID: "deployed", CredentialRef: "cleaner_ref", Mechanism: specification.AuthenticationMechanism{Type: "bearer"}, Operations: []specification.AuthorizedOperation{{Capability: "http.request", Method: "DELETE", Path: "/accounts/{id}"}, {Capability: "http.request", Method: "DELETE", Path: "/profiles/{id}"}}}}
	catalog, err := testidentity.NewCatalog(identities)
	if err != nil {
		t.Fatal(err)
	}
	return catalog
}

// reducedFixtureContract builds retained authority for every fixture operation through the public reducer.
func reducedFixtureContract(t *testing.T) contractcatalog.ReducedContext {
	t.Helper()
	catalog := approvedCatalog(t, contractcatalog.ClassAuthoritative)
	rules := map[contractcatalog.ClaimKind]contractcatalog.ClaimRule{}
	for _, kind := range []contractcatalog.ClaimKind{contractcatalog.ClaimOperationIdentity, contractcatalog.ClaimSchema, contractcatalog.ClaimSecurity, contractcatalog.ClaimResponse} {
		rules[kind] = contractcatalog.ClaimRule{AllowedClasses: []contractcatalog.SourceClass{contractcatalog.ClassAuthoritative}, MinimumConfidence: map[contractcatalog.SourceClass]contractcatalog.Confidence{contractcatalog.ClassAuthoritative: contractcatalog.ConfidenceHigh}, RequiredCompleteness: contractcatalog.CompletenessComplete, Required: true}
	}
	policy, err := contractcatalog.NewPolicy(contractcatalog.SufficiencyPolicy{ID: "fixture", Version: contractcatalog.VersionV1, Claims: rules, ConflictTreatment: contractcatalog.ConflictDeny})
	if err != nil {
		t.Fatal(err)
	}
	keys := []contractcatalog.OperationKey{}
	for _, operation := range catalog.Operations() {
		keys = append(keys, operation.Key)
	}
	reduced, err := contractcatalog.BuildContext(context.Background(), catalog, policy, []contractcatalog.ConstraintSelection{{ConstraintID: "fixture", Operations: keys}}, contractcatalog.ReductionLimits{MaxBytes: 1 << 20, MaxOperations: 16, MaxClaims: 1024, MaxDepth: 32, MaxDiagnostics: 64})
	if err != nil {
		t.Fatal(err)
	}
	return reduced
}

// TestFixtureOperationsRequireExactApprovedContractBindings proves all operations bind to approved catalog and explicit identities.
func TestFixtureOperationsRequireExactApprovedContractBindings(t *testing.T) {
	set, err := ValidateSet(fixtureDeclarations())
	if err != nil {
		t.Fatal(err)
	}
	request := BindRequest{RunID: "run", TargetID: "deployed", RepositoryID: "repo", Revision: "rev", Catalog: approvedCatalog(t, contractcatalog.ClassAuthoritative), CatalogApproved: true, Identities: identityCatalog(t), Idempotency: allowIdempotency(true)}
	bound, err := Bind(set, request)
	if err != nil || len(bound.Fixtures) != 2 || len(bound.Digest) != 64 || bound.Fixtures[0].Create.Identity.IdentityID != "writer" || bound.Fixtures[0].Cleanup.Identity.IdentityID != "cleaner" {
		t.Fatalf("bound=%#v err=%v", bound, err)
	}
	request.CatalogApproved = false
	if _, err := Bind(set, request); !errors.Is(err, &Error{Category: CategoryDenied}) {
		t.Fatalf("unapproved accepted: %v", err)
	}
	request.CatalogApproved = true
	request.Idempotency = allowIdempotency(false)
	if _, err := Bind(set, request); !errors.Is(err, &Error{Category: CategoryDenied}) {
		t.Fatalf("non-idempotent accepted: %v", err)
	}
}

// TestDerivedCatalogAndIdentityDriftCannotAuthorizeFixtures covers source class and explicit operation denial.
func TestDerivedCatalogAndIdentityDriftCannotAuthorizeFixtures(t *testing.T) {
	set, _ := ValidateSet(fixtureDeclarations())
	request := BindRequest{RunID: "run", TargetID: "deployed", RepositoryID: "repo", Revision: "rev", Catalog: approvedCatalog(t, contractcatalog.ClassDerived), CatalogApproved: true, Identities: identityCatalog(t), Idempotency: allowIdempotency(true)}
	if _, err := Bind(set, request); !errors.Is(err, &Error{Category: CategoryDenied}) {
		t.Fatalf("derived catalog accepted: %v", err)
	}
	request.Catalog = approvedCatalog(t, contractcatalog.ClassAuthoritative)
	request.TargetID = "other"
	if _, err := Bind(set, request); !errors.Is(err, &Error{Category: CategoryDenied}) {
		t.Fatalf("identity drift accepted: %v", err)
	}
}

// TestContractIdempotencyPolicyRequiresSemanticOrTrustedKeyGuarantee covers the production retry-safety decision.
func TestContractIdempotencyPolicyRequiresSemanticOrTrustedKeyGuarantee(t *testing.T) {
	policy := ContractIdempotencyPolicy{}
	keyed := contractcatalog.OperationDescriptor{Key: contractcatalog.OperationKey{Method: "POST", Path: "/keyed"}, Parameters: []contractcatalog.Parameter{{Name: "Idempotency-Key", In: "header", Schema: &contractcatalog.Schema{Type: "string"}}}}
	if !policy.Allows(contractcatalog.OperationDescriptor{Key: contractcatalog.OperationKey{Method: "DELETE", Path: "/items/{id}"}}) || !policy.Allows(keyed) || policy.Allows(contractcatalog.OperationDescriptor{Key: contractcatalog.OperationKey{Method: "POST", Path: "/unsafe"}}) {
		t.Fatal("idempotency policy widened or rejected verified guarantee")
	}
	declared := contractcatalog.OperationDescriptor{Key: contractcatalog.OperationKey{Method: "POST", Path: "/declared"}, DeclarativeConstraints: []contractcatalog.DeclarativeConstraint{{Kind: "idempotency", Expression: "guaranteed"}}}
	if !policy.Allows(declared) {
		t.Fatal("contract guarantee rejected")
	}
}

// TestReducedAuthorityRebindsEveryDelivery proves productive workflow handles receive fresh canonical seals from retained authority.
func TestReducedAuthorityRebindsEveryDelivery(t *testing.T) {
	set, _ := ValidateSet(fixtureDeclarations())
	authority := ReducedAuthority{Set: set, TargetID: "deployed", RepositoryID: "repo", Revision: "rev", Contract: reducedFixtureContract(t), Identities: identityCatalog(t), Idempotency: allowIdempotency(true)}
	first, err := authority.Resolve(context.Background(), "run-a")
	if err != nil || ValidateBoundSet(first) != nil || first.RunID != "run-a" {
		t.Fatal(first, err)
	}
	second, err := authority.Resolve(context.Background(), "run-b")
	if err != nil || first.Digest == second.Digest || second.RunID != "run-b" {
		t.Fatal(second, err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := authority.Resolve(ctx, "run-c"); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	authority.Idempotency = allowIdempotency(false)
	if _, err := authority.Resolve(context.Background(), "run-c"); !errors.Is(err, &Error{Category: CategoryDenied}) {
		t.Fatal(err)
	}
}

// staticAuthority returns one immutable bound set and can simulate reconstruction failure.
type staticAuthority struct {
	bound BoundSet
	err   error
}

// Resolve supplies trusted workflow composition without accepting declarations from the adapter call.
func (a staticAuthority) Resolve(context.Context, string) (BoundSet, error) {
	return cloneBoundSet(a.bound), a.err
}

// TestWorkflowAdapterDelegatesSharedLifecycleAndRejectsHandleDrift verifies productive surfaces share one authority path.
func TestWorkflowAdapterDelegatesSharedLifecycleAndRejectsHandleDrift(t *testing.T) {
	executor := &scriptedExecutor{results: []HTTPResult{{Status: 201, Body: map[string]any{"body": map[string]any{"id": "a"}}, CrossedNetwork: true}, {Status: 201, Body: map[string]any{"body": map[string]any{"id": "p"}}, CrossedNetwork: true}, {Status: 204}, {Status: 204}}}
	lifecycle, _, _, _ := lifecycleForTest(t, executor, fixedScopes{calls: 2})
	bound := boundFixtureSet(t)
	adapter := WorkflowAdapter{Lifecycle: lifecycle, Authority: staticAuthority{bound: bound}}
	summary, err := adapter.Prepare(context.Background(), bound.RunID)
	if err != nil || summary.PreparationStatus != PreparationPrepared {
		t.Fatal(summary, err)
	}
	report, err := adapter.Cleanup(context.Background(), bound.RunID)
	if err != nil || report.Status != CleanupSucceeded {
		t.Fatal(report, err)
	}
	if _, err := adapter.Prepare(context.Background(), "wrong"); !errors.Is(err, &Error{Category: CategoryInvalid}) {
		t.Fatalf("handle drift=%v", err)
	}
	empty := WorkflowAdapter{}
	if _, err := empty.Prepare(context.Background(), "run"); err == nil {
		t.Fatal("empty adapter accepted")
	}
}
