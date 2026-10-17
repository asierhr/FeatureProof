package githubpr

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"
)

// ErrNotFound indicates that no durable record exists for an opaque identity.
var ErrNotFound = errors.New("githubpr record not found")

// maxJournalRecordBytes is the strict scanner ceiling; every encoded line must remain below it.
const maxJournalRecordBytes = 1 << 20

// maxIntentRecordBytes leaves deterministic room for both allowed intents and their sealed association in one recovery batch.
const maxIntentRecordBytes = maxJournalRecordBytes / 3

// StoreEvent is one append-only audit fact from which snapshots can be rebuilt.
type StoreEvent struct {
	Sequence    uint64             `json:"sequence"`
	Kind        string             `json:"kind"`
	At          time.Time          `json:"at"`
	Delivery    *DeliveryReceipt   `json:"delivery,omitempty"`
	Association *Association       `json:"association,omitempty"`
	Intent      *PublicationIntent `json:"intent,omitempty"`
	Claims      *ClaimBatch        `json:"claims,omitempty"`
}

// ClaimBatch is one journal record that makes an already-validated recovery lease set visible atomically.
type ClaimBatch struct {
	Associations []Association       `json:"associations"`
	Intents      []PublicationIntent `json:"intents"`
}

// Store owns deliveries, associations, publication intents, CAS, leases, and audit history.
type Store interface {
	DeliveryStore
	ReserveAssociation(context.Context, Association) (Association, bool, error)
	GetAssociation(context.Context, string) (Association, error)
	UpdateAssociation(context.Context, Association, uint64) (Association, error)
	ClaimAssociation(context.Context, string, string, time.Time, time.Duration) (Association, bool, error)
	RenewAssociation(context.Context, string, string, time.Time, time.Duration) (Association, error)
	ReleaseAssociation(context.Context, string, string, time.Time) error
	PrepareIntent(context.Context, PublicationIntent) (PublicationIntent, bool, error)
	ConfirmIntent(context.Context, string, string, string) error
	ReleaseIntent(context.Context, string, string, string, time.Time) error
	ClaimPending(context.Context, string, time.Time, time.Duration) ([]Association, []PublicationIntent, error)
	ClaimPendingExcept(context.Context, string, time.Time, time.Duration, map[string]bool) ([]Association, []PublicationIntent, error)
	Events(context.Context) ([]StoreEvent, error)
}

// ReleaseIntent yields an unconfirmed startup claim so another process can recover it immediately.
func (s *LocalStore) ReleaseIntent(ctx context.Context, associationID, kind, owner string, now time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	release, err := s.refreshLocked(ctx)
	if err != nil {
		return err
	}
	defer release()
	key := associationID + ":" + kind
	intent, ok := s.state.Intents[key]
	if !ok {
		return ErrNotFound
	}
	if intent.State != "prepared" {
		return nil
	}
	if intent.Owner != owner {
		return &Error{Code: CodeConflict, Operation: "intent_owner"}
	}
	intent.Owner, intent.LeaseUntil = "", time.Time{}
	return s.commit(StoreEvent{Kind: "intent_released", At: now, Intent: &intent})
}

// RenewAssociation extends only the current owner's durable lease and advances CAS before another external boundary.
func (s *LocalStore) RenewAssociation(ctx context.Context, id, owner string, now time.Time, ttl time.Duration) (Association, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	release, err := s.refreshLocked(ctx)
	if err != nil {
		return Association{}, err
	}
	defer release()
	association, ok := s.state.Associations[id]
	if !ok {
		return Association{}, ErrNotFound
	}
	if err := VerifyAssociation(association); err != nil {
		return Association{}, err
	}
	if owner == "" || ttl <= 0 || association.Owner != owner || !association.LeaseUntil.After(now) {
		return Association{}, &Error{Code: CodeConflict, Operation: "association_lease"}
	}
	association.LeaseUntil = now.Add(ttl)
	association.VersionCAS++
	association, err = SealAssociation(association)
	if err != nil {
		return Association{}, err
	}
	if err = s.commit(StoreEvent{Kind: "association_renewed", At: now, Association: &association}); err != nil {
		return Association{}, err
	}
	return association, nil
}

