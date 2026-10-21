package contractreview

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/asierhr/featureProof/internal/contractcatalog"
)

// reconcilingPublisher simulates an atomic publication whose process loses the following store commit.
type reconcilingPublisher struct {
	target, digest string
	calls          int
}

// Publish records the first durable effect and rejects later no-replace attempts like a real publisher.
func (p *reconcilingPublisher) Publish(_ context.Context, request PublishRequest, _ []byte, digest string) (string, error) {
	p.calls++
	if p.digest != "" {
		return "", typedError(CategoryTargetExists, "publish.target", nil)
	}
	p.target = request.Target
	p.digest = digest
	return request.Target, nil
}

// Inspect lets the service reconcile the exact already-published digest.
func (p *reconcilingPublisher) Inspect(_ context.Context, _ string, attemptID string) (string, string, error) {
	if p.digest == "" || attemptID == "" {
		return "", "", errors.New("missing")
	}
	return p.target, p.digest, nil
}

// failCommitStore injects one crash-equivalent failure after publication and before export commit.
type failCommitStore struct {
	Store
	fail bool
}

// CompareAndSwap forwards every transition except the configured export commit crash point.
func (s *failCommitStore) CompareAndSwap(ctx context.Context, principal string, expected uint64, operation, key, digest string, next ReviewRecord) (ReviewRecord, error) {
	if operation == "export.commit" && s.fail {
		s.fail = false
		return ReviewRecord{}, typedError(CategoryUnavailable, "store.crash", nil)
	}
	return s.Store.CompareAndSwap(ctx, principal, expected, operation, key, digest, next)
}

// mutablePolicyAuthority lets tests prove current policy drift invalidates durable authority.
type mutablePolicyAuthority struct{ digest string }

// CurrentPolicyDigest returns the test-controlled current policy identity.
func (p *mutablePolicyAuthority) CurrentPolicyDigest(context.Context, string, string) (string, error) {
	return p.digest, nil
}

// TestRevalidationRejectsCatalogMutationWithStaleIdentity proves the catalog bytes are checked independently of the claimed identity.
func TestRevalidationRejectsCatalogMutationWithStaleIdentity(t *testing.T) {
	store := NewMemoryStore()
	service, resolver, _, _ := serviceFixture(t, store)
	view := createServiceReview(t, service)
	mutated := fixtureCatalog(t, "a-source", []contractcatalog.OperationDescriptor{fixtureOperation("a-source", strings.Repeat("a", 64), "/different", "different", "200")}, contractcatalog.CompletenessComplete, nil, contractcatalog.OmissionCounts{})
	resolver.mu.Lock()
	resolver.candidate.Catalog = mutated
	resolver.mu.Unlock()
	_, err := service.Decide(context.Background(), DecisionCommand{PrincipalID: "actor", ReviewID: view.ID, Decision: DecisionInput{ExpectedRevision: 0, Whole: WholeAccept, IdempotencyKey: "mutated"}})
	errorCategory(t, err, CategoryInvalidated)
}

// TestPolicyDriftInvalidatesBeforeDecisionCAS proves policy authority is checked at use time.
func TestPolicyDriftInvalidatesBeforeDecisionCAS(t *testing.T) {
	service, _, _, _ := serviceFixture(t, NewMemoryStore())
	policy := &mutablePolicyAuthority{digest: strings.Repeat("d", 64)}
	service.Policy = policy
	view := createServiceReview(t, service)
	policy.digest = strings.Repeat("e", 64)
	_, err := service.Decide(context.Background(), DecisionCommand{PrincipalID: "actor", ReviewID: view.ID, Decision: DecisionInput{ExpectedRevision: 0, Whole: WholeAccept, IdempotencyKey: "policy-drift"}})
	errorCategory(t, err, CategoryInvalidated)
}

// TestDerivedBaselineRequiresApprovedReference prevents a caller from self-asserting approval with a boolean.
func TestDerivedBaselineRequiresApprovedReference(t *testing.T) {
	catalog := fixtureCatalog(t, "a-source", nil, contractcatalog.CompletenessComplete, nil, contractcatalog.OmissionCounts{})
	baseline := baselineFor(catalog)
	baseline.Kind = "approved_projection"
	baseline.ApprovedReference = nil
	_, err := CreateArtifact(context.Background(), candidateFor(catalog), baseline, reviewLimits())
	errorCategory(t, err, CategoryIncompatibleBaseline)
}

