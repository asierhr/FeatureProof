package functionalflow

import (
	"context"
	"encoding/json"
	"errors"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/asierhr/featureProof/internal/agentplanning"
	"github.com/asierhr/featureProof/internal/fixture"
	"github.com/asierhr/featureProof/internal/planning"
)

// TestGeneratedSnapshotCoversCanonicalInputUnion proves recovered generated plans re-enter strict normalization.
func TestGeneratedSnapshotCoversCanonicalInputUnion(t *testing.T) {
	field := "value"
	snapshot := agentplanning.PreparedSnapshot{ID: "generated", Checks: []agentplanning.CandidateCheck{{
		ID: "check", ConstraintID: "c1", Capability: "http.request", Operation: &agentplanning.OperationReference{Method: "GET", Path: "/items/{id}"}, IdentityID: "reader",
		Steps: []planning.PlanStep{
			{ID: "first", Tool: "http.request", Input: map[string]planning.InputValue{
				"literal": {Literal: &planning.LiteralValue{Value: "x"}},
				"object":  {Object: map[string]planning.InputValue{"n": {Literal: &planning.LiteralValue{Value: true}}}},
				"array":   {Array: []planning.InputValue{{Literal: &planning.LiteralValue{Value: 1}}}},
			}, Assertions: []planning.AssertionDeclaration{assertionDecl("a1")}, DependsOn: []string{}},
			{ID: "second", Tool: "http.request", Input: map[string]planning.InputValue{"ref": {Reference: &planning.OutputReference{StepID: "first", Path: []planning.PathSegment{{Field: &field}}, ValueType: planning.ValueString}}}, Assertions: []planning.AssertionDeclaration{assertionDecl("a2")}, DependsOn: []string{"first"}},
		},
	}}}
	flow, err := NormalizeGeneratedSnapshot(snapshot, GeneratedAuthority{ContextDigest: digestValue("context"), TargetID: "preview", ContractDigest: digestValue("contract"), Effects: map[string]bool{}})
	if err != nil {
		t.Fatal(err)
	}
	if flow.Source != SourceGenerated || len(flow.Steps) != 2 || flow.Steps[1].Input["ref"].Reference.Step.StepID != "check-first" {
		t.Fatalf("%#v", flow)
	}
	if _, err := Validate(flow, testLimits(), testAuthority()); err != nil {
		t.Logf("flow=%#v step=%#v", flow, flow.Steps[0])
		t.Fatal(err)
	}
	duplicate := snapshot
	duplicate.Checks[0].Steps[1].ID = "first"
	if _, err := NormalizeGeneratedSnapshot(duplicate, GeneratedAuthority{ContextDigest: "c", TargetID: "t", ContractDigest: "d"}); err == nil {
		t.Fatal("accepted duplicate generated step")
	}
	if _, err := NormalizeGeneratedSnapshot(agentplanning.PreparedSnapshot{}, GeneratedAuthority{}); err == nil {
		t.Fatal("accepted missing authority")
	}
}

// TestValidationRejectsEveryClosedUnionAndLimitBoundary exercises fail-closed shape and trusted bounds.
func TestValidationRejectsEveryClosedUnionAndLimitBoundary(t *testing.T) {
	cases := []func(*Flow){
		func(f *Flow) { f.Version = "future" }, func(f *Flow) { f.Source = "model" }, func(f *Flow) { f.Steps[0].ID = "" }, func(f *Flow) { f.Steps[1].ID = "before" },
		func(f *Flow) { f.Steps[0].Kind = "shell" }, func(f *Flow) { f.Steps[0].ConstraintID = "unknown" }, func(f *Flow) { f.Steps[0].IdentityID = "" }, func(f *Flow) { f.Steps[0].Operation.Method = "get" },
		func(f *Flow) { f.Steps[0].DependsOn = nil }, func(f *Flow) { f.Steps[0].Input = nil }, func(f *Flow) { f.Steps[0].Assertions = nil }, func(f *Flow) { f.Steps[0].RequiredEvidence = []string{"missing"} },
		func(f *Flow) { f.Steps[0].Input["x"] = InputValue{} }, func(f *Flow) { value := literalNode("x"); value.Array = []InputValue{}; f.Steps[0].Input["x"] = value }, func(f *Flow) { f.Steps[0].Input["x"] = InputValue{Reference: &Reference{}} },
	}
	for i, mutate := range cases {
		flow := baseFlow()
		mutate(&flow)
		if _, err := Validate(flow, testLimits(), testAuthority()); err == nil {
			t.Fatalf("case %d accepted", i)
		}
	}
	limits := testLimits()
	limits.MaxSteps = 1
	if _, err := Validate(baseFlow(), limits, testAuthority()); err == nil {
		t.Fatal("step limit accepted")
	}
	limits = testLimits()
	limits.MaxDepth = 1
	flow := baseFlow()
	flow.Steps[0].Input["deep"] = InputValue{Object: map[string]InputValue{"child": literalNode(true)}}
	if _, err := Validate(flow, limits, testAuthority()); err == nil {
		t.Fatal("depth accepted")
	}
	limits = testLimits()
	limits.MaxAssertions = 1
	if _, err := Validate(baseFlow(), limits, testAuthority()); err == nil {
		t.Fatal("assertion limit accepted")
	}
	limits = testLimits()
	limits.MaxDocumentBytes = 0
	if _, err := Validate(baseFlow(), limits, testAuthority()); err == nil {
		t.Fatal("invalid trusted limits accepted")
	}
}

