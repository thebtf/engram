package gorm

import (
	"fmt"

	"github.com/go-gormigrate/gormigrate/v2"
	gormlib "gorm.io/gorm"
)

// accessMilestoneMigration156 extends the dashboard auth substrate with invite
// and session lifecycle columns needed by the operator-console access lane.
// Expand-only: historical rows are preserved and existing migration 080 remains
// untouched; this migration only adds columns, constraints, and indexes.
func accessMilestoneMigration156() *gormigrate.Migration {
	return &gormigrate.Migration{
		ID: "156_access_milestone",
		Migrate: func(tx *gormlib.DB) error {
			stmts := []string{
				`ALTER TABLE invitations ADD COLUMN IF NOT EXISTS invitee_email TEXT NOT NULL DEFAULT ''`,
				`ALTER TABLE invitations ADD COLUMN IF NOT EXISTS role TEXT NOT NULL DEFAULT 'operator'`,
				`ALTER TABLE invitations ADD COLUMN IF NOT EXISTS expires_at TIMESTAMPTZ NOT NULL DEFAULT (now() + interval '7 days')`,
				`ALTER TABLE invitations ADD COLUMN IF NOT EXISTS revoked_at TIMESTAMPTZ`,
				`ALTER TABLE invitations ADD COLUMN IF NOT EXISTS revoked_by INTEGER REFERENCES users(id)`,
				`ALTER TABLE invitations ADD COLUMN IF NOT EXISTS revocation_reason TEXT NOT NULL DEFAULT ''`,
				`ALTER TABLE sessions ADD COLUMN IF NOT EXISTS user_agent TEXT NOT NULL DEFAULT ''`,
				`ALTER TABLE sessions ADD COLUMN IF NOT EXISTS remote_addr TEXT NOT NULL DEFAULT ''`,
				`ALTER TABLE sessions ADD COLUMN IF NOT EXISTS revoked_at TIMESTAMPTZ`,
				`ALTER TABLE sessions ADD COLUMN IF NOT EXISTS revoked_by INTEGER REFERENCES users(id)`,
				`ALTER TABLE sessions ADD COLUMN IF NOT EXISTS revocation_reason TEXT NOT NULL DEFAULT ''`,
				`CREATE INDEX IF NOT EXISTS idx_invitations_expires_created ON invitations (expires_at, created_at DESC)`,
				`CREATE INDEX IF NOT EXISTS idx_invitations_created_by_created ON invitations (created_by, created_at DESC)`,
				`CREATE INDEX IF NOT EXISTS idx_sessions_user_active_created ON sessions (user_id, created_at DESC)`,
				`CREATE INDEX IF NOT EXISTS idx_sessions_revoked_expires ON sessions (revoked_at, expires_at DESC)`,
				`DO $$
				BEGIN
					IF NOT EXISTS (
						SELECT 1 FROM pg_constraint WHERE conname = 'invitations_role_chk'
					) THEN
						ALTER TABLE invitations
							ADD CONSTRAINT invitations_role_chk
							CHECK (role IN ('admin','operator'));
					END IF;
				END $$`,
			}
			for _, stmt := range stmts {
				if err := tx.Exec(stmt).Error; err != nil {
					return fmt.Errorf("migration 156: %w", err)
				}
			}
			return nil
		},
		Rollback: func(tx *gormlib.DB) error {
			// Expand-only production migration: lifecycle evidence must remain.
			return nil
		},
	}
}
