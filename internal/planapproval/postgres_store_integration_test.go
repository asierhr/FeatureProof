//go:build postgres_integration

package planapproval

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/asierhr/featureProof/internal/harness"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// postgresApprovalIntegrationPool creates a test-owned schema for the private approval adapter contract.
func postgresApprovalIntegrationPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	dsn := os.Getenv("FEATUREPROOF_POSTGRES_TEST_DSN")
	if dsn == "" {
		t.Fatal("FEATUREPROOF_POSTGRES_TEST_DSN is required")
	}
	ctx := context.Background()
	admin, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	schema := fmt.Sprintf("approval_%d", time.Now().UnixNano())
	if _, err = admin.Exec(ctx, `CREATE SCHEMA `+pgx.Identifier{schema}.Sanitize()); err != nil {
		admin.Close()
		t.Fatal(err)
	}
	separator := "?"
	if strings.Contains(dsn, "?") {
		separator = "&"
	}
	scoped, err := pgxpool.New(ctx, dsn+separator+"search_path="+url.QueryEscape(schema))
	if err != nil {
		admin.Close()
		t.Fatal(err)
	}
	ddl := `CREATE TABLE approval_runs(run_id text PRIMARY KEY, revision bigint NOT NULL DEFAULT 0);
CREATE TABLE approval_requests(run_id text NOT NULL REFERENCES approval_runs(run_id) ON DELETE CASCADE, revision bigint NOT NULL, snapshot bytea NOT NULL, snapshot_digest text NOT NULL, PRIMARY KEY(run_id,revision));
CREATE TABLE approval_events(run_id text NOT NULL REFERENCES approval_runs(run_id) ON DELETE CASCADE, revision bigint NOT NULL, event_id text NOT NULL UNIQUE, event bytea NOT NULL, PRIMARY KEY(run_id,revision));`
	if _, err = scoped.Exec(ctx, ddl); err != nil {
		scoped.Close()
		admin.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() {
		scoped.Close()
		if _, dropErr := admin.Exec(context.Background(), `DROP SCHEMA `+pgx.Identifier{schema}.Sanitize()+` CASCADE`); dropErr != nil {
			t.Errorf("drop approval schema: %v", dropErr)
		}
		admin.Close()
	})
	return scoped
}

