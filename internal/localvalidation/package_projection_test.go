package localvalidation

import (
	"context"
	"encoding/json"
	"math"
	"strings"
	"testing"

	"github.com/asierhr/featureProof/internal/evidence"
	"github.com/asierhr/featureProof/internal/planning"
	"github.com/asierhr/featureProof/internal/revisionbinding"
)

func TestSafeAssertionValuesRejectSecretsAndUnsupportedNumbers(t *testing.T) {
	policy, err := evidence.NewPolicy(evidence.Config{MaxBodyBytes: 8, SensitiveJSONPaths: []string{"/secret"}})
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		value      any
		path, want string
		fail       bool
	}{
		{nil, "/public", "present", false}, {true, "/public", "present", false}, {int64(4), "/public", "present", false}, {uint(4), "/public", "present", false}, {float64(4), "/public", "present", false}, {json.Number("4"), "/public", "present", false}, {"CANARY", "/public", "redacted", false}, {map[string]any{"secret": "CANARY"}, "/public", "redacted", false}, {[]any{"CANARY"}, "/public", "redacted", false}, {123, "/secret", "redacted", false}, {math.NaN(), "/public", "", true}, {json.Number("bogus"), "/public", "", true},
	} {
		got, err := safeAssertionValue(planning.AssertionValue{Present: true, Value: tc.value}, tc.path, policy)
		if tc.fail {
			if !IsPackageError(err, "unsafe_projection") {
				t.Fatalf("value=%v err=%v", tc.value, err)
			}
		} else if err != nil || got.State != tc.want {
			t.Fatalf("value=%v got=%+v err=%v", tc.value, got, err)
		}
	}
	if _, err := captureFailures(context.Background(), "c", planning.PlanResult{}, nil, nil); !IsPackageError(err, "unsafe_projection") {
		t.Fatal(err)
	}
}

func TestPackageFailurePreservesConfirmedTruncationState(t *testing.T) {
	ctx := context.Background()
	policy, err := evidence.NewPolicy(evidence.Config{MaxBodyBytes: 5})
	if err != nil {
		t.Fatal(err)
	}
	store, err := evidence.NewFileStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	sanitized, err := evidence.NewSanitizer(policy).Sanitize(ctx, map[string]any{"body": "123456789"})
	if err != nil {
		t.Fatal(err)
	}
	record, err := evidence.NewRecord(evidence.Record{ID: "ev", RunID: "run"}, sanitized, sanitized)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Save(ctx, record); err != nil {
		t.Fatal(err)
	}
	node := planning.AssertionResult{DeclarationID: "a", NodePath: "0", Operator: "equals", Path: "/body", Status: planning.AssertionFailed, Observed: planning.AssertionValue{Present: true, Value: "12345"}, EvidenceIDs: []string{"ev"}}
	result := planning.PlanResult{Status: planning.StepFailed, Steps: []planning.StepResult{{StepID: "step", Status: planning.StepFailed, AssertionResults: []planning.AssertionResult{node}}}}
	failures, err := captureFailures(ctx, "c", result, policy, store)
	if err != nil || len(failures) != 1 || failures[0].Observed.State != "truncated" {
		t.Fatalf("failures=%+v err=%v", failures, err)
	}
	node.EvidenceIDs = []string{"missing"}
	result.Steps[0].AssertionResults = []planning.AssertionResult{node}
	if _, err := captureFailures(ctx, "c", result, policy, store); !IsPackageError(err, "unsafe_projection") {
		t.Fatal(err)
	}
}

