package tenantsecurity

import (
	"context"
	"fmt"
	"slices"
	"time"
)

const (
	// DeletionPlanned identifies durable work that has not crossed the consumer boundary.
	DeletionPlanned = "planned"
	// DeletionDeleting identifies a fenced attempt that may invoke its owning consumer.
	DeletionDeleting = "deleting"
	// DeletionUncertain retains an intent whose external delete outcome requires observation.
	DeletionUncertain = "uncertain"
	// DeletionTombstone preserves minimal proof that consumer absence was confirmed.
	DeletionTombstone = "tombstoned"
)

// DeletionCommand gives a consumer only its scoped identity, immutable candidate digest and fence.
type DeletionCommand struct {
	Key             ScopedKey
	CandidateDigest string
	DeleteDigest    string
	Fence           uint64
}

// DeletionObservation is the consumer-owned live-reference and existence check immediately before delete.
type DeletionObservation struct {
	Exists         bool
	LiveReferences uint64
	Digest         string
}

// RetentionConsumer owns observation and irreversible deletion for its resource type.
type RetentionConsumer interface {
	Observe(context.Context, DeletionCommand) (DeletionObservation, error)
	Delete(context.Context, DeletionCommand) (EffectStatus, error)
}

// DeletionPlan binds a durable intent to the exact eligibility snapshot approved by policy.
type DeletionPlan struct {
	Capability  Capability
	OperationID string
	Eligibility EligibilitySnapshot
	Resource    ResourceClass
}

// RetentionController converges durable intents without placing network callbacks inside transactions.
type RetentionController struct {
	executor  *SensitiveExecutor
	store     DurableStore
	clock     Clock
	consumers map[ResourceClass]RetentionConsumer
}

// NewRetentionController validates the complete consumer-owned deletion registry at composition.
func NewRetentionController(store DurableStore, clock Clock, consumers map[ResourceClass]RetentionConsumer) (*RetentionController, error) {
	executor, err := NewSensitiveExecutor(store, clock)
	if err != nil || consumers == nil {
		return nil, failure(CategoryInvalid, "retention_controller_new", err)
	}
	owned := make(map[ResourceClass]RetentionConsumer, len(consumers))
	for resource, consumer := range consumers {
		if !validResource(resource) || consumer == nil {
			return nil, failure(CategoryInvalid, "retention_controller_new", nil)
		}
		owned[resource] = consumer
	}
	return &RetentionController{executor: executor, store: store, clock: clock, consumers: owned}, nil
}

// Plan persists an intent before any consumer observes or deletes the candidate.
func (controller *RetentionController) Plan(ctx context.Context, plan DeletionPlan) (DeletionIntent, error) {
	if controller == nil || ValidateID(plan.OperationID) != nil || ValidateEligibilitySnapshot(plan.Eligibility) != nil || plan.Capability.Action != ActionRetentionExecute || plan.Capability.Resource != plan.Resource || retentionResource(plan.Eligibility.CandidateClass) != plan.Resource {
		return DeletionIntent{}, failure(CategoryInvalid, "deletion_plan_durable", nil)
	}
	commandDigest := digestStrings("deletion-plan", plan.Capability.Digest, plan.OperationID, plan.Eligibility.Digest, string(plan.Resource))
	action := SensitiveAction{Capability: plan.Capability, OperationID: plan.OperationID, CommandDigest: commandDigest, Outcome: "committed", ResourceID: plan.Eligibility.CandidateID}
	for attempt := 0; attempt < MaxItems; attempt++ {
		_, err := controller.executor.Apply(ctx, action, func(snapshot *Snapshot) error {
			key := deletionKey(plan.Capability.Scope, plan.Eligibility.CandidateID)
			if existing, ok := snapshot.Deletions[key]; ok {
				if existing.EligibilityDigest != plan.Eligibility.Digest || existing.Resource != plan.Resource {
					return failure(CategoryConflict, "deletion_plan_durable", nil)
				}
				return nil
			}
			now := controller.clock.Now().UTC()
			intent := DeletionIntent{ID: plan.Eligibility.CandidateID, Scope: plan.Capability.Scope, Resource: plan.Resource, State: DeletionPlanned, Fence: 1, PolicyVersion: plan.Eligibility.PolicyVersion, PolicyDigest: plan.Eligibility.PolicyDigest, EligibilityDigest: plan.Eligibility.Digest, CandidateDigest: plan.Eligibility.CandidateDigest, PlannedAt: now, UpdatedAt: now}
			intent.DeleteDigest = digestStrings(intent.Scope.OrganizationID, intent.Scope.ProjectID, intent.ID, string(intent.Resource), intent.CandidateDigest, fmt.Sprint(intent.Fence))
			intent.Digest = deletionIntentDigest(intent)
			snapshot.Deletions[key] = intent
			return nil
		})
		if err == nil {
			snapshot, loadErr := controller.store.Snapshot(ctx)
			if loadErr != nil {
				return DeletionIntent{}, loadErr
			}
			return snapshot.Deletions[deletionKey(plan.Capability.Scope, plan.Eligibility.CandidateID)], nil
		}
		if !IsCategory(err, CategoryStale) {
			return DeletionIntent{}, err
		}
	}
	return DeletionIntent{}, failure(CategoryUnavailable, "deletion_plan_durable", nil)
}

