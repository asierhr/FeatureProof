package environment

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/asierhr/featureProof/internal/controlplane"
)

// Registry is a composition-owned immutable provider lookup.
type Registry struct {
	providers map[string]EnvironmentProvider
}

// NewRegistry validates provider/version keys and rejects duplicate or nil adapters.
func NewRegistry(values map[string]EnvironmentProvider) (*Registry, error) {
	if len(values) == 0 {
		return nil, &Error{Category: CategoryInvalid, Operation: "registry"}
	}
	providers := make(map[string]EnvironmentProvider, len(values))
	for key, provider := range values {
		parts := strings.Split(key, "@")
		if len(parts) != 2 || !safeID.MatchString(parts[0]) || !safeID.MatchString(parts[1]) || provider == nil {
			return nil, &Error{Category: CategoryInvalid, Operation: "registry_entry"}
		}
		providers[key] = provider
	}
	return &Registry{providers: providers}, nil
}

// Resolve returns only the exact registered provider version.
func (r *Registry) Resolve(providerID, version string) (EnvironmentProvider, error) {
	if r == nil {
		return nil, &Error{Category: CategoryUnauthorized, Operation: "provider"}
	}
	provider := r.providers[providerID+"@"+version]
	if provider == nil {
		return nil, &Error{Category: CategoryUnauthorized, Operation: "provider"}
	}
	return provider, nil
}

// MemoryStore provides race-safe deterministic lifecycle CAS for contract tests.
type MemoryStore struct {
	mu      sync.Mutex
	records map[string]Record
	events  map[string][]Event
}

// NewMemoryStore constructs an empty in-memory lifecycle store.
func NewMemoryStore() *MemoryStore {
	return &MemoryStore{records: map[string]Record{}, events: map[string][]Event{}}
}

