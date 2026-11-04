package gorm

import (
	"fmt"

	"github.com/go-gormigrate/gormigrate/v2"
	"gorm.io/gorm"
)

func issueAttributionMigration191() *gormigrate.Migration {
	return &gormigrate.Migration{
		ID: "191_issue_session_attribution",
		Migrate: func(db *gorm.DB) error {
			return db.Transaction(func(tx *gorm.DB) error {
				for _, statement := range []string{
					`ALTER TABLE issues ADD COLUMN IF NOT EXISTS created_by_session_source TEXT NOT NULL DEFAULT ''`,
					`ALTER TABLE issues ADD COLUMN IF NOT EXISTS source_client TEXT NOT NULL DEFAULT ''`,
					`ALTER TABLE issues ADD COLUMN IF NOT EXISTS target_session TEXT NOT NULL DEFAULT ''`,
					`ALTER TABLE issue_comments ADD COLUMN IF NOT EXISTS author_session TEXT NOT NULL DEFAULT ''`,
					`ALTER TABLE issue_comments ADD COLUMN IF NOT EXISTS author_session_source TEXT NOT NULL DEFAULT ''`,
					`ALTER TABLE issue_comments ADD COLUMN IF NOT EXISTS author_client TEXT NOT NULL DEFAULT ''`,
					`CREATE TABLE IF NOT EXISTS issue_history_events (
						id BIGSERIAL PRIMARY KEY,
						issue_id BIGINT NOT NULL REFERENCES issues(id) ON DELETE CASCADE,
						status TEXT NOT NULL CHECK (status IN ('open','acknowledged','resolved','reopened','closed','rejected')),
						author_project TEXT NOT NULL DEFAULT '',
						author_agent TEXT NOT NULL DEFAULT '',
						author_session TEXT NOT NULL DEFAULT '',
						session_source TEXT NOT NULL DEFAULT '' CHECK (session_source IN ('','transport','reported')),
						client TEXT NOT NULL DEFAULT '',
						created_at TIMESTAMPTZ NOT NULL DEFAULT now()
					)`,
					`CREATE INDEX IF NOT EXISTS idx_issues_created_by_session ON issues(created_by_session) WHERE created_by_session <> ''`,
					`CREATE INDEX IF NOT EXISTS idx_issues_target_session ON issues(target_session) WHERE target_session <> ''`,
					`CREATE INDEX IF NOT EXISTS idx_issue_comments_author_session ON issue_comments(author_session, issue_id) WHERE author_session <> ''`,
					`CREATE INDEX IF NOT EXISTS idx_issue_history_events_author_session ON issue_history_events(author_session, issue_id) WHERE author_session <> ''`,
					`CREATE INDEX IF NOT EXISTS idx_issue_history_events_issue_id ON issue_history_events(issue_id, id)`,
					`CREATE OR REPLACE TRIGGER engram_consolidation_scope_write
						BEFORE INSERT OR UPDATE ON issue_history_events
						FOR EACH ROW EXECUTE FUNCTION engram_consolidation_write_guard('author_project')`,
				} {
					if err := tx.Exec(statement).Error; err != nil {
						return fmt.Errorf("migration 191: %w", err)
					}
				}
				return nil
			})
		},
		Rollback: func(*gorm.DB) error {
			return fmt.Errorf("migration 191 rollback refused: authored issue attribution and history must be preserved")
		},
	}
}
