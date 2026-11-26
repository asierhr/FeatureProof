package tenantsecurity

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"slices"
	"sync"
	"testing"
	"time"
)

// fakeClock fixes expiry and quota windows in tests.
type fakeClock struct{ now time.Time }

// Now returns the controlled instant.
func (c *fakeClock) Now() time.Time { return c.now }

// secretCaller derives the trusted caller proof used by tests that own the issued lease.
func secretCaller(lease SecretLease) SecretUseIdentity {
	return SecretUseIdentity{RunID: lease.RunID, Phase: lease.Phase, Consumer: lease.Consumer, Purpose: lease.Purpose, Fence: lease.Fence}
}

// fakeKeys exercises ciphertext boundaries without an external key service.
type fakeKeys struct{}

// Algorithm returns a stable fake provider algorithm identifier.
func (fakeKeys) Algorithm() string { return "fake-v1" }

// Encrypt applies a reversible test-only transform bound to AAD.
func (fakeKeys) Encrypt(_ context.Context, keyReference string, plaintext, aad, nonce []byte) (EncryptedPayload, error) {
	tag := sha256.Sum256(append(append(append([]byte{}, keyReference...), aad...), nonce...))
	out := make([]byte, sha256.Size+len(plaintext))
	copy(out, tag[:])
	for i := range plaintext {
		out[sha256.Size+i] = plaintext[i] ^ aad[i%len(aad)] ^ nonce[i%len(nonce)]
	}
	return EncryptedPayload{Algorithm: "fake-v1", Nonce: slices.Clone(nonce), Ciphertext: out}, nil
}

// Decrypt reverses the test transform under matching AAD.
func (fakeKeys) Decrypt(_ context.Context, keyReference string, payload EncryptedPayload, aad []byte) ([]byte, error) {
	if payload.Algorithm != "fake-v1" || len(payload.Ciphertext) < sha256.Size || len(payload.Nonce) == 0 {
		return nil, errors.New("fake authentication")
	}
	tag := sha256.Sum256(append(append(append([]byte{}, keyReference...), aad...), payload.Nonce...))
	if !bytes.Equal(tag[:], payload.Ciphertext[:sha256.Size]) {
		return nil, errors.New("fake authentication")
	}
	out := make([]byte, len(payload.Ciphertext)-sha256.Size)
	for i := range out {
		out[i] = payload.Ciphertext[sha256.Size+i] ^ aad[i%len(aad)] ^ payload.Nonce[i%len(payload.Nonce)]
	}
	return out, nil
}

// fakeRandom returns deterministic nonces so crypto tests are reproducible.
type fakeRandom struct{}

// Bytes returns a bounded deterministic byte sequence.
func (fakeRandom) Bytes(_ context.Context, size int) ([]byte, error) {
	return bytes.Repeat([]byte{0x5a}, size), nil
}

// testDurable provides atomic snapshot CAS for core package tests.
type testDurable struct {
	mu          sync.Mutex
	snapshot    Snapshot
	afterCommit error
}

