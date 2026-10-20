package contractcatalog

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// mutatingAuthoritySource attempts to weaken every adapter-visible projection slice before returning prohibited content.
type mutatingAuthoritySource struct {
	snapshot SourceSnapshot
	calls    atomic.Int32
}

// Load mutates only its isolated request copy, which must not affect registry post-validation authority.
func (s *mutatingAuthoritySource) Load(_ context.Context, request SourceRequest) (SourceSnapshot, error) {
	s.calls.Add(1)
	request.authority.Projection.AllowedURLSchemes[0] = "http"
	request.authority.Projection.SensitiveNames[0] = "changed"
	request.authority.Projection.SensitivePaths[0] = "/changed"
	request.authority.Projection.SensitiveHeaderNames[0] = "changed"
	request.authority.Projection.ForbiddenValues[0] = "changed"
	return cloneSnapshot(s.snapshot), nil
}

// blockingSnapshotSource exposes deterministic overlap between caller mutation and registry validation without sharing authority.
type blockingSnapshotSource struct {
	snapshot SourceSnapshot
	entered  chan struct{}
	release  chan struct{}
}

// Load waits on test-owned barriers and returns only an isolated inert snapshot.
func (s *blockingSnapshotSource) Load(context.Context, SourceRequest) (SourceSnapshot, error) {
	close(s.entered)
	<-s.release
	return cloneSnapshot(s.snapshot), nil
}

