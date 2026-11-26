package tenantsecurity

import (
	"context"
	"fmt"
	"slices"
	"time"
)

// secretNonceBytes bounds provider-independent nonce allocation before encryption.
const secretNonceBytes = 24

// KeyProvider performs envelope cryptography outside stores and owns all key material.
type KeyProvider interface {
	Algorithm() string
	Encrypt(context.Context, string, []byte, []byte, []byte) (EncryptedPayload, error)
	Decrypt(context.Context, string, EncryptedPayload, []byte) ([]byte, error)
}

// RandomSource supplies cryptographic nonces without coupling the core to a provider.
type RandomSource interface {
	Bytes(context.Context, int) ([]byte, error)
}

// EncryptedPayload is ciphertext-only provider output safe for durable storage.
type EncryptedPayload struct {
	Algorithm  string `json:"algorithm"`
	Nonce      []byte `json:"nonce"`
	Ciphertext []byte `json:"ciphertext"`
}

// SecretVersion is an immutable tenant-scoped envelope version or revocation tombstone.
type SecretVersion struct {
	OrganizationID string           `json:"organization_id"`
	ProjectID      string           `json:"project_id"`
	ReferenceID    string           `json:"reference_id"`
	Version        uint64           `json:"version"`
	Purpose        string           `json:"purpose"`
	KeyReference   string           `json:"key_reference,omitempty"`
	State          State            `json:"state"`
	Payload        EncryptedPayload `json:"payload,omitempty"`
	AADDigest      string           `json:"aad_digest,omitempty"`
	PreviousDigest string           `json:"previous_digest,omitempty"`
	CreatedAt      time.Time        `json:"created_at"`
	Digest         string           `json:"digest"`
}

// SecretLease is durable single-use authority bound to an exact workflow consumer and fence.
type SecretLease struct {
	ID          string      `json:"id"`
	Scope       TenantScope `json:"scope"`
	ReferenceID string      `json:"reference_id"`
	Version     uint64      `json:"version"`
	RunID       string      `json:"run_id"`
	Phase       string      `json:"phase"`
	Consumer    string      `json:"consumer"`
	Purpose     string      `json:"purpose"`
	Fence       uint64      `json:"fence"`
	IssuedAt    time.Time   `json:"issued_at"`
	ExpiresAt   time.Time   `json:"expires_at"`
	ConsumedAt  time.Time   `json:"consumed_at,omitempty"`
	Revoked     bool        `json:"revoked"`
	Digest      string      `json:"digest"`
}

// SecretMutationResult exposes ciphertext metadata plus its causal receipt and audit link.
type SecretMutationResult struct {
	Version SecretVersion
	Receipt StoreReceipt
	Audit   AuditEvent
}

// SecretLeaseResult exposes a JIT authority only after it and its audit are durable.
type SecretLeaseResult struct {
	Lease   SecretLease
	Receipt StoreReceipt
	Audit   AuditEvent
}

// SecretService encrypts outside transactions and delivers plaintext only to a private callback.
type SecretService struct {
	store     DurableStore
	keys      KeyProvider
	random    RandomSource
	clock     Clock
	limits    Limits
	sensitive *SensitiveExecutor
}

// NewSecretService validates consumer-owned persistence, crypto, randomness and clock ports.
func NewSecretService(store DurableStore, keys KeyProvider, random RandomSource, clock Clock, limits Limits) (*SecretService, error) {
	if store == nil || keys == nil || random == nil || clock == nil || ValidateLimits(limits) != nil || ValidateID(keys.Algorithm()) != nil {
		return nil, failure(CategoryInvalid, "secret_new", nil)
	}
	sensitive, err := NewSensitiveExecutor(store, clock)
	if err != nil {
		return nil, err
	}
	return &SecretService{store: store, keys: keys, random: random, clock: clock, limits: limits, sensitive: sensitive}, nil
}

