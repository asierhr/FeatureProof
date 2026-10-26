package kubernetes

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/asierhr/featureProof/internal/environment"
)

// TestKubernetesCatalogRequiresExactTrustedImmutableSelection covers catalog sealing and immutable image authority.
func TestKubernetesCatalogRequiresExactTrustedImmutableSelection(t *testing.T) {
	_, _, selection, _, _ := validFixture(t, false)
	provider, _, _, _, _ := validFixture(t, false)
	if _, err := provider.deps.Catalog.Resolve(selection); err != nil {
		t.Fatalf("resolve exact selection: %v", err)
	}
	entry := provider.deps.Catalog.Entries[0]
	entry.Image = "registry.example/feature:latest"
	if _, err := SealCatalog(Catalog{Version: CatalogVersion, Entries: []Entry{entry}}); err == nil {
		t.Fatal("mutable image accepted")
	}
	entry = provider.deps.Catalog.Entries[0]
	entry.CPULimitMilli = entry.QuotaCPUMilli + 1
	if _, err := SealCatalog(Catalog{Version: CatalogVersion, Entries: []Entry{entry}}); err == nil {
		t.Fatal("policy widening accepted")
	}
	raw, _ := json.Marshal(provider.deps.Catalog)
	raw = append(raw[:len(raw)-1], []byte(`,"unknown":true}`)...)
	if _, err := DecodeCatalog(raw); err == nil {
		t.Fatal("unknown field accepted")
	}
	duplicate := []byte(`{"version":1,"version":1,"entries":[],"digest":""}`)
	if _, err := DecodeCatalog(duplicate); err == nil {
		t.Fatal("duplicate field accepted")
	}
	tampered := selection
	tampered.ProfileID = "other"
	if _, err := provider.deps.Catalog.Resolve(tampered); err == nil {
		t.Fatal("recalculated authority not required")
	}
	for _, invalid := range []string{
		"registry.example//feature@sha256:" + strings.Repeat("2", 64),
		"registry.example/a/../feature@sha256:" + strings.Repeat("2", 64),
		"registry.example/a/./feature@sha256:" + strings.Repeat("2", 64),
		"Registry.example/feature@sha256:" + strings.Repeat("2", 64),
		"registry.example/feature:latest@sha256:" + strings.Repeat("2", 64),
		"feature@sha256:" + strings.Repeat("2", 64),
		"registry.example/feature@sha256:" + strings.Repeat("2", 32),
		"registry.example/feature@sha512:" + strings.Repeat("2", 128),
	} {
		candidate := provider.deps.Catalog.Entries[0]
		candidate.Image = invalid
		if _, err := SealCatalog(Catalog{Version: CatalogVersion, Entries: []Entry{candidate}}); err == nil {
			t.Fatalf("noncanonical OCI reference accepted: %s", invalid)
		}
	}
}

// TestKubernetesProviderIsolatesAndAuthenticatesNamespacePerRun covers deterministic per-run isolation and hostile namespace rejection.
func TestKubernetesProviderIsolatesAndAuthenticatesNamespacePerRun(t *testing.T) {
	provider, cluster, _, request, _ := validFixture(t, false)
	first, err := provider.Acquire(context.Background(), request)
	if err != nil {
		t.Fatalf("first acquire: %v", err)
	}
	request.RunID, request.IdempotencyKey = "run-2", fmt.Sprintf("%064x", 5)
	second, err := provider.Acquire(context.Background(), request)
	if err != nil {
		t.Fatalf("second acquire: %v", err)
	}
	if first.EnvironmentID == second.EnvironmentID || first.ReservationID == second.ReservationID {
		t.Fatal("runs shared namespace identity")
	}
	cluster.mu.Lock()
	hostile := cluster.namespaces[first.EnvironmentID]
	hostile.Meta.Annotations[0].Value = "hostile"
	cluster.namespaces[first.EnvironmentID] = hostile
	cluster.mu.Unlock()
	request.RunID, request.IdempotencyKey = "run-1", fmt.Sprintf("%064x", 3)
	if _, err = provider.Reconcile(context.Background(), request); !environment.IsCategory(err, environment.CategoryIntegrity) {
		t.Fatalf("hostile namespace category: %v", err)
	}
}

