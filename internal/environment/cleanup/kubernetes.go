package cleanup

import (
	"context"
	"encoding/json"
	"strconv"
	"sync"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes"
)

// KubernetesInventoryConfig seals one cluster owner scope and page budget.
type KubernetesInventoryConfig struct {
	ProviderID, ProviderVersion, Owner string
	IdentityPath                       string
	Client                             kubernetes.Interface
	MaxPages, PageSize                 int
}

// KubernetesInventory is the destructive identity separated from the read-only validation tool.
type KubernetesInventory struct {
	config     KubernetesInventoryConfig
	mu         sync.Mutex
	identities map[string]kubernetesIdentity
	version    uint64
	fault      func(string) error
}

// kubernetesIdentity keeps raw namespace authority private to the adapter.
type kubernetesIdentity struct {
	Name, CleanupID, BindingDigest, Digest string
	UID                                    types.UID
}

// NewKubernetesInventory rejects missing client or unbounded owner scope.
func NewKubernetesInventory(config KubernetesInventoryConfig) (*KubernetesInventory, error) {
	if config.Client == nil || config.IdentityPath == "" || !safeID.MatchString(config.ProviderID) || !safeID.MatchString(config.ProviderVersion) || !safeID.MatchString(config.Owner) || config.MaxPages <= 0 || config.PageSize <= 0 {
		return nil, &Error{Category: CategoryInvalid, Operation: "kubernetes_inventory"}
	}
	inventory := &KubernetesInventory{config: config, identities: map[string]kubernetesIdentity{}, version: 1}
	if err := ensureDurableParent(config.IdentityPath); err != nil {
		return nil, &Error{Category: CategoryIntegrity, Operation: "kubernetes_identity_directory", Cause: err}
	}
	release, err := acquireCleanupStoreLock(context.Background(), config.IdentityPath+".lock")
	if err != nil {
		return nil, &Error{Category: CategoryIntegrity, Operation: "kubernetes_identity_lock", Cause: err}
	}
	defer release()
	state, err := loadDurableSnapshot(config.IdentityPath, "kubernetes-identities")
	if err != nil {
		return nil, &Error{Category: CategoryIntegrity, Operation: "kubernetes_identity_open", Cause: err}
	}
	if len(state.Payload) == 0 {
		return inventory, nil
	}
	inventory.identities, err = decodeKubernetesIdentities(state.Payload)
	if err != nil {
		return nil, err
	}
	inventory.version = state.Version
	return inventory, nil
}

// decodeKubernetesIdentities validates every raw namespace authority loaded through the private durable head.
func decodeKubernetesIdentities(raw []byte) (map[string]kubernetesIdentity, error) {
	identities := map[string]kubernetesIdentity{}
	if json.Unmarshal(raw, &identities) != nil {
		return nil, &Error{Category: CategoryIntegrity, Operation: "kubernetes_identity_open"}
	}
	for cleanupID, identity := range identities {
		candidate := identity
		candidate.Digest = ""
		if cleanupID != identity.CleanupID || !safeID.MatchString(identity.Name) || identity.UID == "" || !digestPattern.MatchString(identity.BindingDigest) || identity.Digest != digestOf(candidate) {
			return nil, &Error{Category: CategoryIntegrity, Operation: "kubernetes_identity_open"}
		}
	}
	return identities, nil
}

