package functionalflow

import (
	"context"

	"github.com/asierhr/featureProof/internal/fixture"
	"github.com/asierhr/featureProof/internal/planning"
)

// RecoverCompleted reconstructs a fully terminal primary from authenticated state, journal and evidence without executing a tool.
func (e *Executor) RecoverCompleted(ctx context.Context, runID string, validated ValidatedFlow, artifact Artifact) (Result, bool, error) {
	if err := ValidateArtifactAuthority(validated, artifact, e.Authority); err != nil {
		return Result{}, false, err
	}
	if e.Journal == nil || e.State == nil || e.Evidence == nil || e.Assertions == nil || runID == "" {
		return Result{}, false, flowError(CategoryInternal, "recovery.compose", nil)
	}
	events, err := e.Journal.Events(ctx, runID)
	if err != nil {
		return Result{}, false, flowError(CategoryIntegrity, "recovery.journal", err)
	}
	terminals, comparisonEvents, primaryEvent, complete, err := completedRuntimeEvents(events, runID, artifact, validated)
	if err != nil || !complete {
		return Result{}, false, err
	}
	flow := validated.Flow()
	records := map[string]StepRecord{}
	reconcile := []StepRecord{}
	ledger := Ledger{}
	for _, index := range validated.order {
		step := flow.Steps[index]
		event := terminals[step.ID]
		record, exists, loadErr := e.State.Terminal(ctx, runID, flow.Revision, step.ID)
		if loadErr != nil {
			return Result{}, false, flowError(CategoryIntegrity, "recovery.state", loadErr)
		}
		if !exists {
			record, err = e.recordFromTerminalEvent(ctx, runID, flow.Revision, step, event, records)
			if err != nil {
				return Result{}, false, err
			}
			reconcile = append(reconcile, record)
		}
		if err = terminalMatchesEvent(record, event, runID, flow.Revision); err != nil {
			return Result{}, false, err
		}
		records[step.ID] = record
		ledger = appendRecordLedger(ledger, step, record)
	}
	comparisons := []ComparisonResult{}
	for _, step := range flow.Steps {
		for _, comparison := range step.Comparisons {
			event, exists := comparisonEvents[declarationCoordinate(step.ID, comparison.ID)]
			if !exists {
				return Result{}, false, nil
			}
			result, compareErr := EvaluateComparison(ctx, runID, flow.Revision, comparison, records, e.Evidence)
			if compareErr != nil {
				return Result{}, false, compareErr
			}
			result.StepID = step.ID
			evidenceIDs := canonicalEvidenceIDs([]string{result.BeforeEvidenceID, result.AfterEvidenceID})
			if digestValue(evidenceIDs) != digestValue(canonicalEvidenceIDs(event.EvidenceIDs)) {
				return Result{}, false, flowError(CategoryIntegrity, "recovery.comparison", nil)
			}
			comparisons = append(comparisons, result)
			synthetic := planning.AssertionResult{DeclarationID: comparison.ID, NodePath: comparison.ID, Operator: string(comparison.Operator), Status: result.Status, EvidenceIDs: evidenceIDs}
			ledger, err = ledger.Append(LedgerEntry{Revision: flow.Revision, StepID: step.ID, ConstraintID: step.ConstraintID, Result: synthetic, EvidenceIDs: evidenceIDs})
			if err != nil {
				return Result{}, false, err
			}
		}
	}
	orderedSteps := sortStepRecords(flow, records)
	constraints, outcome := ledger.Aggregate(flow, orderedSteps, false)
	result := Result{FlowID: flow.ID, Revision: flow.Revision, Steps: orderedSteps, Ledger: orderedLedger(ledger.Entries(), flow), Comparisons: comparisons, Constraints: constraints, Outcome: outcome, ArtifactDigest: artifact.ArtifactDigest}
	if primaryEvent != nil && primaryEvent.Outcome != outcome {
		return Result{}, false, flowError(CategoryIntegrity, "recovery.primary", nil)
	}
	if err = e.verifyCompletedRuntimeProof(ctx, runID, artifact, events, result); err != nil {
		return Result{}, false, err
	}
	if len(reconcile) > 0 {
		reconciler, ok := e.State.(TerminalReconciler)
		if !ok {
			return Result{}, false, flowError(CategoryIntegrity, "recovery.reconcile", nil)
		}
		for _, record := range reconcile {
			if err = reconciler.ReconcileTerminal(ctx, record); err != nil {
				return Result{}, false, flowError(CategoryIntegrity, "recovery.reconcile", err)
			}
		}
	}
	return result, true, nil
}

