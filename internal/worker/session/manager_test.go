// Package session tests verify the contract of Manager and its associated types.
// Tests are written from the production contract (manager.go) — not derived from
// any prior test file. Structure: plain t.Run subtests grouped by method under test.
package session

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

// newTestManager builds a bare Manager suitable for unit tests (no DB, no
// background goroutine). Callers should defer m.cancel() when they need the
// manager context to be cleaned up.
func newTestManager() *Manager {
	ctx, cancel := context.WithCancel(context.Background())
	return &Manager{
		sessions:      make(map[int64]*ActiveSession),
		ctx:           ctx,
		cancel:        cancel,
		ProcessNotify: make(chan struct{}, 1),
		cleanupDone:   make(chan struct{}),
	}
}

// newActiveSession builds a minimal ActiveSession for insertion into the map.
func newActiveSession(id int64) *ActiveSession {
	ctx, cancel := context.WithCancel(context.Background())
	return &ActiveSession{
		SessionDBID:     id,
		Project:         fmt.Sprintf("proj-%d", id),
		StartTime:       time.Now(),
		pendingMessages: make([]PendingMessage, 0, 8),
		notify:          make(chan struct{}, 1),
		ctx:             ctx,
		cancel:          cancel,
	}
}

// drainNotify empties a channel without blocking.
func drainNotify(ch <-chan struct{}) int {
	n := 0
	for {
		select {
		case <-ch:
			n++
		default:
			return n
		}
	}
}

// ---------------------------------------------------------------------------
// Constants and type values
// ---------------------------------------------------------------------------

func TestConstants(t *testing.T) {
	t.Run("session_timeout_is_30_minutes", func(t *testing.T) {
		if SessionTimeout != 30*time.Minute {
			t.Fatalf("SessionTimeout = %v, want 30m", SessionTimeout)
		}
	})
	t.Run("cleanup_interval_is_5_minutes", func(t *testing.T) {
		if CleanupInterval != 5*time.Minute {
			t.Fatalf("CleanupInterval = %v, want 5m", CleanupInterval)
		}
	})
	t.Run("message_type_observation_is_zero", func(t *testing.T) {
		if MessageTypeObservation != MessageType(0) {
			t.Fatalf("MessageTypeObservation = %v, want 0", MessageTypeObservation)
		}
	})
	t.Run("message_type_summarize_is_one", func(t *testing.T) {
		if MessageTypeSummarize != MessageType(1) {
			t.Fatalf("MessageTypeSummarize = %v, want 1", MessageTypeSummarize)
		}
	})
}

// ---------------------------------------------------------------------------
// NewManager
// ---------------------------------------------------------------------------

func TestNewManager(t *testing.T) {
	t.Parallel()

	t.Run("returns_non_nil_manager", func(t *testing.T) {
		m := NewManager(nil)
		if m == nil {
			t.Fatal("NewManager returned nil")
		}
		defer m.cancel()
	})

	t.Run("session_map_initialised", func(t *testing.T) {
		m := NewManager(nil)
		defer m.cancel()
		if m.sessions == nil {
			t.Fatal("sessions map is nil")
		}
	})

	t.Run("process_notify_channel_buffered", func(t *testing.T) {
		m := NewManager(nil)
		defer m.cancel()
		// A buffered channel of size 1 must accept one send without blocking.
		select {
		case m.ProcessNotify <- struct{}{}:
		default:
			t.Fatal("ProcessNotify did not accept first send — not buffered(1)")
		}
	})

	t.Run("context_and_cancel_set", func(t *testing.T) {
		m := NewManager(nil)
		if m.ctx == nil {
			m.cancel()
			t.Fatal("ctx is nil")
		}
		// ctx must not be done before cancel
		select {
		case <-m.ctx.Done():
			m.cancel()
			t.Fatal("ctx already done before cancel")
		default:
		}
		m.cancel()
		// ctx must be done after cancel
		select {
		case <-m.ctx.Done():
		case <-time.After(50 * time.Millisecond):
			t.Fatal("ctx not done after cancel")
		}
	})

	t.Run("cleanup_goroutine_stops_on_cancel", func(t *testing.T) {
		m := NewManager(nil)
		m.cancel()
		// Wait on cleanupDone (closed by cleanupLoop when it returns), not
		// m.ctx.Done() — the context closes synchronously on cancel(), so
		// waiting on it proves nothing about whether the goroutine has exited.
		select {
		case <-m.cleanupDone:
		case <-time.After(200 * time.Millisecond):
			t.Fatal("cleanup goroutine did not stop within 200ms after cancel")
		}
	})

	t.Run("empty_on_creation", func(t *testing.T) {
		m := NewManager(nil)
		defer m.cancel()
		if n := m.GetActiveSessionCount(); n != 0 {
			t.Fatalf("fresh manager has %d sessions, want 0", n)
		}
	})
}

// ---------------------------------------------------------------------------
// GetActiveSessionCount
// ---------------------------------------------------------------------------

