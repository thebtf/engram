package mcp

// tools_memory_audit_test.go — TDD tests for audit logging on mutation paths (T002/T003).
// Uses a mock auditWriter injected via setTestAuditWriter to assert that the
// logAudit* helpers emit correct entries without a live DB.

import (
	"context"
	"encoding/json"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	gorm "github.com/thebtf/engram/internal/db/gorm"
	"github.com/thebtf/engram/pkg/models"
)

// ---------------------------------------------------------------------------
// Mock auditWriter
// ---------------------------------------------------------------------------

type mockAuditWriter struct {
	mu      sync.Mutex
	entries []gorm.AuditLogEntry
}

func (m *mockAuditWriter) Log(_ context.Context, entry gorm.AuditLogEntry) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.entries = append(m.entries, entry)
	return nil
}

// drain returns a copy of recorded entries and blocks until at least n entries
// have arrived (with a short timeout so tests don't hang indefinitely).
func (m *mockAuditWriter) waitN(n int) []gorm.AuditLogEntry {
	deadline := time.Now().Add(200 * time.Millisecond)
	for time.Now().Before(deadline) {
		m.mu.Lock()
		if len(m.entries) >= n {
			out := make([]gorm.AuditLogEntry, len(m.entries))
			copy(out, m.entries)
			m.mu.Unlock()
			return out
		}
		m.mu.Unlock()
		time.Sleep(5 * time.Millisecond)
	}
	m.mu.Lock()
	out := make([]gorm.AuditLogEntry, len(m.entries))
	copy(out, m.entries)
	m.mu.Unlock()
	return out
}

func (m *mockAuditWriter) snapshot() []gorm.AuditLogEntry {
	return m.waitN(0)
}

// ---------------------------------------------------------------------------
// T002: logAuditCreate
// ---------------------------------------------------------------------------

// TestAuditCreate_LogCalledOnSuccess verifies that logAuditCreate emits one
// audit_log entry with action="create" when ENGRAM_VNEXT_ENABLED=true.
func TestAuditCreate_LogCalledOnSuccess(t *testing.T) {
	t.Setenv("ENGRAM_VNEXT_ENABLED", "true")

	maw := &mockAuditWriter{}
	srv := NewServer(ServerOptions{Version: "audit-test"})
	srv.setTestAuditWriter(maw)

	created := &models.Memory{ID: 42, Project: "proj", Content: "hello world"}
	logAuditCreate(context.Background(), srv, created, "agent-session")

	entries := maw.waitN(1)
	require.Len(t, entries, 1)
	assert.Equal(t, "create", entries[0].Action)
	assert.Equal(t, int64(42), *entries[0].MemoryID)
	assert.Equal(t, "agent-session", entries[0].Actor)
	assert.NotNil(t, entries[0].AfterState)
	assert.Nil(t, entries[0].BeforeState)
}

// TestAuditCreate_SkippedWhenFlagOff verifies no audit write when flag is off.
func TestAuditCreate_SkippedWhenFlagOff(t *testing.T) {
	t.Setenv("ENGRAM_VNEXT_ENABLED", "false")

	maw := &mockAuditWriter{}
	srv := NewServer(ServerOptions{Version: "audit-test"})
	srv.setTestAuditWriter(maw)

	created := &models.Memory{ID: 99, Project: "proj", Content: "content"}
	logAuditCreate(context.Background(), srv, created, "agent")

	// Give goroutine time to run if it were to fire
	time.Sleep(30 * time.Millisecond)
	assert.Empty(t, maw.snapshot(), "no audit write when ENGRAM_VNEXT_ENABLED != true")
}

// TestAuditCreate_SkippedWhenAuditStoreNil verifies no panic when auditStore is nil.
func TestAuditCreate_SkippedWhenAuditStoreNil(t *testing.T) {
	t.Setenv("ENGRAM_VNEXT_ENABLED", "true")

	srv := NewServer(ServerOptions{Version: "audit-test"})
	// auditStore and testAuditWriter are both nil — must not panic
	assert.NotPanics(t, func() {
		logAuditCreate(context.Background(), srv, &models.Memory{ID: 1}, "agent")
		time.Sleep(10 * time.Millisecond)
	})
}

// ---------------------------------------------------------------------------
// T003: logAuditEdit / logAuditDelete / logAuditSupersede
// ---------------------------------------------------------------------------

