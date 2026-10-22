package localvalidation

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/asierhr/featureProof/internal/evidence"
	"github.com/asierhr/featureProof/internal/harness"
	"github.com/asierhr/featureProof/internal/planmanifest"
	"github.com/asierhr/featureProof/internal/planning"
	"github.com/asierhr/featureProof/internal/revisionbinding"
	specyaml "github.com/asierhr/featureProof/internal/specification/yaml"
)

// testClock supplies deterministic timestamps to complete runner tests.
type testClock struct{ next time.Time }

// Now advances deterministically so run and evidence durations are reproducible.
func (c *testClock) Now() time.Time { c.next = c.next.Add(time.Millisecond); return c.next }

// testIDs supplies deterministic isolated run and evidence identities.
type testIDs struct{ next int }

// Next returns deterministic unique IDs within one isolated invocation.
func (i *testIDs) Next() string { i.next++; return fmt.Sprintf("id-%d", i.next) }

// testWaiter removes real retry delays from the local execution suite.
type testWaiter struct{}

// Wait observes cancellation without adding wall-clock delays to tests.
func (testWaiter) Wait(ctx context.Context, _ time.Duration) error { return ctx.Err() }

// writeInputs creates a valid specification and explicit manifest for one functional scenario.
func writeInputs(t *testing.T, expected int, maxCalls int) (string, string) {
	t.Helper()
	root := t.TempDir()
	spec := fmt.Sprintf("version: 1\nfeature:\n  id: feature\n  description: local\ntarget:\n  id: deployed\n  base_url: https://unused.example\nconstraints:\n  - id: health\n    description: responds\n    target_id: deployed\nlimits:\n  max_steps: 2\n  max_tool_calls: %d\n  max_retries: 0\n", maxCalls)
	manifest := fmt.Sprintf(`{"version":1,"runtime":{"http":{"allowed_methods":["GET"],"allowed_request_headers":["X-Secret"],"allowed_response_headers":["Content-Type","X-Secret"],"follow_redirects_for_methods":[],"max_redirects":0,"timeout_ms":1000,"max_url_bytes":2048,"max_request_header_bytes":8192,"max_request_body_bytes":1024,"max_response_header_bytes":8192,"max_response_body_bytes":1024},"redaction":{"max_body_bytes":1024,"additional_sensitive_headers":["X-Secret"],"sensitive_json_paths":["/body/secret"],"model_sensitive_json_paths":[]},"policy":{"allowed_tools":["http.request"]}},"plans":[{"id":"health-plan","constraint_id":"health","steps":[{"id":"request","tool":"http.request","input":{"method":{"literal":"GET"},"path":{"literal":"/"},"headers":{"object":{"X-Secret":{"array":[{"literal":"CANARY-SECRET"}]}}}},"assertions":[{"id":"status","definition":{"operator":"eq","path":"/status","expected":%d}}],"depends_on":[]}]}]}`, expected)
	specPath := filepath.Join(root, "spec.yaml")
	planPath := filepath.Join(root, "plan.json")
	if err := os.WriteFile(specPath, []byte(spec), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(planPath, []byte(manifest), 0600); err != nil {
		t.Fatal(err)
	}
	return specPath, planPath
}

// TestPreparedRunnerUsesHistoricalDeterministicComposition proves generated plans enter the same runner as explicit manifests.
func TestPreparedRunnerUsesHistoricalDeterministicComposition(t *testing.T) {
	specPath, planPath := writeInputs(t, 200, 1)
	spec, err := specyaml.Load(context.Background(), specPath)
	if err != nil {
		t.Fatal(err)
	}
	manifest, err := planmanifest.Load(context.Background(), planPath, spec)
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	deps := DefaultDependencies(root, &bytes.Buffer{}, &bytes.Buffer{})
	commit := strings.Repeat("a", 40)
	deps.RevisionResolver = fakeRevision{revisionbinding.Revision{RepositoryID: "repo", CommitOID: commit}}
	deps.EnvironmentAttestor = fakeAttestation{revisionbinding.Attestation{EnvironmentID: "preview", RepositoryID: "repo", CommitOID: commit, Reference: "proof-1", ValidUntil: time.Unix(10000, 0), Verified: true}}
	deps.SafeProjector, deps.Clock, deps.IDs, deps.Waiter, deps.FinalizeTimeout = legacyFixtureProjector{}, &testClock{next: time.Unix(100, 0)}, &testIDs{}, testWaiter{}, time.Second
	runner, err := NewPreparedRunner(context.Background(), PreparedRequest{Specification: spec, Runtime: manifest, Plans: manifest.Plans, Target: "http://127.0.0.1:1", RepositoryID: "repo", Revision: "main", EnvironmentID: "preview"}, deps)
	if err != nil || runner == nil {
		t.Fatalf("prepared runner unavailable: %v", err)
	}
	bad := manifest.Plans
	bad[0].ConstraintID = "other"
	if _, err = NewPreparedRunner(context.Background(), PreparedRequest{Specification: spec, Runtime: manifest, Plans: bad, Target: "http://127.0.0.1:1", RepositoryID: "repo", Revision: "main", EnvironmentID: "preview"}, deps); err == nil {
		t.Fatal("mismatched prepared plan accepted")
	}
}

// runnerFor binds a scenario to local-only network and deterministic process boundaries.
func runnerFor(t *testing.T, target string, expected, maxCalls int) (*Runner, string, *bytes.Buffer, *bytes.Buffer) {
	t.Helper()
	spec, plan := writeInputs(t, expected, maxCalls)
	root := t.TempDir()
	stdout, stderr := &bytes.Buffer{}, &bytes.Buffer{}
	clock := &testClock{next: time.Unix(100, 0).UTC()}
	deps := DefaultDependencies(root, stdout, stderr)
	commit := strings.Repeat("a", 40)
	deps.RevisionResolver = fakeRevision{revisionbinding.Revision{RepositoryID: "repo", CommitOID: commit}}
	deps.EnvironmentAttestor = fakeAttestation{revisionbinding.Attestation{EnvironmentID: "preview", RepositoryID: "repo", CommitOID: commit, Reference: "proof-1", ValidUntil: time.Unix(10000, 0), Verified: true}}
	deps.SafeProjector = legacyFixtureProjector{}
	deps.Clock = clock
	deps.IDs = &testIDs{}
	deps.Waiter = testWaiter{}
	deps.FinalizeTimeout = 10 * time.Second
	runner, err := NewRunner(context.Background(), Request{SpecPath: spec, PlanPath: plan, Target: target, RepositoryID: "repo", Revision: "main", EnvironmentID: "preview"}, deps)
	if err != nil {
		t.Fatal(err)
	}
	return runner, root, stdout, stderr
}

// TestRunnerExecutesConstraintsSequentiallyWithoutModel traces R2, R4-R7, R9, and R11 through the complete local flow.
func TestRunnerExecutesConstraintsSequentiallyWithoutModel(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("X-Secret", "CANARY-SECRET")
		_, _ = w.Write([]byte(`{"secret":"CANARY-SECRET","ok":true}`))
	}))
	defer server.Close()
	for _, scenario := range []struct {
		name, target             string
		expected, maxCalls, code int
		result                   ResultStatus
	}{{"passed", server.URL, 200, 1, 0, ResultPassed}, {"failed", server.URL, 201, 1, 1, ResultFailed}, {"inconclusive", "http://127.0.0.1:1", 200, 1, 2, ResultInconclusive}} {
		t.Run(scenario.name, func(t *testing.T) {
			runner, root, stdout, stderr := runnerFor(t, scenario.target, scenario.expected, scenario.maxCalls)
			if code := runner.Run(context.Background()); code != scenario.code {
				t.Fatalf("code=%d stdout=%s stderr=%s", code, stdout.String(), stderr.String())
			}
			if !strings.HasPrefix(stdout.String(), "run_id=id-1\nphase=executing\n") || !strings.HasSuffix(stdout.String(), "result="+string(scenario.result)+"\n") {
				t.Fatalf("unsafe or unordered stdout %q", stdout.String())
			}
			bundle := filepath.Join(root, ".featureproof", "runs", "id-1")
			for _, name := range []string{"run.json", "events.jsonl", "plan.json", "report.json", "report.md"} {
				if _, err := os.Stat(filepath.Join(bundle, name)); err != nil {
					t.Fatalf("missing %s: %v", name, err)
				}
			}
			all := stdout.String() + stderr.String()
			err := filepath.Walk(bundle, func(path string, info os.FileInfo, err error) error {
				if err != nil || info.IsDir() {
					return err
				}
				raw, readErr := os.ReadFile(path)
				if readErr != nil {
					return readErr
				}
				all += string(raw)
				return nil
			})
			if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(all, "CANARY-SECRET") {
				t.Fatalf("secret leaked: %s", all)
			}
		})
	}
}

