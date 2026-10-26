package kubernetes

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	discoveryv1 "k8s.io/api/discovery/v1"
	networkingv1 "k8s.io/api/networking/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes/fake"
	"k8s.io/client-go/rest"
)

// TestAPIClientTranslatesClosedTypedResources exercises every productive port operation against the official fake clientset.
func TestAPIClientTranslatesClosedTypedResources(t *testing.T) {
	clientset := fake.NewSimpleClientset()
	client, err := newAPIClient(clientset)
	if err != nil {
		t.Fatalf("new client: %v", err)
	}
	if _, err = newAPIClient(nil); err == nil {
		t.Fatal("nil client accepted")
	}
	if _, err = NewAPIClient(nil); err == nil {
		t.Fatal("nil config accepted")
	}
	if _, err = NewAPIClient(&rest.Config{}); err == nil {
		t.Fatal("empty host accepted")
	}
	if _, err = NewAPIClient(&rest.Config{Host: "https://cluster.invalid"}); err != nil {
		t.Fatalf("explicit config rejected: %v", err)
	}

	ctx := context.Background()
	namespaceMeta := testMeta("namespace-1", "")
	namespace, err := client.CreateNamespace(ctx, Namespace{Meta: namespaceMeta})
	if err != nil {
		t.Fatalf("create namespace: %v", err)
	}
	if _, err = client.CreateNamespace(ctx, Namespace{Meta: namespaceMeta}); !IsClientCode(err, CodeAlreadyExists) {
		t.Fatalf("already exists: %v", err)
	}
	if _, err = client.GetNamespace(ctx, namespace.Meta.Name); err != nil {
		t.Fatalf("get namespace: %v", err)
	}

	quota := ResourceQuota{Meta: testMeta("quota", namespace.Meta.Name), CPUMilli: 400, Memory: 256 << 20, Pods: 2, Services: 1}
	if _, err = client.CreateResourceQuota(ctx, quota); err != nil {
		t.Fatalf("create quota: %v", err)
	}
	if actual, readErr := client.GetResourceQuota(ctx, namespace.Meta.Name, quota.Meta.Name); readErr != nil || !actual.ClosedShape || actual.CPUMilli != quota.CPUMilli || actual.Memory != quota.Memory {
		t.Fatalf("get quota: %#v %v", actual, readErr)
	}
	limit := LimitRange{Meta: testMeta("limits", namespace.Meta.Name), Min: Resources{100, 100, 64 << 20, 64 << 20}, Max: Resources{200, 200, 128 << 20, 128 << 20}, Default: Resources{100, 200, 64 << 20, 128 << 20}}
	if _, err = client.CreateLimitRange(ctx, limit); err != nil {
		t.Fatalf("create limit: %v", err)
	}
	if actual, readErr := client.GetLimitRange(ctx, namespace.Meta.Name, limit.Meta.Name); readErr != nil || !actual.ClosedShape || actual.Default != limit.Default {
		t.Fatalf("get limit: %#v %v", actual, readErr)
	}
	account := ServiceAccount{Meta: testMeta("account", namespace.Meta.Name)}
	if _, err = client.CreateServiceAccount(ctx, account); err != nil {
		t.Fatalf("create account: %v", err)
	}
	if actual, readErr := client.GetServiceAccount(ctx, namespace.Meta.Name, account.Meta.Name); readErr != nil || !actual.ClosedShape || actual.AutomountToken {
		t.Fatalf("get account: %#v %v", actual, readErr)
	}

	for _, policy := range []NetworkPolicy{{Meta: testMeta("deny", namespace.Meta.Name), Direction: "deny-all"}, {Meta: testMeta("ingress", namespace.Meta.Name), Direction: "ingress", Rules: []NetworkRule{{CIDR: "10.0.0.0/24", Port: 80}}}, {Meta: testMeta("egress", namespace.Meta.Name), Direction: "egress", Rules: []NetworkRule{{CIDR: "10.1.0.0/24", Port: 443}}}} {
		if _, err = client.CreateNetworkPolicy(ctx, policy); err != nil {
			t.Fatalf("create network %s: %v", policy.Direction, err)
		}
		if actual, readErr := client.GetNetworkPolicy(ctx, namespace.Meta.Name, policy.Meta.Name); readErr != nil || !actual.ClosedShape || actual.Direction != policy.Direction || !reflect.DeepEqual(actual.Rules, policy.Rules) {
			t.Fatalf("get network: %#v %v", actual, readErr)
		}
	}
	secret := Secret{Meta: testMeta("secret", namespace.Meta.Name), Type: "Opaque", Values: []SecretValue{{Name: "value", Value: []byte("canary")}}}
	if _, err = client.CreateSecret(ctx, secret); err != nil {
		t.Fatalf("create secret: %v", err)
	}
	if actual, readErr := client.VerifySecret(ctx, secret); readErr != nil || len(actual.Values) != 1 || len(actual.Values[0].Value) != 0 {
		t.Fatalf("get secret: %#v %v", actual, readErr)
	}

	deployment := Deployment{Meta: testMeta("featureproof-workload", namespace.Meta.Name), Image: "registry.example/app@sha256:" + strings.Repeat("a", 64), RepositoryID: "repository-1", CommitOID: strings.Repeat("b", 40), Replicas: 1, ContainerPort: 8080, Resources: Resources{100, 200, 64 << 20, 128 << 20}, ServiceAccount: account.Meta.Name, PodSecurity: "restricted", ApplicationSecrets: []string{secret.Meta.Name}, ImagePullSecrets: []string{secret.Meta.Name}}
	deployment.Meta.Labels = append(deployment.Meta.Labels, Pair{Name: "pod-security.kubernetes.io/enforce", Value: "restricted"})
	if _, err = client.CreateDeployment(ctx, deployment); err != nil {
		t.Fatalf("create deployment: %v", err)
	}
	if actual, readErr := client.GetDeployment(ctx, namespace.Meta.Name, deployment.Meta.Name); readErr != nil || !actual.ClosedShape || actual.Image != deployment.Image || actual.Resources != deployment.Resources || !equalStrings(actual.ApplicationSecrets, deployment.ApplicationSecrets) || !equalStrings(actual.ImagePullSecrets, deployment.ImagePullSecrets) {
		t.Fatalf("get deployment: %#v %v", actual, readErr)
	}
	service := Service{Meta: testMeta("featureproof-service", namespace.Meta.Name), Selector: deployment.Meta.Name, Scheme: "http", Port: 80, TargetPort: 8080}
	createdService, err := client.CreateService(ctx, service)
	if err != nil {
		t.Fatalf("create service: %v", err)
	}
	serviceAPI, _ := clientset.CoreV1().Services(namespace.Meta.Name).Get(ctx, service.Meta.Name, metav1.GetOptions{})
	serviceAPI.Spec.ClusterIP = "10.0.0.1"
	serviceAPI.Spec.ClusterIPs = []string{"10.0.0.1"}
	serviceAPI.Spec.IPFamilies = []corev1.IPFamily{corev1.IPv4Protocol}
	singleStack := corev1.IPFamilyPolicySingleStack
	serviceAPI.Spec.IPFamilyPolicy = &singleStack
	internalCluster := corev1.ServiceInternalTrafficPolicyCluster
	serviceAPI.Spec.InternalTrafficPolicy = &internalCluster
	_, _ = clientset.CoreV1().Services(namespace.Meta.Name).Update(ctx, serviceAPI, metav1.UpdateOptions{})
	if actual, readErr := client.GetService(ctx, namespace.Meta.Name, service.Meta.Name); readErr != nil || !actual.ClosedShape || actual.Port != service.Port {
		t.Fatalf("get service: %#v %v", actual, readErr)
	}

	controller := true
	createdDeployment, _ := clientset.AppsV1().Deployments(namespace.Meta.Name).Get(ctx, deployment.Meta.Name, metav1.GetOptions{})
	createdDeployment.UID = types.UID("deployment-uid")
	_, _ = clientset.AppsV1().Deployments(namespace.Meta.Name).Update(ctx, createdDeployment, metav1.UpdateOptions{})
	pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "pod-1", Namespace: namespace.Meta.Name, UID: types.UID("pod-uid"), Labels: createdDeployment.Spec.Template.Labels, Annotations: createdDeployment.Spec.Template.Annotations, OwnerReferences: []metav1.OwnerReference{{APIVersion: "apps/v1", Kind: "Deployment", Name: deployment.Meta.Name, UID: createdDeployment.UID, Controller: &controller}}}, Spec: *createdDeployment.Spec.Template.Spec.DeepCopy(), Status: corev1.PodStatus{Conditions: []corev1.PodCondition{{Type: corev1.PodReady, Status: corev1.ConditionTrue}}, ContainerStatuses: []corev1.ContainerStatus{{Name: "application", Image: deployment.Image, ImageID: "containerd://" + deployment.Image}}}}
	_, _ = clientset.CoreV1().Pods(namespace.Meta.Name).Create(ctx, pod, metav1.CreateOptions{})
	pods, err := client.ListOwnedPods(ctx, namespace.Meta.Name, string(createdDeployment.UID), 2)
	if err != nil || len(pods) != 1 || !pods[0].Ready || !pods[0].ClosedShape {
		t.Fatalf("list pods: %#v %v", pods, err)
	}
	widenedPod := pod.DeepCopy()
	widenedPod.Spec.Containers = append(widenedPod.Spec.Containers, corev1.Container{Name: "foreign-sidecar", Image: deployment.Image})
	if _, err = clientset.CoreV1().Pods(namespace.Meta.Name).Update(ctx, widenedPod, metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
	if widened, listErr := client.ListOwnedPods(ctx, namespace.Meta.Name, string(createdDeployment.UID), 2); listErr != nil || len(widened) != 1 || widened[0].ClosedShape {
		t.Fatalf("widened Pod accepted: %#v %v", widened, listErr)
	}
	widenedPod.Spec = *pod.Spec.DeepCopy()
	if _, err = clientset.CoreV1().Pods(namespace.Meta.Name).Update(ctx, widenedPod, metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}

	createdServiceAPI, _ := clientset.CoreV1().Services(namespace.Meta.Name).Get(ctx, createdService.Meta.Name, metav1.GetOptions{})
	createdServiceAPI.UID = types.UID("service-uid")
	_, _ = clientset.CoreV1().Services(namespace.Meta.Name).Update(ctx, createdServiceAPI, metav1.UpdateOptions{})
	port, ready, portName, protocol := int32(80), true, "http", corev1.ProtocolTCP
	slice := &discoveryv1.EndpointSlice{ObjectMeta: metav1.ObjectMeta{Name: "slice-1", Namespace: namespace.Meta.Name, Labels: map[string]string{discoveryv1.LabelServiceName: service.Meta.Name, discoveryv1.LabelManagedBy: "featureproof-test"}, OwnerReferences: []metav1.OwnerReference{{APIVersion: "v1", Kind: "Service", Name: service.Meta.Name, UID: createdServiceAPI.UID, Controller: &controller}}}, AddressType: discoveryv1.AddressTypeIPv4, Ports: []discoveryv1.EndpointPort{{Name: &portName, Protocol: &protocol, Port: &port}}, Endpoints: []discoveryv1.Endpoint{{Addresses: []string{"10.0.0.2"}, Conditions: discoveryv1.EndpointConditions{Ready: &ready}, TargetRef: &corev1.ObjectReference{Kind: "Pod", Namespace: namespace.Meta.Name, Name: pod.Name, UID: pod.UID}}}}
	_, _ = clientset.DiscoveryV1().EndpointSlices(namespace.Meta.Name).Create(ctx, slice, metav1.CreateOptions{})
	slices, err := client.ListOwnedEndpointSlices(ctx, namespace.Meta.Name, string(createdServiceAPI.UID), 2)
	if err != nil || len(slices) != 1 || !slices[0].ClosedShape || slices[0].PortName != service.Scheme || slices[0].Endpoints[0].TargetKind != "Pod" || !slices[0].Endpoints[0].Ready {
		t.Fatalf("list slices: %#v %v", slices, err)
	}
	if result, deleteErr := client.DeleteNamespace(ctx, namespace.Meta.Name, string(namespace.Meta.UID)); deleteErr != nil || result != DeleteRequested {
		t.Fatalf("delete: %s %v", result, deleteErr)
	}
	if result, deleteErr := client.DeleteNamespace(ctx, namespace.Meta.Name, string(namespace.Meta.UID)); deleteErr != nil || result != DeleteAbsent {
		t.Fatalf("delete absent: %s %v", result, deleteErr)
	}
}

