package githubcontext

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// responseLossObserver persists through the real observer and simulates loss of its first acknowledgement.
type responseLossObserver struct {
	inner *LocalRecordObserver
	lost  bool
}

// failingRecordObserver simulates a terminal journal that cannot confirm durable publication.
type failingRecordObserver struct{ err error }

// Observe returns the configured publication failure without retaining the record.
func (o failingRecordObserver) Observe(context.Context, Record) error { return o.err }

// recoveringRecordObserver fails one confirmation before delegating later redelivery to durable storage.
type recoveringRecordObserver struct {
	inner *LocalRecordObserver
	err   error
	calls int
}

// Observe injects one failure and then confirms the same stable terminal key durably.
func (o *recoveringRecordObserver) Observe(ctx context.Context, record Record) error {
	o.calls++
	if o.calls == 1 {
		return o.err
	}
	return o.inner.Observe(ctx, record)
}

// readDurableRecords strictly reloads every immutable terminal record currently confirmed by one observer.
func readDurableRecords(t *testing.T, observer *LocalRecordObserver) []Record {
	t.Helper()
	entries, err := os.ReadDir(observer.records)
	if err != nil {
		t.Fatal(err)
	}
	records := make([]Record, 0, len(entries))
	for _, entry := range entries {
		id := strings.TrimSuffix(entry.Name(), ".json")
		record, readErr := observer.Read(context.Background(), id)
		if readErr != nil {
			t.Fatal(readErr)
		}
		records = append(records, record)
	}
	return records
}

// Observe commits before returning one injected response-loss failure.
func (o *responseLossObserver) Observe(ctx context.Context, record Record) error {
	if err := o.inner.Observe(ctx, record); err != nil {
		return err
	}
	if !o.lost {
		o.lost = true
		return errors.New("observer response lost")
	}
	return nil
}

// cancellingStore changes caller context during one selected durable boundary.
type cancellingStore struct {
	cancel   context.CancelCauseFunc
	onCommit bool
	cause    error
}

// Load cancels during the read boundary when configured for load.
func (s cancellingStore) Load(context.Context, string) (string, Context, Binding, bool, error) {
	if !s.onCommit {
		s.cancel(s.cause)
		return "", Context{}, Binding{}, false, s.cause
	}
	return "", Context{}, Binding{}, false, nil
}

// Commit cancels during the write boundary when configured for commit.
func (s cancellingStore) Commit(context.Context, string, string, Context, Binding) error {
	if s.onCommit {
		s.cancel(s.cause)
	}
	return s.cause
}

// signTestContext recalculates an outer digest for adversarial semantic-validation probes.
func signTestContext(value Context) Context {
	value.Digest = ""
	value.Digest, _ = digestValue(value)
	return value
}

// signTestBinding recalculates a binding digest for adversarial semantic-validation probes.
func signTestBinding(value Binding) Binding {
	value.Digest = ""
	value.Digest, _ = digestValue(value)
	return value
}

// acquiredFixture returns one fully verified two-file context and binding.
func acquiredFixture(t *testing.T) (Context, Binding, Policy, Request) {
	t.Helper()
	policy := testPolicy(t)
	metadata := testMetadata()
	patch := "@@ -1 +1 @@\n-old\n+new"
	gateway := &fakeGateway{metadata: []RemoteMetadata{metadata, metadata}, pages: map[int]FilePage{1: {Files: []RemoteFile{{Path: "src/a.go", Status: StatusModified, Additions: 1, Deletions: 1, Changes: 2, Patch: &patch}, {Path: "api/openapi.yaml", Status: StatusModified, Additions: 1, Deletions: 1, Changes: 2, Patch: &patch}}}}}
	acquirer, _ := testAcquirer(t, gateway, &fakeGate{})
	request := testRequest(policy)
	value, binding, err := acquirer.Acquire(context.Background(), request, policy)
	if err != nil {
		t.Fatal(err)
	}
	return value, binding, policy, request
}

// TestLocalRecordObserverIsDurableExactlyOnceAndContentFree closes REV-1 across response loss and reopen.
func TestLocalRecordObserverIsDurableExactlyOnceAndContentFree(t *testing.T) {
	root := t.TempDir()
	durable, err := OpenLocalRecordObserver(root)
	if err != nil {
		t.Fatal(err)
	}
	observer := &responseLossObserver{inner: durable}
	policy := testPolicy(t)
	request := testRequest(policy)
	metadata := testMetadata()
	gateway := &fakeGateway{metadata: []RemoteMetadata{metadata, metadata}, pages: map[int]FilePage{1: {}}}
	acquirer, err := NewAcquirer(Dependencies{Gateway: gateway, Secrets: &fakeGate{}, Clock: &testClock{now: time.Unix(1, 0)}, Store: NewMemoryStore(), Observer: observer})
	if err != nil {
		t.Fatal(err)
	}
	if value, binding, acquireErr := acquirer.Acquire(context.Background(), request, policy); !IsCategory(acquireErr, CategoryPersistence) || value.Digest == "" || binding.Digest == "" {
		t.Fatalf("lost observer acknowledgement changed confirmed result: %#v %#v %v", value, binding, acquireErr)
	}
	if _, _, err = acquirer.Acquire(context.Background(), request, policy); err != nil {
		t.Fatal(err)
	}
	reopened, err := OpenLocalRecordObserver(root)
	if err != nil {
		t.Fatal(err)
	}
	records := readDurableRecords(t, reopened)
	if len(records) != 1 || records[0].Outcome != "success" || records[0].Attempts != 1 || records[0].ContextDigest == "" {
		t.Fatalf("records=%#v", records)
	}
	entries, err := os.ReadDir(filepath.Join(root, "records"))
	if err != nil || len(entries) != 1 {
		t.Fatalf("entries=%d err=%v", len(entries), err)
	}
	raw, err := os.ReadFile(filepath.Join(root, "records", entries[0].Name()))
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{metadata.Title, metadata.Body, "src/a.go", "Authorization", "Bearer"} {
		if strings.Contains(string(raw), forbidden) {
			t.Fatalf("record retained external payload %q", forbidden)
		}
	}
}

