package openapi

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/asierhr/featureProof/internal/contractcatalog"
)

// TestExcludedValueTraversalIsDeterministicCancelableAndAllOrNothing covers the private pre-publication traversal.
func TestExcludedValueTraversalIsDeterministicCancelableAndAllOrNothing(t *testing.T) {
	location := "entry#/projection/omitted"
	limits := excludedTraversalLimits{MaxDepth: 16, MaxNodes: 64, MaxBytes: 1024, MaxValues: 16}
	value := map[string]any{"schema": map[string]any{"examples": []any{"second", map[string]any{"nested": "first"}}, "default": "third"}}
	values, err := collectExcludedValues(context.Background(), value, limits, location)
	if err != nil || strings.Join(values, ",") != "first,second,third" {
		t.Fatalf("deterministic values=%v err=%v", values, err)
	}

	cancelled := &compatibilityCancellingContext{Context: context.Background()}
	cancelled.remaining.Store(2)
	values, err = collectExcludedValues(cancelled, value, limits, location)
	assertOpenAPITraversalError(t, values, err, Cancelled, location, context.Canceled)

	deadline, cancel := context.WithDeadline(context.Background(), time.Unix(1, 0))
	defer cancel()
	values, err = collectExcludedValues(deadline, value, limits, location)
	assertOpenAPITraversalError(t, values, err, Cancelled, location, context.DeadlineExceeded)
}

// TestExcludedValueTraversalRejectsEveryWorkDimensionAndCycles proves bounded stack, CPU and retained memory.
func TestExcludedValueTraversalRejectsEveryWorkDimensionAndCycles(t *testing.T) {
	location := "entry#/projection/omitted"
	base := excludedTraversalLimits{MaxDepth: 16, MaxNodes: 64, MaxBytes: 1024, MaxValues: 16}
	cases := []struct {
		name   string
		value  any
		limits excludedTraversalLimits
	}{
		{"depth", map[string]any{"examples": []any{[]any{"secret"}}}, excludedTraversalLimits{MaxDepth: 2, MaxNodes: 64, MaxBytes: 1024, MaxValues: 16}},
		{"nodes", map[string]any{"examples": []any{"one", "two"}}, excludedTraversalLimits{MaxDepth: 16, MaxNodes: 2, MaxBytes: 1024, MaxValues: 16}},
		{"bytes", map[string]any{"examples": []any{"secret"}}, excludedTraversalLimits{MaxDepth: 16, MaxNodes: 64, MaxBytes: 10, MaxValues: 16}},
		{"values", map[string]any{"examples": []any{"one", "two"}}, excludedTraversalLimits{MaxDepth: 16, MaxNodes: 64, MaxBytes: 1024, MaxValues: 1}},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			values, err := collectExcludedValues(context.Background(), test.value, test.limits, location)
			assertOpenAPITraversalError(t, values, err, LimitExceeded, location, nil)
		})
	}
	cycle := map[string]any{}
	cycle["examples"] = cycle
	values, err := collectExcludedValues(context.Background(), cycle, base, location)
	assertOpenAPITraversalError(t, values, err, LimitExceeded, location, nil)
}

// TestLoaderPublishesNoContractWhenExcludedTraversalExhausts proves the bounded traversal is part of atomic loading.
func TestLoaderPublishesNoContractWhenExcludedTraversalExhausts(t *testing.T) {
	root := t.TempDir()
	writeFixture(t, root, "limited.yaml", `openapi: "3.1.0"
info: {title: limited, version: "1"}
paths:
  /pets:
    post:
      requestBody:
        content:
          application/json:
            schema: {type: string, examples: [one, two]}
      responses: {"204": {description: ok}}
`)
	config := testConfig(root)
	config.MaxReferences = 1
	loader, err := NewLoader(context.Background(), config, LocalFS{})
	if err != nil {
		t.Fatal(err)
	}
	contract, err := loader.Load(context.Background(), "limited.yaml")
	var typed *Error
	if contract != nil || !errors.As(err, &typed) || typed.Category != LimitExceeded || !strings.HasSuffix(typed.Location, "#/projection/omitted") {
		t.Fatalf("partial contract=%#v typed=%#v err=%v", contract, typed, err)
	}
}

