// Package session manages the in-memory lifecycle of active engram sessions.
// Each session corresponds to a database row and carries a queue of pending
// SDK messages (observations and summarize requests) that a background
// processor drains and dispatches. The Manager is the single source of truth
// for which sessions are alive at any moment.
package session

import (
	"context"
	"sync"
	"sync/atomic"
	"time"

	"github.com/thebtf/engram/internal/db/gorm"
	"github.com/thebtf/engram/pkg/models"
	"github.com/rs/zerolog/log"
)

// MessageType discriminates between the two kinds of pending messages that
// can be queued against a session.
type MessageType int

const (
	// MessageTypeObservation carries a tool-use observation for the learning
	// memory pipeline.
	MessageTypeObservation MessageType = iota
	// MessageTypeSummarize requests a summarization pass over the last
	// conversation turn.
	MessageTypeSummarize
)

// ObservationData bundles everything the SDK processor needs to generate
// a learning memory entry from a single tool invocation.
type ObservationData struct {
	ToolInput    interface{}
	ToolResponse interface{}
	ToolName     string
	CWD          string
	// UserPrompt is the prompt that preceded this tool call; Learning Memory
	// v3 uses it to link observations to the intent that triggered them.
	UserPrompt   string
	PromptNumber int
}

// SummarizeData carries the last turn's messages so the processor can
// produce a compact factual summary without re-requesting the full history.
type SummarizeData struct {
	LastUserMessage      string
	LastAssistantMessage string
}

// PendingMessage is the tagged-union envelope stored in the session queue.
// Exactly one of Observation or Summarize is non-nil, selected by Type.
type PendingMessage struct {
	Observation *ObservationData
	Summarize   *SummarizeData
	Type        MessageType
}

// ActiveSession is the in-memory representation of a live session.
// It is created by InitializeSession and destroyed by DeleteSession.
// Fields at the top of the struct are read/written by multiple goroutines;
// each has its own synchronization:
//   - pendingMessages is protected by messageMu
//   - generatorActive uses an atomic to avoid lock contention on the hot
//     path where IsAnySessionProcessing polls many sessions concurrently
//   - all other exported fields are set once at initialization and then
//     read-only (no additional synchronization needed)
type ActiveSession struct {
	StartTime              time.Time
	ctx                    context.Context
	cancel                 context.CancelFunc
	notify                 chan struct{}
	Project                string
	UserPrompt             string
	SDKSessionID           string
	ClaudeSessionID        string
	pendingMessages        []PendingMessage
	LastPromptNumber       int
	CumulativeInputTokens  int64
	CumulativeOutputTokens int64
	SessionDBID            int64
	messageMu              sync.Mutex
	generatorActive        atomic.Bool
}

// SessionTimeout is the inactivity window after which a session with no
// pending messages and no active generator is eligible for cleanup.
const SessionTimeout = 30 * time.Minute

// CleanupInterval is how frequently the background goroutine scans for
// sessions that have exceeded SessionTimeout.
const CleanupInterval = 5 * time.Minute

// Manager owns the set of active sessions and drives their lifecycle.
// The mu field protects the sessions map and the callback fields.
// Internal goroutine management uses ctx/cancel; cleanupDone is closed
// by cleanupLoop when the goroutine returns so callers (and tests) can
// observe full shutdown, not just context cancellation.
type Manager struct {
	ctx           context.Context
	sessionStore  *gorm.SessionStore
	sessions      map[int64]*ActiveSession
	onCreated     func(int64)
	onDeleted     func(int64)
	cancel        context.CancelFunc
	ProcessNotify chan struct{}
	// cleanupDone is closed by cleanupLoop when it exits. Tests can wait on
	// this channel to verify the background goroutine has fully stopped, not
	// merely that the context has been cancelled.
	cleanupDone chan struct{}
	mu          sync.RWMutex
}

