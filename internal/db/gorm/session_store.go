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

// SessionStore implements session-related database operations backed by GORM.
type SessionStore struct {
	db *gorm.DB
}

var (
	// ErrSessionNotFound is returned when no session row matches the given identifier.
	ErrSessionNotFound = errors.New("session not found")
	// ErrSessionOutcomeConflict is returned when a conflicting outcome is already recorded.
	ErrSessionOutcomeConflict = errors.New("session outcome conflict")
)

// NewSessionStore wraps a Store's database connection for session operations.
func NewSessionStore(store *Store) *SessionStore {
	return &SessionStore{db: store.DB}
}

// CreateSDKSession upserts an SDK session row and returns the row's numeric ID.
// INSERT OR IGNORE (via OnConflict DoNothing) makes the call idempotent — when
// the session already exists, project/user_prompt are refreshed if non-empty,
// and the existing ID is returned. This is the KEY to how engram stays unified
// across hooks: multiple hook firings with the same claude_session_id converge
// on a single row.
func (s *SessionStore) CreateSDKSession(ctx context.Context, claudeSessionID, project, userPrompt string) (int64, error) {
	now := time.Now()

	row := &SDKSession{
		ClaudeSessionID: claudeSessionID,
		SDKSessionID:    sql.NullString{String: claudeSessionID, Valid: true},
		Project:         project,
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

	result := s.db.WithContext(ctx).
		Clauses(clause.OnConflict{
			Columns:   []clause.Column{{Name: "claude_session_id"}},
			DoNothing: true,
		}).
		Create(row)

	if result.Error != nil {
		return 0, result.Error
	}

	if result.RowsAffected == 0 {
		// Row already existed — refresh project/prompt when the caller supplies them.
		if project != "" {
			patch := map[string]interface{}{"project": project}
			if userPrompt != "" {
				patch["user_prompt"] = userPrompt
			}
			if err := s.db.WithContext(ctx).
				Model(&SDKSession{}).
				Where("claude_session_id = ?", claudeSessionID).
				Updates(patch).Error; err != nil {
				return 0, fmt.Errorf("failed to update session: %w", err)
			}
		}

		var existing SDKSession
		if err := s.db.WithContext(ctx).
			Where("claude_session_id = ?", claudeSessionID).
			First(&existing).Error; err != nil {
			return 0, err
		}
		return existing.ID, nil
	}

	return row.ID, nil
}

// GetSessionByID loads a session row by its primary key.
// Returns (nil, nil) when no row with that ID exists.
func (s *SessionStore) GetSessionByID(ctx context.Context, id int64) (*models.SDKSession, error) {
	var row SDKSession
	if err := s.db.WithContext(ctx).First(&row, id).Error; err != nil {
		if err == gorm.ErrRecordNotFound {
			return nil, nil
		}
		return nil, err
	}
	return toModelSDKSession(&row), nil
}

// FindAnySDKSession looks up a session by its Claude-issued session ID.
// Any status is considered; returns (nil, nil) when no matching row exists.
func (s *SessionStore) FindAnySDKSession(ctx context.Context, claudeSessionID string) (*models.SDKSession, error) {
	var row SDKSession
	if err := s.db.WithContext(ctx).
		Where("claude_session_id = ?", claudeSessionID).
		First(&row).Error; err != nil {
		if err == gorm.ErrRecordNotFound {
			return nil, nil
		}
		return nil, err
	}
	return toModelSDKSession(&row), nil
}

// ResolveClaudeSessionID converts a session identifier to its canonical Claude session ID.
// The identifier may be either a Claude session ID string or a decimal DB primary-key string.
func (s *SessionStore) ResolveClaudeSessionID(ctx context.Context, sessionIdentifier string) (string, error) {
	row, _, err := resolveSessionForOutcome(s.db.WithContext(ctx), sessionIdentifier)
	if err != nil {
		return "", err
	}
	if row == nil {
		return "", fmt.Errorf("%w: %s", ErrSessionNotFound, sessionIdentifier)
	}
	return row.ClaudeSessionID, nil
}

// IncrementPromptCounter atomically increments the session's prompt counter
// and returns the updated value. Uses a single UPDATE … RETURNING query on
// PostgreSQL for minimal round-trips; falls back to a two-query increment +
// fetch on databases that do not support RETURNING.
func (s *SessionStore) IncrementPromptCounter(ctx context.Context, id int64) (int, error) {
	var updated int
	err := s.db.WithContext(ctx).Raw(`
		UPDATE sdk_sessions
		SET prompt_counter = COALESCE(prompt_counter, 0) + 1
		WHERE id = ?
		RETURNING prompt_counter
	`, id).Scan(&updated).Error

	if err != nil {
		// Fall back when the driver does not support RETURNING (e.g. SQLite).
		if err.Error() == "near \"RETURNING\": syntax error" || updated == 0 {
			if uerr := s.db.WithContext(ctx).
				Model(&SDKSession{}).
				Where("id = ?", id).
				Update("prompt_counter", gorm.Expr("COALESCE(prompt_counter, 0) + 1")).Error; uerr != nil {
				return 0, uerr
			}

			var row SDKSession
			if ferr := s.db.WithContext(ctx).
				Select("prompt_counter").
				First(&row, id).Error; ferr != nil {
				return 0, ferr
			}
			return row.PromptCounter, nil
		}
		return 0, err
	}

	return updated, nil
}

// GetPromptCounter returns the current prompt counter value for the given session ID.
func (s *SessionStore) GetPromptCounter(ctx context.Context, id int64) (int, error) {
	var row SDKSession
	if err := s.db.WithContext(ctx).
		Select("prompt_counter").
		First(&row, id).Error; err != nil {
		return 0, err
	}
	return row.PromptCounter, nil
}

// GetSessionsToday counts sessions whose started_at_epoch falls on or after
// midnight local time (the start of the current calendar day).
func (s *SessionStore) GetSessionsToday(ctx context.Context) (int, error) {
	now := time.Now()
	midnight := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())

	var n int64
	if err := s.db.WithContext(ctx).
		Model(&SDKSession{}).
		Where("started_at_epoch >= ?", midnight.UnixMilli()).
		Count(&n).Error; err != nil {
		return 0, err
	}
	return int(n), nil
}