// TestAuthorityBindingsAreImmutableAndSelfVerifying covers every catalog, policy and reduced-context authority surface.
func TestAuthorityBindingsAreImmutableAndSelfVerifying(t *testing.T) {
	snapshot := testSnapshot("one", "fake", ClassAuthoritative, ConfidenceHigh, CompletenessComplete, OperationKey{"GET", "/pets"})
	scheme := SecurityScheme{Name: "oauth", Type: "oauth2", Flows: map[string]OAuthFlow{"clientCredentials": {TokenURL: "https://issuer.example/token", Scopes: map[string]string{"read": "read pets"}}}}
	snapshot.Operations[0].Security = []SecurityRequirement{{Schemes: map[string][]string{"oauth": {"read"}}}}
	snapshot.Operations[0].SecuritySchemes = map[string]SecurityScheme{"oauth": scheme}
	snapshot.Operations[0].Claims = ClaimsForOperation(snapshot.Operations[0], snapshot.Operations[0].Provenance[0])
	snapshot.SecuritySchemes = []SecurityScheme{scheme}
	catalog, err := Build(context.Background(), loadedForTest(snapshot), catalogLimits())
	if err != nil {
		t.Fatal(err)
	}
	operations := catalog.Operations()
	operations[0].Key.Path = "/changed"
	operations[0].Responses["200"] = Response{}
	operations[0].SecuritySchemes["oauth"].Flows["clientCredentials"].Scopes["read"] = "changed"
	sources := catalog.Sources()
	sources[0].ID = "changed"
	schemes := catalog.SecuritySchemes()
	mutatedScheme := schemes["oauth"]
	mutatedFlow := mutatedScheme.Flows["clientCredentials"]
	mutatedFlow.Scopes["read"] = "changed"
	mutatedScheme.Flows["clientCredentials"] = mutatedFlow
	schemes["oauth"] = mutatedScheme
	if catalog.Operations()[0].Key.Path != "/pets" || catalog.Sources()[0].ID != "one" || catalog.SecuritySchemes()["oauth"].Flows["clientCredentials"].Scopes["read"] != "read pets" {
		t.Fatal("catalog accessor leaked authority")
	}
	conflicting := cloneSnapshot(snapshot)
	conflicting.Source.ID, conflicting.Source.Digest = "two", testDigest("two")
	conflicting.Operations[0].LogicalID = "different"
	conflicting.Operations[0].Provenance[0].SourceID, conflicting.Operations[0].Provenance[0].SourceDigest = "two", testDigest("two")
	conflicting.Operations[0].Claims = ClaimsForOperation(conflicting.Operations[0], conflicting.Operations[0].Provenance[0])
	conflictCatalog, err := Build(context.Background(), loadedForTest(snapshot, conflicting), catalogLimits())
	if err != nil {
		t.Fatal(err)
	}
	conflicts := conflictCatalog.Conflicts()
	conflicts[0].Locations[0] = "changed"
	conflicts[0].OperationKeys[0].Path = "/changed"
	if conflictCatalog.Conflicts()[0].Locations[0] == "changed" || conflictCatalog.Conflicts()[0].OperationKeys[0].Path == "/changed" {
		t.Fatal("conflict accessor leaked authority")
	}
	catalogJSON, marshalErr := json.Marshal(catalog)
	if marshalErr != nil || len(catalogJSON) == 0 || catalog.Version() != VersionV1 || catalog.Omissions() != (OmissionCounts{}) {
		t.Fatalf("catalog accessors or audit projection failed: %s %v", catalogJSON, marshalErr)
	}
	tamperedCatalog := catalog
	tamperedCatalog.operations = catalog.Operations()
	tamperedCatalog.operations[0].Key.Path = "/changed"
	if tamperedCatalog.Validate() == nil {
		t.Fatal("catalog content tamper accepted")
	}
	tamperedCatalog = catalog
	tamperedCatalog.digest = testDigest("changed")
	if tamperedCatalog.Validate() == nil {
		t.Fatal("catalog digest tamper accepted")
	}
	policy := strictPolicyForTest("strict", ConflictDeny)
	tamperedPolicy := policy
	tamperedPolicy.config.Claims = map[ClaimKind]ClaimRule{}
	for kind, rule := range policy.config.Claims {
		tamperedPolicy.config.Claims[kind] = rule
	}
	tamperedPolicy.config.Claims[ClaimSchema] = ClaimRule{}
	if tamperedPolicy.Validate() == nil {
		t.Fatal("policy content tamper accepted")
	}
	tamperedPolicy = policy
	tamperedPolicy.digest = testDigest("changed")
	if tamperedPolicy.Validate() == nil {
		t.Fatal("policy digest tamper accepted")
	}
	contextValue, err := BuildContext(context.Background(), catalog, policy, []ConstraintSelection{{ConstraintID: "c", Operations: []OperationKey{{"GET", "/pets"}}}}, testReductionLimits())
	if err != nil {
		t.Fatal(err)
	}
	bytesCopy := contextValue.Bytes()
	bytesCopy[0] = 'x'
	retainedCopy := contextValue.RetainedOperations()
	retainedCopy[0].Operation.Key.Path = "/changed"
	if contextValue.Validate() != nil || contextValue.Bytes()[0] == 'x' || contextValue.RetainedOperations()[0].Operation.Key.Path != "/pets" {
		t.Fatal("context accessor leaked authority")
	}
	contextJSON, marshalErr := json.Marshal(contextValue)
	if marshalErr != nil || len(contextJSON) == 0 || contextValue.MediaType() != ContextMediaType || !contextValue.Untrusted() || contextValue.Omissions() != (OmissionCounts{}) || contextValue.CatalogDigest() != catalog.Digest() || contextValue.PolicyDigest() != policy.Digest() || contextValue.Digest() == "" {
		t.Fatalf("context accessors or audit projection failed: %s %v", contextJSON, marshalErr)
	}
	mutations := []func(*ReducedContext){func(v *ReducedContext) { v.mediaType = "changed" }, func(v *ReducedContext) { v.untrusted = false }, func(v *ReducedContext) { v.bytes[0] = 'x' }, func(v *ReducedContext) { v.omissions.Unselected++ }, func(v *ReducedContext) { v.catalogDigest = testDigest("catalog") }, func(v *ReducedContext) { v.policyDigest = testDigest("policy") }, func(v *ReducedContext) { v.retained[0].CatalogDigest = testDigest("catalog") }, func(v *ReducedContext) { v.retained[0].PolicyDigest = testDigest("policy") }, func(v *ReducedContext) { v.retained[0].Operation.Key.Path = "/changed" }, func(v *ReducedContext) { v.retained[0].ItemDigest = testDigest("item") }, func(v *ReducedContext) { v.digest = testDigest("context") }}
	for index, mutate := range mutations {
		changed := contextValue
		changed.bytes = contextValue.Bytes()
		changed.retained = cloneRetained(contextValue.retained)
		mutate(&changed)
		if changed.Validate() == nil {
			t.Fatalf("context tamper %d accepted", index)
		}
	}
}