// TestKubernetesProviderHasNoArbitraryManifestOrCommandPath audits the closed request and client surfaces.
func TestKubernetesProviderHasNoArbitraryManifestOrCommandPath(t *testing.T) {
	for _, subject := range []reflect.Type{reflect.TypeOf(environment.AcquisitionRequest{}), reflect.TypeOf(Entry{})} {
		for fieldIndex := 0; fieldIndex < subject.NumField(); fieldIndex++ {
			name := strings.ToLower(subject.Field(fieldIndex).Name)
			if strings.Contains(name, "manifest") || name == "command" || name == "args" || name == "volume" || name == "serviceaccountname" || name == "namespace" {
				t.Fatalf("arbitrary field exposed: %s", name)
			}
		}
	}
	clientType := reflect.TypeOf((*ClusterClient)(nil)).Elem()
	for methodIndex := 0; methodIndex < clientType.NumMethod(); methodIndex++ {
		name := strings.ToLower(clientType.Method(methodIndex).Name)
		if strings.Contains(name, "exec") || strings.Contains(name, "patch") || strings.Contains(name, "apply") || strings.Contains(name, "log") || strings.Contains(name, "proxy") {
			t.Fatalf("arbitrary operation exposed: %s", name)
		}
	}
}

// TestKubernetesBootstrapPrecedesEveryWorkloadAndFailsClosed covers the mandatory security ordering.
func TestKubernetesBootstrapPrecedesEveryWorkloadAndFailsClosed(t *testing.T) {
	provider, cluster, _, request, _ := validFixture(t, false)
	if _, err := provider.Acquire(context.Background(), request); err != nil {
		t.Fatalf("acquire: %v", err)
	}
	want := []string{"namespace", "quota", "limit", "account", "network:deny-all", "network:ingress", "network:egress", "deployment", "service"}
	if !reflect.DeepEqual(cluster.order, want) {
		t.Fatalf("unsafe order: %v", cluster.order)
	}
	for _, boundary := range want[1:7] {
		candidate, failingCluster, _, failingRequest, _ := validFixture(t, false)
		failingCluster.failAt = boundary
		if _, err := candidate.Acquire(context.Background(), failingRequest); err == nil {
			t.Fatalf("boundary %s succeeded", boundary)
		}
		if len(failingCluster.deployments) != 0 {
			t.Fatalf("workload created after %s failure", boundary)
		}
	}
}

// TestKubernetesResourcesCannotExceedProjectPolicy covers exact quota, limits, and workload resources.
func TestKubernetesResourcesCannotExceedProjectPolicy(t *testing.T) {
	provider, cluster, _, request, _ := validFixture(t, false)
	observation, err := provider.Acquire(context.Background(), request)
	if err != nil {
		t.Fatalf("acquire: %v", err)
	}
	quota := cluster.quotas[key(observation.EnvironmentID, "featureproof-quota")]
	deployment := cluster.deployments[key(observation.EnvironmentID, "featureproof-workload")]
	if quota.CPUMilli != 400 || quota.Memory != 256<<20 || deployment.Resources.CPURequestMilli != 100 || deployment.Resources.CPULimitMilli != 200 {
		t.Fatal("resource policy not rendered exactly")
	}
	cluster.mu.Lock()
	deployment.Resources.CPULimitMilli++
	cluster.deployments[key(observation.EnvironmentID, deployment.Meta.Name)] = deployment
	cluster.mu.Unlock()
	if _, err = provider.Observe(context.Background(), observationRequest(observation, request)); !environment.IsCategory(err, environment.CategoryIntegrity) {
		t.Fatalf("resource drift category: %v", err)
	}
}

// TestKubernetesEnvironmentIsDenyByDefaultBeforeWorkload covers exact network bootstrap and target policy.
func TestKubernetesEnvironmentIsDenyByDefaultBeforeWorkload(t *testing.T) {
	provider, cluster, _, request, _ := validFixture(t, false)
	observation, err := provider.Acquire(context.Background(), request)
	if err != nil {
		t.Fatalf("acquire: %v", err)
	}
	deny := cluster.networks[key(observation.EnvironmentID, "featureproof-default-deny")]
	if deny.Direction != "deny-all" || len(deny.Rules) != 0 || observation.Network.PolicyID != request.Selection.NetworkPolicyID {
		t.Fatal("deny-by-default authority missing")
	}
	cluster.mu.Lock()
	deny.Direction = "ingress"
	cluster.networks[key(observation.EnvironmentID, deny.Meta.Name)] = deny
	cluster.mu.Unlock()
	if _, err = provider.Observe(context.Background(), observationRequest(observation, request)); !environment.IsCategory(err, environment.CategoryIntegrity) {
		t.Fatalf("network drift category: %v", err)
	}
}

