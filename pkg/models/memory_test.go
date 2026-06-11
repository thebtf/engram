package models

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

// TestMemory_PrivacyScope_T002 verifies the Memory struct exposes the two new
// fields added by engram vNext Milestone F TG1/T002:
//
//   - PrivacyScope string         json:"privacy_scope,omitempty"
//   - SourceSessions []string     json:"source_sessions,omitempty"
//
// Asserts:
//   - Both fields exist with the documented Go types.
//   - JSON tags match the spec §FR-F1 schema delta.
//   - Existing fields preserve their JSON tag names (no rename collateral).
//
// Anti-stub property: deleting either field or renaming the JSON tag fails this
// test — reflection inspects the struct shape directly rather than relying on
// behaviour.
func TestMemory_PrivacyScope_T002(t *testing.T) {
	memT := reflect.TypeOf(Memory{})

	// PrivacyScope field.
	privacyScope, ok := memT.FieldByName("PrivacyScope")
	if !ok {
		t.Fatal("Memory.PrivacyScope field missing — T002 not implemented")
	}
	if privacyScope.Type.Kind() != reflect.String {
		t.Errorf("Memory.PrivacyScope: want kind=string, got %s", privacyScope.Type.Kind())
	}
	if got, want := privacyScope.Tag.Get("json"), "privacy_scope,omitempty"; got != want {
		t.Errorf("Memory.PrivacyScope json tag: want %q, got %q", want, got)
	}

	// SourceSessions field.
	sourceSessions, ok := memT.FieldByName("SourceSessions")
	if !ok {
		t.Fatal("Memory.SourceSessions field missing — T002 not implemented")
	}
	if sourceSessions.Type.Kind() != reflect.Slice {
		t.Errorf("Memory.SourceSessions: want kind=slice, got %s", sourceSessions.Type.Kind())
	}
	if sourceSessions.Type.Elem().Kind() != reflect.String {
		t.Errorf("Memory.SourceSessions: want []string, got []%s", sourceSessions.Type.Elem().Kind())
	}
	if got, want := sourceSessions.Tag.Get("json"), "source_sessions,omitempty"; got != want {
		t.Errorf("Memory.SourceSessions json tag: want %q, got %q", want, got)
	}

	// SourceWorkstationID field (T003b — AMEND 2026-05-25 to close FR-F1 gap).
	sourceWs, ok := memT.FieldByName("SourceWorkstationID")
	if !ok {
		t.Fatal("Memory.SourceWorkstationID field missing — T003b not implemented")
	}
	if sourceWs.Type.Kind() != reflect.String {
		t.Errorf("Memory.SourceWorkstationID: want kind=string, got %s", sourceWs.Type.Kind())
	}
	if got, want := sourceWs.Tag.Get("json"), "source_workstation_id,omitempty"; got != want {
		t.Errorf("Memory.SourceWorkstationID json tag: want %q, got %q", want, got)
	}

	// Pre-existing field tags survive (regression guard for accidental rename).
	for _, prev := range []struct{ field, tag string }{
		{"Project", "project"},
		{"Content", "content"},
		{"SourceAgent", "source_agent,omitempty"},
		{"Tier", "tier,omitempty"},
		{"EpistemicType", "epistemic_type,omitempty"},
		{"PromotionTarget", "promotion_target,omitempty"},
		{"Tags", "tags"},
	} {
		f, ok := memT.FieldByName(prev.field)
		if !ok {
			t.Errorf("regression: Memory.%s field disappeared", prev.field)
			continue
		}
		if got := f.Tag.Get("json"); got != prev.tag {
			t.Errorf("regression: Memory.%s json tag drift: want %q, got %q", prev.field, prev.tag, got)
		}
	}
}

// TestMemory_PrivacyScope_JSONRoundtrip exercises the omitempty contract:
// zero-value PrivacyScope + nil SourceSessions must not appear in the JSON
// payload at all (RI-F1 backward-compat — v6.4.x consumers parsing the
// response see no new fields when the server is not yet using them).
func TestMemory_PrivacyScope_JSONRoundtrip(t *testing.T) {
	// Empty privacy_scope + nil sessions — must be absent from JSON.
	zero := Memory{ID: 1, Project: "p", Content: "c"}
	zeroJSON, err := json.Marshal(zero)
	if err != nil {
		t.Fatalf("marshal zero Memory: %v", err)
	}
	if strings.Contains(string(zeroJSON), "privacy_scope") {
		t.Errorf("zero PrivacyScope must be omitted (omitempty); got JSON: %s", zeroJSON)
	}
	if strings.Contains(string(zeroJSON), "source_sessions") {
		t.Errorf("nil SourceSessions must be omitted (omitempty); got JSON: %s", zeroJSON)
	}

	// Populated values — must appear in JSON with documented keys.
	populated := Memory{
		ID:             2,
		Project:        "p",
		Content:        "c",
		PrivacyScope:   "private",
		SourceSessions: []string{"sess-a", "sess-b"},
	}
	popJSON, err := json.Marshal(populated)
	if err != nil {
		t.Fatalf("marshal populated Memory: %v", err)
	}
	if !strings.Contains(string(popJSON), `"privacy_scope":"private"`) {
		t.Errorf("populated PrivacyScope missing or wrong key in JSON: %s", popJSON)
	}
	if !strings.Contains(string(popJSON), `"source_sessions":["sess-a","sess-b"]`) {
		t.Errorf("populated SourceSessions missing or wrong shape in JSON: %s", popJSON)
	}

	// Roundtrip.
	var got Memory
	if err := json.Unmarshal(popJSON, &got); err != nil {
		t.Fatalf("unmarshal populated Memory: %v", err)
	}
	if got.PrivacyScope != "private" {
		t.Errorf("roundtrip PrivacyScope: want %q, got %q", "private", got.PrivacyScope)
	}
	if !reflect.DeepEqual(got.SourceSessions, []string{"sess-a", "sess-b"}) {
		t.Errorf("roundtrip SourceSessions: want %v, got %v", []string{"sess-a", "sess-b"}, got.SourceSessions)
	}
}