// TestRegistryPrevalidatesIdentityAndCoalescesExactDuplicates proves descriptors are checked before adapters and exact repeats are idempotent.
func TestRegistryPrevalidatesIdentityAndCoalescesExactDuplicates(t *testing.T) {
	snapshot := testSnapshot("one", "fake", ClassAuthoritative, ConfidenceHigh, CompletenessComplete, OperationKey{"GET", "/pets"})
	source := &countingSource{snapshot: snapshot}
	registry, _ := NewRegistry(map[string]ContractSource{"fake": source})
	request := requestFor(snapshot)
	loaded, err := registry.Load(context.Background(), []SourceRequest{request, request}, catalogLimits())
	if err != nil || source.calls.Load() != 1 || len(loaded.snapshots) != 1 {
		t.Fatalf("idempotence=%#v calls=%d err=%v", loaded, source.calls.Load(), err)
	}
	first, err := Build(context.Background(), loaded, catalogLimits())
	if err != nil {
		t.Fatal(err)
	}
	second, err := Build(context.Background(), loaded, catalogLimits())
	if err != nil || first.Digest() != second.Digest() {
		t.Fatal("sealed builder is not deterministic")
	}
	driftDescriptor := descriptorFor(snapshot)
	driftDescriptor.Confidence = ConfidenceMedium
	drift := NewSourceRequest(driftDescriptor)
	source.calls.Store(0)
	if _, err = registry.Load(context.Background(), []SourceRequest{request, drift}, catalogLimits()); err == nil || source.calls.Load() != 0 {
		t.Fatal("same ID metadata drift invoked adapter")
	}
	otherDescriptor := descriptorFor(snapshot)
	otherDescriptor.ID = "other"
	other := NewSourceRequest(otherDescriptor)
	source.calls.Store(0)
	if _, err = registry.Load(context.Background(), []SourceRequest{request, other}, catalogLimits()); err == nil || source.calls.Load() != 0 {
		t.Fatal("digest reuse invoked adapter")
	}
	if _, err = Build(context.Background(), LoadedSources{}, catalogLimits()); err == nil {
		t.Fatal("builder accepted unsealed sources")
	}
	missingScheme := cloneSnapshot(snapshot)
	missingScheme.SecuritySchemes = nil
	missingScheme.Operations[0].SecuritySchemes = nil
	source.snapshot = missingScheme
	if _, err = registry.Load(context.Background(), []SourceRequest{request}, catalogLimits()); err == nil {
		t.Fatal("reachable security scheme omission accepted")
	}
	extraScheme := cloneSnapshot(snapshot)
	extraScheme.SecuritySchemes = append(extraScheme.SecuritySchemes, SecurityScheme{Name: "unused", Type: "http"})
	source.snapshot = extraScheme
	if _, err = registry.Load(context.Background(), []SourceRequest{request}, catalogLimits()); err == nil {
		t.Fatal("unreachable security scheme accepted")
	}
	duplicateClaim := cloneSnapshot(snapshot)
	duplicateClaim.Operations[0].Claims[1] = duplicateClaim.Operations[0].Claims[0]
	source.snapshot = duplicateClaim
	if _, err = registry.Load(context.Background(), []SourceRequest{request}, catalogLimits()); err == nil {
		t.Fatal("duplicated claim concealed a missing material claim")
	}
}

