package controlplane

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
	"sort"
	"strings"
	"time"
)

// LocalImportStore is the filesystem-backed offline source whose durable freeze prevents dual writes.
type LocalImportStore struct {
	root      string
	identity  string
	exporters []LocalSnapshotExporter
}

// LocalSnapshotExport is one consumer-authenticated DTO plus the source paths
// whose private representation it replaces inside the frozen manifest.
type LocalSnapshotExport struct {
	Stream       string
	RelationKey  string
	Document     []byte
	ClaimedPaths []string
}

// LocalSnapshotExporter converts consumer-owned local state into a secret-free
// typed document while the caller holds the global offline-import exclusion.
type LocalSnapshotExporter func(context.Context, string) ([]LocalSnapshotExport, error)

// localImportState is the atomic source receipt and byte-exact stream inventory persisted under the source root.
type localImportState struct {
	Frozen   bool                        `json:"frozen"`
	Archived bool                        `json:"archived"`
	ImportID string                      `json:"import_id,omitempty"`
	Manifest *ImportManifest             `json:"manifest,omitempty"`
	Receipt  *ImportCutoverReceipt       `json:"receipt,omitempty"`
	Streams  map[string][]ImportDocument `json:"streams"`
}

// functionalImportSource mirrors the authenticated local envelope only long
// enough to verify it with source-private material; claims are deliberately
// excluded from the transferable representation because leases cannot survive
// an offline backend cutover.
type functionalImportSource struct {
	Version   uint64                     `json:"Version"`
	MAC       string                     `json:"MAC"`
	Claims    map[string]json.RawMessage `json:"Claims"`
	Intents   map[string]json.RawMessage `json:"Intents"`
	Terminals map[string]json.RawMessage `json:"Terminals"`
	Events    []json.RawMessage          `json:"Events"`
}

// functionalImportDocument is the secret-free DTO materialized by PostgreSQL
// after the source MAC has been verified under the global freeze.
type functionalImportDocument struct {
	Version   uint64                     `json:"version"`
	Intents   map[string]json.RawMessage `json:"intents"`
	Terminals map[string]json.RawMessage `json:"terminals"`
	Events    []json.RawMessage          `json:"events"`
}

// OpenLocalImportStore opens an explicit local source without selecting or contacting PostgreSQL.
func OpenLocalImportStore(root, identity string) (*LocalImportStore, error) {
	if root == "" || !safeID.MatchString(identity) {
		return nil, ErrInvalid
	}
	if err := os.MkdirAll(root, 0o700); err != nil {
		return nil, ErrUnavailable
	}
	store := &LocalImportStore{root: root, identity: identity}
	if _, err := os.Stat(store.statePath()); errors.Is(err, os.ErrNotExist) {
		if err = store.writeState(localImportState{Streams: map[string][]ImportDocument{}}); err != nil {
			return nil, err
		}
	} else if err != nil {
		return nil, ErrUnavailable
	}
	return store, nil
}

// RegisterSnapshotExporter adds one composition-owned consumer exporter before
// freeze; registration is rejected after any source state has been captured.
func (store *LocalImportStore) RegisterSnapshotExporter(exporter LocalSnapshotExporter) error {
	if store == nil || exporter == nil {
		return ErrInvalid
	}
	state, err := store.readState()
	if err != nil {
		return err
	}
	if state.Frozen || state.Archived || len(state.Streams) != 0 {
		return ErrConflict
	}
	store.exporters = append(store.exporters, exporter)
	return nil
}

// EnsureLocalImportWritable lets existing local adapters reject mutations while a shared root is frozen or archived.
func EnsureLocalImportWritable(root string) error {
	release, err := AcquireLocalImportWrite(context.Background(), root)
	if err != nil {
		return err
	}
	release()
	return nil
}

