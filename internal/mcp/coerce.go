package mcp

import (
	"encoding/json"
	"fmt"
	"math"
	"strconv"
)

// TimelineParams holds parsed parameters for timeline-style MCP tool calls.
// Kept as a concrete struct so tests can marshal/unmarshal independently of
// the handler implementation.
type TimelineParams struct {
	AnchorID  int64  `json:"anchor_id"`
	DateStart int64  `json:"dateStart"`
	DateEnd   int64  `json:"dateEnd"`
	Query     string `json:"query"`
	Project   string `json:"project"`
	Concepts  string `json:"concepts"`
	Files     string `json:"files"`
	ObsType   string `json:"obs_type"`
	Format    string `json:"format"`
	Before    int    `json:"before"`
	After     int    `json:"after"`
}

// parseArgs unmarshals JSON args into map[string]any for safe type coercion.
// MCP clients may send numeric values as strings or floats; this intermediate
// representation lets each handler coerce fields individually.
func parseArgs(args json.RawMessage) (map[string]any, error) {
	if len(args) == 0 {
		return make(map[string]any), nil
	}
	var m map[string]any
	if err := json.Unmarshal(args, &m); err != nil {
		return nil, fmt.Errorf("invalid arguments: %w", err)
	}
	if m == nil {
		m = make(map[string]any)
	}
	return m, nil
}

// coerceString extracts a string from a JSON any value.
// Returns defaultVal if the key is missing, nil, or not a string.
func coerceString(v any, defaultVal string) string {
	if v == nil {
		return defaultVal
	}
	switch s := v.(type) {
	case string:
		return s
	case json.Number:
		return s.String()
	case float64:
		return strconv.FormatFloat(s, 'f', -1, 64)
	case bool:
		return strconv.FormatBool(s)
	default:
		return defaultVal
	}
}

// coerceInt extracts an int from a JSON any value.
// Handles float64 (default JSON number), json.Number, and string representations.
// Values are clamped to int range to prevent overflow.
func coerceInt(v any, defaultVal int) int {
	if v == nil {
		return defaultVal
	}
	switch n := v.(type) {
	case float64:
		return clampToInt(n)
	case json.Number:
		if i, err := n.Int64(); err == nil {
			return clampInt64ToInt(i)
		}
		if f, err := n.Float64(); err == nil {
			return clampToInt(f)
		}
		return defaultVal
	case string:
		if i, err := strconv.ParseInt(n, 10, 64); err == nil {
			return clampInt64ToInt(i)
		}
		if f, err := strconv.ParseFloat(n, 64); err == nil {
			return clampToInt(f)
		}
		return defaultVal
	default:
		return defaultVal
	}
}

// coerceInt64 extracts an int64 from a JSON any value.
// Handles float64 (default JSON number), json.Number, and string representations.
func coerceInt64(v any, defaultVal int64) int64 {
	if v == nil {
		return defaultVal
	}
	switch n := v.(type) {
	case float64:
		return int64(n)
	case json.Number:
		if i, err := n.Int64(); err == nil {
			return i
		}
		if f, err := n.Float64(); err == nil {
			return int64(f)
		}
		return defaultVal
	case string:
		if i, err := strconv.ParseInt(n, 10, 64); err == nil {
			return i
		}
		if f, err := strconv.ParseFloat(n, 64); err == nil {
			return int64(f)
		}
		return defaultVal
	default:
		return defaultVal
	}
}

// coerceFloat64 extracts a float64 from a JSON any value.
// Handles float64 (default JSON number), json.Number, and string representations.
func coerceFloat64(v any, defaultVal float64) float64 {
	if v == nil {
		return defaultVal
	}
	switch n := v.(type) {
	case float64:
		return n
	case json.Number:
		if f, err := n.Float64(); err == nil {
			return f
		}
		return defaultVal
	case string:
		if f, err := strconv.ParseFloat(n, 64); err == nil {
			return f
		}
		return defaultVal
	default:
		return defaultVal
	}
}

// coerceBool extracts a bool from a JSON any value.
// Handles bool, string ("true"/"false"), and numeric (0/1) representations.
func coerceBool(v any, defaultVal bool) bool {
	if v == nil {
		return defaultVal
	}
	switch b := v.(type) {
	case bool:
		return b
	case string:
		if parsed, err := strconv.ParseBool(b); err == nil {
			return parsed
		}
		return defaultVal
	case float64:
		return b != 0
	case json.Number:
		if f, err := b.Float64(); err == nil {
			return f != 0
		}
		return defaultVal
	default:
		return defaultVal
	}
}

// coerceStringSlice extracts a []string from a JSON any value.
// Handles both a single string and an array of strings.
func coerceStringSlice(v any) []string {
	if v == nil {
		return nil
	}
	switch s := v.(type) {
	case []any:
		result := make([]string, 0, len(s))
		for _, item := range s {
			if str, ok := item.(string); ok {
				result = append(result, str)
			}
		}
		return result
	case []string:
		return s
	case string:
		if s != "" {
			return []string{s}
		}
		return nil
	default:
		return nil
	}
}

// coerceInt64Slice extracts a []int64 from a JSON any value.
// Handles arrays of numbers, strings, and mixed types.
func coerceInt64Slice(v any) []int64 {
	if v == nil {
		return nil
	}
	arr, ok := v.([]any)
	if !ok {
		return nil
	}
	result := make([]int64, 0, len(arr))
	for _, item := range arr {
		if id := coerceInt64(item, 0); id != 0 {
			result = append(result, id)
		}
	}
	return result
}

// clampToInt safely converts a float64 to int, clamping to int range.
func clampToInt(f float64) int {
	if math.IsNaN(f) || math.IsInf(f, 0) {
		return 0
	}
	if f > float64(math.MaxInt) {
		return math.MaxInt
	}
	if f < float64(math.MinInt) {
		return math.MinInt
	}
	return int(f)
}

// clampInt64ToInt safely converts int64 to int.
func clampInt64ToInt(i int64) int {
	if i > int64(math.MaxInt) {
		return math.MaxInt
	}
	if i < int64(math.MinInt) {
		return math.MinInt
	}
	return int(i)
}
