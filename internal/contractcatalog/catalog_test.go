package contractcatalog

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
)

// testDigest derives stable safe SHA-256 source identities without external state.
func testDigest(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}

// catalogLimits supplies complete finite limits shared by hermetic contract catalog tests.
func catalogLimits() Limits {
	return Limits{MaxSources: 8, MaxOperations: 64, MaxClaims: 1024, MaxBytes: 1 << 20, MaxDepth: 32, MaxDiagnostics: 64, MaxLocationBytes: 256}
}

// testProjectionPolicy supplies an explicit structural redaction policy without external dependencies.
func testProjectionPolicy() ProjectionPolicy {
	return ProjectionPolicy{MaxStringBytes: 512, AllowedURLSchemes: []string{"https"}, SensitiveNames: []string{}, SensitivePaths: []string{}, SensitiveHeaderNames: []string{"authorization", "cookie", "set-cookie", "x-api-key"}, ForbiddenValues: []string{}}
}

// loadedForTest seals already validated fixtures inside the package so direct builder tests cannot forge production authority.
func loadedForTest(snapshots ...SourceSnapshot) LoadedSources {
	return LoadedSources{snapshots: snapshots}
}

// strictPolicyForTest creates complete claim rules for every material dimension.
func strictPolicyForTest(id string, treatment ConflictTreatment) Policy {
	rules := map[ClaimKind]ClaimRule{}
	for _, kind := range []ClaimKind{ClaimOperationIdentity, ClaimSchema, ClaimSecurity, ClaimResponse} {
		rules[kind] = ClaimRule{AllowedClasses: []SourceClass{ClassAuthoritative}, MinimumConfidence: map[SourceClass]Confidence{ClassAuthoritative: ConfidenceHigh}, RequiredCompleteness: CompletenessComplete, Required: true}
	}
	policy, _ := NewPolicy(SufficiencyPolicy{ID: id, Version: VersionV1, Claims: rules, ConflictTreatment: treatment})
	return policy
}

// testReductionLimits bounds every reducer dimension in hermetic tests.
func testReductionLimits() ReductionLimits {
	return ReductionLimits{MaxBytes: 1 << 20, MaxOperations: 8, MaxClaims: 1024, MaxDepth: 32, MaxDiagnostics: 64}
}

// testOperation creates one complete neutral operation with exact safe provenance.
func testOperation(sourceID string, class SourceClass, confidence Confidence, completeness Completeness, key OperationKey) OperationDescriptor {
	provenance := Provenance{SourceID: sourceID, Class: class, SourceDigest: testDigest(sourceID), AdapterVersion: "fake-v1", SafeLocation: "operations[0]", Confidence: confidence, Completeness: completeness}
	operation := OperationDescriptor{Key: key, LogicalID: sourceID, Responses: map[string]Response{"200": {Content: map[string]MediaType{"application/json": {Schema: &Schema{Type: "array", Items: &Schema{Type: "string"}}}}}}, Security: []SecurityRequirement{{Schemes: map[string][]string{"oauth": {"read"}}}}, SecuritySchemes: map[string]SecurityScheme{"oauth": {Name: "oauth", Type: "oauth2", Flows: map[string]OAuthFlow{"clientCredentials": {TokenURL: "https://issuer.example/token", Scopes: map[string]string{"read": "read"}}}}}, Provenance: []Provenance{provenance}, Confidence: confidence, Completeness: completeness, Classes: []SourceClass{class}}
	operation.Claims = ClaimsForOperation(operation, provenance)
	return operation
}

// testSnapshot wraps one operation using trusted metadata matching the registry request.
func testSnapshot(id, kind string, class SourceClass, confidence Confidence, completeness Completeness, key OperationKey) SourceSnapshot {
	operation := testOperation(id, class, confidence, completeness, key)
	return SourceSnapshot{Version: VersionV1, Source: SourceSummary{ID: id, Kind: kind, Class: class, Digest: testDigest(id), AdapterVersion: "fake-v1", Confidence: confidence, Completeness: completeness}, Operations: []OperationDescriptor{operation}, SecuritySchemes: []SecurityScheme{operation.SecuritySchemes["oauth"]}}
}

