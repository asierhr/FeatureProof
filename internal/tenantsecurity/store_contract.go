package tenantsecurity

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"slices"
)

// StoreCommand confirms a complete tenant-security snapshot, audit projection and receipt atomically.
type StoreCommand struct {
	OrganizationID string   `json:"organization_id"`
	OperationID    string   `json:"operation_id"`
	CommandDigest  string   `json:"command_digest"`
	ExpectedEpoch  uint64   `json:"expected_epoch"`
	Snapshot       Snapshot `json:"snapshot"`
}

// StoreReceipt resolves redelivery and unknown commit without repeating a security mutation.
type StoreReceipt struct {
	OrganizationID string `json:"organization_id"`
	OperationID    string `json:"operation_id"`
	CommandDigest  string `json:"command_digest"`
	Epoch          uint64 `json:"epoch"`
	SnapshotDigest string `json:"snapshot_digest"`
	Digest         string `json:"digest"`
}

// DurableStore is the consumer-owned contract shared by local and PostgreSQL security adapters.
type DurableStore interface {
	Commit(context.Context, StoreCommand) (StoreReceipt, error)
	Receipt(context.Context, string, string) (StoreReceipt, error)
	Snapshot(context.Context) (Snapshot, error)
}

// SealStoreReceipt seals every idempotency and projection field into an immutable receipt.
func SealStoreReceipt(receipt StoreReceipt) (StoreReceipt, error) {
	if ValidateID(receipt.OrganizationID) != nil || ValidateID(receipt.OperationID) != nil || !validDigest(receipt.CommandDigest) || receipt.Epoch == 0 || receipt.Epoch > MaxDurableCounter || !validDigest(receipt.SnapshotDigest) {
		return StoreReceipt{}, failure(CategoryInvalid, "store_receipt_seal", nil)
	}
	receipt.Digest = digestStrings(receipt.OrganizationID, receipt.OperationID, receipt.CommandDigest, fmt.Sprint(receipt.Epoch), receipt.SnapshotDigest)
	return receipt, nil
}

// ValidateStoreReceipt rejects altered durable receipt columns or payloads.
func ValidateStoreReceipt(receipt StoreReceipt) error {
	sealed, err := SealStoreReceipt(StoreReceipt{OrganizationID: receipt.OrganizationID, OperationID: receipt.OperationID, CommandDigest: receipt.CommandDigest, Epoch: receipt.Epoch, SnapshotDigest: receipt.SnapshotDigest})
	if err != nil || sealed.Digest != receipt.Digest {
		return failure(CategoryIntegrity, "store_receipt_validate", err)
	}
	return nil
}

// SnapshotDigest computes the stable identity of one complete durable security projection.
func SnapshotDigest(snapshot Snapshot) (string, error) {
	payload, err := json.Marshal(snapshot)
	if err != nil {
		return "", failure(CategoryIntegrity, "snapshot_digest", err)
	}
	sum := sha256.Sum256(payload)
	return hex.EncodeToString(sum[:]), nil
}

// CloneSnapshot returns an ownership-safe copy for adapters that enrich a command with receipts.
func CloneSnapshot(snapshot Snapshot) (Snapshot, error) {
	payload, err := json.Marshal(snapshot)
	if err != nil {
		return Snapshot{}, failure(CategoryIntegrity, "snapshot_clone", err)
	}
	var clone Snapshot
	if err = json.Unmarshal(payload, &clone); err != nil {
		return Snapshot{}, failure(CategoryIntegrity, "snapshot_clone", err)
	}
	if err = ValidateSnapshot(clone); err != nil {
		return Snapshot{}, err
	}
	return clone, nil
}

// ValidateStoreCommand checks bounds, CAS and immutable payload identity before adapter I/O.
func ValidateStoreCommand(command StoreCommand) error {
	if command.ExpectedEpoch >= MaxDurableCounter {
		return failure(CategoryExhausted, "store_command_validate", nil)
	}
	if ValidateID(command.OrganizationID) != nil || ValidateID(command.OperationID) != nil || !validDigest(command.CommandDigest) || command.Snapshot.Epoch != command.ExpectedEpoch+1 {
		return failure(CategoryInvalid, "store_command_validate", nil)
	}
	if err := ValidateSnapshot(command.Snapshot); err != nil {
		return err
	}
	if _, ok := command.Snapshot.Organizations[command.OrganizationID]; !ok {
		return failure(CategoryUnauthorized, "store_command_validate", nil)
	}
	return nil
}

