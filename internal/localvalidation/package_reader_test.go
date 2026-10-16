package localvalidation

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/asierhr/featureProof/internal/planmanifest"
	"github.com/asierhr/featureProof/internal/planning"
	"github.com/asierhr/featureProof/internal/revisionbinding"
	"github.com/asierhr/featureProof/internal/specification"
)

func twoBatchPackageFixture(t *testing.T) string {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(200) }))
	t.Cleanup(server.Close)
	spec, plan := writeInputs(t, 201, 2)
	raw, err := os.ReadFile(spec)
	if err != nil {
		t.Fatal(err)
	}
	raw = bytes.Replace(raw, []byte("  - id: health\n    description: responds\n    target_id: deployed\n"), []byte("  - id: health\n    description: responds\n    target_id: deployed\n  - id: second\n    description: responds again\n    target_id: deployed\n"), 1)
	if err := os.WriteFile(spec, raw, 0600); err != nil {
		t.Fatal(err)
	}
	raw, err = os.ReadFile(plan)
	if err != nil {
		t.Fatal(err)
	}
	var manifest map[string]any
	if err := json.Unmarshal(raw, &manifest); err != nil {
		t.Fatal(err)
	}
	plans := manifest["plans"].([]any)
	copyRaw, err := json.Marshal(plans[0])
	if err != nil {
		t.Fatal(err)
	}
	var second map[string]any
	if err := json.Unmarshal(copyRaw, &second); err != nil {
		t.Fatal(err)
	}
	second["id"] = "second-plan"
	second["constraint_id"] = "second"
	// Repeat the step ID to exercise the plan-scoped identity boundary.
	second["steps"].([]any)[0].(map[string]any)["assertions"].([]any)[0].(map[string]any)["definition"].(map[string]any)["expected"] = float64(201)
	delete(plans[0].(map[string]any)["steps"].([]any)[0].(map[string]any)["input"].(map[string]any), "headers")
	delete(second["steps"].([]any)[0].(map[string]any)["input"].(map[string]any), "headers")
	manifest["plans"] = append(plans, second)
	raw, err = json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(plan, raw, 0600); err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	stdout, stderr := &bytes.Buffer{}, &bytes.Buffer{}
	deps := DefaultDependencies(root, stdout, stderr)
	commit := strings.Repeat("a", 40)
	deps.RevisionResolver = fakeRevision{revisionbinding.Revision{RepositoryID: "repo", CommitOID: commit}}
	deps.EnvironmentAttestor = fakeAttestation{revisionbinding.Attestation{EnvironmentID: "preview", RepositoryID: "repo", CommitOID: commit, Reference: "proof-1", ValidUntil: time.Unix(10000, 0), Verified: true}}
	deps.SafeProjector = projectorFunc(func(_ context.Context, spec specification.Specification, manifest planmanifest.Manifest, _ []planning.ValidatedPlan) (SafeProjections, error) {
		plans := make([]revisionbinding.SafeValue, len(manifest.Plans))
		for i, plan := range manifest.Plans {
			plans[i] = revisionbinding.SafeObject(map[string]revisionbinding.SafeValue{"plan_id": revisionbinding.PublicString(plan.ID), "constraint_id": revisionbinding.PublicString(plan.ConstraintID)})
		}
		return SafeProjections{Constraints: revisionbinding.SafeObject(map[string]revisionbinding.SafeValue{"feature_id": revisionbinding.PublicString(spec.Feature.ID)}), Contract: revisionbinding.SafeObject(map[string]revisionbinding.SafeValue{"target_id": revisionbinding.PublicString(spec.Target.ID)}), Configuration: revisionbinding.SafeObject(map[string]revisionbinding.SafeValue{"max_steps": revisionbinding.PublicInt(int64(spec.Limits.MaxSteps))}), Plans: plans}, nil
	})
	deps.Clock = &testClock{next: time.Unix(100, 0).UTC()}
	deps.IDs = &testIDs{}
	deps.Waiter = testWaiter{}
	deps.FinalizeTimeout = 10 * time.Second
	runner, err := NewRunner(context.Background(), Request{SpecPath: spec, PlanPath: plan, Target: server.URL, RepositoryID: "repo", Revision: "main", EnvironmentID: "preview"}, deps)
	if err != nil {
		t.Fatal(err)
	}
	if code := runner.Run(context.Background()); code != 1 {
		t.Fatalf("code=%d stdout=%s stderr=%s", code, stdout, stderr)
	}
	dir := filepath.Join(root, ".featureproof", "runs", "id-1")
	if _, err := ReadPackage(context.Background(), dir); err != nil {
		t.Fatalf("fixture invalid: %v", err)
	}
	return dir
}

