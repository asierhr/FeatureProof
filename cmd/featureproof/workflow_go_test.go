package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/asierhr/featureProof/internal/agentplanning"
	"github.com/asierhr/featureProof/internal/contractcatalog"
	"github.com/asierhr/featureProof/internal/goinspector"
	"github.com/asierhr/featureProof/internal/specification"
	"github.com/asierhr/featureProof/internal/springinspector"
	"github.com/asierhr/featureProof/internal/validationworkflow"
)

// TestWorkflowGoContractReducerAdmitsExactRevisionAndPolicy proves the product composition reaches planning only through the neutral policy gate.
func TestWorkflowGoContractReducerAdmitsExactRevisionAndPolicy(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "app"), 0700); err != nil {
		t.Fatal(err)
	}
	source := []byte("package app\nimport \"net/http\"\nfunc health(w http.ResponseWriter,r *http.Request){w.WriteHeader(200)}\nfunc routes(){http.HandleFunc(\"GET /health\",health)}")
	if err := os.WriteFile(filepath.Join(root, "app", "main.go"), source, 0600); err != nil {
		t.Fatal(err)
	}
	projection := contractcatalog.ProjectionPolicy{MaxStringBytes: 1024, AllowedURLSchemes: []string{"https"}, SensitiveNames: []string{}, SensitivePaths: []string{}, SensitiveHeaderNames: []string{}, ForbiddenValues: []string{}}
	limits := goinspector.Limits{MaxFiles: 10, MaxBytes: 1 << 20, MaxPackages: 5, MaxASTNodes: 1000, MaxDepth: 10, MaxTypes: 20, MaxOperations: 10, MaxDiagnostics: 10, MaxDuration: 30 * time.Second}
	reducer := workflowGoContractReducer{root: root, repositoryID: "repo", revision: "0123456789abcdef0123456789abcdef01234567", config: workflowGoInspectorConfig{Roots: []string{"app"}, GOOS: "linux", GOARCH: "amd64", Limits: limits, EntrySymbols: []string{"routes"}}, selections: map[string][]workflowOperationSelection{"c1": {{Method: "GET", Path: "/health"}}}, projection: projection}
	reduced, err := reducer.Reduce(context.Background(), root, specification.Specification{Constraints: []specification.Constraint{{ID: "c1"}}})
	if err != nil {
		t.Fatal(err)
	}
	if reduced.CatalogDigest() == "" || len(reduced.RetainedOperations()) != 1 {
		t.Fatalf("derived context missing: %+v", reduced)
	}
	if _, err = reducer.Reduce(context.Background(), root+"-other", specification.Specification{}); err == nil {
		t.Fatal("unauthorized contract root accepted")
	}
	authorized := validationworkflow.ContextWithContractAuthority(context.Background(), validationworkflow.RevisionContext{RepositoryID: "repo", CommitOID: "0123456789abcdef0123456789abcdef01234567", Roots: []string{"app"}, ChangedPaths: []string{"app/main.go"}})
	if _, err = reducer.Reduce(authorized, root, specification.Specification{Constraints: []specification.Constraint{{ID: "c1"}}}); err != nil {
		t.Fatalf("verified authority rejected: %v", err)
	}
	drift := validationworkflow.ContextWithContractAuthority(context.Background(), validationworkflow.RevisionContext{RepositoryID: "repo", CommitOID: "ffffffffffffffffffffffffffffffffffffffff", Roots: []string{"app"}})
	if _, err = reducer.Reduce(drift, root, specification.Specification{}); err == nil {
		t.Fatal("revision authority drift accepted")
	}
}

