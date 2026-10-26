package assertion

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/asierhr/featureProof/internal/environment"
	"github.com/asierhr/featureProof/internal/evidence"
	"github.com/asierhr/featureProof/internal/harness"
	"github.com/asierhr/featureProof/internal/planning"
	"github.com/asierhr/featureProof/internal/policy"
	"github.com/asierhr/featureProof/internal/tools"
	kubernetestool "github.com/asierhr/featureProof/internal/tools/kubernetes"
)

// assertionKubernetesClock fixes target verification time independently of executor evidence time.
type assertionKubernetesClock struct{ now time.Time }

// Now returns the immutable composition instant used by target verification.
func (c assertionKubernetesClock) Now() time.Time { return c.now }

// assertionKubernetesReader scripts Deployment, namespace drift, and access outcomes for vertical tests.
type assertionKubernetesReader struct {
	deployment    kubernetestool.Deployment
	err           error
	namespaceRead int
	drift         bool
}

// GetNamespace returns the authorized UID and optionally changes its opaque version on reread.
func (r *assertionKubernetesReader) GetNamespace(context.Context, string) (kubernetestool.Namespace, error) {
	r.namespaceRead++
	version := "namespace-v1"
	if r.drift && r.namespaceRead > 1 {
		version = "namespace-v2"
	}
	return kubernetestool.Namespace{UID: "namespace-uid", ResourceVersion: version}, nil
}

// GetDeployment returns the programmed observation or resource-scoped operational outcome.
func (r *assertionKubernetesReader) GetDeployment(context.Context, string, string) (kubernetestool.Deployment, error) {
	return r.deployment, r.err
}

// ListOwnedPodsPage is outside these Deployment-only vertical scenarios.
func (r *assertionKubernetesReader) ListOwnedPodsPage(context.Context, string, string, kubernetestool.PageRequest) (kubernetestool.PodPage, error) {
	return kubernetestool.PodPage{}, nil
}

// GetJob is outside these Deployment-only vertical scenarios.
func (r *assertionKubernetesReader) GetJob(context.Context, string, string) (kubernetestool.Job, error) {
	return kubernetestool.Job{}, nil
}

// GetService is outside these Deployment-only vertical scenarios.
func (r *assertionKubernetesReader) GetService(context.Context, string, string) (kubernetestool.Service, error) {
	return kubernetestool.Service{}, nil
}

// ListServiceEndpointSlicesPage is outside these Deployment-only vertical scenarios.
func (r *assertionKubernetesReader) ListServiceEndpointSlicesPage(context.Context, string, string, kubernetestool.PageRequest) (kubernetestool.EndpointSlicePage, error) {
	return kubernetestool.EndpointSlicePage{}, nil
}

// ListScopedEventsPage is outside these Deployment-only vertical scenarios.
func (r *assertionKubernetesReader) ListScopedEventsPage(context.Context, string, []string, kubernetestool.PageRequest) (kubernetestool.EventPage, error) {
	return kubernetestool.EventPage{}, nil
}

// GetNetworkPolicy is outside these Deployment-only vertical scenarios.
func (r *assertionKubernetesReader) GetNetworkPolicy(context.Context, string, string) (kubernetestool.NetworkPolicy, error) {
	return kubernetestool.NetworkPolicy{}, nil
}

