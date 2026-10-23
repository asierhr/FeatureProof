package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/asierhr/featureProof/internal/assertion"
	"github.com/asierhr/featureProof/internal/contractcatalog"
	"github.com/asierhr/featureProof/internal/evidence"
	"github.com/asierhr/featureProof/internal/fixture"
	"github.com/asierhr/featureProof/internal/githubpr"
	"github.com/asierhr/featureProof/internal/harness"
	"github.com/asierhr/featureProof/internal/localvalidation"
	"github.com/asierhr/featureProof/internal/planmanifest"
	"github.com/asierhr/featureProof/internal/planning"
	"github.com/asierhr/featureProof/internal/specification"
	"github.com/asierhr/featureProof/internal/testidentity"
	"github.com/asierhr/featureProof/internal/validationworkflow"
)

// workflowIdentityClock supplies a trusted fixed instant to the product transport composition test.
type workflowIdentityClock struct{}

// Now returns the deterministic instant used for lease validity and observation duration.
func (workflowIdentityClock) Now() time.Time { return time.Unix(100, 0) }

// workflowIdentitySpecification returns one target-scoped bearer identity with a configured response credential path.
func workflowIdentitySpecification() specification.Specification {
	return specification.Specification{Version: 2, Target: specification.Target{ID: "deployed"}, Identities: []specification.TestIdentity{{ID: "customer", TargetID: "deployed", CredentialRef: "cred_customer", Mechanism: specification.AuthenticationMechanism{Type: "bearer", SensitiveJSONPaths: []string{"/body/session"}}, Operations: []specification.AuthorizedOperation{{Capability: "http.request", Method: "GET", Path: "/account"}}}}}
}

