package functionalflow

import (
	"encoding/json"
	"sort"

	"github.com/asierhr/featureProof/internal/planning"
)

// Ledger is an immutable append-only snapshot of deduplicated deterministic assertion results.
type Ledger struct{ entries []LedgerEntry }

// Entries returns deep copies so external projections cannot rewrite confirmed proof.
func (l Ledger) Entries() []LedgerEntry {
	raw, _ := json.Marshal(l.entries)
	var out []LedgerEntry
	_ = json.Unmarshal(raw, &out)
	return out
}

// Append adds one unique assertion coordinate or rejects contradictory redelivery.
func (l Ledger) Append(entry LedgerEntry) (Ledger, error) {
	if entry.Revision == 0 || entry.StepID == "" || entry.ConstraintID == "" || entry.Result.DeclarationID == "" {
		return l, flowError(CategoryIntegrity, "ledger.entry", nil)
	}
	entry.EvidenceIDs = canonicalEvidenceIDs(entry.EvidenceIDs)
	for _, existing := range l.entries {
		if existing.Revision == entry.Revision && existing.StepID == entry.StepID && existing.Result.DeclarationID == entry.Result.DeclarationID && existing.Result.NodePath == entry.Result.NodePath {
			if digestValue(existing) == digestValue(entry) {
				return l, nil
			}
			return l, flowError(CategoryIntegrity, "ledger.conflict", nil)
		}
	}
	next := Ledger{entries: append(l.Entries(), entry)}
	return next, nil
}

// Aggregate derives constraint and flow outcomes solely from required declarations and confirmed ledger entries.
func (l Ledger) Aggregate(flow Flow, steps []StepRecord, cancelled bool) ([]ConstraintResult, PrimaryOutcome) {
	constraints := map[string][]Step{}
	order := []string{}
	for _, step := range flow.Steps {
		if _, exists := constraints[step.ConstraintID]; !exists {
			order = append(order, step.ConstraintID)
		}
		constraints[step.ConstraintID] = append(constraints[step.ConstraintID], step)
	}
	entries := map[string]LedgerEntry{}
	for _, entry := range l.entries {
		entries[entry.StepID+"\x00"+entry.Result.DeclarationID] = entry
	}
	stepByID := map[string]StepRecord{}
	for _, record := range steps {
		stepByID[record.StepID] = record
	}
	results := make([]ConstraintResult, 0, len(order))
	global := OutcomePassed
	for _, constraintID := range order {
		outcome := OutcomePassed
		evidence := []string{}
		for _, step := range constraints[constraintID] {
			record, exists := stepByID[step.ID]
			if !exists || record.Status == planning.StepBlocked || record.Status == planning.StepInconclusive {
				if exists && (record.Category == CategoryIntegrity || record.Category == CategoryInternal) {
					outcome = OutcomeError
				} else if outcome != OutcomeFailed && outcome != OutcomeError {
					outcome = OutcomeInconclusive
				}
				continue
			}
			if record.Status == planning.StepFailed {
				outcome = OutcomeFailed
			}
			for _, required := range step.RequiredEvidence {
				entry, ok := entries[step.ID+"\x00"+required]
				if !ok || len(entry.EvidenceIDs) == 0 || entry.Result.Status == planning.AssertionInconclusive {
					if outcome != OutcomeFailed {
						outcome = OutcomeInconclusive
					}
					continue
				}
				evidence = append(evidence, entry.EvidenceIDs...)
				if entry.Result.Status == planning.AssertionFailed {
					outcome = OutcomeFailed
				}
			}
		}
		results = append(results, ConstraintResult{ConstraintID: constraintID, Outcome: outcome, EvidenceIDs: canonicalEvidenceIDs(evidence)})
		if outcome == OutcomeError {
			global = OutcomeError
		} else if outcome == OutcomeFailed && global != OutcomeError {
			global = OutcomeFailed
		} else if outcome == OutcomeInconclusive && global == OutcomePassed {
			global = OutcomeInconclusive
		}
	}
	if cancelled && global != OutcomeFailed {
		global = OutcomeCancelled
	}
	return results, global
}

// sortStepRecords projects step terminals in declaration order regardless of recovery arrival order.
func sortStepRecords(flow Flow, records map[string]StepRecord) []StepRecord {
	out := make([]StepRecord, 0, len(flow.Steps))
	for _, step := range flow.Steps {
		if record, ok := records[step.ID]; ok {
			record.Assertions = cloneAssertionResults(record.Assertions)
			record.Blocking = append([]planning.BlockingCause(nil), record.Blocking...)
			out = append(out, record)
		}
	}
	return out
}

// cloneAssertionResults detaches nested deterministic results for immutable records and projections.
func cloneAssertionResults(results []planning.AssertionResult) []planning.AssertionResult {
	raw, _ := json.Marshal(results)
	var out []planning.AssertionResult
	_ = json.Unmarshal(raw, &out)
	return out
}

// orderedLedger sorts reconstructed proof by revision, declaration order, and assertion ID.
func orderedLedger(entries []LedgerEntry, flow Flow) []LedgerEntry {
	position := map[string]int{}
	for i, step := range flow.Steps {
		position[step.ID] = i
	}
	sort.SliceStable(entries, func(i, j int) bool {
		if entries[i].Revision != entries[j].Revision {
			return entries[i].Revision < entries[j].Revision
		}
		if position[entries[i].StepID] != position[entries[j].StepID] {
			return position[entries[i].StepID] < position[entries[j].StepID]
		}
		return entries[i].Result.DeclarationID < entries[j].Result.DeclarationID
	})
	return entries
}
