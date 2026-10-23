// Package specification defines the internal validation specification model.
package specification

// Specification is a validated description of a feature and its validation
// budget. External formats are translated into this model by adapters.
type Specification struct {
	Version     int
	Feature     Feature
	Target      Target
	Constraints []Constraint
	Limits      Limits
	Identities  []TestIdentity
	Fixtures    []Fixture
}

// Fixture declares one trusted HTTP resource lifecycle without granting target or tool authority.
type Fixture struct {
	ID        string
	DependsOn []string
	Create    FixtureOperation
	Captures  []CaptureDeclaration
	Cleanup   FixtureOperation
}

// FixtureOperation selects an exact contract operation, identity, bounded input, statuses, and verified idempotency mode.
type FixtureOperation struct {
	Method           string
	Path             string
	IdentityID       string
	Input            FixtureInput
	AcceptedStatuses []int
	Idempotency      string
}

// FixtureInput contains structured HTTP locations whose leaves are strict literal/reference unions.
type FixtureInput struct {
	PathParameters map[string]FixtureValue
	Query          map[string]FixtureValue
	Body           *FixtureValue
}

// FixtureValue is a recursive input node with exactly one literal, reference, object, or array alternative.
type FixtureValue struct {
	Literal   any
	Reference *FixtureReference
	Object    map[string]FixtureValue
	Array     []FixtureValue
	Kind      string
}

// FixtureReference selects one typed, confirmed capture from the same run.
type FixtureReference struct {
	FixtureID string
	Output    string
	ValueType string
}

// CaptureDeclaration defines the only typed and bounded value that may leave a create response.
type CaptureDeclaration struct {
	Name      string
	Path      []CapturePathSegment
	ValueType string
	MaxBytes  int
}

// CapturePathSegment selects exactly one object field or array index without expression evaluation.
type CapturePathSegment struct {
	Field *string
	Index *int
}

// AuthenticationMechanism declares how the terminal HTTP boundary injects one credential without carrying its value.
type AuthenticationMechanism struct {
	Type               string
	Name               string
	SensitiveJSONPaths []string
}

// AuthorizedOperation restricts a test identity to one exact capability, method, and structural path template.
type AuthorizedOperation struct {
	Capability string
	Method     string
	Path       string
}

// TestIdentity is the trusted non-secret declaration that binds an opaque credential reference to exact operations.
type TestIdentity struct {
	ID            string
	TargetID      string
	CredentialRef string
	Mechanism     AuthenticationMechanism
	Operations    []AuthorizedOperation
}

// Feature identifies and describes the functionality under validation.
type Feature struct {
	ID          string
	Description string
}

// Target identifies the deployed application that may be validated.
type Target struct {
	ID      string
	BaseURL string
}

// Constraint describes a condition that must hold for a target.
type Constraint struct {
	ID          string
	Description string
	TargetID    string
}

// Limits bounds the work allowed for a validation.
type Limits struct {
	MaxSteps     int `json:"max_steps"`
	MaxToolCalls int `json:"max_tool_calls"`
	MaxRetries   int `json:"max_retries"`
}
