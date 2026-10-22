package validationworkflow

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/asierhr/featureProof/internal/agentplanning"
	"github.com/asierhr/featureProof/internal/githubpr"
	"github.com/asierhr/featureProof/internal/planning"
)

// identityWorkflowResponse returns a complete model response that explicitly selects the admitted identity.
func identityWorkflowResponse() []byte {
	return []byte(`{"version":1,"kind":"plan","plan":{"id":"workflow-1-plan","scenarios":[{"constraint_id":"c1","scenario":"positive","status":"planned"},{"constraint_id":"c1","scenario":"negative","status":"not_applicable","reason":"not retained"},{"constraint_id":"c1","scenario":"error","status":"not_applicable","reason":"not retained"}],"checks":[{"id":"check-1","constraint_id":"c1","scenario":"positive","capability":"http.request","operation":{"method":"GET","path":"/pets"},"identity_id":"customer","steps":[{"id":"step-1","tool":"http.request","input":{},"assertions":[{"id":"assert-1","definition":{"operator":"exists"}}]}],"evidence":[{"step_id":"step-1","assertion_ids":["assert-1"]}]}]}}`)
}

// identityWorkflowService builds one shared-service surface fixture with a sealed authenticated operation.
func identityWorkflowService(t *testing.T) (*Service, *fakeAdmission) {
	t.Helper()
	service, admission, _, _, _ := workflowFixture(t, identityWorkflowResponse())
	admission.snapshot.Generation.Capabilities = []agentplanning.CapabilityDescriptor{{Name: "http.request", Class: "http", Effects: true, Validate: func(map[string]planning.InputValue) error { return nil }}}
	admission.snapshot.Generation.Identities = workflowIdentityDescriptors()
	return service, admission
}

// requireIdentityArtifact verifies a surface produced the same exact non-secret identity binding.
func requireIdentityArtifact(t *testing.T, service *Service, handle string) {
	t.Helper()
	artifact, err := service.deps.Artifacts.Load(context.Background(), handle)
	if err != nil {
		t.Fatal(err)
	}
	step := artifact.Plans[0].Steps[0]
	if step.IdentityID != "customer" || len(step.OperationBindingSHA256) != 64 {
		t.Fatalf("identity binding mismatch: %#v", step)
	}
}

// TestAllSurfacesRejectSecretInputsBeforeIdentityAwareWorkflow proves external adapters cannot inject credential values or sources.
func TestAllSurfacesRejectSecretInputsBeforeIdentityAwareWorkflow(t *testing.T) {
	const canary = "CANARY-SURFACE-SECRET"
	profile := strings.Repeat("8", 64)
	direct := DirectTarget{Alias: "target-1", Digest: strings.Repeat("6", 64)}
	base := []string{"--spec", "spec-1", "--contract", "contract-1", "--provider", "fake", "--model", "selected", "--profile", "profile-1", "--runtime-profile", "runtime-1", "--repository", "repo-1", "--revision", strings.Repeat("a", 40), "--approval", "required", "--idempotency-key", "request-1", "--target", "target-1"}
	for _, flag := range []string{"--token", "--cookie", "--password", "--api-key", "--credential-ref", "--resolver"} {
		if _, err := parseStartCLI(append(append([]string(nil), base...), flag, canary), map[string]string{"profile-1": profile}, map[string]DirectTarget{"target-1": direct}, nil); !IsCode(err, CodeInvalid) || strings.Contains(err.Error(), canary) {
			t.Fatalf("CLI %s result: %v", flag, err)
		}
	}
	service, _, _, _, _ := workflowFixture(t, validPlanResponse())
	mcp := MCPServer{Service: service, Principal: "user-1", AuthProfiles: map[string]string{"profile-1": profile}, DirectTargets: map[string]DirectTarget{"target-1": direct}}
	for _, member := range []string{"token", "cookie", "password", "api_key", "credential_ref", "resolver"} {
		arguments := json.RawMessage(`{"idempotency_key":"request-1","specification_ref":"spec-1","contract_ref":"contract-1","provider":"fake","model":"selected","auth_profile":"profile-1","runtime_profile":"runtime-1","repository_id":"repo-1","revision":"` + strings.Repeat("a", 40) + `","approval":"required","target_alias":"target-1","` + member + `":"` + canary + `"}`)
		response := mcp.Handle(context.Background(), MCPRequest{Version: ContractVersion, Operation: "workflow.start", Arguments: arguments})
		encoded, _ := json.Marshal(response)
		if response.Error != CodeInvalid || strings.Contains(string(encoded), canary) {
			t.Fatalf("MCP %s result: %s", member, encoded)
		}
	}
	for _, member := range []string{"token", "credential_ref", "resolver"} {
		raw := []byte(`{"principal":"user-1","` + member + `":"` + canary + `"}`)
		var command StartCommand
		if err := decodeStrict(raw, &command); err == nil || strings.Contains(err.Error(), canary) {
			t.Fatalf("API %s result: %v", member, err)
		}
	}
}

