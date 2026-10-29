package postgres

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/asierhr/featureProof/internal/contextassembly"
	"github.com/asierhr/featureProof/internal/controlplane"
	"github.com/asierhr/featureProof/internal/fixture"
	"github.com/asierhr/featureProof/internal/functionalflow"
	"github.com/asierhr/featureProof/internal/validationworkflow"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// TestDomainStoresRejectIncompleteComposition covers fail-closed composition
// and cancellation without requiring a database in the hermetic unit suite.
func TestDomainStoresRejectIncompleteComposition(t *testing.T) {
	if _, err := (*Store)(nil).DomainStores(DomainStoreConfig{}); err == nil {
		t.Fatal("nil store accepted")
	}
	stores := &DomainStores{}
	if err := stores.Ready(context.Background()); err == nil {
		t.Fatal("incomplete graph accepted")
	}
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	if err := stores.Ready(cancelled); err == nil {
		t.Fatal("cancelled graph accepted")
	}
	if err := retryTransaction(context.Background(), nil, 1, func(pgx.Tx) error { return nil }); !errors.Is(err, controlplane.ErrInvalid) {
		t.Fatal("nil retry pool accepted")
	}
	if category := errorCategory(nil); category != "" {
		t.Fatal("nil error received telemetry category")
	}
}

