package openapi

import (
	"errors"
	"strconv"
	"strings"

	jsonschema "github.com/santhosh-tekuri/jsonschema/v6"
)

// schemaValidator is the inspector-owned port for version-aware JSON Schema validation.
type schemaValidator interface{ Validate(any) error }

// deniedSchemaLoader guarantees compiled schemas cannot resolve network or filesystem URLs.
type deniedSchemaLoader struct{}

// Load rejects every external schema request at the adapter boundary.
func (deniedSchemaLoader) Load(string) (any, error) {
	return nil, errors.New("external schema loading is disabled")
}

// compileSchemaValidator compiles a resolved schema using the OpenAPI family's closest JSON Schema dialect.
func compileSchemaValidator(raw map[string]any, version string) (schemaValidator, error) {
	prepared := prepareResponseSchema(raw, strings.HasPrefix(version, "3.0."))
	compiler := jsonschema.NewCompiler()
	compiler.UseLoader(deniedSchemaLoader{})
	if strings.HasPrefix(version, "3.0.") {
		compiler.DefaultDraft(jsonschema.Draft4)
	} else {
		compiler.DefaultDraft(jsonschema.Draft2020)
	}
	const location = "urn:featureproof:response-schema"
	if err := compiler.AddResource(location, prepared); err != nil {
		return nil, err
	}
	return compiler.Compile(location)
}

// prepareResponseSchema clones schema data, applies 3.0 nullable semantics and removes response-side writeOnly obligations.
func prepareResponseSchema(raw any, openapi30 bool) any {
	switch value := raw.(type) {
	case []any:
		out := make([]any, len(value))
		for i, item := range value {
			out[i] = prepareResponseSchema(item, openapi30)
		}
		return out
	case map[string]any:
		if recursive, ok := value["x-recursive-ref"].(string); ok {
			return map[string]any{"$ref": recursive}
		}
		out := make(map[string]any, len(value))
		for key, item := range value {
			if key == "nullable" || key == "format" {
				continue
			}
			out[key] = prepareResponseSchema(item, openapi30)
		}
		if properties, ok := value["properties"].(map[string]any); ok {
			required := stringSet(value["required"])
			for name, property := range properties {
				if propertyMap, ok := property.(map[string]any); ok && boolValue(propertyMap["writeOnly"]) {
					delete(required, name)
				}
			}
			if len(required) > 0 {
				names := make([]any, 0, len(required))
				for _, name := range sortedKeys(required) {
					names = append(names, name)
				}
				out["required"] = names
			} else {
				delete(out, "required")
			}
		}
		if openapi30 && boolValue(value["nullable"]) {
			base := out
			return map[string]any{"anyOf": []any{base, map[string]any{"type": "null"}}}
		}
		return out
	default:
		return value
	}
}

// stringSet converts a schema required array into a deterministic membership map.
func stringSet(raw any) map[string]bool {
	out := map[string]bool{}
	if values, ok := raw.([]any); ok {
		for _, value := range values {
			if name, ok := value.(string); ok {
				out[name] = true
			}
		}
	}
	return out
}

// compiledSchemaViolations translates dependency diagnostics into value-free inspector violations.
func compiledSchemaViolations(validator schemaValidator, value any, location string) []Violation {
	if validator == nil {
		return nil
	}
	err := validator.Validate(value)
	if err == nil {
		return nil
	}
	var validation *jsonschema.ValidationError
	if !errors.As(err, &validation) {
		return []Violation{{Category: "schema_violation", Location: location, Keyword: "schema"}}
	}
	var out []Violation
	collectValidationErrors(validation, location, &out)
	return out
}

// collectValidationErrors flattens leaf failures using only instance locations and keyword names.
func collectValidationErrors(err *jsonschema.ValidationError, base string, out *[]Violation) {
	if len(err.Causes) > 0 {
		for _, cause := range err.Causes {
			collectValidationErrors(cause, base, out)
		}
		return
	}
	location := base
	for index := range err.InstanceLocation {
		location += "/segment[" + strconv.Itoa(index) + "]"
	}
	keyword := "schema"
	if err.ErrorKind != nil {
		path := err.ErrorKind.KeywordPath()
		if len(path) > 0 {
			keyword = path[len(path)-1]
		}
	}
	*out = append(*out, Violation{Category: "schema_violation", Location: location, Keyword: keyword})
}
