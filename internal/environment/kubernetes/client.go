package kubernetes

import (
	"context"
	"crypto/subtle"
	"errors"
	"net"
	"reflect"
	"strings"

	"github.com/asierhr/featureProof/internal/environment"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	discoveryv1 "k8s.io/api/discovery/v1"
	networkingv1 "k8s.io/api/networking/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
)

// APIClient translates the closed ClusterClient port to the official typed Kubernetes clientset.
type APIClient struct{ client kubernetes.Interface }

// NewAPIClient creates a productive client only from explicit composition-owned REST configuration.
func NewAPIClient(config *rest.Config) (*APIClient, error) {
	if config == nil || config.Host == "" {
		return nil, environmentError(environment.CategoryInvalid, "kubernetes_client_config", nil)
	}
	copyConfig := rest.CopyConfig(config)
	client, err := kubernetes.NewForConfig(copyConfig)
	if err != nil {
		return nil, environmentError(environment.CategoryInvalid, "kubernetes_client_config", err)
	}
	return &APIClient{client: client}, nil
}

// newAPIClient wraps an injected official clientset for hermetic adapter contract tests.
func newAPIClient(client kubernetes.Interface) (*APIClient, error) {
	if client == nil {
		return nil, environmentError(environment.CategoryInvalid, "kubernetes_client", nil)
	}
	return &APIClient{client: client}, nil
}

// CreateNamespace creates the lifecycle root without updates or apply semantics.
func (c *APIClient) CreateNamespace(ctx context.Context, value Namespace) (Namespace, error) {
	created, err := c.client.CoreV1().Namespaces().Create(ctx, &corev1.Namespace{ObjectMeta: toMeta(value.Meta)}, metav1.CreateOptions{})
	if err != nil {
		return Namespace{}, classifyAPI(ctx, "create_namespace", err)
	}
	return Namespace{Meta: fromMeta(created.ObjectMeta)}, nil
}

// GetNamespace reads one exact lifecycle root by its derived name.
func (c *APIClient) GetNamespace(ctx context.Context, name string) (Namespace, error) {
	value, err := c.client.CoreV1().Namespaces().Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return Namespace{}, classifyAPI(ctx, "get_namespace", err)
	}
	return Namespace{Meta: fromMeta(value.ObjectMeta)}, nil
}

// DeleteNamespace deletes one namespace only when its server UID still matches the confirmed handle.
func (c *APIClient) DeleteNamespace(ctx context.Context, name, uid string) (DeleteResult, error) {
	precondition := types.UID(uid)
	err := c.client.CoreV1().Namespaces().Delete(ctx, name, metav1.DeleteOptions{Preconditions: &metav1.Preconditions{UID: &precondition}})
	if apierrors.IsNotFound(err) {
		return DeleteAbsent, nil
	}
	if err != nil {
		return "", classifyAPI(ctx, "delete_namespace", err)
	}
	return DeleteRequested, nil
}

// CreateResourceQuota creates aggregate CPU, memory, pod, and Service ceilings.
func (c *APIClient) CreateResourceQuota(ctx context.Context, value ResourceQuota) (ResourceQuota, error) {
	object := quotaToAPI(value)
	created, err := c.client.CoreV1().ResourceQuotas(value.Meta.Namespace).Create(ctx, object, metav1.CreateOptions{})
	if err != nil {
		return ResourceQuota{}, classifyAPI(ctx, "create_quota", err)
	}
	return quotaFromAPI(created), nil
}

// GetResourceQuota reads one exact aggregate ceiling.
func (c *APIClient) GetResourceQuota(ctx context.Context, namespace, name string) (ResourceQuota, error) {
	value, err := c.client.CoreV1().ResourceQuotas(namespace).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return ResourceQuota{}, classifyAPI(ctx, "get_quota", err)
	}
	return quotaFromAPI(value), nil
}

// CreateLimitRange creates exact per-container min, max, and default resource policy.
func (c *APIClient) CreateLimitRange(ctx context.Context, value LimitRange) (LimitRange, error) {
	created, err := c.client.CoreV1().LimitRanges(value.Meta.Namespace).Create(ctx, limitToAPI(value), metav1.CreateOptions{})
	if err != nil {
		return LimitRange{}, classifyAPI(ctx, "create_limit", err)
	}
	return limitFromAPI(created), nil
}

// limitToAPI constructs the sole complete per-container policy form accepted by the provider.
func limitToAPI(value LimitRange) *corev1.LimitRange {
	item := corev1.LimitRangeItem{Type: corev1.LimitTypeContainer, Min: resourceList(value.Min.CPURequestMilli, value.Min.MemoryRequest), Max: resourceList(value.Max.CPULimitMilli, value.Max.MemoryLimit), DefaultRequest: resourceList(value.Default.CPURequestMilli, value.Default.MemoryRequest), Default: resourceList(value.Default.CPULimitMilli, value.Default.MemoryLimit)}
	return &corev1.LimitRange{ObjectMeta: toMeta(value.Meta), Spec: corev1.LimitRangeSpec{Limits: []corev1.LimitRangeItem{item}}}
}

