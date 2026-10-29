//go:build postgres_integration

package postgres

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/asierhr/featureProof/internal/agentplanning"
	"github.com/asierhr/featureProof/internal/controlplane"
	"github.com/asierhr/featureProof/internal/correction"
	"github.com/asierhr/featureProof/internal/environment"
	cleanup "github.com/asierhr/featureProof/internal/environment/cleanup"
	"github.com/asierhr/featureProof/internal/functionalflow"
	"github.com/asierhr/featureProof/internal/localvalidation"
	"github.com/asierhr/featureProof/internal/validationworkflow"
)

// TestPostgresConsumerAdapterMatrix exercises restartable consumer contracts against durable PostgreSQL state.
func TestPostgresConsumerAdapterMatrix(t *testing.T) {
	ctx := context.Background()
	store := integrationStore(t)
	if err := store.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Truncate(time.Microsecond)

	t.Run("cleanup capacity ledger", func(t *testing.T) {
		ledger := store.CleanupCapacityLedger()
		integrationReserveBackend(t, store, "matrix-run", "matrix-project", now)
		integrationReserveBackend(t, store, "matrix-other-run", "matrix-project", now)
		integrationReserveBackend(t, store, "matrix-run-two", "matrix-project", now)
		integrationReserveBackend(t, store, "matrix-orphan-run", "matrix-orphan-project", now)
		integrationReserveBackend(t, store, "matrix-unbound-run", "matrix-unbound-project", now)
		ctx := integrationBoundContext(t, store, "matrix-run")
		policy, err := cleanup.SealCapacityPolicy(cleanup.CapacityPolicy{ProjectID: "matrix-project", MaxEnvironments: 2, MaxCPUMilli: 2000, MaxMemoryBytes: 4096, MaxObjectCount: 20, MaxTTL: 2 * time.Hour})
		if err != nil {
			t.Fatal(err)
		}
		request := cleanup.CapacityRequest{ProjectID: policy.ProjectID, RunID: "matrix-run", AcquisitionKey: "matrix-acquisition", SelectionDigest: integrationDigest("selection"), Demand: cleanup.ResourceDemand{Environments: 1, CPUMilli: 100, MemoryBytes: 512, ObjectCount: 2}, TTL: time.Hour, Policy: policy}
		receipt, created, err := ledger.Reserve(ctx, request)
		if err != nil || !created {
			t.Fatal("reserve", created, err)
		}
		if repeated, repeatedCreated, repeatErr := ledger.Reserve(ctx, request); repeatErr != nil || repeatedCreated || repeated.Digest != receipt.Digest {
			t.Fatal("idempotent reserve", repeatedCreated, repeatErr)
		}
		conflicting := request
		conflicting.RunID = "matrix-other-run"
		if _, _, conflictErr := ledger.Reserve(integrationBoundContext(t, store, conflicting.RunID), conflicting); !cleanup.IsCategory(conflictErr, cleanup.CategoryConflict) {
			t.Fatal("conflicting reserve", conflictErr)
		}
		loaded, err := ledger.Receipt(ctx, receipt.ID)
		if err != nil || loaded.Digest != receipt.Digest {
			t.Fatal("receipt", err)
		}
		bound, err := ledger.BindLifecycle(ctx, receipt)
		if err != nil || !bound.LifecycleBound {
			t.Fatal("bind", err)
		}
		if rebound, bindErr := ledger.BindLifecycle(ctx, bound); bindErr != nil || rebound.Digest != bound.Digest {
			t.Fatal("idempotent bind", bindErr)
		}
		environmentDigest, resourceDigest := integrationDigest("environment"), integrationDigest("resource")
		confirmed, err := ledger.ConfirmEnvironment(ctx, bound, environmentDigest, resourceDigest)
		if err != nil {
			t.Fatal("confirm", err)
		}
		if _, confirmErr := ledger.ConfirmEnvironment(ctx, confirmed, integrationDigest("other"), resourceDigest); !cleanup.IsCategory(confirmErr, cleanup.CategoryConflict) {
			t.Fatal("conflicting confirmation", confirmErr)
		}
		if _, _, releaseErr := ledger.Release(ctx, confirmed, "matrix-cleanup", integrationDigest("wrong")); !cleanup.IsCategory(releaseErr, cleanup.CategoryConflict) {
			t.Fatal("invalid release", releaseErr)
		}
		confirmation := cleanup.ReleaseConfirmationDigest("matrix-cleanup", environmentDigest, resourceDigest, receipt.ID)
		released, changed, err := ledger.Release(ctx, confirmed, "matrix-cleanup", confirmation)
		if err != nil || !changed || !released.Released {
			t.Fatal("release", changed, err)
		}
		if _, changed, err = ledger.Release(ctx, released, "matrix-cleanup", confirmation); err != nil || changed {
			t.Fatal("release replay", changed, err)
		}
		values, next, err := ledger.ScanReceipts(ctx, "", 1)
		if err != nil || len(values) != 1 || next != "" {
			t.Fatal("scan", next, err)
		}
		if _, _, err = ledger.ScanReceipts(ctx, "", 0); !cleanup.IsCategory(err, cleanup.CategoryInvalid) {
			t.Fatal("invalid scan", err)
		}
		secondRequest := request
		secondRequest.RunID = "matrix-run-two"
		secondRequest.AcquisitionKey = "matrix-acquisition-two"
		if _, created, err = ledger.Reserve(integrationBoundContext(t, store, secondRequest.RunID), secondRequest); err != nil || !created {
			t.Fatal("second reserve", created, err)
		}
		if values, next, err = ledger.ScanReceipts(ctx, "", 1); err != nil || len(values) != 1 || next == "" {
			t.Fatal("paged scan", next, err)
		}
		overLimit := request
		overLimit.AcquisitionKey = "matrix-acquisition-over-limit"
		overLimit.Demand.Environments = policy.MaxEnvironments + 1
		if _, _, err = ledger.Reserve(ctx, overLimit); !cleanup.IsCategory(err, cleanup.CategoryConflict) {
			t.Fatal("capacity limit", err)
		}
		invalidPolicy := request
		invalidPolicy.Policy.Digest = ""
		if _, _, err = ledger.Reserve(ctx, invalidPolicy); !cleanup.IsCategory(err, cleanup.CategoryInvalid) {
			t.Fatal("invalid policy", err)
		}
		if _, _, err = ledger.AdoptUnknown(ctx, "", "run", "acquisition", integrationDigest("authority"), cleanup.ResourceDemand{Environments: 1}, time.Hour); !cleanup.IsCategory(err, cleanup.CategoryInvalid) {
			t.Fatal("invalid orphan accepted", err)
		}
		orphanContext := integrationBoundContext(t, store, "matrix-orphan-run")
		orphan, adopted, err := ledger.AdoptUnknown(orphanContext, "matrix-orphan-project", "matrix-orphan-run", "matrix-orphan-acquisition", integrationDigest("orphan-authority"), cleanup.ResourceDemand{Environments: 1, CPUMilli: 10, MemoryBytes: 10, ObjectCount: 1}, time.Hour)
		if err != nil || !adopted || !orphan.Unknown {
			t.Fatal("adopt orphan", adopted, err)
		}
		if replayed, changed, replayErr := ledger.AdoptUnknown(orphanContext, orphan.ProjectID, orphan.RunID, orphan.AcquisitionKey, orphan.AuthorityDigest, orphan.Demand, orphan.TTL); replayErr != nil || changed || replayed.ID != orphan.ID {
			t.Fatal("orphan replay", changed, replayErr)
		}
		if _, _, err = ledger.BlockUnknown(ctx, "", integrationDigest("identity")); !cleanup.IsCategory(err, cleanup.CategoryInvalid) {
			t.Fatal("invalid unknown block accepted", err)
		}
		blocked, changed, err := ledger.BlockUnknown(ctx, "matrix-block-project", integrationDigest("inventory-identity"))
		if err != nil || !changed || !blocked.Unknown {
			t.Fatal("block unknown", changed, err)
		}
		if replayed, replayChanged, replayErr := ledger.BlockUnknown(ctx, blocked.ProjectID, integrationDigest("inventory-identity")); replayErr != nil || replayChanged || replayed.ID != blocked.ID {
			t.Fatal("block replay", replayChanged, replayErr)
		}
		snapshot, err := ledger.Snapshot(orphan.ProjectID)
		if err != nil || !snapshot.Unknown || snapshot.Reserved.Environments != 1 {
			t.Fatal("orphan snapshot", snapshot, err)
		}
		unboundRequest := request
		unboundRequest.ProjectID, unboundRequest.RunID, unboundRequest.AcquisitionKey = "matrix-unbound-project", "matrix-unbound-run", "matrix-unbound-acquisition"
		unboundRequest.Policy, err = cleanup.SealCapacityPolicy(cleanup.CapacityPolicy{ProjectID: unboundRequest.ProjectID, MaxEnvironments: 1, MaxCPUMilli: 1000, MaxMemoryBytes: 4096, MaxObjectCount: 20, MaxTTL: 2 * time.Hour})
		if err != nil {
			t.Fatal(err)
		}
		unboundContext := integrationBoundContext(t, store, unboundRequest.RunID)
		unbound, _, err := ledger.Reserve(unboundContext, unboundRequest)
		if err != nil {
			t.Fatal(err)
		}
		if _, _, err = ledger.ReleaseUnbound(unboundContext, unbound, integrationDigest("wrong")); !cleanup.IsCategory(err, cleanup.CategoryConflict) {
			t.Fatal("invalid unbound release", err)
		}
		resolution := cleanup.ReceiptOnlyResolutionDigest(unbound)
		if freed, changed, releaseErr := ledger.ReleaseUnbound(unboundContext, unbound, resolution); releaseErr != nil || !changed || !freed.Released {
			t.Fatal("unbound release", changed, releaseErr)
		}
		if _, _, err = ledger.ReleaseConfirmed(ctx, "missing", "missing", integrationDigest("missing"), "cleanup", integrationDigest("confirmation")); !cleanup.IsCategory(err, cleanup.CategoryConflict) {
			t.Fatal("missing release binding", err)
		}
		if _, err = store.pool.Exec(ctx, `UPDATE cleanup_capacity_receipts SET receipt='not-json' WHERE receipt_id=$1`, blocked.ID); err != nil {
			t.Fatal(err)
		}
		if _, err = ledger.Receipt(ctx, blocked.ID); !cleanup.IsCategory(err, cleanup.CategoryIntegrity) {
			t.Fatal("corrupt capacity receipt accepted", err)
		}
		if _, err = ledger.Snapshot(blocked.ProjectID); !cleanup.IsCategory(err, cleanup.CategoryIntegrity) {
			t.Fatal("corrupt capacity snapshot accepted", err)
		}
	})

	t.Run("queue deadline exhaustion and cancellation", func(t *testing.T) {
		epoch, err := controlplane.SealBackendEpoch(controlplane.BackendEpoch{RunID: "matrix-queue-run", ProjectID: "matrix-project", Epoch: 1, Mode: controlplane.BackendPostgres, BackendIdentity: "pg-test", SchemaVersion: 1, CreatedAt: now})
		if err != nil {
			t.Fatal(err)
		}
		head, err := store.ReserveInitial(ctx, epoch)
		if err != nil {
			t.Fatal(err)
		}
		short, _ := controlplane.SealJob(controlplane.Job{JobID: "matrix-short-job", ProjectID: epoch.ProjectID, RunID: epoch.RunID, BackendEpoch: 1, BindingDigest: head.EpochDigest, Phase: controlplane.PhaseExecute, State: controlplane.JobQueued, InputDigest: integrationDigest("short"), CreatedAt: now, AvailableAt: now, Deadline: now.Add(10 * time.Second)})
		if _, err = store.Enqueue(ctx, short); err != nil {
			t.Fatal(err)
		}
		claimed, err := store.Claim(ctx, controlplane.ClaimRequest{ProjectID: epoch.ProjectID, WorkerID: "matrix-queue-worker", Now: now, Lease: time.Minute, Limit: 1})
		if err != nil || len(claimed) != 1 || !claimed[0].LeaseUntil.Equal(short.Deadline) {
			t.Fatal("bounded lease", err)
		}
		leased := claimed[0]
		if _, err = store.Heartbeat(ctx, leased.JobID, leased.Owner, leased.Fence, leased.CancelRevision, now.Add(time.Second), 5*time.Minute); err != nil {
			t.Fatal("bounded heartbeat", err)
		}
		dead, err := store.Retry(ctx, leased.JobID, leased.Owner, leased.Fence, leased.CancelRevision, now.Add(time.Second), controlplane.RetryDecision{Category: "exhausted", Backoff: time.Second, MaxAttempts: 1}, now.Add(2*time.Second))
		if err != nil || dead.State != controlplane.JobDead {
			t.Fatal("retry exhaustion", err)
		}
		cancelJob, _ := controlplane.SealJob(controlplane.Job{JobID: "matrix-cancel-job", ProjectID: epoch.ProjectID, RunID: epoch.RunID, BackendEpoch: 1, BindingDigest: head.EpochDigest, Phase: controlplane.PhaseExecute, State: controlplane.JobQueued, InputDigest: integrationDigest("cancel"), CreatedAt: now.Add(time.Second), AvailableAt: now.Add(time.Second), Deadline: now.Add(time.Hour)})
		if _, err = store.Enqueue(ctx, cancelJob); err != nil {
			t.Fatal(err)
		}
		claimed, err = store.Claim(ctx, controlplane.ClaimRequest{ProjectID: epoch.ProjectID, WorkerID: "matrix-cancel-worker", Now: now.Add(time.Second), Lease: time.Minute, Limit: 1})
		if err != nil || len(claimed) != 1 {
			t.Fatal("cancel claim", err)
		}
		cancelled, err := store.Cancel(ctx, claimed[0].JobID, 2)
		if err != nil || cancelled.State != controlplane.JobCancelled || cancelled.Fence <= claimed[0].Fence {
			t.Fatal("cancel", err)
		}
		if replay, replayErr := store.Cancel(ctx, cancelled.JobID, 2); replayErr != nil || replay.CancelRevision != 2 {
			t.Fatal("cancel replay", replayErr)
		}
		if _, err = store.Cancel(ctx, cancelled.JobID, 1); !errors.Is(err, controlplane.ErrConflict) {
			t.Fatal("stale cancel", err)
		}
		if _, err = store.Complete(ctx, claimed[0].JobID, claimed[0].Owner, claimed[0].Fence, claimed[0].CancelRevision, now, controlplane.JobSucceeded, ""); !errors.Is(err, controlplane.ErrConflict) {
			t.Fatal("cancelled completion", err)
		}
		if _, err = store.Heartbeat(ctx, claimed[0].JobID, claimed[0].Owner, claimed[0].Fence, claimed[0].CancelRevision, now, 0); !errors.Is(err, controlplane.ErrInvalid) {
			t.Fatal("invalid lease", err)
		}
	})

	t.Run("import replay conflicts and verification", func(t *testing.T) {
		document, err := controlplane.SealImportDocument(controlplane.ImportDocument{Stream: "packages", RelationKey: "typed-packages", Document: []byte(`{"items":[]}`)})
		if err != nil {
			t.Fatal(err)
		}
		manifest, err := controlplane.SealImportManifest(controlplane.ImportManifest{ImportID: "matrix-import", RunID: "matrix-import-run", ProjectID: "matrix-project", SourceIdentity: "matrix-local", ExpectedHeadDigest: integrationDigest("head"), ExpectedEpoch: 1, Counts: map[string]uint64{"packages": 1}, StreamDigests: map[string]string{"packages": postgresImportDigest([]controlplane.ImportDocument{document}, false)}, RelationDigests: map[string]string{"packages": postgresImportDigest([]controlplane.ImportDocument{document}, true)}, State: controlplane.ImportFrozen, CreatedAt: now})
		if err != nil {
			t.Fatal(err)
		}
		manifest, err = store.SaveImport(ctx, manifest, "")
		if err != nil {
			t.Fatal(err)
		}
		if replay, replayErr := store.SaveImport(ctx, manifest, ""); replayErr != nil || replay.Digest != manifest.Digest {
			t.Fatal("manifest replay", replayErr)
		}
		conflictingManifest := manifest
		conflictingManifest.SourceIdentity = "other-local"
		conflictingManifest.Digest = ""
		if _, conflictErr := store.SaveImport(ctx, conflictingManifest, ""); !errors.Is(conflictErr, controlplane.ErrConflict) {
			t.Fatal("manifest conflict accepted", conflictErr)
		}
		nonFrozen := manifest
		nonFrozen.State = controlplane.ImportCandidate
		nonFrozen.Digest = ""
		if _, conflictErr := store.SaveImport(ctx, nonFrozen, ""); !errors.Is(conflictErr, controlplane.ErrConflict) {
			t.Fatal("initial candidate accepted", conflictErr)
		}
		if err = store.SaveImportPage(ctx, manifest.ImportID, nil); !errors.Is(err, controlplane.ErrInvalid) {
			t.Fatal("empty import page accepted")
		}
		if _, err = store.CutoverImport(ctx, manifest.ImportID, now); !errors.Is(err, controlplane.ErrConflict) {
			t.Fatal("frozen import cut over")
		}
		if err = store.SaveImportPage(ctx, manifest.ImportID, []controlplane.ImportDocument{document}); err != nil {
			t.Fatal(err)
		}
		if err = store.SaveImportPage(ctx, manifest.ImportID, []controlplane.ImportDocument{document}); err != nil {
			t.Fatal("page replay", err)
		}
		conflicting := document
		conflicting.Document = []byte(`{"run":false}`)
		conflicting.Digest = ""
		if err = store.SaveImportPage(ctx, manifest.ImportID, []controlplane.ImportDocument{conflicting}); !errors.Is(err, controlplane.ErrConflict) {
			t.Fatal("page conflict", err)
		}
		verified, err := store.VerifyImport(ctx, manifest.ImportID)
		if err != nil || verified.State != controlplane.ImportVerified {
			t.Fatal("verify", err)
		}
		if repeated, repeatErr := store.VerifyImport(ctx, manifest.ImportID); repeatErr != nil || repeated.Digest != verified.Digest {
			t.Fatal("verify replay", repeatErr)
		}
		if _, err = store.LoadImportCutoverReceipt(ctx, manifest.ImportID); !errors.Is(err, controlplane.ErrNotFound) {
			t.Fatal("missing cutover receipt", err)
		}
		if _, err = store.AbandonImport(ctx, manifest.ImportID); !errors.Is(err, controlplane.ErrConflict) {
			t.Fatal("verified abandon accepted", err)
		}
		abandonCandidate, err := controlplane.SealImportManifest(controlplane.ImportManifest{ImportID: "matrix-abandon", RunID: "matrix-abandon-run", ProjectID: "matrix-project", SourceIdentity: "matrix-local", ExpectedHeadDigest: integrationDigest("abandon-head"), ExpectedEpoch: 1, Counts: map[string]uint64{}, StreamDigests: map[string]string{}, RelationDigests: map[string]string{}, State: controlplane.ImportFrozen, CreatedAt: now})
		if err != nil {
			t.Fatal(err)
		}
		if _, err = store.SaveImport(ctx, abandonCandidate, ""); err != nil {
			t.Fatal(err)
		}
		abandoned, err := store.AbandonImport(ctx, abandonCandidate.ImportID)
		if err != nil || abandoned.State != controlplane.ImportAbandoned {
			t.Fatal("abandon", err)
		}
		if replay, replayErr := store.AbandonImport(ctx, abandonCandidate.ImportID); replayErr != nil || replay.Digest != abandoned.Digest {
			t.Fatal("abandon replay", replayErr)
		}
		if _, err = store.CutoverImport(ctx, "", now); !errors.Is(err, controlplane.ErrInvalid) {
			t.Fatal("empty cutover accepted")
		}
		if _, err = store.CutoverImport(ctx, manifest.ImportID, time.Time{}); !errors.Is(err, controlplane.ErrInvalid) {
			t.Fatal("zero-time cutover accepted")
		}
	})

	t.Run("outbox and authority reject unowned work", func(t *testing.T) {
		claim := OutboxClaim{OperationID: "missing-operation", ProjectID: "matrix-project", Owner: "matrix-dispatcher", LeaseUntil: now.Add(time.Minute), Fence: 1, Attempts: 1}
		if _, err := store.ClaimEffect(ctx, "", claim.Owner, now, time.Minute); !errors.Is(err, controlplane.ErrInvalid) {
			t.Fatal("invalid effect claim accepted", err)
		}
		if _, err := store.ClaimEffect(ctx, claim.OperationID, claim.Owner, now, time.Minute); !errors.Is(err, controlplane.ErrConflict) {
			t.Fatal("missing effect claimed", err)
		}
		if _, err := store.LoadEffect(ctx, OutboxClaim{}); !errors.Is(err, controlplane.ErrNotFound) {
			t.Fatal("invalid effect load accepted", err)
		}
		if err := store.ConfirmEffectAndJob(ctx, claim, controlplane.Job{}, "short", now); !errors.Is(err, controlplane.ErrInvalid) {
			t.Fatal("invalid causal confirmation accepted", err)
		}
		if err := store.ConfirmEffectAndJob(ctx, claim, controlplane.Job{JobID: "missing-job"}, integrationDigest("receipt"), now); !errors.Is(err, controlplane.ErrNotFound) {
			t.Fatal("missing causal effect confirmed", err)
		}
		if err := store.HeartbeatOutbox(ctx, claim, time.Now().UTC().Add(10*time.Minute)); !errors.Is(err, controlplane.ErrInvalid) {
			t.Fatal("unbounded outbox heartbeat", err)
		}
		if err := store.RetryOutbox(ctx, claim, now, "transient"); !errors.Is(err, controlplane.ErrConflict) {
			t.Fatal("missing outbox retry", err)
		}
		if err := store.ConfirmOutbox(ctx, claim, "short"); !errors.Is(err, controlplane.ErrInvalid) {
			t.Fatal("invalid outbox receipt", err)
		}
		if err := store.ConfirmOutbox(ctx, claim, integrationDigest("receipt")); !errors.Is(err, controlplane.ErrNotFound) {
			t.Fatal("missing outbox confirm", err)
		}
		if err := store.SaveAuthorityGrant(ctx, controlplane.AuthorityGrant{}); err == nil {
			t.Fatal("invalid authority grant accepted")
		}
		if _, err := store.LoadAuthorityGrant(ctx, "missing-run", controlplane.PhaseExecute); !errors.Is(err, controlplane.ErrNotFound) {
			t.Fatal("missing authority grant", err)
		}
		if err := store.VerifyLiveAuthority(ctx, controlplane.AuthorityEnvelope{JobID: "missing-job"}, "worker", now); !errors.Is(err, controlplane.ErrNotFound) {
			t.Fatal("missing live authority", err)
		}
	})

	t.Run("cleanup scheduling and cursors", func(t *testing.T) {
		integrationReserveBackend(t, store, "matrix-cleanup-run", "matrix-project", now)
		ctx := integrationBoundContext(t, store, "matrix-cleanup-run")
		adapter := store.CleanupStore()
		binding, err := cleanup.SealLifecycleCleanupBinding(cleanup.LifecycleCleanupBinding{Version: cleanup.BindingVersion, ProjectID: "matrix-project", RunID: "matrix-cleanup-run", ProviderID: "provider", ProviderVersion: "v1", SelectionDigest: integrationDigest("selection"), AcquisitionKey: "matrix-acquisition-2", AcquisitionDigest: integrationDigest("acquisition"), EnvironmentDigest: integrationDigest("environment"), ResourceIdentityDigest: integrationDigest("resource"), LifecycleDigest: integrationDigest("lifecycle"), CleanupKey: "matrix-cleanup-key", CapacityReceiptDigest: integrationDigest("capacity"), LifecycleRecordDigest: integrationDigest("record")})
		if err != nil {
			t.Fatal(err)
		}
		item, err := cleanup.SealItem(cleanup.Item{Version: cleanup.Version, CleanupID: "matrix-cleanup-item", Source: "lifecycle", OwnerDigest: integrationDigest("owner"), ProjectID: binding.ProjectID, RunID: binding.RunID, Binding: binding, CreatedAt: now, ExpiresAt: now.Add(time.Hour), TTL: time.Hour, Demand: cleanup.ResourceDemand{Environments: 1, CPUMilli: 100, MemoryBytes: 512, ObjectCount: 2}, State: cleanup.StatePending, VersionNumber: 1})
		if err != nil {
			t.Fatal(err)
		}
		initialEvent := cleanup.Event{ID: "matrix-cleanup-reserved", CleanupID: item.CleanupID, ProjectID: item.ProjectID, Kind: "cleanup_reserved", At: now}
		item, created, err := adapter.Reserve(ctx, item, initialEvent)
		if err != nil || !created {
			t.Fatal("reserve", created, err)
		}
		if _, created, err = adapter.Reserve(ctx, item, initialEvent); err != nil || created {
			t.Fatal("reserve replay", created, err)
		}
		page, err := adapter.Scan(ctx, "", 1)
		if err != nil || len(page.Items) != 1 || !page.Scanned {
			t.Fatal("scan", err)
		}
		if _, err = adapter.Scan(ctx, "", 0); !cleanup.IsCategory(err, cleanup.CategoryInvalid) {
			t.Fatal("invalid scan", err)
		}
		item, claimed, err := adapter.Claim(ctx, item.CleanupID, "matrix-cleanup-worker", now, time.Minute)
		if err != nil || !claimed {
			t.Fatal("claim", claimed, err)
		}
		if _, claimed, err = adapter.Claim(ctx, item.CleanupID, "other-worker", now, time.Minute); err != nil || claimed {
			t.Fatal("live claim", claimed, err)
		}
		staleRevision := item
		item, err = adapter.Renew(ctx, item, item.LeaseOwner, item.FencingVersion, now.Add(time.Second), time.Minute)
		if err != nil {
			t.Fatal("renew", err)
		}
		if _, err = adapter.Renew(ctx, item, "stale-worker", item.FencingVersion, now.Add(time.Second), time.Minute); !cleanup.IsCategory(err, cleanup.CategoryConflict) {
			t.Fatal("stale renew", err)
		}
		staleRevision.Attempts++
		if _, err = adapter.SaveOwned(ctx, staleRevision, staleRevision.LeaseOwner, staleRevision.FencingVersion, cleanup.Event{ID: "matrix-cleanup-stale", Kind: "cleanup_progress", At: now.Add(2 * time.Second)}); !cleanup.IsCategory(err, cleanup.CategoryConflict) {
			t.Fatal("stale cleanup revision saved", err)
		}
		item.Attempts++
		item, err = adapter.SaveOwned(ctx, item, item.LeaseOwner, item.FencingVersion, cleanup.Event{ID: "matrix-cleanup-progress", CleanupID: item.CleanupID, ProjectID: item.ProjectID, Owner: item.LeaseOwner, Kind: "cleanup_progress", At: now.Add(2 * time.Second)})
		if err != nil {
			t.Fatal("save", err)
		}
		status, err := adapter.Status(ctx, item.CleanupID)
		if err != nil || status.Attempts != 1 {
			t.Fatal("status", err)
		}
		events, err := adapter.Events(ctx, item.CleanupID)
		if err != nil || len(events) != 3 {
			t.Fatal("events", len(events), err)
		}
		item, err = adapter.ReleaseOwner(ctx, item, item.LeaseOwner, item.FencingVersion, cleanup.Event{ID: "matrix-cleanup-release", CleanupID: item.CleanupID, ProjectID: item.ProjectID, Kind: "cleanup_owner_released", At: now.Add(3 * time.Second)})
		if err != nil || item.LeaseOwner != "" {
			t.Fatal("release owner", err)
		}
		if err = adapter.SaveCursor(ctx, "matrix-source", "", "first"); err != nil {
			t.Fatal("cursor save", err)
		}
		if value, loadErr := adapter.LoadCursor(ctx, "matrix-source"); loadErr != nil || value != "first" {
			t.Fatal("cursor load", value, loadErr)
		}
		if err = adapter.SaveCursor(ctx, "matrix-source", "first", "second"); err != nil {
			t.Fatal("legacy cursor", err)
		}
		if err = adapter.SaveCursor(ctx, "matrix-source", "first", "third"); !cleanup.IsCategory(err, cleanup.CategoryConflict) {
			t.Fatal("stale cursor observation saved", err)
		}
		pass, err := adapter.LoadCursorPass(ctx, "workflow-intents")
		if err != nil || pass.Epoch != 1 {
			t.Fatal("pass cursor genesis", err)
		}
		if err = adapter.SaveCursorPass(ctx, "workflow-intents", pass, cleanup.CursorPass{Epoch: 4}); !cleanup.IsCategory(err, cleanup.CategoryConflict) {
			t.Fatal("invalid cursor epoch", err)
		}
		item.State = cleanup.StateBlocked
		item, err = adapter.SaveOwned(ctx, item, "", item.FencingVersion, cleanup.Event{ID: "matrix-cleanup-blocked", CleanupID: item.CleanupID, ProjectID: item.ProjectID, Kind: "cleanup_blocked", At: now.Add(4 * time.Second)})
		if err != nil {
			t.Fatal("block item", err)
		}
		authorization, err := cleanup.SealRequeueAuthorization(cleanup.RequeueAuthorization{Version: cleanup.Version, ID: "matrix-requeue", Principal: "matrix-operator", CleanupID: item.CleanupID, ProjectID: item.ProjectID, ExpectedItemDigest: item.Digest, ReasonDigest: integrationDigest("reason")})
		if err != nil {
			t.Fatal(err)
		}
		item, err = adapter.Requeue(ctx, authorization, now.Add(5*time.Second))
		if err != nil || item.State != cleanup.StatePending {
			t.Fatal("requeue", err)
		}
		authorization.Digest = integrationDigest("tampered")
		if _, err = adapter.Requeue(ctx, authorization, now); !cleanup.IsCategory(err, cleanup.CategoryUnauthorized) {
			t.Fatal("tampered requeue", err)
		}
	})

	t.Run("functional intent and journal", func(t *testing.T) {
		integrationReserveBackend(t, store, "matrix-flow-run", "matrix-project", now)
		ctx := integrationBoundContext(t, store, "matrix-flow-run")
		if _, err := store.FunctionalStore(nil, 0); err == nil {
			t.Fatal("invalid functional store accepted")
		}
		adapter, err := store.FunctionalStore(integrationClock{now: now}, time.Minute)
		if err != nil {
			t.Fatal(err)
		}
		claim, created, err := adapter.Claim(ctx, "matrix-flow-run", 1, "step-1", "matrix-flow-worker")
		if err != nil || !created {
			t.Fatal("claim", created, err)
		}
		if _, _, invalidErr := adapter.Claim(ctx, "", 0, "", ""); invalidErr == nil {
			t.Fatal("invalid functional claim accepted")
		}
		if _, created, err = adapter.Claim(ctx, "matrix-flow-run", 1, "step-1", "other-worker"); err != nil || created {
			t.Fatal("claim contention", created, err)
		}
		claim, err = adapter.RenewClaim(ctx, "matrix-flow-run", 1, "step-1", claim)
		if err != nil {
			t.Fatal("renew", err)
		}
		staleClaim := claim
		staleClaim.Token = "stale-token"
		if _, err = adapter.RenewClaim(ctx, "matrix-flow-run", 1, "step-1", staleClaim); err == nil {
			t.Fatal("stale functional claim renewed")
		}
		if err = adapter.ConfirmIntent(ctx, functionalflow.Intent{}); err == nil {
			t.Fatal("invalid intent accepted")
		}
		intent := functionalflow.Intent{RunID: "matrix-flow-run", Revision: 1, StepID: "step-1", BindingDigest: integrationDigest("binding"), KeyDigest: integrationDigest("key"), ClaimToken: claim.Token}
		if err = adapter.ConfirmIntent(ctx, intent); err != nil {
			t.Fatal("intent", err)
		}
		if err = adapter.ConfirmIntent(ctx, intent); err != nil {
			t.Fatal("intent replay", err)
		}
		intent.EffectStarted = true
		if err = adapter.ConfirmIntent(ctx, intent); err != nil {
			t.Fatal("intent advance", err)
		}
		regressedIntent := intent
		regressedIntent.EffectStarted = false
		if err = adapter.ConfirmIntent(ctx, regressedIntent); err == nil {
			t.Fatal("intent regression accepted")
		}
		loadedIntent, found, err := adapter.Intent(ctx, intent.RunID, intent.Revision, intent.StepID)
		if err != nil || !found || loadedIntent.BindingDigest != intent.BindingDigest {
			t.Fatal("intent load", found, err)
		}
		if _, found, err = adapter.Intent(ctx, intent.RunID, intent.Revision, "missing"); err != nil || found {
			t.Fatal("missing intent", found, err)
		}
		firstEvent, err := adapter.AppendEvent(ctx, functionalflow.Event{Type: "artifact_committed", RunID: intent.RunID, FlowID: "matrix-flow", Revision: 1, ArtifactDigest: integrationDigest("artifact")})
		if err != nil || firstEvent.Sequence != 1 {
			t.Fatal("event", err)
		}
		if _, err = adapter.AppendEvent(ctx, functionalflow.Event{Type: "step_claimed", RunID: intent.RunID, FlowID: "matrix-flow", Revision: 1, StepID: intent.StepID, ArtifactDigest: integrationDigest("artifact")}); err != nil {
			t.Fatal("second event", err)
		}
		if events, eventsErr := adapter.Events(ctx, intent.RunID); eventsErr != nil || len(events) != 2 || events[1].PreviousDigest != firstEvent.Digest {
			t.Fatal("events", eventsErr)
		}
		terminal := functionalflow.StepRecord{RunID: intent.RunID, Revision: 1, StepID: intent.StepID, ClaimToken: claim.Token}
		if err = adapter.ConfirmTerminal(ctx, functionalflow.StepRecord{RunID: intent.RunID, Revision: 1, StepID: intent.StepID}); err == nil {
			t.Fatal("terminal without active claim token accepted")
		}
		if err = adapter.ConfirmTerminal(ctx, terminal); err != nil {
			t.Fatal("terminal", err)
		}
		terminal.ClaimToken = ""
		if err = adapter.ConfirmTerminal(ctx, terminal); err != nil {
			t.Fatal("terminal replay", err)
		}
		if _, created, err = adapter.Claim(ctx, intent.RunID, 1, intent.StepID, "late-worker"); err != nil || created {
			t.Fatal("terminal claim", created, err)
		}
		if _, found, err = adapter.Terminal(ctx, intent.RunID, 1, intent.StepID); err != nil || !found {
			t.Fatal("terminal load", found, err)
		}
		if _, found, err = adapter.Terminal(ctx, intent.RunID, 1, "missing"); err != nil || found {
			t.Fatal("missing terminal", found, err)
		}
		if err = adapter.ReconcileTerminal(ctx, functionalflow.StepRecord{RunID: intent.RunID, Revision: 1, StepID: "step-2"}); err != nil {
			t.Fatal("reconcile", err)
		}
		if err = adapter.ReconcileTerminal(ctx, functionalflow.StepRecord{RunID: intent.RunID, Revision: 1, StepID: "step-2", EvidenceID: "different"}); err == nil {
			t.Fatal("conflicting reconciliation accepted")
		}
	})

	t.Run("workflow lifecycle and immutable artifacts", func(t *testing.T) {
		adapter := store.WorkflowStore()
		record := validationworkflow.Record{Version: 1, Handle: "matrix-workflow", Principal: "matrix-principal", IdempotencyKey: "matrix-workflow-key", RequestDigest: integrationDigest("request"), RepositoryID: "matrix-repository", Revision: strings.Repeat("c", 40), Phase: validationworkflow.PhaseAdmitted}
		proposed := record
		record, created, ctx, err := adapter.ReserveBound(ctx, record, "matrix-workflow-worker", now, time.Minute)
		if err != nil || !created {
			t.Fatal("reserve", created, err)
		}
		if loaded, loadErr := adapter.Load(ctx, record.Handle); loadErr != nil || loaded.VersionCAS != record.VersionCAS {
			t.Fatal("load", loadErr)
		}
		if foundRecord, found, findErr := adapter.FindReservation(ctx, record.Principal, record.IdempotencyKey); findErr != nil || !found || foundRecord.Handle != record.Handle {
			t.Fatal("find reservation", found, findErr)
		}
		if _, found, findErr := adapter.FindReservation(ctx, record.Principal, "missing-key"); findErr != nil || found {
			t.Fatal("missing reservation", found, findErr)
		}
		if _, _, findErr := adapter.FindReservation(ctx, "unsafe principal", record.IdempotencyKey); findErr == nil {
			t.Fatal("unsafe reservation lookup accepted")
		}
		if _, loadErr := adapter.Load(ctx, "unsafe handle"); loadErr == nil {
			t.Fatal("unsafe workflow handle accepted")
		}
		if _, cancelErr := adapter.RequestCancel(ctx, "missing-workflow", now, validationworkflow.Event{}); cancelErr == nil {
			t.Fatal("missing workflow cancelled")
		}
		if _, _, claimErr := adapter.Claim(ctx, "missing-workflow", "worker", now, time.Minute); claimErr == nil {
			t.Fatal("missing workflow claimed")
		}
		if replay, replayCreated, replayErr := adapter.Reserve(ctx, proposed, "matrix-workflow-worker", now, time.Minute); replayErr != nil || replayCreated || replay.Handle != record.Handle {
			t.Fatal("reserve replay", replayCreated, replayErr)
		}
		conflictingProposal := proposed
		conflictingProposal.RequestDigest = integrationDigest("different-request")
		if _, _, conflictErr := adapter.Reserve(ctx, conflictingProposal, "matrix-workflow-worker", now, time.Minute); conflictErr == nil {
			t.Fatal("workflow idempotency conflict accepted")
		}
		if _, activeClaimed, claimErr := adapter.Claim(ctx, record.Handle, "other-workflow-worker", now, time.Minute); claimErr != nil || activeClaimed {
			t.Fatal("active workflow reclaimed", activeClaimed, claimErr)
		}
		if _, renewErr := adapter.Renew(ctx, record.Handle, "other-workflow-worker", now, time.Minute); renewErr == nil {
			t.Fatal("wrong workflow owner renewed")
		}
		if _, releaseErr := adapter.ReleaseOwner(ctx, record.Handle, "other-workflow-worker", record.VersionCAS, now); releaseErr == nil {
			t.Fatal("wrong workflow owner released")
		}
		record, err = adapter.Renew(ctx, record.Handle, record.Owner, now.Add(time.Second), time.Minute)
		if err != nil {
			t.Fatal("renew", err)
		}
		record, err = adapter.ReleaseOwner(ctx, record.Handle, record.Owner, record.VersionCAS, now.Add(2*time.Second))
		if err != nil {
			t.Fatal("release", err)
		}
		if pending, pendingErr := adapter.Pending(ctx, now.Add(3*time.Second)); pendingErr != nil || len(pending) == 0 {
			t.Fatal("pending", pendingErr)
		}
		record, claimed, err := adapter.Claim(ctx, record.Handle, "matrix-workflow-worker-2", now.Add(3*time.Second), time.Minute)
		if err != nil || !claimed {
			t.Fatal("claim", claimed, err)
		}
		record, err = adapter.RequestCancel(ctx, record.Handle, now.Add(4*time.Second), validationworkflow.Event{Type: "cancel_requested"})
		if err != nil || !record.CancelRequested {
			t.Fatal("cancel", err)
		}
		if replay, replayErr := adapter.RequestCancel(ctx, record.Handle, now.Add(5*time.Second), validationworkflow.Event{Type: "cancel_requested"}); replayErr != nil || !replay.CancelRequested {
			t.Fatal("cancel replay", replayErr)
		}
		artifact, err := validationworkflow.SealPlanArtifactForStore(validationworkflow.PlanArtifact{Version: validationworkflow.ArtifactVersion, WorkflowID: record.Handle, PlanID: "matrix-plan", Provider: "openai", Model: "model", Plans: []localvalidation.PlanProjection{{ID: "matrix-plan"}}, ContextInstructionsHash: integrationDigest("instructions")})
		if err != nil {
			t.Fatal("seal artifact", err)
		}
		if err = store.PlanArtifactStore().Commit(ctx, artifact); err != nil {
			t.Fatal("artifact commit", err)
		}
		if err = store.PlanArtifactStore().Commit(ctx, artifact); err != nil {
			t.Fatal("artifact replay", err)
		}
		conflictingArtifact := artifact
		conflictingArtifact.PlanID = "other-plan"
		conflictingArtifact.Plans = []localvalidation.PlanProjection{{ID: "other-plan"}}
		conflictingArtifact, err = validationworkflow.SealPlanArtifactForStore(conflictingArtifact)
		if err != nil {
			t.Fatal("seal conflicting artifact", err)
		}
		if err = store.PlanArtifactStore().Commit(ctx, conflictingArtifact); err == nil {
			t.Fatal("artifact conflict accepted")
		}
		if loaded, loadErr := store.PlanArtifactStore().Load(ctx, record.Handle); loadErr != nil || loaded.ArtifactSHA256 != artifact.ArtifactSHA256 {
			t.Fatal("artifact load", loadErr)
		}
		if _, loadErr := store.PlanArtifactStore().Load(ctx, "missing-artifact"); !errors.Is(loadErr, validationworkflow.ErrNotFound) {
			t.Fatal("missing artifact", loadErr)
		}
		material := validationworkflow.ExecutableMaterial{Version: validationworkflow.ArtifactVersion, WorkflowID: record.Handle, ArtifactSHA256: artifact.ArtifactSHA256, ContextDigest: integrationDigest("context"), RevisionDigest: integrationDigest("revision"), TargetDigest: integrationDigest("target"), Snapshot: agentplanning.PreparedSnapshot{ID: "matrix-plan", Checks: []agentplanning.CandidateCheck{{}}}, CommitmentKey: []byte("0123456789abcdef0123456789abcdef")}
		if err = store.ExecutablePlanStore().Commit(ctx, record.Handle, material); err != nil {
			t.Fatal("executable commit", err)
		}
		loadedMaterial, err := store.ExecutablePlanStore().Load(ctx, record.Handle)
		if err != nil || string(loadedMaterial.CommitmentKey) != "0123456789abcdef0123456789abcdef" {
			t.Fatal("executable load", err)
		}
		loadedMaterial.CommitmentKey[0] = 'X'
		if reloaded, reloadErr := store.ExecutablePlanStore().Load(ctx, record.Handle); reloadErr != nil || string(reloaded.CommitmentKey) != "0123456789abcdef0123456789abcdef" {
			t.Fatal("executable detached", reloadErr)
		}
		conflictingMaterial := material
		conflictingMaterial.CommitmentKey = []byte("abcdef0123456789abcdef0123456789")
		if err = store.ExecutablePlanStore().Commit(ctx, record.Handle, conflictingMaterial); err == nil {
			t.Fatal("executable conflict accepted")
		}
		if err = store.ExecutablePlanStore().Delete(ctx, record.Handle); err != nil {
			t.Fatal("executable delete", err)
		}
		if err = store.ExecutablePlanStore().Delete(ctx, record.Handle); err != nil {
			t.Fatal("executable delete replay", err)
		}
		if _, loadErr := store.ExecutablePlanStore().Load(ctx, record.Handle); loadErr == nil {
			t.Fatal("deleted executable loaded")
		}
	})

	t.Run("environment lifecycle and capacity conflicts", func(t *testing.T) {
		integrationReserveBackend(t, store, "matrix-environment", "matrix-project", now)
		ctx := integrationBoundContext(t, store, "matrix-environment")
		adapter := store.EnvironmentStore()
		if _, _, err := adapter.Reserve(ctx, environment.Record{}); err == nil {
			t.Fatal("invalid environment reserve accepted")
		}
		if _, err := adapter.Load(ctx, "missing-environment"); err == nil {
			t.Fatal("missing environment loaded")
		}
		selection, err := environment.SealSelection(environment.Selection{Version: 1, Alias: "matrix-env", ProviderID: "provider", ProviderVersion: "v1", Kind: environment.KindManaged, TemplateReference: "template", ProfileID: "profile", NetworkPolicyID: "network", Secretless: true, ProjectID: "matrix-project", RepositoryID: "matrix-repository", Capabilities: []string{"http"}})
		if err != nil {
			t.Fatal(err)
		}
		record := environment.Record{Version: 1, RunID: "matrix-environment", ProjectID: "matrix-project", RequestDigest: integrationDigest("request"), AcquisitionKey: integrationDigest("environment-acquisition"), CommitOID: strings.Repeat("e", 40), Selection: selection, State: environment.StateSelected, Revision: 1, CreatedAt: now, TTL: time.Hour, ExpiresAt: now.Add(time.Hour), Demand: environment.ResourceDemand{Environments: 1, CPUMilli: 100, MemoryBytes: 512, ObjectCount: 1}, CapacityReceipt: environment.CapacityReceipt{ID: "matrix-capacity", Digest: integrationDigest("capacity")}}
		record, created, err := adapter.Reserve(ctx, record)
		if err != nil || !created {
			t.Fatal("reserve", created, err)
		}
		if same, repeated, repeatErr := adapter.Reserve(ctx, record); repeatErr != nil || repeated || same.RunID != record.RunID {
			t.Fatal("reserve replay", repeated, repeatErr)
		}
		if loaded, loadErr := adapter.Load(ctx, record.RunID); loadErr != nil || loaded.Revision != 1 {
			t.Fatal("load", loadErr)
		}
		record.State = environment.StateAcquiring
		record.ReleaseRequired = true
		record, err = adapter.Save(ctx, record, record.Revision, integrationEnvironmentEvent(record, "matrix-environment-acquiring", now))
		if err != nil {
			t.Fatal("save", err)
		}
		if _, err = adapter.Save(ctx, record, 1, integrationEnvironmentEvent(record, "matrix-environment-stale", now)); err == nil {
			t.Fatal("stale save accepted")
		}
		job := integrationJob(now, "matrix-environment-job", 1, integrationDigest("binding"))
		effect := controlplane.EffectIntent{OperationID: "matrix-environment-effect", IdempotencyKey: "matrix-environment-key", ProjectID: record.ProjectID, RunID: record.RunID, Phase: controlplane.PhaseCleanup, BindingDigest: integrationDigest("binding"), InputDigest: integrationDigest("input")}
		if _, err = adapter.SaveCausal(ctx, record, record.Revision, integrationEnvironmentEvent(record, "matrix-environment-two-outputs", now), &job, &effect); err == nil {
			t.Fatal("two causal outputs accepted")
		}
		if events, eventsErr := adapter.Events(ctx, record.RunID); eventsErr != nil || len(events) != 1 {
			t.Fatal("events", len(events), eventsErr)
		}
		if _, err = store.CapacityAdmission(nil); !errors.Is(err, controlplane.ErrInvalid) {
			t.Fatal("empty capacity policy accepted")
		}
		if _, err = store.CapacityAdmission(map[string]environment.CapacityLimits{"matrix-project": {}}); !errors.Is(err, controlplane.ErrInvalid) {
			t.Fatal("invalid capacity policy accepted")
		}
		capacity, err := store.CapacityAdmission(map[string]environment.CapacityLimits{"matrix-project": {Environments: 1, CPUMilli: 100, MemoryBytes: 512, ObjectCount: 1, MaxTTL: time.Hour}})
		if err != nil {
			t.Fatal(err)
		}
		request := environment.CapacityRequest{ProjectID: "matrix-project", RunID: record.RunID, AcquisitionKey: "matrix-environment-capacity", SelectionDigest: selection.Digest, Demand: record.Demand, TTL: time.Minute, PolicyDigest: integrationDigest("policy")}
		receipt, err := capacity.Reserve(ctx, request)
		if err != nil {
			t.Fatal("capacity reserve", err)
		}
		if replay, replayErr := capacity.Reserve(ctx, request); replayErr != nil || replay.Digest != receipt.Digest {
			t.Fatal("capacity replay", replayErr)
		}
		conflicting := request
		conflicting.RunID = "other-run"
		if _, err = capacity.Reserve(ctx, conflicting); err == nil {
			t.Fatal("capacity conflict accepted")
		}
		unauthorized := request
		unauthorized.ProjectID = "other-project"
		if _, err = capacity.Reserve(ctx, unauthorized); err == nil {
			t.Fatal("unknown project accepted")
		}
		if _, err = capacity.Confirm(ctx, environment.CapacityReceipt{ID: receipt.ID, Digest: integrationDigest("wrong")}, integrationDigest("environment"), integrationDigest("resource")); err == nil {
			t.Fatal("wrong receipt confirmed")
		}
		unconfirmedProof := environment.CapacityReleaseConfirmation("matrix-cleanup", "", "", receipt.ID)
		if err = capacity.Release(ctx, receipt, "matrix-cleanup", unconfirmedProof); err == nil {
			t.Fatal("unconfirmed capacity released")
		}
		if _, err = capacity.Confirm(ctx, receipt, integrationDigest("environment"), integrationDigest("resource")); err != nil {
			t.Fatal("capacity confirm", err)
		}
		overLimit := request
		overLimit.AcquisitionKey = "matrix-environment-over-limit"
		if _, err = capacity.Reserve(ctx, overLimit); err == nil {
			t.Fatal("aggregate capacity limit ignored")
		}
	})

	t.Run("correction lineage budget and recovery", func(t *testing.T) {
		integrationReserveBackend(t, store, "matrix-parent", "matrix-project", now)
		ctx := integrationBoundContext(t, store, "matrix-parent")
		adapter := store.CorrectionStore()
		if _, err := adapter.Reserve(ctx, correction.Reservation{}); err == nil {
			t.Fatal("invalid correction reserve accepted")
		}
		link, err := (correction.CorrectionLink{Version: correction.Version, CorrectionID: "matrix-correction", Handle: "matrix-correction", PrincipalID: "matrix-project", ParentRunID: "matrix-parent", ParentBindingSHA256: integrationDigest("parent"), RootRunID: "matrix-parent", Attempt: 1, SelectionAlias: "matrix-selection", SelectionSHA256: integrationDigest("selection"), PolicySHA256: integrationDigest("policy"), LimitsSHA256: integrationDigest("limits"), CreatedAt: now, UpdatedAt: now}).Seal()
		if err != nil {
			t.Fatal(err)
		}
		limits := correction.ChainLimits{MaxAttempts: 2, MaxDuration: time.Hour, MaxModelCalls: 4, MaxModelTokens: 1000}
		record := correction.Record{Link: link, RequestHash: integrationDigest("request"), KeyHash: integrationDigest("key"), Phase: correction.PhaseReserved, Owner: "matrix-correction-worker"}
		reserved, err := adapter.Reserve(ctx, correction.Reservation{Record: record, Limits: limits})
		if err != nil || !reserved.New {
			t.Fatal("reserve", err)
		}
		if replay, replayErr := adapter.Reserve(ctx, correction.Reservation{Record: record, Limits: limits}); replayErr != nil || replay.New {
			t.Fatal("reserve replay", replayErr)
		}
		loaded, err := adapter.Get(ctx, record.Link.Handle)
		if err != nil || loaded.KeyHash != record.KeyHash {
			t.Fatal("get", err)
		}
		loaded.Phase = correction.PhasePreflight
		loaded.Link.UpdatedAt = now.Add(time.Second)
		loaded.Link, _ = loaded.Link.Seal()
		loaded, err = adapter.Update(ctx, loaded.Link.Handle, correction.PhaseReserved, loaded, "preflight_started")
		if err != nil {
			t.Fatal("update", err)
		}
		loaded, allowance, err := adapter.ReserveRemainingBudget(ctx, loaded.Link.Handle, correction.PhasePreflight, now.Add(2*time.Second))
		if err != nil || allowance.Calls != limits.MaxModelCalls {
			t.Fatal("budget reserve", allowance, err)
		}
		if _, _, err = adapter.ReserveRemainingBudget(ctx, loaded.Link.Handle, correction.PhasePreflight, now.Add(2*time.Second)); err == nil {
			t.Fatal("duplicate budget reservation accepted")
		}
		if _, err = adapter.ConfirmBudget(ctx, loaded.Link.Handle, correction.PhasePreflight, correction.ModelUsage{Calls: allowance.Calls + 1}, now.Add(3*time.Second)); err == nil {
			t.Fatal("excess budget confirmation accepted")
		}
		loaded, err = adapter.ConfirmBudget(ctx, loaded.Link.Handle, correction.PhasePreflight, correction.ModelUsage{Calls: 1, Tokens: 20}, now.Add(3*time.Second))
		if err != nil || loaded.Usage.Calls != 1 {
			t.Fatal("budget confirm", err)
		}
		if _, err = adapter.ConfirmBudget(ctx, loaded.Link.Handle, correction.PhasePreflight, correction.ModelUsage{Calls: 1}, now.Add(3*time.Second)); err == nil {
			t.Fatal("unreserved budget confirmation accepted")
		}
		if _, _, err = adapter.ReserveCancel(ctx, loaded.Link.Handle, "wrong-principal", integrationDigest("cancel-key"), integrationDigest("cancel-request"), now); err == nil {
			t.Fatal("cross-principal cancel accepted")
		}
		loaded, changed, err := adapter.ReserveCancel(ctx, loaded.Link.Handle, loaded.Link.PrincipalID, integrationDigest("cancel-key"), integrationDigest("cancel-request"), now.Add(4*time.Second))
		if err != nil || !changed {
			t.Fatal("cancel", changed, err)
		}
		if _, changed, err = adapter.ReserveCancel(ctx, loaded.Link.Handle, loaded.Link.PrincipalID, integrationDigest("cancel-key"), integrationDigest("cancel-request"), now.Add(5*time.Second)); err != nil || changed {
			t.Fatal("cancel replay", changed, err)
		}
		if _, _, err = adapter.ReserveCancel(ctx, loaded.Link.Handle, loaded.Link.PrincipalID, integrationDigest("other-key"), integrationDigest("cancel-request"), now.Add(5*time.Second)); err == nil {
			t.Fatal("cancel conflict")
		}
		if events, eventsErr := adapter.Events(ctx, loaded.Link.Handle); eventsErr != nil || len(events) < 5 {
			t.Fatal("events", len(events), eventsErr)
		}
		if err = adapter.Recover(ctx, "matrix-recovery", now.Add(6*time.Second)); err != nil {
			t.Fatal("recover", err)
		}
		terminal, err := adapter.Get(ctx, loaded.Link.Handle)
		if err != nil || terminal.Phase != correction.PhaseTerminal {
			t.Fatal("terminal recovery", err)
		}
		adapter.ReleaseOwner(loaded.Link.Handle)
		if _, err = adapter.Get(ctx, "missing-correction"); err == nil {
			t.Fatal("missing correction")
		}
		pendingLink, _ := (correction.CorrectionLink{Version: correction.Version, CorrectionID: "matrix-pending", Handle: "matrix-pending", PrincipalID: "matrix-project", ParentRunID: loaded.Link.Handle, ParentBindingSHA256: integrationDigest("parent-two"), RootRunID: "matrix-parent", Attempt: 2, SelectionAlias: "matrix-selection", SelectionSHA256: integrationDigest("selection"), PolicySHA256: integrationDigest("policy"), LimitsSHA256: integrationDigest("limits"), CreatedAt: now.Add(10 * time.Second), UpdatedAt: now.Add(10 * time.Second)}).Seal()
		pending, err := adapter.Reserve(ctx, correction.Reservation{Record: correction.Record{Link: pendingLink, RequestHash: integrationDigest("pending-request"), KeyHash: integrationDigest("pending-key"), Phase: correction.PhaseReserved}, Limits: limits})
		if err != nil {
			t.Fatal("pending reserve", err)
		}
		pendingRecord := pending.Record
		for _, phase := range []correction.Phase{correction.PhasePreflight, correction.PhaseAwaitingApproval, correction.PhaseFailedFirst, correction.PhaseRegression, correction.PhaseFinalizing} {
			expected := pendingRecord.Phase
			pendingRecord.Phase = phase
			pendingRecord.Link.UpdatedAt = pendingRecord.Link.UpdatedAt.Add(time.Second)
			pendingRecord.Link, _ = pendingRecord.Link.Seal()
			if phase == correction.PhaseFinalizing {
				pendingRecord.ComparisonIntent = &correction.Comparison{}
			}
			pendingRecord, err = adapter.Update(ctx, pendingRecord.Link.Handle, expected, pendingRecord, "matrix_phase_advanced")
			if err != nil {
				t.Fatal("phase advance", phase, err)
			}
		}
		claimedComparisons, err := adapter.ClaimPendingComparisons(ctx, "matrix-finalizer", now.Add(time.Minute))
		if err != nil || len(claimedComparisons) != 1 || claimedComparisons[0].Owner != "matrix-finalizer" {
			t.Fatal("pending comparisons", len(claimedComparisons), err)
		}
		adapter.ReleaseOwner(pendingRecord.Link.Handle)
		if _, err = store.WorkflowStore().Load(ctx, "missing-workflow"); !errors.Is(err, validationworkflow.ErrNotFound) {
			t.Fatal("missing workflow", err)
		}
	})

	t.Run("durable corruption rejection", func(t *testing.T) {
		queueHead, queueEpoch, err := store.Load(ctx, "matrix-queue-run")
		if err != nil {
			t.Fatal(err)
		}
		grant, _ := controlplane.SealAuthorityGrant(controlplane.AuthorityGrant{ProjectID: queueHead.ProjectID, RunID: queueHead.RunID, BackendEpoch: queueHead.Epoch, BindingDigest: queueHead.EpochDigest, Phase: controlplane.PhaseExecute, AllowedCapabilities: []string{"http.execute"}, TargetBindingRef: "matrix-target", TargetBindingDigest: integrationDigest("target")})
		if err = store.SaveAuthorityGrant(ctx, grant); err != nil {
			t.Fatal(err)
		}
		if _, err = store.pool.Exec(ctx, `UPDATE authority_grants SET document='not-json'::bytea WHERE run_id='matrix-queue-run'`); err != nil {
			t.Fatal(err)
		}
		if _, err = store.LoadAuthorityGrant(ctx, queueHead.RunID, controlplane.PhaseExecute); !errors.Is(err, controlplane.ErrIntegrity) {
			t.Fatal("corrupt authority grant accepted", err)
		}
		effect := controlplane.EffectIntent{OperationID: "matrix-corrupt-effect", IdempotencyKey: "matrix-corrupt-effect-key", ProjectID: queueHead.ProjectID, RunID: queueHead.RunID, Phase: controlplane.PhasePublish, BindingDigest: queueHead.EpochDigest, InputDigest: integrationDigest("effect-input"), ReconcileMethod: "poll"}
		if _, err = store.Prepare(ctx, effect); err != nil {
			t.Fatal(err)
		}
		claims, err := store.ClaimOutbox(ctx, "matrix-corrupt-dispatcher", time.Now().UTC(), time.Minute, 100)
		if err != nil {
			t.Fatal("corrupt effect claim", err)
		}
		var effectClaim OutboxClaim
		for _, claim := range claims {
			if claim.OperationID == effect.OperationID {
				effectClaim = claim
			}
		}
		if effectClaim.OperationID == "" {
			t.Fatal("corrupt effect was not claimed")
		}
		if _, err = store.pool.Exec(ctx, `UPDATE effect_intents SET document='not-json'::bytea WHERE operation_id='matrix-corrupt-effect'`); err != nil {
			t.Fatal(err)
		}
		if err = store.ConfirmOutbox(ctx, effectClaim, integrationDigest("receipt")); !errors.Is(err, controlplane.ErrIntegrity) {
			t.Fatal("corrupt effect accepted", err)
		}
		admission := integrationAdmission(now, "matrix-corrupt")
		if _, err = store.Admit(ctx, admission); err != nil {
			t.Fatal(err)
		}
		if _, err = store.pool.Exec(ctx, `UPDATE ingress_inbox SET envelope='not-json'::bytea WHERE source=$1 AND idempotency_key=$2`, admission.Envelope.Source, admission.Envelope.IdempotencyKey); err != nil {
			t.Fatal(err)
		}
		if _, err = store.LoadIngress(ctx, admission.Epoch.RunID); !errors.Is(err, controlplane.ErrIntegrity) {
			t.Fatal("corrupt ingress accepted", err)
		}
		if _, err = store.pool.Exec(ctx, `UPDATE jobs SET document='not-json'::bytea WHERE job_id='matrix-cancel-job'`); err != nil {
			t.Fatal(err)
		}
		if _, err = store.Cancel(ctx, "matrix-cancel-job", 3); !errors.Is(err, controlplane.ErrIntegrity) {
			t.Fatal("corrupt queue job accepted", err)
		}
		if _, err = store.pool.Exec(ctx, `UPDATE backend_binding_heads SET document='not-json'::bytea WHERE run_id='matrix-queue-run'`); err != nil {
			t.Fatal(err)
		}
		if _, _, err = store.Load(ctx, queueEpoch.RunID); !errors.Is(err, controlplane.ErrIntegrity) {
			t.Fatal("corrupt binding head accepted", err)
		}
		if _, err := store.pool.Exec(ctx, `UPDATE workflow_events SET event='{}'::bytea WHERE handle='matrix-workflow' AND sequence=1`); err != nil {
			t.Fatal(err)
		}
		if _, err := store.WorkflowStore().Events(ctx, "matrix-workflow"); err == nil {
			t.Fatal("corrupt workflow event accepted")
		}
		if _, err := store.pool.Exec(ctx, `UPDATE workflow_artifacts SET artifact='not-json'::bytea WHERE workflow_id='matrix-workflow'`); err != nil {
			t.Fatal(err)
		}
		if _, err := store.PlanArtifactStore().Load(ctx, "matrix-workflow"); err == nil {
			t.Fatal("corrupt workflow artifact accepted")
		}
		if _, err := store.pool.Exec(ctx, `UPDATE functional_intents SET intent='not-json'::bytea WHERE run_id='matrix-flow-run'`); err != nil {
			t.Fatal(err)
		}
		functionalAdapter, err := store.FunctionalStore(integrationClock{now: now}, time.Minute)
		if err != nil {
			t.Fatal(err)
		}
		if _, _, err := functionalAdapter.Intent(ctx, "matrix-flow-run", 1, "step-1"); err == nil {
			t.Fatal("corrupt functional intent accepted")
		}
		if _, err := store.pool.Exec(ctx, `UPDATE functional_terminals SET terminal='not-json'::bytea WHERE run_id='matrix-flow-run' AND step_id='step-1'`); err != nil {
			t.Fatal(err)
		}
		if _, _, err := functionalAdapter.Terminal(ctx, "matrix-flow-run", 1, "step-1"); err == nil {
			t.Fatal("corrupt functional terminal accepted")
		}
		if _, err := store.pool.Exec(ctx, `UPDATE functional_events SET event='not-json'::bytea WHERE run_id='matrix-flow-run' AND sequence=1`); err != nil {
			t.Fatal(err)
		}
		if _, err := functionalAdapter.Events(ctx, "matrix-flow-run"); err == nil {
			t.Fatal("corrupt functional event accepted")
		}
		if _, err := store.pool.Exec(ctx, `UPDATE environment_records SET record='not-json'::bytea WHERE run_id='matrix-environment'`); err != nil {
			t.Fatal(err)
		}
		if _, err := store.EnvironmentStore().Load(ctx, "matrix-environment"); err == nil {
			t.Fatal("corrupt environment record accepted")
		}
		if _, err := store.pool.Exec(ctx, `UPDATE environment_events SET event='not-json'::bytea WHERE run_id='matrix-environment'`); err != nil {
			t.Fatal(err)
		}
		if _, err := store.EnvironmentStore().Events(ctx, "matrix-environment"); err == nil {
			t.Fatal("corrupt environment event accepted")
		}
		if _, err := store.pool.Exec(ctx, `UPDATE cleanup_items SET item='not-json'::bytea WHERE cleanup_id='matrix-cleanup-item'`); err != nil {
			t.Fatal(err)
		}
		if _, err := store.CleanupStore().Scan(ctx, "", 100); err == nil {
			t.Fatal("corrupt cleanup item accepted")
		}
		if _, err := store.pool.Exec(ctx, `UPDATE cleanup_events SET event='not-json'::bytea WHERE cleanup_id='matrix-cleanup-item'`); err != nil {
			t.Fatal(err)
		}
		if _, err := store.CleanupStore().Events(ctx, "matrix-cleanup-item"); err == nil {
			t.Fatal("corrupt cleanup event accepted")
		}
		if _, err := store.pool.Exec(ctx, `UPDATE correction_records SET record='not-json'::bytea WHERE run_id='matrix-correction'`); err != nil {
			t.Fatal(err)
		}
		if _, err := store.CorrectionStore().Get(ctx, "matrix-correction"); err == nil {
			t.Fatal("corrupt correction record accepted")
		}
		if _, err := store.pool.Exec(ctx, `UPDATE correction_events SET event='not-json'::bytea WHERE run_id='matrix-pending'`); err != nil {
			t.Fatal(err)
		}
		if _, err := store.CorrectionStore().Events(ctx, "matrix-pending"); err == nil {
			t.Fatal("corrupt correction event accepted")
		}
	})

	if _, _, err := store.Load(ctx, "matrix-missing-binding"); !errors.Is(err, controlplane.ErrNotFound) {
		t.Fatal("missing binding", err)
	}
}

