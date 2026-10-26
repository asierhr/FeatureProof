package kubernetes

import (
	"context"
	"errors"
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	discoveryv1 "k8s.io/api/discovery/v1"
	networkingv1 "k8s.io/api/networking/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/intstr"
	clientfake "k8s.io/client-go/kubernetes/fake"
	clienttesting "k8s.io/client-go/testing"
)

// boolValue creates explicit Kubernetes ownership and readiness flags in fixtures.
func boolValue(value bool) *bool { return &value }

// int32Value creates explicit replica and completion pointers in fixtures.
func int32Value(value int32) *int32 { return &value }

// stringValue creates explicit EndpointSlice port names in fixtures.
func stringValue(value string) *string { return &value }

// protocolValue creates explicit EndpointSlice and NetworkPolicy protocols in fixtures.
func protocolValue(value corev1.Protocol) *corev1.Protocol { return &value }

// ownerReference constructs one unambiguous controller relationship.
func ownerReference(uid, name, kind string) metav1.OwnerReference {
	return metav1.OwnerReference{UID: types.UID(uid), Name: name, Kind: kind, Controller: boolValue(true)}
}

// TestAdapterUsesOnlyGetAndListOnAllowlistedResources exercises every adapter operation against client-go's fake boundary.
func TestAdapterUsesOnlyGetAndListOnAllowlistedResources(t *testing.T) {
	namespace := "fp-run"
	client := clientfake.NewSimpleClientset(
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: namespace, UID: "namespace-uid", ResourceVersion: "n1"}},
		&appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Name: "app", Namespace: namespace, UID: "deployment-uid", ResourceVersion: "d1"}, Spec: appsv1.DeploymentSpec{Replicas: int32Value(2)}, Status: appsv1.DeploymentStatus{ObservedGeneration: 1, UpdatedReplicas: 2, AvailableReplicas: 1, ReadyReplicas: 1, Conditions: []appsv1.DeploymentCondition{{Type: appsv1.DeploymentAvailable, Status: corev1.ConditionTrue, Reason: "Available", Message: "secret-canary"}}}},
		&corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "pod", Namespace: namespace, UID: "pod-uid", ResourceVersion: "p1", Labels: map[string]string{"app": "demo"}, OwnerReferences: []metav1.OwnerReference{ownerReference("deployment-uid", "app", "Deployment")}}, Status: corev1.PodStatus{Phase: corev1.PodRunning, Conditions: []corev1.PodCondition{{Type: corev1.PodReady, Status: corev1.ConditionTrue, Message: "secret-canary"}}, ContainerStatuses: []corev1.ContainerStatus{{Name: "application", Ready: true, RestartCount: 3, Image: "secret-canary"}}}},
		&batchv1.Job{ObjectMeta: metav1.ObjectMeta{Name: "migration", Namespace: namespace, UID: "job-uid", ResourceVersion: "j1"}, Spec: batchv1.JobSpec{Completions: int32Value(1)}, Status: batchv1.JobStatus{Succeeded: 1, Conditions: []batchv1.JobCondition{{Type: batchv1.JobComplete, Status: corev1.ConditionTrue, Reason: "Completed", Message: "secret-canary"}}}},
		&corev1.Service{ObjectMeta: metav1.ObjectMeta{Name: "app", Namespace: namespace, UID: "service-uid", ResourceVersion: "s1"}, Spec: corev1.ServiceSpec{Type: corev1.ServiceTypeClusterIP, ClusterIP: "10.0.0.1", Ports: []corev1.ServicePort{{Name: "http", Protocol: corev1.ProtocolTCP, Port: 8080}}}},
		&discoveryv1.EndpointSlice{ObjectMeta: metav1.ObjectMeta{Name: "app-1", Namespace: namespace, UID: "slice-uid", ResourceVersion: "e1", Labels: map[string]string{discoveryv1.LabelServiceName: "app"}, OwnerReferences: []metav1.OwnerReference{ownerReference("service-uid", "app", "Service")}}, AddressType: discoveryv1.AddressTypeIPv4, Ports: []discoveryv1.EndpointPort{{Name: stringValue("http"), Protocol: protocolValue(corev1.ProtocolTCP), Port: int32Value(8080)}}, Endpoints: []discoveryv1.Endpoint{{Addresses: []string{"10.0.0.2"}, Conditions: discoveryv1.EndpointConditions{Ready: boolValue(true)}}}},
		&corev1.Event{ObjectMeta: metav1.ObjectMeta{Name: "event", Namespace: namespace, UID: "event-uid", ResourceVersion: "v1"}, InvolvedObject: corev1.ObjectReference{UID: "deployment-uid"}, Type: "Warning", Reason: "BackOff", Message: "secret-canary", Count: 2, FirstTimestamp: metav1.NewTime(testNow.Add(-time.Minute)), LastTimestamp: metav1.NewTime(testNow)},
		&networkingv1.NetworkPolicy{ObjectMeta: metav1.ObjectMeta{Name: "allow-app", Namespace: namespace, UID: "policy-uid", ResourceVersion: "np1"}, Spec: networkingv1.NetworkPolicySpec{Ingress: []networkingv1.NetworkPolicyIngressRule{{Ports: []networkingv1.NetworkPolicyPort{{Protocol: protocolValue(corev1.ProtocolTCP), Port: intOrString(8080)}}}}}},
	)
	reader, err := NewAPIReader(client)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = reader.GetNamespace(context.Background(), namespace); err != nil {
		t.Fatal(err)
	}
	deployment, err := reader.GetDeployment(context.Background(), namespace, "app")
	if err != nil || deployment.Available != 1 {
		t.Fatalf("deployment = %+v, %v", deployment, err)
	}
	pods, err := reader.ListOwnedPodsPage(context.Background(), namespace, "app=demo", PageRequest{Limit: 10})
	if err != nil || len(pods.Items) != 1 || pods.Items[0].Containers[0].Restarts != 3 {
		t.Fatalf("pods = %+v, %v", pods, err)
	}
	job, err := reader.GetJob(context.Background(), namespace, "migration")
	if err != nil || job.Succeeded != 1 {
		t.Fatalf("job = %+v, %v", job, err)
	}
	service, err := reader.GetService(context.Background(), namespace, "app")
	if err != nil || len(service.Ports) != 1 {
		t.Fatalf("service = %+v, %v", service, err)
	}
	endpoints, err := reader.ListServiceEndpointSlicesPage(context.Background(), namespace, "app", PageRequest{Limit: 10})
	if err != nil || len(endpoints.Items) != 1 || endpoints.Items[0].Ready != 1 {
		t.Fatalf("endpoints = %+v, %v", endpoints, err)
	}
	events, err := reader.ListScopedEventsPage(context.Background(), namespace, []string{"deployment-uid"}, PageRequest{Limit: 10})
	if err != nil || len(events.Items) != 1 || events.Items[0].Reason != "BackOff" {
		t.Fatalf("events = %+v, %v", events, err)
	}
	policy, err := reader.GetNetworkPolicy(context.Background(), namespace, "allow-app")
	if err != nil || !policy.Declared || policy.Port != 8080 {
		t.Fatalf("policy = %+v, %v", policy, err)
	}
	for _, action := range client.Actions() {
		if action.GetVerb() != "get" && action.GetVerb() != "list" {
			t.Fatalf("forbidden verb: %s", action.GetVerb())
		}
		if action.GetResource().Resource == "secrets" {
			t.Fatal("adapter requested Secrets")
		}
		if action.GetNamespace() != "" && action.GetNamespace() != namespace {
			t.Fatalf("cross-namespace action: %#v", action)
		}
	}
	if got := mustJSON([]any{deployment, pods, job, service, endpoints, events, policy}); contains(got, "secret-canary", "10.0.0.1", "10.0.0.2") {
		t.Fatalf("sensitive API fields crossed adapter: %s", got)
	}
}

