package kubernetes

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"reflect"
	"strconv"
	"strings"
	"time"

	"github.com/asierhr/featureProof/internal/environment"
)

// Clock supplies deterministic time for expiry metadata and attestation validity.
type Clock interface{ Now() time.Time }

// PrivateSecretBundle owns application and pull secret bytes only inside composition and transport.
type PrivateSecretBundle struct{ values map[string][]byte }

// NewPrivateSecretBundle copies an exact set of opaque secret references into private memory.
func NewPrivateSecretBundle(values map[string][]byte) (*PrivateSecretBundle, error) {
	copyValues := make(map[string][]byte, len(values))
	for reference, value := range values {
		if !boundedID.MatchString(reference) || len(value) == 0 {
			return nil, environmentError(environment.CategoryInvalid, "kubernetes_secret_bundle", nil)
		}
		copyValues[reference] = append([]byte(nil), value...)
	}
	return &PrivateSecretBundle{values: copyValues}, nil
}

// secret returns an attempt-owned copy so the renderer never retains shared sensitive bytes.
func (b *PrivateSecretBundle) secret(reference string) ([]byte, bool) {
	if b == nil {
		return nil, false
	}
	value, present := b.values[reference]
	return append([]byte(nil), value...), present
}

// Dependencies are all trusted collaborators required by KubernetesProvider.
type Dependencies struct {
	Catalog   Catalog
	Client    ClusterClient
	Clock     Clock
	Secrets   *PrivateSecretBundle
	Owner     string
	ClusterID string
	MaxList   int64
}

// KubernetesProvider reconciles one closed Kubernetes environment lifecycle without owning run state.
type KubernetesProvider struct{ deps Dependencies }

// desiredState is the complete canonical object set for one acquisition.
type desiredState struct {
	entry      Entry
	namespace  Namespace
	quota      ResourceQuota
	limit      LimitRange
	account    ServiceAccount
	networks   []NetworkPolicy
	secrets    []Secret
	deployment Deployment
	service    Service
	expiresAt  time.Time
	request    environment.AcquisitionRequest
}

// NewProvider seals dependencies and requires an exact secret bundle for every catalog reference.
func NewProvider(deps Dependencies) (*KubernetesProvider, error) {
	sealed, err := SealCatalog(deps.Catalog)
	if err != nil || deps.Catalog.Digest != sealed.Digest || deps.Client == nil || deps.Clock == nil || !boundedID.MatchString(deps.Owner) || !boundedID.MatchString(deps.ClusterID) || deps.MaxList <= 0 {
		return nil, environmentError(environment.CategoryInvalid, "kubernetes_provider", err)
	}
	references := map[string]struct{}{}
	for _, entry := range sealed.Entries {
		if entry.ClusterID != deps.ClusterID {
			return nil, environmentError(environment.CategoryInvalid, "kubernetes_cluster_authority", nil)
		}
		for _, reference := range entry.SecretReferences {
			references[reference.ID] = struct{}{}
		}
	}
	if len(references) == 0 && deps.Secrets != nil && len(deps.Secrets.values) != 0 {
		return nil, environmentError(environment.CategoryInvalid, "kubernetes_secret_authority", nil)
	}
	for reference := range references {
		if _, present := deps.Secrets.secret(reference); !present {
			return nil, environmentError(environment.CategoryInvalid, "kubernetes_secret_authority", nil)
		}
	}
	if deps.Secrets != nil && len(deps.Secrets.values) != len(references) {
		return nil, environmentError(environment.CategoryInvalid, "kubernetes_secret_authority", nil)
	}
	deps.Catalog = sealed
	return &KubernetesProvider{deps: deps}, nil
}

// Acquire creates or verifies the same desired state for one stable acquisition key.
func (p *KubernetesProvider) Acquire(ctx context.Context, request environment.AcquisitionRequest) (environment.Observation, error) {
	return p.reconcile(ctx, request)
}

// Reconcile recovers an uncertain or partial acquisition without selecting a second identity.
func (p *KubernetesProvider) Reconcile(ctx context.Context, request environment.AcquisitionRequest) (environment.Observation, error) {
	return p.reconcile(ctx, request)
}

// Observe reads and verifies current resources without repairing drift.
func (p *KubernetesProvider) Observe(ctx context.Context, request environment.ObservationRequest) (environment.Observation, error) {
	if err := ctx.Err(); err != nil {
		return environment.Observation{}, mapContext("kubernetes_observe", err)
	}
	entry, err := p.deps.Catalog.Resolve(request.Selection)
	if err != nil || !validObservationRequest(request) {
		return environment.Observation{}, environmentError(environment.CategoryUnauthorized, "kubernetes_observe_authority", err)
	}
	if subtle.ConstantTimeCompare([]byte(request.LifecycleDigest), []byte(providerLifecycleDigest(request.Selection.Digest, request.EnvironmentID, request.LifecycleHandle))) != 1 {
		return environment.Observation{}, environmentError(environment.CategoryIntegrity, "kubernetes_observe_lifecycle", nil)
	}
	namespaceName, namespaceUID, acquisitionDigest, err := parseHandle(request.LifecycleHandle, handleAuthority(request.RunID, request.Selection.Digest, request.RepositoryID, request.CommitOID, request.EnvironmentID))
	if err != nil || namespaceName != request.EnvironmentID {
		return environment.Observation{}, environmentError(environment.CategoryIntegrity, "kubernetes_observe_handle", err)
	}
	namespace, err := p.deps.Client.GetNamespace(ctx, namespaceName)
	if err != nil {
		return environment.Observation{}, p.mapClient("kubernetes_observe_namespace", err)
	}
	if namespace.Meta.UID != namespaceUID {
		return environment.Observation{}, environmentError(environment.CategoryIntegrity, "kubernetes_observe_uid", nil)
	}
	expiry, err := p.verifyNamespaceAuthority(namespace, request.RunID, request.Selection.ProjectID, request.RepositoryID, request.Selection.Digest, request.CommitOID, "", entry)
	if err != nil {
		return environment.Observation{}, err
	}
	acquisitionKey := pairValue(namespace.Meta.Annotations, "featureproof.io/acquisition")
	if subtle.ConstantTimeCompare([]byte(acquisitionDigest), []byte(shortDigest(acquisitionKey))) != 1 {
		return environment.Observation{}, environmentError(environment.CategoryIntegrity, "kubernetes_observe_acquisition", nil)
	}
	acquisition := environment.AcquisitionRequest{RunID: request.RunID, IdempotencyKey: acquisitionKey, ProjectID: request.Selection.ProjectID, RepositoryID: request.RepositoryID, CommitOID: request.CommitOID, Selection: request.Selection}
	desired, err := p.render(acquisition, entry, expiry)
	if err != nil {
		return environment.Observation{}, err
	}
	desired.namespace = namespace
	return p.observeDesired(ctx, desired, request.LifecycleHandle)
}