// assertOpenAPITraversalError requires a safe typed failure and no partially collected values.
func assertOpenAPITraversalError(t *testing.T, values []string, err error, category ErrorCategory, location string, cause error) {
	t.Helper()
	var typed *Error
	if len(values) != 0 || !errors.As(err, &typed) || typed.Category != category || typed.Location != location {
		t.Fatalf("values=%v typed=%#v err=%v", values, typed, err)
	}
	if cause != nil && !errors.Is(err, cause) {
		t.Fatalf("cause %v not preserved by %v", cause, err)
	}
	raw, marshalErr := json.Marshal(err)
	if marshalErr != nil || strings.Contains(string(raw), "secret") {
		t.Fatalf("unsafe error=%s marshal=%v", raw, marshalErr)
	}
}

// TestNeutralCompatibilityReturnsSafeTypedCancellation covers initial, deadline and mid-conversion termination.
func TestNeutralCompatibilityReturnsSafeTypedCancellation(t *testing.T) {
	root := t.TempDir()
	writeFixture(t, root, "api.json", validDocument("3.1.0", true))
	contract := loadFixture(t, root, "api.json", testConfig(root))
	reduced, err := contract.BuildContext(context.Background(), ContextRequest{Constraints: []ConstraintSelection{{ConstraintID: "c", Operations: []OperationKey{{Method: "GET", Path: "/pets"}}}}})
	if err != nil {
		t.Fatal(err)
	}
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = NeutralizeReducedContext(cancelled, reduced, "sensitive-source", strings.Repeat("a", 64), "policy")
	assertCatalogCancellation(t, err, "openapi.compatibility", context.Canceled)

	deadline, stop := context.WithDeadline(context.Background(), time.Unix(1, 0))
	defer stop()
	_, err = NeutralizeReducedContext(deadline, reduced, "sensitive-source", strings.Repeat("a", 64), "policy")
	assertCatalogCancellation(t, err, "openapi.compatibility", context.DeadlineExceeded)

	midway := &compatibilityCancellingContext{Context: context.Background()}
	midway.remaining.Store(2)
	_, err = NeutralizeReducedContext(midway, reduced, "sensitive-source", strings.Repeat("a", 64), "policy")
	assertCatalogCancellation(t, err, "openapi.compatibility.operations", context.Canceled)

	request := contractcatalog.NewSourceRequest(contractcatalog.SourceDescriptor{Kind: "openapi", ID: "source", Class: contractcatalog.ClassAuthoritative, Confidence: contractcatalog.ConfidenceHigh, Completeness: contractcatalog.CompletenessComplete, ExpectedDigest: strings.Repeat("a", 64), AdapterVersion: "openapi-v1", Input: contract, Limits: contractcatalog.Limits{MaxSources: 1, MaxOperations: 8, MaxClaims: 128, MaxBytes: 1 << 20, MaxDepth: 32, MaxDiagnostics: 8, MaxLocationBytes: 256}, Projection: contractcatalog.ProjectionPolicy{MaxStringBytes: 4096, AllowedURLSchemes: []string{"https"}, SensitiveNames: []string{}, SensitivePaths: []string{}, SensitiveHeaderNames: []string{}, ForbiddenValues: []string{}}})
	_, err = (ContractSourceAdapter{AdapterVersion: "openapi-v1"}).Load(cancelled, request)
	assertCatalogCancellation(t, err, "openapi.adapter", context.Canceled)
}

// assertCatalogCancellation requires the neutral error category, structural location and unwrap cause without payload leakage.
func assertCatalogCancellation(t *testing.T, err error, location string, cause error) {
	t.Helper()
	var typed *contractcatalog.Error
	if !errors.As(err, &typed) || typed.Category != contractcatalog.CategoryCancelled || typed.Location != location || !errors.Is(err, cause) {
		t.Fatalf("typed cancellation=%#v err=%v", typed, err)
	}
	raw, marshalErr := json.Marshal(err)
	if marshalErr != nil || strings.Contains(string(raw), "sensitive-source") {
		t.Fatalf("unsafe cancellation=%s marshal=%v", raw, marshalErr)
	}
}
