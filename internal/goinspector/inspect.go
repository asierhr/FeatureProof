package goinspector

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"go/ast"
	"go/build/constraint"
	"go/parser"
	"go/token"
	"path"
	"sort"
	"strconv"
	"strings"

	"github.com/asierhr/featureProof/internal/contractcatalog"
)

// Inspector owns one sequential analysis and exposes an internal phase barrier only to deterministic package tests.
type Inspector struct{ phaseBarrier func(context.Context, string) }

// parsedFile retains only local AST state for one immutable repository file.
type parsedFile struct {
	name, pkg string
	file      *ast.File
	imports   map[string]string
	implicit  map[string]string
}

// analysisState owns all per-call budgets, indexes, diagnostics, and derived operations.
type analysisState struct {
	ctx             context.Context
	input           Input
	fset            *token.FileSet
	files           []parsedFile
	constants       map[string]ast.Expr
	objectConstants map[*ast.Object]ast.Expr
	functions       map[string]*ast.FuncDecl
	types           map[string]ast.Expr
	customJSON      map[string]bool
	modulePath      string
	packages        map[string]string
	globalMuxes     map[string]bool
	muxFields       map[string]bool
	diagnostics     []Diagnostic
	omitted         int
	nodes           int
	typesVisited    int
	operations      []operationFact
	mounts          []mountFact
	failure         error
}

// operationFact carries exact source identity and inherited middleware until neutral projection.
type operationFact struct {
	method, path, handler, framework, file, pkg string
	line                                        int
	middleware                                  []middlewareFact
	partial                                     bool
	router                                      string
	dependencies                                []string
	requestSchemaLocations                      []string
	responseSchemaLocations                     []string
	requestSchemaClaims                         map[string][]string
	responseSchemaByStatus                      map[int][]string
	responseSchemaClaims                        map[int]map[string][]string
	parameterLocations                          map[string][]string
	requestFactLocations                        []string
	responseFactLocations                       map[int][]string
	securityLocations                           map[string][]string
}

// middlewareFact binds one syntactic application to its collision-free trusted identity and optional local declaration lookup.
type middlewareFact struct {
	identity                    AuthIdentity
	syntax, lookup, application string
}

// mountFact defers static router composition so Go declaration order cannot change derived routes.
type mountFact struct {
	prefix, child, parent string
	diagnostic            Diagnostic
	applied               bool
}

// Inspect parses only sealed bytes, derives supported operations, and returns a deterministic neutral snapshot.
func (inspector Inspector) Inspect(ctx context.Context, input Input, sourceID string) (Result, error) {
	if input.digest == "" || !safeToken(sourceID, 128) {
		return Result{}, typedError(CategoryInvalidInput, "inspect", nil)
	}
	bounded, cancel := inspectionContext(ctx, input.config.Limits.MaxDuration)
	if !input.deadlineAt.IsZero() {
		cancel()
		bounded, cancel = context.WithDeadline(ctx, input.deadlineAt)
	}
	defer cancel()
	if err := inspector.crossPhase(bounded, "index"); err != nil {
		return Result{}, err
	}
	state := &analysisState{ctx: bounded, input: input, fset: token.NewFileSet(), constants: map[string]ast.Expr{}, objectConstants: map[*ast.Object]ast.Expr{}, functions: map[string]*ast.FuncDecl{}, types: map[string]ast.Expr{}, customJSON: map[string]bool{}, packages: map[string]string{}, globalMuxes: map[string]bool{}, muxFields: map[string]bool{}}
	if err := state.index(); err != nil {
		return Result{}, err
	}
	if err := inspector.crossPhase(bounded, "routes"); err != nil {
		return Result{}, err
	}
	if err := state.extractRoutes(); err != nil {
		return Result{}, err
	}
	if len(state.operations) == 0 {
		return Result{}, typedError(CategoryInsufficientContext, "routes", nil)
	}
	if err := inspector.crossPhase(bounded, "projection"); err != nil {
		return Result{}, err
	}
	result, err := state.project(sourceID)
	if err != nil {
		return Result{}, err
	}
	if err := bounded.Err(); err != nil {
		return Result{}, cancelled("projection", err)
	}
	if err := contractcatalog.ValidateSourceProjection(result.Snapshot, input.config.Projection); err != nil {
		var projectionError *contractcatalog.Error
		if errors.As(err, &projectionError) {
			return Result{}, typedError(CategoryInvalidInput, "projection."+projectionError.Location, nil)
		}
		return Result{}, typedError(CategoryInvalidInput, "projection", nil)
	}
	if !state.resultDiagnosticsSafe(result.Diagnostics) {
		return Result{}, typedError(CategoryInvalidInput, "projection.diagnostic", nil)
	}
	if err := bounded.Err(); err != nil {
		return Result{}, cancelled("projection", err)
	}
	return result, nil
}

// crossPhase lets tests exhaust the same sealed context at exact stage boundaries and otherwise adds no behavior.
func (inspector Inspector) crossPhase(ctx context.Context, stage string) error {
	if inspector.phaseBarrier != nil {
		inspector.phaseBarrier(ctx, stage)
	}
	if err := ctx.Err(); err != nil {
		return cancelled(stage, err)
	}
	return nil
}

// resultDiagnosticsSafe ensures direct inspector results cannot expose policy-sensitive raw diagnostic fields.
func (s *analysisState) resultDiagnosticsSafe(values []Diagnostic) bool {
	for _, value := range values {
		if s.safePublic(value.Path, "path") != value.Path || s.safePublic(value.Symbol, "symbol") != value.Symbol || s.safePublic(value.Framework, "framework") != value.Framework {
			return false
		}
	}
	return true
}

