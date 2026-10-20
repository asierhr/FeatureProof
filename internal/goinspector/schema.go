package goinspector

import (
	"go/ast"
	"go/token"
	"reflect"
	"strconv"
	"strings"

	"github.com/asierhr/featureProof/internal/contractcatalog"
)

// schemaProjectionTrace records every local declaration and field that materially contributed to a projected schema.
type schemaProjectionTrace struct {
	locations    []string
	dependencies []string
	paths        map[string][]string
}

// projectSchema returns a schema together with the exact local closure used for provenance and PR impact.
func (s *analysisState) projectSchema(name string) (*contractcatalog.Schema, bool, []string, []string, map[string][]string) {
	trace := &schemaProjectionTrace{paths: map[string][]string{}}
	schema, complete := s.schemaForNameTrace(name, map[string]bool{}, 0, trace)
	return schema, complete, uniqueStrings(trace.locations), uniqueStrings(trace.dependencies), trace.paths
}

// schemaForName projects one locally declared type under cycle, depth, and type-node budgets.
func (s *analysisState) schemaForName(name string, active map[string]bool, depth int) (*contractcatalog.Schema, bool) {
	return s.schemaForNameTrace(name, active, depth, nil)
}

// schemaForNameTrace projects a named type and records its declaration before descending into its structural fields.
func (s *analysisState) schemaForNameTrace(name string, active map[string]bool, depth int, trace *schemaProjectionTrace) (*contractcatalog.Schema, bool) {
	if err := s.ctx.Err(); err != nil {
		return nil, false
	}
	name = strings.TrimPrefix(name, "*")
	pkg := ""
	if dot := strings.LastIndex(name, "."); dot > 0 {
		pkg, name = name[:dot], name[dot+1:]
	}
	key := name
	if pkg != "" {
		key = pkg + "." + name
	}
	if active[key] || depth >= s.input.config.Limits.MaxDepth {
		s.addDiagnostic(Diagnostic{Code: DiagnosticSchemaLimit, Symbol: boundedSymbol(name)})
		return nil, false
	}
	if s.customJSON[key] {
		s.addDiagnostic(Diagnostic{Code: DiagnosticUnsupported, Symbol: boundedSymbol(name)})
		return nil, false
	}
	expression, ok := s.types[key]
	if !ok {
		schema, primitive := primitiveSchema(name)
		if !primitive {
			s.addDiagnostic(Diagnostic{Code: DiagnosticMissingType, Symbol: boundedSymbol(name)})
			return nil, false
		}
		return schema, true
	}
	s.typesVisited++
	if s.typesVisited > s.input.config.Limits.MaxTypes {
		s.addDiagnostic(Diagnostic{Code: DiagnosticSchemaLimit, Symbol: boundedSymbol(name)})
		return nil, false
	}
	active[key] = true
	defer delete(active, key)
	if trace != nil {
		s.addSchemaTrace(trace, expression.Pos(), key)
	}
	return s.schemaForExprInTrace(expression, pkg, key, active, depth+1, trace)
}

// schemaForExpr translates only the structural Go subset that the neutral catalog can prove.
func (s *analysisState) schemaForExpr(expression ast.Expr, active map[string]bool, depth int) (*contractcatalog.Schema, bool) {
	return s.schemaForExprInTrace(expression, "", "schema", active, depth, nil)
}

// schemaForExprIn resolves unqualified local types only inside the declaration's package identity.
func (s *analysisState) schemaForExprIn(expression ast.Expr, pkg string, active map[string]bool, depth int) (*contractcatalog.Schema, bool) {
	return s.schemaForExprInTrace(expression, pkg, "schema", active, depth, nil)
}

