package tenantsecurity

import (
	"context"
	"encoding/base64"
	"fmt"
	"slices"
	"strings"
	"time"
)

// ScopedEntityClass names tenant-owned product projections without accepting arbitrary table names.
type ScopedEntityClass string

const (
	// EntityRun stores validation workflow and external-agent run projections.
	EntityRun ScopedEntityClass = "run"
	// EntityConfiguration stores effective configuration projections.
	EntityConfiguration ScopedEntityClass = "configuration"
	// EntityPackage stores evidence package metadata.
	EntityPackage ScopedEntityClass = "package"
	// EntityEvidence stores evidence metadata.
	EntityEvidence ScopedEntityClass = "evidence"
	// EntityEnvironment stores environment lifecycle projections.
	EntityEnvironment ScopedEntityClass = "environment"
	// EntityCapacity stores environment capacity receipts.
	EntityCapacity ScopedEntityClass = "capacity"
	// EntityCleanup stores cleanup lifecycle projections.
	EntityCleanup ScopedEntityClass = "cleanup"
	// EntityCorrection stores correction lineage projections.
	EntityCorrection ScopedEntityClass = "correction"
)

// ConfigurationCoordinate is the trusted effective configuration fixed at admission.
type ConfigurationCoordinate struct {
	Generation      uint64 `json:"generation"`
	VersionDigest   string `json:"version_digest"`
	PolicyDigest    string `json:"policy_digest"`
	CatalogDigest   string `json:"catalog_digest"`
	EffectiveDigest string `json:"effective_digest"`
}

// TenantRunBinding seals tenant, principal, policy, configuration, run, phase and fence causally.
type TenantRunBinding struct {
	RunID         string                  `json:"run_id"`
	Scope         TenantScope             `json:"scope"`
	Configuration ConfigurationCoordinate `json:"configuration"`
	Phase         string                  `json:"phase"`
	Fence         uint64                  `json:"fence"`
	CreatedAt     time.Time               `json:"created_at"`
	Digest        string                  `json:"digest"`
}

// ScopedRecord is one tenant/project keyed product projection with optimistic version.
type ScopedRecord struct {
	OrganizationID string            `json:"organization_id"`
	ProjectID      string            `json:"project_id"`
	Class          ScopedEntityClass `json:"class"`
	ID             string            `json:"id"`
	Version        uint64            `json:"version"`
	PayloadDigest  string            `json:"payload_digest"`
	Digest         string            `json:"digest"`
}

// ScopedJob is a tenant/project/run queue item fenced across worker transitions.
type ScopedJob struct {
	OrganizationID string `json:"organization_id"`
	ProjectID      string `json:"project_id"`
	RunID          string `json:"run_id"`
	JobID          string `json:"job_id"`
	Phase          string `json:"phase"`
	State          string `json:"state"`
	Owner          string `json:"owner,omitempty"`
	Fence          uint64 `json:"fence"`
	BindingDigest  string `json:"binding_digest"`
	Digest         string `json:"digest"`
}

// MessageKind distinguishes tenant-scoped ingress, outbox and effect intent identities.
type MessageKind string

const (
	// MessageInbox is an externally delivered command accepted exactly once.
	MessageInbox MessageKind = "inbox"
	// MessageOutbox is a durable publication intent.
	MessageOutbox MessageKind = "outbox"
	// MessageEffect is a reconciliable external-effect intent.
	MessageEffect MessageKind = "effect"
)

// ScopedMessage is one idempotent tenant delivery or effect fenced for workers.
type ScopedMessage struct {
	OrganizationID string      `json:"organization_id"`
	ProjectID      string      `json:"project_id"`
	Kind           MessageKind `json:"kind"`
	IdempotencyKey string      `json:"idempotency_key"`
	PayloadDigest  string      `json:"payload_digest"`
	State          string      `json:"state"`
	Owner          string      `json:"owner,omitempty"`
	Fence          uint64      `json:"fence"`
	Digest         string      `json:"digest"`
}

// Admission atomically persists tenant binding, initial run/job, audit and receipt.
type Admission struct {
	store DurableStore
	clock Clock
}

// NewAdmission validates the durable authority used by run admission.
func NewAdmission(store DurableStore, clock Clock) (*Admission, error) {
	if store == nil || clock == nil {
		return nil, failure(CategoryInvalid, "admission_new", nil)
	}
	return &Admission{store: store, clock: clock}, nil
}

// Admit fixes every causal coordinate and publishes the initial run and job in one commit.
func (service *Admission) Admit(ctx context.Context, capability Capability, operationID, runID, phase string, configuration ConfigurationCoordinate) (TenantRunBinding, StoreReceipt, error) {
	return service.admit(ctx, capability, operationID, runID, phase, configuration, "queued")
}

