package agenteval

import (
	"context"
	"os"
)

// criteriaWire is the closed versioned policy shape whose fields are all mandatory.
type criteriaWire struct {
	SchemaVersion       int                  `json:"schema_version"`
	CriteriaID          string               `json:"criteria_id"`
	CriteriaVersion     string               `json:"criteria_version"`
	MinimumPPM          map[MetricName]int64 `json:"minimum_ppm"`
	HardFailCategories  []InventionCategory  `json:"hard_fail_categories"`
	MaxNegativeDeltaPPM map[string]int64     `json:"max_negative_delta_ppm"`
	FailFast            *bool                `json:"fail_fast"`
	RunLimits           RunLimits            `json:"run_limits"`
}

// LoadCriteria reads one strict versioned local policy without filling omitted values.
func LoadCriteria(ctx context.Context, path string, maxBytes int64) (Criteria, error) {
	if err := ctx.Err(); err != nil {
		return Criteria{}, contextEvalError("criteria", err)
	}
	if maxBytes <= 0 {
		return Criteria{}, typedError(InvalidInput, "criteria", "max_bytes", nil)
	}
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || info.Size() > maxBytes {
		return Criteria{}, typedError(InvalidInput, "criteria", "file", nil)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return Criteria{}, typedError(InvalidInput, "criteria", "file", nil)
	}
	var wire criteriaWire
	if err := decodeStrict(data, &wire); err != nil {
		return Criteria{}, typedError(InvalidInput, "criteria", "document", nil)
	}
	if wire.SchemaVersion != 1 {
		return Criteria{}, typedError(UnsupportedVersion, "criteria", "schema_version", nil)
	}
	if wire.FailFast == nil {
		return Criteria{}, typedError(InvalidInput, "criteria", "fail_fast", nil)
	}
	value := Criteria{ID: wire.CriteriaID, Version: wire.CriteriaVersion, Digest: digestBytes(data), MinimumPPM: wire.MinimumPPM, HardFails: wire.HardFailCategories, MaxNegativeDeltaPPM: wire.MaxNegativeDeltaPPM, FailFast: *wire.FailFast, Limits: wire.RunLimits}
	if err := validateCriteria(value); err != nil {
		return Criteria{}, err
	}
	return value, nil
}

// validateCriteria proves policy completeness, unique categories and positive non-contradictory limits.
func validateCriteria(c Criteria) error {
	if !stableID.MatchString(c.ID) || !semanticVersion.MatchString(c.Version) || len(c.MinimumPPM) != 5 || c.MaxNegativeDeltaPPM == nil {
		return typedError(InvalidInput, "criteria", "identity", nil)
	}
	for _, name := range []MetricName{MetricConstraintCoverage, MetricPlanValidity, MetricOperationSelection, MetricInsufficientContext, MetricLimitCompliance} {
		v, ok := c.MinimumPPM[name]
		if !ok || v < 0 || v > 1_000_000 {
			return typedError(InvalidInput, "criteria", "minimum_ppm", nil)
		}
	}
	seen := map[InventionCategory]bool{}
	for _, v := range c.HardFails {
		if seen[v] || !validInvention(v) {
			return typedError(InvalidInput, "criteria", "hard_fail_categories", nil)
		}
		seen[v] = true
	}
	requiredDeltas := []string{string(MetricConstraintCoverage), string(MetricPlanValidity), string(MetricOperationSelection), string(MetricInsufficientContext), string(MetricLimitCompliance), string(InventionEndpoint), string(InventionCapability), string(InventionIdentity), string(InventionCredential), string(InventionEvidence)}
	if len(c.MaxNegativeDeltaPPM) != len(requiredDeltas) {
		return typedError(InvalidInput, "criteria", "max_negative_delta_ppm", nil)
	}
	for _, key := range requiredDeltas {
		v, ok := c.MaxNegativeDeltaPPM[key]
		if !ok || v < 0 || v > 1_000_000 {
			return typedError(InvalidInput, "criteria", "max_negative_delta_ppm", nil)
		}
	}
	for _, v := range c.MaxNegativeDeltaPPM {
		if v < 0 || v > 1_000_000 {
			return typedError(InvalidInput, "criteria", "max_negative_delta_ppm", nil)
		}
	}
	l := c.Limits
	if l.MaxFiles <= 0 || l.MaxTotalBytes <= 0 || l.MaxCases <= 0 || l.MaxReplayBytes <= 0 || l.MaxResultBytes <= 0 || l.MaxArtifactBytes <= 0 || l.MaxDurationMillis <= 0 || l.MaxArtifactBytes < l.MaxResultBytes {
		return typedError(InvalidInput, "criteria", "run_limits", nil)
	}
	if _, ok := durationFromMilliseconds(l.MaxDurationMillis); !ok {
		return typedError(LimitExceeded, "criteria", "run_limits.max_duration_millis", nil)
	}
	return nil
}

// validInvention constrains hard-fail configuration to the five structural oracle categories.
func validInvention(v InventionCategory) bool {
	return v == InventionEndpoint || v == InventionCapability || v == InventionIdentity || v == InventionCredential || v == InventionEvidence
}
