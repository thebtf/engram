package cognitive

import (
	"reflect"
	"testing"
)

// TestTypesExported asserts that the 10 public payload types named in spec
// FR-6 and ADR-010 are present and reachable via reflection. The boundary
// invariant (no CORE-internal DTOs leaking into pkg/cognitive) is owned by
// the T004 boundary test in internal/cognitive/core/.
func TestTypesExported(t *testing.T) {
	cases := []struct {
		name string
		ptr  any
	}{
		{"AttentionEvent", (*AttentionEvent)(nil)},
		{"HintProposal", (*HintProposal)(nil)},
		{"HintDelivery", (*HintDelivery)(nil)},
		{"SessionStateSlots", (*SessionStateSlots)(nil)},
		{"ProjectStateRecord", (*ProjectStateRecord)(nil)},
		{"AttentionEventRecord", (*AttentionEventRecord)(nil)},
		{"RawSignal", (*RawSignal)(nil)},
		{"Distilled", (*Distilled)(nil)},
	}
	for _, c := range cases {
		c := c
		t.Run(c.name, func(t *testing.T) {
			typ := reflect.TypeOf(c.ptr).Elem()
			if typ.Kind() != reflect.Struct {
				t.Fatalf("expected %s to be a struct, got %s", c.name, typ.Kind())
			}
		})
	}
}

// TestResolutionPolicyEnum asserts the ResolutionPolicy string enum and its
// two canonical constants per spec FR-7 + Clarify C2.
func TestResolutionPolicyEnum(t *testing.T) {
	if PolicyFanOut != ResolutionPolicy("fan_out") {
		t.Errorf("PolicyFanOut = %q, want %q", PolicyFanOut, "fan_out")
	}
	if PolicySinglePrimary != ResolutionPolicy("single_primary") {
		t.Errorf("PolicySinglePrimary = %q, want %q", PolicySinglePrimary, "single_primary")
	}
	var p ResolutionPolicy = "fan_out"
	if p != PolicyFanOut {
		t.Errorf("ResolutionPolicy(\"fan_out\") != PolicyFanOut")
	}
}

// TestHintSurfaceEnum asserts the HintSurface string enum and its two
// canonical constants per arch ADR-006 (OQ8 resolution) + spec FR-6.
func TestHintSurfaceEnum(t *testing.T) {
	if HintSurfaceUserPromptSubmit != HintSurface("user_prompt_submit") {
		t.Errorf("HintSurfaceUserPromptSubmit = %q, want %q",
			HintSurfaceUserPromptSubmit, "user_prompt_submit")
	}
	if HintSurfaceMCPPoll != HintSurface("mcp_poll") {
		t.Errorf("HintSurfaceMCPPoll = %q, want %q",
			HintSurfaceMCPPoll, "mcp_poll")
	}
	var s HintSurface = "mcp_poll"
	if s != HintSurfaceMCPPoll {
		t.Errorf("HintSurface(\"mcp_poll\") != HintSurfaceMCPPoll")
	}
}
