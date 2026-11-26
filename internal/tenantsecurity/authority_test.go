package tenantsecurity

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"
)

// authorityStore exposes mutable durable heads to exercise process-live authorization decisions.
type authorityStore struct {
	mu        sync.RWMutex
	snapshot  Snapshot
	err       error
	commitErr error
}

// Snapshot returns the current durable head without caching it in the authority.
func (store *authorityStore) Snapshot(context.Context) (Snapshot, error) {
	store.mu.RLock()
	defer store.mu.RUnlock()
	return store.snapshot, store.err
}

// Commit atomically publishes authorization audit state for the live authority fixture.
func (store *authorityStore) Commit(_ context.Context, command StoreCommand) (StoreReceipt, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	if store.commitErr != nil {
		return StoreReceipt{}, store.commitErr
	}
	if command.ExpectedEpoch != store.snapshot.Epoch {
		return StoreReceipt{}, failure(CategoryStale, "authority_test_commit", nil)
	}
	digest, err := SnapshotDigest(command.Snapshot)
	if err != nil {
		return StoreReceipt{}, err
	}
	receipt, err := SealStoreReceipt(StoreReceipt{OrganizationID: command.OrganizationID, OperationID: command.OperationID, CommandDigest: command.CommandDigest, Epoch: command.Snapshot.Epoch, SnapshotDigest: digest})
	if err != nil {
		return StoreReceipt{}, err
	}
	command.Snapshot.Receipts[receipt.OrganizationID+"/"+receipt.OperationID] = receipt
	store.snapshot = command.Snapshot
	return receipt, nil
}

// Receipt resolves authorization audit replay without repeating an event.
func (store *authorityStore) Receipt(_ context.Context, organizationID, operationID string) (StoreReceipt, error) {
	store.mu.RLock()
	defer store.mu.RUnlock()
	receipt, ok := store.snapshot.Receipts[organizationID+"/"+operationID]
	if !ok {
		return StoreReceipt{}, failure(CategoryNotFoundHidden, "authority_test_receipt", nil)
	}
	return receipt, nil
}

// panickingAuthorityObserver proves telemetry cannot change an authorization result.
type panickingAuthorityObserver struct{}

// Observe simulates an untrusted metrics implementation panic.
func (panickingAuthorityObserver) Observe(context.Context, Fact) { panic("observer canary") }

// TestDurableAuthorityReloadsMembershipAndSecurityHead verifies all product decisions use the latest durable head.
func TestDurableAuthorityReloadsMembershipAndSecurityHead(t *testing.T) {
	clock := &fakeClock{now: time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)}
	memory := NewMemoryStore()
	service, err := NewService(memory, clock, DefaultLimits(), nil)
	if err != nil {
		t.Fatal(err)
	}
	policyDigest := digestStrings("policy")
	role := Role{OrganizationID: "org-live", ID: "operator", Actions: []Action{ActionRunAdmit}, Resources: []ResourceClass{ResourceRun}, ProjectIDs: []string{"project-live"}, Version: 1}
	if err = service.Bootstrap(context.Background(), Organization{ID: "org-live", State: StateActive, Version: 1, SecurityRevision: 1, PolicyDigest: policyDigest}, ProjectRegistration{OrganizationID: "org-live", ProjectID: "project-live", RepositoryID: "repo-live", State: StateActive, Version: 1}, Principal{ID: "principal-live", Kind: PrincipalHuman, SubjectDigest: digestStrings("cli", "subject-live"), State: StateActive, Version: 1}, Membership{OrganizationID: "org-live", PrincipalID: "principal-live", RoleIDs: []string{"operator"}, ProjectIDs: []string{"project-live"}, State: StateActive, ValidFrom: clock.now.Add(-time.Hour), Version: 1}, []Role{role}); err != nil {
		t.Fatal(err)
	}
	store := &authorityStore{snapshot: memory.Snapshot()}
	authority, err := NewDurableAuthority(store, clock, DefaultLimits(), panickingAuthorityObserver{})
	if err != nil {
		t.Fatal(err)
	}
	identity := TrustedResourceIdentity{OrganizationID: "org-live", ProjectID: "project-live", RepositoryID: "repo-live"}
	capability, err := authority.ResolveAndAuthorize(context.Background(), AuthenticatedSubject{Issuer: "cli", Subject: "subject-live"}, identity, ActionRunAdmit, ResourceRun)
	if err != nil {
		t.Fatal(err)
	}
	store.mu.Lock()
	membership := store.snapshot.Memberships[membershipKey("org-live", "principal-live")]
	membership.State, membership.Version = StateSuspended, 2
	store.snapshot.Memberships[membershipKey("org-live", "principal-live")] = membership
	organization := store.snapshot.Organizations["org-live"]
	organization.SecurityRevision++
	store.snapshot.Organizations["org-live"] = organization
	store.mu.Unlock()
	if _, err = authority.ResolveAndAuthorize(context.Background(), AuthenticatedSubject{Issuer: "cli", Subject: "subject-live"}, identity, ActionRunAdmit, ResourceRun); !IsCategory(err, CategoryUnauthorized) {
		t.Fatalf("expected live membership denial, got %v", err)
	}
	if err = ValidateCapabilitySnapshot(store.snapshot, capability, ActionRunAdmit, ResourceRun, clock.now); !IsCategory(err, CategoryUnauthorized) {
		t.Fatalf("expected old capability revoked by security head, got %v", err)
	}
}

