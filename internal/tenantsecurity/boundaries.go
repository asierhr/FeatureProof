package tenantsecurity

import (
	"context"
	"fmt"
	"net"
	"slices"
	"sync"
	"time"
)

// ScopedKey derives cache/object identities only from a verified capability scope.
type ScopedKey struct {
	OrganizationID string
	ProjectID      string
	Class          ResourceClass
	ID             string
	ScopeDigest    string
	Digest         string
}

// NewScopedKey rejects caller-controlled paths and seals a tenant object key.
func NewScopedKey(capability Capability, class ResourceClass, id string) (ScopedKey, error) {
	if capability.Scope.Digest == "" || !validResource(class) || ValidateID(id) != nil {
		return ScopedKey{}, failure(CategoryInvalid, "scoped_key", nil)
	}
	key := ScopedKey{OrganizationID: capability.Scope.OrganizationID, ProjectID: capability.Scope.ProjectID, Class: class, ID: id, ScopeDigest: capability.Scope.Digest}
	key.Digest = digestStrings(key.OrganizationID, key.ProjectID, string(class), id, capability.Scope.Digest)
	return key, nil
}

// ScopedCache is an isolated in-memory cache that verifies full sealed keys.
type ScopedCache struct {
	mu     sync.RWMutex
	values map[string][]byte
}

// NewScopedCache creates an empty cache without global-ID aliases.
func NewScopedCache() *ScopedCache { return &ScopedCache{values: map[string][]byte{}} }

