// Package models — T032 RED test: write-lint domain types shape contract.
// Verifies JSON tag stability and struct field presence per spec §FR-F5.
package models

import (
	"encoding/json"
	"reflect"
	"testing"
)

// TestWriteLint_DomainTypes_T032 verifies that all write-lint domain types have
// the expected fields and JSON tags per spec §FR-F5.
func TestWriteLint_DomainTypes_T032(t *testing.T) {
	t.Run("LintSignalType_enum_values", func(t *testing.T) {
		expected := []LintSignalType{
			LintSignalPossibleDuplicate,
			LintSignalPossibleConflict,
			LintSignalSupersessionCandidate,
			LintSignalMissingProvenance,
			LintSignalLowConfidenceWithoutBasis,
			LintSignalPrivateDataRisk,
		}
		if len(expected) != 6 {
			t.Fatalf("expected 6 LintSignalType values, found %d", len(expected))
		}
		// Verify each has the correct string value
		vals := map[LintSignalType]string{
			LintSignalPossibleDuplicate:          "possible_duplicate",
			LintSignalPossibleConflict:            "possible_conflict",
			LintSignalSupersessionCandidate:       "supersession_candidate",
			LintSignalMissingProvenance:           "missing_provenance",
			LintSignalLowConfidenceWithoutBasis:   "low_confidence_without_basis",
			LintSignalPrivateDataRisk:             "private_data_risk",
		}
		for k, v := range vals {
			if string(k) != v {
				t.Errorf("LintSignalType %q: expected string value %q", k, v)
			}
		}
	})

	t.Run("LintSignal_JSON_tags", func(t *testing.T) {
		sig := LintSignal{
			Type:              LintSignalPossibleDuplicate,
			SimilarMemoryID:   ptr64(42),
			SimilarityScore:   0.93,
			SimilarityMethod:  "jaccard+cosine",
			ConflictingMemoryID: nil,
			ConflictType:      "",
			Reason:            "",
			OlderMemoryID:     nil,
			Evidence:          "",
		}
		data, err := json.Marshal(sig)
		if err != nil {
			t.Fatalf("marshal LintSignal: %v", err)
		}
		var m map[string]any
		if err := json.Unmarshal(data, &m); err != nil {
			t.Fatalf("unmarshal LintSignal: %v", err)
		}
		if _, ok := m["type"]; !ok {
			t.Error("LintSignal: missing JSON field 'type'")
		}
		if _, ok := m["similar_memory_id"]; !ok {
			t.Error("LintSignal: missing JSON field 'similar_memory_id'")
		}
		if _, ok := m["similarity_score"]; !ok {
			t.Error("LintSignal: missing JSON field 'similarity_score'")
		}
	})

	t.Run("ResolutionOption_JSON_tags", func(t *testing.T) {
		opt := ResolutionOption{
			Option:   "merge_with",
			MemoryID: ptr64(42),
			Result:   "update memory 42 with merged content",
		}
		data, err := json.Marshal(opt)
		if err != nil {
			t.Fatalf("marshal ResolutionOption: %v", err)
		}
		var m map[string]any
		if err := json.Unmarshal(data, &m); err != nil {
			t.Fatalf("unmarshal ResolutionOption: %v", err)
		}
		for _, field := range []string{"option", "result"} {
			if _, ok := m[field]; !ok {
				t.Errorf("ResolutionOption: missing JSON field %q", field)
			}
		}
	})

	t.Run("ResolutionToken_JSON_tags", func(t *testing.T) {
		tok := ResolutionToken{
			Token:   "wlrt_abc123",
			TTLSecs: 600,
		}
		data, err := json.Marshal(tok)
		if err != nil {
			t.Fatalf("marshal ResolutionToken: %v", err)
		}
		var m map[string]any
		if err := json.Unmarshal(data, &m); err != nil {
			t.Fatalf("unmarshal ResolutionToken: %v", err)
		}
		if _, ok := m["token"]; !ok {
			t.Error("ResolutionToken: missing JSON field 'token'")
		}
	})

	t.Run("WriteResolutionPhase1Response_fields", func(t *testing.T) {
		r := WriteResolutionPhase1Response{
			Stored:            false,
			LintSignals:       []LintSignal{{Type: LintSignalPossibleDuplicate}},
			ResolutionOptions: []ResolutionOption{{Option: "merge_with"}},
			ResolutionToken:   "wlrt_abc123",
		}
		data, err := json.Marshal(r)
		if err != nil {
			t.Fatalf("marshal Phase1Response: %v", err)
		}
		var m map[string]any
		if err := json.Unmarshal(data, &m); err != nil {
			t.Fatalf("unmarshal Phase1Response: %v", err)
		}
		for _, field := range []string{"stored", "lint_signals", "resolution_options", "resolution_token"} {
			if _, ok := m[field]; !ok {
				t.Errorf("Phase1Response: missing JSON field %q", field)
			}
		}
	})

	t.Run("WriteResolutionPhase2Request_fields", func(t *testing.T) {
		req := WriteResolutionPhase2Request{
			ResolutionToken: "wlrt_abc123",
			Option:          "merge_with",
			TargetMemoryID:  ptr64(42),
		}
		rt := reflect.TypeOf(req)
		fields := map[string]bool{}
		for i := 0; i < rt.NumField(); i++ {
			fields[rt.Field(i).Name] = true
		}
		for _, name := range []string{"ResolutionToken", "Option", "TargetMemoryID"} {
			if !fields[name] {
				t.Errorf("WriteResolutionPhase2Request: missing field %q", name)
			}
		}
	})

	t.Run("WriteResolutionPhase2Response_fields", func(t *testing.T) {
		resp := WriteResolutionPhase2Response{
			Stored:      true,
			MemoryID:    42,
			ActionTaken: "merge",
			AuditLogID:  5678,
		}
		data, err := json.Marshal(resp)
		if err != nil {
			t.Fatalf("marshal Phase2Response: %v", err)
		}
		var m map[string]any
		if err := json.Unmarshal(data, &m); err != nil {
			t.Fatalf("unmarshal Phase2Response: %v", err)
		}
		for _, field := range []string{"stored", "memory_id", "action_taken", "audit_log_id"} {
			if _, ok := m[field]; !ok {
				t.Errorf("Phase2Response: missing JSON field %q", field)
			}
		}
	})
}

func ptr64(v int64) *int64 { return &v }