// descriptorFor mirrors trusted composition metadata before the immutable request boundary seals it.
func descriptorFor(snapshot SourceSnapshot) SourceDescriptor {
	return SourceDescriptor{Kind: snapshot.Source.Kind, ID: snapshot.Source.ID, Class: snapshot.Source.Class, Confidence: snapshot.Source.Confidence, Completeness: snapshot.Source.Completeness, ExpectedDigest: snapshot.Source.Digest, AdapterVersion: snapshot.Source.AdapterVersion, Input: struct{}{}, Limits: catalogLimits(), Projection: testProjectionPolicy()}
}

// requestFor seals trusted test composition metadata without granting the fake source external capabilities.
func requestFor(snapshot SourceSnapshot) SourceRequest {
	return NewSourceRequest(descriptorFor(snapshot))
}

// TestAllSourceClassesProduceCommonSnapshot proves every epistemic class crosses one bounded source contract.
func TestAllSourceClassesProduceCommonSnapshot(t *testing.T) {
	for _, class := range []SourceClass{ClassAuthoritative, ClassDerived, ClassObserved, ClassDeclared} {
		snapshot := testSnapshot(string(class), "fake", class, ConfidenceMedium, CompletenessPartial, OperationKey{"GET", "/pets"})
		source := &StaticSource{Snapshot: snapshot}
		registry, err := NewRegistry(map[string]ContractSource{"fake": source})
		if err != nil {
			t.Fatal(err)
		}
		loaded, err := registry.Load(context.Background(), []SourceRequest{requestFor(snapshot)}, catalogLimits())
		if err != nil || len(loaded.snapshots) != 1 || loaded.snapshots[0].Source.Class != class {
			t.Fatalf("class %s: %#v %v", class, loaded, err)
		}
	}
}

// TestUnsupportedVersionsAndPartialPoliciesFailBeforeSelection proves both neutral contracts require explicit complete versions.
func TestUnsupportedVersionsAndPartialPoliciesFailBeforeSelection(t *testing.T) {
	snapshot := testSnapshot("one", "fake", ClassAuthoritative, ConfidenceHigh, CompletenessComplete, OperationKey{"GET", "/pets"})
	snapshot.Version = "2"
	registry, _ := NewRegistry(map[string]ContractSource{"fake": &StaticSource{Snapshot: snapshot}})
	if _, err := registry.Load(context.Background(), []SourceRequest{requestFor(snapshot)}, catalogLimits()); err == nil {
		t.Fatal("unsupported source version accepted")
	}
	if err := (OperationCatalog{}).Validate(); err == nil {
		t.Fatal("empty catalog accepted")
	}
	if _, err := NewPolicy(SufficiencyPolicy{ID: "partial", Version: "1", Claims: map[ClaimKind]ClaimRule{}}); err == nil {
		t.Fatal("partial policy received defaults")
	}
}

// TestRegistryRejectsUnknownDuplicateAndAuthorityElevation verifies trusted metadata cannot be self-selected by adapters.
func TestRegistryRejectsUnknownDuplicateAndAuthorityElevation(t *testing.T) {
	base := testSnapshot("one", "fake", ClassDerived, ConfidenceMedium, CompletenessPartial, OperationKey{"GET", "/pets"})
	registry, _ := NewRegistry(map[string]ContractSource{"fake": &StaticSource{Snapshot: base}})
	unknownDescriptor := descriptorFor(base)
	unknownDescriptor.Kind = "missing"
	unknown := NewSourceRequest(unknownDescriptor)
	if _, err := registry.Load(context.Background(), []SourceRequest{unknown}, catalogLimits()); err == nil {
		t.Fatal("unknown adapter accepted")
	}
	elevated := base
	elevated.Source.Class = ClassAuthoritative
	registry, _ = NewRegistry(map[string]ContractSource{"fake": &StaticSource{Snapshot: elevated}})
	if _, err := registry.Load(context.Background(), []SourceRequest{requestFor(base)}, catalogLimits()); err == nil {
		t.Fatal("source elevated configured class")
	}
	driftDescriptor := descriptorFor(base)
	driftDescriptor.ExpectedDigest = testDigest("other")
	drift := NewSourceRequest(driftDescriptor)
	counted := &countingSource{snapshot: base}
	registry, _ = NewRegistry(map[string]ContractSource{"fake": counted})
	if _, err := registry.Load(context.Background(), []SourceRequest{requestFor(base), drift}, catalogLimits()); err == nil || counted.calls.Load() != 0 {
		t.Fatal("duplicate identity drift accepted")
	}
}