// TestArtifactBindsSeparateCandidateAndBaselineManifests proves every material source is auditable and digest-bound.
func TestArtifactBindsSeparateCandidateAndBaselineManifests(t *testing.T) {
	candidate := fixtureCatalog(t, "a-source", nil, contractcatalog.CompletenessComplete, nil, contractcatalog.OmissionCounts{})
	baseline := fixtureCatalog(t, "b-source", nil, contractcatalog.CompletenessComplete, nil, contractcatalog.OmissionCounts{})
	artifact, err := CreateArtifact(context.Background(), candidateFor(candidate), baselineFor(baseline), reviewLimits())
	if err != nil {
		t.Fatal(err)
	}
	if len(artifact.Manifests.Candidate) != 1 || artifact.Manifests.Candidate[0].ID != "a-source" || len(artifact.Manifests.Baseline) != 1 || artifact.Manifests.Baseline[0].ID != "b-source" {
		t.Fatalf("incomplete manifests: %#v", artifact.Manifests)
	}
}

// TestAggregateSchemaAndSourceLimitsFailWithoutArtifact proves budgets include both manifests and recursive schemas.
func TestAggregateSchemaAndSourceLimitsFailWithoutArtifact(t *testing.T) {
	op := fixtureOperation("a-source", strings.Repeat("a", 64), "/deep", "deep", "200")
	op.Parameters = []contractcatalog.Parameter{{Name: "p", In: "query", Schema: &contractcatalog.Schema{Type: "array", Items: &contractcatalog.Schema{Type: "array", Items: &contractcatalog.Schema{Type: "string"}}}}}
	candidate := fixtureCatalog(t, "a-source", []contractcatalog.OperationDescriptor{op}, contractcatalog.CompletenessComplete, nil, contractcatalog.OmissionCounts{})
	baseline := fixtureCatalog(t, "b-source", nil, contractcatalog.CompletenessComplete, nil, contractcatalog.OmissionCounts{})
	limits := reviewLimits()
	limits.MaxSources = 1
	_, err := CreateArtifact(context.Background(), candidateFor(candidate), baselineFor(baseline), limits)
	errorCategory(t, err, CategoryLimitExceeded)
	limits = reviewLimits()
	limits.MaxSchemaDepth = 2
	_, err = CreateArtifact(context.Background(), candidateFor(candidate), baselineFor(baseline), limits)
	errorCategory(t, err, CategoryLimitExceeded)
	limits = reviewLimits()
	limits.MaxSchemaNodes = 1
	_, err = CreateArtifact(context.Background(), candidateFor(candidate), baselineFor(baseline), limits)
	errorCategory(t, err, CategoryLimitExceeded)
	diagnosticCatalog := fixtureCatalog(t, "a-source", []contractcatalog.OperationDescriptor{op}, contractcatalog.CompletenessComplete, []contractcatalog.SourceDiagnostic{{Code: "bounded"}}, contractcatalog.OmissionCounts{})
	limits = reviewLimits()
	limits.MaxDiagnostics = 0
	if limits.Validate() == nil {
		t.Fatal("nonpositive diagnostic budget accepted")
	}
	limits = reviewLimits()
	limits.MaxDiagnostics = 1
	_, err = CreateArtifact(context.Background(), candidateFor(diagnosticCatalog), baselineFor(diagnosticCatalog), limits)
	errorCategory(t, err, CategoryLimitExceeded)
	provenanceOp := deepClone(op)
	provenanceOp.Provenance = append(provenanceOp.Provenance, provenanceOp.Provenance...)
	provenanceCatalog := fixtureCatalog(t, "a-source", []contractcatalog.OperationDescriptor{provenanceOp}, contractcatalog.CompletenessComplete, nil, contractcatalog.OmissionCounts{})
	limits = reviewLimits()
	limits.MaxProvenance = 1
	_, err = CreateArtifact(context.Background(), candidateFor(provenanceCatalog), baselineFor(baseline), limits)
	errorCategory(t, err, CategoryLimitExceeded)
}

