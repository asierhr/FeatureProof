package springinspector

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/asierhr/featureProof/internal/contractcatalog"
)

// testRevision is an immutable fixture revision accepted by the authority boundary.
const testRevision = "0123456789abcdef0123456789abcdef01234567"

// testLimits returns explicit finite budgets for every inspection dimension.
func testLimits() Limits {
	return Limits{MaxSourceSets: 10, MaxRoots: 10, MaxFiles: 20, MaxBytes: 1 << 20, MaxUnits: 20, MaxTokens: 10000, MaxDepth: 12, MaxTypes: 100, MaxSymbols: 1000, MaxSchemaNodes: 1000, MaxOperations: 100, MaxCrossProduct: 100, MaxDiagnostics: 30, MaxStringBytes: 512, MaxDuration: time.Minute}
}

// testProjection supplies an explicit redaction policy without hidden defaults.
func testProjection() contractcatalog.ProjectionPolicy {
	return contractcatalog.ProjectionPolicy{MaxStringBytes: 512, AllowedURLSchemes: []string{"https"}, SensitiveNames: []string{}, SensitivePaths: []string{}, SensitiveHeaderNames: []string{"authorization"}, ForbiddenValues: []string{"FORBIDDEN_CANARY"}}
}

// testConfig returns trusted Spring inspection authority for one explicit source set.
func testConfig() Configuration {
	return Configuration{RepositoryID: "repo", Revision: testRevision, RootIdentity: "checkout-main", FrameworkIdentities: SupportedFrameworkIdentities(), SourceSets: []SourceSet{{ID: "main", Root: "src/main/java"}}, JavaRelease: 17, Generated: GeneratedExclude, Limits: testLimits(), Confidence: contractcatalog.ConfidenceMedium, Completeness: contractcatalog.CompletenessPartial, Projection: testProjection()}
}

// inspectFixture seals and inspects in-memory files through the same product boundary.
func inspectFixture(t *testing.T, files map[string][]byte) (Result, Input) {
	t.Helper()
	snapshot, err := NewSnapshot("repo", testRevision, files)
	if err != nil {
		t.Fatal(err)
	}
	input, err := NewInput(snapshot, testConfig())
	if err != nil {
		t.Fatal(err)
	}
	result, err := (Inspector{}).Inspect(context.Background(), input, "spring")
	if err != nil {
		t.Fatal(err)
	}
	return result, input
}

// TestSpringInspectionRejectsUnsealedRevisionSourceSetsAndEscapingPaths covers authority rejection before analysis.
func TestSpringInspectionRejectsUnsealedRevisionSourceSetsAndEscapingPaths(t *testing.T) {
	config := testConfig()
	cases := []func(*Configuration){func(c *Configuration) { c.Revision = "main" }, func(c *Configuration) { c.SourceSets[0].Root = "../x" }, func(c *Configuration) { c.JavaRelease = 0 }, func(c *Configuration) { c.ChangedPaths = []string{"other/X.java"} }}
	for _, mutate := range cases {
		candidate := config
		candidate.SourceSets = append([]SourceSet(nil), config.SourceSets...)
		mutate(&candidate)
		if _, err := newInput(Snapshot{}, candidate, time.Time{}); err == nil {
			t.Fatal("invalid authority accepted")
		}
	}
}

// TestMappingsComposeAliasesArraysConstantsAndPrefixes verifies closed Spring mappings and neutral projection.
func TestMappingsComposeAliasesArraysConstantsAndPrefixes(t *testing.T) {
	source := `package demo;
import org.springframework.web.bind.annotation.RestController;
import org.springframework.web.bind.annotation.RequestMapping;
import org.springframework.web.bind.annotation.GetMapping;
import org.springframework.web.bind.annotation.PathVariable;
import org.springframework.web.bind.annotation.RequestParam;
import org.springframework.web.bind.annotation.ResponseStatus;
import org.springframework.http.HttpStatus;
@RestController @RequestMapping(path={"/api","/v1"}, produces={"application/json"})
class Users {
 @GetMapping(value={"/users/{id}"}) @ResponseStatus(HttpStatus.OK)
 public User find(@PathVariable("id") String id, @RequestParam(name="verbose", required=false) Boolean verbose) { return null; }
}
record User(String name, int age) {}`
	result, _ := inspectFixture(t, map[string][]byte{"src/main/java/demo/Users.java": []byte(source)})
	if len(result.Snapshot.Operations) != 2 {
		t.Fatalf("operations=%d diagnostics=%+v", len(result.Snapshot.Operations), result.Diagnostics)
	}
	op := result.Snapshot.Operations[0]
	if op.Key.Method != "GET" || !strings.Contains(op.Key.Path, "users/{id}") || len(op.Parameters) != 2 || op.Responses["200"].Content["application/json"].Schema == nil {
		t.Fatalf("unexpected operation: %+v", op)
	}
}

