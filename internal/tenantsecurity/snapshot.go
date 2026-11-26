package tenantsecurity

import "slices"

// Snapshot is the complete ciphertext-only durable state exchanged with adapters.
type Snapshot struct {
	Epoch             uint64                         `json:"epoch"`
	Organizations     map[string]Organization        `json:"organizations"`
	Policies          map[string]TenantPolicy        `json:"policies"`
	Projects          map[string]ProjectRegistration `json:"projects"`
	Principals        map[string]Principal           `json:"principals"`
	ServiceIdentities map[string]ServiceIdentity     `json:"service_identities"`
	Subjects          map[string]string              `json:"subjects"`
	Memberships       map[string]Membership          `json:"memberships"`
	Roles             map[string]Role                `json:"roles"`
	Audits            map[string][]AuditEvent        `json:"audits"`
	AuditHeads        map[string]AuditHead           `json:"audit_heads"`
	Secrets           map[string][]SecretVersion     `json:"secrets"`
	SecretLeases      map[string]SecretLease         `json:"secret_leases"`
	Quotas            map[string]QuotaCounter        `json:"quotas"`
	Reservations      map[string]Reservation         `json:"reservations"`
	Deletions         map[string]DeletionIntent      `json:"deletions"`
	Receipts          map[string]StoreReceipt        `json:"receipts"`
	Bindings          map[string]TenantRunBinding    `json:"bindings"`
	Records           map[string]ScopedRecord        `json:"records"`
	Jobs              map[string]ScopedJob           `json:"jobs"`
	Messages          map[string]ScopedMessage       `json:"messages"`
}

// Snapshot returns defensive copies without exposing store synchronization.
func (s *MemoryStore) Snapshot() Snapshot {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := Snapshot{Epoch: s.Epoch, Organizations: cloneMap(s.Organizations), Policies: map[string]TenantPolicy{}, Projects: cloneMap(s.Projects), Principals: cloneMap(s.Principals), ServiceIdentities: cloneMap(s.ServiceIdentities), Subjects: cloneMap(s.Subjects), Memberships: map[string]Membership{}, Roles: map[string]Role{}, Audits: map[string][]AuditEvent{}, AuditHeads: cloneMap(s.AuditHeads), Secrets: map[string][]SecretVersion{}, SecretLeases: cloneMap(s.SecretLeases), Quotas: cloneMap(s.Quotas), Reservations: cloneMap(s.Reservations), Deletions: cloneMap(s.Deletions), Receipts: cloneMap(s.Receipts), Bindings: cloneMap(s.Bindings), Records: cloneMap(s.Records), Jobs: cloneMap(s.Jobs), Messages: cloneMap(s.Messages)}
	for key, policy := range s.Policies {
		out.Policies[key] = cloneTenantPolicy(policy)
	}
	for k, v := range s.Memberships {
		out.Memberships[k] = cloneMembership(v)
	}
	for k, v := range s.Roles {
		v.Actions = slices.Clone(v.Actions)
		v.Resources = slices.Clone(v.Resources)
		v.ProjectIDs = slices.Clone(v.ProjectIDs)
		out.Roles[k] = v
	}
	for k, v := range s.Audits {
		out.Audits[k] = slices.Clone(v)
	}
	for k, v := range s.Secrets {
		out.Secrets[k] = slices.Clone(v)
	}
	for key, reservation := range out.Reservations {
		reservation.CounterKeys = slices.Clone(reservation.CounterKeys)
		reservation.ResourceIDs = slices.Clone(reservation.ResourceIDs)
		reservation.CompletedIDs = slices.Clone(reservation.CompletedIDs)
		out.Reservations[key] = reservation
	}
	return out
}

// Restore replaces state only after adapter integrity verification.
func (s *MemoryStore) Restore(snapshot Snapshot) error {
	if err := ValidateSnapshot(snapshot); err != nil {
		return err
	}
	if snapshot.Organizations == nil || snapshot.Policies == nil || snapshot.Projects == nil || snapshot.Principals == nil || snapshot.ServiceIdentities == nil || snapshot.Subjects == nil || snapshot.Memberships == nil || snapshot.Roles == nil || snapshot.Audits == nil || snapshot.AuditHeads == nil || snapshot.Secrets == nil || snapshot.SecretLeases == nil || snapshot.Quotas == nil || snapshot.Reservations == nil || snapshot.Deletions == nil || snapshot.Receipts == nil || snapshot.Bindings == nil || snapshot.Records == nil || snapshot.Jobs == nil || snapshot.Messages == nil {
		return failure(CategoryIntegrity, "snapshot_restore", nil)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.Epoch = snapshot.Epoch
	s.Organizations = snapshot.Organizations
	s.Policies = snapshot.Policies
	s.Projects = snapshot.Projects
	s.Principals = snapshot.Principals
	s.ServiceIdentities = snapshot.ServiceIdentities
	s.Subjects = snapshot.Subjects
	s.Memberships = snapshot.Memberships
	s.Roles = snapshot.Roles
	s.Audits = snapshot.Audits
	s.AuditHeads = snapshot.AuditHeads
	s.Secrets = snapshot.Secrets
	s.SecretLeases = snapshot.SecretLeases
	s.Quotas = snapshot.Quotas
	s.Reservations = snapshot.Reservations
	s.Deletions = snapshot.Deletions
	s.Receipts = snapshot.Receipts
	s.Bindings = snapshot.Bindings
	s.Records = snapshot.Records
	s.Jobs = snapshot.Jobs
	s.Messages = snapshot.Messages
	return nil
}

// cloneMap copies scalar-valued maps for snapshot isolation.
func cloneMap[K comparable, V any](in map[K]V) map[K]V {
	out := make(map[K]V, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}
