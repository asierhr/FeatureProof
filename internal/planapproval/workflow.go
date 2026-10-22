package planapproval

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/asierhr/featureProof/internal/agentplanning"
	"github.com/asierhr/featureProof/internal/harness"
	"github.com/asierhr/featureProof/internal/testidentity"
)

// ApprovalWorkflow coordinates staging and human decisions while never owning an executor or tool handle.
type ApprovalWorkflow struct {
	store     ApprovalStore
	lifecycle *runLifecycle
	sanitizer Sanitizer
	clock     Clock
	limits    Limits
}

// NewWorkflow validates the fail-closed collaborators and budgets required by every approval operation.
func NewWorkflow(store ApprovalStore, sanitizer Sanitizer, clock Clock, limits Limits, run *harness.Run) (*ApprovalWorkflow, error) {
	if store == nil || sanitizer == nil || clock == nil || !validLimits(limits) || run == nil {
		return nil, typedError(CategoryInvalidInput, "compose", "dependencies", nil)
	}
	lifecycle, err := store.BindRun(run)
	if err != nil {
		return nil, err
	}
	return &ApprovalWorkflow{store: store, lifecycle: lifecycle, sanitizer: sanitizer, clock: clock, limits: limits}, nil
}

// ReopenForReplan records that a consumed approved batch finished review and permits a fresh approval revision.
func (w *ApprovalWorkflow) ReopenForReplan(ctx context.Context, commandID string) error {
	if commandID == "" {
		return typedError(CategoryInvalidInput, "reopen", "command_id", nil)
	}
	run := w.lifecycle.run
	if err := ctx.Err(); err != nil {
		return contextFailure("reopen", err)
	}
	events, err := w.store.Events(ctx, run.ID)
	if err != nil {
		return err
	}
	projection, err := Project(ctx, events, w.limits.MaxEvents)
	if err != nil {
		return err
	}
	if projection.State == harness.StatusPlanning && run.Status() == harness.StatusPlanning {
		return nil
	}
	if projection.State != harness.StatusExecuting || !projection.Consumed || run.Status() != harness.StatusReviewing {
		return typedError(CategoryInvalidState, "reopen", "state", nil)
	}
	token, err := w.lifecycle.reserveLifecycle(harness.StatusReviewing)
	if err != nil {
		return err
	}
	defer w.lifecycle.releaseLifecycle(token)
	confirmed, err := w.store.Events(ctx, run.ID)
	if err != nil {
		return err
	}
	latest, err := Project(ctx, confirmed, w.limits.MaxEvents)
	if err != nil {
		return err
	}
	if !sameProjectionVersion(projection, latest) {
		return typedError(CategoryRevisionConflict, "reopen", "projection", nil)
	}
	event := ApprovalEvent{Version: 1, Revision: projection.Revision, EventID: eventID(run.ID, projection.Revision, EventReplanReady), DecisionID: commandID, Type: EventReplanReady, RunID: run.ID, PlanID: projection.PlanID, PlanHash: projection.PlanHash, ContextHash: projection.ContextHash, ViewHash: projection.ViewHash, OccurredAt: utc(w.clock.Now()), CommandFingerprint: projection.PlanHash + ":" + projection.ContextHash}
	appended, _, err := w.store.CompareAndAppend(ctx, run.ID, projection.Sequence, commandID, event)
	if err != nil {
		return err
	}
	projected, err := w.projectConfirmed(ctx, run.ID, confirmed, appended)
	if err != nil {
		return err
	}
	if projected.State != harness.StatusPlanning {
		return typedError(CategoryIntegrityFailure, "reopen", "projection", nil)
	}
	return w.lifecycle.commitLifecycle(token, projected.State)
}

// validLimits ensures every traversed approval dimension has an explicit positive bound.
func validLimits(limits Limits) bool {
	return limits.MaxBytes > 0 && limits.MaxDepth > 0 && limits.MaxEvents > 0 && limits.MaxConstraints > 0 && limits.MaxChecks > 0 && limits.MaxSteps > 0 && limits.MaxAssertions > 0 && limits.MaxEvidence > 0
}