// TestInvalidCompositionProducesNoRunArtifactsOrNetwork traces R2 and R3's pre-run validation boundary.
func TestInvalidCompositionProducesNoRunArtifactsOrNetwork(t *testing.T) {
	spec, plan := writeInputs(t, 200, 1)
	root := t.TempDir()
	stdout, stderr := &bytes.Buffer{}, &bytes.Buffer{}
	deps := DefaultDependencies(root, stdout, stderr)
	deps.Clock = &testClock{}
	deps.IDs = &testIDs{}
	deps.Waiter = testWaiter{}
	deps.FinalizeTimeout = time.Second
	_, err := NewRunner(context.Background(), Request{SpecPath: spec, PlanPath: plan, Target: "http://user:pass@127.0.0.1/path?query=x"}, deps)
	if err == nil {
		t.Fatal("invalid target accepted")
	}
	if _, statErr := os.Stat(filepath.Join(root, ".featureproof")); !os.IsNotExist(statErr) {
		t.Fatalf("run artifacts created: %v", statErr)
	}
}

// TestRunCommandReportsSafeTypedValidationDiagnostics traces R3 diagnostics across every composition boundary.
func TestRunCommandReportsSafeTypedValidationDiagnostics(t *testing.T) {
	for _, scenario := range []struct {
		name, mutateSpec, mutatePlan, target, expected string
	}{
		{name: "specification", mutateSpec: "version: 3", target: "http://127.0.0.1:1", expected: "category=unsupported_version field=version"},
		{name: "manifest", mutatePlan: `"version":3`, target: "http://127.0.0.1:1", expected: "category=unsupported_version field=version"},
		{name: "redaction", mutatePlan: `"additional_sensitive_headers":["X-Secret","X-Secret"]`, target: "http://127.0.0.1:1", expected: "category=invalid_redaction_config field=additional_sensitive_headers[1]"},
		{name: "http", target: "http://user:CANARY@127.0.0.1:1", expected: "category=invalid_http_config field=target.base_url"},
		{name: "planning", mutatePlan: `"operator":"BAD"`, target: "http://127.0.0.1:1", expected: "category=invalid_value field=steps[0].assertions[0].definition.operator"},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			spec, plan := writeInputs(t, 200, 1)
			if scenario.mutateSpec != "" {
				raw, err := os.ReadFile(spec)
				if err != nil {
					t.Fatal(err)
				}
				if err = os.WriteFile(spec, []byte(strings.Replace(string(raw), "version: 1", scenario.mutateSpec, 1)), 0600); err != nil {
					t.Fatal(err)
				}
			}
			if scenario.mutatePlan != "" {
				raw, err := os.ReadFile(plan)
				if err != nil {
					t.Fatal(err)
				}
				old := map[string]string{
					"manifest":  `"version":1`,
					"redaction": `"additional_sensitive_headers":["X-Secret"]`,
					"planning":  `"operator":"eq"`,
				}[scenario.name]
				if err = os.WriteFile(plan, []byte(strings.Replace(string(raw), old, scenario.mutatePlan, 1)), 0600); err != nil {
					t.Fatal(err)
				}
			}
			var stdout, stderr bytes.Buffer
			code := RunCommand(t.Context(), []string{"validate", "--spec", spec, "--plan", plan, "--target", scenario.target}, strings.NewReader(""), &stdout, &stderr)
			if code != 3 || !strings.Contains(stderr.String(), "featureproof_error=validation "+scenario.expected) {
				t.Fatalf("code=%d stderr=%q", code, stderr.String())
			}
			if strings.Contains(stderr.String(), "CANARY") {
				t.Fatalf("untrusted value leaked: %q", stderr.String())
			}
		})
	}
}

