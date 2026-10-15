package agenteval

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// expectationDocument returns the mutable expected-outcome object from one controlled fixture document.
func expectationDocument(document map[string]any) map[string]any {
	return document["expected"].(map[string]any)
}

// TestEveryStructuralExpectationSetRejectsDuplicates covers all general set and relation uniqueness rules from REV-10.
func TestEveryStructuralExpectationSetRejectsDuplicates(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(map[string]any)
	}{
		{"required_constraints", func(expected map[string]any) { expected["required_constraints"] = []any{"c1", "c1"} }},
		{"allowed_capabilities", func(expected map[string]any) {
			expected["allowed_capabilities"] = []any{"http.request", "http.request"}
		}},
		{"allowed_identities", func(expected map[string]any) { expected["allowed_identities"] = []any{"user-a", "user-a"} }},
		{"allowed_evidence", func(expected map[string]any) { expected["allowed_evidence"] = []any{"step/assert", "step/assert"} }},
		{"operations", func(expected map[string]any) {
			operation := expected["operations"].([]any)[0]
			expected["operations"] = []any{operation, operation}
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			paths := writeEvaluationFixture(t, "plan")
			rewriteFixtureCase(t, paths, func(document map[string]any) { test.mutate(expectationDocument(document)) })
			loader, _ := NewLoader(StaticContractRegistry{"http.request/v1": AcceptDescriptor})
			if _, err := loader.Load(context.Background(), paths.root, "dataset.json", LoadLimits{20, 1 << 20, 1 << 18, 10}); err == nil {
				t.Fatalf("duplicate %s accepted", test.name)
			}
		})
	}
}

// configureCanonicalSetFixture adds two valid members to every structural expectation set in a chosen order.
func configureCanonicalSetFixture(t *testing.T, paths fixturePaths, reverse bool) string {
	t.Helper()
	apiPath := filepath.Join(paths.root, "cases", "api.json")
	api := []byte(`{"openapi":"3.1.0","info":{"title":"CANARY_OPENAPI_123","version":"1"},"paths":{"/owners":{"get":{"responses":{"200":{"description":"ok"}}}},"/pets":{"get":{"responses":{"200":{"description":"ok"}}}}}}`)
	if err := os.WriteFile(apiPath, api, 0o600); err != nil {
		t.Fatal(err)
	}
	rewriteFixtureCase(t, paths, func(document map[string]any) {
		document["constraints"] = []any{map[string]any{"id": "c1", "description": "list pets"}, map[string]any{"id": "c2", "description": "list owners"}}
		document["descriptors"] = []any{
			map[string]any{"name": "http.request", "class": "http", "description": "descriptor only", "effects": false, "idempotent": true, "contract_id": "http.request/v1"},
			map[string]any{"name": "http.inspect", "class": "http", "description": "descriptor only", "effects": false, "idempotent": true, "contract_id": "http.request/v1"},
		}
		openAPI := document["openapi"].(map[string]any)
		openAPI["sha256"] = digestBytes(api)
		openAPI["selections"] = []any{
			map[string]any{"constraint_id": "c1", "operations": []any{map[string]any{"method": "GET", "path": "/pets"}}},
			map[string]any{"constraint_id": "c2", "operations": []any{map[string]any{"method": "GET", "path": "/owners"}}},
		}
		expected := expectationDocument(document)
		expected["outcome"] = "insufficient_context"
		expected["required_constraints"] = []any{"c1", "c2"}
		expected["allowed_capabilities"] = []any{"http.request", "http.inspect"}
		expected["allowed_identities"] = []any{"user-a", "user-b"}
		expected["allowed_evidence"] = []any{"step/assert", "other/assert"}
		expected["operations"] = []any{
			map[string]any{"constraint_id": "c1", "method": "get", "path": "/pets"},
			map[string]any{"constraint_id": "c2", "method": "GET", "path": "/owners"},
		}
		expected["missing_kinds"] = []any{"question", "evidence"}
		expected["missing_constraints"] = []any{"c1", "c2"}
		if reverse {
			for _, field := range []string{"required_constraints", "allowed_capabilities", "allowed_identities", "allowed_evidence", "operations", "missing_kinds", "missing_constraints"} {
				values := expected[field].([]any)
				values[0], values[1] = values[1], values[0]
			}
		}
	})
	data, _ := os.ReadFile(paths.dataset)
	var manifest map[string]any
	_ = json.Unmarshal(data, &manifest)
	return manifest["cases"].([]any)[0].(map[string]any)["sha256"].(string)
}