// Stage snapshots one fully prepared plan, publishes its safe view and atomically enters awaiting approval.
func (w *ApprovalWorkflow) Stage(ctx context.Context, command StageCommand) (ApprovalView, error) {
	run := w.lifecycle.run
	if err := ctx.Err(); err != nil {
		return ApprovalView{}, contextFailure("stage", err)
	}
	if strings.TrimSpace(run.ID) == "" || command.Plan.ID() == "" {
		return ApprovalView{}, typedError(CategoryInvalidInput, "stage", "command", nil)
	}
	events, err := w.store.Events(ctx, run.ID)
	if err != nil {
		return ApprovalView{}, err
	}
	projection, err := Project(ctx, events, w.limits.MaxEvents)
	if err != nil {
		return ApprovalView{}, err
	}
	if run.Status() != harness.StatusPlanning {
		return ApprovalView{}, typedError(CategoryInvalidState, "stage", "state", nil)
	}
	if projection.State != harness.StatusPlanning {
		return ApprovalView{}, typedError(CategoryInvalidState, "stage", "state", nil)
	}
	if !command.Plan.MatchesProvenance(provenanceInput(command.Context)) {
		return ApprovalView{}, typedError(CategoryInvalidInput, "stage", "plan_provenance", nil)
	}
	planHash, err := fingerprintPlan(ctx, command.Plan, w.limits)
	if err != nil {
		return ApprovalView{}, err
	}
	contextHashes, err := fingerprintContext(ctx, command.Context, w.limits)
	if err != nil {
		return ApprovalView{}, err
	}
	revision := projection.Revision + 1
	view, err := buildView(ctx, run.ID, revision, command.Plan, command.Context, planHash, contextHashes.All, w.sanitizer, w.limits)
	if err != nil {
		return ApprovalView{}, err
	}
	viewHash, err := canonicalHash(ctx, view, w.limits, "fingerprint_view")
	if err != nil {
		return ApprovalView{}, err
	}
	token, err := w.lifecycle.reserveLifecycle(harness.StatusPlanning)
	if err != nil {
		return ApprovalView{}, err
	}
	defer w.lifecycle.releaseLifecycle(token)
	confirmedEvents, err := w.store.Events(ctx, run.ID)
	if err != nil {
		return ApprovalView{}, err
	}
	confirmedProjection, err := Project(ctx, confirmedEvents, w.limits.MaxEvents)
	if err != nil {
		return ApprovalView{}, err
	}
	if !sameProjectionVersion(projection, confirmedProjection) {
		return ApprovalView{}, typedError(CategoryRevisionConflict, "stage", "projection", nil)
	}
	request := approvalRequest{runID: run.ID, planID: command.Plan.ID(), planHash: planHash, viewHash: viewHash, contextHashes: contextHashes, revision: revision, plan: command.Plan, context: cloneContext(command.Context), view: cloneView(view)}
	if err := w.store.SaveRequest(ctx, request); err != nil {
		return ApprovalView{}, err
	}
	event := ApprovalEvent{Version: 1, Revision: revision, EventID: eventID(run.ID, revision, EventApprovalRequested), Type: EventApprovalRequested, RunID: run.ID, PlanID: command.Plan.ID(), PlanHash: planHash, ContextHash: contextHashes.All, ViewHash: viewHash, OccurredAt: utc(w.clock.Now())}
	confirmed, _, err := w.store.CompareAndAppend(ctx, run.ID, projection.Sequence, "", event)
	if err != nil {
		return ApprovalView{}, err
	}
	projected, projectionErr := w.projectConfirmed(ctx, run.ID, confirmedEvents, confirmed)
	if projected.State == "" {
		return ApprovalView{}, projectionErr
	}
	if projected.Revision != revision || projected.State != harness.StatusAwaitingApproval {
		return ApprovalView{}, typedError(CategoryIntegrityFailure, "stage", "confirmed_projection", nil)
	}
	if err := w.lifecycle.commitLifecycle(token, projected.State); err != nil {
		return ApprovalView{}, err
	}
	if projectionErr != nil {
		return ApprovalView{}, projectionErr
	}
	return cloneView(view), nil
}

