package tenantsecurity

import (
	"context"
	"fmt"
	"math"
	"slices"
	"time"
)

// RetentionClass is a closed lifecycle class with an independently versioned retention period.
type RetentionClass string

const (
	// RetentionRun governs terminal validation-run records.
	RetentionRun RetentionClass = "run"
	// RetentionPackage governs generated validation packages.
	RetentionPackage RetentionClass = "package"
	// RetentionEvidence governs evidence artifacts and their metadata.
	RetentionEvidence RetentionClass = "evidence"
	// RetentionProject governs project-owned configuration data.
	RetentionProject RetentionClass = "project_data"
	// RetentionSecret governs inactive encrypted secret versions.
	RetentionSecret RetentionClass = "secret_version"
	// RetentionAudit governs protected security journal records.
	RetentionAudit RetentionClass = "audit"
	// RetentionTombstone governs minimal deletion proof retained after purge.
	RetentionTombstone RetentionClass = "tombstone"
)

// retentionClasses returns the canonical order used by policy digests.
func retentionClasses() []RetentionClass {
	return []RetentionClass{RetentionRun, RetentionPackage, RetentionEvidence, RetentionProject, RetentionSecret, RetentionAudit, RetentionTombstone}
}

// RetentionCandidate is the immutable lifecycle observation evaluated before a deletion intent exists.
type RetentionCandidate struct {
	ID             string
	Class          RetentionClass
	TerminalAt     time.Time
	Active         bool
	LiveReferences uint64
	PendingJobs    uint64
	PendingOutbox  uint64
	PendingCleanup uint64
	ActiveSecret   bool
	Protected      bool
	Digest         string
}

// EligibilitySnapshot freezes the policy and candidate boundary used by all retries of one delete.
type EligibilitySnapshot struct {
	CandidateID     string
	CandidateClass  RetentionClass
	CandidateDigest string
	PolicyVersion   uint64
	PolicyDigest    string
	EvaluatedAt     time.Time
	EligibleAfter   time.Time
	Digest          string
}

// SealRetentionCandidate validates a bounded observation and seals its live-reference facts.
func SealRetentionCandidate(candidate RetentionCandidate) (RetentionCandidate, error) {
	if ValidateID(candidate.ID) != nil || !slices.Contains(retentionClasses(), candidate.Class) || candidate.TerminalAt.IsZero() || candidate.LiveReferences > MaxDurableCounter || candidate.PendingJobs > MaxDurableCounter || candidate.PendingOutbox > MaxDurableCounter || candidate.PendingCleanup > MaxDurableCounter {
		return RetentionCandidate{}, failure(CategoryInvalid, "retention_candidate", nil)
	}
	candidate.Digest = digestStrings(candidate.ID, string(candidate.Class), candidate.TerminalAt.UTC().Format(time.RFC3339Nano), fmt.Sprint(candidate.Active), fmt.Sprint(candidate.LiveReferences), fmt.Sprint(candidate.PendingJobs), fmt.Sprint(candidate.PendingOutbox), fmt.Sprint(candidate.PendingCleanup), fmt.Sprint(candidate.ActiveSecret), fmt.Sprint(candidate.Protected))
	return candidate, nil
}

// EvaluateRetention rejects active or referenced data and binds eligible work to one policy version.
func EvaluateRetention(policy RetentionPolicy, candidate RetentionCandidate, now time.Time) (EligibilitySnapshot, error) {
	compiled, err := CompileRetention(policy)
	if err != nil {
		return EligibilitySnapshot{}, err
	}
	candidate, err = SealRetentionCandidate(candidate)
	if err != nil {
		return EligibilitySnapshot{}, err
	}
	protected := candidate.Protected || slices.Contains(compiled.ProtectedRecords[candidate.Class], candidate.ID) || slices.Contains(compiled.Protected, retentionResource(candidate.Class))
	if candidate.Active || candidate.LiveReferences != 0 || candidate.PendingJobs != 0 || candidate.PendingOutbox != 0 || candidate.PendingCleanup != 0 || candidate.ActiveSecret || protected {
		return EligibilitySnapshot{}, failure(CategoryConflict, "retention_eligibility", nil)
	}
	eligibleAfter := candidate.TerminalAt.Add(compiled.Periods[candidate.Class])
	if now.Before(eligibleAfter) {
		return EligibilitySnapshot{}, failure(CategoryConflict, "retention_eligibility", nil)
	}
	snapshot := EligibilitySnapshot{CandidateID: candidate.ID, CandidateClass: candidate.Class, CandidateDigest: candidate.Digest, PolicyVersion: compiled.Version, PolicyDigest: compiled.Digest, EvaluatedAt: now.UTC(), EligibleAfter: eligibleAfter.UTC()}
	snapshot.Digest = digestStrings(snapshot.CandidateID, string(snapshot.CandidateClass), snapshot.CandidateDigest, fmt.Sprint(snapshot.PolicyVersion), snapshot.PolicyDigest, snapshot.EvaluatedAt.Format(time.RFC3339Nano), snapshot.EligibleAfter.Format(time.RFC3339Nano))
	return snapshot, nil
}

