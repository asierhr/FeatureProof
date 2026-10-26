package kubernetes

import (
	"context"
	"errors"

	corev1 "k8s.io/api/core/v1"
	discoveryv1 "k8s.io/api/discovery/v1"
	networkingv1 "k8s.io/api/networking/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/fields"
	"k8s.io/apimachinery/pkg/labels"
	clientset "k8s.io/client-go/kubernetes"
)

// APIReader adapts an explicitly authenticated client-go interface to the narrow read port.
type APIReader struct{ client clientset.Interface }

// NewAPIReader rejects missing composition and never discovers kubeconfig or environment credentials.
func NewAPIReader(client clientset.Interface) (*APIReader, error) {
	if client == nil {
		return nil, &ConfigError{Field: "client"}
	}
	return &APIReader{client: client}, nil
}

// GetNamespace reads the namespace anchor without requesting unrelated metadata resources.
func (r *APIReader) GetNamespace(ctx context.Context, name string) (Namespace, error) {
	value, err := r.client.CoreV1().Namespaces().Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return Namespace{}, classifyAPI(ctx, err)
	}
	return Namespace{UID: string(value.UID), ResourceVersion: value.ResourceVersion}, nil
}

// GetDeployment reads one exact namespaced Deployment and projects only finite status.
func (r *APIReader) GetDeployment(ctx context.Context, namespace, name string) (Deployment, error) {
	value, err := r.client.AppsV1().Deployments(namespace).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return Deployment{}, classifyAPI(ctx, err)
	}
	conditions := make([]Condition, 0, len(value.Status.Conditions))
	for _, condition := range value.Status.Conditions {
		conditions = append(conditions, Condition{Type: string(condition.Type), Status: string(condition.Status), Reason: condition.Reason})
	}
	desired := int32(0)
	if value.Spec.Replicas != nil {
		desired = *value.Spec.Replicas
	}
	return Deployment{UID: string(value.UID), OwnerUID: controllerUID(value.OwnerReferences), ResourceVersion: value.ResourceVersion, Generation: value.Generation, ObservedGeneration: value.Status.ObservedGeneration, Desired: desired, Updated: value.Status.UpdatedReplicas, Available: value.Status.AvailableReplicas, Ready: value.Status.ReadyReplicas, Conditions: conditions}, nil
}

// ListOwnedPodsPage lists one page using only the precompiled catalog selector.
func (r *APIReader) ListOwnedPodsPage(ctx context.Context, namespace, selector string, request PageRequest) (PodPage, error) {
	value, err := r.client.CoreV1().Pods(namespace).List(ctx, metav1.ListOptions{LabelSelector: selector, Limit: request.Limit, Continue: request.Continue, ResourceVersion: request.ResourceVersion})
	if err != nil {
		return PodPage{}, classifyAPI(ctx, err)
	}
	items := make([]Pod, 0, len(value.Items))
	for index := range value.Items {
		items = append(items, projectPod(&value.Items[index]))
	}
	return PodPage{Items: items, Continue: value.Continue, ResourceVersion: value.ResourceVersion}, nil
}

// GetJob reads one exact Job and discards status messages before crossing the port.
func (r *APIReader) GetJob(ctx context.Context, namespace, name string) (Job, error) {
	value, err := r.client.BatchV1().Jobs(namespace).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return Job{}, classifyAPI(ctx, err)
	}
	conditions := make([]Condition, 0, len(value.Status.Conditions))
	for _, condition := range value.Status.Conditions {
		conditions = append(conditions, Condition{Type: string(condition.Type), Status: string(condition.Status), Reason: condition.Reason})
	}
	completions := int32(0)
	if value.Spec.Completions != nil {
		completions = *value.Spec.Completions
	}
	return Job{UID: string(value.UID), OwnerUID: controllerUID(value.OwnerReferences), ResourceVersion: value.ResourceVersion, Generation: value.Generation, Completions: completions, Active: value.Status.Active, Succeeded: value.Status.Succeeded, Failed: value.Status.Failed, Conditions: conditions}, nil
}

