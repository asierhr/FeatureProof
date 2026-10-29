package postgres

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/asierhr/featureProof/internal/controlplane"
	"github.com/asierhr/featureProof/internal/fixture"
	"github.com/asierhr/featureProof/internal/functionalflow"
	"github.com/asierhr/featureProof/internal/validationworkflow"
	"github.com/jackc/pgx/v5"
)

// PostgresFunctionalArtifactStore owns admitted functional artifacts in the same fenced backend as their workflow.
type PostgresFunctionalArtifactStore struct{ store *Store }

// PostgresFunctionalEvidenceStore owns sanitized evidence and exposes only authenticated package manifests.
type PostgresFunctionalEvidenceStore struct{ store *Store }

// PostgresFunctionalPublisher stages primary proof and commits canonical packages without filesystem paths.
type PostgresFunctionalPublisher struct {
	store     *Store
	artifacts validationworkflow.FunctionalFlowArtifactStore
	evidence  validationworkflow.FunctionalFlowEvidenceManifestSource
	journal   functionalflow.EventJournal
}

// PostgresFunctionalCatalog resolves preauthorized immutable flow documents without a mutable local catalog.
type PostgresFunctionalCatalog struct {
	store   *Store
	aliases map[string]string
	limits  functionalflow.Limits
}

// PostgresFunctionalAttemptSink persists only bounded identity-attempt audit facts.
type PostgresFunctionalAttemptSink struct{ store *Store }

// postgresFunctionalEvidence is the private at-rest shape retaining sanitized output needed by assertions.
type postgresFunctionalEvidence struct {
	ID, RunID, StepID, BindingDigest, ResourceBindingDigest, Digest string
	Revision                                                        uint64
	Output                                                          any
	Terminal, Truncated                                             bool
}

var (
	// postgresFunctionalArtifactContract prevents the artifact adapter from drifting from its consumer-owned port.
	postgresFunctionalArtifactContract validationworkflow.FunctionalFlowArtifactStore = (*PostgresFunctionalArtifactStore)(nil)
	// postgresFunctionalEvidenceContract prevents private evidence storage from drifting from executor semantics.
	postgresFunctionalEvidenceContract functionalflow.EvidenceStore = (*PostgresFunctionalEvidenceStore)(nil)
	// postgresFunctionalManifestContract exposes sanitized evidence metadata to package assembly without leaking raw output.
	postgresFunctionalManifestContract validationworkflow.FunctionalFlowEvidenceManifestSource = (*PostgresFunctionalEvidenceStore)(nil)
	// postgresFunctionalPublisherContract binds staged proof and package commit to the workflow publication port.
	postgresFunctionalPublisherContract validationworkflow.FunctionalFlowPublisher = (*PostgresFunctionalPublisher)(nil)
	// postgresFunctionalCatalogContract proves PostgreSQL can replace the local provided-flow catalog.
	postgresFunctionalCatalogContract validationworkflow.FunctionalFlowCatalog = (*PostgresFunctionalCatalog)(nil)
	// postgresFunctionalAttemptContract persists bounded identity attempts for audit without granting execution authority.
	postgresFunctionalAttemptContract validationworkflow.FunctionalIdentityAttemptSink = (*PostgresFunctionalAttemptSink)(nil)
)

// FunctionalRuntimeStores returns the PostgreSQL ports needed by the productive functional-flow runtime.
func (store *Store) FunctionalRuntimeStores(aliases map[string]string, limits functionalflow.Limits, journal functionalflow.EventJournal) (validationworkflow.FunctionalFlowArtifactStore, functionalflow.EvidenceStore, validationworkflow.FunctionalFlowPublisher, validationworkflow.FunctionalFlowCatalog, validationworkflow.FunctionalIdentityAttemptSink, error) {
	if store == nil || journal == nil || limits.MaxDocumentBytes <= 0 {
		return nil, nil, nil, nil, nil, controlplane.ErrInvalid
	}
	artifacts := &PostgresFunctionalArtifactStore{store: store}
	evidence := &PostgresFunctionalEvidenceStore{store: store}
	publisher := &PostgresFunctionalPublisher{store: store, artifacts: artifacts, evidence: evidence, journal: journal}
	catalog := &PostgresFunctionalCatalog{store: store, aliases: cloneStringMap(aliases), limits: limits}
	return artifacts, evidence, publisher, catalog, &PostgresFunctionalAttemptSink{store: store}, nil
}