// Snapshot returns an ownership-safe state copy.
func (store *testDurable) Snapshot(ctx context.Context) (Snapshot, error) {
	if err := ctx.Err(); err != nil {
		return Snapshot{}, classifyContext("test_snapshot", err)
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	if err := ValidateSnapshot(store.snapshot); err != nil {
		return Snapshot{}, err
	}
	return CloneSnapshot(store.snapshot)
}

// Receipt resolves one scoped idempotency result.
func (store *testDurable) Receipt(_ context.Context, organizationID, operationID string) (StoreReceipt, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	receipt, ok := store.snapshot.Receipts[organizationID+"/"+operationID]
	if !ok {
		return StoreReceipt{}, failure(CategoryNotFoundHidden, "test_receipt", nil)
	}
	return receipt, nil
}

// Commit applies whole-state CAS and seals the receipt into the committed snapshot.
func (store *testDurable) Commit(_ context.Context, command StoreCommand) (StoreReceipt, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	if command.ExpectedEpoch != store.snapshot.Epoch {
		return StoreReceipt{}, failure(CategoryStale, "test_commit", nil)
	}
	if err := ValidateStoreCommand(command); err != nil {
		return StoreReceipt{}, err
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
	if store.afterCommit != nil {
		return StoreReceipt{}, store.afterCommit
	}
	return receipt, nil
}

// fixture creates an isolated organization graph and resolved principal.
func fixture(t *testing.T) (*Service, *MemoryStore, *fakeClock, PrincipalContext, TrustedResourceIdentity) {
	t.Helper()
	clock := &fakeClock{now: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)}
	store := NewMemoryStore()
	service, err := NewService(store, clock, DefaultLimits(), nil)
	if err != nil {
		t.Fatal(err)
	}
	subjectDigest := digestStrings("issuer", "subject")
	org := Organization{ID: "org-a", State: StateActive, Version: 1, SecurityRevision: 1, PolicyDigest: digestStrings("policy")}
	project := ProjectRegistration{OrganizationID: org.ID, ProjectID: "project-a", RepositoryID: "repo-a", State: StateActive, Version: 1}
	principal := Principal{ID: "principal-a", Kind: PrincipalHuman, SubjectDigest: subjectDigest, State: StateActive, Version: 1}
	membership := Membership{OrganizationID: org.ID, PrincipalID: principal.ID, RoleIDs: []string{"operator"}, ProjectIDs: []string{project.ProjectID}, State: StateActive, ValidFrom: clock.now.Add(-time.Hour), ValidUntil: clock.now.Add(time.Hour), Version: 1}
	role := Role{OrganizationID: org.ID, ID: "operator", Actions: []Action{ActionRunAdmit, ActionRunRead, ActionEvidenceRead, ActionSecretResolve, ActionQuotaReserve, ActionRetentionExecute, ActionTenantAdmin, ActionAuditRead}, Resources: []ResourceClass{ResourceRun, ResourceSecret, ResourceQuota, ResourceEvidence, ResourceTenant, ResourceAudit}, ProjectIDs: []string{project.ProjectID}, Version: 1}
	if err := service.Bootstrap(context.Background(), org, project, principal, membership, []Role{role}); err != nil {
		t.Fatal(err)
	}
	identity := TrustedResourceIdentity{OrganizationID: org.ID, ProjectID: project.ProjectID, RepositoryID: project.RepositoryID}
	resolved, err := service.Resolve(context.Background(), AuthenticatedSubject{Issuer: "issuer", Subject: "subject"}, identity)
	if err != nil {
		t.Fatal(err)
	}
	return service, store, clock, resolved, identity
}

// TestRBACScopeAndRevocationFailClosed covers tenant isolation and revision invalidation.
func TestRBACScopeAndRevocationFailClosed(t *testing.T) {
	service, store, _, principal, identity := fixture(t)
	capability, err := service.Authorize(context.Background(), principal, ActionRunRead, ResourceRun, identity)
	if err != nil {
		t.Fatal(err)
	}
	if err := service.VerifyCapability(context.Background(), capability, ActionRunRead, ResourceRun); err != nil {
		t.Fatal(err)
	}
	wrong := identity
	wrong.OrganizationID = "org-b"
	if _, err := service.Authorize(context.Background(), principal, ActionRunRead, ResourceRun, wrong); !IsCategory(err, CategoryUnauthorized) {
		t.Fatalf("cross tenant=%v", err)
	}
	store.mu.Lock()
	org := store.Organizations[identity.OrganizationID]
	org.SecurityRevision++
	store.Organizations[identity.OrganizationID] = org
	store.mu.Unlock()
	if err := service.VerifyCapability(context.Background(), capability, ActionRunRead, ResourceRun); !IsCategory(err, CategoryRevoked) {
		t.Fatalf("revision=%v", err)
	}
}

// TestRoleCompilationDefensiveAndClosed rejects policy expansion and aliases.
func TestRoleCompilationDefensiveAndClosed(t *testing.T) {
	input := Role{OrganizationID: "org-a", ID: "role-a", Actions: []Action{ActionRunRead}, Resources: []ResourceClass{ResourceRun}, ProjectIDs: []string{"project-a"}, Version: 1}
	compiled, err := CompileRole(input, DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	input.Actions[0] = Action("invented")
	if compiled.Actions[0] != ActionRunRead {
		t.Fatal("role aliased input")
	}
	input.Actions = []Action{Action("wildcard")}
	if _, err := CompileRole(input, DefaultLimits()); !IsCategory(err, CategoryInvalid) {
		t.Fatalf("unknown=%v", err)
	}
}

// TestAuditChainDetectsTamper proves audit state cannot be silently rewritten.
func TestAuditChainDetectsTamper(t *testing.T) {
	service, store, _, principal, identity := fixture(t)
	capability, err := service.Authorize(context.Background(), principal, ActionRunRead, ResourceRun, identity)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = service.AppendAudit(context.Background(), capability, "allowed", ""); err != nil {
		t.Fatal(err)
	}
	if _, err = service.AppendAudit(context.Background(), capability, "committed", ""); err != nil {
		t.Fatal(err)
	}
	if err = service.VerifyAudit(context.Background(), identity.OrganizationID); err != nil {
		t.Fatal(err)
	}
	store.mu.Lock()
	store.Audits[identity.OrganizationID][0].Outcome = "denied"
	store.mu.Unlock()
	if err = service.VerifyAudit(context.Background(), identity.OrganizationID); !IsCategory(err, CategoryIntegrity) {
		t.Fatalf("tamper=%v", err)
	}
}

// TestQuotaConcurrentLastSlotAndReleaseOnce proves pre-effect capacity cannot oversubscribe.
func TestQuotaConcurrentLastSlotAndReleaseOnce(t *testing.T) {
	service, _, _, principal, identity := fixture(t)
	capability, err := service.Authorize(context.Background(), principal, ActionQuotaReserve, ResourceQuota, identity)
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	success := 0
	var mu sync.Mutex
	for i := 0; i < 12; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, err := service.ReserveQuota(context.Background(), capability, "op-"+string(rune('a'+i)), "worker", 1, QuotaLimit{Limit: 1, Window: time.Hour})
			if err == nil {
				mu.Lock()
				success++
				mu.Unlock()
			} else if !IsCategory(err, CategoryQuotaExhausted) {
				t.Errorf("reserve=%v", err)
			}
		}(i)
	}
	wg.Wait()
	if success != 1 {
		t.Fatalf("success=%d", success)
	}
	for key, r := range service.store.Reservations {
		if !r.Released {
			op := key[len(key)-4:]
			if err := service.ReleaseQuota(context.Background(), capability, op); err != nil {
				t.Fatal(err)
			}
			if err := service.ReleaseQuota(context.Background(), capability, op); err != nil {
				t.Fatal(err)
			}
		}
	}
}

