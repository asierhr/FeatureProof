package postgres

import (
	"context"
	"time"

	"github.com/asierhr/featureProof/internal/contextassembly"
	"github.com/asierhr/featureProof/internal/environment"
	cleanup "github.com/asierhr/featureProof/internal/environment/cleanup"
	"github.com/asierhr/featureProof/internal/functionalflow"
	"github.com/asierhr/featureProof/internal/planapproval"
)

// DomainStores is the PostgreSQL-only composition of every consumer-owned durable adapter needed by distributed runs.
type DomainStores struct {
	Workflow       *PostgresWorkflowStore
	PlanArtifacts  *PostgresPlanArtifactStore
	ExecutablePlan *PostgresExecutableStore
	Approval       *planapproval.PostgresApprovalStore
	Functional     *PostgresFunctionalStore
	Environment    *PostgresEnvironmentStore
	Capacity       *PostgresCapacityAdmission
	Cleanup        *PostgresCleanupStore
	CleanupLedger  *PostgresCleanupCapacityLedger
	Correction     *PostgresCorrectionStore
	Fixtures       *PostgresFixtureStore
	Context        contextassembly.Store
	Packages       *Store
	clock          functionalflow.Clock
}

// DomainStoreConfig supplies the explicit policies adapters need without reading process configuration themselves.
type DomainStoreConfig struct {
	Clock          functionalflow.Clock
	FunctionalTTL  time.Duration
	CapacityLimits map[string]environment.CapacityLimits
}

// DomainStores constructs one backend-homogeneous adapter graph and rejects partial PostgreSQL composition.
func (store *Store) DomainStores(config DomainStoreConfig) (*DomainStores, error) {
	if store == nil || config.Clock == nil || config.FunctionalTTL <= 0 || len(config.CapacityLimits) == 0 {
		return nil, cleanupFailure(cleanup.CategoryInvalid, "domain_compose")
	}
	approval, err := store.ApprovalStore()
	if err != nil {
		return nil, err
	}
	functional, err := store.FunctionalStore(config.Clock, config.FunctionalTTL)
	if err != nil {
		return nil, err
	}
	capacity, err := store.CapacityAdmission(config.CapacityLimits)
	if err != nil {
		return nil, err
	}
	return &DomainStores{Workflow: store.WorkflowStore(), PlanArtifacts: store.PlanArtifactStore(), ExecutablePlan: store.ExecutablePlanStore(), Approval: approval, Functional: functional, Environment: store.EnvironmentStore(), Capacity: capacity, Cleanup: store.CleanupStore(), CleanupLedger: store.CleanupCapacityLedger(), Correction: store.CorrectionStore(), Fixtures: store.FixtureStore(), Context: store.ContextStore(), Packages: store, clock: config.Clock}, nil
}

// Ready verifies that every required adapter belongs to the selected graph before a worker accepts product work.
func (stores *DomainStores) Ready(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if stores == nil || stores.Workflow == nil || stores.PlanArtifacts == nil || stores.ExecutablePlan == nil || stores.Approval == nil || stores.Functional == nil || stores.Environment == nil || stores.Capacity == nil || stores.Cleanup == nil || stores.CleanupLedger == nil || stores.Correction == nil || stores.Fixtures == nil || stores.Context == nil || stores.Packages == nil || stores.clock == nil {
		return cleanupFailure(cleanup.CategoryInvalid, "domain_ready")
	}
	return nil
}