// GetService reads one exact Service without returning cluster IPs, selectors, or annotations.
func (r *APIReader) GetService(ctx context.Context, namespace, name string) (Service, error) {
	value, err := r.client.CoreV1().Services(namespace).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return Service{}, classifyAPI(ctx, err)
	}
	ports := make([]ServicePort, 0, len(value.Spec.Ports))
	for _, port := range value.Spec.Ports {
		ports = append(ports, ServicePort{Name: port.Name, Protocol: string(port.Protocol), Port: port.Port})
	}
	return Service{UID: string(value.UID), OwnerUID: controllerUID(value.OwnerReferences), ResourceVersion: value.ResourceVersion, Type: string(value.Spec.Type), Ports: ports}, nil
}

// ListServiceEndpointSlicesPage lists only EndpointSlices selected by the exact service-name label.
func (r *APIReader) ListServiceEndpointSlicesPage(ctx context.Context, namespace, service string, request PageRequest) (EndpointSlicePage, error) {
	selector := labels.Set{discoveryv1.LabelServiceName: service}.AsSelector().String()
	value, err := r.client.DiscoveryV1().EndpointSlices(namespace).List(ctx, metav1.ListOptions{LabelSelector: selector, Limit: request.Limit, Continue: request.Continue, ResourceVersion: request.ResourceVersion})
	if err != nil {
		return EndpointSlicePage{}, classifyAPI(ctx, err)
	}
	items := make([]EndpointSlice, 0, len(value.Items))
	for index := range value.Items {
		items = append(items, projectEndpointSlice(&value.Items[index]))
	}
	return EndpointSlicePage{Items: items, Continue: value.Continue, ResourceVersion: value.ResourceVersion}, nil
}

// ListScopedEventsPage requests namespace-local events and defensively filters exact involved UIDs.
func (r *APIReader) ListScopedEventsPage(ctx context.Context, namespace string, involvedUIDs []string, request PageRequest) (EventPage, error) {
	selector := ""
	if len(involvedUIDs) == 1 {
		selector = fields.OneTermEqualSelector("involvedObject.uid", involvedUIDs[0]).String()
	}
	value, err := r.client.CoreV1().Events(namespace).List(ctx, metav1.ListOptions{FieldSelector: selector, Limit: request.Limit, Continue: request.Continue, ResourceVersion: request.ResourceVersion})
	if err != nil {
		return EventPage{}, classifyAPI(ctx, err)
	}
	allowed := make(map[string]bool, len(involvedUIDs))
	for _, uid := range involvedUIDs {
		allowed[uid] = true
	}
	items := make([]Event, 0, len(value.Items))
	for index := range value.Items {
		item := &value.Items[index]
		involved := string(item.InvolvedObject.UID)
		if !allowed[involved] {
			continue
		}
		first, last := item.FirstTimestamp.Time, item.LastTimestamp.Time
		if item.EventTime.Time.After(last) {
			last = item.EventTime.Time
		}
		items = append(items, Event{UID: string(item.UID), InvolvedUID: involved, ResourceVersion: item.ResourceVersion, Type: item.Type, Reason: item.Reason, Count: item.Count, FirstObserved: first, LastObserved: last})
	}
	return EventPage{Items: items, Continue: value.Continue, ResourceVersion: value.ResourceVersion}, nil
}

// GetNetworkPolicy reads one exact policy and projects only a single declared TCP relation.
func (r *APIReader) GetNetworkPolicy(ctx context.Context, namespace, name string) (NetworkPolicy, error) {
	value, err := r.client.NetworkingV1().NetworkPolicies(namespace).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return NetworkPolicy{}, classifyAPI(ctx, err)
	}
	projection := NetworkPolicy{UID: string(value.UID), ResourceVersion: value.ResourceVersion, SpecDigest: digest(value.Spec)}
	projectPolicy(value, &projection)
	return projection, nil
}

