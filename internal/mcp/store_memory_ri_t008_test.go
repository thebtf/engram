package mcp

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

// The v7 store response and durable row must preserve both requested scope fields.
func TestStoreMemory_PrivacyScopeResponseMatchesStoredRow(t *testing.T) {
	project := uuid.NewString()
	env := newMemoryServerForT007(t, project)
	db := env.store.DB

	// Cleanup any stragglers from prior runs against the same DSN
	require.NoError(t, db.Exec(`DELETE FROM memories WHERE project = ?`, project).Error)

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
		// NOTE: privacy_scope=private is intentionally not tested here.
		// Per spec FR-F1 AMEND (fix b5ac7ec, 2026-05-25), store_memory rejects
		// private-scope writes when the caller has no non-empty workstation
		// identity — scope.Resolve fail-closes private memories whose
		// source_workstation_id is empty, making such rows permanently
		// unreadable. Integration tests at this MCP layer run without a
		// SourceClient keycard, so they carry no WorkstationID. The private
		// scope write-rejection is covered by
		// TestRI_F2_InvalidPrivacyScope_StillStructuredErrorUnderFlagOn_T008
		// and the scope.Resolve unit tests.
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
			require.True(t, hasScope, "store response must retain the requested scope; got %v", resp)
			require.Equal(t, tc.wantLegacyScope, scope, "legacy scope must match request value")

			privacyScope, hasPS := resp["privacy_scope"]
			require.True(t, hasPS, "store response must report resolved privacy_scope; got %v", resp)
			require.Equal(t, tc.wantPrivacyScope, privacyScope, "privacy_scope must be the resolved 4-tier value")
			stored, err := env.srv.memoryStore.Get(context.Background(), int64(resp["id"].(float64)))
			require.NoError(t, err)
			require.Equal(t, tc.wantPrivacyScope, stored.PrivacyScope)
			require.Equal(t, project, stored.Project)
		})
	}
}

// TestRI_F2_InvalidPrivacyScope_StillStructuredErrorUnderFlagOn_T008 is a
// regression guard tying the T005 invariant (structured `invalid_privacy_scope:`
// error prefix) to the T008 dual-field surface. The structured error format
// is part of the RI-F2 contract surface — clients parse the error_code
// regardless of which response-shape (dual-field or legacy-only) they're on.
func TestRI_F2_InvalidPrivacyScope_StillStructuredErrorUnderFlagOn_T008(t *testing.T) {
	project := uuid.NewString()
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