func mutatePackageIndex(t *testing.T, dir string, mutate func(*PackageIndex)) {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(dir, "package.json"))
	if err != nil {
		t.Fatal(err)
	}
	var index PackageIndex
	if err := json.Unmarshal(raw, &index); err != nil {
		t.Fatal(err)
	}
	mutate(&index)
	raw, err = json.Marshal(index)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "package.json"), raw, 0600); err != nil {
		t.Fatal(err)
	}
	rewriteManifest(t, dir, index.Binding.BindingSHA256)
}

func TestOfflinePackageRejectsRehashedTopologyForgery(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(*PackageIndex)
	}{
		{"step evidence from another batch", func(p *PackageIndex) {
			var first, other *PackageFact
			for i := range p.Facts {
				if p.Facts[i].Kind == "step" {
					if first == nil {
						first = &p.Facts[i]
					} else {
						other = &p.Facts[i]
					}
				}
			}
			if first == nil || other == nil {
				t.Fatal("missing step facts")
			}
			first.EvidenceIDs = append([]string(nil), other.EvidenceIDs...)
		}},
		{"failure attributed to other batch", func(p *PackageIndex) { p.Failures[0].BatchID = "2" }},
		{"tool fact attributed to other plan", func(p *PackageIndex) {
			for i := range p.Facts {
				if p.Facts[i].Kind == "tool" {
					p.Facts[i].PlanID = "second-plan"
					break
				}
			}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := twoBatchPackageFixture(t)
			mutatePackageIndex(t, dir, tc.mutate)
			if _, err := ReadPackage(context.Background(), dir); !IsPackageError(err, "broken_reference") {
				t.Fatalf("err=%v", err)
			}
		})
	}
}

func TestOfflinePackageRejectsForgedPassingTree(t *testing.T) {
	dir := failedPackageFixture(t)
	raw, err := os.ReadFile(filepath.Join(dir, "report.json"))
	if err != nil {
		t.Fatal(err)
	}
	var report reportArtifact
	if err := json.Unmarshal(raw, &report); err != nil {
		t.Fatal(err)
	}
	report.Status = ResultPassed
	report.Constraints[0].Status = ResultPassed
	report.Constraints[0].Steps[0].Status = "passed"
	raw, err = json.Marshal(report)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "report.json"), raw, 0600); err != nil {
		t.Fatal(err)
	}
	// Keep cross-document status claims aligned so the assertion tree is decisive.
	raw, err = os.ReadFile(filepath.Join(dir, "run.json"))
	if err != nil {
		t.Fatal(err)
	}
	var run runArtifact
	if err := json.Unmarshal(raw, &run); err != nil {
		t.Fatal(err)
	}
	run.Status = ResultPassed
	run.Constraints[0].Status = ResultPassed
	raw, err = json.Marshal(run)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "run.json"), raw, 0600); err != nil {
		t.Fatal(err)
	}
	mutatePackageIndex(t, dir, func(p *PackageIndex) {
		p.Status = ResultPassed
		p.Constraints[0].Status = ResultPassed
		for i := range p.Facts {
			if p.Facts[i].Kind == "step" {
				p.Facts[i].Status = "passed"
			}
		}
	})
	if _, err := ReadPackage(context.Background(), dir); !IsPackageError(err, "broken_reference") {
		t.Fatalf("err=%v", err)
	}
}

func TestOfflinePackageRejectsSemanticReadBetweenMatchingScans(t *testing.T) {
	dir := fixturePackage(t)
	path := filepath.Join(dir, "package.json")
	original, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var index PackageIndex
	if err := json.Unmarshal(original, &index); err != nil {
		t.Fatal(err)
	}
	index.Status = ResultFailed
	altered, err := json.Marshal(index)
	if err != nil {
		t.Fatal(err)
	}
	calls := 0
	scan := func(ctx context.Context, path string) ([]PackageArtifact, error) {
		entries, err := inventoryPackage(ctx, path)
		if err != nil {
			return nil, err
		}
		calls++
		if calls == 1 {
			err = os.WriteFile(filepath.Join(path, "package.json"), altered, 0600)
		} else {
			err = os.WriteFile(filepath.Join(path, "package.json"), original, 0600)
		}
		return entries, err
	}
	if _, err := readPackageWithInventory(context.Background(), dir, scan); !IsPackageError(err, "digest_mismatch") {
		t.Fatalf("calls=%d err=%v", calls, err)
	}
}

