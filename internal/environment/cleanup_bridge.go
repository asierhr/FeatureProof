package environment

import (
	"context"
	"errors"
	"os"
	"sort"
	"time"

	environmentcleanup "github.com/asierhr/featureProof/internal/environment/cleanup"
)

// CleanupBridge projects private lifecycle records into sealed cleanup authority and delegates effects to the lifecycle service.
type CleanupBridge struct {
	Store   *LocalStore
	Service *LifecycleService
	Owner   string
}

// ResolveCleanupAuthority resolves one workflow intent only from the exact private lifecycle record.
func (b *CleanupBridge) ResolveCleanupAuthority(ctx context.Context, runID, cleanupID string) (environmentcleanup.Item, error) {
	if b == nil || b.Store == nil || b.Service == nil || !safeID.MatchString(b.Owner) || !safeID.MatchString(cleanupID) {
		return environmentcleanup.Item{}, &Error{Category: CategoryInvalid, Operation: "cleanup_bridge"}
	}
	record, err := b.Store.Load(ctx, runID)
	if err != nil {
		return environmentcleanup.Item{}, err
	}
	return b.item(record, cleanupID, "workflow")
}

// ScanLifecycleCleanup returns stable lifecycle candidates after the supplied run cursor.
func (b *CleanupBridge) ScanLifecycleCleanup(ctx context.Context, cursor string, limit int) ([]environmentcleanup.LifecycleIndexRecord, string, error) {
	if b == nil || b.Store == nil || limit <= 0 {
		return nil, "", &Error{Category: CategoryInvalid, Operation: "cleanup_scan"}
	}
	entries, err := os.ReadDir(b.Store.root + string(os.PathSeparator) + "records")
	if err != nil {
		return nil, "", err
	}
	ids := make([]string, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() && safeID.MatchString(entry.Name()) && entry.Name() > cursor {
			ids = append(ids, entry.Name())
		}
	}
	sort.Strings(ids)
	next := ""
	if len(ids) > limit {
		next, ids = ids[limit-1], ids[:limit]
	}
	result := make([]environmentcleanup.LifecycleIndexRecord, 0, len(ids))
	for _, id := range ids {
		record, loadErr := b.Store.Load(ctx, id)
		if loadErr != nil {
			return nil, "", loadErr
		}
		if record.Selection.Kind != KindManaged || record.LifecycleHandle == "" {
			continue
		}
		item, itemErr := b.item(record, "lifecycle-"+stableKey("cleanup", id)[:24], "lifecycle")
		if itemErr != nil {
			return nil, "", itemErr
		}
		result = append(result, environmentcleanup.LifecycleIndexRecord{Item: item, Released: record.State == StateReleased && record.ReleaseConfirmed, LeaseUntil: record.LeaseUntil})
	}
	return result, next, nil
}

// LoadCleanupBinding finds only a byte-equivalent sealed binding in the private lifecycle store.
func (b *CleanupBridge) LoadCleanupBinding(ctx context.Context, bindingDigest string) (environmentcleanup.KnownLifecycleRecord, error) {
	record, binding, err := b.findBinding(ctx, bindingDigest)
	if err != nil {
		return environmentcleanup.KnownLifecycleRecord{}, err
	}
	status := environmentcleanup.LifecyclePresent
	if record.State == StateReleased && record.ReleaseConfirmed {
		status = environmentcleanup.LifecycleAbsent
	}
	return environmentcleanup.KnownLifecycleRecord{Binding: binding, Status: status}, nil
}

// ReconcileCleanupBinding rechecks exact private authority without performing a destructive fallback.
func (b *CleanupBridge) ReconcileCleanupBinding(ctx context.Context, expected environmentcleanup.LifecycleCleanupBinding) (environmentcleanup.KnownLifecycleRecord, error) {
	loaded, err := b.LoadCleanupBinding(ctx, expected.Digest)
	if err != nil || loaded.Binding.Digest != expected.Digest {
		return environmentcleanup.KnownLifecycleRecord{}, &Error{Category: CategoryConflict, Operation: "cleanup_binding"}
	}
	return loaded, nil
}

// ReleaseCleanupBinding invokes the existing lifecycle release only after exact binding lookup.
func (b *CleanupBridge) ReleaseCleanupBinding(ctx context.Context, expected environmentcleanup.LifecycleCleanupBinding) error {
	loaded, err := b.LoadCleanupBinding(ctx, expected.Digest)
	if err != nil || loaded.Binding.Digest != expected.Digest {
		return &Error{Category: CategoryConflict, Operation: "cleanup_release_binding"}
	}
	return b.Service.Release(ctx, expected.RunID)
}

// findBinding scans private records and returns only an exact derived binding digest.
func (b *CleanupBridge) findBinding(ctx context.Context, digest string) (Record, environmentcleanup.LifecycleCleanupBinding, error) {
	if b == nil || b.Store == nil || !digestPattern.MatchString(digest) {
		return Record{}, environmentcleanup.LifecycleCleanupBinding{}, &Error{Category: CategoryInvalid, Operation: "cleanup_binding"}
	}
	entries, err := os.ReadDir(b.Store.root + string(os.PathSeparator) + "records")
	if err != nil {
		return Record{}, environmentcleanup.LifecycleCleanupBinding{}, err
	}
	for _, entry := range entries {
		if !entry.IsDir() || !safeID.MatchString(entry.Name()) {
			continue
		}
		record, loadErr := b.Store.Load(ctx, entry.Name())
		if loadErr != nil {
			return Record{}, environmentcleanup.LifecycleCleanupBinding{}, loadErr
		}
		binding, bindingErr := b.binding(record)
		if bindingErr == nil && binding.Digest == digest {
			return record, binding, nil
		}
	}
	return Record{}, environmentcleanup.LifecycleCleanupBinding{}, os.ErrNotExist
}

