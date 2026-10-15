package agentplanning

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
)

// planningDecisionDTO is the strict wire representation of the planning union.
type planningDecisionDTO struct {
	Version        int             `json:"version"`
	Kind           DecisionKind    `json:"kind"`
	Plan           *CandidatePlan  `json:"plan,omitempty"`
	MissingContext *MissingContext `json:"missing_context,omitempty"`
}

// Decode accepts exactly one bounded, duplicate-free protocol v1 JSON object.
func Decode(raw []byte, maxBytes int64) (PlanningDecision, error) {
	if maxBytes <= 0 || int64(len(raw)) > maxBytes {
		return PlanningDecision{}, typedError(CategoryLimitExceeded, "decode", "output", nil)
	}
	if err := rejectDuplicateKeys(raw); err != nil {
		return PlanningDecision{}, typedError(CategoryInvalidModelResponse, "decode", "json", nil)
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	decoder.DisallowUnknownFields()
	var dto planningDecisionDTO
	if err := decoder.Decode(&dto); err != nil {
		return PlanningDecision{}, typedError(CategoryInvalidModelResponse, "decode", "json", nil)
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return PlanningDecision{}, typedError(CategoryInvalidModelResponse, "decode", "json", nil)
	}
	if dto.Version != 1 {
		return PlanningDecision{}, typedError(CategoryInvalidModelResponse, "decode", "version", nil)
	}
	validPlan := dto.Kind == DecisionPlan && dto.Plan != nil && dto.MissingContext == nil
	validMissing := dto.Kind == DecisionInsufficientContext && dto.Plan == nil && dto.MissingContext != nil
	if !validPlan && !validMissing {
		return PlanningDecision{}, typedError(CategoryInvalidModelResponse, "decode", "kind", nil)
	}
	return PlanningDecision{Version: dto.Version, Kind: dto.Kind, Plan: dto.Plan, MissingContext: dto.MissingContext}, nil
}

// rejectDuplicateKeys walks one JSON value and rejects duplicate object members and trailing data.
func rejectDuplicateKeys(raw []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if err := consumeUniqueValue(decoder); err != nil {
		return err
	}
	if _, err := decoder.Token(); !errors.Is(err, io.EOF) {
		return errors.New("trailing JSON")
	}
	return nil
}

// consumeUniqueValue recursively verifies uniqueness in every JSON object.
func consumeUniqueValue(decoder *json.Decoder) error {
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
			keyToken, err := decoder.Token()
			if err != nil {
				return err
			}
			key, ok := keyToken.(string)
			if !ok || seen[key] {
				return errors.New("duplicate key")
			}
			seen[key] = true
			if err := consumeUniqueValue(decoder); err != nil {
				return err
			}
		}
	case '[':
		for decoder.More() {
			if err := consumeUniqueValue(decoder); err != nil {
				return err
			}
		}
	default:
		return errors.New("invalid delimiter")
	}
	_, err = decoder.Token()
	return err
}