// Create writes version one only when the reference is absent.
func (service *SecretService) Create(ctx context.Context, capability Capability, operationID, referenceID, purpose, keyReference string, plaintext []byte) (SecretMutationResult, error) {
	return service.rotate(ctx, capability, operationID, referenceID, purpose, keyReference, plaintext, 0)
}

// Rotate appends exactly one immutable version using optimistic secret-version CAS.
func (service *SecretService) Rotate(ctx context.Context, capability Capability, operationID, referenceID, purpose, keyReference string, plaintext []byte, expectedVersion uint64) (SecretMutationResult, error) {
	if expectedVersion == 0 {
		return SecretMutationResult{}, failure(CategoryInvalid, "secret_rotate", nil)
	}
	return service.rotate(ctx, capability, operationID, referenceID, purpose, keyReference, plaintext, expectedVersion)
}

// rotate encrypts before durable I/O and commits only ciphertext metadata through SensitiveExecutor.
func (service *SecretService) rotate(ctx context.Context, capability Capability, operationID, referenceID, purpose, keyReference string, plaintext []byte, expectedVersion uint64) (SecretMutationResult, error) {
	if capability.Action != ActionTenantAdmin && capability.Action != ActionSecretResolve || capability.Resource != ResourceSecret || ValidateID(operationID) != nil || ValidateID(referenceID) != nil || ValidateID(purpose) != nil || ValidateID(keyReference) != nil || len(plaintext) == 0 || len(plaintext) > service.limits.MaxPayloadBytes || expectedVersion >= MaxDurableCounter {
		return SecretMutationResult{}, failure(CategoryInvalid, "secret_rotate", nil)
	}
	version := expectedVersion + 1
	algorithm := service.keys.Algorithm()
	aad := canonicalSecretAAD(capability.Scope.OrganizationID, capability.Scope.ProjectID, referenceID, version, purpose, keyReference, algorithm)
	nonce, err := service.random.Bytes(ctx, secretNonceBytes)
	if err != nil || len(nonce) != secretNonceBytes {
		return SecretMutationResult{}, failure(CategoryUnavailable, "secret_random", nil)
	}
	input := slices.Clone(plaintext)
	defer zeroBytes(input)
	payload, err := service.keys.Encrypt(ctx, keyReference, input, aad, slices.Clone(nonce))
	zeroBytes(nonce)
	if err != nil {
		return SecretMutationResult{}, failure(CategoryUnavailable, "secret_encrypt", nil)
	}
	if payload.Algorithm != algorithm || len(payload.Nonce) == 0 || len(payload.Ciphertext) == 0 || len(payload.Ciphertext) > service.limits.MaxPayloadBytes {
		return SecretMutationResult{}, failure(CategoryIntegrity, "secret_encrypt", nil)
	}
	secret := SecretVersion{OrganizationID: capability.Scope.OrganizationID, ProjectID: capability.Scope.ProjectID, ReferenceID: referenceID, Version: version, Purpose: purpose, KeyReference: keyReference, State: StateActive, Payload: cloneEncryptedPayload(payload), AADDigest: digestStrings(string(aad)), CreatedAt: service.clock.Now().UTC()}
	commandDigest := SensitiveCommandDigest("secret.rotate", expectedVersion, capability.Scope.Digest, referenceID, purpose, keyReference, secret.AADDigest, payload.Algorithm, string(payload.Nonce), string(payload.Ciphertext))
	result, err := service.sensitive.Apply(ctx, SensitiveAction{Capability: capability, OperationID: operationID, CommandDigest: commandDigest, Outcome: "committed", ResourceID: referenceID}, func(snapshot *Snapshot) error {
		key := secretKey(capability.Scope, referenceID)
		versions := snapshot.Secrets[key]
		if uint64(len(versions)) != expectedVersion || expectedVersion > 0 && versions[len(versions)-1].State != StateActive {
			return failure(CategoryStale, "secret_rotate", nil)
		}
		if expectedVersion > 0 {
			secret.PreviousDigest = versions[len(versions)-1].Digest
		}
		secret.Digest = secretVersionDigest(secret)
		snapshot.Secrets[key] = append(versions, secret)
		return nil
	})
	if err != nil {
		return SecretMutationResult{}, err
	}
	return SecretMutationResult{Version: secret, Receipt: result.Receipt, Audit: result.Audit}, nil
}

