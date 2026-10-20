package springinspector

import (
	"context"
	"errors"
	"go/parser"
	gotoken "go/token"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/asierhr/featureProof/internal/contractcatalog"
)

// TestSpringInspectionNeverImportsProcessNetworkEnvironmentOrHostFilesystem enforces the parser capability boundary statically.
func TestSpringInspectionNeverImportsProcessNetworkEnvironmentOrHostFilesystem(t *testing.T) {
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	forbidden := map[string]bool{"os/exec": true, "net/http": true, "net": true, "os": true, "path/filepath": true}
	for _, entry := range entries {
		if filepath.Ext(entry.Name()) != ".go" || strings.HasSuffix(entry.Name(), "_test.go") {
			continue
		}
		file, err := parser.ParseFile(gotoken.NewFileSet(), entry.Name(), nil, parser.ImportsOnly)
		if err != nil {
			t.Fatal(err)
		}
		for _, spec := range file.Imports {
			name, _ := strconv.Unquote(spec.Path.Value)
			if forbidden[name] {
				t.Fatalf("forbidden capability import %s in %s", name, entry.Name())
			}
		}
	}
}

// eventRecorder captures safe inspector lifecycle events for observability tests.
type eventRecorder struct{ events []Event }

// Record appends one already-redacted event without influencing inspection.
func (r *eventRecorder) Record(event Event) { r.events = append(r.events, event) }

// TestInspectionEventsContainOnlyBoundedMetadata verifies start/completion and failure use the closed event shape.
func TestInspectionEventsContainOnlyBoundedMetadata(t *testing.T) {
	snapshot, _ := NewSnapshot("repo", testRevision, map[string][]byte{"src/main/java/demo/C.java": []byte(`package demo; import org.springframework.web.bind.annotation.*; @RestController class C {@GetMapping("/x") String x(){return "CANARY";}}`)})
	input, _ := NewInput(snapshot, testConfig())
	recorder := &eventRecorder{}
	if _, err := (Inspector{Observer: recorder}).Inspect(context.Background(), input, "spring"); err != nil {
		t.Fatal(err)
	}
	if len(recorder.events) != 2 || recorder.events[0].Type != "spring_inspection_started" || recorder.events[1].Type != "spring_inspection_completed" || recorder.events[1].Operations != 1 {
		t.Fatalf("events=%+v", recorder.events)
	}
	failed := &eventRecorder{}
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	_, _ = (Inspector{Observer: failed}).Inspect(cancelled, input, "spring")
	if len(failed.events) != 2 || failed.events[1].Outcome != "failed" {
		t.Fatalf("failed events=%+v", failed.events)
	}
}

// TestRequestMappingMethodsResponseStatusAndControllerResponseBody covers explicit generic mapping, status and view-controller safeguards.
func TestRequestMappingMethodsResponseStatusAndControllerResponseBody(t *testing.T) {
	source := `package demo;
import org.springframework.stereotype.Controller;
import org.springframework.web.bind.annotation.RequestMapping;
import org.springframework.web.bind.annotation.ResponseBody;
import org.springframework.web.bind.annotation.ResponseStatus;
import org.springframework.http.HttpStatus;
@Controller @RequestMapping(path="/api", method={RequestMethod.GET,RequestMethod.POST}) class C {
 @ResponseBody @RequestMapping(path="/x", method=RequestMethod.POST, produces="application/json") @ResponseStatus(code=HttpStatus.CREATED)
 public Optional<String> x(){ return null; }
}`
	result, _ := inspectFixture(t, map[string][]byte{"src/main/java/demo/C.java": []byte(source)})
	if len(result.Snapshot.Operations) != 1 {
		t.Fatalf("operations=%d", len(result.Snapshot.Operations))
	}
	op := result.Snapshot.Operations[0]
	if op.Key.Method != "POST" || op.Key.Path != "/api/x" {
		t.Fatalf("unexpected key %+v", op.Key)
	}
	if _, ok := op.Responses["201"]; !ok {
		t.Fatalf("status=%+v", op.Responses)
	}
}