// AcquireLocalImportWrite holds the source-wide exclusion from the final
// frozen-state check until the caller has durably published its local write.
func AcquireLocalImportWrite(ctx context.Context, root string) (func(), error) {
	if root == "" {
		return nil, ErrInvalid
	}
	current, err := filepath.Abs(root)
	if err != nil {
		return nil, ErrInvalid
	}
	for {
		statePath := filepath.Join(current, "featureproof-import.json")
		data, readErr := os.ReadFile(statePath)
		if readErr == nil {
			if err = ctx.Err(); err != nil {
				return nil, err
			}
			token := make([]byte, 32)
			if _, err = rand.Read(token); err != nil {
				return nil, ErrUnavailable
			}
			lockPath := filepath.Join(current, ".featureproof-import.lock")
			lock, lockErr := os.OpenFile(lockPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
			if lockErr != nil {
				return nil, ErrConflict
			}
			if _, lockErr = lock.Write(token); lockErr == nil {
				lockErr = lock.Sync()
			}
			if closeErr := lock.Close(); lockErr == nil {
				lockErr = closeErr
			}
			if lockErr != nil {
				_ = os.Remove(lockPath)
				return nil, ErrUnavailable
			}
			data, readErr = os.ReadFile(statePath)
			if readErr != nil {
				_ = os.Remove(lockPath)
				return nil, ErrUnavailable
			}
			var state localImportState
			if StrictDecode(data, &state) != nil {
				_ = os.Remove(lockPath)
				return nil, ErrIntegrity
			}
			if state.Frozen || state.Archived {
				_ = os.Remove(lockPath)
				return nil, ErrConflict
			}
			release := func() {
				owned, readLockErr := os.ReadFile(lockPath)
				if readLockErr == nil && bytes.Equal(owned, token) {
					_ = os.Remove(lockPath)
				}
			}
			return release, nil
		}
		if !errors.Is(readErr, os.ErrNotExist) {
			return nil, ErrUnavailable
		}
		parent := filepath.Dir(current)
		if parent == current {
			break
		}
		current = parent
	}
	return func() {}, nil
}

// Put appends one source document only while the local authority is writable.
func (store *LocalImportStore) Put(ctx context.Context, value ImportDocument) error {
	sealed, err := SealImportDocument(value)
	if err != nil {
		return err
	}
	return store.withLock(ctx, func(state *localImportState) error {
		if state.Frozen || state.Archived {
			return ErrConflict
		}
		items := state.Streams[sealed.Stream]
		if sealed.Ordinal != uint64(len(items)) {
			return ErrConflict
		}
		for _, item := range items {
			if item.RelationKey == sealed.RelationKey {
				return ErrConflict
			}
		}
		state.Streams[sealed.Stream] = append(items, sealed)
		return nil
	})
}

// SnapshotLocalStores captures every durable file below the explicit local
// root as a byte-exact, relation-keyed stream before the global freeze begins.
func (store *LocalImportStore) SnapshotLocalStores(ctx context.Context) error {
	if store == nil {
		return ErrInvalid
	}
	return store.withLock(ctx, func(state *localImportState) error {
		if state.Frozen || state.Archived || len(state.Streams) != 0 {
			return ErrConflict
		}
		return store.snapshotLocalStoresLocked(ctx, state)
	})
}

// FreezeLocalStores acquires the source-wide writer exclusion before reading
// any consumer-owned store and publishes the manifest in the same critical
// section, so no writable interval exists between export and freeze.
func (store *LocalImportStore) FreezeLocalStores(ctx context.Context, importID, runID, projectID, expectedHead string, expectedEpoch uint64, now time.Time) (ImportManifest, error) {
	var result ImportManifest
	if store == nil {
		return result, ErrInvalid
	}
	err := store.withLock(ctx, func(state *localImportState) error {
		if state.Archived {
			return ErrConflict
		}
		if state.Frozen {
			if state.ImportID != importID || state.Manifest == nil {
				return ErrConflict
			}
			result = *state.Manifest
			return nil
		}
		if len(state.Streams) != 0 {
			return ErrConflict
		}
		if err := store.snapshotLocalStoresLocked(ctx, state); err != nil {
			return err
		}
		manifest, err := store.sealFrozenManifest(importID, runID, projectID, expectedHead, expectedEpoch, now, state.Streams)
		if err != nil {
			return err
		}
		state.Frozen, state.ImportID, state.Manifest = true, importID, &manifest
		result = manifest
		return nil
	})
	return result, err
}

// snapshotLocalStoresLocked reads the closed typed export allowlist while the
// same cross-process exclusion used by local writers remains held.
func (store *LocalImportStore) snapshotLocalStoresLocked(ctx context.Context, state *localImportState) error {
	type localFile struct {
		relation string
		stream   string
		data     []byte
	}
	var files []localFile
	claimed := map[string]bool{}
	for _, exporter := range store.exporters {
		exports, exportErr := exporter(ctx, store.root)
		if exportErr != nil {
			return exportErr
		}
		for _, exported := range exports {
			if !validImportStream(exported.Stream) || !safeID.MatchString(exported.RelationKey) || len(exported.Document) == 0 || len(exported.Document) > MaxCanonicalBytes {
				return ErrIntegrity
			}
			files = append(files, localFile{relation: exported.RelationKey, stream: exported.Stream, data: append([]byte(nil), exported.Document...)})
			for _, path := range exported.ClaimedPaths {
				absolute, absoluteErr := filepath.Abs(path)
				if absoluteErr != nil || !pathWithinRoot(store.root, absolute) {
					return ErrIntegrity
				}
				claimed[filepath.Clean(absolute)] = true
			}
		}
	}
	err := filepath.WalkDir(store.root, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if entry.IsDir() {
			if path != store.root && pathClaimed(path, claimed) {
				return filepath.SkipDir
			}
			return nil
		}
		if pathClaimed(path, claimed) {
			return nil
		}
		name := entry.Name()
		if name == "featureproof-import.json" || name == ".flow-state.key" || name == "head.json" || strings.HasPrefix(name, ".featureproof-import") || strings.HasSuffix(name, ".lock") || strings.HasSuffix(name, ".tmp") {
			return nil
		}
		relative, err := filepath.Rel(store.root, path)
		if err != nil {
			return err
		}
		data, err := os.ReadFile(path)
		if err != nil || len(data) == 0 || len(data) > MaxCanonicalBytes || len(files) >= 10000 {
			return ErrIntegrity
		}
		stream, classified := localStoreStream(relative)
		if !classified {
			return ErrIntegrity
		}
		if stream == "functional" && strings.HasSuffix(strings.ToLower(name), ".flow.json") {
			key, keyErr := os.ReadFile(filepath.Join(filepath.Dir(path), ".flow-state.key"))
			if keyErr != nil || len(key) != 32 {
				return ErrIntegrity
			}
			data, keyErr = sanitizeFunctionalImport(data, key)
			if keyErr != nil {
				return keyErr
			}
		}
		relationPath := []byte(filepath.ToSlash(relative))
		if len(relationPath) == 0 || len(relationPath) > 63 {
			return ErrIntegrity
		}
		files = append(files, localFile{relation: "p" + hex.EncodeToString(relationPath), stream: stream, data: data})
		return nil
	})
	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return err
		}
		if errors.Is(err, ErrIntegrity) {
			return ErrIntegrity
		}
		return ErrUnavailable
	}
	sort.Slice(files, func(i, j int) bool {
		if files[i].stream == files[j].stream {
			return files[i].relation < files[j].relation
		}
		return files[i].stream < files[j].stream
	})
	for _, file := range files {
		document, sealErr := SealImportDocument(ImportDocument{Stream: file.stream, Ordinal: uint64(len(state.Streams[file.stream])), RelationKey: file.relation, Document: file.data})
		if sealErr != nil {
			return sealErr
		}
		state.Streams[file.stream] = append(state.Streams[file.stream], document)
	}
	return nil
}

