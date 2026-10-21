package contractreview

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/asierhr/featureProof/internal/contractcatalog"
)

// runtimeApplication records calls so production transport routing is tested independently of service fixtures.
type runtimeApplication struct{ calls []string }

// Create records productive create routing and returns one safe view.
func (a *runtimeApplication) Create(context.Context, CreateCommand) (ReviewView, error) {
	a.calls = append(a.calls, "create")
	return ReviewView{ID: strings.Repeat("a", 24)}, nil
}

// Get records productive show routing and returns one safe view.
func (a *runtimeApplication) Get(context.Context, string, string) (ReviewView, error) {
	a.calls = append(a.calls, "show")
	return ReviewView{ID: strings.Repeat("a", 24)}, nil
}

// Decide records productive decision routing and returns one safe view.
func (a *runtimeApplication) Decide(context.Context, DecisionCommand) (ReviewView, error) {
	a.calls = append(a.calls, "decide")
	return ReviewView{ID: strings.Repeat("a", 24)}, nil
}

// Export records productive export routing and returns one safe receipt.
func (a *runtimeApplication) Export(context.Context, ExportCommand) (ExportReceipt, error) {
	a.calls = append(a.calls, "export")
	return ExportReceipt{ReviewID: strings.Repeat("a", 24)}, nil
}

// TestProductiveHTTPAndMCPRegisterCompleteLifecycle proves both servers route every shared operation.
func TestProductiveHTTPAndMCPRegisterCompleteLifecycle(t *testing.T) {
	application := &runtimeApplication{}
	api := APIAdapter{Service: application, Config: SurfaceConfig{PrincipalID: "actor", PolicyDigest: strings.Repeat("d", 64), Limits: reviewLimits(), Info: ExportInfo{Title: "contract", Version: "1"}}}
	token := "0123456789abcdef"
	handler := (HTTPHandler{API: api, MaxBody: 1 << 20, Auth: BearerAuthenticator{Token: []byte(token), Principal: "actor"}}).Handler()
	review := strings.Repeat("a", 24)
	requests := []*http.Request{
		httptest.NewRequest(http.MethodPost, "/v1/contract-reviews", strings.NewReader(`{"version":"1","candidate_ref":"candidate","baseline_ref":"baseline","idempotency_key":"key"}`)),
		httptest.NewRequest(http.MethodGet, "/v1/contract-reviews/"+review, nil),
		httptest.NewRequest(http.MethodPost, "/v1/contract-reviews/"+review+"/decisions", strings.NewReader(`{"version":"1","review_id":"`+review+`","idempotency_key":"key","expected_revision":0,"whole":"accept_all"}`)),
		httptest.NewRequest(http.MethodPost, "/v1/contract-reviews/"+review+"/exports", strings.NewReader(`{"version":"1","review_id":"`+review+`","idempotency_key":"key","target":"contract.json","expected_revision":1}`)),
	}
	for _, request := range requests {
		request.Header.Set("Authorization", "Bearer "+token)
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, request)
		if recorder.Code != http.StatusOK {
			t.Fatalf("route %s returned %d: %s", request.URL.Path, recorder.Code, recorder.Body.String())
		}
	}
	var protocol bytes.Buffer
	for index, tool := range []string{"contract_review_create_v1", "contract_review_show_v1", "contract_review_decide_v1", "contract_review_export_v1"} {
		arguments := []string{`{"version":"1","candidate_ref":"candidate","baseline_ref":"baseline","idempotency_key":"mcp"}`, `{"version":"1","review_id":"` + review + `"}`, `{"version":"1","review_id":"` + review + `","idempotency_key":"mcp","expected_revision":0,"whole":"accept_all"}`, `{"version":"1","review_id":"` + review + `","idempotency_key":"mcp","target":"mcp.json","expected_revision":1}`}[index]
		protocol.WriteString(`{"jsonrpc":"2.0","id":` + string(rune('1'+index)) + `,"method":"tools/call","params":{"name":"` + tool + `","arguments":` + arguments + `}}` + "\n")
	}
	var output bytes.Buffer
	if err := (MCPServer{Adapter: MCPAdapter{API: api}, MaxLine: 1 << 20}).Serve(context.Background(), &protocol, &output); err != nil || strings.Count(output.String(), `"content"`) != 4 {
		t.Fatalf("MCP lifecycle missing: %v %s", err, output.String())
	}
	if strings.Join(application.calls, ",") != "create,show,decide,export,create,show,decide,export" {
		t.Fatalf("unexpected routing: %v", application.calls)
	}
}

