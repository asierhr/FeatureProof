package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/asierhr/featureProof/internal/agenteval"
	"github.com/asierhr/featureProof/internal/agentplanning"
	"github.com/asierhr/featureProof/internal/contextassembly"
	"github.com/asierhr/featureProof/internal/contractcatalog"
	"github.com/asierhr/featureProof/internal/evidence"
	"github.com/asierhr/featureProof/internal/externalagent"
	"github.com/asierhr/featureProof/internal/goinspector"
	"github.com/asierhr/featureProof/internal/model"
	"github.com/asierhr/featureProof/internal/planmanifest"
	"github.com/asierhr/featureProof/internal/specification"
	"github.com/asierhr/featureProof/internal/validationworkflow"
)

// staticWorkflowSpecification keeps the admission test focused on productive contract composition while returning an isolated trusted spec.
type staticWorkflowSpecification struct{ value specification.Specification }

// Load returns the test-owned specification without performing external work.
func (s staticWorkflowSpecification) Load(context.Context, string) (specification.Specification, error) {
	return s.value, nil
}

// rejectingWorkflowTransport records any unexpected productive network attempt and always fails it.
type rejectingWorkflowTransport struct{ calls atomic.Int32 }

// RoundTrip makes a model, OAuth or tool network attempt observable without reaching an external system.
func (t *rejectingWorkflowTransport) RoundTrip(*http.Request) (*http.Response, error) {
	t.calls.Add(1)
	return nil, errors.New("unexpected network capability")
}

// TestWorkflowConfigurationRequiresExplicitSnakeCaseSelections proves composition never supplies a default model or accepts ambiguous fields.
func TestWorkflowConfigurationRequiresExplicitSnakeCaseSelections(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "workflow.json")
	config := map[string]any{
		"provider": "openai", "model": "selected-model", "auth_profile": "profile", "profile_store": filepath.Join(root, "profiles"), "runtime_profile": "runtime",
		"specification_path": "spec.yaml", "contract_path": "openapi.yaml", "runtime_manifest_path": "runtime.json", "repository_id": "repo", "commit_oid": strings.Repeat("a", 40),
		"environment_id": "environment", "attestation_ref": "attestation", "target_alias": "target", "target_url": "https://example.test", "target_kind": "direct", "policy_id": "network",
		"attestation_valid_until": time.Now().Add(time.Hour).UTC(), "allow_disabled_approval": false,
		"contract_projection": map[string]any{"max_string_bytes": 4096, "allowed_url_schemes": []string{"https"}, "sensitive_names": []string{}, "sensitive_paths": []string{}, "sensitive_header_names": []string{"authorization"}, "forbidden_values": []string{}},
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
	config["model"] = "selected-model"
	delete(config, "contract_projection")
	write(config)
	if _, err = loadWorkflowConfig(); err == nil {
		t.Fatal("missing projection policy received defaults")
	}
	config["contract_projection"] = map[string]any{"max_string_bytes": 4096, "allowed_url_schemes": []string{"https"}, "sensitive_names": []string{}, "sensitive_paths": []string{}, "sensitive_header_names": []string{"authorization"}}
	write(config)
	if _, err = loadWorkflowConfig(); err == nil {
		t.Fatal("partial projection policy accepted")
	}
	config["contract_projection"] = map[string]any{"max_string_bytes": 4096, "allowed_url_schemes": []string{"https"}, "sensitive_names": []string{}, "sensitive_paths": []string{}, "sensitive_header_names": []string{"authorization"}, "forbidden_values": []string{}}
	config["model"], config["unknown"] = "selected-model", true
	write(config)
	if _, err = loadWorkflowConfig(); err == nil {
		t.Fatal("unknown workflow config field accepted")
	}
	delete(config, "unknown")
	config["contract_review_id"] = strings.Repeat("a", 24)
	write(config)
	if _, err = loadWorkflowConfig(); err == nil {
		t.Fatal("partial approved contract reference accepted")
	}
	config["contract_review_revision"] = uint64(2)
	config["contract_review_digest"] = strings.Repeat("b", 64)
	write(config)
	loaded, err = loadWorkflowConfig()
	if err != nil || loaded.ContractReviewRevision != 2 {
		t.Fatalf("complete approved contract reference rejected: %#v %v", loaded, err)
	}
	delete(config, "contract_review_id")
	delete(config, "contract_review_revision")
	delete(config, "contract_review_digest")
	config["contract_source"] = "unknown"
	write(config)
	if _, err = loadWorkflowConfig(); err == nil {
		t.Fatal("unknown contract source accepted")
	}
	config["contract_source"] = "go-static-v1"
	write(config)
	if _, err = loadWorkflowConfig(); err == nil {
		t.Fatal("incomplete Go source accepted")
	}
	config["contract_source"] = "combined"
	write(config)
	if _, err = loadWorkflowConfig(); err == nil {
		t.Fatal("incomplete combined source accepted")
	}
	t.Setenv("FEATUREPROOF_WORKFLOW_CONFIG", filepath.Join(root, "missing.json"))
	if _, err = loadWorkflowConfig(); err == nil {
		t.Fatal("missing configuration file accepted")
	}
}

