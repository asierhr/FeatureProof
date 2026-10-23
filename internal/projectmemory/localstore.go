package projectmemory

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"
)

// errProjectionStale distinguishes a valid older projection from corruption that must fail closed.
var errProjectionStale = errors.New("projection stale")

// IntegrityAuthority authenticates canonical bytes with keys kept outside the memory root.
type IntegrityAuthority interface {
	Sign(context.Context, []byte) (string, string, error)
	Verify(context.Context, string, []byte, string) error
}

// EventEnvelope is one authenticated append-only mutation in a project stream.
type EventEnvelope struct {
	Format, ProjectID                                               string
	Sequence                                                        uint64
	EventID, IdempotencyDigest                                      string
	ExpectedPrevious                                                uint64
	PreviousEventDigest, Transition                                 string
	Record                                                          Record
	PolicyDigest, MutationDigest, EventDigest, KeyID, Authenticator string
}

// receipt binds an idempotency identity to exactly one mutation and result.
type receipt struct {
	MutationDigest string `json:"mutation_digest"`
	RecordID       string `json:"record_id"`
	Version        uint64 `json:"version"`
	Sequence       uint64 `json:"sequence"`
}

// localSnapshot is an authenticated materialization rebuilt solely from the verified journal.
type localSnapshot struct {
	Format, ProjectID    string
	Head                 Head
	Current              map[string]Record
	History              map[string][]Record
	Receipts             map[string]receipt
	KeyID, Authenticator string
}

// localIndex is a derived authenticated structural reference set with no new content.
type localIndex struct {
	Format, ProjectID    string
	Head                 Head
	References           []indexReference
	KeyID, Authenticator string
}

// durableAnchor authenticates the highest committed journal head outside replaceable project projections.
type durableAnchor struct {
	Format, ProjectID    string
	Head                 Head
	Receipts             map[string]receipt
	KeyID, Authenticator string
}

// pendingCommit is the authenticated local recovery material whose opaque identity is advanced by the external witness.
type pendingCommit struct {
	Format   string
	Project  string
	Expected WitnessState
	Next     WitnessState
	Event    EventEnvelope
}

// indexReference contains only structural fields and an authenticated record identity.
type indexReference struct {
	ID                 string
	Version            uint64
	Class              Class
	State              State
	Authority          Authority
	Sensitivity        Sensitivity
	Scope              Scope
	LogicalKey, Digest string
}

// LocalStore persists authenticated project streams beneath a configured canonical root.
type LocalStore struct {
	root          string
	rootIdentity  string
	trustRoot     string
	trustIdentity string
	integrity     IntegrityAuthority
	witness       MonotonicWitness
	limits        Limits
	global        sync.Mutex
	mu            sync.Mutex
	pinnedMu      sync.RWMutex
	locks         map[string]*sync.Mutex
	pinned        map[string]string
	boundary      func(string, string)
}

// NewLocalStore validates the local roots and requires an independently durable monotonic witness.
func NewLocalStore(root string, integrity IntegrityAuthority, witness MonotonicWitness, limits Limits) (*LocalStore, error) {
	trustRoot := filepath.Join(filepath.Dir(filepath.Clean(root)), ".featureproof-memory-trust")
	return NewLocalStoreWithAuthorities(root, trustRoot, integrity, witness, limits)
}

// NewLocalStoreWithTrustRoot places the authenticated stream registry outside the mutable memory root.
func NewLocalStoreWithTrustRoot(root, trustRoot string, integrity IntegrityAuthority, witness MonotonicWitness, limits Limits) (*LocalStore, error) {
	return NewLocalStoreWithAuthorities(root, trustRoot, integrity, witness, limits)
}

// NewLocalStoreWithAuthorities separates filesystem persistence from the non-replayable witness authority.
func NewLocalStoreWithAuthorities(root, trustRoot string, integrity IntegrityAuthority, witness MonotonicWitness, limits Limits) (*LocalStore, error) {
	if !filepath.IsAbs(root) || !filepath.IsAbs(trustRoot) || integrity == nil || witness == nil {
		return nil, fieldError("invalid_input", "store.open", "root", nil)
	}
	clean := filepath.Clean(root)
	cleanTrust := filepath.Clean(trustRoot)
	if clean != root || cleanTrust != trustRoot || pathContains(clean, cleanTrust) || pathContains(cleanTrust, clean) {
		return nil, fieldError("invalid_input", "store.open", "root", nil)
	}
	if err := secureMkdirAll(clean); err != nil {
		return nil, err
	}
	if err := secureMkdirAll(cleanTrust); err != nil {
		return nil, err
	}
	identity, err := memoryDirectoryIdentity(clean)
	if err != nil {
		return nil, err
	}
	trustIdentity, err := memoryDirectoryIdentity(cleanTrust)
	if err != nil {
		return nil, err
	}
	store := &LocalStore{root: clean, rootIdentity: identity, trustRoot: cleanTrust, trustIdentity: trustIdentity, integrity: integrity, witness: witness, limits: limits, locks: map[string]*sync.Mutex{}, pinned: map[string]string{}}
	for _, fixed := range []string{filepath.Join(clean, "v1"), filepath.Join(clean, "v1", "projects"), filepath.Join(cleanTrust, "v1"), filepath.Join(cleanTrust, "v1", "registry")} {
		if err = store.pinStoreDirectory(fixed); err != nil {
			return nil, err
		}
	}
	return store, nil
}

// pathContains reports whether child is equal to or nested below parent after canonical cleaning.
func pathContains(parent, child string) bool {
	relative, err := filepath.Rel(parent, child)
	return err == nil && (relative == "." || relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator)))
}

// openStoreDirectory traverses one configured storage tree entirely relative to its fixed root handle.
func (store *LocalStore) openStoreDirectory(path string, create bool) (*memoryDirectoryHandle, error) {
	clean := filepath.Clean(path)
	root, identity := "", ""
	if pathContains(store.root, clean) {
		root, identity = store.root, store.rootIdentity
	} else if pathContains(store.trustRoot, clean) {
		root, identity = store.trustRoot, store.trustIdentity
	} else {
		return nil, fieldError("path", "store.identity", "escape", nil)
	}
	relative, err := filepath.Rel(root, clean)
	if err != nil {
		return nil, fieldError("path", "store.path", "relative", err)
	}
	directory, err := openMemoryDirectoryTree(root, relative, create)
	if err != nil {
		return nil, fieldError("path", "store.path", "component", err)
	}
	if directory.RootIdentity() != identity {
		_ = directory.Close()
		return nil, fieldError("path", "store.identity", "root_handle", nil)
	}
	prefix := root
	components := directory.ComponentIdentities()
	relativeParts := []string{}
	if relative != "." {
		relativeParts = strings.Split(relative, string(filepath.Separator))
	}
	for index, component := range relativeParts {
		prefix = filepath.Join(prefix, component)
		store.pinnedMu.RLock()
		expected, ok := store.pinned[prefix]
		store.pinnedMu.RUnlock()
		if ok && (index >= len(components) || components[index] != expected) {
			_ = directory.Close()
			return nil, fieldError("path", "store.identity", "component", nil)
		}
	}
	return directory, nil
}

// pinStoreDirectory creates one fixed infrastructure component and remembers its effective identity.
func (store *LocalStore) pinStoreDirectory(path string) error {
	directory, err := store.openStoreDirectory(path, true)
	if err != nil {
		return err
	}
	defer directory.Close()
	identities := directory.ComponentIdentities()
	if len(identities) == 0 {
		return fieldError("path", "store.identity", "pin", nil)
	}
	store.pinnedMu.Lock()
	store.pinned[filepath.Clean(path)] = identities[len(identities)-1]
	store.pinnedMu.Unlock()
	return nil
}