// Prepare persists a non-runnable tenant binding intent before the workflow
// store commits its run and first event.
func (service *Admission) Prepare(ctx context.Context, capability Capability, operationID, runID, phase string, configuration ConfigurationCoordinate) (TenantRunBinding, StoreReceipt, error) {
	return service.admit(ctx, capability, operationID, runID, phase, configuration, "pending")
}

// admit commits either the legacy atomic admission or a pending saga intent.
func (service *Admission) admit(ctx context.Context, capability Capability, operationID, runID, phase string, configuration ConfigurationCoordinate, initialState string) (TenantRunBinding, StoreReceipt, error) {
	if err := ctx.Err(); err != nil {
		return TenantRunBinding{}, StoreReceipt{}, classifyContext("admission", err)
	}
	if ValidateID(operationID) != nil || ValidateID(runID) != nil || !validPhase(phase) || !validConfiguration(configuration) {
		return TenantRunBinding{}, StoreReceipt{}, failure(CategoryInvalid, "admission", nil)
	}
	commandDigest := digestStrings(runID, phase, fmt.Sprint(configuration.Generation), configuration.VersionDigest, configuration.PolicyDigest, configuration.CatalogDigest, configuration.EffectiveDigest, capability.Digest)
	if receipt, err := service.store.Receipt(ctx, capability.Scope.OrganizationID, operationID); err == nil {
		if receipt.CommandDigest != commandDigest {
			return TenantRunBinding{}, StoreReceipt{}, failure(CategoryConflict, "admission_replay", nil)
		}
		snapshot, loadErr := service.store.Snapshot(ctx)
		return snapshot.Bindings[bindingKey(capability.Scope, runID)], receipt, loadErr
	} else if !IsCategory(err, CategoryNotFoundHidden) {
		return TenantRunBinding{}, StoreReceipt{}, err
	}
	snapshot, err := service.store.Snapshot(ctx)
	if err != nil {
		return TenantRunBinding{}, StoreReceipt{}, err
	}
	if err = verifyDurableCapability(snapshot, capability, ActionRunAdmit, ResourceRun, service.clock.Now()); err != nil {
		return TenantRunBinding{}, StoreReceipt{}, err
	}
	if configuration.PolicyDigest != capability.Scope.PolicyDigest {
		return TenantRunBinding{}, StoreReceipt{}, failure(CategoryUnauthorized, "admission_policy", nil)
	}
	key := bindingKey(capability.Scope, runID)
	if _, exists := snapshot.Bindings[key]; exists {
		return TenantRunBinding{}, StoreReceipt{}, failure(CategoryConflict, "admission_run", nil)
	}
	binding := TenantRunBinding{RunID: runID, Scope: capability.Scope, Configuration: configuration, Phase: phase, Fence: 1, CreatedAt: service.clock.Now().UTC()}
	binding.Digest = tenantRunBindingDigest(binding)
	record := sealScopedRecord(ScopedRecord{OrganizationID: capability.Scope.OrganizationID, ProjectID: capability.Scope.ProjectID, Class: EntityRun, ID: runID, Version: 1, PayloadDigest: commandDigest})
	job := sealScopedJob(ScopedJob{OrganizationID: capability.Scope.OrganizationID, ProjectID: capability.Scope.ProjectID, RunID: runID, JobID: runID + "-first", Phase: phase, State: initialState, Fence: 1, BindingDigest: binding.Digest})
	snapshot.Bindings[key] = binding
	snapshot.Records[recordKey(record.OrganizationID, record.ProjectID, record.Class, record.ID)] = record
	snapshot.Jobs[jobKey(job.OrganizationID, job.ProjectID, job.JobID)] = job
	if _, err = appendCapabilityAudit(&snapshot, capability, "committed", "", service.clock.Now()); err != nil {
		return TenantRunBinding{}, StoreReceipt{}, err
	}
	if snapshot.Epoch >= MaxDurableCounter {
		return TenantRunBinding{}, StoreReceipt{}, failure(CategoryExhausted, "admission", nil)
	}
	expected := snapshot.Epoch
	snapshot.Epoch++
	receipt, err := service.store.Commit(ctx, StoreCommand{OrganizationID: capability.Scope.OrganizationID, OperationID: operationID, CommandDigest: commandDigest, ExpectedEpoch: expected, Snapshot: snapshot})
	if err != nil {
		if resolved, resolveErr := service.store.Receipt(ctx, capability.Scope.OrganizationID, operationID); resolveErr == nil && resolved.CommandDigest == commandDigest {
			return binding, resolved, nil
		}
		return TenantRunBinding{}, StoreReceipt{}, err
	}
	return binding, receipt, nil
}

