package cleanup

import (
	"context"
	"time"
)

// LifecycleIndexRecord carries a complete cleanup candidate with private lifecycle lease classification.
type LifecycleIndexRecord struct {
	Item       Item
	Released   bool
	LeaseUntil time.Time
}

// LifecycleIndexStore scans lifecycle projections without exposing provider handles.
type LifecycleIndexStore interface {
	ScanLifecycleCleanup(context.Context, string, int) ([]LifecycleIndexRecord, string, error)
}

// LifecycleIndexSource excludes released records and live lifecycle owners before cleanup reservation.
type LifecycleIndexSource struct {
	store LifecycleIndexStore
	clock Clock
}

// NewLifecycleIndexSource requires a durable lifecycle index and the controller clock.
func NewLifecycleIndexSource(store LifecycleIndexStore, clock Clock) (*LifecycleIndexSource, error) {
	if store == nil || clock == nil {
		return nil, &Error{Category: CategoryInvalid, Operation: "lifecycle_discovery"}
	}
	return &LifecycleIndexSource{store: store, clock: clock}, nil
}

// ScanCleanupCandidates hands off only records without a live lifecycle lease at the inclusive boundary.
func (s *LifecycleIndexSource) ScanCleanupCandidates(ctx context.Context, cursor string, limit int) (Page, error) {
	records, next, err := s.store.ScanLifecycleCleanup(ctx, cursor, limit)
	if err != nil {
		return Page{}, err
	}
	now := s.clock.Now().UTC()
	page := Page{Next: next}
	for _, record := range records {
		if record.Released || record.LeaseUntil.After(now) {
			continue
		}
		if VerifyItem(record.Item) != nil || record.Item.Source != "lifecycle" {
			return Page{}, &Error{Category: CategoryIntegrity, Operation: "lifecycle_discovery"}
		}
		page.Items = append(page.Items, record.Item)
	}
	return page, nil
}

// CleanupCandidateStore is the read-only private index shared by lifecycle and capacity discovery adapters.
type CleanupCandidateStore interface {
	ScanCleanupCandidates(context.Context, string, int) (Page, error)
}

// IndexedDiscoverySource validates complete items returned by one trusted durable index.
type IndexedDiscoverySource struct {
	store  CleanupCandidateStore
	source string
}

// NewIndexedDiscoverySource binds a lifecycle or capacity index to its required cleanup source.
func NewIndexedDiscoverySource(source string, store CleanupCandidateStore) (*IndexedDiscoverySource, error) {
	if store == nil || source != "lifecycle" && source != "workflow" {
		return nil, &Error{Category: CategoryInvalid, Operation: "indexed_discovery"}
	}
	return &IndexedDiscoverySource{store: store, source: source}, nil
}

// ScanCleanupCandidates rejects partial or wrongly classified records before controller persistence.
func (s *IndexedDiscoverySource) ScanCleanupCandidates(ctx context.Context, cursor string, limit int) (Page, error) {
	page, err := s.store.ScanCleanupCandidates(ctx, cursor, limit)
	if err != nil {
		return Page{}, err
	}
	for _, item := range page.Items {
		if VerifyItem(item) != nil || item.Source != s.source {
			return Page{}, &Error{Category: CategoryIntegrity, Operation: "indexed_discovery"}
		}
	}
	return page, nil
}

// KnownLifecycleRecord is the private authority projection loaded by the lifecycle adapter.
type KnownLifecycleRecord struct {
	Binding LifecycleCleanupBinding
	Status  LifecycleStatus
}

// KnownLifecyclePort supplies exact records and effects without granting lookup-by-run authority.
type KnownLifecyclePort interface {
	LoadCleanupBinding(context.Context, string) (KnownLifecycleRecord, error)
	ReconcileCleanupBinding(context.Context, LifecycleCleanupBinding) (KnownLifecycleRecord, error)
	ReleaseCleanupBinding(context.Context, LifecycleCleanupBinding) error
}

// UncertainLifecyclePort reconciles acquisition effects without accepting destructive release authority.
type UncertainLifecyclePort interface {
	ReconcileUncertainAcquisition(context.Context, UncertainAcquisitionBinding) (KnownLifecycleRecord, error)
}

// LifecycleAdapter verifies every complete binding before delegating to the existing lifecycle.
type LifecycleAdapter struct{ port KnownLifecyclePort }

// NewLifecycleAdapter requires an explicit private lifecycle authority port.
func NewLifecycleAdapter(port KnownLifecyclePort) (*LifecycleAdapter, error) {
	if port == nil {
		return nil, &Error{Category: CategoryInvalid, Operation: "lifecycle_adapter"}
	}
	return &LifecycleAdapter{port: port}, nil
}

// Reconcile validates the supplied binding and never completes it from RunID alone.
func (a *LifecycleAdapter) Reconcile(ctx context.Context, binding LifecycleCleanupBinding) (LifecycleStatus, LifecycleCleanupBinding, error) {
	if VerifyLifecycleCleanupBinding(binding) != nil {
		return LifecycleUncertain, LifecycleCleanupBinding{}, &Error{Category: CategoryUnauthorized, Operation: "lifecycle_reconcile"}
	}
	record, err := a.port.ReconcileCleanupBinding(ctx, binding)
	if err != nil {
		return LifecycleUncertain, LifecycleCleanupBinding{}, err
	}
	if VerifyLifecycleCleanupBinding(record.Binding) != nil || !sameBindingAuthority(binding, record.Binding) {
		return LifecycleUncertain, LifecycleCleanupBinding{}, &Error{Category: CategoryIntegrity, Operation: "lifecycle_reconcile"}
	}
	return record.Status, record.Binding, nil
}

