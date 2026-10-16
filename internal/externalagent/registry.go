package externalagent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// Stable registry errors are translated without inspecting text.
var (
	ErrConflict = errors.New("registry conflict")
	ErrNotFound = errors.New("registry not found")
)

// registryDocument is the closed private persistence schema for all local runs.
type registryDocument struct {
	Version int                  `json:"version"`
	Runs    map[string]RunRecord `json:"runs"`
	Events  []RunEvent           `json:"events"`
}

// LocalRegistry durably stores private run state in one protected local document.
type LocalRegistry struct {
	mu     sync.Mutex
	path   string
	leases map[string]func()
}

// NewLocalRegistry prepares the trusted root and its private registry file.
func NewLocalRegistry(root string) (*LocalRegistry, error) {
	if root == "" || !filepath.IsAbs(root) {
		return nil, &AppError{Code: CodeInternal}
	}
	if err := os.MkdirAll(root, 0o700); err != nil {
		return nil, fmt.Errorf("create external registry: %w", err)
	}
	registry := &LocalRegistry{path: filepath.Join(root, "runs.json"), leases: map[string]func(){}}
	registry.mu.Lock()
	defer registry.mu.Unlock()
	release, err := acquireRegistryLock(context.Background(), registry.path+".lock")
	if err != nil {
		return nil, err
	}
	defer release()
	if _, err := os.Stat(registry.path); errors.Is(err, os.ErrNotExist) {
		if err := registry.saveLocked(registryDocument{Version: 1, Runs: map[string]RunRecord{}, Events: []RunEvent{}}); err != nil {
			return nil, err
		}
	} else if err != nil {
		return nil, fmt.Errorf("stat external registry: %w", err)
	}
	if _, err := registry.loadLocked(); err != nil {
		return nil, err
	}
	return registry, nil
}

