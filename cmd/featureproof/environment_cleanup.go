package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"path/filepath"
	"sync"
	"time"

	"github.com/asierhr/featureProof/internal/environment"
	environmentcleanup "github.com/asierhr/featureProof/internal/environment/cleanup"
	"github.com/asierhr/featureProof/internal/validationworkflow"
)

// workflowCleanupRuntimeConfig contains every local controller budget under product composition authority.
type workflowCleanupRuntimeConfig struct {
	PageSize, MaxPages, MaxItems, MaxClaims, MaxAttempts int
	Interval, LeaseTTL, ProviderTimeout                  time.Duration
	MaxBackoff, MaxCumulativeWait, MaxCycleDuration      time.Duration
	OrphanGrace                                          time.Duration
	Owner                                                string
}

// controllerConfig translates trusted workflow cleanup limits without request or model input.
func (c workflowCleanupRuntimeConfig) controllerConfig() environmentcleanup.Config {
	return environmentcleanup.Config{PageSize: c.PageSize, MaxPages: c.MaxPages, MaxItems: c.MaxItems, MaxClaims: c.MaxClaims, MaxAttemptsPerItem: c.MaxAttempts, Interval: c.Interval, LeaseTTL: c.LeaseTTL, ProviderTimeout: c.ProviderTimeout, MaxBackoff: c.MaxBackoff, MaxCumulativeWait: c.MaxCumulativeWait, MaxCycleDuration: c.MaxCycleDuration, OrphanGrace: c.OrphanGrace, Owner: c.Owner}
}

// workflowDurableCapacity adapts the single cleanup ledger to lifecycle admission while deferring physical release to the cleanup controller.
type workflowDurableCapacity struct {
	ledger   *environmentcleanup.CapacityLedger
	policies map[string]environmentcleanup.CapacityPolicy
	// afterBind injects response loss only at the productive post-commit boundary exercised by composition tests.
	afterBind func() error
}

// Reserve persists one shared receipt before lifecycle provider I/O.
func (a *workflowDurableCapacity) Reserve(ctx context.Context, request environment.CapacityRequest) (environment.CapacityReceipt, error) {
	policy, ok := a.policies[request.PolicyDigest]
	if !ok || policy.ProjectID != request.ProjectID {
		return environment.CapacityReceipt{}, errors.New("capacity policy unavailable")
	}
	receipt, _, err := a.ledger.Reserve(ctx, environmentcleanup.CapacityRequest{ProjectID: request.ProjectID, RunID: request.RunID, AcquisitionKey: request.AcquisitionKey, SelectionDigest: request.SelectionDigest, Demand: environmentcleanup.ResourceDemand{Environments: request.Demand.Environments, CPUMilli: request.Demand.CPUMilli, MemoryBytes: request.Demand.MemoryBytes, ObjectCount: request.Demand.ObjectCount}, TTL: request.TTL, Policy: policy})
	if err != nil {
		return environment.CapacityReceipt{}, err
	}
	return environment.CapacityReceipt{ID: receipt.ID, Digest: receipt.Digest}, nil
}

// Confirm binds the shared receipt to the exact lifecycle provider identity.
func (a *workflowDurableCapacity) Confirm(ctx context.Context, receipt environment.CapacityReceipt, environmentDigest, resourceDigest string) (environment.CapacityReceipt, error) {
	current, err := a.ledger.Receipt(ctx, receipt.ID)
	if err != nil || current.Digest != receipt.Digest {
		return environment.CapacityReceipt{}, errors.New("capacity receipt conflict")
	}
	confirmed, err := a.ledger.ConfirmEnvironment(ctx, current, environmentDigest, resourceDigest)
	if err != nil {
		return environment.CapacityReceipt{}, err
	}
	return environment.CapacityReceipt{ID: confirmed.ID, Digest: confirmed.Digest}, nil
}

// BindLifecycle records the durable environment-store handoff before provider I/O is authorized.
func (a *workflowDurableCapacity) BindLifecycle(ctx context.Context, receipt environment.CapacityReceipt) (environment.CapacityReceipt, error) {
	current, err := a.ledger.Receipt(ctx, receipt.ID)
	if err != nil || current.Digest != receipt.Digest {
		return environment.CapacityReceipt{}, errors.New("capacity receipt conflict")
	}
	bound, err := a.ledger.BindLifecycle(ctx, current)
	if err != nil {
		return environment.CapacityReceipt{}, err
	}
	if a.afterBind != nil {
		if err = a.afterBind(); err != nil {
			return environment.CapacityReceipt{}, err
		}
	}
	return environment.CapacityReceipt{ID: bound.ID, Digest: bound.Digest}, nil
}

