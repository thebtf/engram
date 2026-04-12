// Package gorm provides GORM-based database operations for engram.
package gorm

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strconv"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/thebtf/engram/pkg/models"
)

// SessionStore provides session-related database operations using GORM.
type SessionStore struct {
	db *gorm.DB
}

var (
	// ErrSessionNotFound indicates that no matching session row exists for the provided identifier.
	ErrSessionNotFound = errors.New("session not found")
	// ErrSessionOutcomeConflict indicates that a different outcome was already recorded for the session.
	ErrSessionOutcomeConflict = errors.New("session outcome conflict")
)

// NewSessionStore creates a new session store.
func NewSessionStore(store *Store) *SessionStore {
	return &SessionStore{db: store.DB}
}

// CreateSDKSession creates a new SDK session (idempotent - returns existing ID if exists).
// This is the KEY to how engram stays unified across hooks.
func (s *SessionStore) CreateSDKSession(ctx context.Context, claudeSessionID, project, userPrompt string) (int64, error) {
	now := time.Now()

	session := &SDKSession{
		ClaudeSessionID: claudeSessionID,
		SDKSessionID: func() sql.NullString {
			return sql.NullString{String: claudeSessionID, Valid: true}
		}(),
		Project: project,
		UserPrompt: func() sql.NullString {
			if userPrompt != "" {
				return sql.NullString{String: userPrompt, Valid: true}
			}
			return sql.NullString{Valid: false}
		}(),
		Status:         "active",
		StartedAt:      now.Format(time.RFC3339),
		StartedAtEpoch: now.UnixMilli(),
	}

	// CRITICAL: INSERT OR IGNORE makes this idempotent
	// Use OnConflict with DoNothing to achieve INSERT OR IGNORE behavior
	result := s.db.WithContext(ctx).
		Clauses(clause.OnConflict{
			Columns:   []clause.Column{{Name: "claude_session_id"}},
			DoNothing: true,
		}).
		Create(session)

	if result.Error != nil {
		return 0, result.Error
	}

	// Check if insert happened
	if result.RowsAffected == 0 {
		// Session exists - UPDATE project and user_prompt if we have non-empty values
		if project != "" {
			updates := map[string]interface{}{
				"project": project,
			}
			if userPrompt != "" {
				updates["user_prompt"] = userPrompt
			}
			if err := s.db.WithContext(ctx).
				Model(&SDKSession{}).
				Where("claude_session_id = ?", claudeSessionID).
				Updates(updates).Error; err != nil {
				return 0, fmt.Errorf("failed to update session: %w", err)
			}
		}

		// Fetch existing session
		var existing SDKSession
		err := s.db.WithContext(ctx).
			Where("claude_session_id = ?", claudeSessionID).
			First(&existing).Error
		if err != nil {
			return 0, err
		}
		return existing.ID, nil
	}

	return session.ID, nil
}

