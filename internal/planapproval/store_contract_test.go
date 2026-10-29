package planapproval

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/asierhr/featureProof/internal/harness"
)

// approvalStoreContractFactory reopens the same durable backend without
// exposing filesystem or SQL implementation details to the shared suite.
type approvalStoreContractFactory func() (ApprovalStore, error)

// runApprovalStoreContract exercises the exact consumer-owned snapshot and
// journal semantics against every durable backend implementation.
func runApprovalStoreContract(t *testing.T, store ApprovalStore, reopen approvalStoreContractFactory, runID string) {
	t.Helper()
	ctx := context.Background()
	run, err := harness.NewRun(runID, "shared approval contract", "constraint", harness.Limits{MaxSteps: 1, MaxToolCalls: 1, MaxRetries: 1})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = store.BindRun(context.Background(), run); err != nil {
		t.Fatal(err)
	}
	plan, contract := approvalFixture(t)
	contextHashes, err := fingerprintContext(ctx, contract, testLimits())
	if err != nil {
		t.Fatal(err)
	}
	request := approvalRequest{runID: run.ID, planID: "plan-shared", planHash: strings.Repeat("a", 64), viewHash: strings.Repeat("b", 64), contextHashes: contextHashes, revision: 1, plan: plan, context: contract, view: ApprovalView{RunID: run.ID, PlanID: "plan-shared", Revision: 1}}
	if err = store.SaveRequest(ctx, request); err != nil {
		t.Fatal(err)
	}
	if loaded, loadErr := store.Request(ctx, run.ID, request.revision); loadErr != nil || loaded.planHash != request.planHash {
		t.Fatal("snapshot round trip", loadErr)
	}
	event := ApprovalEvent{Version: 1, Revision: 1, EventID: eventID(run.ID, 1, EventApprovalRequested), Type: EventApprovalRequested, RunID: run.ID, PlanID: request.planID, PlanHash: request.planHash, ContextHash: request.contextHashes.All, ViewHash: request.viewHash, OccurredAt: time.Now().UTC()}
	appended, replayed, err := store.CompareAndAppend(ctx, run.ID, 0, "", event)
	if err != nil || replayed || appended.Sequence != 1 {
		t.Fatal("append", replayed, err)
	}
	decision := event
	decision.Type, decision.EventID, decision.DecisionID, decision.Actor, decision.CommandFingerprint = EventApproved, eventID(run.ID, 1, EventApproved), run.ID+"-decision", &Actor{Subject: "operator", Source: "contract"}, strings.Repeat("c", 64)
	if _, replayed, err = store.CompareAndAppend(ctx, run.ID, 1, decision.DecisionID, decision); err != nil || replayed {
		t.Fatal("decision append", replayed, err)
	}
	if _, replayed, err = store.CompareAndAppend(ctx, run.ID, 2, decision.DecisionID, decision); err != nil || !replayed {
		t.Fatal("replay", replayed, err)
	}
	conflict := decision
	conflict.CommandFingerprint = strings.Repeat("d", 64)
	if _, _, err = store.CompareAndAppend(ctx, run.ID, 2, decision.DecisionID, conflict); err == nil {
		t.Fatal("decision conflict accepted")
	}
	restarted, err := reopen()
	if err != nil {
		t.Fatal(err)
	}
	if events, eventsErr := restarted.Events(ctx, run.ID); eventsErr != nil || len(events) != 2 || events[0].EventID != event.EventID || events[1].EventID != decision.EventID {
		t.Fatal("restart journal", eventsErr)
	}
	if loaded, loadErr := restarted.Request(ctx, run.ID, request.revision); loadErr != nil || loaded.planHash != request.planHash {
		t.Fatal("restart snapshot", loadErr)
	}
}

// TestLocalApprovalStoreSharedContract runs the same durable consumer contract
// used by the tagged PostgreSQL adapter suite.
func TestLocalApprovalStoreSharedContract(t *testing.T) {
	root := t.TempDir()
	store, err := NewLocalStore(root)
	if err != nil {
		t.Fatal(err)
	}
	runApprovalStoreContract(t, store, func() (ApprovalStore, error) { return NewLocalStore(root) }, "local-shared-approval")
}