// projectPod removes names, addresses, messages, images, and unknown container roles from one Pod.
func projectPod(value *corev1.Pod) Pod {
	containers := make([]ContainerStatus, 0, len(value.Status.ContainerStatuses))
	for _, status := range value.Status.ContainerStatuses {
		containers = append(containers, ContainerStatus{Role: status.Name, Ready: status.Ready, Restarts: status.RestartCount})
	}
	ready := false
	for _, condition := range value.Status.Conditions {
		if condition.Type == corev1.PodReady && condition.Status == corev1.ConditionTrue {
			ready = true
		}
	}
	return Pod{UID: string(value.UID), OwnerUID: controllerUID(value.OwnerReferences), ResourceVersion: value.ResourceVersion, Phase: string(value.Status.Phase), Ready: ready, Terminating: value.DeletionTimestamp != nil, Containers: containers}
}

// projectEndpointSlice aggregates conditions without allowing addresses or target names across the port.
func projectEndpointSlice(value *discoveryv1.EndpointSlice) EndpointSlice {
	ready, notReady := 0, 0
	for _, endpoint := range value.Endpoints {
		if endpoint.Conditions.Ready != nil && *endpoint.Conditions.Ready {
			ready++
		} else {
			notReady++
		}
	}
	owner := controllerUID(value.OwnerReferences)
	return EndpointSlice{UID: string(value.UID), OwnerUID: owner, ServiceUID: owner, ResourceVersion: value.ResourceVersion, Ready: ready, NotReady: notReady}
}

// projectPolicy accepts only an explicit single ingress or egress port as declared evidence.
func projectPolicy(value *networkingv1.NetworkPolicy, output *NetworkPolicy) {
	if len(value.Spec.Ingress) == 1 && len(value.Spec.Ingress[0].Ports) == 1 {
		projectPolicyPort(value.Spec.Ingress[0].Ports[0], output)
	}
	if len(value.Spec.Egress) == 1 && len(value.Spec.Egress[0].Ports) == 1 {
		projectPolicyPort(value.Spec.Egress[0].Ports[0], output)
	}
}

// projectPolicyPort copies only an integer TCP port and marks the relation declarative.
func projectPolicyPort(value networkingv1.NetworkPolicyPort, output *NetworkPolicy) {
	if value.Port == nil {
		return
	}
	protocol := corev1.ProtocolTCP
	if value.Protocol != nil {
		protocol = *value.Protocol
	}
	if protocol != corev1.ProtocolTCP || value.Port.Type != 0 {
		return
	}
	output.Protocol, output.Port, output.Declared = string(protocol), int32(value.Port.IntValue()), true
}

// controllerUID returns the sole controller owner or empty when ownership is ambiguous.
func controllerUID(values []metav1.OwnerReference) string {
	result := ""
	for _, value := range values {
		if value.Controller != nil && *value.Controller {
			if result != "" {
				return ""
			}
			result = string(value.UID)
		}
	}
	return result
}

// classifyAPI maps official Kubernetes errors to a content-free operational taxonomy.
func classifyAPI(ctx context.Context, err error) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	category, transient := OutcomeIntegrity, false
	switch {
	case apierrors.IsNotFound(err):
		category = OutcomeAbsent
	case apierrors.IsForbidden(err):
		category = OutcomeAccessDenied
	case apierrors.IsTooManyRequests(err), apierrors.IsServerTimeout(err), apierrors.IsTimeout(err), apierrors.IsServiceUnavailable(err):
		category, transient = OutcomeTransient, true
	}
	cause := error(nil)
	if errors.Is(err, context.Canceled) {
		cause = context.Canceled
	}
	if errors.Is(err, context.DeadlineExceeded) {
		cause = context.DeadlineExceeded
	}
	return &ReaderError{Category: category, Transient: transient, Cause: cause}
}

// compile-time assertions ensure the adapter cannot silently lose any read-only operation.
var _ Reader = (*APIReader)(nil)