// TestAPIClientRejectsWidenedAuthoritativeReadbacks proves official objects cannot hide fields discarded by the provider DTOs.
func TestAPIClientRejectsWidenedAuthoritativeReadbacks(t *testing.T) {
	meta := testMeta("featureproof-workload", "namespace-1")
	deployment := Deployment{Meta: meta, Image: "registry.example/app@sha256:" + strings.Repeat("a", 64), RepositoryID: "repository-1", CommitOID: strings.Repeat("b", 40), Replicas: 1, ContainerPort: 8080, Resources: Resources{100, 200, 64 << 20, 128 << 20}, ServiceAccount: "featureproof-workload", PodSecurity: "restricted", ReadinessScheme: "http", ReadinessPath: "/ready", InitialDelay: 1, Period: 2, Timeout: 1, FailureThreshold: 3, ClosedShape: true}
	deployment.Meta.Labels = append(deployment.Meta.Labels, Pair{Name: "pod-security.kubernetes.io/enforce", Value: "restricted"})
	for name, mutate := range map[string]func(*appsv1.Deployment){
		"automount": func(value *appsv1.Deployment) {
			value.Spec.Template.Spec.AutomountServiceAccountToken = boolPointer(true)
		},
		"host_network": func(value *appsv1.Deployment) { value.Spec.Template.Spec.HostNetwork = true },
		"host_pid":     func(value *appsv1.Deployment) { value.Spec.Template.Spec.HostPID = true },
		"host_ipc":     func(value *appsv1.Deployment) { value.Spec.Template.Spec.HostIPC = true },
		"host_path": func(value *appsv1.Deployment) {
			value.Spec.Template.Spec.Volumes = []corev1.Volume{{Name: "host", VolumeSource: corev1.VolumeSource{HostPath: &corev1.HostPathVolumeSource{Path: "/"}}}}
		},
		"init_container": func(value *appsv1.Deployment) {
			value.Spec.Template.Spec.InitContainers = []corev1.Container{{Name: "init", Image: deployment.Image}}
		},
		"ephemeral": func(value *appsv1.Deployment) {
			value.Spec.Template.Spec.EphemeralContainers = []corev1.EphemeralContainer{{EphemeralContainerCommon: corev1.EphemeralContainerCommon{Name: "debug", Image: deployment.Image}}}
		},
		"sidecar": func(value *appsv1.Deployment) {
			value.Spec.Template.Spec.Containers = append(value.Spec.Template.Spec.Containers, corev1.Container{Name: "sidecar", Image: deployment.Image})
		},
		"selector":          func(value *appsv1.Deployment) { value.Spec.Selector.MatchLabels["other"] = "workload" },
		"template_metadata": func(value *appsv1.Deployment) { value.Spec.Template.Labels["other"] = "label" },
		"command":           func(value *appsv1.Deployment) { value.Spec.Template.Spec.Containers[0].Command = []string{"sh"} },
		"privileged": func(value *appsv1.Deployment) {
			value.Spec.Template.Spec.Containers[0].SecurityContext.Privileged = boolPointer(true)
		},
	} {
		t.Run("deployment_"+name, func(t *testing.T) {
			object := deploymentToAPI(deployment)
			object.UID, object.ResourceVersion = types.UID("deployment-uid"), "1"
			mutate(object)
			client, _ := newAPIClient(fake.NewSimpleClientset(object))
			actual, err := client.GetDeployment(context.Background(), meta.Namespace, meta.Name)
			if err != nil || actual.ClosedShape {
				t.Fatalf("widened Deployment accepted: %#v %v", actual, err)
			}
		})
	}

	quota := ResourceQuota{Meta: testMeta("quota", "namespace-1"), CPUMilli: 400, Memory: 256 << 20, Pods: 2, Services: 1, ClosedShape: true}
	quotaObject := quotaToAPI(quota)
	quotaObject.Spec.Hard[corev1.ResourceConfigMaps] = *resource.NewQuantity(10, resource.DecimalSI)
	quotaObject.Spec.Scopes = []corev1.ResourceQuotaScope{corev1.ResourceQuotaScopeBestEffort}
	quotaClient, _ := newAPIClient(fake.NewSimpleClientset(quotaObject))
	if actual, err := quotaClient.GetResourceQuota(context.Background(), quota.Meta.Namespace, quota.Meta.Name); err != nil || actual.ClosedShape {
		t.Fatalf("widened quota accepted: %#v %v", actual, err)
	}

	limit := LimitRange{Meta: testMeta("limits", "namespace-1"), Min: Resources{100, 100, 64 << 20, 64 << 20}, Max: Resources{200, 200, 128 << 20, 128 << 20}, Default: Resources{100, 200, 64 << 20, 128 << 20}, ClosedShape: true}
	for name, mutate := range map[string]func(*corev1.LimitRange){
		"type": func(value *corev1.LimitRange) { value.Spec.Limits[0].Type = corev1.LimitTypePod },
		"extra_list": func(value *corev1.LimitRange) {
			value.Spec.Limits = append(value.Spec.Limits, value.Spec.Limits[0])
		},
		"extra_resource": func(value *corev1.LimitRange) {
			value.Spec.Limits[0].Min[corev1.ResourceEphemeralStorage] = *resource.NewQuantity(1, resource.BinarySI)
		},
		"ratio": func(value *corev1.LimitRange) {
			value.Spec.Limits[0].MaxLimitRequestRatio = corev1.ResourceList{corev1.ResourceCPU: *resource.NewQuantity(2, resource.DecimalSI)}
		},
	} {
		t.Run("limit_"+name, func(t *testing.T) {
			object := limitToAPI(limit)
			mutate(object)
			client, _ := newAPIClient(fake.NewSimpleClientset(object))
			actual, err := client.GetLimitRange(context.Background(), limit.Meta.Namespace, limit.Meta.Name)
			if err != nil || actual.ClosedShape {
				t.Fatalf("widened LimitRange accepted: %#v %v", actual, err)
			}
		})
	}

	policy := NetworkPolicy{Meta: testMeta("deny", "namespace-1"), Direction: "deny-all", ClosedShape: true}
	policyObject := networkToAPI(policy)
	policyObject.Spec.PodSelector.MatchLabels = map[string]string{"other": "pod"}
	policyObject.Spec.Ingress = []networkingv1.NetworkPolicyIngressRule{{}}
	policyClient, _ := newAPIClient(fake.NewSimpleClientset(policyObject))
	if actual, err := policyClient.GetNetworkPolicy(context.Background(), policy.Meta.Namespace, policy.Meta.Name); err != nil || actual.ClosedShape {
		t.Fatalf("widened network policy accepted: %#v %v", actual, err)
	}

	service := Service{Meta: testMeta("service", "namespace-1"), Selector: deployment.Meta.Name, Scheme: "http", Port: 80, TargetPort: 8080, ClosedShape: true}
	for name, mutate := range map[string]func(*corev1.Service){
		"external": func(value *corev1.Service) {
			value.Spec.Type = corev1.ServiceTypeLoadBalancer
			value.Spec.ExternalIPs = []string{"203.0.113.1"}
			value.Spec.Selector["other"] = "workload"
		},
		"internal_local": func(value *corev1.Service) {
			local := corev1.ServiceInternalTrafficPolicyLocal
			value.Spec.InternalTrafficPolicy = &local
		},
		"session_config": func(value *corev1.Service) {
			value.Spec.SessionAffinityConfig = &corev1.SessionAffinityConfig{ClientIP: &corev1.ClientIPConfig{TimeoutSeconds: int32Pointer(30)}}
		},
		"load_balancer_class": func(value *corev1.Service) {
			class := "example.invalid/class"
			value.Spec.LoadBalancerClass = &class
		},
	} {
		t.Run("service_"+name, func(t *testing.T) {
			object := serviceToAPI(service)
			object.Spec.ClusterIP, object.Spec.ClusterIPs = "10.0.0.1", []string{"10.0.0.1"}
			object.Spec.IPFamilies = []corev1.IPFamily{corev1.IPv4Protocol}
			singleStack, internalCluster := corev1.IPFamilyPolicySingleStack, corev1.ServiceInternalTrafficPolicyCluster
			object.Spec.IPFamilyPolicy, object.Spec.InternalTrafficPolicy = &singleStack, &internalCluster
			mutate(object)
			client, _ := newAPIClient(fake.NewSimpleClientset(object))
			actual, err := client.GetService(context.Background(), service.Meta.Namespace, service.Meta.Name)
			if err != nil || actual.ClosedShape {
				t.Fatalf("widened Service accepted: %#v %v", actual, err)
			}
		})
	}
}

