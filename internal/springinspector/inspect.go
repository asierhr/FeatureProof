package springinspector

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/asierhr/featureProof/internal/contractcatalog"
)

// Event is the closed metadata-only observability record emitted around one inspection.
type Event struct {
	Type, RepositoryID, Revision, SourceID, Digest, Outcome string
	Operations, Diagnostics                                 int
}

// Observer receives safe lifecycle metadata and has no authority over inspection.
type Observer interface{ Record(Event) }

// Inspector runs the passive Java parser and neutral projection; phaseBarrier is a test cancellation seam.
type Inspector struct {
	Observer     Observer
	phaseBarrier func(context.Context, string)
}

// mappingFact is a resolved closed-set Spring mapping annotation.
type mappingFact struct {
	methods, paths, consumes, produces []string
	dependencies                       []string
	line                               int
	framework                          string
	valid                              bool
}

// typeScope binds Java type resolution to one source set, package and explicit import table.
type typeScope struct {
	sourceSet, packageName, owner string
	imports                       map[string]string
	file, symbol                  string
	line                          int
}

// analysisState owns one inspection's indexes, budgets, diagnostics and derived dependencies.
type analysisState struct {
	ctx            context.Context
	input          Input
	units          []javaUnit
	types          map[string]javaType
	ambiguousTypes map[string]bool
	constants      map[string][]string
	constantFiles  map[string]string
	diagnostics    []Diagnostic
	omitted        int
	operations     []contractcatalog.OperationDescriptor
	dependencies   map[contractcatalog.OperationKey]map[string]bool
	schemaNodes    int
	fatal          error
}

// Inspect derives only operations whose method and path are fully proven from the sealed snapshot.
func (inspector Inspector) Inspect(ctx context.Context, input Input, sourceID string) (result Result, returnErr error) {
	if input.digest == "" || !safeToken(sourceID, 128) {
		return Result{}, typedError(CategoryInvalidInput, "inspect", nil)
	}
	inspector.emit(Event{Type: "spring_inspection_started", RepositoryID: input.config.RepositoryID, Revision: input.config.Revision, SourceID: sourceID, Digest: input.Digest(), Outcome: "started"})
	defer func() {
		event := Event{Type: "spring_inspection_completed", RepositoryID: input.config.RepositoryID, Revision: input.config.Revision, SourceID: sourceID, Digest: input.Digest(), Outcome: "completed", Operations: len(result.Snapshot.Operations), Diagnostics: len(result.Diagnostics)}
		if returnErr != nil {
			event.Type = "spring_inspection_failed"
			event.Outcome = "failed"
		}
		inspector.emit(event)
	}()
	bounded, cancel := inspectionContext(ctx, input.config.Limits.MaxDuration, input.deadlineAt)
	defer cancel()
	s := &analysisState{ctx: bounded, input: input, types: map[string]javaType{}, ambiguousTypes: map[string]bool{}, constants: map[string][]string{}, constantFiles: map[string]string{}, dependencies: map[contractcatalog.OperationKey]map[string]bool{}}
	if err := inspector.check(bounded, "parse"); err != nil {
		return Result{}, err
	}
	if err := s.parse(); err != nil {
		return Result{}, err
	}
	if err := inspector.check(bounded, "index"); err != nil {
		return Result{}, err
	}
	if err := s.index(); err != nil {
		return Result{}, err
	}
	if err := inspector.check(bounded, "mappings"); err != nil {
		return Result{}, err
	}
	if err := s.extract(sourceID); err != nil {
		return Result{}, err
	}
	if err := bounded.Err(); err != nil {
		return Result{}, typedError(CategoryCancelled, "projection", err)
	}
	if len(s.operations) == 0 {
		return Result{}, typedError(CategoryInsufficientContext, "mappings", nil)
	}
	sort.Slice(s.operations, func(i, j int) bool { return opKey(s.operations[i].Key) < opKey(s.operations[j].Key) })
	affected := s.affectedOperations()
	if err := bounded.Err(); err != nil {
		return Result{}, typedError(CategoryCancelled, "closure", err)
	}
	snapshot := contractcatalog.SourceSnapshot{Version: contractcatalog.VersionV1, Source: contractcatalog.SourceSummary{ID: sourceID, RepositoryID: input.config.RepositoryID, Revision: input.config.Revision, Kind: SourceKind, Class: contractcatalog.ClassDerived, Digest: input.Digest(), AdapterVersion: AdapterVersion, Confidence: input.config.Confidence, Completeness: input.config.Completeness}, Operations: s.operations, Omissions: contractcatalog.OmissionCounts{Unselected: s.omitted}, Diagnostics: s.catalogDiagnostics(), AffectedOperations: affected}
	if err := contractcatalog.ValidateSourceProjection(snapshot, input.config.Projection); err != nil {
		var catalogError *contractcatalog.Error
		if errors.As(err, &catalogError) {
			return Result{}, typedError(CategoryInvalidInput, "projection."+catalogError.Location, nil)
		}
		return Result{}, typedError(CategoryInvalidInput, "projection", nil)
	}
	raw, _ := json.Marshal(snapshot)
	sum := sha256.Sum256(raw)
	return Result{Snapshot: snapshot, Diagnostics: append([]Diagnostic(nil), s.diagnostics...), OmittedDiagnostics: s.omitted, AffectedOperations: affected, Digest: hex.EncodeToString(sum[:])}, nil
}

// emit publishes only the fixed safe event shape when an observer was explicitly composed.
func (inspector Inspector) emit(event Event) {
	if inspector.Observer != nil {
		inspector.Observer.Record(event)
	}
}

// check provides deterministic cancellation points between major parser phases.
func (inspector Inspector) check(ctx context.Context, stage string) error {
	if inspector.phaseBarrier != nil {
		inspector.phaseBarrier(ctx, stage)
	}
	if err := ctx.Err(); err != nil {
		return typedError(CategoryCancelled, stage, err)
	}
	return nil
}

// inspectionContext installs the shorter of caller, configured and sealed deadlines.
func inspectionContext(ctx context.Context, maximum time.Duration, sealed time.Time) (context.Context, context.CancelFunc) {
	deadline := time.Now().Add(maximum)
	if parent, ok := ctx.Deadline(); ok && parent.Before(deadline) {
		deadline = parent
	}
	if !sealed.IsZero() && sealed.Before(deadline) {
		deadline = sealed
	}
	return context.WithDeadline(ctx, deadline)
}