// Confirm publishes a prepared first job only after the workflow run/event
// commit is known durable; replay is exactly once after crash or timeout.
func (service *Admission) Confirm(ctx context.Context, capability Capability, operationID, runID string) error {
	if service == nil || ValidateID(operationID) != nil || ValidateID(runID) != nil {
		return failure(CategoryInvalid, "admission_confirm", nil)
	}
	action := SensitiveAction{Capability: capability, OperationID: operationID, CommandDigest: digestStrings("admission-confirm", capability.Digest, runID), Outcome: "committed", ResourceID: runID}
	executor, err := NewSensitiveExecutor(service.store, service.clock)
	if err != nil {
		return err
	}
	_, err = executor.Apply(ctx, action, func(snapshot *Snapshot) error {
		binding, ok := snapshot.Bindings[bindingKey(capability.Scope, runID)]
		if !ok || binding.Digest != tenantRunBindingDigest(binding) {
			return failure(CategoryNotFoundHidden, "admission_confirm", nil)
		}
		key := jobKey(capability.Scope.OrganizationID, capability.Scope.ProjectID, runID+"-first")
		job, ok := snapshot.Jobs[key]
		if !ok || job.BindingDigest != binding.Digest {
			return failure(CategoryIntegrity, "admission_confirm", nil)
		}
		if job.State == "queued" {
			return nil
		}
		if job.State != "pending" {
			return failure(CategoryConflict, "admission_confirm", nil)
		}
		job.State = "queued"
		job.Digest = scopedJobDigest(job)
		snapshot.Jobs[key] = job
		return nil
	})
	return err
}

// Pending returns bounded pending intent identities in deterministic order for
// a recovery worker already authorized to admit runs in the same tenant.
func (service *Admission) Pending(ctx context.Context, capability Capability) ([]string, error) {
	bindings, err := service.PendingBindings(ctx, capability)
	if err != nil {
		return nil, err
	}
	runIDs := make([]string, len(bindings))
	for index, binding := range bindings {
		runIDs[index] = binding.RunID
	}
	return runIDs, nil
}

// PendingBindings returns each pending intent with its original immutable
// scope so recovery can use the creating principal rather than process identity.
func (service *Admission) PendingBindings(ctx context.Context, capability Capability) ([]TenantRunBinding, error) {
	if service == nil {
		return nil, failure(CategoryInvalid, "admission_pending", nil)
	}
	snapshot, err := service.store.Snapshot(ctx)
	if err != nil {
		return nil, err
	}
	if err = verifyDurableCapability(snapshot, capability, ActionRunAdmit, ResourceRun, service.clock.Now()); err != nil {
		return nil, err
	}
	bindings := make([]TenantRunBinding, 0)
	for _, binding := range snapshot.Bindings {
		if binding.Scope.OrganizationID != capability.Scope.OrganizationID || binding.Scope.ProjectID != capability.Scope.ProjectID {
			continue
		}
		job, ok := snapshot.Jobs[jobKey(binding.Scope.OrganizationID, binding.Scope.ProjectID, binding.RunID+"-first")]
		if ok && job.State == "pending" && job.BindingDigest == binding.Digest {
			bindings = append(bindings, binding)
		}
	}
	slices.SortFunc(bindings, func(left, right TenantRunBinding) int { return strings.Compare(left.RunID, right.RunID) })
	if len(bindings) > MaxItems {
		return nil, failure(CategoryExhausted, "admission_pending", nil)
	}
	return bindings, nil
}

// Abort terminalizes an orphan intent without deleting its binding or audit
// history, making repeated crash recovery exactly once and observable.
func (service *Admission) Abort(ctx context.Context, capability Capability, operationID, runID string) error {
	if service == nil || ValidateID(operationID) != nil || ValidateID(runID) != nil {
		return failure(CategoryInvalid, "admission_abort", nil)
	}
	action := SensitiveAction{Capability: capability, OperationID: operationID, CommandDigest: digestStrings("admission-abort", capability.Digest, runID), Outcome: "committed", ResourceID: runID}
	executor, err := NewSensitiveExecutor(service.store, service.clock)
	if err != nil {
		return err
	}
	_, err = executor.Apply(ctx, action, func(snapshot *Snapshot) error {
		binding, ok := snapshot.Bindings[bindingKey(capability.Scope, runID)]
		if !ok || binding.Digest != tenantRunBindingDigest(binding) {
			return failure(CategoryNotFoundHidden, "admission_abort", nil)
		}
		key := jobKey(capability.Scope.OrganizationID, capability.Scope.ProjectID, runID+"-first")
		job, ok := snapshot.Jobs[key]
		if !ok || job.BindingDigest != binding.Digest {
			return failure(CategoryIntegrity, "admission_abort", nil)
		}
		if job.State == "cancelled" {
			return nil
		}
		if job.State != "pending" {
			return failure(CategoryConflict, "admission_abort", nil)
		}
		job.State = "cancelled"
		job.Digest = scopedJobDigest(job)
		snapshot.Jobs[key] = job
		return nil
	})
	return err
}

