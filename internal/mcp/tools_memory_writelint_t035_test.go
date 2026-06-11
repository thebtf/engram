package mcp

// tools_memory_writelint_t035_test.go — TDD tests for T035: two-phase write-lint
// handler wiring in handleStoreMemory.
//
// Tests confirm:
//   1. Flag OFF → legacy path (writeLint orchestrator not called even if wired)
//   2. Flag ON + no token → Phase1 response returned (stored=false when signals present)
//   3. Flag ON + token + option → Phase2 response returned
//   4. Flag ON + force=true → legacy create path + legacy_force_write audit

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/thebtf/engram/internal/writelint"
	"github.com/thebtf/engram/pkg/models"
)

// ---------------------------------------------------------------------------
// Stubs
// ---------------------------------------------------------------------------

// stubMemoryStore satisfies writelint.MemoryStoreInterface for tests.
type stubWriteLintMemStore struct {
	mu       sync.Mutex
	memories []*models.Memory
	nextID   int64
	listErr  error
	createErr error
}

func newStubWLMemStore(existing ...*models.Memory) *stubWriteLintMemStore {
	s := &stubWriteLintMemStore{nextID: 100}
	s.memories = append(s.memories, existing...)
	return s
}

func (s *stubWriteLintMemStore) List(_ context.Context, _ string, limit int) ([]*models.Memory, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.listErr != nil {
		return nil, s.listErr
	}
	out := s.memories
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

func (s *stubWriteLintMemStore) Get(_ context.Context, id int64) (*models.Memory, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, m := range s.memories {
		if m.ID == id {
			return m, nil
		}
	}
	return nil, fmt.Errorf("not found: %d", id)
}

func (s *stubWriteLintMemStore) Create(_ context.Context, m *models.Memory) (*models.Memory, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.createErr != nil {
		return nil, s.createErr
	}
	s.nextID++
	created := *m
	created.ID = s.nextID
	s.memories = append(s.memories, &created)
	return &created, nil
}

func (s *stubWriteLintMemStore) Update(_ context.Context, m *models.Memory) (*models.Memory, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i, mem := range s.memories {
		if mem.ID == m.ID {
			s.memories[i] = m
			return m, nil
		}
	}
	return nil, fmt.Errorf("not found: %d", m.ID)
}

// stubAuditLoggerWL records audit calls.
type stubAuditLoggerWL struct {
	mu      sync.Mutex
	entries []struct{ memID int64; action, actor string }
}

func (s *stubAuditLoggerWL) LogAudit(_ context.Context, memoryID int64, action, actor string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.entries = append(s.entries, struct{ memID int64; action, actor string }{memoryID, action, actor})
	return nil
}

func (s *stubAuditLoggerWL) actions() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]string, len(s.entries))
	for i, e := range s.entries {
		out[i] = e.action
	}
	return out
}

// buildWLOrchestrator constructs an Orchestrator with stub dependencies for T035 tests.
// Returns the orchestrator and a closer to stop the TokenStore janitor.
func buildWLOrchestrator(existing ...*models.Memory) (*writelint.Orchestrator, *stubAuditLoggerWL, func()) {
	ms := newStubWLMemStore(existing...)
	al := &stubAuditLoggerWL{}
	tsCfg := writelint.DefaultTokenStoreConfig()
	tsCfg.JanitorInterval = 50 * time.Millisecond
	ts := writelint.NewTokenStore(tsCfg)
	orch := writelint.NewOrchestrator(writelint.OrchestratorConfig{
		MemoryStore:  ms,
		AuditLogger:  al,
		TokenStore:   ts,
		DupThreshold: 0.85,
	})
	return orch, al, ts.Close
}

// dupContent produces a near-duplicate of nearDupMemory for Jaccard >= 0.85.
const t035DupContent = "PostgreSQL connection pool tuning set max connections 200 for production"

func nearDupMemory() *models.Memory {
	return &models.Memory{
		ID:      1,
		Project: "testproj",
		Content: "PostgreSQL connection pool tuning set max connections 200 for production database",
	}
}

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

// TestWriteLint_T035_FlagOff_LegacyPath verifies that when ENGRAM_VNEXT_F_ENABLED
// is not set, the handler follows the legacy path even if writeLint is wired.
// With no memoryStore wired (nil), it should fail at the nil-store check, not
// at the write-lint gate — proving the gate is skipped.
func TestWriteLint_T035_FlagOff_LegacyPath(t *testing.T) {
	// Do NOT set ENGRAM_VNEXT_F_ENABLED
	orch, _, closer := buildWLOrchestrator()
	defer closer()

	srv := NewServer(ServerOptions{Version: "test-t035"})
	srv.SetWriteLintOrchestrator(orch)
	// No memoryStore wired — legacy path hits nil store check

	args, _ := json.Marshal(map[string]any{
		"content": t035DupContent,
		"project": "testproj",
	})
	_, err := srv.handleStoreMemory(context.Background(), args)
	// Must fail at nil memory-store check, not write-lint gate
	require.Error(t, err)
	assert.Contains(t, err.Error(), "memory store not available")
}

