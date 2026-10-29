package functionalflow

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"time"

	"github.com/asierhr/featureProof/internal/controlplane"
)

// safeStoreID constrains run and step coordinates to flat traversal-free durable names.
var safeStoreID = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)

// storeClaim is the authenticated finite ownership lease retained only by the private store.
type storeClaim struct {
	Owner, Token string
	ExpiresAt    time.Time
}

// durableState is the authenticated CAS snapshot for claims, intents, and terminal records of one run.
type durableState struct {
	Version   uint64
	MAC       string
	Claims    map[string]storeClaim
	Intents   map[string]Intent
	Terminals map[string]StepRecord
	Events    []Event
}

// AppendEvent atomically extends one authenticated digest-chained causal journal.
func (s *LocalStore) AppendEvent(ctx context.Context, event Event) (Event, error) {
	if !validStoreCoordinate(event.RunID) || event.FlowID == "" || event.Revision == 0 || event.Type == "" || !validDigest(event.ArtifactDigest) {
		return Event{}, flowError(CategoryInvalid, "store.event", nil)
	}
	release, err := s.lock(ctx, event.RunID)
	if err != nil {
		return Event{}, err
	}
	defer release()
	state, err := s.loadOrInitialize(event.RunID)
	if err != nil {
		return Event{}, err
	}
	if len(state.Events) > 0 {
		prior := state.Events[len(state.Events)-1]
		if prior.FlowID != event.FlowID || prior.Revision != event.Revision || prior.ArtifactDigest != event.ArtifactDigest {
			return Event{}, flowError(CategoryIntegrity, "store.event.binding", nil)
		}
		candidate := event
		candidate.Sequence, candidate.PreviousDigest, candidate.Digest = prior.Sequence, prior.PreviousDigest, prior.Digest
		if digestValue(candidate) == digestValue(prior) {
			return prior, nil
		}
		event.PreviousDigest = prior.Digest
	}
	event.Sequence = uint64(len(state.Events) + 1)
	event.EvidenceIDs = canonicalEvidenceIDs(event.EvidenceIDs)
	event.Digest = eventDigest(event)
	state.Events = append(state.Events, event)
	if err = s.save(event.RunID, state); err != nil {
		return Event{}, err
	}
	return event, nil
}

// Events returns a detached verified journal in its durable append order.
func (s *LocalStore) Events(ctx context.Context, runID string) ([]Event, error) {
	release, err := s.lock(ctx, runID)
	if err != nil {
		return nil, err
	}
	defer release()
	state, err := s.loadOrInitialize(runID)
	if err != nil {
		return nil, err
	}
	out := append([]Event(nil), state.Events...)
	for i, event := range out {
		previous := ""
		if i > 0 {
			previous = out[i-1].Digest
		}
		if event.Sequence != uint64(i+1) || event.PreviousDigest != previous || event.Digest != eventDigest(event) {
			return nil, flowError(CategoryIntegrity, "store.events", nil)
		}
		out[i].EvidenceIDs = append([]string(nil), event.EvidenceIDs...)
	}
	return out, nil
}

// LocalStore implements cross-process leases and atomic durable flow checkpoints under one trusted root.
type LocalStore struct {
	root     string
	clock    Clock
	leaseTTL time.Duration
}

// NewLocalStore validates explicit ownership configuration without performing filesystem effects.
func NewLocalStore(root string, clock Clock, leaseTTL time.Duration) (*LocalStore, error) {
	if root == "" || clock == nil || leaseTTL <= 0 {
		return nil, flowError(CategoryInternal, "store.config", nil)
	}
	return &LocalStore{root: filepath.Clean(root), clock: clock, leaseTTL: leaseTTL}, nil
}

