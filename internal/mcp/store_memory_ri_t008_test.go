package mcp

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

// TestRI_F2_DualFieldResponse_FlagOn_T008 verifies the RI-F2 (Release
// Invariant — Legacy Scope field present alongside new PrivacyScope) dual-
// field response shape when ENGRAM_VNEXT_F_ENABLED is true.
//
// Spec §FR-F1 + plan.md §Release Invariants Gate RI-F2:
//
//	Legacy Memory.Scope field present in API responses (computed from
//	PrivacyScope) for minimum 2 minor versions (earliest removal v6.7.0).
//
// Asserts:
//   - response includes both `scope` (legacy) AND `privacy_scope` (new) keys
//   - `scope` value matches the legacy 2-tier value that was supplied
//   - `privacy_scope` value matches the resolved 4-tier value
//   - storage = "memories"
//
// Anti-stub: a response that drops either field fails the assertion. A
// response that returns the same value in both fields fails the value
// distinction when private scope is requested (private -> scope stays
// 'project' since legacy 2-tier has no private representation).
func TestRI_F2_DualFieldResponse_FlagOn_T008(t *testing.T) {
	project := "t008-ri-f2-flagon-" + uuid.NewString()
	env := newMemoryServerForT007(t, project)
	db := env.store.DB

	// Cleanup any stragglers from prior runs against the same DSN
	require.NoError(t, db.Exec(`DELETE FROM memories WHERE project = ?`, project).Error)

	// Enable the flag for this test only.
	t.Setenv("ENGRAM_VNEXT_F_ENABLED", "true")

	cases := []struct {
		name               string
		legacyScope        string
		privacyScope       string
		wantLegacyScope    string
		wantPrivacyScope   string
		wantDualFieldsBoth bool
	}{
		{
			name:               "legacy scope=project, no privacy_scope -> dual project",
			legacyScope:        "project",
			privacyScope:       "",
			wantLegacyScope:    "project",
			wantPrivacyScope:   "project",
			wantDualFieldsBoth: true,
		},
		{
			name:               "legacy scope=global, no privacy_scope -> dual global",
			legacyScope:        "global",
			privacyScope:       "",
			wantLegacyScope:    "global",
			wantPrivacyScope:   "global",
			wantDualFieldsBoth: true,
		},
		{
			name:               "explicit privacy_scope=private overrides legacy",
			legacyScope:        "project",
			privacyScope:       "private",
			wantLegacyScope:    "project",
			wantPrivacyScope:   "private",
			wantDualFieldsBoth: true,
		},
		{
			name:               "explicit privacy_scope=shared overrides legacy",
			legacyScope:        "global",
			privacyScope:       "shared",
			wantLegacyScope:    "global",
			wantPrivacyScope:   "shared",
			wantDualFieldsBoth: true,
		},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			args := mustJSON(t, map[string]any{
				"content":       "T008 RI-F2 fixture: " + tc.name,
				"project":       project,
				"scope":         tc.legacyScope,
				"privacy_scope": tc.privacyScope,
			})

			out, err := env.srv.handleStoreMemory(context.Background(), args)
			require.NoError(t, err)

			var resp map[string]any
			require.NoError(t, json.Unmarshal([]byte(out), &resp))

			require.Equal(t, "memories", resp["storage"], "response storage must be 'memories'")

			scope, hasScope := resp["scope"]
			require.True(t, hasScope, "RI-F2: legacy `scope` field must remain in response under flag ON; got %v", resp)
			require.Equal(t, tc.wantLegacyScope, scope, "legacy scope must match request value")

			privacyScope, hasPS := resp["privacy_scope"]
			require.True(t, hasPS, "RI-F2: new `privacy_scope` field must be present under flag ON; got %v", resp)
			require.Equal(t, tc.wantPrivacyScope, privacyScope, "privacy_scope must be the resolved 4-tier value")
		})
	}
}

// TestRI_F2_DualFieldResponse_FlagOff_LegacyOnly_T008 verifies that the
// flag-OFF path returns ONLY the legacy `scope` field — no `privacy_scope`
// surfaces in the response. This is the RI-F1 byte-identical-v6.4.x
// requirement on the response shape side: clients that have not yet adopted
// the new field see no schema drift.
//
// Anti-stub: leaking `privacy_scope` into the flag-OFF response fails this
// assertion.
func TestRI_F2_DualFieldResponse_FlagOff_LegacyOnly_T008(t *testing.T) {
	project := "t008-ri-f2-flagoff-" + uuid.NewString()
	env := newMemoryServerForT007(t, project)
	db := env.store.DB
	require.NoError(t, db.Exec(`DELETE FROM memories WHERE project = ?`, project).Error)

	// Explicitly clear the flag.
	t.Setenv("ENGRAM_VNEXT_F_ENABLED", "")

	// Even when the caller passes privacy_scope, the flag-OFF path must
	// ignore it — both at write-time (no Memory.PrivacyScope population)
	// and at response-time (no privacy_scope field in the JSON output).
	args := mustJSON(t, map[string]any{
		"content":       "T008 RI-F1 flag-OFF fixture",
		"project":       project,
		"scope":         "global",
		"privacy_scope": "shared", // must be silently ignored
	})

	out, err := env.srv.handleStoreMemory(context.Background(), args)
	require.NoError(t, err)

	var resp map[string]any
	require.NoError(t, json.Unmarshal([]byte(out), &resp))

	require.Equal(t, "memories", resp["storage"])
	require.Equal(t, "global", resp["scope"], "legacy scope must echo the request value")

	_, hasPS := resp["privacy_scope"]
	require.False(t, hasPS,
		"RI-F1 flag-OFF: response MUST NOT include privacy_scope key; got %v", resp)

	_, hasWs := resp["source_workstation_id"]
	require.False(t, hasWs,
		"RI-F1 flag-OFF: response MUST NOT include source_workstation_id key; got %v", resp)

	_, hasSess := resp["source_sessions"]
	require.False(t, hasSess,
		"RI-F1 flag-OFF: response MUST NOT include source_sessions key; got %v", resp)
}

// TestRI_F2_InvalidPrivacyScope_StillStructuredErrorUnderFlagOn_T008 is a
// regression guard tying the T005 invariant (structured `invalid_privacy_scope:`
// error prefix) to the T008 dual-field surface. The structured error format
// is part of the RI-F2 contract surface — clients parse the error_code
// regardless of which response-shape (dual-field or legacy-only) they're on.
func TestRI_F2_InvalidPrivacyScope_StillStructuredErrorUnderFlagOn_T008(t *testing.T) {
	project := "t008-ri-f2-invalid-" + uuid.NewString()
	env := newMemoryServerForT007(t, project)

	t.Setenv("ENGRAM_VNEXT_F_ENABLED", "true")

	args := mustJSON(t, map[string]any{
		"content":       "T008 invalid privacy_scope regression guard",
		"project":       project,
		"privacy_scope": "ADMIN", // not in the 4-tier enum
	})

	_, err := env.srv.handleStoreMemory(context.Background(), args)
	require.Error(t, err)
	require.Contains(t, err.Error(), "invalid_privacy_scope:",
		"structured error prefix must remain under T008 surface; got %q", err.Error())
}
