package tenantsecurity

import (
	"context"
	"fmt"
	"time"
)

// SensitiveMutation changes only an ownership-safe candidate snapshot; it must not perform I/O.
type SensitiveMutation func(*Snapshot) error

// SensitiveResult binds a security mutation to its immutable audit link and durable receipt.
type SensitiveResult struct {
	Receipt StoreReceipt
	Audit   AuditEvent
}

// SensitiveAction describes one authorized, idempotent mutation and its allowlisted audit identity.
type SensitiveAction struct {
	Capability    Capability
	OperationID   string
	CommandDigest string
	Outcome       string
	Reason        Category
	ResourceID    string
}

// SensitiveExecutor commits mutation, audit and receipt through one durable CAS boundary.
type SensitiveExecutor struct {
	store DurableStore
	clock Clock
}

// NewSensitiveExecutor validates the durable state and deterministic clock dependencies.
func NewSensitiveExecutor(store DurableStore, clock Clock) (*SensitiveExecutor, error) {
	if store == nil || clock == nil {
		return nil, failure(CategoryInvalid, "sensitive_new", nil)
	}
	return &SensitiveExecutor{store: store, clock: clock}, nil
}

// Apply resolves retries by receipt and never invokes the mutation after an unknown successful commit.
func (executor *SensitiveExecutor) Apply(ctx context.Context, action SensitiveAction, mutate SensitiveMutation) (SensitiveResult, error) {
	if err := ctx.Err(); err != nil {
		return SensitiveResult{}, classifyContext("sensitive_apply", err)
	}
	if mutate == nil || ValidateID(action.OperationID) != nil || !validDigest(action.CommandDigest) || action.Outcome != "committed" && action.Outcome != "allowed" {
		return SensitiveResult{}, failure(CategoryInvalid, "sensitive_apply", nil)
	}
	if receipt, err := executor.store.Receipt(ctx, action.Capability.Scope.OrganizationID, action.OperationID); err == nil {
		if receipt.CommandDigest != action.CommandDigest {
			return SensitiveResult{}, failure(CategoryConflict, "sensitive_replay", nil)
		}
		snapshot, loadErr := executor.store.Snapshot(ctx)
		return SensitiveResult{Receipt: receipt, Audit: auditByOperation(snapshot, action.Capability.Scope.OrganizationID, action.OperationID)}, loadErr
	} else if !IsCategory(err, CategoryNotFoundHidden) {
		return SensitiveResult{}, err
	}
	snapshot, err := executor.store.Snapshot(ctx)
	if err != nil {
		return SensitiveResult{}, err
	}
	if err = verifyDurableCapability(snapshot, action.Capability, action.Capability.Action, action.Capability.Resource, executor.clock.Now()); err != nil {
		return SensitiveResult{}, err
	}
	if err = mutate(&snapshot); err != nil {
		return SensitiveResult{}, err
	}
	event, err := appendSensitiveAudit(&snapshot, action, executor.clock.Now())
	if err != nil {
		return SensitiveResult{}, err
	}
	if snapshot.Epoch >= MaxDurableCounter {
		return SensitiveResult{}, failure(CategoryExhausted, "sensitive_apply", nil)
	}
	expectedEpoch := snapshot.Epoch
	snapshot.Epoch++
	receipt, err := executor.store.Commit(ctx, StoreCommand{OrganizationID: action.Capability.Scope.OrganizationID, OperationID: action.OperationID, CommandDigest: action.CommandDigest, ExpectedEpoch: expectedEpoch, Snapshot: snapshot})
	if err != nil {
		if resolved, resolveErr := executor.store.Receipt(ctx, action.Capability.Scope.OrganizationID, action.OperationID); resolveErr == nil && resolved.CommandDigest == action.CommandDigest {
			return SensitiveResult{Receipt: resolved, Audit: event}, nil
		}
		return SensitiveResult{}, err
	}
	return SensitiveResult{Receipt: receipt, Audit: event}, nil
}

