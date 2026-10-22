package specyaml_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/asierhr/featureProof/internal/specification"
	specyaml "github.com/asierhr/featureProof/internal/specification/yaml"
)

// validV2YAML returns a complete external identity contract without embedding credential material.
func validV2YAML() string {
	return `version: 2
feature:
  id: accounts
  description: authenticated accounts
target:
  id: deployed
  base_url: https://example.test
constraints:
  - id: c1
    description: customer reads account
    target_id: deployed
limits:
  max_steps: 3
  max_tool_calls: 3
  max_retries: 1
identities:
  - id: customer
    target_id: deployed
    credential_ref: cred_customer
    mechanism:
      type: header
      name: X-Test-Key
      sensitive_json_paths:
        - /body/session
    operations:
      - capability: http.request
        method: GET
        path: /api/accounts/{account_id}
`
}

// TestV2LoadsTypedIdentitiesWithoutCredentialValues verifies strict adapter translation into independent domain entities.
func TestV2LoadsTypedIdentitiesWithoutCredentialValues(t *testing.T) {
	spec, err := specyaml.Load(context.Background(), writeSpec(t, validV2YAML()))
	if err != nil {
		t.Fatal(err)
	}
	if spec.Version != 2 || len(spec.Identities) != 1 || spec.Identities[0].CredentialRef != "cred_customer" || spec.Identities[0].Mechanism.Name != "X-Test-Key" {
		t.Fatalf("unexpected v2 translation: %#v", spec)
	}
}

// TestV2RejectsInlineSecretsWithoutEcho proves unknown secret-bearing members fail before their canary value reaches diagnostics.
func TestV2RejectsInlineSecretsWithoutEcho(t *testing.T) {
	for _, member := range []string{"token", "cookie", "password", "api_key", "value"} {
		body := strings.Replace(validV2YAML(), "    operations:\n", "    "+member+": CANARY-SECRET\n    operations:\n", 1)
		_, err := specyaml.Load(context.Background(), writeSpec(t, body))
		if !errors.Is(err, specification.ErrUnknownField) || strings.Contains(err.Error(), "CANARY-SECRET") {
			t.Fatalf("member %s leaked or was accepted: %v", member, err)
		}
	}
}

// TestV2RejectsDuplicateAndObjectCredentialReference covers duplicate keys and non-scalar opaque references.
func TestV2RejectsDuplicateAndObjectCredentialReference(t *testing.T) {
	bodies := []string{
		strings.Replace(validV2YAML(), "    target_id: deployed\n", "    target_id: deployed\n    target_id: deployed\n", 1),
		strings.Replace(validV2YAML(), "    credential_ref: cred_customer", "    credential_ref:\n      env: CANARY-SECRET", 1),
	}
	for _, body := range bodies {
		_, err := specyaml.Load(context.Background(), writeSpec(t, body))
		if err == nil || strings.Contains(err.Error(), "CANARY-SECRET") {
			t.Fatalf("unsafe document result: %v", err)
		}
	}
}

// TestV2RejectsMalformedIdentityShapes covers every nested collection and union boundary before adapter translation.
func TestV2RejectsMalformedIdentityShapes(t *testing.T) {
	bodies := []string{
		strings.Replace(validV2YAML(), "identities:\n  - id:", "identities: invalid\nremoved:\n  - id:", 1),
		strings.Replace(validV2YAML(), "  - id: customer", "  - invalid", 1),
		strings.Replace(validV2YAML(), "    credential_ref: cred_customer\n", "", 1),
		strings.Replace(validV2YAML(), "  - id: customer", "  - id: [bad]", 1),
		strings.Replace(validV2YAML(), "    mechanism:\n      type: header", "    mechanism: header", 1),
		strings.Replace(validV2YAML(), "      type: header\n", "", 1),
		strings.Replace(validV2YAML(), "      name: X-Test-Key", "      name: 7", 1),
		strings.Replace(validV2YAML(), "      sensitive_json_paths:\n        - /body/session", "      sensitive_json_paths: /body/session", 1),
		strings.Replace(validV2YAML(), "        - /body/session", "        - 7", 1),
		strings.Replace(validV2YAML(), "    operations:\n      - capability:", "    operations: invalid\nremoved_operations:\n      - capability:", 1),
		strings.Replace(validV2YAML(), "      - capability: http.request", "      - invalid", 1),
		strings.Replace(validV2YAML(), "        path: /api/accounts/{account_id}\n", "", 1),
		strings.Replace(validV2YAML(), "        method: GET", "        method: 7", 1),
	}
	for i, body := range bodies {
		if _, err := specyaml.Load(context.Background(), writeSpec(t, body)); err == nil {
			t.Fatalf("malformed identity shape %d was accepted", i)
		}
	}
}

// TestV1CompatibilityRemainsIdentityFree proves the unchanged fixture does not receive a synthesized identity.
func TestV1CompatibilityRemainsIdentityFree(t *testing.T) {
	spec, err := specyaml.Load(context.Background(), "testdata/valid_v1.yaml")
	if err != nil {
		t.Fatal(err)
	}
	if spec.Version != 1 || spec.Identities != nil {
		t.Fatalf("v1 semantics changed: %#v", spec.Identities)
	}
}
