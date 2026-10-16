package replanning

import (
	"context"
	"strings"
	"sync"
	"testing"

	"github.com/asierhr/featureProof/internal/agentplanning"
	"github.com/asierhr/featureProof/internal/planning"
)

// TestLedgerReplayIsIdempotentAndConflictFailsClosed confirms monotonic failed assertions.
func TestLedgerReplayIsIdempotentAndConflictFailsClosed(t *testing.T) {
	l, _ := NewAssertionLedger(4)
	id := AssertionIdentity{RunID: "r", BatchID: "b", CheckID: "c", StepID: "s", DeclarationID: "a", NodePath: "root"}
	entry := ConfirmedAssertion{Identity: id, Status: planning.AssertionFailed, Required: true, EvidenceIDs: []string{"e"}}
	if err := l.Confirm(context.Background(), entry); err != nil {
		t.Fatal(err)
	}
	if err := l.Confirm(context.Background(), entry); err != nil {
		t.Fatal(err)
	}
	entry.Status = planning.AssertionPassed
	if err := l.Confirm(context.Background(), entry); err == nil {
		t.Fatal("failed result overwritten")
	}
	if len(l.Entries()) != 1 || l.Verdict([]AssertionIdentity{id}) != VerdictFailed {
		t.Fatal("ledger changed")
	}
}

// TestConcurrentCycleHasOneConfirmedWinner verifies CAS ownership before any model callback.
func TestConcurrentCycleHasOneConfirmedWinner(t *testing.T) {
	j := NewMemoryJournal()
	var wg sync.WaitGroup
	results := make(chan bool, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, replayed, err := j.CompareAndAppend(context.Background(), "r", 0, "cycle", Event{Version: 1, RunID: "r", Cycle: 1, Type: EventCycleReserved})
			results <- err == nil && !replayed
		}()
	}
	wg.Wait()
	close(results)
	wins := 0
	for won := range results {
		if won {
			wins++
		}
	}
	if wins != 1 {
		t.Fatalf("wins=%d", wins)
	}
	events, _ := j.Events(context.Background(), "r")
	if len(events) != 1 {
		t.Fatal("duplicate cycle")
	}
	if _, _, err := j.CompareAndAppend(context.Background(), "r", 0, "cycle", Event{Version: 1, RunID: "r", Cycle: 2, Type: EventCycleReserved}); err == nil {
		t.Fatal("changed idempotency key accepted")
	}
}

// TestVerdictUsesOnlyCompleteAssertionLedger proves missing evidence stays inconclusive and later required failures dominate.
func TestVerdictUsesOnlyCompleteAssertionLedger(t *testing.T) {
	l, _ := NewAssertionLedger(8)
	missing := DeclarationIdentity{RunID: "r", BatchID: "b", CheckID: "c", StepID: "s", DeclarationID: "missing"}
	failed := DeclarationIdentity{RunID: "r", BatchID: "b", CheckID: "c", StepID: "s", DeclarationID: "failed"}
	if l.VerdictRequired([]DeclarationIdentity{missing}) != VerdictInconclusive {
		t.Fatal("missing passed")
	}
	if err := l.Confirm(context.Background(), ConfirmedAssertion{Identity: AssertionIdentity{RunID: "r", BatchID: "b", CheckID: "c", StepID: "s", DeclarationID: "failed", NodePath: "/assertions/failed"}, Status: planning.AssertionFailed, EvidenceIDs: []string{"e"}, Required: true}); err != nil {
		t.Fatal(err)
	}
	if l.VerdictRequired([]DeclarationIdentity{missing, failed}) != VerdictFailed {
		t.Fatal("confirmed failure lost")
	}
}

