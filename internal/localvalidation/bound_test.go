package localvalidation

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/asierhr/featureProof/internal/evidence"
	"github.com/asierhr/featureProof/internal/harness"
	"github.com/asierhr/featureProof/internal/planmanifest"
	"github.com/asierhr/featureProof/internal/planning"
	"github.com/asierhr/featureProof/internal/revisionbinding"
	"github.com/asierhr/featureProof/internal/specification"
)

// fakeRevision keeps source resolution separate from local Git in runner tests.
type fakeRevision struct{ value revisionbinding.Revision }

// resolverFunc scripts revision observations at admission and later rechecks.
type resolverFunc func(context.Context, string, string) (revisionbinding.Revision, error)

func (f resolverFunc) Resolve(ctx context.Context, repo, ref string) (revisionbinding.Revision, error) {
	return f(ctx, repo, ref)
}

func (f fakeRevision) Resolve(context.Context, string, string) (revisionbinding.Revision, error) {
	return f.value, nil
}

// fakeAttestation represents a configured trusted deployment source.
type fakeAttestation struct{ value revisionbinding.Attestation }

func (f fakeAttestation) Attest(context.Context, string) (revisionbinding.Attestation, error) {
	return f.value, nil
}

// fixtureProjector classifies only the known public fixture and rejects any
// extra literal or payload field instead of hashing its bytes.
type fixtureProjector struct{}

// projectorFunc scripts safe semantic snapshots and cancellation in tests.
type projectorFunc func(context.Context, specification.Specification, planmanifest.Manifest, []planning.ValidatedPlan) (SafeProjections, error)

func (f projectorFunc) Project(ctx context.Context, spec specification.Specification, manifest planmanifest.Manifest, plans []planning.ValidatedPlan) (SafeProjections, error) {
	return f(ctx, spec, manifest, plans)
}

// legacyFixtureProjector models the historic X-Secret test header as a
// configured credential reference, without hashing the changing secret bytes.
type legacyFixtureProjector struct{}

func (legacyFixtureProjector) Project(ctx context.Context, spec specification.Specification, manifest planmanifest.Manifest, validated []planning.ValidatedPlan) (SafeProjections, error) {
	copyManifest := manifest
	copyManifest.Plans = append([]planning.ValidationPlan(nil), manifest.Plans...)
	if len(copyManifest.Plans) != 1 || len(copyManifest.Plans[0].Steps) != 1 {
		return SafeProjections{}, fmt.Errorf("unexpected fixture")
	}
	plan := copyManifest.Plans[0]
	plan.Steps = append([]planning.PlanStep(nil), plan.Steps...)
	step := plan.Steps[0]
	if len(step.Input) != 3 || step.Input["headers"].Object == nil {
		return SafeProjections{}, fmt.Errorf("missing credential reference")
	}
	step.Input = map[string]planning.InputValue{"method": step.Input["method"], "path": step.Input["path"]}
	plan.Steps[0] = step
	copyManifest.Plans[0] = plan
	projection, err := (fixtureProjector{}).Project(ctx, spec, copyManifest, validated)
	if err != nil {
		return SafeProjections{}, err
	}
	projection.Plans[0] = revisionbinding.SafeArray(projection.Plans[0], revisionbinding.SafeObject(map[string]revisionbinding.SafeValue{"auth_ref": revisionbinding.CredentialReference("test-secret")}))
	return projection, nil
}

func (fixtureProjector) Project(_ context.Context, spec specification.Specification, manifest planmanifest.Manifest, _ []planning.ValidatedPlan) (SafeProjections, error) {
	if len(spec.Constraints) != 1 || len(manifest.Plans) != 1 || len(manifest.Plans[0].Steps) != 1 || spec.Feature.Description != "local" || spec.Constraints[0].Description != "responds" {
		return SafeProjections{}, fmt.Errorf("unclassified specification")
	}
	step := manifest.Plans[0].Steps[0]
	if len(step.Input) != 2 || step.Input["method"].Literal == nil || step.Input["path"].Literal == nil || step.Input["method"].Literal.Value != "GET" || step.Input["path"].Literal.Value != "/" || len(step.Assertions) != 1 {
		return SafeProjections{}, fmt.Errorf("unclassified plan")
	}
	if step.Assertions[0].Definition["operator"] != "eq" || step.Assertions[0].Definition["path"] != "/status" {
		return SafeProjections{}, fmt.Errorf("unclassified assertion")
	}
	var expected int64
	switch value := step.Assertions[0].Definition["expected"].(type) {
	case json.Number:
		expected, _ = value.Int64()
	case int:
		expected = int64(value)
	default:
		return SafeProjections{}, fmt.Errorf("unclassified expected value")
	}
	return SafeProjections{
		Constraints:   revisionbinding.SafeObject(map[string]revisionbinding.SafeValue{"feature_id": revisionbinding.PublicString(spec.Feature.ID), "description": revisionbinding.PublicString(spec.Feature.Description), "constraint_id": revisionbinding.PublicString(spec.Constraints[0].ID), "constraint_description": revisionbinding.PublicString(spec.Constraints[0].Description)}),
		Contract:      revisionbinding.SafeObject(map[string]revisionbinding.SafeValue{"target_id": revisionbinding.PublicString(spec.Target.ID), "target_url": revisionbinding.PublicString(spec.Target.BaseURL)}),
		Configuration: revisionbinding.SafeObject(map[string]revisionbinding.SafeValue{"max_steps": revisionbinding.PublicInt(int64(spec.Limits.MaxSteps)), "max_tool_calls": revisionbinding.PublicInt(int64(spec.Limits.MaxToolCalls)), "max_retries": revisionbinding.PublicInt(int64(spec.Limits.MaxRetries))}),
		Plans:         []revisionbinding.SafeValue{revisionbinding.SafeObject(map[string]revisionbinding.SafeValue{"plan_id": revisionbinding.PublicString(manifest.Plans[0].ID), "step_id": revisionbinding.PublicString(step.ID), "tool": revisionbinding.PublicString(step.Tool), "method": revisionbinding.PublicString("GET"), "path": revisionbinding.PublicString("/"), "expected_status": revisionbinding.PublicInt(expected)})},
	}, nil
}