// TestResolverCoversNestedTypesAndRejectsCorruption exercises all exact JSON type and path branches.
func TestResolverCoversNestedTypesAndRejectsCorruption(t *testing.T) {
	values := map[string]any{"s": "x", "b": true, "n": json.Number("1"), "o": map[string]any{"x": "y"}, "a": []any{"z"}}
	for key, value := range values {
		if !matchesValueType(value, map[string]planning.ValueType{"s": planning.ValueString, "b": planning.ValueBoolean, "n": planning.ValueNumber, "o": planning.ValueObject, "a": planning.ValueArray}[key]) {
			t.Fatalf("type %s rejected", key)
		}
	}
	if matchesValueType(nil, planning.ValueString) || matchesValueType("1", planning.ValueNumber) {
		t.Fatal("coercion accepted")
	}
	store := &memoryEvidence{values: map[string]Evidence{}}
	evidence := SealEvidence(Evidence{ID: "e", RunID: "run", Revision: 1, StepID: "parent", Output: map[string]any{"nested": []any{map[string]any{"value": "ok"}}}})
	store.values["e"] = evidence
	fieldNested, indexZero, fieldValue := "nested", 0, "value"
	step := Step{Input: map[string]InputValue{"object": {Object: map[string]InputValue{"array": {Array: []InputValue{{Reference: &Reference{Step: &StepReference{StepID: "parent", Path: []planning.PathSegment{{Field: &fieldNested}, {Index: &indexZero}, {Field: &fieldValue}}, ValueType: planning.ValueString}}}}}}}}}
	resolver := InputResolver{Evidence: store, MaxBytes: 1024}
	resolved, err := resolver.Resolve(context.Background(), "run", 1, step, map[string]StepRecord{"parent": {StepID: "parent", Status: planning.StepPassed, EvidenceID: "e"}})
	if err != nil || resolved["object"] == nil {
		t.Fatalf("resolved=%#v err=%v", resolved, err)
	}
	store.values["e"] = Evidence{ID: "e", RunID: "run", Revision: 1, StepID: "parent", Terminal: true, Digest: "corrupt", Output: evidence.Output}
	if _, err := resolver.Resolve(context.Background(), "run", 1, step, map[string]StepRecord{"parent": {StepID: "parent", Status: planning.StepPassed, EvidenceID: "e"}}); err == nil {
		t.Fatal("corrupt evidence accepted")
	}
	tiny := resolver
	tiny.MaxBytes = 1
	tinyStep := Step{Input: map[string]InputValue{"x": literalNode("large")}}
	if _, err := tiny.Resolve(context.Background(), "run", 1, tinyStep, nil); err == nil {
		t.Fatal("resolved-byte limit accepted")
	}
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := resolver.Resolve(cancelled, "run", 1, tinyStep, nil); err == nil {
		t.Fatal("cancel ignored")
	}
}