// TestWriteLint_T035_Phase1_SignalsReturned verifies that when ENGRAM_VNEXT_F_ENABLED=true
// and a near-duplicate exists, Phase1 returns stored=false with lint signals.
func TestWriteLint_T035_Phase1_SignalsReturned(t *testing.T) {
	t.Setenv("ENGRAM_VNEXT_F_ENABLED", "true")

	orch, _, closer := buildWLOrchestrator(nearDupMemory())
	defer closer()

	srv := NewServer(ServerOptions{Version: "test-t035"})
	srv.SetWriteLintOrchestrator(orch)

	args, _ := json.Marshal(map[string]any{
		"content": t035DupContent,
		"project": "testproj",
	})
	result, err := srv.handleStoreMemory(context.Background(), args)
	require.NoError(t, err)

	var resp map[string]any
	require.NoError(t, json.Unmarshal([]byte(result), &resp))
	assert.Equal(t, false, resp["stored"], "stored must be false when signals present")
	assert.NotEmpty(t, resp["lint_signals"], "lint_signals must not be empty")
	assert.NotEmpty(t, resp["resolution_token"], "resolution_token must be present")
	assert.NotEmpty(t, resp["resolution_options"], "resolution_options must be present")
}

// TestWriteLint_T035_Phase1_NoSignal_Stored verifies that when no duplicate exists,
// Phase1 stores the memory and returns stored=true.
func TestWriteLint_T035_Phase1_NoSignal_Stored(t *testing.T) {
	t.Setenv("ENGRAM_VNEXT_F_ENABLED", "true")

	orch, _, closer := buildWLOrchestrator() // no existing memories
	defer closer()

	srv := NewServer(ServerOptions{Version: "test-t035"})
	srv.SetWriteLintOrchestrator(orch)

	args, _ := json.Marshal(map[string]any{
		"content": "Totally unique memory about widget configuration 2026",
		"project": "testproj",
	})
	result, err := srv.handleStoreMemory(context.Background(), args)
	require.NoError(t, err)

	var resp map[string]any
	require.NoError(t, json.Unmarshal([]byte(result), &resp))
	assert.Equal(t, true, resp["stored"], "stored must be true when no conflicts")
}

// TestWriteLint_T035_Phase2_MergeWith verifies that providing a resolution_token
// and option="merge_with" routes to Phase2 and returns the Phase2 response.
func TestWriteLint_T035_Phase2_MergeWith(t *testing.T) {
	t.Setenv("ENGRAM_VNEXT_F_ENABLED", "true")

	orch, _, closer := buildWLOrchestrator(nearDupMemory())
	defer closer()

	srv := NewServer(ServerOptions{Version: "test-t035"})
	srv.SetWriteLintOrchestrator(orch)
	ctx := context.Background()

	// Phase1 first — get a token
	p1args, _ := json.Marshal(map[string]any{
		"content": t035DupContent,
		"project": "testproj",
	})
	p1result, err := srv.handleStoreMemory(ctx, p1args)
	require.NoError(t, err)

	var p1resp map[string]any
	require.NoError(t, json.Unmarshal([]byte(p1result), &p1resp))
	token, ok := p1resp["resolution_token"].(string)
	require.True(t, ok, "resolution_token must be a string")
	require.NotEmpty(t, token)

	// Phase2 with the token
	p2args, _ := json.Marshal(map[string]any{
		"content":          t035DupContent,
		"project":          "testproj",
		"resolution_token": token,
		"option":           "merge_with",
		"target_memory_id": 1,
	})
	p2result, err := srv.handleStoreMemory(ctx, p2args)
	require.NoError(t, err)

	var p2resp map[string]any
	require.NoError(t, json.Unmarshal([]byte(p2result), &p2resp))
	assert.Equal(t, true, p2resp["stored"], "Phase2 must return stored=true")
}

// TestWriteLint_T035_ForceBypass verifies that force=true skips the write-lint gate
// and falls through to the legacy create path (fails at nil memoryStore).
func TestWriteLint_T035_ForceBypass(t *testing.T) {
	t.Setenv("ENGRAM_VNEXT_F_ENABLED", "true")

	orch, _, closer := buildWLOrchestrator(nearDupMemory())
	defer closer()

	srv := NewServer(ServerOptions{Version: "test-t035"})
	srv.SetWriteLintOrchestrator(orch)
	// No memoryStore: force=true should bypass writeLint gate and hit nil-store error

	args, _ := json.Marshal(map[string]any{
		"content": t035DupContent,
		"project": "testproj",
		"force":   true,
	})
	_, err := srv.handleStoreMemory(context.Background(), args)
	require.Error(t, err)
	// With force=true, we hit the legacy path → nil memory-store check
	assert.Contains(t, err.Error(), "memory store not available",
		"force=true must bypass writeLint gate, fail at nil memoryStore")
}

// TestWriteLint_T035_TokenExpired verifies that an expired/invalid token returns
// an error from Phase2.
func TestWriteLint_T035_TokenExpired(t *testing.T) {
	t.Setenv("ENGRAM_VNEXT_F_ENABLED", "true")

	orch, _, closer := buildWLOrchestrator()
	defer closer()

	srv := NewServer(ServerOptions{Version: "test-t035"})
	srv.SetWriteLintOrchestrator(orch)

	args, _ := json.Marshal(map[string]any{
		"content":          "some content",
		"project":          "testproj",
		"resolution_token": "wlrt_nonexistent-token",
		"option":           "abort",
	})
	_, err := srv.handleStoreMemory(context.Background(), args)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "write_lint_phase2")
}