// testBinding supplies a complete verified identity without any external source.
func testBinding(t *testing.T) revisionbinding.Binding {
	return testBindingForRun(t, "run")
}

// testBindingForRun creates one isolated verified run identity for publisher fixtures.
func testBindingForRun(t *testing.T, runID string) revisionbinding.Binding {
	t.Helper()
	digest := strings.Repeat("a", 64)
	revision := revisionbinding.Revision{RepositoryID: "repo", CommitOID: strings.Repeat("b", 40)}
	attestation := revisionbinding.Attestation{EnvironmentID: "preview", RepositoryID: "repo", CommitOID: revision.CommitOID, Reference: "attestation", ValidUntil: time.Now().Add(time.Hour), Verified: true}
	binding, err := revisionbinding.NewBinding(context.Background(), runID, revision, attestation, digest, digest, digest)
	if err != nil {
		t.Fatal(err)
	}
	return binding
}

// publishFixture gives older publisher tests the required valid binding envelope.
func publishFixture(t *testing.T, publisher *Publisher, ctx context.Context, snapshot RunSnapshot, manifest planmanifest.Manifest, store evidence.Store) error {
	t.Helper()
	if snapshot.ID == "" {
		snapshot.ID = "run"
	}
	binding := testBindingForRun(t, snapshot.ID)
	snapshot.Binding = &binding
	if snapshot.Status == "" {
		snapshot.Status = ResultInconclusive
	}
	plan, err := revisionbinding.NewPlanBinding(ctx, binding, 1, strings.Repeat("d", 64), "approval_not_required", true)
	if err != nil {
		t.Fatal(err)
	}
	snapshot.PlanBindings = []revisionbinding.PlanBinding{plan}
	terminal := harness.EventRunCompleted
	if snapshot.Status == ResultInconclusive {
		terminal = harness.EventRunInconclusive
	} else if snapshot.Status == ResultError {
		terminal = harness.EventRunFailed
	} else if snapshot.Status == ResultCancelled {
		terminal = harness.EventRunCancelled
	}
	events := []harness.Event{
		{Sequence: 1, Type: harness.EventRunCreated, RunID: snapshot.ID, BindingSHA256: binding.BindingSHA256},
		{Sequence: 2, Type: harness.EventBindingConfirmed, RunID: snapshot.ID, BindingSHA256: binding.BindingSHA256},
		{Sequence: 3, Type: harness.EventPlanBindingConfirmed, RunID: snapshot.ID, BindingSHA256: binding.BindingSHA256, PlanSHA256: plan.PlanSHA256},
		{Sequence: 4, Type: terminal, RunID: snapshot.ID, BindingSHA256: binding.BindingSHA256, Status: string(snapshot.Status)},
	}
	return publisher.Publish(ctx, snapshot, events, manifest, store)
}

// TestEventsAndEvidenceShareBinding checks automatic metadata propagation and origin enforcement.
func TestEventsAndEvidenceShareBinding(t *testing.T) {
	binding := testBinding(t)
	journal := boundJournal{inner: harness.NewMemoryJournal(), runID: binding.RunID, digest: binding.BindingSHA256}
	if err := journal.Append(context.Background(), harness.Event{RunID: "run", Type: harness.EventRunCreated}); err != nil {
		t.Fatal(err)
	}
	events, err := journal.Events(context.Background(), "run")
	if err != nil || len(events) != 1 || events[0].BindingSHA256 != binding.BindingSHA256 {
		t.Fatalf("events=%+v err=%v", events, err)
	}
	if err := journal.Append(context.Background(), harness.Event{RunID: "foreign"}); err == nil {
		t.Fatal("foreign event accepted")
	}
	inner, err := evidence.NewFileStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	store := boundEvidence{inner: inner, runID: "run", digest: binding.BindingSHA256}
	if _, err := store.Get(context.Background(), "missing"); err == nil {
		t.Fatal("missing evidence accepted")
	}
}