// TestWorkflowCombinedContractReducerCoalescesSources verifies one product path builds a shared authoritative-plus-derived catalog.
func TestWorkflowCombinedContractReducerCoalescesSources(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "app"), 0700); err != nil {
		t.Fatal(err)
	}
	goSource := []byte("package app\nimport \"net/http\"\nfunc health(w http.ResponseWriter,r *http.Request){w.WriteHeader(200)}\nfunc routes(){http.HandleFunc(\"GET /health\",health)}")
	if err := os.WriteFile(filepath.Join(root, "app", "main.go"), goSource, 0600); err != nil {
		t.Fatal(err)
	}
	springRoot := filepath.Join(root, "src", "main", "java", "demo")
	if err := os.MkdirAll(springRoot, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(springRoot, "Health.java"), []byte(`package demo; import org.springframework.web.bind.annotation.*; @RestController class Health {@GetMapping("/spring") String health(){return "ok";}}`), 0600); err != nil {
		t.Fatal(err)
	}
	openAPI := `openapi: 3.1.0
info: {title: test, version: "1"}
paths:
  /health:
    get:
      operationId: health
      responses:
        "200": {description: ok}
`
	openAPIPath := filepath.Join(root, "api.yaml")
	if err := os.WriteFile(openAPIPath, []byte(openAPI), 0600); err != nil {
		t.Fatal(err)
	}
	projection := contractcatalog.ProjectionPolicy{MaxStringBytes: 1024, AllowedURLSchemes: []string{"https"}, SensitiveNames: []string{}, SensitivePaths: []string{}, SensitiveHeaderNames: []string{}, ForbiddenValues: []string{}}
	limits := goinspector.Limits{MaxFiles: 10, MaxBytes: 1 << 20, MaxPackages: 5, MaxASTNodes: 1000, MaxDepth: 10, MaxTypes: 20, MaxOperations: 10, MaxDiagnostics: 10, MaxDuration: time.Second}
	springLimits := springinspector.Limits{MaxSourceSets: 2, MaxRoots: 2, MaxFiles: 10, MaxBytes: 1 << 20, MaxUnits: 10, MaxTokens: 1000, MaxDepth: 10, MaxTypes: 20, MaxSymbols: 100, MaxSchemaNodes: 100, MaxOperations: 10, MaxCrossProduct: 10, MaxDiagnostics: 10, MaxStringBytes: 1024, MaxDuration: time.Second}
	springConfig := &workflowSpringInspectorConfig{RootIdentity: "combined-checkout", FrameworkIdentities: springinspector.SupportedFrameworkIdentities(), SourceSets: []springinspector.SourceSet{{ID: "main", Root: "src/main/java"}}, JavaRelease: 17, Generated: springinspector.GeneratedExclude, Limits: springLimits}
	reducer := workflowCombinedContractReducer{openAPIPath: openAPIPath, root: root, authorizedRef: "both", repositoryID: "repo", revision: "0123456789abcdef0123456789abcdef01234567", config: workflowGoInspectorConfig{Roots: []string{"app"}, GOOS: "linux", GOARCH: "amd64", Limits: limits, EntrySymbols: []string{"routes"}}, spring: springConfig, sources: []string{"openapi", goinspector.SourceKind, springinspector.SourceKind}, selections: map[string][]workflowOperationSelection{"c1": {{Method: "GET", Path: "/health"}}}, projection: projection}
	reduced, err := reducer.Reduce(context.Background(), "both", specification.Specification{Constraints: []specification.Constraint{{ID: "c1"}}})
	if err != nil {
		t.Fatal(err)
	}
	if len(reduced.RetainedOperations()) != 1 || reduced.CatalogDigest() == "" {
		t.Fatalf("combined context missing: %+v", reduced)
	}
	withoutOpenAPI := reducer
	withoutOpenAPI.sources, withoutOpenAPI.openAPIPath = []string{goinspector.SourceKind, springinspector.SourceKind}, ""
	if _, err := withoutOpenAPI.Reduce(context.Background(), "both", specification.Specification{Constraints: []specification.Constraint{{ID: "c1"}}}); err != nil {
		t.Fatalf("explicit Go+Spring composition rejected: %v", err)
	}
	springOnly := reducer
	springOnly.sources, springOnly.openAPIPath, springOnly.config = []string{springinspector.SourceKind}, "", workflowGoInspectorConfig{}
	springOnly.selections = map[string][]workflowOperationSelection{"spring": {{Method: "GET", Path: "/spring"}}}
	if _, err := springOnly.Reduce(context.Background(), "both", specification.Specification{Constraints: []specification.Constraint{{ID: "spring"}}}); err != nil {
		t.Fatalf("explicit Spring-only registry rejected: %v", err)
	}
	springAuthority := validationworkflow.ContextWithContractAuthority(context.Background(), validationworkflow.RevisionContext{RepositoryID: "repo", CommitOID: "0123456789abcdef0123456789abcdef01234567", ChangedPaths: []string{"src/main/java/demo/Health.java"}})
	if _, err := springOnly.Reduce(springAuthority, "both", specification.Specification{Constraints: []specification.Constraint{{ID: "spring"}}}); err != nil {
		t.Fatalf("Spring changed-path authority rejected: %v", err)
	}
	missingSpring := reducer
	missingSpring.sources, missingSpring.spring = []string{springinspector.SourceKind, goinspector.SourceKind}, nil
	if _, err := missingSpring.Reduce(context.Background(), "both", specification.Specification{}); err == nil {
		t.Fatal("source list without configured adapter accepted")
	}
	unknown := reducer
	unknown.sources = []string{"unknown", goinspector.SourceKind}
	if _, err := unknown.Reduce(context.Background(), "both", specification.Specification{}); err == nil {
		t.Fatal("unknown source in sealed list accepted")
	}
	noDuration := reducer
	noDuration.sources, noDuration.spring, noDuration.config = []string{"openapi"}, nil, workflowGoInspectorConfig{}
	if _, err := noDuration.Reduce(context.Background(), "both", specification.Specification{}); err == nil {
		t.Fatal("source list without finite duration accepted")
	}
	if _, err := reducer.Reduce(context.Background(), "payload-selected", specification.Specification{}); err == nil {
		t.Fatal("payload changed authorized sources")
	}
	authorized := validationworkflow.ContextWithContractAuthority(context.Background(), validationworkflow.RevisionContext{RepositoryID: "repo", CommitOID: "0123456789abcdef0123456789abcdef01234567", Roots: []string{"app"}})
	if _, err := reducer.Reduce(authorized, "both", specification.Specification{Constraints: []specification.Constraint{{ID: "c1"}}}); err != nil {
		t.Fatalf("combined verified authority rejected: %v", err)
	}
	drift := validationworkflow.ContextWithContractAuthority(context.Background(), validationworkflow.RevisionContext{RepositoryID: "other", CommitOID: "0123456789abcdef0123456789abcdef01234567", Roots: []string{"app"}})
	if _, err := reducer.Reduce(drift, "both", specification.Specification{}); err == nil {
		t.Fatal("combined authority drift accepted")
	}
	if err := os.WriteFile(filepath.Join(springRoot, "Health.java"), []byte(`package demo; import org.springframework.web.bind.annotation.*; @RestController class Health {@GetMapping("/health") String health(){return "ok";}}`), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := reducer.Reduce(context.Background(), "both", specification.Specification{Constraints: []specification.Constraint{{ID: "c1"}}}); err == nil {
		t.Fatal("Spring conflict with other configured adapters was admitted")
	}
}

