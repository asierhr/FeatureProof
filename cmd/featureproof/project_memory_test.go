package main

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/asierhr/featureProof/internal/contextassembly"
	"github.com/asierhr/featureProof/internal/githubpr"
	"github.com/asierhr/featureProof/internal/projectmemory"
)

// memoryWebhookStore keeps only the current delivery receipt for productive webhook routing tests.
type memoryWebhookStore struct{}

// ReserveDelivery returns a fresh authenticated delivery so service idempotency remains observable.
func (memoryWebhookStore) ReserveDelivery(_ context.Context, receipt githubpr.DeliveryReceipt, _ string, _ time.Time, _ time.Duration) (githubpr.DeliveryReceipt, bool, error) {
	return receipt, true, nil
}

// CompleteDelivery returns the final safe association produced by the memory handler.
func (memoryWebhookStore) CompleteDelivery(_ context.Context, delivery string, _ uint64, state, association string) (githubpr.DeliveryReceipt, error) {
	return githubpr.DeliveryReceipt{DeliveryID: delivery, State: state, AssociationID: association}, nil
}

// memoryWebhookClock supplies deterministic delivery lease time to the webhook boundary.
type memoryWebhookClock struct{ now time.Time }

// Now returns the configured webhook instant.
func (clock memoryWebhookClock) Now() time.Time { return clock.now }

// Wait completes immediately because webhook routing tests do not exercise retry timers.
func (memoryWebhookClock) Wait(context.Context, time.Duration) error { return nil }

// configuredProjectMemoryAuthority returns one operational human policy for composition boundary tests.
func configuredProjectMemoryAuthority() projectMemoryAuthority {
	return projectMemoryAuthority{principal: "principal", credential: "credential", project: "project-1", repository: "repo-1", commit: strings.Repeat("a", 40), configuration: strings.Repeat("b", 64), role: "human", actorKind: "human", actions: map[string]bool{"propose": true, string(projectmemory.StateApproved): true, string(projectmemory.StateInvalidated): true, string(projectmemory.StateSuperseded): true}, query: projectMemoryQueryPolicy{classes: []projectmemory.Class{projectmemory.ClassFact}, states: []projectmemory.State{projectmemory.StateObserved, projectmemory.StateApproved}, authorities: []projectmemory.Authority{projectmemory.AuthorityObservation, projectmemory.AuthorityHuman}, maxSensitivity: projectmemory.SensitivityInternal}}
}

// memoryWebhookSignature computes the exact GitHub HMAC header for a controlled request body.
func memoryWebhookSignature(secret, body []byte) string {
	mac := hmac.New(sha256.New, secret)
	_, _ = mac.Write(body)
	return fmt.Sprintf("sha256=%x", mac.Sum(nil))
}

// compositionWitness is a linearizable in-memory stand-in reached through the productive HTTP witness client.
type compositionWitness struct {
	mu     sync.Mutex
	states map[string]projectmemory.WitnessState
}