// TestNoChangeAcceptAllCreatesApproval proves an identical authoritative baseline can be explicitly approved.
func TestNoChangeAcceptAllCreatesApproval(t *testing.T) {
	catalog := fixtureCatalog(t, "a-source", []contractcatalog.OperationDescriptor{fixtureOperation("a-source", strings.Repeat("a", 64), "/same", "same", "200")}, contractcatalog.CompletenessComplete, nil, contractcatalog.OmissionCounts{})
	artifact, err := CreateArtifact(context.Background(), candidateFor(catalog), baselineFor(catalog), reviewLimits())
	if err != nil {
		t.Fatal(err)
	}
	_, state, projection, err := ReduceDecision(context.Background(), artifact, 0, nil, DecisionInput{ExpectedRevision: 0, Whole: WholeAccept, IdempotencyKey: "same"}, "actor", fixedClock{}.Now().UTC(), candidateFor(catalog), baselineFor(catalog), strings.Repeat("d", 64))
	if err != nil || state != StateApproved || projection == nil {
		t.Fatalf("state=%s projection=%v err=%v", state, projection != nil, err)
	}
}

// TestDiffEmitsAtomicParameterResponseAndSchemeUnits proves selective choices do not replace sibling structures.
func TestDiffEmitsAtomicParameterResponseAndSchemeUnits(t *testing.T) {
	baseOp := fixtureOperation("a-source", strings.Repeat("a", 64), "/atomic", "atomic", "200")
	baseOp.Parameters = []contractcatalog.Parameter{{Name: "keep", In: "query", Schema: &contractcatalog.Schema{Type: "string"}}, {Name: "change", In: "query", Schema: &contractcatalog.Schema{Type: "string"}}}
	candidateOp := deepClone(baseOp)
	candidateOp.Parameters[1].Schema.Type = "integer"
	candidateOp.Parameters = append(candidateOp.Parameters, contractcatalog.Parameter{Name: "added", In: "header", Schema: &contractcatalog.Schema{Type: "string"}})
	candidateOp.Responses["201"] = contractcatalog.Response{Content: map[string]contractcatalog.MediaType{"application/json": {Schema: &contractcatalog.Schema{Type: "object"}}}}
	candidateOp.SecuritySchemes = map[string]contractcatalog.SecurityScheme{"oauth": {Name: "oauth", Type: "oauth2"}}
	candidateOp.Security = []contractcatalog.SecurityRequirement{{Schemes: map[string][]string{"oauth": {"read"}}}}
	base := fixtureCatalog(t, "a-source", []contractcatalog.OperationDescriptor{baseOp}, contractcatalog.CompletenessComplete, nil, contractcatalog.OmissionCounts{})
	candidate := fixtureCatalog(t, "a-source", []contractcatalog.OperationDescriptor{candidateOp}, contractcatalog.CompletenessComplete, nil, contractcatalog.OmissionCounts{})
	artifact, err := CreateArtifact(context.Background(), candidateFor(candidate), baselineFor(base), reviewLimits())
	if err != nil {
		t.Fatal(err)
	}
	paths := map[string]ReviewItem{}
	for _, item := range artifact.Items {
		paths[item.Path] = item
	}
	if len(paths) < 4 {
		t.Fatalf("coarse diff: %#v", paths)
	}
	var parameterItem ReviewItem
	for path, item := range paths {
		if strings.HasPrefix(path, "parameters/"+encodePathSegment("query\x00change")+"/schema/type") {
			parameterItem = item
		}
	}
	if parameterItem.ID == "" {
		t.Fatal("changed parameter was not atomic")
	}
	_, state, projection, err := ReduceDecision(context.Background(), artifact, 0, nil, DecisionInput{ExpectedRevision: 0, Items: []ItemDecision{{ItemID: parameterItem.ID, Value: DecisionAccept}}, IdempotencyKey: "one"}, "actor", fixedClock{}.Now().UTC(), candidateFor(candidate), baselineFor(base), strings.Repeat("d", 64))
	if err != nil || state != StatePartiallyDecided || projection != nil {
		t.Fatalf("partial decision state=%s err=%v", state, err)
	}
	securityItem := paths["security"]
	if len(securityItem.Dependencies) == 0 {
		t.Fatal("security item lacks scheme dependency")
	}
	_, _, _, err = ReduceDecision(context.Background(), artifact, 0, nil, DecisionInput{ExpectedRevision: 0, Items: []ItemDecision{{ItemID: securityItem.ID, Value: DecisionAccept}}, IdempotencyKey: "dependency"}, "actor", fixedClock{}.Now().UTC(), candidateFor(candidate), baselineFor(base), strings.Repeat("d", 64))
	if err == nil {
		t.Fatal("open dependency set accepted")
	}
	_, state, projection, err = ReduceDecision(context.Background(), artifact, 0, nil, DecisionInput{ExpectedRevision: 0, Whole: WholeAccept, IdempotencyKey: "all-atomic"}, "actor", fixedClock{}.Now().UTC(), candidateFor(candidate), baselineFor(base), strings.Repeat("d", 64))
	if err != nil || state != StateApproved || projection == nil || len(projection.Operations) != 1 {
		t.Fatalf("atomic whole projection failed: state=%s projection=%v err=%v", state, projection != nil, err)
	}
}