// schemaForExprInTrace resolves structural children while carrying owner identity and material projection trace.
func (s *analysisState) schemaForExprInTrace(expression ast.Expr, pkg, owner string, active map[string]bool, depth int, trace *schemaProjectionTrace) (*contractcatalog.Schema, bool) {
	if depth > s.input.config.Limits.MaxDepth {
		return nil, false
	}
	switch value := expression.(type) {
	case *ast.Ident:
		name := value.Name
		if pkg != "" {
			name = pkg + "." + name
		}
		return s.schemaForNameTrace(name, active, depth, trace)
	case *ast.StarExpr:
		schema, complete := s.schemaForExprInTrace(value.X, pkg, owner, active, depth+1, trace)
		if schema != nil {
			schema.Nullable = true
		}
		return schema, complete
	case *ast.ArrayType:
		item, complete := s.schemaForExprInTrace(value.Elt, pkg, owner, active, depth+1, trace)
		if item == nil {
			return nil, false
		}
		return &contractcatalog.Schema{Type: "array", Items: item}, complete
	case *ast.MapType:
		key, ok := value.Key.(*ast.Ident)
		if !ok || key.Name != "string" {
			return nil, false
		}
		child, complete := s.schemaForExprInTrace(value.Value, pkg, owner, active, depth+1, trace)
		if child == nil {
			return nil, false
		}
		return &contractcatalog.Schema{Type: "object", Items: child}, complete
	case *ast.StructType:
		properties := map[string]*contractcatalog.Schema{}
		required := []string{}
		complete := true
		for _, field := range value.Fields.List {
			if len(field.Names) == 0 {
				complete = false
				continue
			}
			for _, fieldName := range field.Names {
				if !fieldName.IsExported() {
					continue
				}
				jsonName, omit, optional := jsonField(fieldName.Name, field.Tag)
				if omit {
					continue
				}
				childTrace := trace
				if trace != nil {
					childTrace = &schemaProjectionTrace{paths: map[string][]string{}}
				}
				child, childComplete := s.schemaForExprInTrace(field.Type, pkg, owner, active, depth+1, childTrace)
				if child == nil {
					complete = false
					continue
				}
				if trace != nil {
					trace.locations = append(trace.locations, childTrace.locations...)
					trace.dependencies = append(trace.dependencies, childTrace.dependencies...)
					location := s.addSchemaTrace(trace, fieldName.Pos(), owner+"."+fieldName.Name)
					trace.paths[jsonName] = append(trace.paths[jsonName], location)
					for childPath, childLocations := range childTrace.paths {
						joined := jsonName
						if childPath != "" {
							joined += "/" + childPath
						}
						trace.paths[joined] = append(trace.paths[joined], childLocations...)
					}
				}
				properties[jsonName] = child
				if !optional && !isPointer(field.Type) {
					required = append(required, jsonName)
				}
				complete = complete && childComplete
			}
		}
		return &contractcatalog.Schema{Type: "object", Properties: properties, Required: required}, complete
	case *ast.InterfaceType, *ast.IndexExpr, *ast.IndexListExpr, *ast.SelectorExpr:
		return nil, false
	}
	return nil, false
}

// addSchemaTrace appends a redacted declaration location and its repository-relative dependency exactly once at projection time.
func (s *analysisState) addSchemaTrace(trace *schemaProjectionTrace, position token.Pos, symbol string) string {
	value := s.fset.Position(position)
	location := s.safePublic(value.Filename, "path") + "/line-" + strconv.Itoa(value.Line) + "/" + s.safePublic(publicSymbol(symbol), "symbol") + "/schema"
	trace.dependencies = append(trace.dependencies, value.Filename)
	trace.locations = append(trace.locations, location)
	trace.paths[""] = append(trace.paths[""], location)
	return location
}

// primitiveSchema maps closed scalar names without consulting external type information.
func primitiveSchema(name string) (*contractcatalog.Schema, bool) {
	switch name {
	case "string":
		return &contractcatalog.Schema{Type: "string"}, true
	case "bool":
		return &contractcatalog.Schema{Type: "boolean"}, true
	case "int", "int8", "int16", "int32", "int64", "uint", "uint8", "uint16", "uint32", "uint64":
		return &contractcatalog.Schema{Type: "integer"}, true
	case "float32", "float64":
		return &contractcatalog.Schema{Type: "number"}, true
	case "byte":
		return &contractcatalog.Schema{Type: "integer", Format: "byte"}, true
	}
	return nil, false
}

// jsonField applies only standard json tag naming, omission, and omitempty semantics.
func jsonField(fallback string, tag *ast.BasicLit) (string, bool, bool) {
	if tag == nil {
		return fallback, false, false
	}
	raw := strings.Trim(tag.Value, "`")
	value := reflect.StructTag(raw).Get("json")
	if value == "" {
		return fallback, false, false
	}
	parts := strings.Split(value, ",")
	if parts[0] == "-" {
		return "", true, false
	}
	name := parts[0]
	if name == "" {
		name = fallback
	}
	optional := false
	for _, part := range parts[1:] {
		if part == "omitempty" {
			optional = true
		}
	}
	return name, false, optional
}

// isPointer ensures pointer fields are never presented as structurally required.
func isPointer(expression ast.Expr) bool { _, ok := expression.(*ast.StarExpr); return ok }
