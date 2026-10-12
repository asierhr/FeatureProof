package httptool

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"net/url"
	"reflect"
	"sort"
	"strings"
	"unicode/utf8"
)

// Validate enforces the complete input contract before registry policy or execution can authorize it.
func (t *Tool) Validate(input map[string]any) error {
	_, err := t.validateInput(input)
	return err
}

// validateInput translates untrusted maps into one immutable, pre-encoded request description.
func (t *Tool) validateInput(input map[string]any) (validatedInput, error) {
	if input == nil {
		return validatedInput{}, inputError("input", "required", nil)
	}
	allowed := map[string]struct{}{"method": {}, "path": {}, "query": {}, "headers": {}, "body": {}}
	for keyIndex, key := range sortedExternalKeys(input) {
		if _, ok := allowed[key]; !ok {
			return validatedInput{}, inputError(fmt.Sprintf("input.members[%d]", keyIndex), "unknown_field", nil)
		}
	}
	method, ok := input["method"].(string)
	if !ok || method == "" {
		return validatedInput{}, inputError("method", "invalid_type", nil)
	}
	if _, ok := t.methods[method]; !ok || method != strings.ToUpper(method) || !validToken(method) {
		return validatedInput{}, inputError("method", "not_allowed", nil)
	}
	rawPath, ok := input["path"].(string)
	if !ok {
		return validatedInput{}, inputError("path", "invalid_type", nil)
	}
	path, err := parsePath(rawPath, false)
	if err != nil {
		return validatedInput{}, inputError("path", "invalid_path", err)
	}
	query := map[string][]string{}
	if raw, exists := input["query"]; exists {
		query, err = stringLists(raw, "query", nil, false)
		if err != nil {
			return validatedInput{}, err
		}
	}
	headers := map[string][]string{}
	if raw, exists := input["headers"]; exists {
		headers, err = stringLists(raw, "headers", t.requestHeaders, true)
		if err != nil {
			return validatedInput{}, err
		}
	}
	validated := validatedInput{method: method, path: path, query: query, headers: headers}
	if body, exists := input["body"]; exists {
		if err := validateJSON(body, "body"); err != nil {
			return validatedInput{}, err
		}
		encoded, err := json.Marshal(body)
		if err != nil {
			return validatedInput{}, inputError("body", "encode", err)
		}
		if len(encoded) > t.maxRequestBody {
			return validatedInput{}, inputError("body", "request_limit", nil)
		}
		validated.body, validated.bodyPresent = encoded, true
	}
	requestURL := t.buildURL(validated)
	if len(requestURL.String()) > t.maxURL {
		return validatedInput{}, inputError("request.url", "request_limit", nil)
	}
	request, err := t.request(context.Background(), validated)
	if err != nil {
		return validatedInput{}, inputError("request", "encode", err)
	}
	if err := t.validateSerializedRequestHeaders(request); err != nil {
		return validatedInput{}, inputError("headers", "request_limit", nil)
	}
	return validated, nil
}

// parsePath validates hierarchy before decoding path segments for safe composition and comparison.
func parsePath(raw string, target bool) ([]string, error) {
	if !utf8.ValidString(raw) || strings.ContainsAny(raw, "?#\\") || strings.HasPrefix(raw, "//") {
		return nil, fmt.Errorf("forbidden path syntax")
	}
	for _, r := range raw {
		if r < 0x20 || r == 0x7f {
			return nil, fmt.Errorf("control byte")
		}
	}
	trimmed := strings.TrimPrefix(raw, "/")
	if trimmed == "" {
		return nil, nil
	}
	rawSegments := strings.Split(trimmed, "/")
	segments := make([]string, 0, len(rawSegments))
	for _, segment := range rawSegments {
		if segment == "" {
			if target {
				continue
			}
			return nil, fmt.Errorf("empty segment")
		}
		decoded, err := url.PathUnescape(segment)
		if err != nil || decoded == "." || decoded == ".." || strings.ContainsAny(decoded, "/\\") {
			return nil, fmt.Errorf("unsafe segment")
		}
		for _, r := range decoded {
			if r < 0x20 || r == 0x7f {
				return nil, fmt.Errorf("control byte")
			}
		}
		segments = append(segments, decoded)
	}
	return segments, nil
}

