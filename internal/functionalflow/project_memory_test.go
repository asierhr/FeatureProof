package functionalflow

import (
	"context"

	"github.com/asierhr/featureProof/internal/projectmemory"
)

// recordingProjectMemoryObserver captures the trusted observation emitted after package verification.
type recordingProjectMemoryObserver struct {
	input projectmemory.ObservationInput
}

// Observe records the separated memory mutation without changing the functional package result.
func (observer *recordingProjectMemoryObserver) Observe(_ context.Context, input projectmemory.ObservationInput) (projectmemory.ReviewView, error) {
	observer.input = input
	return projectmemory.ReviewView{State: projectmemory.StateObserved}, nil
}