// installProjectMemoryWitness provisions one externally hosted generation-zero stream for composition tests.
func installProjectMemoryWitness(t *testing.T) {
	t.Helper()
	emptyReceipts := sha256.Sum256([]byte("{}"))
	authority := &compositionWitness{states: map[string]projectmemory.WitnessState{"project-1": {ProjectID: "project-1", ReceiptsDigest: fmt.Sprintf("%x", emptyReceipts)}, "project-1-context": contextassembly.InitialWitnessState("project-1-context")}}
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Header.Get("Authorization") != "Bearer witness-credential" {
			writer.WriteHeader(http.StatusUnauthorized)
			return
		}
		parts := strings.Split(strings.Trim(request.URL.Path, "/"), "/")
		if len(parts) < 3 {
			writer.WriteHeader(http.StatusNotFound)
			return
		}
		project := parts[2]
		authority.mu.Lock()
		defer authority.mu.Unlock()
		current, exists := authority.states[project]
		if request.Method == http.MethodGet && len(parts) == 3 {
			if !exists {
				writer.WriteHeader(http.StatusNotFound)
				return
			}
			_ = json.NewEncoder(writer).Encode(current)
			return
		}
		if request.Method == http.MethodPost && len(parts) == 4 && parts[3] == "advance" {
			var command struct {
				Expected projectmemory.WitnessState `json:"expected"`
				Next     projectmemory.WitnessState `json:"next"`
			}
			if json.NewDecoder(request.Body).Decode(&command) != nil || !exists || current != command.Expected || command.Next.Generation != current.Generation+1 {
				writer.WriteHeader(http.StatusConflict)
				return
			}
			authority.states[project] = command.Next
			_ = json.NewEncoder(writer).Encode(command.Next)
			return
		}
		writer.WriteHeader(http.StatusMethodNotAllowed)
	}))
	t.Cleanup(server.Close)
	t.Setenv("FEATUREPROOF_MEMORY_WITNESS_URL", server.URL)
	t.Setenv("FEATUREPROOF_MEMORY_WITNESS_CREDENTIAL", "witness-credential")
	t.Setenv("FEATUREPROOF_MEMORY_WITNESS_GUARANTEE", "linearizable-durable-nonreplayable-v1")
	t.Setenv("FEATUREPROOF_CONTEXT_WITNESS_STREAM", "project-1-context")
}

// setProjectMemoryEnvironment installs one complete isolated production composition for entrypoint tests.
func setProjectMemoryEnvironment(t *testing.T, base, principal, repository string) {
	t.Helper()
	t.Setenv("FEATUREPROOF_MEMORY_ROOT", filepath.Join(base, "memory"))
	t.Setenv("FEATUREPROOF_MEMORY_TRUST_ROOT", filepath.Join(base, "registry"))
	installProjectMemoryWitness(t)
	t.Setenv("FEATUREPROOF_MEMORY_INTEGRITY_KEY", "0123456789abcdef0123456789abcdef")
	t.Setenv("FEATUREPROOF_MEMORY_PRINCIPAL", principal)
	t.Setenv("FEATUREPROOF_MEMORY_CREDENTIAL", "Bearer credential")
	t.Setenv("FEATUREPROOF_MEMORY_PROJECT", "project-1")
	t.Setenv("FEATUREPROOF_MEMORY_REPOSITORY", repository)
	t.Setenv("FEATUREPROOF_MEMORY_COMMIT", strings.Repeat("a", 40))
	t.Setenv("FEATUREPROOF_MEMORY_CONFIGURATION_DIGEST", strings.Repeat("b", 64))
}