// GetLimitRange reads one exact per-container policy.
func (c *APIClient) GetLimitRange(ctx context.Context, namespace, name string) (LimitRange, error) {
	value, err := c.client.CoreV1().LimitRanges(namespace).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return LimitRange{}, classifyAPI(ctx, "get_limit", err)
	}
	return limitFromAPI(value), nil
}

// CreateServiceAccount creates the provider-owned tokenless workload identity.
func (c *APIClient) CreateServiceAccount(ctx context.Context, value ServiceAccount) (ServiceAccount, error) {
	automount := value.AutomountToken
	created, err := c.client.CoreV1().ServiceAccounts(value.Meta.Namespace).Create(ctx, &corev1.ServiceAccount{ObjectMeta: toMeta(value.Meta), AutomountServiceAccountToken: &automount}, metav1.CreateOptions{})
	if err != nil {
		return ServiceAccount{}, classifyAPI(ctx, "create_service_account", err)
	}
	return accountFromAPI(created), nil
}

// GetServiceAccount reads the exact workload identity without listing namespace principals.
func (c *APIClient) GetServiceAccount(ctx context.Context, namespace, name string) (ServiceAccount, error) {
	value, err := c.client.CoreV1().ServiceAccounts(namespace).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return ServiceAccount{}, classifyAPI(ctx, "get_service_account", err)
	}
	return accountFromAPI(value), nil
}

// CreateNetworkPolicy creates one default-deny or CIDR/port allowlist policy.
func (c *APIClient) CreateNetworkPolicy(ctx context.Context, value NetworkPolicy) (NetworkPolicy, error) {
	created, err := c.client.NetworkingV1().NetworkPolicies(value.Meta.Namespace).Create(ctx, networkToAPI(value), metav1.CreateOptions{})
	if err != nil {
		return NetworkPolicy{}, classifyAPI(ctx, "create_network_policy", err)
	}
	return networkFromAPI(created), nil
}

// GetNetworkPolicy reads one exact policy by its provider-owned name.
func (c *APIClient) GetNetworkPolicy(ctx context.Context, namespace, name string) (NetworkPolicy, error) {
	value, err := c.client.NetworkingV1().NetworkPolicies(namespace).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return NetworkPolicy{}, classifyAPI(ctx, "get_network_policy", err)
	}
	return networkFromAPI(value), nil
}

// CreateSecret sends attempt-owned values directly to Kubernetes.
func (c *APIClient) CreateSecret(ctx context.Context, value Secret) (Secret, error) {
	data := make(map[string][]byte, len(value.Values))
	for _, item := range value.Values {
		data[item.Name] = append([]byte(nil), item.Value...)
	}
	created, err := c.client.CoreV1().Secrets(value.Meta.Namespace).Create(ctx, &corev1.Secret{ObjectMeta: toMeta(value.Meta), Type: corev1.SecretType(value.Type), Data: data}, metav1.CreateOptions{})
	for key := range data {
		clear(data[key])
	}
	if err != nil {
		return Secret{}, classifyAPI(ctx, "create_secret", err)
	}
	return secretFromAPI(created), nil
}

// VerifySecret compares sensitive bytes only inside transport and returns provider-owned shape without values.
func (c *APIClient) VerifySecret(ctx context.Context, expected Secret) (Secret, error) {
	value, err := c.client.CoreV1().Secrets(expected.Meta.Namespace).Get(ctx, expected.Meta.Name, metav1.GetOptions{})
	if err != nil {
		return Secret{}, classifyAPI(ctx, "verify_secret", err)
	}
	if len(value.Data) != len(expected.Values) {
		return Secret{}, &ClientError{Code: CodeConflict, Operation: "verify_secret"}
	}
	for _, item := range expected.Values {
		observed, present := value.Data[item.Name]
		if !present || subtle.ConstantTimeCompare(observed, item.Value) != 1 {
			return Secret{}, &ClientError{Code: CodeConflict, Operation: "verify_secret"}
		}
	}
	return secretFromAPI(value), nil
}

// CreateDeployment creates the immutable workload with restrictive security and explicit resources.
func (c *APIClient) CreateDeployment(ctx context.Context, value Deployment) (Deployment, error) {
	object := deploymentToAPI(value)
	created, err := c.client.AppsV1().Deployments(value.Meta.Namespace).Create(ctx, object, metav1.CreateOptions{})
	if err != nil {
		return Deployment{}, classifyAPI(ctx, "create_deployment", err)
	}
	return deploymentFromAPI(created), nil
}

// GetDeployment reads desired workload and rollout status for readiness attestation.
func (c *APIClient) GetDeployment(ctx context.Context, namespace, name string) (Deployment, error) {
	value, err := c.client.AppsV1().Deployments(namespace).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return Deployment{}, classifyAPI(ctx, "get_deployment", err)
	}
	return deploymentFromAPI(value), nil
}