// Commit serializes writers, stages one authenticated event, advances the witness, and only then materializes local state.
func (store *LocalStore) Commit(ctx context.Context, mutation Mutation) (CommitResult, error) {
	if err := ctx.Err(); err != nil {
		return CommitResult{}, fieldError("cancelled", "store.commit", "", err)
	}
	if !safeID.MatchString(mutation.ProjectID) || mutation.Record.ProjectID != mutation.ProjectID || mutation.IdempotencyKey == "" || mutation.Transition == "" || !digestPattern.MatchString(mutation.PolicyDigest) {
		return CommitResult{}, fieldError("invalid_input", "store.commit", "mutation", nil)
	}
	if err := VerifyRecord(mutation.Record, store.limits); err != nil {
		return CommitResult{}, err
	}
	release, err := store.acquire(ctx, mutation.ProjectID)
	if err != nil {
		return CommitResult{}, err
	}
	defer release()
	snapshot, events, err := store.loadVerified(ctx, mutation.ProjectID, true)
	if err != nil {
		return CommitResult{}, err
	}
	idempotencyDigest := digestCanonical(struct{ Key string }{mutation.IdempotencyKey})
	mutationDigest := mutation.RequestDigest
	if mutationDigest == "" {
		mutationDigest = digestCanonical(struct {
			Project         string
			ExpectedVersion uint64
			Transition      string
			RecordDigest    string
		}{mutation.ProjectID, mutation.ExpectedVersion, mutation.Transition, mutation.Record.RecordDigest})
	}
	if !digestPattern.MatchString(mutationDigest) {
		return CommitResult{}, fieldError("invalid_input", "store.commit", "request_digest", nil)
	}
	if err = store.rejectCrossProjectReceipt(ctx, mutation.ProjectID, idempotencyDigest); err != nil {
		return CommitResult{}, err
	}
	if previous, ok := snapshot.Receipts[idempotencyDigest]; ok {
		if previous.MutationDigest != mutationDigest {
			return CommitResult{}, fieldError("conflict", "store.idempotency", "idempotency_key", nil)
		}
		record, found := historical(snapshot, previous.RecordID, previous.Version)
		if !found {
			return CommitResult{}, fieldError("integrity", "store.receipt", "record", nil)
		}
		return CommitResult{Record: record, Head: snapshot.Head, Redelivered: true}, nil
	}
	if snapshot.Head.Sequence != mutation.ExpectedSequence {
		return CommitResult{}, fieldError("conflict", "store.cas", "sequence", nil)
	}
	if mutation.ExpectedVersion == 0 {
		if _, exists := snapshot.Current[mutation.Record.ID]; exists {
			return CommitResult{}, fieldError("conflict", "store.cas", "version", nil)
		}
	} else {
		current, exists := snapshot.Current[mutation.Record.ID]
		if !exists || current.Version != mutation.ExpectedVersion || mutation.Record.Version != current.Version+1 {
			return CommitResult{}, fieldError("conflict", "store.cas", "version", nil)
		}
	}
	event := EventEnvelope{Format: ContractVersion, ProjectID: mutation.ProjectID, Sequence: snapshot.Head.Sequence + 1, EventID: randomToken(), IdempotencyDigest: idempotencyDigest, ExpectedPrevious: mutation.ExpectedSequence, PreviousEventDigest: snapshot.Head.Digest, Transition: mutation.Transition, Record: cloneRecord(mutation.Record), PolicyDigest: mutation.PolicyDigest, MutationDigest: mutationDigest}
	event.EventDigest = envelopeDigest(event)
	unsigned := event
	unsigned.KeyID, unsigned.Authenticator = "", ""
	canonical, _ := json.Marshal(unsigned)
	event.KeyID, event.Authenticator, err = store.integrity.Sign(ctx, canonical)
	if err != nil {
		return CommitResult{}, fieldError("unavailable", "store.sign", "", err)
	}
	after := append(append([]EventEnvelope(nil), events...), event)
	pending := pendingCommit{Format: ContractVersion, Project: mutation.ProjectID, Expected: witnessStateForEvents(mutation.ProjectID, events), Next: witnessStateForEvents(mutation.ProjectID, after), Event: event}
	if err = store.preparePending(ctx, pending); err != nil {
		return CommitResult{}, err
	}
	advanced, advanceErr := store.advanceWitness(ctx, pending)
	if advanceErr != nil {
		if !advanced {
			_ = store.removePending(mutation.ProjectID)
		}
		return CommitResult{}, advanceErr
	}
	if err = store.materializePending(ctx, events, pending); err != nil {
		return CommitResult{}, err
	}
	if err = store.publishAnchor(ctx, mutation.ProjectID, after); err != nil {
		return CommitResult{}, err
	}
	events = after
	snapshot, err = store.replay(ctx, mutation.ProjectID, events)
	if err != nil {
		return CommitResult{}, err
	}
	if err = store.publish(ctx, mutation.ProjectID, snapshot); err != nil {
		return CommitResult{}, err
	}
	if err = store.removePending(mutation.ProjectID); err != nil {
		return CommitResult{}, err
	}
	return CommitResult{Record: cloneRecord(mutation.Record), Head: snapshot.Head}, nil
}

// BootstrapProject explicitly asks the configured external authority to create an irreversible generation-zero stream.
func (store *LocalStore) BootstrapProject(ctx context.Context, project string) error {
	if !safeID.MatchString(project) {
		return fieldError("invalid_input", "store.witness.bootstrap", "project", nil)
	}
	state, err := store.witness.Bootstrap(ctx, project)
	if err != nil {
		return fieldError("unavailable", "store.witness.bootstrap", "authority", err)
	}
	expected := witnessStateForEvents(project, nil)
	if state != expected {
		return fieldError("integrity", "store.witness.bootstrap", "state", nil)
	}
	return nil
}

// Current returns the authenticated latest immutable version for one memory ID.
func (store *LocalStore) Current(ctx context.Context, project, id string) (Record, error) {
	snapshot, _, err := store.loadReadVerified(ctx, project)
	if err != nil {
		return Record{}, err
	}
	record, ok := snapshot.Current[id]
	if !ok {
		return Record{}, fieldError("not_found", "store.current", "id", nil)
	}
	return cloneRecord(record), nil
}

// Version returns one exact authenticated historical version even after later terminal transitions.
func (store *LocalStore) Version(ctx context.Context, project, id string, version uint64) (Record, error) {
	snapshot, _, err := store.loadReadVerified(ctx, project)
	if err != nil {
		return Record{}, err
	}
	record, ok := historical(snapshot, id, version)
	if !ok {
		return Record{}, fieldError("not_found", "store.version", "id", nil)
	}
	return cloneRecord(record), nil
}

