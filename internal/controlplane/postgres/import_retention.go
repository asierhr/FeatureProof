package postgres

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/asierhr/featureProof/internal/controlplane"
	"github.com/asierhr/featureProof/internal/correction"
	"github.com/asierhr/featureProof/internal/environment"
	cleanup "github.com/asierhr/featureProof/internal/environment/cleanup"
	"github.com/asierhr/featureProof/internal/functionalflow"
	"github.com/asierhr/featureProof/internal/githubpr"
	"github.com/asierhr/featureProof/internal/planapproval"
	"github.com/asierhr/featureProof/internal/validationworkflow"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// functionalImportDocument is the secret-free, source-authenticated transfer
// object emitted by LocalImportStore after it has discarded ephemeral claims.
type functionalImportDocument struct {
	Version   uint64                               `json:"version"`
	Intents   map[string]functionalflow.Intent     `json:"intents"`
	Terminals map[string]functionalflow.StepRecord `json:"terminals"`
	Events    []functionalflow.Event               `json:"events"`
}

// approvalImportRequest contains only the coordinates needed to decide whether
// a sanitized local request is still pending and therefore non-recoverable.
type approvalImportRequest struct {
	RunID    string `json:"RunID"`
	Revision uint64 `json:"Revision"`
}

// environmentImportRecord mirrors the consumer's sealed version wrapper while
// retaining its digest chain for verification in candidate tables.
type environmentImportRecord struct {
	Digest         string             `json:"digest"`
	PreviousDigest string             `json:"previous_digest,omitempty"`
	EventID        string             `json:"event_id,omitempty"`
	EventDigest    string             `json:"event_digest,omitempty"`
	Record         environment.Record `json:"record"`
}

// retentionReferenceContract guarantees the Store can maintain the live-edge
// graph consulted before terminal jobs are purged.
var retentionReferenceContract controlplane.RetentionReferenceWriter = (*Store)(nil)

// syncRunRetentionReferenceTx mirrors a consumer-owned relation onto the
// originating dispatcher job inside the same transaction as the relation.
func syncRunRetentionReferenceTx(ctx context.Context, tx pgx.Tx, runID, kind, referenceID string, live bool) error {
	if runID == "" || kind == "" || referenceID == "" {
		return controlplane.ErrInvalid
	}
	_, err := tx.Exec(ctx, `INSERT INTO retention_references(project_id,job_id,reference_kind,reference_id,live)
		SELECT j.project_id,j.job_id,$2,$3,$4 FROM workflow_runs w JOIN jobs j ON j.job_id=substring(w.idempotency_key from 8)
		WHERE w.handle=$1 AND w.idempotency_key LIKE 'github:%'
		ON CONFLICT(project_id,job_id,reference_kind,reference_id) DO UPDATE SET live=EXCLUDED.live`, runID, kind, referenceID, live)
	return err
}

// SaveRetentionReference writes or retires one product reference only while
// the exact leased job and active PostgreSQL backend fence are still live.
func (store *Store) SaveRetentionReference(ctx context.Context, job controlplane.Job, kind, referenceID string, live bool, owner string, now time.Time) error {
	if kind == "" || referenceID == "" || owner == "" || now.IsZero() || len(kind) > 128 || len(referenceID) > 128 {
		return controlplane.ErrInvalid
	}
	tx, err := store.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return mapError(err)
	}
	defer tx.Rollback(ctx)
	current, err := loadJobTx(ctx, tx, job.JobID, true)
	if err != nil {
		return err
	}
	if err = store.verifyActiveJobTx(ctx, tx, current); err != nil {
		return err
	}
	if current.Digest != job.Digest || current.State != controlplane.JobLeased || current.Owner != owner || current.Fence != job.Fence || current.CancelRevision != job.CancelRevision || !current.LeaseUntil.After(now.UTC()) {
		return controlplane.ErrConflict
	}
	if live {
		_, err = tx.Exec(ctx, `INSERT INTO retention_references(project_id,job_id,reference_kind,reference_id,live) VALUES($1,$2,$3,$4,true) ON CONFLICT(project_id,job_id,reference_kind,reference_id) DO UPDATE SET live=true`, current.ProjectID, current.JobID, kind, referenceID)
	} else {
		var tag pgconn.CommandTag
		tag, err = tx.Exec(ctx, `UPDATE retention_references SET live=false WHERE project_id=$1 AND job_id=$2 AND reference_kind=$3 AND reference_id=$4`, current.ProjectID, current.JobID, kind, referenceID)
		if err == nil && tag.RowsAffected() != 1 {
			return controlplane.ErrConflict
		}
	}
	if err != nil {
		return mapError(err)
	}
	return mapError(tx.Commit(ctx))
}

// SaveImport compare-and-swaps resumable offline import state by prior digest.
func (store *Store) SaveImport(ctx context.Context, value controlplane.ImportManifest, expected string) (controlplane.ImportManifest, error) {
	sealed, err := controlplane.SealImportManifest(value)
	if err != nil {
		return controlplane.ImportManifest{}, err
	}
	data, _ := canonical(sealed)
	if expected == "" {
		if sealed.State != controlplane.ImportFrozen {
			return controlplane.ImportManifest{}, controlplane.ErrConflict
		}
		result, execErr := store.pool.Exec(ctx, `INSERT INTO import_manifests(import_id,run_id,expected_head_digest,document,digest,state) VALUES($1,$2,$3,$4,$5,$6) ON CONFLICT DO NOTHING`, sealed.ImportID, sealed.RunID, sealed.ExpectedHeadDigest, data, sealed.Digest, sealed.State)
		if execErr != nil {
			return controlplane.ImportManifest{}, mapError(execErr)
		}
		if result.RowsAffected() != 1 {
			current, loadErr := store.LoadImport(ctx, sealed.ImportID)
			if loadErr != nil {
				return controlplane.ImportManifest{}, loadErr
			}
			if current.Digest != sealed.Digest {
				return controlplane.ImportManifest{}, controlplane.ErrConflict
			}
			return current, nil
		}
		return sealed, nil
	}
	current, loadErr := store.LoadImport(ctx, sealed.ImportID)
	if loadErr != nil {
		return controlplane.ImportManifest{}, loadErr
	}
	if current.Digest != expected {
		return controlplane.ImportManifest{}, controlplane.ErrConflict
	}
	advanced, advanceErr := controlplane.AdvanceImport(current, sealed.State)
	if advanceErr != nil || advanced.Digest != sealed.Digest {
		return controlplane.ImportManifest{}, controlplane.ErrConflict
	}
	result, err := store.pool.Exec(ctx, `UPDATE import_manifests SET document=$1,digest=$2,state=$3 WHERE import_id=$4 AND digest=$5`, data, sealed.Digest, sealed.State, sealed.ImportID, expected)
	if err != nil {
		return controlplane.ImportManifest{}, mapError(err)
	}
	if result.RowsAffected() != 1 {
		return controlplane.ImportManifest{}, controlplane.ErrConflict
	}
	return sealed, nil
}

// SaveImportPage persists byte-exact candidate documents idempotently without creating backend authority.
func (store *Store) SaveImportPage(ctx context.Context, importID string, documents []controlplane.ImportDocument) error {
	if importID == "" || len(documents) == 0 || len(documents) > 1000 {
		return controlplane.ErrInvalid
	}
	return retryTransactionObserved(ctx, store.pool, store.maxTxAttempts, "import.save_page", store.observer, func(tx pgx.Tx) error {
		var state string
		if err := tx.QueryRow(ctx, `SELECT state FROM import_manifests WHERE import_id=$1 FOR UPDATE`, importID).Scan(&state); err != nil {
			return err
		}
		if state != string(controlplane.ImportFrozen) && state != string(controlplane.ImportCandidate) {
			return controlplane.ErrConflict
		}
		for _, document := range documents {
			sealed, err := controlplane.SealImportDocument(document)
			if err != nil {
				return err
			}
			tag, err := tx.Exec(ctx, `INSERT INTO import_streams(import_id,stream_name,ordinal,relation_key,document,digest) VALUES($1,$2,$3,$4,$5,$6) ON CONFLICT DO NOTHING`, importID, sealed.Stream, sealed.Ordinal, sealed.RelationKey, sealed.Document, sealed.Digest)
			if err != nil {
				return err
			}
			if tag.RowsAffected() == 0 {
				var digest string
				if err = tx.QueryRow(ctx, `SELECT digest FROM import_streams WHERE import_id=$1 AND stream_name=$2 AND ordinal=$3`, importID, sealed.Stream, sealed.Ordinal).Scan(&digest); err != nil {
					return err
				}
				if digest != sealed.Digest {
					return controlplane.ErrConflict
				}
			}
		}
		if state == string(controlplane.ImportFrozen) {
			manifest, err := loadImportTx(ctx, tx, importID)
			if err != nil {
				return err
			}
			next, err := controlplane.AdvanceImport(manifest, controlplane.ImportCandidate)
			if err != nil {
				return err
			}
			data, _ := canonical(next)
			_, err = tx.Exec(ctx, `UPDATE import_manifests SET document=$1,digest=$2,state=$3 WHERE import_id=$4 AND digest=$5`, data, next.Digest, next.State, importID, manifest.Digest)
			return err
		}
		return nil
	})
}

