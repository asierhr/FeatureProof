package testidentity

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/asierhr/featureProof/internal/specification"
)

// catalogFixture builds two independently authorized identities in deliberately non-canonical order.
func catalogFixture(t *testing.T) *Catalog {
	t.Helper()
	identities := []specification.TestIdentity{
		{ID: "z-admin", TargetID: "deployed", CredentialRef: "PRIVATE_ADMIN_REF", Mechanism: specification.AuthenticationMechanism{Type: "header", Name: "X-Key"}, Operations: []specification.AuthorizedOperation{{Capability: "http.request", Method: "POST", Path: "/api/accounts/{id}"}}},
		{ID: "a-customer", TargetID: "deployed", CredentialRef: "PRIVATE_CUSTOMER_REF", Mechanism: specification.AuthenticationMechanism{Type: "bearer"}, Operations: []specification.AuthorizedOperation{{Capability: "http.request", Method: "GET", Path: "/api/accounts/{id}"}}},
	}
	catalog, err := NewCatalog(identities)
	if err != nil {
		t.Fatal(err)
	}
	identities[0].CredentialRef = "mutated"
	identities[0].Operations[0].Path = "/mutated"
	return catalog
}

// TestAuthenticatedOperationRequiresExactDeclaredIdentityBinding proves explicit selection and exact operation authorization.
func TestAuthenticatedOperationRequiresExactDeclaredIdentityBinding(t *testing.T) {
	catalog := catalogFixture(t)
	operation := Operation{Capability: "http.request", TargetID: "deployed", Method: "GET", Path: "/api/accounts/123"}
	binding, err := catalog.Bind("a-customer", operation)
	if err != nil {
		t.Fatal(err)
	}
	if binding.IdentityID != "a-customer" || binding.Operation.Path != "/api/accounts/{id}" || len(binding.Digest) != 64 {
		t.Fatalf("bad binding: %#v", binding)
	}
	for _, id := range []string{"", "missing", "z-admin"} {
		_, err := catalog.Bind(id, operation)
		if err == nil {
			t.Fatalf("identity %q was implicitly accepted", id)
		}
	}
}

// TestIdentityBindingRejectsTargetMethodPathAndTemplateConfusion covers prefix, traversal, escapes, and route drift.
func TestIdentityBindingRejectsTargetMethodPathAndTemplateConfusion(t *testing.T) {
	catalog := catalogFixture(t)
	base := Operation{Capability: "http.request", TargetID: "deployed", Method: "GET", Path: "/api/accounts/123"}
	bad := []Operation{
		{Capability: "http.request", TargetID: "other", Method: "GET", Path: base.Path},
		{Capability: "http.request", TargetID: "deployed", Method: "POST", Path: base.Path},
		{Capability: "http.request", TargetID: "deployed", Method: "GET", Path: "/api/accountsx/123"},
		{Capability: "http.request", TargetID: "deployed", Method: "GET", Path: "/api/accounts/../admin"},
		{Capability: "http.request", TargetID: "deployed", Method: "GET", Path: "/api/accounts/%2e%2e"},
	}
	for _, operation := range bad {
		if _, err := catalog.Bind("a-customer", operation); !errors.Is(err, &Error{Category: CategoryIdentityNotAllowed}) {
			t.Fatalf("accepted %#v: %v", operation, err)
		}
	}
}

// TestModelReceivesOnlyApplicableSafeIdentityDescriptors proves canonical filtering, deep copies, and secret omission.
func TestModelReceivesOnlyApplicableSafeIdentityDescriptors(t *testing.T) {
	catalog := catalogFixture(t)
	descriptors := catalog.Descriptors(Operation{Capability: "http.request", TargetID: "deployed", Method: "GET", Path: "/api/accounts/7"})
	if len(descriptors) != 1 || descriptors[0].ID != "a-customer" {
		t.Fatalf("unexpected descriptors: %#v", descriptors)
	}
	raw, err := json.Marshal(descriptors)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "PRIVATE_") || strings.Contains(string(raw), "credential") || strings.Contains(string(raw), "X-Key") {
		t.Fatalf("private metadata leaked: %s", raw)
	}
	descriptors[0].Operations[0].Path = "/changed"
	again := catalog.Descriptors(Operation{Capability: "http.request", TargetID: "deployed", Method: "GET", Path: "/api/accounts/7"})
	if again[0].Operations[0].Path != "/api/accounts/{id}" {
		t.Fatal("descriptor mutated catalog")
	}
}

// TestCatalogDigestExcludesCredentialReferences proves secret rotation does not alter the authorization snapshot digest.
func TestCatalogDigestExcludesCredentialReferences(t *testing.T) {
	first := catalogFixture(t)
	identities := []specification.TestIdentity{{ID: "a-customer", TargetID: "deployed", CredentialRef: "ROTATED", Mechanism: specification.AuthenticationMechanism{Type: "bearer"}, Operations: []specification.AuthorizedOperation{{Capability: "http.request", Method: "GET", Path: "/api/accounts/{id}"}}}, {ID: "z-admin", TargetID: "deployed", CredentialRef: "OTHER", Mechanism: specification.AuthenticationMechanism{Type: "header", Name: "Different-Private-Name"}, Operations: []specification.AuthorizedOperation{{Capability: "http.request", Method: "POST", Path: "/api/accounts/{id}"}}}}
	second, err := NewCatalog(identities)
	if err != nil {
		t.Fatal(err)
	}
	if first.Digest() != second.Digest() {
		t.Fatalf("private composition metadata affected digest")
	}
}
