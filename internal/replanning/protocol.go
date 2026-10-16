package replanning

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"unicode/utf8"

	"github.com/asierhr/featureProof/internal/agentplanning"
)

// decisionDTO binds exact wire fields before domain validation.
type decisionDTO struct {
	Version      int          `json:"version"`
	Kind         DecisionKind `json:"kind"`
	Terminate    *struct{}    `json:"terminate,omitempty"`
	Insufficient *struct {
		Items []agentplanning.MissingItem `json:"items"`
	} `json:"insufficient_context,omitempty"`
	Additional *AdditionalBatch `json:"additional_checks,omitempty"`
}

// DecodeDecision accepts one bounded, UTF-8, duplicate-free protocol-v1 branch.
func DecodeDecision(raw []byte, limits Limits, constraints []string) (Decision, error) {
	if int64(len(raw)) > limits.MaxOutputBytes || len(raw) == 0 || !utf8.Valid(raw) {
		return Decision{}, failure(CategoryInvalidDecision, "decode", "bytes")
	}
	if err := uniqueJSON(raw); err != nil {
		return Decision{}, failure(CategoryInvalidDecision, "decode", "json")
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	decoder.UseNumber()
	var dto decisionDTO
	if err := decoder.Decode(&dto); err != nil {
		return Decision{}, failure(CategoryInvalidDecision, "decode", "json")
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return Decision{}, failure(CategoryInvalidDecision, "decode", "json")
	}
	if dto.Version != 1 {
		return Decision{}, failure(CategoryInvalidDecision, "decode", "version")
	}
	branches := 0
	if dto.Terminate != nil {
		branches++
	}
	if dto.Insufficient != nil {
		branches++
	}
	if dto.Additional != nil {
		branches++
	}
	if branches != 1 {
		return Decision{}, failure(CategoryInvalidDecision, "decode", "branch")
	}
	switch dto.Kind {
	case DecisionTerminate:
		if dto.Terminate == nil {
			return Decision{}, failure(CategoryInvalidDecision, "decode", "kind")
		}
		return Decision{Version: 1, Kind: dto.Kind}, nil
	case DecisionInsufficientContext:
		if dto.Insufficient == nil || len(dto.Insufficient.Items) == 0 || len(dto.Insufficient.Items) > limits.MaxMissingItems {
			return Decision{}, failure(CategoryInvalidDecision, "decode", "missing_items")
		}
		known := make(map[string]bool, len(constraints))
		for _, id := range constraints {
			known[id] = true
		}
		for _, item := range dto.Insufficient.Items {
			if (item.Kind != agentplanning.MissingQuestion && item.Kind != agentplanning.MissingEvidence) || len(item.Request) == 0 || len(item.Request) > 512 || len(item.ConstraintIDs) == 0 || len(item.ConstraintIDs) > limits.MaxConstraints {
				return Decision{}, failure(CategoryInvalidDecision, "decode", "missing_item")
			}
			seen := map[string]bool{}
			for _, id := range item.ConstraintIDs {
				if !known[id] || seen[id] {
					return Decision{}, failure(CategoryInvalidDecision, "decode", "constraint_id")
				}
				seen[id] = true
			}
		}
		return Decision{Version: 1, Kind: dto.Kind, Missing: append([]agentplanning.MissingItem(nil), dto.Insufficient.Items...)}, nil
	case DecisionAdditionalChecks:
		batch := dto.Additional
		if batch == nil || !safeEventID(batch.BatchID) || len(batch.Checks) == 0 || len(batch.Checks) > limits.MaxChecks {
			return Decision{}, failure(CategoryInvalidDecision, "decode", "batch")
		}
		known := make(map[string]bool, len(constraints))
		for _, id := range constraints {
			known[id] = true
		}
		seen := map[string]bool{}
		for _, check := range batch.Checks {
			if !safeEventID(check.ID) || seen[check.ID] || !known[check.ConstraintID] || check.Capability == "" || len(check.Steps) == 0 || len(check.Steps) > limits.MaxStepsPerCheck || len(check.Evidence) == 0 {
				return Decision{}, failure(CategoryInvalidDecision, "decode", "check")
			}
			seen[check.ID] = true
			for _, step := range check.Steps {
				if len(step.Assertions) > limits.MaxAssertionsPerStep {
					return Decision{}, failure(CategoryInvalidDecision, "decode", "assertions")
				}
			}
		}
		return Decision{Version: 1, Kind: dto.Kind, Additional: batch}, nil
	default:
		return Decision{}, failure(CategoryInvalidDecision, "decode", "kind")
	}
}

// uniqueJSON rejects duplicate members and malformed or excessively deep JSON before typed decode.
func uniqueJSON(raw []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if err := uniqueValue(decoder, 0); err != nil {
		return err
	}
	if _, err := decoder.Token(); !errors.Is(err, io.EOF) {
		return errors.New("trailing data")
	}
	return nil
}

// uniqueValue validates nested object identity with a fixed recursion ceiling.
func uniqueValue(decoder *json.Decoder, depth int) error {
	if depth > 64 {
		return errors.New("depth")
	}
	token, err := decoder.Token()
	if err != nil {
		return err
	}
	delim, ok := token.(json.Delim)
	if !ok {
		return nil
	}
	switch delim {
	case '{':
		seen := map[string]bool{}
		for decoder.More() {
			key, err := decoder.Token()
			if err != nil {
				return err
			}
			name, ok := key.(string)
			if !ok || seen[name] {
				return errors.New("duplicate member")
			}
			seen[name] = true
			if err := uniqueValue(decoder, depth+1); err != nil {
				return err
			}
		}
	case '[':
		for decoder.More() {
			if err := uniqueValue(decoder, depth+1); err != nil {
				return err
			}
		}
	default:
		return errors.New("delimiter")
	}
	_, err = decoder.Token()
	return err
}
