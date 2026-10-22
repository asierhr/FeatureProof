package evidence

import (
	"context"
	"testing"

	"github.com/asierhr/featureProof/internal/specification"
)

// TestAuthRedactionIsCaseInsensitiveMonotonicAndFailClosed proves mandatory and declared locations cannot be removed by configuration.
func TestAuthRedactionIsCaseInsensitiveMonotonicAndFailClosed(t *testing.T) {
	identities := []specification.TestIdentity{{Mechanism: specification.AuthenticationMechanism{Type: "header", Name: "X-Test-Key", SensitiveJSONPaths: []string{"/body/session/secret"}}}}
	config := AugmentAuthenticationConfig(Config{MaxBodyBytes: 4096, AdditionalHeaders: []string{"X-Existing"}, SensitiveJSONPaths: []string{"/body/private"}}, identities)
	policy, err := NewPolicy(config)
	if err != nil {
		t.Fatal(err)
	}
	sanitized, err := NewSanitizer(policy).Sanitize(context.Background(), map[string]any{"headers": map[string]any{"x-TEST-key": "CANARY-H", "Set-Cookie": "CANARY-C"}, "body": map[string]any{"token": "CANARY-T", "private": "CANARY-P", "session": map[string]any{"secret": "CANARY-S"}, "visible": "ok"}})
	if err != nil {
		t.Fatal(err)
	}
	value := sanitized.Value().(map[string]any)
	headers := value["headers"].(map[string]any)
	body := value["body"].(map[string]any)
	if headers["x-TEST-key"] != Redacted || headers["Set-Cookie"] != Redacted || body["token"] != Redacted || body["private"] != Redacted || body["session"].(map[string]any)["secret"] != Redacted || body["visible"] != "ok" {
		t.Fatalf("redaction result: %#v", value)
	}
}

// TestAuthenticationRedactionRejectsUnclassifiablePayloadWithoutPartialValue proves sanitizer failure never returns a publishable root.
func TestAuthenticationRedactionRejectsUnclassifiablePayloadWithoutPartialValue(t *testing.T) {
	policy, err := NewPolicy(Config{MaxBodyBytes: 100})
	if err != nil {
		t.Fatal(err)
	}
	value, err := NewSanitizer(policy).Sanitize(context.Background(), map[string]any{"body": make(chan string)})
	if err == nil || value.Value() != nil {
		t.Fatalf("unsafe value survived: %#v %v", value, err)
	}
}