// ValidateEligibilitySnapshot rejects policy or timing fields altered after the planner decision.
func ValidateEligibilitySnapshot(snapshot EligibilitySnapshot) error {
	if ValidateID(snapshot.CandidateID) != nil || !slices.Contains(retentionClasses(), snapshot.CandidateClass) || !validDigest(snapshot.CandidateDigest) || snapshot.PolicyVersion == 0 || !validDigest(snapshot.PolicyDigest) || snapshot.EvaluatedAt.IsZero() || snapshot.EligibleAfter.IsZero() {
		return failure(CategoryIntegrity, "retention_eligibility_validate", nil)
	}
	expected := digestStrings(snapshot.CandidateID, string(snapshot.CandidateClass), snapshot.CandidateDigest, fmt.Sprint(snapshot.PolicyVersion), snapshot.PolicyDigest, snapshot.EvaluatedAt.UTC().Format(time.RFC3339Nano), snapshot.EligibleAfter.UTC().Format(time.RFC3339Nano))
	if snapshot.Digest != expected {
		return failure(CategoryIntegrity, "retention_eligibility_validate", nil)
	}
	return nil
}

// retentionResource maps lifecycle classes to the authorization resource protecting them.
func retentionResource(class RetentionClass) ResourceClass {
	switch class {
	case RetentionRun:
		return ResourceRun
	case RetentionPackage, RetentionProject:
		return ResourceConfig
	case RetentionEvidence:
		return ResourceEvidence
	case RetentionSecret:
		return ResourceSecret
	case RetentionAudit, RetentionTombstone:
		return ResourceAudit
	default:
		return ""
	}
}

// QuotaPolicySnapshot freezes the tenant and project ceilings used by one reservation.
type QuotaPolicySnapshot struct {
	Version            uint64
	Tenant             map[string]QuotaLimit
	Project            map[string]QuotaLimit
	TenantOutstanding  uint64
	ProjectOutstanding uint64
	ReservationTTL     time.Duration
	Digest             string
}

