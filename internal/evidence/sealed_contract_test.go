package evidence_test

import (
	"errors"
	"testing"

	"github.com/asierhr/featureProof/internal/evidence"
)

// TestSanitizedValueCannotBeFabricatedOutsideEvidence proves zero-value results cannot seal records.
func TestSanitizedValueCannotBeFabricatedOutsideEvidence(t *testing.T) {
	_, err := evidence.NewRecord(evidence.Record{ID: "forged"}, evidence.SanitizedValue{}, evidence.SanitizedValue{})
	if !errors.Is(err, evidence.ErrUntreated) {
		t.Fatalf("fabricated sanitizer result accepted: %v", err)
	}
	legacy := evidence.Sanitizer{Sensitive: map[string]bool{"secret": true}}
	result, sanitizeErr := legacy.Sanitize(t.Context(), map[string]any{"secret": "raw"})
	if sanitizeErr == nil {
		t.Fatal("policyless compatibility sanitizer minted a complete result")
	}
	if _, err := evidence.NewRecord(evidence.Record{ID: "legacy"}, result, result); !errors.Is(err, evidence.ErrUntreated) {
		t.Fatalf("policyless result sealed: %v", err)
	}
}
