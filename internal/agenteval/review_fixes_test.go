package agenteval

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/asierhr/featureProof/internal/agentplanning"
)

// rewriteFixtureCase applies a bounded fixture mutation and refreshes its manifest digest.
func rewriteFixtureCase(t *testing.T, paths fixturePaths, mutate func(map[string]any)) {
	t.Helper()
	casePath := filepath.Join(paths.root, "cases", "valid-list.json")
	data, err := os.ReadFile(casePath)
	if err != nil {
		t.Fatal(err)
	}
	var document map[string]any
	if err := json.Unmarshal(data, &document); err != nil {
		t.Fatal(err)
	}
	mutate(document)
	data, _ = json.Marshal(document)
	if err := os.WriteFile(casePath, data, 0o600); err != nil {
		t.Fatal(err)
	}
	manifestData, _ := os.ReadFile(paths.dataset)
	var manifest map[string]any
	_ = json.Unmarshal(manifestData, &manifest)
	manifest["cases"].([]any)[0].(map[string]any)["sha256"] = digestBytes(data)
	manifestData, _ = json.Marshal(manifest)
	if err := os.WriteFile(paths.dataset, manifestData, 0o600); err != nil {
		t.Fatal(err)
	}
}

// createDirectoryLink creates the platform's local directory-link primitive for containment tests.
func createDirectoryLink(target, link string) error {
	if runtime.GOOS == "windows" {
		return exec.Command("cmd", "/c", "mklink", "/J", link, target).Run()
	}
	return os.Symlink(target, link)
}

