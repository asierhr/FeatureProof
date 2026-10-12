package assertion

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"math/big"
	"reflect"
	"sort"
	"strconv"
	"strings"

	"github.com/asierhr/featureProof/internal/evidence"
	"github.com/asierhr/featureProof/internal/planning"
)

// ValidateDefinition implements planning's consumer-owned validation port without retaining mutable state.
func (v DefinitionValidator) ValidateDefinition(declaration planning.AssertionDeclaration, fieldPath string) error {
	compiled, err := compileDeclaration(declaration, fieldPath)
	if err != nil {
		return err
	}
	return validateSensitiveLeaves(compiled.root, fieldPath, v.Policy)
}

// validateSensitiveLeaves rejects the first value-reading leaf that overlaps persisted redaction.
func validateSensitiveLeaves(current node, path string, policy *evidence.Policy) error {
	for i, child := range current.children {
		if err := validateSensitiveLeaves(child, fmt.Sprintf("%s.%s[%d]", path, current.operator, i), policy); err != nil {
			return err
		}
	}
	if len(current.children) == 0 && current.operator != "exists" && current.operator != "absent" && policy != nil && policy.OverlapsSensitive(current.path) {
		return configError(ErrorInvalidType, path+".path", fmt.Errorf("assertion_sensitive_path"))
	}
	return nil
}

// compileDeclaration validates and freezes one declaration for deterministic evaluation.
func compileDeclaration(declaration planning.AssertionDeclaration, fieldPath string) (compiledDeclaration, error) {
	return compileDeclarationContext(context.Background(), declaration, fieldPath)
}

// compileDeclarationContext validates one declaration while honoring cancellation at every recursive boundary.
func compileDeclarationContext(ctx context.Context, declaration planning.AssertionDeclaration, fieldPath string) (compiledDeclaration, error) {
	if err := ctx.Err(); err != nil {
		return compiledDeclaration{}, err
	}
	if declaration.Definition == nil {
		return compiledDeclaration{}, configError(ErrorRequired, fieldPath, nil)
	}
	root, err := compileNode(ctx, declaration.Definition, fieldPath, map[visit]bool{})
	if err != nil {
		return compiledDeclaration{}, err
	}
	if err := ctx.Err(); err != nil {
		return compiledDeclaration{}, err
	}
	return compiledDeclaration{id: declaration.ID, basePath: fieldPath, root: root}, nil
}

// visit identifies mutable collection identities while rejecting cyclic definitions.
type visit struct {
	kind reflect.Kind
	ptr  uintptr
}

// compileNode validates one exact leaf/group shape and recursively freezes its content.
func compileNode(ctx context.Context, raw any, path string, active map[visit]bool) (node, error) {
	if err := ctx.Err(); err != nil {
		return node{}, err
	}
	object, ok := raw.(map[string]any)
	if !ok || object == nil {
		return node{}, configError(ErrorInvalidShape, path, nil)
	}
	identity := visit{kind: reflect.Map, ptr: reflect.ValueOf(object).Pointer()}
	if active[identity] {
		return node{}, configError(ErrorInvalidShape, path, fmt.Errorf("cyclic definition"))
	}
	active[identity] = true
	defer delete(active, identity)
	if _, all := object["all"]; all {
		if len(object) != 1 {
			return node{}, unknownOrShape(ctx, object, path, []string{"all"})
		}
		return compileGroup(ctx, "all", object["all"], path+".all", active)
	}
	if _, anyGroup := object["any"]; anyGroup {
		if len(object) != 1 {
			return node{}, unknownOrShape(ctx, object, path, []string{"any"})
		}
		return compileGroup(ctx, "any", object["any"], path+".any", active)
	}
	return compileLeaf(ctx, object, path)
}