// TestWorkflowIdentityCompositionIsLateRestartableAndFailClosed proves trusted resolver wiring precedes inference without reading credential material eagerly.
func TestWorkflowIdentityCompositionIsLateRestartableAndFailClosed(t *testing.T) {
	spec := workflowIdentitySpecification()
	if _, _, _, err := composeWorkflowIdentities(spec, workflowConfig{}); err == nil {
		t.Fatal("missing trusted resolver mapping was accepted")
	}
	const environment = "FEATUREPROOF_TEST_CREDENTIAL_CANARY"
	previous, present := os.LookupEnv(environment)
	if err := os.Unsetenv(environment); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if present {
			_ = os.Setenv(environment, previous)
		} else {
			_ = os.Unsetenv(environment)
		}
	})
	config := workflowConfig{IdentityCredentialEnvironment: map[string]string{"cred_customer": environment}, IdentityObservationTimeoutMS: 100}
	resolvers, observer, timeout, err := composeWorkflowIdentities(spec, config)
	if err != nil || observer == nil || timeout != 100*time.Millisecond {
		t.Fatalf("identity composition = %v %v", timeout, err)
	}
	request := testidentity.ResolutionRequest{CredentialReference: "cred_customer", Mechanism: testidentity.MechanismBearer}
	if _, err = resolvers["cred_customer"].Resolve(context.Background(), request); err == nil {
		t.Fatal("missing late credential material was accepted")
	}
	if err = os.Setenv(environment, "CANARY-PRODUCT-IDENTITY"); err != nil {
		t.Fatal(err)
	}
	resolvers, observer, timeout, err = composeWorkflowIdentities(spec, config)
	if err != nil {
		t.Fatal(err)
	}
	catalog, _ := testidentity.NewCatalog(spec.Identities)
	transport, err := testidentity.NewTransport(testidentity.TransportConfig{Catalog: catalog, Resolver: resolvers["cred_customer"], Clock: workflowIdentityClock{}, Observer: observer, ObservationTimeout: timeout, MaxHeaderBytes: 1024})
	if err != nil {
		t.Fatal(err)
	}
	operation := testidentity.Operation{Capability: "http.request", TargetID: "deployed", Method: "GET", Path: "/account"}
	binding, _ := catalog.Bind("customer", operation)
	httpRequest, _ := http.NewRequest(http.MethodGet, "https://target.test/account", nil)
	_, err = transport.Exchange(context.Background(), binding, operation, httpRequest, "run", "repo", 1, func(injected *http.Request) (*http.Response, error) {
		if injected.Header.Get("Authorization") != "Bearer CANARY-PRODUCT-IDENTITY" {
			return nil, errors.New("late credential missing")
		}
		return &http.Response{StatusCode: http.StatusOK}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(config)
	if strings.Contains(string(raw), "CANARY-PRODUCT-IDENTITY") {
		t.Fatal("credential material entered trusted non-secret configuration")
	}
}

// TestWorkflowEffectiveRedactionRejectsIdentityAssertionsBeforePlanning proves explicit and generated validation share the sealed augmented policy.
func TestWorkflowEffectiveRedactionRejectsIdentityAssertionsBeforePlanning(t *testing.T) {
	spec := workflowIdentitySpecification()
	manifest := planmanifest.Manifest{Redaction: evidence.Config{MaxBodyBytes: 1024}}
	policy, err := workflowEffectiveRedaction(spec, &manifest)
	if err != nil {
		t.Fatal(err)
	}
	validator := assertion.DefinitionValidator{Policy: policy}
	for _, test := range []struct {
		name, path string
	}{
		{name: "generated header", path: "/headers/Authorization"},
		{name: "explicit response path", path: "/body/session"},
	} {
		t.Run(test.name, func(t *testing.T) {
			declaration := planning.AssertionDeclaration{ID: "auth", Definition: map[string]any{"operator": "eq", "path": test.path, "expected": "forbidden"}}
			if err := validator.ValidateDefinition(declaration, "assertions[0]"); err == nil {
				t.Fatalf("identity auth assertion %s accepted", test.path)
			}
		})
	}
	if len(manifest.Redaction.SensitiveJSONPaths) == 0 {
		t.Fatal("effective redaction was not sealed into the runtime manifest")
	}
}

// TestWorkflowIdentityCompositionRejectsInvalidTrustedConfiguration covers fail-closed composition and resolver boundary branches.
func TestWorkflowIdentityCompositionRejectsInvalidTrustedConfiguration(t *testing.T) {
	if resolvers, observer, timeout, err := composeWorkflowIdentities(specification.Specification{}, workflowConfig{}); err != nil || resolvers != nil || observer != nil || timeout != 0 {
		t.Fatalf("empty composition mismatch: %#v %#v %v %v", resolvers, observer, timeout, err)
	}
	if _, _, _, err := composeWorkflowIdentities(specification.Specification{}, workflowConfig{IdentityObservationTimeoutMS: 1}); err == nil {
		t.Fatal("unexpected identity configuration accepted")
	}
	spec := workflowIdentitySpecification()
	for _, config := range []workflowConfig{
		{IdentityCredentialEnvironment: map[string]string{"cred_customer": "ENV"}},
		{IdentityCredentialEnvironment: map[string]string{"cred_customer": "BAD=ENV"}, IdentityObservationTimeoutMS: 1},
		{IdentityCredentialEnvironment: map[string]string{"other": "ENV"}, IdentityObservationTimeoutMS: 1},
	} {
		if _, _, _, err := composeWorkflowIdentities(spec, config); err == nil {
			t.Fatalf("invalid trusted identity composition accepted: %#v", config)
		}
	}
	resolver := workflowEnvironmentCredentialResolver{reference: "expected", environment: "MISSING_FEATUREPROOF_IDENTITY"}
	if _, err := resolver.Resolve(context.Background(), testidentity.ResolutionRequest{CredentialReference: "other"}); err == nil {
		t.Fatal("mismatched credential reference accepted")
	}
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := resolver.Resolve(cancelled, testidentity.ResolutionRequest{CredentialReference: "expected"}); !errors.Is(err, context.Canceled) {
		t.Fatalf("resolver cancellation result: %v", err)
	}
	if _, err := workflowEffectiveRedaction(spec, nil); err == nil {
		t.Fatal("nil runtime manifest accepted")
	}
	first := workflowIdentityCompositionDigest(workflowConfig{IdentityCredentialEnvironment: map[string]string{"b": "B", "a": "A"}, IdentityObservationTimeoutMS: 1})
	second := workflowIdentityCompositionDigest(workflowConfig{IdentityCredentialEnvironment: map[string]string{"a": "A", "b": "B"}, IdentityObservationTimeoutMS: 1})
	if first != second || len(first) != 64 {
		t.Fatal("identity composition digest depended on map iteration order")
	}
}

// TestWorkflowIdentityCompositionUsesExactUniqueCredentialReferenceSet proves shared references need one resolver while extra mappings remain fail-closed.
func TestWorkflowIdentityCompositionUsesExactUniqueCredentialReferenceSet(t *testing.T) {
	spec := workflowIdentitySpecification()
	second := spec.Identities[0]
	second.ID = "administrator"
	spec.Identities = append(spec.Identities, second)
	config := workflowConfig{IdentityCredentialEnvironment: map[string]string{"cred_customer": "FEATUREPROOF_SHARED_IDENTITY"}, IdentityObservationTimeoutMS: 10}
	resolvers, observer, timeout, err := composeWorkflowIdentities(spec, config)
	if err != nil || len(resolvers) != 1 || resolvers["cred_customer"] == nil || observer == nil || timeout != 10*time.Millisecond {
		t.Fatalf("shared reference composition: resolvers=%d timeout=%s err=%v", len(resolvers), timeout, err)
	}
	config.IdentityCredentialEnvironment["extra"] = "FEATUREPROOF_EXTRA_IDENTITY"
	if _, _, _, err = composeWorkflowIdentities(spec, config); err == nil {
		t.Fatal("surplus credential mapping was accepted")
	}
	delete(config.IdentityCredentialEnvironment, "cred_customer")
	if _, _, _, err = composeWorkflowIdentities(spec, config); err == nil {
		t.Fatal("missing declared credential mapping was accepted")
	}
}

// workflowProductModelTransport returns one identity-selecting Responses API stream while counting real product inference calls.
type workflowProductModelTransport struct {
	calls atomic.Int32
	plan  string
	fault string
}

// workflowProductResolverSpy counts actual productive Resolve calls while delegating late material acquisition to the environment resolver.
type workflowProductResolverSpy struct {
	calls atomic.Int32
}

// Factory returns a composition resolver wrapper for one trusted reference and environment mapping.
func (s *workflowProductResolverSpy) Factory(reference, environment string) testidentity.CredentialResolver {
	return workflowProductCountingResolver{next: newWorkflowEnvironmentCredentialResolver(reference, environment), calls: &s.calls}
}

// workflowProductCountingResolver records the actual resolver boundary without deriving counts from audit events.
type workflowProductCountingResolver struct {
	next  testidentity.CredentialResolver
	calls *atomic.Int32
}

// Resolve counts and delegates one real late credential acquisition.
func (r workflowProductCountingResolver) Resolve(ctx context.Context, request testidentity.ResolutionRequest) (testidentity.CredentialLease, error) {
	r.calls.Add(1)
	return r.next.Resolve(ctx, request)
}

// RoundTrip derives the composition-selected plan ID from the bounded model request and returns one complete authenticated plan.
func (t *workflowProductModelTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	t.calls.Add(1)
	if request.URL.Host != "api.openai.com" || request.Header.Get("Authorization") != "Bearer MODEL-CREDENTIAL" {
		t.fault = "request"
		return nil, errors.New("unexpected product model request")
	}
	raw, err := io.ReadAll(request.Body)
	if err != nil {
		return nil, err
	}
	var envelope struct {
		Input []struct {
			Content string `json:"content"`
		} `json:"input"`
	}
	if json.Unmarshal(raw, &envelope) != nil || len(envelope.Input) != 1 {
		t.fault = "envelope:" + string(raw)
		return nil, errors.New("invalid product model request")
	}
	var untrusted struct {
		PlanID string `json:"plan_id"`
	}
	content := envelope.Input[0].Content
	if start := strings.IndexByte(content, '{'); start >= 0 {
		content = content[start:]
	}
	if json.Unmarshal([]byte(content), &untrusted) != nil || untrusted.PlanID == "" {
		t.fault = "content:" + envelope.Input[0].Content
		return nil, errors.New("missing product plan identity")
	}
	scenarios := []map[string]any{
		{"constraint_id": "c1", "scenario": "positive", "status": "planned"},
		{"constraint_id": "c1", "scenario": "negative", "status": "not_applicable", "reason": "no negative operation retained"},
		{"constraint_id": "c1", "scenario": "error", "status": "not_applicable", "reason": "no error operation retained"},
	}
	checks := []map[string]any{{
		"id":            "positive-1",
		"constraint_id": "c1",
		"scenario":      "positive",
		"capability":    "http.request",
		"operation":     map[string]any{"method": "GET", "path": "/pets"},
		"identity_id":   "customer",
		"steps": []any{map[string]any{
			"id":    "step",
			"tool":  "http.request",
			"input": map[string]any{},
			"assertions": []any{map[string]any{
				"id":         "assert",
				"definition": map[string]any{"operator": "exists", "path": "/status"},
			}},
		}},
		"evidence": []any{map[string]any{"step_id": "step", "assertion_ids": []string{"assert"}}},
	}}
	planRaw, _ := json.Marshal(map[string]any{"version": 1, "kind": "plan", "plan": map[string]any{"id": untrusted.PlanID, "scenarios": scenarios, "checks": checks}})
	plan := string(planRaw)
	t.plan = plan
	delta, _ := json.Marshal(plan)
	stream := "data: {\"type\":\"response.output_text.delta\",\"delta\":" + string(delta) + "}\n\n" + "data: {\"type\":\"response.completed\",\"response\":{\"usage\":{\"input_tokens\":4,\"output_tokens\":6}}}\n\n" + "data: [DONE]\n\n"
	return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": []string{"text/event-stream"}}, Body: io.NopCloser(strings.NewReader(stream))}, nil
}

// workflowProductSurfaceResult captures the public values and effect counters compared across workflow adapters.
type workflowProductSurfaceResult struct {
	identityID, bindingDigest, outcome     string
	code                                   int
	modelCalls, resolverCalls, targetCalls int32
}

// workflowFixtureContractFixture extends the productive OpenAPI authority with exact create and cleanup operations.
func workflowFixtureContractFixture() map[string]any {
	document := workflowContractFixture()
	paths := document["paths"].(map[string]any)
	security := []any{map[string]any{"oauth": []any{"read"}}}
	paths["/fixtures"] = map[string]any{"post": map[string]any{"operationId": "createFixture", "parameters": []any{map[string]any{"name": "Idempotency-Key", "in": "header", "required": true, "schema": map[string]any{"type": "string"}}}, "requestBody": map[string]any{"required": true, "content": map[string]any{"application/json": map[string]any{"schema": map[string]any{"type": "object"}}}}, "security": security, "responses": map[string]any{"201": map[string]any{"description": "created", "headers": map[string]any{}, "content": map[string]any{"application/json": map[string]any{"schema": map[string]any{"type": "object"}}}}}}}
	paths["/fixtures/{id}"] = map[string]any{"delete": map[string]any{"operationId": "deleteFixture", "parameters": []any{map[string]any{"name": "id", "in": "path", "required": true, "schema": map[string]any{"type": "string"}}}, "security": security, "responses": map[string]any{"204": map[string]any{"description": "deleted", "headers": map[string]any{}}, "404": map[string]any{"description": "absent", "headers": map[string]any{}}}}}
	return document
}

// composeProductIdentityWorkflow writes trusted v2 inputs and creates the real workflow composition around controlled model and target servers.
func composeProductIdentityWorkflow(t *testing.T, root string, targetURL string, modelTransport *workflowProductModelTransport) (*validationworkflow.CLI, string, string) {
	return composeProductIdentityWorkflowWithOptions(t, root, targetURL, modelTransport, nil, true)
}

// composeProductIdentityWorkflowWithOptions creates the fixture and injects a trusted resolver port plus explicit startup recovery behavior.
func composeProductIdentityWorkflowWithOptions(t *testing.T, root string, targetURL string, modelTransport *workflowProductModelTransport, spy *workflowProductResolverSpy, recoverPending bool) (*validationworkflow.CLI, string, string) {
	return composeProductWorkflowWithOptions(t, root, targetURL, modelTransport, spy, recoverPending, false)
}

// TestWorkflowFixtureRuntimeConfigFailsClosed proves every effect, retry, timeout, backoff, and lease limit comes from coherent trusted configuration.
func TestWorkflowFixtureRuntimeConfigFailsClosed(t *testing.T) {
	valid := workflowFixtureRuntimeConfig{MaxCaptureBytes: 1024, MaxToolCalls: 8, MaxRetries: 2, CreateRetries: 1, CleanupTimeoutMS: 2000, CleanupMaxCalls: 2, CleanupRetries: 1, RetryBackoffMS: 10, LeaseTTLMS: 3000}
	if err := valid.validate(); err != nil {
		t.Fatal(err)
	}
	invalid := []workflowFixtureRuntimeConfig{valid, valid, valid, valid, valid, valid, valid, valid, valid, valid, valid, valid}
	invalid[0].MaxCaptureBytes = 0
	invalid[1].MaxToolCalls = 0
	invalid[2].MaxRetries = 0
	invalid[3].CreateRetries = 0
	invalid[4].CleanupTimeoutMS = 0
	invalid[5].CleanupMaxCalls = 0
	invalid[6].CleanupRetries = 0
	invalid[7].RetryBackoffMS = 0
	invalid[8].LeaseTTLMS = 0
	invalid[9].CreateRetries = valid.MaxRetries + 1
	invalid[10].CleanupMaxCalls = valid.MaxToolCalls + 1
	invalid[11].LeaseTTLMS = valid.CleanupTimeoutMS
	for index := range invalid {
		if err := invalid[index].validate(); err == nil {
			t.Fatalf("invalid trusted fixture limit %d accepted", index)
		}
	}
	if _, _, err := composeWorkflowFixtures(context.Background(), t.TempDir(), "owner", specification.Specification{}, planmanifest.Manifest{}, localvalidation.RuntimeDependencies{}, contractcatalog.ReducedContext{}, workflowConfig{}, nil); err == nil {
		t.Fatal("fixture composition accepted missing trusted limits")
	}
}

// composeProductFixtureWorkflowWithOptions enables v3 fixtures in the same productive service graph.
func composeProductFixtureWorkflowWithOptions(t *testing.T, root string, targetURL string, modelTransport *workflowProductModelTransport, spy *workflowProductResolverSpy, recoverPending bool) (*validationworkflow.CLI, string, string) {
	return composeProductWorkflowWithOptions(t, root, targetURL, modelTransport, spy, recoverPending, true)
}

// composeProductWorkflowWithOptions creates controlled real composition with optional fixture lifecycle declarations.
func composeProductWorkflowWithOptions(t *testing.T, root string, targetURL string, modelTransport *workflowProductModelTransport, spy *workflowProductResolverSpy, recoverPending, withFixtures bool) (*validationworkflow.CLI, string, string) {
	t.Helper()
	document := workflowContractFixture()
	if withFixtures {
		document = workflowFixtureContractFixture()
	}
	configPath, specPath, contractPath := writeWorkflowCompositionFixture(t, root, document, "/pets", workflowProjectionFixture())
	spec := "version: 2\nfeature:\n  id: identity-product\n  description: product identity\ntarget:\n  id: deployed\n  base_url: " + targetURL + "\nconstraints:\n  - id: c1\n    description: selected contract\n    target_id: deployed\nlimits:\n  max_steps: 6\n  max_tool_calls: 3\n  max_retries: 0\nidentities:\n  - id: customer\n    target_id: deployed\n    credential_ref: cred_customer\n    mechanism:\n      type: bearer\n    operations:\n      - capability: http.request\n        method: GET\n        path: /pets\n"
	manifest := `{"version":2,"runtime":{"http":{"allowed_methods":["GET"],"allowed_request_headers":[],"allowed_response_headers":["Content-Type"],"follow_redirects_for_methods":[],"max_redirects":0,"timeout_ms":1000,"max_url_bytes":2048,"max_request_header_bytes":8192,"max_request_body_bytes":1024,"max_response_header_bytes":8192,"max_response_body_bytes":1024},"redaction":{"max_body_bytes":1024,"additional_sensitive_headers":[],"sensitive_json_paths":[],"model_sensitive_json_paths":[]},"policy":{"allowed_tools":["http.request"]}},"plans":[{"id":"p1","constraint_id":"c1","steps":[{"id":"request","tool":"http.request","identity_id":"customer","input":{"method":{"literal":"GET"},"path":{"literal":"/pets"}},"assertions":[{"id":"status","definition":{"operator":"eq","path":"/status","expected":200}}],"depends_on":[]}]}]}`
	if withFixtures {
		manifest = strings.Replace(manifest, `"version":2`, `"version":3`, 1)
		spec = strings.Replace(spec, "version: 2", "version: 3", 1)
		spec += "      - capability: http.request\n        method: POST\n        path: /fixtures\n      - capability: http.request\n        method: DELETE\n        path: /fixtures/{id}\n"
		spec += `fixtures:
  - id: account
    depends_on: []
    create:
      operation: {method: POST, path: /fixtures}
      identity_id: customer
      input:
        body:
          object:
            name: {literal: product-fixture}
      accepted_statuses: [201]
      idempotency: contract
    capture:
      - name: id
        path: [id]
        value_type: string
        max_bytes: 64
    cleanup:
      operation: {method: DELETE, path: "/fixtures/{id}"}
      identity_id: customer
      input:
        path_parameters:
          id:
            reference: {fixture_id: account, output: id, value_type: string}
      accepted_statuses: [204, 404]
      idempotency: contract
`
		manifest = strings.Replace(manifest, `"allowed_methods":["GET"]`, `"allowed_methods":["GET","POST","DELETE"]`, 1)
		manifest = strings.Replace(manifest, `"allowed_request_headers":[]`, `"allowed_request_headers":["Idempotency-Key"]`, 1)
	}
	if err := os.WriteFile(specPath, []byte(spec), 0o600); err != nil {
		t.Fatal(err)
	}
	manifestPath := filepath.Join(root, "runtime.json")
	if err := os.WriteFile(manifestPath, []byte(manifest), 0o600); err != nil {
		t.Fatal(err)
	}
	profilePath := filepath.Join(root, "profiles.json")
	profile := map[string]any{"profile": map[string]any{"ProfileID": "profile", "ClientID": "client", "Subject": "subject", "AccessToken": "MODEL-CREDENTIAL", "RefreshToken": "refresh", "IDToken": "id", "Scopes": []string{"chatgpt.tokens.use.direct"}, "ExpiresAt": time.Now().Add(time.Hour)}}
	profileRaw, _ := json.Marshal(profile)
	if err := os.WriteFile(profilePath, profileRaw, 0o600); err != nil {
		t.Fatal(err)
	}
	rewriteWorkflowConfig(t, configPath, func(value map[string]any) {
		value["repository_id"] = "7"
		value["target_url"] = targetURL
		value["allow_disabled_approval"] = true
		value["identity_credential_environment"] = map[string]any{"cred_customer": "FEATUREPROOF_PRODUCT_IDENTITY"}
		value["identity_observation_timeout_ms"] = 1000
		if withFixtures {
			value["contract_selections"] = map[string]any{"c1": []any{map[string]any{"method": "GET", "path": "/pets"}, map[string]any{"method": "POST", "path": "/fixtures"}, map[string]any{"method": "DELETE", "path": "/fixtures/{id}"}}}
			value["fixture_runtime"] = map[string]any{"max_capture_bytes": 1024, "max_tool_calls": 8, "max_retries": 2, "create_retries": 1, "cleanup_timeout_ms": 10000, "cleanup_max_calls": 2, "cleanup_retries": 1, "retry_backoff_ms": 10, "lease_ttl_ms": 11000}
		}
	})
	t.Setenv("FEATUREPROOF_WORKFLOW_CONFIG", configPath)
	t.Setenv("FEATUREPROOF_PRODUCT_IDENTITY", "PRODUCT-IDENTITY-CANARY")
	previousTransport := http.DefaultTransport
	http.DefaultTransport = modelTransport
	t.Cleanup(func() { http.DefaultTransport = previousTransport })
	factory := workflowCredentialResolverFactory(newWorkflowEnvironmentCredentialResolver)
	if spy != nil {
		factory = spy.Factory
	}
	cli, err := composeWorkflowCLIWithOptions(context.Background(), io.Discard, io.Discard, workflowCompositionOptions{credentialResolverFactory: factory, recoverPending: recoverPending})
	if err != nil {
		t.Fatal(err)
	}
	return cli, specPath, contractPath
}

// recomposeProductIdentityWorkflow recreates only the productive service graph over the existing durable fixture so recovery observes the exact sealed snapshot.
func recomposeProductIdentityWorkflow(t *testing.T, modelTransport *workflowProductModelTransport) *validationworkflow.CLI {
	return recomposeProductIdentityWorkflowWithOptions(t, modelTransport, nil, true)
}

// recomposeProductIdentityWorkflowWithOptions rebuilds the productive graph with an observable resolver and selectable automatic recovery.
func recomposeProductIdentityWorkflowWithOptions(t *testing.T, modelTransport *workflowProductModelTransport, spy *workflowProductResolverSpy, recoverPending bool) *validationworkflow.CLI {
	t.Helper()
	previousTransport := http.DefaultTransport
	http.DefaultTransport = modelTransport
	t.Cleanup(func() { http.DefaultTransport = previousTransport })
	factory := workflowCredentialResolverFactory(newWorkflowEnvironmentCredentialResolver)
	if spy != nil {
		factory = spy.Factory
	}
	cli, err := composeWorkflowCLIWithOptions(context.Background(), io.Discard, io.Discard, workflowCompositionOptions{credentialResolverFactory: factory, recoverPending: recoverPending})
	if err != nil {
		t.Fatal(err)
	}
	return cli
}

// productIdentityStartCommand builds the one safe semantic request reused by product parity, redelivery, and restart tests.
func productIdentityStartCommand(cli *validationworkflow.CLI, specPath, contractPath, key string, approval validationworkflow.ApprovalMode) validationworkflow.StartCommand {
	direct := cli.DirectTargets["target"]
	return validationworkflow.StartCommand{Principal: cli.Principal, IdempotencyKey: key, SpecificationRef: specPath, ContractRef: contractPath, Provider: "openai", Model: "selected-model", CompositionRef: cli.AuthProfiles["profile"], RuntimeProfile: "runtime", RepositoryID: "7", Revision: strings.Repeat("a", 40), Approval: approval, Target: validationworkflow.TargetSelection{Direct: &direct}}
}

// readProductIdentityBinding loads the public artifact emitted by real composition and returns only safe binding facts.
func readProductIdentityBinding(t *testing.T, root, handle string) (string, string) {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(root, ".featureproof", "workflows", "plans", handle+".plan.json"))
	if err != nil {
		t.Fatal(err)
	}
	var artifact validationworkflow.PlanArtifact
	if json.Unmarshal(raw, &artifact) != nil || len(artifact.Plans) != 1 || len(artifact.Plans[0].Steps) == 0 {
		t.Fatalf("invalid product artifact: %s", raw)
	}
	step := artifact.Plans[0].Steps[0]
	for _, candidate := range artifact.Plans[0].Steps[1:] {
		if candidate.IdentityID != step.IdentityID || candidate.OperationBindingSHA256 != step.OperationBindingSHA256 {
			t.Fatalf("product artifact changed identity binding across checks: %#v", artifact.Plans[0].Steps)
		}
	}
	return step.IdentityID, step.OperationBindingSHA256
}

