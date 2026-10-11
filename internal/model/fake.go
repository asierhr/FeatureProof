package model

import (
	"context"
	"fmt"
	"sync"

	"github.com/asierhr/featureProof/internal/harness"
)

// Fake is a programmable model adapter that keeps harness tests offline and deterministic.
type Fake struct {
	mu        sync.Mutex
	Decisions []harness.ModelDecision
	Errors    []error
	Requests  []harness.ModelRequest
	Block     <-chan struct{}
}

// Decide captures model context and returns the next programmed decision or failure.
func (f *Fake) Decide(ctx context.Context, r harness.ModelRequest) (harness.ModelDecision, error) {
	if f.Block != nil {
		select {
		case <-ctx.Done():
			return harness.ModelDecision{}, ctx.Err()
		case <-f.Block:
		}
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.Requests = append(f.Requests, r)
	if len(f.Decisions) == 0 {
		return harness.ModelDecision{}, fmt.Errorf("fake model has no decision")
	}
	d := f.Decisions[0]
	f.Decisions = f.Decisions[1:]
	var err error
	if len(f.Errors) > 0 {
		err = f.Errors[0]
		f.Errors = f.Errors[1:]
	}
	return d, err
}

// CapturedRequests returns an isolated history for assertions about model context.
func (f *Fake) CapturedRequests() []harness.ModelRequest {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]harness.ModelRequest(nil), f.Requests...)
}