// storeSnapshot is a disposable derived index rebuilt from the authoritative journal.
type storeSnapshot struct {
	Sequence     uint64                       `json:"sequence"`
	Deliveries   map[string]DeliveryReceipt   `json:"deliveries"`
	Associations map[string]Association       `json:"associations"`
	Intents      map[string]PublicationIntent `json:"intents"`
}

// LocalStore persists an append-only journal and atomically replaced derived snapshot.
type LocalStore struct {
	mu                     sync.Mutex
	dir, journal, snapshot string
	state                  storeSnapshot
	// beforeCommit injects a pre-append failure in hermetic durability tests.
	beforeCommit func(StoreEvent) error
}

// OpenLocalStore loads and verifies the journal before exposing durable operations.
func OpenLocalStore(dir string) (*LocalStore, error) {
	if dir == "" {
		return nil, &Error{Code: CodeInvalid, Operation: "store"}
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("create github store: %w", err)
	}
	s := &LocalStore{dir: dir, journal: filepath.Join(dir, "events.jsonl"), snapshot: filepath.Join(dir, "snapshot.json"), state: storeSnapshot{Deliveries: map[string]DeliveryReceipt{}, Associations: map[string]Association{}, Intents: map[string]PublicationIntent{}}}
	release, err := acquireGitHubStoreLock(context.Background(), filepath.Join(dir, "store.lock"))
	if err != nil {
		return nil, fmt.Errorf("lock github store: %w", err)
	}
	defer release()
	if err := s.replay(); err != nil {
		return nil, err
	}
	return s, nil
}

// ReserveDelivery atomically establishes delivery ID and digest idempotency.
func (s *LocalStore) ReserveDelivery(ctx context.Context, receipt DeliveryReceipt, owner string, now time.Time, leaseTTL time.Duration) (DeliveryReceipt, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	release, err := s.refreshLocked(ctx)
	if err != nil {
		return DeliveryReceipt{}, false, err
	}
	defer release()
	if err := ctx.Err(); err != nil {
		return DeliveryReceipt{}, false, err
	}
	if prior, ok := s.state.Deliveries[receipt.DeliveryID]; ok {
		if prior.PayloadSHA256 != receipt.PayloadSHA256 {
			return DeliveryReceipt{}, false, &Error{Code: CodeConflict, Operation: "delivery"}
		}
		if prior.State == "received" && !prior.LeaseUntil.After(now) {
			prior.Owner, prior.LeaseUntil, prior.VersionCAS = owner, now.Add(leaseTTL), prior.VersionCAS+1
			if err := s.commit(StoreEvent{Kind: "delivery_reclaimed", At: now, Delivery: &prior}); err != nil {
				return DeliveryReceipt{}, false, err
			}
			return prior, true, nil
		}
		return prior, false, nil
	}
	if owner == "" || leaseTTL <= 0 {
		return DeliveryReceipt{}, false, &Error{Code: CodeInvalid, Operation: "delivery_lease"}
	}
	receipt.VersionCAS = 1
	receipt.Owner, receipt.LeaseUntil = owner, now.Add(leaseTTL)
	event := StoreEvent{Kind: "delivery_reserved", At: now, Delivery: &receipt}
	if err := s.commit(event); err != nil {
		return DeliveryReceipt{}, false, err
	}
	return receipt, true, nil
}