// CompileQuotaPolicy validates both hierarchy levels and seals dimensions in canonical order.
func CompileQuotaPolicy(policy QuotaPolicySnapshot) (QuotaPolicySnapshot, error) {
	if policy.Version == 0 || policy.Tenant == nil || policy.Project == nil || len(policy.Tenant) == 0 || len(policy.Tenant) > MaxItems || len(policy.Project) > MaxItems || policy.TenantOutstanding == 0 || policy.ProjectOutstanding == 0 || policy.ProjectOutstanding > policy.TenantOutstanding || policy.TenantOutstanding > uint64(MaxItems) || policy.ReservationTTL <= 0 {
		return QuotaPolicySnapshot{}, failure(CategoryInvalid, "quota_policy", nil)
	}
	dimensions := make([]string, 0, len(policy.Tenant))
	for dimension, tenant := range policy.Tenant {
		if ValidateID(dimension) != nil || !validQuotaLimit(tenant) {
			return QuotaPolicySnapshot{}, failure(CategoryInvalid, "quota_policy", nil)
		}
		project, ok := policy.Project[dimension]
		if !ok || !validQuotaLimit(project) || project.Window != tenant.Window || exceedsQuotaLimit(project, tenant) || policy.ReservationTTL > tenant.Window {
			return QuotaPolicySnapshot{}, failure(CategoryInvalid, "quota_policy", nil)
		}
		dimensions = append(dimensions, dimension)
	}
	if len(policy.Project) != len(policy.Tenant) {
		return QuotaPolicySnapshot{}, failure(CategoryInvalid, "quota_policy", nil)
	}
	slices.Sort(dimensions)
	parts := []string{fmt.Sprint(policy.Version), fmt.Sprint(policy.TenantOutstanding), fmt.Sprint(policy.ProjectOutstanding), policy.ReservationTTL.String()}
	tenantCopy, projectCopy := make(map[string]QuotaLimit, len(dimensions)), make(map[string]QuotaLimit, len(dimensions))
	for _, dimension := range dimensions {
		tenant, project := policy.Tenant[dimension], policy.Project[dimension]
		tenantCopy[dimension], projectCopy[dimension] = tenant, project
		parts = append(parts, dimension, fmt.Sprint(tenant.Limit), tenant.Window.String(), fmt.Sprint(tenant.Burst), fmt.Sprint(project.Limit), project.Window.String(), fmt.Sprint(project.Burst))
	}
	policy.Tenant, policy.Project = tenantCopy, projectCopy
	policy.Digest = digestStrings(parts...)
	return policy, nil
}

// QuotaRequest describes one durable pre-effect reservation identity and demand.
type QuotaRequest struct {
	Capability  Capability
	OperationID string
	Dimension   string
	Units       uint64
	Policy      QuotaPolicySnapshot
}

// DurableQuotaService serializes hierarchical reservations through the shared snapshot CAS boundary.
type DurableQuotaService struct {
	executor *SensitiveExecutor
	store    DurableStore
	clock    Clock
}

// NewDurableQuotaService requires durable receipts so process restart cannot repeat capacity grants.
func NewDurableQuotaService(store DurableStore, clock Clock) (*DurableQuotaService, error) {
	executor, err := NewSensitiveExecutor(store, clock)
	if err != nil {
		return nil, err
	}
	return &DurableQuotaService{executor: executor, store: store, clock: clock}, nil
}

