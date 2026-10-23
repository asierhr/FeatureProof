package specification

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
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
	if spec.Version != 1 && spec.Version != 2 && spec.Version != 3 {
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
		if len(spec.Fixtures) != 0 {
			return validationError(CategoryUnknownField, "fixtures", nil)
		}
		return nil
	}
	if err := validateIdentities(spec); err != nil {
		return err
	}
	if spec.Version == 2 && len(spec.Fixtures) != 0 {
		return validationError(CategoryUnknownField, "fixtures", nil)
	}
	if spec.Version == 3 {
		return validateFixtures(spec)
	}
	return nil
}

// validateFixtures checks v3 local shape while leaving graph closure and contract authority to the fixture package.
func validateFixtures(spec Specification) error {
	seen := map[string]bool{}
	for i, fixture := range spec.Fixtures {
		prefix := fmt.Sprintf("fixtures[%d]", i)
		if err := validID(fixture.ID, prefix+".id"); err != nil {
			return err
		}
		if seen[fixture.ID] {
			return validationError(CategoryDuplicateID, prefix+".id", nil)
		}
		seen[fixture.ID] = true
		deps := map[string]bool{}
		for j, dependency := range fixture.DependsOn {
			if !identifierPattern.MatchString(dependency) || dependency == fixture.ID {
				return validationError(CategoryInvalidValue, fmt.Sprintf("%s.depends_on[%d]", prefix, j), nil)
			}
			if deps[dependency] {
				return validationError(CategoryDuplicateID, fmt.Sprintf("%s.depends_on[%d]", prefix, j), nil)
			}
			deps[dependency] = true
		}
		if err := validateFixtureOperation(fixture.Create, prefix+".create"); err != nil {
			return err
		}
		if len(fixture.Captures) == 0 {
			return validationError(CategoryRequired, prefix+".capture", nil)
		}
		captureNames := map[string]bool{}
		for j, capture := range fixture.Captures {
			field := fmt.Sprintf("%s.capture[%d]", prefix, j)
			if err := validID(capture.Name, field+".name"); err != nil {
				return err
			}
			if captureNames[capture.Name] {
				return validationError(CategoryDuplicateID, field+".name", nil)
			}
			captureNames[capture.Name] = true
			if len(capture.Path) == 0 || !validFixtureType(capture.ValueType) || capture.MaxBytes <= 0 {
				return validationError(CategoryInvalidValue, field, nil)
			}
			for k, segment := range capture.Path {
				if (segment.Field == nil) == (segment.Index == nil) || segment.Field != nil && *segment.Field == "" || segment.Index != nil && *segment.Index < 0 {
					return validationError(CategoryInvalidValue, fmt.Sprintf("%s.path[%d]", field, k), nil)
				}
			}
		}
		if err := validateFixtureOperation(fixture.Cleanup, prefix+".cleanup"); err != nil {
			return err
		}
	}
	return nil
}

// validateFixtureOperation rejects incomplete, unsafe, or caller-keyed operation declarations before composition.
func validateFixtureOperation(operation FixtureOperation, field string) error {
	if !validHTTPMethod(operation.Method) || !validOperationTemplate(operation.Path) {
		return validationError(CategoryInvalidValue, field+".operation", nil)
	}
	if !identifierPattern.MatchString(operation.IdentityID) {
		return validationError(CategoryRequired, field+".identity_id", nil)
	}
	if operation.Idempotency != "contract" {
		return validationError(CategoryInvalidValue, field+".idempotency", nil)
	}
	if len(operation.AcceptedStatuses) == 0 {
		return validationError(CategoryRequired, field+".accepted_statuses", nil)
	}
	seen := map[int]bool{}
	for i, status := range operation.AcceptedStatuses {
		if status < 100 || status > 599 || seen[status] {
			return validationError(CategoryInvalidValue, fmt.Sprintf("%s.accepted_statuses[%d]", field, i), nil)
		}
		seen[status] = true
	}
	return validateFixtureInput(operation.Input, field+".input")
}