// TestComposedWorkflowRejectsConfiguredSensitiveContractSurfacesBeforeEffects exercises the real composition and CLI admission boundary.
func TestComposedWorkflowRejectsConfiguredSensitiveContractSurfacesBeforeEffects(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(map[string]any, string) string
		policy func(map[string]any, string)
	}{
		{"path", mutateWorkflowPath, func(policy map[string]any, value string) { policy["sensitive_paths"] = []string{"/" + value} }},
		{"logical_id", func(doc map[string]any, value string) string {
			workflowOperation(doc, "/pets")["operationId"] = value
			return "/pets"
		}, func(policy map[string]any, value string) { policy["sensitive_names"] = []string{value} }},
		{"header", func(doc map[string]any, value string) string {
			workflowOperation(doc, "/pets")["parameters"] = []any{map[string]any{"name": value, "in": "header", "schema": map[string]any{"type": "string"}}}
			return "/pets"
		}, func(policy map[string]any, value string) { policy["sensitive_header_names"] = []string{value} }},
		{"parameter", func(doc map[string]any, value string) string {
			workflowOperation(doc, "/pets")["parameters"] = []any{map[string]any{"name": value, "in": "query", "schema": map[string]any{"type": "string"}}}
			return "/pets"
		}, forbidWorkflowValue},
		{"schema_enum", func(doc map[string]any, value string) string {
			workflowRequestSchema(doc)["enum"] = []any{value}
			return "/pets"
		}, forbidWorkflowValue},
		{"schema_default_examples", func(doc map[string]any, value string) string {
			workflowRequestSchema(doc)["default"], workflowRequestSchema(doc)["example"] = value, value
			return "/pets"
		}, forbidWorkflowValue},
		{"oauth_url", func(doc map[string]any, value string) string {
			workflowOAuthFlow(doc)["tokenUrl"] = "https://issuer.example/" + value
			return "/pets"
		}, forbidWorkflowValue},
		{"oauth_scope", mutateWorkflowScope, forbidWorkflowValue},
		{"response", func(doc map[string]any, value string) string {
			workflowResponse(doc)["headers"] = map[string]any{value: map[string]any{"schema": map[string]any{"type": "string"}}}
			return "/pets"
		}, func(policy map[string]any, value string) { policy["sensitive_header_names"] = []string{value} }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			installProjectMemoryWitness(t)
			root := t.TempDir()
			t.Chdir(root)
			secret := "opaque-" + strings.ReplaceAll(test.name, "_", "-") + "-918273"
			document := workflowContractFixture()
			selectedPath := test.mutate(document, secret)
			policy := workflowProjectionFixture()
			test.policy(policy, secret)
			config, specPath, contractPath := writeWorkflowCompositionFixture(t, root, document, selectedPath, policy)
			t.Setenv("FEATUREPROOF_WORKFLOW_CONFIG", config)
			transport := &rejectingWorkflowTransport{}
			previousTransport := http.DefaultTransport
			http.DefaultTransport = transport
			t.Cleanup(func() { http.DefaultTransport = previousTransport })

			var stdout, stderr bytes.Buffer
			cli, err := composeWorkflowCLI(context.Background(), &stdout, &stderr)
			if err != nil {
				t.Fatalf("compose: %v", err)
			}
			args := []string{"start", "--spec", specPath, "--contract", contractPath, "--provider", "openai", "--model", "selected-model", "--profile", "profile", "--runtime-profile", "runtime", "--repository", "repo", "--revision", strings.Repeat("a", 40), "--approval", "required", "--idempotency-key", "redaction-test-key", "--target", "target"}
			if code := cli.Run(context.Background(), args, &stdout); code != 3 {
				t.Fatalf("code=%d output=%s stderr=%s", code, stdout.String(), stderr.String())
			}
			if transport.calls.Load() != 0 {
				t.Fatalf("network/model/tool capability invoked %d times", transport.calls.Load())
			}
			if strings.Contains(stdout.String(), secret) || strings.Contains(stderr.String(), secret) {
				t.Fatalf("sensitive value escaped through diagnostics: stdout=%s stderr=%s", stdout.String(), stderr.String())
			}
			assertWorkflowStoresContainNoSensitiveState(t, root, secret)
		})
	}
}

