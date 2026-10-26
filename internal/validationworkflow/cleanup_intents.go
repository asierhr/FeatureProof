package validationworkflow

import (
	"context"
	"errors"
	"sort"

	environmentcleanup "github.com/asierhr/featureProof/internal/environment/cleanup"
)

// CleanupAuthorityResolver resolves a workflow intent through the private environment lifecycle store.
type CleanupAuthorityResolver interface {
	ResolveCleanupAuthority(context.Context, string, string) (environmentcleanup.Item, error)
}

// CleanupIntentSource projects durable desired outcomes without workflow mutation authority.
type CleanupIntentSource struct {
	Store     WorkflowStore
	Authority CleanupAuthorityResolver
	Clock     Clock
}

// cleanupPendingStore exposes terminal cleanup obligations without changing workflow recovery scans.
type cleanupPendingStore interface {
	CleanupPending(context.Context, string, int) ([]Record, string, bool, error)
}

// ScanCleanupIntents returns stable bounded cleanup items for every durable desired terminal outcome.
func (s CleanupIntentSource) ScanCleanupIntents(ctx context.Context, cursor string, limit int) (environmentcleanup.Page, error) {
	if s.Store == nil || s.Authority == nil || s.Clock == nil || limit <= 0 {
		return environmentcleanup.Page{}, &Error{Code: CodeInvalid, Operation: "cleanup_intents"}
	}
	var records []Record
	storeNext := ""
	storeScanned := false
	storeComplete := false
	var err error
	if durable, ok := s.Store.(cleanupPendingStore); ok {
		records, storeNext, storeComplete, err = durable.CleanupPending(ctx, cursor, limit)
		storeScanned = true
	} else {
		records, err = s.Store.Pending(ctx, s.Clock.Now().UTC())
	}
	if err != nil {
		return environmentcleanup.Page{}, err
	}
	sort.Slice(records, func(i, j int) bool { return records[i].Handle < records[j].Handle })
	page := environmentcleanup.Page{Next: storeNext, Scanned: storeScanned, Complete: storeComplete}
	for _, record := range records {
		if record.Handle <= cursor || !record.CleanupPending || record.DesiredOutcome == nil || !cleanupOutcome(*record.DesiredOutcome) {
			continue
		}
		if len(page.Items) == limit {
			page.Next = page.Items[len(page.Items)-1].RunID
			break
		}
		item, resolveErr := s.Authority.ResolveCleanupAuthority(ctx, record.Handle, record.CleanupIntentID)
		if resolveErr != nil {
			return environmentcleanup.Page{}, resolveErr
		}
		if environmentcleanup.VerifyItem(item) != nil || item.RunID != record.Handle {
			return environmentcleanup.Page{}, &Error{Code: CodeConflict, Operation: "cleanup_authority"}
		}
		page.Items = append(page.Items, item)
	}
	if !storeScanned && page.Next == "" {
		page.Complete = true
	}
	return page, nil
}

// LookupRun distinguishes authoritative terminal, active and absent workflow state.
func (s CleanupIntentSource) LookupRun(ctx context.Context, _ string, runID string) (string, error) {
	record, err := s.Store.Load(ctx, runID)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			return "absent", nil
		}
		return "absent", err
	}
	if record.Phase == PhaseTerminal {
		return "terminal", nil
	}
	if record.CancelRequested || record.DesiredOutcome != nil && *record.DesiredOutcome == OutcomeCancelled {
		return "cancelled", nil
	}
	return "active", nil
}

// RunAbsent exposes only authoritative NotFound for orphan adoption and treats every other state as present.
func (s CleanupIntentSource) RunAbsent(ctx context.Context, project, runID string) (bool, error) {
	state, err := s.LookupRun(ctx, project, runID)
	return state == "absent", err
}

// cleanupOutcome accepts the complete closed desired terminal vocabulary.
func cleanupOutcome(value Outcome) bool {
	switch value {
	case OutcomePassed, OutcomeFailed, OutcomeInconclusive, OutcomeInsufficientContext, OutcomeError, OutcomeCancelled:
		return true
	default:
		return false
	}
}