// index selects authorized Go files, parses independently, and builds bounded local declaration maps.
func (s *analysisState) index() error {
	s.modulePath = parseModulePath(s.input.snapshot.files["go.mod"])
	names := make([]string, 0, len(s.input.snapshot.files))
	for name := range s.input.snapshot.files {
		if withinRoots(name, s.input.config.Roots) && strings.HasSuffix(name, ".go") && !strings.HasSuffix(name, "_test.go") {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	if len(names) > s.input.config.Limits.MaxFiles {
		return typedError(CategoryLimitExceeded, "files", nil)
	}
	packages := map[string]struct{}{}
	bytes := 0
	for _, name := range names {
		if err := s.ctx.Err(); err != nil {
			return cancelled("parse", err)
		}
		content := s.input.snapshot.files[name]
		if !s.input.config.IncludeGenerated && generatedFile(content) {
			s.omitted++
			continue
		}
		bytes += len(content)
		if bytes > s.input.config.Limits.MaxBytes {
			return typedError(CategoryLimitExceeded, "bytes", nil)
		}
		if !matchFilename(name, s.input.config.Build) || !matchBuild(content, s.input.config.Build) {
			continue
		}
		file, err := parser.ParseFile(s.fset, name, content, parser.ParseComments)
		if err != nil {
			s.addDiagnostic(Diagnostic{Code: DiagnosticSyntax, Path: name})
			continue
		}
		packages[path.Dir(name)+"/"+file.Name.Name] = struct{}{}
		if len(packages) > s.input.config.Limits.MaxPackages {
			return typedError(CategoryLimitExceeded, "packages", nil)
		}
		imports := map[string]string{}
		implicit := map[string]string{}
		for _, spec := range file.Imports {
			importPath, _ := strconv.Unquote(spec.Path.Value)
			alias := path.Base(importPath)
			if importPath == "github.com/go-chi/chi/v5" {
				alias = "chi"
			}
			if spec.Name != nil {
				alias = spec.Name.Name
			} else {
				implicit[importPath] = alias
			}
			imports[alias] = importPath
		}
		packageID := path.Dir(name) + "#" + file.Name.Name
		parsed := parsedFile{name: name, pkg: packageID, file: file, imports: imports, implicit: implicit}
		importIdentity := s.modulePath
		if directory := path.Dir(name); directory != "." && directory != "" {
			importIdentity += "/" + directory
		}
		s.packages[importIdentity] = packageID
		s.files = append(s.files, parsed)
		ast.Inspect(file, func(node ast.Node) bool {
			declaration, ok := node.(*ast.GenDecl)
			if !ok || declaration.Tok != token.CONST {
				return true
			}
			for _, raw := range declaration.Specs {
				spec := raw.(*ast.ValueSpec)
				for index, ident := range spec.Names {
					if ident.Obj != nil && index < len(spec.Values) {
						s.objectConstants[ident.Obj] = spec.Values[index]
					}
				}
			}
			return true
		})
		ast.Inspect(file, func(node ast.Node) bool {
			if node != nil {
				s.nodes++
				if s.nodes > s.input.config.Limits.MaxASTNodes {
					return false
				}
			}
			return s.nodes <= s.input.config.Limits.MaxASTNodes
		})
		if s.nodes > s.input.config.Limits.MaxASTNodes {
			return typedError(CategoryLimitExceeded, "ast_nodes", nil)
		}
		for _, decl := range file.Decls {
			switch value := decl.(type) {
			case *ast.GenDecl:
				for _, spec := range value.Specs {
					switch item := spec.(type) {
					case *ast.ValueSpec:
						for index, ident := range item.Names {
							if serveMuxType(parsed, item.Type) || index < len(item.Values) && isNewServeMux(parsed, item.Values[index]) {
								s.globalMuxes[packageID+"."+ident.Name] = true
							}
						}
						if value.Tok == token.CONST {
							for index, ident := range item.Names {
								if index < len(item.Values) {
									s.constants[packageID+"."+ident.Name] = item.Values[index]
								}
							}
						}
					case *ast.TypeSpec:
						s.types[packageID+"."+item.Name.Name] = item.Type
						if structure, ok := item.Type.(*ast.StructType); ok {
							for _, field := range structure.Fields.List {
								if serveMuxType(parsed, field.Type) {
									for _, name := range field.Names {
										s.muxFields[packageID+"."+item.Name.Name+"."+name.Name] = true
									}
								}
							}
						}
					}
				}
			case *ast.FuncDecl:
				s.functions[packageID+"."+funcSymbol(value)] = value
				if (value.Name.Name == "MarshalJSON" || value.Name.Name == "UnmarshalJSON") && value.Recv != nil && len(value.Recv.List) == 1 {
					if receiver := receiverTypeName(value.Recv.List[0].Type); receiver != "" {
						s.customJSON[packageID+"."+receiver] = true
					}
				}
			}
		}
	}
	if len(s.files) == 0 {
		return typedError(CategoryInsufficientContext, "files", nil)
	}
	s.resolveImplicitImportNames()
	return nil
}

// receiverTypeName returns the exact local named receiver for value and pointer method declarations.
func receiverTypeName(expression ast.Expr) string {
	if pointer, ok := expression.(*ast.StarExpr); ok {
		expression = pointer.X
	}
	if identifier, ok := expression.(*ast.Ident); ok {
		return identifier.Name
	}
	return ""
}

// resolveImplicitImportNames binds local imports without aliases to the package name declared inside the sealed snapshot.
func (s *analysisState) resolveImplicitImportNames() {
	for index := range s.files {
		for importPath, fallback := range s.files[index].implicit {
			packageID := s.packages[importPath]
			marker := strings.LastIndex(packageID, "#")
			if marker < 0 || marker+1 >= len(packageID) {
				if importPath != "net/http" && importPath != "encoding/json" && importPath != "github.com/go-chi/chi/v5" {
					delete(s.files[index].imports, fallback)
					s.addDiagnostic(Diagnostic{Code: DiagnosticUnsupported, Path: s.safePublic(s.files[index].name, "path"), Symbol: s.safePublic(fallback, "symbol")})
				}
				continue
			}
			declared := packageID[marker+1:]
			delete(s.files[index].imports, fallback)
			s.files[index].imports[declared] = importPath
		}
	}
}

// generatedFile recognizes the standard generated-code marker without executing its generator.
func generatedFile(content []byte) bool {
	head := string(content)
	if len(head) > 2048 {
		head = head[:2048]
	}
	return strings.Contains(head, "Code generated") && strings.Contains(head, "DO NOT EDIT.")
}

// extractRoutes walks supported registration calls while keeping prefix and middleware scope local to each block.
func (s *analysisState) extractRoutes() error {
	for _, entry := range s.input.config.EntrySymbols {
		matches := []*ast.FuncDecl{}
		for key, declaration := range s.functions {
			if key == entry || strings.HasSuffix(key, "."+entry) {
				duplicate := false
				for _, prior := range matches {
					if prior == declaration {
						duplicate = true
					}
				}
				if !duplicate {
					matches = append(matches, declaration)
				}
			}
		}
		if len(matches) == 0 {
			return typedError(CategoryInsufficientContext, "entry_symbol", nil)
		}
		if len(matches) != 1 {
			return typedError(CategoryInvalidInput, "entry_symbol", nil)
		}
		function := matches[0]
		file := s.fileAt(function.Pos())
		if err := s.ctx.Err(); err != nil {
			return cancelled("routes", err)
		}
		s.walkBlock(file, function.Body.List, "", nil, map[string]bool{}, s.routeScope(file, function), 0, []string{file.name})
		if s.failure != nil {
			return s.failure
		}
		if err := s.ctx.Err(); err != nil {
			return cancelled("routes", err)
		}
	}
	s.resolveMounts()
	sort.Slice(s.operations, func(i, j int) bool {
		a, b := s.operations[i], s.operations[j]
		return a.method+"\x00"+a.path+"\x00"+a.handler < b.method+"\x00"+b.path+"\x00"+b.handler
	})
	if len(s.operations) > 1 {
		unique := s.operations[:1]
		for _, operation := range s.operations[1:] {
			prior := unique[len(unique)-1]
			if operation.method != prior.method || operation.path != prior.path || operation.handler != prior.handler {
				unique = append(unique, operation)
			}
		}
		s.operations = unique
	}
	if len(s.operations) > s.input.config.Limits.MaxOperations {
		return typedError(CategoryLimitExceeded, "operations", nil)
	}
	return nil
}

// walkBlock resolves the closed net/http and Chi v5 call patterns inside one statically ordered block.
func (s *analysisState) walkBlock(file parsedFile, statements []ast.Stmt, prefix string, inherited []middlewareFact, active map[string]bool, routers map[string]bool, depth int, dependencies []string) {
	if depth >= s.input.config.Limits.MaxDepth {
		s.addDiagnostic(Diagnostic{Code: DiagnosticControlFlow, Path: file.name})
		return
	}
	middleware := append([]middlewareFact(nil), inherited...)
	for _, statement := range statements {
		if s.ctx.Err() != nil || s.failure != nil {
			return
		}
		expression, ok := statement.(*ast.ExprStmt)
		if !ok {
			if block, nested := statement.(*ast.BlockStmt); nested {
				s.walkBlock(file, block.List, prefix, middleware, active, cloneRouterScope(routers), depth+1, dependencies)
				continue
			}
			if assignment, assigned := statement.(*ast.AssignStmt); assigned {
				for index, left := range assignment.Lhs {
					if index < len(assignment.Rhs) {
						name := exprSymbol(left)
						shadowRouterName(routers, name)
						if name != "" && newChiRouter(file, assignment.Rhs[index]) {
							routers[name] = true
						} else if name != "" && isNewServeMux(file, assignment.Rhs[index]) {
							routers["mux:"+name] = true
						} else if name != "" {
							delete(routers, "mux:"+name)
						}
					}
				}
			}
			if declaration, declared := statement.(*ast.DeclStmt); declared {
				if values, valid := declaration.Decl.(*ast.GenDecl); valid && values.Tok == token.VAR {
					for _, raw := range values.Specs {
						spec := raw.(*ast.ValueSpec)
						for index, identifier := range spec.Names {
							shadowRouterName(routers, identifier.Name)
							if serveMuxType(file, spec.Type) || index < len(spec.Values) && isNewServeMux(file, spec.Values[index]) {
								routers["mux:"+identifier.Name] = true
							}
							if index < len(spec.Values) && newChiRouter(file, spec.Values[index]) {
								routers[identifier.Name] = true
							}
						}
					}
				}
			}
			switch statement.(type) {
			case *ast.IfStmt, *ast.ForStmt, *ast.RangeStmt, *ast.SwitchStmt, *ast.TypeSwitchStmt:
				s.addDiagnostic(s.diag(file, statement.Pos(), DiagnosticControlFlow, "", "chi"))
			}
			continue
		}
		call, ok := expression.X.(*ast.CallExpr)
		if !ok {
			continue
		}
		if ident, local := call.Fun.(*ast.Ident); local {
			if ident.Obj != nil && ident.Obj.Kind != ast.Fun {
				continue
			}
			builder := s.functions[file.pkg+"."+ident.Name]
			if builder != nil && builder.Body != nil && !active[ident.Name] && callUsesRouter(call, routers) {
				builderFile := s.fileAt(builder.Pos())
				active[ident.Name] = true
				s.walkBlock(builderFile, builder.Body.List, prefix, middleware, active, routerParameters(builderFile, builder.Type.Params), depth+1, append(append([]string(nil), dependencies...), builderFile.name))
				delete(active, ident.Name)
			}
			continue
		}
		selector, receiver, ok := callSelector(call.Fun)
		if !ok {
			continue
		}
		if isHTTPRegistration(file, receiver, selector, routers) {
			s.extractHTTP(file, call, selector, dependencies)
			continue
		}
		if !isChiReceiver(call.Fun, routers) {
			continue
		}
		if selector == "Mount" || selector == "Handle" {
			if len(call.Args) >= 2 {
				mountPath, resolved := s.stringValue(call.Args[0], map[string]bool{})
				child := exprSymbol(call.Args[1])
				if builderCall, ok := call.Args[1].(*ast.CallExpr); ok {
					if ident, ok := builderCall.Fun.(*ast.Ident); ok {
						builder := s.functions[file.pkg+"."+ident.Name]
						if builder != nil {
							child = returnedRouter(builder)
							builderFile := s.fileAt(builder.Pos())
							if child != "" && !active[ident.Name] {
								active[ident.Name] = true
								s.walkBlock(builderFile, builder.Body.List, "", nil, active, map[string]bool{child: true}, depth+1, append(append([]string(nil), dependencies...), builderFile.name))
								delete(active, ident.Name)
							}
						}
					}
				}
				parent := chiReceiverName(call.Fun)
				if resolved && child != "" && parent != "" {
					s.mounts = append(s.mounts, mountFact{prefix: joinRoute(prefix, mountPath), child: child, parent: parent, diagnostic: s.diag(file, call.Pos(), DiagnosticUnsupported, "", "chi")})
					continue
				}
			}
			s.addDiagnostic(s.diag(file, call.Pos(), DiagnosticUnsupported, "", "chi"))
			continue
		}
		if selector == "Use" {
			for _, arg := range call.Args {
				if fact, valid := s.middlewareFact(file, arg, call.Pos()); valid {
					middleware = append(middleware, fact)
				}
			}
			continue
		}
		if selector == "Route" || selector == "Group" {
			if len(call.Args) == 0 {
				continue
			}
			nextPrefix := prefix
			bodyIndex := 0
			if selector == "Route" {
				fragment, resolved := s.stringValue(call.Args[0], map[string]bool{})
				if !resolved {
					s.addDiagnostic(s.diag(file, call.Pos(), DiagnosticDynamicRoute, "", "chi"))
					continue
				}
				nextPrefix = joinRoute(prefix, fragment)
				bodyIndex = 1
			}
			if bodyIndex < len(call.Args) {
				if literal, ok := call.Args[bodyIndex].(*ast.FuncLit); ok && literal.Body != nil {
					nestedRouters := routerParameters(file, literal.Type.Params)
					if len(nestedRouters) == 0 {
						nestedRouters = routers
					}
					s.walkBlock(file, literal.Body.List, nextPrefix, middleware, active, nestedRouters, depth+1, dependencies)
				} else {
					s.addDiagnostic(s.diag(file, call.Pos(), DiagnosticUnsupported, "", "chi"))
				}
			}
			continue
		}
		method, verb := chiMethod(selector, call, s)
		if !verb {
			continue
		}
		pathIndex := 0
		if selector == "Method" {
			pathIndex = 1
		}
		if len(call.Args) <= pathIndex+1 {
			s.addDiagnostic(s.diag(file, call.Pos(), DiagnosticDynamicRoute, "", "chi"))
			continue
		}
		route, resolved := s.stringValue(call.Args[pathIndex], map[string]bool{})
		handler := s.localHandlerSymbol(file, call.Args[pathIndex+1])
		if !resolved || handler == "" {
			s.addDiagnostic(s.diag(file, call.Pos(), DiagnosticDynamicRoute, handler, "chi"))
			continue
		}
		localMiddleware := append([]middlewareFact(nil), middleware...)
		for _, middlewareExpression := range withMiddleware(call.Fun) {
			if fact, valid := s.middlewareFact(file, middlewareExpression, call.Pos()); valid {
				localMiddleware = append(localMiddleware, fact)
			}
		}
		position := s.fset.Position(call.Pos())
		if len(s.operations) >= s.input.config.Limits.MaxOperations {
			s.failure = typedError(CategoryLimitExceeded, "operations", nil)
			return
		}
		fact := newOperationFact(method, joinRoute(prefix, route), handler, "chi-v5", file, position.Line, dependencies)
		fact.middleware = localMiddleware
		fact.router = chiReceiverName(call.Fun)
		s.operations = append(s.operations, fact)
	}
}

// shadowRouterName removes a lexical identifier and every tracked receiver field rooted at that identifier.
func shadowRouterName(values map[string]bool, name string) {
	if name == "" {
		return
	}
	delete(values, name)
	delete(values, "mux:"+name)
	for key := range values {
		if strings.HasPrefix(key, "mux:"+name+".") {
			delete(values, key)
		}
	}
}

// resolveMounts composes deferred child routers to a fixed point and diagnoses every unresolved branch.
func (s *analysisState) resolveMounts() {
	for pass := 0; pass <= len(s.mounts); pass++ {
		progress := false
		for mountIndex := range s.mounts {
			mount := &s.mounts[mountIndex]
			if mount.applied {
				continue
			}
			matched := false
			for operationIndex := range s.operations {
				if s.operations[operationIndex].router == mount.child {
					s.operations[operationIndex].path = joinRoute(mount.prefix, s.operations[operationIndex].path)
					s.operations[operationIndex].router = mount.parent
					matched = true
				}
			}
			if matched {
				mount.applied = true
				progress = true
			}
		}
		if !progress {
			break
		}
	}
	for _, mount := range s.mounts {
		if !mount.applied {
			s.addDiagnostic(mount.diagnostic)
		}
	}
}

// fileAt returns the parsed repository file owning one AST position without consulting the host filesystem.
func (s *analysisState) fileAt(position token.Pos) parsedFile {
	name := s.fset.Position(position).Filename
	for _, file := range s.files {
		if file.name == name {
			return file
		}
	}
	return parsedFile{name: name, imports: map[string]string{}}
}

// routerParameters recognizes only parameters whose selector resolves to the exact Chi v5 import.
func routerParameters(file parsedFile, fields *ast.FieldList) map[string]bool {
	out := map[string]bool{}
	if fields == nil {
		return out
	}
	for _, field := range fields.List {
		for _, name := range field.Names {
			out[name.Name] = false
			out["mux:"+name.Name] = false
		}
		if serveMuxType(file, field.Type) {
			for _, name := range field.Names {
				out["mux:"+name.Name] = true
			}
			continue
		}
		selector, ok := field.Type.(*ast.SelectorExpr)
		if !ok || selector.Sel.Name != "Router" || file.imports[exprSymbol(selector.X)] != "github.com/go-chi/chi/v5" {
			continue
		}
		for _, name := range field.Names {
			out[name.Name] = true
		}
	}
	return out
}

// routeScope combines exact function parameters with package-level ServeMux declarations visible to that function.
func (s *analysisState) routeScope(file parsedFile, function *ast.FuncDecl) map[string]bool {
	visible := map[string]bool{}
	for key := range s.globalMuxes {
		prefix := file.pkg + "."
		if strings.HasPrefix(key, prefix) {
			visible["mux:"+strings.TrimPrefix(key, prefix)] = true
		}
	}
	for key, value := range routerParameters(file, function.Type.Params) {
		visible[key] = value
	}
	if function.Recv != nil {
		for _, receiver := range function.Recv.List {
			typeName := exprSymbol(receiver.Type)
			for _, name := range receiver.Names {
				prefix := file.pkg + "." + typeName + "."
				for key := range s.muxFields {
					if strings.HasPrefix(key, prefix) {
						visible["mux:"+name.Name+"."+strings.TrimPrefix(key, prefix)] = true
					}
				}
			}
		}
	}
	return visible
}

// cloneRouterScope isolates nested lexical declarations while preserving routers visible at block entry.
func cloneRouterScope(values map[string]bool) map[string]bool {
	copyValues := make(map[string]bool, len(values))
	for key, value := range values {
		copyValues[key] = value
	}
	return copyValues
}

// publicSymbol removes private package-directory identity while retaining the declared package and selector.
func publicSymbol(value string) string {
	if marker := strings.Index(value, "#"); marker >= 0 {
		return value[marker+1:]
	}
	return value
}

// callUsesRouter proves a local builder receives an already tracked Chi router argument.
func callUsesRouter(call *ast.CallExpr, routers map[string]bool) bool {
	for _, argument := range call.Args {
		if routers[exprSymbol(argument)] {
			return true
		}
	}
	return false
}

// isChiReceiver follows only a tracked router or its immediate With fluent call.
func isChiReceiver(function ast.Expr, routers map[string]bool) bool {
	selector, ok := function.(*ast.SelectorExpr)
	if !ok {
		return false
	}
	if routers[exprSymbol(selector.X)] {
		return true
	}
	call, ok := selector.X.(*ast.CallExpr)
	if !ok {
		return false
	}
	inner, ok := call.Fun.(*ast.SelectorExpr)
	return ok && inner.Sel.Name == "With" && routers[exprSymbol(inner.X)]
}

// newChiRouter recognizes exact Chi v5 router construction without executing repository initialization.
func newChiRouter(file parsedFile, expression ast.Expr) bool {
	call, ok := expression.(*ast.CallExpr)
	if !ok {
		return false
	}
	selector, ok := call.Fun.(*ast.SelectorExpr)
	return ok && selector.Sel.Name == "NewRouter" && file.imports[exprSymbol(selector.X)] == "github.com/go-chi/chi/v5"
}

// chiReceiverName returns the tracked router identity behind a direct or With fluent registration.
func chiReceiverName(function ast.Expr) string {
	selector, ok := function.(*ast.SelectorExpr)
	if !ok {
		return ""
	}
	if name := exprSymbol(selector.X); name != "" {
		return name
	}
	call, ok := selector.X.(*ast.CallExpr)
	if !ok {
		return ""
	}
	inner, ok := call.Fun.(*ast.SelectorExpr)
	if !ok {
		return ""
	}
	return exprSymbol(inner.X)
}

// returnedRouter resolves a builder only when every return names the same local router variable.
func returnedRouter(function *ast.FuncDecl) string {
	result := ""
	valid := true
	ast.Inspect(function.Body, func(node ast.Node) bool {
		returnStatement, ok := node.(*ast.ReturnStmt)
		if !ok {
			return true
		}
		if len(returnStatement.Results) != 1 {
			valid = false
			return false
		}
		name := exprSymbol(returnStatement.Results[0])
		if name == "" || (result != "" && result != name) {
			valid = false
			return false
		}
		result = name
		return true
	})
	if !valid {
		return ""
	}
	return result
}

// extractHTTP admits only Go 1.22 patterns with an explicit method and a resolvable handler.
func (s *analysisState) extractHTTP(file parsedFile, call *ast.CallExpr, selector string, dependencies []string) {
	if len(call.Args) < 2 {
		return
	}
	pattern, ok := s.stringValue(call.Args[0], map[string]bool{})
	handler := s.localHandlerSymbol(file, call.Args[1])
	fields := strings.Fields(pattern)
	if !ok || len(fields) != 2 || !validHTTPMethod(fields[0]) || !strings.HasPrefix(fields[1], "/") || strings.Contains(fields[1], "{") && strings.Contains(fields[1], "...") || handler == "" {
		s.addDiagnostic(s.diag(file, call.Pos(), DiagnosticDynamicRoute, handler, "net-http"))
		return
	}
	position := s.fset.Position(call.Pos())
	if len(s.operations) >= s.input.config.Limits.MaxOperations {
		s.failure = typedError(CategoryLimitExceeded, "operations", nil)
		return
	}
	s.operations = append(s.operations, newOperationFact(fields[0], fields[1], handler, "net-http", file, position.Line, dependencies))
}

// newOperationFact initializes every field-granular provenance bucket for one statically proven route.
func newOperationFact(method, route, handler, framework string, file parsedFile, line int, dependencies []string) operationFact {
	return operationFact{method: method, path: route, handler: handler, framework: framework, file: file.name, pkg: file.pkg, line: line, dependencies: append([]string(nil), dependencies...), parameterLocations: map[string][]string{}, requestSchemaClaims: map[string][]string{}, responseFactLocations: map[int][]string{}, responseSchemaByStatus: map[int][]string{}, responseSchemaClaims: map[int]map[string][]string{}, securityLocations: map[string][]string{}}
}

// project enriches route identities with locally proven handler facts and seals canonical claims.
func (s *analysisState) project(sourceID string) (Result, error) {
	operations := make([]contractcatalog.OperationDescriptor, 0, len(s.operations))
	affected := []contractcatalog.OperationKey{}
	for _, fact := range s.operations {
		if err := s.ctx.Err(); err != nil {
			return Result{}, cancelled("projection", err)
		}
		completeness := s.input.config.Completeness
		if fact.partial || len(s.diagnostics) > 0 {
			completeness = contractcatalog.CompletenessPartial
		}
		provenance := contractcatalog.Provenance{SourceID: sourceID, Class: contractcatalog.ClassDerived, SourceDigest: s.input.digest, AdapterVersion: AdapterVersion, SafeLocation: s.safeLocation(fact), Confidence: s.input.config.Confidence, Completeness: completeness}
		op := contractcatalog.OperationDescriptor{Key: contractcatalog.OperationKey{Method: strings.ToUpper(fact.method), Path: fact.path}, LogicalID: s.safePublic(publicSymbol(fact.handler), "symbol"), Responses: map[string]contractcatalog.Response{}, Security: []contractcatalog.SecurityRequirement{}, SecuritySchemes: map[string]contractcatalog.SecurityScheme{}, Provenance: []contractcatalog.Provenance{provenance}, Confidence: s.input.config.Confidence, Completeness: completeness, Classes: []contractcatalog.SourceClass{contractcatalog.ClassDerived}}
		s.enrichHandler(&op, &fact)
		if len(op.Responses) == 0 {
			op.Responses["default"] = contractcatalog.Response{}
			op.Completeness = contractcatalog.CompletenessPartial
			provenance.Completeness = op.Completeness
			op.Provenance[0] = provenance
		}
		seenSecurity := map[string]bool{}
		for _, middleware := range fact.middleware {
			locations := []string{middleware.application}
			if declaration := s.middlewareDeclaration(fact.pkg, middleware.lookup); declaration != nil {
				fact.dependencies = append(fact.dependencies, s.fset.Position(declaration.Pos()).Filename)
				locations = append(locations, s.declarationLocation(declaration, middleware.syntax, "middleware"))
			}
			for _, matcher := range s.input.config.Auth {
				if authIdentityKey(middleware.identity) == authIdentityKey(matcher.Identity) && !seenSecurity[matcher.Scheme.Name] {
					op.Security = append(op.Security, contractcatalog.SecurityRequirement{Schemes: map[string][]string{matcher.Scheme.Name: {}}})
					op.SecuritySchemes[matcher.Scheme.Name] = matcher.Scheme
					fact.securityLocations[matcher.Scheme.Name] = append(fact.securityLocations[matcher.Scheme.Name], locations...)
					seenSecurity[matcher.Scheme.Name] = true
				}
			}
		}
		op.Claims = contractcatalog.ClaimsForOperation(op, op.Provenance[0])
		handlerProvenance := provenance
		if declaration := s.functionFor(fact); declaration != nil {
			handlerProvenance.SafeLocation = s.declarationLocation(declaration, fact.handler, "handler")
		}
		canonicalParameters := append([]contractcatalog.Parameter(nil), op.Parameters...)
		sort.Slice(canonicalParameters, func(i, j int) bool {
			return canonicalParameters[i].In+"\x00"+canonicalParameters[i].Name < canonicalParameters[j].In+"\x00"+canonicalParameters[j].Name
		})
		for index := range op.Claims {
			locations := []string{}
			field := op.Claims[index].Field
			if strings.HasPrefix(field, "parameters/") {
				parts := strings.Split(field, "/")
				if position, err := strconv.Atoi(parts[1]); err == nil && position < len(canonicalParameters) {
					parameter := canonicalParameters[position]
					locations = fact.parameterLocations[parameter.In+"\x00"+parameter.Name]
				}
			} else if strings.HasPrefix(field, "request_body") {
				locations = append([]string(nil), fact.requestFactLocations...)
				if path, schemaClaim := schemaProvenancePath(field); schemaClaim {
					locations = append(locations, fact.requestSchemaClaims[path]...)
				} else {
					locations = append(locations, fact.requestSchemaLocations...)
				}
			} else if strings.HasPrefix(field, "responses/") {
				selector := strings.Split(field, "/")[1]
				if status, err := strconv.Atoi(selector); err == nil {
					locations = append([]string(nil), fact.responseFactLocations[status]...)
					if path, schemaClaim := schemaProvenancePath(field); schemaClaim {
						locations = append(locations, fact.responseSchemaClaims[status][path]...)
					} else {
						locations = append(locations, fact.responseSchemaByStatus[status]...)
					}
				}
			} else if op.Claims[index].Kind == contractcatalog.ClaimSecurity {
				if scheme := securityClaimScheme(field, op.Security); scheme != "" {
					locations = append(locations, fact.securityLocations[scheme]...)
				}
			}
			if len(locations) > 0 {
				op.Claims[index].Provenance = s.provenanceAtLocations(handlerProvenance, locations)
			}
		}
		operations = append(operations, op)
		if s.isAffected(fact) {
			affected = append(affected, op.Key)
		}
	}
	neutralDiagnostics := make([]contractcatalog.SourceDiagnostic, len(s.diagnostics))
	for index, diagnostic := range s.diagnostics {
		location := s.safePublic(diagnostic.Path, "path")
		if diagnostic.Line > 0 {
			location += "/line-" + strconv.Itoa(diagnostic.Line)
		}
		neutralDiagnostics[index] = contractcatalog.SourceDiagnostic{Code: string(diagnostic.Code), SafeLocation: location}
	}
	snapshot := contractcatalog.SourceSnapshot{Version: contractcatalog.VersionV1, Source: contractcatalog.SourceSummary{ID: sourceID, Kind: SourceKind, Class: contractcatalog.ClassDerived, Digest: s.input.digest, AdapterVersion: AdapterVersion, Confidence: s.input.config.Confidence, Completeness: s.input.config.Completeness}, Operations: operations, Omissions: contractcatalog.OmissionCounts{SizeLimit: s.omitted}, Diagnostics: neutralDiagnostics, AffectedOperations: append([]contractcatalog.OperationKey(nil), affected...)}
	for _, op := range operations {
		for name, scheme := range op.SecuritySchemes {
			_ = name
			snapshot.SecuritySchemes = append(snapshot.SecuritySchemes, scheme)
		}
	}
	sort.Slice(snapshot.SecuritySchemes, func(i, j int) bool { return snapshot.SecuritySchemes[i].Name < snapshot.SecuritySchemes[j].Name })
	snapshot.SecuritySchemes = uniqueSchemes(snapshot.SecuritySchemes)
	sort.Slice(affected, func(i, j int) bool { return affected[i].Method+affected[i].Path < affected[j].Method+affected[j].Path })
	projection := struct {
		Snapshot    contractcatalog.SourceSnapshot
		Diagnostics []Diagnostic
		Omitted     int
		Affected    []contractcatalog.OperationKey
	}{snapshot, s.diagnostics, s.omitted, affected}
	raw, _ := json.Marshal(projection)
	sum := sha256.Sum256(raw)
	return Result{Snapshot: snapshot, Diagnostics: append([]Diagnostic(nil), s.diagnostics...), OmittedDiagnostics: s.omitted, AffectedOperations: affected, Digest: hex.EncodeToString(sum[:])}, nil
}

// schemaProvenancePath translates a catalog schema claim field into the JSON property path traced by the projector.
func schemaProvenancePath(field string) (string, bool) {
	marker := strings.Index(field, "/schema")
	if marker < 0 {
		return "", false
	}
	parts := strings.Split(strings.TrimPrefix(field[marker+len("/schema"):], "/"), "/")
	pathParts := []string{}
	for index := 0; index < len(parts); index++ {
		if parts[index] == "properties" && index+1 < len(parts) {
			pathParts = append(pathParts, parts[index+1])
			index++
		}
	}
	return strings.Join(pathParts, "/"), true
}

// mergeSchemaClaimLocations combines exact schema-node traces without sharing mutable source slices.
func mergeSchemaClaimLocations(target, source map[string][]string) {
	for path, locations := range source {
		target[path] = append(target[path], locations...)
	}
}

// provenanceAtLocations expands a material schema claim to every exact local type and field location that proved it.
func (s *analysisState) provenanceAtLocations(fallback contractcatalog.Provenance, locations []string) []contractcatalog.Provenance {
	if len(locations) == 0 {
		return []contractcatalog.Provenance{fallback}
	}
	values := make([]contractcatalog.Provenance, 0, len(locations))
	for _, location := range uniqueStrings(locations) {
		value := fallback
		value.SafeLocation = location
		values = append(values, value)
	}
	return values
}

// declarationLocation creates one redacted provenance location from the declaration that proved a fact.
func (s *analysisState) declarationLocation(declaration *ast.FuncDecl, symbol, role string) string {
	position := s.fset.Position(declaration.Pos())
	return s.safePublic(position.Filename, "path") + "/line-" + strconv.Itoa(position.Line) + "/" + s.safePublic(symbol, "symbol") + "/" + role
}

// enrichHandler extracts supported request parameters, JSON bodies, and explicit responses from one local handler.
func (s *analysisState) enrichHandler(operation *contractcatalog.OperationDescriptor, fact *operationFact) {
	decl := s.functionFor(*fact)
	if decl == nil || decl.Body == nil {
		operation.Completeness = contractcatalog.CompletenessPartial
		return
	}
	handlerFile := s.fset.Position(decl.Pos()).Filename
	fact.dependencies = append(fact.dependencies, handlerFile)
	handlerPackage := s.fileAt(decl.Pos()).pkg
	writerName, requestName := handlerBindings(s.fileAt(decl.Pos()), decl)
	if writerName == "" || requestName == "" {
		operation.Completeness = contractcatalog.CompletenessPartial
		return
	}
	writerObject := parameterObject(decl, writerName)
	requestObject := parameterObject(decl, requestName)
	if writerObject == nil || requestObject == nil {
		operation.Completeness = contractcatalog.CompletenessPartial
		return
	}
	seenParameters := map[string]bool{}
	requiredQueries := s.requiredQueryNames(handlerFile, decl.Body, requestObject, writerObject)
	ast.Inspect(decl.Body, func(node ast.Node) bool {
		if s.ctx.Err() != nil || node == nil {
			return s.ctx.Err() == nil
		}
		if index, ok := node.(*ast.IndexExpr); ok && isQueryCollection(index.X, requestObject) {
			if name, resolved := s.stringValue(index.Index, map[string]bool{}); resolved {
				key := "query\x00" + name
				if !seenParameters[key] {
					operation.Parameters = append(operation.Parameters, contractcatalog.Parameter{Name: name, In: "query", Schema: &contractcatalog.Schema{Type: "string"}})
					fact.parameterLocations[key] = append(fact.parameterLocations[key], s.factLocation(index.Pos(), "query"))
					seenParameters[key] = true
				}
			}
			return true
		}
		call, ok := node.(*ast.CallExpr)
		if !ok {
			return true
		}
		selector, _, ok := callSelector(call.Fun)
		if !ok {
			return true
		}
		switch selector {
		case "PathValue", "URLParam":
			selectorExpr, _ := call.Fun.(*ast.SelectorExpr)
			if selector == "PathValue" && !expressionUsesObject(selectorExpr.X, requestObject) {
				return true
			}
			if selector == "URLParam" && (!s.callFromImport(handlerFile, call.Fun, "github.com/go-chi/chi/v5") || len(call.Args) < 2 || !expressionUsesObject(call.Args[0], requestObject)) {
				return true
			}
			if len(call.Args) > 0 {
				argument := call.Args[len(call.Args)-1]
				if name, ok := s.stringValue(argument, map[string]bool{}); ok && routeHasParameter(fact.path, name) {
					key := "path\x00" + name
					if !seenParameters[key] {
						operation.Parameters = append(operation.Parameters, contractcatalog.Parameter{Name: name, In: "path", Required: true, Schema: &contractcatalog.Schema{Type: "string"}})
						fact.parameterLocations[key] = append(fact.parameterLocations[key], s.factLocation(call.Pos(), "path"))
						seenParameters[key] = true
					}
				} else {
					s.addDiagnostic(s.diag(s.fileAt(call.Pos()), call.Pos(), DiagnosticUnsupported, fact.handler, fact.framework))
				}
			}
		case "Get", "Has":
			if len(call.Args) == 1 && isQueryCall(call.Fun, requestObject) {
				if name, ok := s.stringValue(call.Args[0], map[string]bool{}); ok {
					key := "query\x00" + name
					if !seenParameters[key] {
						operation.Parameters = append(operation.Parameters, contractcatalog.Parameter{Name: name, In: "query", Required: requiredQueries[name], Schema: &contractcatalog.Schema{Type: "string"}})
						fact.parameterLocations[key] = append(fact.parameterLocations[key], s.factLocation(call.Pos(), "query"))
						seenParameters[key] = true
					}
				} else {
					s.addDiagnostic(s.diag(s.fileAt(call.Pos()), call.Pos(), DiagnosticUnsupported, fact.handler, fact.framework))
				}
			}
		case "Decode":
			if len(call.Args) == 1 && s.decoderCall(handlerFile, call.Fun, requestObject) {
				if unary, ok := call.Args[0].(*ast.UnaryExpr); ok && unary.Op == token.AND {
					if ident, ok := unary.X.(*ast.Ident); ok {
						if typeName := localVariableObjectType(ident); typeName != "" {
							schema, complete, locations, dependencies, claimLocations := s.projectSchema(handlerPackage + "." + typeName)
							if schema != nil {
								fact.dependencies = append(fact.dependencies, dependencies...)
								fact.requestSchemaLocations = append(fact.requestSchemaLocations, locations...)
								mergeSchemaClaimLocations(fact.requestSchemaClaims, claimLocations)
								fact.requestFactLocations = append(fact.requestFactLocations, s.factLocation(call.Pos(), "decode"))
								operation.RequestBody = &contractcatalog.RequestBody{Content: map[string]contractcatalog.MediaType{"application/json": {Schema: schema}}}
								if !complete {
									operation.Completeness = contractcatalog.CompletenessPartial
								}
							}
						}
					}
				}
			}
		}
		return true
	})
	_, flowSafe := s.interpretResponseStatements(operation, fact, decl, handlerFile, handlerPackage, writerObject, decl.Body.List, []responsePathState{{status: 200}})
	if !flowSafe {
		s.addDiagnostic(Diagnostic{Code: DiagnosticControlFlow, Path: handlerFile, Symbol: boundedSymbol(fact.handler), Framework: fact.framework})
	}
}

// factLocation creates a redacted location for the exact syntax node that proved one contract fact.
func (s *analysisState) factLocation(position token.Pos, role string) string {
	value := s.fset.Position(position)
	return s.safePublic(value.Filename, "path") + "/line-" + strconv.Itoa(value.Line) + "/" + role
}

// responsePathState carries response facts through exactly one control-flow continuation.
type responsePathState struct {
	status         int
	explicit       bool
	jsonMedia      bool
	mediaLocations []string
	committed      bool
	terminated     bool
}

// interpretResponseStatements propagates writer state per path and returns only continuations that are statically possible.
func (s *analysisState) interpretResponseStatements(operation *contractcatalog.OperationDescriptor, fact *operationFact, declaration *ast.FuncDecl, fileName, packageID string, writer *ast.Object, statements []ast.Stmt, states []responsePathState) ([]responsePathState, bool) {
	safe := true
	for _, statement := range statements {
		next := make([]responsePathState, 0, len(states)*2)
		for _, state := range states {
			if state.terminated {
				next = append(next, state)
				continue
			}
			values, valid := s.interpretResponseStatement(operation, fact, declaration, fileName, packageID, writer, statement, state)
			safe = safe && valid
			next = append(next, values...)
		}
		states = next
	}
	return states, safe
}

// interpretResponseStatement updates one path for the closed writer calls or forks it for a supported branch.
func (s *analysisState) interpretResponseStatement(operation *contractcatalog.OperationDescriptor, fact *operationFact, declaration *ast.FuncDecl, fileName, packageID string, writer *ast.Object, statement ast.Stmt, state responsePathState) ([]responsePathState, bool) {
	switch value := statement.(type) {
	case *ast.ReturnStmt:
		state.terminated = true
		return []responsePathState{state}, true
	case *ast.IfStmt:
		thenStates, thenSafe := s.interpretResponseStatements(operation, fact, declaration, fileName, packageID, writer, value.Body.List, []responsePathState{state})
		elseStates := []responsePathState{state}
		elseSafe := true
		if alternate, ok := value.Else.(*ast.BlockStmt); ok {
			elseStates, elseSafe = s.interpretResponseStatements(operation, fact, declaration, fileName, packageID, writer, alternate.List, []responsePathState{state})
		} else if value.Else != nil {
			return []responsePathState{{terminated: true}}, false
		}
		return append(thenStates, elseStates...), thenSafe && elseSafe
	case *ast.ForStmt, *ast.RangeStmt, *ast.SwitchStmt, *ast.TypeSwitchStmt, *ast.SelectStmt:
		s.interpretUnsupportedResponseControl(operation, fact, declaration, fileName, packageID, writer, value, state)
		return []responsePathState{state}, false
	}
	var callExpression ast.Expr
	if expression, ok := statement.(*ast.ExprStmt); ok {
		callExpression = expression.X
	} else if assignment, ok := statement.(*ast.AssignStmt); ok && len(assignment.Rhs) == 1 {
		callExpression = assignment.Rhs[0]
	} else {
		return []responsePathState{state}, true
	}
	call, ok := callExpression.(*ast.CallExpr)
	if !ok {
		return []responsePathState{state}, true
	}
	selector, _, ok := callSelector(call.Fun)
	if !ok {
		return []responsePathState{state}, true
	}
	switch selector {
	case "WriteHeader":
		if !state.committed && len(call.Args) == 1 && selectorUsesObject(call.Fun, writer) {
			if status, valid := s.intValue(call.Args[0]); valid {
				state.status, state.explicit, state.committed = status, true, true
				ensureResponse(operation, status)
				fact.responseFactLocations[status] = append(fact.responseFactLocations[status], s.factLocation(call.Pos(), "status"))
				fact.responseFactLocations[status] = append(fact.responseFactLocations[status], state.mediaLocations...)
			}
		}
	case "Error":
		if len(call.Args) >= 3 && s.callFromImport(fileName, call.Fun, "net/http") && expressionUsesObject(call.Args[0], writer) {
			if status, valid := s.intValue(call.Args[2]); valid && !state.committed {
				state.status, state.explicit, state.committed = status, true, true
				ensureResponse(operation, state.status)
				fact.responseFactLocations[state.status] = append(fact.responseFactLocations[state.status], s.factLocation(call.Pos(), "status"))
				fact.responseFactLocations[state.status] = append(fact.responseFactLocations[state.status], state.mediaLocations...)
			}
		}
	case "Set":
		if !state.committed && len(call.Args) == 2 && writerHeaderSetUsesObject(call.Fun, writer) {
			name, nameOK := s.stringValue(call.Args[0], map[string]bool{})
			media, mediaOK := s.stringValue(call.Args[1], map[string]bool{})
			if nameOK && mediaOK && strings.EqualFold(name, "Content-Type") {
				state.jsonMedia = strings.HasPrefix(strings.ToLower(media), "application/json")
				if state.jsonMedia {
					state.mediaLocations = append(state.mediaLocations, s.factLocation(call.Pos(), "media"))
				}
			}
		}
	case "Write":
		if selectorUsesObject(call.Fun, writer) {
			state.committed = true
			ensureResponse(operation, state.status)
			fact.responseFactLocations[state.status] = append(fact.responseFactLocations[state.status], state.mediaLocations...)
			fact.responseFactLocations[state.status] = append(fact.responseFactLocations[state.status], s.factLocation(call.Pos(), "write"))
		}
	case "Encode":
		if len(call.Args) == 1 && s.encoderCallObject(fileName, call.Fun, writer) {
			state.committed = true
			ensureResponse(operation, state.status)
			fact.responseFactLocations[state.status] = append(fact.responseFactLocations[state.status], state.mediaLocations...)
			fact.responseFactLocations[state.status] = append(fact.responseFactLocations[state.status], s.factLocation(call.Pos(), "encode"))
			if state.jsonMedia {
				s.addEncodedSchema(operation, fact, packageID, state.status, call.Args[0])
			}
		}
	}
	return []responsePathState{state}, true
}

// interpretUnsupportedResponseControl preserves explicit writer facts inside unsupported branches while refusing to infer their continuation or exhaustiveness.
func (s *analysisState) interpretUnsupportedResponseControl(operation *contractcatalog.OperationDescriptor, fact *operationFact, declaration *ast.FuncDecl, fileName, packageID string, writer *ast.Object, statement ast.Stmt, state responsePathState) {
	branches := [][]ast.Stmt{}
	switch value := statement.(type) {
	case *ast.ForStmt:
		branches = append(branches, value.Body.List)
	case *ast.RangeStmt:
		branches = append(branches, value.Body.List)
	case *ast.SwitchStmt:
		for _, raw := range value.Body.List {
			if clause, ok := raw.(*ast.CaseClause); ok {
				branches = append(branches, clause.Body)
			}
		}
	case *ast.TypeSwitchStmt:
		for _, raw := range value.Body.List {
			if clause, ok := raw.(*ast.CaseClause); ok {
				branches = append(branches, clause.Body)
			}
		}
	case *ast.SelectStmt:
		for _, raw := range value.Body.List {
			if clause, ok := raw.(*ast.CommClause); ok {
				branches = append(branches, clause.Body)
			}
		}
	}
	for _, branch := range branches {
		_, _ = s.interpretResponseStatements(operation, fact, declaration, fileName, packageID, writer, branch, []responsePathState{state})
	}
}

// ensureResponse records the status already established on one response path without inventing a default sibling.
func ensureResponse(operation *contractcatalog.OperationDescriptor, status int) {
	key := strconv.Itoa(status)
	if _, exists := operation.Responses[key]; !exists {
		operation.Responses[key] = contractcatalog.Response{}
	}
}

// addEncodedSchema attaches one proven JSON schema to the status carried by the same response path.
func (s *analysisState) addEncodedSchema(operation *contractcatalog.OperationDescriptor, fact *operationFact, packageID string, status int, expression ast.Expr) {
	identifier, ok := expression.(*ast.Ident)
	if !ok {
		return
	}
	typeName := localVariableObjectType(identifier)
	if typeName == "" {
		return
	}
	schema, _, locations, dependencies, claimLocations := s.projectSchema(packageID + "." + typeName)
	if schema == nil {
		return
	}
	fact.dependencies = append(fact.dependencies, dependencies...)
	fact.responseSchemaLocations = append(fact.responseSchemaLocations, locations...)
	fact.responseSchemaByStatus[status] = append(fact.responseSchemaByStatus[status], locations...)
	if fact.responseSchemaClaims[status] == nil {
		fact.responseSchemaClaims[status] = map[string][]string{}
	}
	mergeSchemaClaimLocations(fact.responseSchemaClaims[status], claimLocations)
	response := operation.Responses[strconv.Itoa(status)]
	if response.Content == nil {
		response.Content = map[string]contractcatalog.MediaType{}
	}
	response.Content["application/json"] = contractcatalog.MediaType{Schema: schema}
	operation.Responses[strconv.Itoa(status)] = response
}

// functionFor resolves unqualified handlers in the registration package and qualified local package selectors exactly.
func (s *analysisState) functionFor(fact operationFact) *ast.FuncDecl {
	if strings.Contains(fact.handler, ".") {
		if declaration := s.functions[fact.handler]; declaration != nil {
			return declaration
		}
	}
	if declaration := s.functions[fact.pkg+"."+fact.handler]; declaration != nil {
		return declaration
	}
	return nil
}

// isQueryCollection recognizes only request.URL.Query() as the base of a constant index access.
func isQueryCollection(expression ast.Expr, request *ast.Object) bool {
	call, ok := expression.(*ast.CallExpr)
	if !ok || len(call.Args) != 0 {
		return false
	}
	selector, ok := call.Fun.(*ast.SelectorExpr)
	if !ok || selector.Sel.Name != "Query" {
		return false
	}
	url, ok := selector.X.(*ast.SelectorExpr)
	return ok && url.Sel.Name == "URL" && expressionUsesObject(url.X, request)
}

// requiredQueryNames recognizes exact terminal HTTP writer guards and never infers required from a homonymous call.
func (s *analysisState) requiredQueryNames(fileName string, body *ast.BlockStmt, request, writer *ast.Object) map[string]bool {
	variables := map[*ast.Object]string{}
	required := map[string]bool{}
	ast.Inspect(body, func(node ast.Node) bool {
		assignment, ok := node.(*ast.AssignStmt)
		if !ok {
			return true
		}
		for index, left := range assignment.Lhs {
			if index >= len(assignment.Rhs) {
				continue
			}
			name, ok := left.(*ast.Ident)
			if !ok {
				continue
			}
			if query, ok := s.queryName(assignment.Rhs[index], request); ok {
				variables[name.Obj] = query
			}
		}
		return true
	})
	ast.Inspect(body, func(node ast.Node) bool {
		branch, ok := node.(*ast.IfStmt)
		if !ok {
			return true
		}
		variable, ok := emptyStringGuardObject(branch.Cond)
		if !ok || variables[variable] == "" || !s.terminalRejection(fileName, branch.Body, writer) {
			return true
		}
		required[variables[variable]] = true
		return true
	})
	return required
}

// queryName extracts a constant query key from the exact Query Get/Has call pattern.
func (s *analysisState) queryName(expression ast.Expr, request *ast.Object) (string, bool) {
	call, ok := expression.(*ast.CallExpr)
	if !ok || !isQueryCall(call.Fun, request) || len(call.Args) != 1 {
		return "", false
	}
	return s.stringValue(call.Args[0], map[string]bool{})
}

// emptyStringGuardObject recognizes equality to an empty string and returns the parser-resolved variable object.
func emptyStringGuardObject(expression ast.Expr) (*ast.Object, bool) {
	binary, ok := expression.(*ast.BinaryExpr)
	if !ok || binary.Op != token.EQL {
		return nil, false
	}
	if ident, ok := binary.X.(*ast.Ident); ok {
		if literal, ok := binary.Y.(*ast.BasicLit); ok && literal.Kind == token.STRING && literal.Value == `""` {
			return ident.Obj, ident.Obj != nil
		}
	}
	if ident, ok := binary.Y.(*ast.Ident); ok {
		if literal, ok := binary.X.(*ast.BasicLit); ok && literal.Kind == token.STRING && literal.Value == `""` {
			return ident.Obj, ident.Obj != nil
		}
	}
	return nil, false
}

// terminalRejection proves a direct exact HTTP error/write on the bound writer followed by a terminal return.
func (s *analysisState) terminalRejection(fileName string, block *ast.BlockStmt, writer *ast.Object) bool {
	if block == nil || len(block.List) < 2 {
		return false
	}
	if _, ok := block.List[len(block.List)-1].(*ast.ReturnStmt); !ok {
		return false
	}
	for _, statement := range block.List[:len(block.List)-1] {
		expression, ok := statement.(*ast.ExprStmt)
		if !ok {
			continue
		}
		call, ok := expression.X.(*ast.CallExpr)
		if !ok {
			continue
		}
		selector, _, ok := callSelector(call.Fun)
		if !ok {
			continue
		}
		if selector == "WriteHeader" && len(call.Args) == 1 && selectorUsesObject(call.Fun, writer) {
			_, valid := s.intValue(call.Args[0])
			return valid
		}
		if selector == "Error" && len(call.Args) >= 3 && s.callFromImport(fileName, call.Fun, "net/http") && expressionUsesObject(call.Args[0], writer) {
			_, valid := s.intValue(call.Args[2])
			return valid
		}
	}
	return false
}

// callFromImport proves that a selector receiver alias names the exact allowlisted import path.
func (s *analysisState) callFromImport(fileName string, expression ast.Expr, importPath string) bool {
	selector, ok := expression.(*ast.SelectorExpr)
	if !ok {
		return false
	}
	if identifier, local := selector.X.(*ast.Ident); local && identifier.Obj != nil {
		return false
	}
	alias := exprSymbol(selector.X)
	for _, file := range s.files {
		if file.name == fileName {
			return file.imports[alias] == importPath
		}
	}
	return false
}

// handlerBindings proves the concrete request and writer parameter identities from exact net/http types.
func handlerBindings(file parsedFile, declaration *ast.FuncDecl) (string, string) {
	writer, request := "", ""
	if declaration.Type.Params == nil {
		return writer, request
	}
	for _, field := range declaration.Type.Params.List {
		if len(field.Names) != 1 {
			continue
		}
		switch value := field.Type.(type) {
		case *ast.SelectorExpr:
			if value.Sel.Name == "ResponseWriter" && file.imports[exprSymbol(value.X)] == "net/http" {
				writer = field.Names[0].Name
			}
		case *ast.StarExpr:
			if selector, ok := value.X.(*ast.SelectorExpr); ok && selector.Sel.Name == "Request" && file.imports[exprSymbol(selector.X)] == "net/http" {
				request = field.Names[0].Name
			}
		}
	}
	return writer, request
}

// parameterObject returns the parser-resolved declaration identity for one named function parameter.
func parameterObject(declaration *ast.FuncDecl, name string) *ast.Object {
	if declaration == nil || declaration.Type.Params == nil {
		return nil
	}
	for _, field := range declaration.Type.Params.List {
		for _, identifier := range field.Names {
			if identifier.Name == name {
				return identifier.Obj
			}
		}
	}
	return nil
}

// expressionUsesObject proves a direct identifier refers to the exact parser-resolved declaration.
func expressionUsesObject(expression ast.Expr, object *ast.Object) bool {
	identifier, ok := expression.(*ast.Ident)
	return ok && object != nil && identifier.Obj == object
}

// selectorUsesObject proves a method receiver refers to the exact parser-resolved declaration.
func selectorUsesObject(expression ast.Expr, object *ast.Object) bool {
	selector, ok := expression.(*ast.SelectorExpr)
	return ok && expressionUsesObject(selector.X, object)
}

// writerHeaderSetUsesObject recognizes only writer.Header().Set on the proven response-writer declaration.
func writerHeaderSetUsesObject(expression ast.Expr, writer *ast.Object) bool {
	outer, ok := expression.(*ast.SelectorExpr)
	if !ok {
		return false
	}
	headerCall, ok := outer.X.(*ast.CallExpr)
	if !ok {
		return false
	}
	headerSelector, ok := headerCall.Fun.(*ast.SelectorExpr)
	return ok && headerSelector.Sel.Name == "Header" && expressionUsesObject(headerSelector.X, writer)
}

// isQueryCall recognizes only request.URL.Query().Get/Has and rejects same-named unrelated methods.
func isQueryCall(expression ast.Expr, request *ast.Object) bool {
	outer, ok := expression.(*ast.SelectorExpr)
	if !ok {
		return false
	}
	queryCall, ok := outer.X.(*ast.CallExpr)
	if !ok || len(queryCall.Args) != 0 {
		return false
	}
	querySelector, ok := queryCall.Fun.(*ast.SelectorExpr)
	if !ok || querySelector.Sel.Name != "Query" {
		return false
	}
	url, ok := querySelector.X.(*ast.SelectorExpr)
	return ok && url.Sel.Name == "URL" && expressionUsesObject(url.X, request)
}

// decoderCall recognizes only encoding/json.NewDecoder(...).Decode through the file's exact import identity.
func (s *analysisState) decoderCall(fileName string, expression ast.Expr, request *ast.Object) bool {
	outer, ok := expression.(*ast.SelectorExpr)
	if !ok {
		return false
	}
	constructor, ok := outer.X.(*ast.CallExpr)
	if !ok || len(constructor.Args) != 1 {
		return false
	}
	body, ok := constructor.Args[0].(*ast.SelectorExpr)
	if !ok || body.Sel.Name != "Body" || !expressionUsesObject(body.X, request) {
		return false
	}
	selector, ok := constructor.Fun.(*ast.SelectorExpr)
	if !ok || selector.Sel.Name != "NewDecoder" {
		return false
	}
	if identifier, local := selector.X.(*ast.Ident); local && identifier.Obj != nil {
		return false
	}
	alias := exprSymbol(selector.X)
	for _, file := range s.files {
		if file.name == fileName {
			return file.imports[alias] == "encoding/json"
		}
	}
	return false
}

// encoderCallObject recognizes encoding/json.NewEncoder(writer).Encode through exact import and writer identities.
func (s *analysisState) encoderCallObject(fileName string, expression ast.Expr, writer *ast.Object) bool {
	outer, ok := expression.(*ast.SelectorExpr)
	if !ok {
		return false
	}
	constructor, ok := outer.X.(*ast.CallExpr)
	if !ok || len(constructor.Args) != 1 {
		return false
	}
	if !expressionUsesObject(constructor.Args[0], writer) {
		return false
	}
	selector, ok := constructor.Fun.(*ast.SelectorExpr)
	if !ok || selector.Sel.Name != "NewEncoder" {
		return false
	}
	if identifier, local := selector.X.(*ast.Ident); local && identifier.Obj != nil {
		return false
	}
	alias := exprSymbol(selector.X)
	for _, file := range s.files {
		if file.name == fileName {
			return file.imports[alias] == "encoding/json"
		}
	}
	return false
}

// stringValue resolves only literals and local constant expressions without evaluating functions or initializers.
func (s *analysisState) stringValue(expression ast.Expr, active map[string]bool) (string, bool) {
	switch value := expression.(type) {
	case *ast.BasicLit:
		if value.Kind == token.STRING {
			parsed, err := strconv.Unquote(value.Value)
			return parsed, err == nil
		}
	case *ast.Ident:
		if value.Obj != nil {
			if constant, ok := s.objectConstants[value.Obj]; ok {
				if active[value.Name] {
					return "", false
				}
				active[value.Name] = true
				result, valid := s.stringValue(constant, active)
				delete(active, value.Name)
				return result, valid
			}
			if value.Obj.Kind != ast.Con {
				return "", false
			}
		}
		file := s.fileAt(value.Pos())
		key := file.pkg + "." + value.Name
		if active[key] {
			return "", false
		}
		constant, ok := s.constants[key]
		if !ok {
			return "", false
		}
		active[key] = true
		result, valid := s.stringValue(constant, active)
		delete(active, key)
		return result, valid
	case *ast.BinaryExpr:
		if value.Op == token.ADD {
			left, leftOK := s.stringValue(value.X, active)
			right, rightOK := s.stringValue(value.Y, active)
			return left + right, leftOK && rightOK
		}
	case *ast.SelectorExpr:
		file := s.fileAt(value.Pos())
		alias := exprSymbol(value.X)
		if file.imports[alias] == "net/http" && strings.HasPrefix(value.Sel.Name, "Method") {
			return strings.TrimPrefix(value.Sel.Name, "Method"), true
		}
		if imported := file.imports[alias]; s.packages[imported] != "" {
			if constant := s.constants[s.packages[imported]+"."+value.Sel.Name]; constant != nil {
				return s.stringValue(constant, active)
			}
		}
	}
	return "", false
}

// parseModulePath reads only the single go.mod module directive and never resolves modules or imports.
func parseModulePath(content []byte) string {
	for _, line := range strings.Split(string(content), "\n") {
		fields := strings.Fields(line)
		if len(fields) == 2 && fields[0] == "module" && safeSymbol(fields[1]) {
			return fields[1]
		}
	}
	return ""
}

// localHandlerSymbol maps exact local module import aliases to package-qualified declarations without host resolution.
func (s *analysisState) localHandlerSymbol(file parsedFile, expression ast.Expr) string {
	if identifier, ok := expression.(*ast.Ident); ok && identifier.Obj != nil && identifier.Obj.Kind != ast.Fun {
		return ""
	}
	selector, ok := expression.(*ast.SelectorExpr)
	if !ok {
		return exprSymbol(expression)
	}
	alias := exprSymbol(selector.X)
	importPath, imported := file.imports[alias]
	if !imported {
		if identifier, ok := selector.X.(*ast.Ident); !ok || identifier.Obj == nil {
			return ""
		}
	}
	if packageID := s.packages[importPath]; packageID != "" {
		return packageID + "." + selector.Sel.Name
	}
	return exprSymbol(expression)
}

// qualifySymbol resolves one selector string through the snapshot's exact import-to-package identity map.
func (s *analysisState) qualifySymbol(file parsedFile, symbol string) string {
	parts := strings.SplitN(symbol, ".", 2)
	if len(parts) == 2 {
		importPath, imported := file.imports[parts[0]]
		if !imported {
			return ""
		}
		if packageID := s.packages[importPath]; packageID != "" {
			return packageID + "." + parts[1]
		}
	}
	return symbol
}

// middlewareFact derives a collision-free local identity or an explicit external alias identity from one exact expression.
func (s *analysisState) middlewareFact(file parsedFile, expression ast.Expr, position token.Pos) (middlewareFact, bool) {
	symbol := exprSymbol(expression)
	if symbol == "" {
		return middlewareFact{}, false
	}
	if identifier, ok := expression.(*ast.Ident); ok {
		if identifier.Obj != nil && identifier.Obj.Kind != ast.Fun {
			return middlewareFact{}, false
		}
		identity := AuthIdentity{Kind: AuthIdentityLocal, ImportPath: s.packageAuthorityIdentity(file.pkg), Symbol: identifier.Name}
		return middlewareFact{identity: identity, syntax: identifier.Name, lookup: identifier.Name, application: s.factLocation(position, "middleware-application")}, true
	}
	selector, ok := expression.(*ast.SelectorExpr)
	if !ok {
		return middlewareFact{}, false
	}
	alias, ok := selector.X.(*ast.Ident)
	if !ok || alias.Obj != nil {
		return middlewareFact{}, false
	}
	importPath, imported := file.imports[alias.Name]
	if !imported {
		return middlewareFact{}, false
	}
	lookup := symbol
	identity := AuthIdentity{Kind: AuthIdentityExternal, ImportPath: importPath, Alias: alias.Name, Symbol: selector.Sel.Name}
	if packageID := s.packages[importPath]; packageID != "" {
		lookup = packageID + "." + selector.Sel.Name
		identity = AuthIdentity{Kind: AuthIdentityLocal, ImportPath: importPath, Symbol: selector.Sel.Name}
	}
	return middlewareFact{identity: identity, syntax: symbol, lookup: lookup, application: s.factLocation(position, "middleware-application")}, true
}

// packageAuthorityIdentity returns the module import path when known and otherwise the repository directory plus declared package name.
func (s *analysisState) packageAuthorityIdentity(packageID string) string {
	for importPath, candidate := range s.packages {
		if candidate == packageID && !strings.HasPrefix(importPath, "/") {
			return importPath
		}
	}
	parts := strings.SplitN(packageID, "#", 2)
	if parts[0] != "." && parts[0] != "" {
		return parts[0]
	}
	if len(parts) == 2 {
		return parts[1]
	}
	return packageID
}

// securityClaimScheme identifies only the concrete scheme represented by one security claim field.
func securityClaimScheme(field string, requirements []contractcatalog.SecurityRequirement) string {
	if strings.HasPrefix(field, "security_schemes/") {
		return strings.TrimPrefix(field, "security_schemes/")
	}
	parts := strings.Split(field, "/")
	if len(parts) >= 4 && parts[0] == "security" && parts[2] == "schemes" {
		return parts[3]
	}
	if len(parts) == 2 && parts[0] == "security" {
		requirements = append([]contractcatalog.SecurityRequirement(nil), requirements...)
		sort.Slice(requirements, func(i, j int) bool {
			left, _ := json.Marshal(requirements[i])
			right, _ := json.Marshal(requirements[j])
			return string(left) < string(right)
		})
		index, err := strconv.Atoi(parts[1])
		if err != nil || index >= len(requirements) {
			return ""
		}
		for name := range requirements[index].Schemes {
			return name
		}
	}
	return ""
}

// middlewareDeclaration resolves local or imported middleware without global name fallback.
func (s *analysisState) middlewareDeclaration(packageID, symbol string) *ast.FuncDecl {
	if declaration := s.functions[symbol]; declaration != nil {
		return declaration
	}
	return s.functions[packageID+"."+symbol]
}

// intValue resolves response status literals and the closed net/http status constants.
func (s *analysisState) intValue(expression ast.Expr) (int, bool) {
	if literal, ok := expression.(*ast.BasicLit); ok && literal.Kind == token.INT {
		value, err := strconv.Atoi(literal.Value)
		return value, err == nil
	}
	statuses := map[string]int{"http.StatusOK": 200, "http.StatusCreated": 201, "http.StatusNoContent": 204, "http.StatusBadRequest": 400, "http.StatusUnauthorized": 401, "http.StatusForbidden": 403, "http.StatusNotFound": 404, "http.StatusConflict": 409, "http.StatusInternalServerError": 500}
	value, ok := statuses[exprSymbol(expression)]
	return value, ok
}

// addDiagnostic deduplicates safe diagnostics and accounts for excess without retaining hidden text.
func (s *analysisState) addDiagnostic(value Diagnostic) {
	for _, existing := range s.diagnostics {
		if existing == value {
			return
		}
	}
	if len(s.diagnostics) >= s.input.config.Limits.MaxDiagnostics {
		s.omitted++
		return
	}
	s.diagnostics = append(s.diagnostics, value)
	sort.Slice(s.diagnostics, func(i, j int) bool {
		a, b := s.diagnostics[i], s.diagnostics[j]
		return string(a.Code)+a.Path+strconv.Itoa(a.Line) < string(b.Code)+b.Path+strconv.Itoa(b.Line)
	})
}

// diag creates provenance-safe structured diagnostics without parser messages or source snippets.
func (s *analysisState) diag(file parsedFile, position token.Pos, code DiagnosticCode, symbol, framework string) Diagnostic {
	return Diagnostic{Code: code, Path: s.safePublic(file.name, "path"), Line: s.fset.Position(position).Line, Symbol: s.safePublic(boundedSymbol(symbol), "symbol"), Framework: framework}
}

// isAffected marks operations whose registration, handler declaration, or static local type closure touches a trusted changed path.
func (s *analysisState) isAffected(fact operationFact) bool {
	if len(s.input.config.ChangedPaths) == 0 {
		return false
	}
	for _, changed := range s.input.config.ChangedPaths {
		for _, dependency := range fact.dependencies {
			if changed == dependency {
				return true
			}
		}
		if changed == fact.file {
			return true
		}
		if decl := s.functionFor(fact); decl != nil {
			if s.fset.Position(decl.Pos()).Filename == changed {
				return true
			}
		}
	}
	return false
}

// matchBuild evaluates only explicit go:build expressions against the sealed build context.
func matchBuild(content []byte, build BuildContext) bool {
	text := string(content)
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "//go:build ") {
			expression, err := constraint.Parse(line)
			if err != nil {
				return false
			}
			tags := map[string]bool{build.GOOS: true, build.GOARCH: true}
			for _, tag := range build.Tags {
				tags[tag] = true
			}
			return expression.Eval(func(tag string) bool { return tags[tag] })
		}
		if line != "" && !strings.HasPrefix(line, "//") {
			break
		}
	}
	return true
}