// materializeImportDocumentTx decodes closed typed local streams into their
// real consumer tables while the local backend remains the sole authority.
func materializeImportDocumentTx(ctx context.Context, tx pgx.Tx, document controlplane.ImportDocument, authority *importMaterializationAuthority) error {
	if validateImportRelation(document) != nil {
		return controlplane.ErrIntegrity
	}
	path, _ := importRelationPath(document.RelationKey)
	switch document.Stream {
	case "workflow":
		record, events, err := validationworkflow.DecodeWorkflowImportCheckpoint(document.Document)
		if err != nil {
			return controlplane.ErrIntegrity
		}
		recordData, _ := canonical(record)
		if _, err = tx.Exec(ctx, `INSERT INTO workflow_runs(handle,principal,idempotency_key,request_digest,version,phase,owner,lease_until,updated_at,record) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10) ON CONFLICT DO NOTHING`, record.Handle, record.Principal, record.IdempotencyKey, record.RequestDigest, record.VersionCAS, record.Phase, nullableImportOwner(record.Owner), nullableImportLease(record.LeaseUntil), record.UpdatedAt, recordData); err != nil {
			return err
		}
		for _, event := range events {
			eventData, _ := canonical(event)
			if _, err = tx.Exec(ctx, `INSERT INTO workflow_events(handle,sequence,event_id,event) VALUES($1,$2,$3,$4) ON CONFLICT DO NOTHING`, record.Handle, event.Sequence, event.ID, eventData); err != nil {
				return err
			}
		}
	case "artifacts":
		var artifact validationworkflow.PlanArtifact
		if json.Unmarshal(document.Document, &artifact) != nil || validationworkflow.ValidatePlanArtifactForStore(artifact, artifact.WorkflowID) != nil {
			return controlplane.ErrIntegrity
		}
		if _, err := tx.Exec(ctx, `INSERT INTO workflow_artifacts(workflow_id,artifact_digest,media_type,artifact) VALUES($1,$2,'application/vnd.featureproof.plan+json',$3) ON CONFLICT DO NOTHING`, artifact.WorkflowID, artifact.ArtifactSHA256, document.Document); err != nil {
			return err
		}
	case "executables":
		var material validationworkflow.ExecutableMaterial
		if json.Unmarshal(document.Document, &material) != nil || !validationworkflow.ValidExecutableMaterialForStore(material.WorkflowID, material) {
			return controlplane.ErrIntegrity
		}
		if _, err := tx.Exec(ctx, `INSERT INTO workflow_executables(workflow_id,artifact_digest,material) VALUES($1,$2,$3) ON CONFLICT DO NOTHING`, material.WorkflowID, material.ArtifactSHA256, document.Document); err != nil {
			return err
		}
	case "functional":
		if !strings.HasSuffix(path, ".flow.json") {
			return controlplane.ErrIntegrity
		}
		var snapshot functionalImportDocument
		if controlplane.StrictDecode(document.Document, &snapshot) != nil || snapshot.Version == 0 || snapshot.Intents == nil || snapshot.Terminals == nil || snapshot.Events == nil {
			return controlplane.ErrIntegrity
		}
		for _, intent := range snapshot.Intents {
			if intent.RunID == "" || intent.Revision == 0 || intent.StepID == "" {
				return controlplane.ErrIntegrity
			}
			data, _ := canonical(intent)
			if _, err := tx.Exec(ctx, `INSERT INTO functional_intents(run_id,revision,step_id,intent,digest) VALUES($1,$2,$3,$4,$5) ON CONFLICT DO NOTHING`, intent.RunID, intent.Revision, intent.StepID, data, digestBytes(data)); err != nil {
				return err
			}
		}
		for _, terminal := range snapshot.Terminals {
			if terminal.RunID == "" || terminal.Revision == 0 || terminal.StepID == "" {
				return controlplane.ErrIntegrity
			}
			data, _ := canonical(terminal)
			if _, err := tx.Exec(ctx, `INSERT INTO functional_terminals(run_id,revision,step_id,terminal,digest) VALUES($1,$2,$3,$4,$5) ON CONFLICT DO NOTHING`, terminal.RunID, terminal.Revision, terminal.StepID, data, digestBytes(data)); err != nil {
				return err
			}
		}
		for _, event := range snapshot.Events {
			data, _ := canonical(event)
			if event.RunID == "" || event.Sequence == 0 || event.Digest == "" {
				return controlplane.ErrIntegrity
			}
			if _, err := tx.Exec(ctx, `INSERT INTO functional_events(run_id,sequence,flow_id,revision,artifact_digest,event,digest) VALUES($1,$2,$3,$4,$5,$6,$7) ON CONFLICT DO NOTHING`, event.RunID, event.Sequence, event.FlowID, event.Revision, event.ArtifactDigest, data, event.Digest); err != nil {
				return err
			}
		}
	case "approval":
		if strings.HasSuffix(path, ".jsonl") {
			events, err := decodeApprovalImportEvents(document.Document)
			if err != nil {
				return err
			}
			if len(events) == 0 {
				return controlplane.ErrIntegrity
			}
			if _, err = planapproval.Project(ctx, events, 100000); err != nil {
				return controlplane.ErrIntegrity
			}
			if _, err = tx.Exec(ctx, `INSERT INTO approval_runs(run_id,revision) VALUES($1,$2) ON CONFLICT(run_id) DO UPDATE SET revision=GREATEST(approval_runs.revision,EXCLUDED.revision)`, events[0].RunID, events[len(events)-1].Revision); err != nil {
				return err
			}
			for _, event := range events {
				data, _ := canonical(event)
				if _, err = tx.Exec(ctx, `INSERT INTO approval_events(run_id,revision,event_id,event) VALUES($1,$2,$3,$4) ON CONFLICT DO NOTHING`, event.RunID, event.Sequence, event.EventID, data); err != nil {
					return err
				}
			}
		} else if strings.Contains(path, ".request.") && strings.HasSuffix(path, ".json") {
			var request approvalImportRequest
			if controlplane.StrictDecode(document.Document, &request) != nil || request.RunID == "" || request.Revision == 0 {
				return controlplane.ErrIntegrity
			}
			terminal, err := approvalRevisionTerminalTx(ctx, tx, request.RunID, request.Revision)
			if err != nil {
				return err
			}
			if !terminal {
				return controlplane.ErrConflict
			}
		} else {
			return controlplane.ErrIntegrity
		}
	case "environment":
		if strings.Contains(path, "/records/") && strings.HasSuffix(path, ".json") {
			var wrapper environmentImportRecord
			if controlplane.StrictDecode(document.Document, &wrapper) != nil || wrapper.Digest == "" || wrapper.Record.RunID == "" || wrapper.Record.ProjectID == "" || wrapper.Record.Revision == 0 {
				return controlplane.ErrIntegrity
			}
			recordData, _ := canonical(wrapper.Record)
			if _, err := tx.Exec(ctx, `INSERT INTO environment_records(run_id,project_id,revision,state,record,digest) VALUES($1,$2,$3,$4,$5,$6) ON CONFLICT(run_id) DO UPDATE SET project_id=EXCLUDED.project_id,revision=EXCLUDED.revision,state=EXCLUDED.state,record=EXCLUDED.record,digest=EXCLUDED.digest WHERE environment_records.revision<EXCLUDED.revision`, wrapper.Record.RunID, wrapper.Record.ProjectID, wrapper.Record.Revision, wrapper.Record.State, recordData, wrapper.Digest); err != nil {
				return err
			}
			return nil
		}
		if strings.Contains(path, "/events/") && strings.HasSuffix(path, ".json") {
			var event environment.Event
			if controlplane.StrictDecode(document.Document, &event) != nil || event.ID == "" || event.RunID == "" || event.Digest == "" {
				return controlplane.ErrIntegrity
			}
			data, _ := canonical(event)
			if _, err := tx.Exec(ctx, `INSERT INTO environment_events(run_id,event) VALUES($1,$2)`, event.RunID, data); err != nil {
				return err
			}
			return nil
		}
		return controlplane.ErrIntegrity
	case "cleanup":
		var snapshot cleanup.ImportSnapshot
		if controlplane.StrictDecode(document.Document, &snapshot) != nil || snapshot.Items == nil || snapshot.Events == nil || snapshot.Cursors == nil {
			return controlplane.ErrIntegrity
		}
		for id, item := range snapshot.Items {
			if id != item.CleanupID || cleanup.VerifyItem(item) != nil {
				return controlplane.ErrIntegrity
			}
			data, _ := canonical(item)
			if _, err := tx.Exec(ctx, `INSERT INTO cleanup_items(cleanup_id,project_id,run_id,state,version,owner,lease_until,fence,next_attempt_at,item,digest) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11) ON CONFLICT DO NOTHING`, item.CleanupID, item.ProjectID, item.RunID, item.State, item.VersionNumber, nullString(item.LeaseOwner), nullTime(item.LeaseUntil), item.FencingVersion, nullTime(item.NextAttemptAt), data, item.Digest); err != nil {
				return err
			}
		}
		for id, events := range snapshot.Events {
			for _, event := range events {
				if event.CleanupID != id || event.Digest == "" {
					return controlplane.ErrIntegrity
				}
				data, _ := canonical(event)
				if _, err := tx.Exec(ctx, `INSERT INTO cleanup_events(cleanup_id,event_id,event) VALUES($1,$2,$3) ON CONFLICT DO NOTHING`, id, event.ID, data); err != nil {
					return err
				}
			}
		}
		for source, cursor := range snapshot.Cursors {
			if source != cursor.Source || cursor.Epoch == 0 || cursor.Digest == "" {
				return controlplane.ErrIntegrity
			}
			if _, err := tx.Exec(ctx, `INSERT INTO cleanup_cursors(source,epoch,cursor,revision) VALUES($1,$2,$3,1) ON CONFLICT DO NOTHING`, source, cursor.Epoch, cursor.Value); err != nil {
				return err
			}
		}
	case "capacity":
		var snapshot cleanup.ImportCapacitySnapshot
		if controlplane.StrictDecode(document.Document, &snapshot) != nil || snapshot.Receipts == nil || snapshot.Events == nil {
			return controlplane.ErrIntegrity
		}
		for id, receipt := range snapshot.Receipts {
			if id != receipt.ID || receipt.Digest == "" {
				return controlplane.ErrIntegrity
			}
			state := "reserved"
			if receipt.Released {
				state = "released"
			} else if receipt.EnvironmentDigest != "" {
				state = "confirmed"
			} else if receipt.LifecycleBound {
				state = "bound"
			}
			data, _ := canonical(receipt)
			if _, err := tx.Exec(ctx, `INSERT INTO cleanup_capacity_receipts(receipt_id,project_id,run_id,acquisition_key,state,receipt,digest) VALUES($1,$2,$3,$4,$5,$6,$7) ON CONFLICT DO NOTHING`, receipt.ID, receipt.ProjectID, receipt.RunID, receipt.AcquisitionKey, state, data, receipt.Digest); err != nil {
				return err
			}
		}
		for project, events := range snapshot.Events {
			for _, event := range events {
				if event.ProjectID != project || event.Digest == "" {
					return controlplane.ErrIntegrity
				}
				data, _ := canonical(event)
				if _, err := tx.Exec(ctx, `INSERT INTO cleanup_capacity_events(project_id,event_id,receipt_id,event) VALUES($1,$2,$3,$4) ON CONFLICT DO NOTHING`, project, event.ID, event.ReceiptID, data); err != nil {
					return err
				}
			}
		}
	case "correction":
		var snapshot correction.ImportSnapshot
		if controlplane.StrictDecode(document.Document, &snapshot) != nil || snapshot.Chains == nil {
			return controlplane.ErrIntegrity
		}
		for root, events := range snapshot.Chains {
			if root == "" || len(events) == 0 {
				return controlplane.ErrIntegrity
			}
			latest := map[string]correction.Record{}
			revisions := map[string]uint64{}
			positions := map[string]uint64{}
			var nextPosition uint64
			for index, event := range events {
				if event.Sequence != uint64(index+1) || event.Record.Link.RootRunID != root || event.Record.Link.Verify() != nil {
					return controlplane.ErrIntegrity
				}
				handle := event.Record.Link.Handle
				latest[handle] = event.Record
				revisions[handle]++
				if _, exists := positions[handle]; !exists {
					nextPosition++
					positions[handle] = nextPosition
				}
			}
			for handle, record := range latest {
				data, _ := canonical(record)
				if _, err := tx.Exec(ctx, `INSERT INTO correction_records(run_id,project_id,root_run_id,parent_run_id,revision,phase,record,digest) VALUES($1,$2,$3,$4,$5,$6,$7,$8) ON CONFLICT DO NOTHING`, handle, record.Link.PrincipalID, root, record.Link.ParentRunID, revisions[handle], record.Phase, data, digestBytes(data)); err != nil {
					return err
				}
			}
			for handle, position := range positions {
				record := latest[handle]
				if _, err := tx.Exec(ctx, `INSERT INTO correction_relations(root_run_id,parent_run_id,child_run_id,position) VALUES($1,$2,$3,$4) ON CONFLICT DO NOTHING`, root, record.Link.ParentRunID, handle, position); err != nil {
					return err
				}
			}
			for _, event := range events {
				data, _ := canonical(event)
				if _, err := tx.Exec(ctx, `INSERT INTO correction_events(root_run_id,sequence,run_id,event,digest) VALUES($1,$2,$3,$4,$5) ON CONFLICT DO NOTHING`, root, event.Sequence, event.Record.Link.Handle, data, digestBytes(data)); err != nil {
					return err
				}
			}
		}
	case "github":
		if authority == nil {
			return controlplane.ErrIntegrity
		}
		var snapshot githubpr.ImportSnapshot
		if controlplane.StrictDecode(document.Document, &snapshot) != nil || snapshot.Events == nil {
			return controlplane.ErrIntegrity
		}
		for index, event := range snapshot.Events {
			if event.Sequence != uint64(index+1) || !validGitHubImportEvent(event) {
				return controlplane.ErrIntegrity
			}
			data, _ := canonical(event)
			if _, err := tx.Exec(ctx, `INSERT INTO github_import_events(source_id,sequence,event,digest) VALUES($1,$2,$3,$4) ON CONFLICT DO NOTHING`, document.RelationKey, event.Sequence, data, digestBytes(data)); err != nil {
				return err
			}
			if event.Delivery != nil {
				receipt, _ := canonical(event.Delivery)
				if _, err := tx.Exec(ctx, `INSERT INTO ingress_inbox(source,idempotency_key,request_digest,project_id,envelope,committed_at) VALUES('github',$1,$2,$3,$4,$5) ON CONFLICT DO NOTHING`, event.Delivery.DeliveryID, event.Delivery.PayloadSHA256, authority.ProjectID, receipt, event.At.UTC()); err != nil {
					return err
				}
			}
		}
	case "packages":
		var snapshot validationworkflow.PackageImportSnapshot
		if controlplane.StrictDecode(document.Document, &snapshot) != nil || snapshot.Items == nil {
			return controlplane.ErrIntegrity
		}
		for _, item := range snapshot.Items {
			if item.RunID == "" || item.PackageID == "" || item.Digest != digestBytes(item.Document) || item.ArtifactDigest == "" || item.MediaType == "" || item.SchemaVersion == "" {
				return controlplane.ErrIntegrity
			}
			if authority == nil {
				return controlplane.ErrIntegrity
			}
			if _, err := tx.Exec(ctx, `INSERT INTO packages(project_id,package_id,run_id,media_type,schema_version,artifact_digest,binding_digest,backend_epoch,backend_identity,document,digest,created_at) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12) ON CONFLICT DO NOTHING`, authority.ProjectID, item.PackageID, item.RunID, item.MediaType, item.SchemaVersion, item.ArtifactDigest, authority.BindingDigest, authority.Epoch, authority.BackendIdentity, item.Document, item.Digest, authority.CreatedAt); err != nil {
				return err
			}
		}
	default:
		return controlplane.ErrIntegrity
	}
	return nil
}