// TestWorkflowContractReducerForSelectsSharedAdapters verifies every public adapter receives the same reducer through the shared service composition.
func TestWorkflowContractReducerForSelectsSharedAdapters(t *testing.T) {
	projection := contractcatalog.ProjectionPolicy{MaxStringBytes: 128, AllowedURLSchemes: []string{"https"}, SensitiveNames: []string{}, SensitivePaths: []string{}, SensitiveHeaderNames: []string{}, ForbiddenValues: []string{}}
	goConfig := &workflowGoInspectorConfig{}
	if _, ok := workflowContractReducerFor(workflowConfig{}, projection).(workflowContractReducer); !ok {
		t.Fatal("openapi reducer not selected")
	}
	if _, ok := workflowContractReducerFor(workflowConfig{ContractSource: goinspector.SourceKind, GoInspector: goConfig}, projection).(workflowGoContractReducer); !ok {
		t.Fatal("go reducer not selected")
	}
	if _, ok := workflowContractReducerFor(workflowConfig{ContractSource: "combined", GoInspector: goConfig}, projection).(workflowCombinedContractReducer); !ok {
		t.Fatal("combined reducer not selected")
	}
}

// TestWorkflowContractSourceListValidationCoversClosedAdapters verifies duplicate, unknown and budget helper branches.
func TestWorkflowContractSourceListValidationCoversClosedAdapters(t *testing.T) {
	if !validContractSources([]string{"openapi", springinspector.SourceKind}) || validContractSources([]string{"openapi", "openapi"}) || validContractSources([]string{"unknown"}) || maxInt(1, 2) != 2 || maxInt(3, 2) != 3 {
		t.Fatal("contract source list validation is not closed")
	}
}

