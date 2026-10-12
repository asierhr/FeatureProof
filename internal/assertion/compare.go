package assertion

import (
	"math/big"
	"reflect"
)

// Cmp compares two compact decimals without expanding either base-10 exponent.
func (n *decimalNumber) Cmp(other *decimalNumber) int {
	if n.sign != other.sign {
		if n.sign < other.sign {
			return -1
		}
		return 1
	}
	if n.sign == 0 {
		return 0
	}
	leftMagnitude := new(big.Int).Add(n.scale, new(big.Int).SetUint64(uint64(len(n.digits))))
	rightMagnitude := new(big.Int).Add(other.scale, new(big.Int).SetUint64(uint64(len(other.digits))))
	comparison := leftMagnitude.Cmp(rightMagnitude)
	if comparison == 0 {
		length := len(n.digits)
		if len(other.digits) > length {
			length = len(other.digits)
		}
		for i := 0; i < length; i++ {
			left, right := byte('0'), byte('0')
			if i < len(n.digits) {
				left = n.digits[i]
			}
			if i < len(other.digits) {
				right = other.digits[i]
			}
			if left < right {
				comparison = -1
				break
			}
			if left > right {
				comparison = 1
				break
			}
		}
	}
	if n.sign < 0 {
		return -comparison
	}
	return comparison
}

// equalJSON applies strict JSON type equality while treating all finite numeric representations by value.
func equalJSON(left, right any) bool {
	leftNumber, leftIsNumber := exactNumber(left)
	rightNumber, rightIsNumber := exactNumber(right)
	if leftIsNumber || rightIsNumber {
		return leftIsNumber && rightIsNumber && leftNumber.Cmp(rightNumber) == 0
	}
	leftObject, leftIsObject := left.(map[string]any)
	rightObject, rightIsObject := right.(map[string]any)
	if leftIsObject || rightIsObject {
		if !leftIsObject || !rightIsObject || len(leftObject) != len(rightObject) {
			return false
		}
		for key, value := range leftObject {
			other, exists := rightObject[key]
			if !exists || !equalJSON(value, other) {
				return false
			}
		}
		return true
	}
	leftArray, leftIsArray := left.([]any)
	rightArray, rightIsArray := right.([]any)
	if leftIsArray || rightIsArray {
		if !leftIsArray || !rightIsArray || len(leftArray) != len(rightArray) {
			return false
		}
		for i := range leftArray {
			if !equalJSON(leftArray[i], rightArray[i]) {
				return false
			}
		}
		return true
	}
	return reflect.TypeOf(left) == reflect.TypeOf(right) && reflect.DeepEqual(left, right)
}

// compareNumeric applies one exact relation and reports false for missing or incompatible observations.
func compareNumeric(operator string, expected any, observed planningValue) bool {
	if !observed.present {
		return false
	}
	want, wantOK := exactNumber(expected)
	got, gotOK := exactNumber(observed.value)
	if !wantOK || !gotOK {
		return false
	}
	comparison := got.Cmp(want)
	switch operator {
	case "gt":
		return comparison > 0
	case "gte":
		return comparison >= 0
	case "lt":
		return comparison < 0
	case "lte":
		return comparison <= 0
	default:
		return false
	}
}

// planningValue is the small local view used by numeric comparison to avoid altering shared result values.
type planningValue struct {
	present bool
	value   any
}