// importMaterializationAuthority binds filesystem-independent packages to the
// exact PostgreSQL epoch that becomes active in the same cutover transaction.
type importMaterializationAuthority struct {
	ProjectID, BindingDigest, BackendIdentity string
	Epoch                                     uint64
	CreatedAt                                 time.Time
}

// validGitHubImportEvent rechecks every optional consumer payload before its
// source-authenticated journal enters PostgreSQL audit storage.
func validGitHubImportEvent(event githubpr.StoreEvent) bool {
	if event.Sequence == 0 || event.Kind == "" || event.At.IsZero() {
		return false
	}
	if event.Association != nil && githubpr.VerifyAssociation(*event.Association) != nil {
		return false
	}
	if event.Intent != nil && githubpr.VerifyPublicationIntent(*event.Intent) != nil {
		return false
	}
	if event.Delivery != nil && (event.Delivery.Version != githubpr.SchemaVersion || event.Delivery.DeliveryID == "" || len(event.Delivery.PayloadSHA256) != 64) {
		return false
	}
	return event.Delivery != nil || event.Association != nil || event.Intent != nil || event.Claims != nil
}

// approvalRevisionTerminalTx evaluates sanitized confirmed events in Go because
// their canonical bytea representation is intentionally opaque to PostgreSQL.
func approvalRevisionTerminalTx(ctx context.Context, tx pgx.Tx, runID string, revision uint64) (bool, error) {
	rows, err := tx.Query(ctx, `SELECT event FROM approval_events WHERE run_id=$1 ORDER BY revision`, runID)
	if err != nil {
		return false, err
	}
	defer rows.Close()
	for rows.Next() {
		var data []byte
		var event planapproval.ApprovalEvent
		if rows.Scan(&data) != nil || controlplane.StrictDecode(data, &event) != nil {
			return false, controlplane.ErrIntegrity
		}
		if event.Revision == revision && (event.Type == planapproval.EventApproved || event.Type == planapproval.EventRejected || event.Type == planapproval.EventInvalidated || event.Type == planapproval.EventGrantConsumed) {
			return true, nil
		}
	}
	return false, rows.Err()
}

