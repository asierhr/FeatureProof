// Package contracttest owns backend-neutral executable contracts reused by
// local and PostgreSQL adapter tests.
package contracttest

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"time"

	"github.com/asierhr/featureProof/internal/functionalflow"
)

// FunctionalStore combines the consumer-owned ports whose causal semantics
// must remain identical across persistence backends.
type FunctionalStore interface {
	functionalflow.IntentStore
	functionalflow.EventJournal
	functionalflow.TerminalReconciler
}

// FunctionalFactory reopens the same durable consumer partition.
type FunctionalFactory func() (FunctionalStore, error)

// RunFunctionalStore executes claim, intent, reopen, journal and terminal
// behavior without knowing which backend implements the ports.
func RunFunctionalStore(ctx context.Context, factory FunctionalFactory, runID string) error {
	if factory == nil || runID == "" {
		return fmt.Errorf("functional contract configuration")
	}
	store, err := factory()
	if err != nil {
		return fmt.Errorf("open initial: %w", err)
	}
	claim, created, err := store.Claim(ctx, runID, 1, "step-1", "contract-worker")
	if err != nil || !created || claim.Token == "" {
		return fmt.Errorf("claim created=%v: %w", created, err)
	}
	intent := functionalflow.Intent{RunID: runID, Revision: 1, StepID: "step-1", BindingDigest: contractDigest("binding"), KeyDigest: contractDigest("key"), ClaimToken: claim.Token}
	if err = store.ConfirmIntent(ctx, intent); err != nil {
		return fmt.Errorf("confirm intent: %w", err)
	}
	reopened, err := factory()
	if err != nil {
		return fmt.Errorf("reopen: %w", err)
	}
	loaded, found, err := reopened.Intent(ctx, runID, 1, "step-1")
	if err != nil || !found || loaded.BindingDigest != intent.BindingDigest || loaded.ClaimToken != "" {
		return fmt.Errorf("load intent found=%v token=%q: %w", found, loaded.ClaimToken, err)
	}
	first, err := reopened.AppendEvent(ctx, functionalflow.Event{Type: "artifact_committed", RunID: runID, FlowID: "contract-flow", Revision: 1, ArtifactDigest: contractDigest("artifact")})
	if err != nil || first.Sequence != 1 || first.PreviousDigest != "" {
		return fmt.Errorf("first event sequence=%d: %w", first.Sequence, err)
	}
	second, err := reopened.AppendEvent(ctx, functionalflow.Event{Type: "step_claimed", RunID: runID, FlowID: "contract-flow", Revision: 1, StepID: "step-1", ArtifactDigest: contractDigest("artifact")})
	if err != nil || second.Sequence != 2 || second.PreviousDigest != first.Digest {
		return fmt.Errorf("second event sequence=%d: %w", second.Sequence, err)
	}
	terminal := functionalflow.StepRecord{RunID: runID, Revision: 1, StepID: "step-1", ClaimToken: claim.Token}
	if err = reopened.ConfirmTerminal(ctx, terminal); err != nil {
		return fmt.Errorf("confirm terminal: %w", err)
	}
	terminal.ClaimToken = ""
	if err = reopened.ConfirmTerminal(ctx, terminal); err != nil {
		return fmt.Errorf("terminal replay: %w", err)
	}
	if _, acquired, claimErr := reopened.Claim(ctx, runID, 1, "step-1", "late-worker"); claimErr != nil || acquired {
		return fmt.Errorf("terminal reclaimed=%v: %w", acquired, claimErr)
	}
	events, err := reopened.Events(ctx, runID)
	if err != nil || len(events) != 2 {
		return fmt.Errorf("events count=%d: %w", len(events), err)
	}
	return nil
}

// FixedFunctionalClock supplies one deterministic lease instant to both
// implementations of the shared contract.
type FixedFunctionalClock struct{ At time.Time }

// Now returns the contract-owned instant.
func (clock FixedFunctionalClock) Now() time.Time { return clock.At }

// contractDigest creates bounded canonical identities for contract fixtures.
func contractDigest(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}