// Scan returns verified detached current records, leaving final eligibility to the service.
func (store *LocalStore) Scan(ctx context.Context, project string, filter StructuralFilter, limit int) (VerifiedPage, error) {
	snapshot, _, err := store.loadReadVerified(ctx, project)
	if err != nil {
		return VerifiedPage{}, err
	}
	ids := make([]string, 0, len(snapshot.Current))
	for id := range snapshot.Current {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	records := []Record{}
	for _, id := range ids {
		if err = ctx.Err(); err != nil {
			return VerifiedPage{}, fieldError("cancelled", "store.scan", "", err)
		}
		record := snapshot.Current[id]
		if !containsClass(filter.Classes, record.Class) || !containsState(filter.States, record.State) || !containsAuthority(filter.Authorities, record.Authority) {
			continue
		}
		records = append(records, cloneRecord(record))
		if limit > 0 && len(records) >= limit {
			break
		}
	}
	return VerifiedPage{Records: records, Head: snapshot.Head}, nil
}

// Head returns the verified stream head used by historical bindings.
func (store *LocalStore) Head(ctx context.Context, project string) (Head, error) {
	snapshot, _, err := store.loadReadVerified(ctx, project)
	if err != nil {
		return Head{}, err
	}
	return snapshot.Head, nil
}

// loadReadVerified gives readers the same cross-process recovery ownership used by writers.
func (store *LocalStore) loadReadVerified(ctx context.Context, project string) (localSnapshot, []EventEnvelope, error) {
	release, err := store.acquire(ctx, project)
	if err != nil {
		return localSnapshot{}, nil, err
	}
	defer release()
	return store.loadVerified(ctx, project, true)
}

// Recover rebuilds missing or stale derived projections from the verified append-only journal.
func (store *LocalStore) Recover(ctx context.Context, project string) error {
	release, err := store.acquire(ctx, project)
	if err != nil {
		return err
	}
	defer release()
	snapshot, _, err := store.loadVerified(ctx, project, false)
	if err != nil {
		return err
	}
	return store.publish(ctx, project, snapshot)
}

// acquire obtains in-process and filesystem ownership while honoring cancellation.
func (store *LocalStore) acquire(ctx context.Context, project string) (func(), error) {
	if err := lockMutex(ctx, &store.global); err != nil {
		return nil, err
	}
	globalDir := filepath.Join(store.root, "v1")
	if err := secureMkdirAll(globalDir); err != nil {
		store.global.Unlock()
		return nil, err
	}
	globalLockPath := filepath.Join(globalDir, "write.lock")
	globalIdentity, identityErr := memoryDirectoryIdentity(globalDir)
	if identityErr != nil {
		store.global.Unlock()
		return nil, identityErr
	}
	if store.boundary != nil {
		store.boundary("lock.global.before_open", globalLockPath)
	}
	if err := store.verifyRootIdentity(); err != nil {
		store.global.Unlock()
		return nil, err
	}
	globalDirectory, err := store.openStoreDirectory(globalDir, true)
	if err != nil {
		store.global.Unlock()
		return nil, err
	}
	releaseGlobalLock, err := acquireMemoryProcessLock(ctx, globalDirectory, filepath.Base(globalLockPath))
	if err != nil {
		_ = globalDirectory.Close()
		store.global.Unlock()
		return nil, fieldError("cancelled", "store.lock", "", err)
	}
	releaseGlobal := func() { releaseGlobalLock(); _ = globalDirectory.Close() }
	currentGlobalIdentity, identityErr := memoryDirectoryIdentity(globalDir)
	if identityErr != nil || currentGlobalIdentity != globalIdentity || store.verifyRootIdentity() != nil {
		releaseGlobal()
		store.global.Unlock()
		return nil, fieldError("path", "store.identity", "global_lock", identityErr)
	}
	store.mu.Lock()
	lock := store.locks[project]
	if lock == nil {
		lock = &sync.Mutex{}
		store.locks[project] = lock
	}
	store.mu.Unlock()
	if err = lockMutex(ctx, lock); err != nil {
		releaseGlobal()
		store.global.Unlock()
		return nil, err
	}
	dir, err := store.projectDir(project, true)
	if err != nil {
		lock.Unlock()
		releaseGlobal()
		store.global.Unlock()
		return nil, err
	}
	path := filepath.Join(dir, "write.lock")
	projectIdentity, identityErr := memoryDirectoryIdentity(dir)
	if identityErr != nil {
		lock.Unlock()
		releaseGlobal()
		store.global.Unlock()
		return nil, identityErr
	}
	if store.boundary != nil {
		store.boundary("lock.project.before_open", path)
	}
	projectDirectory, err := store.openStoreDirectory(dir, true)
	if err != nil {
		lock.Unlock()
		releaseGlobal()
		store.global.Unlock()
		return nil, err
	}
	releaseProcessLock, err := acquireMemoryProcessLock(ctx, projectDirectory, filepath.Base(path))
	if err != nil {
		_ = projectDirectory.Close()
		lock.Unlock()
		releaseGlobal()
		store.global.Unlock()
		if ctx.Err() != nil {
			return nil, fieldError("cancelled", "store.lock", "", ctx.Err())
		}
		return nil, fieldError("unavailable", "store.lock", "", err)
	}
	releaseProcess := func() { releaseProcessLock(); _ = projectDirectory.Close() }
	currentProjectIdentity, identityErr := memoryDirectoryIdentity(dir)
	if identityErr != nil || currentProjectIdentity != projectIdentity || store.verifyRootIdentity() != nil {
		releaseProcess()
		lock.Unlock()
		releaseGlobal()
		store.global.Unlock()
		return nil, fieldError("path", "store.identity", "lock", identityErr)
	}
	return func() { releaseProcess(); lock.Unlock(); releaseGlobal(); store.global.Unlock() }, nil
}

// lockMutex waits without helper goroutines so cancellation cannot leave background ownership work.
func lockMutex(ctx context.Context, lock *sync.Mutex) error {
	for !lock.TryLock() {
		select {
		case <-ctx.Done():
			return fieldError("cancelled", "store.lock", "", ctx.Err())
		case <-time.After(time.Millisecond):
		}
	}
	return nil
}

// rejectCrossProjectReceipt prevents one raw idempotency identity from being rebound to another project stream.
func (store *LocalStore) rejectCrossProjectReceipt(ctx context.Context, project, idempotencyDigest string) error {
	projectsRoot := filepath.Join(store.root, "v1", "projects")
	directory, err := store.openStoreDirectory(projectsRoot, false)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fieldError("unavailable", "store.receipts", "projects", err)
	}
	defer directory.Close()
	entries, err := directory.ReadDirNames()
	if err != nil {
		return fieldError("unavailable", "store.receipts", "projects", err)
	}
	work := 0
	for _, entry := range entries {
		if err = ctx.Err(); err != nil {
			return fieldError("cancelled", "store.receipts", "", err)
		}
		decoded, decodeErr := base64.RawURLEncoding.DecodeString(entry)
		if decodeErr != nil || !safeID.Match(decoded) {
			return fieldError("integrity", "store.receipts", "project", nil)
		}
		other := string(decoded)
		if other == project {
			continue
		}
		events, readErr := store.readJournal(ctx, other, filepath.Join(projectsRoot, entry, "journal.log"))
		if readErr != nil {
			return readErr
		}
		for _, event := range events {
			work++
			if store.limits.MaxQueryWork > 0 && work > store.limits.MaxQueryWork {
				return fieldError("limit", "store.receipts", "work", nil)
			}
			if event.IdempotencyDigest == idempotencyDigest {
				return fieldError("conflict", "store.idempotency", "project", nil)
			}
		}
	}
	return nil
}