// Release validates lifecycle absence proof but deliberately leaves the shared receipt reserved for controller confirmation.
func (a *workflowDurableCapacity) Release(ctx context.Context, receipt environment.CapacityReceipt, cleanupID, confirmation string) error {
	current, err := a.ledger.Receipt(ctx, receipt.ID)
	if err != nil || current.Digest != receipt.Digest || confirmation != workflowCapacityConfirmation(cleanupID, current.EnvironmentDigest, current.ResourceIdentityDigest, current.ID) {
		return errors.New("capacity release confirmation conflict")
	}
	return nil
}

// workflowCapacityConfirmation mirrors the lifecycle proof seal without exposing raw provider identity.
func workflowCapacityConfirmation(cleanupID, environmentDigest, resourceDigest, receiptID string) string {
	hash := sha256.New()
	for _, part := range []string{"capacity-release", cleanupID, environmentDigest, resourceDigest, receiptID} {
		_, _ = hash.Write([]byte(part))
		_, _ = hash.Write([]byte{0})
	}
	return hex.EncodeToString(hash.Sum(nil))
}

// workflowEnvironmentRuntime owns one durable lifecycle/cleanup composition but starts no goroutine until Start is called.
type workflowEnvironmentRuntime struct {
	root, project string
	config        workflowCleanupRuntimeConfig
	ledger        *environmentcleanup.CapacityLedger
	// capacity retains the shared adapter so composition tests can exercise its post-commit crash boundary.
	capacity    *workflowDurableCapacity
	bridge      *environment.CleanupBridge
	controller  *environmentcleanup.Controller
	inventories map[string]environmentcleanup.ManagedInventory
	port        validationworkflow.EnvironmentPort
	mu          sync.Mutex
	running     bool
}

// workflowEnvironmentRuntimes guarantees one in-process durable authority per absolute root and project.
var workflowEnvironmentRuntimes = struct {
	sync.Mutex
	values map[string]*workflowEnvironmentRuntime
}{values: map[string]*workflowEnvironmentRuntime{}}

// workflowEnvironmentRuntimeKey canonicalizes process-local runtime ownership.
func workflowEnvironmentRuntimeKey(root, project string) (string, error) {
	absolute, err := filepath.Abs(root)
	if err != nil || root == "" || project == "" {
		return "", errors.New("cleanup runtime root unavailable")
	}
	return absolute + "\x00" + project, nil
}

// forgetWorkflowEnvironmentRuntime releases only process-local ownership so restart tests can prove disk reopen semantics.
func forgetWorkflowEnvironmentRuntime(root, project string) {
	key, err := workflowEnvironmentRuntimeKey(root, project)
	if err != nil {
		return
	}
	workflowEnvironmentRuntimes.Lock()
	delete(workflowEnvironmentRuntimes.values, key)
	workflowEnvironmentRuntimes.Unlock()
}

// workflowOrphanAuthority combines independent lifecycle and workflow absence stores without treating transport failures as absence.
type workflowOrphanAuthority struct {
	lifecycle *environment.CleanupBridge
	runs      validationworkflow.CleanupIntentSource
}

// workflowCapacitySource exposes the joint lifecycle/receipt index under an independent durable discovery cursor.
type workflowCapacitySource struct {
	ledger    *environmentcleanup.CapacityLedger
	lifecycle *environment.CleanupBridge
}

// ScanCleanupCandidates resolves only receipt-only pre-provider crash windows and advances its independent durable cursor.
func (s workflowCapacitySource) ScanCleanupCandidates(ctx context.Context, cursor string, limit int) (environmentcleanup.Page, error) {
	if s.ledger == nil || s.lifecycle == nil {
		return environmentcleanup.Page{}, errors.New("capacity discovery unavailable")
	}
	receipts, next, err := s.ledger.ScanReceipts(ctx, cursor, limit)
	if err != nil {
		return environmentcleanup.Page{}, err
	}
	for _, receipt := range receipts {
		if receipt.Released || receipt.LifecycleBound || receipt.EnvironmentDigest != "" {
			continue
		}
		exists, lookupErr := s.lifecycle.HasLifecycleReservation(ctx, receipt.ProjectID, receipt.RunID, receipt.AcquisitionKey)
		if lookupErr != nil {
			return environmentcleanup.Page{}, lookupErr
		}
		if exists {
			continue
		}
		if _, _, releaseErr := s.ledger.ReleaseUnbound(ctx, receipt, environmentcleanup.ReceiptOnlyResolutionDigest(receipt)); releaseErr != nil && !environmentcleanup.IsCategory(releaseErr, environmentcleanup.CategoryConflict) {
			return environmentcleanup.Page{}, releaseErr
		}
	}
	return environmentcleanup.Page{Next: next, Scanned: true}, nil
}

