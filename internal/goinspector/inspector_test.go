package goinspector

import (
	"context"
	"encoding/json"
	"errors"
	"go/ast"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/asierhr/featureProof/internal/contractcatalog"
)

// mutatingRepositoryFS simulates a checkout that changes at the filesystem capability boundary.
type mutatingRepositoryFS struct {
	files map[string][]byte
	err   error
}

// ReadSnapshot returns one adversarial materialization without granting the inspector ambient filesystem access.
func (f mutatingRepositoryFS) ReadSnapshot(ctx context.Context, request SnapshotRequest) (map[string][]byte, error) {
	if f.err != nil {
		return nil, f.err
	}
	copyFiles := map[string][]byte{}
	for name, content := range f.files {
		copyFiles[name] = append([]byte(nil), content...)
	}
	return copyFiles, nil
}

// blockingRepositoryFS proves cancellation interrupts repository materialization before any partial snapshot exists.
type blockingRepositoryFS struct{}

// ReadSnapshot waits exclusively on the sealed context deadline.
func (blockingRepositoryFS) ReadSnapshot(ctx context.Context, request SnapshotRequest) (map[string][]byte, error) {
	<-ctx.Done()
	return nil, cancelled("fake-filesystem", ctx.Err())
}

// TestRepositoryFSCapabilityIsAtomicAndCancelable verifies capability errors and mid-read deadlines expose no snapshot.
func TestRepositoryFSCapabilityIsAtomicAndCancelable(t *testing.T) {
	limits := testLimits()
	if snapshot, err := freezeWithRepositoryFS(context.Background(), mutatingRepositoryFS{err: typedError(CategoryInvalidInput, "changed", nil)}, "ignored", "repo", testRevision, []string{"app"}, limits); err == nil || snapshot.digest != "" {
		t.Fatalf("failed repository capability leaked a snapshot: %#v %v", snapshot, err)
	}
	limits.MaxDuration = time.Millisecond
	if snapshot, err := freezeWithRepositoryFS(context.Background(), blockingRepositoryFS{}, "ignored", "repo", testRevision, []string{"app"}, limits); !errors.Is(err, context.DeadlineExceeded) || snapshot.digest != "" {
		t.Fatalf("deadline did not fail atomically: %#v %v", snapshot, err)
	}
}

// TestProjectionCanariesNeverReachPublicSurfaces verifies sensitive filenames and symbols are replaced before catalog projection.
func TestProjectionCanariesNeverReachPublicSurfaces(t *testing.T) {
	canary := "PRIVATE_CANARY"
	input := sealedInput(t, map[string][]byte{"app/" + canary + ".go": []byte("package app\nimport \"net/http\"\nfunc " + canary + "(w http.ResponseWriter,r *http.Request){w.WriteHeader(204)}\nfunc routes(){http.HandleFunc(\"GET /safe\"," + canary + ")}\n")}, func(config *Configuration) {
		config.Projection.SensitiveNames = []string{canary}
		config.Projection.ForbiddenValues = []string{canary}
	})
	result, err := (Inspector{}).Inspect(context.Background(), input, "source")
	if err != nil {
		t.Fatal(err)
	}
	encoded, _ := json.Marshal(result)
	if strings.Contains(string(encoded), canary) {
		t.Fatalf("projection leaked canary: %s", encoded)
	}
}

// TestEntryClosureRejectsDeadAndHomonymousFacts verifies only the configured lexical root and exact standard-library bindings contribute facts.
func TestEntryClosureRejectsDeadAndHomonymousFacts(t *testing.T) {
	input := sealedInput(t, map[string][]byte{"app/main.go": []byte(`package app
import "net/http"
type fakeRequest struct{URL struct{}}; type fakeWriter struct{}
func (fakeRequest) PathValue(string) string{return ""}; func (fakeWriter) WriteHeader(int){}
func live(w http.ResponseWriter,r *http.Request){_=r.PathValue("id");w.WriteHeader(204)}
func dead(w http.ResponseWriter,r *http.Request){w.WriteHeader(500)}
func routes(){http.HandleFunc("GET /live/{id}",live)}
func unused(){http.HandleFunc("GET /dead",dead)}
`)}, nil)
	result, err := (Inspector{}).Inspect(context.Background(), input, "source")
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Snapshot.Operations) != 1 || result.Snapshot.Operations[0].Key.Path != "/live/{id}" || len(result.Snapshot.Operations[0].Parameters) != 1 {
		t.Fatalf("lexical closure escaped: %+v", result.Snapshot.Operations)
	}
}

// TestPlatformHandleOpenerRejectsObjectKindMismatch verifies validation applies to the retained operating-system handle.
func TestPlatformHandleOpenerRejectsObjectKindMismatch(t *testing.T) {
	root := t.TempDir()
	plain := filepath.Join(root, "plain.go")
	if err := os.WriteFile(plain, []byte("package plain"), 0o600); err != nil {
		t.Fatal(err)
	}
	if handle, err := openNoFollow(plain, false); err != nil {
		t.Fatal(err)
	} else {
		_ = handle.Close()
	}
	if handle, err := openNoFollow(root, true); err != nil {
		t.Fatal(err)
	} else {
		_ = handle.Close()
	}
	if handle, err := openNoFollow(plain, true); err == nil {
		_ = handle.Close()
		t.Fatal("file accepted as directory")
	}
	if handle, err := openNoFollow(root, false); err == nil {
		_ = handle.Close()
		t.Fatal("directory accepted as file")
	}
	if _, err := openNoFollow(filepath.Join(root, "missing"), false); err == nil {
		t.Fatal("missing object accepted")
	}
}

// TestFilesystemAndFilenameBoundaryMatrix covers sealed build suffixes and atomic repository budget failures.
func TestFilesystemAndFilenameBoundaryMatrix(t *testing.T) {
	build := BuildContext{GOOS: "linux", GOARCH: "amd64"}
	cases := map[string]bool{"x.go": true, "x_linux.go": true, "x_amd64.go": true, "x_linux_amd64.go": true, "x_windows.go": false, "x_linux_arm64.go": false, "x_test.go": true}
	for name, expected := range cases {
		if actual := matchFilename(name, build); actual != expected {
			t.Fatalf("%s: got %v", name, actual)
		}
	}
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "app"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "app", "a.go"), []byte("package app"), 0o600); err != nil {
		t.Fatal(err)
	}
	limits := testLimits()
	limits.MaxBytes = 1
	if snapshot, err := freezeRepository(context.Background(), root, "repo", testRevision, []string{"app"}, limits); err == nil || snapshot.digest != "" {
		t.Fatalf("byte limit leaked: %#v %v", snapshot, err)
	}
	limits = testLimits()
	limits.MaxFiles = 0
	if _, err := freezeRepository(context.Background(), root, "repo", testRevision, []string{"app"}, limits); err == nil {
		t.Fatal("invalid limits accepted")
	}
	limits = testLimits()
	if _, err := freezeRepository(context.Background(), root, "repo", testRevision, []string{"../escape"}, limits); err == nil {
		t.Fatal("escape accepted")
	}
	if _, err := freezeRepository(context.Background(), root, "repo", testRevision, []string{"missing"}, limits); err == nil {
		t.Fatal("missing root accepted")
	}
}

