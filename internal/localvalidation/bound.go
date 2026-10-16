package localvalidation

import (
	"context"

	"github.com/asierhr/featureProof/internal/evidence"
	"github.com/asierhr/featureProof/internal/harness"
	"github.com/asierhr/featureProof/internal/revisionbinding"
)

// boundJournal assigns the immutable digest to every event before confirmation.
type boundJournal struct {
	inner         harness.Journal
	runID, digest string
}

// Append rejects an event from a different run or with a conflicting binding.
func (j boundJournal) Append(ctx context.Context, event harness.Event) error {
	if event.RunID != j.runID || (event.BindingSHA256 != "" && event.BindingSHA256 != j.digest) {
		return &revisionbinding.Error{Category: revisionbinding.BindingIntegrity, Phase: "journal"}
	}
	event.BindingSHA256 = j.digest
	return j.inner.Append(ctx, event)
}

// Events verifies the confirmed sequence before handing it to publication.
func (j boundJournal) Events(ctx context.Context, runID string) ([]harness.Event, error) {
	if runID != j.runID {
		return nil, &revisionbinding.Error{Category: revisionbinding.BindingIntegrity, Phase: "journal"}
	}
	events, err := j.inner.Events(ctx, runID)
	if err != nil {
		return nil, err
	}
	for _, event := range events {
		if event.RunID != j.runID || event.BindingSHA256 != j.digest {
			return nil, &revisionbinding.Error{Category: revisionbinding.BindingIntegrity, Phase: "journal"}
		}
	}
	return events, nil
}

// boundEvidence seals the run binding with each sanitized record and checks reads.
type boundEvidence struct {
	inner         evidence.Store
	runID, digest string
}

// Save refuses foreign records and confirms a treated, binding-sealed copy.
func (s boundEvidence) Save(ctx context.Context, record evidence.Record) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	bound, err := evidence.BindRecord(record, s.runID, s.digest)
	if err != nil {
		return &revisionbinding.Error{Category: revisionbinding.BindingIntegrity, Phase: "evidence"}
	}
	return s.inner.Save(ctx, bound)
}

// Get prevents a foreign or unbound observation from affecting later steps.
func (s boundEvidence) Get(ctx context.Context, id string) (evidence.Record, error) {
	record, err := s.inner.Get(ctx, id)
	if err != nil {
		return evidence.Record{}, err
	}
	if record.RunID != s.runID || record.BindingSHA256 != s.digest {
		return evidence.Record{}, &revisionbinding.Error{Category: revisionbinding.BindingIntegrity, Phase: "evidence"}
	}
	return record, nil
}

// verifyBoundSequence rejects gaps, foreign origins, missing admission, and
// reordered or mismatched plan bindings before a bundle becomes visible.
func verifyBoundSequence(ctx context.Context, binding revisionbinding.Binding, plans []revisionbinding.PlanBinding, events []harness.Event, status ResultStatus) error {
	if err := binding.Verify(ctx); err != nil {
		return err
	}
	if len(events) < 2 || events[0].Type != harness.EventRunCreated || events[1].Type != harness.EventBindingConfirmed {
		return &revisionbinding.Error{Category: revisionbinding.BindingIntegrity, Phase: "events"}
	}
	planIndex := 0
	terminalSeen := false
	for n, plan := range plans {
		canonical, err := revisionbinding.NewPlanBinding(ctx, binding, n+1, plan.PlanSHA256, plan.Mode, true)
		if err != nil || canonical != plan {
			return &revisionbinding.Error{Category: revisionbinding.BindingIntegrity, Phase: "plans"}
		}
	}
	for i, event := range events {
		if err := ctx.Err(); err != nil {
			return err
		}
		if event.Sequence != i+1 || event.RunID != binding.RunID || event.BindingSHA256 != binding.BindingSHA256 {
			return &revisionbinding.Error{Category: revisionbinding.BindingIntegrity, Phase: "events"}
		}
		if terminalSeen {
			return &revisionbinding.Error{Category: revisionbinding.BindingIntegrity, Phase: "events"}
		}
		if i >= 2 && planIndex == 0 && event.Type != harness.EventPlanBindingConfirmed && !terminalBindingEvent(event.Type) {
			return &revisionbinding.Error{Category: revisionbinding.BindingIntegrity, Phase: "events"}
		}
		if event.Type == harness.EventPlanBindingConfirmed {
			if planIndex >= len(plans) || event.PlanSHA256 != plans[planIndex].PlanSHA256 || plans[planIndex].Batch != planIndex+1 || plans[planIndex].BindingSHA256 != binding.BindingSHA256 {
				return &revisionbinding.Error{Category: revisionbinding.BindingIntegrity, Phase: "plans"}
			}
			planIndex++
		}
		if isTerminalRunEvent(event.Type) {
			if i != len(events)-1 || event.Status != string(status) || !terminalMatchesStatus(event.Type, status) {
				return &revisionbinding.Error{Category: revisionbinding.BindingIntegrity, Phase: "events"}
			}
			terminalSeen = true
		}
	}
	if !terminalSeen || planIndex != len(plans) {
		return &revisionbinding.Error{Category: revisionbinding.BindingIntegrity, Phase: "plans"}
	}
	return nil
}

// isTerminalRunEvent identifies the one journal closure required by publication.
func isTerminalRunEvent(kind harness.EventType) bool {
	return kind == harness.EventRunCompleted || kind == harness.EventRunInconclusive || kind == harness.EventRunFailed || kind == harness.EventRunCancelled
}

// terminalMatchesStatus rejects functional verdicts that disagree with the
// event that closes the confirmed journal.
func terminalMatchesStatus(kind harness.EventType, status ResultStatus) bool {
	switch status {
	case ResultPassed, ResultFailed:
		return kind == harness.EventRunCompleted
	case ResultInconclusive:
		return kind == harness.EventRunInconclusive
	case ResultError:
		return kind == harness.EventRunFailed
	case ResultCancelled:
		return kind == harness.EventRunCancelled
	default:
		return false
	}
}

// terminalBindingEvent permits an admitted run to close before any plan when
// preflight drift or cancellation prevents functional work.
func terminalBindingEvent(kind harness.EventType) bool {
	return kind == harness.EventRunInconclusive || kind == harness.EventRunFailed || kind == harness.EventRunCancelled
}