// TestComposeWorkflowCLIFailsClosedAtInputAndStorageBoundaries covers productive composition failures before any model call.
func TestComposeWorkflowCLIFailsClosedAtInputAndStorageBoundaries(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*testing.T, string, string, string)
	}{
		{"missing_specification", func(t *testing.T, _ string, spec, _ string) {
			if err := os.Remove(spec); err != nil {
				t.Fatal(err)
			}
		}},
		{"missing_manifest", func(t *testing.T, root, _, config string) {
			rewriteWorkflowConfig(t, config, func(value map[string]any) {
				value["runtime_manifest_path"] = filepath.Join(root, "absent-runtime.json")
			})
		}},
		{"invalid_redaction", func(t *testing.T, root, _, _ string) {
			rewriteWorkflowConfig(t, filepath.Join(root, "runtime.json"), func(value map[string]any) {
				value["runtime"].(map[string]any)["redaction"].(map[string]any)["additional_sensitive_headers"] = []any{"X-Duplicate", "x-duplicate"}
			})
		}},
		{"invalid_target", func(t *testing.T, _, _, config string) {
			rewriteWorkflowConfig(t, config, func(value map[string]any) { value["target_url"] = "ftp://target.example" })
		}},
		{"workflow_store", func(t *testing.T, root, _, _ string) {
			if err := os.MkdirAll(filepath.Join(root, ".featureproof"), 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(root, ".featureproof", "workflows"), []byte("blocked"), 0o600); err != nil {
				t.Fatal(err)
			}
		}},
		{"artifact_store", func(t *testing.T, root, _, _ string) { blockWorkflowStoreDirectory(t, root, "plans") }},
		{"executable_store", func(t *testing.T, root, _, _ string) { blockWorkflowStoreDirectory(t, root, "private") }},
		{"approval_store", func(t *testing.T, root, _, _ string) { blockWorkflowStoreDirectory(t, root, "approval") }},
		{"recovery", func(t *testing.T, root, _, _ string) {
			store := filepath.Join(root, ".featureproof", "workflows")
			if err := os.MkdirAll(store, 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(store, "corrupt.state.json"), []byte("bad"), 0o600); err != nil {
				t.Fatal(err)
			}
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			t.Chdir(root)
			config, spec, _ := writeWorkflowCompositionFixture(t, root, workflowContractFixture(), "/pets", workflowProjectionFixture())
			t.Setenv("FEATUREPROOF_WORKFLOW_CONFIG", config)
			test.mutate(t, root, spec, config)
			transport := &rejectingWorkflowTransport{}
			previousTransport := http.DefaultTransport
			http.DefaultTransport = transport
			t.Cleanup(func() { http.DefaultTransport = previousTransport })
			if _, err := composeWorkflowCLI(context.Background(), io.Discard, io.Discard); err == nil || transport.calls.Load() != 0 {
				t.Fatalf("composition did not fail before network: calls=%d err=%v", transport.calls.Load(), err)
			}
		})
	}
}

// TestComposeWorkflowCLISealsGoInspectorAuthority verifies productive composition carries configured roots into the shared binding port.
func TestComposeWorkflowCLISealsGoInspectorAuthority(t *testing.T) {
	installProjectMemoryWitness(t)
	root := t.TempDir()
	t.Chdir(root)
	configPath, _, _ := writeWorkflowCompositionFixture(t, root, workflowContractFixture(), "/pets", workflowProjectionFixture())
	if err := os.Mkdir(filepath.Join(root, "app"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "app", "main.go"), []byte("package app\nimport \"net/http\"\nfunc h(w http.ResponseWriter,r *http.Request){}\nfunc routes(){http.HandleFunc(\"GET /pets\",h)}"), 0o600); err != nil {
		t.Fatal(err)
	}
	rewriteWorkflowConfig(t, configPath, func(value map[string]any) {
		value["contract_source"] = goinspector.SourceKind
		value["contract_path"] = root
		value["go_inspector"] = map[string]any{"roots": []string{"app"}, "changed_paths": []string{"app/main.go"}, "goos": "linux", "goarch": "amd64", "build_tags": []string{}, "auth_matchers": []any{}, "limits": map[string]any{"max_files": 10, "max_bytes": 1048576, "max_packages": 5, "max_ast_nodes": 1000, "max_depth": 10, "max_types": 20, "max_operations": 10, "max_diagnostics": 10, "max_duration": int64(time.Second)}, "include_generated": false, "entry_symbols": []string{"routes"}}
	})
	t.Setenv("FEATUREPROOF_WORKFLOW_CONFIG", configPath)
	cli, err := composeWorkflowCLI(context.Background(), io.Discard, io.Discard)
	if err != nil || cli == nil {
		t.Fatalf("Go composition failed: %v", err)
	}
}