// TestDynamicAndLookalikeAnnotationsDoNotCreateOperations proves that source names cannot elevate framework identity.
func TestDynamicAndLookalikeAnnotationsDoNotCreateOperations(t *testing.T) {
	files := map[string][]byte{"src/main/java/demo/X.java": []byte(`package demo; @interface RestController{} @interface GetMapping{String value();} @RestController class X {@GetMapping(value=dynamic()) String x(){return "";}}`)}
	snapshot, _ := NewSnapshot("repo", testRevision, files)
	input, _ := NewInput(snapshot, testConfig())
	_, err := (Inspector{}).Inspect(context.Background(), input, "spring")
	var typed *Error
	if !errors.As(err, &typed) || typed.Category != CategoryInsufficientContext {
		t.Fatalf("expected insufficient, got %v", err)
	}
}

// TestLocalDTORecordsEnumsNestedAndGenericsProjectBoundedly verifies local schema closure and enum structure.
func TestLocalDTORecordsEnumsNestedAndGenericsProjectBoundedly(t *testing.T) {
	source := `package demo;
import org.springframework.web.bind.annotation.*;
import org.springframework.http.ResponseEntity;
import java.util.List;
@RestController class C { @PostMapping(path="/items", consumes="application/json", produces="application/json") public ResponseEntity<List<Item>> add(@RequestBody Item body){ return null; } }
record Item(String name, Kind kind) {} enum Kind { A, B }`
	result, _ := inspectFixture(t, map[string][]byte{"src/main/java/demo/C.java": []byte(source)})
	op := result.Snapshot.Operations[0]
	if op.RequestBody == nil || op.RequestBody.Content["application/json"].Schema.Properties["kind"].Enum[0] != "A" {
		t.Fatalf("schema not projected: %+v", op.RequestBody)
	}
}

// TestSecurityAnnotationsRemainStructuredNonExecutableData verifies security metadata never creates an executable requirement.
func TestSecurityAnnotationsRemainStructuredNonExecutableData(t *testing.T) {
	source := `package demo;
import org.springframework.web.bind.annotation.RestController;
import org.springframework.web.bind.annotation.GetMapping;
import org.springframework.security.access.prepost.PreAuthorize;
import jakarta.annotation.security.RolesAllowed;
@RestController @RolesAllowed({"ADMIN","OPS"}) class C { @GetMapping("/secure") @PreAuthorize("hasRole('ADMIN')") public String get(){return "";} }`
	result, _ := inspectFixture(t, map[string][]byte{"src/main/java/demo/C.java": []byte(source)})
	op := result.Snapshot.Operations[0]
	if len(op.Security) != 0 || len(op.SecuritySchemes) != 0 || len(op.DeclarativeConstraints) != 2 {
		t.Fatalf("declarative security elevated or lost: %+v", op)
	}
	raw, _ := json.Marshal(op)
	if strings.Contains(string(raw), "credential") {
		t.Fatal("credential authority leaked")
	}
}