// Put stores a defensive copy under the full tenant key.
func (c *ScopedCache) Put(key ScopedKey, value []byte) error {
	if !validScopedKey(key) || len(value) > MaxPayloadBytes {
		return failure(CategoryInvalid, "cache_put", nil)
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.values[key.Digest] = append([]byte(nil), value...)
	return nil
}

// Get returns a defensive copy only for an exact sealed key.
func (c *ScopedCache) Get(key ScopedKey) ([]byte, bool) {
	if !validScopedKey(key) {
		return nil, false
	}
	c.mu.RLock()
	defer c.mu.RUnlock()
	value, ok := c.values[key.Digest]
	return append([]byte(nil), value...), ok
}

// Delete removes only one exact tenant-scoped key.
func (c *ScopedCache) Delete(key ScopedKey) {
	if !validScopedKey(key) {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.values, key.Digest)
}

// ScopedObject is ciphertext/non-secret object material plus independently checked scope metadata.
type ScopedObject struct {
	Key           ScopedKey
	Payload       []byte
	PayloadDigest string
}

// ScopedObjectStore isolates in-memory object operations by sealed tenant keys.
type ScopedObjectStore struct {
	mu      sync.RWMutex
	objects map[string]ScopedObject
}

// NewScopedObjectStore creates an empty object boundary without global-ID aliases.
func NewScopedObjectStore() *ScopedObjectStore {
	return &ScopedObjectStore{objects: map[string]ScopedObject{}}
}

// Put publishes one immutable scoped object and rejects divergent redelivery.
func (store *ScopedObjectStore) Put(key ScopedKey, payload []byte) error {
	if !validScopedKey(key) || len(payload) > MaxPayloadBytes {
		return failure(CategoryInvalid, "object_put", nil)
	}
	digest := shortDigest(string(payload))
	store.mu.Lock()
	defer store.mu.Unlock()
	if current, ok := store.objects[key.Digest]; ok {
		if current.PayloadDigest != digest {
			return failure(CategoryConflict, "object_put", nil)
		}
		return nil
	}
	store.objects[key.Digest] = ScopedObject{Key: key, Payload: append([]byte(nil), payload...), PayloadDigest: digest}
	return nil
}

// Get returns an exact object only after rechecking stored scope metadata and content digest.
func (store *ScopedObjectStore) Get(key ScopedKey) ([]byte, error) {
	if !validScopedKey(key) {
		return nil, failure(CategoryInvalid, "object_get", nil)
	}
	store.mu.RLock()
	defer store.mu.RUnlock()
	object, ok := store.objects[key.Digest]
	if !ok {
		return nil, failure(CategoryNotFoundHidden, "object_get", nil)
	}
	if object.Key != key || object.PayloadDigest != shortDigest(string(object.Payload)) {
		return nil, failure(CategoryIntegrity, "object_get", nil)
	}
	return append([]byte(nil), object.Payload...), nil
}

// Delete removes only the exact scoped object and returns hidden absence otherwise.
func (store *ScopedObjectStore) Delete(key ScopedKey) error {
	if !validScopedKey(key) {
		return failure(CategoryInvalid, "object_delete", nil)
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	if _, ok := store.objects[key.Digest]; !ok {
		return failure(CategoryNotFoundHidden, "object_delete", nil)
	}
	delete(store.objects, key.Digest)
	return nil
}

// validScopedKey detects forged metadata even when a caller copies another digest.
func validScopedKey(key ScopedKey) bool {
	return ValidateID(key.OrganizationID) == nil && ValidateID(key.ProjectID) == nil && validResource(key.Class) && ValidateID(key.ID) == nil && validDigest(key.ScopeDigest) && key.Digest == digestStrings(key.OrganizationID, key.ProjectID, string(key.Class), key.ID, key.ScopeDigest)
}

// TransportSecurity is composition evidence for protected remote boundaries.
type TransportSecurity struct {
	TLS                  bool
	PeerAuthenticated    bool
	ServerName           string
	IsolatedLoopbackTest bool
}

// ValidateTransport rejects insecure production connections and downgrade.
func ValidateTransport(security TransportSecurity) error {
	if security.IsolatedLoopbackTest {
		host := security.ServerName
		if parsed, _, err := net.SplitHostPort(security.ServerName); err == nil {
			host = parsed
		}
		if host == "localhost" || net.ParseIP(host) != nil && net.ParseIP(host).IsLoopback() {
			return nil
		}
		return failure(CategoryInvalid, "transport_security", nil)
	}
	if !security.TLS || !security.PeerAuthenticated || security.ServerName == "" {
		return failure(CategoryInvalid, "transport_security", nil)
	}
	return nil
}

// TransportGuard pins the accepted production posture across reconnects.
type TransportGuard struct {
	mu       sync.Mutex
	baseline TransportSecurity
}

// NewTransportGuard fails composition closed before any remote client is used.
func NewTransportGuard(initial TransportSecurity) (*TransportGuard, error) {
	if err := ValidateTransport(initial); err != nil {
		return nil, err
	}
	return &TransportGuard{baseline: initial}, nil
}

// ValidateReconnect rejects TLS, peer-auth, hostname or isolation downgrades.
func (guard *TransportGuard) ValidateReconnect(candidate TransportSecurity) error {
	if guard == nil {
		return failure(CategoryInvalid, "transport_reconnect", nil)
	}
	guard.mu.Lock()
	defer guard.mu.Unlock()
	if err := ValidateTransport(candidate); err != nil || candidate.TLS != guard.baseline.TLS || candidate.PeerAuthenticated != guard.baseline.PeerAuthenticated || candidate.ServerName != guard.baseline.ServerName || candidate.IsolatedLoopbackTest != guard.baseline.IsolatedLoopbackTest {
		return failure(CategoryInvalid, "transport_reconnect", nil)
	}
	return nil
}

// RetentionPolicy versions floors and protected record classes.
type RetentionPolicy struct {
	OrganizationID   string                           `json:"organization_id"`
	ProjectID        string                           `json:"project_id,omitempty"`
	Version          uint64                           `json:"version"`
	MinimumAge       time.Duration                    `json:"minimum_age,omitempty"`
	Floors           map[RetentionClass]time.Duration `json:"floors"`
	Periods          map[RetentionClass]time.Duration `json:"periods"`
	Protected        []ResourceClass                  `json:"protected,omitempty"`
	ProtectedRecords map[RetentionClass][]string      `json:"protected_records,omitempty"`
	BatchLimit       uint64                           `json:"batch_limit"`
	GraphLimit       uint64                           `json:"graph_limit"`
	Digest           string                           `json:"digest"`
}

// CompileRetention validates and seals a deterministic retention snapshot.
func CompileRetention(policy RetentionPolicy) (RetentionPolicy, error) {
	classes := retentionClasses()
	if ValidateID(policy.OrganizationID) != nil || policy.Version == 0 || policy.MinimumAge < 0 || len(policy.Protected) > MaxItems || policy.BatchLimit == 0 || policy.GraphLimit == 0 || policy.BatchLimit > uint64(MaxItems) || policy.GraphLimit > uint64(MaxItems) || len(policy.Floors) != len(classes) || len(policy.Periods) != len(classes) || len(policy.ProtectedRecords) > len(classes) {
		return RetentionPolicy{}, failure(CategoryInvalid, "retention_compile", nil)
	}
	if policy.ProjectID != "" && ValidateID(policy.ProjectID) != nil {
		return RetentionPolicy{}, failure(CategoryInvalid, "retention_compile", nil)
	}
	for class := range policy.ProtectedRecords {
		if !slices.Contains(classes, class) {
			return RetentionPolicy{}, failure(CategoryInvalid, "retention_compile", nil)
		}
	}
	for _, class := range policy.Protected {
		if !validResource(class) {
			return RetentionPolicy{}, failure(CategoryInvalid, "retention_compile", nil)
		}
	}
	parts := []string{policy.OrganizationID, policy.ProjectID, fmt.Sprint(policy.Version), policy.MinimumAge.String(), fmt.Sprint(policy.BatchLimit), fmt.Sprint(policy.GraphLimit)}
	floors, periods := make(map[RetentionClass]time.Duration, len(classes)), make(map[RetentionClass]time.Duration, len(classes))
	protectedRecords := make(map[RetentionClass][]string, len(policy.ProtectedRecords))
	for _, class := range classes {
		floor, floorOK := policy.Floors[class]
		period, periodOK := policy.Periods[class]
		if !floorOK || !periodOK || floor < 0 || period < floor {
			return RetentionPolicy{}, failure(CategoryInvalid, "retention_compile", nil)
		}
		parts = append(parts, string(class), floor.String(), period.String())
		floors[class], periods[class] = floor, period
		records := slices.Clone(policy.ProtectedRecords[class])
		slices.Sort(records)
		if len(records) > MaxItems || len(slices.Compact(slices.Clone(records))) != len(records) {
			return RetentionPolicy{}, failure(CategoryInvalid, "retention_compile", nil)
		}
		for _, record := range records {
			if ValidateID(record) != nil {
				return RetentionPolicy{}, failure(CategoryInvalid, "retention_compile", nil)
			}
			parts = append(parts, "protected", record)
		}
		if len(records) != 0 {
			protectedRecords[class] = records
		}
	}
	protected := slices.Clone(policy.Protected)
	slices.Sort(protected)
	if len(slices.Compact(slices.Clone(protected))) != len(protected) {
		return RetentionPolicy{}, failure(CategoryInvalid, "retention_compile", nil)
	}
	for _, class := range protected {
		parts = append(parts, "class", string(class))
	}
	policy.Protected = protected
	policy.Floors, policy.Periods, policy.ProtectedRecords = floors, periods, protectedRecords
	policy.Digest = digestStrings(parts...)
	return policy, nil
}

// Deleter is a consumer-owned irreversible deletion boundary.
type Deleter interface {
	Delete(context.Context, ScopedKey) error
}

// ExecuteDeletion advances a planned intent only after a typed consumer confirms absence/delete.
func (s *Service) ExecuteDeletion(ctx context.Context, capability Capability, intent DeletionIntent, deleter Deleter) error {
	if deleter == nil {
		return failure(CategoryInvalid, "deletion_execute", nil)
	}
	if err := s.VerifyCapability(ctx, capability, ActionRetentionExecute, intent.Resource); err != nil {
		return err
	}
	key, err := NewScopedKey(capability, intent.Resource, intent.ID)
	if err != nil {
		return err
	}
	if err = deleter.Delete(ctx, key); err != nil {
		return failure(CategoryUnavailable, "deletion_execute", err)
	}
	storeKey := intent.Scope.OrganizationID + "/" + intent.Scope.ProjectID + "/" + intent.ID
	s.store.mu.Lock()
	defer s.store.mu.Unlock()
	current, ok := s.store.Deletions[storeKey]
	if !ok || current.Digest != intent.Digest {
		return failure(CategoryConflict, "deletion_execute", nil)
	}
	current.State = "deleted"
	s.store.Deletions[storeKey] = current
	return nil
}
