package specification

import (
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"strings"

	"github.com/asierhr/featureProof/internal/jsonpath"
)

// Category is a stable validation error classification.
type Category string

const (
	CategoryRequired           Category = "required"
	CategoryInvalidValue       Category = "invalid_value"
	CategoryDuplicateID        Category = "duplicate_id"
	CategoryUnknownReference   Category = "unknown_reference"
	CategoryUnknownField       Category = "unknown_field"
	CategorySyntax             Category = "syntax"
	CategoryUnsupportedVersion Category = "unsupported_version"
)

var (
	// Stable sentinels let callers branch on validation failures without
	// depending on human-readable error messages.
	ErrRequired           = errors.New("required field")
	ErrInvalidValue       = errors.New("invalid value")
	ErrDuplicateID        = errors.New("duplicate identifier")
	ErrUnknownReference   = errors.New("unknown reference")
	ErrUnknownField       = errors.New("unknown field")
	ErrSyntax             = errors.New("invalid syntax")
	ErrUnsupportedVersion = errors.New("unsupported version")
)

// ValidationError describes a rejected value without exposing adapter types.
type ValidationError struct {
	Category Category
	Field    string
	Line     int
	Column   int
	Cause    error
}

// Error renders an actionable validation failure for CLI and adapter callers.
func (e *ValidationError) Error() string {
	location := e.Field
	if location == "" && e.Line > 0 {
		location = fmt.Sprintf("line %d, column %d", e.Line, e.Column)
	}
	if location == "" {
		return fmt.Sprintf("validate specification: %s", e.Category)
	}
	return fmt.Sprintf("validate specification %s: %s", location, e.Category)
}

// Unwrap exposes the underlying parser or validation cause for diagnostics.
func (e *ValidationError) Unwrap() error { return e.Cause }

// Is maps the error category to FeatureProof's stable validation sentinels.
func (e *ValidationError) Is(target error) bool { return target == categorySentinel(e.Category) }

// categorySentinel translates an internal category into its public sentinel.
func categorySentinel(category Category) error {
	switch category {
	case CategoryRequired:
		return ErrRequired
	case CategoryInvalidValue:
		return ErrInvalidValue
	case CategoryDuplicateID:
		return ErrDuplicateID
	case CategoryUnknownReference:
		return ErrUnknownReference
	case CategoryUnknownField:
		return ErrUnknownField
	case CategorySyntax:
		return ErrSyntax
	case CategoryUnsupportedVersion:
		return ErrUnsupportedVersion
	default:
		return nil
	}
}

// UnsupportedVersionError reports a format version that cannot be decoded.
type UnsupportedVersionError struct {
	Received  int
	Supported []int
}

// Error reports the received contract version and the versions FeatureProof accepts.
func (e *UnsupportedVersionError) Error() string {
	return fmt.Sprintf("validate specification version: unsupported_version received=%d supported=%v", e.Received, e.Supported)
}

// Is makes unsupported versions distinguishable through errors.Is.
func (e *UnsupportedVersionError) Is(target error) bool { return target == ErrUnsupportedVersion }

var identifierPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)

// validationError builds the domain error returned by deterministic validation rules.
func validationError(category Category, field string, cause error) error {
	return &ValidationError{Category: category, Field: field, Cause: cause}
}

// requiredString enforces presence before more specific string rules run.
func requiredString(value, field string) error {
	if value == "" {
		return validationError(CategoryRequired, field, nil)
	}
	return nil
}

// validID checks the stable identifier syntax shared by specification entities.
func validID(value, field string) error {
	if err := requiredString(value, field); err != nil {
		return err
	}
	if !identifierPattern.MatchString(value) {
		return validationError(CategoryInvalidValue, field, nil)
	}
	return nil
}

// validDescription rejects missing or whitespace-only human descriptions.
func validDescription(value, field string) error {
	if value == "" {
		return validationError(CategoryRequired, field, nil)
	}
	if strings.TrimSpace(value) == "" {
		return validationError(CategoryInvalidValue, field, nil)
	}
	return nil
}