// Release deletes only the exact confirmed namespace using its immutable UID precondition.
func (p *KubernetesProvider) Release(ctx context.Context, request environment.ReleaseRequest) error {
	if err := ctx.Err(); err != nil {
		return mapContext("kubernetes_release", err)
	}
	entry, err := p.deps.Catalog.Resolve(request.Selection)
	if err != nil || !validReleaseRequest(request) {
		return environmentError(environment.CategoryUnauthorized, "kubernetes_release_authority", err)
	}
	if subtle.ConstantTimeCompare([]byte(request.LifecycleDigest), []byte(providerLifecycleDigest(request.Selection.Digest, request.EnvironmentID, request.LifecycleHandle))) != 1 {
		return environmentError(environment.CategoryIntegrity, "kubernetes_release_lifecycle", nil)
	}
	namespaceName, namespaceUID, acquisitionDigest, err := parseHandle(request.LifecycleHandle, handleAuthority(request.RunID, request.Selection.Digest, request.RepositoryID, request.CommitOID, request.EnvironmentID))
	if err != nil || namespaceName != request.EnvironmentID {
		return environmentError(environment.CategoryIntegrity, "kubernetes_release_handle", err)
	}
	namespace, err := p.deps.Client.GetNamespace(ctx, namespaceName)
	if IsClientCode(err, CodeNotFound) {
		return environmentError(environment.CategoryAlreadyReleased, "kubernetes_release_absent", nil)
	}
	if err != nil {
		return p.mapClient("kubernetes_release_get", err)
	}
	if namespace.Meta.UID != namespaceUID {
		return environmentError(environment.CategoryIntegrity, "kubernetes_release_uid", nil)
	}
	if subtle.ConstantTimeCompare([]byte(acquisitionDigest), []byte(shortDigest(request.AcquisitionKey))) != 1 {
		return environmentError(environment.CategoryIntegrity, "kubernetes_release_acquisition", nil)
	}
	if _, err = p.verifyNamespaceAuthority(namespace, request.RunID, request.ProjectID, request.RepositoryID, request.Selection.Digest, request.CommitOID, request.AcquisitionKey, entry); err != nil {
		return err
	}
	result, err := p.deps.Client.DeleteNamespace(ctx, namespaceName, namespaceUID)
	if err != nil {
		return p.mapClient("kubernetes_release_delete", err)
	}
	if result == DeleteAbsent {
		return environmentError(environment.CategoryAlreadyReleased, "kubernetes_release_absent", nil)
	}
	if result != DeleteRequested {
		return environmentError(environment.CategoryCleanupPending, "kubernetes_release_uncertain", nil)
	}
	return nil
}

// reconcile establishes namespace identity, security bootstrap, workload, and Service in strict order.
func (p *KubernetesProvider) reconcile(ctx context.Context, request environment.AcquisitionRequest) (environment.Observation, error) {
	if err := ctx.Err(); err != nil {
		return environment.Observation{}, mapContext("kubernetes_acquire", err)
	}
	entry, err := p.deps.Catalog.Resolve(request.Selection)
	if err != nil || !validAcquisitionRequest(request) {
		return environment.Observation{}, environmentError(environment.CategoryUnauthorized, "kubernetes_acquire_authority", err)
	}
	desired, err := p.render(request, entry, p.deps.Clock.Now().UTC().Add(entry.TTL))
	if err != nil {
		return environment.Observation{}, err
	}
	namespace, err := p.createOrGetNamespace(ctx, desired.namespace)
	if err != nil {
		return environment.Observation{}, err
	}
	expiry, err := p.verifyNamespaceAuthority(namespace, request.RunID, request.ProjectID, request.RepositoryID, request.Selection.Digest, request.CommitOID, request.IdempotencyKey, entry)
	if err != nil {
		return environment.Observation{}, err
	}
	desired, err = p.render(request, entry, expiry)
	if err != nil || !sameNamespace(namespace, desired.namespace) {
		return environment.Observation{}, environmentError(environment.CategoryIntegrity, "kubernetes_namespace_drift", err)
	}
	desired.namespace = namespace
	if err = p.reconcileBootstrap(ctx, desired); err != nil {
		return environment.Observation{}, err
	}
	if err = p.reconcileSecrets(ctx, desired); err != nil {
		return environment.Observation{}, err
	}
	deployment, err := p.createOrGetDeployment(ctx, desired.deployment)
	if err != nil {
		return environment.Observation{}, err
	}
	desired.deployment.Meta.UID = deployment.Meta.UID
	desired.deployment.Meta.ResourceVersion = deployment.Meta.ResourceVersion
	service, err := p.createOrGetService(ctx, desired.service)
	if err != nil {
		return environment.Observation{}, err
	}
	desired.service.Meta.UID = service.Meta.UID
	desired.service.Meta.ResourceVersion = service.Meta.ResourceVersion
	handle := makeHandle(desired.namespace.Meta.Name, desired.namespace.Meta.UID, request.IdempotencyKey, handleAuthority(request.RunID, request.Selection.Digest, request.RepositoryID, request.CommitOID, desired.namespace.Meta.Name))
	return p.pendingObservation(desired, handle), nil
}

// reconcileBootstrap proves quota, limits, identity, and deny-by-default network policy before workloads.
func (p *KubernetesProvider) reconcileBootstrap(ctx context.Context, desired desiredState) error {
	if _, err := p.createOrGetQuota(ctx, desired.quota); err != nil {
		return err
	}
	if _, err := p.createOrGetLimit(ctx, desired.limit); err != nil {
		return err
	}
	if _, err := p.createOrGetAccount(ctx, desired.account); err != nil {
		return err
	}
	for _, policy := range desired.networks {
		if _, err := p.createOrGetNetwork(ctx, policy); err != nil {
			return err
		}
	}
	return nil
}

// reconcileSecrets sends sensitive values directly to the client and clears attempt-owned copies afterward.
func (p *KubernetesProvider) reconcileSecrets(ctx context.Context, desired desiredState) error {
	for _, secret := range desired.secrets {
		_, err := p.createOrGetSecret(ctx, secret)
		for valueIndex := range secret.Values {
			clear(secret.Values[valueIndex].Value)
		}
		if err != nil {
			return err
		}
	}
	return nil
}

