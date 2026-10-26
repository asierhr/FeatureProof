package kubernetes

import (
	"context"
	"crypto/subtle"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/asierhr/featureProof/internal/environment"
)

// fakeClock fixes lifecycle expiry and attestation time for deterministic tests.
type fakeClock struct{ now time.Time }

// Now returns the controlled composition time.
func (c *fakeClock) Now() time.Time { return c.now }

// Wait advances deterministic lifecycle time without sleeping.
func (c *fakeClock) Wait(ctx context.Context, duration time.Duration) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	c.now = c.now.Add(duration)
	return nil
}

// fakeCluster is a concurrency-safe hermetic implementation of the closed ClusterClient port.
type fakeCluster struct {
	mu          sync.Mutex
	sequence    int
	failAt      string
	loseAt      string
	autoReady   bool
	deleteCalls int
	order       []string
	namespaces  map[string]Namespace
	quotas      map[string]ResourceQuota
	limits      map[string]LimitRange
	accounts    map[string]ServiceAccount
	networks    map[string]NetworkPolicy
	secrets     map[string]Secret
	deployments map[string]Deployment
	services    map[string]Service
	pods        []Pod
	slices      []EndpointSlice
}

// newFakeCluster creates empty isolated Kubernetes state for one test scenario.
func newFakeCluster() *fakeCluster {
	return &fakeCluster{namespaces: map[string]Namespace{}, quotas: map[string]ResourceQuota{}, limits: map[string]LimitRange{}, accounts: map[string]ServiceAccount{}, networks: map[string]NetworkPolicy{}, secrets: map[string]Secret{}, deployments: map[string]Deployment{}, services: map[string]Service{}}
}

// key identifies one namespaced fake resource without accepting global list operations.
func key(namespace, name string) string { return namespace + "/" + name }

// serverMeta assigns immutable fake UID/resourceVersion while preserving desired authority.
func (f *fakeCluster) serverMeta(meta ObjectMeta) ObjectMeta {
	f.sequence++
	meta.UID = fmt.Sprintf("uid-%d", f.sequence)
	meta.ResourceVersion = fmt.Sprintf("rv-%d", f.sequence)
	return meta
}

// before records operation order and injects one content-free typed boundary failure.
func (f *fakeCluster) before(ctx context.Context, operation string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	f.order = append(f.order, operation)
	if f.failAt == operation {
		return &ClientError{Code: CodeForbidden, Operation: operation}
	}
	return nil
}

// after simulates a lost response after the fake API has durably created or deleted an object.
func (f *fakeCluster) after(operation string) error {
	if f.loseAt == operation {
		f.loseAt = ""
		return &ClientError{Code: CodeTransient, Operation: operation}
	}
	return nil
}

// CreateNamespace implements exclusive namespace creation.
func (f *fakeCluster) CreateNamespace(ctx context.Context, value Namespace) (Namespace, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.before(ctx, "namespace"); err != nil {
		return Namespace{}, err
	}
	if _, exists := f.namespaces[value.Meta.Name]; exists {
		return Namespace{}, &ClientError{Code: CodeAlreadyExists, Operation: "namespace"}
	}
	value.Meta = f.serverMeta(value.Meta)
	f.namespaces[value.Meta.Name] = value
	if err := f.after("namespace"); err != nil {
		return Namespace{}, err
	}
	return value, nil
}

// GetNamespace reads exact fake namespace identity.
func (f *fakeCluster) GetNamespace(ctx context.Context, name string) (Namespace, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return Namespace{}, err
	}
	value, exists := f.namespaces[name]
	if !exists {
		return Namespace{}, &ClientError{Code: CodeNotFound, Operation: "namespace"}
	}
	return value, nil
}

// DeleteNamespace applies the UID precondition and confirms absence idempotently.
func (f *fakeCluster) DeleteNamespace(ctx context.Context, name, uid string) (DeleteResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.deleteCalls++
	if err := f.before(ctx, "delete"); err != nil {
		return "", err
	}
	value, exists := f.namespaces[name]
	if !exists {
		return DeleteAbsent, nil
	}
	if value.Meta.UID != uid {
		return "", &ClientError{Code: CodeConflict, Operation: "delete"}
	}
	delete(f.namespaces, name)
	if err := f.after("delete"); err != nil {
		return "", err
	}
	return DeleteRequested, nil
}

