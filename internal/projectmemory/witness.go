package projectmemory

import (
	"context"
	"errors"
)

// ErrWitnessMissing reports that the external authority has never initialized the requested stream.
var ErrWitnessMissing = errors.New("monotonic witness stream missing")

// ErrWitnessConflict reports that compare-and-advance observed a different durable generation.
var ErrWitnessConflict = errors.New("monotonic witness compare conflict")

// WitnessState is the exact non-replayable stream head retained outside every LocalStore-managed file.
type WitnessState struct {
	ProjectID      string `json:"project_id"`
	Generation     uint64 `json:"generation"`
	Head           Head   `json:"head"`
	ReceiptsDigest string `json:"receipts_digest"`
	CommitDigest   string `json:"commit_digest,omitempty"`
}

// MonotonicWitness owns irreversible per-project freshness independently from local journals and projections.
type MonotonicWitness interface {
	// Read returns the exact durable state without deriving or repairing it from caller data.
	Read(context.Context, string) (WitnessState, error)
	// Bootstrap creates generation zero exactly once through an explicitly authorized operation.
	Bootstrap(context.Context, string) (WitnessState, error)
	// CompareAndAdvance atomically replaces the expected state with its single valid successor.
	CompareAndAdvance(context.Context, WitnessState, WitnessState) error
}

// witnessStateForEvents derives the exact local claim that an external witness must already hold.
func witnessStateForEvents(project string, events []EventEnvelope) WitnessState {
	state := WitnessState{ProjectID: project, ReceiptsDigest: digestCanonical(receiptsForEvents(project, events))}
	if len(events) > 0 {
		last := events[len(events)-1]
		state.Generation = uint64(len(events))
		state.Head = Head{Sequence: last.Sequence, Digest: last.EventDigest}
		state.CommitDigest = digestCanonical(last)
	}
	return state
}
