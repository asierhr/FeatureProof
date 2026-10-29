package controlplane

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"sort"
	"time"
)

// BackendSet contains one complete adapter family chosen at composition.
type BackendSet struct {
	Mode     BackendMode
	Identity string
	Bindings BackendBindings
	Queue    JobQueue
	Ingress  IngressStore
	Effects  EffectStore
}

// SelectBackend validates that a run receives one coherent adapter identity.
func SelectBackend(set BackendSet) (BackendSet, error) {
	if (set.Mode != BackendLocal && set.Mode != BackendPostgres) || !safeID.MatchString(set.Identity) || set.Bindings == nil || set.Queue == nil || set.Ingress == nil || set.Effects == nil {
		return BackendSet{}, ErrInvalid
	}
	return set, nil
}

// ImportState is the closed offline migration state machine.
type ImportState string

const (
	// ImportFrozen leaves the source authoritative but rejects new mutations.
	ImportFrozen ImportState = "frozen"
	// ImportCandidate has copied data without acquiring authority.
	ImportCandidate ImportState = "candidate"
	// ImportVerified has passed counts, relations and digest verification.
	ImportVerified ImportState = "verified"
	// ImportCutover records the new authoritative epoch.
	ImportCutover ImportState = "cutover"
	// ImportAbandoned discards a pre-cutover candidate explicitly.
	ImportAbandoned ImportState = "abandoned"
)

// ImportManifest seals expected head, stream counts and digests without secrets.
type ImportManifest struct {
	Version            string            `json:"version"`
	ImportID           string            `json:"import_id"`
	RunID              string            `json:"run_id"`
	ProjectID          string            `json:"project_id"`
	SourceIdentity     string            `json:"source_identity"`
	ExpectedHeadDigest string            `json:"expected_head_digest"`
	ExpectedEpoch      uint64            `json:"expected_epoch,omitempty"`
	Counts             map[string]uint64 `json:"counts"`
	StreamDigests      map[string]string `json:"stream_digests"`
	RelationDigests    map[string]string `json:"relation_digests,omitempty"`
	EventDigests       map[string]string `json:"event_digests,omitempty"`
	State              ImportState       `json:"state"`
	CreatedAt          time.Time         `json:"created_at"`
	Digest             string            `json:"digest"`
}

// SealImportManifest canonicalizes detached maps and seals an offline snapshot.
func SealImportManifest(value ImportManifest) (ImportManifest, error) {
	value.CreatedAt = value.CreatedAt.UTC()
	if value.Version == "" {
		value.Version = ModelVersion
	}
	valid := value.State == ImportFrozen || value.State == ImportCandidate || value.State == ImportVerified || value.State == ImportCutover || value.State == ImportAbandoned
	if value.Version != ModelVersion || !safeID.MatchString(value.ImportID) || !safeID.MatchString(value.RunID) || !safeID.MatchString(value.ProjectID) || !safeID.MatchString(value.SourceIdentity) || !digestPattern.MatchString(value.ExpectedHeadDigest) || !valid || value.CreatedAt.IsZero() {
		return ImportManifest{}, ErrInvalid
	}
	value.Counts = cloneCounts(value.Counts)
	value.StreamDigests = cloneDigests(value.StreamDigests)
	value.RelationDigests = cloneDigests(value.RelationDigests)
	value.EventDigests = cloneDigests(value.EventDigests)
	for _, collection := range []map[string]string{value.StreamDigests, value.RelationDigests, value.EventDigests} {
		for key, item := range collection {
			if !safeID.MatchString(key) || !digestPattern.MatchString(item) {
				return ImportManifest{}, ErrInvalid
			}
		}
	}
	value.Digest = ""
	encoded, err := json.Marshal(value)
	if err != nil || len(encoded) > MaxCanonicalBytes {
		return ImportManifest{}, ErrInvalid
	}
	value.Digest = digest(value)
	return value, nil
}

// ImportDocument is one byte-exact ordered source record with an explicit relation key.
type ImportDocument struct {
	Stream      string `json:"stream"`
	Ordinal     uint64 `json:"ordinal"`
	RelationKey string `json:"relation_key"`
	Document    []byte `json:"document"`
	Digest      string `json:"digest"`
}

