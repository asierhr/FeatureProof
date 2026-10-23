package functionalflow

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"math"
	"time"

	"github.com/asierhr/featureProof/internal/planning"
)

// BindingRevalidator proves contract, target, identity, and owner authority immediately before each attempt.
type BindingRevalidator interface {
	Revalidate(context.Context, string, BoundStep) error
}

// ExecutionConfig supplies finite trusted runtime limits and one explicit owner identity.
type ExecutionConfig struct {
	Owner               string
	MaxCalls            int
	MaxRetries          int
	MaxWork             int
	StepTimeout         time.Duration
	RunTimeout          time.Duration
	ConfirmationTimeout time.Duration
	RetryDelay          time.Duration
}

// Executor owns sequential scheduling, guards, intents, retries, evidence confirmation, and ledger aggregation.
type Executor struct {
	Authority   BindingAuthority
	Clock       Clock
	Policy      Policy
	Revalidator BindingRevalidator
	Tool        Tool
	Evidence    EvidenceStore
	Fixtures    FixtureCaptureStore
	Assertions  AssertionEvaluator
	State       IntentStore
	Journal     EventJournal
	Keys        KeyFactory
	Waiter      Waiter
	Config      ExecutionConfig
}

// Execute runs one verified artifact sequentially and returns a primary result derived only from confirmed proof.
func (e *Executor) Execute(ctx context.Context, runID string, validated ValidatedFlow, artifact Artifact) (Result, error) {
	if err := ValidateArtifactAuthority(validated, artifact, e.Authority); err != nil {
		return Result{}, flowError(CategoryIntegrity, "artifact.binding", err)
	}
	claimWindow, claimWindowOK := totalClaimWindow(e.Config)
	if runID == "" || e.Clock == nil || e.Policy == nil || e.Revalidator == nil || e.Tool == nil || e.Evidence == nil || e.Assertions == nil || e.State == nil || e.Keys == nil || e.Config.Owner == "" || e.Config.MaxCalls <= 0 || e.Config.MaxRetries < 0 || e.Config.MaxWork <= 0 || e.Config.StepTimeout <= 0 || e.Config.RunTimeout <= 0 || e.Config.ConfirmationTimeout <= 0 || e.Config.RetryDelay < 0 || !claimWindowOK {
		return Result{}, flowError(CategoryInternal, "executor", nil)
	}
	runCtx, cancel := context.WithTimeout(ctx, e.Config.RunTimeout)
	defer cancel()
	flow := validated.Flow()
	boundByID := map[string]BoundStep{}
	for _, step := range artifact.Steps {
		boundByID[step.Step.ID] = step
	}
	records := map[string]StepRecord{}
	ledger := Ledger{}
	calls, work := 0, 0
	for _, index := range validated.order {
		step := flow.Steps[index]
		if terminal, ok, err := e.State.Terminal(runCtx, runID, flow.Revision, step.ID); err != nil {
			return Result{}, flowError(CategoryIntegrity, "state.terminal", err)
		} else if ok {
			recorded, journalErr := e.journalContainsTerminal(runCtx, runID, step.ID)
			if journalErr != nil {
				return Result{}, journalErr
			}
			if !recorded {
				if err := e.appendRuntimeEvent(runCtx, artifact, runID, Event{Type: "recovery_resumed", StepID: step.ID, Status: string(terminal.Status)}); err != nil {
					return Result{}, err
				}
				if err := e.appendRuntimeEvent(runCtx, artifact, runID, Event{Type: "step_terminal", StepID: step.ID, Status: string(terminal.Status), Category: terminal.Category, EvidenceIDs: []string{terminal.EvidenceID}}); err != nil {
					return Result{}, err
				}
			}
			records[step.ID] = terminal
			ledger = appendRecordLedger(ledger, step, terminal)
			continue
		}
		if runCtx.Err() != nil {
			record := StepRecord{RunID: runID, Revision: flow.Revision, StepID: step.ID, Status: planning.StepInconclusive, Category: CategoryCancelled}
			confirmCtx, confirmCancel := context.WithTimeout(context.WithoutCancel(runCtx), e.Config.ConfirmationTimeout)
			err := e.State.ConfirmTerminal(confirmCtx, record)
			if err != nil {
				confirmCancel()
				return Result{}, flowError(CategoryIntegrity, "state.cancel", err)
			}
			if err := e.appendRuntimeEvent(confirmCtx, artifact, runID, Event{Type: "step_terminal", StepID: step.ID, Status: string(record.Status), Category: record.Category}); err != nil {
				confirmCancel()
				return Result{}, err
			}
			confirmCancel()
			records[step.ID] = record
			continue
		}
		blocking := directBlocking(step, records)
		if len(blocking) > 0 {
			record := StepRecord{RunID: runID, Revision: flow.Revision, StepID: step.ID, Status: planning.StepBlocked, Blocking: blocking}
			if err := e.State.ConfirmTerminal(runCtx, record); err != nil {
				return Result{}, flowError(CategoryIntegrity, "state.blocked", err)
			}
			if err := e.appendRuntimeEvent(runCtx, artifact, runID, Event{Type: "step_terminal", StepID: step.ID, Status: string(record.Status)}); err != nil {
				return Result{}, err
			}
			records[step.ID] = record
			continue
		}
		if calls >= e.Config.MaxCalls || work >= e.Config.MaxWork {
			record := StepRecord{RunID: runID, Revision: flow.Revision, StepID: step.ID, Status: planning.StepInconclusive, Category: CategoryLimit}
			if err := e.State.ConfirmTerminal(runCtx, record); err != nil {
				return Result{}, flowError(CategoryIntegrity, "state.budget", err)
			}
			if err := e.appendRuntimeEvent(runCtx, artifact, runID, Event{Type: "step_terminal", StepID: step.ID, Status: string(record.Status), Category: record.Category}); err != nil {
				return Result{}, err
			}
			records[step.ID] = record
			continue
		}
		claim, claimed, err := e.State.Claim(runCtx, runID, flow.Revision, step.ID, e.Config.Owner)
		if err != nil {
			return Result{}, flowError(CategoryIntegrity, "state.claim", err)
		}
		if !claimed {
			terminal, ok, err := e.State.Terminal(runCtx, runID, flow.Revision, step.ID)
			if err != nil || !ok {
				return Result{}, flowError(CategoryUncertain, "state.claim", err)
			}
			records[step.ID] = terminal
			ledger = appendRecordLedger(ledger, step, terminal)
			continue
		}
		if !claim.ExpiresAt.After(e.Clock.Now().Add(claimWindow)) {
			return Result{}, flowError(CategoryAuthority, "state.claim_ttl", nil)
		}
		if err := e.appendRuntimeEvent(runCtx, artifact, runID, Event{Type: "step_claimed", StepID: step.ID, OwnerDigest: opaqueDigest(e.Config.Owner)}); err != nil {
			return Result{}, err
		}
		bound := boundByID[step.ID]
		if bound.InputDigest != digestValue(step.Input) {
			return Result{}, flowError(CategoryIntegrity, "artifact.input_digest", nil)
		}
		bound.Step = cloneStep(step)
		key := ""
		if step.Kind == StepAct {
			if bound.Operation.Idempotent || bound.Operation.ReconciliationSafe {
				key = e.Keys.Key(runID, flow.Revision, step.ID)
				if key == "" {
					return Result{}, flowError(CategoryInternal, "key", nil)
				}
			}
			prior, exists, err := e.prepareActionIntent(runCtx, runID, flow.Revision, bound, key, claim.Token)
			if err != nil {
				return Result{}, err
			}
			keyDigest := ""
			if key != "" {
				keyDigest = opaqueDigest(key)
			}
			if err := e.appendRuntimeEvent(runCtx, artifact, runID, Event{Type: "intent_confirmed", StepID: step.ID, Status: "prepared", BindingDigest: bound.Operation.BindingDigest, KeyDigest: keyDigest}); err != nil {
				return Result{}, err
			}
			if exists && prior.EvidenceID != "" {
				record, entries, recoveredEvidence, recoverErr := e.recoverConfirmedEvidence(runCtx, runID, flow.Revision, bound, prior.EvidenceID, claim)
				if recoverErr != nil {
					return Result{}, recoverErr
				}
				record.ClaimToken = claim.Token
				confirmCtx, confirmCancel := context.WithTimeout(context.WithoutCancel(runCtx), e.Config.ConfirmationTimeout)
				if _, renewErr := e.renewClaim(confirmCtx, runID, flow.Revision, step.ID, claim); renewErr != nil {
					confirmCancel()
					return Result{}, renewErr
				}
				if saveErr := e.State.ConfirmTerminal(confirmCtx, record); saveErr != nil {
					confirmCancel()
					return Result{}, flowError(CategoryIntegrity, "state.recover_evidence", saveErr)
				}
				if err := e.appendRuntimeEvent(confirmCtx, artifact, runID, Event{Type: "recovery_resumed", StepID: step.ID, EvidenceIDs: []string{prior.EvidenceID}}); err != nil {
					confirmCancel()
					return Result{}, err
				}
				evidenceEvent, eventErr := evidenceConfirmedEvent(step.ID, 0, recoveredEvidence)
				if eventErr != nil {
					confirmCancel()
					return Result{}, eventErr
				}
				if err := e.appendRuntimeEvent(confirmCtx, artifact, runID, evidenceEvent); err != nil {
					confirmCancel()
					return Result{}, err
				}
				if err := e.appendRuntimeEvent(confirmCtx, artifact, runID, Event{Type: "assertions_confirmed", StepID: step.ID, EvidenceIDs: []string{prior.EvidenceID}}); err != nil {
					confirmCancel()
					return Result{}, err
				}
				if err := e.appendRuntimeEvent(confirmCtx, artifact, runID, Event{Type: "step_terminal", StepID: step.ID, Status: string(record.Status), EvidenceIDs: []string{prior.EvidenceID}}); err != nil {
					confirmCancel()
					return Result{}, err
				}
				confirmCancel()
				record.ClaimToken = ""
				records[step.ID] = record
				for _, entry := range entries {
					ledger, err = ledger.Append(entry)
					if err != nil {
						return Result{}, err
					}
				}
				continue
			}
			if exists && prior.EffectStarted && !bound.Operation.Idempotent && !bound.Operation.ReconciliationSafe {
				record := StepRecord{RunID: runID, Revision: flow.Revision, StepID: step.ID, Status: planning.StepInconclusive, Category: CategoryUncertain, ClaimToken: claim.Token}
				confirmCtx, confirmCancel := context.WithTimeout(context.WithoutCancel(runCtx), e.Config.ConfirmationTimeout)
				if _, renewErr := e.renewClaim(confirmCtx, runID, flow.Revision, step.ID, claim); renewErr != nil {
					confirmCancel()
					return Result{}, renewErr
				}
				if saveErr := e.State.ConfirmTerminal(confirmCtx, record); saveErr != nil {
					confirmCancel()
					return Result{}, flowError(CategoryIntegrity, "state.uncertain", saveErr)
				}
				if err := e.appendRuntimeEvent(confirmCtx, artifact, runID, Event{Type: "step_terminal", StepID: step.ID, Status: string(record.Status), Category: record.Category}); err != nil {
					confirmCancel()
					return Result{}, err
				}
				confirmCancel()
				record.ClaimToken = ""
				records[step.ID] = record
				continue
			}
		}
		resolver := InputResolver{Fixtures: e.Fixtures, Evidence: e.Evidence, MaxBytes: e.Config.MaxWork * 1024}
		input, err := resolver.Resolve(runCtx, runID, flow.Revision, step, records)
		work++
		if err != nil {
			record := terminalForError(runID, flow.Revision, step.ID, err)
			if saveErr := e.State.ConfirmTerminal(runCtx, record); saveErr != nil {
				return Result{}, flowError(CategoryIntegrity, "state.resolve", saveErr)
			}
			if err := e.appendRuntimeEvent(runCtx, artifact, runID, Event{Type: "step_terminal", StepID: step.ID, Status: string(record.Status), Category: record.Category}); err != nil {
				return Result{}, err
			}
			records[step.ID] = record
			continue
		}
		record, resultEntries, usedCalls, err := e.executeStep(runCtx, runID, flow.Revision, artifact, bound, input, key, claim, calls)
		record.ClaimToken = claim.Token
		calls += usedCalls
		if err != nil && record.StepID == "" {
			return Result{}, err
		}
		confirmCtx, confirmCancel := context.WithTimeout(context.WithoutCancel(runCtx), e.Config.ConfirmationTimeout)
		if _, renewErr := e.renewClaim(confirmCtx, runID, flow.Revision, step.ID, claim); renewErr != nil {
			confirmCancel()
			return Result{}, renewErr
		}
		saveErr := e.State.ConfirmTerminal(confirmCtx, record)
		if saveErr != nil {
			confirmCancel()
			return Result{}, flowError(CategoryIntegrity, "state.confirm", saveErr)
		}
		if err := e.appendRuntimeEvent(confirmCtx, artifact, runID, Event{Type: "step_terminal", StepID: step.ID, Status: string(record.Status), Category: record.Category, EvidenceIDs: []string{record.EvidenceID}}); err != nil {
			confirmCancel()
			return Result{}, err
		}
		confirmCancel()
		records[step.ID] = record
		for _, entry := range resultEntries {
			ledger, err = ledger.Append(entry)
			if err != nil {
				return Result{}, err
			}
		}
	}
	comparisons := []ComparisonResult{}
	for _, step := range flow.Steps {
		for _, comparison := range step.Comparisons {
			result, err := EvaluateComparison(runCtx, runID, flow.Revision, comparison, records, e.Evidence)
			if err != nil {
				return Result{}, err
			}
			result.StepID = step.ID
			comparisons = append(comparisons, result)
			synthetic := planning.AssertionResult{DeclarationID: comparison.ID, NodePath: comparison.ID, Operator: string(comparison.Operator), Status: result.Status, EvidenceIDs: canonicalEvidenceIDs([]string{result.BeforeEvidenceID, result.AfterEvidenceID})}
			ledger, err = ledger.Append(LedgerEntry{Revision: flow.Revision, StepID: step.ID, ConstraintID: step.ConstraintID, Result: synthetic, EvidenceIDs: synthetic.EvidenceIDs})
			if err != nil {
				return Result{}, err
			}
			if err := e.appendRuntimeEvent(runCtx, artifact, runID, Event{Type: "comparisons_confirmed", StepID: step.ID, DeclarationID: comparison.ID, EvidenceIDs: synthetic.EvidenceIDs}); err != nil {
				return Result{}, err
			}
		}
	}
	orderedSteps := sortStepRecords(flow, records)
	constraints, outcome := ledger.Aggregate(flow, orderedSteps, runCtx.Err() != nil)
	return Result{FlowID: flow.ID, Revision: flow.Revision, Steps: orderedSteps, Ledger: orderedLedger(ledger.Entries(), flow), Comparisons: comparisons, Constraints: constraints, Outcome: outcome, ArtifactDigest: artifact.ArtifactDigest}, nil
}