// TestSchemaContainersFieldsCyclesAndUnsupportedSerialization verifies bounded local schema behavior and safe degradation.
func TestSchemaContainersFieldsCyclesAndUnsupportedSerialization(t *testing.T) {
	state := analysisState{input: Input{config: testConfig()}, types: map[string]javaType{"main\x00demo.Box": {name: "Box", packageName: "demo", file: "Box.java", sourceSet: "main", imports: map[string]string{"Map": "java.util.Map"}, fields: []javaField{{name: "values", typeName: "Map<String,Integer>"}}}, "main\x00demo.Loop": {name: "Loop", packageName: "demo", file: "Loop.java", sourceSet: "main", imports: map[string]string{}, fields: []javaField{{name: "next", typeName: "Loop"}}}, "main\x00demo.Custom": {name: "Custom", packageName: "demo", file: "Custom.java", sourceSet: "main", imports: map[string]string{}, customSerialization: true}}}
	scope := typeScope{sourceSet: "main", packageName: "demo", imports: map[string]string{"Map": "java.util.Map", "Optional": "java.util.Optional"}}
	if schema, _, ok := state.schemaFor("Box[]", scope, 0, map[string]bool{}); !ok || schema.Items.Properties["values"].Type != "object" {
		t.Fatalf("container schema %+v", schema)
	}
	if _, _, ok := state.schemaFor("Loop", scope, 0, map[string]bool{}); ok {
		t.Fatal("cycle accepted")
	}
	if _, _, ok := state.schemaFor("Custom", scope, 0, map[string]bool{}); ok {
		t.Fatal("custom serializer accepted")
	}
	if schema, _, ok := state.schemaFor("Optional<String>", scope, 0, map[string]bool{}); !ok || !schema.Nullable {
		t.Fatal("optional not projected")
	}
	if _, _, ok := state.schemaFor("Map<Integer,String>", scope, 0, map[string]bool{}); ok {
		t.Fatal("non-string map key accepted")
	}
}

// TestParserSyntaxFieldsConstantsAndLimits exercises passive lexical errors, constants and declaration parsing.
func TestParserSyntaxFieldsConstantsAndLimits(t *testing.T) {
	ctx := context.Background()
	if _, err := lexJava(ctx, []byte(`class X { String x="unterminated; }`), 100); err == nil {
		t.Fatal("unterminated literal accepted")
	}
	if _, err := lexJava(ctx, []byte(`class X { int x; }`), 1); err == nil {
		t.Fatal("token limit accepted")
	}
	unit, err := parseJavaUnit(ctx, "X.java", []byte(`package demo; class X { private final String name; public static int count=2; void helper(){} }`), 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(unit.types) != 1 || len(unit.types[0].fields) != 1 {
		t.Fatalf("fields=%+v", unit.types[0].fields)
	}
	if _, ok := literalValues([]token{{text: "UNKNOWN"}}, map[string][]string{}); ok {
		t.Fatal("unknown constant accepted")
	}
	if identifier("1bad") {
		t.Fatal("bad identifier accepted")
	}
}

// TestMappingsResolveLocalConstantsAndArrays verifies exact local constants without evaluating calls or external classpaths.
func TestMappingsResolveLocalConstantsAndArrays(t *testing.T) {
	source := `package demo; import org.springframework.web.bind.annotation.RestController; import org.springframework.web.bind.annotation.GetMapping; @RestController class C { private static final String PATH="/constant"; @GetMapping(path=PATH) String x(){return "";} }`
	result, _ := inspectFixture(t, map[string][]byte{"src/main/java/demo/C.java": []byte(source)})
	if len(result.Snapshot.Operations) != 1 || result.Snapshot.Operations[0].Key.Path != "/constant" {
		t.Fatalf("constant mapping=%+v", result.Snapshot.Operations)
	}
}

// TestMappingsResolveOnlyExplicitStaticImportedConstants verifies cross-file constants use exact package/class identity.
func TestMappingsResolveOnlyExplicitStaticImportedConstants(t *testing.T) {
	files := map[string][]byte{"src/main/java/demo/C.java": []byte(`package demo; import org.springframework.web.bind.annotation.RestController; import org.springframework.web.bind.annotation.GetMapping; import static good.Paths.ROUTE; @RestController class C {@GetMapping(ROUTE) String x(){return "";}}`), "src/main/java/good/Paths.java": []byte(`package good; class Paths { public static final String ROUTE="/good"; }`), "src/main/java/bad/Paths.java": []byte(`package bad; class Paths { public static final String ROUTE="/bad"; }`)}
	result, _ := inspectFixture(t, files)
	if result.Snapshot.Operations[0].Key.Path != "/good" {
		t.Fatalf("constant identity=%+v", result.Snapshot.Operations)
	}
	snapshot, _ := NewSnapshot("repo", testRevision, files)
	config := testConfig()
	config.ChangedPaths = []string{"src/main/java/good/Paths.java"}
	input, _ := NewInput(snapshot, config)
	changed, err := (Inspector{}).Inspect(context.Background(), input, "spring")
	if err != nil {
		t.Fatal(err)
	}
	if len(changed.AffectedOperations) != 1 {
		t.Fatal("changed constant did not mark dependent operation")
	}
}

// TestJavaReleaseControlsRecordProjection verifies syntax newer than the sealed release cannot contribute schemas.
func TestJavaReleaseControlsRecordProjection(t *testing.T) {
	source := []byte(`package demo; import org.springframework.web.bind.annotation.*; @RestController class C {@GetMapping(path="/x",produces="application/json") Item x(){return null;}} record Item(String value){}`)
	snapshot, _ := NewSnapshot("repo", testRevision, map[string][]byte{"src/main/java/demo/C.java": source})
	config := testConfig()
	config.JavaRelease = 11
	input, _ := NewInput(snapshot, config)
	result, err := (Inspector{}).Inspect(context.Background(), input, "spring")
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Snapshot.Operations[0].Responses["200"].Content) != 0 {
		t.Fatal("record projected under Java 11")
	}
}