// ImportCutoverReceipt proves which frozen head was atomically superseded by one verified epoch.
type ImportCutoverReceipt struct {
	ImportID      string    `json:"import_id"`
	RunID         string    `json:"run_id"`
	SourceHead    string    `json:"source_head_digest"`
	TargetHead    string    `json:"target_head_digest"`
	ArchivedEpoch uint64    `json:"archived_epoch"`
	ActiveEpoch   uint64    `json:"active_epoch"`
	CommittedAt   time.Time `json:"committed_at"`
	Digest        string    `json:"digest"`
}

// SealImportDocument validates byte-exact import identity without interpreting consumer-owned documents.
func SealImportDocument(value ImportDocument) (ImportDocument, error) {
	if !safeID.MatchString(value.Stream) || !safeID.MatchString(value.RelationKey) || len(value.Document) == 0 || len(value.Document) > MaxCanonicalBytes {
		return ImportDocument{}, ErrInvalid
	}
	value.Document = append([]byte(nil), value.Document...)
	sum := sha256.Sum256(value.Document)
	actual := hex.EncodeToString(sum[:])
	if value.Digest != "" && value.Digest != actual {
		return ImportDocument{}, ErrIntegrity
	}
	value.Digest = actual
	return value, nil
}

// SealImportCutoverReceipt authenticates the monotonic backend transition outcome.
func SealImportCutoverReceipt(value ImportCutoverReceipt) (ImportCutoverReceipt, error) {
	value.CommittedAt = value.CommittedAt.UTC().Truncate(time.Microsecond)
	if !safeID.MatchString(value.ImportID) || !safeID.MatchString(value.RunID) || !digestPattern.MatchString(value.SourceHead) || !digestPattern.MatchString(value.TargetHead) || value.ArchivedEpoch == 0 || value.ActiveEpoch != value.ArchivedEpoch+1 || value.CommittedAt.IsZero() {
		return ImportCutoverReceipt{}, ErrInvalid
	}
	value.Digest = ""
	value.Digest = digest(value)
	return value, nil
}

// AdvanceImport applies the closed offline state transition matrix.
func AdvanceImport(value ImportManifest, next ImportState) (ImportManifest, error) {
	allowed := value.State == ImportFrozen && (next == ImportCandidate || next == ImportAbandoned) || value.State == ImportCandidate && (next == ImportVerified || next == ImportAbandoned) || value.State == ImportVerified && next == ImportCutover
	if !allowed {
		return ImportManifest{}, ErrConflict
	}
	value.State = next
	value.Digest = ""
	return SealImportManifest(value)
}

// cloneCounts detaches import counts from subsequent caller mutation.
func cloneCounts(source map[string]uint64) map[string]uint64 {
	result := make(map[string]uint64, len(source))
	for key, value := range source {
		result[key] = value
	}
	return result
}

// cloneDigests detaches and validates per-stream snapshot identities.
func cloneDigests(source map[string]string) map[string]string {
	result := make(map[string]string, len(source))
	for key, value := range source {
		result[key] = value
	}
	return result
}

// ImportStore persists resumable candidate state without backend authority.
type ImportStore interface {
	SaveImport(context.Context, ImportManifest, string) (ImportManifest, error)
	LoadImport(context.Context, string) (ImportManifest, error)
}

// OfflineImportSource is the frozen local authority consumed by the explicit
// offline migration controller; it never exposes arbitrary filesystem paths.
type OfflineImportSource interface {
	FreezeLocalStores(context.Context, string, string, string, string, uint64, time.Time) (ImportManifest, error)
	ReadImportPage(context.Context, string, string, uint64, int) ([]ImportDocument, error)
	Archive(context.Context, ImportCutoverReceipt) error
}

// OfflineImportTarget is the candidate-only PostgreSQL protocol used before
// the final head CAS grants backend authority.
type OfflineImportTarget interface {
	SaveImport(context.Context, ImportManifest, string) (ImportManifest, error)
	SaveImportPage(context.Context, string, []ImportDocument) error
	VerifyImport(context.Context, string) (ImportManifest, error)
	CutoverImport(context.Context, string, time.Time) (ImportCutoverReceipt, error)
	LoadImportCutoverReceipt(context.Context, string) (ImportCutoverReceipt, error)
}

// OperatorAuthorizer authenticates one explicit operational controller call
// before it can inspect state or mutate retention/import authority.
type OperatorAuthorizer interface {
	Authorize(context.Context, string, string) error
}

// OfflineImportRequest contains only operator-selected identities and finite
// pagination; it carries neither credentials nor arbitrary stream names.
type OfflineImportRequest struct {
	ImportID, RunID, ProjectID, ExpectedHead string
	ExpectedEpoch                            uint64
	PageSize                                 int
	Now                                      time.Time
	ReconcileTimeout                         time.Duration
}