// TestDurableAuthorityHidesCrossTenantExistence verifies known IDs never become an existence oracle.
func TestDurableAuthorityHidesCrossTenantExistence(t *testing.T) {
	store := &authorityStore{snapshot: emptySnapshot()}
	authority, err := NewDurableAuthority(store, RealClock{}, DefaultLimits(), nil)
	if err != nil {
		t.Fatal(err)
	}
	_, err = authority.ResolveAndAuthorize(context.Background(), AuthenticatedSubject{Issuer: "cli", Subject: "known-subject"}, TrustedResourceIdentity{OrganizationID: "other-org", ProjectID: "known-project", RepositoryID: "known-repository"}, ActionAuditRead, ResourceAudit)
	if !IsCategory(err, CategoryUnauthenticated) && !IsCategory(err, CategoryUnauthorized) {
		t.Fatalf("expected hidden denial, got %v", err)
	}
}

// TestDurableAuthorityCommitsAllowAndDenyAuditAndSurfacesJournalFailure proves telemetry never substitutes for the authoritative decision journal.
func TestDurableAuthorityCommitsAllowAndDenyAuditAndSurfacesJournalFailure(t *testing.T) {
	clock := &fakeClock{now: time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)}
	memory := NewMemoryStore()
	service, _ := NewService(memory, clock, DefaultLimits(), nil)
	policy := digestStrings("audit-policy")
	role := Role{OrganizationID: "org-audit", ID: "reader", Actions: []Action{ActionRunRead}, Resources: []ResourceClass{ResourceRun}, ProjectIDs: []string{"project-audit"}, Version: 1}
	if err := service.Bootstrap(context.Background(), Organization{ID: "org-audit", State: StateActive, Version: 1, SecurityRevision: 1, PolicyDigest: policy}, ProjectRegistration{OrganizationID: "org-audit", ProjectID: "project-audit", RepositoryID: "repo-audit", State: StateActive, Version: 1}, Principal{ID: "principal-audit", Kind: PrincipalHuman, SubjectDigest: digestStrings("oidc", "subject-audit"), State: StateActive, Version: 1}, Membership{OrganizationID: "org-audit", PrincipalID: "principal-audit", RoleIDs: []string{"reader"}, ProjectIDs: []string{"project-audit"}, State: StateActive, ValidFrom: clock.now.Add(-time.Hour), Version: 1}, []Role{role}); err != nil {
		t.Fatal(err)
	}
	store := &authorityStore{snapshot: memory.Snapshot()}
	authority, _ := NewDurableAuthority(store, clock, DefaultLimits(), nil)
	subject := AuthenticatedSubject{Issuer: "oidc", Subject: "subject-audit"}
	identity := TrustedResourceIdentity{OrganizationID: "org-audit", ProjectID: "project-audit", RepositoryID: "repo-audit"}
	if _, err := authority.ResolveAndAuthorize(context.Background(), subject, identity, ActionRunRead, ResourceRun); err != nil {
		t.Fatal(err)
	}
	if _, err := authority.ResolveAndAuthorize(context.Background(), subject, identity, ActionRunCancel, ResourceRun); !IsCategory(err, CategoryUnauthorized) {
		t.Fatalf("denial=%v", err)
	}
	unknown := AuthenticatedSubject{Issuer: "oidc", Subject: "secret-unknown-subject"}
	if _, err := authority.ResolveAndAuthorize(context.Background(), unknown, identity, ActionRunRead, ResourceRun); !IsCategory(err, CategoryUnauthenticated) {
		t.Fatalf("unknown subject denial=%v", err)
	}
	store.mu.RLock()
	events := append([]AuditEvent(nil), store.snapshot.Audits[identity.OrganizationID]...)
	store.mu.RUnlock()
	if len(events) != 3 || events[0].Outcome != "allowed" || events[1].Outcome != "denied" || events[1].Reason != CategoryUnauthorized || events[2].Reason != CategoryUnauthenticated {
		t.Fatalf("authorization audit=%+v", events)
	}
	raw, _ := json.Marshal(events[2])
	if strings.Contains(string(raw), unknown.Subject) || strings.Contains(string(raw), unknown.Issuer) {
		t.Fatalf("unauthenticated audit leaked subject: %s", raw)
	}
	store.mu.Lock()
	store.commitErr = errors.New("journal offline")
	store.mu.Unlock()
	if _, err := authority.ResolveAndAuthorize(context.Background(), subject, identity, ActionTenantAdmin, ResourceTenant); !IsCategory(err, CategoryUnavailable) {
		t.Fatalf("audit failure category=%v", err)
	}
}