func TestOfflinePackageRejectsRehashedDanglingEventIDs(t *testing.T) {
	for _, field := range []string{"plan_id", "constraint_id", "step_id", "tool_call_id"} {
		t.Run(field, func(t *testing.T) {
			dir := failedPackageFixture(t)
			path := filepath.Join(dir, "events.jsonl")
			raw, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			lines := bytes.Split(bytes.TrimSpace(raw), []byte{'\n'})
			mutated := false
			for i, line := range lines {
				var event map[string]any
				if err := json.Unmarshal(line, &event); err != nil {
					t.Fatal(err)
				}
				if _, ok := event[field]; !ok {
					continue
				}
				event[field] = "ghost"
				lines[i], err = json.Marshal(event)
				if err != nil {
					t.Fatal(err)
				}
				mutated = true
				break
			}
			if !mutated {
				t.Fatalf("no event with %s", field)
			}
			if err := os.WriteFile(path, append(bytes.Join(lines, []byte{'\n'}), '\n'), 0600); err != nil {
				t.Fatal(err)
			}
			indexRaw, err := os.ReadFile(filepath.Join(dir, "package.json"))
			if err != nil {
				t.Fatal(err)
			}
			var index PackageIndex
			if err := json.Unmarshal(indexRaw, &index); err != nil {
				t.Fatal(err)
			}
			rewriteManifest(t, dir, index.Binding.BindingSHA256)
			if _, err := ReadPackage(context.Background(), dir); !IsPackageError(err, "broken_reference") {
				t.Fatalf("err=%v", err)
			}
		})
	}
}

func TestOfflinePackageRejectsContradictoryAssertionGroup(t *testing.T) {
	group := reportAssertion{Operator: "all", Status: "passed", Children: []reportAssertion{{Operator: "eq", Status: "failed"}}}
	if err := checkReportAssertions([]reportAssertion{group}, nil); !IsPackageError(err, "broken_reference") {
		t.Fatalf("err=%v", err)
	}
	group.Operator = "any"
	group.Status = "failed"
	group.Children = append(group.Children, reportAssertion{Operator: "eq", Status: "passed"})
	if err := checkReportAssertions([]reportAssertion{group}, nil); !IsPackageError(err, "broken_reference") {
		t.Fatalf("err=%v", err)
	}
	for _, tc := range []struct {
		operator string
		children []reportAssertion
		want     planning.AssertionStatus
	}{
		{"all", []reportAssertion{{Operator: "eq", Status: "passed"}, {Operator: "eq", Status: "passed"}}, "passed"},
		{"all", []reportAssertion{{Operator: "eq", Status: "passed"}, {Operator: "eq", Status: "inconclusive"}}, "inconclusive"},
		{"any", []reportAssertion{{Operator: "eq", Status: "failed"}, {Operator: "eq", Status: "inconclusive"}}, "inconclusive"},
		{"any", []reportAssertion{{Operator: "eq", Status: "failed"}, {Operator: "eq", Status: "failed"}}, "failed"},
	} {
		if err := checkReportAssertions([]reportAssertion{{Operator: tc.operator, Status: tc.want, Children: tc.children}}, nil); err != nil {
			t.Fatalf("%s %s: %v", tc.operator, tc.want, err)
		}
	}
}

func rewriteManifest(t *testing.T, dir string, binding string) {
	t.Helper()
	entries, err := inventoryPackage(context.Background(), dir)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(PackageManifest{Version: 1, RunID: "id-1", BindingSHA256: binding, Artifacts: entries})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "manifest.json"), raw, 0600); err != nil {
		t.Fatal(err)
	}
}

func failedPackageFixture(t *testing.T) string {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(200) }))
	defer server.Close()
	runner, root, stdout, stderr := runnerFor(t, server.URL, 201, 1)
	if code := runner.Run(context.Background()); code != 1 {
		t.Fatalf("code=%d stdout=%s stderr=%s", code, stdout, stderr)
	}
	return filepath.Join(root, ".featureproof", "runs", "id-1")
}