// Validate checks the internal specification in contractual field order.
func Validate(spec Specification) error {
	if spec.Version == 0 {
		return validationError(CategoryRequired, "version", nil)
	}
	if spec.Version != 1 && spec.Version != 2 {
		return validationError(CategoryInvalidValue, "version", nil)
	}
	if err := validID(spec.Feature.ID, "feature.id"); err != nil {
		return err
	}
	if err := validDescription(spec.Feature.Description, "feature.description"); err != nil {
		return err
	}
	if err := validID(spec.Target.ID, "target.id"); err != nil {
		return err
	}
	if err := requiredString(spec.Target.BaseURL, "target.base_url"); err != nil {
		return err
	}
	parsedURL, err := url.Parse(spec.Target.BaseURL)
	if err != nil || (parsedURL.Scheme != "http" && parsedURL.Scheme != "https") || parsedURL.Host == "" || parsedURL.User != nil || parsedURL.RawQuery != "" || parsedURL.Fragment != "" {
		return validationError(CategoryInvalidValue, "target.base_url", err)
	}
	if len(spec.Constraints) == 0 {
		return validationError(CategoryRequired, "constraints", nil)
	}
	seen := make(map[string]struct{}, len(spec.Constraints))
	for index, constraint := range spec.Constraints {
		prefix := fmt.Sprintf("constraints[%d]", index)
		if err := validID(constraint.ID, prefix+".id"); err != nil {
			return err
		}
		if _, exists := seen[constraint.ID]; exists {
			return validationError(CategoryDuplicateID, prefix+".id", nil)
		}
		seen[constraint.ID] = struct{}{}
		if err := validDescription(constraint.Description, prefix+".description"); err != nil {
			return err
		}
		if err := requiredString(constraint.TargetID, prefix+".target_id"); err != nil {
			return err
		}
		if constraint.TargetID != spec.Target.ID {
			return validationError(CategoryUnknownReference, prefix+".target_id", nil)
		}
	}
	if spec.Limits.MaxSteps <= 0 {
		return validationError(CategoryInvalidValue, "limits.max_steps", nil)
	}
	if spec.Limits.MaxToolCalls <= 0 {
		return validationError(CategoryInvalidValue, "limits.max_tool_calls", nil)
	}
	if spec.Limits.MaxRetries < 0 {
		return validationError(CategoryInvalidValue, "limits.max_retries", nil)
	}
	if spec.Version == 1 {
		if len(spec.Identities) != 0 {
			return validationError(CategoryUnknownField, "identities", nil)
		}
		return nil
	}
	if err := validateIdentities(spec); err != nil {
		return err
	}
	return nil
}

// validateIdentities proves every v2 identity is unique, target-bound, non-secret, and limited to complete operations.
func validateIdentities(spec Specification) error {
	seenIDs := make(map[string]struct{}, len(spec.Identities))
	for i, identity := range spec.Identities {
		prefix := fmt.Sprintf("identities[%d]", i)
		if err := validID(identity.ID, prefix+".id"); err != nil {
			return err
		}
		if _, exists := seenIDs[identity.ID]; exists {
			return validationError(CategoryDuplicateID, prefix+".id", nil)
		}
		seenIDs[identity.ID] = struct{}{}
		if identity.TargetID != spec.Target.ID {
			return validationError(CategoryUnknownReference, prefix+".target_id", nil)
		}
		if !opaqueCredentialReference(identity.CredentialRef) {
			return validationError(CategoryInvalidValue, prefix+".credential_ref", nil)
		}
		if err := validateMechanism(identity.Mechanism, prefix+".mechanism"); err != nil {
			return err
		}
		if len(identity.Operations) == 0 {
			return validationError(CategoryRequired, prefix+".operations", nil)
		}
		seenOperations := map[string]struct{}{}
		for j, operation := range identity.Operations {
			path := fmt.Sprintf("%s.operations[%d]", prefix, j)
			if operation.Capability != "http.request" {
				return validationError(CategoryInvalidValue, path+".capability", nil)
			}
			if !validHTTPMethod(operation.Method) {
				return validationError(CategoryInvalidValue, path+".method", nil)
			}
			if !validOperationTemplate(operation.Path) {
				return validationError(CategoryInvalidValue, path+".path", nil)
			}
			key := operation.Capability + "\x00" + operation.Method + "\x00" + operation.Path
			if _, exists := seenOperations[key]; exists {
				return validationError(CategoryDuplicateID, path, nil)
			}
			seenOperations[key] = struct{}{}
		}
	}
	return nil
}

