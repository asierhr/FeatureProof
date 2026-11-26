package tenantsecurity

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"strings"
	"testing"
	"time"
)

// TestTenantPolicyCompilationAndTamperValidation exercises canonical ordering,
// defensive ownership and integrity validation at the project-policy boundary.
func TestTenantPolicyCompilationAndTamperValidation(t *testing.T) {
	policy := TenantPolicy{
		OrganizationID:       "org-policy",
		Version:              1,
		MinimumRetentionDays: 7,
		MaximumRetentionDays: 30,
		Allowed: map[string][]string{
			"provider": {"provider-b", "provider-a"},
			"contract": {"contract-a"},
		},
		AllowedCapabilities: []string{"tool-b", "tool-a"},
	}
	compiled, err := CompileTenantPolicy(policy, DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	policy.Allowed["provider"][0] = "mutated"
	policy.AllowedCapabilities[0] = "mutated"
	if compiled.Allowed["provider"][0] != "provider-a" || compiled.AllowedCapabilities[0] != "tool-a" {
		t.Fatal("compiled policy retained caller-owned slices")
	}
	if err = ValidateTenantPolicy(compiled, DefaultLimits()); err != nil {
		t.Fatal(err)
	}
	tampered := compiled
	tampered.MaximumRetentionDays++
	if err = ValidateTenantPolicy(tampered, DefaultLimits()); !IsCategory(err, CategoryIntegrity) {
		t.Fatalf("tampered policy category=%v", err)
	}
	for name, candidate := range map[string]TenantPolicy{
		"duplicate capability": {OrganizationID: "org-policy", Version: 1, Allowed: map[string][]string{}, AllowedCapabilities: []string{"same", "same"}},
		"unknown catalog":      {OrganizationID: "org-policy", Version: 1, Allowed: map[string][]string{"unknown": {"value"}}},
		"invalid item":         {OrganizationID: "org-policy", Version: 1, Allowed: map[string][]string{"provider": {"not/valid"}}},
	} {
		t.Run(name, func(t *testing.T) {
			if _, compileErr := CompileTenantPolicy(candidate, DefaultLimits()); !IsCategory(compileErr, CategoryInvalid) {
				t.Fatalf("category=%v", compileErr)
			}
		})
	}
}

// TestAdministrationMutationsRemainAtomicAndVersioned covers every closed
// relationship mutation through the same command+audit+receipt transaction.
func TestAdministrationMutationsRemainAtomicAndVersioned(t *testing.T) {
	for name, mutation := range map[string]func(*fakeClock, PrincipalContext) AdministrationMutation{
		"project": func(_ *fakeClock, _ PrincipalContext) AdministrationMutation {
			return AdministrationMutation{Kind: AdministrationProject, Project: &ProjectRegistration{OrganizationID: "org-a", ProjectID: "project-b", RepositoryID: "repo-b", State: StateActive, Version: 1}}
		},
		"role": func(_ *fakeClock, _ PrincipalContext) AdministrationMutation {
			return AdministrationMutation{Kind: AdministrationRole, Role: &Role{OrganizationID: "org-a", ID: "reader", Actions: []Action{ActionRunRead}, Resources: []ResourceClass{ResourceRun}, ProjectIDs: []string{"project-a"}, Version: 1}}
		},
		"membership": func(clock *fakeClock, _ PrincipalContext) AdministrationMutation {
			principal := Principal{ID: "principal-b", Kind: PrincipalHuman, SubjectDigest: digestStrings("issuer", "subject-b"), State: StateActive, Version: 1}
			membership := Membership{OrganizationID: "org-a", PrincipalID: principal.ID, RoleIDs: []string{"operator"}, ProjectIDs: []string{"project-a"}, State: StateActive, ValidFrom: clock.now.Add(-time.Minute), ValidUntil: clock.now.Add(time.Hour), Version: 1}
			return AdministrationMutation{Kind: AdministrationMembership, Principal: &principal, Membership: &membership}
		},
		"service identity": func(clock *fakeClock, principal PrincipalContext) AdministrationMutation {
			servicePrincipal := Principal{ID: "service-a", Kind: PrincipalService, SubjectDigest: digestStrings("worker", "service-a"), State: StateActive, Version: 1}
			identity := ServiceIdentity{OrganizationID: "org-a", PrincipalID: servicePrincipal.ID, OwnerPrincipalID: principal.Principal.ID, Purpose: "worker", ExpiresAt: clock.now.Add(time.Hour), State: StateActive, Version: 1}
			return AdministrationMutation{Kind: AdministrationServiceIdentity, Principal: &servicePrincipal, ServiceIdentity: &identity}
		},
	} {
		t.Run(name, func(t *testing.T) {
			service, memory, clock, principal, identity := fixture(t)
			capability, err := service.Authorize(context.Background(), principal, ActionTenantAdmin, ResourceTenant, identity)
			if err != nil {
				t.Fatal(err)
			}
			durable := &testDurable{snapshot: memory.Snapshot()}
			administration, err := NewAdministration(durable, clock, DefaultLimits())
			if err != nil {
				t.Fatal(err)
			}
			result, err := administration.Apply(context.Background(), capability, "admin-"+strings.ReplaceAll(name, " ", "-"), 0, mutation(clock, principal))
			if err != nil || result.Receipt.OperationID == "" || result.Audit.Sequence == 0 {
				t.Fatalf("result=%+v err=%v", result, err)
			}
			replayed, err := administration.Apply(context.Background(), capability, result.Receipt.OperationID, 0, mutation(clock, principal))
			if err != nil || replayed.Receipt.Digest != result.Receipt.Digest {
				t.Fatalf("replay=%+v err=%v", replayed, err)
			}
		})
	}
}

// TestScopedRuntimeVerticalCoversAdmissionRecordsQueueAndMessages proves that
// the full tenant scope and fences survive every durable runtime transition.
func TestScopedRuntimeVerticalCoversAdmissionRecordsQueueAndMessages(t *testing.T) {
	service, memory, clock, principal, identity := fixture(t)
	ctx := context.Background()
	admitCapability, _ := service.Authorize(ctx, principal, ActionRunAdmit, ResourceRun, identity)
	runCapability, _ := service.Authorize(ctx, principal, ActionRunRead, ResourceRun, identity)
	evidenceCapability, _ := service.Authorize(ctx, principal, ActionEvidenceRead, ResourceEvidence, identity)
	durable := &testDurable{snapshot: memory.Snapshot()}

	admission, err := NewAdmission(durable, clock)
	if err != nil {
		t.Fatal(err)
	}
	coordinate := ConfigurationCoordinate{Generation: 1, VersionDigest: strings.Repeat("1", 64), PolicyDigest: admitCapability.Scope.PolicyDigest, CatalogDigest: strings.Repeat("2", 64), EffectiveDigest: strings.Repeat("3", 64)}
	binding, receipt, err := admission.Admit(ctx, admitCapability, "admit-runtime", "run-runtime", "plan", coordinate)
	if err != nil || receipt.OperationID == "" || ValidateTenantRunBinding(binding) != nil {
		t.Fatalf("binding=%+v receipt=%+v err=%v", binding, receipt, err)
	}
	loaded, err := admission.LoadBinding(ctx, runCapability, binding.RunID)
	if err != nil || loaded.Digest != binding.Digest {
		t.Fatalf("loaded=%+v err=%v", loaded, err)
	}

	repository, _ := NewScopedRepository(durable, clock)
	record, _, err := repository.Put(ctx, evidenceCapability, "put-runtime-a", EntityEvidence, "evidence-a", strings.Repeat("a", 64), 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err = repository.Put(ctx, evidenceCapability, "put-runtime-b", EntityEvidence, "evidence-b", strings.Repeat("b", 64), 0); err != nil {
		t.Fatal(err)
	}
	got, err := repository.Get(ctx, evidenceCapability, EntityEvidence, record.ID)
	if err != nil || got.Digest != record.Digest {
		t.Fatalf("record=%+v err=%v", got, err)
	}
	page, err := repository.List(ctx, evidenceCapability, EntityEvidence, "", 1)
	if err != nil || len(page.Records) != 1 || page.Cursor == "" {
		t.Fatalf("page=%+v err=%v", page, err)
	}
	if _, err = repository.List(ctx, evidenceCapability, EntityEvidence, page.Cursor+"x", 1); !IsCategory(err, CategoryInvalid) {
		t.Fatalf("tampered cursor=%v", err)
	}

	queue, _ := NewQueue(durable, clock)
	claimed, err := queue.Claim(ctx, runCapability, "claim-runtime", "run-runtime-first", "worker-a", 1)
	if err != nil || claimed.Fence != 2 {
		t.Fatalf("claim=%+v err=%v", claimed, err)
	}
	completed, err := queue.Complete(ctx, runCapability, "complete-runtime", claimed.JobID, claimed.Owner, claimed.Fence)
	if err != nil || completed.State != "completed" {
		t.Fatalf("complete=%+v err=%v", completed, err)
	}

	bus, _ := NewMessageBus(durable, clock)
	message, err := bus.Accept(ctx, runCapability, "message-runtime", MessageInbox, "delivery-a", strings.Repeat("d", 64))
	if err != nil || message.State != "pending" {
		t.Fatalf("message=%+v err=%v", message, err)
	}
	claimedMessage, err := bus.Claim(ctx, runCapability, "message-claim", MessageInbox, message.IdempotencyKey, "worker-a", message.Fence)
	if err != nil || claimedMessage.Fence != 2 {
		t.Fatalf("message claim=%+v err=%v", claimedMessage, err)
	}
	if _, err = bus.Accept(ctx, runCapability, "message-divergent", MessageInbox, message.IdempotencyKey, strings.Repeat("e", 64)); !IsCategory(err, CategoryConflict) {
		t.Fatalf("divergent message=%v", err)
	}
	if _, err = bus.Claim(ctx, runCapability, "message-stale", MessageInbox, message.IdempotencyKey, "worker-b", 1); !IsCategory(err, CategoryStale) {
		t.Fatalf("stale message claim=%v", err)
	}
	if _, err = queue.Complete(ctx, runCapability, "complete-stale", completed.JobID, completed.Owner, 1); !IsCategory(err, CategoryStale) {
		t.Fatalf("stale queue completion=%v", err)
	}
	if _, _, err = repository.Put(ctx, evidenceCapability, "put-stale", EntityEvidence, record.ID, strings.Repeat("c", 64), 0); !IsCategory(err, CategoryStale) {
		t.Fatalf("stale record update=%v", err)
	}
	if _, err = repository.Get(ctx, evidenceCapability, EntityEvidence, "missing-evidence"); !IsCategory(err, CategoryNotFoundHidden) {
		t.Fatalf("missing record=%v", err)
	}
	if _, _, err = admission.Admit(ctx, admitCapability, "admit-runtime", "run-runtime", "execute", coordinate); !IsCategory(err, CategoryConflict) {
		t.Fatalf("divergent admission replay=%v", err)
	}

	snapshot, err := durable.Snapshot(ctx)
	if err != nil || ValidateSnapshot(snapshot) != nil {
		t.Fatalf("runtime snapshot invalid: %v", err)
	}
	for name, tamper := range map[string]func(*Snapshot){
		"binding": func(candidate *Snapshot) {
			value := candidate.Bindings[bindingKey(binding.Scope, binding.RunID)]
			value.Digest = strings.Repeat("f", 64)
			candidate.Bindings[bindingKey(binding.Scope, binding.RunID)] = value
		},
		"record": func(candidate *Snapshot) {
			key := recordKey(record.OrganizationID, record.ProjectID, record.Class, record.ID)
			value := candidate.Records[key]
			value.Digest = strings.Repeat("f", 64)
			candidate.Records[key] = value
		},
		"job": func(candidate *Snapshot) {
			key := jobKey(completed.OrganizationID, completed.ProjectID, completed.JobID)
			value := candidate.Jobs[key]
			value.Digest = strings.Repeat("f", 64)
			candidate.Jobs[key] = value
		},
		"message": func(candidate *Snapshot) {
			key := messageKey(claimedMessage.OrganizationID, claimedMessage.ProjectID, claimedMessage.Kind, claimedMessage.IdempotencyKey)
			value := candidate.Messages[key]
			value.Digest = strings.Repeat("f", 64)
			candidate.Messages[key] = value
		},
	} {
		t.Run("snapshot "+name, func(t *testing.T) {
			candidate, cloneErr := CloneSnapshot(snapshot)
			if cloneErr != nil {
				t.Fatal(cloneErr)
			}
			tamper(&candidate)
			if validationErr := ValidateSnapshot(candidate); !IsCategory(validationErr, CategoryIntegrity) {
				t.Fatalf("tamper category=%v", validationErr)
			}
		})
	}
}

// TestScopedRuntimeRejectsInvalidCompositionAndCoordinates exercises closed
// constructors and malformed durable coordinates before any store mutation.
func TestScopedRuntimeRejectsInvalidCompositionAndCoordinates(t *testing.T) {
	clock := &fakeClock{now: time.Now().UTC()}
	if _, err := NewAdmission(nil, clock); !IsCategory(err, CategoryInvalid) {
		t.Fatalf("nil admission store=%v", err)
	}
	if _, err := NewScopedRepository(nil, clock); !IsCategory(err, CategoryInvalid) {
		t.Fatalf("nil repository store=%v", err)
	}
	if _, err := NewMessageBus(nil, clock); !IsCategory(err, CategoryInvalid) {
		t.Fatalf("nil bus store=%v", err)
	}
	if _, err := NewQueue(nil, clock); !IsCategory(err, CategoryInvalid) {
		t.Fatalf("nil queue store=%v", err)
	}
	if err := ValidateTenantRunBinding(TenantRunBinding{}); !IsCategory(err, CategoryIntegrity) {
		t.Fatalf("empty binding=%v", err)
	}
	if _, err := decodeRecordCursor("not-base64", TenantScope{}, EntityRun); !IsCategory(err, CategoryInvalid) {
		t.Fatalf("cursor=%v", err)
	}
	if validConfiguration(ConfigurationCoordinate{}) || validMessageKind(MessageKind("unknown")) || validEntityClass(ScopedEntityClass("unknown")) {
		t.Fatal("unknown durable coordinate accepted")
	}
}

// TestScopedCacheAndObjectStoreRejectForgedAndDivergentKeys covers shared
// in-memory boundaries without relying on opaque ID uniqueness.
func TestScopedCacheAndObjectStoreRejectForgedAndDivergentKeys(t *testing.T) {
	service, _, _, principal, identity := fixture(t)
	capability, _ := service.Authorize(context.Background(), principal, ActionEvidenceRead, ResourceEvidence, identity)
	key, err := NewScopedKey(capability, ResourceEvidence, "object-a")
	if err != nil {
		t.Fatal(err)
	}
	cache := NewScopedCache()
	if err = cache.Put(key, []byte("cached")); err != nil {
		t.Fatal(err)
	}
	if value, ok := cache.Get(key); !ok || string(value) != "cached" {
		t.Fatalf("cache value=%q ok=%v", value, ok)
	}
	cache.Delete(key)
	if _, ok := cache.Get(key); ok {
		t.Fatal("deleted cache entry remained visible")
	}

	objects := NewScopedObjectStore()
	if err = objects.Put(key, []byte("payload")); err != nil {
		t.Fatal(err)
	}
	if err = objects.Put(key, []byte("different")); !IsCategory(err, CategoryConflict) {
		t.Fatalf("divergent object=%v", err)
	}
	if value, getErr := objects.Get(key); getErr != nil || string(value) != "payload" {
		t.Fatalf("object value=%q err=%v", value, getErr)
	}
	forged := key
	forged.ProjectID = "project-b"
	if _, getErr := objects.Get(forged); !IsCategory(getErr, CategoryInvalid) {
		t.Fatalf("forged object=%v", getErr)
	}
	if err = objects.Delete(key); err != nil {
		t.Fatal(err)
	}
	if err = objects.Delete(key); !IsCategory(err, CategoryNotFoundHidden) {
		t.Fatalf("missing delete=%v", err)
	}
}

// absentRetentionConsumer models a consumer that has already removed its
// object, allowing dependency-order tests to converge without external I/O.
type absentRetentionConsumer struct{}

// Observe confirms absence at the consumer-owned boundary.
func (absentRetentionConsumer) Observe(context.Context, DeletionCommand) (DeletionObservation, error) {
	return DeletionObservation{Exists: false}, nil
}

// Delete is unreachable after an absence observation.
func (absentRetentionConsumer) Delete(context.Context, DeletionCommand) (EffectStatus, error) {
	return EffectRejected, errors.New("unexpected delete")
}

// TestRetentionBatchAndGraphRespectDependencyOrdering exercises bounded batch
// traversal and per-resource authority selection over heterogeneous intents.
func TestRetentionBatchAndGraphRespectDependencyOrdering(t *testing.T) {
	service, memory, clock, principal, identity := fixture(t)
	ctx := context.Background()
	evidenceCapability, _ := service.Authorize(ctx, principal, ActionRetentionExecute, ResourceEvidence, identity)
	runCapability, _ := service.Authorize(ctx, principal, ActionRetentionExecute, ResourceRun, identity)
	store := &testDurable{snapshot: memory.Snapshot()}
	controller, err := NewRetentionController(store, clock, map[ResourceClass]RetentionConsumer{ResourceEvidence: absentRetentionConsumer{}, ResourceRun: absentRetentionConsumer{}})
	if err != nil {
		t.Fatal(err)
	}
	floors, periods := map[RetentionClass]time.Duration{}, map[RetentionClass]time.Duration{}
	for _, class := range retentionClasses() {
		floors[class], periods[class] = time.Hour, time.Hour
	}
	policy := RetentionPolicy{OrganizationID: identity.OrganizationID, Version: 1, Floors: floors, Periods: periods, ProtectedRecords: map[RetentionClass][]string{}, BatchLimit: 8, GraphLimit: 8}
	plan := func(id string, class RetentionClass, capability Capability, resource ResourceClass) {
		t.Helper()
		eligibility, evaluateErr := EvaluateRetention(policy, RetentionCandidate{ID: id, Class: class, TerminalAt: clock.now.Add(-2 * time.Hour)}, clock.now)
		if evaluateErr != nil {
			t.Fatal(evaluateErr)
		}
		if _, planErr := controller.Plan(ctx, DeletionPlan{Capability: capability, OperationID: "plan-" + id, Eligibility: eligibility, Resource: resource}); planErr != nil {
			t.Fatal(planErr)
		}
	}
	plan("batch-evidence-a", RetentionEvidence, evidenceCapability, ResourceEvidence)
	plan("batch-evidence-b", RetentionEvidence, evidenceCapability, ResourceEvidence)
	if err = controller.ConvergeBatch(ctx, evidenceCapability, []string{"batch-evidence-b", "batch-evidence-a"}, "batch-delete", 2); err != nil {
		t.Fatal(err)
	}
	plan("graph-run", RetentionRun, runCapability, ResourceRun)
	plan("graph-evidence", RetentionEvidence, evidenceCapability, ResourceEvidence)
	capabilities := map[ResourceClass]Capability{ResourceRun: runCapability, ResourceEvidence: evidenceCapability}
	if err = controller.ConvergeGraph(ctx, capabilities, []string{"graph-run", "graph-evidence"}, "graph-delete", 2); err != nil {
		t.Fatal(err)
	}
	if err = controller.ConvergeGraph(ctx, map[ResourceClass]Capability{ResourceRun: runCapability}, []string{"graph-evidence"}, "graph-deny", 1); !IsCategory(err, CategoryUnauthorized) {
		t.Fatalf("missing graph authority=%v", err)
	}
}

// TestSnapshotRestoreAndHistoryRejectRollback covers all append-only history
// families and proves a restored aggregate never aliases the source snapshot.
func TestSnapshotRestoreAndHistoryRejectRollback(t *testing.T) {
	current := emptySnapshot()
	current.Receipts["org/op"] = StoreReceipt{OrganizationID: "org", OperationID: "op", Digest: strings.Repeat("a", 64)}
	current.Audits["org"] = []AuditEvent{{OrganizationID: "org", Sequence: 1, Digest: strings.Repeat("b", 64)}}
	version := SecretVersion{ReferenceID: "secret", Version: 1}
	version.Digest = secretVersionDigest(version)
	current.Secrets["org/project/secret"] = []SecretVersion{version}
	lease := SecretLease{ID: "lease", Scope: TenantScope{Digest: strings.Repeat("c", 64)}, ReferenceID: "secret", Version: 1, RunID: "run", Phase: "execute", Consumer: "worker", Purpose: "deploy", Fence: 1, IssuedAt: time.Unix(1, 0), ExpiresAt: time.Unix(2, 0)}
	current.SecretLeases["org/project/lease"] = lease
	next := emptySnapshot()
	next.Receipts["org/op"] = current.Receipts["org/op"]
	next.Audits["org"] = append([]AuditEvent(nil), current.Audits["org"]...)
	next.Secrets["org/project/secret"] = append([]SecretVersion(nil), current.Secrets["org/project/secret"]...)
	next.SecretLeases["org/project/lease"] = current.SecretLeases["org/project/lease"]
	if err := ValidateReceiptHistory(current, next); err != nil {
		t.Fatalf("history err=%v", err)
	}

	for name, mutate := range map[string]func(Snapshot){
		"receipt": func(candidate Snapshot) { delete(candidate.Receipts, "org/op") },
		"audit":   func(candidate Snapshot) { candidate.Audits["org"] = nil },
		"secret":  func(candidate Snapshot) { candidate.Secrets["org/project/secret"] = nil },
		"lease":   func(candidate Snapshot) { delete(candidate.SecretLeases, "org/project/lease") },
	} {
		t.Run(name, func(t *testing.T) {
			candidate := emptySnapshot()
			candidate.Receipts["org/op"] = next.Receipts["org/op"]
			candidate.Audits["org"] = append([]AuditEvent(nil), next.Audits["org"]...)
			candidate.Secrets["org/project/secret"] = append([]SecretVersion(nil), next.Secrets["org/project/secret"]...)
			candidate.SecretLeases["org/project/lease"] = next.SecretLeases["org/project/lease"]
			mutate(candidate)
			if historyErr := ValidateReceiptHistory(current, candidate); !IsCategory(historyErr, CategoryIntegrity) {
				t.Fatalf("history category=%v", historyErr)
			}
		})
	}

	_, memory, _, _, _ := fixture(t)
	snapshot := memory.Snapshot()
	restored := NewMemoryStore()
	if err := restored.Restore(snapshot); err != nil {
		t.Fatal(err)
	}
	if restored.Snapshot().Organizations["org-a"].ID != "org-a" {
		t.Fatal("restore lost validated organization")
	}
	broken := restored.Snapshot()
	broken.Messages = nil
	if err := restored.Restore(broken); !IsCategory(err, CategoryIntegrity) {
		t.Fatalf("nil map restore=%v", err)
	}
}

// requireTenantCategory keeps the adversarial matrix explicit about every
// closed failure class without depending on message text.
func requireTenantCategory(t *testing.T, err error, category Category) {
	t.Helper()
	if !IsCategory(err, category) {
		t.Fatalf("expected %s, got %v", category, err)
	}
}

// TestClosedBoundaryAdversarialMatrix covers malformed, stale and cancelled
// inputs across the security core before they can mutate durable state.
func TestClosedBoundaryAdversarialMatrix(t *testing.T) {
	service, memory, clock, principal, identity := fixture(t)
	ctx := context.Background()
	cancelled, cancel := context.WithCancel(ctx)
	cancel()

	if (&Error{}).Error() == "" || (*Error)(nil).Error() == "" || (&Error{Cause: context.Canceled}).Unwrap() != context.Canceled {
		t.Fatal("typed error contract changed")
	}
	requireTenantCategory(t, ValidateLimits(Limits{}), CategoryInvalid)
	requireTenantCategory(t, ValidateID("bad/id"), CategoryInvalid)
	_, err := CompileRole(Role{OrganizationID: "org-a", ID: "role", Actions: []Action{ActionRunRead, ActionRunRead}, Resources: []ResourceClass{ResourceRun}, Version: 1}, DefaultLimits())
	requireTenantCategory(t, err, CategoryInvalid)

	if _, err = NewService(nil, clock, DefaultLimits(), nil); !IsCategory(err, CategoryInvalid) {
		t.Fatalf("nil service store=%v", err)
	}
	badOrganization := Organization{ID: "bad/id", State: StateActive, Version: 1, SecurityRevision: 1, PolicyDigest: strings.Repeat("a", 64)}
	err = service.Bootstrap(ctx, badOrganization, ProjectRegistration{}, Principal{}, Membership{}, nil)
	requireTenantCategory(t, err, CategoryInvalid)
	_, err = service.Resolve(cancelled, AuthenticatedSubject{}, identity)
	requireTenantCategory(t, err, CategoryCancelled)
	_, err = service.Resolve(ctx, AuthenticatedSubject{}, identity)
	requireTenantCategory(t, err, CategoryUnauthenticated)
	_, err = service.Authorize(cancelled, principal, ActionRunRead, ResourceRun, identity)
	requireTenantCategory(t, err, CategoryCancelled)
	_, err = service.Authorize(ctx, principal, Action("unknown"), ResourceRun, identity)
	requireTenantCategory(t, err, CategoryUnauthorized)
	err = service.VerifyCapability(cancelled, Capability{}, ActionRunRead, ResourceRun)
	requireTenantCategory(t, err, CategoryCancelled)
	_, err = service.AppendAudit(ctx, Capability{}, "invented", "")
	requireTenantCategory(t, err, CategoryUnauthorized)
	if err = service.VerifyAudit(ctx, "missing-org"); err != nil {
		t.Fatalf("empty audit chain=%v", err)
	}
	_, err = service.ReserveQuota(ctx, Capability{}, "bad/id", "worker", 0, QuotaLimit{})
	requireTenantCategory(t, err, CategoryUnauthorized)
	err = service.ReleaseQuota(ctx, Capability{}, "bad/id")
	requireTenantCategory(t, err, CategoryUnauthorized)
	_, err = service.PlanDeletion(ctx, Capability{}, "bad/id", ResourceEvidence)
	requireTenantCategory(t, err, CategoryUnauthorized)

	durable := &testDurable{snapshot: memory.Snapshot()}
	admin, _ := NewAdministration(durable, clock, DefaultLimits())
	_, err = admin.Apply(cancelled, Capability{}, "operation", 0, AdministrationMutation{})
	requireTenantCategory(t, err, CategoryCancelled)
	_, err = admin.Apply(ctx, Capability{}, "bad/id", 0, AdministrationMutation{})
	requireTenantCategory(t, err, CategoryInvalid)
	for name, mutation := range map[string]AdministrationMutation{
		"unknown":    {Kind: AdministrationKind("unknown")},
		"project":    {Kind: AdministrationProject},
		"membership": {Kind: AdministrationMembership},
		"role":       {Kind: AdministrationRole},
		"service":    {Kind: AdministrationServiceIdentity},
	} {
		t.Run("admin "+name, func(t *testing.T) {
			candidate := memory.Snapshot()
			requireTenantCategory(t, admin.applyMutation(&candidate, identity.OrganizationID, 0, mutation), CategoryInvalid)
		})
	}
	values := map[string]Organization{"existing": {ID: "existing", Version: 1}}
	requireTenantCategory(t, replaceVersioned(values, "existing", 0, 1, Organization{ID: "existing", Version: 1}), CategoryStale)
	requireTenantCategory(t, replaceVersioned(values, "missing", 1, 2, Organization{ID: "missing", Version: 2}), CategoryStale)

	if _, err = NewAuditService(nil, clock, 1, time.Minute); !IsCategory(err, CategoryInvalid) {
		t.Fatalf("nil audit store=%v", err)
	}
	audits, _ := NewAuditService(durable, clock, 2, time.Minute)
	requireTenantCategory(t, audits.Verify(ctx, "bad/id"), CategoryInvalid)
	_, err = audits.Page(ctx, Capability{}, "", 0)
	requireTenantCategory(t, err, CategoryInvalid)
	_, err = audits.decodeCursor("not-base64", TenantScope{})
	requireTenantCategory(t, err, CategoryInvalid)

	if _, err = NewSensitiveExecutor(nil, clock); !IsCategory(err, CategoryInvalid) {
		t.Fatalf("nil sensitive store=%v", err)
	}
	executor, _ := NewSensitiveExecutor(durable, clock)
	_, err = executor.Apply(ctx, SensitiveAction{}, nil)
	requireTenantCategory(t, err, CategoryInvalid)
	err = executor.RecordDenied(ctx, Capability{}, "operation-denied", "resource", CategoryUnauthorized)
	requireTenantCategory(t, err, CategoryInvalid)

	if _, err = NewSecretService(nil, fakeKeys{}, fakeRandom{}, clock, DefaultLimits()); !IsCategory(err, CategoryInvalid) {
		t.Fatalf("nil secret store=%v", err)
	}
	secrets, _ := NewSecretService(durable, fakeKeys{}, fakeRandom{}, clock, DefaultLimits())
	_, err = secrets.Create(ctx, Capability{}, "bad/id", "secret", "purpose", "key", []byte("value"))
	requireTenantCategory(t, err, CategoryInvalid)
	_, err = secrets.Revoke(cancelled, Capability{}, "operation", "secret", 1)
	requireTenantCategory(t, err, CategoryInvalid)
	_, err = secrets.IssueLease(ctx, Capability{}, "operation", "lease", "secret", "run", "bad-phase", "consumer", "purpose", 0, 0)
	requireTenantCategory(t, err, CategoryInvalid)
	err = secrets.WithSecret(ctx, Capability{}, "operation", SecretLease{}, SecretUseIdentity{}, nil)
	requireTenantCategory(t, err, CategoryInvalid)

	if _, err = SealRetentionCandidate(RetentionCandidate{}); !IsCategory(err, CategoryInvalid) {
		t.Fatalf("empty retention candidate=%v", err)
	}
	if _, err = EvaluateRetention(RetentionPolicy{}, RetentionCandidate{}, clock.now); !IsCategory(err, CategoryInvalid) {
		t.Fatalf("empty retention policy=%v", err)
	}
	requireTenantCategory(t, ValidateEligibilitySnapshot(EligibilitySnapshot{}), CategoryIntegrity)
	if _, err = NewRetentionController(nil, clock, nil); !IsCategory(err, CategoryInvalid) {
		t.Fatalf("nil retention controller=%v", err)
	}
	controller, _ := NewRetentionController(durable, clock, map[ResourceClass]RetentionConsumer{})
	_, err = controller.Plan(ctx, DeletionPlan{})
	requireTenantCategory(t, err, CategoryInvalid)
	requireTenantCategory(t, controller.Converge(ctx, Capability{}, "bad/id", "operation"), CategoryInvalid)
	requireTenantCategory(t, controller.ConvergeBatch(ctx, Capability{}, nil, "operation", 0), CategoryInvalid)
	requireTenantCategory(t, controller.ConvergeGraph(ctx, nil, nil, "operation", 0), CategoryInvalid)

	for name, policy := range map[string]QuotaPolicySnapshot{
		"empty":       {},
		"outstanding": {Version: 1, Tenant: map[string]QuotaLimit{"worker": {Limit: 1, Window: time.Hour}}, Project: map[string]QuotaLimit{"worker": {Limit: 1, Window: time.Hour}}, TenantOutstanding: 1, ProjectOutstanding: 2, ReservationTTL: time.Minute},
		"dimension":   {Version: 1, Tenant: map[string]QuotaLimit{"bad/id": {Limit: 1, Window: time.Hour}}, Project: map[string]QuotaLimit{"bad/id": {Limit: 1, Window: time.Hour}}, TenantOutstanding: 2, ProjectOutstanding: 1, ReservationTTL: time.Minute},
	} {
		t.Run("quota "+name, func(t *testing.T) {
			_, compileErr := CompileQuotaPolicy(policy)
			requireTenantCategory(t, compileErr, CategoryInvalid)
		})
	}
	if _, err = NewDurableQuotaService(nil, clock); !IsCategory(err, CategoryInvalid) {
		t.Fatalf("nil quota store=%v", err)
	}
	quotas, _ := NewDurableQuotaService(durable, clock)
	_, err = quotas.Reserve(ctx, QuotaRequest{})
	requireTenantCategory(t, err, CategoryInvalid)
	requireTenantCategory(t, quotas.MarkUncertain(ctx, Capability{}, "bad/id", "operation"), CategoryInvalid)
	requireTenantCategory(t, quotas.Release(ctx, Capability{}, "bad/id", "operation"), CategoryInvalid)
	requireTenantCategory(t, quotas.ReleaseExpired(ctx, Capability{}, "bad/id", "operation"), CategoryInvalid)
	if _, err = NewQuotaGuard(nil); !IsCategory(err, CategoryInvalid) {
		t.Fatalf("nil quota guard=%v", err)
	}
	if _, err = NewQuotaScopedRepository(nil, nil); !IsCategory(err, CategoryInvalid) {
		t.Fatalf("nil quota repository=%v", err)
	}
	if quotaEffectStatus(failure(CategoryInvalid, "effect", nil)) != EffectRejected || quotaEffectStatus(errors.New("unknown")) != EffectUnknown || quotaEffectStatus(nil) != EffectCommitted {
		t.Fatal("quota effect classification changed")
	}
}

// TestRetentionControllerClassifiesConsumerOutcomes proves that observation
// failures and every effect status preserve a recoverable durable intent.
func TestRetentionControllerClassifiesConsumerOutcomes(t *testing.T) {
	for name, test := range map[string]struct {
		observation DeletionObservation
		observeErr  error
		status      EffectStatus
		deleteErr   error
		category    Category
		state       string
	}{
		"observe unavailable": {observation: DeletionObservation{Exists: true}, observeErr: errors.New("observe unavailable"), category: CategoryUnavailable, state: DeletionPlanned},
		"delete rejected":     {observation: DeletionObservation{Exists: true}, status: EffectRejected, deleteErr: errors.New("rejected"), state: DeletionPlanned},
		"delete unknown":      {observation: DeletionObservation{Exists: true}, status: EffectUnknown, deleteErr: errors.New("unknown"), state: DeletionUncertain},
		"invalid status":      {observation: DeletionObservation{Exists: true}, status: EffectStatus("invalid"), category: CategoryIntegrity, state: DeletionDeleting},
	} {
		t.Run(name, func(t *testing.T) {
			store, clock, capability := durableCapability(t, ActionRetentionExecute, ResourceEvidence)
			floors, periods := map[RetentionClass]time.Duration{}, map[RetentionClass]time.Duration{}
			for _, class := range retentionClasses() {
				floors[class], periods[class] = time.Hour, time.Hour
			}
			candidateID := "outcome-" + strings.ReplaceAll(name, " ", "-")
			eligibility, err := EvaluateRetention(RetentionPolicy{OrganizationID: capability.Scope.OrganizationID, Version: 1, Floors: floors, Periods: periods, ProtectedRecords: map[RetentionClass][]string{}, BatchLimit: 2, GraphLimit: 2}, RetentionCandidate{ID: candidateID, Class: RetentionEvidence, TerminalAt: clock.now.Add(-2 * time.Hour)}, clock.now)
			if err != nil {
				t.Fatal(err)
			}
			consumer := &outcomeRetentionConsumer{observation: test.observation, observeErr: test.observeErr, status: test.status, deleteErr: test.deleteErr}
			controller, err := NewRetentionController(store, clock, map[ResourceClass]RetentionConsumer{ResourceEvidence: consumer})
			if err != nil {
				t.Fatal(err)
			}
			if _, err = controller.Plan(context.Background(), DeletionPlan{Capability: capability, OperationID: "plan-" + candidateID, Eligibility: eligibility, Resource: ResourceEvidence}); err != nil {
				t.Fatal(err)
			}
			err = controller.Converge(context.Background(), capability, candidateID, "converge-"+candidateID)
			if test.category != "" {
				requireTenantCategory(t, err, test.category)
			} else if !errors.Is(err, test.deleteErr) {
				t.Fatalf("expected consumer error, got %v", err)
			}
			intent, loadErr := controller.loadIntent(context.Background(), capability.Scope, candidateID)
			if loadErr != nil || intent.State != test.state {
				t.Fatalf("intent=%+v err=%v", intent, loadErr)
			}
		})
	}
}

// outcomeRetentionConsumer exposes explicit observe/delete outcomes to the
// retention state-machine test without performing an external effect.
type outcomeRetentionConsumer struct {
	observation DeletionObservation
	observeErr  error
	status      EffectStatus
	deleteErr   error
}

// failingSurfaceProvider exposes authenticated-provider failures to gateway tests.
type failingSurfaceProvider struct{ err error }

// Binding returns the scripted provider failure.
func (provider failingSurfaceProvider) Binding(context.Context) (SurfaceBinding, error) {
	return SurfaceBinding{}, provider.err
}

// panicObserver proves telemetry cannot change authorization outcomes.
type panicObserver struct{}

// Observe deliberately panics at the non-authoritative boundary.
func (panicObserver) Observe(context.Context, Fact) { panic("observer failure") }

// scriptedDeleter exposes consumer deletion success or failure.
type scriptedDeleter struct{ err error }

// Delete returns the scripted consumer outcome.
func (deleter scriptedDeleter) Delete(context.Context, ScopedKey) error { return deleter.err }

// faultDurable injects one adapter boundary failure while delegating all other calls.
type faultDurable struct {
	DurableStore
	receiptErr  error
	snapshotErr error
	commitErr   error
}

// driftSnapshotDurable applies one valid state drift immediately before a selected snapshot read.
type driftSnapshotDurable struct {
	*testDurable
	reads   int
	driftAt int
	drift   func(*Snapshot)
}

// Snapshot injects the selected durable drift before returning an ownership-safe state copy.
func (store *driftSnapshotDurable) Snapshot(ctx context.Context) (Snapshot, error) {
	store.reads++
	if store.reads == store.driftAt {
		store.testDurable.mu.Lock()
		store.drift(&store.testDurable.snapshot)
		store.testDurable.mu.Unlock()
	}
	return store.testDurable.Snapshot(ctx)
}

// Receipt returns the injected lookup failure or delegates normally.
func (store faultDurable) Receipt(ctx context.Context, organizationID, operationID string) (StoreReceipt, error) {
	if store.receiptErr != nil {
		return StoreReceipt{}, store.receiptErr
	}
	return store.DurableStore.Receipt(ctx, organizationID, operationID)
}

// Snapshot returns the injected reload failure or delegates normally.
func (store faultDurable) Snapshot(ctx context.Context) (Snapshot, error) {
	if store.snapshotErr != nil {
		return Snapshot{}, store.snapshotErr
	}
	return store.DurableStore.Snapshot(ctx)
}

// Commit returns the injected publication failure or delegates normally.
func (store faultDurable) Commit(ctx context.Context, command StoreCommand) (StoreReceipt, error) {
	if store.commitErr != nil {
		return StoreReceipt{}, store.commitErr
	}
	return store.DurableStore.Commit(ctx, command)
}

// Observe returns the scripted live-reference result.
func (consumer *outcomeRetentionConsumer) Observe(context.Context, DeletionCommand) (DeletionObservation, error) {
	return consumer.observation, consumer.observeErr
}

// Delete returns the scripted consumer effect classification.
func (consumer *outcomeRetentionConsumer) Delete(context.Context, DeletionCommand) (EffectStatus, error) {
	return consumer.status, consumer.deleteErr
}

// TestScopedRuntimeFailureBranchesStayTenantHidden exercises cancellation,
// malformed coordinates, hidden misses and exact idempotent replay behavior.
func TestScopedRuntimeFailureBranchesStayTenantHidden(t *testing.T) {
	service, memory, clock, principal, identity := fixture(t)
	ctx := context.Background()
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	admit, _ := service.Authorize(ctx, principal, ActionRunAdmit, ResourceRun, identity)
	run, _ := service.Authorize(ctx, principal, ActionRunRead, ResourceRun, identity)
	evidence, _ := service.Authorize(ctx, principal, ActionEvidenceRead, ResourceEvidence, identity)
	store := &testDurable{snapshot: memory.Snapshot()}
	admission, _ := NewAdmission(store, clock)
	repository, _ := NewScopedRepository(store, clock)
	queue, _ := NewQueue(store, clock)
	bus, _ := NewMessageBus(store, clock)
	coordinate := ConfigurationCoordinate{Generation: 1, VersionDigest: strings.Repeat("1", 64), PolicyDigest: admit.Scope.PolicyDigest, CatalogDigest: strings.Repeat("2", 64), EffectiveDigest: strings.Repeat("3", 64)}

	_, _, err := admission.Admit(cancelled, admit, "cancel-admit", "run-cancel", "plan", coordinate)
	requireTenantCategory(t, err, CategoryCancelled)
	_, _, err = admission.Admit(ctx, admit, "bad/id", "run", "plan", coordinate)
	requireTenantCategory(t, err, CategoryInvalid)
	binding, receipt, err := admission.Admit(ctx, admit, "admit-replay", "run-replay", "plan", coordinate)
	if err != nil {
		t.Fatal(err)
	}
	replayed, replayReceipt, err := admission.Admit(ctx, admit, "admit-replay", "run-replay", "plan", coordinate)
	if err != nil || replayed.Digest != binding.Digest || replayReceipt != receipt {
		t.Fatalf("replay=%+v receipt=%+v err=%v", replayed, replayReceipt, err)
	}
	_, err = admission.LoadBinding(cancelled, run, binding.RunID)
	requireTenantCategory(t, err, CategoryCancelled)
	_, err = admission.LoadBinding(ctx, run, "missing-run")
	requireTenantCategory(t, err, CategoryNotFoundHidden)

	_, _, err = repository.Put(cancelled, evidence, "put-cancel", EntityEvidence, "record", strings.Repeat("a", 64), 0)
	requireTenantCategory(t, err, CategoryCancelled)
	_, _, err = repository.Put(ctx, evidence, "put-invalid", ScopedEntityClass("invalid"), "record", strings.Repeat("a", 64), 0)
	requireTenantCategory(t, err, CategoryInvalid)
	_, err = repository.Get(cancelled, evidence, EntityEvidence, "record")
	requireTenantCategory(t, err, CategoryCancelled)
	_, err = repository.List(ctx, evidence, EntityEvidence, "", 0)
	requireTenantCategory(t, err, CategoryInvalid)

	_, err = bus.Accept(cancelled, run, "message-cancel", MessageInbox, "delivery", strings.Repeat("a", 64))
	requireTenantCategory(t, err, CategoryCancelled)
	message, err := bus.Accept(ctx, run, "message-replay", MessageOutbox, "delivery-replay", strings.Repeat("b", 64))
	if err != nil {
		t.Fatal(err)
	}
	redelivery, err := bus.Accept(ctx, run, "message-redelivery", MessageOutbox, "delivery-replay", strings.Repeat("b", 64))
	if err != nil || redelivery.Digest != message.Digest {
		t.Fatalf("redelivery=%+v err=%v", redelivery, err)
	}
	_, err = bus.Claim(ctx, run, "claim-missing", MessageOutbox, "missing-delivery", "worker", 1)
	requireTenantCategory(t, err, CategoryNotFoundHidden)
	_, err = queue.Claim(cancelled, run, "claim-cancel", binding.RunID+"-first", "worker", 1)
	requireTenantCategory(t, err, CategoryCancelled)
	_, err = queue.Claim(ctx, run, "claim-missing-job", "missing-job", "worker", 1)
	requireTenantCategory(t, err, CategoryNotFoundHidden)
}

// TestServiceStateDriftAndReplayMatrix proves authentication, RBAC, audit,
// quota and retention decisions fail closed as their authoritative state moves.
func TestServiceStateDriftAndReplayMatrix(t *testing.T) {
	service, memory, clock, _, identity := fixture(t)
	ctx := context.Background()
	if _, err := NewService(memory, clock, Limits{} /* invalid bounded policy */, nil); !IsCategory(err, CategoryInvalid) {
		t.Fatalf("invalid limits=%v", err)
	}
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if err := service.Bootstrap(cancelled, Organization{}, ProjectRegistration{}, Principal{}, Membership{}, nil); !IsCategory(err, CategoryCancelled) {
		t.Fatalf("cancelled bootstrap=%v", err)
	}
	invalidRole := Role{OrganizationID: "org-a", ID: "invalid", Actions: []Action{ActionRunRead, ActionRunRead}, Resources: []ResourceClass{ResourceRun}, Version: 1}
	org := Organization{ID: "org-new", State: StateActive, Version: 1, SecurityRevision: 1, PolicyDigest: strings.Repeat("a", 64)}
	project := ProjectRegistration{OrganizationID: org.ID, ProjectID: "project", RepositoryID: "repo", State: StateActive, Version: 1}
	member := Principal{ID: "member", Kind: PrincipalHuman, SubjectDigest: strings.Repeat("b", 64), State: StateActive, Version: 1}
	membership := Membership{OrganizationID: org.ID, PrincipalID: member.ID, State: StateActive, Version: 1}
	if err := service.Bootstrap(ctx, org, project, member, membership, []Role{invalidRole}); !IsCategory(err, CategoryInvalid) {
		t.Fatalf("invalid role bootstrap=%v", err)
	}
	crossRole := Role{OrganizationID: "other-org", ID: "reader", Actions: []Action{ActionRunRead}, Resources: []ResourceClass{ResourceRun}, Version: 1}
	if err := service.Bootstrap(ctx, org, project, member, membership, []Role{crossRole}); !IsCategory(err, CategoryUnauthorized) {
		t.Fatalf("cross-org role bootstrap=%v", err)
	}
	if err := service.Bootstrap(ctx, memory.Snapshot().Organizations["org-a"], memory.Snapshot().Projects["org-a/project-a"], memory.Snapshot().Principals["org-a/principal-a"], memory.Snapshot().Memberships["org-a/principal-a"], nil); !IsCategory(err, CategoryConflict) {
		t.Fatalf("duplicate bootstrap=%v", err)
	}

	subject := AuthenticatedSubject{Issuer: "issuer", Subject: "subject"}
	resolved, err := service.Resolve(ctx, subject, identity)
	if err != nil {
		t.Fatal(err)
	}
	originalPrincipal := memory.Principals["org-a/principal-a"]
	mutated := originalPrincipal
	mutated.State = StateSuspended
	memory.Principals["org-a/principal-a"] = mutated
	if _, err = service.Resolve(ctx, subject, identity); !IsCategory(err, CategoryUnauthenticated) {
		t.Fatalf("suspended principal=%v", err)
	}
	memory.Principals["org-a/principal-a"] = originalPrincipal
	originalOrg := memory.Organizations["org-a"]
	mutatedOrg := originalOrg
	mutatedOrg.State = StateSuspended
	memory.Organizations["org-a"] = mutatedOrg
	if _, err = service.Resolve(ctx, subject, identity); !IsCategory(err, CategoryUnauthorized) {
		t.Fatalf("suspended org=%v", err)
	}
	memory.Organizations["org-a"] = originalOrg
	originalProject := memory.Projects["org-a/project-a"]
	mutatedProject := originalProject
	mutatedProject.RepositoryID = "other-repo"
	memory.Projects["org-a/project-a"] = mutatedProject
	if _, err = service.Resolve(ctx, subject, identity); !IsCategory(err, CategoryUnauthorized) {
		t.Fatalf("repository drift=%v", err)
	}
	memory.Projects["org-a/project-a"] = originalProject
	originalMembership := memory.Memberships["org-a/principal-a"]
	mutatedMembership := originalMembership
	mutatedMembership.ValidUntil = clock.now
	memory.Memberships["org-a/principal-a"] = mutatedMembership
	if _, err = service.Resolve(ctx, subject, identity); !IsCategory(err, CategoryUnauthorized) {
		t.Fatalf("expired membership=%v", err)
	}
	memory.Memberships["org-a/principal-a"] = originalMembership

	if _, err = service.Authorize(ctx, resolved, ActionRunRead, ResourceRun, TrustedResourceIdentity{OrganizationID: "org-a", ProjectID: "missing", RepositoryID: "repo"}); !IsCategory(err, CategoryUnauthorized) {
		t.Fatalf("missing project authorize=%v", err)
	}
	role := memory.Roles["org-a/operator"]
	delete(memory.Roles, "org-a/operator")
	if _, err = service.Authorize(ctx, resolved, ActionRunRead, ResourceRun, identity); !IsCategory(err, CategoryUnauthorized) {
		t.Fatalf("missing role authorize=%v", err)
	}
	memory.Roles["org-a/operator"] = role
	capability, err := service.Authorize(ctx, resolved, ActionQuotaReserve, ResourceQuota, identity)
	if err != nil {
		t.Fatal(err)
	}
	forged := capability
	forged.Digest = strings.Repeat("f", 64)
	if err = service.VerifyCapability(ctx, forged, ActionQuotaReserve, ResourceQuota); !IsCategory(err, CategoryUnauthorized) {
		t.Fatalf("forged capability=%v", err)
	}
	revokedOrg := memory.Organizations["org-a"]
	revokedOrg.SecurityRevision++
	memory.Organizations["org-a"] = revokedOrg
	if err = service.VerifyCapability(ctx, capability, ActionQuotaReserve, ResourceQuota); !IsCategory(err, CategoryRevoked) {
		t.Fatalf("revision drift=%v", err)
	}
	memory.Organizations["org-a"] = originalOrg

	if _, err = service.AppendAudit(ctx, capability, "invalid", ""); !IsCategory(err, CategoryInvalid) {
		t.Fatalf("invalid audit outcome=%v", err)
	}
	firstAudit, err := service.AppendAudit(ctx, capability, "allowed", "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = service.AppendAudit(ctx, capability, "committed", ""); err != nil {
		t.Fatal(err)
	}
	memory.Audits["org-a"][0].Digest = strings.Repeat("e", 64)
	if err = service.VerifyAudit(ctx, "org-a"); !IsCategory(err, CategoryIntegrity) {
		t.Fatalf("audit tamper=%v", err)
	}
	memory.Audits["org-a"][0].Digest = firstAudit.Digest
	memory.AuditHeads["org-a"] = AuditHead{}
	if err = service.VerifyAudit(ctx, "org-a"); !IsCategory(err, CategoryIntegrity) {
		t.Fatalf("audit head tamper=%v", err)
	}

	limit := QuotaLimit{Limit: 2, Window: time.Hour}
	reservation, err := service.ReserveQuota(ctx, capability, "legacy-quota", "worker", 1, limit)
	if err != nil {
		t.Fatal(err)
	}
	replay, err := service.ReserveQuota(ctx, capability, "legacy-quota", "worker", 1, limit)
	if err != nil || replay.Digest != reservation.Digest {
		t.Fatalf("quota replay=%+v err=%v", replay, err)
	}
	if _, err = service.ReserveQuota(ctx, capability, "legacy-quota", "worker", 2, limit); !IsCategory(err, CategoryConflict) {
		t.Fatalf("quota divergence=%v", err)
	}
	if _, err = service.ReserveQuota(ctx, capability, "legacy-exhaust", "worker", 2, limit); !IsCategory(err, CategoryQuotaExhausted) {
		t.Fatalf("quota exhaust=%v", err)
	}
	if err = service.ReleaseQuota(ctx, capability, "missing-quota"); !IsCategory(err, CategoryNotFoundHidden) {
		t.Fatalf("missing release=%v", err)
	}
	if err = service.ReleaseQuota(ctx, capability, reservation.ID); err != nil || service.ReleaseQuota(ctx, capability, reservation.ID) != nil {
		t.Fatalf("quota release=%v", err)
	}

	retention, _ := service.Authorize(ctx, resolved, ActionRetentionExecute, ResourceEvidence, identity)
	intent, err := service.PlanDeletion(ctx, retention, "legacy-delete", ResourceEvidence)
	if err != nil {
		t.Fatal(err)
	}
	replayedIntent, err := service.PlanDeletion(ctx, retention, "legacy-delete", ResourceEvidence)
	if err != nil || replayedIntent.Digest != intent.Digest {
		t.Fatalf("deletion replay=%+v err=%v", replayedIntent, err)
	}
}

// TestRetentionControllerRejectsStalePlansAndTamperedIntents covers replay,
// scope drift, corrupted durable state and the closed dependency rank catalog.
func TestRetentionControllerRejectsStalePlansAndTamperedIntents(t *testing.T) {
	store, clock, capability := durableCapability(t, ActionRetentionExecute, ResourceEvidence)
	if _, err := NewRetentionController(store, clock, map[ResourceClass]RetentionConsumer{ResourceClass("unknown"): absentRetentionConsumer{}}); !IsCategory(err, CategoryInvalid) {
		t.Fatalf("unknown consumer=%v", err)
	}
	floors, periods := map[RetentionClass]time.Duration{}, map[RetentionClass]time.Duration{}
	for _, class := range retentionClasses() {
		floors[class], periods[class] = time.Hour, time.Hour
	}
	policy := RetentionPolicy{OrganizationID: capability.Scope.OrganizationID, Version: 1, Floors: floors, Periods: periods, ProtectedRecords: map[RetentionClass][]string{}, BatchLimit: 2, GraphLimit: 2}
	eligibility, _ := EvaluateRetention(policy, RetentionCandidate{ID: "stale-plan", Class: RetentionEvidence, TerminalAt: clock.now.Add(-2 * time.Hour)}, clock.now)
	controller, _ := NewRetentionController(store, clock, map[ResourceClass]RetentionConsumer{ResourceEvidence: absentRetentionConsumer{}})
	plan := DeletionPlan{Capability: capability, OperationID: "stale-plan-op", Eligibility: eligibility, Resource: ResourceEvidence}
	if _, err := controller.Plan(context.Background(), plan); err != nil {
		t.Fatal(err)
	}
	if _, err := controller.Plan(context.Background(), plan); err != nil {
		t.Fatalf("exact plan replay=%v", err)
	}
	divergent := plan
	divergent.Eligibility.CandidateDigest = strings.Repeat("f", 64)
	divergent.Eligibility.Digest = digestStrings(divergent.Eligibility.CandidateID, string(divergent.Eligibility.CandidateClass), divergent.Eligibility.CandidateDigest, fmt.Sprint(divergent.Eligibility.PolicyVersion), divergent.Eligibility.PolicyDigest, divergent.Eligibility.EvaluatedAt.Format(time.RFC3339Nano), divergent.Eligibility.EligibleAfter.Format(time.RFC3339Nano))
	if _, err := controller.Plan(context.Background(), divergent); !IsCategory(err, CategoryConflict) {
		t.Fatalf("divergent plan=%v", err)
	}
	if err := controller.Converge(context.Background(), capability, "missing-intent", "missing-converge"); !IsCategory(err, CategoryNotFoundHidden) {
		t.Fatalf("missing intent=%v", err)
	}
	snapshot, _ := store.Snapshot(context.Background())
	key := deletionKey(capability.Scope, eligibility.CandidateID)
	intent := snapshot.Deletions[key]
	intent.Digest = strings.Repeat("e", 64)
	snapshot.Deletions[key] = intent
	store.snapshot = snapshot
	if _, err := controller.loadIntent(context.Background(), capability.Scope, eligibility.CandidateID); !IsCategory(err, CategoryIntegrity) {
		t.Fatalf("tampered intent=%v", err)
	}
	if deletionRank(ResourceEnvironment) != 0 || deletionRank(ResourceSecret) != 1 || deletionRank(ResourceConfig) != 2 || deletionRank(ResourceTenant) != 3 || deletionRank(ResourceClass("unknown")) != 4 {
		t.Fatal("dependency rank catalog changed")
	}
}

// TestSnapshotValidationRejectsEveryForgedProjectionFamily proves every
// persisted namespace is independently checked before adapter reload.
func TestSnapshotValidationRejectsEveryForgedProjectionFamily(t *testing.T) {
	_, memory, clock, _, _ := fixture(t)
	base := memory.Snapshot()
	for name, mutate := range map[string]func(*Snapshot){
		"policy": func(snapshot *Snapshot) {
			snapshot.Policies["org-a"] = TenantPolicy{OrganizationID: "org-a"}
		},
		"project namespace": func(snapshot *Snapshot) {
			project := snapshot.Projects["org-a/project-a"]
			snapshot.Projects["forged"] = project
			delete(snapshot.Projects, "org-a/project-a")
		},
		"principal namespace": func(snapshot *Snapshot) {
			principal := snapshot.Principals["org-a/principal-a"]
			snapshot.Principals["forged"] = principal
			delete(snapshot.Principals, "org-a/principal-a")
		},
		"membership namespace": func(snapshot *Snapshot) {
			membership := snapshot.Memberships["org-a/principal-a"]
			snapshot.Memberships["forged"] = membership
			delete(snapshot.Memberships, "org-a/principal-a")
		},
		"service identity": func(snapshot *Snapshot) {
			snapshot.ServiceIdentities["org-a/service"] = ServiceIdentity{OrganizationID: "org-a", PrincipalID: "service"}
		},
		"role": func(snapshot *Snapshot) {
			role := snapshot.Roles["org-a/operator"]
			role.Digest = ""
			snapshot.Roles["org-a/operator"] = role
		},
		"audit": func(snapshot *Snapshot) {
			snapshot.Audits["org-a"] = []AuditEvent{{OrganizationID: "org-a", Sequence: 2}}
		},
		"audit head": func(snapshot *Snapshot) {
			snapshot.AuditHeads["org-a"] = AuditHead{OrganizationID: "org-a", LastSequence: 1}
		},
		"secret": func(snapshot *Snapshot) {
			snapshot.Secrets["org-a/project-a/secret"] = []SecretVersion{{OrganizationID: "org-a", ProjectID: "project-a", ReferenceID: "secret", Version: 1}}
		},
		"secret lease": func(snapshot *Snapshot) {
			snapshot.SecretLeases["org-a/project-a/lease"] = SecretLease{ID: "lease"}
		},
		"quota": func(snapshot *Snapshot) {
			snapshot.Quotas["quota"] = QuotaCounter{OrganizationID: "bad/id", PolicyDigest: strings.Repeat("a", 64)}
		},
		"reservation": func(snapshot *Snapshot) {
			snapshot.Reservations["reservation"] = Reservation{PolicyDigest: strings.Repeat("a", 64)}
		},
		"deletion": func(snapshot *Snapshot) {
			snapshot.Deletions["deletion"] = DeletionIntent{EligibilityDigest: strings.Repeat("a", 64)}
		},
		"receipt": func(snapshot *Snapshot) {
			snapshot.Receipts["receipt"] = StoreReceipt{OrganizationID: "org-a", OperationID: "receipt"}
		},
		"binding": func(snapshot *Snapshot) {
			snapshot.Bindings["binding"] = TenantRunBinding{RunID: "binding"}
		},
		"record": func(snapshot *Snapshot) {
			snapshot.Records["record"] = ScopedRecord{OrganizationID: "org-a", ProjectID: "project-a", Class: EntityEvidence, ID: "record", Version: 1}
		},
		"job": func(snapshot *Snapshot) {
			snapshot.Jobs["job"] = ScopedJob{OrganizationID: "org-a", ProjectID: "project-a", JobID: "job", Fence: 1}
		},
		"message": func(snapshot *Snapshot) {
			snapshot.Messages["message"] = ScopedMessage{OrganizationID: "org-a", ProjectID: "project-a", Kind: MessageInbox, IdempotencyKey: "message", Fence: 1}
		},
	} {
		t.Run(name, func(t *testing.T) {
			candidate, err := CloneSnapshot(base)
			if err != nil {
				t.Fatal(err)
			}
			mutate(&candidate)
			if err = ValidateSnapshot(candidate); !IsCategory(err, CategoryIntegrity) {
				t.Fatalf("forged projection accepted at %s: %v", clock.now, err)
			}
		})
	}
}

// TestDurableAuthorityRejectsPartialSurfacesAndLiveStateDrift covers closed
// ingress composition, provider errors and current-head authorization denial.
func TestDurableAuthorityRejectsPartialSurfacesAndLiveStateDrift(t *testing.T) {
	if _, err := DigestSubject(AuthenticatedSubject{}); !IsCategory(err, CategoryInvalid) {
		t.Fatalf("empty subject=%v", err)
	}
	if _, err := NewDurableAuthority(nil, RealClock{}, DefaultLimits(), nil); !IsCategory(err, CategoryInvalid) {
		t.Fatalf("nil durable store=%v", err)
	}
	if _, err := NewProviderSurfaceGateway(nil, nil); !IsCategory(err, CategoryInvalid) {
		t.Fatalf("nil gateway=%v", err)
	}
	if _, err := (*SurfaceGateway)(nil).Authorize(context.Background(), SurfaceCLI, ActionRunRead, ResourceRun); !IsCategory(err, CategoryInvalid) {
		t.Fatalf("nil authorization=%v", err)
	}

	_, memory, clock, principal, identity := fixture(t)
	store := &testDurable{snapshot: memory.Snapshot()}
	authority, err := NewDurableAuthority(store, clock, DefaultLimits(), panicObserver{})
	if err != nil {
		t.Fatal(err)
	}
	binding := SurfaceBinding{Subject: AuthenticatedSubject{Issuer: "issuer", Subject: "subject"}, Resource: identity}
	bindings := map[Surface]SurfaceBinding{}
	for _, surface := range productSurfaces() {
		bindings[surface] = binding
	}
	gateway, err := NewSurfaceGateway(authority, bindings)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = gateway.Authorize(context.Background(), SurfaceCLI, ActionRunRead, ResourceRun); err != nil {
		t.Fatalf("observer panic escaped=%v", err)
	}
	if _, err = gateway.Authorize(context.Background(), Surface("unknown"), ActionRunRead, ResourceRun); !IsCategory(err, CategoryUnauthenticated) {
		t.Fatalf("unknown surface=%v", err)
	}
	providers := make(map[Surface]SurfaceBindingProvider, len(bindings))
	for surface, value := range bindings {
		providers[surface] = StaticSurfaceBindingProvider{Value: value}
	}
	providers[SurfaceAPI] = failingSurfaceProvider{err: errors.New("identity provider unavailable")}
	failingGateway, _ := NewProviderSurfaceGateway(authority, providers)
	if _, err = failingGateway.Authorize(context.Background(), SurfaceAPI, ActionRunRead, ResourceRun); !IsCategory(err, CategoryUnauthenticated) {
		t.Fatalf("provider failure=%v", err)
	}
	delete(providers, SurfaceWorker)
	if _, err = NewProviderSurfaceGateway(authority, providers); !IsCategory(err, CategoryInvalid) {
		t.Fatalf("partial surfaces=%v", err)
	}

	if _, err = (*DurableAuthority)(nil).ResolveAndAuthorize(context.Background(), binding.Subject, identity, ActionRunRead, ResourceRun); !IsCategory(err, CategoryInvalid) {
		t.Fatalf("nil authority=%v", err)
	}
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err = authority.ResolveAndAuthorize(cancelled, binding.Subject, identity, ActionRunRead, ResourceRun); !IsCategory(err, CategoryCancelled) {
		t.Fatalf("cancelled authority=%v", err)
	}
	if _, err = authority.ResolveAndAuthorize(context.Background(), AuthenticatedSubject{}, identity, ActionRunRead, ResourceRun); !IsCategory(err, CategoryUnauthenticated) {
		t.Fatalf("empty authority subject=%v", err)
	}
	snapshot := memory.Snapshot()
	delete(snapshot.Roles, roleKey(identity.OrganizationID, principal.Membership.RoleIDs[0]))
	store.snapshot = snapshot
	if _, err = authority.ResolveAndAuthorize(context.Background(), binding.Subject, identity, ActionRunRead, ResourceRun); !IsCategory(err, CategoryUnauthorized) {
		t.Fatalf("missing live role=%v", err)
	}
	if categoryOf(errors.New("opaque")) != CategoryUnavailable {
		t.Fatal("opaque adapter error was not redacted")
	}
}

// TestScopedBoundariesRejectForgeryDowngradeAndUnsafeRetention covers cache,
// object, transport and legacy deletion boundaries with current capabilities.
func TestScopedBoundariesRejectForgeryDowngradeAndUnsafeRetention(t *testing.T) {
	service, _, _, principal, identity := fixture(t)
	ctx := context.Background()
	evidence, _ := service.Authorize(ctx, principal, ActionEvidenceRead, ResourceEvidence, identity)
	if _, err := NewScopedKey(Capability{}, ResourceEvidence, "object"); !IsCategory(err, CategoryInvalid) {
		t.Fatalf("empty scoped key=%v", err)
	}
	key, _ := NewScopedKey(evidence, ResourceEvidence, "object")
	forged := key
	forged.Digest = strings.Repeat("f", 64)
	cache := NewScopedCache()
	if err := cache.Put(forged, []byte("value")); !IsCategory(err, CategoryInvalid) {
		t.Fatalf("forged cache put=%v", err)
	}
	if _, ok := cache.Get(forged); ok {
		t.Fatal("forged cache get succeeded")
	}
	cache.Delete(forged)
	objects := NewScopedObjectStore()
	if err := objects.Put(forged, []byte("value")); !IsCategory(err, CategoryInvalid) {
		t.Fatalf("forged object put=%v", err)
	}
	if _, err := objects.Get(key); !IsCategory(err, CategoryNotFoundHidden) {
		t.Fatalf("missing object=%v", err)
	}
	if err := objects.Delete(forged); !IsCategory(err, CategoryInvalid) {
		t.Fatalf("forged object delete=%v", err)
	}
	if err := objects.Put(key, []byte("value")); err != nil {
		t.Fatal(err)
	}
	objects.objects[key.Digest] = ScopedObject{Key: key, Payload: []byte("tampered"), PayloadDigest: strings.Repeat("a", 64)}
	if _, err := objects.Get(key); !IsCategory(err, CategoryIntegrity) {
		t.Fatalf("tampered object=%v", err)
	}

	if err := ValidateTransport(TransportSecurity{IsolatedLoopbackTest: true, ServerName: "127.0.0.1:5432"}); err != nil {
		t.Fatalf("isolated loopback=%v", err)
	}
	if err := ValidateTransport(TransportSecurity{IsolatedLoopbackTest: true, ServerName: "remote.example"}); !IsCategory(err, CategoryInvalid) {
		t.Fatalf("remote isolated test=%v", err)
	}
	production := TransportSecurity{TLS: true, PeerAuthenticated: true, ServerName: "database.example"}
	guard, err := NewTransportGuard(production)
	if err != nil {
		t.Fatal(err)
	}
	if err = guard.ValidateReconnect(production); err != nil {
		t.Fatal(err)
	}
	if err = guard.ValidateReconnect(TransportSecurity{TLS: true, PeerAuthenticated: true, ServerName: "other.example"}); !IsCategory(err, CategoryInvalid) {
		t.Fatalf("hostname downgrade=%v", err)
	}
	if err = (*TransportGuard)(nil).ValidateReconnect(production); !IsCategory(err, CategoryInvalid) {
		t.Fatalf("nil transport guard=%v", err)
	}

	floors, periods := map[RetentionClass]time.Duration{}, map[RetentionClass]time.Duration{}
	for _, class := range retentionClasses() {
		floors[class], periods[class] = time.Hour, time.Hour
	}
	basePolicy := RetentionPolicy{OrganizationID: "org-a", Version: 1, Floors: floors, Periods: periods, ProtectedRecords: map[RetentionClass][]string{}, BatchLimit: 2, GraphLimit: 2}
	for name, mutate := range map[string]func(*RetentionPolicy){
		"project":            func(policy *RetentionPolicy) { policy.ProjectID = "bad/id" },
		"unknown protected":  func(policy *RetentionPolicy) { policy.ProtectedRecords[RetentionClass("unknown")] = []string{"record"} },
		"bad resource":       func(policy *RetentionPolicy) { policy.Protected = []ResourceClass{ResourceClass("unknown")} },
		"period below floor": func(policy *RetentionPolicy) { policy.Periods[RetentionRun] = 0 },
		"duplicate record":   func(policy *RetentionPolicy) { policy.ProtectedRecords[RetentionRun] = []string{"same", "same"} },
		"invalid record":     func(policy *RetentionPolicy) { policy.ProtectedRecords[RetentionRun] = []string{"bad/id"} },
		"duplicate class":    func(policy *RetentionPolicy) { policy.Protected = []ResourceClass{ResourceRun, ResourceRun} },
	} {
		t.Run(name, func(t *testing.T) {
			candidate := basePolicy
			candidate.Floors, candidate.Periods, candidate.ProtectedRecords = maps.Clone(floors), maps.Clone(periods), map[RetentionClass][]string{}
			mutate(&candidate)
			if _, compileErr := CompileRetention(candidate); !IsCategory(compileErr, CategoryInvalid) {
				t.Fatalf("invalid retention=%v", compileErr)
			}
		})
	}

	retention, _ := service.Authorize(ctx, principal, ActionRetentionExecute, ResourceEvidence, identity)
	intent, err := service.PlanDeletion(ctx, retention, "legacy-boundary-delete", ResourceEvidence)
	if err != nil {
		t.Fatal(err)
	}
	if err = service.ExecuteDeletion(ctx, retention, intent, nil); !IsCategory(err, CategoryInvalid) {
		t.Fatalf("nil deleter=%v", err)
	}
	consumerFailure := errors.New("delete unavailable")
	if err = service.ExecuteDeletion(ctx, retention, intent, scriptedDeleter{err: consumerFailure}); !IsCategory(err, CategoryUnavailable) {
		t.Fatalf("delete failure=%v", err)
	}
	if err = service.ExecuteDeletion(ctx, retention, intent, scriptedDeleter{}); err != nil {
		t.Fatal(err)
	}
	storedKey := intent.Scope.OrganizationID + "/" + intent.Scope.ProjectID + "/" + intent.ID
	tamperedIntent := service.store.Deletions[storedKey]
	tamperedIntent.Digest = strings.Repeat("f", 64)
	service.store.Deletions[storedKey] = tamperedIntent
	if err = service.ExecuteDeletion(ctx, retention, intent, scriptedDeleter{}); !IsCategory(err, CategoryConflict) {
		t.Fatalf("stale delete=%v", err)
	}
}

// TestAdministrationRejectsDivergenceAndSupportsVersionedUpdates covers the
// optimistic administrative lifecycle rather than create-only mutations.
func TestAdministrationRejectsDivergenceAndSupportsVersionedUpdates(t *testing.T) {
	service, memory, clock, principal, identity := fixture(t)
	ctx := context.Background()
	capability, _ := service.Authorize(ctx, principal, ActionTenantAdmin, ResourceTenant, identity)
	store := &testDurable{snapshot: memory.Snapshot()}
	if _, err := NewAdministration(nil, clock, DefaultLimits()); !IsCategory(err, CategoryInvalid) {
		t.Fatalf("nil administration=%v", err)
	}
	admin, _ := NewAdministration(store, clock, DefaultLimits())
	project := ProjectRegistration{OrganizationID: identity.OrganizationID, ProjectID: "admin-versioned", RepositoryID: "repo-versioned", State: StateActive, Version: 1}
	mutation := AdministrationMutation{Kind: AdministrationProject, Project: &project}
	if _, err := admin.Apply(ctx, capability, "admin-create-versioned", 0, mutation); err != nil {
		t.Fatal(err)
	}
	current, _ := store.Snapshot(ctx)
	organization := current.Organizations[identity.OrganizationID]
	staleCapability := capability
	capability.Scope.SecurityRevision = organization.SecurityRevision
	// The capability scope digest itself is immutable and therefore an old
	// capability must be rejected after the security revision advances.
	if _, err := admin.Apply(ctx, staleCapability, "admin-stale-capability", 1, mutation); !IsCategory(err, CategoryUnauthorized) {
		t.Fatalf("stale capability=%v", err)
	}
	capability.Scope.Digest = digestStrings(capability.Scope.OrganizationID, capability.Scope.ProjectID, capability.Scope.RepositoryID, capability.Scope.PrincipalID, fmt.Sprint(capability.Scope.MembershipVersion), fmt.Sprint(capability.Scope.SecurityRevision), capability.Scope.PolicyDigest, capability.Scope.ExpiresAt.UTC().Format(time.RFC3339Nano))
	capability.Digest = digestStrings(capability.Scope.Digest, string(capability.Action), string(capability.Resource))
	project.RepositoryID, project.Version = "repo-versioned-v2", 2
	mutation.Project = &project
	if _, err := admin.Apply(ctx, capability, "admin-update-versioned", 1, mutation); err != nil {
		t.Fatal(err)
	}
	divergent := project
	divergent.RepositoryID = "repo-divergent"
	if _, err := admin.Apply(ctx, capability, "admin-update-versioned", 1, AdministrationMutation{Kind: AdministrationProject, Project: &divergent}); !IsCategory(err, CategoryConflict) {
		t.Fatalf("divergent replay=%v", err)
	}
	if _, err := admin.Apply(ctx, Capability{}, "admin-no-authority", 0, AdministrationMutation{Kind: AdministrationProject, Project: &ProjectRegistration{OrganizationID: identity.OrganizationID, ProjectID: "unauthorized", RepositoryID: "repo", State: StateActive, Version: 1}}); !IsCategory(err, CategoryUnauthorized) {
		t.Fatalf("missing admin authority=%v", err)
	}
	candidate := current
	if err := admin.applyMutation(&candidate, identity.OrganizationID, 0, AdministrationMutation{Kind: AdministrationProject, Project: &ProjectRegistration{OrganizationID: "other-org", ProjectID: "project", RepositoryID: "repo", State: StateActive, Version: 1}}); !IsCategory(err, CategoryUnauthorized) {
		t.Fatalf("cross-org project=%v", err)
	}
	if err := admin.applyMutation(&candidate, identity.OrganizationID, 0, AdministrationMutation{Kind: AdministrationRole, Role: &Role{OrganizationID: "other-org", ID: "role", Actions: []Action{ActionRunRead}, Resources: []ResourceClass{ResourceRun}, Version: 1}}); !IsCategory(err, CategoryUnauthorized) {
		t.Fatalf("cross-org role=%v", err)
	}
	if err := admin.applyMutation(&candidate, identity.OrganizationID, 0, AdministrationMutation{Kind: AdministrationMembership, Principal: &Principal{ID: "human", Kind: PrincipalHuman, SubjectDigest: strings.Repeat("a", 64), State: StateActive, Version: 1}, Membership: &Membership{OrganizationID: identity.OrganizationID, PrincipalID: "human", RoleIDs: []string{"missing-role"}, State: StateActive, Version: 1}}); !IsCategory(err, CategoryUnauthorized) {
		t.Fatalf("invalid membership=%v", err)
	}
	values := map[string]int{"existing": 1}
	if err := replaceVersioned(values, "existing", 1, 2, 2); !IsCategory(err, CategoryStale) {
		t.Fatalf("unversioned current value=%v", err)
	}
}

// TestAuditPaginationScopesCursorsAndDetectsTailTamper covers bounded paging,
// continuation expiry and independent terminal-head integrity.
func TestAuditPaginationScopesCursorsAndDetectsTailTamper(t *testing.T) {
	service, memory, clock, principal, identity := fixture(t)
	ctx := context.Background()
	capability, _ := service.Authorize(ctx, principal, ActionAuditRead, ResourceAudit, identity)
	for _, outcome := range []string{"allowed", "committed", "denied"} {
		if _, err := service.AppendAudit(ctx, capability, outcome, CategoryUnauthorized); err != nil {
			t.Fatal(err)
		}
	}
	store := &testDurable{snapshot: memory.Snapshot()}
	audits, err := NewAuditService(store, clock, 2, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if err = audits.Verify(ctx, identity.OrganizationID); err != nil {
		t.Fatal(err)
	}
	page, err := audits.Page(ctx, capability, "", 1)
	if err != nil || len(page.Events) != 1 || page.NextCursor == "" {
		t.Fatalf("first page=%+v err=%v", page, err)
	}
	next, err := audits.Page(ctx, capability, page.NextCursor, 2)
	if err != nil || len(next.Events) != 2 {
		t.Fatalf("next page=%+v err=%v", next, err)
	}
	clock.now = clock.now.Add(2 * time.Minute)
	if _, err = audits.Page(ctx, capability, page.NextCursor, 1); !IsCategory(err, CategoryInvalid) {
		t.Fatalf("expired cursor=%v", err)
	}
	snapshot, _ := store.Snapshot(ctx)
	snapshot.AuditHeads[identity.OrganizationID] = AuditHead{}
	store.snapshot = snapshot
	if err = audits.Verify(ctx, identity.OrganizationID); !IsCategory(err, CategoryIntegrity) {
		t.Fatalf("tail tamper=%v", err)
	}
	snapshot.Audits[identity.OrganizationID][0].Digest = strings.Repeat("f", 64)
	store.snapshot = snapshot
	if err = audits.Verify(ctx, identity.OrganizationID); !IsCategory(err, CategoryIntegrity) {
		t.Fatalf("event tamper=%v", err)
	}
}

// TestScopedRuntimeUpdatesAndIntegrityChecks covers optimistic record updates
// and rejects forged durable projections at every runtime read boundary.
func TestScopedRuntimeUpdatesAndIntegrityChecks(t *testing.T) {
	service, memory, clock, principal, identity := fixture(t)
	ctx := context.Background()
	admit, _ := service.Authorize(ctx, principal, ActionRunAdmit, ResourceRun, identity)
	run, _ := service.Authorize(ctx, principal, ActionRunRead, ResourceRun, identity)
	evidence, _ := service.Authorize(ctx, principal, ActionEvidenceRead, ResourceEvidence, identity)
	store := &testDurable{snapshot: memory.Snapshot()}
	admission, _ := NewAdmission(store, clock)
	coordinate := ConfigurationCoordinate{Generation: 1, VersionDigest: strings.Repeat("1", 64), PolicyDigest: admit.Scope.PolicyDigest, CatalogDigest: strings.Repeat("2", 64), EffectiveDigest: strings.Repeat("3", 64)}
	binding, _, err := admission.Admit(ctx, admit, "integrity-admit", "integrity-run", "plan", coordinate)
	if err != nil {
		t.Fatal(err)
	}
	wrongPolicy := coordinate
	wrongPolicy.PolicyDigest = strings.Repeat("f", 64)
	if _, _, err = admission.Admit(ctx, admit, "wrong-policy-admit", "wrong-policy-run", "plan", wrongPolicy); !IsCategory(err, CategoryUnauthorized) {
		t.Fatalf("wrong policy admission=%v", err)
	}
	forgedAdmit := admit
	forgedAdmit.Digest = strings.Repeat("f", 64)
	if _, _, err = admission.Admit(ctx, forgedAdmit, "forged-admit", "forged-run", "plan", coordinate); !IsCategory(err, CategoryUnauthorized) {
		t.Fatalf("forged admission=%v", err)
	}
	if _, _, err = admission.Admit(ctx, admit, "duplicate-run-admit", binding.RunID, "plan", coordinate); !IsCategory(err, CategoryConflict) {
		t.Fatalf("duplicate run=%v", err)
	}
	if _, err = admission.LoadBinding(ctx, run, "bad/id"); !IsCategory(err, CategoryInvalid) {
		t.Fatalf("invalid binding id=%v", err)
	}
	snapshot, _ := store.Snapshot(ctx)
	value := snapshot.Bindings[bindingKey(binding.Scope, binding.RunID)]
	value.Digest = strings.Repeat("f", 64)
	snapshot.Bindings[bindingKey(binding.Scope, binding.RunID)] = value
	store.snapshot = snapshot
	if _, err = admission.LoadBinding(ctx, run, binding.RunID); !IsCategory(err, CategoryIntegrity) {
		t.Fatalf("tampered binding=%v", err)
	}
	value.Digest = tenantRunBindingDigest(value)
	snapshot.Bindings[bindingKey(binding.Scope, binding.RunID)] = value
	store.snapshot = snapshot

	repository, _ := NewScopedRepository(store, clock)
	record, _, err := repository.Put(ctx, evidence, "record-create", EntityEvidence, "versioned-record", strings.Repeat("a", 64), 0)
	if err != nil {
		t.Fatal(err)
	}
	updated, _, err := repository.Put(ctx, evidence, "record-update", EntityEvidence, record.ID, strings.Repeat("b", 64), record.Version)
	if err != nil || updated.Version != 2 {
		t.Fatalf("updated=%+v err=%v", updated, err)
	}
	replayedRecord, replayReceipt, err := repository.Put(ctx, evidence, "record-update", EntityEvidence, record.ID, strings.Repeat("b", 64), record.Version)
	if err != nil || replayedRecord.Digest != updated.Digest || replayReceipt.OperationID == "" {
		t.Fatalf("record replay=%+v receipt=%+v err=%v", replayedRecord, replayReceipt, err)
	}
	if _, _, err = repository.Put(ctx, evidence, "record-update", EntityEvidence, record.ID, strings.Repeat("c", 64), record.Version); !IsCategory(err, CategoryConflict) {
		t.Fatalf("record divergent replay=%v", err)
	}
	snapshot, _ = store.Snapshot(ctx)
	recordKeyValue := recordKey(updated.OrganizationID, updated.ProjectID, updated.Class, updated.ID)
	forgedRecord := snapshot.Records[recordKeyValue]
	forgedRecord.Digest = strings.Repeat("f", 64)
	snapshot.Records[recordKeyValue] = forgedRecord
	store.snapshot = snapshot
	if _, err = repository.Get(ctx, evidence, updated.Class, updated.ID); !IsCategory(err, CategoryIntegrity) {
		t.Fatalf("tampered record=%v", err)
	}
	forgedRecord.Digest = scopedRecordDigest(forgedRecord)
	snapshot.Records[recordKeyValue] = forgedRecord
	store.snapshot = snapshot
	forgedCapability := evidence
	forgedCapability.Digest = strings.Repeat("f", 64)
	if _, err = repository.Get(ctx, forgedCapability, EntityEvidence, updated.ID); !IsCategory(err, CategoryUnauthorized) {
		t.Fatalf("forged get=%v", err)
	}
	if _, _, err = repository.Put(ctx, forgedCapability, "forged-put", EntityEvidence, "forged", strings.Repeat("c", 64), 0); !IsCategory(err, CategoryUnauthorized) {
		t.Fatalf("forged put=%v", err)
	}
	if _, err = repository.List(ctx, forgedCapability, EntityEvidence, "", 1); !IsCategory(err, CategoryUnauthorized) {
		t.Fatalf("forged list=%v", err)
	}
	bus, _ := NewMessageBus(store, clock)
	if _, err = bus.Accept(ctx, run, "invalid-message", MessageKind("unknown"), "delivery", strings.Repeat("d", 64)); !IsCategory(err, CategoryInvalid) {
		t.Fatalf("invalid message=%v", err)
	}
	if _, err = bus.Accept(ctx, forgedCapability, "forged-message", MessageInbox, "delivery", strings.Repeat("d", 64)); !IsCategory(err, CategoryUnauthorized) {
		t.Fatalf("forged message=%v", err)
	}
	if _, err = bus.Claim(ctx, run, "invalid-claim", MessageInbox, "delivery", "worker", 0); !IsCategory(err, CategoryInvalid) {
		t.Fatalf("zero fence claim=%v", err)
	}
	queue, _ := NewQueue(store, clock)
	if _, err = queue.Claim(ctx, run, "invalid-queue", "job", "worker", 0); !IsCategory(err, CategoryInvalid) {
		t.Fatalf("zero fence queue=%v", err)
	}
	if entityResource(EntityConfiguration) != ResourceConfig || entityResource(EntityEnvironment) != ResourceEnvironment || entityResource(EntityRun) != ResourceRun {
		t.Fatal("entity authorization mapping changed")
	}
}

// TestSensitiveExecutorReplayDivergenceAndDeniedAudit proves receipt replay
// bypasses mutation while divergent commands and denied actions remain audited.
func TestSensitiveExecutorReplayDivergenceAndDeniedAudit(t *testing.T) {
	service, memory, clock, principal, identity := fixture(t)
	capability, _ := service.Authorize(context.Background(), principal, ActionTenantAdmin, ResourceTenant, identity)
	store := &testDurable{snapshot: memory.Snapshot()}
	executor, _ := NewSensitiveExecutor(store, clock)
	action := SensitiveAction{Capability: capability, OperationID: "sensitive-replay", CommandDigest: strings.Repeat("a", 64), Outcome: "committed", ResourceID: "tenant"}
	calls := 0
	result, err := executor.Apply(context.Background(), action, func(*Snapshot) error { calls++; return nil })
	if err != nil || result.Receipt.OperationID == "" || calls != 1 {
		t.Fatalf("result=%+v calls=%d err=%v", result, calls, err)
	}
	replay, err := executor.Apply(context.Background(), action, func(*Snapshot) error { calls++; return nil })
	if err != nil || replay.Receipt != result.Receipt || replay.Audit.OperationID != action.OperationID || calls != 1 {
		t.Fatalf("replay=%+v calls=%d err=%v", replay, calls, err)
	}
	divergent := action
	divergent.CommandDigest = strings.Repeat("b", 64)
	if _, err = executor.Apply(context.Background(), divergent, func(*Snapshot) error { return nil }); !IsCategory(err, CategoryConflict) {
		t.Fatalf("divergent replay=%v", err)
	}
	mutationFailure := errors.New("candidate rejected")
	newAction := action
	newAction.OperationID = "sensitive-mutation-failure"
	if _, err = executor.Apply(context.Background(), newAction, func(*Snapshot) error { return mutationFailure }); !errors.Is(err, mutationFailure) {
		t.Fatalf("mutation failure=%v", err)
	}
	if err = executor.RecordDenied(context.Background(), capability, "sensitive-denied", "tenant", CategoryUnauthorized); !IsCategory(err, CategoryUnauthorized) {
		t.Fatalf("denied result=%v", err)
	}
	snapshot, _ := store.Snapshot(context.Background())
	if auditByOperation(snapshot, identity.OrganizationID, "sensitive-denied").Outcome != "denied" {
		t.Fatal("denied action was not audited")
	}
}

// TestSecretConsumeRejectsDurableLeaseDrift proves a lease consumed between the
// private preflight and atomic mutation cannot disclose plaintext or skip the denial.
func TestSecretConsumeRejectsDurableLeaseDrift(t *testing.T) {
	service, memory, clock, principal, identity := fixture(t)
	ctx := context.Background()
	admin, _ := service.Authorize(ctx, principal, ActionTenantAdmin, ResourceSecret, identity)
	resolve, _ := service.Authorize(ctx, principal, ActionSecretResolve, ResourceSecret, identity)
	base := &testDurable{snapshot: memory.Snapshot()}
	secrets, _ := NewSecretService(base, fakeKeys{}, fakeRandom{}, clock, DefaultLimits())
	if _, err := secrets.Create(ctx, admin, "drift-secret-create", "drift-secret", "deploy", "key-a", []byte("private")); err != nil {
		t.Fatal(err)
	}
	issued, err := secrets.IssueLease(ctx, resolve, "drift-lease-issue", "drift-lease", "drift-secret", "drift-run", "execute", "worker", "deploy", 1, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	drifting := &driftSnapshotDurable{testDurable: base, driftAt: 2, drift: func(snapshot *Snapshot) {
		key := secretLeaseKey(resolve.Scope, issued.Lease.ID)
		lease := snapshot.SecretLeases[key]
		lease.ConsumedAt = clock.Now().UTC()
		lease.Digest = secretLeaseDigest(lease)
		snapshot.SecretLeases[key] = lease
	}}
	consumer, _ := NewSecretService(drifting, fakeKeys{}, fakeRandom{}, clock, DefaultLimits())
	callbacks := 0
	err = consumer.WithSecret(ctx, resolve, "drift-lease-consume", issued.Lease, secretCaller(issued.Lease), func([]byte) error {
		callbacks++
		return nil
	})
	if !IsCategory(err, CategoryRevoked) || callbacks != 0 {
		t.Fatalf("drift consume callbacks=%d err=%v", callbacks, err)
	}
}

// TestScopedRuntimePropagatesDurableAdapterFailures proves every admission,
// record, delivery and queue boundary stops on authoritative storage errors.
func TestScopedRuntimePropagatesDurableAdapterFailures(t *testing.T) {
	service, memory, clock, principal, identity := fixture(t)
	ctx := context.Background()
	admit, _ := service.Authorize(ctx, principal, ActionRunAdmit, ResourceRun, identity)
	run, _ := service.Authorize(ctx, principal, ActionRunRead, ResourceRun, identity)
	evidence, _ := service.Authorize(ctx, principal, ActionEvidenceRead, ResourceEvidence, identity)
	base := &testDurable{snapshot: memory.Snapshot()}
	coordinate := ConfigurationCoordinate{Generation: 1, VersionDigest: strings.Repeat("1", 64), PolicyDigest: admit.Scope.PolicyDigest, CatalogDigest: strings.Repeat("2", 64), EffectiveDigest: strings.Repeat("3", 64)}
	adapterFailure := failure(CategoryUnavailable, "adapter", nil)

	receiptFault := faultDurable{DurableStore: base, receiptErr: adapterFailure}
	admission, _ := NewAdmission(receiptFault, clock)
	if _, _, err := admission.Admit(ctx, admit, "receipt-fault-admit", "receipt-fault-run", "plan", coordinate); !IsCategory(err, CategoryUnavailable) {
		t.Fatalf("admission receipt failure=%v", err)
	}
	repository, _ := NewScopedRepository(receiptFault, clock)
	if _, _, err := repository.Put(ctx, evidence, "receipt-fault-put", EntityEvidence, "record", strings.Repeat("a", 64), 0); !IsCategory(err, CategoryUnavailable) {
		t.Fatalf("record receipt failure=%v", err)
	}

	snapshotFault := faultDurable{DurableStore: base, snapshotErr: adapterFailure}
	admission, _ = NewAdmission(snapshotFault, clock)
	if _, _, err := admission.Admit(ctx, admit, "snapshot-fault-admit", "snapshot-fault-run", "plan", coordinate); !IsCategory(err, CategoryUnavailable) {
		t.Fatalf("admission snapshot failure=%v", err)
	}
	repository, _ = NewScopedRepository(snapshotFault, clock)
	if _, _, err := repository.Put(ctx, evidence, "snapshot-fault-put", EntityEvidence, "record", strings.Repeat("a", 64), 0); !IsCategory(err, CategoryUnavailable) {
		t.Fatalf("record snapshot failure=%v", err)
	}
	if _, err := repository.Get(ctx, evidence, EntityEvidence, "record"); !IsCategory(err, CategoryUnavailable) {
		t.Fatalf("record get failure=%v", err)
	}
	if _, err := repository.List(ctx, evidence, EntityEvidence, "", 1); !IsCategory(err, CategoryUnavailable) {
		t.Fatalf("record list failure=%v", err)
	}
	bus, _ := NewMessageBus(snapshotFault, clock)
	if _, err := bus.Accept(ctx, run, "message-fault", MessageInbox, "delivery", strings.Repeat("b", 64)); !IsCategory(err, CategoryUnavailable) {
		t.Fatalf("message accept failure=%v", err)
	}
	if _, err := bus.Claim(ctx, run, "message-claim-fault", MessageInbox, "delivery", "worker", 1); !IsCategory(err, CategoryUnavailable) {
		t.Fatalf("message claim failure=%v", err)
	}
	queue, _ := NewQueue(snapshotFault, clock)
	if _, err := queue.Claim(ctx, run, "queue-fault", "job", "worker", 1); !IsCategory(err, CategoryUnavailable) {
		t.Fatalf("queue failure=%v", err)
	}

	seedAdmission, _ := NewAdmission(base, clock)
	seedBinding, _, err := seedAdmission.Admit(ctx, admit, "fault-seed-admit", "fault-seed-run", "plan", coordinate)
	if err != nil {
		t.Fatal(err)
	}
	seedBus, _ := NewMessageBus(base, clock)
	if _, err = seedBus.Accept(ctx, run, "fault-seed-message", MessageInbox, "fault-delivery", strings.Repeat("d", 64)); err != nil {
		t.Fatal(err)
	}
	commitFault := faultDurable{DurableStore: base, commitErr: adapterFailure}
	admission, _ = NewAdmission(commitFault, clock)
	if _, _, err := admission.Admit(ctx, admit, "commit-fault-admit", "commit-fault-run", "plan", coordinate); !IsCategory(err, CategoryUnavailable) {
		t.Fatalf("admission commit failure=%v", err)
	}
	repository, _ = NewScopedRepository(commitFault, clock)
	if _, _, err := repository.Put(ctx, evidence, "commit-fault-put", EntityEvidence, "record", strings.Repeat("c", 64), 0); !IsCategory(err, CategoryUnavailable) {
		t.Fatalf("record commit failure=%v", err)
	}
	bus, _ = NewMessageBus(commitFault, clock)
	if _, err := bus.Accept(ctx, run, "commit-fault-message", MessageOutbox, "new-delivery", strings.Repeat("e", 64)); !IsCategory(err, CategoryUnavailable) {
		t.Fatalf("message commit failure=%v", err)
	}
	if _, err := bus.Claim(ctx, run, "commit-fault-claim", MessageInbox, "fault-delivery", "worker", 1); !IsCategory(err, CategoryUnavailable) {
		t.Fatalf("message claim commit failure=%v", err)
	}
	queue, _ = NewQueue(commitFault, clock)
	if _, err := queue.Claim(ctx, run, "commit-fault-queue", seedBinding.RunID+"-first", "worker", 1); !IsCategory(err, CategoryUnavailable) {
		t.Fatalf("queue commit failure=%v", err)
	}
}