func TestOfflinePackageRejectsCrossDocumentTampering(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(*PackageIndex)
		want   string
	}{
		{"missing failure", func(p *PackageIndex) { p.Failures = nil }, "invalid_schema"},
		{"missing failed node", func(p *PackageIndex) { p.Failures = []PackageFailure{} }, "broken_reference"},
		{"foreign evidence", func(p *PackageIndex) { p.Failures[0].EvidenceIDs = []string{"ghost"} }, "broken_reference"},
		{"wrong assertion", func(p *PackageIndex) { p.Failures[0].DeclarationID = "imagined" }, "broken_reference"},
		{"wrong batch", func(p *PackageIndex) { p.Failures[0].BatchID = "99" }, "broken_reference"},
		{"invented check", func(p *PackageIndex) { p.Failures[0].CheckID = "model-only" }, "broken_reference"},
		{"invented scenario", func(p *PackageIndex) { p.Reproduction[0].Scenario = "model-only" }, "broken_reference"},
		{"wrong fact", func(p *PackageIndex) { p.Facts[0].StepID = "imagined" }, "broken_reference"},
		{"wrong reproduction commit", func(p *PackageIndex) { p.Reproduction[0].CommitOID = strings.Repeat("b", 40) }, "broken_reference"},
		{"wrong reproduction plan", func(p *PackageIndex) { p.Reproduction[0].PlanSHA256[0] = strings.Repeat("b", 64) }, "broken_reference"},
		{"unsafe present value", func(p *PackageIndex) { p.Failures[0].Observed = SafeValue{State: "present", Value: "SECRET"} }, "invalid_schema"},
		{"duplicate failure", func(p *PackageIndex) { p.Failures = append(p.Failures, p.Failures[0]) }, "broken_reference"},
		{"duplicate fact", func(p *PackageIndex) { p.Facts = append(p.Facts, p.Facts[0]) }, "broken_reference"},
		{"wrong tool status", func(p *PackageIndex) {
			for i := range p.Facts {
				if p.Facts[i].Kind == "tool" {
					p.Facts[i].Status = "passed"
					break
				}
			}
		}, "broken_reference"},
		{"wrong evidence path", func(p *PackageIndex) { p.Evidence[0].Path = "evidence/other.json" }, "broken_reference"},
		{"wrong reproduction step", func(p *PackageIndex) { p.Reproduction[0].StepIDs = []string{"imagined"} }, "broken_reference"},
		{"missing reproduction", func(p *PackageIndex) { p.Reproduction = []PackageReproduction{} }, "broken_reference"},
		{"foreign binding", func(p *PackageIndex) { p.Binding.CommitOID = strings.Repeat("c", 40) }, "binding_integrity"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := failedPackageFixture(t)
			raw, err := os.ReadFile(filepath.Join(dir, "package.json"))
			if err != nil {
				t.Fatal(err)
			}
			var index PackageIndex
			if err := json.Unmarshal(raw, &index); err != nil {
				t.Fatal(err)
			}
			tc.mutate(&index)
			raw, err = json.Marshal(index)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(dir, "package.json"), raw, 0600); err != nil {
				t.Fatal(err)
			}
			rewriteManifest(t, dir, index.Binding.BindingSHA256)
			if _, err := ReadPackage(context.Background(), dir); !IsPackageError(err, tc.want) {
				t.Fatalf("err=%v", err)
			}
		})
	}
}

type cancelAfterChecks struct{ remaining int }

func (c *cancelAfterChecks) Deadline() (time.Time, bool) { return time.Time{}, false }
func (c *cancelAfterChecks) Done() <-chan struct{}       { return nil }
func (c *cancelAfterChecks) Value(any) any               { return nil }
func (c *cancelAfterChecks) Err() error {
	c.remaining--
	if c.remaining <= 0 {
		return context.Canceled
	}
	return nil
}

func fixturePackage(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	binding := testBindingForRun(t, "run")
	write := func(name string, value any) {
		t.Helper()
		raw, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, name), raw, 0600); err != nil {
			t.Fatal(err)
		}
	}
	write("run.json", runArtifact{ID: "run", Binding: &binding, Status: ResultCancelled, Constraints: []runConstraintArtifact{}})
	write("report.json", reportArtifact{RunID: "run", Binding: &binding, Status: ResultCancelled, Constraints: []reportConstraint{}})
	write("plan.json", map[string]any{"version": 1})
	write("package.json", PackageIndex{Version: 1, RunID: "run", Binding: binding, PlanBindings: []revisionbinding.PlanBinding{}, Status: ResultCancelled, Constraints: []PackageConstraint{}, Failures: []PackageFailure{}, Facts: []PackageFact{}, Evidence: []PackageEvidence{}, Reproduction: []PackageReproduction{}})
	if err := os.WriteFile(filepath.Join(dir, "report.md"), []byte("# report"), 0600); err != nil {
		t.Fatal(err)
	}
	events := `{"sequence":1,"type":"run_created","run_id":"run","binding_sha256":"` + binding.BindingSHA256 + `"}` + "\n" + `{"sequence":2,"type":"binding_confirmed","run_id":"run","binding_sha256":"` + binding.BindingSHA256 + `"}` + "\n" + `{"sequence":3,"type":"run_cancelled","run_id":"run","binding_sha256":"` + binding.BindingSHA256 + `","status":"cancelled"}` + "\n"
	if err := os.WriteFile(filepath.Join(dir, "events.jsonl"), []byte(events), 0600); err != nil {
		t.Fatal(err)
	}
	entries, err := inventoryPackage(context.Background(), dir)
	if err != nil {
		t.Fatal(err)
	}
	write("manifest.json", PackageManifest{Version: 1, RunID: "run", BindingSHA256: binding.BindingSHA256, Artifacts: entries})
	return dir
}