// unknownOrShape reports a stable member index without exposing an unexpected external key.
func unknownOrShape(ctx context.Context, object map[string]any, path string, allowed []string) error {
	allowedSet := map[string]bool{}
	for _, key := range allowed {
		allowedSet[key] = true
	}
	keys := make([]string, 0, len(object))
	for key := range object {
		if err := ctx.Err(); err != nil {
			return err
		}
		keys = append(keys, key)
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	sort.Strings(keys)
	for index, key := range keys {
		if !allowedSet[key] {
			if trustedAssertionField(key) {
				return configError(ErrorInvalidShape, path+"."+key, nil)
			}
			return configError(ErrorInvalidShape, fmt.Sprintf("%s.members[%d]", path, index), nil)
		}
	}
	return configError(ErrorInvalidShape, path, nil)
}

// trustedAssertionField recognizes only fixed v1 schema names safe to render in diagnostics.
func trustedAssertionField(field string) bool {
	switch field {
	case "all", "any", "operator", "path", "expected":
		return true
	default:
		return false
	}
}

// compileGroup validates a non-empty ordered child list and recursively compiles every child.
func compileGroup(ctx context.Context, operator string, raw any, path string, active map[visit]bool) (node, error) {
	if err := ctx.Err(); err != nil {
		return node{}, err
	}
	children, ok := raw.([]any)
	if !ok || len(children) == 0 {
		return node{}, configError(ErrorInvalidShape, path, nil)
	}
	identity := visit{kind: reflect.Slice, ptr: reflect.ValueOf(children).Pointer()}
	if active[identity] {
		return node{}, configError(ErrorInvalidShape, path, fmt.Errorf("cyclic definition"))
	}
	active[identity] = true
	defer delete(active, identity)
	out := node{operator: operator, expected: planning.AssertionValue{Present: true, Value: true}, children: make([]node, len(children))}
	for i, child := range children {
		if err := ctx.Err(); err != nil {
			return node{}, err
		}
		compiled, err := compileNode(ctx, child, fmt.Sprintf("%s[%d]", path, i), active)
		if err != nil {
			return node{}, err
		}
		out.children[i] = compiled
	}
	if err := ctx.Err(); err != nil {
		return node{}, err
	}
	return out, nil
}

// compileLeaf enforces exact fields, operator-specific expected rules, path grammar, and JSON types.
func compileLeaf(ctx context.Context, object map[string]any, path string) (node, error) {
	if err := ctx.Err(); err != nil {
		return node{}, err
	}
	operatorRaw, exists := object["operator"]
	if !exists {
		return node{}, configError(ErrorRequired, path+".operator", nil)
	}
	operator, ok := operatorRaw.(string)
	if !ok {
		return node{}, configError(ErrorInvalidType, path+".operator", nil)
	}
	if !supportedOperator(operator) {
		return node{}, configError(ErrorUnsupportedOperator, path+".operator", nil)
	}
	pathRaw, exists := object["path"]
	if !exists {
		return node{}, configError(ErrorRequired, path+".path", nil)
	}
	pathValue, ok := pathRaw.(string)
	if !ok {
		return node{}, configError(ErrorInvalidType, path+".path", nil)
	}
	tokens, err := parsePathContext(ctx, pathValue)
	if err != nil {
		return node{}, configError(ErrorInvalidPath, path+".path", err)
	}
	requiresExpected := operator != "exists" && operator != "absent"
	expected, hasExpected := object["expected"]
	allowed := []string{"operator", "path"}
	if requiresExpected {
		allowed = append(allowed, "expected")
		if !hasExpected {
			return node{}, configError(ErrorRequired, path+".expected", nil)
		}
	} else if hasExpected {
		return node{}, configError(ErrorInvalidShape, path+".expected", nil)
	}
	if len(object) != len(allowed) {
		return node{}, unknownOrShape(ctx, object, path, allowed)
	}
	out := node{operator: operator, path: pathValue, tokens: tokens}
	if requiresExpected {
		frozen, err := cloneJSONContext(ctx, expected, map[visit]bool{})
		if err != nil {
			return node{}, configError(ErrorInvalidType, path+".expected", err)
		}
		out.expected = planning.AssertionValue{Present: true, Value: frozen}
	}
	if operator != "eq" && requiresExpected {
		number, ok := exactNumberContext(ctx, expected)
		if !ok {
			if err := ctx.Err(); err != nil {
				return node{}, err
			}
			return node{}, configError(ErrorInvalidType, path+".expected", nil)
		}
		out.number = number
	}
	if err := ctx.Err(); err != nil {
		return node{}, err
	}
	return out, nil
}

// supportedOperator keeps the leaf language closed and case-sensitive.
func supportedOperator(operator string) bool {
	switch operator {
	case "eq", "exists", "absent", "gt", "gte", "lt", "lte":
		return true
	default:
		return false
	}
}

// cloneJSONContext validates and copies a JSON value while checking cancellation at collection boundaries.
func cloneJSONContext(ctx context.Context, value any, active map[visit]bool) (any, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if value == nil {
		return nil, nil
	}
	switch typed := value.(type) {
	case bool, string, json.Number:
		if number, ok := typed.(json.Number); ok {
			if _, valid := exactNumberContext(ctx, number); !valid {
				if err := ctx.Err(); err != nil {
					return nil, err
				}
				return nil, fmt.Errorf("invalid JSON number")
			}
		}
		return typed, nil
	case int, int8, int16, int32, int64, uint, uint8, uint16, uint32, uint64:
		return typed, nil
	case float32:
		if math.IsNaN(float64(typed)) || math.IsInf(float64(typed), 0) {
			return nil, fmt.Errorf("non-finite number")
		}
		return typed, nil
	case float64:
		if math.IsNaN(typed) || math.IsInf(typed, 0) {
			return nil, fmt.Errorf("non-finite number")
		}
		return typed, nil
	case map[string]any:
		identity := visit{kind: reflect.Map, ptr: reflect.ValueOf(typed).Pointer()}
		if active[identity] {
			return nil, fmt.Errorf("cyclic value")
		}
		active[identity] = true
		defer delete(active, identity)
		out := make(map[string]any, len(typed))
		for key, child := range typed {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			cloned, err := cloneJSONContext(ctx, child, active)
			if err != nil {
				return nil, err
			}
			out[key] = cloned
		}
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		return out, nil
	case []any:
		identity := visit{kind: reflect.Slice, ptr: reflect.ValueOf(typed).Pointer()}
		if active[identity] {
			return nil, fmt.Errorf("cyclic value")
		}
		active[identity] = true
		defer delete(active, identity)
		out := make([]any, len(typed))
		for i, child := range typed {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			cloned, err := cloneJSONContext(ctx, child, active)
			if err != nil {
				return nil, err
			}
			out[i] = cloned
		}
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		return out, nil
	default:
		return nil, fmt.Errorf("unsupported JSON value %T", value)
	}
}

// exactNumber converts supported finite Go number forms to a compact exact decimal for comparison.
func exactNumber(value any) (*decimalNumber, bool) {
	return exactNumberContext(context.Background(), value)
}

// exactNumberContext normalizes a finite number and checks cancellation around linear parsing work.
func exactNumberContext(ctx context.Context, value any) (*decimalNumber, bool) {
	if ctx.Err() != nil {
		return nil, false
	}
	var text string
	switch number := value.(type) {
	case int:
		text = strconv.FormatInt(int64(number), 10)
	case int8:
		text = strconv.FormatInt(int64(number), 10)
	case int16:
		text = strconv.FormatInt(int64(number), 10)
	case int32:
		text = strconv.FormatInt(int64(number), 10)
	case int64:
		text = strconv.FormatInt(number, 10)
	case uint:
		text = strconv.FormatUint(uint64(number), 10)
	case uint8:
		text = strconv.FormatUint(uint64(number), 10)
	case uint16:
		text = strconv.FormatUint(uint64(number), 10)
	case uint32:
		text = strconv.FormatUint(uint64(number), 10)
	case uint64:
		text = strconv.FormatUint(number, 10)
	case float32:
		if math.IsNaN(float64(number)) || math.IsInf(float64(number), 0) {
			return nil, false
		}
		text = strconv.FormatFloat(float64(number), 'g', -1, 32)
	case float64:
		if math.IsNaN(number) || math.IsInf(number, 0) {
			return nil, false
		}
		text = strconv.FormatFloat(number, 'g', -1, 64)
	case json.Number:
		text = string(number)
	default:
		return nil, false
	}
	number, ok := decimalRationalContext(ctx, text)
	return number, ok && ctx.Err() == nil
}

// decimalRationalContext parses one symbolic decimal and observes cancellation across each input scan.
func decimalRationalContext(ctx context.Context, text string) (*decimalNumber, bool) {
	if ctx.Err() != nil {
		return nil, false
	}
	if text == "" {
		return nil, false
	}
	sign := ""
	if text[0] == '-' {
		sign, text = "-", text[1:]
	}
	parts := []string{text}
	if separator := strings.IndexAny(text, "eE"); separator >= 0 {
		if separator == 0 || separator == len(text)-1 || strings.IndexAny(text[separator+1:], "eE") >= 0 {
			return nil, false
		}
		parts = []string{text[:separator], text[separator+1:]}
	}
	exponent := new(big.Int)
	if len(parts) == 2 {
		exponentDigits := parts[1]
		if exponentDigits[0] == '+' || exponentDigits[0] == '-' {
			exponentDigits = exponentDigits[1:]
		}
		if exponentDigits == "" {
			return nil, false
		}
		for _, digit := range exponentDigits {
			if ctx.Err() != nil || digit < '0' || digit > '9' {
				return nil, false
			}
		}
		parsed, ok := new(big.Int).SetString(parts[1], 10)
		if !ok {
			return nil, false
		}
		exponent.Set(parsed)
	}
	mantissa := parts[0]
	decimalParts := strings.Split(mantissa, ".")
	if len(decimalParts) > 2 || decimalParts[0] == "" || (len(decimalParts) == 2 && decimalParts[1] == "") {
		return nil, false
	}
	if len(decimalParts[0]) > 1 && decimalParts[0][0] == '0' {
		return nil, false
	}
	digits := decimalParts[0]
	if len(decimalParts) == 2 {
		digits += decimalParts[1]
		exponent.Sub(exponent, new(big.Int).SetUint64(uint64(len(decimalParts[1]))))
	}
	for _, digit := range digits {
		if ctx.Err() != nil {
			return nil, false
		}
		if digit < '0' || digit > '9' {
			return nil, false
		}
	}
	if ctx.Err() != nil {
		return nil, false
	}
	digits = strings.TrimLeft(digits, "0")
	if digits == "" {
		return &decimalNumber{digits: "0", scale: new(big.Int)}, true
	}
	trailing := len(digits) - len(strings.TrimRight(digits, "0"))
	if trailing > 0 {
		digits = digits[:len(digits)-trailing]
		exponent.Add(exponent, new(big.Int).SetUint64(uint64(trailing)))
	}
	numberSign := 1
	if sign == "-" {
		numberSign = -1
	}
	return &decimalNumber{sign: numberSign, digits: digits, scale: new(big.Int).Set(exponent)}, true
}

// configError constructs the stable typed failure returned through the planning boundary.
func configError(category ErrorCategory, path string, cause error) error {
	return &ConfigError{Category: category, Path: path, Cause: cause}
}
