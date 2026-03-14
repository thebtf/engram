package mcp

import (
	"encoding/json"
	"math"
	"testing"
)

func TestParseArgs(t *testing.T) {
	tests := []struct {
		name    string
		args    json.RawMessage
		wantErr bool
		wantNil bool
	}{
		{"nil args", nil, false, false},
		{"empty bytes", json.RawMessage{}, false, false},
		{"empty object", json.RawMessage(`{}`), false, false},
		{"valid object", json.RawMessage(`{"key":"value"}`), false, false},
		{"invalid json", json.RawMessage(`{invalid}`), true, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m, err := parseArgs(tt.args)
			if (err != nil) != tt.wantErr {
				t.Errorf("parseArgs() error = %v, wantErr %v", err, tt.wantErr)
			}
			if !tt.wantErr && m == nil {
				t.Error("parseArgs() returned nil map for valid input")
			}
		})
	}
}

func TestCoerceString(t *testing.T) {
	tests := []struct {
		name string
		v    any
		def  string
		want string
	}{
		{"nil", nil, "default", "default"},
		{"string", "hello", "", "hello"},
		{"float64", 3.14, "", "3.14"},
		{"bool", true, "", "true"},
		{"json.Number", json.Number("42"), "", "42"},
		{"wrong type", []int{1}, "fallback", "fallback"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := coerceString(tt.v, tt.def)
			if got != tt.want {
				t.Errorf("coerceString() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestCoerceInt(t *testing.T) {
	tests := []struct {
		name string
		v    any
		def  int
		want int
	}{
		{"nil", nil, 20, 20},
		{"float64", float64(5), 0, 5},
		{"float64 with decimal", 5.7, 0, 5},
		{"string int", "10", 0, 10},
		{"string float", "5.9", 0, 5},
		{"json.Number int", json.Number("42"), 0, 42},
		{"json.Number float", json.Number("3.14"), 0, 3},
		{"string non-numeric", "abc", 99, 99},
		{"bool", true, 0, 0},
		{"negative float", float64(-3), 0, -3},
		{"zero", float64(0), 5, 0},
		{"overflow float64", float64(math.MaxFloat64), 0, math.MaxInt},
		{"negative overflow", float64(-math.MaxFloat64), 0, math.MinInt},
		{"NaN", math.NaN(), 42, 0},
		{"Inf", math.Inf(1), 42, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := coerceInt(tt.v, tt.def)
			if got != tt.want {
				t.Errorf("coerceInt() = %d, want %d", got, tt.want)
			}
		})
	}
}

func TestCoerceInt64(t *testing.T) {
	tests := []struct {
		name string
		v    any
		def  int64
		want int64
	}{
		{"nil", nil, 0, 0},
		{"float64", float64(12345), 0, 12345},
		{"string", "67890", 0, 67890},
		{"json.Number", json.Number("99999"), 0, 99999},
		{"json.Number float", json.Number("3.14"), 0, 3},
		{"string float", "123.45", 0, 123},
		{"invalid string", "abc", -1, -1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := coerceInt64(tt.v, tt.def)
			if got != tt.want {
				t.Errorf("coerceInt64() = %d, want %d", got, tt.want)
			}
		})
	}
}

func TestCoerceFloat64(t *testing.T) {
	tests := []struct {
		name string
		v    any
		def  float64
		want float64
	}{
		{"nil", nil, 1.5, 1.5},
		{"float64", 3.14, 0, 3.14},
		{"string", "2.718", 0, 2.718},
		{"json.Number", json.Number("1.618"), 0, 1.618},
		{"invalid string", "abc", 0.5, 0.5},
		{"integer string", "42", 0, 42.0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := coerceFloat64(tt.v, tt.def)
			if got != tt.want {
				t.Errorf("coerceFloat64() = %f, want %f", got, tt.want)
			}
		})
	}
}

func TestCoerceBool(t *testing.T) {
	tests := []struct {
		name string
		v    any
		def  bool
		want bool
	}{
		{"nil", nil, false, false},
		{"true", true, false, true},
		{"false", false, true, false},
		{"string true", "true", false, true},
		{"string false", "false", true, false},
		{"float 1", float64(1), false, true},
		{"float 0", float64(0), true, false},
		{"invalid string", "abc", true, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := coerceBool(tt.v, tt.def)
			if got != tt.want {
				t.Errorf("coerceBool() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestCoerceStringSlice(t *testing.T) {
	tests := []struct {
		name string
		v    any
		want []string
	}{
		{"nil", nil, nil},
		{"single string", "hello", []string{"hello"}},
		{"empty string", "", nil},
		{"array of strings", []any{"a", "b", "c"}, []string{"a", "b", "c"}},
		{"mixed array", []any{"a", 123, "b"}, []string{"a", "b"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := coerceStringSlice(tt.v)
			if tt.want == nil {
				if got != nil {
					t.Errorf("coerceStringSlice() = %v, want nil", got)
				}
				return
			}
			if len(got) != len(tt.want) {
				t.Errorf("coerceStringSlice() len = %d, want %d", len(got), len(tt.want))
				return
			}
			for i := range got {
				if got[i] != tt.want[i] {
					t.Errorf("coerceStringSlice()[%d] = %q, want %q", i, got[i], tt.want[i])
				}
			}
		})
	}
}

func TestCoerceInt64Slice(t *testing.T) {
	tests := []struct {
		name string
		v    any
		want []int64
	}{
		{"nil", nil, nil},
		{"not array", "hello", nil},
		{"float64 array", []any{float64(1), float64(2), float64(3)}, []int64{1, 2, 3}},
		{"string array", []any{"1", "2", "3"}, []int64{1, 2, 3}},
		{"mixed array", []any{float64(1), "2", json.Number("3")}, []int64{1, 2, 3}},
		{"with zeros", []any{float64(0), float64(1)}, []int64{1}}, // 0 is skipped
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := coerceInt64Slice(tt.v)
			if tt.want == nil {
				if got != nil {
					t.Errorf("coerceInt64Slice() = %v, want nil", got)
				}
				return
			}
			if len(got) != len(tt.want) {
				t.Errorf("coerceInt64Slice() len = %d, want %d", len(got), len(tt.want))
				return
			}
			for i := range got {
				if got[i] != tt.want[i] {
					t.Errorf("coerceInt64Slice()[%d] = %d, want %d", i, got[i], tt.want[i])
				}
			}
		})
	}
}