// TestKubernetesClosedShapeReadbackFailsDuringAdoptionAndObserve proves narrowed DTOs cannot adopt or attest widened authoritative objects.
func TestKubernetesClosedShapeReadbackFailsDuringAdoptionAndObserve(t *testing.T) {
	// cases enumerates every authoritative object whose transport readback carries a closed-shape verdict.
	cases := map[string]func(*fakeCluster, string){
		"quota": func(cluster *fakeCluster, namespace string) {
			value := cluster.quotas[key(namespace, "featureproof-quota")]
			value.ClosedShape = false
			cluster.quotas[key(namespace, value.Meta.Name)] = value
		},
		"network": func(cluster *fakeCluster, namespace string) {
			value := cluster.networks[key(namespace, "featureproof-default-deny")]
			value.ClosedShape = false
			cluster.networks[key(namespace, value.Meta.Name)] = value
		},
		"limit": func(cluster *fakeCluster, namespace string) {
			value := cluster.limits[key(namespace, "featureproof-limits")]
			value.ClosedShape = false
			cluster.limits[key(namespace, value.Meta.Name)] = value
		},
		"deployment": func(cluster *fakeCluster, namespace string) {
			value := cluster.deployments[key(namespace, "featureproof-workload")]
			value.ClosedShape = false
			cluster.deployments[key(namespace, value.Meta.Name)] = value
		},
		"service": func(cluster *fakeCluster, namespace string) {
			value := cluster.services[key(namespace, "featureproof-service")]
			value.ClosedShape = false
			cluster.services[key(namespace, value.Meta.Name)] = value
		},
	}
	for name, mutate := range cases {
		t.Run(name+"_already_exists", func(t *testing.T) {
			provider, cluster, _, request, _ := validFixture(t, false)
			acquired, err := provider.Acquire(context.Background(), request)
			if err != nil {
				t.Fatal(err)
			}
			cluster.mu.Lock()
			mutate(cluster, acquired.EnvironmentID)
			cluster.mu.Unlock()
			if _, err = provider.Reconcile(context.Background(), request); !environment.IsCategory(err, environment.CategoryIntegrity) {
				t.Fatalf("widened object adopted: %v", err)
			}
		})
		t.Run(name+"_observe", func(t *testing.T) {
			provider, cluster, _, request, _ := validFixture(t, false)
			acquired, err := provider.Acquire(context.Background(), request)
			if err != nil {
				t.Fatal(err)
			}
			cluster.mu.Lock()
			mutate(cluster, acquired.EnvironmentID)
			cluster.mu.Unlock()
			if _, err = provider.Observe(context.Background(), observationRequest(acquired, request)); !environment.IsCategory(err, environment.CategoryIntegrity) {
				t.Fatalf("widened object observed: %v", err)
			}
		})
	}
}

// TestKubernetesAcquireAndReconcileConvergeAcrossEveryCreateBoundary covers redelivery without duplicate objects.
func TestKubernetesAcquireAndReconcileConvergeAcrossEveryCreateBoundary(t *testing.T) {
	provider, cluster, _, request, _ := validFixture(t, false)
	first, err := provider.Acquire(context.Background(), request)
	if err != nil {
		t.Fatalf("acquire: %v", err)
	}
	second, err := provider.Reconcile(context.Background(), request)
	if err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if first.EnvironmentID != second.EnvironmentID || first.LifecycleHandle != second.LifecycleHandle || len(cluster.namespaces) != 1 || len(cluster.deployments) != 1 || len(cluster.services) != 1 {
		t.Fatal("redelivery did not converge")
	}
}

