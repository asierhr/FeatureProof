package contractcatalog

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
)

// ClaimsForOperation derives field-granular claims from one canonical neutral operation.
func ClaimsForOperation(operation OperationDescriptor, provenance Provenance) []Claim {
	operation = canonicalOperation(operation)
	claims := []Claim{structuralClaim(ClaimOperationIdentity, "identity", struct {
		Key       OperationKey
		LogicalID string
	}{operation.Key, operation.LogicalID}, provenance)}
	for index, parameter := range operation.Parameters {
		field := fmt.Sprintf("parameters/%d", index)
		claims = append(claims, structuralClaim(ClaimSchema, field, parameter, provenance))
		claims = appendSchemaClaims(claims, field+"/schema", parameter.Schema, provenance)
		for _, media := range sortedMediaKeys(parameter.Content) {
			claims = appendSchemaClaims(claims, field+"/content/"+media+"/schema", parameter.Content[media].Schema, provenance)
		}
	}
	claims = append(claims, structuralClaim(ClaimSchema, "request_body", operation.RequestBody, provenance))
	if operation.RequestBody != nil {
		for _, media := range sortedMediaKeys(operation.RequestBody.Content) {
			claims = appendSchemaClaims(claims, "request_body/content/"+media+"/schema", operation.RequestBody.Content[media].Schema, provenance)
		}
	}
	for index, requirement := range operation.Security {
		claims = append(claims, structuralClaim(ClaimSecurity, fmt.Sprintf("security/%d", index), requirement, provenance))
		for _, name := range sortedStringSliceMapKeys(requirement.Schemes) {
			claims = append(claims, structuralClaim(ClaimSecurity, fmt.Sprintf("security/%d/schemes/%s", index, name), requirement.Schemes[name], provenance))
		}
	}
	if len(operation.Security) == 0 {
		claims = append(claims, structuralClaim(ClaimSecurity, "security", []SecurityRequirement{}, provenance))
	}
	for index, constraint := range operation.DeclarativeConstraints {
		claims = append(claims, structuralClaim(ClaimSecurity, fmt.Sprintf("declarative_constraints/%d", index), constraint, provenance))
	}
	for _, name := range sortedSecuritySchemeKeys(operation.SecuritySchemes) {
		claims = append(claims, structuralClaim(ClaimSecurity, "security_schemes/"+name, operation.SecuritySchemes[name], provenance))
	}
	selectors := make([]string, 0, len(operation.Responses))
	for selector := range operation.Responses {
		selectors = append(selectors, selector)
	}
	sort.Strings(selectors)
	for _, selector := range selectors {
		response, field := operation.Responses[selector], "responses/"+selector
		claims = append(claims, structuralClaim(ClaimResponse, field, response, provenance))
		for _, header := range sortedHeaderKeys(response.Headers) {
			headerField := field + "/headers/" + header
			claims = append(claims, structuralClaim(ClaimResponse, headerField, response.Headers[header], provenance))
			claims = appendSchemaClaims(claims, headerField+"/schema", response.Headers[header].Schema, provenance)
			for _, media := range sortedMediaKeys(response.Headers[header].Content) {
				claims = appendSchemaClaims(claims, headerField+"/content/"+media+"/schema", response.Headers[header].Content[media].Schema, provenance)
			}
		}
		for _, media := range sortedMediaKeys(response.Content) {
			claims = appendSchemaClaims(claims, field+"/content/"+media+"/schema", response.Content[media].Schema, provenance)
		}
	}
	sort.Slice(claims, func(i, j int) bool {
		return string(claims[i].Kind)+claims[i].Field < string(claims[j].Kind)+claims[j].Field
	})
	return claims
}

// appendSchemaClaims records every reachable schema node separately for exact provenance and policy evaluation.
func appendSchemaClaims(claims []Claim, field string, schema *Schema, provenance Provenance) []Claim {
	if schema == nil {
		return claims
	}
	claims = append(claims, structuralClaim(ClaimSchema, field, schema, provenance))
	for _, name := range sortedSchemaKeys(schema.Properties) {
		claims = appendSchemaClaims(claims, field+"/properties/"+name, schema.Properties[name], provenance)
	}
	claims = appendSchemaClaims(claims, field+"/items", schema.Items, provenance)
	for index, member := range schema.AllOf {
		claims = appendSchemaClaims(claims, fmt.Sprintf("%s/allOf/%d", field, index), member, provenance)
	}
	return claims
}

// structuralClaim fingerprints canonical structure and derives an exact relative structural location.
func structuralClaim(kind ClaimKind, field string, value any, provenance Provenance) Claim {
	raw, _ := json.Marshal(value)
	sum := sha256.Sum256(raw)
	provenance.SafeLocation += "/" + field
	return Claim{Kind: kind, Field: field, Fingerprint: hex.EncodeToString(sum[:]), Provenance: []Provenance{provenance}, Confidence: provenance.Confidence, Completeness: provenance.Completeness}
}

// sortedMediaKeys returns deterministic media-type keys.
func sortedMediaKeys(values map[string]MediaType) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

// sortedHeaderKeys returns deterministic response-header keys.
func sortedHeaderKeys(values map[string]Header) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

// sortedSchemaKeys returns deterministic schema-property keys.
func sortedSchemaKeys(values map[string]*Schema) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

// sortedStringSliceMapKeys returns deterministic keys for security requirement maps.
func sortedStringSliceMapKeys(values map[string][]string) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

// sortedSecuritySchemeKeys returns deterministic security-scheme names.
func sortedSecuritySchemeKeys(values map[string]SecurityScheme) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}
