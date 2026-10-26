package cleanup

import (
	"context"
	"encoding/json"
	"sort"
	"sync"
	"time"
)

// CapacityRequest contains trusted demand resolved before provider acquisition.
type CapacityRequest struct {
	ProjectID, RunID, AcquisitionKey, SelectionDigest string
	Demand                                            ResourceDemand
	TTL                                               time.Duration
	Policy                                            CapacityPolicy
}

// CapacityReceipt accounts for capacity until one exact absence proof is bound.
type CapacityReceipt struct {
	ID, ProjectID, RunID, AcquisitionKey, SelectionDigest, PolicyDigest string
	Demand                                                              ResourceDemand
	TTL                                                                 time.Duration
	EnvironmentDigest, ResourceIdentityDigest, AuthorityDigest          string
	ReleaseAuthorityDigest, ConfirmationDigest                          string
	Released, Unknown, LifecycleBound                                   bool
	Digest                                                              string
}

// ScanReceipts returns a bounded stable page from the durable capacity index.
func (l *CapacityLedger) ScanReceipts(ctx context.Context, cursor string, limit int) ([]CapacityReceipt, string, error) {
	if limit <= 0 {
		return nil, "", &Error{Category: CategoryInvalid, Operation: "capacity_scan"}
	}
	if err := l.refresh(ctx); err != nil {
		return nil, "", err
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	ids := make([]string, 0, len(l.receipts))
	for id := range l.receipts {
		if id > cursor {
			ids = append(ids, id)
		}
	}
	sort.Strings(ids)
	next := ""
	if len(ids) > limit {
		next, ids = ids[limit-1], ids[:limit]
	}
	receipts := make([]CapacityReceipt, len(ids))
	for index, id := range ids {
		receipts[index] = l.receipts[id]
	}
	return receipts, next, nil
}

// BindLifecycle seals the environment-record handoff before the lifecycle service may perform provider I/O.
func (l *CapacityLedger) BindLifecycle(ctx context.Context, receipt CapacityReceipt) (CapacityReceipt, error) {
	result, err := l.update(ctx, func(snapshot *capacityLedgerSnapshot) (any, bool, error) {
		current, ok := snapshot.Receipts[receipt.ID]
		if !ok || current.Digest != receipt.Digest || current.Released {
			return CapacityReceipt{}, false, &Error{Category: CategoryConflict, Operation: "capacity_bind_lifecycle"}
		}
		if current.LifecycleBound {
			return current, false, nil
		}
		current.Digest, current.LifecycleBound = "", true
		current.Digest = digestOf(current)
		snapshot.Receipts[current.ID] = current
		eventErr := appendCapacityEvent(snapshot, CapacityEvent{ID: "lifecycle-" + current.ID[:16], ProjectID: current.ProjectID, ReceiptID: current.ID, Owner: current.RunID, Kind: "capacity_lifecycle_bound"})
		return current, true, eventErr
	})
	if err != nil {
		return CapacityReceipt{}, err
	}
	return result.(CapacityReceipt), nil
}

// ReleaseUnbound frees only a receipt proven never to have crossed the durable lifecycle boundary.
func (l *CapacityLedger) ReleaseUnbound(ctx context.Context, receipt CapacityReceipt, resolutionDigest string) (CapacityReceipt, bool, error) {
	if !digestPattern.MatchString(resolutionDigest) || resolutionDigest != ReceiptOnlyResolutionDigest(receipt) {
		return CapacityReceipt{}, false, &Error{Category: CategoryInvalid, Operation: "capacity_release_unbound"}
	}
	result, err := l.update(ctx, func(snapshot *capacityLedgerSnapshot) (any, bool, error) {
		current, ok := snapshot.Receipts[receipt.ID]
		if !ok || current.Digest != receipt.Digest || current.LifecycleBound || current.EnvironmentDigest != "" {
			return ledgerResult{}, false, &Error{Category: CategoryConflict, Operation: "capacity_release_unbound"}
		}
		if current.Released {
			return ledgerResult{Receipt: current}, false, nil
		}
		current.ReleaseAuthorityDigest, current.ConfirmationDigest = current.Digest, resolutionDigest
		current.Digest, current.Released = "", true
		current.Digest = digestOf(current)
		snapshot.Receipts[current.ID] = current
		eventErr := appendCapacityEvent(snapshot, CapacityEvent{ID: "unbound-" + current.ID[:16], ProjectID: current.ProjectID, ReceiptID: current.ID, Owner: current.RunID, Kind: "capacity_unbound_released"})
		return ledgerResult{Receipt: current, Changed: true}, true, eventErr
	})
	if err != nil {
		return CapacityReceipt{}, false, err
	}
	value := result.(ledgerResult)
	return value.Receipt, value.Changed, nil
}

// ReceiptOnlyResolutionDigest proves the exact unbound receipt inspected by lifecycle absence resolution.
func ReceiptOnlyResolutionDigest(receipt CapacityReceipt) string {
	return digestOf(struct{ ID, Project, Run, Acquisition, Receipt string }{receipt.ID, receipt.ProjectID, receipt.RunID, receipt.AcquisitionKey, receipt.Digest})
}

// CapacitySnapshot exposes safe aggregate usage for admission and status.
type CapacitySnapshot struct {
	ProjectID string
	Reserved  ResourceDemand
	Unknown   bool
}

// CapacityEvent is the sealed content-free audit record for one quota decision.
type CapacityEvent struct {
	ID, ProjectID, ReceiptID, Owner, Kind string
	Attempt                               uint64
	Category                              Category
	At                                    time.Time
	Digest                                string
}

// capacityLedgerSnapshot is the complete reopenable receipt and audit state.
type capacityLedgerSnapshot struct {
	Receipts map[string]CapacityReceipt `json:"receipts"`
	Events   map[string][]CapacityEvent `json:"events"`
}

// CapacityLedger publishes immutable quota versions behind one process-safe durable head.
type CapacityLedger struct {
	mu       sync.Mutex
	receipts map[string]CapacityReceipt
	events   map[string][]CapacityEvent
	path     string
	version  uint64
	fault    func(string) error
}

// NewCapacityLedger constructs an empty explicit local ledger.
func NewCapacityLedger() *CapacityLedger {
	return &CapacityLedger{receipts: map[string]CapacityReceipt{}, events: map[string][]CapacityEvent{}}
}

// OpenCapacityLedger reopens receipts and verifies every sealed audit payload.
func OpenCapacityLedger(path string) (*CapacityLedger, error) {
	if path == "" {
		return nil, &Error{Category: CategoryInvalid, Operation: "capacity_path"}
	}
	l := NewCapacityLedger()
	l.path = path
	if err := ensureDurableParent(path); err != nil {
		return nil, &Error{Category: CategoryIntegrity, Operation: "capacity_directory", Cause: err}
	}
	release, err := acquireCleanupStoreLock(context.Background(), path+".lock")
	if err != nil {
		return nil, &Error{Category: CategoryIntegrity, Operation: "capacity_lock", Cause: err}
	}
	defer release()
	state, err := loadDurableSnapshot(path, "capacity-ledger")
	if err != nil {
		return nil, &Error{Category: CategoryIntegrity, Operation: "capacity_open", Cause: err}
	}
	if len(state.Payload) == 0 {
		return l, nil
	}
	s, err := decodeCapacitySnapshot(state.Payload)
	if err != nil {
		return nil, err
	}
	l.receipts, l.events, l.version = s.Receipts, s.Events, state.Version
	return l, nil
}

// decodeCapacitySnapshot validates every receipt and event loaded through a durable head.
func decodeCapacitySnapshot(raw []byte) (capacityLedgerSnapshot, error) {
	s := capacityLedgerSnapshot{}
	if json.Unmarshal(raw, &s) != nil || s.Receipts == nil || s.Events == nil {
		return capacityLedgerSnapshot{}, &Error{Category: CategoryIntegrity, Operation: "capacity_open"}
	}
	for id, r := range s.Receipts {
		c := r
		c.Digest = ""
		if id != r.ID || digestOf(c) != r.Digest {
			return capacityLedgerSnapshot{}, &Error{Category: CategoryIntegrity, Operation: "capacity_receipt"}
		}
	}
	for project, events := range s.Events {
		seen := map[string]struct{}{}
		for _, e := range events {
			c := e
			c.Digest = ""
			_, duplicate := seen[e.ID]
			if duplicate || e.ProjectID != project || !validCapacityEvent(e) || e.Digest != digestOf(c) {
				return capacityLedgerSnapshot{}, &Error{Category: CategoryIntegrity, Operation: "capacity_event"}
			}
			seen[e.ID] = struct{}{}
		}
	}
	return s, nil
}

// ledgerMutation prepares an immutable candidate and reports whether publication is required.
type ledgerMutation func(*capacityLedgerSnapshot) (any, bool, error)

// update retries optimistic publication without exposing tentative state.
func (l *CapacityLedger) update(ctx context.Context, mutate ledgerMutation) (any, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if l.path == "" {
		l.mu.Lock()
		candidate := cloneCapacitySnapshot(l.receipts, l.events)
		result, changed, err := mutate(&candidate)
		if err != nil || !changed {
			l.mu.Unlock()
			return result, err
		}
		l.receipts, l.events, l.version = candidate.Receipts, candidate.Events, l.version+1
		l.mu.Unlock()
		return result, nil
	}
	if l.fault != nil {
		if err := l.fault("before_write"); err != nil {
			return nil, &Error{Category: CategoryIntegrity, Operation: "capacity_fault", Cause: err}
		}
	}
	release, err := acquireCleanupStoreLock(ctx, l.path+".lock")
	if err != nil {
		return nil, &Error{Category: CategoryIntegrity, Operation: "capacity_lock", Cause: err}
	}
	defer release()
	state, err := loadDurableSnapshot(l.path, "capacity-ledger")
	if err != nil {
		return nil, &Error{Category: CategoryIntegrity, Operation: "capacity_load", Cause: err}
	}
	current := capacityLedgerSnapshot{Receipts: map[string]CapacityReceipt{}, Events: map[string][]CapacityEvent{}}
	if len(state.Payload) > 0 {
		current, err = decodeCapacitySnapshot(state.Payload)
		if err != nil {
			return nil, err
		}
	}
	candidate := cloneCapacitySnapshot(current.Receipts, current.Events)
	result, changed, err := mutate(&candidate)
	if err != nil || !changed {
		l.replaceMemory(current, state.Version)
		return result, err
	}
	if err = l.mutationFault(); err != nil {
		return nil, &Error{Category: CategoryIntegrity, Operation: "capacity_fault", Cause: err}
	}
	raw, err := json.Marshal(candidate)
	if err != nil {
		return nil, &Error{Category: CategoryIntegrity, Operation: "capacity_encode", Cause: err}
	}
	err = publishDurableSnapshot(ctx, l.path, "capacity-ledger", state, raw, l.durableFault)
	if err != nil {
		if committed, loadErr := loadDurableSnapshot(l.path, "capacity-ledger"); loadErr == nil && committed.Version > state.Version {
			if committedData, decodeErr := decodeCapacitySnapshot(committed.Payload); decodeErr == nil {
				l.replaceMemory(committedData, committed.Version)
			}
		}
		return nil, &Error{Category: CategoryIntegrity, Operation: "capacity_publish", Cause: err}
	}
	l.replaceMemory(candidate, state.Version+1)
	return result, nil
}

// mutationFault exposes receipt event/index boundaries before the immutable ledger version is published.
func (l *CapacityLedger) mutationFault() error {
	if l.fault == nil {
		return nil
	}
	for _, boundary := range []string{"before_event", "after_event", "before_index", "after_index"} {
		if err := l.fault(boundary); err != nil {
			return err
		}
	}
	return nil
}

// durableFault preserves the legacy before_publish boundary while pre-lock before_write remains independently testable.
func (l *CapacityLedger) durableFault(boundary string) error {
	if l.fault == nil || boundary == "before_write" {
		return nil
	}
	return l.fault(boundary)
}

// replaceMemory updates the detached read cache only after durable publication or reload.
func (l *CapacityLedger) replaceMemory(snapshot capacityLedgerSnapshot, version uint64) {
	l.mu.Lock()
	l.receipts, l.events, l.version = snapshot.Receipts, snapshot.Events, version
	l.mu.Unlock()
}

// refresh reloads the current durable head so separate processes share quota and audit state.
func (l *CapacityLedger) refresh(ctx context.Context) error {
	if l.path == "" {
		return nil
	}
	release, err := acquireCleanupStoreLock(ctx, l.path+".lock")
	if err != nil {
		return err
	}
	defer release()
	state, err := loadDurableSnapshot(l.path, "capacity-ledger")
	if err != nil {
		return err
	}
	snapshot := capacityLedgerSnapshot{Receipts: map[string]CapacityReceipt{}, Events: map[string][]CapacityEvent{}}
	if len(state.Payload) > 0 {
		snapshot, err = decodeCapacitySnapshot(state.Payload)
		if err != nil {
			return err
		}
	}
	l.replaceMemory(snapshot, state.Version)
	return nil
}

// cloneCapacitySnapshot detaches maps and event slices for optimistic mutation.
func cloneCapacitySnapshot(receipts map[string]CapacityReceipt, events map[string][]CapacityEvent) capacityLedgerSnapshot {
	c := capacityLedgerSnapshot{Receipts: make(map[string]CapacityReceipt, len(receipts)), Events: make(map[string][]CapacityEvent, len(events))}
	for id, r := range receipts {
		c.Receipts[id] = r
	}
	for p, values := range events {
		c.Events[p] = append([]CapacityEvent(nil), values...)
	}
	return c
}

// Events returns detached content-free audit events for one project.
func (l *CapacityLedger) Events(project string) ([]CapacityEvent, error) {
	if !safeID.MatchString(project) {
		return nil, &Error{Category: CategoryInvalid, Operation: "capacity_events"}
	}
	if err := l.refresh(context.Background()); err != nil {
		return nil, err
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]CapacityEvent(nil), l.events[project]...), nil
}

