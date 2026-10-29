package controlplane

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestSanitizeFunctionalImportAuthenticatesAndDropsPrivateAuthority proves the
// offline DTO contains durable causal facts but never the source MAC or claims.
func TestSanitizeFunctionalImportAuthenticatesAndDropsPrivateAuthority(t *testing.T) {
	key := bytes.Repeat([]byte{7}, 32)
	source := functionalImportSource{Version: 2, MAC: "", Claims: map[string]json.RawMessage{"step": json.RawMessage(`{"Owner":"worker","Token":"secret","ExpiresAt":"2030-01-01T00:00:00Z"}`)}, Intents: map[string]json.RawMessage{"step": json.RawMessage(`{"run_id":"run","revision":1,"step_id":"step","binding_digest":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","key_digest":"bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"}`)}, Terminals: map[string]json.RawMessage{}, Events: []json.RawMessage{}}
	unsigned, err := json.Marshal(source)
	if err != nil {
		t.Fatal(err)
	}
	authenticator := hmac.New(sha256.New, key)
	_, _ = authenticator.Write(unsigned)
	source.MAC = hex.EncodeToString(authenticator.Sum(nil))
	raw, _ := json.Marshal(source)
	document, err := sanitizeFunctionalImport(raw, key)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(document, []byte("secret")) || bytes.Contains(document, []byte("MAC")) || bytes.Contains(document, []byte("Claims")) {
		t.Fatalf("private source authority crossed import boundary: %s", document)
	}
	wrongKey := bytes.Repeat([]byte{8}, 32)
	if _, err = sanitizeFunctionalImport(raw, wrongKey); !errors.Is(err, ErrIntegrity) {
		t.Fatal("tampered functional state accepted", err)
	}
}

// TestLocalStoreStreamPrioritizesProductRoots proves nested workflow consumers
// cannot be collapsed into the shared workflow stream during freeze.
func TestLocalStoreStreamPrioritizesProductRoots(t *testing.T) {
	cases := map[string]string{"workflows/run.state.json": "workflow", "workflows/plans/run.plan.json": "artifacts", "workflows/private/run.private.json": "executables", "workflows/approval/run.jsonl": "approval", "workflows/functional-flow/run.flow.json": "functional", "environment/records/run/1.json": "environment", "environment/events/run/1.json": "environment"}
	for path, expected := range cases {
		actual, ok := localStoreStream(path)
		if !ok || actual != expected {
			t.Fatalf("path=%q stream=%q ok=%t", path, actual, ok)
		}
	}
	if actual, ok := localStoreStream("secrets/token.json"); ok || actual != "" {
		t.Fatalf("unknown root classified as %q", actual)
	}
}

// freezing or reading source state.
type importAuthorizerProbe struct {
	called bool
	err    error
}

// Authorize records the closed operation/project tuple without granting any
// additional stream or backend capability.
func (probe *importAuthorizerProbe) Authorize(_ context.Context, operation, projectID string) error {
	probe.called = operation == "offline_import" && projectID == "controller-project"
	return probe.err
}

// importTargetProbe models resumable candidate persistence and one lost
// cutover response for the controller contract.
type importTargetProbe struct {
	manifest ImportManifest
	docs     []ImportDocument
	receipt  ImportCutoverReceipt
	lost     bool
}

// importSourceFailureProbe injects one source-side failure at a time while
// retaining a fully sealed manifest and page for the controller error matrix.
type importSourceFailureProbe struct {
	manifest   ImportManifest
	page       []ImportDocument
	freezeErr  error
	pageErr    error
	archiveErr error
}

// FreezeLocalStores returns the configured frozen source result.
func (probe *importSourceFailureProbe) FreezeLocalStores(context.Context, string, string, string, string, uint64, time.Time) (ImportManifest, error) {
	return probe.manifest, probe.freezeErr
}

// ReadImportPage returns the configured page or read failure.
func (probe *importSourceFailureProbe) ReadImportPage(context.Context, string, string, uint64, int) ([]ImportDocument, error) {
	return probe.page, probe.pageErr
}

// Archive exposes a post-cutover source archival failure.
func (probe *importSourceFailureProbe) Archive(context.Context, ImportCutoverReceipt) error {
	return probe.archiveErr
}

// importTargetFailureProbe injects failures at each target protocol boundary.
type importTargetFailureProbe struct {
	manifest     ImportManifest
	receipt      ImportCutoverReceipt
	saveErr      error
	pageErr      error
	verifyErr    error
	cutoverErr   error
	reconcileErr error
}

// importRetentionProbe is a valid store used to isolate policy validation.
type importRetentionProbe struct{}

// DeleteTerminalBefore is unreachable when the policy is invalid.
func (importRetentionProbe) DeleteTerminalBefore(context.Context, int64, int) (int, error) {
	return 0, nil
}

// SaveImport returns the configured candidate persistence result.
func (probe *importTargetFailureProbe) SaveImport(context.Context, ImportManifest, string) (ImportManifest, error) {
	return probe.manifest, probe.saveErr
}

// SaveImportPage returns the configured page persistence result.
func (probe *importTargetFailureProbe) SaveImportPage(context.Context, string, []ImportDocument) error {
	return probe.pageErr
}

// VerifyImport returns the configured verification result.
func (probe *importTargetFailureProbe) VerifyImport(context.Context, string) (ImportManifest, error) {
	return probe.manifest, probe.verifyErr
}

// CutoverImport returns the configured commit result or unknown outcome.
func (probe *importTargetFailureProbe) CutoverImport(context.Context, string, time.Time) (ImportCutoverReceipt, error) {
	return probe.receipt, probe.cutoverErr
}

// LoadImportCutoverReceipt returns the configured reconciliation result.
func (probe *importTargetFailureProbe) LoadImportCutoverReceipt(context.Context, string) (ImportCutoverReceipt, error) {
	return probe.receipt, probe.reconcileErr
}