// assertionKubernetesTool builds the real read-only tool with literal EnvironmentID authority.
func assertionKubernetesTool(t *testing.T, reader kubernetestool.Reader, maxOutput int) *kubernetestool.Tool {
	t.Helper()
	now := time.Date(2026, 10, 26, 12, 0, 0, 0, time.UTC)
	target, err := environment.SealTarget(environment.ValidationTarget{Version: environment.TargetVersion, RunID: "run", SelectionDigest: strings.Repeat("a", 64), ProviderID: "kubernetes", ProviderVersion: "v1", Kind: environment.KindManaged, ProjectID: "project", RepositoryID: "repository", CommitOID: strings.Repeat("b", 40), EnvironmentID: "fp-run", EnvironmentRef: "template-reference", ReservationID: "namespace-uid", Endpoint: "https://app.test/", Capabilities: []string{kubernetestool.ToolName}, Attestation: environment.Attestation{EnvironmentID: "fp-run", RepositoryID: "repository", CommitOID: strings.Repeat("b", 40), Reference: "attestation", ValidUntil: now.Add(time.Hour), Verified: true}, Network: environment.NetworkPolicy{PolicyID: "network", Schemes: []string{"https"}, Hosts: []string{"app.test"}, Ports: []int{443}, BasePaths: []string{"/"}, MaxURLBytes: 2048}, LifecycleDigest: strings.Repeat("c", 64), ObservedVersion: "v1", ObservationDigest: strings.Repeat("d", 64)}, now)
	if err != nil {
		t.Fatal(err)
	}
	binding, err := environment.SealBinding(environment.Binding{Version: environment.BindingVersion, RunID: target.RunID, SelectionDigest: target.SelectionDigest, ProviderID: target.ProviderID, ProviderVersion: target.ProviderVersion, RepositoryID: target.RepositoryID, CommitOID: target.CommitOID, EnvironmentID: target.EnvironmentID, TargetDigest: target.Digest, LifecycleDigest: target.LifecycleDigest, NetworkDigest: assertionJSONDigest(target.Network), AttestationDigest: assertionJSONDigest(target.Attestation)}, target, now)
	if err != nil {
		t.Fatal(err)
	}
	catalog, err := kubernetestool.SealCatalog(kubernetestool.Catalog{Version: kubernetestool.CatalogVersion, TargetDigest: target.Digest, SelectionDigest: target.SelectionDigest, ProviderVersion: target.ProviderVersion, Namespace: target.EnvironmentID, NamespaceUID: target.ReservationID, Resources: []kubernetestool.Resource{{Alias: "deployment", Kind: kubernetestool.KindDeployment, Name: "app", UID: "deployment-uid", Checks: []kubernetestool.CheckKind{kubernetestool.CheckDeployment}}}})
	if err != nil {
		t.Fatal(err)
	}
	identity, err := kubernetestool.SealIdentityBinding(kubernetestool.IdentityBinding{Namespace: target.EnvironmentID, ServiceAccount: "featureproof-observer", Rules: kubernetestool.RequiredRBACRules(target.EnvironmentID)})
	if err != nil {
		t.Fatal(err)
	}
	tool, err := kubernetestool.New(kubernetestool.Config{Target: target, Binding: binding, Identity: identity, Catalog: catalog, Timeout: time.Second, MaxPages: 2, PageSize: 2, MaxItems: 2, MaxEvents: 2, MaxOutputBytes: maxOutput, AllowedReasons: []string{"Available"}, Clock: assertionKubernetesClock{now}, WithTimeout: context.WithTimeout, Reader: reader})
	if err != nil {
		t.Fatal(err)
	}
	return tool
}

// assertionJSONDigest reproduces canonical provider-neutral binding hashes.
func assertionJSONDigest(value any) string {
	encoded, _ := json.Marshal(value)
	sum := sha256.Sum256(encoded)
	return hex.EncodeToString(sum[:])
}

// executeKubernetesAssertion runs a real validated plan, evidence store, and assertion Engine.
func executeKubernetesAssertion(t *testing.T, tool *kubernetestool.Tool, definition map[string]any) planning.PlanResult {
	t.Helper()
	registry, err := tools.NewRegistry(tool.Spec())
	if err != nil {
		t.Fatal(err)
	}
	plan := planning.ValidationPlan{ID: "plan", ConstraintID: "constraint", Steps: []planning.PlanStep{{ID: "kubernetes", Tool: kubernetestool.ToolName, Input: map[string]planning.InputValue{"check_kind": {Literal: &planning.LiteralValue{Value: string(kubernetestool.CheckDeployment)}}, "resource_alias": {Literal: &planning.LiteralValue{Value: "deployment"}}}, Assertions: []planning.AssertionDeclaration{{ID: "condition", Definition: definition}}}}}
	validator, err := planning.NewValidator(integrationConstraints{}, registry, DefinitionValidator{})
	if err != nil {
		t.Fatal(err)
	}
	validated, err := validator.Validate(plan)
	if err != nil {
		t.Fatal(err)
	}
	run, err := harness.NewRun("run", "objective", "constraint", harness.Limits{MaxToolCalls: 2, MaxRetries: 0})
	if err != nil {
		t.Fatal(err)
	}
	store, err := evidence.NewFileStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	executor, err := planning.NewPlanExecutor(planning.ExecutorConfig{Run: run}, planning.ExecutorDependencies{Registry: registry, Policy: &policy.Static{Decisions: map[string]harness.PolicyDecision{kubernetestool.ToolName: {Allowed: true}}}, Evidence: store, Journal: harness.NewMemoryJournal(), Sanitizer: integrationSanitizer(t), Clock: &integrationClock{}, IDs: &integrationIDs{}, Waiter: integrationWaiter{}, Evaluator: Engine{}})
	if err != nil {
		t.Fatal(err)
	}
	result, err := executor.Execute(context.Background(), validated)
	if err != nil {
		t.Fatal(err)
	}
	return result
}