// Revoke appends a tombstone and revokes every unused lease in the same transaction.
func (service *SecretService) Revoke(ctx context.Context, capability Capability, operationID, referenceID string, expectedVersion uint64) (SecretMutationResult, error) {
	if capability.Action != ActionTenantAdmin && capability.Action != ActionSecretResolve || capability.Resource != ResourceSecret || ValidateID(operationID) != nil || ValidateID(referenceID) != nil || expectedVersion == 0 || expectedVersion >= MaxDurableCounter {
		return SecretMutationResult{}, failure(CategoryInvalid, "secret_revoke", nil)
	}
	tombstone := SecretVersion{OrganizationID: capability.Scope.OrganizationID, ProjectID: capability.Scope.ProjectID, ReferenceID: referenceID, Version: expectedVersion + 1, State: StateRevoked, CreatedAt: service.clock.Now().UTC()}
	commandDigest := SensitiveCommandDigest("secret.revoke", expectedVersion, capability.Scope.Digest, referenceID)
	result, err := service.sensitive.Apply(ctx, SensitiveAction{Capability: capability, OperationID: operationID, CommandDigest: commandDigest, Outcome: "committed", ResourceID: referenceID}, func(snapshot *Snapshot) error {
		key := secretKey(capability.Scope, referenceID)
		versions := snapshot.Secrets[key]
		if uint64(len(versions)) != expectedVersion || len(versions) == 0 || versions[len(versions)-1].State != StateActive {
			return failure(CategoryStale, "secret_revoke", nil)
		}
		tombstone.Purpose = versions[len(versions)-1].Purpose
		tombstone.PreviousDigest = versions[len(versions)-1].Digest
		tombstone.Digest = secretVersionDigest(tombstone)
		snapshot.Secrets[key] = append(versions, tombstone)
		for leaseKey, lease := range snapshot.SecretLeases {
			if lease.Scope.OrganizationID == capability.Scope.OrganizationID && lease.Scope.ProjectID == capability.Scope.ProjectID && lease.ReferenceID == referenceID && lease.ConsumedAt.IsZero() {
				lease.Revoked = true
				lease.Digest = secretLeaseDigest(lease)
				snapshot.SecretLeases[leaseKey] = lease
			}
		}
		return nil
	})
	if err != nil {
		return SecretMutationResult{}, err
	}
	return SecretMutationResult{Version: tombstone, Receipt: result.Receipt, Audit: result.Audit}, nil
}

// IssueLease persists one exact single-use run/phase/consumer/purpose/fence authority.
func (service *SecretService) IssueLease(ctx context.Context, capability Capability, operationID, id, referenceID, runID, phase, consumer, purpose string, fence uint64, ttl time.Duration) (SecretLeaseResult, error) {
	if capability.Action != ActionSecretResolve || capability.Resource != ResourceSecret || ValidateID(operationID) != nil || ValidateID(id) != nil || ValidateID(referenceID) != nil || ValidateID(runID) != nil || !validPhase(phase) || ValidateID(consumer) != nil || ValidateID(purpose) != nil || fence == 0 || ttl <= 0 {
		return SecretLeaseResult{}, failure(CategoryInvalid, "lease_issue", nil)
	}
	issued := service.clock.Now().UTC()
	lease := SecretLease{ID: id, Scope: capability.Scope, ReferenceID: referenceID, RunID: runID, Phase: phase, Consumer: consumer, Purpose: purpose, Fence: fence, IssuedAt: issued, ExpiresAt: issued.Add(ttl)}
	commandDigest := SensitiveCommandDigest("secret.lease", 0, capability.Scope.Digest, id, referenceID, runID, phase, consumer, purpose, fmt.Sprint(fence), lease.ExpiresAt.Format(time.RFC3339Nano))
	result, err := service.sensitive.Apply(ctx, SensitiveAction{Capability: capability, OperationID: operationID, CommandDigest: commandDigest, Outcome: "committed", ResourceID: referenceID}, func(snapshot *Snapshot) error {
		if _, exists := snapshot.SecretLeases[secretLeaseKey(capability.Scope, id)]; exists {
			return failure(CategoryConflict, "lease_issue", nil)
		}
		versions := snapshot.Secrets[secretKey(capability.Scope, referenceID)]
		if len(versions) == 0 || versions[len(versions)-1].State != StateActive || versions[len(versions)-1].Purpose != purpose {
			return failure(CategoryRevoked, "lease_issue", nil)
		}
		lease.Version = versions[len(versions)-1].Version
		lease.Digest = secretLeaseDigest(lease)
		snapshot.SecretLeases[secretLeaseKey(capability.Scope, id)] = lease
		return nil
	})
	if err != nil {
		return SecretLeaseResult{}, err
	}
	return SecretLeaseResult{Lease: lease, Receipt: result.Receipt, Audit: result.Audit}, nil
}