// LoadBinding reloads one exact tenant/project/run binding without a global run lookup.
func (service *Admission) LoadBinding(ctx context.Context, capability Capability, runID string) (TenantRunBinding, error) {
	if ValidateID(runID) != nil {
		return TenantRunBinding{}, failure(CategoryInvalid, "binding_load", nil)
	}
	snapshot, err := service.store.Snapshot(ctx)
	if err != nil {
		return TenantRunBinding{}, err
	}
	if err = verifyDurableCapability(snapshot, capability, capability.Action, capability.Resource, service.clock.Now()); err != nil {
		return TenantRunBinding{}, err
	}
	binding, ok := snapshot.Bindings[bindingKey(capability.Scope, runID)]
	if !ok {
		return TenantRunBinding{}, failure(CategoryNotFoundHidden, "binding_load", nil)
	}
	if binding.Digest != tenantRunBindingDigest(binding) || binding.Scope.OrganizationID != capability.Scope.OrganizationID || binding.Scope.ProjectID != capability.Scope.ProjectID {
		return TenantRunBinding{}, failure(CategoryIntegrity, "binding_load", nil)
	}
	return binding, nil
}

// ScopedRepository stores and pages product projections using full tenant predicates.
type ScopedRepository struct {
	store DurableStore
	clock Clock
}

// NewScopedRepository creates a tenant-owned product projection repository.
func NewScopedRepository(store DurableStore, clock Clock) (*ScopedRepository, error) {
	if store == nil || clock == nil {
		return nil, failure(CategoryInvalid, "scoped_repository_new", nil)
	}
	return &ScopedRepository{store: store, clock: clock}, nil
}

// Put creates or CAS-updates one scoped projection with a durable receipt.
func (repository *ScopedRepository) Put(ctx context.Context, capability Capability, operationID string, class ScopedEntityClass, id, payloadDigest string, expectedVersion uint64) (ScopedRecord, StoreReceipt, error) {
	if ValidateID(operationID) != nil || ValidateID(id) != nil || !validDigest(payloadDigest) || !validEntityClass(class) || expectedVersion >= MaxDurableCounter {
		return ScopedRecord{}, StoreReceipt{}, failure(CategoryInvalid, "scoped_record_put", nil)
	}
	commandDigest := digestStrings(string(class), id, payloadDigest, fmt.Sprint(expectedVersion), capability.Digest)
	if receipt, err := repository.store.Receipt(ctx, capability.Scope.OrganizationID, operationID); err == nil {
		if receipt.CommandDigest != commandDigest {
			return ScopedRecord{}, StoreReceipt{}, failure(CategoryConflict, "scoped_record_replay", nil)
		}
		record, loadErr := repository.Get(ctx, capability, class, id)
		return record, receipt, loadErr
	} else if !IsCategory(err, CategoryNotFoundHidden) {
		return ScopedRecord{}, StoreReceipt{}, err
	}
	snapshot, err := repository.store.Snapshot(ctx)
	if err != nil {
		return ScopedRecord{}, StoreReceipt{}, err
	}
	if err = verifyEntityCapability(snapshot, capability, class, repository.clock.Now()); err != nil {
		return ScopedRecord{}, StoreReceipt{}, err
	}
	key := recordKey(capability.Scope.OrganizationID, capability.Scope.ProjectID, class, id)
	current, exists := snapshot.Records[key]
	if expectedVersion == 0 && exists || expectedVersion != 0 && (!exists || current.Version != expectedVersion) {
		return ScopedRecord{}, StoreReceipt{}, failure(CategoryStale, "scoped_record_put", nil)
	}
	record := sealScopedRecord(ScopedRecord{OrganizationID: capability.Scope.OrganizationID, ProjectID: capability.Scope.ProjectID, Class: class, ID: id, Version: expectedVersion + 1, PayloadDigest: payloadDigest})
	snapshot.Records[key] = record
	expectedEpoch := snapshot.Epoch
	snapshot.Epoch++
	receipt, err := repository.store.Commit(ctx, StoreCommand{OrganizationID: capability.Scope.OrganizationID, OperationID: operationID, CommandDigest: commandDigest, ExpectedEpoch: expectedEpoch, Snapshot: snapshot})
	return record, receipt, err
}