// TestKubernetesReconcileRecoversEveryLostCreateResponse covers uncertain effects at each resource boundary.
func TestKubernetesReconcileRecoversEveryLostCreateResponse(t *testing.T) {
	boundaries := []string{"namespace", "quota", "limit", "account", "network:deny-all", "network:ingress", "network:egress", "secret", "deployment", "service"}
	for _, boundary := range boundaries {
		t.Run(strings.ReplaceAll(boundary, ":", "_"), func(t *testing.T) {
			provider, cluster, _, request, _ := validFixture(t, boundary == "secret")
			cluster.loseAt = boundary
			if _, err := provider.Acquire(context.Background(), request); !environment.IsCategory(err, environment.CategoryTransient) {
				t.Fatalf("lost response category: %v", err)
			}
			observation, err := provider.Reconcile(context.Background(), request)
			if err != nil || observation.EnvironmentID == "" {
				t.Fatalf("reconcile after %s: %v", boundary, err)
			}
			if len(cluster.namespaces) != 1 || len(cluster.deployments) != 1 || len(cluster.services) != 1 {
				t.Fatalf("duplicate after %s", boundary)
			}
		})
	}
}

// TestKubernetesReadinessRequiresRolloutPodsServiceAndEndpoints covers pending, unhealthy, and fully ready projections.
func TestKubernetesReadinessRequiresRolloutPodsServiceAndEndpoints(t *testing.T) {
	provider, cluster, _, request, _ := validFixture(t, false)
	acquired, err := provider.Acquire(context.Background(), request)
	if err != nil {
		t.Fatalf("acquire: %v", err)
	}
	pending, err := provider.Observe(context.Background(), observationRequest(acquired, request))
	if err != nil || pending.Availability != environment.AvailabilityPending {
		t.Fatalf("pending: %v %s", err, pending.Availability)
	}
	readyCluster(t, cluster, acquired, request)
	ready, err := provider.Observe(context.Background(), observationRequest(acquired, request))
	if err != nil || ready.Availability != environment.AvailabilityReady {
		t.Fatalf("ready: %v %s", err, ready.Availability)
	}
	cluster.mu.Lock()
	deployment := cluster.deployments[key(acquired.EnvironmentID, "featureproof-workload")]
	deployment.ProgressDeadlineExceeded = true
	cluster.deployments[key(acquired.EnvironmentID, deployment.Meta.Name)] = deployment
	cluster.mu.Unlock()
	unhealthy, err := provider.Observe(context.Background(), observationRequest(acquired, request))
	if err != nil || unhealthy.Availability != environment.AvailabilityUnhealthy {
		t.Fatalf("unhealthy: %v %s", err, unhealthy.Availability)
	}
}

// TestKubernetesReadinessRejectsNonExactPodAndEndpointSets covers duplicated, foreign, and metadata-tampered routing candidates.
func TestKubernetesReadinessRejectsNonExactPodAndEndpointSets(t *testing.T) {
	tests := map[string]func(*fakeCluster){
		"duplicate_pod_uid": func(cluster *fakeCluster) { cluster.pods = append(cluster.pods, cluster.pods[0]) },
		"pod_annotation":    func(cluster *fakeCluster) { cluster.pods[0].Meta.Annotations[0].Value = "tampered" },
		"pod_closed_shape":  func(cluster *fakeCluster) { cluster.pods[0].ClosedShape = false },
		"foreign_pod": func(cluster *fakeCluster) {
			foreign := cluster.pods[0]
			foreign.Meta.Name, foreign.Meta.UID, foreign.OwnerUID = "foreign", "foreign-uid", "foreign-owner"
			cluster.pods = append(cluster.pods, foreign)
		},
		"foreign_endpoint": func(cluster *fakeCluster) {
			cluster.slices[0].Endpoints = append(cluster.slices[0].Endpoints, Endpoint{Address: "10.2.0.5", PodUID: "foreign-uid", PodName: "foreign", PodNamespace: cluster.pods[0].Meta.Namespace, Ready: true})
		},
		"duplicate_endpoint": func(cluster *fakeCluster) {
			cluster.slices[0].Endpoints = append(cluster.slices[0].Endpoints, cluster.slices[0].Endpoints[0])
		},
		"foreign_slice_owner": func(cluster *fakeCluster) { cluster.slices[0].OwnerUID = "foreign-service" },
		"slice_port_name":     func(cluster *fakeCluster) { cluster.slices[0].PortName = "other" },
		"target_api_version":  func(cluster *fakeCluster) { cluster.slices[0].Endpoints[0].TargetAPIVersion = "v1" },
		"target_resource_version": func(cluster *fakeCluster) {
			cluster.slices[0].Endpoints[0].TargetResourceVersion = "2"
		},
		"target_field_path": func(cluster *fakeCluster) { cluster.slices[0].Endpoints[0].TargetFieldPath = "spec.nodeName" },
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			provider, cluster, _, request, _ := validFixture(t, false)
			acquired, err := provider.Acquire(context.Background(), request)
			if err != nil {
				t.Fatal(err)
			}
			readyCluster(t, cluster, acquired, request)
			cluster.mu.Lock()
			mutate(cluster)
			cluster.mu.Unlock()
			if _, err = provider.Observe(context.Background(), observationRequest(acquired, request)); !environment.IsCategory(err, environment.CategoryIntegrity) {
				t.Fatalf("non-exact readiness set accepted: %v", err)
			}
		})
	}
}