// projectDir encodes the project ID and verifies every existing path component before use.
func (store *LocalStore) projectDir(project string, create bool) (string, error) {
	if err := store.verifyRootIdentity(); err != nil {
		return "", err
	}
	if !safeID.MatchString(project) {
		return "", fieldError("invalid_input", "store.path", "project", nil)
	}
	encoded := base64.RawURLEncoding.EncodeToString([]byte(project))
	dir := filepath.Join(store.root, "v1", "projects", encoded)
	if store.boundary != nil {
		store.boundary("path.project.before_open", dir)
	}
	directory, err := store.openStoreDirectory(dir, create)
	if err != nil {
		return "", err
	}
	defer directory.Close()
	identities := directory.ComponentIdentities()
	if len(identities) == 0 {
		return "", fieldError("path", "store.identity", "project", nil)
	}
	store.pinnedMu.Lock()
	if expected, ok := store.pinned[dir]; ok && expected != identities[len(identities)-1] {
		store.pinnedMu.Unlock()
		return "", fieldError("path", "store.identity", "project", nil)
	}
	store.pinned[dir] = identities[len(identities)-1]
	store.pinnedMu.Unlock()
	if store.boundary != nil {
		store.boundary("path.project.after_open", dir)
	}
	if err := store.verifyRootIdentity(); err != nil {
		return "", err
	}
	return dir, nil
}

// verifyRootIdentity detects root replacement before any project path is accepted.
func (store *LocalStore) verifyRootIdentity() error {
	identity, err := memoryDirectoryIdentity(store.root)
	if err != nil || identity != store.rootIdentity {
		return fieldError("path", "store.identity", "root", err)
	}
	return nil
}

// appendEvent durably appends exactly one canonical journal line and syncs it before projections.
func (store *LocalStore) appendEvent(ctx context.Context, project string, event EventEnvelope) error {
	if err := ctx.Err(); err != nil {
		return fieldError("cancelled", "store.append", "", err)
	}
	dir, err := store.projectDir(project, true)
	if err != nil {
		return err
	}
	path := filepath.Join(dir, "journal.log")
	directoryIdentity, identityErr := memoryDirectoryIdentity(dir)
	if identityErr != nil {
		return fieldError("path", "store.identity", "project", identityErr)
	}
	directory, identityErr := store.openStoreDirectory(dir, true)
	if identityErr != nil {
		return fieldError("path", "store.identity", "project", identityErr)
	}
	defer directory.Close()
	if err = verifyRegularFile(path, true); err != nil {
		return err
	}
	if store.boundary != nil {
		store.boundary("append.before_open", path)
	}
	file, err := directory.OpenAppend(filepath.Base(path))
	if err != nil {
		return fieldError("unavailable", "store.append", "", err)
	}
	defer file.Close()
	currentDirectoryIdentity, identityErr := memoryDirectoryIdentity(dir)
	if identityErr != nil || currentDirectoryIdentity != directoryIdentity || store.verifyRootIdentity() != nil {
		return fieldError("path", "store.identity", "project", identityErr)
	}
	raw, _ := json.Marshal(event)
	raw = append(raw, '\n')
	written, writeErr := file.Write(raw)
	if writeErr != nil || written != len(raw) {
		if writeErr == nil {
			writeErr = io.ErrShortWrite
		}
		return fieldError("unavailable", "store.append", "", writeErr)
	}
	if store.boundary != nil {
		store.boundary("append.after_write", path)
	}
	if err = file.Sync(); err != nil {
		return fieldError("unavailable", "store.sync", "", err)
	}
	if store.boundary != nil {
		store.boundary("append.after_sync", path)
	}
	return nil
}

// loadVerified verifies the journal source of truth and optionally repairs only derived projections.
func (store *LocalStore) loadVerified(ctx context.Context, project string, repair bool) (localSnapshot, []EventEnvelope, error) {
	dir, err := store.projectDir(project, true)
	if err != nil {
		return localSnapshot{}, nil, err
	}
	path := filepath.Join(dir, "journal.log")
	if err = store.recoverPending(ctx, project, path); err != nil {
		return localSnapshot{}, nil, err
	}
	events, err := store.readJournal(ctx, project, path)
	if err != nil {
		return localSnapshot{}, nil, err
	}
	snapshot, err := store.replay(ctx, project, events)
	if err != nil {
		return localSnapshot{}, nil, err
	}
	if repair {
		if err = store.verifyOrRepairProjections(ctx, project, snapshot); err != nil {
			return localSnapshot{}, nil, err
		}
	}
	return snapshot, events, nil
}

// readJournal rejects malformed, truncated, reordered, copied, or unauthenticated events.
func (store *LocalStore) readJournal(ctx context.Context, project, path string) ([]EventEnvelope, error) {
	events, err := store.readJournalSource(ctx, project, path)
	if err != nil {
		return nil, err
	}
	if err = store.verifyWitness(ctx, project, events); err != nil {
		return nil, err
	}
	if err = store.verifyAnchor(ctx, project, events); err != nil {
		return nil, err
	}
	return events, nil
}

// readJournalSource verifies local event bytes while leaving witness reconciliation to the recovery owner.
func (store *LocalStore) readJournalSource(ctx context.Context, project, path string) ([]EventEnvelope, error) {
	dir := filepath.Dir(path)
	directoryIdentity, identityErr := memoryDirectoryIdentity(dir)
	if identityErr != nil {
		return nil, fieldError("path", "store.identity", "project", identityErr)
	}
	directory, identityErr := store.openStoreDirectory(dir, false)
	if identityErr != nil {
		return nil, fieldError("path", "store.identity", "project", identityErr)
	}
	defer directory.Close()
	if err := verifyRegularFile(path, true); err != nil {
		return nil, err
	}
	if store.boundary != nil {
		store.boundary("read.before_open", path)
	}
	file, err := directory.OpenRead(filepath.Base(path))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fieldError("unavailable", "store.read", "", err)
	}
	defer file.Close()
	currentDirectoryIdentity, identityErr := memoryDirectoryIdentity(filepath.Dir(path))
	if identityErr != nil || currentDirectoryIdentity != directoryIdentity || store.verifyRootIdentity() != nil {
		return nil, fieldError("path", "store.identity", "project", identityErr)
	}
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 1024), store.limits.MaxDocumentBytes*4)
	events := []EventEnvelope{}
	for scanner.Scan() {
		if err = ctx.Err(); err != nil {
			return nil, fieldError("cancelled", "store.read", "", err)
		}
		decoder := json.NewDecoder(bytes.NewReader(scanner.Bytes()))
		decoder.DisallowUnknownFields()
		var event EventEnvelope
		if decoder.Decode(&event) != nil {
			return events, fieldError("integrity", "store.journal", "event", nil)
		}
		var trailing any
		if err = decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
			return events, fieldError("integrity", "store.journal", "trailing", err)
		}
		if event.ProjectID != project || event.Format != ContractVersion || event.Sequence != uint64(len(events)+1) || event.ExpectedPrevious+1 != event.Sequence || event.EventDigest != envelopeDigest(event) || !digestPattern.MatchString(event.IdempotencyDigest) || !digestPattern.MatchString(event.PolicyDigest) || !digestPattern.MatchString(event.MutationDigest) {
			return events, fieldError("integrity", "store.journal", "chain", nil)
		}
		previous := ""
		if len(events) > 0 {
			previous = events[len(events)-1].EventDigest
		}
		if event.PreviousEventDigest != previous {
			return events, fieldError("integrity", "store.journal", "previous", nil)
		}
		unsigned := event
		unsigned.KeyID, unsigned.Authenticator = "", ""
		raw, _ := json.Marshal(unsigned)
		if store.integrity.Verify(ctx, event.KeyID, raw, event.Authenticator) != nil {
			return events, fieldError("integrity", "store.journal", "authenticator", nil)
		}
		if VerifyRecord(event.Record, store.limits) != nil {
			return events, fieldError("integrity", "store.journal", "record", nil)
		}
		events = append(events, event)
	}
	if err = scanner.Err(); err != nil {
		return events, fieldError("integrity", "store.journal", "truncated", err)
	}
	return events, nil
}