// TestSourceRequestFreezesProjectionAgainstAdapterAndConcurrentCallerMutation proves trusted policy ownership on both sides of Load.
func TestSourceRequestFreezesProjectionAgainstAdapterAndConcurrentCallerMutation(t *testing.T) {
	const canary = "sealed-canary-4102"
	prohibited := testSnapshot("one", "mutating", ClassAuthoritative, ConfidenceHigh, CompletenessComplete, OperationKey{"GET", "/" + canary})
	prohibited.Operations[0].Claims = ClaimsForOperation(prohibited.Operations[0], prohibited.Operations[0].Provenance[0])
	descriptor := descriptorFor(prohibited)
	descriptor.Projection = ProjectionPolicy{MaxStringBytes: 512, AllowedURLSchemes: []string{"https"}, SensitiveNames: []string{"private-marker"}, SensitivePaths: []string{"/private"}, SensitiveHeaderNames: []string{"authorization"}, ForbiddenValues: []string{canary}}
	request := NewSourceRequest(descriptor)
	adapter := &mutatingAuthoritySource{snapshot: prohibited}
	registry, _ := NewRegistry(map[string]ContractSource{"mutating": adapter})
	loaded, err := registry.Load(context.Background(), []SourceRequest{request}, catalogLimits())
	if err == nil || len(loaded.snapshots) != 0 || adapter.calls.Load() != 1 {
		t.Fatalf("adapter weakened sealed authority: %#v %v", loaded, err)
	}
	projectionCopy := request.Projection()
	projectionCopy.ForbiddenValues[0] = "changed"
	if request.Projection().ForbiddenValues[0] != canary {
		t.Fatal("projection accessor aliased sealed authority")
	}

	valid := testSnapshot("two", "blocking", ClassAuthoritative, ConfidenceHigh, CompletenessComplete, OperationKey{"GET", "/pets"})
	callerDescriptor := descriptorFor(valid)
	callerDescriptor.Projection = ProjectionPolicy{MaxStringBytes: 512, AllowedURLSchemes: []string{"https"}, SensitiveNames: []string{"private-marker"}, SensitivePaths: []string{"/private"}, SensitiveHeaderNames: []string{"authorization"}, ForbiddenValues: []string{canary}}
	frozen := NewSourceRequest(callerDescriptor)
	blocking := &blockingSnapshotSource{snapshot: valid, entered: make(chan struct{}), release: make(chan struct{})}
	registry, _ = NewRegistry(map[string]ContractSource{"blocking": blocking})
	done := make(chan error, 1)
	go func() {
		_, loadErr := registry.Load(context.Background(), []SourceRequest{frozen}, catalogLimits())
		done <- loadErr
	}()
	<-blocking.entered
	callerDescriptor.Projection.AllowedURLSchemes[0] = "http"
	callerDescriptor.Projection.SensitiveNames[0] = "changed"
	callerDescriptor.Projection.SensitivePaths[0] = "/changed"
	callerDescriptor.Projection.SensitiveHeaderNames[0] = "changed"
	callerDescriptor.Projection.ForbiddenValues[0] = "changed"
	close(blocking.release)
	if err = <-done; err != nil {
		t.Fatalf("caller mutation drifted sealed request: %v", err)
	}
}

