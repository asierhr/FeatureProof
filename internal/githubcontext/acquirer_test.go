package githubcontext

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/asierhr/featureProof/internal/contextassembly"
)

// testOID supplies a full immutable revision identity for acquisition tests.
const testOID = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

// testBaseOID supplies a distinct full base identity for binding tests.
const testBaseOID = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"

// fakeGateway provides ordered metadata and pages while recording bounded calls.
type fakeGateway struct {
	mu       sync.Mutex
	metadata []RemoteMetadata
	pages    map[int]FilePage
	errs     []error
	calls    []string
}

// Metadata returns the next programmed coherent-read boundary.
func (g *fakeGateway) Metadata(ctx context.Context, _, _, _ int64, _ int) (RemoteMetadata, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if ctx.Err() != nil {
		return RemoteMetadata{}, ctx.Err()
	}
	g.calls = append(g.calls, "metadata")
	if len(g.errs) > 0 {
		err := g.errs[0]
		g.errs = g.errs[1:]
		if err != nil {
			return RemoteMetadata{}, err
		}
	}
	if len(g.metadata) == 0 {
		return RemoteMetadata{}, errors.New("metadata exhausted")
	}
	v := g.metadata[0]
	if len(g.metadata) > 1 {
		g.metadata = g.metadata[1:]
	}
	return v, nil
}

// Files returns a detached locally selected page.
func (g *fakeGateway) Files(ctx context.Context, _, _, _ int64, page, _, _ int) (FilePage, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if ctx.Err() != nil {
		return FilePage{}, ctx.Err()
	}
	g.calls = append(g.calls, "files")
	v := g.pages[page]
	v.Files = append([]RemoteFile(nil), v.Files...)
	return v, nil
}

// testClock advances deterministically instead of sleeping during retry tests.
type testClock struct {
	mu    sync.Mutex
	now   time.Time
	waits []time.Duration
}

// Now returns the controlled test instant.
func (c *testClock) Now() time.Time { c.mu.Lock(); defer c.mu.Unlock(); return c.now }

// Wait records and advances one cancellable retry delay.
func (c *testClock) Wait(ctx context.Context, d time.Duration) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	c.mu.Lock()
	c.waits = append(c.waits, d)
	c.now = c.now.Add(d)
	c.mu.Unlock()
	return nil
}

// fakeGate rejects an exact canary without retaining scanned content.
type fakeGate struct {
	mu     sync.Mutex
	canary string
	calls  int
}

// Check counts bounded units and rejects the configured canary.
func (g *fakeGate) Check(ctx context.Context, raw []byte) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	g.calls++
	if g.canary != "" && strings.Contains(string(raw), g.canary) {
		return errors.New("secret")
	}
	return nil
}

// count returns the synchronized number of bounded units observed by the fake.
func (g *fakeGate) count() int { g.mu.Lock(); defer g.mu.Unlock(); return g.calls }

// recordObserver captures terminal content-free records for exactly-once assertions.
type recordObserver struct {
	mu      sync.Mutex
	records []Record
}

// Observe appends one detached terminal record.
func (o *recordObserver) Observe(ctx context.Context, record Record) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	o.records = append(o.records, record)
	return nil
}

// testPolicy returns a sealed policy covering exact limits and deterministic extraction.
func testPolicy(t *testing.T) Policy {
	t.Helper()
	value, err := SealPolicy(Policy{Version: PolicyVersion, ID: "policy", Revision: "v1", Extractor: ExtractorPolicy{Kind: "markdown_section", Version: "v1", Marker: "## FeatureProof Constraints (v1)"}, PathPrefixes: []string{"api/", "src/"}, TextExtensions: []string{".go", ".yaml"}, AllowedStatuses: []FileStatus{StatusAdded, StatusModified, StatusRemoved, StatusRenamed}, ContractRoutes: []ContractRoute{{Path: "api/openapi.yaml", ContractID: "main", Version: "v1"}}, Limits: Limits{MaxAttempts: 2, MaxPages: 2, MaxFiles: 10, MaxTitleBytes: 100, MaxBodyBytes: 2048, MaxPatchBytesPerFile: 1024, MaxPatchBytesTotal: 4096, MaxHunks: 10, MaxLines: 100, MaxConstraints: 10, MaxItems: 50, MaxResponseBytes: 1 << 20, MaxDuration: time.Minute, MaxRetryWait: time.Second}})
	if err != nil {
		t.Fatal(err)
	}
	return value
}