// TestRuntimeFailureEnvelopesAndProtocolBranches covers bounded status mapping and MCP negotiation without leaking errors.
func TestRuntimeFailureEnvelopesAndProtocolBranches(t *testing.T) {
	handler := HTTPHandler{}
	cases := map[ErrorCategory]int{CategoryInvalidInput: http.StatusBadRequest, CategoryUnauthorized: http.StatusForbidden, CategoryConflict: http.StatusConflict, CategoryInvalidated: http.StatusConflict, CategoryIncompatibleBaseline: http.StatusConflict, CategoryTargetExists: http.StatusConflict, CategoryLimitExceeded: http.StatusRequestEntityTooLarge, CategoryCancelled: http.StatusRequestTimeout, CategoryUnavailable: http.StatusServiceUnavailable, CategoryInternal: http.StatusInternalServerError}
	for category, status := range cases {
		recorder := httptest.NewRecorder()
		handler.write(recorder, nil, typedError(category, "safe", nil))
		if recorder.Code != status || strings.Contains(recorder.Body.String(), "safe") {
			t.Fatalf("category=%s status=%d body=%s", category, recorder.Code, recorder.Body.String())
		}
	}
	application := &runtimeApplication{}
	api := APIAdapter{Service: application, Config: SurfaceConfig{PrincipalID: "actor", PolicyDigest: strings.Repeat("d", 64), Limits: reviewLimits()}}
	input := strings.NewReader("{}\n" + `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"name":"","arguments":null}}` + "\n" + `{"jsonrpc":"2.0","id":2,"method":"tools/list","params":{"name":"","arguments":null}}` + "\n" + `{"jsonrpc":"2.0","id":3,"method":"unknown","params":{"name":"","arguments":null}}` + "\n" + `{"jsonrpc":"2.0","id":4,"method":"tools/call","params":{"name":"unknown","arguments":{}}}` + "\n")
	var output bytes.Buffer
	if err := (MCPServer{Adapter: MCPAdapter{API: api}, MaxLine: 4096}).Serve(context.Background(), input, &output); err != nil {
		t.Fatal(err)
	}
	for _, expected := range []string{"-32600", "protocolVersion", "contract_review_create_v1", `"additionalProperties":false`, `"required"`, `"properties"`, "-32601", `"isError":true`} {
		if !strings.Contains(output.String(), expected) {
			t.Fatalf("missing %s in %s", expected, output.String())
		}
	}
	recorder := httptest.NewRecorder()
	(HTTPHandler{API: api}).Handler().ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, "/v1/contract-reviews", strings.NewReader("{}")))
	if recorder.Code != http.StatusServiceUnavailable {
		t.Fatalf("unbounded HTTP composition accepted: %d", recorder.Code)
	}
	authenticated := (HTTPHandler{API: api, MaxBody: 1024, Auth: BearerAuthenticator{Token: []byte("0123456789abcdef"), Principal: "actor"}}).Handler()
	recorder = httptest.NewRecorder()
	authenticated.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/v1/contract-reviews/"+strings.Repeat("a", 24), nil))
	if recorder.Code != http.StatusForbidden {
		t.Fatalf("unauthenticated HTTP request status=%d", recorder.Code)
	}
	if _, err := api.createTrustedGitHub(context.Background(), CreateInput{Version: VersionV1, CandidateRef: "candidate", BaselineRef: "baseline", IdempotencyKey: "legacy"}, nil); err != nil {
		t.Fatalf("trusted GitHub compatibility route failed: %v", err)
	}
}

