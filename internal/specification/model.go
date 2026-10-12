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