// render constructs only provider-owned typed resources from sealed catalog authority.
func (p *KubernetesProvider) render(request environment.AcquisitionRequest, entry Entry, expiresAt time.Time) (desiredState, error) {
	if expiresAt.IsZero() || !expiresAt.After(p.deps.Clock.Now().UTC().Add(-entry.MaxTTL)) || expiresAt.After(p.deps.Clock.Now().UTC().Add(entry.MaxTTL)) {
		return desiredState{}, environmentError(environment.CategoryIntegrity, "kubernetes_expiry", nil)
	}
	namespaceName := namespaceFor(request.IdempotencyKey)
	labels := []Pair{{"app.kubernetes.io/managed-by", "featureproof"}, {"featureproof.io/owner", p.deps.Owner}, {"featureproof.io/run", shortDigest(request.RunID)}, {"featureproof.io/project", shortDigest(request.ProjectID)}, {"featureproof.io/selection", shortDigest(request.Selection.Digest)}, {"featureproof.io/template", shortDigest(entry.TemplateDigest)}, {"featureproof.io/image", imageDigest(entry.Image)}}
	annotations := []Pair{{"featureproof.io/run-id", request.RunID}, {"featureproof.io/project-id", request.ProjectID}, {"featureproof.io/repository-id", request.RepositoryID}, {"featureproof.io/commit-oid", request.CommitOID}, {"featureproof.io/acquisition", request.IdempotencyKey}, {"featureproof.io/selection-digest", request.Selection.Digest}, {"featureproof.io/template-digest", entry.TemplateDigest}, {"featureproof.io/image-digest", imageDigest(entry.Image)}, {"featureproof.io/created-at", expiresAt.UTC().Add(-entry.TTL).Format(time.RFC3339Nano)}, {"featureproof.io/expires-at", expiresAt.UTC().Format(time.RFC3339Nano)}, {"featureproof.io/ttl-seconds", strconv.FormatInt(int64(entry.TTL/time.Second), 10)}}
	baseMeta := func(name string) ObjectMeta {
		return ObjectMeta{Name: name, Namespace: namespaceName, Labels: append([]Pair(nil), labels...), Annotations: append([]Pair(nil), annotations...)}
	}
	namespace := Namespace{Meta: baseMeta(namespaceName)}
	namespace.Meta.Namespace = ""
	namespace.Meta.Labels = append(namespace.Meta.Labels, Pair{"pod-security.kubernetes.io/enforce", entry.PodSecurity}, Pair{"pod-security.kubernetes.io/enforce-version", "latest"})
	quota := ResourceQuota{Meta: baseMeta("featureproof-quota"), CPUMilli: entry.QuotaCPUMilli, Memory: entry.QuotaMemory, Pods: entry.MaxPods, Services: entry.MaxServices, ClosedShape: true}
	limit := LimitRange{Meta: baseMeta("featureproof-limits"), Min: Resources{CPURequestMilli: entry.CPURequestMilli, CPULimitMilli: entry.CPURequestMilli, MemoryRequest: entry.MemoryRequest, MemoryLimit: entry.MemoryRequest}, Max: Resources{CPURequestMilli: entry.CPULimitMilli, CPULimitMilli: entry.CPULimitMilli, MemoryRequest: entry.MemoryLimit, MemoryLimit: entry.MemoryLimit}, Default: Resources{CPURequestMilli: entry.CPURequestMilli, CPULimitMilli: entry.CPULimitMilli, MemoryRequest: entry.MemoryRequest, MemoryLimit: entry.MemoryLimit}, ClosedShape: true}
	account := ServiceAccount{Meta: baseMeta("featureproof-workload"), AutomountToken: false, ClosedShape: true}
	networks := []NetworkPolicy{{Meta: baseMeta("featureproof-default-deny"), Direction: "deny-all", ClosedShape: true}, {Meta: baseMeta("featureproof-ingress"), Direction: "ingress", Rules: append([]NetworkRule(nil), entry.Ingress...), ClosedShape: true}, {Meta: baseMeta("featureproof-egress"), Direction: "egress", Rules: append([]NetworkRule(nil), entry.Egress...), ClosedShape: true}}
	secrets := make([]Secret, 0, len(entry.SecretReferences))
	applicationSecrets, imagePullSecrets := []string{}, []string{}
	for index, reference := range entry.SecretReferences {
		value, present := p.deps.Secrets.secret(reference.ID)
		if !present {
			return desiredState{}, environmentError(environment.CategoryUnauthorized, "kubernetes_secret_resolution", nil)
		}
		secretName, secretType, secretKey := fmt.Sprintf("featureproof-secret-%d", index), "Opaque", "value"
		if reference.Kind == "image_pull" {
			secretType, secretKey = "kubernetes.io/dockerconfigjson", ".dockerconfigjson"
			imagePullSecrets = append(imagePullSecrets, secretName)
		} else {
			applicationSecrets = append(applicationSecrets, secretName)
		}
		secret := Secret{Meta: baseMeta(secretName), Type: secretType, Values: []SecretValue{{Name: secretKey, Value: value}}, ClosedShape: true}
		secret.Meta.SpecDigest = digest(struct {
			Meta            ObjectMeta
			Type, Reference string
		}{sanitizedMeta(secret.Meta), secret.Type, reference.ID})
		secrets = append(secrets, secret)
	}
	deployment := Deployment{Meta: baseMeta("featureproof-workload"), Image: entry.Image, RepositoryID: request.RepositoryID, CommitOID: request.CommitOID, Replicas: entry.Replicas, ContainerPort: entry.ContainerPort, Resources: Resources{CPURequestMilli: entry.CPURequestMilli, CPULimitMilli: entry.CPULimitMilli, MemoryRequest: entry.MemoryRequest, MemoryLimit: entry.MemoryLimit}, ServiceAccount: account.Meta.Name, PodSecurity: entry.PodSecurity, ReadinessScheme: entry.Scheme, ReadinessPath: entry.ReadinessPath, InitialDelay: entry.InitialDelay, Period: entry.Period, Timeout: entry.Timeout, FailureThreshold: entry.FailureThreshold, ApplicationSecrets: applicationSecrets, ImagePullSecrets: imagePullSecrets, ClosedShape: true}
	deployment.Meta.Labels = append(deployment.Meta.Labels, Pair{Name: "pod-security.kubernetes.io/enforce", Value: entry.PodSecurity})
	service := Service{Meta: baseMeta("featureproof-service"), Selector: deployment.Meta.Name, Scheme: entry.Scheme, Port: entry.ServicePort, TargetPort: entry.ContainerPort, ClosedShape: true}
	namespace.Meta.SpecDigest = resourceDigest(namespace)
	quota.Meta.SpecDigest = resourceDigest(quota)
	limit.Meta.SpecDigest = resourceDigest(limit)
	account.Meta.SpecDigest = resourceDigest(account)
	for index := range networks {
		networks[index].Meta.SpecDigest = resourceDigest(networks[index])
	}
	deployment.Meta.SpecDigest = resourceDigest(deployment)
	service.Meta.SpecDigest = resourceDigest(service)
	return desiredState{entry: entry, namespace: namespace, quota: quota, limit: limit, account: account, networks: networks, secrets: secrets, deployment: deployment, service: service, expiresAt: expiresAt.UTC(), request: request}, nil
}

