package planapproval

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sync"

	"github.com/asierhr/featureProof/internal/agentplanning"
	"github.com/asierhr/featureProof/internal/harness"
)

// approvalRequest is the private immutable snapshot paired with one confirmed requested event.
type approvalRequest struct {
	runID, planID, planHash, viewHash string
	contextHashes                     contextFingerprints
	revision                          uint64
	plan                              agentplanning.PreparedPlan
	context                           ContractSnapshot
	view                              ApprovalView
}

// ApprovalStore owns per-run ordering, idempotency and private request snapshots.
type ApprovalStore interface {
	BindRun(*harness.Run) (*runLifecycle, error)
	Events(context.Context, string) ([]ApprovalEvent, error)
	Request(context.Context, string, uint64) (approvalRequest, error)
	SaveRequest(context.Context, approvalRequest) error
	CompareAndAppend(context.Context, string, uint64, string, ApprovalEvent) (ApprovalEvent, bool, error)
}

// runLifecycle serializes journal CAS, projection and one immutable harness aggregate identity for a run.
type runLifecycle struct {
	mu          sync.Mutex
	run         *harness.Run
	nextToken   uint64
	activeToken uint64
}

// reserveLifecycle records one in-flight owner operation without retaining the mutex across external work.
func (l *runLifecycle) reserveLifecycle(expected ...harness.Status) (uint64, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.activeToken != 0 {
		return 0, typedError(CategoryRevisionConflict, "reserve_lifecycle", "active", nil)
	}
	current := l.run.Status()
	matched := false
	for _, state := range expected {
		matched = matched || current == state
	}
	if !matched {
		return 0, typedError(CategoryInvalidState, "reserve_lifecycle", "state", nil)
	}
	l.nextToken++
	l.activeToken = l.nextToken
	return l.activeToken, nil
}

// commitLifecycle applies one journal-derived state while excluding competing lifecycle commits only for this short section.
func (l *runLifecycle) commitLifecycle(token uint64, state harness.Status) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if token == 0 || l.activeToken != token {
		return typedError(CategoryRevisionConflict, "commit_lifecycle", "token", nil)
	}
	if l.run.Status() != state {
		if err := l.run.Transition(state); err != nil {
			return typedError(CategoryIntegrityFailure, "commit_lifecycle", "state", nil)
		}
	}
	l.activeToken = 0
	return nil
}

// releaseLifecycle abandons only the caller's uncommitted reservation after an external failure.
func (l *runLifecycle) releaseLifecycle(token uint64) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.activeToken == token {
		l.activeToken = 0
	}
}

// runPartition isolates one run's journal and request snapshots from concurrent runs.
type runPartition struct {
	mu       sync.Mutex
	events   []ApprovalEvent
	requests map[uint64]approvalRequest
}

// MemoryStore is the hermetic atomic approval journal used by application composition and tests.
type MemoryStore struct {
	mu         sync.Mutex
	runs       map[string]*runPartition
	lifecycles map[string]*runLifecycle
}

// NewMemoryStore creates an empty store whose locking is partitioned by run.
func NewMemoryStore() *MemoryStore {
	return &MemoryStore{runs: map[string]*runPartition{}, lifecycles: map[string]*runLifecycle{}}
}

// BindRun registers the sole aggregate identity allowed to observe and mutate one journal partition.
func (s *MemoryStore) BindRun(run *harness.Run) (*runLifecycle, error) {
	if run == nil || !safeRunID.MatchString(run.ID) {
		return nil, typedError(CategoryInvalidInput, "bind_run", "run", nil)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	owner := s.lifecycles[run.ID]
	if owner == nil {
		owner = &runLifecycle{run: run}
		s.lifecycles[run.ID] = owner
		return owner, nil
	}
	if owner.run != run {
		return nil, typedError(CategoryIntegrityFailure, "bind_run", "identity", nil)
	}
	return owner, nil
}

// partition returns the stable per-run owner without holding a global lock during run work.
func (s *MemoryStore) partition(runID string) *runPartition {
	s.mu.Lock()
	defer s.mu.Unlock()
	partition := s.runs[runID]
	if partition == nil {
		partition = &runPartition{requests: map[uint64]approvalRequest{}}
		s.runs[runID] = partition
	}
	return partition
}

// Events returns an isolated confirmed prefix for projection.
func (s *MemoryStore) Events(ctx context.Context, runID string) ([]ApprovalEvent, error) {
	if err := ctx.Err(); err != nil {
		return nil, contextFailure("events", err)
	}
	p := s.partition(runID)
	p.mu.Lock()
	defer p.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return nil, contextFailure("events", err)
	}
	return cloneEvents(p.events), nil
}