// TestRunCommandNeverRendersExternalMapKeysInDiagnostics covers REV-6 across specification, manifest, duplicate-key, and planning paths.
func TestRunCommandNeverRendersExternalMapKeysInDiagnostics(t *testing.T) {
	const hostile = `CANARY\r\n\tforged=1|:=\u0001`
	for _, scenario := range []struct {
		name, category, fieldPrefix string
		mutate                      func(*testing.T, string, string)
	}{
		{name: "specification unknown key", category: "unknown_field", fieldPrefix: "$mapping.members[", mutate: func(t *testing.T, spec, _ string) {
			raw, err := os.ReadFile(spec)
			if err != nil {
				t.Fatal(err)
			}
			raw = append(raw, []byte(`"`+hostile+`": true`+"\n")...)
			if err = os.WriteFile(spec, raw, 0600); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "manifest input key", category: "invalid_union", fieldPrefix: "plans[0].steps[0].input.members[", mutate: func(t *testing.T, _, plan string) {
			replaceFile(t, plan, `"method":{"literal":"GET"}`, `"`+hostile+`":{}`)
		}},
		{name: "duplicate key", category: "duplicate_key", fieldPrefix: "$.members[", mutate: func(t *testing.T, _, plan string) {
			replaceFile(t, plan, `{"version":1`, `{"`+hostile+`":0,"`+hostile+`":1,"version":1`)
		}},
		{name: "assertion key", category: "invalid_value", fieldPrefix: "steps[0].assertions[0].definition.members[", mutate: func(t *testing.T, _, plan string) {
			replaceFile(t, plan, `"expected":200`, `"expected":200,"`+hostile+`":true`)
		}},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			spec, plan := writeInputs(t, 200, 1)
			scenario.mutate(t, spec, plan)
			var stdout, stderr bytes.Buffer
			code := RunCommand(t.Context(), []string{"validate", "--spec", spec, "--plan", plan, "--target", "http://127.0.0.1:1"}, strings.NewReader(""), &stdout, &stderr)
			diagnostic := stderr.String()
			if code != 3 || strings.Count(diagnostic, "\n") != 1 || strings.ContainsAny(strings.TrimSuffix(diagnostic, "\n"), "\r\t\x01") {
				t.Fatalf("unsafe diagnostic shape code=%d stderr=%q", code, diagnostic)
			}
			if strings.Contains(diagnostic, "CANARY") || strings.Contains(diagnostic, "forged") || strings.Contains(diagnostic, "|:=") {
				t.Fatalf("external key leaked: %q", diagnostic)
			}
			want := "featureproof_error=validation category=" + scenario.category + " field=" + scenario.fieldPrefix
			if !strings.Contains(diagnostic, want) {
				t.Fatalf("diagnostic=%q want prefix=%q", diagnostic, want)
			}
		})
	}
}

