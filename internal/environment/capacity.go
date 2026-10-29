package environment

import (
	"context"
	"math"
	"sync"
	"time"
)

// StaticDemandResolver is explicit composition-owned demand used until project configuration is versioned.
type StaticDemandResolver struct {
	Demands       map[string]ResourceDemand
	TTLs          map[string]time.Duration
	PolicyDigests map[string]string
}

// Resolve selects only the exact sealed selection digest.
func (r StaticDemandResolver) Resolve(ctx context.Context, selection Selection) (ResourceDemand, time.Duration, string, error) {
	if err := ctx.Err(); err != nil {
		return ResourceDemand{}, 0, "", err
	}
	demand, ok := r.Demands[selection.Digest]
	ttl := r.TTLs[selection.Digest]
	policy := r.PolicyDigests[selection.Digest]
	if !ok || demand.Environments != 1 || ttl <= 0 || !digestPattern.MatchString(policy) {
		return ResourceDemand{}, 0, "", &Error{Category: CategoryUnauthorized, Operation: "capacity_demand"}
	}
	return demand, ttl, policy, nil
}

// CapacityLimits defines explicit per-project aggregate bounds.
type CapacityLimits struct {
	Environments, CPUMilli, MemoryBytes, ObjectCount uint64
	MaxTTL                                           time.Duration
}

// MemoryCapacityAdmission provides race-safe atomic admission for tests and single-process local composition.
type MemoryCapacityAdmission struct {
	mu       sync.Mutex
	limits   map[string]CapacityLimits
	receipts map[string]memoryCapacityReceipt
}

// memoryCapacityReceipt owns one exact reservation and its binding state.
type memoryCapacityReceipt struct {
	Request               CapacityRequest
	Receipt               CapacityReceipt
	Environment, Resource string
	Confirmation          string
	Released              bool
}

// NewMemoryCapacityAdmission validates explicit non-zero project limits.
func NewMemoryCapacityAdmission(limits map[string]CapacityLimits) (*MemoryCapacityAdmission, error) {
	if len(limits) == 0 {
		return nil, &Error{Category: CategoryInvalid, Operation: "capacity_limits"}
	}
	copyLimits := map[string]CapacityLimits{}
	for project, limit := range limits {
		if !safeID.MatchString(project) || limit.Environments == 0 || limit.CPUMilli == 0 || limit.MemoryBytes == 0 || limit.ObjectCount == 0 || limit.MaxTTL <= 0 {
			return nil, &Error{Category: CategoryInvalid, Operation: "capacity_limit"}
		}
		copyLimits[project] = limit
	}
	return &MemoryCapacityAdmission{limits: copyLimits, receipts: map[string]memoryCapacityReceipt{}}, nil
}

// Reserve atomically accounts demand before provider I/O and converges exact redelivery.
func (a *MemoryCapacityAdmission) Reserve(ctx context.Context, request CapacityRequest) (CapacityReceipt, error) {
	if err := ctx.Err(); err != nil {
		return CapacityReceipt{}, err
	}
	limit, ok := a.limits[request.ProjectID]
	if !ok || request.Demand.Environments != 1 || request.TTL <= 0 || request.TTL > limit.MaxTTL || !digestPattern.MatchString(request.PolicyDigest) {
		return CapacityReceipt{}, &Error{Category: CategoryUnauthorized, Operation: "capacity_reserve"}
	}
	id := stableKey("capacity", request.ProjectID, request.AcquisitionKey)
	digest := digestOf(request)
	a.mu.Lock()
	defer a.mu.Unlock()
	if current, exists := a.receipts[id]; exists {
		if digestOf(current.Request) != digest {
			return CapacityReceipt{}, &Error{Category: CategoryConflict, Operation: "capacity_redelivery"}
		}
		return current.Receipt, nil
	}
	total := ResourceDemand{}
	for _, current := range a.receipts {
		if current.Request.ProjectID == request.ProjectID && !current.Released {
			var overflow bool
			total, overflow = addResourceDemand(total, current.Request.Demand)
			if overflow {
				return CapacityReceipt{}, &Error{Category: CategoryIntegrity, Operation: "capacity_overflow"}
			}
		}
	}
	next, overflow := addResourceDemand(total, request.Demand)
	if overflow || next.Environments > limit.Environments || next.CPUMilli > limit.CPUMilli || next.MemoryBytes > limit.MemoryBytes || next.ObjectCount > limit.ObjectCount {
		return CapacityReceipt{}, &Error{Category: CategoryConflict, Operation: "capacity_limit"}
	}
	receipt := CapacityReceipt{ID: id, Digest: stableKey("capacity-receipt", id, digest)}
	a.receipts[id] = memoryCapacityReceipt{Request: request, Receipt: receipt}
	return receipt, nil
}

