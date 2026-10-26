package kubernetes

import (
	"context"
	"errors"
	"fmt"
)

// Pair represents provider-owned metadata without accepting arbitrary maps from callers.
type Pair struct {
	Name  string
	Value string
}

// ObjectMeta is the bounded identity and authority projection shared by managed resources.
type ObjectMeta struct {
	Name, Namespace, UID, ResourceVersion, SpecDigest string
	Labels, Annotations                               []Pair
}

// Resources captures explicit CPU and memory requests and limits for one container.
type Resources struct {
	CPURequestMilli, CPULimitMilli int64
	MemoryRequest, MemoryLimit     int64
}

// Namespace is the isolated lifecycle root whose UID guards ownership and deletion.
type Namespace struct{ Meta ObjectMeta }

// ResourceQuota is the exact aggregate namespace resource ceiling.
type ResourceQuota struct {
	Meta                             ObjectMeta
	CPUMilli, Memory, Pods, Services int64
	ClosedShape                      bool
}

// LimitRange is the exact per-container resource range applied before workloads.
type LimitRange struct {
	Meta        ObjectMeta
	Min, Max    Resources
	Default     Resources
	ClosedShape bool
}

// ServiceAccount is the provider-owned identity with token automount disabled.
type ServiceAccount struct {
	Meta           ObjectMeta
	AutomountToken bool
	ClosedShape    bool
}

// NetworkPolicy is one deny-by-default or explicitly allowlisted namespaced policy.
type NetworkPolicy struct {
	Meta        ObjectMeta
	Direction   string
	Rules       []NetworkRule
	ClosedShape bool
}

// SecretValue is sensitive application or image-pull material passed only to ClusterClient.
type SecretValue struct {
	Name  string
	Value []byte
}

// Secret is a provider-owned Kubernetes secret that never crosses observations or durable state.
type Secret struct {
	Meta        ObjectMeta
	Type        string
	Values      []SecretValue
	ClosedShape bool
}

// Deployment is the closed workload specification plus the status needed for readiness.
type Deployment struct {
	Meta                                  ObjectMeta
	Image, RepositoryID, CommitOID        string
	Replicas, ContainerPort               int32
	Resources                             Resources
	ServiceAccount, PodSecurity           string
	ReadinessScheme, ReadinessPath        string
	InitialDelay, Period, Timeout         int32
	FailureThreshold                      int32
	ApplicationSecrets, ImagePullSecrets  []string
	Generation, ObservedGeneration        int64
	AvailableReplicas, UpdatedReplicas    int32
	Progressing, ProgressDeadlineExceeded bool
	ClosedShape                           bool
}

// Service is the internal-only endpoint selector and port projection.
type Service struct {
	Meta             ObjectMeta
	Selector, Scheme string
	Port, TargetPort int32
	ClosedShape      bool
}

// Pod is one owned workload observation used to prove revision, readiness, and immutable image.
type Pod struct {
	Meta                                 ObjectMeta
	OwnerUID, Revision, Image, ImageID   string
	Resources                            Resources
	ServiceAccount                       string
	ContainerPort                        int32
	ReadinessScheme, ReadinessPath       string
	InitialDelay, Period, Timeout        int32
	FailureThreshold                     int32
	ApplicationSecrets, ImagePullSecrets []string
	Ready, Terminating                   bool
	ClosedShape                          bool
}

// Endpoint is one service endpoint tied to an expected Pod UID.
type Endpoint struct {
	Address, PodUID, PodName, PodNamespace                               string
	TargetKind, TargetAPIVersion, TargetResourceVersion, TargetFieldPath string
	Ready                                                                bool
}

// EndpointSlice is the bounded service discovery observation used before target publication.
type EndpointSlice struct {
	Meta                               ObjectMeta
	ServiceName, OwnerUID, AddressType string
	PortName                           string
	Port                               int32
	Protocol                           string
	Endpoints                          []Endpoint
	ClosedShape                        bool
}

// DeleteResult distinguishes confirmed absence from a newly requested deletion.
type DeleteResult string

// Delete outcomes let Release preserve uncertainty instead of guessing from transport errors.
const (
	// DeleteRequested means Kubernetes accepted deletion for the exact namespace UID.
	DeleteRequested DeleteResult = "requested"
	// DeleteAbsent means the namespace is already confirmed absent.
	DeleteAbsent DeleteResult = "absent"
)

// ClientCode is the closed Kubernetes transport failure taxonomy.
type ClientCode string

// Client codes preserve retry and authority semantics without leaking remote bodies.
const (
	// CodeNotFound reports confirmed absence.
	CodeNotFound ClientCode = "not_found"
	// CodeAlreadyExists requires readback and exact comparison.
	CodeAlreadyExists ClientCode = "already_exists"
	// CodeConflict reports UID, resourceVersion, or ownership contention.
	CodeConflict ClientCode = "conflict"
	// CodeForbidden reports authorization, admission, quota, or policy rejection.
	CodeForbidden ClientCode = "forbidden"
	// CodeInvalid reports a request rejected by Kubernetes validation.
	CodeInvalid ClientCode = "invalid"
	// CodeRateLimited permits bounded retry by the neutral lifecycle.
	CodeRateLimited ClientCode = "rate_limited"
	// CodeTransient reports a retryable API or transport outage.
	CodeTransient ClientCode = "transient"
)

// ClientError hides remote content while retaining a stable programmatic classification.
type ClientError struct {
	Code      ClientCode
	Operation string
	Cause     error
}

// Error returns content-free adapter-owned text.
func (e *ClientError) Error() string { return fmt.Sprintf("kubernetes %s: %s", e.Operation, e.Code) }

// Unwrap preserves context cancellation and safe typed causes.
func (e *ClientError) Unwrap() error { return e.Cause }

// IsClientCode classifies a client boundary error without text matching.
func IsClientCode(err error, code ClientCode) bool {
	var typed *ClientError
	return errors.As(err, &typed) && typed.Code == code
}

// ClusterClient is the narrow consumer-owned Kubernetes API port with no arbitrary operation.
type ClusterClient interface {
	CreateNamespace(context.Context, Namespace) (Namespace, error)
	GetNamespace(context.Context, string) (Namespace, error)
	DeleteNamespace(context.Context, string, string) (DeleteResult, error)
	CreateResourceQuota(context.Context, ResourceQuota) (ResourceQuota, error)
	GetResourceQuota(context.Context, string, string) (ResourceQuota, error)
	CreateLimitRange(context.Context, LimitRange) (LimitRange, error)
	GetLimitRange(context.Context, string, string) (LimitRange, error)
	CreateServiceAccount(context.Context, ServiceAccount) (ServiceAccount, error)
	GetServiceAccount(context.Context, string, string) (ServiceAccount, error)
	CreateNetworkPolicy(context.Context, NetworkPolicy) (NetworkPolicy, error)
	GetNetworkPolicy(context.Context, string, string) (NetworkPolicy, error)
	CreateSecret(context.Context, Secret) (Secret, error)
	VerifySecret(context.Context, Secret) (Secret, error)
	CreateDeployment(context.Context, Deployment) (Deployment, error)
	GetDeployment(context.Context, string, string) (Deployment, error)
	CreateService(context.Context, Service) (Service, error)
	GetService(context.Context, string, string) (Service, error)
	ListOwnedPods(context.Context, string, string, int64) ([]Pod, error)
	ListOwnedEndpointSlices(context.Context, string, string, int64) ([]EndpointSlice, error)
}
