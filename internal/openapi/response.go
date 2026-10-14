package openapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"mime"
	"reflect"
	"strconv"
	"strings"
)

// ValidateResponse validates a bounded observation without persisting evidence or changing run state.
func (c *Contract) ValidateResponse(ctx context.Context, key OperationKey, observed ObservedResponse) (ResponseValidation, error) {
	if err := ctx.Err(); err != nil {
		return ResponseValidation{}, cancelledError("response", err)
	}
	key.Method = strings.ToUpper(strings.TrimSpace(key.Method))
	op, ok := c.findOperation(key)
	if !ok {
		return ResponseValidation{}, typedError(InvalidInput, "operation", errors.New("unknown operation"))
	}
	if observed.Status < 100 || observed.Status > 599 {
		return ResponseValidation{}, typedError(InvalidInput, "response.status", errors.New("status outside HTTP range"))
	}
	if len(observed.Body) > c.maxBody {
		return ResponseValidation{}, typedError(LimitExceeded, "response.body", errors.New("observed body budget"))
	}
	headers, err := canonicalizeObservedHeaders(observed.Headers)
	if err != nil {
		return ResponseValidation{}, err
	}
	selector, response, ok := selectResponse(op.Responses, observed.Status)
	if !ok {
		v := []Violation{{Category: "status_not_declared", Location: "response.status", Keyword: "status"}}
		return ResponseValidation{Conforms: false, Violations: v}, nil
	}
	violations := validateHeaders(response.Headers, headers)
	if err := ctx.Err(); err != nil {
		return ResponseValidation{}, cancelledError("response", err)
	}
	violations = append(violations, validateBody(response.Content, headers, observed.Body)...)
	if err := ctx.Err(); err != nil {
		return ResponseValidation{}, cancelledError("response", err)
	}
	sortViolations(violations)
	return ResponseValidation{Conforms: len(violations) == 0, ResponseSelector: selector, Violations: violations}, nil
}

// canonicalizeObservedHeaders creates one stable case-insensitive view and rejects ambiguous spellings before validation.
func canonicalizeObservedHeaders(observed map[string][]string) (map[string][]string, error) {
	canonical := make(map[string][]string, len(observed))
	for _, name := range sortedKeys(observed) {
		folded := strings.ToLower(name)
		if _, exists := canonical[folded]; exists {
			return nil, typedError(InvalidInput, "response.headers", errors.New("duplicate case-insensitive header"))
		}
		values := append([]string(nil), observed[name]...)
		if folded == "content-type" && len(values) > 1 {
			return nil, typedError(InvalidInput, "response.headers", errors.New("ambiguous content type header"))
		}
		canonical[folded] = values
	}
	return canonical, nil
}

// findOperation locates an operation in the immutable sorted snapshot.
func (c *Contract) findOperation(key OperationKey) (OperationDescriptor, bool) {
	for _, op := range c.operations {
		if op.Key == key {
			return op, true
		}
	}
	return OperationDescriptor{}, false
}

// selectResponse applies exact, class range and default precedence.
func selectResponse(responses map[string]Response, status int) (string, Response, bool) {
	exact := strconv.Itoa(status)
	if r, ok := responses[exact]; ok {
		return exact, r, true
	}
	class := strconv.Itoa(status/100) + "XX"
	if r, ok := responses[class]; ok {
		return class, r, true
	}
	r, ok := responses["DEFAULT"]
	return "default", r, ok
}

// validateHeaders checks required and typed declared headers case-insensitively.
func validateHeaders(declared map[string]Header, observed map[string][]string) []Violation {
	var out []Violation
	for headerIndex, name := range sortedKeys(declared) {
		h := declared[name]
		values, ok := observed[strings.ToLower(name)]
		loc := "response.headers/item[" + strconv.Itoa(headerIndex) + "]"
		if h.Required && !ok {
			out = append(out, Violation{Category: "required_header_missing", Location: loc, Keyword: "required"})
			continue
		}
		schema := h.Schema
		if schema == nil {
			for _, mediaType := range sortedKeys(h.Content) {
				schema = h.Content[mediaType].Schema
				break
			}
		}
		if ok && schema != nil {
			for _, value := range values {
				if !headerConforms(schema, value) {
					out = append(out, Violation{Category: "header_invalid", Location: loc, Keyword: "type"})
					break
				}
			}
		}
	}
	return out
}

// headerConforms validates scalar header syntax against the declared primitive type.
func headerConforms(schema *Schema, value string) bool {
	var candidate any = value
	switch schema.Type {
	case "integer":
		if _, err := strconv.ParseInt(value, 10, 64); err != nil {
			return false
		}
		candidate = json.Number(value)
	case "number":
		if _, err := strconv.ParseFloat(value, 64); err != nil {
			return false
		}
		candidate = json.Number(value)
	case "boolean":
		parsed, err := strconv.ParseBool(value)
		if err != nil {
			return false
		}
		candidate = parsed
	}
	if schema.validator != nil {
		return schema.validator.Validate(candidate) == nil
	}
	return true
}