// TestChangedDTOMarksDependentOperations verifies changed-file dependency closure while retaining the full catalog.
func TestChangedDTOMarksDependentOperations(t *testing.T) {
	files := map[string][]byte{"src/main/java/demo/C.java": []byte(`package demo; import org.springframework.web.bind.annotation.*; @RestController class C {@GetMapping(path="/x",produces="application/json") Item x(){return null;}}`), "src/main/java/demo/Item.java": []byte(`package demo; record Item(String value){}`)}
	snapshot, _ := NewSnapshot("repo", testRevision, files)
	config := testConfig()
	config.ChangedPaths = []string{"src/main/java/demo/Item.java"}
	input, _ := NewInput(snapshot, config)
	result, err := (Inspector{}).Inspect(context.Background(), input, "spring")
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Snapshot.Operations) != 1 || len(result.AffectedOperations) != 1 {
		t.Fatalf("catalog=%d affected=%d", len(result.Snapshot.Operations), len(result.AffectedOperations))
	}
}

// TestSourceSetIsolationPreventsCrossSetDTOResolution verifies identical type names cannot satisfy another authorized source set.
func TestSourceSetIsolationPreventsCrossSetDTOResolution(t *testing.T) {
	files := map[string][]byte{"a/demo/C.java": []byte(`package demo; import org.springframework.web.bind.annotation.*; @RestController class C {@GetMapping(path="/x",produces="application/json") Shared x(){return null;}}`), "b/demo/Shared.java": []byte(`package demo; record Shared(String secret){}`)}
	snapshot, _ := NewSnapshot("repo", testRevision, files)
	config := testConfig()
	config.SourceSets = []SourceSet{{ID: "a", Root: "a"}, {ID: "b", Root: "b"}}
	input, err := NewInput(snapshot, config)
	if err != nil {
		t.Fatal(err)
	}
	result, err := (Inspector{}).Inspect(context.Background(), input, "spring")
	if err != nil {
		t.Fatal(err)
	}
	response := result.Snapshot.Operations[0].Responses["200"]
	if len(response.Content) != 0 {
		t.Fatal("DTO crossed source-set boundary")
	}
}

// TestExplicitJavaImportResolvesExactDTOIdentity verifies same simple names cannot collide across packages.
func TestExplicitJavaImportResolvesExactDTOIdentity(t *testing.T) {
	files := map[string][]byte{"src/main/java/api/C.java": []byte(`package api; import org.springframework.web.bind.annotation.*; import good.Shared; @RestController class C {@GetMapping(path="/x",produces="application/json") Shared x(){return null;}}`), "src/main/java/good/Shared.java": []byte(`package good; record Shared(String allowed){}`), "src/main/java/bad/Shared.java": []byte(`package bad; record Shared(String forbidden){}`)}
	result, _ := inspectFixture(t, files)
	schema := result.Snapshot.Operations[0].Responses["200"].Content["application/json"].Schema
	if schema.Properties["allowed"] == nil || schema.Properties["forbidden"] != nil {
		t.Fatalf("wrong DTO identity: %+v", schema)
	}
}

// TestNestedDTOResolvesWithinControllerIdentity verifies direct nested source types use their enclosing symbol identity.
func TestNestedDTOResolvesWithinControllerIdentity(t *testing.T) {
	source := []byte(`package demo; import org.springframework.web.bind.annotation.*; @RestController class C {@GetMapping(path="/x",produces="application/json") Reply x(){return null;} record Reply(String value){} }`)
	result, _ := inspectFixture(t, map[string][]byte{"src/main/java/demo/C.java": source})
	schema := result.Snapshot.Operations[0].Responses["200"].Content["application/json"].Schema
	if schema == nil || schema.Properties["value"] == nil {
		t.Fatalf("nested schema=%+v", schema)
	}
}

// TestEverySpringClaimUsesItsExactDeclarationProvenance verifies parameter, schema, status and security facts do not inherit only route provenance.
func TestEverySpringClaimUsesItsExactDeclarationProvenance(t *testing.T) {
	source := []byte(`package demo; import org.springframework.web.bind.annotation.*; import org.springframework.security.access.prepost.PreAuthorize; import org.springframework.http.HttpStatus; @RestController class C {@GetMapping(path="/x/{id}",produces="application/json") @ResponseStatus(HttpStatus.CREATED) @PreAuthorize("ok") Reply x(@PathVariable("id") String id){return null;}} record Reply(String value){}`)
	result, _ := inspectFixture(t, map[string][]byte{"src/main/java/demo/C.java": source})
	claims := result.Snapshot.Operations[0].Claims
	found := map[string]bool{}
	for _, claim := range claims {
		location := claim.Provenance[0].SafeLocation
		if strings.HasPrefix(claim.Field, "parameters/") && strings.Contains(location, "PathVariable") {
			found["parameter"] = true
		}
		if strings.Contains(claim.Field, "properties/value") && strings.Contains(location, "java.field") {
			found["schema"] = true
		}
		if strings.HasPrefix(claim.Field, "responses/") && strings.Contains(location, "ResponseStatus") {
			found["response"] = true
		}
		if strings.HasPrefix(claim.Field, "declarative_constraints/") && strings.Contains(location, "PreAuthorize") {
			found["security"] = true
		}
	}
	for _, kind := range []string{"parameter", "schema", "response", "security"} {
		if !found[kind] {
			t.Fatalf("missing exact %s provenance: %+v", kind, claims)
		}
	}
}

