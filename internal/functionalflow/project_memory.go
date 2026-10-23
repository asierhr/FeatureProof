package functionalflow

import (
	"context"

	"github.com/asierhr/featureProof/internal/projectmemory"
)

// ProjectMemoryObserver is the functional-flow-owned port that accepts only verified terminal package lineage.
type ProjectMemoryObserver interface {
	Observe(context.Context, projectmemory.ObservationInput) (projectmemory.ReviewView, error)
}

// ObserveProjectMemory verifies a terminal package, derives evidence lineage, and submits a separate observed-memory mutation.
func ObserveProjectMemory(ctx context.Context, observer ProjectMemoryObserver, pkg Package, input projectmemory.ObservationInput) (projectmemory.ReviewView, error) {
	if observer == nil || VerifyPackage(pkg) != nil || pkg.Primary.Outcome != OutcomePassed && pkg.Primary.Outcome != OutcomeFailed {
		return projectmemory.ReviewView{}, flowError(CategoryIntegrity, "memory_observation", nil)
	}
	input.FunctionalResultDigest = digestValue(pkg.Primary)
	input.Candidate.Evidence = make([]projectmemory.EvidenceRef, len(pkg.Evidence))
	for index, evidence := range pkg.Evidence {
		input.Candidate.Evidence[index] = projectmemory.EvidenceRef{ID: evidence.ID, Digest: evidence.RecordDigest, RunID: pkg.RunID, Project: input.Candidate.ProjectID}
	}
	return observer.Observe(ctx, input)
}
