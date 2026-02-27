package sessions

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	gormdb "github.com/lukaszraczylo/claude-mnemonic/internal/db/gorm"
	"github.com/rs/zerolog"
)

type Indexer struct {
	store         *Store
	sessionsDir   string
	workstationID string
	logger        zerolog.Logger
}

func NewIndexer(store *Store, sessionsDir string, workstationID string, logger zerolog.Logger) *Indexer {
	return &Indexer{store: store, sessionsDir: sessionsDir, workstationID: workstationID, logger: logger}
}

// IndexAll scans sessionsDir for *.jsonl files and indexes new/changed ones.
// Returns count of sessions indexed.
func (idx *Indexer) IndexAll(ctx context.Context) (int, error) {
	count := 0

	err := filepath.WalkDir(idx.sessionsDir, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			idx.logger.Error().Err(walkErr).Str("path", path).Msg("walk session file")
			return nil
		}

		if ctx.Err() != nil {
			return ctx.Err()
		}

		if d.IsDir() {
			return nil
		}

		if !strings.HasSuffix(strings.ToLower(d.Name()), ".jsonl") {
			return nil
		}

		indexed, err := idx.IndexFile(ctx, path)
		if err != nil {
			idx.logger.Error().Err(err).Str("path", path).Msg("failed to index session file")
			return nil
		}
		if indexed {
			count++
		}
		return nil
	})

	if err != nil {
		return count, fmt.Errorf("walk sessions directory: %w", err)
	}

	return count, nil
}

// IndexFile indexes a single JSONL file. Skips if mtime unchanged.
// Returns (true, nil) if indexed, (false, nil) if skipped.
func (idx *Indexer) IndexFile(ctx context.Context, path string) (bool, error) {
	info, err := os.Stat(path)
	if err != nil {
		return false, fmt.Errorf("stat session file: %w", err)
	}

	sessionID := strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
	mtime := info.ModTime().UTC()

	savedMtime, found, err := idx.store.GetSessionMtime(ctx, sessionID)
	if err != nil {
		return false, fmt.Errorf("get session mtime: %w", err)
	}
	if found && savedMtime.Equal(mtime) {
		return false, nil
	}

	meta, err := ParseSession(path)
	if err != nil {
		return false, fmt.Errorf("parse session file: %w", err)
	}
	meta.SessionID = sessionID

	parts := make([]string, 0, len(meta.Exchanges)*2)
	for _, exchange := range meta.Exchanges {
		parts = append(parts, exchange.UserText)
		parts = append(parts, exchange.AssistantText)
	}
	content := strings.Join(parts, "\n")

	counts := meta.ToolCounts
	if counts == nil {
		counts = make(map[string]int)
	}
	toolCounts, err := json.Marshal(counts)
	if err != nil {
		return false, fmt.Errorf("marshal tool counts: %w", err)
	}

	session := &gormdb.IndexedSession{
		ID:            sessionID,
		WorkstationID: idx.workstationID,
		ProjectID:     ProjectID(meta.ProjectPath),
		ProjectPath:   sql.NullString{String: meta.ProjectPath, Valid: meta.ProjectPath != ""},
		GitBranch:     sql.NullString{String: meta.GitBranch, Valid: meta.GitBranch != ""},
		FirstMsgAt:    sql.NullTime{Time: meta.FirstMsgAt, Valid: !meta.FirstMsgAt.IsZero()},
		LastMsgAt:     sql.NullTime{Time: meta.LastMsgAt, Valid: !meta.LastMsgAt.IsZero()},
		ExchangeCount: meta.ExchangeCount,
		ToolCounts:    sql.NullString{String: string(toolCounts), Valid: true},
		Content:       sql.NullString{String: content, Valid: content != ""},
		FileMtime:     sql.NullTime{Time: mtime, Valid: true},
	}

	if err := idx.store.UpsertSession(ctx, session); err != nil {
		return false, fmt.Errorf("upsert session: %w", err)
	}

	return true, nil
}