// Claim atomically acquires an absent or expired step lease and returns its private confirmation token.
func (s *LocalStore) Claim(ctx context.Context, runID string, revision uint64, stepID, owner string) (Claim, bool, error) {
	if !validStoreCoordinate(runID) || !validStoreCoordinate(stepID) || owner == "" || revision == 0 {
		return Claim{}, false, flowError(CategoryInvalid, "store.claim", nil)
	}
	release, err := s.lock(ctx, runID)
	if err != nil {
		return Claim{}, false, err
	}
	defer release()
	state, err := s.loadOrInitialize(runID)
	if err != nil {
		return Claim{}, false, err
	}
	key := storeStepKey(revision, stepID)
	if _, terminal := state.Terminals[key]; terminal {
		return Claim{}, false, nil
	}
	now := s.clock.Now()
	if prior, exists := state.Claims[key]; exists && prior.ExpiresAt.After(now) {
		return Claim{}, false, nil
	}
	token := digestValue(struct {
		RunID, StepID, Owner string
		Revision, Version    uint64
		Expires              int64
	}{runID, stepID, owner, revision, state.Version + 1, now.Add(s.leaseTTL).UnixNano()})
	state.Claims[key] = storeClaim{Owner: owner, Token: token, ExpiresAt: now.Add(s.leaseTTL)}
	if err := s.save(runID, state); err != nil {
		return Claim{}, false, err
	}
	return Claim{Owner: owner, Token: token, ExpiresAt: now.Add(s.leaseTTL)}, true, nil
}

// RenewClaim extends the same unexpired owner token before a blocking post-tool boundary.
func (s *LocalStore) RenewClaim(ctx context.Context, runID string, revision uint64, stepID string, claim Claim) (Claim, error) {
	if !validStoreCoordinate(runID) || !validStoreCoordinate(stepID) || revision == 0 || claim.Owner == "" || claim.Token == "" {
		return Claim{}, flowError(CategoryInvalid, "store.claim_renew", nil)
	}
	release, err := s.lock(ctx, runID)
	if err != nil {
		return Claim{}, err
	}
	defer release()
	state, err := s.loadOrInitialize(runID)
	if err != nil {
		return Claim{}, err
	}
	key := storeStepKey(revision, stepID)
	stored, exists := state.Claims[key]
	now := s.clock.Now()
	if !exists || stored.Owner != claim.Owner || !validClaim(stored, claim.Token, now) {
		return Claim{}, flowError(CategoryAuthority, "store.claim", nil)
	}
	stored.ExpiresAt = now.Add(s.leaseTTL)
	state.Claims[key] = stored
	if err = s.save(runID, state); err != nil {
		return Claim{}, err
	}
	return Claim{Owner: stored.Owner, Token: stored.Token, ExpiresAt: stored.ExpiresAt}, nil
}

// ConfirmIntent verifies the active lease and durably records an action intent before external I/O.
func (s *LocalStore) ConfirmIntent(ctx context.Context, intent Intent) error {
	if !validStoreCoordinate(intent.RunID) || !validStoreCoordinate(intent.StepID) || intent.Revision == 0 || !validDigest(intent.BindingDigest) || intent.KeyDigest != "" && !validDigest(intent.KeyDigest) || intent.EvidenceID != "" && !validStoreCoordinate(intent.EvidenceID) {
		return flowError(CategoryInvalid, "store.intent", nil)
	}
	release, err := s.lock(ctx, intent.RunID)
	if err != nil {
		return err
	}
	defer release()
	state, err := s.loadOrInitialize(intent.RunID)
	if err != nil {
		return err
	}
	key := storeStepKey(intent.Revision, intent.StepID)
	if !validClaim(state.Claims[key], intent.ClaimToken, s.clock.Now()) {
		return flowError(CategoryAuthority, "store.claim", nil)
	}
	safe := intent
	safe.ClaimToken = ""
	if safe.EvidenceID != "" && !safe.EffectStarted {
		return flowError(CategoryIntegrity, "store.intent", nil)
	}
	if prior, exists := state.Intents[key]; exists {
		if !sameIntent(prior, safe) || prior.EffectStarted && !safe.EffectStarted || prior.EvidenceID != "" && prior.EvidenceID != safe.EvidenceID || safe.EvidenceID != "" && !safe.EffectStarted {
			return flowError(CategoryIntegrity, "store.intent", nil)
		}
		if prior.EffectStarted == safe.EffectStarted && prior.EvidenceID == safe.EvidenceID {
			return nil
		}
	}
	state.Intents[key] = safe
	return s.save(intent.RunID, state)
}

// Intent returns one authenticated durable action intent without its ephemeral claim token.
func (s *LocalStore) Intent(ctx context.Context, runID string, revision uint64, stepID string) (Intent, bool, error) {
	release, err := s.lock(ctx, runID)
	if err != nil {
		return Intent{}, false, err
	}
	defer release()
	state, err := s.loadOrInitialize(runID)
	if err != nil {
		return Intent{}, false, err
	}
	value, ok := state.Intents[storeStepKey(revision, stepID)]
	return value, ok, nil
}

