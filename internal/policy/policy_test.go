package policy

import (
	"context"
	"errors"
	"testing"

	"github.com/asierhr/featureProof/internal/harness"
)

func TestStaticPolicyAllowsConfiguredAndDeniesUnknown(t *testing.T) {
	called := false
	p := &Static{Decisions: map[string]harness.PolicyDecision{"probe": {Allowed: true}}, OnEvaluate: func() { called = true }}
	r, _ := harness.NewRun("run", "goal", "constraint", harness.Limits{})
	d, err := p.Evaluate(context.Background(), r, harness.ToolCall{Tool: "probe"})
	if err != nil || !d.Allowed || !called {
		t.Fatalf("decision=%+v err=%v", d, err)
	}
	d, err = p.Evaluate(context.Background(), r, harness.ToolCall{Tool: "missing"})
	if err != nil || d.Allowed || d.Reason != "default_deny" {
		t.Fatalf("decision=%+v err=%v", d, err)
	}
}

func TestStaticPolicyObservesCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := (&Static{}).Evaluate(ctx, nil, harness.ToolCall{}); !errors.Is(err, context.Canceled) {
		t.Fatalf("got %v", err)
	}
}
