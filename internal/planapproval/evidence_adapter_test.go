package planapproval

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/asierhr/featureProof/internal/evidence"
)

// TestEvidenceSanitizerReusesConfiguredRedaction proves approval does not invent a weaker secret boundary.
func TestEvidenceSanitizerReusesConfiguredRedaction(t *testing.T) {
	if _, err := NewEvidenceSanitizer(evidence.Sanitizer{}); err == nil {
		t.Fatal("unconfigured sanitizer accepted")
	}
	policy, err := evidence.NewPolicy(evidence.Config{MaxBodyBytes: 1024})
	if err != nil {
		t.Fatal(err)
	}
	adapter, err := NewEvidenceSanitizer(evidence.NewSanitizer(policy))
	if err != nil {
		t.Fatal(err)
	}
	value, err := adapter.Sanitize(context.Background(), map[string]any{"headers": map[string]any{"Authorization": "SECRET"}})
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(value)
	if contains(string(raw), "SECRET") || !contains(string(raw), evidence.Redacted) {
		t.Fatalf("redaction not reused: %s", raw)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := adapter.Sanitize(ctx, map[string]any{}); err == nil {
		t.Fatal("sanitizer cancellation ignored")
	}
}