// TestSmallStaticHelpersRejectAmbiguity covers malformed selector, builder return, and schema paths.
func TestSmallStaticHelpersRejectAmbiguity(t *testing.T) {
	function := &ast.FuncDecl{Body: &ast.BlockStmt{List: []ast.Stmt{&ast.ReturnStmt{Results: []ast.Expr{ast.NewIdent("a")}}, &ast.ReturnStmt{Results: []ast.Expr{ast.NewIdent("b")}}}}}
	if returnedRouter(function) != "" {
		t.Fatal("ambiguous builder accepted")
	}
	if _, _, ok := callSelector(ast.NewIdent("plain")); ok {
		t.Fatal("plain call accepted as selector")
	}
	state := analysisState{ctx: context.Background(), input: Input{config: testConfig()}, types: map[string]ast.Expr{}}
	if schema, ok := state.schemaForExpr(ast.NewIdent("string"), map[string]bool{}, 0); !ok || schema.Type != "string" {
		t.Fatal("schema wrapper failed")
	}
	if schema, ok := state.schemaForExpr(&ast.MapType{Key: ast.NewIdent("int"), Value: ast.NewIdent("string")}, map[string]bool{}, 0); ok || schema != nil {
		t.Fatal("unsupported map key accepted")
	}
	if schema, ok := state.schemaForExpr(&ast.InterfaceType{}, map[string]bool{}, 0); ok || schema != nil {
		t.Fatal("interface schema accepted")
	}
	if schema, ok := state.schemaForName("Missing", map[string]bool{}, 0); ok || schema != nil {
		t.Fatal("missing type accepted")
	}
	if schema, ok := state.schemaForName("string", map[string]bool{}, testConfig().Limits.MaxDepth); ok || schema != nil {
		t.Fatal("depth limit ignored")
	}
	if funcSymbol(&ast.FuncDecl{Name: ast.NewIdent("Call"), Recv: &ast.FieldList{List: []*ast.Field{{Type: ast.NewIdent("Receiver")}}}}) != "Receiver.Call" || funcSymbol(&ast.FuncDecl{Name: ast.NewIdent("plain")}) != "plain" {
		t.Fatal("function symbol mismatch")
	}
	if exprSymbol(&ast.StarExpr{X: ast.NewIdent("value")}) != "value" || exprSymbol(&ast.CallExpr{Fun: ast.NewIdent("make")}) != "" {
		t.Fatal("expression symbol mismatch")
	}
	if exprSymbol(&ast.SelectorExpr{X: ast.NewIdent("pkg"), Sel: ast.NewIdent("Value")}) != "pkg.Value" || exprSymbol(&ast.FuncLit{}) != "func_literal" {
		t.Fatal("selector symbol mismatch")
	}
	if boundedSymbol(strings.Repeat("x", 129)) != "symbol" || boundedSymbol("unsafe/name") != "symbol" {
		t.Fatal("unsafe symbol retained")
	}
	if joinRoute("/api/", "/users") != "/api/users" || !routeHasParameter("/x/{id}", "id") {
		t.Fatal("route helper mismatch")
	}
	if name, omit, _ := jsonField("Field", &ast.BasicLit{Value: "`json:\"-\"`"}); name != "" || !omit {
		t.Fatal("json omission mismatch")
	}
	if name, omit, optional := jsonField("Field", &ast.BasicLit{Value: "`json:\",omitempty\"`"}); name != "Field" || omit || !optional {
		t.Fatal("json optional mismatch")
	}
	if generatedFile([]byte("// Code generated by tool. DO NOT EDIT.\npackage p")) != true || generatedFile([]byte("package p")) {
		t.Fatal("generated marker mismatch")
	}
	if values := uniqueSchemes([]contractcatalog.SecurityScheme{{Name: "a"}, {Name: "a"}, {Name: "b"}}); len(values) != 2 {
		t.Fatal("scheme deduplication mismatch")
	}
	if matchBuild([]byte("//go:build windows\npackage p"), BuildContext{GOOS: "linux", GOARCH: "amd64"}) || !matchBuild([]byte("package p"), BuildContext{GOOS: "linux", GOARCH: "amd64"}) || matchBuild([]byte("//go:build (\npackage p"), BuildContext{GOOS: "linux", GOARCH: "amd64"}) {
		t.Fatal("build constraint mismatch")
	}
}

// TestChangedPathClosureIncludesBuilderMiddlewareHandlerAndSchema verifies every proven local dependency affects its operation.
func TestChangedPathClosureIncludesBuilderMiddlewareHandlerAndSchema(t *testing.T) {
	files := map[string][]byte{
		"app/root.go":    []byte("package app\nimport \"github.com/go-chi/chi/v5\"\nfunc routes(r chi.Router){build(r)}"),
		"app/builder.go": []byte("package app\nimport \"github.com/go-chi/chi/v5\"\nfunc build(r chi.Router){r.Use(auth);r.Post(\"/items\",create)}"),
		"app/auth.go":    []byte("package app\nimport \"net/http\"\nfunc auth(next http.Handler) http.Handler{return next}"),
		"app/handler.go": []byte("package app\nimport(\"encoding/json\";\"net/http\")\nfunc create(w http.ResponseWriter,r *http.Request){var body Payload;_=json.NewDecoder(r.Body).Decode(&body);w.WriteHeader(201)}"),
		"app/types.go":   []byte("package app\ntype Payload struct{Name string}"),
	}
	for _, changed := range []string{"app/root.go", "app/builder.go", "app/auth.go", "app/handler.go", "app/types.go"} {
		input := sealedInput(t, files, func(config *Configuration) {
			config.ChangedPaths = []string{changed}
			config.Auth = []AuthMatcher{{Identity: AuthIdentity{Kind: AuthIdentityLocal, ImportPath: "app", Symbol: "auth"}, Scheme: contractcatalog.SecurityScheme{Name: "bearer", Type: "http", Scheme: "bearer"}}}
		})
		result, err := (Inspector{}).Inspect(context.Background(), input, "source")
		if err != nil {
			t.Fatalf("%s: %v", changed, err)
		}
		if len(result.AffectedOperations) != 1 {
			t.Fatalf("%s not in impact closure: %+v", changed, result)
		}
	}
}

// TestRepositoryDescriptorChainRejectsEnumeratedFileReplacement verifies a deterministic mutation cannot change sealed bytes.
func TestRepositoryDescriptorChainRejectsEnumeratedFileReplacement(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "app"), 0o700); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(root, "app", "main.go")
	if err := os.WriteFile(target, []byte("package app"), 0o600); err != nil {
		t.Fatal(err)
	}
	mutated := false
	capability := LocalRepositoryFS{BeforeOpen: func(stage, relative string) {
		if stage == "file" && relative == "app/main.go" && !mutated {
			mutated = true
			_ = os.WriteFile(target, []byte("package replaced\n"), 0o600)
		}
	}}
	snapshot, err := freezeWithRepositoryFS(context.Background(), capability, root, "repo", testRevision, []string{"app"}, testLimits())
	if err == nil || snapshot.digest != "" {
		t.Fatalf("replacement was sealed: %#v %v", snapshot, err)
	}
}