// TestKubernetesAttestationBindsRunningImageRepositoryCommitAndEndpoint covers immutable runtime attestation.
func TestKubernetesAttestationBindsRunningImageRepositoryCommitAndEndpoint(t *testing.T) {
	provider, cluster, _, request, _ := validFixture(t, false)
	acquired, err := provider.Acquire(context.Background(), request)
	if err != nil {
		t.Fatalf("acquire: %v", err)
	}
	readyCluster(t, cluster, acquired, request)
	ready, err := provider.Observe(context.Background(), observationRequest(acquired, request))
	if err != nil {
		t.Fatalf("observe: %v", err)
	}
	if !ready.Attestation.Verified || ready.Attestation.RepositoryID != request.RepositoryID || ready.Attestation.CommitOID != request.CommitOID || !strings.Contains(ready.Endpoint, acquired.EnvironmentID+".svc") {
		t.Fatal("attestation relationship incomplete")
	}
	cluster.mu.Lock()
	cluster.pods[0].ImageID = "containerd://registry.example/other@sha256:" + fmt.Sprintf("%064x", 9)
	cluster.mu.Unlock()
	if _, err = provider.Observe(context.Background(), observationRequest(acquired, request)); !environment.IsCategory(err, environment.CategoryIntegrity) {
		t.Fatalf("image drift category: %v", err)
	}
}

// TestKubernetesSecretsNeverCrossProviderBoundary covers secret confinement and secretless behavior.
func TestKubernetesSecretsNeverCrossProviderBoundary(t *testing.T) {
	provider, cluster, _, request, _ := validFixture(t, true)
	observation, err := provider.Acquire(context.Background(), request)
	if err != nil {
		t.Fatalf("acquire: %v", err)
	}
	encoded, _ := json.Marshal(observation)
	if strings.Contains(string(encoded), "test-canary-secret") {
		t.Fatal("secret leaked into observation")
	}
	secret := cluster.secrets[key(observation.EnvironmentID, "featureproof-secret-0")]
	if string(secret.Values[0].Value) != "test-canary-secret" {
		t.Fatal("secret not delivered to cluster")
	}
	if deployment := cluster.deployments[key(observation.EnvironmentID, "featureproof-workload")]; !equalStrings(deployment.ApplicationSecrets, []string{"featureproof-secret-0"}) {
		t.Fatal("application secret not referenced by workload")
	}
	imageEntry := provider.deps.Catalog.Entries[0]
	imageEntry.SecretReferences[0].Kind = "image_pull"
	imageCatalog, imageCatalogErr := SealCatalog(Catalog{Version: CatalogVersion, Entries: []Entry{imageEntry}})
	if imageCatalogErr != nil {
		t.Fatal(imageCatalogErr)
	}
	imageCluster := newFakeCluster()
	imageProvider, imageProviderErr := NewProvider(Dependencies{Catalog: imageCatalog, Client: imageCluster, Clock: provider.deps.Clock, Secrets: provider.deps.Secrets, Owner: provider.deps.Owner, ClusterID: provider.deps.ClusterID, MaxList: provider.deps.MaxList})
	if imageProviderErr != nil {
		t.Fatal(imageProviderErr)
	}
	imageObservation, imageAcquireErr := imageProvider.Acquire(context.Background(), request)
	if imageAcquireErr != nil {
		t.Fatal(imageAcquireErr)
	}
	if imageSecret := imageCluster.secrets[key(imageObservation.EnvironmentID, "featureproof-secret-0")]; imageSecret.Type != "kubernetes.io/dockerconfigjson" || !equalStrings(imageCluster.deployments[key(imageObservation.EnvironmentID, "featureproof-workload")].ImagePullSecrets, []string{"featureproof-secret-0"}) {
		t.Fatal("image pull secret not confined to pod reference")
	}
	secretless, secretlessCluster, _, secretlessRequest, _ := validFixture(t, false)
	if _, err = secretless.Acquire(context.Background(), secretlessRequest); err != nil || len(secretlessCluster.secrets) != 0 {
		t.Fatalf("secretless created material: %v", err)
	}
}