// TestRequestBodyAndDefaultValueRequirednessFollowSpringDeclarations verifies body media types and explicit/default required semantics.
func TestRequestBodyAndDefaultValueRequirednessFollowSpringDeclarations(t *testing.T) {
	source := []byte(`package demo; import org.springframework.web.bind.annotation.*; @RestController class C {@PostMapping(path="/x",consumes={"application/json","application/cbor"}) String x(@RequestBody(required=false) String body,@RequestParam(defaultValue="all") String mode){return "";}}`)
	result, _ := inspectFixture(t, map[string][]byte{"src/main/java/demo/C.java": source})
	op := result.Snapshot.Operations[0]
	if op.RequestBody == nil || op.RequestBody.Required || len(op.RequestBody.Content) != 2 {
		t.Fatalf("body=%+v", op.RequestBody)
	}
	if len(op.Parameters) != 1 || op.Parameters[0].Required {
		t.Fatalf("parameters=%+v", op.Parameters)
	}
}

// TestResponseEntityWithoutFixedStatusUsesPartialDefaultSelector verifies runtime status is not invented as HTTP 200.
func TestResponseEntityWithoutFixedStatusUsesPartialDefaultSelector(t *testing.T) {
	source := []byte(`package demo; import org.springframework.web.bind.annotation.*; import org.springframework.http.ResponseEntity; @RestController class C {@GetMapping(path="/x",produces="text/plain") ResponseEntity<String> x(){return service.reply();}}`)
	result, _ := inspectFixture(t, map[string][]byte{"src/main/java/demo/C.java": source})
	op := result.Snapshot.Operations[0]
	if _, ok := op.Responses["default"]; !ok {
		t.Fatalf("responses=%+v", op.Responses)
	}
	if _, ok := op.Responses["200"]; ok {
		t.Fatal("runtime ResponseEntity status invented")
	}
}