// GetSessionByID retrieves a session by its database ID.
func (s *SessionStore) GetSessionByID(ctx context.Context, id int64) (*models.SDKSession, error) {
	var sess SDKSession
	err := s.db.WithContext(ctx).First(&sess, id).Error
	if err == gorm.ErrRecordNotFound {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return toModelSDKSession(&sess), nil
}

// FindAnySDKSession finds any session by Claude session ID (any status).
func (s *SessionStore) FindAnySDKSession(ctx context.Context, claudeSessionID string) (*models.SDKSession, error) {
	var sess SDKSession
	err := s.db.WithContext(ctx).
		Where("claude_session_id = ?", claudeSessionID).
		First(&sess).Error
	if err == gorm.ErrRecordNotFound {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return toModelSDKSession(&sess), nil
}

// ResolveClaudeSessionID resolves a session identifier to its canonical Claude session ID.
// Accepts either a Claude session ID or a numeric DB ID string.
func (s *SessionStore) ResolveClaudeSessionID(ctx context.Context, sessionIdentifier string) (string, error) {
	sess, _, err := resolveSessionForOutcome(s.db.WithContext(ctx), sessionIdentifier)
	if err != nil {
		return "", err
	}
	if sess == nil {
		return "", fmt.Errorf("%w: %s", ErrSessionNotFound, sessionIdentifier)
	}
	return sess.ClaudeSessionID, nil
}

// IncrementPromptCounter increments the prompt counter and returns the new value.
// Uses a single SQL query with RETURNING clause for optimal performance.
func (s *SessionStore) IncrementPromptCounter(ctx context.Context, id int64) (int, error) {
	// Use raw SQL with RETURNING to get updated value in single query
	// PostgreSQL supports RETURNING natively
	var newCounter int
	err := s.db.WithContext(ctx).Raw(`
		UPDATE sdk_sessions
		SET prompt_counter = COALESCE(prompt_counter, 0) + 1
		WHERE id = ?
		RETURNING prompt_counter
	`, id).Scan(&newCounter).Error

	if err != nil {
		// Fallback if RETURNING clause fails
		if err.Error() == "near \"RETURNING\": syntax error" || newCounter == 0 {
			// Atomic increment
			updateErr := s.db.WithContext(ctx).
				Model(&SDKSession{}).
				Where("id = ?", id).
				Update("prompt_counter", gorm.Expr("COALESCE(prompt_counter, 0) + 1")).Error
			if updateErr != nil {
				return 0, updateErr
			}

			// Fetch updated value
			var sess SDKSession
			fetchErr := s.db.WithContext(ctx).
				Select("prompt_counter").
				First(&sess, id).Error
			if fetchErr != nil {
				return 0, fetchErr
			}
			return sess.PromptCounter, nil
		}
		return 0, err
	}

	return newCounter, nil
}

// GetPromptCounter returns the current prompt counter for a session.
func (s *SessionStore) GetPromptCounter(ctx context.Context, id int64) (int, error) {
	var sess SDKSession
	err := s.db.WithContext(ctx).
		Select("prompt_counter").
		First(&sess, id).Error
	if err != nil {
		return 0, err
	}
	return sess.PromptCounter, nil
}

// GetSessionsToday returns the count of sessions started today.
func (s *SessionStore) GetSessionsToday(ctx context.Context) (int, error) {
	// Get start of today in milliseconds
	now := time.Now()
	startOfDay := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
	startEpoch := startOfDay.UnixMilli()

	var count int64
	err := s.db.WithContext(ctx).
		Model(&SDKSession{}).
		Where("started_at_epoch >= ?", startEpoch).
		Count(&count).Error

	return int(count), err
}

// GetAllProjects returns all unique project names.
func (s *SessionStore) GetAllProjects(ctx context.Context) ([]string, error) {
	var projects []string
	err := s.db.WithContext(ctx).
		Model(&SDKSession{}).
		Distinct("project").
		Where("project IS NOT NULL AND project != ''").
		Order("project ASC").
		Pluck("project", &projects).Error

	return projects, err
}

// ListSDKSessions returns a paginated list of SDK sessions, optionally filtered by project.
// Results are ordered by started_at DESC (newest first). Returns sessions and total count.
func (s *SessionStore) ListSDKSessions(ctx context.Context, project string, limit, offset, minPrompts int, from, to int64) ([]*models.SDKSession, int64, error) {
	var sessions []SDKSession
	var total int64

	q := s.db.WithContext(ctx).Model(&SDKSession{})
	if project != "" {
		q = q.Where("project = ?", project)
	}
	if minPrompts > 0 {
		q = q.Where("prompt_counter >= ?", minPrompts)
	}
	if from > 0 {
		q = q.Where("started_at_epoch >= ?", from)
	}
	if to > 0 {
		q = q.Where("started_at_epoch <= ?", to)
	}

	if err := q.Count(&total).Error; err != nil {
		return nil, 0, err
	}

	if err := q.Order("started_at_epoch DESC, id DESC").
		Limit(limit).
		Offset(offset).
		Find(&sessions).Error; err != nil {
		return nil, 0, err
	}

	result := make([]*models.SDKSession, len(sessions))
	for i := range sessions {
		result[i] = toModelSDKSession(&sessions[i])
	}
	return result, total, nil
}

// UpdateSessionOutcome records the outcome of a session identified by Claude session ID or numeric DB ID.
// If a Claude session row does not exist yet, it is auto-created with empty project/user prompt before recording outcome.
func (s *SessionStore) UpdateSessionOutcome(ctx context.Context, sessionIdentifier, outcome, reason string) error {
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		sess, isNumericIDInput, err := resolveSessionForOutcome(tx, sessionIdentifier)
		if err != nil {
			return err
		}

		if sess == nil {
			if isNumericIDInput {
				return fmt.Errorf("%w: %s", ErrSessionNotFound, sessionIdentifier)
			}

			now := time.Now()
			sess = &SDKSession{
				ClaudeSessionID: sessionIdentifier,
				SDKSessionID: sql.NullString{
					String: sessionIdentifier,
					Valid:  true,
				},
				Project:        "",
				UserPrompt:     sql.NullString{Valid: false},
				Status:         "active",
				StartedAt:      now.Format(time.RFC3339),
				StartedAtEpoch: now.UnixMilli(),
			}
			createResult := tx.Clauses(clause.OnConflict{
				Columns:   []clause.Column{{Name: "claude_session_id"}},
				DoNothing: true,
			}).Create(sess)
			if createResult.Error != nil {
				return createResult.Error
			}
			if createResult.RowsAffected == 0 {
				var existing SDKSession
				err := tx.WithContext(ctx).
					Where("claude_session_id = ?", sessionIdentifier).
					First(&existing).Error
				if err != nil {
					if errors.Is(err, gorm.ErrRecordNotFound) {
						return fmt.Errorf("%w: %s", ErrSessionNotFound, sessionIdentifier)
					}
					return err
				}
				sess = &existing
			}
		}

		existingOutcome := ""
		if sess.Outcome.Valid {
			existingOutcome = sess.Outcome.String
		}
		if existingOutcome != "" {
			if existingOutcome == outcome {
				return nil // idempotent repeated write
			}
			return fmt.Errorf("%w: session=%s existing=%s requested=%s", ErrSessionOutcomeConflict, sess.ClaudeSessionID, existingOutcome, outcome)
		}

		result := tx.Model(&SDKSession{}).
			Where("id = ? AND (outcome IS NULL OR outcome = '')", sess.ID).
			Updates(map[string]interface{}{
				"outcome":             outcome,
				"outcome_reason":      reason,
				"outcome_recorded_at": time.Now(),
			})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected > 0 {
			return nil
		}

		// Concurrent writer may have set the outcome between our read and update.
		var latest SDKSession
		err = tx.WithContext(ctx).
			Select("id", "claude_session_id", "outcome").
			Where("id = ?", sess.ID).
			First(&latest).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return fmt.Errorf("%w: %s", ErrSessionNotFound, sessionIdentifier)
		}
		if err != nil {
			return err
		}
		if latest.Outcome.Valid && latest.Outcome.String != "" {
			if latest.Outcome.String == outcome {
				return nil
			}
			return fmt.Errorf("%w: session=%s existing=%s requested=%s", ErrSessionOutcomeConflict, latest.ClaudeSessionID, latest.Outcome.String, outcome)
		}

		return fmt.Errorf("%w: %s", ErrSessionNotFound, sessionIdentifier)
	})
}

