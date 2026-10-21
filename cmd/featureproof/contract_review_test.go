package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/asierhr/featureProof/internal/contractcatalog"
	"github.com/asierhr/featureProof/internal/contractreview"
)

// commandReviewSnapshot creates one registry-valid neutral source for product-composition tests.
func commandReviewSnapshot(id string, class contractcatalog.SourceClass, operations []contractcatalog.OperationDescriptor) contractcatalog.SourceSnapshot {
	digest := strings.Repeat(id[:1], 64)
	for index := range operations {
		provenance := contractcatalog.Provenance{SourceID: id, RepositoryID: "repo-1", Revision: strings.Repeat("a", 40), Class: class, SourceDigest: digest, AdapterVersion: "fixture-v1", SafeLocation: "src/api", Confidence: contractcatalog.ConfidenceHigh, Completeness: contractcatalog.CompletenessComplete}
		operations[index].Provenance = []contractcatalog.Provenance{provenance}
		operations[index].Classes = []contractcatalog.SourceClass{class}
		operations[index].Confidence = contractcatalog.ConfidenceHigh
		operations[index].Completeness = contractcatalog.CompletenessComplete
		operations[index].Claims = contractcatalog.ClaimsForOperation(operations[index], provenance)
	}
	return contractcatalog.SourceSnapshot{Version: contractcatalog.VersionV1, Source: contractcatalog.SourceSummary{ID: id, RepositoryID: "repo-1", Revision: strings.Repeat("a", 40), Kind: "fixture-" + id, Class: class, Digest: digest, AdapterVersion: "fixture-v1", Confidence: contractcatalog.ConfidenceHigh, Completeness: contractcatalog.CompletenessComplete}, Operations: operations}
}

// commandProjectionPolicy supplies a non-empty production-shaped redaction authority for composition tests.
func commandProjectionPolicy() contractcatalog.ProjectionPolicy {
	return contractcatalog.ProjectionPolicy{MaxStringBytes: 2048, AllowedURLSchemes: []string{"https"}, SensitiveNames: []string{"password", "secret", "token"}, SensitivePaths: []string{"/admin"}, SensitiveHeaderNames: []string{"Authorization", "Cookie", "Set-Cookie"}, ForbiddenValues: []string{"CANARY_SECRET"}}
}

// commandPolicyDigest binds the exact serialized projection policy used by composition.
func commandPolicyDigest(policy contractcatalog.ProjectionPolicy) string {
	raw, _ := json.Marshal(policy)
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

// commandReviewCatalog builds the exact digest later pinned into trusted command configuration.
func commandReviewCatalog(t *testing.T, snapshot contractcatalog.SourceSnapshot) contractcatalog.OperationCatalog {
	t.Helper()
	limits := contractcatalog.Limits{MaxSources: 32, MaxOperations: 4096, MaxClaims: 65536, MaxBytes: 32 << 20, MaxDepth: 64, MaxDiagnostics: 4096, MaxLocationBytes: 512}
	policy := commandProjectionPolicy()
	catalog, err := buildConfiguredReviewCatalog(context.Background(), []contractcatalog.SourceSnapshot{snapshot}, limits, policy)
	if err != nil {
		t.Fatal(err)
	}
	return catalog
}

// startProductReviewAPI binds a free loopback address and runs the real composition entrypoint until the returned cancel is called.
func startProductReviewAPI(t *testing.T, token string) (string, context.CancelFunc, <-chan int) {
	t.Helper()
	probe, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	address := probe.Addr().String()
	_ = probe.Close()
	t.Setenv("FEATUREPROOF_CONTRACT_REVIEW_ADDRESS", address)
	t.Setenv("FEATUREPROOF_CONTRACT_REVIEW_API_TOKEN", token)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan int, 1)
	go func() {
		done <- runWithContext(ctx, []string{"contract-review", "api", "serve"}, strings.NewReader(""), io.Discard, io.Discard)
	}()
	return "http://" + address, cancel, done
}

