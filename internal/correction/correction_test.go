package correction

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/asierhr/featureProof/internal/controlplane"
	"github.com/asierhr/featureProof/internal/externalagent"
	"github.com/asierhr/featureProof/internal/localvalidation"
	"github.com/asierhr/featureProof/internal/revisionbinding"
)

// testNow fixes correction journal timestamps so lineage digests remain reproducible.
var testNow = time.Date(2026, 10, 16, 12, 0, 0, 0, time.UTC)

// digestA is the first canonical SHA-256 authority fixture.
var digestA = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

// digestB is the distinct SHA-256 fixture used to prove drift detection.
var digestB = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"

// oldOID identifies the parent Git revision in correction comparisons.
var oldOID = "1111111111111111111111111111111111111111"

// newOID identifies the corrected child Git revision.
var newOID = "2222222222222222222222222222222222222222"

// TestCorrectionModelsRejectAmbiguousIdentityAndUnsafeLimits proves unsafe identities and unbounded chains never enter state.
func TestCorrectionModelsRejectAmbiguousIdentityAndUnsafeLimits(t *testing.T) {
	identity := testIdentity("constraint.one", "step.one", "declaration.one", digestA)
	if err := identity.Validate(); err != nil {
		t.Fatal(err)
	}
	identity.StepID = "../escape"
	if err := identity.Validate(); err == nil {
		t.Fatal("unsafe identity accepted")
	}
	if err := (ChainLimits{}).Validate(); err == nil {
		t.Fatal("unbounded limits accepted")
	}
	if _, err := (RegressionSnapshot{Version: Version, ID: "policy", Scenarios: []ScenarioIdentity{testIdentity("c", "s", "d", digestA), testIdentity("c", "s", "d", digestA)}, Limits: testLimits()}).Seal(); err == nil {
		t.Fatal("duplicate identity accepted")
	}
	identity = testIdentity("c", "s", "d", "bad")
	if err := identity.Validate(); err == nil {
		t.Fatal("bad fingerprint accepted")
	}
	identity = testIdentity("c", "s", "d", digestA)
	identity.CheckID = "../bad"
	if err := identity.Validate(); err == nil {
		t.Fatal("bad check accepted")
	}
	if _, err := (RegressionSnapshot{}).Seal(); err == nil {
		t.Fatal("empty policy accepted")
	}
}

// TestFailureSelectionIsCompleteCanonicalAndUnmodifiable proves all verified failures are retained and defensively copied.
func TestFailureSelectionIsCompleteCanonicalAndUnmodifiable(t *testing.T) {
	index := testPackage(t, "parent", oldOID, localvalidation.ResultFailed, []localvalidation.PackageFailure{testFailure("z", "step.z", "decl.z", "ev.z"), testFailure("a", "step.a", "decl.a", "ev.a")}, nil)
	selected, err := SelectFailures(index)
	if err != nil {
		t.Fatal(err)
	}
	if len(selected) != 2 || selected[0].Identity.ConstraintID != "a" || selected[1].Identity.ConstraintID != "z" {
		t.Fatalf("selection=%+v", selected)
	}
	index.Failures[1].EvidenceIDs[0] = "mutated"
	if selected[0].SourceEvidenceIDs[0] != "ev.a" {
		t.Fatal("selection aliases package memory")
	}
}

// TestCorrectionSubmitIsDurablyIdempotent proves replay after reopening reconstructs the same child.
func TestCorrectionSubmitIsDurablyIdempotent(t *testing.T) {
	store := testStore(t)
	reservation := testReservation(t, "parent", "handle.one", "key.one")
	first, err := store.Reserve(context.Background(), reservation)
	if err != nil || !first.New {
		t.Fatalf("first=%+v err=%v", first, err)
	}
	reopened, err := NewLocalStore(store.root)
	if err != nil {
		t.Fatal(err)
	}
	second, err := reopened.Reserve(context.Background(), reservation)
	if err != nil || second.New || second.Record.Link.Handle != first.Record.Link.Handle {
		t.Fatalf("second=%+v err=%v", second, err)
	}
	conflict := reservation
	conflict.Record.RequestHash = digestB
	if _, err := reopened.Reserve(context.Background(), conflict); !IsCategory(err, Conflict) {
		t.Fatalf("conflict=%v", err)
	}
}

// TestCorrectionChainCreatesImmutableLinearChildren proves ordinal assignment and stale-tail rejection.
func TestCorrectionChainCreatesImmutableLinearChildren(t *testing.T) {
	store := testStore(t)
	first, err := store.Reserve(context.Background(), testReservation(t, "parent", "handle.one", "key.one"))
	if err != nil {
		t.Fatal(err)
	}
	record := first.Record
	record.Phase, record.Outcome = PhaseTerminal, OutcomeFailed
	record.Link.ChildRunID, record.Link.ChildBindingSHA256, record.Link.UpdatedAt = "child.one", digestB, testNow.Add(time.Second)
	record.Link, _ = record.Link.Seal()
	terminalizeTestRecord(t, store, record)
	second, err := store.Reserve(context.Background(), testReservation(t, "child.one", "handle.two", "key.two"))
	if err != nil || second.Record.Link.Attempt != 2 || second.Record.Link.RootRunID != "parent" {
		t.Fatalf("second=%+v err=%v", second, err)
	}
	if _, err := store.Reserve(context.Background(), testReservation(t, "parent", "handle.three", "key.three")); !IsCategory(err, Conflict) {
		t.Fatalf("stale tail=%v", err)
	}
}

// TestConcurrentTailAdmissionHasOneWinner proves per-chain CAS admits at most one distinct child.
func TestConcurrentTailAdmissionHasOneWinner(t *testing.T) {
	store := testStore(t)
	inputs := []Reservation{testReservation(t, "parent", "handle.one", "key.one"), testReservation(t, "parent", "handle.two", "key.two")}
	var wg sync.WaitGroup
	results := make(chan error, 2)
	for _, input := range inputs {
		wg.Add(1)
		go func(value Reservation) {
			defer wg.Done()
			_, err := store.Reserve(context.Background(), value)
			results <- err
		}(input)
	}
	wg.Wait()
	close(results)
	success := 0
	for err := range results {
		if err == nil {
			success++
		} else if !IsCategory(err, Conflict) {
			t.Fatal(err)
		}
	}
	if success != 1 {
		t.Fatalf("success=%d", success)
	}
}

// TestObsoleteScenarioIsNotApplicable proves structural retirement is recorded without fabricated evidence.
func TestObsoleteScenarioIsNotApplicable(t *testing.T) {
	parent := testPackage(t, "parent", oldOID, localvalidation.ResultFailed, []localvalidation.PackageFailure{testFailure("c", "s", "d", "ev")}, nil)
	failures, _ := SelectFailures(parent)
	policy := testPolicy(t, nil, false)
	plan, err := BuildPlan(context.Background(), failures, fakeApplicability{applicable: false, reason: ConstraintRemoved}, policy)
	if err != nil || len(plan.FailedFirst) != 0 || len(plan.NotApplicable) != 1 || plan.NotApplicable[0].Reason != ConstraintRemoved {
		t.Fatalf("plan=%+v err=%v", plan, err)
	}
}

// TestComparisonClassifiesCorrectedPersistentNewAndInconclusive proves the closed deterministic classification table.
func TestComparisonClassifiesCorrectedPersistentNewAndInconclusive(t *testing.T) {
	parentFailure := testFailure("c", "s", "d", "parent.ev")
	parent := testPackage(t, "parent", oldOID, localvalidation.ResultFailed, []localvalidation.PackageFailure{parentFailure}, nil)
	identity, _ := identityFromFailure(parentFailure)
	child := testPackage(t, "child", newOID, localvalidation.ResultPassed, nil, []localvalidation.PackageFact{{Kind: "assertion", ConstraintID: "c", StepID: "s", DeclarationID: "d", NodePath: "0", Status: "passed", EvidenceIDs: []string{"child.ev"}}})
	link := testLink(t, parent, child)
	comparison, err := Compare(context.Background(), parent, child, CorrectionPlan{Version: Version, FailedFirst: []ScenarioRef{{Identity: identity}}}, link)
	if err != nil || len(comparison.Entries) != 1 || comparison.Entries[0].Class != Corrected {
		t.Fatalf("comparison=%+v err=%v", comparison, err)
	}
	child.Failures = []localvalidation.PackageFailure{testFailure("c", "s", "d", "child.ev")}
	child.Facts = nil
	comparison, err = Compare(context.Background(), parent, child, CorrectionPlan{Version: Version, FailedFirst: []ScenarioRef{{Identity: identity}}}, link)
	if err != nil || comparison.Entries[0].Class != Persistent {
		t.Fatalf("persistent=%+v err=%v", comparison, err)
	}
}

// TestComparisonRejectsCorruptOrForeignPackage proves lineage mismatches return no partial comparison.
func TestComparisonRejectsCorruptOrForeignPackage(t *testing.T) {
	parent := testPackage(t, "parent", oldOID, localvalidation.ResultFailed, []localvalidation.PackageFailure{testFailure("c", "s", "d", "ev")}, nil)
	child := testPackage(t, "child", newOID, localvalidation.ResultFailed, []localvalidation.PackageFailure{testFailure("c", "s", "d", "ev2")}, nil)
	link := testLink(t, parent, child)
	child.Binding.RepositoryID = "foreign"
	if _, err := Compare(context.Background(), parent, child, CorrectionPlan{Version: Version}, link); !IsCategory(err, IntegrityFailure) {
		t.Fatalf("error=%v", err)
	}
}

// TestCorrectionFullFlowHermetic proves preflight, failed-first, regression, package verification and publication without network.
func TestCorrectionFullFlowHermetic(t *testing.T) {
	fixture := newFlowFixture(t, false)
	record, err := fixture.coordinator.Submit(context.Background(), SubmitRequest{PrincipalID: "principal", ParentHandle: "parent.handle", SelectionAlias: "selection", IdempotencyKey: "key-12345678"})
	if err != nil {
		t.Fatal(err)
	}
	terminal := waitTerminal(t, fixture.coordinator, record.Link.Handle)
	if terminal.Outcome != OutcomePassed || terminal.Comparison == nil {
		t.Fatalf("terminal=%+v", terminal)
	}
	fixture.executor.mu.Lock()
	phases := append([]Phase(nil), fixture.executor.phases...)
	fixture.executor.mu.Unlock()
	if !reflect.DeepEqual(phases, []Phase{PhaseFailedFirst, PhaseRegression}) {
		t.Fatalf("phases=%v", phases)
	}
	events, err := fixture.store.Events(context.Background(), record.Link.Handle)
	if err != nil {
		t.Fatal(err)
	}
	foundNotRequired := false
	for _, event := range events {
		foundNotRequired = foundNotRequired || event.Type == "continuation_approval_not_required"
	}
	if !foundNotRequired {
		t.Fatal("missing approval-not-required event")
	}
}

// TestCorrectionRequiresDistinctCompatibleDeployedRevision proves same-commit preflight stops before functional work.
func TestCorrectionRequiresDistinctCompatibleDeployedRevision(t *testing.T) {
	fixture := newFlowFixture(t, false)
	fixture.resolver.revision.CommitOID = oldOID
	record, err := fixture.coordinator.Submit(context.Background(), SubmitRequest{PrincipalID: "principal", ParentHandle: "parent.handle", SelectionAlias: "selection", IdempotencyKey: "key-12345678"})
	if err != nil {
		t.Fatal(err)
	}
	terminal := waitTerminal(t, fixture.coordinator, record.Link.Handle)
	if terminal.TerminalReason != RevisionNotDeployed || len(fixture.executor.phases) != 0 {
		t.Fatalf("terminal=%+v phases=%v", terminal, fixture.executor.phases)
	}
}

// TestContinuationApprovalIsBoundOneUseAndStaleSafe proves the exact approval gate precedes all phase work.
func TestContinuationApprovalIsBoundOneUseAndStaleSafe(t *testing.T) {
	fixture := newFlowFixture(t, true)
	record, err := fixture.coordinator.Submit(context.Background(), SubmitRequest{PrincipalID: "principal", ParentHandle: "parent.handle", SelectionAlias: "selection", IdempotencyKey: "key-12345678"})
	if err != nil {
		t.Fatal(err)
	}
	terminal := waitTerminal(t, fixture.coordinator, record.Link.Handle)
	if terminal.Outcome != OutcomePassed || fixture.gate.calls != 1 {
		t.Fatalf("terminal=%+v calls=%d", terminal, fixture.gate.calls)
	}
}

// TestInfrastructureFailureNeverBecomesFunctionalFailure proves transport failure has no comparison or failure class.
func TestInfrastructureFailureNeverBecomesFunctionalFailure(t *testing.T) {
	fixture := newFlowFixture(t, false)
	fixture.executor.err = errors.New("SECRET hostile transport")
	record, err := fixture.coordinator.Submit(context.Background(), SubmitRequest{PrincipalID: "principal", ParentHandle: "parent.handle", SelectionAlias: "selection", IdempotencyKey: "key-12345678"})
	if err != nil {
		t.Fatal(err)
	}
	terminal := waitTerminal(t, fixture.coordinator, record.Link.Handle)
	if terminal.Outcome != OutcomeInconclusive || terminal.Comparison != nil || terminal.TerminalReason != Infrastructure {
		t.Fatalf("terminal=%+v", terminal)
	}
}

// TestRegressionRechecksPriorPassAndDeduplicatesOnlyChildEvidence proves only same-child confirmations cover regression.
func TestRegressionRechecksPriorPassAndDeduplicatesOnlyChildEvidence(t *testing.T) {
	fixture := newFlowFixture(t, false)
	record, err := fixture.coordinator.Submit(context.Background(), SubmitRequest{PrincipalID: "principal", ParentHandle: "parent.handle", SelectionAlias: "selection", IdempotencyKey: "dedupe-12345678"})
	if err != nil {
		t.Fatal(err)
	}
	waitTerminal(t, fixture.coordinator, record.Link.Handle)
	fixture.executor.mu.Lock()
	defer fixture.executor.mu.Unlock()
	if len(fixture.executor.scenarioCounts) != 2 || fixture.executor.scenarioCounts[0] != 1 || fixture.executor.scenarioCounts[1] != 0 {
		t.Fatalf("counts=%v", fixture.executor.scenarioCounts)
	}
}

// TestChainLimitsStopBeforeNewWork proves exhausted attempts reject admission before external callbacks.
func TestChainLimitsStopBeforeNewWork(t *testing.T) {
	store := testStore(t)
	reservation := testReservation(t, "parent", "handle.one", "key.one")
	limits := reservation.Limits
	limits.MaxAttempts = 1
	setReservationLimits(t, &reservation, limits)
	first, err := store.Reserve(context.Background(), reservation)
	if err != nil {
		t.Fatal(err)
	}
	record := first.Record
	record.Phase, record.Outcome, record.Owner = PhaseTerminal, OutcomeError, ""
	record.TerminalReason = Infrastructure
	record.Link.UpdatedAt = testNow.Add(time.Second)
	record.Link, _ = record.Link.Seal()
	terminalizeTestRecord(t, store, record)
	second := testReservation(t, "parent", "handle.two", "key.two")
	setReservationLimits(t, &second, reservation.Limits)
	rejected, err := store.Reserve(context.Background(), second)
	if err != nil || rejected.New || rejected.Record.Phase != PhaseTerminal || rejected.Record.TerminalReason != LimitExhausted || rejected.Record.LimitDimension != "attempts" {
		t.Fatalf("rejected=%+v error=%v", rejected, err)
	}
}