// CompleteDelivery uses CAS to attach the replayable terminal admission result.
func (s *LocalStore) CompleteDelivery(ctx context.Context, id string, version uint64, state, associationID string) (DeliveryReceipt, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	release, err := s.refreshLocked(ctx)
	if err != nil {
		return DeliveryReceipt{}, err
	}
	defer release()
	if err := ctx.Err(); err != nil {
		return DeliveryReceipt{}, err
	}
	receipt, ok := s.state.Deliveries[id]
	if !ok {
		return DeliveryReceipt{}, ErrNotFound
	}
	if receipt.VersionCAS != version {
		if receipt.State == state && receipt.AssociationID == associationID {
			return receipt, nil
		}
		return DeliveryReceipt{}, &Error{Code: CodeConflict, Operation: "delivery_cas"}
	}
	receipt.State, receipt.AssociationID, receipt.VersionCAS = state, associationID, version+1
	if err := s.commit(StoreEvent{Kind: "delivery_completed", At: time.Now().UTC(), Delivery: &receipt}); err != nil {
		return DeliveryReceipt{}, err
	}
	return receipt, nil
}

// ReserveAssociation enforces one logical PR/head/run relationship per ID.
func (s *LocalStore) ReserveAssociation(ctx context.Context, association Association) (Association, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	release, err := s.refreshLocked(ctx)
	if err != nil {
		return Association{}, false, err
	}
	defer release()
	if err := ctx.Err(); err != nil {
		return Association{}, false, err
	}
	if VerifyAssociation(association) != nil {
		return Association{}, false, &Error{Code: CodeIntegrity, Operation: "association"}
	}
	for _, prior := range s.state.Associations {
		if prior.DeliveryID != association.DeliveryID {
			continue
		}
		if prior.Snapshot.Fingerprint != association.Snapshot.Fingerprint || prior.SelectionAlias != association.SelectionAlias || prior.CommandActorID != association.CommandActorID {
			return Association{}, false, &Error{Code: CodeConflict, Operation: "association_delivery_identity"}
		}
		return prior, false, nil
	}
	if prior, ok := s.state.Associations[association.ID]; ok {
		if prior.ExternalCheckID != association.ExternalCheckID || prior.Snapshot.Fingerprint != association.Snapshot.Fingerprint {
			return Association{}, false, &Error{Code: CodeConflict, Operation: "association"}
		}
		return prior, false, nil
	}
	if association.ExternalCheckID != "" {
		for _, prior := range s.state.Associations {
			if prior.ExternalCheckID == association.ExternalCheckID {
				if prior.RunHandle == association.RunHandle && prior.Snapshot.Fingerprint == association.Snapshot.Fingerprint {
					return prior, false, nil
				}
				return Association{}, false, &Error{Code: CodeConflict, Operation: "association_run_identity"}
			}
		}
	}
	if err := s.commit(StoreEvent{Kind: "association_reserved", At: time.Now().UTC(), Association: &association}); err != nil {
		return Association{}, false, err
	}
	return association, true, nil
}

// GetAssociation verifies snapshot integrity on every resume.
func (s *LocalStore) GetAssociation(ctx context.Context, id string) (Association, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	release, err := s.refreshLocked(ctx)
	if err != nil {
		return Association{}, err
	}
	defer release()
	if err := ctx.Err(); err != nil {
		return Association{}, err
	}
	association, ok := s.state.Associations[id]
	if !ok {
		return Association{}, ErrNotFound
	}
	if err := VerifyAssociation(association); err != nil {
		return Association{}, err
	}
	return association, nil
}