// TestSafeFieldFailsClosedForUntrustedSegments keeps the final stderr projection safe if an upstream adapter regresses.
func TestSafeFieldFailsClosedForUntrustedSegments(t *testing.T) {
	for _, field := range []string{"steps[0].input.CANARY", "steps[x].input", "steps[0].input.members[-1]", "target.base_url\nforged=1"} {
		if got := safeField(field); got != "document" {
			t.Fatalf("safeField(%q)=%q", field, got)
		}
	}
	for _, field := range []string{"$", "$mapping.members[2]", "plans[0].steps[1].input.members[0]", "steps[0].assertions[0].definition.members[2]"} {
		if got := safeField(field); got != field {
			t.Fatalf("safeField(%q)=%q", field, got)
		}
	}
}

// replaceFile applies one exact fixture mutation while keeping diagnostic tests readable.
func replaceFile(t *testing.T, path, old, replacement string) {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	updated := strings.Replace(string(raw), old, replacement, 1)
	if updated == string(raw) {
		t.Fatalf("fixture fragment not found: %s", old)
	}
	if err = os.WriteFile(path, []byte(updated), 0600); err != nil {
		t.Fatal(err)
	}
}

// TestPublisherFailureLeavesOnlyIncompleteDiagnosticDirectory traces R8's atomic visibility boundary.
func TestPublisherFailureLeavesOnlyIncompleteDiagnosticDirectory(t *testing.T) {
	root := t.TempDir()
	publisher, err := NewPublisherWithFilesystem(root, "failure", &faultFileSystem{base: OSFileSystem{}, fail: "write"})
	if err != nil {
		t.Fatal(err)
	}
	snapshot := RunSnapshot{ID: "failure", Status: ResultPassed, StartedAt: time.Unix(1, 0), FinishedAt: time.Unix(2, 0)}
	if err := publishFixture(t, publisher, context.Background(), snapshot, planmanifest.Manifest{}, nil); err == nil {
		t.Fatal("injected failure ignored")
	}
	if _, err := os.Stat(filepath.Join(root, ".featureproof", "runs", "failure")); !os.IsNotExist(err) {
		t.Fatalf("final bundle exposed: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, ".featureproof", "runs", "failure.incomplete")); err != nil {
		t.Fatalf("staging missing: %v", err)
	}
}

// TestPublisherValidatesEvidenceLinksBeforeArtifactWrites traces R7's self-consistency checks.
func TestPublisherValidatesEvidenceLinksBeforeArtifactWrites(t *testing.T) {
	root := t.TempDir()
	publisher, err := NewPublisher(root, "run")
	if err != nil {
		t.Fatal(err)
	}
	store, err := publisher.EvidenceStore()
	if err != nil {
		t.Fatal(err)
	}
	policy, err := evidence.NewPolicy(evidence.Config{MaxBodyBytes: 10})
	if err != nil {
		t.Fatal(err)
	}
	sanitizer := evidence.NewSanitizer(policy)
	input, err := sanitizer.Sanitize(context.Background(), map[string]any{})
	if err != nil {
		t.Fatal(err)
	}
	output, err := sanitizer.Sanitize(context.Background(), map[string]any{"ok": true})
	if err != nil {
		t.Fatal(err)
	}
	record, err := evidence.NewRecord(evidence.Record{ID: "ev", RunID: "other"}, input, output)
	if err != nil {
		t.Fatal(err)
	}
	if err = store.Save(context.Background(), record); err != nil {
		t.Fatal(err)
	}
	snapshot := RunSnapshot{ID: "run", Constraints: []ConstraintResult{{ConstraintID: "c", EvidenceIDs: []string{"ev"}}}}
	if err = publishFixture(t, publisher, context.Background(), snapshot, planmanifest.Manifest{}, store); err == nil {
		t.Fatal("foreign evidence accepted")
	}
	second, err := NewPublisher(root, "duplicate")
	if err != nil {
		t.Fatal(err)
	}
	duplicate := RunSnapshot{ID: "duplicate", Constraints: []ConstraintResult{{EvidenceIDs: []string{"ev", "ev"}}}}
	if err = publishFixture(t, second, context.Background(), duplicate, planmanifest.Manifest{}, nil); err == nil {
		t.Fatal("duplicate evidence accepted")
	}
	third, err := NewPublisher(root, "missing-store")
	if err != nil {
		t.Fatal(err)
	}
	missing := RunSnapshot{ID: "missing-store", Constraints: []ConstraintResult{{EvidenceIDs: []string{"ev"}}}}
	if err = publishFixture(t, third, context.Background(), missing, planmanifest.Manifest{}, nil); err == nil {
		t.Fatal("missing evidence store accepted")
	}
}

type emptyIDs struct{}

// Next simulates an unavailable production identity source before publication.
func (emptyIDs) Next() string { return "" }

// TestRunnerRejectsEmptyGeneratedIdentityBeforeStaging traces R5's non-empty run identity invariant.
func TestRunnerRejectsEmptyGeneratedIdentityBeforeStaging(t *testing.T) {
	runner, root, _, _ := runnerFor(t, "http://127.0.0.1:1", 200, 1)
	runner.deps.IDs = emptyIDs{}
	if code := runner.Run(context.Background()); code != 3 {
		t.Fatalf("code=%d", code)
	}
	if _, err := os.Stat(filepath.Join(root, ".featureproof")); !os.IsNotExist(err) {
		t.Fatalf("artifacts created: %v", err)
	}
}

// TestTerminalSignalMarkerClassifiesNotifyContextCancellation traces the production signal marker.
func TestTerminalSignalMarkerClassifiesNotifyContextCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(TerminalSignalContext(context.Background()))
	cancel()
	if !terminalCancellation(ctx) {
		t.Fatal("terminal marker lost")
	}
	if safeError("unused", nil) != nil {
		t.Fatal("nil error changed")
	}
	if durationMillis(time.Unix(2, 0), time.Unix(1, 0)) != 0 {
		t.Fatal("negative duration retained")
	}
}

