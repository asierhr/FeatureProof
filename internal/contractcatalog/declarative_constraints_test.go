package contractcatalog

import "testing"

// TestDeclarativeConstraintsCanonicalizeCloneAndClaim verifies inert security metadata participates in immutable canonical identity.
func TestDeclarativeConstraintsCanonicalizeCloneAndClaim(t *testing.T) {
	provenance := Provenance{SourceID: "source", Class: ClassDerived, SourceDigest: testDigest("source"), AdapterVersion: "spring-static-v1", SafeLocation: "A.java[1]/C.m/PreAuthorize", Confidence: ConfidenceMedium, Completeness: CompletenessPartial}
	operation := OperationDescriptor{Key: OperationKey{Method: "GET", Path: "/x"}, Responses: map[string]Response{"200": {}}, Security: []SecurityRequirement{}, DeclarativeConstraints: []DeclarativeConstraint{{Kind: "roles_allowed", Values: []string{"B", "A", "A"}, Provenance: []Provenance{provenance}}}, Provenance: []Provenance{provenance}, Confidence: ConfidenceMedium, Completeness: CompletenessPartial, Classes: []SourceClass{ClassDerived}}
	canonical := canonicalOperation(operation)
	if len(canonical.DeclarativeConstraints[0].Values) != 2 || canonical.DeclarativeConstraints[0].Values[0] != "A" {
		t.Fatalf("not canonical: %+v", canonical.DeclarativeConstraints)
	}
	claims := ClaimsForOperation(canonical, provenance)
	found := false
	for _, claim := range claims {
		if claim.Field == "declarative_constraints/0" {
			found = true
		}
	}
	if !found {
		t.Fatal("constraint missing from claims")
	}
	clone := cloneOperation(canonical)
	clone.DeclarativeConstraints[0].Values[0] = "mutated"
	if canonical.DeclarativeConstraints[0].Values[0] != "A" {
		t.Fatal("clone alias retained")
	}
}

// TestDeclarativeConstraintsApplyProjectionPolicy verifies forbidden role or expression values fail before catalog retention.
func TestDeclarativeConstraintsApplyProjectionPolicy(t *testing.T) {
	policy := ProjectionPolicy{MaxStringBytes: 128, AllowedURLSchemes: []string{"https"}, SensitiveNames: []string{}, SensitivePaths: []string{}, SensitiveHeaderNames: []string{}, ForbiddenValues: []string{"CANARY"}}
	operation := OperationDescriptor{Key: OperationKey{Method: "GET", Path: "/x"}, Responses: map[string]Response{"200": {}}, DeclarativeConstraints: []DeclarativeConstraint{{Kind: "pre_authorize", Expression: "CANARY"}}}
	if validateProjection(operation, policy) == nil {
		t.Fatal("forbidden declarative constraint accepted")
	}
}