// decodeApprovalImportEvents strictly reconstructs one newline-delimited
// confirmed journal before the projector validates its complete state machine.
func decodeApprovalImportEvents(data []byte) ([]planapproval.ApprovalEvent, error) {
	lines := bytes.Split(bytes.TrimSpace(data), []byte{'\n'})
	events := make([]planapproval.ApprovalEvent, 0, len(lines))
	for _, line := range lines {
		if len(bytes.TrimSpace(line)) == 0 {
			continue
		}
		var event planapproval.ApprovalEvent
		if controlplane.StrictDecode(line, &event) != nil {
			return nil, controlplane.ErrIntegrity
		}
		events = append(events, event)
	}
	return events, nil
}

// nullableImportOwner preserves an existing lease owner without inventing one
// for passive frozen projections.
func nullableImportOwner(owner string) any {
	if owner == "" {
		return nil
	}
	return owner
}

// nullableImportLease preserves the exact frozen lease timestamp while using
// SQL NULL for unowned workflow projections.
func nullableImportLease(lease time.Time) any {
	if lease.IsZero() {
		return nil
	}
	return lease.UTC()
}

// importRelationPath reverses only relation keys minted from a frozen local
// allowlist; caller-authored opaque relation keys remain staging-only.
func importRelationPath(relation string) (string, bool) {
	if len(relation) < 3 || relation[0] != 'p' {
		return "", false
	}
	decoded, err := hex.DecodeString(relation[1:])
	if err != nil || len(decoded) == 0 || len(decoded) > 63 {
		return "", false
	}
	rawPath := string(decoded)
	if strings.ContainsRune(rawPath, '\\') {
		return "", false
	}
	path := filepath.ToSlash(rawPath)
	segments := strings.Split(path, "/")
	if filepath.IsAbs(path) || strings.HasPrefix(path, "/") || len(segments) < 2 || strings.ContainsRune(path, '\x00') {
		return "", false
	}
	for _, segment := range segments {
		if segment == "" || segment == "." || segment == ".." {
			return "", false
		}
	}
	return path, true
}