// validImportStream closes the typed-export vocabulary to materializers owned
// by the target adapter instead of accepting caller-defined table selectors.
func validImportStream(stream string) bool {
	switch stream {
	case "workflow", "events", "artifacts", "executables", "approval", "functional", "environment", "capacity", "cleanup", "correction", "packages", "github":
		return true
	default:
		return false
	}
}

// pathWithinRoot prevents a consumer exporter from claiming or suppressing
// files outside the explicitly selected offline source.
func pathWithinRoot(root, candidate string) bool {
	relative, err := filepath.Rel(root, candidate)
	return err == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator)) && !filepath.IsAbs(relative)
}

// pathClaimed reports whether a source path is replaced by a typed exporter;
// directory claims cover their complete private subtree.
func pathClaimed(path string, claimed map[string]bool) bool {
	clean := filepath.Clean(path)
	for prefix := range claimed {
		if clean == prefix || strings.HasPrefix(clean, prefix+string(filepath.Separator)) {
			return true
		}
	}
	return false
}

// sanitizeFunctionalImport authenticates the consumer-owned snapshot before
// producing a canonical DTO that contains neither the MAC key nor stale lease
// claims and can therefore cross the offline import boundary safely.
func sanitizeFunctionalImport(data, key []byte) ([]byte, error) {
	var source functionalImportSource
	if StrictDecode(data, &source) != nil || source.Version == 0 || len(source.MAC) != 64 || source.Claims == nil || source.Intents == nil || source.Terminals == nil || source.Events == nil {
		return nil, ErrIntegrity
	}
	provided, err := hex.DecodeString(source.MAC)
	if err != nil {
		return nil, ErrIntegrity
	}
	source.MAC = ""
	canonicalSource, err := json.Marshal(source)
	if err != nil {
		return nil, ErrIntegrity
	}
	authenticator := hmac.New(sha256.New, key)
	_, _ = authenticator.Write(canonicalSource)
	if !hmac.Equal(provided, authenticator.Sum(nil)) {
		return nil, ErrIntegrity
	}
	document, err := json.Marshal(functionalImportDocument{Version: source.Version, Intents: source.Intents, Terminals: source.Terminals, Events: source.Events})
	if err != nil || len(document) > MaxCanonicalBytes {
		return nil, ErrIntegrity
	}
	return document, nil
}