// passiveRepository is a capability spy proving validation occurs before repository observation.
type passiveRepository struct {
	mu      sync.Mutex
	reads   int
	files   map[string][]byte
	request SnapshotRequest
}

// ReadSnapshot records the bounded request and returns isolated fixture bytes without other capabilities.
func (p *passiveRepository) ReadSnapshot(ctx context.Context, request SnapshotRequest) (map[string][]byte, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.reads++
	p.request = request
	out := map[string][]byte{}
	for k, v := range p.files {
		out[k] = append([]byte(nil), v...)
	}
	return out, ctx.Err()
}

// TestGeneratedSourceSetsAreNotReadUnlessExplicitlyIncluded verifies exclusion occurs before the repository capability.
func TestGeneratedSourceSetsAreNotReadUnlessExplicitlyIncluded(t *testing.T) {
	repo := &passiveRepository{files: map[string][]byte{"src/main/java/C.java": []byte(`class C{}`)}}
	config := testConfig()
	config.SourceSets = append(config.SourceSets, SourceSet{ID: "generated", Root: "build/generated", Generated: true})
	if _, err := FreezeConfiguredRepositoryWithClock(context.Background(), repo, "root", config, fixedClock{time.Now()}); err != nil {
		t.Fatal(err)
	}
	if len(repo.request.SourceSets) != 1 || repo.request.SourceSets[0].Generated {
		t.Fatalf("generated set reached repository: %+v", repo.request.SourceSets)
	}
}

// fixedClock makes deadline sealing deterministic without sleeps.
type fixedClock struct{ now time.Time }

// Now returns the fixture instant used by the shared deadline.
func (c fixedClock) Now() time.Time { return c.now }

// TestSpringInspectorLoadsAsDerivedContractSource verifies exact registry metadata and immutable adapter output.
func TestSpringInspectorLoadsAsDerivedContractSource(t *testing.T) {
	source := []byte(`package demo; import org.springframework.web.bind.annotation.RestController; import org.springframework.web.bind.annotation.GetMapping; @RestController class C {@GetMapping("/x") String x(){return "";}}`)
	repo := &passiveRepository{files: map[string][]byte{"src/main/java/demo/C.java": source}}
	input, err := FreezeConfiguredRepositoryWithClock(context.Background(), repo, "sealed", testConfig(), fixedClock{time.Now()})
	if err != nil {
		t.Fatal(err)
	}
	limits := contractcatalog.Limits{MaxSources: 1, MaxOperations: 10, MaxClaims: 100, MaxBytes: 1 << 20, MaxDepth: 30, MaxDiagnostics: 30, MaxLocationBytes: 1024}
	request := SourceRequest("spring", input, limits, testProjection())
	registry, err := contractcatalog.NewRegistry(map[string]contractcatalog.ContractSource{SourceKind: DerivedSource{}})
	if err != nil {
		t.Fatal(err)
	}
	loaded, err := registry.Load(context.Background(), []contractcatalog.SourceRequest{request}, limits)
	if err != nil {
		t.Fatal(err)
	}
	catalog, err := contractcatalog.Build(context.Background(), loaded, limits)
	if err != nil {
		t.Fatal(err)
	}
	if len(catalog.Operations()) != 1 || repo.reads != 1 {
		t.Fatal("source not loaded exactly once")
	}
}

