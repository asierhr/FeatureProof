package tenantsecurity

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// secretFixture composes the fake durable and private crypto boundaries used by adversarial tests.
func secretFixture(t *testing.T) (*Service, *SecretService, *testDurable, *fakeClock, PrincipalContext, TrustedResourceIdentity) {
	t.Helper()
	service, memory, clock, principal, identity := fixture(t)
	durable := &testDurable{snapshot: memory.Snapshot()}
	secrets, err := NewSecretService(durable, fakeKeys{}, fakeRandom{}, clock, DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	return service, secrets, durable, clock, principal, identity
}

// TestSecretRotationCASAndCiphertextOnlyPersistence covers concurrent versioning and canary absence.
func TestSecretRotationCASAndCiphertextOnlyPersistence(t *testing.T) {
	service, secrets, durable, _, principal, identity := secretFixture(t)
	capability, err := service.Authorize(context.Background(), principal, ActionTenantAdmin, ResourceSecret, identity)
	if err != nil {
		t.Fatal(err)
	}
	canary := []byte("SECRET-CANARY-NEVER-PERSIST")
	created, err := secrets.Create(context.Background(), capability, "secret-create", "deploy-token", "deploy", "key-a", canary)
	if err != nil || created.Version.Version != 1 {
		t.Fatalf("create=%#v err=%v", created, err)
	}
	document, err := json.Marshal(durable.snapshot)
	if err != nil || bytes.Contains(document, canary) {
		t.Fatalf("plaintext reached durable document: %v", err)
	}
	var successes atomic.Int32
	var wait sync.WaitGroup
	for _, operationID := range []string{"rotate-one", "rotate-two"} {
		wait.Add(1)
		go func(operationID string) {
			defer wait.Done()
			if _, rotateErr := secrets.Rotate(context.Background(), capability, operationID, "deploy-token", "deploy", "key-b", []byte("replacement"), 1); rotateErr == nil {
				successes.Add(1)
			} else if !IsCategory(rotateErr, CategoryStale) {
				t.Errorf("rotation category=%v", rotateErr)
			}
		}(operationID)
	}
	wait.Wait()
	if successes.Load() != 1 {
		t.Fatalf("concurrent rotation successes=%d", successes.Load())
	}
	snapshot, err := durable.Snapshot(context.Background())
	if err != nil || len(snapshot.Secrets[identity.OrganizationID+"/"+identity.ProjectID+"/deploy-token"]) != 2 {
		t.Fatalf("versions=%d err=%v", len(snapshot.Secrets[identity.OrganizationID+"/"+identity.ProjectID+"/deploy-token"]), err)
	}
}

// TestSecretLeaseConsumesAndAuditsBeforePrivateCallback proves single use and causal delivery order.
func TestSecretLeaseConsumesAndAuditsBeforePrivateCallback(t *testing.T) {
	service, secrets, durable, _, principal, identity := secretFixture(t)
	admin, _ := service.Authorize(context.Background(), principal, ActionTenantAdmin, ResourceSecret, identity)
	resolve, _ := service.Authorize(context.Background(), principal, ActionSecretResolve, ResourceSecret, identity)
	if _, err := secrets.Create(context.Background(), admin, "create-lease-secret", "lease-secret", "deploy", "key-a", []byte("private-value")); err != nil {
		t.Fatal(err)
	}
	issued, err := secrets.IssueLease(context.Background(), resolve, "issue-lease", "lease-one", "lease-secret", "run-one", "execute", "environment", "deploy", 7, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	callbackCount := 0
	err = secrets.WithSecret(context.Background(), resolve, "consume-lease", issued.Lease, secretCaller(issued.Lease), func(value []byte) error {
		callbackCount++
		if string(value) != "private-value" {
			t.Fatalf("private callback value mismatch")
		}
		snapshot, loadErr := durable.Snapshot(context.Background())
		stored := snapshot.SecretLeases[identity.OrganizationID+"/"+identity.ProjectID+"/lease-one"]
		if loadErr != nil || stored.ConsumedAt.IsZero() || auditByOperation(snapshot, identity.OrganizationID, "consume-lease").Sequence == 0 {
			t.Fatalf("callback preceded consume/audit: lease=%#v err=%v", stored, loadErr)
		}
		return nil
	})
	if err != nil || callbackCount != 1 {
		t.Fatalf("consume count=%d err=%v", callbackCount, err)
	}
	if err = secrets.WithSecret(context.Background(), resolve, "consume-replay", issued.Lease, secretCaller(issued.Lease), func([]byte) error { callbackCount++; return nil }); !IsCategory(err, CategoryRevoked) || callbackCount != 1 {
		t.Fatalf("single-use replay count=%d err=%v", callbackCount, err)
	}
}

// TestSecretTamperExpiryRevocationAndCancellationFailClosed covers exact envelope and lease constraints.
func TestSecretTamperExpiryRevocationAndCancellationFailClosed(t *testing.T) {
	service, secrets, durable, clock, principal, identity := secretFixture(t)
	admin, _ := service.Authorize(context.Background(), principal, ActionTenantAdmin, ResourceSecret, identity)
	resolve, _ := service.Authorize(context.Background(), principal, ActionSecretResolve, ResourceSecret, identity)
	if _, err := secrets.Create(context.Background(), admin, "create-tamper", "tamper-secret", "deploy", "key-a", []byte("canary")); err != nil {
		t.Fatal(err)
	}
	issued, err := secrets.IssueLease(context.Background(), resolve, "issue-expiry", "expiry-lease", "tamper-secret", "run-one", "verify", "verifier", "deploy", 1, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	for name, caller := range map[string]SecretUseIdentity{
		"run":      {RunID: "run-other", Phase: issued.Lease.Phase, Consumer: issued.Lease.Consumer, Purpose: issued.Lease.Purpose, Fence: issued.Lease.Fence},
		"phase":    {RunID: issued.Lease.RunID, Phase: "execute", Consumer: issued.Lease.Consumer, Purpose: issued.Lease.Purpose, Fence: issued.Lease.Fence},
		"consumer": {RunID: issued.Lease.RunID, Phase: issued.Lease.Phase, Consumer: "attacker", Purpose: issued.Lease.Purpose, Fence: issued.Lease.Fence},
		"fence":    {RunID: issued.Lease.RunID, Phase: issued.Lease.Phase, Consumer: issued.Lease.Consumer, Purpose: issued.Lease.Purpose, Fence: issued.Lease.Fence + 1},
	} {
		t.Run("original lease wrong "+name, func(t *testing.T) {
			callbacks := 0
			consumeErr := secrets.WithSecret(context.Background(), resolve, "caller-"+name, issued.Lease, caller, func([]byte) error { callbacks++; return nil })
			if !IsCategory(consumeErr, CategoryRevoked) || callbacks != 0 {
				t.Fatalf("caller mismatch callbacks=%d err=%v", callbacks, consumeErr)
			}
		})
	}
	forged := issued.Lease
	forged.Consumer = "attacker"
	if err = secrets.WithSecret(context.Background(), resolve, "consume-forged", forged, secretCaller(forged), func([]byte) error { return nil }); !IsCategory(err, CategoryRevoked) {
		t.Fatalf("forged lease=%v", err)
	}
	clock.now = clock.now.Add(2 * time.Minute)
	if err = secrets.WithSecret(context.Background(), resolve, "consume-expired", issued.Lease, secretCaller(issued.Lease), func([]byte) error { return nil }); !IsCategory(err, CategoryRevoked) {
		t.Fatalf("expired lease=%v", err)
	}
	clock.now = clock.now.Add(-2 * time.Minute)
	if _, err = secrets.Revoke(context.Background(), admin, "revoke-tamper", "tamper-secret", 1); err != nil {
		t.Fatal(err)
	}
	snapshot, _ := durable.Snapshot(context.Background())
	if !snapshot.SecretLeases[identity.OrganizationID+"/"+identity.ProjectID+"/expiry-lease"].Revoked {
		t.Fatal("revocation left unused lease active")
	}
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err = secrets.IssueLease(cancelled, resolve, "issue-cancelled", "cancelled-lease", "tamper-secret", "run-one", "verify", "verifier", "deploy", 1, time.Minute); !IsCategory(err, CategoryCancelled) {
		t.Fatalf("cancel=%v", err)
	}
}

// TestAuditPaginationTamperAndCursorIsolation verifies heads, ordering and opaque continuation state.
func TestAuditPaginationTamperAndCursorIsolation(t *testing.T) {
	service, secrets, durable, clock, principal, identity := secretFixture(t)
	admin, _ := service.Authorize(context.Background(), principal, ActionTenantAdmin, ResourceSecret, identity)
	for index := 0; index < 3; index++ {
		id := string(rune('a' + index))
		if _, err := secrets.Create(context.Background(), admin, "audit-op-"+id, "secret-"+id, "deploy", "key-a", []byte("value-"+id)); err != nil {
			t.Fatal(err)
		}
	}
	read, _ := service.Authorize(context.Background(), principal, ActionAuditRead, ResourceAudit, identity)
	audits, err := NewAuditService(durable, clock, 2, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	page, err := audits.Page(context.Background(), read, "", 2)
	if err != nil || len(page.Events) != 2 || page.NextCursor == "" {
		t.Fatalf("page=%#v err=%v", page, err)
	}
	if _, err = audits.Page(context.Background(), read, page.NextCursor+"x", 2); !IsCategory(err, CategoryInvalid) {
		t.Fatalf("cursor tamper=%v", err)
	}
	if err = audits.Verify(context.Background(), identity.OrganizationID); err != nil {
		t.Fatal(err)
	}
	durable.mu.Lock()
	events := durable.snapshot.Audits[identity.OrganizationID]
	durable.snapshot.Audits[identity.OrganizationID] = events[:len(events)-1]
	durable.mu.Unlock()
	if err = audits.Verify(context.Background(), identity.OrganizationID); !IsCategory(err, CategoryIntegrity) {
		t.Fatalf("tail deletion=%v", err)
	}
}

// TestSensitiveUnknownCommitAndDenyNeverGrants covers receipt recovery and failed denial audit.
func TestSensitiveUnknownCommitAndDenyNeverGrants(t *testing.T) {
	service, _, durable, clock, principal, identity := secretFixture(t)
	capability, _ := service.Authorize(context.Background(), principal, ActionTenantAdmin, ResourceSecret, identity)
	executor, _ := NewSensitiveExecutor(durable, clock)
	durable.afterCommit = errors.New("unknown commit")
	commandDigest := SensitiveCommandDigest("unknown", 0, capability.Digest)
	result, err := executor.Apply(context.Background(), SensitiveAction{Capability: capability, OperationID: "unknown-op", CommandDigest: commandDigest, Outcome: "committed", ResourceID: "secret"}, func(snapshot *Snapshot) error { return nil })
	if err != nil || result.Receipt.OperationID != "unknown-op" {
		t.Fatalf("unknown commit=%#v err=%v", result, err)
	}
	durable.afterCommit = errors.New("audit unavailable")
	if err = executor.RecordDenied(context.Background(), capability, "deny-op", "hidden", CategoryUnauthorized); !IsCategory(err, CategoryUnauthorized) {
		t.Fatalf("deny granted on audit failure=%v", err)
	}
}

// TestTransportSecurityFailsClosedAndPinsReconnect validates production and isolated-test posture.
func TestTransportSecurityFailsClosedAndPinsReconnect(t *testing.T) {
	if _, err := NewTransportGuard(TransportSecurity{ServerName: "db.internal"}); !IsCategory(err, CategoryInvalid) {
		t.Fatalf("insecure production=%v", err)
	}
	guard, err := NewTransportGuard(TransportSecurity{TLS: true, PeerAuthenticated: true, ServerName: "db.internal"})
	if err != nil {
		t.Fatal(err)
	}
	if err = guard.ValidateReconnect(TransportSecurity{TLS: true, PeerAuthenticated: false, ServerName: "db.internal"}); !IsCategory(err, CategoryInvalid) {
		t.Fatalf("peer downgrade=%v", err)
	}
	if ValidateTransport(TransportSecurity{ServerName: "db.internal", IsolatedLoopbackTest: true}) == nil {
		t.Fatal("remote insecure endpoint accepted as loopback test")
	}
	if err = ValidateTransport(TransportSecurity{ServerName: "127.0.0.1:5432", IsolatedLoopbackTest: true}); err != nil {
		t.Fatalf("isolated loopback=%v", err)
	}
	if strings.Contains(errString(err), "db.internal") {
		t.Fatal("transport error reflected endpoint")
	}
}

// errString safely renders an optional error for redaction assertions.
func errString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}