// TestSurfaceGatewayRequiresAndConvergesEveryProductSurface proves no adapter
// can be omitted or provide a request-selected principal/resource override.
func TestSurfaceGatewayRequiresAndConvergesEveryProductSurface(t *testing.T) {
	clock := &fakeClock{now: time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)}
	memory := NewMemoryStore()
	service, _ := NewService(memory, clock, DefaultLimits(), nil)
	role := Role{OrganizationID: "org-product", ID: "operator", Actions: []Action{ActionRunRead}, Resources: []ResourceClass{ResourceRun}, ProjectIDs: []string{"project-product"}, Version: 1}
	err := service.Bootstrap(context.Background(), Organization{ID: "org-product", State: StateActive, Version: 1, SecurityRevision: 1, PolicyDigest: digestStrings("policy")}, ProjectRegistration{OrganizationID: "org-product", ProjectID: "project-product", RepositoryID: "repo-product", State: StateActive, Version: 1}, Principal{ID: "principal-product", Kind: PrincipalHuman, SubjectDigest: digestStrings("trusted", "subject"), State: StateActive, Version: 1}, Membership{OrganizationID: "org-product", PrincipalID: "principal-product", RoleIDs: []string{"operator"}, ProjectIDs: []string{"project-product"}, State: StateActive, ValidFrom: clock.now.Add(-time.Hour), Version: 1}, []Role{role})
	if err != nil {
		t.Fatal(err)
	}
	authority, _ := NewDurableAuthority(&authorityStore{snapshot: memory.Snapshot()}, clock, DefaultLimits(), nil)
	resource := TrustedResourceIdentity{OrganizationID: "org-product", ProjectID: "project-product", RepositoryID: "repo-product"}
	bindings := map[Surface]SurfaceBinding{}
	for _, surface := range productSurfaces() {
		bindings[surface] = SurfaceBinding{Subject: AuthenticatedSubject{Issuer: "trusted", Subject: "subject"}, Resource: resource}
	}
	delete(bindings, SurfaceWorker)
	if _, err = NewSurfaceGateway(authority, bindings); !IsCategory(err, CategoryInvalid) {
		t.Fatalf("expected missing worker binding rejected, got %v", err)
	}
	bindings[SurfaceWorker] = SurfaceBinding{Subject: AuthenticatedSubject{Issuer: "trusted", Subject: "subject"}, Resource: resource}
	gateway, err := NewSurfaceGateway(authority, bindings)
	if err != nil {
		t.Fatal(err)
	}
	for _, surface := range productSurfaces() {
		capability, authorizeErr := gateway.Authorize(context.Background(), surface, ActionRunRead, ResourceRun)
		if authorizeErr != nil || capability.Scope.PrincipalID != "principal-product" || capability.Scope.ProjectID != "project-product" {
			t.Fatalf("surface %s diverged: capability=%+v err=%v", surface, capability, authorizeErr)
		}
	}
	if _, err = gateway.Authorize(context.Background(), Surface("payload-override"), ActionRunRead, ResourceRun); !IsCategory(err, CategoryUnauthenticated) {
		t.Fatalf("expected unknown surface fail closed, got %v", err)
	}
}

