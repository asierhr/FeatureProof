// Package kubernetes provides the target-scoped, read-only Kubernetes observation capability.
package kubernetes

import (
	"context"
	"fmt"
	"time"

	"github.com/asierhr/featureProof/internal/environment"
)

// ToolName is the stable registry identity exposed to validated plans.
const ToolName = "kubernetes.observe"

// SchemaVersion identifies the normalized observation envelope.
const SchemaVersion = "featureproof.kubernetes-observation/v1"

// CatalogVersion identifies the sealed inspection authority schema.
const CatalogVersion = 1

// CheckKind is the closed vocabulary of observations a plan may select.
type CheckKind string

// Supported check kinds prevent plans from constructing arbitrary Kubernetes queries.
const (
	// CheckDeployment selects rollout generation, replica, and condition observations.
	CheckDeployment CheckKind = "deployment_availability"
	// CheckPodRestarts selects owner-scoped Pod readiness and restart observations.
	CheckPodRestarts CheckKind = "pod_restarts"
	// CheckJob selects exact Job counters and terminal conditions.
	CheckJob CheckKind = "job_status"
	// CheckService selects Service shape and address-free EndpointSlice readiness.
	CheckService CheckKind = "service_endpoints"
	// CheckEvents selects bounded allowlisted Events for one authorized identity.
	CheckEvents CheckKind = "events"
	// CheckConnectivity selects one catalogued policy relation without active probing.
	CheckConnectivity CheckKind = "declared_connectivity"
)

// ResourceKind classifies composition-owned catalog entries without exposing GVRs.
type ResourceKind string

// Resource kinds form the only objects addressable through the inspection catalog.
const (
	// KindDeployment identifies an exact rollout anchor.
	KindDeployment ResourceKind = "Deployment"
	// KindPodSet identifies a selector and owner-bound Pod collection.
	KindPodSet ResourceKind = "PodSet"
	// KindJob identifies one exact batch Job.
	KindJob ResourceKind = "Job"
	// KindService identifies one exact Service and its owned EndpointSlices.
	KindService ResourceKind = "Service"
	// KindEventScope identifies the exact involved UID authorized for Events.
	KindEventScope ResourceKind = "EventScope"
	// KindNetworkPolicy identifies one exact policy in a declared relation.
	KindNetworkPolicy ResourceKind = "NetworkPolicy"
)

// OutcomeCategory separates observations from operationally inconclusive outcomes.
type OutcomeCategory string

// Outcome categories are stable inputs to deterministic planning behavior.
const (
	// OutcomeObserved means a complete observation is available for assertions.
	OutcomeObserved OutcomeCategory = "observed"
	// OutcomeAbsent means the exact workload resource was confirmed absent.
	OutcomeAbsent OutcomeCategory = "resource_absent"
	// OutcomeInsufficient means collection completeness or snapshot stability was lost.
	OutcomeInsufficient OutcomeCategory = "insufficient_evidence"
	// OutcomeAccessDenied means the minimum read identity was forbidden.
	OutcomeAccessDenied OutcomeCategory = "access_denied"
	// OutcomeTimeout means the tool-owned deadline expired.
	OutcomeTimeout OutcomeCategory = "timeout"
	// OutcomeTransient means Kubernetes reported a retry-safe availability failure.
	OutcomeTransient OutcomeCategory = "transient_error"
	// OutcomeIntegrity means authority, ownership, identity, or schema drifted.
	OutcomeIntegrity OutcomeCategory = "integrity_error"
)

// Clock provides authority-validation time without binding tests to wall time.
type Clock interface{ Now() time.Time }

// TimeoutFactory creates the per-observation deadline owned by the tool call.
type TimeoutFactory func(context.Context, time.Duration) (context.Context, context.CancelFunc)

// Config fixes authority, limits, and replaceable boundaries for one target-scoped tool.
type Config struct {
	Target         environment.ValidationTarget
	Binding        environment.Binding
	Identity       IdentityBinding
	Catalog        Catalog
	Timeout        time.Duration
	MaxPages       int
	PageSize       int64
	MaxItems       int
	MaxEvents      int
	MaxOutputBytes int
	AllowedReasons []string
	Clock          Clock
	WithTimeout    TimeoutFactory
	Reader         Reader
}