// observeDesired verifies every security object and computes finite readiness from current resources.
func (p *KubernetesProvider) observeDesired(ctx context.Context, desired desiredState, handle string) (environment.Observation, error) {
	if err := p.verifyBootstrap(ctx, desired); err != nil {
		return environment.Observation{}, err
	}
	deployment, err := p.deps.Client.GetDeployment(ctx, desired.namespace.Meta.Name, desired.deployment.Meta.Name)
	if err != nil {
		return environment.Observation{}, p.mapClient("kubernetes_observe_deployment", err)
	}
	if !sameDeploymentSpec(deployment, desired.deployment) {
		return environment.Observation{}, environmentError(environment.CategoryIntegrity, "kubernetes_deployment_drift", nil)
	}
	service, err := p.deps.Client.GetService(ctx, desired.namespace.Meta.Name, desired.service.Meta.Name)
	if err != nil {
		return environment.Observation{}, p.mapClient("kubernetes_observe_service", err)
	}
	if !sameService(service, desired.service) {
		return environment.Observation{}, environmentError(environment.CategoryIntegrity, "kubernetes_service_drift", nil)
	}
	observation := p.pendingObservation(desired, handle)
	if deployment.ProgressDeadlineExceeded {
		observation.Availability = environment.AvailabilityUnhealthy
		observation.ObservationDigest = observationDigest(observation)
		return observation, nil
	}
	if deployment.ObservedGeneration != deployment.Generation || deployment.AvailableReplicas != desired.entry.Replicas || deployment.UpdatedReplicas != desired.entry.Replicas || !deployment.Progressing {
		return observation, nil
	}
	pods, err := p.deps.Client.ListOwnedPods(ctx, desired.namespace.Meta.Name, deployment.Meta.UID, p.deps.MaxList)
	if err != nil {
		return environment.Observation{}, p.mapClient("kubernetes_observe_pods", err)
	}
	if int32(len(pods)) > desired.entry.Replicas {
		return environment.Observation{}, environmentError(environment.CategoryIntegrity, "kubernetes_pod_set", nil)
	}
	if int32(len(pods)) < desired.entry.Replicas {
		return observation, nil
	}
	podUIDs := make(map[string]Pod, len(pods))
	for _, pod := range pods {
		if _, duplicate := podUIDs[pod.Meta.UID]; duplicate || pod.Meta.UID == "" || !samePodSpec(pod, desired.deployment) || !samePodMeta(pod.Meta, desired.deployment.Meta) || pod.OwnerUID != deployment.Meta.UID || pod.Revision != desired.request.CommitOID || pod.Image != desired.entry.Image || !strings.HasSuffix(pod.ImageID, "@sha256:"+imageDigest(desired.entry.Image)) {
			return environment.Observation{}, environmentError(environment.CategoryIntegrity, "kubernetes_pod_attestation", nil)
		}
		if !pod.Ready || pod.Terminating {
			return observation, nil
		}
		podUIDs[pod.Meta.UID] = pod
	}
	slices, err := p.deps.Client.ListOwnedEndpointSlices(ctx, desired.namespace.Meta.Name, service.Meta.UID, p.deps.MaxList)
	if err != nil {
		return environment.Observation{}, p.mapClient("kubernetes_observe_endpoints", err)
	}
	readyEndpoints := make(map[string]struct{}, len(podUIDs))
	sliceUIDs, endpointAddresses := map[string]struct{}{}, map[string]struct{}{}
	for _, slice := range slices {
		if _, duplicate := sliceUIDs[slice.Meta.UID]; duplicate || slice.Meta.UID == "" || !slice.ClosedShape || slice.OwnerUID != service.Meta.UID || slice.Meta.Namespace != desired.namespace.Meta.Name || slice.ServiceName != service.Meta.Name || slice.PortName != service.Scheme || slice.Port != service.Port || slice.Protocol != "TCP" || (slice.AddressType != "IPv4" && slice.AddressType != "IPv6") {
			return environment.Observation{}, environmentError(environment.CategoryIntegrity, "kubernetes_endpoint_drift", nil)
		}
		sliceUIDs[slice.Meta.UID] = struct{}{}
		for _, endpoint := range slice.Endpoints {
			pod, owned := podUIDs[endpoint.PodUID]
			parsed := net.ParseIP(endpoint.Address)
			_, duplicatePod := readyEndpoints[endpoint.PodUID]
			_, duplicateAddress := endpointAddresses[endpoint.Address]
			if !endpoint.Ready || !owned || duplicatePod || duplicateAddress || parsed == nil || parsed.String() != endpoint.Address || endpoint.PodName != pod.Meta.Name || endpoint.PodNamespace != pod.Meta.Namespace || endpoint.TargetKind != "Pod" || endpoint.TargetAPIVersion != "" || endpoint.TargetResourceVersion != "" || endpoint.TargetFieldPath != "" {
				return environment.Observation{}, environmentError(environment.CategoryIntegrity, "kubernetes_endpoint_drift", nil)
			}
			readyEndpoints[endpoint.PodUID], endpointAddresses[endpoint.Address] = struct{}{}, struct{}{}
		}
	}
	if len(readyEndpoints) != len(podUIDs) {
		return observation, nil
	}
	observation.Availability = environment.AvailabilityReady
	observation.ObservedVersion = digest(struct{ Namespace, Deployment, Service string }{desired.namespace.Meta.ResourceVersion, deployment.Meta.ResourceVersion, service.Meta.ResourceVersion})
	observation.ObservationDigest = observationDigest(observation)
	return observation, nil
}

