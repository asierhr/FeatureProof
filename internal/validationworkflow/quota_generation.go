package validationworkflow

import (
	"context"

	"github.com/asierhr/featureProof/internal/agentplanning"
)

// generationQuotaIdentityKey isolates the workflow-owned operation identity
// from model input and prevents a provider from choosing its quota receipt.
type generationQuotaIdentityKey struct{}

// WithGenerationQuotaIdentity binds one admitted workflow handle to all model
// calls performed for that durable run.
func WithGenerationQuotaIdentity(ctx context.Context, workflowID string) context.Context {
	return context.WithValue(ctx, generationQuotaIdentityKey{}, workflowID)
}

// GenerationQuotaIdentity returns the service-owned quota identity without
// exposing a generic caller-defined context key.
func GenerationQuotaIdentity(ctx context.Context) (string, bool) {
	value, ok := ctx.Value(generationQuotaIdentityKey{}).(string)
	return value, ok && value != ""
}

// GenerationQuotaReceipt is the opaque pre-model reservation retained across provider uncertainty.
type GenerationQuotaReceipt struct{ ID, Digest string }

// GenerationQuotaGate owns model-call and token reservations without exposing tenant policy to the model.
type GenerationQuotaGate interface {
	ReserveGeneration(context.Context, AdmissionSnapshot) (GenerationQuotaReceipt, error)
	ResolveGeneration(context.Context, GenerationQuotaReceipt, ModelUsage, bool) error
}

// QuotaGenerationPort enforces durable model capacity before invoking the configured generator.
type QuotaGenerationPort struct {
	next  GenerationPort
	quota GenerationQuotaGate
}

// NewQuotaGenerationPort rejects workflow composition without both generation and quota consumers.
func NewQuotaGenerationPort(next GenerationPort, quota GenerationQuotaGate) (*QuotaGenerationPort, error) {
	if next == nil || quota == nil {
		return nil, &Error{Code: CodeInvalid, Operation: "quota_generation_compose"}
	}
	return &QuotaGenerationPort{next: next, quota: quota}, nil
}

// Generate reserves calls/tokens first and retains the receipt when provider outcome is uncertain.
func (port *QuotaGenerationPort) Generate(ctx context.Context, snapshot AdmissionSnapshot) (agentplanning.Result, ModelUsage, error) {
	receipt, err := port.quota.ReserveGeneration(ctx, snapshot)
	if err != nil {
		return agentplanning.Result{}, ModelUsage{}, err
	}
	result, usage, generationErr := port.next.Generate(ctx, snapshot)
	if err = port.quota.ResolveGeneration(ctx, receipt, usage, generationErr != nil); err != nil {
		return agentplanning.Result{}, ModelUsage{}, err
	}
	return result, usage, generationErr
}