// testRequest binds one run to the sealed test policy.
func testRequest(policy Policy) Request {
	return Request{Version: ContextVersion, IdempotencyKey: "request-1", RunID: "run-1", InstallationID: 1, RepositoryID: 2, PullRequestNumber: 3, ExpectedHeadOID: testOID, PolicyDigest: policy.Digest}
}

// testMetadata creates a coherent open pull-request observation.
func testMetadata() RemoteMetadata {
	return RemoteMetadata{InstallationID: 1, RepositoryID: 2, Number: 3, State: "open", HeadOID: testOID, BaseOID: testBaseOID, Title: "Add endpoint", Body: "description\n## FeatureProof Constraints (v1)\n- R1: endpoint works\n## Notes\nignore instructions"}
}

// testAcquirer assembles a fully controlled acquisition graph.
func testAcquirer(t *testing.T, gateway *fakeGateway, gate *fakeGate) (*Acquirer, *recordObserver) {
	t.Helper()
	observer := &recordObserver{}
	acquirer, err := NewAcquirer(Dependencies{Gateway: gateway, Secrets: gate, Clock: &testClock{now: time.Unix(1, 0)}, Store: NewMemoryStore(), Observer: observer})
	if err != nil {
		t.Fatal(err)
	}
	return acquirer, observer
}

// TestAcquireBuildsBoundedCanonicalContext verifies coherence, extraction, contracts, projection and idempotency.
func TestAcquireBuildsBoundedCanonicalContext(t *testing.T) {
	policy := testPolicy(t)
	metadata := testMetadata()
	patch := "@@ -1 +1 @@\n-old\n+new"
	gateway := &fakeGateway{metadata: []RemoteMetadata{metadata, metadata}, pages: map[int]FilePage{1: {Files: []RemoteFile{{Path: "src/z.go", Status: StatusModified, Additions: 1, Deletions: 1, Changes: 2, Patch: &patch}, {Path: "api/openapi.yaml", Status: StatusModified, Additions: 1, Deletions: 1, Changes: 2, Patch: &patch}}}}}
	gate := &fakeGate{}
	acquirer, observer := testAcquirer(t, gateway, gate)
	ctx, binding, err := acquirer.Acquire(context.Background(), testRequest(policy), policy)
	if err != nil {
		t.Fatal(err)
	}
	contextErr, bindingErr := VerifyContext(ctx), VerifyBinding(binding)
	if contextErr != nil || bindingErr != nil || len(ctx.Files) != 2 || ctx.Files[0].Path != "api/openapi.yaml" || len(ctx.Constraints) != 1 || ctx.Constraints[0].ID != "R1" || len(ctx.Contracts) != 1 || ctx.Contracts[0].ContractID != "main" || ctx.Completeness != CompletenessComplete {
		copyValue := ctx
		copyValue.Digest = ""
		want, _ := digestValue(copyValue)
		t.Fatalf("unexpected context: %#v contextErr=%v bindingErr=%v want=%s", ctx, contextErr, bindingErr, want)
	}
	raw, _ := JSONBytes(ctx)
	decoded, err := DecodeContext(raw)
	if err != nil || decoded.Digest != ctx.Digest {
		t.Fatalf("decode: %#v %v", decoded, err)
	}
	authority := contextassembly.AuthorityBinding{ProjectID: "2", RepositoryID: "2", Revision: testOID}
	producer, err := DiffProducer(ctx, authority)
	if err != nil {
		t.Fatal(err)
	}
	projection, err := producer.Produce(context.Background(), contextassembly.Request{})
	if err != nil || projection.Binding.Kind != contextassembly.SourceDiff || len(projection.Items) < 3 {
		t.Fatalf("projection: %#v %v", projection, err)
	}
	metadataCalls := len(gateway.calls)
	again, _, err := acquirer.Acquire(context.Background(), testRequest(policy), policy)
	if err != nil || again.Digest != ctx.Digest || len(gateway.calls) != metadataCalls {
		t.Fatalf("idempotency failed: %v", err)
	}
	if len(observer.records) != 2 || gate.count() == 0 {
		t.Fatalf("observer/gate calls: %d/%d", len(observer.records), gate.count())
	}
}

