package mcp

// tools_memory_edit_test.go — regression tests for cross-review findings on
// handleEditMemory (Findings 1, 2, 3).
//
// Finding 1: edit content must pass through hard-limit, soft-truncation, and
//            secret-redaction — identical to the create path.
// Finding 2: edit must refuse cross-project access; callers cannot edit another
//            project's memory by id when EnforceSourceProject is enabled.
// Finding 3: SourceSessionID in the audit entry must be the raw session ID from
//            context, not the actor fallback "agent".

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/thebtf/engram/internal/config"
	"github.com/thebtf/engram/pkg/models"
)

// reloadConfig forces the global config singleton to re-read from the environment.
// Required because config.Get() is cached via sync.Once; t.Setenv alone is not enough.
func reloadConfig(t *testing.T) {
	t.Helper()
	_, _, err := config.Reload()
	if err != nil {
		t.Logf("config.Reload warning (non-fatal in test environment): %v", err)
	}
}

// ---------------------------------------------------------------------------
// Mock memory editor
// ---------------------------------------------------------------------------

type mockMemoryEditor struct {
	stored   map[int64]*models.Memory // in-memory records
	updateFn func(m *models.Memory) (*models.Memory, error)
	getFn    func(id int64) (*models.Memory, error)
}

func newMockMemoryEditor() *mockMemoryEditor {
	return &mockMemoryEditor{stored: make(map[int64]*models.Memory)}
}

func (m *mockMemoryEditor) seed(mem *models.Memory) {
	m.stored[mem.ID] = mem
}

func (m *mockMemoryEditor) Get(_ context.Context, id int64) (*models.Memory, error) {
	if m.getFn != nil {
		return m.getFn(id)
	}
	mem, ok := m.stored[id]
	if !ok {
		return nil, fmt.Errorf("not found")
	}
	// return a copy
	cp := *mem
	return &cp, nil
}

func (m *mockMemoryEditor) Update(_ context.Context, mem *models.Memory) (*models.Memory, error) {
	if m.updateFn != nil {
		return m.updateFn(mem)
	}
	cp := *mem
	m.stored[mem.ID] = &cp
	return &cp, nil
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

func editArgs(id int64, narrative string) json.RawMessage {
	b, _ := json.Marshal(map[string]any{
		"action":    "edit",
		"id":        id,
		"narrative": narrative,
	})
	return b
}

func newEditServer(t *testing.T, mem *mockMemoryEditor) *Server {
	t.Helper()
	srv := NewServer(ServerOptions{Version: "edit-test"})
	srv.setTestMemoryEditor(mem)
	return srv
}

// ---------------------------------------------------------------------------
// Finding 1: hard limit rejection
// ---------------------------------------------------------------------------

func TestEditMemory_HardLimitRejected(t *testing.T) {
	t.Setenv("ENGRAM_ENFORCE_SOURCE_PROJECT", "false")
	reloadConfig(t)

	mem := newMockMemoryEditor()
	mem.seed(&models.Memory{ID: 1, Project: "proj", Content: "original"})

	srv := newEditServer(t, mem)

	// Default hard limit is 10 000 runes — build a string just over it.
	overLimit := strings.Repeat("a", 10001)
	_, err := srv.handleEditMemory(context.Background(), editArgs(1, overLimit))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "exceeds maximum length")
}

// ---------------------------------------------------------------------------
// Finding 1: soft-limit truncation
// ---------------------------------------------------------------------------

func TestEditMemory_SoftLimitTruncates(t *testing.T) {
	t.Setenv("ENGRAM_ENFORCE_SOURCE_PROJECT", "false")
	reloadConfig(t)

	mem := newMockMemoryEditor()
	mem.seed(&models.Memory{ID: 2, Project: "proj", Content: "original"})

	srv := newEditServer(t, mem)

	// Default soft limit is 1 000 runes; build content just over it.
	overSoft := strings.Repeat("b", 1001)
	_, err := srv.handleEditMemory(context.Background(), editArgs(2, overSoft))
	require.NoError(t, err)

	stored := mem.stored[2]
	require.NotNil(t, stored)
	assert.Equal(t, 1000, utf8.RuneCountInString(stored.Content),
		"content must be truncated to soft limit")
}

// ---------------------------------------------------------------------------
// Finding 1: secret redaction
// ---------------------------------------------------------------------------

func TestEditMemory_SecretRedacted(t *testing.T) {
	t.Setenv("ENGRAM_ENFORCE_SOURCE_PROJECT", "false")
	reloadConfig(t)

	mem := newMockMemoryEditor()
	mem.seed(&models.Memory{ID: 3, Project: "proj", Content: "original"})

	srv := newEditServer(t, mem)

	// A plausible AWS-style key that the privacy package should detect.
	secretContent := "here is my key AKIAIOSFODNN7EXAMPLE and secret wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY"
	_, err := srv.handleEditMemory(context.Background(), editArgs(3, secretContent))
	require.NoError(t, err)

	stored := mem.stored[3]
	require.NotNil(t, stored)
	assert.NotContains(t, stored.Content, "AKIAIOSFODNN7EXAMPLE",
		"raw secret key must not appear in stored content after edit")
}

// ---------------------------------------------------------------------------
// Finding 2: cross-project access denied (EnforceSourceProject=true)
// ---------------------------------------------------------------------------

