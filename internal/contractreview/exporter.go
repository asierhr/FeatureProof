package contractreview

import (
	"context"
	"encoding/json"
	"sort"
	"strconv"
	"strings"

	"github.com/asierhr/featureProof/internal/contractcatalog"
)

// ExportInfo is trusted composition-owned metadata for a deterministic OpenAPI document.
type ExportInfo struct {
	Title   string `json:"title"`
	Version string `json:"version"`
}

// operationAudit projects field-granular claims and uncertainty onto the exact OpenAPI unit being serialized.
type operationAudit struct {
	operation   contractcatalog.OperationDescriptor
	conflicts   []contractcatalog.Conflict
	omissions   contractcatalog.OmissionCounts
	diagnostics []contractcatalog.SourceDiagnostic
}

// attach adds bounded audit extensions for the exact field or, for containers, its descendant claims.
func (a operationAudit) attach(target map[string]any, field string, descendants bool) {
	claims := []contractcatalog.Claim{}
	for _, claim := range a.operation.Claims {
		if claim.Field == field || descendants && strings.HasPrefix(claim.Field, field+"/") {
			claims = append(claims, deepClone(claim))
		}
	}
	if len(claims) > 0 {
		target["x-featureproof-claims"] = claims
		provenance := []contractcatalog.Provenance{}
		classes := []contractcatalog.SourceClass{}
		confidence, completeness := contractcatalog.ConfidenceHigh, contractcatalog.CompletenessComplete
		for _, claim := range claims {
			provenance = append(provenance, claim.Provenance...)
			confidence = weakerConfidence(confidence, claim.Confidence)
			completeness = weakerCompleteness(completeness, claim.Completeness)
			for _, value := range claim.Provenance {
				classes = append(classes, value.Class)
			}
		}
		target["x-featureproof-provenance"] = exportProvenance(uniqueProvenance(provenance))
		target["x-featureproof-source-classes"] = sortedUniqueClasses(classes)
		target["x-featureproof-confidence"] = confidence
		target["x-featureproof-completeness"] = completeness
	}
	conflicts := []contractcatalog.Conflict{}
	for _, conflict := range a.conflicts {
		for _, location := range conflict.Locations {
			if location == field || descendants && strings.HasPrefix(location, field+"/") {
				conflicts = append(conflicts, conflict)
				break
			}
		}
	}
	if len(conflicts) > 0 {
		target["x-featureproof-conflicts"] = conflicts
	}
	diagnostics := []contractcatalog.SourceDiagnostic{}
	for _, diagnostic := range a.diagnostics {
		if diagnosticApplies(diagnostic.SafeLocation, field, descendants) {
			diagnostics = append(diagnostics, diagnostic)
		}
	}
	if len(diagnostics) > 0 {
		target["x-featureproof-diagnostics"] = diagnostics
	}
}

// diagnosticApplies associates only explicitly located diagnostics with a structural unit.
func diagnosticApplies(location, field string, descendants bool) bool {
	location = strings.Trim(strings.ReplaceAll(location, "\\", "/"), "/")
	field = strings.Trim(field, "/")
	if location == "" || field == "" {
		return false
	}
	if strings.HasSuffix(location, "/"+field) || location == field {
		return true
	}
	return descendants && (strings.Contains(location, "/"+field+"/") || strings.HasPrefix(location, field+"/"))
}

// uniqueProvenance canonicalizes field lineage as a set without dropping any distinct authority tuple.
func uniqueProvenance(values []contractcatalog.Provenance) []contractcatalog.Provenance {
	canonical := canonicalProvenance(values, len(values))
	out := canonical[:0]
	for _, value := range canonical {
		if len(out) == 0 || !decisionJSONEqual(out[len(out)-1], value) {
			out = append(out, value)
		}
	}
	return out
}

// sortedUniqueClasses canonicalizes audit source roles without duplicating a class across claims.
func sortedUniqueClasses(values []contractcatalog.SourceClass) []contractcatalog.SourceClass {
	sort.Slice(values, func(i, j int) bool { return values[i] < values[j] })
	out := values[:0]
	for _, value := range values {
		if len(out) == 0 || out[len(out)-1] != value {
			out = append(out, value)
		}
	}
	return out
}

