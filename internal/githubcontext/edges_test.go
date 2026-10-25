package githubcontext

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/asierhr/featureProof/internal/contextassembly"
)

// failingStore injects durable boundary failures without performing I/O.
type failingStore struct{ loadErr, commitErr error }

// Load returns the configured failure.
func (s failingStore) Load(context.Context, string) (string, Context, Binding, bool, error) {
	return "", Context{}, Binding{}, false, s.loadErr
}

// Commit returns the configured failure.
func (s failingStore) Commit(context.Context, string, string, Context, Binding) error {
	return s.commitErr
}

// TestCanaryGateAndCompositionFailures verifies secret patterns and missing dependencies fail closed.
func TestCanaryGateAndCompositionFailures(t *testing.T) {
	gate := NewCanaryGate([][]byte{[]byte("PRIVATE-CANARY")})
	for _, value := range []string{"PRIVATE-CANARY", "Authorization: Bearer abcdefghi", "token=abcdefgh"} {
		if err := gate.Check(context.Background(), []byte(value)); err == nil {
			t.Fatalf("secret accepted: %s", value)
		}
	}
	if err := gate.Check(context.Background(), []byte("safe")); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := gate.Check(ctx, []byte("safe")); err == nil {
		t.Fatal("cancel ignored")
	}
	if _, err := NewAcquirer(Dependencies{}); !IsCategory(err, CategoryInvalid) {
		t.Fatalf("empty composition: %v", err)
	}
	policy := testPolicy(t)
	gateway := &fakeGateway{}
	observer := &recordObserver{}
	acquirer, _ := NewAcquirer(Dependencies{Gateway: gateway, Secrets: &fakeGate{}, Clock: &testClock{now: time.Unix(1, 0)}, Store: failingStore{loadErr: errors.New("disk")}, Observer: observer})
	if _, _, err := acquirer.Acquire(context.Background(), testRequest(policy), policy); !IsCategory(err, CategoryPersistence) {
		t.Fatalf("load error: %v", err)
	}
	acquirer, _ = NewAcquirer(Dependencies{Gateway: gateway, Secrets: &fakeGate{}, Clock: &testClock{now: time.Unix(1, 0)}, Store: failingStore{}, Observer: observer})
	bad := testRequest(policy)
	bad.PolicyDigest = strings.Repeat("f", 64)
	if _, _, err := acquirer.Acquire(context.Background(), bad, policy); !IsCategory(err, CategoryUnauthorized) {
		t.Fatalf("policy widening: %v", err)
	}
}

// TestRevalidateDetectsCurrentObservationDrift verifies every fresh observation participates in binding.
func TestRevalidateDetectsCurrentObservationDrift(t *testing.T) {
	policy := testPolicy(t)
	metadata := testMetadata()
	gateway := &fakeGateway{metadata: []RemoteMetadata{metadata}, pages: map[int]FilePage{1: {}}}
	acquirer, _ := testAcquirer(t, gateway, &fakeGate{})
	request := testRequest(policy)
	value, binding, err := acquirer.Acquire(context.Background(), request, policy)
	if err != nil {
		t.Fatal(err)
	}
	if err = acquirer.Revalidate(context.Background(), request, policy, binding); err != nil {
		t.Fatal(err)
	}
	changed := metadata
	changed.Body = "changed"
	gateway.metadata = []RemoteMetadata{changed}
	if err = acquirer.Revalidate(context.Background(), request, policy, binding); !IsCategory(err, CategoryStale) {
		t.Fatalf("drift accepted: %v", err)
	}
	retryingGateway := &fakeGateway{metadata: []RemoteMetadata{metadata, metadata}, pages: map[int]FilePage{1: {}}, errs: []error{&Error{Category: CategoryRateLimited, Stage: "fake"}}}
	clock := &testClock{now: time.Unix(1, 0)}
	retrying, createErr := NewAcquirer(Dependencies{Gateway: retryingGateway, Secrets: &fakeGate{}, Clock: clock, Store: NewMemoryStore(), Observer: &recordObserver{}})
	if createErr != nil {
		t.Fatal(createErr)
	}
	if _, _, err = retrying.Refresh(context.Background(), request, policy); err != nil || len(clock.waits) != 1 {
		t.Fatalf("refresh retry: %v %#v", err, clock.waits)
	}
	binding.ContextDigest = value.Digest
	binding.Digest = "bad"
	if err = acquirer.Revalidate(context.Background(), request, policy, binding); err == nil {
		t.Fatal("invalid binding accepted")
	}
}