// localStoreStream maps exact path segments from the closed durable-store
// allowlist; unknown files are rejected so credentials and unrelated files can
// never enter an import candidate by accident.
func localStoreStream(path string) (string, bool) {
	segments := strings.Split(strings.ToLower(filepath.ToSlash(path)), "/")
	productPrefixes := []struct {
		prefix string
		stream string
	}{{"workflows/plans/", "artifacts"}, {"workflows/private/", "executables"}, {"workflows/approval/", "approval"}, {"workflows/functional-flow/", "functional"}, {"workflows/", "workflow"}, {"environment/records/", "environment"}, {"environment/events/", "environment"}}
	normalized := strings.Join(segments, "/")
	for _, mapping := range productPrefixes {
		if strings.HasPrefix(normalized, mapping.prefix) {
			return mapping.stream, true
		}
	}
	aliases := map[string]string{"workflows": "workflow", "workflow": "workflow", "events": "events", "approval": "approval", "functional-flow": "functional", "functional": "functional", "environment": "environment", "capacity": "capacity", "cleanup": "cleanup", "correction": "correction", "packages": "packages", "plans": "artifacts", "artifacts": "artifacts", "executables": "executables", "github-pr": "github", "github": "github"}
	for _, segment := range segments {
		segment = strings.TrimSuffix(segment, filepath.Ext(segment))
		if stream, ok := aliases[segment]; ok {
			return stream, true
		}
	}
	return "", false
}

// Freeze persists a canonical immutable manifest before any candidate data is copied.
func (store *LocalImportStore) Freeze(ctx context.Context, importID, runID, projectID, expectedHead string, expectedEpoch uint64, now time.Time) (ImportManifest, error) {
	var result ImportManifest
	err := store.withLock(ctx, func(state *localImportState) error {
		if state.Archived {
			return ErrConflict
		}
		if state.Frozen {
			if state.ImportID != importID || state.Manifest == nil {
				return ErrConflict
			}
			result = *state.Manifest
			return nil
		}
		manifest, sealErr := store.sealFrozenManifest(importID, runID, projectID, expectedHead, expectedEpoch, now, state.Streams)
		if sealErr != nil {
			return sealErr
		}
		state.Frozen, state.ImportID, state.Manifest = true, importID, &manifest
		result = manifest
		return nil
	})
	return result, err
}

// sealFrozenManifest derives the immutable import inventory exclusively from
// documents captured under the source-wide exclusion.
func (store *LocalImportStore) sealFrozenManifest(importID, runID, projectID, expectedHead string, expectedEpoch uint64, now time.Time, documents map[string][]ImportDocument) (ImportManifest, error) {
	counts, streams, relations, events := map[string]uint64{}, map[string]string{}, map[string]string{}, map[string]string{}
	for name, items := range documents {
		counts[name] = uint64(len(items))
		streams[name] = importDocumentsDigest(items, false)
		relations[name] = importDocumentsDigest(items, true)
		if name == "events" || name == "job_events" {
			events[name] = streams[name]
		}
	}
	return SealImportManifest(ImportManifest{ImportID: importID, RunID: runID, ProjectID: projectID, SourceIdentity: store.identity, ExpectedHeadDigest: expectedHead, ExpectedEpoch: expectedEpoch, Counts: counts, StreamDigests: streams, RelationDigests: relations, EventDigests: events, State: ImportFrozen, CreatedAt: now})
}