// OfflineImportController owns freeze, resumable copy, verification, cutover
// reconciliation and source archival as one explicit operator workflow.
type OfflineImportController struct {
	Source     OfflineImportSource
	Target     OfflineImportTarget
	Authorizer OperatorAuthorizer
}

// Run copies every manifest-declared stream in stable order and resolves an
// unknown cutover result from the durable receipt before archiving the source.
func (controller OfflineImportController) Run(ctx context.Context, request OfflineImportRequest) (ImportCutoverReceipt, error) {
	if controller.Source == nil || controller.Target == nil || controller.Authorizer == nil || request.PageSize <= 0 || request.PageSize > 1000 || request.Now.IsZero() || request.ReconcileTimeout <= 0 {
		return ImportCutoverReceipt{}, ErrInvalid
	}
	if err := controller.Authorizer.Authorize(ctx, "offline_import", request.ProjectID); err != nil {
		return ImportCutoverReceipt{}, ErrUnauthorized
	}
	manifest, err := controller.Source.FreezeLocalStores(ctx, request.ImportID, request.RunID, request.ProjectID, request.ExpectedHead, request.ExpectedEpoch, request.Now)
	if err != nil {
		return ImportCutoverReceipt{}, err
	}
	if _, err = controller.Target.SaveImport(ctx, manifest, ""); err != nil {
		return ImportCutoverReceipt{}, err
	}
	streams := orderedImportStreams(manifest.Counts)
	for _, stream := range streams {
		for after := uint64(0); after < manifest.Counts[stream]; {
			page, pageErr := controller.Source.ReadImportPage(ctx, manifest.ImportID, stream, after, request.PageSize)
			if pageErr != nil || len(page) == 0 {
				if pageErr != nil {
					return ImportCutoverReceipt{}, pageErr
				}
				return ImportCutoverReceipt{}, ErrIntegrity
			}
			if pageErr = controller.Target.SaveImportPage(ctx, manifest.ImportID, page); pageErr != nil {
				return ImportCutoverReceipt{}, pageErr
			}
			after += uint64(len(page))
		}
	}
	if _, err = controller.Target.VerifyImport(ctx, manifest.ImportID); err != nil {
		return ImportCutoverReceipt{}, err
	}
	receipt, err := controller.Target.CutoverImport(ctx, manifest.ImportID, request.Now)
	if err != nil {
		reconcileContext, cancel := context.WithTimeout(context.WithoutCancel(ctx), request.ReconcileTimeout)
		receipt, err = controller.Target.LoadImportCutoverReceipt(reconcileContext, manifest.ImportID)
		cancel()
		if err != nil {
			return ImportCutoverReceipt{}, err
		}
	}
	archiveContext, cancel := context.WithTimeout(context.WithoutCancel(ctx), request.ReconcileTimeout)
	err = controller.Source.Archive(archiveContext, receipt)
	cancel()
	if err != nil {
		return ImportCutoverReceipt{}, err
	}
	return receipt, nil
}

// orderedImportStreams preserves foreign-key dependencies while retaining a
// deterministic order for future or opaque streams.
func orderedImportStreams(counts map[string]uint64) []string {
	priority := []string{"workflow", "events", "artifacts", "executables", "approval", "functional", "environment", "capacity", "cleanup", "correction", "packages", "github"}
	seen := make(map[string]bool, len(counts))
	streams := make([]string, 0, len(counts))
	for _, stream := range priority {
		if _, ok := counts[stream]; ok {
			streams, seen[stream] = append(streams, stream), true
		}
	}
	remaining := make([]string, 0, len(counts)-len(streams))
	for stream := range counts {
		if !seen[stream] {
			remaining = append(remaining, stream)
		}
	}
	sort.Strings(remaining)
	return append(streams, remaining...)
}

// RetentionStore owns reference-safe bounded deletion and tombstone preservation.
type RetentionStore interface {
	DeleteTerminalBefore(context.Context, int64, int) (int, error)
}

// RunRetention performs no deletion unless composition supplied a valid policy.
func RunRetention(ctx context.Context, store RetentionStore, policy *RetentionPolicy) (int, error) {
	if policy == nil {
		return 0, nil
	}
	if store == nil {
		return 0, ErrInvalid
	}
	if err := policy.Validate(); err != nil {
		return 0, err
	}
	return store.DeleteTerminalBefore(ctx, policy.BeforeUnix, policy.BatchSize)
}