// preparePending durably publishes one signed recovery event before any irreversible witness mutation.
func (store *LocalStore) preparePending(ctx context.Context, pending pendingCommit) error {
	dir, err := store.projectDir(pending.Project, true)
	if err != nil {
		return err
	}
	path := filepath.Join(dir, "pending.json")
	if _, err = store.readObject(path, true, "pending.read"); err == nil {
		return fieldError("integrity", "store.pending", "occupied", nil)
	} else if !errors.Is(err, os.ErrNotExist) {
		return fieldError("integrity", "store.pending", "existing", err)
	}
	return store.atomicJSON(ctx, dir, "pending.json", pending, "pending.write")
}

// advanceWitness binds the staged commit identity before any definitive journal append and reports whether authority advanced.
func (store *LocalStore) advanceWitness(ctx context.Context, pending pendingCommit) (bool, error) {
	if store.boundary != nil {
		store.boundary("witness.before_advance", pending.Project)
	}
	if err := store.confirmPending(ctx, pending); err != nil {
		return false, err
	}
	current, err := store.witness.Read(ctx, pending.Project)
	if err != nil {
		if errors.Is(err, ErrWitnessMissing) {
			return false, fieldError("integrity", "store.witness.read", "missing", err)
		}
		return false, fieldError("unavailable", "store.witness.read", "authority", err)
	}
	if current != pending.Expected {
		return false, fieldError("integrity", "store.witness", "generation", nil)
	}
	if err = store.witness.CompareAndAdvance(ctx, pending.Expected, pending.Next); err != nil {
		confirmed, confirmErr := store.witness.Read(ctx, pending.Project)
		if confirmErr == nil && confirmed == pending.Next {
			if store.boundary != nil {
				store.boundary("witness.after_advance", pending.Project)
			}
			if pendingErr := store.confirmPending(ctx, pending); pendingErr != nil {
				return true, pendingErr
			}
			return true, nil
		}
		if confirmErr == nil && confirmed == pending.Expected {
			if errors.Is(err, ErrWitnessConflict) {
				return false, fieldError("conflict", "store.witness.advance", "generation", err)
			}
			return false, fieldError("unavailable", "store.witness.advance", "authority", err)
		}
		return true, fieldError("unavailable", "store.witness.advance", "uncertain", err)
	}
	if store.boundary != nil {
		store.boundary("witness.after_advance", pending.Project)
	}
	if err = store.confirmPending(ctx, pending); err != nil {
		return true, err
	}
	confirmed, err := store.witness.Read(ctx, pending.Project)
	if err != nil {
		return true, fieldError("unavailable", "store.witness.confirm", "authority", err)
	}
	if confirmed != pending.Next {
		return true, fieldError("integrity", "store.witness.confirm", "state", nil)
	}
	return true, nil
}

// confirmPending reopens staging and compares its complete canonical identity at each authority boundary.
func (store *LocalStore) confirmPending(ctx context.Context, expected pendingCommit) error {
	persisted, err := store.readPending(ctx, expected.Project)
	if err != nil {
		return fieldError("integrity", "store.pending", "effective", err)
	}
	if digestCanonical(persisted) != digestCanonical(expected) {
		return fieldError("integrity", "store.pending", "identity", nil)
	}
	return nil
}

// recoverPending reconciles an exact signed staging event with the irreversible witness before exposing local state.
func (store *LocalStore) recoverPending(ctx context.Context, project, journalPath string) error {
	events, journalErr := store.readJournalSource(ctx, project, journalPath)
	state, err := store.witness.Read(ctx, project)
	if err != nil {
		if errors.Is(err, ErrWitnessMissing) {
			return fieldError("integrity", "store.witness", "missing", err)
		}
		return fieldError("unavailable", "store.witness", "authority", err)
	}
	pending, pendingErr := store.readPending(ctx, project)
	expected := witnessStateForEvents(project, events)
	if state == expected {
		if journalErr != nil {
			return journalErr
		}
		if pendingErr == nil {
			if pending.Next == state && len(events) > 0 && digestCanonical(events[len(events)-1]) == pending.Next.CommitDigest && digestCanonical(pending.Event) == pending.Next.CommitDigest && store.validatePending(ctx, events[:len(events)-1], pending) == nil {
				if err = store.publishAnchor(ctx, project, events); err != nil {
					return err
				}
				snapshot, replayErr := store.replay(ctx, project, events)
				if replayErr != nil {
					return replayErr
				}
				if err = store.publish(ctx, project, snapshot); err != nil {
					return err
				}
				return store.removePending(project)
			}
			if err = store.validatePending(ctx, events, pending); err != nil || pending.Expected != state {
				return fieldError("integrity", "store.pending", "stale", err)
			}
			return store.removePending(project)
		}
		if !errors.Is(pendingErr, os.ErrNotExist) {
			return pendingErr
		}
		return nil
	}
	if pendingErr != nil {
		return fieldError("integrity", "store.pending", "missing", pendingErr)
	}
	if err = store.validatePending(ctx, events, pending); err != nil {
		return err
	}
	if pending.Expected != expected || pending.Next != state {
		return fieldError("integrity", "store.pending", "witness", nil)
	}
	if journalErr != nil {
		if err = store.rewriteJournal(ctx, project, append(events, pending.Event)); err != nil {
			return err
		}
	} else if err = store.materializePending(ctx, events, pending); err != nil {
		return err
	}
	after := append(events, pending.Event)
	if err = store.publishAnchor(ctx, project, after); err != nil {
		return err
	}
	snapshot, err := store.replay(ctx, project, after)
	if err != nil {
		return err
	}
	if err = store.publish(ctx, project, snapshot); err != nil {
		return err
	}
	return store.removePending(project)
}

// readPending strictly decodes the single local staging object through a no-follow handle.
func (store *LocalStore) readPending(ctx context.Context, project string) (pendingCommit, error) {
	dir, err := store.projectDir(project, true)
	if err != nil {
		return pendingCommit{}, err
	}
	raw, err := store.readObject(filepath.Join(dir, "pending.json"), true, "pending.read")
	if err != nil {
		return pendingCommit{}, err
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	var pending pendingCommit
	if decoder.Decode(&pending) != nil || decoder.Decode(&struct{}{}) != io.EOF {
		return pendingCommit{}, fieldError("integrity", "store.pending", "encoding", nil)
	}
	if err = store.validatePending(ctx, nil, pending); err != nil {
		return pendingCommit{}, err
	}
	return pending, nil
}

// validatePending authenticates staging and binds its event, expected head, successor, project, and opaque witness digest.
func (store *LocalStore) validatePending(ctx context.Context, events []EventEnvelope, pending pendingCommit) error {
	if pending.Format != ContractVersion || !safeID.MatchString(pending.Project) || pending.Event.ProjectID != pending.Project || pending.Event.EventDigest != envelopeDigest(pending.Event) || pending.Next.CommitDigest != digestCanonical(pending.Event) {
		return fieldError("integrity", "store.pending", "binding", nil)
	}
	unsigned := pending.Event
	unsigned.KeyID, unsigned.Authenticator = "", ""
	raw, _ := json.Marshal(unsigned)
	if store.integrity.Verify(ctx, pending.Event.KeyID, raw, pending.Event.Authenticator) != nil || VerifyRecord(pending.Event.Record, store.limits) != nil {
		return fieldError("integrity", "store.pending", "authenticator", nil)
	}
	if events != nil {
		after := append(append([]EventEnvelope(nil), events...), pending.Event)
		if pending.Expected != witnessStateForEvents(pending.Project, events) || pending.Next != witnessStateForEvents(pending.Project, after) || pending.Event.Sequence != uint64(len(after)) || pending.Event.ExpectedPrevious+1 != pending.Event.Sequence || pending.Event.PreviousEventDigest != pending.Expected.Head.Digest {
			return fieldError("integrity", "store.pending", "chain", nil)
		}
	}
	return nil
}

// materializePending appends the staged event only after the witness has confirmed its opaque identity.
func (store *LocalStore) materializePending(ctx context.Context, events []EventEnvelope, pending pendingCommit) error {
	if err := store.validatePending(ctx, events, pending); err != nil {
		return err
	}
	if err := store.confirmPending(ctx, pending); err != nil {
		return err
	}
	if err := store.appendEvent(ctx, pending.Project, pending.Event); err != nil {
		return err
	}
	if store.boundary != nil {
		store.boundary("materialize.after_append", pending.Project)
	}
	return nil
}

// removePending deletes only the fixed verified staging object after success or a confirmed pre-advance failure.
func (store *LocalStore) removePending(project string) error {
	dir, err := store.projectDir(project, true)
	if err != nil {
		return err
	}
	directory, err := store.openStoreDirectory(dir, false)
	if err != nil {
		return err
	}
	defer directory.Close()
	if err = directory.Remove("pending.json"); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fieldError("unavailable", "store.pending.remove", "", err)
	}
	return directory.Sync()
}