// journalContainsTerminal prevents recovery from appending a second terminal for already authenticated work.
func (e *Executor) journalContainsTerminal(ctx context.Context, runID, stepID string) (bool, error) {
	if e.Journal == nil {
		return false, nil
	}
	events, err := e.Journal.Events(ctx, runID)
	if err != nil {
		return false, flowError(CategoryIntegrity, "journal.events", err)
	}
	found := false
	for _, event := range events {
		if event.Type == "step_terminal" && event.StepID == stepID {
			if found {
				return false, flowError(CategoryIntegrity, "journal.terminal", nil)
			}
			found = true
		}
	}
	return found, nil
}

// totalClaimWindow bounds ownership across every permitted attempt, retry wait, and terminal confirmation boundary.
func totalClaimWindow(config ExecutionConfig) (time.Duration, bool) {
	attempts := int64(config.MaxRetries) + 1
	if attempts <= 0 || int64(config.StepTimeout) > math.MaxInt64/attempts {
		return 0, false
	}
	total := time.Duration(attempts * int64(config.StepTimeout))
	retries := int64(config.MaxRetries)
	if retries > 0 {
		if int64(config.RetryDelay) > math.MaxInt64/retries || int64(total) > math.MaxInt64-retries*int64(config.RetryDelay) {
			return 0, false
		}
		total += time.Duration(retries * int64(config.RetryDelay))
	}
	if int64(total) > math.MaxInt64-int64(config.ConfirmationTimeout) {
		return 0, false
	}
	return total + config.ConfirmationTimeout, true
}

