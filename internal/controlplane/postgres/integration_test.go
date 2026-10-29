//go:build postgres_integration

package postgres

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/asierhr/featureProof/internal/agentplanning"
	"github.com/asierhr/featureProof/internal/contextassembly"
	"github.com/asierhr/featureProof/internal/controlplane"
	"github.com/asierhr/featureProof/internal/controlplane/contracttest"
	"github.com/asierhr/featureProof/internal/correction"
	"github.com/asierhr/featureProof/internal/environment"
	cleanup "github.com/asierhr/featureProof/internal/environment/cleanup"
	"github.com/asierhr/featureProof/internal/fixture"
	"github.com/asierhr/featureProof/internal/functionalflow"
	"github.com/asierhr/featureProof/internal/githubpr"
	"github.com/asierhr/featureProof/internal/harness"
	"github.com/asierhr/featureProof/internal/localvalidation"
	"github.com/asierhr/featureProof/internal/planning"
	"github.com/asierhr/featureProof/internal/testidentity"
	"github.com/asierhr/featureProof/internal/validationworkflow"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// integrationImportAuthorizer grants only the explicit isolated import test
// operation and carries no reusable product credential.
type integrationImportAuthorizer struct{}

// Authorize accepts the closed offline-import operation for the tagged gate.
func (integrationImportAuthorizer) Authorize(_ context.Context, operation, projectID string) error {
	if operation != "offline_import" || projectID == "" {
		return controlplane.ErrUnauthorized
	}
	return nil
}

// integrationDigest creates stable fixture identities without exposing payloads.
func integrationDigest(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}

// integrationEnvironmentEvent seals one consumer-valid lifecycle event for
// PostgreSQL contract and fault-boundary fixtures.
func integrationEnvironmentEvent(record environment.Record, identity string, now time.Time) environment.Event {
	value, _ := environment.SealStoreEvent(environment.Event{ID: integrationDigest(identity), RunID: record.RunID, SelectionDigest: record.Selection.Digest, ProviderID: record.Selection.ProviderID, EnvironmentDigest: integrationDigest("environment-" + record.RunID), State: record.State, OccurredAt: now.UTC()}, record.RunID)
	return value
}

// TestPostgresFunctionalStoreSharedContract executes the exact same
// consumer-owned suite as the filesystem implementation across fresh pools.
func TestPostgresFunctionalStoreSharedContract(t *testing.T) {
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Microsecond)
	owner := integrationStore(t)
	if err := owner.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	integrationReserveBackend(t, owner, "postgres-functional-contract", "contract-project", now)
	ctx = integrationBoundContext(t, owner, "postgres-functional-contract")
	clock := contracttest.FixedFunctionalClock{At: now}
	dsn := owner.pool.Config().ConnString()
	opened := []*Store{}
	factory := func() (contracttest.FunctionalStore, error) {
		store, err := Open(ctx, Config{DSN: dsn, BackendIdentity: "pg-test", MaxConnections: 2, StatementTimeout: 5 * time.Second, MaxLease: 5 * time.Minute})
		if err != nil {
			return nil, err
		}
		opened = append(opened, store)
		return store.FunctionalStore(clock, time.Minute)
	}
	t.Cleanup(func() {
		for _, store := range opened {
			store.Close()
		}
	})
	if err := contracttest.RunFunctionalStore(ctx, factory, "postgres-functional-contract"); err != nil {
		t.Fatal(err)
	}
}

// TestPostgresCleanupStoreSharedContract executes the exact same cleanup and
// cursor suite as the filesystem implementation across fresh pools.
func TestPostgresCleanupStoreSharedContract(t *testing.T) {
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Microsecond)
	owner := integrationStore(t)
	if err := owner.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	integrationReserveBackend(t, owner, "postgres-cleanup-contract", "contract-project", now)
	ctx = integrationBoundContext(t, owner, "postgres-cleanup-contract")
	dsn := owner.pool.Config().ConnString()
	opened := []*Store{}
	factory := func() (contracttest.CleanupStore, error) {
		store, err := Open(ctx, Config{DSN: dsn, BackendIdentity: "pg-test", MaxConnections: 2, StatementTimeout: 5 * time.Second, MaxLease: 5 * time.Minute})
		if err != nil {
			return nil, err
		}
		opened = append(opened, store)
		return store.CleanupStore(), nil
	}
	t.Cleanup(func() {
		for _, store := range opened {
			store.Close()
		}
	})
	if err := contracttest.RunCleanupStore(ctx, factory, "postgres-cleanup-contract", "contract-project", now); err != nil {
		t.Fatal(err)
	}
}

// TestPostgresEnvironmentStoreSharedContract executes the exact same lifecycle
// and cleanup-discovery suite as the filesystem implementation across pools.
func TestPostgresEnvironmentStoreSharedContract(t *testing.T) {
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Microsecond)
	owner := integrationStore(t)
	if err := owner.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	integrationReserveBackend(t, owner, "postgres-environment-contract", "contract-project", now)
	ctx = integrationBoundContext(t, owner, "postgres-environment-contract")
	dsn := owner.pool.Config().ConnString()
	opened := []*Store{}
	factory := func() (contracttest.EnvironmentStore, error) {
		store, err := Open(ctx, Config{DSN: dsn, BackendIdentity: "pg-test", MaxConnections: 2, StatementTimeout: 5 * time.Second, MaxLease: 5 * time.Minute})
		if err != nil {
			return nil, err
		}
		opened = append(opened, store)
		return store.EnvironmentStore(), nil
	}
	t.Cleanup(func() {
		for _, store := range opened {
			store.Close()
		}
	})
	if err := contracttest.RunEnvironmentStore(ctx, factory, "postgres-environment-contract", "contract-project", now); err != nil {
		t.Fatal(err)
	}
}

// TestPostgresCorrectionStoreSharedContract executes the exact same lineage
// suite as the filesystem implementation across independently reopened pools.
func TestPostgresCorrectionStoreSharedContract(t *testing.T) {
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Microsecond)
	owner := integrationStore(t)
	if err := owner.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	integrationReserveBackend(t, owner, "postgres-correction-root", "contract-project", now)
	ctx = integrationBoundContext(t, owner, "postgres-correction-root")
	dsn := owner.pool.Config().ConnString()
	opened := []*Store{}
	factory := func() (correction.Store, error) {
		store, err := Open(ctx, Config{DSN: dsn, BackendIdentity: "pg-test", MaxConnections: 2, StatementTimeout: 5 * time.Second, MaxLease: 5 * time.Minute})
		if err != nil {
			return nil, err
		}
		opened = append(opened, store)
		return store.CorrectionStore(), nil
	}
	t.Cleanup(func() {
		for _, store := range opened {
			store.Close()
		}
	})
	if err := contracttest.RunCorrectionStore(ctx, factory, "postgres-correction-root", "postgres-correction-child", "contract-project", now); err != nil {
		t.Fatal(err)
	}
}

// TestPostgresCapacityAdmissionSharedContract executes the exact same quota
// suite as the local implementation against one PostgreSQL adapter.
func TestPostgresCapacityAdmissionSharedContract(t *testing.T) {
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Microsecond)
	store := integrationStore(t)
	if err := store.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	integrationReserveBackend(t, store, "postgres-capacity-a", "contract-project", now)
	integrationReserveBackend(t, store, "postgres-capacity-b", "contract-project", now)
	admission, err := store.CapacityAdmission(map[string]environment.CapacityLimits{"contract-project": {Environments: 1, CPUMilli: 100, MemoryBytes: 100, ObjectCount: 10, MaxTTL: time.Hour}})
	if err != nil {
		t.Fatal(err)
	}
	if err = contracttest.RunCapacityAdmissionBound(ctx, admission, "contract-project", "postgres-capacity-a", "postgres-capacity-b", func(_ context.Context, runID string) context.Context {
		return integrationBoundContext(t, store, runID)
	}); err != nil {
		t.Fatal(err)
	}
}

// TestPostgresWorkflowMaterialSharedContract executes the exact same public
// artifact and private executable suite as the filesystem implementation.
func TestPostgresWorkflowMaterialSharedContract(t *testing.T) {
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Microsecond)
	owner := integrationStore(t)
	if err := owner.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	workflow := validationworkflow.Record{Version: 1, Handle: "postgres-material-contract", Principal: "contract-principal", IdempotencyKey: "contract-material", RequestDigest: integrationDigest("material-request"), RepositoryID: "contract-project", Revision: strings.Repeat("a", 40), Phase: validationworkflow.PhaseAdmitted}
	if _, created, boundContext, err := owner.WorkflowStore().ReserveBound(ctx, workflow, "contract-worker", now, time.Minute); err != nil || !created {
		t.Fatal("reserve material workflow", created, err)
	} else {
		ctx = boundContext
	}
	dsn := owner.pool.Config().ConnString()
	opened := []*Store{}
	factory := func() (validationworkflow.PlanArtifactStore, validationworkflow.ExecutablePlanStore, error) {
		store, err := Open(ctx, Config{DSN: dsn, BackendIdentity: "pg-test", MaxConnections: 2, StatementTimeout: 5 * time.Second, MaxLease: 5 * time.Minute})
		if err != nil {
			return nil, nil, err
		}
		opened = append(opened, store)
		return store.PlanArtifactStore(), store.ExecutablePlanStore(), nil
	}
	t.Cleanup(func() {
		for _, store := range opened {
			store.Close()
		}
	})
	if err := contracttest.RunWorkflowMaterialStores(ctx, factory, "postgres-material-contract"); err != nil {
		t.Fatal(err)
	}
}