// TestOperationalSetupFailuresStayOutsideFunctionalResults covers pre-execution and publication error branches.
func TestOperationalSetupFailuresStayOutsideFunctionalResults(t *testing.T) {
	fileRoot := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(fileRoot, []byte("x"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := NewPublisher(fileRoot, "run"); err == nil {
		t.Fatal("file root accepted")
	}
	publisher, err := NewPublisher(t.TempDir(), "cancelled")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err = publisher.Publish(ctx, RunSnapshot{}, nil, planmanifest.Manifest{}, nil); err == nil {
		t.Fatal("cancelled publication accepted")
	}
	runner, root, _, _ := runnerFor(t, "http://127.0.0.1:1", 200, 1)
	if err = os.MkdirAll(filepath.Join(root, ".featureproof", "runs", "id-1"), 0700); err != nil {
		t.Fatal(err)
	}
	if code := runner.Run(context.Background()); code != 3 {
		t.Fatalf("collision code=%d", code)
	}
	spec, plan := writeInputs(t, 200, 1)
	if _, err = NewRunner(context.Background(), Request{SpecPath: spec, PlanPath: plan, Target: "http://127.0.0.1:1"}, RuntimeDependencies{}); err == nil {
		t.Fatal("missing dependencies accepted")
	}
	var stdout, stderr bytes.Buffer
	if code := RunCommand(context.Background(), []string{"validate", "--spec", filepath.Join(t.TempDir(), "missing"), "--plan", plan, "--target", "http://127.0.0.1:1"}, strings.NewReader(""), &stdout, &stderr); code != 3 || !strings.Contains(stderr.String(), "featureproof_error=validation") {
		t.Fatalf("code=%d stderr=%s", code, stderr.String())
	}
}

// TestRunnerClassifiesPublisherAndEvidenceSetupFailures covers replaceable filesystem boundaries.
func TestRunnerClassifiesPublisherAndEvidenceSetupFailures(t *testing.T) {
	runner, _, _, _ := runnerFor(t, "http://127.0.0.1:1", 200, 1)
	runner.deps.PublisherFactory = func(string, string) (*Publisher, error) { return nil, errors.New("injected") }
	if code := runner.Run(context.Background()); code != 3 {
		t.Fatalf("publisher code=%d", code)
	}
	runner, _, _, _ = runnerFor(t, "http://127.0.0.1:1", 200, 1)
	runner.deps.PublisherFactory = func(root, id string) (*Publisher, error) {
		publisher, err := NewPublisher(root, id)
		if err == nil {
			if removeErr := os.Remove(publisher.staging); removeErr != nil {
				return nil, removeErr
			}
			if writeErr := os.WriteFile(publisher.staging, []byte("file"), 0600); writeErr != nil {
				return nil, writeErr
			}
		}
		return publisher, err
	}
	if code := runner.Run(context.Background()); code != 3 {
		t.Fatalf("evidence setup code=%d", code)
	}
	if err := (contextWaiter{}).Wait(context.Background(), 0); err != nil {
		t.Fatal(err)
	}
}

// TestNewRunnerRejectsRedactionAndAssertionConfigurationBeforeRun traces R3's complete composition boundary.
func TestNewRunnerRejectsRedactionAndAssertionConfigurationBeforeRun(t *testing.T) {
	for _, replacement := range []struct{ old, new string }{{`"additional_sensitive_headers":["X-Secret"]`, `"additional_sensitive_headers":["X-Secret","X-Secret"]`}, {`"operator":"eq"`, `"operator":"BAD"`}} {
		spec, plan := writeInputs(t, 200, 1)
		raw, err := os.ReadFile(plan)
		if err != nil {
			t.Fatal(err)
		}
		if err = os.WriteFile(plan, []byte(strings.Replace(string(raw), replacement.old, replacement.new, 1)), 0600); err != nil {
			t.Fatal(err)
		}
		deps := DefaultDependencies(t.TempDir(), &bytes.Buffer{}, &bytes.Buffer{})
		if _, err = NewRunner(context.Background(), Request{SpecPath: spec, PlanPath: plan, Target: "http://127.0.0.1:1"}, deps); err == nil {
			t.Fatalf("configuration accepted after replacing %s", replacement.old)
		}
	}
}

// TestIndependentRunnersDoNotShareMutableState traces R11 under concurrent isolated roots.
func TestIndependentRunnersDoNotShareMutableState(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) }))
	defer server.Close()
	first, firstRoot, _, _ := runnerFor(t, server.URL, 200, 1)
	second, secondRoot, _, _ := runnerFor(t, server.URL, 200, 1)
	codes := make([]int, 2)
	var group sync.WaitGroup
	group.Add(2)
	go func() { defer group.Done(); codes[0] = first.Run(context.Background()) }()
	go func() { defer group.Done(); codes[1] = second.Run(context.Background()) }()
	group.Wait()
	if codes[0] != 0 || codes[1] != 0 {
		t.Fatalf("codes=%v", codes)
	}
	for _, root := range []string{firstRoot, secondRoot} {
		if _, err := os.Stat(filepath.Join(root, ".featureproof", "runs", "id-1", "report.json")); err != nil {
			t.Fatal(err)
		}
	}
}