// Restage confirms that an already staged request has exactly the supplied plan and context after recovery.
func (w *ApprovalWorkflow) Restage(ctx context.Context, command StageCommand) (ApprovalView, error) {
	run := w.lifecycle.run
	if err := ctx.Err(); err != nil {
		return ApprovalView{}, contextFailure("restage", err)
	}
	if strings.TrimSpace(run.ID) == "" || command.Plan.ID() == "" || run.Status() != harness.StatusAwaitingApproval {
		return ApprovalView{}, typedError(CategoryInvalidState, "restage", "state", nil)
	}
	events, err := w.store.Events(ctx, run.ID)
	if err != nil {
		return ApprovalView{}, err
	}
	projection, err := Project(ctx, events, w.limits.MaxEvents)
	if err != nil {
		return ApprovalView{}, err
	}
	planHash, err := fingerprintPlan(ctx, command.Plan, w.limits)
	if err != nil {
		return ApprovalView{}, err
	}
	contextHashes, err := fingerprintContext(ctx, command.Context, w.limits)
	if err != nil {
		return ApprovalView{}, err
	}
	if projection.State != harness.StatusAwaitingApproval || projection.PlanID != command.Plan.ID() || projection.PlanHash != planHash || projection.ContextHash != contextHashes.All {
		return ApprovalView{}, typedError(CategoryStaleApproval, "restage", "binding", nil)
	}
	return w.View(ctx, run.ID)
}

// View returns the safe immutable projection for the currently awaiting revision.
func (w *ApprovalWorkflow) View(ctx context.Context, runID string) (ApprovalView, error) {
	events, err := w.store.Events(ctx, runID)
	if err != nil {
		return ApprovalView{}, err
	}
	projection, err := Project(ctx, events, w.limits.MaxEvents)
	if err != nil {
		return ApprovalView{}, err
	}
	if projection.State != harness.StatusAwaitingApproval {
		return ApprovalView{}, typedError(CategoryInvalidState, "view", "state", nil)
	}
	request, err := w.store.Request(ctx, runID, projection.Revision)
	if err != nil {
		return ApprovalView{}, err
	}
	if err := w.validateRequest(ctx, projection, request); err != nil {
		return ApprovalView{}, err
	}
	return cloneView(request.view), nil
}

// ViewRevision reconstructs a validated safe view for an already requested or approved revision during replan recovery.
func (w *ApprovalWorkflow) ViewRevision(ctx context.Context, runID string, revision uint64) (ApprovalView, error) {
	events, err := w.store.Events(ctx, runID)
	if err != nil {
		return ApprovalView{}, err
	}
	projection, err := Project(ctx, events, w.limits.MaxEvents)
	if err != nil {
		return ApprovalView{}, err
	}
	if projection.Revision != revision || projection.State != harness.StatusAwaitingApproval && projection.State != harness.StatusExecuting {
		return ApprovalView{}, typedError(CategoryInvalidState, "view_revision", "state", nil)
	}
	request, err := w.store.Request(ctx, runID, revision)
	if err != nil {
		return ApprovalView{}, err
	}
	if err := w.validateRequest(ctx, projection, request); err != nil {
		return ApprovalView{}, err
	}
	return cloneView(request.view), nil
}

// RecoverAuthorization reconstructs an unconsumed approved grant from confirmed events without a new human decision.
func (w *ApprovalWorkflow) RecoverAuthorization(ctx context.Context, runID string, revision uint64, prepared agentplanning.PreparedPlan, current ContractSnapshot) (*ExecutionAuthorization, error) {
	events, err := w.store.Events(ctx, runID)
	if err != nil {
		return nil, err
	}
	projection, err := Project(ctx, events, w.limits.MaxEvents)
	if err != nil {
		return nil, err
	}
	if projection.Revision != revision || projection.State != harness.StatusExecuting || projection.Consumed || projection.PlanID != prepared.ID() || w.lifecycle.run.ID != runID {
		return nil, typedError(CategoryInvalidState, "recover_authorization", "revision", nil)
	}
	request, err := w.store.Request(ctx, runID, revision)
	if err != nil {
		return nil, err
	}
	if err := w.validateRequest(ctx, projection, request); err != nil {
		return nil, err
	}
	planHash, err := fingerprintPlan(ctx, prepared, w.limits)
	if err != nil {
		return nil, err
	}
	contextHashes, err := fingerprintContext(ctx, current, w.limits)
	if err != nil {
		return nil, err
	}
	if planHash != request.planHash || contextHashes.All != request.contextHashes.All {
		return nil, typedError(CategoryStaleApproval, "recover_authorization", "snapshot", nil)
	}
	for i := len(events) - 1; i >= 0; i-- {
		if events[i].Revision == revision && events[i].Type == EventApproved && events[i].DecisionID == projection.DecisionID {
			return newAuthorization(request, events[i].DecisionID, prepared, current), nil
		}
	}
	return nil, typedError(CategoryIntegrityFailure, "recover_authorization", "approval", nil)
}