// LifecycleAbsent reports authoritative local absence only when no exact lifecycle binding exists.
func (b *CleanupBridge) LifecycleAbsent(ctx context.Context, binding environmentcleanup.LifecycleCleanupBinding) (bool, error) {
	loaded, err := b.LoadCleanupBinding(ctx, binding.Digest)
	if errors.Is(err, os.ErrNotExist) {
		return true, nil
	}
	if err != nil {
		return false, err
	}
	return loaded.Status == environmentcleanup.LifecycleAbsent, nil
}

// HasLifecycleReservation reports whether the exact pre-provider lifecycle handoff already exists.
func (b *CleanupBridge) HasLifecycleReservation(ctx context.Context, project, runID, acquisition string) (bool, error) {
	if b == nil || b.Store == nil || !safeID.MatchString(project) || !safeID.MatchString(runID) || !digestPattern.MatchString(acquisition) {
		return false, &Error{Category: CategoryInvalid, Operation: "cleanup_capacity_lookup"}
	}
	record, err := b.Store.Load(ctx, runID)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if record.ProjectID != project || record.AcquisitionKey != acquisition {
		return false, &Error{Category: CategoryConflict, Operation: "cleanup_capacity_lookup"}
	}
	return true, nil
}

// item seals one cleanup scheduling projection without exposing the lifecycle handle.
func (b *CleanupBridge) item(record Record, cleanupID, source string) (environmentcleanup.Item, error) {
	binding, err := b.binding(record)
	if err != nil {
		return environmentcleanup.Item{}, err
	}
	state := environmentcleanup.StateIntentCommitted
	if source == "lifecycle" {
		state = environmentcleanup.StateDiscovered
	}
	return environmentcleanup.SealItem(environmentcleanup.Item{Version: environmentcleanup.Version, CleanupID: cleanupID, Source: source, OwnerDigest: stableKey("cleanup-owner", b.Owner), ProjectID: record.ProjectID, RunID: record.RunID, Binding: binding, CreatedAt: record.CreatedAt.UTC(), ExpiresAt: record.ExpiresAt.UTC(), TTL: record.TTL, Demand: environmentcleanup.ResourceDemand{Environments: record.Demand.Environments, CPUMilli: record.Demand.CPUMilli, MemoryBytes: record.Demand.MemoryBytes, ObjectCount: record.Demand.ObjectCount}, State: state, VersionNumber: 1})
}

// binding derives complete destructive authority exclusively from a verified private lifecycle record.
func (b *CleanupBridge) binding(record Record) (environmentcleanup.LifecycleCleanupBinding, error) {
	if validateRecord(record, record.RunID, record.Revision) != nil || record.Selection.Kind != KindManaged || record.LifecycleHandle == "" || record.ReleaseKey == "" || !digestPattern.MatchString(record.CapacityEnvironmentDigest) || !digestPattern.MatchString(record.CapacityResourceDigest) {
		return environmentcleanup.LifecycleCleanupBinding{}, &Error{Category: CategoryIntegrity, Operation: "cleanup_binding"}
	}
	return environmentcleanup.SealLifecycleCleanupBinding(environmentcleanup.LifecycleCleanupBinding{Version: environmentcleanup.BindingVersion, ProjectID: record.ProjectID, RunID: record.RunID, ProviderID: record.Selection.ProviderID, ProviderVersion: record.Selection.ProviderVersion, SelectionDigest: record.Selection.Digest, AcquisitionKey: record.AcquisitionKey, AcquisitionDigest: record.RequestDigest, EnvironmentDigest: record.CapacityEnvironmentDigest, ResourceIdentityDigest: record.CapacityResourceDigest, LifecycleDigest: record.LifecycleDigest, CleanupKey: record.ReleaseKey, CapacityReceiptDigest: record.CapacityReceipt.Digest, LifecycleRecordDigest: cleanupLifecycleRecordDigest(record)})
}

// cleanupLifecycleRecordDigest seals immutable lifecycle authority while allowing state, lease, retry, and revision transitions during cleanup.
func cleanupLifecycleRecordDigest(record Record) string {
	return digestOf(struct {
		ProjectID, RunID, CommitOID, AcquisitionKey, AcquisitionDigest string
		Selection                                                      Selection
		CreatedAt, ExpiresAt                                           time.Time
		TTL                                                            time.Duration
		Demand                                                         ResourceDemand
		EnvironmentDigest, ResourceIdentityDigest, LifecycleDigest     string
		CleanupKey, CapacityReceiptID, CapacityReceiptDigest           string
	}{record.ProjectID, record.RunID, record.CommitOID, record.AcquisitionKey, record.RequestDigest, record.Selection, record.CreatedAt.UTC(), record.ExpiresAt.UTC(), record.TTL, record.Demand, record.CapacityEnvironmentDigest, record.CapacityResourceDigest, record.LifecycleDigest, record.ReleaseKey, record.CapacityReceipt.ID, record.CapacityReceipt.Digest})
}