func TestEditMemory_CrossProjectDenied(t *testing.T) {
	t.Setenv("ENGRAM_ENFORCE_SOURCE_PROJECT", "true")
	reloadConfig(t)

	mem := newMockMemoryEditor()
	mem.seed(&models.Memory{ID: 10, Project: "project-A", Content: "owned by A"})

	srv := newEditServer(t, mem)

	// Inject project-B into context — should be denied.
	ctx := ContextWithProject(context.Background(), "project-B")
	_, err := srv.handleEditMemory(ctx, editArgs(10, "attempted override"))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not found",
		"cross-project edit must return not-found (no existence leak)")

	// Verify original content is unchanged.
	assert.Equal(t, "owned by A", mem.stored[10].Content)
}

// ---------------------------------------------------------------------------
// Finding 2: same-project edit allowed (EnforceSourceProject=true)
// ---------------------------------------------------------------------------

func TestEditMemory_SameProjectAllowed(t *testing.T) {
	t.Setenv("ENGRAM_ENFORCE_SOURCE_PROJECT", "true")
	reloadConfig(t)

	mem := newMockMemoryEditor()
	mem.seed(&models.Memory{ID: 20, Project: "project-A", Content: "old content"})

	srv := newEditServer(t, mem)

	ctx := ContextWithProject(context.Background(), "project-A")
	_, err := srv.handleEditMemory(ctx, editArgs(20, "new content"))
	require.NoError(t, err)
	assert.Equal(t, "new content", mem.stored[20].Content)
}

// ---------------------------------------------------------------------------
// Finding 2: enforcement skipped when EnforceSourceProject=false
// ---------------------------------------------------------------------------

func TestEditMemory_CrossProjectAllowedWhenEnforcementOff(t *testing.T) {
	t.Setenv("ENGRAM_ENFORCE_SOURCE_PROJECT", "false")
	reloadConfig(t)

	mem := newMockMemoryEditor()
	mem.seed(&models.Memory{ID: 30, Project: "project-A", Content: "original"})

	srv := newEditServer(t, mem)

	ctx := ContextWithProject(context.Background(), "project-B")
	_, err := srv.handleEditMemory(ctx, editArgs(30, "updated"))
	require.NoError(t, err)
	assert.Equal(t, "updated", mem.stored[30].Content)
}

// ---------------------------------------------------------------------------
// Finding 3: SourceSessionID is session from context, not actor fallback
// ---------------------------------------------------------------------------

func TestEditMemory_AuditSourceSessionIDFromContext(t *testing.T) {
	t.Setenv("ENGRAM_VNEXT_ENABLED", "true")
	t.Setenv("ENGRAM_ENFORCE_SOURCE_PROJECT", "false")
	reloadConfig(t)

	mem := newMockMemoryEditor()
	mem.seed(&models.Memory{ID: 40, Project: "proj", Content: "before"})

	maw := &mockAuditWriter{}
	srv := newEditServer(t, mem)
	srv.setTestAuditWriter(maw)

	// Inject a distinct session ID.
	ctx := ContextWithSession(context.Background(), "sess-abc-123")
	_, err := srv.handleEditMemory(ctx, editArgs(40, "after"))
	require.NoError(t, err)

	entries := maw.waitN(1)
	require.Len(t, entries, 1)
	assert.Equal(t, "update", entries[0].Action)
	assert.Equal(t, "sess-abc-123", entries[0].SourceSessionID,
		"SourceSessionID must be the session from context, not the actor fallback")
}

// TestEditMemory_AuditSourceSessionIDEmptyWhenNoSession verifies that when no
// session is in context, SourceSessionID is empty string (not "agent").
func TestEditMemory_AuditSourceSessionIDEmptyWhenNoSession(t *testing.T) {
	t.Setenv("ENGRAM_VNEXT_ENABLED", "true")
	t.Setenv("ENGRAM_ENFORCE_SOURCE_PROJECT", "false")
	reloadConfig(t)

	mem := newMockMemoryEditor()
	mem.seed(&models.Memory{ID: 50, Project: "proj", Content: "before"})

	maw := &mockAuditWriter{}
	srv := newEditServer(t, mem)
	srv.setTestAuditWriter(maw)

	// No session in context.
	_, err := srv.handleEditMemory(context.Background(), editArgs(50, "after"))
	require.NoError(t, err)

	entries := maw.waitN(1)
	require.Len(t, entries, 1)
	assert.Equal(t, "", entries[0].SourceSessionID,
		"SourceSessionID must be empty string when no session in context")
}

// ---------------------------------------------------------------------------
// Finding 4: runAuditAsync panic recovery — no process crash
// ---------------------------------------------------------------------------

func TestRunAuditAsync_PanicRecovered(t *testing.T) {
	assert.NotPanics(t, func() {
		runAuditAsync("test-panic", 99, func(_ context.Context) error {
			panic("simulated audit panic")
		})
		// Give the goroutine time to execute.
		time.Sleep(50 * time.Millisecond)
	}, "panic inside runAuditAsync goroutine must not crash the caller")
}

// TestRunAuditAsync_ErrorLogged verifies that a non-nil error from fn does not
// propagate (fire-and-forget contract) but also does not panic.
func TestRunAuditAsync_ErrorLogged(t *testing.T) {
	assert.NotPanics(t, func() {
		runAuditAsync("test-error", 88, func(_ context.Context) error {
			return fmt.Errorf("simulated db error")
		})
		time.Sleep(50 * time.Millisecond)
	}, "error inside runAuditAsync goroutine must not panic the caller")
}