// TestAPIClientPreservesAndRejectsEndpointSliceAuthorityDrift covers port identity and the complete allowed Pod target reference.
func TestAPIClientPreservesAndRejectsEndpointSliceAuthorityDrift(t *testing.T) {
	controller, ready, port, protocol := true, true, int32(80), corev1.ProtocolTCP
	for name, mutate := range map[string]func(*discoveryv1.EndpointSlice){
		"port_name":               func(value *discoveryv1.EndpointSlice) { changed := "other"; value.Ports[0].Name = &changed },
		"target_api_version":      func(value *discoveryv1.EndpointSlice) { value.Endpoints[0].TargetRef.APIVersion = "v1" },
		"target_resource_version": func(value *discoveryv1.EndpointSlice) { value.Endpoints[0].TargetRef.ResourceVersion = "2" },
		"target_field_path":       func(value *discoveryv1.EndpointSlice) { value.Endpoints[0].TargetRef.FieldPath = "spec.nodeName" },
	} {
		t.Run(name, func(t *testing.T) {
			portName := "http"
			object := &discoveryv1.EndpointSlice{ObjectMeta: metav1.ObjectMeta{Name: "slice", Namespace: "namespace-1", UID: types.UID("slice-uid"), ResourceVersion: "1", Labels: map[string]string{discoveryv1.LabelServiceName: "featureproof-service", discoveryv1.LabelManagedBy: "controller"}, OwnerReferences: []metav1.OwnerReference{{APIVersion: "v1", Kind: "Service", Name: "featureproof-service", UID: types.UID("service-uid"), Controller: &controller}}}, AddressType: discoveryv1.AddressTypeIPv4, Ports: []discoveryv1.EndpointPort{{Name: &portName, Protocol: &protocol, Port: &port}}, Endpoints: []discoveryv1.Endpoint{{Addresses: []string{"10.0.0.2"}, Conditions: discoveryv1.EndpointConditions{Ready: &ready}, TargetRef: &corev1.ObjectReference{Kind: "Pod", Namespace: "namespace-1", Name: "pod", UID: types.UID("pod-uid")}}}}
			mutate(object)
			client, _ := newAPIClient(fake.NewSimpleClientset(object))
			actual, err := client.ListOwnedEndpointSlices(context.Background(), "namespace-1", "service-uid", 2)
			if err != nil || len(actual) != 1 {
				t.Fatalf("endpoint readback failed: %#v %v", actual, err)
			}
			if name == "port_name" {
				if actual[0].PortName != "other" {
					t.Fatalf("port name authority discarded: %#v", actual[0])
				}
			} else if actual[0].ClosedShape {
				t.Fatalf("widened target reference accepted: %#v", actual[0])
			}
		})
	}
}