// TestRegistryReservesEveryAggregateDimensionBeforeNextSource proves no second adapter starts after the first exhausts a budget.
func TestRegistryReservesEveryAggregateDimensionBeforeNextSource(t *testing.T) {
	firstSnapshot := testSnapshot("first", "first", ClassAuthoritative, ConfidenceHigh, CompletenessComplete, OperationKey{"GET", "/first"})
	secondSnapshot := testSnapshot("second", "second", ClassAuthoritative, ConfidenceHigh, CompletenessComplete, OperationKey{"GET", "/second"})
	metrics, err := measureSnapshot(context.Background(), firstSnapshot)
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name string
		tune func(*Limits, *Limits)
	}{
		{"operations", func(a, p *Limits) { a.MaxOperations, p.MaxOperations = metrics.operations, metrics.operations }}, {"claims", func(a, p *Limits) { a.MaxClaims, p.MaxClaims = metrics.claims, metrics.claims }}, {"bytes", func(a, p *Limits) { a.MaxBytes, p.MaxBytes = metrics.bytes, metrics.bytes }}, {"depth", func(a, p *Limits) { a.MaxDepth, p.MaxDepth = metrics.depth, metrics.depth }}, {"diagnostics", func(a, p *Limits) { firstSnapshot.Omissions.Examples = 1; a.MaxDiagnostics, p.MaxDiagnostics = 1, 1 }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			aggregate := Limits{MaxSources: 2, MaxOperations: metrics.operations * 2, MaxClaims: metrics.claims * 2, MaxBytes: metrics.bytes * 2, MaxDepth: metrics.depth * 2, MaxDiagnostics: 4, MaxLocationBytes: 256}
			per := Limits{MaxSources: 1, MaxOperations: metrics.operations, MaxClaims: metrics.claims, MaxBytes: metrics.bytes, MaxDepth: metrics.depth, MaxDiagnostics: 1, MaxLocationBytes: 256}
			tc.tune(&aggregate, &per)
			first := &countingSource{snapshot: firstSnapshot}
			second := &countingSource{snapshot: secondSnapshot}
			registry, _ := NewRegistry(map[string]ContractSource{"first": first, "second": second})
			r1Descriptor, r2Descriptor := descriptorFor(firstSnapshot), descriptorFor(secondSnapshot)
			r1Descriptor.Limits, r2Descriptor.Limits = per, per
			r1, r2 := NewSourceRequest(r1Descriptor), NewSourceRequest(r2Descriptor)
			_, loadErr := registry.Load(context.Background(), []SourceRequest{r1, r2}, aggregate)
			var typed *Error
			if !errors.As(loadErr, &typed) || typed.Category != CategoryLimitExceeded || first.calls.Load() != 1 || second.calls.Load() != 0 {
				t.Fatalf("err=%v calls=%d/%d", loadErr, first.calls.Load(), second.calls.Load())
			}
		})
	}
}

// TestCanonicalizationIgnoresEverySetLikePermutationAndDetectsLogicalIdentityConflict covers recursive semantic ordering.
func TestCanonicalizationIgnoresEverySetLikePermutationAndDetectsLogicalIdentityConflict(t *testing.T) {
	base := testSnapshot("one", "fake", ClassAuthoritative, ConfidenceHigh, CompletenessComplete, OperationKey{"GET", "/pets"})
	op := &base.Operations[0]
	op.LogicalID = "same"
	op.Parameters = []Parameter{{Name: "b", In: "query", Schema: &Schema{Type: "object", Required: []string{"z", "a"}, Enum: []any{"b", "a"}, AllOf: []*Schema{{Type: "string"}, {Type: "integer"}}}}, {Name: "a", In: "query", Schema: &Schema{Type: "string"}}}
	op.Security = []SecurityRequirement{{Schemes: map[string][]string{"oauth": {"write", "read"}}}, {Schemes: map[string][]string{"api": {}}}}
	op.Claims = ClaimsForOperation(*op, op.Provenance[0])
	permuted := cloneSnapshot(base)
	p := &permuted.Operations[0]
	p.Parameters[0], p.Parameters[1] = p.Parameters[1], p.Parameters[0]
	schema := p.Parameters[1].Schema
	schema.Required[0], schema.Required[1] = schema.Required[1], schema.Required[0]
	schema.Enum[0], schema.Enum[1] = schema.Enum[1], schema.Enum[0]
	schema.AllOf[0], schema.AllOf[1] = schema.AllOf[1], schema.AllOf[0]
	p.Security[0], p.Security[1] = p.Security[1], p.Security[0]
	p.Security[1].Schemes["oauth"][0], p.Security[1].Schemes["oauth"][1] = p.Security[1].Schemes["oauth"][1], p.Security[1].Schemes["oauth"][0]
	p.Claims = ClaimsForOperation(*p, p.Provenance[0])
	a, _ := Build(context.Background(), loadedForTest(base), catalogLimits())
	b, _ := Build(context.Background(), loadedForTest(permuted), catalogLimits())
	if a.Digest() != b.Digest() || !reflect.DeepEqual(a.Operations(), b.Operations()) {
		t.Fatal("set-like permutation changed catalog")
	}
	different := cloneSnapshot(base)
	different.Source.ID = "two"
	different.Source.Digest = testDigest("two")
	different.Operations[0].LogicalID = "different"
	different.Operations[0].Provenance[0].SourceID = "two"
	different.Operations[0].Provenance[0].SourceDigest = testDigest("two")
	different.Operations[0].Claims = ClaimsForOperation(different.Operations[0], different.Operations[0].Provenance[0])
	merged, _ := Build(context.Background(), loadedForTest(base, different), catalogLimits())
	found := false
	for _, conflict := range merged.Conflicts() {
		if conflict.Kind == ConflictIdentity {
			found = true
		}
	}
	if !found {
		t.Fatal("logical identity conflict omitted")
	}
}