// NewManager constructs a Manager and starts the background cleanup
// goroutine.  The goroutine stops when the returned manager's context is
// cancelled (via cancel) or ShutdownAll is called.
func NewManager(sessionStore *gorm.SessionStore) *Manager {
	ctx, cancel := context.WithCancel(context.Background())
	m := &Manager{
		sessionStore:  sessionStore,
		sessions:      make(map[int64]*ActiveSession),
		ctx:           ctx,
		cancel:        cancel,
		ProcessNotify: make(chan struct{}, 1),
		cleanupDone:   make(chan struct{}),
	}
	go m.cleanupLoop()
	return m
}

// cleanupLoop is the background goroutine that periodically removes stale
// sessions.  It exits when the manager context is cancelled, and closes
// cleanupDone as its last action so callers can synchronize on it.
func (m *Manager) cleanupLoop() {
	defer close(m.cleanupDone)
	ticker := time.NewTicker(CleanupInterval)
	defer ticker.Stop()

	for {
		select {
		case <-m.ctx.Done():
			return
		case <-ticker.C:
			m.cleanupStaleSessions()
		}
	}
}

// cleanupStaleSessions removes sessions that have been inactive (no pending
// messages, no active generator) for longer than SessionTimeout.
// The scan uses RLock so normal enqueue operations are not blocked; the
// actual deletion acquires a write lock inside DeleteSession.
func (m *Manager) cleanupStaleSessions() {
	m.mu.RLock()
	var stale []int64
	now := time.Now()
	for id, s := range m.sessions {
		if m.isSessionBusy(s) {
			continue
		}
		if now.Sub(s.StartTime) > SessionTimeout {
			stale = append(stale, id)
		}
	}
	m.mu.RUnlock()

	for _, id := range stale {
		log.Info().Int64("sessionId", id).Dur("age", SessionTimeout).Msg("Cleaning up stale session")
		m.DeleteSession(id)
	}
}

// isSessionBusy returns true when a session must not be cleaned up because
// it has work in flight — either pending messages or an active generator.
// Callers must hold at least m.mu.RLock when calling this.
func (m *Manager) isSessionBusy(s *ActiveSession) bool {
	s.messageMu.Lock()
	hasPending := len(s.pendingMessages) > 0
	s.messageMu.Unlock()
	return hasPending || s.generatorActive.Load()
}

// SetOnSessionCreated registers a callback that fires after a new session
// is inserted into the active map.  Called outside the session lock.
func (m *Manager) SetOnSessionCreated(callback func(int64)) {
	m.onCreated = callback
}

// SetOnSessionDeleted registers a callback that fires after a session is
// removed from the active map and its context is cancelled.
func (m *Manager) SetOnSessionDeleted(callback func(int64)) {
	m.onDeleted = callback
}

// InitializeSession ensures sessionDBID is represented in the active map.
// If a session already exists, it updates UserPrompt and LastPromptNumber
// when a non-empty userPrompt is provided, then returns the existing pointer.
// For new sessions it fetches the database record, builds an ActiveSession,
// and inserts it with a double-check to handle concurrent initialization.
// Returns nil, nil when the database record does not exist.
func (m *Manager) InitializeSession(ctx context.Context, sessionDBID int64, userPrompt string, promptNumber int) (*ActiveSession, error) {
	m.mu.Lock()
	if existing, ok := m.sessions[sessionDBID]; ok {
		m.updateExistingSession(existing, userPrompt, promptNumber)
		m.mu.Unlock()
		return existing, nil
	}
	m.mu.Unlock()

	// Fetch from the database without holding the lock so other sessions
	// can be initialized or accessed concurrently.
	dbSession, err := m.sessionStore.GetSessionByID(ctx, sessionDBID)
	if err != nil {
		return nil, err
	}
	if dbSession == nil {
		return nil, nil
	}

	session := m.buildNewSession(ctx, sessionDBID, dbSession, userPrompt, promptNumber)

	// Re-acquire the write lock and do a double-check: another goroutine
	// may have completed initialization of the same ID while we were in the
	// database call.
	m.mu.Lock()
	if existing, ok := m.sessions[sessionDBID]; ok {
		m.mu.Unlock()
		session.cancel() // discard the unused context
		return existing, nil
	}
	m.sessions[sessionDBID] = session
	onCreated := m.onCreated
	m.mu.Unlock()

	log.Info().
		Int64("sessionId", sessionDBID).
		Str("project", session.Project).
		Str("claudeSessionId", session.ClaudeSessionID).
		Msg("Session initialized")

	if onCreated != nil {
		onCreated(sessionDBID)
	}
	return session, nil
}