// TestExactNodeVerdictRequiresConfirmedEvidence checks the node-level verdict used for replay diagnostics.
func TestExactNodeVerdictRequiresConfirmedEvidence(t *testing.T) {
	l, _ := NewAssertionLedger(4)
	id := AssertionIdentity{RunID: "r", BatchID: "b", CheckID: "c", StepID: "s", DeclarationID: "a", NodePath: "/a"}
	if l.Verdict(nil) != VerdictInconclusive || l.Verdict([]AssertionIdentity{id}) != VerdictInconclusive {
		t.Fatal("missing node passed")
	}
	entry := ConfirmedAssertion{Identity: id, Status: planning.AssertionPassed}
	if err := l.Confirm(context.Background(), entry); err != nil {
		t.Fatal(err)
	}
	if l.Verdict([]AssertionIdentity{id}) != VerdictInconclusive {
		t.Fatal("passing node without evidence passed")
	}
	other := id
	other.NodePath = "/b"
	if err := l.Confirm(context.Background(), ConfirmedAssertion{Identity: other, Status: planning.AssertionPassed, EvidenceIDs: []string{"e"}}); err != nil {
		t.Fatal(err)
	}
	if l.Verdict([]AssertionIdentity{other}) != VerdictPassed || l.Verdict([]AssertionIdentity{other, id}) != VerdictInconclusive {
		t.Fatal("incomplete node verdict")
	}
}

// TestAnyGroupUsesRootVerdictPreservingFailedChild verifies a satisfied group keeps its child audit without failing the declaration.
func TestAnyGroupUsesRootVerdictPreservingFailedChild(t *testing.T) {
	l, _ := NewAssertionLedger(4)
	root := AssertionIdentity{RunID: "r", BatchID: "b", CheckID: "c", StepID: "s", DeclarationID: "a", NodePath: "/a"}
	child := root
	child.NodePath = "/a/children/1"
	if err := l.Confirm(context.Background(), ConfirmedAssertion{Identity: root, Status: planning.AssertionPassed, EvidenceIDs: []string{"e"}, Required: true}); err != nil {
		t.Fatal(err)
	}
	if err := l.Confirm(context.Background(), ConfirmedAssertion{Identity: child, Status: planning.AssertionFailed, EvidenceIDs: []string{"e"}, Required: false}); err != nil {
		t.Fatal(err)
	}
	want := DeclarationIdentity{RunID: "r", BatchID: "b", CheckID: "c", StepID: "s", DeclarationID: "a"}
	if l.VerdictRequired([]DeclarationIdentity{want}) != VerdictPassed || len(l.Entries()) != 2 {
		t.Fatal("any group child overrode passing root")
	}
}

// TestLineageReconstructsDecisionEvidenceAndActions verifies strict versioned cycle projection.
func TestLineageReconstructsDecisionEvidenceAndActions(t *testing.T) {
	j := NewMemoryJournal()
	ctx := context.Background()
	digest := strings.Repeat("a", 64)
	digest2 := strings.Repeat("b", 64)
	fingerprint := strings.Repeat("c", 64)
	events := []Event{
		{Version: 1, RunID: "r", Cycle: 1, Type: EventCycleReserved},
		{Version: 1, RunID: "r", Cycle: 1, Type: EventSummaryConfirmed, SummaryDigest: digest},
		{Version: 1, RunID: "r", Cycle: 1, Type: EventDecisionReceived, DecisionID: "d", SummaryDigest: digest},
		{Version: 1, RunID: "r", Cycle: 1, Type: EventProposalPrepared, DecisionID: "d", ProposalFingerprint: fingerprint, BatchID: "b", PlanID: "p"},
		{Version: 1, RunID: "r", Cycle: 1, Type: EventBatchStarted, DecisionID: "d", BatchID: "b", PlanID: "p"},
		{Version: 1, RunID: "r", Cycle: 1, Type: EventBatchCompleted, DecisionID: "d", BatchID: "b", NewEvidenceIDs: []string{"e"}},
		assertionsEvent("r", 1, EventAssertionsConfirmed, "d", "b", nil, []ConfirmedAssertion{{Identity: AssertionIdentity{RunID: "r", BatchID: "b", CheckID: "c", StepID: "s", DeclarationID: "a", NodePath: "/a"}, Status: planning.AssertionPassed, EvidenceIDs: []string{"e"}, Required: true}}),
		{Version: 1, RunID: "r", Cycle: 2, Type: EventCycleReserved, ParentDecisionID: "d"},
		{Version: 1, RunID: "r", Cycle: 2, Type: EventSummaryConfirmed, SummaryDigest: digest2, ObservedEvidenceIDs: []string{"e"}},
		{Version: 1, RunID: "r", Cycle: 2, Type: EventDecisionReceived, DecisionID: "d2", ParentDecisionID: "d", SummaryDigest: digest2, ObservedEvidenceIDs: []string{"e"}},
		{Version: 1, RunID: "r", Cycle: 2, Type: EventTerminated, Verdict: VerdictPassed},
	}
	for i, e := range events {
		if _, _, err := j.CompareAndAppend(ctx, "r", uint64(i), e.Type.String()+string(rune('a'+i)), e); err != nil {
			t.Fatal(err)
		}
	}
	confirmed, _ := j.Events(ctx, "r")
	p, err := Project(ctx, confirmed, 32)
	if err != nil {
		t.Fatal(err)
	}
	if !p.Terminal || p.Verdict != VerdictPassed || !p.Fingerprints[fingerprint] {
		t.Fatal("projection incomplete")
	}
	confirmed[2].SummaryDigest = "fork"
	if _, err := Project(ctx, confirmed, 32); err == nil {
		t.Fatal("fork accepted")
	}
	confirmed, _ = j.Events(ctx, "r")
	confirmed[len(confirmed)-1].Verdict = VerdictFailed
	if _, err := Project(ctx, confirmed, 32); err == nil {
		t.Fatal("terminal verdict contradicted confirmed assertion facts")
	}
}