// countProductIdentityAttempts reads the persisted safe journal and counts terminal authenticated attempt records.
func countProductIdentityAttempts(t *testing.T, root, handle string) int32 {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(root, ".featureproof", "runs", handle, "events.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	var count int32
	for _, line := range bytes.Split(raw, []byte{'\n'}) {
		if len(bytes.TrimSpace(line)) == 0 {
			continue
		}
		var event struct {
			Type string `json:"type"`
		}
		if json.Unmarshal(line, &event) != nil {
			t.Fatalf("invalid product event: %s", line)
		}
		if event.Type == "identity_attempt_terminal" {
			count++
		}
	}
	return count
}

// readProductIdentityTerminalEvents returns the ordered safe identity attempt facts from one durable run journal.
func readProductIdentityTerminalEvents(t *testing.T, root, handle string) []harness.Event {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(root, ".featureproof", "runs", handle, "events.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	var events []harness.Event
	for _, line := range bytes.Split(raw, []byte{'\n'}) {
		if len(bytes.TrimSpace(line)) == 0 {
			continue
		}
		var encoded struct {
			Type                   harness.EventType `json:"type"`
			IdentityID             string            `json:"identity_id"`
			OperationBindingSHA256 string            `json:"operation_binding_sha256"`
			Attempt                int               `json:"attempt"`
			OutcomeCategory        string            `json:"outcome_category"`
		}
		if json.Unmarshal(line, &encoded) != nil {
			t.Fatalf("invalid product event: %s", line)
		}
		if encoded.Type == harness.EventIdentityAttemptTerminal {
			events = append(events, harness.Event{Type: encoded.Type, IdentityID: encoded.IdentityID, OperationBindingSHA256: encoded.OperationBindingSHA256, Attempt: encoded.Attempt, OutcomeCategory: encoded.OutcomeCategory})
		}
	}
	return events
}

// assertProductDurableFilesExclude walks generated workflow, journal, evidence, report, and package files for forbidden identity data.
func assertProductDurableFilesExclude(t *testing.T, root string, forbidden ...string) {
	t.Helper()
	err := filepath.Walk(filepath.Join(root, ".featureproof"), func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return err
		}
		raw, readErr := os.ReadFile(path)
		if readErr != nil {
			return readErr
		}
		for _, value := range forbidden {
			if bytes.Contains(raw, []byte(value)) {
				t.Fatalf("forbidden identity data %q persisted in %s", value, path)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

// TestProductIdentityWorkflowParityReachesTerminalAcrossAllSurfaces executes one authenticated semantic snapshot through real composition for every adapter.
func TestProductIdentityWorkflowParityReachesTerminalAcrossAllSurfaces(t *testing.T) {
	results := map[string]workflowProductSurfaceResult{}
	for _, surface := range []string{"api", "cli", "mcp", "github"} {
		t.Run(surface, func(t *testing.T) {
			root := t.TempDir()
			t.Chdir(root)
			var targetCalls atomic.Int32
			target := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
				targetCalls.Add(1)
				if request.URL.Path != "/pets" || request.Header.Get("Authorization") != "Bearer PRODUCT-IDENTITY-CANARY" {
					t.Fatalf("authenticated target request path=%q auth=%q", request.URL.Path, request.Header.Get("Authorization"))
				}
				writer.Header().Set("Content-Type", "application/json")
				writer.WriteHeader(http.StatusOK)
				_, _ = writer.Write([]byte(`{"ok":true}`))
			}))
			defer target.Close()
			modelTransport := &workflowProductModelTransport{}
			resolverSpy := &workflowProductResolverSpy{}
			cli, specPath, contractPath := composeProductIdentityWorkflowWithOptions(t, root, target.URL, modelTransport, resolverSpy, true)
			command := productIdentityStartCommand(cli, specPath, contractPath, "product-"+surface, validationworkflow.ApprovalDisabled)
			handle, code := "", 0
			switch surface {
			case "api":
				view, err := cli.Service.Start(context.Background(), command)
				if err != nil {
					t.Fatal(err)
				}
				handle = view.Handle
			case "cli":
				var output bytes.Buffer
				args := []string{"start", "--spec", specPath, "--contract", contractPath, "--provider", "openai", "--model", "selected-model", "--profile", "profile", "--runtime-profile", "runtime", "--repository", "7", "--revision", strings.Repeat("a", 40), "--approval", "disabled", "--idempotency-key", command.IdempotencyKey, "--target", "target"}
				code = cli.Run(context.Background(), args, &output)
				var view validationworkflow.WorkflowView
				if code != 0 || json.Unmarshal(output.Bytes(), &view) != nil {
					t.Fatalf("CLI code=%d output=%s", code, output.String())
				}
				handle = view.Handle
			case "mcp":
				server := validationworkflow.MCPServer{Service: cli.Service, Principal: cli.Principal, AuthProfiles: cli.AuthProfiles, DirectTargets: cli.DirectTargets}
				arguments, _ := json.Marshal(map[string]any{"idempotency_key": command.IdempotencyKey, "specification_ref": specPath, "contract_ref": contractPath, "provider": "openai", "model": "selected-model", "auth_profile": "profile", "runtime_profile": "runtime", "repository_id": "7", "revision": strings.Repeat("a", 40), "approval": "disabled", "target_alias": "target"})
				response := server.Handle(context.Background(), validationworkflow.MCPRequest{Version: validationworkflow.ContractVersion, Operation: "workflow.start", Arguments: arguments})
				view, ok := response.Value.(validationworkflow.WorkflowView)
				if response.Error != "" || !ok {
					t.Fatalf("MCP response=%#v", response)
				}
				handle = view.Handle
			case "github":
				command.Approval = validationworkflow.ApprovalRequired
				snapshot, err := githubpr.SealSnapshot(githubpr.PullRequestSnapshot{Version: 1, InstallationID: 1, RepositoryID: 7, Number: 2, AuthorID: 9, State: "open", HeadOID: strings.Repeat("a", 40), HeadRepositoryID: 7})
				if err != nil {
					t.Fatal(err)
				}
				gateway := validationworkflow.GitHubGateway{Service: cli.Service, Principal: cli.Principal, Commands: map[string]validationworkflow.StartCommand{"default": command}}
				state, err := gateway.Submit(context.Background(), githubpr.Selection{Alias: "default"}, snapshot, command.IdempotencyKey)
				if err != nil {
					t.Fatal(err)
				}
				handle = state.RunHandle
				approval, _ := cli.Service.ApprovalView(context.Background(), handle)
				if _, err = cli.Service.Decide(context.Background(), validationworkflow.DecisionCommand{Principal: cli.Principal, Handle: handle, DecisionID: "approve-github", Revision: approval.Revision, Action: validationworkflow.DecisionApprove}); err != nil {
					t.Fatal(err)
				}
			}
			result, err := cli.Service.Result(context.Background(), handle)
			if err != nil || result.View.Outcome == nil || result.Package == nil {
				outcome := ""
				if result.View.Outcome != nil {
					outcome = string(*result.View.Outcome)
				}
				t.Fatalf("terminal outcome=%s result=%#v err=%v model=%d target=%d plan=%s fault=%s", outcome, result, err, modelTransport.calls.Load(), targetCalls.Load(), modelTransport.plan, modelTransport.fault)
			}
			identityID, bindingDigest := readProductIdentityBinding(t, root, handle)
			results[surface] = workflowProductSurfaceResult{identityID: identityID, bindingDigest: bindingDigest, outcome: string(*result.View.Outcome), code: code, modelCalls: modelTransport.calls.Load(), resolverCalls: resolverSpy.calls.Load(), targetCalls: targetCalls.Load()}
		})
	}
	var expected workflowProductSurfaceResult
	for _, surface := range []string{"api", "cli", "mcp", "github"} {
		result := results[surface]
		if result.identityID != "customer" || len(result.bindingDigest) != 64 || result.outcome != string(validationworkflow.OutcomePassed) || result.code != 0 || result.modelCalls != 1 || result.resolverCalls != 1 || result.targetCalls != 1 {
			t.Fatalf("%s parity result=%#v", surface, result)
		}
		if expected.identityID == "" {
			expected = result
		} else if result.identityID != expected.identityID || result.bindingDigest != expected.bindingDigest || result.outcome != expected.outcome || result.code != expected.code || result.modelCalls != expected.modelCalls || result.resolverCalls != expected.resolverCalls || result.targetCalls != expected.targetCalls {
			t.Fatalf("surface mismatch %s=%#v expected=%#v", surface, result, expected)
		}
	}
}

// TestProductWorkflowMCPEntrypointComposesFunctionalAliases proves the executable forwards trusted remote flow selections into the shared MCP server.
func TestProductWorkflowMCPEntrypointComposesFunctionalAliases(t *testing.T) {
	root := t.TempDir()
	t.Chdir(root)
	target := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	defer target.Close()
	modelTransport := &workflowProductModelTransport{}
	_, _, _ = composeProductIdentityWorkflowWithOptions(t, root, target.URL, modelTransport, nil, true)
	configPath := filepath.Join(root, "workflow.json")
	rewriteWorkflowConfig(t, configPath, func(value map[string]any) {
		value["functional_flow_aliases"] = map[string]any{"approved": strings.Repeat("a", 64)}
	})
	request := `{"jsonrpc":"2.0","id":1,"method":"tools/list","params":{"protocolVersion":"2026-07-28"}}` + "\n"
	var stdout, stderr bytes.Buffer
	if code := runWithContext(context.Background(), []string{"workflow", "mcp", "serve"}, strings.NewReader(request), &stdout, &stderr); code != 0 || !strings.Contains(stdout.String(), "featureproof.workflow.start") {
		t.Fatalf("code=%d stdout=%s stderr=%s", code, stdout.String(), stderr.String())
	}
}

// TestProductFixtureLifecycleParityAcrossAllSurfaces exercises real v3 create, primary validation, cleanup, and safe projection through every adapter.
func TestProductFixtureLifecycleParityAcrossAllSurfaces(t *testing.T) {
	for _, surface := range []string{"api", "cli", "mcp", "github"} {
		t.Run(surface, func(t *testing.T) {
			root := t.TempDir()
			t.Chdir(root)
			var targetCalls atomic.Int32
			target := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
				targetCalls.Add(1)
				if request.Header.Get("Authorization") != "Bearer PRODUCT-IDENTITY-CANARY" {
					t.Fatalf("missing rotated identity at %s", request.URL.Path)
				}
				switch {
				case request.Method == http.MethodPost && request.URL.Path == "/fixtures":
					if request.Header.Get("Idempotency-Key") == "" {
						t.Fatal("create omitted contract-declared idempotency key")
					}
					writer.Header().Set("Content-Type", "application/json")
					writer.WriteHeader(http.StatusCreated)
					_, _ = writer.Write([]byte(`{"id":"fixture-private-canary"}`))
				case request.Method == http.MethodGet && request.URL.Path == "/pets":
					writer.Header().Set("Content-Type", "application/json")
					_, _ = writer.Write([]byte(`{"ok":true}`))
				case request.Method == http.MethodDelete && request.URL.Path == "/fixtures/fixture-private-canary":
					if request.Header.Get("Idempotency-Key") != "" {
						t.Fatal("cleanup received header absent from its operation contract")
					}
					writer.WriteHeader(http.StatusNoContent)
				default:
					t.Fatalf("unexpected fixture request %s %s", request.Method, request.URL.Path)
				}
			}))
			defer target.Close()
			modelTransport := &workflowProductModelTransport{}
			resolverSpy := &workflowProductResolverSpy{}
			cli, specPath, contractPath := composeProductFixtureWorkflowWithOptions(t, root, target.URL, modelTransport, resolverSpy, true)
			command := productIdentityStartCommand(cli, specPath, contractPath, "fixture-"+surface, validationworkflow.ApprovalDisabled)
			handle := ""
			switch surface {
			case "api":
				view, err := cli.Service.Start(context.Background(), command)
				if err != nil {
					t.Fatal(err)
				}
				handle = view.Handle
			case "cli":
				var output bytes.Buffer
				args := []string{"start", "--spec", specPath, "--contract", contractPath, "--provider", "openai", "--model", "selected-model", "--profile", "profile", "--runtime-profile", "runtime", "--repository", "7", "--revision", strings.Repeat("a", 40), "--approval", "disabled", "--idempotency-key", command.IdempotencyKey, "--target", "target"}
				if code := cli.Run(context.Background(), args, &output); code != 0 {
					t.Fatalf("CLI code=%d output=%s", code, output.String())
				}
				var view validationworkflow.WorkflowView
				if json.Unmarshal(output.Bytes(), &view) != nil {
					t.Fatal("invalid CLI projection")
				}
				handle = view.Handle
			case "mcp":
				arguments, _ := json.Marshal(map[string]any{"idempotency_key": command.IdempotencyKey, "specification_ref": specPath, "contract_ref": contractPath, "provider": "openai", "model": "selected-model", "auth_profile": "profile", "runtime_profile": "runtime", "repository_id": "7", "revision": strings.Repeat("a", 40), "approval": "disabled", "target_alias": "target"})
				response := (validationworkflow.MCPServer{Service: cli.Service, Principal: cli.Principal, AuthProfiles: cli.AuthProfiles, DirectTargets: cli.DirectTargets}).Handle(context.Background(), validationworkflow.MCPRequest{Version: validationworkflow.ContractVersion, Operation: "workflow.start", Arguments: arguments})
				view, ok := response.Value.(validationworkflow.WorkflowView)
				if response.Error != "" || !ok {
					t.Fatalf("MCP=%#v", response)
				}
				handle = view.Handle
			case "github":
				command.Approval = validationworkflow.ApprovalRequired
				snapshot, _ := githubpr.SealSnapshot(githubpr.PullRequestSnapshot{Version: 1, InstallationID: 1, RepositoryID: 7, Number: 2, AuthorID: 9, State: "open", HeadOID: strings.Repeat("a", 40), HeadRepositoryID: 7})
				state, err := (validationworkflow.GitHubGateway{Service: cli.Service, Principal: cli.Principal, Commands: map[string]validationworkflow.StartCommand{"default": command}}).Submit(context.Background(), githubpr.Selection{Alias: "default"}, snapshot, command.IdempotencyKey)
				if err != nil {
					t.Fatal(err)
				}
				handle = state.RunHandle
				approval, _ := cli.Service.ApprovalView(context.Background(), handle)
				if _, err = cli.Service.Decide(context.Background(), validationworkflow.DecisionCommand{Principal: cli.Principal, Handle: handle, DecisionID: "fixture-approve", Revision: approval.Revision, Action: validationworkflow.DecisionApprove}); err != nil {
					t.Fatal(err)
				}
			}
			result, err := cli.Service.Result(context.Background(), handle)
			if err != nil || result.View.Fixtures == nil || result.View.Fixtures.PreparationStatus != fixture.PreparationPrepared || result.View.Fixtures.Cleanup.Status != fixture.CleanupSucceeded || len(result.View.Fixtures.Fixtures) != 1 || len(result.View.Fixtures.Fixtures[0].ConfirmedCaptureNames) != 1 || targetCalls.Load() != 3 || resolverSpy.calls.Load() != 3 {
				t.Fatalf("result=%#v calls=%d resolver=%d err=%v", result, targetCalls.Load(), resolverSpy.calls.Load(), err)
			}
			assertProductDurableFilesExclude(t, root, "PRODUCT-IDENTITY-CANARY")
		})
	}
}

