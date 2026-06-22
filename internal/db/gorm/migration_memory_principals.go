package gorm

import (
	"fmt"

	"github.com/go-gormigrate/gormigrate/v2"
	"gorm.io/gorm"
)

func memoryPrincipalsMigration149() *gormigrate.Migration {
	return &gormigrate.Migration{
		ID: "149_memory_principals",
		Migrate: func(tx *gorm.DB) error {
			sqls := []string{
				`ALTER TABLE memories
					ADD COLUMN IF NOT EXISTS owner_principal TEXT NOT NULL DEFAULT ''`,
				`ALTER TABLE memories
					ADD COLUMN IF NOT EXISTS owner_principal_kind TEXT NOT NULL DEFAULT ''`,
				`ALTER TABLE memories
					ADD COLUMN IF NOT EXISTS agent_visibility TEXT NOT NULL DEFAULT ''`,
				`ALTER TABLE memories
					ADD COLUMN IF NOT EXISTS domain TEXT NOT NULL DEFAULT ''`,
				`DO $$
				BEGIN
					IF NOT EXISTS (
						SELECT 1
						FROM pg_constraint
						WHERE conname = 'memories_owner_principal_kind_chk'
					) THEN
						ALTER TABLE memories
							ADD CONSTRAINT memories_owner_principal_kind_chk
								CHECK (owner_principal_kind IN ('','human','agent','service'));
					END IF;
				END $$`,
				`DO $$
				BEGIN
					IF NOT EXISTS (
						SELECT 1
						FROM pg_constraint
						WHERE conname = 'memories_agent_visibility_chk'
					) THEN
						ALTER TABLE memories
							ADD CONSTRAINT memories_agent_visibility_chk
								CHECK (agent_visibility IN ('','private','shared'));
					END IF;
				END $$`,
				`CREATE INDEX IF NOT EXISTS idx_memories_owner_principal_created
					ON memories (owner_principal, created_at DESC)
					WHERE owner_principal <> '' AND deleted_at IS NULL`,
				`CREATE INDEX IF NOT EXISTS idx_memories_agent_visibility_created
					ON memories (agent_visibility, created_at DESC)
					WHERE agent_visibility <> '' AND deleted_at IS NULL`,
				`CREATE INDEX IF NOT EXISTS idx_memories_domain_owner
					ON memories (domain, owner_principal)
					WHERE domain <> '' AND deleted_at IS NULL`,
			}
			for _, stmt := range sqls {
				if err := tx.Exec(stmt).Error; err != nil {
					return fmt.Errorf("migration 149: %w", err)
				}
			}
			return nil
		},
		Rollback: func(tx *gorm.DB) error {
			sqls := []string{
				`DROP INDEX IF EXISTS idx_memories_domain_owner`,
				`DROP INDEX IF EXISTS idx_memories_agent_visibility_created`,
				`DROP INDEX IF EXISTS idx_memories_owner_principal_created`,
				`ALTER TABLE memories
					DROP CONSTRAINT IF EXISTS memories_agent_visibility_chk`,
				`ALTER TABLE memories
					DROP CONSTRAINT IF EXISTS memories_owner_principal_kind_chk`,
				`ALTER TABLE memories
					DROP COLUMN IF EXISTS domain`,
				`ALTER TABLE memories
					DROP COLUMN IF EXISTS agent_visibility`,
				`ALTER TABLE memories
					DROP COLUMN IF EXISTS owner_principal_kind`,
				`ALTER TABLE memories
					DROP COLUMN IF EXISTS owner_principal`,
			}
			for _, stmt := range sqls {
				if err := tx.Exec(stmt).Error; err != nil {
					return fmt.Errorf("migration 149 rollback: %w", err)
				}
			}
			return nil
		},
	}
}