// SaveImport records the frozen candidate identity before pages are accepted.
func (probe *importTargetProbe) SaveImport(_ context.Context, value ImportManifest, _ string) (ImportManifest, error) {
	probe.manifest = value
	return value, nil
}

// SaveImportPage records detached typed pages in controller order.
func (probe *importTargetProbe) SaveImportPage(_ context.Context, _ string, page []ImportDocument) error {
	probe.docs = append(probe.docs, page...)
	return nil
}

// VerifyImport advances only the controller's already-copied manifest.
func (probe *importTargetProbe) VerifyImport(_ context.Context, _ string) (ImportManifest, error) {
	return AdvanceImport(mustAdvanceImport(probe.manifest, ImportCandidate), ImportVerified)
}

// CutoverImport records a stable receipt and simulates response loss once.
func (probe *importTargetProbe) CutoverImport(_ context.Context, _ string, now time.Time) (ImportCutoverReceipt, error) {
	probe.receipt, _ = SealImportCutoverReceipt(ImportCutoverReceipt{ImportID: probe.manifest.ImportID, RunID: probe.manifest.RunID, SourceHead: probe.manifest.ExpectedHeadDigest, TargetHead: fixedDigest("controller-target"), ArchivedEpoch: probe.manifest.ExpectedEpoch, ActiveEpoch: probe.manifest.ExpectedEpoch + 1, CommittedAt: now})
	if !probe.lost {
		probe.lost = true
		return ImportCutoverReceipt{}, ErrUnavailable
	}
	return probe.receipt, nil
}

// LoadImportCutoverReceipt resolves the target's committed response loss.
func (probe *importTargetProbe) LoadImportCutoverReceipt(context.Context, string) (ImportCutoverReceipt, error) {
	return probe.receipt, nil
}

// mustAdvanceImport creates a test-only intermediate state whose failure is
// surfaced later by the controller assertion rather than hidden by a fallback.
func mustAdvanceImport(value ImportManifest, state ImportState) ImportManifest {
	next, _ := AdvanceImport(value, state)
	return next
}

// TestLocalImportFreezeResumeArchive proves durable read-only source behavior across reopen and cutover recovery.
func TestLocalImportFreezeResumeArchive(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	store, err := OpenLocalImportStore(root, "local-source")
	if err != nil {
		t.Fatal(err)
	}
	document, err := SealImportDocument(ImportDocument{Stream: "runs", RelationKey: "run-1", Document: []byte(`{"run":"one"}`)})
	if err != nil {
		t.Fatal(err)
	}
	if err = store.Put(ctx, document); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	manifest, err := store.Freeze(ctx, "import-1", "run-1", "project-1", fixedDigest("head"), 1, now)
	if err != nil || manifest.Counts["runs"] != 1 || manifest.RelationDigests["runs"] == "" {
		t.Fatal("freeze", err)
	}
	if err = store.Put(ctx, ImportDocument{Stream: "runs", Ordinal: 1, RelationKey: "run-2", Document: []byte(`{}`)}); !errors.Is(err, ErrConflict) {
		t.Fatal("frozen source accepted mutation")
	}
	reopened, err := OpenLocalImportStore(root, "local-source")
	if err != nil {
		t.Fatal(err)
	}
	page, err := reopened.ReadImportPage(ctx, manifest.ImportID, "runs", 0, 10)
	if err != nil || len(page) != 1 || string(page[0].Document) != string(document.Document) {
		t.Fatal("resume", err)
	}
	receipt, _ := SealImportCutoverReceipt(ImportCutoverReceipt{ImportID: manifest.ImportID, RunID: manifest.RunID, SourceHead: manifest.ExpectedHeadDigest, TargetHead: fixedDigest("target"), ArchivedEpoch: 1, ActiveEpoch: 2, CommittedAt: now})
	if err = reopened.Archive(ctx, receipt); err != nil {
		t.Fatal(err)
	}
	if err = reopened.Archive(ctx, receipt); err != nil {
		t.Fatal("archive redelivery", err)
	}
	if err = reopened.Unfreeze(ctx, manifest.ImportID); !errors.Is(err, ErrConflict) {
		t.Fatal("post-cutover unfreeze")
	}
}