// verifyWitness requires the external authority to match local history exactly without forward or backward repair.
func (store *LocalStore) verifyWitness(ctx context.Context, project string, events []EventEnvelope) error {
	state, err := store.witness.Read(ctx, project)
	if err != nil {
		if errors.Is(err, ErrWitnessMissing) {
			return fieldError("integrity", "store.witness", "missing", err)
		}
		return fieldError("unavailable", "store.witness", "authority", err)
	}
	if state != witnessStateForEvents(project, events) {
		return fieldError("integrity", "store.witness", "generation", nil)
	}
	return nil
}

// receiptsForEvents reconstructs the idempotency ledger sealed by the external registry.
func receiptsForEvents(project string, events []EventEnvelope) map[string]receipt {
	receipts := make(map[string]receipt, len(events))
	for _, event := range events {
		mutationDigest := event.MutationDigest
		if mutationDigest == "" {
			mutationDigest = digestCanonical(struct {
				Project                  string
				ExpectedVersion          uint64
				Transition, RecordDigest string
			}{project, event.Record.Version - 1, event.Transition, event.Record.RecordDigest})
		}
		receipts[event.IdempotencyDigest] = receipt{MutationDigest: mutationDigest, RecordID: event.Record.ID, Version: event.Record.Version, Sequence: event.Sequence}
	}
	return receipts
}

// publishAnchor atomically refreshes the replaceable local registry after the witness confirms the journal head.
func (store *LocalStore) publishAnchor(ctx context.Context, project string, events []EventEnvelope) error {
	anchor := durableAnchor{Format: ContractVersion, ProjectID: project, Receipts: receiptsForEvents(project, events)}
	if len(events) > 0 {
		last := events[len(events)-1]
		anchor.Head = Head{Sequence: last.Sequence, Digest: last.EventDigest}
	}
	unsigned := anchor
	canonical, _ := json.Marshal(unsigned)
	var err error
	anchor.KeyID, anchor.Authenticator, err = store.integrity.Sign(ctx, canonical)
	if err != nil {
		return fieldError("unavailable", "store.anchor", "sign", err)
	}
	dir := filepath.Join(store.trustRoot, "v1", "registry")
	name := base64.RawURLEncoding.EncodeToString([]byte(project)) + ".json"
	return store.atomicJSON(ctx, dir, name, anchor, "registry.write")
}