// appendRuntimeEvent persists one safe causal checkpoint when a production journal is composed.
func (e *Executor) appendRuntimeEvent(ctx context.Context, artifact Artifact, runID string, event Event) error {
	if e.Journal == nil {
		return nil
	}
	event.RunID, event.FlowID, event.Revision, event.ArtifactDigest = runID, artifact.FlowID, artifact.Revision, artifact.ArtifactDigest
	if _, err := e.Journal.AppendEvent(ctx, event); err != nil {
		return flowError(CategoryIntegrity, "journal.append", err)
	}
	return nil
}

// executeStep applies all runtime guards on every attempt and confirms evidence before assertions.
func (e *Executor) executeStep(ctx context.Context, runID string, revision uint64, artifact Artifact, bound BoundStep, input map[string]any, key string, claim Claim, usedCalls int) (StepRecord, []LedgerEntry, int, error) {
	step := bound.Step
	attempts := e.Config.MaxRetries + 1
	for attempt := 1; attempt <= attempts; attempt++ {
		if err := e.appendRuntimeEvent(ctx, artifact, runID, Event{Type: "attempt_started", StepID: step.ID, Attempt: attempt, BindingDigest: bound.Operation.BindingDigest}); err != nil {
			return StepRecord{}, nil, attempt - 1, err
		}
		stepCtx, cancel := context.WithTimeout(ctx, e.Config.StepTimeout)
		if err := e.Revalidator.Revalidate(stepCtx, runID, bound); err != nil {
			cancel()
			return terminalForError(runID, revision, step.ID, flowError(CategoryAuthority, "binding", err)), nil, attempt - 1, nil
		}
		decision, err := e.Policy.Evaluate(stepCtx, runID, bound)
		if err != nil || decision != PolicyAllowed {
			cancel()
			return terminalForError(runID, revision, step.ID, flowError(CategoryPolicy, "policy", err)), nil, attempt - 1, nil
		}
		if usedCalls+attempt > e.Config.MaxCalls {
			cancel()
			return terminalForError(runID, revision, step.ID, flowError(CategoryLimit, "calls", nil)), nil, attempt - 1, nil
		}
		if step.Kind == StepAct {
			if err := e.markActionProgress(stepCtx, runID, revision, bound, key, claim.Token, true, ""); err != nil {
				cancel()
				return StepRecord{}, nil, attempt - 1, err
			}
		}
		response, toolErr := e.Tool.Execute(stepCtx, ExecutionRequest{RunID: runID, Revision: revision, StepID: step.ID, Operation: bound.Operation, Input: input, IdempotencyKey: key, Attempt: attempt})
		cancel()
		if toolErr != nil {
			if ctx.Err() != nil {
				return terminalForError(runID, revision, step.ID, ctx.Err()), nil, attempt, nil
			}
			canRetry := response.Transient && (step.Kind == StepObserve || bound.Operation.Idempotent || bound.Operation.ReconciliationSafe) && attempt < attempts
			if canRetry {
				if err := e.appendRuntimeEvent(ctx, artifact, runID, Event{Type: "retry_scheduled", StepID: step.ID, Attempt: attempt, Category: CategoryUncertain}); err != nil {
					return StepRecord{}, nil, attempt, err
				}
				if e.Waiter == nil {
					return StepRecord{}, nil, attempt, flowError(CategoryInternal, "retry.waiter", nil)
				}
				if err := e.Waiter.Wait(ctx, e.Config.RetryDelay); err != nil {
					return terminalForError(runID, revision, step.ID, err), nil, attempt, nil
				}
				continue
			}
			category := CategoryInternal
			if response.CrossedNetwork {
				category = CategoryUncertain
			}
			return StepRecord{RunID: runID, Revision: revision, StepID: step.ID, Status: planning.StepInconclusive, Category: category}, nil, attempt, nil
		}
		confirmationCtx, confirmationCancel := context.WithTimeout(context.WithoutCancel(ctx), e.Config.ConfirmationTimeout)
		claim, err = e.renewClaim(confirmationCtx, runID, revision, step.ID, claim)
		if err != nil {
			confirmationCancel()
			return StepRecord{}, nil, attempt, err
		}
		evidence := SealEvidence(Evidence{ID: response.EvidenceID, RunID: runID, Revision: revision, StepID: step.ID, BindingDigest: bound.Operation.BindingDigest, ResourceBindingDigest: response.ResourceBindingDigest, Output: response.Output, Terminal: true, Truncated: response.Truncated})
		confirmed, err := e.Evidence.Confirm(confirmationCtx, evidence)
		if err != nil || !validEvidence(confirmed, runID, revision, step.ID) {
			confirmationCancel()
			return StepRecord{}, nil, attempt, flowError(CategoryIntegrity, "evidence", err)
		}
		claim, err = e.renewClaim(confirmationCtx, runID, revision, step.ID, claim)
		if err != nil {
			confirmationCancel()
			return StepRecord{}, nil, attempt, err
		}
		evidenceEvent, eventErr := evidenceConfirmedEvent(step.ID, attempt, confirmed)
		if eventErr != nil {
			confirmationCancel()
			return StepRecord{}, nil, attempt, eventErr
		}
		if err := e.appendRuntimeEvent(confirmationCtx, artifact, runID, evidenceEvent); err != nil {
			confirmationCancel()
			return StepRecord{}, nil, attempt, err
		}
		if step.Kind == StepAct {
			claim, err = e.renewClaim(confirmationCtx, runID, revision, step.ID, claim)
			if err != nil {
				confirmationCancel()
				return StepRecord{}, nil, attempt, err
			}
			if err := e.markActionProgress(confirmationCtx, runID, revision, bound, key, claim.Token, true, confirmed.ID); err != nil {
				confirmationCancel()
				return StepRecord{}, nil, attempt, err
			}
		}
		claim, err = e.renewClaim(confirmationCtx, runID, revision, step.ID, claim)
		if err != nil {
			confirmationCancel()
			return StepRecord{}, nil, attempt, err
		}
		assertions, err := e.Assertions.Evaluate(confirmationCtx, step.Assertions, confirmed)
		if err != nil {
			confirmationCancel()
			return StepRecord{}, nil, attempt, flowError(CategoryIntegrity, "assertions", err)
		}
		claim, err = e.renewClaim(confirmationCtx, runID, revision, step.ID, claim)
		if err != nil {
			confirmationCancel()
			return StepRecord{}, nil, attempt, err
		}
		if err := e.appendRuntimeEvent(confirmationCtx, artifact, runID, Event{Type: "assertions_confirmed", StepID: step.ID, Attempt: attempt, EvidenceIDs: []string{confirmed.ID}}); err != nil {
			confirmationCancel()
			return StepRecord{}, nil, attempt, err
		}
		confirmationCancel()
		status := planning.StepPassed
		entries := make([]LedgerEntry, len(assertions))
		for i, result := range assertions {
			result.EvidenceIDs = canonicalEvidenceIDs(append(result.EvidenceIDs, confirmed.ID))
			assertions[i] = result
			if result.Status == planning.AssertionFailed {
				status = planning.StepFailed
			} else if result.Status == planning.AssertionInconclusive && status == planning.StepPassed {
				status = planning.StepInconclusive
			}
			entries[i] = LedgerEntry{Revision: revision, StepID: step.ID, ConstraintID: step.ConstraintID, Result: result, EvidenceIDs: result.EvidenceIDs}
		}
		if response.FunctionalFailure && status == planning.StepPassed {
			status = planning.StepFailed
		}
		return StepRecord{RunID: runID, Revision: revision, StepID: step.ID, Status: status, EvidenceID: confirmed.ID, Assertions: assertions}, entries, attempt, nil
	}
	return StepRecord{RunID: runID, Revision: revision, StepID: step.ID, Status: planning.StepInconclusive, Category: CategoryLimit}, nil, attempts, nil
}