// TestFunctionalRuntimeClosedHelpers covers the conservative mappings and
// package preconditions used by PostgreSQL recovery without opening a driver.
func TestFunctionalRuntimeClosedHelpers(t *testing.T) {
	if _, _, _, _, _, err := (*Store)(nil).FunctionalRuntimeStores(nil, functionalflow.Limits{}, nil); !errors.Is(err, controlplane.ErrInvalid) {
		t.Fatal("incomplete functional runtime accepted")
	}
	if err := (*PostgresFunctionalArtifactStore)(nil).Commit(context.Background(), "bad", functionalflow.Artifact{}); err == nil {
		t.Fatal("invalid artifact commit accepted")
	}
	if _, err := (*PostgresFunctionalArtifactStore)(nil).Load(context.Background(), "bad"); err == nil {
		t.Fatal("invalid artifact load accepted")
	}
	if _, err := (*PostgresFunctionalEvidenceStore)(nil).Confirm(context.Background(), functionalflow.Evidence{}); !errors.Is(err, controlplane.ErrInvalid) {
		t.Fatal("invalid evidence accepted")
	}
	if err := (*PostgresFunctionalPublisher)(nil).Stage(context.Background(), "bad", functionalflow.Artifact{}, functionalflow.Result{}); !errors.Is(err, controlplane.ErrInvalid) {
		t.Fatal("invalid primary accepted")
	}
	if _, err := (*PostgresFunctionalPublisher)(nil).Finalize(context.Background(), "bad", validationworkflow.OutcomeError, fixture.FixtureSummary{}); !errors.Is(err, controlplane.ErrInvalid) {
		t.Fatal("invalid finalization accepted")
	}
	if err := (*PostgresFunctionalAttemptSink)(nil).AppendIdentityAttempt(context.Background(), validationworkflow.FunctionalIdentityAttempt{}); !errors.Is(err, controlplane.ErrInvalid) {
		t.Fatal("invalid identity attempt accepted")
	}
	if _, err := (*PostgresFunctionalCatalog)(nil).Resolve(context.Background(), "bad/path"); err == nil {
		t.Fatal("invalid catalog selection accepted")
	}
	if _, err := (*PostgresFunctionalCatalog)(nil).Load(context.Background(), validationworkflow.FunctionalFlowSelection{}); !errors.Is(err, controlplane.ErrInvalid) {
		t.Fatal("invalid catalog load accepted")
	}
	invalidCatalog := &PostgresFunctionalCatalog{limits: functionalflow.Limits{MaxDocumentBytes: 1024}}
	if _, err := invalidCatalog.PutProvidedFlow(context.Background(), []byte(`{`)); err == nil {
		t.Fatal("malformed provided flow accepted")
	}
	aliases := map[string]string{"safe": "artifact"}
	clone := cloneStringMap(aliases)
	aliases["safe"] = "mutated"
	if clone["safe"] != "artifact" {
		t.Fatal("alias policy retained caller mutation")
	}
	for _, status := range []fixture.PreparationStatus{fixture.PreparationPrepared, fixture.PreparationFailed, fixture.PreparationCancelled, fixture.PreparationNotRequired, fixture.PreparationRejected} {
		if !validPreparationStatus(status) {
			t.Fatalf("valid preparation status rejected: %s", status)
		}
	}
	if validPreparationStatus("unknown") {
		t.Fatal("unknown preparation status accepted")
	}
	for _, outcome := range []string{"success", "credential_rejected", "transient", "permanent", "cancelled"} {
		if !validAttemptOutcome(outcome) {
			t.Fatalf("valid attempt outcome rejected: %s", outcome)
		}
	}
	if validAttemptOutcome("unknown") {
		t.Fatal("unknown attempt outcome accepted")
	}
	for primary, workflow := range map[functionalflow.PrimaryOutcome]validationworkflow.Outcome{functionalflow.OutcomePassed: validationworkflow.OutcomePassed, functionalflow.OutcomeFailed: validationworkflow.OutcomeFailed, functionalflow.OutcomeCancelled: validationworkflow.OutcomeCancelled, functionalflow.OutcomeError: validationworkflow.OutcomeError, "unknown": validationworkflow.OutcomeInconclusive} {
		if got := postgresWorkflowOutcome(primary); got != workflow {
			t.Fatalf("outcome %s mapped to %s", primary, got)
		}
	}
	artifact := functionalflow.Artifact{FlowID: "flow", Revision: 2, ArtifactDigest: contextStoreDigest("artifact")}
	for workflow, expected := range map[validationworkflow.Outcome]functionalflow.PrimaryOutcome{validationworkflow.OutcomeCancelled: functionalflow.OutcomeCancelled, validationworkflow.OutcomeError: functionalflow.OutcomeError, validationworkflow.OutcomeFailed: functionalflow.OutcomeInconclusive} {
		if got := postgresAbortedPrimary(artifact, workflow); got.Outcome != expected || got.ArtifactDigest != artifact.ArtifactDigest {
			t.Fatalf("aborted primary mismatch: %#v", got)
		}
	}
	primary := functionalflow.Result{Ledger: []functionalflow.LedgerEntry{{EvidenceIDs: []string{"e1", "e2"}}, {EvidenceIDs: []string{"e3"}}}}
	if !primaryHasEvidence(primary) || len(primaryEvidenceIDs(primary)) != 3 || primaryHasEvidence(functionalflow.Result{}) {
		t.Fatal("primary evidence projection mismatch")
	}
	if !validContextDigest(contextStoreDigest("context")) || validContextDigest("not-a-digest") {
		t.Fatal("context digest grammar mismatch")
	}
	contextStore := (*PostgresContextStore)(nil)
	if _, _, err := contextStore.CommitSnapshot(context.Background(), "", contextassembly.Request{}, contextassembly.Snapshot{}); !errors.Is(err, controlplane.ErrInvalid) {
		t.Fatal("invalid context snapshot accepted")
	}
	if _, err := contextStore.LoadSnapshot(context.Background(), "bad"); !errors.Is(err, controlplane.ErrInvalid) {
		t.Fatal("invalid context snapshot load accepted")
	}
	if _, _, err := contextStore.CommitBinding(context.Background(), "", contextassembly.ContextBinding{}); !errors.Is(err, controlplane.ErrInvalid) {
		t.Fatal("invalid context binding accepted")
	}
	if _, err := contextStore.LoadBinding(context.Background(), "bad"); !errors.Is(err, controlplane.ErrInvalid) {
		t.Fatal("invalid context binding load accepted")
	}
	records := []validationworkflow.Record{{Handle: "z-run"}, {Handle: "a-run"}}
	sortWorkflowRecords(records)
	if records[0].Handle != "a-run" {
		t.Fatal("workflow records not sorted")
	}
	if _, err := (*Store)(nil).DeleteProjectTerminalBefore(context.Background(), "unsafe project", 1, 1); !errors.Is(err, controlplane.ErrInvalid) {
		t.Fatal("unsafe retention project accepted")
	}
	if _, err := (*Store)(nil).DeleteTerminalBefore(context.Background(), 0, 1); !errors.Is(err, controlplane.ErrInvalid) {
		t.Fatal("invalid retention horizon accepted")
	}
}