// TestRepositoryDescriptorChainRejectsRootAndModuleReplacement verifies anchored directory and go.mod identities at race hooks.
func TestRepositoryDescriptorChainRejectsRootAndModuleReplacement(t *testing.T) {
	for _, stage := range []string{"root", "module"} {
		t.Run(stage, func(t *testing.T) {
			root := t.TempDir()
			app := filepath.Join(root, "app")
			if err := os.Mkdir(app, 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(app, "main.go"), []byte("package app"), 0o600); err != nil {
				t.Fatal(err)
			}
			module := filepath.Join(root, "go.mod")
			if err := os.WriteFile(module, []byte("module before.test\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			mutated := false
			replaced := false
			capability := LocalRepositoryFS{BeforeOpen: func(actual, relative string) {
				if actual != stage || mutated {
					return
				}
				mutated = true
				if stage == "module" {
					replaced = os.WriteFile(module, []byte("module replacement.test/longer\n"), 0o600) == nil
					return
				}
				old := app + ".old"
				if os.Rename(app, old) == nil {
					replaced = true
					_ = os.Mkdir(app, 0o700)
					_ = os.WriteFile(filepath.Join(app, "main.go"), []byte("package replacement"), 0o600)
				}
			}}
			snapshot, err := freezeWithRepositoryFS(context.Background(), capability, root, "repo", testRevision, []string{"app"}, testLimits())
			if replaced && err == nil {
				t.Fatalf("%s replacement was sealed: %#v", stage, snapshot)
			}
		})
	}
}

// TestRepositoryStableReadRejectsSameSizeInPlaceWrites verifies file and module bytes cannot change after their retained handle opens.
func TestRepositoryStableReadRejectsSameSizeInPlaceWrites(t *testing.T) {
	for _, relative := range []string{"app/main.go", "go.mod"} {
		for _, boundary := range []string{"opened", "read"} {
			t.Run(strings.ReplaceAll(relative+"_"+boundary, "/", "_"), func(t *testing.T) {
				root := t.TempDir()
				if err := os.Mkdir(filepath.Join(root, "app"), 0o700); err != nil {
					t.Fatal(err)
				}
				original := map[string][]byte{"app/main.go": []byte("package app\n"), "go.mod": []byte("module old.test\n")}
				changed := map[string][]byte{"app/main.go": []byte("package bad\n"), "go.mod": []byte("module new.test\n")}
				for name, content := range original {
					if err := os.WriteFile(filepath.Join(root, filepath.FromSlash(name)), content, 0o600); err != nil {
						t.Fatal(err)
					}
				}
				var writeErr error
				attempted := false
				capability := LocalRepositoryFS{BeforeOpen: func(stage, current string) {
					if attempted || stage != boundary || current != relative {
						return
					}
					attempted = true
					writeErr = os.WriteFile(filepath.Join(root, filepath.FromSlash(relative)), changed[relative], 0o600)
				}}
				snapshot, err := freezeWithRepositoryFS(context.Background(), capability, root, "repo", testRevision, []string{"app"}, testLimits())
				if !attempted {
					t.Fatal("stable-read boundary was not reached")
				}
				if writeErr == nil && (err == nil || snapshot.digest != "") {
					t.Fatalf("same-size in-place mutation produced a snapshot: %#v %v", snapshot, err)
				}
				if writeErr != nil && err != nil && snapshot.digest != "" {
					t.Fatalf("failed read exposed partial snapshot: %#v %v", snapshot, err)
				}
			})
		}
	}
}

// TestUnsupportedResponseControlFlowNeverCrossJoinsFacts verifies branch, switch and loop facts cannot share JSON schemas.
func TestUnsupportedResponseControlFlowNeverCrossJoinsFacts(t *testing.T) {
	for name, body := range map[string]string{"if_else": "if r.Method==\"GET\"{http.Error(w,\"bad\",400)}else{w.Header().Set(\"Content-Type\",\"application/json\");var out Reply;_ = json.NewEncoder(w).Encode(out)}", "switch": "switch r.Method{case \"GET\":http.Error(w,\"bad\",400);default:w.Header().Set(\"Content-Type\",\"application/json\");var out Reply;_ = json.NewEncoder(w).Encode(out)}", "loop": "for i:=0;i<1;i++{http.Error(w,\"bad\",400)};w.Header().Set(\"Content-Type\",\"application/json\");var out Reply;_ = json.NewEncoder(w).Encode(out)", "select": "select{default:http.Error(w,\"bad\",400)};w.Header().Set(\"Content-Type\",\"application/json\");var out Reply;_ = json.NewEncoder(w).Encode(out)"} {
		t.Run(name, func(t *testing.T) {
			source := "package app\nimport(\"encoding/json\";\"net/http\")\ntype Reply struct{OK bool}\nfunc h(w http.ResponseWriter,r *http.Request){" + body + "}\nfunc routes(){http.HandleFunc(\"GET /x\",h)}"
			input := sealedInput(t, map[string][]byte{"app/main.go": []byte(source)}, nil)
			result, err := (Inspector{}).Inspect(context.Background(), input, "source")
			if err != nil {
				t.Fatal(err)
			}
			if response := result.Snapshot.Operations[0].Responses["400"]; len(response.Content) != 0 {
				t.Fatalf("400 inherited unrelated content: %+v", response)
			}
		})
	}
}

// TestPackageIdentityUsesDirectoryAndDeclaredName verifies equal package names never collide and imports resolve declared names.
func TestPackageIdentityUsesDirectoryAndDeclaredName(t *testing.T) {
	files := map[string][]byte{"go.mod": []byte("module example.test/service\n"), "app/routes.go": []byte("package app\nimport(\"net/http\"; alias \"example.test/service/first\")\nfunc routes(){http.HandleFunc(\"GET /right\",alias.Right)}"), "first/handler.go": []byte("package handlers\nimport \"net/http\"\nfunc Right(w http.ResponseWriter,r *http.Request){w.WriteHeader(201)}"), "second/handler.go": []byte("package handlers\nimport \"net/http\"\nfunc Right(w http.ResponseWriter,r *http.Request){w.WriteHeader(500)}")}
	input := sealedInput(t, files, func(config *Configuration) { config.Roots = []string{"app", "first", "second"} })
	result, err := (Inspector{}).Inspect(context.Background(), input, "source")
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := result.Snapshot.Operations[0].Responses["201"]; !ok {
		t.Fatalf("wrong package declaration selected: %+v", result)
	}
	if _, ok := result.Snapshot.Operations[0].Responses["500"]; ok {
		t.Fatal("same-name package collision")
	}
}

// TestServeMuxScopeDistinguishesGlobalFieldAndShadow verifies only lexically visible exact net/http mux objects register routes.
func TestServeMuxScopeDistinguishesGlobalFieldAndShadow(t *testing.T) {
	global := sealedInput(t, map[string][]byte{"app/main.go": []byte("package app\nimport \"net/http\"\nvar mux=http.NewServeMux()\nfunc h(w http.ResponseWriter,r *http.Request){}\nfunc routes(){mux.HandleFunc(\"GET /global\",h)}")}, nil)
	if result, err := (Inspector{}).Inspect(context.Background(), global, "source"); err != nil || result.Snapshot.Operations[0].Key.Path != "/global" {
		t.Fatalf("global mux unresolved: %+v %v", result, err)
	}
	field := sealedInput(t, map[string][]byte{"app/main.go": []byte("package app\nimport \"net/http\"\ntype Server struct{mux *http.ServeMux}\nfunc h(w http.ResponseWriter,r *http.Request){}\nfunc (s *Server) routes(){s.mux.HandleFunc(\"GET /field\",h)}")}, func(config *Configuration) { config.EntrySymbols = []string{"Server.routes"} })
	if result, err := (Inspector{}).Inspect(context.Background(), field, "source"); err != nil || result.Snapshot.Operations[0].Key.Path != "/field" {
		t.Fatalf("field mux unresolved: %+v %v", result, err)
	}
	shadow := sealedInput(t, map[string][]byte{"app/main.go": []byte("package app\nimport \"net/http\"\nvar mux=http.NewServeMux()\ntype fake struct{}\nfunc(fake)HandleFunc(string,any){}\nfunc h(w http.ResponseWriter,r *http.Request){}\nfunc routes(){mux:=fake{};mux.HandleFunc(\"GET /wrong\",h)}")}, nil)
	if _, err := (Inspector{}).Inspect(context.Background(), shadow, "source"); err == nil {
		t.Fatal("shadowed fake mux registered")
	}
}

// TestLexicalObjectsPreventConstantBuilderAndHandlerShadowing verifies package declarations cannot leak through local objects.
func TestLexicalObjectsPreventConstantBuilderAndHandlerShadowing(t *testing.T) {
	constant := sealedInput(t, map[string][]byte{"app/main.go": []byte("package app\nimport \"net/http\"\nconst route=\"GET /wrong\"\nfunc h(w http.ResponseWriter,r *http.Request){}\nfunc routes(){const route=\"GET /right\";http.HandleFunc(route,h)}")}, nil)
	if result, err := (Inspector{}).Inspect(context.Background(), constant, "source"); err != nil || result.Snapshot.Operations[0].Key.Path != "/right" {
		t.Fatalf("local constant scope lost: %+v %v", result, err)
	}
	shadow := sealedInput(t, map[string][]byte{"app/main.go": []byte("package app\nimport \"net/http\"\nfunc h(w http.ResponseWriter,r *http.Request){}\nfunc routes(){h:=func(w http.ResponseWriter,r *http.Request){};http.HandleFunc(\"GET /wrong\",h)}")}, nil)
	if _, err := (Inspector{}).Inspect(context.Background(), shadow, "source"); err == nil {
		t.Fatal("shadowed handler resolved to package function")
	}
}

// TestOperationBudgetFailsAtomicallyDuringTraversal verifies multiple entries cannot publish a truncated success.
func TestOperationBudgetFailsAtomicallyDuringTraversal(t *testing.T) {
	input := sealedInput(t, map[string][]byte{"app/main.go": []byte("package app\nimport \"net/http\"\nfunc h(w http.ResponseWriter,r *http.Request){}\nfunc one(){http.HandleFunc(\"GET /one\",h)}\nfunc two(){http.HandleFunc(\"GET /two\",h)}")}, func(config *Configuration) {
		config.EntrySymbols = []string{"one", "two"}
		config.Limits.MaxOperations = 1
	})
	result, err := (Inspector{}).Inspect(context.Background(), input, "source")
	var typed *Error
	if !errors.As(err, &typed) || typed.Category != CategoryLimitExceeded || len(result.Snapshot.Operations) != 0 {
		t.Fatalf("operation limit returned partial success: %+v %v", result, err)
	}
}

// TestSourceRequestRejectsProjectionDrift verifies composition cannot substitute a second redaction authority.
func TestSourceRequestRejectsProjectionDrift(t *testing.T) {
	input := sealedInput(t, map[string][]byte{"app/main.go": []byte("package app\nfunc routes(){}")}, nil)
	drift := cloneProjection(input.config.Projection)
	drift.ForbiddenValues = []string{"drift"}
	request := SourceRequest("derived", input, contractcatalog.Limits{MaxSources: 1, MaxOperations: 10, MaxClaims: 100, MaxBytes: 1 << 20, MaxDepth: 20, MaxDiagnostics: 20, MaxLocationBytes: 512}, drift)
	_, err := (DerivedSource{}).Load(context.Background(), request)
	var catalogError *contractcatalog.Error
	if !errors.As(err, &catalogError) || catalogError.Category != contractcatalog.CategoryInvalidSource {
		t.Fatalf("projection drift accepted: %v", err)
	}
}

// TestProjectionCanaryMatrixCoversStructuralSurfaces verifies direct results fail closed before any forbidden structural value is returned.
func TestProjectionCanaryMatrixCoversStructuralSurfaces(t *testing.T) {
	const canary = "PRIVATE_MATRIX_CANARY"
	cases := map[string]string{"path": "package app\nimport \"net/http\"\nfunc h(w http.ResponseWriter,r *http.Request){}\nfunc routes(){http.HandleFunc(\"GET /" + canary + "\",h)}", "query": "package app\nimport \"net/http\"\nfunc h(w http.ResponseWriter,r *http.Request){_=r.URL.Query().Get(\"" + canary + "\")}\nfunc routes(){http.HandleFunc(\"GET /x\",h)}", "property": "package app\nimport(\"encoding/json\";\"net/http\")\ntype Body struct{Field string `json:\"" + canary + "\"`}\nfunc h(w http.ResponseWriter,r *http.Request){var b Body;_=json.NewDecoder(r.Body).Decode(&b)}\nfunc routes(){http.HandleFunc(\"POST /x\",h)}", "diagnostic": "package app\nimport(\"encoding/json\";\"net/http\")\nfunc h(w http.ResponseWriter,r *http.Request){var b " + canary + ";_=json.NewDecoder(r.Body).Decode(&b)}\nfunc routes(){http.HandleFunc(\"POST /x\",h)}"}
	for name, source := range cases {
		t.Run(name, func(t *testing.T) {
			input := sealedInput(t, map[string][]byte{"app/main.go": []byte(source)}, func(config *Configuration) { config.Projection.ForbiddenValues = []string{canary} })
			result, err := (Inspector{}).Inspect(context.Background(), input, "source")
			encoded, _ := json.Marshal(result)
			if err == nil || strings.Contains(string(encoded), canary) || strings.Contains(err.Error(), canary) {
				t.Fatalf("canary crossed %s: %s %v", name, encoded, err)
			}
		})
	}
	input := sealedInput(t, map[string][]byte{"app/main.go": []byte("package app\nimport(\"net/http\"; secret \"example.invalid/" + canary + "\")\nvar _=secret.Value\nfunc h(w http.ResponseWriter,r *http.Request){}\nfunc routes(){http.HandleFunc(\"GET /safe\",h)}")}, func(config *Configuration) { config.Projection.ForbiddenValues = []string{canary} })
	result, err := (Inspector{}).Inspect(context.Background(), input, "source")
	encoded, _ := json.Marshal(result)
	if err != nil || strings.Contains(string(encoded), canary) {
		t.Fatalf("private import leaked: %s %v", encoded, err)
	}
}

// TestResponseTypeAndImportedMiddlewareCarryExactProvenanceAndImpact verifies material claims point to their proving declarations.
func TestResponseTypeAndImportedMiddlewareCarryExactProvenanceAndImpact(t *testing.T) {
	files := map[string][]byte{"go.mod": []byte("module example.test/service\n"), "app/routes.go": []byte("package app\nimport(\"net/http\";\"encoding/json\";\"github.com/go-chi/chi/v5\"; auth \"example.test/service/security\")\nfunc h(w http.ResponseWriter,r *http.Request){w.Header().Set(\"Content-Type\",\"application/json\");var out Reply;_ = json.NewEncoder(w).Encode(out)}\nfunc routes(r chi.Router){r.Use(auth.Require);r.Get(\"/x\",h)}"), "app/types.go": []byte("package app\ntype Reply struct{OK bool}"), "security/auth.go": []byte("package authz\nimport \"net/http\"\nfunc Require(next http.Handler)http.Handler{return next}")}
	for _, changed := range []string{"app/types.go", "security/auth.go"} {
		input := sealedInput(t, files, func(config *Configuration) {
			config.Roots = []string{"app", "security"}
			config.ChangedPaths = []string{changed}
			config.Auth = []AuthMatcher{{Identity: AuthIdentity{Kind: AuthIdentityLocal, ImportPath: "example.test/service/security", Symbol: "Require"}, Scheme: contractcatalog.SecurityScheme{Name: "bearer", Type: "http", Scheme: "bearer"}}}
		})
		result, err := (Inspector{}).Inspect(context.Background(), input, "source")
		if err != nil {
			t.Fatalf("%s: %v", changed, err)
		}
		if len(result.AffectedOperations) != 1 {
			t.Fatalf("%s absent from closure", changed)
		}
		encoded, _ := json.Marshal(result.Snapshot.Operations[0].Claims)
		if !strings.Contains(string(encoded), "app/types.go") || !strings.Contains(string(encoded), "Reply.OK") || !strings.Contains(string(encoded), "security/auth.go") {
			t.Fatalf("exact provenance missing: %s", encoded)
		}
	}
}

// TestImplicitLocalImportUsesDeclaredPackageName verifies an unaliased import resolves the package clause rather than its directory basename.
func TestImplicitLocalImportUsesDeclaredPackageName(t *testing.T) {
	files := map[string][]byte{"go.mod": []byte("module example.test/service\n"), "app/routes.go": []byte("package app\nimport(\"net/http\";\"github.com/go-chi/chi/v5\";\"example.test/service/security\")\nfunc h(w http.ResponseWriter,r *http.Request){}\nfunc routes(r chi.Router){r.Use(authz.Require);r.Get(\"/x\",h)}"), "security/auth.go": []byte("package authz\nimport \"net/http\"\nfunc Require(next http.Handler)http.Handler{return next}")}
	input := sealedInput(t, files, func(config *Configuration) {
		config.Roots = []string{"app", "security"}
		config.Auth = []AuthMatcher{{Identity: AuthIdentity{Kind: AuthIdentityLocal, ImportPath: "example.test/service/security", Symbol: "Require"}, Scheme: contractcatalog.SecurityScheme{Name: "bearer", Type: "http", Scheme: "bearer"}}}
	})
	result, err := (Inspector{}).Inspect(context.Background(), input, "source")
	if err != nil || len(result.Snapshot.Operations) != 1 || len(result.Snapshot.Operations[0].Security) != 1 {
		t.Fatalf("declared import name unresolved: %+v %v", result, err)
	}
}

// TestServeMuxShadowsCoverParametersVarsNestedBlocksAndFields verifies every lexical shadow hides package or receiver mux authority.
func TestServeMuxShadowsCoverParametersVarsNestedBlocksAndFields(t *testing.T) {
	cases := map[string]string{
		"parameter": "var mux=http.NewServeMux();func routes(mux fake){mux.HandleFunc(\"GET /wrong\",h)}",
		"var":       "var mux=http.NewServeMux();func routes(){var mux fake;mux.HandleFunc(\"GET /wrong\",h)}",
		"nested":    "var mux=http.NewServeMux();func routes(){{var mux fake;mux.HandleFunc(\"GET /wrong\",h)}}",
		"field":     "type Server struct{mux *http.ServeMux};type fakeServer struct{mux fake};func(s *Server)routes(){{s:=fakeServer{};s.mux.HandleFunc(\"GET /wrong\",h)}}",
	}
	for name, declaration := range cases {
		t.Run(name, func(t *testing.T) {
			source := "package app\nimport \"net/http\"\ntype fake struct{}\nfunc(fake)HandleFunc(string,any){}\nfunc h(w http.ResponseWriter,r *http.Request){}\n" + declaration
			input := sealedInput(t, map[string][]byte{"app/main.go": []byte(source)}, func(config *Configuration) {
				if name == "field" {
					config.EntrySymbols = []string{"Server.routes"}
				}
			})
			if _, err := (Inspector{}).Inspect(context.Background(), input, "source"); err == nil {
				t.Fatal("shadowed mux registered a route")
			}
		})
	}
}

// TestQueryRequiredRejectsHomonymousAndShadowedWriters verifies only the bound response writer proves a terminal rejection.
func TestQueryRequiredRejectsHomonymousAndShadowedWriters(t *testing.T) {
	cases := map[string]string{
		"fake_error":       "f:=fake{};if q==\"\"{f.Error(w,\"bad\",400);return}",
		"shadowed_writer":  "if q==\"\"{w:=fake{};w.WriteHeader(400);return}",
		"shadowed_package": "http:=fake{};if q==\"\"{http.Error(w,\"bad\",400);return}",
	}
	for name, guard := range cases {
		t.Run(name, func(t *testing.T) {
			source := "package app\nimport \"net/http\"\ntype fake struct{}\nfunc(fake)Error(http.ResponseWriter,string,int){}\nfunc(fake)WriteHeader(int){}\nfunc h(w http.ResponseWriter,r *http.Request){q:=r.URL.Query().Get(\"q\");" + guard + ";w.WriteHeader(200)}\nfunc routes(){http.HandleFunc(\"GET /x\",h)}"
			result, err := (Inspector{}).Inspect(context.Background(), sealedInput(t, map[string][]byte{"app/main.go": []byte(source)}, nil), "source")
			if err != nil {
				t.Fatal(err)
			}
			if len(result.Snapshot.Operations[0].Parameters) != 1 || result.Snapshot.Operations[0].Parameters[0].Required {
				t.Fatalf("homonymous rejection made query required: %+v", result.Snapshot.Operations[0].Parameters)
			}
		})
	}
}

// TestResponseInterpreterCarriesStatusMediaAndContinuationPerPath verifies no implicit 200 or cross-branch schema is fabricated.
func TestResponseInterpreterCarriesStatusMediaAndContinuationPerPath(t *testing.T) {
	cases := map[string]struct {
		body     string
		statuses []string
	}{
		"linear_201":     {body: "w.WriteHeader(201);w.Header().Set(\"Content-Type\",\"application/json\");var out Reply;_=json.NewEncoder(w).Encode(out)", statuses: []string{"201"}},
		"terminal_guard": {body: "if r.Method==\"\"{http.Error(w,\"bad\",400);return};w.Header().Set(\"Content-Type\",\"application/json\");var out Reply;_=json.NewEncoder(w).Encode(out)", statuses: []string{"200", "400"}},
		"nonterminal":    {body: "if r.Method==\"GET\"{w.WriteHeader(201)};w.Header().Set(\"Content-Type\",\"application/json\");var out Reply;_=json.NewEncoder(w).Encode(out)", statuses: []string{"200", "201"}},
		"if_else":        {body: "if r.Method==\"GET\"{w.WriteHeader(201)}else{w.WriteHeader(202)};w.Header().Set(\"Content-Type\",\"application/json\");var out Reply;_=json.NewEncoder(w).Encode(out)", statuses: []string{"201", "202"}},
	}
	for name, test := range cases {
		t.Run(name, func(t *testing.T) {
			source := "package app\nimport(\"encoding/json\";\"net/http\")\ntype Reply struct{OK bool}\nfunc h(w http.ResponseWriter,r *http.Request){" + test.body + "}\nfunc routes(){http.HandleFunc(\"GET /x\",h)}"
			result, err := (Inspector{}).Inspect(context.Background(), sealedInput(t, map[string][]byte{"app/main.go": []byte(source)}, nil), "source")
			if err != nil {
				t.Fatal(err)
			}
			responses := result.Snapshot.Operations[0].Responses
			if len(responses) != len(test.statuses) {
				t.Fatalf("unexpected path statuses: %+v", responses)
			}
			for _, status := range test.statuses {
				if _, ok := responses[status]; !ok {
					t.Fatalf("missing status %s: %+v", status, responses)
				}
			}
		})
	}
}

// TestNestedRequestAndResponseSchemaClosureCarriesImpactAndProvenance verifies every projected alias, type and field enters the PR closure.
func TestNestedRequestAndResponseSchemaClosureCarriesImpactAndProvenance(t *testing.T) {
	files := map[string][]byte{
		"app/routes.go":    []byte("package app\nimport(\"encoding/json\";\"net/http\")\nfunc h(w http.ResponseWriter,r *http.Request){var in Payload;_=json.NewDecoder(r.Body).Decode(&in);w.Header().Set(\"Content-Type\",\"application/json\");var out Reply;_=json.NewEncoder(w).Encode(out)}\nfunc routes(){http.HandleFunc(\"POST /x\",h)}"),
		"app/request.go":   []byte("package app\ntype Payload struct{Address RequestAddress}"),
		"app/request2.go":  []byte("package app\ntype RequestAddress = AddressFields\ntype AddressFields struct{Street string}"),
		"app/response.go":  []byte("package app\ntype Reply struct{Address ResponseAddress}"),
		"app/response2.go": []byte("package app\ntype ResponseAddress = ReplyFields\ntype ReplyFields struct{City string}"),
	}
	for _, changed := range []string{"app/request2.go", "app/response2.go"} {
		input := sealedInput(t, files, func(config *Configuration) { config.ChangedPaths = []string{changed} })
		result, err := (Inspector{}).Inspect(context.Background(), input, "source")
		if err != nil {
			t.Fatalf("%s: %v", changed, err)
		}
		if len(result.AffectedOperations) != 1 {
			t.Fatalf("nested schema missing from impact for %s: %+v", changed, result.AffectedOperations)
		}
		claims, _ := json.Marshal(result.Snapshot.Operations[0].Claims)
		if !strings.Contains(string(claims), changed) {
			t.Fatalf("nested schema provenance missing %s: %s", changed, claims)
		}
	}
}

// testRevision is an immutable fixture revision accepted by the sealed authority boundary.
const testRevision = "0123456789abcdef0123456789abcdef01234567"

// testLimits returns explicit generous limits while individual tests reduce one dimension at a time.
func testLimits() Limits {
	return Limits{MaxFiles: 20, MaxBytes: 1 << 20, MaxPackages: 10, MaxASTNodes: 10000, MaxDepth: 12, MaxTypes: 100, MaxOperations: 100, MaxDiagnostics: 20, MaxDuration: time.Second}
}

// testConfig creates the trusted composition values shared by hermetic fixture tests.
func testConfig() Configuration {
	return Configuration{RepositoryID: "repo", Revision: testRevision, Roots: []string{"app"}, Build: BuildContext{GOOS: "linux", GOARCH: "amd64"}, Limits: testLimits(), Confidence: contractcatalog.ConfidenceMedium, Completeness: contractcatalog.CompletenessPartial, EntrySymbols: []string{"routes"}, Projection: contractcatalog.ProjectionPolicy{MaxStringBytes: 512, AllowedURLSchemes: []string{"https"}, SensitiveNames: []string{}, SensitivePaths: []string{}, SensitiveHeaderNames: []string{}, ForbiddenValues: []string{}}}
}

// sealedInput creates an immutable input and fails the current test at the authority boundary.
func sealedInput(t *testing.T, files map[string][]byte, mutate func(*Configuration)) Input {
	t.Helper()
	snapshot, err := NewSnapshot("repo", testRevision, files)
	if err != nil {
		t.Fatal(err)
	}
	config := testConfig()
	if mutate != nil {
		mutate(&config)
	}
	input, err := newInput(snapshot, config, time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	return input
}

// TestInspectionRejectsUnsealedRevisionAndEscapingPaths covers revision, root, changed-path, and defensive-copy authority rules.
func TestInspectionRejectsUnsealedRevisionAndEscapingPaths(t *testing.T) {
	if _, err := NewSnapshot("repo", "main", map[string][]byte{"app/main.go": []byte("package app")}); err == nil {
		t.Fatal("mutable revision accepted")
	}
	if _, err := NewSnapshot("repo", testRevision, map[string][]byte{"../escape.go": []byte("package app")}); err == nil {
		t.Fatal("escape accepted")
	}
	snapshot, err := NewSnapshot("repo", testRevision, map[string][]byte{"app/main.go": []byte("package app")})
	if err != nil {
		t.Fatal(err)
	}
	config := testConfig()
	config.ChangedPaths = []string{"other/file.go"}
	if _, err = newInput(snapshot, config, time.Time{}); err == nil {
		t.Fatal("changed path outside root accepted")
	}
	config = testConfig()
	config.Roots = []string{"app"}
	input, err := newInput(snapshot, config, time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	config.Roots[0] = "other"
	if input.config.Roots[0] != "app" {
		t.Fatal("configuration aliases caller")
	}
}

// TestNetHTTPGo122RegistrationsProduceExactOperations verifies aliases, constants, exact handlers, and legacy rejection.
func TestNetHTTPGo122RegistrationsProduceExactOperations(t *testing.T) {
	input := sealedInput(t, map[string][]byte{"app/main.go": []byte(`package app
import h "net/http"
const route = "/users/{id}"
func user(w h.ResponseWriter, r *h.Request){ _ = r.PathValue("id"); w.WriteHeader(h.StatusNoContent) }
func routes(){ h.HandleFunc("GET "+route, user); h.HandleFunc("/legacy", user) }
`)}, nil)
	result, err := (Inspector{}).Inspect(context.Background(), input, "source")
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Snapshot.Operations) != 1 {
		t.Fatalf("operations=%d diagnostics=%v", len(result.Snapshot.Operations), result.Diagnostics)
	}
	op := result.Snapshot.Operations[0]
	if op.Key.Method != "GET" || op.Key.Path != "/users/{id}" || op.LogicalID != "user" {
		t.Fatalf("unexpected identity: %+v", op)
	}
	if len(op.Parameters) != 1 || !op.Parameters[0].Required || op.Responses["204"].Content != nil {
		t.Fatalf("facts missing: %+v", op)
	}
	if len(result.Diagnostics) != 1 || result.Diagnostics[0].Code != DiagnosticDynamicRoute {
		t.Fatalf("legacy route diagnostic missing: %+v", result.Diagnostics)
	}
}

// TestChiComposesRoutesGroupsBuildersAndMiddleware verifies nested prefixes, fluent middleware, body schemas, auth, and impact.
func TestChiComposesRoutesGroupsBuildersAndMiddleware(t *testing.T) {
	files := map[string][]byte{
		"app/router.go": []byte(`package app
import ("github.com/go-chi/chi/v5"; auth "example/auth")
const prefix="/api"
func routes(r chi.Router){ r.Use(auth.Require); r.Route(prefix, func(r chi.Router){ r.With(auth.Require).Post("/users/{id}", create) }) }
`),
		"app/handler.go": []byte(`package app
import ("encoding/json"; "net/http"; "github.com/go-chi/chi/v5")
type Address struct { City string ` + "`json:\"city\"`" + ` }
type Create struct { Name string ` + "`json:\"name\"`" + `; Nick *string ` + "`json:\"nick,omitempty\"`" + `; Hidden string ` + "`json:\"-\"`" + `; addr string }
func create(w http.ResponseWriter,r *http.Request){ var body Create; _=json.NewDecoder(r.Body).Decode(&body); _=chi.URLParam(r,"id"); _=r.URL.Query().Get("expand"); w.WriteHeader(http.StatusCreated) }
`),
	}
	input := sealedInput(t, files, func(config *Configuration) {
		config.ChangedPaths = []string{"app/handler.go"}
		config.Auth = []AuthMatcher{{Identity: AuthIdentity{Kind: AuthIdentityExternal, ImportPath: "example/auth", Alias: "auth", Symbol: "Require"}, Scheme: contractcatalog.SecurityScheme{Name: "session", Type: "http", Scheme: "bearer"}}}
	})
	result, err := (Inspector{}).Inspect(context.Background(), input, "source")
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Snapshot.Operations) != 1 {
		t.Fatalf("operations=%d diagnostics=%v", len(result.Snapshot.Operations), result.Diagnostics)
	}
	op := result.Snapshot.Operations[0]
	if op.Key.Path != "/api/users/{id}" || op.Key.Method != "POST" {
		t.Fatalf("bad route: %+v", op.Key)
	}
	if len(op.Parameters) != 2 || op.RequestBody == nil || op.RequestBody.Content["application/json"].Schema.Properties["name"] == nil {
		t.Fatalf("handler facts missing: %+v", op)
	}
	if _, ok := op.RequestBody.Content["application/json"].Schema.Properties["Hidden"]; ok {
		t.Fatal("ignored field projected")
	}
	if len(op.Security) != 1 || len(result.AffectedOperations) != 1 {
		t.Fatalf("auth or impact missing: %+v %+v", op.Security, result.AffectedOperations)
	}
}

// TestDynamicPatternsAndUnrelatedMethodsDoNotInventClaims proves closed-pattern matching against adversarial same-named calls.
func TestDynamicPatternsAndUnrelatedMethodsDoNotInventClaims(t *testing.T) {
	input := sealedInput(t, map[string][]byte{"app/main.go": []byte(`package app
import "net/http"
func h(w http.ResponseWriter,r *http.Request){ var fake struct{ URL struct{} }; _=fake; _=r.URL.Query().Get(variable); w.WriteHeader(code) }
var variable string; var code int
func routes(){ p:="GET /x"; http.HandleFunc(p,h); http.HandleFunc("/x",h) }
`)}, nil)
	_, err := (Inspector{}).Inspect(context.Background(), input, "source")
	var inspection *Error
	if !errors.As(err, &inspection) || inspection.Category != CategoryInsufficientContext {
		t.Fatalf("expected insufficient context, got %v", err)
	}
}

// TestPointersTagsCyclesAndExternalTypesStayConservative verifies atomic schema omission and partial completeness.
func TestPointersTagsCyclesAndExternalTypesStayConservative(t *testing.T) {
	input := sealedInput(t, map[string][]byte{"app/main.go": []byte(`package app
import ("encoding/json";"net/http")
type Node struct { Name string ` + "`json:\"name\"`" + `; Next *Node ` + "`json:\"next,omitempty\"`" + `; Any interface{} ` + "`json:\"any\"`" + ` }
func h(w http.ResponseWriter,r *http.Request){var body Node;_=json.NewDecoder(r.Body).Decode(&body);w.Write([]byte("ok"))}
func routes(){http.HandleFunc("POST /nodes",h)}
`)}, nil)
	result, err := (Inspector{}).Inspect(context.Background(), input, "source")
	if err != nil {
		t.Fatal(err)
	}
	schema := result.Snapshot.Operations[0].RequestBody.Content["application/json"].Schema
	if schema.Properties["name"] == nil || schema.Properties["next"] != nil || schema.Properties["any"] != nil {
		t.Fatalf("schema fabricated or lost: %+v", schema)
	}
}

// TestIndependentRoutesSurviveSyntaxAndTypeErrors verifies safe independent facts plus bounded diagnostics.
func TestIndependentRoutesSurviveSyntaxAndTypeErrors(t *testing.T) {
	input := sealedInput(t, map[string][]byte{"app/good.go": []byte(`package app
import "net/http"
func ok(w http.ResponseWriter,r *http.Request){w.WriteHeader(200)}
func routes(){http.HandleFunc("GET /ok",ok)}
`), "app/bad.go": []byte("package app\nfunc broken(")}, func(config *Configuration) { config.Limits.MaxDiagnostics = 1 })
	result, err := (Inspector{}).Inspect(context.Background(), input, "source")
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Snapshot.Operations) != 1 || len(result.Diagnostics) != 1 {
		t.Fatalf("partial result lost: %+v", result)
	}
}

// TestLimitsCancellationBuildSelectionAndDeterminism covers typed budgets, context causes, build tags, and canonical order.
func TestLimitsCancellationBuildSelectionAndDeterminism(t *testing.T) {
	files := map[string][]byte{"app/linux.go": []byte("//go:build linux\npackage app\nimport \"net/http\"\nfunc h(w http.ResponseWriter,r *http.Request){w.WriteHeader(200)}\nfunc routes(){http.HandleFunc(\"GET /ok\",h)}"), "app/windows.go": []byte("//go:build windows\npackage app\nfunc nope(){")}
	input := sealedInput(t, files, nil)
	first, err := (Inspector{}).Inspect(context.Background(), input, "source")
	if err != nil {
		t.Fatal(err)
	}
	second, err := (Inspector{}).Inspect(context.Background(), input, "source")
	if err != nil || !reflect.DeepEqual(first, second) {
		t.Fatal("inspection is not deterministic")
	}
	cancelledContext, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = (Inspector{}).Inspect(cancelledContext, input, "source")
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation cause lost: %v", err)
	}
	limited := sealedInput(t, files, func(config *Configuration) { config.Limits.MaxFiles = 1 })
	_, err = (Inspector{}).Inspect(context.Background(), limited, "source")
	var safe *Error
	if !errors.As(err, &safe) || safe.Category != CategoryLimitExceeded {
		t.Fatalf("limit not typed: %v", err)
	}
}

