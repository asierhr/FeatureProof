// Package agenteval evaluates agent planning behavior without executing application actions.
package agenteval

import (
	"context"
	"fmt"
	"math"
	"time"

	"github.com/asierhr/featureProof/internal/agentplanning"
	"github.com/asierhr/featureProof/internal/openapi"
	"github.com/asierhr/featureProof/internal/planning"
)

// ErrorCategory identifies a stable evaluation failure without exposing fixture or provider text.
type ErrorCategory string

const (
	InvalidInput         ErrorCategory = "invalid_input"
	UnsupportedVersion   ErrorCategory = "unsupported_version"
	IntegrityFailure     ErrorCategory = "integrity_failure"
	LimitExceeded        ErrorCategory = "limit_exceeded"
	Cancelled            ErrorCategory = "cancelled"
	Timeout              ErrorCategory = "timeout"
	ModelFailure         ErrorCategory = "model_failure"
	ArtifactFailure      ErrorCategory = "artifact_failure"
	IncompatibleBaseline ErrorCategory = "incompatible_baseline"
)

// EvalError is the safe diagnostic boundary for evaluation configuration and execution.
type EvalError struct {
	Category    ErrorCategory
	Stage, Path string
	Cause       error
}

// Error formats only controlled structural metadata.
func (e *EvalError) Error() string {
	return fmt.Sprintf("agent evaluation %s failed (%s at %s)", e.Stage, e.Category, e.Path)
}

// Unwrap preserves cancellation causes for exit-code mapping.
func (e *EvalError) Unwrap() error { return e.Cause }

// LoadLimits bounds every local input admitted before inference.
type LoadLimits struct {
	MaxFiles      int   `json:"max_files"`
	MaxTotalBytes int64 `json:"max_total_bytes"`
	MaxFileBytes  int64 `json:"max_file_bytes"`
	MaxCases      int   `json:"max_cases"`
}

// EvaluationBudget closes the five measurable resource dimensions for one case.
type EvaluationBudget struct {
	MaxCalls          int   `json:"max_calls"`
	MaxTokens         int64 `json:"max_tokens"`
	MaxDurationMillis int64 `json:"max_duration_millis"`
	MaxReplans        int   `json:"max_replans"`
	MaxWork           int64 `json:"max_work"`
}

// SourceBudget records the safe non-monetary source and I/O ceilings used by a run.
type SourceBudget struct {
	Kind              string `json:"kind"`
	Provider          string `json:"provider"`
	Model             string `json:"model"`
	CallTimeoutMillis int64  `json:"call_timeout_millis"`
	MaxCalls          int    `json:"max_calls"`
	MaxReplans        int    `json:"max_replans"`
	MaxTokens         int64  `json:"max_tokens"`
	MaxDurationMillis int64  `json:"max_duration_millis"`
	MaxRequestBytes   int64  `json:"max_request_bytes"`
	MaxHeaderBytes    int64  `json:"max_header_bytes"`
	MaxStreamBytes    int64  `json:"max_stream_bytes"`
	MaxOutputBytes    int64  `json:"max_output_bytes"`
}

// CaseConfiguration records generation and measurement limits without fixture or credential data.
type CaseConfiguration struct {
	CaseID     string               `json:"case_id"`
	Generation agentplanning.Limits `json:"generation"`
	Evaluation EvaluationBudget     `json:"evaluation"`
}

// SafeConfiguration is the versioned, secret-free configuration published for auditability.
type SafeConfiguration struct {
	SchemaVersion int                 `json:"schema_version"`
	Load          LoadLimits          `json:"load"`
	Run           RunLimits           `json:"run"`
	Source        SourceBudget        `json:"source"`
	Cases         []CaseConfiguration `json:"cases"`
}

// RunLimits bounds evaluation result construction and publication.
type RunLimits struct {
	MaxFiles          int   `json:"max_files"`
	MaxTotalBytes     int64 `json:"max_total_bytes"`
	MaxCases          int   `json:"max_cases"`
	MaxReplayBytes    int64 `json:"max_replay_bytes"`
	MaxResultBytes    int64 `json:"max_result_bytes"`
	MaxArtifactBytes  int64 `json:"max_artifact_bytes"`
	MaxDurationMillis int64 `json:"max_duration_millis"`
}