// TestLoadsAreConcurrentAndCancellationStopsFollowingSource proves independent registry calls are race-safe and fail atomically.
func TestLoadsAreConcurrentAndCancellationStopsFollowingSource(t *testing.T) {
	snapshot := testSnapshot("one", "fake", ClassDeclared, ConfidenceLow, CompletenessUnknown, OperationKey{"GET", "/pets"})
	source := &countingSource{snapshot: snapshot}
	registry, _ := NewRegistry(map[string]ContractSource{"fake": source})
	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := registry.Load(context.Background(), []SourceRequest{requestFor(snapshot)}, catalogLimits()); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	if source.calls.Load() != 16 {
		t.Fatalf("calls=%d", source.calls.Load())
	}
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	if result, err := registry.Load(cancelled, []SourceRequest{requestFor(snapshot)}, catalogLimits()); !errors.Is(err, context.Canceled) || len(result.snapshots) != 0 {
		t.Fatalf("result=%v err=%v", result, err)
	}
	if source.calls.Load() != 16 {
		t.Fatal("cancelled load invoked adapter")
	}
}

// countingSource records invocations while exposing no network, process, credential or tool port.
type countingSource struct {
	snapshot SourceSnapshot
	calls    atomic.Int32
}

// failingSource returns one controlled typed failure and records whether later work was attempted.
type failingSource struct {
	calls atomic.Int32
	err   error
}

// Load reports the configured safe error without returning partial source state.
func (s *failingSource) Load(context.Context, SourceRequest) (SourceSnapshot, error) {
	s.calls.Add(1)
	return SourceSnapshot{}, s.err
}

// TestLimitFailureStopsBeforeNextSourceAndPreservesCategory verifies sequential all-or-nothing ingestion.
func TestLimitFailureStopsBeforeNextSourceAndPreservesCategory(t *testing.T) {
	firstSnapshot := testSnapshot("first", "limit", ClassDeclared, ConfidenceLow, CompletenessUnknown, OperationKey{"GET", "/first"})
	secondSnapshot := testSnapshot("second", "next", ClassDeclared, ConfidenceLow, CompletenessUnknown, OperationKey{"GET", "/second"})
	first := &failingSource{err: &Error{Category: CategoryLimitExceeded, Location: "operations"}}
	next := &countingSource{snapshot: secondSnapshot}
	registry, _ := NewRegistry(map[string]ContractSource{"limit": first, "next": next})
	result, err := registry.Load(context.Background(), []SourceRequest{requestFor(firstSnapshot), requestFor(secondSnapshot)}, catalogLimits())
	var typed *Error
	if len(result.snapshots) != 0 || !errors.As(err, &typed) || typed.Category != CategoryLimitExceeded || first.calls.Load() != 1 || next.calls.Load() != 0 {
		t.Fatalf("result=%v err=%v calls=%d/%d", result, err, first.calls.Load(), next.calls.Load())
	}
}

// Load copies one inert snapshot and observes caller cancellation.
func (s *countingSource) Load(ctx context.Context, _ SourceRequest) (SourceSnapshot, error) {
	if err := ctx.Err(); err != nil {
		return SourceSnapshot{}, err
	}
	s.calls.Add(1)
	return cloneSnapshot(s.snapshot), nil
}

// TestMergeIsOrderIndependentAndCoalescesEqualClaims verifies canonical digest, conservative summaries and full provenance.
func TestMergeIsOrderIndependentAndCoalescesEqualClaims(t *testing.T) {
	a := testSnapshot("a", "fake", ClassAuthoritative, ConfidenceHigh, CompletenessComplete, OperationKey{"GET", "/pets"})
	b := testSnapshot("b", "fake", ClassDerived, ConfidenceMedium, CompletenessPartial, OperationKey{"GET", "/pets"})
	first, err := Build(context.Background(), loadedForTest(a, b), catalogLimits())
	if err != nil {
		t.Fatal(err)
	}
	second, err := Build(context.Background(), loadedForTest(b, a), catalogLimits())
	if err != nil {
		t.Fatal(err)
	}
	if first.Digest() != second.Digest() || !jsonEqual(first.Operations(), second.Operations()) {
		t.Fatal("merge depends on source order")
	}
	op := first.Operations()[0]
	if len(op.Provenance) != 2 || op.Confidence != ConfidenceMedium || op.Completeness != CompletenessPartial {
		t.Fatalf("summary=%#v", op)
	}
	coalesced := false
	for _, claim := range op.Claims {
		if claim.Kind == ClaimResponse && len(claim.Provenance) == 2 {
			coalesced = true
		}
	}
	if !coalesced {
		t.Fatalf("equal material claims lost provenance: %#v", op.Claims)
	}
	op.Provenance[0].SourceID = "mutated"
	if first.Operations()[0].Provenance[0].SourceID == "mutated" {
		t.Fatal("catalog leaked mutable state")
	}
}