// TestForeignBindingPreventsBundleCommit rejects a mismatched journal before final rename.
func TestForeignBindingPreventsBundleCommit(t *testing.T) {
	binding := testBinding(t)
	root := t.TempDir()
	publisher, err := NewPublisher(root, "run")
	if err != nil {
		t.Fatal(err)
	}
	snapshot := RunSnapshot{ID: "run", Binding: &binding}
	events := []harness.Event{
		{Sequence: 1, Type: harness.EventRunCreated, RunID: "run", BindingSHA256: binding.BindingSHA256},
		{Sequence: 2, Type: harness.EventBindingConfirmed, RunID: "run", BindingSHA256: strings.Repeat("c", 64)},
	}
	err = publisher.Publish(context.Background(), snapshot, events, planmanifest.Manifest{}, nil)
	if !revisionbinding.IsCategory(err, revisionbinding.BindingIntegrity) {
		t.Fatalf("foreign event: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, ".featureproof", "runs", "run")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("bundle committed: %v", err)
	}
}

// TestFunctionalEventRequiresConfirmedPlanBinding rejects a journal whose
// valid batch digest was confirmed only after tool work had already started.
func TestFunctionalEventRequiresConfirmedPlanBinding(t *testing.T) {
	binding := testBinding(t)
	plan, err := revisionbinding.NewPlanBinding(context.Background(), binding, 1, strings.Repeat("d", 64), "approval_not_required", true)
	if err != nil {
		t.Fatal(err)
	}
	events := []harness.Event{
		{Sequence: 1, Type: harness.EventRunCreated, RunID: binding.RunID, BindingSHA256: binding.BindingSHA256},
		{Sequence: 2, Type: harness.EventBindingConfirmed, RunID: binding.RunID, BindingSHA256: binding.BindingSHA256},
		{Sequence: 3, Type: harness.EventExecutionStarted, RunID: binding.RunID, BindingSHA256: binding.BindingSHA256},
		{Sequence: 4, Type: harness.EventPlanBindingConfirmed, RunID: binding.RunID, BindingSHA256: binding.BindingSHA256, PlanSHA256: plan.PlanSHA256},
		{Sequence: 5, Type: harness.EventRunCompleted, RunID: binding.RunID, BindingSHA256: binding.BindingSHA256, Status: string(ResultPassed)},
	}
	if err := verifyBoundSequence(context.Background(), binding, []revisionbinding.PlanBinding{plan}, events, ResultPassed); !revisionbinding.IsCategory(err, revisionbinding.BindingIntegrity) {
		t.Fatalf("unbound functional prefix accepted: %v", err)
	}
	events[2], events[3] = events[3], events[2]
	events[2].Sequence, events[3].Sequence = 3, 4
	if err := verifyBoundSequence(context.Background(), binding, []revisionbinding.PlanBinding{plan}, events, ResultPassed); err != nil {
		t.Fatalf("valid batch prefix rejected: %v", err)
	}
}

// TestApprovedPlanBindingRetainsItsOpaqueReferenceDuringPublication proves canonical journal verification accepts the exact consumed approval grant.
func TestApprovedPlanBindingRetainsItsOpaqueReferenceDuringPublication(t *testing.T) {
	binding := testBinding(t)
	plan, err := revisionbinding.NewApprovedPlanBinding(context.Background(), binding, 1, strings.Repeat("d", 64), "approval:0123456789abcdef0123456789abcdef")
	if err != nil {
		t.Fatal(err)
	}
	events := []harness.Event{
		{Sequence: 1, Type: harness.EventRunCreated, RunID: binding.RunID, BindingSHA256: binding.BindingSHA256},
		{Sequence: 2, Type: harness.EventBindingConfirmed, RunID: binding.RunID, BindingSHA256: binding.BindingSHA256},
		{Sequence: 3, Type: harness.EventPlanBindingConfirmed, RunID: binding.RunID, BindingSHA256: binding.BindingSHA256, PlanSHA256: plan.PlanSHA256},
		{Sequence: 4, Type: harness.EventRunCompleted, RunID: binding.RunID, BindingSHA256: binding.BindingSHA256, Status: string(ResultPassed)},
	}
	if err = verifyBoundSequence(context.Background(), binding, []revisionbinding.PlanBinding{plan}, events, ResultPassed); err != nil {
		t.Fatalf("approved binding rejected: %v", err)
	}
}

// TestEventReferencedForeignEvidencePreventsBundleCommit verifies records
// referenced only by journal events, including intermediate attempts.
func TestEventReferencedForeignEvidencePreventsBundleCommit(t *testing.T) {
	binding := testBinding(t)
	root := t.TempDir()
	publisher, err := NewPublisher(root, "run")
	if err != nil {
		t.Fatal(err)
	}
	store, err := publisher.EvidenceStore()
	if err != nil {
		t.Fatal(err)
	}
	policy, err := evidence.NewPolicy(evidence.Config{MaxBodyBytes: 1024})
	if err != nil {
		t.Fatal(err)
	}
	sanitizer := evidence.NewSanitizer(policy)
	clean, err := sanitizer.Sanitize(context.Background(), map[string]any{"status": 200})
	if err != nil {
		t.Fatal(err)
	}
	record, err := evidence.NewRecord(evidence.Record{ID: "ev", RunID: "run"}, clean, clean)
	if err != nil {
		t.Fatal(err)
	}
	record, err = evidence.BindRecord(record, "run", strings.Repeat("c", 64))
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Save(context.Background(), record); err != nil {
		t.Fatal(err)
	}
	plan, err := revisionbinding.NewPlanBinding(context.Background(), binding, 1, strings.Repeat("d", 64), "approval_not_required", true)
	if err != nil {
		t.Fatal(err)
	}
	events := []harness.Event{
		{Sequence: 1, Type: harness.EventRunCreated, RunID: "run", BindingSHA256: binding.BindingSHA256},
		{Sequence: 2, Type: harness.EventBindingConfirmed, RunID: "run", BindingSHA256: binding.BindingSHA256},
		{Sequence: 3, Type: harness.EventPlanBindingConfirmed, RunID: "run", BindingSHA256: binding.BindingSHA256, PlanSHA256: plan.PlanSHA256},
		{Sequence: 4, Type: harness.EventEvidencePersisted, RunID: "run", BindingSHA256: binding.BindingSHA256, EvidenceIDs: []string{"ev"}},
		{Sequence: 5, Type: harness.EventRunInconclusive, RunID: "run", BindingSHA256: binding.BindingSHA256, Status: string(ResultInconclusive)},
	}
	err = publisher.Publish(context.Background(), RunSnapshot{ID: "run", Binding: &binding, PlanBindings: []revisionbinding.PlanBinding{plan}, Status: ResultInconclusive}, events, planmanifest.Manifest{}, store)
	if !revisionbinding.IsCategory(err, revisionbinding.BindingIntegrity) {
		t.Fatalf("foreign evidence accepted: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, ".featureproof", "runs", "run")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("bundle committed: %v", err)
	}
}

// TestPublishedArtifactsShareBinding verifies every terminal view and one
// correction run while preserving the first committed bytes.
func TestPublishedArtifactsShareBinding(t *testing.T) {
	root := t.TempDir()
	first := testBinding(t)
	publish := func(binding revisionbinding.Binding) string {
		t.Helper()
		publisher, err := NewPublisher(root, binding.RunID)
		if err != nil {
			t.Fatal(err)
		}
		snapshot := RunSnapshot{ID: binding.RunID, Binding: &binding, Status: ResultInconclusive}
		events := []harness.Event{
			{Sequence: 1, Type: harness.EventRunCreated, RunID: binding.RunID, BindingSHA256: binding.BindingSHA256},
			{Sequence: 2, Type: harness.EventBindingConfirmed, RunID: binding.RunID, BindingSHA256: binding.BindingSHA256},
			{Sequence: 3, Type: harness.EventRunInconclusive, RunID: binding.RunID, BindingSHA256: binding.BindingSHA256, Status: string(ResultInconclusive)},
		}
		if err := publisher.Publish(context.Background(), snapshot, events, planmanifest.Manifest{}, nil); err != nil {
			t.Fatal(err)
		}
		bundle := filepath.Join(root, ".featureproof", "runs", binding.RunID)
		for _, name := range []string{"run.json", "events.jsonl", "report.json", "report.md"} {
			raw, err := os.ReadFile(filepath.Join(bundle, name))
			if err != nil || !strings.Contains(string(raw), binding.BindingSHA256) {
				t.Fatalf("%s missing binding: %v %s", name, err, raw)
			}
		}
		raw, err := os.ReadFile(filepath.Join(bundle, "run.json"))
		if err != nil {
			t.Fatal(err)
		}
		return string(raw)
	}
	before := publish(first)
	revision := revisionbinding.Revision{RepositoryID: "repo", CommitOID: strings.Repeat("c", 40)}
	attestation := revisionbinding.Attestation{EnvironmentID: "preview", RepositoryID: "repo", CommitOID: revision.CommitOID, Reference: "attestation-2", Verified: true}
	digest := strings.Repeat("a", 64)
	second, err := revisionbinding.NewBinding(context.Background(), "correction", revision, attestation, digest, digest, digest)
	if err != nil {
		t.Fatal(err)
	}
	publish(second)
	after, err := os.ReadFile(filepath.Join(root, ".featureproof", "runs", first.RunID, "run.json"))
	if err != nil || string(after) != before || first.BindingSHA256 == second.BindingSHA256 {
		t.Fatalf("correction modified prior bundle: %v", err)
	}
}

// TestRunnerPublishesAttestedRevisionBinding exercises admission, one safe
// plan batch, evidence persistence, and terminal publication without Git remote.
func TestRunnerPublishesAttestedRevisionBinding(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()
	specPath, planPath := writeInputs(t, 200, 1)
	raw, err := os.ReadFile(planPath)
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]any
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	plans := doc["plans"].([]any)
	step := plans[0].(map[string]any)["steps"].([]any)[0].(map[string]any)
	delete(step["input"].(map[string]any), "headers")
	raw, err = json.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(planPath, raw, 0600); err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	stdout, stderr := &strings.Builder{}, &strings.Builder{}
	deps := DefaultDependencies(root, stdout, stderr)
	deps.Clock = &testClock{next: time.Unix(100, 0).UTC()}
	deps.IDs = &testIDs{}
	deps.Waiter = testWaiter{}
	commit := strings.Repeat("a", 40)
	deps.RevisionResolver = fakeRevision{revisionbinding.Revision{RepositoryID: "repo", CommitOID: commit}}
	deps.EnvironmentAttestor = fakeAttestation{revisionbinding.Attestation{EnvironmentID: "preview", RepositoryID: "repo", CommitOID: commit, Reference: "proof-1", ValidUntil: time.Unix(10000, 0), Verified: true}}
	deps.SafeProjector = fixtureProjector{}
	runner, err := NewRunner(context.Background(), Request{SpecPath: specPath, PlanPath: planPath, Target: server.URL, RepositoryID: "repo", Revision: "main", EnvironmentID: "preview"}, deps)
	if err != nil {
		t.Fatal(err)
	}
	if code := runner.Run(context.Background()); code != 0 {
		t.Fatalf("code=%d stdout=%s stderr=%s", code, stdout, stderr)
	}
	bundle := filepath.Join(root, ".featureproof", "runs", "id-1")
	runRaw, err := os.ReadFile(filepath.Join(bundle, "run.json"))
	if err != nil {
		t.Fatal(err)
	}
	var run struct {
		Binding      revisionbinding.Binding       `json:"binding"`
		PlanBindings []revisionbinding.PlanBinding `json:"plan_bindings"`
	}
	if err := json.Unmarshal(runRaw, &run); err != nil {
		t.Fatal(err)
	}
	if err := run.Binding.Verify(context.Background()); err != nil || run.Binding.CommitOID != commit || len(run.PlanBindings) != 1 {
		t.Fatalf("binding=%+v plans=%+v err=%v", run.Binding, run.PlanBindings, err)
	}
	for _, name := range []string{"events.jsonl", "report.json", "report.md"} {
		raw, err := os.ReadFile(filepath.Join(bundle, name))
		if err != nil || !strings.Contains(string(raw), run.Binding.BindingSHA256) {
			t.Fatalf("%s missing binding: %v", name, err)
		}
	}
	entries, err := os.ReadDir(filepath.Join(bundle, "evidence"))
	if err != nil || len(entries) != 1 {
		t.Fatalf("evidence=%v err=%v", entries, err)
	}
	recordRaw, err := os.ReadFile(filepath.Join(bundle, "evidence", entries[0].Name()))
	if err != nil || !strings.Contains(string(recordRaw), run.Binding.BindingSHA256) {
		t.Fatalf("unbound evidence: %v", err)
	}
}

// TestUnclassifiedLiteralStopsBeforeRunOrTool proves that a secret-bearing
// legacy manifest cannot acquire a public digest through the test projector.
func TestUnclassifiedLiteralStopsBeforeRunOrTool(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()
	specPath, planPath := writeInputs(t, 200, 1)
	root := t.TempDir()
	stdout, stderr := &strings.Builder{}, &strings.Builder{}
	deps := DefaultDependencies(root, stdout, stderr)
	deps.Clock = &testClock{next: time.Unix(100, 0).UTC()}
	deps.IDs = &testIDs{}
	commit := strings.Repeat("a", 40)
	deps.RevisionResolver = fakeRevision{revisionbinding.Revision{RepositoryID: "repo", CommitOID: commit}}
	deps.EnvironmentAttestor = fakeAttestation{revisionbinding.Attestation{EnvironmentID: "preview", RepositoryID: "repo", CommitOID: commit, Reference: "proof-1", ValidUntil: time.Unix(10000, 0), Verified: true}}
	deps.SafeProjector = fixtureProjector{}
	runner, err := NewRunner(context.Background(), Request{SpecPath: specPath, PlanPath: planPath, Target: server.URL, RepositoryID: "repo", Revision: "main", EnvironmentID: "preview"}, deps)
	if err != nil {
		t.Fatal(err)
	}
	if code := runner.Run(context.Background()); code != 3 || calls.Load() != 0 {
		t.Fatalf("code=%d calls=%d", code, calls.Load())
	}
	if strings.Contains(stdout.String()+stderr.String(), "CANARY-SECRET") {
		t.Fatal("secret in diagnostic")
	}
	if _, err := os.Stat(filepath.Join(root, ".featureproof", "runs")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("bundle directory created: %v", err)
	}
}

// TestRevisionOrConfigurationDriftStopsBeforeTool exercises rechecks between
// preflight and the first batch without publishing a functional verdict.
func TestRevisionOrConfigurationDriftStopsBeforeTool(t *testing.T) {
	for _, variant := range []string{"revision", "configuration"} {
		t.Run(variant, func(t *testing.T) {
			var calls atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				calls.Add(1)
				w.WriteHeader(http.StatusOK)
			}))
			defer server.Close()
			runner, root, _, stderr := runnerFor(t, server.URL, 200, 1)
			if variant == "revision" {
				var checks atomic.Int32
				runner.deps.RevisionResolver = resolverFunc(func(context.Context, string, string) (revisionbinding.Revision, error) {
					if checks.Add(1) == 1 {
						return runner.revision, nil
					}
					return revisionbinding.Revision{RepositoryID: "repo", CommitOID: strings.Repeat("b", 40)}, nil
				})
			} else {
				var checks atomic.Int32
				runner.deps.SafeProjector = projectorFunc(func(ctx context.Context, spec specification.Specification, manifest planmanifest.Manifest, plans []planning.ValidatedPlan) (SafeProjections, error) {
					projection, err := (legacyFixtureProjector{}).Project(ctx, spec, manifest, plans)
					if checks.Add(1) > 1 {
						projection.Configuration = revisionbinding.PublicString("changed")
					}
					return projection, err
				})
			}
			if code := runner.Run(context.Background()); code != 2 || calls.Load() != 0 {
				t.Fatalf("code=%d calls=%d stderr=%s", code, calls.Load(), stderr)
			}
			raw, err := os.ReadFile(filepath.Join(root, ".featureproof", "runs", "id-1", "run.json"))
			if err != nil || !strings.Contains(string(raw), `"status": "inconclusive"`) {
				t.Fatalf("missing bound inconclusive run: %v %s", err, raw)
			}
		})
	}
}