// pendingObservation constructs a safe provider projection without using readiness as functional evidence.
func (p *KubernetesProvider) pendingObservation(desired desiredState, handle string) environment.Observation {
	host := desired.service.Meta.Name + "." + desired.namespace.Meta.Name + ".svc"
	endpoint := fmt.Sprintf("%s://%s:%d%s", desired.entry.Scheme, host, desired.entry.ServicePort, desired.entry.BasePath)
	observation := environment.Observation{ProviderID: ProviderID, ProviderVersion: ProviderVersion, EnvironmentID: desired.namespace.Meta.Name, Endpoint: endpoint, EnvironmentRef: desired.request.Selection.TemplateReference, ProfileID: desired.request.Selection.ProfileID, ReservationID: desired.namespace.Meta.UID, Fingerprint: imageDigest(desired.entry.Image), LifecycleHandle: handle, ObservedVersion: digest(struct{ Namespace, Deployment, Service string }{desired.namespace.Meta.ResourceVersion, desired.deployment.Meta.ResourceVersion, desired.service.Meta.ResourceVersion}), Availability: environment.AvailabilityPending, Attestation: environment.Attestation{EnvironmentID: desired.namespace.Meta.Name, RepositoryID: desired.request.RepositoryID, CommitOID: desired.request.CommitOID, Reference: "k8s-" + shortDigest(desired.entry.Digest), ValidUntil: desired.expiresAt, Verified: true}, Network: environment.NetworkPolicy{PolicyID: desired.request.Selection.NetworkPolicyID, Schemes: []string{desired.entry.Scheme}, Hosts: []string{host}, Ports: []int{int(desired.entry.ServicePort)}, BasePaths: []string{desired.entry.BasePath}, MaxURLBytes: 2048}, Capabilities: append([]string(nil), desired.entry.Capabilities...)}
	observation.ObservationDigest = observationDigest(observation)
	return observation
}

// verifyBootstrap rejects policy drift during every readiness observation.
func (p *KubernetesProvider) verifyBootstrap(ctx context.Context, desired desiredState) error {
	quota, err := p.deps.Client.GetResourceQuota(ctx, desired.namespace.Meta.Name, desired.quota.Meta.Name)
	if err != nil || !sameQuota(quota, desired.quota) {
		return p.integrityOrClient("kubernetes_quota_drift", err)
	}
	limit, err := p.deps.Client.GetLimitRange(ctx, desired.namespace.Meta.Name, desired.limit.Meta.Name)
	if err != nil || !sameLimit(limit, desired.limit) {
		return p.integrityOrClient("kubernetes_limit_drift", err)
	}
	account, err := p.deps.Client.GetServiceAccount(ctx, desired.namespace.Meta.Name, desired.account.Meta.Name)
	if err != nil || !sameAccount(account, desired.account) {
		return p.integrityOrClient("kubernetes_account_drift", err)
	}
	for _, expected := range desired.networks {
		actual, readErr := p.deps.Client.GetNetworkPolicy(ctx, desired.namespace.Meta.Name, expected.Meta.Name)
		if readErr != nil || !sameNetwork(actual, expected) {
			return p.integrityOrClient("kubernetes_network_drift", readErr)
		}
	}
	return nil
}

// verifyNamespaceAuthority binds owner, run, project, revision, selection, template, image, TTL, and expiry.
func (p *KubernetesProvider) verifyNamespaceAuthority(namespace Namespace, runID, projectID, repositoryID, selectionDigest, commitOID, acquisitionKey string, entry Entry) (time.Time, error) {
	observedAcquisition := pairValue(namespace.Meta.Annotations, "featureproof.io/acquisition")
	if pairValue(namespace.Meta.Labels, "app.kubernetes.io/managed-by") != "featureproof" || pairValue(namespace.Meta.Labels, "featureproof.io/owner") != p.deps.Owner || pairValue(namespace.Meta.Labels, "featureproof.io/run") != shortDigest(runID) || pairValue(namespace.Meta.Labels, "featureproof.io/project") != shortDigest(projectID) || pairValue(namespace.Meta.Labels, "featureproof.io/selection") != shortDigest(selectionDigest) || pairValue(namespace.Meta.Labels, "featureproof.io/template") != shortDigest(entry.TemplateDigest) || pairValue(namespace.Meta.Labels, "featureproof.io/image") != imageDigest(entry.Image) || pairValue(namespace.Meta.Labels, "pod-security.kubernetes.io/enforce") != entry.PodSecurity || pairValue(namespace.Meta.Annotations, "featureproof.io/run-id") != runID || pairValue(namespace.Meta.Annotations, "featureproof.io/project-id") != projectID || pairValue(namespace.Meta.Annotations, "featureproof.io/repository-id") != repositoryID || pairValue(namespace.Meta.Annotations, "featureproof.io/commit-oid") != commitOID || pairValue(namespace.Meta.Annotations, "featureproof.io/selection-digest") != selectionDigest || pairValue(namespace.Meta.Annotations, "featureproof.io/template-digest") != entry.TemplateDigest || pairValue(namespace.Meta.Annotations, "featureproof.io/image-digest") != imageDigest(entry.Image) || !digestPattern(observedAcquisition) || (acquisitionKey != "" && subtle.ConstantTimeCompare([]byte(observedAcquisition), []byte(acquisitionKey)) != 1) {
		return time.Time{}, environmentError(environment.CategoryIntegrity, "kubernetes_namespace_authority", nil)
	}
	ttl, ttlErr := strconv.ParseInt(pairValue(namespace.Meta.Annotations, "featureproof.io/ttl-seconds"), 10, 64)
	created, createdErr := time.Parse(time.RFC3339Nano, pairValue(namespace.Meta.Annotations, "featureproof.io/created-at"))
	expiry, expiryErr := time.Parse(time.RFC3339Nano, pairValue(namespace.Meta.Annotations, "featureproof.io/expires-at"))
	if ttlErr != nil || createdErr != nil || expiryErr != nil || time.Duration(ttl)*time.Second != entry.TTL || expiry.Sub(created) != entry.TTL || created.After(p.deps.Clock.Now().UTC()) || !expiry.After(p.deps.Clock.Now().UTC()) || expiry.After(p.deps.Clock.Now().UTC().Add(entry.MaxTTL)) {
		return time.Time{}, environmentError(environment.CategoryIntegrity, "kubernetes_namespace_expiry", nil)
	}
	return expiry.UTC(), nil
}

