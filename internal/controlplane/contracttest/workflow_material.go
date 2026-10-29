package contracttest

import (
	"context"
	"fmt"
	"reflect"

	"github.com/asierhr/featureProof/internal/agentplanning"
	"github.com/asierhr/featureProof/internal/localvalidation"
	"github.com/asierhr/featureProof/internal/validationworkflow"
)

// WorkflowMaterialFactory reopens the immutable public and private workflow
// material ports over the same durable backend.
type WorkflowMaterialFactory func() (validationworkflow.PlanArtifactStore, validationworkflow.ExecutablePlanStore, error)

// RunWorkflowMaterialStores verifies byte-stable public artifact publication,
// conflict rejection, private material restart and idempotent retirement.
func RunWorkflowMaterialStores(ctx context.Context, factory WorkflowMaterialFactory, handle string) error {
	if factory == nil || handle == "" {
		return fmt.Errorf("workflow material contract configuration")
	}
	artifacts, executables, err := factory()
	if err != nil {
		return fmt.Errorf("open initial: %w", err)
	}
	artifact := contractPlanArtifact(handle, "plan")
	if err = artifacts.Commit(ctx, validationworkflow.PlanArtifact{WorkflowID: handle}); err == nil {
		return fmt.Errorf("invalid artifact accepted")
	}
	if err = artifacts.Commit(ctx, artifact); err != nil {
		return fmt.Errorf("artifact commit: %w", err)
	}
	if err = artifacts.Commit(ctx, artifact); err != nil {
		return fmt.Errorf("artifact redelivery: %w", err)
	}
	if err = artifacts.Commit(ctx, contractPlanArtifact(handle, "other-plan")); err == nil {
		return fmt.Errorf("conflicting artifact accepted")
	}
	material := validationworkflow.ExecutableMaterial{Version: validationworkflow.ArtifactVersion, WorkflowID: handle, ArtifactSHA256: artifact.ArtifactSHA256, ContextDigest: contractDigest("context"), RevisionDigest: contractDigest("revision"), TargetDigest: contractDigest("target"), Snapshot: agentplanning.PreparedSnapshot{ID: "plan", Checks: []agentplanning.CandidateCheck{{}}}, CommitmentKey: []byte("0123456789abcdef0123456789abcdef")}
	if err = executables.Commit(ctx, handle, material); err != nil {
		return fmt.Errorf("executable commit: %w", err)
	}
	conflictingMaterial := material
	conflictingMaterial.CommitmentKey = []byte("abcdef0123456789abcdef0123456789")
	if err = executables.Commit(ctx, handle, conflictingMaterial); err == nil {
		return fmt.Errorf("conflicting executable accepted")
	}
	reopenedArtifacts, reopenedExecutables, err := factory()
	if err != nil {
		return fmt.Errorf("reopen: %w", err)
	}
	loadedArtifact, err := reopenedArtifacts.Load(ctx, handle)
	if err != nil || !reflect.DeepEqual(loadedArtifact, artifact) {
		return fmt.Errorf("artifact reload: %w", err)
	}
	loadedMaterial, err := reopenedExecutables.Load(ctx, handle)
	if err != nil || !reflect.DeepEqual(loadedMaterial, material) {
		return fmt.Errorf("executable reload: %w", err)
	}
	loadedMaterial.CommitmentKey[0] ^= 0xff
	again, err := reopenedExecutables.Load(ctx, handle)
	if err != nil || reflect.DeepEqual(again.CommitmentKey, loadedMaterial.CommitmentKey) {
		return fmt.Errorf("executable key alias: %w", err)
	}
	if err = reopenedExecutables.Delete(ctx, handle); err != nil {
		return fmt.Errorf("executable delete: %w", err)
	}
	if err = reopenedExecutables.Delete(ctx, handle); err != nil {
		return fmt.Errorf("executable delete redelivery: %w", err)
	}
	if _, err = reopenedExecutables.Load(ctx, handle); err == nil {
		return fmt.Errorf("deleted executable remained visible")
	}
	return nil
}

// contractPlanArtifact seals the minimal valid public artifact using the
// consumer's canonical self-excluding digest contract.
func contractPlanArtifact(handle, planID string) validationworkflow.PlanArtifact {
	value := validationworkflow.PlanArtifact{Version: validationworkflow.ArtifactVersion, WorkflowID: handle, PlanID: planID, Plans: []localvalidation.PlanProjection{{ID: planID}}, ContextInstructionsHash: contractDigest("instructions")}
	sealed, _ := validationworkflow.SealPlanArtifactForStore(value)
	return sealed
}