// TestProjectionCancellationStartsNoRun verifies cancellation inside the
// classification boundary before ID generation and staging.
func TestProjectionCancellationStartsNoRun(t *testing.T) {
	runner, root, _, _ := runnerFor(t, "http://127.0.0.1:1", 200, 1)
	ctx, cancel := context.WithCancelCause(context.Background())
	runner.deps.SafeProjector = projectorFunc(func(ctx context.Context, _ specification.Specification, _ planmanifest.Manifest, _ []planning.ValidatedPlan) (SafeProjections, error) {
		cancel(ErrTerminalSignal)
		return SafeProjections{}, ctx.Err()
	})
	if code := runner.Run(ctx); code != 130 {
		t.Fatalf("code=%d", code)
	}
	if _, err := os.Stat(filepath.Join(root, ".featureproof")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("staging created after cancellation: %v", err)
	}
}

// TestRunnerWithLocalGitAndTrustedAttestor crosses the real read-only Git
// adapter and fake deployment boundary through the complete local runner.
func TestRunnerWithLocalGitAndTrustedAttestor(t *testing.T) {
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	gitRoot := t.TempDir()
	git := func(args ...string) string {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = gitRoot
		cmd.Env = append(os.Environ(), "GIT_OPTIONAL_LOCKS=0")
		raw, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v: %s", args, err, raw)
		}
		return strings.TrimSpace(string(raw))
	}
	git("init", "-q")
	git("config", "user.name", "Fixture")
	git("config", "user.email", "fixture@example.invalid")
	if err := os.WriteFile(filepath.Join(gitRoot, "tracked.txt"), []byte("v1"), 0600); err != nil {
		t.Fatal(err)
	}
	git("add", "tracked.txt")
	git("commit", "-qm", "v1")
	commit := git("rev-parse", "HEAD")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) }))
	defer server.Close()
	specPath, planPath := writeInputs(t, 200, 1)
	raw, err := os.ReadFile(planPath)
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]any
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	step := doc["plans"].([]any)[0].(map[string]any)["steps"].([]any)[0].(map[string]any)
	delete(step["input"].(map[string]any), "headers")
	raw, err = json.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(planPath, raw, 0600); err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	stdout, stderr := &strings.Builder{}, &strings.Builder{}
	deps := DefaultDependencies(root, stdout, stderr)
	deps.Clock = &testClock{next: time.Unix(100, 0).UTC()}
	deps.IDs = &testIDs{}
	deps.Waiter = testWaiter{}
	deps.RevisionResolver = revisionbinding.GitResolver{Root: gitRoot, RepositoryID: "repo"}
	deps.EnvironmentAttestor = fakeAttestation{revisionbinding.Attestation{EnvironmentID: "preview", RepositoryID: "repo", CommitOID: commit, Reference: "proof-1", ValidUntil: time.Unix(10000, 0), Verified: true}}
	deps.SafeProjector = fixtureProjector{}
	request := Request{SpecPath: specPath, PlanPath: planPath, Target: server.URL, RepositoryID: "repo", Revision: "HEAD", EnvironmentID: "preview"}
	runner, err := NewRunner(context.Background(), request, deps)
	if err != nil {
		t.Fatal(err)
	}
	if code := runner.Run(context.Background()); code != 0 {
		t.Fatalf("code=%d stdout=%s stderr=%s", code, stdout, stderr)
	}
	runRaw, err := os.ReadFile(filepath.Join(root, ".featureproof", "runs", "id-1", "run.json"))
	if err != nil || !strings.Contains(string(runRaw), commit) {
		t.Fatalf("commit not bound: %v %s", err, runRaw)
	}
	if err := os.WriteFile(filepath.Join(gitRoot, "tracked.txt"), []byte("dirty"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := NewRunner(context.Background(), request, deps); !revisionbinding.IsCategory(err, revisionbinding.DirtyWorktree) {
		t.Fatalf("dirty working tree admitted: %v", err)
	}
	if _, err := NewRunner(context.Background(), Request{SpecPath: specPath, PlanPath: planPath, Target: server.URL, RepositoryID: "repo", Revision: "missing", EnvironmentID: "preview"}, deps); !revisionbinding.IsCategory(err, revisionbinding.UnknownCommit) {
		t.Fatalf("unknown commit admitted: %v", err)
	}
}

// TestReportOnlyEvidenceReferencesMustBelongToBinding covers links that occur
// only in report.json, including descendants absent from constraint summaries.
func TestReportOnlyEvidenceReferencesMustBelongToBinding(t *testing.T) {
	for _, location := range []string{"step", "terminal", "assertion", "nested_assertion"} {
		for _, foreign := range []bool{false, true} {
			name := location + "/missing"
			if foreign {
				name = location + "/foreign"
			}
			t.Run(name, func(t *testing.T) {
				binding := testBinding(t)
				root := t.TempDir()
				publisher, err := NewPublisher(root, binding.RunID)
				if err != nil {
					t.Fatal(err)
				}
				store, err := publisher.EvidenceStore()
				if err != nil {
					t.Fatal(err)
				}
				if foreign {
					policy, err := evidence.NewPolicy(evidence.Config{MaxBodyBytes: 1024})
					if err != nil {
						t.Fatal(err)
					}
					clean, err := evidence.NewSanitizer(policy).Sanitize(context.Background(), map[string]any{"ok": true})
					if err != nil {
						t.Fatal(err)
					}
					record, err := evidence.NewRecord(evidence.Record{ID: "ev", RunID: binding.RunID}, clean, clean)
					if err != nil {
						t.Fatal(err)
					}
					record, err = evidence.BindRecord(record, binding.RunID, strings.Repeat("c", 64))
					if err != nil {
						t.Fatal(err)
					}
					if err := store.Save(context.Background(), record); err != nil {
						t.Fatal(err)
					}
				}
				step := planning.StepResult{StepID: "step"}
				switch location {
				case "step":
					step.EvidenceIDs = []string{"ev"}
				case "terminal":
					step.TerminalEvidenceID = "ev"
				case "assertion":
					step.AssertionResults = []planning.AssertionResult{{EvidenceIDs: []string{"ev"}}}
				case "nested_assertion":
					step.AssertionResults = []planning.AssertionResult{{Children: []planning.AssertionResult{{EvidenceIDs: []string{"ev"}}}}}
				}
				snapshot := RunSnapshot{ID: binding.RunID, Binding: &binding, Status: ResultInconclusive, Constraints: []ConstraintResult{{ConstraintID: "c", Steps: []planning.StepResult{step}}}}
				events := []harness.Event{
					{Sequence: 1, Type: harness.EventRunCreated, RunID: binding.RunID, BindingSHA256: binding.BindingSHA256},
					{Sequence: 2, Type: harness.EventBindingConfirmed, RunID: binding.RunID, BindingSHA256: binding.BindingSHA256},
					{Sequence: 3, Type: harness.EventRunInconclusive, RunID: binding.RunID, BindingSHA256: binding.BindingSHA256, Status: string(ResultInconclusive)},
				}
				err = publisher.Publish(context.Background(), snapshot, events, planmanifest.Manifest{}, store)
				if !revisionbinding.IsCategory(err, revisionbinding.BindingIntegrity) {
					t.Fatalf("unverified report evidence category: %v", err)
				}
				if _, err := os.Stat(filepath.Join(root, ".featureproof", "runs", binding.RunID)); !errors.Is(err, os.ErrNotExist) {
					t.Fatalf("bundle committed: %v", err)
				}
			})
		}
	}
}

// TestPublisherRejectsEvidenceIdentityMismatchWithTypedError exercises an
// injected store returning a valid-looking record under the wrong key.
func TestPublisherRejectsEvidenceIdentityMismatchWithTypedError(t *testing.T) {
	binding := testBinding(t)
	root := t.TempDir()
	publisher, err := NewPublisher(root, binding.RunID)
	if err != nil {
		t.Fatal(err)
	}
	snapshot := RunSnapshot{ID: binding.RunID, Binding: &binding, Status: ResultInconclusive, Constraints: []ConstraintResult{{EvidenceIDs: []string{"expected"}}}}
	events := []harness.Event{
		{Sequence: 1, Type: harness.EventRunCreated, RunID: binding.RunID, BindingSHA256: binding.BindingSHA256},
		{Sequence: 2, Type: harness.EventBindingConfirmed, RunID: binding.RunID, BindingSHA256: binding.BindingSHA256},
		{Sequence: 3, Type: harness.EventRunInconclusive, RunID: binding.RunID, BindingSHA256: binding.BindingSHA256, Status: string(ResultInconclusive)},
	}
	store := misleadingEvidenceStore{record: evidence.Record{ID: "other", RunID: binding.RunID, BindingSHA256: binding.BindingSHA256}}
	if err := publisher.Publish(context.Background(), snapshot, events, planmanifest.Manifest{}, store); !revisionbinding.IsCategory(err, revisionbinding.BindingIntegrity) {
		t.Fatalf("identity mismatch category: %v", err)
	}
}

// TestPublisherRejectsCorruptEvidenceWithTypedError treats a referenced but
// unreadable record as a binding-integrity failure before final visibility.
func TestPublisherRejectsCorruptEvidenceWithTypedError(t *testing.T) {
	binding := testBinding(t)
	root := t.TempDir()
	publisher, err := NewPublisher(root, binding.RunID)
	if err != nil {
		t.Fatal(err)
	}
	store, err := publisher.EvidenceStore()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ".featureproof", "runs", binding.RunID+".incomplete", "evidence", "ev.json"), []byte("{broken"), 0600); err != nil {
		t.Fatal(err)
	}
	snapshot := RunSnapshot{ID: binding.RunID, Binding: &binding, Status: ResultInconclusive, Constraints: []ConstraintResult{{EvidenceIDs: []string{"ev"}}}}
	events := []harness.Event{
		{Sequence: 1, Type: harness.EventRunCreated, RunID: binding.RunID, BindingSHA256: binding.BindingSHA256},
		{Sequence: 2, Type: harness.EventBindingConfirmed, RunID: binding.RunID, BindingSHA256: binding.BindingSHA256},
		{Sequence: 3, Type: harness.EventRunInconclusive, RunID: binding.RunID, BindingSHA256: binding.BindingSHA256, Status: string(ResultInconclusive)},
	}
	if err := publisher.Publish(context.Background(), snapshot, events, planmanifest.Manifest{}, store); !revisionbinding.IsCategory(err, revisionbinding.BindingIntegrity) {
		t.Fatalf("corrupt evidence category: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, ".featureproof", "runs", binding.RunID)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("bundle committed: %v", err)
	}
}