// validateFixtureInput bounds recursive structured input and proves each node uses exactly one union alternative.
func validateFixtureInput(input FixtureInput, field string) error {
	for name, value := range input.PathParameters {
		if !identifierPattern.MatchString(name) {
			return validationError(CategoryInvalidValue, field+".path_parameters", nil)
		}
		if err := validateFixtureValue(value, field+".path_parameters."+name, 0); err != nil {
			return err
		}
	}
	for name, value := range input.Query {
		if !identifierPattern.MatchString(name) {
			return validationError(CategoryInvalidValue, field+".query", nil)
		}
		if err := validateFixtureValue(value, field+".query."+name, 0); err != nil {
			return err
		}
	}
	if input.Body != nil {
		return validateFixtureValue(*input.Body, field+".body", 0)
	}
	return nil
}

// validateFixtureValue validates the explicit union discriminator and recursive finite JSON shape.
func validateFixtureValue(value FixtureValue, field string, depth int) error {
	if depth > 32 {
		return validationError(CategoryInvalidValue, field, nil)
	}
	switch value.Kind {
	case "literal":
		if value.Reference != nil || value.Object != nil || value.Array != nil || !validFixtureJSON(value.Literal, depth) {
			return validationError(CategoryInvalidValue, field, nil)
		}
		return nil
	case "reference":
		if value.Reference == nil || value.Literal != nil || value.Object != nil || value.Array != nil || !identifierPattern.MatchString(value.Reference.FixtureID) || !identifierPattern.MatchString(value.Reference.Output) || !validFixtureType(value.Reference.ValueType) {
			return validationError(CategoryInvalidValue, field+".reference", nil)
		}
	case "object":
		if value.Reference != nil || value.Literal != nil || value.Object == nil || value.Array != nil {
			return validationError(CategoryInvalidValue, field, nil)
		}
		for key, child := range value.Object {
			if key == "" {
				return validationError(CategoryInvalidValue, field, nil)
			}
			if err := validateFixtureValue(child, field+"."+key, depth+1); err != nil {
				return err
			}
		}
	case "array":
		if value.Reference != nil || value.Literal != nil || value.Object != nil || value.Array == nil {
			return validationError(CategoryInvalidValue, field, nil)
		}
		for i, child := range value.Array {
			if err := validateFixtureValue(child, fmt.Sprintf("%s[%d]", field, i), depth+1); err != nil {
				return err
			}
		}
	default:
		return validationError(CategoryInvalidValue, field, nil)
	}
	return nil
}

// validFixtureType limits captures and references to closed JSON-compatible scalar/container types.
func validFixtureType(value string) bool {
	switch value {
	case "string", "number", "boolean", "object", "array":
		return true
	}
	return false
}

// validFixtureJSON recursively accepts only finite JSON-compatible literal values within the fixture depth bound.
func validFixtureJSON(value any, depth int) bool {
	if depth > 32 {
		return false
	}
	switch typed := value.(type) {
	case nil, string, bool, json.Number:
		if number, ok := typed.(json.Number); ok {
			parsed, err := number.Float64()
			return err == nil && !math.IsNaN(parsed) && !math.IsInf(parsed, 0)
		}
		return true
	case float32:
		return !math.IsNaN(float64(typed)) && !math.IsInf(float64(typed), 0)
	case float64:
		return !math.IsNaN(typed) && !math.IsInf(typed, 0)
	case int, int8, int16, int32, int64, uint, uint8, uint16, uint32, uint64:
		return true
	case map[string]any:
		for key, child := range typed {
			if key == "" || !validFixtureJSON(child, depth+1) {
				return false
			}
		}
		return true
	case []any:
		for _, child := range typed {
			if !validFixtureJSON(child, depth+1) {
				return false
			}
		}
		return true
	default:
		return false
	}
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