// Exporter projects only a current approved neutral catalog into bounded OpenAPI 3.1 JSON.
type Exporter struct{}

// Export creates repeatable bytes and never invents a representation for unsupported facts.
func (Exporter) Export(ctx context.Context, projection ApprovedProjection, info ExportInfo, limits Limits) ([]byte, string, error) {
	if err := limits.Validate(); err != nil {
		return nil, "", err
	}
	if err := cancellation(ctx, "export"); err != nil {
		return nil, "", err
	}
	if strings.TrimSpace(info.Title) == "" || strings.TrimSpace(info.Version) == "" || len(info.Title) > 128 || len(info.Version) > 64 {
		return nil, "", typedError(CategoryInvalidInput, "export.info", nil)
	}
	if err := validateProjectionBounds(ctx, projection, limits); err != nil {
		return nil, "", err
	}
	digest, err := projectionDigest(ctx, projection, limits.MaxBytes)
	if err != nil {
		return nil, "", err
	}
	if digest != projection.Binding.ProjectionDigest || projection.Binding.ReviewDigest == "" {
		return nil, "", typedError(CategoryInvalidated, "projection", nil)
	}
	document := map[string]any{"openapi": "3.1.0", "info": map[string]any{"title": info.Title, "version": info.Version}, "paths": map[string]any{}, "components": map[string]any{"securitySchemes": exportSchemes(projection)}, "x-featureproof-review": map[string]any{"review_id": projection.Binding.ReviewID, "review_digest": projection.Binding.ReviewDigest, "decision_digest": projection.Binding.DecisionDigest, "projection_digest": projection.Binding.ProjectionDigest}, "x-featureproof-omissions": projection.Omissions, "x-featureproof-diagnostics": projection.Diagnostics, "x-featureproof-conflicts": projection.Conflicts}
	paths := document["paths"].(map[string]any)
	for _, operation := range projection.Operations {
		if err := cancellation(ctx, "export.operations"); err != nil {
			return nil, "", err
		}
		pathItem, ok := paths[operation.Key.Path].(map[string]any)
		if !ok {
			pathItem = map[string]any{}
			paths[operation.Key.Path] = pathItem
		}
		pathItem[strings.ToLower(operation.Key.Method)] = exportOperation(operation, projection)
	}
	raw, err := canonicalBytes(ctx, document, limits.MaxBytes)
	if err != nil {
		return nil, "", err
	}
	return raw, digestBytes(raw), nil
}

// exportOperation maps one neutral operation and attaches its smallest applicable audit extensions.
func exportOperation(operation contractcatalog.OperationDescriptor, projection ApprovedProjection) map[string]any {
	audit := operationAudit{operation: operation, conflicts: conflictsForOperation(projection.Conflicts, operation.Key), omissions: projection.Omissions, diagnostics: projection.Diagnostics}
	out := map[string]any{"responses": exportResponses(operation.Responses, audit), "x-featureproof-provenance": exportProvenance(operation.Provenance), "x-featureproof-source-classes": operation.Classes, "x-featureproof-confidence": operation.Confidence, "x-featureproof-completeness": operation.Completeness, "x-featureproof-review": map[string]any{"review_id": projection.Binding.ReviewID, "approval_digest": projection.Binding.DecisionDigest, "projection_digest": projection.Binding.ProjectionDigest}}
	if operation.LogicalID != "" {
		out["operationId"] = operation.LogicalID
	}
	if len(audit.conflicts) > 0 {
		out["x-featureproof-conflicts"] = audit.conflicts
	}
	if len(operation.Claims) > 0 {
		out["x-featureproof-claims"] = operation.Claims
	}
	if len(operation.Parameters) > 0 {
		parameters := make([]any, 0, len(operation.Parameters))
		for index, parameter := range canonicalParameters(operation.Parameters) {
			entry := map[string]any{"name": parameter.Name, "in": parameter.In, "required": parameter.Required}
			field := "parameters/" + strconv.Itoa(index)
			audit.attach(entry, field, true)
			if parameter.Schema != nil {
				entry["schema"] = exportSchema(parameter.Schema, audit, field+"/schema")
			} else if len(parameter.Content) > 0 {
				entry["content"] = exportContent(parameter.Content, audit, field+"/content")
			}
			parameters = append(parameters, entry)
		}
		out["parameters"] = parameters
	}
	if operation.RequestBody != nil {
		body := map[string]any{"required": operation.RequestBody.Required, "content": exportContent(operation.RequestBody.Content, audit, "request_body/content")}
		audit.attach(body, "request_body", true)
		out["requestBody"] = body
	}
	if len(operation.Security) > 0 {
		out["security"] = exportSecurity(operation.Security, audit)
	}
	if len(operation.DeclarativeConstraints) > 0 {
		omissions := make([]any, 0, len(operation.DeclarativeConstraints))
		for range operation.DeclarativeConstraints {
			omissions = append(omissions, map[string]any{"code": "declarative_constraint", "location": "operation.security"})
		}
		out["x-featureproof-omissions"] = omissions
	}
	return out
}