// matchFilename applies Go's explicit GOOS/GOARCH suffix selection without consulting host defaults.
func matchFilename(name string, build BuildContext) bool {
	base := strings.TrimSuffix(path.Base(name), ".go")
	parts := strings.Split(base, "_")
	if len(parts) < 2 {
		return true
	}
	oses := map[string]bool{"aix": true, "android": true, "darwin": true, "dragonfly": true, "freebsd": true, "illumos": true, "ios": true, "js": true, "linux": true, "netbsd": true, "openbsd": true, "plan9": true, "solaris": true, "wasip1": true, "windows": true}
	arches := map[string]bool{"386": true, "amd64": true, "arm": true, "arm64": true, "loong64": true, "mips": true, "mips64": true, "mips64le": true, "mipsle": true, "ppc64": true, "ppc64le": true, "riscv64": true, "s390x": true, "wasm": true}
	last := parts[len(parts)-1]
	if arches[last] && last != build.GOARCH {
		return false
	}
	if oses[last] && last != build.GOOS {
		return false
	}
	if len(parts) >= 3 {
		previous := parts[len(parts)-2]
		if oses[previous] && previous != build.GOOS {
			return false
		}
	}
	return true
}

// funcSymbol returns an exact local function or receiver-method identity.
func funcSymbol(value *ast.FuncDecl) string {
	if value.Recv == nil || len(value.Recv.List) == 0 {
		return value.Name.Name
	}
	return exprSymbol(value.Recv.List[0].Type) + "." + value.Name.Name
}