// TestProjectMemoryCommandComposesDurableVertical exercises the executable composition through proposal, review query, transition, and operational query.
func TestProjectMemoryCommandComposesDurableVertical(t *testing.T) {
	t.Setenv("FEATUREPROOF_MEMORY_ROOT", filepath.Join(t.TempDir(), "memory"))
	t.Setenv("FEATUREPROOF_MEMORY_TRUST_ROOT", filepath.Join(t.TempDir(), "trust"))
	installProjectMemoryWitness(t)
	t.Setenv("FEATUREPROOF_MEMORY_INTEGRITY_KEY", "0123456789abcdef0123456789abcdef")
	t.Setenv("FEATUREPROOF_MEMORY_PRINCIPAL", "principal")
	t.Setenv("FEATUREPROOF_MEMORY_CREDENTIAL", "Bearer credential")
	t.Setenv("FEATUREPROOF_MEMORY_PROJECT", "project-1")
	t.Setenv("FEATUREPROOF_MEMORY_REPOSITORY", "repo-1")
	t.Setenv("FEATUREPROOF_MEMORY_COMMIT", strings.Repeat("a", 40))
	t.Setenv("FEATUREPROOF_MEMORY_CONFIGURATION_DIGEST", strings.Repeat("b", 64))
	now := time.Now().UTC().Add(-time.Second)
	candidate := projectmemory.Candidate{ContractVersion: projectmemory.ContractVersion, ProjectID: "project-1", Scope: projectmemory.Scope{Kind: projectmemory.ScopeRevision, RepositoryID: "repo-1", CommitOID: strings.Repeat("a", 40)}, Class: projectmemory.ClassFact, Subject: "api.pet", Predicate: "supports", Value: projectmemory.TypedValue{Kind: projectmemory.ValueBoolean, Data: true}, Provenance: projectmemory.Provenance{Source: "forged", Reference: "forged", Actor: projectmemory.Actor{Kind: "human", ID: "admin"}, Producer: "forged", ProducerVersion: "forged", ObservedAt: now, RepositoryID: "repo-1", CommitOID: strings.Repeat("a", 40)}, Confidence: projectmemory.ConfidenceHigh, Sensitivity: projectmemory.SensitivityPublic, ValidFrom: now}
	proposalRaw, _ := json.Marshal(projectmemory.ProposalCommand{IdempotencyKey: "product-proposal", Candidate: candidate})
	var proposalOutput bytes.Buffer
	if code := runWithContext(context.Background(), []string{"memory", string(projectmemory.OperationPropose)}, bytes.NewReader(proposalRaw), &proposalOutput, &bytes.Buffer{}); code != 0 {
		t.Fatalf("proposal code=%d output=%s", code, proposalOutput.String())
	}
	var proposal projectmemory.SurfaceResponse
	if json.Unmarshal(proposalOutput.Bytes(), &proposal) != nil || proposal.Review == nil || proposal.Review.Provenance.Actor.ID != "principal" {
		t.Fatalf("proposal response=%s", proposalOutput.String())
	}
	transitionRaw, _ := json.Marshal(projectmemory.TransitionCommand{ProjectID: "project-1", MemoryID: proposal.Review.ID, IdempotencyKey: "product-approve", ExpectedSequence: 1, ExpectedVersion: 1, DecisionID: "decision", Target: projectmemory.StateApproved})
	var transitionOutput bytes.Buffer
	if code := runWithContext(context.Background(), []string{"memory", string(projectmemory.OperationTransition)}, bytes.NewReader(transitionRaw), &transitionOutput, &bytes.Buffer{}); code != 0 {
		t.Fatalf("transition code=%d output=%s", code, transitionOutput.String())
	}
	query := projectmemory.QueryRequest{ProjectID: "project-1", Scope: candidate.Scope, States: []projectmemory.State{projectmemory.StateApproved}, Authorities: []projectmemory.Authority{projectmemory.AuthorityHuman}, MaxSensitivity: projectmemory.SensitivityPublic, Limit: 10, MaxBytes: 1 << 20}
	queryRaw, _ := json.Marshal(query)
	var queryOutput bytes.Buffer
	if code := runWithContext(context.Background(), []string{"memory", string(projectmemory.OperationQuery)}, bytes.NewReader(queryRaw), &queryOutput, &bytes.Buffer{}); code != 0 {
		t.Fatalf("query code=%d output=%s", code, queryOutput.String())
	}
	var response projectmemory.SurfaceResponse
	if json.Unmarshal(queryOutput.Bytes(), &response) != nil || response.Query == nil || len(response.Query.Items) != 1 || response.Query.Items[0].ID != proposal.Review.ID {
		t.Fatalf("query response=%s", queryOutput.String())
	}
}