// verifyAnchor rejects missing journals, registry deletion, receipt rollback, and tail deletion.
func (store *LocalStore) verifyAnchor(ctx context.Context, project string, events []EventEnvelope) error {
	path := filepath.Join(store.trustRoot, "v1", "registry", base64.RawURLEncoding.EncodeToString([]byte(project))+".json")
	raw, err := store.readObject(path, true, "registry.read")
	if errors.Is(err, os.ErrNotExist) && len(events) == 0 {
		return nil
	}
	if err != nil {
		return fieldError("integrity", "store.anchor", "missing", err)
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	var anchor durableAnchor
	if decoder.Decode(&anchor) != nil {
		return fieldError("integrity", "store.anchor", "decode", nil)
	}
	var trailing any
	if decodeErr := decoder.Decode(&trailing); !errors.Is(decodeErr, io.EOF) {
		return fieldError("integrity", "store.anchor", "trailing", decodeErr)
	}
	auth, key := anchor.Authenticator, anchor.KeyID
	anchor.KeyID, anchor.Authenticator = "", ""
	canonical, _ := json.Marshal(anchor)
	if anchor.Format != ContractVersion || anchor.ProjectID != project || store.integrity.Verify(ctx, key, canonical, auth) != nil {
		return fieldError("integrity", "store.anchor", "authenticator", nil)
	}
	actual := Head{}
	if len(events) > 0 {
		last := events[len(events)-1]
		actual = Head{Sequence: last.Sequence, Digest: last.EventDigest}
	}
	if actual != anchor.Head {
		if actual.Sequence > anchor.Head.Sequence && (anchor.Head.Sequence == 0 || events[anchor.Head.Sequence-1].EventDigest == anchor.Head.Digest) {
			return store.publishAnchor(ctx, project, events)
		}
		return fieldError("integrity", "store.journal", "tail", nil)
	}
	if digestCanonical(anchor.Receipts) != digestCanonical(receiptsForEvents(project, events)) {
		return fieldError("integrity", "store.registry", "receipts", nil)
	}
	return nil
}

// replay deterministically reconstructs snapshots and receipts from authenticated events.
func (store *LocalStore) replay(ctx context.Context, project string, events []EventEnvelope) (localSnapshot, error) {
	snapshot := localSnapshot{Format: ContractVersion, ProjectID: project, Current: map[string]Record{}, History: map[string][]Record{}, Receipts: map[string]receipt{}}
	for _, event := range events {
		if err := ctx.Err(); err != nil {
			return localSnapshot{}, fieldError("cancelled", "store.replay", "", err)
		}
		current, exists := snapshot.Current[event.Record.ID]
		if !exists && event.Record.Version != 1 {
			return localSnapshot{}, fieldError("integrity", "store.replay", "version", nil)
		}
		if exists && event.Record.Version != current.Version+1 {
			return localSnapshot{}, fieldError("integrity", "store.replay", "version", nil)
		}
		mutationDigest := event.MutationDigest
		if mutationDigest == "" {
			mutationDigest = digestCanonical(struct {
				Project         string
				ExpectedVersion uint64
				Transition      string
				RecordDigest    string
			}{project, event.Record.Version - 1, event.Transition, event.Record.RecordDigest})
		}
		if prior, ok := snapshot.Receipts[event.IdempotencyDigest]; ok && prior.MutationDigest != mutationDigest {
			return localSnapshot{}, fieldError("integrity", "store.replay", "receipt", nil)
		}
		snapshot.Current[event.Record.ID] = cloneRecord(event.Record)
		snapshot.History[event.Record.ID] = append(snapshot.History[event.Record.ID], cloneRecord(event.Record))
		snapshot.Receipts[event.IdempotencyDigest] = receipt{MutationDigest: mutationDigest, RecordID: event.Record.ID, Version: event.Record.Version, Sequence: event.Sequence}
		snapshot.Head = Head{Sequence: event.Sequence, Digest: event.EventDigest}
	}
	return snapshot, nil
}

// publish atomically replaces authenticated snapshot and structural index files after journal durability.
func (store *LocalStore) publish(ctx context.Context, project string, snapshot localSnapshot) error {
	dir, err := store.projectDir(project, true)
	if err != nil {
		return err
	}
	unsigned := snapshot
	unsigned.KeyID, unsigned.Authenticator = "", ""
	raw, _ := json.Marshal(unsigned)
	snapshot.KeyID, snapshot.Authenticator, err = store.integrity.Sign(ctx, raw)
	if err != nil {
		return fieldError("unavailable", "store.snapshot.sign", "", err)
	}
	references := []indexReference{}
	for _, record := range snapshot.Current {
		references = append(references, indexReference{ID: record.ID, Version: record.Version, Class: record.Class, State: record.State, Authority: record.Authority, Sensitivity: record.Sensitivity, Scope: record.Scope, LogicalKey: record.LogicalKey(), Digest: record.RecordDigest})
	}
	sort.Slice(references, func(i, j int) bool { return references[i].ID < references[j].ID })
	index := localIndex{Format: ContractVersion, ProjectID: project, Head: snapshot.Head, References: references}
	indexUnsigned := index
	indexRaw, _ := json.Marshal(indexUnsigned)
	index.KeyID, index.Authenticator, err = store.integrity.Sign(ctx, indexRaw)
	if err != nil {
		return fieldError("unavailable", "store.index.sign", "", err)
	}
	if err = store.atomicJSON(ctx, dir, "snapshot.json", snapshot, "snapshot.write"); err != nil {
		return err
	}
	return store.atomicJSON(ctx, dir, "index.json", index, "index.write")
}

// verifyOrRepairProjections verifies present projections and rebuilds missing or stale derived files.
func (store *LocalStore) verifyOrRepairProjections(ctx context.Context, project string, expected localSnapshot) error {
	dir, err := store.projectDir(project, true)
	if err != nil {
		return err
	}
	snapshotErr := store.verifySnapshot(ctx, filepath.Join(dir, "snapshot.json"), expected)
	if snapshotErr != nil && !errors.Is(snapshotErr, os.ErrNotExist) && !errors.Is(snapshotErr, errProjectionStale) {
		return fieldError("integrity", "store.snapshot", "projection", snapshotErr)
	}
	if errors.Is(snapshotErr, errProjectionStale) {
		var persisted localSnapshot
		raw, readErr := store.readObject(filepath.Join(dir, "snapshot.json"), false, "snapshot.read")
		if readErr == nil && json.Unmarshal(raw, &persisted) == nil && persisted.Head.Sequence > expected.Head.Sequence {
			return fieldError("integrity", "store.journal", "truncated", nil)
		}
	}
	snapshotOK := snapshotErr == nil
	indexOK := store.verifyIndex(ctx, filepath.Join(dir, "index.json"), expected) == nil
	if snapshotOK && indexOK {
		return nil
	}
	return store.publish(ctx, project, expected)
}

// verifySnapshot authenticates a snapshot and requires exact agreement with journal replay.
func (store *LocalStore) verifySnapshot(ctx context.Context, path string, expected localSnapshot) error {
	raw, err := store.readObject(path, false, "snapshot.read")
	if err != nil {
		return err
	}
	var snapshot localSnapshot
	if json.Unmarshal(raw, &snapshot) != nil {
		return errors.New("invalid")
	}
	auth := snapshot.Authenticator
	key := snapshot.KeyID
	snapshot.KeyID, snapshot.Authenticator = "", ""
	canonical, _ := json.Marshal(snapshot)
	if store.integrity.Verify(ctx, key, canonical, auth) != nil {
		return errors.New("auth")
	}
	expected.KeyID, expected.Authenticator = "", ""
	if digestCanonical(snapshot) != digestCanonical(expected) {
		return errProjectionStale
	}
	return nil
}

// verifyIndex authenticates the derived index and matches its head and record references to replay.
func (store *LocalStore) verifyIndex(ctx context.Context, path string, expected localSnapshot) error {
	raw, err := store.readObject(path, false, "index.read")
	if err != nil {
		return err
	}
	var index localIndex
	if json.Unmarshal(raw, &index) != nil {
		return errors.New("invalid")
	}
	auth, key := index.Authenticator, index.KeyID
	index.KeyID, index.Authenticator = "", ""
	canonical, _ := json.Marshal(index)
	if store.integrity.Verify(ctx, key, canonical, auth) != nil {
		return errors.New("auth")
	}
	if index.Head != expected.Head || index.ProjectID != expected.ProjectID {
		return errors.New("stale")
	}
	for _, reference := range index.References {
		record, ok := expected.Current[reference.ID]
		if !ok || record.Version != reference.Version || record.RecordDigest != reference.Digest {
			return errors.New("poisoned")
		}
	}
	if len(index.References) != len(expected.Current) {
		return errors.New("stale")
	}
	return nil
}

// rewriteJournal replaces only a damaged unconfirmed tail with the verified prefix plus witness-bound staged event.
func (store *LocalStore) rewriteJournal(ctx context.Context, project string, events []EventEnvelope) error {
	dir, err := store.projectDir(project, true)
	if err != nil {
		return err
	}
	var raw bytes.Buffer
	for _, event := range events {
		line, marshalErr := json.Marshal(event)
		if marshalErr != nil {
			return fieldError("internal", "store.journal.rewrite", "encoding", marshalErr)
		}
		raw.Write(line)
		raw.WriteByte('\n')
	}
	return store.atomicBytes(ctx, dir, "journal.log", raw.Bytes(), "journal.recover")
}

// atomicJSON encodes one bounded object before delegating its durable publication to the shared byte writer.
func (store *LocalStore) atomicJSON(ctx context.Context, dir, name string, value any, boundary string) error {
	raw, err := json.Marshal(value)
	if err != nil {
		return fieldError("internal", "store.publish", name, err)
	}
	raw = append(raw, '\n')
	return store.atomicBytes(ctx, dir, name, raw, boundary)
}

// atomicBytes writes one exclusive owned temporary, syncs it, atomically replaces the target, and syncs its parent.
func (store *LocalStore) atomicBytes(ctx context.Context, dir, name string, raw []byte, boundary string) error {
	if err := ctx.Err(); err != nil {
		return fieldError("cancelled", "store.publish", name, err)
	}
	directoryIdentity, err := memoryDirectoryIdentity(dir)
	if err != nil || store.verifyStorageIdentity(dir) != nil {
		return fieldError("path", "store.identity", name, err)
	}
	directory, err := store.openStoreDirectory(dir, true)
	if err != nil {
		return fieldError("path", "store.identity", name, err)
	}
	defer directory.Close()
	tmpName := ".featureproof-owned-" + randomToken() + ".tmp"
	tmp := filepath.Join(dir, tmpName)
	if store.boundary != nil {
		store.boundary(boundary+".before_temp", tmp)
	}
	file, err := directory.CreateExclusive(tmpName)
	if err != nil {
		return fieldError("unavailable", "store.publish", name, err)
	}
	temporaryIdentity, err := memoryOpenFileIdentity(file)
	if err != nil {
		_ = file.Close()
		return err
	}
	verifyDirectory := func(stage string) error {
		if store.boundary != nil {
			store.boundary(boundary+"."+stage, tmp)
		}
		currentIdentity, identityErr := directory.Identity()
		if identityErr != nil || currentIdentity != directoryIdentity || directory.RootIdentity() == "" || store.verifyStorageIdentity(dir) != nil {
			return fieldError("path", "store.identity", name, identityErr)
		}
		resolved, resolveErr := store.openStoreDirectory(dir, false)
		if resolveErr != nil {
			return fieldError("path", "store.identity", name, resolveErr)
		}
		resolvedIdentity, resolveIdentityErr := resolved.Identity()
		_ = resolved.Close()
		if resolveIdentityErr != nil || resolvedIdentity != directoryIdentity {
			return fieldError("path", "store.identity", name, resolveIdentityErr)
		}
		return nil
	}
	ok := false
	defer func() {
		_ = file.Close()
		if !ok {
			if currentIdentity, identityErr := memoryDirectoryIdentity(dir); identityErr == nil && currentIdentity == directoryIdentity && store.verifyStorageIdentity(dir) == nil {
				_ = directory.Remove(tmpName)
			}
		}
	}()
	if err = verifyDirectory("after_temp_open"); err != nil {
		return err
	}
	written, writeErr := file.Write(raw)
	if writeErr != nil || written != len(raw) {
		if writeErr == nil {
			writeErr = io.ErrShortWrite
		}
		return fieldError("unavailable", "store.publish", name, writeErr)
	}
	if err = verifyDirectory("after_temp_write"); err != nil {
		return err
	}
	if err = file.Sync(); err != nil {
		return fieldError("unavailable", "store.publish", name, err)
	}
	if err = verifyDirectory("after_temp_sync"); err != nil {
		return err
	}
	if existing, existingErr := directory.OpenRead(name); existingErr == nil {
		_ = existing.Close()
	} else if !errors.Is(existingErr, os.ErrNotExist) {
		return fieldError("path", "store.identity", name, existingErr)
	}
	if store.boundary != nil {
		store.boundary(boundary+".before_replace", filepath.Join(dir, name))
	}
	if err = verifyDirectory("replace_checked"); err != nil {
		return err
	}
	reopened, reopenErr := directory.OpenRead(tmpName)
	if reopenErr != nil {
		return fieldError("path", "store.identity", "temporary", reopenErr)
	}
	reopenedIdentity, identityErr := memoryOpenFileIdentity(reopened)
	_ = reopened.Close()
	if identityErr != nil || reopenedIdentity != temporaryIdentity {
		return fieldError("path", "store.identity", "temporary", identityErr)
	}
	if store.boundary != nil {
		store.boundary(boundary+".after_source_check", tmp)
	}
	if err = directory.Replace(tmpName, name); err != nil {
		return fieldError("unavailable", "store.publish", name, err)
	}
	published, publishErr := directory.OpenRead(name)
	if publishErr != nil {
		return fieldError("path", "store.identity", "published", publishErr)
	}
	publishedIdentity, identityErr := memoryOpenFileIdentity(published)
	_ = published.Close()
	if identityErr != nil || publishedIdentity != temporaryIdentity {
		_ = directory.Remove(name)
		return fieldError("path", "store.identity", "published", identityErr)
	}
	if err = file.Close(); err != nil {
		return fieldError("unavailable", "store.publish", name, err)
	}
	_ = directory.Sync()
	ok = true
	if err = verifyDirectory("after_replace"); err != nil {
		return err
	}
	return nil
}

// readObject opens the effective no-follow handle and checks its directory and storage root around the read.
func (store *LocalStore) readObject(path string, missingAllowed bool, boundary string) ([]byte, error) {
	dir := filepath.Dir(path)
	directoryIdentity, err := memoryDirectoryIdentity(dir)
	if err != nil {
		return nil, err
	}
	if err = store.verifyStorageIdentity(dir); err != nil {
		return nil, err
	}
	directory, err := store.openStoreDirectory(dir, false)
	if err != nil {
		return nil, err
	}
	defer directory.Close()
	if store.boundary != nil {
		store.boundary(boundary+".before_open", path)
	}
	file, err := directory.OpenRead(filepath.Base(path))
	if errors.Is(err, os.ErrNotExist) && missingAllowed {
		return nil, os.ErrNotExist
	}
	if err != nil {
		return nil, err
	}
	defer file.Close()
	raw, err := io.ReadAll(io.LimitReader(file, int64(store.limits.MaxDocumentBytes*store.limits.MaxRecords)+1))
	currentIdentity, identityErr := memoryDirectoryIdentity(dir)
	if err != nil || identityErr != nil || currentIdentity != directoryIdentity || store.verifyStorageIdentity(dir) != nil {
		return nil, fieldError("path", "store.identity", filepath.Base(path), identityErr)
	}
	return raw, nil
}

// verifyStorageIdentity proves the selected path is still below one of the two LocalStore-managed roots.
func (store *LocalStore) verifyStorageIdentity(path string) error {
	if pathContains(store.trustRoot, path) {
		identity, err := memoryDirectoryIdentity(store.trustRoot)
		if err != nil || identity != store.trustIdentity {
			return fieldError("path", "store.identity", "trust_root", err)
		}
		return nil
	}
	if pathContains(store.root, path) {
		return store.verifyRootIdentity()
	}
	return fieldError("path", "store.identity", "escape", nil)
}

// secureMkdirAll creates private directories and rejects links or unexpected objects in the resulting chain.
func secureMkdirAll(path string) error {
	clean := filepath.Clean(path)
	base := filepath.VolumeName(clean) + string(filepath.Separator)
	relative, err := filepath.Rel(base, clean)
	if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return fieldError("path", "store.path", "root", err)
	}
	current := base
	for _, part := range strings.Split(relative, string(filepath.Separator)) {
		if part == "" || part == "." {
			continue
		}
		current = filepath.Join(current, part)
		info, statErr := os.Lstat(current)
		if errors.Is(statErr, os.ErrNotExist) {
			if mkdirErr := os.Mkdir(current, 0700); mkdirErr != nil {
				return fieldError("unavailable", "store.path", "root", mkdirErr)
			}
			if permissionErr := secureMemoryPermissions(current); permissionErr != nil {
				return fieldError("path", "store.permissions", "root", permissionErr)
			}
			info, statErr = os.Lstat(current)
		}
		if statErr != nil || info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
			return fieldError("path", "store.path", "component", statErr)
		}
	}
	return nil
}