// renewClaim extends and verifies exclusive ownership before a blocking post-tool checkpoint.
func (e *Executor) renewClaim(ctx context.Context, runID string, revision uint64, stepID string, claim Claim) (Claim, error) {
	renewed, err := e.State.RenewClaim(ctx, runID, revision, stepID, claim)
	if err != nil || renewed.Owner != claim.Owner || renewed.Token != claim.Token || !renewed.ExpiresAt.After(e.Clock.Now().Add(e.Config.ConfirmationTimeout)) {
		return Claim{}, flowError(CategoryAuthority, "state.claim_renew", err)
	}
	return renewed, nil
}

// prepareActionIntent durably binds one effect before input resolution and returns any prior crash checkpoint.
func (e *Executor) prepareActionIntent(ctx context.Context, runID string, revision uint64, bound BoundStep, key, claimToken string) (Intent, bool, error) {
	intent := Intent{RunID: runID, Revision: revision, StepID: bound.Step.ID, BindingDigest: bound.Operation.BindingDigest, KeyDigest: opaqueDigest(key), ClaimToken: claimToken}
	if key == "" {
		intent.KeyDigest = ""
	}
	if prior, ok, err := e.State.Intent(ctx, runID, revision, bound.Step.ID); err != nil {
		return Intent{}, false, flowError(CategoryIntegrity, "intent.load", err)
	} else if ok && !sameIntent(prior, intent) {
		return Intent{}, false, flowError(CategoryIntegrity, "intent.conflict", nil)
	} else if ok {
		intent.EffectStarted, intent.EvidenceID = prior.EffectStarted, prior.EvidenceID
		intent.ClaimToken = claimToken
		if err := e.State.ConfirmIntent(ctx, intent); err != nil {
			return Intent{}, false, flowError(CategoryIntegrity, "intent.confirm", err)
		}
		return prior, true, nil
	}
	if err := e.State.ConfirmIntent(ctx, intent); err != nil {
		return Intent{}, false, flowError(CategoryIntegrity, "intent.confirm", err)
	}
	return Intent{}, false, nil
}