func TestOfflinePackageInventoryAndMutation(t *testing.T) {
	dir := fixturePackage(t)
	index, err := ReadPackage(context.Background(), dir)
	if err != nil || index.Status != ResultCancelled {
		t.Fatalf("index=%+v err=%v", index, err)
	}
	if err := os.WriteFile(filepath.Join(dir, "extra.json"), []byte("{}"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadPackage(context.Background(), dir); !IsPackageError(err, "extra_artifact") {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(dir, "extra.json")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "report.md"), []byte("changed"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadPackage(context.Background(), dir); !IsPackageError(err, "digest_mismatch") {
		t.Fatal(err)
	}
}

func TestOfflinePackageRejectsUnsafePathsAndManifestSelfEntry(t *testing.T) {
	for _, path := range []string{"../escape", "/absolute", "C:/drive", "evidence//id.json", "a/./b", "a\\b"} {
		if canonicalArtifactPath(path) {
			t.Fatalf("accepted %q", path)
		}
	}
	dir := fixturePackage(t)
	raw, err := os.ReadFile(filepath.Join(dir, "manifest.json"))
	if err != nil {
		t.Fatal(err)
	}
	var manifest PackageManifest
	if err := json.Unmarshal(raw, &manifest); err != nil {
		t.Fatal(err)
	}
	manifest.Artifacts = append(manifest.Artifacts, PackageArtifact{Path: "manifest.json", SHA256: strings.Repeat("a", 64)})
	changed, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "manifest.json"), changed, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadPackage(context.Background(), dir); err == nil {
		t.Fatal("manifest self-entry accepted")
	}
}

func TestOfflinePackageRejectsSymlinkAndCancellation(t *testing.T) {
	dir := fixturePackage(t)
	if err := os.Symlink(filepath.Join(dir, "report.md"), filepath.Join(dir, "link.json")); err == nil {
		if _, err := ReadPackage(context.Background(), dir); !IsPackageError(err, "invalid_path") {
			t.Fatal(err)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := ReadPackage(ctx, dir); err != context.Canceled {
		t.Fatal(err)
	}
}

func TestOfflinePackageRejectsDetectableHardLink(t *testing.T) {
	dir := fixturePackage(t)
	outside := filepath.Join(t.TempDir(), "alias")
	if err := os.Link(filepath.Join(dir, "report.md"), outside); err != nil {
		t.Skipf("hard links unavailable: %v", err)
	}
	if _, err := ReadPackage(context.Background(), dir); !IsPackageError(err, "invalid_path") {
		t.Fatal(err)
	}
}

func TestOfflinePackageRejectsForgedReferenceAfterRehash(t *testing.T) {
	dir := fixturePackage(t)
	raw, err := os.ReadFile(filepath.Join(dir, "package.json"))
	if err != nil {
		t.Fatal(err)
	}
	var index PackageIndex
	if err := json.Unmarshal(raw, &index); err != nil {
		t.Fatal(err)
	}
	index.Facts = []PackageFact{{Kind: "tool", StepID: "imagined", Status: "observed", EvidenceIDs: []string{"ghost"}}}
	raw, err = json.Marshal(index)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "package.json"), raw, 0600); err != nil {
		t.Fatal(err)
	}
	entries, err := inventoryPackage(context.Background(), dir)
	if err != nil {
		t.Fatal(err)
	}
	manifest := PackageManifest{Version: 1, RunID: "run", BindingSHA256: index.Binding.BindingSHA256, Artifacts: entries}
	raw, err = json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "manifest.json"), raw, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadPackage(context.Background(), dir); !IsPackageError(err, "broken_reference") {
		t.Fatal(err)
	}
}

func TestPackageInventoryCancellationDuringHash(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "large.json"), make([]byte, 1<<20), 0600); err != nil {
		t.Fatal(err)
	}
	ctx := &cancelAfterChecks{remaining: 6}
	if _, err := inventoryPackage(ctx, dir); err != context.Canceled {
		t.Fatalf("err=%v", err)
	}
}