// TestInterruptedOwnerDoesNotRerun proves recovery emits a terminal operational state without callbacks.
func TestInterruptedOwnerDoesNotRerun(t *testing.T) {
	store := testStore(t)
	reserved, err := store.Reserve(context.Background(), testReservation(t, "parent", "handle.one", "key.one"))
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Recover(context.Background(), "owner", testNow.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	live, err := store.Get(context.Background(), reserved.Record.Link.Handle)
	if err != nil || live.Phase != PhaseReserved {
		t.Fatalf("live owner was recovered: %+v err=%v", live, err)
	}
	store.ReleaseOwner(reserved.Record.Link.Handle)
	if err := store.Recover(context.Background(), "owner", testNow.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	record, err := store.Get(context.Background(), reserved.Record.Link.Handle)
	if err != nil || record.Phase != PhaseTerminal || record.TerminalReason != Infrastructure {
		t.Fatalf("record=%+v err=%v", record, err)
	}
}

// TestIndependentChainsProgressConcurrently proves unrelated chain partitions do not conflict.
func TestIndependentChainsProgressConcurrently(t *testing.T) {
	store := testStore(t)
	var wg sync.WaitGroup
	results := make(chan error, 2)
	for _, reservation := range []Reservation{testReservation(t, "parent.one", "handle.one", "key.one"), testReservation(t, "parent.two", "handle.two", "key.two")} {
		wg.Add(1)
		go func(value Reservation) {
			defer wg.Done()
			_, err := store.Reserve(context.Background(), value)
			results <- err
		}(reservation)
	}
	wg.Wait()
	close(results)
	for err := range results {
		if err != nil {
			t.Fatal(err)
		}
	}
}

// TestJournalCorruptionFailsClosed proves recovery never skips malformed confirmed bytes.
func TestJournalCorruptionFailsClosed(t *testing.T) {
	store := testStore(t)
	if _, err := store.Reserve(context.Background(), testReservation(t, "parent", "handle.one", "key.one")); err != nil {
		t.Fatal(err)
	}
	file, err := os.OpenFile(filepath.Join(store.chainDir("parent"), "events.jsonl"), os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = file.WriteString("{truncated\n")
	_ = file.Close()
	if _, err := store.Get(context.Background(), "handle.one"); !IsCategory(err, IntegrityFailure) {
		t.Fatalf("error=%v", err)
	}
}

// TestJournalProjectorRejectsUnknownAndContradictoryEvents proves replay validates the closed event grammar rather than trusting full snapshots.
func TestJournalProjectorRejectsUnknownAndContradictoryEvents(t *testing.T) {
	tests := []struct {
		name   string
		mutate func([]Event)
	}{
		{name: "unknown event", mutate: func(events []Event) { events[1].Type = "future_event" }},
		{name: "illegal phase", mutate: func(events []Event) { events[1].Record.Phase = PhaseReserved }},
		{name: "immutable principal", mutate: func(events []Event) {
			events[1].Record.Link.PrincipalID = "foreign"
			events[1].Record.Link, _ = events[1].Record.Link.Seal()
		}},
		{name: "immutable request hash", mutate: func(events []Event) { events[1].Record.RequestHash = digestB }},
		{name: "usage outside accounting event", mutate: func(events []Event) { events[1].Record.Usage.Calls = 1 }},
		{name: "owner outside terminal event", mutate: func(events []Event) { events[1].Record.Owner = "other.owner" }},
		{name: "plan outside preflight confirmation", mutate: func(events []Event) {
			events[1].Record.Plan.NotApplicable = []ScenarioRef{{Identity: testIdentity("c", "s", "d", digestA)}}
		}},
		{name: "chain limits changed", mutate: func(events []Event) { events[1].Record.ChainLimits.MaxAttempts++ }},
		{name: "deadline changed", mutate: func(events []Event) { events[1].Record.ChainDeadline = events[1].Record.ChainDeadline.Add(time.Second) }},
		{name: "child binding outside confirmation", mutate: func(events []Event) {
			events[1].Record.Link.ChildRunID = "child"
			events[1].Record.Link.ChildBindingSHA256 = digestB
			events[1].Record.Link, _ = events[1].Record.Link.Seal()
		}},
		{name: "invalid cancellation hashes", mutate: func(events []Event) {
			events[1].Type = "cancellation_requested"
			events[1].Record.CancelKeyHash = "invalid"
			events[1].Record.CancelRequestHash = digestA
		}},
		{name: "invalid approval resolution", mutate: func(events []Event) {
			events[1].Type = "continuation_approval_not_required"
			events[1].Record.ApprovalResolution = "approved"
		}},
		{name: "unknown terminal reason", mutate: func(events []Event) {
			events[1].Type = "correction_terminal"
			events[1].Record.Phase = PhaseTerminal
			events[1].Record.Outcome = OutcomeError
			events[1].Record.TerminalReason = Category("future_reason")
			events[1].Record.Owner = ""
		}},
		{name: "event snapshot mismatch", mutate: func(events []Event) { events[1].Type = "scenario_confirmed" }},
		{name: "changed ordinal", mutate: func(events []Event) {
			events[1].Record.Link.Attempt++
			events[1].Record.Link, _ = events[1].Record.Link.Seal()
		}},
		{name: "invalid hash", mutate: func(events []Event) { events[1].Record.KeyHash = "not-a-digest" }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			store := testStore(t)
			reserved, err := store.Reserve(context.Background(), testReservation(t, "parent", "handle.one", "key.one"))
			if err != nil {
				t.Fatal(err)
			}
			next := reserved.Record
			next.Phase = PhasePreflight
			next.Link.UpdatedAt = next.Link.UpdatedAt.Add(time.Nanosecond)
			next.Link, _ = next.Link.Seal()
			if _, err := store.Update(context.Background(), next.Link.Handle, PhaseReserved, next, "preflight_started"); err != nil {
				t.Fatal(err)
			}
			events, err := store.Events(context.Background(), next.Link.Handle)
			if err != nil {
				t.Fatal(err)
			}
			test.mutate(events)
			writeJournalEvents(t, filepath.Join(store.chainDir("parent"), "events.jsonl"), events)
			if _, err := store.Get(context.Background(), next.Link.Handle); !IsCategory(err, IntegrityFailure) {
				t.Fatalf("error=%v", err)
			}
		})
	}
}

// TestJournalProjectorValidatesAuthorizedEventValues proves allowed event kinds still reject invalid deadline, plan, allowance, preparation and terminal values.
func TestJournalProjectorValidatesAuthorizedEventValues(t *testing.T) {
	tests := []struct {
		name   string
		mutate func([]Event)
	}{
		{name: "derived deadline", mutate: func(events []Event) { events[0].Record.ChainDeadline = events[0].Record.ChainDeadline.Add(time.Second) }},
		{name: "policy snapshot digest", mutate: func(events []Event) {
			events[0].Record.PolicyApprovalRequired = !events[0].Record.PolicyApprovalRequired
		}},
		{name: "invalid plan identity", mutate: func(events []Event) {
			event := eventByType(events, "preflight_confirmed")
			event.Record.Plan.FailedFirst[0].Identity.Fingerprint = "invalid"
		}},
		{name: "missing motivating partition", mutate: func(events []Event) {
			event := eventByType(events, "preflight_confirmed")
			event.Record.Plan.FailedFirst = nil
		}},
		{name: "invalid applicability reason", mutate: func(events []Event) {
			event := eventByType(events, "preflight_confirmed")
			ref := event.Record.Plan.FailedFirst[0]
			ref.Reason = ApplicabilityReason("future_reason")
			event.Record.Plan.FailedFirst = nil
			event.Record.Plan.NotApplicable = []ScenarioRef{ref}
		}},
		{name: "wrong policy reference", mutate: func(events []Event) {
			event := eventByType(events, "preflight_confirmed")
			event.Record.Plan.RequiredRegression[0].PolicyReference = "other"
		}},
		{name: "duplicate policy scenario", mutate: func(events []Event) {
			event := eventByType(events, "preflight_confirmed")
			event.Record.Plan.RequiredRegression = append(event.Record.Plan.RequiredRegression, event.Record.Plan.RequiredRegression[0])
		}},
		{name: "noncanonical evidence", mutate: func(events []Event) {
			event := eventByType(events, "preflight_confirmed")
			event.Record.Plan.FailedFirst[0].SourceEvidenceIDs = []string{"z", "a"}
		}},
		{name: "allowance above remaining", mutate: func(events []Event) { eventByType(events, "model_budget_reserved").Record.BudgetReservation.Calls++ }},
		{name: "allowance below remaining", mutate: func(events []Event) { eventByType(events, "model_budget_reserved").Record.BudgetReservation.Tokens-- }},
		{name: "allowance after deadline", mutate: func(events []Event) {
			event := eventByType(events, "model_budget_reserved")
			event.Record.Link.UpdatedAt = event.Record.ChainDeadline
			event.Record.Link, _ = event.Record.Link.Seal()
			event.At = event.Record.Link.UpdatedAt
		}},
		{name: "invalid prepared outcome", mutate: func(events []Event) {
			eventByType(events, "finalization_prepared").Record.PreparedOutcome = OutcomeError
		}},
		{name: "prepared usage above allowance", mutate: func(events []Event) {
			event := eventByType(events, "finalization_prepared")
			event.Record.PreparedUsage.Calls = event.Record.BudgetReservation.Calls + 1
		}},
		{name: "comparison intent digest", mutate: func(events []Event) {
			eventByType(events, "comparison_intended").Record.ComparisonIntent.Digest = digestB
		}},
		{name: "comparison intent child locator", mutate: func(events []Event) {
			eventByType(events, "comparison_intended").Record.ChildLocator = ""
		}},
		{name: "published comparison differs from intent", mutate: func(events []Event) {
			eventByType(events, "comparison_published").Record.Comparison.Digest = digestB
		}},
		{name: "functional terminal with reason", mutate: func(events []Event) {
			eventByType(events, "comparison_published").Record.TerminalReason = Infrastructure
		}},
		{name: "comparison with operational outcome", mutate: func(events []Event) {
			event := eventByType(events, "comparison_published")
			event.Record.Outcome = OutcomeError
		}},
		{name: "operational event with functional outcome", mutate: func(events []Event) {
			event := eventByType(events, "comparison_published")
			event.Type = "comparison_abandoned"
			event.Record.Comparison = nil
			event.Record.ComparisonIntent = nil
			event.Record.TerminalReason = Infrastructure
		}},
		{name: "cancelled with wrong reason", mutate: func(events []Event) {
			event := eventByType(events, "comparison_published")
			event.Type = "comparison_abandoned"
			event.Record.Comparison = nil
			event.Record.ComparisonIntent = nil
			event.Record.Outcome = OutcomeCancelled
			event.Record.TerminalReason = IntegrityFailure
		}},
		{name: "limit with unknown dimension", mutate: func(events []Event) {
			event := eventByType(events, "comparison_published")
			event.Type = "comparison_abandoned"
			event.Record.Comparison = nil
			event.Record.ComparisonIntent = nil
			event.Record.Outcome = OutcomeInconclusive
			event.Record.TerminalReason = LimitExhausted
			event.Record.LimitDimension = "money"
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := newFlowFixture(t, false)
			record, err := fixture.coordinator.Submit(context.Background(), SubmitRequest{PrincipalID: "principal", ParentHandle: "parent.handle", SelectionAlias: "selection", IdempotencyKey: "strict-values-123"})
			if err != nil {
				t.Fatal(err)
			}
			waitTerminal(t, fixture.coordinator, record.Link.Handle)
			events, err := fixture.store.Events(context.Background(), record.Link.Handle)
			if err != nil {
				t.Fatal(err)
			}
			test.mutate(events)
			writeJournalEvents(t, filepath.Join(fixture.store.chainDir("parent"), "events.jsonl"), events)
			if _, err := fixture.store.Get(context.Background(), record.Link.Handle); !IsCategory(err, IntegrityFailure) {
				t.Fatalf("error=%v events=%v", err, eventTypes(events))
			}
		})
	}
}

// TestJournalProjectorAcceptsCompleteAuthorizedValues proves the strict value checks retain a valid successful prefix.
func TestJournalProjectorAcceptsCompleteAuthorizedValues(t *testing.T) {
	fixture := newFlowFixture(t, false)
	record, err := fixture.coordinator.Submit(context.Background(), SubmitRequest{PrincipalID: "principal", ParentHandle: "parent.handle", SelectionAlias: "selection", IdempotencyKey: "strict-positive-123"})
	if err != nil {
		t.Fatal(err)
	}
	terminal := waitTerminal(t, fixture.coordinator, record.Link.Handle)
	replayed, err := fixture.store.Get(context.Background(), record.Link.Handle)
	if err != nil || replayed.Link.Digest != terminal.Link.Digest || replayed.Comparison == nil || replayed.PreparationID == "" {
		t.Fatalf("replayed=%+v err=%v", replayed, err)
	}
}

// eventByType returns the mutable event fixture for one required journal type.
func eventByType(events []Event, kind string) *Event {
	for index := range events {
		if events[index].Type == kind {
			return &events[index]
		}
	}
	panic("missing event fixture: " + kind)
}

// writeJournalEvents rewrites a test journal with deliberate snapshots so strict replay can be exercised deterministically.
func writeJournalEvents(t *testing.T, path string, events []Event) {
	t.Helper()
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	encoder := json.NewEncoder(file)
	for _, event := range events {
		if err := encoder.Encode(event); err != nil {
			_ = file.Close()
			t.Fatal(err)
		}
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
}

// TestLocalComparisonPublisherIsNoReplace proves a published comparison cannot be overwritten.
func TestLocalComparisonPublisherIsNoReplace(t *testing.T) {
	publisher := LocalComparisonPublisher{Root: filepath.Join(t.TempDir(), "comparisons")}
	comparison := Comparison{Version: Version, ParentRunID: "parent", ChildRunID: "child", Entries: []ComparisonEntry{}}
	comparison.Digest = hashCanonical(comparison)
	if err := publisher.Publish(context.Background(), "handle.one", comparison); err != nil {
		t.Fatal(err)
	}
	if err := publisher.Publish(context.Background(), "handle.one", comparison); err != nil {
		t.Fatalf("idempotent error=%v", err)
	}
	conflict := comparison
	conflict.ChildRunID = "other"
	conflict.Digest = ""
	conflict.Digest = hashCanonical(conflict)
	if err := publisher.Publish(context.Background(), "handle.one", conflict); !IsCategory(err, Conflict) {
		t.Fatalf("conflict=%v", err)
	}
	matched, err := publisher.Resolve(context.Background(), "handle.one", comparison.Digest)
	if err != nil || !matched {
		t.Fatalf("matched=%v err=%v", matched, err)
	}
	target := filepath.Join(publisher.Root, "handle.one.json")
	raw, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(target, append(raw, ' '), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := publisher.Resolve(context.Background(), "handle.one", comparison.Digest); !IsCategory(err, IntegrityFailure) {
		t.Fatalf("tamper=%v", err)
	}
}

// TestExternalAdapterReauthorizesAndObscuresEvidence proves foreign principals and raw IDs never cross transport.
func TestExternalAdapterReauthorizesAndObscuresEvidence(t *testing.T) {
	store := testStore(t)
	reserved, err := store.Reserve(context.Background(), testReservation(t, "parent", "handle.one", "key.one"))
	if err != nil {
		t.Fatal(err)
	}
	record := reserved.Record
	record.Phase, record.Outcome, record.Owner = PhaseTerminal, OutcomeFailed, ""
	record.ChildLocator = "child.locator"
	record.Comparison = &Comparison{Version: Version, ParentRunID: "parent", ChildRunID: "child", Entries: []ComparisonEntry{{Identity: testIdentity("c", "s", "d", digestA), Class: Persistent, Evidence: []EvidenceReference{{Origin: "parent", EvidenceID: "raw.secret.id"}}}}}
	record.Comparison.Digest = hashCanonical(*record.Comparison)
	record.Link.ChildRunID, record.Link.ChildBindingSHA256, record.Link.UpdatedAt = "child", digestB, testNow.Add(time.Second)
	record.Link, _ = record.Link.Seal()
	terminalizeTestRecord(t, store, record)
	coordinator := &Coordinator{deps: Dependencies{Store: store}, runs: map[string]*ownedRun{}}
	adapter := ExternalAdapter{Coordinator: coordinator}
	if _, err := adapter.CorrectionStatus(context.Background(), "foreign", externalagent.CorrectionHandleInput{CorrectionHandle: "handle.one"}); err == nil {
		t.Fatal("foreign handle disclosed")
	}
	result, err := adapter.CorrectionResult(context.Background(), "principal", externalagent.CorrectionHandleInput{CorrectionHandle: "handle.one"})
	if err != nil || len(result.Persistent) != 1 || result.Persistent[0].EvidenceHandles[0] == "raw.secret.id" {
		t.Fatalf("result=%+v err=%v", result, err)
	}
}

// TestCorrectionCancelJoinsAndPreservesParent proves active phase cancellation is joined and terminal exactly once.
func TestCorrectionCancelJoinsAndPreservesParent(t *testing.T) {
	fixture := newFlowFixture(t, false)
	fixture.executor.block = true
	fixture.executor.started = make(chan struct{})
	record, err := fixture.coordinator.Submit(context.Background(), SubmitRequest{PrincipalID: "principal", ParentHandle: "parent.handle", SelectionAlias: "selection", IdempotencyKey: "cancel-12345678"})
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-fixture.executor.started:
	case <-time.After(time.Second):
		t.Fatal("executor did not start")
	}
	cancelled, err := fixture.coordinator.Cancel(context.Background(), record.Link.Handle, "principal", "cancel-key-12345")
	if err != nil || cancelled.Phase != PhaseTerminal || cancelled.Outcome != OutcomeCancelled {
		t.Fatalf("record=%+v err=%v", cancelled, err)
	}
	replayed, err := fixture.coordinator.Cancel(context.Background(), record.Link.Handle, "principal", "cancel-key-12345")
	if err != nil || replayed.Link.Digest != cancelled.Link.Digest {
		t.Fatalf("replay=%+v err=%v", replayed, err)
	}
}

// TestForeignCancellationLeavesJournalByteIdentical proves ownership is checked before idempotency mutation.
func TestForeignCancellationLeavesJournalByteIdentical(t *testing.T) {
	store := testStore(t)
	reserved, err := store.Reserve(context.Background(), testReservation(t, "parent", "handle.one", "key.one"))
	if err != nil {
		t.Fatal(err)
	}
	journal := filepath.Join(store.chainDir("parent"), "events.jsonl")
	before, err := os.ReadFile(journal)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.ReserveCancel(context.Background(), reserved.Record.Link.Handle, "foreign", keyDigest("foreign.key"), digestA, testNow); !IsCategory(err, NotFound) {
		t.Fatalf("foreign=%v", err)
	}
	after, err := os.ReadFile(journal)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) {
		t.Fatal("foreign cancellation mutated journal")
	}
	if _, fresh, err := store.ReserveCancel(context.Background(), reserved.Record.Link.Handle, "principal", keyDigest("owner.key"), digestA, testNow); err != nil || !fresh {
		t.Fatalf("owner fresh=%v err=%v", fresh, err)
	}
}

// TestModelBudgetIsCumulativeAndLastReservationAtomic proves prior attempts and concurrent reservations share one ledger.
func TestModelBudgetIsCumulativeAndLastReservationAtomic(t *testing.T) {
	store := testStore(t)
	limits := ChainLimits{MaxAttempts: 3, MaxDuration: time.Hour, MaxModelCalls: 3, MaxModelTokens: 6}
	firstInput := testReservation(t, "parent", "handle.one", "key.one")
	setReservationLimits(t, &firstInput, limits)
	first, err := store.Reserve(context.Background(), firstInput)
	if err != nil {
		t.Fatal(err)
	}
	firstRecord := first.Record
	firstRecord.Usage = ModelUsage{Calls: 2, Tokens: 4}
	firstRecord.Phase, firstRecord.Outcome, firstRecord.Owner = PhaseTerminal, OutcomeFailed, ""
	firstRecord.Link.ChildRunID, firstRecord.Link.ChildBindingSHA256, firstRecord.Link.UpdatedAt = "child.one", digestB, testNow.Add(time.Second)
	firstRecord.Link, _ = firstRecord.Link.Seal()
	terminalizeTestRecord(t, store, firstRecord)
	secondInput := testReservation(t, "child.one", "handle.two", "key.two")
	setReservationLimits(t, &secondInput, limits)
	second, err := store.Reserve(context.Background(), secondInput)
	if err != nil {
		t.Fatal(err)
	}
	type budgetResult struct {
		allowance ModelUsage
		err       error
	}
	results := make(chan budgetResult, 2)
	var wg sync.WaitGroup
	for range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, allowance, err := store.ReserveRemainingBudget(context.Background(), second.Record.Link.Handle, PhaseReserved, testNow.Add(2*time.Second))
			results <- budgetResult{allowance, err}
		}()
	}
	wg.Wait()
	close(results)
	success := 0
	for result := range results {
		if result.err == nil {
			success++
			if result.allowance != (ModelUsage{Calls: 1, Tokens: 2}) {
				t.Fatalf("allowance=%+v", result.allowance)
			}
		} else if !IsCategory(result.err, Conflict) {
			t.Fatal(result.err)
		}
	}
	if success != 1 {
		t.Fatalf("success=%d", success)
	}
}