// ValidateSnapshot checks namespace, relationship and integrity invariants before reload or commit.
func ValidateSnapshot(snapshot Snapshot) error {
	if snapshot.Epoch > MaxDurableCounter || snapshot.Organizations == nil || snapshot.Policies == nil || snapshot.Projects == nil || snapshot.Principals == nil || snapshot.ServiceIdentities == nil || snapshot.Subjects == nil || snapshot.Memberships == nil || snapshot.Roles == nil || snapshot.Audits == nil || snapshot.AuditHeads == nil || snapshot.Secrets == nil || snapshot.SecretLeases == nil || snapshot.Quotas == nil || snapshot.Reservations == nil || snapshot.Deletions == nil || snapshot.Receipts == nil || snapshot.Bindings == nil || snapshot.Records == nil || snapshot.Jobs == nil || snapshot.Messages == nil {
		return failure(CategoryIntegrity, "snapshot_validate", nil)
	}
	for organizationID, policy := range snapshot.Policies {
		organization, ok := snapshot.Organizations[organizationID]
		if !ok || policy.OrganizationID != organizationID || ValidateTenantPolicy(policy, DefaultLimits()) != nil || organization.PolicyDigest != policy.Digest {
			return failure(CategoryIntegrity, "snapshot_validate", nil)
		}
	}
	for key, project := range snapshot.Projects {
		if key != projectKey(project.OrganizationID, project.ProjectID) || ValidateID(project.OrganizationID) != nil || ValidateID(project.ProjectID) != nil {
			return failure(CategoryIntegrity, "snapshot_validate", nil)
		}
		if _, ok := snapshot.Organizations[project.OrganizationID]; !ok {
			return failure(CategoryIntegrity, "snapshot_validate", nil)
		}
	}
	for key, principal := range snapshot.Principals {
		partsValid := false
		for organizationID := range snapshot.Organizations {
			if key == principalKey(organizationID, principal.ID) {
				partsValid = true
				break
			}
		}
		if !partsValid || ValidateID(principal.ID) != nil {
			return failure(CategoryIntegrity, "snapshot_validate", nil)
		}
	}
	for key, membership := range snapshot.Memberships {
		if key != membershipKey(membership.OrganizationID, membership.PrincipalID) {
			return failure(CategoryIntegrity, "snapshot_validate", nil)
		}
		if _, ok := snapshot.Principals[principalKey(membership.OrganizationID, membership.PrincipalID)]; !ok {
			return failure(CategoryIntegrity, "snapshot_validate", nil)
		}
	}
	for key, identity := range snapshot.ServiceIdentities {
		if key != principalKey(identity.OrganizationID, identity.PrincipalID) || identity.State != StateActive && identity.State != StateSuspended && identity.State != StateRevoked || identity.Version == 0 || ValidateID(identity.Purpose) != nil {
			return failure(CategoryIntegrity, "snapshot_validate", nil)
		}
		principal, principalOK := snapshot.Principals[principalKey(identity.OrganizationID, identity.PrincipalID)]
		_, ownerOK := snapshot.Principals[principalKey(identity.OrganizationID, identity.OwnerPrincipalID)]
		if !principalOK || principal.Kind != PrincipalService || !ownerOK || identity.ExpiresAt.IsZero() {
			return failure(CategoryIntegrity, "snapshot_validate", nil)
		}
	}
	for key, role := range snapshot.Roles {
		if key != roleKey(role.OrganizationID, role.ID) || role.Digest == "" {
			return failure(CategoryIntegrity, "snapshot_validate", nil)
		}
	}
	for organizationID, events := range snapshot.Audits {
		previous := ""
		for index, event := range events {
			if event.OrganizationID != organizationID || event.Sequence != uint64(index+1) || event.PreviousDigest != previous || event.Digest != auditDigest(event) {
				return failure(CategoryIntegrity, "snapshot_validate", nil)
			}
			previous = event.Digest
		}
		if len(events) > 0 {
			head, ok := snapshot.AuditHeads[organizationID]
			expected := sealAuditHead(organizationID, uint64(len(events)), previous)
			if !ok || head != expected {
				return failure(CategoryIntegrity, "snapshot_validate", nil)
			}
		}
	}
	for organizationID := range snapshot.AuditHeads {
		if len(snapshot.Audits[organizationID]) == 0 {
			return failure(CategoryIntegrity, "snapshot_validate", nil)
		}
	}
	for key, versions := range snapshot.Secrets {
		previous := ""
		for index, secret := range versions {
			if key != secret.OrganizationID+"/"+secret.ProjectID+"/"+secret.ReferenceID || secret.Version != uint64(index+1) || secret.PreviousDigest != previous || secret.Digest != secretVersionDigest(secret) || secret.State != StateActive && secret.State != StateRevoked || ValidateID(secret.ReferenceID) != nil || ValidateID(secret.Purpose) != nil {
				return failure(CategoryIntegrity, "snapshot_validate", nil)
			}
			if secret.State == StateActive && (ValidateID(secret.KeyReference) != nil || ValidateID(secret.Payload.Algorithm) != nil || len(secret.Payload.Nonce) == 0 || len(secret.Payload.Ciphertext) == 0 || !validDigest(secret.AADDigest)) {
				return failure(CategoryIntegrity, "snapshot_validate", nil)
			}
			if secret.State == StateRevoked && index != len(versions)-1 {
				return failure(CategoryIntegrity, "snapshot_validate", nil)
			}
			previous = secret.Digest
		}
	}
	for key, lease := range snapshot.SecretLeases {
		if key != lease.Scope.OrganizationID+"/"+lease.Scope.ProjectID+"/"+lease.ID || ValidateID(lease.ID) != nil || ValidateID(lease.ReferenceID) != nil || ValidateID(lease.RunID) != nil || !validPhase(lease.Phase) || ValidateID(lease.Consumer) != nil || ValidateID(lease.Purpose) != nil || lease.Version == 0 || lease.Fence == 0 || !lease.ExpiresAt.After(lease.IssuedAt) || lease.Digest != secretLeaseDigest(lease) {
			return failure(CategoryIntegrity, "snapshot_validate", nil)
		}
	}
	for _, counter := range snapshot.Quotas {
		if counter.PolicyDigest != "" && (ValidateID(counter.OrganizationID) != nil || ValidateID(counter.ProjectID) != nil || ValidateID(counter.Dimension) != nil || counter.Level != "tenant" && counter.Level != "project" || !validDigest(counter.PolicyDigest) || !validQuotaLimit(counter.Limit) || counter.WindowStart.IsZero() || counter.Reserved > counter.Limit.Limit+counter.Limit.Burst) {
			return failure(CategoryIntegrity, "snapshot_validate", nil)
		}
	}
	for key, reservation := range snapshot.Reservations {
		if reservation.PolicyDigest == "" {
			continue
		}
		if key != quotaReservationKey(reservation.Scope, reservation.ID) || !validDigest(reservation.PolicyDigest) || !validDigest(reservation.DemandDigest) || reservation.PolicyVersion == 0 || reservation.Units == 0 || reservation.ExpiresAt.IsZero() || len(reservation.CounterKeys) != 2 || reservation.Digest != reservationDigest(reservation) || reservation.Released && reservation.Uncertain || !slices.IsSorted(reservation.ResourceIDs) || hasDuplicates(reservation.ResourceIDs) || !slices.IsSorted(reservation.CompletedIDs) || hasDuplicates(reservation.CompletedIDs) {
			return failure(CategoryIntegrity, "snapshot_validate", nil)
		}
		for _, id := range reservation.ResourceIDs {
			if ValidateID(id) != nil {
				return failure(CategoryIntegrity, "snapshot_validate", nil)
			}
		}
		for _, id := range reservation.CompletedIDs {
			if !slices.Contains(reservation.ResourceIDs, id) {
				return failure(CategoryIntegrity, "snapshot_validate", nil)
			}
		}
	}
	for key, intent := range snapshot.Deletions {
		if intent.EligibilityDigest == "" {
			continue
		}
		if key != deletionKey(intent.Scope, intent.ID) || !slices.Contains([]string{DeletionPlanned, DeletionDeleting, DeletionUncertain, DeletionTombstone}, intent.State) || intent.Fence == 0 || intent.PolicyVersion == 0 || !validDigest(intent.PolicyDigest) || !validDigest(intent.EligibilityDigest) || !validDigest(intent.CandidateDigest) || !validDigest(intent.DeleteDigest) || intent.Digest != deletionIntentDigest(intent) || intent.State == DeletionTombstone && !validDigest(intent.TombstoneDigest) {
			return failure(CategoryIntegrity, "snapshot_validate", nil)
		}
	}
	for key, receipt := range snapshot.Receipts {
		if key != receipt.OrganizationID+"/"+receipt.OperationID || ValidateStoreReceipt(receipt) != nil || receipt.Epoch > snapshot.Epoch {
			return failure(CategoryIntegrity, "snapshot_validate", nil)
		}
	}
	for key, binding := range snapshot.Bindings {
		if key != bindingKey(binding.Scope, binding.RunID) || ValidateTenantRunBinding(binding) != nil {
			return failure(CategoryIntegrity, "snapshot_validate", nil)
		}
	}
	for key, record := range snapshot.Records {
		if key != recordKey(record.OrganizationID, record.ProjectID, record.Class, record.ID) || !validEntityClass(record.Class) || record.Version == 0 || record.Digest != scopedRecordDigest(record) {
			return failure(CategoryIntegrity, "snapshot_validate", nil)
		}
		if _, ok := snapshot.Projects[projectKey(record.OrganizationID, record.ProjectID)]; !ok {
			return failure(CategoryIntegrity, "snapshot_validate", nil)
		}
	}
	for key, job := range snapshot.Jobs {
		if key != jobKey(job.OrganizationID, job.ProjectID, job.JobID) || job.Fence == 0 || job.Digest != scopedJobDigest(job) {
			return failure(CategoryIntegrity, "snapshot_validate", nil)
		}
		if _, ok := snapshot.Bindings[job.OrganizationID+"/"+job.ProjectID+"/"+job.RunID]; !ok {
			return failure(CategoryIntegrity, "snapshot_validate", nil)
		}
	}
	for key, message := range snapshot.Messages {
		if key != messageKey(message.OrganizationID, message.ProjectID, message.Kind, message.IdempotencyKey) || !validMessageKind(message.Kind) || message.Fence == 0 || message.Digest != scopedMessageDigest(message) {
			return failure(CategoryIntegrity, "snapshot_validate", nil)
		}
		if _, ok := snapshot.Projects[projectKey(message.OrganizationID, message.ProjectID)]; !ok {
			return failure(CategoryIntegrity, "snapshot_validate", nil)
		}
	}
	return nil
}