// verifyCompletedRuntimeProof applies the package verifier to a recovery-only lifecycle projection before publication or cleanup.
func (e *Executor) verifyCompletedRuntimeProof(ctx context.Context, runID string, artifact Artifact, events []Event, result Result) error {
	evidenceIDs := []string{}
	for _, entry := range result.Ledger {
		evidenceIDs = append(evidenceIDs, entry.EvidenceIDs...)
	}
	records := make([]EvidenceRecord, 0, len(evidenceIDs))
	for _, id := range canonicalEvidenceIDs(evidenceIDs) {
		evidence, err := e.Evidence.Load(ctx, id)
		if err != nil {
			return flowError(CategoryIntegrity, "recovery.evidence_manifest", err)
		}
		record, err := SealEvidenceRecord(evidence)
		if err != nil {
			return flowError(CategoryIntegrity, "recovery.evidence_manifest", err)
		}
		records = append(records, record)
	}
	verificationEvents := cloneRecoveryEvents(events)
	allEvidence := canonicalEvidenceIDs(evidenceIDs)
	lastType := verificationEvents[len(verificationEvents)-1].Type
	if lastType != "primary_terminal" && lastType != "cleanup_terminal" {
		verificationEvents = appendRecoveryVerificationEvent(verificationEvents, Event{Type: "primary_terminal", Outcome: result.Outcome, EvidenceIDs: allEvidence}, runID, artifact)
		lastType = "primary_terminal"
	}
	if lastType != "cleanup_terminal" {
		verificationEvents = appendRecoveryVerificationEvent(verificationEvents, Event{Type: "cleanup_terminal", Status: string(fixture.CleanupNotRequired)}, runID, artifact)
	}
	preparation := fixture.PreparationPrepared
	if verificationEvents[1].Type == "fixtures_terminal" {
		preparation = fixture.PreparationStatus(verificationEvents[1].Status)
	}
	cleanup := fixture.CleanupReport{Status: fixture.CleanupStatus(verificationEvents[len(verificationEvents)-1].Status)}
	if cleanup.Status == "" {
		cleanup.Status = fixture.CleanupNotRequired
	}
	_, err := BuildPackage(runID, artifact, records, verificationEvents, result, fixture.FixtureSummary{PreparationStatus: preparation, Cleanup: cleanup}, cleanup)
	if err != nil {
		return flowError(CategoryIntegrity, "recovery.causal_proof", err)
	}
	return nil
}

// cloneRecoveryEvents detaches nested evidence identifiers before adding verification-only terminal checkpoints.
func cloneRecoveryEvents(events []Event) []Event {
	out := append([]Event(nil), events...)
	for index := range out {
		out[index].EvidenceIDs = append([]string(nil), out[index].EvidenceIDs...)
	}
	return out
}

// appendRecoveryVerificationEvent seals one in-memory checkpoint used only to invoke the canonical package verifier.
func appendRecoveryVerificationEvent(events []Event, event Event, runID string, artifact Artifact) []Event {
	event.Sequence = uint64(len(events) + 1)
	event.RunID = runID
	event.FlowID = artifact.FlowID
	event.Revision = artifact.Revision
	event.ArtifactDigest = artifact.ArtifactDigest
	event.EvidenceIDs = canonicalEvidenceIDs(event.EvidenceIDs)
	if len(events) > 0 {
		event.PreviousDigest = events[len(events)-1].Digest
	}
	event.Digest = eventDigest(event)
	return append(events, event)
}