// TestProjectionRejectsConfiguredSensitiveMaterialAcrossStructuralSurfaces proves allowlisted projection precedes cataloging.
func TestProjectionRejectsConfiguredSensitiveMaterialAcrossStructuralSurfaces(t *testing.T) {
	const canary = "opaque-canary-918273"
	mutations := []func(*OperationDescriptor){func(op *OperationDescriptor) { op.Key.Path = "/" + canary }, func(op *OperationDescriptor) { op.LogicalID = canary }, func(op *OperationDescriptor) { op.Parameters = []Parameter{{Name: canary, In: "header"}} }, func(op *OperationDescriptor) { op.Parameters = []Parameter{{Name: "authorization", In: "header"}} }, func(op *OperationDescriptor) {
		op.RequestBody = &RequestBody{Content: map[string]MediaType{"application/json": {Schema: &Schema{Type: "string", Enum: []any{canary}}}}}
	}, func(op *OperationDescriptor) {
		op.Security = []SecurityRequirement{{Schemes: map[string][]string{"oauth": {canary}}}}
	}, func(op *OperationDescriptor) {
		op.SecuritySchemes = map[string]SecurityScheme{"oauth": {Name: "oauth", Type: "oauth2", Flows: map[string]OAuthFlow{"clientCredentials": {TokenURL: "https://issuer.example/" + canary, Scopes: map[string]string{"read": "ok"}}}}}
	}, func(op *OperationDescriptor) {
		op.Responses = map[string]Response{"200": {Headers: map[string]Header{canary: {}}}}
	}}
	for index, mutate := range mutations {
		snapshot := testSnapshot("one", "fake", ClassAuthoritative, ConfidenceHigh, CompletenessComplete, OperationKey{"GET", "/pets"})
		mutate(&snapshot.Operations[0])
		snapshot.Operations[0].Claims = ClaimsForOperation(snapshot.Operations[0], snapshot.Operations[0].Provenance[0])
		source := &countingSource{snapshot: snapshot}
		registry, _ := NewRegistry(map[string]ContractSource{"fake": source})
		descriptor := descriptorFor(snapshot)
		descriptor.Projection.ForbiddenValues = []string{canary}
		request := NewSourceRequest(descriptor)
		_, err := registry.Load(context.Background(), []SourceRequest{request}, catalogLimits())
		raw, _ := json.Marshal(err)
		if err == nil || strings.Contains(string(raw), canary) || strings.Contains(err.Error(), canary) {
			t.Fatalf("surface %d leaked/accepted: %s", index, raw)
		}
	}
}

// cancellingContext cancels deterministically after a fixed number of traversal checks without wall-clock sleeps.
type cancellingContext struct {
	context.Context
	remaining atomic.Int32
}

// Err returns cancellation once the traversal has consumed its deterministic check budget.
func (c *cancellingContext) Err() error {
	if c.remaining.Add(-1) <= 0 {
		return context.Canceled
	}
	return nil
}

// Deadline delegates the unused deadline contract to the embedded context.
func (c *cancellingContext) Deadline() (time.Time, bool) { return time.Time{}, false }