// CreateResourceQuota creates one exact aggregate ceiling.
func (f *fakeCluster) CreateResourceQuota(ctx context.Context, value ResourceQuota) (ResourceQuota, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.before(ctx, "quota"); err != nil {
		return ResourceQuota{}, err
	}
	itemKey := key(value.Meta.Namespace, value.Meta.Name)
	if _, exists := f.quotas[itemKey]; exists {
		return ResourceQuota{}, &ClientError{Code: CodeAlreadyExists, Operation: "quota"}
	}
	value.Meta = f.serverMeta(value.Meta)
	f.quotas[itemKey] = value
	if err := f.after("quota"); err != nil {
		return ResourceQuota{}, err
	}
	return value, nil
}

// GetResourceQuota reads one exact aggregate ceiling.
func (f *fakeCluster) GetResourceQuota(ctx context.Context, namespace, name string) (ResourceQuota, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	value, exists := f.quotas[key(namespace, name)]
	if !exists {
		return ResourceQuota{}, &ClientError{Code: CodeNotFound, Operation: "quota"}
	}
	return value, ctx.Err()
}

// CreateLimitRange creates one exact container policy.
func (f *fakeCluster) CreateLimitRange(ctx context.Context, value LimitRange) (LimitRange, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.before(ctx, "limit"); err != nil {
		return LimitRange{}, err
	}
	itemKey := key(value.Meta.Namespace, value.Meta.Name)
	if _, exists := f.limits[itemKey]; exists {
		return LimitRange{}, &ClientError{Code: CodeAlreadyExists, Operation: "limit"}
	}
	value.Meta = f.serverMeta(value.Meta)
	f.limits[itemKey] = value
	if err := f.after("limit"); err != nil {
		return LimitRange{}, err
	}
	return value, nil
}

// GetLimitRange reads one exact container policy.
func (f *fakeCluster) GetLimitRange(ctx context.Context, namespace, name string) (LimitRange, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	value, exists := f.limits[key(namespace, name)]
	if !exists {
		return LimitRange{}, &ClientError{Code: CodeNotFound, Operation: "limit"}
	}
	return value, ctx.Err()
}

// CreateServiceAccount creates one tokenless identity.
func (f *fakeCluster) CreateServiceAccount(ctx context.Context, value ServiceAccount) (ServiceAccount, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.before(ctx, "account"); err != nil {
		return ServiceAccount{}, err
	}
	itemKey := key(value.Meta.Namespace, value.Meta.Name)
	if _, exists := f.accounts[itemKey]; exists {
		return ServiceAccount{}, &ClientError{Code: CodeAlreadyExists, Operation: "account"}
	}
	value.Meta = f.serverMeta(value.Meta)
	f.accounts[itemKey] = value
	if err := f.after("account"); err != nil {
		return ServiceAccount{}, err
	}
	return value, nil
}

// GetServiceAccount reads one exact tokenless identity.
func (f *fakeCluster) GetServiceAccount(ctx context.Context, namespace, name string) (ServiceAccount, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	value, exists := f.accounts[key(namespace, name)]
	if !exists {
		return ServiceAccount{}, &ClientError{Code: CodeNotFound, Operation: "account"}
	}
	return value, ctx.Err()
}

// CreateNetworkPolicy creates one deny or allow policy.
func (f *fakeCluster) CreateNetworkPolicy(ctx context.Context, value NetworkPolicy) (NetworkPolicy, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.before(ctx, "network:"+value.Direction); err != nil {
		return NetworkPolicy{}, err
	}
	itemKey := key(value.Meta.Namespace, value.Meta.Name)
	if _, exists := f.networks[itemKey]; exists {
		return NetworkPolicy{}, &ClientError{Code: CodeAlreadyExists, Operation: "network"}
	}
	value.Meta = f.serverMeta(value.Meta)
	f.networks[itemKey] = value
	if err := f.after("network:" + value.Direction); err != nil {
		return NetworkPolicy{}, err
	}
	return value, nil
}

// GetNetworkPolicy reads one exact deny or allow policy.
func (f *fakeCluster) GetNetworkPolicy(ctx context.Context, namespace, name string) (NetworkPolicy, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	value, exists := f.networks[key(namespace, name)]
	if !exists {
		return NetworkPolicy{}, &ClientError{Code: CodeNotFound, Operation: "network"}
	}
	return value, ctx.Err()
}

