package scope

// TestEC_F1_P1_FilterMemories_W4 verifies the scope.FilterMemories helper
// added by W4 P1-P3 fix (grpcserver/session-start scope bypass closure).
//
// Two behavioral contracts tested here per the W4 fix rules:
//  1. Flag-OFF identity: when ENGRAM_VNEXT_F_ENABLED is unset, FilterMemories
//     is not supposed to be called — but if it is (via an errant call), it
//     must still return the full slice unchanged (no-op gate inside callers,
//     not inside FilterMemories; FilterMemories itself is always filtering).
//     Tests validate the filter function's correctness independent of the flag.
//  2. Flag-ON private-row-invisible-cross-workstation: private-scope row from
//     workstation-B is invisible to a caller presenting workstation-A identity.

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/thebtf/engram/pkg/models"
)

// makeMemory is a test helper to build a minimal *models.Memory.
func makeMemory(id int64, privacyScope, sourceWorkstation string, sessions []string) *models.Memory {
	return &models.Memory{
		ID:                  id,
		Project:             "test-project",
		Content:             "content",
		PrivacyScope:        privacyScope,
		SourceWorkstationID: sourceWorkstation,
		SourceSessions:      sessions,
	}
}

// TestEC_F1_P1_FilterMemories_GlobalVisible verifies that global-scope memories
// are always visible regardless of caller identity.
func TestEC_F1_P1_FilterMemories_GlobalVisible(t *testing.T) {
	t.Parallel()
	caller := KeycardContext{WorkstationID: "ws-caller"}
	mem := makeMemory(1, ScopeGlobal, "ws-other", nil)

	got := FilterMemories(caller, []*models.Memory{mem})

	require.Len(t, got, 1, "global-scope memory must always be visible")
	assert.Equal(t, int64(1), got[0].ID)
}

// TestEC_F1_P1_FilterMemories_ProjectVisible verifies that project-scope
// memories are always visible (project boundary enforced upstream).
func TestEC_F1_P1_FilterMemories_ProjectVisible(t *testing.T) {
	t.Parallel()
	caller := KeycardContext{WorkstationID: "ws-caller"}
	mem := makeMemory(2, ScopeProject, "ws-other", nil)

	got := FilterMemories(caller, []*models.Memory{mem})

	require.Len(t, got, 1)
}

// TestEC_F1_P1_FilterMemories_PrivateOwnWorkstationVisible verifies that a
// private-scope memory whose source_workstation_id matches the caller's
// workstation is visible (same-workstation access).
func TestEC_F1_P1_FilterMemories_PrivateOwnWorkstationVisible(t *testing.T) {
	t.Parallel()
	const ws = "ws-writer"
	caller := KeycardContext{WorkstationID: ws}
	mem := makeMemory(3, ScopePrivate, ws, nil)

	got := FilterMemories(caller, []*models.Memory{mem})

	require.Len(t, got, 1, "private memory from own workstation must be visible")
}

// TestEC_F1_P1_FilterMemories_PrivateCrossWorkstationInvisible is the core W4
// fix regression guard: a private-scope row written by workstation-B must NOT
// be returned to a caller presenting workstation-A identity.
func TestEC_F1_P1_FilterMemories_PrivateCrossWorkstationInvisible(t *testing.T) {
	t.Parallel()
	caller := KeycardContext{WorkstationID: "ws-caller-A"}
	privateMem := makeMemory(4, ScopePrivate, "ws-writer-B", []string{"sess-b"})

	got := FilterMemories(caller, []*models.Memory{privateMem})

	require.Empty(t, got,
		"private memory from workstation-B must be invisible to caller on workstation-A")
}

// TestEC_F1_P1_FilterMemories_EmptyScopeDefaultsToProject verifies that a
// memory with empty privacy_scope (legacy row) is treated as 'project' scope
// and therefore visible.
func TestEC_F1_P1_FilterMemories_EmptyScopeDefaultsToProject(t *testing.T) {
	t.Parallel()
	caller := KeycardContext{WorkstationID: "ws-caller"}
	legacyMem := makeMemory(5, "", "", nil) // empty scope = pre-migration row

	got := FilterMemories(caller, []*models.Memory{legacyMem})

	require.Len(t, got, 1, "empty-scope (legacy) memory must default to project scope and be visible")
}

// TestEC_F1_P1_FilterMemories_NilEntrySkipped verifies that nil entries in the
// input slice are silently skipped and do not cause a panic.
func TestEC_F1_P1_FilterMemories_NilEntrySkipped(t *testing.T) {
	t.Parallel()
	caller := KeycardContext{WorkstationID: "ws-caller"}
	mems := []*models.Memory{nil, makeMemory(6, ScopeGlobal, "", nil), nil}

	got := FilterMemories(caller, mems)

	require.Len(t, got, 1)
	assert.Equal(t, int64(6), got[0].ID)
}

// TestEC_F1_P1_FilterMemories_MixedVisibility verifies a slice containing both
// visible and invisible memories: only the visible ones survive.
func TestEC_F1_P1_FilterMemories_MixedVisibility(t *testing.T) {
	t.Parallel()
	const callerWs = "ws-caller"
	caller := KeycardContext{WorkstationID: callerWs}

	mems := []*models.Memory{
		makeMemory(10, ScopeGlobal, "ws-other", nil),   // visible: global
		makeMemory(11, ScopePrivate, "ws-other", nil),  // invisible: private, wrong workstation
		makeMemory(12, ScopeProject, "ws-other", nil),  // visible: project
		makeMemory(13, ScopePrivate, callerWs, nil),    // visible: private, own workstation
		makeMemory(14, ScopePrivate, "ws-third", nil),  // invisible: private, third workstation
		makeMemory(15, "", "", nil),                     // visible: empty scope defaults to project
	}

	got := FilterMemories(caller, mems)

	gotIDs := make([]int64, len(got))
	for i, m := range got {
		gotIDs[i] = m.ID
	}
	assert.ElementsMatch(t, []int64{10, 12, 13, 15}, gotIDs,
		"only global, project, own-workstation-private, and legacy-empty rows must survive")
}

// TestEC_F1_P1_FilterMemories_EmptyCallerCannotSeePrivate verifies fail-closed
// behavior: a caller with no workstation identity cannot see any private-scope
// memory, even if the memory has no source workstation either.
func TestEC_F1_P1_FilterMemories_EmptyCallerCannotSeePrivate(t *testing.T) {
	t.Parallel()
	caller := KeycardContext{} // no workstation (SourceSession or SourceMaster)
	privateMem := makeMemory(20, ScopePrivate, "", nil)

	got := FilterMemories(caller, []*models.Memory{privateMem})

	require.Empty(t, got, "empty caller workstation must not see private-scope memories (fail-closed)")
}