// TestCancellationDuringSourceAndReductionTraversalReturnsNoPartialState proves traversal checks are not post-serialization only.
func TestCancellationDuringSourceAndReductionTraversalReturnsNoPartialState(t *testing.T) {
	snapshot := testSnapshot("one", "fake", ClassAuthoritative, ConfidenceHigh, CompletenessComplete, OperationKey{"GET", "/pets"})
	snapshot.Operations[0].RequestBody = &RequestBody{Content: map[string]MediaType{"application/json": {Schema: &Schema{Type: "object", Properties: map[string]*Schema{}}}}}
	for i := 0; i < 200; i++ {
		snapshot.Operations[0].RequestBody.Content["application/json"].Schema.Properties[string(rune('a'+i%26))+string(rune('A'+i/26))] = &Schema{Type: "string"}
	}
	snapshot.Operations[0].Claims = ClaimsForOperation(snapshot.Operations[0], snapshot.Operations[0].Provenance[0])
	registry, _ := NewRegistry(map[string]ContractSource{"fake": &StaticSource{Snapshot: snapshot}})
	ctx := &cancellingContext{Context: context.Background()}
	ctx.remaining.Store(40)
	loaded, err := registry.Load(ctx, []SourceRequest{requestFor(snapshot)}, catalogLimits())
	if !errors.Is(err, context.Canceled) || len(loaded.snapshots) != 0 {
		t.Fatalf("source cancellation=%#v %v", loaded, err)
	}
	ctx = &cancellingContext{Context: context.Background()}
	ctx.remaining.Store(40)
	merged, err := Build(ctx, loadedForTest(snapshot), catalogLimits())
	if !errors.Is(err, context.Canceled) || merged.Digest() != "" {
		t.Fatalf("merge cancellation=%#v %v", merged, err)
	}
	catalog, _ := Build(context.Background(), loadedForTest(snapshot), catalogLimits())
	policy := strictPolicyForTest("strict", ConflictDeny)
	ctx = &cancellingContext{Context: context.Background()}
	ctx.remaining.Store(40)
	reduced, err := BuildContext(ctx, catalog, policy, []ConstraintSelection{{ConstraintID: "c", Operations: []OperationKey{{"GET", "/pets"}}}}, testReductionLimits())
	if !errors.Is(err, context.Canceled) || len(reduced.Bytes()) != 0 {
		t.Fatalf("reduction cancellation=%#v %v", reduced, err)
	}
}

// TestReducerFailsClosedForEveryBoundedDimension proves no partial context escapes when a selected unit or its diagnostics exceed a limit.
func TestReducerFailsClosedForEveryBoundedDimension(t *testing.T) {
	snapshot := testSnapshot("one", "fake", ClassAuthoritative, ConfidenceHigh, CompletenessComplete, OperationKey{"GET", "/pets"})
	catalog, err := Build(context.Background(), loadedForTest(snapshot), catalogLimits())
	if err != nil {
		t.Fatal(err)
	}
	policy := strictPolicyForTest("strict", ConflictDeny)
	baseSelection := []ConstraintSelection{{ConstraintID: "c", Operations: []OperationKey{{"GET", "/pets"}}}}
	claimCount := len(catalog.Operations()[0].Claims)
	cases := []struct {
		name       string
		limits     ReductionLimits
		selections []ConstraintSelection
	}{
		{name: "bytes", limits: ReductionLimits{MaxBytes: 32, MaxOperations: 8, MaxClaims: 1024, MaxDepth: 32, MaxDiagnostics: 64}, selections: baseSelection},
		{name: "operations", limits: ReductionLimits{MaxBytes: 1 << 20, MaxOperations: 1, MaxClaims: 1024, MaxDepth: 32, MaxDiagnostics: 64}, selections: []ConstraintSelection{{ConstraintID: "a", Operations: []OperationKey{{"GET", "/pets"}}}, {ConstraintID: "b", Operations: []OperationKey{{"GET", "/pets"}}}}},
		{name: "claims", limits: ReductionLimits{MaxBytes: 1 << 20, MaxOperations: 8, MaxClaims: claimCount - 1, MaxDepth: 32, MaxDiagnostics: 64}, selections: baseSelection},
		{name: "depth", limits: ReductionLimits{MaxBytes: 1 << 20, MaxOperations: 8, MaxClaims: 1024, MaxDepth: 2, MaxDiagnostics: 64}, selections: baseSelection},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			reduced, reduceErr := BuildContext(context.Background(), catalog, policy, testCase.selections, testCase.limits)
			var decision *DecisionError
			if !errors.As(reduceErr, &decision) || decision.Category != CategoryInsufficient || len(reduced.Bytes()) != 0 {
				t.Fatalf("reduction=%#v decision=%#v err=%v", reduced, decision, reduceErr)
			}
		})
	}
	diagnosticSnapshot := cloneSnapshot(snapshot)
	diagnosticSnapshot.Omissions.Examples = 2
	diagnosticCatalog, err := Build(context.Background(), loadedForTest(diagnosticSnapshot), catalogLimits())
	if err != nil {
		t.Fatal(err)
	}
	reduced, err := BuildContext(context.Background(), diagnosticCatalog, policy, baseSelection, ReductionLimits{MaxBytes: 1 << 20, MaxOperations: 8, MaxClaims: 1024, MaxDepth: 32, MaxDiagnostics: 1})
	var typed *Error
	if !errors.As(err, &typed) || typed.Category != CategoryLimitExceeded || len(reduced.Bytes()) != 0 {
		t.Fatalf("diagnostic reduction=%#v err=%v", reduced, err)
	}
}