// Reserve establishes one immutable request identity or returns its redelivery.
func (s *MemoryStore) Reserve(ctx context.Context, candidate Record) (Record, bool, error) {
	if err := ctx.Err(); err != nil {
		return Record{}, false, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if current, ok := s.records[candidate.RunID]; ok {
		return cloneRecord(current), false, nil
	}
	candidate.Revision = 1
	if validateRecord(candidate, candidate.RunID, candidate.Revision) != nil {
		return Record{}, false, &Error{Category: CategoryIntegrity, Operation: "store_record"}
	}
	s.records[candidate.RunID] = cloneRecord(candidate)
	return cloneRecord(candidate), true, nil
}

// Load returns a detached current record.
func (s *MemoryStore) Load(ctx context.Context, runID string) (Record, error) {
	if err := ctx.Err(); err != nil {
		return Record{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	record, ok := s.records[runID]
	if !ok {
		return Record{}, os.ErrNotExist
	}
	return cloneRecord(record), nil
}

// Save applies one version CAS and appends one unique event atomically in memory.
func (s *MemoryStore) Save(ctx context.Context, candidate Record, expected uint64, event Event) (Record, error) {
	if err := ctx.Err(); err != nil {
		return Record{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	current, ok := s.records[candidate.RunID]
	if ok && current.Revision == expected+1 {
		candidate.Revision = expected + 1
		if recordDigest(current) == recordDigest(candidate) {
			return cloneRecord(current), nil
		}
	}
	if !ok || current.Revision != expected {
		return Record{}, &Error{Category: CategoryConflict, Operation: "store_cas"}
	}
	if !allowedRecordTransition(current.State, candidate.State) {
		return Record{}, &Error{Category: CategoryConflict, Operation: "store_transition"}
	}
	candidate.Revision = expected + 1
	if validateRecord(candidate, candidate.RunID, candidate.Revision) != nil || validateEvent(event, candidate.RunID) != nil {
		return Record{}, &Error{Category: CategoryIntegrity, Operation: "store_record"}
	}
	s.records[candidate.RunID] = cloneRecord(candidate)
	s.events[candidate.RunID] = append(s.events[candidate.RunID], event)
	return cloneRecord(candidate), nil
}

// Events returns detached append-only lifecycle observations.
func (s *MemoryStore) Events(ctx context.Context, runID string) ([]Event, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]Event(nil), s.events[runID]...), nil
}

// LocalStore persists immutable version receipts and one rollback-resistant head without holding locks during I/O.
type LocalStore struct {
	root  string
	fault func(string) error
}

// OpenLocalStore validates or creates the private lifecycle root.
func OpenLocalStore(root string) (*LocalStore, error) {
	absolute, err := filepath.Abs(root)
	if err != nil || root == "" {
		return nil, &Error{Category: CategoryInvalid, Operation: "store_root"}
	}
	if err = os.MkdirAll(filepath.Join(absolute, "records"), 0o700); err != nil {
		return nil, &Error{Category: CategoryPersistence, Operation: "store_root", Cause: err}
	}
	if err = os.MkdirAll(filepath.Join(absolute, "events"), 0o700); err != nil {
		return nil, &Error{Category: CategoryPersistence, Operation: "store_root", Cause: err}
	}
	return &LocalStore{root: absolute}, nil
}

// Reserve publishes revision one with exclusive creation and converges redelivery.
func (s *LocalStore) Reserve(ctx context.Context, candidate Record) (Record, bool, error) {
	if err := ctx.Err(); err != nil {
		return Record{}, false, err
	}
	if !safeID.MatchString(candidate.RunID) {
		return Record{}, false, &Error{Category: CategoryInvalid, Operation: "reserve"}
	}
	if current, err := s.Load(ctx, candidate.RunID); err == nil {
		return current, false, nil
	}
	candidate.Revision = 1
	created, err := s.publishVersion(candidate, Event{})
	if err == nil && created {
		return cloneRecord(candidate), true, nil
	}
	if err != nil {
		return Record{}, false, err
	}
	current, loadErr := s.Load(ctx, candidate.RunID)
	if loadErr != nil {
		return Record{}, false, loadErr
	}
	return current, false, nil
}

// Load reads and verifies the highest immutable version receipt.
func (s *LocalStore) Load(ctx context.Context, runID string) (Record, error) {
	if err := ctx.Err(); err != nil {
		return Record{}, err
	}
	head, err := s.readHead(runID)
	if err != nil {
		return Record{}, err
	}
	versions, err := s.versions(runID)
	if err != nil || len(versions) < int(head.Revision) || len(versions) > int(head.Revision)+1 {
		return Record{}, &Error{Category: CategoryIntegrity, Operation: "record_sequence", Cause: err}
	}
	previousDigest := ""
	for revision := uint64(1); revision <= head.Revision; revision++ {
		if int(revision-1) >= len(versions) || versions[revision-1] != revision {
			return Record{}, &Error{Category: CategoryIntegrity, Operation: "record_sequence"}
		}
		wrapper, readErr := s.readVersionWrapper(runID, revision)
		if readErr != nil || wrapper.PreviousDigest != previousDigest {
			return Record{}, &Error{Category: CategoryIntegrity, Operation: "record_chain", Cause: readErr}
		}
		if wrapper.EventID != "" {
			eventRaw, eventErr := os.ReadFile(filepath.Join(s.root, "events", runID, wrapper.EventID+".json"))
			var event Event
			if eventErr != nil || strictDecode(eventRaw, &event) != nil || event.Digest != wrapper.EventDigest || validateEvent(event, runID) != nil {
				return Record{}, &Error{Category: CategoryIntegrity, Operation: "record_event", Cause: eventErr}
			}
		}
		previousDigest = wrapper.Digest
	}
	if previousDigest != head.RecordDigest {
		return Record{}, &Error{Category: CategoryIntegrity, Operation: "record_head"}
	}
	return s.readVersion(runID, head.Revision)
}

// Save publishes exactly expected+1 and rejects conflicting concurrent candidates.
func (s *LocalStore) Save(ctx context.Context, candidate Record, expected uint64, event Event) (Record, error) {
	if err := ctx.Err(); err != nil {
		return Record{}, err
	}
	current, err := s.Load(ctx, candidate.RunID)
	if err == nil && current.Revision == expected+1 {
		candidate.Revision = expected + 1
		if recordDigest(current) == recordDigest(candidate) {
			if event.ID != "" {
				if eventErr := s.publishEvent(candidate.RunID, event); eventErr != nil {
					return Record{}, eventErr
				}
			}
			return current, nil
		}
	}
	if err != nil || current.Revision != expected {
		return Record{}, &Error{Category: CategoryConflict, Operation: "store_cas", Cause: err}
	}
	if !allowedRecordTransition(current.State, candidate.State) {
		return Record{}, &Error{Category: CategoryConflict, Operation: "store_transition"}
	}
	candidate.Revision = expected + 1
	created, err := s.publishVersion(candidate, event)
	if err != nil {
		return Record{}, err
	}
	if !created {
		winner, loadErr := s.readVersion(candidate.RunID, candidate.Revision)
		if loadErr == nil && recordDigest(winner) == recordDigest(candidate) {
			return winner, nil
		}
		return Record{}, &Error{Category: CategoryConflict, Operation: "store_cas"}
	}
	return cloneRecord(candidate), nil
}

// Events reconstructs ordered content-free events from immutable event files.
func (s *LocalStore) Events(ctx context.Context, runID string) ([]Event, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if !safeID.MatchString(runID) {
		return nil, &Error{Category: CategoryInvalid, Operation: "events"}
	}
	entries, err := os.ReadDir(filepath.Join(s.root, "events", runID))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	events := make([]Event, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".json" {
			continue
		}
		raw, readErr := os.ReadFile(filepath.Join(s.root, "events", runID, entry.Name()))
		var event Event
		if readErr != nil || strictDecode(raw, &event) != nil || validateEvent(event, runID) != nil {
			return nil, &Error{Category: CategoryIntegrity, Operation: "event"}
		}
		expected := event.Digest
		event.Digest = ""
		if digestOf(event) != expected {
			return nil, &Error{Category: CategoryIntegrity, Operation: "event"}
		}
		event.Digest = expected
		events = append(events, event)
	}
	sort.Slice(events, func(i, j int) bool {
		return events[i].OccurredAt.Before(events[j].OccurredAt) || events[i].OccurredAt.Equal(events[j].OccurredAt) && events[i].ID < events[j].ID
	})
	return events, nil
}

// publishVersion atomically claims one canonical revision path using hard-link publication.
func (s *LocalStore) publishVersion(record Record, event Event) (bool, error) {
	releaseImportWrite, err := controlplane.AcquireLocalImportWrite(context.Background(), s.root)
	if err != nil {
		return false, &Error{Category: CategoryConflict, Operation: "offline_import_freeze", Cause: err}
	}
	defer releaseImportWrite()
	if err := validateRecord(record, record.RunID, record.Revision); err != nil {
		return false, err
	}
	directory := filepath.Join(s.root, "records", record.RunID)
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return false, err
	}
	previousDigest := ""
	if record.Revision > 1 {
		previous, err := s.readVersionWrapper(record.RunID, record.Revision-1)
		if err != nil {
			return false, &Error{Category: CategoryIntegrity, Operation: "record_previous", Cause: err}
		}
		previousDigest = previous.Digest
	}
	wrapper := durableRecord{Digest: recordDigest(record), PreviousDigest: previousDigest, Record: record}
	if event.ID != "" {
		if err := validateEvent(event, record.RunID); err != nil {
			return false, err
		}
		wrapper.EventID, wrapper.EventDigest = event.ID, event.Digest
	}
	raw, err := json.Marshal(wrapper)
	if err != nil {
		return false, err
	}
	temporary, err := os.CreateTemp(directory, ".candidate-*")
	if err != nil {
		return false, err
	}
	temporaryPath := temporary.Name()
	defer func() { _ = os.Remove(temporaryPath) }()
	if chmodErr := temporary.Chmod(0o600); chmodErr != nil {
		_ = temporary.Close()
		return false, chmodErr
	}
	if _, err = temporary.Write(raw); err != nil {
		_ = temporary.Close()
		return false, err
	}
	if err = temporary.Sync(); err != nil {
		_ = temporary.Close()
		return false, err
	}
	if err = temporary.Close(); err != nil {
		return false, err
	}
	canonical := filepath.Join(directory, fmt.Sprintf("%020d.json", record.Revision))
	created := true
	if err = os.Link(temporaryPath, canonical); err != nil {
		if errors.Is(err, os.ErrExist) {
			created = false
			existing, readErr := s.readVersionWrapper(record.RunID, record.Revision)
			if readErr != nil || existing.Digest != wrapper.Digest || existing.PreviousDigest != wrapper.PreviousDigest || existing.EventID != wrapper.EventID || existing.EventDigest != wrapper.EventDigest {
				return false, &Error{Category: CategoryConflict, Operation: "record_publish", Cause: readErr}
			}
		} else {
			return false, err
		}
	} else if err = syncEnvironmentDirectory(directory); err != nil {
		return created, err
	}
	if err = s.fail("record_published"); err != nil {
		return created, err
	}
	if event.ID != "" {
		if err = s.publishEvent(record.RunID, event); err != nil {
			return created, &Error{Category: CategoryPersistence, Operation: "event_publish", Cause: err}
		}
	}
	if err = s.fail("event_published"); err != nil {
		return created, err
	}
	if err = s.publishHead(wrapper); err != nil {
		return created, err
	}
	if err = s.fail("head_published"); err != nil {
		return created, err
	}
	return created, nil
}

// publishEvent writes one content-free event under its stable identity.
func (s *LocalStore) publishEvent(runID string, event Event) error {
	if err := validateEvent(event, runID); err != nil {
		return err
	}
	directory := filepath.Join(s.root, "events", runID)
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return err
	}
	raw, err := json.Marshal(event)
	if err != nil {
		return err
	}
	path := filepath.Join(directory, event.ID+".json")
	temporary, err := os.CreateTemp(directory, ".event-*")
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	defer func() { _ = os.Remove(temporaryPath) }()
	if err = temporary.Chmod(0o600); err == nil {
		_, err = temporary.Write(raw)
	}
	if err == nil {
		err = temporary.Sync()
	}
	if closeErr := temporary.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	if err = os.Link(temporaryPath, path); errors.Is(err, os.ErrExist) {
		existing, readErr := os.ReadFile(path)
		if readErr == nil && string(existing) == string(raw) {
			return nil
		}
		return &Error{Category: CategoryConflict, Operation: "event"}
	}
	if err != nil {
		return err
	}
	return syncEnvironmentDirectory(directory)
}

// versions enumerates only canonical numeric immutable receipts.
func (s *LocalStore) versions(runID string) ([]uint64, error) {
	if !safeID.MatchString(runID) {
		return nil, &Error{Category: CategoryInvalid, Operation: "versions"}
	}
	entries, err := os.ReadDir(filepath.Join(s.root, "records", runID))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	versions := []uint64{}
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".json" {
			continue
		}
		version, parseErr := strconv.ParseUint(strings.TrimSuffix(entry.Name(), ".json"), 10, 64)
		if parseErr != nil {
			continue
		}
		versions = append(versions, version)
	}
	sort.Slice(versions, func(i, j int) bool { return versions[i] < versions[j] })
	return versions, nil
}

