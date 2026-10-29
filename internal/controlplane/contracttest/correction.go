package contracttest

import (
	"context"
	"fmt"
	"time"

	"github.com/asierhr/featureProof/internal/correction"
)

// CorrectionFactory reopens one durable correction lineage store.
type CorrectionFactory func() (correction.Store, error)

// RunCorrectionStore verifies idempotent reserve, reopen, lineage-preserving
// phase CAS and append-only events through the consumer-owned Store contract.
func RunCorrectionStore(ctx context.Context, factory CorrectionFactory, rootRunID, handle, projectID string, now time.Time) error {
	if factory == nil || rootRunID == "" || handle == "" || projectID == "" || now.IsZero() {
		return fmt.Errorf("correction contract configuration")
	}
	limits := correction.ChainLimits{MaxAttempts: 2, MaxDuration: time.Hour, MaxModelCalls: 2, MaxModelTokens: 100}
	policy, err := (correction.RegressionSnapshot{Version: correction.Version, ID: "contract-policy", Scenarios: []correction.ScenarioIdentity{}, Limits: limits}).Seal()
	if err != nil {
		return fmt.Errorf("policy: %w", err)
	}
	link, err := (correction.CorrectionLink{Version: correction.Version, CorrectionID: handle, Handle: handle, PrincipalID: projectID, ParentRunID: rootRunID, ParentBindingSHA256: contractDigest("parent"), RootRunID: rootRunID, Attempt: 1, SelectionAlias: "contract-selection", SelectionSHA256: contractDigest("selection"), PolicySHA256: policy.Digest, LimitsSHA256: contractDigest("limits"), CreatedAt: now, UpdatedAt: now}).Seal()
	if err != nil {
		return fmt.Errorf("link: %w", err)
	}
	record := correction.Record{Link: link, RequestHash: contractDigest("request"), KeyHash: contractDigest("key"), Phase: correction.PhaseReserved, Plan: correction.CorrectionPlan{Version: correction.Version}, Owner: "contract-worker", PolicyID: policy.ID, PolicyScenarios: []correction.ScenarioIdentity{}}
	store, err := factory()
	if err != nil {
		return fmt.Errorf("open initial: %w", err)
	}
	reserved, err := store.Reserve(ctx, correction.Reservation{Record: record, Limits: limits})
	if err != nil || !reserved.New {
		return fmt.Errorf("reserve new=%v: %w", reserved.New, err)
	}
	closeCorrectionStore(store)
	reopened, err := factory()
	if err != nil {
		return fmt.Errorf("reopen: %w", err)
	}
	defer closeCorrectionStore(reopened)
	loaded, err := reopened.Get(ctx, handle)
	if err != nil || loaded.Link.RootRunID != rootRunID {
		return fmt.Errorf("load root=%q: %w", loaded.Link.RootRunID, err)
	}
	loaded.Phase = correction.PhasePreflight
	loaded.Link.UpdatedAt = now.Add(time.Second)
	loaded.Link, err = loaded.Link.Seal()
	if err != nil {
		return fmt.Errorf("reseal: %w", err)
	}
	loaded, err = reopened.Update(ctx, handle, correction.PhaseReserved, loaded, "preflight_started")
	if err != nil || loaded.Phase != correction.PhasePreflight {
		return fmt.Errorf("update phase=%q: %w", loaded.Phase, err)
	}
	if _, err = reopened.Update(ctx, handle, correction.PhaseReserved, loaded, "stale_update"); err == nil {
		return fmt.Errorf("stale correction phase accepted")
	}
	events, err := reopened.Events(ctx, handle)
	if err != nil || len(events) != 2 {
		return fmt.Errorf("events count=%d: %w", len(events), err)
	}
	return nil
}

// closeCorrectionStore releases optional local ownership leases while keeping
// the consumer-owned Store interface free of backend lifecycle details.
func closeCorrectionStore(store correction.Store) {
	if closer, ok := store.(interface{ Close() }); ok {
		closer.Close()
	}
}
