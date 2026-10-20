package contractcatalog

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"sort"
	"strings"
)

// catalogProjection is the sole authority-bearing representation used by catalog validation and hashing.
func catalogProjection(operations []OperationDescriptor, schemes map[string]SecurityScheme, sources []SourceSummary, conflicts []Conflict, omissions OmissionCounts, diagnostics []SourceDiagnostic, affected []OperationKey) any {
	return struct {
		Version            string                    `json:"version"`
		Operations         []OperationDescriptor     `json:"operations"`
		SecuritySchemes    map[string]SecurityScheme `json:"security_schemes,omitempty"`
		Sources            []SourceSummary           `json:"sources"`
		Conflicts          []Conflict                `json:"conflicts"`
		Omissions          OmissionCounts            `json:"omissions"`
		Diagnostics        []SourceDiagnostic        `json:"diagnostics,omitempty"`
		AffectedOperations []OperationKey            `json:"affected_operations,omitempty"`
	}{VersionV1, cloneOperations(operations), cloneSecuritySchemes(schemes), append([]SourceSummary(nil), sources...), cloneConflicts(conflicts), omissions, append([]SourceDiagnostic(nil), diagnostics...), append([]OperationKey(nil), affected...)}
}

// digestBytes computes the lowercase SHA-256 identity used for every immutable binding.
func digestBytes(raw []byte) string { sum := sha256.Sum256(raw); return hex.EncodeToString(sum[:]) }

// canonicalOperation normalizes every set-like collection recursively before equality or hashing.
func canonicalOperation(operation OperationDescriptor) OperationDescriptor {
	operation = cloneOperation(operation)
	operation.Key.Method = strings.ToUpper(operation.Key.Method)
	for index := range operation.Parameters {
		canonicalizeParameter(&operation.Parameters[index])
	}
	sort.Slice(operation.Parameters, func(i, j int) bool {
		return operation.Parameters[i].In+"\x00"+operation.Parameters[i].Name < operation.Parameters[j].In+"\x00"+operation.Parameters[j].Name
	})
	if operation.RequestBody != nil {
		canonicalizeContent(operation.RequestBody.Content)
	}
	for selector, response := range operation.Responses {
		for name, header := range response.Headers {
			canonicalizeHeader(&header)
			response.Headers[name] = header
		}
		canonicalizeContent(response.Content)
		operation.Responses[strings.ToUpper(selector)] = response
		if strings.ToUpper(selector) != selector {
			delete(operation.Responses, selector)
		}
	}
	for index := range operation.Security {
		for name, scopes := range operation.Security[index].Schemes {
			operation.Security[index].Schemes[name] = sortedUniqueStrings(scopes)
		}
	}
	for index := range operation.DeclarativeConstraints {
		operation.DeclarativeConstraints[index].Values = sortedUniqueStrings(operation.DeclarativeConstraints[index].Values)
		operation.DeclarativeConstraints[index].Provenance = uniqueProvenance(operation.DeclarativeConstraints[index].Provenance)
	}
	sort.Slice(operation.DeclarativeConstraints, func(i, j int) bool {
		a, _ := json.Marshal(operation.DeclarativeConstraints[i])
		b, _ := json.Marshal(operation.DeclarativeConstraints[j])
		return string(a) < string(b)
	})
	sort.Slice(operation.Security, func(i, j int) bool {
		a, _ := json.Marshal(operation.Security[i])
		b, _ := json.Marshal(operation.Security[j])
		return string(a) < string(b)
	})
	for name, scheme := range operation.SecuritySchemes {
		operation.SecuritySchemes[name] = canonicalSecurityScheme(scheme)
	}
	operation.Classes = uniqueClasses(operation.Classes)
	operation.Provenance = uniqueProvenance(operation.Provenance)
	return operation
}

// canonicalizeParameter normalizes nested schema and content collections.
func canonicalizeParameter(parameter *Parameter) {
	parameter.Schema = canonicalSchema(parameter.Schema)
	canonicalizeContent(parameter.Content)
}

// canonicalizeHeader normalizes nested response-header schemas and content.
func canonicalizeHeader(header *Header) {
	header.Schema = canonicalSchema(header.Schema)
	canonicalizeContent(header.Content)
}

// canonicalizeContent normalizes every schema reachable through a media map.
func canonicalizeContent(content map[string]MediaType) {
	for media, value := range content {
		value.Schema = canonicalSchema(value.Schema)
		content[media] = value
	}
}

// canonicalSchema recursively canonicalizes required, enum, properties and allOf semantics.
func canonicalSchema(schema *Schema) *Schema {
	if schema == nil {
		return nil
	}
	out := cloneSchema(schema)
	out.Required = sortedUniqueStrings(out.Required)
	for name, child := range out.Properties {
		out.Properties[name] = canonicalSchema(child)
	}
	out.Items = canonicalSchema(out.Items)
	for index := range out.AllOf {
		out.AllOf[index] = canonicalSchema(out.AllOf[index])
	}
	sort.Slice(out.AllOf, func(i, j int) bool {
		a, _ := json.Marshal(out.AllOf[i])
		b, _ := json.Marshal(out.AllOf[j])
		return string(a) < string(b)
	})
	sort.Slice(out.Enum, func(i, j int) bool {
		a, _ := json.Marshal(out.Enum[i])
		b, _ := json.Marshal(out.Enum[j])
		return string(a) < string(b)
	})
	return out
}

// cloneSchema isolates a schema graph before canonicalization.
func cloneSchema(schema *Schema) *Schema {
	if schema == nil {
		return nil
	}
	raw, _ := json.Marshal(schema)
	var out Schema
	_ = json.Unmarshal(raw, &out)
	return &out
}

// canonicalSecurityScheme normalizes OAuth scope maps without changing their contractual descriptions.
func canonicalSecurityScheme(scheme SecurityScheme) SecurityScheme { return scheme }

// cloneSecuritySchemes isolates the catalog-level security map.
func cloneSecuritySchemes(values map[string]SecurityScheme) map[string]SecurityScheme {
	raw, _ := json.Marshal(values)
	out := map[string]SecurityScheme{}
	_ = json.Unmarshal(raw, &out)
	return out
}

// cloneConflicts isolates nested conflict collections.
func cloneConflicts(values []Conflict) []Conflict {
	out := make([]Conflict, len(values))
	for index, value := range values {
		out[index] = value
		out[index].OperationKeys = append([]OperationKey(nil), value.OperationKeys...)
		out[index].Locations = append([]string(nil), value.Locations...)
	}
	return out
}