// CreateSecret stores a detached fake value only inside hermetic cluster memory.
func (f *fakeCluster) CreateSecret(ctx context.Context, value Secret) (Secret, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.before(ctx, "secret"); err != nil {
		return Secret{}, err
	}
	itemKey := key(value.Meta.Namespace, value.Meta.Name)
	if _, exists := f.secrets[itemKey]; exists {
		return Secret{}, &ClientError{Code: CodeAlreadyExists, Operation: "secret"}
	}
	value.Meta = f.serverMeta(value.Meta)
	stored := value
	stored.Values = make([]SecretValue, len(value.Values))
	for index, item := range value.Values {
		stored.Values[index] = SecretValue{Name: item.Name, Value: append([]byte(nil), item.Value...)}
	}
	f.secrets[itemKey] = stored
	if err := f.after("secret"); err != nil {
		return Secret{}, err
	}
	return value, nil
}

// VerifySecret compares fake sensitive bytes in transport and returns only safe key shape.
func (f *fakeCluster) VerifySecret(ctx context.Context, expected Secret) (Secret, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	value, exists := f.secrets[key(expected.Meta.Namespace, expected.Meta.Name)]
	if !exists {
		return Secret{}, &ClientError{Code: CodeNotFound, Operation: "secret"}
	}
	if len(value.Values) != len(expected.Values) {
		return Secret{}, &ClientError{Code: CodeConflict, Operation: "secret"}
	}
	for index := range value.Values {
		if value.Values[index].Name != expected.Values[index].Name || subtle.ConstantTimeCompare(value.Values[index].Value, expected.Values[index].Value) != 1 {
			return Secret{}, &ClientError{Code: CodeConflict, Operation: "secret"}
		}
	}
	for index := range value.Values {
		value.Values[index].Value = nil
	}
	return value, ctx.Err()
}

// CreateDeployment creates one immutable fake workload.
func (f *fakeCluster) CreateDeployment(ctx context.Context, value Deployment) (Deployment, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.before(ctx, "deployment"); err != nil {
		return Deployment{}, err
	}
	itemKey := key(value.Meta.Namespace, value.Meta.Name)
	if _, exists := f.deployments[itemKey]; exists {
		return Deployment{}, &ClientError{Code: CodeAlreadyExists, Operation: "deployment"}
	}
	value.Meta = f.serverMeta(value.Meta)
	value.Generation = 1
	f.deployments[itemKey] = value
	if err := f.after("deployment"); err != nil {
		return Deployment{}, err
	}
	return value, nil
}

// GetDeployment reads one fake workload and readiness status.
func (f *fakeCluster) GetDeployment(ctx context.Context, namespace, name string) (Deployment, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	value, exists := f.deployments[key(namespace, name)]
	if !exists {
		return Deployment{}, &ClientError{Code: CodeNotFound, Operation: "deployment"}
	}
	return value, ctx.Err()
}

// CreateService creates one immutable fake internal endpoint.
func (f *fakeCluster) CreateService(ctx context.Context, value Service) (Service, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.before(ctx, "service"); err != nil {
		return Service{}, err
	}
	itemKey := key(value.Meta.Namespace, value.Meta.Name)
	if _, exists := f.services[itemKey]; exists {
		return Service{}, &ClientError{Code: CodeAlreadyExists, Operation: "service"}
	}
	value.Meta = f.serverMeta(value.Meta)
	f.services[itemKey] = value
	if err := f.after("service"); err != nil {
		return Service{}, err
	}
	if f.autoReady {
		deploymentKey := key(value.Meta.Namespace, "featureproof-workload")
		deployment := f.deployments[deploymentKey]
		deployment.ObservedGeneration, deployment.AvailableReplicas, deployment.UpdatedReplicas, deployment.Progressing = deployment.Generation, deployment.Replicas, deployment.Replicas, true
		f.deployments[deploymentKey] = deployment
		podMeta := ObjectMeta{Name: "pod-auto", Namespace: value.Meta.Namespace, UID: "pod-auto-uid", ResourceVersion: "pod-auto-rv", Labels: append(append([]Pair(nil), deployment.Meta.Labels...), Pair{Name: "featureproof.io/workload", Value: deployment.Meta.Name}), Annotations: append([]Pair(nil), deployment.Meta.Annotations...)}
		pod := Pod{Meta: podMeta, OwnerUID: deployment.Meta.UID, Revision: deployment.CommitOID, Image: deployment.Image, ImageID: "containerd://" + deployment.Image, Resources: deployment.Resources, ServiceAccount: deployment.ServiceAccount, ContainerPort: deployment.ContainerPort, ReadinessScheme: deployment.ReadinessScheme, ReadinessPath: deployment.ReadinessPath, InitialDelay: deployment.InitialDelay, Period: deployment.Period, Timeout: deployment.Timeout, FailureThreshold: deployment.FailureThreshold, ApplicationSecrets: append([]string(nil), deployment.ApplicationSecrets...), ImagePullSecrets: append([]string(nil), deployment.ImagePullSecrets...), Ready: true, ClosedShape: true}
		f.pods = []Pod{pod}
		f.slices = []EndpointSlice{{Meta: ObjectMeta{Name: "slice-auto", Namespace: value.Meta.Namespace, UID: "slice-auto-uid", ResourceVersion: "slice-auto-rv"}, ServiceName: value.Meta.Name, OwnerUID: value.Meta.UID, AddressType: "IPv4", PortName: value.Scheme, Port: value.Port, Protocol: "TCP", Endpoints: []Endpoint{{Address: "10.2.0.4", PodUID: pod.Meta.UID, PodName: pod.Meta.Name, PodNamespace: pod.Meta.Namespace, TargetKind: "Pod", Ready: true}}, ClosedShape: true}}
	}
	return value, nil
}