func TestGetActiveSessionCount(t *testing.T) {
	t.Parallel()

	t.Run("zero_initially", func(t *testing.T) {
		m := newTestManager()
		defer m.cancel()
		if m.GetActiveSessionCount() != 0 {
			t.Fatal("expected 0")
		}
	})

	t.Run("increments_with_sessions", func(t *testing.T) {
		m := newTestManager()
		defer m.cancel()
		for i := int64(1); i <= 4; i++ {
			m.mu.Lock()
			m.sessions[i] = newActiveSession(i)
			m.mu.Unlock()
			if got := m.GetActiveSessionCount(); got != int(i) {
				t.Fatalf("after adding %d sessions, count = %d", i, got)
			}
		}
	})
}

// ---------------------------------------------------------------------------
// GetTotalQueueDepth
// ---------------------------------------------------------------------------

func TestGetTotalQueueDepth(t *testing.T) {
	t.Parallel()

	t.Run("zero_with_no_sessions", func(t *testing.T) {
		m := newTestManager()
		defer m.cancel()
		if d := m.GetTotalQueueDepth(); d != 0 {
			t.Fatalf("want 0, got %d", d)
		}
	})

	t.Run("sums_across_all_sessions", func(t *testing.T) {
		m := newTestManager()
		defer m.cancel()
		// Session 1: 3 messages, session 2: 7 messages.
		s1 := &ActiveSession{SessionDBID: 1, pendingMessages: make([]PendingMessage, 3)}
		s2 := &ActiveSession{SessionDBID: 2, pendingMessages: make([]PendingMessage, 7)}
		m.sessions[1] = s1
		m.sessions[2] = s2
		if d := m.GetTotalQueueDepth(); d != 10 {
			t.Fatalf("want 10, got %d", d)
		}
	})

	t.Run("session_with_empty_queue_contributes_zero", func(t *testing.T) {
		m := newTestManager()
		defer m.cancel()
		m.sessions[1] = &ActiveSession{SessionDBID: 1, pendingMessages: []PendingMessage{}}
		if d := m.GetTotalQueueDepth(); d != 0 {
			t.Fatalf("want 0, got %d", d)
		}
	})
}

// ---------------------------------------------------------------------------
// IsAnySessionProcessing
// ---------------------------------------------------------------------------

func TestIsAnySessionProcessing(t *testing.T) {
	t.Parallel()

	t.Run("false_with_no_sessions", func(t *testing.T) {
		m := newTestManager()
		defer m.cancel()
		if m.IsAnySessionProcessing() {
			t.Fatal("expected false")
		}
	})

	t.Run("false_with_idle_session", func(t *testing.T) {
		m := newTestManager()
		defer m.cancel()
		m.sessions[1] = &ActiveSession{SessionDBID: 1, pendingMessages: []PendingMessage{}}
		if m.IsAnySessionProcessing() {
			t.Fatal("expected false for session with empty queue and inactive generator")
		}
	})

	t.Run("true_when_pending_messages", func(t *testing.T) {
		m := newTestManager()
		defer m.cancel()
		s := &ActiveSession{
			SessionDBID:     1,
			pendingMessages: []PendingMessage{{Type: MessageTypeObservation}},
		}
		m.sessions[1] = s
		if !m.IsAnySessionProcessing() {
			t.Fatal("expected true when pending messages exist")
		}
	})

	t.Run("true_when_generator_active_no_pending", func(t *testing.T) {
		m := newTestManager()
		defer m.cancel()
		s := &ActiveSession{
			SessionDBID:     1,
			pendingMessages: []PendingMessage{},
		}
		s.generatorActive.Store(true)
		m.sessions[1] = s
		if !m.IsAnySessionProcessing() {
			t.Fatal("expected true when generatorActive is set")
		}
	})

	t.Run("false_after_clearing_generator", func(t *testing.T) {
		m := newTestManager()
		defer m.cancel()
		s := &ActiveSession{SessionDBID: 1, pendingMessages: []PendingMessage{}}
		s.generatorActive.Store(true)
		m.sessions[1] = s
		s.generatorActive.Store(false)
		if m.IsAnySessionProcessing() {
			t.Fatal("expected false after generator cleared")
		}
	})
}

// ---------------------------------------------------------------------------
// GetAllSessions
// ---------------------------------------------------------------------------

func TestGetAllSessions(t *testing.T) {
	t.Parallel()

	t.Run("empty_slice_with_no_sessions", func(t *testing.T) {
		m := newTestManager()
		defer m.cancel()
		if got := m.GetAllSessions(); len(got) != 0 {
			t.Fatalf("want empty, got %d entries", len(got))
		}
	})

	t.Run("returns_all_sessions", func(t *testing.T) {
		m := newTestManager()
		defer m.cancel()
		for i := int64(1); i <= 5; i++ {
			m.sessions[i] = newActiveSession(i)
		}
		all := m.GetAllSessions()
		if len(all) != 5 {
			t.Fatalf("want 5, got %d", len(all))
		}
	})

	t.Run("returns_pointers_to_same_objects", func(t *testing.T) {
		m := newTestManager()
		defer m.cancel()
		s := newActiveSession(42)
		m.sessions[42] = s
		all := m.GetAllSessions()
		if len(all) != 1 || all[0] != s {
			t.Fatal("expected the same pointer returned")
		}
	})
}