// Confirm binds a reservation to provider identity without releasing it.
func (a *MemoryCapacityAdmission) Confirm(ctx context.Context, receipt CapacityReceipt, environment, resource string) (CapacityReceipt, error) {
	if err := ctx.Err(); err != nil {
		return CapacityReceipt{}, err
	}
	if !ValidCapacityProviderIdentity(environment, resource) {
		return CapacityReceipt{}, &Error{Category: CategoryIntegrity, Operation: "capacity_confirm"}
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	current, ok := a.receipts[receipt.ID]
	if !ok || current.Receipt != receipt {
		return CapacityReceipt{}, &Error{Category: CategoryConflict, Operation: "capacity_confirm"}
	}
	if current.Environment != "" && (current.Environment != environment || current.Resource != resource) {
		return CapacityReceipt{}, &Error{Category: CategoryConflict, Operation: "capacity_confirm"}
	}
	current.Environment, current.Resource = environment, resource
	a.receipts[receipt.ID] = current
	return receipt, nil
}

// Release frees one confirmed receipt exactly once after cleanup confirmation.
func (a *MemoryCapacityAdmission) Release(ctx context.Context, receipt CapacityReceipt, cleanupID, confirmation string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if !ValidCapacityReleaseInput(cleanupID, confirmation) {
		return &Error{Category: CategoryIntegrity, Operation: "capacity_release"}
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	current, ok := a.receipts[receipt.ID]
	if !ok || current.Receipt != receipt || current.Environment == "" {
		return &Error{Category: CategoryConflict, Operation: "capacity_release"}
	}
	expected := capacityReleaseConfirmation(cleanupID, current.Environment, current.Resource, current.Receipt.ID)
	if confirmation != expected || current.Confirmation != "" && current.Confirmation != confirmation {
		return &Error{Category: CategoryConflict, Operation: "capacity_release_confirmation"}
	}
	current.Released, current.Confirmation = true, confirmation
	a.receipts[receipt.ID] = current
	return nil
}

// capacityReleaseConfirmation binds one absence proof to cleanup identity, provider identity, and receipt authority.
func capacityReleaseConfirmation(cleanupID, environment, resource, receiptID string) string {
	return stableKey("capacity-release", cleanupID, environment, resource, receiptID)
}

// CapacityReleaseConfirmation lets persistence adapters verify the same consumer-owned absence proof as the in-memory admission implementation.
func CapacityReleaseConfirmation(cleanupID, environment, resource, receiptID string) string {
	return capacityReleaseConfirmation(cleanupID, environment, resource, receiptID)
}

// ValidCapacityProviderIdentity applies the consumer-owned digest grammar at
// every in-memory or persistent admission boundary.
func ValidCapacityProviderIdentity(environment, resource string) bool {
	return digestPattern.MatchString(environment) && digestPattern.MatchString(resource)
}

// ValidCapacityReleaseInput applies the consumer-owned cleanup and proof
// grammar before a backend can mutate quota state.
func ValidCapacityReleaseInput(cleanupID, confirmation string) bool {
	return safeID.MatchString(cleanupID) && digestPattern.MatchString(confirmation)
}

// addResourceDemand performs overflow-safe vector addition.
func addResourceDemand(left, right ResourceDemand) (ResourceDemand, bool) {
	if math.MaxUint64-left.Environments < right.Environments || math.MaxUint64-left.CPUMilli < right.CPUMilli || math.MaxUint64-left.MemoryBytes < right.MemoryBytes || math.MaxUint64-left.ObjectCount < right.ObjectCount {
		return ResourceDemand{}, true
	}
	return ResourceDemand{Environments: left.Environments + right.Environments, CPUMilli: left.CPUMilli + right.CPUMilli, MemoryBytes: left.MemoryBytes + right.MemoryBytes, ObjectCount: left.ObjectCount + right.ObjectCount}, false
}