// validAcquisitionRequest rejects all caller authority not already sealed by the neutral catalog.
func validAcquisitionRequest(value environment.AcquisitionRequest) bool {
	return boundedID.MatchString(value.RunID) && digestPattern(value.IdempotencyKey) && boundedID.MatchString(value.ProjectID) && boundedID.MatchString(value.RepositoryID) && fullOID.MatchString(value.CommitOID) && value.ProjectID == value.Selection.ProjectID && value.RepositoryID == value.Selection.RepositoryID
}

// validObservationRequest checks the safe identity needed to reconstruct a provider lifecycle.
func validObservationRequest(value environment.ObservationRequest) bool {
	return boundedID.MatchString(value.RunID) && boundedID.MatchString(value.RepositoryID) && fullOID.MatchString(value.CommitOID) && boundedID.MatchString(value.EnvironmentID) && digestPattern(value.LifecycleDigest) && value.RepositoryID == value.Selection.RepositoryID
}

// validReleaseRequest binds cleanup to acquisition, project, repository, and immutable revision authority.
func validReleaseRequest(value environment.ReleaseRequest) bool {
	return boundedID.MatchString(value.RunID) && digestPattern(value.AcquisitionKey) && digestPattern(value.IdempotencyKey) && boundedID.MatchString(value.ProjectID) && boundedID.MatchString(value.RepositoryID) && fullOID.MatchString(value.CommitOID) && boundedID.MatchString(value.EnvironmentID) && digestPattern(value.LifecycleDigest) && value.ProjectID == value.Selection.ProjectID && value.RepositoryID == value.Selection.RepositoryID
}

// namespaceFor derives a DNS-safe name from an opaque acquisition key without exposing caller identifiers.
func namespaceFor(acquisitionKey string) string { return "fp-" + shortDigest(acquisitionKey) }

// imageDigest extracts the immutable SHA-256 value already validated by catalog sealing.
func imageDigest(image string) string {
	return strings.TrimPrefix(image[strings.LastIndex(image, "@")+1:], "sha256:")
}

// providerLifecycleDigest reproduces the neutral lifecycle relation so Observe and Release reject stale records before I/O.
func providerLifecycleDigest(selectionDigest, environmentID, handle string) string {
	return providerStableKey("lifecycle", selectionDigest, ProviderID, ProviderVersion, environmentID, handle)
}

// providerStableKey hashes ordered authority fields with the neutral lifecycle delimiter contract.
func providerStableKey(parts ...string) string {
	hash := sha256.New()
	for _, part := range parts {
		_, _ = hash.Write([]byte(part))
		_, _ = hash.Write([]byte{0})
	}
	return hex.EncodeToString(hash.Sum(nil))
}

// shortDigest bounds labels and names while retaining collision-resistant opaque identity.
func shortDigest(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:12])
}

// handleAuthority binds the opaque handle checksum to every release-critical identity.
func handleAuthority(parts ...string) string { return strings.Join(parts, "\x00") }

// makeHandle creates a bounded opaque lifecycle handle containing namespace identity plus an authority checksum.
func makeHandle(namespace, uid, acquisitionKey, authority string) string {
	acquisitionDigest := shortDigest(acquisitionKey)
	return "kh:" + namespace + ":" + uid + ":" + acquisitionDigest + ":" + shortDigest(authority+"\x00"+namespace+"\x00"+uid+"\x00"+acquisitionDigest)
}

// parseHandle authenticates the lifecycle handle before any namespaced read or delete.
func parseHandle(value, authority string) (string, string, string, error) {
	parts := strings.Split(value, ":")
	if len(parts) != 5 || parts[0] != "kh" || !boundedID.MatchString(parts[1]) || !boundedID.MatchString(parts[2]) || len(parts[3]) != 24 {
		return "", "", "", errors.New("invalid lifecycle handle")
	}
	expected := shortDigest(authority + "\x00" + parts[1] + "\x00" + parts[2] + "\x00" + parts[3])
	if subtle.ConstantTimeCompare([]byte(expected), []byte(parts[4])) != 1 {
		return "", "", "", errors.New("invalid lifecycle handle")
	}
	return parts[1], parts[2], parts[3], nil
}

// observationDigest seals one secret-free provider observation without self-reference.
func observationDigest(value environment.Observation) string {
	value.ObservationDigest = ""
	return digest(value)
}

// resourceDigest fingerprints a desired resource after removing server-owned metadata and status.
func resourceDigest(value any) string {
	switch resource := value.(type) {
	case Namespace:
		resource.Meta = sanitizedMeta(resource.Meta)
		return digest(resource)
	case ResourceQuota:
		resource.Meta = sanitizedMeta(resource.Meta)
		return digest(resource)
	case LimitRange:
		resource.Meta = sanitizedMeta(resource.Meta)
		return digest(resource)
	case ServiceAccount:
		resource.Meta = sanitizedMeta(resource.Meta)
		return digest(resource)
	case NetworkPolicy:
		resource.Meta = sanitizedMeta(resource.Meta)
		return digest(resource)
	case Deployment:
		resource.Meta = sanitizedMeta(resource.Meta)
		resource.Generation, resource.ObservedGeneration, resource.AvailableReplicas, resource.UpdatedReplicas = 0, 0, 0, 0
		resource.Progressing, resource.ProgressDeadlineExceeded = false, false
		return digest(resource)
	case Service:
		resource.Meta = sanitizedMeta(resource.Meta)
		return digest(resource)
	default:
		return ""
	}
}

// sanitizedMeta removes server state and the self-referential desired digest before hashing.
func sanitizedMeta(value ObjectMeta) ObjectMeta {
	value.UID, value.ResourceVersion, value.SpecDigest = "", "", ""
	value.Labels = sortedPairs(value.Labels)
	value.Annotations = sortedPairs(value.Annotations)
	return value
}

// sortedPairs returns canonical provider-owned metadata order.
func sortedPairs(values []Pair) []Pair {
	result := append([]Pair(nil), values...)
	sortPairs(result)
	return result
}

// sortPairs orders metadata in place for stable equality and digesting.
func sortPairs(values []Pair) {
	for left := 0; left < len(values); left++ {
		for right := left + 1; right < len(values); right++ {
			if values[right].Name < values[left].Name {
				values[left], values[right] = values[right], values[left]
			}
		}
	}
}

// pairValue returns an exact provider-owned metadata value or empty for absent/duplicate keys.
func pairValue(values []Pair, name string) string {
	found := ""
	for _, pair := range values {
		if pair.Name == name {
			if found != "" {
				return ""
			}
			found = pair.Value
		}
	}
	return found
}