// Reserve commits tenant and project counters atomically before the caller may perform an effect.
func (service *DurableQuotaService) Reserve(ctx context.Context, request QuotaRequest) (Reservation, error) {
	if service == nil || request.Capability.Action != ActionQuotaReserve || request.Capability.Resource != ResourceQuota || ValidateID(request.OperationID) != nil || ValidateID(request.Dimension) != nil || request.Units == 0 {
		return Reservation{}, failure(CategoryInvalid, "quota_reserve_durable", nil)
	}
	policy, err := CompileQuotaPolicy(request.Policy)
	if err != nil {
		return Reservation{}, err
	}
	tenantLimit, tenantOK := policy.Tenant[request.Dimension]
	projectLimit, projectOK := policy.Project[request.Dimension]
	if !tenantOK || !projectOK {
		return Reservation{}, failure(CategoryQuotaExhausted, "quota_reserve_durable", nil)
	}
	demandDigest := digestStrings(request.Dimension, fmt.Sprint(request.Units), policy.Digest, request.Capability.Scope.OrganizationID, request.Capability.Scope.ProjectID)
	action := SensitiveAction{Capability: request.Capability, OperationID: request.OperationID, CommandDigest: digestStrings("quota-reserve", request.Capability.Digest, request.OperationID, demandDigest), Outcome: "committed", ResourceID: request.Dimension}
	for attempt := 0; attempt < MaxItems; attempt++ {
		_, err = service.executor.Apply(ctx, action, func(snapshot *Snapshot) error {
			key := quotaReservationKey(request.Capability.Scope, request.OperationID)
			if existing, ok := snapshot.Reservations[key]; ok {
				if existing.DemandDigest != demandDigest {
					return failure(CategoryConflict, "quota_reserve_durable", nil)
				}
				return nil
			}
			now := service.clock.Now().UTC()
			var tenantOutstanding, projectOutstanding uint64
			for _, outstanding := range snapshot.Reservations {
				if outstanding.Released || outstanding.Scope.OrganizationID != request.Capability.Scope.OrganizationID {
					continue
				}
				tenantOutstanding++
				if outstanding.Scope.ProjectID == request.Capability.Scope.ProjectID {
					projectOutstanding++
				}
			}
			if tenantOutstanding >= policy.TenantOutstanding || projectOutstanding >= policy.ProjectOutstanding {
				return failure(CategoryQuotaExhausted, "quota_reserve_durable", nil)
			}
			windowStart := now.Truncate(tenantLimit.Window)
			counterKeys := []string{
				quotaCounterKey(request.Capability.Scope.OrganizationID, "", request.Dimension, windowStart),
				quotaCounterKey(request.Capability.Scope.OrganizationID, request.Capability.Scope.ProjectID, request.Dimension, windowStart),
			}
			limits := []QuotaLimit{tenantLimit, projectLimit}
			for index, counterKey := range counterKeys {
				counter := snapshot.Quotas[counterKey]
				ceiling, overflow := addUint64(limits[index].Limit, limits[index].Burst)
				if overflow || request.Units > ceiling || counter.Reserved > ceiling-request.Units {
					return failure(CategoryQuotaExhausted, "quota_reserve_durable", nil)
				}
			}
			for index, counterKey := range counterKeys {
				counter := snapshot.Quotas[counterKey]
				counter.OrganizationID = request.Capability.Scope.OrganizationID
				counter.ProjectID = request.Capability.Scope.ProjectID
				counter.Dimension = request.Dimension
				counter.Level = "tenant"
				if index == 1 {
					counter.Level = "project"
				}
				counter.WindowStart, counter.Limit, counter.PolicyDigest = windowStart, limits[index], policy.Digest
				counter.Reserved += request.Units
				snapshot.Quotas[counterKey] = counter
			}
			reservation := Reservation{ID: request.OperationID, Scope: request.Capability.Scope, Dimension: request.Dimension, Units: request.Units, WindowStart: windowStart, ExpiresAt: now.Add(policy.ReservationTTL), PolicyVersion: policy.Version, PolicyDigest: policy.Digest, DemandDigest: demandDigest, CounterKeys: counterKeys}
			reservation.Digest = reservationDigest(reservation)
			snapshot.Reservations[key] = reservation
			return nil
		})
		if err == nil {
			snapshot, loadErr := service.store.Snapshot(ctx)
			if loadErr != nil {
				return Reservation{}, loadErr
			}
			return snapshot.Reservations[quotaReservationKey(request.Capability.Scope, request.OperationID)], nil
		}
		if !IsCategory(err, CategoryStale) {
			return Reservation{}, err
		}
	}
	return Reservation{}, failure(CategoryUnavailable, "quota_reserve_durable", nil)
}

// MarkUncertain retains charged capacity until reconciliation establishes a terminal outcome.
func (service *DurableQuotaService) MarkUncertain(ctx context.Context, capability Capability, reservationID, operationID string) error {
	return service.updateReservation(ctx, capability, reservationID, operationID, "uncertain", func(reservation *Reservation, _ *Snapshot) error {
		reservation.Uncertain = true
		reservation.Digest = reservationDigest(*reservation)
		return nil
	})
}

// ReservationState returns one verified durable reservation so a consumer
// adapter can distinguish confirmed terminal work from unresolved effects.
func (service *DurableQuotaService) ReservationState(ctx context.Context, capability Capability, reservationID string) (Reservation, error) {
	if service == nil || ValidateID(reservationID) != nil {
		return Reservation{}, failure(CategoryInvalid, "quota_reservation_state", nil)
	}
	snapshot, err := service.store.Snapshot(ctx)
	if err != nil {
		return Reservation{}, err
	}
	if err = verifyDurableCapability(snapshot, capability, ActionQuotaReserve, ResourceQuota, service.clock.Now()); err != nil {
		return Reservation{}, err
	}
	reservation, ok := snapshot.Reservations[quotaReservationKey(capability.Scope, reservationID)]
	if !ok || reservation.Digest != reservationDigest(reservation) {
		return Reservation{}, failure(CategoryNotFoundHidden, "quota_reservation_state", nil)
	}
	return reservation, nil
}