// String exposes an event type only for stable local test keys.
func (e EventType) String() string { return string(e) }

// TestEquivalentAdditionalBatchesAreRejectedBeforeApprovalAndTools proves decorative check IDs do not evade replay.
func TestEquivalentAdditionalBatchesAreRejectedBeforeApprovalAndTools(t *testing.T) {
	check := agentplanning.CandidateCheck{ID: "one", ConstraintID: "c", Scenario: agentplanning.ScenarioPositive, Capability: "http", Steps: []planning.PlanStep{{ID: "s", Tool: "http", Input: map[string]planning.InputValue{}, Assertions: []planning.AssertionDeclaration{{ID: "a", Definition: map[string]any{"operator": "exists", "path": "/x"}}}}}, Evidence: []agentplanning.EvidenceRequirement{{StepID: "s", AssertionIDs: []string{"a"}}}}
	one, err := Fingerprint(context.Background(), AdditionalBatch{BatchID: "b1", Checks: []agentplanning.CandidateCheck{check}}, 10000)
	if err != nil {
		t.Fatal(err)
	}
	check.ID = "two"
	check.Steps[0].ID = "renamed"
	check.Steps[0].Assertions[0].ID = "z"
	check.Evidence[0].StepID = "renamed"
	check.Evidence[0].AssertionIDs = []string{"z"}
	two, err := Fingerprint(context.Background(), AdditionalBatch{BatchID: "b2", Checks: []agentplanning.CandidateCheck{check}}, 10000)
	if err != nil {
		t.Fatal(err)
	}
	if one != two {
		t.Fatal("decorative rename changed fingerprint")
	}
	seen := NewFingerprintSet()
	if err := seen.Reserve(one); err != nil {
		t.Fatal(err)
	}
	if err := seen.Reserve(two); err == nil {
		t.Fatal("duplicate admitted")
	}
	check.Capability = "other"
	three, err := Fingerprint(context.Background(), AdditionalBatch{Checks: []agentplanning.CandidateCheck{check}}, 10000)
	if err != nil || three == one {
		t.Fatal("semantic change ignored")
	}
}

// TestFingerprintChargesEachVisitBeforeCanonicalization verifies a run budget can stop traversal at its first unit.
func TestFingerprintChargesEachVisitBeforeCanonicalization(t *testing.T) {
	checks := []agentplanning.CandidateCheck{{ID: "c", ConstraintID: "x"}}
	visits := 0
	_, err := FingerprintWithBudget(context.Background(), AdditionalBatch{Checks: checks}, 100, func(units int) error {
		visits += units
		return failure(CategoryBudget, "reserve", "validation_work")
	})
	if err == nil || visits != 1 {
		t.Fatalf("canonicalization ignored first reservation: %d, %v", visits, err)
	}
}