// exportResponses converts response selectors, headers and media schemas without source payloads.
func exportResponses(values map[string]contractcatalog.Response, audit operationAudit) map[string]any {
	out := map[string]any{}
	for selector, response := range values {
		entry := map[string]any{"description": "FeatureProof approved response"}
		field := "responses/" + selector
		audit.attach(entry, field, true)
		if len(response.Headers) > 0 {
			headers := map[string]any{}
			for name, header := range response.Headers {
				h := map[string]any{"required": header.Required}
				headerField := field + "/headers/" + name
				audit.attach(h, headerField, true)
				if header.Schema != nil {
					h["schema"] = exportSchema(header.Schema, audit, headerField+"/schema")
				} else if len(header.Content) > 0 {
					h["content"] = exportContent(header.Content, audit, headerField+"/content")
				}
				headers[name] = h
			}
			entry["headers"] = headers
		}
		if len(response.Content) > 0 {
			entry["content"] = exportContent(response.Content, audit, field+"/content")
		}
		out[selector] = entry
	}
	return out
}

// exportContent maps negotiated media types while retaining only structural schemas.
func exportContent(values map[string]contractcatalog.MediaType, audit operationAudit, prefix string) map[string]any {
	out := map[string]any{}
	for name, media := range values {
		entry := map[string]any{}
		field := prefix + "/" + name
		audit.attach(entry, field, true)
		if media.Schema != nil {
			entry["schema"] = exportSchema(media.Schema, audit, field+"/schema")
		}
		out[name] = entry
	}
	return out
}

// exportSchema recursively maps the supported JSON Schema subset and exact nullable union.
func exportSchema(schema *contractcatalog.Schema, audit operationAudit, field string) map[string]any {
	if schema == nil {
		return map[string]any{}
	}
	out := map[string]any{}
	audit.attach(out, field, false)
	if schema.ID != "" {
		out["x-featureproof-neutral-schema-id"] = schema.ID
	}
	if schema.Type != "" {
		if schema.Nullable {
			out["type"] = []string{schema.Type, "null"}
		} else {
			out["type"] = schema.Type
		}
	}
	if schema.Format != "" {
		out["format"] = schema.Format
	}
	if len(schema.Required) > 0 {
		required := append([]string(nil), schema.Required...)
		sort.Strings(required)
		out["required"] = required
	}
	if len(schema.Properties) > 0 {
		properties := map[string]any{}
		for name, value := range schema.Properties {
			properties[name] = exportSchema(value, audit, field+"/properties/"+name)
		}
		out["properties"] = properties
	}
	if schema.Items != nil {
		out["items"] = exportSchema(schema.Items, audit, field+"/items")
	}
	if len(schema.AllOf) > 0 {
		values := make([]any, len(schema.AllOf))
		for i, value := range schema.AllOf {
			values[i] = exportSchema(value, audit, field+"/allOf/"+strconv.Itoa(i))
		}
		out["allOf"] = values
	}
	if len(schema.Enum) > 0 {
		out["enum"] = deepClone(schema.Enum)
	}
	if schema.MinProperties != nil {
		out["minProperties"] = *schema.MinProperties
	}
	if schema.ReadOnly {
		out["readOnly"] = true
	}
	if schema.WriteOnly {
		out["writeOnly"] = true
	}
	return out
}