// completedRuntimeEvents validates one complete topological runtime journal and returns its terminal proof coordinates.
func completedRuntimeEvents(events []Event, runID string, artifact Artifact, validated ValidatedFlow) (map[string]Event, map[string]Event, *Event, bool, error) {
	if len(events) < 2 {
		return nil, nil, nil, false, nil
	}
	previous := ""
	for index, event := range events {
		if event.Sequence != uint64(index+1) || event.RunID != runID || event.FlowID != artifact.FlowID || event.Revision != artifact.Revision || event.ArtifactDigest != artifact.ArtifactDigest || event.PreviousDigest != previous || event.Digest != eventDigest(event) {
			return nil, nil, nil, false, flowError(CategoryIntegrity, "recovery.journal_binding", nil)
		}
		previous = event.Digest
	}
	if events[0].Type != "flow_artifact_committed" || events[1].Type != "fixtures_terminal" && events[1].Type != "fixtures_prepared" {
		return nil, nil, nil, false, flowError(CategoryIntegrity, "recovery.lifecycle", nil)
	}
	flow := validated.Flow()
	expected := make([]string, len(validated.order))
	for position, index := range validated.order {
		expected[position] = flow.Steps[index].ID
	}
	known := map[string]bool{}
	for _, id := range expected {
		known[id] = true
	}
	terminals := map[string]Event{}
	comparisons := map[string]Event{}
	order := []string{}
	type runtimeState struct {
		claimed, intent, attempted, evidenced, asserted, terminal bool
		attempt                                                   int
	}
	states := map[string]runtimeState{}
	boundByID := map[string]BoundStep{}
	for _, bound := range artifact.Steps {
		boundByID[bound.Step.ID] = bound
	}
	var primary *Event
	for index := 2; index < len(events); index++ {
		event := events[index]
		state := states[event.StepID]
		bound := boundByID[event.StepID]
		switch event.Type {
		case "step_claimed":
			if !known[event.StepID] || state.claimed || state.terminal || !validDigest(event.OwnerDigest) {
				return nil, nil, nil, false, flowError(CategoryIntegrity, "recovery.claim", nil)
			}
			state.claimed = true
		case "intent_confirmed":
			if !state.claimed || bound.Step.Kind != StepAct || state.attempted || state.terminal || event.BindingDigest != bound.Operation.BindingDigest {
				return nil, nil, nil, false, flowError(CategoryIntegrity, "recovery.intent", nil)
			}
			state.intent = true
		case "attempt_started":
			if !state.claimed || bound.Step.Kind == StepAct && !state.intent || state.terminal || event.Attempt != state.attempt+1 || event.BindingDigest != bound.Operation.BindingDigest {
				return nil, nil, nil, false, flowError(CategoryIntegrity, "recovery.attempt", nil)
			}
			state.attempted, state.attempt = true, event.Attempt
		case "retry_scheduled":
			if !state.attempted || state.evidenced || state.terminal || event.Attempt != state.attempt {
				return nil, nil, nil, false, flowError(CategoryIntegrity, "recovery.retry", nil)
			}
		case "evidence_confirmed":
			if !state.attempted || state.evidenced || state.terminal || len(canonicalEvidenceIDs(event.EvidenceIDs)) != 1 || !validDigest(event.BindingDigest) || event.ResourceBindingDigest == "" || !validDigest(event.ContentDigest) || !validDigest(event.RecordDigest) {
				return nil, nil, nil, false, flowError(CategoryIntegrity, "recovery.evidence", nil)
			}
			state.evidenced = true
		case "assertions_confirmed":
			if !state.evidenced || state.asserted || state.terminal || len(canonicalEvidenceIDs(event.EvidenceIDs)) != 1 {
				return nil, nil, nil, false, flowError(CategoryIntegrity, "recovery.assertions", nil)
			}
			state.asserted = true
		case "recovery_resumed":
			if !state.claimed || state.terminal {
				return nil, nil, nil, false, flowError(CategoryIntegrity, "recovery.resume", nil)
			}
		case "step_terminal":
			if !known[event.StepID] || terminals[event.StepID].Type != "" || state.terminal || state.evidenced != state.asserted {
				return nil, nil, nil, false, flowError(CategoryIntegrity, "recovery.terminal", nil)
			}
			if len(canonicalEvidenceIDs(event.EvidenceIDs)) == 1 && !state.asserted {
				return nil, nil, nil, false, flowError(CategoryIntegrity, "recovery.terminal", nil)
			}
			state.terminal = true
			terminals[event.StepID] = event
			order = append(order, event.StepID)
		case "comparisons_confirmed":
			coordinate := declarationCoordinate(event.StepID, event.DeclarationID)
			if !known[event.StepID] || !state.terminal || event.DeclarationID == "" || comparisons[coordinate].Type != "" {
				return nil, nil, nil, false, flowError(CategoryIntegrity, "recovery.comparison", nil)
			}
			comparisons[coordinate] = event
		case "primary_terminal":
			if primary != nil || len(terminals) != len(expected) {
				return nil, nil, nil, false, flowError(CategoryIntegrity, "recovery.primary", nil)
			}
			copy := event
			primary = &copy
		case "cleanup_terminal":
			if primary == nil || index != len(events)-1 {
				return nil, nil, nil, false, flowError(CategoryIntegrity, "recovery.cleanup", nil)
			}
		default:
			return nil, nil, nil, false, flowError(CategoryIntegrity, "recovery.event", nil)
		}
		if event.StepID != "" {
			states[event.StepID] = state
		}
	}
	if len(terminals) != len(expected) {
		return terminals, comparisons, primary, false, nil
	}
	for index, id := range expected {
		if order[index] != id {
			return nil, nil, nil, false, flowError(CategoryIntegrity, "recovery.order", nil)
		}
	}
	return terminals, comparisons, primary, true, nil
}