// Decide applies one authenticated closed action with CAS and returns a grant only for a confirmed current approval.
func (w *ApprovalWorkflow) Decide(ctx context.Context, command DecisionCommand) (DecisionResult, error) {
	run := w.lifecycle.run
	if err := validateDecision(command); err != nil {
		return DecisionResult{}, err
	}
	events, err := w.store.Events(ctx, run.ID)
	if err != nil {
		return DecisionResult{}, err
	}
	commandHash, err := canonicalHash(ctx, struct {
		RunID, PlanID, DecisionID string
		Revision                  uint64
		Actor                     Actor
		Action                    Action
	}{run.ID, command.PlanID, command.DecisionID, command.ExpectedRevision, command.Actor, command.Action}, w.limits, "decision")
	if err != nil {
		return DecisionResult{}, err
	}
	for _, event := range events {
		if event.DecisionID == command.DecisionID {
			if event.CommandFingerprint != commandHash {
				return DecisionResult{}, typedError(CategoryDecisionConflict, "decide", "decision_id", nil)
			}
			projection, projectErr := Project(ctx, events, w.limits.MaxEvents)
			if projectErr != nil {
				return DecisionResult{}, projectErr
			}
			token, reserveErr := w.lifecycle.reserveLifecycle(projection.State)
			if reserveErr != nil {
				return DecisionResult{}, reserveErr
			}
			defer w.lifecycle.releaseLifecycle(token)
			result, replayErr := w.replayDecision(ctx, event, command)
			if replayErr != nil {
				return DecisionResult{}, replayErr
			}
			if commitErr := w.lifecycle.commitLifecycle(token, result.Projection.State); commitErr != nil {
				return DecisionResult{}, commitErr
			}
			return result, nil
		}
	}
	projection, err := Project(ctx, events, w.limits.MaxEvents)
	if err != nil {
		return DecisionResult{}, err
	}
	if projection.State != harness.StatusAwaitingApproval {
		return DecisionResult{}, typedError(CategoryInvalidState, "decide", "state", nil)
	}
	if run.Status() != harness.StatusAwaitingApproval {
		return DecisionResult{}, typedError(CategoryInvalidState, "decide", "run", nil)
	}
	if projection.Revision != command.ExpectedRevision || projection.PlanID != command.PlanID {
		return DecisionResult{}, typedError(CategoryRevisionConflict, "decide", "revision", nil)
	}
	request, err := w.store.Request(ctx, run.ID, projection.Revision)
	if err != nil {
		return DecisionResult{}, err
	}
	if err := w.validateRequest(ctx, projection, request); err != nil {
		return DecisionResult{}, err
	}
	token, err := w.lifecycle.reserveLifecycle(harness.StatusAwaitingApproval)
	if err != nil {
		return DecisionResult{}, err
	}
	defer w.lifecycle.releaseLifecycle(token)
	confirmedEvents, err := w.store.Events(ctx, run.ID)
	if err != nil {
		return DecisionResult{}, err
	}
	confirmedProjection, err := Project(ctx, confirmedEvents, w.limits.MaxEvents)
	if err != nil {
		return DecisionResult{}, err
	}
	if !sameProjectionVersion(projection, confirmedProjection) {
		return DecisionResult{}, typedError(CategoryRevisionConflict, "decide", "projection", nil)
	}
	now := utc(w.clock.Now())
	event := ApprovalEvent{Version: 1, Revision: projection.Revision, DecisionID: command.DecisionID, RunID: run.ID, PlanID: command.PlanID, PlanHash: request.planHash, ContextHash: request.contextHashes.All, ViewHash: request.viewHash, OccurredAt: now, Actor: &Actor{Subject: command.Actor.Subject, Source: command.Actor.Source}, CommandFingerprint: commandHash}
	result := DecisionResult{}
	switch command.Action {
	case ActionApprove:
		currentPlanHash, hashErr := fingerprintPlan(ctx, command.CurrentPlan, w.limits)
		if hashErr != nil {
			return DecisionResult{}, hashErr
		}
		currentContextHashes, hashErr := fingerprintContext(ctx, command.CurrentContext, w.limits)
		if hashErr != nil {
			return DecisionResult{}, hashErr
		}
		if reason := staleReason(request.planHash, currentPlanHash, request.contextHashes, currentContextHashes); reason != "" {
			event.Type, event.InvalidationReason = EventInvalidated, reason
			event.EventID = eventID(run.ID, projection.Revision, event.Type)
			confirmed, _, appendErr := w.store.CompareAndAppend(ctx, run.ID, projection.Sequence, command.DecisionID, event)
			if appendErr != nil {
				return DecisionResult{}, appendErr
			}
			projected, projectErr := w.projectConfirmed(ctx, run.ID, confirmedEvents, confirmed)
			if projected.State == "" {
				return DecisionResult{}, projectErr
			}
			if transitionErr := w.lifecycle.commitLifecycle(token, projected.State); transitionErr != nil {
				return DecisionResult{}, transitionErr
			}
			if projectErr != nil {
				return DecisionResult{}, projectErr
			}
			return DecisionResult{Projection: projected}, typedError(CategoryStaleApproval, "decide", string(reason), nil)
		}
		event.Type = EventApproved
	case ActionRejectTerminal:
		event.Type, event.Disposition = EventRejected, "terminal"
	case ActionRejectReplan:
		event.Type, event.Disposition = EventRejected, "replan"
	case ActionRegenerate:
		event.Type, event.InvalidationReason = EventInvalidated, ReasonRegenerationRequested
	case ActionEditConstraints:
		event.Type, event.InvalidationReason = EventInvalidated, ReasonConstraintsEdited
	}
	event.EventID = eventID(run.ID, projection.Revision, event.Type)
	confirmed, replayed, err := w.store.CompareAndAppend(ctx, run.ID, projection.Sequence, command.DecisionID, event)
	if err != nil {
		return DecisionResult{}, err
	}
	projected, projectionErr := w.projectConfirmed(ctx, run.ID, confirmedEvents, confirmed)
	if projected.State == "" {
		return DecisionResult{}, projectionErr
	}
	result.Projection = projected
	if err := w.lifecycle.commitLifecycle(token, projected.State); err != nil {
		return DecisionResult{}, err
	}
	if projectionErr != nil {
		return DecisionResult{}, projectionErr
	}
	if command.Action == ActionApprove {
		result.Authorization = newAuthorization(request, confirmed.DecisionID, command.CurrentPlan, command.CurrentContext)
	}
	if command.Action == ActionRegenerate {
		result.Generation = &GenerationRequest{RunID: run.ID, Revision: projection.Revision}
	}
	if command.Action == ActionEditConstraints {
		result.Edit = &EditRequest{RunID: run.ID, Revision: projection.Revision}
	}
	_ = replayed
	return result, nil
}

