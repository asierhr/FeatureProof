package replanning

import (
	"context"
	"errors"
	"sync"

	"github.com/asierhr/featureProof/internal/agentplanning"
)

// Fingerprint computes the approved ID-independent batch graph identity.
func Fingerprint(ctx context.Context, batch AdditionalBatch, maxWork int) (string, error) {
	return FingerprintWithBudget(ctx, batch, maxWork, nil)
}

// FingerprintWithBudget charges canonicalization against the run-wide validation budget before each visit.
func FingerprintWithBudget(ctx context.Context, batch AdditionalBatch, maxWork int, reserve func(int) error) (string, error) {
	if len(batch.Checks) == 0 {
		return "", failure(CategoryInvalidInput, "fingerprint", "checks")
	}
	value, err := agentplanning.BatchFingerprintWithBudget(ctx, batch.Checks, maxWork, reserve)
	if err != nil {
		var typed *Error
		if errors.As(err, &typed) && typed.Category == CategoryBudget {
			return "", err
		}
		return "", failure(CategoryRejected, "fingerprint", "batch")
	}
	return value, nil
}

// FingerprintSet atomically remembers proposed, approved and executed semantics per run.
type FingerprintSet struct {
	mu   sync.Mutex
	seen map[string]bool
}

// NewFingerprintSet starts an empty semantic replay barrier.
func NewFingerprintSet() *FingerprintSet { return &FingerprintSet{seen: map[string]bool{}} }

// Reserve rejects prior semantics before any approval or tool work.
func (s *FingerprintSet) Reserve(fingerprint string) error {
	if fingerprint == "" {
		return failure(CategoryInvalidInput, "fingerprint", "value")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.seen[fingerprint] {
		return failure(CategoryRepeated, "fingerprint", "proposal")
	}
	s.seen[fingerprint] = true
	return nil
}
