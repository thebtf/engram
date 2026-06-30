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
		{"ResumePacket", (*ResumePacket)(nil)},
		{"ExperienceQueryRequest", (*ExperienceQueryRequest)(nil)},
		{"ExperienceResponse", (*ExperienceResponse)(nil)},
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

func TestStatePacketSourceEnum(t *testing.T) {
	if StatePacketSourceNative != StatePacketSource("native") {
		t.Errorf("StatePacketSourceNative = %q, want %q", StatePacketSourceNative, "native")
	}
	if StatePacketSourceFilesystemFallback != StatePacketSource("filesystem_fallback") {
		t.Errorf("StatePacketSourceFilesystemFallback = %q, want %q",
			StatePacketSourceFilesystemFallback, "filesystem_fallback")
	}
	if StatePacketSourceConflict != StatePacketSource("conflict") {
		t.Errorf("StatePacketSourceConflict = %q, want %q", StatePacketSourceConflict, "conflict")
	}
}

func TestExperienceApplicabilityStateEnum(t *testing.T) {
	if ExperienceApplicabilityApplies != ExperienceApplicabilityState("applies") {
		t.Errorf("ExperienceApplicabilityApplies = %q, want applies", ExperienceApplicabilityApplies)
	}
	if ExperienceApplicabilityUncertain != ExperienceApplicabilityState("uncertain") {
		t.Errorf("ExperienceApplicabilityUncertain = %q, want uncertain", ExperienceApplicabilityUncertain)
	}
	if ExperienceApplicabilityBlocked != ExperienceApplicabilityState("blocked") {
		t.Errorf("ExperienceApplicabilityBlocked = %q, want blocked", ExperienceApplicabilityBlocked)
	}
}

func TestExperienceResponseContract_RequiredFieldsBinaryDefined(t *testing.T) {
	typ := reflect.TypeOf(ExperienceResponse{})
	required := map[string]struct {
		jsonTag string
		kind    reflect.Kind
	}{
		"Source":                {"source", reflect.String},
		"Lesson":                {"lesson", reflect.String},
		"Applicability":         {"applicability", reflect.Struct},
		"AntiApplicability":     {"anti_applicability", reflect.Slice},
		"SourceAttribution":     {"source_attribution", reflect.Slice},
		"ArchiveTriggerClasses": {"archive_trigger_classes", reflect.Slice},
	}

	for name, want := range required {
		field, ok := typ.FieldByName(name)
		if !ok {
			t.Fatalf("ExperienceResponse missing required field %s", name)
		}
		if got := field.Tag.Get("json"); got != want.jsonTag {
			t.Fatalf("ExperienceResponse.%s json tag = %q, want %q", name, got, want.jsonTag)
		}
		if got := field.Type.Kind(); got != want.kind {
			t.Fatalf("ExperienceResponse.%s kind = %s, want %s", name, got, want.kind)
		}
	}
}

func TestForgettingOperationTaxonomy_ExplicitFiveClasses(t *testing.T) {
	cases := map[ForgettingOperation]string{
		ForgettingOperationSuppress:    "suppress",
		ForgettingOperationExpire:      "expire",
		ForgettingOperationArchive:     "archive",
		ForgettingOperationConsolidate: "consolidate",
		ForgettingOperationDestroy:     "destroy",
	}

	if len(cases) != 5 {
		t.Fatalf("taxonomy collapsed: got %d operation classes, want 5", len(cases))
	}
	for operation, want := range cases {
		if string(operation) != want {
			t.Fatalf("operation %q string value: got %q, want %q", operation, operation, want)
		}
	}
}

func TestForgettingDecisionContract_RequiredAuditAndSafetyFields(t *testing.T) {
	typ := reflect.TypeOf(ForgettingDecision{})
	required := map[string]struct {
		jsonTag string
		kind    reflect.Kind
	}{
		"Operation":                {"operation", reflect.String},
		"State":                    {"state", reflect.String},
		"Rationale":                {"rationale", reflect.String},
		"PolicyBoundary":           {"policy_boundary", reflect.String},
		"Audit":                    {"audit", reflect.Struct},
		"Review":                   {"review", reflect.Struct},
		"StructuralLoss":           {"structural_loss", reflect.Struct},
		"DataDestructionByDefault": {"data_destruction_by_default", reflect.Bool},
	}

	for name, want := range required {
		field, ok := typ.FieldByName(name)
		if !ok {
			t.Fatalf("ForgettingDecision missing required field %s", name)
		}
		if got := field.Tag.Get("json"); got != want.jsonTag {
			t.Fatalf("ForgettingDecision.%s json tag = %q, want %q", name, got, want.jsonTag)
		}
		if got := field.Type.Kind(); got != want.kind {
			t.Fatalf("ForgettingDecision.%s kind = %s, want %s", name, got, want.kind)
		}
	}
}