// exprSymbol reduces identifiers and selectors without following arbitrary calls.
func exprSymbol(value ast.Expr) string {
	switch node := value.(type) {
	case *ast.Ident:
		return node.Name
	case *ast.SelectorExpr:
		prefix := exprSymbol(node.X)
		if prefix != "" {
			return prefix + "." + node.Sel.Name
		}
	case *ast.StarExpr:
		return exprSymbol(node.X)
	case *ast.FuncLit:
		return "func_literal"
	}
	return ""
}

// callSelector separates a call selector from its receiver for exact framework matching.
func callSelector(value ast.Expr) (string, string, bool) {
	selector, ok := value.(*ast.SelectorExpr)
	if !ok {
		return "", "", false
	}
	return selector.Sel.Name, exprSymbol(selector.X), true
}

// isHTTPRegistration uses import identity for package functions and the closed ServeMux method names.
func isHTTPRegistration(file parsedFile, receiver, selector string, visible map[string]bool) bool {
	if selector != "Handle" && selector != "HandleFunc" {
		return false
	}
	if file.imports[receiver] == "net/http" {
		return true
	}
	return receiver != "" && visible["mux:"+receiver]
}

// isNewServeMux recognizes construction through the exact net/http import alias.
func isNewServeMux(file parsedFile, expression ast.Expr) bool {
	call, ok := expression.(*ast.CallExpr)
	if !ok {
		return false
	}
	selector, ok := call.Fun.(*ast.SelectorExpr)
	return ok && selector.Sel.Name == "NewServeMux" && file.imports[exprSymbol(selector.X)] == "net/http"
}