// intOrString creates an explicit integer NetworkPolicy port.
func intOrString(value int) *intstr.IntOrString { port := intstr.FromInt(value); return &port }

// contains reports whether any forbidden canary occurs in a serialized projection.
func contains(value string, needles ...string) bool {
	for _, needle := range needles {
		if stringsContains(value, needle) {
			return true
		}
	}
	return false
}

// stringsContains isolates simple canary matching for the adapter boundary test.
func stringsContains(value, needle string) bool {
	return len(needle) <= len(value) && find(value, needle) >= 0
}

// find implements deterministic substring search without importing unrelated helpers into production.
func find(value, needle string) int {
	for index := 0; index+len(needle) <= len(value); index++ {
		if value[index:index+len(needle)] == needle {
			return index
		}
	}
	return -1
}

// TestForbiddenIsAccessDeniedAndSecretsAreNeverRequested proves remote text is discarded and no fallback occurs.
func TestForbiddenIsAccessDeniedAndSecretsAreNeverRequested(t *testing.T) {
	client := clientfake.NewSimpleClientset()
	client.PrependReactor("get", "deployments", func(clienttesting.Action) (bool, anyRuntimeObject, error) {
		return true, nil, apierrors.NewForbidden(schema.GroupResource{Group: "apps", Resource: "deployments"}, "secret-canary", errors.New("remote secret-canary"))
	})
	reader, _ := NewAPIReader(client)
	_, err := reader.GetDeployment(context.Background(), "fp-run", "app")
	var typed *ReaderError
	if !errors.As(err, &typed) || typed.Category != OutcomeAccessDenied || find(err.Error(), "secret-canary") >= 0 {
		t.Fatalf("unsafe forbidden mapping: %v", err)
	}
	if len(client.Actions()) != 1 {
		t.Fatalf("unexpected fallback actions: %#v", client.Actions())
	}
}

// anyRuntimeObject aliases the client-go reactor return without weakening production types.
type anyRuntimeObject = runtime.Object

// TestAPIReaderRejectsNilClientAndMapsCancellation covers constructor and cancellation edges.
func TestAPIReaderRejectsNilClientAndMapsCancellation(t *testing.T) {
	if _, err := NewAPIReader(nil); err == nil {
		t.Fatal("nil client accepted")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	mapped := classifyAPI(ctx, errors.New("secret-canary"))
	if !errors.Is(mapped, context.Canceled) {
		t.Fatalf("cancellation lost: %v", mapped)
	}
	transient := classifyAPI(context.Background(), apierrors.NewTooManyRequests("secret-canary", 1))
	var typed *ReaderError
	if !errors.As(transient, &typed) || !typed.Transient || find(transient.Error(), "secret-canary") >= 0 {
		t.Fatalf("transient mapping unsafe: %v", transient)
	}
}