func TestForgettingAuditSurface_NamesSnapshotAuditAndExport(t *testing.T) {
	typ := reflect.TypeOf(ForgettingAuditSurface{})
	required := map[string]string{
		"Required":      "required",
		"SnapshotStore": "snapshot_store",
		"AuditStore":    "audit_store",
		"ExportPath":    "export_path",
		"Evidence":      "evidence",
	}

	for name, wantTag := range required {
		field, ok := typ.FieldByName(name)
		if !ok {
			t.Fatalf("ForgettingAuditSurface missing required field %s", name)
		}
		if got := field.Tag.Get("json"); got != wantTag {
			t.Fatalf("ForgettingAuditSurface.%s json tag = %q, want %q", name, got, wantTag)
		}
	}
}

func TestTemporalTruthQueryStateEnum(t *testing.T) {
	cases := map[TemporalTruthQueryState]string{
		TemporalTruthFound:       "found",
		TemporalTruthNotSelected: "not_selected",
		TemporalTruthUnknown:     "unknown",
	}

	if len(cases) != 3 {
		t.Fatalf("temporal truth states: got %d, want 3", len(cases))
	}
	for state, want := range cases {
		if string(state) != want {
			t.Fatalf("TemporalTruthQueryState %q = %q, want %q", state, state, want)
		}
	}
}

func TestTemporalTruthResponseContract_RequiredBoundedFields(t *testing.T) {
	typ := reflect.TypeOf(TemporalTruthResponse{})
	required := map[string]struct {
		jsonTag string
		kind    reflect.Kind
	}{
		"Scope":           {"scope", reflect.Struct},
		"State":           {"state", reflect.String},
		"TrueNow":         {"true_now,omitempty", reflect.Ptr},
		"TrueThen":        {"true_then", reflect.Ptr},
		"History":         {"history", reflect.Slice},
		"ProvenanceChain": {"provenance_chain", reflect.Slice},
	}

	for name, want := range required {
		field, ok := typ.FieldByName(name)
		if !ok {
			t.Fatalf("TemporalTruthResponse missing required field %s", name)
		}
		if got := field.Tag.Get("json"); got != want.jsonTag {
			t.Fatalf("TemporalTruthResponse.%s json tag = %q, want %q", name, got, want.jsonTag)
		}
		if got := field.Type.Kind(); got != want.kind {
			t.Fatalf("TemporalTruthResponse.%s kind = %s, want %s", name, got, want.kind)
		}
	}
}

func TestTemporalTruthEntryContract_NamesValidityInvalidationAndProvenance(t *testing.T) {
	typ := reflect.TypeOf(TemporalTruthEntry{})
	required := map[string]struct {
		jsonTag string
		kind    reflect.Kind
	}{
		"Value":                 {"value", reflect.String},
		"ValidFrom":             {"valid_from", reflect.Struct},
		"ValidUntil":            {"valid_until", reflect.Ptr},
		"InvalidatedAt":         {"invalidated_at", reflect.Ptr},
		"InvalidationRationale": {"invalidation_rationale", reflect.String},
		"Provenance":            {"provenance", reflect.Slice},
	}

	for name, want := range required {
		field, ok := typ.FieldByName(name)
		if !ok {
			t.Fatalf("TemporalTruthEntry missing required field %s", name)
		}
		if got := field.Tag.Get("json"); got != want.jsonTag {
			t.Fatalf("TemporalTruthEntry.%s json tag = %q, want %q", name, got, want.jsonTag)
		}
		if got := field.Type.Kind(); got != want.kind {
			t.Fatalf("TemporalTruthEntry.%s kind = %s, want %s", name, got, want.kind)
		}
	}
}