// stringLists validates structured query/header maps without reinterpreting embedded URL syntax.
func stringLists(raw any, field string, allowlist map[string]string, headers bool) (map[string][]string, error) {
	result := map[string][]string{}
	seenHeaders := map[string]struct{}{}
	var source map[string]any
	switch value := raw.(type) {
	case map[string]any:
		source = value
	case map[string][]string:
		source = make(map[string]any, len(value))
		for key, entries := range value {
			source[key] = entries
		}
	default:
		return nil, inputError(field, "invalid_type", nil)
	}
	for keyIndex, key := range sortedExternalKeys(source) {
		rawValues := source[key]
		path := fmt.Sprintf("%s.members[%d]", field, keyIndex)
		if headers {
			if !validToken(key) {
				return nil, inputError(path, "invalid_header", nil)
			}
			lower := strings.ToLower(key)
			if _, duplicate := seenHeaders[lower]; duplicate {
				return nil, inputError(path, "duplicate_header", nil)
			}
			seenHeaders[lower] = struct{}{}
			if _, sensitive := sensitiveHeaders[lower]; sensitive {
				return nil, inputError(path, "sensitive_header", nil)
			}
			if _, hop := hopByHopHeaders[lower]; hop {
				return nil, inputError(path, "hop_by_hop_header", nil)
			}
			canonical, ok := allowlist[lower]
			if !ok {
				return nil, inputError(path, "not_allowed", nil)
			}
			key = canonical
		}
		values, ok := toStrings(rawValues)
		if !ok {
			return nil, inputError(path, "invalid_type", nil)
		}
		for index, value := range values {
			if !validHeaderValue(value) {
				return nil, inputError(fmt.Sprintf("%s[%d]", path, index), "invalid_value", nil)
			}
		}
		result[key] = append([]string(nil), values...)
	}
	return result, nil
}

// validHeaderValue rejects line injection and control bytes before a request may trigger DNS.
func validHeaderValue(value string) bool {
	if !utf8.ValidString(value) {
		return false
	}
	for i := 0; i < len(value); i++ {
		if value[i] == '\t' {
			continue
		}
		if value[i] < 0x20 || value[i] == 0x7f {
			return false
		}
	}
	return true
}

// toStrings accepts the two slice forms produced by decoded JSON and direct Go callers.
func toStrings(raw any) ([]string, bool) {
	switch values := raw.(type) {
	case []string:
		return append([]string(nil), values...), true
	case []any:
		out := make([]string, len(values))
		for i, value := range values {
			entry, ok := value.(string)
			if !ok {
				return nil, false
			}
			out[i] = entry
		}
		return out, true
	default:
		return nil, false
	}
}

// validateJSON rejects non-JSON values and non-finite numbers with a precise field path.
func validateJSON(value any, path string) error {
	if value == nil {
		return nil
	}
	switch typed := value.(type) {
	case string, bool:
		return nil
	case json.Number:
		if _, err := json.Marshal(typed); err != nil {
			return inputError(path, "invalid_json_number", err)
		}
		return nil
	case float32:
		if math.IsNaN(float64(typed)) || math.IsInf(float64(typed), 0) {
			return inputError(path, "invalid_json_number", nil)
		}
		return nil
	case float64:
		if math.IsNaN(typed) || math.IsInf(typed, 0) {
			return inputError(path, "invalid_json_number", nil)
		}
		return nil
	case int, int8, int16, int32, int64, uint, uint8, uint16, uint32, uint64:
		return nil
	case map[string]any:
		for keyIndex, key := range sortedExternalKeys(typed) {
			if err := validateJSON(typed[key], fmt.Sprintf("%s.members[%d]", path, keyIndex)); err != nil {
				return err
			}
		}
		return nil
	case []any:
		for i, child := range typed {
			if err := validateJSON(child, fmt.Sprintf("%s[%d]", path, i)); err != nil {
				return err
			}
		}
		return nil
	default:
		kind := reflect.TypeOf(value).Kind()
		if kind == reflect.Slice || kind == reflect.Array || kind == reflect.Map {
			return inputError(path, "invalid_json_type", nil)
		}
		return inputError(path, "invalid_json_type", nil)
	}
}

// sortedExternalKeys provides stable map traversal without placing external names in error fields.
func sortedExternalKeys[T any](values map[string]T) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

// headerBytes computes a deterministic encoded-size upper bound before the request reaches DNS.
func headerBytes(headers map[string][]string) int {
	total := 0
	for name, values := range headers {
		for _, value := range values {
			total += len(name) + 2 + len(value) + 2
		}
	}
	return total
}

// inputError centralizes typed, secret-free validation failures.
func inputError(field, category string, cause error) error {
	return &InputError{Field: field, Category: category, Cause: cause}
}

// buildURL combines decoded target and input segments, allowing net/url to escape each resulting path safely.
func (t *Tool) buildURL(input validatedInput) *url.URL {
	segments := append(append([]string(nil), t.scope.baseSegments...), input.path...)
	path := "/"
	if len(segments) > 0 {
		path += strings.Join(segments, "/")
	}
	query := url.Values{}
	for key, values := range input.query {
		for _, value := range values {
			query.Add(key, value)
		}
	}
	return &url.URL{Scheme: t.scope.scheme, Host: t.scope.authority(), Path: path, RawQuery: query.Encode()}
}

// responseHeaderBytes measures observed header fields before any allowlisted value crosses the adapter boundary.
func responseHeaderBytes(headers http.Header) int {
	return headerBytes(map[string][]string(headers))
}
