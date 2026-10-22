package specification

import (
	"errors"
	"testing"
)

// validIdentitySpecification returns the smallest v2 domain fixture with one non-secret identity declaration.
func validIdentitySpecification() Specification {
	return Specification{Version: 2, Feature: Feature{ID: "feature", Description: "authenticated behavior"}, Target: Target{ID: "deployed", BaseURL: "https://example.test"}, Constraints: []Constraint{{ID: "c1", Description: "works", TargetID: "deployed"}}, Limits: Limits{MaxSteps: 2, MaxToolCalls: 2}, Identities: []TestIdentity{{ID: "customer", TargetID: "deployed", CredentialRef: "cred_customer", Mechanism: AuthenticationMechanism{Type: "bearer"}, Operations: []AuthorizedOperation{{Capability: "http.request", Method: "GET", Path: "/api/accounts/{account_id}"}}}}}
}

// TestV2ValidatesTypedIdentitiesWithoutCredentialValues proves the internal contract contains only opaque references and exact permissions.
func TestV2ValidatesTypedIdentitiesWithoutCredentialValues(t *testing.T) {
	spec := validIdentitySpecification()
	if err := Validate(spec); err != nil {
		t.Fatal(err)
	}
	if spec.Identities[0].CredentialRef != "cred_customer" || spec.Identities[0].Mechanism.Type != "bearer" {
		t.Fatal("identity declaration was not retained")
	}
}

// TestV2RejectsInvalidIdentityDeclarations covers duplicates, paths, target drift, mechanism unions, and incomplete operations.
func TestV2RejectsInvalidIdentityDeclarations(t *testing.T) {
	tests := []struct {
		name string
		edit func(*Specification)
		want error
	}{
		{"duplicate id", func(s *Specification) { s.Identities = append(s.Identities, s.Identities[0]) }, ErrDuplicateID},
		{"credential path", func(s *Specification) { s.Identities[0].CredentialRef = "../secret" }, ErrInvalidValue},
		{"target", func(s *Specification) { s.Identities[0].TargetID = "other" }, ErrUnknownReference},
		{"mechanism", func(s *Specification) { s.Identities[0].Mechanism.Type = "oauth" }, ErrInvalidValue},
		{"bearer name", func(s *Specification) { s.Identities[0].Mechanism.Name = "unexpected" }, ErrInvalidValue},
		{"empty header name", func(s *Specification) { s.Identities[0].Mechanism = AuthenticationMechanism{Type: "header"} }, ErrInvalidValue},
		{"bad cookie name", func(s *Specification) {
			s.Identities[0].Mechanism = AuthenticationMechanism{Type: "cookie", Name: "bad cookie"}
		}, ErrInvalidValue},
		{"reserved header", func(s *Specification) {
			s.Identities[0].Mechanism = AuthenticationMechanism{Type: "header", Name: "Authorization"}
		}, ErrInvalidValue},
		{"invalid json path", func(s *Specification) { s.Identities[0].Mechanism.SensitiveJSONPaths = []string{"body.token"} }, ErrInvalidValue},
		{"duplicate json path", func(s *Specification) {
			s.Identities[0].Mechanism.SensitiveJSONPaths = []string{"/body/token", "/body/token"}
		}, ErrDuplicateID},
		{"missing operations", func(s *Specification) { s.Identities[0].Operations = nil }, ErrRequired},
		{"capability", func(s *Specification) { s.Identities[0].Operations[0].Capability = "browser.navigate" }, ErrInvalidValue},
		{"method case", func(s *Specification) { s.Identities[0].Operations[0].Method = "get" }, ErrInvalidValue},
		{"duplicate operation", func(s *Specification) {
			s.Identities[0].Operations = append(s.Identities[0].Operations, s.Identities[0].Operations[0])
		}, ErrDuplicateID},
		{"path confusion", func(s *Specification) { s.Identities[0].Operations[0].Path = "/api/../admin" }, ErrInvalidValue},
		{"path escape", func(s *Specification) { s.Identities[0].Operations[0].Path = "/api/%2e%2e" }, ErrInvalidValue},
		{"path partial template", func(s *Specification) { s.Identities[0].Operations[0].Path = "/api/prefix{id}" }, ErrInvalidValue},
		{"path duplicate parameter", func(s *Specification) { s.Identities[0].Operations[0].Path = "/api/{id}/{id}" }, ErrInvalidValue},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			spec := validIdentitySpecification()
			test.edit(&spec)
			if err := Validate(spec); !errors.Is(err, test.want) {
				t.Fatalf("got %v, want %v", err, test.want)
			}
		})
	}
}

// TestV1IdentityCompatibilityRemainsStrict proves v1 gets no identity default and rejects an internal identity field.
func TestV1IdentityCompatibilityRemainsStrict(t *testing.T) {
	spec := validIdentitySpecification()
	spec.Version = 1
	spec.Identities = nil
	if err := Validate(spec); err != nil {
		t.Fatal(err)
	}
	spec.Identities = []TestIdentity{{ID: "unexpected"}}
	if err := Validate(spec); !errors.Is(err, ErrUnknownField) {
		t.Fatalf("got %v", err)
	}
}