// Request returns an isolated private snapshot for the requested revision.
func (s *MemoryStore) Request(ctx context.Context, runID string, revision uint64) (approvalRequest, error) {
	if err := ctx.Err(); err != nil {
		return approvalRequest{}, contextFailure("request", err)
	}
	p := s.partition(runID)
	p.mu.Lock()
	defer p.mu.Unlock()
	r, ok := p.requests[revision]
	if !ok {
		return approvalRequest{}, typedError(CategoryIntegrityFailure, "request", "revision", nil)
	}
	return cloneRequest(r), nil
}

// SaveRequest stores the immutable private material before its request event can be confirmed.
func (s *MemoryStore) SaveRequest(ctx context.Context, request approvalRequest) error {
	if err := ctx.Err(); err != nil {
		return contextFailure("save_request", err)
	}
	p := s.partition(request.runID)
	p.mu.Lock()
	defer p.mu.Unlock()
	if existing, exists := p.requests[request.revision]; exists {
		if existing.runID == request.runID && existing.planID == request.planID && existing.planHash == request.planHash && existing.viewHash == request.viewHash && existing.contextHashes.All == request.contextHashes.All {
			return nil
		}
		return typedError(CategoryRevisionConflict, "save_request", "revision", nil)
	}
	p.requests[request.revision] = cloneRequest(request)
	return nil
}

// CompareAndAppend atomically confirms one next event or replays an identical decision ID outcome.
func (s *MemoryStore) CompareAndAppend(ctx context.Context, runID string, expectedSequence uint64, decisionID string, event ApprovalEvent) (ApprovalEvent, bool, error) {
	if err := ctx.Err(); err != nil {
		return ApprovalEvent{}, false, contextFailure("append", err)
	}
	p := s.partition(runID)
	p.mu.Lock()
	defer p.mu.Unlock()
	if decisionID != "" {
		for _, recorded := range p.events {
			if recorded.DecisionID == decisionID {
				if recorded.CommandFingerprint != event.CommandFingerprint {
					return ApprovalEvent{}, false, typedError(CategoryDecisionConflict, "append", "decision_id", nil)
				}
				return recorded, true, nil
			}
		}
	}
	if uint64(len(p.events)) != expectedSequence {
		return ApprovalEvent{}, false, typedError(CategoryRevisionConflict, "append", "sequence", nil)
	}
	event.Sequence = expectedSequence + 1
	p.events = append(p.events, cloneEvent(event))
	return cloneEvent(event), false, nil
}