// TestRemoteSurfaceProvidersRequirePerRequestAuthenticatedSubjects proves all
// remote adapters reject missing, cross-surface, wrong-issuer and spoofed
// subjects instead of inheriting a process-wide configured principal.
func TestRemoteSurfaceProvidersRequirePerRequestAuthenticatedSubjects(t *testing.T) {
	clock := &fakeClock{now: time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)}
	memory := NewMemoryStore()
	service, _ := NewService(memory, clock, DefaultLimits(), nil)
	resource := TrustedResourceIdentity{OrganizationID: "org-request", ProjectID: "project-request", RepositoryID: "repo-request"}
	role := Role{OrganizationID: resource.OrganizationID, ID: "reader", Actions: []Action{ActionRunRead}, Resources: []ResourceClass{ResourceRun}, ProjectIDs: []string{resource.ProjectID}, Version: 1}
	err := service.Bootstrap(context.Background(), Organization{ID: resource.OrganizationID, State: StateActive, Version: 1, SecurityRevision: 1, PolicyDigest: digestStrings("request-policy")}, ProjectRegistration{OrganizationID: resource.OrganizationID, ProjectID: resource.ProjectID, RepositoryID: resource.RepositoryID, State: StateActive, Version: 1}, Principal{ID: "principal-request", Kind: PrincipalHuman, SubjectDigest: digestStrings("adapter", "subject-a"), State: StateActive, Version: 1}, Membership{OrganizationID: resource.OrganizationID, PrincipalID: "principal-request", RoleIDs: []string{"reader"}, ProjectIDs: []string{resource.ProjectID}, State: StateActive, ValidFrom: clock.now.Add(-time.Hour), Version: 1}, []Role{role})
	if err != nil {
		t.Fatal(err)
	}
	authority, _ := NewDurableAuthority(&authorityStore{snapshot: memory.Snapshot()}, clock, DefaultLimits(), nil)
	providers := map[Surface]SurfaceBindingProvider{SurfaceCLI: StaticSurfaceBindingProvider{Value: SurfaceBinding{Subject: AuthenticatedSubject{Issuer: "adapter", Subject: "subject-a"}, Resource: resource}}}
	for _, surface := range []Surface{SurfaceAPI, SurfaceMCP, SurfaceGitHub, SurfaceExternalAgent, SurfaceWorker} {
		providers[surface] = RequestSurfaceBindingProvider{Surface: surface, Issuer: "adapter", Resource: resource}
	}
	gateway, err := NewProviderSurfaceGateway(authority, providers)
	if err != nil {
		t.Fatal(err)
	}
	nilProvider := make(map[Surface]SurfaceBindingProvider, len(providers))
	for surface, provider := range providers {
		nilProvider[surface] = provider
	}
	nilProvider[SurfaceWorker] = nil
	if _, gatewayErr := NewProviderSurfaceGateway(authority, nilProvider); !IsCategory(gatewayErr, CategoryInvalid) {
		t.Fatalf("nil worker provider=%v", gatewayErr)
	}
	if _, gatewayErr := (*SurfaceGateway)(nil).Authorize(context.Background(), SurfaceAPI, ActionRunRead, ResourceRun); !IsCategory(gatewayErr, CategoryInvalid) {
		t.Fatalf("nil gateway=%v", gatewayErr)
	}
	if _, digestErr := DigestSubject(AuthenticatedSubject{}); !IsCategory(digestErr, CategoryInvalid) {
		t.Fatalf("empty subject digest=%v", digestErr)
	}
	if digest, digestErr := DigestSubject(AuthenticatedSubject{Issuer: "adapter", Subject: "subject-a"}); digestErr != nil || digest == "" {
		t.Fatalf("subject digest=%q err=%v", digest, digestErr)
	}
	for _, surface := range []Surface{SurfaceAPI, SurfaceMCP, SurfaceGitHub, SurfaceExternalAgent, SurfaceWorker} {
		if _, authorizeErr := gateway.Authorize(context.Background(), surface, ActionRunRead, ResourceRun); !IsCategory(authorizeErr, CategoryUnauthenticated) {
			t.Fatalf("%s accepted missing request subject: %v", surface, authorizeErr)
		}
		ctx, bindErr := WithAuthenticatedSurface(context.Background(), surface, AuthenticatedSubject{Issuer: "adapter", Subject: "subject-a"})
		if bindErr != nil {
			t.Fatal(bindErr)
		}
		if capability, authorizeErr := gateway.Authorize(ctx, surface, ActionRunRead, ResourceRun); authorizeErr != nil || capability.Scope.PrincipalID != "principal-request" {
			t.Fatalf("%s authenticated capability=%+v err=%v", surface, capability, authorizeErr)
		}
		wrongIssuer, _ := WithAuthenticatedSurface(context.Background(), surface, AuthenticatedSubject{Issuer: "other-adapter", Subject: "subject-a"})
		if _, authorizeErr := gateway.Authorize(wrongIssuer, surface, ActionRunRead, ResourceRun); !IsCategory(authorizeErr, CategoryUnauthenticated) {
			t.Fatalf("%s accepted wrong issuer: %v", surface, authorizeErr)
		}
		cancelled, cancel := context.WithCancel(ctx)
		cancel()
		if _, authorizeErr := gateway.Authorize(cancelled, surface, ActionRunRead, ResourceRun); !IsCategory(authorizeErr, CategoryUnauthenticated) {
			t.Fatalf("%s accepted cancelled identity: %v", surface, authorizeErr)
		}
		spoofed, _ := WithAuthenticatedSurface(context.Background(), surface, AuthenticatedSubject{Issuer: "adapter", Subject: "subject-b"})
		if _, authorizeErr := gateway.Authorize(spoofed, surface, ActionRunRead, ResourceRun); !IsCategory(authorizeErr, CategoryUnauthenticated) {
			t.Fatalf("%s inherited prior principal for spoofed subject: %v", surface, authorizeErr)
		}
		crossSurface, _ := WithAuthenticatedSurface(context.Background(), SurfaceAPI, AuthenticatedSubject{Issuer: "adapter", Subject: "subject-a"})
		if surface != SurfaceAPI {
			if _, authorizeErr := gateway.Authorize(crossSurface, surface, ActionRunRead, ResourceRun); !IsCategory(authorizeErr, CategoryUnauthenticated) {
				t.Fatalf("%s accepted API-bound identity: %v", surface, authorizeErr)
			}
		}
	}
}