// ReadImportPage returns detached frozen records in stable ordinal order for resumable copying.
func (store *LocalImportStore) ReadImportPage(ctx context.Context, importID, stream string, after uint64, limit int) ([]ImportDocument, error) {
	if limit <= 0 || limit > 1000 {
		return nil, ErrInvalid
	}
	state, err := store.readState()
	if err != nil {
		return nil, err
	}
	if err = ctx.Err(); err != nil {
		return nil, err
	}
	if !state.Frozen || state.ImportID != importID || state.Manifest == nil {
		return nil, ErrConflict
	}
	items := state.Streams[stream]
	start := int(after)
	if start > len(items) {
		return nil, ErrInvalid
	}
	end := start + limit
	if end > len(items) {
		end = len(items)
	}
	result := make([]ImportDocument, end-start)
	for index, item := range items[start:end] {
		item.Document = append([]byte(nil), item.Document...)
		result[index] = item
	}
	return result, nil
}

// Unfreeze abandons only a pre-cutover candidate and restores the unchanged local epoch.
func (store *LocalImportStore) Unfreeze(ctx context.Context, importID string) error {
	return store.withLock(ctx, func(state *localImportState) error {
		if state.Archived || !state.Frozen || state.ImportID != importID {
			return ErrConflict
		}
		state.Frozen, state.ImportID, state.Manifest = false, "", nil
		return nil
	})
}

// Archive records the PostgreSQL cutover receipt under the source freeze, so
// local writers stay permanently disabled after authority moves backends.
func (store *LocalImportStore) Archive(ctx context.Context, receipt ImportCutoverReceipt) error {
	sealed, err := SealImportCutoverReceipt(receipt)
	if err != nil {
		return err
	}
	return store.withLock(ctx, func(state *localImportState) error {
		if !state.Frozen || state.ImportID != sealed.ImportID {
			return ErrConflict
		}
		if state.Receipt != nil {
			if state.Receipt.Digest != sealed.Digest {
				return ErrConflict
			}
			return nil
		}
		state.Archived, state.Receipt = true, &sealed
		return nil
	})
}

// importDocumentsDigest computes ordered stream or relation identity from sealed records.
func importDocumentsDigest(documents []ImportDocument, relations bool) string {
	hash := sha256.New()
	ordered := append([]ImportDocument(nil), documents...)
	sort.Slice(ordered, func(i, j int) bool { return ordered[i].Ordinal < ordered[j].Ordinal })
	for _, item := range ordered {
		if relations {
			_, _ = hash.Write([]byte(item.RelationKey))
		} else {
			_, _ = hash.Write(item.Document)
		}
	}
	return hex.EncodeToString(hash.Sum(nil))
}

// statePath identifies the one atomic local import state document.
func (store *LocalImportStore) statePath() string {
	return filepath.Join(store.root, "featureproof-import.json")
}

// readState strictly decodes local durable state before it can govern writability.
func (store *LocalImportStore) readState() (localImportState, error) {
	data, err := os.ReadFile(store.statePath())
	if err != nil {
		return localImportState{}, ErrUnavailable
	}
	var state localImportState
	if StrictDecode(data, &state) != nil || state.Streams == nil {
		return localImportState{}, ErrIntegrity
	}
	return state, nil
}

// writeState atomically replaces the local receipt after syncing its temporary file.
func (store *LocalImportStore) writeState(state localImportState) error {
	data, err := json.Marshal(state)
	if err != nil || len(data) > MaxCanonicalBytes {
		return ErrInvalid
	}
	temporary, err := os.CreateTemp(store.root, ".featureproof-import-*")
	if err != nil {
		return ErrUnavailable
	}
	name := temporary.Name()
	defer os.Remove(name)
	if temporary.Chmod(0o600) != nil {
		temporary.Close()
		return ErrUnavailable
	}
	if _, err = temporary.Write(data); err == nil {
		err = temporary.Sync()
	}
	if closeErr := temporary.Close(); err == nil {
		err = closeErr
	}
	if err != nil || os.Rename(name, store.statePath()) != nil {
		return ErrUnavailable
	}
	return nil
}

// withLock serializes cross-process local mutations and persists one all-or-nothing state transition.
func (store *LocalImportStore) withLock(ctx context.Context, callback func(*localImportState) error) error {
	if store == nil || callback == nil {
		return ErrInvalid
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	lockPath := filepath.Join(store.root, ".featureproof-import.lock")
	lock, err := os.OpenFile(lockPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return ErrConflict
	}
	_ = lock.Close()
	defer os.Remove(lockPath)
	state, err := store.readState()
	if err != nil {
		return err
	}
	if err = callback(&state); err != nil {
		return err
	}
	return store.writeState(state)
}