// TestMethodPathSchemaSecurityAndResponseConflictsAreExplicit covers every fail-closed conflict category.
func TestMethodPathSchemaSecurityAndResponseConflictsAreExplicit(t *testing.T) {
	base := testSnapshot("a", "fake", ClassAuthoritative, ConfidenceHigh, CompletenessComplete, OperationKey{"GET", "/pets"})
	variants := []SourceSnapshot{}
	identity := testOperation("a", ClassAuthoritative, ConfidenceHigh, CompletenessComplete, OperationKey{"POST", "/pets"})
	identity.LogicalID = base.Operations[0].LogicalID
	base.Operations = append(base.Operations, identity)
	schema := testSnapshot("schema", "fake", ClassAuthoritative, ConfidenceHigh, CompletenessComplete, OperationKey{"GET", "/pets"})
	schema.Operations[0].RequestBody = &RequestBody{Required: true, Content: map[string]MediaType{"application/json": {Schema: &Schema{Type: "object"}}}}
	schema.Operations[0].Claims = ClaimsForOperation(schema.Operations[0], schema.Operations[0].Provenance[0])
	variants = append(variants, schema)
	security := testSnapshot("security", "fake", ClassAuthoritative, ConfidenceHigh, CompletenessComplete, OperationKey{"GET", "/pets"})
	security.Operations[0].Security = nil
	security.Operations[0].Claims = ClaimsForOperation(security.Operations[0], security.Operations[0].Provenance[0])
	variants = append(variants, security)
	response := testSnapshot("response", "fake", ClassAuthoritative, ConfidenceHigh, CompletenessComplete, OperationKey{"GET", "/pets"})
	response.Operations[0].Responses = map[string]Response{"204": {}}
	response.Operations[0].Claims = ClaimsForOperation(response.Operations[0], response.Operations[0].Provenance[0])
	variants = append(variants, response)
	all := append([]SourceSnapshot{base}, variants...)
	catalog, err := Build(context.Background(), loadedForTest(all...), catalogLimits())
	if err != nil {
		t.Fatal(err)
	}
	seen := map[ConflictKind]bool{}
	for _, c := range catalog.Conflicts() {
		seen[c.Kind] = true
	}
	for _, kind := range []ConflictKind{ConflictIdentity, ConflictSchema, ConflictSecurity, ConflictResponse} {
		if !seen[kind] {
			t.Fatalf("missing %s conflict: %#v", kind, catalog.Conflicts())
		}
	}
}

// TestPolicyReturnsEligibleInsufficientOrReviewRequired proves the decision union and independent-operation behavior.
func TestPolicyReturnsEligibleInsufficientOrReviewRequired(t *testing.T) {
	good := testSnapshot("good", "fake", ClassAuthoritative, ConfidenceHigh, CompletenessComplete, OperationKey{"GET", "/good"})
	weak := testSnapshot("weak", "fake", ClassObserved, ConfidenceLow, CompletenessPartial, OperationKey{"GET", "/weak"})
	catalog, _ := Build(context.Background(), loadedForTest(good, weak), catalogLimits())
	policy := strictPolicyForTest("strict", ConflictDeny)
	selected := []ConstraintSelection{{ConstraintID: "c", Operations: []OperationKey{{"GET", "/good"}}}}
	result, err := policy.Evaluate(context.Background(), selected, catalog)
	if err != nil || result.Eligible == nil {
		t.Fatalf("eligible=%#v err=%v", result, err)
	}
	result, _ = policy.Evaluate(context.Background(), []ConstraintSelection{{ConstraintID: "c", Operations: []OperationKey{{"GET", "/weak"}}}}, catalog)
	if result.Insufficient == nil {
		t.Fatalf("weak result=%#v", result)
	}
	conflicting := testSnapshot("conflict", "fake", ClassAuthoritative, ConfidenceHigh, CompletenessComplete, OperationKey{"GET", "/good"})
	conflicting.Operations[0].Responses = map[string]Response{"204": {}}
	conflicting.Operations[0].Claims = ClaimsForOperation(conflicting.Operations[0], conflicting.Operations[0].Provenance[0])
	catalog, _ = Build(context.Background(), loadedForTest(good, conflicting), catalogLimits())
	reviewPolicy := strictPolicyForTest("review", ConflictRequireReview)
	result, _ = reviewPolicy.Evaluate(context.Background(), selected, catalog)
	if result.Review == nil {
		t.Fatalf("review result=%#v", result)
	}
}