// cloneStringMap detaches composition-owned alias policy from caller mutation.
func cloneStringMap(values map[string]string) map[string]string {
	result := make(map[string]string, len(values))
	for key, value := range values {
		result[key] = value
	}
	return result
}

// Commit confirms one immutable artifact only while this adapter owns the active workflow epoch.
func (adapter *PostgresFunctionalArtifactStore) Commit(ctx context.Context, handle string, artifact functionalflow.Artifact) error {
	if adapter == nil || adapter.store == nil || !workflowID.MatchString(handle) || functionalflow.ValidateArtifact(artifact) != nil {
		return &validationworkflow.Error{Code: validationworkflow.CodeInvalid, Operation: "functional_flow_commit"}
	}
	data, err := canonical(artifact)
	if err != nil {
		return err
	}
	digest := digestBytes(data)
	tx, err := adapter.store.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return workflowError(mapError(err), "functional_flow_commit")
	}
	defer tx.Rollback(ctx)
	if _, _, err = adapter.store.verifyWorkflowBackendTx(ctx, tx, handle); err != nil {
		return workflowError(err, "functional_flow_backend")
	}
	result, err := tx.Exec(ctx, `INSERT INTO functional_artifacts(handle,document,digest) VALUES($1,$2,$3) ON CONFLICT DO NOTHING`, handle, data, digest)
	if err != nil {
		return workflowError(mapError(err), "functional_flow_commit")
	}
	if result.RowsAffected() == 0 {
		var existing string
		if err = tx.QueryRow(ctx, `SELECT digest FROM functional_artifacts WHERE handle=$1`, handle).Scan(&existing); err != nil || existing != digest {
			return &validationworkflow.Error{Code: validationworkflow.CodeConflict, Operation: "functional_flow_commit"}
		}
	}
	return workflowError(mapError(tx.Commit(ctx)), "functional_flow_commit")
}

// Load revalidates the active backend and immutable artifact digest before returning a detached value.
func (adapter *PostgresFunctionalArtifactStore) Load(ctx context.Context, handle string) (functionalflow.Artifact, error) {
	if adapter == nil || adapter.store == nil || !workflowID.MatchString(handle) {
		return functionalflow.Artifact{}, &validationworkflow.Error{Code: validationworkflow.CodeInvalid, Operation: "functional_flow_load"}
	}
	var data []byte
	var digest string
	err := adapter.store.pool.QueryRow(ctx, `SELECT a.document,a.digest FROM functional_artifacts a JOIN backend_binding_heads h ON h.run_id=a.handle JOIN backend_binding_epochs e ON e.run_id=h.run_id AND e.epoch=h.epoch WHERE a.handle=$1 AND convert_from(e.document,'UTF8')::jsonb->>'backend_identity'=$2`, handle, adapter.store.backendIdentity).Scan(&data, &digest)
	if err == pgx.ErrNoRows {
		return functionalflow.Artifact{}, validationworkflow.ErrNotFound
	}
	if err != nil || digestBytes(data) != digest {
		return functionalflow.Artifact{}, &validationworkflow.Error{Code: validationworkflow.CodeConflict, Operation: "functional_flow_integrity"}
	}
	var artifact functionalflow.Artifact
	if controlplane.StrictDecode(data, &artifact) != nil || functionalflow.ValidateArtifact(artifact) != nil {
		return functionalflow.Artifact{}, &validationworkflow.Error{Code: validationworkflow.CodeConflict, Operation: "functional_flow_integrity"}
	}
	return artifact, nil
}