// updateExistingSession applies a new userPrompt and promptNumber to a
// session that is already in the active map.  A zero/empty prompt is ignored
// so callers that only need to trigger initialization do not clobber the
// previously stored prompt.
// Callers must hold m.mu (write) when calling this.
func (m *Manager) updateExistingSession(s *ActiveSession, userPrompt string, promptNumber int) {
	if userPrompt != "" {
		s.UserPrompt = userPrompt
		s.LastPromptNumber = promptNumber
	}
}

// buildNewSession constructs an ActiveSession from the database record.
// Caller-supplied userPrompt takes priority over the database value so that
// the hook that triggers initialization can inject the current prompt without
// a separate update call.  promptNumber is fetched from the database when
// the caller passes zero.
func (m *Manager) buildNewSession(ctx context.Context, sessionDBID int64, row *models.SDKSession, userPrompt string, promptNumber int) *ActiveSession {
	prompt := userPrompt
	if prompt == "" && row.UserPrompt.Valid {
		prompt = row.UserPrompt.String
	}
	if promptNumber <= 0 {
		promptNumber, _ = m.sessionStore.GetPromptCounter(ctx, sessionDBID)
	}

	sessionCtx, cancel := context.WithCancel(context.Background())
	return &ActiveSession{
		SessionDBID:      sessionDBID,
		ClaudeSessionID:  row.ClaudeSessionID,
		SDKSessionID:     row.SDKSessionID.String,
		Project:          row.Project,
		UserPrompt:       prompt,
		LastPromptNumber: promptNumber,
		StartTime:        time.Now(),
		pendingMessages:  make([]PendingMessage, 0, 32),
		notify:           make(chan struct{}, 1),
		ctx:              sessionCtx,
		cancel:           cancel,
	}
}

// QueueObservation appends a tool-use observation to the session's pending
// queue.  If the session is not yet active it is auto-initialized from the
// database so hook callers do not need to pre-initialize sessions explicitly.
func (m *Manager) QueueObservation(ctx context.Context, sessionDBID int64, data ObservationData) error {
	session, err := m.resolveSession(ctx, sessionDBID)
	if err != nil || session == nil {
		return err
	}

	session.messageMu.Lock()
	session.pendingMessages = append(session.pendingMessages, PendingMessage{
		Type:        MessageTypeObservation,
		Observation: &data,
	})
	depth := len(session.pendingMessages)
	session.messageMu.Unlock()

	m.signalQueued(session)

	log.Info().
		Int64("sessionId", sessionDBID).
		Str("tool", data.ToolName).
		Int("queueDepth", depth).
		Msg("Observation queued")

	return nil
}

// QueueSummarize appends a summarize request to the session's pending queue.
// Like QueueObservation, it auto-initializes the session when needed.
func (m *Manager) QueueSummarize(ctx context.Context, sessionDBID int64, lastUserMessage, lastAssistantMessage string) error {
	session, err := m.resolveSession(ctx, sessionDBID)
	if err != nil || session == nil {
		return err
	}

	session.messageMu.Lock()
	session.pendingMessages = append(session.pendingMessages, PendingMessage{
		Type: MessageTypeSummarize,
		Summarize: &SummarizeData{
			LastUserMessage:      lastUserMessage,
			LastAssistantMessage: lastAssistantMessage,
		},
	})
	depth := len(session.pendingMessages)
	session.messageMu.Unlock()

	m.signalQueued(session)

	log.Info().
		Int64("sessionId", sessionDBID).
		Int("queueDepth", depth).
		Msg("Summarize request queued")

	return nil
}

// resolveSession returns the ActiveSession for the given ID, initializing
// it from the database when it is not yet present in the active map.
// Returns nil, nil when the database row does not exist.
func (m *Manager) resolveSession(ctx context.Context, sessionDBID int64) (*ActiveSession, error) {
	m.mu.Lock()
	s, ok := m.sessions[sessionDBID]
	if ok {
		m.mu.Unlock()
		return s, nil
	}
	m.mu.Unlock()
	return m.InitializeSession(ctx, sessionDBID, "", 0)
}