// readVersion verifies one immutable record wrapper before returning it.
func (s *LocalStore) readVersion(runID string, version uint64) (Record, error) {
	wrapper, err := s.readVersionWrapper(runID, version)
	if err != nil {
		return Record{}, err
	}
	return cloneRecord(wrapper.Record), nil
}

// readVersionWrapper verifies one immutable chained record and returns its relationship metadata.
func (s *LocalStore) readVersionWrapper(runID string, version uint64) (durableRecord, error) {
	raw, err := os.ReadFile(filepath.Join(s.root, "records", runID, fmt.Sprintf("%020d.json", version)))
	if err != nil {
		return durableRecord{}, err
	}
	var wrapper durableRecord
	if strictDecode(raw, &wrapper) != nil || validateRecord(wrapper.Record, runID, version) != nil || wrapper.Digest != recordDigest(wrapper.Record) {
		return durableRecord{}, &Error{Category: CategoryIntegrity, Operation: "record"}
	}
	return wrapper, nil
}

// durableRecord binds private lifecycle bytes to one integrity digest.
type durableRecord struct {
	Digest         string `json:"digest"`
	PreviousDigest string `json:"previous_digest,omitempty"`
	EventID        string `json:"event_id,omitempty"`
	EventDigest    string `json:"event_digest,omitempty"`
	Record         Record `json:"record"`
}