// Receipt returns one detached private receipt for a trusted composition adapter.
func (l *CapacityLedger) Receipt(ctx context.Context, id string) (CapacityReceipt, error) {
	if !digestPattern.MatchString(id) {
		return CapacityReceipt{}, &Error{Category: CategoryInvalid, Operation: "capacity_receipt_load"}
	}
	if err := l.refresh(ctx); err != nil {
		return CapacityReceipt{}, err
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	receipt, ok := l.receipts[id]
	if !ok {
		return CapacityReceipt{}, &Error{Category: CategoryConflict, Operation: "capacity_receipt_load"}
	}
	return receipt, nil
}

// appendCapacityEvent converges byte-identical redelivery and rejects contradictory identity reuse.
func appendCapacityEvent(s *capacityLedgerSnapshot, e CapacityEvent) error {
	events := s.Events[e.ProjectID]
	for _, existing := range events {
		if existing.ID == e.ID {
			e.At = existing.At
			e.Digest = ""
			e.Digest = digestOf(e)
			if existing.Digest != e.Digest {
				return &Error{Category: CategoryConflict, Operation: "capacity_event_redelivery"}
			}
			return nil
		}
	}
	if e.At.IsZero() {
		e.At = time.Now().UTC()
	}
	e.Digest = ""
	if !validCapacityEvent(e) {
		return &Error{Category: CategoryIntegrity, Operation: "capacity_event"}
	}
	e.Digest = digestOf(e)
	s.Events[e.ProjectID] = append(events, e)
	return nil
}

// validCapacityEvent restricts audit fields to content-free identities and the closed taxonomy.
func validCapacityEvent(e CapacityEvent) bool {
	return safeID.MatchString(e.ID) && safeID.MatchString(e.ProjectID) && safeID.MatchString(e.ReceiptID) && safeID.MatchString(e.Owner) && safeID.MatchString(e.Kind) && validAuditCategory(e.Category) && validDurableTime(e.At)
}

// ledgerResult carries a transition and a domain denial returned after its audit commits.
type ledgerResult struct {
	Receipt CapacityReceipt
	Changed bool
	Domain  error
}

// Reserve atomically admits one exact request before provider I/O.
func (l *CapacityLedger) Reserve(ctx context.Context, request CapacityRequest) (CapacityReceipt, bool, error) {
	policy, err := SealCapacityPolicy(request.Policy)
	if err != nil || policy.Digest != request.Policy.Digest || request.ProjectID != policy.ProjectID || !safeID.MatchString(request.RunID) || !safeID.MatchString(request.AcquisitionKey) || !digestPattern.MatchString(request.SelectionDigest) || request.TTL <= 0 || request.TTL > policy.MaxTTL || request.Demand.Environments != 1 {
		return CapacityReceipt{}, false, &Error{Category: CategoryInvalid, Operation: "capacity_reserve"}
	}
	id := digestOf(struct{ Project, Acquisition string }{request.ProjectID, request.AcquisitionKey})
	wanted := CapacityReceipt{ID: id, ProjectID: request.ProjectID, RunID: request.RunID, AcquisitionKey: request.AcquisitionKey, SelectionDigest: request.SelectionDigest, PolicyDigest: policy.Digest, Demand: request.Demand, TTL: request.TTL}
	wanted.Digest = digestOf(wanted)
	result, err := l.update(ctx, func(s *capacityLedgerSnapshot) (any, bool, error) {
		if current, ok := s.Receipts[id]; ok {
			if !capacityReceiptMatchesRequest(current, wanted) || !capacityReceiptHasLegalProgress(current) {
				return ledgerResult{}, false, &Error{Category: CategoryConflict, Operation: "capacity_redelivery"}
			}
			return ledgerResult{Receipt: current}, false, nil
		}
		usage := ResourceDemand{}
		unknown := false
		for _, r := range s.Receipts {
			if r.ProjectID == request.ProjectID && !r.Released {
				var addErr error
				usage, addErr = AddDemand(usage, r.Demand)
				if addErr != nil {
					return ledgerResult{}, false, addErr
				}
				unknown = unknown || r.Unknown
			}
		}
		total, addErr := AddDemand(usage, request.Demand)
		reason, operation := "", ""
		if unknown {
			reason, operation = "unknown", "capacity_unknown"
		} else if addErr != nil || total.Environments > policy.MaxEnvironments || total.CPUMilli > policy.MaxCPUMilli || total.MemoryBytes > policy.MaxMemoryBytes || total.ObjectCount > policy.MaxObjectCount {
			reason, operation = "limit", "capacity_limit"
		}
		if reason != "" {
			eventID := "deny-" + digestOf(struct{ Receipt, Reason string }{id, reason})[:16]
			eventErr := appendCapacityEvent(s, CapacityEvent{ID: eventID, ProjectID: request.ProjectID, ReceiptID: id, Owner: request.RunID, Kind: "capacity_denied_" + reason, Category: CategoryConflict})
			return ledgerResult{Domain: &Error{Category: CategoryConflict, Operation: operation}}, true, eventErr
		}
		s.Receipts[id] = wanted
		eventErr := appendCapacityEvent(s, CapacityEvent{ID: "reserve-" + id[:16], ProjectID: request.ProjectID, ReceiptID: id, Owner: request.RunID, Kind: "capacity_reserved"})
		return ledgerResult{Receipt: wanted, Changed: true}, true, eventErr
	})
	if err != nil {
		return CapacityReceipt{}, false, err
	}
	value := result.(ledgerResult)
	return value.Receipt, value.Changed, value.Domain
}

// capacityReceiptMatchesRequest compares only immutable admission authority so legal lifecycle progress remains redeliverable.
func capacityReceiptMatchesRequest(current, wanted CapacityReceipt) bool {
	return current.ID == wanted.ID && current.ProjectID == wanted.ProjectID && current.RunID == wanted.RunID && current.AcquisitionKey == wanted.AcquisitionKey && current.SelectionDigest == wanted.SelectionDigest && current.PolicyDigest == wanted.PolicyDigest && current.Demand == wanted.Demand && current.TTL == wanted.TTL && !current.Unknown && current.AuthorityDigest == ""
}

// capacityReceiptHasLegalProgress accepts only initial, lifecycle-bound or provider-confirmed managed states and rejects released or partial authority.
func capacityReceiptHasLegalProgress(receipt CapacityReceipt) bool {
	if receipt.Released || receipt.ReleaseAuthorityDigest != "" || receipt.ConfirmationDigest != "" {
		return false
	}
	providerIdentityComplete := digestPattern.MatchString(receipt.EnvironmentDigest) && digestPattern.MatchString(receipt.ResourceIdentityDigest)
	providerIdentityEmpty := receipt.EnvironmentDigest == "" && receipt.ResourceIdentityDigest == ""
	return providerIdentityEmpty || receipt.LifecycleBound && providerIdentityComplete
}

// ConfirmEnvironment binds a receipt to the exact provider identity returned by acquisition.
func (l *CapacityLedger) ConfirmEnvironment(ctx context.Context, receipt CapacityReceipt, environment, resource string) (CapacityReceipt, error) {
	if !digestPattern.MatchString(environment) || !digestPattern.MatchString(resource) {
		return CapacityReceipt{}, &Error{Category: CategoryIntegrity, Operation: "capacity_confirm"}
	}
	result, err := l.update(ctx, func(s *capacityLedgerSnapshot) (any, bool, error) {
		current, ok := s.Receipts[receipt.ID]
		if !ok || current.Digest != receipt.Digest {
			return CapacityReceipt{}, false, &Error{Category: CategoryConflict, Operation: "capacity_confirm"}
		}
		if current.EnvironmentDigest != "" {
			if current.EnvironmentDigest != environment || current.ResourceIdentityDigest != resource {
				return CapacityReceipt{}, false, &Error{Category: CategoryConflict, Operation: "capacity_confirm"}
			}
			return current, false, nil
		}
		current.Digest = ""
		current.EnvironmentDigest, current.ResourceIdentityDigest = environment, resource
		current.Digest = digestOf(current)
		s.Receipts[current.ID] = current
		eventErr := appendCapacityEvent(s, CapacityEvent{ID: "confirm-" + current.ID[:16], ProjectID: current.ProjectID, ReceiptID: current.ID, Owner: current.RunID, Kind: "capacity_confirmed"})
		return current, true, eventErr
	})
	if err != nil {
		return CapacityReceipt{}, err
	}
	return result.(CapacityReceipt), nil
}

// AdoptUnknown reserves fail-closed orphan capacity when its original receipt is unavailable.
func (l *CapacityLedger) AdoptUnknown(ctx context.Context, project, runID, acquisition, authority string, demand ResourceDemand, ttl time.Duration) (CapacityReceipt, bool, error) {
	if !safeID.MatchString(project) || !safeID.MatchString(runID) || !safeID.MatchString(acquisition) || !digestPattern.MatchString(authority) || demand.Environments != 1 || ttl <= 0 {
		return CapacityReceipt{}, false, &Error{Category: CategoryInvalid, Operation: "capacity_orphan"}
	}
	id := digestOf(struct{ Project, Acquisition string }{project, acquisition})
	result, err := l.update(ctx, func(s *capacityLedgerSnapshot) (any, bool, error) {
		if current, ok := s.Receipts[id]; ok {
			if current.Digest != authority && current.AuthorityDigest != authority {
				return ledgerResult{}, false, &Error{Category: CategoryConflict, Operation: "capacity_orphan"}
			}
			return ledgerResult{Receipt: current}, false, nil
		}
		r := CapacityReceipt{ID: id, ProjectID: project, RunID: runID, AcquisitionKey: acquisition, Demand: demand, TTL: ttl, AuthorityDigest: authority, Unknown: true}
		r.Digest = digestOf(r)
		s.Receipts[id] = r
		eventErr := appendCapacityEvent(s, CapacityEvent{ID: "orphan-" + id[:16], ProjectID: project, ReceiptID: id, Owner: runID, Kind: "capacity_orphan_adopted"})
		return ledgerResult{Receipt: r, Changed: true}, true, eventErr
	})
	if err != nil {
		return CapacityReceipt{}, false, err
	}
	value := result.(ledgerResult)
	return value.Receipt, value.Changed, nil
}

// BlockUnknown records malformed owned inventory as an admission block.
func (l *CapacityLedger) BlockUnknown(ctx context.Context, project, identity string) (CapacityReceipt, bool, error) {
	if !safeID.MatchString(project) || !digestPattern.MatchString(identity) {
		return CapacityReceipt{}, false, &Error{Category: CategoryInvalid, Operation: "capacity_unknown"}
	}
	id := digestOf(struct{ Project, Identity string }{project, identity})
	result, err := l.update(ctx, func(s *capacityLedgerSnapshot) (any, bool, error) {
		if current, ok := s.Receipts[id]; ok {
			return ledgerResult{Receipt: current}, false, nil
		}
		r := CapacityReceipt{ID: id, ProjectID: project, Unknown: true}
		r.Digest = digestOf(r)
		s.Receipts[id] = r
		eventErr := appendCapacityEvent(s, CapacityEvent{ID: "unknown-" + id[:16], ProjectID: project, ReceiptID: id, Owner: "inventory-discovery", Kind: "capacity_unknown_blocked", Category: CategoryIntegrity})
		return ledgerResult{Receipt: r, Changed: true}, true, eventErr
	})
	if err != nil {
		return CapacityReceipt{}, false, err
	}
	value := result.(ledgerResult)
	return value.Receipt, value.Changed, nil
}

// ReleaseConfirmationDigest derives the sole accepted proof from cleanup, environment, UID and receipt.
func ReleaseConfirmationDigest(cleanupID, environment, resource, receiptID string) string {
	return digestOf(struct{ Cleanup, Environment, Resource, Receipt string }{cleanupID, environment, resource, receiptID})
}

// Release frees capacity exactly once and persists the exact confirmation proof.
func (l *CapacityLedger) Release(ctx context.Context, receipt CapacityReceipt, cleanupID, confirmation string) (CapacityReceipt, bool, error) {
	if !safeID.MatchString(cleanupID) || !digestPattern.MatchString(confirmation) {
		return CapacityReceipt{}, false, &Error{Category: CategoryIntegrity, Operation: "capacity_release"}
	}
	result, err := l.update(ctx, func(s *capacityLedgerSnapshot) (any, bool, error) {
		current, ok := s.Receipts[receipt.ID]
		if !ok || current.Digest != receipt.Digest || current.EnvironmentDigest == "" {
			return ledgerResult{}, false, &Error{Category: CategoryConflict, Operation: "capacity_release"}
		}
		expected := ReleaseConfirmationDigest(cleanupID, current.EnvironmentDigest, current.ResourceIdentityDigest, current.ID)
		if confirmation != expected || current.ConfirmationDigest != "" && current.ConfirmationDigest != confirmation {
			return ledgerResult{}, false, &Error{Category: CategoryConflict, Operation: "capacity_release_confirmation"}
		}
		if current.Released {
			return ledgerResult{Receipt: current}, false, nil
		}
		current.ReleaseAuthorityDigest = current.Digest
		current.Digest = ""
		current.Released, current.ConfirmationDigest = true, confirmation
		current.Digest = digestOf(current)
		s.Receipts[current.ID] = current
		eventErr := appendCapacityEvent(s, CapacityEvent{ID: "release-" + current.ID[:16], ProjectID: current.ProjectID, ReceiptID: current.ID, Owner: current.RunID, Kind: "capacity_released"})
		return ledgerResult{Receipt: current, Changed: true}, true, eventErr
	})
	if err != nil {
		return CapacityReceipt{}, false, err
	}
	value := result.(ledgerResult)
	return value.Receipt, value.Changed, nil
}

// ReleaseConfirmed resolves the private receipt before enforcing its exact proof.
func (l *CapacityLedger) ReleaseConfirmed(ctx context.Context, project, acquisition, expectedDigest, cleanupID, confirmation string) (CapacityReceipt, bool, error) {
	id := digestOf(struct{ Project, Acquisition string }{project, acquisition})
	if err := l.refresh(ctx); err != nil {
		return CapacityReceipt{}, false, err
	}
	l.mu.Lock()
	current, ok := l.receipts[id]
	l.mu.Unlock()
	if !ok || current.Digest != expectedDigest && current.AuthorityDigest != expectedDigest && current.ReleaseAuthorityDigest != expectedDigest {
		return CapacityReceipt{}, false, &Error{Category: CategoryConflict, Operation: "capacity_release_binding"}
	}
	return l.Release(ctx, current, cleanupID, confirmation)
}

// Snapshot returns one project's detached current aggregate.
func (l *CapacityLedger) Snapshot(project string) (CapacitySnapshot, error) {
	if err := l.refresh(context.Background()); err != nil {
		return CapacitySnapshot{}, err
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	usage := ResourceDemand{}
	unknown := false
	for _, r := range l.receipts {
		if r.ProjectID == project && !r.Released {
			var err error
			usage, err = AddDemand(usage, r.Demand)
			if err != nil {
				return CapacitySnapshot{}, err
			}
			unknown = unknown || r.Unknown
		}
	}
	return CapacitySnapshot{ProjectID: project, Reserved: usage, Unknown: unknown}, nil
}