// TestResponseEntityConstructorAndJacksonCustomizationRemainConservative verifies exact constructor status and custom shape omission.
func TestResponseEntityConstructorAndJacksonCustomizationRemainConservative(t *testing.T) {
	constructor := []byte(`package demo; import org.springframework.web.bind.annotation.*; import org.springframework.http.ResponseEntity; import org.springframework.http.HttpStatus; @RestController class C {@GetMapping(path="/x",produces="text/plain") ResponseEntity<String> x(){return new ResponseEntity<String>("ok",HttpStatus.CONFLICT);}}`)
	result, _ := inspectFixture(t, map[string][]byte{"src/main/java/demo/C.java": constructor})
	if _, ok := result.Snapshot.Operations[0].Responses["409"]; !ok {
		t.Fatalf("constructor responses=%+v", result.Snapshot.Operations[0].Responses)
	}
	custom := []byte(`package demo; import org.springframework.web.bind.annotation.*; import com.fasterxml.jackson.databind.annotation.JsonSerialize; @RestController class C {@GetMapping(path="/y",produces="application/json") Custom y(){return null;}} @JsonSerialize class Custom {String secret;}`)
	result, _ = inspectFixture(t, map[string][]byte{"src/main/java/demo/C.java": custom})
	if len(result.Snapshot.Operations[0].Responses["200"].Content) != 0 {
		t.Fatal("custom Jackson shape projected")
	}
}

// TestMappingAliasesDynamicCrossProductAndDiagnostics verifies conflict, limit and bounded diagnostic branches.
func TestMappingAliasesDynamicCrossProductAndDiagnostics(t *testing.T) {
	state := analysisState{input: Input{config: testConfig()}, constants: map[string][]string{}, ctx: context.Background()}
	if _, ok := state.annotationAttributes([]token{{text: "path"}, {text: "="}, {text: "\"/a\""}, {text: ","}, {text: "path"}, {text: "="}, {text: "\"/b\""}}, map[string][]string{}); ok {
		t.Fatal("conflicting alias accepted")
	}
	if crossPaths([]string{"/a", "/b"}, []string{"/c", "/d"}, 3) != nil {
		t.Fatal("cross product limit ignored")
	}
	if got := intersect([]string{"GET", "POST"}, []string{"POST", "PUT"}); len(got) != 1 || got[0] != "POST" {
		t.Fatalf("intersection=%v", got)
	}
	state.input.config.Limits.MaxDiagnostics = 1
	state.addDiagnostic(Diagnostic{Code: DiagnosticPartial, Path: "a"})
	state.addDiagnostic(Diagnostic{Code: DiagnosticSyntax, Path: "b"})
	state.addDiagnostic(Diagnostic{Code: DiagnosticSyntax, Path: "FORBIDDEN_CANARY"})
	if len(state.diagnostics) != 1 || state.omitted != 2 {
		t.Fatalf("diagnostics=%d omitted=%d", len(state.diagnostics), state.omitted)
	}
	if httpStatus("HttpStatus.NOT_FOUND") != "404" || httpStatus("599") != "599" || httpStatus("UNKNOWN") != "" {
		t.Fatal("status mapping invalid")
	}
	if parts := splitGeneric("String,List<Integer>"); len(parts) != 2 {
		t.Fatalf("generic=%v", parts)
	}
}

// failingRepository is a capability spy that returns a collaborator error which must be redacted.
type failingRepository struct {
	called bool
	err    error
}

// ReadSnapshot records invocation and returns only its configured operational outcome.
func (r *failingRepository) ReadSnapshot(context.Context, SnapshotRequest) (map[string][]byte, error) {
	r.called = true
	return nil, r.err
}