// Reserve atomically binds principal, operation, key hash, and request hash.
func (r *LocalRegistry) Reserve(ctx context.Context, candidate RunRecord) (ReserveResult, error) {
	if err := ctx.Err(); err != nil {
		return ReserveResult{}, err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	release, err := acquireRegistryLock(ctx, r.path+".lock")
	if err != nil {
		return ReserveResult{}, err
	}
	defer release()
	document, err := r.loadLocked()
	if err != nil {
		return ReserveResult{}, err
	}
	for _, existing := range document.Runs {
		if existing.PrincipalID == candidate.PrincipalID && existing.SubmitKeyHash == candidate.SubmitKeyHash {
			if existing.RequestHash != candidate.RequestHash {
				return ReserveResult{}, ErrConflict
			}
			return ReserveResult{Record: existing}, nil
		}
	}
	if _, exists := document.Runs[candidate.Handle]; exists {
		return ReserveResult{}, ErrConflict
	}
	if !validInitialRecord(candidate) {
		return ReserveResult{}, errors.New("invalid initial registry record")
	}
	ownerRelease, err := acquireRegistryLock(ctx, r.ownerPath(candidate.Handle))
	if err != nil {
		return ReserveResult{}, err
	}
	document.Runs[candidate.Handle] = candidate
	appendRunEvent(&document, candidate, "reserved", "")
	if err := r.saveLocked(document); err != nil {
		ownerRelease()
		return ReserveResult{}, err
	}
	r.leases[candidate.Handle] = ownerRelease
	return ReserveResult{Record: candidate, New: true}, nil
}

// Get reads one exact record without disclosing path details in errors.
func (r *LocalRegistry) Get(ctx context.Context, handle string) (RunRecord, error) {
	if err := ctx.Err(); err != nil {
		return RunRecord{}, err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	release, err := acquireRegistryLock(ctx, r.path+".lock")
	if err != nil {
		return RunRecord{}, err
	}
	defer release()
	document, err := r.loadLocked()
	if err != nil {
		return RunRecord{}, err
	}
	record, ok := document.Runs[handle]
	if !ok {
		return RunRecord{}, ErrNotFound
	}
	return record, nil
}

// Transition applies one compare-and-swap lifecycle change and persists it atomically.
func (r *LocalRegistry) Transition(ctx context.Context, handle string, from, to Phase, status Outcome, packageDir, owner string, now time.Time) (RunRecord, error) {
	if err := ctx.Err(); err != nil {
		return RunRecord{}, err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	release, err := acquireRegistryLock(ctx, r.path+".lock")
	if err != nil {
		return RunRecord{}, err
	}
	defer release()
	document, err := r.loadLocked()
	if err != nil {
		return RunRecord{}, err
	}
	record, ok := document.Runs[handle]
	if !ok {
		return RunRecord{}, ErrNotFound
	}
	if record.Phase != from || !validTransition(from, to) {
		return RunRecord{}, ErrConflict
	}
	if to == PhaseTerminal && !validOutcome(status) {
		return RunRecord{}, ErrConflict
	}
	if to != PhaseTerminal && status != "" {
		return RunRecord{}, ErrConflict
	}
	record.Phase, record.Status, record.PackageDir, record.Owner = to, status, packageDir, owner
	if now.IsZero() {
		return RunRecord{}, ErrConflict
	}
	record.UpdatedAt = now.UTC()
	document.Runs[handle] = record
	if to == PhaseRunning {
		appendRunEvent(&document, record, "started", "")
	} else if to == PhaseTerminal {
		appendRunEvent(&document, record, "terminal", record.Cause)
	}
	if err := r.saveLocked(document); err != nil {
		return RunRecord{}, err
	}
	return record, nil
}

// ReserveCancel binds one cancellation key to its canonical request.
func (r *LocalRegistry) ReserveCancel(ctx context.Context, handle, keyHash, requestHash string) (RunRecord, bool, error) {
	if err := ctx.Err(); err != nil {
		return RunRecord{}, false, err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	release, err := acquireRegistryLock(ctx, r.path+".lock")
	if err != nil {
		return RunRecord{}, false, err
	}
	defer release()
	document, err := r.loadLocked()
	if err != nil {
		return RunRecord{}, false, err
	}
	record, ok := document.Runs[handle]
	if !ok {
		return RunRecord{}, false, ErrNotFound
	}
	for existingHandle, existing := range document.Runs {
		if existing.CancelKeyHash == keyHash {
			if existingHandle != handle || existing.CancelHash != requestHash {
				return RunRecord{}, false, ErrConflict
			}
			return existing, false, nil
		}
	}
	if record.CancelKeyHash != "" {
		if record.CancelKeyHash != keyHash || record.CancelHash != requestHash {
			return RunRecord{}, false, ErrConflict
		}
		return record, false, nil
	}
	record.CancelKeyHash, record.CancelHash = keyHash, requestHash
	document.Runs[handle] = record
	appendRunEvent(&document, record, "cancel_requested", "")
	if err := r.saveLocked(document); err != nil {
		return RunRecord{}, false, err
	}
	return record, true, nil
}

// Recover closes nonterminal records owned by a previous process without rerunning them.
func (r *LocalRegistry) Recover(ctx context.Context, owner string, now time.Time) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	release, err := acquireRegistryLock(ctx, r.path+".lock")
	if err != nil {
		return err
	}
	defer release()
	document, err := r.loadLocked()
	if err != nil {
		return err
	}
	changed := false
	for handle, record := range document.Runs {
		if (record.Phase == PhaseQueued || record.Phase == PhaseRunning || record.Phase == PhaseFinalizing) && record.Owner != "" && record.Owner != owner {
			releaseOwner, acquired, lockErr := tryRegistryLock(r.ownerPath(handle))
			if lockErr != nil {
				return lockErr
			}
			if !acquired {
				continue
			}
			releaseOwner()
			record.Phase, record.Status, record.PackageDir, record.Owner, record.UpdatedAt, record.Cause = PhaseTerminal, OutcomeError, "", "", now.UTC(), "interrupted"
			document.Runs[handle] = record
			appendRunEvent(&document, record, "recovered", "interrupted")
			appendRunEvent(&document, record, "terminal", "interrupted")
			changed = true
		}
	}
	if changed {
		return r.saveLocked(document)
	}
	return nil
}

// Events returns a defensive, ordered audit prefix for one authorized internal caller.
func (r *LocalRegistry) Events(ctx context.Context, handle string) ([]RunEvent, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	release, err := acquireRegistryLock(ctx, r.path+".lock")
	if err != nil {
		return nil, err
	}
	defer release()
	document, err := r.loadLocked()
	if err != nil {
		return nil, err
	}
	if _, ok := document.Runs[handle]; !ok {
		return nil, ErrNotFound
	}
	events := []RunEvent{}
	for _, event := range document.Events {
		if event.RunHandle == handle {
			events = append(events, event)
		}
	}
	return events, nil
}

// MarkPublicationFailure records the safe failure cause with the current snapshot atomically.
func (r *LocalRegistry) MarkPublicationFailure(ctx context.Context, handle string, now time.Time) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	release, err := acquireRegistryLock(ctx, r.path+".lock")
	if err != nil {
		return err
	}
	defer release()
	document, err := r.loadLocked()
	if err != nil {
		return err
	}
	record, ok := document.Runs[handle]
	if !ok {
		return ErrNotFound
	}
	if record.Phase != PhaseRunning || now.IsZero() {
		return ErrConflict
	}
	record.Cause, record.UpdatedAt = "publication_failed", now.UTC()
	document.Runs[handle] = record
	appendRunEvent(&document, record, "publication_failed", "publication_failed")
	return r.saveLocked(document)
}

// ReleaseOwner drops the process-lifetime OS lease only after child work and terminal persistence finish.
func (r *LocalRegistry) ReleaseOwner(handle string) {
	r.mu.Lock()
	release := r.leases[handle]
	delete(r.leases, handle)
	r.mu.Unlock()
	if release != nil {
		release()
	}
}

// ownerPath derives an internal lock path only from an already-validated opaque handle.
func (r *LocalRegistry) ownerPath(handle string) string { return r.path + ".owner." + handle + ".lock" }

// loadLocked decodes a bounded, closed registry document while the local mutex is held.
func (r *LocalRegistry) loadLocked() (registryDocument, error) {
	raw, err := os.ReadFile(r.path)
	if err != nil {
		return registryDocument{}, fmt.Errorf("read external registry: %w", err)
	}
	if len(raw) > 16<<20 {
		return registryDocument{}, errors.New("external registry limit")
	}
	var document registryDocument
	if err := DecodeStrict(raw, &document); err != nil || document.Version != 1 || document.Runs == nil || document.Events == nil || validateJournal(document) != nil {
		return registryDocument{}, errors.New("invalid external registry")
	}
	return document, nil
}

// appendRunEvent extends the immutable global prefix using a monotonic sequence.
func appendRunEvent(document *registryDocument, record RunRecord, kind, cause string) {
	document.Events = append(document.Events, RunEvent{Sequence: len(document.Events) + 1, Type: kind, RunHandle: record.Handle, At: record.UpdatedAt, Phase: record.Phase, Status: record.Status, Cause: cause})
}

// validateJournal rejects reordered, truncated, foreign, or snapshot-inconsistent event histories.
func validateJournal(document registryDocument) error {
	last := map[string]RunEvent{}
	terminalSeen := map[string]bool{}
	for index, event := range document.Events {
		if event.Sequence != index+1 || event.RunHandle == "" || event.At.IsZero() {
			return errors.New("invalid journal sequence")
		}
		if _, ok := document.Runs[event.RunHandle]; !ok {
			return errors.New("foreign journal event")
		}
		if previous, ok := last[event.RunHandle]; ok && event.At.Before(previous.At) {
			return errors.New("invalid journal time")
		}
		if event.Type == "terminal" {
			terminalSeen[event.RunHandle] = true
		}
		last[event.RunHandle] = event
	}
	for handle, record := range document.Runs {
		_, ok := last[handle]
		if !ok || (record.Phase == PhaseTerminal) != terminalSeen[handle] {
			return errors.New("journal snapshot mismatch")
		}
	}
	return nil
}

// saveLocked publishes a complete registry through fsync and same-directory rename.
func (r *LocalRegistry) saveLocked(document registryDocument) error {
	raw, err := json.Marshal(document)
	if err != nil {
		return fmt.Errorf("encode external registry: %w", err)
	}
	temporary := r.path + ".tmp"
	file, err := os.OpenFile(temporary, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return fmt.Errorf("open external registry staging: %w", err)
	}
	failed := true
	defer func() {
		_ = file.Close()
		if failed {
			_ = os.Remove(temporary)
		}
	}()
	if _, err := file.Write(raw); err != nil {
		return fmt.Errorf("write external registry: %w", err)
	}
	if err := file.Sync(); err != nil {
		return fmt.Errorf("sync external registry: %w", err)
	}
	if err := file.Close(); err != nil {
		return fmt.Errorf("close external registry: %w", err)
	}
	if err := os.Rename(temporary, r.path); err != nil {
		return fmt.Errorf("publish external registry: %w", err)
	}
	if err := syncRegistryDir(filepath.Dir(r.path)); err != nil {
		return fmt.Errorf("sync external registry directory: %w", err)
	}
	failed = false
	return nil
}

// validInitialRecord guards persistence invariants before the first write.
func validInitialRecord(record RunRecord) bool {
	return validHandle(record.Handle) && validHandle(record.InternalID) && record.PrincipalID != "" && record.SelectionAlias != "" && len(record.RequestHash) == 64 && len(record.SubmitKeyHash) == 64 && record.Phase == PhaseQueued && record.Status == "" && !record.UpdatedAt.IsZero()
}

// validTransition defines the monotonic public lifecycle.
func validTransition(from, to Phase) bool {
	return from == PhaseQueued && (to == PhaseRunning || to == PhaseTerminal) || from == PhaseRunning && (to == PhaseFinalizing || to == PhaseTerminal) || from == PhaseFinalizing && to == PhaseTerminal
}

// validOutcome validates every terminal value in the application contract.
func validOutcome(outcome Outcome) bool {
	switch outcome {
	case OutcomePassed, OutcomeFailed, OutcomeInconclusive, OutcomeCancelled, OutcomeError:
		return true
	}
	return false
}
