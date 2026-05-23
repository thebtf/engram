package cognitive

import (
	"bytes"
	"encoding/json"
	"testing"
)

// TestNormalize_StripsVolatileFields_FromNested asserts that every key in
// VolatileFields is removed at every depth in the input. Fixture places one
// volatile key per depth level so the recursive walk must touch all levels
// to pass.
func TestNormalize_StripsVolatileFields_FromNested(t *testing.T) {
	in := []byte(`{
		"generated_at": "2026-05-23T00:00:00Z",
		"alpha": {
			"server_version": "v7.0.0",
			"beta": {
				"session_id": "sess-1",
				"gamma": {
					"log_ts": 12345,
					"keep": "stay"
				}
			}
		}
	}`)

	out, err := NormalizeForDiff(in)
	if err != nil {
		t.Fatalf("NormalizeForDiff returned error: %v", err)
	}

	for _, vf := range VolatileFields {
		if bytes.Contains(out, []byte(`"`+vf+`"`)) {
			t.Errorf("volatile field %q not stripped from output: %s", vf, out)
		}
	}

	// Positive control — the non-volatile "keep" leaf must still be present.
	if !bytes.Contains(out, []byte(`"keep":"stay"`)) {
		t.Errorf("non-volatile leaf missing from output: %s", out)
	}
}

// TestNormalize_SortsMemoriesById asserts that an array of maps containing
// MemorySortKey is sorted ascending by that key's string value.
func TestNormalize_SortsMemoriesById(t *testing.T) {
	in := []byte(`{"memories":[{"memory_id":"c"},{"memory_id":"a"},{"memory_id":"b"}]}`)

	out, err := NormalizeForDiff(in)
	if err != nil {
		t.Fatalf("NormalizeForDiff returned error: %v", err)
	}

	// The canonical-key-order rule will alphabetize the inner maps, but each
	// inner map only has memory_id so byte order of inner elements is fixed.
	want := []byte(`{"memories":[{"memory_id":"a"},{"memory_id":"b"},{"memory_id":"c"}]}`)
	if !bytes.Equal(out, want) {
		t.Errorf("sort order mismatch:\n  got:  %s\n  want: %s", out, want)
	}
}

// TestNormalize_CanonicalKeyOrder asserts that re-marshaling produces
// alphabetical key order regardless of input key order.
func TestNormalize_CanonicalKeyOrder(t *testing.T) {
	in := []byte(`{"z":1,"a":2,"m":3}`)

	out, err := NormalizeForDiff(in)
	if err != nil {
		t.Fatalf("NormalizeForDiff returned error: %v", err)
	}

	want := []byte(`{"a":2,"m":3,"z":1}`)
	if !bytes.Equal(out, want) {
		t.Errorf("canonical order mismatch:\n  got:  %s\n  want: %s", out, want)
	}
}

// TestNormalize_InvalidJSON_ReturnsError asserts the AC-mandated failure
// path: returned bytes equal the original input, returned error non-nil.
func TestNormalize_InvalidJSON_ReturnsError(t *testing.T) {
	in := []byte(`{not json`)

	out, err := NormalizeForDiff(in)
	if err == nil {
		t.Fatal("expected error for invalid JSON, got nil")
	}
	if !bytes.Equal(out, in) {
		t.Errorf("invalid-JSON returned bytes must equal input:\n  got:  %s\n  want: %s", out, in)
	}
}

// TestNormalize_EmptyPayload_ReturnsEmpty resolves the empty-input edge case:
// zero-length payload returns (empty, nil) — the byte-identity gate cannot
// fail on a missing fixture.
func TestNormalize_EmptyPayload_ReturnsEmpty(t *testing.T) {
	in := []byte("")

	out, err := NormalizeForDiff(in)
	if err != nil {
		t.Fatalf("empty payload returned error: %v", err)
	}
	if len(out) != 0 {
		t.Errorf("empty payload should return empty bytes, got: %q", out)
	}
}