// TestAtomicProjectionAppliesEveryStructuralUnit proves the reducer reconstructs a rich candidate only by atomic paths.
func TestAtomicProjectionAppliesEveryStructuralUnit(t *testing.T) {
	minimumOne, minimumTwo := 1, 2
	beforeSchema := &contractcatalog.Schema{ID: "before", Type: "object", Format: "old", Required: []string{"old"}, Enum: []any{"old"}, MinProperties: &minimumOne, Properties: map[string]*contractcatalog.Schema{"value": {Type: "string"}, "removed": {Type: "string"}}, Items: &contractcatalog.Schema{Type: "string"}, AllOf: []*contractcatalog.Schema{{Type: "string"}}}
	afterSchema := &contractcatalog.Schema{ID: "after", Type: "array", Format: "new", Required: []string{"new"}, Enum: []any{"new"}, MinProperties: &minimumTwo, Nullable: true, ReadOnly: true, WriteOnly: true, Properties: map[string]*contractcatalog.Schema{"value": {Type: "integer"}, "added": {Type: "boolean"}}, Items: &contractcatalog.Schema{Type: "integer"}, AllOf: []*contractcatalog.Schema{{Type: "integer"}, {Type: "boolean"}}}
	before := contractcatalog.OperationDescriptor{Key: contractcatalog.OperationKey{Method: "POST", Path: "/atomic-rich"}, LogicalID: "before", Parameters: []contractcatalog.Parameter{{Name: "query", In: "query", Schema: deepClone(beforeSchema), Content: map[string]contractcatalog.MediaType{"application/json": {Schema: deepClone(beforeSchema)}}}}, RequestBody: &contractcatalog.RequestBody{Content: map[string]contractcatalog.MediaType{"application/json": {Schema: deepClone(beforeSchema)}}}, Responses: map[string]contractcatalog.Response{"200": {Headers: map[string]contractcatalog.Header{"X-Value": {Schema: deepClone(beforeSchema)}}, Content: map[string]contractcatalog.MediaType{"application/json": {Schema: deepClone(beforeSchema)}}}}, Security: []contractcatalog.SecurityRequirement{{Schemes: map[string][]string{"old": {"read"}}}}, SecuritySchemes: map[string]contractcatalog.SecurityScheme{"old": {Name: "old", Type: "apiKey"}}, Confidence: contractcatalog.ConfidenceLow, Completeness: contractcatalog.CompletenessPartial}
	after := deepClone(before)
	after.LogicalID = "after"
	after.Parameters[0].Required = true
	after.Parameters[0].Schema = deepClone(afterSchema)
	after.Parameters[0].Content["application/json"] = contractcatalog.MediaType{Schema: deepClone(afterSchema)}
	after.RequestBody.Required = true
	after.RequestBody.Content["application/json"] = contractcatalog.MediaType{Schema: deepClone(afterSchema)}
	after.Responses["200"] = contractcatalog.Response{Headers: map[string]contractcatalog.Header{"X-Value": {Required: true, Schema: deepClone(afterSchema)}, "X-New": {Schema: &contractcatalog.Schema{Type: "string"}}}, Content: map[string]contractcatalog.MediaType{"application/json": {Schema: deepClone(afterSchema)}, "text/plain": {Schema: &contractcatalog.Schema{Type: "string"}}}}
	after.Security = []contractcatalog.SecurityRequirement{{Schemes: map[string][]string{"new": {"write"}}}}
	after.SecuritySchemes = map[string]contractcatalog.SecurityScheme{"new": {Name: "new", Type: "oauth2"}}
	after.Confidence, after.Completeness = contractcatalog.ConfidenceHigh, contractcatalog.CompletenessComplete
	projected := deepClone(before)
	for _, dimension := range operationDimensions(before, after) {
		if dimension.Before != dimension.After {
			_ = dimensionValue(before, dimension.Path)
			_ = dimensionValue(after, dimension.Path)
			applyDimension(&projected, after, dimension.Path)
		}
	}
	if !decisionJSONEqual(projected, after) {
		left, _ := json.Marshal(projected)
		right, _ := json.Marshal(after)
		t.Fatalf("atomic projection mismatch: projected=%s candidate=%s", left, right)
	}
}