// RecordDenied persists a denial before returning unauthorized and reports a closed operational failure when audit cannot be confirmed.
func (executor *SensitiveExecutor) RecordDenied(ctx context.Context, capability Capability, operationID, resourceID string, reason Category) error {
	if ValidateID(operationID) != nil || !validDigest(capability.Digest) {
		return failure(CategoryInvalid, "sensitive_deny", nil)
	}
	snapshot, err := executor.store.Snapshot(ctx)
	if err != nil {
		return failure(CategoryUnavailable, "sensitive_deny_audit", err)
	}
	if snapshot.Epoch >= MaxDurableCounter {
		return failure(CategoryExhausted, "sensitive_deny_audit", nil)
	}
	action := SensitiveAction{Capability: capability, OperationID: operationID, CommandDigest: digestStrings("deny", capability.Digest, operationID, string(reason)), Outcome: "denied", Reason: reason, ResourceID: resourceID}
	if _, err = appendSensitiveAudit(&snapshot, action, executor.clock.Now()); err != nil {
		return failure(CategoryUnavailable, "sensitive_deny_audit", err)
	}
	expectedEpoch := snapshot.Epoch
	snapshot.Epoch++
	if _, err = executor.store.Commit(ctx, StoreCommand{OrganizationID: capability.Scope.OrganizationID, OperationID: operationID, CommandDigest: action.CommandDigest, ExpectedEpoch: expectedEpoch, Snapshot: snapshot}); err != nil {
		if receipt, resolveErr := executor.store.Receipt(ctx, capability.Scope.OrganizationID, operationID); resolveErr != nil || receipt.CommandDigest != action.CommandDigest {
			return failure(CategoryUnavailable, "sensitive_deny_audit", err)
		}
	}
	if reason != CategoryUnauthenticated && reason != CategoryUnauthorized && reason != CategoryNotFoundHidden {
		reason = CategoryUnauthorized
	}
	return failure(reason, "sensitive_deny", nil)
}

// appendSensitiveAudit seals tenant, actor, policy, operation and resource pseudonym into one chain link.
func appendSensitiveAudit(snapshot *Snapshot, action SensitiveAction, at time.Time) (AuditEvent, error) {
	events := snapshot.Audits[action.Capability.Scope.OrganizationID]
	if uint64(len(events)) >= MaxDurableCounter {
		return AuditEvent{}, failure(CategoryExhausted, "sensitive_audit", nil)
	}
	previous := ""
	if len(events) > 0 {
		previous = events[len(events)-1].Digest
	}
	event := AuditEvent{OrganizationID: action.Capability.Scope.OrganizationID, ProjectID: action.Capability.Scope.ProjectID, Sequence: uint64(len(events) + 1), ActorDigest: digestStrings(action.Capability.Scope.PrincipalID), Action: action.Capability.Action, Resource: action.Capability.Resource, ResourceDigest: digestStrings(action.ResourceID), OperationID: action.OperationID, Outcome: action.Outcome, Reason: action.Reason, PolicyDigest: action.Capability.Scope.PolicyDigest, PreviousDigest: previous, At: at.UTC()}
	event.Digest = auditDigest(event)
	snapshot.Audits[event.OrganizationID] = append(events, event)
	snapshot.AuditHeads[event.OrganizationID] = sealAuditHead(event.OrganizationID, event.Sequence, event.Digest)
	return event, nil
}

// sealAuditHead binds the terminal sequence and digest so tail deletion is detectable.
func sealAuditHead(organizationID string, sequence uint64, lastDigest string) AuditHead {
	head := AuditHead{OrganizationID: organizationID, LastSequence: sequence, LastDigest: lastDigest, Version: sequence}
	head.Digest = digestStrings(head.OrganizationID, fmt.Sprint(head.LastSequence), head.LastDigest, fmt.Sprint(head.Version))
	return head
}

// auditByOperation finds only an exact organization-scoped idempotent audit result.
func auditByOperation(snapshot Snapshot, organizationID, operationID string) AuditEvent {
	for index := len(snapshot.Audits[organizationID]) - 1; index >= 0; index-- {
		if snapshot.Audits[organizationID][index].OperationID == operationID {
			return snapshot.Audits[organizationID][index]
		}
	}
	return AuditEvent{}
}

// SensitiveCommandDigest seals an action's public metadata without accepting plaintext payloads.
func SensitiveCommandDigest(kind string, expectedVersion uint64, values ...string) string {
	return digestStrings(append([]string{kind, fmt.Sprint(expectedVersion)}, values...)...)
}