// Get returns only an exact organization/project/class/id projection and verifies its checksum.
func (repository *ScopedRepository) Get(ctx context.Context, capability Capability, class ScopedEntityClass, id string) (ScopedRecord, error) {
	if err := ctx.Err(); err != nil {
		return ScopedRecord{}, classifyContext("scoped_record_get", err)
	}
	snapshot, err := repository.store.Snapshot(ctx)
	if err != nil {
		return ScopedRecord{}, err
	}
	if err = verifyEntityCapability(snapshot, capability, class, repository.clock.Now()); err != nil {
		return ScopedRecord{}, err
	}
	record, ok := snapshot.Records[recordKey(capability.Scope.OrganizationID, capability.Scope.ProjectID, class, id)]
	if !ok {
		return ScopedRecord{}, failure(CategoryNotFoundHidden, "scoped_record_get", nil)
	}
	if record.Digest != scopedRecordDigest(record) {
		return ScopedRecord{}, failure(CategoryIntegrity, "scoped_record_get", nil)
	}
	return record, nil
}

// RecordPage is one bounded tenant-only page and opaque continuation cursor.
type RecordPage struct {
	Records []ScopedRecord
	Cursor  string
}

// List returns a bounded page without exposing cross-tenant counts through cursors.
func (repository *ScopedRepository) List(ctx context.Context, capability Capability, class ScopedEntityClass, cursor string, limit int) (RecordPage, error) {
	if limit <= 0 || limit > MaxItems {
		return RecordPage{}, failure(CategoryInvalid, "scoped_record_list", nil)
	}
	snapshot, err := repository.store.Snapshot(ctx)
	if err != nil {
		return RecordPage{}, err
	}
	if err = verifyEntityCapability(snapshot, capability, class, repository.clock.Now()); err != nil {
		return RecordPage{}, err
	}
	after, err := decodeRecordCursor(cursor, capability.Scope, class)
	if err != nil {
		return RecordPage{}, err
	}
	values := make([]ScopedRecord, 0)
	for _, record := range snapshot.Records {
		if record.OrganizationID == capability.Scope.OrganizationID && record.ProjectID == capability.Scope.ProjectID && record.Class == class && record.ID > after {
			values = append(values, record)
		}
	}
	slices.SortFunc(values, func(a, b ScopedRecord) int { return strings.Compare(a.ID, b.ID) })
	page := RecordPage{}
	if len(values) > limit {
		page.Records = values[:limit]
		page.Cursor = encodeRecordCursor(capability.Scope, class, page.Records[len(page.Records)-1].ID)
	} else {
		page.Records = values
	}
	return page, nil
}

// Queue transitions tenant-scoped jobs using exact scope, run, owner and fence checks.
type Queue struct {
	store DurableStore
	clock Clock
}

// MessageBus persists tenant-scoped inbox, outbox and effect identities without global aliases.
type MessageBus struct {
	store DurableStore
	clock Clock
}

// NewMessageBus creates a durable delivery boundary.
func NewMessageBus(store DurableStore, clock Clock) (*MessageBus, error) {
	if store == nil || clock == nil {
		return nil, failure(CategoryInvalid, "message_bus_new", nil)
	}
	return &MessageBus{store: store, clock: clock}, nil
}

// Accept confirms one payload identity and converges exact redelivery within its tenant scope.
func (bus *MessageBus) Accept(ctx context.Context, capability Capability, operationID string, kind MessageKind, idempotencyKey, payloadDigest string) (ScopedMessage, error) {
	if err := ctx.Err(); err != nil {
		return ScopedMessage{}, classifyContext("message_accept", err)
	}
	if ValidateID(operationID) != nil || ValidateID(idempotencyKey) != nil || !validDigest(payloadDigest) || !validMessageKind(kind) {
		return ScopedMessage{}, failure(CategoryInvalid, "message_accept", nil)
	}
	snapshot, err := bus.store.Snapshot(ctx)
	if err != nil {
		return ScopedMessage{}, err
	}
	if err = verifyDurableCapability(snapshot, capability, capability.Action, ResourceRun, bus.clock.Now()); err != nil {
		return ScopedMessage{}, err
	}
	key := messageKey(capability.Scope.OrganizationID, capability.Scope.ProjectID, kind, idempotencyKey)
	if existing, ok := snapshot.Messages[key]; ok {
		if existing.PayloadDigest != payloadDigest {
			return ScopedMessage{}, failure(CategoryConflict, "message_accept", nil)
		}
		return existing, nil
	}
	message := sealScopedMessage(ScopedMessage{OrganizationID: capability.Scope.OrganizationID, ProjectID: capability.Scope.ProjectID, Kind: kind, IdempotencyKey: idempotencyKey, PayloadDigest: payloadDigest, State: "pending", Fence: 1})
	snapshot.Messages[key] = message
	commandDigest := digestStrings(string(kind), idempotencyKey, payloadDigest, capability.Digest)
	expectedEpoch := snapshot.Epoch
	snapshot.Epoch++
	if _, err = bus.store.Commit(ctx, StoreCommand{OrganizationID: capability.Scope.OrganizationID, OperationID: operationID, CommandDigest: commandDigest, ExpectedEpoch: expectedEpoch, Snapshot: snapshot}); err != nil {
		return ScopedMessage{}, err
	}
	return message, nil
}