// TestPolicyCopiesMutableConfiguration proves caller-owned threshold maps cannot change an admitted policy after construction.
func TestPolicyCopiesMutableConfiguration(t *testing.T) {
	minimum := map[SourceClass]Confidence{ClassAuthoritative: ConfidenceHigh}
	rules := map[ClaimKind]ClaimRule{}
	for _, kind := range []ClaimKind{ClaimOperationIdentity, ClaimSchema, ClaimSecurity, ClaimResponse} {
		rules[kind] = ClaimRule{AllowedClasses: []SourceClass{ClassAuthoritative}, MinimumConfidence: minimum, RequiredCompleteness: CompletenessComplete, Required: true}
	}
	policy, err := NewPolicy(SufficiencyPolicy{ID: "immutable", Version: VersionV1, Claims: rules, ConflictTreatment: ConflictDeny})
	if err != nil {
		t.Fatal(err)
	}
	minimum[ClassAuthoritative] = ConfidenceLow
	weak := testSnapshot("weak", "fake", ClassAuthoritative, ConfidenceLow, CompletenessComplete, OperationKey{"GET", "/weak"})
	catalog, err := Build(context.Background(), loadedForTest(weak), catalogLimits())
	if err != nil {
		t.Fatal(err)
	}
	result, err := policy.Evaluate(context.Background(), []ConstraintSelection{{ConstraintID: "c", Operations: []OperationKey{{"GET", "/weak"}}}}, catalog)
	if err != nil || result.Insufficient == nil {
		t.Fatalf("result=%#v err=%v", result, err)
	}
}

// TestConflictedOperationFailsClosedButUnselectedOperationRemainsUsable proves conflicts do not contaminate independent selection.
func TestConflictedOperationFailsClosedButUnselectedOperationRemainsUsable(t *testing.T) {
	good := testSnapshot("good", "fake", ClassAuthoritative, ConfidenceHigh, CompletenessComplete, OperationKey{"GET", "/good"})
	badA := testSnapshot("bad-a", "fake", ClassAuthoritative, ConfidenceHigh, CompletenessComplete, OperationKey{"GET", "/bad"})
	badB := testSnapshot("bad-b", "fake", ClassAuthoritative, ConfidenceHigh, CompletenessComplete, OperationKey{"GET", "/bad"})
	badB.Operations[0].Responses = map[string]Response{"204": {}}
	badB.Operations[0].Claims = ClaimsForOperation(badB.Operations[0], badB.Operations[0].Provenance[0])
	catalog, err := Build(context.Background(), loadedForTest(badB, good, badA), catalogLimits())
	if err != nil {
		t.Fatal(err)
	}
	policy := strictPolicyForTest("strict", ConflictDeny)
	independent, _ := policy.Evaluate(context.Background(), []ConstraintSelection{{ConstraintID: "c", Operations: []OperationKey{{"GET", "/good"}}}}, catalog)
	if independent.Eligible == nil {
		t.Fatalf("independent selection=%#v", independent)
	}
	blocked, _ := policy.Evaluate(context.Background(), []ConstraintSelection{{ConstraintID: "c", Operations: []OperationKey{{"GET", "/bad"}}}}, catalog)
	if blocked.Insufficient == nil {
		t.Fatalf("conflict selection=%#v", blocked)
	}
}