// TestKubernetesSelectionSecretAuthorityMustMatchCatalogBeforeIO binds neutral secret claims to the private entry and bundle.
func TestKubernetesSelectionSecretAuthorityMustMatchCatalogBeforeIO(t *testing.T) {
	secretProvider, secretCluster, secretSelection, secretRequest, _ := validFixture(t, true)
	secretlessClaim := secretSelection
	secretlessClaim.Secretless, secretlessClaim.CredentialReference, secretlessClaim.Digest = true, "", ""
	secretlessClaim, _ = environment.SealSelection(secretlessClaim)
	secretRequest.Selection = secretlessClaim
	if _, err := secretProvider.Acquire(context.Background(), secretRequest); !environment.IsCategory(err, environment.CategoryUnauthorized) || len(secretCluster.namespaces) != 0 {
		t.Fatalf("secretless mismatch crossed cluster boundary: %v", err)
	}
	secretlessProvider, secretlessCluster, secretlessSelection, secretlessRequest, _ := validFixture(t, false)
	credentialClaim := secretlessSelection
	credentialClaim.Secretless, credentialClaim.CredentialReference, credentialClaim.Digest = false, "application-token", ""
	credentialClaim, _ = environment.SealSelection(credentialClaim)
	secretlessRequest.Selection = credentialClaim
	if _, err := secretlessProvider.Acquire(context.Background(), secretlessRequest); !environment.IsCategory(err, environment.CategoryUnauthorized) || len(secretlessCluster.namespaces) != 0 {
		t.Fatalf("credential mismatch crossed cluster boundary: %v", err)
	}
}

// TestKubernetesEnvironmentCarriesVerifiableOwnerAndExpiry covers independent cleanup metadata.
func TestKubernetesEnvironmentCarriesVerifiableOwnerAndExpiry(t *testing.T) {
	provider, cluster, _, request, clock := validFixture(t, false)
	observation, err := provider.Acquire(context.Background(), request)
	if err != nil {
		t.Fatalf("acquire: %v", err)
	}
	namespace := cluster.namespaces[observation.EnvironmentID]
	if pairValue(namespace.Meta.Annotations, "featureproof.io/run-id") != request.RunID || pairValue(namespace.Meta.Annotations, "featureproof.io/project-id") != request.ProjectID || pairValue(namespace.Meta.Annotations, "featureproof.io/ttl-seconds") != "1800" {
		t.Fatal("cleanup metadata incomplete")
	}
	expiry, _ := time.Parse(time.RFC3339Nano, pairValue(namespace.Meta.Annotations, "featureproof.io/expires-at"))
	if !expiry.Equal(clock.now.Add(30 * time.Minute)) {
		t.Fatal("expiry mismatch")
	}
}

// TestKubernetesReleaseIsDurableIdempotentAndUIDSafe covers exact UID deletion and confirmed absence.
func TestKubernetesReleaseIsDurableIdempotentAndUIDSafe(t *testing.T) {
	provider, cluster, _, request, _ := validFixture(t, false)
	observation, err := provider.Acquire(context.Background(), request)
	if err != nil {
		t.Fatalf("acquire: %v", err)
	}
	release := releaseRequest(observation, request)
	if err = provider.Release(context.Background(), release); err != nil {
		t.Fatalf("release: %v", err)
	}
	if err = provider.Release(context.Background(), release); !environment.IsCategory(err, environment.CategoryAlreadyReleased) {
		t.Fatalf("idempotent release: %v", err)
	}
	provider, cluster, _, request, _ = validFixture(t, false)
	observation, _ = provider.Acquire(context.Background(), request)
	cluster.mu.Lock()
	namespace := cluster.namespaces[observation.EnvironmentID]
	namespace.Meta.UID = "replacement-uid"
	cluster.namespaces[observation.EnvironmentID] = namespace
	cluster.mu.Unlock()
	if err = provider.Release(context.Background(), releaseRequest(observation, request)); !environment.IsCategory(err, environment.CategoryIntegrity) {
		t.Fatalf("replacement release category: %v", err)
	}
	provider, cluster, _, request, _ = validFixture(t, false)
	observation, _ = provider.Acquire(context.Background(), request)
	cluster.loseAt = "delete"
	if err = provider.Release(context.Background(), releaseRequest(observation, request)); !environment.IsCategory(err, environment.CategoryTransient) {
		t.Fatalf("lost delete category: %v", err)
	}
	if err = provider.Release(context.Background(), releaseRequest(observation, request)); !environment.IsCategory(err, environment.CategoryAlreadyReleased) {
		t.Fatalf("lost delete recovery: %v", err)
	}
}