// TestProjectMemoryCompositionAuthorityRejectsUntrustedCoordinates covers each production principal mapping boundary.
func TestProjectMemoryCompositionAuthorityRejectsUntrustedCoordinates(t *testing.T) {
	authority := configuredProjectMemoryAuthority()
	if _, err := authority.Authorize(context.Background(), "other", "project-1", "propose"); err == nil {
		t.Fatal("unknown principal authorized")
	}
	request := projectmemory.QueryRequest{ProjectID: "project-1", Scope: projectmemory.Scope{Kind: projectmemory.ScopeRevision, RepositoryID: "repo-1", CommitOID: strings.Repeat("a", 40)}}
	if _, err := authority.Resolve(context.Background(), "other", request); err == nil {
		t.Fatal("unknown query principal authorized")
	}
	request.Scope.RepositoryID = "other"
	if _, err := authority.Resolve(context.Background(), "principal", request); err == nil {
		t.Fatal("query scope widening accepted")
	}
	if principal, err := authority.ResolvePrincipal(context.Background(), "credential"); err != nil || principal != "principal" {
		t.Fatalf("credential mapping=%q %v", principal, err)
	}
	if _, err := authority.ResolvePrincipal(context.Background(), "bad"); err == nil {
		t.Fatal("bad credential accepted")
	}
	actor := projectmemory.VerifiedGitHubActor{ActorID: "principal", RepositoryID: "repo-1", Action: "memory.review"}
	if principal, err := authority.ResolveGitHubPrincipal(context.Background(), actor, projectmemory.OperationQuery, ""); err != nil || principal != "principal" {
		t.Fatalf("github mapping=%q %v", principal, err)
	}
	actor.Action = ""
	if _, err := authority.ResolveGitHubPrincipal(context.Background(), actor, projectmemory.OperationQuery, ""); err == nil {
		t.Fatal("empty GitHub action accepted")
	}
	actor.Action = "memory.review"
	if _, err := authority.ResolveGitHubPrincipal(context.Background(), actor, projectmemory.OperationTransition, projectmemory.StateInvalidated); err == nil {
		t.Fatal("review action authorized invalidate")
	}
	actor.Action = "memory.invalidate"
	if _, err := authority.ResolveGitHubPrincipal(context.Background(), actor, projectmemory.OperationTransition, projectmemory.StateApproved); err == nil {
		t.Fatal("invalidate action authorized approval")
	}
	if err := (projectMemoryEvidence{}).Verify(context.Background(), "project-1", request.Scope, projectmemory.EvidenceRef{}); err == nil {
		t.Fatal("unconfigured evidence accepted")
	}
	t.Setenv("FEATUREPROOF_MEMORY_WITNESS_GUARANTEE", "filesystem-hmac")
	if _, err := composeProjectMemory(); err == nil {
		t.Fatal("composition accepted a replayable witness guarantee")
	}
	t.Setenv("FEATUREPROOF_MEMORY_WITNESS_GUARANTEE", "linearizable-durable-nonreplayable-v1")
	t.Setenv("FEATUREPROOF_MEMORY_INTEGRITY_KEY", "")
	if _, err := composeProjectMemory(); err == nil {
		t.Fatal("incomplete composition accepted")
	}
}

// TestProjectMemoryOperationalQueryPolicyRejectsCallerSelectedEligibility proves request filters only narrow server configuration.
func TestProjectMemoryOperationalQueryPolicyRejectsCallerSelectedEligibility(t *testing.T) {
	authority := configuredProjectMemoryAuthority()
	base := projectmemory.QueryRequest{ProjectID: "project-1", Scope: projectmemory.Scope{Kind: projectmemory.ScopeRevision, RepositoryID: "repo-1", CommitOID: strings.Repeat("a", 40)}, Classes: []projectmemory.Class{projectmemory.ClassFact}, States: []projectmemory.State{projectmemory.StateApproved}, Authorities: []projectmemory.Authority{projectmemory.AuthorityHuman}, MaxSensitivity: projectmemory.SensitivityInternal}
	if resolved, err := authority.Resolve(context.Background(), "principal", base); err != nil || resolved.Review || resolved.MaxSensitivity != projectmemory.SensitivityInternal {
		t.Fatalf("configured operational query denied: %#v %v", resolved, err)
	}
	cases := []struct {
		name   string
		mutate func(*projectmemory.QueryRequest)
	}{
		{name: "proposed", mutate: func(request *projectmemory.QueryRequest) {
			request.States = []projectmemory.State{projectmemory.StateProposed}
			request.IncludeProposed = true
		}},
		{name: "terminal", mutate: func(request *projectmemory.QueryRequest) {
			request.States = []projectmemory.State{projectmemory.StateInvalidated}
		}},
		{name: "restricted", mutate: func(request *projectmemory.QueryRequest) {
			request.MaxSensitivity = projectmemory.SensitivityRestricted
		}},
		{name: "authority", mutate: func(request *projectmemory.QueryRequest) {
			request.Authorities = []projectmemory.Authority{projectmemory.AuthorityAgentProposal}
		}},
		{name: "class", mutate: func(request *projectmemory.QueryRequest) {
			request.Classes = []projectmemory.Class{projectmemory.ClassDiagnostic}
		}},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			request := base
			testCase.mutate(&request)
			if _, err := authority.Resolve(context.Background(), "principal", request); err == nil {
				t.Fatal("caller widened configured query authority")
			}
		})
	}
}