// GetService reads one fake internal endpoint.
func (f *fakeCluster) GetService(ctx context.Context, namespace, name string) (Service, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	value, exists := f.services[key(namespace, name)]
	if !exists {
		return Service{}, &ClientError{Code: CodeNotFound, Operation: "service"}
	}
	return value, ctx.Err()
}

// ListOwnedPods returns a bounded detached fake observation.
func (f *fakeCluster) ListOwnedPods(ctx context.Context, _ string, _ string, limit int64) ([]Pod, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	result := []Pod{}
	result = append(result, f.pods...)
	if int64(len(result)) > limit {
		return nil, &ClientError{Code: CodeInvalid, Operation: "pods_limit"}
	}
	return append([]Pod(nil), result...), nil
}

// ListOwnedEndpointSlices returns bounded detached fake service discovery.
func (f *fakeCluster) ListOwnedEndpointSlices(ctx context.Context, _ string, _ string, limit int64) ([]EndpointSlice, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	result := []EndpointSlice{}
	result = append(result, f.slices...)
	if int64(len(result)) > limit {
		return nil, &ClientError{Code: CodeInvalid, Operation: "slices_limit"}
	}
	return append([]EndpointSlice(nil), result...), nil
}

// validFixture builds one sealed catalog, selection, request, provider, and fake cluster.
func validFixture(t *testing.T, withSecret bool) (*KubernetesProvider, *fakeCluster, environment.Selection, environment.AcquisitionRequest, *fakeClock) {
	t.Helper()
	clock := &fakeClock{now: time.Date(2026, 10, 26, 8, 0, 0, 0, time.UTC)}
	entry := Entry{TemplateReference: "web-v1", TemplateDigest: fmt.Sprintf("%064x", 1), ProfileID: "project-standard", NetworkPolicyID: "network-default", ClusterID: "cluster-test", Image: "registry.example/feature@sha256:" + fmt.Sprintf("%064x", 2), Replicas: 1, ContainerPort: 8080, ServicePort: 80, Scheme: "http", BasePath: "/", ReadinessPath: "/ready", InitialDelay: 1, Period: 2, Timeout: 1, FailureThreshold: 3, CPURequestMilli: 100, CPULimitMilli: 200, MemoryRequest: 64 << 20, MemoryLimit: 128 << 20, QuotaCPUMilli: 400, QuotaMemory: 256 << 20, MaxPods: 2, MaxServices: 1, TTL: 30 * time.Minute, MaxTTL: time.Hour, PodSecurity: "restricted", NetworkSupported: true, Ingress: []NetworkRule{{CIDR: "10.0.0.0/24", Port: 80}}, Egress: []NetworkRule{{CIDR: "10.1.0.0/24", Port: 443}}, Capabilities: []string{"http"}}
	secretValues := map[string][]byte{}
	if withSecret {
		entry.SecretReferences = []SecretReference{{ID: "application-token", Kind: "application"}}
		entry.CredentialReference = "application-token"
		secretValues["application-token"] = []byte("test-canary-secret")
	}
	catalog, err := SealCatalog(Catalog{Version: CatalogVersion, Entries: []Entry{entry}})
	if err != nil {
		t.Fatalf("seal catalog: %v", err)
	}
	selection, err := environment.SealSelection(environment.Selection{Version: 1, Alias: "kubernetes-test", ProviderID: ProviderID, ProviderVersion: ProviderVersion, Kind: environment.KindManaged, TemplateReference: entry.TemplateReference, ProfileID: entry.ProfileID, NetworkPolicyID: entry.NetworkPolicyID, Secretless: !withSecret, CredentialReference: map[bool]string{true: "application-token"}[withSecret], ProjectID: "project-1", RepositoryID: "repository-1", Capabilities: []string{"http"}})
	if err != nil {
		t.Fatalf("seal selection: %v", err)
	}
	bundle, err := NewPrivateSecretBundle(secretValues)
	if err != nil {
		t.Fatalf("secret bundle: %v", err)
	}
	cluster := newFakeCluster()
	provider, err := NewProvider(Dependencies{Catalog: catalog, Client: cluster, Clock: clock, Secrets: bundle, Owner: "featureproof-owner", ClusterID: "cluster-test", MaxList: 8})
	if err != nil {
		t.Fatalf("new provider: %v", err)
	}
	request := environment.AcquisitionRequest{RunID: "run-1", IdempotencyKey: fmt.Sprintf("%064x", 3), ProjectID: "project-1", RepositoryID: "repository-1", CommitOID: fmt.Sprintf("%040x", 4), Selection: selection}
	return provider, cluster, selection, request, clock
}

