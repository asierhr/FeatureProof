package cleanup

import (
	"context"
	"encoding/json"
	"sort"
	"sync"
	"time"
)

// Page is a bounded canonical scan result.
type Page struct {
	Items    []Item
	Next     string
	Scanned  bool
	Complete bool
}

// Store is the consumer-owned durable scheduling boundary.
type Store interface {
	Reserve(context.Context, Item, Event) (Item, bool, error)
	Scan(context.Context, string, int) (Page, error)
	Claim(context.Context, string, string, time.Time, time.Duration) (Item, bool, error)
	Renew(context.Context, Item, string, uint64, time.Time, time.Duration) (Item, error)
	SaveOwned(context.Context, Item, string, uint64, Event) (Item, error)
	ReleaseOwner(context.Context, Item, string, uint64, Event) (Item, error)
	Requeue(context.Context, RequeueAuthorization, time.Time) (Item, error)
	Events(context.Context, string) ([]Event, error)
	Status(context.Context, string) (Status, error)
}

// Status returns a content-free operational projection without lifecycle lookup authority.
func (s *LocalStore) Status(ctx context.Context, id string) (Status, error) {
	if err := ctx.Err(); err != nil {
		return Status{}, err
	}
	if !safeID.MatchString(id) {
		return Status{}, &Error{Category: CategoryInvalid, Operation: "status"}
	}
	if err := s.refresh(ctx); err != nil {
		return Status{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	item, ok := s.data.Items[id]
	if !ok {
		return Status{}, &Error{Category: CategoryInvalid, Operation: "status_missing"}
	}
	updated := item.CreatedAt
	events := s.data.Events[id]
	if len(events) > 0 && events[len(events)-1].At.After(updated) {
		updated = events[len(events)-1].At
	}
	return Status{CleanupID: item.CleanupID, ProjectID: item.ProjectID, State: item.State, Attempts: item.Attempts, NextAttemptAt: item.NextAttemptAt, Category: item.LastCategory, UpdatedAt: updated}, nil
}

// Requeue applies one sealed operator decision to an exact quarantined or blocked item.
func (s *LocalStore) Requeue(ctx context.Context, authorization RequeueAuthorization, now time.Time) (Item, error) {
	if err := ctx.Err(); err != nil {
		return Item{}, err
	}
	if VerifyRequeueAuthorization(authorization) != nil || !validDurableTime(now) {
		return Item{}, &Error{Category: CategoryUnauthorized, Operation: "requeue"}
	}
	result, err := s.update(ctx, func(snapshot *localSnapshot) (any, bool, error) {
		current, ok := snapshot.Items[authorization.CleanupID]
		if !ok || current.ProjectID != authorization.ProjectID || current.Digest != authorization.ExpectedItemDigest || current.LeaseOwner != "" || current.State != StateQuarantine && current.State != StateBlocked {
			return Item{}, false, &Error{Category: CategoryConflict, Operation: "requeue"}
		}
		current.State, current.Attempts, current.CumulativeWait, current.NextAttemptAt, current.LastCategory = StatePending, 0, 0, time.Time{}, ""
		current.VersionNumber++
		current.Digest = ""
		current, _ = SealItem(current)
		snapshot.Items[current.CleanupID] = current
		if eventErr := appendStoreEvent(snapshot, current, Event{ID: authorization.ID, Owner: authorization.Principal, Kind: "cleanup_requeued", At: now.UTC()}); eventErr != nil {
			return Item{}, false, eventErr
		}
		return cloneItem(current), true, nil
	})
	if err != nil {
		return Item{}, err
	}
	return result.(Item), nil
}

// CursorStore durably checkpoints each independent discovery source using compare-and-swap.
type CursorStore interface {
	LoadCursor(context.Context, string) (string, error)
	SaveCursor(context.Context, string, string, string) error
}

// CursorPass identifies one durable bounded scan epoch and its monotonic continuation.
type CursorPass struct {
	Epoch uint64
	Value string
}

// PassCursorStore atomically advances or completes restartable scan epochs.
type PassCursorStore interface {
	LoadCursorPass(context.Context, string) (CursorPass, error)
	SaveCursorPass(context.Context, string, CursorPass, CursorPass) error
}

// localSnapshot is the complete reopenable local cleanup state.
type localSnapshot struct {
	Items   map[string]Item          `json:"items"`
	Events  map[string][]Event       `json:"events"`
	Cursors map[string]cursorReceipt `json:"cursors,omitempty"`
}

// cursorReceipt seals one source continuation so corruption cannot silently skip cleanup work.
type cursorReceipt struct {
	Version, Source, Value, Digest string
	Epoch                          uint64
}

// LocalStore publishes immutable version receipts behind a process-safe durable head.
type LocalStore struct {
	mu      sync.Mutex
	path    string
	data    localSnapshot
	version uint64
	fault   func(string) error
}

// OpenLocalStore loads or creates one authoritative local cleanup store.
func OpenLocalStore(path string) (*LocalStore, error) {
	if path == "" {
		return nil, &Error{Category: CategoryInvalid, Operation: "store_path"}
	}
	store := &LocalStore{path: path, data: emptyLocalSnapshot()}
	if err := ensureDurableParent(path); err != nil {
		return nil, &Error{Category: CategoryIntegrity, Operation: "store_directory", Cause: err}
	}
	release, err := acquireCleanupStoreLock(context.Background(), path+".lock")
	if err != nil {
		return nil, &Error{Category: CategoryIntegrity, Operation: "store_lock", Cause: err}
	}
	defer release()
	state, err := loadDurableSnapshot(path, "cleanup-store")
	if err != nil {
		return nil, &Error{Category: CategoryIntegrity, Operation: "store_open", Cause: err}
	}
	if len(state.Payload) == 0 {
		return store, nil
	}
	store.data, err = decodeLocalSnapshot(state.Payload)
	if err != nil {
		return nil, err
	}
	store.version = state.Version
	return store, nil
}

// emptyLocalSnapshot creates detached maps for one new durable cleanup store.
func emptyLocalSnapshot() localSnapshot {
	return localSnapshot{Items: map[string]Item{}, Events: map[string][]Event{}, Cursors: map[string]cursorReceipt{}}
}

// decodeLocalSnapshot validates every item, cursor and audit event loaded from one immutable receipt.
func decodeLocalSnapshot(raw []byte) (localSnapshot, error) {
	data := localSnapshot{}
	if json.Unmarshal(raw, &data) != nil || data.Items == nil || data.Events == nil {
		return localSnapshot{}, &Error{Category: CategoryIntegrity, Operation: "store_open"}
	}
	if data.Cursors == nil {
		data.Cursors = map[string]cursorReceipt{}
	}
	for source, receipt := range data.Cursors {
		candidate := receipt
		candidate.Digest = ""
		if receipt.Version != Version || receipt.Source != source || source == "workflow-intents" && receipt.Epoch == 0 || receipt.Digest != digestOf(candidate) {
			return localSnapshot{}, &Error{Category: CategoryIntegrity, Operation: "store_cursor"}
		}
	}
	for _, item := range data.Items {
		if VerifyItem(item) != nil {
			return localSnapshot{}, &Error{Category: CategoryIntegrity, Operation: "store_item"}
		}
	}
	for cleanupID, events := range data.Events {
		seen := map[string]struct{}{}
		for _, event := range events {
			candidate := event
			candidate.Digest = ""
			_, duplicate := seen[event.ID]
			ownerValid := event.Owner == "" || safeID.MatchString(event.Owner)
			if duplicate || event.CleanupID != cleanupID || !safeID.MatchString(event.ID) || !safeID.MatchString(event.ProjectID) || !safeID.MatchString(event.Kind) || !digestPattern.MatchString(event.OwnerDigest) || !ownerValid || !validAuditCategory(event.Category) || !validDurableTime(event.At) || event.Digest != digestOf(candidate) {
				return localSnapshot{}, &Error{Category: CategoryIntegrity, Operation: "store_event"}
			}
			seen[event.ID] = struct{}{}
		}
	}
	return data, nil
}

// storeMutation prepares one immutable candidate snapshot and reports whether durable publication is required.
type storeMutation func(*localSnapshot) (any, bool, error)

// update serializes durable head selection across processes while keeping every filesystem operation outside the Go mutex.
func (s *LocalStore) update(ctx context.Context, mutate storeMutation) (any, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if s.fault != nil {
		if err := s.fault("before_write"); err != nil {
			return nil, &Error{Category: CategoryIntegrity, Operation: "store_fault", Cause: err}
		}
	}
	release, err := acquireCleanupStoreLock(ctx, s.path+".lock")
	if err != nil {
		return nil, &Error{Category: CategoryIntegrity, Operation: "store_lock", Cause: err}
	}
	defer release()
	state, err := loadDurableSnapshot(s.path, "cleanup-store")
	if err != nil {
		return nil, &Error{Category: CategoryIntegrity, Operation: "store_load", Cause: err}
	}
	current := emptyLocalSnapshot()
	if len(state.Payload) > 0 {
		current, err = decodeLocalSnapshot(state.Payload)
		if err != nil {
			return nil, err
		}
	}
	candidate := cloneLocalSnapshot(current)
	result, changed, err := mutate(&candidate)
	if err != nil || !changed {
		s.replaceMemory(current, state.Version)
		return result, err
	}
	if err = s.mutationFault(); err != nil {
		return nil, &Error{Category: CategoryIntegrity, Operation: "store_fault", Cause: err}
	}
	raw, err := json.Marshal(candidate)
	if err != nil {
		return nil, &Error{Category: CategoryIntegrity, Operation: "store_encode", Cause: err}
	}
	err = publishDurableSnapshot(ctx, s.path, "cleanup-store", state, raw, s.durableFault)
	if err != nil {
		if committed, loadErr := loadDurableSnapshot(s.path, "cleanup-store"); loadErr == nil && committed.Version > state.Version {
			if committedData, decodeErr := decodeLocalSnapshot(committed.Payload); decodeErr == nil {
				s.replaceMemory(committedData, committed.Version)
			}
		}
		return nil, &Error{Category: CategoryIntegrity, Operation: "store_publish", Cause: err}
	}
	s.replaceMemory(candidate, state.Version+1)
	return result, nil
}

// mutationFault exposes event and index crash boundaries before either becomes reachable from a durable head.
func (s *LocalStore) mutationFault() error {
	if s.fault == nil {
		return nil
	}
	for _, boundary := range []string{"before_event", "after_event", "before_index", "after_index"} {
		if err := s.fault(boundary); err != nil {
			return err
		}
	}
	return nil
}

// durableFault preserves legacy boundary names while avoiding a second pre-lock before_write callback.
func (s *LocalStore) durableFault(boundary string) error {
	if s.fault == nil || boundary == "before_write" {
		return nil
	}
	return s.fault(boundary)
}

// replaceMemory publishes a detached read cache only after durable head selection completes.
func (s *LocalStore) replaceMemory(data localSnapshot, version uint64) {
	s.mu.Lock()
	s.data, s.version = cloneLocalSnapshot(data), version
	s.mu.Unlock()
}

// refresh reloads the current durable head so independently opened instances observe every commit.
func (s *LocalStore) refresh(ctx context.Context) error {
	release, err := acquireCleanupStoreLock(ctx, s.path+".lock")
	if err != nil {
		return err
	}
	defer release()
	state, err := loadDurableSnapshot(s.path, "cleanup-store")
	if err != nil {
		return err
	}
	data := emptyLocalSnapshot()
	if len(state.Payload) > 0 {
		data, err = decodeLocalSnapshot(state.Payload)
		if err != nil {
			return err
		}
	}
	s.replaceMemory(data, state.Version)
	return nil
}

// cloneLocalSnapshot detaches every mutable map and event slice before optimistic mutation.
func cloneLocalSnapshot(source localSnapshot) localSnapshot {
	clone := localSnapshot{Items: make(map[string]Item, len(source.Items)), Events: make(map[string][]Event, len(source.Events)), Cursors: make(map[string]cursorReceipt, len(source.Cursors))}
	for id, item := range source.Items {
		clone.Items[id] = cloneItem(item)
	}
	for id, events := range source.Events {
		clone.Events[id] = append([]Event(nil), events...)
	}
	for id, cursor := range source.Cursors {
		clone.Cursors[id] = cursor
	}
	return clone
}

// LoadCursor returns the last committed continuation for one trusted discovery source.
func (s *LocalStore) LoadCursor(ctx context.Context, source string) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if !safeID.MatchString(source) {
		return "", &Error{Category: CategoryInvalid, Operation: "cursor_load"}
	}
	if err := s.refresh(ctx); err != nil {
		return "", err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	receipt, ok := s.data.Cursors[source]
	if !ok {
		return "", nil
	}
	candidate := receipt
	candidate.Digest = ""
	if receipt.Version != Version || receipt.Source != source || receipt.Digest != digestOf(candidate) {
		return "", &Error{Category: CategoryIntegrity, Operation: "cursor_load"}
	}
	return receipt.Value, nil
}

// SaveCursor advances one source checkpoint only when the caller observed its current value.
func (s *LocalStore) SaveCursor(ctx context.Context, source, expected, next string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if !safeID.MatchString(source) || source == "workflow-intents" {
		return &Error{Category: CategoryInvalid, Operation: "cursor_save"}
	}
	_, err := s.update(ctx, func(snapshot *localSnapshot) (any, bool, error) {
		current := snapshot.Cursors[source]
		if current.Value != expected {
			return nil, false, &Error{Category: CategoryConflict, Operation: "cursor_cas"}
		}
		receipt := cursorReceipt{Version: Version, Source: source, Value: next}
		receipt.Digest = digestOf(receipt)
		snapshot.Cursors[source] = receipt
		return nil, true, nil
	})
	return err
}

// LoadCursorPass returns the current workflow scan epoch, creating epoch one logically before its first commit.
func (s *LocalStore) LoadCursorPass(ctx context.Context, source string) (CursorPass, error) {
	if err := ctx.Err(); err != nil {
		return CursorPass{}, err
	}
	if source != "workflow-intents" {
		return CursorPass{}, &Error{Category: CategoryInvalid, Operation: "cursor_pass_load"}
	}
	if err := s.refresh(ctx); err != nil {
		return CursorPass{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	receipt, ok := s.data.Cursors[source]
	if !ok {
		return CursorPass{Epoch: 1}, nil
	}
	candidate := receipt
	candidate.Digest = ""
	if receipt.Version != Version || receipt.Source != source || receipt.Epoch == 0 || receipt.Digest != digestOf(candidate) {
		return CursorPass{}, &Error{Category: CategoryIntegrity, Operation: "cursor_pass_load"}
	}
	return CursorPass{Epoch: receipt.Epoch, Value: receipt.Value}, nil
}

// SaveCursorPass commits either a monotonic continuation in the same epoch or one authorized completion into the next epoch.
func (s *LocalStore) SaveCursorPass(ctx context.Context, source string, expected, next CursorPass) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if source != "workflow-intents" || expected.Epoch == 0 || next.Epoch == 0 {
		return &Error{Category: CategoryInvalid, Operation: "cursor_pass_save"}
	}
	advance := next.Epoch == expected.Epoch && next.Value != "" && (expected.Value == "" || next.Value > expected.Value)
	complete := expected.Epoch < ^uint64(0) && next.Epoch == expected.Epoch+1 && next.Value == ""
	if !advance && !complete {
		return &Error{Category: CategoryIntegrity, Operation: "cursor_pass_transition"}
	}
	_, err := s.update(ctx, func(snapshot *localSnapshot) (any, bool, error) {
		current, ok := snapshot.Cursors[source]
		actual := CursorPass{Epoch: 1}
		if ok {
			actual = CursorPass{Epoch: current.Epoch, Value: current.Value}
		}
		if actual != expected {
			return nil, false, &Error{Category: CategoryConflict, Operation: "cursor_pass_cas"}
		}
		receipt := cursorReceipt{Version: Version, Source: source, Value: next.Value, Epoch: next.Epoch}
		receipt.Digest = digestOf(receipt)
		snapshot.Cursors[source] = receipt
		return nil, true, nil
	})
	return err
}

// Reserve persists intent before any provider effect and converges exact redelivery.
func (s *LocalStore) Reserve(ctx context.Context, candidate Item, event Event) (Item, bool, error) {
	if err := ctx.Err(); err != nil {
		return Item{}, false, err
	}
	sealed, err := SealItem(candidate)
	if err != nil {
		return Item{}, false, err
	}
	result, err := s.update(ctx, func(snapshot *localSnapshot) (any, bool, error) {
		if current, ok := snapshot.Items[sealed.CleanupID]; ok {
			if !sameCleanupIntent(current, sealed) {
				return storeReserveResult{}, false, &Error{Category: CategoryConflict, Operation: "reserve"}
			}
			return storeReserveResult{Item: cloneItem(current)}, false, nil
		}
		snapshot.Items[sealed.CleanupID] = sealed
		if eventErr := appendStoreEvent(snapshot, sealed, event); eventErr != nil {
			return storeReserveResult{}, false, eventErr
		}
		return storeReserveResult{Item: cloneItem(sealed), Changed: true}, true, nil
	})
	if err != nil {
		return Item{}, false, err
	}
	reserved := result.(storeReserveResult)
	return reserved.Item, reserved.Changed, nil
}

// storeReserveResult transports one optimistic reservation result without exposing the candidate snapshot.
type storeReserveResult struct {
	Item    Item
	Changed bool
}

// sameCleanupIntent compares immutable authority while allowing redelivery after scheduler state advances.
func sameCleanupIntent(current, candidate Item) bool {
	initialState := candidate.State == StateDiscovered || candidate.Source != "orphan" && candidate.State == StateIntentCommitted
	initial := initialState && candidate.VersionNumber == 1 && candidate.Attempts == 0 && candidate.LeaseOwner == "" && candidate.LeaseUntil.IsZero() && candidate.NextAttemptAt.IsZero() && candidate.CumulativeWait == 0 && candidate.LastCategory == ""
	return initial && current.CleanupID == candidate.CleanupID && current.Source == candidate.Source && current.OwnerDigest == candidate.OwnerDigest && current.ProjectID == candidate.ProjectID && current.RunID == candidate.RunID && current.Binding.Digest == candidate.Binding.Digest && current.CreatedAt.Equal(candidate.CreatedAt) && current.ExpiresAt.Equal(candidate.ExpiresAt) && current.TTL == candidate.TTL && current.Demand == candidate.Demand
}

// Scan returns a stable ID-ordered page and an opaque next ID cursor.
func (s *LocalStore) Scan(ctx context.Context, cursor string, limit int) (Page, error) {
	if err := ctx.Err(); err != nil {
		return Page{}, err
	}
	if limit <= 0 {
		return Page{}, &Error{Category: CategoryInvalid, Operation: "scan"}
	}
	if err := s.refresh(ctx); err != nil {
		return Page{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	ids := make([]string, 0, len(s.data.Items))
	for id := range s.data.Items {
		if id > cursor {
			ids = append(ids, id)
		}
	}
	sort.Strings(ids)
	next := ""
	if len(ids) > limit {
		next, ids = ids[limit-1], ids[:limit]
	}
	items := make([]Item, len(ids))
	for index, id := range ids {
		items[index] = cloneItem(s.data.Items[id])
	}
	return Page{Items: items, Next: next}, nil
}

// Claim grants one inclusive-expiry lease and increments its fencing token.
func (s *LocalStore) Claim(ctx context.Context, id, owner string, now time.Time, ttl time.Duration) (Item, bool, error) {
	if err := ctx.Err(); err != nil {
		return Item{}, false, err
	}
	if !safeID.MatchString(owner) || ttl <= 0 {
		return Item{}, false, &Error{Category: CategoryInvalid, Operation: "claim"}
	}
	result, err := s.update(ctx, func(snapshot *localSnapshot) (any, bool, error) {
		item, ok := snapshot.Items[id]
		if !ok {
			return storeReserveResult{}, false, &Error{Category: CategoryInvalid, Operation: "claim_missing"}
		}
		if item.LeaseOwner != "" && now.Before(item.LeaseUntil) || !eligible(item, now) {
			return storeReserveResult{Item: cloneItem(item)}, false, nil
		}
		item.LeaseOwner, item.LeaseUntil = owner, now.UTC().Add(ttl)
		if item.State != StateConfirmed {
			item.State = StateClaimed
		}
		item.FencingVersion++
		item.VersionNumber++
		item.Digest = ""
		item, _ = SealItem(item)
		snapshot.Items[id] = item
		claimEvent := Event{ID: id + ":claim:" + itoa(item.FencingVersion), Owner: owner, Kind: "cleanup_claimed", At: now.UTC()}
		if eventErr := appendStoreEvent(snapshot, item, claimEvent); eventErr != nil {
			return storeReserveResult{}, false, eventErr
		}
		return storeReserveResult{Item: cloneItem(item), Changed: true}, true, nil
	})
	if err != nil {
		return Item{}, false, err
	}
	claimed := result.(storeReserveResult)
	return claimed.Item, claimed.Changed, nil
}

// Renew extends only the live lease held by the current fencing owner.
func (s *LocalStore) Renew(ctx context.Context, expected Item, owner string, fencing uint64, now time.Time, ttl time.Duration) (Item, error) {
	return s.mutateOwned(ctx, expected, owner, fencing, now, func(item *Item) { item.LeaseUntil = now.UTC().Add(ttl) }, Event{})
}

// SaveOwned persists one state transition while proving current ownership.
func (s *LocalStore) SaveOwned(ctx context.Context, value Item, owner string, fencing uint64, event Event) (Item, error) {
	return s.mutateOwned(ctx, value, owner, fencing, time.Time{}, func(item *Item) { *item = value; item.LeaseOwner = owner }, event)
}

// ReleaseOwner yields a claimed item without discarding its durable intent.
func (s *LocalStore) ReleaseOwner(ctx context.Context, value Item, owner string, fencing uint64, event Event) (Item, error) {
	return s.mutateOwned(ctx, value, owner, fencing, time.Time{}, func(item *Item) { *item = value; item.LeaseOwner = ""; item.LeaseUntil = time.Time{} }, event)
}

// Events returns detached append-only audit events for one cleanup identity.
func (s *LocalStore) Events(ctx context.Context, id string) ([]Event, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := s.refresh(ctx); err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]Event(nil), s.data.Events[id]...), nil
}

// mutateOwned applies one CAS transition and never invokes external work under the lock.
func (s *LocalStore) mutateOwned(ctx context.Context, expected Item, owner string, fencing uint64, now time.Time, mutate func(*Item), event Event) (Item, error) {
	if err := ctx.Err(); err != nil {
		return Item{}, err
	}
	result, err := s.update(ctx, func(snapshot *localSnapshot) (any, bool, error) {
		current, ok := snapshot.Items[expected.CleanupID]
		if !ok || current.LeaseOwner != owner || current.FencingVersion != fencing || current.VersionNumber != expected.VersionNumber || (!now.IsZero() && !now.Before(current.LeaseUntil)) {
			return Item{}, false, &Error{Category: CategoryConflict, Operation: "owned_save"}
		}
		mutate(&current)
		current.FencingVersion = fencing
		current.VersionNumber++
		current.Digest = ""
		sealed, sealErr := SealItem(current)
		if sealErr != nil {
			return Item{}, false, sealErr
		}
		snapshot.Items[sealed.CleanupID] = sealed
		if event.Owner == "" {
			event.Owner = owner
		}
		if eventErr := appendStoreEvent(snapshot, sealed, event); eventErr != nil {
			return Item{}, false, eventErr
		}
		return cloneItem(sealed), true, nil
	})
	if err != nil {
		return Item{}, err
	}
	return result.(Item), nil
}

// appendStoreEvent appends only byte-identical redelivery and rejects reuse of an event identity with different sealed content.
func appendStoreEvent(snapshot *localSnapshot, item Item, event Event) error {
	if event.ID == "" {
		return nil
	}
	if event.Kind == "" {
		event.Kind = "cleanup_state_changed"
	}
	events := snapshot.Events[item.CleanupID]
	event.CleanupID, event.ProjectID, event.OwnerDigest, event.State, event.Attempt, event.FencingVersion = item.CleanupID, item.ProjectID, item.OwnerDigest, item.State, item.Attempts, item.FencingVersion
	if event.Owner == "" {
		event.Owner = item.LeaseOwner
	}
	event.At = event.At.UTC()
	event.Category = item.LastCategory
	event.Digest = ""
	event.Digest = digestOf(event)
	for _, existing := range events {
		if existing.ID == event.ID {
			if existing.Digest != event.Digest {
				return &Error{Category: CategoryConflict, Operation: "store_event_redelivery"}
			}
			return nil
		}
	}
	snapshot.Events[item.CleanupID] = append(events, event)
	return nil
}

// cloneItem prevents callers from mutating stored cleanup authority.
func cloneItem(value Item) Item {
	if value.Uncertain != nil {
		uncertain := *value.Uncertain
		value.Uncertain = &uncertain
	}
	return value
}
