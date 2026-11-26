package local

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"github.com/asierhr/featureProof/internal/tenantsecurity"
	"github.com/asierhr/featureProof/internal/tenantsecurity/contracttest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// localAdminClock fixes administrative expiry and audit timestamps.
type localAdminClock struct{ now time.Time }

// Now returns the test-owned administrative instant.
func (clock localAdminClock) Now() time.Time { return clock.now }

// TestStoreRestartAndCorruption verifies atomic reload and fail-closed integrity.
func TestStoreRestartAndCorruption(t *testing.T) {
	root := t.TempDir()
	store, err := New(root)
	if err != nil {
		t.Fatal(err)
	}
	state := tenantsecurity.NewMemoryStore()
	if err = store.Save(state); err != nil {
		t.Fatal(err)
	}
	if _, err = store.Load(); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, "security.json")
	document, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	document[len(document)/2] ^= 1
	if err = os.WriteFile(path, document, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err = store.Load(); err == nil {
		t.Fatal("corruption accepted")
	}
}

// TestStoreSerializesSecondProcess proves the OS lock fences an independent executable.
func TestStoreSerializesSecondProcess(t *testing.T) {
	if root := os.Getenv("FEATUREPROOF_TENANT_LOCAL_CHILD"); root != "" {
		ready := filepath.Join(root, "child-ready")
		release := filepath.Join(root, "child-release")
		store, err := NewWithFault(root, func(boundary string) error {
			if boundary != "before_write" {
				return nil
			}
			if writeErr := os.WriteFile(ready, []byte("ready"), 0o600); writeErr != nil {
				return writeErr
			}
			for {
				if _, statErr := os.Stat(release); statErr == nil {
					return nil
				}
				time.Sleep(5 * time.Millisecond)
			}
		})
		if err != nil {
			t.Fatal(err)
		}
		if _, err = store.Commit(context.Background(), localTestCommand("child-org", "child", "b")); err != nil {
			t.Fatal(err)
		}
		return
	}
	root := t.TempDir()
	command := exec.Command(os.Args[0], "-test.run=^TestStoreSerializesSecondProcess$")
	command.Env = append(os.Environ(), "FEATUREPROOF_TENANT_LOCAL_CHILD="+root)
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	ready := filepath.Join(root, "child-ready")
	deadline := time.Now().Add(5 * time.Second)
	for {
		if _, err := os.Stat(ready); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("child did not acquire the store lock")
		}
		time.Sleep(5 * time.Millisecond)
	}
	parent, _ := New(root)
	result := make(chan error, 1)
	go func() {
		_, err := parent.Commit(context.Background(), localTestCommand("parent-org", "parent", "c"))
		result <- err
	}()
	select {
	case err := <-result:
		t.Fatalf("parent crossed child lock: %v", err)
	case <-time.After(50 * time.Millisecond):
	}
	if err := os.WriteFile(filepath.Join(root, "child-release"), []byte("release"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := command.Wait(); err != nil {
		t.Fatal(err)
	}
	if err := <-result; !tenantsecurity.IsCategory(err, tenantsecurity.CategoryStale) {
		t.Fatalf("parent result=%v", err)
	}
}

// localTestCommand creates one valid first-epoch aggregate for lock tests.
func localTestCommand(organizationID, operationID, digestRune string) tenantsecurity.StoreCommand {
	snapshot := tenantsecurity.NewMemoryStore().Snapshot()
	snapshot.Epoch = 1
	snapshot.Organizations[organizationID] = tenantsecurity.Organization{ID: organizationID, State: tenantsecurity.StateActive, Version: 1, SecurityRevision: 1, PolicyDigest: strings.Repeat("a", 64)}
	return tenantsecurity.StoreCommand{OrganizationID: organizationID, OperationID: operationID, CommandDigest: strings.Repeat(digestRune, 64), ExpectedEpoch: 0, Snapshot: snapshot}
}

// TestAdministrationCommitsMutationAuditAndReceipt exercises every T6 entity through local durability.
func TestAdministrationCommitsMutationAuditAndReceipt(t *testing.T) {
	clock := localAdminClock{now: time.Date(2026, 11, 20, 10, 0, 0, 0, time.UTC)}
	root := t.TempDir()
	store, capability := localAdministrationFixture(t, root, clock, nil)
	admin, err := tenantsecurity.NewAdministration(store, clock, tenantsecurity.DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	project := tenantsecurity.ProjectRegistration{OrganizationID: "org-admin", ProjectID: "project-b", RepositoryID: "repo-b", State: tenantsecurity.StateActive, Version: 1}
	result, err := admin.Apply(context.Background(), capability, "project-create", 0, tenantsecurity.AdministrationMutation{Kind: tenantsecurity.AdministrationProject, Project: &project})
	if err != nil || result.Audit.Sequence != 1 {
		t.Fatalf("project result=%+v err=%v", result, err)
	}
	replay, err := admin.Apply(context.Background(), capability, "project-create", 0, tenantsecurity.AdministrationMutation{Kind: tenantsecurity.AdministrationProject, Project: &project})
	if err != nil || replay.Receipt != result.Receipt {
		t.Fatalf("replay=%+v err=%v", replay, err)
	}
	divergent := project
	divergent.RepositoryID = "repo-other"
	if _, err = admin.Apply(context.Background(), capability, "project-create", 0, tenantsecurity.AdministrationMutation{Kind: tenantsecurity.AdministrationProject, Project: &divergent}); !tenantsecurity.IsCategory(err, tenantsecurity.CategoryConflict) {
		t.Fatalf("divergent replay=%v", err)
	}
	capability = localReloadAdminCapability(t, store, clock)
	role, err := tenantsecurity.CompileRole(tenantsecurity.Role{OrganizationID: "org-admin", ID: "auditor", Actions: []tenantsecurity.Action{tenantsecurity.ActionRunRead}, Resources: []tenantsecurity.ResourceClass{tenantsecurity.ResourceRun}, ProjectIDs: []string{"project-b"}, Version: 1}, tenantsecurity.DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	if _, err = admin.Apply(context.Background(), capability, "role-create", 0, tenantsecurity.AdministrationMutation{Kind: tenantsecurity.AdministrationRole, Role: &role}); err != nil {
		t.Fatal(err)
	}
	capability = localReloadAdminCapability(t, store, clock)
	member := tenantsecurity.Principal{ID: "member-b", Kind: tenantsecurity.PrincipalHuman, SubjectDigest: strings.Repeat("b", 64), State: tenantsecurity.StateActive, Version: 1}
	membership := tenantsecurity.Membership{OrganizationID: "org-admin", PrincipalID: member.ID, RoleIDs: []string{role.ID}, ProjectIDs: []string{"project-b"}, State: tenantsecurity.StateActive, ValidFrom: clock.now.Add(-time.Minute), ValidUntil: clock.now.Add(time.Hour), Version: 1}
	if _, err = admin.Apply(context.Background(), capability, "member-create", 0, tenantsecurity.AdministrationMutation{Kind: tenantsecurity.AdministrationMembership, Principal: &member, Membership: &membership}); err != nil {
		t.Fatal(err)
	}
	capability = localReloadAdminCapability(t, store, clock)
	servicePrincipal := tenantsecurity.Principal{ID: "service-b", Kind: tenantsecurity.PrincipalService, SubjectDigest: strings.Repeat("c", 64), State: tenantsecurity.StateActive, Version: 1}
	serviceIdentity := tenantsecurity.ServiceIdentity{OrganizationID: "org-admin", PrincipalID: servicePrincipal.ID, OwnerPrincipalID: "admin", Purpose: "worker", ExpiresAt: clock.now.Add(time.Hour), State: tenantsecurity.StateActive, Version: 1}
	if _, err = admin.Apply(context.Background(), capability, "service-create", 0, tenantsecurity.AdministrationMutation{Kind: tenantsecurity.AdministrationServiceIdentity, Principal: &servicePrincipal, ServiceIdentity: &serviceIdentity}); err != nil {
		t.Fatal(err)
	}
	snapshot, err := store.Snapshot(context.Background())
	if err != nil || len(snapshot.Audits["org-admin"]) != 4 || len(snapshot.Receipts) != 4 || snapshot.ServiceIdentities["org-admin/service-b"].Purpose != "worker" {
		t.Fatalf("snapshot audits=%d receipts=%d services=%d err=%v", len(snapshot.Audits["org-admin"]), len(snapshot.Receipts), len(snapshot.ServiceIdentities), err)
	}
}

// TestAdministrationFaultsAreAtomicAndUnknownCommitResolves verifies both sides of the local publication boundary.
func TestAdministrationFaultsAreAtomicAndUnknownCommitResolves(t *testing.T) {
	clock := localAdminClock{now: time.Date(2026, 11, 20, 10, 0, 0, 0, time.UTC)}
	project := tenantsecurity.ProjectRegistration{OrganizationID: "org-admin", ProjectID: "project-fault", RepositoryID: "repo-fault", State: tenantsecurity.StateActive, Version: 1}
	failing, capability := localAdministrationFixture(t, t.TempDir(), clock, func(boundary string) error {
		if boundary == "after_temp_sync" {
			return errors.New("injected")
		}
		return nil
	})
	admin, _ := tenantsecurity.NewAdministration(failing, clock, tenantsecurity.DefaultLimits())
	if _, err := admin.Apply(context.Background(), capability, "fault-op", 0, tenantsecurity.AdministrationMutation{Kind: tenantsecurity.AdministrationProject, Project: &project}); !tenantsecurity.IsCategory(err, tenantsecurity.CategoryUnavailable) {
		t.Fatalf("fault=%v", err)
	}
	snapshot, err := failing.Snapshot(context.Background())
	if err != nil || len(snapshot.Audits["org-admin"]) != 0 || len(snapshot.Receipts) != 0 {
		t.Fatalf("partial state=%+v err=%v", snapshot, err)
	}
	unknown, capability := localAdministrationFixture(t, t.TempDir(), clock, func(boundary string) error {
		if boundary == "after_rename" {
			return errors.New("lost response")
		}
		return nil
	})
	admin, _ = tenantsecurity.NewAdministration(unknown, clock, tenantsecurity.DefaultLimits())
	if _, err = admin.Apply(context.Background(), capability, "unknown-op", 0, tenantsecurity.AdministrationMutation{Kind: tenantsecurity.AdministrationProject, Project: &project}); err != nil {
		t.Fatalf("unknown commit not resolved=%v", err)
	}
	if _, err = unknown.Receipt(context.Background(), "org-admin", "unknown-op"); err != nil {
		t.Fatal(err)
	}
}

// localAdministrationFixture creates one durable admin organization and returns its initial capability.
func localAdministrationFixture(t *testing.T, root string, clock localAdminClock, fault func(string) error) (*Store, tenantsecurity.Capability) {
	t.Helper()
	memory := tenantsecurity.NewMemoryStore()
	service, _ := tenantsecurity.NewService(memory, clock, tenantsecurity.DefaultLimits(), nil)
	organization := tenantsecurity.Organization{ID: "org-admin", State: tenantsecurity.StateActive, Version: 1, SecurityRevision: 1, PolicyDigest: strings.Repeat("a", 64)}
	project := tenantsecurity.ProjectRegistration{OrganizationID: organization.ID, ProjectID: "project-admin", RepositoryID: "repo-admin", State: tenantsecurity.StateActive, Version: 1}
	principal := tenantsecurity.Principal{ID: "admin", Kind: tenantsecurity.PrincipalHuman, SubjectDigest: tenantsecuritySubjectDigest("issuer", "admin-subject"), State: tenantsecurity.StateActive, Version: 1}
	membership := tenantsecurity.Membership{OrganizationID: organization.ID, PrincipalID: principal.ID, RoleIDs: []string{"administrator"}, ProjectIDs: []string{project.ProjectID}, State: tenantsecurity.StateActive, ValidFrom: clock.now.Add(-time.Hour), ValidUntil: clock.now.Add(time.Hour), Version: 1}
	role := tenantsecurity.Role{OrganizationID: organization.ID, ID: "administrator", Actions: []tenantsecurity.Action{tenantsecurity.ActionTenantAdmin}, Resources: []tenantsecurity.ResourceClass{tenantsecurity.ResourceTenant}, ProjectIDs: []string{project.ProjectID}, Version: 1}
	if err := service.Bootstrap(context.Background(), organization, project, principal, membership, []tenantsecurity.Role{role}); err != nil {
		t.Fatal(err)
	}
	store, err := New(root)
	if err != nil {
		t.Fatal(err)
	}
	if err = store.Save(memory); err != nil {
		t.Fatal(err)
	}
	store.fault = fault
	return store, localReloadAdminCapability(t, store, clock)
}

// localReloadAdminCapability reloads durable state and issues authority at its current security revision.
func localReloadAdminCapability(t *testing.T, store *Store, clock localAdminClock) tenantsecurity.Capability {
	t.Helper()
	memory, err := store.Load()
	if err != nil {
		t.Fatal(err)
	}
	service, _ := tenantsecurity.NewService(memory, clock, tenantsecurity.DefaultLimits(), nil)
	identity := tenantsecurity.TrustedResourceIdentity{OrganizationID: "org-admin", ProjectID: "project-admin", RepositoryID: "repo-admin"}
	principal, err := service.Resolve(context.Background(), tenantsecurity.AuthenticatedSubject{Issuer: "issuer", Subject: "admin-subject"}, identity)
	if err != nil {
		t.Fatal(err)
	}
	capability, err := service.Authorize(context.Background(), principal, tenantsecurity.ActionTenantAdmin, tenantsecurity.ResourceTenant, identity)
	if err != nil {
		t.Fatal(err)
	}
	return capability
}

// tenantsecuritySubjectDigest mirrors the public subject mapping contract without exposing raw subject storage.
func tenantsecuritySubjectDigest(issuer, subject string) string {
	hash := sha256.New()
	for _, value := range []string{issuer, subject} {
		_, _ = hash.Write([]byte(fmt.Sprintf("%d:", len(value))))
		_, _ = hash.Write([]byte(value))
	}
	return hex.EncodeToString(hash.Sum(nil))
}

// TestScopedAdmissionRepositoryQueueCacheAndObjects covers the T7–T10 isolation vertical locally.
func TestScopedAdmissionRepositoryQueueCacheAndObjects(t *testing.T) {
	clock := localAdminClock{now: time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)}
	store, capabilities := localScopedRuntimeFixture(t, clock)
	configurationA := tenantsecurity.ConfigurationCoordinate{Generation: 1, VersionDigest: strings.Repeat("1", 64), PolicyDigest: strings.Repeat("a", 64), CatalogDigest: strings.Repeat("2", 64), EffectiveDigest: strings.Repeat("3", 64)}
	configurationB := configurationA
	configurationB.PolicyDigest = strings.Repeat("b", 64)
	admission, _ := tenantsecurity.NewAdmission(store, clock)
	bindingA, receiptA, err := admission.Admit(context.Background(), capabilities["org-a/admit"], "admit-a", "shared-run", "plan", configurationA)
	if err != nil || bindingA.Scope.OrganizationID != "org-a" || receiptA.Epoch != 1 {
		t.Fatalf("admit a binding=%+v receipt=%+v err=%v", bindingA, receiptA, err)
	}
	bindingB, _, err := admission.Admit(context.Background(), capabilities["org-b/admit"], "admit-b", "shared-run", "plan", configurationB)
	if err != nil || bindingB.Scope.OrganizationID != "org-b" || bindingB.Digest == bindingA.Digest {
		t.Fatalf("admit b binding=%+v err=%v", bindingB, err)
	}
	wrongPolicy := configurationA
	wrongPolicy.PolicyDigest = strings.Repeat("f", 64)
	if _, _, err = admission.Admit(context.Background(), capabilities["org-a/admit"], "admit-drift", "drift-run", "plan", wrongPolicy); !tenantsecurity.IsCategory(err, tenantsecurity.CategoryUnauthorized) {
		t.Fatalf("policy drift=%v", err)
	}
	if _, err = admission.LoadBinding(context.Background(), capabilities["org-b/run"], "shared-run"); err != nil {
		t.Fatal(err)
	}
	if _, err = admission.LoadBinding(context.Background(), capabilities["org-b/run"], "missing-run"); !tenantsecurity.IsCategory(err, tenantsecurity.CategoryNotFoundHidden) {
		t.Fatalf("missing binding=%v", err)
	}
	repository, _ := tenantsecurity.NewScopedRepository(store, clock)
	recordA, _, err := repository.Put(context.Background(), capabilities["org-a/evidence"], "package-a", tenantsecurity.EntityPackage, "shared-package", strings.Repeat("4", 64), 0)
	if err != nil {
		t.Fatal(err)
	}
	recordB, _, err := repository.Put(context.Background(), capabilities["org-b/evidence"], "package-b", tenantsecurity.EntityPackage, "shared-package", strings.Repeat("5", 64), 0)
	if err != nil || recordA.Digest == recordB.Digest {
		t.Fatalf("record b=%+v err=%v", recordB, err)
	}
	page, err := repository.List(context.Background(), capabilities["org-a/evidence"], tenantsecurity.EntityPackage, "", 1)
	if err != nil || len(page.Records) != 1 || page.Records[0].OrganizationID != "org-a" {
		t.Fatalf("page=%+v err=%v", page, err)
	}
	if _, err = repository.List(context.Background(), capabilities["org-b/evidence"], tenantsecurity.EntityPackage, page.Cursor+"tamper", 1); !tenantsecurity.IsCategory(err, tenantsecurity.CategoryInvalid) {
		t.Fatalf("cursor tamper=%v", err)
	}
	queue, _ := tenantsecurity.NewQueue(store, clock)
	claimed, err := queue.Claim(context.Background(), capabilities["org-a/run"], "claim-a", "shared-run-first", "worker-a", 1)
	if err != nil || claimed.Fence != 2 || claimed.Owner != "worker-a" {
		t.Fatalf("claim=%+v err=%v", claimed, err)
	}
	if _, err = queue.Claim(context.Background(), capabilities["org-b/run"], "claim-b-wrong", "shared-run-first", "worker-b", 1); err != nil {
		// Org B has an independent same-ID job and is allowed to claim only its own copy.
		t.Fatal(err)
	}
	if _, err = queue.Complete(context.Background(), capabilities["org-a/run"], "complete-stale", "shared-run-first", "worker-a", 1); !tenantsecurity.IsCategory(err, tenantsecurity.CategoryStale) {
		t.Fatalf("stale fence=%v", err)
	}
	completed, err := queue.Complete(context.Background(), capabilities["org-a/run"], "complete-a", "shared-run-first", "worker-a", 2)
	if err != nil || completed.State != "completed" {
		t.Fatalf("complete=%+v err=%v", completed, err)
	}
	keyA, _ := tenantsecurity.NewScopedKey(capabilities["org-a/evidence"], tenantsecurity.ResourceEvidence, "shared-object")
	keyB, _ := tenantsecurity.NewScopedKey(capabilities["org-b/evidence"], tenantsecurity.ResourceEvidence, "shared-object")
	cache := tenantsecurity.NewScopedCache()
	if err = cache.Put(keyA, []byte("a")); err != nil {
		t.Fatal(err)
	}
	if _, ok := cache.Get(keyB); ok {
		t.Fatal("cache leaked cross tenant")
	}
	objects := tenantsecurity.NewScopedObjectStore()
	if err = objects.Put(keyA, []byte("object-a")); err != nil {
		t.Fatal(err)
	}
	if _, err = objects.Get(keyB); !tenantsecurity.IsCategory(err, tenantsecurity.CategoryNotFoundHidden) {
		t.Fatalf("object leak=%v", err)
	}
	forged := keyA
	forged.OrganizationID = "org-b"
	if _, err = objects.Get(forged); !tenantsecurity.IsCategory(err, tenantsecurity.CategoryInvalid) {
		t.Fatalf("forged key=%v", err)
	}
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err = repository.Get(cancelled, capabilities["org-a/evidence"], tenantsecurity.EntityPackage, "shared-package"); !tenantsecurity.IsCategory(err, tenantsecurity.CategoryCancelled) {
		t.Fatalf("cancel=%v", err)
	}
}

// localScopedRuntimeFixture creates two tenants with identical opaque product IDs and isolated capabilities.
func localScopedRuntimeFixture(t *testing.T, clock localAdminClock) (*Store, map[string]tenantsecurity.Capability) {
	t.Helper()
	memory := tenantsecurity.NewMemoryStore()
	service, _ := tenantsecurity.NewService(memory, clock, tenantsecurity.DefaultLimits(), nil)
	capabilities := map[string]tenantsecurity.Capability{}
	for _, suffix := range []string{"a", "b"} {
		organizationID := "org-" + suffix
		organization := tenantsecurity.Organization{ID: organizationID, State: tenantsecurity.StateActive, Version: 1, SecurityRevision: 1, PolicyDigest: strings.Repeat(suffix, 64)}
		project := tenantsecurity.ProjectRegistration{OrganizationID: organizationID, ProjectID: "shared-project", RepositoryID: "shared-repo", State: tenantsecurity.StateActive, Version: 1}
		principal := tenantsecurity.Principal{ID: "principal", Kind: tenantsecurity.PrincipalHuman, SubjectDigest: strings.Repeat(suffix, 64), State: tenantsecurity.StateActive, Version: 1}
		membership := tenantsecurity.Membership{OrganizationID: organizationID, PrincipalID: principal.ID, RoleIDs: []string{"operator"}, ProjectIDs: []string{project.ProjectID}, State: tenantsecurity.StateActive, ValidFrom: clock.now.Add(-time.Hour), ValidUntil: clock.now.Add(time.Hour), Version: 1}
		role := tenantsecurity.Role{OrganizationID: organizationID, ID: "operator", Actions: []tenantsecurity.Action{tenantsecurity.ActionRunAdmit, tenantsecurity.ActionRunRead, tenantsecurity.ActionEvidenceRead}, Resources: []tenantsecurity.ResourceClass{tenantsecurity.ResourceRun, tenantsecurity.ResourceEvidence}, ProjectIDs: []string{project.ProjectID}, Version: 1}
		if err := service.Bootstrap(context.Background(), organization, project, principal, membership, []tenantsecurity.Role{role}); err != nil {
			t.Fatal(err)
		}
		principalContext := tenantsecurity.PrincipalContext{Principal: principal, Membership: membership, Organization: organization}
		identity := tenantsecurity.TrustedResourceIdentity{OrganizationID: organizationID, ProjectID: project.ProjectID, RepositoryID: project.RepositoryID}
		capabilities[organizationID+"/admit"], _ = service.Authorize(context.Background(), principalContext, tenantsecurity.ActionRunAdmit, tenantsecurity.ResourceRun, identity)
		capabilities[organizationID+"/run"], _ = service.Authorize(context.Background(), principalContext, tenantsecurity.ActionRunRead, tenantsecurity.ResourceRun, identity)
		capabilities[organizationID+"/evidence"], _ = service.Authorize(context.Background(), principalContext, tenantsecurity.ActionEvidenceRead, tenantsecurity.ResourceEvidence, identity)
	}
	store, err := New(t.TempDir())
	if err != nil || store.Save(memory) != nil {
		t.Fatalf("store err=%v", err)
	}
	return store, capabilities
}

// TestStoreContract runs the adapter-neutral tenant isolation and receipt contract locally.
func TestStoreContract(t *testing.T) {
	root := t.TempDir()
	err := contracttest.Run(context.Background(), func() (tenantsecurity.DurableStore, error) { return New(root) }, "local")
	if err != nil {
		t.Fatal(err)
	}
}

// TestScopedRuntimeContract runs tenant admission, product records and queue fencing locally.
func TestScopedRuntimeContract(t *testing.T) {
	root := t.TempDir()
	if err := contracttest.RunScopedRuntime(context.Background(), func() (tenantsecurity.DurableStore, error) { return New(root) }, "local-runtime"); err != nil {
		t.Fatal(err)
	}
}

// TestStoreUnknownCommitAndFaultBoundaries proves receipts resolve lost responses and pre-rename failures do not mutate.
func TestStoreUnknownCommitAndFaultBoundaries(t *testing.T) {
	root := t.TempDir()
	state := tenantsecurity.NewMemoryStore().Snapshot()
	state.Epoch = 1
	state.Organizations["org"] = tenantsecurity.Organization{ID: "org", State: tenantsecurity.StateActive, Version: 1, SecurityRevision: 1, PolicyDigest: strings.Repeat("a", 64)}
	command := tenantsecurity.StoreCommand{OrganizationID: "org", OperationID: "operation", CommandDigest: strings.Repeat("b", 64), ExpectedEpoch: 0, Snapshot: state}
	store, err := NewWithFault(root, func(boundary string) error {
		if boundary == "after_rename" {
			return errors.New("lost response")
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = store.Commit(context.Background(), command); !tenantsecurity.IsCategory(err, tenantsecurity.CategoryUnavailable) {
		t.Fatalf("unknown commit category: %v", err)
	}
	if _, err = store.Receipt(context.Background(), "org", "operation"); err != nil {
		t.Fatalf("durable receipt: %v", err)
	}
	preRoot := t.TempDir()
	pre, _ := NewWithFault(preRoot, func(boundary string) error {
		if boundary == "after_temp_sync" {
			return errors.New("disk fault")
		}
		return nil
	})
	if _, err = pre.Commit(context.Background(), command); !tenantsecurity.IsCategory(err, tenantsecurity.CategoryUnavailable) {
		t.Fatalf("precommit category: %v", err)
	}
	loaded, err := pre.Snapshot(context.Background())
	if err != nil || loaded.Epoch != 0 || len(loaded.Receipts) != 0 {
		t.Fatalf("precommit mutation: epoch=%d receipts=%d err=%v", loaded.Epoch, len(loaded.Receipts), err)
	}
}

// TestStoreSerializesIndependentHandles proves only one process-style handle can win the same expected epoch.
func TestStoreSerializesIndependentHandles(t *testing.T) {
	root := t.TempDir()
	first, _ := New(root)
	second, _ := New(root)
	results := make(chan error, 2)
	var wait sync.WaitGroup
	for index, store := range []*Store{first, second} {
		wait.Add(1)
		go func(index int, store *Store) {
			defer wait.Done()
			state := tenantsecurity.NewMemoryStore().Snapshot()
			state.Epoch = 1
			org := "org-" + string(rune('a'+index))
			state.Organizations[org] = tenantsecurity.Organization{ID: org, State: tenantsecurity.StateActive, Version: 1, SecurityRevision: 1, PolicyDigest: strings.Repeat("a", 64)}
			_, commitErr := store.Commit(context.Background(), tenantsecurity.StoreCommand{OrganizationID: org, OperationID: "operation", CommandDigest: strings.Repeat(string(rune('b'+index)), 64), ExpectedEpoch: 0, Snapshot: state})
			results <- commitErr
		}(index, store)
	}
	wait.Wait()
	close(results)
	successes, stale := 0, 0
	for err := range results {
		if err == nil {
			successes++
		} else if tenantsecurity.IsCategory(err, tenantsecurity.CategoryStale) {
			stale++
		} else {
			t.Fatal(err)
		}
	}
	if successes != 1 || stale != 1 {
		t.Fatalf("success=%d stale=%d", successes, stale)
	}
}