// TestKubernetesLifecycleDigestTamperingFailsBeforeObserveOrReleaseIO covers stale recovery and cleanup records with well-formed substitute digests.
func TestKubernetesLifecycleDigestTamperingFailsBeforeObserveOrReleaseIO(t *testing.T) {
	provider, cluster, _, request, _ := validFixture(t, false)
	observation, err := provider.Acquire(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	observe := observationRequest(observation, request)
	observe.LifecycleDigest = strings.Repeat("9", 64)
	cluster.mu.Lock()
	namespace := cluster.namespaces[observation.EnvironmentID]
	namespace.Meta.UID = "would-be-observed"
	cluster.namespaces[observation.EnvironmentID] = namespace
	cluster.mu.Unlock()
	if _, err = provider.Observe(context.Background(), observe); !environment.IsCategory(err, environment.CategoryIntegrity) || !strings.Contains(err.Error(), "kubernetes_observe_lifecycle") {
		t.Fatalf("substitute observe lifecycle accepted or reached cluster: %v", err)
	}
	release := releaseRequest(observation, request)
	release.LifecycleDigest = strings.Repeat("8", 64)
	if err = provider.Release(context.Background(), release); !environment.IsCategory(err, environment.CategoryIntegrity) || !strings.Contains(err.Error(), "kubernetes_release_lifecycle") {
		t.Fatalf("substitute release lifecycle accepted or reached cluster: %v", err)
	}
	cluster.mu.Lock()
	deletes := cluster.deleteCalls
	cluster.mu.Unlock()
	if deletes != 0 {
		t.Fatal("tampered lifecycle reached delete")
	}
}

// TestKubernetesFailuresRemainOperational covers safe classification without remote body disclosure.
func TestKubernetesFailuresRemainOperational(t *testing.T) {
	provider, cluster, _, request, _ := validFixture(t, false)
	cluster.failAt = "quota"
	_, err := provider.Acquire(context.Background(), request)
	if !environment.IsCategory(err, environment.CategoryUnavailable) || strings.Contains(err.Error(), "remote-secret-body") {
		t.Fatalf("unsafe operational failure: %v", err)
	}
	for _, code := range []ClientCode{CodeRateLimited, CodeTransient, CodeConflict, CodeForbidden, CodeInvalid, CodeNotFound, CodeAlreadyExists} {
		mapped := provider.mapClient("test", &ClientError{Code: code, Operation: "remote", Cause: fmt.Errorf("remote-secret-body")})
		if strings.Contains(mapped.Error(), "remote-secret-body") {
			t.Fatalf("client body leaked for %s", code)
		}
	}
}

// TestKubernetesProviderSatisfiesNeutralLifecycleContract covers acquire, readiness binding, restore, and durable release through feature 31.
func TestKubernetesProviderSatisfiesNeutralLifecycleContract(t *testing.T) {
	provider, cluster, selection, _, clock := validFixture(t, false)
	cluster.autoReady = true
	registry, err := environment.NewRegistry(map[string]environment.EnvironmentProvider{ProviderID + "@" + ProviderVersion: provider})
	if err != nil {
		t.Fatal(err)
	}
	catalog, err := environment.SealCatalog(environment.Catalog{Version: 1, Items: []environment.Selection{selection}})
	if err != nil {
		t.Fatal(err)
	}
	service, err := environment.NewLifecycleService(environment.Dependencies{Catalog: catalog, Providers: registry, Store: environment.NewMemoryStore(), Network: environment.AllowlistedNetworkAuthorizer{}, Clock: clock, Waiter: clock, Observer: &environment.MemoryObserver{}, Owner: "kubernetes-contract", Limits: environment.Limits{AcquireTimeout: time.Second, ObserveTimeout: time.Second, ReleaseTimeout: time.Second, NetworkTimeout: time.Second, ObserverTimeout: time.Second, LeaseTTL: time.Minute, MaxAttempts: 2, MaxReadiness: 2, MaxElapsed: 10 * time.Second, MaxCumulativeWait: time.Second, InitialBackoff: time.Millisecond}})
	if err != nil {
		t.Fatalf("compose lifecycle: %v", err)
	}
	request := environment.Request{RunID: "run-contract", SelectionAlias: selection.Alias, ProjectID: selection.ProjectID, RepositoryID: selection.RepositoryID, CommitOID: fmt.Sprintf("%040x", 4), IdempotencyKey: "delivery-contract"}
	acquired, err := service.Acquire(context.Background(), request)
	if err != nil {
		t.Fatalf("neutral acquire: %v", err)
	}
	if acquired.Target.ProviderID != ProviderID || acquired.Target.EnvironmentID == "" || environment.VerifyBinding(acquired.Binding, acquired.Target, clock.Now()) != nil {
		t.Fatal("neutral binding invalid")
	}
	if _, err = service.Restore(context.Background(), request); err != nil {
		t.Fatalf("neutral restore: %v", err)
	}
	if err = service.Release(context.Background(), request.RunID); err != nil {
		t.Fatalf("neutral release: %v", err)
	}
	if len(cluster.namespaces) != 0 {
		t.Fatal("neutral release left namespace")
	}
}

// TestKubernetesOperationsAreBoundedCancelableAndRaceSafe covers cancellation, list bounds, and concurrent redelivery.
func TestKubernetesOperationsAreBoundedCancelableAndRaceSafe(t *testing.T) {
	provider, cluster, _, request, _ := validFixture(t, false)
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := provider.Acquire(cancelled, request); !environment.IsCategory(err, environment.CategoryCancelled) {
		t.Fatalf("cancel category: %v", err)
	}
	const contenders = 8
	errorsSeen := make(chan error, contenders)
	var group sync.WaitGroup
	for contender := 0; contender < contenders; contender++ {
		group.Add(1)
		go func() {
			defer group.Done()
			_, err := provider.Acquire(context.Background(), request)
			errorsSeen <- err
		}()
	}
	group.Wait()
	close(errorsSeen)
	for err := range errorsSeen {
		if err != nil {
			t.Fatalf("concurrent convergence: %v", err)
		}
	}
	if len(cluster.namespaces) != 1 || len(cluster.deployments) != 1 {
		t.Fatal("concurrency created duplicate resources")
	}
	observation, _ := provider.Reconcile(context.Background(), request)
	readyCluster(t, cluster, observation, request)
	cluster.mu.Lock()
	for index := 0; index < int(provider.deps.MaxList)+1; index++ {
		cluster.pods = append(cluster.pods, cluster.pods[0])
	}
	cluster.mu.Unlock()
	if _, err := provider.Observe(context.Background(), observationRequest(observation, request)); err == nil {
		t.Fatal("oversized pod list accepted")
	}
}

// observationRequest reconstructs the neutral observe request for provider contract tests.
func observationRequest(value environment.Observation, request environment.AcquisitionRequest) environment.ObservationRequest {
	return environment.ObservationRequest{RunID: request.RunID, RepositoryID: request.RepositoryID, CommitOID: request.CommitOID, EnvironmentID: value.EnvironmentID, LifecycleHandle: value.LifecycleHandle, LifecycleDigest: providerLifecycleDigest(request.Selection.Digest, value.EnvironmentID, value.LifecycleHandle), Selection: request.Selection}
}

// releaseRequest reconstructs the neutral release request for provider contract tests.
func releaseRequest(value environment.Observation, request environment.AcquisitionRequest) environment.ReleaseRequest {
	return environment.ReleaseRequest{RunID: request.RunID, AcquisitionKey: request.IdempotencyKey, IdempotencyKey: fmt.Sprintf("%064x", 7), ProjectID: request.ProjectID, RepositoryID: request.RepositoryID, CommitOID: request.CommitOID, EnvironmentID: value.EnvironmentID, LifecycleHandle: value.LifecycleHandle, LifecycleDigest: providerLifecycleDigest(request.Selection.Digest, value.EnvironmentID, value.LifecycleHandle), Selection: request.Selection}
}