// serveMuxType recognizes explicit local pointers or values of net/http.ServeMux.
func serveMuxType(file parsedFile, expression ast.Expr) bool {
	if pointer, ok := expression.(*ast.StarExpr); ok {
		expression = pointer.X
	}
	selector, ok := expression.(*ast.SelectorExpr)
	return ok && selector.Sel.Name == "ServeMux" && file.imports[exprSymbol(selector.X)] == "net/http"
}

// chiMethod maps only supported Chi v5 verbs and statically resolved Method values.
func chiMethod(selector string, call *ast.CallExpr, state *analysisState) (string, bool) {
	verbs := map[string]string{"Get": "GET", "Post": "POST", "Put": "PUT", "Patch": "PATCH", "Delete": "DELETE", "Head": "HEAD", "Options": "OPTIONS", "Connect": "CONNECT", "Trace": "TRACE"}
	if method, ok := verbs[selector]; ok {
		return method, true
	}
	if selector == "Method" && len(call.Args) > 0 {
		return state.stringValue(call.Args[0], map[string]bool{})
	}
	return "", false
}

// withMiddleware extracts exact With argument expressions from the immediate fluent receiver.
func withMiddleware(value ast.Expr) []ast.Expr {
	selector, ok := value.(*ast.SelectorExpr)
	if !ok {
		return nil
	}
	call, ok := selector.X.(*ast.CallExpr)
	if !ok {
		return nil
	}
	name, _, ok := callSelector(call.Fun)
	if !ok || name != "With" {
		return nil
	}
	return append([]ast.Expr(nil), call.Args...)
}