// TestClaimPolicyUsesAnySufficientProvenanceAndPreservesTypedDecisions proves weak corroboration cannot degrade a strong claim.
func TestClaimPolicyUsesAnySufficientProvenanceAndPreservesTypedDecisions(t *testing.T) {
	strong := testSnapshot("a", "fake", ClassAuthoritative, ConfidenceHigh, CompletenessComplete, OperationKey{"GET", "/pets"})
	weak := testSnapshot("b", "fake", ClassObserved, ConfidenceLow, CompletenessPartial, OperationKey{"GET", "/pets"})
	strong.Operations[0].LogicalID, weak.Operations[0].LogicalID = "pets", "pets"
	strong.Operations[0].Claims = ClaimsForOperation(strong.Operations[0], strong.Operations[0].Provenance[0])
	weak.Operations[0].Claims = ClaimsForOperation(weak.Operations[0], weak.Operations[0].Provenance[0])
	catalog, err := Build(context.Background(), loadedForTest(weak, strong), catalogLimits())
	if err != nil {
		t.Fatal(err)
	}
	policy := strictPolicyForTest("strict", ConflictDeny)
	selection := []ConstraintSelection{{ConstraintID: "c", Operations: []OperationKey{{"GET", "/pets"}}}}
	result, err := policy.Evaluate(context.Background(), selection, catalog)
	if err != nil || result.Eligible == nil {
		t.Fatalf("strong claim degraded by weak corroboration: %#v %v", result, err)
	}
	_, err = BuildContext(context.Background(), catalog, policy, []ConstraintSelection{{ConstraintID: "c", Operations: []OperationKey{{"GET", "/missing"}}}}, testReductionLimits())
	var decision *DecisionError
	if !errors.As(err, &decision) || decision.Category != CategoryInsufficient || len(decision.Operations) != 1 || decision.Operations[0].Path != "/missing" || len(decision.MissingKinds) == 0 {
		t.Fatalf("typed insufficient decision lost: %#v %v", decision, err)
	}
	if decision.Error() != "contract catalog: insufficient_context at selection" {
		t.Fatalf("unsafe decision error: %q", decision.Error())
	}
	conflicting := cloneSnapshot(strong)
	conflicting.Source.ID = "c"
	conflicting.Source.Digest = testDigest("c")
	conflicting.Operations[0].LogicalID = "other"
	conflicting.Operations[0].Provenance[0].SourceID = "c"
	conflicting.Operations[0].Provenance[0].SourceDigest = testDigest("c")
	conflicting.Operations[0].Claims = ClaimsForOperation(conflicting.Operations[0], conflicting.Operations[0].Provenance[0])
	conflictCatalog, _ := Build(context.Background(), loadedForTest(strong, conflicting), catalogLimits())
	review := strictPolicyForTest("review", ConflictRequireReview)
	_, err = BuildContext(context.Background(), conflictCatalog, review, selection, testReductionLimits())
	if !errors.As(err, &decision) || decision.Category != CategoryReviewRequired || len(decision.ConflictIDs) == 0 || len(decision.Operations) != 1 {
		t.Fatalf("typed review decision lost: %#v %v", decision, err)
	}
}