// CreateService creates one ClusterIP-only internal endpoint.
func (c *APIClient) CreateService(ctx context.Context, value Service) (Service, error) {
	object := serviceToAPI(value)
	created, err := c.client.CoreV1().Services(value.Meta.Namespace).Create(ctx, object, metav1.CreateOptions{})
	if err != nil {
		return Service{}, classifyAPI(ctx, "create_service", err)
	}
	return serviceFromAPI(created), nil
}

// GetService reads one exact internal endpoint without listing Services.
func (c *APIClient) GetService(ctx context.Context, namespace, name string) (Service, error) {
	value, err := c.client.CoreV1().Services(namespace).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return Service{}, classifyAPI(ctx, "get_service", err)
	}
	return serviceFromAPI(value), nil
}

// ListOwnedPods returns a bounded workload observation and filters exact Deployment ownership.
func (c *APIClient) ListOwnedPods(ctx context.Context, namespace, _ string, limit int64) ([]Pod, error) {
	list, err := c.client.CoreV1().Pods(namespace).List(ctx, metav1.ListOptions{LabelSelector: "featureproof.io/workload=featureproof-workload", Limit: limit + 1})
	if err != nil {
		return nil, classifyAPI(ctx, "list_pods", err)
	}
	if int64(len(list.Items)) > limit || list.Continue != "" {
		return nil, &ClientError{Code: CodeInvalid, Operation: "list_pods_limit"}
	}
	result := make([]Pod, 0, len(list.Items))
	for itemIndex := range list.Items {
		pod := podFromAPI(&list.Items[itemIndex])
		result = append(result, pod)
	}
	return result, nil
}

// ListOwnedEndpointSlices returns bounded Service discovery tied to the exact Service UID.
func (c *APIClient) ListOwnedEndpointSlices(ctx context.Context, namespace, _ string, limit int64) ([]EndpointSlice, error) {
	list, err := c.client.DiscoveryV1().EndpointSlices(namespace).List(ctx, metav1.ListOptions{LabelSelector: discoveryv1.LabelServiceName + "=featureproof-service", Limit: limit + 1})
	if err != nil {
		return nil, classifyAPI(ctx, "list_endpoint_slices", err)
	}
	if int64(len(list.Items)) > limit || list.Continue != "" {
		return nil, &ClientError{Code: CodeInvalid, Operation: "list_endpoint_slices_limit"}
	}
	result := make([]EndpointSlice, 0, len(list.Items))
	for itemIndex := range list.Items {
		item := &list.Items[itemIndex]
		result = append(result, endpointSliceFromAPI(item))
	}
	return result, nil
}

// toMeta translates only provider-owned metadata and the desired-spec digest.
func toMeta(value ObjectMeta) metav1.ObjectMeta {
	annotations := pairsToMap(value.Annotations)
	annotations["featureproof.io/spec-digest"] = value.SpecDigest
	return metav1.ObjectMeta{Name: value.Name, Namespace: value.Namespace, Labels: pairsToMap(value.Labels), Annotations: annotations}
}

// fromMeta projects server identity plus exact provider metadata for fail-closed comparison.
func fromMeta(value metav1.ObjectMeta) ObjectMeta {
	annotations := mapToPairs(value.Annotations, "featureproof.io/spec-digest")
	return ObjectMeta{Name: value.Name, Namespace: value.Namespace, UID: string(value.UID), ResourceVersion: value.ResourceVersion, SpecDigest: value.Annotations["featureproof.io/spec-digest"], Labels: mapToPairs(value.Labels, ""), Annotations: annotations}
}

// pairsToMap converts already sealed unique provider metadata for official API DTOs.
func pairsToMap(values []Pair) map[string]string {
	result := make(map[string]string, len(values))
	for _, value := range values {
		result[value.Name] = value.Value
	}
	return result
}

// mapToPairs converts official DTO maps back to deterministic provider-owned projections.
func mapToPairs(values map[string]string, omit string) []Pair {
	result := make([]Pair, 0, len(values))
	for name, value := range values {
		if name != omit {
			result = append(result, Pair{Name: name, Value: value})
		}
	}
	return sortedPairs(result)
}

// resourceList builds CPU and memory quantities without parsing untrusted strings.
func resourceList(cpuMilli, memory int64) corev1.ResourceList {
	return corev1.ResourceList{corev1.ResourceCPU: *resource.NewMilliQuantity(cpuMilli, resource.DecimalSI), corev1.ResourceMemory: *resource.NewQuantity(memory, resource.BinarySI)}
}

// quotaToAPI constructs the only aggregate quota vocabulary authorized by the provider.
func quotaToAPI(value ResourceQuota) *corev1.ResourceQuota {
	return &corev1.ResourceQuota{ObjectMeta: toMeta(value.Meta), Spec: corev1.ResourceQuotaSpec{Hard: corev1.ResourceList{corev1.ResourceRequestsCPU: *resource.NewMilliQuantity(value.CPUMilli, resource.DecimalSI), corev1.ResourceRequestsMemory: *resource.NewQuantity(value.Memory, resource.BinarySI), corev1.ResourcePods: *resource.NewQuantity(value.Pods, resource.DecimalSI), corev1.ResourceServices: *resource.NewQuantity(value.Services, resource.DecimalSI)}}}
}