// BindResources durably associates one reservation with the exact resources
// returned by the external claim/acquisition result.
func (service *DurableQuotaService) BindResources(ctx context.Context, capability Capability, reservationID, operationID string, resourceIDs []string) error {
	resourceIDs = slices.Clone(resourceIDs)
	slices.Sort(resourceIDs)
	if len(resourceIDs) == 0 || len(resourceIDs) > MaxItems || hasDuplicates(resourceIDs) {
		return failure(CategoryInvalid, "quota_bind_resources", nil)
	}
	for _, id := range resourceIDs {
		if ValidateID(id) != nil {
			return failure(CategoryInvalid, "quota_bind_resources", nil)
		}
	}
	return service.updateReservation(ctx, capability, reservationID, operationID, "bind", func(reservation *Reservation, _ *Snapshot) error {
		if len(reservation.ResourceIDs) != 0 && !slices.Equal(reservation.ResourceIDs, resourceIDs) {
			return failure(CategoryConflict, "quota_bind_resources", nil)
		}
		reservation.ResourceIDs = slices.Clone(resourceIDs)
		reservation.Digest = reservationDigest(*reservation)
		return nil
	})
}

// ReleaseResource records one terminal resource exactly once and releases the
// reservation only after every resource in its durable batch is terminal.
func (service *DurableQuotaService) ReleaseResource(ctx context.Context, capability Capability, resourceID, operationID string, uncertain bool) error {
	if ValidateID(resourceID) != nil {
		return failure(CategoryInvalid, "quota_release_resource", nil)
	}
	snapshot, err := service.store.Snapshot(ctx)
	if err != nil {
		return err
	}
	var reservationID string
	for _, reservation := range snapshot.Reservations {
		if reservation.Scope.OrganizationID == capability.Scope.OrganizationID && reservation.Scope.ProjectID == capability.Scope.ProjectID && slices.Contains(reservation.ResourceIDs, resourceID) {
			if reservationID != "" && reservationID != reservation.ID {
				return failure(CategoryIntegrity, "quota_release_resource", nil)
			}
			reservationID = reservation.ID
		}
	}
	if reservationID == "" {
		return failure(CategoryNotFoundHidden, "quota_release_resource", nil)
	}
	return service.updateReservation(ctx, capability, reservationID, operationID, "resource", func(reservation *Reservation, state *Snapshot) error {
		if !slices.Contains(reservation.ResourceIDs, resourceID) {
			return failure(CategoryNotFoundHidden, "quota_release_resource", nil)
		}
		if uncertain {
			reservation.Uncertain = true
			reservation.Digest = reservationDigest(*reservation)
			return nil
		}
		if !slices.Contains(reservation.CompletedIDs, resourceID) {
			reservation.CompletedIDs = append(reservation.CompletedIDs, resourceID)
			slices.Sort(reservation.CompletedIDs)
		}
		if len(reservation.CompletedIDs) == len(reservation.ResourceIDs) && !reservation.Released {
			if err := service.releaseReservationCounters(reservation, state); err != nil {
				return err
			}
		}
		reservation.Digest = reservationDigest(*reservation)
		return nil
	})
}

// Release decrements both hierarchy counters exactly once after terminal proof.
func (service *DurableQuotaService) Release(ctx context.Context, capability Capability, reservationID, operationID string) error {
	return service.updateReservation(ctx, capability, reservationID, operationID, "release", func(reservation *Reservation, snapshot *Snapshot) error {
		if err := service.releaseReservationCounters(reservation, snapshot); err != nil {
			return err
		}
		reservation.Digest = reservationDigest(*reservation)
		return nil
	})
}

// releaseReservationCounters performs the shared exactly-once hierarchical decrement.
func (service *DurableQuotaService) releaseReservationCounters(reservation *Reservation, snapshot *Snapshot) error {
	if reservation.Released {
		return nil
	}
	for _, key := range reservation.CounterKeys {
		counter := snapshot.Quotas[key]
		if counter.Reserved < reservation.Units {
			return failure(CategoryIntegrity, "quota_release_durable", nil)
		}
		counter.Reserved -= reservation.Units
		snapshot.Quotas[key] = counter
	}
	reservation.Released, reservation.Uncertain, reservation.ReleasedAt = true, false, service.clock.Now().UTC()
	return nil
}

