package contract

import (
	"encoding/json"
	"math/big"
)

func equalJSONValue(left, right any) bool {
	switch left := left.(type) {
	case map[string]any:
		right, ok := right.(map[string]any)
		if !ok || len(left) != len(right) {
			return false
		}
		for key, value := range left {
			other, exists := right[key]
			if !exists || !equalJSONValue(value, other) {
				return false
			}
		}
		return true
	case []any:
		right, ok := right.([]any)
		if !ok || len(left) != len(right) {
			return false
		}
		for index, value := range left {
			if !equalJSONValue(value, right[index]) {
				return false
			}
		}
		return true
	case json.Number:
		right, ok := right.(json.Number)
		if !ok {
			return false
		}
		if left == right {
			return true
		}
		a, ok := new(big.Rat).SetString(string(left))
		if !ok {
			return false
		}
		b, ok := new(big.Rat).SetString(string(right))
		return ok && a.Cmp(b) == 0
	case string:
		right, ok := right.(string)
		return ok && left == right
	case bool:
		right, ok := right.(bool)
		return ok && left == right
	case nil:
		return right == nil
	default:
		return false
	}
}

func normalizeZero(value any) any {
	switch typed := value.(type) {
	case float64:
		if typed == 0 {
			return float64(0)
		}
	case []any:
		for index, child := range typed {
			typed[index] = normalizeZero(child)
		}
	case map[string]any:
		for key, child := range typed {
			typed[key] = normalizeZero(child)
		}
	}
	return value
}