// TestAcquisitionLimitAndOmissionMatrix verifies all filtering decisions remain explicit and whole-unit.
func TestAcquisitionLimitAndOmissionMatrix(t *testing.T) {
	policy := testPolicy(t)
	policy.Limits.MaxPatchBytesPerFile = 100
	policy.Limits.MaxPatchBytesTotal = 1000
	policy.Limits.MaxHunks = 1
	policy.Limits.MaxLines = 2
	policy, _ = SealPolicy(policy)
	metadata := testMetadata()
	validPatch := "@@ -1 +1 @@\n-a\n+b"
	oversized := validPatch + strings.Repeat("x", 200)
	files := []RemoteFile{{Path: "other/a.go", Status: StatusModified, Additions: 1, Deletions: 1, Changes: 2, Patch: &validPatch}, {Path: "src/a.bin", Status: StatusModified, Additions: 1, Deletions: 1, Changes: 2, Patch: &validPatch}, {Path: "src/a.go", Status: StatusAdded, Additions: 1, Changes: 1, Patch: nil}, {Path: "src/b.go", Status: StatusModified, Additions: 1, Deletions: 1, Changes: 2, Patch: &oversized}, {Path: "src/c.go", Status: StatusModified, Additions: 1, Deletions: 1, Changes: 2, Patch: &validPatch}, {Path: "src/d.go", Status: StatusModified, Additions: 1, Deletions: 1, Changes: 2, Patch: &validPatch}}
	gateway := &fakeGateway{metadata: []RemoteMetadata{metadata, metadata}, pages: map[int]FilePage{1: {Files: files}}}
	acquirer, _ := testAcquirer(t, gateway, &fakeGate{})
	value, _, err := acquirer.Acquire(context.Background(), testRequest(policy), policy)
	if err != nil {
		t.Fatal(err)
	}
	reasons := map[OmissionReason]bool{}
	for _, omission := range value.Omissions {
		reasons[omission.Reason] = true
	}
	for _, reason := range []OmissionReason{OmissionPath, OmissionType, OmissionPatch, OmissionPatchSize, OmissionHunks} {
		if !reasons[reason] {
			t.Fatalf("missing omission %s: %#v", reason, value.Omissions)
		}
	}
	if value.Completeness != CompletenessPartial {
		t.Fatal("omissions did not mark partial")
	}
	itemPolicy := testPolicy(t)
	itemPolicy.Limits.MaxItems = 1
	itemPolicy, _ = SealPolicy(itemPolicy)
	contractWithoutPatch := RemoteFile{Path: "api/openapi.yaml", Status: StatusRemoved, Deletions: 1, Changes: 1}
	gateway = &fakeGateway{metadata: []RemoteMetadata{metadata, metadata}, pages: map[int]FilePage{1: {Files: []RemoteFile{contractWithoutPatch, {Path: "src/item.go", Status: StatusModified, Additions: 1, Deletions: 1, Changes: 2, Patch: &validPatch}}}}}
	acquirer, _ = testAcquirer(t, gateway, &fakeGate{})
	value, _, err = acquirer.Acquire(context.Background(), testRequest(itemPolicy), itemPolicy)
	if err != nil || len(value.Files) != 0 || len(value.Contracts) != 1 || value.Contracts[0].Status != StatusRemoved {
		t.Fatalf("item/contract limits: %#v %v", value, err)
	}
	producer, err := DiffProducer(value, contextassembly.AuthorityBinding{ProjectID: "2", RepositoryID: "2", Revision: testOID})
	if err != nil {
		t.Fatal(err)
	}
	projection, err := producer.Produce(context.Background(), contextassembly.Request{})
	if err != nil || len(projection.Items) != 1 {
		t.Fatalf("projection item budget: %#v %v", projection, err)
	}
	if _, err = DiffProducer(value, contextassembly.AuthorityBinding{RepositoryID: "other", Revision: testOID}); !IsCategory(err, CategoryUnauthorized) {
		t.Fatalf("projection authority: %v", err)
	}
	partial := value
	partial.Completeness = CompletenessInsufficient
	partial.Digest = ""
	partial.Digest, _ = digestValue(partial)
	producer, err = DiffProducer(partial, contextassembly.AuthorityBinding{ProjectID: "2", RepositoryID: "2", Revision: testOID})
	if err != nil {
		t.Fatal(err)
	}
	projection, err = producer.Produce(context.Background(), contextassembly.Request{})
	if err != nil || projection.Binding.Completeness != contextassembly.CompletenessInsufficient {
		t.Fatalf("insufficient projection: %#v %v", projection, err)
	}
}