// readyCluster advances the fake workload and endpoints to one fully attested ready state.
func readyCluster(t *testing.T, cluster *fakeCluster, observation environment.Observation, request environment.AcquisitionRequest) {
	t.Helper()
	cluster.mu.Lock()
	defer cluster.mu.Unlock()
	deploymentKey := key(observation.EnvironmentID, "featureproof-workload")
	deployment := cluster.deployments[deploymentKey]
	deployment.ObservedGeneration, deployment.AvailableReplicas, deployment.UpdatedReplicas, deployment.Progressing = deployment.Generation, deployment.Replicas, deployment.Replicas, true
	cluster.deployments[deploymentKey] = deployment
	podMeta := ObjectMeta{Name: "pod-1", Namespace: observation.EnvironmentID, UID: "pod-uid-1", ResourceVersion: "pod-rv-1", Labels: append(append([]Pair(nil), deployment.Meta.Labels...), Pair{Name: "featureproof.io/workload", Value: deployment.Meta.Name}), Annotations: append([]Pair(nil), deployment.Meta.Annotations...)}
	pod := Pod{Meta: podMeta, OwnerUID: deployment.Meta.UID, Revision: request.CommitOID, Image: deployment.Image, ImageID: "containerd://" + deployment.Image, Resources: deployment.Resources, ServiceAccount: deployment.ServiceAccount, ContainerPort: deployment.ContainerPort, ReadinessScheme: deployment.ReadinessScheme, ReadinessPath: deployment.ReadinessPath, InitialDelay: deployment.InitialDelay, Period: deployment.Period, Timeout: deployment.Timeout, FailureThreshold: deployment.FailureThreshold, ApplicationSecrets: append([]string(nil), deployment.ApplicationSecrets...), ImagePullSecrets: append([]string(nil), deployment.ImagePullSecrets...), Ready: true, ClosedShape: true}
	cluster.pods = []Pod{pod}
	service := cluster.services[key(observation.EnvironmentID, "featureproof-service")]
	cluster.slices = []EndpointSlice{{Meta: ObjectMeta{Name: "slice-1", Namespace: observation.EnvironmentID, UID: "slice-uid-1", ResourceVersion: "slice-rv-1"}, ServiceName: service.Meta.Name, OwnerUID: service.Meta.UID, AddressType: "IPv4", PortName: service.Scheme, Port: service.Port, Protocol: "TCP", Endpoints: []Endpoint{{Address: "10.2.0.4", PodUID: pod.Meta.UID, PodName: pod.Meta.Name, PodNamespace: pod.Meta.Namespace, TargetKind: "Pod", Ready: true}}, ClosedShape: true}}
}