// TestSecretCiphertextLeaseSingleUseAndRevocation proves ciphertext-only persistence and JIT use.
func TestSecretCiphertextLeaseSingleUseAndRevocation(t *testing.T) {
	service, store, clock, principal, identity := fixture(t)
	admin, err := service.Authorize(context.Background(), principal, ActionTenantAdmin, ResourceSecret, identity)
	if err != nil {
		t.Fatal(err)
	}
	durable := &testDurable{snapshot: store.Snapshot()}
	secrets, err := NewSecretService(durable, fakeKeys{}, fakeRandom{}, clock, DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	canary := []byte("CANARY-PLAINTEXT")
	created, err := secrets.Create(context.Background(), admin, "create-a", "secret-a", "deploy", "key-a", canary)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(created.Version.Payload.Ciphertext, canary) {
		t.Fatal("plaintext persisted")
	}
	resolve, err := service.Authorize(context.Background(), principal, ActionSecretResolve, ResourceSecret, identity)
	if err != nil {
		t.Fatal(err)
	}
	lease, err := secrets.IssueLease(context.Background(), resolve, "issue-a", "lease-a", "secret-a", "run-a", "execute", "environment", "deploy", 1, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	var observed []byte
	if err := secrets.WithSecret(context.Background(), resolve, "consume-a", lease.Lease, secretCaller(lease.Lease), func(value []byte) error { observed = slices.Clone(value); return nil }); err != nil {
		t.Fatal(err)
	}
	if string(observed) != string(canary) {
		t.Fatalf("observed=%q", observed)
	}
	if err := secrets.WithSecret(context.Background(), resolve, "consume-b", lease.Lease, secretCaller(lease.Lease), func([]byte) error { return nil }); !IsCategory(err, CategoryRevoked) {
		t.Fatalf("replay=%v", err)
	}
	if _, err := secrets.Revoke(context.Background(), admin, "revoke-a", "secret-a", 1); err != nil {
		t.Fatal(err)
	}
	if _, err := secrets.IssueLease(context.Background(), resolve, "issue-b", "lease-b", "secret-a", "run-a", "execute", "environment", "deploy", 1, time.Minute); !IsCategory(err, CategoryRevoked) {
		t.Fatalf("revoked=%v", err)
	}
}

// TestCancellationAndHiddenNotFound preserves context and non-oracle errors.
func TestCancellationAndHiddenNotFound(t *testing.T) {
	service, _, _, principal, identity := fixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := service.Resolve(ctx, AuthenticatedSubject{Issuer: "issuer", Subject: "subject"}, identity); !IsCategory(err, CategoryCancelled) {
		t.Fatalf("cancel=%v", err)
	}
	capability, err := service.Authorize(context.Background(), principal, ActionQuotaReserve, ResourceQuota, identity)
	if err != nil {
		t.Fatal(err)
	}
	if err := service.ReleaseQuota(context.Background(), capability, "missing"); !IsCategory(err, CategoryNotFoundHidden) {
		t.Fatalf("missing=%v", err)
	}
}

// TestSubjectDigestDoesNotPersistRawSubject ensures mappings never store raw subjects.
func TestSubjectDigestDoesNotPersistRawSubject(t *testing.T) {
	_, store, _, _, _ := fixture(t)
	raw := []byte("subject")
	sum := sha256.Sum256(raw)
	_ = sum
	for key := range store.Subjects {
		if key == string(raw) {
			t.Fatal("raw subject persisted")
		}
	}
	if errors.Is(nil, context.Canceled) {
		t.Fatal("unreachable")
	}
}

// recordingDeleter captures only sealed scoped deletion keys.
type recordingDeleter struct{ keys []ScopedKey }

// Delete records one consumer-owned deletion invocation.
func (d *recordingDeleter) Delete(_ context.Context, key ScopedKey) error {
	d.keys = append(d.keys, key)
	return nil
}

// TestScopedCacheTransportAndConvergentDeletion covers isolation, secure transport and intent-first deletion.
func TestScopedCacheTransportAndConvergentDeletion(t *testing.T) {
	service, _, _, principal, identity := fixture(t)
	read, err := service.Authorize(context.Background(), principal, ActionRunRead, ResourceRun, identity)
	if err != nil {
		t.Fatal(err)
	}
	key, err := NewScopedKey(read, ResourceRun, "run-a")
	if err != nil {
		t.Fatal(err)
	}
	cache := NewScopedCache()
	if err = cache.Put(key, []byte("safe")); err != nil {
		t.Fatal(err)
	}
	other := key
	other.OrganizationID = "org-b"
	other.Digest = digestStrings("other")
	if _, ok := cache.Get(other); ok {
		t.Fatal("cross tenant cache hit")
	}
	if ValidateTransport(TransportSecurity{TLS: true, PeerAuthenticated: true, ServerName: "db.internal"}) != nil {
		t.Fatal("secure transport rejected")
	}
	if err = ValidateTransport(TransportSecurity{}); !IsCategory(err, CategoryInvalid) {
		t.Fatalf("insecure=%v", err)
	}
	retention, err := service.Authorize(context.Background(), principal, ActionRetentionExecute, ResourceEvidence, identity)
	if err != nil {
		t.Fatal(err)
	}
	intent, err := service.PlanDeletion(context.Background(), retention, "evidence-a", ResourceEvidence)
	if err != nil {
		t.Fatal(err)
	}
	deleter := &recordingDeleter{}
	if err = service.ExecuteDeletion(context.Background(), retention, intent, deleter); err != nil {
		t.Fatal(err)
	}
	if len(deleter.keys) != 1 {
		t.Fatalf("deletes=%d", len(deleter.keys))
	}
}
