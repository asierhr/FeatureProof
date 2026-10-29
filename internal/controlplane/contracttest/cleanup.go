package contracttest

import (
	"context"
	"fmt"
	"time"

	cleanup "github.com/asierhr/featureProof/internal/environment/cleanup"
)

// CleanupStore combines scheduling and cursor ports that must reopen with the
// same lease, fencing, event and continuation semantics.
type CleanupStore interface {
	cleanup.Store
	cleanup.CursorStore
	cleanup.PassCursorStore
}

// CleanupFactory reopens one durable cleanup partition.
type CleanupFactory func() (CleanupStore, error)

// RunCleanupStore exercises reserve, reopen, claim, renew, fenced save,
// release and cursor CAS through only consumer-owned interfaces.
func RunCleanupStore(ctx context.Context, factory CleanupFactory, runID, projectID string, now time.Time) error {
	if factory == nil || runID == "" || projectID == "" || now.IsZero() {
		return fmt.Errorf("cleanup contract configuration")
	}
	store, err := factory()
	if err != nil {
		return fmt.Errorf("open initial: %w", err)
	}
	binding, err := cleanup.SealLifecycleCleanupBinding(cleanup.LifecycleCleanupBinding{Version: cleanup.BindingVersion, ProjectID: projectID, RunID: runID, ProviderID: "provider", ProviderVersion: "v1", SelectionDigest: contractDigest("selection"), AcquisitionKey: "contract-acquisition", AcquisitionDigest: contractDigest("acquisition"), EnvironmentDigest: contractDigest("environment"), ResourceIdentityDigest: contractDigest("resource"), LifecycleDigest: contractDigest("lifecycle"), CleanupKey: "contract-cleanup-key", CapacityReceiptDigest: contractDigest("capacity"), LifecycleRecordDigest: contractDigest("record")})
	if err != nil {
		return fmt.Errorf("binding: %w", err)
	}
	item, err := cleanup.SealItem(cleanup.Item{Version: cleanup.Version, CleanupID: runID + "-cleanup", Source: "lifecycle", OwnerDigest: contractDigest("owner"), ProjectID: projectID, RunID: runID, Binding: binding, CreatedAt: now, ExpiresAt: now.Add(time.Hour), TTL: time.Hour, Demand: cleanup.ResourceDemand{Environments: 1, CPUMilli: 1, MemoryBytes: 1, ObjectCount: 1}, State: cleanup.StatePending, VersionNumber: 1})
	if err != nil {
		return fmt.Errorf("item: %w", err)
	}
	item, created, err := store.Reserve(ctx, item, cleanup.Event{ID: item.CleanupID + "-reserved", CleanupID: item.CleanupID, ProjectID: projectID, Kind: "cleanup_reserved", At: now})
	if err != nil || !created {
		return fmt.Errorf("reserve created=%v: %w", created, err)
	}
	reopened, err := factory()
	if err != nil {
		return fmt.Errorf("reopen: %w", err)
	}
	item, claimed, err := reopened.Claim(ctx, item.CleanupID, "contract-worker", now, time.Minute)
	if err != nil || !claimed || item.FencingVersion == 0 {
		return fmt.Errorf("claim claimed=%v fence=%d: %w", claimed, item.FencingVersion, err)
	}
	item, err = reopened.Renew(ctx, item, item.LeaseOwner, item.FencingVersion, now.Add(time.Second), time.Minute)
	if err != nil {
		return fmt.Errorf("renew: %w", err)
	}
	item.Attempts++
	item, err = reopened.SaveOwned(ctx, item, item.LeaseOwner, item.FencingVersion, cleanup.Event{ID: item.CleanupID + "-progress", CleanupID: item.CleanupID, ProjectID: projectID, Owner: item.LeaseOwner, Kind: "cleanup_progress", At: now.Add(2 * time.Second)})
	if err != nil {
		return fmt.Errorf("save owned: %w", err)
	}
	item, err = reopened.ReleaseOwner(ctx, item, item.LeaseOwner, item.FencingVersion, cleanup.Event{ID: item.CleanupID + "-released", CleanupID: item.CleanupID, ProjectID: projectID, Kind: "cleanup_owner_released", At: now.Add(3 * time.Second)})
	if err != nil || item.LeaseOwner != "" {
		return fmt.Errorf("release owner=%q: %w", item.LeaseOwner, err)
	}
	events, err := reopened.Events(ctx, item.CleanupID)
	if err != nil || len(events) != 4 {
		return fmt.Errorf("events count=%d: %w", len(events), err)
	}
	source := runID + "-source"
	if err = reopened.SaveCursor(ctx, source, "", "first"); err != nil {
		return fmt.Errorf("cursor genesis: %w", err)
	}
	if err = reopened.SaveCursor(ctx, source, "first", "second"); err != nil {
		return fmt.Errorf("cursor advance: %w", err)
	}
	if err = reopened.SaveCursor(ctx, source, "first", "stale"); err == nil {
		return fmt.Errorf("stale cursor accepted")
	}
	return nil
}
