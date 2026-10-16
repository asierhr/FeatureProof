// Package replanning coordinates bounded post-execution decisions without granting the model execution authority.
package replanning

import (
	"fmt"
	"time"

	"github.com/asierhr/featureProof/internal/agentplanning"
)

// DecisionKind identifies the only branches accepted after an execution batch.
type DecisionKind string

const (
	DecisionTerminate           DecisionKind = "terminate"
	DecisionInsufficientContext DecisionKind = "insufficient_context"
	DecisionAdditionalChecks    DecisionKind = "additional_checks"
)

// AdditionalBatch is an atomic candidate batch, with no executable capability handles.
type AdditionalBatch struct {
	BatchID string                         `json:"batch_id"`
	Checks  []agentplanning.CandidateCheck `json:"checks"`
}

// Decision is the closed protocol-v1 domain union.
type Decision struct {
	Version    int
	Kind       DecisionKind
	Missing    []agentplanning.MissingItem
	Additional *AdditionalBatch
}

// Limits bounds structural data and deterministic work owned by one run.
type Limits struct {
	MaxInputBytes, MaxOutputBytes, MaxSummaryBytes                            int64
	MaxConstraints, MaxChecks, MaxStepsPerCheck, MaxAssertionsPerStep         int
	MaxEvidenceRefs, MaxMissingItems, MaxEvents, MaxLedgerNodes               int
	MaxReplans, MaxAgentSteps, MaxModelCalls, MaxToolCalls, MaxValidationWork int
	MaxModelTokens                                                            int64
	MaxDuration                                                               time.Duration
}

// Validate rejects unsafe or incomplete run limits before composition.
func (l Limits) Validate() error {
	if l.MaxInputBytes <= 0 || l.MaxOutputBytes <= 0 || l.MaxSummaryBytes <= 0 || l.MaxDuration <= 0 ||
		l.MaxConstraints <= 0 || l.MaxChecks <= 0 || l.MaxStepsPerCheck <= 0 || l.MaxAssertionsPerStep <= 0 ||
		l.MaxEvidenceRefs <= 0 || l.MaxMissingItems <= 0 || l.MaxEvents <= 0 || l.MaxLedgerNodes <= 0 ||
		l.MaxReplans < 0 || l.MaxAgentSteps < 0 || l.MaxModelCalls < 0 || l.MaxToolCalls < 0 || l.MaxValidationWork < 0 || l.MaxModelTokens < 0 {
		return failure(CategoryInvalidInput, "limits", "configuration")
	}
	return nil
}

// Category is a stable, payload-free operational classification.
type Category string

const (
	CategoryInvalidInput    Category = "invalid_input"
	CategoryInvalidDecision Category = "invalid_decision"
	CategoryIntegrity       Category = "integrity"
	CategoryBudget          Category = "budget_exhausted"
	CategoryRepeated        Category = "repeated_proposal"
	CategoryRejected        Category = "rejected_proposal"
	CategoryApprovalReplan  Category = "approval_replan"
	CategoryCancelled       Category = "cancelled"
	CategoryDependency      Category = "dependency_failure"
)

// Error exposes only system-generated category, stage, and path.
type Error struct {
	Category    Category
	Stage, Path string
}

// Error renders safe operational metadata without untrusted values.
func (e *Error) Error() string {
	return fmt.Sprintf("replanning %s failed (%s at %s)", e.Stage, e.Category, e.Path)
}

// failure constructs the sole public error surface for local validation.
func failure(category Category, stage, path string) error {
	return &Error{Category: category, Stage: stage, Path: path}
}