// quotaFromAPI translates exact aggregate resource ceilings.
func quotaFromAPI(value *corev1.ResourceQuota) ResourceQuota {
	result := ResourceQuota{Meta: fromMeta(value.ObjectMeta), CPUMilli: quantityMilliValue(value.Spec.Hard, corev1.ResourceRequestsCPU), Memory: quantityValue(value.Spec.Hard, corev1.ResourceRequestsMemory), Pods: quantityValue(value.Spec.Hard, corev1.ResourcePods), Services: quantityValue(value.Spec.Hard, corev1.ResourceServices)}
	result.ClosedShape = reflect.DeepEqual(value.Spec, quotaToAPI(result).Spec)
	return result
}

// limitFromAPI translates the single supported container limit policy.
func limitFromAPI(value *corev1.LimitRange) LimitRange {
	result := LimitRange{Meta: fromMeta(value.ObjectMeta)}
	if len(value.Spec.Limits) == 1 {
		item := value.Spec.Limits[0]
		result.Min = resourcesFromLists(item.Min, item.Min)
		result.Max = resourcesFromLists(item.Max, item.Max)
		result.Default = resourcesFromLists(item.DefaultRequest, item.Default)
	}
	result.ClosedShape = reflect.DeepEqual(value.Spec, limitToAPI(result).Spec)
	return result
}

// resourcesFromLists translates request and limit resource lists.
func resourcesFromLists(requests, limits corev1.ResourceList) Resources {
	return Resources{CPURequestMilli: requests.Cpu().MilliValue(), CPULimitMilli: limits.Cpu().MilliValue(), MemoryRequest: requests.Memory().Value(), MemoryLimit: limits.Memory().Value()}
}

// quantityValue safely extracts one integer quota dimension.
func quantityValue(values corev1.ResourceList, name corev1.ResourceName) int64 {
	value := values[name]
	return value.Value()
}

// quantityMilliValue safely extracts one milli-unit quota dimension.
func quantityMilliValue(values corev1.ResourceList, name corev1.ResourceName) int64 {
	value := values[name]
	return value.MilliValue()
}

// accountFromAPI translates token automount without treating nil as an allowed default.
func accountFromAPI(value *corev1.ServiceAccount) ServiceAccount {
	automount := true
	if value.AutomountServiceAccountToken != nil {
		automount = *value.AutomountServiceAccountToken
	}
	return ServiceAccount{Meta: fromMeta(value.ObjectMeta), AutomountToken: automount, ClosedShape: value.AutomountServiceAccountToken != nil && !automount && len(value.Secrets) == 0 && len(value.ImagePullSecrets) == 0}
}

// networkToAPI translates the closed default-deny and CIDR rule vocabulary.
func networkToAPI(value NetworkPolicy) *networkingv1.NetworkPolicy {
	object := &networkingv1.NetworkPolicy{ObjectMeta: toMeta(value.Meta), Spec: networkingv1.NetworkPolicySpec{PodSelector: metav1.LabelSelector{}}}
	if value.Direction == "deny-all" || value.Direction == "ingress" {
		object.Spec.PolicyTypes = append(object.Spec.PolicyTypes, networkingv1.PolicyTypeIngress)
	}
	if value.Direction == "deny-all" || value.Direction == "egress" {
		object.Spec.PolicyTypes = append(object.Spec.PolicyTypes, networkingv1.PolicyTypeEgress)
	}
	for _, rule := range value.Rules {
		protocol := corev1.ProtocolTCP
		port := intstrFromInt(int32(rule.Port))
		peer := networkingv1.NetworkPolicyPeer{IPBlock: &networkingv1.IPBlock{CIDR: rule.CIDR}}
		if value.Direction == "ingress" {
			object.Spec.Ingress = append(object.Spec.Ingress, networkingv1.NetworkPolicyIngressRule{From: []networkingv1.NetworkPolicyPeer{peer}, Ports: []networkingv1.NetworkPolicyPort{{Protocol: &protocol, Port: &port}}})
		} else {
			object.Spec.Egress = append(object.Spec.Egress, networkingv1.NetworkPolicyEgressRule{To: []networkingv1.NetworkPolicyPeer{peer}, Ports: []networkingv1.NetworkPolicyPort{{Protocol: &protocol, Port: &port}}})
		}
	}
	return object
}