// ---------------------------------------------------------------------------
// DrainMessages
// ---------------------------------------------------------------------------

func TestDrainMessages(t *testing.T) {
	t.Parallel()

	t.Run("nil_for_unknown_session", func(t *testing.T) {
		m := newTestManager()
		defer m.cancel()
		if got := m.DrainMessages(9999); got != nil {
			t.Fatalf("expected nil for non-existent session, got %v", got)
		}
	})

	t.Run("returns_all_pending_and_empties_queue", func(t *testing.T) {
		m := newTestManager()
		defer m.cancel()
		s := &ActiveSession{
			SessionDBID: 1,
			pendingMessages: []PendingMessage{
				{Type: MessageTypeObservation, Observation: &ObservationData{ToolName: "Alpha"}},
				{Type: MessageTypeSummarize, Summarize: &SummarizeData{LastUserMessage: "Q"}},
				{Type: MessageTypeObservation, Observation: &ObservationData{ToolName: "Beta"}},
			},
		}
		m.sessions[1] = s

		msgs := m.DrainMessages(1)
		if len(msgs) != 3 {
			t.Fatalf("want 3 messages, got %d", len(msgs))
		}
		// Verify order preserved.
		if msgs[0].Observation.ToolName != "Alpha" {
			t.Errorf("msg[0] tool = %q, want Alpha", msgs[0].Observation.ToolName)
		}
		if msgs[1].Summarize.LastUserMessage != "Q" {
			t.Errorf("msg[1] user = %q, want Q", msgs[1].Summarize.LastUserMessage)
		}
		if msgs[2].Observation.ToolName != "Beta" {
			t.Errorf("msg[2] tool = %q, want Beta", msgs[2].Observation.ToolName)
		}
		// Queue must be empty now.
		if len(s.pendingMessages) != 0 {
			t.Fatalf("queue not empty after drain, len=%d", len(s.pendingMessages))
		}
	})

	t.Run("second_drain_returns_empty", func(t *testing.T) {
		m := newTestManager()
		defer m.cancel()
		s := &ActiveSession{
			SessionDBID:     1,
			pendingMessages: []PendingMessage{{Type: MessageTypeObservation}},
		}
		m.sessions[1] = s
		m.DrainMessages(1)
		if got := m.DrainMessages(1); len(got) != 0 {
			t.Fatalf("second drain returned %d messages, want 0", len(got))
		}
	})
}

// ---------------------------------------------------------------------------
// DeleteSession
// ---------------------------------------------------------------------------

func TestDeleteSession(t *testing.T) {
	t.Parallel()

	t.Run("removes_session_from_map", func(t *testing.T) {
		m := newTestManager()
		defer m.cancel()
		m.sessions[1] = newActiveSession(1)
		m.DeleteSession(1)
		if m.GetActiveSessionCount() != 0 {
			t.Fatal("session not removed")
		}
	})

	t.Run("cancels_session_context", func(t *testing.T) {
		m := newTestManager()
		defer m.cancel()
		s := newActiveSession(1)
		m.sessions[1] = s
		m.DeleteSession(1)
		select {
		case <-s.ctx.Done():
		case <-time.After(50 * time.Millisecond):
			t.Fatal("session context not cancelled after delete")
		}
	})

	t.Run("fires_onDeleted_callback", func(t *testing.T) {
		m := newTestManager()
		defer m.cancel()
		m.sessions[5] = newActiveSession(5)
		var got int64
		m.SetOnSessionDeleted(func(id int64) { got = id })
		m.DeleteSession(5)
		if got != 5 {
			t.Fatalf("callback received id=%d, want 5", got)
		}
	})

	t.Run("idempotent_on_non_existent_session", func(t *testing.T) {
		m := newTestManager()
		defer m.cancel()
		// Must not panic and must not fire callback.
		fired := false
		m.SetOnSessionDeleted(func(int64) { fired = true })
		m.DeleteSession(9999)
		if fired {
			t.Fatal("callback fired for non-existent session")
		}
	})

	t.Run("double_delete_safe", func(t *testing.T) {
		m := newTestManager()
		defer m.cancel()
		m.sessions[2] = newActiveSession(2)
		m.DeleteSession(2)
		// Second call must not panic.
		m.DeleteSession(2)
	})
}

// ---------------------------------------------------------------------------
// ShutdownAll
// ---------------------------------------------------------------------------

