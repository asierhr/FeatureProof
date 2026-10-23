package fixture

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"sort"

	"github.com/asierhr/featureProof/internal/specification"
)

// Sanitizer rejects sensitive values and returns an isolated safe value before durable confirmation.
type Sanitizer interface {
	Sanitize(context.Context, any) (any, error)
}

// ExtractCaptures selects only declared paths from an accepted, complete response and enforces per-value and aggregate bounds.
func ExtractCaptures(ctx context.Context, runID string, fixture specification.Fixture, bindingDigest string, result HTTPResult, sanitizer Sanitizer, maxTotalBytes int) ([]Capture, error) {
	if result.Truncated || sanitizer == nil || maxTotalBytes <= 0 {
		return nil, errFor(ctx, CategoryCapture, "capture", fixture.ID, nil)
	}
	seen, total := map[string]bool{}, 0
	out := make([]Capture, 0, len(fixture.Captures))
	for _, declaration := range fixture.Captures {
		if seen[declaration.Name] {
			return nil, errFor(ctx, CategoryCapture, "capture", fixture.ID, nil)
		}
		seen[declaration.Name] = true
		value, ok := selectCapture(result.Body, declaration.Path)
		if !ok || !matchesType(value, declaration.ValueType) {
			return nil, errFor(ctx, CategoryCapture, "capture", fixture.ID, nil)
		}
		safe, err := sanitizer.Sanitize(ctx, value)
		if err != nil || !matchesType(safe, declaration.ValueType) {
			return nil, errFor(ctx, CategoryCapture, "capture", fixture.ID, err)
		}
		raw, err := json.Marshal(safe)
		if err != nil || len(raw) > declaration.MaxBytes || total+len(raw) > maxTotalBytes {
			return nil, errFor(ctx, CategoryCapture, "capture", fixture.ID, err)
		}
		total += len(raw)
		sum := sha256.Sum256(raw)
		out = append(out, Capture{RunID: runID, FixtureID: fixture.ID, Name: declaration.Name, ValueType: declaration.ValueType, Value: safe, Bytes: len(raw), Digest: hex.EncodeToString(sum[:]), BindingDigest: bindingDigest})
	}
	return out, nil
}

// selectCapture walks field/index segments without JSONPath expressions or coercion.
func selectCapture(root any, path []specification.CapturePathSegment) (any, bool) {
	current := root
	for _, segment := range path {
		if segment.Field != nil {
			object, ok := current.(map[string]any)
			if !ok {
				return nil, false
			}
			current, ok = object[*segment.Field]
			if !ok {
				return nil, false
			}
		} else if segment.Index != nil {
			array, ok := current.([]any)
			if !ok || *segment.Index < 0 || *segment.Index >= len(array) {
				return nil, false
			}
			current = array[*segment.Index]
		} else {
			return nil, false
		}
	}
	return current, true
}

// matchesType enforces exact JSON capture/reference types and rejects coercion.
func matchesType(value any, expected string) bool {
	switch expected {
	case "string":
		_, ok := value.(string)
		return ok
	case "boolean":
		_, ok := value.(bool)
		return ok
	case "number":
		switch value.(type) {
		case float64, float32, int, int8, int16, int32, int64, uint, uint8, uint16, uint32, uint64, json.Number:
			return true
		}
		return false
	case "object":
		_, ok := value.(map[string]any)
		return ok
	case "array":
		_, ok := value.([]any)
		return ok
	}
	return false
}

// ResolveInput replaces references only from confirmed captures of the same run and returns detached structured data.
func ResolveInput(runID string, input specification.FixtureInput, captures map[string]Capture) (HTTPRequest, error) {
	request := HTTPRequest{RunID: runID, PathParameters: map[string]any{}, Query: map[string]any{}}
	for _, target := range []struct {
		source map[string]specification.FixtureValue
		output map[string]any
	}{{input.PathParameters, request.PathParameters}, {input.Query, request.Query}} {
		keys := make([]string, 0, len(target.source))
		for key := range target.source {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		for _, key := range keys {
			value, err := resolveValue(runID, target.source[key], captures)
			if err != nil {
				return HTTPRequest{}, err
			}
			target.output[key] = value
		}
	}
	if input.Body != nil {
		value, err := resolveValue(runID, *input.Body, captures)
		if err != nil {
			return HTTPRequest{}, err
		}
		request.Body = value
	}
	return request, nil
}

// resolveValue recursively clones literals and resolves an exact private capture record.
func resolveValue(runID string, value specification.FixtureValue, captures map[string]Capture) (any, error) {
	switch value.Kind {
	case "reference":
		if value.Reference == nil {
			break
		}
		key := captureKey(value.Reference.FixtureID, value.Reference.Output)
		capture, ok := captures[key]
		if !ok || capture.RunID != runID || capture.FixtureID != value.Reference.FixtureID || capture.Name != value.Reference.Output || capture.ValueType != value.Reference.ValueType {
			return nil, &Error{Category: CategoryInputUnavailable, Phase: "resolve", FixtureID: value.Reference.FixtureID}
		}
		return cloneJSON(capture.Value)
	case "literal":
		return cloneJSON(value.Literal)
	case "object":
		out := map[string]any{}
		for key, child := range value.Object {
			resolved, err := resolveValue(runID, child, captures)
			if err != nil {
				return nil, err
			}
			out[key] = resolved
		}
		return out, nil
	case "array":
		out := make([]any, len(value.Array))
		for i, child := range value.Array {
			resolved, err := resolveValue(runID, child, captures)
			if err != nil {
				return nil, err
			}
			out[i] = resolved
		}
		return out, nil
	}
	return nil, &Error{Category: CategoryInvalid, Phase: "resolve"}
}

// cloneJSON returns independent finite JSON-compatible data and propagates any invalid or non-finite representation.
func cloneJSON(value any) (any, error) {
	raw, err := json.Marshal(value)
	if err != nil {
		return nil, &Error{Category: CategoryInvalid, Phase: "clone_json", Cause: err}
	}
	var out any
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, &Error{Category: CategoryInvalid, Phase: "clone_json", Cause: err}
	}
	return out, nil
}

// captureKey names a private same-run capture without exposing its value.
func captureKey(fixtureID, name string) string { return fixtureID + "\x00" + name }