// networkFromAPI translates only the closed policy forms emitted by this adapter.
func networkFromAPI(value *networkingv1.NetworkPolicy) NetworkPolicy {
	result := NetworkPolicy{Meta: fromMeta(value.ObjectMeta)}
	if len(value.Spec.PolicyTypes) == 2 {
		result.Direction = "deny-all"
	} else if len(value.Spec.PolicyTypes) == 1 && value.Spec.PolicyTypes[0] == networkingv1.PolicyTypeIngress {
		result.Direction = "ingress"
		for _, rule := range value.Spec.Ingress {
			if len(rule.From) == 1 && rule.From[0].IPBlock != nil && len(rule.Ports) == 1 && rule.Ports[0].Port != nil {
				result.Rules = append(result.Rules, NetworkRule{CIDR: rule.From[0].IPBlock.CIDR, Port: rule.Ports[0].Port.IntValue()})
			}
		}
	} else if len(value.Spec.PolicyTypes) == 1 {
		result.Direction = "egress"
		for _, rule := range value.Spec.Egress {
			if len(rule.To) == 1 && rule.To[0].IPBlock != nil && len(rule.Ports) == 1 && rule.Ports[0].Port != nil {
				result.Rules = append(result.Rules, NetworkRule{CIDR: rule.To[0].IPBlock.CIDR, Port: rule.Ports[0].Port.IntValue()})
			}
		}
	}
	result.Rules = sortedRules(result.Rules)
	result.ClosedShape = reflect.DeepEqual(value.Spec, networkToAPI(result).Spec)
	return result
}

// secretFromAPI returns key shape only and deliberately discards all sensitive bytes.
func secretFromAPI(value *corev1.Secret) Secret {
	keys := make([]SecretValue, 0, len(value.Data))
	for key := range value.Data {
		keys = append(keys, SecretValue{Name: key})
	}
	return Secret{Meta: fromMeta(value.ObjectMeta), Type: string(value.Type), Values: keys, ClosedShape: value.Immutable == nil || !*value.Immutable}
}

// deploymentToAPI constructs the complete explicit Deployment and Pod template accepted by readback.
func deploymentToAPI(value Deployment) *appsv1.Deployment {
	labels := pairsToMap(value.Meta.Labels)
	labels["featureproof.io/workload"] = value.Meta.Name
	allowEscalation, readOnly, runAsNonRoot, enableServiceLinks := false, true, true, false
	replicas, terminationGrace, revisionHistory, progressDeadline := value.Replicas, int64(30), int32(0), int32(600)
	maxUnavailable, maxSurge := intstr.FromInt32(1), intstr.FromInt32(0)
	return &appsv1.Deployment{ObjectMeta: toMeta(value.Meta), Spec: appsv1.DeploymentSpec{Replicas: &replicas, Selector: &metav1.LabelSelector{MatchLabels: map[string]string{"featureproof.io/workload": value.Meta.Name}}, Strategy: appsv1.DeploymentStrategy{Type: appsv1.RollingUpdateDeploymentStrategyType, RollingUpdate: &appsv1.RollingUpdateDeployment{MaxUnavailable: &maxUnavailable, MaxSurge: &maxSurge}}, RevisionHistoryLimit: &revisionHistory, ProgressDeadlineSeconds: &progressDeadline, Template: corev1.PodTemplateSpec{ObjectMeta: metav1.ObjectMeta{Labels: labels, Annotations: pairsToMap(value.Meta.Annotations)}, Spec: corev1.PodSpec{ServiceAccountName: value.ServiceAccount, DeprecatedServiceAccount: value.ServiceAccount, AutomountServiceAccountToken: boolPointer(false), ImagePullSecrets: localObjectReferences(value.ImagePullSecrets), RestartPolicy: corev1.RestartPolicyAlways, TerminationGracePeriodSeconds: &terminationGrace, DNSPolicy: corev1.DNSClusterFirst, SchedulerName: corev1.DefaultSchedulerName, EnableServiceLinks: &enableServiceLinks, SecurityContext: &corev1.PodSecurityContext{RunAsNonRoot: &runAsNonRoot, SeccompProfile: &corev1.SeccompProfile{Type: corev1.SeccompProfileTypeRuntimeDefault}}, Containers: []corev1.Container{{Name: "application", Image: value.Image, ImagePullPolicy: corev1.PullIfNotPresent, TerminationMessagePath: corev1.TerminationMessagePathDefault, TerminationMessagePolicy: corev1.TerminationMessageReadFile, EnvFrom: secretEnvironmentSources(value.ApplicationSecrets), Ports: []corev1.ContainerPort{{Name: "http", ContainerPort: value.ContainerPort, Protocol: corev1.ProtocolTCP}}, ReadinessProbe: &corev1.Probe{ProbeHandler: corev1.ProbeHandler{HTTPGet: &corev1.HTTPGetAction{Path: value.ReadinessPath, Port: intstrFromInt(value.ContainerPort), Scheme: corev1.URIScheme(strings.ToUpper(value.ReadinessScheme))}}, InitialDelaySeconds: value.InitialDelay, PeriodSeconds: value.Period, TimeoutSeconds: value.Timeout, SuccessThreshold: 1, FailureThreshold: value.FailureThreshold}, Resources: corev1.ResourceRequirements{Requests: resourceList(value.Resources.CPURequestMilli, value.Resources.MemoryRequest), Limits: resourceList(value.Resources.CPULimitMilli, value.Resources.MemoryLimit)}, SecurityContext: &corev1.SecurityContext{AllowPrivilegeEscalation: &allowEscalation, ReadOnlyRootFilesystem: &readOnly, RunAsNonRoot: &runAsNonRoot, Capabilities: &corev1.Capabilities{Drop: []corev1.Capability{"ALL"}}}}}}}}}
}

