package contractcatalog

import (
	"context"
	"strings"
	"testing"
)

// approvedReviewFixture builds additive original and human provenance under the special approved-source contract.
func approvedReviewFixture() (SourceSnapshot, SourceRequest) {
	original := Provenance{SourceID: "derived", RepositoryID: "repo", Revision: strings.Repeat("a", 40), Class: ClassDerived, SourceDigest: testDigest("derived"), AdapterVersion: "derived-v1", SafeLocation: "src/handler", Confidence: ConfidenceMedium, Completeness: CompletenessPartial}
	approval := Provenance{SourceID: "review-1", RepositoryID: "repo", Revision: strings.Repeat("a", 40), Class: ClassAuthoritative, SourceDigest: testDigest("projection"), AdapterVersion: "contract-review-v1", SafeLocation: "contract-review/review-1", Confidence: ConfidenceMedium, Completeness: CompletenessPartial}
	operation := OperationDescriptor{Key: OperationKey{Method: "GET", Path: "/pets"}, LogicalID: "pets", Responses: map[string]Response{"200": {}}, Provenance: []Provenance{original, approval}, Confidence: ConfidenceMedium, Completeness: CompletenessPartial, Classes: []SourceClass{ClassAuthoritative, ClassDerived}}
	operation.Claims = ClaimsForOperation(operation, original)
	for index := range operation.Claims {
		operation.Claims[index].Provenance = append(operation.Claims[index].Provenance, approval)
	}
	snapshot := SourceSnapshot{Version: VersionV1, Source: SourceSummary{ID: approval.SourceID, RepositoryID: approval.RepositoryID, Revision: approval.Revision, Kind: "review-approved-v1", Class: approval.Class, Digest: approval.SourceDigest, AdapterVersion: approval.AdapterVersion, Confidence: approval.Confidence, Completeness: approval.Completeness}, Operations: []OperationDescriptor{operation}}
	request := NewSourceRequest(SourceDescriptor{Kind: snapshot.Source.Kind, ID: snapshot.Source.ID, Class: snapshot.Source.Class, Confidence: snapshot.Source.Confidence, Completeness: snapshot.Source.Completeness, ExpectedDigest: snapshot.Source.Digest, AdapterVersion: snapshot.Source.AdapterVersion, Limits: catalogLimits(), Projection: testProjectionPolicy()})
	return snapshot, request
}

// TestApprovedReviewSnapshotRequiresCompleteAdditiveLineageAndClosedMetadata covers nominal and adversarial special-source validation.
func TestApprovedReviewSnapshotRequiresCompleteAdditiveLineageAndClosedMetadata(t *testing.T) {
	snapshot, request := approvedReviewFixture()
	if err := validateSnapshot(context.Background(), snapshot, request); err != nil {
		t.Fatalf("valid approved snapshot rejected: %v", err)
	}
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	if err := validateSnapshot(cancelled, snapshot, request); err == nil {
		t.Fatal("cancelled approved snapshot validation continued")
	}
	tests := map[string]func(*SourceSnapshot){
		"original_lineage": func(value *SourceSnapshot) { value.Operations[0].Provenance = value.Operations[0].Provenance[1:] },
		"approval_lineage": func(value *SourceSnapshot) { value.Operations[0].Provenance = value.Operations[0].Provenance[:1] },
		"claim_count":      func(value *SourceSnapshot) { value.Operations[0].Claims = value.Operations[0].Claims[:1] },
		"diagnostic":       func(value *SourceSnapshot) { value.Diagnostics = []SourceDiagnostic{{Code: strings.Repeat("x", 65)}} },
		"affected": func(value *SourceSnapshot) {
			value.AffectedOperations = []OperationKey{{Method: "GET", Path: "/absent"}}
		},
		"unsafe_provenance": func(value *SourceSnapshot) {
			value.Operations[0].Claims[0].Provenance[1].SafeLocation = strings.Repeat("x", 257)
		},
		"unnamed_scheme": func(value *SourceSnapshot) { value.SecuritySchemes = []SecurityScheme{{}} },
		"duplicate_scheme": func(value *SourceSnapshot) {
			value.SecuritySchemes = []SecurityScheme{{Name: "oauth", Type: "oauth2"}, {Name: "oauth", Type: "apiKey"}}
		},
		"scheme": func(value *SourceSnapshot) {
			value.Operations[0].Security = []SecurityRequirement{{Schemes: map[string][]string{"oauth": {"read"}}}}
			value.Operations[0].SecuritySchemes = map[string]SecurityScheme{"oauth": {Name: "oauth", Type: "oauth2"}}
			value.Operations[0].Claims = ClaimsForOperation(value.Operations[0], value.Operations[0].Provenance[0])
			for index := range value.Operations[0].Claims {
				value.Operations[0].Claims[index].Provenance = append(value.Operations[0].Claims[index].Provenance, value.Operations[0].Provenance[1])
			}
		},
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			value := cloneSnapshot(snapshot)
			mutate(&value)
			if err := validateSnapshot(context.Background(), value, request); err == nil {
				t.Fatal("invalid approved snapshot accepted")
			}
		})
	}
}