// TestPostgresApprovalSnapshotRoundTrip guards restart decoding before any database I/O.
func TestPostgresApprovalSnapshotRoundTrip(t *testing.T) {
	plan, contract := approvalFixture(t)
	snapshot := postgresApprovalSnapshot{RunID: "roundtrip-run", PlanID: "plan-1", PlanHash: strings.Repeat("a", 64), ViewHash: strings.Repeat("b", 64), Revision: 1, Plan: plan, Context: contract, View: ApprovalView{RunID: "roundtrip-run", PlanID: "plan-1", Revision: 1}}
	data, err := json.Marshal(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	var decoded postgresApprovalSnapshot
	decoder := json.NewDecoder(bytes.NewReader(data))
	if err = decoder.Decode(&decoded); err != nil {
		t.Fatal(err)
	}
}

// TestPostgresApprovalStoreContract verifies snapshots, journal CAS, replay and restart persistence.
func TestPostgresApprovalStoreContract(t *testing.T) {
	ctx := context.Background()
	pool := postgresApprovalIntegrationPool(t)
	store, err := NewPostgresApprovalStore(pool)
	if err != nil {
		t.Fatal(err)
	}
	runApprovalStoreContract(t, store, func() (ApprovalStore, error) { return NewPostgresApprovalStore(pool) }, "postgres-shared-approval")
	run, err := harness.NewRun("postgres-approval-run", "approval persistence", "constraint", harness.Limits{MaxSteps: 1, MaxToolCalls: 1, MaxRetries: 1})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = store.BindRun(context.Background(), run); err != nil {
		t.Fatal(err)
	}
	if _, err = store.BindRun(context.Background(), run); err != nil {
		t.Fatal("bind replay", err)
	}
	otherRun, _ := harness.NewRun(run.ID, "different aggregate", "constraint", harness.Limits{MaxSteps: 1, MaxToolCalls: 1, MaxRetries: 1})
	if _, err = store.BindRun(context.Background(), otherRun); err == nil {
		t.Fatal("different aggregate identity accepted")
	}
	plan, contract := approvalFixture(t)
	request := approvalRequest{runID: run.ID, planID: "plan-1", planHash: strings.Repeat("a", 64), viewHash: strings.Repeat("b", 64), revision: 1, plan: plan, context: contract, view: ApprovalView{RunID: run.ID, PlanID: "plan-1", Revision: 1}}
	diagnosticData, _ := json.Marshal(postgresApprovalSnapshot{RunID: request.runID, PlanID: request.planID, PlanHash: request.planHash, ViewHash: request.viewHash, ContextHashes: request.contextHashes, Revision: request.revision, Plan: request.plan, Context: request.context, View: request.view})
	var diagnostic postgresApprovalSnapshot
	diagnosticDecoder := json.NewDecoder(bytes.NewReader(diagnosticData))
	if decodeErr := diagnosticDecoder.Decode(&diagnostic); decodeErr != nil {
		t.Fatalf("snapshot fixture is not round-trippable: %v", decodeErr)
	}
	if err = store.SaveRequest(ctx, request); err != nil {
		t.Fatal(err)
	}
	if err = store.SaveRequest(ctx, request); err != nil {
		t.Fatal("snapshot replay", err)
	}
	loaded, err := store.Request(ctx, run.ID, 1)
	if err != nil || loaded.planID != request.planID {
		t.Fatal("snapshot load", err)
	}
	event := ApprovalEvent{Version: 1, Revision: 1, EventID: eventID(run.ID, 1, EventApproved), DecisionID: "decision-1", Type: EventApproved, RunID: run.ID, PlanID: request.planID, PlanHash: request.planHash, ContextHash: strings.Repeat("e", 64), ViewHash: request.viewHash, OccurredAt: time.Now().UTC(), Actor: &Actor{Subject: "operator", Source: "integration"}, CommandFingerprint: strings.Repeat("c", 64)}
	appended, replayed, err := store.CompareAndAppend(ctx, run.ID, 0, event.DecisionID, event)
	if err != nil || replayed || appended.Sequence != 1 {
		t.Fatal("append", replayed, err)
	}
	if replay, wasReplay, replayErr := store.CompareAndAppend(ctx, run.ID, 1, event.DecisionID, event); replayErr != nil || !wasReplay || replay.Sequence != appended.Sequence {
		t.Fatal("decision replay", wasReplay, replayErr)
	}
	conflicting := event
	conflicting.CommandFingerprint = strings.Repeat("d", 64)
	if _, _, err = store.CompareAndAppend(ctx, run.ID, 1, event.DecisionID, conflicting); err == nil {
		t.Fatal("decision conflict accepted")
	}
	if _, _, err = store.CompareAndAppend(ctx, run.ID, 0, "decision-2", ApprovalEvent{EventID: "event-2", RunID: run.ID}); err == nil {
		t.Fatal("stale sequence accepted")
	}
	events, err := store.Events(ctx, run.ID)
	if err != nil || len(events) != 1 || events[0].EventID != event.EventID {
		t.Fatal("events", err)
	}
	restarted, err := NewPostgresApprovalStore(pool)
	if err != nil {
		t.Fatal(err)
	}
	if events, err = restarted.Events(ctx, run.ID); err != nil || len(events) != 1 {
		t.Fatal("restart", err)
	}
}

// TestPostgresApprovalStoreRejectsCorruptAndConflictingDurableState covers fail-closed snapshot and journal boundaries.
func TestPostgresApprovalStoreRejectsCorruptAndConflictingDurableState(t *testing.T) {
	ctx := context.Background()
	pool := postgresApprovalIntegrationPool(t)
	if _, err := NewPostgresApprovalStore(nil); err == nil {
		t.Fatal("nil pool accepted")
	}
	store, err := NewPostgresApprovalStore(pool)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = store.BindRun(context.Background(), nil); err == nil {
		t.Fatal("nil aggregate accepted")
	}
	run, err := harness.NewRun("postgres-approval-corrupt", "approval persistence", "constraint", harness.Limits{MaxSteps: 1, MaxToolCalls: 1, MaxRetries: 1})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = store.BindRun(context.Background(), run); err != nil {
		t.Fatal(err)
	}
	plan, contract := approvalFixture(t)
	request := approvalRequest{runID: run.ID, planID: "plan-corrupt", planHash: strings.Repeat("a", 64), viewHash: strings.Repeat("b", 64), revision: 1, plan: plan, context: contract, view: ApprovalView{RunID: run.ID, PlanID: "plan-corrupt", Revision: 1}}
	if err = store.SaveRequest(ctx, request); err != nil {
		t.Fatal(err)
	}
	conflict := request
	conflict.planHash = strings.Repeat("c", 64)
	if err = store.SaveRequest(ctx, conflict); err == nil {
		t.Fatal("conflicting snapshot accepted")
	}
	if _, err = store.Request(ctx, run.ID, 99); err == nil {
		t.Fatal("missing snapshot accepted")
	}
	validTamper, _ := json.Marshal(postgresApprovalSnapshot{RunID: run.ID, PlanID: "tampered", PlanHash: request.planHash, ViewHash: request.viewHash, Revision: 1, Plan: request.plan, Context: request.context, View: request.view})
	if _, err = pool.Exec(ctx, `UPDATE approval_requests SET snapshot=$1 WHERE run_id=$2 AND revision=1`, validTamper, run.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = store.Request(ctx, run.ID, 1); err == nil {
		t.Fatal("valid JSON with stale snapshot digest accepted")
	}
	if _, err = pool.Exec(ctx, `UPDATE approval_requests SET snapshot=$1 WHERE run_id=$2 AND revision=1`, []byte(`{"broken":`), run.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = store.Request(ctx, run.ID, 1); err == nil {
		t.Fatal("corrupt snapshot accepted")
	}
	if _, _, err = store.CompareAndAppend(ctx, "missing-run", 0, "decision-missing", ApprovalEvent{EventID: "event-missing"}); err == nil {
		t.Fatal("missing journal aggregate accepted")
	}
	event := ApprovalEvent{Version: 1, Revision: 1, EventID: eventID(run.ID, 1, EventApproved), DecisionID: "decision-corrupt", Type: EventApproved, RunID: run.ID, PlanID: request.planID, PlanHash: request.planHash, ContextHash: strings.Repeat("e", 64), ViewHash: request.viewHash, OccurredAt: time.Now().UTC(), Actor: &Actor{Subject: "operator", Source: "integration"}, CommandFingerprint: strings.Repeat("d", 64)}
	if _, _, err = store.CompareAndAppend(ctx, run.ID, 0, event.DecisionID, event); err != nil {
		t.Fatal(err)
	}
	if _, err = pool.Exec(ctx, `UPDATE approval_events SET event=$1 WHERE run_id=$2`, []byte(`{"broken":`), run.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = store.Events(ctx, run.ID); err == nil {
		t.Fatal("corrupt journal accepted")
	}
	if _, _, err = store.CompareAndAppend(ctx, run.ID, 1, event.DecisionID, event); err == nil {
		t.Fatal("corrupt replay accepted")
	}
}