// TestGoInspectorLoadsAsDerivedContractSource verifies exact metadata binding through registry and neutral catalog construction.
func TestGoInspectorLoadsAsDerivedContractSource(t *testing.T) {
	input := sealedInput(t, map[string][]byte{"app/main.go": []byte(`package app
import "net/http"
func h(w http.ResponseWriter,r *http.Request){w.WriteHeader(200)}
func routes(){http.HandleFunc("GET /health",h);http.HandleFunc("/legacy",h)}
`)}, func(config *Configuration) { config.ChangedPaths = []string{"app/main.go"} })
	limits := contractcatalog.Limits{MaxSources: 1, MaxOperations: 10, MaxClaims: 100, MaxBytes: 1 << 20, MaxDepth: 30, MaxDiagnostics: 20, MaxLocationBytes: 512}
	projection := input.config.Projection
	registry, err := contractcatalog.NewRegistry(map[string]contractcatalog.ContractSource{SourceKind: DerivedSource{}})
	if err != nil {
		t.Fatal(err)
	}
	loaded, err := registry.Load(context.Background(), []contractcatalog.SourceRequest{SourceRequest("derived", input, limits, projection)}, limits)
	if err != nil {
		t.Fatal(err)
	}
	catalog, err := contractcatalog.Build(context.Background(), loaded, limits)
	if err != nil {
		t.Fatal(err)
	}
	if catalog.Sources()[0].Class != contractcatalog.ClassDerived || catalog.Operations()[0].Key.Path != "/health" {
		t.Fatalf("bad derived catalog: %+v", catalog)
	}
	if len(catalog.Diagnostics()) != 1 || len(catalog.AffectedOperations()) != 1 {
		t.Fatalf("diagnostics or PR impact lost: %+v %+v", catalog.Diagnostics(), catalog.AffectedOperations())
	}
	bad := contractcatalog.NewSourceRequest(contractcatalog.SourceDescriptor{Kind: SourceKind, ID: "derived", Class: contractcatalog.ClassAuthoritative, Confidence: contractcatalog.ConfidenceMedium, Completeness: contractcatalog.CompletenessPartial, ExpectedDigest: input.Digest(), AdapterVersion: AdapterVersion, Input: input, Limits: limits, Projection: projection})
	if _, err = registry.Load(context.Background(), []contractcatalog.SourceRequest{bad}, limits); err == nil {
		t.Fatal("source elevated its authority")
	}
}

