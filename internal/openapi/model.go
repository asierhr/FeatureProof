// Package openapi loads bounded local OpenAPI contracts and exposes safe deterministic views.
package openapi

import (
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
)

// ErrorCategory classifies failures without exposing untrusted document values.
type ErrorCategory string

const (
	InvalidConfig      ErrorCategory = "invalid_config"
	ReadFailed         ErrorCategory = "read_failed"
	InvalidSyntax      ErrorCategory = "invalid_syntax"
	UnsupportedVersion ErrorCategory = "unsupported_version"
	InvalidDocument    ErrorCategory = "invalid_document"
	ReferenceDenied    ErrorCategory = "reference_denied"
	ReferenceBroken    ErrorCategory = "reference_broken"
	ReferenceCycle     ErrorCategory = "reference_cycle"
	LimitExceeded      ErrorCategory = "limit_exceeded"
	InvalidInput       ErrorCategory = "invalid_input"
	Cancelled          ErrorCategory = "cancelled"
)

// Error is the safe diagnostic boundary returned by the inspector.
type Error struct {
	Category ErrorCategory
	Location string
	Cause    error
}

// Error returns only category and structural location so payloads never leak through diagnostics.
func (e *Error) Error() string { return fmt.Sprintf("openapi: %s at %s", e.Category, e.Location) }

// Unwrap preserves the operational cause for errors.Is/errors.As callers.
func (e *Error) Unwrap() error { return e.Cause }

// MarshalJSON serializes only safe diagnostics and never concrete causes containing external paths or values.
func (e *Error) MarshalJSON() ([]byte, error) {
	return json.Marshal(struct {
		Category ErrorCategory `json:"category"`
		Location string        `json:"location"`
	}{Category: e.Category, Location: e.Location})
}

// Config declares every filesystem and traversal budget used by an inspector.
type Config struct {
	AuthorizedRoot       string
	MaxDocumentBytes     int64
	MaxTotalBytes        int64
	MaxDocuments         int
	MaxReferences        int
	MaxReferenceDepth    int
	MaxObservedBodyBytes int
	MaxContextBytes      int
}

// OperationKey identifies an OpenAPI operation independently of optional operationId.
type OperationKey struct {
	Method string `json:"method"`
	Path   string `json:"path"`
}

// Parameter describes an effective path or operation parameter.
type Parameter struct {
	Name     string               `json:"name"`
	In       string               `json:"in"`
	Required bool                 `json:"required,omitempty"`
	Schema   *Schema              `json:"schema,omitempty"`
	Content  map[string]MediaType `json:"content,omitempty"`
}

// Schema is the inspector-owned subset used for cataloging, validation and reduced context.
type Schema struct {
	ID            string             `json:"id,omitempty"`
	Type          string             `json:"type,omitempty"`
	Format        string             `json:"format,omitempty"`
	Required      []string           `json:"required,omitempty"`
	Properties    map[string]*Schema `json:"properties,omitempty"`
	Items         *Schema            `json:"items,omitempty"`
	AllOf         []*Schema          `json:"allOf,omitempty"`
	Enum          []any              `json:"enum,omitempty"`
	MinProperties *int               `json:"minProperties,omitempty"`
	Nullable      bool               `json:"nullable,omitempty"`
	ReadOnly      bool               `json:"readOnly,omitempty"`
	WriteOnly     bool               `json:"writeOnly,omitempty"`
	validator     schemaValidator
}

// MediaType describes one response or request-body representation.
type MediaType struct {
	Schema *Schema `json:"schema,omitempty"`
}

// RequestBody describes the normalized request payload contract.
type RequestBody struct {
	Required bool                 `json:"required,omitempty"`
	Content  map[string]MediaType `json:"content,omitempty"`
}

// Header describes one declared response header.
type Header struct {
	Required bool                 `json:"required,omitempty"`
	Schema   *Schema              `json:"schema,omitempty"`
	Content  map[string]MediaType `json:"content,omitempty"`
}

// Response describes headers and media types for a response selector.
type Response struct {
	Headers map[string]Header    `json:"headers,omitempty"`
	Content map[string]MediaType `json:"content,omitempty"`
}

// SecurityRequirement represents one AND alternative; the operation slice represents OR.
type SecurityRequirement struct {
	Schemes map[string][]string `json:"schemes"`
}

// SecurityScheme exposes contractual authentication metadata but no credential values.
type SecurityScheme struct {
	Name             string               `json:"name"`
	Type             string               `json:"type"`
	In               string               `json:"in,omitempty"`
	ParameterName    string               `json:"parameter_name,omitempty"`
	Scheme           string               `json:"scheme,omitempty"`
	OpenIDConnectURL string               `json:"openid_connect_url,omitempty"`
	Flows            map[string]OAuthFlow `json:"flows,omitempty"`
}

