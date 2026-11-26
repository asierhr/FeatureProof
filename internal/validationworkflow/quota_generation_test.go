package validationworkflow

import (
	"context"
	"errors"
	"testing"

	"github.com/asierhr/featureProof/internal/agentplanning"
)

// denyingGenerationQuota proves model generation is unreachable after quota denial.
type denyingGenerationQuota struct{ err error }

// ReserveGeneration returns the configured model-budget denial.
func (quota denyingGenerationQuota) ReserveGeneration(context.Context, AdmissionSnapshot) (GenerationQuotaReceipt, error) {
	return GenerationQuotaReceipt{}, quota.err
}

// ResolveGeneration is unreachable without a reservation.
func (denyingGenerationQuota) ResolveGeneration(context.Context, GenerationQuotaReceipt, ModelUsage, bool) error {
	return nil
}

// countingGeneration records model calls made by the guarded port.
type countingGeneration struct{ calls int }

// Generate records one model invocation.
func (generation *countingGeneration) Generate(context.Context, AdmissionSnapshot) (agentplanning.Result, ModelUsage, error) {
	generation.calls++
	return agentplanning.Result{}, ModelUsage{}, nil
}

// TestQuotaGenerationRequiresGuardAndDeniesBeforeModel covers mandatory model quota composition.
func TestQuotaGenerationRequiresGuardAndDeniesBeforeModel(t *testing.T) {
	next := &countingGeneration{}
	if _, err := NewQuotaGenerationPort(next, nil); !IsCode(err, CodeInvalid) {
		t.Fatalf("missing quota=%v", err)
	}
	denial := errors.New("model quota denied")
	port, _ := NewQuotaGenerationPort(next, denyingGenerationQuota{err: denial})
	if _, _, err := port.Generate(context.Background(), AdmissionSnapshot{}); !errors.Is(err, denial) || next.calls != 0 {
		t.Fatalf("generate=%v calls=%d", err, next.calls)
	}
}