// DatasetRef binds one ordered case file to its expected content digest.
type DatasetRef struct{ ID, File, SHA256 string }

// DatasetIdentity is the versioned immutable identity used by bundles and baselines.
type DatasetIdentity struct {
	ID      string `json:"id"`
	Version string `json:"version"`
	Digest  string `json:"digest"`
}

// OperationExpectation identifies one exact operation expected from a constraint.
type OperationExpectation struct{ ConstraintID, Method, Path string }

// RejectionExpectation allowlists a typed generator rejection.
type RejectionExpectation struct {
	Category    agentplanning.ErrorCategory `json:"category"`
	Stage, Path string
}

// CaseExpectation defines bounded structural properties and never scores model prose.
type CaseExpectation struct {
	Outcome                                                                      string `json:"outcome"`
	RequiredConstraints, AllowedCapabilities, AllowedIdentities, AllowedEvidence []string
	Operations                                                                   []OperationExpectation
	MissingKinds                                                                 []agentplanning.MissingKind
	MissingConstraints                                                           []string
	Rejection                                                                    *RejectionExpectation
}

// DescriptorWire names a local allowlisted validator without serializing executable code.
type DescriptorWire struct {
	Name, Class, Description, ContractID string
	Effects, Idempotent                  bool
}

// SelectionWire binds one constraint to operations selected from a local OpenAPI document.
type SelectionWire struct {
	ConstraintID string
	Operations   []openapi.OperationKey
}

// ExternalFragment models synthetic log or response data inserted into an untrusted constraint description.
type ExternalFragment struct{ Source, ConstraintID, Canary string }

// Case is an immutable composed evaluation case with no executable application handle.
type Case struct {
	id, category, digest string
	tags, canaries       []string
	constraints          []agentplanning.Constraint
	openAPI              openapi.ReducedContext
	descriptors          []agentplanning.CapabilityDescriptor
	limits               agentplanning.Limits
	evaluation           EvaluationBudget
	expectation          CaseExpectation
}

// ID returns the stable dataset case identifier.
func (c Case) ID() string { return c.id }

// Digest returns the verified canonical case digest.
func (c Case) Digest() string { return c.digest }

// Canaries returns isolated synthetic secret and injection markers.
func (c Case) Canaries() []string { return append([]string(nil), c.canaries...) }

// Expectation returns an isolated structural expectation.
func (c Case) Expectation() CaseExpectation { return cloneExpectation(c.expectation) }

// Suite is the immutable ordered snapshot consumed by one runner.
type Suite struct {
	identity DatasetIdentity
	cases    []Case
	load     LoadLimits
}

// Identity returns the versioned dataset identity.
func (s Suite) Identity() DatasetIdentity { return s.identity }

// Cases returns an isolated ordered case slice.
func (s Suite) Cases() []Case { return append([]Case(nil), s.cases...) }

// LoadLimits returns the exact local admission limits used to compose the suite.
func (s Suite) LoadLimits() LoadLimits { return s.load }

// MetricName is one stable scoring category shared by criteria and comparison.
type MetricName string

const (
	MetricConstraintCoverage  MetricName = "constraint_coverage"
	MetricPlanValidity        MetricName = "plan_validity"
	MetricOperationSelection  MetricName = "operation_selection"
	MetricInsufficientContext MetricName = "insufficient_context"
	MetricLimitCompliance     MetricName = "limit_compliance"
)

// InventionCategory is one deterministic unsafe structural relation.
type InventionCategory string

const (
	InventionEndpoint   InventionCategory = "endpoint"
	InventionCapability InventionCategory = "capability"
	InventionIdentity   InventionCategory = "identity"
	InventionCredential InventionCategory = "credential"
	InventionEvidence   InventionCategory = "evidence"
)

// Criteria is the complete immutable release policy with no hidden defaults.
type Criteria struct {
	ID, Version, Digest string
	MinimumPPM          map[MetricName]int64
	HardFails           []InventionCategory
	MaxNegativeDeltaPPM map[string]int64
	FailFast            bool
	Limits              RunLimits
}