// TestTerminalRecordsPreserveFailureOutcomes closes REV-1 for stale, insufficient, cancellation and persistence.
func TestTerminalRecordsPreserveFailureOutcomes(t *testing.T) {
	policy := testPolicy(t)
	base := testMetadata()
	tests := []struct {
		name     string
		category Category
		prepare  func(Request, *LocalRecordObserver) (*Acquirer, context.Context)
	}{
		{name: "stale", category: CategoryStale, prepare: func(_ Request, observer *LocalRecordObserver) (*Acquirer, context.Context) {
			changed := base
			changed.HeadOID = strings.Repeat("c", 40)
			acquirer, _ := NewAcquirer(Dependencies{Gateway: &fakeGateway{metadata: []RemoteMetadata{changed}, pages: map[int]FilePage{}}, Secrets: &fakeGate{}, Clock: &testClock{now: time.Unix(1, 0)}, Store: NewMemoryStore(), Observer: observer})
			return acquirer, context.Background()
		}},
		{name: "insufficient", category: CategoryInsufficient, prepare: func(_ Request, observer *LocalRecordObserver) (*Acquirer, context.Context) {
			limited := policy
			limited.Limits.MaxBodyBytes = 8
			limited, _ = SealPolicy(limited)
			policy = limited
			acquirer, _ := NewAcquirer(Dependencies{Gateway: &fakeGateway{metadata: []RemoteMetadata{base, base}, pages: map[int]FilePage{1: {}}}, Secrets: &fakeGate{}, Clock: &testClock{now: time.Unix(1, 0)}, Store: NewMemoryStore(), Observer: observer})
			return acquirer, context.Background()
		}},
		{name: "cancelled", category: CategoryCancelled, prepare: func(_ Request, observer *LocalRecordObserver) (*Acquirer, context.Context) {
			acquirer, _ := NewAcquirer(Dependencies{Gateway: &fakeGateway{}, Secrets: &fakeGate{}, Clock: &testClock{now: time.Unix(1, 0)}, Store: NewMemoryStore(), Observer: observer})
			ctx, cancel := context.WithCancelCause(context.Background())
			cancel(errors.New("caller stopped"))
			return acquirer, ctx
		}},
		{name: "persistence", category: CategoryPersistence, prepare: func(_ Request, observer *LocalRecordObserver) (*Acquirer, context.Context) {
			acquirer, _ := NewAcquirer(Dependencies{Gateway: &fakeGateway{}, Secrets: &fakeGate{}, Clock: &testClock{now: time.Unix(1, 0)}, Store: failingStore{loadErr: errors.New("disk")}, Observer: observer})
			return acquirer, context.Background()
		}},
	}
	for index, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			casePolicy := policy
			request := testRequest(casePolicy)
			request.RunID = "run-outcome-" + string(rune('a'+index))
			request.IdempotencyKey = request.RunID
			observer, err := OpenLocalRecordObserver(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			acquirer, caller := test.prepare(request, observer)
			request.PolicyDigest = policy.Digest
			_, _, _ = acquirer.Acquire(caller, request, policy)
			records := readDurableRecords(t, observer)
			if len(records) != 1 || records[0].Category != test.category || records[0].Outcome != string(test.category) {
				t.Fatalf("records=%#v", records)
			}
		})
	}
}