// TestAggregateProvenanceLimitIncludesConstraintsAndAllOperations proves no per-item truncation can evade the budget.
func TestAggregateProvenanceLimitIncludesConstraintsAndAllOperations(t *testing.T) {
	op1 := fixtureOperation("a-source", strings.Repeat("a", 64), "/one", "one", "200")
	op2 := fixtureOperation("a-source", strings.Repeat("a", 64), "/two", "two", "200")
	op1.DeclarativeConstraints = []contractcatalog.DeclarativeConstraint{{Kind: "validation", Provenance: op1.Provenance}}
	candidate := fixtureCatalog(t, "a-source", []contractcatalog.OperationDescriptor{op1, op2}, contractcatalog.CompletenessComplete, nil, contractcatalog.OmissionCounts{})
	baseline := fixtureCatalog(t, "b-source", nil, contractcatalog.CompletenessComplete, nil, contractcatalog.OmissionCounts{})
	limits := reviewLimits()
	limits.MaxProvenance = 1
	_, err := CreateArtifact(context.Background(), candidateFor(candidate), baselineFor(baseline), limits)
	errorCategory(t, err, CategoryLimitExceeded)
}

// TestProjectionDependenciesAndAllOfOrdering proves closed bidirectional groups and stable reducer order.
func TestProjectionDependenciesAndAllOfOrdering(t *testing.T) {
	base := fixtureOperation("a-source", strings.Repeat("a", 64), "/secure", "secure", "200")
	base.Responses["200"] = contractcatalog.Response{Content: map[string]contractcatalog.MediaType{"application/json": {Schema: &contractcatalog.Schema{AllOf: []*contractcatalog.Schema{{Type: "string"}, {Type: "integer"}, {Type: "boolean"}}}}}}
	candidate := deepClone(base)
	candidate.Responses["200"] = contractcatalog.Response{Content: map[string]contractcatalog.MediaType{"application/json": {Schema: &contractcatalog.Schema{AllOf: []*contractcatalog.Schema{{Type: "string"}}}}}}
	candidate.Security = []contractcatalog.SecurityRequirement{{Schemes: map[string][]string{"oauth": {"read"}}}}
	candidate.SecuritySchemes = map[string]contractcatalog.SecurityScheme{"oauth": {Name: "oauth", Type: "oauth2"}}
	baseCatalog := fixtureCatalog(t, "a-source", []contractcatalog.OperationDescriptor{base}, contractcatalog.CompletenessComplete, nil, contractcatalog.OmissionCounts{})
	candidateCatalog := fixtureCatalog(t, "a-source", []contractcatalog.OperationDescriptor{candidate}, contractcatalog.CompletenessComplete, nil, contractcatalog.OmissionCounts{})
	artifact, err := CreateArtifact(context.Background(), candidateFor(candidateCatalog), baselineFor(baseCatalog), reviewLimits())
	if err != nil {
		t.Fatal(err)
	}
	var security, scheme ReviewItem
	for _, item := range artifact.Items {
		if item.Path == "security" {
			security = item
		}
		if strings.HasPrefix(item.Path, "security_schemes/") {
			scheme = item
		}
	}
	if security.ID == "" || scheme.ID == "" || !contains(security.Dependencies, scheme.ID) || !contains(scheme.Dependencies, security.ID) {
		t.Fatalf("dependency group not bidirectional: security=%#v scheme=%#v", security, scheme)
	}
	_, _, _, err = ReduceDecision(context.Background(), artifact, 0, nil, DecisionInput{ExpectedRevision: 0, Items: []ItemDecision{{ItemID: scheme.ID, Value: DecisionAccept}}, IdempotencyKey: "open"}, "actor", fixedClock{}.Now(), candidateFor(candidateCatalog), baselineFor(baseCatalog), strings.Repeat("d", 64))
	if err == nil {
		t.Fatal("inverse-open dependency accepted")
	}
	_, state, projection, err := ReduceDecision(context.Background(), artifact, 0, nil, DecisionInput{ExpectedRevision: 0, Whole: WholeAccept, IdempotencyKey: "all"}, "actor", fixedClock{}.Now(), candidateFor(candidateCatalog), baselineFor(baseCatalog), strings.Repeat("d", 64))
	if err != nil || state != StateApproved || !decisionJSONEqual(projection.Operations, candidateCatalog.Operations()) {
		t.Fatalf("accept_all failed to reconstruct ordered allOf: state=%s err=%v", state, err)
	}
}