// TestLoaderConfinesIntermediateLinksAndUsesVerifiedSnapshot covers REV-1 path and TOCTOU boundaries.
func TestLoaderConfinesIntermediateLinksAndUsesVerifiedSnapshot(t *testing.T) {
	paths := writeEvaluationFixture(t, "plan")
	loader, _ := NewLoader(StaticContractRegistry{"http.request/v1": AcceptDescriptor})
	apiPath := filepath.Join(paths.root, "cases", "api.json")
	loader.afterSnapshot = func() {
		if err := os.WriteFile(apiPath, []byte(`{"openapi":"3.1.0","info":{"title":"changed","version":"1"},"paths":{}}`), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	suite, err := loader.Load(context.Background(), paths.root, "dataset.json", LoadLimits{20, 1 << 20, 1 << 18, 10})
	if err != nil || len(suite.Cases()[0].openAPI.RetainedOperations()) != 1 {
		t.Fatalf("composition did not use verified bytes: %v", err)
	}

	escape := writeEvaluationFixture(t, "plan")
	outside := t.TempDir()
	outsideCases := filepath.Join(outside, "cases")
	if err := os.Mkdir(outsideCases, 0o700); err != nil {
		t.Fatal(err)
	}
	caseBytes, _ := os.ReadFile(filepath.Join(escape.root, "cases", "valid-list.json"))
	apiBytes, _ := os.ReadFile(filepath.Join(escape.root, "cases", "api.json"))
	_ = os.WriteFile(filepath.Join(outsideCases, "valid-list.json"), caseBytes, 0o600)
	_ = os.WriteFile(filepath.Join(outsideCases, "api.json"), apiBytes, 0o600)
	if err := os.RemoveAll(filepath.Join(escape.root, "cases")); err != nil {
		t.Fatal(err)
	}
	if err := createDirectoryLink(outsideCases, filepath.Join(escape.root, "cases")); err != nil {
		t.Fatalf("cannot create intermediate link: %v", err)
	}
	loader.afterSnapshot = nil
	if _, err := loader.Load(context.Background(), escape.root, "dataset.json", LoadLimits{20, 1 << 20, 1 << 18, 10}); err == nil {
		t.Fatal("intermediate link escape accepted")
	}
}

// TestMissingContextExpectationsAreClosedAndCompared covers wrong, missing and extra kind/constraint sets.
func TestMissingContextExpectationsAreClosedAndCompared(t *testing.T) {
	expected := CaseExpectation{MissingKinds: []agentplanning.MissingKind{agentplanning.MissingQuestion}, MissingConstraints: []string{"c1"}}
	valid := []agentplanning.MissingItem{{Kind: agentplanning.MissingQuestion, ConstraintIDs: []string{"c1"}}}
	variants := [][]agentplanning.MissingItem{
		{{Kind: agentplanning.MissingEvidence, ConstraintIDs: []string{"c1"}}},
		{{Kind: agentplanning.MissingQuestion, ConstraintIDs: []string{"c2"}}},
		{{Kind: agentplanning.MissingQuestion, ConstraintIDs: []string{"c1", "c2"}}},
		{},
	}
	if !missingContextMatches(expected, valid) {
		t.Fatal("valid missing context rejected")
	}
	for _, variant := range variants {
		if missingContextMatches(expected, variant) {
			t.Fatalf("invalid missing context accepted: %#v", variant)
		}
	}
	caseValue := Case{id: "missing", digest: "digest", expectation: CaseExpectation{Outcome: "insufficient_context", MissingKinds: expected.MissingKinds, MissingConstraints: expected.MissingConstraints}, evaluation: EvaluationBudget{MaxCalls: 1, MaxTokens: 1, MaxDurationMillis: 1, MaxReplans: 0, MaxWork: 10}}
	wrongResult := agentplanning.Result{Missing: &agentplanning.MissingContext{Items: variants[0]}}
	scored := scoreCase(context.Background(), caseValue, wrongResult, nil, &capturingModel{}, SourceSnapshot{}, SourceSnapshot{})
	if scored.Metrics[MetricInsufficientContext].Numerator != 0 || scored.ErrorPath != "expected.missing_context" {
		t.Fatalf("wrong missing structure received credit: %#v", scored)
	}
	paths := writeEvaluationFixture(t, "insufficient_context")
	rewriteFixtureCase(t, paths, func(document map[string]any) {
		document["expected"].(map[string]any)["missing_kinds"] = []any{"question", "question"}
	})
	loader, _ := NewLoader(StaticContractRegistry{"http.request/v1": AcceptDescriptor})
	if _, err := loader.Load(context.Background(), paths.root, "dataset.json", LoadLimits{20, 1 << 20, 1 << 18, 10}); err == nil {
		t.Fatal("duplicate missing kind accepted")
	}
}

// TestRejectionMatchingIncludesTypedPath covers a rejection differing only in structural path.
func TestRejectionMatchingIncludesTypedPath(t *testing.T) {
	expected := CaseExpectation{Outcome: "rejected", Rejection: &RejectionExpectation{Category: agentplanning.CategorySemanticRejection, Stage: "semantic", Path: "plan"}}
	result := CaseResult{Outcome: "rejected", ErrorCategory: string(agentplanning.CategorySemanticRejection), ErrorStage: "semantic", ErrorPath: "operation", LimitCompliant: true}
	if casePasses(result, expected) {
		t.Fatal("wrong rejection path accepted")
	}
}

// planningModelFunc adapts one controlled Generate function for orchestration tests.
type planningModelFunc func(context.Context, agentplanning.StructuredRequest) (agentplanning.ModelResponse, error)

// Generate delegates to the controlled model function.
func (f planningModelFunc) Generate(ctx context.Context, request agentplanning.StructuredRequest) (agentplanning.ModelResponse, error) {
	return f(ctx, request)
}

// orderedFailureSource fails its first reservation and serves later cases in order.
type orderedFailureSource struct{ reservations, calls int }

// Model records admission order and returns a case-specific valid response after the first failure.
func (s *orderedFailureSource) Model(_ context.Context, c Case) (agentplanning.PlanningModel, error) {
	s.reservations++
	if s.reservations == 1 {
		return nil, errors.New("controlled failure")
	}
	return planningModelFunc(func(context.Context, agentplanning.StructuredRequest) (agentplanning.ModelResponse, error) {
		s.calls++
		return agentplanning.ModelResponse{JSON: validPlanResponse("plan-"+c.id, "/pets")}, nil
	}), nil
}

// Metadata exposes stable synthetic source identifiers.
func (*orderedFailureSource) Metadata() (string, string) { return "fixture", "ordered" }

// Snapshot returns controlled call accounting for limit scoring.
func (s *orderedFailureSource) Snapshot() SourceSnapshot { return SourceSnapshot{Calls: s.calls} }

// TestFailFastFalseContinuesAfterQualifiableFailure covers ordered multicaso continuation.
func TestFailFastFalseContinuesAfterQualifiableFailure(t *testing.T) {
	paths := writeEvaluationFixture(t, "plan")
	suite, criteria, _ := loadFixture(t, paths)
	second := suite.cases[0]
	second.id = "second"
	suite.cases = append(suite.cases, second)
	criteria.FailFast = false
	source := &orderedFailureSource{}
	runner, _ := NewRunner(acceptDefinitions{}, fixedEvalClock{})
	run, err := runner.Run(context.Background(), suite, criteria, source)
	var typed *EvalError
	if !errors.As(err, &typed) || typed.Category != ModelFailure || len(run.Cases) != 2 || source.reservations != 2 || run.Cases[1].Outcome != "plan" {
		t.Fatalf("fail-fast=false did not continue safely: run=%#v err=%v", run, err)
	}
}

// blockingSource returns a model that terminates only through the propagated run deadline.
type blockingSource struct{}

// Model supplies the deadline-observing model.
func (blockingSource) Model(context.Context, Case) (agentplanning.PlanningModel, error) {
	return planningModelFunc(func(ctx context.Context, _ agentplanning.StructuredRequest) (agentplanning.ModelResponse, error) {
		<-ctx.Done()
		return agentplanning.ModelResponse{}, ctx.Err()
	}), nil
}

// Metadata returns safe synthetic source selection.
func (blockingSource) Metadata() (string, string) { return "fixture", "blocking" }

// Snapshot reports zero work around the blocked inference.
func (blockingSource) Snapshot() SourceSnapshot { return SourceSnapshot{} }

// TestRunDeadlineAndEveryLimitDimensionAreEnforced covers REV-5 deadline and accounting dimensions.
func TestRunDeadlineAndEveryLimitDimensionAreEnforced(t *testing.T) {
	paths := writeEvaluationFixture(t, "plan")
	suite, criteria, _ := loadFixture(t, paths)
	criteria.Limits.MaxDurationMillis = 5
	runner, _ := NewRunner(acceptDefinitions{}, fixedEvalClock{})
	if _, err := runner.Run(context.Background(), suite, criteria, blockingSource{}); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("run deadline was not propagated: %v", err)
	}
	base := suite.cases[0]
	base.evaluation = EvaluationBudget{MaxCalls: 1, MaxTokens: 10, MaxDurationMillis: 10, MaxReplans: 0, MaxWork: 1}
	missing := agentplanning.Result{Missing: &agentplanning.MissingContext{Items: []agentplanning.MissingItem{{}, {}}}}
	tests := []SourceSnapshot{{Calls: 2}, {Calls: 1, Tokens: 11}, {Calls: 1, Duration: 11 * time.Millisecond}, {Calls: 1, Replans: 1}, {Calls: 1}}
	for index, after := range tests {
		result := agentplanning.Result{}
		if index == len(tests)-1 {
			result = missing
		}
		scored := scoreCase(context.Background(), base, result, nil, &capturingModel{}, SourceSnapshot{}, after)
		if scored.LimitCompliant {
			t.Fatalf("limit dimension %d accepted: %#v", index, scored)
		}
	}
	decreased := scoreCase(context.Background(), base, agentplanning.Result{}, nil, &capturingModel{}, SourceSnapshot{Tokens: 2}, SourceSnapshot{Tokens: 1})
	if decreased.LimitCompliant {
		t.Fatal("decreasing accounting accepted")
	}
}

// writeBaselineFixture publishes one complete local bundle for baseline mutation tests.
func writeBaselineFixture(t *testing.T, paths fixturePaths) string {
	t.Helper()
	suite, criteria, source := loadFixture(t, paths)
	runner, _ := NewRunner(acceptDefinitions{}, fixedEvalClock{})
	run, err := runner.Run(context.Background(), suite, criteria, source)
	if err != nil {
		t.Fatal(err)
	}
	dir, err := NewWriter(nil).Write(context.Background(), t.TempDir(), "baseline", run, criteria, nil, time.Unix(1, 0), time.Unix(2, 0), nil)
	if err != nil {
		t.Fatal(err)
	}
	return dir
}

// TestManifestPublishesSafeBudgetsWithoutProfile covers auditable non-secret configuration.
func TestManifestPublishesSafeBudgetsWithoutProfile(t *testing.T) {
	paths := writeEvaluationFixture(t, "plan")
	dir := writeBaselineFixture(t, paths)
	data, _ := os.ReadFile(filepath.Join(dir, "manifest.json"))
	var manifest Manifest
	if err := decodeStrict(data, &manifest); err != nil {
		t.Fatal(err)
	}
	config := manifest.SafeConfiguration
	if manifest.Authentication != "not_configured" || config.SchemaVersion != 1 || config.Load.MaxFiles == 0 || config.Run.MaxDurationMillis == 0 || len(config.Cases) != 1 || config.Cases[0].Evaluation.MaxTokens == 0 || config.Cases[0].Generation.MaxValidationWork == 0 {
		t.Fatalf("safe budgets incomplete: %#v", config)
	}
	if bytes.Contains(data, []byte("private-profile")) || strings.Contains(string(data), "auth_profile") {
		t.Fatal("authentication identity entered manifest")
	}
}

// TestBaselineRequiresAuthenticatedArtifactsAndCrossInvariants covers omitted entries and files.
func TestBaselineRequiresAuthenticatedArtifactsAndCrossInvariants(t *testing.T) {
	paths := writeEvaluationFixture(t, "plan")
	dir := writeBaselineFixture(t, paths)
	manifestPath := filepath.Join(dir, "manifest.json")
	data, _ := os.ReadFile(manifestPath)
	var manifest Manifest
	_ = json.Unmarshal(data, &manifest)
	delete(manifest.Files, "metrics.json")
	data, _ = json.Marshal(manifest)
	_ = os.WriteFile(manifestPath, data, 0o600)
	if _, err := LoadBundle(context.Background(), dir, 1<<20); err == nil {
		t.Fatal("unauthenticated metrics accepted")
	}
	dir = writeBaselineFixture(t, paths)
	if err := os.Remove(filepath.Join(dir, "cases.jsonl")); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadBundle(context.Background(), dir, 1<<20); err == nil {
		t.Fatal("missing mandatory cases accepted")
	}
}

// TestAffectedCasesAreComputedPerCategory covers same-cardinality category changes and metric isolation.
func TestAffectedCasesAreComputedPerCategory(t *testing.T) {
	old := []CaseResult{{CaseID: "a", Outcome: "plan", Findings: []Finding{{Category: InventionEndpoint}}, Metrics: map[MetricName]Score{MetricPlanValidity: score(1, 1)}}}
	current := []CaseResult{{CaseID: "a", Outcome: "rejected", Findings: []Finding{{Category: InventionCredential}}, Metrics: map[MetricName]Score{MetricPlanValidity: score(1, 1)}}}
	if len(affectedCases(current, old, string(InventionEndpoint))) != 1 || len(affectedCases(current, old, string(InventionCredential))) != 1 || len(affectedCases(current, old, string(MetricPlanValidity))) != 0 {
		t.Fatal("affected cases were not category-specific")
	}
}

// countingComposer proves incompatible baseline preflight performs no external composition.
type countingComposer struct{ calls int }

// Compose records an unexpected external-boundary attempt.
func (c *countingComposer) Compose(context.Context, CommandOptions) (CandidateSource, error) {
	c.calls++
	return nil, errors.New("must not compose")
}

// TestIncompatibleBaselineStopsBeforeLiveComposition covers compatible-file drift preflight ordering.
func TestIncompatibleBaselineStopsBeforeLiveComposition(t *testing.T) {
	paths := writeEvaluationFixture(t, "plan")
	dir := writeBaselineFixture(t, paths)
	manifestPath := filepath.Join(dir, "manifest.json")
	data, _ := os.ReadFile(manifestPath)
	var manifest Manifest
	_ = json.Unmarshal(data, &manifest)
	manifest.Dataset.Version = "2.0.0"
	data, _ = json.Marshal(manifest)
	_ = os.WriteFile(manifestPath, data, 0o600)
	composer := &countingComposer{}
	args := []string{"-dataset", paths.dataset, "-criteria", paths.criteria, "-output", t.TempDir(), "-source", "live", "-live", "-provider", "openai", "-model", "chosen", "-auth-profile", "private", "-profile-store", "store", "-call-timeout", "1s", "-max-duration", "1s", "-max-calls", "1", "-max-tokens", "10", "-max-replans", "0", "-max-request-bytes", "1024", "-max-header-bytes", "1024", "-max-stream-bytes", "1024", "-max-output-bytes", "1024", "-baseline", dir}
	if code := RunCommand(context.Background(), args, &bytes.Buffer{}, &bytes.Buffer{}, composer); code != 2 || composer.calls != 0 {
		t.Fatalf("incompatible baseline reached composition: code=%d calls=%d", code, composer.calls)
	}
}

// TestHardenedBoundaryHelpersRejectAllInvalidBranches exercises the safe adapters added by the review fixes.
func TestHardenedBoundaryHelpersRejectAllInvalidBranches(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "api.json")
	snapshot := openAPISnapshot{root: root, files: map[string][]byte{path: []byte("{}")}}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := snapshot.Canonicalize(ctx, path); !errors.Is(err, context.Canceled) {
		t.Fatalf("snapshot canonicalization cancellation lost: %v", err)
	}
	if _, err := snapshot.Canonicalize(context.Background(), filepath.Join(root, "..", "outside")); err == nil {
		t.Fatal("snapshot accepted outside path")
	}
	if _, err := snapshot.ReadFile(ctx, path, 10); !errors.Is(err, context.Canceled) {
		t.Fatalf("snapshot read cancellation lost: %v", err)
	}
	if _, err := snapshot.ReadFile(context.Background(), filepath.Join(root, "missing"), 10); err == nil {
		t.Fatal("snapshot served absent bytes")
	}
	if _, err := snapshot.ReadFile(context.Background(), path, 1); err == nil {
		t.Fatal("snapshot ignored document limit")
	}
	if data, err := snapshot.ReadFile(context.Background(), path, 2); err != nil || string(data) != "{}" {
		t.Fatalf("snapshot rejected verified bytes: %v", err)
	}
	if _, ok := checkedIntDelta(1, -1); ok {
		t.Fatal("negative call snapshot accepted")
	}
	if _, ok := checkedInt64Delta(1, 2); ok {
		t.Fatal("decreasing token snapshot accepted")
	}
	if _, ok := checkedDurationDelta(time.Millisecond, -time.Millisecond); ok {
		t.Fatal("negative duration snapshot accepted")
	}
	if value, ok := checkedIntDelta(3, 1); !ok || value != 2 {
		t.Fatal("valid call delta rejected")
	}
	if itoa64(-12) != "-12" {
		t.Fatal("signed metric formatting failed")
	}

	paths := writeEvaluationFixture(t, "plan")
	suite, _, source := loadFixture(t, paths)
	budget := source.SafeBudget()
	if budget.MaxCalls != 1 || budget.MaxTokens != 30 || budget.MaxRequestBytes == 0 {
		t.Fatalf("replay safe budget incomplete: %#v", budget)
	}
	baseline := BundleSnapshot{Dataset: suite.Identity(), CaseDigests: map[string]string{suite.cases[0].id: suite.cases[0].digest}, MetricSchema: "wrong", PlanningProtocol: "agentplanning/v1", CriteriaSchema: "agent-eval-criteria/v1"}
	if ValidateBaselineCompatibility(suite, baseline) == nil {
		t.Fatal("schema drift accepted")
	}
	baseline.MetricSchema = "agent-eval-metrics/v1"
	baseline.CaseDigests = map[string]string{}
	if ValidateBaselineCompatibility(suite, baseline) == nil {
		t.Fatal("case count drift accepted")
	}
	baseline.CaseDigests = map[string]string{suite.cases[0].id: "wrong"}
	if ValidateBaselineCompatibility(suite, baseline) == nil {
		t.Fatal("case digest drift accepted")
	}
}