// recordFromTerminalEvent rebuilds a missing state terminal from journal-owned evidence and deterministic assertions.
func (e *Executor) recordFromTerminalEvent(ctx context.Context, runID string, revision uint64, step Step, event Event, records map[string]StepRecord) (StepRecord, error) {
	status := planning.StepStatus(event.Status)
	if status != planning.StepPassed && status != planning.StepFailed && status != planning.StepInconclusive && status != planning.StepBlocked {
		return StepRecord{}, flowError(CategoryIntegrity, "recovery.status", nil)
	}
	record := StepRecord{RunID: runID, Revision: revision, StepID: step.ID, Status: status, Category: event.Category}
	evidenceIDs := canonicalEvidenceIDs(event.EvidenceIDs)
	if len(evidenceIDs) > 1 {
		return StepRecord{}, flowError(CategoryIntegrity, "recovery.evidence", nil)
	}
	if len(evidenceIDs) == 1 {
		evidence, err := e.Evidence.Load(ctx, evidenceIDs[0])
		if err != nil || !validEvidence(evidence, runID, revision, step.ID) {
			return StepRecord{}, flowError(CategoryIntegrity, "recovery.evidence", err)
		}
		assertions, err := e.Assertions.Evaluate(ctx, step.Assertions, evidence)
		if err != nil {
			return StepRecord{}, flowError(CategoryIntegrity, "recovery.assertions", err)
		}
		for index := range assertions {
			assertions[index].EvidenceIDs = canonicalEvidenceIDs(append(assertions[index].EvidenceIDs, evidence.ID))
		}
		record.EvidenceID, record.Assertions = evidence.ID, assertions
	} else if status == planning.StepPassed || status == planning.StepFailed {
		return StepRecord{}, flowError(CategoryIntegrity, "recovery.evidence", nil)
	}
	if status == planning.StepBlocked {
		record.Blocking = directBlocking(step, records)
		if len(record.Blocking) == 0 {
			return StepRecord{}, flowError(CategoryIntegrity, "recovery.blocking", nil)
		}
	}
	return record, nil
}

// terminalMatchesEvent rejects state and journal divergence before a primary can be promoted.
func terminalMatchesEvent(record StepRecord, event Event, runID string, revision uint64) error {
	if record.RunID != runID || record.Revision != revision || record.StepID != event.StepID || string(record.Status) != event.Status || record.Category != event.Category {
		return flowError(CategoryIntegrity, "recovery.terminal_binding", nil)
	}
	recordedEvidence := canonicalEvidenceIDs(event.EvidenceIDs)
	stateEvidence := canonicalEvidenceIDs([]string{record.EvidenceID})
	if digestValue(recordedEvidence) != digestValue(stateEvidence) {
		return flowError(CategoryIntegrity, "recovery.terminal_evidence", nil)
	}
	return nil
}