var safeRunID = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`)

// LocalStore persists append-only event streams with fsync while delegating atomic in-process CAS to partitions.
type LocalStore struct {
	root    string
	backend *localBackend
	files   journalFS
	load    journalLoadFunc
}

// journalLoadFunc isolates recovery and stream reads so blocked loads can be coordinated without retaining backend locks.
type journalLoadFunc func(context.Context, string) ([]ApprovalEvent, error)

// journalFile is the durability surface needed before a replacement journal can become visible.
type journalFile interface {
	Write([]byte) (int, error)
	Sync() error
	Close() error
}

// journalFS isolates atomic publication primitives so partial filesystem failures can be verified hermetically.
type journalFS interface {
	Create(string) (journalFile, error)
	Rename(string, string) error
	Remove(string) error
	Stat(string) (os.FileInfo, error)
	SyncDir(string) error
}

// osJournalFS implements durable journal publication with local operating-system files only.
type osJournalFS struct{}

// Create opens one private replacement file and truncates stale unconfirmed bytes.
func (osJournalFS) Create(path string) (journalFile, error) {
	return os.OpenFile(path, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
}

// Rename atomically changes one journal pathname on the same filesystem.
func (osJournalFS) Rename(from, to string) error { return os.Rename(from, to) }

// Remove deletes an unpublished temporary or redundant backup file.
func (osJournalFS) Remove(path string) error { return os.Remove(path) }

// Stat reports whether an earlier confirmed journal prefix exists.
func (osJournalFS) Stat(path string) (os.FileInfo, error) { return os.Stat(path) }

// localBackend shares per-root run partitions and per-run load ownership across store handles in the monolithic process.
type localBackend struct {
	memory  *MemoryStore
	loadMu  sync.Mutex
	loaded  map[string]bool
	loading map[string]*runLoadState
}

// runLoadState lets same-run waiters observe one owner result through a cancellation-aware completion signal.
type runLoadState struct {
	done chan struct{}
	err  error
}

// localBackends prevents two independently composed handles from bypassing per-run CAS in one process.
var localBackends sync.Map

// persistedRequest contains only sanitized presentation and fingerprints, never raw executable or credential-bearing context.
type persistedRequest struct {
	RunID, PlanID, PlanHash, ViewHash string
	ContextHashes                     contextFingerprints
	Revision                          uint64
	View                              ApprovalView
}

// NewLocalStore validates the authorized journal root without creating network or credential dependencies.
func NewLocalStore(root string) (*LocalStore, error) {
	if root == "" {
		return nil, typedError(CategoryInvalidInput, "compose_store", "root", nil)
	}
	absolute, err := filepath.Abs(root)
	if err != nil {
		return nil, typedError(CategoryInvalidInput, "compose_store", "root", nil)
	}
	absolute = filepath.Clean(absolute)
	if err := os.MkdirAll(absolute, 0o700); err != nil {
		return nil, typedError(CategoryStorageFailure, "compose_store", "root", nil)
	}
	created := &localBackend{memory: NewMemoryStore(), loaded: map[string]bool{}, loading: map[string]*runLoadState{}}
	value, _ := localBackends.LoadOrStore(absolute, created)
	store := &LocalStore{root: absolute, backend: value.(*localBackend), files: osJournalFS{}}
	store.load = store.loadJournal
	return store, nil
}

// ensureLoaded recovers one strict JSONL prefix once before reads or appends.
func (s *LocalStore) ensureLoaded(ctx context.Context, runID string) error {
	if !safeRunID.MatchString(runID) {
		return typedError(CategoryInvalidInput, "load_store", "run_id", nil)
	}
	state, owner, loaded := s.backend.claimLoad(runID)
	if loaded {
		return nil
	}
	if !owner {
		select {
		case <-ctx.Done():
			return contextFailure("load_store", ctx.Err())
		case <-state.done:
			return state.err
		}
	}
	path := filepath.Join(s.root, runID+".jsonl")
	temporary, err := s.load(ctx, path)
	if err == nil {
		_, err = Project(ctx, temporary, 100000)
	}
	if err == nil {
		if contextErr := ctx.Err(); contextErr != nil {
			err = contextFailure("load_store", contextErr)
		}
	}
	if err == nil {
		published := cloneEvents(temporary)
		p := s.backend.memory.partition(runID)
		p.mu.Lock()
		p.events = published
		p.mu.Unlock()
		_ = os.Remove(path + ".bak")
		_ = os.Remove(path + ".tmp")
	}
	s.backend.finishLoad(runID, state, err)
	return err
}

// claimLoad elects one same-run owner or returns a stable waiter signal while holding the coordination mutex briefly.
func (b *localBackend) claimLoad(runID string) (*runLoadState, bool, bool) {
	b.loadMu.Lock()
	defer b.loadMu.Unlock()
	if b.loaded[runID] {
		return nil, false, true
	}
	if state := b.loading[runID]; state != nil {
		return state, false, false
	}
	state := &runLoadState{done: make(chan struct{})}
	b.loading[runID] = state
	return state, true, false
}

// finishLoad publishes one owner outcome atomically, retaining success and permitting a clean retry after failure.
func (b *localBackend) finishLoad(runID string, state *runLoadState, err error) {
	b.loadMu.Lock()
	state.err = err
	if err == nil {
		b.loaded[runID] = true
	}
	if b.loading[runID] == state {
		delete(b.loading, runID)
	}
	close(state.done)
	b.loadMu.Unlock()
}

// loadJournal performs recovery and strict decoding outside all backend coordination locks.
func (s *LocalStore) loadJournal(ctx context.Context, path string) ([]ApprovalEvent, error) {
	if err := s.recoverJournalFiles(path); err != nil {
		return nil, err
	}
	file, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return []ApprovalEvent{}, nil
	}
	if err != nil {
		return nil, typedError(CategoryStorageFailure, "load_store", "journal", nil)
	}
	defer file.Close()
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 4096), 1<<20)
	temporary := make([]ApprovalEvent, 0)
	for scanner.Scan() {
		if err := ctx.Err(); err != nil {
			return nil, contextFailure("load_store", err)
		}
		var event ApprovalEvent
		if json.Unmarshal(scanner.Bytes(), &event) != nil {
			return nil, typedError(CategoryIntegrityFailure, "load_store", "journal", nil)
		}
		temporary = append(temporary, event)
	}
	if scanner.Err() != nil {
		return nil, typedError(CategoryStorageFailure, "load_store", "journal", nil)
	}
	return temporary, nil
}

// BindRun shares the same lifecycle owner across every local-store handle for one root and run ID.
func (s *LocalStore) BindRun(run *harness.Run) (*runLifecycle, error) {
	return s.backend.memory.BindRun(run)
}

// RestoreRun returns the exact shared aggregate identity, rebuilding its projected status after process restart.
func (s *LocalStore) RestoreRun(ctx context.Context, runID string) (*harness.Run, error) {
	if err := s.ensureLoaded(ctx, runID); err != nil {
		return nil, err
	}
	s.backend.memory.mu.Lock()
	owner := s.backend.memory.lifecycles[runID]
	s.backend.memory.mu.Unlock()
	if owner != nil {
		return owner.run, nil
	}
	events, err := s.Events(ctx, runID)
	if err != nil {
		return nil, err
	}
	projection, err := Project(ctx, events, 100000)
	if err != nil {
		return nil, err
	}
	run, err := harness.NewRun(runID, "recovered approval", "workflow", harness.Limits{MaxSteps: 1, MaxToolCalls: 1, MaxRetries: 1})
	if err != nil {
		return nil, typedError(CategoryIntegrityFailure, "restore_run", "run", nil)
	}
	if err = run.Transition(harness.StatusPlanning); err != nil {
		return nil, err
	}
	if projection.State != harness.StatusPlanning {
		if err = run.Transition(harness.StatusAwaitingApproval); err != nil {
			return nil, err
		}
	}
	if projection.State == harness.StatusExecuting {
		if err = run.Transition(harness.StatusExecuting); err != nil {
			return nil, err
		}
	}
	if _, err = s.backend.memory.BindRun(run); err != nil {
		return nil, err
	}
	return run, nil
}

// recoverJournalFiles restores a previously confirmed backup when publication was interrupted between pathname updates.
func (s *LocalStore) recoverJournalFiles(path string) error {
	backup := path + ".bak"
	_, journalErr := os.Stat(path)
	_, backupErr := os.Stat(backup)
	if errors.Is(journalErr, os.ErrNotExist) && backupErr == nil {
		if err := os.Rename(backup, path); err != nil {
			return typedError(CategoryStorageFailure, "load_store", "journal", nil)
		}
		if err := (osJournalFS{}).SyncDir(s.root); err != nil {
			return typedError(CategoryStorageFailure, "load_store", "journal", nil)
		}
	}
	return nil
}

// Events loads and returns one recovered confirmed journal prefix.
func (s *LocalStore) Events(ctx context.Context, runID string) ([]ApprovalEvent, error) {
	if err := s.ensureLoaded(ctx, runID); err != nil {
		return nil, err
	}
	return s.backend.memory.Events(ctx, runID)
}

// Request returns a process-private staged snapshot; raw execution material is intentionally absent from JSONL.
func (s *LocalStore) Request(ctx context.Context, runID string, revision uint64) (approvalRequest, error) {
	if err := s.ensureLoaded(ctx, runID); err != nil {
		return approvalRequest{}, err
	}
	request, err := s.backend.memory.Request(ctx, runID, revision)
	if err == nil {
		return request, nil
	}
	raw, readErr := os.ReadFile(s.requestPath(runID, revision))
	if readErr != nil {
		return approvalRequest{}, typedError(CategoryIntegrityFailure, "request", "revision", nil)
	}
	var persisted persistedRequest
	if json.Unmarshal(raw, &persisted) != nil || persisted.RunID != runID || persisted.Revision != revision {
		return approvalRequest{}, typedError(CategoryIntegrityFailure, "request", "snapshot", nil)
	}
	request = approvalRequest{runID: persisted.RunID, planID: persisted.PlanID, planHash: persisted.PlanHash, viewHash: persisted.ViewHash, contextHashes: persisted.ContextHashes, revision: persisted.Revision, view: persisted.View}
	p := s.backend.memory.partition(runID)
	p.mu.Lock()
	p.requests[revision] = cloneRequest(request)
	p.mu.Unlock()
	return cloneRequest(request), nil
}

// SaveRequest atomically persists sanitized recovery material and keeps raw executable snapshots process-private.
func (s *LocalStore) SaveRequest(ctx context.Context, request approvalRequest) error {
	if err := s.ensureLoaded(ctx, request.runID); err != nil {
		return err
	}
	if err := s.backend.memory.SaveRequest(ctx, request); err != nil {
		return err
	}
	persisted := persistedRequest{RunID: request.runID, PlanID: request.planID, PlanHash: request.planHash, ViewHash: request.viewHash, ContextHashes: request.contextHashes, Revision: request.revision, View: cloneView(request.view)}
	raw, err := json.Marshal(persisted)
	if err != nil {
		return typedError(CategoryInternal, "save_request", "snapshot", nil)
	}
	temporary := s.requestPath(request.runID, request.revision) + ".tmp"
	file, err := os.OpenFile(temporary, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err == nil {
		_, err = file.Write(raw)
	}
	if err == nil {
		err = file.Sync()
	}
	if file != nil {
		closeErr := file.Close()
		if err == nil {
			err = closeErr
		}
	}
	if err == nil {
		err = os.Rename(temporary, s.requestPath(request.runID, request.revision))
	}
	if err != nil {
		_ = os.Remove(temporary)
		p := s.backend.memory.partition(request.runID)
		p.mu.Lock()
		delete(p.requests, request.revision)
		p.mu.Unlock()
		return typedError(CategoryStorageFailure, "save_request", "snapshot", nil)
	}
	return nil
}

// requestPath derives one confined snapshot filename from an already validated run identity and monotonic revision.
func (s *LocalStore) requestPath(runID string, revision uint64) string {
	return filepath.Join(s.root, runID+".request."+fmt.Sprint(revision)+".json")
}

// CompareAndAppend persists the event under the run partition lock before exposing it as confirmed.
func (s *LocalStore) CompareAndAppend(ctx context.Context, runID string, expectedSequence uint64, decisionID string, event ApprovalEvent) (ApprovalEvent, bool, error) {
	if err := s.ensureLoaded(ctx, runID); err != nil {
		return ApprovalEvent{}, false, err
	}
	p := s.backend.memory.partition(runID)
	p.mu.Lock()
	defer p.mu.Unlock()
	if decisionID != "" {
		for _, old := range p.events {
			if old.DecisionID == decisionID {
				if old.CommandFingerprint != event.CommandFingerprint {
					return ApprovalEvent{}, false, typedError(CategoryDecisionConflict, "append", "decision_id", nil)
				}
				return cloneEvent(old), true, nil
			}
		}
	}
	if uint64(len(p.events)) != expectedSequence {
		return ApprovalEvent{}, false, typedError(CategoryRevisionConflict, "append", "sequence", nil)
	}
	event.Sequence = expectedSequence + 1
	candidate := append(cloneEvents(p.events), cloneEvent(event))
	if _, err := Project(ctx, candidate, 100000); err != nil {
		return ApprovalEvent{}, false, err
	}
	raw := make([]byte, 0, len(candidate)*256)
	for _, confirmed := range candidate {
		encoded, err := json.Marshal(confirmed)
		if err != nil {
			return ApprovalEvent{}, false, typedError(CategoryInternal, "append", "event", nil)
		}
		raw = append(raw, encoded...)
		raw = append(raw, '\n')
	}
	path := filepath.Join(s.root, runID+".jsonl")
	if err := s.publishJournal(path, raw); err != nil {
		return ApprovalEvent{}, false, typedError(CategoryStorageFailure, "append", "journal", nil)
	}
	p.events = append(p.events, cloneEvent(event))
	return cloneEvent(event), false, nil
}

// publishJournal writes and syncs a full candidate prefix before atomically replacing the last confirmed prefix.
func (s *LocalStore) publishJournal(path string, raw []byte) error {
	temporary, backup := path+".tmp", path+".bak"
	_ = s.files.Remove(temporary)
	_ = s.files.Remove(backup)
	file, err := s.files.Create(temporary)
	if err != nil {
		return err
	}
	written, writeErr := file.Write(raw)
	if writeErr == nil && written != len(raw) {
		writeErr = io.ErrShortWrite
	}
	if writeErr == nil {
		writeErr = file.Sync()
	}
	closeErr := file.Close()
	if writeErr == nil {
		writeErr = closeErr
	}
	if writeErr != nil {
		_ = s.files.Remove(temporary)
		return writeErr
	}
	_, statErr := s.files.Stat(path)
	hadPrefix := statErr == nil
	if statErr != nil && !errors.Is(statErr, os.ErrNotExist) {
		_ = s.files.Remove(temporary)
		return statErr
	}
	if hadPrefix {
		if err := s.files.Rename(path, backup); err != nil {
			_ = s.files.Remove(temporary)
			return err
		}
	}
	if err := s.files.Rename(temporary, path); err != nil {
		if hadPrefix {
			_ = s.files.Rename(backup, path)
		}
		_ = s.files.Remove(temporary)
		return err
	}
	if err := s.files.SyncDir(s.root); err != nil {
		_ = s.files.Remove(path)
		if hadPrefix {
			_ = s.files.Rename(backup, path)
		}
		_ = s.files.SyncDir(s.root)
		return err
	}
	if hadPrefix {
		_ = s.files.Remove(backup)
		_ = s.files.SyncDir(s.root)
	}
	return nil
}

// cloneEvents isolates actor pointers and the event slice from store callers.
func cloneEvents(events []ApprovalEvent) []ApprovalEvent {
	out := make([]ApprovalEvent, len(events))
	for i := range events {
		out[i] = cloneEvent(events[i])
	}
	return out
}

// cloneEvent isolates optional actor identity from caller mutation.
func cloneEvent(event ApprovalEvent) ApprovalEvent {
	if event.Actor != nil {
		actor := *event.Actor
		event.Actor = &actor
	}
	return event
}

// cloneRequest isolates all mutable context and view collections while retaining opaque immutable plan values.
func cloneRequest(request approvalRequest) approvalRequest {
	request.context = cloneContext(request.context)
	request.view = cloneView(request.view)
	return request
}