// Confirm stores one sanitized evidence object immutably under its workflow's active backend head.
func (adapter *PostgresFunctionalEvidenceStore) Confirm(ctx context.Context, item functionalflow.Evidence) (functionalflow.Evidence, error) {
	if adapter == nil || adapter.store == nil || !workflowID.MatchString(item.RunID) || item.ID == "" || functionalflow.SealEvidence(item).Digest != item.Digest {
		return functionalflow.Evidence{}, controlplane.ErrInvalid
	}
	record := postgresFunctionalEvidence{ID: item.ID, RunID: item.RunID, Revision: item.Revision, StepID: item.StepID, BindingDigest: item.BindingDigest, ResourceBindingDigest: item.ResourceBindingDigest, Output: item.Output, Terminal: item.Terminal, Truncated: item.Truncated, Digest: item.Digest}
	data, err := canonical(record)
	if err != nil {
		return functionalflow.Evidence{}, err
	}
	digest := digestBytes(data)
	tx, err := adapter.store.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return functionalflow.Evidence{}, mapError(err)
	}
	defer tx.Rollback(ctx)
	if _, _, err = adapter.store.verifyWorkflowBackendTx(ctx, tx, item.RunID); err != nil {
		return functionalflow.Evidence{}, err
	}
	result, err := tx.Exec(ctx, `INSERT INTO functional_evidence(evidence_id,handle,document,digest) VALUES($1,$2,$3,$4) ON CONFLICT DO NOTHING`, item.ID, item.RunID, data, digest)
	if err != nil {
		return functionalflow.Evidence{}, mapError(err)
	}
	if result.RowsAffected() == 0 {
		var existing string
		if err = tx.QueryRow(ctx, `SELECT digest FROM functional_evidence WHERE evidence_id=$1`, item.ID).Scan(&existing); err != nil || existing != digest {
			return functionalflow.Evidence{}, controlplane.ErrConflict
		}
	}
	if err = tx.Commit(ctx); err != nil {
		return functionalflow.Evidence{}, mapError(err)
	}
	return adapter.Load(ctx, item.ID)
}

// Load verifies one private evidence record and reconstructs its detached sanitized output.
func (adapter *PostgresFunctionalEvidenceStore) Load(ctx context.Context, evidenceID string) (functionalflow.Evidence, error) {
	var data []byte
	var digest string
	if err := adapter.store.pool.QueryRow(ctx, `SELECT f.document,f.digest FROM functional_evidence f JOIN backend_binding_heads h ON h.run_id=f.handle JOIN backend_binding_epochs e ON e.run_id=h.run_id AND e.epoch=h.epoch WHERE f.evidence_id=$1 AND convert_from(e.document,'UTF8')::jsonb->>'backend_identity'=$2`, evidenceID, adapter.store.backendIdentity).Scan(&data, &digest); err == pgx.ErrNoRows {
		return functionalflow.Evidence{}, validationworkflow.ErrNotFound
	} else if err != nil {
		return functionalflow.Evidence{}, mapError(err)
	}
	if digestBytes(data) != digest {
		return functionalflow.Evidence{}, controlplane.ErrIntegrity
	}
	var record postgresFunctionalEvidence
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	if decoder.Decode(&record) != nil {
		return functionalflow.Evidence{}, controlplane.ErrIntegrity
	}
	item := functionalflow.Evidence{ID: record.ID, RunID: record.RunID, Revision: record.Revision, StepID: record.StepID, BindingDigest: record.BindingDigest, ResourceBindingDigest: record.ResourceBindingDigest, Output: record.Output, Terminal: record.Terminal, Truncated: record.Truncated, Digest: record.Digest}
	if functionalflow.SealEvidence(item).Digest != item.Digest {
		return functionalflow.Evidence{}, controlplane.ErrIntegrity
	}
	return item, nil
}