// contains reports exact membership for dependency assertions.
func contains(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

// TestPublicationMarkersRejectPreexistingIdenticalTargets proves reconciliation needs durable attempt evidence.
func TestPublicationMarkersRejectPreexistingIdenticalTargets(t *testing.T) {
	root := t.TempDir()
	raw := []byte(`{"openapi":"3.1.0"}`)
	if err := os.WriteFile(filepath.Join(root, "contract.json"), raw, 0600); err != nil {
		t.Fatal(err)
	}
	publisher, err := NewLocalPublisher(root)
	if err != nil {
		t.Fatal(err)
	}
	_, _, err = publisher.Inspect(context.Background(), "contract.json", strings.Repeat("a", 64))
	errorCategory(t, err, CategoryConflict)
	_, err = publisher.Publish(context.Background(), PublishRequest{Target: "contract.json", AttemptID: strings.Repeat("a", 64)}, raw, digestBytes(raw))
	errorCategory(t, err, CategoryTargetExists)
	_, err = publisher.Publish(context.Background(), PublishRequest{Target: "contract.json", Replace: true, ExpectedDigest: digestBytes(raw), AttemptID: strings.Repeat("a", 64)}, raw, digestBytes(raw))
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err = publisher.Inspect(context.Background(), "contract.json", strings.Repeat("a", 64)); err != nil {
		t.Fatal(err)
	}
}

// TestStoreReadsJournalLargerThanOneMiB proves configured MaxBytes, rather than Scanner's default, governs recovery.
func TestStoreReadsJournalLargerThanOneMiB(t *testing.T) {
	root := filepath.Join(t.TempDir(), "reviews")
	store, err := NewLocalStore(root)
	if err != nil {
		t.Fatal(err)
	}
	catalog := fixtureCatalog(t, "a-source", nil, contractcatalog.CompletenessComplete, nil, contractcatalog.OmissionCounts{})
	limits := reviewLimits()
	limits.MaxBytes = 2 << 20
	limits.MaxItems = 2000
	artifact, err := CreateArtifact(context.Background(), candidateFor(catalog), baselineFor(catalog), limits)
	if err != nil {
		t.Fatal(err)
	}
	bindings := make([]IdempotencyBinding, 7000)
	for index := range bindings {
		bindings[index] = IdempotencyBinding{Principal: "actor", Operation: "historical", Key: "key-" + strconv.Itoa(index), RequestDigest: strings.Repeat("d", 64)}
	}
	record := ReviewRecord{Artifact: artifact, State: StateCreated, Events: []AuditEvent{{Kind: "review_created", ActorID: "actor", ReviewID: artifact.ID, Digest: artifact.Digest}}, Idempotency: bindings}
	if _, err = store.Create(context.Background(), "actor", "large", strings.Repeat("d", 64), record); err != nil {
		t.Fatal(err)
	}
	restarted, err := NewLocalStore(root)
	if err != nil {
		t.Fatal(err)
	}
	loaded, err := restarted.Load(context.Background(), artifact.ID)
	if err != nil || len(loaded.Idempotency) != len(bindings)+1 {
		t.Fatalf("large journal not recovered: %v", err)
	}
}

// TestStoreBoundsLifecycleGrowthAndClosesDescriptors proves oversized aggregates fail before I/O and handles are releasable.
func TestStoreBoundsLifecycleGrowthAndClosesDescriptors(t *testing.T) {
	root := filepath.Join(t.TempDir(), "reviews")
	store, err := NewLocalStore(root)
	if err != nil {
		t.Fatal(err)
	}
	catalog := fixtureCatalog(t, "a-source", nil, contractcatalog.CompletenessComplete, nil, contractcatalog.OmissionCounts{})
	artifact, err := CreateArtifact(context.Background(), candidateFor(catalog), baselineFor(catalog), reviewLimits())
	if err != nil {
		t.Fatal(err)
	}
	record := ReviewRecord{Artifact: artifact, State: StateCreated, Events: make([]AuditEvent, artifact.Limits.MaxItems*4+1)}
	_, err = store.Create(context.Background(), "actor", "oversized", strings.Repeat("d", 64), record)
	errorCategory(t, err, CategoryLimitExceeded)
	if _, statErr := os.Stat(filepath.Join(root, artifact.ID)); !os.IsNotExist(statErr) {
		t.Fatalf("oversized aggregate reached disk: %v", statErr)
	}
	if err = store.Close(); err != nil {
		t.Fatal(err)
	}
	if err = store.Close(); err != nil {
		t.Fatal(err)
	}
	_, err = store.Load(context.Background(), artifact.ID)
	errorCategory(t, err, CategoryConflict)
	publisher, err := NewLocalPublisher(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err = publisher.Close(); err != nil {
		t.Fatal(err)
	}
	if err = publisher.Close(); err != nil {
		t.Fatal(err)
	}
}

// TestLocalStoreRejectsSynchronizedAggregateSwap proves a retained review descriptor cannot be redirected before reading authority.
func TestLocalStoreRejectsSynchronizedAggregateSwap(t *testing.T) {
	root := filepath.Join(t.TempDir(), "reviews")
	store, err := NewLocalStore(root)
	if err != nil {
		t.Fatal(err)
	}
	catalog := fixtureCatalog(t, "a-source", nil, contractcatalog.CompletenessComplete, nil, contractcatalog.OmissionCounts{})
	artifact, err := CreateArtifact(context.Background(), candidateFor(catalog), baselineFor(catalog), reviewLimits())
	if err != nil {
		t.Fatal(err)
	}
	record := ReviewRecord{Artifact: artifact, State: StateCreated, Events: []AuditEvent{{Kind: "review_created", ReviewID: artifact.ID}}}
	if _, err = store.Create(context.Background(), "actor", "swap-create", strings.Repeat("d", 64), record); err != nil {
		t.Fatal(err)
	}
	reviewPath := filepath.Join(root, artifact.ID)
	store.checkpoint = func(phase string) {
		if phase != "load_directory_opened" {
			return
		}
		store.checkpoint = nil
		if renameErr := os.Rename(reviewPath, reviewPath+"-detached"); renameErr != nil {
			t.Fatalf("aggregate swap failed: %v", renameErr)
		}
		if makeErr := os.Mkdir(reviewPath, 0700); makeErr != nil {
			t.Fatalf("replacement aggregate failed: %v", makeErr)
		}
	}
	_, err = store.Load(context.Background(), artifact.ID)
	errorCategory(t, err, CategoryConflict)
	_ = store.Close()
}

// TestStableRootDescriptorRejectsReplacement proves store and publisher do not follow a swapped root.
func TestStableRootDescriptorRejectsReplacement(t *testing.T) {
	parent := t.TempDir()
	root := filepath.Join(parent, "root")
	if err := os.Mkdir(root, 0700); err != nil {
		t.Fatal(err)
	}
	publisher, err := NewLocalPublisher(root)
	if err != nil {
		t.Fatal(err)
	}
	old := filepath.Join(parent, "old")
	if err = os.Rename(root, old); err != nil {
		t.Fatal(err)
	}
	if err = os.Mkdir(root, 0700); err != nil {
		t.Fatal(err)
	}
	_, err = publisher.Publish(context.Background(), PublishRequest{Target: "x.json"}, []byte("x"), digestBytes([]byte("x")))
	errorCategory(t, err, CategoryConflict)
	storeRoot := filepath.Join(parent, "store")
	store, err := NewLocalStore(storeRoot)
	if err != nil {
		t.Fatal(err)
	}
	storeOld := filepath.Join(parent, "store-old")
	if err = os.Rename(storeRoot, storeOld); err != nil {
		t.Fatal(err)
	}
	if err = os.Mkdir(storeRoot, 0700); err != nil {
		t.Fatal(err)
	}
	_, err = store.Load(context.Background(), strings.Repeat("a", 24))
	errorCategory(t, err, CategoryConflict)
}

// TestExporterPlacesAuditAtSmallestUnits proves parameter, body, response, header, media and schema carry local audit metadata.
func TestExporterPlacesAuditAtSmallestUnits(t *testing.T) {
	projection, limits := approvedProjectionFixture(t)
	op := &projection.Operations[0]
	op.Parameters = []contractcatalog.Parameter{{Name: "q", In: "query", Schema: &contractcatalog.Schema{Type: "string"}}}
	op.RequestBody = &contractcatalog.RequestBody{Content: map[string]contractcatalog.MediaType{"application/json": {Schema: &contractcatalog.Schema{Type: "object", Properties: map[string]*contractcatalog.Schema{"id": {Type: "string"}}}}}}
	op.Responses["200"] = contractcatalog.Response{Headers: map[string]contractcatalog.Header{"X-ID": {Schema: &contractcatalog.Schema{Type: "string"}}}, Content: map[string]contractcatalog.MediaType{"application/json": {Schema: &contractcatalog.Schema{Type: "object"}}}}
	op.Security = []contractcatalog.SecurityRequirement{{Schemes: map[string][]string{"oauth": {"read"}}}}
	op.SecuritySchemes = map[string]contractcatalog.SecurityScheme{"oauth": {Name: "oauth", Type: "oauth2"}}
	projection.SecuritySchemes = map[string]contractcatalog.SecurityScheme{"oauth": {Name: "oauth", Type: "oauth2"}}
	op.Claims = contractcatalog.ClaimsForOperation(*op, op.Provenance[0])
	second := deepClone(*op)
	second.Key.Path = "/shared"
	second.Provenance[0].SourceID = "shared-source"
	second.Provenance[0].SourceDigest = strings.Repeat("e", 64)
	second.Claims = contractcatalog.ClaimsForOperation(second, second.Provenance[0])
	projection.Operations = append(projection.Operations, second)
	projection.Diagnostics = []contractcatalog.SourceDiagnostic{{Code: "header-local", SafeLocation: "src/api/responses/200/headers/X-ID/schema"}, {Code: "unrelated", SafeLocation: "src/api/request_body/schema"}}
	projection.Omissions = contractcatalog.OmissionCounts{Extensions: 1}
	digest, _ := projectionDigest(context.Background(), projection, limits.MaxBytes)
	projection.Binding.ProjectionDigest = digest
	raw, _, err := (Exporter{}).Export(context.Background(), projection, ExportInfo{Title: "audit", Version: "1"}, limits)
	if err != nil {
		t.Fatal(err)
	}
	var document map[string]any
	if json.Unmarshal(raw, &document) != nil {
		t.Fatal("invalid json")
	}
	operation := document["paths"].(map[string]any)[op.Key.Path].(map[string]any)[strings.ToLower(op.Key.Method)].(map[string]any)
	response := operation["responses"].(map[string]any)["200"].(map[string]any)
	header := response["headers"].(map[string]any)["X-ID"].(map[string]any)
	headerSchema := header["schema"].(map[string]any)
	media := response["content"].(map[string]any)["application/json"].(map[string]any)
	mediaSchema := media["schema"].(map[string]any)
	security := operation["security"].([]any)[0].(map[string]any)
	units := []map[string]any{operation["parameters"].([]any)[0].(map[string]any), operation["requestBody"].(map[string]any), response, header, headerSchema, media, mediaSchema, security}
	for _, unit := range units {
		if _, ok := unit["x-featureproof-provenance"]; !ok {
			t.Fatalf("unit lacks provenance: %#v", unit)
		}
	}
	if _, ok := security["x-featureproof-scheme-audit"].(map[string]any)["oauth"]; !ok {
		t.Fatalf("security scope lacks local audit: %#v", security)
	}
	diagnostics, _ := headerSchema["x-featureproof-diagnostics"].([]any)
	if len(diagnostics) != 1 || strings.Contains(string(raw), `"x-featureproof-omissions":{"examples":0,"extensions":1`) && headerSchema["x-featureproof-omissions"] != nil {
		t.Fatalf("local diagnostics/omissions misattributed: %#v", headerSchema)
	}
	scheme := document["components"].(map[string]any)["securitySchemes"].(map[string]any)["oauth"].(map[string]any)
	if _, ok := scheme["x-featureproof-provenance"]; !ok {
		t.Fatalf("scheme lacks provenance: %#v", scheme)
	}
	if len(scheme["x-featureproof-provenance"].([]any)) != 2 {
		t.Fatalf("shared scheme lineage overwritten: %#v", scheme)
	}
}