// UpdateAssociation commits an owned mutation only at the expected snapshot version.
func (s *LocalStore) UpdateAssociation(ctx context.Context, association Association, version uint64) (Association, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	release, err := s.refreshLocked(ctx)
	if err != nil {
		return Association{}, err
	}
	defer release()
	if err := ctx.Err(); err != nil {
		return Association{}, err
	}
	prior, ok := s.state.Associations[association.ID]
	if !ok {
		return Association{}, ErrNotFound
	}
	if prior.VersionCAS != version || prior.ExternalCheckID != "" && prior.ExternalCheckID != association.ExternalCheckID || prior.Snapshot.Fingerprint != association.Snapshot.Fingerprint {
		return Association{}, &Error{Code: CodeConflict, Operation: "association_cas"}
	}
	if err := VerifyAssociation(prior); err != nil {
		return Association{}, err
	}
	if association.ExternalCheckID != "" {
		for id, existing := range s.state.Associations {
			if id != association.ID && existing.ExternalCheckID == association.ExternalCheckID {
				return Association{}, &Error{Code: CodeConflict, Operation: "association_run_identity"}
			}
		}
	}
	association.VersionCAS = version + 1
	association, err = SealAssociation(association)
	if err != nil {
		return Association{}, err
	}
	if err := s.commit(StoreEvent{Kind: "association_updated", At: time.Now().UTC(), Association: &association}); err != nil {
		return Association{}, err
	}
	return association, nil
}

// ClaimAssociation grants one live process the durable lease required before work or publication.
func (s *LocalStore) ClaimAssociation(ctx context.Context, id, owner string, now time.Time, ttl time.Duration) (Association, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	release, err := s.refreshLocked(ctx)
	if err != nil {
		return Association{}, false, err
	}
	defer release()
	association, ok := s.state.Associations[id]
	if !ok {
		return Association{}, false, ErrNotFound
	}
	if err := VerifyAssociation(association); err != nil {
		return Association{}, false, err
	}
	if association.Owner != "" && association.Owner != owner && association.LeaseUntil.After(now) {
		return association, false, nil
	}
	association.Owner = owner
	association.LeaseUntil = now.Add(ttl)
	association.VersionCAS++
	association, err = SealAssociation(association)
	if err != nil {
		return Association{}, false, err
	}
	if err = s.commit(StoreEvent{Kind: "association_claimed", At: now, Association: &association}); err != nil {
		return Association{}, false, err
	}
	return association, true, nil
}

// ReleaseAssociation clears only the current owner's lease after terminalization or safe suspension.
func (s *LocalStore) ReleaseAssociation(ctx context.Context, id, owner string, now time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	release, err := s.refreshLocked(ctx)
	if err != nil {
		return err
	}
	defer release()
	association, ok := s.state.Associations[id]
	if !ok {
		return ErrNotFound
	}
	if association.Owner != owner {
		return &Error{Code: CodeConflict, Operation: "association_owner"}
	}
	association.Owner = ""
	association.LeaseUntil = time.Time{}
	association.VersionCAS++
	association, err = SealAssociation(association)
	if err != nil {
		return err
	}
	return s.commit(StoreEvent{Kind: "association_released", At: now, Association: &association})
}

// PrepareIntent records publication identity and payload before a remote mutation.
func (s *LocalStore) PrepareIntent(ctx context.Context, intent PublicationIntent) (PublicationIntent, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	release, err := s.refreshLocked(ctx)
	if err != nil {
		return PublicationIntent{}, false, err
	}
	defer release()
	if err := ctx.Err(); err != nil {
		return PublicationIntent{}, false, err
	}
	if err := VerifyPublicationIntent(intent); err != nil {
		return PublicationIntent{}, false, err
	}
	intentRaw, err := json.Marshal(intent)
	if err != nil || len(intentRaw) >= maxIntentRecordBytes {
		return PublicationIntent{}, false, &Error{Code: CodeInvalid, Operation: "intent_size"}
	}
	key := intent.AssociationID + ":" + intent.Kind
	if prior, ok := s.state.Intents[key]; ok {
		if prior.PayloadSHA256 != intent.PayloadSHA256 || prior.ExpectedID != intent.ExpectedID {
			if prior.State != "confirmed" {
				return PublicationIntent{}, false, &Error{Code: CodeConflict, Operation: "intent"}
			}
		} else {
			if prior.State == "confirmed" {
				return prior, false, nil
			}
			if prior.Owner != intent.Owner && prior.LeaseUntil.After(intent.LeaseStartedAt) {
				return PublicationIntent{}, false, &Error{Code: CodeConflict, Operation: "intent_owner"}
			}
			intent.Version, intent.State = SchemaVersion, "prepared"
			if err := s.commit(StoreEvent{Kind: "intent_reclaimed", At: intent.LeaseStartedAt, Intent: &intent}); err != nil {
				return PublicationIntent{}, false, err
			}
			return intent, true, nil
		}
	}
	intent.Version, intent.State = SchemaVersion, "prepared"
	if err := s.commit(StoreEvent{Kind: "publication_prepared", At: time.Now().UTC(), Intent: &intent}); err != nil {
		return PublicationIntent{}, false, err
	}
	return intent, true, nil
}