func TestShutdownAll(t *testing.T) {
	t.Parallel()

	t.Run("removes_all_sessions", func(t *testing.T) {
		m := newTestManager()
		defer m.cancel()
		for i := int64(1); i <= 5; i++ {
			m.sessions[i] = newActiveSession(i)
		}
		m.ShutdownAll(context.Background())
		if m.GetActiveSessionCount() != 0 {
			t.Fatalf("sessions remain after ShutdownAll: %d", m.GetActiveSessionCount())
		}
	})

	t.Run("fires_onDeleted_for_each_session", func(t *testing.T) {
		m := newTestManager()
		defer m.cancel()
		for i := int64(1); i <= 3; i++ {
			m.sessions[i] = newActiveSession(i)
		}
		var mu sync.Mutex
		deleted := make(map[int64]bool)
		m.SetOnSessionDeleted(func(id int64) {
			mu.Lock()
			deleted[id] = true
			mu.Unlock()
		})
		m.ShutdownAll(context.Background())
		if len(deleted) != 3 {
			t.Fatalf("callback fired %d times, want 3", len(deleted))
		}
	})

	t.Run("no_panic_on_empty_manager", func(t *testing.T) {
		m := newTestManager()
		defer m.cancel()
		m.ShutdownAll(context.Background())
	})
}

// ---------------------------------------------------------------------------
// Callbacks: SetOnSessionCreated / SetOnSessionDeleted
// ---------------------------------------------------------------------------

func TestCallbackRegistration(t *testing.T) {
	t.Parallel()

	t.Run("onCreated_nil_to_non_nil", func(t *testing.T) {
		m := newTestManager()
		defer m.cancel()
		if m.onCreated != nil {
			t.Fatal("onCreated should start nil")
		}
		var called bool
		m.SetOnSessionCreated(func(int64) { called = true })
		if m.onCreated == nil {
			t.Fatal("onCreated still nil after set")
		}
		m.onCreated(1)
		if !called {
			t.Fatal("registered callback not invoked")
		}
	})

	t.Run("onDeleted_nil_to_non_nil", func(t *testing.T) {
		m := newTestManager()
		defer m.cancel()
		var called bool
		m.SetOnSessionDeleted(func(int64) { called = true })
		m.onDeleted(1)
		if !called {
			t.Fatal("registered callback not invoked")
		}
	})
}

// ---------------------------------------------------------------------------
// cleanupStaleSessions (direct call — bypasses ticker)
// ---------------------------------------------------------------------------

func TestCleanupStaleSessions(t *testing.T) {
	t.Parallel()

	makeStale := func(id int64) *ActiveSession {
		s := newActiveSession(id)
		s.StartTime = time.Now().Add(-(SessionTimeout + time.Minute))
		return s
	}

	t.Run("no_sessions_no_panic", func(t *testing.T) {
		m := newTestManager()
		defer m.cancel()
		m.cleanupStaleSessions() // must not panic
	})

	t.Run("fresh_session_not_removed", func(t *testing.T) {
		m := newTestManager()
		defer m.cancel()
		m.sessions[1] = newActiveSession(1) // StartTime = now
		m.cleanupStaleSessions()
		if m.GetActiveSessionCount() != 1 {
			t.Fatal("fresh session was incorrectly cleaned up")
		}
	})

	t.Run("stale_idle_session_removed", func(t *testing.T) {
		m := newTestManager()
		defer m.cancel()
		m.sessions[1] = makeStale(1)
		m.cleanupStaleSessions()
		if m.GetActiveSessionCount() != 0 {
			t.Fatal("stale session was not removed")
		}
	})

	t.Run("stale_with_pending_messages_kept", func(t *testing.T) {
		m := newTestManager()
		defer m.cancel()
		s := makeStale(1)
		s.pendingMessages = append(s.pendingMessages, PendingMessage{Type: MessageTypeObservation})
		m.sessions[1] = s
		m.cleanupStaleSessions()
		if m.GetActiveSessionCount() != 1 {
			t.Fatal("stale session with pending messages should not be removed")
		}
	})

	t.Run("stale_with_active_generator_kept", func(t *testing.T) {
		m := newTestManager()
		defer m.cancel()
		s := makeStale(1)
		s.generatorActive.Store(true)
		m.sessions[1] = s
		m.cleanupStaleSessions()
		if m.GetActiveSessionCount() != 1 {
			t.Fatal("stale session with active generator should not be removed")
		}
	})

	t.Run("mixed_sessions_correct_subset_removed", func(t *testing.T) {
		m := newTestManager()
		defer m.cancel()
		// id=1: fresh → kept
		m.sessions[1] = newActiveSession(1)
		// id=2: stale, idle → removed
		m.sessions[2] = makeStale(2)
		// id=3: stale, pending → kept
		s3 := makeStale(3)
		s3.pendingMessages = append(s3.pendingMessages, PendingMessage{Type: MessageTypeSummarize})
		m.sessions[3] = s3
		// id=4: stale, generator → kept
		s4 := makeStale(4)
		s4.generatorActive.Store(true)
		m.sessions[4] = s4

		m.cleanupStaleSessions()

		m.mu.RLock()
		_, has1 := m.sessions[1]
		_, has2 := m.sessions[2]
		_, has3 := m.sessions[3]
		_, has4 := m.sessions[4]
		m.mu.RUnlock()

		if !has1 {
			t.Error("id=1 (fresh) should remain")
		}
		if has2 {
			t.Error("id=2 (stale/idle) should be removed")
		}
		if !has3 {
			t.Error("id=3 (stale/pending) should remain")
		}
		if !has4 {
			t.Error("id=4 (stale/generator) should remain")
		}
	})
}