func resolveSessionForOutcome(tx *gorm.DB, sessionIdentifier string) (*SDKSession, bool, error) {
	isNumericIDInput := false
	if numericID, err := strconv.ParseInt(sessionIdentifier, 10, 64); err == nil && numericID > 0 {
		isNumericIDInput = true
		var byID SDKSession
		err = tx.Where("id = ?", numericID).First(&byID).Error
		if err == nil {
			return &byID, true, nil
		}
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, true, err
		}
	}

	var byClaudeID SDKSession
	err := tx.Where("claude_session_id = ?", sessionIdentifier).First(&byClaudeID).Error
	if err == nil {
		return &byClaudeID, isNumericIDInput, nil
	}
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, isNumericIDInput, nil
	}
	return nil, isNumericIDInput, err
}

// UpdateUtilityPropagatedAt records when utility propagation was last triggered for a session.
func (s *SessionStore) UpdateUtilityPropagatedAt(ctx context.Context, claudeSessionID string) error {
	result := s.db.WithContext(ctx).
		Model(&SDKSession{}).
		Where("claude_session_id = ?", claudeSessionID).
		Update("utility_propagated_at", time.Now().UTC())
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 {
		return fmt.Errorf("session not found: %s", claudeSessionID)
	}
	return nil
}

// UpdateUtilityPropagatedAtIfStale atomically claims the propagation slot for a session.
// Returns (true, nil) if the claim succeeded (session was not propagated within the last minute),
// or (false, nil) if the session is rate-limited (propagated within the last minute).
// This is the TOCTOU-free replacement for the read-then-write pattern.
func (s *SessionStore) UpdateUtilityPropagatedAtIfStale(ctx context.Context, claudeSessionID string) (bool, error) {
	result := s.db.WithContext(ctx).Exec(`
		UPDATE sdk_sessions
		SET utility_propagated_at = NOW()
		WHERE claude_session_id = ?
		  AND (utility_propagated_at IS NULL OR utility_propagated_at < NOW() - INTERVAL '1 minute')
	`, claudeSessionID)
	if result.Error != nil {
		return false, result.Error
	}
	return result.RowsAffected > 0, nil
}