// ReleaseExpired releases only a known reservation whose frozen TTL elapsed and never frees uncertainty.
func (service *DurableQuotaService) ReleaseExpired(ctx context.Context, capability Capability, reservationID, operationID string) error {
	return service.updateReservation(ctx, capability, reservationID, operationID, "expiry", func(reservation *Reservation, snapshot *Snapshot) error {
		if reservation.Uncertain || reservation.ExpiresAt.IsZero() || service.clock.Now().Before(reservation.ExpiresAt) {
			return failure(CategoryConflict, "quota_expiry", nil)
		}
		if reservation.Released {
			return nil
		}
		for _, key := range reservation.CounterKeys {
			counter := snapshot.Quotas[key]
			if counter.Reserved < reservation.Units {
				return failure(CategoryIntegrity, "quota_expiry", nil)
			}
			counter.Reserved -= reservation.Units
			snapshot.Quotas[key] = counter
		}
		reservation.Released, reservation.ReleasedAt = true, service.clock.Now().UTC()
		reservation.Digest = reservationDigest(*reservation)
		return nil
	})
}

// updateReservation persists a state transition with an independent idempotency receipt.
func (service *DurableQuotaService) updateReservation(ctx context.Context, capability Capability, reservationID, operationID, kind string, mutate func(*Reservation, *Snapshot) error) error {
	if service == nil || ValidateID(reservationID) != nil || ValidateID(operationID) != nil || capability.Action != ActionQuotaReserve || capability.Resource != ResourceQuota {
		return failure(CategoryInvalid, "quota_update", nil)
	}
	action := SensitiveAction{Capability: capability, OperationID: operationID, CommandDigest: digestStrings("quota-"+kind, capability.Digest, reservationID), Outcome: "committed", ResourceID: reservationID}
	for attempt := 0; attempt < MaxItems; attempt++ {
		_, err := service.executor.Apply(ctx, action, func(snapshot *Snapshot) error {
			key := quotaReservationKey(capability.Scope, reservationID)
			reservation, ok := snapshot.Reservations[key]
			if !ok || reservation.Scope.OrganizationID != capability.Scope.OrganizationID || reservation.Scope.ProjectID != capability.Scope.ProjectID {
				return failure(CategoryNotFoundHidden, "quota_update", nil)
			}
			if err := mutate(&reservation, snapshot); err != nil {
				return err
			}
			snapshot.Reservations[key] = reservation
			return nil
		})
		if err == nil || !IsCategory(err, CategoryStale) {
			return err
		}
	}
	return failure(CategoryUnavailable, "quota_update", nil)
}

// EffectStatus distinguishes terminal failure from an outcome that still requires reconciliation.
type EffectStatus string

const (
	// EffectCommitted confirms the consumer effect happened and may be finalized.
	EffectCommitted EffectStatus = "committed"
	// EffectRejected confirms no consumer effect happened and capacity may be released.
	EffectRejected EffectStatus = "rejected"
	// EffectUnknown preserves capacity and intent until consumer observation reconciles it.
	EffectUnknown EffectStatus = "unknown"
)

// QuotaEffect is a consumer-owned boundary invoked only after its durable reservation exists.
type QuotaEffect interface {
	Execute(context.Context, Reservation) (EffectStatus, error)
}

// QuotaGuard causally orders quota reservation before queue, model, environment and storage effects.
type QuotaGuard struct{ quotas *DurableQuotaService }

// NewQuotaGuard rejects composition without the mandatory pre-effect quota service.
func NewQuotaGuard(quotas *DurableQuotaService) (*QuotaGuard, error) {
	if quotas == nil {
		return nil, failure(CategoryInvalid, "quota_guard_new", nil)
	}
	return &QuotaGuard{quotas: quotas}, nil
}