// importPathID is the filesystem-safe coordinate grammar shared by local
// stores; dots are excluded here so required schema suffixes stay unambiguous.
var importPathID = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]*$`)

// validateImportRelation closes every stream over the exact relation shape
// owned by its consumer so an unknown document can never verify as a no-op.
func validateImportRelation(document controlplane.ImportDocument) error {
	typedRelations := map[string]string{
		"capacity":   "typed-capacity",
		"cleanup":    "typed-cleanup",
		"correction": "typed-correction",
		"github":     "typed-github",
		"packages":   "typed-packages",
	}
	if expected, typed := typedRelations[document.Stream]; typed {
		if document.RelationKey == expected {
			return nil
		}
		return controlplane.ErrIntegrity
	}
	path, decoded := importRelationPath(document.RelationKey)
	if !decoded {
		return controlplane.ErrIntegrity
	}
	switch document.Stream {
	case "workflow":
		if validImportFilePath(path, "workflows", ".state.json") {
			return nil
		}
	case "artifacts":
		if validImportFilePath(path, "workflows/plans", ".plan.json") {
			return nil
		}
	case "executables":
		if validImportFilePath(path, "workflows/private", ".private.json") {
			return nil
		}
	case "functional":
		if validImportFilePath(path, "workflows/functional-flow", ".flow.json") {
			return nil
		}
	case "approval":
		if validImportFilePath(path, "workflows/approval", ".jsonl") || validApprovalRequestRelation(path) {
			return nil
		}
	case "environment":
		if validEnvironmentImportPath(path) {
			return nil
		}
	}
	return controlplane.ErrIntegrity
}

// validImportFilePath accepts one exact consumer directory, one safe
// identifier and one schema-bearing suffix with no extra segments.
func validImportFilePath(path, directory, suffix string) bool {
	segments := strings.Split(path, "/")
	directorySegments := strings.Split(directory, "/")
	if len(segments) != len(directorySegments)+1 || !strings.HasSuffix(segments[len(segments)-1], suffix) {
		return false
	}
	for index, expected := range directorySegments {
		if segments[index] != expected {
			return false
		}
	}
	identifier := strings.TrimSuffix(segments[len(segments)-1], suffix)
	return importPathID.MatchString(identifier)
}

// validEnvironmentImportPath accepts only versioned record or event files at
// `environment/<kind>/<run>/<positive revision>.json`.
func validEnvironmentImportPath(path string) bool {
	segments := strings.Split(path, "/")
	if len(segments) != 4 || segments[0] != "environment" || segments[1] != "records" && segments[1] != "events" || !importPathID.MatchString(segments[2]) || !strings.HasSuffix(segments[3], ".json") {
		return false
	}
	return validPositiveDecimal(strings.TrimSuffix(segments[3], ".json"))
}

// validPositiveDecimal recognizes canonical positive base-ten versions with
// no sign, zero value or leading zero aliases.
func validPositiveDecimal(value string) bool {
	if value == "" || value[0] == '0' {
		return false
	}
	for _, digit := range value {
		if digit < '0' || digit > '9' {
			return false
		}
	}
	return true
}

// validApprovalRequestRelation accepts only the consumer's
// `<run>.request.<positive-revision>.json` durable filename grammar.
func validApprovalRequestRelation(path string) bool {
	segments := strings.Split(path, "/")
	if len(segments) != 3 || segments[0] != "workflows" || segments[1] != "approval" || !strings.HasSuffix(segments[2], ".json") {
		return false
	}
	stem := strings.TrimSuffix(segments[2], ".json")
	marker := strings.LastIndex(stem, ".request.")
	if marker <= 0 || marker+len(".request.") == len(stem) || !importPathID.MatchString(stem[:marker]) {
		return false
	}
	return validPositiveDecimal(stem[marker+len(".request."):])
}

// VerifyImport recomputes counts, ordered bytes, relations and event streams before marking a candidate verified.
func (store *Store) VerifyImport(ctx context.Context, importID string) (controlplane.ImportManifest, error) {
	var verified controlplane.ImportManifest
	err := retryTransactionObserved(ctx, store.pool, store.maxTxAttempts, "import.verify", store.observer, func(tx pgx.Tx) error {
		manifest, err := loadImportTx(ctx, tx, importID)
		if err != nil {
			return err
		}
		if manifest.State == controlplane.ImportVerified {
			verified = manifest
			return nil
		}
		if manifest.State != controlplane.ImportCandidate {
			return controlplane.ErrConflict
		}
		rows, err := tx.Query(ctx, `SELECT stream_name,ordinal,relation_key,document,digest FROM import_streams WHERE import_id=$1 ORDER BY stream_name,ordinal`, importID)
		if err != nil {
			return err
		}
		streams := map[string][]controlplane.ImportDocument{}
		for rows.Next() {
			var item controlplane.ImportDocument
			if err = rows.Scan(&item.Stream, &item.Ordinal, &item.RelationKey, &item.Document, &item.Digest); err != nil {
				rows.Close()
				return err
			}
			sealed, sealErr := controlplane.SealImportDocument(item)
			if sealErr != nil {
				rows.Close()
				return sealErr
			}
			streams[item.Stream] = append(streams[item.Stream], sealed)
		}
		rows.Close()
		if rows.Err() != nil {
			return rows.Err()
		}
		if !exactImportStreamSet(manifest, streams) {
			return controlplane.ErrIntegrity
		}
		for stream, count := range manifest.Counts {
			items := streams[stream]
			if uint64(len(items)) != count || postgresImportDigest(items, false) != manifest.StreamDigests[stream] || manifest.RelationDigests[stream] != "" && postgresImportDigest(items, true) != manifest.RelationDigests[stream] {
				return controlplane.ErrIntegrity
			}
			if expected := manifest.EventDigests[stream]; expected != "" && postgresImportDigest(items, false) != expected {
				return controlplane.ErrIntegrity
			}
		}
		ordered := orderImportDocuments(streams)
		verificationAuthority := &importMaterializationAuthority{ProjectID: manifest.ProjectID, BindingDigest: manifest.ExpectedHeadDigest, BackendIdentity: store.backendIdentity, Epoch: manifest.ExpectedEpoch + 1, CreatedAt: manifest.CreatedAt}
		if _, err = tx.Exec(ctx, `SAVEPOINT import_candidate_validation`); err != nil {
			return err
		}
		for _, item := range ordered {
			if err = materializeImportDocumentTx(ctx, tx, item, verificationAuthority); err != nil {
				return err
			}
		}
		for _, item := range ordered {
			if err = verifyMaterializedImportDocumentTx(ctx, tx, item); err != nil {
				return err
			}
		}
		if _, err = tx.Exec(ctx, `ROLLBACK TO SAVEPOINT import_candidate_validation`); err != nil {
			return err
		}
		if _, err = tx.Exec(ctx, `RELEASE SAVEPOINT import_candidate_validation`); err != nil {
			return err
		}
		next, err := controlplane.AdvanceImport(manifest, controlplane.ImportVerified)
		if err != nil {
			return err
		}
		data, _ := canonical(next)
		tag, err := tx.Exec(ctx, `UPDATE import_manifests SET document=$1,digest=$2,state=$3 WHERE import_id=$4 AND digest=$5`, data, next.Digest, next.State, importID, manifest.Digest)
		if err != nil || tag.RowsAffected() != 1 {
			return controlplane.ErrConflict
		}
		verified = next
		return nil
	})
	return verified, err
}

// exactImportStreamSet rejects undeclared, unsupported and partially described
// streams so verification covers precisely the frozen manifest allowlist.
func exactImportStreamSet(manifest controlplane.ImportManifest, streams map[string][]controlplane.ImportDocument) bool {
	allowed := map[string]bool{"workflow": true, "artifacts": true, "executables": true, "approval": true, "functional": true, "environment": true, "capacity": true, "cleanup": true, "correction": true, "packages": true, "github": true}
	if len(streams) != len(manifest.Counts) || len(manifest.StreamDigests) != len(manifest.Counts) {
		return false
	}
	for stream := range streams {
		if !allowed[stream] {
			return false
		}
		if _, declared := manifest.Counts[stream]; !declared {
			return false
		}
		if _, described := manifest.StreamDigests[stream]; !described {
			return false
		}
		for _, document := range streams[stream] {
			if validateImportRelation(document) != nil {
				return false
			}
		}
	}
	for stream := range manifest.Counts {
		if !allowed[stream] {
			return false
		}
		if _, present := streams[stream]; !present && manifest.Counts[stream] != 0 {
			return false
		}
	}
	return true
}

// orderImportDocuments establishes consumer FK and journal dependencies before
// any candidate row is materialized in the verification transaction.
func orderImportDocuments(streams map[string][]controlplane.ImportDocument) []controlplane.ImportDocument {
	priority := map[string]int{"workflow": 10, "artifacts": 20, "executables": 30, "approval": 40, "functional": 50, "environment": 60, "capacity": 70, "cleanup": 80, "correction": 90, "packages": 100, "github": 110}
	var ordered []controlplane.ImportDocument
	for _, items := range streams {
		ordered = append(ordered, items...)
	}
	sort.SliceStable(ordered, func(i, j int) bool {
		left, right := priority[ordered[i].Stream], priority[ordered[j].Stream]
		if left != right {
			return left < right
		}
		leftPath, _ := importRelationPath(ordered[i].RelationKey)
		rightPath, _ := importRelationPath(ordered[j].RelationKey)
		if ordered[i].Stream == "approval" {
			leftRequest, rightRequest := strings.Contains(leftPath, ".request."), strings.Contains(rightPath, ".request.")
			if leftRequest != rightRequest {
				return !leftRequest
			}
		}
		if ordered[i].Stream == "environment" {
			leftEvent, rightEvent := strings.Contains(leftPath, "/events/"), strings.Contains(rightPath, "/events/")
			if leftEvent != rightEvent {
				return !leftEvent
			}
		}
		return ordered[i].Ordinal < ordered[j].Ordinal
	})
	return ordered
}

// verifyMaterializedImportDocumentTx compares staged typed bytes with the real
// consumer projections and journals before a candidate can become verified.
func verifyMaterializedImportDocumentTx(ctx context.Context, tx pgx.Tx, document controlplane.ImportDocument) error {
	if validateImportRelation(document) != nil {
		return controlplane.ErrIntegrity
	}
	path, _ := importRelationPath(document.RelationKey)
	switch document.Stream {
	case "workflow":
		record, events, err := validationworkflow.DecodeWorkflowImportCheckpoint(document.Document)
		if err != nil {
			return controlplane.ErrIntegrity
		}
		var stored []byte
		if err = tx.QueryRow(ctx, `SELECT record FROM workflow_runs WHERE handle=$1`, record.Handle).Scan(&stored); err != nil || !bytes.Equal(stored, mustCanonical(record)) {
			return controlplane.ErrIntegrity
		}
		rows, err := tx.Query(ctx, `SELECT event FROM workflow_events WHERE handle=$1 ORDER BY sequence`, record.Handle)
		if err != nil {
			return err
		}
		defer rows.Close()
		index := 0
		for rows.Next() {
			if index >= len(events) || rows.Scan(&stored) != nil || !bytes.Equal(stored, mustCanonical(events[index])) {
				return controlplane.ErrIntegrity
			}
			index++
		}
		if rows.Err() != nil || index != len(events) {
			return controlplane.ErrIntegrity
		}
	case "artifacts":
		var artifact validationworkflow.PlanArtifact
		if json.Unmarshal(document.Document, &artifact) != nil {
			return controlplane.ErrIntegrity
		}
		var stored []byte
		if err := tx.QueryRow(ctx, `SELECT artifact FROM workflow_artifacts WHERE workflow_id=$1`, artifact.WorkflowID).Scan(&stored); err != nil || !bytes.Equal(stored, document.Document) {
			return controlplane.ErrIntegrity
		}
	case "executables":
		var material validationworkflow.ExecutableMaterial
		if json.Unmarshal(document.Document, &material) != nil {
			return controlplane.ErrIntegrity
		}
		var stored []byte
		if err := tx.QueryRow(ctx, `SELECT material FROM workflow_executables WHERE workflow_id=$1`, material.WorkflowID).Scan(&stored); err != nil || !bytes.Equal(stored, document.Document) {
			return controlplane.ErrIntegrity
		}
	case "functional":
		var snapshot functionalImportDocument
		if controlplane.StrictDecode(document.Document, &snapshot) != nil {
			return controlplane.ErrIntegrity
		}
		for _, intent := range snapshot.Intents {
			var stored []byte
			if err := tx.QueryRow(ctx, `SELECT intent FROM functional_intents WHERE run_id=$1 AND revision=$2 AND step_id=$3`, intent.RunID, intent.Revision, intent.StepID).Scan(&stored); err != nil || !bytes.Equal(stored, mustCanonical(intent)) {
				return controlplane.ErrIntegrity
			}
		}
		for _, terminal := range snapshot.Terminals {
			var stored []byte
			if err := tx.QueryRow(ctx, `SELECT terminal FROM functional_terminals WHERE run_id=$1 AND revision=$2 AND step_id=$3`, terminal.RunID, terminal.Revision, terminal.StepID).Scan(&stored); err != nil || !bytes.Equal(stored, mustCanonical(terminal)) {
				return controlplane.ErrIntegrity
			}
		}
		for _, event := range snapshot.Events {
			var stored []byte
			if err := tx.QueryRow(ctx, `SELECT event FROM functional_events WHERE run_id=$1 AND sequence=$2`, event.RunID, event.Sequence).Scan(&stored); err != nil || !bytes.Equal(stored, mustCanonical(event)) {
				return controlplane.ErrIntegrity
			}
		}
	case "approval":
		if strings.HasSuffix(path, ".jsonl") {
			events, err := decodeApprovalImportEvents(document.Document)
			if err != nil {
				return err
			}
			for _, event := range events {
				var stored []byte
				if err := tx.QueryRow(ctx, `SELECT event FROM approval_events WHERE run_id=$1 AND revision=$2`, event.RunID, event.Sequence).Scan(&stored); err != nil || !bytes.Equal(stored, mustCanonical(event)) {
					return controlplane.ErrIntegrity
				}
			}
		}
	case "environment":
		if strings.Contains(path, "/records/") && !strings.HasSuffix(path, "/head.json") {
			var wrapper environmentImportRecord
			if controlplane.StrictDecode(document.Document, &wrapper) != nil {
				return controlplane.ErrIntegrity
			}
			var revision uint64
			var digest string
			if err := tx.QueryRow(ctx, `SELECT revision,digest FROM environment_records WHERE run_id=$1`, wrapper.Record.RunID).Scan(&revision, &digest); err != nil || revision < wrapper.Record.Revision || revision == wrapper.Record.Revision && digest != wrapper.Digest {
				return controlplane.ErrIntegrity
			}
		}
	case "cleanup":
		var snapshot cleanup.ImportSnapshot
		if controlplane.StrictDecode(document.Document, &snapshot) != nil {
			return controlplane.ErrIntegrity
		}
		for id, item := range snapshot.Items {
			var stored []byte
			if err := tx.QueryRow(ctx, `SELECT item FROM cleanup_items WHERE cleanup_id=$1`, id).Scan(&stored); err != nil || !bytes.Equal(stored, mustCanonical(item)) {
				return controlplane.ErrIntegrity
			}
		}
		for id, events := range snapshot.Events {
			for _, event := range events {
				var stored []byte
				if err := tx.QueryRow(ctx, `SELECT event FROM cleanup_events WHERE cleanup_id=$1 AND event_id=$2`, id, event.ID).Scan(&stored); err != nil || !bytes.Equal(stored, mustCanonical(event)) {
					return controlplane.ErrIntegrity
				}
			}
		}
		for source, cursor := range snapshot.Cursors {
			var epoch uint64
			var value string
			if err := tx.QueryRow(ctx, `SELECT epoch,cursor FROM cleanup_cursors WHERE source=$1`, source).Scan(&epoch, &value); err != nil || epoch != cursor.Epoch || value != cursor.Value {
				return controlplane.ErrIntegrity
			}
		}
	case "capacity":
		var snapshot cleanup.ImportCapacitySnapshot
		if controlplane.StrictDecode(document.Document, &snapshot) != nil {
			return controlplane.ErrIntegrity
		}
		for id, receipt := range snapshot.Receipts {
			var stored []byte
			if err := tx.QueryRow(ctx, `SELECT receipt FROM cleanup_capacity_receipts WHERE receipt_id=$1`, id).Scan(&stored); err != nil || !bytes.Equal(stored, mustCanonical(receipt)) {
				return controlplane.ErrIntegrity
			}
		}
		for project, events := range snapshot.Events {
			for _, event := range events {
				var stored []byte
				if err := tx.QueryRow(ctx, `SELECT event FROM cleanup_capacity_events WHERE project_id=$1 AND event_id=$2`, project, event.ID).Scan(&stored); err != nil || !bytes.Equal(stored, mustCanonical(event)) {
					return controlplane.ErrIntegrity
				}
			}
		}
	case "correction":
		var snapshot correction.ImportSnapshot
		if controlplane.StrictDecode(document.Document, &snapshot) != nil {
			return controlplane.ErrIntegrity
		}
		for root, events := range snapshot.Chains {
			for _, event := range events {
				var stored []byte
				if err := tx.QueryRow(ctx, `SELECT event FROM correction_events WHERE root_run_id=$1 AND sequence=$2`, root, event.Sequence).Scan(&stored); err != nil || !bytes.Equal(stored, mustCanonical(event)) {
					return controlplane.ErrIntegrity
				}
			}
		}
	case "github":
		var snapshot githubpr.ImportSnapshot
		if controlplane.StrictDecode(document.Document, &snapshot) != nil {
			return controlplane.ErrIntegrity
		}
		for _, event := range snapshot.Events {
			var stored []byte
			if err := tx.QueryRow(ctx, `SELECT event FROM github_import_events WHERE source_id=$1 AND sequence=$2`, document.RelationKey, event.Sequence).Scan(&stored); err != nil || !bytes.Equal(stored, mustCanonical(event)) {
				return controlplane.ErrIntegrity
			}
			if event.Delivery != nil {
				var digest string
				var envelope []byte
				if err := tx.QueryRow(ctx, `SELECT request_digest,envelope FROM ingress_inbox WHERE source='github' AND idempotency_key=$1`, event.Delivery.DeliveryID).Scan(&digest, &envelope); err != nil || digest != event.Delivery.PayloadSHA256 || !bytes.Equal(envelope, mustCanonical(event.Delivery)) {
					return controlplane.ErrIntegrity
				}
			}
		}
	case "packages":
		var snapshot validationworkflow.PackageImportSnapshot
		if controlplane.StrictDecode(document.Document, &snapshot) != nil {
			return controlplane.ErrIntegrity
		}
		for _, item := range snapshot.Items {
			var runID, artifactDigest, digest string
			var stored []byte
			if item.Digest != digestBytes(item.Document) || tx.QueryRow(ctx, `SELECT run_id,artifact_digest,document,digest FROM packages WHERE package_id=$1`, item.PackageID).Scan(&runID, &artifactDigest, &stored, &digest) != nil || runID != item.RunID || artifactDigest != item.ArtifactDigest || digest != item.Digest || !bytes.Equal(stored, item.Document) {
				return controlplane.ErrIntegrity
			}
		}
	}
	return nil
}

// AbandonImport marks a pre-cutover candidate inert so composition may safely unfreeze its unchanged local source.
func (store *Store) AbandonImport(ctx context.Context, importID string) (controlplane.ImportManifest, error) {
	var abandoned controlplane.ImportManifest
	err := retryTransactionObserved(ctx, store.pool, store.maxTxAttempts, "import.abandon", store.observer, func(tx pgx.Tx) error {
		manifest, err := loadImportTx(ctx, tx, importID)
		if err != nil {
			return err
		}
		if manifest.State == controlplane.ImportAbandoned {
			abandoned = manifest
			return nil
		}
		next, err := controlplane.AdvanceImport(manifest, controlplane.ImportAbandoned)
		if err != nil {
			return err
		}
		data, _ := canonical(next)
		tag, err := tx.Exec(ctx, `UPDATE import_manifests SET document=$1,digest=$2,state=$3 WHERE import_id=$4 AND digest=$5`, data, next.Digest, next.State, importID, manifest.Digest)
		if err != nil || tag.RowsAffected() != 1 {
			return controlplane.ErrConflict
		}
		abandoned = next
		return nil
	})
	return abandoned, err
}

// CutoverImport publishes exactly one verified PostgreSQL epoch and durable archive receipt in one retried transaction.
func (store *Store) CutoverImport(ctx context.Context, importID string, now time.Time) (controlplane.ImportCutoverReceipt, error) {
	if importID == "" || now.IsZero() {
		return controlplane.ImportCutoverReceipt{}, controlplane.ErrInvalid
	}
	var receipt controlplane.ImportCutoverReceipt
	err := retryTransactionObserved(ctx, store.pool, store.maxTxAttempts, "import.cutover", store.observer, func(tx pgx.Tx) error {
		if existing, err := loadImportReceiptTx(ctx, tx, importID); err == nil {
			receipt = existing
			return nil
		} else if err != pgx.ErrNoRows {
			return err
		}
		manifest, err := loadImportTx(ctx, tx, importID)
		if err != nil {
			return err
		}
		if manifest.State != controlplane.ImportVerified {
			return controlplane.ErrConflict
		}
		var headBytes, epochBytes []byte
		if err = tx.QueryRow(ctx, `SELECT h.document,e.document FROM backend_binding_heads h JOIN backend_binding_epochs e ON e.run_id=h.run_id AND e.epoch=h.epoch WHERE h.run_id=$1 FOR UPDATE OF h`, manifest.RunID).Scan(&headBytes, &epochBytes); err != nil {
			return err
		}
		var head controlplane.BindingHead
		var prior controlplane.BackendEpoch
		if controlplane.StrictDecode(headBytes, &head) != nil || controlplane.StrictDecode(epochBytes, &prior) != nil || controlplane.VerifyBindingHead(head, prior) != nil || head.Digest != manifest.ExpectedHeadDigest || manifest.ExpectedEpoch != 0 && head.Epoch != manifest.ExpectedEpoch {
			return controlplane.ErrConflict
		}
		if prior.Mode != controlplane.BackendLocal {
			return controlplane.ErrConflict
		}
		next, err := controlplane.SealBackendEpoch(controlplane.BackendEpoch{RunID: head.RunID, ProjectID: head.ProjectID, Epoch: head.Epoch + 1, Mode: controlplane.BackendPostgres, BackendIdentity: store.backendIdentity, SchemaVersion: prior.SchemaVersion, CreatedAt: now, PreviousDigest: prior.Digest})
		if err != nil {
			return err
		}
		nextBytes, _ := canonical(next)
		if _, err = tx.Exec(ctx, `INSERT INTO backend_binding_epochs(run_id,project_id,epoch,document,digest) VALUES($1,$2,$3,$4,$5)`, next.RunID, next.ProjectID, next.Epoch, nextBytes, next.Digest); err != nil {
			return err
		}
		documents, err := loadImportDocumentsTx(ctx, tx, importID)
		if err != nil {
			return err
		}
		authority := &importMaterializationAuthority{ProjectID: next.ProjectID, BindingDigest: next.Digest, BackendIdentity: next.BackendIdentity, Epoch: next.Epoch, CreatedAt: now}
		for _, document := range documents {
			if err = materializeImportDocumentTx(ctx, tx, document, authority); err != nil {
				return err
			}
		}
		for _, document := range documents {
			if err = verifyMaterializedImportDocumentTx(ctx, tx, document); err != nil {
				return err
			}
		}
		newHead, err := controlplane.SealBindingHead(controlplane.BindingHead{RunID: head.RunID, ProjectID: head.ProjectID, Epoch: next.Epoch, EpochDigest: next.Digest, Revision: head.Revision + 2})
		if err != nil {
			return err
		}
		newHeadBytes, _ := canonical(newHead)
		tag, err := tx.Exec(ctx, `UPDATE backend_binding_heads SET epoch=$1,epoch_digest=$2,revision=$3,document=$4,digest=$5 WHERE run_id=$6 AND digest=$7`, newHead.Epoch, newHead.EpochDigest, newHead.Revision, newHeadBytes, newHead.Digest, newHead.RunID, head.Digest)
		if err != nil || tag.RowsAffected() != 1 {
			return controlplane.ErrConflict
		}
		if _, err = tx.Exec(ctx, `INSERT INTO backend_binding_events(run_id,sequence,kind,epoch,occurred_at) VALUES($1,$2,'archived',$3,$4),($1,$5,'cutover',$6,$4)`, head.RunID, head.Revision+1, head.Epoch, now, head.Revision+2, next.Epoch); err != nil {
			return err
		}
		receipt, err = controlplane.SealImportCutoverReceipt(controlplane.ImportCutoverReceipt{ImportID: importID, RunID: head.RunID, SourceHead: head.Digest, TargetHead: newHead.Digest, ArchivedEpoch: head.Epoch, ActiveEpoch: next.Epoch, CommittedAt: now})
		if err != nil {
			return err
		}
		if _, err = tx.Exec(ctx, `INSERT INTO import_cutover_receipts(import_id,run_id,source_head_digest,target_head_digest,archived_epoch,active_epoch,committed_at,digest) VALUES($1,$2,$3,$4,$5,$6,$7,$8)`, receipt.ImportID, receipt.RunID, receipt.SourceHead, receipt.TargetHead, receipt.ArchivedEpoch, receipt.ActiveEpoch, receipt.CommittedAt, receipt.Digest); err != nil {
			return err
		}
		cutover, err := controlplane.AdvanceImport(manifest, controlplane.ImportCutover)
		if err != nil {
			return err
		}
		data, _ := canonical(cutover)
		_, err = tx.Exec(ctx, `UPDATE import_manifests SET document=$1,digest=$2,state=$3 WHERE import_id=$4 AND digest=$5`, data, cutover.Digest, cutover.State, importID, manifest.Digest)
		return err
	})
	if err != nil {
		return controlplane.ImportCutoverReceipt{}, err
	}
	if faultErr := store.injectFault("import.after_commit"); faultErr != nil {
		return controlplane.ImportCutoverReceipt{}, mapOperationError(faultErr, "import.cutover")
	}
	return receipt, nil
}

// loadImportDocumentsTx returns the verified candidate in deterministic
// dependency order so cutover can materialize it before publishing authority.
func loadImportDocumentsTx(ctx context.Context, tx pgx.Tx, importID string) ([]controlplane.ImportDocument, error) {
	rows, err := tx.Query(ctx, `SELECT stream_name,ordinal,relation_key,document,digest FROM import_streams WHERE import_id=$1 ORDER BY stream_name,ordinal`, importID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	streams := map[string][]controlplane.ImportDocument{}
	for rows.Next() {
		var item controlplane.ImportDocument
		if err = rows.Scan(&item.Stream, &item.Ordinal, &item.RelationKey, &item.Document, &item.Digest); err != nil {
			return nil, err
		}
		streams[item.Stream] = append(streams[item.Stream], item)
	}
	if rows.Err() != nil {
		return nil, rows.Err()
	}
	return orderImportDocuments(streams), nil
}

// LoadImportCutoverReceipt resolves unknown commit outcomes without repeating the authority transition.
func (store *Store) LoadImportCutoverReceipt(ctx context.Context, importID string) (controlplane.ImportCutoverReceipt, error) {
	receipt, err := loadImportReceiptTx(ctx, store.pool, importID)
	if err == pgx.ErrNoRows {
		return controlplane.ImportCutoverReceipt{}, controlplane.ErrNotFound
	}
	if err != nil {
		return controlplane.ImportCutoverReceipt{}, mapOperationError(err, "import.receipt")
	}
	return receipt, nil
}

// loadImportTx reloads and verifies one locked candidate manifest inside its causal transaction.
func loadImportTx(ctx context.Context, tx interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}, importID string) (controlplane.ImportManifest, error) {
	var data []byte
	if err := tx.QueryRow(ctx, `SELECT document FROM import_manifests WHERE import_id=$1 FOR UPDATE`, importID).Scan(&data); err != nil {
		return controlplane.ImportManifest{}, err
	}
	var manifest controlplane.ImportManifest
	if controlplane.StrictDecode(data, &manifest) != nil {
		return controlplane.ImportManifest{}, controlplane.ErrIntegrity
	}
	want := manifest.Digest
	sealed, err := controlplane.SealImportManifest(manifest)
	if err != nil || sealed.Digest != want {
		return controlplane.ImportManifest{}, controlplane.ErrIntegrity
	}
	return sealed, nil
}

// loadImportReceiptTx strictly verifies a committed cutover receipt through any query-row capable boundary.
func loadImportReceiptTx(ctx context.Context, tx interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}, importID string) (controlplane.ImportCutoverReceipt, error) {
	var value controlplane.ImportCutoverReceipt
	var archivedEpoch, activeEpoch int64
	if err := tx.QueryRow(ctx, `SELECT import_id,run_id,source_head_digest,target_head_digest,archived_epoch,active_epoch,committed_at,digest FROM import_cutover_receipts WHERE import_id=$1`, importID).Scan(&value.ImportID, &value.RunID, &value.SourceHead, &value.TargetHead, &archivedEpoch, &activeEpoch, &value.CommittedAt, &value.Digest); err != nil {
		return controlplane.ImportCutoverReceipt{}, err
	}
	if archivedEpoch <= 0 || activeEpoch <= 0 {
		return controlplane.ImportCutoverReceipt{}, controlplane.ErrIntegrity
	}
	value.ArchivedEpoch, value.ActiveEpoch = uint64(archivedEpoch), uint64(activeEpoch)
	want := value.Digest
	sealed, err := controlplane.SealImportCutoverReceipt(value)
	if err != nil || sealed.Digest != want {
		return controlplane.ImportCutoverReceipt{}, controlplane.ErrIntegrity
	}
	return sealed, nil
}

// postgresImportDigest recomputes stable staged byte or relation identity for candidate verification.
func postgresImportDigest(documents []controlplane.ImportDocument, relations bool) string {
	ordered := append([]controlplane.ImportDocument(nil), documents...)
	sort.Slice(ordered, func(i, j int) bool { return ordered[i].Ordinal < ordered[j].Ordinal })
	hash := sha256.New()
	for _, item := range ordered {
		if relations {
			_, _ = hash.Write([]byte(item.RelationKey))
		} else {
			_, _ = hash.Write(item.Document)
		}
	}
	return hex.EncodeToString(hash.Sum(nil))
}

// LoadImport strictly reloads one offline candidate without granting authority.
func (store *Store) LoadImport(ctx context.Context, id string) (controlplane.ImportManifest, error) {
	var data []byte
	if err := store.pool.QueryRow(ctx, `SELECT document FROM import_manifests WHERE import_id=$1`, id).Scan(&data); err == pgx.ErrNoRows {
		return controlplane.ImportManifest{}, controlplane.ErrNotFound
	} else if err != nil {
		return controlplane.ImportManifest{}, mapError(err)
	}
	var value controlplane.ImportManifest
	if controlplane.StrictDecode(data, &value) != nil {
		return controlplane.ImportManifest{}, controlplane.ErrIntegrity
	}
	expected := value.Digest
	sealed, err := controlplane.SealImportManifest(value)
	if err != nil || expected != sealed.Digest {
		return controlplane.ImportManifest{}, controlplane.ErrIntegrity
	}
	return sealed, nil
}

// DeleteTerminalBefore summarizes and removes one locked terminal-time page while preserving live references and tombstones.
func (store *Store) DeleteTerminalBefore(ctx context.Context, beforeUnix int64, limit int) (int, error) {
	return store.deleteTerminalBefore(ctx, "", beforeUnix, limit)
}

// DeleteProjectTerminalBefore summarizes and removes one authenticated
// project's locked terminal-time page without observing unrelated projects.
func (store *Store) DeleteProjectTerminalBefore(ctx context.Context, projectID string, beforeUnix int64, limit int) (int, error) {
	if !controlplane.SafeID(projectID) {
		return 0, controlplane.ErrInvalid
	}
	return store.deleteTerminalBefore(ctx, projectID, beforeUnix, limit)
}

// deleteTerminalBefore implements the shared global maintenance and
// project-scoped controller transaction while keeping the SQL parameterized.
func (store *Store) deleteTerminalBefore(ctx context.Context, projectID string, beforeUnix int64, limit int) (int, error) {
	if beforeUnix <= 0 || limit <= 0 || limit > 1000 {
		return 0, controlplane.ErrInvalid
	}
	var deleted int64
	err := retryTransactionObserved(ctx, store.pool, store.maxTxAttempts, "retention.delete_page", store.observer, func(tx pgx.Tx) error {
		rows, queryErr := tx.Query(ctx, `SELECT j.project_id,j.job_id,j.terminal_at,j.state,j.digest FROM jobs j WHERE ($1='' OR j.project_id=$1) AND j.terminal_at<$2 AND NOT EXISTS (SELECT 1 FROM retention_references r WHERE r.project_id=j.project_id AND r.job_id=j.job_id AND r.live) AND NOT EXISTS (SELECT 1 FROM effect_intents e WHERE e.run_id=j.run_id AND e.state<>'confirmed') AND NOT EXISTS (SELECT 1 FROM authority_envelopes a WHERE a.job_id=j.job_id) ORDER BY j.terminal_at,j.job_id FOR UPDATE OF j SKIP LOCKED LIMIT $3`, projectID, time.Unix(beforeUnix, 0).UTC(), limit)
		if queryErr != nil {
			return queryErr
		}
		type terminalSummary struct {
			projectID, jobID, state, digest string
			terminalAt                      time.Time
		}
		var summaries []terminalSummary
		for rows.Next() {
			var item terminalSummary
			if scanErr := rows.Scan(&item.projectID, &item.jobID, &item.terminalAt, &item.state, &item.digest); scanErr != nil {
				rows.Close()
				return scanErr
			}
			summaries = append(summaries, item)
		}
		rows.Close()
		if rows.Err() != nil {
			return rows.Err()
		}
		for _, item := range summaries {
			if _, insertErr := tx.Exec(ctx, `INSERT INTO retention_summaries(project_id,job_id,terminal_at,state,digest) VALUES($1,$2,$3,$4,$5) ON CONFLICT(job_id,project_id) DO NOTHING`, item.projectID, item.jobID, item.terminalAt, item.state, item.digest); insertErr != nil {
				return insertErr
			}
			if _, deleteErr := tx.Exec(ctx, `DELETE FROM jobs WHERE project_id=$1 AND job_id=$2 AND terminal_at=$3`, item.projectID, item.jobID, item.terminalAt); deleteErr != nil {
				return deleteErr
			}
		}
		deleted = int64(len(summaries))
		return nil
	})
	if err != nil {
		return 0, err
	}
	store.observe(ctx, controlplane.Observation{Operation: "retention.deleted", Count: deleted})
	return int(deleted), nil
}