// ClearUtilityPropagatedAt resets the propagation timestamp to NULL for a session.
// Called when a background propagation goroutine fails, to allow the next caller to retry.
func (s *SessionStore) ClearUtilityPropagatedAt(ctx context.Context, claudeSessionID string) error {
	return s.db.WithContext(ctx).Exec(`
		UPDATE sdk_sessions SET utility_propagated_at = NULL WHERE claude_session_id = ?
	`, claudeSessionID).Error
}

// StrategyStatRow holds aggregated stats for a single injection strategy.
type StrategyStatRow struct {
	Strategy  string
	Sessions  int64
	Successes int64
}

// GetStrategyStats returns per-strategy session and success counts from sdk_sessions.
// Only strategies with at least one session are included.
func (s *SessionStore) GetStrategyStats(ctx context.Context) ([]StrategyStatRow, error) {
	type rawRow struct {
		InjectionStrategy string
		Sessions          int64
		Successes         int64
	}
	var raw []rawRow
	err := s.db.WithContext(ctx).
		Model(&SDKSession{}).
		Select("injection_strategy, COUNT(*) AS sessions, COUNT(CASE WHEN outcome = 'success' THEN 1 END) AS successes").
		Where("injection_strategy IS NOT NULL AND injection_strategy != ''").
		Group("injection_strategy").
		Scan(&raw).Error
	if err != nil {
		return nil, err
	}
	out := make([]StrategyStatRow, len(raw))
	for i, r := range raw {
		out[i] = StrategyStatRow{
			Strategy:  r.InjectionStrategy,
			Sessions:  r.Sessions,
			Successes: r.Successes,
		}
	}
	return out, nil
}

// LearningCurveRow holds daily session outcome counts for the learning curve endpoint.
type LearningCurveRow struct {
	Date        string
	Sessions    int64
	Successes   int64
	OutcomeRate float64
}

