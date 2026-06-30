package cognitive

import (
	"context"
	"reflect"
	"sort"
	"testing"
	"time"
)

func requireContractField(t *testing.T, typ reflect.Type, name, jsonTag string, wantType reflect.Type) {
	t.Helper()
	field, ok := typ.FieldByName(name)
	if !ok {
		t.Fatalf("%s missing required field %s", typ.Name(), name)
	}
	if got := field.Tag.Get("json"); got != jsonTag {
		t.Fatalf("%s.%s json tag = %q, want %q", typ.Name(), name, got, jsonTag)
	}
	if got := field.Type; got != wantType {
		t.Fatalf("%s.%s type = %s, want %s", typ.Name(), name, got, wantType)
	}
}

func TestResumePacketContract_RequiredFieldsBinaryDefined(t *testing.T) {
	typ := reflect.TypeOf(ResumePacket{})

	required := map[string]struct {
		jsonTag string
		typ     reflect.Type
	}{
		"PacketID":         {jsonTag: "packet_id", typ: reflect.TypeOf("")},
		"Project":          {jsonTag: "project", typ: reflect.TypeOf("")},
		"Principal":        {jsonTag: "principal", typ: reflect.TypeOf("")},
		"SessionID":        {jsonTag: "session_id", typ: reflect.TypeOf("")},
		"StateVersion":     {jsonTag: "state_version", typ: reflect.TypeOf("")},
		"Source":           {jsonTag: "source", typ: reflect.TypeOf(StatePacketSource(""))},
		"FallbackUsed":     {jsonTag: "fallback_used", typ: reflect.TypeOf(false)},
		"Freshness":        {jsonTag: "freshness", typ: reflect.TypeOf(StateFreshness(""))},
		"Drift":            {jsonTag: "drift", typ: reflect.TypeOf(StateDrift{})},
		"NextAction":       {jsonTag: "next_action", typ: reflect.TypeOf(StateAction{})},
		"NextVerification": {jsonTag: "next_verification", typ: reflect.TypeOf(StateVerification{})},
		"GeneratedAt":      {jsonTag: "generated_at", typ: reflect.TypeOf(time.Time{})},
		"EvidenceRefs":     {jsonTag: "evidence_refs", typ: reflect.TypeOf([]string{})},
		"Scopes":           {jsonTag: "scopes", typ: reflect.TypeOf([]StateScopeKind{})},
	}

	for name, want := range required {
		requireContractField(t, typ, name, want.jsonTag, want.typ)
	}
}

func TestResumePacketRequestContract_ExplicitScopeAndFallbackFields(t *testing.T) {
	typ := reflect.TypeOf(ResumePacketRequest{})

	required := map[string]struct {
		jsonTag string
		typ     reflect.Type
	}{
		"Project":                 {jsonTag: "project", typ: reflect.TypeOf("")},
		"Principal":               {jsonTag: "principal,omitempty", typ: reflect.TypeOf("")},
		"SessionID":               {jsonTag: "session_id,omitempty", typ: reflect.TypeOf("")},
		"GoalID":                  {jsonTag: "goal_id,omitempty", typ: reflect.TypeOf("")},
		"TaskID":                  {jsonTag: "task_id,omitempty", typ: reflect.TypeOf("")},
		"Scopes":                  {jsonTag: "scopes", typ: reflect.TypeOf([]StateScopeKind{})},
		"AllowFilesystemFallback": {jsonTag: "allow_filesystem_fallback,omitempty", typ: reflect.TypeOf(false)},
	}

	for name, want := range required {
		requireContractField(t, typ, name, want.jsonTag, want.typ)
	}
}

func TestResumePacketNestedContract_DriftConflictActionVerification(t *testing.T) {
	action := reflect.TypeOf(StateAction{})
	requireContractField(t, action, "Kind", "kind", reflect.TypeOf(StateActionKind("")))
	requireContractField(t, action, "Description", "description", reflect.TypeOf(""))
	requireContractField(t, action, "Command", "command,omitempty", reflect.TypeOf(""))

	verification := reflect.TypeOf(StateVerification{})
	requireContractField(t, verification, "Kind", "kind", reflect.TypeOf(StateVerificationKind("")))
	requireContractField(t, verification, "Description", "description", reflect.TypeOf(""))
	requireContractField(t, verification, "Command", "command,omitempty", reflect.TypeOf(""))

	drift := reflect.TypeOf(StateDrift{})
	requireContractField(t, drift, "Kind", "kind", reflect.TypeOf(StateDriftKind("")))
	requireContractField(t, drift, "Conflicts", "conflicts", reflect.TypeOf([]StateConflict{}))
	requireContractField(t, drift, "CheckedAt", "checked_at,omitempty", reflect.TypeOf(time.Time{}))

	conflict := reflect.TypeOf(StateConflict{})
	requireContractField(t, conflict, "Field", "field", reflect.TypeOf(""))
	requireContractField(t, conflict, "NativeValue", "native_value,omitempty", reflect.TypeOf(""))
	requireContractField(t, conflict, "FallbackValue", "fallback_value,omitempty", reflect.TypeOf(""))
	requireContractField(t, conflict, "Resolution", "resolution,omitempty", reflect.TypeOf(""))
}