// OAuthFlow exposes contractual OAuth endpoints and scopes required for planning.
type OAuthFlow struct {
	AuthorizationURL string            `json:"authorization_url,omitempty"`
	TokenURL         string            `json:"token_url,omitempty"`
	Scopes           map[string]string `json:"scopes,omitempty"`
}

// OperationDescriptor is an immutable caller-facing snapshot of one operation.
type OperationDescriptor struct {
	Key         OperationKey          `json:"key"`
	OperationID string                `json:"operation_id,omitempty"`
	Parameters  []Parameter           `json:"parameters,omitempty"`
	RequestBody *RequestBody          `json:"request_body,omitempty"`
	Responses   map[string]Response   `json:"responses"`
	Security    []SecurityRequirement `json:"security"`
}

// Catalog is the deterministic operation and security view returned to callers.
type Catalog struct {
	Version         string                `json:"version"`
	Operations      []OperationDescriptor `json:"operations"`
	SecuritySchemes []SecurityScheme      `json:"security_schemes,omitempty"`
}

// ObservedResponse contains an already bounded HTTP observation supplied by the caller.
type ObservedResponse struct {
	Status  int
	Headers map[string][]string
	Body    []byte
}

// Violation identifies a contract mismatch without copying observed or expected values.
type Violation struct {
	Category string `json:"category"`
	Location string `json:"location"`
	Keyword  string `json:"keyword"`
}

// ResponseValidation is the deterministic result of validating an observation.
type ResponseValidation struct {
	Conforms         bool        `json:"conforms"`
	ResponseSelector string      `json:"response_selector,omitempty"`
	Violations       []Violation `json:"violations"`
}

// ConstraintSelection binds a caller-owned constraint to explicit operation keys.
type ConstraintSelection struct {
	ConstraintID string
	Operations   []OperationKey
}

// ContextRequest selects the only operations permitted in a reduced model context.
type ContextRequest struct{ Constraints []ConstraintSelection }

// OmissionCounts makes filtering and whole-unit size omissions explicit.
type OmissionCounts struct {
	Examples      int `json:"examples"`
	Extensions    int `json:"extensions"`
	Documentation int `json:"documentation"`
	Unselected    int `json:"unselected"`
	SizeLimit     int `json:"size_limit"`
}

// ReducedContext is a canonical untrusted-data envelope for a future model adapter.
type ReducedContext struct {
	MediaType string         `json:"media_type"`
	Untrusted bool           `json:"untrusted"`
	Bytes     []byte         `json:"-"`
	Omissions OmissionCounts `json:"omissions"`
	retained  []RetainedOperation
}

// RetainedOperation ties one caller constraint to an operation actually serialized into reduced context.
type RetainedOperation struct {
	ConstraintID string
	Operation    OperationDescriptor
}

// RetainedOperations returns an isolated exact provenance index for serialized operations only.
func (r ReducedContext) RetainedOperations() []RetainedOperation {
	out := make([]RetainedOperation, len(r.retained))
	for i, item := range r.retained {
		out[i] = RetainedOperation{ConstraintID: item.ConstraintID, Operation: cloneOperation(item.Operation)}
	}
	return out
}

// Contract owns a normalized immutable snapshot produced by a successful load.
type Contract struct {
	version    string
	operations []OperationDescriptor
	schemes    []SecurityScheme
	omitted    OmissionCounts
	maxBody    int
	maxContext int
}

// Catalog returns a deep copy so caller mutation cannot alter retained contract state.
func (c *Contract) Catalog() Catalog {
	return Catalog{Version: c.version, Operations: cloneOperations(c.operations), SecuritySchemes: cloneSchemes(c.schemes)}
}

// typedError constructs one safe error and preserves its underlying cause.
func typedError(category ErrorCategory, location string, cause error) error {
	return &Error{Category: category, Location: location, Cause: cause}
}

// cancelledError translates context cancellation into the inspector error boundary.
func cancelledError(location string, cause error) error {
	return typedError(Cancelled, location, cause)
}

// cloneOperations isolates every mutable collection in caller-facing catalog snapshots.
func cloneOperations(in []OperationDescriptor) []OperationDescriptor {
	out := make([]OperationDescriptor, len(in))
	for i := range in {
		out[i] = cloneOperation(in[i])
	}
	return out
}