// TestComposeWorkflowCLIUsesMatchingPreviewConfiguration covers the productive preview branch without acquiring an environment.
func TestComposeWorkflowCLIUsesMatchingPreviewConfiguration(t *testing.T) {
	installProjectMemoryWitness(t)
	root := t.TempDir()
	t.Chdir(root)
	previewPath := writeConfiguredPreviewFixture(t, root, "https://target.example/")
	t.Setenv("FEATUREPROOF_PREVIEW_CONFIG", previewPath)
	var preview configuredPreviewFile
	rawPreview, err := os.ReadFile(previewPath)
	if err != nil || json.Unmarshal(rawPreview, &preview) != nil {
		t.Fatal("read preview fixture")
	}
	contractPath := filepath.Join(root, "openapi.json")
	rawContract, _ := json.Marshal(workflowContractFixture())
	if err = os.WriteFile(contractPath, rawContract, 0o600); err != nil {
		t.Fatal(err)
	}
	configPath := filepath.Join(root, "workflow.json")
	config := map[string]any{"provider": "openai", "model": "selected-model", "auth_profile": "profile", "profile_store": filepath.Join(root, "profiles.json"), "runtime_profile": "runtime", "specification_path": preview.SpecPath, "contract_path": contractPath, "runtime_manifest_path": preview.PlanPath, "repository_id": preview.RepositoryID, "commit_oid": preview.CommitOID, "environment_id": preview.EnvironmentID, "attestation_ref": preview.AttestationRef, "target_alias": preview.Alias, "target_url": preview.Target, "target_kind": "preview", "policy_id": preview.NetworkPolicyID, "attestation_valid_until": preview.AttestationValidUntil, "allow_disabled_approval": false, "contract_selections": map[string]any{"health": []any{map[string]any{"method": "GET", "path": "/pets"}}}, "contract_projection": workflowProjectionFixture()}
	rawConfig, _ := json.Marshal(config)
	if err = os.WriteFile(configPath, rawConfig, 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("FEATUREPROOF_WORKFLOW_CONFIG", configPath)
	cli, err := composeWorkflowCLI(context.Background(), io.Discard, io.Discard)
	if err != nil || len(cli.PreviewTargets) != 1 || len(cli.DirectTargets) != 0 {
		t.Fatalf("preview composition=%#v err=%v", cli, err)
	}

	rewriteWorkflowConfig(t, configPath, func(value map[string]any) { value["environment_id"] = "drifted" })
	if _, err = composeWorkflowCLI(context.Background(), io.Discard, io.Discard); err == nil {
		t.Fatal("preview identity drift accepted")
	}
}

// TestComposeWorkflowCLIRequiresDurableContextWitness proves production cannot silently replace restart authority with process memory.
func TestComposeWorkflowCLIRequiresDurableContextWitness(t *testing.T) {
	root := t.TempDir()
	t.Chdir(root)
	configPath, _, _ := writeWorkflowCompositionFixture(t, root, workflowContractFixture(), "/pets", workflowProjectionFixture())
	t.Setenv("FEATUREPROOF_WORKFLOW_CONFIG", configPath)
	t.Run("missing", func(t *testing.T) {
		t.Setenv("FEATUREPROOF_MEMORY_WITNESS_URL", "")
		t.Setenv("FEATUREPROOF_MEMORY_WITNESS_CREDENTIAL", "")
		t.Setenv("FEATUREPROOF_MEMORY_WITNESS_GUARANTEE", "")
		t.Setenv("FEATUREPROOF_CONTEXT_WITNESS_STREAM", "")
		if _, err := composeWorkflowCLI(context.Background(), io.Discard, io.Discard); err == nil || !strings.Contains(err.Error(), "durable context witness required") {
			t.Fatalf("production accepted volatile context persistence: %v", err)
		}
	})
	t.Run("weak-guarantee", func(t *testing.T) {
		t.Setenv("FEATUREPROOF_CONTEXT_WITNESS_STREAM", "context-required")
		t.Setenv("FEATUREPROOF_MEMORY_WITNESS_GUARANTEE", "filesystem-only")
		if _, err := composeWorkflowCLI(context.Background(), io.Discard, io.Discard); err == nil || !strings.Contains(err.Error(), "context witness guarantee unavailable") {
			t.Fatalf("production accepted replayable witness: %v", err)
		}
	})
	t.Run("unavailable", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) { writer.WriteHeader(http.StatusServiceUnavailable) }))
		defer server.Close()
		t.Setenv("FEATUREPROOF_CONTEXT_WITNESS_STREAM", "context-required")
		t.Setenv("FEATUREPROOF_MEMORY_WITNESS_GUARANTEE", "linearizable-durable-nonreplayable-v1")
		t.Setenv("FEATUREPROOF_MEMORY_WITNESS_URL", server.URL)
		t.Setenv("FEATUREPROOF_MEMORY_WITNESS_CREDENTIAL", "credential")
		if _, err := composeWorkflowCLI(context.Background(), io.Discard, io.Discard); err == nil || !strings.Contains(err.Error(), "context witness unavailable") {
			t.Fatalf("production accepted unavailable witness: %v", err)
		}
	})
	t.Run("bootstrap-failure", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
			if request.Method == http.MethodGet {
				writer.WriteHeader(http.StatusNotFound)
				return
			}
			writer.WriteHeader(http.StatusServiceUnavailable)
		}))
		defer server.Close()
		t.Setenv("FEATUREPROOF_CONTEXT_WITNESS_STREAM", "context-required")
		t.Setenv("FEATUREPROOF_MEMORY_WITNESS_GUARANTEE", "linearizable-durable-nonreplayable-v1")
		t.Setenv("FEATUREPROOF_MEMORY_WITNESS_URL", server.URL)
		t.Setenv("FEATUREPROOF_MEMORY_WITNESS_CREDENTIAL", "credential")
		if _, err := composeWorkflowCLI(context.Background(), io.Discard, io.Discard); err == nil || !strings.Contains(err.Error(), "context witness bootstrap unavailable") {
			t.Fatalf("production accepted uninitialized witness: %v", err)
		}
	})
	t.Run("caller-cancellation", func(t *testing.T) {
		entered, stopped := make(chan struct{}), make(chan struct{})
		server := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, request *http.Request) {
			close(entered)
			<-request.Context().Done()
			close(stopped)
		}))
		defer server.Close()
		t.Setenv("FEATUREPROOF_CONTEXT_WITNESS_STREAM", "context-required")
		t.Setenv("FEATUREPROOF_MEMORY_WITNESS_GUARANTEE", "linearizable-durable-nonreplayable-v1")
		t.Setenv("FEATUREPROOF_MEMORY_WITNESS_URL", server.URL)
		t.Setenv("FEATUREPROOF_MEMORY_WITNESS_CREDENTIAL", "credential")
		cause := errors.New("read cancellation sentinel")
		ctx, cancel := context.WithCancelCause(context.Background())
		result := make(chan error, 1)
		go func() {
			_, composeErr := composeWorkflowCLI(ctx, io.Discard, io.Discard)
			result <- composeErr
		}()
		select {
		case <-entered:
		case <-time.After(5 * time.Second):
			cancel(cause)
			t.Fatal("composition did not reach witness HTTP")
		}
		cancel(cause)
		select {
		case composeErr := <-result:
			if !contextassembly.IsCategory(composeErr, contextassembly.CategoryCancelled) || !errors.Is(composeErr, cause) || strings.Contains(composeErr.Error(), cause.Error()) {
				t.Fatalf("cancelled witness composition=%v", composeErr)
			}
		case <-time.After(time.Second):
			t.Fatal("cancelled composition did not return")
		}
		select {
		case <-stopped:
		case <-time.After(time.Second):
			t.Fatal("cancelled witness request leaked")
		}
	})
	t.Run("bootstrap-cancellation", func(t *testing.T) {
		entered, stopped := make(chan struct{}), make(chan struct{})
		server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
			if request.Method == http.MethodGet {
				writer.WriteHeader(http.StatusNotFound)
				return
			}
			_, _ = io.Copy(io.Discard, request.Body)
			close(entered)
			<-request.Context().Done()
			close(stopped)
		}))
		defer server.Close()
		t.Setenv("FEATUREPROOF_CONTEXT_WITNESS_STREAM", "context-required")
		t.Setenv("FEATUREPROOF_MEMORY_WITNESS_GUARANTEE", "linearizable-durable-nonreplayable-v1")
		t.Setenv("FEATUREPROOF_MEMORY_WITNESS_URL", server.URL)
		t.Setenv("FEATUREPROOF_MEMORY_WITNESS_CREDENTIAL", "credential")
		cause := errors.New("bootstrap cancellation sentinel")
		ctx, cancel := context.WithCancelCause(context.Background())
		result := make(chan error, 1)
		go func() {
			_, composeErr := composeWorkflowCLI(ctx, io.Discard, io.Discard)
			result <- composeErr
		}()
		select {
		case <-entered:
		case <-time.After(5 * time.Second):
			cancel(cause)
			t.Fatal("composition did not reach witness bootstrap HTTP")
		}
		cancel(cause)
		select {
		case composeErr := <-result:
			if !contextassembly.IsCategory(composeErr, contextassembly.CategoryCancelled) || !errors.Is(composeErr, cause) || strings.Contains(composeErr.Error(), cause.Error()) {
				t.Fatalf("cancelled witness bootstrap=%v", composeErr)
			}
		case <-time.After(time.Second):
			t.Fatal("cancelled bootstrap did not return")
		}
		select {
		case <-stopped:
		case <-time.After(time.Second):
			t.Fatal("cancelled bootstrap request leaked")
		}
	})
	t.Run("read-deadline", func(t *testing.T) {
		entered, stopped := make(chan struct{}), make(chan struct{})
		server := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, request *http.Request) {
			close(entered)
			<-request.Context().Done()
			close(stopped)
		}))
		defer server.Close()
		t.Setenv("FEATUREPROOF_CONTEXT_WITNESS_STREAM", "context-required")
		t.Setenv("FEATUREPROOF_MEMORY_WITNESS_GUARANTEE", "linearizable-durable-nonreplayable-v1")
		t.Setenv("FEATUREPROOF_MEMORY_WITNESS_URL", server.URL)
		t.Setenv("FEATUREPROOF_MEMORY_WITNESS_CREDENTIAL", "credential")
		ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
		defer cancel()
		result := make(chan error, 1)
		go func() {
			_, composeErr := composeWorkflowCLI(ctx, io.Discard, io.Discard)
			result <- composeErr
		}()
		select {
		case <-entered:
		case <-time.After(2 * time.Second):
			t.Fatal("composition did not reach deadline witness HTTP")
		}
		select {
		case composeErr := <-result:
			if !contextassembly.IsCategory(composeErr, contextassembly.CategoryTimeout) || !errors.Is(composeErr, context.DeadlineExceeded) {
				t.Fatalf("deadline witness composition=%v", composeErr)
			}
		case <-time.After(2 * time.Second):
			t.Fatal("deadline composition did not return")
		}
		select {
		case <-stopped:
		case <-time.After(time.Second):
			t.Fatal("deadline witness request leaked")
		}
	})
}