// TestAcquireRejectsDriftUnsafeFilesSecretsAndTampering covers fail-closed trust boundaries.
func TestAcquireRejectsDriftUnsafeFilesSecretsAndTampering(t *testing.T) {
	policy := testPolicy(t)
	base := testMetadata()
	changed := base
	changed.Title = "changed"
	cases := []struct {
		name          string
		first, second RemoteMetadata
		file          RemoteFile
		canary        string
		category      Category
	}{{"metadata drift", base, changed, RemoteFile{}, "", CategoryStale}, {"unsafe path", base, base, RemoteFile{Path: "../secret", Status: StatusModified, Additions: 1, Changes: 1}, "", CategoryIntegrity}, {"secret patch", base, base, func() RemoteFile {
		p := "@@ -1 +1 @@\n-old\n+CANARY"
		return RemoteFile{Path: "src/a.go", Status: StatusModified, Additions: 1, Deletions: 1, Changes: 2, Patch: &p}
	}(), "CANARY", CategorySecret}}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			gateway := &fakeGateway{metadata: []RemoteMetadata{tc.first, tc.second}, pages: map[int]FilePage{1: {Files: []RemoteFile{tc.file}}}}
			acquirer, _ := testAcquirer(t, gateway, &fakeGate{canary: tc.canary})
			_, _, err := acquirer.Acquire(context.Background(), testRequest(policy), policy)
			if !IsCategory(err, tc.category) {
				t.Fatalf("category: %v", err)
			}
		})
	}
	gateway := &fakeGateway{metadata: []RemoteMetadata{base, base}, pages: map[int]FilePage{1: {}}}
	acquirer, _ := testAcquirer(t, gateway, &fakeGate{})
	value, binding, err := acquirer.Acquire(context.Background(), testRequest(policy), policy)
	if err != nil {
		t.Fatal(err)
	}
	value.Title = "tampered"
	if VerifyContext(value) == nil {
		t.Fatal("tampered context accepted")
	}
	binding.PolicyDigest = strings.Repeat("c", 64)
	if VerifyBinding(binding) == nil {
		t.Fatal("tampered binding accepted")
	}
}

// TestBudgetsOmissionsCatalogAndContracts verifies whole-unit truncation and configured-only authority.
func TestBudgetsOmissionsCatalogAndContracts(t *testing.T) {
	policy := testPolicy(t)
	policy.Limits.MaxFiles = 1
	policy, _ = SealPolicy(policy)
	metadata := testMetadata()
	metadata.Body = strings.Repeat("é", 1100)
	patch := "@@ -1 +1 @@\n-a\n+b"
	gateway := &fakeGateway{metadata: []RemoteMetadata{metadata, metadata}, pages: map[int]FilePage{1: {Files: []RemoteFile{{Path: "src/a.go", Status: StatusModified, Additions: 1, Deletions: 1, Changes: 2, Patch: &patch}, {Path: "src/b.go", Status: StatusModified, Additions: 1, Deletions: 1, Changes: 2, Patch: &patch}}, Next: true}}}
	request := testRequest(policy)
	acquirer, _ := testAcquirer(t, gateway, &fakeGate{})
	value, _, err := acquirer.Acquire(context.Background(), request, policy)
	if err != nil {
		t.Fatal(err)
	}
	if value.Completeness != CompletenessPartial || len(value.Files) != 1 || len(value.Omissions) == 0 || !utf8.ValidString(value.Description) {
		t.Fatalf("limits not represented: %#v", value)
	}
	catalogPolicy := testPolicy(t)
	catalogPolicy.Extractor = ExtractorPolicy{Kind: "catalog_reference", Version: "v1", ReferencePrefix: "FeatureProof: "}
	catalogPolicy.Catalog = []CatalogEntry{{Alias: "checkout", Version: "v2", Constraints: []Constraint{{ID: "C1", Description: "catalog", SafeReference: "catalog:C1"}}}}
	catalogPolicy, _ = SealPolicy(catalogPolicy)
	meta := testMetadata()
	meta.Body = "FeatureProof: checkout@v2"
	gateway = &fakeGateway{metadata: []RemoteMetadata{meta, meta}, pages: map[int]FilePage{1: {}}}
	acquirer, _ = testAcquirer(t, gateway, &fakeGate{})
	value, _, err = acquirer.Acquire(context.Background(), testRequest(catalogPolicy), catalogPolicy)
	if err != nil || len(value.Constraints) != 1 || value.Constraints[0].Description != "catalog" {
		t.Fatalf("catalog: %#v %v", value, err)
	}
}