// GetLearningCurve returns daily session outcome rates for the past N days.
// Optional project filter limits results to sessions matching the project field.
func (s *SessionStore) GetLearningCurve(ctx context.Context, days int, project string) ([]LearningCurveRow, error) {
	type rawRow struct {
		Date      string
		Sessions  int64
		Successes int64
	}
	if days <= 0 {
		days = 30
	}

	// Use fmt.Sprintf for the interval expression: days is a validated integer (> 0), safe to embed directly.
	intervalExpr := fmt.Sprintf("'%d days'::interval", days)
	q := s.db.WithContext(ctx).
		Model(&SDKSession{}).
		Select("DATE(outcome_recorded_at) AS date, COUNT(*) AS sessions, COUNT(CASE WHEN outcome = 'success' THEN 1 END) AS successes").
		Where("outcome IS NOT NULL AND outcome_recorded_at >= NOW() - " + intervalExpr).
		Group("DATE(outcome_recorded_at)").
		Order("date ASC")

	if project != "" {
		q = q.Where("project = ?", project)
	}

	var raw []rawRow
	if err := q.Scan(&raw).Error; err != nil {
		return nil, err
	}

	out := make([]LearningCurveRow, len(raw))
	for i, r := range raw {
		var rate float64
		if r.Sessions > 0 {
			rate = float64(r.Successes) / float64(r.Sessions)
		}
		out[i] = LearningCurveRow{
			Date:        r.Date,
			Sessions:    r.Sessions,
			Successes:   r.Successes,
			OutcomeRate: rate,
		}
	}
	return out, nil
}

// PendingOutcomeSession holds the session ID and project for outcome recording.
type PendingOutcomeSession struct {
	ClaudeSessionID string
	Project         string
}

// GetSessionsWithPendingOutcome returns sessions that have injection records but no outcome yet,
// where the most recent injection is older than 10 minutes (to avoid processing active sessions).
func (s *SessionStore) GetSessionsWithPendingOutcome(ctx context.Context) ([]PendingOutcomeSession, error) {
	var rows []struct {
		ClaudeSessionID string `gorm:"column:claude_session_id"`
		Project         string `gorm:"column:project"`
	}

	err := s.db.WithContext(ctx).Raw(`
		SELECT s.claude_session_id, s.project
		FROM sdk_sessions s
		WHERE s.outcome IS NULL
		AND EXISTS (
			SELECT 1 FROM observation_injections oi
			WHERE oi.session_id = s.claude_session_id
		)
		AND NOT EXISTS (
			SELECT 1 FROM observation_injections oi
			WHERE oi.session_id = s.claude_session_id
			AND oi.injected_at > NOW() - INTERVAL '10 minutes'
		)
		AND (s.utility_propagated_at IS NULL OR s.utility_propagated_at < NOW() - INTERVAL '2 hours')
	`).Scan(&rows).Error
	if err != nil {
		return nil, err
	}

	result := make([]PendingOutcomeSession, len(rows))
	for i, r := range rows {
		result[i] = PendingOutcomeSession{
			ClaudeSessionID: r.ClaudeSessionID,
			Project:         r.Project,
		}
	}
	return result, nil
}

// UpdateInjectionStrategy records the injection strategy used for a session.
// Identified by the Claude session ID. Errors are silently dropped by callers (fire-and-forget).
func (s *SessionStore) UpdateInjectionStrategy(ctx context.Context, claudeSessionID, strategy string) error {
	result := s.db.WithContext(ctx).
		Model(&SDKSession{}).
		Where("claude_session_id = ?", claudeSessionID).
		Update("injection_strategy", strategy)
	return result.Error
}

// toModelSDKSession converts a GORM SDKSession to pkg/models.SDKSession.
func toModelSDKSession(sess *SDKSession) *models.SDKSession {
	return &models.SDKSession{
		ID:                  sess.ID,
		ClaudeSessionID:     sess.ClaudeSessionID,
		SDKSessionID:        sess.SDKSessionID,
		Project:             sess.Project,
		UserPrompt:          sess.UserPrompt,
		WorkerPort:          sess.WorkerPort,
		PromptCounter:       int64(sess.PromptCounter),
		Status:              models.SessionStatus(sess.Status),
		StartedAt:           sess.StartedAt,
		StartedAtEpoch:      sess.StartedAtEpoch,
		CompletedAt:         sess.CompletedAt,
		CompletedAtEpoch:    sess.CompletedAtEpoch,
		Outcome:             sess.Outcome,
		OutcomeReason:       sess.OutcomeReason,
		OutcomeRecordedAt:   sess.OutcomeRecordedAt,
		UtilityPropagatedAt: sess.UtilityPropagatedAt,
		InjectionStrategy:   sess.InjectionStrategy,
	}
}