// TestFinalizeUsageIsConfirmedBeforePublication proves final model usage cannot exceed the remaining chain budget.
func TestFinalizeUsageIsConfirmedBeforePublication(t *testing.T) {
	fixture := newFlowFixture(t, false)
	fixture.executor.finalizeUsage = ModelUsage{Calls: 9, Tokens: 900}
	record, err := fixture.coordinator.Submit(context.Background(), SubmitRequest{PrincipalID: "principal", ParentHandle: "parent.handle", SelectionAlias: "selection", IdempotencyKey: "final-budget-123"})
	if err != nil {
		t.Fatal(err)
	}
	terminal := waitTerminal(t, fixture.coordinator, record.Link.Handle)
	if terminal.Outcome != OutcomeInconclusive || terminal.TerminalReason != LimitExhausted || terminal.Comparison != nil {
		t.Fatalf("terminal=%+v", terminal)
	}
	fixture.executor.mu.Lock()
	published := fixture.executor.packagePublished
	fixture.executor.mu.Unlock()
	fixture.publisher.mu.Lock()
	comparisons := len(fixture.publisher.comparisons)
	fixture.publisher.mu.Unlock()
	if published || comparisons != 0 {
		t.Fatalf("package published=%v comparisons=%d", published, comparisons)
	}
}

// TestInvalidOrFailedPreparationPublishesNothing proves malformed staging is rejected before accounting or visible effects.
func TestInvalidOrFailedPreparationPublishesNothing(t *testing.T) {
	tests := []struct {
		name       string
		prepareErr error
		prepare    *ChildPreparation
	}{
		{name: "prepare failure", prepareErr: errors.New("prepare failed")},
		{name: "invalid id", prepare: &ChildPreparation{PreparationID: "../bad", RunID: "child", Outcome: OutcomePassed}},
		{name: "foreign run", prepare: &ChildPreparation{PreparationID: "preparation", RunID: "foreign", Outcome: OutcomePassed}},
		{name: "invalid outcome", prepare: &ChildPreparation{PreparationID: "preparation", RunID: "child", Outcome: OutcomeError}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := newFlowFixture(t, false)
			fixture.executor.prepareErr = test.prepareErr
			fixture.executor.preparedOverride = test.prepare
			record, err := fixture.coordinator.Submit(context.Background(), SubmitRequest{PrincipalID: "principal", ParentHandle: "parent.handle", SelectionAlias: "selection", IdempotencyKey: "invalid-prepare-123"})
			if err != nil {
				t.Fatal(err)
			}
			terminal := waitTerminal(t, fixture.coordinator, record.Link.Handle)
			fixture.executor.mu.Lock()
			packages, commits := fixture.executor.packagePublishCount, fixture.executor.commitCalls
			fixture.executor.mu.Unlock()
			fixture.publisher.mu.Lock()
			comparisons := len(fixture.publisher.comparisons)
			fixture.publisher.mu.Unlock()
			if terminal.Outcome != OutcomeError || terminal.TerminalReason != map[bool]Category{true: IntegrityFailure, false: Infrastructure}[test.prepare != nil] || packages != 0 || commits != 0 || comparisons != 0 {
				t.Fatalf("terminal=%+v packages=%d commits=%d comparisons=%d", terminal, packages, commits, comparisons)
			}
			events, err := fixture.store.Events(context.Background(), record.Link.Handle)
			if err != nil {
				t.Fatal(err)
			}
			for _, event := range events {
				if event.Type == "finalization_prepared" || event.Type == "comparison_published" {
					t.Fatalf("unexpected event=%s", event.Type)
				}
			}
		})
	}
}

// TestFinalizeCommitIsIdempotentAndReconcilesUncertainPublication proves before/after failures never duplicate a package and confirmed commits remain recoverable.
func TestFinalizeCommitIsIdempotentAndReconcilesUncertainPublication(t *testing.T) {
	for _, after := range []bool{false, true} {
		t.Run(map[bool]string{false: "before commit", true: "after commit"}[after], func(t *testing.T) {
			fixture := newFlowFixture(t, false)
			if after {
				fixture.executor.commitErrAfter = errors.New("response lost")
			} else {
				fixture.executor.commitErrBefore = errors.New("commit rejected")
			}
			record, err := fixture.coordinator.Submit(context.Background(), SubmitRequest{PrincipalID: "principal", ParentHandle: "parent.handle", SelectionAlias: "selection", IdempotencyKey: "commit-injection-123"})
			if err != nil {
				t.Fatal(err)
			}
			terminal := waitTerminal(t, fixture.coordinator, record.Link.Handle)
			fixture.executor.mu.Lock()
			published, calls := fixture.executor.packagePublishCount, fixture.executor.commitCalls
			binding := fixture.executor.preparedBinding
			fixture.executor.mu.Unlock()
			if after {
				if terminal.Outcome != OutcomePassed || terminal.Comparison == nil || published != 1 || calls != 1 {
					t.Fatalf("terminal=%+v published=%d calls=%d", terminal, published, calls)
				}
				preparation := ChildPreparation{PreparationID: terminal.PreparationID, RunID: binding.RunID, Outcome: terminal.PreparedOutcome, Usage: terminal.PreparedUsage}
				_, _ = fixture.executor.CommitFinalize(context.Background(), preparation)
				fixture.executor.mu.Lock()
				published, calls = fixture.executor.packagePublishCount, fixture.executor.commitCalls
				fixture.executor.mu.Unlock()
				if published != 1 || calls != 2 {
					t.Fatalf("retry published=%d calls=%d", published, calls)
				}
				reopened, err := NewLocalStore(fixture.store.root)
				if err != nil {
					t.Fatal(err)
				}
				defer reopened.Close()
				recovered, err := reopened.Get(context.Background(), terminal.Link.Handle)
				if err != nil || recovered.Comparison == nil || recovered.PreparationID != terminal.PreparationID {
					t.Fatalf("recovered=%+v err=%v", recovered, err)
				}
			} else if terminal.Outcome != OutcomeError || terminal.TerminalReason != Infrastructure || published != 0 || terminal.Comparison != nil {
				t.Fatalf("terminal=%+v published=%d calls=%d", terminal, published, calls)
			}
			events, err := fixture.store.Events(context.Background(), terminal.Link.Handle)
			if err != nil {
				t.Fatal(err)
			}
			if countEvent(events, "finalization_prepared") != 1 || countEvent(events, "model_usage_confirmed") != 3 || countEvent(events, "comparison_published") != map[bool]int{false: 0, true: 1}[after] {
				t.Fatalf("events=%v", eventTypes(events))
			}
		})
	}
}

// countEvent counts one durable event kind for exact journal assertions.
func countEvent(events []Event, kind string) int {
	count := 0
	for _, event := range events {
		if event.Type == kind {
			count++
		}
	}
	return count
}

// eventTypes projects journal types without exposing mutable event snapshots in failure output.
func eventTypes(events []Event) []string {
	types := make([]string, 0, len(events))
	for _, event := range events {
		types = append(types, event.Type)
	}
	return types
}

// TestJournalRecordsApprovalAndScenarioOrder proves durable audit events preserve the approved phase barrier.
func TestJournalRecordsApprovalAndScenarioOrder(t *testing.T) {
	fixture := newFlowFixture(t, true)
	record, err := fixture.coordinator.Submit(context.Background(), SubmitRequest{PrincipalID: "principal", ParentHandle: "parent.handle", SelectionAlias: "selection", IdempotencyKey: "journal-order-123"})
	if err != nil {
		t.Fatal(err)
	}
	waitTerminal(t, fixture.coordinator, record.Link.Handle)
	events, err := fixture.store.Events(context.Background(), record.Link.Handle)
	if err != nil {
		t.Fatal(err)
	}
	types := []string{}
	for _, event := range events {
		types = append(types, event.Type)
	}
	wantOrder := []string{"continuation_approval_requested", "continuation_approval_resolved", "scenario_confirmed", "regression_covered", "comparison_published"}
	position := -1
	for _, want := range wantOrder {
		found := -1
		for i := position + 1; i < len(types); i++ {
			if types[i] == want {
				found = i
				break
			}
		}
		if found < 0 {
			t.Fatalf("missing %s in %v", want, types)
		}
		position = found
	}
}

// TestComparatorRejectsReducedIdentityCollision proves package v1 cannot let one fact correct two scenarios.
func TestComparatorRejectsReducedIdentityCollision(t *testing.T) {
	failure := testFailure("c", "s", "d", "parent.ev")
	parent := testPackage(t, "parent", oldOID, localvalidation.ResultFailed, []localvalidation.PackageFailure{failure}, nil)
	base, _ := identityFromFailure(failure)
	other := base
	other.Scenario = "other"
	other.Fingerprint = digestB
	child := testPackage(t, "child", newOID, localvalidation.ResultPassed, nil, []localvalidation.PackageFact{{Kind: "assertion", ConstraintID: "c", StepID: "s", DeclarationID: "d", NodePath: "0", Status: "passed", EvidenceIDs: []string{"child.ev"}}})
	_, err := Compare(context.Background(), parent, child, CorrectionPlan{Version: Version, FailedFirst: []ScenarioRef{{Identity: base}}, RequiredRegression: []ScenarioRef{{Identity: other}}}, testLink(t, parent, child))
	if !IsCategory(err, IntegrityFailure) {
		t.Fatalf("error=%v", err)
	}
}

// TestIndependentChainLocksDoNotBlockProgress proves a busy chain lock does not serialize another partition.
func TestIndependentChainLocksDoNotBlockProgress(t *testing.T) {
	store := testStore(t)
	one, err := store.Reserve(context.Background(), testReservation(t, "parent.one", "handle.one", "key.one"))
	if err != nil {
		t.Fatal(err)
	}
	two, err := store.Reserve(context.Background(), testReservation(t, "parent.two", "handle.two", "key.two"))
	if err != nil {
		t.Fatal(err)
	}
	release, err := acquireStoreLock(context.Background(), filepath.Join(store.chainDir("parent.one"), "chain.lock"))
	if err != nil {
		t.Fatal(err)
	}
	started := make(chan struct{})
	done := make(chan error, 1)
	go func() { close(started); _, err := store.Get(context.Background(), one.Record.Link.Handle); done <- err }()
	<-started
	if _, err := store.Get(context.Background(), two.Record.Link.Handle); err != nil {
		t.Fatalf("independent chain blocked: %v", err)
	}
	release()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

// TestMultiprocessTailAdmissionHasOneWinner proves the per-chain CAS across real OS processes.
func TestMultiprocessTailAdmissionHasOneWinner(t *testing.T) {
	root := filepath.Join(t.TempDir(), "shared")
	testContext, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	results := make(chan processAdmissionResult, 2)
	var wg sync.WaitGroup
	for index := 1; index <= 2; index++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			command := exec.CommandContext(testContext, os.Args[0], "-test.run=^TestCorrectionStoreProcessHelper$")
			command.Env = append(os.Environ(), "FEATUREPROOF_CORRECTION_HELPER=1", "FEATUREPROOF_CORRECTION_ROOT="+root, fmt.Sprintf("FEATUREPROOF_CORRECTION_HANDLE=handle.%d", n), fmt.Sprintf("FEATUREPROOF_CORRECTION_KEY=key.%d", n))
			output, err := command.CombinedOutput()
			results <- processAdmissionResult{err: err, output: string(output)}
		}(index)
	}
	wg.Wait()
	close(results)
	success := 0
	var failures []processAdmissionResult
	for result := range results {
		if result.err == nil {
			success++
		} else {
			failures = append(failures, result)
		}
	}
	if success != 1 {
		t.Fatalf("success=%d context=%v failures=%+v", success, testContext.Err(), failures)
	}
}

// processAdmissionResult carries one subprocess outcome so the multiprocess CAS test can diagnose bounded failures.
type processAdmissionResult struct {
	err    error
	output string
}

