package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/asierhr/featureProof/internal/contractcatalog"
	"github.com/asierhr/featureProof/internal/specification"
	"github.com/asierhr/featureProof/internal/springinspector"
	"github.com/asierhr/featureProof/internal/validationworkflow"
)

// TestWorkflowSpringReducerUsesExplicitRevisionSourceSetsAndSelections verifies the product composition reaches Spring without host defaults.
func TestWorkflowSpringReducerUsesExplicitRevisionSourceSetsAndSelections(t *testing.T) {
	root := t.TempDir()
	sourceRoot := filepath.Join(root, "src", "main", "java", "demo")
	if err := os.MkdirAll(sourceRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	source := `package demo; import org.springframework.web.bind.annotation.RestController; import org.springframework.web.bind.annotation.GetMapping; @RestController class C {@GetMapping("/health") String health(){return "ok";}}`
	if err := os.WriteFile(filepath.Join(sourceRoot, "C.java"), []byte(source), 0o600); err != nil {
		t.Fatal(err)
	}
	projection := contractcatalog.ProjectionPolicy{MaxStringBytes: 512, AllowedURLSchemes: []string{"https"}, SensitiveNames: []string{}, SensitivePaths: []string{}, SensitiveHeaderNames: []string{}, ForbiddenValues: []string{}}
	limits := springinspector.Limits{MaxSourceSets: 4, MaxRoots: 4, MaxFiles: 10, MaxBytes: 1 << 20, MaxUnits: 10, MaxTokens: 1000, MaxDepth: 10, MaxTypes: 20, MaxSymbols: 100, MaxSchemaNodes: 100, MaxOperations: 10, MaxCrossProduct: 20, MaxDiagnostics: 10, MaxStringBytes: 512, MaxDuration: time.Second}
	reducer := workflowSpringContractReducer{root: root, authorizedRef: "spring-contract", repositoryID: "repo", revision: "0123456789abcdef0123456789abcdef01234567", config: workflowSpringInspectorConfig{RootIdentity: "checkout-main", FrameworkIdentities: springinspector.SupportedFrameworkIdentities(), SourceSets: []springinspector.SourceSet{{ID: "main", Root: "src/main/java"}}, JavaRelease: 17, Generated: springinspector.GeneratedExclude, Limits: limits}, selections: map[string][]workflowOperationSelection{"constraint": {{Method: "GET", Path: "/health"}}}, projection: projection}
	spec := specification.Specification{Constraints: []specification.Constraint{{ID: "constraint"}}}
	reduced, err := reducer.Reduce(context.Background(), "spring-contract", spec)
	if err != nil {
		t.Fatal(err)
	}
	if reduced.CatalogDigest() == "" || len(reduced.RetainedOperations()) != 1 {
		t.Fatalf("invalid reduced context: %+v", reduced)
	}
	if _, err := reducer.Reduce(context.Background(), "other", spec); err == nil {
		t.Fatal("unauthorized ref accepted")
	}
}

// TestWorkflowContractReducerSelectsSpringOnlyWhenExplicit verifies the shared API/CLI/MCP/GitHub composition switch is exact.
func TestWorkflowContractReducerSelectsSpringOnlyWhenExplicit(t *testing.T) {
	config := workflowConfig{ContractSource: springinspector.SourceKind, ContractPath: "contract", RepositoryRoot: "repo", RepositoryID: "id", CommitOID: "0123456789abcdef0123456789abcdef01234567", SpringInspector: &workflowSpringInspectorConfig{}}
	projection := contractcatalog.ProjectionPolicy{}
	if _, ok := workflowContractReducerFor(config, projection).(workflowSpringContractReducer); !ok {
		t.Fatal("spring reducer not selected")
	}
}

// TestWorkflowSpringReducerRejectsRevisionAndRootDriftBeforeInspection verifies PR authority cannot widen the sealed source set.
func TestWorkflowSpringReducerRejectsRevisionAndRootDriftBeforeInspection(t *testing.T) {
	reducer := workflowSpringContractReducer{root: "missing", authorizedRef: "contract", repositoryID: "repo", revision: "0123456789abcdef0123456789abcdef01234567", config: workflowSpringInspectorConfig{RootIdentity: "checkout-main", FrameworkIdentities: springinspector.SupportedFrameworkIdentities(), SourceSets: []springinspector.SourceSet{{ID: "main", Root: "src/main/java"}}, JavaRelease: 17, Generated: springinspector.GeneratedExclude, Limits: springinspector.Limits{MaxDuration: time.Second}}}
	spec := specification.Specification{}
	drift := validationworkflow.ContextWithContractAuthority(context.Background(), validationworkflow.RevisionContext{RepositoryID: "other", CommitOID: reducer.revision, Roots: []string{"src/main/java"}})
	if _, err := reducer.Reduce(drift, "contract", spec); err == nil {
		t.Fatal("revision authority drift accepted")
	}
	widen := validationworkflow.ContextWithContractAuthority(context.Background(), validationworkflow.RevisionContext{RepositoryID: "repo", CommitOID: reducer.revision, Roots: []string{"outside"}})
	if _, err := reducer.Reduce(widen, "contract", spec); err == nil {
		t.Fatal("source root widening accepted")
	}
	if _, err := reducer.Reduce(context.Background(), "contract", spec); err == nil {
		t.Fatal("invalid sealed inspector configuration accepted")
	}
	invalidChanged := validationworkflow.ContextWithContractAuthority(context.Background(), validationworkflow.RevisionContext{RepositoryID: "repo", CommitOID: reducer.revision, Roots: []string{"src/main/java"}, ChangedPaths: []string{"outside/X.java"}})
	if _, err := reducer.Reduce(invalidChanged, "contract", spec); err == nil {
		t.Fatal("changed path escape accepted")
	}
}