// TestLedgerDeduplicationAndConflictAreDeterministic covers idempotent redelivery and contradictory replay.
func TestLedgerDeduplicationAndConflictAreDeterministic(t *testing.T) {
	entry := LedgerEntry{Revision: 1, StepID: "s", ConstraintID: "c", Result: planning.AssertionResult{DeclarationID: "a", NodePath: "root", Status: planning.AssertionPassed}, EvidenceIDs: []string{"e", "e"}}
	ledger, err := (Ledger{}).Append(entry)
	if err != nil {
		t.Fatal(err)
	}
	same, err := ledger.Append(entry)
	if err != nil || len(same.Entries()) != 1 {
		t.Fatalf("entries=%d err=%v", len(same.Entries()), err)
	}
	entry.Result.Status = planning.AssertionFailed
	if _, err := ledger.Append(entry); err == nil {
		t.Fatal("contradictory replay accepted")
	}
	if _, err := (Ledger{}).Append(LedgerEntry{}); err == nil {
		t.Fatal("empty ledger entry accepted")
	}
}

// TestSafeProjectionAndPackageTamperingFailClosed covers valid projection and event/package digest changes.
func TestSafeProjectionAndPackageTamperingFailClosed(t *testing.T) {
	flow := baseFlow()
	validated, _ := Validate(flow, testLimits(), testAuthority())
	artifact, _ := Bind(validated, testBindingAuthority(flow))
	steps := []StepRecord{{RunID: "run", Revision: 1, StepID: "before", Status: planning.StepInconclusive}, {RunID: "run", Revision: 1, StepID: "after", Status: planning.StepBlocked}}
	constraints, outcome := (Ledger{}).Aggregate(flow, steps, false)
	comparison := ComparisonResult{StepID: "after", ID: "changed", Status: planning.AssertionInconclusive}
	ledger := []LedgerEntry{{Revision: 1, StepID: "after", ConstraintID: "c1", Result: planning.AssertionResult{DeclarationID: "changed", NodePath: "changed", Status: planning.AssertionInconclusive}}}
	result := Result{FlowID: flow.ID, Revision: 1, Steps: steps, Ledger: ledger, Comparisons: []ComparisonResult{comparison}, Constraints: constraints, Outcome: outcome, ArtifactDigest: artifact.ArtifactDigest}
	events := BuildLifecycleEvents("run", artifact, result)
	pkg, err := BuildPackage("run", artifact, []EvidenceRecord{}, events, result, fixture.FixtureSummary{}, fixture.CleanupReport{Status: fixture.CleanupPartial})
	if err != nil {
		t.Fatal(err)
	}
	primary, cleanup, err := SafeProjection(pkg)
	if err != nil || primary.Outcome != OutcomeInconclusive || cleanup.Status != fixture.CleanupPartial {
		t.Fatalf("primary=%#v cleanup=%#v err=%v", primary, cleanup, err)
	}
	pkg.Events[0].Type = "forked"
	if err := VerifyPackage(pkg); err == nil {
		t.Fatal("tampered event accepted")
	}
	if _, _, err := SafeProjection(pkg); err == nil {
		t.Fatal("tampered projection accepted")
	}
}

// TestRevisionBoundsAndArtifactShapeFailClosed covers invalid initial, exhausted, and shortened revisions.
func TestRevisionBoundsAndArtifactShapeFailClosed(t *testing.T) {
	if _, err := NewRevisionSet(Artifact{}, 0); err == nil {
		t.Fatal("invalid revision set accepted")
	}
	flow := baseFlow()
	validated, _ := Validate(flow, testLimits(), testAuthority())
	artifact, _ := Bind(validated, testBindingAuthority(flow))
	set, _ := NewRevisionSet(artifact, 1)
	next := cloneArtifact(artifact)
	next.Revision = 2
	next.SemanticDigest = "new"
	next.ArtifactDigest = artifactDigest(next)
	if _, _, err := set.Add(next); err == nil {
		t.Fatal("revision budget ignored")
	}
	corrupt := cloneArtifact(artifact)
	corrupt.Steps = nil
	corrupt.ArtifactDigest = artifactDigest(corrupt)
	if err := ValidateArtifact(corrupt); err == nil {
		t.Fatal("empty artifact accepted")
	}
}

// errorTool always fails after I/O to exercise conservative non-idempotent action handling.
type errorTool struct{ calls int }

// Execute reports a transient uncertain effect without yielding evidence.
func (e *errorTool) Execute(_ context.Context, _ ExecutionRequest) (ExecutionResponse, error) {
	e.calls++
	return ExecutionResponse{CrossedNetwork: true, Transient: true}, errors.New("raw external canary")
}