// Claim advances one exact delivery fence before a worker performs its effect.
func (bus *MessageBus) Claim(ctx context.Context, capability Capability, operationID string, kind MessageKind, idempotencyKey, worker string, expectedFence uint64) (ScopedMessage, error) {
	if ValidateID(operationID) != nil || ValidateID(idempotencyKey) != nil || ValidateID(worker) != nil || !validMessageKind(kind) || expectedFence == 0 || expectedFence >= MaxDurableCounter {
		return ScopedMessage{}, failure(CategoryInvalid, "message_claim", nil)
	}
	snapshot, err := bus.store.Snapshot(ctx)
	if err != nil {
		return ScopedMessage{}, err
	}
	if err = verifyDurableCapability(snapshot, capability, capability.Action, ResourceRun, bus.clock.Now()); err != nil {
		return ScopedMessage{}, err
	}
	key := messageKey(capability.Scope.OrganizationID, capability.Scope.ProjectID, kind, idempotencyKey)
	message, ok := snapshot.Messages[key]
	if !ok {
		return ScopedMessage{}, failure(CategoryNotFoundHidden, "message_claim", nil)
	}
	if message.State != "pending" || message.Fence != expectedFence {
		return ScopedMessage{}, failure(CategoryStale, "message_claim", nil)
	}
	message.State, message.Owner, message.Fence = "claimed", worker, message.Fence+1
	message = sealScopedMessage(message)
	snapshot.Messages[key] = message
	commandDigest := digestStrings(string(kind), idempotencyKey, worker, fmt.Sprint(expectedFence), capability.Digest)
	expectedEpoch := snapshot.Epoch
	snapshot.Epoch++
	if _, err = bus.store.Commit(ctx, StoreCommand{OrganizationID: capability.Scope.OrganizationID, OperationID: operationID, CommandDigest: commandDigest, ExpectedEpoch: expectedEpoch, Snapshot: snapshot}); err != nil {
		return ScopedMessage{}, err
	}
	return message, nil
}

// NewQueue creates a durable tenant job queue.
func NewQueue(store DurableStore, clock Clock) (*Queue, error) {
	if store == nil || clock == nil {
		return nil, failure(CategoryInvalid, "queue_new", nil)
	}
	return &Queue{store: store, clock: clock}, nil
}

// Claim moves one exact queued job to running and advances its fence atomically.
func (queue *Queue) Claim(ctx context.Context, capability Capability, operationID, jobID, worker string, expectedFence uint64) (ScopedJob, error) {
	return queue.transition(ctx, capability, operationID, jobID, worker, expectedFence, "queued", "running")
}

// Complete terminates one job only for its current owner and fence.
func (queue *Queue) Complete(ctx context.Context, capability Capability, operationID, jobID, worker string, expectedFence uint64) (ScopedJob, error) {
	return queue.transition(ctx, capability, operationID, jobID, worker, expectedFence, "running", "completed")
}

// transition performs one receipt-backed queue state CAS without a global job lookup.
func (queue *Queue) transition(ctx context.Context, capability Capability, operationID, jobID, worker string, expectedFence uint64, from, to string) (ScopedJob, error) {
	if err := ctx.Err(); err != nil {
		return ScopedJob{}, classifyContext("queue_transition", err)
	}
	if ValidateID(operationID) != nil || ValidateID(jobID) != nil || ValidateID(worker) != nil || expectedFence == 0 || expectedFence >= MaxDurableCounter {
		return ScopedJob{}, failure(CategoryInvalid, "queue_transition", nil)
	}
	snapshot, err := queue.store.Snapshot(ctx)
	if err != nil {
		return ScopedJob{}, err
	}
	if err = verifyDurableCapability(snapshot, capability, capability.Action, ResourceRun, queue.clock.Now()); err != nil {
		return ScopedJob{}, err
	}
	key := jobKey(capability.Scope.OrganizationID, capability.Scope.ProjectID, jobID)
	job, ok := snapshot.Jobs[key]
	if !ok {
		return ScopedJob{}, failure(CategoryNotFoundHidden, "queue_transition", nil)
	}
	if job.State != from || job.Fence != expectedFence || from == "running" && job.Owner != worker {
		return ScopedJob{}, failure(CategoryStale, "queue_transition", nil)
	}
	job.State, job.Fence = to, job.Fence+1
	if to == "running" {
		job.Owner = worker
	}
	job = sealScopedJob(job)
	snapshot.Jobs[key] = job
	commandDigest := digestStrings(jobID, worker, fmt.Sprint(expectedFence), from, to, capability.Digest)
	expectedEpoch := snapshot.Epoch
	snapshot.Epoch++
	if _, err = queue.store.Commit(ctx, StoreCommand{OrganizationID: capability.Scope.OrganizationID, OperationID: operationID, CommandDigest: commandDigest, ExpectedEpoch: expectedEpoch, Snapshot: snapshot}); err != nil {
		return ScopedJob{}, err
	}
	return job, nil
}

