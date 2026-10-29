package contracttest

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/asierhr/featureProof/internal/environment"
)

// EnvironmentStore combines lifecycle CAS with cleanup discovery for the
// shared local/PostgreSQL persistence contract.
type EnvironmentStore interface {
	environment.CleanupLifecycleStore
}

// EnvironmentFactory reopens one durable lifecycle partition.
type EnvironmentFactory func() (EnvironmentStore, error)

// RunEnvironmentStore verifies idempotent reserve, reopen, transition/event
// CAS and stable cleanup discovery through consumer-owned ports.
func RunEnvironmentStore(ctx context.Context, factory EnvironmentFactory, runID, projectID string, now time.Time) error {
	if factory == nil || runID == "" || projectID == "" || now.IsZero() {
		return fmt.Errorf("environment contract configuration")
	}
	selection, err := environment.SealSelection(environment.Selection{Version: 1, Alias: "contract-env", ProviderID: "provider", ProviderVersion: "v1", Kind: environment.KindManaged, TemplateReference: "template", ProfileID: "profile", NetworkPolicyID: "network", Secretless: true, ProjectID: projectID, RepositoryID: "repository", Capabilities: []string{"http"}})
	if err != nil {
		return fmt.Errorf("selection: %w", err)
	}
	record := environment.Record{Version: 1, RunID: runID, ProjectID: projectID, RequestDigest: contractDigest("request"), AcquisitionKey: contractDigest("acquisition"), CommitOID: strings.Repeat("a", 40), Selection: selection, State: environment.StateSelected, Revision: 1, CreatedAt: now, TTL: time.Hour, ExpiresAt: now.Add(time.Hour), Demand: environment.ResourceDemand{Environments: 1, CPUMilli: 1, MemoryBytes: 1, ObjectCount: 1}, CapacityReceipt: environment.CapacityReceipt{ID: "contract-capacity", Digest: contractDigest("capacity")}}
	store, err := factory()
	if err != nil {
		return fmt.Errorf("open initial: %w", err)
	}
	record, created, err := store.Reserve(ctx, record)
	if err != nil || !created {
		return fmt.Errorf("reserve created=%v: %w", created, err)
	}
	reopened, err := factory()
	if err != nil {
		return fmt.Errorf("reopen: %w", err)
	}
	loaded, err := reopened.Load(ctx, runID)
	if err != nil || loaded.Revision != 1 {
		return fmt.Errorf("load revision=%d: %w", loaded.Revision, err)
	}
	loaded.State, loaded.ReleaseRequired = environment.StateAcquiring, true
	event, err := environment.SealStoreEvent(environment.Event{ID: contractDigest(runID + "-acquiring"), RunID: runID, SelectionDigest: selection.Digest, ProviderID: selection.ProviderID, EnvironmentDigest: contractDigest("environment"), State: environment.StateAcquiring, OccurredAt: now}, runID)
	if err != nil {
		return fmt.Errorf("event: %w", err)
	}
	loaded, err = reopened.Save(ctx, loaded, 1, event)
	if err != nil || loaded.Revision != 2 {
		return fmt.Errorf("save revision=%d: %w", loaded.Revision, err)
	}
	stale := loaded
	stale.RequestDigest = contractDigest("stale-request")
	if _, err = reopened.Save(ctx, stale, 1, event); err == nil {
		return fmt.Errorf("stale lifecycle revision accepted")
	}
	events, err := reopened.Events(ctx, runID)
	if err != nil || len(events) != 1 {
		return fmt.Errorf("events count=%d: %w", len(events), err)
	}
	page, _, err := reopened.ScanCleanupLifecycle(ctx, "", 10)
	if err != nil || len(page) != 1 || page[0].RunID != runID {
		return fmt.Errorf("cleanup scan count=%d: %w", len(page), err)
	}
	return nil
}