// misleadingEvidenceStore models an external store violating its lookup contract.
type misleadingEvidenceStore struct{ record evidence.Record }

// Save is unused by publication.
func (misleadingEvidenceStore) Save(context.Context, evidence.Record) error { return nil }

// Get returns the intentionally wrong record identity.
func (s misleadingEvidenceStore) Get(context.Context, string) (evidence.Record, error) {
	return s.record, nil
}

// TestPublisherRejectsMissingOrMismatchedTerminal prevents a report verdict
// from being published without the matching unique journal closure.
func TestPublisherRejectsMissingOrMismatchedTerminal(t *testing.T) {
	for _, variant := range []string{"missing", "mismatch", "duplicate"} {
		t.Run(variant, func(t *testing.T) {
			binding := testBinding(t)
			root := t.TempDir()
			publisher, err := NewPublisher(root, binding.RunID)
			if err != nil {
				t.Fatal(err)
			}
			events := []harness.Event{
				{Sequence: 1, Type: harness.EventRunCreated, RunID: binding.RunID, BindingSHA256: binding.BindingSHA256},
				{Sequence: 2, Type: harness.EventBindingConfirmed, RunID: binding.RunID, BindingSHA256: binding.BindingSHA256},
			}
			switch variant {
			case "mismatch":
				events = append(events, harness.Event{Sequence: 3, Type: harness.EventRunInconclusive, RunID: binding.RunID, BindingSHA256: binding.BindingSHA256, Status: string(ResultInconclusive)})
			case "duplicate":
				events = append(events,
					harness.Event{Sequence: 3, Type: harness.EventRunCompleted, RunID: binding.RunID, BindingSHA256: binding.BindingSHA256, Status: string(ResultPassed)},
					harness.Event{Sequence: 4, Type: harness.EventRunCompleted, RunID: binding.RunID, BindingSHA256: binding.BindingSHA256, Status: string(ResultPassed)})
			}
			snapshot := RunSnapshot{ID: binding.RunID, Binding: &binding, Status: ResultPassed}
			if err := publisher.Publish(context.Background(), snapshot, events, planmanifest.Manifest{}, nil); !revisionbinding.IsCategory(err, revisionbinding.BindingIntegrity) {
				t.Fatalf("invalid terminal accepted: %v", err)
			}
			if _, err := os.Stat(filepath.Join(root, ".featureproof", "runs", binding.RunID)); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("bundle committed: %v", err)
			}
		})
	}
}

