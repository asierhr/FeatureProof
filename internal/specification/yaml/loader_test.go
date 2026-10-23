package specyaml_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/asierhr/featureProof/internal/specification"
	specyaml "github.com/asierhr/featureProof/internal/specification/yaml"
)

const validYAML = `version: 1
feature:
  id: checkout-search
  description: Search returns products
target:
  id: staging
  base_url: https://staging.example.test
constraints:
  - id: search-results
    description: Search returns at least one result
    target_id: staging
limits:
  max_steps: 8
  max_tool_calls: 4
  max_retries: 1
`

func writeSpec(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "spec.yaml")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestLoadV1Specification(t *testing.T) {
	got, err := specyaml.Load(context.Background(), writeSpec(t, validYAML))
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	want := specification.Specification{
		Version:     1,
		Feature:     specification.Feature{ID: "checkout-search", Description: "Search returns products"},
		Target:      specification.Target{ID: "staging", BaseURL: "https://staging.example.test"},
		Constraints: []specification.Constraint{{ID: "search-results", Description: "Search returns at least one result", TargetID: "staging"}},
		Limits:      specification.Limits{MaxSteps: 8, MaxToolCalls: 4, MaxRetries: 1},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Load() = %#v, want %#v", got, want)
	}
}

func TestLoadRejectsUnreadableMalformedAndMultipleDocuments(t *testing.T) {
	tests := []struct {
		name string
		path func(*testing.T) string
	}{
		{"missing", func(t *testing.T) string { return filepath.Join(t.TempDir(), "missing.yaml") }},
		{"not a readable file", func(t *testing.T) string { return t.TempDir() }},
		{"malformed", func(t *testing.T) string { return writeSpec(t, "version: [") }},
		{"multiple documents", func(t *testing.T) string { return writeSpec(t, validYAML+"---\nversion: 1\n") }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := specyaml.Load(context.Background(), test.path(t))
			if err == nil || !reflect.DeepEqual(got, specification.Specification{}) {
				t.Fatalf("Load() = %#v, %v", got, err)
			}
		})
	}
}

func TestLoadRejectsInvalidRoots(t *testing.T) {
	for _, body := range []string{"", "null\n", "- version: 1\n"} {
		got, err := specyaml.Load(context.Background(), writeSpec(t, body))
		if !errors.Is(err, specification.ErrSyntax) || !reflect.DeepEqual(got, specification.Specification{}) {
			t.Fatalf("Load(%q) = %#v, %v", body, got, err)
		}
	}
}

func TestLoadRejectsUnknownAndDuplicateKeys(t *testing.T) {
	tests := []struct {
		name     string
		body     string
		category error
		field    string
	}{
		{"unknown root", validYAML + "typo: true\n", specification.ErrUnknownField, "$mapping.members[5]"},
		{"unknown nested", strings.Replace(validYAML, "  id: checkout-search", "  id: checkout-search\n  typo: value", 1), specification.ErrUnknownField, "feature.members[1]"},
		{"duplicate", strings.Replace(validYAML, "version: 1", "version: 1\nversion: 1", 1), specification.ErrSyntax, "version"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := specyaml.Load(context.Background(), writeSpec(t, test.body))
			var validationErr *specification.ValidationError
			if !errors.Is(err, test.category) || !errors.As(err, &validationErr) || validationErr.Field != test.field {
				t.Fatalf("Load() error = %#v", err)
			}
		})
	}
}

// TestLoadRejectsUnsupportedVersionWithReceivedAndSupported proves future envelopes are rejected before their members are interpreted.
func TestLoadRejectsUnsupportedVersionWithReceivedAndSupported(t *testing.T) {
	body := "version: 4\nfuture_field: true\n"
	got, err := specyaml.Load(context.Background(), writeSpec(t, body))
	var versionErr *specification.UnsupportedVersionError
	if !errors.Is(err, specification.ErrUnsupportedVersion) || !errors.As(err, &versionErr) {
		t.Fatalf("Load() = %#v, %v", got, err)
	}
	if versionErr.Received != 4 || !reflect.DeepEqual(versionErr.Supported, []int{1, 2, 3}) {
		t.Fatalf("UnsupportedVersionError = %#v", versionErr)
	}
}

func TestLoadRejectsMissingAndInvalidVersion(t *testing.T) {
	for _, body := range []string{"feature: {}\n", "version: \"1\"\n"} {
		_, err := specyaml.Load(context.Background(), writeSpec(t, body))
		var validationErr *specification.ValidationError
		if !errors.As(err, &validationErr) || validationErr.Field != "version" {
			t.Fatalf("Load(%q) error = %#v", body, err)
		}
	}
}