func TestOfflinePackageRejectsManifestAndFileBounds(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(string)
		want   string
	}{
		{"missing file", func(dir string) { _ = os.Remove(filepath.Join(dir, "report.md")) }, "missing_artifact"},
		{"oversized manifest", func(dir string) {
			_ = os.WriteFile(filepath.Join(dir, "manifest.json"), make([]byte, maxManifestBytes+1), 0600)
		}, "limit_exceeded"},
		{"manifest duplicate key", func(dir string) {
			raw, _ := os.ReadFile(filepath.Join(dir, "manifest.json"))
			raw = append([]byte(`{"version":1,`), raw[1:]...)
			_ = os.WriteFile(filepath.Join(dir, "manifest.json"), raw, 0600)
		}, "invalid_schema"},
		{"extra directory", func(dir string) { _ = os.Mkdir(filepath.Join(dir, "unexpected"), 0700) }, "extra_artifact"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := fixturePackage(t)
			tc.change(dir)
			if _, err := ReadPackage(context.Background(), dir); !IsPackageError(err, tc.want) {
				t.Fatal(err)
			}
		})
	}
}

func TestOfflinePackageRejectsRehashedArtifactTampering(t *testing.T) {
	for _, tc := range []struct{ name, file, from, to, want string }{
		{"run status", "run.json", `"status": "failed"`, `"status": "passed"`, "binding_integrity"},
		{"report status", "report.json", `"status": "failed"`, `"status": "passed"`, "binding_integrity"},
		{"plan step", "plan.json", `"id": "request"`, `"id": "other"`, "broken_reference"},
		{"event terminal", "events.jsonl", `"status":"failed"`, `"status":"passed"`, "binding_integrity"},
		{"index version", "package.json", `"version": 1`, `"version": 2`, "unsupported_version"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := failedPackageFixture(t)
			raw, err := os.ReadFile(filepath.Join(dir, tc.file))
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(string(raw), tc.from) {
				t.Fatalf("missing mutation source %q", tc.from)
			}
			if tc.file == "events.jsonl" {
				raw = []byte(strings.ReplaceAll(string(raw), tc.from, tc.to))
			} else {
				raw = []byte(strings.Replace(string(raw), tc.from, tc.to, 1))
			}
			if err := os.WriteFile(filepath.Join(dir, tc.file), raw, 0600); err != nil {
				t.Fatal(err)
			}
			indexRaw, err := os.ReadFile(filepath.Join(dir, "package.json"))
			if err != nil {
				t.Fatal(err)
			}
			var index PackageIndex
			if err := json.Unmarshal(indexRaw, &index); err != nil {
				t.Fatal(err)
			}
			rewriteManifest(t, dir, index.Binding.BindingSHA256)
			if _, err := ReadPackage(context.Background(), dir); !IsPackageError(err, tc.want) {
				t.Fatal(err)
			}
		})
	}
}

func TestOfflinePackageRejectsRehashedEvidenceTampering(t *testing.T) {
	for _, tc := range []struct {
		name, field string
		value       any
		want        string
	}{
		{"wrong id", "ID", "other", "binding_integrity"},
		{"foreign run", "RunID", "foreign", "binding_integrity"},
		{"foreign binding", "BindingSHA256", strings.Repeat("b", 64), "binding_integrity"},
		{"wrong internal hash", "IntegrityHash", strings.Repeat("b", 64), "digest_mismatch"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := failedPackageFixture(t)
			files, err := filepath.Glob(filepath.Join(dir, "evidence", "*.json"))
			if err != nil || len(files) != 1 {
				t.Fatalf("files=%v err=%v", files, err)
			}
			raw, err := os.ReadFile(files[0])
			if err != nil {
				t.Fatal(err)
			}
			var record map[string]any
			if err := json.Unmarshal(raw, &record); err != nil {
				t.Fatal(err)
			}
			record[tc.field] = tc.value
			raw, err = json.Marshal(record)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(files[0], raw, 0600); err != nil {
				t.Fatal(err)
			}
			indexRaw, err := os.ReadFile(filepath.Join(dir, "package.json"))
			if err != nil {
				t.Fatal(err)
			}
			var index PackageIndex
			if err := json.Unmarshal(indexRaw, &index); err != nil {
				t.Fatal(err)
			}
			rewriteManifest(t, dir, index.Binding.BindingSHA256)
			if _, err := ReadPackage(context.Background(), dir); !IsPackageError(err, tc.want) {
				t.Fatal(err)
			}
		})
	}
}