func TestResumePacketEnums_PinNativeFallbackAndContractSourceTaxonomy(t *testing.T) {
	for _, tt := range []struct {
		want string
		got  StatePacketSource
	}{
		{want: "native", got: StatePacketSourceNative},
		{want: "filesystem_fallback", got: StatePacketSourceFilesystemFallback},
		{want: "imported", got: StatePacketSourceImported},
		{want: "mixed", got: StatePacketSourceMixed},
		{want: "conflict", got: StatePacketSourceConflict},
	} {
		if string(tt.got) != tt.want {
			t.Fatalf("StatePacketSource value = %q, want %q", tt.got, tt.want)
		}
	}

	for _, tt := range []struct {
		want string
		got  StateFreshness
	}{
		{want: "fresh", got: StateFreshnessFresh},
		{want: "stale", got: StateFreshnessStale},
		{want: "unknown", got: StateFreshnessUnknown},
	} {
		if string(tt.got) != tt.want {
			t.Fatalf("StateFreshness value = %q, want %q", tt.got, tt.want)
		}
	}

	for _, tt := range []struct {
		want string
		got  StateDriftKind
	}{
		{want: "none", got: StateDriftNone},
		{want: "native_stale", got: StateDriftNativeStale},
		{want: "fallback_newer", got: StateDriftFallbackNewer},
		{want: "conflict", got: StateDriftConflict},
		{want: "unknown", got: StateDriftUnknown},
	} {
		if string(tt.got) != tt.want {
			t.Fatalf("StateDriftKind value = %q, want %q", tt.got, tt.want)
		}
	}

	for _, tt := range []struct {
		want string
		got  StateScopeKind
	}{
		{want: "session", got: StateScopeSession},
		{want: "project", got: StateScopeProject},
		{want: "goal", got: StateScopeGoal},
		{want: "task", got: StateScopeTask},
	} {
		if string(tt.got) != tt.want {
			t.Fatalf("StateScopeKind value = %q, want %q", tt.got, tt.want)
		}
	}

	for _, tt := range []struct {
		want string
		got  StateActionKind
	}{
		{want: "command", got: StateActionCommand},
		{want: "instruction", got: StateActionInstruction},
		{want: "review_gate", got: StateActionReviewGate},
	} {
		if string(tt.got) != tt.want {
			t.Fatalf("StateActionKind value = %q, want %q", tt.got, tt.want)
		}
	}

	for _, tt := range []struct {
		want string
		got  StateVerificationKind
	}{
		{want: "command", got: StateVerificationCommand},
		{want: "artifact", got: StateVerificationArtifact},
		{want: "manual", got: StateVerificationManual},
	} {
		if string(tt.got) != tt.want {
			t.Fatalf("StateVerificationKind value = %q, want %q", tt.got, tt.want)
		}
	}
}

func TestStatePlaneInterface_ReadWriteAgentOwned(t *testing.T) {
	typ := reflect.TypeOf((*StatePlane)(nil)).Elem()

	var got []string
	for i := 0; i < typ.NumMethod(); i++ {
		got = append(got, typ.Method(i).Name)
	}
	sort.Strings(got)

	want := []string{
		"ReadProjectState",
		"ReadResumePacket",
		"ReadSessionState",
		"WriteProjectState",
		"WriteSessionState",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("StatePlane methods:\n  got:  %v\n  want: %v", got, want)
	}
}

type statePlaneCompileCheck struct{}

func (statePlaneCompileCheck) WriteSessionState(context.Context, string, SessionStateSlots) error {
	return nil
}

func (statePlaneCompileCheck) WriteProjectState(context.Context, string, ProjectStateRecord) error {
	return nil
}

func (statePlaneCompileCheck) ReadSessionState(context.Context, string) (SessionStateSlots, error) {
	return SessionStateSlots{}, nil
}

func (statePlaneCompileCheck) ReadProjectState(context.Context, string) (ProjectStateRecord, error) {
	return ProjectStateRecord{}, nil
}

func (statePlaneCompileCheck) ReadResumePacket(context.Context, ResumePacketRequest) (ResumePacket, error) {
	return ResumePacket{}, nil
}

var _ StatePlane = statePlaneCompileCheck{}