// blockWorkflowStoreDirectory places a regular file where one composition-owned store directory must be created.
func blockWorkflowStoreDirectory(t *testing.T, root, name string) {
	t.Helper()
	store := filepath.Join(root, ".featureproof", "workflows")
	if err := os.MkdirAll(store, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(store, name), []byte("blocked"), 0o600); err != nil {
		t.Fatal(err)
	}
}

// rewriteWorkflowConfig applies one trusted test mutation while retaining strict JSON field names.
func rewriteWorkflowConfig(t *testing.T, path string, mutate func(map[string]any)) {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var value map[string]any
	if err = json.Unmarshal(raw, &value); err != nil {
		t.Fatal(err)
	}
	mutate(value)
	raw, err = json.Marshal(value)
	if err != nil || os.WriteFile(path, raw, 0o600) != nil {
		t.Fatal("rewrite workflow config")
	}
}

// workflowContractFixture returns one complete selected OpenAPI operation with request, OAuth and response surfaces.
func workflowContractFixture() map[string]any {
	return map[string]any{"openapi": "3.1.0", "info": map[string]any{"title": "workflow", "version": "1"}, "paths": map[string]any{"/pets": map[string]any{"get": map[string]any{"operationId": "readPets", "parameters": []any{}, "requestBody": map[string]any{"content": map[string]any{"application/json": map[string]any{"schema": map[string]any{"type": "string"}}}}, "security": []any{map[string]any{"oauth": []any{"read"}}}, "responses": map[string]any{"200": map[string]any{"description": "ok", "headers": map[string]any{}, "content": map[string]any{"application/json": map[string]any{"schema": map[string]any{"type": "string"}}}}}}}}, "components": map[string]any{"securitySchemes": map[string]any{"oauth": map[string]any{"type": "oauth2", "flows": map[string]any{"clientCredentials": map[string]any{"tokenUrl": "https://issuer.example/token", "scopes": map[string]any{"read": "read"}}}}}}}
}