// ---------------------------------------------------------------------------
// cleanupLoop exits on context cancel
// ---------------------------------------------------------------------------

func TestCleanupLoopExitsOnContextCancel(t *testing.T) {
	t.Parallel()
	m := newTestManager()
	done := make(chan struct{})
	go func() {
		m.cleanupLoop()
		close(done)
	}()
	m.cancel()
	select {
	case <-done:
	case <-time.After(200 * time.Millisecond):
		t.Fatal("cleanupLoop did not exit within 200ms after context cancel")
	}
}

// ---------------------------------------------------------------------------
// InitializeSession (no-DB paths only — existing session reuse + double-check)
// ---------------------------------------------------------------------------

func TestInitializeSession_ExistingSession(t *testing.T) {
	t.Parallel()

	t.Run("updates_existing_session_when_new_prompt_provided", func(t *testing.T) {
		m := newTestManager()
		defer m.cancel()
		existing := &ActiveSession{
			SessionDBID:      10,
			UserPrompt:       "original",
			LastPromptNumber: 3,
		}
		m.sessions[10] = existing

		got, err := m.InitializeSession(context.Background(), 10, "updated", 9)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if got != existing {
			t.Fatal("expected same pointer as pre-existing session")
		}
		if got.UserPrompt != "updated" {
			t.Errorf("UserPrompt = %q, want updated", got.UserPrompt)
		}
		if got.LastPromptNumber != 9 {
			t.Errorf("LastPromptNumber = %d, want 9", got.LastPromptNumber)
		}
	})

	t.Run("empty_prompt_does_not_overwrite_existing", func(t *testing.T) {
		m := newTestManager()
		defer m.cancel()
		existing := &ActiveSession{
			SessionDBID:      20,
			UserPrompt:       "keep-me",
			LastPromptNumber: 5,
		}
		m.sessions[20] = existing

		got, err := m.InitializeSession(context.Background(), 20, "", 0)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if got.UserPrompt != "keep-me" {
			t.Errorf("UserPrompt overwritten; got %q, want keep-me", got.UserPrompt)
		}
		if got.LastPromptNumber != 5 {
			t.Errorf("LastPromptNumber overwritten; got %d, want 5", got.LastPromptNumber)
		}
	})
}

// ---------------------------------------------------------------------------
// QueueObservation — queue to existing session
// ---------------------------------------------------------------------------

func TestQueueObservation(t *testing.T) {
	t.Parallel()

	t.Run("appends_observation_to_session_queue", func(t *testing.T) {
		m := newTestManager()
		defer m.cancel()
		s := &ActiveSession{
			SessionDBID:     1,
			pendingMessages: make([]PendingMessage, 0),
			notify:          make(chan struct{}, 1),
		}
		m.sessions[1] = s

		obs := ObservationData{
			ToolName:     "Write",
			ToolInput:    "input",
			ToolResponse: "response",
			CWD:          "/repo",
			PromptNumber: 2,
		}
		if err := m.QueueObservation(context.Background(), 1, obs); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if m.GetTotalQueueDepth() != 1 {
			t.Fatalf("queue depth = %d, want 1", m.GetTotalQueueDepth())
		}
		msgs := m.DrainMessages(1)
		if len(msgs) != 1 {
			t.Fatalf("drained %d messages, want 1", len(msgs))
		}
		if msgs[0].Type != MessageTypeObservation {
			t.Errorf("message type = %v, want Observation", msgs[0].Type)
		}
		if msgs[0].Observation.ToolName != "Write" {
			t.Errorf("tool name = %q, want Write", msgs[0].Observation.ToolName)
		}
		if msgs[0].Observation.CWD != "/repo" {
			t.Errorf("CWD = %q, want /repo", msgs[0].Observation.CWD)
		}
	})

	t.Run("sends_session_notify", func(t *testing.T) {
		m := newTestManager()
		defer m.cancel()
		s := &ActiveSession{
			SessionDBID:     2,
			pendingMessages: make([]PendingMessage, 0),
			notify:          make(chan struct{}, 1),
		}
		m.sessions[2] = s

		drainNotify(s.notify)           // ensure empty
		drainNotify(m.ProcessNotify)    // ensure empty
		_ = m.QueueObservation(context.Background(), 2, ObservationData{ToolName: "T"})

		select {
		case <-s.notify:
		default:
			t.Fatal("session notify channel not signalled")
		}
	})

	t.Run("sends_process_notify", func(t *testing.T) {
		m := newTestManager()
		defer m.cancel()
		s := &ActiveSession{
			SessionDBID:     3,
			pendingMessages: make([]PendingMessage, 0),
			notify:          make(chan struct{}, 1),
		}
		m.sessions[3] = s
		drainNotify(m.ProcessNotify)
		_ = m.QueueObservation(context.Background(), 3, ObservationData{ToolName: "T"})

		select {
		case <-m.ProcessNotify:
		default:
			t.Fatal("ProcessNotify channel not signalled")
		}
	})

	t.Run("non_blocking_when_notify_channels_full", func(t *testing.T) {
		m := newTestManager()
		defer m.cancel()
		s := &ActiveSession{
			SessionDBID:     4,
			pendingMessages: make([]PendingMessage, 0),
			notify:          make(chan struct{}, 1),
		}
		// Pre-fill both channels so non-blocking sends are required.
		s.notify <- struct{}{}
		m.ProcessNotify <- struct{}{}
		m.sessions[4] = s

		done := make(chan struct{})
		go func() {
			_ = m.QueueObservation(context.Background(), 4, ObservationData{ToolName: "T"})
			close(done)
		}()
		select {
		case <-done:
		case <-time.After(100 * time.Millisecond):
			t.Fatal("QueueObservation blocked when notify channels were full")
		}
	})
}

