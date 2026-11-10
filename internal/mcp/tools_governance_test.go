// Package mcp — tools_governance_test.go tests governance MCP tool schema and admin gate.
// Engram vNext Milestone F TG6 / T043.
package mcp

import (
	"context"
	"encoding/json"
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/thebtf/engram/internal/auth"
	gormdb "github.com/thebtf/engram/internal/db/gorm"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// governanceTestServer builds a minimal Server with snapshotStore non-nil.
// Uses a live DB when DATABASE_DSN is set; otherwise uses a nil-store sentinel.
func governanceTestServer(t *testing.T) *Server {
	t.Helper()
	s := NewServer(ServerOptions{Version: "test"})

	dsn := os.Getenv("DATABASE_DSN")
	if dsn == "" {
		// For schema-only tests we need a non-nil snapshotStore but won't call any methods.
		// Create a zero-value SnapshotStore via unexported constructor — not possible externally.
		// Instead: just confirm tools are listed when snapshotStore is nil and the flag is off.
		// Flag-on + nil store: tools should NOT appear. So for schema test we skip this branch.
		return s
	}

	db, err := gorm.Open(postgres.Open(dsn), &gorm.Config{Logger: logger.Default.LogMode(logger.Warn)})
	require.NoError(t, err)
	sqlDB, _ := db.DB()
	t.Cleanup(func() { _ = sqlDB.Close() })

	s.SetSnapshotStore(gormdb.NewSnapshotStore(db))
	return s
}

// TestGovernanceTools_AdvertisementRequiresSnapshotStore verifies the wiring gate.
func TestGovernanceTools_AdvertisementRequiresSnapshotStore(t *testing.T) {
	t.Setenv("ENGRAM_VNEXT_F_ENABLED", "false")
	s := NewServer(ServerOptions{Version: "test"})
	names := func() map[string]bool {
		result := make(map[string]bool)
		for _, tool := range s.ListTools() {
			result[tool.Name] = true
		}
		return result
	}
	governance := []string{"list_snapshots", "rollback_snapshot", "pin_snapshot", "redaction_rules_status"}
	unwired := names()
	for _, name := range governance {
		assert.False(t, unwired[name], "%s must not be advertised without a snapshot store", name)
	}
	s.SetSnapshotStore(gormdb.NewSnapshotStore(nil))
	wired := names()
	for _, name := range governance {
		assert.True(t, wired[name], "%s must be advertised when the snapshot store is wired", name)
	}
}

// TestGovernanceTools_AdminGate_NoIdentity verifies that when no identity is in context
// (auth disabled or context missing), the tool returns admin_required error.
func TestGovernanceTools_AdminGate_NoIdentity(t *testing.T) {
	// Use a bare context (no auth.Identity).
	ctx := context.Background()
	s := governanceTestServer(t)

	toolsToCheck := []struct {
		name string
		args json.RawMessage
	}{
		{"list_snapshots", json.RawMessage(`{}`)},
		{"rollback_snapshot", json.RawMessage(`{"snapshot_id":"test"}`)},
		{"pin_snapshot", json.RawMessage(`{"snapshot_id":"test"}`)},
		{"redaction_rules_status", json.RawMessage(`{}`)},
	}

	for _, tc := range toolsToCheck {
		t.Run(tc.name, func(t *testing.T) {
			_, err := s.callTool(ctx, tc.name, tc.args)
			require.Error(t, err, "%s must return error when no identity in ctx", tc.name)
			assert.Contains(t, err.Error(), "admin_required", "%s must say admin_required", tc.name)
		})
	}
}

// TestGovernanceTools_AdminGate_ReadOnlyCaller verifies read-only callers are rejected.
func TestGovernanceTools_AdminGate_ReadOnlyCaller(t *testing.T) {
	roIdentity := auth.Identity{Role: auth.RoleReadOnly, Source: auth.SourceClient}
	ctx := auth.WithIdentity(context.Background(), roIdentity)
	s := governanceTestServer(t)

	_, err := s.callTool(ctx, "list_snapshots", json.RawMessage(`{}`))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "admin_required")
}

// TestGovernanceTools_RedactionRulesStatus_NoAdminRequired_WithAdmin verifies
// the status of a server with no rules wired at startup.
func TestGovernanceTools_RedactionRulesStatus_NoAdminRequired_WithAdmin(t *testing.T) {
	t.Setenv("ENGRAM_REDACTION_RULES_PATH", "")
	adminID := auth.Identity{Role: auth.RoleAdmin, Source: auth.SourceMaster}
	ctx := auth.WithIdentity(context.Background(), adminID)
	s := NewServer(ServerOptions{Version: "test"})

	result, err := s.handleRedactionRulesStatus(ctx, json.RawMessage(`{}`))
	require.NoError(t, err, "admin caller must not get error from redaction_rules_status")

	var out map[string]any
	require.NoError(t, json.Unmarshal([]byte(result), &out))
	assert.Equal(t, "inactive", out["status"])
	assert.Equal(t, float64(0), out["rule_count"])
	assert.Equal(t, true, out["restart_required"], "restart_required must be true (EC-F9)")
}

func TestGovernanceTools_ListSnapshotsSchemaIncludesReviewActionOpTypes(t *testing.T) {
	tools := governanceTools()
	var listSnapshots *Tool
	for i := range tools {
		if tools[i].Name == "list_snapshots" {
			listSnapshots = &tools[i]
			break
		}
	}
	require.NotNil(t, listSnapshots)

	properties, ok := listSnapshots.InputSchema["properties"].(map[string]any)
	require.True(t, ok)
	opType, ok := properties["op_type"].(map[string]any)
	require.True(t, ok)
	enum, ok := opType["enum"].([]string)
	require.True(t, ok)
	assert.ElementsMatch(t, []string{"ingest_doc", "bulk_promote", "bulk_delete", "bulk_supersede", "candidate_review_action", "forgetting_review_action"}, enum)
}