// TestProductFixtureRetryAndCleanupResolveRotatedCredentials proves credentials are reacquired at each fixture and primary attempt.
func TestProductFixtureRetryAndCleanupResolveRotatedCredentials(t *testing.T) {
	root := t.TempDir()
	t.Chdir(root)
	var calls atomic.Int32
	target := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		attempt := calls.Add(1)
		wanted := "Bearer PRODUCT-IDENTITY-CANARY"
		if attempt > 1 {
			wanted = "Bearer ROTATED-PRODUCT-IDENTITY"
		}
		if request.Header.Get("Authorization") != wanted {
			t.Fatalf("attempt %d credential=%q wanted=%q", attempt, request.Header.Get("Authorization"), wanted)
		}
		switch request.Method {
		case http.MethodPost:
			_ = os.Setenv("FEATUREPROOF_PRODUCT_IDENTITY", "ROTATED-PRODUCT-IDENTITY")
			writer.Header().Set("Content-Type", "application/json")
			writer.WriteHeader(http.StatusCreated)
			_, _ = writer.Write([]byte(`{"id":"rotated-id"}`))
		case http.MethodGet:
			writer.Header().Set("Content-Type", "application/json")
			_, _ = writer.Write([]byte(`{"ok":true}`))
		case http.MethodDelete:
			writer.WriteHeader(http.StatusNoContent)
		}
	}))
	defer target.Close()
	modelTransport := &workflowProductModelTransport{}
	spy := &workflowProductResolverSpy{}
	cli, specPath, contractPath := composeProductFixtureWorkflowWithOptions(t, root, target.URL, modelTransport, spy, true)
	view, err := cli.Service.Start(context.Background(), productIdentityStartCommand(cli, specPath, contractPath, "fixture-rotation", validationworkflow.ApprovalDisabled))
	if err != nil {
		t.Fatal(err)
	}
	result, err := cli.Service.Result(context.Background(), view.Handle)
	if err != nil || result.View.Fixtures == nil || result.View.Fixtures.Cleanup.Status != fixture.CleanupSucceeded || calls.Load() != 3 || spy.calls.Load() != 3 {
		t.Fatalf("result=%#v calls=%d resolver=%d err=%v", result, calls.Load(), spy.calls.Load(), err)
	}
}