// ValidateReceiptHistory prevents a later aggregate from deleting or rewriting committed receipts.
func ValidateReceiptHistory(current, next Snapshot) error {
	for key, receipt := range current.Receipts {
		candidate, ok := next.Receipts[key]
		if !ok || candidate != receipt {
			return failure(CategoryIntegrity, "store_receipt_history", nil)
		}
	}
	for organizationID, events := range current.Audits {
		candidate := next.Audits[organizationID]
		if len(candidate) < len(events) {
			return failure(CategoryIntegrity, "store_audit_history", nil)
		}
		for index := range events {
			if candidate[index] != events[index] {
				return failure(CategoryIntegrity, "store_audit_history", nil)
			}
		}
	}
	for key, versions := range current.Secrets {
		candidate := next.Secrets[key]
		if len(candidate) < len(versions) {
			return failure(CategoryIntegrity, "store_secret_history", nil)
		}
		for index := range versions {
			if candidate[index].Digest != versions[index].Digest || secretVersionDigest(candidate[index]) != versions[index].Digest {
				return failure(CategoryIntegrity, "store_secret_history", nil)
			}
		}
	}
	for key, lease := range current.SecretLeases {
		candidate, ok := next.SecretLeases[key]
		if !ok || candidate.ID != lease.ID || candidate.Scope.Digest != lease.Scope.Digest || candidate.ReferenceID != lease.ReferenceID || candidate.Version != lease.Version || candidate.RunID != lease.RunID || candidate.Phase != lease.Phase || candidate.Consumer != lease.Consumer || candidate.Purpose != lease.Purpose || candidate.Fence != lease.Fence || candidate.IssuedAt != lease.IssuedAt || candidate.ExpiresAt != lease.ExpiresAt || !lease.ConsumedAt.IsZero() && candidate.ConsumedAt != lease.ConsumedAt || lease.Revoked && !candidate.Revoked {
			return failure(CategoryIntegrity, "store_secret_lease_history", nil)
		}
	}
	return nil
}

// validDigest recognizes only lowercase canonical SHA-256 identities.
func validDigest(value string) bool {
	if len(value) != sha256.Size*2 {
		return false
	}
	decoded, err := hex.DecodeString(value)
	return err == nil && hex.EncodeToString(decoded) == value
}