// deploymentFromAPI translates immutable workload authority and finite rollout status.
func deploymentFromAPI(value *appsv1.Deployment) Deployment {
	result := Deployment{Meta: fromMeta(value.ObjectMeta), Generation: value.Generation, ObservedGeneration: value.Status.ObservedGeneration, AvailableReplicas: value.Status.AvailableReplicas, UpdatedReplicas: value.Status.UpdatedReplicas}
	if value.Spec.Replicas != nil {
		result.Replicas = *value.Spec.Replicas
	}
	if len(value.Spec.Template.Spec.Containers) == 1 {
		container := value.Spec.Template.Spec.Containers[0]
		result.Image = container.Image
		if len(container.Ports) == 1 {
			result.ContainerPort = container.Ports[0].ContainerPort
		}
		result.Resources = resourcesFromLists(container.Resources.Requests, container.Resources.Limits)
		if container.ReadinessProbe != nil && container.ReadinessProbe.HTTPGet != nil {
			result.ReadinessScheme = strings.ToLower(string(container.ReadinessProbe.HTTPGet.Scheme))
			result.ReadinessPath = container.ReadinessProbe.HTTPGet.Path
			result.InitialDelay = container.ReadinessProbe.InitialDelaySeconds
			result.Period = container.ReadinessProbe.PeriodSeconds
			result.Timeout = container.ReadinessProbe.TimeoutSeconds
			result.FailureThreshold = container.ReadinessProbe.FailureThreshold
		}
		for _, source := range container.EnvFrom {
			if source.SecretRef != nil {
				result.ApplicationSecrets = append(result.ApplicationSecrets, source.SecretRef.Name)
			}
		}
	}
	for _, reference := range value.Spec.Template.Spec.ImagePullSecrets {
		result.ImagePullSecrets = append(result.ImagePullSecrets, reference.Name)
	}
	result.ApplicationSecrets, result.ImagePullSecrets = sortedStrings(result.ApplicationSecrets), sortedStrings(result.ImagePullSecrets)
	result.RepositoryID = value.Annotations["featureproof.io/repository-id"]
	result.CommitOID = value.Annotations["featureproof.io/commit-oid"]
	result.ServiceAccount = value.Spec.Template.Spec.ServiceAccountName
	result.PodSecurity = value.Labels["pod-security.kubernetes.io/enforce"]
	for _, condition := range value.Status.Conditions {
		if condition.Type == appsv1.DeploymentProgressing && condition.Status == corev1.ConditionTrue {
			result.Progressing = true
		}
		if condition.Type == appsv1.DeploymentProgressing && condition.Reason == "ProgressDeadlineExceeded" {
			result.ProgressDeadlineExceeded = true
		}
	}
	result.ClosedShape = reflect.DeepEqual(value.Spec, deploymentToAPI(result).Spec)
	return result
}

// serviceToAPI constructs the sole internal ClusterIP Service form accepted by the provider.
func serviceToAPI(value Service) *corev1.Service {
	return &corev1.Service{ObjectMeta: toMeta(value.Meta), Spec: corev1.ServiceSpec{Type: corev1.ServiceTypeClusterIP, Selector: map[string]string{"featureproof.io/workload": value.Selector}, Ports: []corev1.ServicePort{{Name: value.Scheme, Protocol: corev1.ProtocolTCP, Port: value.Port, TargetPort: intstrFromInt(value.TargetPort)}}, SessionAffinity: corev1.ServiceAffinityNone}}
}

// serviceFromAPI translates one ClusterIP Service and rejects unsupported shapes through comparison.
func serviceFromAPI(value *corev1.Service) Service {
	result := Service{Meta: fromMeta(value.ObjectMeta)}
	if len(value.Spec.Ports) == 1 {
		result.Scheme = value.Spec.Ports[0].Name
		result.Port = value.Spec.Ports[0].Port
		result.TargetPort = int32(value.Spec.Ports[0].TargetPort.IntValue())
	}
	result.Selector = value.Spec.Selector["featureproof.io/workload"]
	result.ClosedShape = serviceShapeValid(value, result)
	return result
}