// TestReducedContextContainsOnlyEligibleSelectedClaimsAndBindsDigests verifies relevance, canary exclusion and exact authority.
func TestReducedContextContainsOnlyEligibleSelectedClaimsAndBindsDigests(t *testing.T) {
	selected := testSnapshot("selected", "fake", ClassAuthoritative, ConfidenceHigh, CompletenessComplete, OperationKey{"GET", "/selected"})
	omitted := testSnapshot("omitted", "fake", ClassAuthoritative, ConfidenceHigh, CompletenessComplete, OperationKey{"GET", "/CANARY_SECRET"})
	catalog, _ := Build(context.Background(), loadedForTest(selected, omitted), catalogLimits())
	policy := strictPolicyForTest("strict", ConflictDeny)
	reduced, err := BuildContext(context.Background(), catalog, policy, []ConstraintSelection{{ConstraintID: "c", Operations: []OperationKey{{"GET", "/selected"}}}}, testReductionLimits())
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(reduced.Bytes()), "CANARY_SECRET") || !strings.Contains(string(reduced.Bytes()), "untrusted_data") {
		t.Fatalf("unsafe context=%s", reduced.Bytes())
	}
	retained := reduced.RetainedOperations()
	if len(retained) != 1 || retained[0].CatalogDigest != catalog.Digest() || retained[0].PolicyDigest != policy.Digest() {
		t.Fatalf("retained=%#v", retained)
	}
	retained[0].Operation.Key.Path = "/mutated"
	if reduced.RetainedOperations()[0].Operation.Key.Path != "/selected" {
		t.Fatal("retained index leaked state")
	}
	if _, err := BuildContext(context.Background(), catalog, policy, []ConstraintSelection{{ConstraintID: "c", Operations: []OperationKey{{"GET", "/selected"}}}}, ReductionLimits{MaxBytes: 32, MaxOperations: 8, MaxClaims: 1024, MaxDepth: 32, MaxDiagnostics: 64}); err == nil {
		t.Fatal("partial unit accepted")
	}
	secondSelection := []ConstraintSelection{{ConstraintID: "z", Operations: []OperationKey{{"GET", "/selected"}}}, {ConstraintID: "a", Operations: []OperationKey{{"GET", "/selected"}}}}
	firstOrder, err := BuildContext(context.Background(), catalog, policy, secondSelection, testReductionLimits())
	if err != nil {
		t.Fatal(err)
	}
	secondSelection[0], secondSelection[1] = secondSelection[1], secondSelection[0]
	secondOrder, err := BuildContext(context.Background(), catalog, policy, secondSelection, testReductionLimits())
	if err != nil || string(firstOrder.Bytes()) != string(secondOrder.Bytes()) || !jsonEqual(firstOrder.RetainedOperations(), secondOrder.RetainedOperations()) {
		t.Fatal("equivalent selection order changed context")
	}
}

// TestUnsafeLocationsAndTypedErrorsExcludeCanaries verifies safe serialization and category-based branching.
func TestUnsafeLocationsAndTypedErrorsExcludeCanaries(t *testing.T) {
	snapshot := testSnapshot("one", "fake", ClassDeclared, ConfidenceLow, CompletenessUnknown, OperationKey{"GET", "/pets"})
	snapshot.Operations[0].Provenance[0].SafeLocation = "C:/CANARY_SECRET?token=x"
	registry, _ := NewRegistry(map[string]ContractSource{"fake": &StaticSource{Snapshot: snapshot}})
	_, err := registry.Load(context.Background(), []SourceRequest{requestFor(snapshot)}, catalogLimits())
	var typed *Error
	if !errors.As(err, &typed) || typed.Category != CategoryInvalidSource {
		t.Fatalf("err=%v", err)
	}
	raw, _ := json.Marshal(err)
	if strings.Contains(string(raw), "CANARY") || strings.Contains(err.Error(), "CANARY") {
		t.Fatalf("secret escaped: %s %s", raw, err)
	}
}

// TestPolicyAndCatalogDriftChangeBindings proves approval/execution can detect both trust and content drift.
func TestPolicyAndCatalogDriftChangeBindings(t *testing.T) {
	snapshot := testSnapshot("one", "fake", ClassAuthoritative, ConfidenceHigh, CompletenessComplete, OperationKey{"GET", "/pets"})
	catalog, _ := Build(context.Background(), loadedForTest(snapshot), catalogLimits())
	a := strictPolicyForTest("a", ConflictDeny)
	b := strictPolicyForTest("b", ConflictDeny)
	if a.Digest() == b.Digest() {
		t.Fatal("policy drift invisible")
	}
	snapshot.Operations[0].Responses = map[string]Response{"204": {}}
	snapshot.Operations[0].Claims = ClaimsForOperation(snapshot.Operations[0], snapshot.Operations[0].Provenance[0])
	changed, _ := Build(context.Background(), loadedForTest(snapshot), catalogLimits())
	if catalog.Digest() == changed.Digest() {
		t.Fatal("catalog drift invisible")
	}
}