// TestTerminalIdentityHandlesInvalidFailureAndLaterSuccess closes REV-1 for malformed input and non-poisoning outcomes.
func TestTerminalIdentityHandlesInvalidFailureAndLaterSuccess(t *testing.T) {
	policy := testPolicy(t)
	metadata := testMetadata()
	observer, err := OpenLocalRecordObserver(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	invalid := testRequest(policy)
	invalid.RunID = "invalid run id"
	invalidAcquirer, _ := NewAcquirer(Dependencies{Gateway: &fakeGateway{}, Secrets: &fakeGate{}, Clock: &testClock{now: time.Unix(1, 0)}, Store: NewMemoryStore(), Observer: observer})
	if _, _, err = invalidAcquirer.Acquire(context.Background(), invalid, policy); !IsCategory(err, CategoryInvalid) {
		t.Fatalf("invalid input category changed: %v", err)
	}
	if _, _, err = invalidAcquirer.Acquire(context.Background(), invalid, policy); !IsCategory(err, CategoryInvalid) {
		t.Fatalf("invalid terminal redelivery changed: %v", err)
	}
	records := readDurableRecords(t, observer)
	if len(records) != 1 || records[0].Category != CategoryInvalid || records[0].RunID != "" {
		t.Fatalf("invalid terminal record: %#v", records)
	}
	publicationFailure := errors.New("journal unavailable")
	failingAcquirer, _ := NewAcquirer(Dependencies{Gateway: &fakeGateway{metadata: []RemoteMetadata{metadata, metadata}, pages: map[int]FilePage{1: {}}}, Secrets: &fakeGate{}, Clock: &testClock{now: time.Unix(1, 0)}, Store: NewMemoryStore(), Observer: failingRecordObserver{err: publicationFailure}})
	if value, binding, acquireErr := failingAcquirer.Acquire(context.Background(), testRequest(policy), policy); !IsCategory(acquireErr, CategoryPersistence) || !errors.Is(acquireErr, publicationFailure) || value.Digest == "" || binding.Digest == "" {
		t.Fatalf("observer failure changed confirmed result: %#v %#v %v", value, binding, acquireErr)
	}
	transitionObserver, err := OpenLocalRecordObserver(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	stale := metadata
	stale.HeadOID = strings.Repeat("c", 40)
	transitionGateway := &fakeGateway{metadata: []RemoteMetadata{stale, metadata, metadata}, pages: map[int]FilePage{1: {}}}
	transitionAcquirer, _ := NewAcquirer(Dependencies{Gateway: transitionGateway, Secrets: &fakeGate{}, Clock: &testClock{now: time.Unix(1, 0)}, Store: NewMemoryStore(), Observer: transitionObserver})
	request := testRequest(policy)
	if _, _, err = transitionAcquirer.Acquire(context.Background(), request, policy); !IsCategory(err, CategoryStale) {
		t.Fatalf("expected stale first terminal: %v", err)
	}
	if _, _, err = transitionAcquirer.Acquire(context.Background(), request, policy); err != nil {
		t.Fatalf("later success was poisoned: %v", err)
	}
	if _, _, err = transitionAcquirer.Acquire(context.Background(), request, policy); err != nil {
		t.Fatalf("same success redelivery failed: %v", err)
	}
	records = readDurableRecords(t, transitionObserver)
	if len(records) != 2 {
		t.Fatalf("terminal identities did not converge: %#v", records)
	}
	outcomes := map[string]int{}
	for _, record := range records {
		outcomes[record.Outcome]++
	}
	if outcomes[string(CategoryStale)] != 1 || outcomes["success"] != 1 {
		t.Fatalf("unexpected terminal outcomes: %#v", outcomes)
	}
}

// TestObserverFailuresPreserveConfirmedBindingAndRedeliver closes REV-9 for error, cancellation and timeout.
func TestObserverFailuresPreserveConfirmedBindingAndRedeliver(t *testing.T) {
	policy := testPolicy(t)
	metadata := testMetadata()
	tests := []struct {
		name     string
		failure  error
		category Category
	}{
		{name: "error", failure: errors.New("observer unavailable"), category: CategoryPersistence},
		{name: "cancelled", failure: context.Canceled, category: CategoryCancelled},
		{name: "timeout", failure: context.DeadlineExceeded, category: CategoryTimeout},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			durable, err := OpenLocalRecordObserver(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			observer := &recoveringRecordObserver{inner: durable, err: test.failure}
			gateway := &fakeGateway{metadata: []RemoteMetadata{metadata, metadata}, pages: map[int]FilePage{1: {}}}
			acquirer, err := NewAcquirer(Dependencies{Gateway: gateway, Secrets: &fakeGate{}, Clock: &testClock{now: time.Unix(1, 0)}, Store: NewMemoryStore(), Observer: observer})
			if err != nil {
				t.Fatal(err)
			}
			request := testRequest(policy)
			value, binding, acquireErr := acquirer.Acquire(context.Background(), request, policy)
			if !IsCategory(acquireErr, test.category) || !errors.Is(acquireErr, test.failure) || value.Digest == "" || binding.Digest == "" {
				t.Fatalf("confirmed result changed: %#v %#v %v", value, binding, acquireErr)
			}
			gateway.mu.Lock()
			callsAfterFailure := len(gateway.calls)
			gateway.mu.Unlock()
			redelivered, redeliveredBinding, redeliveryErr := acquirer.Acquire(context.Background(), request, policy)
			if redeliveryErr != nil || redelivered.Digest != value.Digest || redeliveredBinding.Digest != binding.Digest {
				t.Fatalf("redelivery failed: %#v %#v %v", redelivered, redeliveredBinding, redeliveryErr)
			}
			gateway.mu.Lock()
			callsAfterRedelivery := len(gateway.calls)
			gateway.mu.Unlock()
			if callsAfterRedelivery != callsAfterFailure || len(readDurableRecords(t, durable)) != 1 {
				t.Fatalf("redelivery repeated external work: before=%d after=%d", callsAfterFailure, callsAfterRedelivery)
			}
		})
	}
}

// TestPolicyCatalogAndExtensionsAreCanonical closes REV-4 for ambiguous trusted configuration.
func TestPolicyCatalogAndExtensionsAreCanonical(t *testing.T) {
	base := testPolicy(t)
	valid := base
	valid.Extractor = ExtractorPolicy{Kind: "catalog_reference", Version: "v1", ReferencePrefix: "FP: "}
	valid.Catalog = []CatalogEntry{{Alias: "z", Version: "v2", Constraints: []Constraint{{ID: "B", Description: "two", SafeReference: "catalog:B"}, {ID: "A", Description: "one", SafeReference: "catalog:A"}}}, {Alias: "a", Version: "v1", Constraints: []Constraint{{ID: "C", Description: "three", SafeReference: "catalog:C"}}}}
	first, err := SealPolicy(valid)
	if err != nil {
		t.Fatal(err)
	}
	valid.Catalog[0], valid.Catalog[1] = valid.Catalog[1], valid.Catalog[0]
	second, err := SealPolicy(valid)
	if err != nil || first.Digest != second.Digest {
		t.Fatalf("catalog permutation changed authority: %v", err)
	}
	cases := []Policy{
		func() Policy { v := base; v.TextExtensions = []string{"go"}; return v }(),
		func() Policy { v := base; v.TextExtensions = []string{".GO"}; return v }(),
		func() Policy { v := base; v.PathPrefixes = []string{"src/", "src/api/"}; return v }(),
		func() Policy { v := first; v.Catalog = append(v.Catalog, v.Catalog[0]); return v }(),
		func() Policy { v := first; v.Catalog[0].Constraints[0].SafeReference = ""; return v }(),
		func() Policy { v := first; v.Catalog[0].Constraints[0].Description = "  "; return v }(),
		func() Policy {
			v := first
			v.Catalog[1].Constraints[1].SafeReference = v.Catalog[1].Constraints[0].SafeReference
			return v
		}(),
		func() Policy {
			v := first
			v.Catalog[0].Constraints = append(v.Catalog[0].Constraints, v.Catalog[0].Constraints[0])
			return v
		}(),
		func() Policy { v := first; v.Extractor.ReferencePrefix = "\nFP: "; return v }(),
	}
	for _, value := range cases {
		if _, err := SealPolicy(value); !IsCategory(err, CategoryInvalid) {
			t.Fatalf("invalid policy accepted: %#v err=%v", value, err)
		}
	}
}

// TestConstraintExtractionNeverPublishesByteTruncatedUnits closes REV-3 for Markdown and catalog sources.
func TestConstraintExtractionNeverPublishesByteTruncatedUnits(t *testing.T) {
	markdown := testPolicy(t)
	metadata := testMetadata()
	markdown.Limits.MaxBodyBytes = len([]byte(metadata.Body)) - 5
	markdown, _ = SealPolicy(markdown)
	gateway := &fakeGateway{metadata: []RemoteMetadata{metadata, metadata}, pages: map[int]FilePage{1: {}}}
	acquirer, _ := testAcquirer(t, gateway, &fakeGate{})
	if value, _, err := acquirer.Acquire(context.Background(), testRequest(markdown), markdown); !IsCategory(err, CategoryInsufficient) || value.Digest != "" || len(value.Constraints) != 0 {
		t.Fatalf("truncated markdown published: %#v %v", value, err)
	}
	separatedDuplicate := metadata
	separatedDuplicate.Body += "\n# Later\n" + markdown.Extractor.Marker + "\n- C2: second\n"
	gateway = &fakeGateway{metadata: []RemoteMetadata{separatedDuplicate, separatedDuplicate}, pages: map[int]FilePage{1: {}}}
	acquirer, _ = testAcquirer(t, gateway, &fakeGate{})
	if _, _, err := acquirer.Acquire(context.Background(), testRequest(testPolicy(t)), testPolicy(t)); !IsCategory(err, CategoryConflict) {
		t.Fatalf("separated duplicate Markdown section accepted: %v", err)
	}
	catalog := testPolicy(t)
	catalog.Extractor = ExtractorPolicy{Kind: "catalog_reference", Version: "v1", ReferencePrefix: "FP: "}
	catalog.Catalog = []CatalogEntry{{Alias: "safe", Version: "v1", Constraints: []Constraint{{ID: "C1", Description: "trusted", SafeReference: "catalog:C1"}}}}
	catalog.Limits.MaxBodyBytes = 4
	catalog, _ = SealPolicy(catalog)
	metadata.Body = "intro\nFP: safe@v1"
	gateway = &fakeGateway{metadata: []RemoteMetadata{metadata, metadata}, pages: map[int]FilePage{1: {}}}
	acquirer, _ = testAcquirer(t, gateway, &fakeGate{})
	if value, _, err := acquirer.Acquire(context.Background(), testRequest(catalog), catalog); !IsCategory(err, CategoryInsufficient) || len(value.Constraints) != 0 {
		t.Fatalf("truncated catalog reference published: %#v %v", value, err)
	}
	withoutSource := testPolicy(t)
	withoutSource.Limits.MaxBodyBytes = 4
	withoutSource, _ = SealPolicy(withoutSource)
	metadata.Body = "long description without configured source"
	gateway = &fakeGateway{metadata: []RemoteMetadata{metadata, metadata}, pages: map[int]FilePage{1: {}}}
	acquirer, _ = testAcquirer(t, gateway, &fakeGate{})
	value, _, err := acquirer.Acquire(context.Background(), testRequest(withoutSource), withoutSource)
	if err != nil || value.Description != "" || value.Completeness != CompletenessPartial || len(value.Constraints) != 0 {
		t.Fatalf("whole description omission: %#v %v", value, err)
	}
}

// TestUnifiedDiffRangesRequireCompleteDeclaredUnits closes REV-7 for forged, incomplete and overflowing hunks.
func TestUnifiedDiffRangesRequireCompleteDeclaredUnits(t *testing.T) {
	valid := []string{
		"@@ -1 +1 @@\n-old\n+new",
		"@@ -1,2 +1,2 @@ function\n same\n-old\n+new",
		"@@ -0,0 +1,2 @@\n+first\n+second",
		"@@ -1 +1 @@\n-old\n+new\n@@ -4,2 +4,2 @@\n context\n-before\n+after",
		"@@ -1,0 +1 @@\n+inserted\n@@ -2 +3 @@\n-old\n+new",
		"@@ -1 +1,0 @@\n-removed\n@@ -3 +2 @@\n-old\n+new",
	}
	for _, patch := range valid {
		if _, err := parsePatch(patch); err != nil {
			t.Fatalf("valid unified diff rejected: %q %v", patch, err)
		}
	}
	invalid := []string{
		"@@ forged header\n+line",
		"@@ -1,3 +1,3 @@\n-old\n+new",
		"@@ -1,2 +1,2 @@\n context",
		"@@ -0 +1 @@\n-old\n+new",
		"@@ -1 +0 @@\n-old\n+new",
		"@@ -01 +1 @@\n-old\n+new",
		"@@ -0,0 +0,0 @@",
		"@@ -999999999999999999999 +1 @@\n-old\n+new",
		"@@ -2147483647,2 +1 @@\n-old\n+new",
		"@@ -1 +1 @@\n-old\n+new\n@@ broken\n+later",
		"@@ -1 +1 @@\n-old\n+new\n\n",
		"@@ -1 +1 @@\n\\ No newline at end of file\n-old\n+new",
		"@@ -10 +10 @@\n-old\n+new\n@@ -1 +1 @@\n-old\n+new",
		"@@ -1 +1 @@\n-old\n+new\n@@ -1 +1 @@\n-old\n+new",
		"@@ -1,2 +1,2 @@\n same\n-old\n+new\n@@ -2 +3 @@\n-old\n+new",
		"@@ -1,0 +1 @@\n+one\n@@ -1,0 +3 @@\n+two",
		"@@ -1 +1,0 @@\n-one\n@@ -3 +1,0 @@\n-two",
	}
	for _, patch := range invalid {
		if _, err := parsePatch(patch); !IsCategory(err, CategoryIntegrity) {
			t.Fatalf("invalid unified diff accepted: %q %v", patch, err)
		}
	}
	value, _, _, _ := acquiredFixture(t)
	for _, mutate := range []func(*Hunk){
		func(hunk *Hunk) { hunk.Header = "@@ forged header" },
		func(hunk *Hunk) { hunk.Header = "@@ -1,3 +1,3 @@" },
	} {
		tampered := cloneContext(value)
		mutate(&tampered.Files[0].Hunks[0])
		tampered.Files[0].Digest = ""
		tampered.Files[0].Digest, _ = digestValue(tampered.Files[0])
		tampered = signTestContext(tampered)
		if VerifyContext(tampered) == nil {
			t.Fatal("self-signed malformed hunk accepted")
		}
	}
	ordered, err := parsePatch("@@ -1 +1 @@\n-old\n+new\n@@ -4 +4 @@\n-old\n+new")
	if err != nil {
		t.Fatal(err)
	}
	tampered := cloneContext(value)
	tampered.Files[0].Hunks = []Hunk{ordered[1], ordered[0]}
	tampered.Files[0].Digest = ""
	tampered.Files[0].Digest, _ = digestValue(tampered.Files[0])
	tampered.Counts.Hunks++
	tampered.Counts.Lines += 2
	tampered = signTestContext(tampered)
	if VerifyContext(tampered) == nil {
		t.Fatal("self-signed regressing hunk sequence accepted")
	}
}

// TestCanonicalRepositoryPathRejectsControlsDevicesAndEmptySegments closes REV-8 in the domain boundary.
func TestCanonicalRepositoryPathRejectsControlsDevicesAndEmptySegments(t *testing.T) {
	valid := []string{"src/main.go", "docs/área/read me.md", "a/.config", "com10/file.go"}
	for _, value := range valid {
		if canonical, ok := CanonicalRepositoryPath(value); !ok || canonical != value {
			t.Fatalf("safe POSIX path rejected: %q %q %t", value, canonical, ok)
		}
	}
	invalid := []string{"src/CON.go", "NUL.yaml", "src/aux", "src/Lpt9.txt", "COM1/file.go", "src//a.go", "src/a.go/", "src/./a.go", "src/../a.go", "src/\x00a.go", "src/\x1fa.go", "src/\x7fa.go", "src/\u0085a.go", "src/\xff.go", "src\\a.go", "C:/a.go"}
	for _, value := range invalid {
		if _, ok := CanonicalRepositoryPath(value); ok || validPath(value) {
			t.Fatalf("unsafe repository path accepted: %q", value)
		}
		if validRemoteFile(RemoteFile{Path: value, Status: StatusModified}) {
			t.Fatalf("unsafe provider file accepted: %q", value)
		}
	}
	policy := testPolicy(t)
	request := testRequest(policy)
	for _, mutate := range []func(*RemoteMetadata){
		func(metadata *RemoteMetadata) { metadata.Title = string([]byte{0xff}) },
		func(metadata *RemoteMetadata) { metadata.Body = string([]byte{0xff}) },
	} {
		metadata := testMetadata()
		mutate(&metadata)
		if err := validateMetadata(metadata, request); !IsCategory(err, CategoryStale) {
			t.Fatalf("invalid UTF-8 metadata accepted: %#v %v", metadata, err)
		}
	}
	if _, err := parsePatch(string([]byte{'@', '@', ' ', '-', '1', ' ', '+', '1', ' ', '@', '@', '\n', '+', 0xff})); !IsCategory(err, CategoryIntegrity) {
		t.Fatalf("invalid UTF-8 patch accepted: %v", err)
	}
}

// TestSelfSignedContextAndBindingRequireSemanticValidity closes REV-5 for coordinated local tampering.
func TestSelfSignedContextAndBindingRequireSemanticValidity(t *testing.T) {
	value, binding, _, _ := acquiredFixture(t)
	contexts := []Context{
		func() Context { v := cloneContext(value); v.Version = "evil"; return signTestContext(v) }(),
		func() Context { v := cloneContext(value); v.Completeness = "evil"; return signTestContext(v) }(),
		func() Context { v := cloneContext(value); v.Authority.RepositoryID = -1; return signTestContext(v) }(),
		func() Context {
			v := cloneContext(value)
			v.Files[0], v.Files[1] = v.Files[1], v.Files[0]
			return signTestContext(v)
		}(),
		func() Context { v := cloneContext(value); v.Counts.FilesProjected++; return signTestContext(v) }(),
		func() Context {
			v := cloneContext(value)
			v.Constraints = append(v.Constraints, v.Constraints[0])
			v.Counts.Constraints++
			return signTestContext(v)
		}(),
		func() Context {
			v := cloneContext(value)
			v.Contracts[0].HeadOID = testBaseOID
			return signTestContext(v)
		}(),
		func() Context { v := cloneContext(value); v.Files = nil; return signTestContext(v) }(),
	}
	for _, tampered := range contexts {
		if VerifyContext(tampered) == nil {
			t.Fatalf("self-signed context accepted: %#v", tampered)
		}
		raw, _ := jsonBytesWithoutVerification(tampered)
		if _, err := DecodeContext(raw); err == nil {
			t.Fatal("semantic decode accepted")
		}
	}
	raw, _ := jsonBytesWithoutVerification(value)
	duplicate := strings.Replace(string(raw), `"version":"PullRequestContext/v1"`, `"version":"PullRequestContext/v1","version":"PullRequestContext/v1"`, 1)
	if _, err := DecodeContext([]byte(duplicate)); err == nil {
		t.Fatal("duplicate JSON member accepted")
	}
	bindings := []Binding{
		func() Binding { v := binding; v.Version = "evil"; return signTestBinding(v) }(),
		func() Binding { v := binding; v.Authority.BaseOID = "bad"; return signTestBinding(v) }(),
		func() Binding { v := binding; v.ExtractorVersion = "free/v9"; return signTestBinding(v) }(),
		func() Binding { v := binding; v.ProducerID = "other"; return signTestBinding(v) }(),
	}
	for _, tampered := range bindings {
		if VerifyBinding(tampered) == nil {
			t.Fatalf("self-signed binding accepted: %#v", tampered)
		}
	}
	relation := signTestBinding(func() Binding { v := binding; v.RunID = "other-run"; return v }())
	store, err := OpenLocalStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err = store.Commit(context.Background(), "relation", strings.Repeat("f", 64), value, relation); !IsCategory(err, CategoryInvalid) {
		t.Fatalf("cross-object relation accepted: %v", err)
	}
}

// jsonBytesWithoutVerification serializes a deliberately self-signed adversarial object for DecodeContext probes.
func jsonBytesWithoutVerification(value Context) ([]byte, error) { return json.Marshal(value) }

// TestStoreCancellationWinsOverPersistenceWrapping closes REV-6 at load and commit boundaries.
func TestStoreCancellationWinsOverPersistenceWrapping(t *testing.T) {
	policy := testPolicy(t)
	metadata := testMetadata()
	for _, onCommit := range []bool{false, true} {
		ctx, cancel := context.WithCancelCause(context.Background())
		sentinel := errors.New("store stopped")
		store := cancellingStore{cancel: cancel, onCommit: onCommit, cause: sentinel}
		gateway := &fakeGateway{metadata: []RemoteMetadata{metadata, metadata}, pages: map[int]FilePage{1: {}}}
		acquirer, err := NewAcquirer(Dependencies{Gateway: gateway, Secrets: &fakeGate{}, Clock: &testClock{now: time.Unix(1, 0)}, Store: store, Observer: &recordObserver{}})
		if err != nil {
			t.Fatal(err)
		}
		_, _, err = acquirer.Acquire(ctx, testRequest(policy), policy)
		if !IsCategory(err, CategoryCancelled) || !errors.Is(err, sentinel) {
			t.Fatalf("onCommit=%t err=%v", onCommit, err)
		}
	}
	deadline, cancel := context.WithDeadlineCause(context.Background(), time.Unix(0, 0), context.DeadlineExceeded)
	defer cancel()
	acquirer, _ := NewAcquirer(Dependencies{Gateway: &fakeGateway{}, Secrets: &fakeGate{}, Clock: &testClock{now: time.Unix(1, 0)}, Store: NewMemoryStore(), Observer: &recordObserver{}})
	_, _, err := acquirer.Acquire(deadline, testRequest(policy), policy)
	if !IsCategory(err, CategoryTimeout) || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("deadline err=%v", err)
	}
}