// joinRoute composes prefixes with exactly one slash while preserving the root route.
func joinRoute(prefix, fragment string) string {
	if prefix == "" {
		if fragment == "" {
			return "/"
		}
		return "/" + strings.TrimPrefix(fragment, "/")
	}
	if fragment == "/" || fragment == "" {
		return "/" + strings.Trim(strings.TrimSuffix(prefix, "/"), "/")
	}
	return "/" + strings.Trim(prefix, "/") + "/" + strings.TrimPrefix(fragment, "/")
}

// validHTTPMethod admits the standard explicit methods supported by the v1 catalog.
func validHTTPMethod(value string) bool {
	switch value {
	case "GET", "POST", "PUT", "PATCH", "DELETE", "HEAD", "OPTIONS", "CONNECT", "TRACE":
		return true
	}
	return false
}

// routeHasParameter verifies that an access name is present in the effective route template.
func routeHasParameter(route, name string) bool {
	return strings.Contains(route, "{"+name+"}") || strings.Contains(route, "{"+name+":")
}

// localVariableObjectType resolves only the declaration object bound to the exact identifier use, preserving lexical scope and order.
func localVariableObjectType(identifier *ast.Ident) string {
	if identifier == nil || identifier.Obj == nil || identifier.Obj.Kind != ast.Var {
		return ""
	}
	switch declaration := identifier.Obj.Decl.(type) {
	case *ast.ValueSpec:
		if declaration.Type != nil {
			return exprSymbol(declaration.Type)
		}
		for index, name := range declaration.Names {
			if name.Obj == identifier.Obj && index < len(declaration.Values) {
				if literal, ok := declaration.Values[index].(*ast.CompositeLit); ok {
					return exprSymbol(literal.Type)
				}
			}
		}
	case *ast.AssignStmt:
		for index, left := range declaration.Lhs {
			name, ok := left.(*ast.Ident)
			if ok && name.Obj == identifier.Obj && index < len(declaration.Rhs) {
				if literal, ok := declaration.Rhs[index].(*ast.CompositeLit); ok {
					return exprSymbol(literal.Type)
				}
			}
		}
	}
	return ""
}