// TestProviderIntegrityAndParsingEdges verifies invalid pages, metadata, patches and catalogs never project partial authority.
func TestProviderIntegrityAndParsingEdges(t *testing.T) {
	policy := testPolicy(t)
	metadata := testMetadata()
	invalidMetadata := []RemoteMetadata{func() RemoteMetadata { v := metadata; v.State = "closed"; return v }(), func() RemoteMetadata { v := metadata; v.BaseOID = "short"; return v }(), func() RemoteMetadata { v := metadata; v.RepositoryID = 99; return v }()}
	for _, value := range invalidMetadata {
		gateway := &fakeGateway{metadata: []RemoteMetadata{value}, pages: map[int]FilePage{}}
		acquirer, _ := testAcquirer(t, gateway, &fakeGate{})
		if _, _, err := acquirer.Acquire(context.Background(), testRequest(policy), policy); !IsCategory(err, CategoryStale) {
			t.Fatalf("metadata accepted: %#v %v", value, err)
		}
	}
	for _, patch := range []string{"not a hunk", "@@ -1 +1 @@\ninvalid", "\xff"} {
		file := RemoteFile{Path: "src/a.go", Status: StatusModified, Additions: 1, Deletions: 1, Changes: 2, Patch: &patch}
		gateway := &fakeGateway{metadata: []RemoteMetadata{metadata, metadata}, pages: map[int]FilePage{1: {Files: []RemoteFile{file}}}}
		acquirer, _ := testAcquirer(t, gateway, &fakeGate{})
		if _, _, err := acquirer.Acquire(context.Background(), testRequest(policy), policy); !IsCategory(err, CategoryIntegrity) {
			t.Fatalf("patch accepted: %q %v", patch, err)
		}
	}
	catalog := testPolicy(t)
	catalog.Extractor = ExtractorPolicy{Kind: "catalog_reference", Version: "v1", ReferencePrefix: "FP: "}
	catalog.Catalog = []CatalogEntry{{Alias: "one", Version: "v1", Constraints: []Constraint{{ID: "A", Description: "a", SafeReference: "catalog:A"}}}}
	catalog, _ = SealPolicy(catalog)
	for _, body := range []string{"FP: missing@v1", "FP: one@v1\nFP: one@v1"} {
		v := metadata
		v.Body = body
		gateway := &fakeGateway{metadata: []RemoteMetadata{v, v}, pages: map[int]FilePage{1: {}}}
		acquirer, _ := testAcquirer(t, gateway, &fakeGate{})
		if _, _, err := acquirer.Acquire(context.Background(), testRequest(catalog), catalog); err == nil {
			t.Fatalf("catalog accepted: %s", body)
		}
	}
}

// TestPolicyValidationAndModelEdges verifies duplicate authority, trailing JSON and utility error paths.
func TestPolicyValidationAndModelEdges(t *testing.T) {
	base := testPolicy(t)
	cases := []Policy{func() Policy { v := base; v.AllowedStatuses = []FileStatus{StatusAdded, StatusAdded}; return v }(), func() Policy {
		v := base
		v.ContractRoutes = []ContractRoute{{Path: "api/openapi.yaml", ContractID: "a", Version: "v1"}, {Path: "api/openapi.yaml", ContractID: "b", Version: "v1"}}
		return v
	}(), func() Policy { v := base; v.Extractor = ExtractorPolicy{Kind: "unknown", Version: "v1"}; return v }(), func() Policy { v := base; v.Limits.MaxPages = 0; return v }()}
	for _, value := range cases {
		if _, err := SealPolicy(value); err == nil {
			t.Fatalf("invalid policy accepted: %#v", value)
		}
	}
	if err := ValidateRequest(Request{}); err == nil {
		t.Fatal("invalid request accepted")
	}
	if _, err := DecodeContext([]byte(`{} {}`)); err == nil {
		t.Fatal("trailing JSON accepted")
	}
	if (&Error{Category: CategoryInternal, Stage: "x"}).Error() == "" {
		t.Fatal("empty error")
	}
	if _, err := digestValue(make(chan int)); !IsCategory(err, CategoryInternal) {
		t.Fatalf("unencodable digest: %v", err)
	}
	if minInt(2, 1) != 1 || minInt(1, 2) != 1 {
		t.Fatal("minimum broken")
	}
	deadline, cancel := context.WithDeadline(context.Background(), time.Unix(0, 0))
	defer cancel()
	if err := contextError(deadline, "test"); !IsCategory(err, CategoryTimeout) {
		t.Fatalf("deadline category: %v", err)
	}
}

// TestLocalStoreBoundaryErrorsAndWallClock verifies absent receipts, invalid commits and cancellable timers.
func TestLocalStoreBoundaryErrorsAndWallClock(t *testing.T) {
	if _, err := OpenLocalStore(""); !IsCategory(err, CategoryInvalid) {
		t.Fatalf("empty root: %v", err)
	}
	store, err := OpenLocalStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if _, _, _, found, err := store.Load(context.Background(), "missing"); err != nil || found {
		t.Fatalf("missing receipt: %v %t", err, found)
	}
	if _, _, _, _, err = store.Load(context.Background(), "../bad"); !IsCategory(err, CategoryInvalid) {
		t.Fatalf("unsafe key: %v", err)
	}
	if err = store.Commit(context.Background(), "bad/key", "bad", Context{}, Binding{}); !IsCategory(err, CategoryInvalid) {
		t.Fatalf("invalid commit: %v", err)
	}
	clock := WallClock{}
	if clock.Now().IsZero() {
		t.Fatal("zero time")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err = clock.Wait(ctx, time.Hour); err == nil {
		t.Fatal("cancelled wait succeeded")
	}
	observer, err := OpenLocalRecordObserver(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err = observer.Observe(ctx, Record{}); err == nil {
		t.Fatal("observer ignored cancellation")
	}
	root := t.TempDir()
	name := filepath.Join(root, "large")
	if err = os.WriteFile(name, []byte("xx"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err = readBounded(name, 1); !IsCategory(err, CategoryIntegrity) {
		t.Fatalf("oversize read: %v", err)
	}
}
