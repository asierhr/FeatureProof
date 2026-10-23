package specyaml_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/asierhr/featureProof/internal/specification"
	"github.com/asierhr/featureProof/internal/specification/yaml"
)

// writeFixtureSpec writes one hermetic YAML v3 document for strict loader tests.
func writeFixtureSpec(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "spec.yaml")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// validFixtureYAML returns the normative v3 shape with nested literals, a typed capture, and self cleanup reference.
func validFixtureYAML() string {
	return `version: 3
feature: {id: account-summary, description: account summary}
target: {id: deployed, base_url: https://preview.example.test}
constraints:
  - {id: visible, description: account visible, target_id: deployed}
limits: {max_steps: 8, max_tool_calls: 8, max_retries: 1}
identities:
  - id: writer
    target_id: deployed
    credential_ref: writer_ref
    mechanism: {type: bearer}
    operations:
      - {capability: http.request, method: POST, path: /api/accounts}
      - {capability: http.request, method: DELETE, path: "/api/accounts/{account_id}"}
fixtures:
  - id: account
    depends_on: []
    create:
      operation: {method: POST, path: /api/accounts}
      identity_id: writer
      input:
        body:
          object:
            name: {literal: FeatureProof}
      accepted_statuses: [201]
      idempotency: contract
    capture:
      - {name: account_id, path: [body, id], value_type: string, max_bytes: 128}
    cleanup:
      operation: {method: DELETE, path: "/api/accounts/{account_id}"}
      identity_id: writer
      input:
        path_parameters:
          account_id:
            reference: {fixture_id: account, output: account_id, value_type: string}
      accepted_statuses: [204, 404]
      idempotency: contract
`
}

// TestV3LoadsStrictFixtureDeclarations proves v3 translation preserves typed fixture fields and reference unions.
func TestV3LoadsStrictFixtureDeclarations(t *testing.T) {
	got, err := specyaml.Load(context.Background(), writeFixtureSpec(t, validFixtureYAML()))
	if err != nil {
		t.Fatalf("%v cause=%v", err, errors.Unwrap(err))
	}
	if got.Version != 3 || len(got.Fixtures) != 1 || got.Fixtures[0].Create.Input.Body == nil || got.Fixtures[0].Create.Input.Body.Kind != "object" || got.Fixtures[0].Cleanup.Input.PathParameters["account_id"].Reference.Output != "account_id" {
		t.Fatalf("unexpected fixture: %#v", got.Fixtures)
	}
}

// TestV3RejectsUnknownDuplicateAndInvalidFixtureFields exercises strict schema and stable field diagnostics.
func TestV3RejectsUnknownDuplicateAndInvalidFixtureFields(t *testing.T) {
	cases := []struct {
		name, body, field string
		sentinel          error
	}{{"unknown", validFixtureYAML() + "    surprise: true\n", "fixtures[0].members[5]", specification.ErrUnknownField}, {"ambiguous union", replaceOnce(validFixtureYAML(), "name: {literal: FeatureProof}", "name: {literal: FeatureProof, reference: {fixture_id: account, output: account_id, value_type: string}}"), "fixtures[0].create.input.body.object.name", specification.ErrInvalidValue}, {"bad status", replaceOnce(validFixtureYAML(), "accepted_statuses: [201]", "accepted_statuses: [99]"), "fixtures[0].create.accepted_statuses[0]", specification.ErrInvalidValue}}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			_, err := specyaml.Load(context.Background(), writeFixtureSpec(t, test.body))
			var validation *specification.ValidationError
			if !errors.Is(err, test.sentinel) || !errors.As(err, &validation) || validation.Field != test.field {
				t.Fatalf("error=%#v", err)
			}
		})
	}
}

// TestV3RejectsNonFiniteFixtureLiteral proves YAML numeric extensions cannot enter the finite JSON model.
func TestV3RejectsNonFiniteFixtureLiteral(t *testing.T) {
	body := replaceOnce(validFixtureYAML(), "name: {literal: FeatureProof}", "name: {literal: .nan}")
	if _, err := specyaml.Load(context.Background(), writeFixtureSpec(t, body)); !errors.Is(err, specification.ErrInvalidValue) {
		t.Fatalf("non-finite fixture literal accepted: %v", err)
	}
}