// workflowOperation returns the mutable fixture operation at one selected path.
func workflowOperation(document map[string]any, path string) map[string]any {
	return document["paths"].(map[string]any)[path].(map[string]any)["get"].(map[string]any)
}

// workflowRequestSchema returns the selected request schema used by projection adversaries.
func workflowRequestSchema(document map[string]any) map[string]any {
	return workflowOperation(document, "/pets")["requestBody"].(map[string]any)["content"].(map[string]any)["application/json"].(map[string]any)["schema"].(map[string]any)
}

// workflowOAuthFlow returns the selected OAuth flow used by projection adversaries.
func workflowOAuthFlow(document map[string]any) map[string]any {
	return document["components"].(map[string]any)["securitySchemes"].(map[string]any)["oauth"].(map[string]any)["flows"].(map[string]any)["clientCredentials"].(map[string]any)
}

// workflowResponse returns the selected success response used by projection adversaries.
func workflowResponse(document map[string]any) map[string]any {
	return workflowOperation(document, "/pets")["responses"].(map[string]any)["200"].(map[string]any)
}

// mutateWorkflowPath moves the selected operation beneath one configured sensitive path.
func mutateWorkflowPath(document map[string]any, value string) string {
	path := "/" + value
	paths := document["paths"].(map[string]any)
	paths[path], paths["/pets"] = paths["/pets"], nil
	delete(paths, "/pets")
	return path
}

// mutateWorkflowScope puts the configured value in both a declared and required OAuth scope.
func mutateWorkflowScope(document map[string]any, value string) string {
	workflowOAuthFlow(document)["scopes"] = map[string]any{value: "sensitive scope"}
	workflowOperation(document, "/pets")["security"] = []any{map[string]any{"oauth": []any{value}}}
	return "/pets"
}

// forbidWorkflowValue configures one exact forbidden fragment without relying on a product canary constant.
func forbidWorkflowValue(policy map[string]any, value string) {
	policy["forbidden_values"] = []string{value}
}

// workflowProjectionFixture returns every required productive projection setting with explicit empty sets.
func workflowProjectionFixture() map[string]any {
	return map[string]any{"max_string_bytes": 4096, "allowed_url_schemes": []string{"https"}, "sensitive_names": []string{}, "sensitive_paths": []string{}, "sensitive_header_names": []string{"authorization"}, "forbidden_values": []string{}}
}