// List returns only namespaces carrying the exact configured FeatureProof owner metadata.
func (k *KubernetesInventory) List(ctx context.Context, token string, limit int, snapshot string) (InventoryPage, error) {
	if err := ctx.Err(); err != nil {
		return InventoryPage{}, err
	}
	if limit <= 0 || limit > k.config.PageSize {
		return InventoryPage{}, &Error{Category: CategoryInvalid, Operation: "kubernetes_list"}
	}
	selector := labels.SelectorFromSet(labels.Set{"featureproof.io/managed-by": "featureproof"}).String()
	list, err := k.config.Client.CoreV1().Namespaces().List(ctx, metav1.ListOptions{LabelSelector: selector, Limit: int64(limit), Continue: token, ResourceVersion: snapshot})
	if err != nil {
		return InventoryPage{}, mapKubernetesError(ctx, "kubernetes_list", err)
	}
	page := InventoryPage{Next: list.Continue, SnapshotVersion: list.ResourceVersion}
	for _, namespace := range list.Items {
		labels, annotations := namespace.Labels, namespace.Annotations
		if labels["featureproof.io/managed-by"] != "featureproof" || annotations["featureproof.io/owner"] != k.config.Owner {
			continue
		}
		created, createdErr := time.Parse(time.RFC3339Nano, annotations["featureproof.io/created-at"])
		expires, expiresErr := time.Parse(time.RFC3339Nano, annotations["featureproof.io/expires-at"])
		ttl, ttlErr := time.ParseDuration(annotations["featureproof.io/ttl"])
		binding := LifecycleCleanupBinding{Version: BindingVersion, ProjectID: annotations["featureproof.io/project"], RunID: annotations["featureproof.io/run"], ProviderID: k.config.ProviderID, ProviderVersion: k.config.ProviderVersion, SelectionDigest: annotations["featureproof.io/selection-digest"], AcquisitionKey: annotations["featureproof.io/acquisition-key"], AcquisitionDigest: annotations["featureproof.io/acquisition-digest"], EnvironmentDigest: digestOf(namespace.Name), ResourceIdentityDigest: digestOf(string(namespace.UID)), LifecycleDigest: annotations["featureproof.io/lifecycle-digest"], CleanupKey: annotations["featureproof.io/cleanup-key"], CapacityReceiptDigest: annotations["featureproof.io/capacity-receipt-digest"], LifecycleRecordDigest: annotations["featureproof.io/lifecycle-record-digest"]}
		binding, bindingErr := SealLifecycleCleanupBinding(binding)
		cpu, cpuErr := strconv.ParseUint(annotations["featureproof.io/cpu-milli"], 10, 64)
		memory, memoryErr := strconv.ParseUint(annotations["featureproof.io/memory-bytes"], 10, 64)
		objects, objectsErr := strconv.ParseUint(annotations["featureproof.io/object-count"], 10, 64)
		if createdErr != nil || expiresErr != nil || ttlErr != nil || bindingErr != nil || cpuErr != nil || memoryErr != nil || objectsErr != nil || namespace.UID == "" {
			if safeID.MatchString(annotations["featureproof.io/project"]) {
				page.Quarantines = append(page.Quarantines, InventoryQuarantine{ProjectID: annotations["featureproof.io/project"], IdentityDigest: digestOf(struct{ Name, UID string }{namespace.Name, string(namespace.UID)})})
			}
			continue
		}
		ownerDigest := digestOf(k.config.Owner)
		item := Item{Version: Version, CleanupID: "orphan-" + shortDigest(binding.Digest), Source: "orphan", OwnerDigest: ownerDigest, ProjectID: binding.ProjectID, RunID: binding.RunID, Binding: binding, CreatedAt: created, ExpiresAt: expires, TTL: ttl, Demand: ResourceDemand{Environments: 1, CPUMilli: cpu, MemoryBytes: memory, ObjectCount: objects}, State: StateDiscovered, VersionNumber: 1}
		item, itemErr := SealItem(item)
		if itemErr != nil {
			continue
		}
		identity := kubernetesIdentity{Name: namespace.Name, UID: namespace.UID, CleanupID: item.CleanupID, BindingDigest: item.Binding.Digest}
		identity.Digest = digestOf(identity)
		if err = k.persistIdentity(ctx, identity); err != nil {
			return InventoryPage{}, err
		}
		page.Candidates = append(page.Candidates, InventoryCandidate{Item: item, OwnerDigest: ownerDigest, SnapshotVersion: list.ResourceVersion})
	}
	return page, nil
}

// ReconcileCleanup rereads exact namespace identity, deletes with UID precondition and confirms absence.
func (k *KubernetesInventory) ReconcileCleanup(ctx context.Context, item Item) (LifecycleStatus, error) {
	if err := VerifyItem(item); err != nil || item.Source != "orphan" || item.Binding.ProviderID != k.config.ProviderID || item.Binding.ProviderVersion != k.config.ProviderVersion {
		return LifecycleUncertain, &Error{Category: CategoryUnauthorized, Operation: "kubernetes_cleanup"}
	}
	if err := k.refreshIdentities(ctx); err != nil {
		return LifecycleUncertain, err
	}
	k.mu.Lock()
	identity, ok := k.identities[item.CleanupID]
	k.mu.Unlock()
	if !ok {
		return LifecycleUncertain, &Error{Category: CategoryIntegrity, Operation: "kubernetes_private_identity"}
	}
	if identity.BindingDigest != item.Binding.Digest {
		return LifecycleUncertain, &Error{Category: CategoryIntegrity, Operation: "kubernetes_private_identity"}
	}
	return k.DeleteNamespace(ctx, identity.Name, identity.UID, item.Binding)
}

// persistIdentity durably binds raw namespace authority before its digest-only candidate can advance discovery.
func (k *KubernetesInventory) persistIdentity(ctx context.Context, identity kubernetesIdentity) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if k.fault != nil {
		if err := k.fault("before_write"); err != nil {
			return &Error{Category: CategoryIntegrity, Operation: "kubernetes_identity_fault", Cause: err}
		}
	}
	release, err := acquireCleanupStoreLock(ctx, k.config.IdentityPath+".lock")
	if err != nil {
		return &Error{Category: CategoryIntegrity, Operation: "kubernetes_identity_lock", Cause: err}
	}
	defer release()
	state, err := loadDurableSnapshot(k.config.IdentityPath, "kubernetes-identities")
	if err != nil {
		return &Error{Category: CategoryIntegrity, Operation: "kubernetes_identity_load", Cause: err}
	}
	candidate := map[string]kubernetesIdentity{}
	if len(state.Payload) > 0 {
		candidate, err = decodeKubernetesIdentities(state.Payload)
		if err != nil {
			return err
		}
	}
	if current, ok := candidate[identity.CleanupID]; ok {
		if current.Digest != identity.Digest {
			return &Error{Category: CategoryConflict, Operation: "kubernetes_identity_redelivery"}
		}
		k.replaceIdentities(candidate, state.Version)
		return nil
	}
	candidate[identity.CleanupID] = identity
	if k.fault != nil {
		for _, boundary := range []string{"before_index", "after_index"} {
			if err = k.fault(boundary); err != nil {
				return &Error{Category: CategoryIntegrity, Operation: "kubernetes_identity_fault", Cause: err}
			}
		}
	}
	raw, err := json.Marshal(candidate)
	if err != nil {
		return &Error{Category: CategoryIntegrity, Operation: "kubernetes_identity_encode", Cause: err}
	}
	if err = publishDurableSnapshot(ctx, k.config.IdentityPath, "kubernetes-identities", state, raw, k.durableFault); err != nil {
		return &Error{Category: CategoryIntegrity, Operation: "kubernetes_identity_publish", Cause: err}
	}
	k.replaceIdentities(candidate, state.Version+1)
	return nil
}

