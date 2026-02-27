package sessions

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	gormdb "github.com/thebtf/claude-mnemonic-plus/internal/db/gorm"
	"gorm.io/gorm"
)

type Store struct {
	db    *gorm.DB
	rawDB *sql.DB
}

type ListOptions struct {
	WorkstationID string
	ProjectID     string
	Limit         int
	Offset        int
}

type SessionSearchResult struct {
	Session gormdb.IndexedSession
	Rank    float64
}

func NewStore(store *gormdb.Store) *Store {
	return &Store{db: store.GetDB(), rawDB: store.GetRawDB()}
}

// UpsertSession upserts via raw SQL ON CONFLICT(id) DO UPDATE.
func (s *Store) UpsertSession(ctx context.Context, session *gormdb.IndexedSession) error {
	if session == nil {
		return fmt.Errorf("upsert session: nil session")
	}

	query := `
		INSERT INTO indexed_sessions (
			id, workstation_id, project_id, project_path, git_branch,
			first_msg_at, last_msg_at, exchange_count, tool_counts, content, file_mtime, indexed_at
		)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,NOW())
		ON CONFLICT (id) DO UPDATE SET
			workstation_id = EXCLUDED.workstation_id,
			project_id = EXCLUDED.project_id,
			project_path = EXCLUDED.project_path,
			git_branch = EXCLUDED.git_branch,
			first_msg_at = EXCLUDED.first_msg_at,
			last_msg_at = EXCLUDED.last_msg_at,
			exchange_count = EXCLUDED.exchange_count,
			tool_counts = EXCLUDED.tool_counts,
			content = EXCLUDED.content,
			file_mtime = EXCLUDED.file_mtime,
			indexed_at = NOW()
	`

	_, err := s.rawDB.ExecContext(
		ctx,
		query,
		session.ID,
		session.WorkstationID,
		session.ProjectID,
		session.ProjectPath,
		session.GitBranch,
		session.FirstMsgAt,
		session.LastMsgAt,
		session.ExchangeCount,
		session.ToolCounts,
		session.Content,
		session.FileMtime,
	)
	if err != nil {
		return fmt.Errorf("upsert session: %w", err)
	}

	return nil
}

// GetSession retrieves by ID.
func (s *Store) GetSession(ctx context.Context, id string) (*gormdb.IndexedSession, error) {
	var session gormdb.IndexedSession

	err := s.db.WithContext(ctx).Where("id = ?", id).First(&session).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get session: %w", err)
	}

	return &session, nil
}

// ListSessions lists with optional WorkstationID/ProjectID filters + Limit/Offset.
func (s *Store) ListSessions(ctx context.Context, opts ListOptions) ([]gormdb.IndexedSession, error) {
	query := s.db.WithContext(ctx).Model(&gormdb.IndexedSession{})

	if opts.WorkstationID != "" {
		query = query.Where("workstation_id = ?", opts.WorkstationID)
	}
	if opts.ProjectID != "" {
		query = query.Where("project_id = ?", opts.ProjectID)
	}
	if opts.Limit > 0 {
		query = query.Limit(opts.Limit)
	}
	if opts.Offset > 0 {
		query = query.Offset(opts.Offset)
	}
	query = query.Order("COALESCE(last_msg_at, first_msg_at) DESC, id ASC")

	var sessions []gormdb.IndexedSession
	if err := query.Find(&sessions).Error; err != nil {
		return nil, fmt.Errorf("list sessions: %w", err)
	}

	return sessions, nil
}

// SearchSessions FTS via ts_rank + websearch_to_tsquery.
func (s *Store) SearchSessions(ctx context.Context, query string, limit int) ([]SessionSearchResult, error) {
	if limit <= 0 {
		limit = 10
	}

	sqlQuery := `
		SELECT id, workstation_id, project_id, project_path, git_branch, first_msg_at, last_msg_at, exchange_count, tool_counts, topics, content, file_mtime, indexed_at, ts_rank(tsv, websearch_to_tsquery('english', $1)) AS rank
		FROM indexed_sessions
		WHERE tsv @@ websearch_to_tsquery('english', $1)
		ORDER BY rank DESC
		LIMIT $2
	`

	rows, err := s.rawDB.QueryContext(ctx, sqlQuery, query, limit)
	if err != nil {
		return nil, fmt.Errorf("search sessions: %w", err)
	}
	defer func() { _ = rows.Close() }()

	results := make([]SessionSearchResult, 0)
	for rows.Next() {
		var result SessionSearchResult
		var rank sql.NullFloat64
		if err := rows.Scan(
			&result.Session.ID,
			&result.Session.WorkstationID,
			&result.Session.ProjectID,
			&result.Session.ProjectPath,
			&result.Session.GitBranch,
			&result.Session.FirstMsgAt,
			&result.Session.LastMsgAt,
			&result.Session.ExchangeCount,
			&result.Session.ToolCounts,
			&result.Session.Topics,
			&result.Session.Content,
			&result.Session.FileMtime,
			&result.Session.IndexedAt,
			&rank,
		); err != nil {
			return nil, fmt.Errorf("scan search session: %w", err)
		}

		if rank.Valid {
			result.Rank = rank.Float64
		}

		results = append(results, result)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate search sessions: %w", err)
	}

	return results, nil
}

// GetSessionMtime returns (mtime, found, error) for skip check.
func (s *Store) GetSessionMtime(ctx context.Context, id string) (time.Time, bool, error) {
	var mtime sql.NullTime

	row := s.rawDB.QueryRowContext(ctx, `SELECT file_mtime FROM indexed_sessions WHERE id = $1`, id)
	if err := row.Scan(&mtime); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return time.Time{}, false, nil
		}
		return time.Time{}, false, fmt.Errorf("get session mtime: %w", err)
	}
	if !mtime.Valid {
		return time.Time{}, false, nil
	}

	return mtime.Time, true, nil
}