// TestDurableBoundaryValidationMatrix exercises fail-closed observer/store inputs and exactly-once first-writer recovery.
func TestDurableBoundaryValidationMatrix(t *testing.T) {
	value, _, policy, request := acquiredFixture(t)
	acquisitionDigest, _ := digestValue(request)
	valid := Record{Version: RecordVersion, AcquisitionDigest: acquisitionDigest, RunID: request.RunID, RepositoryID: request.RepositoryID, PullRequestNumber: request.PullRequestNumber, HeadOID: request.ExpectedHeadOID, BaseOID: value.Authority.BaseOID, PolicyDigest: policy.Digest, ObservationDigest: value.ObservationDigest, ContextDigest: value.Digest, Outcome: "success", Completeness: value.Completeness, Counts: value.Counts, OmissionReasons: summarizeOmissions(value.Omissions), Attempts: 1}
	valid.ID = terminalRecordID(valid)
	valid.Digest = terminalRecordDigest(valid)
	if err := VerifyRecord(valid); err != nil {
		t.Fatal(err)
	}
	for _, mutate := range []func(*Record){
		func(record *Record) { record.Attempts += 99 },
		func(record *Record) { record.Duration += time.Hour },
	} {
		durable, openErr := OpenLocalRecordObserver(t.TempDir())
		if openErr != nil {
			t.Fatal(openErr)
		}
		if observeErr := durable.Observe(context.Background(), valid); observeErr != nil {
			t.Fatal(observeErr)
		}
		tampered := valid
		mutate(&tampered)
		raw, _ := json.Marshal(tampered)
		if writeErr := os.WriteFile(filepath.Join(durable.records, valid.ID+".json"), raw, 0600); writeErr != nil {
			t.Fatal(writeErr)
		}
		if _, readErr := durable.Read(context.Background(), valid.ID); !IsCategory(readErr, CategoryIntegrity) {
			t.Fatalf("unsigned record mutation accepted: %#v %v", tampered, readErr)
		}
	}
	failure := Record{Version: RecordVersion, AcquisitionDigest: digestBytes([]byte("failure")), Outcome: string(CategoryTransient), Category: CategoryTransient, OmissionReasons: []OmissionSummary{}}
	failure.ID = terminalRecordID(failure)
	failure.Digest = terminalRecordDigest(failure)
	if err := VerifyRecord(failure); err != nil {
		t.Fatal(err)
	}
	invalid := []Record{
		func() Record { v := valid; v.Version = "bad"; return v }(),
		func() Record { v := valid; v.RunID = "bad id"; return v }(),
		func() Record { v := valid; v.Counts.FilesObserved = -1; return v }(),
		func() Record { v := valid; v.Category = CategoryTransient; return v }(),
		func() Record { v := failure; v.Outcome = "unknown"; return v }(),
		func() Record {
			v := valid
			v.OmissionReasons = []OmissionSummary{{Reason: OmissionBody, Count: 1}, {Reason: OmissionBody, Count: 1}}
			return v
		}(),
	}
	for _, record := range invalid {
		if VerifyRecord(record) == nil {
			t.Fatalf("invalid record accepted: %#v", record)
		}
	}
	if _, err := OpenLocalRecordObserver(""); !IsCategory(err, CategoryInvalid) {
		t.Fatalf("empty observer root accepted: %v", err)
	}
	observer, err := OpenLocalRecordObserver(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err = (*LocalRecordObserver)(nil).Observe(context.Background(), valid); !IsCategory(err, CategoryInvalid) {
		t.Fatalf("nil observer accepted: %v", err)
	}
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	if err = observer.Observe(cancelled, valid); !IsCategory(err, CategoryCancelled) {
		t.Fatalf("observer cancellation ignored: %v", err)
	}
	if _, err = observer.Read(context.Background(), "bad"); !IsCategory(err, CategoryInvalid) {
		t.Fatalf("invalid observer id accepted: %v", err)
	}
	if _, err = observer.Read(cancelled, valid.ID); !IsCategory(err, CategoryCancelled) {
		t.Fatalf("observer read cancellation ignored: %v", err)
	}
	if err = observer.Observe(context.Background(), valid); err != nil {
		t.Fatal(err)
	}
	redelivery := valid
	redelivery.Duration++
	redelivery.Digest = terminalRecordDigest(redelivery)
	if err = observer.Observe(context.Background(), redelivery); err != nil {
		t.Fatalf("same terminal redelivery failed: %v", err)
	}
	conflicting := failure
	conflicting.ID = valid.ID
	if err = observer.Observe(context.Background(), conflicting); !IsCategory(err, CategoryInvalid) {
		t.Fatalf("forged terminal identity accepted: %v", err)
	}
	stored, err := observer.Read(context.Background(), valid.ID)
	if err != nil || stored.Outcome != "success" {
		t.Fatalf("first writer was replaced: %#v %v", stored, err)
	}
	if err = os.WriteFile(filepath.Join(observer.records, valid.ID+".json"), []byte(`{"version":"bad"}`), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err = observer.Read(context.Background(), valid.ID); !IsCategory(err, CategoryIntegrity) {
		t.Fatalf("corrupt observer record accepted: %v", err)
	}
	if _, err = OpenLocalStore(""); !IsCategory(err, CategoryInvalid) {
		t.Fatalf("empty store root accepted: %v", err)
	}
	store, err := OpenLocalStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if _, _, _, found, loadErr := store.Load(context.Background(), "missing"); loadErr != nil || found {
		t.Fatalf("missing receipt result: found=%t err=%v", found, loadErr)
	}
	if _, _, _, _, err = store.Load(context.Background(), "bad key"); !IsCategory(err, CategoryInvalid) {
		t.Fatalf("invalid store key accepted: %v", err)
	}
	if err = store.Commit(cancelled, "key", digestBytes([]byte("fingerprint")), value, Binding{}); !IsCategory(err, CategoryCancelled) {
		t.Fatalf("store commit cancellation ignored: %v", err)
	}
	if err = store.Commit(context.Background(), "bad key", digestBytes([]byte("fingerprint")), value, Binding{}); !IsCategory(err, CategoryInvalid) {
		t.Fatalf("invalid store commit accepted: %v", err)
	}
}

// TestDurableStorageCorruptionMatrix exercises immutable publication and strict decode recovery branches.
func TestDurableStorageCorruptionMatrix(t *testing.T) {
	directory := t.TempDir()
	if err := publishImmutable(context.Background(), directory, "object", []byte("one")); err != nil {
		t.Fatal(err)
	}
	if err := publishImmutable(context.Background(), directory, "object", []byte("one")); err != nil {
		t.Fatalf("identical immutable publish failed: %v", err)
	}
	if err := publishImmutable(context.Background(), directory, "object", []byte("two")); !IsCategory(err, CategoryConflict) {
		t.Fatalf("conflicting immutable publish accepted: %v", err)
	}
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	if err := publishImmutable(cancelled, directory, "cancelled", []byte("value")); !IsCategory(err, CategoryCancelled) {
		t.Fatalf("cancelled immutable publish accepted: %v", err)
	}
	oversized := filepath.Join(directory, "oversized")
	if err := os.WriteFile(oversized, []byte("ab"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := readBounded(oversized, 1); !IsCategory(err, CategoryIntegrity) {
		t.Fatalf("oversized durable object accepted: %v", err)
	}
	var decoded receipt
	for _, raw := range [][]byte{[]byte(`{"version":"v","unknown":true}`), []byte(`{"version":"v"}{}`), []byte(`{"version":"v","version":"v"}`)} {
		if decodeStrict(raw, &decoded) == nil {
			t.Fatalf("non-strict JSON accepted: %s", raw)
		}
	}
	value, binding, fingerprint := storedFixture(t)
	for _, corruptContext := range []bool{true, false} {
		root := t.TempDir()
		store, err := OpenLocalStore(root)
		if err != nil {
			t.Fatal(err)
		}
		if err = store.Commit(context.Background(), "key", fingerprint, value, binding); err != nil {
			t.Fatal(err)
		}
		path := filepath.Join(root, "bindings", binding.Digest+".json")
		if corruptContext {
			path = filepath.Join(root, "contexts", value.Digest+".json")
		}
		if err = os.WriteFile(path, []byte(`{"version":"bad"}`), 0600); err != nil {
			t.Fatal(err)
		}
		if _, _, _, _, err = store.Load(context.Background(), "key"); !IsCategory(err, CategoryIntegrity) {
			t.Fatalf("corrupt immutable object accepted: context=%t err=%v", corruptContext, err)
		}
	}
	memory := NewMemoryStore()
	memoryCancelled, memoryCancel := context.WithCancel(context.Background())
	memoryCancel()
	if _, _, _, _, err := memory.Load(memoryCancelled, "key"); !errors.Is(err, context.Canceled) {
		t.Fatalf("memory load cancellation ignored: %v", err)
	}
	if err := memory.Commit(memoryCancelled, "key", fingerprint, value, binding); !errors.Is(err, context.Canceled) {
		t.Fatalf("memory commit cancellation ignored: %v", err)
	}
	if err := memory.Commit(context.Background(), "key", fingerprint, value, binding); err != nil {
		t.Fatal(err)
	}
	if err := memory.Commit(context.Background(), "key", digestBytes([]byte("other")), value, binding); !IsCategory(err, CategoryConflict) {
		t.Fatalf("memory conflict accepted: %v", err)
	}
	if err := (WallClock{}).Wait(context.Background(), 0); err != nil {
		t.Fatal(err)
	}
	if maxInt(2, 1) != 2 {
		t.Fatal("maximum selector lost the larger bound")
	}
	tampered := value
	tampered.Version = "bad"
	if _, err := JSONBytes(tampered); err == nil {
		t.Fatal("invalid context serialized")
	}
}