// Converge rechecks live references, fences one attempt, and records a tombstone only after confirmed absence.
func (controller *RetentionController) Converge(ctx context.Context, capability Capability, candidateID, operationPrefix string) error {
	if controller == nil || ValidateID(candidateID) != nil || ValidateID(operationPrefix) != nil || capability.Action != ActionRetentionExecute {
		return failure(CategoryInvalid, "deletion_converge", nil)
	}
	intent, err := controller.loadIntent(ctx, capability.Scope, candidateID)
	if err != nil {
		return err
	}
	if intent.State == DeletionTombstone {
		return nil
	}
	consumer := controller.consumers[intent.Resource]
	if consumer == nil || capability.Resource != intent.Resource {
		return failure(CategoryInvalid, "deletion_converge", nil)
	}
	if err = ctx.Err(); err != nil {
		return classifyContext("deletion_converge", err)
	}
	command, err := deletionCommand(capability, intent)
	if err != nil {
		return err
	}
	observation, err := consumer.Observe(ctx, command)
	if err != nil {
		return failure(CategoryUnavailable, "deletion_observe", err)
	}
	if observation.LiveReferences != 0 {
		return failure(CategoryConflict, "deletion_live_reference", nil)
	}
	if !observation.Exists {
		return controller.finish(ctx, capability, intent, operationPrefix+"-absent")
	}
	intent, err = controller.claim(ctx, capability, intent, operationPrefix+"-claim")
	if err != nil {
		return err
	}
	command, err = deletionCommand(capability, intent)
	if err != nil {
		return err
	}
	status, deleteErr := consumer.Delete(ctx, command)
	switch status {
	case EffectCommitted:
		return controller.finish(ctx, capability, intent, operationPrefix+"-finish")
	case EffectUnknown:
		if err = controller.setState(ctx, capability, intent, operationPrefix+"-unknown", DeletionUncertain); err != nil {
			return err
		}
		return deleteErr
	case EffectRejected:
		if err = controller.setState(ctx, capability, intent, operationPrefix+"-retry", DeletionPlanned); err != nil {
			return err
		}
		return deleteErr
	default:
		return failure(CategoryIntegrity, "deletion_delete", deleteErr)
	}
}

// ConvergeBatch enforces child-before-parent dependency order and a bounded graph traversal.
func (controller *RetentionController) ConvergeBatch(ctx context.Context, capability Capability, candidateIDs []string, operationPrefix string, limit uint64) error {
	if limit == 0 || limit > uint64(MaxItems) || uint64(len(candidateIDs)) > limit {
		return failure(CategoryInvalid, "deletion_batch", nil)
	}
	type entry struct {
		id     string
		intent DeletionIntent
	}
	entries := make([]entry, 0, len(candidateIDs))
	for _, id := range candidateIDs {
		intent, err := controller.loadIntent(ctx, capability.Scope, id)
		if err != nil {
			return err
		}
		entries = append(entries, entry{id: id, intent: intent})
	}
	slices.SortStableFunc(entries, func(left, right entry) int {
		return deletionRank(left.intent.Resource) - deletionRank(right.intent.Resource)
	})
	for index, entry := range entries {
		if err := controller.Converge(ctx, capability, entry.id, fmt.Sprintf("%s-%d", operationPrefix, index)); err != nil {
			return err
		}
	}
	return nil
}

