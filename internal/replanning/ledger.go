package replanning

import (
	"context"
	"encoding/json"
	"sync"

	"github.com/asierhr/featureProof/internal/planning"
)

// AssertionIdentity binds a deterministic node to its exact run and execution declaration.
type AssertionIdentity struct{ RunID, BatchID, CheckID, StepID, DeclarationID, NodePath string }

// DeclarationIdentity names a required assertion without evaluator-specific node paths.
type DeclarationIdentity struct{ RunID, BatchID, CheckID, StepID, DeclarationID string }

// ConfirmedAssertion is immutable ledger content with safe cause and opaque evidence references.
type ConfirmedAssertion struct {
	Identity                  AssertionIdentity
	Status                    planning.AssertionStatus
	Operator, Path, SafeCause string
	EvidenceIDs               []string
	Required                  bool
}

// Verdict is calculated exclusively from confirmed required assertions.
type Verdict string

const (
	VerdictPassed       Verdict = "passed"
	VerdictFailed       Verdict = "failed"
	VerdictInconclusive Verdict = "inconclusive"
	VerdictError        Verdict = "error"
)

// AssertionLedger owns an append-only sequence and rejects conflicting replays.
type AssertionLedger struct {
	mu         sync.Mutex
	entries    []ConfirmedAssertion
	byIdentity map[AssertionIdentity][]byte
	maximum    int
}

// NewAssertionLedger fixes the maximum number of confirmed nodes before any result arrives.
func NewAssertionLedger(maximum int) (*AssertionLedger, error) {
	if maximum <= 0 {
		return nil, failure(CategoryInvalidInput, "compose_ledger", "maximum")
	}
	return &AssertionLedger{maximum: maximum, byIdentity: map[AssertionIdentity][]byte{}}, nil
}

// Confirm appends one deterministic result or accepts an identical idempotent replay.
func (l *AssertionLedger) Confirm(ctx context.Context, entry ConfirmedAssertion) error {
	if ctx.Err() != nil {
		return failure(CategoryCancelled, "ledger", "context")
	}
	if entry.Identity.RunID == "" || entry.Identity.BatchID == "" || entry.Identity.CheckID == "" || entry.Identity.StepID == "" || entry.Identity.DeclarationID == "" || entry.Identity.NodePath == "" || (entry.Status != planning.AssertionPassed && entry.Status != planning.AssertionFailed && entry.Status != planning.AssertionInconclusive) {
		return failure(CategoryInvalidInput, "ledger", "entry")
	}
	copy := entry
	copy.EvidenceIDs = append([]string(nil), entry.EvidenceIDs...)
	raw, err := json.Marshal(copy)
	if err != nil {
		return failure(CategoryInvalidInput, "ledger", "entry")
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if previous, exists := l.byIdentity[entry.Identity]; exists {
		if string(previous) == string(raw) {
			return nil
		}
		return failure(CategoryIntegrity, "ledger", "conflicting_replay")
	}
	if len(l.entries) >= l.maximum {
		return failure(CategoryBudget, "ledger", "nodes")
	}
	l.entries = append(l.entries, copy)
	l.byIdentity[entry.Identity] = raw
	return nil
}

// Entries returns copies so callers cannot rewrite confirmed failures or evidence references.
func (l *AssertionLedger) Entries() []ConfirmedAssertion {
	l.mu.Lock()
	defer l.mu.Unlock()
	out := make([]ConfirmedAssertion, len(l.entries))
	for i, v := range l.entries {
		out[i] = v
		out[i].EvidenceIDs = append([]string(nil), v.EvidenceIDs...)
	}
	return out
}

// Verdict evaluates required declarations with failed precedence and requires evidence for passing nodes.
func (l *AssertionLedger) Verdict(required []AssertionIdentity) Verdict {
	l.mu.Lock()
	defer l.mu.Unlock()
	if len(required) == 0 {
		return VerdictInconclusive
	}
	allPassed := true
	for _, id := range required {
		raw, ok := l.byIdentity[id]
		if !ok {
			allPassed = false
			continue
		}
		var entry ConfirmedAssertion
		if json.Unmarshal(raw, &entry) != nil {
			return VerdictError
		}
		if entry.Status == planning.AssertionFailed {
			return VerdictFailed
		}
		if entry.Status != planning.AssertionPassed || len(entry.EvidenceIDs) == 0 {
			allPassed = false
		}
	}
	if allPassed {
		return VerdictPassed
	}
	return VerdictInconclusive
}

// VerdictRequired aggregates every confirmed node of each required declaration with failure precedence.
func (l *AssertionLedger) VerdictRequired(required []DeclarationIdentity) Verdict {
	l.mu.Lock()
	defer l.mu.Unlock()
	if len(required) == 0 {
		return VerdictInconclusive
	}
	allPassed := true
	for _, want := range required {
		found := false
		for _, entry := range l.entries {
			id := entry.Identity
			if !entry.Required || id.RunID != want.RunID || id.BatchID != want.BatchID || id.CheckID != want.CheckID || id.StepID != want.StepID || id.DeclarationID != want.DeclarationID {
				continue
			}
			found = true
			if entry.Status == planning.AssertionFailed {
				return VerdictFailed
			}
			if entry.Status != planning.AssertionPassed || len(entry.EvidenceIDs) == 0 {
				allPassed = false
			}
		}
		if !found {
			allPassed = false
		}
	}
	if allPassed {
		return VerdictPassed
	}
	return VerdictInconclusive
}