// TestProductIdentityFailureParityAcrossAllSurfaces proves one missing late credential has equivalent semantics and surface-native status projections.
func TestProductIdentityFailureParityAcrossAllSurfaces(t *testing.T) {
	type failureResult struct {
		identityID, bindingDigest, category, nativeCode string
		modelCalls, resolverCalls, targetCalls          int32
	}
	results := map[string]failureResult{}
	for _, surface := range []string{"api", "cli", "mcp", "github"} {
		t.Run(surface, func(t *testing.T) {
			root := t.TempDir()
			t.Chdir(root)
			var targetCalls atomic.Int32
			target := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { targetCalls.Add(1) }))
			defer target.Close()
			modelTransport := &workflowProductModelTransport{}
			resolverSpy := &workflowProductResolverSpy{}
			cli, specPath, contractPath := composeProductIdentityWorkflowWithOptions(t, root, target.URL, modelTransport, resolverSpy, true)
			if err := os.Unsetenv("FEATUREPROOF_PRODUCT_IDENTITY"); err != nil {
				t.Fatal(err)
			}
			command := productIdentityStartCommand(cli, specPath, contractPath, "product-failure-"+surface, validationworkflow.ApprovalDisabled)
			handle, nativeCode := "", ""
			switch surface {
			case "api":
				view, err := cli.Service.Start(context.Background(), command)
				if err != nil {
					t.Fatal(err)
				}
				handle, nativeCode = view.Handle, "api:"+string(*view.Outcome)
			case "cli":
				var output bytes.Buffer
				args := []string{"start", "--spec", specPath, "--contract", contractPath, "--provider", "openai", "--model", "selected-model", "--profile", "profile", "--runtime-profile", "runtime", "--repository", "7", "--revision", strings.Repeat("a", 40), "--approval", "disabled", "--idempotency-key", command.IdempotencyKey, "--target", "target"}
				code := cli.Run(context.Background(), args, &output)
				var view validationworkflow.WorkflowView
				if json.Unmarshal(output.Bytes(), &view) != nil || view.Outcome == nil {
					t.Fatalf("CLI code=%d output=%s", code, output.String())
				}
				handle, nativeCode = view.Handle, "cli:"+strconv.Itoa(code)+":"+string(*view.Outcome)
			case "mcp":
				server := validationworkflow.MCPServer{Service: cli.Service, Principal: cli.Principal, AuthProfiles: cli.AuthProfiles, DirectTargets: cli.DirectTargets}
				arguments, _ := json.Marshal(map[string]any{"idempotency_key": command.IdempotencyKey, "specification_ref": specPath, "contract_ref": contractPath, "provider": "openai", "model": "selected-model", "auth_profile": "profile", "runtime_profile": "runtime", "repository_id": "7", "revision": strings.Repeat("a", 40), "approval": "disabled", "target_alias": "target"})
				response := server.Handle(context.Background(), validationworkflow.MCPRequest{Version: validationworkflow.ContractVersion, Operation: "workflow.start", Arguments: arguments})
				view, ok := response.Value.(validationworkflow.WorkflowView)
				if !ok || view.Outcome == nil {
					t.Fatalf("MCP response=%#v", response)
				}
				handle, nativeCode = view.Handle, "mcp:"+string(response.Error)+":"+string(*view.Outcome)
			case "github":
				command.Approval = validationworkflow.ApprovalRequired
				snapshot, _ := githubpr.SealSnapshot(githubpr.PullRequestSnapshot{Version: 1, InstallationID: 1, RepositoryID: 7, Number: 2, AuthorID: 9, State: "open", HeadOID: strings.Repeat("a", 40), HeadRepositoryID: 7})
				gateway := validationworkflow.GitHubGateway{Service: cli.Service, Principal: cli.Principal, Commands: map[string]validationworkflow.StartCommand{"default": command}}
				state, err := gateway.Submit(context.Background(), githubpr.Selection{Alias: "default"}, snapshot, command.IdempotencyKey)
				if err != nil {
					t.Fatal(err)
				}
				handle = state.RunHandle
				approval, _ := cli.Service.ApprovalView(context.Background(), handle)
				if _, err = cli.Service.Decide(context.Background(), validationworkflow.DecisionCommand{Principal: cli.Principal, Handle: handle, DecisionID: "approve-failure", Revision: approval.Revision, Action: validationworkflow.DecisionApprove}); err != nil {
					t.Fatal(err)
				}
				state, err = gateway.Status(context.Background(), handle)
				if err != nil {
					t.Fatal(err)
				}
				nativeCode = "github:" + state.Outcome
			}
			result, err := cli.Service.Result(context.Background(), handle)
			if err != nil || result.View.Outcome == nil {
				t.Fatalf("failure result=%#v err=%v", result, err)
			}
			identityID, bindingDigest := readProductIdentityBinding(t, root, handle)
			results[surface] = failureResult{identityID: identityID, bindingDigest: bindingDigest, category: string(*result.View.Outcome), nativeCode: nativeCode, modelCalls: modelTransport.calls.Load(), resolverCalls: resolverSpy.calls.Load(), targetCalls: targetCalls.Load()}
		})
	}
	expectedCodes := map[string]string{"api": "api:inconclusive", "cli": "cli:0:inconclusive", "mcp": "mcp::inconclusive", "github": "github:inconclusive"}
	binding := ""
	for _, surface := range []string{"api", "cli", "mcp", "github"} {
		result := results[surface]
		if result.identityID != "customer" || result.category != "inconclusive" || result.nativeCode != expectedCodes[surface] || result.modelCalls != 1 || result.resolverCalls != 1 || result.targetCalls != 0 {
			t.Fatalf("%s failure parity=%#v", surface, result)
		}
		if binding == "" {
			binding = result.bindingDigest
		} else if result.bindingDigest != binding {
			t.Fatalf("%s binding=%s expected=%s", surface, result.bindingDigest, binding)
		}
	}
}