// SecretUseIdentity is the composition-owned caller proof presented at the private JIT boundary.
type SecretUseIdentity struct {
	RunID, Phase, Consumer, Purpose string
	Fence                           uint64
}

// WithSecret decrypts outside storage only for the exact run/phase/consumer/fence, commits consume+audit, then invokes the private callback once.
func (service *SecretService) WithSecret(ctx context.Context, capability Capability, consumeOperationID string, lease SecretLease, caller SecretUseIdentity, callback func([]byte) error) error {
	if callback == nil || ValidateID(consumeOperationID) != nil || ValidateID(caller.RunID) != nil || !validPhase(caller.Phase) || ValidateID(caller.Consumer) != nil || ValidateID(caller.Purpose) != nil || caller.Fence == 0 {
		return failure(CategoryInvalid, "secret_consume", nil)
	}
	snapshot, err := service.store.Snapshot(ctx)
	if err != nil {
		return err
	}
	if err = verifyDurableCapability(snapshot, capability, ActionSecretResolve, ResourceSecret, service.clock.Now()); err != nil {
		return err
	}
	stored, ok := snapshot.SecretLeases[secretLeaseKey(capability.Scope, lease.ID)]
	if !ok || stored != lease || stored.Digest != lease.Digest || stored.Revoked || !stored.ConsumedAt.IsZero() || !service.clock.Now().Before(stored.ExpiresAt) || stored.Scope.Digest != capability.Scope.Digest || stored.RunID != caller.RunID || stored.Phase != caller.Phase || stored.Consumer != caller.Consumer || stored.Purpose != caller.Purpose || stored.Fence != caller.Fence {
		return failure(CategoryRevoked, "secret_consume", nil)
	}
	versions := snapshot.Secrets[secretKey(capability.Scope, stored.ReferenceID)]
	secret, ok := exactActiveSecret(versions, stored.Version)
	if !ok || secret.Purpose != stored.Purpose {
		return failure(CategoryRevoked, "secret_consume", nil)
	}
	aad := canonicalSecretAAD(secret.OrganizationID, secret.ProjectID, secret.ReferenceID, secret.Version, secret.Purpose, secret.KeyReference, secret.Payload.Algorithm)
	if secret.AADDigest != digestStrings(string(aad)) {
		return failure(CategoryIntegrity, "secret_consume", nil)
	}
	plaintext, err := service.keys.Decrypt(ctx, secret.KeyReference, cloneEncryptedPayload(secret.Payload), aad)
	if err != nil {
		return failure(CategoryUnavailable, "secret_decrypt", nil)
	}
	defer zeroBytes(plaintext)
	commandDigest := SensitiveCommandDigest("secret.consume", stored.Version, capability.Scope.Digest, stored.Digest)
	_, err = service.sensitive.Apply(ctx, SensitiveAction{Capability: capability, OperationID: consumeOperationID, CommandDigest: commandDigest, Outcome: "committed", ResourceID: stored.ReferenceID}, func(candidate *Snapshot) error {
		current, exists := candidate.SecretLeases[secretLeaseKey(capability.Scope, stored.ID)]
		if !exists || current.Digest != stored.Digest || current.Revoked || !current.ConsumedAt.IsZero() || !service.clock.Now().Before(current.ExpiresAt) || current.RunID != caller.RunID || current.Phase != caller.Phase || current.Consumer != caller.Consumer || current.Purpose != caller.Purpose || current.Fence != caller.Fence {
			return failure(CategoryRevoked, "secret_consume", nil)
		}
		latest := candidate.Secrets[secretKey(capability.Scope, stored.ReferenceID)]
		if len(latest) == 0 || latest[len(latest)-1].State != StateActive || latest[len(latest)-1].Version != stored.Version {
			return failure(CategoryRevoked, "secret_consume", nil)
		}
		current.ConsumedAt = service.clock.Now().UTC()
		current.Digest = secretLeaseDigest(current)
		candidate.SecretLeases[secretLeaseKey(capability.Scope, stored.ID)] = current
		return nil
	})
	if err != nil {
		return err
	}
	if err = ctx.Err(); err != nil {
		return classifyContext("secret_consume", err)
	}
	return callback(plaintext)
}

