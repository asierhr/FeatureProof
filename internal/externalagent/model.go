// Package externalagent exposes FeatureProof validation through a small,
// versioned application contract shared by local transports.
package externalagent

import (
	"encoding/json"
	"fmt"
	"time"
)

// ContractVersion identifies the backward-incompatible application DTO set.
const ContractVersion = 1

// MCPVersion identifies the only MCP wire protocol accepted by this release.
const MCPVersion = "2026-07-28"

// Phase is the durable lifecycle visible to an external agent.
type Phase string

const (
	PhaseQueued     Phase = "queued"
	PhaseRunning    Phase = "running"
	PhaseFinalizing Phase = "finalizing"
	PhaseTerminal   Phase = "terminal"
)

// Outcome is the closed terminal vocabulary; functional failure remains data.
type Outcome string

const (
	OutcomePassed       Outcome = "passed"
	OutcomeFailed       Outcome = "failed"
	OutcomeInconclusive Outcome = "inconclusive"
	OutcomeCancelled    Outcome = "cancelled"
	OutcomeError        Outcome = "error"
)

// ErrorCode classifies safe application failures independently of transport.
type ErrorCode string

const (
	CodeInvalidRequest ErrorCode = "invalid_request"
	CodeUnauthorized   ErrorCode = "unauthorized"
	CodeNotFound       ErrorCode = "not_found"
	CodeConflict       ErrorCode = "conflict"
	CodeInProgress     ErrorCode = "in_progress"
	CodeInternal       ErrorCode = "internal_error"
)

// AppError is the only error representation allowed to cross a transport.
type AppError struct {
	Code      ErrorCode `json:"code"`
	Field     string    `json:"field,omitempty"`
	Retryable bool      `json:"retryable"`
}

// Error intentionally contains no rejected value, provider text, or path.
func (e *AppError) Error() string { return fmt.Sprintf("external agent request: %s", e.Code) }

// Envelope carries either application data or one closed, value-free error.
type Envelope struct {
	ContractVersion int       `json:"contract_version"`
	Data            any       `json:"data,omitempty"`
	Error           *AppError `json:"error,omitempty"`
}

// Operation advertises one stable application operation and its schemas.
type Operation struct {
	Name         string         `json:"name"`
	InputSchema  map[string]any `json:"input_schema"`
	OutputSchema map[string]any `json:"output_schema"`
}

// CapabilitiesResult lets clients discover supported operations without guessing.
type CapabilitiesResult struct {
	ContractVersion     int         `json:"contract_version"`
	ResultSchemaVersion int         `json:"result_schema_version"`
	RetentionSeconds    int64       `json:"retention_seconds"`
	Operations          []Operation `json:"operations"`
}

// SubmitInput selects only a pre-authorized local alias and durable request key.
type SubmitInput struct {
	SelectionAlias string `json:"selection_alias"`
	IdempotencyKey string `json:"idempotency_key"`
}

// HandleInput addresses a run without conveying authority by possession.
type HandleInput struct {
	RunHandle string `json:"run_handle"`
}

// CancelInput makes cancellation itself durably idempotent.
type CancelInput struct {
	RunHandle      string `json:"run_handle"`
	IdempotencyKey string `json:"idempotency_key"`
}

// RunView is the safe lifecycle projection returned by submit, status, and cancel.
type RunView struct {
	RunHandle string    `json:"run_handle"`
	Phase     Phase     `json:"phase"`
	Status    Outcome   `json:"status,omitempty"`
	UpdatedAt time.Time `json:"updated_at"`
}

// SafeValue preserves evidence redaction states while excluding arbitrary types.
type SafeValue struct {
	State string `json:"state"`
	Value any    `json:"value,omitempty"`
}

// ConstraintView reports a deterministic constraint outcome and evidence handles.
type ConstraintView struct {
	ConstraintID    string   `json:"constraint_id"`
	Status          Outcome  `json:"status"`
	EvidenceHandles []string `json:"evidence_handles"`
}

// FailureView exposes only closed assertion metadata and already-safe values.
type FailureView struct {
	ConstraintID    string    `json:"constraint_id"`
	StepID          string    `json:"step_id"`
	DeclarationID   string    `json:"declaration_id"`
	Operator        string    `json:"operator"`
	Status          string    `json:"status"`
	Expected        SafeValue `json:"expected"`
	Observed        SafeValue `json:"observed"`
	EvidenceHandles []string  `json:"evidence_handles"`
}

// FactView exposes payload-free assertion and step facts from a verified package.
type FactView struct {
	Kind            string   `json:"kind"`
	ConstraintID    string   `json:"constraint_id"`
	StepID          string   `json:"step_id"`
	DeclarationID   string   `json:"declaration_id,omitempty"`
	Status          string   `json:"status"`
	EvidenceHandles []string `json:"evidence_handles"`
}

// EvidenceView is an opaque reference and never contains an artifact path.
type EvidenceView struct {
	EvidenceHandle string `json:"evidence_handle"`
}

// ReproductionView contains declarative, non-executable reproduction metadata.
type ReproductionView struct {
	ConstraintID        string   `json:"constraint_id"`
	CommitOID           string   `json:"commit_oid"`
	RepositoryID        string   `json:"repository_id"`
	EnvironmentID       string   `json:"environment_id"`
	ConstraintsSHA256   string   `json:"constraints_sha256"`
	ContractSHA256      string   `json:"contract_sha256"`
	ConfigurationSHA256 string   `json:"configuration_sha256"`
	PlanSHA256          []string `json:"plan_sha256"`
	CredentialRequired  bool     `json:"credential_required"`
}

// ResultView is derived only from a verified evidence package.
type ResultView struct {
	RunHandle    string             `json:"run_handle"`
	Phase        Phase              `json:"phase"`
	Status       Outcome            `json:"status"`
	Constraints  []ConstraintView   `json:"constraints"`
	Failures     []FailureView      `json:"failures"`
	Facts        []FactView         `json:"facts"`
	Evidence     []EvidenceView     `json:"evidence"`
	Reproduction []ReproductionView `json:"reproduction"`
}

// CanonicalJSON serializes application DTOs deterministically for both transports.
func CanonicalJSON(value any) ([]byte, error) { return json.Marshal(value) }
