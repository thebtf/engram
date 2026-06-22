package gorm

import (
	"fmt"

	"github.com/go-gormigrate/gormigrate/v2"
	"gorm.io/gorm"
)

func apiTokenPrincipalsMigration148() *gormigrate.Migration {
	return &gormigrate.Migration{
		ID: "148_api_token_principals",
		Migrate: func(tx *gorm.DB) error {
			sqls := []string{
				`ALTER TABLE api_tokens
					ADD COLUMN IF NOT EXISTS principal TEXT NOT NULL DEFAULT ''`,
				`ALTER TABLE api_tokens
					ADD COLUMN IF NOT EXISTS principal_kind TEXT NOT NULL DEFAULT 'human'`,
				`DO $$
				BEGIN
					IF NOT EXISTS (
						SELECT 1
						FROM pg_constraint
						WHERE conname = 'api_tokens_principal_kind_chk'
					) THEN
						ALTER TABLE api_tokens
							ADD CONSTRAINT api_tokens_principal_kind_chk
								CHECK (principal_kind IN ('human','agent','service'));
					END IF;
				END $$`,
				`CREATE INDEX IF NOT EXISTS idx_api_tokens_principal
					ON api_tokens (principal)
					WHERE principal <> '' AND NOT revoked`,
			}
			for _, stmt := range sqls {
				if err := tx.Exec(stmt).Error; err != nil {
					return fmt.Errorf("migration 148: %w", err)
				}
			}
			return nil
		},
		Rollback: func(tx *gorm.DB) error {
			sqls := []string{
				`DROP INDEX IF EXISTS idx_api_tokens_principal`,
				`ALTER TABLE api_tokens
					DROP CONSTRAINT IF EXISTS api_tokens_principal_kind_chk`,
				`ALTER TABLE api_tokens
					DROP COLUMN IF EXISTS principal_kind`,
				`ALTER TABLE api_tokens
					DROP COLUMN IF EXISTS principal`,
			}
			for _, stmt := range sqls {
				if err := tx.Exec(stmt).Error; err != nil {
					return fmt.Errorf("migration 148 rollback: %w", err)
				}
			}
			return nil
		},
	}
}