// Records projects unique authenticated evidence manifests reachable from one immutable primary ledger.
func (adapter *PostgresFunctionalEvidenceStore) Records(ctx context.Context, runID string, result functionalflow.Result) ([]functionalflow.EvidenceRecord, error) {
	seen := map[string]bool{}
	records := []functionalflow.EvidenceRecord{}
	for _, entry := range result.Ledger {
		for _, evidenceID := range entry.EvidenceIDs {
			if seen[evidenceID] {
				continue
			}
			item, err := adapter.Load(ctx, evidenceID)
			if err != nil || item.RunID != runID {
				return nil, controlplane.ErrIntegrity
			}
			record, err := functionalflow.SealEvidenceRecord(item)
			if err != nil {
				return nil, controlplane.ErrIntegrity
			}
			records, seen[evidenceID] = append(records, record), true
		}
	}
	return records, nil
}

// Stage persists immutable primary proof before cleanup and package publication.
func (publisher *PostgresFunctionalPublisher) Stage(ctx context.Context, handle string, artifact functionalflow.Artifact, result functionalflow.Result) error {
	if publisher == nil || result.ArtifactDigest != artifact.ArtifactDigest || functionalflow.ValidateArtifact(artifact) != nil {
		return controlplane.ErrInvalid
	}
	document := struct {
		Artifact functionalflow.Artifact `json:"artifact"`
		Primary  functionalflow.Result   `json:"primary"`
	}{artifact, result}
	data, err := canonical(document)
	if err != nil {
		return err
	}
	digest := digestBytes(data)
	tx, err := publisher.store.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return mapError(err)
	}
	defer tx.Rollback(ctx)
	if _, _, err = publisher.store.verifyWorkflowBackendTx(ctx, tx, handle); err != nil {
		return err
	}
	resultTag, err := tx.Exec(ctx, `INSERT INTO functional_primaries(handle,artifact_digest,document,digest) VALUES($1,$2,$3,$4) ON CONFLICT DO NOTHING`, handle, artifact.ArtifactDigest, data, digest)
	if err != nil {
		return mapError(err)
	}
	if resultTag.RowsAffected() == 0 {
		var existing string
		if err = tx.QueryRow(ctx, `SELECT digest FROM functional_primaries WHERE handle=$1`, handle).Scan(&existing); err != nil || existing != digest {
			return controlplane.ErrConflict
		}
	}
	return mapError(tx.Commit(ctx))
}

