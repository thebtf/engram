package cognitive

import (
	"context"
	"reflect"
	"sort"
	"testing"
)

func TestResumePacketContract_RequiredFieldsBinaryDefined(t *testing.T) {
	typ := reflect.TypeOf(ResumePacket{})

	required := map[string]struct {
		jsonTag string
		kind    reflect.Kind
	}{
		"PacketID":         {jsonTag: "packet_id", kind: reflect.String},
		"Project":          {jsonTag: "project", kind: reflect.String},
		"Principal":        {jsonTag: "principal", kind: reflect.String},
		"SessionID":        {jsonTag: "session_id", kind: reflect.String},
		"StateVersion":     {jsonTag: "state_version", kind: reflect.String},
		"Source":           {jsonTag: "source", kind: reflect.String},
		"FallbackUsed":     {jsonTag: "fallback_used", kind: reflect.Bool},
		"Freshness":        {jsonTag: "freshness", kind: reflect.String},
		"Drift":            {jsonTag: "drift", kind: reflect.Struct},
		"NextAction":       {jsonTag: "next_action", kind: reflect.Struct},
		"NextVerification": {jsonTag: "next_verification", kind: reflect.Struct},
		"GeneratedAt":      {jsonTag: "generated_at", kind: reflect.Struct},
		"EvidenceRefs":     {jsonTag: "evidence_refs", kind: reflect.Slice},
	}

	for name, want := range required {
		field, ok := typ.FieldByName(name)
		if !ok {
			t.Fatalf("ResumePacket missing required field %s", name)
		}
		if got := field.Tag.Get("json"); got != want.jsonTag {
			t.Fatalf("ResumePacket.%s json tag = %q, want %q", name, got, want.jsonTag)
		}
		if got := field.Type.Kind(); got != want.kind {
			t.Fatalf("ResumePacket.%s kind = %s, want %s", name, got, want.kind)
		}
	}
}

func TestResumePacketEnums_PinNativeFallbackAndContractSourceTaxonomy(t *testing.T) {
	if StatePacketSourceNative != StatePacketSource("native") {
		t.Fatalf("StatePacketSourceNative = %q, want native", StatePacketSourceNative)
	}
	if StatePacketSourceFilesystemFallback != StatePacketSource("filesystem_fallback") {
		t.Fatalf("StatePacketSourceFilesystemFallback = %q, want filesystem_fallback", StatePacketSourceFilesystemFallback)
	}
	if StatePacketSourceImported != StatePacketSource("imported") {
		t.Fatalf("StatePacketSourceImported = %q, want imported", StatePacketSourceImported)
	}
	if StatePacketSourceMixed != StatePacketSource("mixed") {
		t.Fatalf("StatePacketSourceMixed = %q, want mixed", StatePacketSourceMixed)
	}
	if StatePacketSourceConflict != StatePacketSource("conflict") {
		t.Fatalf("StatePacketSourceConflict = %q, want legacy conflict compatibility value", StatePacketSourceConflict)
	}
	if StateFreshnessFresh != StateFreshness("fresh") {
		t.Fatalf("StateFreshnessFresh = %q, want fresh", StateFreshnessFresh)
	}
	if StateFreshnessStale != StateFreshness("stale") {
		t.Fatalf("StateFreshnessStale = %q, want stale", StateFreshnessStale)
	}
	if StateDriftConflict != StateDriftKind("conflict") {
		t.Fatalf("StateDriftConflict = %q, want conflict", StateDriftConflict)
	}
	if StateActionCommand != StateActionKind("command") {
		t.Fatalf("StateActionCommand = %q, want command", StateActionCommand)
	}
	if StateActionReviewGate != StateActionKind("review_gate") {
		t.Fatalf("StateActionReviewGate = %q, want review_gate", StateActionReviewGate)
	}
	if StateVerificationCommand != StateVerificationKind("command") {
		t.Fatalf("StateVerificationCommand = %q, want command", StateVerificationCommand)
	}
	if StateVerificationArtifact != StateVerificationKind("artifact") {
		t.Fatalf("StateVerificationArtifact = %q, want artifact", StateVerificationArtifact)
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