// TestPlanSnapshotPreservesSafeStructureWithoutLiteralValues traces R5 and R7 for references and grouped assertions.
func TestPlanSnapshotPreservesSafeStructureWithoutLiteralValues(t *testing.T) {
	field := "status"
	definition := map[string]any{"all": []any{
		map[string]any{"operator": "eq", "path": "/status", "expected": "CANARY"},
		map[string]any{"any": []any{map[string]any{"operator": "exists", "path": "/body"}}},
	}}
	step := planning.PlanStep{ID: "s", Tool: "http.request", Input: map[string]planning.InputValue{
		"secret":    {Literal: &planning.LiteralValue{Value: "CANARY"}},
		"reference": {Reference: &planning.OutputReference{StepID: "prior", ValueType: planning.ValueString, Path: []planning.PathSegment{{Field: &field}}}},
	}, Assertions: []planning.AssertionDeclaration{{ID: "a", Definition: definition}}}
	manifest := planmanifest.Manifest{Version: 1, Plans: []planning.ValidationPlan{{ID: "p", ConstraintID: "c", Steps: []planning.PlanStep{step}}}}
	raw, err := json.Marshal(safeManifest(manifest, ""))
	if err != nil {
		t.Fatal(err)
	}
	var document struct {
		Plans json.RawMessage `json:"plans"`
	}
	if err = json.Unmarshal(raw, &document); err != nil {
		t.Fatal(err)
	}
	text := string(document.Plans)
	if strings.Contains(text, "CANARY") || strings.Contains(text, "commitment") || !strings.Contains(text, "reference") || !strings.Contains(text, "[REDACTED]") {
		t.Fatalf("unsafe snapshot %s", text)
	}
}

// TestHistoricalValidatePlanPackageHasNoCommitmentOracle proves the production legacy runner publishes no unkeyed value hashes.
func TestHistoricalValidatePlanPackageHasNoCommitmentOracle(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) }))
	defer server.Close()
	runner, root, _, _ := runnerFor(t, server.URL, 200, 1)
	if code := runner.Run(context.Background()); code != 0 {
		t.Fatalf("historical validate exit=%d", code)
	}
	raw, err := os.ReadFile(filepath.Join(root, ".featureproof", "runs", "id-1", "plan.json"))
	if err != nil {
		t.Fatal(err)
	}
	var document struct {
		Plans json.RawMessage `json:"plans"`
	}
	if err = json.Unmarshal(raw, &document); err != nil {
		t.Fatal(err)
	}
	text := string(document.Plans)
	if strings.Contains(text, "commitment") || strings.Contains(text, "GET") || strings.Contains(text, "200") {
		t.Fatalf("legacy package exposes a dictionary oracle: %s", text)
	}
}