// TestUncertainNonIdempotentActionDoesNotRetry proves unsafe transient effects stop after one attempt.
func TestUncertainNonIdempotentActionDoesNotRetry(t *testing.T) {
	flow, authority := actionFlow()
	authority.Operations[1].Idempotent = false
	authority.Operations[1].ReconciliationSafe = false
	validated, _ := Validate(flow, testLimits(), testAuthority())
	artifact, _ := Bind(validated, authority)
	state := &memoryIntents{claims: map[string]string{}, intents: map[string]Intent{}, terminals: map[string]StepRecord{}}
	tool := &scriptedTool{outputs: map[string]any{"before": map[string]any{"ok": true, "value": "old"}}, transient: map[string]int{}}
	failing := &errorTool{}
	multiplex := toolByStep{normal: tool, action: failing}
	executor := &Executor{Authority: authority, Clock: fixedFlowClock{now: time.Unix(100, 0)}, Policy: allowPolicy{}, Revalidator: allowBinding{}, Tool: multiplex, Evidence: &memoryEvidence{values: map[string]Evidence{}}, Assertions: deterministicAssertions{}, State: state, Keys: stableKeys{}, Waiter: immediateWaiter{}, Config: ExecutionConfig{Owner: "o", MaxCalls: 10, MaxRetries: 2, MaxWork: 100, StepTimeout: time.Second, RunTimeout: time.Second, ConfirmationTimeout: time.Second}}
	result, err := executor.Execute(context.Background(), "run", validated, artifact)
	if err != nil || failing.calls != 1 || result.Outcome != OutcomeInconclusive {
		t.Fatalf("calls=%d outcome=%s err=%v", failing.calls, result.Outcome, err)
	}
}

// TestObserveTransportFailureIsOperationallyInconclusive proves a crossed read boundary cannot be promoted to an internal integrity error.
func TestObserveTransportFailureIsOperationallyInconclusive(t *testing.T) {
	tool := &scriptedTool{outputs: map[string]any{}, transient: map[string]int{"before": 3}}
	executor, validated, artifact, _, _ := executorFixture(t, tool, allowPolicy{})
	result, err := executor.Execute(context.Background(), "observe-failure", validated, artifact)
	if err != nil || result.Outcome != OutcomeInconclusive || len(result.Steps) == 0 || result.Steps[0].Category != CategoryUncertain || tool.callCount("before") != 3 {
		t.Fatalf("result=%#v calls=%d err=%v", result, tool.callCount("before"), err)
	}
}

// toolByStep routes only the action to an uncertain adapter while observations remain deterministic.
type toolByStep struct {
	normal Tool
	action Tool
}

// TestFunctionalFlowSystemicCommentsAndVerificationGates enforces responsibility comments on every declared type and function.
func TestFunctionalFlowSystemicCommentsAndVerificationGates(t *testing.T) {
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".go" {
			continue
		}
		file, err := parser.ParseFile(token.NewFileSet(), entry.Name(), nil, parser.ParseComments)
		if err != nil {
			t.Fatal(err)
		}
		for _, declaration := range file.Decls {
			switch node := declaration.(type) {
			case *ast.FuncDecl:
				if node.Doc == nil || len(node.Doc.List) == 0 {
					t.Errorf("%s: function %s lacks systemic comment", entry.Name(), node.Name.Name)
				}
			case *ast.GenDecl:
				if node.Tok == token.CONST {
					for _, spec := range node.Specs {
						value := spec.(*ast.ValueSpec)
						if value.Doc == nil && value.Comment == nil && node.Doc == nil {
							t.Errorf("%s: constant %s lacks systemic comment", entry.Name(), value.Names[0].Name)
						}
					}
				} else if node.Tok == token.TYPE || node.Tok == token.VAR {
					if node.Doc == nil || len(node.Doc.List) == 0 {
						t.Errorf("%s: %s declaration lacks systemic comment", entry.Name(), node.Tok)
					}
				}
			}
		}
	}
}

// Execute delegates by stable step ID without adding authority.
func (m toolByStep) Execute(ctx context.Context, request ExecutionRequest) (ExecutionResponse, error) {
	if request.StepID == "act" {
		return m.action.Execute(ctx, request)
	}
	return m.normal.Execute(ctx, request)
}