// insecurePermissions rejects group/world-writable durable directories on Unix while respecting Windows ACL semantics.
func insecurePermissions(info os.FileInfo) bool {
	return runtime.GOOS != "windows" && info.Mode().Perm()&0022 != 0
}

// verifyRegularFile rejects links, devices, directories, and multiply-linked mutable files where detectable.
func verifyRegularFile(path string, missingAllowed bool) error {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) && missingAllowed {
		return nil
	}
	if err != nil {
		return fieldError("path", "store.path", "file", err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return fieldError("path", "store.path", "file", nil)
	}
	if insecurePermissions(info) {
		return fieldError("path", "store.path", "permissions", nil)
	}
	if err = verifyFileIdentity(path); err != nil {
		return err
	}
	return nil
}

// envelopeDigest hashes the event without digest or authentication fields.
func envelopeDigest(event EventEnvelope) string {
	event.EventDigest, event.KeyID, event.Authenticator = "", "", ""
	return digestCanonical(event)
}

// historical locates one exact version in a verified snapshot.
func historical(snapshot localSnapshot, id string, version uint64) (Record, bool) {
	for _, record := range snapshot.History[id] {
		if record.Version == version {
			return cloneRecord(record), true
		}
	}
	return Record{}, false
}

// randomToken produces an unguessable flat owner/event identifier.
func randomToken() string {
	buffer := make([]byte, 16)
	if _, err := io.ReadFull(rand.Reader, buffer); err != nil {
		return hex.EncodeToString([]byte(time.Now().UTC().Format(time.RFC3339Nano)))
	}
	return hex.EncodeToString(buffer)
}