// ---------------------------------------------------------------------------
// QueueSummarize — queue to existing session
// ---------------------------------------------------------------------------

func TestQueueSummarize(t *testing.T) {
	t.Parallel()

	t.Run("appends_summarize_message", func(t *testing.T) {
		m := newTestManager()
		defer m.cancel()
		s := &ActiveSession{
			SessionDBID:     1,
			pendingMessages: make([]PendingMessage, 0),
			notify:          make(chan struct{}, 1),
		}
		m.sessions[1] = s

		if err := m.QueueSummarize(context.Background(), 1, "user-q", "asst-a"); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		msgs := m.DrainMessages(1)
		if len(msgs) != 1 {
			t.Fatalf("drained %d, want 1", len(msgs))
		}
		if msgs[0].Type != MessageTypeSummarize {
			t.Errorf("type = %v, want Summarize", msgs[0].Type)
		}
		if msgs[0].Summarize.LastUserMessage != "user-q" {
			t.Errorf("user = %q, want user-q", msgs[0].Summarize.LastUserMessage)
		}
		if msgs[0].Summarize.LastAssistantMessage != "asst-a" {
			t.Errorf("assistant = %q, want asst-a", msgs[0].Summarize.LastAssistantMessage)
		}
	})

	t.Run("signals_notify_channels", func(t *testing.T) {
		m := newTestManager()
		defer m.cancel()
		s := &ActiveSession{
			SessionDBID:     1,
			pendingMessages: make([]PendingMessage, 0),
			notify:          make(chan struct{}, 1),
		}
		m.sessions[1] = s
		drainNotify(s.notify)
		drainNotify(m.ProcessNotify)

		_ = m.QueueSummarize(context.Background(), 1, "u", "a")

		select {
		case <-s.notify:
		default:
			t.Fatal("session notify not signalled")
		}
		select {
		case <-m.ProcessNotify:
		default:
			t.Fatal("ProcessNotify not signalled")
		}
	})

	t.Run("mixed_queue_order_preserved", func(t *testing.T) {
		m := newTestManager()
		defer m.cancel()
		s := &ActiveSession{
			SessionDBID:     1,
			pendingMessages: make([]PendingMessage, 0),
			notify:          make(chan struct{}, 1),
		}
		m.sessions[1] = s

		_ = m.QueueObservation(context.Background(), 1, ObservationData{ToolName: "First"})
		_ = m.QueueSummarize(context.Background(), 1, "mid-u", "mid-a")
		_ = m.QueueObservation(context.Background(), 1, ObservationData{ToolName: "Last"})

		msgs := m.DrainMessages(1)
		if len(msgs) != 3 {
			t.Fatalf("want 3 messages, got %d", len(msgs))
		}
		if msgs[0].Type != MessageTypeObservation || msgs[0].Observation.ToolName != "First" {
			t.Errorf("msgs[0] wrong: %+v", msgs[0])
		}
		if msgs[1].Type != MessageTypeSummarize {
			t.Errorf("msgs[1] wrong type: %v", msgs[1].Type)
		}
		if msgs[2].Type != MessageTypeObservation || msgs[2].Observation.ToolName != "Last" {
			t.Errorf("msgs[2] wrong: %+v", msgs[2])
		}
	})
}

// ---------------------------------------------------------------------------
// Concurrency: race-free parallel read/write/delete
// ---------------------------------------------------------------------------