// sameMeta verifies all provider-owned metadata while allowing only server UID/resourceVersion additions.
func sameMeta(actual, expected ObjectMeta) bool {
	return actual.Name == expected.Name && actual.Namespace == expected.Namespace && actual.SpecDigest == expected.SpecDigest && reflect.DeepEqual(sortedPairs(actual.Labels), sortedPairs(expected.Labels)) && reflect.DeepEqual(sortedPairs(actual.Annotations), sortedPairs(expected.Annotations)) && actual.UID != "" && actual.ResourceVersion != ""
}

// sameNamespace verifies immutable namespace specification and confirmed server identity.
func sameNamespace(actual, expected Namespace) bool {
	return sameMeta(actual.Meta, expected.Meta) && resourceDigest(actual) == expected.Meta.SpecDigest
}

// sameQuota verifies exact aggregate resource ceilings.
func sameQuota(actual, expected ResourceQuota) bool {
	return sameMeta(actual.Meta, expected.Meta) && actual.ClosedShape && expected.ClosedShape && actual.CPUMilli == expected.CPUMilli && actual.Memory == expected.Memory && actual.Pods == expected.Pods && actual.Services == expected.Services
}

// sameLimit verifies exact per-container policy.
func sameLimit(actual, expected LimitRange) bool {
	return sameMeta(actual.Meta, expected.Meta) && actual.ClosedShape && expected.ClosedShape && actual.Min == expected.Min && actual.Max == expected.Max && actual.Default == expected.Default
}

// sameAccount verifies minimal service-account behavior.
func sameAccount(actual, expected ServiceAccount) bool {
	return sameMeta(actual.Meta, expected.Meta) && actual.ClosedShape && expected.ClosedShape && actual.AutomountToken == expected.AutomountToken
}

// sameNetwork verifies deny and allow policies without widening rules.
func sameNetwork(actual, expected NetworkPolicy) bool {
	return sameMeta(actual.Meta, expected.Meta) && actual.ClosedShape && expected.ClosedShape && actual.Direction == expected.Direction && reflect.DeepEqual(sortedRules(actual.Rules), sortedRules(expected.Rules))
}

// sameSecret verifies safe secret metadata and shape without comparing or exposing secret values.
func sameSecret(actual, expected Secret) bool {
	if !sameMeta(actual.Meta, expected.Meta) || !actual.ClosedShape || !expected.ClosedShape || actual.Type != expected.Type || len(actual.Values) != len(expected.Values) {
		return false
	}
	actualNames, expectedNames := make([]string, len(actual.Values)), make([]string, len(expected.Values))
	for index := range actual.Values {
		actualNames[index] = actual.Values[index].Name
	}
	for index := range expected.Values {
		expectedNames[index] = expected.Values[index].Name
	}
	return equalStrings(actualNames, expectedNames)
}

// sameDeploymentSpec verifies immutable workload authority while ignoring readiness status.
func sameDeploymentSpec(actual, expected Deployment) bool {
	return sameMeta(actual.Meta, expected.Meta) && actual.ClosedShape && expected.ClosedShape && actual.Image == expected.Image && actual.RepositoryID == expected.RepositoryID && actual.CommitOID == expected.CommitOID && actual.Replicas == expected.Replicas && actual.ContainerPort == expected.ContainerPort && actual.Resources == expected.Resources && actual.ServiceAccount == expected.ServiceAccount && actual.PodSecurity == expected.PodSecurity && actual.ReadinessScheme == expected.ReadinessScheme && actual.ReadinessPath == expected.ReadinessPath && actual.InitialDelay == expected.InitialDelay && actual.Period == expected.Period && actual.Timeout == expected.Timeout && actual.FailureThreshold == expected.FailureThreshold && equalStrings(actual.ApplicationSecrets, expected.ApplicationSecrets) && equalStrings(actual.ImagePullSecrets, expected.ImagePullSecrets)
}

// sameService verifies internal-only endpoint selection.
func sameService(actual, expected Service) bool {
	return sameMeta(actual.Meta, expected.Meta) && actual.ClosedShape && expected.ClosedShape && actual.Selector == expected.Selector && actual.Scheme == expected.Scheme && actual.Port == expected.Port && actual.TargetPort == expected.TargetPort
}

// samePodMeta authenticates the controller-derived Pod metadata while allowing only its required template-hash label.
func samePodMeta(actual, deployment ObjectMeta) bool {
	if actual.Namespace != deployment.Namespace || actual.Name == "" || actual.UID == "" || actual.ResourceVersion == "" || !reflect.DeepEqual(sortedPairs(actual.Annotations), sortedPairs(deployment.Annotations)) {
		return false
	}
	expectedLabels := append(append([]Pair(nil), deployment.Labels...), Pair{Name: "featureproof.io/workload", Value: deployment.Name})
	actualLabels := make([]Pair, 0, len(actual.Labels))
	templateHash := ""
	for _, label := range actual.Labels {
		if label.Name == "pod-template-hash" {
			if templateHash != "" || !boundedID.MatchString(label.Value) {
				return false
			}
			templateHash = label.Value
			continue
		}
		actualLabels = append(actualLabels, label)
	}
	return reflect.DeepEqual(sortedPairs(actualLabels), sortedPairs(expectedLabels))
}

// samePodSpec proves each routed Pod still implements the exact closed Deployment template selected by composition.
func samePodSpec(actual Pod, expected Deployment) bool {
	return actual.ClosedShape && actual.Resources == expected.Resources && actual.ServiceAccount == expected.ServiceAccount && actual.ContainerPort == expected.ContainerPort && actual.ReadinessScheme == expected.ReadinessScheme && actual.ReadinessPath == expected.ReadinessPath && actual.InitialDelay == expected.InitialDelay && actual.Period == expected.Period && actual.Timeout == expected.Timeout && actual.FailureThreshold == expected.FailureThreshold && equalStrings(actual.ApplicationSecrets, expected.ApplicationSecrets) && equalStrings(actual.ImagePullSecrets, expected.ImagePullSecrets)
}

// mapContext preserves cancellation and deadline as operational lifecycle categories.
func mapContext(operation string, cause error) error {
	if errors.Is(cause, context.DeadlineExceeded) {
		return environmentError(environment.CategoryTimeout, operation, cause)
	}
	return environmentError(environment.CategoryCancelled, operation, cause)
}