// TestComposedMemoryEntrypointsShareServiceCASIdempotencyAndDTO enters CLI, API, MCP, and GitHub through productive adapters.
func TestComposedMemoryEntrypointsShareServiceCASIdempotencyAndDTO(t *testing.T) {
	base := t.TempDir()
	t.Setenv("FEATUREPROOF_MEMORY_ROOT", filepath.Join(base, "memory"))
	t.Setenv("FEATUREPROOF_MEMORY_TRUST_ROOT", filepath.Join(base, "registry"))
	installProjectMemoryWitness(t)
	t.Setenv("FEATUREPROOF_MEMORY_INTEGRITY_KEY", "0123456789abcdef0123456789abcdef")
	t.Setenv("FEATUREPROOF_MEMORY_PRINCIPAL", "22")
	t.Setenv("FEATUREPROOF_MEMORY_CREDENTIAL", "Bearer credential")
	t.Setenv("FEATUREPROOF_MEMORY_PROJECT", "project-1")
	t.Setenv("FEATUREPROOF_MEMORY_REPOSITORY", "33")
	t.Setenv("FEATUREPROOF_MEMORY_COMMIT", strings.Repeat("a", 40))
	t.Setenv("FEATUREPROOF_MEMORY_CONFIGURATION_DIGEST", strings.Repeat("b", 64))
	composition, err := composeProjectMemory()
	if err != nil {
		t.Fatal(err)
	}
	if composition.CLI.Surface.Service != composition.Service || composition.API.Surface.Service != composition.Service || composition.MCP.Surface.Service != composition.Service || composition.GitHub.Surface.Service != composition.Service {
		t.Fatal("entrypoints do not share the composed service")
	}
	now := time.Now().UTC().Add(-time.Second)
	candidate := projectmemory.Candidate{ContractVersion: projectmemory.ContractVersion, ProjectID: "project-1", Scope: projectmemory.Scope{Kind: projectmemory.ScopeRevision, RepositoryID: "33", CommitOID: strings.Repeat("a", 40)}, Class: projectmemory.ClassFact, Subject: "api.pet", Predicate: "supports", Value: projectmemory.TypedValue{Kind: projectmemory.ValueBoolean, Data: true}, Confidence: projectmemory.ConfidenceHigh, Sensitivity: projectmemory.SensitivityPublic, ValidFrom: now}
	proposalRaw, _ := json.Marshal(projectmemory.ProposalCommand{IdempotencyKey: "shared-proposal", Candidate: candidate})
	var cliOutput bytes.Buffer
	if code := composition.CLI.Run(context.Background(), projectmemory.OperationPropose, proposalRaw, &cliOutput); code != 0 {
		t.Fatalf("CLI propose code=%d body=%s", code, cliOutput.String())
	}
	var proposed projectmemory.SurfaceResponse
	if json.Unmarshal(cliOutput.Bytes(), &proposed) != nil || proposed.Review == nil {
		t.Fatalf("CLI DTO=%s", cliOutput.String())
	}
	apiReplay := httptest.NewRecorder()
	apiRequest := httptest.NewRequest("POST", "/v1/memory", bytes.NewReader(proposalRaw))
	apiRequest.Header.Set("Authorization", "Bearer credential")
	apiRequest.Header.Set("FeatureProof-Operation", string(projectmemory.OperationPropose))
	composition.API.ServeHTTP(apiReplay, apiRequest)
	var replayed projectmemory.SurfaceResponse
	if json.Unmarshal(apiReplay.Body.Bytes(), &replayed) != nil || replayed.Review == nil || replayed.Review.ID != proposed.Review.ID || replayed.Review.Version != 1 {
		t.Fatalf("API idempotent replay DTO=%s", apiReplay.Body.String())
	}
	secret := []byte("0123456789abcdef")
	webhook := &githubpr.WebhookHandler{Secret: secret, MaxBody: 1 << 20, Store: memoryWebhookStore{}, Admit: func(context.Context, githubpr.WebhookCommand) (string, error) { return "", nil }, Review: projectMemoryGitHubHandler(composition), Owner: "memory-owner", Clock: memoryWebhookClock{now: now}, LeaseTTL: time.Minute}
	body, _ := json.Marshal(map[string]any{"action": "created", "installation": map[string]any{"id": 1}, "repository": map[string]any{"id": 33}, "issue": map[string]any{"number": 7, "pull_request": map[string]any{}}, "comment": map[string]any{"body": fmt.Sprintf("/featureproof memory approve %s 1 1 decision-1", proposed.Review.ID), "user": map[string]any{"id": 22}}})
	for _, delivery := range []string{"delivery-1", "delivery-1"} {
		request := httptest.NewRequest("POST", "/", bytes.NewReader(body))
		request.Header.Set("X-GitHub-Delivery", delivery)
		request.Header.Set("X-GitHub-Event", "issue_comment")
		request.Header.Set("X-Hub-Signature-256", memoryWebhookSignature(secret, body))
		response := httptest.NewRecorder()
		webhook.ServeHTTP(response, request)
		if response.Code != 202 || !strings.Contains(response.Body.String(), proposed.Review.ID) {
			t.Fatalf("GitHub webhook status=%d body=%s", response.Code, response.Body.String())
		}
	}
	query := projectmemory.QueryRequest{ProjectID: "project-1", Scope: candidate.Scope, Classes: []projectmemory.Class{projectmemory.ClassFact}, States: []projectmemory.State{projectmemory.StateApproved}, Authorities: []projectmemory.Authority{projectmemory.AuthorityHuman}, MaxSensitivity: projectmemory.SensitivityInternal, Limit: 10, MaxBytes: 1 << 20}
	queryRaw, _ := json.Marshal(query)
	apiQuery := httptest.NewRecorder()
	apiQueryRequest := httptest.NewRequest("POST", "/v1/memory", bytes.NewReader(queryRaw))
	apiQueryRequest.Header.Set("Authorization", "Bearer credential")
	apiQueryRequest.Header.Set("FeatureProof-Operation", string(projectmemory.OperationQuery))
	composition.API.ServeHTTP(apiQuery, apiQueryRequest)
	var apiResponse projectmemory.SurfaceResponse
	if json.Unmarshal(apiQuery.Body.Bytes(), &apiResponse) != nil || apiResponse.Query == nil || len(apiResponse.Query.Items) != 1 {
		t.Fatalf("API query DTO=%s", apiQuery.Body.String())
	}
	arguments, _ := json.Marshal(query)
	rpcLine, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "method": "tools/call", "params": map[string]any{"credential": "Bearer credential", "name": projectmemory.OperationQuery, "arguments": json.RawMessage(arguments)}})
	var mcpOutput bytes.Buffer
	if err := (projectmemory.MCPServer{Adapter: composition.MCP, MaxLine: 1 << 20}).Serve(context.Background(), bytes.NewReader(append(rpcLine, '\n')), &mcpOutput); err != nil || !strings.Contains(mcpOutput.String(), proposed.Review.ID) || !strings.Contains(mcpOutput.String(), `"isError":false`) {
		t.Fatalf("MCP query output=%s err=%v", mcpOutput.String(), err)
	}
	if head, err := composition.Service.Store.Head(context.Background(), "project-1"); err != nil || head.Sequence != 2 {
		t.Fatalf("shared CAS head=%#v %v", head, err)
	}
}