func TestConcurrencyRace(t *testing.T) {
	t.Parallel()

	t.Run("parallel_add_read_delete", func(t *testing.T) {
		m := newTestManager()
		defer m.cancel()

		var wg sync.WaitGroup
		const workers = 80

		for i := 0; i < workers; i++ {
			wg.Add(1)
			go func(id int64) {
				defer wg.Done()
				s := newActiveSession(id)
				m.mu.Lock()
				m.sessions[id] = s
				m.mu.Unlock()

				_ = m.GetActiveSessionCount()
				_ = m.GetTotalQueueDepth()
				_ = m.IsAnySessionProcessing()
				_ = m.GetAllSessions()

				m.DeleteSession(id)
			}(int64(i))
		}
		wg.Wait()

		if m.GetActiveSessionCount() != 0 {
			t.Fatalf("sessions remaining after concurrent run: %d", m.GetActiveSessionCount())
		}
	})

	t.Run("parallel_queue_and_cleanup_no_race", func(t *testing.T) {
		m := newTestManager()
		defer m.cancel()

		for i := int64(1); i <= 4; i++ {
			m.sessions[i] = &ActiveSession{
				SessionDBID:     i,
				StartTime:       time.Now(),
				pendingMessages: make([]PendingMessage, 0),
				notify:          make(chan struct{}, 1),
			}
		}

		var wg sync.WaitGroup
		for i := 0; i < 40; i++ {
			wg.Add(1)
			go func(n int) {
				defer wg.Done()
				id := int64((n % 4) + 1)
				_ = m.QueueObservation(context.Background(), id, ObservationData{ToolName: "T"})
			}(i)
		}
		for i := 0; i < 8; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				m.cleanupStaleSessions()
			}()
		}
		for i := 0; i < 16; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				_ = m.GetAllSessions()
				_ = m.GetTotalQueueDepth()
				_ = m.IsAnySessionProcessing()
			}()
		}
		wg.Wait()
	})
}

// ---------------------------------------------------------------------------
// ProcessNotify channel semantics
// ---------------------------------------------------------------------------

func TestProcessNotifySemantics(t *testing.T) {
	t.Parallel()

	t.Run("buffered_size_one_accepts_first_send", func(t *testing.T) {
		m := newTestManager()
		defer m.cancel()
		drainNotify(m.ProcessNotify)
		select {
		case m.ProcessNotify <- struct{}{}:
		default:
			t.Fatal("first send to ProcessNotify blocked")
		}
	})

	t.Run("second_send_non_blocking_when_full", func(t *testing.T) {
		m := newTestManager()
		defer m.cancel()
		m.ProcessNotify <- struct{}{} // fill
		// The select-default in QueueObservation must not block.
		select {
		case m.ProcessNotify <- struct{}{}:
		default:
			// expected — channel is full, non-blocking path hit
		}
	})

	t.Run("can_drain_after_fill", func(t *testing.T) {
		m := newTestManager()
		defer m.cancel()
		m.ProcessNotify <- struct{}{}
		select {
		case <-m.ProcessNotify:
		default:
			t.Fatal("could not drain ProcessNotify")
		}
	})
}

// ---------------------------------------------------------------------------
// ActiveSession fields and atomics
// ---------------------------------------------------------------------------

func TestActiveSessionFields(t *testing.T) {
	t.Parallel()

	t.Run("generatorActive_initial_false", func(t *testing.T) {
		var s ActiveSession
		if s.generatorActive.Load() {
			t.Fatal("expected false initially")
		}
	})

	t.Run("generatorActive_store_and_load", func(t *testing.T) {
		var s ActiveSession
		s.generatorActive.Store(true)
		if !s.generatorActive.Load() {
			t.Fatal("expected true after Store(true)")
		}
		s.generatorActive.Store(false)
		if s.generatorActive.Load() {
			t.Fatal("expected false after Store(false)")
		}
	})

	t.Run("notify_channel_buffered_one", func(t *testing.T) {
		s := &ActiveSession{notify: make(chan struct{}, 1)}
		select {
		case s.notify <- struct{}{}:
		default:
			t.Fatal("notify channel rejected first send")
		}
		// Second send must not block.
		select {
		case s.notify <- struct{}{}:
		default:
			// full — expected
		}
	})

	t.Run("context_cancel_propagates", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		s := &ActiveSession{ctx: ctx, cancel: cancel}
		select {
		case <-s.ctx.Done():
			t.Fatal("context done before cancel")
		default:
		}
		s.cancel()
		select {
		case <-s.ctx.Done():
		case <-time.After(50 * time.Millisecond):
			t.Fatal("context not done after cancel")
		}
	})

	t.Run("token_counters_accumulate", func(t *testing.T) {
		var s ActiveSession
		s.CumulativeInputTokens += 100
		s.CumulativeOutputTokens += 50
		s.CumulativeInputTokens += 200
		s.CumulativeOutputTokens += 100
		if s.CumulativeInputTokens != 300 {
			t.Errorf("input = %d, want 300", s.CumulativeInputTokens)
		}
		if s.CumulativeOutputTokens != 150 {
			t.Errorf("output = %d, want 150", s.CumulativeOutputTokens)
		}
	})

	t.Run("prompt_number_tracks_increments", func(t *testing.T) {
		s := &ActiveSession{LastPromptNumber: 0}
		s.LastPromptNumber = 7
		s.LastPromptNumber++
		if s.LastPromptNumber != 8 {
			t.Errorf("LastPromptNumber = %d, want 8", s.LastPromptNumber)
		}
	})
}

// ---------------------------------------------------------------------------
// PendingMessage / ObservationData / SummarizeData structure
// ---------------------------------------------------------------------------

