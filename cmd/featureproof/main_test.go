package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/asierhr/featureProof/internal/agenteval"
	"github.com/asierhr/featureProof/internal/evidence"
	"github.com/asierhr/featureProof/internal/externalagent"
	"github.com/asierhr/featureProof/internal/model"
	"github.com/asierhr/featureProof/internal/planmanifest"
	"github.com/asierhr/featureProof/internal/validationworkflow"
)

// TestWorkflowConfigurationRequiresExplicitSnakeCaseSelections proves composition never supplies a default model or accepts ambiguous fields.
func TestWorkflowConfigurationRequiresExplicitSnakeCaseSelections(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "workflow.json")
	config := map[string]any{
		"provider": "openai", "model": "selected-model", "auth_profile": "profile", "profile_store": filepath.Join(root, "profiles"), "runtime_profile": "runtime",
		"specification_path": "spec.yaml", "contract_path": "openapi.yaml", "runtime_manifest_path": "runtime.json", "repository_id": "repo", "commit_oid": strings.Repeat("a", 40),
		"environment_id": "environment", "attestation_ref": "attestation", "target_alias": "target", "target_url": "https://example.test", "target_kind": "direct", "policy_id": "network",
		"attestation_valid_until": time.Now().Add(time.Hour).UTC(), "allow_disabled_approval": false,
	}
	write := func(value map[string]any) {
		raw, err := json.Marshal(value)
		if err != nil || os.WriteFile(path, raw, 0o600) != nil {
			t.Fatal("write workflow config")
		}
	}
	t.Setenv("FEATUREPROOF_WORKFLOW_CONFIG", path)
	write(config)
	loaded, err := loadWorkflowConfig()
	if err != nil || loaded.Model != "selected-model" || loaded.TargetAlias != "target" {
		t.Fatalf("valid config rejected: %#v %v", loaded, err)
	}
	delete(config, "model")
	write(config)
	if _, err = loadWorkflowConfig(); err == nil {
		t.Fatal("missing model received a default")
	}
	config["model"], config["unknown"] = "selected-model", true
	write(config)
	if _, err = loadWorkflowConfig(); err == nil {
		t.Fatal("unknown workflow config field accepted")
	}
}

