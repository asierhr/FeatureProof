package policy

import (
	"context"
	"github.com/asierhr/featureProof/internal/harness"
)

// Static is a deterministic allowlist policy used to exercise the authorization boundary.
type Static struct {
	Decisions  map[string]harness.PolicyDecision
	Calls      []harness.ToolCall
	OnEvaluate func()
}

// Evaluate records the call and denies capabilities without an explicit decision.
func (p *Static) Evaluate(ctx context.Context, _ *harness.Run, c harness.ToolCall) (harness.PolicyDecision, error) {
	if err := ctx.Err(); err != nil {
		return harness.PolicyDecision{}, err
	}
	if p.OnEvaluate != nil {
		p.OnEvaluate()
	}
	p.Calls = append(p.Calls, c)
	d, ok := p.Decisions[c.Tool]
	if !ok {
		return harness.PolicyDecision{Allowed: false, Reason: "default_deny"}, nil
	}
	return d, nil
}