// durableFault keeps the pre-lock boundary separate from receipt/head publication faults.
func (k *KubernetesInventory) durableFault(boundary string) error {
	if k.fault == nil || boundary == "before_write" {
		return nil
	}
	return k.fault(boundary)
}

// replaceIdentities updates the private read cache only after a durable head is selected.
func (k *KubernetesInventory) replaceIdentities(identities map[string]kubernetesIdentity, version uint64) {
	k.mu.Lock()
	k.identities, k.version = identities, version
	k.mu.Unlock()
}

// refreshIdentities reloads the private head so a restarted or concurrent adapter sees exact raw authority.
func (k *KubernetesInventory) refreshIdentities(ctx context.Context) error {
	release, err := acquireCleanupStoreLock(ctx, k.config.IdentityPath+".lock")
	if err != nil {
		return err
	}
	defer release()
	state, err := loadDurableSnapshot(k.config.IdentityPath, "kubernetes-identities")
	if err != nil {
		return err
	}
	identities := map[string]kubernetesIdentity{}
	if len(state.Payload) > 0 {
		identities, err = decodeKubernetesIdentities(state.Payload)
		if err != nil {
			return err
		}
	}
	k.replaceIdentities(identities, state.Version)
	return nil
}

// DeleteNamespace performs the adapter-private exact cleanup after a trusted name-to-binding lookup.
func (k *KubernetesInventory) DeleteNamespace(ctx context.Context, name string, uid types.UID, expected LifecycleCleanupBinding) (LifecycleStatus, error) {
	if !safeID.MatchString(name) || uid == "" || VerifyLifecycleCleanupBinding(expected) != nil || digestOf(name) != expected.EnvironmentDigest || digestOf(string(uid)) != expected.ResourceIdentityDigest {
		return LifecycleUncertain, &Error{Category: CategoryUnauthorized, Operation: "kubernetes_delete"}
	}
	namespace, err := k.config.Client.CoreV1().Namespaces().Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		if apierrors.IsNotFound(err) {
			return LifecycleAbsent, nil
		}
		return LifecycleUncertain, mapKubernetesError(ctx, "kubernetes_get", err)
	}
	if namespace.UID != uid || namespace.Annotations["featureproof.io/owner"] != k.config.Owner {
		return LifecycleUncertain, &Error{Category: CategoryIntegrity, Operation: "kubernetes_identity"}
	}
	if namespace.DeletionTimestamp != nil {
		return LifecyclePresent, nil
	}
	err = k.config.Client.CoreV1().Namespaces().Delete(ctx, name, metav1.DeleteOptions{Preconditions: &metav1.Preconditions{UID: &uid}})
	if err != nil {
		if apierrors.IsNotFound(err) {
			return LifecycleAbsent, nil
		}
		return LifecycleUncertain, mapKubernetesError(ctx, "kubernetes_delete", err)
	}
	_, err = k.config.Client.CoreV1().Namespaces().Get(ctx, name, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return LifecycleAbsent, nil
	}
	if err != nil {
		return LifecycleUncertain, mapKubernetesError(ctx, "kubernetes_confirm", err)
	}
	return LifecyclePresent, nil
}

// mapKubernetesError removes remote bodies while preserving cancellation.
func mapKubernetesError(ctx context.Context, operation string, err error) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if apierrors.IsForbidden(err) || apierrors.IsUnauthorized(err) {
		return &Error{Category: CategoryUnauthorized, Operation: operation, Cause: err}
	}
	if apierrors.IsConflict(err) || apierrors.IsInvalid(err) {
		return &Error{Category: CategoryIntegrity, Operation: operation, Cause: err}
	}
	if apierrors.IsTooManyRequests(err) {
		return &Error{Category: CategoryRateLimited, Operation: operation, Cause: err}
	}
	if apierrors.IsTimeout(err) || apierrors.IsServerTimeout(err) {
		return &Error{Category: CategoryTimeout, Operation: operation, Cause: err}
	}
	return &Error{Category: CategoryTransient, Operation: operation, Cause: err}
}

// shortDigest creates a bounded non-secret orphan identifier.
func shortDigest(value string) string {
	if len(value) < 24 {
		return value
	}
	return value[:24]
}