// TestSignalCancellationBeforeAdmissionStartsNoRun traces the dedicated exit code and no-work preflight.
func TestSignalCancellationBeforeAdmissionStartsNoRun(t *testing.T) {
	runner, root, stdout, _ := runnerFor(t, "http://127.0.0.1:1", 200, 1)
	ctx, cancel := context.WithCancelCause(context.Background())
	cancel(ErrTerminalSignal)
	if code := runner.Run(ctx); code != 130 {
		t.Fatalf("code=%d", code)
	}
	if _, err := os.Stat(filepath.Join(root, ".featureproof", "runs")); !os.IsNotExist(err) {
		t.Fatalf("run created after pre-admission cancellation: %v", err)
	}
	if stdout.Len() != 0 {
		t.Fatalf("stdout=%q", stdout.String())
	}
}

// TestSignalCancellationDuringHTTPPropagatesAndPublishesConfirmedPrefix traces R10 across the live tool boundary.
func TestSignalCancellationDuringHTTPPropagatesAndPublishesConfirmedPrefix(t *testing.T) {
	started := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) { close(started); <-r.Context().Done() }))
	defer server.Close()
	runner, root, stdout, _ := runnerFor(t, server.URL, 200, 1)
	ctx, cancel := context.WithCancelCause(context.Background())
	result := make(chan int, 1)
	go func() { result <- runner.Run(ctx) }()
	<-started
	cancel(ErrTerminalSignal)
	if code := <-result; code != 130 {
		t.Fatalf("code=%d stdout=%s", code, stdout.String())
	}
	raw, err := os.ReadFile(filepath.Join(root, ".featureproof", "runs", "id-1", "run.json"))
	if err != nil || !strings.Contains(string(raw), `"status": "cancelled"`) {
		t.Fatalf("cancelled bundle=%s err=%v", raw, err)
	}
	index, err := ReadPackage(context.Background(), filepath.Join(root, ".featureproof", "runs", "id-1"))
	if err != nil || index.Status != ResultCancelled || len(index.Failures) != 0 {
		t.Fatalf("cancelled package=%+v err=%v", index, err)
	}
}

// TestCancellationFinalizationFailureDoesNotPublishPartialRun traces R10's failure path and retained diagnostics.
func TestCancellationFinalizationFailureDoesNotPublishPartialRun(t *testing.T) {
	started := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) { close(started); <-r.Context().Done() }))
	defer server.Close()
	runner, root, _, _ := runnerFor(t, server.URL, 200, 1)
	runner.deps.PublisherFactory = func(root, id string) (*Publisher, error) {
		return NewPublisherWithFilesystem(root, id, &faultFileSystem{base: OSFileSystem{}, fail: "write"})
	}
	ctx, cancel := context.WithCancelCause(context.Background())
	result := make(chan int, 1)
	go func() { result <- runner.Run(ctx) }()
	<-started
	cancel(ErrTerminalSignal)
	if code := <-result; code != 130 {
		t.Fatalf("code=%d", code)
	}
	if _, err := os.Stat(filepath.Join(root, ".featureproof", "runs", "id-1")); !os.IsNotExist(err) {
		t.Fatalf("final exposed: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, ".featureproof", "runs", "id-1.incomplete")); err != nil {
		t.Fatalf("staging missing: %v", err)
	}
}