type durableHead struct {
	RunID, RecordDigest, PreviousDigest, Digest string
	Revision                                    uint64
}

// publishHead advances the rollback witness only after record and event durability is confirmed.
func (s *LocalStore) publishHead(wrapper durableRecord) error {
	directory := filepath.Join(s.root, "records", wrapper.Record.RunID)
	current, err := s.readHead(wrapper.Record.RunID)
	if err == nil {
		if current.Revision == wrapper.Record.Revision && current.RecordDigest == wrapper.Digest {
			return nil
		}
		if current.Revision+1 != wrapper.Record.Revision || current.RecordDigest != wrapper.PreviousDigest {
			return &Error{Category: CategoryConflict, Operation: "head_cas"}
		}
	} else if !errors.Is(err, os.ErrNotExist) || wrapper.Record.Revision != 1 {
		return &Error{Category: CategoryIntegrity, Operation: "head_previous", Cause: err}
	}
	head := durableHead{RunID: wrapper.Record.RunID, Revision: wrapper.Record.Revision, RecordDigest: wrapper.Digest, PreviousDigest: wrapper.PreviousDigest}
	head.Digest = digestOf(head)
	raw, err := json.Marshal(head)
	if err != nil {
		return err
	}
	temporary, err := os.CreateTemp(directory, ".head-*")
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	defer func() { _ = os.Remove(temporaryPath) }()
	if err = temporary.Chmod(0o600); err == nil {
		_, err = temporary.Write(raw)
	}
	if err == nil {
		err = temporary.Sync()
	}
	if closeErr := temporary.Close(); err == nil {
		err = closeErr
	}
	if err == nil {
		err = os.Rename(temporaryPath, filepath.Join(directory, "head.json"))
	}
	if err != nil {
		return err
	}
	return syncEnvironmentDirectory(directory)
}