// replayDecision reconstructs an idempotent outcome without appending a duplicate event.
func (w *ApprovalWorkflow) replayDecision(ctx context.Context, event ApprovalEvent, command DecisionCommand) (DecisionResult, error) {
	events, err := w.store.Events(ctx, event.RunID)
	if err != nil {
		return DecisionResult{}, err
	}
	projection, err := Project(ctx, events, w.limits.MaxEvents)
	if err != nil {
		return DecisionResult{}, err
	}
	result := DecisionResult{Projection: projection}
	request, err := w.store.Request(ctx, event.RunID, event.Revision)
	if err != nil {
		return DecisionResult{}, err
	}
	if err := w.validateRequest(ctx, projection, request); err != nil {
		return DecisionResult{}, err
	}
	switch event.Type {
	case EventApproved:
		planHash, planErr := fingerprintPlan(ctx, command.CurrentPlan, w.limits)
		if planErr != nil {
			return DecisionResult{}, planErr
		}
		contextHashes, contextErr := fingerprintContext(ctx, command.CurrentContext, w.limits)
		if contextErr != nil {
			return DecisionResult{}, contextErr
		}
		if planHash != request.planHash || contextHashes.All != request.contextHashes.All {
			return DecisionResult{}, typedError(CategoryStaleApproval, "replay", "snapshot", nil)
		}
		result.Authorization = newAuthorization(request, event.DecisionID, command.CurrentPlan, command.CurrentContext)
	case EventInvalidated:
		if event.InvalidationReason == ReasonRegenerationRequested {
			result.Generation = &GenerationRequest{RunID: event.RunID, Revision: event.Revision}
		}
		if event.InvalidationReason == ReasonConstraintsEdited {
			result.Edit = &EditRequest{RunID: event.RunID, Revision: event.Revision}
		}
	}
	return result, nil
}

