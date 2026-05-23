package cognitive

import (
	"encoding/json"
	"sort"
)

// VolatileFields enumerates the JSON object keys whose values legitimately
// change between runs (timestamps, server version, session identifiers,
// log timestamps) and that MUST be stripped before NormalizeForDiff returns.
// The list is the canonical allowlist anchor from ADR-007: adding a new
// run-to-run varying field requires opting into this list explicitly, or
// the FR-9 byte-identity gate will fail and surface the regression.
//
// The slice is a package-level value rather than a constant because Go does
// not support constant slices, but it is treated as read-only by callers.
var VolatileFields = []string{
	"generated_at",
	"server_version",
	"session_id",
	"log_ts",
}

// MemorySortKey is the field name inside memory-element maps used as the
// canonical sort key (per Clarify C3). Any JSON array whose elements are
// objects containing this key is sorted ascending by the key's string
// value before re-marshalling — this gives the FR-9 byte-identity gate a
// deterministic order for memory lists regardless of how the underlying
// retrieval path returns them.
const MemorySortKey = "memory_id"

// NormalizeForDiff returns a canonical-form serialization of payload suitable
// for byte-for-byte comparison in the FR-9 CI gate. The transformation:
//
//  1. Decodes payload into untyped JSON (`any`).
//  2. Recursively strips every key listed in VolatileFields from every
//     nested object.
//  3. Recursively sorts every nested array whose elements are objects
//     containing MemorySortKey, ascending by that key's string value.
//  4. Re-encodes the tree with `encoding/json`, which alphabetizes
//     `map[string]any` keys by default.
//
// Edge cases:
//   - len(payload) == 0   → returns (payload, nil) — the byte-identity gate
//     cannot fail on a missing fixture.
//   - invalid JSON        → returns (payload, err) — the original bytes are
//     returned alongside the parse error so callers can decide whether to
//     surface the failure or treat it as a no-op.
//
// On success the returned slice is a fresh allocation owned by the caller.
func NormalizeForDiff(payload []byte) ([]byte, error) {
	if len(payload) == 0 {
		return payload, nil
	}

	var parsed any
	if err := json.Unmarshal(payload, &parsed); err != nil {
		return payload, err
	}

	volatileSet := make(map[string]struct{}, len(VolatileFields))
	for _, f := range VolatileFields {
		volatileSet[f] = struct{}{}
	}

	stripped := stripKeysRecursive(parsed, volatileSet)
	sorted := sortArraysByKey(stripped, MemorySortKey)

	return canonicalEncode(sorted)
}

// stripKeysRecursive walks node depth-first, removing every entry in maps
// whose key is in keys. Slices are traversed element-wise so volatile keys
// inside nested map elements are stripped too. Scalars are returned
// unchanged. The walk mutates maps in place to avoid allocating a parallel
// tree; the returned value is the same root as the input.
func stripKeysRecursive(node any, keys map[string]struct{}) any {
	switch v := node.(type) {
	case map[string]any:
		for k := range v {
			if _, hit := keys[k]; hit {
				delete(v, k)
				continue
			}
			v[k] = stripKeysRecursive(v[k], keys)
		}
		return v
	case []any:
		for i, elem := range v {
			v[i] = stripKeysRecursive(elem, keys)
		}
		return v
	default:
		return node
	}
}

// sortArraysByKey walks node depth-first and, for every slice whose elements
// are maps containing field with a string value, sorts the slice ascending
// by that string value. Mixed-type slices (some elements lack the key, or
// the value is not a string) are left untouched — preserving original order
// is safer than coercing types or panicking. Nested maps and slices are
// recursed first so the deepest matching arrays are sorted before any
// outer slice containing them.
func sortArraysByKey(node any, field string) any {
	switch v := node.(type) {
	case map[string]any:
		for k, vv := range v {
			v[k] = sortArraysByKey(vv, field)
		}
		return v
	case []any:
		// Recurse into each element first.
		for i, elem := range v {
			v[i] = sortArraysByKey(elem, field)
		}
		// Decide whether this slice itself is sortable.
		if !sliceElementsHaveStringField(v, field) {
			return v
		}
		sort.SliceStable(v, func(i, j int) bool {
			return stringField(v[i], field) < stringField(v[j], field)
		})
		return v
	default:
		return node
	}
}

// sliceElementsHaveStringField reports whether every element of s is a
// non-nil map containing field with a string value. Returning false for any
// mismatch leaves the original order untouched.
func sliceElementsHaveStringField(s []any, field string) bool {
	if len(s) == 0 {
		return false
	}
	for _, elem := range s {
		m, ok := elem.(map[string]any)
		if !ok {
			return false
		}
		val, present := m[field]
		if !present {
			return false
		}
		if _, ok := val.(string); !ok {
			return false
		}
	}
	return true
}

// stringField returns m[field] as a string when m is a map and the value is
// a string; otherwise the empty string. Callers must have validated the
// element shape via sliceElementsHaveStringField first.
func stringField(elem any, field string) string {
	m, ok := elem.(map[string]any)
	if !ok {
		return ""
	}
	s, _ := m[field].(string)
	return s
}

// canonicalEncode marshals node back to JSON. Go's encoding/json sorts
// `map[string]any` keys alphabetically by default, so canonical key order
// falls out of the standard library guarantee.
func canonicalEncode(node any) ([]byte, error) {
	return json.Marshal(node)
}
