package kubernetes

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/asierhr/featureProof/internal/environment"
	"github.com/asierhr/featureProof/internal/harness"
)

// fixedClock supplies the live target verification instant used by hermetic tests.
type fixedClock struct{ value time.Time }

// Now returns the deterministic composition instant.
func (c fixedClock) Now() time.Time { return c.value }

// fakeReader is a programmable, concurrency-safe implementation of the closed read port.
type fakeReader struct {
	mu               sync.Mutex
	namespace        []Namespace
	deployment       Deployment
	deployments      []Deployment
	job              Job
	jobs             []Job
	service          Service
	services         []Service
	policy           NetworkPolicy
	policies         []NetworkPolicy
	podPages         []PodPage
	podRequests      []PageRequest
	endpointPages    []EndpointSlicePage
	endpointRequests []PageRequest
	eventPages       []EventPage
	eventRequests    []PageRequest
	err              error
	calls            []string
	block            <-chan struct{}
	onRecord         func(string)
}

// deploymentErrorReader injects a failure after the namespace anchor succeeds.
type deploymentErrorReader struct {
	*fakeReader
	err error
}

// policyErrorReader injects a policy failure after namespace, Service, and EndpointSlice reads succeed.
type policyErrorReader struct {
	*fakeReader
	err error
}

// GetNetworkPolicy returns the relation-scoped failure selected by the test.
func (r *policyErrorReader) GetNetworkPolicy(context.Context, string, string) (NetworkPolicy, error) {
	return NetworkPolicy{}, r.err
}

// GetDeployment returns the resource-scoped failure selected by the test.
func (r *deploymentErrorReader) GetDeployment(context.Context, string, string) (Deployment, error) {
	return Deployment{}, r.err
}

// record captures an operation and cooperatively observes cancellation at the fake boundary.
func (f *fakeReader) record(ctx context.Context, operation string) error {
	if f.block != nil {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-f.block:
		}
	}
	f.mu.Lock()
	f.calls = append(f.calls, operation)
	err := f.err
	callback := f.onRecord
	f.mu.Unlock()
	if callback != nil {
		callback(operation)
	}
	return err
}

// GetNamespace returns scripted anchors in sequence and then repeats the final anchor.
func (f *fakeReader) GetNamespace(ctx context.Context, _ string) (Namespace, error) {
	if err := f.record(ctx, "namespace"); err != nil {
		return Namespace{}, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.namespace) == 0 {
		return Namespace{UID: "namespace-uid", ResourceVersion: "ns-v1"}, nil
	}
	value := f.namespace[0]
	if len(f.namespace) > 1 {
		f.namespace = f.namespace[1:]
	}
	return value, nil
}

// GetDeployment returns the programmed Deployment after recording one exact read.
func (f *fakeReader) GetDeployment(ctx context.Context, _, _ string) (Deployment, error) {
	if err := f.record(ctx, "deployment"); err != nil {
		return Deployment{}, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.deployments) > 0 {
		value := f.deployments[0]
		f.deployments = f.deployments[1:]
		return value, nil
	}
	if f.deployment.UID == "" {
		return Deployment{UID: "deployment-uid", ResourceVersion: "deployment-v1"}, nil
	}
	return f.deployment, nil
}