// TestFreezeValidationPrecedesIOAndSourceClassifiesFailures verifies fail-closed composition and adapter errors.
func TestFreezeValidationPrecedesIOAndSourceClassifiesFailures(t *testing.T) {
	repo := &failingRepository{err: errors.New("FORBIDDEN_CANARY")}
	config := testConfig()
	config.Revision = "main"
	if _, err := FreezeConfiguredRepository(context.Background(), repo, "root", config); err == nil || repo.called {
		t.Fatal("invalid authority reached repository")
	}
	config = testConfig()
	if _, err := FreezeConfiguredRepository(context.Background(), repo, "root", config); err == nil || err.Error() == "FORBIDDEN_CANARY" {
		t.Fatalf("unsafe repository error: %v", err)
	}
	_ = SystemClock{}.Now()
	bad := contractcatalog.NewSourceRequest(contractcatalog.SourceDescriptor{})
	if _, err := (DerivedSource{}).Load(context.Background(), bad); err == nil {
		t.Fatal("invalid source accepted")
	}
	snapshot, _ := NewSnapshot("repo", testRevision, map[string][]byte{"src/main/java/X.java": []byte(`class X{}`)})
	input, _ := NewInput(snapshot, testConfig())
	limits := contractcatalog.Limits{MaxSources: 1, MaxOperations: 10, MaxClaims: 100, MaxBytes: 1 << 20, MaxDepth: 20, MaxDiagnostics: 20, MaxLocationBytes: 512}
	request := SourceRequest("spring", input, limits, testProjection())
	_, err := (DerivedSource{}).Load(context.Background(), request)
	var catalogErr *contractcatalog.Error
	if !errors.As(err, &catalogErr) || catalogErr.Category != contractcatalog.CategoryInsufficient {
		t.Fatalf("classification=%v", err)
	}
}

// TestSnapshotAndInputOwnMutableAliases verifies digests and results cannot drift through caller mutation.
func TestSnapshotAndInputOwnMutableAliases(t *testing.T) {
	files := map[string][]byte{"src/main/java/X.java": []byte(`class X{}`)}
	snapshot, _ := NewSnapshot("repo", testRevision, files)
	config := testConfig()
	input, _ := NewInput(snapshot, config)
	digest := input.Digest()
	files["src/main/java/X.java"][0] = 'X'
	config.SourceSets[0].Root = "mutated"
	config.Projection.ForbiddenValues[0] = "mutated"
	if input.Digest() != digest || string(input.snapshot.files["src/main/java/X.java"])[0] != 'c' {
		t.Fatal("sealed aliases drifted")
	}
	if (&Error{Category: CategoryInvalidInput, Stage: "x"}).Error() == "" {
		t.Fatal("empty safe error")
	}
	if cloneStrings(nil) != nil {
		t.Fatal("nil policy changed")
	}
	if _, ok := safeRelativePath(`C:\x`); ok {
		t.Fatal("volume path accepted")
	}
	if validRevision("zzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzz") {
		t.Fatal("invalid oid accepted")
	}
	ctx, cancel := context.WithDeadline(context.Background(), time.Now().Add(time.Hour))
	defer cancel()
	bounded, stop := inspectionContext(ctx, time.Minute, time.Now().Add(time.Second))
	defer stop()
	deadline, _ := bounded.Deadline()
	if time.Until(deadline) > 2*time.Second {
		t.Fatal("sealed deadline ignored")
	}
}

// TestPermutationAndConcurrentInspectionRemainDeterministic verifies immutable inputs produce one digest and projection under concurrency.
func TestPermutationAndConcurrentInspectionRemainDeterministic(t *testing.T) {
	a := map[string][]byte{"src/main/java/demo/C.java": []byte(`package demo; import org.springframework.web.bind.annotation.*; @RestController class C {@GetMapping("/x") String x(){return "";}}`), "src/main/java/demo/Z.java": []byte(`package demo; class Z{}`)}
	b := map[string][]byte{"src/main/java/demo/Z.java": a["src/main/java/demo/Z.java"], "src/main/java/demo/C.java": a["src/main/java/demo/C.java"]}
	sa, _ := NewSnapshot("repo", testRevision, a)
	sb, _ := NewSnapshot("repo", testRevision, b)
	ia, _ := NewInput(sa, testConfig())
	ib, _ := NewInput(sb, testConfig())
	if ia.Digest() != ib.Digest() {
		t.Fatal("permutation changed digest")
	}
	results := make(chan string, 16)
	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			result, err := (Inspector{}).Inspect(context.Background(), ia, "spring")
			if err != nil {
				results <- err.Error()
				return
			}
			results <- result.Digest
		}()
	}
	wg.Wait()
	close(results)
	expected := ""
	for value := range results {
		if expected == "" {
			expected = value
		}
		if value != expected {
			t.Fatalf("concurrent drift %s != %s", value, expected)
		}
	}
}
