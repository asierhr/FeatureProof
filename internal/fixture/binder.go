package fixture

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"

	"github.com/asierhr/featureProof/internal/contractcatalog"
	"github.com/asierhr/featureProof/internal/specification"
	"github.com/asierhr/featureProof/internal/testidentity"
)

// IdempotencyPolicy verifies that an approved operation supplies retry/redelivery safety outside specification input.
type IdempotencyPolicy interface {
	Allows(contractcatalog.OperationDescriptor) bool
}

// ContractIdempotencyPolicy accepts only HTTP-defined semantics or capability declared by the approved operation contract.
type ContractIdempotencyPolicy struct{}

// Allows verifies retry safety without trusting the fixture declaration or caller-provided headers.
func (p ContractIdempotencyPolicy) Allows(operation contractcatalog.OperationDescriptor) bool {
	switch strings.ToUpper(operation.Key.Method) {
	case "GET", "HEAD", "PUT", "DELETE", "OPTIONS", "TRACE":
		return true
	}
	for _, parameter := range operation.Parameters {
		if strings.EqualFold(parameter.Name, "Idempotency-Key") && parameter.In == "header" && parameter.Schema != nil && parameter.Schema.Type == "string" {
			return true
		}
	}
	for _, constraint := range operation.DeclarativeConstraints {
		if constraint.Kind == "idempotency" && constraint.Expression == "guaranteed" {
			return true
		}
	}
	return false
}

// contractIdempotencyHeader returns the exact contract-declared key location without treating runtime allowlists as authority.
func contractIdempotencyHeader(operation contractcatalog.OperationDescriptor) string {
	for _, parameter := range operation.Parameters {
		if strings.EqualFold(parameter.Name, "Idempotency-Key") && parameter.In == "header" && parameter.Schema != nil && parameter.Schema.Type == "string" {
			return parameter.Name
		}
	}
	return ""
}

// BindRequest contains trusted composition authority for one immutable fixture set.
type BindRequest struct {
	RunID           string
	TargetID        string
	RepositoryID    string
	Revision        string
	Catalog         contractcatalog.OperationCatalog
	CatalogApproved bool
	Identities      *testidentity.Catalog
	Idempotency     IdempotencyPolicy
}

// ReducedBindRequest contains the exact retained authority already admitted for productive workflow composition.
type ReducedBindRequest struct {
	RunID        string
	TargetID     string
	RepositoryID string
	Revision     string
	Contract     contractcatalog.ReducedContext
	Identities   *testidentity.Catalog
	Idempotency  IdempotencyPolicy
}

// BindReduced binds fixtures only to operations retained by the same reduced contract used for admission.
func BindReduced(set Set, request ReducedBindRequest) (BoundSet, error) {
	if request.RunID == "" || request.TargetID == "" || request.RepositoryID == "" || request.Revision == "" || request.Contract.Validate() != nil || request.Identities == nil || request.Idempotency == nil {
		return BoundSet{}, &Error{Category: CategoryDenied, Phase: "binding"}
	}
	retained := request.Contract.RetainedOperations()
	lookup := map[contractcatalog.OperationKey]contractcatalog.OperationDescriptor{}
	for _, item := range retained {
		if item.CatalogDigest != request.Contract.CatalogDigest() {
			return BoundSet{}, &Error{Category: CategoryDenied, Phase: "binding_catalog"}
		}
		lookup[item.Operation.Key] = item.Operation
	}
	bound := BoundSet{RunID: request.RunID, TargetID: request.TargetID, RepositoryID: request.RepositoryID, Revision: request.Revision, CreateOrder: set.CreateOrder(), CleanupOrder: set.CleanupOrder()}
	for _, declaration := range set.Fixtures() {
		create, err := bindReducedOperation(declaration.ID, "create", declaration.Create, request, lookup)
		if err != nil {
			return BoundSet{}, err
		}
		cleanup, err := bindReducedOperation(declaration.ID, "cleanup", declaration.Cleanup, request, lookup)
		if err != nil {
			return BoundSet{}, err
		}
		bound.Fixtures = append(bound.Fixtures, BoundFixture{Declaration: declaration, Create: create, Cleanup: cleanup})
	}
	return SealBoundSet(bound)
}