// TestWorkflowEffectiveContextDigestsBindConfigurationBehindStableIDs proves production projections cannot hide changed objects behind unchanged aliases.
func TestWorkflowEffectiveContextDigestsBindConfigurationBehindStableIDs(t *testing.T) {
	config := workflowConfig{RuntimeProfile: "runtime", PolicyID: "policy", TargetURL: "https://example.test/", EnvironmentID: "environment", AttestationRef: "proof", AttestationValidUntil: time.Unix(10000, 0).UTC()}
	network, err := workflowNetworkContext(config, nil)
	if err != nil {
		t.Fatal(err)
	}
	target := validationworkflow.TargetContext{Kind: "direct", Alias: "target", URL: config.TargetURL, EnvironmentID: config.EnvironmentID, Attestation: validationworkflow.AttestationContext{EnvironmentID: config.EnvironmentID, RepositoryID: "repo", CommitOID: strings.Repeat("a", 40), Reference: config.AttestationRef, ValidUntil: config.AttestationValidUntil, Verified: true}, Network: network}
	targetDigest, err := validationworkflow.TargetContextDigest(target)
	if err != nil {
		t.Fatal(err)
	}
	for name, mutate := range map[string]func(*validationworkflow.TargetContext){
		"environment": func(value *validationworkflow.TargetContext) {
			value.EnvironmentID, value.Attestation.EnvironmentID = "environment-2", "environment-2"
		},
		"attestation": func(value *validationworkflow.TargetContext) { value.Attestation.Reference = "proof-2" },
		"valid_until": func(value *validationworkflow.TargetContext) {
			value.Attestation.ValidUntil = value.Attestation.ValidUntil.Add(time.Hour)
		},
		"network": func(value *validationworkflow.TargetContext) { value.Network.Hosts = []string{"other.test"} },
	} {
		t.Run("target_"+name, func(t *testing.T) {
			changed := target
			changed.Network.Hosts = append([]string(nil), target.Network.Hosts...)
			mutate(&changed)
			digest, digestErr := validationworkflow.TargetContextDigest(changed)
			if digestErr != nil || digest == targetDigest {
				t.Fatalf("effective target drift hidden behind stable IDs: %v", digestErr)
			}
		})
	}
	manifest := planmanifest.Manifest{Version: 1, HTTP: planmanifest.HTTPConfig{AllowedMethods: []string{"GET"}, AllowedRequestHeaders: []string{}, AllowedResponseHeaders: []string{}, FollowRedirectsForMethods: []string{}, TimeoutMS: 1000, MaxURLBytes: 2048, MaxRequestHeaderBytes: 1024, MaxRequestBodyBytes: 1024, MaxResponseHeaderBytes: 1024, MaxResponseBodyBytes: 1024}, Redaction: evidence.Config{MaxBodyBytes: 1024, AdditionalHeaders: []string{}, SensitiveJSONPaths: []string{}, ModelSensitiveJSONPaths: []string{}}}
	runtime := workflowRuntimeContext(config, manifest, network)
	runtimeDigest, err := validationworkflow.RuntimeContextDigest(runtime)
	if err != nil {
		t.Fatal(err)
	}
	for name, mutate := range map[string]func(*validationworkflow.RuntimeContext){
		"manifest":  func(value *validationworkflow.RuntimeContext) { value.HTTP.TimeoutMS++ },
		"schema":    func(value *validationworkflow.RuntimeContext) { value.Schema.Version = "v2" },
		"redaction": func(value *validationworkflow.RuntimeContext) { value.Redaction.MaxBodyBytes++ },
		"validator": func(value *validationworkflow.RuntimeContext) { value.Validator.ToolVersion = "v2" },
		"policy":    func(value *validationworkflow.RuntimeContext) { value.Policy.Reason = "changed" },
		"network":   func(value *validationworkflow.RuntimeContext) { value.Network.Ports = []int{8443} },
	} {
		t.Run("runtime_"+name, func(t *testing.T) {
			changed := runtime
			mutate(&changed)
			digest, digestErr := validationworkflow.RuntimeContextDigest(changed)
			if digestErr != nil || digest == runtimeDigest {
				t.Fatalf("effective runtime drift hidden behind stable IDs: %v", digestErr)
			}
		})
	}
}

// TestWorkflowCommandFailsClosedBeforeHistoricalDispatch proves workflow composition errors do not alter validate routing.
func TestWorkflowCommandFailsClosedBeforeHistoricalDispatch(t *testing.T) {
	t.Setenv("FEATUREPROOF_WORKFLOW_CONFIG", "")
	if code := run([]string{"workflow", "status", "unknown"}, strings.NewReader(""), io.Discard, io.Discard); code != 3 {
		t.Fatalf("workflow composition code=%d", code)
	}
}