// IdentityBinding seals the exact ServiceAccount and effective RBAC rules approved by composition.
type IdentityBinding struct {
	Namespace      string     `json:"namespace"`
	ServiceAccount string     `json:"service_account"`
	Rules          []RBACRule `json:"rules"`
	Digest         string     `json:"digest"`
}

// Resource grants one logical alias access to one exact object or precompiled selector.
type Resource struct {
	Alias          string        `json:"alias"`
	Kind           ResourceKind  `json:"kind"`
	Name           string        `json:"name"`
	UID            string        `json:"uid"`
	OwnerUID       string        `json:"owner_uid,omitempty"`
	OwnerAlias     string        `json:"owner_alias,omitempty"`
	Selector       string        `json:"selector,omitempty"`
	Checks         []CheckKind   `json:"checks"`
	ContainerRoles []string      `json:"container_roles,omitempty"`
	Ports          []ServicePort `json:"ports,omitempty"`
}

// Relationship grants one logical alias access to a declared service-policy relation.
type Relationship struct {
	Alias            string `json:"alias"`
	SourceAlias      string `json:"source_alias"`
	DestinationAlias string `json:"destination_alias"`
	PolicyAlias      string `json:"policy_alias"`
	Protocol         string `json:"protocol"`
	Port             int32  `json:"port"`
	PolicyDigest     string `json:"policy_digest"`
}

// Catalog is the canonical composition-owned authority used by a single tool instance.
type Catalog struct {
	Version         int            `json:"version"`
	TargetDigest    string         `json:"target_digest"`
	SelectionDigest string         `json:"selection_digest"`
	ProviderVersion string         `json:"provider_version"`
	Namespace       string         `json:"namespace"`
	NamespaceUID    string         `json:"namespace_uid"`
	Resources       []Resource     `json:"resources"`
	Relationships   []Relationship `json:"relationships"`
	Digest          string         `json:"digest"`
}

// PageRequest carries only tool-owned pagination state to the read-only adapter.
type PageRequest struct {
	Limit                     int64
	Continue, ResourceVersion string
}

// Namespace is the minimal namespace anchor needed to detect replacement and drift.
type Namespace struct{ UID, ResourceVersion string }

// Condition is a text-minimized status flag whose reason must pass the configured allowlist.
type Condition struct{ Type, Status, Reason string }

// Deployment is the consumer-owned projection used for rollout observations.
type Deployment struct {
	UID, OwnerUID, ResourceVersion     string
	Generation, ObservedGeneration     int64
	Desired, Updated, Available, Ready int32
	Conditions                         []Condition
}

// ContainerStatus contains only role, readiness, and restart count.
type ContainerStatus struct {
	Role     string
	Ready    bool
	Restarts int32
}

// Pod is the consumer-owned projection used for owner-scoped restart observations.
type Pod struct {
	UID, OwnerUID, ResourceVersion, Phase string
	Ready, Terminating                    bool
	Containers                            []ContainerStatus
}

// PodPage is one finite page returned for a precompiled owner selector.
type PodPage struct {
	Items                     []Pod
	Continue, ResourceVersion string
}

// Job is the consumer-owned projection of finite Job status and conditions.
type Job struct {
	UID, OwnerUID, ResourceVersion         string
	Generation                             int64
	Completions, Active, Succeeded, Failed int32
	Conditions                             []Condition
}

// ServicePort is a declared service protocol and port without addresses.
type ServicePort struct {
	Name     string `json:"name"`
	Protocol string `json:"protocol"`
	Port     int32  `json:"port"`
}

// Service is the consumer-owned projection used as the EndpointSlice anchor.
type Service struct {
	UID, OwnerUID, ResourceVersion, Type string
	Ports                                []ServicePort
}