// TestPostgresOfflineImportMaterializesWorkflowConsumers proves a frozen local
// workflow, artifact and executable become the real PostgreSQL projections
// before one atomic epoch cutover grants them authority.
func TestPostgresOfflineImportMaterializesWorkflowConsumers(t *testing.T) {
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Microsecond)
	store := integrationStore(t)
	if err := store.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	runID, projectID := "typed-import-run", "typed-import-project"
	epoch, err := controlplane.SealBackendEpoch(controlplane.BackendEpoch{RunID: runID, ProjectID: projectID, Epoch: 1, Mode: controlplane.BackendLocal, BackendIdentity: "typed-local", SchemaVersion: 1, CreatedAt: now})
	if err != nil {
		t.Fatal(err)
	}
	head, err := store.ReserveInitial(ctx, epoch)
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	source, err := controlplane.OpenLocalImportStore(root, "typed-local")
	if err != nil {
		t.Fatal(err)
	}
	workflowStore, _ := validationworkflow.OpenLocalStore(filepath.Join(root, "workflows"))
	artifactStore, _ := validationworkflow.OpenLocalArtifactStore(filepath.Join(root, "workflows", "plans"))
	executableStore, _ := validationworkflow.OpenLocalExecutableStore(filepath.Join(root, "workflows", "private"))
	record := validationworkflow.Record{Version: 1, Handle: runID, Principal: "typed-principal", IdempotencyKey: "typed-request", RequestDigest: integrationDigest("typed-request"), RepositoryID: projectID, Revision: strings.Repeat("a", 40), Phase: validationworkflow.PhaseAdmitted}
	if _, created, reserveErr := workflowStore.Reserve(ctx, record, "typed-worker", now, time.Minute); reserveErr != nil || !created {
		t.Fatal("local workflow", created, reserveErr)
	}
	artifact, err := validationworkflow.SealPlanArtifactForStore(validationworkflow.PlanArtifact{Version: validationworkflow.ArtifactVersion, WorkflowID: runID, PlanID: "typed-plan", Plans: []localvalidation.PlanProjection{{ID: "typed-plan"}}, ContextInstructionsHash: integrationDigest("instructions")})
	if err != nil || artifactStore.Commit(ctx, artifact) != nil {
		t.Fatal("local artifact", err)
	}
	material := validationworkflow.ExecutableMaterial{Version: validationworkflow.ArtifactVersion, WorkflowID: runID, ArtifactSHA256: artifact.ArtifactSHA256, ContextDigest: integrationDigest("context"), RevisionDigest: integrationDigest("revision"), TargetDigest: integrationDigest("target"), Snapshot: agentplanning.PreparedSnapshot{ID: "typed-plan", Checks: []agentplanning.CandidateCheck{{}}}, CommitmentKey: []byte("0123456789abcdef0123456789abcdef")}
	if err = executableStore.Commit(ctx, runID, material); err != nil {
		t.Fatal("local executable", err)
	}
	controller := controlplane.OfflineImportController{Source: source, Target: store, Authorizer: integrationImportAuthorizer{}}
	receipt, err := controller.Run(ctx, controlplane.OfflineImportRequest{ImportID: "typed-import", RunID: runID, ProjectID: projectID, ExpectedHead: head.Digest, ExpectedEpoch: head.Epoch, PageSize: 2, Now: now.Add(time.Second), ReconcileTimeout: time.Second})
	if err != nil || receipt.ActiveEpoch != 2 {
		t.Fatal("typed import", receipt.ActiveEpoch, err)
	}
	restarted, err := Open(ctx, Config{DSN: store.pool.Config().ConnString(), BackendIdentity: "pg-test", MaxConnections: 2, StatementTimeout: 5 * time.Second, MaxLease: 5 * time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	defer restarted.Close()
	if loaded, loadErr := restarted.WorkflowStore().Load(ctx, runID); loadErr != nil || loaded.Handle != runID {
		t.Fatal("materialized workflow", loadErr)
	}
	if loaded, loadErr := restarted.PlanArtifactStore().Load(ctx, runID); loadErr != nil || loaded.ArtifactSHA256 != artifact.ArtifactSHA256 {
		t.Fatal("materialized artifact", loadErr)
	}
	if loaded, loadErr := restarted.ExecutablePlanStore().Load(ctx, runID); loadErr != nil || loaded.ArtifactSHA256 != material.ArtifactSHA256 {
		t.Fatal("materialized executable", loadErr)
	}
}

// TestPostgresOfflineImportMaterializesOwnedJournals proves correction and
// GitHub source packages authenticate their journals before atomic cutover and
// remain readable after the complete filesystem source is removed.
func TestPostgresOfflineImportMaterializesOwnedJournals(t *testing.T) {
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Microsecond)
	store := integrationStore(t)
	if err := store.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	runID, projectID := "journal-import-run", "journal-import-project"
	epoch, err := controlplane.SealBackendEpoch(controlplane.BackendEpoch{RunID: runID, ProjectID: projectID, Epoch: 1, Mode: controlplane.BackendLocal, BackendIdentity: "journal-local", SchemaVersion: 1, CreatedAt: now})
	if err != nil {
		t.Fatal(err)
	}
	head, err := store.ReserveInitial(ctx, epoch)
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	correctionRoot := filepath.Join(root, "corrections")
	if err = contracttest.RunCorrectionStore(ctx, func() (correction.Store, error) { return correction.NewLocalStore(correctionRoot) }, runID, "journal-correction", projectID, now); err != nil {
		t.Fatal(err)
	}
	githubRoot := filepath.Join(root, "github")
	githubStore, err := githubpr.OpenLocalStore(githubRoot)
	if err != nil {
		t.Fatal(err)
	}
	if _, fresh, reserveErr := githubStore.ReserveDelivery(ctx, githubpr.DeliveryReceipt{Version: githubpr.SchemaVersion, DeliveryID: "journal-delivery", Event: "issue_comment", PayloadSHA256: integrationDigest("journal-payload"), State: "received"}, "journal-owner", now, time.Minute); reserveErr != nil || !fresh {
		t.Fatal("github journal", fresh, reserveErr)
	}
	cleanupPath := filepath.Join(root, "cleanup-items.json")
	if err = contracttest.RunCleanupStore(ctx, func() (contracttest.CleanupStore, error) { return cleanup.OpenLocalStore(cleanupPath) }, runID, projectID, now); err != nil {
		t.Fatal(err)
	}
	capacityPath := filepath.Join(root, "capacity.json")
	capacityLedger, err := cleanup.OpenCapacityLedger(capacityPath)
	if err != nil {
		t.Fatal(err)
	}
	capacityPolicy, err := cleanup.SealCapacityPolicy(cleanup.CapacityPolicy{ProjectID: projectID, MaxEnvironments: 2, MaxCPUMilli: 1000, MaxMemoryBytes: 1 << 20, MaxObjectCount: 100, MaxTTL: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	if _, created, reserveErr := capacityLedger.Reserve(ctx, cleanup.CapacityRequest{ProjectID: projectID, RunID: runID, AcquisitionKey: "journal-capacity", SelectionDigest: integrationDigest("journal-selection"), Demand: cleanup.ResourceDemand{Environments: 1, CPUMilli: 1, MemoryBytes: 1, ObjectCount: 1}, TTL: time.Minute, Policy: capacityPolicy}); reserveErr != nil || !created {
		t.Fatal("capacity journal", created, reserveErr)
	}
	source, err := controlplane.OpenLocalImportStore(root, "journal-local")
	if err != nil {
		t.Fatal(err)
	}
	if err = source.RegisterSnapshotExporter(func(ctx context.Context, _ string) ([]controlplane.LocalSnapshotExport, error) {
		document, exportErr := correction.ExportImportSnapshot(ctx, correctionRoot)
		return []controlplane.LocalSnapshotExport{{Stream: "correction", RelationKey: "typed-correction", Document: document, ClaimedPaths: correction.ImportClaimedPaths(correctionRoot)}}, exportErr
	}); err != nil {
		t.Fatal(err)
	}
	if err = source.RegisterSnapshotExporter(func(ctx context.Context, _ string) ([]controlplane.LocalSnapshotExport, error) {
		document, exportErr := cleanup.ExportImportSnapshot(ctx, cleanupPath)
		return []controlplane.LocalSnapshotExport{{Stream: "cleanup", RelationKey: "typed-cleanup", Document: document, ClaimedPaths: cleanup.ImportClaimedPaths(cleanupPath)}}, exportErr
	}); err != nil {
		t.Fatal(err)
	}
	if err = source.RegisterSnapshotExporter(func(ctx context.Context, _ string) ([]controlplane.LocalSnapshotExport, error) {
		document, exportErr := cleanup.ExportCapacityImportSnapshot(ctx, capacityPath)
		return []controlplane.LocalSnapshotExport{{Stream: "capacity", RelationKey: "typed-capacity", Document: document, ClaimedPaths: cleanup.ImportClaimedPaths(capacityPath)}}, exportErr
	}); err != nil {
		t.Fatal(err)
	}
	if err = source.RegisterSnapshotExporter(func(ctx context.Context, _ string) ([]controlplane.LocalSnapshotExport, error) {
		document, exportErr := githubpr.ExportImportSnapshot(ctx, githubRoot)
		return []controlplane.LocalSnapshotExport{{Stream: "github", RelationKey: "typed-github", Document: document, ClaimedPaths: githubpr.ImportClaimedPaths(githubRoot)}}, exportErr
	}); err != nil {
		t.Fatal(err)
	}
	controller := controlplane.OfflineImportController{Source: source, Target: store, Authorizer: integrationImportAuthorizer{}}
	receipt, err := controller.Run(ctx, controlplane.OfflineImportRequest{ImportID: "journal-import", RunID: runID, ProjectID: projectID, ExpectedHead: head.Digest, ExpectedEpoch: 1, PageSize: 2, Now: now.Add(time.Second), ReconcileTimeout: time.Second})
	if err != nil || receipt.ActiveEpoch != 2 {
		t.Fatal("journal cutover", receipt, err)
	}
	if err = os.RemoveAll(root); err != nil {
		t.Fatal(err)
	}
	restarted, err := Open(ctx, Config{DSN: store.pool.Config().ConnString(), BackendIdentity: "pg-test", MaxConnections: 2, StatementTimeout: 5 * time.Second, MaxLease: 5 * time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	defer restarted.Close()
	if loaded, loadErr := restarted.CorrectionStore().Get(ctx, "journal-correction"); loadErr != nil || loaded.Link.RootRunID != runID {
		t.Fatal("correction after filesystem removal", loadErr)
	}
	var githubEvents int
	if err = restarted.pool.QueryRow(ctx, `SELECT count(*) FROM github_import_events WHERE source_id='typed-github'`).Scan(&githubEvents); err != nil || githubEvents != 1 {
		t.Fatal("github after filesystem removal", githubEvents, err)
	}
	if status, loadErr := restarted.CleanupStore().Status(ctx, runID+"-cleanup"); loadErr != nil || status.ProjectID != projectID {
		t.Fatal("cleanup after filesystem removal", loadErr)
	}
	if receipts, _, scanErr := restarted.CleanupCapacityLedger().ScanReceipts(ctx, "", 10); scanErr != nil || len(receipts) != 1 || receipts[0].RunID != runID {
		t.Fatal("capacity after filesystem removal", receipts, scanErr)
	}
}

// TestPostgresFunctionalRuntimeSurvivesRestart proves artifacts, evidence, primary journal and canonical package do not depend on the local root.
func TestPostgresFunctionalRuntimeSurvivesRestart(t *testing.T) {
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Microsecond)
	store := integrationStore(t)
	if err := store.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	record := validationworkflow.Record{Version: 1, Handle: "functional-restart", Principal: "principal-functional", IdempotencyKey: "functional-key", RequestDigest: integrationDigest("functional-request"), RepositoryID: "functional-project", Revision: strings.Repeat("a", 40), Phase: validationworkflow.PhaseAdmitted}
	if _, created, boundContext, err := store.WorkflowStore().ReserveBound(ctx, record, "functional-worker", now, time.Minute); err != nil || !created {
		t.Fatal("reserve workflow", err)
	} else {
		ctx = boundContext
	}
	limits := functionalflow.Limits{MaxDocumentBytes: 1 << 20, MaxSteps: 4, MaxAssertions: 4, MaxComparisons: 2, MaxReferences: 4, MaxDepth: 8, MaxResolvedBytes: 1 << 16, MaxToolCalls: 4, MaxRetries: 1, MaxWork: 16, MaxRevisions: 2, StepTimeout: time.Second, RunTimeout: time.Minute}
	operation := functionalflow.OperationReference{Capability: "http.request", TargetID: "target", Method: "GET", Path: "/health", ContractDigest: integrationDigest("contract")}
	assertion := planning.AssertionDeclaration{ID: "health-exists", Definition: map[string]any{"operator": "exists", "path": "/status"}}
	flow := functionalflow.Flow{Version: functionalflow.VersionV1, ID: "functional-flow", Source: functionalflow.SourceGenerated, Revision: 1, ContextDigest: integrationDigest("context"), Steps: []functionalflow.Step{{ID: "observe", Kind: functionalflow.StepObserve, ConstraintID: "constraint", DependsOn: []string{}, Operation: operation, IdentityID: "reader", Input: map[string]functionalflow.InputValue{}, Assertions: []planning.AssertionDeclaration{assertion}, Comparisons: []functionalflow.ObservationComparison{}, RequiredEvidence: []string{"health-exists"}}}, RequiredEvidence: []string{"health-exists"}}
	validated, err := functionalflow.Validate(flow, limits, functionalflow.ValidationAuthority{Constraints: map[string]bool{"constraint": true}})
	if err != nil {
		t.Fatal("validate flow", err)
	}
	binding := functionalflow.BindingAuthority{Operations: []functionalflow.OperationAuthority{{Reference: operation}}, Identities: []testidentity.Descriptor{{ID: "reader", Mechanism: testidentity.MechanismBearer, Operations: []testidentity.Operation{{Capability: operation.Capability, TargetID: operation.TargetID, Method: operation.Method, Path: operation.Path}}}}, Bindings: functionalflow.ArtifactBindings{Specification: integrationDigest("spec"), Revision: integrationDigest("revision"), Target: integrationDigest("target"), Contract: operation.ContractDigest, Identities: integrationDigest("identities"), Policy: integrationDigest("policy"), Fixtures: integrationDigest("fixtures"), Capabilities: integrationDigest("capabilities"), Limits: integrationDigest("limits")}}
	artifact, err := functionalflow.Bind(validated, binding)
	if err != nil {
		t.Fatal("bind flow", err)
	}
	journal, err := store.FunctionalStore(integrationClock{now: now}, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	artifacts, evidenceStore, publisher, catalog, attempts, err := store.FunctionalRuntimeStores(map[string]string{"health": "missing-until-put"}, limits, journal)
	if err != nil {
		t.Fatal(err)
	}
	provided := flow
	provided.Source = functionalflow.SourceProvided
	document, err := json.Marshal(provided)
	if err != nil {
		t.Fatal("marshal provided flow", err)
	}
	artifactID, err := catalog.(*PostgresFunctionalCatalog).PutProvidedFlow(ctx, document)
	if err != nil {
		t.Fatal("put provided flow", err)
	}
	if replayID, replayErr := catalog.(*PostgresFunctionalCatalog).PutProvidedFlow(ctx, document); replayErr != nil || replayID != artifactID {
		t.Fatal("replay provided flow", replayID, replayErr)
	}
	catalog.(*PostgresFunctionalCatalog).aliases["health"] = artifactID
	if resolved, resolveErr := catalog.Resolve(ctx, "health"); resolveErr != nil || resolved != artifactID {
		t.Fatal("resolve provided alias", resolved, resolveErr)
	}
	if loaded, loadErr := catalog.Load(ctx, validationworkflow.FunctionalFlowSelection{ArtifactID: artifactID}); loadErr != nil || len(loaded) == 0 {
		t.Fatal("load provided flow", loadErr)
	}
	if loaded, loadErr := catalog.Load(ctx, validationworkflow.FunctionalFlowSelection{Alias: "health"}); loadErr != nil || len(loaded) == 0 {
		t.Fatal("load provided alias", loadErr)
	}
	if _, resolveErr := catalog.Resolve(ctx, "missing"); !errors.Is(resolveErr, validationworkflow.ErrNotFound) {
		t.Fatal("missing provided flow accepted", resolveErr)
	}
	if err = artifacts.Commit(ctx, record.Handle, artifact); err != nil {
		t.Fatal("commit artifact", err)
	}
	if err = artifacts.Commit(ctx, record.Handle, artifact); err != nil {
		t.Fatal("replay artifact", err)
	}
	if _, loadErr := artifacts.Load(ctx, "missing-functional"); !errors.Is(loadErr, validationworkflow.ErrNotFound) {
		t.Fatal("missing artifact accepted", loadErr)
	}
	if _, err = journal.AppendEvent(ctx, functionalflow.Event{Type: "flow_artifact_committed", RunID: record.Handle, FlowID: artifact.FlowID, Revision: artifact.Revision, ArtifactDigest: artifact.ArtifactDigest}); err != nil {
		t.Fatal("artifact event", err)
	}
	if _, err = journal.AppendEvent(ctx, functionalflow.Event{Type: "fixtures_terminal", Status: string(fixture.PreparationRejected), RunID: record.Handle, FlowID: artifact.FlowID, Revision: artifact.Revision, ArtifactDigest: artifact.ArtifactDigest}); err != nil {
		t.Fatal("fixture event", err)
	}
	evidence := functionalflow.SealEvidence(functionalflow.Evidence{ID: "functional-evidence", RunID: record.Handle, Revision: artifact.Revision, StepID: "observe", BindingDigest: artifact.Steps[0].Operation.BindingDigest, ResourceBindingDigest: integrationDigest("resource"), Output: map[string]any{"status": float64(200)}, Terminal: true})
	if _, err = evidenceStore.Confirm(ctx, evidence); err != nil {
		t.Fatal("confirm evidence", err)
	}
	if replayed, replayErr := evidenceStore.Confirm(ctx, evidence); replayErr != nil || replayed.Digest != evidence.Digest {
		t.Fatal("replay evidence", replayErr)
	}
	conflictingEvidence := evidence
	conflictingEvidence.Output = map[string]any{"status": float64(500)}
	conflictingEvidence.Digest = ""
	conflictingEvidence = functionalflow.SealEvidence(conflictingEvidence)
	if _, confirmErr := evidenceStore.Confirm(ctx, conflictingEvidence); !errors.Is(confirmErr, controlplane.ErrConflict) {
		t.Fatal("conflicting evidence accepted", confirmErr)
	}
	if _, loadErr := evidenceStore.Load(ctx, "missing-evidence"); !errors.Is(loadErr, validationworkflow.ErrNotFound) {
		t.Fatal("missing evidence accepted", loadErr)
	}
	records, err := evidenceStore.(*PostgresFunctionalEvidenceStore).Records(ctx, record.Handle, functionalflow.Result{Ledger: []functionalflow.LedgerEntry{{EvidenceIDs: []string{evidence.ID, evidence.ID}}}})
	if err != nil || len(records) != 1 {
		t.Fatal("deduplicated evidence records", records, err)
	}
	if _, recordErr := evidenceStore.(*PostgresFunctionalEvidenceStore).Records(ctx, "other-run", functionalflow.Result{Ledger: []functionalflow.LedgerEntry{{EvidenceIDs: []string{evidence.ID}}}}); !errors.Is(recordErr, controlplane.ErrIntegrity) {
		t.Fatal("cross-run evidence accepted", recordErr)
	}
	if err = attempts.AppendIdentityAttempt(ctx, validationworkflow.FunctionalIdentityAttempt{RunID: record.Handle, IdentityID: "reader", OperationBindingSHA256: artifact.Steps[0].Operation.Identity.Digest, Attempt: 1, OutcomeCategory: "success"}); err != nil {
		t.Fatal("attempt audit", err)
	}
	primary := functionalflow.Result{FlowID: artifact.FlowID, Revision: artifact.Revision, Outcome: functionalflow.OutcomeInconclusive, ArtifactDigest: artifact.ArtifactDigest}
	if err = publisher.Stage(ctx, record.Handle, artifact, primary); err != nil {
		t.Fatal("stage primary", err)
	}
	if err = publisher.Stage(ctx, record.Handle, artifact, primary); err != nil {
		t.Fatal("replay primary", err)
	}
	conflictingPrimary := primary
	conflictingPrimary.Outcome = functionalflow.OutcomePassed
	if err = publisher.Stage(ctx, record.Handle, artifact, conflictingPrimary); !errors.Is(err, controlplane.ErrConflict) {
		t.Fatal("conflicting primary accepted", err)
	}
	restarted, err := Open(ctx, Config{DSN: store.pool.Config().ConnString(), BackendIdentity: "pg-test", MaxConnections: 4, StatementTimeout: 5 * time.Second, MaxLease: 5 * time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	defer restarted.Close()
	restartedJournal, _ := restarted.FunctionalStore(integrationClock{now: now}, time.Minute)
	restartedArtifacts, restartedEvidence, restartedPublisher, _, _, err := restarted.FunctionalRuntimeStores(nil, limits, restartedJournal)
	if err != nil {
		t.Fatal(err)
	}
	if loaded, loadErr := restartedArtifacts.Load(ctx, record.Handle); loadErr != nil || loaded.ArtifactDigest != artifact.ArtifactDigest {
		t.Fatal("reload artifact", loadErr)
	}
	if loaded, loadErr := restartedEvidence.Load(ctx, evidence.ID); loadErr != nil || loaded.Digest != evidence.Digest {
		t.Fatal("reload evidence", loadErr)
	}
	summary := fixture.FixtureSummary{PreparationStatus: fixture.PreparationRejected, Cleanup: fixture.CleanupReport{Status: fixture.CleanupNotRequired}}
	if _, finalizeErr := restartedPublisher.Finalize(ctx, record.Handle, validationworkflow.OutcomePassed, summary); !errors.Is(finalizeErr, controlplane.ErrConflict) {
		t.Fatal("mismatched final outcome accepted", finalizeErr)
	}
	projection, err := restartedPublisher.Finalize(ctx, record.Handle, validationworkflow.OutcomeInconclusive, summary)
	if err != nil || !strings.HasPrefix(projection.PackageDir, "postgres:") {
		t.Fatal("finalize postgres package", err)
	}
	if stored, loadErr := restarted.LoadPackage(ctx, "functional-project", record.Handle+"-functional"); loadErr != nil || len(stored.Document) == 0 {
		t.Fatal("reload canonical package", loadErr)
	}
	if _, err = restarted.pool.Exec(ctx, `UPDATE functional_artifacts SET digest='tampered' WHERE handle=$1`, record.Handle); err != nil {
		t.Fatal(err)
	}
	if _, loadErr := restartedArtifacts.Load(ctx, record.Handle); loadErr == nil {
		t.Fatal("tampered artifact accepted")
	}
	if _, err = restarted.pool.Exec(ctx, `UPDATE functional_evidence SET digest='tampered' WHERE evidence_id=$1`, evidence.ID); err != nil {
		t.Fatal(err)
	}
	if _, loadErr := restartedEvidence.Load(ctx, evidence.ID); !errors.Is(loadErr, controlplane.ErrIntegrity) {
		t.Fatal("tampered evidence accepted", loadErr)
	}
	if _, err = restarted.pool.Exec(ctx, `UPDATE functional_primaries SET digest='tampered' WHERE handle=$1`, record.Handle); err != nil {
		t.Fatal(err)
	}
	if _, loadErr := restartedPublisher.(*PostgresFunctionalPublisher).loadPrimary(ctx, record.Handle); !errors.Is(loadErr, controlplane.ErrIntegrity) {
		t.Fatal("tampered primary accepted", loadErr)
	}
}

// TestPostgresContextStoreEnforcesReceiptsAndBackendAuthority exercises the
// durable context boundary independently of workflow orchestration retries.
func TestPostgresContextStoreEnforcesReceiptsAndBackendAuthority(t *testing.T) {
	ctx := context.Background()
	store := integrationStore(t)
	if err := store.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Truncate(time.Microsecond)
	record := validationworkflow.Record{Version: 1, Handle: "context-store-run", Principal: "context-principal", IdempotencyKey: "context-workflow-key", RequestDigest: integrationDigest("context-request"), RepositoryID: "context-project", Revision: strings.Repeat("c", 40), Phase: validationworkflow.PhaseAdmitted}
	if _, created, boundContext, err := store.WorkflowStore().ReserveBound(ctx, record, "context-worker", now, time.Minute); err != nil || !created {
		t.Fatal("reserve workflow", created, err)
	} else {
		ctx = boundContext
	}
	adapter := store.ContextStore()
	request := contextassembly.Request{Version: "1", IdempotencyKey: "context-request-key", RunID: record.Handle, PlanID: "context-plan"}
	snapshot := contextassembly.Snapshot{Version: "1", RunID: record.Handle, PlanID: request.PlanID, Digest: integrationDigest("context-snapshot")}
	stored, created, err := adapter.CommitSnapshot(ctx, "snapshot-key", request, snapshot)
	if err != nil || !created || stored.Digest != snapshot.Digest {
		t.Fatal("commit snapshot", created, err)
	}
	if replayed, replayCreated, replayErr := adapter.CommitSnapshot(ctx, "snapshot-key", request, snapshot); replayErr != nil || replayCreated || replayed.Digest != snapshot.Digest {
		t.Fatal("snapshot replay", replayCreated, replayErr)
	}
	conflict := snapshot
	conflict.Digest = integrationDigest("other-snapshot")
	if _, _, conflictErr := adapter.CommitSnapshot(ctx, "snapshot-key", request, conflict); !errors.Is(conflictErr, controlplane.ErrConflict) {
		t.Fatal("snapshot conflict accepted", conflictErr)
	}
	if loaded, loadErr := adapter.LoadSnapshot(ctx, snapshot.Digest); loadErr != nil || loaded.RunID != record.Handle {
		t.Fatal("load snapshot", loadErr)
	}
	if _, loadErr := adapter.LoadSnapshot(ctx, integrationDigest("missing-snapshot")); !errors.Is(loadErr, controlplane.ErrNotFound) {
		t.Fatal("missing snapshot accepted", loadErr)
	}
	binding := contextassembly.ContextBinding{Version: "1", RunID: record.Handle, PlanID: request.PlanID, ContextDigest: snapshot.Digest, Digest: integrationDigest("context-binding")}
	bound, created, err := adapter.CommitBinding(ctx, "binding-key", binding)
	if err != nil || !created || bound.Digest != binding.Digest {
		t.Fatal("commit binding", created, err)
	}
	if replayed, replayCreated, replayErr := adapter.CommitBinding(ctx, "binding-key", binding); replayErr != nil || replayCreated || replayed.Digest != binding.Digest {
		t.Fatal("binding replay", replayCreated, replayErr)
	}
	conflictingBinding := binding
	conflictingBinding.Digest = integrationDigest("other-binding")
	if _, _, conflictErr := adapter.CommitBinding(ctx, "binding-key", conflictingBinding); !errors.Is(conflictErr, controlplane.ErrConflict) {
		t.Fatal("binding conflict accepted", conflictErr)
	}
	if loaded, loadErr := adapter.LoadBinding(ctx, binding.Digest); loadErr != nil || loaded.RunID != record.Handle {
		t.Fatal("load binding", loadErr)
	}
	if _, loadErr := adapter.LoadBinding(ctx, integrationDigest("missing-binding")); !errors.Is(loadErr, controlplane.ErrNotFound) {
		t.Fatal("missing binding accepted", loadErr)
	}
	missingSnapshot := snapshot
	missingSnapshot.RunID, missingSnapshot.Digest = "missing-context-run", integrationDigest("missing-context-object")
	if _, _, commitErr := adapter.CommitSnapshot(ctx, "missing-context-key", request, missingSnapshot); !errors.Is(commitErr, controlplane.ErrNotFound) {
		t.Fatal("context committed without workflow authority", commitErr)
	}
	if _, err = store.pool.Exec(ctx, `UPDATE context_snapshots SET document='{}' WHERE digest=$1`, snapshot.Digest); err != nil {
		t.Fatal(err)
	}
	if _, loadErr := adapter.LoadSnapshot(ctx, snapshot.Digest); !errors.Is(loadErr, controlplane.ErrIntegrity) {
		t.Fatal("tampered snapshot accepted", loadErr)
	}
	if _, err = store.pool.Exec(ctx, `UPDATE context_bindings SET document='{}' WHERE digest=$1`, binding.Digest); err != nil {
		t.Fatal(err)
	}
	if _, loadErr := adapter.LoadBinding(ctx, binding.Digest); !errors.Is(loadErr, controlplane.ErrIntegrity) {
		t.Fatal("tampered binding accepted", loadErr)
	}
	head, activeEpoch, err := store.Load(ctx, record.Handle)
	if err != nil {
		t.Fatal(err)
	}
	localEpoch := controlplane.BackendEpoch{RunID: record.Handle, ProjectID: head.ProjectID, Epoch: head.Epoch + 1, Mode: controlplane.BackendLocal, BackendIdentity: "archived-local", SchemaVersion: activeEpoch.SchemaVersion, CreatedAt: time.Now().UTC(), PreviousDigest: head.EpochDigest}
	if _, err = store.Cutover(ctx, head, localEpoch); err != nil {
		t.Fatal(err)
	}
	fencedBinding := binding
	fencedBinding.Digest = integrationDigest("fenced-binding")
	if _, _, commitErr := adapter.CommitBinding(ctx, "fenced-binding-key", fencedBinding); !errors.Is(commitErr, controlplane.ErrConflict) {
		t.Fatal("context committed after backend cutover", commitErr)
	}
}

// TestPostgresImportMaterializersRejectMalformedConsumerDocuments proves each
// typed stream fails closed before malformed frozen bytes reach consumer tables.
func TestPostgresImportMaterializersRejectMalformedConsumerDocuments(t *testing.T) {
	ctx := context.Background()
	store := integrationStore(t)
	if err := store.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	tx, err := store.pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	relation := func(path string) string { return "p" + hex.EncodeToString([]byte(path)) }
	cases := []controlplane.ImportDocument{
		{Stream: "workflow", RelationKey: relation("workflows/bad.state.json"), Document: []byte(`{}`)},
		{Stream: "artifacts", RelationKey: relation("workflows/bad.plan.json"), Document: []byte(`{}`)},
		{Stream: "executables", RelationKey: relation("workflows/bad.private.json"), Document: []byte(`{}`)},
		{Stream: "functional", RelationKey: relation("workflows/bad.flow.json"), Document: []byte(`{}`)},
		{Stream: "approval", RelationKey: relation("approval/bad.jsonl"), Document: []byte(`{}`)},
		{Stream: "approval", RelationKey: relation("approval/bad.request.1.json"), Document: []byte(`{}`)},
		{Stream: "environment", RelationKey: relation("environment/records/bad.json"), Document: []byte(`{}`)},
		{Stream: "environment", RelationKey: relation("environment/events/bad.json"), Document: []byte(`{}`)},
		{Stream: "environment", RelationKey: relation("environment/unknown"), Document: []byte(`{}`)},
		{Stream: "cleanup", RelationKey: "typed-cleanup", Document: []byte(`{}`)},
		{Stream: "capacity", RelationKey: "typed-capacity", Document: []byte(`{}`)},
		{Stream: "correction", RelationKey: "typed-correction", Document: []byte(`{}`)},
		{Stream: "github", RelationKey: "typed-github", Document: []byte(`{}`)},
		{Stream: "packages", RelationKey: "typed-packages", Document: []byte(`{}`)},
		{Stream: "unknown", RelationKey: "typed-unknown", Document: []byte(`{}`)},
	}
	for _, document := range cases {
		if materializeErr := materializeImportDocumentTx(ctx, tx, document, nil); !errors.Is(materializeErr, controlplane.ErrIntegrity) {
			t.Fatalf("materialize stream %s error=%v", document.Stream, materializeErr)
		}
		_ = verifyMaterializedImportDocumentTx(ctx, tx, document)
	}
	encode := func(value any) []byte {
		data, encodeErr := json.Marshal(value)
		if encodeErr != nil {
			t.Fatal(encodeErr)
		}
		return data
	}
	invalidTyped := []controlplane.ImportDocument{
		{Stream: "approval", RelationKey: relation("approval/empty.jsonl"), Document: []byte(" \n")},
		{Stream: "approval", RelationKey: relation("approval/pending.request.1.json"), Document: encode(approvalImportRequest{RunID: "pending-approval", Revision: 1})},
		{Stream: "functional", RelationKey: relation("workflows/invalid.flow.json"), Document: encode(functionalImportDocument{Version: 1, Intents: map[string]functionalflow.Intent{"bad": {}}, Terminals: map[string]functionalflow.StepRecord{}, Events: []functionalflow.Event{}})},
		{Stream: "functional", RelationKey: relation("workflows/invalid.flow.json"), Document: encode(functionalImportDocument{Version: 1, Intents: map[string]functionalflow.Intent{}, Terminals: map[string]functionalflow.StepRecord{"bad": {}}, Events: []functionalflow.Event{}})},
		{Stream: "functional", RelationKey: relation("workflows/invalid.flow.json"), Document: encode(functionalImportDocument{Version: 1, Intents: map[string]functionalflow.Intent{}, Terminals: map[string]functionalflow.StepRecord{}, Events: []functionalflow.Event{{}}})},
		{Stream: "cleanup", RelationKey: "typed-cleanup", Document: encode(cleanup.ImportSnapshot{Items: map[string]cleanup.Item{"wrong": {}}, Events: map[string][]cleanup.Event{}, Cursors: map[string]cleanup.ImportCursor{}})},
		{Stream: "cleanup", RelationKey: "typed-cleanup", Document: encode(cleanup.ImportSnapshot{Items: map[string]cleanup.Item{}, Events: map[string][]cleanup.Event{"cleanup": {{}}}, Cursors: map[string]cleanup.ImportCursor{}})},
		{Stream: "cleanup", RelationKey: "typed-cleanup", Document: encode(cleanup.ImportSnapshot{Items: map[string]cleanup.Item{}, Events: map[string][]cleanup.Event{}, Cursors: map[string]cleanup.ImportCursor{"source": {}}})},
		{Stream: "capacity", RelationKey: "typed-capacity", Document: encode(cleanup.ImportCapacitySnapshot{Receipts: map[string]cleanup.CapacityReceipt{"wrong": {}}, Events: map[string][]cleanup.CapacityEvent{}})},
		{Stream: "capacity", RelationKey: "typed-capacity", Document: encode(cleanup.ImportCapacitySnapshot{Receipts: map[string]cleanup.CapacityReceipt{}, Events: map[string][]cleanup.CapacityEvent{"project": {{}}}})},
		{Stream: "correction", RelationKey: "typed-correction", Document: encode(correction.ImportSnapshot{Chains: map[string][]correction.Event{"": {{}}}})},
		{Stream: "correction", RelationKey: "typed-correction", Document: encode(correction.ImportSnapshot{Chains: map[string][]correction.Event{"root": {}}})},
		{Stream: "correction", RelationKey: "typed-correction", Document: encode(correction.ImportSnapshot{Chains: map[string][]correction.Event{"root": {{Sequence: 2}}}})},
		{Stream: "github", RelationKey: "typed-github", Document: encode(githubpr.ImportSnapshot{Events: []githubpr.StoreEvent{{}}})},
	}
	for _, document := range invalidTyped {
		if materializeErr := materializeImportDocumentTx(ctx, tx, document, nil); materializeErr == nil {
			t.Fatalf("nested materialize stream %s error=%v", document.Stream, materializeErr)
		}
	}
	capacityReceipts := map[string]cleanup.CapacityReceipt{}
	for index, receipt := range []cleanup.CapacityReceipt{
		{ID: "capacity-reserved", ProjectID: "capacity-project", RunID: "capacity-run", AcquisitionKey: "reserved", Digest: integrationDigest("reserved")},
		{ID: "capacity-bound", ProjectID: "capacity-project", RunID: "capacity-run", AcquisitionKey: "bound", LifecycleBound: true, Digest: integrationDigest("bound")},
		{ID: "capacity-confirmed", ProjectID: "capacity-project", RunID: "capacity-run", AcquisitionKey: "confirmed", EnvironmentDigest: integrationDigest("environment"), Digest: integrationDigest("confirmed")},
		{ID: "capacity-released", ProjectID: "capacity-project", RunID: "capacity-run", AcquisitionKey: "released", Released: true, Digest: integrationDigest("released")},
	} {
		_ = index
		capacityReceipts[receipt.ID] = receipt
	}
	capacityDocument := controlplane.ImportDocument{Stream: "capacity", RelationKey: "typed-capacity", Document: encode(cleanup.ImportCapacitySnapshot{Receipts: capacityReceipts, Events: map[string][]cleanup.CapacityEvent{}})}
	if err = materializeImportDocumentTx(ctx, tx, capacityDocument, nil); err != nil {
		t.Fatal("capacity state materialization", err)
	}
	for _, rejectedMetadata := range []controlplane.ImportDocument{
		{Stream: "workflow", RelationKey: relation("workflows/head.json"), Document: []byte(`{}`)},
		{Stream: "artifacts", RelationKey: relation("workflows/head.json"), Document: []byte(`{}`)},
		{Stream: "executables", RelationKey: relation("workflows/head.json"), Document: []byte(`{}`)},
		{Stream: "environment", RelationKey: relation("environment/records/run/head.json"), Document: []byte(`{}`)},
	} {
		if err = materializeImportDocumentTx(ctx, tx, rejectedMetadata, nil); !errors.Is(err, controlplane.ErrIntegrity) {
			t.Fatal("unknown metadata relation accepted", rejectedMetadata.Stream, err)
		}
	}
	missingMaterialized := []controlplane.ImportDocument{
		{Stream: "functional", RelationKey: relation("workflows/missing.flow.json"), Document: encode(functionalImportDocument{Version: 1, Intents: map[string]functionalflow.Intent{"intent": {RunID: "missing-functional", Revision: 1, StepID: "intent"}}, Terminals: map[string]functionalflow.StepRecord{}, Events: []functionalflow.Event{}})},
		{Stream: "functional", RelationKey: relation("workflows/missing.flow.json"), Document: encode(functionalImportDocument{Version: 1, Intents: map[string]functionalflow.Intent{}, Terminals: map[string]functionalflow.StepRecord{"terminal": {RunID: "missing-functional", Revision: 1, StepID: "terminal"}}, Events: []functionalflow.Event{}})},
		{Stream: "functional", RelationKey: relation("workflows/missing.flow.json"), Document: encode(functionalImportDocument{Version: 1, Intents: map[string]functionalflow.Intent{}, Terminals: map[string]functionalflow.StepRecord{}, Events: []functionalflow.Event{{RunID: "missing-functional", Sequence: 1}}})},
		{Stream: "environment", RelationKey: relation("environment/records/missing.json"), Document: encode(environmentImportRecord{Digest: integrationDigest("missing-record"), Record: environment.Record{RunID: "missing-environment", Revision: 1}})},
		{Stream: "correction", RelationKey: "typed-correction", Document: encode(correction.ImportSnapshot{Chains: map[string][]correction.Event{"missing-root": {{Sequence: 1}}}})},
		{Stream: "github", RelationKey: "typed-github", Document: encode(githubpr.ImportSnapshot{Events: []githubpr.StoreEvent{{Sequence: 1}}})},
	}
	for _, document := range missingMaterialized {
		if verifyErr := verifyMaterializedImportDocumentTx(ctx, tx, document); !errors.Is(verifyErr, controlplane.ErrIntegrity) {
			t.Fatalf("missing materialized stream %s error=%v", document.Stream, verifyErr)
		}
	}
	opaque := controlplane.ImportDocument{Stream: "opaque", RelationKey: "caller-key", Document: []byte(`opaque`)}
	if err = materializeImportDocumentTx(ctx, tx, opaque, nil); !errors.Is(err, controlplane.ErrIntegrity) {
		t.Fatal("opaque staging accepted", err)
	}
	if err = verifyMaterializedImportDocumentTx(ctx, tx, opaque); !errors.Is(err, controlplane.ErrIntegrity) {
		t.Fatal("opaque verification accepted", err)
	}
	packageBytes := []byte(`{"proof":"detached"}`)
	packageSnapshot, err := json.Marshal(validationworkflow.PackageImportSnapshot{Items: []validationworkflow.PackageImportItem{{RunID: "import-package-run", PackageID: "import-package", MediaType: "application/json", SchemaVersion: "1", ArtifactDigest: integrationDigest("package-artifact"), Digest: digestBytes(packageBytes), Document: packageBytes}}})
	if err != nil {
		t.Fatal(err)
	}
	packageDocument := controlplane.ImportDocument{Stream: "packages", RelationKey: "typed-packages", Document: packageSnapshot}
	authority := &importMaterializationAuthority{ProjectID: "import-package-project", BindingDigest: integrationDigest("package-binding"), BackendIdentity: "pg-test", Epoch: 2, CreatedAt: time.Now().UTC()}
	if err = materializeImportDocumentTx(ctx, tx, packageDocument, authority); err != nil {
		t.Fatal("materialize package", err)
	}
	if err = verifyMaterializedImportDocumentTx(ctx, tx, packageDocument); err != nil {
		t.Fatal("verify package", err)
	}
	if _, err = tx.Exec(ctx, `UPDATE packages SET document='{}',digest=$1 WHERE package_id='import-package'`, integrationDigest("conflicting-package")); err != nil {
		t.Fatal(err)
	}
	if err = materializeImportDocumentTx(ctx, tx, packageDocument, authority); err != nil {
		t.Fatal("package conflict materialization", err)
	}
	if err = verifyMaterializedImportDocumentTx(ctx, tx, packageDocument); !errors.Is(err, controlplane.ErrIntegrity) {
		t.Fatal("package ON CONFLICT divergence verified", err)
	}
	receipt := githubpr.DeliveryReceipt{Version: githubpr.SchemaVersion, DeliveryID: "import-redelivery", Event: "issue_comment", PayloadSHA256: integrationDigest("github-payload"), State: "received", VersionCAS: 1}
	githubEvent := githubpr.StoreEvent{Sequence: 1, Kind: "delivery_reserved", At: time.Now().UTC(), Delivery: &receipt}
	githubDocument := controlplane.ImportDocument{Stream: "github", RelationKey: "typed-github", Document: encode(githubpr.ImportSnapshot{Events: []githubpr.StoreEvent{githubEvent}})}
	if _, err = tx.Exec(ctx, `INSERT INTO ingress_inbox(source,idempotency_key,request_digest,project_id,envelope,committed_at) VALUES('github',$1,$2,$3,'{}',$4)`, receipt.DeliveryID, integrationDigest("other-payload"), authority.ProjectID, githubEvent.At); err != nil {
		t.Fatal(err)
	}
	if err = materializeImportDocumentTx(ctx, tx, githubDocument, authority); err != nil {
		t.Fatal("github conflict materialization", err)
	}
	if err = verifyMaterializedImportDocumentTx(ctx, tx, githubDocument); !errors.Is(err, controlplane.ErrIntegrity) {
		t.Fatal("github receipt redelivery divergence verified", err)
	}
}

// TestPostgresImportUnknownRelationsNeverVerifyOrCutOver proves every supported
// stream rejects an opaque relation before materialization and remains inert.
func TestPostgresImportUnknownRelationsNeverVerifyOrCutOver(t *testing.T) {
	ctx := context.Background()
	now := time.Now().UTC()
	store := integrationStore(t)
	if err := store.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	relation := func(path string) string { return "p" + hex.EncodeToString([]byte(path)) }
	cases := []controlplane.ImportDocument{
		{Stream: "workflow", RelationKey: relation("workflows/run.unknown.json"), Document: []byte(`{}`)},
		{Stream: "artifacts", RelationKey: relation("workflows/plans/run.unknown.json"), Document: []byte(`{}`)},
		{Stream: "executables", RelationKey: relation("workflows/private/run.unknown.json"), Document: []byte(`{}`)},
		{Stream: "functional", RelationKey: relation("workflows/functional-flow/run.unknown.json"), Document: []byte(`{}`)},
		{Stream: "approval", RelationKey: relation("workflows/approval/run.request.x.json"), Document: []byte(`{}`)},
		{Stream: "environment", RelationKey: relation("environment/records/run/head.json"), Document: []byte(`{}`)},
		{Stream: "cleanup", RelationKey: "typed-cleanup-unknown", Document: []byte(`{}`)},
		{Stream: "capacity", RelationKey: "typed-capacity-unknown", Document: []byte(`{}`)},
		{Stream: "correction", RelationKey: "typed-correction-unknown", Document: []byte(`{}`)},
		{Stream: "github", RelationKey: "typed-github-unknown", Document: []byte(`{}`)},
		{Stream: "packages", RelationKey: "typed-packages-unknown", Document: []byte(`{}`)},
	}
	validPathRelations := []controlplane.ImportDocument{
		{Stream: "workflow", RelationKey: relation("workflows/run.state.json")},
		{Stream: "artifacts", RelationKey: relation("workflows/plans/run.plan.json")},
		{Stream: "executables", RelationKey: relation("workflows/private/run.private.json")},
		{Stream: "functional", RelationKey: relation("workflows/functional-flow/run.flow.json")},
		{Stream: "approval", RelationKey: relation("workflows/approval/run.jsonl")},
		{Stream: "environment", RelationKey: relation("environment/records/run/1.json")},
	}
	pathStreams := []string{"workflow", "artifacts", "executables", "functional", "approval", "environment"}
	for _, sourceRelation := range validPathRelations {
		for _, targetStream := range pathStreams {
			if targetStream == sourceRelation.Stream {
				continue
			}
			foreignRelation := sourceRelation
			foreignRelation.Stream = targetStream
			foreignRelation.Document = []byte(`{}`)
			cases = append(cases, foreignRelation)
		}
	}
	hostilePaths := []string{"secrets/run.state.json", "workflows/../run.state.json", `workflows\run.state.json`, "environment/records/run/0.json", "environment/records/run/01.json"}
	for _, hostilePath := range hostilePaths {
		for _, targetStream := range pathStreams {
			cases = append(cases, controlplane.ImportDocument{Stream: targetStream, RelationKey: relation(hostilePath), Document: []byte(`{}`)})
		}
	}
	for index, raw := range cases {
		runID := fmt.Sprintf("unknown-relation-%d", index)
		head := integrationReserveBackend(t, store, runID, "unknown-project", now)
		document, sealErr := controlplane.SealImportDocument(raw)
		if sealErr != nil {
			t.Fatal(sealErr)
		}
		manifest, sealErr := controlplane.SealImportManifest(controlplane.ImportManifest{
			ImportID:           fmt.Sprintf("unknown-import-%d", index),
			RunID:              runID,
			ProjectID:          "unknown-project",
			SourceIdentity:     "unknown-local",
			ExpectedHeadDigest: head.Digest,
			ExpectedEpoch:      head.Epoch,
			Counts:             map[string]uint64{raw.Stream: 1},
			StreamDigests:      map[string]string{raw.Stream: postgresImportDigest([]controlplane.ImportDocument{document}, false)},
			RelationDigests:    map[string]string{raw.Stream: postgresImportDigest([]controlplane.ImportDocument{document}, true)},
			State:              controlplane.ImportFrozen,
			CreatedAt:          now,
		})
		if sealErr != nil {
			t.Fatal(sealErr)
		}
		if _, err := store.SaveImport(ctx, manifest, ""); err != nil {
			t.Fatal(err)
		}
		if err := store.SaveImportPage(ctx, manifest.ImportID, []controlplane.ImportDocument{document}); err != nil {
			t.Fatal(err)
		}
		if _, err := store.VerifyImport(ctx, manifest.ImportID); !errors.Is(err, controlplane.ErrIntegrity) {
			t.Fatalf("%s unknown relation verified: %v", raw.Stream, err)
		}
		if _, err := store.CutoverImport(ctx, manifest.ImportID, now.Add(time.Second)); !errors.Is(err, controlplane.ErrConflict) {
			t.Fatalf("%s unknown relation cut over: %v", raw.Stream, err)
		}
	}
}

// TestPostgresNewAdaptersMapClosedPoolFailures verifies recently added
// persistence ports fail closed when the selected backend disappears.
func TestPostgresNewAdaptersMapClosedPoolFailures(t *testing.T) {
	ctx := context.Background()
	store := integrationStore(t)
	if err := store.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	contextStore := store.ContextStore()
	capacity := store.CleanupCapacityLedger()
	functionalJournal, err := store.FunctionalStore(integrationClock{now: time.Now().UTC()}, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	_, evidence, publisher, catalog, attempts, err := store.FunctionalRuntimeStores(map[string]string{"known": integrationDigest("catalog")}, functionalflow.Limits{MaxDocumentBytes: 1024}, functionalJournal)
	if err != nil {
		t.Fatal(err)
	}
	store.Close()
	snapshot := contextassembly.Snapshot{RunID: "closed-context-run", Digest: integrationDigest("closed-context")}
	if _, _, err = contextStore.CommitSnapshot(ctx, "closed-key", contextassembly.Request{RunID: snapshot.RunID}, snapshot); err == nil {
		t.Fatal("closed context snapshot commit succeeded")
	}
	if _, err = contextStore.LoadSnapshot(ctx, snapshot.Digest); err == nil {
		t.Fatal("closed context snapshot load succeeded")
	}
	binding := contextassembly.ContextBinding{RunID: snapshot.RunID, ContextDigest: snapshot.Digest, Digest: integrationDigest("closed-binding")}
	if _, _, err = contextStore.CommitBinding(ctx, "closed-binding-key", binding); err == nil {
		t.Fatal("closed context binding commit succeeded")
	}
	if _, err = contextStore.LoadBinding(ctx, binding.Digest); err == nil {
		t.Fatal("closed context binding load succeeded")
	}
	if _, err = catalog.Resolve(ctx, "known"); err == nil {
		t.Fatal("closed catalog resolution succeeded")
	}
	if _, err = catalog.Load(ctx, validationworkflow.FunctionalFlowSelection{ArtifactID: integrationDigest("catalog")}); err == nil {
		t.Fatal("closed catalog load succeeded")
	}
	if _, err = evidence.Load(ctx, "closed-evidence"); err == nil {
		t.Fatal("closed evidence load succeeded")
	}
	if err = attempts.AppendIdentityAttempt(ctx, validationworkflow.FunctionalIdentityAttempt{RunID: "closed-functional-run", IdentityID: "identity", OperationBindingSHA256: integrationDigest("operation"), Attempt: 1, OutcomeCategory: "success"}); err == nil {
		t.Fatal("closed attempt audit succeeded")
	}
	if _, err = publisher.Finalize(ctx, "closed-functional-run", validationworkflow.OutcomeError, fixture.FixtureSummary{PreparationStatus: fixture.PreparationFailed}); err == nil {
		t.Fatal("closed finalization succeeded")
	}
	if _, _, err = capacity.AdoptUnknown(ctx, "closed-project", "closed-run", "closed-acquisition", integrationDigest("authority"), cleanup.ResourceDemand{Environments: 1}, time.Minute); err == nil {
		t.Fatal("closed orphan adoption succeeded")
	}
	if _, _, err = capacity.BlockUnknown(ctx, "closed-project", integrationDigest("identity")); err == nil {
		t.Fatal("closed unknown block succeeded")
	}
	if _, _, err = capacity.ReleaseConfirmed(ctx, "closed-project", "closed-acquisition", integrationDigest("receipt"), "cleanup", integrationDigest("confirmation")); err == nil {
		t.Fatal("closed confirmed release succeeded")
	}
	if _, err = capacity.Snapshot("closed-project"); err == nil {
		t.Fatal("closed capacity snapshot succeeded")
	}
}

// integrationStore creates and owns one isolated schema and fails when DSN is absent.
func integrationStore(t *testing.T) *Store {
	t.Helper()
	dsn := os.Getenv("FEATUREPROOF_POSTGRES_TEST_DSN")
	if dsn == "" {
		t.Fatal("FEATUREPROOF_POSTGRES_TEST_DSN is required for the explicit PostgreSQL gate")
	}
	admin, err := Open(context.Background(), Config{DSN: dsn, BackendIdentity: "pg-test", MaxConnections: 8, StatementTimeout: 30 * time.Second, MaxLease: 5 * time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	schema := fmt.Sprintf("featureproof_%d", time.Now().UnixNano())
	if _, err = admin.pool.Exec(context.Background(), `CREATE SCHEMA `+pgx.Identifier{schema}.Sanitize()); err != nil {
		admin.Close()
		t.Fatal(err)
	}
	admin.Close()
	separator := "?"
	if strings.Contains(dsn, "?") {
		separator = "&"
	}
	scoped, err := Open(context.Background(), Config{DSN: dsn + separator + "search_path=" + schema, BackendIdentity: "pg-test", MaxConnections: 16, StatementTimeout: 30 * time.Second, MaxLease: 5 * time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		scoped.Close()
		cleanup, openErr := Open(context.Background(), Config{DSN: dsn, BackendIdentity: "pg-test", MaxConnections: 2, StatementTimeout: 30 * time.Second, MaxLease: 5 * time.Minute})
		if openErr != nil {
			t.Errorf("cleanup open: %v", openErr)
			return
		}
		defer cleanup.Close()
		if _, dropErr := cleanup.pool.Exec(context.Background(), `DROP SCHEMA `+pgx.Identifier{schema}.Sanitize()+` CASCADE`); dropErr != nil {
			t.Errorf("cleanup schema: %v", dropErr)
		}
	})
	return scoped
}

// integrationClock supplies deterministic time to real functional-flow adapters.
type integrationClock struct{ now time.Time }

// Now returns the test-owned database boundary instant.
func (clock integrationClock) Now() time.Time { return clock.now }

// integrationSecretResolver counts the private lookup performed after a real
// PostgreSQL lease and authority envelope have both been revalidated.
type integrationSecretResolver struct{ calls int }

// Resolve returns one synthetic value only after the PostgreSQL verifier authorizes the boundary.
func (resolver *integrationSecretResolver) Resolve(context.Context, []string) (map[string]string, error) {
	resolver.calls++
	return map[string]string{"secret-1": "ephemeral"}, nil
}

// integrationJob creates one valid PostgreSQL queue fixture.
func integrationJob(now time.Time, id string, epoch uint64, bindingDigest string) controlplane.Job {
	job, _ := controlplane.SealJob(controlplane.Job{JobID: id, ProjectID: "project-1", RunID: "run-1", BackendEpoch: epoch, BindingDigest: bindingDigest, Phase: controlplane.PhaseExecute, State: controlplane.JobQueued, InputDigest: integrationDigest("input"), CreatedAt: now, AvailableAt: now, Deadline: now.Add(time.Hour)})
	return job
}

// integrationAdmission constructs one complete causal ingress fixture for a fresh run.
func integrationAdmission(now time.Time, suffix string) controlplane.IngressAdmission {
	runID := "ingress-run-" + suffix
	epoch, _ := controlplane.SealBackendEpoch(controlplane.BackendEpoch{RunID: runID, ProjectID: "project-1", Epoch: 1, Mode: controlplane.BackendPostgres, BackendIdentity: "pg-test", SchemaVersion: 1, CreatedAt: now})
	job, _ := controlplane.SealJob(controlplane.Job{JobID: "ingress-job-" + suffix, ProjectID: "project-1", RunID: runID, BackendEpoch: 1, BindingDigest: epoch.Digest, Phase: controlplane.PhasePlan, State: controlplane.JobQueued, InputDigest: integrationDigest("input-" + suffix), CreatedAt: now, AvailableAt: now, Deadline: now.Add(time.Hour)})
	grant, _ := controlplane.SealAuthorityGrant(controlplane.AuthorityGrant{ProjectID: epoch.ProjectID, RunID: epoch.RunID, BackendEpoch: epoch.Epoch, BindingDigest: epoch.Digest, Phase: controlplane.PhasePlan, InputReferences: []string{"ingress"}, AllowedCapabilities: []string{"context.read"}})
	intent := controlplane.EffectIntent{OperationID: job.JobID + "-dispatch", IdempotencyKey: job.JobID, ProjectID: job.ProjectID, RunID: job.RunID, Phase: job.Phase, BindingDigest: job.BindingDigest, InputDigest: job.InputDigest, PayloadRef: "ingress:" + runID, PayloadDigest: job.InputDigest, ReconcileMethod: "reload_ingress"}
	return controlplane.IngressAdmission{Envelope: controlplane.InboxEnvelope{Source: "github", ExternalID: "delivery-" + suffix, IdempotencyKey: "delivery-" + suffix, ProjectID: "project-1", RequestDigest: integrationDigest("request-" + suffix), CommandKind: "validate"}, Epoch: epoch, Job: job, Grant: grant, Intent: intent}
}

// integrationReserveBackend creates the active PostgreSQL head required by
// every domain adapter mutation in integration contracts.
func integrationReserveBackend(t *testing.T, store *Store, runID, projectID string, now time.Time) controlplane.BindingHead {
	t.Helper()
	epoch, err := controlplane.SealBackendEpoch(controlplane.BackendEpoch{RunID: runID, ProjectID: projectID, Epoch: 1, Mode: controlplane.BackendPostgres, BackendIdentity: "pg-test", SchemaVersion: 1, CreatedAt: now})
	if err != nil {
		t.Fatal(err)
	}
	head, err := store.ReserveInitial(context.Background(), epoch)
	if err != nil {
		t.Fatal(err)
	}
	return head
}

// integrationBoundContext captures the same immutable active coordinate a
// productive read supplies before a consumer-owned adapter mutation.
func integrationBoundContext(t *testing.T, store *Store, runID string) context.Context {
	t.Helper()
	head, epoch, err := store.Load(context.Background(), runID)
	if err != nil {
		t.Fatal(err)
	}
	coordinate := controlplane.BackendCoordinate{RunID: runID, BackendIdentity: epoch.BackendIdentity, Epoch: head.Epoch, BindingDigest: epoch.Digest}
	return controlplane.WithBackendCoordinate(context.Background(), coordinate)
}

// TestPostgresGate validates migrations, epochs, queue concurrency, inbox and effects.
func TestPostgresGate(t *testing.T) {
	ctx := context.Background()
	store := integrationStore(t)
	if err := store.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	tx, err := store.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	cancelledVerification, cancelVerification := context.WithCancel(ctx)
	cancelVerification()
	if err = store.verifyActiveJobTx(cancelledVerification, tx, controlplane.Job{RunID: "cancelled-verification"}); !errors.Is(err, controlplane.ErrCancelled) {
		t.Fatalf("cancelled active-job verification returned %v", err)
	}
	if err := store.Ready(ctx); err != nil {
		t.Fatal("readiness", err)
	}
	if err := store.Migrate(ctx); err != nil {
		t.Fatal("rerun", err)
	}
	var migrateWait sync.WaitGroup
	for index := 0; index < 2; index++ {
		migrateWait.Add(1)
		go func() {
			defer migrateWait.Done()
			if migrateErr := store.Migrate(ctx); migrateErr != nil {
				t.Error(migrateErr)
			}
		}()
	}
	migrateWait.Wait()
	now := time.Now().UTC()
	integrationReserveBackend(t, store, "approval-run", "project-1", now)
	approvalAdapter, approvalErr := store.ApprovalStore()
	if approvalErr != nil {
		t.Fatal("approval adapter", approvalErr)
	}
	approvalRun, approvalErr := harness.NewRun("approval-run", "verify approval persistence", "constraint-1", harness.Limits{MaxSteps: 1, MaxToolCalls: 1, MaxRetries: 1})
	if approvalErr != nil {
		t.Fatal("approval run", approvalErr)
	}
	if _, approvalErr = approvalAdapter.BindRun(integrationBoundContext(t, store, approvalRun.ID), approvalRun); approvalErr != nil {
		t.Fatal("approval bind", approvalErr)
	}
	if approvalEvents, eventsErr := approvalAdapter.Events(ctx, approvalRun.ID); eventsErr != nil || len(approvalEvents) != 0 {
		t.Fatal("approval events", eventsErr)
	}
	epoch, _ := controlplane.SealBackendEpoch(controlplane.BackendEpoch{RunID: "run-1", ProjectID: "project-1", Epoch: 1, Mode: controlplane.BackendPostgres, BackendIdentity: "pg-test", SchemaVersion: 1, CreatedAt: now})
	head, err := store.ReserveInitial(ctx, epoch)
	if err != nil {
		t.Fatal(err)
	}
	if sameHead, reserveErr := store.ReserveInitial(ctx, epoch); reserveErr != nil || sameHead.Digest != head.Digest {
		t.Fatal("binding idempotency", reserveErr)
	}
	conflictingEpoch := epoch
	conflictingEpoch.BackendIdentity = "pg-other"
	conflictingEpoch.Digest = ""
	if _, reserveErr := store.ReserveInitial(ctx, conflictingEpoch); !errors.Is(reserveErr, controlplane.ErrConflict) {
		t.Fatal("binding conflict")
	}
	if _, _, loadErr := store.Load(ctx, "missing"); !errors.Is(loadErr, controlplane.ErrNotFound) {
		t.Fatal("missing binding")
	}
	loaded, _, err := store.Load(ctx, epoch.RunID)
	if err != nil || loaded.Digest != head.Digest {
		t.Fatal("binding reload")
	}
	next := controlplane.BackendEpoch{RunID: epoch.RunID, ProjectID: epoch.ProjectID, Epoch: 2, Mode: controlplane.BackendPostgres, BackendIdentity: "pg-test", SchemaVersion: epoch.SchemaVersion, CreatedAt: now.Add(time.Second), PreviousDigest: epoch.Digest}
	cutoverHead, cutoverErr := store.Cutover(ctx, head, next)
	if cutoverErr != nil {
		t.Fatal(cutoverErr)
	}
	if _, cutoverErr = store.Cutover(ctx, head, next); !errors.Is(cutoverErr, controlplane.ErrConflict) {
		t.Fatal("stale cutover")
	}
	_ = cutoverHead
	for index := 0; index < 24; index++ {
		if _, err = store.Enqueue(ctx, integrationJob(now, fmt.Sprintf("job-%02d", index), cutoverHead.Epoch, cutoverHead.EpochDigest)); err != nil {
			t.Fatal(err)
		}
	}
	duplicateJob := integrationJob(now, "job-00", cutoverHead.Epoch, cutoverHead.EpochDigest)
	if _, err = store.Enqueue(ctx, duplicateJob); err != nil {
		t.Fatal("job idempotency")
	}
	conflictJob := duplicateJob
	conflictJob.Deadline = conflictJob.Deadline.Add(time.Minute)
	conflictJob.Digest = ""
	conflictJob, _ = controlplane.SealJob(conflictJob)
	if _, err = store.Enqueue(ctx, conflictJob); !errors.Is(err, controlplane.ErrConflict) {
		t.Fatal("job conflict")
	}
	var wait sync.WaitGroup
	claimed := make(chan controlplane.Job, 24)
	for worker := 0; worker < 4; worker++ {
		wait.Add(1)
		go func(index int) {
			defer wait.Done()
			jobs, claimErr := store.Claim(ctx, controlplane.ClaimRequest{ProjectID: "project-1", WorkerID: fmt.Sprintf("worker-%d", index), Now: now, Lease: time.Minute, Limit: 6})
			if claimErr != nil {
				t.Error(claimErr)
				return
			}
			for _, job := range jobs {
				claimed <- job
			}
		}(worker)
	}
	wait.Wait()
	close(claimed)
	seen := map[string]bool{}
	for job := range claimed {
		if seen[job.JobID] {
			t.Fatalf("double claim %s", job.JobID)
		}
		seen[job.JobID] = true
		if job.JobID == "job-00" {
			if err = store.SaveRetentionReference(ctx, job, "package", "package-00", true, job.Owner, now); err != nil {
				t.Fatal("save live retention reference", err)
			}
			if err = store.SaveRetentionReference(ctx, job, "package", "package-00", false, job.Owner, now); err != nil {
				t.Fatal("retire retention reference", err)
			}
			if err = store.SaveRetentionReference(ctx, job, "package", "missing-reference", false, job.Owner, now); !errors.Is(err, controlplane.ErrConflict) {
				t.Fatal("missing retention reference retired", err)
			}
			if err = store.SaveRetentionReference(ctx, job, "", "invalid", true, job.Owner, now); !errors.Is(err, controlplane.ErrInvalid) {
				t.Fatal("invalid retention reference accepted", err)
			}
		}
		if _, err = store.Complete(ctx, job.JobID, job.Owner, job.Fence, job.CancelRevision, now, controlplane.JobSucceeded, ""); err != nil {
			t.Fatal(err)
		}
	}
	if len(seen) != 24 {
		t.Fatalf("claimed %d", len(seen))
	}
	retryJob := integrationJob(now, "retry-job", cutoverHead.Epoch, cutoverHead.EpochDigest)
	if _, err = store.Enqueue(ctx, retryJob); err != nil {
		t.Fatal(err)
	}
	retryClaim, _ := store.Claim(ctx, controlplane.ClaimRequest{ProjectID: "project-1", WorkerID: "retry-worker", Now: now, Lease: time.Minute, Limit: 1})
	if len(retryClaim) != 1 {
		t.Fatal("retry claim")
	}
	leased := retryClaim[0]
	leased, err = store.Heartbeat(ctx, leased.JobID, leased.Owner, leased.Fence, leased.CancelRevision, now, 2*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = store.Retry(ctx, leased.JobID, leased.Owner, leased.Fence, leased.CancelRevision, now, controlplane.RetryDecision{Category: "transient", Backoff: time.Minute, MaxAttempts: 3}, now.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	if _, err = store.Heartbeat(ctx, leased.JobID, "stale-worker", leased.Fence, leased.CancelRevision, now, time.Minute); !errors.Is(err, controlplane.ErrConflict) {
		t.Fatal("stale heartbeat")
	}
	if _, err = store.Complete(ctx, leased.JobID, leased.Owner, leased.Fence, leased.CancelRevision, now, controlplane.JobQueued, ""); !errors.Is(err, controlplane.ErrInvalid) {
		t.Fatal("invalid complete")
	}
	retryClaim, _ = store.Claim(ctx, controlplane.ClaimRequest{ProjectID: "project-1", WorkerID: "retry-worker-2", Now: now.Add(time.Minute), Lease: time.Minute, Limit: 1})
	if len(retryClaim) != 1 || retryClaim[0].Fence <= leased.Fence {
		t.Fatal("retry fence")
	}
	authority, authorityErr := controlplane.BuildAuthority(retryClaim[0], controlplane.AuthorityRequest{EnvelopeID: "envelope-1", WorkerID: retryClaim[0].Owner, ExpiresAt: retryClaim[0].LeaseUntil.Add(-time.Second), TargetBindingRef: "target-1", TargetBindingDigest: integrationDigest("target")}, now.Add(time.Minute))
	if authorityErr != nil {
		t.Fatal(authorityErr)
	}
	if err = store.SaveAuthority(ctx, authority, retryClaim[0], retryClaim[0].Owner, now.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	cancelJob := integrationJob(now, "cancel-job", cutoverHead.Epoch, cutoverHead.EpochDigest)
	store.Enqueue(ctx, cancelJob)
	if cancelled, cancelErr := store.Cancel(ctx, cancelJob.JobID, 1); cancelErr != nil || cancelled.State != controlplane.JobCancelled {
		t.Fatal("cancel", cancelErr)
	}
	expiredJob := integrationJob(now, "expired-job", cutoverHead.Epoch, cutoverHead.EpochDigest)
	store.Enqueue(ctx, expiredJob)
	expiredClaim, _ := store.Claim(ctx, controlplane.ClaimRequest{ProjectID: "project-1", WorkerID: "expired-worker", Now: now, Lease: time.Second, Limit: 1})
	if len(expiredClaim) != 1 {
		t.Fatal("expired claim")
	}
	if recovered, recoverErr := store.Recover(ctx, now.Add(time.Second), 10); recoverErr != nil || recovered < 1 {
		t.Fatal("recover", recoverErr)
	}
	inboxJob := integrationJob(now, "inbox-job", cutoverHead.Epoch, cutoverHead.EpochDigest)
	envelope := controlplane.InboxEnvelope{Source: "api", ExternalID: "request-1", IdempotencyKey: "key-1", ProjectID: "project-1", RequestDigest: integrationDigest("request"), CommandKind: "validate"}
	created, err := store.Accept(ctx, envelope, inboxJob)
	if err != nil || !created {
		t.Fatal("accept", err)
	}
	conflictEnvelope := envelope
	conflictEnvelope.RequestDigest = integrationDigest("different")
	if _, acceptErr := store.Accept(ctx, conflictEnvelope, inboxJob); !errors.Is(acceptErr, controlplane.ErrConflict) {
		t.Fatal("inbox conflict")
	}
	created, err = store.Accept(ctx, envelope, inboxJob)
	if err != nil || created {
		t.Fatal("redelivery", err)
	}
	intent := controlplane.EffectIntent{OperationID: "operation-1", IdempotencyKey: "effect-1", ProjectID: "project-1", RunID: "run-1", Phase: controlplane.PhasePublish, BindingDigest: cutoverHead.EpochDigest, InputDigest: integrationDigest("input"), ReconcileMethod: "poll"}
	if _, err = store.Prepare(ctx, intent); err != nil {
		t.Fatal(err)
	}
	if _, err = store.Prepare(ctx, intent); err != nil {
		t.Fatal("intent idempotency")
	}
	conflictIntent := intent
	conflictIntent.InputDigest = integrationDigest("different")
	if _, err = store.Prepare(ctx, conflictIntent); !errors.Is(err, controlplane.ErrConflict) {
		t.Fatal("intent conflict")
	}
	if err = store.Confirm(ctx, intent.OperationID, integrationDigest("receipt")); err != nil {
		t.Fatal(err)
	}
	if err = store.Confirm(ctx, intent.OperationID, integrationDigest("other")); !errors.Is(err, controlplane.ErrConflict) {
		t.Fatal("receipt conflict")
	}
	outboxIntent := intent
	outboxIntent.OperationID = "operation-2"
	outboxIntent.IdempotencyKey = "effect-2"
	if _, err = store.Prepare(ctx, outboxIntent); err != nil {
		t.Fatal(err)
	}
	dispatchNow := time.Now().UTC()
	outboxClaims, err := store.ClaimOutbox(ctx, "dispatcher-1", dispatchNow, time.Minute, 1)
	if err != nil || len(outboxClaims) != 1 {
		t.Fatal("outbox claim", err)
	}
	if err = store.HeartbeatOutbox(ctx, outboxClaims[0], outboxClaims[0].LeaseUntil.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	if err = store.RetryOutbox(ctx, outboxClaims[0], dispatchNow.Add(time.Minute), "transient"); err != nil {
		t.Fatal(err)
	}
	outboxClaims, err = store.ClaimOutbox(ctx, "dispatcher-2", dispatchNow.Add(time.Minute), time.Minute, 1)
	if err != nil || len(outboxClaims) != 1 {
		t.Fatal("outbox retry", err)
	}
	if err = store.ConfirmOutbox(ctx, outboxClaims[0], integrationDigest("receipt-2")); err != nil {
		t.Fatal(err)
	}
	if err = store.RetryOutbox(ctx, outboxClaims[0], dispatchNow.Add(time.Minute), "stale"); !errors.Is(err, controlplane.ErrConflict) {
		t.Fatal("stale outbox retry")
	}
	if err = store.ConfirmOutbox(ctx, outboxClaims[0], integrationDigest("receipt-2")); !errors.Is(err, controlplane.ErrConflict) {
		t.Fatal("stale outbox confirm")
	}
	if err = store.HeartbeatOutbox(ctx, outboxClaims[0], outboxClaims[0].LeaseUntil.Add(time.Minute)); err == nil {
		t.Fatal("stale outbox fence accepted")
	}
	expiringIntent := intent
	expiringIntent.OperationID = "operation-3"
	expiringIntent.IdempotencyKey = "effect-3"
	store.Prepare(ctx, expiringIntent)
	expiringClaims, _ := store.ClaimOutbox(ctx, "dispatcher-3", time.Now().UTC(), time.Second, 1)
	if len(expiringClaims) != 1 {
		t.Fatal("expiring outbox")
	}
	if recovered, recoverErr := store.RecoverOutbox(ctx, expiringClaims[0].LeaseUntil, 10); recoverErr != nil || recovered != 1 {
		t.Fatal("outbox recovery", recoverErr)
	}
	packageValue := validationworkflow.PackageDocument{ProjectID: "project-1", RunID: "run-1", PackageID: "package-1", MediaType: "application/json", SchemaVersion: "featureproof.package/v1", ArtifactDigest: integrationDigest("artifact"), BindingDigest: cutoverHead.EpochDigest, BackendEpoch: cutoverHead.Epoch, BackendIdentity: "pg-test", Document: []byte(`{"proof":true}`), CreatedAt: now}
	storedPackage, err := store.CommitPackage(ctx, packageValue)
	if err != nil {
		t.Fatal(err)
	}
	loadedPackage, err := store.LoadPackage(ctx, packageValue.ProjectID, packageValue.PackageID)
	if err != nil || loadedPackage.Digest != storedPackage.Digest {
		t.Fatal("package", err)
	}
	if _, err = store.CommitPackage(ctx, packageValue); err != nil {
		t.Fatal("package idempotency")
	}
	conflictingPackage := packageValue
	conflictingPackage.Document = []byte(`{"proof":false}`)
	conflictingPackage.Digest = ""
	if _, err = store.CommitPackage(ctx, conflictingPackage); err == nil {
		t.Fatal("package conflict accepted")
	}
	if _, err = store.LoadPackage(ctx, "project-1", "missing"); err == nil {
		t.Fatal("missing package found")
	}
	workflowAdapter := store.WorkflowStore()
	workflow := validationworkflow.Record{Version: 1, Handle: "workflow-1", Principal: "principal-1", IdempotencyKey: "workflow-key", RequestDigest: integrationDigest("workflow-request"), RepositoryID: "repository-1", Revision: strings.Repeat("a", 40), Phase: validationworkflow.PhaseAdmitted}
	workflow, created, workflowCtx, err := workflowAdapter.ReserveBound(ctx, workflow, "workflow-worker", now, time.Minute)
	if err != nil || !created {
		t.Fatal("workflow reserve", err)
	}
	workflow.Phase = validationworkflow.PhaseGenerating
	workflow, err = workflowAdapter.SaveOwned(workflowCtx, workflow, "workflow-worker", workflow.VersionCAS, now, time.Minute, validationworkflow.Event{Type: "generation_started"})
	if err != nil {
		t.Fatal("workflow save", err)
	}
	workflowEvents, err := workflowAdapter.Events(ctx, workflow.Handle)
	if err != nil || len(workflowEvents) != 2 {
		t.Fatal("workflow events", err)
	}
	if _, err = workflowAdapter.ReleaseOwner(workflowCtx, workflow.Handle, "workflow-worker", workflow.VersionCAS, now); err != nil {
		t.Fatal("workflow release", err)
	}
	faultCases := []struct {
		name      string
		effect    bool
		committed bool
	}{{"workflow.after_projection", false, false}, {"workflow.after_event", false, false}, {"workflow.after_job", false, false}, {"workflow.after_outbox", true, false}, {"workflow.after_commit", false, true}}
	for index, faultCase := range faultCases {
		faultRecord := validationworkflow.Record{Version: 1, Handle: fmt.Sprintf("fault-workflow-%d", index), Principal: "principal-1", IdempotencyKey: fmt.Sprintf("fault-key-%d", index), RequestDigest: integrationDigest(faultCase.name), RepositoryID: "repository-1", Revision: strings.Repeat("b", 40), Phase: validationworkflow.PhaseAdmitted}
		var faultCtx context.Context
		faultRecord, _, faultCtx, err = workflowAdapter.ReserveBound(ctx, faultRecord, "fault-worker", now, time.Minute)
		if err != nil {
			t.Fatal(err)
		}
		_, faultEpoch, loadErr := store.Load(ctx, faultRecord.Handle)
		if loadErr != nil {
			t.Fatal(loadErr)
		}
		faultRecord.Phase = validationworkflow.PhaseGenerating
		faultJob := integrationJob(now, fmt.Sprintf("fault-job-%d", index), 1, faultEpoch.Digest)
		faultJob.RunID, faultJob.ProjectID = faultRecord.Handle, faultEpoch.ProjectID
		faultJob.Digest = ""
		faultJob, _ = controlplane.SealJob(faultJob)
		var causalJob *controlplane.Job
		var causalEffect *controlplane.EffectIntent
		if faultCase.effect {
			causalEffect = &controlplane.EffectIntent{OperationID: fmt.Sprintf("fault-effect-%d", index), IdempotencyKey: fmt.Sprintf("fault-effect-key-%d", index), ProjectID: faultEpoch.ProjectID, RunID: faultRecord.Handle, Phase: controlplane.PhasePublish, BindingDigest: faultEpoch.Digest, InputDigest: integrationDigest("input"), ReconcileMethod: "poll"}
		} else {
			causalJob = &faultJob
		}
		store.fault = func(boundary string) error {
			if boundary == faultCase.name {
				return errors.New("injected")
			}
			return nil
		}
		_, saveErr := workflowAdapter.SaveOwnedCausal(faultCtx, faultRecord, "fault-worker", faultRecord.VersionCAS, now, time.Minute, validationworkflow.Event{Type: "generation_started"}, causalJob, causalEffect)
		store.fault = nil
		if saveErr == nil {
			t.Fatalf("fault %s did not fire", faultCase.name)
		}
		reloaded, reloadErr := workflowAdapter.Load(ctx, faultRecord.Handle)
		if reloadErr != nil {
			t.Fatal(reloadErr)
		}
		if faultCase.committed && reloaded.Phase != validationworkflow.PhaseGenerating {
			t.Fatal("unknown commit was not discoverable")
		}
		if !faultCase.committed && reloaded.Phase != validationworkflow.PhaseAdmitted {
			t.Fatal("partial workflow transaction committed")
		}
	}
	functionalAdapter, err := store.FunctionalStore(integrationClock{now: now}, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	integrationReserveBackend(t, store, "flow-run", "project-1", now)
	flowCtx := integrationBoundContext(t, store, "flow-run")
	claim, functionalClaimed, err := functionalAdapter.Claim(flowCtx, "flow-run", 1, "step-1", "flow-worker")
	if err != nil || !functionalClaimed {
		t.Fatal("functional claim", err)
	}
	intentValue := functionalflow.Intent{RunID: "flow-run", Revision: 1, StepID: "step-1", BindingDigest: integrationDigest("binding"), KeyDigest: integrationDigest("key"), ClaimToken: claim.Token}
	if err = functionalAdapter.ConfirmIntent(flowCtx, intentValue); err != nil {
		t.Fatal("functional intent", err)
	}
	terminal := functionalflow.StepRecord{RunID: "flow-run", Revision: 1, StepID: "step-1", ClaimToken: claim.Token}
	if err = functionalAdapter.ConfirmTerminal(flowCtx, terminal); err != nil {
		t.Fatal("functional terminal", err)
	}
	if _, found, terminalErr := functionalAdapter.Terminal(ctx, "flow-run", 1, "step-1"); terminalErr != nil || !found {
		t.Fatal("functional reload", terminalErr)
	}
	integrationReserveBackend(t, store, "environment-run", "project-1", now)
	environmentCtx := integrationBoundContext(t, store, "environment-run")
	environmentAdapter := store.EnvironmentStore()
	selection, _ := environment.SealSelection(environment.Selection{Version: 1, Alias: "test-env", ProviderID: "provider", ProviderVersion: "v1", Kind: environment.KindManaged, TemplateReference: "template-v1", ProfileID: "profile-1", NetworkPolicyID: "network-1", Secretless: true, ProjectID: "project-1", RepositoryID: "repository-1", Capabilities: []string{"http"}})
	environmentRecord := environment.Record{Version: 1, RunID: "environment-run", ProjectID: "project-1", RequestDigest: integrationDigest("request"), AcquisitionKey: integrationDigest("acquisition-1"), CommitOID: strings.Repeat("a", 40), Selection: selection, State: environment.StateSelected, Revision: 1, CreatedAt: now, TTL: time.Hour, ExpiresAt: now.Add(time.Hour), Demand: environment.ResourceDemand{Environments: 1, CPUMilli: 100, MemoryBytes: 1024, ObjectCount: 1}, CapacityReceipt: environment.CapacityReceipt{ID: "capacity-receipt", Digest: integrationDigest("capacity")}}
	environmentRecord, environmentCreated, environmentErr := environmentAdapter.Reserve(environmentCtx, environmentRecord)
	if environmentErr != nil || !environmentCreated {
		t.Fatal("environment reserve", environmentErr)
	}
	environmentRecord.State = environment.StateAcquiring
	environmentRecord.ReleaseRequired = true
	if _, environmentErr = environmentAdapter.Save(environmentCtx, environmentRecord, environmentRecord.Revision, integrationEnvironmentEvent(environmentRecord, "environment-event", now)); environmentErr != nil {
		t.Fatal("environment save", environmentErr)
	}
	capacityAdapter, capacityErr := store.CapacityAdmission(map[string]environment.CapacityLimits{"project-1": {Environments: 2, CPUMilli: 2000, MemoryBytes: 1 << 30, ObjectCount: 100, MaxTTL: time.Hour}})
	if capacityErr != nil {
		t.Fatal(capacityErr)
	}
	capacityReceipt, capacityErr := capacityAdapter.Reserve(environmentCtx, environment.CapacityRequest{ProjectID: "project-1", RunID: "environment-run", AcquisitionKey: "acquisition-1", SelectionDigest: integrationDigest("selection"), Demand: environment.ResourceDemand{Environments: 1, CPUMilli: 100, MemoryBytes: 1024, ObjectCount: 1}, TTL: time.Minute, PolicyDigest: integrationDigest("policy")})
	if capacityErr != nil {
		t.Fatal("capacity reserve", capacityErr)
	}
	capacityReceipt, capacityErr = capacityAdapter.Confirm(environmentCtx, capacityReceipt, integrationDigest("environment"), integrationDigest("resource"))
	if capacityErr != nil {
		t.Fatal("capacity confirm", capacityErr)
	}
	if capacityErr = capacityAdapter.Release(environmentCtx, capacityReceipt, "cleanup-capacity", integrationDigest("wrong-confirmation")); capacityErr == nil {
		t.Fatal("capacity accepted an unrelated cleanup confirmation")
	}
	releaseConfirmation := environment.CapacityReleaseConfirmation("cleanup-capacity", integrationDigest("environment"), integrationDigest("resource"), capacityReceipt.ID)
	if capacityErr = capacityAdapter.Release(environmentCtx, capacityReceipt, "cleanup-capacity", releaseConfirmation); capacityErr != nil {
		t.Fatal("capacity release", capacityErr)
	}
	if capacityErr = capacityAdapter.Release(environmentCtx, capacityReceipt, "cleanup-capacity", releaseConfirmation); capacityErr != nil {
		t.Fatal("capacity release redelivery", capacityErr)
	}
	binding, _ := cleanup.SealLifecycleCleanupBinding(cleanup.LifecycleCleanupBinding{Version: cleanup.BindingVersion, ProjectID: "project-1", RunID: "cleanup-run", ProviderID: "provider-1", ProviderVersion: "v1", SelectionDigest: integrationDigest("selection"), AcquisitionKey: "acquisition-2", AcquisitionDigest: integrationDigest("acquisition"), EnvironmentDigest: integrationDigest("environment"), ResourceIdentityDigest: integrationDigest("resource"), LifecycleDigest: integrationDigest("lifecycle"), CleanupKey: "cleanup-key", CapacityReceiptDigest: integrationDigest("capacity"), LifecycleRecordDigest: integrationDigest("record")})
	cleanupItem, _ := cleanup.SealItem(cleanup.Item{Version: cleanup.Version, CleanupID: "cleanup-1", Source: "lifecycle", OwnerDigest: integrationDigest("owner"), ProjectID: "project-1", RunID: "cleanup-run", Binding: binding, CreatedAt: now, ExpiresAt: now.Add(time.Hour), TTL: time.Hour, Demand: cleanup.ResourceDemand{Environments: 1, CPUMilli: 100, MemoryBytes: 1024, ObjectCount: 1}, State: cleanup.StatePending, VersionNumber: 1})
	integrationReserveBackend(t, store, "cleanup-run", "project-1", now)
	cleanupCtx := integrationBoundContext(t, store, "cleanup-run")
	cleanupAdapter := store.CleanupStore()
	cleanupItem, cleanupCreated, cleanupErr := cleanupAdapter.Reserve(cleanupCtx, cleanupItem, cleanup.Event{ID: "cleanup-event-1", CleanupID: cleanupItem.CleanupID, ProjectID: cleanupItem.ProjectID, Kind: "cleanup_reserved", At: now})
	if cleanupErr != nil || !cleanupCreated {
		t.Fatal("cleanup reserve", cleanupErr)
	}
	cleanupItem, cleanupClaimed, cleanupErr := cleanupAdapter.Claim(cleanupCtx, cleanupItem.CleanupID, "cleanup-worker", now, time.Minute)
	if cleanupErr != nil || !cleanupClaimed {
		t.Fatal("cleanup claim", cleanupErr)
	}
	if _, cleanupErr = cleanupAdapter.ReleaseOwner(cleanupCtx, cleanupItem, "cleanup-worker", cleanupItem.FencingVersion, cleanup.Event{ID: "cleanup-event-2", CleanupID: cleanupItem.CleanupID, ProjectID: cleanupItem.ProjectID, Kind: "cleanup_released_owner", At: now}); cleanupErr != nil {
		t.Fatal("cleanup release", cleanupErr)
	}
	link, linkErr := (correction.CorrectionLink{Version: correction.Version, CorrectionID: "correction-1", Handle: "correction-1", PrincipalID: "project-1", ParentRunID: "parent-run", ParentBindingSHA256: integrationDigest("parent"), RootRunID: "parent-run", Attempt: 1, SelectionAlias: "selection-1", SelectionSHA256: integrationDigest("selection"), PolicySHA256: integrationDigest("policy"), LimitsSHA256: integrationDigest("limits"), CreatedAt: now, UpdatedAt: now}).Seal()
	if linkErr != nil {
		t.Fatal(linkErr)
	}
	limits := correction.ChainLimits{MaxAttempts: 3, MaxDuration: time.Hour, MaxModelCalls: 3, MaxModelTokens: 1000}
	correctionRecord := correction.Record{Link: link, RequestHash: integrationDigest("correction-request"), KeyHash: integrationDigest("correction-key"), Phase: correction.PhaseReserved, Owner: "correction-worker"}
	integrationReserveBackend(t, store, "parent-run", "project-1", now)
	correctionCtx := integrationBoundContext(t, store, "parent-run")
	correctionAdapter := store.CorrectionStore()
	reserved, correctionErr := correctionAdapter.Reserve(correctionCtx, correction.Reservation{Record: correctionRecord, Limits: limits})
	if correctionErr != nil || !reserved.New {
		t.Fatal("correction reserve", correctionErr)
	}
	nextCorrection := reserved.Record
	nextCorrection.Phase = correction.PhasePreflight
	nextCorrection.Link.UpdatedAt = now.Add(time.Second)
	nextCorrection.Link, _ = nextCorrection.Link.Seal()
	if _, correctionErr = correctionAdapter.Update(correctionCtx, nextCorrection.Link.Handle, correction.PhaseReserved, nextCorrection, "preflight_started"); correctionErr != nil {
		t.Fatal("correction update", correctionErr)
	}
	packageDocument, _ := controlplane.SealImportDocument(controlplane.ImportDocument{Stream: "packages", RelationKey: "typed-packages", Document: []byte(`{"items":[]}`)})
	manifest, err := controlplane.SealImportManifest(controlplane.ImportManifest{ImportID: "import-1", RunID: "run-1", ProjectID: "project-1", SourceIdentity: "local-1", ExpectedHeadDigest: head.Digest, ExpectedEpoch: head.Epoch, Counts: map[string]uint64{"packages": 1}, StreamDigests: map[string]string{"packages": packageDocument.Digest}, RelationDigests: map[string]string{"packages": integrationDigest("typed-packages")}, State: controlplane.ImportFrozen, CreatedAt: now})
	if err != nil {
		t.Fatal(err)
	}
	manifest, err = store.SaveImport(ctx, manifest, "")
	if err != nil {
		t.Fatal(err)
	}
	if sameManifest, saveErr := store.SaveImport(ctx, manifest, ""); saveErr != nil || sameManifest.Digest != manifest.Digest {
		t.Fatal("import idempotency", saveErr)
	}
	conflictingManifest := manifest
	conflictingManifest.Counts = map[string]uint64{"packages": 2}
	conflictingManifest.Digest = ""
	if _, saveErr := store.SaveImport(ctx, conflictingManifest, ""); !errors.Is(saveErr, controlplane.ErrConflict) {
		t.Fatal("import identity conflict")
	}
	if err = store.SaveImportPage(ctx, manifest.ImportID, []controlplane.ImportDocument{packageDocument}); err != nil {
		t.Fatal(err)
	}
	manifest, err = store.VerifyImport(ctx, manifest.ImportID)
	if err != nil {
		t.Fatal(err)
	}
	loadedManifest, err := store.LoadImport(ctx, manifest.ImportID)
	if err != nil || loadedManifest.State != controlplane.ImportVerified {
		t.Fatal("import", err)
	}
	previous := integrationDigest("stale")
	staleManifest := manifest
	staleManifest.State = controlplane.ImportCutover
	staleManifest.Digest = ""
	if _, err = store.SaveImport(ctx, staleManifest, previous); err == nil {
		t.Fatal("stale import accepted")
	}
	if _, err = store.LoadImport(ctx, "missing"); err == nil {
		t.Fatal("missing import found")
	}
	if _, err = store.DeleteTerminalBefore(ctx, now.Add(2*time.Hour).Unix(), 100); err != nil {
		t.Fatal(err)
	}
	if err = retryTransaction(ctx, store.pool, 1, func(pgx.Tx) error { return nil }); err != nil {
		t.Fatal(err)
	}
	if err = retryTransaction(ctx, store.pool, 2, func(pgx.Tx) error { return &pgconn.PgError{Code: "40001"} }); err == nil {
		t.Fatal("transaction retry did not exhaust")
	}
	if err = retryTransaction(ctx, store.pool, 1, func(pgx.Tx) error { return &pgconn.PgError{Code: "23505"} }); !errors.Is(err, controlplane.ErrConflict) {
		t.Fatal("nonretry transaction mapping")
	}
	cancelledRetry, cancelRetry := context.WithCancel(ctx)
	cancelRetry()
	if err = retryTransaction(cancelledRetry, store.pool, 1, func(pgx.Tx) error { return nil }); !errors.Is(err, controlplane.ErrCancelled) {
		t.Fatal("cancelled transaction retry")
	}
}

// TestPostgresProductDispatchSurvivesWorkerRestart proves causal ingress, workflow state and effect intent resume solely from PostgreSQL.
func TestPostgresProductDispatchSurvivesWorkerRestart(t *testing.T) {
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Microsecond)
	store := integrationStore(t)
	if err := store.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	admission := integrationAdmission(now, "product-restart")
	if _, err := store.Admit(ctx, admission); err != nil {
		t.Fatal(err)
	}
	claimed, err := store.Claim(ctx, controlplane.ClaimRequest{ProjectID: admission.Job.ProjectID, WorkerID: "product-worker", Now: now, Lease: time.Minute, Limit: 1})
	if err != nil || len(claimed) != 1 {
		t.Fatal("claim", err)
	}
	limits := map[string]environment.CapacityLimits{admission.Job.ProjectID: {Environments: 2, CPUMilli: 2000, MemoryBytes: 4096, ObjectCount: 20, MaxTTL: time.Hour}}
	domains, err := store.DomainStores(DomainStoreConfig{Clock: integrationClock{now: now}, FunctionalTTL: time.Minute, CapacityLimits: limits})
	if err != nil {
		t.Fatal(err)
	}
	workflowRecord := validationworkflow.Record{Version: 1, Handle: "product-workflow", Principal: "product-principal", IdempotencyKey: "product-workflow-key", RequestDigest: admission.Job.InputDigest, RepositoryID: admission.Job.ProjectID, Revision: strings.Repeat("f", 40), Phase: validationworkflow.PhaseAdmitted}
	if _, created, _, reserveErr := domains.Workflow.ReserveBound(ctx, workflowRecord, "product-workflow-worker", now, time.Minute); reserveErr != nil || !created {
		t.Fatal("workflow reserve", reserveErr)
	}
	ctx = integrationBoundContext(t, store, admission.Job.RunID)
	selection, err := environment.SealSelection(environment.Selection{Version: 1, Alias: "product-env", ProviderID: "provider", ProviderVersion: "v1", Kind: environment.KindManaged, TemplateReference: "template", ProfileID: "profile", NetworkPolicyID: "network", Secretless: true, ProjectID: admission.Job.ProjectID, RepositoryID: "repository", Capabilities: []string{"http"}})
	if err != nil {
		t.Fatal(err)
	}
	environmentRecord := environment.Record{Version: 1, RunID: admission.Job.RunID, ProjectID: admission.Job.ProjectID, RequestDigest: admission.Job.InputDigest, AcquisitionKey: integrationDigest("product-acquisition"), CommitOID: strings.Repeat("e", 40), Selection: selection, State: environment.StateSelected, Revision: 1, CreatedAt: now, TTL: time.Hour, ExpiresAt: now.Add(time.Hour), Demand: environment.ResourceDemand{Environments: 1, CPUMilli: 100, MemoryBytes: 512, ObjectCount: 1}, CapacityReceipt: environment.CapacityReceipt{ID: "product-capacity", Digest: integrationDigest("product-capacity")}}
	environmentRecord, _, err = domains.Environment.Reserve(ctx, environmentRecord)
	if err != nil {
		t.Fatal(err)
	}
	nextJob, err := controlplane.SealJob(controlplane.Job{JobID: "product-next-job", ProjectID: admission.Job.ProjectID, RunID: admission.Job.RunID, BackendEpoch: admission.Job.BackendEpoch, BindingDigest: admission.Job.BindingDigest, Phase: controlplane.PhaseCleanup, State: controlplane.JobQueued, InputDigest: admission.Job.InputDigest, CreatedAt: now, AvailableAt: now, Deadline: now.Add(time.Hour)})
	if err != nil {
		t.Fatal(err)
	}
	effect := controlplane.EffectIntent{OperationID: "product-environment-effect", IdempotencyKey: "product-environment", ProjectID: admission.Job.ProjectID, RunID: admission.Job.RunID, Phase: controlplane.PhaseExecute, BindingDigest: admission.Job.BindingDigest, InputDigest: admission.Job.InputDigest, ReconcileMethod: "reload_environment"}
	environmentRecord.State, environmentRecord.ReleaseRequired, environmentRecord.UpdatedAt = environment.StateAcquiring, true, now
	for _, boundary := range []string{"environment.after_projection", "environment.after_event", "environment.after_job", "environment.after_outbox"} {
		store.fault = func(candidate string) error {
			if candidate == boundary {
				return controlplane.ErrUnavailable
			}
			return nil
		}
		if _, err = domains.Environment.SaveCausal(ctx, environmentRecord, environmentRecord.Revision, integrationEnvironmentEvent(environmentRecord, "product-environment-"+boundary, now), &nextJob, &effect); err == nil {
			t.Fatal("environment fault not injected", boundary)
		}
	}
	store.fault = nil
	if _, err = domains.Environment.SaveCausal(ctx, environmentRecord, environmentRecord.Revision, integrationEnvironmentEvent(environmentRecord, "product-environment-acquiring", now), &nextJob, &effect); err != nil {
		t.Fatal("environment causal outputs", err)
	}
	var effects int
	if err = store.pool.QueryRow(ctx, `SELECT count(*) FROM effect_intents WHERE run_id=$1`, admission.Job.RunID).Scan(&effects); err != nil || effects != 2 {
		t.Fatal("causal effect", effects, err)
	}
	restarted, err := Open(ctx, Config{DSN: store.pool.Config().ConnString(), BackendIdentity: "pg-test", MaxConnections: 4, StatementTimeout: 5 * time.Second, MaxLease: 5 * time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	defer restarted.Close()
	if err = restarted.Ready(ctx); err != nil {
		t.Fatal("restart readiness", err)
	}
	restartedDomains, err := restarted.DomainStores(DomainStoreConfig{Clock: integrationClock{now: now.Add(time.Second)}, FunctionalTTL: time.Minute, CapacityLimits: limits})
	if err != nil {
		t.Fatal(err)
	}
	workflow, err := restartedDomains.Workflow.Load(ctx, workflowRecord.Handle)
	if err != nil || workflow.Phase != validationworkflow.PhaseAdmitted {
		t.Fatal("workflow restart", err)
	}
	if _, err = restarted.LoadIngress(ctx, admission.Job.RunID); err != nil {
		t.Fatal("ingress restart", err)
	}
	claim, err := restarted.ClaimEffect(ctx, admission.Intent.OperationID, "restarted-dispatcher", now, time.Minute)
	if err != nil {
		t.Fatal("claim causal ingress effect", err)
	}
	if loaded, loadErr := restarted.LoadEffect(ctx, claim); loadErr != nil || loaded.OperationID != admission.Intent.OperationID {
		t.Fatal("load causal ingress effect", loadErr)
	}
	causalJob := claimed[0]
	if err = restarted.ConfirmEffectAndJob(ctx, claim, causalJob, integrationDigest("remote-receipt"), now.Add(time.Second)); err != nil {
		t.Fatal("causal terminal", err)
	}
	if terminal, completeErr := restarted.Complete(ctx, causalJob.JobID, causalJob.Owner, causalJob.Fence, causalJob.CancelRevision, now.Add(time.Second), controlplane.JobSucceeded, ""); completeErr != nil || terminal.State != controlplane.JobSucceeded {
		t.Fatal("causal terminal replay", completeErr)
	}
	var outboxState string
	if err = restarted.pool.QueryRow(ctx, `SELECT state FROM outbox WHERE operation_id=$1`, claim.OperationID).Scan(&outboxState); err != nil || outboxState != "confirmed" {
		t.Fatal("outbox terminal", outboxState, err)
	}
}

// TestPostgresDomainSessionFenceRejectsCutoverLateWrite proves an adapter that
// observed epoch N cannot mutate after cutover while a freshly composed
// process may recover the same durable projection under epoch N+1.
func TestPostgresDomainSessionFenceRejectsCutoverLateWrite(t *testing.T) {
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Microsecond)
	store := integrationStore(t)
	if err := store.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	workflow := validationworkflow.Record{Version: 1, Handle: "domain-fence-run", Principal: "domain-principal", IdempotencyKey: "domain-key", RequestDigest: integrationDigest("domain-workflow"), RepositoryID: "domain-fence-project", Revision: strings.Repeat("d", 40), Phase: validationworkflow.PhaseAdmitted}
	_, created, _, err := store.WorkflowStore().ReserveBound(ctx, workflow, "domain-owner", now, time.Minute)
	if err != nil || !created {
		t.Fatal(err)
	}
	if _, err = store.WorkflowStore().RequestCancel(ctx, workflow.Handle, now, validationworkflow.Event{Type: "missing-coordinate"}); err == nil {
		t.Fatal("epoch one mutation accepted without a bound coordinate")
	}
	_, staleContext, err := store.WorkflowStore().LoadBound(ctx, workflow.Handle)
	if err != nil {
		t.Fatal(err)
	}
	head, epoch, err := store.Load(ctx, workflow.Handle)
	if err != nil {
		t.Fatal(err)
	}
	next := controlplane.BackendEpoch{RunID: epoch.RunID, ProjectID: epoch.ProjectID, Epoch: 2, Mode: controlplane.BackendPostgres, BackendIdentity: "pg-test", SchemaVersion: 1, CreatedAt: now.Add(time.Second), PreviousDigest: epoch.Digest}
	if _, err = store.Cutover(ctx, head, next); err != nil {
		t.Fatal(err)
	}
	if _, err = store.WorkflowStore().RequestCancel(staleContext, workflow.Handle, now, validationworkflow.Event{Type: "stale-cancel"}); err == nil {
		t.Fatal("first mutation prepared under the stale read crossed cutover")
	}
	restarted, err := Open(ctx, Config{DSN: store.pool.Config().ConnString(), BackendIdentity: "pg-test", MaxConnections: 2, StatementTimeout: 5 * time.Second, MaxLease: 5 * time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	defer restarted.Close()
	_, freshContext, bindErr := restarted.WorkflowStore().LoadBound(ctx, epoch.RunID)
	if bindErr != nil {
		t.Fatal(bindErr)
	}
	if _, err = restarted.WorkflowStore().RequestCancel(freshContext, workflow.Handle, now, validationworkflow.Event{Type: "fresh-cancel"}); err != nil {
		t.Fatal("new epoch recovery", err)
	}
}

// TestPostgresGateRejectsInvalidAndStaleOperations covers fail-closed adapter boundaries.
func TestPostgresGateRejectsInvalidAndStaleOperations(t *testing.T) {
	ctx := context.Background()
	store := integrationStore(t)
	if err := store.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	if _, err := store.Enqueue(ctx, controlplane.Job{}); !errors.Is(err, controlplane.ErrInvalid) {
		t.Fatal("invalid enqueue")
	}
	if _, err := store.Claim(ctx, controlplane.ClaimRequest{}); !errors.Is(err, controlplane.ErrInvalid) {
		t.Fatal("invalid claim")
	}
	if _, err := store.Heartbeat(ctx, "missing", "worker-1", 1, 0, now, time.Minute); !errors.Is(err, controlplane.ErrNotFound) {
		t.Fatal("missing heartbeat")
	}
	if _, err := store.Complete(ctx, "missing", "worker-1", 1, 0, now, controlplane.JobSucceeded, ""); !errors.Is(err, controlplane.ErrNotFound) {
		t.Fatal("missing complete")
	}
	if _, err := store.Retry(ctx, "missing", "worker-1", 1, 0, now, controlplane.RetryDecision{}, now); !errors.Is(err, controlplane.ErrInvalid) {
		t.Fatal("invalid retry")
	}
	if _, err := store.Cancel(ctx, "missing", 1); !errors.Is(err, controlplane.ErrNotFound) {
		t.Fatal("missing cancel")
	}
	if _, err := store.Recover(ctx, now, 0); !errors.Is(err, controlplane.ErrInvalid) {
		t.Fatal("invalid recover")
	}
	if _, err := store.ClaimOutbox(ctx, "", now, time.Minute, 1); !errors.Is(err, controlplane.ErrInvalid) {
		t.Fatal("invalid outbox claim")
	}
	if _, err := store.RecoverOutbox(ctx, now, 0); !errors.Is(err, controlplane.ErrInvalid) {
		t.Fatal("invalid outbox recover")
	}
	if _, err := store.CommitPackage(ctx, validationworkflow.PackageDocument{}); !errors.Is(err, controlplane.ErrInvalid) {
		t.Fatal("invalid package")
	}
	badPackage := validationworkflow.PackageDocument{ProjectID: "project-1", RunID: "run-1", PackageID: "package-1", MediaType: "x", SchemaVersion: "v1", ArtifactDigest: integrationDigest("artifact"), BindingDigest: integrationDigest("binding"), Document: []byte("x"), Digest: integrationDigest("other"), CreatedAt: now}
	if _, err := store.CommitPackage(ctx, badPackage); !errors.Is(err, controlplane.ErrIntegrity) {
		t.Fatal("tampered package")
	}
	if _, _, workflowErr := store.WorkflowStore().Reserve(ctx, validationworkflow.Record{}, "", now, time.Minute); workflowErr == nil {
		t.Fatal("invalid workflow")
	}
	if _, err := store.DeleteTerminalBefore(ctx, 0, 0); !errors.Is(err, controlplane.ErrInvalid) {
		t.Fatal("invalid retention")
	}
	if err := store.SaveAuthority(ctx, controlplane.AuthorityEnvelope{}, controlplane.Job{}, "worker", now); err == nil {
		t.Fatal("invalid authority")
	}
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if err := store.Migrate(cancelled); !errors.Is(err, controlplane.ErrCancelled) {
		t.Fatal("cancelled migrate")
	}
	if _, _, err := store.Load(cancelled, "missing"); !errors.Is(err, controlplane.ErrCancelled) {
		t.Fatal("cancelled load")
	}
	if _, err := store.Enqueue(cancelled, integrationJob(now, "cancelled-job", 1, integrationDigest("binding"))); !errors.Is(err, controlplane.ErrCancelled) {
		t.Fatal("cancelled enqueue")
	}
	if _, err := store.Claim(cancelled, controlplane.ClaimRequest{ProjectID: "project-1", WorkerID: "worker-1", Now: now, Lease: time.Minute, Limit: 1}); !errors.Is(err, controlplane.ErrCancelled) {
		t.Fatal("cancelled claim")
	}
	if _, err := store.DeleteTerminalBefore(cancelled, now.Unix(), 1); !errors.Is(err, controlplane.ErrCancelled) {
		t.Fatal("cancelled retention")
	}
}

// TestPostgresIngressRollbackAndUnknownCommit proves ACK state is causal across every admission statement boundary.
func TestPostgresIngressRollbackAndUnknownCommit(t *testing.T) {
	ctx := context.Background()
	store := integrationStore(t)
	if err := store.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	boundaries := []struct {
		name      string
		committed bool
	}{{"ingress.after_inbox", false}, {"ingress.after_epoch", false}, {"ingress.after_head", false}, {"ingress.after_event", false}, {"ingress.after_job", false}, {"ingress.after_grant", false}, {"ingress.after_commit", true}}
	for index, boundary := range boundaries {
		admission := integrationAdmission(now.Add(time.Duration(index)*time.Second), fmt.Sprintf("fault-%d", index))
		store.fault = func(name string) error {
			if name == boundary.name {
				return errors.New("injected")
			}
			return nil
		}
		if _, err := store.Admit(ctx, admission); err == nil {
			t.Fatalf("boundary %s did not fail", boundary.name)
		}
		store.fault = nil
		var heads int
		if err := store.pool.QueryRow(ctx, `SELECT count(*) FROM backend_binding_heads WHERE run_id=$1`, admission.Epoch.RunID).Scan(&heads); err != nil {
			t.Fatal(err)
		}
		if boundary.committed && heads != 1 || !boundary.committed && heads != 0 {
			t.Fatalf("boundary %s left heads=%d", boundary.name, heads)
		}
		if boundary.committed {
			if created, err := store.Admit(ctx, admission); err != nil || created {
				t.Fatal("unknown commit was not reconciled", err)
			}
		}
	}
}

// TestPostgresQueueFencesCutoverCancelExpiryOrderingAndSecrets covers distributed worker interleavings with no sleeps.
func TestPostgresQueueFencesCutoverCancelExpiryOrderingAndSecrets(t *testing.T) {
	ctx := context.Background()
	store := integrationStore(t)
	if err := store.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	reserve := func(runID string) (controlplane.BindingHead, controlplane.BackendEpoch) {
		epoch, _ := controlplane.SealBackendEpoch(controlplane.BackendEpoch{RunID: runID, ProjectID: "project-1", Epoch: 1, Mode: controlplane.BackendPostgres, BackendIdentity: "pg-test", SchemaVersion: 1, CreatedAt: now})
		head, err := store.ReserveInitial(ctx, epoch)
		if err != nil {
			t.Fatal(err)
		}
		return head, epoch
	}
	boundJob := func(runID, jobID string, epoch uint64, digest string, created, available time.Time) controlplane.Job {
		job, _ := controlplane.SealJob(controlplane.Job{JobID: jobID, ProjectID: "project-1", RunID: runID, BackendEpoch: epoch, BindingDigest: digest, Phase: controlplane.PhaseExecute, State: controlplane.JobQueued, InputDigest: integrationDigest(jobID), CreatedAt: created, AvailableAt: available, Deadline: available.Add(time.Hour)})
		return job
	}
	head, epoch := reserve("stale-run")
	job := boundJob(epoch.RunID, "stale-job", 1, epoch.Digest, now, now)
	if _, err := store.Enqueue(ctx, job); err != nil {
		t.Fatal(err)
	}
	claimed, err := store.Claim(ctx, controlplane.ClaimRequest{ProjectID: "project-1", WorkerID: "worker-stale", Now: now, Lease: time.Minute, Limit: 1})
	if err != nil || len(claimed) != 1 {
		t.Fatal("stale claim", err)
	}
	grant, _ := controlplane.SealAuthorityGrant(controlplane.AuthorityGrant{ProjectID: "project-1", RunID: epoch.RunID, BackendEpoch: epoch.Epoch, BindingDigest: epoch.Digest, Phase: controlplane.PhaseExecute, AllowedCapabilities: []string{"http.execute"}, TargetBindingRef: "target-1", TargetBindingDigest: integrationDigest("target"), SecretReferenceIDs: []string{"secret-1"}})
	if err = store.SaveAuthorityGrant(ctx, grant); err != nil {
		t.Fatal(err)
	}
	if err = store.SaveAuthorityGrant(ctx, grant); err != nil {
		t.Fatal("authority grant replay", err)
	}
	conflictingGrant := grant
	conflictingGrant.TargetBindingRef = "target-2"
	conflictingGrant.TargetBindingDigest = integrationDigest("target-2")
	conflictingGrant.Digest = ""
	if err = store.SaveAuthorityGrant(ctx, conflictingGrant); !errors.Is(err, controlplane.ErrConflict) {
		t.Fatal("authority grant conflict accepted", err)
	}
	authority, err := controlplane.DeriveAuthority(ctx, store, claimed[0], "authority-1", claimed[0].Owner, now.Add(30*time.Second), now)
	if err != nil || store.SaveAuthority(ctx, authority, claimed[0], claimed[0].Owner, now) != nil {
		t.Fatal("authority derivation", err)
	}
	resolver := &integrationSecretResolver{}
	if _, err = controlplane.ResolvePersistedSecrets(ctx, store, authority, claimed[0].Owner, now, resolver); err != nil || resolver.calls != 1 {
		t.Fatal("authorized JIT resolution", err)
	}
	next := controlplane.BackendEpoch{RunID: epoch.RunID, ProjectID: epoch.ProjectID, Epoch: 2, Mode: controlplane.BackendLocal, BackendIdentity: "local-archive", SchemaVersion: 1, CreatedAt: now.Add(time.Second), PreviousDigest: epoch.Digest}
	if _, err = store.Cutover(ctx, head, next); err != nil {
		t.Fatal(err)
	}
	if _, err = store.Complete(ctx, claimed[0].JobID, claimed[0].Owner, claimed[0].Fence, claimed[0].CancelRevision, now.Add(time.Second), controlplane.JobSucceeded, ""); !errors.Is(err, controlplane.ErrConflict) {
		t.Fatal("stale epoch completed")
	}
	if _, err = controlplane.ResolvePersistedSecrets(ctx, store, authority, claimed[0].Owner, now.Add(time.Second), resolver); err == nil || resolver.calls != 1 {
		t.Fatal("stale epoch resolved secrets")
	}
	_, cancelEpoch := reserve("cancel-run")
	cancelJob := boundJob(cancelEpoch.RunID, "cancel-race-job", 1, cancelEpoch.Digest, now, now)
	store.Enqueue(ctx, cancelJob)
	cancelClaim, _ := store.Claim(ctx, controlplane.ClaimRequest{ProjectID: "project-1", WorkerID: "worker-cancel", Now: now, Lease: time.Minute, Limit: 1})
	var owned controlplane.Job
	for _, candidate := range cancelClaim {
		if candidate.JobID == cancelJob.JobID {
			owned = candidate
		}
	}
	if owned.JobID == "" {
		t.Fatal("cancel job was not claimed")
	}
	if _, err = store.Cancel(ctx, owned.JobID, 1); err != nil {
		t.Fatal(err)
	}
	var cleanupJobs int
	if err = store.pool.QueryRow(ctx, `SELECT count(*) FROM jobs WHERE run_id=$1 AND phase='cleanup' AND state='queued'`, owned.RunID).Scan(&cleanupJobs); err != nil || cleanupJobs != 1 {
		t.Fatal("cancel did not schedule convergence cleanup", err)
	}
	if _, err = store.Complete(ctx, owned.JobID, owned.Owner, owned.Fence, owned.CancelRevision, now, controlplane.JobSucceeded, ""); !errors.Is(err, controlplane.ErrConflict) {
		t.Fatal("cancelled owner completed")
	}
	_, expiryEpoch := reserve("expiry-run")
	expiryJob := boundJob(expiryEpoch.RunID, "expiry-race-job", 1, expiryEpoch.Digest, now.Add(-3*time.Second), now.Add(-3*time.Second))
	store.Enqueue(ctx, expiryJob)
	expiryClaims, _ := store.Claim(ctx, controlplane.ClaimRequest{ProjectID: "project-1", WorkerID: "worker-expiry", Now: now.Add(-2 * time.Second), Lease: time.Second, Limit: 10})
	for _, candidate := range expiryClaims {
		if candidate.JobID == expiryJob.JobID {
			if _, err = store.Complete(ctx, candidate.JobID, candidate.Owner, candidate.Fence, candidate.CancelRevision, now, controlplane.JobSucceeded, ""); !errors.Is(err, controlplane.ErrConflict) {
				t.Fatal("expired owner completed")
			}
		}
	}
	_, orderEpoch := reserve("order-run")
	available := now.Add(10 * time.Second)
	for _, ordered := range []controlplane.Job{boundJob(orderEpoch.RunID, "order-z", 1, orderEpoch.Digest, now, available), boundJob(orderEpoch.RunID, "order-a", 1, orderEpoch.Digest, now.Add(time.Second), available)} {
		if _, err = store.Enqueue(ctx, ordered); err != nil {
			t.Fatal(err)
		}
	}
	orderedClaims, err := store.Claim(ctx, controlplane.ClaimRequest{ProjectID: "project-1", WorkerID: "worker-order", Now: available, Lease: time.Minute, Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	positions := map[string]int{}
	for index, candidate := range orderedClaims {
		positions[candidate.JobID] = index
	}
	if positions["order-z"] >= positions["order-a"] {
		t.Fatal("created_at ordering was not preserved")
	}
}

// runWorkflowStoreContract verifies identical consumer semantics across local and PostgreSQL stores.
func runWorkflowStoreContract(t *testing.T, store validationworkflow.WorkflowStore) {
	t.Helper()
	ctx := context.Background()
	now := time.Now().UTC()
	record := validationworkflow.Record{Version: 1, Handle: "contract-workflow", Principal: "principal-1", IdempotencyKey: "contract-key", RequestDigest: integrationDigest("contract-request"), Phase: validationworkflow.PhaseAdmitted}
	var reserved validationworkflow.Record
	var created bool
	var err error
	if bounded, ok := store.(validationworkflow.BoundWorkflowStore); ok {
		reserved, created, ctx, err = bounded.ReserveBound(ctx, record, "contract-worker", now, time.Minute)
	} else {
		reserved, created, err = store.Reserve(ctx, record, "contract-worker", now, time.Minute)
	}
	if err != nil || !created {
		t.Fatal("reserve", err)
	}
	var same validationworkflow.Record
	if bounded, ok := store.(validationworkflow.BoundWorkflowStore); ok {
		same, created, ctx, err = bounded.ReserveBound(ctx, record, "contract-worker", now, time.Minute)
	} else {
		same, created, err = store.Reserve(ctx, record, "contract-worker", now, time.Minute)
	}
	if err != nil || created || same.Handle != reserved.Handle {
		t.Fatal("redelivery", err)
	}
	reserved.Phase = validationworkflow.PhaseGenerating
	saved, err := store.SaveOwned(ctx, reserved, "contract-worker", reserved.VersionCAS, now, time.Minute, validationworkflow.Event{Type: "generation_started"})
	if err != nil {
		t.Fatal("save", err)
	}
	if _, err = store.SaveOwned(ctx, reserved, "contract-worker", reserved.VersionCAS, now, time.Minute, validationworkflow.Event{Type: "stale"}); err == nil {
		t.Fatal("stale CAS accepted")
	}
	events, err := store.Events(ctx, reserved.Handle)
	if err != nil || len(events) != 2 || events[1].Sequence != 2 {
		t.Fatal("journal", err)
	}
	if _, err = store.ReleaseOwner(ctx, saved.Handle, "contract-worker", saved.VersionCAS, now); err != nil {
		t.Fatal("release", err)
	}
}

// TestWorkflowStoreContractLocalAndPostgres reuses one suite against both authoritative modes.
func TestWorkflowStoreContractLocalAndPostgres(t *testing.T) {
	t.Run("local", func(t *testing.T) {
		store, err := validationworkflow.OpenLocalStore(t.TempDir())
		if err != nil {
			t.Fatal(err)
		}
		runWorkflowStoreContract(t, store)
	})
	t.Run("postgres", func(t *testing.T) {
		store := integrationStore(t)
		if err := store.Migrate(context.Background()); err != nil {
			t.Fatal(err)
		}
		runWorkflowStoreContract(t, store.WorkflowStore())
	})
}

// TestPostgresMigrationLedgerFailsClosed exercises dirty, abort, checksum, hole, future, server and binary-window guards.
func TestPostgresMigrationLedgerFailsClosed(t *testing.T) {
	ctx := context.Background()
	cases := []struct {
		name   string
		mutate func(context.Context, *Store) error
	}{
		{"dirty", func(ctx context.Context, store *Store) error {
			_, err := store.pool.Exec(ctx, `UPDATE schema_migration_state SET dirty=true,target_version=current_version+1`)
			return err
		}},
		{"checksum", func(ctx context.Context, store *Store) error {
			_, err := store.pool.Exec(ctx, `UPDATE schema_migrations SET checksum=$1 WHERE version=1`, integrationDigest("tampered"))
			return err
		}},
		{"hole", func(ctx context.Context, store *Store) error {
			_, err := store.pool.Exec(ctx, `DELETE FROM schema_migrations WHERE version=2`)
			return err
		}},
		{"future", func(ctx context.Context, store *Store) error {
			manifest, _ := loadMigrations()
			if _, err := store.pool.Exec(ctx, `INSERT INTO schema_migrations(version,name,checksum,phase,boundary) VALUES($1,'future.sql',$2,'expand','transaction')`, len(manifest)+1, integrationDigest("future")); err != nil {
				return err
			}
			_, err := store.pool.Exec(ctx, `UPDATE schema_migration_state SET current_version=$1,min_compatible=$1,max_compatible=$1`, len(manifest)+1)
			return err
		}},
		{"server", func(ctx context.Context, store *Store) error {
			_, err := store.pool.Exec(ctx, `UPDATE schema_migration_state SET server_major=server_major+1`)
			return err
		}},
	}
	for _, item := range cases {
		t.Run(item.name, func(t *testing.T) {
			store := integrationStore(t)
			if err := store.Migrate(ctx); err != nil {
				t.Fatal(err)
			}
			if err := item.mutate(ctx, store); err != nil {
				t.Fatal(err)
			}
			if err := store.Ready(ctx); !errors.Is(err, ErrSchema) {
				t.Fatalf("expected schema rejection: %v", err)
			}
		})
	}
	t.Run("aborted_descriptor", func(t *testing.T) {
		store := integrationStore(t)
		if err := store.Migrate(ctx); err != nil {
			t.Fatal(err)
		}
		manifest, _ := loadMigrations()
		connection, err := store.pool.Acquire(ctx)
		if err != nil {
			t.Fatal(err)
		}
		defer connection.Release()
		if _, err = connection.Exec(ctx, `UPDATE schema_migration_state SET current_version=current_version+1,max_compatible=max_compatible+1`); err != nil {
			t.Fatal(err)
		}
		if err = markMigrationDirty(ctx, connection, len(manifest)+2); err != nil {
			t.Fatal(err)
		}
		invalid := migration{version: len(manifest) + 2, name: "invalid.sql", checksum: integrationDigest("invalid"), sql: `THIS IS NOT SQL`, phase: phaseExpand, boundary: migrationBoundaryTransaction}
		if err = applyMigration(ctx, connection, invalid, supportedServerMajor, invalid.version); err == nil {
			t.Fatal("invalid migration committed")
		}
		var dirty bool
		if err = store.pool.QueryRow(ctx, `SELECT dirty FROM schema_migration_state`).Scan(&dirty); err != nil || !dirty {
			t.Fatal("dirty marker lost", err)
		}
	})
	t.Run("binary_window", func(t *testing.T) {
		store := integrationStore(t)
		if err := store.Migrate(ctx); err != nil {
			t.Fatal(err)
		}
		manifest, _ := loadMigrations()
		major, err := readServerMajor(ctx, store.pool)
		if err != nil {
			t.Fatal(err)
		}
		oldManifest := append([]migration(nil), manifest[:len(manifest)-1]...)
		ledger, applied, err := readAndValidateLedger(ctx, store.pool, oldManifest, major)
		if err != nil || applied != len(manifest) || ledger.currentVersion != len(manifest) {
			t.Fatal("N manifest rejected N+1 forward-compatible schema", err)
		}
		outsideWindow := append([]migration(nil), manifest[:len(manifest)-2]...)
		if _, _, err = readAndValidateLedger(ctx, store.pool, outsideWindow, major); !errors.Is(err, ErrSchema) {
			t.Fatal("N-1 manifest accepted outside declared rollback window")
		}
	})
}

// TestPostgresMigrationRequiresDDLPermission proves readiness cannot bootstrap under an underprivileged runtime role.
func TestPostgresMigrationRequiresDDLPermission(t *testing.T) {
	ctx := context.Background()
	dsn := os.Getenv("FEATUREPROOF_POSTGRES_TEST_DSN")
	admin, err := Open(ctx, Config{DSN: dsn, BackendIdentity: "pg-test", MaxConnections: 2, StatementTimeout: 5 * time.Second, MaxLease: time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close()
	suffix := fmt.Sprint(time.Now().UnixNano())
	role, schema := "featureproof_reader_"+suffix, "featureproof_permission_"+suffix
	roleID, schemaID := pgx.Identifier{role}.Sanitize(), pgx.Identifier{schema}.Sanitize()
	if _, err = admin.pool.Exec(ctx, `CREATE ROLE `+roleID+` LOGIN; CREATE SCHEMA `+schemaID+`; GRANT USAGE ON SCHEMA `+schemaID+` TO `+roleID); err != nil {
		t.Fatal(err)
	}
	defer func() {
		_, _ = admin.pool.Exec(context.Background(), `DROP SCHEMA `+schemaID+` CASCADE`)
		_, _ = admin.pool.Exec(context.Background(), `DROP ROLE `+roleID)
	}()
	parsed, err := url.Parse(dsn)
	if err != nil {
		t.Fatal(err)
	}
	parsed.User = url.User(role)
	query := parsed.Query()
	query.Set("search_path", schema)
	parsed.RawQuery = query.Encode()
	limited, err := Open(ctx, Config{DSN: parsed.String(), BackendIdentity: "pg-test", MaxConnections: 1, StatementTimeout: 5 * time.Second, MaxLease: time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	defer limited.Close()
	if err = limited.Migrate(ctx); !errors.Is(err, controlplane.ErrUnauthorized) {
		t.Fatalf("underprivileged migration accepted: %v", err)
	}
}

// TestPostgresOfflineImportCutoverRecovery proves staged data has no authority and cutover/receipt recover unknown commits.
func TestPostgresOfflineImportCutoverRecovery(t *testing.T) {
	ctx := context.Background()
	now := time.Now().UTC()
	store := integrationStore(t)
	if err := store.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	local, err := controlplane.OpenLocalImportStore(t.TempDir(), "local-import")
	if err != nil {
		t.Fatal(err)
	}
	document := controlplane.ImportDocument{Stream: "packages", RelationKey: "typed-packages", Document: []byte(`{"items":[]}`)}
	if err = local.Put(ctx, document); err != nil {
		t.Fatal(err)
	}
	epoch, _ := controlplane.SealBackendEpoch(controlplane.BackendEpoch{RunID: "import-run", ProjectID: "project-1", Epoch: 1, Mode: controlplane.BackendLocal, BackendIdentity: "local-import", SchemaVersion: 1, CreatedAt: now})
	head, err := store.ReserveInitial(ctx, epoch)
	if err != nil {
		t.Fatal(err)
	}
	manifest, err := local.Freeze(ctx, "real-import", epoch.RunID, epoch.ProjectID, head.Digest, head.Epoch, now)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = store.SaveImport(ctx, manifest, ""); err != nil {
		t.Fatal(err)
	}
	page, err := local.ReadImportPage(ctx, manifest.ImportID, "packages", 0, 10)
	if err != nil {
		t.Fatal(err)
	}
	partial := manifest
	partial.ImportID, partial.Counts, partial.Digest = "partial-import", map[string]uint64{"packages": 2}, ""
	partial, _ = controlplane.SealImportManifest(partial)
	if _, err = store.SaveImport(ctx, partial, ""); err != nil {
		t.Fatal(err)
	}
	if err = store.SaveImportPage(ctx, partial.ImportID, page); err != nil {
		t.Fatal(err)
	}
	if _, err = store.VerifyImport(ctx, partial.ImportID); !errors.Is(err, controlplane.ErrIntegrity) {
		t.Fatal("partial candidate verified", err)
	}
	if _, err = store.AbandonImport(ctx, partial.ImportID); err != nil {
		t.Fatal("abandon candidate", err)
	}
	extra := manifest
	extra.ImportID, extra.Digest = "extra-stream-import", ""
	extra, _ = controlplane.SealImportManifest(extra)
	if _, err = store.SaveImport(ctx, extra, ""); err != nil {
		t.Fatal(err)
	}
	opaque, _ := controlplane.SealImportDocument(controlplane.ImportDocument{Stream: "opaque", RelationKey: "typed-opaque", Document: []byte(`{"unexpected":true}`)})
	if err = store.SaveImportPage(ctx, extra.ImportID, append(page, opaque)); err != nil {
		t.Fatal(err)
	}
	if _, err = store.VerifyImport(ctx, extra.ImportID); !errors.Is(err, controlplane.ErrIntegrity) {
		t.Fatal("undeclared stream verified", err)
	}
	if err = store.SaveImportPage(ctx, manifest.ImportID, page); err != nil {
		t.Fatal(err)
	}
	if _, err = store.VerifyImport(ctx, manifest.ImportID); err != nil {
		t.Fatal(err)
	}
	store.fault = func(boundary string) error {
		if boundary == "import.after_commit" {
			return errors.New("lost-response-canary")
		}
		return nil
	}
	if _, err = store.CutoverImport(ctx, manifest.ImportID, now.Add(time.Second)); !errors.Is(err, controlplane.ErrUnavailable) {
		t.Fatal("unknown commit not surfaced", err)
	}
	store.fault = nil
	if _, rawErr := loadImportReceiptTx(ctx, store.pool, manifest.ImportID); rawErr != nil {
		t.Fatalf("raw receipt: %T %v", rawErr, rawErr)
	}
	receipt, err := store.LoadImportCutoverReceipt(ctx, manifest.ImportID)
	if err != nil {
		t.Fatal(err)
	}
	if err = local.Archive(ctx, receipt); err != nil {
		t.Fatal(err)
	}
	loadedHead, loadedEpoch, err := store.Load(ctx, epoch.RunID)
	if err != nil || loadedHead.Epoch != 2 || loadedEpoch.Mode != controlplane.BackendPostgres {
		t.Fatal("cutover authority", err)
	}
	if _, err = store.CutoverImport(ctx, manifest.ImportID, now.Add(2*time.Second)); err != nil {
		t.Fatal("cutover redelivery", err)
	}
	if err = local.Unfreeze(ctx, manifest.ImportID); !errors.Is(err, controlplane.ErrConflict) {
		t.Fatal("archived source revived")
	}
}

// TestPostgresConcurrentImportsPublishOneHead proves two verified candidates cannot both supersede one expected head.
func TestPostgresConcurrentImportsPublishOneHead(t *testing.T) {
	ctx := context.Background()
	now := time.Now().UTC()
	store := integrationStore(t)
	if err := store.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	epoch, _ := controlplane.SealBackendEpoch(controlplane.BackendEpoch{RunID: "double-import-run", ProjectID: "project-1", Epoch: 1, Mode: controlplane.BackendLocal, BackendIdentity: "local-double", SchemaVersion: 1, CreatedAt: now})
	head, err := store.ReserveInitial(ctx, epoch)
	if err != nil {
		t.Fatal(err)
	}
	document := controlplane.ImportDocument{Stream: "packages", RelationKey: "typed-packages", Document: []byte(`{"items":[]}`)}
	sealedDocument, _ := controlplane.SealImportDocument(document)
	for _, importID := range []string{"double-import-a", "double-import-b"} {
		manifest, _ := controlplane.SealImportManifest(controlplane.ImportManifest{ImportID: importID, RunID: epoch.RunID, ProjectID: epoch.ProjectID, SourceIdentity: "local-double", ExpectedHeadDigest: head.Digest, ExpectedEpoch: head.Epoch, Counts: map[string]uint64{"packages": 1}, StreamDigests: map[string]string{"packages": sealedDocument.Digest}, RelationDigests: map[string]string{"packages": integrationDigest("typed-packages")}, State: controlplane.ImportFrozen, CreatedAt: now})
		if _, err = store.SaveImport(ctx, manifest, ""); err != nil {
			t.Fatal(err)
		}
		if err = store.SaveImportPage(ctx, importID, []controlplane.ImportDocument{sealedDocument}); err != nil {
			t.Fatal(err)
		}
		if _, err = store.VerifyImport(ctx, importID); err != nil {
			t.Fatal(err)
		}
	}
	results := make(chan error, 2)
	for _, importID := range []string{"double-import-a", "double-import-b"} {
		go func(id string) {
			_, cutoverErr := store.CutoverImport(ctx, id, now.Add(time.Second))
			results <- cutoverErr
		}(importID)
	}
	successes, conflicts := 0, 0
	for index := 0; index < 2; index++ {
		if result := <-results; result == nil {
			successes++
		} else if errors.Is(result, controlplane.ErrConflict) {
			conflicts++
		} else {
			t.Fatal(result)
		}
	}
	if successes != 1 || conflicts != 1 {
		t.Fatalf("success=%d conflict=%d", successes, conflicts)
	}
}

// TestPostgresImportCutoverRejectsMissingBindingHead proves a verified import
// cannot create authority when its source run has no durable binding head.
func TestPostgresImportCutoverRejectsMissingBindingHead(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	store := integrationStore(t)
	if err := store.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	document, err := controlplane.SealImportDocument(controlplane.ImportDocument{Stream: "packages", RelationKey: "typed-packages", Document: []byte(`{"items":[]}`)})
	if err != nil {
		t.Fatal(err)
	}
	manifest, err := controlplane.SealImportManifest(controlplane.ImportManifest{
		ImportID:           "missing-head-import",
		RunID:              "missing-head-run",
		ProjectID:          "project-1",
		SourceIdentity:     "local-missing-head",
		ExpectedHeadDigest: integrationDigest("missing-head"),
		ExpectedEpoch:      1,
		Counts:             map[string]uint64{"packages": 1},
		StreamDigests:      map[string]string{"packages": document.Digest},
		RelationDigests:    map[string]string{"packages": integrationDigest("typed-packages")},
		State:              controlplane.ImportFrozen,
		CreatedAt:          time.Now().UTC(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = store.SaveImport(ctx, manifest, ""); err != nil {
		t.Fatal(err)
	}
	if err = store.SaveImportPage(ctx, manifest.ImportID, []controlplane.ImportDocument{document}); err != nil {
		t.Fatal(err)
	}
	if _, err = store.VerifyImport(ctx, manifest.ImportID); err != nil {
		t.Fatal(err)
	}
	if _, err = store.CutoverImport(ctx, manifest.ImportID, time.Now().UTC()); !errors.Is(err, controlplane.ErrUnavailable) {
		t.Fatalf("missing binding head allowed import cutover: %v", err)
	}
}

// TestPostgresKeysetRetentionAndObserver covers stable pages, authenticated cursors, references, summaries and sink isolation.
func TestPostgresKeysetRetentionAndObserver(t *testing.T) {
	ctx := context.Background()
	now := time.Now().UTC()
	store := integrationStore(t)
	if err := store.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	observer := &failingObserver{}
	store.observer = observer
	epoch, _ := controlplane.SealBackendEpoch(controlplane.BackendEpoch{RunID: "query-run", ProjectID: "project-1", Epoch: 1, Mode: controlplane.BackendPostgres, BackendIdentity: "pg-test", SchemaVersion: 1, CreatedAt: now})
	if _, err := store.ReserveInitial(ctx, epoch); err != nil {
		t.Fatal(err)
	}
	for index, id := range []string{"query-a", "query-b"} {
		created := now.Add(time.Duration(index) * time.Nanosecond)
		job, _ := controlplane.SealJob(controlplane.Job{JobID: id, ProjectID: epoch.ProjectID, RunID: epoch.RunID, BackendEpoch: 1, BindingDigest: epoch.Digest, Phase: controlplane.PhaseExecute, State: controlplane.JobQueued, InputDigest: integrationDigest(id), CreatedAt: created, AvailableAt: created, Deadline: now.Add(time.Hour)})
		if _, err := store.Enqueue(ctx, job); err != nil {
			t.Fatal(err)
		}
	}
	codec, _ := controlplane.NewCursorCodec([]byte("0123456789abcdef0123456789abcdef"))
	queryNow := now.Add(time.Second)
	first, err := store.ListJobs(ctx, controlplane.JobQuery{ProjectID: epoch.ProjectID, Limit: 1, Now: queryNow, CursorTTL: time.Hour}, codec)
	if err != nil || len(first.Jobs) != 1 || first.NextCursor == "" {
		t.Fatal("first page", err)
	}
	late, _ := controlplane.SealJob(controlplane.Job{JobID: "query-late", ProjectID: epoch.ProjectID, RunID: epoch.RunID, BackendEpoch: 1, BindingDigest: epoch.Digest, Phase: controlplane.PhaseExecute, State: controlplane.JobQueued, InputDigest: integrationDigest("late"), CreatedAt: queryNow.Add(time.Second), AvailableAt: queryNow.Add(time.Second), Deadline: queryNow.Add(time.Hour)})
	if _, err = store.Enqueue(ctx, late); err != nil {
		t.Fatal(err)
	}
	second, err := store.ListJobs(ctx, controlplane.JobQuery{ProjectID: epoch.ProjectID, Limit: 10, Now: queryNow, CursorTTL: time.Hour, Cursor: first.NextCursor}, codec)
	if err != nil || len(second.Jobs) != 1 || second.Jobs[0].JobID == late.JobID {
		t.Fatal("snapshot page", err)
	}
	claims, err := store.Claim(ctx, controlplane.ClaimRequest{ProjectID: epoch.ProjectID, WorkerID: "retention-worker", Now: queryNow.Add(2 * time.Second), Lease: time.Minute, Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	for _, job := range claims {
		if job.JobID == "query-a" || job.JobID == "query-b" {
			if _, err = store.Complete(ctx, job.JobID, job.Owner, job.Fence, job.CancelRevision, queryNow.Add(3*time.Second), controlplane.JobSucceeded, ""); err != nil {
				t.Fatal(err)
			}
		}
	}
	otherEpoch, _ := controlplane.SealBackendEpoch(controlplane.BackendEpoch{RunID: "query-other-run", ProjectID: "project-2", Epoch: 1, Mode: controlplane.BackendPostgres, BackendIdentity: "pg-test", SchemaVersion: 1, CreatedAt: now})
	if _, err = store.ReserveInitial(ctx, otherEpoch); err != nil {
		t.Fatal(err)
	}
	otherJob, _ := controlplane.SealJob(controlplane.Job{JobID: "query-other", ProjectID: otherEpoch.ProjectID, RunID: otherEpoch.RunID, BackendEpoch: 1, BindingDigest: otherEpoch.Digest, Phase: controlplane.PhaseExecute, State: controlplane.JobQueued, InputDigest: integrationDigest("other"), CreatedAt: now, AvailableAt: now, Deadline: now.Add(time.Hour)})
	if _, err = store.Enqueue(ctx, otherJob); err != nil {
		t.Fatal(err)
	}
	otherClaims, claimErr := store.Claim(ctx, controlplane.ClaimRequest{ProjectID: otherEpoch.ProjectID, WorkerID: "other-retention-worker", Now: queryNow.Add(2 * time.Second), Lease: time.Minute, Limit: 1})
	if claimErr != nil || len(otherClaims) != 1 {
		t.Fatal("other project claim", claimErr)
	}
	if _, err = store.Complete(ctx, otherClaims[0].JobID, otherClaims[0].Owner, otherClaims[0].Fence, otherClaims[0].CancelRevision, queryNow.Add(3*time.Second), controlplane.JobSucceeded, ""); err != nil {
		t.Fatal(err)
	}
	if _, err = store.pool.Exec(ctx, `INSERT INTO retention_references(project_id,job_id,reference_kind,reference_id,live) VALUES($1,'query-b','evidence','evidence-1',true)`, epoch.ProjectID); err != nil {
		t.Fatal(err)
	}
	deleted, err := store.DeleteProjectTerminalBefore(ctx, epoch.ProjectID, queryNow.Add(time.Hour).Unix(), 1)
	if err != nil || deleted != 1 {
		t.Fatal("retention", deleted, err)
	}
	var summaries int
	if err = store.pool.QueryRow(ctx, `SELECT count(*) FROM retention_summaries WHERE job_id='query-a'`).Scan(&summaries); err != nil || summaries != 1 {
		t.Fatal("summary", err)
	}
	if len(observer.observations) == 0 {
		t.Fatal("observer missing")
	}
	var otherCount int
	if err = store.pool.QueryRow(ctx, `SELECT count(*) FROM jobs WHERE job_id='query-other'`).Scan(&otherCount); err != nil || otherCount != 1 {
		t.Fatal("project-scoped retention crossed boundary", err)
	}
	if deleted, err = store.DeleteProjectTerminalBefore(ctx, epoch.ProjectID, queryNow.Add(time.Hour).Unix(), 10); err != nil || deleted != 0 {
		t.Fatal("live reference deleted", deleted, err)
	}
	if _, err = store.pool.Exec(ctx, `UPDATE retention_references SET live=false WHERE job_id='query-b'`); err != nil {
		t.Fatal(err)
	}
	if deleted, err = store.DeleteProjectTerminalBefore(ctx, epoch.ProjectID, queryNow.Add(time.Hour).Unix(), 10); err != nil || deleted != 1 {
		t.Fatal("released reference retained", deleted, err)
	}
	attempts := 0
	if err = retryTransactionObserved(ctx, store.pool, 2, "retry.canary", observer, func(pgx.Tx) error {
		attempts++
		if attempts == 1 {
			return &pgconn.PgError{Code: "40001", Message: "secret-canary"}
		}
		return nil
	}); err != nil || attempts != 2 {
		t.Fatal("bounded retry", attempts, err)
	}
}
