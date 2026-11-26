package contracttest

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/asierhr/featureProof/internal/tenantsecurity"
)

// contractClock fixes capability, binding and queue timestamps across adapters.
type contractClock struct{ now time.Time }

// Now returns the shared contract instant.
func (clock contractClock) Now() time.Time { return clock.now }

// RunScopedRuntime exercises T7–T10 admission, records, queue and tenant isolation on one backend.
func RunScopedRuntime(ctx context.Context, factory Factory, suffix string) error {
	store, err := factory()
	if err != nil {
		return err
	}
	clock := contractClock{now: time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)}
	memory := tenantsecurity.NewMemoryStore()
	service, _ := tenantsecurity.NewService(memory, clock, tenantsecurity.DefaultLimits(), nil)
	capabilities := map[string]tenantsecurity.Capability{}
	for _, tenant := range []struct{ id, digest string }{{"a", "a"}, {"b", "b"}} {
		organizationID := "runtime-" + tenant.id + "-" + suffix
		organization := tenantsecurity.Organization{ID: organizationID, State: tenantsecurity.StateActive, Version: 1, SecurityRevision: 1, PolicyDigest: strings.Repeat(tenant.digest, 64)}
		project := tenantsecurity.ProjectRegistration{OrganizationID: organizationID, ProjectID: "shared-project", RepositoryID: "shared-repo", State: tenantsecurity.StateActive, Version: 1}
		principal := tenantsecurity.Principal{ID: "principal", Kind: tenantsecurity.PrincipalHuman, SubjectDigest: strings.Repeat(tenant.digest, 64), State: tenantsecurity.StateActive, Version: 1}
		membership := tenantsecurity.Membership{OrganizationID: organizationID, PrincipalID: principal.ID, RoleIDs: []string{"operator"}, ProjectIDs: []string{project.ProjectID}, State: tenantsecurity.StateActive, ValidFrom: clock.now.Add(-time.Hour), ValidUntil: clock.now.Add(time.Hour), Version: 1}
		role := tenantsecurity.Role{OrganizationID: organizationID, ID: "operator", Actions: []tenantsecurity.Action{tenantsecurity.ActionRunAdmit, tenantsecurity.ActionRunRead, tenantsecurity.ActionEvidenceRead, tenantsecurity.ActionQuotaReserve, tenantsecurity.ActionRetentionExecute}, Resources: []tenantsecurity.ResourceClass{tenantsecurity.ResourceRun, tenantsecurity.ResourceEvidence, tenantsecurity.ResourceQuota}, ProjectIDs: []string{project.ProjectID}, Version: 1}
		if err = service.Bootstrap(ctx, organization, project, principal, membership, []tenantsecurity.Role{role}); err != nil {
			return err
		}
		identity := tenantsecurity.TrustedResourceIdentity{OrganizationID: organizationID, ProjectID: project.ProjectID, RepositoryID: project.RepositoryID}
		principalContext := tenantsecurity.PrincipalContext{Principal: principal, Membership: membership, Organization: organization}
		capabilities[tenant.id+"-admit"], _ = service.Authorize(ctx, principalContext, tenantsecurity.ActionRunAdmit, tenantsecurity.ResourceRun, identity)
		capabilities[tenant.id+"-run"], _ = service.Authorize(ctx, principalContext, tenantsecurity.ActionRunRead, tenantsecurity.ResourceRun, identity)
		capabilities[tenant.id+"-evidence"], _ = service.Authorize(ctx, principalContext, tenantsecurity.ActionEvidenceRead, tenantsecurity.ResourceEvidence, identity)
		capabilities[tenant.id+"-quota"], _ = service.Authorize(ctx, principalContext, tenantsecurity.ActionQuotaReserve, tenantsecurity.ResourceQuota, identity)
		capabilities[tenant.id+"-retention"], _ = service.Authorize(ctx, principalContext, tenantsecurity.ActionRetentionExecute, tenantsecurity.ResourceEvidence, identity)
	}
	snapshot := memory.Snapshot()
	snapshot.Epoch = 1
	if _, err = store.Commit(ctx, tenantsecurity.StoreCommand{OrganizationID: capabilities["a-admit"].Scope.OrganizationID, OperationID: "runtime-bootstrap", CommandDigest: strings.Repeat("c", 64), ExpectedEpoch: 0, Snapshot: snapshot}); err != nil {
		return fmt.Errorf("runtime bootstrap: %w", err)
	}
	admission, _ := tenantsecurity.NewAdmission(store, clock)
	for _, tenant := range []string{"a", "b"} {
		coordinate := tenantsecurity.ConfigurationCoordinate{Generation: 1, VersionDigest: strings.Repeat("1", 64), PolicyDigest: capabilities[tenant+"-admit"].Scope.PolicyDigest, CatalogDigest: strings.Repeat("2", 64), EffectiveDigest: strings.Repeat("3", 64)}
		if _, _, err = admission.Admit(ctx, capabilities[tenant+"-admit"], "admit-"+tenant, "same-run", "plan", coordinate); err != nil {
			return fmt.Errorf("admit %s: %w", tenant, err)
		}
	}
	repository, _ := tenantsecurity.NewScopedRepository(store, clock)
	for _, tenant := range []string{"a", "b"} {
		if _, _, err = repository.Put(ctx, capabilities[tenant+"-evidence"], "package-"+tenant, tenantsecurity.EntityPackage, "same-package", strings.Repeat(tenant, 64), 0); err != nil {
			return fmt.Errorf("package %s: %w", tenant, err)
		}
	}
	page, err := repository.List(ctx, capabilities["a-evidence"], tenantsecurity.EntityPackage, "", 10)
	if err != nil || len(page.Records) != 1 || page.Records[0].OrganizationID != capabilities["a-evidence"].Scope.OrganizationID {
		return fmt.Errorf("tenant page isolation: %w", err)
	}
	queue, _ := tenantsecurity.NewQueue(store, clock)
	claimed, err := queue.Claim(ctx, capabilities["a-run"], "claim-a", "same-run-first", "worker-a", 1)
	if err != nil || claimed.Fence != 2 {
		return fmt.Errorf("claim: %w", err)
	}
	if _, err = queue.Complete(ctx, capabilities["a-run"], "complete-stale", "same-run-first", "worker-a", 1); !tenantsecurity.IsCategory(err, tenantsecurity.CategoryStale) {
		return fmt.Errorf("stale fence: %w", err)
	}
	bus, _ := tenantsecurity.NewMessageBus(store, clock)
	for _, tenant := range []string{"a", "b"} {
		if _, err = bus.Accept(ctx, capabilities[tenant+"-run"], "inbox-"+tenant, tenantsecurity.MessageInbox, "same-delivery", strings.Repeat("d", 64)); err != nil {
			return fmt.Errorf("inbox %s: %w", tenant, err)
		}
	}
	if _, err = bus.Accept(ctx, capabilities["a-run"], "inbox-a-divergent", tenantsecurity.MessageInbox, "same-delivery", strings.Repeat("e", 64)); !tenantsecurity.IsCategory(err, tenantsecurity.CategoryConflict) {
		return fmt.Errorf("divergent delivery: %w", err)
	}
	if claimedMessage, claimErr := bus.Claim(ctx, capabilities["a-run"], "inbox-claim", tenantsecurity.MessageInbox, "same-delivery", "worker-a", 1); claimErr != nil || claimedMessage.Fence != 2 {
		return fmt.Errorf("delivery claim: %w", claimErr)
	}
	quotas, err := tenantsecurity.NewDurableQuotaService(store, clock)
	if err != nil {
		return err
	}
	quotaPolicy := tenantsecurity.QuotaPolicySnapshot{Version: 1, Tenant: map[string]tenantsecurity.QuotaLimit{tenantsecurity.QuotaWorker: {Limit: 1, Window: time.Hour}}, Project: map[string]tenantsecurity.QuotaLimit{tenantsecurity.QuotaWorker: {Limit: 1, Window: time.Hour}}, TenantOutstanding: 2, ProjectOutstanding: 2, ReservationTTL: time.Hour}
	reservation, err := quotas.Reserve(ctx, tenantsecurity.QuotaRequest{Capability: capabilities["a-quota"], OperationID: "quota-contract", Dimension: tenantsecurity.QuotaWorker, Units: 1, Policy: quotaPolicy})
	if err != nil {
		return fmt.Errorf("quota reserve: %w", err)
	}
	if err = quotas.Release(ctx, capabilities["a-quota"], reservation.ID, "quota-contract-release"); err != nil {
		return fmt.Errorf("quota release: %w", err)
	}
	secondHandle, err := factory()
	if err != nil {
		return err
	}
	secondQuotas, err := tenantsecurity.NewDurableQuotaService(secondHandle, clock)
	if err != nil {
		return err
	}
	start := make(chan struct{})
	results := make(chan error, 2)
	var quotaWait sync.WaitGroup
	for index, quotaService := range []*tenantsecurity.DurableQuotaService{quotas, secondQuotas} {
		quotaWait.Add(1)
		go func(index int, quotaService *tenantsecurity.DurableQuotaService) {
			defer quotaWait.Done()
			<-start
			_, reserveErr := quotaService.Reserve(ctx, tenantsecurity.QuotaRequest{Capability: capabilities["a-quota"], OperationID: fmt.Sprintf("quota-last-%d", index), Dimension: tenantsecurity.QuotaWorker, Units: 1, Policy: quotaPolicy})
			results <- reserveErr
		}(index, quotaService)
	}
	close(start)
	quotaWait.Wait()
	close(results)
	successes := 0
	for reserveErr := range results {
		if reserveErr == nil {
			successes++
		} else if !tenantsecurity.IsCategory(reserveErr, tenantsecurity.CategoryQuotaExhausted) {
			return fmt.Errorf("quota last slot: %w", reserveErr)
		}
	}
	if successes != 1 {
		return fmt.Errorf("quota last slot successes=%d", successes)
	}
	floors, periods := map[tenantsecurity.RetentionClass]time.Duration{}, map[tenantsecurity.RetentionClass]time.Duration{}
	for _, class := range []tenantsecurity.RetentionClass{tenantsecurity.RetentionRun, tenantsecurity.RetentionPackage, tenantsecurity.RetentionEvidence, tenantsecurity.RetentionProject, tenantsecurity.RetentionSecret, tenantsecurity.RetentionAudit, tenantsecurity.RetentionTombstone} {
		floors[class], periods[class] = time.Hour, time.Hour
	}
	eligibility, err := tenantsecurity.EvaluateRetention(tenantsecurity.RetentionPolicy{OrganizationID: capabilities["a-retention"].Scope.OrganizationID, Version: 1, Floors: floors, Periods: periods, ProtectedRecords: map[tenantsecurity.RetentionClass][]string{}, BatchLimit: 2, GraphLimit: 2}, tenantsecurity.RetentionCandidate{ID: "retention-contract", Class: tenantsecurity.RetentionEvidence, TerminalAt: clock.now.Add(-2 * time.Hour)}, clock.now)
	if err != nil {
		return fmt.Errorf("retention eligibility: %w", err)
	}
	retentionConsumer := &contractRetentionConsumer{observation: tenantsecurity.DeletionObservation{Exists: false}}
	controller, err := tenantsecurity.NewRetentionController(store, clock, map[tenantsecurity.ResourceClass]tenantsecurity.RetentionConsumer{tenantsecurity.ResourceEvidence: retentionConsumer})
	if err != nil {
		return err
	}
	if _, err = controller.Plan(ctx, tenantsecurity.DeletionPlan{Capability: capabilities["a-retention"], OperationID: "retention-contract-plan", Eligibility: eligibility, Resource: tenantsecurity.ResourceEvidence}); err != nil {
		return fmt.Errorf("retention plan: %w", err)
	}
	if err = controller.Converge(ctx, capabilities["a-retention"], eligibility.CandidateID, "retention-contract-delete"); err != nil {
		return fmt.Errorf("retention converge: %w", err)
	}
	store, err = factory()
	if err != nil {
		return err
	}
	admission, _ = tenantsecurity.NewAdmission(store, clock)
	binding, err := admission.LoadBinding(ctx, capabilities["b-run"], "same-run")
	if err != nil || binding.Scope.OrganizationID != capabilities["b-run"].Scope.OrganizationID {
		return fmt.Errorf("restart binding: %w", err)
	}
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err = repository.Get(cancelled, capabilities["a-evidence"], tenantsecurity.EntityPackage, "same-package"); !tenantsecurity.IsCategory(err, tenantsecurity.CategoryCancelled) {
		return fmt.Errorf("cancellation: %w", err)
	}
	return nil
}

// contractRetentionConsumer confirms absence without crossing an external effect boundary.
type contractRetentionConsumer struct {
	observation tenantsecurity.DeletionObservation
}

// Observe returns the adapter-contract lifecycle observation.
func (consumer *contractRetentionConsumer) Observe(context.Context, tenantsecurity.DeletionCommand) (tenantsecurity.DeletionObservation, error) {
	return consumer.observation, nil
}

// Delete is present to satisfy the consumer contract but absence reconciliation never calls it.
func (*contractRetentionConsumer) Delete(context.Context, tenantsecurity.DeletionCommand) (tenantsecurity.EffectStatus, error) {
	return tenantsecurity.EffectCommitted, nil
}