// verifyDurableCapability authenticates the sealed current scope before any resource lookup.
func verifyDurableCapability(snapshot Snapshot, capability Capability, action Action, resource ResourceClass, now time.Time) error {
	organization, ok := snapshot.Organizations[capability.Scope.OrganizationID]
	project, projectOK := snapshot.Projects[projectKey(capability.Scope.OrganizationID, capability.Scope.ProjectID)]
	projectPermitsAction := project.State == StateActive || project.State == StateSuspended && action == ActionRetentionExecute
	if !ok || organization.State != StateActive || !projectOK || !projectPermitsAction || project.RepositoryID != capability.Scope.RepositoryID || capability.Action != action || capability.Resource != resource || capability.Digest != digestStrings(capability.Scope.Digest, string(capability.Action), string(capability.Resource)) || capability.Scope.SecurityRevision != organization.SecurityRevision || capability.Scope.PolicyDigest != organization.PolicyDigest || !now.Before(capability.Scope.ExpiresAt) {
		return failure(CategoryUnauthorized, "capability_durable_verify", nil)
	}
	return nil
}

// ValidateCapabilitySnapshot exposes current-snapshot verification to consumer-owned adapters.
func ValidateCapabilitySnapshot(snapshot Snapshot, capability Capability, action Action, resource ResourceClass, now time.Time) error {
	return verifyDurableCapability(snapshot, capability, action, resource, now)
}

// verifyEntityCapability maps product classes to their required resource class.
func verifyEntityCapability(snapshot Snapshot, capability Capability, class ScopedEntityClass, now time.Time) error {
	return verifyDurableCapability(snapshot, capability, capability.Action, entityResource(class), now)
}

// appendCapabilityAudit appends one immutable allowlisted event to a candidate transaction.
func appendCapabilityAudit(snapshot *Snapshot, capability Capability, outcome string, reason Category, at time.Time) (AuditEvent, error) {
	events := snapshot.Audits[capability.Scope.OrganizationID]
	if uint64(len(events)) >= MaxDurableCounter {
		return AuditEvent{}, failure(CategoryExhausted, "capability_audit", nil)
	}
	previous := ""
	if len(events) != 0 {
		previous = events[len(events)-1].Digest
	}
	event := AuditEvent{OrganizationID: capability.Scope.OrganizationID, Sequence: uint64(len(events) + 1), ActorDigest: digestStrings(capability.Scope.PrincipalID), Action: capability.Action, Resource: capability.Resource, Outcome: outcome, Reason: reason, PreviousDigest: previous, At: at.UTC()}
	event.Digest = auditDigest(event)
	snapshot.Audits[event.OrganizationID] = append(events, event)
	snapshot.AuditHeads[event.OrganizationID] = sealAuditHead(event.OrganizationID, event.Sequence, event.Digest)
	return event, nil
}

// tenantRunBindingDigest seals every causal coordinate without rederiving organization from IDs.
func tenantRunBindingDigest(binding TenantRunBinding) string {
	return digestStrings(binding.RunID, binding.Scope.Digest, fmt.Sprint(binding.Configuration.Generation), binding.Configuration.VersionDigest, binding.Configuration.PolicyDigest, binding.Configuration.CatalogDigest, binding.Configuration.EffectiveDigest, binding.Phase, fmt.Sprint(binding.Fence), binding.CreatedAt.UTC().Format(time.RFC3339Nano))
}

// scopedRecordDigest seals one exact composite record identity and payload.
func scopedRecordDigest(record ScopedRecord) string {
	return digestStrings(record.OrganizationID, record.ProjectID, string(record.Class), record.ID, fmt.Sprint(record.Version), record.PayloadDigest)
}

// sealScopedRecord computes the immutable identity of one product projection.
func sealScopedRecord(record ScopedRecord) ScopedRecord {
	record.Digest = scopedRecordDigest(record)
	return record
}

// scopedJobDigest seals scope, binding and fencing state for workers.
func scopedJobDigest(job ScopedJob) string {
	return digestStrings(job.OrganizationID, job.ProjectID, job.RunID, job.JobID, job.Phase, job.State, job.Owner, fmt.Sprint(job.Fence), job.BindingDigest)
}

