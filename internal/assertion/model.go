// Package assertion validates and evaluates deterministic assertions over confirmed structured evidence.
package assertion

import (
	"fmt"
	"math/big"

	"github.com/asierhr/featureProof/internal/evidence"
	"github.com/asierhr/featureProof/internal/planning"
)

// ErrorCategory classifies invalid assertion configuration independently of diagnostic wording.
type ErrorCategory string

const (
	// ErrorRequired identifies a missing contract field.
	ErrorRequired ErrorCategory = "required"
	// ErrorInvalidShape identifies a node that is not exactly one supported union variant.
	ErrorInvalidShape ErrorCategory = "invalid_shape"
	// ErrorUnsupportedOperator identifies a leaf operator outside the closed language.
	ErrorUnsupportedOperator ErrorCategory = "unsupported_operator"
	// ErrorInvalidPath identifies a path outside the documented JSON Pointer subset.
	ErrorInvalidPath ErrorCategory = "invalid_path"
	// ErrorInvalidType identifies a non-JSON or operator-incompatible value.
	ErrorInvalidType ErrorCategory = "invalid_type"
)

// ConfigError locates the first deterministic assertion configuration failure.
type ConfigError struct {
	Category ErrorCategory
	Path     string
	Cause    error
}

// Error renders the typed assertion failure for human diagnostics.
func (e *ConfigError) Error() string {
	if e.Cause == nil {
		return fmt.Sprintf("%s at %s", e.Category, e.Path)
	}
	return fmt.Sprintf("%s at %s: %v", e.Category, e.Path, e.Cause)
}

// Unwrap preserves the concrete parsing or validation cause.
func (e *ConfigError) Unwrap() error { return e.Cause }

// PlanErrorCategory translates assertion configuration failures into planning's stable public categories.
func (e *ConfigError) PlanErrorCategory() planning.ErrorCategory {
	switch e.Category {
	case ErrorRequired:
		return planning.ErrorRequired
	case ErrorInvalidPath:
		return planning.ErrorInvalidReference
	default:
		return planning.ErrorInvalidValue
	}
}

// PlanFieldPath preserves the compiler's exact nested location through the planning boundary.
func (e *ConfigError) PlanFieldPath() string { return e.Path }

// node is the immutable compiled form of one leaf or group declaration.
type node struct {
	operator string
	path     string
	tokens   []string
	expected planning.AssertionValue
	number   *decimalNumber
	children []node
}

// decimalNumber stores a canonical finite decimal as compact significant digits and an arbitrary-size base-10 scale.
type decimalNumber struct {
	sign   int
	digits string
	scale  *big.Int
}

// compiledDeclaration binds one stable root ID and field path to its immutable AST.
type compiledDeclaration struct {
	id       string
	basePath string
	root     node
}

// DefinitionValidator compiles declarations and enforces evidence visibility before any effect.
type DefinitionValidator struct{ Policy *evidence.Policy }