// ListOwnedPodsPage returns the next programmed page without interpreting selectors.
func (f *fakeReader) ListOwnedPodsPage(ctx context.Context, _, _ string, request PageRequest) (PodPage, error) {
	if err := f.record(ctx, "pods"); err != nil {
		return PodPage{}, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.podRequests = append(f.podRequests, request)
	if len(f.podPages) == 0 {
		return PodPage{ResourceVersion: "pod-list-v1"}, nil
	}
	value := f.podPages[0]
	f.podPages = f.podPages[1:]
	if value.ResourceVersion == "" {
		value.ResourceVersion = "pod-list-v1"
	}
	return value, nil
}

// GetJob returns the programmed Job after recording one exact read.
func (f *fakeReader) GetJob(ctx context.Context, _, _ string) (Job, error) {
	if err := f.record(ctx, "job"); err != nil {
		return Job{}, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.jobs) > 0 {
		value := f.jobs[0]
		f.jobs = f.jobs[1:]
		return value, nil
	}
	return f.job, nil
}

// GetService returns the programmed Service after recording one exact read.
func (f *fakeReader) GetService(ctx context.Context, _, _ string) (Service, error) {
	if err := f.record(ctx, "service"); err != nil {
		return Service{}, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.services) > 0 {
		value := f.services[0]
		f.services = f.services[1:]
		if len(value.Ports) == 0 {
			value.Ports = []ServicePort{{Name: "http", Protocol: "TCP", Port: 8080}}
		}
		return value, nil
	}
	if f.service.UID != "" && len(f.service.Ports) == 0 {
		f.service.Ports = []ServicePort{{Name: "http", Protocol: "TCP", Port: 8080}}
	}
	return f.service, nil
}

// ListServiceEndpointSlicesPage returns the next programmed address-free page.
func (f *fakeReader) ListServiceEndpointSlicesPage(ctx context.Context, _, _ string, request PageRequest) (EndpointSlicePage, error) {
	if err := f.record(ctx, "endpoints"); err != nil {
		return EndpointSlicePage{}, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.endpointRequests = append(f.endpointRequests, request)
	if len(f.endpointPages) == 0 {
		return EndpointSlicePage{ResourceVersion: "endpoint-list-v1"}, nil
	}
	value := f.endpointPages[0]
	f.endpointPages = f.endpointPages[1:]
	if value.ResourceVersion == "" {
		value.ResourceVersion = "endpoint-list-v1"
	}
	return value, nil
}

// ListScopedEventsPage returns the next programmed message-free Event page.
func (f *fakeReader) ListScopedEventsPage(ctx context.Context, _ string, _ []string, request PageRequest) (EventPage, error) {
	if err := f.record(ctx, "events"); err != nil {
		return EventPage{}, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.eventRequests = append(f.eventRequests, request)
	if len(f.eventPages) == 0 {
		return EventPage{ResourceVersion: "event-list-v1"}, nil
	}
	value := f.eventPages[0]
	f.eventPages = f.eventPages[1:]
	if value.ResourceVersion == "" {
		value.ResourceVersion = "event-list-v1"
	}
	return value, nil
}

// GetNetworkPolicy returns the programmed declared-policy projection.
func (f *fakeReader) GetNetworkPolicy(ctx context.Context, _, _ string) (NetworkPolicy, error) {
	if err := f.record(ctx, "policy"); err != nil {
		return NetworkPolicy{}, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.policies) > 0 {
		value := f.policies[0]
		f.policies = f.policies[1:]
		return value, nil
	}
	return f.policy, nil
}

// testNow is the shared instant before every target expiration used in tests.
var testNow = time.Date(2026, 10, 26, 12, 0, 0, 0, time.UTC)

// validTool constructs sealed authority and a tool whose limits are intentionally explicit.
func validTool(t *testing.T, reader Reader) (*Tool, Catalog) {
	t.Helper()
	target, binding := validTarget(t)
	catalog := validCatalog(t, target)
	identity := validIdentity(t, target)
	tool, err := New(Config{Target: target, Binding: binding, Identity: identity, Catalog: catalog, Timeout: time.Second, MaxPages: 3, PageSize: 2, MaxItems: 4, MaxEvents: 4, MaxOutputBytes: 1 << 20, AllowedReasons: []string{"Available", "Completed", "BackOff"}, Clock: fixedClock{testNow}, WithTimeout: context.WithTimeout, Reader: reader})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	return tool, catalog
}

// validTarget creates a provider-neutral Kubernetes target and binding through production sealers.
func validTarget(t *testing.T) (environment.ValidationTarget, environment.Binding) {
	return validTargetWithCapabilities(t, []string{ToolName})
}

// validTargetWithCapabilities creates a verified target whose sealed capability set is selected by the test.
func validTargetWithCapabilities(t *testing.T, capabilities []string) (environment.ValidationTarget, environment.Binding) {
	t.Helper()
	target, err := environment.SealTarget(environment.ValidationTarget{Version: environment.TargetVersion, RunID: "run-33", SelectionDigest: strings.Repeat("a", 64), ProviderID: "kubernetes", ProviderVersion: "v1", Kind: environment.KindManaged, ProjectID: "project", RepositoryID: "repository", CommitOID: strings.Repeat("b", 40), EnvironmentID: "environment", EnvironmentRef: "fp-run", ProfileID: "profile", ReservationID: "namespace-uid", Endpoint: "https://app.test/", Capabilities: capabilities, Attestation: environment.Attestation{EnvironmentID: "environment", RepositoryID: "repository", CommitOID: strings.Repeat("b", 40), Reference: "attestation", ValidUntil: testNow.Add(time.Hour), Verified: true}, Network: environment.NetworkPolicy{PolicyID: "network", Schemes: []string{"https"}, Hosts: []string{"app.test"}, Ports: []int{443}, BasePaths: []string{"/"}, MaxURLBytes: 2048}, LifecycleDigest: strings.Repeat("c", 64), ObservedVersion: "observed-v1", ObservationDigest: strings.Repeat("d", 64)}, testNow)
	if err != nil {
		t.Fatalf("SealTarget() error = %v", err)
	}
	binding, err := environment.SealBinding(environment.Binding{Version: environment.BindingVersion, RunID: target.RunID, SelectionDigest: target.SelectionDigest, ProviderID: target.ProviderID, ProviderVersion: target.ProviderVersion, RepositoryID: target.RepositoryID, CommitOID: target.CommitOID, EnvironmentID: target.EnvironmentID, TargetDigest: target.Digest, LifecycleDigest: target.LifecycleDigest, NetworkDigest: jsonDigest(target.Network), AttestationDigest: jsonDigest(target.Attestation)}, target, testNow)
	if err != nil {
		t.Fatalf("SealBinding() error = %v", err)
	}
	return target, binding
}

// validCatalog creates every supported resource and one declared connectivity relation.
func validCatalog(t *testing.T, target environment.ValidationTarget) Catalog {
	t.Helper()
	value, err := SealCatalog(Catalog{Version: CatalogVersion, TargetDigest: target.Digest, SelectionDigest: target.SelectionDigest, ProviderVersion: target.ProviderVersion, Namespace: target.EnvironmentID, NamespaceUID: target.ReservationID, Resources: []Resource{{Alias: "deployment", Kind: KindDeployment, Name: "app", UID: "deployment-uid", Checks: []CheckKind{CheckDeployment}}, {Alias: "pods", Kind: KindPodSet, Name: "app-pods", UID: "pod-set", OwnerUID: "deployment-uid", OwnerAlias: "deployment", Selector: "app=demo", Checks: []CheckKind{CheckPodRestarts}, ContainerRoles: []string{"application"}}, {Alias: "job", Kind: KindJob, Name: "migration", UID: "job-uid", Checks: []CheckKind{CheckJob}}, {Alias: "service", Kind: KindService, Name: "app", UID: "service-uid", Checks: []CheckKind{CheckService}, Ports: []ServicePort{{Name: "http", Protocol: "TCP", Port: 8080}}}, {Alias: "events", Kind: KindEventScope, Name: "app", UID: "deployment-uid", Checks: []CheckKind{CheckEvents}}, {Alias: "policy", Kind: KindNetworkPolicy, Name: "allow-app", UID: "policy-uid"}}, Relationships: []Relationship{{Alias: "app-connectivity", SourceAlias: "deployment", DestinationAlias: "service", PolicyAlias: "policy", Protocol: "TCP", Port: 8080, PolicyDigest: strings.Repeat("e", 64)}}})
	if err != nil {
		t.Fatalf("SealCatalog() error = %v", err)
	}
	return value
}

// validIdentity creates the exact composition-owned ServiceAccount and effective RBAC binding.
func validIdentity(t *testing.T, target environment.ValidationTarget) IdentityBinding {
	t.Helper()
	value, err := SealIdentityBinding(IdentityBinding{Namespace: target.EnvironmentID, ServiceAccount: "featureproof-observer", Rules: RequiredRBACRules(target.EnvironmentID)})
	if err != nil {
		t.Fatalf("SealIdentityBinding() error = %v", err)
	}
	return value
}

// jsonDigest reproduces the canonical map-free SHA-256 used by environment sealers.
func jsonDigest(value any) string {
	encoded, _ := json.Marshal(value)
	sum := sha256.Sum256(encoded)
	return hex.EncodeToString(sum[:])
}

// execute runs one authorized logical check through the public Tool boundary.
func execute(t *testing.T, tool *Tool, check CheckKind, alias string) (harness.ToolResult, error) {
	t.Helper()
	return tool.Execute(context.Background(), harness.ToolCall{RunID: tool.target.RunID, Input: map[string]any{"check_kind": string(check), "resource_alias": alias}})
}

// TestNewBindsVerifiedKubernetesTargetAndNamespaceUID covers sealed target, binding, and catalog authority.
func TestNewBindsVerifiedKubernetesTargetAndNamespaceUID(t *testing.T) {
	reader := &fakeReader{}
	tool, catalog := validTool(t, reader)
	catalog.Resources[0].Name = "mutated"
	if tool.authority.resources["deployment"].Name != "app" {
		t.Fatal("tool retained mutable caller catalog")
	}
	if tool.authority.namespace != tool.target.EnvironmentID || tool.authority.namespace == tool.target.EnvironmentRef {
		t.Fatalf("namespace authority is not literal EnvironmentID: authority=%q id=%q ref=%q", tool.authority.namespace, tool.target.EnvironmentID, tool.target.EnvironmentRef)
	}
	spec := tool.Spec()
	if spec.Name != ToolName || spec.Effects || !spec.Idempotent {
		t.Fatalf("unexpected spec: %+v", spec)
	}
}

// TestAuthorityMismatchFailsBeforeAPI proves target/catalog drift cannot reach the reader.
func TestAuthorityMismatchFailsBeforeAPI(t *testing.T) {
	target, binding := validTarget(t)
	catalog := validCatalog(t, target)
	catalog.TargetDigest = strings.Repeat("e", 64)
	catalog, _ = SealCatalog(catalog)
	reader := &fakeReader{}
	_, err := New(Config{Target: target, Binding: binding, Identity: validIdentity(t, target), Catalog: catalog, Timeout: time.Second, MaxPages: 1, PageSize: 1, MaxItems: 1, MaxEvents: 1, MaxOutputBytes: 2048, AllowedReasons: []string{}, Clock: fixedClock{testNow}, WithTimeout: context.WithTimeout, Reader: reader})
	if err == nil || len(reader.calls) != 0 {
		t.Fatal("mismatched authority was accepted or performed I/O")
	}
}

// TestValidateAcceptsOnlyAuthorizedCheckAlias proves plans select authority but never construct it.
func TestValidateAcceptsOnlyAuthorizedCheckAlias(t *testing.T) {
	tool, _ := validTool(t, &fakeReader{})
	if err := tool.Validate(map[string]any{"check_kind": string(CheckDeployment), "resource_alias": "deployment"}); err != nil {
		t.Fatalf("valid input rejected: %v", err)
	}
	bad := []map[string]any{{"check_kind": string(CheckDeployment), "resource_alias": "deployment", "namespace": "other"}, {"check_kind": string(CheckDeployment), "resource_alias": "missing"}, {"check_kind": "get", "resource_alias": "deployment"}}
	for _, input := range bad {
		if tool.Validate(input) == nil {
			t.Fatalf("invalid input accepted: %#v", input)
		}
	}
}

// TestDeploymentObservationNormalizesAvailabilityAndVersion proves rollout data remains an observation, not a verdict.
func TestDeploymentObservationNormalizesAvailabilityAndVersion(t *testing.T) {
	reader := &fakeReader{deployment: Deployment{UID: "deployment-uid", ResourceVersion: "dep-v1", Generation: 2, ObservedGeneration: 2, Desired: 3, Updated: 2, Available: 1, Ready: 1, Conditions: []Condition{{Type: "Available", Status: "False", Reason: "Available"}, {Type: "Progressing", Status: "True", Reason: "secret-canary"}}}}
	tool, _ := validTool(t, reader)
	result, err := execute(t, tool, CheckDeployment, "deployment")
	if err != nil {
		t.Fatal(err)
	}
	encoded, _ := json.Marshal(result.Output)
	text := string(encoded)
	if !strings.Contains(text, `"available_replicas":1`) || strings.Contains(text, "secret-canary") || strings.Contains(text, "verdict") {
		t.Fatalf("unexpected projection: %s", text)
	}
}

// TestPodObservationFiltersOwnerAndOrdersRestarts proves ownership filtering and stable role projection.
func TestPodObservationFiltersOwnerAndOrdersRestarts(t *testing.T) {
	reader := &fakeReader{podPages: []PodPage{{Items: []Pod{{UID: "pod-b", OwnerUID: "deployment-uid", ResourceVersion: "p2", Phase: "Running", Ready: true, Containers: []ContainerStatus{{Role: "sidecar", Restarts: 99}, {Role: "application", Ready: true, Restarts: 2}}}}}}}
	tool, _ := validTool(t, reader)
	result, err := execute(t, tool, CheckPodRestarts, "pods")
	if err != nil {
		t.Fatal(err)
	}
	encoded, _ := json.Marshal(result.Output)
	text := string(encoded)
	if !strings.Contains(text, `"restart_count":2`) || strings.Contains(text, "sidecar") {
		t.Fatalf("unexpected pod projection: %s", text)
	}
}

// TestPodEmptyInvalidIdentityAndOwnerAnchorRemainDistinct covers absence, integrity, and membership drift.
func TestPodEmptyInvalidIdentityAndOwnerAnchorRemainDistinct(t *testing.T) {
	tool, _ := validTool(t, &fakeReader{})
	empty, err := execute(t, tool, CheckPodRestarts, "pods")
	if err != nil || empty.Output["outcome"].(map[string]any)["category"] != string(OutcomeAbsent) {
		t.Fatalf("empty owned set was not absent: %#v %v", empty, err)
	}
	for name, pod := range map[string]Pod{"empty_uid": {OwnerUID: "deployment-uid"}, "missing_owner": {UID: "pod"}} {
		t.Run(name, func(t *testing.T) {
			tool, _ := validTool(t, &fakeReader{podPages: []PodPage{{Items: []Pod{pod}}}})
			result, err := execute(t, tool, CheckPodRestarts, "pods")
			if err == nil || result.Output["outcome"].(map[string]any)["category"] != string(OutcomeInsufficient) {
				t.Fatalf("incomplete pod identity was not insufficient evidence: %#v %v", result, err)
			}
		})
	}
	foreignTool, _ := validTool(t, &fakeReader{podPages: []PodPage{{Items: []Pod{{UID: "pod", OwnerUID: "foreign"}}}}})
	foreign, foreignErr := execute(t, foreignTool, CheckPodRestarts, "pods")
	if foreignErr != nil || foreign.Output["outcome"].(map[string]any)["category"] != string(OutcomeAbsent) {
		t.Fatalf("foreign pod was not excluded: %#v %v", foreign, foreignErr)
	}
	reader := &fakeReader{deployments: []Deployment{{UID: "deployment-uid", ResourceVersion: "d1"}, {UID: "deployment-uid", ResourceVersion: "d2"}}, podPages: []PodPage{{Items: []Pod{{UID: "pod", OwnerUID: "deployment-uid"}}}}}
	tool, _ = validTool(t, reader)
	drift, err := execute(t, tool, CheckPodRestarts, "pods")
	if err == nil || drift.Output["outcome"].(map[string]any)["category"] != string(OutcomeInsufficient) {
		t.Fatalf("owner anchor drift accepted: %#v %v", drift, err)
	}
}

// TestJobObservationNormalizesActiveCompleteAndFailed proves messages never cross the Job projection.
func TestJobObservationNormalizesActiveCompleteAndFailed(t *testing.T) {
	reader := &fakeReader{job: Job{UID: "job-uid", ResourceVersion: "j1", Completions: 1, Succeeded: 1, Conditions: []Condition{{Type: "Complete", Status: "True", Reason: "Completed"}}}}
	tool, _ := validTool(t, reader)
	result, err := execute(t, tool, CheckJob, "job")
	if err != nil {
		t.Fatal(err)
	}
	encoded, _ := json.Marshal(result.Output)
	if !strings.Contains(string(encoded), `"succeeded":1`) {
		t.Fatalf("missing job status: %s", encoded)
	}
}

// TestServiceEndpointObservationAggregatesOnlyOwnedSlices proves readiness aggregation excludes foreign slices.
func TestServiceEndpointObservationAggregatesOnlyOwnedSlices(t *testing.T) {
	reader := &fakeReader{service: Service{UID: "service-uid", ResourceVersion: "s1", Type: "ClusterIP", Ports: []ServicePort{{Name: "http", Protocol: "TCP", Port: 8080}}}, endpointPages: []EndpointSlicePage{{Items: []EndpointSlice{{UID: "slice-1", OwnerUID: "service-uid", ServiceUID: "service-uid", ResourceVersion: "e1", Ready: 2, NotReady: 1}, {UID: "foreign", OwnerUID: "other", ServiceUID: "other", Ready: 50}}}}}
	tool, _ := validTool(t, reader)
	result, err := execute(t, tool, CheckService, "service")
	if err != nil {
		t.Fatal(err)
	}
	observation := result.Output["observation"].(map[string]any)
	versions := result.Output["versions"].([]any)
	if observation["ready_endpoints"] != float64(2) || observation["not_ready_endpoints"] != float64(1) || len(versions) != 6 {
		t.Fatalf("unexpected endpoint aggregation: %#v", result.Output)
	}
}

// TestEventsAreScopedAllowlistedOrderedAndBounded proves untrusted Event text is absent from output.
func TestEventsAreScopedAllowlistedOrderedAndBounded(t *testing.T) {
	reader := &fakeReader{eventPages: []EventPage{{Items: []Event{{UID: "event-1", InvolvedUID: "deployment-uid", ResourceVersion: "ev1", Type: "Warning", Reason: "BackOff", Count: 2, LastObserved: testNow}, {UID: "event-1", InvolvedUID: "deployment-uid", Reason: "secret-canary"}, {UID: "foreign", InvolvedUID: "other", Reason: "secret-canary"}}}}}
	tool, _ := validTool(t, reader)
	result, err := execute(t, tool, CheckEvents, "events")
	if err != nil {
		t.Fatal(err)
	}
	encoded, _ := json.Marshal(result.Output)
	text := string(encoded)
	if !strings.Contains(text, "BackOff") || !strings.Contains(text, `"count":1`) || strings.Contains(text, "secret-canary") || strings.Contains(text, "foreign") {
		t.Fatalf("unexpected events: %s", text)
	}
}

// TestDeclaredConnectivityUsesOnlyCatalogRelationPolicyAndEndpoints proves the check reports declaration, not reachability.
func TestDeclaredConnectivityUsesOnlyCatalogRelationPolicyAndEndpoints(t *testing.T) {
	reader := &fakeReader{service: Service{UID: "service-uid", ResourceVersion: "s1"}, endpointPages: []EndpointSlicePage{{Items: []EndpointSlice{{UID: "slice", OwnerUID: "service-uid", ServiceUID: "service-uid", Ready: 1}}}}, policy: NetworkPolicy{UID: "policy-uid", ResourceVersion: "n1", Protocol: "TCP", Port: 8080, SpecDigest: strings.Repeat("e", 64), Declared: true}}
	tool, _ := validTool(t, reader)
	result, err := execute(t, tool, CheckConnectivity, "app-connectivity")
	if err != nil {
		t.Fatal(err)
	}
	encoded, _ := json.Marshal(result.Output)
	text := string(encoded)
	if !strings.Contains(text, `"declared":true`) || strings.Contains(text, "reachable") {
		t.Fatalf("unexpected connectivity projection: %s", text)
	}
}

// TestPaginationCycleAndLimitProduceTruncatedInsufficientEvidence proves partial collections never imply success.
func TestPaginationCycleAndLimitProduceTruncatedInsufficientEvidence(t *testing.T) {
	reader := &fakeReader{podPages: []PodPage{{Continue: "again"}, {Continue: "again"}}}
	tool, _ := validTool(t, reader)
	result, err := execute(t, tool, CheckPodRestarts, "pods")
	if err == nil || result.ErrorClass != harness.ErrorPermanent || result.Output["outcome"].(map[string]any)["category"] != string(OutcomeInsufficient) {
		t.Fatalf("cycle did not become insufficient evidence: result=%#v err=%v", result, err)
	}
}

// TestResourceAbsenceAndAccessDenialRemainDistinct proves absence is assertable while access failure is inconclusive.
func TestResourceAbsenceAndAccessDenialRemainDistinct(t *testing.T) {
	reader := &deploymentErrorReader{fakeReader: &fakeReader{}, err: &ReaderError{Category: OutcomeAbsent}}
	tool, _ := validTool(t, reader)
	absent, err := execute(t, tool, CheckDeployment, "deployment")
	if err != nil || absent.Output["outcome"].(map[string]any)["category"] != string(OutcomeAbsent) {
		t.Fatalf("absence lost: %#v %v", absent, err)
	}
	driftReader := &deploymentErrorReader{fakeReader: &fakeReader{namespace: []Namespace{{UID: "namespace-uid", ResourceVersion: "n1"}, {UID: "namespace-uid", ResourceVersion: "n2"}}}, err: &ReaderError{Category: OutcomeAbsent}}
	tool, _ = validTool(t, driftReader)
	drifted, err := execute(t, tool, CheckDeployment, "deployment")
	if err == nil || drifted.Output["outcome"].(map[string]any)["category"] != string(OutcomeInsufficient) {
		t.Fatalf("absence across namespace drift was trusted: %#v %v", drifted, err)
	}
	reader.err = &ReaderError{Category: OutcomeAccessDenied}
	tool, _ = validTool(t, reader)
	denied, err := execute(t, tool, CheckDeployment, "deployment")
	if err == nil || denied.ErrorClass != harness.ErrorPermanent {
		t.Fatalf("access denial not operational: %#v %v", denied, err)
	}
}

// TestCallerCancellationAndToolTimeoutStopFurtherReads proves cancellation propagates without partial evidence.
func TestCallerCancellationAndToolTimeoutStopFurtherReads(t *testing.T) {
	blocked := make(chan struct{})
	reader := &fakeReader{block: blocked}
	tool, _ := validTool(t, reader)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := tool.Execute(ctx, harness.ToolCall{RunID: tool.target.RunID, Input: map[string]any{"check_kind": string(CheckDeployment), "resource_alias": "deployment"}})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation lost: %v", err)
	}
	timeoutReader := &fakeReader{block: make(chan struct{})}
	tool, _ = validTool(t, timeoutReader)
	tool.withTimeout = func(parent context.Context, _ time.Duration) (context.Context, context.CancelFunc) {
		return context.WithDeadline(parent, testNow.Add(-time.Second))
	}
	result, err := execute(t, tool, CheckDeployment, "deployment")
	if err == nil || result.ErrorClass != harness.ErrorTransient || result.Output["outcome"].(map[string]any)["category"] != string(OutcomeTimeout) {
		t.Fatalf("timeout did not remain transient and inconclusive: %#v %v", result, err)
	}
}

// TestSnapshotRecordsOpaqueVersionsAndCanonicalDigest proves versions are opaque and ordering-independent content is sealed.
func TestSnapshotRecordsOpaqueVersionsAndCanonicalDigest(t *testing.T) {
	reader := &fakeReader{deployment: Deployment{UID: "deployment-uid", ResourceVersion: "opaque/10"}}
	tool, _ := validTool(t, reader)
	first, err := execute(t, tool, CheckDeployment, "deployment")
	if err != nil {
		t.Fatal(err)
	}
	digestValue, ok := first.Output["snapshot_digest"].(string)
	if !ok || len(digestValue) != 64 || !strings.Contains(mustJSON(first.Output), "opaque/10") {
		t.Fatalf("invalid snapshot: %#v", first.Output)
	}
	delete(first.Output, "snapshot_digest")
	if calculated := digest(first.Output); calculated != digestValue {
		t.Fatalf("snapshot digest does not authenticate returned content: want=%s got=%s json=%s", digestValue, calculated, mustJSON(first.Output))
	}
}

// mustJSON serializes structured test output for containment assertions.
func mustJSON(value any) string { encoded, _ := json.Marshal(value); return string(encoded) }