func TestOfflinePackageRejectsDisappearingFileBetweenInventoryPasses(t *testing.T) {
	dir := fixturePackage(t)
	calls := 0
	scan := func(ctx context.Context, path string) ([]PackageArtifact, error) {
		items, err := inventoryPackage(ctx, path)
		if err != nil {
			return nil, err
		}
		calls++
		if calls == 2 {
			return items[:len(items)-1], nil
		}
		return items, nil
	}
	index, err := readPackageWithInventory(context.Background(), dir, scan)
	if !IsPackageError(err, "missing_artifact") || index.RunID != "" || calls != 2 {
		t.Fatalf("index=%+v err=%v calls=%d", index, err, calls)
	}
}

func TestOfflinePackageRejectsManifestReplacementBetweenInventoryPasses(t *testing.T) {
	dir := fixturePackage(t)
	calls := 0
	scan := func(ctx context.Context, path string) ([]PackageArtifact, error) {
		items, err := inventoryPackage(ctx, path)
		if err != nil {
			return nil, err
		}
		calls++
		if calls == 2 {
			raw, err := os.ReadFile(filepath.Join(path, "manifest.json"))
			if err != nil {
				return nil, err
			}
			if err := os.WriteFile(filepath.Join(path, "manifest.json"), append(raw, ' '), 0600); err != nil {
				return nil, err
			}
		}
		return items, nil
	}
	index, err := readPackageWithInventory(context.Background(), dir, scan)
	if !IsPackageError(err, "digest_mismatch") || index.RunID != "" || calls != 2 {
		t.Fatalf("index=%+v err=%v calls=%d", index, err, calls)
	}
}

func TestOfflinePackageRejectsForgedManifestEntries(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(*PackageManifest)
		want   string
	}{
		{"unsupported version", func(m *PackageManifest) { m.Version = 2 }, "unsupported_version"},
		{"traversal", func(m *PackageManifest) { m.Artifacts[0].Path = "../outside" }, "invalid_path"},
		{"absolute path", func(m *PackageManifest) { m.Artifacts[0].Path = "C:/outside" }, "invalid_path"},
		{"duplicate path", func(m *PackageManifest) { m.Artifacts[1].Path = m.Artifacts[0].Path }, "invalid_path"},
		{"wrong size", func(m *PackageManifest) { m.Artifacts[0].SizeBytes++ }, "digest_mismatch"},
		{"wrong digest", func(m *PackageManifest) { m.Artifacts[0].SHA256 = strings.Repeat("a", 64) }, "digest_mismatch"},
		{"missing entry", func(m *PackageManifest) { m.Artifacts = m.Artifacts[1:] }, "extra_artifact"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := fixturePackage(t)
			raw, err := os.ReadFile(filepath.Join(dir, "manifest.json"))
			if err != nil {
				t.Fatal(err)
			}
			var manifest PackageManifest
			if err := json.Unmarshal(raw, &manifest); err != nil {
				t.Fatal(err)
			}
			tc.mutate(&manifest)
			raw, err = json.Marshal(manifest)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(dir, "manifest.json"), raw, 0600); err != nil {
				t.Fatal(err)
			}
			if _, err := ReadPackage(context.Background(), dir); !IsPackageError(err, tc.want) {
				t.Fatal(err)
			}
		})
	}
}

func TestPackageInventoryRejectsOversizedAndInvalidEntries(t *testing.T) {
	dir := t.TempDir()
	file, err := os.Create(filepath.Join(dir, "large.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := file.Truncate(maxArtifactBytes + 1); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := inventoryPackage(context.Background(), dir); !IsPackageError(err, "limit_exceeded") {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(dir, "large.json")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "bad name.json"), []byte("x"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := inventoryPackage(context.Background(), dir); !IsPackageError(err, "invalid_path") {
		t.Fatal(err)
	}
}

func TestOfflinePackageRejectsEvidenceInventoryAndSchemaErrors(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(string)
		want   string
	}{
		{"undeclared evidence", func(dir string) {
			_ = os.WriteFile(filepath.Join(dir, "evidence", "extra.json"), []byte(`{}`), 0600)
			raw, _ := os.ReadFile(filepath.Join(dir, "package.json"))
			var p PackageIndex
			_ = json.Unmarshal(raw, &p)
			rewriteManifest(t, dir, p.Binding.BindingSHA256)
		}, "extra_artifact"},
		{"missing evidence file", func(dir string) {
			files, _ := filepath.Glob(filepath.Join(dir, "evidence", "*.json"))
			_ = os.Remove(files[0])
		}, "missing_artifact"},
		{"duplicate package key", func(dir string) {
			raw, _ := os.ReadFile(filepath.Join(dir, "package.json"))
			raw = append([]byte(`{"version":1,`), raw[1:]...)
			_ = os.WriteFile(filepath.Join(dir, "package.json"), raw, 0600)
			manifest, _ := os.ReadFile(filepath.Join(dir, "manifest.json"))
			var m PackageManifest
			_ = json.Unmarshal(manifest, &m)
			rewriteManifest(t, dir, m.BindingSHA256)
		}, "invalid_schema"},
		{"evidence symlink", func(dir string) {
			files, _ := filepath.Glob(filepath.Join(dir, "evidence", "*.json"))
			_ = os.Remove(files[0])
			if err := os.Symlink(filepath.Join(dir, "report.md"), files[0]); err != nil {
				t.Skipf("symlinks unavailable: %v", err)
			}
		}, "invalid_path"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := failedPackageFixture(t)
			tc.change(dir)
			if _, err := ReadPackage(context.Background(), dir); !IsPackageError(err, tc.want) {
				t.Fatal(err)
			}
		})
	}
}