// markActionProgress advances the durable effect checkpoint before I/O and after confirmed evidence.
func (e *Executor) markActionProgress(ctx context.Context, runID string, revision uint64, bound BoundStep, key, claimToken string, started bool, evidenceID string) error {
	keyDigest := ""
	if key != "" {
		keyDigest = opaqueDigest(key)
	}
	intent := Intent{RunID: runID, Revision: revision, StepID: bound.Step.ID, BindingDigest: bound.Operation.BindingDigest, KeyDigest: keyDigest, EffectStarted: started, EvidenceID: evidenceID, ClaimToken: claimToken}
	if err := e.State.ConfirmIntent(ctx, intent); err != nil {
		return flowError(CategoryIntegrity, "intent.progress", err)
	}
	return nil
}

// recoverConfirmedEvidence finishes deterministic assertions from an evidence checkpoint without replaying the tool.
func (e *Executor) recoverConfirmedEvidence(ctx context.Context, runID string, revision uint64, bound BoundStep, evidenceID string, claim Claim) (StepRecord, []LedgerEntry, Evidence, error) {
	confirmationCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), e.Config.ConfirmationTimeout)
	defer cancel()
	var err error
	claim, err = e.renewClaim(confirmationCtx, runID, revision, bound.Step.ID, claim)
	if err != nil {
		return StepRecord{}, nil, Evidence{}, err
	}
	evidence, err := e.Evidence.Load(confirmationCtx, evidenceID)
	if err != nil || !validEvidence(evidence, runID, revision, bound.Step.ID) {
		return StepRecord{}, nil, Evidence{}, flowError(CategoryIntegrity, "intent.evidence", err)
	}
	if _, err = e.renewClaim(confirmationCtx, runID, revision, bound.Step.ID, claim); err != nil {
		return StepRecord{}, nil, Evidence{}, err
	}
	assertions, err := e.Assertions.Evaluate(confirmationCtx, bound.Step.Assertions, evidence)
	if err != nil {
		return StepRecord{}, nil, Evidence{}, flowError(CategoryIntegrity, "assertions", err)
	}
	status := planning.StepPassed
	entries := make([]LedgerEntry, len(assertions))
	for i, result := range assertions {
		result.EvidenceIDs = canonicalEvidenceIDs(append(result.EvidenceIDs, evidence.ID))
		assertions[i] = result
		if result.Status == planning.AssertionFailed {
			status = planning.StepFailed
		} else if result.Status == planning.AssertionInconclusive && status == planning.StepPassed {
			status = planning.StepInconclusive
		}
		entries[i] = LedgerEntry{Revision: revision, StepID: bound.Step.ID, ConstraintID: bound.Step.ConstraintID, Result: result, EvidenceIDs: result.EvidenceIDs}
	}
	return StepRecord{RunID: runID, Revision: revision, StepID: bound.Step.ID, Status: status, EvidenceID: evidence.ID, Assertions: assertions}, entries, evidence, nil
}