// writeWorkflowCompositionFixture writes complete trusted composition inputs and returns their exact references.
func writeWorkflowCompositionFixture(t *testing.T, root string, document map[string]any, selectedPath string, projection map[string]any) (string, string, string) {
	t.Helper()
	specPath := filepath.Join(root, "spec.yaml")
	contractPath := filepath.Join(root, "openapi.json")
	manifestPath := filepath.Join(root, "runtime.json")
	profilePath := filepath.Join(root, "profiles.json")
	configPath := filepath.Join(root, "workflow.json")
	spec := "version: 1\nfeature:\n  id: contract-redaction\n  description: redaction\ntarget:\n  id: deployed\n  base_url: https://unused.example\nconstraints:\n  - id: c1\n    description: selected contract\n    target_id: deployed\nlimits:\n  max_steps: 2\n  max_tool_calls: 2\n  max_retries: 0\n"
	manifest := `{"version":1,"runtime":{"http":{"allowed_methods":["GET"],"allowed_request_headers":[],"allowed_response_headers":[],"follow_redirects_for_methods":[],"max_redirects":0,"timeout_ms":1000,"max_url_bytes":2048,"max_request_header_bytes":8192,"max_request_body_bytes":1024,"max_response_header_bytes":8192,"max_response_body_bytes":1024},"redaction":{"max_body_bytes":1024,"additional_sensitive_headers":[],"sensitive_json_paths":[],"model_sensitive_json_paths":[]},"policy":{"allowed_tools":["http.request"]}},"plans":[{"id":"p1","constraint_id":"c1","steps":[{"id":"request","tool":"http.request","input":{"method":{"literal":"GET"},"path":{"literal":"/"},"headers":{"object":{}}},"assertions":[{"id":"status","definition":{"operator":"eq","path":"/status","expected":200}}],"depends_on":[]}]}]}`
	contract, err := json.Marshal(document)
	if err != nil {
		t.Fatal(err)
	}
	config := map[string]any{"provider": "openai", "model": "selected-model", "auth_profile": "profile", "profile_store": profilePath, "runtime_profile": "runtime", "specification_path": specPath, "contract_path": contractPath, "runtime_manifest_path": manifestPath, "repository_id": "repo", "commit_oid": strings.Repeat("a", 40), "environment_id": "environment", "attestation_ref": "attestation", "target_alias": "target", "target_url": "https://target.example", "target_kind": "direct", "policy_id": "network", "attestation_valid_until": time.Now().Add(time.Hour).UTC(), "allow_disabled_approval": false, "contract_selections": map[string]any{"c1": []any{map[string]any{"method": "GET", "path": selectedPath}}}, "contract_projection": projection}
	rawConfig, err := json.Marshal(config)
	if err != nil {
		t.Fatal(err)
	}
	for path, contents := range map[string][]byte{specPath: []byte(spec), contractPath: contract, manifestPath: []byte(manifest), configPath: rawConfig} {
		if err = os.WriteFile(path, contents, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return configPath, specPath, contractPath
}

// assertWorkflowStoresContainNoSensitiveState proves rejected admission created no plan, approval, executable or workflow record.
func assertWorkflowStoresContainNoSensitiveState(t *testing.T, root, secret string) {
	t.Helper()
	storeRoot := filepath.Join(root, ".featureproof", "workflows")
	err := filepath.Walk(storeRoot, func(path string, info os.FileInfo, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if info.IsDir() {
			return nil
		}
		raw, readErr := os.ReadFile(path)
		if readErr != nil {
			return readErr
		}
		if strings.Contains(string(raw), secret) {
			t.Fatalf("sensitive value persisted in %s", path)
		}
		if !strings.HasSuffix(info.Name(), ".lock") {
			t.Fatalf("rejected admission unexpectedly persisted %s", path)
		}
		return nil
	})
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		t.Fatal(err)
	}
}

// TestWorkflowContractReducerUsesOnlyTrustedStructuredSelections proves productive composition never assigns every operation to every constraint.
func TestWorkflowContractReducerUsesOnlyTrustedStructuredSelections(t *testing.T) {
	root := t.TempDir()
	contractPath := filepath.Join(root, "api.yaml")
	document := `openapi: "3.1.0"
info: {title: selections, version: "1"}
paths:
  /one: {get: {responses: {"200": {description: ok}}}}
  /two: {post: {responses: {"204": {description: ok}}}}
  /UNSELECTED_OPERATION_MARKER: {delete: {responses: {"204": {description: ok}}}}
`
	if err := os.WriteFile(contractPath, []byte(document), 0o600); err != nil {
		t.Fatal(err)
	}
	projection := contractcatalog.ProjectionPolicy{MaxStringBytes: 4096, AllowedURLSchemes: []string{"https"}, SensitiveNames: []string{}, SensitivePaths: []string{}, SensitiveHeaderNames: []string{}, ForbiddenValues: []string{}}
	reducer := workflowContractReducer{path: contractPath, selections: map[string][]workflowOperationSelection{"c1": {{Method: "GET", Path: "/one"}}, "c2": {{Method: "POST", Path: "/two"}}}, projection: projection}
	spec := specification.Specification{Version: 1, Feature: specification.Feature{ID: "f", Description: "f"}, Target: specification.Target{ID: "t", BaseURL: "https://target.example"}, Constraints: []specification.Constraint{{ID: "c1", Description: "one", TargetID: "t"}, {ID: "c2", Description: "two", TargetID: "t"}}, Limits: specification.Limits{MaxSteps: 2, MaxToolCalls: 2}}
	reduced, err := reducer.Reduce(context.Background(), contractPath, spec)
	if err != nil {
		t.Fatal(err)
	}
	raw := string(reduced.Bytes())
	if strings.Contains(raw, "UNSELECTED_OPERATION_MARKER") || !strings.Contains(raw, "/one") || !strings.Contains(raw, "/two") {
		t.Fatalf("selection projection=%s", raw)
	}
	network := validationworkflow.NetworkContext{PolicyID: "network", Schemes: []string{"https"}, Hosts: []string{"target.example"}, Ports: []int{443}, BasePaths: []string{"/"}, MaxURLBytes: 2048}
	target := validationworkflow.TargetContext{Kind: "direct", Alias: "target", URL: "https://target.example", EnvironmentID: "environment", Attestation: validationworkflow.AttestationContext{EnvironmentID: "environment", RepositoryID: "repo", CommitOID: strings.Repeat("a", 40), Reference: "attestation", ValidUntil: time.Now().Add(time.Hour), Verified: true}, Network: network}
	targetDigest, err := validationworkflow.TargetContextDigest(target)
	if err != nil {
		t.Fatal(err)
	}
	runtime := validationworkflow.RuntimeContext{Profile: "runtime", ManifestVersion: 1, Registry: validationworkflow.RegistryContext{Version: "v1", Tools: []string{"probe"}}, Schema: validationworkflow.SchemaContext{Version: "v1", ReferenceVersion: "v1", Fields: []string{"path"}}, Validator: validationworkflow.ValidatorContext{Tool: "probe", ToolVersion: "v1", AssertionEngine: "definition", AssertionVersion: "v1"}, Policy: validationworkflow.PolicyContext{ID: "policy", AllowedTools: []string{"probe"}, Reason: "test"}, Network: network}
	command := validationworkflow.StartCommand{SpecificationRef: "spec", ContractRef: contractPath, Provider: "provider", Model: "model", CompositionRef: "composition", RuntimeProfile: "runtime", RepositoryID: "repo", Revision: strings.Repeat("a", 40), Target: validationworkflow.TargetSelection{Direct: &validationworkflow.DirectTarget{Alias: "target", Digest: targetDigest}}}
	builder := validationworkflow.TrustedAdmissionBuilder{Specifications: staticWorkflowSpecification{value: spec}, Contracts: reducer, Bindings: workflowBindingVerifier{config: workflowConfig{RepositoryID: command.RepositoryID, CommitOID: command.Revision}, binding: validationworkflow.BindingContext{Revision: validationworkflow.RevisionContext{RepositoryID: command.RepositoryID, CommitOID: command.Revision}, Target: target}}, Catalog: workflowCatalog{profile: "runtime", runtime: runtime, descriptors: []agentplanning.CapabilityDescriptor{{Name: "probe", Class: "read"}}}, Config: validationworkflow.AdmissionConfig{Provider: command.Provider, Model: command.Model, CompositionRef: command.CompositionRef, Limits: agentplanning.Limits{MaxInputBytes: 1 << 20, MaxOutputBytes: 1 << 20, MaxConstraints: 8, MaxCapabilities: 8, MaxChecks: 16, MaxStepsPerCheck: 8, MaxAssertionsPerStep: 8, MaxMissingItems: 8, MaxValidationWork: 1024}}}
	admitted, err := builder.Build(context.Background(), command)
	if err != nil || len(admitted.Generation.Constraints) != 2 || strings.Contains(string(admitted.Generation.Contract.Bytes()), "UNSELECTED_OPERATION_MARKER") {
		t.Fatalf("vertical admission=%#v err=%v", admitted, err)
	}
	reducer.selections = map[string][]workflowOperationSelection{"c1": {{Method: "GET", Path: "/one"}}}
	_, err = reducer.Reduce(context.Background(), contractPath, spec)
	var decision *contractcatalog.DecisionError
	if !errors.As(err, &decision) || decision.Category != contractcatalog.CategoryInsufficient || len(decision.MissingKinds) == 0 {
		t.Fatalf("missing trusted selection=%#v %v", decision, err)
	}
	if _, err = reducer.Reduce(context.Background(), filepath.Join(root, "other.yaml"), spec); err == nil {
		t.Fatal("unauthorized contract reference accepted")
	}
	invalidPath := filepath.Join(root, "invalid.yaml")
	if err = os.WriteFile(invalidPath, []byte("openapi: ["), 0o600); err != nil {
		t.Fatal(err)
	}
	reducer.path = invalidPath
	if _, err = reducer.Reduce(context.Background(), invalidPath, spec); err == nil {
		t.Fatal("invalid contract accepted")
	}
	reducer.path = contractPath
	reducer.projection.ForbiddenValues = nil
	if _, err = reducer.Reduce(context.Background(), contractPath, spec); err == nil {
		t.Fatal("incomplete projection authority accepted")
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

// TestGitHubRuntimeOwnsProductReviewLifecycle proves failed composition, shutdown, and recomposition release real review store and publisher handles.
func TestGitHubRuntimeOwnsProductReviewLifecycle(t *testing.T) {
	root := t.TempDir()
	t.Chdir(root)
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	for name, value := range map[string]string{
		"FEATUREPROOF_GITHUB_INSTALLATION_ID": "1", "FEATUREPROOF_GITHUB_REPOSITORY_ID": "42", "FEATUREPROOF_GITHUB_ACTOR_ID": "7",
		"FEATUREPROOF_GITHUB_APP_ID": "8", "FEATUREPROOF_GITHUB_BOT_ID": "9", "FEATUREPROOF_GITHUB_WEBHOOK_SECRET": "0123456789abcdef",
		"FEATUREPROOF_GITHUB_PRIVATE_KEY_PEM": string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: encoded})), "FEATUREPROOF_EVIDENCE_ORIGIN": "https://evidence.example.test",
	} {
		t.Setenv(name, value)
	}
	t.Setenv("FEATUREPROOF_CONTRACT_REVIEW_CONFIG", writeProductReviewConfig(t, root))
	githubStore := filepath.Join(root, ".featureproof", "github-pr")
	if err = os.MkdirAll(filepath.Dir(githubStore), 0700); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(githubStore, []byte("block store"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, _, err = composeGitHubRuntime(context.Background()); err == nil {
		t.Fatal("blocked GitHub store unexpectedly composed")
	}
	if err = os.Remove(githubStore); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{filepath.Join(root, ".featureproof", "contract-reviews"), filepath.Join(root, "exports")} {
		if err = os.RemoveAll(path); err != nil {
			t.Fatalf("failed composition leaked review handle for %s: %v", path, err)
		}
	}
	for iteration := 0; iteration < 2; iteration++ {
		if err = os.MkdirAll(filepath.Join(root, "exports"), 0700); err != nil {
			t.Fatal(err)
		}
		runtime, _, composeErr := composeGitHubRuntime(context.Background())
		if composeErr != nil {
			t.Fatal(composeErr)
		}
		closeCtx, cancel := context.WithTimeout(context.Background(), time.Second)
		closeErr := runtime.Close(closeCtx)
		cancel()
		if closeErr != nil {
			t.Fatal(closeErr)
		}
		for _, path := range []string{filepath.Join(root, ".featureproof", "contract-reviews"), filepath.Join(root, "exports")} {
			if err = os.RemoveAll(path); err != nil {
				t.Fatalf("iteration %d leaked review handle for %s: %v", iteration, path, err)
			}
		}
	}
}
