package grpcserver

// session_start_scope_w4_test.go — W4 P1 scope-bypass regression guard.
//
// Tests the fix that closes the gRPC GetSessionStartContext scope bypass
// (triage report §2.2 BYPASS 1): private-scope memories written by workstation-B
// must not appear in responses to callers presenting workstation-A identity when
// ENGRAM_VNEXT_F_ENABLED=true.
//
// Two coverage contracts per W4 fix rules:
//   - Flag-OFF identity: memory list unchanged from pre-fix behavior.
//   - Flag-ON private-row-invisible-cross-workstation: private row from other
//     workstation absent; own-workstation private row present.
//
// DSN-gated: skipped when DATABASE_DSN is unset (same pattern as HappyPath tests).

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/thebtf/engram/internal/auth"
	localgorm "github.com/thebtf/engram/internal/db/gorm"
	"github.com/thebtf/engram/pkg/models"
	pb "github.com/thebtf/engram/proto/engram/v1"
)

// TestEC_F1_P1_GRPCSessionStart_FlagOff_ByteIdentity verifies that with
// ENGRAM_VNEXT_F_ENABLED unset the gRPC session-start path returns memories
// identically to the pre-fix behavior: all project-visible rows regardless of
// privacy_scope. This is the flag-OFF contract from the W4 fix rules.
func TestEC_F1_P1_GRPCSessionStart_FlagOff_ByteIdentity(t *testing.T) {
	db, cleanup := openSessionStartTestDB(t)
	defer cleanup()

	t.Setenv("ENGRAM_VNEXT_F_ENABLED", "") // explicitly off
	t.Setenv("ENGRAM_VNEXT_ENABLED", "false")

	ctx := context.Background()
	project := fmt.Sprintf("grpc-scope-w4-flagoff-%d", time.Now().UnixNano())
	defer db.Exec(`DELETE FROM memories WHERE project = ?`, project)

	memStore := localgorm.NewMemoryStore(&localgorm.Store{DB: db})

	// Private memory from workstation-B.
	_, err := memStore.Create(ctx, &models.Memory{
		Project:             project,
		Content:             "private from ws-B",
		PrivacyScope:        "private",
		SourceWorkstationID: "ws-writer-B",
		SourceSessions:      []string{"sess-b"},
		Tags:                []string{"private-b"},
		SourceAgent:         "test",
		EditedBy:            project,
	})
	require.NoError(t, err)

	// Project-scoped memory (visible to everyone).
	_, err = memStore.Create(ctx, &models.Memory{
		Project:      project,
		Content:      "project scoped",
		PrivacyScope: "project",
		Tags:         []string{"project"},
		SourceAgent:  "test",
		EditedBy:     project,
	})
	require.NoError(t, err)

	// Caller presents workstation-A identity (different from ws-B).
	callerCtx := auth.WithIdentity(ctx, auth.Client("ws-caller-A", "ws-caller-A"))

	srv := &Server{db: db}
	resp, err := srv.GetSessionStartContext(callerCtx, &pb.GetSessionStartContextRequest{
		Project:       project,
		MemoriesLimit: 10,
	})
	require.NoError(t, err)

	// Flag OFF: both memories must be returned (byte-identity with pre-fix behavior).
	assert.Len(t, resp.Memories, 2,
		"flag-OFF: all project memories must be returned regardless of privacy_scope")
}