// serviceShapeValid rejects external exposure, additional selectors/ports and noncanonical Service behavior.
func serviceShapeValid(value *corev1.Service, projected Service) bool {
	expected := serviceToAPI(projected).Spec
	if value.Spec.ClusterIP == "" || value.Spec.ClusterIP == corev1.ClusterIPNone || len(value.Spec.ClusterIPs) != 1 || value.Spec.ClusterIPs[0] != value.Spec.ClusterIP || len(value.Spec.IPFamilies) != 1 || value.Spec.IPFamilyPolicy == nil || *value.Spec.IPFamilyPolicy != corev1.IPFamilyPolicySingleStack || value.Spec.InternalTrafficPolicy == nil || *value.Spec.InternalTrafficPolicy != corev1.ServiceInternalTrafficPolicyCluster {
		return false
	}
	actual := *value.Spec.DeepCopy()
	actual.ClusterIP, actual.ClusterIPs, actual.IPFamilies, actual.IPFamilyPolicy, actual.InternalTrafficPolicy = "", nil, nil, nil, nil
	return reflect.DeepEqual(actual, expected)
}

// podFromAPI translates one owned Pod readiness and runtime image identity.
func podFromAPI(value *corev1.Pod) Pod {
	result := Pod{Meta: fromMeta(value.ObjectMeta), OwnerUID: ownerReferenceUID(value.OwnerReferences), Revision: value.Annotations["featureproof.io/commit-oid"], Terminating: value.DeletionTimestamp != nil}
	if len(value.Spec.Containers) == 1 {
		container := value.Spec.Containers[0]
		result.Image = container.Image
		result.Resources = resourcesFromLists(container.Resources.Requests, container.Resources.Limits)
		if len(container.Ports) == 1 {
			result.ContainerPort = container.Ports[0].ContainerPort
		}
		if container.ReadinessProbe != nil && container.ReadinessProbe.HTTPGet != nil {
			result.ReadinessScheme = strings.ToLower(string(container.ReadinessProbe.HTTPGet.Scheme))
			result.ReadinessPath = container.ReadinessProbe.HTTPGet.Path
			result.InitialDelay = container.ReadinessProbe.InitialDelaySeconds
			result.Period = container.ReadinessProbe.PeriodSeconds
			result.Timeout = container.ReadinessProbe.TimeoutSeconds
			result.FailureThreshold = container.ReadinessProbe.FailureThreshold
		}
		for _, source := range container.EnvFrom {
			if source.SecretRef != nil {
				result.ApplicationSecrets = append(result.ApplicationSecrets, source.SecretRef.Name)
			}
		}
	}
	result.ServiceAccount = value.Spec.ServiceAccountName
	for _, reference := range value.Spec.ImagePullSecrets {
		result.ImagePullSecrets = append(result.ImagePullSecrets, reference.Name)
	}
	result.ApplicationSecrets, result.ImagePullSecrets = sortedStrings(result.ApplicationSecrets), sortedStrings(result.ImagePullSecrets)
	if len(value.Status.ContainerStatuses) == 1 {
		result.ImageID = value.Status.ContainerStatuses[0].ImageID
	}
	for _, condition := range value.Status.Conditions {
		if condition.Type == corev1.PodReady && condition.Status == corev1.ConditionTrue {
			result.Ready = true
		}
	}
	projected := Deployment{Meta: ObjectMeta{Name: value.Labels["featureproof.io/workload"]}, Image: result.Image, Resources: result.Resources, ServiceAccount: result.ServiceAccount, ContainerPort: result.ContainerPort, ReadinessScheme: result.ReadinessScheme, ReadinessPath: result.ReadinessPath, InitialDelay: result.InitialDelay, Period: result.Period, Timeout: result.Timeout, FailureThreshold: result.FailureThreshold, ApplicationSecrets: result.ApplicationSecrets, ImagePullSecrets: result.ImagePullSecrets}
	expectedSpec := deploymentToAPI(projected).Spec.Template.Spec
	expectedSpec.NodeName = value.Spec.NodeName
	result.ClosedShape = reflect.DeepEqual(value.Spec, expectedSpec) && len(value.Status.ContainerStatuses) == 1 && value.Status.ContainerStatuses[0].Name == "application" && value.Status.ContainerStatuses[0].Image == result.Image
	return result
}