// readHead verifies the stable rollback witness independently of record enumeration.
func (s *LocalStore) readHead(runID string) (durableHead, error) {
	raw, err := os.ReadFile(filepath.Join(s.root, "records", runID, "head.json"))
	if err != nil {
		return durableHead{}, err
	}
	var head durableHead
	if strictDecode(raw, &head) != nil || head.RunID != runID || head.Revision == 0 || !digestPattern.MatchString(head.RecordDigest) {
		return durableHead{}, &Error{Category: CategoryIntegrity, Operation: "head"}
	}
	expected := head.Digest
	head.Digest = ""
	if !digestPattern.MatchString(expected) || digestOf(head) != expected {
		return durableHead{}, &Error{Category: CategoryIntegrity, Operation: "head"}
	}
	head.Digest = expected
	return head, nil
}

// fail injects one deterministic crash boundary only in store contract tests.
func (s *LocalStore) fail(boundary string) error {
	if s.fault == nil {
		return nil
	}
	return s.fault(boundary)
}

// recordDigest fingerprints the complete private lifecycle record.
func recordDigest(value Record) string { return digestOf(value) }

// validateRecord rejects self-signed private records whose lifecycle relationships are impossible.
func validateRecord(value Record, runID string, revision uint64) error {
	sealed, selectionErr := SealSelection(value.Selection)
	ownerValid := value.Owner == "" && value.LeaseUntil.IsZero() || safeID.MatchString(value.Owner) && !value.LeaseUntil.IsZero()
	if value.Version != 1 || value.RunID != runID || value.Revision != revision || !safeID.MatchString(value.RunID) || !digestPattern.MatchString(value.RequestDigest) || !digestPattern.MatchString(value.AcquisitionKey) || !fullOID.MatchString(value.CommitOID) || selectionErr != nil || sealed.Digest != value.Selection.Digest || !validLifecycleState(value.State) || !ownerValid {
		return &Error{Category: CategoryIntegrity, Operation: "record"}
	}
	if value.Selection.Kind == KindManaged {
		if !safeID.MatchString(value.ProjectID) || value.ProjectID != value.Selection.ProjectID || value.CreatedAt.IsZero() || value.TTL <= 0 || value.CreatedAt.Add(value.TTL) != value.ExpiresAt || value.Demand.Environments != 1 || !safeID.MatchString(value.CapacityReceipt.ID) || !digestPattern.MatchString(value.CapacityReceipt.Digest) {
			return &Error{Category: CategoryIntegrity, Operation: "record_capacity"}
		}
		if value.LifecycleHandle != "" && (!digestPattern.MatchString(value.CapacityEnvironmentDigest) || !digestPattern.MatchString(value.CapacityResourceDigest)) {
			return &Error{Category: CategoryIntegrity, Operation: "record_capacity_identity"}
		}
	} else if value.ProjectID != "" || !value.CreatedAt.IsZero() || !value.ExpiresAt.IsZero() || value.TTL != 0 || value.Demand != (ResourceDemand{}) || value.CapacityReceipt != (CapacityReceipt{}) {
		return &Error{Category: CategoryIntegrity, Operation: "record_capacity"}
	}
	hasLifecycle := value.LifecycleHandle != "" && safeID.MatchString(value.EnvironmentID) && digestPattern.MatchString(value.LifecycleDigest) && digestPattern.MatchString(value.ReleaseKey)
	hasTarget := value.Target.Version != ""
	if value.LifecycleHandle != "" && !hasLifecycle {
		return &Error{Category: CategoryIntegrity, Operation: "record_lifecycle"}
	}
	switch value.State {
	case StateSelected:
		if value.ReleaseRequired || value.LifecycleHandle != "" || hasTarget {
			return &Error{Category: CategoryIntegrity, Operation: "record_selected"}
		}
	case StateAcquiring:
		if !value.ReleaseRequired || hasTarget {
			return &Error{Category: CategoryIntegrity, Operation: "record_acquiring"}
		}
	case StateAcquired, StateWaiting, StateReady:
		if !value.ReleaseRequired || !hasLifecycle || hasTarget {
			return &Error{Category: CategoryIntegrity, Operation: "record_acquired"}
		}
	case StateBound:
		if !value.ReleaseRequired || !hasLifecycle || !hasTarget {
			return &Error{Category: CategoryIntegrity, Operation: "record_bound"}
		}
	case StateReleaseIntent, StateReleasePending:
		knownLifecycle := hasLifecycle && digestPattern.MatchString(value.ReleaseKey)
		uncertainLifecycle := value.State == StateReleasePending && value.EnvironmentID == "" && value.LifecycleHandle == "" && value.LifecycleDigest == "" && value.ReleaseKey == ""
		if !value.ReleaseRequired || (!knownLifecycle && !uncertainLifecycle) || value.ReleaseConfirmed {
			return &Error{Category: CategoryIntegrity, Operation: "record_release"}
		}
	case StateReleased:
		if !value.ReleaseRequired || !value.ReleaseConfirmed || !digestPattern.MatchString(value.LifecycleDigest) || !digestPattern.MatchString(value.ReleaseKey) || value.Owner != "" {
			return &Error{Category: CategoryIntegrity, Operation: "record_released"}
		}
	}
	if hasTarget {
		if VerifyTarget(value.Target, time.Unix(0, 0).UTC()) != nil || VerifyBinding(value.Binding, value.Target, time.Unix(0, 0).UTC()) != nil || value.Target.RunID != value.RunID || value.Target.SelectionDigest != value.Selection.Digest || value.Target.LifecycleDigest != value.LifecycleDigest {
			return &Error{Category: CategoryIntegrity, Operation: "record_target"}
		}
	}
	return nil
}