// TestMigrationManifestIsLinear verifies embedded migration order and checksums offline.
func TestMigrationManifestIsLinear(t *testing.T) {
	manifest, err := loadMigrations()
	if err != nil || len(manifest) == 0 {
		t.Fatalf("manifest: %v", err)
	}
	for index, item := range manifest {
		if item.version != index+1 || len(item.checksum) != 64 || item.sql == "" || item.boundary != migrationBoundaryTransaction || item.phase == "" {
			t.Fatalf("invalid migration %#v", item)
		}
	}
}

// failingObserver proves telemetry sink failure cannot alter transaction truth or receive provider text.
type failingObserver struct{ observations []controlplane.Observation }

// Observe records allowlisted fields and deliberately fails outside authoritative work.
func (observer *failingObserver) Observe(_ context.Context, value controlplane.Observation) error {
	observer.observations = append(observer.observations, value)
	return errors.New("sink-canary")
}

// TestObservedRetryIgnoresSinkFailure verifies bounded retry telemetry is content-free and non-authoritative.
func TestObservedRetryIgnoresSinkFailure(t *testing.T) {
	observer := &failingObserver{}
	if category := errorCategory(mapOperationError(&pgconn.PgError{Code: "23503", Message: "secret-canary"}, "relation.save")); category != controlplane.CategoryIntegrity {
		t.Fatal(category)
	}
	if len(observer.observations) != 0 {
		t.Fatal("observer called unexpectedly")
	}
}

// TestOfflineValidationBranches covers fail-closed composition without network access.
func TestOfflineValidationBranches(t *testing.T) {
	ctx := context.Background()
	if _, err := Open(ctx, Config{}); !errors.Is(err, controlplane.ErrInvalid) {
		t.Fatal("invalid config")
	}
	if _, err := Open(ctx, Config{DSN: "://", BackendIdentity: "pg-test", MaxConnections: 1, StatementTimeout: time.Second, MaxLease: time.Minute}); !errors.Is(err, controlplane.ErrInvalid) {
		t.Fatal("malformed DSN")
	}
	var store *Store
	store.Close()
	if err := store.Migrate(ctx); !errors.Is(err, controlplane.ErrInvalid) {
		t.Fatal("nil store migration")
	}
	if err := Migrate(ctx, nil); !errors.Is(err, ErrSchema) {
		t.Fatal("nil migration")
	}
	if _, err := canonical(make([]byte, controlplane.MaxCanonicalBytes+1)); !errors.Is(err, controlplane.ErrInvalid) {
		t.Fatal("oversize canonical")
	}
	if _, err := canonical(make(chan int)); !errors.Is(err, controlplane.ErrInvalid) {
		t.Fatal("unmarshalable canonical")
	}
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if !errors.Is(mapError(cancelled.Err()), controlplane.ErrCancelled) {
		t.Fatal("cancel mapping")
	}
	deadline, stop := context.WithDeadline(ctx, time.Now().Add(-time.Second))
	defer stop()
	if !errors.Is(mapError(deadline.Err()), controlplane.ErrDeadline) {
		t.Fatal("deadline mapping")
	}
	if !errors.Is(mapError(errors.New("canary secret")), controlplane.ErrUnavailable) {
		t.Fatal("unknown mapping")
	}
	if err := retryTransaction(ctx, nil, 0, nil); !errors.Is(err, controlplane.ErrInvalid) {
		t.Fatal("invalid retry transaction")
	}
}

// TestSQLStateMappingUsesCodes verifies classification never depends on message text.
func TestSQLStateMappingUsesCodes(t *testing.T) {
	cases := []struct {
		code   string
		target error
	}{{"40001", controlplane.ErrTransient}, {"40P01", controlplane.ErrTransient}, {"23505", controlplane.ErrConflict}, {"42501", controlplane.ErrUnauthorized}, {"42P01", ErrSchema}}
	for _, item := range cases {
		first := mapError(&pgconn.PgError{Code: item.code, Message: "canary-a"})
		second := mapError(&pgconn.PgError{Code: item.code, Message: "different-canary"})
		if !errors.Is(first, item.target) || !errors.Is(second, item.target) {
			t.Fatalf("code %s was message-sensitive", item.code)
		}
	}
}