// safeLocation encodes bounded relative file, line, symbol, and framework provenance.
func (s *analysisState) safeLocation(fact operationFact) string {
	return s.safePublic(fact.file, "path") + "/line-" + strconv.Itoa(fact.line) + "/" + s.safePublic(boundedSymbol(fact.handler), "symbol") + "/" + fact.framework
}

// safePublic replaces projection-sensitive private strings with deterministic non-reversible identifiers.
func (s *analysisState) safePublic(value, prefix string) string {
	unsafe := len(value) > s.input.config.Projection.MaxStringBytes || strings.ContainsAny(value, "\x00\r\n")
	lower := strings.ToLower(value)
	for _, sensitive := range append(append([]string(nil), s.input.config.Projection.SensitiveNames...), s.input.config.Projection.SensitivePaths...) {
		unsafe = unsafe || sensitive != "" && strings.Contains(lower, strings.ToLower(sensitive))
	}
	for _, forbidden := range s.input.config.Projection.ForbiddenValues {
		unsafe = unsafe || forbidden != "" && strings.Contains(value, forbidden)
	}
	if !unsafe {
		return value
	}
	digest := sha256.Sum256([]byte(value))
	return prefix + "-" + hex.EncodeToString(digest[:6])
}

// boundedSymbol prevents arbitrary source identifiers from expanding diagnostic or provenance records.
func boundedSymbol(value string) string {
	if len(value) > 128 {
		return "symbol"
	}
	for _, r := range value {
		if !(r == '.' || r == '_' || r == '*' || r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9') {
			return "symbol"
		}
	}
	return value
}

// uniqueSchemes removes identical scheme identities from the catalog-level projection.
func uniqueSchemes(values []contractcatalog.SecurityScheme) []contractcatalog.SecurityScheme {
	if len(values) == 0 {
		return values
	}
	out := values[:0]
	seen := map[string]bool{}
	for _, value := range values {
		if !seen[value.Name] {
			seen[value.Name] = true
			out = append(out, value)
		}
	}
	return out
}