// TestWorkflowCombinedContractReducerDeniesConflictingIdentity verifies incompatible authoritative and derived facts stop before planning effects.
func TestWorkflowCombinedContractReducerDeniesConflictingIdentity(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "app"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "app", "main.go"), []byte("package app\nimport \"net/http\"\nfunc health(w http.ResponseWriter,r *http.Request){w.WriteHeader(200)}\nfunc routes(){http.HandleFunc(\"GET /health\",health)}"), 0o600); err != nil {
		t.Fatal(err)
	}
	api := filepath.Join(root, "api.yaml")
	if err := os.WriteFile(api, []byte("openapi: 3.1.0\ninfo: {title: test, version: \"1\"}\npaths:\n  /health:\n    get:\n      operationId: incompatible\n      responses:\n        \"200\": {description: ok}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	projection := contractcatalog.ProjectionPolicy{MaxStringBytes: 1024, AllowedURLSchemes: []string{"https"}, SensitiveNames: []string{}, SensitivePaths: []string{}, SensitiveHeaderNames: []string{}, ForbiddenValues: []string{}}
	limits := goinspector.Limits{MaxFiles: 10, MaxBytes: 1 << 20, MaxPackages: 5, MaxASTNodes: 1000, MaxDepth: 10, MaxTypes: 20, MaxOperations: 10, MaxDiagnostics: 10, MaxDuration: time.Second}
	reducer := workflowCombinedContractReducer{openAPIPath: api, root: root, authorizedRef: "both", repositoryID: "repo", revision: "0123456789abcdef0123456789abcdef01234567", config: workflowGoInspectorConfig{Roots: []string{"app"}, GOOS: "linux", GOARCH: "amd64", Limits: limits, EntrySymbols: []string{"routes"}}, selections: map[string][]workflowOperationSelection{"c1": {{Method: "GET", Path: "/health"}}}, projection: projection}
	if _, err := reducer.Reduce(context.Background(), "both", specification.Specification{Constraints: []specification.Constraint{{ID: "c1"}}}); err == nil {
		t.Fatal("conflicting sources reached context")
	}
}

// TestWorkflowReducersFailBeforeEffectsAcrossSourceStages covers loader, repository, input and adapter gates.
func TestWorkflowReducersFailBeforeEffectsAcrossSourceStages(t *testing.T) {
	projection := contractcatalog.ProjectionPolicy{MaxStringBytes: 128, AllowedURLSchemes: []string{"https"}, SensitiveNames: []string{}, SensitivePaths: []string{}, SensitiveHeaderNames: []string{}, ForbiddenValues: []string{}}
	limits := goinspector.Limits{MaxFiles: 10, MaxBytes: 1 << 20, MaxPackages: 5, MaxASTNodes: 1000, MaxDepth: 10, MaxTypes: 20, MaxOperations: 10, MaxDiagnostics: 10, MaxDuration: time.Second}
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "app"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "app", "main.go"), []byte("package app\nfunc routes(){}"), 0o600); err != nil {
		t.Fatal(err)
	}
	base := workflowCombinedContractReducer{openAPIPath: filepath.Join(root, "missing.yaml"), root: root, authorizedRef: "both", repositoryID: "repo", revision: "0123456789abcdef0123456789abcdef01234567", config: workflowGoInspectorConfig{Roots: []string{"app"}, GOOS: "linux", GOARCH: "amd64", Limits: limits, EntrySymbols: []string{"routes"}}, projection: projection}
	if _, err := base.Reduce(context.Background(), "both", specification.Specification{}); err == nil {
		t.Fatal("missing OpenAPI passed")
	}
	api := filepath.Join(root, "api.yaml")
	if err := os.WriteFile(api, []byte("openapi: 3.1.0\ninfo: {title: t, version: \"1\"}\npaths: {}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	base.openAPIPath = api
	base.root = filepath.Join(root, "missing")
	if _, err := base.Reduce(context.Background(), "both", specification.Specification{}); err == nil {
		t.Fatal("missing repository passed")
	}
	base.root = root
	base.projection = contractcatalog.ProjectionPolicy{}
	if _, err := base.Reduce(context.Background(), "both", specification.Specification{}); err == nil {
		t.Fatal("invalid projection passed")
	}
	goReducer := workflowGoContractReducer{root: root, repositoryID: "repo", revision: "0123456789abcdef0123456789abcdef01234567", config: workflowGoInspectorConfig{Roots: []string{"app"}, GOOS: "linux", GOARCH: "amd64", Limits: limits, EntrySymbols: []string{"missing"}}, projection: projection}
	if _, err := goReducer.Reduce(context.Background(), root, specification.Specification{}); err == nil {
		t.Fatal("insufficient source passed adapter")
	}
	goReducer.root = filepath.Join(root, "missing")
	if _, err := goReducer.Reduce(context.Background(), goReducer.root, specification.Specification{}); err == nil {
		t.Fatal("missing Go repository passed")
	}
}

// TestWorkflowBindingAuthorityAndCatalogBranches verifies local/GitHub authority, drift and runtime-profile failures without invoking model or tools.
func TestWorkflowBindingAuthorityAndCatalogBranches(t *testing.T) {
	revision := validationworkflow.RevisionContext{RepositoryID: "repo", CommitOID: "0123456789abcdef0123456789abcdef01234567", Roots: []string{"app"}}
	binding := validationworkflow.BindingContext{Revision: revision}
	config := workflowConfig{RepositoryID: revision.RepositoryID, CommitOID: revision.CommitOID}
	command := validationworkflow.StartCommand{RepositoryID: revision.RepositoryID, Revision: revision.CommitOID}
	verifier := workflowBindingVerifier{config: config, binding: binding}
	if authority, err := verifier.ContractAuthority(context.Background(), command); err != nil || len(authority.Roots) != 1 {
		t.Fatalf("local authority rejected: %+v %v", authority, err)
	}
	if _, err := verifier.ContractAuthority(context.Background(), validationworkflow.StartCommand{RepositoryID: "other", Revision: revision.CommitOID}); err == nil {
		t.Fatal("local authority drift accepted")
	}
	github := validationworkflow.RevisionContext{RepositoryID: "repo", CommitOID: revision.CommitOID, ChangedPaths: []string{"app/main.go"}}
	if authority, err := contractAuthorityFor(config, binding, command, github, true); err != nil || len(authority.Roots) != 1 || len(authority.ChangedPaths) != 1 {
		t.Fatalf("GitHub authority rejected: %+v %v", authority, err)
	}
	github.CommitOID = "ffffffffffffffffffffffffffffffffffffffff"
	if _, err := contractAuthorityFor(config, binding, command, github, true); err == nil {
		t.Fatal("GitHub head drift accepted")
	}
	if verified, err := verifier.Verify(context.Background(), command); err != nil || verified.Revision.CommitOID != revision.CommitOID {
		t.Fatalf("binding verification failed: %+v %v", verified, err)
	}
	if _, err := verifier.Verify(context.Background(), validationworkflow.StartCommand{RepositoryID: "repo", Revision: "wrong"}); err == nil {
		t.Fatal("binding drift accepted")
	}
	catalog := workflowCatalog{profile: "runtime", runtime: validationworkflow.RuntimeContext{Profile: "runtime"}, descriptors: []agentplanning.CapabilityDescriptor{{Name: "http"}}}
	values, runtimeContext, err := catalog.Descriptors(context.Background(), "runtime", validationworkflow.TargetSelection{})
	if err != nil || len(values) != 1 || runtimeContext.Profile != "runtime" {
		t.Fatalf("catalog rejected: %+v %+v %v", values, runtimeContext, err)
	}
	values[0].Name = "changed"
	if catalog.descriptors[0].Name == "changed" {
		t.Fatal("catalog leaked mutable descriptor slice")
	}
	if _, _, err := catalog.Descriptors(context.Background(), "other", validationworkflow.TargetSelection{}); err == nil {
		t.Fatal("unknown runtime profile accepted")
	}
}