// evidenceConfirmedEvent binds one causal checkpoint to the full safe evidence manifest without exposing output.
func evidenceConfirmedEvent(stepID string, attempt int, evidence Evidence) (Event, error) {
	record, err := SealEvidenceRecord(evidence)
	if err != nil {
		return Event{}, err
	}
	return Event{Type: "evidence_confirmed", StepID: stepID, Attempt: attempt, BindingDigest: record.BindingDigest, ResourceBindingDigest: record.ResourceBindingDigest, ContentDigest: record.ContentDigest, RecordDigest: record.RecordDigest, EvidenceIDs: []string{record.ID}}, nil
}

// sameIntent compares durable action coordinates while excluding the ephemeral owner claim token.
func sameIntent(left, right Intent) bool {
	left.ClaimToken, right.ClaimToken = "", ""
	left.EffectStarted, right.EffectStarted = false, false
	left.EvidenceID, right.EvidenceID = "", ""
	return left == right
}

// directBlocking collects every direct non-passing dependency in declaration order.
func directBlocking(step Step, records map[string]StepRecord) []planning.BlockingCause {
	out := []planning.BlockingCause{}
	for _, dependency := range step.DependsOn {
		record, ok := records[dependency]
		if !ok || record.Status != planning.StepPassed {
			status := planning.StepInconclusive
			if ok {
				status = record.Status
			}
			out = append(out, planning.BlockingCause{StepID: dependency, Status: status})
		}
	}
	return out
}

// terminalForError maps safe operational categories to an inconclusive or cancelled step without publishing error text.
func terminalForError(runID string, revision uint64, stepID string, err error) StepRecord {
	category := CategoryInternal
	var typed *Error
	if errors.As(err, &typed) {
		category = typed.Category
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		category = CategoryCancelled
	}
	return StepRecord{RunID: runID, Revision: revision, StepID: stepID, Status: planning.StepInconclusive, Category: category}
}

// appendRecordLedger restores confirmed assertion entries without re-evaluating tools during recovery.
func appendRecordLedger(ledger Ledger, step Step, record StepRecord) Ledger {
	for _, result := range record.Assertions {
		next, err := ledger.Append(LedgerEntry{Revision: record.Revision, StepID: step.ID, ConstraintID: step.ConstraintID, Result: result, EvidenceIDs: result.EvidenceIDs})
		if err == nil {
			ledger = next
		}
	}
	return ledger
}

// opaqueDigest binds a system key into durable state without persisting the key itself.
func opaqueDigest(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}