func TestOfflineBoundedReadRejectsChangedOrUnsafeFiles(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "safe.json"), []byte("123456789"), 0600); err != nil {
		t.Fatal(err)
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	for _, tc := range []struct {
		name  string
		ctx   context.Context
		path  string
		limit int64
		want  string
	}{
		{"missing", context.Background(), "missing.json", 100, "missing_artifact"},
		{"invalid path", context.Background(), "../outside", 100, "invalid_path"},
		{"size limit", context.Background(), "safe.json", 4, "limit_exceeded"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := readPackageFile(tc.ctx, root, tc.path, tc.limit); !IsPackageError(err, tc.want) {
				t.Fatal(err)
			}
		})
	}
	if err := os.Mkdir(filepath.Join(dir, "folder"), 0700); err != nil {
		t.Fatal(err)
	}
	if _, err := readPackageFile(context.Background(), root, "folder", 100); !IsPackageError(err, "invalid_path") {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(dir, "safe.json"), filepath.Join(dir, "link.json")); err == nil {
		if _, err := readPackageFile(context.Background(), root, "link.json", 100); !IsPackageError(err, "invalid_path") {
			t.Fatal(err)
		}
	}
	if err := os.Link(filepath.Join(dir, "safe.json"), filepath.Join(t.TempDir(), "alias")); err == nil {
		if _, err := readPackageFile(context.Background(), root, "safe.json", 100); !IsPackageError(err, "invalid_path") {
			t.Fatal(err)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := readPackageFile(ctx, root, "safe.json", 100); err != context.Canceled {
		t.Fatal(err)
	}
}

func TestOfflinePackageRejectsForgedReportTopologyAndReferences(t *testing.T) {
	for _, tc := range []struct {
		name, file, want string
		change           func(map[string]any)
	}{
		{"run constraint status", "run.json", "broken_reference", func(m map[string]any) { m["constraints"].([]any)[0].(map[string]any)["status"] = "passed" }},
		{"report assertion evidence", "report.json", "broken_reference", func(m map[string]any) {
			node := m["constraints"].([]any)[0].(map[string]any)["steps"].([]any)[0].(map[string]any)["assertions"].([]any)[0].(map[string]any)
			node["evidence_ids"] = []any{"ghost"}
		}},
		{"report missing plan", "report.json", "binding_integrity", func(m map[string]any) { m["plan_bindings"] = []any{} }},
		{"plan wrong version", "plan.json", "invalid_schema", func(m map[string]any) { m["version"] = 2 }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := failedPackageFixture(t)
			raw, err := os.ReadFile(filepath.Join(dir, tc.file))
			if err != nil {
				t.Fatal(err)
			}
			var doc map[string]any
			if err := json.Unmarshal(raw, &doc); err != nil {
				t.Fatal(err)
			}
			tc.change(doc)
			raw, err = json.Marshal(doc)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(dir, tc.file), raw, 0600); err != nil {
				t.Fatal(err)
			}
			indexRaw, err := os.ReadFile(filepath.Join(dir, "package.json"))
			if err != nil {
				t.Fatal(err)
			}
			var index PackageIndex
			if err := json.Unmarshal(indexRaw, &index); err != nil {
				t.Fatal(err)
			}
			rewriteManifest(t, dir, index.Binding.BindingSHA256)
			if _, err := ReadPackage(context.Background(), dir); !IsPackageError(err, tc.want) {
				t.Fatal(err)
			}
		})
	}
}