// Execute reserves first, invokes the consumer once, and retains uncertain capacity for reconciliation.
func (guard *QuotaGuard) Execute(ctx context.Context, request QuotaRequest, effect QuotaEffect) (Reservation, error) {
	if guard == nil || effect == nil {
		return Reservation{}, failure(CategoryInvalid, "quota_guard_execute", nil)
	}
	reservation, err := guard.quotas.Reserve(ctx, request)
	if err != nil {
		return Reservation{}, err
	}
	status, effectErr := effect.Execute(ctx, reservation)
	switch status {
	case EffectUnknown:
		markErr := guard.quotas.MarkUncertain(ctx, request.Capability, reservation.ID, request.OperationID+"-unknown")
		if markErr != nil {
			return reservation, markErr
		}
	case EffectRejected:
		if releaseErr := guard.quotas.Release(ctx, request.Capability, reservation.ID, request.OperationID+"-release"); releaseErr != nil {
			return reservation, releaseErr
		}
	case EffectCommitted:
	default:
		return reservation, failure(CategoryIntegrity, "quota_guard_execute", effectErr)
	}
	return reservation, effectErr
}

// QuotaScopedRepository meters object count and storage bytes before a scoped record is persisted.
type QuotaScopedRepository struct {
	next  *ScopedRepository
	guard *QuotaGuard
}

// NewQuotaScopedRepository rejects object-store composition that omits durable quota admission.
func NewQuotaScopedRepository(next *ScopedRepository, guard *QuotaGuard) (*QuotaScopedRepository, error) {
	if next == nil || guard == nil {
		return nil, failure(CategoryInvalid, "quota_repository_new", nil)
	}
	return &QuotaScopedRepository{next: next, guard: guard}, nil
}

// Put reserves storage and object dimensions before invoking the real scoped repository mutation.
func (repository *QuotaScopedRepository) Put(ctx context.Context, quotaCapability, recordCapability Capability, operationID string, class ScopedEntityClass, id, payloadDigest string, expectedVersion, storageBytes uint64, policy QuotaPolicySnapshot) (ScopedRecord, StoreReceipt, error) {
	if repository == nil || storageBytes == 0 {
		return ScopedRecord{}, StoreReceipt{}, failure(CategoryInvalid, "quota_repository_put", nil)
	}
	objectEffect := &quotaRepositoryPutEffect{repository: repository.next, capability: recordCapability, operationID: operationID, class: class, id: id, payloadDigest: payloadDigest, expectedVersion: expectedVersion}
	objectRequest := QuotaRequest{Capability: quotaCapability, OperationID: quotaDerivedOperation("object", operationID), Dimension: QuotaObject, Units: 1, Policy: policy}
	storageEffect := &quotaNestedEffect{guard: repository.guard, request: objectRequest, next: objectEffect}
	storageRequest := QuotaRequest{Capability: quotaCapability, OperationID: quotaDerivedOperation("storage", operationID), Dimension: QuotaStorageByte, Units: storageBytes, Policy: policy}
	_, err := repository.guard.Execute(ctx, storageRequest, storageEffect)
	return objectEffect.record, objectEffect.receipt, err
}

// quotaRepositoryPutEffect adapts one real repository mutation to the pre-effect quota protocol.
type quotaRepositoryPutEffect struct {
	repository      *ScopedRepository
	capability      Capability
	operationID     string
	class           ScopedEntityClass
	id              string
	payloadDigest   string
	expectedVersion uint64
	record          ScopedRecord
	receipt         StoreReceipt
	status          EffectStatus
}

// Execute invokes the object consumer once and classifies uncertainty conservatively.
func (effect *quotaRepositoryPutEffect) Execute(ctx context.Context, _ Reservation) (EffectStatus, error) {
	record, receipt, err := effect.repository.Put(ctx, effect.capability, effect.operationID, effect.class, effect.id, effect.payloadDigest, effect.expectedVersion)
	effect.record, effect.receipt, effect.status = record, receipt, quotaEffectStatus(err)
	return effect.status, err
}

// quotaNestedEffect composes multiple dimensions while compensating a later known denial before I/O.
type quotaNestedEffect struct {
	guard   *QuotaGuard
	request QuotaRequest
	next    *quotaRepositoryPutEffect
}

// Execute reserves the inner object dimension before delegating the repository effect.
func (effect *quotaNestedEffect) Execute(ctx context.Context, _ Reservation) (EffectStatus, error) {
	_, err := effect.guard.Execute(ctx, effect.request, effect.next)
	if err == nil {
		return EffectCommitted, nil
	}
	if effect.next.status == EffectUnknown {
		return EffectUnknown, err
	}
	return EffectRejected, err
}

