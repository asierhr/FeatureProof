package planapproval

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"strings"
	"time"

	"github.com/asierhr/featureProof/internal/agentplanning"
	"github.com/asierhr/featureProof/internal/harness"
	"github.com/asierhr/featureProof/internal/testidentity"
)

// Project reconstructs the only authoritative approval state from a strict confirmed event prefix.
func Project(ctx context.Context, events []ApprovalEvent, maxEvents int) (Projection, error) {
	if maxEvents <= 0 || len(events) > maxEvents {
		return Projection{}, typedError(CategoryLimitExceeded, "project", "events", nil)
	}
	projection := Projection{State: harness.StatusPlanning}
	for i, event := range events {
		if err := ctx.Err(); err != nil {
			return Projection{}, contextFailure("project", err)
		}
		if err := validateEventSchema(event); err != nil {
			return Projection{}, err
		}
		if event.Sequence != uint64(i+1) {
			return Projection{}, typedError(CategoryIntegrityFailure, "project", "event", nil)
		}
		if projection.RunID != "" && event.RunID != projection.RunID {
			return Projection{}, typedError(CategoryIntegrityFailure, "project", "run_id", nil)
		}
		switch event.Type {
		case EventApprovalRequested:
			if projection.State != harness.StatusPlanning || event.Revision != projection.Revision+1 {
				return Projection{}, typedError(CategoryIntegrityFailure, "project", "request", nil)
			}
			projection.State = harness.StatusAwaitingApproval
			projection.Consumed = false
			projection.Revision, projection.RunID, projection.PlanID = event.Revision, event.RunID, event.PlanID
			projection.PlanHash, projection.ContextHash, projection.ViewHash = event.PlanHash, event.ContextHash, event.ViewHash
		case EventApproved:
			if !matchesAwaiting(projection, event) {
				return Projection{}, typedError(CategoryIntegrityFailure, "project", "approval", nil)
			}
			projection.State, projection.DecisionID = harness.StatusExecuting, event.DecisionID
		case EventRejected:
			if !matchesAwaiting(projection, event) {
				return Projection{}, typedError(CategoryIntegrityFailure, "project", "rejection", nil)
			}
			if event.Disposition == "terminal" {
				projection.State = harness.StatusCancelled
			} else if event.Disposition == "replan" {
				projection.State = harness.StatusPlanning
			} else {
				return Projection{}, typedError(CategoryIntegrityFailure, "project", "disposition", nil)
			}
			projection.DecisionID = event.DecisionID
		case EventInvalidated:
			if !matchesAwaiting(projection, event) || event.InvalidationReason == "" {
				return Projection{}, typedError(CategoryIntegrityFailure, "project", "invalidation", nil)
			}
			projection.State, projection.DecisionID = harness.StatusPlanning, event.DecisionID
		case EventGrantConsumed:
			if projection.State != harness.StatusExecuting || projection.Consumed || event.Revision != projection.Revision || event.PlanHash != projection.PlanHash || event.ContextHash != projection.ContextHash || event.ViewHash != projection.ViewHash || event.DecisionID != "consume:"+projection.DecisionID {
				return Projection{}, typedError(CategoryIntegrityFailure, "project", "consumption", nil)
			}
			projection.Consumed = true
		case EventReplanReady:
			if projection.State != harness.StatusExecuting || !projection.Consumed || event.Revision != projection.Revision || event.PlanHash != projection.PlanHash || event.ContextHash != projection.ContextHash || event.ViewHash != projection.ViewHash {
				return Projection{}, typedError(CategoryIntegrityFailure, "project", "replan", nil)
			}
			projection.State = harness.StatusPlanning
		default:
			return Projection{}, typedError(CategoryIntegrityFailure, "project", "type", nil)
		}
		projection.Sequence = event.Sequence
		if projection.State == harness.StatusCancelled && i != len(events)-1 {
			return Projection{}, typedError(CategoryIntegrityFailure, "project", "terminal", nil)
		}
	}
	return projection, nil
}