// TestEC_F1_P1_GRPCSessionStart_FlagOn_PrivateCrossWorkstationInvisible verifies
// the core W4 P1 fix: with ENGRAM_VNEXT_F_ENABLED=true, a private-scope row
// written by workstation-B is not returned to a caller from workstation-A.
func TestEC_F1_P1_GRPCSessionStart_FlagOn_PrivateCrossWorkstationInvisible(t *testing.T) {
	db, cleanup := openSessionStartTestDB(t)
	defer cleanup()

	t.Setenv("ENGRAM_VNEXT_F_ENABLED", "true")
	t.Setenv("ENGRAM_VNEXT_ENABLED", "false") // use the legacy (non-Thompson) branch

	ctx := context.Background()
	project := fmt.Sprintf("grpc-scope-w4-flagon-%d", time.Now().UnixNano())
	defer db.Exec(`DELETE FROM memories WHERE project = ?`, project)

	memStore := localgorm.NewMemoryStore(&localgorm.Store{DB: db})

	// Private memory from workstation-B — must be invisible to ws-A caller.
	_, err := memStore.Create(ctx, &models.Memory{
		Project:             project,
		Content:             "private from ws-B",
		PrivacyScope:        "private",
		SourceWorkstationID: "ws-writer-B",
		SourceSessions:      []string{"sess-b"},
		Tags:                []string{"private-b"},
		SourceAgent:         "test",
		EditedBy:            project,
	})
	require.NoError(t, err)

	// Private memory from workstation-A — must be visible to ws-A caller.
	ownPrivate, err := memStore.Create(ctx, &models.Memory{
		Project:             project,
		Content:             "private from ws-A (own)",
		PrivacyScope:        "private",
		SourceWorkstationID: "ws-caller-A",
		SourceSessions:      []string{"sess-a"},
		Tags:                []string{"private-a"},
		SourceAgent:         "test",
		EditedBy:            project,
	})
	require.NoError(t, err)

	// Project-scoped memory — visible to all.
	projMem, err := memStore.Create(ctx, &models.Memory{
		Project:      project,
		Content:      "project scoped",
		PrivacyScope: "project",
		Tags:         []string{"project"},
		SourceAgent:  "test",
		EditedBy:     project,
	})
	require.NoError(t, err)

	// Caller presents workstation-A identity.
	callerCtx := auth.WithIdentity(ctx, auth.Client("ws-caller-A", "ws-caller-A"))

	srv := &Server{db: db}
	resp, err := srv.GetSessionStartContext(callerCtx, &pb.GetSessionStartContextRequest{
		Project:       project,
		MemoriesLimit: 10,
	})
	require.NoError(t, err)

	// Collect returned IDs.
	returnedIDs := make(map[int64]bool, len(resp.Memories))
	for _, m := range resp.Memories {
		returnedIDs[m.Id] = true
	}

	assert.True(t, returnedIDs[projMem.ID],
		"project-scoped memory must be visible to any caller")
	assert.True(t, returnedIDs[ownPrivate.ID],
		"private memory from caller's own workstation must be visible")
	assert.False(t, returnedIDs[0],
		"sanity: ID 0 must not appear")

	// Core assertion: private row from ws-B must NOT appear.
	for _, m := range resp.Memories {
		if m.Content == "private from ws-B" {
			t.Errorf("P1 bypass NOT fixed: private memory from workstation-B returned to workstation-A caller (id=%d)", m.Id)
		}
	}
	assert.Len(t, resp.Memories, 2,
		"only own-workstation private + project-scoped memories must be returned")
}

// TestEC_F1_P1_GRPCSessionStart_FlagOn_NoCallerIdentity_PrivateInvisible verifies
// fail-closed behavior: when the caller has no workstation identity (e.g., via a
// SourceSession keycard) private-scope memories are not returned.
func TestEC_F1_P1_GRPCSessionStart_FlagOn_NoCallerIdentity_PrivateInvisible(t *testing.T) {
	db, cleanup := openSessionStartTestDB(t)
	defer cleanup()

	t.Setenv("ENGRAM_VNEXT_F_ENABLED", "true")
	t.Setenv("ENGRAM_VNEXT_ENABLED", "false")

	ctx := context.Background()
	project := fmt.Sprintf("grpc-scope-w4-noid-%d", time.Now().UnixNano())
	defer db.Exec(`DELETE FROM memories WHERE project = ?`, project)

	memStore := localgorm.NewMemoryStore(&localgorm.Store{DB: db})

	_, err := memStore.Create(ctx, &models.Memory{
		Project:             project,
		Content:             "private-only",
		PrivacyScope:        "private",
		SourceWorkstationID: "ws-writer",
		SourceSessions:      []string{"sess-x"},
		Tags:                []string{"private"},
		SourceAgent:         "test",
		EditedBy:            project,
	})
	require.NoError(t, err)

	projMem, err := memStore.Create(ctx, &models.Memory{
		Project:      project,
		Content:      "project-visible",
		PrivacyScope: "project",
		Tags:         []string{"project"},
		SourceAgent:  "test",
		EditedBy:     project,
	})
	require.NoError(t, err)

	// Session-authenticated caller: WorkstationID() returns empty string.
	sessionCtx := auth.WithIdentity(ctx, auth.Session("admin-user"))

	srv := &Server{db: db}
	resp, err := srv.GetSessionStartContext(sessionCtx, &pb.GetSessionStartContextRequest{
		Project:       project,
		MemoriesLimit: 10,
	})
	require.NoError(t, err)

	ids := make([]int64, len(resp.Memories))
	for i, m := range resp.Memories {
		ids[i] = m.Id
	}
	assert.Contains(t, ids, projMem.ID,
		"project-scoped memory must appear for session caller")
	for _, m := range resp.Memories {
		if m.Content == "private-only" {
			t.Errorf("P1 fail-closed NOT working: private memory visible to session caller (id=%d)", m.Id)
		}
	}

	_ = os.Getenv("ENGRAM_VNEXT_F_ENABLED") // suppress unused import warning
}