// EndpointSlice is the address-free aggregation unit for service readiness.
type EndpointSlice struct {
	UID, OwnerUID, ServiceUID, ResourceVersion string
	Ready, NotReady                            int
}

// EndpointSlicePage is one finite page selected by an exact catalogued service relation.
type EndpointSlicePage struct {
	Items                     []EndpointSlice
	Continue, ResourceVersion string
}

// Event is the allowlist-ready projection that deliberately omits message and reporting identity.
type Event struct {
	UID, InvolvedUID, ResourceVersion, Type, Reason string
	Count                                           int32
	FirstObserved, LastObserved                     time.Time
}

// EventPage is one finite page already scoped to catalogued involved UIDs.
type EventPage struct {
	Items                     []Event
	Continue, ResourceVersion string
}

// NetworkPolicy is the minimal declared policy projection; it never represents active reachability.
type NetworkPolicy struct {
	UID, ResourceVersion      string
	Protocol                  string
	Port                      int32
	SourceUID, DestinationUID string
	SpecDigest                string
	Declared                  bool
}

// Reader is the deliberately non-generic Kubernetes read port owned by this consumer.
type Reader interface {
	GetNamespace(context.Context, string) (Namespace, error)
	GetDeployment(context.Context, string, string) (Deployment, error)
	ListOwnedPodsPage(context.Context, string, string, PageRequest) (PodPage, error)
	GetJob(context.Context, string, string) (Job, error)
	GetService(context.Context, string, string) (Service, error)
	ListServiceEndpointSlicesPage(context.Context, string, string, PageRequest) (EndpointSlicePage, error)
	ListScopedEventsPage(context.Context, string, []string, PageRequest) (EventPage, error)
	GetNetworkPolicy(context.Context, string, string) (NetworkPolicy, error)
}

// ReaderError carries a content-free adapter category across the trust boundary.
type ReaderError struct {
	Category  OutcomeCategory
	Transient bool
	Cause     error
}

// Error renders only a stable category and never remote Kubernetes text.
func (e *ReaderError) Error() string { return "kubernetes read failed: " + string(e.Category) }

// Unwrap preserves cancellation without making remote bodies observable.
func (e *ReaderError) Unwrap() error { return e.Cause }

// InputError identifies a rejected plan field without echoing its untrusted value.
type InputError struct{ Field, Category string }

// Error renders a stable input rejection suitable for journals.
func (e *InputError) Error() string {
	return fmt.Sprintf("invalid kubernetes observation input: %s at %s", e.Category, e.Field)
}

// ConfigError identifies trusted composition data rejected before any cluster I/O.
type ConfigError struct{ Field string }

// Error renders the invalid configuration boundary without exposing its value.
func (e *ConfigError) Error() string {
	return "invalid kubernetes observation configuration at " + e.Field
}

// Outcome reports whether an observation is complete, drift-free, and untruncated.
type Outcome struct {
	Category  OutcomeCategory `json:"category"`
	Complete  bool            `json:"complete"`
	Drift     bool            `json:"drift"`
	Truncated bool            `json:"truncated"`
}

// Version records an opaque observed version against a safe logical alias.
type Version struct {
	ResourceAlias   string `json:"resource_alias"`
	Kind            string `json:"kind"`
	UIDDigest       string `json:"uid_digest"`
	ResourceVersion string `json:"resource_version"`
}

// Truncation records loss of exhaustiveness without retaining omitted values.
type Truncation struct {
	Section  string `json:"section"`
	Observed int    `json:"observed"`
	Retained int    `json:"retained"`
}

// Result is the normalized, bounded envelope persisted as evidence.
type Result struct {
	Schema         string         `json:"schema"`
	CheckKind      CheckKind      `json:"check_kind"`
	ResourceAlias  string         `json:"resource_alias"`
	Outcome        Outcome        `json:"outcome"`
	Observation    any            `json:"observation"`
	Versions       []Version      `json:"versions"`
	SnapshotDigest string         `json:"snapshot_digest,omitempty"`
	Truncations    []Truncation   `json:"truncations"`
	Error          map[string]any `json:"error"`
}