// TestNormalize_DeepNesting_VolatileStripped asserts the walk reaches a
// depth-5 leaf and strips volatile keys there.
func TestNormalize_DeepNesting_VolatileStripped(t *testing.T) {
	in := []byte(`{"a":{"b":{"c":{"d":{"e":{"generated_at":"x","real":"y"}}}}}}`)

	out, err := NormalizeForDiff(in)
	if err != nil {
		t.Fatalf("NormalizeForDiff returned error: %v", err)
	}

	if bytes.Contains(out, []byte(`"generated_at"`)) {
		t.Errorf("generated_at at depth-5 not stripped: %s", out)
	}

	// Parse back and verify the leaf map contains only {"real":"y"}.
	var parsed map[string]any
	if err := json.Unmarshal(out, &parsed); err != nil {
		t.Fatalf("output not parseable: %v\n%s", err, out)
	}

	leaf, ok := descendMap(parsed, "a", "b", "c", "d", "e")
	if !ok {
		t.Fatalf("could not descend to leaf in output: %s", out)
	}
	if len(leaf) != 1 || leaf["real"] != "y" {
		t.Errorf("leaf map mismatch: %v", leaf)
	}
}

// descendMap is a test helper that walks nested maps by key.
func descendMap(root map[string]any, keys ...string) (map[string]any, bool) {
	cur := root
	for _, k := range keys {
		next, ok := cur[k].(map[string]any)
		if !ok {
			return nil, false
		}
		cur = next
	}
	return cur, true
}

// TestVolatileFields_Immutable_CanonicalSet pins the FR-9 byte-identity gate
// to the four canonical ADR-007 keys. VolatileFields is an exported var (Go
// has no constant slices) — this test detects accidental mutation by any
// caller that would silently break the gate's normalization invariant.
func TestVolatileFields_Immutable_CanonicalSet(t *testing.T) {
	want := []string{"generated_at", "server_version", "session_id", "log_ts"}

	if len(VolatileFields) != len(want) {
		t.Fatalf("VolatileFields length: got %d, want %d", len(VolatileFields), len(want))
	}
	for i, w := range want {
		if VolatileFields[i] != w {
			t.Fatalf("VolatileFields[%d]: got %q, want %q", i, VolatileFields[i], w)
		}
	}
}

// TestNormalize_MixedTypeSlice_LeftUntouched covers the documented "safer
// than coercing" behavior of sortArraysByKey: if any slice element lacks
// MemorySortKey or carries a non-string value, the original order survives.
// The test pins both the no-key and non-string-value paths.
func TestNormalize_MixedTypeSlice_LeftUntouched(t *testing.T) {
	t.Run("element missing memory_id", func(t *testing.T) {
		in := []byte(`{"memories":[{"memory_id":"c"},{"other":"x"},{"memory_id":"a"}]}`)
		out, err := NormalizeForDiff(in)
		if err != nil {
			t.Fatalf("NormalizeForDiff: %v", err)
		}
		// Original order preserved (no sort applied) — note canonical key order
		// inside each map will still alphabetize, but element order is unchanged.
		want := []byte(`{"memories":[{"memory_id":"c"},{"other":"x"},{"memory_id":"a"}]}`)
		if !bytes.Equal(out, want) {
			t.Errorf("mixed-slice (missing key) order changed:\n  got:  %s\n  want: %s", out, want)
		}
	})

	t.Run("memory_id with non-string value", func(t *testing.T) {
		in := []byte(`{"memories":[{"memory_id":"b"},{"memory_id":42},{"memory_id":"a"}]}`)
		out, err := NormalizeForDiff(in)
		if err != nil {
			t.Fatalf("NormalizeForDiff: %v", err)
		}
		want := []byte(`{"memories":[{"memory_id":"b"},{"memory_id":42},{"memory_id":"a"}]}`)
		if !bytes.Equal(out, want) {
			t.Errorf("mixed-slice (non-string id) order changed:\n  got:  %s\n  want: %s", out, want)
		}
	})
}