// TestFreezeRepositoryRejectsEscapesAndReadsOnlyAuthorizedRoots covers local snapshot rooting without invoking repository code.
func TestFreezeRepositoryRejectsEscapesAndReadsOnlyAuthorizedRoots(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "app"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "app", "main.go"), []byte("package app\n//go:generate powershell evil.ps1\nfunc init(){ panic(\"must never execute\") }"), 0600); err != nil {
		t.Fatal(err)
	}
	snapshot, err := freezeRepository(context.Background(), root, "repo", testRevision, []string{"app"}, testLimits())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(snapshot.files["app/main.go"]), "go:generate") {
		t.Fatal("fixture not read")
	}
	if _, err = freezeRepository(context.Background(), root, "repo", testRevision, []string{"../"}, testLimits()); err == nil {
		t.Fatal("root escape accepted")
	}
	if runtime.GOOS != "windows" {
		outside := t.TempDir()
		link := filepath.Join(root, "app", "outside")
		if err = os.Symlink(outside, link); err == nil {
			if _, err = freezeRepository(context.Background(), root, "repo", testRevision, []string{"app"}, testLimits()); err == nil {
				t.Fatal("symlink accepted")
			}
		}
	}
}

// TestErrorsDiagnosticsEventsAndCatalogExcludeCanaries proves secrets in syntax text never cross safe surfaces.
func TestErrorsDiagnosticsEventsAndCatalogExcludeCanaries(t *testing.T) {
	canary := "authorization-super-secret"
	input := sealedInput(t, map[string][]byte{"app/good.go": []byte(`package app
import "net/http"
func h(w http.ResponseWriter,r *http.Request){w.WriteHeader(200)}
func routes(){http.HandleFunc("GET /safe",h)}
`), "app/bad.go": []byte("package app\nfunc " + canary + "(")}, nil)
	result, err := (Inspector{}).Inspect(context.Background(), input, "source")
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(result)
	if strings.Contains(string(raw), canary) {
		t.Fatal("canary leaked")
	}
}