// TestApprovedSchemeSetComparisonBindsNamesAndValues proves derived baselines cannot swap or omit security definitions.
func TestApprovedSchemeSetComparisonBindsNamesAndValues(t *testing.T) {
	values := map[string]contractcatalog.SecurityScheme{"oauth": {Type: "oauth2"}}
	list := mapSchemes(values)
	if len(list) != 1 || list[0].Name != "oauth" || !equivalentSchemeSet(list, map[string]contractcatalog.SecurityScheme{"oauth": {Name: "oauth", Type: "oauth2"}}) {
		t.Fatalf("scheme canonicalization failed: %#v", list)
	}
	if equivalentSchemeSet(list, nil) || equivalentSchemeSet(list, map[string]contractcatalog.SecurityScheme{"oauth": {Name: "oauth", Type: "apiKey"}}) {
		t.Fatal("scheme drift accepted")
	}
}

// TestApprovalLineageDoesNotElevatePartialEvidence proves human authority preserves epistemic uncertainty.
func TestApprovalLineageDoesNotElevatePartialEvidence(t *testing.T) {
	op := fixtureOperation("a-source", strings.Repeat("a", 64), "/partial", "partial", "200")
	catalog := fixtureCatalog(t, "a-source", []contractcatalog.OperationDescriptor{op}, contractcatalog.CompletenessPartial, nil, contractcatalog.OmissionCounts{Unselected: 1})
	projection := ApprovedProjection{Operations: catalog.Operations(), Omissions: catalog.Omissions(), Binding: ApprovalBinding{ProjectionDigest: strings.Repeat("d", 64)}}
	snapshot := approvedSnapshot(projection, ReviewArtifact{ID: "review", Candidate: candidateFor(catalog).Identity})
	if snapshot.Source.Completeness != contractcatalog.CompletenessPartial || snapshot.Operations[0].Provenance[len(snapshot.Operations[0].Provenance)-1].Completeness != contractcatalog.CompletenessPartial {
		t.Fatalf("authority elevated evidence: %#v", snapshot.Source)
	}
}

// TestPreparedExportReconcilesAfterPublicationCrash proves retry commits one receipt without repeating bytes or accepting a changed request.
func TestPreparedExportReconcilesAfterPublicationCrash(t *testing.T) {
	base := NewMemoryStore()
	wrapped := &failCommitStore{Store: base, fail: true}
	service, _, _, _ := serviceFixture(t, wrapped)
	publisher := &reconcilingPublisher{}
	service.Publisher = publisher
	view := createServiceReview(t, service)
	approved, err := service.Decide(context.Background(), DecisionCommand{PrincipalID: "actor", ReviewID: view.ID, Decision: DecisionInput{ExpectedRevision: 0, Whole: WholeAccept, IdempotencyKey: "approve"}})
	if err != nil {
		t.Fatal(err)
	}
	command := ExportCommand{PrincipalID: "actor", ReviewID: view.ID, IdempotencyKey: "export", ExpectedRevision: approved.Revision, Target: PublishRequest{Target: "contract.json"}, Info: ExportInfo{Title: "contract", Version: "1"}}
	if _, err = service.Export(context.Background(), command); err == nil {
		t.Fatal("injected crash was not observed")
	}
	receipt, err := service.Export(context.Background(), command)
	if err != nil {
		t.Fatal(err)
	}
	if publisher.calls != 2 || receipt.OutputDigest != publisher.digest {
		t.Fatalf("reconcile calls=%d receipt=%#v", publisher.calls, receipt)
	}
	changed := command
	changed.Info.Version = "2"
	_, err = service.Export(context.Background(), changed)
	errorCategory(t, err, CategoryConflict)
}