// cloneOperation isolates one normalized operation and its nested schemas.
func cloneOperation(in OperationDescriptor) OperationDescriptor {
	out := in
	out.Parameters = make([]Parameter, len(in.Parameters))
	for i, p := range in.Parameters {
		out.Parameters[i] = p
		out.Parameters[i].Schema = cloneSchema(p.Schema)
		out.Parameters[i].Content = cloneContent(p.Content)
	}
	if in.RequestBody != nil {
		rb := *in.RequestBody
		rb.Content = cloneContent(in.RequestBody.Content)
		out.RequestBody = &rb
	}
	out.Responses = make(map[string]Response, len(in.Responses))
	for k, r := range in.Responses {
		nr := Response{Headers: map[string]Header{}, Content: cloneContent(r.Content)}
		for n, h := range r.Headers {
			h.Schema = cloneSchema(h.Schema)
			h.Content = cloneContent(h.Content)
			nr.Headers[n] = h
		}
		out.Responses[k] = nr
	}
	out.Security = cloneSecurity(in.Security)
	return out
}

// cloneContent isolates schemas held under media-type maps.
func cloneContent(in map[string]MediaType) map[string]MediaType {
	if in == nil {
		return nil
	}
	out := make(map[string]MediaType, len(in))
	for k, v := range in {
		v.Schema = cloneSchema(v.Schema)
		out[k] = v
	}
	return out
}

// cloneSchema recursively copies a schema snapshot.
func cloneSchema(in *Schema) *Schema {
	if in == nil {
		return nil
	}
	out := *in
	out.validator = nil
	out.Required = append([]string(nil), in.Required...)
	out.Enum = cloneJSONSlice(in.Enum)
	out.Properties = make(map[string]*Schema, len(in.Properties))
	for k, v := range in.Properties {
		out.Properties[k] = cloneSchema(v)
	}
	out.Items = cloneSchema(in.Items)
	out.AllOf = make([]*Schema, len(in.AllOf))
	for i, schema := range in.AllOf {
		out.AllOf[i] = cloneSchema(schema)
	}
	if in.MinProperties != nil {
		minimum := *in.MinProperties
		out.MinProperties = &minimum
	}
	return &out
}

// cloneJSONSlice recursively copies arbitrary JSON arrays used by schema enums.
func cloneJSONSlice(in []any) []any {
	if in == nil {
		return nil
	}
	out := make([]any, len(in))
	for i, value := range in {
		out[i] = cloneJSONValue(value)
	}
	return out
}

// cloneJSONValue recursively isolates JSON objects and arrays from caller mutation.
func cloneJSONValue(value any) any {
	switch typed := value.(type) {
	case map[string]any:
		out := make(map[string]any, len(typed))
		for key, child := range typed {
			out[key] = cloneJSONValue(child)
		}
		return out
	case []any:
		return cloneJSONSlice(typed)
	default:
		return typed
	}
}

// cloneSecurity isolates effective security alternatives and scope slices.
func cloneSecurity(in []SecurityRequirement) []SecurityRequirement {
	out := make([]SecurityRequirement, len(in))
	for i, r := range in {
		out[i].Schemes = map[string][]string{}
		for k, v := range r.Schemes {
			out[i].Schemes[k] = append([]string(nil), v...)
		}
	}
	return out
}

// cloneSchemes isolates OAuth flow and scope maps in security metadata.
func cloneSchemes(in []SecurityScheme) []SecurityScheme {
	out := make([]SecurityScheme, len(in))
	for i, s := range in {
		out[i] = s
		out[i].Flows = map[string]OAuthFlow{}
		for k, f := range s.Flows {
			nf := f
			nf.Scopes = map[string]string{}
			for n, v := range f.Scopes {
				nf.Scopes[n] = v
			}
			out[i].Flows[k] = nf
		}
	}
	return out
}

// sortViolations stabilizes results independently of map traversal order.
func sortViolations(v []Violation) {
	sort.Slice(v, func(i, j int) bool {
		if v[i].Location != v[j].Location {
			return v[i].Location < v[j].Location
		}
		if v[i].Keyword != v[j].Keyword {
			return v[i].Keyword < v[j].Keyword
		}
		return v[i].Category < v[j].Category
	})
}

// validateConfig rejects missing budgets before any filesystem access occurs.
func validateConfig(c Config) error {
	if strings.TrimSpace(c.AuthorizedRoot) == "" || c.MaxDocumentBytes <= 0 || c.MaxTotalBytes <= 0 || c.MaxDocuments <= 0 || c.MaxReferences <= 0 || c.MaxReferenceDepth <= 0 || c.MaxObservedBodyBytes <= 0 || c.MaxContextBytes < minimumContextEnvelopeBytes() {
		return typedError(InvalidConfig, "config", errors.New("invalid required budget"))
	}
	if c.MaxDocumentBytes > c.MaxTotalBytes {
		return typedError(InvalidConfig, "config.max_document_bytes", errors.New("document budget exceeds total"))
	}
	return nil
}