// TestAuditEdit_LogCalledWithBeforeAndAfterState asserts action="update" and
// both before_state and after_state are populated.
func TestAuditEdit_LogCalledWithBeforeAndAfterState(t *testing.T) {
	t.Setenv("ENGRAM_VNEXT_ENABLED", "true")

	maw := &mockAuditWriter{}
	srv := NewServer(ServerOptions{Version: "audit-test"})
	srv.setTestAuditWriter(maw)

	before := &models.Memory{ID: 7, Project: "proj", Content: "old content"}
	after := &models.Memory{ID: 7, Project: "proj", Content: "new content"}
	logAuditEdit(context.Background(), srv, before, after, "session-abc")

	entries := maw.waitN(1)
	require.Len(t, entries, 1)
	assert.Equal(t, "update", entries[0].Action)
	assert.Equal(t, int64(7), *entries[0].MemoryID)
	assert.NotNil(t, entries[0].BeforeState)
	assert.NotNil(t, entries[0].AfterState)

	// Verify before_state actually differs from after_state
	var bef, aft map[string]any
	require.NoError(t, json.Unmarshal(*entries[0].BeforeState, &bef))
	require.NoError(t, json.Unmarshal(*entries[0].AfterState, &aft))
	assert.NotEqual(t, bef["content"], aft["content"])
}

// TestAuditDelete_LogCalledWithBeforeState asserts action="delete" and before_state set.
func TestAuditDelete_LogCalledWithBeforeState(t *testing.T) {
	t.Setenv("ENGRAM_VNEXT_ENABLED", "true")

	maw := &mockAuditWriter{}
	srv := NewServer(ServerOptions{Version: "audit-test"})
	srv.setTestAuditWriter(maw)

	mem := &models.Memory{ID: 13, Project: "proj", Content: "to be deleted"}
	logAuditDelete(context.Background(), srv, mem, "actor-x")

	entries := maw.waitN(1)
	require.Len(t, entries, 1)
	assert.Equal(t, "delete", entries[0].Action)
	assert.Equal(t, int64(13), *entries[0].MemoryID)
	assert.NotNil(t, entries[0].BeforeState)
	assert.Nil(t, entries[0].AfterState)
}

// TestAuditSupersede_LogCalledWithSupersededID asserts action="supersede".
func TestAuditSupersede_LogCalledWithSupersededID(t *testing.T) {
	t.Setenv("ENGRAM_VNEXT_ENABLED", "true")

	maw := &mockAuditWriter{}
	srv := NewServer(ServerOptions{Version: "audit-test"})
	srv.setTestAuditWriter(maw)

	supersededMem := &models.Memory{ID: 55, Project: "proj", Content: "superseded content"}
	logAuditSupersede(context.Background(), srv, supersededMem, "actor-y")

	entries := maw.waitN(1)
	require.Len(t, entries, 1)
	assert.Equal(t, "supersede", entries[0].Action)
	assert.Equal(t, int64(55), *entries[0].MemoryID)
	assert.NotNil(t, entries[0].BeforeState)
}

// TestAuditEdit_SkippedWhenFlagOff verifies no write when VNEXT disabled.
func TestAuditEdit_SkippedWhenFlagOff(t *testing.T) {
	t.Setenv("ENGRAM_VNEXT_ENABLED", "false")

	maw := &mockAuditWriter{}
	srv := NewServer(ServerOptions{Version: "audit-test"})
	srv.setTestAuditWriter(maw)

	logAuditEdit(context.Background(), srv, &models.Memory{ID: 1}, &models.Memory{ID: 1}, "a")
	time.Sleep(30 * time.Millisecond)
	assert.Empty(t, maw.snapshot())
}

// TestAuditDelete_SkippedWhenFlagOff mirrors the flag-off check for delete.
func TestAuditDelete_SkippedWhenFlagOff(t *testing.T) {
	t.Setenv("ENGRAM_VNEXT_ENABLED", "false")

	maw := &mockAuditWriter{}
	srv := NewServer(ServerOptions{Version: "audit-test"})
	srv.setTestAuditWriter(maw)

	logAuditDelete(context.Background(), srv, &models.Memory{ID: 2}, "a")
	time.Sleep(30 * time.Millisecond)
	assert.Empty(t, maw.snapshot())
}

// TestAuditSupersede_SkippedWhenFlagOff mirrors the flag-off check for supersede.
func TestAuditSupersede_SkippedWhenFlagOff(t *testing.T) {
	t.Setenv("ENGRAM_VNEXT_ENABLED", "false")

	maw := &mockAuditWriter{}
	srv := NewServer(ServerOptions{Version: "audit-test"})
	srv.setTestAuditWriter(maw)

	logAuditSupersede(context.Background(), srv, &models.Memory{ID: 3}, "a")
	time.Sleep(30 * time.Millisecond)
	assert.Empty(t, maw.snapshot())
}