// mapClient translates the closed client taxonomy without exposing remote content.
func (p *KubernetesProvider) mapClient(operation string, err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return mapContext(operation, err)
	}
	for _, mapping := range []struct {
		code     ClientCode
		category environment.ErrorCategory
	}{{CodeRateLimited, environment.CategoryRateLimited}, {CodeTransient, environment.CategoryTransient}, {CodeConflict, environment.CategoryConflict}, {CodeForbidden, environment.CategoryUnavailable}, {CodeInvalid, environment.CategoryInvalid}, {CodeNotFound, environment.CategoryUnavailable}, {CodeAlreadyExists, environment.CategoryConflict}} {
		if IsClientCode(err, mapping.code) {
			return environmentError(mapping.category, operation, err)
		}
	}
	return environmentError(environment.CategoryUnavailable, operation, err)
}

// integrityOrClient prefers a typed transport failure and otherwise reports observed drift.
func (p *KubernetesProvider) integrityOrClient(operation string, err error) error {
	if err != nil {
		return p.mapClient(operation, err)
	}
	return environmentError(environment.CategoryIntegrity, operation, nil)
}

// createOrGetNamespace converges AlreadyExists through exact readback.
func (p *KubernetesProvider) createOrGetNamespace(ctx context.Context, expected Namespace) (Namespace, error) {
	actual, err := p.deps.Client.CreateNamespace(ctx, expected)
	if IsClientCode(err, CodeAlreadyExists) {
		actual, err = p.deps.Client.GetNamespace(ctx, expected.Meta.Name)
	}
	if err != nil {
		return Namespace{}, p.mapClient("kubernetes_namespace_create", err)
	}
	return actual, nil
}

// createOrGetQuota converges exact quota creation without updates.
func (p *KubernetesProvider) createOrGetQuota(ctx context.Context, expected ResourceQuota) (ResourceQuota, error) {
	actual, err := p.deps.Client.CreateResourceQuota(ctx, expected)
	if IsClientCode(err, CodeAlreadyExists) {
		actual, err = p.deps.Client.GetResourceQuota(ctx, expected.Meta.Namespace, expected.Meta.Name)
	}
	if err != nil {
		return ResourceQuota{}, p.mapClient("kubernetes_quota_create", err)
	}
	if !sameQuota(actual, expected) {
		return ResourceQuota{}, environmentError(environment.CategoryIntegrity, "kubernetes_quota_drift", nil)
	}
	return actual, nil
}

// createOrGetLimit converges exact LimitRange creation without updates.
func (p *KubernetesProvider) createOrGetLimit(ctx context.Context, expected LimitRange) (LimitRange, error) {
	actual, err := p.deps.Client.CreateLimitRange(ctx, expected)
	if IsClientCode(err, CodeAlreadyExists) {
		actual, err = p.deps.Client.GetLimitRange(ctx, expected.Meta.Namespace, expected.Meta.Name)
	}
	if err != nil {
		return LimitRange{}, p.mapClient("kubernetes_limit_create", err)
	}
	if !sameLimit(actual, expected) {
		return LimitRange{}, environmentError(environment.CategoryIntegrity, "kubernetes_limit_drift", nil)
	}
	return actual, nil
}

// createOrGetAccount converges the tokenless service account without updates.
func (p *KubernetesProvider) createOrGetAccount(ctx context.Context, expected ServiceAccount) (ServiceAccount, error) {
	actual, err := p.deps.Client.CreateServiceAccount(ctx, expected)
	if IsClientCode(err, CodeAlreadyExists) {
		actual, err = p.deps.Client.GetServiceAccount(ctx, expected.Meta.Namespace, expected.Meta.Name)
	}
	if err != nil {
		return ServiceAccount{}, p.mapClient("kubernetes_account_create", err)
	}
	if !sameAccount(actual, expected) {
		return ServiceAccount{}, environmentError(environment.CategoryIntegrity, "kubernetes_account_drift", nil)
	}
	return actual, nil
}

// createOrGetNetwork converges one exact deny or allow policy without updates.
func (p *KubernetesProvider) createOrGetNetwork(ctx context.Context, expected NetworkPolicy) (NetworkPolicy, error) {
	actual, err := p.deps.Client.CreateNetworkPolicy(ctx, expected)
	if IsClientCode(err, CodeAlreadyExists) {
		actual, err = p.deps.Client.GetNetworkPolicy(ctx, expected.Meta.Namespace, expected.Meta.Name)
	}
	if err != nil {
		return NetworkPolicy{}, p.mapClient("kubernetes_network_create", err)
	}
	if !sameNetwork(actual, expected) {
		return NetworkPolicy{}, environmentError(environment.CategoryIntegrity, "kubernetes_network_drift", nil)
	}
	return actual, nil
}

// createOrGetSecret converges only provider-owned secret shape and never logs values.
func (p *KubernetesProvider) createOrGetSecret(ctx context.Context, expected Secret) (Secret, error) {
	actual, err := p.deps.Client.CreateSecret(ctx, expected)
	if IsClientCode(err, CodeAlreadyExists) {
		actual, err = p.deps.Client.VerifySecret(ctx, expected)
	}
	if err != nil {
		return Secret{}, p.mapClient("kubernetes_secret_create", err)
	}
	if !sameSecret(actual, expected) {
		return Secret{}, environmentError(environment.CategoryIntegrity, "kubernetes_secret_drift", nil)
	}
	return actual, nil
}

// createOrGetDeployment converges the immutable workload without patches or updates.
func (p *KubernetesProvider) createOrGetDeployment(ctx context.Context, expected Deployment) (Deployment, error) {
	actual, err := p.deps.Client.CreateDeployment(ctx, expected)
	if IsClientCode(err, CodeAlreadyExists) {
		actual, err = p.deps.Client.GetDeployment(ctx, expected.Meta.Namespace, expected.Meta.Name)
	}
	if err != nil {
		return Deployment{}, p.mapClient("kubernetes_deployment_create", err)
	}
	if !sameDeploymentSpec(actual, expected) {
		return Deployment{}, environmentError(environment.CategoryIntegrity, "kubernetes_deployment_drift", nil)
	}
	return actual, nil
}

// createOrGetService converges the internal Service without external exposure.
func (p *KubernetesProvider) createOrGetService(ctx context.Context, expected Service) (Service, error) {
	actual, err := p.deps.Client.CreateService(ctx, expected)
	if IsClientCode(err, CodeAlreadyExists) {
		actual, err = p.deps.Client.GetService(ctx, expected.Meta.Namespace, expected.Meta.Name)
	}
	if err != nil {
		return Service{}, p.mapClient("kubernetes_service_create", err)
	}
	if !sameService(actual, expected) {
		return Service{}, environmentError(environment.CategoryIntegrity, "kubernetes_service_drift", nil)
	}
	return actual, nil
}