// TestProductIdentityCompositionStopsBeforeInferenceWhenResolverCoverageIsMissing proves productive startup fails before model, resolver, or network effects.
func TestProductIdentityCompositionStopsBeforeInferenceWhenResolverCoverageIsMissing(t *testing.T) {
	root := t.TempDir()
	t.Chdir(root)
	var targetCalls atomic.Int32
	target := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { targetCalls.Add(1) }))
	defer target.Close()
	modelTransport := &workflowProductModelTransport{}
	_, _, _ = composeProductIdentityWorkflow(t, root, target.URL, modelTransport)
	configPath := filepath.Join(root, "workflow.json")
	rewriteWorkflowConfig(t, configPath, func(value map[string]any) {
		value["identity_credential_environment"] = map[string]any{}
	})
	if _, err := composeWorkflowCLI(context.Background(), io.Discard, io.Discard); err == nil {
		t.Fatal("missing productive credential composition was accepted")
	}
	if modelTransport.calls.Load() != 0 || targetCalls.Load() != 0 {
		t.Fatalf("missing composition effects: model=%d target=%d", modelTransport.calls.Load(), targetCalls.Load())
	}
}

// TestProductWorkflowCompositionPersistsTrustedMemoryBinding proves the real workflow entrypoint owns query authority and survives restart.
func TestProductWorkflowCompositionPersistsTrustedMemoryBinding(t *testing.T) {
	root := t.TempDir()
	t.Chdir(root)
	t.Setenv("FEATUREPROOF_MEMORY_ROOT", filepath.Join(root, ".featureproof", "memory"))
	t.Setenv("FEATUREPROOF_MEMORY_TRUST_ROOT", filepath.Join(root, ".featureproof-trust", "memory"))
	installProjectMemoryWitness(t)
	t.Setenv("FEATUREPROOF_MEMORY_INTEGRITY_KEY", "0123456789abcdef0123456789abcdef")
	t.Setenv("FEATUREPROOF_MEMORY_PRINCIPAL", "memory-principal")
	t.Setenv("FEATUREPROOF_MEMORY_CREDENTIAL", "memory-credential")
	t.Setenv("FEATUREPROOF_MEMORY_PROJECT", "project-1")
	t.Setenv("FEATUREPROOF_MEMORY_REPOSITORY", "7")
	t.Setenv("FEATUREPROOF_MEMORY_COMMIT", strings.Repeat("a", 40))
	t.Setenv("FEATUREPROOF_MEMORY_CONFIGURATION_DIGEST", strings.Repeat("b", 64))
	target := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) { writer.WriteHeader(http.StatusOK) }))
	defer target.Close()
	modelTransport := &workflowProductModelTransport{}
	cli, specPath, contractPath := composeProductIdentityWorkflow(t, root, target.URL, modelTransport)
	command := productIdentityStartCommand(cli, specPath, contractPath, "memory-product-vertical", validationworkflow.ApprovalDisabled)
	view, err := cli.Service.Start(context.Background(), command)
	if err != nil || view.Phase != validationworkflow.PhaseTerminal {
		t.Fatalf("workflow=%#v err=%v", view, err)
	}
	raw, err := os.ReadFile(filepath.Join(root, ".featureproof", "workflows", "plans", view.Handle+".plan.json"))
	var artifact validationworkflow.PlanArtifact
	if err != nil || json.Unmarshal(raw, &artifact) != nil || artifact.MemoryBindingDigest == "" {
		t.Fatalf("trusted memory binding was not persisted: %#v %v", artifact, err)
	}
	restarted := recomposeProductIdentityWorkflow(t, modelTransport)
	result, err := restarted.Service.Result(context.Background(), view.Handle)
	if err != nil || result.View.Phase != validationworkflow.PhaseTerminal {
		t.Fatalf("restart result=%#v err=%v", result, err)
	}
}

