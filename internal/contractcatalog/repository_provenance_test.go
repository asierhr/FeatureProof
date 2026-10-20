package contractcatalog

import (
	"encoding/json"
	"testing"
)

// TestRepositoryRevisionProvenanceParticipatesInCanonicalDigest closes the Spring audit identity deep-copy boundary.
func TestRepositoryRevisionProvenanceParticipatesInCanonicalDigest(t *testing.T) {
	first := testSnapshot("spring", "spring-static-v1", ClassDerived, ConfidenceMedium, CompletenessPartial, OperationKey{Method: "GET", Path: "/x"})
	first.Source.RepositoryID, first.Source.Revision = "repo-a", "0123456789abcdef0123456789abcdef01234567"
	first.Operations[0].Provenance[0].RepositoryID, first.Operations[0].Provenance[0].Revision = first.Source.RepositoryID, first.Source.Revision
	first.Operations[0].Claims = ClaimsForOperation(first.Operations[0], first.Operations[0].Provenance[0])
	second := cloneSnapshot(first)
	second.Source.RepositoryID = "repo-b"
	firstBytes, _ := json.Marshal(first)
	secondBytes, _ := json.Marshal(second)
	if string(firstBytes) == string(secondBytes) {
		t.Fatal("repository identity omitted from canonical source")
	}
	copied := cloneSnapshot(first)
	copied.Operations[0].Provenance[0].RepositoryID = "mutated"
	if first.Operations[0].Provenance[0].RepositoryID != "repo-a" {
		t.Fatal("provenance was not deep-copied")
	}
}