// quotaEffectStatus releases only failures proven not to have crossed an external effect boundary.
func quotaEffectStatus(err error) EffectStatus {
	if err == nil {
		return EffectCommitted
	}
	for _, category := range []Category{CategoryInvalid, CategoryUnauthenticated, CategoryUnauthorized, CategoryNotFoundHidden, CategoryConflict, CategoryStale, CategoryRevoked, CategoryRateLimited, CategoryQuotaExhausted, CategoryIntegrity, CategoryExhausted} {
		if IsCategory(err, category) {
			return EffectRejected
		}
	}
	return EffectUnknown
}

// quotaDerivedOperation creates a bounded stable reservation identity without reusing the consumer receipt key.
func quotaDerivedOperation(kind, operationID string) string {
	return kind + "-" + shortDigest(operationID)[:40]
}

const (
	// QuotaQueueClaim meters durable queue claims before workers acquire leases.
	QuotaQueueClaim = "queue_claim"
	// QuotaWorker meters concurrent worker execution.
	QuotaWorker = "worker"
	// QuotaModelCall meters model-provider calls independently from token volume.
	QuotaModelCall = "model_call"
	// QuotaModelToken meters provider-neutral input and output token budgets.
	QuotaModelToken = "model_token"
	// QuotaEnvironment meters environment acquisition before provider calls.
	QuotaEnvironment = "environment"
	// QuotaInfrastructure meters underlying compute capacity reservations.
	QuotaInfrastructure = "infrastructure"
	// QuotaObject meters tenant-owned object creation.
	QuotaObject = "object"
	// QuotaStorageByte meters durable storage volume in integer bytes.
	QuotaStorageByte = "storage_byte"
)

// validQuotaLimit rejects zero windows and arithmetic overflow at policy load time.
func validQuotaLimit(limit QuotaLimit) bool {
	_, overflow := addUint64(limit.Limit, limit.Burst)
	return limit.Limit > 0 && limit.Limit <= MaxDurableCounter && limit.Burst <= MaxDurableCounter && limit.Window > 0 && !overflow && limit.Limit+limit.Burst <= MaxDurableCounter
}

// exceedsQuotaLimit reports whether a project ceiling weakens its organization ceiling.
func exceedsQuotaLimit(project, tenant QuotaLimit) bool {
	projectCeiling, projectOverflow := addUint64(project.Limit, project.Burst)
	tenantCeiling, tenantOverflow := addUint64(tenant.Limit, tenant.Burst)
	return projectOverflow || tenantOverflow || projectCeiling > tenantCeiling
}

// addUint64 provides explicit fail-closed overflow detection for all durable counters.
func addUint64(left, right uint64) (uint64, bool) {
	if left > math.MaxUint64-right {
		return 0, true
	}
	return left + right, false
}

// quotaCounterKey isolates tenant and project counters while sharing a window identity.
func quotaCounterKey(organizationID, projectID, dimension string, windowStart time.Time) string {
	level := "tenant"
	if projectID != "" {
		level = "project/" + projectID
	}
	return organizationID + "/" + level + "/" + dimension + "/" + windowStart.UTC().Format(time.RFC3339Nano)
}

// quotaReservationKey scopes an idempotency key to exactly one project.
func quotaReservationKey(scope TenantScope, operationID string) string {
	return scope.OrganizationID + "/" + scope.ProjectID + "/" + operationID
}

// reservationDigest seals hierarchy counters and lifecycle flags against storage corruption.
func reservationDigest(reservation Reservation) string {
	return digestStrings(reservation.ID, reservation.Scope.Digest, reservation.Dimension, fmt.Sprint(reservation.Units), reservation.WindowStart.UTC().Format(time.RFC3339Nano), reservation.ExpiresAt.UTC().Format(time.RFC3339Nano), fmt.Sprint(reservation.PolicyVersion), reservation.PolicyDigest, reservation.DemandDigest, fmt.Sprint(reservation.CounterKeys), fmt.Sprint(reservation.ResourceIDs), fmt.Sprint(reservation.CompletedIDs), fmt.Sprint(reservation.Released), fmt.Sprint(reservation.Uncertain), reservation.ReleasedAt.UTC().Format(time.RFC3339Nano))
}
