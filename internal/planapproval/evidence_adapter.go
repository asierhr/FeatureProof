package planapproval

import (
	"context"

	"github.com/asierhr/featureProof/internal/evidence"
)

// EvidenceSanitizer adapts the repository redaction policy to approval's minimal safe-projection port.
type EvidenceSanitizer struct{ sanitizer evidence.Sanitizer }

// NewEvidenceSanitizer rejects an unconfigured redaction boundary before approval staging starts.
func NewEvidenceSanitizer(sanitizer evidence.Sanitizer) (EvidenceSanitizer, error) {
	if !sanitizer.Configured() {
		return EvidenceSanitizer{}, typedError(CategoryInvalidInput, "compose_sanitizer", "policy", nil)
	}
	return EvidenceSanitizer{sanitizer: sanitizer}, nil
}

// Sanitize returns only the treated clone produced by the existing evidence policy.
func (s EvidenceSanitizer) Sanitize(ctx context.Context, value any) (any, error) {
	result, err := s.sanitizer.Sanitize(ctx, value)
	if err != nil {
		return nil, err
	}
	return result.Value(), nil
}