// Score stores an exact rational score and its integer parts-per-million projection.
type Score struct {
	Numerator   int64 `json:"numerator"`
	Denominator int64 `json:"denominator"`
	PPM         int64 `json:"ppm"`
	Applicable  bool  `json:"applicable"`
}

// Finding records a category and controlled structural path without the unsafe value.
type Finding struct {
	Category InventionCategory `json:"category"`
	CaseID   string            `json:"case_id"`
	Path     string            `json:"path"`
}

// CaseResult is the stable per-case structural result published by the suite.
type CaseResult struct {
	CaseID          string               `json:"case_id"`
	CaseDigest      string               `json:"case_digest"`
	Outcome         string               `json:"outcome"`
	ExpectedOutcome string               `json:"expected_outcome"`
	Findings        []Finding            `json:"findings"`
	Metrics         map[MetricName]Score `json:"metrics"`
	ErrorCategory   string               `json:"error_category,omitempty"`
	ErrorStage      string               `json:"error_stage,omitempty"`
	ErrorPath       string               `json:"error_path,omitempty"`
	Calls           int                  `json:"calls"`
	Tokens          int64                `json:"tokens"`
	DurationMillis  int64                `json:"duration_millis"`
	Replans         int                  `json:"replans"`
	Work            int64                `json:"work"`
	PromptHash      string               `json:"prompt_hash"`
	LimitCompliant  bool                 `json:"limit_compliant"`
}

// Metrics is the canonical aggregate scoring document.
type Metrics struct {
	SchemaVersion       int                         `json:"schema_version"`
	Scores              map[MetricName]Score        `json:"scores"`
	Inventions          map[InventionCategory]int64 `json:"inventions"`
	Confusion           map[string]int64            `json:"confusion"`
	ExpectationFailures int64                       `json:"expectation_failures"`
	ResultDigest        string                      `json:"result_digest"`
}

// RunResult contains a complete measurement prior to optional comparison and publication.
type RunResult struct {
	Dataset                                                DatasetIdentity
	Cases                                                  []CaseResult
	Metrics                                                Metrics
	PromptText, PromptVersion, PromptHash, Provider, Model string
	Verdict                                                string
	Reasons                                                []string
	SafeConfiguration                                      SafeConfiguration
}

// CandidateSource supplies exactly one non-executing planning model per admitted case.
type CandidateSource interface {
	Model(context.Context, Case) (agentplanning.PlanningModel, error)
	Metadata() (string, string)
	Snapshot() SourceSnapshot
}

// SourceSnapshot exposes only non-secret model accounting needed by limit metrics.
type SourceSnapshot struct {
	Calls, Replans int
	Tokens         int64
	Duration       time.Duration
}

// durationFromMilliseconds converts a positive JSON millisecond limit without allowing time.Duration overflow.
func durationFromMilliseconds(value int64) (time.Duration, bool) {
	if value <= 0 || value > math.MaxInt64/int64(time.Millisecond) {
		return 0, false
	}
	return time.Duration(value) * time.Millisecond, true
}

// ContractRegistry resolves serialized contract IDs to descriptor-only validators.
type ContractRegistry interface {
	Resolve(string) (func(map[string]planning.InputValue) error, bool)
}

// AssertionValidator validates declarations while preserving the deterministic assertion boundary.
type AssertionValidator interface {
	ValidateDefinition(planning.AssertionDeclaration, string) error
}

// cloneExpectation prevents callers from widening mutable allowlists after loading.
func cloneExpectation(in CaseExpectation) CaseExpectation {
	out := in
	out.RequiredConstraints = append([]string(nil), in.RequiredConstraints...)
	out.AllowedCapabilities = append([]string(nil), in.AllowedCapabilities...)
	out.AllowedIdentities = append([]string(nil), in.AllowedIdentities...)
	out.AllowedEvidence = append([]string(nil), in.AllowedEvidence...)
	out.Operations = append([]OperationExpectation(nil), in.Operations...)
	out.MissingKinds = append([]agentplanning.MissingKind(nil), in.MissingKinds...)
	out.MissingConstraints = append([]string(nil), in.MissingConstraints...)
	if in.Rejection != nil {
		v := *in.Rejection
		out.Rejection = &v
	}
	return out
}