// ReconcileUncertain delegates only a sealed reconciliation binding and verifies the returned known authority.
func (a *LifecycleAdapter) ReconcileUncertain(ctx context.Context, uncertain UncertainAcquisitionBinding) (LifecycleStatus, LifecycleCleanupBinding, error) {
	if VerifyUncertainAcquisitionBinding(uncertain) != nil {
		return LifecycleUncertain, LifecycleCleanupBinding{}, &Error{Category: CategoryUnauthorized, Operation: "lifecycle_reconcile_uncertain"}
	}
	port, ok := a.port.(UncertainLifecyclePort)
	if !ok {
		return LifecycleUncertain, LifecycleCleanupBinding{}, &Error{Category: CategoryUnauthorized, Operation: "lifecycle_reconcile_uncertain"}
	}
	record, err := port.ReconcileUncertainAcquisition(ctx, uncertain)
	if err != nil {
		return LifecycleUncertain, LifecycleCleanupBinding{}, err
	}
	if VerifyLifecycleCleanupBinding(record.Binding) != nil || !bindingMatchesUncertain(record.Binding, uncertain) {
		return LifecycleUncertain, LifecycleCleanupBinding{}, &Error{Category: CategoryIntegrity, Operation: "lifecycle_reconcile_uncertain"}
	}
	return record.Status, record.Binding, nil
}

// Release compares the stored record before invoking the destructive port.
func (a *LifecycleAdapter) Release(ctx context.Context, binding LifecycleCleanupBinding) error {
	if VerifyLifecycleCleanupBinding(binding) != nil {
		return &Error{Category: CategoryUnauthorized, Operation: "lifecycle_release"}
	}
	record, err := a.port.LoadCleanupBinding(ctx, binding.Digest)
	if err != nil {
		return err
	}
	if VerifyLifecycleCleanupBinding(record.Binding) != nil || !sameBindingAuthority(binding, record.Binding) {
		return &Error{Category: CategoryIntegrity, Operation: "lifecycle_release"}
	}
	return a.port.ReleaseCleanupBinding(ctx, binding)
}

// Status returns state only after exact private authority comparison.
func (a *LifecycleAdapter) Status(ctx context.Context, binding LifecycleCleanupBinding) (LifecycleStatus, error) {
	if VerifyLifecycleCleanupBinding(binding) != nil {
		return LifecycleUncertain, &Error{Category: CategoryUnauthorized, Operation: "lifecycle_status"}
	}
	record, err := a.port.LoadCleanupBinding(ctx, binding.Digest)
	if err != nil {
		return LifecycleUncertain, err
	}
	if VerifyLifecycleCleanupBinding(record.Binding) != nil || !sameBindingAuthority(binding, record.Binding) {
		return LifecycleUncertain, &Error{Category: CategoryIntegrity, Operation: "lifecycle_status"}
	}
	return record.Status, nil
}

// sameBindingAuthority requires byte-equivalent sealed destructive authority.
func sameBindingAuthority(left, right LifecycleCleanupBinding) bool {
	return left.Digest == right.Digest && left.ProjectID == right.ProjectID && left.RunID == right.RunID && left.ResourceIdentityDigest == right.ResourceIdentityDigest && left.CleanupKey == right.CleanupKey
}

// WorkflowIntentRecord is the read-only terminal projection allowed into cleanup discovery.
type WorkflowIntentRecord struct {
	Item                          Item
	OutcomeDigest, EvidenceDigest string
}

// WorkflowIntentStore scans immutable intent projections without workflow write access.
type WorkflowIntentStore interface {
	ScanCleanupRecords(context.Context, string, int) ([]WorkflowIntentRecord, string, error)
}

// WorkflowIntentSource adapts terminal workflow records into cleanup-only pages.
type WorkflowIntentSource struct{ store WorkflowIntentStore }

// NewWorkflowIntentSource rejects missing read-only workflow storage.
func NewWorkflowIntentSource(store WorkflowIntentStore) (*WorkflowIntentSource, error) {
	if store == nil {
		return nil, &Error{Category: CategoryInvalid, Operation: "workflow_intents"}
	}
	return &WorkflowIntentSource{store: store}, nil
}

// ScanCleanupIntents copies only cleanup items and never exposes package or evidence payloads.
func (s *WorkflowIntentSource) ScanCleanupIntents(ctx context.Context, cursor string, limit int) (Page, error) {
	records, next, err := s.store.ScanCleanupRecords(ctx, cursor, limit)
	if err != nil {
		return Page{}, err
	}
	items := make([]Item, len(records))
	for index, record := range records {
		if VerifyItem(record.Item) != nil {
			return Page{}, &Error{Category: CategoryIntegrity, Operation: "workflow_intent"}
		}
		items[index] = cloneItem(record.Item)
	}
	return Page{Items: items, Next: next, Scanned: true, Complete: next == ""}, nil
}
