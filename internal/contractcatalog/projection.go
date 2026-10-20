package contractcatalog

import (
	"fmt"
	"net/url"
	"regexp"
	"strings"
)

// structuralName accepts only bounded identifiers that are safe to persist and show to a model.
var structuralName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:{}-]*$`)

// NewProjectionPolicy validates and freezes the complete structural redaction authority supplied by composition.
func NewProjectionPolicy(policy ProjectionPolicy) (ProjectionPolicy, error) {
	if !validProjectionPolicy(policy) {
		return ProjectionPolicy{}, typedError(CategoryInvalidSource, "projection.policy", nil)
	}
	return cloneProjectionPolicy(policy), nil
}

// ValidateSourceProjection applies the sealed structural policy to every public source-snapshot surface.
func ValidateSourceProjection(snapshot SourceSnapshot, policy ProjectionPolicy) error {
	if !validProjectionPolicy(policy) {
		return typedError(CategoryInvalidSource, "projection.policy", nil)
	}
	if (snapshot.Source.RepositoryID != "" && !safeProjectedName(snapshot.Source.RepositoryID, policy, false)) || (snapshot.Source.Revision != "" && !safeProjectedName(snapshot.Source.Revision, policy, false)) {
		return typedError(CategoryInvalidSource, "projection.source_identity", nil)
	}
	for _, operation := range snapshot.Operations {
		if err := validateProjection(operation, policy); err != nil {
			return err
		}
		for _, provenance := range operation.Provenance {
			if !safeProjectedText(provenance.SafeLocation, policy) || (provenance.RepositoryID != "" && !safeProjectedName(provenance.RepositoryID, policy, false)) || (provenance.Revision != "" && !safeProjectedName(provenance.Revision, policy, false)) {
				return typedError(CategoryInvalidSource, "projection.provenance", nil)
			}
		}
		for _, claim := range operation.Claims {
			for _, provenance := range claim.Provenance {
				if !safeProjectedText(provenance.SafeLocation, policy) || (provenance.RepositoryID != "" && !safeProjectedName(provenance.RepositoryID, policy, false)) || (provenance.Revision != "" && !safeProjectedName(provenance.Revision, policy, false)) {
					return typedError(CategoryInvalidSource, "projection.claim", nil)
				}
			}
		}
	}
	for _, diagnostic := range snapshot.Diagnostics {
		if !safeProjectedText(diagnostic.Code, policy) || !safeProjectedText(diagnostic.SafeLocation, policy) {
			return typedError(CategoryInvalidSource, "projection.diagnostic", nil)
		}
	}
	for _, key := range snapshot.AffectedOperations {
		if !safeProjectedPath(key.Path, policy) {
			return typedError(CategoryInvalidSource, "projection.affected", nil)
		}
	}
	return nil
}

// ValidateExcludedValues applies the sealed forbidden-value policy to source fields intentionally omitted from the neutral catalog.
func (p ProjectionPolicy) ValidateExcludedValues(values []string) error {
	if !validProjectionPolicy(p) {
		return typedError(CategoryInvalidSource, "projection.policy", nil)
	}
	for _, value := range values {
		if !safeProjectedText(value, p) {
			return typedError(CategoryInvalidSource, "projection.omitted", nil)
		}
	}
	return nil
}

// validProjectionPolicy rejects implicit projection defaults before any adapter is invoked.
func validProjectionPolicy(policy ProjectionPolicy) bool {
	if policy.MaxStringBytes <= 0 || len(policy.AllowedURLSchemes) == 0 || policy.SensitiveNames == nil || policy.SensitivePaths == nil || policy.SensitiveHeaderNames == nil || policy.ForbiddenValues == nil {
		return false
	}
	for _, scheme := range policy.AllowedURLSchemes {
		if scheme == "" || strings.ToLower(scheme) != scheme || !structuralName.MatchString(scheme) {
			return false
		}
	}
	for _, name := range append(append([]string(nil), policy.SensitiveNames...), policy.SensitiveHeaderNames...) {
		if name == "" || len(name) > policy.MaxStringBytes || strings.ContainsAny(name, "\x00\r\n") {
			return false
		}
	}
	for _, path := range policy.SensitivePaths {
		if path == "" || len(path) > policy.MaxStringBytes || !strings.HasPrefix(path, "/") || strings.ContainsAny(path, "?#\\\x00\r\n") {
			return false
		}
	}
	return true
}

// validateProjection enforces the trusted structural allowlist over every model-relevant string surface.
func validateProjection(operation OperationDescriptor, policy ProjectionPolicy) error {
	if !safeProjectedPath(operation.Key.Path, policy) || !safeProjectedName(operation.LogicalID, policy, true) {
		return typedError(CategoryInvalidSource, "projection.operation", nil)
	}
	for index, parameter := range operation.Parameters {
		if !safeProjectedName(parameter.Name, policy, false) || !safeProjectedName(parameter.In, policy, false) || strings.EqualFold(parameter.In, "header") && sensitiveHeader(parameter.Name, policy) {
			return typedError(CategoryInvalidSource, fmt.Sprintf("projection.parameters[%d]", index), nil)
		}
		if err := validateSchemaProjection(parameter.Schema, policy, "projection.parameter.schema"); err != nil {
			return err
		}
		for _, media := range sortedMediaKeys(parameter.Content) {
			if !safeProjectedMedia(media, policy) {
				return typedError(CategoryInvalidSource, "projection.media", nil)
			}
			if err := validateSchemaProjection(parameter.Content[media].Schema, policy, "projection.parameter.content"); err != nil {
				return err
			}
		}
	}
	if operation.RequestBody != nil {
		for _, media := range sortedMediaKeys(operation.RequestBody.Content) {
			if !safeProjectedMedia(media, policy) {
				return typedError(CategoryInvalidSource, "projection.media", nil)
			}
			if err := validateSchemaProjection(operation.RequestBody.Content[media].Schema, policy, "projection.request"); err != nil {
				return err
			}
		}
	}
	for _, requirement := range operation.Security {
		for name, scopes := range requirement.Schemes {
			if !safeProjectedName(name, policy, false) {
				return typedError(CategoryInvalidSource, "projection.security", nil)
			}
			for _, scope := range scopes {
				if !safeProjectedName(scope, policy, false) {
					return typedError(CategoryInvalidSource, "projection.scope", nil)
				}
			}
		}
	}
	for _, constraint := range operation.DeclarativeConstraints {
		if !safeProjectedName(constraint.Kind, policy, false) || !safeProjectedText(constraint.Expression, policy) {
			return typedError(CategoryInvalidSource, "projection.declarative_security", nil)
		}
		for _, value := range constraint.Values {
			if !safeProjectedText(value, policy) {
				return typedError(CategoryInvalidSource, "projection.declarative_security", nil)
			}
		}
		for _, provenance := range constraint.Provenance {
			if !safeProjectedText(provenance.SafeLocation, policy) {
				return typedError(CategoryInvalidSource, "projection.declarative_security", nil)
			}
		}
	}
	for name, scheme := range operation.SecuritySchemes {
		if !safeProjectedName(name, policy, false) || !safeProjectedName(scheme.Type, policy, false) || !safeProjectedName(scheme.In, policy, true) || !safeProjectedName(scheme.ParameterName, policy, true) || !safeProjectedName(scheme.Scheme, policy, true) || !safeProjectedURL(scheme.OpenIDConnectURL, policy) {
			return typedError(CategoryInvalidSource, "projection.scheme", nil)
		}
		for flowName, flow := range scheme.Flows {
			if !safeProjectedName(flowName, policy, false) {
				return typedError(CategoryInvalidSource, "projection.oauth.flow", nil)
			}
			if !safeProjectedURL(flow.AuthorizationURL, policy) || !safeProjectedURL(flow.TokenURL, policy) {
				return typedError(CategoryInvalidSource, "projection.oauth", nil)
			}
			for scope, description := range flow.Scopes {
				if !safeProjectedName(scope, policy, false) || !safeProjectedText(description, policy) {
					return typedError(CategoryInvalidSource, "projection.oauth.scope", nil)
				}
			}
		}
	}
	for selector, response := range operation.Responses {
		if !safeProjectedName(selector, policy, false) {
			return typedError(CategoryInvalidSource, "projection.response", nil)
		}
		for name, header := range response.Headers {
			if !safeProjectedName(name, policy, false) || sensitiveHeader(name, policy) {
				return typedError(CategoryInvalidSource, "projection.response.header", nil)
			}
			if err := validateSchemaProjection(header.Schema, policy, "projection.response.header.schema"); err != nil {
				return err
			}
		}
		for _, media := range sortedMediaKeys(response.Content) {
			if !safeProjectedMedia(media, policy) {
				return typedError(CategoryInvalidSource, "projection.media", nil)
			}
			if err := validateSchemaProjection(response.Content[media].Schema, policy, "projection.response.content"); err != nil {
				return err
			}
		}
	}
	return nil
}

// validateSchemaProjection recursively rejects sensitive names and scalar enum values before claims are retained.
func validateSchemaProjection(schema *Schema, policy ProjectionPolicy, location string) error {
	if schema == nil {
		return nil
	}
	if !safeProjectedName(schema.ID, policy, true) || !safeProjectedName(schema.Type, policy, true) || !safeProjectedName(schema.Format, policy, true) {
		return typedError(CategoryInvalidSource, location, nil)
	}
	for _, name := range schema.Required {
		if !safeProjectedName(name, policy, false) {
			return typedError(CategoryInvalidSource, location+".required", nil)
		}
	}
	for name, child := range schema.Properties {
		if !safeProjectedName(name, policy, false) {
			return typedError(CategoryInvalidSource, location+".properties", nil)
		}
		if err := validateSchemaProjection(child, policy, location+".properties"); err != nil {
			return err
		}
	}
	if err := validateSchemaProjection(schema.Items, policy, location+".items"); err != nil {
		return err
	}
	for _, child := range schema.AllOf {
		if err := validateSchemaProjection(child, policy, location+".allOf"); err != nil {
			return err
		}
	}
	for _, value := range schema.Enum {
		if text, ok := value.(string); ok && !safeProjectedText(text, policy) {
			return typedError(CategoryInvalidSource, location+".enum", nil)
		}
	}
	return nil
}

// safeProjectedPath accepts relative HTTP templates without query, fragment, authority or configured sensitive material.
func safeProjectedPath(value string, policy ProjectionPolicy) bool {
	if !strings.HasPrefix(value, "/") || strings.ContainsAny(value, "?#\\") || !safeProjectedText(value, policy) {
		return false
	}
	for _, sensitive := range policy.SensitivePaths {
		if strings.Contains(value, sensitive) {
			return false
		}
	}
	return true
}

// safeProjectedName validates optional or required structural identifiers.
func safeProjectedName(value string, policy ProjectionPolicy, optional bool) bool {
	if value == "" {
		return optional
	}
	return structuralName.MatchString(value) && safeProjectedText(value, policy)
}

// safeProjectedMedia validates bounded MIME-like structural keys.
func safeProjectedMedia(value string, policy ProjectionPolicy) bool {
	return value != "" && !strings.ContainsAny(value, "\r\n") && safeProjectedText(value, policy)
}

// safeProjectedURL allows only configured public schemes and rejects userinfo, query and fragment surfaces.
func safeProjectedURL(value string, policy ProjectionPolicy) bool {
	if value == "" {
		return true
	}
	parsed, err := url.Parse(value)
	if err != nil || parsed.IsAbs() && parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" || len(value) > policy.MaxStringBytes || strings.ContainsAny(value, "\x00\r\n") || containsForbidden(value, policy) {
		return false
	}
	for _, scheme := range policy.AllowedURLSchemes {
		if strings.EqualFold(parsed.Scheme, scheme) {
			return true
		}
	}
	return false
}

// safeProjectedText applies configured sensitive-name and exact forbidden-value rules without hard-coded canaries.
func safeProjectedText(value string, policy ProjectionPolicy) bool {
	if len(value) > policy.MaxStringBytes || strings.ContainsAny(value, "\x00\r\n") {
		return false
	}
	lower := strings.ToLower(value)
	for _, name := range policy.SensitiveNames {
		if name != "" && strings.Contains(lower, strings.ToLower(name)) {
			return false
		}
	}
	return !containsForbidden(value, policy)
}

// containsForbidden applies configured exact sensitive fragments across structural strings and URLs.
func containsForbidden(value string, policy ProjectionPolicy) bool {
	for _, forbidden := range policy.ForbiddenValues {
		if forbidden != "" && strings.Contains(value, forbidden) {
			return true
		}
	}
	return false
}

// sensitiveHeader applies the configured case-insensitive protected-header set.
func sensitiveHeader(value string, policy ProjectionPolicy) bool {
	for _, name := range policy.SensitiveHeaderNames {
		if strings.EqualFold(value, name) {
			return true
		}
	}
	return false
}