// TestDurableAuthorityFailsClosedBeforeDecisionAudit proves malformed trusted
// coordinates and an unavailable durable head issue no capability even when
// optional telemetry is present.
func TestDurableAuthorityFailsClosedBeforeDecisionAudit(t *testing.T) {
	store := &authorityStore{snapshot: emptySnapshot()}
	authority, err := NewDurableAuthority(store, RealClock{}, DefaultLimits(), panickingAuthorityObserver{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = authority.ResolveAndAuthorize(context.Background(), AuthenticatedSubject{Issuer: "issuer", Subject: "subject"}, TrustedResourceIdentity{OrganizationID: "bad/id", ProjectID: "project", RepositoryID: "repository"}, ActionRunRead, ResourceRun); !IsCategory(err, CategoryUnauthenticated) {
		t.Fatalf("malformed identity=%v", err)
	}
	store.err = failure(CategoryUnavailable, "snapshot", nil)
	if _, err = authority.ResolveAndAuthorize(context.Background(), AuthenticatedSubject{Issuer: "issuer", Subject: "subject"}, TrustedResourceIdentity{OrganizationID: "organization", ProjectID: "project", RepositoryID: "repository"}, ActionRunRead, ResourceRun); !IsCategory(err, CategoryUnavailable) {
		t.Fatalf("snapshot failure=%v", err)
	}
}

// emptySnapshot returns the structurally complete zero security projection used by denial fixtures.
func emptySnapshot() Snapshot {
	return NewMemoryStore().Snapshot()
}