// TestSpringErrorsDiagnosticsEventsAndCatalogExcludeCanaries verifies forbidden source text never crosses public surfaces.
func TestSpringErrorsDiagnosticsEventsAndCatalogExcludeCanaries(t *testing.T) {
	source := []byte(`package demo; // FORBIDDEN_CANARY
import org.springframework.web.bind.annotation.RestController; import org.springframework.web.bind.annotation.GetMapping; import org.springframework.security.access.prepost.PreAuthorize; @RestController class C {@GetMapping("/ok") @PreAuthorize("FORBIDDEN_CANARY") String x(){return "FORBIDDEN_CANARY";}}`)
	result, _ := inspectFixture(t, map[string][]byte{"src/main/java/demo/C.java": source})
	if len(result.Snapshot.Operations[0].DeclarativeConstraints) != 0 || result.OmittedDiagnostics == 0 {
		t.Fatal("forbidden security unit was not omitted atomically")
	}
	raw, _ := json.Marshal(result)
	if strings.Contains(string(raw), "FORBIDDEN_CANARY") {
		t.Fatal("source canary leaked")
	}
}

// TestInspectionCancellationAcrossPhases verifies cancellation is observed before every expensive phase.
func TestInspectionCancellationAcrossPhases(t *testing.T) {
	snapshot, _ := NewSnapshot("repo", testRevision, map[string][]byte{"src/main/java/demo/C.java": []byte(`package demo;`)})
	input, _ := NewInput(snapshot, testConfig())
	for _, phase := range []string{"parse", "index", "mappings"} {
		ctx, cancel := context.WithCancel(context.Background())
		inspector := Inspector{phaseBarrier: func(_ context.Context, current string) {
			if current == phase {
				cancel()
			}
		}}
		_, err := inspector.Inspect(ctx, input, "spring")
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("phase %s: %v", phase, err)
		}
	}
}

// TestSpringSourcesMergeCompatiblyAndRetainConflicts verifies the neutral registry combines independent derived snapshots without selecting a winner.
func TestSpringSourcesMergeCompatiblyAndRetainConflicts(t *testing.T) {
	makeInput := func(source string) Input {
		snapshot, err := NewSnapshot("repo", testRevision, map[string][]byte{"src/main/java/demo/C.java": []byte(source)})
		if err != nil {
			t.Fatal(err)
		}
		input, err := NewInput(snapshot, testConfig())
		if err != nil {
			t.Fatal(err)
		}
		return input
	}
	base := `package demo; import org.springframework.web.bind.annotation.*; @RestController class C {@GetMapping("/x") String x(){return "";}}`
	same := base + "\n"
	conflict := `package demo; import org.springframework.web.bind.annotation.*; @RestController class C {@GetMapping(path="/x",produces="text/plain") String x(){return "";}}`
	limits := contractcatalog.Limits{MaxSources: 3, MaxOperations: 20, MaxClaims: 500, MaxBytes: 2 << 20, MaxDepth: 40, MaxDiagnostics: 50, MaxLocationBytes: 1024}
	registry, _ := contractcatalog.NewRegistry(map[string]contractcatalog.ContractSource{SourceKind: DerivedSource{}})
	build := func(second string) contractcatalog.OperationCatalog {
		one, two := makeInput(base), makeInput(second)
		per := limits
		per.MaxSources = 1
		per.MaxOperations /= 2
		per.MaxClaims /= 2
		per.MaxBytes /= 2
		per.MaxDepth /= 2
		per.MaxDiagnostics /= 2
		loaded, err := registry.Load(context.Background(), []contractcatalog.SourceRequest{SourceRequest("one", one, per, testProjection()), SourceRequest("two", two, per, testProjection())}, limits)
		if err != nil {
			t.Fatal(err)
		}
		catalog, err := contractcatalog.Build(context.Background(), loaded, limits)
		if err != nil {
			t.Fatal(err)
		}
		return catalog
	}
	if got := build(same); len(got.Conflicts()) != 0 {
		t.Fatalf("compatible conflict=%+v", got.Conflicts())
	}
	if got := build(conflict); len(got.Conflicts()) == 0 {
		t.Fatal("material response conflict was hidden")
	}
}