// validateBody selects a compatible representation and validates its schema.
func validateBody(content map[string]MediaType, headers map[string][]string, body []byte) []Violation {
	if len(body) == 0 || len(content) == 0 {
		return nil
	}
	contentType := ""
	if values := headers["content-type"]; len(values) == 1 {
		contentType = values[0]
	}
	base, _, err := mime.ParseMediaType(contentType)
	if err != nil || base == "" {
		return []Violation{{Category: "media_type_not_declared", Location: "response.headers/content-type", Keyword: "content"}}
	}
	selected, ok := selectMedia(content, strings.ToLower(base))
	if !ok {
		return []Violation{{Category: "media_type_not_declared", Location: "response.headers/content-type", Keyword: "content"}}
	}
	var value any
	if isJSONMedia(base) {
		if !json.Valid(body) {
			return []Violation{{Category: "body_invalid", Location: "response.body", Keyword: "syntax"}}
		}
		dec := json.NewDecoder(bytes.NewReader(body))
		dec.UseNumber()
		if err := dec.Decode(&value); err != nil {
			return []Violation{{Category: "body_invalid", Location: "response.body", Keyword: "syntax"}}
		}
	} else {
		value = string(body)
	}
	if selected.Schema != nil && selected.Schema.validator != nil {
		return compiledSchemaViolations(selected.Schema.validator, value, "response.body")
	}
	var out []Violation
	validateSchema(selected.Schema, value, "response.body", &out, map[*Schema]bool{})
	return out
}

// selectMedia applies exact media type before subtype and global wildcards.
func selectMedia(content map[string]MediaType, observed string) (MediaType, bool) {
	if v, ok := content[observed]; ok {
		return v, true
	}
	parts := strings.SplitN(observed, "/", 2)
	if len(parts) == 2 {
		if v, ok := content[parts[0]+"/*"]; ok {
			return v, true
		}
	}
	v, ok := content["*/*"]
	return v, ok
}

// isJSONMedia identifies JSON and structured-suffix JSON representations.
func isJSONMedia(mediaType string) bool {
	mediaType = strings.ToLower(mediaType)
	return mediaType == "application/json" || strings.HasSuffix(mediaType, "+json")
}

// validateSchema recursively emits value-free violations for the supported deterministic subset.
func validateSchema(schema *Schema, value any, location string, out *[]Violation, seen map[*Schema]bool) {
	if schema == nil || schema.ID != "" {
		return
	}
	if value == nil {
		if !schema.Nullable {
			*out = append(*out, Violation{Category: "schema_violation", Location: location, Keyword: "type"})
		}
		return
	}
	if seen[schema] {
		return
	}
	seen[schema] = true
	defer delete(seen, schema)
	if len(schema.Enum) > 0 && !enumContains(schema.Enum, value) {
		*out = append(*out, Violation{Category: "schema_violation", Location: location, Keyword: "enum"})
	}
	switch schema.Type {
	case "object":
		obj, ok := value.(map[string]any)
		if !ok {
			*out = append(*out, Violation{Category: "schema_violation", Location: location, Keyword: "type"})
			return
		}
		propertyNames := sortedKeys(schema.Properties)
		propertyIndex := make(map[string]int, len(propertyNames))
		for index, name := range propertyNames {
			propertyIndex[name] = index
		}
		for _, name := range schema.Required {
			child := schema.Properties[name]
			if child != nil && child.WriteOnly {
				continue
			}
			if _, ok := obj[name]; !ok {
				*out = append(*out, Violation{Category: "schema_violation", Location: location + "/property[" + strconv.Itoa(propertyIndex[name]) + "]", Keyword: "required"})
			}
		}
		for index, name := range propertyNames {
			if childValue, ok := obj[name]; ok {
				validateSchema(schema.Properties[name], childValue, location+"/property["+strconv.Itoa(index)+"]", out, seen)
			}
		}
	case "array":
		arr, ok := value.([]any)
		if !ok {
			*out = append(*out, Violation{Category: "schema_violation", Location: location, Keyword: "type"})
			return
		}
		for i, item := range arr {
			validateSchema(schema.Items, item, fmt.Sprintf("%s/%d", location, i), out, seen)
		}
	case "string":
		if _, ok := value.(string); !ok {
			*out = append(*out, Violation{Category: "schema_violation", Location: location, Keyword: "type"})
		}
	case "integer":
		if !isInteger(value) {
			*out = append(*out, Violation{Category: "schema_violation", Location: location, Keyword: "type"})
		}
	case "number":
		if !isNumber(value) {
			*out = append(*out, Violation{Category: "schema_violation", Location: location, Keyword: "type"})
		}
	case "boolean":
		if _, ok := value.(bool); !ok {
			*out = append(*out, Violation{Category: "schema_violation", Location: location, Keyword: "type"})
		}
	}
}

// enumContains compares normalized JSON values without including them in diagnostics.
func enumContains(values []any, observed any) bool {
	for _, v := range values {
		if reflect.DeepEqual(normalizeNumber(v), normalizeNumber(observed)) {
			return true
		}
	}
	return false
}

// normalizeNumber canonicalizes numeric values for enum comparison.
func normalizeNumber(v any) any {
	switch n := v.(type) {
	case json.Number:
		return n.String()
	default:
		return v
	}
}

// isInteger recognizes JSON numbers with integral lexical form.
func isInteger(v any) bool {
	switch n := v.(type) {
	case json.Number:
		_, err := strconv.ParseInt(n.String(), 10, 64)
		return err == nil
	case float64:
		return n == float64(int64(n))
	case int:
		return true
	default:
		return false
	}
}

// isNumber recognizes decoded JSON numeric values.
func isNumber(v any) bool {
	switch v.(type) {
	case json.Number, float64, float32, int, int64, int32:
		return true
	default:
		return false
	}
}