// bindReducedOperation proves structural operation, explicit identity, and idempotency from retained authority.
func bindReducedOperation(fixtureID, phase string, operation specification.FixtureOperation, request ReducedBindRequest, lookup map[contractcatalog.OperationKey]contractcatalog.OperationDescriptor) (BoundOperation, error) {
	key := contractcatalog.OperationKey{Method: operation.Method, Path: operation.Path}
	descriptor, ok := lookup[key]
	if !ok || descriptor.Key != key || !request.Idempotency.Allows(descriptor) {
		return BoundOperation{}, &Error{Category: CategoryDenied, Phase: phase + "_contract", FixtureID: fixtureID}
	}
	identity, err := request.Identities.Bind(operation.IdentityID, testidentity.Operation{Capability: "http.request", TargetID: request.TargetID, Method: operation.Method, Path: operation.Path})
	if err != nil {
		return BoundOperation{}, &Error{Category: CategoryDenied, Phase: phase + "_identity", FixtureID: fixtureID}
	}
	material := struct{ Catalog, Identity, Method, Path, Target, Phase string }{request.Contract.CatalogDigest(), identity.Digest, operation.Method, operation.Path, request.TargetID, phase}
	raw, _ := json.Marshal(material)
	sum := sha256.Sum256(raw)
	return BoundOperation{Method: operation.Method, Path: operation.Path, Identity: identity, CatalogDigest: request.Contract.CatalogDigest(), BindingDigest: hex.EncodeToString(sum[:]), Idempotent: true, IdempotencyHeader: contractIdempotencyHeader(descriptor)}, nil
}

// Bind proves exact approved contract, explicit identity, and idempotency authority for every operation all-or-nothing.
func Bind(set Set, request BindRequest) (BoundSet, error) {
	if request.RunID == "" || request.TargetID == "" || request.RepositoryID == "" || request.Revision == "" || !request.CatalogApproved || request.Catalog.Digest() == "" || request.Identities == nil || request.Idempotency == nil || !authoritativeCatalog(request.Catalog) {
		return BoundSet{}, &Error{Category: CategoryDenied, Phase: "binding"}
	}
	bound := BoundSet{RunID: request.RunID, TargetID: request.TargetID, RepositoryID: request.RepositoryID, Revision: request.Revision, CreateOrder: set.CreateOrder(), CleanupOrder: set.CleanupOrder()}
	for _, declaration := range set.Fixtures() {
		create, err := bindOperation(declaration.ID, "create", declaration.Create, request)
		if err != nil {
			return BoundSet{}, err
		}
		cleanup, err := bindOperation(declaration.ID, "cleanup", declaration.Cleanup, request)
		if err != nil {
			return BoundSet{}, err
		}
		bound.Fixtures = append(bound.Fixtures, BoundFixture{Declaration: declaration, Create: create, Cleanup: cleanup})
	}
	return SealBoundSet(bound)
}

// authoritativeCatalog rejects derived-only catalogs and unresolved conflicts as execution authority.
func authoritativeCatalog(catalog contractcatalog.OperationCatalog) bool {
	if len(catalog.Conflicts()) != 0 {
		return false
	}
	sources := catalog.Sources()
	if len(sources) == 0 {
		return false
	}
	for _, source := range sources {
		if source.Class != contractcatalog.ClassAuthoritative {
			return false
		}
	}
	return true
}

// bindOperation performs structural exact lookup and explicit identity authorization without resolving credentials.
func bindOperation(fixtureID, phase string, operation specification.FixtureOperation, request BindRequest) (BoundOperation, error) {
	descriptor, ok := request.Catalog.Lookup(contractcatalog.OperationKey{Method: operation.Method, Path: operation.Path})
	if !ok || descriptor.Key.Method != operation.Method || descriptor.Key.Path != operation.Path || !request.Idempotency.Allows(descriptor) {
		return BoundOperation{}, &Error{Category: CategoryDenied, Phase: phase + "_contract", FixtureID: fixtureID}
	}
	identity, err := request.Identities.Bind(operation.IdentityID, testidentity.Operation{Capability: "http.request", TargetID: request.TargetID, Method: operation.Method, Path: operation.Path})
	if err != nil {
		return BoundOperation{}, &Error{Category: CategoryDenied, Phase: phase + "_identity", FixtureID: fixtureID}
	}
	material := struct{ Catalog, Identity, Method, Path, Target, Phase string }{request.Catalog.Digest(), identity.Digest, operation.Method, operation.Path, request.TargetID, phase}
	raw, _ := json.Marshal(material)
	sum := sha256.Sum256(raw)
	return BoundOperation{Method: operation.Method, Path: operation.Path, Identity: identity, CatalogDigest: request.Catalog.Digest(), BindingDigest: hex.EncodeToString(sum[:]), Idempotent: true, IdempotencyHeader: contractIdempotencyHeader(descriptor)}, nil
}

// cloneBoundSet isolates executable authority and nested declarations from caller mutation.
func cloneBoundSet(bound BoundSet) BoundSet {
	out := bound
	out.CreateOrder = append([]string(nil), bound.CreateOrder...)
	out.CleanupOrder = append([]string(nil), bound.CleanupOrder...)
	out.Fixtures = make([]BoundFixture, len(bound.Fixtures))
	for i, item := range bound.Fixtures {
		out.Fixtures[i] = item
		out.Fixtures[i].Declaration = cloneFixtures([]specification.Fixture{item.Declaration})[0]
	}
	return out
}
