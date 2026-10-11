package model

import (
	"context"
	"errors"
	"testing"

	"github.com/asierhr/featureProof/internal/harness"
)

func TestFakeModelIsProgrammableAndCapturesRequests(t *testing.T) {
	want := harness.ModelDecision{Version: 1, Kind: harness.DecisionComplete}
	f := &Fake{Decisions: []harness.ModelDecision{want}, Errors: []error{nil}}
	got, err := f.Decide(context.Background(), harness.ModelRequest{RunID: "run-1"})
	if err != nil || got.Kind != want.Kind || len(f.CapturedRequests()) != 1 {
		t.Fatalf("decision=%+v requests=%v err=%v", got, f.CapturedRequests(), err)
	}
	if _, err := f.Decide(context.Background(), harness.ModelRequest{}); err == nil {
		t.Fatal("empty program accepted")
	}
}

func TestFakeModelPropagatesCancellation(t *testing.T) {
	f := &Fake{Block: make(chan struct{}), Decisions: []harness.ModelDecision{{Version: 1}}}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := f.Decide(ctx, harness.ModelRequest{}); !errors.Is(err, context.Canceled) {
		t.Fatalf("got %v", err)
	}
}