// validateEventSchema enforces the complete closed field contract before an event can influence reconstructed state.
func validateEventSchema(event ApprovalEvent) error {
	if event.Version != 1 || event.Sequence == 0 || event.Revision == 0 || strings.TrimSpace(event.EventID) == "" || strings.TrimSpace(event.RunID) == "" || strings.TrimSpace(event.PlanID) == "" || event.PlanHash == "" || event.ContextHash == "" || event.ViewHash == "" || event.OccurredAt.IsZero() {
		return typedError(CategoryIntegrityFailure, "project", "event", nil)
	}
	if !validSHA256(event.PlanHash) || !validSHA256(event.ContextHash) || !validSHA256(event.ViewHash) {
		return typedError(CategoryIntegrityFailure, "project", "hash", nil)
	}
	if event.OccurredAt.Location() != time.UTC {
		return typedError(CategoryIntegrityFailure, "project", "occurred_at", nil)
	}
	if event.EventID != eventID(event.RunID, event.Revision, event.Type) {
		return typedError(CategoryIntegrityFailure, "project", "event_id", nil)
	}
	actorValid := event.Actor != nil && strings.TrimSpace(event.Actor.Subject) != "" && strings.TrimSpace(event.Actor.Source) != ""
	switch event.Type {
	case EventApprovalRequested:
		if event.DecisionID != "" || event.Actor != nil || event.Disposition != "" || event.InvalidationReason != "" || event.CommandFingerprint != "" {
			return typedError(CategoryIntegrityFailure, "project", "request_schema", nil)
		}
	case EventApproved:
		if event.DecisionID == "" || !actorValid || event.CommandFingerprint == "" || event.Disposition != "" || event.InvalidationReason != "" {
			return typedError(CategoryIntegrityFailure, "project", "approval_schema", nil)
		}
	case EventRejected:
		if event.DecisionID == "" || !actorValid || event.CommandFingerprint == "" || (event.Disposition != "terminal" && event.Disposition != "replan") || event.InvalidationReason != "" {
			return typedError(CategoryIntegrityFailure, "project", "rejection_schema", nil)
		}
	case EventInvalidated:
		if event.DecisionID == "" || !actorValid || event.CommandFingerprint == "" || event.Disposition != "" || !validInvalidationReason(event.InvalidationReason) {
			return typedError(CategoryIntegrityFailure, "project", "invalidation_schema", nil)
		}
	case EventGrantConsumed:
		if event.DecisionID == "" || event.Actor != nil || event.CommandFingerprint == "" || event.Disposition != "" || event.InvalidationReason != "" {
			return typedError(CategoryIntegrityFailure, "project", "consumption_schema", nil)
		}
	case EventReplanReady:
		if event.DecisionID == "" || event.Actor != nil || event.CommandFingerprint == "" || event.Disposition != "" || event.InvalidationReason != "" {
			return typedError(CategoryIntegrityFailure, "project", "replan_schema", nil)
		}
	default:
		return typedError(CategoryIntegrityFailure, "project", "type", nil)
	}
	return nil
}

// validSHA256 accepts only the lowercase 64-character hexadecimal encoding emitted by the canonical fingerprinter.
func validSHA256(value string) bool {
	if len(value) != sha256.Size*2 {
		return false
	}
	for _, character := range value {
		if (character < '0' || character > '9') && (character < 'a' || character > 'f') {
			return false
		}
	}
	return true
}

// validInvalidationReason limits recovery to the reason vocabulary specified by approval protocol v1.
func validInvalidationReason(reason InvalidationReason) bool {
	switch reason {
	case ReasonConstraintsEdited, ReasonRegenerationRequested, ReasonPlanChanged, ReasonTargetChanged, ReasonConstraintsChanged, ReasonContractChanged, ReasonCapabilitiesChanged, ReasonIdentitiesChanged, ReasonRuntimeChanged, ReasonGenerationContractChanged:
		return true
	default:
		return false
	}
}

// matchesAwaiting confirms that a decision event consumes exactly the current staged binding.
func matchesAwaiting(projection Projection, event ApprovalEvent) bool {
	return projection.State == harness.StatusAwaitingApproval && event.Revision == projection.Revision && event.RunID == projection.RunID && event.PlanID == projection.PlanID && event.PlanHash == projection.PlanHash && event.ContextHash == projection.ContextHash && event.ViewHash == projection.ViewHash && event.DecisionID != ""
}

// cloneContext round-trips the serializable contract so no caller-owned collection remains shared.
func cloneContext(snapshot ContractSnapshot) ContractSnapshot {
	out := snapshot
	out.Constraints = append([]agentplanning.Constraint(nil), snapshot.Constraints...)
	out.Capabilities = append([]CapabilitySnapshot(nil), snapshot.Capabilities...)
	out.Identities = testidentity.CloneDescriptors(snapshot.Identities)
	out.Runtime.Limits = make(map[string]int, len(snapshot.Runtime.Limits))
	for key, value := range snapshot.Runtime.Limits {
		out.Runtime.Limits[key] = value
	}
	return out
}

// cloneView copies every mutable collection returned to a presentation adapter.
func cloneView(view ApprovalView) ApprovalView {
	out := view
	out.Constraints = append([]ViewConstraint(nil), view.Constraints...)
	out.Checks = make([]ViewCheck, len(view.Checks))
	for i, check := range view.Checks {
		out.Checks[i] = check
		out.Checks[i].Evidence = make([]agentplanning.EvidenceRequirement, len(check.Evidence))
		for j, evidence := range check.Evidence {
			out.Checks[i].Evidence[j] = evidence
			out.Checks[i].Evidence[j].AssertionIDs = append([]string(nil), evidence.AssertionIDs...)
		}
		out.Checks[i].Steps = make([]ViewStep, len(check.Steps))
		for j, step := range check.Steps {
			out.Checks[i].Steps[j] = step
			out.Checks[i].Steps[j].DependsOn = append([]string(nil), step.DependsOn...)
			out.Checks[i].Steps[j].Input = cloneViewValue(step.Input)
			out.Checks[i].Steps[j].Assertions = cloneViewValue(step.Assertions)
		}
	}
	return out
}

// cloneViewValue isolates sanitized JSON-compatible presentation values from caller mutation.
func cloneViewValue(value any) any {
	raw, err := json.Marshal(value)
	if err != nil {
		return nil
	}
	var out any
	if json.Unmarshal(raw, &out) != nil {
		return nil
	}
	return out
}