// ConvergeGraph orders heterogeneous consumer deletes and selects the matching scoped capability per node.
func (controller *RetentionController) ConvergeGraph(ctx context.Context, capabilities map[ResourceClass]Capability, candidateIDs []string, operationPrefix string, limit uint64) error {
	if controller == nil || len(capabilities) == 0 || limit == 0 || limit > uint64(MaxItems) || uint64(len(candidateIDs)) > limit || ValidateID(operationPrefix) != nil {
		return failure(CategoryInvalid, "deletion_graph", nil)
	}
	var scope TenantScope
	for _, capability := range capabilities {
		if scope.OrganizationID == "" {
			scope = capability.Scope
		}
		if capability.Action != ActionRetentionExecute || capability.Scope.OrganizationID != scope.OrganizationID || capability.Scope.ProjectID != scope.ProjectID {
			return failure(CategoryUnauthorized, "deletion_graph", nil)
		}
	}
	type graphEntry struct {
		id     string
		intent DeletionIntent
	}
	entries := make([]graphEntry, 0, len(candidateIDs))
	for _, id := range candidateIDs {
		intent, err := controller.loadIntent(ctx, scope, id)
		if err != nil {
			return err
		}
		if _, ok := capabilities[intent.Resource]; !ok {
			return failure(CategoryUnauthorized, "deletion_graph", nil)
		}
		entries = append(entries, graphEntry{id: id, intent: intent})
	}
	slices.SortStableFunc(entries, func(left, right graphEntry) int {
		return deletionRank(left.intent.Resource) - deletionRank(right.intent.Resource)
	})
	for index, entry := range entries {
		if err := controller.Converge(ctx, capabilities[entry.intent.Resource], entry.id, fmt.Sprintf("%s-%d", operationPrefix, index)); err != nil {
			return err
		}
	}
	return nil
}

// BeginProjectDeletion blocks admission and revokes project-scoped service identities and secret leases atomically.
func (controller *RetentionController) BeginProjectDeletion(ctx context.Context, capability Capability, operationID string) error {
	if controller == nil || ValidateID(operationID) != nil || capability.Action != ActionRetentionExecute || capability.Resource != ResourceTenant {
		return failure(CategoryInvalid, "project_deletion_begin", nil)
	}
	action := SensitiveAction{Capability: capability, OperationID: operationID, CommandDigest: digestStrings("project-delete", capability.Digest, operationID), Outcome: "committed", ResourceID: capability.Scope.ProjectID}
	_, err := controller.executor.Apply(ctx, action, func(snapshot *Snapshot) error {
		projectKey := projectKey(capability.Scope.OrganizationID, capability.Scope.ProjectID)
		project, ok := snapshot.Projects[projectKey]
		if !ok {
			return failure(CategoryNotFoundHidden, "project_deletion_begin", nil)
		}
		project.State = StateSuspended
		project.Version++
		snapshot.Projects[projectKey] = project
		for key, membership := range snapshot.Memberships {
			if membership.OrganizationID == capability.Scope.OrganizationID && slices.Contains(membership.ProjectIDs, capability.Scope.ProjectID) {
				if identity, identityOK := snapshot.ServiceIdentities[principalKey(membership.OrganizationID, membership.PrincipalID)]; identityOK {
					identity.State = StateRevoked
					identity.Version++
					snapshot.ServiceIdentities[principalKey(identity.OrganizationID, identity.PrincipalID)] = identity
				}
				snapshot.Memberships[key] = membership
			}
		}
		for key, lease := range snapshot.SecretLeases {
			if lease.Scope.OrganizationID == capability.Scope.OrganizationID && lease.Scope.ProjectID == capability.Scope.ProjectID && !lease.Revoked {
				lease.Revoked = true
				lease.Digest = secretLeaseDigest(lease)
				snapshot.SecretLeases[key] = lease
			}
		}
		return nil
	})
	return err
}

// loadIntent reads one scoped intent and rejects altered stored digests.
func (controller *RetentionController) loadIntent(ctx context.Context, scope TenantScope, candidateID string) (DeletionIntent, error) {
	snapshot, err := controller.store.Snapshot(ctx)
	if err != nil {
		return DeletionIntent{}, err
	}
	intent, ok := snapshot.Deletions[deletionKey(scope, candidateID)]
	if !ok {
		return DeletionIntent{}, failure(CategoryNotFoundHidden, "deletion_load", nil)
	}
	if intent.Digest != deletionIntentDigest(intent) {
		return DeletionIntent{}, failure(CategoryIntegrity, "deletion_load", nil)
	}
	return intent, nil
}

