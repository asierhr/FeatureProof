package agentplanning

import "context"

// StaticPolicy is a deterministic exact-capability allowlist for composition and hermetic tests.
type StaticPolicy struct{ Decisions map[string]PolicyDecision }

// Evaluate applies default deny when no exact capability decision exists.
func (p StaticPolicy) Evaluate(ctx context.Context, intent PlanIntent) (PolicyDecision, error) {
	if err := ctx.Err(); err != nil {
		return PolicyDecision{}, err
	}
	decision, ok := p.Decisions[intent.Capability]
	if !ok {
		return PolicyDecision{Allowed: false, Reason: "default_deny"}, nil
	}
	return decision, nil
}