// productReviewRequest retries only listener startup and then returns the real HTTP response for code and body assertions.
func productReviewRequest(t *testing.T, method, target, token, body string) *http.Response {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for {
		request, err := http.NewRequest(method, target, strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		if token != "" {
			request.Header.Set("Authorization", "Bearer "+token)
		}
		request.Header.Set("Content-Type", "application/json")
		response, requestErr := http.DefaultClient.Do(request)
		if requestErr == nil {
			return response
		}
		if time.Now().After(deadline) {
			t.Fatal(requestErr)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// writeProductReviewConfig writes a complete trusted review composition and its authorized export root for lifecycle tests.
func writeProductReviewConfig(t *testing.T, root string) string {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(root, "exports"), 0700); err != nil {
		t.Fatal(err)
	}
	candidateSnapshot := commandReviewSnapshot("candidate", contractcatalog.ClassDerived, []contractcatalog.OperationDescriptor{{Key: contractcatalog.OperationKey{Method: "GET", Path: "/new"}, Responses: map[string]contractcatalog.Response{"200": {}}}})
	baselineSnapshot := commandReviewSnapshot("baseline", contractcatalog.ClassAuthoritative, []contractcatalog.OperationDescriptor{})
	candidateCatalog, baselineCatalog := commandReviewCatalog(t, candidateSnapshot), commandReviewCatalog(t, baselineSnapshot)
	policy := commandProjectionPolicy()
	config := contractReviewConfig{Version: contractreview.VersionV1, Principal: "actor", ExportRoot: "exports", Title: "contract", DocumentVersion: "1", PolicyDigest: commandPolicyDigest(policy), ProjectionPolicy: policy, Candidates: map[string]contractReviewCatalogConfig{"candidate": {Identity: contractreview.CatalogIdentity{Version: candidateCatalog.Version(), Digest: candidateCatalog.Digest(), RepositoryID: "repo-1", Commit: strings.Repeat("a", 40), Scope: "service"}, Analyzers: []contractreview.AnalyzerIdentity{{ID: "fixture", Version: "v1"}}, Sources: []contractcatalog.SourceSnapshot{candidateSnapshot}}}, Baselines: map[string]contractReviewCatalogConfig{"baseline": {Identity: contractreview.CatalogIdentity{Version: baselineCatalog.Version(), Digest: baselineCatalog.Digest(), RepositoryID: "repo-1", Commit: strings.Repeat("0", 40), Scope: "service"}, Kind: "openapi_authoritative", Sources: []contractcatalog.SourceSnapshot{baselineSnapshot}}}}
	raw, _ := json.Marshal(config)
	path := filepath.Join(root, "contract-review-config.json")
	if err := os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	return path
}

// TestContractReviewCLICompositionCreatesDurableReview exercises the executable's trusted local configuration path.
func TestContractReviewCLICompositionCreatesDurableReview(t *testing.T) {
	temporary := t.TempDir()
	previous, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err = os.Chdir(temporary); err != nil {
		t.Fatal(err)
	}
	defer os.Chdir(previous)
	if err = os.MkdirAll(filepath.Join(temporary, "exports"), 0700); err != nil {
		t.Fatal(err)
	}
	candidateSnapshot := commandReviewSnapshot("candidate", contractcatalog.ClassDerived, []contractcatalog.OperationDescriptor{{Key: contractcatalog.OperationKey{Method: "GET", Path: "/new"}, Responses: map[string]contractcatalog.Response{"200": {}}}})
	baselineSnapshot := commandReviewSnapshot("baseline", contractcatalog.ClassAuthoritative, []contractcatalog.OperationDescriptor{})
	candidateCatalog, baselineCatalog := commandReviewCatalog(t, candidateSnapshot), commandReviewCatalog(t, baselineSnapshot)
	policy := commandProjectionPolicy()
	config := contractReviewConfig{Version: contractreview.VersionV1, Principal: "actor", ExportRoot: "exports", Title: "contract", DocumentVersion: "1", PolicyDigest: commandPolicyDigest(policy), ProjectionPolicy: policy, Candidates: map[string]contractReviewCatalogConfig{"candidate": {Identity: contractreview.CatalogIdentity{Version: candidateCatalog.Version(), Digest: candidateCatalog.Digest(), RepositoryID: "repo-1", Commit: strings.Repeat("a", 40), Scope: "service"}, Analyzers: []contractreview.AnalyzerIdentity{{ID: "fixture", Version: "v1"}}, Sources: []contractcatalog.SourceSnapshot{candidateSnapshot}}}, Baselines: map[string]contractReviewCatalogConfig{"baseline": {Identity: contractreview.CatalogIdentity{Version: baselineCatalog.Version(), Digest: baselineCatalog.Digest(), RepositoryID: "repo-1", Commit: strings.Repeat("0", 40), Scope: "service"}, Kind: "openapi_authoritative", Sources: []contractcatalog.SourceSnapshot{baselineSnapshot}}}}
	raw, _ := json.Marshal(config)
	configPath := filepath.Join(temporary, "config.json")
	if err = os.WriteFile(configPath, raw, 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("FEATUREPROOF_CONTRACT_REVIEW_CONFIG", configPath)
	var output bytes.Buffer
	if code := runContractReviewCommand(context.Background(), []string{"create", "--candidate", "candidate", "--baseline", "baseline", "--idempotency-key", "request"}, &output); code != 0 {
		t.Fatalf("command code=%d", code)
	}
	var view contractreview.ReviewView
	if json.Unmarshal(output.Bytes(), &view) != nil || view.State != contractreview.StateCreated {
		t.Fatalf("invalid CLI view: %s", output.String())
	}
	if _, err = os.Stat(filepath.Join(temporary, ".featureproof", "contract-reviews", view.ID, "checkpoint.json")); err != nil {
		t.Fatal(err)
	}
	protocol := strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"contract_review_show_v1","arguments":{"version":"1","review_id":"` + view.ID + `"}}}` + "\n" + `{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"contract_review_decide_v1","arguments":{"version":"1","review_id":"` + view.ID + `","idempotency_key":"approve","expected_revision":0,"whole":"accept_all"}}}` + "\n" + `{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"contract_review_export_v1","arguments":{"version":"1","review_id":"` + view.ID + `","idempotency_key":"export","target":"contract.json","expected_revision":1}}}` + "\n")
	var mcpOutput bytes.Buffer
	if code := runContractReviewMCP(context.Background(), protocol, &mcpOutput); code != 0 || strings.Count(mcpOutput.String(), `"content"`) != 3 {
		t.Fatalf("MCP vertical code=%d output=%s", code, mcpOutput.String())
	}
	if _, err = os.Stat(filepath.Join(temporary, "exports", "contract.json")); err != nil {
		t.Fatalf("MCP export missing: %v", err)
	}
	token := "0123456789abcdef"
	baseURL, cancelAPI, apiDone := startProductReviewAPI(t, token)
	unauthorized := productReviewRequest(t, http.MethodGet, baseURL+"/v1/contract-reviews/"+view.ID, "wrong-token-value", "")
	_, _ = io.Copy(io.Discard, unauthorized.Body)
	_ = unauthorized.Body.Close()
	if unauthorized.StatusCode != http.StatusForbidden {
		t.Fatalf("request-local bearer status=%d", unauthorized.StatusCode)
	}
	response := productReviewRequest(t, http.MethodGet, baseURL+"/v1/contract-reviews/"+view.ID, token, "")
	responseBody, _ := io.ReadAll(response.Body)
	_ = response.Body.Close()
	if response.StatusCode != http.StatusOK || !strings.Contains(string(responseBody), view.ID) {
		t.Fatalf("API vertical status=%d body=%s", response.StatusCode, responseBody)
	}
	var current contractreview.ReviewView
	if err = json.Unmarshal(responseBody, &current); err != nil {
		t.Fatal(err)
	}
	exportRaw, _ := json.Marshal(contractreview.ExportInput{Version: contractreview.VersionV1, ReviewID: view.ID, IdempotencyKey: "api-export", ExpectedRevision: current.Revision, Target: "api-contract.json"})
	response = productReviewRequest(t, http.MethodPost, baseURL+"/v1/contract-reviews/"+view.ID+"/exports", token, string(exportRaw))
	exportResponse, _ := io.ReadAll(response.Body)
	_ = response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("API export status=%d body=%s", response.StatusCode, exportResponse)
	}
	cancelAPI()
	if code := <-apiDone; code != 0 {
		t.Fatalf("API shutdown code=%d", code)
	}
	if _, err = os.Stat(filepath.Join(temporary, "exports", "api-contract.json")); err != nil {
		t.Fatal(err)
	}
	restartURL, cancelRestart, restartDone := startProductReviewAPI(t, token)
	response = productReviewRequest(t, http.MethodGet, restartURL+"/v1/contract-reviews/"+view.ID, token, "")
	restartBody, _ := io.ReadAll(response.Body)
	_ = response.Body.Close()
	if response.StatusCode != http.StatusOK || !strings.Contains(string(restartBody), view.ID) {
		t.Fatalf("API restart status=%d body=%s", response.StatusCode, restartBody)
	}
	cancelRestart()
	if code := <-restartDone; code != 0 {
		t.Fatalf("API restart shutdown code=%d", code)
	}
	t.Setenv("FEATUREPROOF_CONTRACT_REVIEW_ADDRESS", "0.0.0.0:8081")
	if code := run([]string{"contract-review", "api", "serve"}, strings.NewReader(""), io.Discard, io.Discard); code != 3 {
		t.Fatalf("main API dispatch code=%d", code)
	}
	if code := runContractReviewCommand(context.Background(), []string{"unknown"}, &bytes.Buffer{}); code != 2 {
		t.Fatalf("invalid command code=%d", code)
	}
}

// TestContractReviewCLICompositionRejectsMissingAndManipulatedConfig covers fail-closed command composition.
func TestContractReviewCLICompositionRejectsMissingAndManipulatedConfig(t *testing.T) {
	t.Setenv("FEATUREPROOF_CONTRACT_REVIEW_CONFIG", filepath.Join(t.TempDir(), "missing.json"))
	if code := runContractReviewCommand(context.Background(), []string{"show", "--review", strings.Repeat("a", 24)}, &bytes.Buffer{}); code != 3 {
		t.Fatalf("missing config code=%d", code)
	}
	path := filepath.Join(t.TempDir(), "bad.json")
	_ = os.WriteFile(path, []byte(`{"Version":"2"}`), 0600)
	t.Setenv("FEATUREPROOF_CONTRACT_REVIEW_CONFIG", path)
	if _, err := composeContractReviewCLI(context.Background()); err == nil {
		t.Fatal("unsupported config accepted")
	}
}

// TestContractReviewCompositionBoundaryBranches covers resolver misses, authorization denial and command validation.
func TestContractReviewCompositionBoundaryBranches(t *testing.T) {
	resolver := configuredReviewResolver{candidates: map[string]contractreview.CandidateSnapshot{"candidate": {}}, baselines: map[string]contractreview.BaselineSnapshot{"baseline": {}}}
	if _, err := resolver.ResolveCandidate(context.Background(), "candidate"); err != nil {
		t.Fatal(err)
	}
	if _, err := resolver.ResolveCandidate(context.Background(), "missing"); err == nil {
		t.Fatal("missing candidate accepted")
	}
	if _, err := resolver.ResolveBaseline(context.Background(), "baseline"); err != nil {
		t.Fatal(err)
	}
	if _, err := resolver.ResolveBaseline(context.Background(), "missing"); err == nil {
		t.Fatal("missing baseline accepted")
	}
	authorizer := configuredReviewAuthorizer{principal: "actor", repositories: map[string]bool{"repo": true}}
	if err := authorizer.Authorize(context.Background(), "actor", contractreview.ActionRead, "repo", ""); err != nil {
		t.Fatal(err)
	}
	if err := authorizer.Authorize(context.Background(), "other", contractreview.ActionRead, "repo", ""); err == nil {
		t.Fatal("unauthorized actor accepted")
	}
	limits := contractcatalog.Limits{MaxSources: 1, MaxOperations: 1, MaxClaims: 1, MaxBytes: 1024, MaxDepth: 4, MaxDiagnostics: 1, MaxLocationBytes: 64}
	policy := contractcatalog.ProjectionPolicy{MaxStringBytes: 64, AllowedURLSchemes: []string{"https"}, SensitiveNames: []string{}, SensitivePaths: []string{}, SensitiveHeaderNames: []string{}, ForbiddenValues: []string{}}
	if _, err := buildConfiguredReviewCatalog(context.Background(), nil, limits, policy); err == nil {
		t.Fatal("empty catalog accepted")
	}
}