// TestSchemaMatrixAndChiMethod exercises the supported scalar, collection, group, and constant-method projections.
func TestSchemaMatrixAndChiMethod(t *testing.T) {
	input := sealedInput(t, map[string][]byte{"app/main.go": []byte(`package app
import ("encoding/json";"net/http";"github.com/go-chi/chi/v5")
type Label string
type Body struct { Enabled bool ` + "`json:\"enabled\"`" + `; Count int64 ` + "`json:\"count\"`" + `; Ratio float64 ` + "`json:\"ratio\"`" + `; Labels []Label ` + "`json:\"labels\"`" + `; Lookup map[string]int ` + "`json:\"lookup\"`" + ` }
func h(w http.ResponseWriter,r *http.Request){var body Body;_=json.NewDecoder(r.Body).Decode(&body);http.Error(w,"bad",http.StatusBadRequest)}
func routes(r chi.Router){r.Group(func(r chi.Router){r.Method(http.MethodPut,"/matrix",h)})}
`)}, nil)
	result, err := (Inspector{}).Inspect(context.Background(), input, "source")
	if err != nil {
		t.Fatal(err)
	}
	op := result.Snapshot.Operations[0]
	schema := op.RequestBody.Content["application/json"].Schema
	if op.Key.Method != "PUT" || op.Key.Path != "/matrix" || schema.Properties["labels"].Items.Type != "string" || schema.Properties["lookup"].Type != "object" {
		t.Fatalf("matrix not projected: %+v", op)
	}
}