// TestProjectMemoryProductDispatchersOwnMCPAndAPILifecycles enters both server routes through their executable boundaries.
func TestProjectMemoryProductDispatchersOwnMCPAndAPILifecycles(t *testing.T) {
	setProjectMemoryEnvironment(t, t.TempDir(), "22", "33")
	request := `{"jsonrpc":"2.0","id":1,"method":"tools/list"}` + "\n"
	var output bytes.Buffer
	if code := runWithContext(context.Background(), []string{"memory", "mcp", "serve"}, strings.NewReader(request), &output, &bytes.Buffer{}); code != 0 || !strings.Contains(output.String(), "memory.query.v1") {
		t.Fatalf("MCP dispatcher code=%d output=%s", code, output.String())
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	address := listener.Addr().String()
	if err = listener.Close(); err != nil {
		t.Fatal(err)
	}
	t.Setenv("FEATUREPROOF_MEMORY_ADDRESS", address)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan int, 1)
	go func() { done <- runProjectMemoryAPI(ctx) }()
	query := projectmemory.QueryRequest{ProjectID: "project-1", Scope: projectmemory.Scope{Kind: projectmemory.ScopeRevision, RepositoryID: "33", CommitOID: strings.Repeat("a", 40)}, States: []projectmemory.State{projectmemory.StateApproved}, Authorities: []projectmemory.Authority{projectmemory.AuthorityHuman}, MaxSensitivity: projectmemory.SensitivityInternal, Limit: 10, MaxBytes: 1 << 20}
	payload, _ := json.Marshal(query)
	var response *http.Response
	var requestErr error
	for attempt := 0; attempt < 50; attempt++ {
		httpRequest, _ := http.NewRequest(http.MethodPost, "http://"+address+"/v1/memory", bytes.NewReader(payload))
		httpRequest.Header.Set("Authorization", "Bearer credential")
		httpRequest.Header.Set("FeatureProof-Operation", string(projectmemory.OperationQuery))
		response, requestErr = http.DefaultClient.Do(httpRequest)
		if requestErr == nil {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if requestErr != nil || response.StatusCode != http.StatusOK {
		t.Fatalf("API dispatcher response=%v err=%v", response, requestErr)
	}
	_ = response.Body.Close()
	cancel()
	if code := <-done; code != 0 {
		t.Fatalf("API shutdown code=%d", code)
	}
	t.Setenv("FEATUREPROOF_MEMORY_ADDRESS", "0.0.0.0:8082")
	if code := runProjectMemoryAPI(context.Background()); code != 3 {
		t.Fatalf("non-loopback API address code=%d", code)
	}
}

// mustMemoryComposition creates the configured service or fails the owning server test.
func mustMemoryComposition(t *testing.T) projectMemoryComposition {
	t.Helper()
	composition, err := composeProjectMemory()
	if err != nil {
		t.Fatal(err)
	}
	return composition
}

// TestProjectMemoryGitHubHandlerRejectsUnknownActionAndServesReviewQuery covers exact webhook operation routing.
func TestProjectMemoryGitHubHandlerRejectsUnknownActionAndServesReviewQuery(t *testing.T) {
	setProjectMemoryEnvironment(t, t.TempDir(), "22", "33")
	handler := projectMemoryGitHubHandler(mustMemoryComposition(t))
	digest, err := handler(context.Background(), githubpr.WebhookCommand{ActorID: 22, RepositoryID: 33, MemoryOperation: "query"})
	if err != nil || digest == "" {
		t.Fatalf("query digest=%q err=%v", digest, err)
	}
	if _, err = handler(context.Background(), githubpr.WebhookCommand{ActorID: 22, RepositoryID: 33, MemoryOperation: "observe"}); err == nil {
		t.Fatal("unknown GitHub memory operation accepted")
	}
	if _, err = handler(context.Background(), githubpr.WebhookCommand{ActorID: 99, RepositoryID: 33, MemoryOperation: "query"}); err == nil {
		t.Fatal("unauthorized GitHub actor accepted")
	}
	for _, operation := range []string{"approve", "invalidate"} {
		_, transitionErr := handler(context.Background(), githubpr.WebhookCommand{DeliveryID: "delivery-" + operation, ActorID: 22, RepositoryID: 33, MemoryOperation: operation, MemoryID: "missing", MemoryExpectedSequence: 0, MemoryExpectedVersion: 1, MemoryDecisionID: "decision-1"})
		if transitionErr == nil {
			t.Fatalf("missing memory accepted for %s", operation)
		}
	}
}