// endpointSliceFromAPI translates ready endpoints and Pod target UIDs.
func endpointSliceFromAPI(value *discoveryv1.EndpointSlice) EndpointSlice {
	result := EndpointSlice{Meta: fromMeta(value.ObjectMeta), ServiceName: value.Labels[discoveryv1.LabelServiceName], OwnerUID: ownerReferenceUID(value.OwnerReferences), AddressType: string(value.AddressType)}
	if len(value.Ports) == 1 && value.Ports[0].Port != nil && value.Ports[0].Protocol != nil {
		if value.Ports[0].Name != nil {
			result.PortName = *value.Ports[0].Name
		}
		result.Port = *value.Ports[0].Port
		result.Protocol = string(*value.Ports[0].Protocol)
	}
	closed := endpointSliceMetadataValid(value)
	for _, endpoint := range value.Endpoints {
		ready := endpoint.Conditions.Ready != nil && *endpoint.Conditions.Ready
		uid, podName, podNamespace, targetKind, targetAPIVersion, targetResourceVersion, targetFieldPath := "", "", "", "", "", "", ""
		if endpoint.TargetRef != nil && endpoint.TargetRef.Kind == "Pod" && endpoint.TargetRef.UID != "" {
			uid, podName, podNamespace = string(endpoint.TargetRef.UID), endpoint.TargetRef.Name, endpoint.TargetRef.Namespace
			targetKind, targetAPIVersion, targetResourceVersion, targetFieldPath = endpoint.TargetRef.Kind, endpoint.TargetRef.APIVersion, endpoint.TargetRef.ResourceVersion, endpoint.TargetRef.FieldPath
			if targetAPIVersion != "" || targetResourceVersion != "" || targetFieldPath != "" {
				closed = false
			}
		} else {
			closed = false
		}
		if len(endpoint.Addresses) != 1 || endpoint.Hostname != nil || endpoint.NodeName != nil || endpoint.Zone != nil || endpoint.Hints != nil || endpoint.Conditions.Serving != nil || endpoint.Conditions.Terminating != nil {
			closed = false
		}
		for _, address := range endpoint.Addresses {
			if parsed := net.ParseIP(address); parsed == nil || parsed.String() != address {
				closed = false
			}
			result.Endpoints = append(result.Endpoints, Endpoint{Address: address, PodUID: uid, PodName: podName, PodNamespace: podNamespace, TargetKind: targetKind, TargetAPIVersion: targetAPIVersion, TargetResourceVersion: targetResourceVersion, TargetFieldPath: targetFieldPath, Ready: ready})
		}
	}
	result.ClosedShape = closed
	return result
}

// endpointSliceMetadataValid restricts discovery to one exact Service-owned, TCP endpoint shape.
func endpointSliceMetadataValid(value *discoveryv1.EndpointSlice) bool {
	if value.AddressType != discoveryv1.AddressTypeIPv4 && value.AddressType != discoveryv1.AddressTypeIPv6 || len(value.Ports) != 1 || value.Ports[0].Name == nil || value.Ports[0].Port == nil || value.Ports[0].Protocol == nil || *value.Ports[0].Protocol != corev1.ProtocolTCP || value.Ports[0].AppProtocol != nil || len(value.OwnerReferences) != 1 {
		return false
	}
	owner := value.OwnerReferences[0]
	if owner.APIVersion != "v1" || owner.Kind != "Service" || owner.Name != value.Labels[discoveryv1.LabelServiceName] || owner.Controller == nil || !*owner.Controller || owner.UID == "" {
		return false
	}
	for name, label := range value.Labels {
		if name != discoveryv1.LabelServiceName && name != discoveryv1.LabelManagedBy || label == "" {
			return false
		}
	}
	return len(value.Annotations) == 0 && len(value.Finalizers) == 0
}

// ownerReferenceUID returns the sole controller UID or empty when ownership is ambiguous.
func ownerReferenceUID(values []metav1.OwnerReference) string {
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

// boolPointer constructs explicit API booleans without relying on defaults.
func boolPointer(value bool) *bool { return &value }

// secretEnvironmentSources turns provider-owned Secret names into application environment references.
func secretEnvironmentSources(names []string) []corev1.EnvFromSource {
	result := make([]corev1.EnvFromSource, len(names))
	for index, name := range names {
		result[index] = corev1.EnvFromSource{SecretRef: &corev1.SecretEnvSource{LocalObjectReference: corev1.LocalObjectReference{Name: name}}}
	}
	return result
}

// localObjectReferences turns provider-owned image-pull Secret names into exact Pod references.
func localObjectReferences(names []string) []corev1.LocalObjectReference {
	result := make([]corev1.LocalObjectReference, len(names))
	for index, name := range names {
		result[index] = corev1.LocalObjectReference{Name: name}
	}
	return result
}

// intstrFromInt constructs an exact integer Service or policy port.
func intstrFromInt(value int32) intstr.IntOrString { return intstr.FromInt32(value) }

// classifyAPI maps official client errors to the content-free port taxonomy.
func classifyAPI(ctx context.Context, operation string, err error) error {
	if err == nil {
		return nil
	}
	if ctx != nil && ctx.Err() != nil {
		return ctx.Err()
	}
	code := CodeTransient
	switch {
	case apierrors.IsNotFound(err):
		code = CodeNotFound
	case apierrors.IsAlreadyExists(err):
		code = CodeAlreadyExists
	case apierrors.IsConflict(err):
		code = CodeConflict
	case apierrors.IsForbidden(err):
		code = CodeForbidden
	case apierrors.IsInvalid(err), apierrors.IsBadRequest(err):
		code = CodeInvalid
	case apierrors.IsTooManyRequests(err):
		code = CodeRateLimited
	case apierrors.IsServerTimeout(err), apierrors.IsTimeout(err), apierrors.IsServiceUnavailable(err):
		code = CodeTransient
	}
	return &ClientError{Code: code, Operation: operation, Cause: safeCause(err)}
}

// safeCause preserves only recognized cancellation and never remote status text.
func safeCause(err error) error {
	if errors.Is(err, context.Canceled) {
		return context.Canceled
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return context.DeadlineExceeded
	}
	return nil
}