// TestProductIdentityRedeliveryAndRestartRecoveryResolveFreshMaterial proves terminal redelivery is idempotent and pending approval survives service recreation with a new resolver composition.
func TestProductIdentityRedeliveryAndRestartRecoveryResolveFreshMaterial(t *testing.T) {
	t.Run("terminal redelivery", func(t *testing.T) {
		root := t.TempDir()
		t.Chdir(root)
		var targetCalls atomic.Int32
		target := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
			targetCalls.Add(1)
			if request.Header.Get("Authorization") != "Bearer PRODUCT-IDENTITY-CANARY" {
				t.Fatal("redelivery target received an unexpected credential")
			}
			writer.WriteHeader(http.StatusOK)
		}))
		defer target.Close()
		firstModel := &workflowProductModelTransport{}
		resolverSpy := &workflowProductResolverSpy{}
		first, specPath, contractPath := composeProductIdentityWorkflowWithOptions(t, root, target.URL, firstModel, resolverSpy, true)
		command := productIdentityStartCommand(first, specPath, contractPath, "product-redelivery", validationworkflow.ApprovalDisabled)
		initial, err := first.Service.Start(context.Background(), command)
		if err != nil || initial.Outcome == nil || *initial.Outcome != validationworkflow.OutcomePassed {
			t.Fatalf("initial terminal workflow=%#v err=%v", initial, err)
		}
		secondModel := &workflowProductModelTransport{}
		second := recomposeProductIdentityWorkflowWithOptions(t, secondModel, resolverSpy, true)
		redeliveryCommand := productIdentityStartCommand(second, specPath, contractPath, command.IdempotencyKey, validationworkflow.ApprovalDisabled)
		redelivered, err := second.Service.Start(context.Background(), redeliveryCommand)
		if err != nil || redelivered.Handle != initial.Handle || redelivered.Outcome == nil || *redelivered.Outcome != validationworkflow.OutcomePassed {
			t.Fatalf("redelivered workflow=%#v err=%v", redelivered, err)
		}
		if firstModel.calls.Load() != 1 || secondModel.calls.Load() != 0 || targetCalls.Load() != 1 || resolverSpy.calls.Load() != 1 || countProductIdentityAttempts(t, root, initial.Handle) != 1 {
			t.Fatalf("redelivery duplicated effects: first_model=%d second_model=%d resolver=%d target=%d", firstModel.calls.Load(), secondModel.calls.Load(), resolverSpy.calls.Load(), targetCalls.Load())
		}
	})

	t.Run("pending approval restart", func(t *testing.T) {
		root := t.TempDir()
		t.Chdir(root)
		var targetCalls atomic.Int32
		target := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
			targetCalls.Add(1)
			if request.Header.Get("Authorization") != "Bearer PRODUCT-IDENTITY-AFTER-RESTART" {
				t.Fatalf("restart did not use fresh credential material: %q", request.Header.Get("Authorization"))
			}
			writer.WriteHeader(http.StatusOK)
		}))
		defer target.Close()
		firstModel := &workflowProductModelTransport{}
		firstSpy := &workflowProductResolverSpy{}
		first, specPath, contractPath := composeProductIdentityWorkflowWithOptions(t, root, target.URL, firstModel, firstSpy, true)
		command := productIdentityStartCommand(first, specPath, contractPath, "product-restart", validationworkflow.ApprovalRequired)
		pending, err := first.Service.Start(context.Background(), command)
		if err != nil || pending.Phase != validationworkflow.PhaseAwaitingApproval || targetCalls.Load() != 0 {
			t.Fatalf("pending workflow=%#v target=%d err=%v", pending, targetCalls.Load(), err)
		}
		t.Setenv("FEATUREPROOF_PRODUCT_IDENTITY", "PRODUCT-IDENTITY-AFTER-RESTART")
		secondModel := &workflowProductModelTransport{}
		secondSpy := &workflowProductResolverSpy{}
		second := recomposeProductIdentityWorkflowWithOptions(t, secondModel, secondSpy, false)
		if err = second.Service.Recover(context.Background()); err != nil {
			t.Fatal(err)
		}
		if secondModel.calls.Load() != 0 || secondSpy.calls.Load() != 0 || targetCalls.Load() != 0 {
			t.Fatalf("recovery performed execution effects: model=%d resolver=%d target=%d", secondModel.calls.Load(), secondSpy.calls.Load(), targetCalls.Load())
		}
		approval, err := second.Service.ApprovalView(context.Background(), pending.Handle)
		if err != nil {
			t.Fatal(err)
		}
		terminal, err := second.Service.Decide(context.Background(), validationworkflow.DecisionCommand{Principal: second.Principal, Handle: pending.Handle, DecisionID: "approve-after-restart", Revision: approval.Revision, Action: validationworkflow.DecisionApprove})
		if err != nil || terminal.Outcome == nil || *terminal.Outcome != validationworkflow.OutcomePassed {
			t.Fatalf("recovered terminal workflow=%#v err=%v", terminal, err)
		}
		if firstModel.calls.Load() != 1 || firstSpy.calls.Load() != 0 || secondModel.calls.Load() != 0 || secondSpy.calls.Load() != 1 || targetCalls.Load() != 1 || countProductIdentityAttempts(t, root, pending.Handle) != 1 {
			t.Fatalf("restart effects: first_model=%d first_resolver=%d second_model=%d second_resolver=%d target=%d", firstModel.calls.Load(), firstSpy.calls.Load(), secondModel.calls.Load(), secondSpy.calls.Load(), targetCalls.Load())
		}
	})
}