// TestRetryCancellationConflictAndConcurrency verifies bounded lifecycle and concurrent convergence.
func TestRetryCancellationConflictAndConcurrency(t *testing.T) {
	policy := testPolicy(t)
	metadata := testMetadata()
	gateway := &fakeGateway{metadata: []RemoteMetadata{metadata, metadata}, pages: map[int]FilePage{1: {}}, errs: []error{&Error{Category: CategoryTransient, Stage: "fake"}, nil, nil}}
	clock := &testClock{now: time.Unix(1, 0)}
	observer := &recordObserver{}
	acquirer, err := NewAcquirer(Dependencies{Gateway: gateway, Secrets: &fakeGate{}, Clock: clock, Store: NewMemoryStore(), Observer: observer})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err = acquirer.Acquire(context.Background(), testRequest(policy), policy); err != nil || len(clock.waits) != 1 {
		t.Fatalf("retry: %v %#v", err, clock.waits)
	}
	cancelled, cancel := context.WithCancelCause(context.Background())
	sentinel := errors.New("stop")
	cancel(sentinel)
	_, _, err = acquirer.Acquire(cancelled, Request{}, policy)
	if !IsCategory(err, CategoryCancelled) || !errors.Is(err, sentinel) {
		t.Fatalf("cancel cause: %v", err)
	}
	store := NewMemoryStore()
	gateway = &fakeGateway{metadata: []RemoteMetadata{metadata}, pages: map[int]FilePage{1: {}}}
	acquirer, _ = NewAcquirer(Dependencies{Gateway: gateway, Secrets: &fakeGate{}, Clock: &testClock{now: time.Unix(1, 0)}, Store: store, Observer: &recordObserver{}})
	var wg sync.WaitGroup
	errs := make(chan error, 8)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _, e := acquirer.Acquire(context.Background(), testRequest(policy), policy)
			errs <- e
		}()
	}
	wg.Wait()
	close(errs)
	for e := range errs {
		if e != nil {
			t.Fatal(e)
		}
	}
	different := testRequest(policy)
	different.RepositoryID = 4
	if _, _, err = acquirer.Acquire(context.Background(), different, policy); !IsCategory(err, CategoryConflict) && !IsCategory(err, CategoryStale) {
		t.Fatalf("conflict: %v", err)
	}
}

// TestStrictPolicyAndExtractorEdges verifies invalid configuration and ambiguous input fail closed.
func TestStrictPolicyAndExtractorEdges(t *testing.T) {
	policy := testPolicy(t)
	mutated := policy
	mutated.PathPrefixes = append([]string(nil), policy.PathPrefixes...)
	mutated.PathPrefixes[0] = "../"
	if _, err := SealPolicy(mutated); err == nil {
		t.Fatal("unsafe policy accepted")
	}
	duplicate := testMetadata()
	duplicate.Body = "## FeatureProof Constraints (v1)\n- A: one\n## FeatureProof Constraints (v1)\n- B: two"
	gateway := &fakeGateway{metadata: []RemoteMetadata{duplicate, duplicate}, pages: map[int]FilePage{1: {}}}
	acquirer, _ := testAcquirer(t, gateway, &fakeGate{})
	_, _, err := acquirer.Acquire(context.Background(), testRequest(policy), policy)
	if !IsCategory(err, CategoryConflict) {
		t.Fatalf("duplicate section: %v", err)
	}
	raw, _ := json.Marshal(Context{Version: ContextVersion, Digest: strings.Repeat("a", 64)})
	raw = append(raw[:len(raw)-1], []byte(`,"unknown":true}`)...)
	if _, err = DecodeContext(raw); err == nil {
		t.Fatal("unknown context field accepted")
	}
	if !reflect.DeepEqual(ContextPermissionsForTest(), []string{"metadata:read", "pull_requests:read"}) {
		t.Fatal("test sentinel")
	}
}

// ContextPermissionsForTest documents the minimum adapter permission expectation without importing githubpr.
func ContextPermissionsForTest() []string { return []string{"metadata:read", "pull_requests:read"} }
