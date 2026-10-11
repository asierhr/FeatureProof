package specification

import (
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"strings"
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

func (e *ValidationError) Unwrap() error { return e.Cause }

func (e *ValidationError) Is(target error) bool { return target == categorySentinel(e.Category) }

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

func (e *UnsupportedVersionError) Error() string {
	return fmt.Sprintf("validate specification version: unsupported_version received=%d supported=%v", e.Received, e.Supported)
}

func (e *UnsupportedVersionError) Is(target error) bool { return target == ErrUnsupportedVersion }

var identifierPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)

func validationError(category Category, field string, cause error) error {
	return &ValidationError{Category: category, Field: field, Cause: cause}
}

func requiredString(value, field string) error {
	if value == "" {
		return validationError(CategoryRequired, field, nil)
	}
	return nil
}

func validID(value, field string) error {
	if err := requiredString(value, field); err != nil {
		return err
	}
	if !identifierPattern.MatchString(value) {
		return validationError(CategoryInvalidValue, field, nil)
	}
	return nil
}

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
	if spec.Version != 1 {
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
	return nil
}