// TestDerivedSourceMapsFailures verifies adapter metadata, cancellation, insufficient context, and limit categories.
func TestDerivedSourceMapsFailures(t *testing.T) {
	valid := sealedInput(t, map[string][]byte{"app/main.go": []byte("package app\nfunc noRoutes(){}")}, nil)
	source := DerivedSource{}
	request := SourceRequest("derived", valid, contractcatalog.Limits{MaxSources: 1, MaxOperations: 10, MaxClaims: 100, MaxBytes: 1 << 20, MaxDepth: 20, MaxDiagnostics: 20, MaxLocationBytes: 512}, valid.config.Projection)
	_, err := source.Load(context.Background(), request)
	var catalogError *contractcatalog.Error
	if !errors.As(err, &catalogError) || catalogError.Category != contractcatalog.CategoryInsufficient {
		t.Fatalf("insufficient mapping failed: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = source.Load(ctx, request)
	if !errors.As(err, &catalogError) || catalogError.Category != contractcatalog.CategoryCancelled {
		t.Fatalf("cancel mapping failed: %v", err)
	}
	wrong := contractcatalog.NewSourceRequest(contractcatalog.SourceDescriptor{Kind: "wrong", ID: "derived", Class: contractcatalog.ClassDerived, Confidence: contractcatalog.ConfidenceMedium, Completeness: contractcatalog.CompletenessPartial, ExpectedDigest: valid.Digest(), AdapterVersion: AdapterVersion, Input: valid})
	_, err = source.Load(context.Background(), wrong)
	if !errors.As(err, &catalogError) || catalogError.Category != contractcatalog.CategoryInvalidSource {
		t.Fatalf("metadata mapping failed: %v", err)
	}
}

// TestValidationHelpersRejectMalformedAuthority covers closed enums, symbols, revisions, and canonical path forms.
func TestValidationHelpersRejectMalformedAuthority(t *testing.T) {
	if _, ok := safeRelativePath(`C:\repo\file.go`); ok {
		t.Fatal("absolute Windows path accepted")
	}
	if _, ok := safeRelativePath("/file.go"); ok {
		t.Fatal("absolute POSIX path accepted")
	}
	if validRevision(strings.Repeat("z", 40)) || safeSymbol("auth.Require()") || safeToken("repo/path", 20) {
		t.Fatal("malformed authority accepted")
	}
	if got := boundedSymbol(strings.Repeat("x", 129)); got != "symbol" {
		t.Fatal("long symbol was not bounded")
	}
	if (&Error{Category: CategoryInvalidInput, Stage: "input"}).Error() == "" {
		t.Fatal("safe error is empty")
	}
}

// TestDiagnosticCapCountsOmissions proves excess diagnostics retain only a count and no hidden payload.
func TestDiagnosticCapCountsOmissions(t *testing.T) {
	input := sealedInput(t, map[string][]byte{"app/good.go": []byte(`package app
import "net/http"
func h(w http.ResponseWriter,r *http.Request){w.WriteHeader(200)}
func routes(){http.HandleFunc("GET /ok",h);http.HandleFunc("/legacy",h)}
`), "app/a.go": []byte("package app\nfunc a("), "app/b.go": []byte("package app\nfunc b(")}, func(config *Configuration) { config.Limits.MaxDiagnostics = 1 })
	result, err := (Inspector{}).Inspect(context.Background(), input, "source")
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Diagnostics) != 1 || result.OmittedDiagnostics < 1 {
		t.Fatalf("diagnostic cap missing: %+v", result)
	}
}

// TestFreezeRepositoryEnforcesBudgetsAndCancellation covers filesystem failure categories before a snapshot is exposed.
func TestFreezeRepositoryEnforcesBudgetsAndCancellation(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "app"), 0700); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"a.go", "b.go"} {
		if err := os.WriteFile(filepath.Join(root, "app", name), []byte("package app"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	limits := testLimits()
	limits.MaxFiles = 1
	_, err := freezeRepository(context.Background(), root, "repo", testRevision, []string{"app"}, limits)
	var inspection *Error
	if !errors.As(err, &inspection) || inspection.Category != CategoryLimitExceeded {
		t.Fatalf("file limit missing: %v", err)
	}
	limits = testLimits()
	limits.MaxBytes = 1
	_, err = freezeRepository(context.Background(), root, "repo", testRevision, []string{"app"}, limits)
	if !errors.As(err, &inspection) || inspection.Category != CategoryLimitExceeded {
		t.Fatalf("byte limit missing: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = freezeRepository(ctx, root, "repo", testRevision, []string{"app"}, testLimits())
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("filesystem cancellation lost: %v", err)
	}
}

// TestInputRejectsUnboundedAndMismatchedConfiguration proves every trusted dimension is explicit and snapshot-bound.
func TestInputRejectsUnboundedAndMismatchedConfiguration(t *testing.T) {
	snapshot, err := NewSnapshot("repo", testRevision, map[string][]byte{"app/main.go": []byte("package app")})
	if err != nil {
		t.Fatal(err)
	}
	config := testConfig()
	config.RepositoryID = "other"
	if _, err = newInput(snapshot, config, time.Time{}); err == nil {
		t.Fatal("repository mismatch accepted")
	}
	config = testConfig()
	config.Limits.MaxTypes = 0
	if _, err = newInput(snapshot, config, time.Time{}); err == nil {
		t.Fatal("unbounded type traversal accepted")
	}
	config = testConfig()
	config.Build.GOOS = "linux/path"
	if _, err = newInput(snapshot, config, time.Time{}); err == nil {
		t.Fatal("invalid build context accepted")
	}
	config = testConfig()
	config.Confidence = "certain"
	if _, err = newInput(snapshot, config, time.Time{}); err == nil {
		t.Fatal("unknown confidence accepted")
	}
	config = testConfig()
	config.Auth = []AuthMatcher{{Identity: AuthIdentity{Kind: AuthIdentityLocal, ImportPath: "app", Symbol: "bad()"}, Scheme: contractcatalog.SecurityScheme{Name: "session", Type: "http"}}}
	if _, err = newInput(snapshot, config, time.Time{}); err == nil {
		t.Fatal("invalid auth matcher accepted")
	}
}

// TestInputDigestCanonicalizesSetLikeTrustedConfiguration proves caller ordering cannot alter authority identity.
func TestInputDigestCanonicalizesSetLikeTrustedConfiguration(t *testing.T) {
	snapshot, err := NewSnapshot("repo", testRevision, map[string][]byte{"app/main.go": []byte("package app")})
	if err != nil {
		t.Fatal(err)
	}
	a := testConfig()
	a.Build.Tags = []string{"integration", "linux"}
	a.Auth = []AuthMatcher{{Identity: AuthIdentity{Kind: AuthIdentityLocal, ImportPath: "z", Symbol: "Require"}, Scheme: contractcatalog.SecurityScheme{Name: "z", Type: "http"}}, {Identity: AuthIdentity{Kind: AuthIdentityLocal, ImportPath: "a", Symbol: "Require"}, Scheme: contractcatalog.SecurityScheme{Name: "a", Type: "http"}}}
	b := a
	b.Build.Tags = []string{"linux", "integration"}
	b.Auth = []AuthMatcher{a.Auth[1], a.Auth[0]}
	first, err := newInput(snapshot, a, time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	second, err := newInput(snapshot, b, time.Time{})
	if err != nil || first.Digest() != second.Digest() {
		t.Fatal("trusted set ordering changed digest")
	}
	a.Auth[0].Scheme.Flows = map[string]contractcatalog.OAuthFlow{"client": {Scopes: map[string]string{"read": "allowed"}}}
	sealed, err := newInput(snapshot, a, time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	a.Auth[0].Scheme.Flows["client"] = contractcatalog.OAuthFlow{Scopes: map[string]string{"write": "mutated"}}
	for _, matcher := range sealed.config.Auth {
		if matcher.Scheme.Name == "z" && matcher.Scheme.Flows["client"].Scopes["write"] != "" {
			t.Fatal("auth matcher authority aliases caller")
		}
	}
}

// TestServeMuxAndStaticChiBuilderResolveWithoutNameHeuristics verifies exact constructors and unchanged local builder closure.
func TestServeMuxAndStaticChiBuilderResolveWithoutNameHeuristics(t *testing.T) {
	input := sealedInput(t, map[string][]byte{"app/main.go": []byte(`package app
import (h "net/http"; c "github.com/go-chi/chi/v5")
func handler(w h.ResponseWriter,r *h.Request){w.WriteHeader(200)}
func add(r c.Router){r.Get("/built",handler)}
func routes(r c.Router){mux:=h.NewServeMux();mux.HandleFunc("GET /mux",handler);add(r)}
`)}, func(config *Configuration) { config.ChangedPaths = []string{"app/main.go"} })
	result, err := (Inspector{}).Inspect(context.Background(), input, "source")
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Snapshot.Operations) != 2 {
		t.Fatalf("static closure not resolved: %+v", result)
	}
}

// TestClosedScalarMethodAndRouteMatrix verifies every allowlisted scalar and method plus route composition edge cases.
func TestClosedScalarMethodAndRouteMatrix(t *testing.T) {
	for _, method := range []string{"GET", "POST", "PUT", "PATCH", "DELETE", "HEAD", "OPTIONS", "CONNECT", "TRACE"} {
		if !validHTTPMethod(method) {
			t.Fatalf("method %s rejected", method)
		}
	}
	if validHTTPMethod("BREW") {
		t.Fatal("unknown method accepted")
	}
	for _, name := range []string{"string", "bool", "int", "int8", "int16", "int32", "int64", "uint", "uint8", "uint16", "uint32", "uint64", "float32", "float64", "byte"} {
		if schema, ok := primitiveSchema(name); !ok || schema == nil {
			t.Fatalf("scalar %s rejected", name)
		}
	}
	if _, ok := primitiveSchema("complex64"); ok {
		t.Fatal("unsupported scalar accepted")
	}
	cases := map[string]string{"|": "/", "|users": "/users", "/api|/": "/api", "/api|users": "/api/users"}
	for encoded, want := range cases {
		parts := strings.Split(encoded, "|")
		if got := joinRoute(parts[0], parts[1]); got != want {
			t.Fatalf("join %q=%q want %q", encoded, got, want)
		}
	}
}

// TestTerminalQueryGuardAndExplicitJSONResponseDeriveOnlyProvenFacts verifies required guards and media-gated response schemas.
func TestTerminalQueryGuardAndExplicitJSONResponseDeriveOnlyProvenFacts(t *testing.T) {
	input := sealedInput(t, map[string][]byte{"app/main.go": []byte(`package app
import ("encoding/json";"net/http")
type Reply struct { OK bool ` + "`json:\"ok\"`" + ` }
func h(w http.ResponseWriter,r *http.Request){q:=r.URL.Query().Get("q");if q==""{http.Error(w,"missing",400);return};w.Header().Set("Content-Type","application/json");var reply Reply;_ = json.NewEncoder(w).Encode(reply)}
func routes(){http.HandleFunc("GET /search",h)}
`)}, nil)
	result, err := (Inspector{}).Inspect(context.Background(), input, "source")
	if err != nil {
		t.Fatal(err)
	}
	op := result.Snapshot.Operations[0]
	if len(op.Parameters) != 1 || !op.Parameters[0].Required {
		t.Fatalf("terminal guard not derived: %+v", op.Parameters)
	}
	if op.Responses["200"].Content["application/json"].Schema.Properties["ok"] == nil {
		t.Fatalf("JSON response schema missing: %+v", op.Responses)
	}
	if len(op.Responses["400"].Content) != 0 {
		t.Fatalf("error branch inherited success schema: %+v", op.Responses["400"])
	}
}

// TestGeneratedFilesRequireExplicitTrustedPolicy proves repository directives never cause implicit generated-code ingestion.
func TestGeneratedFilesRequireExplicitTrustedPolicy(t *testing.T) {
	files := map[string][]byte{"app/generated.go": []byte("// Code generated by tool. DO NOT EDIT.\npackage app\nimport \"net/http\"\nfunc h(w http.ResponseWriter,r *http.Request){w.WriteHeader(200)}\nfunc routes(){http.HandleFunc(\"GET /generated\",h)}")}
	input := sealedInput(t, files, nil)
	if _, err := (Inspector{}).Inspect(context.Background(), input, "source"); err == nil {
		t.Fatal("generated code admitted without policy")
	}
	allowed := sealedInput(t, files, func(config *Configuration) { config.IncludeGenerated = true })
	result, err := (Inspector{}).Inspect(context.Background(), allowed, "source")
	if err != nil || len(result.Snapshot.Operations) != 1 {
		t.Fatalf("explicit generated policy failed: %v %+v", err, result)
	}
}

// TestModuleLocalImportedHandlerClosureUsesGoModWithoutHostResolution verifies cross-package handlers from sealed module bytes.
func TestModuleLocalImportedHandlerClosureUsesGoModWithoutHostResolution(t *testing.T) {
	files := map[string][]byte{"go.mod": []byte("module example.test/service\n"), "app/routes.go": []byte("package app\nimport (\"net/http\"; u \"example.test/service/users\")\nfunc routes(){http.HandleFunc(\"GET /users\",u.List)}"), "users/handler.go": []byte("package users\nimport \"net/http\"\nfunc List(w http.ResponseWriter,r *http.Request){w.WriteHeader(200)}")}
	input := sealedInput(t, files, func(config *Configuration) {
		config.Roots = []string{"app", "users"}
		config.ChangedPaths = []string{"users/handler.go"}
	})
	result, err := (Inspector{}).Inspect(context.Background(), input, "source")
	if err != nil {
		t.Fatal(err)
	}
	op := result.Snapshot.Operations[0]
	if op.LogicalID != "users.List" || op.Responses["200"].Content != nil || len(result.AffectedOperations) != 1 {
		t.Fatalf("local closure unresolved: %+v %+v", op, result.AffectedOperations)
	}
}

// TestQueryIndexReversedGuardAndServeMuxParameterCoverClosedVariants verifies remaining supported parameter and mux forms.
func TestQueryIndexReversedGuardAndServeMuxParameterCoverClosedVariants(t *testing.T) {
	input := sealedInput(t, map[string][]byte{"app/main.go": []byte(`package app
import "net/http"
func h(w http.ResponseWriter,r *http.Request){raw:=r.URL.Query()["raw"];_ = raw;q:=r.URL.Query().Has("q");_ = q;value:=r.URL.Query().Get("required");if ""==value{w.WriteHeader(400);return};w.WriteHeader(200)}
func routes(mux *http.ServeMux){mux.HandleFunc("GET /query",h)}
`)}, nil)
	result, err := (Inspector{}).Inspect(context.Background(), input, "source")
	if err != nil {
		t.Fatal(err)
	}
	op := result.Snapshot.Operations[0]
	if len(op.Parameters) != 3 {
		t.Fatalf("query variants missing: %+v", op.Parameters)
	}
	required := false
	for _, parameter := range op.Parameters {
		if parameter.Name == "required" {
			required = parameter.Required
		}
	}
	if !required {
		t.Fatal("reversed terminal guard not recognized")
	}
}

// TestTypeBudgetFailsClosed proves schema-node budgets cannot be raised by source content.
func TestTypeBudgetFailsClosed(t *testing.T) {
	files := map[string][]byte{"app/main.go": []byte(`package app
import("encoding/json";"net/http")
type Child struct{Name string};type Body struct{Child Child}
func h(w http.ResponseWriter,r *http.Request){var body Body;_=json.NewDecoder(r.Body).Decode(&body);w.WriteHeader(200)}
func routes(){http.HandleFunc("POST /body",h)}
`)}
	limited := sealedInput(t, files, func(config *Configuration) { config.Limits.MaxTypes = 1 })
	result, err := (Inspector{}).Inspect(context.Background(), limited, "source")
	if err != nil {
		t.Fatal(err)
	}
	if result.Snapshot.Operations[0].RequestBody == nil || len(result.Diagnostics) == 0 {
		t.Fatal("type budget did not preserve partial parent safely")
	}
}

// TestChiMountResolvesOnlyFullyKnownChildRouter verifies mounted explicit methods and unresolved mounts remain conservative.
func TestChiMountResolvesOnlyFullyKnownChildRouter(t *testing.T) {
	input := sealedInput(t, map[string][]byte{"app/main.go": []byte(`package app
import("net/http";"github.com/go-chi/chi/v5")
func h(w http.ResponseWriter,r *http.Request){w.WriteHeader(200)}
func routes(r chi.Router){sub:=chi.NewRouter();sub.Get("/child",h);r.Mount("/api",sub);r.Mount("/dynamic",unknown)}
var unknown chi.Router
`)}, nil)
	result, err := (Inspector{}).Inspect(context.Background(), input, "source")
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Snapshot.Operations) != 1 || result.Snapshot.Operations[0].Key.Path != "/api/child" || len(result.Diagnostics) != 1 {
		t.Fatalf("mount resolution unsafe: %+v", result)
	}
}

// TestChiMountAcceptsStaticRouterReturningBuilder verifies the supported same-router return builder form.
func TestChiMountAcceptsStaticRouterReturningBuilder(t *testing.T) {
	input := sealedInput(t, map[string][]byte{"app/main.go": []byte(`package app
import("net/http";"github.com/go-chi/chi/v5")
func h(w http.ResponseWriter,r *http.Request){w.WriteHeader(200)}
func routes(root chi.Router){root.Mount("/v1",child())}
func child() chi.Router{r:=chi.NewRouter();r.Get("/child",h);return r}
`)}, nil)
	result, err := (Inspector{}).Inspect(context.Background(), input, "source")
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Snapshot.Operations) != 1 || result.Snapshot.Operations[0].Key.Path != "/v1/child" {
		t.Fatalf("router builder unresolved: %+v", result)
	}
}