// TestPublisherRejectsCollisionWithoutOverwrite traces R7's fail-on-exists identity rule.
func TestPublisherRejectsCollisionWithoutOverwrite(t *testing.T) {
	root := t.TempDir()
	first, err := NewPublisher(root, "same")
	if err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(first.staging, "marker")
	if err := os.WriteFile(marker, []byte("keep"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := NewPublisher(root, "same"); err == nil {
		t.Fatal("collision accepted")
	}
	raw, err := os.ReadFile(marker)
	if err != nil || string(raw) != "keep" {
		t.Fatalf("collision modified staging: %q %v", raw, err)
	}
}

// TestOrdinaryContextCancellationIsAFeatureProofError traces R9's signal-only cancellation classification.
func TestOrdinaryContextCancellationIsAFeatureProofError(t *testing.T) {
	runner, _, stdout, _ := runnerFor(t, "http://127.0.0.1:1", 200, 1)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if code := runner.Run(ctx); code != 3 {
		t.Fatalf("code=%d stdout=%s", code, stdout.String())
	}
}

// blockingPolicy exposes deterministic cancellation while authorization owns control.
type blockingPolicy struct{ started chan struct{} }

// Evaluate confirms entry into the policy boundary and exits only through caller cancellation.
func (p *blockingPolicy) Evaluate(ctx context.Context, _ *harness.Run, _ harness.ToolCall) (harness.PolicyDecision, error) {
	close(p.started)
	<-ctx.Done()
	return harness.PolicyDecision{}, ctx.Err()
}

// blockingEvidenceStore exposes deterministic cancellation before an evidence record is confirmed.
type blockingEvidenceStore struct {
	base    evidence.Store
	started chan struct{}
}

// Save confirms entry into persistence and leaves the confirmed prefix unchanged on cancellation.
func (s *blockingEvidenceStore) Save(ctx context.Context, _ evidence.Record) error {
	close(s.started)
	<-ctx.Done()
	return ctx.Err()
}

// Get delegates reads of any previously confirmed evidence to the real isolated store.
func (s *blockingEvidenceStore) Get(ctx context.Context, id string) (evidence.Record, error) {
	return s.base.Get(ctx, id)
}

// blockingEvaluator exposes deterministic cancellation after evidence confirmation and before assertions complete.
type blockingEvaluator struct{ started chan struct{} }

// Evaluate confirms entry into assertion evaluation and exits only through caller cancellation.
func (e *blockingEvaluator) Evaluate(ctx context.Context, _ planning.PlanStep, _ any, _ []string) (planning.StepEvaluation, error) {
	close(e.started)
	<-ctx.Done()
	return planning.StepEvaluation{}, ctx.Err()
}

// assertCancelledBoundary verifies the stable signal exit, terminal bundle, confirmed prefix, and stopped tool work.
func assertCancelledBoundary(t *testing.T, runner *Runner, root string, started <-chan struct{}, cancel context.CancelCauseFunc, ctx context.Context, calls *atomic.Int32, expectedCalls, expectedEvidenceEvents int) {
	t.Helper()
	result := make(chan int, 1)
	go func() { result <- runner.Run(ctx) }()
	<-started
	cancel(ErrTerminalSignal)
	if code := <-result; code != 130 {
		t.Fatalf("code=%d", code)
	}
	if got := int(calls.Load()); got != expectedCalls {
		t.Fatalf("tool calls=%d expected=%d", got, expectedCalls)
	}
	bundle := filepath.Join(root, ".featureproof", "runs", "id-1")
	run, err := os.ReadFile(filepath.Join(bundle, "run.json"))
	if err != nil || !strings.Contains(string(run), `"status": "cancelled"`) {
		t.Fatalf("cancelled run=%s err=%v", run, err)
	}
	events, err := os.ReadFile(filepath.Join(bundle, "events.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Count(string(events), `"type":"evidence_persisted"`); got != expectedEvidenceEvents {
		t.Fatalf("evidence events=%d expected=%d events=%s", got, expectedEvidenceEvents, events)
	}
	if !strings.Contains(string(events), `"type":"run_cancelled"`) {
		t.Fatalf("terminal event missing: %s", events)
	}
}

// TestCancellationDuringPolicyStopsBeforeToolExecution traces R10 at the authorization boundary.
func TestCancellationDuringPolicyStopsBeforeToolExecution(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { calls.Add(1); w.WriteHeader(http.StatusOK) }))
	defer server.Close()
	runner, root, _, _ := runnerFor(t, server.URL, 200, 1)
	started := make(chan struct{})
	runner.policy = &blockingPolicy{started: started}
	ctx, cancel := context.WithCancelCause(context.Background())
	assertCancelledBoundary(t, runner, root, started, cancel, ctx, &calls, 0, 0)
}

// TestCancellationDuringEvidencePersistenceStopsBeforeConfirmation traces R10 at the durable evidence boundary.
func TestCancellationDuringEvidencePersistenceStopsBeforeConfirmation(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { calls.Add(1); w.WriteHeader(http.StatusOK) }))
	defer server.Close()
	runner, root, _, _ := runnerFor(t, server.URL, 200, 1)
	started := make(chan struct{})
	runner.deps.EvidenceFactory = func(publisher *Publisher) (evidence.Store, error) {
		base, err := publisher.EvidenceStore()
		if err != nil {
			return nil, err
		}
		return &blockingEvidenceStore{base: base, started: started}, nil
	}
	ctx, cancel := context.WithCancelCause(context.Background())
	assertCancelledBoundary(t, runner, root, started, cancel, ctx, &calls, 1, 0)
}

// TestCancellationDuringAssertionEvaluationPreservesConfirmedEvidence traces R10 after persistence.
func TestCancellationDuringAssertionEvaluationPreservesConfirmedEvidence(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { calls.Add(1); w.WriteHeader(http.StatusOK) }))
	defer server.Close()
	runner, root, _, _ := runnerFor(t, server.URL, 200, 1)
	started := make(chan struct{})
	runner.deps.Evaluator = &blockingEvaluator{started: started}
	ctx, cancel := context.WithCancelCause(context.Background())
	assertCancelledBoundary(t, runner, root, started, cancel, ctx, &calls, 1, 1)
	evidenceFiles, err := filepath.Glob(filepath.Join(root, ".featureproof", "runs", "id-1", "evidence", "*.json"))
	if err != nil || len(evidenceFiles) != 1 {
		t.Fatalf("confirmed evidence=%v err=%v", evidenceFiles, err)
	}
}