// TestKubernetesObservedConditionUsesRealAssertionEngine proves functional failure and evidence links.
func TestKubernetesObservedConditionUsesRealAssertionEngine(t *testing.T) {
	reader := &assertionKubernetesReader{deployment: kubernetestool.Deployment{UID: "deployment-uid", ResourceVersion: "d1", Available: 0}}
	result := executeKubernetesAssertion(t, assertionKubernetesTool(t, reader, 1<<20), map[string]any{"operator": "eq", "path": "/observation/available_replicas", "expected": 1})
	if result.Status != planning.StepFailed || len(result.Steps) != 1 || len(result.Steps[0].AssertionResults) != 1 || result.Steps[0].AssertionResults[0].Status != planning.AssertionFailed || len(result.Steps[0].AssertionResults[0].EvidenceIDs) != 1 {
		t.Fatalf("real assertion result missing: %#v", result)
	}
}

// TestKubernetesAbsenceFailsRequiredExistenceWithRealEngine proves absence is data, not a model verdict.
func TestKubernetesAbsenceFailsRequiredExistenceWithRealEngine(t *testing.T) {
	reader := &assertionKubernetesReader{err: &kubernetestool.ReaderError{Category: kubernetestool.OutcomeAbsent}}
	result := executeKubernetesAssertion(t, assertionKubernetesTool(t, reader, 1<<20), map[string]any{"operator": "exists", "path": "/observation/available_replicas"})
	if result.Status != planning.StepFailed || result.Steps[0].AssertionResults[0].Status != planning.AssertionFailed {
		t.Fatalf("required absence did not fail: %#v", result)
	}
}

// TestKubernetesOperationalOutcomesBypassRealAssertions proves drift, truncation, and access remain inconclusive.
func TestKubernetesOperationalOutcomesBypassRealAssertions(t *testing.T) {
	types := []string{"Available", "Progressing", "ReplicaFailure", "Complete", "Failed", "SuccessCriteriaMet", "FailureTarget"}
	statuses := []string{"True", "False", "Unknown"}
	reasons := []string{"Available", "unknown"}
	conditions := []kubernetestool.Condition{}
	for _, conditionType := range types {
		for _, status := range statuses {
			for _, reason := range reasons {
				conditions = append(conditions, kubernetestool.Condition{Type: conditionType, Status: status, Reason: reason})
			}
		}
	}
	tests := []struct {
		name      string
		reader    *assertionKubernetesReader
		maxOutput int
	}{{name: "drift", reader: &assertionKubernetesReader{deployment: kubernetestool.Deployment{UID: "deployment-uid", ResourceVersion: "d1"}, drift: true}, maxOutput: 1 << 20}, {name: "truncation", reader: &assertionKubernetesReader{deployment: kubernetestool.Deployment{UID: "deployment-uid", ResourceVersion: "d1", Conditions: conditions}}, maxOutput: 1024}, {name: "access", reader: &assertionKubernetesReader{err: &kubernetestool.ReaderError{Category: kubernetestool.OutcomeAccessDenied}}, maxOutput: 1 << 20}}
	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			result := executeKubernetesAssertion(t, assertionKubernetesTool(t, testCase.reader, testCase.maxOutput), map[string]any{"operator": "eq", "path": "/outcome/complete", "expected": true})
			if result.Status != planning.StepInconclusive || len(result.Steps[0].AssertionResults) != 0 {
				t.Fatalf("operational outcome reached assertions: %#v", result)
			}
		})
	}
}