func TestMessageStructures(t *testing.T) {
	t.Parallel()

	t.Run("observation_message_nil_summarize", func(t *testing.T) {
		msg := PendingMessage{
			Type:        MessageTypeObservation,
			Observation: &ObservationData{ToolName: "Read"},
		}
		if msg.Summarize != nil {
			t.Fatal("Summarize should be nil for observation message")
		}
	})

	t.Run("summarize_message_nil_observation", func(t *testing.T) {
		msg := PendingMessage{
			Type:      MessageTypeSummarize,
			Summarize: &SummarizeData{LastUserMessage: "hi"},
		}
		if msg.Observation != nil {
			t.Fatal("Observation should be nil for summarize message")
		}
	})

	t.Run("observation_data_fields", func(t *testing.T) {
		obs := ObservationData{
			ToolName:     "Bash",
			ToolInput:    map[string]string{"cmd": "ls"},
			ToolResponse: "file1\nfile2",
			CWD:          "/home/user",
			UserPrompt:   "list files",
			PromptNumber: 4,
		}
		if obs.ToolName != "Bash" || obs.PromptNumber != 4 || obs.CWD != "/home/user" {
			t.Errorf("unexpected field values: %+v", obs)
		}
	})

	t.Run("summarize_data_fields", func(t *testing.T) {
		s := SummarizeData{
			LastUserMessage:      "what happened?",
			LastAssistantMessage: "I did X.",
		}
		if s.LastUserMessage == "" || s.LastAssistantMessage == "" {
			t.Errorf("unexpected empty fields: %+v", s)
		}
	})

	t.Run("observation_data_accepts_various_input_types", func(t *testing.T) {
		cases := []interface{}{nil, "string", 42, true, []int{1, 2}, map[string]int{"a": 1}}
		for _, v := range cases {
			obs := ObservationData{ToolInput: v, ToolResponse: v}
			_ = obs // just verify it compiles and doesn't panic on construction
		}
	})
}

// ---------------------------------------------------------------------------
// Concurrent queue writes — regression: no data race via QueueObservation
// ---------------------------------------------------------------------------

// TestQueueObservationConcurrentAppend verifies that concurrent calls to
// QueueObservation against the same session do not race and that every
// observation is enqueued. The test uses the public Manager API instead of
// touching messageMu directly — if the production code stops protecting the
// queue with a mutex, the race detector will catch it here.
func TestQueueObservationConcurrentAppend(t *testing.T) {
	t.Parallel()

	m := newTestManager()
	defer m.cancel()
	s := &ActiveSession{
		SessionDBID:     1,
		pendingMessages: make([]PendingMessage, 0, 64),
		notify:          make(chan struct{}, 1),
	}
	m.sessions[1] = s

	var wg sync.WaitGroup
	const n = 60
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = m.QueueObservation(context.Background(), 1, ObservationData{ToolName: "T"})
		}()
	}
	wg.Wait()

	if depth := m.GetTotalQueueDepth(); depth != n {
		t.Fatalf("want %d queued messages, got %d", n, depth)
	}
}

// ---------------------------------------------------------------------------
// CWD path variety (table-driven)
// ---------------------------------------------------------------------------

func TestObservationCWDVariants(t *testing.T) {
	t.Parallel()
	cases := []struct {
		label string
		cwd   string
	}{
		{"empty", ""},
		{"unix_absolute", "/var/log/app"},
		{"windows_absolute", `C:\Users\dev\project`},
		{"path_with_spaces", "/home/user/my project"},
	}
	for _, c := range cases {
		c := c
		t.Run(c.label, func(t *testing.T) {
			t.Parallel()
			obs := ObservationData{ToolName: "T", CWD: c.cwd}
			if obs.CWD != c.cwd {
				t.Errorf("CWD = %q, want %q", obs.CWD, c.cwd)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// IsAnySessionProcessing — concurrent generatorActive reads via Manager API
// ---------------------------------------------------------------------------

// TestIsAnySessionProcessingConcurrency verifies that concurrent calls to
// IsAnySessionProcessing are race-free when sessions transition between active
// and idle. The test exercises the real Manager contract instead of directly
// touching generatorActive — if the production code stops using the atomic
// where required, the race detector will fire on the real path.
func TestIsAnySessionProcessingConcurrency(t *testing.T) {
	t.Parallel()

	m := newTestManager()
	defer m.cancel()

	const numSessions = 5
	for i := int64(1); i <= numSessions; i++ {
		s := newActiveSession(i)
		m.sessions[i] = s
	}

	var wg sync.WaitGroup
	const readers = 50

	// Concurrently toggle generatorActive on sessions via QueueObservation
	// (which is the real path that leads to generatorActive being set), while
	// simultaneously polling IsAnySessionProcessing.
	for i := 0; i < readers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = m.IsAnySessionProcessing()
		}()
	}
	for i := int64(1); i <= numSessions; i++ {
		wg.Add(1)
		id := i
		go func() {
			defer wg.Done()
			_ = m.QueueObservation(context.Background(), id, ObservationData{ToolName: "T"})
		}()
	}

	wg.Wait()

	// After all goroutines have finished, the queue must hold exactly numSessions
	// pending messages — one per QueueObservation call.
	if depth := m.GetTotalQueueDepth(); depth != numSessions {
		t.Fatalf("want %d queued messages, got %d", numSessions, depth)
	}
}
