package contracttest

import (
	"context"
	"fmt"
	"time"

	"github.com/asierhr/featureProof/internal/environment"
)

// RunCapacityAdmission verifies reservation redelivery, quota exclusion,
// immutable provider binding, release proof and capacity reclamation through
// the consumer-owned CapacityAdmission port.
func RunCapacityAdmission(ctx context.Context, admission environment.CapacityAdmission, projectID, firstRunID, secondRunID string) error {
	return RunCapacityAdmissionBound(ctx, admission, projectID, firstRunID, secondRunID, func(operation context.Context, _ string) context.Context { return operation })
}

// RunCapacityAdmissionBound runs the shared contract while composition binds
// each independent run to its immutable backend coordinate before mutation.
func RunCapacityAdmissionBound(ctx context.Context, admission environment.CapacityAdmission, projectID, firstRunID, secondRunID string, bind func(context.Context, string) context.Context) error {
	if admission == nil || projectID == "" || firstRunID == "" || secondRunID == "" || bind == nil {
		return fmt.Errorf("capacity contract configuration")
	}
	firstContext := bind(ctx, firstRunID)
	secondContext := bind(ctx, secondRunID)
	demand := environment.ResourceDemand{Environments: 1, CPUMilli: 100, MemoryBytes: 100, ObjectCount: 10}
	request := environment.CapacityRequest{ProjectID: projectID, RunID: firstRunID, AcquisitionKey: "contract-acquisition-a", SelectionDigest: contractDigest("selection"), Demand: demand, TTL: time.Hour, PolicyDigest: contractDigest("policy")}
	receipt, err := admission.Reserve(firstContext, request)
	if err != nil {
		return fmt.Errorf("reserve: %w", err)
	}
	redelivered, err := admission.Reserve(firstContext, request)
	if err != nil || redelivered != receipt {
		return fmt.Errorf("reserve redelivery: %w", err)
	}
	second := request
	second.RunID, second.AcquisitionKey = secondRunID, "contract-acquisition-b"
	if _, err = admission.Reserve(secondContext, second); err == nil {
		return fmt.Errorf("capacity limit accepted")
	}
	environmentID, resourceID := contractDigest("environment"), contractDigest("resource")
	if _, err = admission.Confirm(firstContext, receipt, "invalid", resourceID); !environment.IsCategory(err, environment.CategoryIntegrity) {
		return fmt.Errorf("invalid provider identity accepted: %w", err)
	}
	confirmed, err := admission.Confirm(firstContext, receipt, environmentID, resourceID)
	if err != nil || confirmed != receipt {
		return fmt.Errorf("confirm: %w", err)
	}
	if _, err = admission.Confirm(firstContext, receipt, contractDigest("other-environment"), resourceID); err == nil {
		return fmt.Errorf("provider identity rebound")
	}
	confirmation := environment.CapacityReleaseConfirmation("contract-cleanup", environmentID, resourceID, receipt.ID)
	if err = admission.Release(firstContext, receipt, "contract-cleanup", "invalid"); !environment.IsCategory(err, environment.CategoryIntegrity) {
		return fmt.Errorf("invalid release proof accepted: %w", err)
	}
	if err = admission.Release(firstContext, receipt, "contract-cleanup", confirmation); err != nil {
		return fmt.Errorf("release: %w", err)
	}
	if err = admission.Release(firstContext, receipt, "contract-cleanup", confirmation); err != nil {
		return fmt.Errorf("release redelivery: %w", err)
	}
	if _, err = admission.Reserve(secondContext, second); err != nil {
		return fmt.Errorf("capacity not reclaimed: %w", err)
	}
	return nil
}