// ValidateStoreRecord exposes the consumer-owned lifecycle invariant to infrastructure adapters.
func ValidateStoreRecord(value Record, runID string, revision uint64) error {
	return validateRecord(value, runID, revision)
}

// allowedRecordTransition is the complete lifecycle state machine including same-state lease renewals.
func allowedRecordTransition(from, to LifecycleState) bool {
	if from == to {
		return true
	}
	switch from {
	case StateSelected:
		return to == StateAcquiring
	case StateAcquiring:
		return to == StateAcquired || to == StateReleaseIntent || to == StateReleasePending
	case StateAcquired:
		return to == StateWaiting || to == StateReleaseIntent
	case StateWaiting:
		return to == StateReady || to == StateReleaseIntent
	case StateReady:
		return to == StateBound || to == StateReleaseIntent
	case StateBound:
		return to == StateReleaseIntent
	case StateReleaseIntent:
		return to == StateReleasePending || to == StateReleased
	case StateReleasePending:
		return to == StateReleaseIntent || to == StateReleased || to == StateReleasePending
	}
	return false
}

// AllowedStoreTransition exposes the closed lifecycle CAS matrix to persistence adapters.
func AllowedStoreTransition(from, to LifecycleState) bool { return allowedRecordTransition(from, to) }

// validLifecycleState accepts only the closed durable transition vocabulary.
func validLifecycleState(value LifecycleState) bool {
	switch value {
	case StateSelected, StateAcquiring, StateAcquired, StateWaiting, StateReady, StateBound, StateReleaseIntent, StateReleasePending, StateReleased:
		return true
	default:
		return false
	}
}