// TestPostgresConsumerAdaptersFailClosedOnDatabaseLoss verifies every durable boundary rejects a lost sink without inventing success.
func TestPostgresConsumerAdaptersFailClosedOnDatabaseLoss(t *testing.T) {
	ctx := context.Background()
	store := integrationStore(t)
	if err := store.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Truncate(time.Microsecond)
	store.Close()
	requireFailure := func(name string, err error) {
		t.Helper()
		if err == nil {
			t.Errorf("%s accepted an unavailable database", name)
		}
	}

	requireFailure("ready", store.Ready(ctx))
	requireFailure("migrate", store.Migrate(ctx))
	epoch, _ := controlplane.SealBackendEpoch(controlplane.BackendEpoch{RunID: "loss-run", ProjectID: "loss-project", Epoch: 1, Mode: controlplane.BackendPostgres, BackendIdentity: "pg-test", SchemaVersion: 1, CreatedAt: now})
	_, err := store.ReserveInitial(ctx, epoch)
	requireFailure("reserve binding", err)
	_, _, err = store.Load(ctx, epoch.RunID)
	requireFailure("load binding", err)
	_, err = store.Cutover(ctx, controlplane.BindingHead{RunID: epoch.RunID}, epoch)
	requireFailure("cutover", err)
	job := integrationJob(now, "loss-job", 1, integrationDigest("binding"))
	_, err = store.Enqueue(ctx, job)
	requireFailure("enqueue", err)
	_, err = store.Claim(ctx, controlplane.ClaimRequest{ProjectID: job.ProjectID, WorkerID: "loss-worker", Now: now, Lease: time.Minute, Limit: 1})
	requireFailure("claim", err)
	_, err = store.Heartbeat(ctx, job.JobID, "loss-worker", 1, 0, now, time.Minute)
	requireFailure("heartbeat", err)
	_, err = store.Complete(ctx, job.JobID, "loss-worker", 1, 0, now, controlplane.JobSucceeded, "")
	requireFailure("complete", err)
	_, err = store.Retry(ctx, job.JobID, "loss-worker", 1, 0, now, controlplane.RetryDecision{Category: "transient", Backoff: time.Second, MaxAttempts: 2}, now.Add(time.Second))
	requireFailure("retry", err)
	_, err = store.Cancel(ctx, job.JobID, 1)
	requireFailure("cancel", err)
	_, err = store.Recover(ctx, now, 1)
	requireFailure("recover", err)

	admission := integrationAdmission(now, "loss")
	_, err = store.Admit(ctx, admission)
	requireFailure("admit", err)
	_, err = store.Accept(ctx, admission.Envelope, admission.Job)
	requireFailure("accept", err)
	_, err = store.LoadIngress(ctx, admission.Epoch.RunID)
	requireFailure("load ingress", err)
	intent := controlplane.EffectIntent{OperationID: "loss-operation", IdempotencyKey: "loss-effect", ProjectID: "loss-project", RunID: epoch.RunID, Phase: controlplane.PhasePublish, BindingDigest: integrationDigest("binding"), InputDigest: integrationDigest("input"), ReconcileMethod: "poll"}
	_, err = store.Prepare(ctx, intent)
	requireFailure("prepare", err)
	requireFailure("confirm", store.Confirm(ctx, intent.OperationID, integrationDigest("receipt")))
	_, err = store.ClaimOutbox(ctx, "loss-dispatcher", now, time.Minute, 1)
	requireFailure("claim outbox", err)
	_, err = store.RecoverOutbox(ctx, now, 1)
	requireFailure("recover outbox", err)

	workflowAdapter := store.WorkflowStore()
	workflow := validationworkflow.Record{Version: 1, Handle: "loss-workflow", Principal: "loss-principal", IdempotencyKey: "loss-key", RequestDigest: integrationDigest("request"), RepositoryID: "loss-repository", Revision: strings.Repeat("d", 40), Phase: validationworkflow.PhaseAdmitted}
	_, _, err = workflowAdapter.Reserve(ctx, workflow, "loss-worker", now, time.Minute)
	requireFailure("workflow reserve", err)
	_, err = workflowAdapter.Load(ctx, workflow.Handle)
	requireFailure("workflow load", err)
	_, err = workflowAdapter.Events(ctx, workflow.Handle)
	requireFailure("workflow events", err)
	_, err = workflowAdapter.Pending(ctx, now)
	requireFailure("workflow pending", err)
	_, _, err = workflowAdapter.Claim(ctx, workflow.Handle, "loss-worker", now, time.Minute)
	requireFailure("workflow claim", err)
	requireFailure("artifact commit", store.PlanArtifactStore().Commit(ctx, validationworkflow.PlanArtifact{WorkflowID: workflow.Handle, ArtifactSHA256: integrationDigest("artifact")}))
	_, err = store.PlanArtifactStore().Load(ctx, workflow.Handle)
	requireFailure("artifact load", err)
	requireFailure("executable commit", store.ExecutablePlanStore().Commit(ctx, workflow.Handle, validationworkflow.ExecutableMaterial{WorkflowID: workflow.Handle, ArtifactSHA256: integrationDigest("artifact")}))
	_, err = store.ExecutablePlanStore().Load(ctx, workflow.Handle)
	requireFailure("executable load", err)
	requireFailure("executable delete", store.ExecutablePlanStore().Delete(ctx, workflow.Handle))

	functionalAdapter, err := store.FunctionalStore(integrationClock{now: now}, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	_, _, err = functionalAdapter.Claim(ctx, "loss-flow", 1, "step", "worker")
	requireFailure("functional claim", err)
	_, err = functionalAdapter.RenewClaim(ctx, "loss-flow", 1, "step", functionalflow.Claim{Owner: "worker", Token: "token", ExpiresAt: now.Add(time.Minute)})
	requireFailure("functional renew", err)
	requireFailure("functional intent", functionalAdapter.ConfirmIntent(ctx, functionalflow.Intent{RunID: "loss-flow", Revision: 1, StepID: "step", ClaimToken: "token"}))
	_, _, err = functionalAdapter.Intent(ctx, "loss-flow", 1, "step")
	requireFailure("functional intent load", err)
	requireFailure("functional terminal", functionalAdapter.ConfirmTerminal(ctx, functionalflow.StepRecord{RunID: "loss-flow", Revision: 1, StepID: "step"}))
	_, _, err = functionalAdapter.Terminal(ctx, "loss-flow", 1, "step")
	requireFailure("functional terminal load", err)
	_, err = functionalAdapter.AppendEvent(ctx, functionalflow.Event{RunID: "loss-flow"})
	requireFailure("functional append", err)
	_, err = functionalAdapter.Events(ctx, "loss-flow")
	requireFailure("functional events", err)

	cleanupAdapter := store.CleanupStore()
	_, err = cleanupAdapter.Scan(ctx, "", 1)
	requireFailure("cleanup scan", err)
	_, _, err = cleanupAdapter.Claim(ctx, "loss-cleanup", "worker", now, time.Minute)
	requireFailure("cleanup claim", err)
	_, err = cleanupAdapter.Events(ctx, "loss-cleanup")
	requireFailure("cleanup events", err)
	_, err = cleanupAdapter.Status(ctx, "loss-cleanup")
	requireFailure("cleanup status", err)
	_, err = cleanupAdapter.LoadCursorPass(ctx, "loss-source")
	requireFailure("cleanup cursor load", err)
	requireFailure("cleanup cursor save", cleanupAdapter.SaveCursorPass(ctx, "loss-source", cleanup.CursorPass{Epoch: 1}, cleanup.CursorPass{Epoch: 1, Value: "next"}))
	_, err = store.CleanupCapacityLedger().Receipt(ctx, integrationDigest("receipt"))
	requireFailure("capacity receipt", err)
	_, _, err = store.CleanupCapacityLedger().ScanReceipts(ctx, "", 1)
	requireFailure("capacity scan", err)

	_, err = store.EnvironmentStore().Load(ctx, "loss-environment")
	requireFailure("environment load", err)
	_, err = store.EnvironmentStore().Events(ctx, "loss-environment")
	requireFailure("environment events", err)
	_, err = store.CorrectionStore().Get(ctx, "loss-correction")
	requireFailure("correction load", err)
	_, err = store.CorrectionStore().Events(ctx, "loss-correction")
	requireFailure("correction events", err)
	requireFailure("correction recover", store.CorrectionStore().Recover(ctx, "loss-worker", now))
	_, err = store.CorrectionStore().ClaimPendingComparisons(ctx, "loss-worker", now)
	requireFailure("correction claims", err)
	manifest, sealErr := controlplane.SealImportManifest(controlplane.ImportManifest{ImportID: "loss-import", RunID: epoch.RunID, ProjectID: epoch.ProjectID, SourceIdentity: "loss-source", ExpectedHeadDigest: integrationDigest("head"), ExpectedEpoch: 1, Counts: map[string]uint64{"runs": 1}, StreamDigests: map[string]string{"runs": integrationDigest("stream")}, RelationDigests: map[string]string{"runs": integrationDigest("relation")}, State: controlplane.ImportFrozen, CreatedAt: now})
	if sealErr != nil {
		t.Fatal(sealErr)
	}
	_, err = store.SaveImport(ctx, manifest, "")
	requireFailure("import save", err)
	requireFailure("import page", store.SaveImportPage(ctx, manifest.ImportID, []controlplane.ImportDocument{{Stream: "runs", RelationKey: "run", Document: []byte(`{}`)}}))
	_, err = store.VerifyImport(ctx, manifest.ImportID)
	requireFailure("import verify", err)
	_, err = store.AbandonImport(ctx, manifest.ImportID)
	requireFailure("import abandon", err)
	_, err = store.CutoverImport(ctx, manifest.ImportID, now)
	requireFailure("import cutover", err)
	_, err = store.LoadImportCutoverReceipt(ctx, manifest.ImportID)
	requireFailure("import receipt", err)
	_, err = store.LoadImport(ctx, manifest.ImportID)
	requireFailure("import load", err)
	_, err = store.DeleteTerminalBefore(ctx, now.Unix(), 1)
	requireFailure("retention", err)
	codec, codecErr := controlplane.NewCursorCodec([]byte("0123456789abcdef0123456789abcdef"))
	if codecErr != nil {
		t.Fatal(codecErr)
	}
	_, err = store.ListJobs(ctx, controlplane.JobQuery{ProjectID: epoch.ProjectID, Limit: 1, Now: now, CursorTTL: time.Minute}, codec)
	requireFailure("job query", err)
	grant, _ := controlplane.SealAuthorityGrant(controlplane.AuthorityGrant{ProjectID: epoch.ProjectID, RunID: epoch.RunID, BackendEpoch: epoch.Epoch, BindingDigest: epoch.Digest, Phase: controlplane.PhaseExecute, AllowedCapabilities: []string{"http.execute"}, TargetBindingRef: "target", TargetBindingDigest: integrationDigest("target")})
	requireFailure("authority grant save", store.SaveAuthorityGrant(ctx, grant))
	_, err = store.LoadAuthorityGrant(ctx, epoch.RunID, controlplane.PhaseExecute)
	requireFailure("authority grant load", err)
	_, err = store.CommitPackage(ctx, validationworkflow.PackageDocument{ProjectID: "loss-project", PackageID: "loss-package", Document: []byte(`{}`)})
	requireFailure("package commit", err)
	_, err = store.LoadPackage(ctx, "loss-project", "loss-package")
	requireFailure("package load", err)
}