// sameProjectionVersion revalidates the complete approval binding observed before an operation reserved the lifecycle.
func sameProjectionVersion(left, right Projection) bool {
	return left.Sequence == right.Sequence && left.Revision == right.Revision && left.State == right.State && left.RunID == right.RunID && left.PlanID == right.PlanID && left.PlanHash == right.PlanHash && left.ContextHash == right.ContextHash && left.ViewHash == right.ViewHash && left.DecisionID == right.DecisionID && left.Consumed == right.Consumed
}

// projectConfirmed reconstructs the authoritative prefix after append and recovers lifecycle state before reporting a malformed adapter response.
func (w *ApprovalWorkflow) projectConfirmed(ctx context.Context, runID string, prefix []ApprovalEvent, confirmed ApprovalEvent) (Projection, error) {
	projection, err := Project(ctx, append(prefix, confirmed), w.limits.MaxEvents)
	if err == nil {
		return projection, nil
	}
	events, loadErr := w.store.Events(ctx, runID)
	if loadErr != nil {
		return Projection{}, err
	}
	recovered, recoverErr := Project(ctx, events, w.limits.MaxEvents)
	if recoverErr != nil {
		return Projection{}, err
	}
	return recovered, err
}

// validateRequest authenticates recovered safe presentation material against the confirmed request event.
func (w *ApprovalWorkflow) validateRequest(ctx context.Context, projection Projection, request approvalRequest) error {
	if request.runID != projection.RunID || request.planID != projection.PlanID || request.planHash != projection.PlanHash || request.contextHashes.All != projection.ContextHash || request.viewHash != projection.ViewHash {
		return typedError(CategoryIntegrityFailure, "request", "binding", nil)
	}
	hash, err := canonicalHash(ctx, request.view, w.limits, "fingerprint_view")
	if err != nil {
		return err
	}
	if hash != request.viewHash {
		return typedError(CategoryIntegrityFailure, "request", "view", nil)
	}
	return nil
}

// validateDecision rejects incomplete identity, routing or enum values before journal access.
func validateDecision(command DecisionCommand) error {
	if strings.TrimSpace(command.Actor.Subject) == "" || strings.TrimSpace(command.Actor.Source) == "" {
		return typedError(CategoryUnauthorizedIdentity, "decide", "actor", nil)
	}
	if command.PlanID == "" || command.DecisionID == "" || command.ExpectedRevision == 0 {
		return typedError(CategoryInvalidInput, "decide", "command", nil)
	}
	switch command.Action {
	case ActionApprove, ActionRejectTerminal, ActionRejectReplan, ActionRegenerate, ActionEditConstraints:
		return nil
	}
	return typedError(CategoryInvalidInput, "decide", "action", nil)
}

// provenanceInput projects approval context back into the exact callback-free inputs sealed by feature 10.
func provenanceInput(snapshot ContractSnapshot) agentplanning.ProvenanceInput {
	capabilities := make([]agentplanning.CapabilityDescriptor, len(snapshot.Capabilities))
	for i, capability := range snapshot.Capabilities {
		capabilities[i] = agentplanning.CapabilityDescriptor{Name: capability.Name, Class: capability.Class, Description: capability.Description, Effects: capability.Effects, Idempotent: capability.Idempotent}
	}
	return agentplanning.ProvenanceInput{Constraints: append([]agentplanning.Constraint(nil), snapshot.Constraints...), Contract: snapshot.Contract, Capabilities: capabilities, Identities: testidentity.CloneDescriptors(snapshot.Identities)}
}

// transitionRun keeps the mutable harness aggregate aligned with the event-derived approval projection.
func transitionRun(run *harness.Run, state harness.Status) error {
	if run == nil {
		return typedError(CategoryInvalidInput, "transition", "run", nil)
	}
	if run.Status() == state {
		return nil
	}
	if err := run.Transition(state); err != nil {
		return typedError(CategoryIntegrityFailure, "transition", "run", nil)
	}
	return nil
}

// eventID derives a stable non-secret journal identifier from run, revision and event kind.
func eventID(runID string, revision uint64, eventType EventType) string {
	return fmt.Sprintf("%s:%d:%s", runID, revision, eventType)
}

// utc normalizes controlled clock values for stable RFC3339Nano JSON representation.
func utc(value time.Time) time.Time { return value.UTC() }