// GetAllProjects returns the sorted, deduplicated list of non-empty project names
// that appear across all sdk_sessions rows.
func (s *SessionStore) GetAllProjects(ctx context.Context) ([]string, error) {
	var names []string
	if err := s.db.WithContext(ctx).
		Model(&SDKSession{}).
		Distinct("project").
		Where("project IS NOT NULL AND project != ''").
		Order("project ASC").
		Pluck("project", &names).Error; err != nil {
		return nil, err
	}
	return names, nil
}

// ListSDKSessions returns a paginated page of sessions with the total matching count.
// Filters are additive: project, minPrompts, from/to epoch millisecond range.
// Results are ordered newest-first (started_at_epoch DESC, id DESC).
func (s *SessionStore) ListSDKSessions(ctx context.Context, project string, limit, offset, minPrompts int, from, to int64) ([]*models.SDKSession, int64, error) {
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

	var total int64
	if err := q.Count(&total).Error; err != nil {
		return nil, 0, err
	}

	var rows []SDKSession
	if err := q.Order("started_at_epoch DESC, id DESC").
		Limit(limit).
		Offset(offset).
		Find(&rows).Error; err != nil {
		return nil, 0, err
	}

	out := make([]*models.SDKSession, len(rows))
	for i := range rows {
		out[i] = toModelSDKSession(&rows[i])
	}
	return out, total, nil
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

// GetOutcome returns the recorded outcome for a session identified by Claude session ID.
// Returns empty string when no outcome has been recorded yet.
func (s *SessionStore) GetOutcome(ctx context.Context, sessionID string) (string, error) {
	if sessionID == "" {
		return "", nil
	}
	var sess SDKSession
	err := s.db.WithContext(ctx).
		Select("outcome").
		Where("claude_session_id = ?", sessionID).
		First(&sess).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return "", nil
		}
		return "", err
	}
	if sess.Outcome.Valid {
		return sess.Outcome.String, nil
	}
	return "", nil
}

// resolveSessionForOutcome looks up a session by either a numeric DB-ID string or
// a Claude session-ID string. Returns the matching row (nil when not found),
// a flag indicating whether the input was a numeric ID, and any store error.
func resolveSessionForOutcome(tx *gorm.DB, sessionIdentifier string) (*SDKSession, bool, error) {
	numericInput := false

	if numericID, err := strconv.ParseInt(sessionIdentifier, 10, 64); err == nil && numericID > 0 {
		numericInput = true
		var byPK SDKSession
		if err = tx.Where("id = ?", numericID).First(&byPK).Error; err == nil {
			return &byPK, true, nil
		}
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, true, err
		}
	}

	var byClaudeID SDKSession
	if err := tx.Where("claude_session_id = ?", sessionIdentifier).First(&byClaudeID).Error; err == nil {
		return &byClaudeID, numericInput, nil
	} else if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, numericInput, nil
	} else {
		return nil, numericInput, err
	}
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

// toModelSDKSession projects a GORM SDKSession row onto the pkg/models.SDKSession DTO.
func toModelSDKSession(row *SDKSession) *models.SDKSession {
	return &models.SDKSession{
		ID:                  row.ID,
		ClaudeSessionID:     row.ClaudeSessionID,
		SDKSessionID:        row.SDKSessionID,
		Project:             row.Project,
		UserPrompt:          row.UserPrompt,
		WorkerPort:          row.WorkerPort,
		PromptCounter:       int64(row.PromptCounter),
		Status:              models.SessionStatus(row.Status),
		StartedAt:           row.StartedAt,
		StartedAtEpoch:      row.StartedAtEpoch,
		CompletedAt:         row.CompletedAt,
		CompletedAtEpoch:    row.CompletedAtEpoch,
		Outcome:             row.Outcome,
		OutcomeReason:       row.OutcomeReason,
		OutcomeRecordedAt:   row.OutcomeRecordedAt,
		UtilityPropagatedAt: row.UtilityPropagatedAt,
		InjectionStrategy:   row.InjectionStrategy,
	}
}