// replaceOnce changes one known fixture fragment without introducing test-side parsing logic.
func replaceOnce(source, old, replacement string) string {
	for i := 0; i+len(old) <= len(source); i++ {
		if source[i:i+len(old)] == old {
			return source[:i] + replacement + source[i+len(old):]
		}
	}
	return source
}

// TestV1V2CompatibilityRemainsFixtureFree confirms legacy documents retain no fixture defaults and reject the new field.
func TestV1V2CompatibilityRemainsFixtureFree(t *testing.T) {
	legacy := `version: 1
feature: {id: f, description: feature}
target: {id: t, base_url: https://example.test}
constraints: [{id: c, description: condition, target_id: t}]
limits: {max_steps: 1, max_tool_calls: 1, max_retries: 0}
`
	got, err := specyaml.Load(context.Background(), writeFixtureSpec(t, legacy))
	if err != nil || got.Fixtures != nil {
		t.Fatalf("legacy=%#v %v", got, err)
	}
	_, err = specyaml.Load(context.Background(), writeFixtureSpec(t, legacy+"fixtures: []\n"))
	if !errors.Is(err, specification.ErrUnknownField) {
		t.Fatalf("fixtures accepted by v1: %v", err)
	}
}

// TestV3LoadsEveryRecursiveInputArm proves query, object, array, literal, reference, and integer selectors translate through the public loader.
func TestV3LoadsEveryRecursiveInputArm(t *testing.T) {
	body := replaceOnce(validFixtureYAML(), "      input:\n        body:\n          object:\n            name: {literal: FeatureProof}", "      input:\n        query:\n          page: {literal: 1}\n        body:\n          object:\n            name: {literal: FeatureProof}\n            flags:\n              array:\n                - {literal: true}")
	body = replaceOnce(body, "path: [body, id]", "path: [body, 0]")
	got, err := specyaml.Load(context.Background(), writeFixtureSpec(t, body))
	if err != nil || got.Fixtures[0].Create.Input.Query["page"].Kind != "literal" || got.Fixtures[0].Create.Input.Body.Object["flags"].Kind != "array" || got.Fixtures[0].Captures[0].Path[1].Index == nil {
		t.Fatalf("recursive fixture translation failed: %#v err=%v", got.Fixtures, err)
	}
}

// TestV3RejectsStructuralFixtureBoundaryMatrix exercises strict kinds, required keys, recursive unions, and selector scalars.
func TestV3RejectsStructuralFixtureBoundaryMatrix(t *testing.T) {
	base := validFixtureYAML()
	cases := []string{
		replaceOnce(base, "fixtures:\n", "fixtures: {}\nignored:\n"),
		replaceOnce(base, "    depends_on: []", "    depends_on: account"),
		replaceOnce(base, "    create:\n", "    create:\n      unknown: true\n"),
		replaceOnce(base, "operation: {method: POST, path: /api/accounts}", "operation: {path: /api/accounts}"),
		replaceOnce(base, "accepted_statuses: [201]", "accepted_statuses: 201"),
		replaceOnce(base, "accepted_statuses: [201]", "accepted_statuses: [created]"),
		replaceOnce(base, "      input:\n        body:", "      input:\n        path_parameters: []\n        body:"),
		replaceOnce(base, "name: {literal: FeatureProof}", "name: {reference: {fixture_id: account, value_type: string}}"),
		replaceOnce(base, "name: {literal: FeatureProof}", "name: {object: []}"),
		replaceOnce(base, "name: {literal: FeatureProof}", "name: {array: {literal: true}}"),
		replaceOnce(base, "path: [body, id]", "path: [body, true]"),
		replaceOnce(base, "capture:\n      -", "capture: {}\n      -"),
	}
	for index, body := range cases {
		if _, err := specyaml.Load(context.Background(), writeFixtureSpec(t, body)); err == nil {
			t.Fatalf("fixture structural mutation %d accepted", index)
		}
	}
}