// LifecycleAbsent delegates exact binding absence to the private lifecycle bridge.
func (a workflowOrphanAuthority) LifecycleAbsent(ctx context.Context, binding environmentcleanup.LifecycleCleanupBinding) (bool, error) {
	return a.lifecycle.LifecycleAbsent(ctx, binding)
}

// RunAbsent delegates run absence to the real workflow store projection.
func (a workflowOrphanAuthority) RunAbsent(ctx context.Context, project, runID string) (bool, error) {
	return a.runs.RunAbsent(ctx, project, runID)
}

// attachWorkflow connects the real workflow intent source, lifecycle source and shared ledger after all stores exist.
func (r *workflowEnvironmentRuntime) attachWorkflow(store validationworkflow.WorkflowStore, clock interface {
	environmentcleanup.Clock
	environmentcleanup.Waiter
}) error {
	if r == nil || store == nil || clock == nil || r.bridge == nil || r.ledger == nil {
		return errors.New("cleanup runtime dependencies unavailable")
	}
	r.mu.Lock()
	alreadyAttached := r.controller != nil
	r.mu.Unlock()
	if alreadyAttached {
		return nil
	}
	lifecycle, err := environmentcleanup.NewLifecycleAdapter(r.bridge)
	if err != nil {
		return err
	}
	lifecycleSource, err := environmentcleanup.NewLifecycleIndexSource(r.bridge, clock)
	if err != nil {
		return err
	}
	intents := validationworkflow.CleanupIntentSource{Store: store, Authority: r.bridge, Clock: clock}
	cleanupStore, err := environmentcleanup.OpenLocalStore(filepath.Join(r.root, ".featureproof", "cleanup", "items.json"))
	if err != nil {
		return err
	}
	r.controller, err = environmentcleanup.NewController(r.config.controllerConfig(), environmentcleanup.Dependencies{Store: cleanupStore, Intents: intents, Sources: map[string]environmentcleanup.DiscoverySource{"lifecycle": lifecycleSource, "capacity": workflowCapacitySource{ledger: r.ledger, lifecycle: r.bridge}}, Lifecycle: lifecycle, Inventories: r.inventories, Orphans: workflowOrphanAuthority{lifecycle: r.bridge, runs: intents}, Ledger: r.ledger, Clock: clock, Waiter: clock})
	return err
}

// Start creates the sole caller-owned controller goroutine and returns its cancel/join handle.
func (r *workflowEnvironmentRuntime) Start(parent context.Context) (validationworkflow.RuntimeHandle, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.controller == nil || r.running {
		return nil, errors.New("cleanup runtime unavailable")
	}
	ctx, cancel := context.WithCancel(parent)
	handle := &workflowCleanupHandle{cancel: cancel, done: make(chan error, 1)}
	r.running = true
	go func() {
		handle.done <- r.controller.Run(ctx)
		close(handle.done)
		r.mu.Lock()
		r.running = false
		r.mu.Unlock()
	}()
	return handle, nil
}

// workflowCleanupHandle exposes explicit cancellation and join ownership to the entrypoint.
type workflowCleanupHandle struct {
	cancel context.CancelFunc
	done   chan error
	once   sync.Once
}

// Cancel requests coordinated controller shutdown exactly once.
func (h *workflowCleanupHandle) Cancel() { h.once.Do(h.cancel) }

// Join waits for controller termination and treats caller cancellation as successful shutdown.
func (h *workflowCleanupHandle) Join() error {
	err := <-h.done
	if errors.Is(err, context.Canceled) {
		return nil
	}
	return err
}

// composeWorkflowCleanupController remains the narrow explicit constructor used by focused controller tests.
func composeWorkflowCleanupController(root string, config workflowCleanupRuntimeConfig, dependencies environmentcleanup.Dependencies) (*environmentcleanup.Controller, error) {
	if root == "" || dependencies.Lifecycle == nil || dependencies.Clock == nil || dependencies.Waiter == nil {
		return nil, errors.New("environment cleanup composition unavailable")
	}
	store, err := environmentcleanup.OpenLocalStore(filepath.Join(root, ".featureproof", "cleanup", "items.json"))
	if err != nil {
		return nil, err
	}
	ledger, err := environmentcleanup.OpenCapacityLedger(filepath.Join(root, ".featureproof", "cleanup", "capacity.json"))
	if err != nil {
		return nil, err
	}
	dependencies.Store, dependencies.Ledger = store, ledger
	return environmentcleanup.NewController(config.controllerConfig(), dependencies)
}