// sealScopedJob computes the immutable identity of one queue projection.
func sealScopedJob(job ScopedJob) ScopedJob { job.Digest = scopedJobDigest(job); return job }

// bindingKey namespaces a run binding by organization and project.
func bindingKey(scope TenantScope, runID string) string {
	return scope.OrganizationID + "/" + scope.ProjectID + "/" + runID
}

// recordKey namespaces every product record by organization, project and class.
func recordKey(organizationID, projectID string, class ScopedEntityClass, id string) string {
	return organizationID + "/" + projectID + "/" + string(class) + "/" + id
}

// jobKey namespaces every queue identity by organization and project.
func jobKey(organizationID, projectID, jobID string) string {
	return organizationID + "/" + projectID + "/" + jobID
}

// messageKey namespaces idempotency by organization, project and delivery kind.
func messageKey(organizationID, projectID string, kind MessageKind, idempotencyKey string) string {
	return organizationID + "/" + projectID + "/" + string(kind) + "/" + idempotencyKey
}

// scopedMessageDigest seals payload identity, state, owner and fence.
func scopedMessageDigest(message ScopedMessage) string {
	return digestStrings(message.OrganizationID, message.ProjectID, string(message.Kind), message.IdempotencyKey, message.PayloadDigest, message.State, message.Owner, fmt.Sprint(message.Fence))
}

// sealScopedMessage computes one immutable delivery projection checksum.
func sealScopedMessage(message ScopedMessage) ScopedMessage {
	message.Digest = scopedMessageDigest(message)
	return message
}

// validConfiguration rejects incomplete effective configuration coordinates.
func validConfiguration(configuration ConfigurationCoordinate) bool {
	return configuration.Generation > 0 && validDigest(configuration.VersionDigest) && validDigest(configuration.PolicyDigest) && validDigest(configuration.CatalogDigest) && validDigest(configuration.EffectiveDigest)
}

// validPhase recognizes only bounded validation lifecycle phases.
func validPhase(phase string) bool {
	return slices.Contains([]string{"admit", "plan", "execute", "verify", "publish", "cleanup"}, phase)
}

// validEntityClass recognizes only product-owned projection categories.
func validEntityClass(class ScopedEntityClass) bool {
	return slices.Contains([]ScopedEntityClass{EntityRun, EntityConfiguration, EntityPackage, EntityEvidence, EntityEnvironment, EntityCapacity, EntityCleanup, EntityCorrection}, class)
}

// validMessageKind fences delivery persistence to inbox, outbox and effect intents.
func validMessageKind(kind MessageKind) bool {
	return kind == MessageInbox || kind == MessageOutbox || kind == MessageEffect
}

// entityResource maps a product class to the closed authorization resource catalog.
func entityResource(class ScopedEntityClass) ResourceClass {
	switch class {
	case EntityConfiguration:
		return ResourceConfig
	case EntityPackage, EntityEvidence:
		return ResourceEvidence
	case EntityEnvironment, EntityCapacity, EntityCleanup:
		return ResourceEnvironment
	default:
		return ResourceRun
	}
}

// encodeRecordCursor seals tenant scope, class and last ID into an opaque cursor.
func encodeRecordCursor(scope TenantScope, class ScopedEntityClass, after string) string {
	payload := scope.Digest + "\x00" + string(class) + "\x00" + after
	return base64.RawURLEncoding.EncodeToString([]byte(payload + "\x00" + digestStrings(payload)))
}

// decodeRecordCursor rejects manipulated or cross-tenant continuation state.
func decodeRecordCursor(cursor string, scope TenantScope, class ScopedEntityClass) (string, error) {
	if cursor == "" {
		return "", nil
	}
	raw, err := base64.RawURLEncoding.DecodeString(cursor)
	parts := strings.Split(string(raw), "\x00")
	if err != nil || len(parts) != 4 || parts[0] != scope.Digest || parts[1] != string(class) || digestStrings(strings.Join(parts[:3], "\x00")) != parts[3] || ValidateID(parts[2]) != nil {
		return "", failure(CategoryInvalid, "scoped_record_cursor", nil)
	}
	return parts[2], nil
}

// ValidateTenantRunBinding verifies a reloaded binding without consulting current heads.
func ValidateTenantRunBinding(binding TenantRunBinding) error {
	if ValidateID(binding.RunID) != nil || !validConfiguration(binding.Configuration) || !validPhase(binding.Phase) || binding.Fence == 0 || binding.Digest != tenantRunBindingDigest(binding) {
		return failure(CategoryIntegrity, "binding_validate", nil)
	}
	return nil
}