// TestAPIClientClassifiesOfficialErrors covers the stable content-free transport taxonomy.
func TestAPIClientClassifiesOfficialErrors(t *testing.T) {
	resource := schema.GroupResource{Group: "apps", Resource: "deployments"}
	cases := []struct {
		err  error
		code ClientCode
	}{{apierrors.NewNotFound(resource, "x"), CodeNotFound}, {apierrors.NewAlreadyExists(resource, "x"), CodeAlreadyExists}, {apierrors.NewConflict(resource, "x", errors.New("remote")), CodeConflict}, {apierrors.NewForbidden(resource, "x", errors.New("remote")), CodeForbidden}, {apierrors.NewBadRequest("remote"), CodeInvalid}, {apierrors.NewTooManyRequests("remote", 1), CodeRateLimited}, {apierrors.NewServiceUnavailable("remote"), CodeTransient}}
	for _, scenario := range cases {
		classified := classifyAPI(context.Background(), "test", scenario.err)
		if !IsClientCode(classified, scenario.code) || strings.Contains(classified.Error(), "remote") {
			t.Fatalf("classification %s: %v", scenario.code, classified)
		}
	}
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	if err := classifyAPI(cancelled, "test", errors.New("remote")); !errors.Is(err, context.Canceled) {
		t.Fatalf("context classification: %v", err)
	}
}

// testMeta builds provider-owned metadata for productive client translation tests.
func testMeta(name, namespace string) ObjectMeta {
	return ObjectMeta{Name: name, Namespace: namespace, SpecDigest: strings.Repeat("c", 64), Labels: []Pair{{Name: "app.kubernetes.io/managed-by", Value: "featureproof"}}, Annotations: []Pair{{Name: "featureproof.io/repository-id", Value: "repository-1"}, {Name: "featureproof.io/commit-oid", Value: strings.Repeat("b", 40)}}}
}

// int32Pointer constructs API scalar pointers for hostile Service readback tests.
func int32Pointer(value int32) *int32 { return &value }