// TestLocalStoreRebuildsMissingIdempotencyIndexAcrossReviewIDs proves the checkpoint is authoritative after an index-write crash.
func TestLocalStoreRebuildsMissingIdempotencyIndexAcrossReviewIDs(t *testing.T) {
	root := filepath.Join(t.TempDir(), "reviews")
	store, err := NewLocalStore(root)
	if err != nil {
		t.Fatal(err)
	}
	catalog := fixtureCatalog(t, "a-source", nil, contractcatalog.CompletenessComplete, nil, contractcatalog.OmissionCounts{})
	artifact, err := CreateArtifact(context.Background(), candidateFor(catalog), baselineFor(catalog), reviewLimits())
	if err != nil {
		t.Fatal(err)
	}
	first := ReviewRecord{Artifact: artifact, State: StateCreated, Events: []AuditEvent{{Kind: "review_created", ReviewID: artifact.ID}}}
	created, err := store.Create(context.Background(), "actor", "same-key", strings.Repeat("d", 64), first)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.RemoveAll(filepath.Join(root, "idempotency")); err != nil {
		t.Fatal(err)
	}
	second := first.Clone()
	second.Artifact.ID = strings.Repeat("b", 24)
	second.Artifact.Digest = strings.Repeat("b", 64)
	second.Events[0].ReviewID = second.Artifact.ID
	recovered, err := store.Create(context.Background(), "actor", "same-key", strings.Repeat("d", 64), second)
	if err != nil {
		t.Fatal(err)
	}
	if recovered.Artifact.ID != created.Artifact.ID {
		t.Fatalf("duplicate review created: %s", recovered.Artifact.ID)
	}
}

// TestLocalStoreMultiprocessIdempotency proves two processes with the exact same request both receive one complete aggregate.
func TestLocalStoreMultiprocessIdempotency(t *testing.T) {
	if root := os.Getenv("FEATUREPROOF_STORE_HELPER_ROOT"); root != "" {
		store, err := NewLocalStore(root)
		if err != nil {
			panic(err)
		}
		id := os.Getenv("FEATUREPROOF_STORE_HELPER_ID")
		record := ReviewRecord{Artifact: ReviewArtifact{Version: VersionV1, ID: id, Digest: strings.Repeat(id[:1], 64), Limits: reviewLimits()}, State: StateCreated, Events: []AuditEvent{{Kind: "review_created", ReviewID: id}}}
		stored, err := store.Create(context.Background(), "actor", "shared-key", strings.Repeat("d", 64), record)
		if err != nil {
			panic(err)
		}
		fmt.Printf("RESULT=%s|%s|%s|%d|%d\n", stored.Artifact.ID, stored.Integrity, stored.State, len(stored.Events), len(stored.Idempotency))
		return
	}
	root := filepath.Join(t.TempDir(), "reviews")
	commands := []*exec.Cmd{exec.Command(os.Args[0], "-test.run=^TestLocalStoreMultiprocessIdempotency$"), exec.Command(os.Args[0], "-test.run=^TestLocalStoreMultiprocessIdempotency$")}
	ids := []string{strings.Repeat("a", 24), strings.Repeat("a", 24)}
	outputs := make([]bytes.Buffer, 2)
	for index, command := range commands {
		command.Env = append(os.Environ(), "FEATUREPROOF_STORE_HELPER_ROOT="+root, "FEATUREPROOF_STORE_HELPER_ID="+ids[index])
		command.Stdout = &outputs[index]
		command.Stderr = &outputs[index]
		if err := command.Start(); err != nil {
			t.Fatal(err)
		}
	}
	for index, command := range commands {
		if err := command.Wait(); err != nil {
			t.Fatalf("child %d: %v %s", index, err, outputs[index].String())
		}
	}
	first, second := subprocessResult(outputs[0].String()), subprocessResult(outputs[1].String())
	if first == "" || first != second {
		t.Fatalf("duplicated idempotent result: %q %q", outputs[0].String(), outputs[1].String())
	}
	parts := strings.Split(first, "|")
	if len(parts) != 5 || parts[0] != ids[0] || parts[1] == "" || parts[2] != string(StateCreated) || parts[3] != "1" || parts[4] != "1" {
		t.Fatalf("incomplete idempotent aggregate: %q", first)
	}
}

// subprocessResult extracts the durable result marker while ignoring Go test and coverage process output.
func subprocessResult(output string) string {
	for _, line := range strings.Split(output, "\n") {
		if strings.HasPrefix(line, "RESULT=") {
			return strings.TrimSpace(strings.TrimPrefix(line, "RESULT="))
		}
	}
	return ""
}