// Finalize seals journal, evidence and cleanup into canonical package bytes stored under the active workflow epoch.
func (publisher *PostgresFunctionalPublisher) Finalize(ctx context.Context, handle string, outcome validationworkflow.Outcome, summary fixture.FixtureSummary) (validationworkflow.PackageProjection, error) {
	if publisher == nil || !workflowID.MatchString(handle) || !validPreparationStatus(summary.PreparationStatus) {
		return validationworkflow.PackageProjection{}, controlplane.ErrInvalid
	}
	artifact, err := publisher.artifacts.Load(ctx, handle)
	if err != nil {
		return validationworkflow.PackageProjection{}, err
	}
	primary, err := publisher.loadPrimary(ctx, handle)
	if errors.Is(err, controlplane.ErrNotFound) {
		if summary.PreparationStatus != fixture.PreparationFailed && summary.PreparationStatus != fixture.PreparationCancelled && summary.PreparationStatus != fixture.PreparationRejected {
			return validationworkflow.PackageProjection{}, controlplane.ErrUnavailable
		}
		primary = postgresAbortedPrimary(artifact, outcome)
	} else if err != nil {
		return validationworkflow.PackageProjection{}, err
	}
	if postgresWorkflowOutcome(primary.Outcome) != outcome {
		return validationworkflow.PackageProjection{}, controlplane.ErrConflict
	}
	records, err := publisher.evidence.Records(ctx, handle, primary)
	if err != nil && primaryHasEvidence(primary) {
		return validationworkflow.PackageProjection{}, err
	}
	events, err := publisher.journal.Events(ctx, handle)
	if err != nil || len(events) < 2 || events[1].Type != "fixtures_terminal" || events[1].Status != string(summary.PreparationStatus) {
		return validationworkflow.PackageProjection{}, controlplane.ErrConflict
	}
	allEvidence := primaryEvidenceIDs(primary)
	if events[len(events)-1].Type != "primary_terminal" && events[len(events)-1].Type != "cleanup_terminal" {
		if _, err = publisher.journal.AppendEvent(ctx, functionalflow.Event{Type: "primary_terminal", Outcome: primary.Outcome, EvidenceIDs: allEvidence, RunID: handle, FlowID: artifact.FlowID, Revision: artifact.Revision, ArtifactDigest: artifact.ArtifactDigest}); err != nil {
			return validationworkflow.PackageProjection{}, err
		}
	}
	events, err = publisher.journal.Events(ctx, handle)
	if err != nil {
		return validationworkflow.PackageProjection{}, err
	}
	if events[len(events)-1].Type != "cleanup_terminal" {
		if _, err = publisher.journal.AppendEvent(ctx, functionalflow.Event{Type: "cleanup_terminal", Status: string(summary.Cleanup.Status), RunID: handle, FlowID: artifact.FlowID, Revision: artifact.Revision, ArtifactDigest: artifact.ArtifactDigest}); err != nil {
			return validationworkflow.PackageProjection{}, err
		}
	}
	events, err = publisher.journal.Events(ctx, handle)
	if err != nil {
		return validationworkflow.PackageProjection{}, err
	}
	pkg, err := functionalflow.BuildPackage(handle, artifact, records, events, primary, summary, summary.Cleanup)
	if err != nil {
		return validationworkflow.PackageProjection{}, err
	}
	data, err := canonical(pkg)
	if err != nil {
		return validationworkflow.PackageProjection{}, err
	}
	head, epoch, err := publisher.workflowAuthority(ctx, handle)
	if err != nil {
		return validationworkflow.PackageProjection{}, err
	}
	packageID := handle + "-functional"
	stored, err := publisher.store.CommitPackage(ctx, validationworkflow.PackageDocument{ProjectID: head.ProjectID, RunID: handle, PackageID: packageID, MediaType: "application/vnd.featureproof.functional-flow+json", SchemaVersion: pkg.Version, ArtifactDigest: artifact.ArtifactDigest, BindingDigest: head.EpochDigest, BackendEpoch: head.Epoch, BackendIdentity: epoch.BackendIdentity, Document: data, Digest: digestBytes(data), CreatedAt: time.Now().UTC()})
	if err != nil {
		return validationworkflow.PackageProjection{}, err
	}
	return validationworkflow.PackageProjection{RunID: handle, BindingSHA256: artifact.ArtifactDigest, PackageDir: "postgres:" + head.ProjectID + "/" + stored.PackageID, Outcome: outcome, Fixtures: &summary}, nil
}

// loadPrimary authenticates staged proof before package construction.
func (publisher *PostgresFunctionalPublisher) loadPrimary(ctx context.Context, handle string) (functionalflow.Result, error) {
	var data []byte
	var digest string
	if err := publisher.store.pool.QueryRow(ctx, `SELECT document,digest FROM functional_primaries WHERE handle=$1`, handle).Scan(&data, &digest); err == pgx.ErrNoRows {
		return functionalflow.Result{}, controlplane.ErrNotFound
	} else if err != nil {
		return functionalflow.Result{}, mapError(err)
	}
	if digestBytes(data) != digest {
		return functionalflow.Result{}, controlplane.ErrIntegrity
	}
	var staged struct {
		Artifact functionalflow.Artifact `json:"artifact"`
		Primary  functionalflow.Result   `json:"primary"`
	}
	if controlplane.StrictDecode(data, &staged) != nil || functionalflow.ValidateArtifact(staged.Artifact) != nil || staged.Primary.ArtifactDigest != staged.Artifact.ArtifactDigest {
		return functionalflow.Result{}, controlplane.ErrIntegrity
	}
	return staged.Primary, nil
}