// writeConfiguredPreviewFixture creates one complete trusted root configuration and deterministic plan.
func writeConfiguredPreviewFixture(t *testing.T, root, target string) string {
	t.Helper()
	parsed, err := url.Parse(target)
	if err != nil {
		t.Fatal(err)
	}
	port, _ := strconv.Atoi(parsed.Port())
	specPath := filepath.Join(root, "spec.yaml")
	planPath := filepath.Join(root, "plan.json")
	spec := "version: 1\nfeature:\n  id: preview-feature\n  description: root\ntarget:\n  id: deployed\n  base_url: https://unused.example\nconstraints:\n  - id: health\n    description: healthy\n    target_id: deployed\nlimits:\n  max_steps: 2\n  max_tool_calls: 2\n  max_retries: 0\n"
	plan := `{"version":1,"runtime":{"http":{"allowed_methods":["GET"],"allowed_request_headers":[],"allowed_response_headers":["Content-Type"],"follow_redirects_for_methods":[],"max_redirects":0,"timeout_ms":1000,"max_url_bytes":2048,"max_request_header_bytes":8192,"max_request_body_bytes":1024,"max_response_header_bytes":8192,"max_response_body_bytes":1024},"redaction":{"max_body_bytes":1024,"additional_sensitive_headers":[],"sensitive_json_paths":[],"model_sensitive_json_paths":[]},"policy":{"allowed_tools":["http.request"]}},"plans":[{"id":"health-plan","constraint_id":"health","steps":[{"id":"request","tool":"http.request","input":{"method":{"literal":"GET"},"path":{"literal":"/"},"headers":{"object":{}}},"assertions":[{"id":"status","definition":{"operator":"eq","path":"/status","expected":200}}],"depends_on":[]}]}]}`
	if err = os.WriteFile(specPath, []byte(spec), 0o600); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(planPath, []byte(plan), 0o600); err != nil {
		t.Fatal(err)
	}
	config := configuredPreviewFile{Alias: "preview", ProviderID: "configured", EnvironmentRef: "environment-ref", ProfileID: "profile", NetworkPolicyID: "network", RepositoryID: "42", CommitOID: strings.Repeat("a", 40), EnvironmentID: "environment", Target: target, AttestationRef: "configured-proof", AttestationValidUntil: time.Now().Add(time.Hour), SpecPath: specPath, PlanPath: planPath, Scheme: parsed.Scheme, Host: parsed.Hostname(), Port: port, BasePath: "/", Secretless: true}
	raw, _ := json.Marshal(config)
	path := filepath.Join(root, "preview.json")
	if err = os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// TestRunDelegatesInvalidArgumentsWithoutStartingAValidation covers the executable signal wrapper.
func TestRunDelegatesInvalidArgumentsWithoutStartingAValidation(t *testing.T) {
	_ = processArgs()
	var stdout, stderr bytes.Buffer
	if code := run([]string{"validate"}, strings.NewReader(""), &stdout, &stderr); code != 3 || stdout.Len() != 0 {
		t.Fatalf("code=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
}

// TestRunDispatchesHermeticAgentEvaluation proves the executable exposes evals without an implicit provider.
func TestRunDispatchesHermeticAgentEvaluation(t *testing.T) {
	repo, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	base := filepath.Join(repo, "evals", "agentplanning", "v1")
	sha, err := os.ReadFile(filepath.Join(base, "replay.sha256"))
	if err != nil {
		t.Fatal(err)
	}
	args := []string{"eval", "agent", "-dataset", filepath.Join(base, "dataset.json"), "-criteria", filepath.Join(base, "criteria.json"), "-output", t.TempDir(), "-source", "replay", "-replay", filepath.Join(base, "replay.json"), "-replay-sha256", strings.TrimSpace(string(sha)), "-run-id", "command-eval"}
	var stdout, stderr bytes.Buffer
	if code := run(args, strings.NewReader(""), &stdout, &stderr); code != 0 {
		t.Fatalf("exit=%d stderr=%s", code, stderr.String())
	}
}

// TestProductionEvalComposerDefersCredentialResolution proves composition succeeds without network or token access.
func TestProductionEvalComposerDefersCredentialResolution(t *testing.T) {
	options := agenteval.CommandOptions{Provider: "openai", Model: "gpt-test", Profile: "profile-test", ProfileStore: filepath.Join(t.TempDir(), "profiles.json"), CallTimeout: time.Second, MaxDuration: time.Minute, MaxCalls: 1, MaxTokens: 100, MaxReplans: 0, MaxRequestBytes: 1 << 20, MaxHeaderBytes: 1 << 15, MaxStreamBytes: 1 << 20, MaxOutputBytes: 1 << 16}
	source, err := (productionEvalComposer{}).Compose(context.Background(), options)
	if err != nil {
		t.Fatal(err)
	}
	provider, selected := source.Metadata()
	if provider != "openai" || selected != "gpt-test" {
		t.Fatalf("selection=%s/%s", provider, selected)
	}
	budgeted, ok := source.(interface{ SafeBudget() agenteval.SourceBudget })
	if !ok || budgeted.SafeBudget().CallTimeoutMillis != options.CallTimeout.Milliseconds() {
		t.Fatalf("call timeout was not preserved in safe configuration: %#v", source)
	}
	if _, err := (productionEvalComposer{}).Compose(context.Background(), agenteval.CommandOptions{Provider: "other"}); err == nil {
		t.Fatal("unsupported provider accepted")
	}
	if _, err := (productionEvalComposer{}).Compose(context.Background(), agenteval.CommandOptions{Provider: "openai"}); err == nil {
		t.Fatal("invalid profile store accepted")
	}
	invalid := options
	invalid.Profile = ""
	if _, err := (productionEvalComposer{}).Compose(context.Background(), invalid); err == nil {
		t.Fatal("empty profile accepted")
	}
	invalid = options
	invalid.MaxRequestBytes = 0
	if _, err := (productionEvalComposer{}).Compose(context.Background(), invalid); err == nil {
		t.Fatal("invalid adapter bounds accepted")
	}
	invalid = options
	invalid.MaxCalls = -1
	if _, err := (productionEvalComposer{}).Compose(context.Background(), invalid); err == nil {
		t.Fatal("invalid session budget accepted")
	}
	if (evalClock{}).Now().Location() != time.UTC {
		t.Fatal("eval clock is not UTC")
	}
	if err := (evalObserver{}).Observe(context.Background(), model.AttemptRecord{}); err != nil {
		t.Fatal(err)
	}
}

// TestMainPropagatesTheCommandExitCode covers the process shell without terminating the test process.
func TestMainPropagatesTheCommandExitCode(t *testing.T) {
	oldArgs, oldInput, oldOutput, oldError, oldExit := processArgs, processInput, processOutput, processError, processExit
	defer func() {
		processArgs, processInput, processOutput, processError, processExit = oldArgs, oldInput, oldOutput, oldError, oldExit
	}()
	var stdout, stderr bytes.Buffer
	code := -1
	processArgs = func() []string { return []string{"validate"} }
	processInput = strings.NewReader("")
	processOutput = &stdout
	processError = &stderr
	processExit = func(value int) { code = value }
	main()
	if code != 3 {
		t.Fatalf("code=%d", code)
	}
}

// TestProductionExternalAgentCompositionFailsClosed covers trusted OS composition without preview execution.
func TestProductionExternalAgentCompositionFailsClosed(t *testing.T) {
	original, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(t.TempDir()); err != nil {
		t.Fatal(err)
	}
	defer os.Chdir(original)
	service, err := composeExternalAgentService()
	if err != nil {
		t.Fatal(err)
	}
	defer service.Close(context.Background())
	capabilities, err := service.Capabilities(context.Background())
	if err != nil || len(capabilities.Operations) != 1 || capabilities.Operations[0].Name != "capabilities" {
		t.Fatalf("capabilities=%#v err=%v", capabilities, err)
	}
	if _, err := service.Submit(context.Background(), externalagent.SubmitInput{SelectionAlias: "safe", IdempotencyKey: "production-key01"}); err == nil {
		t.Fatal("preview composition admitted a run")
	}
	if !isExternalAgentCommand([]string{"run"}) || isExternalAgentCommand([]string{"validate"}) || isExternalAgentCommand(nil) {
		t.Fatal("external dispatch classification changed")
	}
	var output bytes.Buffer
	if code := writeExternalCompositionError(&output); code != 3 || !strings.Contains(output.String(), string(externalagent.CodeInternal)) {
		t.Fatalf("code=%d body=%s", code, output.String())
	}
}

// TestRunDispatchesExternalCapabilities covers the executable JSON adapter without credentials.
func TestRunDispatchesExternalCapabilities(t *testing.T) {
	original, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(t.TempDir()); err != nil {
		t.Fatal(err)
	}
	defer os.Chdir(original)
	var stdout, stderr bytes.Buffer
	if code := run([]string{"capabilities", "--json"}, strings.NewReader(""), &stdout, &stderr); code != 0 || !strings.Contains(stdout.String(), `"contract_version":2`) {
		t.Fatalf("code=%d stdout=%s stderr=%s", code, stdout.String(), stderr.String())
	}
}

// TestGitHubProductionCompositionRequiresEverySecretAndExactIdentity proves the executable fails closed before binding a server.
func TestGitHubProductionCompositionRequiresEverySecretAndExactIdentity(t *testing.T) {
	for _, name := range []string{"FEATUREPROOF_GITHUB_INSTALLATION_ID", "FEATUREPROOF_GITHUB_REPOSITORY_ID", "FEATUREPROOF_GITHUB_ACTOR_ID", "FEATUREPROOF_GITHUB_APP_ID", "FEATUREPROOF_GITHUB_BOT_ID", "FEATUREPROOF_GITHUB_WEBHOOK_SECRET", "FEATUREPROOF_GITHUB_PRIVATE_KEY_PEM", "FEATUREPROOF_EVIDENCE_ORIGIN"} {
		t.Setenv(name, "")
	}
	if _, _, err := composeGitHubRuntime(context.Background()); err == nil {
		t.Fatal("github runtime composed without installation identity and secrets")
	}
	if _, err := requiredPositiveEnv("FEATUREPROOF_GITHUB_INSTALLATION_ID"); err == nil {
		t.Fatal("empty installation identity accepted")
	}
}

// TestConfiguredPreviewRunsThroughExternalCLIEntrypoint verifies the real CLI service composes and executes feature 19.
func TestConfiguredPreviewRunsThroughExternalCLIEntrypoint(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer server.Close()
	root := t.TempDir()
	t.Chdir(root)
	t.Setenv("FEATUREPROOF_PREVIEW_CONFIG", writeConfiguredPreviewFixture(t, root, server.URL+"/"))
	var stdout, stderr bytes.Buffer
	if code := run([]string{"run", "submit", "--selection", "preview", "--idempotency-key", "root-entrypoint-key", "--json"}, strings.NewReader(""), &stdout, &stderr); code != 0 {
		t.Fatalf("code=%d stdout=%s stderr=%s", code, stdout.String(), stderr.String())
	}
	matches, err := filepath.Glob(filepath.Join(root, ".featureproof", "runs", "*", "report.json"))
	if err != nil || len(matches) != 1 {
		t.Fatalf("reports=%v err=%v", matches, err)
	}
}

// TestConfiguredPreviewRunsThroughMCPEntrypoint verifies stdio tools/call uses the same configured preview lifecycle.
func TestConfiguredPreviewRunsThroughMCPEntrypoint(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer server.Close()
	root := t.TempDir()
	t.Chdir(root)
	t.Setenv("FEATUREPROOF_PREVIEW_CONFIG", writeConfiguredPreviewFixture(t, root, server.URL+"/"))
	request := `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"protocolVersion":"2026-07-28","name":"featureproof.submit","arguments":{"selection_alias":"preview","idempotency_key":"mcp-entrypoint-key"}}}` + "\n"
	var stdout, stderr bytes.Buffer
	if code := run([]string{"mcp", "serve"}, strings.NewReader(request), &stdout, &stderr); code != 0 || !strings.Contains(stdout.String(), `"phase":"queued"`) {
		t.Fatalf("code=%d stdout=%s stderr=%s", code, stdout.String(), stderr.String())
	}
	matches, err := filepath.Glob(filepath.Join(root, ".featureproof", "preview", "*.json"))
	if err != nil || len(matches) != 1 {
		t.Fatalf("preview records=%v err=%v", matches, err)
	}
}

// TestConfiguredPreviewComposesInsideGitHubEntrypoint verifies complete config replaces the temporary fail-closed gateway.
func TestConfiguredPreviewComposesInsideGitHubEntrypoint(t *testing.T) {
	root := t.TempDir()
	t.Chdir(root)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) }))
	defer server.Close()
	t.Setenv("FEATUREPROOF_PREVIEW_CONFIG", writeConfiguredPreviewFixture(t, root, server.URL+"/"))
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	for name, value := range map[string]string{"FEATUREPROOF_GITHUB_INSTALLATION_ID": "1", "FEATUREPROOF_GITHUB_REPOSITORY_ID": "42", "FEATUREPROOF_GITHUB_ACTOR_ID": "7", "FEATUREPROOF_GITHUB_APP_ID": "8", "FEATUREPROOF_GITHUB_BOT_ID": "9", "FEATUREPROOF_GITHUB_WEBHOOK_SECRET": "0123456789abcdef", "FEATUREPROOF_GITHUB_PRIVATE_KEY_PEM": string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: encoded})), "FEATUREPROOF_EVIDENCE_ORIGIN": "https://evidence.example.test"} {
		t.Setenv(name, value)
	}
	runtime, _, err := composeGitHubRuntime(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	closeCtx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err = runtime.Close(closeCtx); err != nil {
		t.Fatal(err)
	}
}