// ConfirmTerminal verifies ownership when claimed, stores one immutable terminal, and releases its lease atomically.
func (s *LocalStore) ConfirmTerminal(ctx context.Context, record StepRecord) error {
	if !validStoreCoordinate(record.RunID) || !validStoreCoordinate(record.StepID) || record.Revision == 0 {
		return flowError(CategoryInvalid, "store.terminal", nil)
	}
	release, err := s.lock(ctx, record.RunID)
	if err != nil {
		return err
	}
	defer release()
	state, err := s.loadOrInitialize(record.RunID)
	if err != nil {
		return err
	}
	key := storeStepKey(record.Revision, record.StepID)
	safe := record
	safe.ClaimToken = ""
	if prior, exists := state.Terminals[key]; exists {
		if digestValue(prior) != digestValue(safe) {
			return flowError(CategoryIntegrity, "store.terminal", nil)
		}
		return nil
	}
	if record.ClaimToken == "" {
		return flowError(CategoryAuthority, "store.claim", nil)
	}
	if claim, exists := state.Claims[key]; !exists || !validClaim(claim, record.ClaimToken, s.clock.Now()) {
		return flowError(CategoryAuthority, "store.claim", nil)
	}
	state.Terminals[key] = safe
	delete(state.Claims, key)
	return s.save(record.RunID, state)
}

// ReconcileTerminal idempotently promotes a terminal already proven by the authenticated journal and releases stale ownership.
func (s *LocalStore) ReconcileTerminal(ctx context.Context, record StepRecord) error {
	if !validStoreCoordinate(record.RunID) || !validStoreCoordinate(record.StepID) || record.Revision == 0 {
		return flowError(CategoryInvalid, "store.terminal_reconcile", nil)
	}
	release, err := s.lock(ctx, record.RunID)
	if err != nil {
		return err
	}
	defer release()
	state, err := s.loadOrInitialize(record.RunID)
	if err != nil {
		return err
	}
	key := storeStepKey(record.Revision, record.StepID)
	safe := record
	safe.ClaimToken = ""
	if prior, exists := state.Terminals[key]; exists {
		if digestValue(prior) != digestValue(safe) {
			return flowError(CategoryIntegrity, "store.terminal_reconcile", nil)
		}
		return nil
	}
	state.Terminals[key] = safe
	delete(state.Claims, key)
	return s.save(record.RunID, state)
}

// Terminal returns one isolated authenticated terminal for recovery convergence.
func (s *LocalStore) Terminal(ctx context.Context, runID string, revision uint64, stepID string) (StepRecord, bool, error) {
	release, err := s.lock(ctx, runID)
	if err != nil {
		return StepRecord{}, false, err
	}
	defer release()
	state, err := s.loadOrInitialize(runID)
	if err != nil {
		return StepRecord{}, false, err
	}
	value, ok := state.Terminals[storeStepKey(revision, stepID)]
	if !ok {
		return StepRecord{}, false, nil
	}
	raw, _ := json.Marshal(value)
	var out StepRecord
	_ = json.Unmarshal(raw, &out)
	return out, true, nil
}

// validClaim checks token equality and expiry inside the trusted store clock boundary.
func validClaim(claim storeClaim, token string, now time.Time) bool {
	return claim.Token != "" && hmac.Equal([]byte(claim.Token), []byte(token)) && claim.ExpiresAt.After(now)
}

// storeStepKey creates one unambiguous private revision-step map coordinate.
func storeStepKey(revision uint64, stepID string) string {
	return itoa(int(revision)) + "\x00" + stepID
}

// validStoreCoordinate prevents traversal and control characters at the filesystem boundary.
func validStoreCoordinate(value string) bool { return safeStoreID.MatchString(value) }

// lock acquires cancelable process ownership automatically released after process death.
func (s *LocalStore) lock(ctx context.Context, runID string) (func(), error) {
	if !validStoreCoordinate(runID) {
		return nil, flowError(CategoryInvalid, "store.run_id", nil)
	}
	if err := os.MkdirAll(s.root, 0o700); err != nil {
		return nil, flowError(CategoryInternal, "store.root", nil)
	}
	release, err := acquireFlowProcessLock(ctx, s.path(runID)+".lock")
	if err != nil {
		return nil, flowError(CategoryCancelled, "store.lock", err)
	}
	return release, nil
}