// TestLogicalDigestsIgnoreOrderOfEveryStructuralSet separates byte integrity from canonical semantic identity.
func TestLogicalDigestsIgnoreOrderOfEveryStructuralSet(t *testing.T) {
	first := writeEvaluationFixture(t, "plan")
	second := writeEvaluationFixture(t, "plan")
	firstIntegrity := configureCanonicalSetFixture(t, first, false)
	secondIntegrity := configureCanonicalSetFixture(t, second, true)
	if firstIntegrity == secondIntegrity {
		t.Fatal("raw integrity digest did not observe reordered bytes")
	}
	loader, _ := NewLoader(StaticContractRegistry{"http.request/v1": AcceptDescriptor})
	limits := LoadLimits{MaxFiles: 20, MaxTotalBytes: 1 << 20, MaxFileBytes: 1 << 18, MaxCases: 10}
	firstSuite, err := loader.Load(context.Background(), first.root, "dataset.json", limits)
	if err != nil {
		t.Fatal(err)
	}
	secondSuite, err := loader.Load(context.Background(), second.root, "dataset.json", limits)
	if err != nil {
		t.Fatal(err)
	}
	if firstSuite.cases[0].digest != secondSuite.cases[0].digest || firstSuite.Identity().Digest != secondSuite.Identity().Digest {
		t.Fatalf("logical digest depends on set order: first=%#v second=%#v", firstSuite.Identity(), secondSuite.Identity())
	}
	for _, values := range [][]string{firstSuite.cases[0].expectation.RequiredConstraints, firstSuite.cases[0].expectation.AllowedCapabilities, firstSuite.cases[0].expectation.AllowedIdentities, firstSuite.cases[0].expectation.AllowedEvidence} {
		if len(values) != 2 || values[0] > values[1] {
			t.Fatalf("set was not normalized: %#v", values)
		}
	}
	if firstSuite.cases[0].expectation.MissingKinds[0] > firstSuite.cases[0].expectation.MissingKinds[1] || firstSuite.cases[0].expectation.MissingConstraints[0] > firstSuite.cases[0].expectation.MissingConstraints[1] {
		t.Fatal("missing-context sets were not normalized")
	}
}

// evalErrorCategory returns the stable category exposed by one evaluation boundary.
func evalErrorCategory(err error) ErrorCategory {
	var typed *EvalError
	if errors.As(err, &typed) {
		return typed.Category
	}
	return ""
}

// TestMillisecondLimitsRejectOverflowAtCriteriaAndCaseBoundaries covers limit and limit+1 without multiplication wraparound.
func TestMillisecondLimitsRejectOverflowAtCriteriaAndCaseBoundaries(t *testing.T) {
	maximum := int64(math.MaxInt64 / int64(time.Millisecond))
	if duration, ok := durationFromMilliseconds(maximum); !ok || duration != time.Duration(maximum)*time.Millisecond {
		t.Fatal("maximum representable millisecond limit rejected")
	}
	if _, ok := durationFromMilliseconds(maximum + 1); ok {
		t.Fatal("overflowing millisecond limit accepted")
	}

	paths := writeEvaluationFixture(t, "plan")
	criteriaData, _ := os.ReadFile(paths.criteria)
	var criteria map[string]any
	_ = json.Unmarshal(criteriaData, &criteria)
	criteria["run_limits"].(map[string]any)["max_duration_millis"] = maximum
	criteriaData, _ = json.Marshal(criteria)
	_ = os.WriteFile(paths.criteria, criteriaData, 0o600)
	if _, err := LoadCriteria(context.Background(), paths.criteria, 1<<20); err != nil {
		t.Fatalf("maximum criteria duration rejected: %v", err)
	}
	criteria["run_limits"].(map[string]any)["max_duration_millis"] = maximum + 1
	criteriaData, _ = json.Marshal(criteria)
	_ = os.WriteFile(paths.criteria, criteriaData, 0o600)
	if _, err := LoadCriteria(context.Background(), paths.criteria, 1<<20); evalErrorCategory(err) != LimitExceeded {
		t.Fatalf("overflowing criteria duration category=%v err=%v", evalErrorCategory(err), err)
	}

	validCase := writeEvaluationFixture(t, "plan")
	rewriteFixtureCase(t, validCase, func(document map[string]any) {
		document["evaluation"].(map[string]any)["max_duration_millis"] = maximum
	})
	loader, _ := NewLoader(StaticContractRegistry{"http.request/v1": AcceptDescriptor})
	limits := LoadLimits{MaxFiles: 20, MaxTotalBytes: 1 << 20, MaxFileBytes: 1 << 18, MaxCases: 10}
	if _, err := loader.Load(context.Background(), validCase.root, "dataset.json", limits); err != nil {
		t.Fatalf("maximum case duration rejected: %v", err)
	}
	overflowCase := writeEvaluationFixture(t, "plan")
	rewriteFixtureCase(t, overflowCase, func(document map[string]any) {
		document["evaluation"].(map[string]any)["max_duration_millis"] = maximum + 1
	})
	if _, err := loader.Load(context.Background(), overflowCase.root, "dataset.json", limits); evalErrorCategory(err) != LimitExceeded {
		t.Fatalf("overflowing case duration category=%v err=%v", evalErrorCategory(err), err)
	}
}