// signalQueued sends non-blocking notifications to both the session-level
// notify channel (wakes the per-session processor goroutine) and the global
// ProcessNotify channel (wakes the top-level processor).  Non-blocking sends
// are correct here: a buffered-but-full channel means a notification is
// already pending, so no additional signal is needed.
func (m *Manager) signalQueued(session *ActiveSession) {
	select {
	case session.notify <- struct{}{}:
	default:
	}
	select {
	case m.ProcessNotify <- struct{}{}:
	default:
	}
}

// DeleteSession removes the session from the active map, cancels its context
// (which stops any in-progress generator), and fires the onDeleted callback.
// Idempotent: a second call for the same ID is a no-op.
func (m *Manager) DeleteSession(sessionDBID int64) {
	m.mu.Lock()
	s, ok := m.sessions[sessionDBID]
	if !ok {
		m.mu.Unlock()
		return
	}
	delete(m.sessions, sessionDBID)
	m.mu.Unlock()

	s.cancel()

	log.Info().
		Int64("sessionId", sessionDBID).
		Str("project", s.Project).
		Dur("duration", time.Since(s.StartTime)).
		Msg("Session deleted")

	if m.onDeleted != nil {
		m.onDeleted(sessionDBID)
	}
}

// ShutdownAll cancels the cleanup goroutine and deletes every active session.
// Called during graceful shutdown; the ctx parameter is accepted for interface
// compatibility but the Manager uses its own internal context.
func (m *Manager) ShutdownAll(ctx context.Context) {
	m.cancel()

	m.mu.Lock()
	ids := make([]int64, 0, len(m.sessions))
	for id := range m.sessions {
		ids = append(ids, id)
	}
	m.mu.Unlock()

	for _, id := range ids {
		m.DeleteSession(id)
	}

	log.Info().Int("count", len(ids)).Msg("All sessions shut down")
}

// GetActiveSessionCount returns the number of sessions currently in the map.
func (m *Manager) GetActiveSessionCount() int {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return len(m.sessions)
}

// GetTotalQueueDepth returns the sum of pending messages across all active
// sessions.  Used by the dashboard and health checks to gauge backpressure.
func (m *Manager) GetTotalQueueDepth() int {
	m.mu.RLock()
	defer m.mu.RUnlock()
	total := 0
	for _, s := range m.sessions {
		s.messageMu.Lock()
		total += len(s.pendingMessages)
		s.messageMu.Unlock()
	}
	return total
}

// IsAnySessionProcessing returns true when at least one session has pending
// messages or an active generator.  The processor polls this to decide
// whether to sleep or immediately begin another drain pass.
func (m *Manager) IsAnySessionProcessing() bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	for _, s := range m.sessions {
		if m.isSessionBusy(s) {
			return true
		}
	}
	return false
}

// GetAllSessions returns a snapshot slice of all current active session
// pointers.  Callers must not modify the sessions through the returned
// pointers in a way that bypasses the messageMu or the manager's mu.
func (m *Manager) GetAllSessions() []*ActiveSession {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := make([]*ActiveSession, 0, len(m.sessions))
	for _, s := range m.sessions {
		out = append(out, s)
	}
	return out
}

// DrainMessages atomically removes and returns all pending messages for a
// session.  Returns nil when the session is not in the active map.
// The returned slice is a copy; the session's queue is reset to zero length
// (backing array retained for reuse).
func (m *Manager) DrainMessages(sessionDBID int64) []PendingMessage {
	m.mu.RLock()
	s, ok := m.sessions[sessionDBID]
	m.mu.RUnlock()
	if !ok {
		return nil
	}

	s.messageMu.Lock()
	msgs := make([]PendingMessage, len(s.pendingMessages))
	copy(msgs, s.pendingMessages)
	s.pendingMessages = s.pendingMessages[:0]
	s.messageMu.Unlock()

	return msgs
}