func TestSyntaxErrorPreservesLocation(t *testing.T) {
	_, err := specyaml.Load(context.Background(), writeSpec(t, "version: 1\nfeature: [\n"))
	var validationErr *specification.ValidationError
	if !errors.Is(err, specification.ErrSyntax) || !errors.As(err, &validationErr) {
		t.Fatalf("Load() error = %v", err)
	}
	if validationErr.Line != 2 || validationErr.Unwrap() == nil {
		t.Fatalf("ValidationError = %#v", validationErr)
	}
}

func TestLoadObservesPreCancelledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	got, err := specyaml.Load(ctx, writeSpec(t, validYAML))
	if !errors.Is(err, context.Canceled) || !reflect.DeepEqual(got, specification.Specification{}) {
		t.Fatalf("Load() = %#v, %v", got, err)
	}
}

func TestLoadRejectsSemanticErrorsWithoutPartialModel(t *testing.T) {
	tests := []struct {
		name     string
		body     string
		category error
		field    string
	}{
		{"missing retry limit", strings.Replace(validYAML, "  max_retries: 1\n", "", 1), specification.ErrRequired, "limits.max_retries"},
		{"duplicate constraint", strings.Replace(validYAML, "limits:\n", "  - id: search-results\n    description: Another\n    target_id: staging\nlimits:\n", 1), specification.ErrDuplicateID, "constraints[1].id"},
		{"unknown target", strings.Replace(validYAML, "    target_id: staging", "    target_id: production", 1), specification.ErrUnknownReference, "constraints[0].target_id"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := specyaml.Load(context.Background(), writeSpec(t, test.body))
			var validationErr *specification.ValidationError
			if !reflect.DeepEqual(got, specification.Specification{}) || !errors.Is(err, test.category) || !errors.As(err, &validationErr) || validationErr.Field != test.field {
				t.Fatalf("Load() = %#v, %#v", got, err)
			}
		})
	}
}

func TestLoadRejectsWrongScalarTypesWithFieldPathAndNoPartialModel(t *testing.T) {
	tests := []struct {
		name  string
		body  string
		field string
	}{
		{"number feature id", strings.Replace(validYAML, "  id: checkout-search", "  id: 123", 1), "feature.id"},
		{"boolean feature description", strings.Replace(validYAML, "  description: Search returns products", "  description: true", 1), "feature.description"},
		{"null target id", strings.Replace(validYAML, "  id: staging", "  id: null", 1), "target.id"},
		{"boolean base URL", strings.Replace(validYAML, "  base_url: https://staging.example.test", "  base_url: false", 1), "target.base_url"},
		{"number constraint id", strings.Replace(validYAML, "  - id: search-results", "  - id: 123", 1), "constraints[0].id"},
		{"boolean constraint description", strings.Replace(validYAML, "    description: Search returns at least one result", "    description: true", 1), "constraints[0].description"},
		{"null constraint target", strings.Replace(validYAML, "    target_id: staging", "    target_id: null", 1), "constraints[0].target_id"},
		{"string max steps", strings.Replace(validYAML, "  max_steps: 8", "  max_steps: \"8\"", 1), "limits.max_steps"},
		{"string max tool calls", strings.Replace(validYAML, "  max_tool_calls: 4", "  max_tool_calls: \"4\"", 1), "limits.max_tool_calls"},
		{"string max retries", strings.Replace(validYAML, "  max_retries: 1", "  max_retries: \"1\"", 1), "limits.max_retries"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := specyaml.Load(context.Background(), writeSpec(t, test.body))
			var validationErr *specification.ValidationError
			if !reflect.DeepEqual(got, specification.Specification{}) || !errors.Is(err, specification.ErrInvalidValue) || !errors.As(err, &validationErr) || validationErr.Field != test.field {
				t.Fatalf("Load() = %#v, %#v; want invalid_value at %q", got, err, test.field)
			}
		})
	}
}

func TestLoadRejectsNullConstraintWithFieldPathAndNoPartialModel(t *testing.T) {
	body := strings.Replace(validYAML, "  - id: search-results\n    description: Search returns at least one result\n    target_id: staging", "  - null", 1)
	got, err := specyaml.Load(context.Background(), writeSpec(t, body))
	var validationErr *specification.ValidationError
	if !reflect.DeepEqual(got, specification.Specification{}) || !errors.Is(err, specification.ErrInvalidValue) || !errors.As(err, &validationErr) || validationErr.Field != "constraints[0]" {
		t.Fatalf("Load() = %#v, %#v", got, err)
	}
}