// workflowAuthority loads the exact active PostgreSQL binding used to seal package lineage.
func (publisher *PostgresFunctionalPublisher) workflowAuthority(ctx context.Context, handle string) (controlplane.BindingHead, controlplane.BackendEpoch, error) {
	tx, err := publisher.store.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return controlplane.BindingHead{}, controlplane.BackendEpoch{}, mapError(err)
	}
	defer tx.Rollback(ctx)
	head, epoch, err := publisher.store.verifyWorkflowBackendTx(ctx, tx, handle)
	if err != nil {
		return controlplane.BindingHead{}, controlplane.BackendEpoch{}, err
	}
	if err = tx.Commit(ctx); err != nil {
		return controlplane.BindingHead{}, controlplane.BackendEpoch{}, mapError(err)
	}
	return head, epoch, nil
}

// AppendIdentityAttempt writes one content-free authenticated-attempt fact under the active workflow backend.
func (sink *PostgresFunctionalAttemptSink) AppendIdentityAttempt(ctx context.Context, attempt validationworkflow.FunctionalIdentityAttempt) error {
	if sink == nil || sink.store == nil || !workflowID.MatchString(attempt.RunID) || attempt.IdentityID == "" || len(attempt.OperationBindingSHA256) != 64 || attempt.Attempt <= 0 || !validAttemptOutcome(attempt.OutcomeCategory) {
		return controlplane.ErrInvalid
	}
	tx, err := sink.store.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return mapError(err)
	}
	defer tx.Rollback(ctx)
	if _, _, err = sink.store.verifyWorkflowBackendTx(ctx, tx, attempt.RunID); err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `INSERT INTO functional_identity_attempts(handle,identity_id,operation_binding_digest,attempt,outcome_category) VALUES($1,$2,$3,$4,$5)`, attempt.RunID, attempt.IdentityID, attempt.OperationBindingSHA256, attempt.Attempt, attempt.OutcomeCategory)
	if err != nil {
		return mapError(err)
	}
	return mapError(tx.Commit(ctx))
}

// Resolve accepts only a configured alias or existing immutable artifact coordinate; it never imports arbitrary paths in distributed mode.
func (catalog *PostgresFunctionalCatalog) Resolve(ctx context.Context, selection string) (string, error) {
	if catalog == nil || ctx.Err() != nil || selection == "" || strings.ContainsAny(selection, `/\\`) {
		return "", &validationworkflow.Error{Code: validationworkflow.CodeInvalid, Operation: "functional_flow_manifest"}
	}
	artifactID := selection
	if aliased := catalog.aliases[selection]; aliased != "" {
		artifactID = aliased
	}
	var exists bool
	if err := catalog.store.pool.QueryRow(ctx, `SELECT true FROM functional_catalog WHERE artifact_id=$1`, artifactID).Scan(&exists); err != nil || !exists {
		return "", validationworkflow.ErrNotFound
	}
	return artifactID, nil
}

// Load returns canonical provided-flow bytes selected by one composition-owned alias or immutable ID.
func (catalog *PostgresFunctionalCatalog) Load(ctx context.Context, selection validationworkflow.FunctionalFlowSelection) ([]byte, error) {
	if catalog == nil || (selection.Alias == "") == (selection.ArtifactID == "") {
		return nil, controlplane.ErrInvalid
	}
	artifactID := selection.ArtifactID
	if selection.Alias != "" {
		artifactID = catalog.aliases[selection.Alias]
	}
	var data []byte
	var digest string
	if err := catalog.store.pool.QueryRow(ctx, `SELECT document,digest FROM functional_catalog WHERE artifact_id=$1`, artifactID).Scan(&data, &digest); err == pgx.ErrNoRows {
		return nil, validationworkflow.ErrNotFound
	} else if err != nil {
		return nil, mapError(err)
	}
	if len(data) == 0 || len(data) > catalog.limits.MaxDocumentBytes || digestBytes(data) != digest {
		return nil, controlplane.ErrIntegrity
	}
	if _, err := functionalflow.DecodeProvided(data, catalog.limits); err != nil {
		return nil, controlplane.ErrIntegrity
	}
	return append([]byte(nil), data...), nil
}