// opaqueCredentialReference accepts only identifier-shaped composition keys and rejects paths, URLs, and environment syntax.
func opaqueCredentialReference(value string) bool {
	return identifierPattern.MatchString(value) && !strings.ContainsAny(value, "/\\:$")
}

// validateMechanism enforces the closed bearer/header/cookie union and non-secret configuration.
func validateMechanism(mechanism AuthenticationMechanism, field string) error {
	switch mechanism.Type {
	case "bearer":
		if mechanism.Name != "" {
			return validationError(CategoryInvalidValue, field+".name", nil)
		}
	case "header", "cookie":
		if !validHTTPToken(mechanism.Name) {
			return validationError(CategoryInvalidValue, field+".name", nil)
		}
		lower := strings.ToLower(mechanism.Name)
		if mechanism.Type == "header" && reservedAuthenticationHeader(lower) {
			return validationError(CategoryInvalidValue, field+".name", nil)
		}
	default:
		return validationError(CategoryInvalidValue, field+".type", nil)
	}
	seenPaths := map[string]bool{}
	for i, path := range mechanism.SensitiveJSONPaths {
		tokens, err := jsonpath.Parse(path)
		if err != nil || len(tokens) == 0 {
			return validationError(CategoryInvalidValue, fmt.Sprintf("%s.sensitive_json_paths[%d]", field, i), nil)
		}
		canonical := jsonpath.Join(tokens)
		if seenPaths[canonical] {
			return validationError(CategoryDuplicateID, fmt.Sprintf("%s.sensitive_json_paths[%d]", field, i), nil)
		}
		seenPaths[canonical] = true
	}
	return nil
}

// validHTTPMethod accepts canonical token methods so authorization comparisons cannot disagree on normalization.
func validHTTPMethod(value string) bool {
	return value != "" && value == strings.ToUpper(value) && validHTTPToken(value)
}

// validHTTPToken implements the ASCII token grammar used by safe header, cookie, and method names.
func validHTTPToken(value string) bool {
	if value == "" {
		return false
	}
	for _, r := range value {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || strings.ContainsRune("!#$%&'*+-.^_`|~", r)) {
			return false
		}
	}
	return true
}

// reservedAuthenticationHeader blocks caller-controlled authentication, framing, and hop-by-hop header names.
func reservedAuthenticationHeader(name string) bool {
	switch name {
	case "authorization", "cookie", "set-cookie", "proxy-authorization", "connection", "keep-alive", "transfer-encoding", "upgrade", "host", "content-length", "te", "trailer":
		return true
	default:
		return false
	}
}

// validOperationTemplate accepts absolute segment templates while rejecting traversal, ambiguous escapes, and duplicate parameters.
func validOperationTemplate(path string) bool {
	if path == "/" {
		return true
	}
	if path == "" || path[0] != '/' || strings.ContainsAny(path, "?#\\") || strings.Contains(path, "//") || strings.Contains(path, "%") {
		return false
	}
	seen := map[string]struct{}{}
	for _, segment := range strings.Split(strings.TrimPrefix(path, "/"), "/") {
		if segment == "." || segment == ".." || segment == "" {
			return false
		}
		if strings.HasPrefix(segment, "{") || strings.HasSuffix(segment, "}") {
			if len(segment) < 3 || segment[0] != '{' || segment[len(segment)-1] != '}' || strings.Contains(segment[1:len(segment)-1], "{") {
				return false
			}
			name := segment[1 : len(segment)-1]
			if !identifierPattern.MatchString(name) {
				return false
			}
			if _, exists := seen[name]; exists {
				return false
			}
			seen[name] = struct{}{}
		} else if strings.ContainsAny(segment, "{}") {
			return false
		}
	}
	return true
}
