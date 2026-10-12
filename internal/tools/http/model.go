// Package httptool provides the target-scoped HTTP capability used to collect bounded evidence.
package httptool

import (
	"context"
	"errors"
	"fmt"
	"net"
	"time"

	"github.com/asierhr/featureProof/internal/harness"
	"github.com/asierhr/featureProof/internal/specification"
)

const toolName = "http.request"

// Resolver supplies the operation-local address snapshot for the authorized hostname.
type Resolver interface {
	LookupIPAddr(context.Context, string) ([]net.IPAddr, error)
}

// Dialer opens a cancelable socket only after the tool has selected an authorized pinned address.
type Dialer interface {
	DialContext(context.Context, string, string) (net.Conn, error)
}

// Clock supplies request duration timestamps without binding results to wall time in tests.
type Clock interface {
	Now() time.Time
}

// TimeoutFactory creates the child deadline that bounds every HTTP exchange independently of its caller.
type TimeoutFactory func(context.Context, time.Duration) (context.Context, context.CancelFunc)

// Config fixes the target, permissions, limits, and replaceable external boundaries for one HTTP capability.
type Config struct {
	Target                    specification.Target
	AllowedMethods            []string
	AllowedRequestHeaders     []string
	AllowedResponseHeaders    []string
	FollowRedirectsForMethods []string
	MaxRedirects              int
	Timeout                   time.Duration
	MaxURLBytes               int
	MaxRequestHeaderBytes     int
	MaxRequestBodyBytes       int
	MaxResponseHeaderBytes    int
	MaxResponseBodyBytes      int
	Resolver                  Resolver
	Dialer                    Dialer
	Clock                     Clock
	WithTimeout               TimeoutFactory
}

// ConfigError reports a constructor rejection before a network capability can be registered.
type ConfigError struct {
	Field string
	Cause error
}

// Error identifies the rejected trusted configuration field without exposing target data.
func (e *ConfigError) Error() string {
	if e.Cause == nil {
		return fmt.Sprintf("invalid HTTP tool configuration at %s", e.Field)
	}
	return fmt.Sprintf("invalid HTTP tool configuration at %s: %v", e.Field, e.Cause)
}

// Unwrap preserves a lower-level parsing or validation cause for diagnostics.
func (e *ConfigError) Unwrap() error { return e.Cause }

// InputError reports untrusted request data rejected before DNS or socket access.
type InputError struct {
	Field    string
	Category string
	Cause    error
}

// Error identifies a stable category and field path without echoing untrusted values.
func (e *InputError) Error() string {
	return fmt.Sprintf("invalid HTTP input: %s at %s", e.Category, e.Field)
}

// Unwrap preserves a safe validation cause while keeping input values out of diagnostics.
func (e *InputError) Unwrap() error { return e.Cause }

// OperationError classifies an exchange failure for output and executor retry policy.
type OperationError struct {
	Category  string
	Transient bool
	Cause     error
}

// Error returns only the stable operational category so URLs, headers, and bodies cannot leak.
func (e *OperationError) Error() string { return "HTTP operation failed: " + e.Category }

// Unwrap preserves the original transport cause for errors.Is and errors.As.
func (e *OperationError) Unwrap() error { return e.Cause }

// Is allows callers to distinguish an operation category without comparing diagnostic text.
func (e *OperationError) Is(target error) bool {
	other, ok := target.(*OperationError)
	return ok && other.Category != "" && e.Category == other.Category
}

// Result is the bounded observation returned to the executor for sanitizing, persistence, and assertions.
type Result struct {
	Status        any
	Headers       map[string][]string
	Body          any
	BodyFormat    string
	BodyBytes     int
	BodyTruncated bool
	DurationMS    int64
	Error         map[string]any
}

// validatedInput is the immutable request representation produced before crossing the network boundary.
type validatedInput struct {
	method      string
	path        []string
	query       map[string][]string
	headers     map[string][]string
	body        []byte
	bodyPresent bool
}

// targetScope is the normalized authority and segment boundary that no plan input may widen.
type targetScope struct {
	scheme       string
	hostname     string
	port         string
	baseSegments []string
}

// Tool owns immutable HTTP permissions while each Execute call owns all request state.
type Tool struct {
	scope            targetScope
	methods          map[string]struct{}
	requestHeaders   map[string]string
	responseHeaders  map[string]string
	redirectMethods  map[string]struct{}
	maxRedirects     int
	timeout          time.Duration
	maxURL           int
	maxRequestHeader int
	maxRequestBody   int
	maxResponseHead  int
	maxResponseBody  int
	resolver         Resolver
	dialer           Dialer
	clock            Clock
	withTimeout      TimeoutFactory
}

var (
	errDestinationDenied = &OperationError{Category: "destination_denied"}
	errRedirectDenied    = &OperationError{Category: "redirect_denied"}
	errRedirectLimit     = &OperationError{Category: "redirect_limit"}
)

// Spec exposes the capability through the existing registry with conservative effect and retry metadata.
func (t *Tool) Spec() harness.ToolSpec {
	return harness.ToolSpec{Name: toolName, Validate: t.Validate, Effects: true, Idempotent: false, Tool: t}
}

// operationFailure maps one typed category to both structured output and the harness retry contract.
func operationFailure(category string, transient bool, cause error, result Result) (harness.ToolResult, error) {
	result.Error = map[string]any{"category": category}
	class := harness.ErrorPermanent
	if transient {
		class = harness.ErrorTransient
	}
	opErr := &OperationError{Category: category, Transient: transient, Cause: cause}
	return harness.ToolResult{Output: resultMap(result), ErrorClass: class}, &harness.OperationError{Class: class, Err: opErr}
}

// resultMap translates adapter-owned result types into the stable JSON-compatible tool boundary.
func resultMap(result Result) map[string]any {
	var structuredError any
	if result.Error != nil {
		structuredError = result.Error
	}
	return map[string]any{
		"status": result.Status, "headers": result.Headers, "body": result.Body,
		"body_format": result.BodyFormat, "body_bytes": result.BodyBytes,
		"body_truncated": result.BodyTruncated, "duration_ms": result.DurationMS, "error": structuredError,
	}
}

// classifiedContextError gives parent cancellation precedence over the tool's child timeout.
func classifiedContextError(parent, operation context.Context, cause error, result Result) (harness.ToolResult, error) {
	if parent.Err() != nil {
		return harness.ToolResult{}, parent.Err()
	}
	if errors.Is(operation.Err(), context.DeadlineExceeded) {
		return operationFailure("timeout", true, context.DeadlineExceeded, result)
	}
	return operationFailure("connect", true, cause, result)
}