// validateEvent verifies one content-free event identity and complete digest.
func validateEvent(value Event, runID string) error {
	expected := value.Digest
	value.Digest = ""
	if !digestPattern.MatchString(expected) || !digestPattern.MatchString(value.ID) || value.RunID != runID || !safeID.MatchString(value.RunID) || !digestPattern.MatchString(value.SelectionDigest) || !safeID.MatchString(value.ProviderID) || !digestPattern.MatchString(value.EnvironmentDigest) || !validLifecycleState(value.State) || value.Attempt < 0 || value.Duration < 0 || value.OccurredAt.IsZero() || digestOf(value) != expected {
		return &Error{Category: CategoryIntegrity, Operation: "event"}
	}
	return nil
}

// SealStoreEvent binds one content-free lifecycle event to the exact run and
// consumer-owned digest schema used by every persistence backend.
func SealStoreEvent(value Event, runID string) (Event, error) {
	if value.RunID == "" {
		value.RunID = runID
	}
	if value.RunID != runID {
		return Event{}, &Error{Category: CategoryIntegrity, Operation: "event"}
	}
	value.Digest = ""
	value.Digest = digestOf(value)
	if err := validateEvent(value, runID); err != nil {
		return Event{}, err
	}
	return value, nil
}

// cloneRecord detaches slices nested in safe target authority.
func cloneRecord(value Record) Record {
	value.Selection.Capabilities = append([]string(nil), value.Selection.Capabilities...)
	value.Target.Capabilities = append([]string(nil), value.Target.Capabilities...)
	value.Target.Network.Schemes = append([]string(nil), value.Target.Network.Schemes...)
	value.Target.Network.Hosts = append([]string(nil), value.Target.Network.Hosts...)
	value.Target.Network.Ports = append([]int(nil), value.Target.Network.Ports...)
	value.Target.Network.BasePaths = append([]string(nil), value.Target.Network.BasePaths...)
	return value
}

// WallClock provides production UTC time and cancellable waiting.
type WallClock struct{}

// Now returns current UTC lifecycle time.
func (WallClock) Now() time.Time { return time.Now().UTC() }

// Wait blocks only until the duration or caller cancellation.
func (WallClock) Wait(ctx context.Context, duration time.Duration) error {
	timer := time.NewTimer(duration)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return context.Cause(ctx)
	case <-timer.C:
		return nil
	}
}

// AllowlistedNetworkAuthorizer accepts only endpoints already covered by the target policy.
type AllowlistedNetworkAuthorizer struct{}

// Authorize reuses the same pure allowlist validation applied during target sealing.
func (AllowlistedNetworkAuthorizer) Authorize(ctx context.Context, policy NetworkPolicy, endpoint string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	parsed, err := parseEndpoint(endpoint)
	if err != nil || !validNetwork(policy, parsed) {
		return &Error{Category: CategoryUnauthorized, Operation: "network"}
	}
	return nil
}

// parseEndpoint centralizes provider endpoint syntax without opening DNS or sockets.
func parseEndpoint(value string) (*url.URL, error) { return url.Parse(value) }

// MemoryObserver stores detached content-free events for deterministic verification.
type MemoryObserver struct {
	mu     sync.Mutex
	events []Event
	err    error
}

// Observe records one event or returns the configured operational failure.
func (o *MemoryObserver) Observe(ctx context.Context, event Event) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.err != nil {
		return o.err
	}
	o.events = append(o.events, event)
	return nil
}

// Snapshot returns detached observed events for tests and telemetry adapters.
func (o *MemoryObserver) Snapshot() []Event {
	o.mu.Lock()
	defer o.mu.Unlock()
	return append([]Event(nil), o.events...)
}