func TestPackageProjectionUsesSealedIdentityAndDeterministicStatus(t *testing.T) {
	ctx := context.Background()
	binding := testBindingForRun(t, "run")
	for _, status := range []ResultStatus{ResultPassed, ResultFailed, ResultInconclusive} {
		constraint := ConstraintResult{ConstraintID: "constraint", Status: status, EvidenceIDs: []string{"ev"}}
		if status == ResultFailed {
			constraint.packageFailures = []PackageFailure{{ConstraintID: "constraint", Status: "failed", EvidenceIDs: []string{"ev"}}}
		}
		index, err := buildPackageIndex(ctx, RunSnapshot{ID: "run", Binding: &binding, Status: status, Constraints: []ConstraintResult{constraint}})
		if err != nil || index.Status != status || index.Constraints[0].Status != status {
			t.Fatalf("status=%s index=%+v err=%v", status, index, err)
		}
	}
	changed := binding
	changed.CommitOID = "cccccccccccccccccccccccccccccccccccccccc"
	if _, err := buildPackageIndex(ctx, RunSnapshot{ID: "run", Binding: &changed, Status: ResultInconclusive}); !IsPackageError(err, "binding_integrity") {
		t.Fatal(err)
	}
	plan, err := revisionbinding.NewPlanBinding(ctx, binding, 2, binding.ConstraintsSHA256, "approval_not_required", true)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := buildPackageIndex(ctx, RunSnapshot{ID: "run", Binding: &binding, PlanBindings: []revisionbinding.PlanBinding{plan}, Status: ResultInconclusive}); !IsPackageError(err, "binding_integrity") {
		t.Fatal(err)
	}
	if _, err := buildPackageIndex(ctx, RunSnapshot{ID: "run", Binding: &binding, Status: ResultPassed, Constraints: []ConstraintResult{{ConstraintID: "constraint", Status: ResultFailed, packageFailures: []PackageFailure{{}}}}}); !IsPackageError(err, "invalid_schema") {
		t.Fatal(err)
	}
}

func TestPackageReproductionContainsSelectorsWithoutCredentialsOrTarget(t *testing.T) {
	binding := testBindingForRun(t, "run")
	snapshot := RunSnapshot{ID: "run", Binding: &binding, TargetURL: "https://user:SECRET@example.invalid", Constraints: []ConstraintResult{{ConstraintID: "c", EvidenceIDs: []string{"ev"}, Steps: []planning.StepResult{{StepID: "step"}}}}}
	items, err := buildReproduction(context.Background(), snapshot)
	if err != nil || len(items) != 1 || items[0].CommitOID != binding.CommitOID || items[0].StepIDs[0] != "step" || !items[0].CredentialRequired || items[0].Replayability != "manual_prerequisite" {
		t.Fatalf("items=%+v err=%v", items, err)
	}
	raw, err := json.Marshal(items)
	if err != nil || strings.Contains(string(raw), "SECRET") || strings.Contains(string(raw), "example.invalid") {
		t.Fatalf("unsafe instructions: %s %v", raw, err)
	}
}

func TestPackageCapturesFailedTreeBeforePayloadRemoval(t *testing.T) {
	policy, err := evidence.NewPolicy(evidence.Config{MaxBodyBytes: 16, SensitiveJSONPaths: []string{"/secret"}})
	if err != nil {
		t.Fatal(err)
	}
	leaf := planning.AssertionResult{DeclarationID: "leaf", NodePath: "0.0", Operator: "equals", Path: "/secret", Status: planning.AssertionFailed, Expected: planning.AssertionValue{Present: true, Value: "SECRET"}, Observed: planning.AssertionValue{Present: true, Value: "SECRET"}, EvidenceIDs: []string{"ev"}}
	group := planning.AssertionResult{DeclarationID: "group", NodePath: "0", Operator: "all", Status: planning.AssertionFailed, EvidenceIDs: []string{"ev"}, Children: []planning.AssertionResult{leaf}}
	result := planning.PlanResult{Status: planning.StepFailed, Steps: []planning.StepResult{{StepID: "step", Status: planning.StepFailed, AssertionResults: []planning.AssertionResult{group}}}}
	failures, err := captureFailures(context.Background(), "constraint", result, policy, nil)
	if err != nil || len(failures) != 2 || failures[0].NodePath != "0" || failures[1].NodePath != "0.0" || failures[1].Expected.State != "redacted" {
		t.Fatalf("failures=%+v err=%v", failures, err)
	}
	safe := constraintResult("constraint", result)
	if safe.Steps[0].AssertionResults[0].Children[0].Expected.Present {
		t.Fatal("report projection retained expected")
	}
	for _, tc := range []struct {
		input planning.AssertionValue
		want  string
	}{{planning.AssertionValue{}, "absent"}, {planning.AssertionValue{Present: true, Value: 7}, "present"}, {planning.AssertionValue{Present: true, Value: string(make([]byte, maxValueBytes+1))}, "truncated"}} {
		got, err := safeAssertionValue(tc.input, "/public", policy)
		if err != nil || got.State != tc.want {
			t.Fatalf("got=%+v err=%v", got, err)
		}
	}
	if _, err := safeAssertionValue(planning.AssertionValue{Present: true, Value: make(chan int)}, "/public", policy); !IsPackageError(err, "unsafe_projection") {
		t.Fatal(err)
	}
}
