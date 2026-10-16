package agentplanning

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"sort"
)

// BatchFingerprint reuses the approved graph canonicalizer for an entire additional-check batch.
func BatchFingerprint(ctx context.Context, checks []CandidateCheck, maxWork int) (string, error) {
	return BatchFingerprintWithBudget(ctx, checks, maxWork, nil)
}

// BatchFingerprintWithBudget charges each canonicalization visit against a caller-owned global budget before work.
func BatchFingerprintWithBudget(ctx context.Context, checks []CandidateCheck, maxWork int, reserve func(int) error) (string, error) {
	if len(checks) == 0 || maxWork <= 0 {
		return "", typedError(CategoryInvalidInput, "fingerprint", "checks", nil)
	}
	work := newValidationWork(ctx, maxWork)
	work.reserve = reserve
	signatures := make([]string, 0, len(checks))
	for _, check := range checks {
		value, err := checkFingerprint(check, work)
		if err != nil {
			return "", err
		}
		signatures = append(signatures, value)
	}
	sort.Strings(signatures)
	raw, err := json.Marshal(struct {
		Version int      `json:"version"`
		Checks  []string `json:"checks"`
	}{Version: 1, Checks: signatures})
	if err != nil {
		return "", typedError(CategoryInternal, "fingerprint", "batch", nil)
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:]), nil
}