// ConfirmIntent transitions a prepared mutation after identity-checked confirmation or reconciliation.
func (s *LocalStore) ConfirmIntent(ctx context.Context, associationID, kind, state string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	release, err := s.refreshLocked(ctx)
	if err != nil {
		return err
	}
	defer release()
	if err := ctx.Err(); err != nil {
		return err
	}
	key := associationID + ":" + kind
	intent, ok := s.state.Intents[key]
	if !ok {
		return ErrNotFound
	}
	if state != "confirmed" && state != "abandoned" {
		return &Error{Code: CodeInvalid, Operation: "intent"}
	}
	intent.State = state
	return s.commit(StoreEvent{Kind: "publication_" + state, At: time.Now().UTC(), Intent: &intent})
}

// ClaimPending atomically leases the next bounded recovery group without local supervisor exclusions.
func (s *LocalStore) ClaimPending(ctx context.Context, owner string, now time.Time, ttl time.Duration) ([]Association, []PublicationIntent, error) {
	return s.ClaimPendingExcept(ctx, owner, now, ttl, nil)
}

// ClaimPendingExcept leases the next bounded group while skipping IDs already handed to this coordinator's supervisor.
func (s *LocalStore) ClaimPendingExcept(ctx context.Context, owner string, now time.Time, ttl time.Duration, excluded map[string]bool) ([]Association, []PublicationIntent, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	release, err := s.refreshLocked(ctx)
	if err != nil {
		return nil, nil, err
	}
	defer release()
	if err := ctx.Err(); err != nil {
		return nil, nil, err
	}
	if owner == "" || ttl <= 0 {
		return nil, nil, &Error{Code: CodeInvalid, Operation: "pending_claim"}
	}
	associations := make([]Association, 0)
	claimedAssociations := make(map[string]bool)
	for _, value := range s.state.Associations {
		if excluded[value.ID] {
			continue
		}
		if value.Lifecycle != "terminal" && (value.Owner == "" || !value.LeaseUntil.After(now)) {
			if err := VerifyAssociation(value); err != nil {
				return nil, nil, err
			}
			value.Owner, value.LeaseUntil, value.VersionCAS = owner, now.Add(ttl), value.VersionCAS+1
			sealed, sealErr := SealAssociation(value)
			if sealErr != nil {
				return nil, nil, sealErr
			}
			associations = append(associations, sealed)
			claimedAssociations[sealed.ID] = true
		}
	}
	intents := make([]PublicationIntent, 0)
	for _, value := range s.state.Intents {
		if excluded[value.AssociationID] {
			continue
		}
		if value.State == "prepared" && (value.Owner == "" || !value.LeaseUntil.After(now)) {
			if err := VerifyPublicationIntent(value); err != nil {
				return nil, nil, err
			}
			association, exists := s.state.Associations[value.AssociationID]
			if !exists {
				orphan := value
				orphan.State, orphan.Owner, orphan.LeaseUntil = "abandoned", "", time.Time{}
				if err := s.commit(StoreEvent{Kind: "orphan_intent_abandoned", At: now, Intent: &orphan}); err != nil {
					return nil, nil, err
				}
				return nil, nil, &Error{Code: CodeIntegrity, Operation: "intent_association"}
			}
			if !claimedAssociations[value.AssociationID] {
				if association.Owner == owner && association.LeaseUntil.After(now) {
					value.Owner, value.LeaseUntil = owner, now.Add(ttl)
					intents = append(intents, value)
					continue
				}
				if association.Owner != "" && association.Owner != owner && association.LeaseUntil.After(now) {
					continue
				}
				if err := VerifyAssociation(association); err != nil {
					return nil, nil, err
				}
				association.Owner, association.LeaseUntil, association.VersionCAS = owner, now.Add(ttl), association.VersionCAS+1
				association, err = SealAssociation(association)
				if err != nil {
					return nil, nil, err
				}
				associations = append(associations, association)
				claimedAssociations[association.ID] = true
			}
			value.Owner, value.LeaseUntil = owner, now.Add(ttl)
			intents = append(intents, value)
		}
	}
	sort.Slice(associations, func(left, right int) bool { return associations[left].ID < associations[right].ID })
	sort.Slice(intents, func(left, right int) bool {
		if intents[left].AssociationID == intents[right].AssociationID {
			return intents[left].Kind < intents[right].Kind
		}
		return intents[left].AssociationID < intents[right].AssociationID
	})
	selectedAssociations := make([]Association, 0, 1)
	selectedIntents := make([]PublicationIntent, 0, 2)
	fits := func(candidateAssociations []Association, candidateIntents []PublicationIntent) bool {
		batch := ClaimBatch{Associations: candidateAssociations, Intents: candidateIntents}
		event := StoreEvent{Sequence: s.state.Sequence + 1, Kind: "pending_claimed", At: now, Claims: &batch}
		raw, marshalErr := json.Marshal(event)
		return marshalErr == nil && len(raw)+1 < maxJournalRecordBytes
	}
	workIDs := make([]string, 0, len(associations)+len(intents))
	seenWork := make(map[string]bool, len(associations)+len(intents))
	for _, association := range associations {
		if !seenWork[association.ID] {
			workIDs, seenWork[association.ID] = append(workIDs, association.ID), true
		}
	}
	for _, intent := range intents {
		if !seenWork[intent.AssociationID] {
			workIDs, seenWork[intent.AssociationID] = append(workIDs, intent.AssociationID), true
		}
	}
	sort.Strings(workIDs)
	if len(workIDs) > 0 {
		workID := workIDs[0]
		for _, association := range associations {
			if association.ID == workID {
				selectedAssociations = append(selectedAssociations, association)
				break
			}
		}
		for _, intent := range intents {
			if intent.AssociationID == workID {
				selectedIntents = append(selectedIntents, intent)
			}
		}
		if !fits(selectedAssociations, selectedIntents) {
			return nil, nil, &Error{Code: CodeInvalid, Operation: "claim_batch_size"}
		}
	}
	if len(selectedAssociations) > 0 || len(selectedIntents) > 0 {
		batch := ClaimBatch{Associations: selectedAssociations, Intents: selectedIntents}
		if err := s.commit(StoreEvent{Kind: "pending_claimed", At: now, Claims: &batch}); err != nil {
			return nil, nil, err
		}
	}
	return selectedAssociations, selectedIntents, nil
}