// TestLocalImportSnapshotsRealStoreFiles proves offline import inventories the
// actual shared local root instead of requiring a synthetic caller document.
func TestLocalImportSnapshotsRealStoreFiles(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	workflowRoot := filepath.Join(root, "workflow")
	cleanupRoot := filepath.Join(root, "cleanup")
	if err := os.MkdirAll(workflowRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(cleanupRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	workflowBytes := []byte(`{"workflow":"one"}`)
	cleanupBytes := []byte(`{"cleanup":"one"}`)
	if err := os.WriteFile(filepath.Join(workflowRoot, "runs.json"), workflowBytes, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cleanupRoot, "items.json"), cleanupBytes, 0o600); err != nil {
		t.Fatal(err)
	}
	store, err := OpenLocalImportStore(root, "real-local-source")
	if err != nil {
		t.Fatal(err)
	}
	if err = store.SnapshotLocalStores(ctx); err != nil {
		t.Fatal(err)
	}
	manifest, err := store.Freeze(ctx, "real-import", "real-run", "real-project", fixedDigest("head"), 1, time.Now().UTC())
	if err != nil || manifest.Counts["workflow"] != 1 || manifest.Counts["cleanup"] != 1 {
		t.Fatal("real store inventory", err)
	}
	workflowPage, err := store.ReadImportPage(ctx, manifest.ImportID, "workflow", 0, 10)
	if err != nil || len(workflowPage) != 1 || !bytes.Equal(workflowPage[0].Document, workflowBytes) {
		t.Fatal("workflow bytes", err)
	}
	if err = EnsureLocalImportWritable(cleanupRoot); !errors.Is(err, ErrConflict) {
		t.Fatal("nested cleanup store remained writable")
	}
}

// TestFreezeLocalStoresPublishesTypedSnapshotUnderOneExclusion proves the
// product import boundary rejects residual files and freezes the exact allowed
// inventory without an intervening writable state.
func TestFreezeLocalStoresPublishesTypedSnapshotUnderOneExclusion(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	workflowRoot := filepath.Join(root, "workflow")
	if err := os.MkdirAll(workflowRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workflowRoot, "runs.json"), []byte(`{"run":"one"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	store, err := OpenLocalImportStore(root, "typed-source")
	if err != nil {
		t.Fatal(err)
	}
	manifest, err := store.FreezeLocalStores(ctx, "typed-import", "typed-run", "typed-project", fixedDigest("typed-head"), 1, time.Now().UTC())
	if err != nil || manifest.Counts["workflow"] != 1 {
		t.Fatal("atomic freeze", err)
	}
	if err = EnsureLocalImportWritable(workflowRoot); !errors.Is(err, ErrConflict) {
		t.Fatal("writer escaped atomic freeze", err)
	}
	unknownRoot := t.TempDir()
	if err = os.WriteFile(filepath.Join(unknownRoot, "oauth-profile.json"), []byte(`{"secret":"forbidden"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	unknown, err := OpenLocalImportStore(unknownRoot, "unknown-source")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = unknown.FreezeLocalStores(ctx, "unknown-import", "unknown-run", "unknown-project", fixedDigest("unknown-head"), 1, time.Now().UTC()); !errors.Is(err, ErrIntegrity) {
		t.Fatal("unknown file entered candidate", err)
	}
}

// TestFreezeAggregatesConsumerExportersWithoutPrivateFiles proves typed DTOs
// replace claimed journals instead of duplicating their implementation bytes.
func TestFreezeAggregatesConsumerExportersWithoutPrivateFiles(t *testing.T) {
	root := t.TempDir()
	privateRoot := filepath.Join(root, "private-consumer")
	if err := os.MkdirAll(privateRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(privateRoot, "secret.anchor"), []byte("must-not-cross"), 0o600); err != nil {
		t.Fatal(err)
	}
	store, err := OpenLocalImportStore(root, "aggregate-source")
	if err != nil {
		t.Fatal(err)
	}
	if err = store.RegisterSnapshotExporter(func(context.Context, string) ([]LocalSnapshotExport, error) {
		return []LocalSnapshotExport{{Stream: "correction", RelationKey: "typed-correction", Document: []byte(`{"chains":{}}`), ClaimedPaths: []string{privateRoot}}}, nil
	}); err != nil {
		t.Fatal(err)
	}
	manifest, err := store.FreezeLocalStores(context.Background(), "aggregate-import", "aggregate-run", "aggregate-project", fixedDigest("aggregate-head"), 1, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	if manifest.Counts["correction"] != 1 {
		t.Fatalf("unexpected typed count: %#v", manifest.Counts)
	}
	page, err := store.ReadImportPage(context.Background(), manifest.ImportID, "correction", 0, 10)
	if err != nil || len(page) != 1 || bytes.Contains(page[0].Document, []byte("must-not-cross")) {
		t.Fatalf("private representation crossed freeze: %#v %v", page, err)
	}
}

// TestLocalWriterHoldsFreezeExclusionThroughPublication proves freeze cannot
// snapshot between a writer's final state check and durable file publication.
func TestLocalWriterHoldsFreezeExclusionThroughPublication(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	workflowRoot := filepath.Join(root, "workflow")
	if err := os.MkdirAll(workflowRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	store, err := OpenLocalImportStore(root, "exclusive-source")
	if err != nil {
		t.Fatal(err)
	}
	release, err := AcquireLocalImportWrite(ctx, workflowRoot)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(workflowRoot, "run.json"), []byte(`{"run":"published"}`), 0o600); err != nil {
		release()
		t.Fatal(err)
	}
	if _, err = store.FreezeLocalStores(ctx, "exclusive-import", "exclusive-run", "exclusive-project", fixedDigest("exclusive-head"), 1, time.Now().UTC()); !errors.Is(err, ErrConflict) {
		release()
		t.Fatal("freeze crossed active writer", err)
	}
	release()
	manifest, err := store.FreezeLocalStores(ctx, "exclusive-import", "exclusive-run", "exclusive-project", fixedDigest("exclusive-head"), 1, time.Now().UTC())
	if err != nil || manifest.Counts["workflow"] != 1 {
		t.Fatal("published file absent after exclusive freeze", err)
	}
}

// TestOfflineImportControllerAuthenticatesCopiesAndReconcilesCutover proves the
// productive protocol orders auth→freeze→copy→verify→cutover→archive and does
// not repeat a commit whose response was lost.
func TestOfflineImportControllerAuthenticatesCopiesAndReconcilesCutover(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "workflow"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "workflow", "runs.json"), []byte(`{"run":"controller"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	source, err := OpenLocalImportStore(root, "controller-source")
	if err != nil {
		t.Fatal(err)
	}
	authorizer := &importAuthorizerProbe{}
	target := &importTargetProbe{}
	now := time.Now().UTC()
	receipt, err := (OfflineImportController{Source: source, Target: target, Authorizer: authorizer}).Run(ctx, OfflineImportRequest{ImportID: "controller-import", RunID: "controller-run", ProjectID: "controller-project", ExpectedHead: fixedDigest("controller-head"), ExpectedEpoch: 1, PageSize: 1, Now: now, ReconcileTimeout: time.Second})
	if err != nil || !authorizer.called || len(target.docs) != 1 || receipt.Digest == "" {
		t.Fatal("offline controller", err)
	}
	if err = EnsureLocalImportWritable(root); !errors.Is(err, ErrConflict) {
		t.Fatal("cutover source remained writable", err)
	}
}

// TestOfflineImportControllerFailsClosedAtEveryBoundary proves no failed
// freeze, page, verification, cutover reconciliation or archive is reported as
// a successful migration.
func TestOfflineImportControllerFailsClosedAtEveryBoundary(t *testing.T) {
	now := time.Now().UTC()
	document, err := SealImportDocument(ImportDocument{Stream: "events", RelationKey: "event-1", Document: []byte(`{"event":1}`)})
	if err != nil {
		t.Fatal(err)
	}
	manifest, err := SealImportManifest(ImportManifest{ImportID: "failure-import", RunID: "failure-run", ProjectID: "failure-project", SourceIdentity: "failure-source", ExpectedHeadDigest: fixedDigest("failure-head"), ExpectedEpoch: 1, Counts: map[string]uint64{"events": 1}, StreamDigests: map[string]string{"events": importDocumentsDigest([]ImportDocument{document}, false)}, RelationDigests: map[string]string{"events": importDocumentsDigest([]ImportDocument{document}, true)}, EventDigests: map[string]string{"events": importDocumentsDigest([]ImportDocument{document}, false)}, State: ImportFrozen, CreatedAt: now})
	if err != nil {
		t.Fatal(err)
	}
	receipt, err := SealImportCutoverReceipt(ImportCutoverReceipt{ImportID: manifest.ImportID, RunID: manifest.RunID, SourceHead: manifest.ExpectedHeadDigest, TargetHead: fixedDigest("failure-target"), ArchivedEpoch: 1, ActiveEpoch: 2, CommittedAt: now})
	if err != nil {
		t.Fatal(err)
	}
	request := OfflineImportRequest{ImportID: manifest.ImportID, RunID: manifest.RunID, ProjectID: manifest.ProjectID, ExpectedHead: manifest.ExpectedHeadDigest, ExpectedEpoch: 1, PageSize: 1, Now: now, ReconcileTimeout: time.Second}
	boundaryErr := errors.New("boundary failure")
	tests := []struct {
		name       string
		source     *importSourceFailureProbe
		target     *importTargetFailureProbe
		authorizer *importAuthorizerProbe
	}{
		{name: "unauthorized", source: &importSourceFailureProbe{manifest: manifest, page: []ImportDocument{document}}, target: &importTargetFailureProbe{manifest: manifest, receipt: receipt}, authorizer: &importAuthorizerProbe{err: boundaryErr}},
		{name: "freeze", source: &importSourceFailureProbe{freezeErr: boundaryErr}, target: &importTargetFailureProbe{}, authorizer: &importAuthorizerProbe{}},
		{name: "save manifest", source: &importSourceFailureProbe{manifest: manifest}, target: &importTargetFailureProbe{saveErr: boundaryErr}, authorizer: &importAuthorizerProbe{}},
		{name: "read page", source: &importSourceFailureProbe{manifest: manifest, pageErr: boundaryErr}, target: &importTargetFailureProbe{manifest: manifest}, authorizer: &importAuthorizerProbe{}},
		{name: "empty page", source: &importSourceFailureProbe{manifest: manifest}, target: &importTargetFailureProbe{manifest: manifest}, authorizer: &importAuthorizerProbe{}},
		{name: "save page", source: &importSourceFailureProbe{manifest: manifest, page: []ImportDocument{document}}, target: &importTargetFailureProbe{manifest: manifest, pageErr: boundaryErr}, authorizer: &importAuthorizerProbe{}},
		{name: "verify", source: &importSourceFailureProbe{manifest: manifest, page: []ImportDocument{document}}, target: &importTargetFailureProbe{manifest: manifest, verifyErr: boundaryErr}, authorizer: &importAuthorizerProbe{}},
		{name: "reconcile", source: &importSourceFailureProbe{manifest: manifest, page: []ImportDocument{document}}, target: &importTargetFailureProbe{manifest: manifest, cutoverErr: boundaryErr, reconcileErr: boundaryErr}, authorizer: &importAuthorizerProbe{}},
		{name: "archive", source: &importSourceFailureProbe{manifest: manifest, page: []ImportDocument{document}, archiveErr: boundaryErr}, target: &importTargetFailureProbe{manifest: manifest, receipt: receipt}, authorizer: &importAuthorizerProbe{}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, runErr := (OfflineImportController{Source: test.source, Target: test.target, Authorizer: test.authorizer}).Run(context.Background(), request); runErr == nil {
				t.Fatal("failed boundary reported success")
			}
		})
	}
	if _, err = (OfflineImportController{}).Run(context.Background(), request); !errors.Is(err, ErrInvalid) {
		t.Fatal("invalid controller accepted", err)
	}
}

// TestLocalImportExporterAndLifecycleFailures exercises composition-time and
// frozen-state guards without weakening source-wide writer exclusion.
func TestLocalImportExporterAndLifecycleFailures(t *testing.T) {
	ctx := context.Background()
	if err := (*LocalImportStore)(nil).RegisterSnapshotExporter(func(context.Context, string) ([]LocalSnapshotExport, error) { return nil, nil }); !errors.Is(err, ErrInvalid) {
		t.Fatal("nil exporter owner accepted", err)
	}
	store, err := OpenLocalImportStore(t.TempDir(), "failure-source")
	if err != nil {
		t.Fatal(err)
	}
	if err = store.RegisterSnapshotExporter(nil); !errors.Is(err, ErrInvalid) {
		t.Fatal("nil exporter accepted", err)
	}
	corrupt, err := OpenLocalImportStore(t.TempDir(), "corrupt-export-source")
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(corrupt.statePath(), []byte(`{"unknown":true}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err = corrupt.RegisterSnapshotExporter(func(context.Context, string) ([]LocalSnapshotExport, error) { return nil, nil }); !errors.Is(err, ErrIntegrity) {
		t.Fatal("exporter registered over corrupt state", err)
	}
	cancelRoot := t.TempDir()
	cancelStore, err := OpenLocalImportStore(cancelRoot, "cancel-write-source")
	if err != nil || cancelStore == nil {
		t.Fatal(err)
	}
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err = AcquireLocalImportWrite(cancelled, cancelRoot); !errors.Is(err, context.Canceled) {
		t.Fatal("cancelled writer admitted", err)
	}
	if err = os.WriteFile(filepath.Join(cancelRoot, ".featureproof-import.lock"), []byte("owned"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err = AcquireLocalImportWrite(ctx, cancelRoot); !errors.Is(err, ErrConflict) {
		t.Fatal("concurrent writer admitted", err)
	}
	if _, err = (*LocalImportStore)(nil).FreezeLocalStores(ctx, "import", "run", "project", fixedDigest("head"), 1, time.Now().UTC()); !errors.Is(err, ErrInvalid) {
		t.Fatal("nil freeze owner accepted", err)
	}
	if err = store.writeState(localImportState{Archived: true, Streams: map[string][]ImportDocument{}}); err != nil {
		t.Fatal(err)
	}
	if _, err = store.FreezeLocalStores(ctx, "import", "run", "project", fixedDigest("head"), 1, time.Now().UTC()); !errors.Is(err, ErrConflict) {
		t.Fatal("archived source frozen", err)
	}
	if err = store.writeState(localImportState{Frozen: true, ImportID: "other", Streams: map[string][]ImportDocument{}}); err != nil {
		t.Fatal(err)
	}
	if _, err = store.FreezeLocalStores(ctx, "import", "run", "project", fixedDigest("head"), 1, time.Now().UTC()); !errors.Is(err, ErrConflict) {
		t.Fatal("foreign freeze resumed", err)
	}
	sealed, err := SealImportDocument(ImportDocument{Stream: "events", RelationKey: "event", Document: []byte(`{}`)})
	if err != nil {
		t.Fatal(err)
	}
	if err = store.writeState(localImportState{Streams: map[string][]ImportDocument{"events": {sealed}}}); err != nil {
		t.Fatal(err)
	}
	if err = store.RegisterSnapshotExporter(func(context.Context, string) ([]LocalSnapshotExport, error) { return nil, nil }); !errors.Is(err, ErrConflict) {
		t.Fatal("exporter registered after capture", err)
	}
	if _, err = store.FreezeLocalStores(ctx, "import", "run", "project", fixedDigest("head"), 1, time.Now().UTC()); !errors.Is(err, ErrConflict) {
		t.Fatal("captured stream frozen twice", err)
	}

	newStore := func(t *testing.T) *LocalImportStore {
		t.Helper()
		value, openErr := OpenLocalImportStore(t.TempDir(), "export-source")
		if openErr != nil {
			t.Fatal(openErr)
		}
		return value
	}
	t.Run("exporter error", func(t *testing.T) {
		value := newStore(t)
		_ = value.RegisterSnapshotExporter(func(context.Context, string) ([]LocalSnapshotExport, error) { return nil, ErrUnavailable })
		if _, freezeErr := value.FreezeLocalStores(ctx, "import", "run", "project", fixedDigest("head"), 1, time.Now().UTC()); !errors.Is(freezeErr, ErrUnavailable) {
			t.Fatal(freezeErr)
		}
	})
	t.Run("invalid export", func(t *testing.T) {
		value := newStore(t)
		_ = value.RegisterSnapshotExporter(func(context.Context, string) ([]LocalSnapshotExport, error) {
			return []LocalSnapshotExport{{Stream: "unknown", RelationKey: "item", Document: []byte(`{}`)}}, nil
		})
		if _, freezeErr := value.FreezeLocalStores(ctx, "import", "run", "project", fixedDigest("head"), 1, time.Now().UTC()); !errors.Is(freezeErr, ErrIntegrity) {
			t.Fatal(freezeErr)
		}
	})
	t.Run("outside claim", func(t *testing.T) {
		value := newStore(t)
		outside := filepath.Join(t.TempDir(), "outside")
		_ = value.RegisterSnapshotExporter(func(context.Context, string) ([]LocalSnapshotExport, error) {
			return []LocalSnapshotExport{{Stream: "events", RelationKey: "item", Document: []byte(`{}`), ClaimedPaths: []string{outside}}}, nil
		})
		if _, freezeErr := value.FreezeLocalStores(ctx, "import", "run", "project", fixedDigest("head"), 1, time.Now().UTC()); !errors.Is(freezeErr, ErrIntegrity) {
			t.Fatal(freezeErr)
		}
	})
	t.Run("stable same-stream ordering", func(t *testing.T) {
		value := newStore(t)
		_ = value.RegisterSnapshotExporter(func(context.Context, string) ([]LocalSnapshotExport, error) {
			return []LocalSnapshotExport{{Stream: "events", RelationKey: "second", Document: []byte(`{"n":2}`)}, {Stream: "events", RelationKey: "first", Document: []byte(`{"n":1}`)}}, nil
		})
		manifest, freezeErr := value.FreezeLocalStores(ctx, "ordered-import", "ordered-run", "ordered-project", fixedDigest("ordered-head"), 1, time.Now().UTC())
		if freezeErr != nil || manifest.Counts["events"] != 2 {
			t.Fatal("same-stream export ordering", freezeErr)
		}
	})
	t.Run("invalid manifest", func(t *testing.T) {
		value := newStore(t)
		if _, freezeErr := value.FreezeLocalStores(ctx, "", "run", "project", fixedDigest("head"), 1, time.Now().UTC()); !errors.Is(freezeErr, ErrInvalid) {
			t.Fatal(freezeErr)
		}
	})
	t.Run("frozen replay", func(t *testing.T) {
		value := newStore(t)
		now := time.Now().UTC()
		manifest, freezeErr := value.FreezeLocalStores(ctx, "replay-import", "replay-run", "replay-project", fixedDigest("replay-head"), 1, now)
		if freezeErr != nil {
			t.Fatal(freezeErr)
		}
		replayed, freezeErr := value.FreezeLocalStores(ctx, manifest.ImportID, manifest.RunID, manifest.ProjectID, manifest.ExpectedHeadDigest, manifest.ExpectedEpoch, now)
		if freezeErr != nil || replayed.Digest != manifest.Digest {
			t.Fatal("frozen replay changed", freezeErr)
		}
	})
	t.Run("legacy freeze and archive guards", func(t *testing.T) {
		value := newStore(t)
		if _, freezeErr := value.Freeze(ctx, "", "run", "project", fixedDigest("head"), 1, time.Now().UTC()); !errors.Is(freezeErr, ErrInvalid) {
			t.Fatal("invalid legacy freeze accepted", freezeErr)
		}
		now := time.Now().UTC()
		receipt, sealErr := SealImportCutoverReceipt(ImportCutoverReceipt{ImportID: "other-import", RunID: "other-run", SourceHead: fixedDigest("source"), TargetHead: fixedDigest("target"), ArchivedEpoch: 1, ActiveEpoch: 2, CommittedAt: now})
		if sealErr != nil {
			t.Fatal(sealErr)
		}
		if archiveErr := value.Archive(ctx, receipt); !errors.Is(archiveErr, ErrConflict) {
			t.Fatal("unfrozen source archived", archiveErr)
		}
	})
}

// TestLocalImportSnapshotRejectsUnownedAndMalformedFiles proves the closed
// filesystem vocabulary rejects unknown, unauthenticated and ambiguous data.
func TestLocalImportSnapshotRejectsUnownedAndMalformedFiles(t *testing.T) {
	tests := []struct {
		name  string
		setup func(string) error
	}{
		{name: "unknown", setup: func(root string) error {
			return os.WriteFile(filepath.Join(root, "secret.txt"), []byte("secret"), 0o600)
		}},
		{name: "functional missing key", setup: func(root string) error {
			directory := filepath.Join(root, "functional-flow")
			if err := os.MkdirAll(directory, 0o700); err != nil {
				return err
			}
			return os.WriteFile(filepath.Join(directory, "run.flow.json"), []byte(`{}`), 0o600)
		}},
		{name: "functional malformed", setup: func(root string) error {
			directory := filepath.Join(root, "functional-flow")
			if err := os.MkdirAll(directory, 0o700); err != nil {
				return err
			}
			if err := os.WriteFile(filepath.Join(directory, ".flow-state.key"), bytes.Repeat([]byte{1}, 32), 0o600); err != nil {
				return err
			}
			return os.WriteFile(filepath.Join(directory, "run.flow.json"), []byte(`{}`), 0o600)
		}},
		{name: "long relation", setup: func(root string) error {
			directory := filepath.Join(root, "workflow")
			if err := os.MkdirAll(directory, 0o700); err != nil {
				return err
			}
			return os.WriteFile(filepath.Join(directory, "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa.json"), []byte(`{}`), 0o600)
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			if err := test.setup(root); err != nil {
				t.Fatal(err)
			}
			store, err := OpenLocalImportStore(root, "closed-source")
			if err != nil {
				t.Fatal(err)
			}
			if _, err = store.FreezeLocalStores(context.Background(), "import", "run", "project", fixedDigest("head"), 1, time.Now().UTC()); !errors.Is(err, ErrIntegrity) {
				t.Fatal("unsafe source accepted", err)
			}
		})
	}
}

// TestImportHelpersRejectMalformedMetadata covers detached manifest maps,
// deterministic future-stream ordering and explicit retention bounds.
func TestImportHelpersRejectMalformedMetadata(t *testing.T) {
	now := time.Now().UTC()
	if _, err := SealImportManifest(ImportManifest{ImportID: "metadata-import", RunID: "metadata-run", ProjectID: "metadata-project", SourceIdentity: "metadata-source", ExpectedHeadDigest: fixedDigest("head"), Counts: map[string]uint64{}, StreamDigests: map[string]string{"unsafe key": fixedDigest("stream")}, State: ImportFrozen, CreatedAt: now}); !errors.Is(err, ErrInvalid) {
		t.Fatal("unsafe digest map accepted", err)
	}
	ordered := orderedImportStreams(map[string]uint64{"future": 1, "events": 1})
	if len(ordered) != 2 || ordered[0] != "events" || ordered[1] != "future" {
		t.Fatal("future stream ordering", ordered)
	}
	if _, err := RunRetention(context.Background(), importRetentionProbe{}, &RetentionPolicy{}); !errors.Is(err, ErrInvalid) {
		t.Fatal("unbounded retention accepted", err)
	}
	documents := []ImportDocument{{Ordinal: 2, RelationKey: "second", Document: []byte("second")}, {Ordinal: 1, RelationKey: "first", Document: []byte("first")}}
	if importDocumentsDigest(documents, true) == "" || importDocumentsDigest(documents, false) == "" {
		t.Fatal("ordered digest absent")
	}
	malformed := functionalImportSource{Version: 2, MAC: "zzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzz", Claims: map[string]json.RawMessage{}, Intents: map[string]json.RawMessage{}, Terminals: map[string]json.RawMessage{}, Events: []json.RawMessage{}}
	raw, err := json.Marshal(malformed)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = sanitizeFunctionalImport(raw, bytes.Repeat([]byte{1}, 32)); !errors.Is(err, ErrIntegrity) {
		t.Fatal("non-hex functional MAC accepted", err)
	}
}

// TestLocalImportSnapshotRejectsUnsafeSources covers cancellation, malformed
// files, repeated inventory and nil ownership without changing source data.
func TestLocalImportSnapshotRejectsUnsafeSources(t *testing.T) {
	if err := (*LocalImportStore)(nil).SnapshotLocalStores(context.Background()); !errors.Is(err, ErrInvalid) {
		t.Fatal("nil source accepted")
	}
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	store, err := OpenLocalImportStore(t.TempDir(), "cancelled-source")
	if err != nil {
		t.Fatal(err)
	}
	if err = store.SnapshotLocalStores(cancelled); !errors.Is(err, context.Canceled) {
		t.Fatal("cancelled snapshot accepted", err)
	}
	emptyRoot := t.TempDir()
	if err = os.WriteFile(filepath.Join(emptyRoot, "workflow.json"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	emptyStore, err := OpenLocalImportStore(emptyRoot, "empty-source")
	if err != nil {
		t.Fatal(err)
	}
	if err = emptyStore.SnapshotLocalStores(context.Background()); !errors.Is(err, ErrIntegrity) {
		t.Fatal("empty durable file accepted", err)
	}
	repeatRoot := t.TempDir()
	if err = os.WriteFile(filepath.Join(repeatRoot, "workflow.json"), []byte(`{}`), 0o600); err != nil {
		t.Fatal(err)
	}
	repeatStore, err := OpenLocalImportStore(repeatRoot, "repeat-source")
	if err != nil || repeatStore.SnapshotLocalStores(context.Background()) != nil {
		t.Fatal("first snapshot", err)
	}
	if err = repeatStore.SnapshotLocalStores(context.Background()); !errors.Is(err, ErrConflict) {
		t.Fatal("repeated snapshot accepted", err)
	}
}

// TestLocalImportRejectsInvalidConcurrencyAndCorruption exercises the offline source's fail-closed recovery boundaries.
func TestLocalImportRejectsInvalidConcurrencyAndCorruption(t *testing.T) {
	ctx := context.Background()
	if _, err := OpenLocalImportStore("", "source"); !errors.Is(err, ErrInvalid) {
		t.Fatal("empty root accepted")
	}
	if _, err := OpenLocalImportStore(t.TempDir(), "unsafe identity"); !errors.Is(err, ErrInvalid) {
		t.Fatal("unsafe identity accepted")
	}
	root := t.TempDir()
	store, err := OpenLocalImportStore(root, "boundary-source")
	if err != nil {
		t.Fatal(err)
	}
	if err = EnsureLocalImportWritable(root); err != nil {
		t.Fatal(err)
	}
	first := ImportDocument{Stream: "events", RelationKey: "event-1", Document: []byte(`{"event":1}`)}
	if err = store.Put(ctx, first); err != nil {
		t.Fatal(err)
	}
	if err = store.Put(ctx, first); !errors.Is(err, ErrConflict) {
		t.Fatal("duplicate ordinal accepted")
	}
	if err = store.Put(ctx, ImportDocument{Stream: "events", Ordinal: 1, RelationKey: first.RelationKey, Document: []byte(`{"event":2}`)}); !errors.Is(err, ErrConflict) {
		t.Fatal("duplicate relation accepted")
	}
	now := time.Now().UTC()
	manifest, err := store.Freeze(ctx, "boundary-import", "boundary-run", "boundary-project", fixedDigest("head"), 1, now)
	if err != nil || manifest.EventDigests["events"] == "" {
		t.Fatal("freeze", err)
	}
	if replay, replayErr := store.Freeze(ctx, manifest.ImportID, manifest.RunID, manifest.ProjectID, manifest.ExpectedHeadDigest, manifest.ExpectedEpoch, now); replayErr != nil || replay.Digest != manifest.Digest {
		t.Fatal("freeze replay", replayErr)
	}
	if _, err = store.Freeze(ctx, "other-import", manifest.RunID, manifest.ProjectID, manifest.ExpectedHeadDigest, manifest.ExpectedEpoch, now); !errors.Is(err, ErrConflict) {
		t.Fatal("second freeze accepted")
	}
	if err = EnsureLocalImportWritable(root); !errors.Is(err, ErrConflict) {
		t.Fatal("frozen root writable")
	}
	if _, err = store.ReadImportPage(ctx, manifest.ImportID, "events", 0, 0); !errors.Is(err, ErrInvalid) {
		t.Fatal("zero page accepted")
	}
	if _, err = store.ReadImportPage(ctx, manifest.ImportID, "events", 0, 1001); !errors.Is(err, ErrInvalid) {
		t.Fatal("oversize page accepted")
	}
	if _, err = store.ReadImportPage(ctx, "wrong-import", "events", 0, 1); !errors.Is(err, ErrConflict) {
		t.Fatal("wrong import read")
	}
	if _, err = store.ReadImportPage(ctx, manifest.ImportID, "events", 2, 1); !errors.Is(err, ErrInvalid) {
		t.Fatal("out-of-range page accepted")
	}
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err = store.ReadImportPage(cancelled, manifest.ImportID, "events", 0, 1); !errors.Is(err, context.Canceled) {
		t.Fatal("cancelled read", err)
	}
	if err = store.Unfreeze(ctx, "wrong-import"); !errors.Is(err, ErrConflict) {
		t.Fatal("wrong unfreeze accepted")
	}
	if err = store.Archive(ctx, ImportCutoverReceipt{}); !errors.Is(err, ErrInvalid) {
		t.Fatal("invalid receipt accepted")
	}
	if err = os.WriteFile(filepath.Join(root, ".featureproof-import.lock"), []byte("owned"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err = store.Unfreeze(ctx, manifest.ImportID); !errors.Is(err, ErrConflict) {
		t.Fatal("concurrent lock accepted")
	}
	if err = os.Remove(filepath.Join(root, ".featureproof-import.lock")); err != nil {
		t.Fatal(err)
	}
	if err = store.Unfreeze(ctx, manifest.ImportID); err != nil {
		t.Fatal(err)
	}
	if _, err = store.ReadImportPage(ctx, manifest.ImportID, "events", 0, 1); !errors.Is(err, ErrConflict) {
		t.Fatal("unfrozen read accepted")
	}
	if err = os.WriteFile(filepath.Join(root, "featureproof-import.json"), []byte(`{"unknown":true}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err = EnsureLocalImportWritable(root); !errors.Is(err, ErrIntegrity) {
		t.Fatal("corrupt state accepted")
	}
	if err = store.Put(ctx, ImportDocument{Stream: "runs", RelationKey: "run", Document: []byte(`{}`)}); !errors.Is(err, ErrIntegrity) {
		t.Fatal("corrupt mutation accepted", err)
	}
	if err = EnsureLocalImportWritable(""); !errors.Is(err, ErrInvalid) {
		t.Fatal("empty writable root accepted")
	}
}

// TestLocalImportPreCutoverUnfreeze proves explicit abandonment restores the unchanged source authority.
func TestLocalImportPreCutoverUnfreeze(t *testing.T) {
	ctx := context.Background()
	store, err := OpenLocalImportStore(t.TempDir(), "local-source")
	if err != nil {
		t.Fatal(err)
	}
	manifest, err := store.Freeze(ctx, "import-2", "run-2", "project-1", fixedDigest("head"), 1, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	if err = store.Unfreeze(ctx, manifest.ImportID); err != nil {
		t.Fatal(err)
	}
	if err = store.Put(ctx, ImportDocument{Stream: "runs", RelationKey: "run-2", Document: []byte(`{}`)}); err != nil {
		t.Fatal("source remained frozen", err)
	}
}

// TestLocalImportFilesystemAndReceiptFailures exercises unavailable storage, corrupt lifecycle and bounded-state branches.
func TestLocalImportFilesystemAndReceiptFailures(t *testing.T) {
	ctx := context.Background()
	rootFile := filepath.Join(t.TempDir(), "root-file")
	if err := os.WriteFile(rootFile, []byte("not-a-directory"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := OpenLocalImportStore(rootFile, "filesystem-source"); !errors.Is(err, ErrUnavailable) {
		t.Fatal("file root accepted", err)
	}
	stateDirectoryRoot := t.TempDir()
	if err := os.Mkdir(filepath.Join(stateDirectoryRoot, "featureproof-import.json"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := EnsureLocalImportWritable(stateDirectoryRoot); !errors.Is(err, ErrUnavailable) {
		t.Fatal("unreadable state accepted", err)
	}
	store, err := OpenLocalImportStore(t.TempDir(), "filesystem-source")
	if err != nil {
		t.Fatal(err)
	}
	if err = store.Put(ctx, ImportDocument{}); !errors.Is(err, ErrInvalid) {
		t.Fatal("invalid source document accepted", err)
	}
	manifest, err := store.Freeze(ctx, "filesystem-import", "filesystem-run", "filesystem-project", fixedDigest("head"), 1, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	receipt, err := SealImportCutoverReceipt(ImportCutoverReceipt{ImportID: manifest.ImportID, RunID: manifest.RunID, SourceHead: manifest.ExpectedHeadDigest, TargetHead: fixedDigest("target-one"), ArchivedEpoch: 1, ActiveEpoch: 2, CommittedAt: time.Now().UTC()})
	if err != nil {
		t.Fatal(err)
	}
	if err = store.Archive(ctx, receipt); err != nil {
		t.Fatal(err)
	}
	conflictingReceipt, err := SealImportCutoverReceipt(ImportCutoverReceipt{ImportID: manifest.ImportID, RunID: manifest.RunID, SourceHead: manifest.ExpectedHeadDigest, TargetHead: fixedDigest("target-two"), ArchivedEpoch: 1, ActiveEpoch: 2, CommittedAt: receipt.CommittedAt})
	if err != nil {
		t.Fatal(err)
	}
	if err = store.Archive(ctx, conflictingReceipt); !errors.Is(err, ErrConflict) {
		t.Fatal("conflicting archive accepted", err)
	}
	if _, err = store.Freeze(ctx, "other-import", manifest.RunID, manifest.ProjectID, manifest.ExpectedHeadDigest, 1, time.Now().UTC()); !errors.Is(err, ErrConflict) {
		t.Fatal("archived source frozen again", err)
	}
	missingState, err := OpenLocalImportStore(t.TempDir(), "missing-state-source")
	if err != nil {
		t.Fatal(err)
	}
	if err = os.Remove(missingState.statePath()); err != nil {
		t.Fatal(err)
	}
	if _, err = missingState.ReadImportPage(ctx, "missing-import", "runs", 0, 1); !errors.Is(err, ErrUnavailable) {
		t.Fatal("missing durable state accepted", err)
	}
	if err = (*LocalImportStore)(nil).withLock(ctx, func(*localImportState) error { return nil }); !errors.Is(err, ErrInvalid) {
		t.Fatal("nil local store accepted", err)
	}
	if err = store.withLock(ctx, nil); !errors.Is(err, ErrInvalid) {
		t.Fatal("nil local transition accepted", err)
	}
	invalidRootStore := &LocalImportStore{root: rootFile, identity: "filesystem-source"}
	if err = invalidRootStore.writeState(localImportState{Streams: map[string][]ImportDocument{}}); !errors.Is(err, ErrUnavailable) {
		t.Fatal("unwritable state accepted", err)
	}
	oversized := localImportState{Streams: map[string][]ImportDocument{"runs": {{Stream: "runs", RelationKey: "large", Document: make([]byte, MaxCanonicalBytes+1)}}}}
	if err = store.writeState(oversized); !errors.Is(err, ErrInvalid) {
		t.Fatal("oversized local state accepted", err)
	}
}