// parse selects Java files from authorized source sets and parses each unit independently.
func (s *analysisState) parse() error {
	names := make([]string, 0, len(s.input.snapshot.files))
	roots := s.activeRoots()
	for name := range s.input.snapshot.files {
		if withinRoots(name, roots) && strings.HasSuffix(name, ".java") {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	if len(names) > s.input.config.Limits.MaxFiles {
		return typedError(CategoryLimitExceeded, "files", nil)
	}
	total, tokens := 0, 0
	for _, name := range names {
		if err := s.ctx.Err(); err != nil {
			return typedError(CategoryCancelled, "parse", err)
		}
		content := s.input.snapshot.files[name]
		total += len(content)
		if total > s.input.config.Limits.MaxBytes {
			return typedError(CategoryLimitExceeded, "bytes", nil)
		}
		unit, err := parseJavaUnit(s.ctx, name, content, s.input.config.Limits.MaxTokens-tokens)
		if err != nil {
			var inspection *Error
			if errors.As(err, &inspection) && (inspection.Category == CategoryLimitExceeded || inspection.Category == CategoryCancelled) {
				return err
			}
			s.addDiagnostic(Diagnostic{Code: DiagnosticSyntax, Path: name})
			continue
		}
		if len(unit.ambiguousImports) > 0 {
			s.addDiagnostic(Diagnostic{Code: DiagnosticUnsupported, Path: name, Symbol: "ambiguous_import"})
			continue
		}
		tokens += unit.tokens
		unit.sourceSet = s.sourceSetFor(name)
		for index := range unit.types {
			unit.types[index].sourceSet = unit.sourceSet
			unit.types[index].imports = cloneImports(unit.imports)
		}
		if s.input.config.JavaRelease < 16 {
			filtered := unit.types[:0]
			for _, declared := range unit.types {
				if declared.kind == "record" {
					s.addDiagnostic(Diagnostic{Code: DiagnosticUnsupported, Path: name, Line: declared.line, Symbol: declared.name})
					continue
				}
				filtered = append(filtered, declared)
			}
			unit.types = filtered
		}
		s.units = append(s.units, unit)
		if len(s.units) > s.input.config.Limits.MaxUnits {
			return typedError(CategoryLimitExceeded, "units", nil)
		}
	}
	return nil
}

// sourceSetFor resolves a file to the unique explicit source set already validated by composition.
func (s *analysisState) sourceSetFor(name string) string {
	for _, set := range s.input.config.SourceSets {
		if withinRoots(name, []string{set.Root}) {
			return set.ID
		}
	}
	return ""
}

// activeRoots returns trusted non-generated roots unless generated sources were explicitly admitted.
func (s *analysisState) activeRoots() []string {
	out := []string{}
	for _, set := range s.input.config.SourceSets {
		if !set.Generated || s.input.config.Generated == GeneratedInclude {
			out = append(out, set.Root)
		}
	}
	return out
}

// index builds local constant and type lookup without opening a classpath.

func (s *analysisState) index() error {
	typeCount, symbolCount := 0, 0
	for _, u := range s.units {
		if err := s.ctx.Err(); err != nil {
			return typedError(CategoryCancelled, "index", err)
		}
		for name, values := range u.constants {
			symbolCount++
			if symbolCount > s.input.config.Limits.MaxSymbols {
				return typedError(CategoryLimitExceeded, "symbols", nil)
			}
			if strings.Contains(name, ".") {
				identity := u.packageName + "." + name
				s.constants[identity] = append([]string(nil), values...)
				s.constantFiles[identity] = u.file
			}
		}
		for _, t := range u.types {
			if err := s.ctx.Err(); err != nil {
				return typedError(CategoryCancelled, "index", err)
			}
			typeCount++
			symbolCount++
			if symbolCount > s.input.config.Limits.MaxSymbols {
				return typedError(CategoryLimitExceeded, "symbols", nil)
			}
			if typeCount > s.input.config.Limits.MaxTypes {
				return typedError(CategoryLimitExceeded, "types", nil)
			}
			fqcn := t.packageName + "." + t.name
			key := t.sourceSet + "\x00" + fqcn
			if _, exists := s.types[key]; exists || s.ambiguousTypes[key] {
				delete(s.types, key)
				s.ambiguousTypes[key] = true
				s.addDiagnostic(Diagnostic{Code: DiagnosticUnsupported, Path: u.file, Line: t.line, Symbol: "duplicate_type"})
				continue
			}
			s.types[key] = t
		}
	}
	return nil
}

// extract composes class and method mappings, parameters, schemas, responses and inert security declarations.
func (s *analysisState) extract(sourceID string) error {
	seen := map[contractcatalog.OperationKey]bool{}
	for _, u := range s.units {
		for _, class := range u.classes {
			if err := s.ctx.Err(); err != nil {
				return typedError(CategoryCancelled, "mappings", err)
			}
			if s.ambiguousTypes[u.sourceSet+"\x00"+u.packageName+"."+class.name] {
				s.addDiagnostic(Diagnostic{Code: DiagnosticUnsupported, Path: u.file, Line: class.line, Symbol: "duplicate_controller"})
				continue
			}
			if !s.isController(u, class) {
				continue
			}
			classMappings := s.mappingAnnotations(u, class.annotations, true)
			if len(classMappings) == 0 {
				classMappings = []mappingFact{{paths: []string{""}, valid: true}}
			}
			classSecurity := s.securityConstraints(sourceID, u, class.annotations, class.name)
			for _, method := range class.methods {
				if err := s.ctx.Err(); err != nil {
					return typedError(CategoryCancelled, "mappings", err)
				}
				if !s.methodHasResponseBody(u, class, method) {
					s.addDiagnostic(Diagnostic{Code: DiagnosticUnsupported, Path: u.file, Line: method.line, Symbol: method.name, Framework: "spring.mvc.view"})
					continue
				}
				methodMappings := s.mappingAnnotations(u, method.annotations, false)
				for _, cm := range classMappings {
					for _, mm := range methodMappings {
						if !cm.valid || !mm.valid || len(mm.methods) == 0 {
							continue
						}
						paths := crossPaths(cm.paths, mm.paths, s.input.config.Limits.MaxCrossProduct)
						if paths == nil {
							return typedError(CategoryLimitExceeded, "mapping.cross_product", nil)
						}
						methods := mm.methods
						if len(cm.methods) > 0 {
							methods = intersect(cm.methods, mm.methods)
						}
						for _, verb := range methods {
							for _, route := range paths {
								if !validRoute(route) {
									s.addDiagnostic(Diagnostic{Code: DiagnosticDynamicMapping, Path: u.file, Line: mm.line, Symbol: method.name, Framework: mm.framework})
									continue
								}
								key := contractcatalog.OperationKey{Method: verb, Path: route}
								if seen[key] {
									s.addDiagnostic(Diagnostic{Code: DiagnosticUnsupported, Path: u.file, Line: mm.line, Symbol: method.name})
									continue
								}
								seen[key] = true
								prov := s.provenance(sourceID, u.file, mm.line, class.name+"."+method.name, mm.framework)
								consumes := mm.consumes
								if len(consumes) == 0 {
									consumes = cm.consumes
								}
								params, body, deps := s.parameters(sourceID, u, method, route, consumes)
								responses, responseDeps, partial := s.responses(sourceID, u, class, method, cm, mm)
								if s.fatal != nil {
									return s.fatal
								}
								deps = append(deps, responseDeps...)
								deps = append(deps, cm.dependencies...)
								deps = append(deps, mm.dependencies...)
								constraints := append(append([]contractcatalog.DeclarativeConstraint(nil), classSecurity...), s.securityConstraints(sourceID, u, method.annotations, class.name+"."+method.name)...)
								op := contractcatalog.OperationDescriptor{Key: key, LogicalID: class.name + "." + method.name, Parameters: params, RequestBody: body, Responses: responses, Security: []contractcatalog.SecurityRequirement{}, DeclarativeConstraints: constraints, Provenance: []contractcatalog.Provenance{prov}, Confidence: s.input.config.Confidence, Completeness: s.input.config.Completeness, Classes: []contractcatalog.SourceClass{contractcatalog.ClassDerived}}
								op.Claims = contractcatalog.ClaimsForOperation(op, prov)
								s.refineClaimProvenance(op.Claims, sourceID, u, method, op)
								s.operations = append(s.operations, op)
								s.dependencies[key] = map[string]bool{u.file: true}
								for _, dep := range deps {
									s.dependencies[key][dep] = true
								}
								if partial {
									s.addDiagnostic(Diagnostic{Code: DiagnosticPartial, Path: u.file, Line: method.line, Symbol: method.name, Framework: mm.framework})
								}
								if len(s.operations) > s.input.config.Limits.MaxOperations {
									return typedError(CategoryLimitExceeded, "operations", nil)
								}
							}
						}
					}
				}
			}
		}
	}
	return nil
}

// refineClaimProvenance binds parameter and body claims to their exact supported annotation rather than only the enclosing mapping.
func (s *analysisState) refineClaimProvenance(claims []contractcatalog.Claim, sourceID string, unit javaUnit, method javaMethod, operation contractcatalog.OperationDescriptor) {
	parameterProvenance := map[string]contractcatalog.Provenance{}
	var bodyProvenance *contractcatalog.Provenance
	var responseProvenance *contractcatalog.Provenance
	schemaProvenance := map[string]contractcatalog.Provenance{}
	if operation.RequestBody != nil {
		for media, value := range operation.RequestBody.Content {
			s.collectSchemaProvenance(schemaProvenance, "request_body/content/"+media+"/schema", value.Schema, sourceID)
		}
	}
	for selector, response := range operation.Responses {
		for media, value := range response.Content {
			s.collectSchemaProvenance(schemaProvenance, "responses/"+selector+"/content/"+media+"/schema", value.Schema, sourceID)
		}
	}
	for position, parameter := range operation.Parameters {
		s.collectSchemaProvenance(schemaProvenance, fmt.Sprintf("parameters/%d/schema", position), parameter.Schema, sourceID)
	}
	for _, parameter := range method.parameters {
		for _, a := range parameter.annotations {
			fq := resolveAnnotation(unit, a.name)
			location := ""
			switch fq {
			case "org.springframework.web.bind.annotation.PathVariable":
				location = "path"
			case "org.springframework.web.bind.annotation.RequestParam":
				location = "query"
			case "org.springframework.web.bind.annotation.RequestHeader":
				location = "header"
			case "org.springframework.web.bind.annotation.RequestBody":
				p := s.provenance(sourceID, unit.file, a.line, method.owner+"."+method.name+"."+parameter.name, fq)
				bodyProvenance = &p
			}
			if location != "" {
				parameterProvenance[location+"\x00"+parameter.name] = s.provenance(sourceID, unit.file, a.line, method.owner+"."+method.name+"."+parameter.name, fq)
			}
		}
	}
	for _, a := range method.annotations {
		if resolveAnnotation(unit, a.name) == "org.springframework.web.bind.annotation.ResponseStatus" {
			p := s.provenance(sourceID, unit.file, a.line, method.owner+"."+method.name, resolveAnnotation(unit, a.name))
			responseProvenance = &p
		}
	}
	for index := range claims {
		claim := &claims[index]
		if strings.HasPrefix(claim.Field, "parameters/") {
			parts := strings.Split(claim.Field, "/")
			if len(parts) >= 2 {
				position, err := strconv.Atoi(parts[1])
				if err == nil && position < len(operation.Parameters) {
					if p, ok := parameterProvenance[operation.Parameters[position].In+"\x00"+operation.Parameters[position].Name]; ok {
						p.SafeLocation += "/" + claim.Field
						claim.Provenance = []contractcatalog.Provenance{p}
					}
				}
			}
		}
		if strings.HasPrefix(claim.Field, "request_body") && bodyProvenance != nil {
			p := *bodyProvenance
			p.SafeLocation += "/" + claim.Field
			claim.Provenance = []contractcatalog.Provenance{p}
		}
		if strings.HasPrefix(claim.Field, "responses/") && responseProvenance != nil {
			p := *responseProvenance
			p.SafeLocation += "/" + claim.Field
			claim.Provenance = []contractcatalog.Provenance{p}
		}
		if strings.HasPrefix(claim.Field, "declarative_constraints/") {
			parts := strings.Split(claim.Field, "/")
			if len(parts) == 2 {
				position, err := strconv.Atoi(parts[1])
				if err == nil && position < len(operation.DeclarativeConstraints) && len(operation.DeclarativeConstraints[position].Provenance) > 0 {
					p := operation.DeclarativeConstraints[position].Provenance[0]
					p.SafeLocation += "/" + claim.Field
					claim.Provenance = []contractcatalog.Provenance{p}
				}
			}
		}
		if p, ok := schemaProvenance[claim.Field]; ok {
			p.SafeLocation += "/" + claim.Field
			claim.Provenance = []contractcatalog.Provenance{p}
		}
	}
}

// collectSchemaProvenance maps each local schema node to its exact Java type or field declaration.
func (s *analysisState) collectSchemaProvenance(out map[string]contractcatalog.Provenance, field string, schema *contractcatalog.Schema, sourceID string) {
	if schema == nil {
		return
	}
	var declared *javaType
	for _, candidate := range s.types {
		identity := candidate.packageName + "." + candidate.name
		if schema.ID != "" && identity == schema.ID {
			copy := candidate
			declared = &copy
			break
		}
	}
	if declared != nil {
		out[field] = s.provenance(sourceID, declared.file, declared.line, declared.name, "java.type")
		for name, child := range schema.Properties {
			childField := field + "/properties/" + name
			for _, javaField := range declared.fields {
				if javaField.name == name {
					out[childField] = s.provenance(sourceID, declared.file, javaField.line, declared.name+"."+name, "java.field")
					break
				}
			}
			s.collectSchemaProvenance(out, childField, child, sourceID)
		}
	}
	if schema.Items != nil {
		s.collectSchemaProvenance(out, field+"/items", schema.Items, sourceID)
	}
	for index, member := range schema.AllOf {
		s.collectSchemaProvenance(out, fmt.Sprintf("%s/allOf/%d", field, index), member, sourceID)
	}
}

// isController accepts only exact allowlisted Spring controller annotation identities.
func (s *analysisState) isController(u javaUnit, class javaClass) bool {
	for _, a := range class.annotations {
		fq := resolveAnnotation(u, a.name)
		if fq == "org.springframework.web.bind.annotation.RestController" || fq == "org.springframework.stereotype.Controller" {
			return true
		}
	}
	return false
}

// methodHasResponseBody proves response semantics for each method independently.
func (s *analysisState) methodHasResponseBody(u javaUnit, class javaClass, method javaMethod) bool {
	rest, controller, classBody := false, false, false
	for _, a := range class.annotations {
		switch resolveAnnotation(u, a.name) {
		case "org.springframework.web.bind.annotation.RestController":
			rest = true
		case "org.springframework.stereotype.Controller":
			controller = true
		case "org.springframework.web.bind.annotation.ResponseBody":
			classBody = true
		}
	}
	if rest || (controller && classBody) {
		return true
	}
	for _, a := range method.annotations {
		if resolveAnnotation(u, a.name) == "org.springframework.web.bind.annotation.ResponseBody" {
			return true
		}
	}
	return controller && resolveTypeIdentity(method.returnType, typeScope{sourceSet: u.sourceSet, packageName: u.packageName, owner: method.owner, imports: u.imports}) == "org.springframework.http.ResponseEntity"
}

// constantsFor resolves local and explicit static-import constants without simple-name global fallback.
func (s *analysisState) constantsFor(unit javaUnit) map[string][]string {
	out := map[string][]string{}
	for name, values := range unit.constants {
		out[name] = append([]string(nil), values...)
	}
	for alias, identity := range unit.imports {
		if values, ok := s.constants[identity]; ok {
			out[alias] = append([]string(nil), values...)
		}
	}
	return out
}

// constantDependencies returns source files for exact static-import constants referenced by an annotation.
func (s *analysisState) constantDependencies(unit javaUnit, tokens []token) []string {
	out := []string{}
	for _, item := range tokens {
		if identity := unit.imports[item.text]; identity != "" {
			if file := s.constantFiles[identity]; file != "" {
				out = append(out, file)
			}
		}
	}
	return unique(out)
}

// mappingAnnotations resolves the exact Spring MVC mapping allowlist and static attributes.
func (s *analysisState) mappingAnnotations(u javaUnit, annotations []annotation, classLevel bool) []mappingFact {
	out := []mappingFact{}
	for _, a := range annotations {
		fq := resolveAnnotation(u, a.name)
		verb := ""
		switch fq {
		case "org.springframework.web.bind.annotation.RequestMapping":
		case "org.springframework.web.bind.annotation.GetMapping":
			verb = "GET"
		case "org.springframework.web.bind.annotation.PostMapping":
			verb = "POST"
		case "org.springframework.web.bind.annotation.PutMapping":
			verb = "PUT"
		case "org.springframework.web.bind.annotation.PatchMapping":
			verb = "PATCH"
		case "org.springframework.web.bind.annotation.DeleteMapping":
			verb = "DELETE"
		default:
			continue
		}
		attrs, ok := s.annotationAttributes(a.args, s.constantsFor(u))
		if !ok {
			s.addDiagnostic(Diagnostic{Code: DiagnosticDynamicMapping, Path: u.file, Line: a.line, Framework: fq})
			continue
		}
		paths := attrs["path"]
		if len(paths) == 0 {
			paths = attrs["value"]
		}
		if len(paths) == 0 {
			paths = []string{""}
		}
		methods := []string{}
		if verb != "" {
			methods = []string{verb}
		} else if values := attrs["method"]; len(values) > 0 {
			for _, value := range values {
				value = strings.TrimPrefix(value, "RequestMethod.")
				switch value {
				case "GET", "POST", "PUT", "PATCH", "DELETE":
					methods = append(methods, value)
				}
			}
		}
		if !classLevel && len(methods) == 0 {
			s.addDiagnostic(Diagnostic{Code: DiagnosticDynamicMapping, Path: u.file, Line: a.line, Framework: fq})
			continue
		}
		out = append(out, mappingFact{methods: unique(methods), paths: unique(paths), consumes: attrs["consumes"], produces: attrs["produces"], dependencies: s.constantDependencies(u, a.args), line: a.line, framework: fq, valid: true})
	}
	return out
}

// annotationAttributes evaluates only named aliases, literal arrays and exact constants.
func (s *analysisState) annotationAttributes(tokens []token, constants map[string][]string) (map[string][]string, bool) {
	result := map[string][]string{}
	if len(tokens) == 0 {
		return result, true
	}
	for _, part := range splitTopLevel(tokens, ",") {
		eq := findTokenRange(part, 0, len(part), "=")
		name := "value"
		value := part
		if eq >= 0 {
			name = part[0].text
			value = part[eq+1:]
		}
		if name == "method" {
			values := []string{}
			if len(value) > 1 && value[0].text == "{" {
				value = value[1 : len(value)-1]
			}
			for _, item := range splitTopLevel(value, ",") {
				text := joinType(item)
				if strings.HasPrefix(text, "RequestMethod.") {
					values = append(values, text)
				} else {
					return nil, false
				}
			}
			result[name] = values
			continue
		}
		if name == "required" && len(value) == 1 && (value[0].text == "true" || value[0].text == "false") {
			result[name] = []string{value[0].text}
			continue
		}
		if (name == "code" || name == "value") && len(value) == 3 && value[1].text == "." && (value[0].text == "HttpStatus" || value[0].text == "HttpStatusCode") {
			result[name] = []string{joinType(value)}
			continue
		}
		values, ok := literalValues(value, constants)
		if !ok {
			return nil, false
		}
		for _, candidate := range values {
			if len(candidate) > s.input.config.Limits.MaxStringBytes {
				return nil, false
			}
		}
		if prior, exists := result[name]; exists && !equalStrings(prior, values) {
			return nil, false
		}
		result[name] = values
	}
	for _, aliases := range [][2]string{{"path", "value"}, {"name", "value"}} {
		a, aok := result[aliases[0]]
		b, bok := result[aliases[1]]
		if aok && bok && !equalStrings(a, b) {
			return nil, false
		}
	}
	return result, true
}

// parameters derives only exact supported Spring parameter annotations and local structural types.
func (s *analysisState) parameters(sourceID string, u javaUnit, method javaMethod, route string, consumes []string) ([]contractcatalog.Parameter, *contractcatalog.RequestBody, []string) {
	out := []contractcatalog.Parameter{}
	var body *contractcatalog.RequestBody
	deps := []string{}
	for _, p := range method.parameters {
		if err := s.ctx.Err(); err != nil {
			s.fatal = typedError(CategoryCancelled, "parameters", err)
			return nil, nil, nil
		}
		for _, a := range p.annotations {
			fq := resolveAnnotation(u, a.name)
			location := ""
			switch fq {
			case "org.springframework.web.bind.annotation.PathVariable":
				location = "path"
			case "org.springframework.web.bind.annotation.RequestParam":
				location = "query"
			case "org.springframework.web.bind.annotation.RequestHeader":
				location = "header"
			case "org.springframework.web.bind.annotation.RequestBody":
				attrs, valid := s.annotationAttributes(a.args, s.constantsFor(u))
				if !valid {
					continue
				}
				required := true
				if value := attrs["required"]; len(value) == 1 {
					required = value[0] == "true"
				}
				schema, dep, ok := s.schemaFor(p.typeName, typeScope{sourceSet: u.sourceSet, packageName: u.packageName, owner: method.owner, imports: u.imports, file: u.file, line: p.line, symbol: p.name}, 0, map[string]bool{})
				if ok {
					content := map[string]contractcatalog.MediaType{}
					for _, media := range consumes {
						content[media] = contractcatalog.MediaType{Schema: schema}
					}
					body = &contractcatalog.RequestBody{Required: required, Content: content}
					deps = append(deps, dep)
				} else {
					s.addDiagnostic(Diagnostic{Code: DiagnosticMissingType, Path: u.file, Line: p.line, Symbol: p.name})
				}
				continue
			default:
				continue
			}
			attrs, ok := s.annotationAttributes(a.args, s.constantsFor(u))
			if !ok {
				continue
			}
			name := p.name
			if v := attrs["name"]; len(v) == 1 {
				name = v[0]
			} else if v := attrs["value"]; len(v) == 1 {
				name = v[0]
			}
			required := true
			if v := attrs["required"]; len(v) == 1 {
				required = v[0] == "true"
			}
			if len(attrs["defaultValue"]) == 1 {
				required = false
			}
			if location == "path" && !strings.Contains(route, "{"+name+"}") {
				continue
			}
			schema, dep, ok := s.schemaFor(p.typeName, typeScope{sourceSet: u.sourceSet, packageName: u.packageName, owner: method.owner, imports: u.imports, file: u.file, line: p.line, symbol: p.name}, 0, map[string]bool{})
			if !ok {
				s.addDiagnostic(Diagnostic{Code: DiagnosticMissingType, Path: u.file, Line: p.line, Symbol: p.name})
				continue
			}
			out = append(out, contractcatalog.Parameter{Name: name, In: location, Required: required, Schema: schema})
			deps = append(deps, dep)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].In+out[i].Name < out[j].In+out[j].Name })
	return out, body, unique(deps)
}

// responses derives explicit response status, negotiated media and ResponseEntity payload types conservatively.
func (s *analysisState) responses(sourceID string, u javaUnit, class javaClass, method javaMethod, cm, mm mappingFact) (map[string]contractcatalog.Response, []string, bool) {
	statuses := []string{"200"}
	partial := false
	for _, a := range append(append([]annotation(nil), class.annotations...), method.annotations...) {
		if resolveAnnotation(u, a.name) == "org.springframework.web.bind.annotation.ResponseStatus" {
			attrs, ok := s.annotationAttributes(a.args, s.constantsFor(u))
			if !ok {
				partial = true
				continue
			}
			values := attrs["code"]
			if len(values) == 0 {
				values = attrs["value"]
			}
			if len(values) == 1 {
				if mapped := httpStatus(values[0]); mapped != "" {
					statuses = []string{mapped}
				} else {
					partial = true
				}
			}
		}
	}
	typeName := method.returnType
	if inner, _, ok := resolvedGeneric(typeName, typeScope{sourceSet: u.sourceSet, packageName: u.packageName, owner: method.owner, imports: u.imports}, map[string]string{"org.springframework.http.ResponseEntity": "response"}); ok {
		typeName = inner
		statuses, partial = responseEntityStatuses(method.body)
	}
	content := map[string]contractcatalog.MediaType{}
	produces := mm.produces
	if len(produces) == 0 {
		produces = cm.produces
	}
	deps := []string{}
	if len(produces) > 0 && typeName != "void" && typeName != "ResponseEntity<Void>" {
		schema, dep, ok := s.schemaFor(typeName, typeScope{sourceSet: u.sourceSet, packageName: u.packageName, owner: method.owner, imports: u.imports, file: u.file, line: method.line, symbol: method.name}, 0, map[string]bool{})
		if ok {
			for _, media := range produces {
				content[media] = contractcatalog.MediaType{Schema: schema}
			}
			deps = append(deps, dep)
		} else {
			partial = true
			s.addDiagnostic(Diagnostic{Code: DiagnosticMissingType, Path: u.file, Line: method.line, Symbol: method.name})
		}
	}
	responses := map[string]contractcatalog.Response{}
	for _, status := range unique(statuses) {
		responses[status] = contractcatalog.Response{Content: content}
	}
	return responses, deps, partial
}

// responseEntityStatuses derives selectors only from actual return expressions and preserves every supported branch.
func responseEntityStatuses(body []token) ([]string, bool) {
	statuses, partial := []string{}, false
	for i := 0; i < len(body); i++ {
		if body[i].text != "return" {
			continue
		}
		end := i + 1
		for end < len(body) && body[end].text != ";" {
			end++
		}
		status, ok := responseStatusFromExpression(body[i+1 : end])
		if ok {
			statuses = append(statuses, status)
		} else {
			partial = true
		}
		i = end
	}
	if len(statuses) == 0 {
		statuses = []string{"default"}
		partial = true
	}
	if len(statuses) > 1 {
		partial = true
	}
	return unique(statuses), partial
}

// responseStatusFromExpression recognizes the closed ResponseEntity builder and constructor set in one return expression.
func responseStatusFromExpression(expression []token) (string, bool) {
	fixed := map[string]string{"ok": "200", "created": "201", "noContent": "204"}
	for i := 0; i+2 < len(expression); i++ {
		if expression[i].text == "ResponseEntity" && expression[i+1].text == "." {
			if status := fixed[expression[i+2].text]; status != "" {
				return status, true
			}
			if expression[i+2].text == "status" && i+3 < len(expression) && expression[i+3].text == "(" {
				end := matching(expression, i+3, "(", ")")
				if end > i+4 {
					status := httpStatus(joinType(expression[i+4 : end]))
					return status, status != ""
				}
			}
		}
		if expression[i].text == "new" && expression[i+1].text == "ResponseEntity" {
			open := findToken(expression, i+2, "(")
			if open < 0 {
				continue
			}
			end := matching(expression, open, "(", ")")
			if end <= open {
				continue
			}
			args := splitTopLevel(expression[open+1:end], ",")
			if len(args) > 0 {
				status := httpStatus(joinType(args[len(args)-1]))
				return status, status != ""
			}
		}
	}
	return "", false
}

// securityConstraints retains allowlisted declarations as inert neutral data with exact provenance.
func (s *analysisState) securityConstraints(sourceID string, u javaUnit, annotations []annotation, symbol string) []contractcatalog.DeclarativeConstraint {
	out := []contractcatalog.DeclarativeConstraint{}
	for _, a := range annotations {
		if err := s.ctx.Err(); err != nil {
			s.fatal = typedError(CategoryCancelled, "security", err)
			return out
		}
		fq := resolveAnnotation(u, a.name)
		kind := ""
		switch fq {
		case "org.springframework.security.access.prepost.PreAuthorize":
			kind = "pre_authorize"
		case "org.springframework.security.access.annotation.Secured":
			kind = "secured"
		case "jakarta.annotation.security.RolesAllowed":
			kind = "roles_allowed"
		default:
			continue
		}
		values, ok := literalValues(a.args, s.constantsFor(u))
		if !ok || len(values) == 0 {
			s.addDiagnostic(Diagnostic{Code: DiagnosticPartial, Path: u.file, Line: a.line, Symbol: symbol, Framework: fq})
			continue
		}
		if s.input.config.Projection.ValidateExcludedValues(values) != nil {
			s.omitted++
			s.addDiagnostic(Diagnostic{Code: DiagnosticPartial, Path: u.file, Line: a.line, Symbol: symbol, Framework: fq})
			continue
		}
		p := s.provenance(sourceID, u.file, a.line, symbol, fq)
		constraint := contractcatalog.DeclarativeConstraint{Kind: kind, Provenance: []contractcatalog.Provenance{p}}
		if kind == "pre_authorize" {
			constraint.Expression = values[0]
		} else {
			constraint.Values = unique(values)
		}
		out = append(out, constraint)
	}
	return out
}

// schemaFor resolves primitives, containers, arrays, enums, records and explicit local fields within budgets.
func (s *analysisState) schemaFor(name string, scope typeScope, depth int, active map[string]bool) (*contractcatalog.Schema, string, bool) {
	if s.ctx != nil && s.ctx.Err() != nil {
		s.fatal = typedError(CategoryCancelled, "schema", s.ctx.Err())
		return nil, "", false
	}
	s.schemaNodes++
	if s.schemaNodes > s.input.config.Limits.MaxSchemaNodes {
		s.fatal = typedError(CategoryLimitExceeded, "schema.nodes", nil)
		s.addDiagnostic(Diagnostic{Code: DiagnosticSchemaLimit, Path: scope.file, Line: scope.line, Symbol: scope.symbol})
		return nil, "", false
	}
	name = strings.TrimSpace(name)
	baseName := name
	if open := strings.Index(baseName, "<"); open >= 0 {
		baseName = baseName[:open]
	}
	_, localNamedType := s.types[scope.sourceSet+"\x00"+strings.TrimSuffix(scope.packageName, ".")+"."+baseName]
	if depth > s.input.config.Limits.MaxDepth {
		s.addDiagnostic(Diagnostic{Code: DiagnosticSchemaLimit, Path: scope.file, Line: scope.line, Symbol: scope.symbol})
		return nil, "", false
	}
	if strings.HasSuffix(name, "[]") {
		item, dep, ok := s.schemaFor(strings.TrimSuffix(name, "[]"), scope, depth+1, active)
		return &contractcatalog.Schema{Type: "array", Items: item}, dep, ok
	}
	if inner, outer, ok := resolvedGeneric(name, scope, map[string]string{"java.util.List": "array", "java.util.Set": "array", "java.util.Collection": "array", "java.util.Optional": "optional"}); ok && !localNamedType {
		item, dep, resolved := s.schemaFor(inner, scope, depth+1, active)
		if outer == "optional" && resolved {
			item.Nullable = true
			return item, dep, true
		}
		return &contractcatalog.Schema{Type: "array", Items: item}, dep, resolved
	}
	if inner, _, ok := resolvedGeneric(name, scope, map[string]string{"java.util.Map": "map"}); ok && !localNamedType {
		parts := splitGeneric(inner)
		if len(parts) == 2 && parts[0] == "String" {
			value, dep, resolved := s.schemaFor(parts[1], scope, depth+1, active)
			if resolved {
				return &contractcatalog.Schema{Type: "object", Properties: map[string]*contractcatalog.Schema{"*": value}}, dep, true
			}
		}
		return nil, "", false
	}
	primitiveName := name
	if !isJavaPrimitive(name) {
		primitiveName = resolveTypeIdentity(name, scope)
	}
	if _, local := s.types[scope.sourceSet+"\x00"+strings.TrimSuffix(scope.packageName, ".")+"."+name]; !local {
		if primitive, ok := primitiveSchema(primitiveName); ok {
			return primitive, "", true
		}
	}
	identity := resolveTypeIdentity(name, scope)
	if localNamedType {
		identity = strings.TrimSuffix(scope.packageName, ".") + "." + name
	}
	t, ok := s.types[scope.sourceSet+"\x00"+identity]
	if !ok && scope.owner != "" {
		identity = scope.packageName + "." + scope.owner + "." + name
		t, ok = s.types[scope.sourceSet+"\x00"+identity]
	}
	if !ok {
		return nil, "", false
	}
	key := t.packageName + "." + t.name
	if active[key] || t.customSerialization || t.unsupportedShape {
		code := DiagnosticMissingType
		if t.customSerialization || t.unsupportedShape {
			code = DiagnosticUnsupported
		}
		s.addDiagnostic(Diagnostic{Code: code, Path: t.file, Line: t.line, Symbol: t.name})
		return nil, t.file, false
	}
	active[key] = true
	defer delete(active, key)
	if t.kind == "enum" {
		values := make([]any, len(t.enumValues))
		for i, v := range t.enumValues {
			values[i] = v
		}
		return &contractcatalog.Schema{ID: key, Type: "string", Enum: values}, t.file, true
	}
	props := map[string]*contractcatalog.Schema{}
	required := []string{}
	for _, field := range t.fields {
		if s.ctx != nil {
			if err := s.ctx.Err(); err != nil {
				s.fatal = typedError(CategoryCancelled, "schema", err)
				return nil, t.file, false
			}
		}
		owner := ""
		if dot := strings.LastIndex(t.name, "."); dot >= 0 {
			owner = t.name[:dot]
		}
		child, _, resolved := s.schemaFor(field.typeName, typeScope{sourceSet: t.sourceSet, packageName: t.packageName, owner: owner, imports: t.imports, file: t.file, line: field.line, symbol: field.name}, depth+1, active)
		if !resolved {
			return nil, t.file, false
		}
		props[field.name] = child
		if field.required {
			required = append(required, field.name)
		}
	}
	return &contractcatalog.Schema{ID: key, Type: "object", Properties: props, Required: required}, t.file, true
}

// resolveTypeIdentity resolves one type through exact imports, one unambiguous wildcard, java.lang, or its local package.
func resolveTypeIdentity(name string, scope typeScope) string {
	base := strings.TrimSpace(name)
	if index := strings.Index(base, "<"); index >= 0 {
		base = base[:index]
	}
	base = strings.TrimSuffix(base, "[]")
	if strings.Contains(base, ".") {
		return base
	}
	if imported := scope.imports[base]; imported != "" && !strings.HasSuffix(imported, ".*") {
		return imported
	}
	javaLang := map[string]bool{"String": true, "Character": true, "Boolean": true, "Byte": true, "Short": true, "Integer": true, "Long": true, "Float": true, "Double": true}
	if javaLang[base] {
		return "java.lang." + base
	}
	if wildcard := scope.imports["*"]; wildcard != "" {
		return strings.TrimSuffix(wildcard, ".*") + "." + base
	}
	if scope.packageName == "" {
		return base
	}
	return scope.packageName + "." + base
}

// resolvedGeneric accepts generic semantics only when the wrapper resolves to an exact allowlisted FQCN.
func resolvedGeneric(value string, scope typeScope, allowed map[string]string) (string, string, bool) {
	open := strings.Index(value, "<")
	if open <= 0 || !strings.HasSuffix(value, ">") {
		return "", "", false
	}
	identity := resolveTypeIdentity(value[:open], scope)
	kind, ok := allowed[identity]
	if !ok {
		return "", "", false
	}
	return value[open+1 : len(value)-1], kind, true
}

// cloneImports isolates a Java unit's explicit name resolution table for later schema closure.
func cloneImports(values map[string]string) map[string]string {
	out := make(map[string]string, len(values))
	for k, v := range values {
		out[k] = v
	}
	return out
}

// primitiveSchema maps Java language and wrapper types without assuming content negotiation.
func primitiveSchema(name string) (*contractcatalog.Schema, bool) {
	switch name {
	case "java.lang.String", "java.lang.Character", "char":
		return &contractcatalog.Schema{Type: "string"}, true
	case "boolean", "java.lang.Boolean":
		return &contractcatalog.Schema{Type: "boolean"}, true
	case "byte", "short", "int", "long", "java.lang.Byte", "java.lang.Short", "java.lang.Integer", "java.lang.Long":
		return &contractcatalog.Schema{Type: "integer"}, true
	case "float", "double", "java.lang.Float", "java.lang.Double", "java.math.BigDecimal":
		return &contractcatalog.Schema{Type: "number"}, true
	}
	return nil, false
}

// isJavaPrimitive distinguishes language primitives from resolvable reference types.
func isJavaPrimitive(name string) bool {
	switch name {
	case "boolean", "byte", "short", "int", "long", "float", "double", "char":
		return true
	}
	return false
}

// provenance creates a source-relative revision-bound claim location.
func (s *analysisState) provenance(sourceID, file string, line int, symbol, framework string) contractcatalog.Provenance {
	return contractcatalog.Provenance{SourceID: sourceID, RepositoryID: s.input.config.RepositoryID, Revision: s.input.config.Revision, Class: contractcatalog.ClassDerived, SourceDigest: s.input.Digest(), AdapterVersion: AdapterVersion, SafeLocation: fmt.Sprintf("%s[%d]/%s/%s", file, line, symbol, framework), Confidence: s.input.config.Confidence, Completeness: s.input.config.Completeness}
}

// affectedOperations computes bidirectional changed controller/type closure without treating it as exhaustive catalog scope.
func (s *analysisState) affectedOperations() []contractcatalog.OperationKey {
	if len(s.input.config.ChangedPaths) == 0 {
		return nil
	}
	changed := map[string]bool{}
	for _, p := range s.input.config.ChangedPaths {
		changed[p] = true
	}
	out := []contractcatalog.OperationKey{}
	for key, deps := range s.dependencies {
		if err := s.ctx.Err(); err != nil {
			s.fatal = typedError(CategoryCancelled, "closure", err)
			return nil
		}
		for dep := range deps {
			if changed[dep] {
				out = append(out, key)
				break
			}
		}
	}
	sort.Slice(out, func(i, j int) bool { return opKey(out[i]) < opKey(out[j]) })
	return out
}

// catalogDiagnostics converts safe diagnostics without carrying raw parser messages.
func (s *analysisState) catalogDiagnostics() []contractcatalog.SourceDiagnostic {
	out := make([]contractcatalog.SourceDiagnostic, len(s.diagnostics))
	for i, d := range s.diagnostics {
		loc := ""
		if d.Path != "" {
			loc = fmt.Sprintf("%s[%d]", d.Path, d.Line)
		}
		out[i] = contractcatalog.SourceDiagnostic{Code: string(d.Code), SafeLocation: loc}
	}
	return out
}

// addDiagnostic caps, deduplicates and redacts diagnostic units deterministically.
func (s *analysisState) addDiagnostic(d Diagnostic) {
	if len(d.Path) > s.input.config.Limits.MaxStringBytes || len(d.Symbol) > s.input.config.Limits.MaxStringBytes || len(d.Framework) > s.input.config.Limits.MaxStringBytes {
		s.omitted++
		return
	}
	for _, forbidden := range s.input.config.Projection.ForbiddenValues {
		if forbidden != "" && (strings.Contains(d.Path, forbidden) || strings.Contains(d.Symbol, forbidden) || strings.Contains(d.Framework, forbidden)) {
			s.omitted++
			return
		}
	}
	for _, existing := range s.diagnostics {
		if existing == d {
			return
		}
	}
	if len(s.diagnostics) >= s.input.config.Limits.MaxDiagnostics {
		s.omitted++
		return
	}
	s.diagnostics = append(s.diagnostics, d)
	sort.Slice(s.diagnostics, func(i, j int) bool { return fmt.Sprint(s.diagnostics[i]) < fmt.Sprint(s.diagnostics[j]) })
}

// resolveAnnotation requires an exact FQCN or an explicit import from the closed allowlist.
func resolveAnnotation(u javaUnit, name string) string {
	if strings.Contains(name, ".") {
		return name
	}
	if exact := u.imports[name]; exact != "" {
		return exact
	}
	if wildcard := u.imports["*"]; wildcard != "" {
		return strings.TrimSuffix(wildcard, ".*") + "." + name
	}
	return ""
}

// crossPaths forms normalized class/method path combinations under a prechecked product budget.
func crossPaths(prefixes, suffixes []string, maximum int) []string {
	if len(prefixes)*len(suffixes) > maximum {
		return nil
	}
	out := []string{}
	for _, a := range prefixes {
		for _, b := range suffixes {
			joined := "/" + strings.Trim(strings.TrimSpace(a), "/")
			if strings.Trim(b, "/") != "" {
				joined += "/" + strings.Trim(b, "/")
			}
			joined = strings.ReplaceAll(joined, "//", "/")
			out = append(out, joined)
		}
	}
	return unique(out)
}

// validRoute rejects unresolved placeholders outside Spring path variables and every non-absolute path.
func validRoute(value string) bool {
	return strings.HasPrefix(value, "/") && !strings.ContainsAny(value, "$()") && !strings.Contains(value, "..")
}

// intersect computes the deterministic method intersection for nested RequestMapping declarations.
func intersect(a, b []string) []string {
	set := map[string]bool{}
	for _, v := range a {
		set[v] = true
	}
	out := []string{}
	for _, v := range b {
		if set[v] {
			out = append(out, v)
		}
	}
	return unique(out)
}

// unique sorts and deduplicates set-like string values.
func unique(values []string) []string {
	sort.Strings(values)
	out := values[:0]
	for _, v := range values {
		if len(out) == 0 || out[len(out)-1] != v {
			out = append(out, v)
		}
	}
	return out
}

// equalStrings compares alias values after preserving their source order.
func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// opKey serializes an operation key for deterministic ordering.
func opKey(k contractcatalog.OperationKey) string { return k.Method + "\x00" + k.Path }

// genericInner extracts one exact generic wrapper payload.
func genericInner(value, outer string) (string, bool) {
	prefix := outer + "<"
	if strings.HasPrefix(value, prefix) && strings.HasSuffix(value, ">") {
		return value[len(prefix) : len(value)-1], true
	}
	return "", false
}

// genericInnerAny extracts a payload from one of a closed set of generic wrappers.
func genericInnerAny(value string, outers []string) (string, bool) {
	for _, outer := range outers {
		if inner, ok := genericInner(value, outer); ok {
			return inner, true
		}
	}
	return "", false
}

// splitGeneric separates top-level generic arguments without resolving external types.
func splitGeneric(value string) []string {
	depth, start := 0, 0
	out := []string{}
	for i, r := range value {
		switch r {
		case '<':
			depth++
		case '>':
			depth--
		case ',':
			if depth == 0 {
				out = append(out, value[start:i])
				start = i + 1
			}
		}
	}
	return append(out, value[start:])
}

// httpStatus maps only closed Spring status constants or exact three-digit literals.
func httpStatus(value string) string {
	value = strings.TrimSpace(value)
	if n, err := strconv.Atoi(value); err == nil && n >= 100 && n <= 599 {
		return strconv.Itoa(n)
	}
	value = strings.TrimPrefix(value, "HttpStatus.")
	values := map[string]string{"OK": "200", "CREATED": "201", "NO_CONTENT": "204", "BAD_REQUEST": "400", "UNAUTHORIZED": "401", "FORBIDDEN": "403", "NOT_FOUND": "404", "CONFLICT": "409", "INTERNAL_SERVER_ERROR": "500"}
	return values[value]
}