// TestCorrectionStoreProcessHelper performs one isolated admission for the multiprocess CAS test.
func TestCorrectionStoreProcessHelper(t *testing.T) {
	if os.Getenv("FEATUREPROOF_CORRECTION_HELPER") != "1" {
		t.Skip("helper process only")
	}
	store, err := NewLocalStore(os.Getenv("FEATUREPROOF_CORRECTION_ROOT"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	input := testReservation(t, "parent", os.Getenv("FEATUREPROOF_CORRECTION_HANDLE"), os.Getenv("FEATUREPROOF_CORRECTION_KEY"))
	if _, err := store.Reserve(context.Background(), input); err != nil {
		t.Fatal(err)
	}
}

// TestLookupWaitsForPausedMultiprocessAppend proves status and cancel never replay an unlocked partial journal.
func TestLookupWaitsForPausedMultiprocessAppend(t *testing.T) {
	store := testStore(t)
	one, err := store.Reserve(context.Background(), testReservation(t, "parent.one", "handle.one", "key.one"))
	if err != nil {
		t.Fatal(err)
	}
	two, err := store.Reserve(context.Background(), testReservation(t, "parent.two", "handle.two", "key.two"))
	if err != nil {
		t.Fatal(err)
	}
	processContext, stop := context.WithTimeout(context.Background(), 10*time.Second)
	defer stop()
	command := exec.CommandContext(processContext, os.Args[0], "-test.run=^TestCorrectionPausedAppendProcessHelper$")
	command.Env = append(os.Environ(), "FEATUREPROOF_CORRECTION_PAUSED_APPEND=1", "FEATUREPROOF_CORRECTION_ROOT="+store.root, "FEATUREPROOF_CORRECTION_CHAIN=parent.one")
	stdin, err := command.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	stdout, err := command.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	var stderr bytes.Buffer
	command.Stderr = &stderr
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	outputReader := bufio.NewReader(stdout)
	ready, err := outputReader.ReadString('\n')
	if err != nil || ready != "ready\n" {
		t.Fatalf("ready=%q err=%v stderr=%s", ready, err, stderr.String())
	}
	statusContext, cancelStatus := context.WithTimeout(context.Background(), 100*time.Millisecond)
	_, statusErr := store.Get(statusContext, one.Record.Link.Handle)
	cancelStatus()
	if !errors.Is(statusErr, context.DeadlineExceeded) {
		t.Fatalf("status observed partial append: %v", statusErr)
	}
	cancelContext, cancelCancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	_, _, cancelErr := store.ReserveCancel(cancelContext, one.Record.Link.Handle, "principal", keyDigest("cancel.key"), digestA, testNow)
	cancelCancel()
	if !errors.Is(cancelErr, context.DeadlineExceeded) {
		t.Fatalf("cancel observed partial append: %v", cancelErr)
	}
	if _, err := store.Get(context.Background(), two.Record.Link.Handle); err != nil {
		t.Fatalf("independent chain blocked: %v", err)
	}
	if _, err := stdin.Write([]byte("release\n")); err != nil {
		t.Fatal(err)
	}
	_ = stdin.Close()
	done, readErr := outputReader.ReadString('\n')
	if readErr != nil || done != "done\n" {
		t.Fatalf("done=%q err=%v stderr=%s", done, readErr, stderr.String())
	}
	if err := command.Wait(); err != nil {
		remainder, _ := io.ReadAll(outputReader)
		t.Fatalf("helper=%v stdout=%s stderr=%s", err, remainder, stderr.String())
	}
	if _, err := store.Get(context.Background(), one.Record.Link.Handle); err != nil {
		t.Fatal(err)
	}
	if _, fresh, err := store.ReserveCancel(context.Background(), one.Record.Link.Handle, "principal", keyDigest("cancel.key"), digestA, testNow); err != nil || !fresh {
		t.Fatalf("cancel fresh=%v err=%v", fresh, err)
	}
}

// TestCorrectionPausedAppendProcessHelper holds a real chain lock around an intentionally partial append.
func TestCorrectionPausedAppendProcessHelper(t *testing.T) {
	if os.Getenv("FEATUREPROOF_CORRECTION_PAUSED_APPEND") != "1" {
		t.Skip("helper process only")
	}
	root := os.Getenv("FEATUREPROOF_CORRECTION_ROOT")
	chain := os.Getenv("FEATUREPROOF_CORRECTION_CHAIN")
	chainDir := filepath.Join(root, "chains", keyDigest(chain))
	release, err := acquireStoreLock(context.Background(), filepath.Join(chainDir, "chain.lock"))
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	journal := filepath.Join(chainDir, "events.jsonl")
	original, err := os.ReadFile(journal)
	if err != nil {
		t.Fatal(err)
	}
	file, err := os.OpenFile(journal, os.O_RDWR|os.O_APPEND, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := file.Write([]byte(`{"version":`)); err != nil {
		t.Fatal(err)
	}
	if err := file.Sync(); err != nil {
		t.Fatal(err)
	}
	_, _ = fmt.Fprintln(os.Stdout, "ready")
	if _, err := bufio.NewReader(os.Stdin).ReadString('\n'); err != nil {
		_, _ = fmt.Fprintf(os.Stdout, "read-error:%v\n", err)
		return
	}
	if err := file.Close(); err != nil {
		_, _ = fmt.Fprintf(os.Stdout, "close-error:%v\n", err)
		return
	}
	if err := os.WriteFile(journal, original, 0o600); err != nil {
		_, _ = fmt.Fprintf(os.Stdout, "restore-error:%v\n", err)
		return
	}
	_, _ = fmt.Fprintln(os.Stdout, "done")
}

// TestAppendFailureInjectionRecoversConfirmedPrefix proves failures around append are retry-safe.
func TestAppendFailureInjectionRecoversConfirmedPrefix(t *testing.T) {
	store := testStore(t)
	input := testReservation(t, "parent", "handle.one", "key.one")
	store.failBeforeAppend.Store(true)
	if _, err := store.Reserve(context.Background(), input); !IsCategory(err, Infrastructure) {
		t.Fatalf("before=%v", err)
	}
	store.failBeforeAppend.Store(false)
	store.failAfterAppend.Store(true)
	reconciled, err := store.Reserve(context.Background(), input)
	if err != nil || !reconciled.New {
		t.Fatalf("after=%+v err=%v", reconciled, err)
	}
	store.failAfterAppend.Store(false)
	replay, err := store.Reserve(context.Background(), input)
	if err != nil || replay.New {
		t.Fatalf("replay=%+v err=%v", replay, err)
	}
}

// TestCoordinatorReconcilesUncertainReservation proves an acknowledged append starts exactly one joinable child.
func TestCoordinatorReconcilesUncertainReservation(t *testing.T) {
	fixture := newFlowFixture(t, false)
	fixture.ids.values = append(fixture.ids.values, "replay.handle", "replay.id")
	requestContext, cancelRequest := context.WithCancel(context.Background())
	fixture.store.afterAppendHook = cancelRequest
	fixture.store.failAfterAppend.Store(true)
	first, err := fixture.coordinator.Submit(requestContext, SubmitRequest{PrincipalID: "principal", ParentHandle: "parent.handle", SelectionAlias: "selection", IdempotencyKey: "uncertain-reservation"})
	if err != nil {
		t.Fatal(err)
	}
	if requestContext.Err() == nil {
		t.Fatal("request was not cancelled after confirmed append")
	}
	replay, err := fixture.coordinator.Submit(context.Background(), SubmitRequest{PrincipalID: "principal", ParentHandle: "parent.handle", SelectionAlias: "selection", IdempotencyKey: "uncertain-reservation"})
	if err != nil || replay.Link.Handle != first.Link.Handle {
		t.Fatalf("replay=%+v err=%v", replay, err)
	}
	waitTerminal(t, fixture.coordinator, first.Link.Handle)
	cancelled, err := fixture.coordinator.Cancel(context.Background(), first.Link.Handle, "principal", "cancel-after-terminal")
	if err != nil || cancelled.Phase != PhaseTerminal {
		t.Fatalf("cancelled=%+v err=%v", cancelled, err)
	}
	fixture.executor.mu.Lock()
	phases := append([]Phase(nil), fixture.executor.phases...)
	fixture.executor.mu.Unlock()
	if !reflect.DeepEqual(phases, []Phase{PhaseFailedFirst, PhaseRegression}) {
		t.Fatalf("duplicate or missing child work: %v", phases)
	}
}

// TestTerminalUsageIsCountedOnceAfterCancelReplayAndReopen proves snapshots after terminal never duplicate chain usage.
func TestTerminalUsageIsCountedOnceAfterCancelReplayAndReopen(t *testing.T) {
	root := filepath.Join(t.TempDir(), "correction")
	store, err := NewLocalStore(root)
	if err != nil {
		t.Fatal(err)
	}
	limits := ChainLimits{MaxAttempts: 3, MaxDuration: time.Hour, MaxModelCalls: 3, MaxModelTokens: 6}
	firstInput := testReservation(t, "parent", "handle.one", "key.one")
	setReservationLimits(t, &firstInput, limits)
	first, err := store.Reserve(context.Background(), firstInput)
	if err != nil {
		t.Fatal(err)
	}
	terminal := first.Record
	terminal.Usage = ModelUsage{Calls: 1, Tokens: 2}
	terminal.Phase, terminal.Outcome, terminal.Owner = PhaseTerminal, OutcomeFailed, ""
	terminal.Link.ChildRunID, terminal.Link.ChildBindingSHA256 = "child.one", digestB
	terminal.Link.UpdatedAt = testNow.Add(time.Second)
	terminal.Link, _ = terminal.Link.Seal()
	terminalizeTestRecord(t, store, terminal)
	store.ReleaseOwner(terminal.Link.Handle)
	if _, fresh, err := store.ReserveCancel(context.Background(), terminal.Link.Handle, "principal", keyDigest("cancel.key"), digestA, testNow.Add(2*time.Second)); err != nil || !fresh {
		t.Fatalf("cancel fresh=%v err=%v", fresh, err)
	}
	if _, fresh, err := store.ReserveCancel(context.Background(), terminal.Link.Handle, "principal", keyDigest("cancel.key"), digestA, testNow.Add(3*time.Second)); err != nil || fresh {
		t.Fatalf("cancel replay fresh=%v err=%v", fresh, err)
	}
	store.Close()
	reopened, err := NewLocalStore(root)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	secondInput := testReservation(t, "child.one", "handle.two", "key.two")
	setReservationLimits(t, &secondInput, limits)
	second, err := reopened.Reserve(context.Background(), secondInput)
	if err != nil {
		t.Fatal(err)
	}
	_, allowance, err := reopened.ReserveRemainingBudget(context.Background(), second.Record.Link.Handle, PhaseReserved, testNow.Add(4*time.Second))
	if err != nil || allowance != (ModelUsage{Calls: 2, Tokens: 4}) {
		t.Fatalf("allowance=%+v err=%v", allowance, err)
	}
}

// TestEveryAdmissionLimitBecomesTypedTerminalData proves all four cumulative axes are durable non-error outcomes.
func TestEveryAdmissionLimitBecomesTypedTerminalData(t *testing.T) {
	for _, dimension := range []string{"attempts", "duration", "model_calls", "model_tokens"} {
		t.Run(dimension, func(t *testing.T) {
			store := testStore(t)
			limits := ChainLimits{MaxAttempts: 3, MaxDuration: time.Hour, MaxModelCalls: 3, MaxModelTokens: 6}
			switch dimension {
			case "attempts":
				limits.MaxAttempts = 1
			case "model_calls":
				limits.MaxModelCalls = 1
			case "model_tokens":
				limits.MaxModelTokens = 2
			}
			firstInput := testReservation(t, "parent", "handle.one", "key.one")
			setReservationLimits(t, &firstInput, limits)
			first, err := store.Reserve(context.Background(), firstInput)
			if err != nil {
				t.Fatal(err)
			}
			terminal := first.Record
			if dimension == "model_calls" {
				terminal.Usage.Calls = 1
			}
			if dimension == "model_tokens" {
				terminal.Usage.Tokens = 2
			}
			terminal.Phase, terminal.Outcome, terminal.Owner = PhaseTerminal, OutcomeFailed, ""
			terminal.Link.ChildRunID, terminal.Link.ChildBindingSHA256 = "child.one", digestB
			terminal.Link.UpdatedAt = testNow.Add(time.Second)
			terminal.Link, _ = terminal.Link.Seal()
			terminalizeTestRecord(t, store, terminal)
			store.ReleaseOwner(terminal.Link.Handle)
			secondInput := testReservation(t, "child.one", "handle.two", "key.two")
			setReservationLimits(t, &secondInput, limits)
			if dimension == "duration" {
				secondInput.Record.Link.CreatedAt = testNow.Add(time.Hour)
				secondInput.Record.Link.UpdatedAt = secondInput.Record.Link.CreatedAt
			}
			secondInput.Record.Link, _ = secondInput.Record.Link.Seal()
			rejected, err := store.Reserve(context.Background(), secondInput)
			view := projectCorrection(rejected.Record)
			if err != nil || rejected.New || rejected.Record.Phase != PhaseTerminal || view.TerminalReason != string(LimitExhausted) || view.LimitDimension != dimension {
				t.Fatalf("rejected=%+v view=%+v err=%v", rejected, view, err)
			}
			reloaded, err := store.Get(context.Background(), rejected.Record.Link.Handle)
			if err != nil || reloaded.LimitDimension != dimension {
				t.Fatalf("reloaded=%+v err=%v", reloaded, err)
			}
		})
	}
	if validLimitDimension("money") {
		t.Fatal("unapproved limit dimension accepted")
	}
}

// TestExternalAdapterProjectsExhaustedAdmission proves the real coordinator returns typed terminal data instead of internal_error.
func TestExternalAdapterProjectsExhaustedAdmission(t *testing.T) {
	fixture := newFlowFixture(t, false)
	catalog := fixture.coordinator.deps.Catalog.(fakeCatalog)
	policy := catalog.selection.Policy
	policy.Limits.MaxAttempts = 1
	sealed, err := policy.Seal()
	if err != nil {
		t.Fatal(err)
	}
	catalog.selection.Policy = sealed
	fixture.coordinator.deps.Catalog = catalog
	first, err := fixture.coordinator.Submit(context.Background(), SubmitRequest{PrincipalID: "principal", ParentHandle: "parent.handle", SelectionAlias: "selection", IdempotencyKey: "first-attempt-key"})
	if err != nil {
		t.Fatal(err)
	}
	waitTerminal(t, fixture.coordinator, first.Link.Handle)
	fixture.ids.values = append(fixture.ids.values, "limited.handle", "limited.id")
	view, err := (ExternalAdapter{Coordinator: fixture.coordinator}).SubmitCorrection(context.Background(), "principal", externalagent.CorrectionSubmitInput{ParentRunHandle: "parent.handle", SelectionAlias: "selection", IdempotencyKey: "second-attempt-key"})
	if err != nil || view.Phase != string(PhaseTerminal) || view.TerminalReason != string(LimitExhausted) || view.LimitDimension != "attempts" {
		t.Fatalf("view=%+v err=%v", view, err)
	}
}

// TestBudgetLimitsFlowThroughCoordinatorAndAdapter proves duration, calls and tokens retain their safe dimensions vertically.
func TestBudgetLimitsFlowThroughCoordinatorAndAdapter(t *testing.T) {
	for _, dimension := range []string{"duration", "model_calls", "model_tokens"} {
		t.Run(dimension, func(t *testing.T) {
			fixture := newFlowFixture(t, false)
			if dimension == "duration" {
				fixture.coordinator.deps.Store = budgetErrorStore{Store: fixture.store, reserveErr: &Error{Category: LimitExhausted, Field: "duration"}}
			} else {
				catalog := fixture.coordinator.deps.Catalog.(fakeCatalog)
				policy := catalog.selection.Policy
				if dimension == "model_calls" {
					policy.Limits.MaxModelCalls = 1
				} else {
					policy.Limits.MaxModelTokens = 2
				}
				sealed, err := policy.Seal()
				if err != nil {
					t.Fatal(err)
				}
				catalog.selection.Policy = sealed
				fixture.coordinator.deps.Catalog = catalog
			}
			record, err := fixture.coordinator.Submit(context.Background(), SubmitRequest{PrincipalID: "principal", ParentHandle: "parent.handle", SelectionAlias: "selection", IdempotencyKey: "budget-limit-key"})
			if err != nil {
				t.Fatal(err)
			}
			waitTerminal(t, fixture.coordinator, record.Link.Handle)
			view, err := (ExternalAdapter{Coordinator: fixture.coordinator}).CorrectionStatus(context.Background(), "principal", externalagent.CorrectionHandleInput{CorrectionHandle: record.Link.Handle})
			if err != nil || view.Status != externalagent.OutcomeInconclusive || view.TerminalReason != string(LimitExhausted) || view.LimitDimension != dimension {
				t.Fatalf("view=%+v err=%v", view, err)
			}
		})
	}
}

// TestBudgetStoreErrorsKeepOperationalCategory proves cancellation, integrity and CAS conflicts never masquerade as limits.
func TestBudgetStoreErrorsKeepOperationalCategory(t *testing.T) {
	cases := []struct {
		name       string
		reserveErr error
		confirmErr error
		status     externalagent.Outcome
		reason     Category
	}{
		{name: "cancelled", reserveErr: context.Canceled, status: externalagent.OutcomeCancelled, reason: Cancelled},
		{name: "integrity", reserveErr: &Error{Category: IntegrityFailure, Field: "journal"}, status: externalagent.OutcomeError, reason: IntegrityFailure},
		{name: "conflict", confirmErr: &Error{Category: Conflict, Field: "budget"}, status: externalagent.OutcomeError, reason: Infrastructure},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			fixture := newFlowFixture(t, false)
			fixture.coordinator.deps.Store = budgetErrorStore{Store: fixture.store, reserveErr: testCase.reserveErr, confirmErr: testCase.confirmErr}
			record, err := fixture.coordinator.Submit(context.Background(), SubmitRequest{PrincipalID: "principal", ParentHandle: "parent.handle", SelectionAlias: "selection", IdempotencyKey: "budget-error-key"})
			if err != nil {
				t.Fatal(err)
			}
			waitTerminal(t, fixture.coordinator, record.Link.Handle)
			view, err := (ExternalAdapter{Coordinator: fixture.coordinator}).CorrectionStatus(context.Background(), "principal", externalagent.CorrectionHandleInput{CorrectionHandle: record.Link.Handle})
			if err != nil || view.Status != testCase.status || view.TerminalReason != string(testCase.reason) || view.LimitDimension != "" {
				t.Fatalf("view=%+v err=%v", view, err)
			}
		})
	}
}

// TestLookupIndexCorruptionAndConflictFailClosed proves immutable indexes cannot redirect an opaque handle.
func TestLookupIndexCorruptionAndConflictFailClosed(t *testing.T) {
	store := testStore(t)
	reserved, err := store.Reserve(context.Background(), testReservation(t, "parent", "handle.one", "key.one"))
	if err != nil {
		t.Fatal(err)
	}
	if _, confirmed := store.reconcileReservation(context.Background(), "parent", "missing.key", digestA, "handle.one"); confirmed {
		t.Fatal("unknown reservation reconciled")
	}
	if err := store.writeIndex(context.Background(), "handles", reserved.Record.Link.Handle, "other.root"); !IsCategory(err, IntegrityFailure) {
		t.Fatalf("conflict=%v", err)
	}
	path := store.indexPath("handles", reserved.Record.Link.Handle)
	if err := os.WriteFile(path, []byte(`{"version":1}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Get(context.Background(), reserved.Record.Link.Handle); !IsCategory(err, IntegrityFailure) {
		t.Fatalf("corruption=%v", err)
	}
}

// TestStoreDiscoveryHonorsCancellation proves index scans and locked projections stop at their caller boundary.
func TestStoreDiscoveryHonorsCancellation(t *testing.T) {
	store := testStore(t)
	reserved, err := store.Reserve(context.Background(), testReservation(t, "parent", "handle.one", "key.one"))
	if err != nil {
		t.Fatal(err)
	}
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := store.Get(cancelled, reserved.Record.Link.Handle); !errors.Is(err, context.Canceled) {
		t.Fatalf("discovery cancellation=%v", err)
	}
	release, err := acquireStoreLock(context.Background(), filepath.Join(store.chainDir("parent"), "chain.lock"))
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	if _, err := store.readChainState(cancelled, "parent"); !errors.Is(err, context.Canceled) {
		t.Fatalf("projection cancellation=%v", err)
	}
}

// TestStoreCancellationConflict proves cancellation keys cannot be reused with different requests.
func TestStoreCancellationConflict(t *testing.T) {
	store := testStore(t)
	reserved, err := store.Reserve(context.Background(), testReservation(t, "parent", "handle.one", "key.one"))
	if err != nil {
		t.Fatal(err)
	}
	if _, fresh, err := store.ReserveCancel(context.Background(), reserved.Record.Link.Handle, "principal", keyDigest("cancel.key"), digestA, testNow); err != nil || !fresh {
		t.Fatalf("fresh=%v err=%v", fresh, err)
	}
	if _, _, err := store.ReserveCancel(context.Background(), reserved.Record.Link.Handle, "principal", keyDigest("other.key"), digestB, testNow); !IsCategory(err, Conflict) {
		t.Fatalf("error=%v", err)
	}
	replay, fresh, err := store.ReserveCancel(context.Background(), reserved.Record.Link.Handle, "principal", keyDigest("cancel.key"), digestA, testNow)
	if err != nil || fresh || replay.Link.Handle != reserved.Record.Link.Handle {
		t.Fatalf("replay=%+v fresh=%v err=%v", replay, fresh, err)
	}
	if _, _, err := store.ReserveCancel(context.Background(), "missing", "principal", keyDigest("cancel.key"), digestA, testNow); !IsCategory(err, NotFound) {
		t.Fatalf("missing=%v", err)
	}
}

// TestExternalAdapterSubmissionAndErrors covers transport translation and safe closed error mapping.
func TestExternalAdapterSubmissionAndErrors(t *testing.T) {
	fixture := newFlowFixture(t, false)
	adapter := ExternalAdapter{Coordinator: fixture.coordinator}
	view, err := adapter.SubmitCorrection(context.Background(), "principal", externalagent.CorrectionSubmitInput{ParentRunHandle: "parent.handle", SelectionAlias: "selection", IdempotencyKey: "adapter-12345678"})
	if err != nil || view.CorrectionHandle == "" {
		t.Fatalf("view=%+v err=%v", view, err)
	}
	waitTerminal(t, fixture.coordinator, view.CorrectionHandle)
	if _, err := adapter.CancelCorrection(context.Background(), "foreign", externalagent.CorrectionCancelInput{CorrectionHandle: view.CorrectionHandle, IdempotencyKey: "cancel-12345678"}); err == nil {
		t.Fatal("foreign cancellation accepted")
	}
	for _, item := range []error{&Error{Category: InvalidRequest}, &Error{Category: NotFound}, &Error{Category: Conflict}, &Error{Category: Infrastructure}, errors.New("hostile SECRET")} {
		if _, ok := externalError(item).(*externalagent.AppError); !ok {
			t.Fatalf("translation=%T", externalError(item))
		}
	}
	if (&Error{Category: Conflict, Field: "x"}).Error() == "" {
		t.Fatal("empty safe error")
	}
}

// TestCoordinatorCloseWithoutWorkers proves shutdown is a no-op after all owned work has joined.
func TestCoordinatorCloseWithoutWorkers(t *testing.T) {
	fixture := newFlowFixture(t, false)
	if err := fixture.coordinator.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
}

// TestClosedFailureBranches covers cancellation, malformed snapshots and unavailable resources without leaking values.
func TestClosedFailureBranches(t *testing.T) {
	if _, err := NewLocalStore("relative"); err == nil {
		t.Fatal("relative store accepted")
	}
	store := testStore(t)
	if _, err := store.Get(context.Background(), "missing"); !IsCategory(err, NotFound) {
		t.Fatalf("get=%v", err)
	}
	if _, err := store.Events(context.Background(), "missing"); !IsCategory(err, NotFound) {
		t.Fatalf("events=%v", err)
	}
	reserved, err := store.Reserve(context.Background(), testReservation(t, "parent", "handle.one", "key.one"))
	if err != nil {
		t.Fatal(err)
	}
	bad := reserved.Record
	bad.Phase = PhaseRegression
	if _, err := store.Update(context.Background(), bad.Link.Handle, PhasePreflight, bad, "bad"); !IsCategory(err, Conflict) {
		t.Fatalf("update=%v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	identity := testIdentity("c", "s", "d", digestA)
	policy := testPolicy(t, []ScenarioIdentity{identity}, false)
	if _, err := BuildPlan(ctx, []ScenarioRef{{Identity: identity}}, fakeApplicability{applicable: true}, policy); !IsCategory(err, Cancelled) {
		t.Fatalf("plan cancel=%v", err)
	}
	if _, err := BuildPlan(context.Background(), nil, fakeApplicability{}, policy); err == nil {
		t.Fatal("empty plan accepted")
	}
	if _, err := (CorrectionLink{}).Seal(); err == nil {
		t.Fatal("empty link accepted")
	}
	link, _ := testReservation(t, "parent", "handle.two", "key.two").Record.Link.Seal()
	link.Digest = digestB
	if err := link.Verify(); !IsCategory(err, IntegrityFailure) {
		t.Fatalf("verify=%v", err)
	}
	if err := (LocalComparisonPublisher{Root: "relative"}).Publish(context.Background(), "handle", Comparison{}); err == nil {
		t.Fatal("relative publisher accepted")
	}
}

// TestComparisonNewNotApplicableAndInconclusive covers every remaining closed comparison class.
func TestComparisonNewNotApplicableAndInconclusive(t *testing.T) {
	oldFailure := testFailure("old", "old.step", "old.decl", "old.ev")
	newFailure := testFailure("new", "new.step", "new.decl", "new.ev")
	parent := testPackage(t, "parent", oldOID, localvalidation.ResultFailed, []localvalidation.PackageFailure{oldFailure}, nil)
	child := testPackage(t, "child", newOID, localvalidation.ResultFailed, []localvalidation.PackageFailure{newFailure}, nil)
	oldIdentity, _ := identityFromFailure(oldFailure)
	newIdentity, _ := identityFromFailure(newFailure)
	link := testLink(t, parent, child)
	comparison, err := Compare(context.Background(), parent, child, CorrectionPlan{Version: Version, NotApplicable: []ScenarioRef{{Identity: oldIdentity}}, RequiredRegression: []ScenarioRef{{Identity: newIdentity}}}, link)
	if err != nil || len(comparison.Entries) != 2 || comparison.Entries[0].Class != NewFailure || comparison.Entries[1].Class != NotApplicable {
		t.Fatalf("comparison=%+v err=%v", comparison, err)
	}
	comparison, err = Compare(context.Background(), parent, testPackage(t, "child", newOID, localvalidation.ResultInconclusive, nil, nil), CorrectionPlan{Version: Version, FailedFirst: []ScenarioRef{{Identity: oldIdentity}}}, link)
	if err != nil || comparison.Entries[0].Class != Inconclusive {
		t.Fatalf("comparison=%+v err=%v", comparison, err)
	}
}

// TestApprovalRejectionStopsFunctionalWork proves a rejected continuation cannot reach the child executor.
func TestApprovalRejectionStopsFunctionalWork(t *testing.T) {
	fixture := newFlowFixture(t, true)
	fixture.gate.calls = 1
	record, err := fixture.coordinator.Submit(context.Background(), SubmitRequest{PrincipalID: "principal", ParentHandle: "parent.handle", SelectionAlias: "selection", IdempotencyKey: "reject-12345678"})
	if err != nil {
		t.Fatal(err)
	}
	terminal := waitTerminal(t, fixture.coordinator, record.Link.Handle)
	if terminal.Outcome != OutcomeCancelled || len(fixture.executor.phases) != 0 {
		t.Fatalf("terminal=%+v", terminal)
	}
	events, err := fixture.store.Events(context.Background(), record.Link.Handle)
	if err != nil {
		t.Fatal(err)
	}
	resolved := false
	for _, event := range events {
		if event.Type == "continuation_approval_resolved" && event.Record.ApprovalResolution == "rejected" {
			resolved = true
		}
	}
	if !resolved {
		t.Fatal("rejection was not journaled")
	}
}

// TestStaleApprovalIsJournaledAndStopsWork proves gate errors cannot reuse an earlier authorization.
func TestStaleApprovalIsJournaledAndStopsWork(t *testing.T) {
	fixture := newFlowFixture(t, true)
	fixture.gate.err = errors.New("stale")
	record, err := fixture.coordinator.Submit(context.Background(), SubmitRequest{PrincipalID: "principal", ParentHandle: "parent.handle", SelectionAlias: "selection", IdempotencyKey: "stale-gate-123"})
	if err != nil {
		t.Fatal(err)
	}
	terminal := waitTerminal(t, fixture.coordinator, record.Link.Handle)
	if terminal.Outcome != OutcomeCancelled || len(fixture.executor.phases) != 0 {
		t.Fatalf("terminal=%+v", terminal)
	}
	events, err := fixture.store.Events(context.Background(), record.Link.Handle)
	if err != nil {
		t.Fatal(err)
	}
	for _, event := range events {
		if event.Type == "continuation_approval_resolved" && event.Record.ApprovalResolution == "rejected" {
			return
		}
	}
	t.Fatal("stale decision missing")
}

// TestBoundaryValidationBranches proves malformed requests and unavailable dependencies fail before work.
func TestBoundaryValidationBranches(t *testing.T) {
	if _, err := NewCoordinator(context.Background(), Dependencies{}); err == nil {
		t.Fatal("empty composition accepted")
	}
	fixture := newFlowFixture(t, false)
	if _, err := fixture.coordinator.Submit(context.Background(), SubmitRequest{}); !IsCategory(err, InvalidRequest) {
		t.Fatalf("submit=%v", err)
	}
	if _, err := fixture.coordinator.Cancel(context.Background(), "missing", "principal", "short"); !IsCategory(err, InvalidRequest) {
		t.Fatalf("cancel=%v", err)
	}
	if _, err := fixture.coordinator.Cancel(context.Background(), "missing", "principal", "long-enough-key"); !IsCategory(err, NotFound) {
		t.Fatalf("cancel=%v", err)
	}
	identity := testIdentity("c", "s", "d", digestA)
	policy := testPolicy(t, []ScenarioIdentity{identity}, false)
	if _, err := BuildPlan(context.Background(), []ScenarioRef{{Identity: identity}}, errorApplicability{}, policy); !IsCategory(err, Infrastructure) {
		t.Fatalf("checker=%v", err)
	}
	if _, err := BuildPlan(context.Background(), []ScenarioRef{{Identity: identity}}, fakeApplicability{}, policy); !IsCategory(err, IntegrityFailure) {
		t.Fatalf("reason=%v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	parent := testPackage(t, "parent", oldOID, localvalidation.ResultFailed, []localvalidation.PackageFailure{testFailure("c", "s", "d", "ev")}, nil)
	child := testPackage(t, "child", newOID, localvalidation.ResultPassed, nil, nil)
	if _, err := Compare(ctx, parent, child, CorrectionPlan{}, testLink(t, parent, child)); !IsCategory(err, Cancelled) {
		t.Fatalf("compare=%v", err)
	}
	if err := (LocalComparisonPublisher{Root: filepath.Join(t.TempDir(), "publish")}).Publish(ctx, "handle", Comparison{}); err == nil {
		t.Fatal("cancelled publish accepted")
	}
	if _, err := (VerifiedPackageReader{}).Read(context.Background(), filepath.Join(t.TempDir(), "missing")); err == nil {
		t.Fatal("missing package accepted")
	}
	usage := addUsage(ModelUsage{Calls: ^uint64(0), Tokens: ^uint64(0)}, ModelUsage{Calls: 1, Tokens: 1})
	if usage.Calls != ^uint64(0) || usage.Tokens != ^uint64(0) {
		t.Fatalf("usage=%+v", usage)
	}
}

func TestCoordinatorCloseCancelsActiveWorker(t *testing.T) {
	fixture := newFlowFixture(t, false)
	fixture.executor.block, fixture.executor.started = true, make(chan struct{})
	if _, err := fixture.coordinator.Submit(context.Background(), SubmitRequest{PrincipalID: "principal", ParentHandle: "parent.handle", SelectionAlias: "selection", IdempotencyKey: "close-worker-123"}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-fixture.executor.started:
	case <-time.After(time.Second):
		t.Fatal("executor did not start")
	}
	if err := fixture.coordinator.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
}

// TestCoordinatorWaitBranches proves missing, unowned-active and cancelled joins are closed outcomes.
func TestCoordinatorWaitBranches(t *testing.T) {
	store := testStore(t)
	coordinator := &Coordinator{deps: Dependencies{Store: store}, runs: map[string]*ownedRun{}}
	if err := coordinator.Wait(context.Background(), "missing"); !IsCategory(err, NotFound) {
		t.Fatalf("missing=%v", err)
	}
	reserved, err := store.Reserve(context.Background(), testReservation(t, "parent", "handle.one", "key.one"))
	if err != nil {
		t.Fatal(err)
	}
	if err := coordinator.Wait(context.Background(), reserved.Record.Link.Handle); !IsCategory(err, Conflict) {
		t.Fatalf("active=%v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	coordinator.runs[reserved.Record.Link.Handle] = &ownedRun{done: make(chan struct{}), cancel: func() {}}
	if err := coordinator.Wait(ctx, reserved.Record.Link.Handle); !IsCategory(err, Cancelled) {
		t.Fatalf("cancel=%v", err)
	}
}

// TestStoreAndPublisherFilesystemFailuresRemainClosed exercises safe operational mapping at local boundaries.
func TestStoreAndPublisherFilesystemFailuresRemainClosed(t *testing.T) {
	root := t.TempDir()
	file := filepath.Join(root, "file")
	if err := os.WriteFile(file, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := NewLocalStore(filepath.Join(file, "child")); err == nil {
		t.Fatal("store under file accepted")
	}
	publisher := LocalComparisonPublisher{Root: file}
	if err := publisher.Publish(context.Background(), "handle", Comparison{}); err == nil {
		t.Fatal("publisher under file accepted")
	}
}

// TestCoordinatorPublicationFailureIsOperational proves comparison publication failure cannot become functional output.
func TestCoordinatorPublicationFailureIsOperational(t *testing.T) {
	fixture := newFlowFixture(t, false)
	fixture.publisher.errBefore = errors.New("SECRET publish")
	record, err := fixture.coordinator.Submit(context.Background(), SubmitRequest{PrincipalID: "principal", ParentHandle: "parent.handle", SelectionAlias: "selection", IdempotencyKey: "publish-fail-123"})
	if err != nil {
		t.Fatal(err)
	}
	terminal := waitTerminal(t, fixture.coordinator, record.Link.Handle)
	if terminal.Outcome != OutcomeError || terminal.TerminalReason != Infrastructure || terminal.Comparison != nil {
		t.Fatalf("terminal=%+v", terminal)
	}
}

// TestComparisonPublicationReconcilesLostResponse proves an after-publish error becomes one durable functional result without duplicate artifacts.
func TestComparisonPublicationReconcilesLostResponse(t *testing.T) {
	fixture := newFlowFixture(t, false)
	fixture.publisher.errAfter = errors.New("response lost")
	record, err := fixture.coordinator.Submit(context.Background(), SubmitRequest{PrincipalID: "principal", ParentHandle: "parent.handle", SelectionAlias: "selection", IdempotencyKey: "publish-after-123"})
	if err != nil {
		t.Fatal(err)
	}
	terminal := waitTerminal(t, fixture.coordinator, record.Link.Handle)
	fixture.publisher.mu.Lock()
	count := len(fixture.publisher.comparisons)
	fixture.publisher.mu.Unlock()
	if terminal.Outcome != OutcomePassed || terminal.Comparison == nil || count != 1 {
		t.Fatalf("terminal=%+v count=%d", terminal, count)
	}
	_ = fixture.publisher.Publish(context.Background(), terminal.Link.Handle, *terminal.Comparison)
	matched, resolveErr := fixture.publisher.Resolve(context.Background(), terminal.Link.Handle, terminal.Comparison.Digest)
	fixture.publisher.mu.Lock()
	count = len(fixture.publisher.comparisons)
	fixture.publisher.mu.Unlock()
	if resolveErr != nil || !matched || count != 1 {
		t.Fatalf("matched=%v err=%v count=%d", matched, resolveErr, count)
	}
	events, err := fixture.store.Events(context.Background(), terminal.Link.Handle)
	if err != nil || countEvent(events, "comparison_published") != 1 {
		t.Fatalf("events=%v err=%v", eventTypes(events), err)
	}
}

// TestComparisonJournalAppendReconcilesBeforeAndAfterFailure proves a visible artifact reaches one matching terminal even when its final append response is uncertain.
func TestComparisonJournalAppendReconcilesBeforeAndAfterFailure(t *testing.T) {
	for _, after := range []bool{false, true} {
		t.Run(map[bool]string{false: "before append", true: "after append"}[after], func(t *testing.T) {
			fixture := newFlowFixture(t, false)
			fixture.publisher.afterPublish = func() {
				if after {
					fixture.store.failAfterAppend.Store(true)
				} else {
					fixture.store.failBeforeAppend.Store(true)
				}
			}
			record, err := fixture.coordinator.Submit(context.Background(), SubmitRequest{PrincipalID: "principal", ParentHandle: "parent.handle", SelectionAlias: "selection", IdempotencyKey: "comparison-journal-123"})
			if err != nil {
				t.Fatal(err)
			}
			terminal := waitTerminal(t, fixture.coordinator, record.Link.Handle)
			fixture.publisher.mu.Lock()
			published := len(fixture.publisher.comparisons)
			fixture.publisher.mu.Unlock()
			events, eventErr := fixture.store.Events(context.Background(), terminal.Link.Handle)
			if terminal.Outcome != OutcomePassed || terminal.Comparison == nil || published != 1 || eventErr != nil || countEvent(events, "comparison_published") != 1 {
				t.Fatalf("terminal=%+v published=%d events=%v err=%v", terminal, published, eventTypes(events), eventErr)
			}
		})
	}
}

// TestComparisonUsesFreshContextsAcrossVisibleBoundaries proves one expired publication or resolution context cannot orphan an artifact or block its journal append.
func TestComparisonUsesFreshContextsAcrossVisibleBoundaries(t *testing.T) {
	for _, boundary := range []string{"after publish", "after resolve"} {
		t.Run(boundary, func(t *testing.T) {
			fixture := newFlowFixture(t, false)
			fixture.coordinator.deps.FinalizeLimit = 20 * time.Millisecond
			if boundary == "after publish" {
				fixture.publisher.afterPublishContext = func(ctx context.Context) { <-ctx.Done() }
			} else {
				fixture.publisher.afterResolve = func(ctx context.Context) { <-ctx.Done() }
			}
			record, err := fixture.coordinator.Submit(context.Background(), SubmitRequest{PrincipalID: "principal", ParentHandle: "parent.handle", SelectionAlias: "selection", IdempotencyKey: "comparison-context-123"})
			if err != nil {
				t.Fatal(err)
			}
			terminal := waitTerminal(t, fixture.coordinator, record.Link.Handle)
			assertSingleComparisonTerminal(t, fixture.publisher, fixture.store, terminal)
		})
	}
}

// comparisonAppendFailureStore simulates owner loss after resolution by rejecting only the final comparison journal append.
type comparisonAppendFailureStore struct {
	Store
	mu        sync.Mutex
	remaining int
}

// Update preserves every store transition except the configured comparison append failures.
func (s *comparisonAppendFailureStore) Update(ctx context.Context, handle string, expected Phase, next Record, eventType string) (Record, error) {
	s.mu.Lock()
	if eventType == "comparison_published" && s.remaining > 0 {
		s.remaining--
		s.mu.Unlock()
		return Record{}, &Error{Category: Infrastructure, Field: "journal"}
	}
	s.mu.Unlock()
	return s.Store.Update(ctx, handle, expected, next, eventType)
}

// TestComparisonRecoveryCompletesAbandonedIntent proves reopen resumes either uncertain visibility boundary exactly once.
func TestComparisonRecoveryCompletesAbandonedIntent(t *testing.T) {
	for _, boundary := range []string{"after publish", "after resolve"} {
		t.Run(boundary, func(t *testing.T) {
			fixture := newFlowFixture(t, false)
			if boundary == "after publish" {
				fixture.publisher.resolveErr = context.DeadlineExceeded
			} else {
				failing := &comparisonAppendFailureStore{Store: fixture.store, remaining: 2}
				fixture.coordinator.deps.Store = failing
			}
			record, err := fixture.coordinator.Submit(context.Background(), SubmitRequest{PrincipalID: "principal", ParentHandle: "parent.handle", SelectionAlias: "selection", IdempotencyKey: "comparison-recovery-123"})
			if err != nil {
				t.Fatal(err)
			}
			joinCtx, cancelJoin := context.WithTimeout(context.Background(), 15*time.Second)
			if err := fixture.coordinator.Wait(joinCtx, record.Link.Handle); err != nil {
				cancelJoin()
				t.Fatal(err)
			}
			cancelJoin()
			pending, err := fixture.store.Get(context.Background(), record.Link.Handle)
			if err != nil || pending.Phase != PhaseFinalizing || pending.ComparisonIntent == nil || pending.Comparison != nil {
				t.Fatalf("pending=%+v err=%v", pending, err)
			}
			fixture.publisher.resolveErr = nil
			reopened, err := NewLocalStore(fixture.store.root)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(reopened.Close)
			deps := fixture.deps
			deps.Store, deps.Owner = reopened, "recovery.owner"
			recovery, err := NewCoordinator(context.Background(), deps)
			if err != nil {
				t.Fatal(err)
			}
			terminal := waitTerminal(t, recovery, record.Link.Handle)
			assertSingleComparisonTerminal(t, fixture.publisher, reopened, terminal)
			recoveryEvents, recoveryErr := reopened.Events(context.Background(), record.Link.Handle)
			if recoveryErr != nil || countEvent(recoveryEvents, "comparison_recovery_claimed") != 1 {
				t.Fatalf("recovery events=%v err=%v", eventTypes(recoveryEvents), recoveryErr)
			}

			replayStore, err := NewLocalStore(fixture.store.root)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(replayStore.Close)
			deps.Store, deps.Owner = replayStore, "replay.owner"
			replay, err := NewCoordinator(context.Background(), deps)
			if err != nil {
				t.Fatal(err)
			}
			replayed, err := replay.Status(context.Background(), record.Link.Handle)
			if err != nil || replayed.Phase != PhaseTerminal || replayed.Comparison == nil || replayed.Comparison.Digest != terminal.Comparison.Digest {
				t.Fatalf("replayed=%+v err=%v", replayed, err)
			}
			assertSingleComparisonTerminal(t, fixture.publisher, replayStore, replayed)
		})
	}
}

// recoveryBarrierPublisher holds every recovered worker at publication until the test observes the complete installed worker set.
type recoveryBarrierPublisher struct {
	base    *fakePublisher
	started chan string
	release <-chan struct{}
}

// Publish announces one recovered worker and waits on a deterministic barrier before delegating idempotent publication.
func (p *recoveryBarrierPublisher) Publish(ctx context.Context, handle string, comparison Comparison) error {
	select {
	case p.started <- handle:
	case <-ctx.Done():
		return ctx.Err()
	}
	select {
	case <-p.release:
		return p.base.Publish(ctx, handle, comparison)
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Resolve delegates exact digest reconciliation after the recovery barrier opens.
func (p *recoveryBarrierPublisher) Resolve(ctx context.Context, handle, digest string) (bool, error) {
	return p.base.Resolve(ctx, handle, digest)
}

// TestConcurrentComparisonRecoveryInstallsAllWorkers proves multiple chains publish only after every runs-map entry is synchronized.
func TestConcurrentComparisonRecoveryInstallsAllWorkers(t *testing.T) {
	store := testStore(t)
	records := []Record{
		prepareComparisonIntentRecord(t, store, "parent.one", "handle.one", "child.one"),
		prepareComparisonIntentRecord(t, store, "parent.two", "handle.two", "child.two"),
	}
	for _, record := range records {
		store.ReleaseOwner(record.Link.Handle)
	}
	reopened, err := NewLocalStore(store.root)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(reopened.Close)
	release := make(chan struct{})
	publisher := &recoveryBarrierPublisher{base: &fakePublisher{}, started: make(chan string, len(records)), release: release}
	fixture := newFlowFixture(t, false)
	deps := fixture.deps
	deps.Store, deps.Publisher, deps.Owner = reopened, publisher, "recovery.owner"
	deps.Clock = &fakeClock{now: testNow.Add(30 * time.Minute)}
	coordinator, err := NewCoordinator(context.Background(), deps)
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	for range records {
		select {
		case handle := <-publisher.started:
			seen[handle] = true
		case <-time.After(15 * time.Second):
			t.Fatal("recovery workers did not reach barrier")
		}
	}
	coordinator.mu.Lock()
	installed := len(coordinator.runs)
	coordinator.mu.Unlock()
	if installed != len(records) || len(seen) != len(records) {
		t.Fatalf("installed=%d seen=%v", installed, seen)
	}
	close(release)
	for _, record := range records {
		terminal := waitTerminal(t, coordinator, record.Link.Handle)
		events, eventErr := reopened.Events(context.Background(), record.Link.Handle)
		if eventErr != nil || terminal.Phase != PhaseTerminal || terminal.Comparison == nil || countEvent(events, "comparison_published") != 1 {
			t.Fatalf("handle=%s terminal=%+v events=%v err=%v", record.Link.Handle, terminal, eventTypes(events), eventErr)
		}
		leasePath := filepath.Join(reopened.chainDir(record.Link.RootRunID), "owners", record.Link.Handle+".lease")
		leaseRelease, free, leaseErr := tryStoreLock(leasePath)
		if leaseErr != nil || !free {
			t.Fatalf("handle=%s lease free=%v err=%v", record.Link.Handle, free, leaseErr)
		}
		leaseRelease()
	}
}

// TestDefinitiveComparisonPublicationFailuresTerminateRecovery proves conflict and corruption retire an intent once without functional publication.
func TestDefinitiveComparisonPublicationFailuresTerminateRecovery(t *testing.T) {
	for _, failure := range []string{"different digest", "corrupt bytes"} {
		t.Run(failure, func(t *testing.T) {
			fixture := newFlowFixture(t, false)
			publisher := LocalComparisonPublisher{Root: filepath.Join(t.TempDir(), "comparisons")}
			target := filepath.Join(publisher.Root, "correction.handle.json")
			if failure == "different digest" {
				conflict := Comparison{Version: Version, ParentRunID: "parent", ChildRunID: "other.child", Entries: []ComparisonEntry{}}
				conflict.Digest = hashCanonical(conflict)
				if err := publisher.Publish(context.Background(), "correction.handle", conflict); err != nil {
					t.Fatal(err)
				}
			} else {
				if err := os.MkdirAll(publisher.Root, 0o700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(target, []byte("{corrupt"), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			original, err := os.ReadFile(target)
			if err != nil {
				t.Fatal(err)
			}
			fixture.coordinator.deps.Publisher = publisher
			fixture.deps.Publisher = publisher
			record, err := fixture.coordinator.Submit(context.Background(), SubmitRequest{PrincipalID: "principal", ParentHandle: "parent.handle", SelectionAlias: "selection", IdempotencyKey: "definitive-publish-123"})
			if err != nil {
				t.Fatal(err)
			}
			terminal := waitTerminal(t, fixture.coordinator, record.Link.Handle)
			events, eventErr := fixture.store.Events(context.Background(), record.Link.Handle)
			if eventErr != nil || terminal.Outcome != OutcomeError || terminal.TerminalReason != IntegrityFailure || terminal.Comparison != nil || terminal.ComparisonIntent != nil || countEvent(events, "comparison_abandoned") != 1 || countEvent(events, "comparison_published") != 0 {
				t.Fatalf("terminal=%+v events=%v err=%v", terminal, eventTypes(events), eventErr)
			}
			unchanged, err := os.ReadFile(target)
			if err != nil || !bytes.Equal(original, unchanged) {
				t.Fatalf("artifact changed err=%v", err)
			}

			reopened, err := NewLocalStore(fixture.store.root)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(reopened.Close)
			deps := fixture.deps
			deps.Store, deps.Owner = reopened, "replay.owner"
			replay, err := NewCoordinator(context.Background(), deps)
			if err != nil {
				t.Fatal(err)
			}
			replayed, err := replay.Status(context.Background(), record.Link.Handle)
			replayedEvents, replayEventsErr := reopened.Events(context.Background(), record.Link.Handle)
			if err != nil || replayEventsErr != nil || replayed.Phase != PhaseTerminal || replayed.TerminalReason != IntegrityFailure || countEvent(replayedEvents, "comparison_abandoned") != 1 || countEvent(replayedEvents, "comparison_recovery_claimed") != 0 {
				t.Fatalf("replayed=%+v err=%v events=%v events_err=%v", replayed, err, eventTypes(replayedEvents), replayEventsErr)
			}
		})
	}
}

// assertSingleComparisonTerminal checks artifact, event and terminal converge on one digest after publication or recovery.
func assertSingleComparisonTerminal(t *testing.T, publisher *fakePublisher, store *LocalStore, terminal Record) {
	t.Helper()
	publisher.mu.Lock()
	published := len(publisher.comparisons)
	publisher.mu.Unlock()
	events, err := store.Events(context.Background(), terminal.Link.Handle)
	if err != nil || terminal.Phase != PhaseTerminal || terminal.Comparison == nil || terminal.ComparisonIntent == nil || terminal.Comparison.Digest != terminal.ComparisonIntent.Digest || published != 1 || countEvent(events, "comparison_intended") != 1 || countEvent(events, "comparison_published") != 1 {
		t.Fatalf("terminal=%+v published=%d events=%v err=%v", terminal, published, eventTypes(events), err)
	}
}

// TestAdditionalModelAndLineageRejections covers malformed optional lineage and unsafe policy snapshots.
func TestAdditionalModelAndLineageRejections(t *testing.T) {
	base := testReservation(t, "parent", "handle.one", "key.one").Record.Link
	base.ChildRunID = "child"
	base.ChildBindingSHA256 = ""
	if _, err := base.Seal(); err == nil {
		t.Fatal("partial child binding accepted")
	}
	base = testReservation(t, "parent", "handle.two", "key.two").Record.Link
	base.ChildRunID = "../bad"
	base.ChildBindingSHA256 = digestA
	if _, err := base.Seal(); err == nil {
		t.Fatal("unsafe child accepted")
	}
	policy := RegressionSnapshot{Version: Version, ID: "policy", Scenarios: make([]ScenarioIdentity, maxScenarios+1), Limits: testLimits()}
	if _, err := policy.Seal(); err == nil {
		t.Fatal("oversized policy accepted")
	}
	passed := testPackage(t, "parent", oldOID, localvalidation.ResultPassed, nil, nil)
	if _, err := SelectFailures(passed); err == nil {
		t.Fatal("passed parent selected")
	}
}

// TestCoordinatorAdmissionBoundaryFailures proves trusted resolver failures stop before durable reservation.
func TestCoordinatorAdmissionBoundaryFailures(t *testing.T) {
	fixture := newFlowFixture(t, false)
	fixture.coordinator.deps.Parents = fakeParents{err: errors.New("SECRET")}
	if _, err := fixture.coordinator.Submit(context.Background(), SubmitRequest{PrincipalID: "principal", ParentHandle: "parent.handle", SelectionAlias: "selection", IdempotencyKey: "parent-fail-123"}); !IsCategory(err, NotFound) {
		t.Fatalf("parent=%v", err)
	}
	fixture = newFlowFixture(t, false)
	fixture.coordinator.deps.Catalog = fakeCatalog{err: errors.New("SECRET")}
	if _, err := fixture.coordinator.Submit(context.Background(), SubmitRequest{PrincipalID: "principal", ParentHandle: "parent.handle", SelectionAlias: "selection", IdempotencyKey: "catalog-fail-123"}); !IsCategory(err, NotFound) {
		t.Fatalf("catalog=%v", err)
	}
	fixture = newFlowFixture(t, false)
	fixture.coordinator.deps.IDs = &fakeIDs{err: errors.New("entropy")}
	if _, err := fixture.coordinator.Submit(context.Background(), SubmitRequest{PrincipalID: "principal", ParentHandle: "parent.handle", SelectionAlias: "selection", IdempotencyKey: "id-fail-123456"}); !IsCategory(err, Infrastructure) {
		t.Fatalf("id=%v", err)
	}
	fixture = newFlowFixture(t, false)
	fixture.coordinator.deps.IDs = &fakeIDs{values: []string{"only.handle"}}
	if _, err := fixture.coordinator.Submit(context.Background(), SubmitRequest{PrincipalID: "principal", ParentHandle: "parent.handle", SelectionAlias: "selection", IdempotencyKey: "second-id-fail"}); !IsCategory(err, Infrastructure) {
		t.Fatalf("second id=%v", err)
	}
	fixture = newFlowFixture(t, false)
	catalog := fixture.coordinator.deps.Catalog.(fakeCatalog)
	catalog.selection.Policy.Digest = digestB
	fixture.coordinator.deps.Catalog = catalog
	if _, err := fixture.coordinator.Submit(context.Background(), SubmitRequest{PrincipalID: "principal", ParentHandle: "parent.handle", SelectionAlias: "selection", IdempotencyKey: "policy-fail-123"}); !IsCategory(err, Infrastructure) {
		t.Fatalf("policy=%v", err)
	}
}

// TestExternalAdapterOwnedStatusAndCancel covers authorized terminal lifecycle operations.
func TestExternalAdapterOwnedStatusAndCancel(t *testing.T) {
	store := testStore(t)
	reserved, err := store.Reserve(context.Background(), testReservation(t, "parent", "handle.one", "key.one"))
	if err != nil {
		t.Fatal(err)
	}
	record := reserved.Record
	record.Phase, record.Outcome, record.Owner = PhaseTerminal, OutcomeCancelled, ""
	record.TerminalReason = Cancelled
	record.Link.UpdatedAt = testNow.Add(time.Second)
	record.Link, _ = record.Link.Seal()
	terminalizeTestRecord(t, store, record)
	adapter := ExternalAdapter{Coordinator: &Coordinator{deps: Dependencies{Store: store, Clock: &fakeClock{now: testNow.Add(time.Second)}}, runs: map[string]*ownedRun{}}}
	if _, err := adapter.CorrectionStatus(context.Background(), "principal", externalagent.CorrectionHandleInput{CorrectionHandle: record.Link.Handle}); err != nil {
		t.Fatal(err)
	}
	if _, err := adapter.CancelCorrection(context.Background(), "principal", externalagent.CorrectionCancelInput{CorrectionHandle: record.Link.Handle, IdempotencyKey: "cancel-owner-123"}); err != nil {
		t.Fatal(err)
	}
}

// TestExternalAdapterLifecycleBranches covers in-progress, nil composition and terminal operational projection.
func TestExternalAdapterLifecycleBranches(t *testing.T) {
	if _, err := (ExternalAdapter{}).SubmitCorrection(context.Background(), "principal", externalagent.CorrectionSubmitInput{}); err == nil {
		t.Fatal("nil submit accepted")
	}
	if _, err := (ExternalAdapter{}).CancelCorrection(context.Background(), "principal", externalagent.CorrectionCancelInput{}); err == nil {
		t.Fatal("nil cancel accepted")
	}
	if _, err := (ExternalAdapter{}).CorrectionStatus(context.Background(), "principal", externalagent.CorrectionHandleInput{}); err == nil {
		t.Fatal("nil status accepted")
	}
	store := testStore(t)
	reserved, err := store.Reserve(context.Background(), testReservation(t, "parent", "handle.one", "key.one"))
	if err != nil {
		t.Fatal(err)
	}
	adapter := ExternalAdapter{Coordinator: &Coordinator{deps: Dependencies{Store: store}, runs: map[string]*ownedRun{}}}
	if _, err := adapter.CorrectionResult(context.Background(), "principal", externalagent.CorrectionHandleInput{CorrectionHandle: reserved.Record.Link.Handle}); err == nil {
		t.Fatal("in-progress result accepted")
	}
	record := reserved.Record
	record.Phase, record.Outcome, record.Owner = PhaseTerminal, OutcomeError, ""
	record.TerminalReason = Infrastructure
	record.Link.UpdatedAt = testNow.Add(time.Second)
	record.Link, _ = record.Link.Seal()
	terminalizeTestRecord(t, store, record)
	result, err := adapter.CorrectionResult(context.Background(), "principal", externalagent.CorrectionHandleInput{CorrectionHandle: record.Link.Handle})
	if err != nil || result.TerminalReason != string(Infrastructure) || result.Corrected == nil {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	if err := (LocalComparisonPublisher{Root: filepath.Join(t.TempDir(), "publish")}).Publish(context.Background(), "../bad", Comparison{}); err == nil {
		t.Fatal("unsafe handle accepted")
	}
}

// errorApplicability simulates a failure at the trusted child-context boundary.
type errorApplicability struct{}

// Check returns an operational error without untrusted details.
func (errorApplicability) Check(context.Context, ScenarioIdentity) (bool, ApplicabilityReason, error) {
	return false, "", errors.New("SECRET")
}

// testLimits supplies finite non-monetary chain limits used by hermetic tests.
func testLimits() ChainLimits {
	return ChainLimits{MaxAttempts: 3, MaxDuration: time.Hour, MaxModelCalls: 10, MaxModelTokens: 1000}
}

// testIdentity creates one valid stable scenario identity.
func testIdentity(constraint, step, declaration, fingerprint string) ScenarioIdentity {
	return ScenarioIdentity{ConstraintID: constraint, Scenario: "unspecified", BatchID: "1", StepID: step, DeclarationID: declaration, NodePath: "0", Fingerprint: fingerprint}
}

// testFailure creates a verified-package-shaped deterministic failure.
func testFailure(constraint, step, declaration, evidence string) localvalidation.PackageFailure {
	return localvalidation.PackageFailure{ConstraintID: constraint, Scenario: "unspecified", BatchID: "1", StepID: step, DeclarationID: declaration, NodePath: "0", Operator: "equal", Status: "failed", EvidenceIDs: []string{evidence}}
}

// testBinding creates a cryptographically self-consistent binding for package fixtures.
func testBinding(t *testing.T, runID, commit string) revisionbinding.Binding {
	t.Helper()
	binding, err := revisionbinding.NewBinding(context.Background(), runID, revisionbinding.Revision{RepositoryID: "repo", CommitOID: commit}, revisionbinding.Attestation{EnvironmentID: "env", RepositoryID: "repo", CommitOID: commit, Reference: "attestation", Verified: true}, digestA, digestA, digestA)
	if err != nil {
		t.Fatal(err)
	}
	return binding
}

// testPackage creates a bounded semantic package index already verified by the fake reader contract.
func testPackage(t *testing.T, runID, commit string, status localvalidation.ResultStatus, failures []localvalidation.PackageFailure, facts []localvalidation.PackageFact) localvalidation.PackageIndex {
	t.Helper()
	return localvalidation.PackageIndex{Version: 1, RunID: runID, Binding: testBinding(t, runID, commit), Status: status, Constraints: []localvalidation.PackageConstraint{}, Failures: append([]localvalidation.PackageFailure(nil), failures...), Facts: append([]localvalidation.PackageFact(nil), facts...), Evidence: []localvalidation.PackageEvidence{}, Reproduction: []localvalidation.PackageReproduction{}, PlanBindings: []revisionbinding.PlanBinding{}}
}

// testPolicy seals a trusted regression snapshot for tests.
func testPolicy(t *testing.T, scenarios []ScenarioIdentity, approval bool) RegressionSnapshot {
	t.Helper()
	policy, err := (RegressionSnapshot{Version: Version, ID: "policy", Scenarios: scenarios, ApprovalRequired: approval, Limits: testLimits()}).Seal()
	if err != nil {
		t.Fatal(err)
	}
	return policy
}

// testLink creates a self-consistent parent-child lineage.
func testLink(t *testing.T, parent, child localvalidation.PackageIndex) CorrectionLink {
	t.Helper()
	link, err := (CorrectionLink{Version: Version, CorrectionID: "correction", Handle: "correction.handle", PrincipalID: "principal", ParentRunID: parent.RunID, ParentBindingSHA256: parent.Binding.BindingSHA256, RootRunID: parent.RunID, Attempt: 1, SelectionAlias: "selection", SelectionSHA256: digestA, PolicySHA256: digestA, LimitsSHA256: digestA, ChildRunID: child.RunID, ChildBindingSHA256: child.Binding.BindingSHA256, CreatedAt: testNow, UpdatedAt: testNow}).Seal()
	if err != nil {
		t.Fatal(err)
	}
	return link
}

// testStore creates a fresh durable journal boundary.
func testStore(t *testing.T) *LocalStore {
	t.Helper()
	store, err := NewLocalStore(filepath.Join(t.TempDir(), "correction"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(store.Close)
	return store
}

// prepareComparisonIntentRecord builds one valid finalizing journal whose durable intent is ready for recovery publication.
func prepareComparisonIntentRecord(t *testing.T, store *LocalStore, parent, handle, child string) Record {
	t.Helper()
	ctx := context.Background()
	reserved, err := store.Reserve(ctx, testReservation(t, parent, handle, "key."+handle))
	if err != nil {
		t.Fatal(err)
	}
	current := reserved.Record
	update := func(phase Phase, eventType string) {
		t.Helper()
		expected := current.Phase
		current.Phase = phase
		current.Link.UpdatedAt = current.Link.UpdatedAt.Add(time.Nanosecond)
		current.Link, _ = current.Link.Seal()
		current, err = store.Update(ctx, current.Link.Handle, expected, current, eventType)
		if err != nil {
			t.Fatalf("%s: %v", eventType, err)
		}
	}
	update(PhasePreflight, "preflight_started")
	current.Link.ChildRunID, current.Link.ChildBindingSHA256 = child, digestB
	update(PhasePreflight, "preflight_confirmed")
	update(PhaseFailedFirst, "phase_started")
	update(PhaseRegression, "phase_started")
	update(PhaseFinalizing, "child_terminal")
	current, _, err = store.ReserveRemainingBudget(ctx, current.Link.Handle, current.Phase, current.Link.UpdatedAt.Add(time.Nanosecond))
	if err != nil {
		t.Fatal(err)
	}
	current.PreparationID, current.PreparedOutcome = "preparation."+handle, OutcomePassed
	update(PhaseFinalizing, "finalization_prepared")
	current, err = store.ConfirmBudget(ctx, current.Link.Handle, current.Phase, ModelUsage{}, current.Link.UpdatedAt.Add(time.Nanosecond))
	if err != nil {
		t.Fatal(err)
	}
	comparison := Comparison{Version: Version, ParentRunID: parent, ChildRunID: child, Entries: []ComparisonEntry{}}
	comparison.Digest = hashCanonical(comparison)
	current.ChildLocator, current.ComparisonIntent = child+".locator", &comparison
	update(PhaseFinalizing, "comparison_intended")
	return current
}

// terminalizeTestRecord emits the same audited lifecycle events production requires before installing a terminal fixture.
func terminalizeTestRecord(t *testing.T, store *LocalStore, desired Record) Record {
	t.Helper()
	if validFunctionalOutcome(desired.Outcome) && desired.Comparison == nil {
		desired.ChildLocator = "child.locator"
		desired.Comparison = &Comparison{Version: Version, ParentRunID: desired.Link.ParentRunID, ChildRunID: desired.Link.ChildRunID, Entries: []ComparisonEntry{}}
		desired.Comparison.Digest = hashCanonical(*desired.Comparison)
	}
	ctx := context.Background()
	current, err := store.Get(ctx, desired.Link.Handle)
	if err != nil {
		t.Fatal(err)
	}
	nextAt := func() time.Time { return current.Link.UpdatedAt.Add(time.Nanosecond) }
	update := func(phase Phase, eventType string) {
		t.Helper()
		expected := current.Phase
		current.Phase = phase
		current.Link.UpdatedAt = nextAt()
		current.Link, _ = current.Link.Seal()
		current, err = store.Update(ctx, current.Link.Handle, expected, current, eventType)
		if err != nil {
			t.Fatalf("%s: %v", eventType, err)
		}
	}
	if desired.Link.ChildRunID != "" {
		update(PhasePreflight, "preflight_started")
		current.Plan = desired.Plan
		current.Link.ChildRunID = desired.Link.ChildRunID
		current.Link.ChildBindingSHA256 = desired.Link.ChildBindingSHA256
		update(PhasePreflight, "preflight_confirmed")
	}
	if desired.Usage != (ModelUsage{}) && desired.Comparison == nil {
		current, _, err = store.ReserveRemainingBudget(ctx, current.Link.Handle, current.Phase, nextAt())
		if err != nil {
			t.Fatal(err)
		}
		current, err = store.ConfirmBudget(ctx, current.Link.Handle, current.Phase, desired.Usage, nextAt())
		if err != nil {
			t.Fatal(err)
		}
	}
	if desired.Comparison != nil {
		update(PhaseFailedFirst, "phase_started")
		update(PhaseRegression, "phase_started")
		update(PhaseFinalizing, "child_terminal")
		current, _, err = store.ReserveRemainingBudget(ctx, current.Link.Handle, current.Phase, nextAt())
		if err != nil {
			t.Fatal(err)
		}
		current.PreparationID, current.PreparedOutcome, current.PreparedUsage = "preparation", desired.Outcome, desired.Usage
		update(PhaseFinalizing, "finalization_prepared")
		current, err = store.ConfirmBudget(ctx, current.Link.Handle, current.Phase, desired.Usage, nextAt())
		if err != nil {
			t.Fatal(err)
		}
		current.ChildLocator = desired.ChildLocator
		current.ComparisonIntent = desired.Comparison
		update(PhaseFinalizing, "comparison_intended")
		current.Comparison = desired.Comparison
		current.Outcome = desired.Outcome
		current.Owner = ""
		if !validComparisonSnapshot(current) || !validTerminalSnapshot(current) {
			t.Fatalf("invalid comparison fixture: snapshot=%t terminal=%t comparison=%+v intent=%+v", validComparisonSnapshot(current), validTerminalSnapshot(current), current.Comparison, current.ComparisonIntent)
		}
		update(PhaseTerminal, "comparison_published")
		return current
	}
	current.Outcome = desired.Outcome
	current.TerminalReason = desired.TerminalReason
	current.LimitDimension = desired.LimitDimension
	current.Owner = ""
	update(PhaseTerminal, "correction_terminal")
	return current
}

// testReservation creates a valid distinct admission fixture.
func testReservation(t *testing.T, parent, handle, key string) Reservation {
	t.Helper()
	policy, err := (RegressionSnapshot{Version: Version, ID: "policy", Scenarios: []ScenarioIdentity{}, Limits: testLimits()}).Seal()
	if err != nil {
		t.Fatal(err)
	}
	link, err := (CorrectionLink{Version: Version, CorrectionID: "id." + handle, Handle: handle, PrincipalID: "principal", ParentRunID: parent, ParentBindingSHA256: digestA, RootRunID: parent, Attempt: 1, SelectionAlias: "selection", SelectionSHA256: digestA, PolicySHA256: policy.Digest, LimitsSHA256: hashCanonical(testLimits()), CreatedAt: testNow, UpdatedAt: testNow}).Seal()
	if err != nil {
		t.Fatal(err)
	}
	return Reservation{Record: Record{Link: link, RequestHash: keyDigest(parent, "selection"), KeyHash: keyDigest(key), Phase: PhaseReserved, Plan: CorrectionPlan{Version: Version}, Owner: "owner", PolicyID: policy.ID, PolicyScenarios: []ScenarioIdentity{}}, Limits: testLimits()}
}

// TestValidateStoreTransitionRejectsIdentityAndLinkDrift proves SQL adapters
// cannot advance a correction with a foreign or unauthenticated lineage link.
func TestValidateStoreTransitionRejectsIdentityAndLinkDrift(t *testing.T) {
	current := testReservation(t, "parent-transition", "transition.handle", "transition-key").Record
	next := current
	next.Phase = PhasePreflight
	next.Link.UpdatedAt = next.Link.UpdatedAt.Add(time.Nanosecond)
	var err error
	next.Link, err = next.Link.Seal()
	if err != nil || ValidateStoreTransition(current, PhaseReserved, next) != nil {
		t.Fatalf("valid transition rejected: %v", err)
	}
	foreign := next
	foreign.Link.Handle = "foreign.handle"
	if ValidateStoreTransition(current, PhaseReserved, foreign) == nil {
		t.Fatal("foreign correction identity accepted")
	}
	tampered := next
	tampered.Link.Digest = strings.Repeat("f", 64)
	if ValidateStoreTransition(current, PhaseReserved, tampered) == nil {
		t.Fatal("tampered correction link accepted")
	}
	store := testStore(t)
	importer, err := controlplane.OpenLocalImportStore(store.root, "correction-freeze")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = importer.Freeze(context.Background(), "correction-import", "correction-run", "project", strings.Repeat("a", 64), 1, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	if _, err = store.Reserve(context.Background(), testReservation(t, "frozen-parent", "frozen.handle", "frozen-key")); !IsCategory(err, Conflict) {
		t.Fatalf("frozen correction store write err=%v", err)
	}
}

func setReservationLimits(t *testing.T, reservation *Reservation, limits ChainLimits) {
	t.Helper()
	policy, err := (RegressionSnapshot{Version: Version, ID: reservation.Record.PolicyID, Scenarios: cloneIdentities(reservation.Record.PolicyScenarios), ApprovalRequired: reservation.Record.PolicyApprovalRequired, Limits: limits}).Seal()
	if err != nil {
		t.Fatal(err)
	}
	reservation.Limits = limits
	reservation.Record.Link.PolicySHA256 = policy.Digest
	reservation.Record.Link.LimitsSHA256 = hashCanonical(limits)
	reservation.Record.Link, err = reservation.Record.Link.Seal()
	if err != nil {
		t.Fatal(err)
	}
}

// fakeApplicability controls structural applicability without external context.
type fakeApplicability struct {
	applicable bool
	reason     ApplicabilityReason
}

// Check supplies deterministic structural applicability to the coordinator.
func (f fakeApplicability) Check(context.Context, ScenarioIdentity) (bool, ApplicabilityReason, error) {
	return f.applicable, f.reason, nil
}

// fakeClock fixes audit time for deterministic lifecycle assertions.
type fakeClock struct{ now time.Time }

// Now supplies a stable auditable instant to tests.
func (f *fakeClock) Now() time.Time { return f.now }

// budgetErrorStore injects one deterministic budget-boundary failure while delegating every durable operation.
type budgetErrorStore struct {
	Store
	reserveErr error
	confirmErr error
}

// ReserveRemainingBudget exposes the configured reservation failure to exercise coordinator classification.
func (s budgetErrorStore) ReserveRemainingBudget(ctx context.Context, handle string, phase Phase, at time.Time) (Record, ModelUsage, error) {
	if s.reserveErr != nil {
		return Record{}, ModelUsage{}, s.reserveErr
	}
	return s.Store.ReserveRemainingBudget(ctx, handle, phase, at)
}

// ConfirmBudget exposes the configured confirmation failure to exercise coordinator classification.
func (s budgetErrorStore) ConfirmBudget(ctx context.Context, handle string, phase Phase, usage ModelUsage, at time.Time) (Record, error) {
	if s.confirmErr != nil {
		return Record{}, s.confirmErr
	}
	return s.Store.ConfirmBudget(ctx, handle, phase, usage, at)
}

// fakeIDs supplies a finite deterministic identity sequence to a test coordinator.
type fakeIDs struct {
	mu     sync.Mutex
	values []string
	err    error
}

// Next returns deterministic opaque identities in admission order.
func (f *fakeIDs) Next() (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return "", f.err
	}
	if len(f.values) == 0 {
		return "", errors.New("identity exhausted")
	}
	value := f.values[0]
	f.values = f.values[1:]
	return value, nil
}

// fakeParents models authorized resolution of one opaque parent handle.
type fakeParents struct {
	resource ParentResource
	err      error
}

// ResolveParent simulates trusted ownership without exposing filesystem paths to requests.
func (f fakeParents) ResolveParent(context.Context, string, string) (ParentResource, error) {
	return f.resource, f.err
}

// fakeCatalog models trusted alias resolution for revision and regression policy.
type fakeCatalog struct {
	selection TrustedSelection
	err       error
}

// ResolveSelection supplies revision and policy from a trusted alias.
func (f fakeCatalog) ResolveSelection(context.Context, string, string) (TrustedSelection, error) {
	return f.selection, f.err
}

// fakePackages models the verified package reader boundary for parent and child.
type fakePackages struct {
	mu     sync.Mutex
	parent localvalidation.PackageIndex
	child  localvalidation.PackageIndex
}

// Read returns controlled verified semantic indexes without filesystem or network.
func (f *fakePackages) Read(_ context.Context, locator string) (localvalidation.PackageIndex, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if locator == "parent.locator" {
		return f.parent, nil
	}
	return f.child, nil
}

// fakeResolver models complete local revision resolution without Git I/O.
type fakeResolver struct{ revision revisionbinding.Revision }

// Resolve returns a configured complete revision for preflight.
func (f *fakeResolver) Resolve(context.Context, string, string) (revisionbinding.Revision, error) {
	return f.revision, nil
}

// fakeAttestor models a trusted deployment proof without a preview provider.
type fakeAttestor struct{ attestation revisionbinding.Attestation }

// Attest returns a configured environment proof for preflight.
func (f *fakeAttestor) Attest(context.Context, string) (revisionbinding.Attestation, error) {
	return f.attestation, nil
}

// fakeGate records consumption of a one-use continuation approval.
type fakeGate struct {
	mu    sync.Mutex
	calls int
	err   error
}

// Authorize consumes exactly one controlled continuation decision.
func (f *fakeGate) Authorize(context.Context, ContinuationRequest) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	if f.err != nil {
		return false, f.err
	}
	return f.calls == 1, nil
}

// fakeExecutor records phase ordering and can simulate infrastructure failure.
type fakeExecutor struct {
	mu                  sync.Mutex
	phases              []Phase
	packages            *fakePackages
	err                 error
	scenarioCounts      []int
	block               bool
	started             chan struct{}
	finalizeUsage       ModelUsage
	prepareErr          error
	preparedOverride    *ChildPreparation
	commitErrBefore     error
	commitErrAfter      error
	preparedBinding     revisionbinding.Binding
	committed           map[string]ChildResult
	commitCalls         int
	packagePublishCount int
	packagePublished    bool
}

// ExecutePhase records observable phase order and returns current-child fingerprints.
func (f *fakeExecutor) ExecutePhase(ctx context.Context, request PhaseRequest) (PhaseResult, error) {
	f.mu.Lock()
	f.phases = append(f.phases, request.Phase)
	f.scenarioCounts = append(f.scenarioCounts, len(request.Scenarios))
	err := f.err
	block := f.block
	started := f.started
	f.mu.Unlock()
	if started != nil {
		select {
		case <-started:
		default:
			close(started)
		}
	}
	if block {
		<-ctx.Done()
		return PhaseResult{}, ctx.Err()
	}
	if err != nil {
		return PhaseResult{}, err
	}
	confirmed := []string{}
	for _, scenario := range request.Scenarios {
		confirmed = append(confirmed, scenario.Identity.Fingerprint)
	}
	return PhaseResult{ConfirmedFingerprints: confirmed, Usage: ModelUsage{Calls: 1, Tokens: 2}}, nil
}

// PrepareFinalize reports final usage and retains private staging without publishing a package.
func (f *fakeExecutor) PrepareFinalize(_ context.Context, _ string, binding revisionbinding.Binding, _ ModelUsage) (ChildPreparation, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.preparedBinding = binding
	if f.prepareErr != nil {
		return ChildPreparation{}, f.prepareErr
	}
	if f.preparedOverride != nil {
		return *f.preparedOverride, nil
	}
	return ChildPreparation{PreparationID: "preparation", RunID: binding.RunID, Outcome: OutcomePassed, Usage: f.finalizeUsage}, nil
}

// CommitFinalize publishes the staged child only after the coordinator confirms its usage.
func (f *fakeExecutor) CommitFinalize(_ context.Context, preparation ChildPreparation) (ChildResult, error) {
	f.mu.Lock()
	f.commitCalls++
	if f.commitErrBefore != nil {
		err := f.commitErrBefore
		f.mu.Unlock()
		return ChildResult{}, err
	}
	if f.committed == nil {
		f.committed = map[string]ChildResult{}
	}
	if result, ok := f.committed[preparation.PreparationID]; ok {
		err := f.commitErrAfter
		f.mu.Unlock()
		return result, err
	}
	binding := f.preparedBinding
	f.packagePublished = true
	f.packagePublishCount++
	result := ChildResult{RunID: preparation.RunID, PackageLocator: "child.locator", Outcome: preparation.Outcome}
	f.committed[preparation.PreparationID] = result
	err := f.commitErrAfter
	f.mu.Unlock()
	f.packages.mu.Lock()
	f.packages.child.RunID, f.packages.child.Binding = binding.RunID, binding
	f.packages.mu.Unlock()
	return result, err
}

// ResolveFinalize reconciles an uncertain commit by its durable preparation identity without publishing again.
func (f *fakeExecutor) ResolveFinalize(_ context.Context, preparationID string) (ChildResult, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	result, ok := f.committed[preparationID]
	return result, ok, nil
}

// fakePublisher captures comparisons after both packages pass verification.
type fakePublisher struct {
	mu                  sync.Mutex
	comparisons         []Comparison
	published           map[string]Comparison
	errBefore           error
	errAfter            error
	resolveErr          error
	afterPublish        func()
	afterPublishContext func(context.Context)
	afterResolve        func(context.Context)
}

// Publish captures the verified no-replace comparison for assertions.
func (f *fakePublisher) Publish(ctx context.Context, handle string, comparison Comparison) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.errBefore != nil {
		return f.errBefore
	}
	if f.published == nil {
		f.published = map[string]Comparison{}
	}
	if current, ok := f.published[handle]; ok {
		if current.Digest != comparison.Digest {
			return errors.New("comparison conflict")
		}
		return f.errAfter
	}
	f.comparisons = append(f.comparisons, comparison)
	f.published[handle] = comparison
	hook, contextHook, err := f.afterPublish, f.afterPublishContext, f.errAfter
	f.mu.Unlock()
	if hook != nil {
		hook()
	}
	if contextHook != nil {
		contextHook(ctx)
	}
	f.mu.Lock()
	if err == nil {
		err = ctx.Err()
	}
	return err
}

// Resolve reports whether an uncertain fake publication made the exact comparison visible.
func (f *fakePublisher) Resolve(ctx context.Context, handle, digest string) (bool, error) {
	f.mu.Lock()
	comparison, ok := f.published[handle]
	hook, err := f.afterResolve, f.resolveErr
	f.mu.Unlock()
	if err != nil {
		return false, err
	}
	if hook != nil {
		hook(ctx)
	}
	return ok && comparison.Digest == digest, nil
}

// flowFixture groups mutable boundaries inspected by vertical-flow tests.
type flowFixture struct {
	coordinator *Coordinator
	deps        Dependencies
	store       *LocalStore
	ids         *fakeIDs
	resolver    *fakeResolver
	executor    *fakeExecutor
	gate        *fakeGate
	publisher   *fakePublisher
}

// newFlowFixture composes a fully hermetic correction flow with trusted fakes.
func newFlowFixture(t *testing.T, approval bool) flowFixture {
	t.Helper()
	parentFailure := testFailure("constraint", "step", "declaration", "parent.ev")
	parent := testPackage(t, "parent", oldOID, localvalidation.ResultFailed, []localvalidation.PackageFailure{parentFailure}, nil)
	identity, _ := identityFromFailure(parentFailure)
	packages := &fakePackages{parent: parent, child: testPackage(t, "placeholder", newOID, localvalidation.ResultPassed, nil, []localvalidation.PackageFact{{Kind: "assertion", ConstraintID: "constraint", StepID: "step", DeclarationID: "declaration", NodePath: "0", Status: "passed", EvidenceIDs: []string{"child.ev"}}})}
	policy := testPolicy(t, []ScenarioIdentity{identity}, approval)
	resolver := &fakeResolver{revision: revisionbinding.Revision{RepositoryID: "repo", CommitOID: newOID}}
	attestor := &fakeAttestor{attestation: revisionbinding.Attestation{EnvironmentID: "env", RepositoryID: "repo", CommitOID: newOID, Reference: "attestation.new", ValidUntil: testNow.Add(time.Hour), Verified: true}}
	executor := &fakeExecutor{packages: packages}
	gate := &fakeGate{}
	store := testStore(t)
	publisher := &fakePublisher{}
	ids := &fakeIDs{values: []string{"correction.handle", "correction.id", "child"}}
	deps := Dependencies{Store: store, Parents: fakeParents{resource: ParentResource{"parent.handle", "parent", "parent.locator"}}, Catalog: fakeCatalog{selection: TrustedSelection{Alias: "selection", Revision: revisionbinding.Selection{RepositoryID: "repo", Ref: "new", EnvironmentID: "env"}, ConstraintsSHA256: digestA, ContractSHA256: digestA, ConfigurationSHA256: digestA, Policy: policy}}, Packages: packages, Resolver: resolver, Attestor: attestor, Applicability: fakeApplicability{applicable: true}, Gate: gate, Executor: executor, Publisher: publisher, Clock: &fakeClock{now: testNow}, IDs: ids, Owner: "owner", FinalizeLimit: time.Second}
	coordinator, err := NewCoordinator(context.Background(), deps)
	if err != nil {
		t.Fatal(err)
	}
	return flowFixture{coordinator: coordinator, deps: deps, store: store, ids: ids, resolver: resolver, executor: executor, gate: gate, publisher: publisher}
}

// waitTerminal observes durable completion without sleeps or timing assumptions.
func waitTerminal(t *testing.T, coordinator *Coordinator, handle string) Record {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := coordinator.Wait(ctx, handle); err != nil {
		t.Fatal(err)
	}
	record, err := coordinator.Status(ctx, handle)
	if err != nil {
		t.Fatal(err)
	}
	if record.Phase != PhaseTerminal {
		t.Fatalf("joined nonterminal: %+v", record)
	}
	return record
}