// conflictsForOperation selects only conflicts affecting one operation for the smallest useful extension location.
func conflictsForOperation(values []contractcatalog.Conflict, key contractcatalog.OperationKey) []contractcatalog.Conflict {
	out := []contractcatalog.Conflict{}
	for _, value := range values {
		for _, affected := range value.OperationKeys {
			if affected == key {
				out = append(out, value)
				break
			}
		}
	}
	return out
}

// exportSecurity canonicalizes OR alternatives and scope sets for stable OpenAPI output.
func exportSecurity(values []contractcatalog.SecurityRequirement, audit operationAudit) []any {
	canonical := canonicalSecurity(values)
	out := make([]any, len(canonical))
	for i, value := range canonical {
		entry := map[string]any{}
		field := "security/" + strconv.Itoa(i)
		audit.attach(entry, field, false)
		schemeAudit := map[string]any{}
		for name, scopes := range value.Schemes {
			entry[name] = scopes
			metadata := map[string]any{}
			audit.attach(metadata, field+"/schemes/"+name, false)
			if len(metadata) > 0 {
				schemeAudit[name] = metadata
			}
		}
		if len(schemeAudit) > 0 {
			entry["x-featureproof-scheme-audit"] = schemeAudit
		}
		out[i] = entry
	}
	return out
}

// exportSchemes maps supported security scheme metadata without credentials.
func exportSchemes(projection ApprovedProjection) map[string]any {
	out := map[string]any{}
	for name, value := range projection.SecuritySchemes {
		entry := map[string]any{"type": value.Type}
		claims := []contractcatalog.Claim{}
		conflicts := []contractcatalog.Conflict{}
		diagnostics := []contractcatalog.SourceDiagnostic{}
		for _, operation := range projection.Operations {
			claims = append(claims, operation.Claims...)
			conflicts = append(conflicts, conflictsForOperation(projection.Conflicts, operation.Key)...)
			diagnostics = append(diagnostics, projection.Diagnostics...)
		}
		operationAudit{operation: contractcatalog.OperationDescriptor{Claims: claims}, conflicts: conflicts, diagnostics: diagnostics}.attach(entry, "security_schemes/"+name, false)
		if value.In != "" {
			entry["in"] = value.In
		}
		if value.ParameterName != "" {
			entry["name"] = value.ParameterName
		}
		if value.Scheme != "" {
			entry["scheme"] = value.Scheme
		}
		if value.OpenIDConnectURL != "" {
			entry["openIdConnectUrl"] = value.OpenIDConnectURL
		}
		if len(value.Flows) > 0 {
			flows := map[string]any{}
			for flowName, flow := range value.Flows {
				f := map[string]any{"scopes": flow.Scopes}
				if flow.AuthorizationURL != "" {
					f["authorizationUrl"] = flow.AuthorizationURL
				}
				if flow.TokenURL != "" {
					f["tokenUrl"] = flow.TokenURL
				}
				flows[flowName] = f
			}
			entry["flows"] = flows
		}
		out[name] = entry
	}
	return out
}

// exportProvenance emits bounded safe lineage identities in canonical order.
func exportProvenance(values []contractcatalog.Provenance) []any {
	values = canonicalProvenance(values, len(values))
	out := make([]any, len(values))
	for i, value := range values {
		out[i] = map[string]any{"source_id": value.SourceID, "repository_id": value.RepositoryID, "revision": value.Revision, "source_digest": value.SourceDigest, "adapter_version": value.AdapterVersion, "safe_location": value.SafeLocation, "class": value.Class}
	}
	return out
}

// validateExportShape provides a closed local structural sanity check before filesystem publication.
func validateExportShape(raw []byte) error {
	var document map[string]any
	if json.Unmarshal(raw, &document) != nil || document["openapi"] != "3.1.0" || document["paths"] == nil {
		return typedError(CategoryInternal, "export.document", nil)
	}
	return nil
}