// TestProductIdentityRotationPreservesHistoricalJournalWithoutSecrets proves two productive runs resolve fresh material while retaining stable append-only identity facts.
func TestProductIdentityRotationPreservesHistoricalJournalWithoutSecrets(t *testing.T) {
	root := t.TempDir()
	t.Chdir(root)
	const firstCanary = "PRODUCT-ROTATION-FIRST-CANARY"
	const secondCanary = "PRODUCT-ROTATION-SECOND-CANARY"
	auth := make(chan string, 2)
	var targetCalls atomic.Int32
	target := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		targetCalls.Add(1)
		auth <- request.Header.Get("Authorization")
		writer.Header().Set("Content-Type", "application/json")
		writer.WriteHeader(http.StatusOK)
		_, _ = writer.Write([]byte(`{"ok":true}`))
	}))
	defer target.Close()
	spy := &workflowProductResolverSpy{}
	firstModel := &workflowProductModelTransport{}
	first, specPath, contractPath := composeProductIdentityWorkflowWithOptions(t, root, target.URL, firstModel, spy, true)
	t.Setenv("FEATUREPROOF_PRODUCT_IDENTITY", firstCanary)
	firstView, err := first.Service.Start(context.Background(), productIdentityStartCommand(first, specPath, contractPath, "rotation-first", validationworkflow.ApprovalDisabled))
	if err != nil || firstView.Outcome == nil || *firstView.Outcome != validationworkflow.OutcomePassed {
		t.Fatalf("first rotation run=%#v err=%v", firstView, err)
	}
	firstJournalPath := filepath.Join(root, ".featureproof", "runs", firstView.Handle, "events.jsonl")
	firstJournal, err := os.ReadFile(firstJournalPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("FEATUREPROOF_PRODUCT_IDENTITY", secondCanary)
	secondModel := &workflowProductModelTransport{}
	second := recomposeProductIdentityWorkflowWithOptions(t, secondModel, spy, true)
	secondView, err := second.Service.Start(context.Background(), productIdentityStartCommand(second, specPath, contractPath, "rotation-second", validationworkflow.ApprovalDisabled))
	if err != nil || secondView.Outcome == nil || *secondView.Outcome != validationworkflow.OutcomePassed {
		t.Fatalf("second rotation run=%#v err=%v", secondView, err)
	}
	firstJournalAfter, err := os.ReadFile(firstJournalPath)
	if err != nil || !bytes.Equal(firstJournal, firstJournalAfter) {
		t.Fatal("second run mutated the first append-only journal")
	}
	if firstModel.calls.Load() != 1 || secondModel.calls.Load() != 1 || spy.calls.Load() != 2 || targetCalls.Load() != 2 {
		t.Fatalf("rotation effects: first_model=%d second_model=%d resolver=%d target=%d", firstModel.calls.Load(), secondModel.calls.Load(), spy.calls.Load(), targetCalls.Load())
	}
	if got := <-auth; got != "Bearer "+firstCanary {
		t.Fatalf("first material=%q", got)
	}
	if got := <-auth; got != "Bearer "+secondCanary {
		t.Fatalf("second material=%q", got)
	}
	firstID, firstBinding := readProductIdentityBinding(t, root, firstView.Handle)
	secondID, secondBinding := readProductIdentityBinding(t, root, secondView.Handle)
	if firstID != "customer" || secondID != firstID || secondBinding != firstBinding {
		t.Fatalf("rotation changed binding: first=%s/%s second=%s/%s", firstID, firstBinding, secondID, secondBinding)
	}
	for _, run := range []string{firstView.Handle, secondView.Handle} {
		events := readProductIdentityTerminalEvents(t, root, run)
		if len(events) != 1 || events[0].Attempt != 1 || events[0].IdentityID != firstID || events[0].OperationBindingSHA256 != firstBinding || events[0].OutcomeCategory != "success" {
			t.Fatalf("rotation terminal events for %s: %#v", run, events)
		}
	}
	assertProductDurableFilesExclude(t, root, firstCanary, secondCanary, "cred_customer")
}