// claim increments the durable fence before crossing the irreversible consumer boundary.
func (controller *RetentionController) claim(ctx context.Context, capability Capability, intent DeletionIntent, operationID string) (DeletionIntent, error) {
	if intent.Fence == MaxDurableCounter {
		return DeletionIntent{}, failure(CategoryExhausted, "deletion_claim", nil)
	}
	err := controller.transition(ctx, capability, intent, operationID, func(current *DeletionIntent) error {
		current.Fence++
		current.State = DeletionDeleting
		current.DeleteDigest = digestStrings(current.Scope.OrganizationID, current.Scope.ProjectID, current.ID, string(current.Resource), current.CandidateDigest, fmt.Sprint(current.Fence))
		return nil
	})
	if err != nil {
		return DeletionIntent{}, err
	}
	return controller.loadIntent(ctx, capability.Scope, intent.ID)
}

// finish atomically records terminal absence and its protected minimal tombstone.
func (controller *RetentionController) finish(ctx context.Context, capability Capability, intent DeletionIntent, operationID string) error {
	return controller.transition(ctx, capability, intent, operationID, func(current *DeletionIntent) error {
		current.State = DeletionTombstone
		current.TombstoneDigest = digestStrings(current.Scope.OrganizationID, current.Scope.ProjectID, current.ID, current.CandidateDigest, current.DeleteDigest, fmt.Sprint(current.Fence))
		return nil
	})
}

// setState persists a recoverable non-terminal controller state.
func (controller *RetentionController) setState(ctx context.Context, capability Capability, intent DeletionIntent, operationID, state string) error {
	return controller.transition(ctx, capability, intent, operationID, func(current *DeletionIntent) error { current.State = state; return nil })
}

// transition applies a fenced compare-and-swap and emits the corresponding audit in one commit.
func (controller *RetentionController) transition(ctx context.Context, capability Capability, expected DeletionIntent, operationID string, mutate func(*DeletionIntent) error) error {
	action := SensitiveAction{Capability: capability, OperationID: operationID, CommandDigest: digestStrings("deletion-transition", capability.Digest, operationID, expected.Digest), Outcome: "committed", ResourceID: expected.ID}
	_, err := controller.executor.Apply(ctx, action, func(snapshot *Snapshot) error {
		key := deletionKey(capability.Scope, expected.ID)
		current, ok := snapshot.Deletions[key]
		if !ok || current.Digest != expected.Digest || current.Fence != expected.Fence {
			return failure(CategoryStale, "deletion_transition", nil)
		}
		if err := mutate(&current); err != nil {
			return err
		}
		current.UpdatedAt = controller.clock.Now().UTC()
		current.Digest = deletionIntentDigest(current)
		snapshot.Deletions[key] = current
		return nil
	})
	return err
}

// deletionCommand derives a consumer key from the same verified capability used for the intent.
func deletionCommand(capability Capability, intent DeletionIntent) (DeletionCommand, error) {
	key, err := NewScopedKey(capability, intent.Resource, intent.ID)
	if err != nil {
		return DeletionCommand{}, err
	}
	return DeletionCommand{Key: key, CandidateDigest: intent.CandidateDigest, DeleteDigest: intent.DeleteDigest, Fence: intent.Fence}, nil
}

// deletionIntentDigest seals policy boundary, fence, state and tombstone identity.
func deletionIntentDigest(intent DeletionIntent) string {
	return digestStrings(intent.ID, intent.Scope.Digest, string(intent.Resource), intent.State, fmt.Sprint(intent.Fence), fmt.Sprint(intent.PolicyVersion), intent.PolicyDigest, intent.EligibilityDigest, intent.CandidateDigest, intent.DeleteDigest, intent.TombstoneDigest, intent.PlannedAt.UTC().Format(time.RFC3339Nano), intent.UpdatedAt.UTC().Format(time.RFC3339Nano))
}

// deletionKey isolates equal candidate IDs across projects.
func deletionKey(scope TenantScope, candidateID string) string {
	return scope.OrganizationID + "/" + scope.ProjectID + "/" + candidateID
}

// deletionRank keeps dependency-bearing containers after their children.
func deletionRank(resource ResourceClass) int {
	switch resource {
	case ResourceEnvironment:
		return 0
	case ResourceEvidence, ResourceSecret:
		return 1
	case ResourceRun, ResourceConfig:
		return 2
	case ResourceTenant:
		return 3
	default:
		return 4
	}
}