// loadOrInitialize reads an authenticated snapshot or creates empty in-memory state before first confirmation.
func (s *LocalStore) loadOrInitialize(runID string) (durableState, error) {
	state, err := s.load(runID)
	if errors.Is(err, os.ErrNotExist) {
		return durableState{Claims: map[string]storeClaim{}, Intents: map[string]Intent{}, Terminals: map[string]StepRecord{}, Events: []Event{}}, nil
	}
	return state, err
}

// load authenticates a complete durable state and rejects truncation, missing maps, or trailing JSON.
func (s *LocalStore) load(runID string) (durableState, error) {
	raw, err := os.ReadFile(s.path(runID))
	if err != nil {
		return durableState{}, err
	}
	var state durableState
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&state); err != nil || ensureJSONEOF(decoder) != nil || state.Version == 0 || state.MAC == "" || state.Claims == nil || state.Intents == nil || state.Terminals == nil || state.Events == nil {
		return durableState{}, flowError(CategoryIntegrity, "store.state", nil)
	}
	key, err := s.authorityKey()
	if err != nil {
		return durableState{}, err
	}
	mac, err := stateMAC(state, key)
	if err != nil || !hmac.Equal([]byte(mac), []byte(state.MAC)) {
		return durableState{}, flowError(CategoryIntegrity, "store.mac", nil)
	}
	return state, nil
}

// save increments the CAS version, authenticates state, and atomically replaces its durable file.
func (s *LocalStore) save(runID string, state durableState) error {
	releaseImportWrite, err := controlplane.AcquireLocalImportWrite(context.Background(), s.root)
	if err != nil {
		return flowError(CategoryAuthority, "store.offline_import_freeze", err)
	}
	defer releaseImportWrite()
	state.Version++
	key, err := s.authorityKey()
	if err != nil {
		return err
	}
	state.MAC, err = stateMAC(state, key)
	if err != nil {
		return err
	}
	raw, err := json.Marshal(state)
	if err != nil {
		return flowError(CategoryInternal, "store.encode", nil)
	}
	temporary, err := os.CreateTemp(s.root, ".flow-*.tmp")
	if err != nil {
		return err
	}
	name := temporary.Name()
	defer os.Remove(name)
	if err = temporary.Chmod(0o600); err == nil {
		_, err = temporary.Write(raw)
	}
	if err == nil {
		err = temporary.Sync()
	}
	closeErr := temporary.Close()
	if err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	if err := replaceFlowFile(name, s.path(runID)); err != nil {
		return err
	}
	directory, err := os.Open(s.root)
	if err == nil {
		err = directory.Sync()
		_ = directory.Close()
		if runtime.GOOS == "windows" {
			err = nil
		}
	}
	return err
}

// authorityKey loads or atomically creates private MAC material never exposed through flow projections.
func (s *LocalStore) authorityKey() ([]byte, error) {
	path := filepath.Join(s.root, ".flow-state.key")
	key, err := os.ReadFile(path)
	if err == nil && len(key) == 32 {
		return key, nil
	}
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	key = make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		return nil, err
	}
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err == nil {
		_, err = file.Write(key)
		if err == nil {
			err = file.Sync()
		}
		closeErr := file.Close()
		if err == nil {
			err = closeErr
		}
	}
	if err != nil {
		existing, readErr := os.ReadFile(path)
		if readErr == nil && len(existing) == 32 {
			return existing, nil
		}
		return nil, err
	}
	return key, nil
}

// stateMAC authenticates every durable field except its own MAC with consumer-owned material.
func stateMAC(state durableState, key []byte) (string, error) {
	state.MAC = ""
	raw, err := json.Marshal(state)
	if err != nil {
		return "", err
	}
	mac := hmac.New(sha256.New, key)
	_, _ = mac.Write(raw)
	return hex.EncodeToString(mac.Sum(nil)), nil
}

// path derives one flat state filename after coordinate validation.
func (s *LocalStore) path(runID string) string { return filepath.Join(s.root, runID+".flow.json") }