// TestGitHubWorkflowTemplateHasNoExternalIdentitySelector proves untrusted delivery text cannot alter the sealed command template.
func TestGitHubWorkflowTemplateHasNoExternalIdentitySelector(t *testing.T) {
	command := validCommand()
	raw, err := json.Marshal(command)
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"identity_id", "credential_ref", "token", "cookie", "resolver"} {
		if strings.Contains(string(raw), forbidden) {
			t.Fatalf("GitHub command exposed %s: %s", forbidden, raw)
		}
	}
}

// TestIdentityWorkflowParityAcrossAPICLIAndIntegrations proves every public adapter reaches the same service-owned identity snapshot and binding contract.
func TestIdentityWorkflowParityAcrossAPICLIAndIntegrations(t *testing.T) {
	profile := strings.Repeat("8", 64)
	direct := DirectTarget{Alias: "target-1", Digest: validSnapshot().TargetDigest}
	t.Run("api", func(t *testing.T) {
		service, _ := identityWorkflowService(t)
		view, err := service.Start(context.Background(), validCommand())
		if err != nil {
			t.Fatal(err)
		}
		requireIdentityArtifact(t, service, view.Handle)
	})
	t.Run("cli", func(t *testing.T) {
		service, _ := identityWorkflowService(t)
		var output bytes.Buffer
		args := []string{"start", "--spec", "spec-1", "--contract", "contract-1", "--provider", "fake", "--model", "selected", "--profile", "profile-1", "--runtime-profile", "runtime-1", "--repository", "repo-1", "--revision", strings.Repeat("a", 40), "--approval", "required", "--idempotency-key", "request-cli-identity", "--target", "target-1"}
		if code := (CLI{Service: service, Principal: "user-1", AuthProfiles: map[string]string{"profile-1": profile}, DirectTargets: map[string]DirectTarget{"target-1": direct}}).Run(context.Background(), args, &output); code != 0 {
			t.Fatalf("CLI code %d: %s", code, output.String())
		}
		var view WorkflowView
		if err := json.Unmarshal(output.Bytes(), &view); err != nil {
			t.Fatal(err)
		}
		requireIdentityArtifact(t, service, view.Handle)
	})
	t.Run("mcp", func(t *testing.T) {
		service, _ := identityWorkflowService(t)
		server := MCPServer{Service: service, Principal: "user-1", AuthProfiles: map[string]string{"profile-1": profile}, DirectTargets: map[string]DirectTarget{"target-1": direct}}
		arguments := json.RawMessage(`{"idempotency_key":"request-mcp-identity","specification_ref":"spec-1","contract_ref":"contract-1","provider":"fake","model":"selected","auth_profile":"profile-1","runtime_profile":"runtime-1","repository_id":"repo-1","revision":"` + strings.Repeat("a", 40) + `","approval":"required","target_alias":"target-1"}`)
		response := server.Handle(context.Background(), MCPRequest{Version: ContractVersion, Operation: "workflow.start", Arguments: arguments})
		view, ok := response.Value.(WorkflowView)
		if response.Error != "" || !ok {
			t.Fatalf("MCP response: %#v", response)
		}
		requireIdentityArtifact(t, service, view.Handle)
	})
	t.Run("github", func(t *testing.T) {
		service, _ := identityWorkflowService(t)
		command := validCommand()
		snapshot, err := githubpr.SealSnapshot(githubpr.PullRequestSnapshot{Version: 1, InstallationID: 1, RepositoryID: 7, Number: 2, AuthorID: 9, State: "open", HeadOID: strings.Repeat("a", 40), HeadRepositoryID: 7})
		if err != nil {
			t.Fatal(err)
		}
		state, err := (GitHubGateway{Service: service, Principal: "user-1", Commands: map[string]StartCommand{"default": command}}).Submit(context.Background(), githubpr.Selection{Alias: "default"}, snapshot, "request-github-identity")
		if err != nil {
			t.Fatal(err)
		}
		requireIdentityArtifact(t, service, state.RunHandle)
	})
}