// TestCancellationAfterLastPlanCannotPublishPassedVerdict interrupts the
// final binding recheck after functional work, before terminal publication.
func TestCancellationAfterLastPlanCannotPublishPassedVerdict(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) }))
	defer server.Close()
	runner, root, _, stderr := runnerFor(t, server.URL, 200, 1)
	ctx, cancel := context.WithCancelCause(context.Background())
	var projections atomic.Int32
	runner.deps.SafeProjector = projectorFunc(func(ctx context.Context, spec specification.Specification, manifest planmanifest.Manifest, plans []planning.ValidatedPlan) (SafeProjections, error) {
		projection, err := (legacyFixtureProjector{}).Project(ctx, spec, manifest, plans)
		if projections.Add(1) == 3 {
			cancel(ErrTerminalSignal)
		}
		return projection, err
	})
	if code := runner.Run(ctx); code != 130 {
		t.Fatalf("code=%d projections=%d stderr=%s", code, projections.Load(), stderr)
	}
	raw, err := os.ReadFile(filepath.Join(root, ".featureproof", "runs", "id-1", "run.json"))
	if err != nil || !strings.Contains(string(raw), `"status": "cancelled"`) {
		t.Fatalf("cancelled terminal bundle missing: %v %s", err, raw)
	}
	events, err := os.ReadFile(filepath.Join(root, ".featureproof", "runs", "id-1", "events.jsonl"))
	if err != nil || !strings.Contains(string(events), `"type":"run_cancelled"`) || strings.Contains(string(events), `"type":"run_completed"`) {
		t.Fatalf("terminal journal mismatch: %v %s", err, events)
	}
}