// Events returns a verified copy of the append-only audit sequence.
func (s *LocalStore) Events(ctx context.Context) ([]StoreEvent, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	release, err := acquireGitHubStoreLock(ctx, filepath.Join(s.dir, "store.lock"))
	if err != nil {
		return nil, err
	}
	defer release()
	file, err := os.Open(s.journal)
	if errors.Is(err, os.ErrNotExist) {
		return []StoreEvent{}, nil
	}
	if err != nil {
		return nil, err
	}
	defer file.Close()
	return scanEvents(ctx, file)
}

// refreshLocked serializes processes and rebuilds current state before each CAS decision.
func (s *LocalStore) refreshLocked(ctx context.Context) (func(), error) {
	release, err := acquireGitHubStoreLock(ctx, filepath.Join(s.dir, "store.lock"))
	if err != nil {
		return nil, err
	}
	s.state = storeSnapshot{Deliveries: map[string]DeliveryReceipt{}, Associations: map[string]Association{}, Intents: map[string]PublicationIntent{}}
	if err := s.replay(); err != nil {
		release()
		return nil, err
	}
	return release, nil
}

// commit appends and fsyncs the source event before atomically publishing its snapshot.
func (s *LocalStore) commit(event StoreEvent) error {
	event.Sequence = s.state.Sequence + 1
	raw, err := json.Marshal(event)
	if err != nil {
		return err
	}
	if s.beforeCommit != nil {
		if err := s.beforeCommit(event); err != nil {
			return err
		}
	}
	line := append(raw, '\n')
	if len(line) >= maxJournalRecordBytes {
		return &Error{Code: CodeInvalid, Operation: "journal_record_size"}
	}
	file, err := os.OpenFile(s.journal, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	start, seekErr := file.Seek(0, io.SeekEnd)
	if seekErr != nil {
		_ = file.Close()
		return seekErr
	}
	written, writeErr := file.Write(line)
	if writeErr == nil && written != len(line) {
		writeErr = io.ErrShortWrite
	}
	if writeErr == nil {
		err = file.Sync()
	} else {
		err = writeErr
	}
	closeErr := file.Close()
	if err == nil {
		err = closeErr
	}
	if err != nil {
		_ = os.Truncate(s.journal, start)
		return err
	}
	applyEvent(&s.state, event)
	tmp := s.snapshot + ".tmp"
	snapshotRaw, err := json.Marshal(s.state)
	if err != nil {
		return err
	}
	if err = os.WriteFile(tmp, snapshotRaw, 0o600); err != nil {
		_ = os.Remove(tmp)
		return nil
	}
	if err = os.Rename(tmp, s.snapshot); err != nil {
		_ = os.Remove(tmp)
	}
	return nil
}

// replay rebuilds authoritative state exclusively from ordered journal events.
func (s *LocalStore) replay() error {
	file, err := os.Open(s.journal)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	defer file.Close()
	events, err := scanEvents(context.Background(), file)
	if err != nil {
		return err
	}
	for _, event := range events {
		applyEvent(&s.state, event)
	}
	return nil
}

// scanEvents rejects malformed, oversized, or discontinuous journal records.
func scanEvents(ctx context.Context, reader *os.File) ([]StoreEvent, error) {
	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 4096), maxJournalRecordBytes)
	events := []StoreEvent{}
	var sequence uint64
	for scanner.Scan() {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		var event StoreEvent
		decoder := json.NewDecoder(bytes.NewReader(scanner.Bytes()))
		decoder.DisallowUnknownFields()
		if decoder.Decode(&event) != nil || event.Sequence != sequence+1 {
			return nil, &Error{Code: CodeIntegrity, Operation: "journal"}
		}
		sequence = event.Sequence
		events = append(events, event)
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	return events, nil
}

// applyEvent derives the latest snapshots from one already-validated event.
func applyEvent(state *storeSnapshot, event StoreEvent) {
	state.Sequence = event.Sequence
	if event.Delivery != nil {
		state.Deliveries[event.Delivery.DeliveryID] = *event.Delivery
	}
	if event.Association != nil {
		state.Associations[event.Association.ID] = *event.Association
	}
	if event.Intent != nil {
		state.Intents[event.Intent.AssociationID+":"+event.Intent.Kind] = *event.Intent
	}
	if event.Claims != nil {
		for _, association := range event.Claims.Associations {
			state.Associations[association.ID] = association
		}
		for _, intent := range event.Claims.Intents {
			state.Intents[intent.AssociationID+":"+intent.Kind] = intent
		}
	}
}