// canonicalSecretAAD binds ciphertext to tenant, reference, immutable version, purpose, key and algorithm.
func canonicalSecretAAD(organizationID, projectID, referenceID string, version uint64, purpose, keyReference, algorithm string) []byte {
	return []byte(digestStrings("featureproof.secret.v1", organizationID, projectID, referenceID, fmt.Sprint(version), purpose, keyReference, algorithm))
}

// secretVersionDigest seals immutable metadata, ciphertext and predecessor linkage.
func secretVersionDigest(secret SecretVersion) string {
	return digestStrings(secret.OrganizationID, secret.ProjectID, secret.ReferenceID, fmt.Sprint(secret.Version), secret.Purpose, secret.KeyReference, string(secret.State), secret.Payload.Algorithm, string(secret.Payload.Nonce), string(secret.Payload.Ciphertext), secret.AADDigest, secret.PreviousDigest, secret.CreatedAt.UTC().Format(time.RFC3339Nano))
}

// secretLeaseDigest seals every delivery constraint and its terminal state.
func secretLeaseDigest(lease SecretLease) string {
	return digestStrings(lease.ID, lease.Scope.Digest, lease.ReferenceID, fmt.Sprint(lease.Version), lease.RunID, lease.Phase, lease.Consumer, lease.Purpose, fmt.Sprint(lease.Fence), lease.IssuedAt.UTC().Format(time.RFC3339Nano), lease.ExpiresAt.UTC().Format(time.RFC3339Nano), lease.ConsumedAt.UTC().Format(time.RFC3339Nano), fmt.Sprint(lease.Revoked))
}

// exactActiveSecret locates one immutable version without falling back to another version.
func exactActiveSecret(versions []SecretVersion, version uint64) (SecretVersion, bool) {
	if len(versions) == 0 || versions[len(versions)-1].State != StateActive || versions[len(versions)-1].Version != version {
		return SecretVersion{}, false
	}
	return versions[len(versions)-1], true
}

// cloneEncryptedPayload prevents providers from mutating durable ciphertext buffers.
func cloneEncryptedPayload(payload EncryptedPayload) EncryptedPayload {
	payload.Nonce = slices.Clone(payload.Nonce)
	payload.Ciphertext = slices.Clone(payload.Ciphertext)
	return payload
}

// zeroBytes clears ephemeral plaintext and nonce buffers after private use.
func zeroBytes(value []byte) {
	for index := range value {
		value[index] = 0
	}
}

// secretKey namespaces a reference under both tenant coordinates.
func secretKey(scope TenantScope, referenceID string) string {
	return scopeKey(scope) + "/" + referenceID
}

// secretLeaseKey prevents same-ID leases from colliding across tenant projects.
func secretLeaseKey(scope TenantScope, leaseID string) string { return scopeKey(scope) + "/" + leaseID }