// PutProvidedFlow stores one canonical provided document for later alias/ID resolution during distributed restarts.
func (catalog *PostgresFunctionalCatalog) PutProvidedFlow(ctx context.Context, document []byte) (string, error) {
	flow, err := functionalflow.DecodeProvided(document, catalog.limits)
	if err != nil {
		return "", err
	}
	data, err := canonical(flow)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(data)
	artifactID := hex.EncodeToString(sum[:])
	digest := digestBytes(data)
	tag, err := catalog.store.pool.Exec(ctx, `INSERT INTO functional_catalog(artifact_id,document,digest) VALUES($1,$2,$3) ON CONFLICT DO NOTHING`, artifactID, data, digest)
	if err != nil {
		return "", mapError(err)
	}
	if tag.RowsAffected() == 0 {
		var existing string
		if err = catalog.store.pool.QueryRow(ctx, `SELECT digest FROM functional_catalog WHERE artifact_id=$1`, artifactID).Scan(&existing); err != nil || existing != digest {
			return "", controlplane.ErrConflict
		}
	}
	return artifactID, nil
}

// validPreparationStatus closes the fixture terminal vocabulary accepted by package finalization.
func validPreparationStatus(status fixture.PreparationStatus) bool {
	switch status {
	case fixture.PreparationPrepared, fixture.PreparationFailed, fixture.PreparationCancelled, fixture.PreparationNotRequired, fixture.PreparationRejected:
		return true
	default:
		return false
	}
}

// validAttemptOutcome closes the payload-free identity audit vocabulary.
func validAttemptOutcome(value string) bool {
	switch value {
	case "success", "credential_rejected", "transient", "permanent", "cancelled":
		return true
	default:
		return false
	}
}

// postgresWorkflowOutcome maps primary outcomes without widening terminal semantics.
func postgresWorkflowOutcome(value functionalflow.PrimaryOutcome) validationworkflow.Outcome {
	switch value {
	case functionalflow.OutcomePassed:
		return validationworkflow.OutcomePassed
	case functionalflow.OutcomeFailed:
		return validationworkflow.OutcomeFailed
	case functionalflow.OutcomeCancelled:
		return validationworkflow.OutcomeCancelled
	case functionalflow.OutcomeError:
		return validationworkflow.OutcomeError
	default:
		return validationworkflow.OutcomeInconclusive
	}
}

// postgresAbortedPrimary records a conservative terminal when fixture admission prevented tool execution.
func postgresAbortedPrimary(artifact functionalflow.Artifact, outcome validationworkflow.Outcome) functionalflow.Result {
	primary := functionalflow.OutcomeInconclusive
	if outcome == validationworkflow.OutcomeCancelled {
		primary = functionalflow.OutcomeCancelled
	} else if outcome == validationworkflow.OutcomeError {
		primary = functionalflow.OutcomeError
	}
	return functionalflow.Result{FlowID: artifact.FlowID, Revision: artifact.Revision, Outcome: primary, ArtifactDigest: artifact.ArtifactDigest}
}

// primaryHasEvidence reports whether manifest projection must load private evidence.
func primaryHasEvidence(primary functionalflow.Result) bool {
	for _, entry := range primary.Ledger {
		if len(entry.EvidenceIDs) > 0 {
			return true
		}
	}
	return false
}

// primaryEvidenceIDs flattens primary evidence references for the terminal journal event.
func primaryEvidenceIDs(primary functionalflow.Result) []string {
	result := []string{}
	for _, entry := range primary.Ledger {
		result = append(result, entry.EvidenceIDs...)
	}
	return result
}
