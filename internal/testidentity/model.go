// Package testidentity owns non-secret identity authorization and the ephemeral credential boundary.
package testidentity

import (
	"context"
	"errors"
	"fmt"
)

// Mechanism identifies the closed authentication injection strategy confirmed by trusted configuration.
type Mechanism string

const (
	// MechanismBearer injects the lease as an Authorization bearer credential.
	MechanismBearer Mechanism = "bearer"
	// MechanismHeader injects the lease into one declared non-reserved header.
	MechanismHeader Mechanism = "header"
	// MechanismCookie injects the lease into one declared dedicated cookie.
	MechanismCookie Mechanism = "cookie"
)

// Operation identifies one exact authorized capability and structural HTTP route.
type Operation struct {
	Capability string `json:"capability"`
	TargetID   string `json:"target_id"`
	Method     string `json:"method"`
	Path       string `json:"path"`
}

// Binding is the immutable, non-secret identity selection carried by plans and audit records.
type Binding struct {
	IdentityID string    `json:"identity_id"`
	Operation  Operation `json:"operation"`
	Digest     string    `json:"digest"`
}

// Descriptor is the only identity representation allowed in model context.
type Descriptor struct {
	ID         string      `json:"id"`
	Mechanism  Mechanism   `json:"mechanism"`
	Operations []Operation `json:"operations"`
}

// ErrorCategory is a stable operational identity failure classification.
type ErrorCategory string

const (
	// CategoryUnknownIdentity reports selection of an identity outside the sealed catalog.
	CategoryUnknownIdentity ErrorCategory = "unknown_identity"
	// CategoryIdentityNotAllowed reports an operation outside the selected identity permission set.
	CategoryIdentityNotAllowed ErrorCategory = "identity_not_allowed"
	// CategoryCredentialUnavailable reports missing or empty material at the resolver boundary.
	CategoryCredentialUnavailable ErrorCategory = "credential_unavailable"
	// CategoryCredentialExpired reports material invalid at the trusted clock.
	CategoryCredentialExpired ErrorCategory = "credential_expired"
	// CategoryMechanismMismatch reports disagreement between declaration and lease metadata.
	CategoryMechanismMismatch ErrorCategory = "mechanism_mismatch"
	// CategoryRequestLimit reports that injected authentication exceeds the final request bound.
	CategoryRequestLimit ErrorCategory = "request_limit"
	// CategoryCancelled reports caller cancellation at an identity boundary.
	CategoryCancelled ErrorCategory = "cancelled"
	// CategoryTimeout reports caller deadline exhaustion at an identity boundary.
	CategoryTimeout ErrorCategory = "timeout"
	// CategoryInternal reports a trusted dependency failure without publishing its diagnostic text.
	CategoryInternal ErrorCategory = "internal"
)

// Error exposes only allowlisted identity failure facts while preserving safe context causes.
type Error struct {
	Category ErrorCategory
	Phase    string
	Field    string
	Cause    error
}

// Error formats a publishable diagnostic without credential references, values, URLs, or collaborator text.
func (e *Error) Error() string {
	return fmt.Sprintf("test identity %s failed (%s at %s)", e.Phase, e.Category, e.Field)
}

// Unwrap preserves only safe cancellation or deadline causes selected by the constructor.
func (e *Error) Unwrap() error { return e.Cause }

// Is permits stable category matching without diagnostic string comparison.
func (e *Error) Is(target error) bool {
	want, ok := target.(*Error)
	return ok && (want.Category == "" || want.Category == e.Category)
}

// typedError constructs the package's safe public error and strips arbitrary dependency text.
func typedError(category ErrorCategory, phase, field string, cause error) error {
	safe := error(nil)
	if errors.Is(cause, context.Canceled) {
		safe = context.Canceled
	} else if errors.Is(cause, context.DeadlineExceeded) {
		safe = context.DeadlineExceeded
	}
	return &Error{Category: category, Phase: phase, Field: field, Cause: safe}
}
