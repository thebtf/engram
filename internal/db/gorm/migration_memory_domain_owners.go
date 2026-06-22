package gorm

import (
	"fmt"

	"github.com/go-gormigrate/gormigrate/v2"
	gormlib "gorm.io/gorm"
)

func memoryDomainOwnersMigration150() *gormigrate.Migration {
	return &gormigrate.Migration{
		ID: "150_memory_domain_owners",
		Migrate: func(tx *gormlib.DB) error {
			sqls := []string{
				`CREATE TABLE IF NOT EXISTS memory_domain_owners (
					domain TEXT PRIMARY KEY,
					owner_principal TEXT NOT NULL,
					owner_principal_kind TEXT NOT NULL DEFAULT 'human',
					mode TEXT NOT NULL DEFAULT 'warn',
					created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
					updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
				)`,
				`DO $$
				BEGIN
					IF NOT EXISTS (
						SELECT 1
						FROM pg_constraint
						WHERE conname = 'memory_domain_owners_domain_nonempty_chk'
					) THEN
						ALTER TABLE memory_domain_owners
							ADD CONSTRAINT memory_domain_owners_domain_nonempty_chk
								CHECK (domain <> '');
					END IF;
				END $$`,
				`DO $$
				BEGIN
					IF NOT EXISTS (
						SELECT 1
						FROM pg_constraint
						WHERE conname = 'memory_domain_owners_owner_nonempty_chk'
					) THEN
						ALTER TABLE memory_domain_owners
							ADD CONSTRAINT memory_domain_owners_owner_nonempty_chk
								CHECK (owner_principal <> '');
					END IF;
				END $$`,
				`DO $$
				BEGIN
					IF NOT EXISTS (
						SELECT 1
						FROM pg_constraint
						WHERE conname = 'memory_domain_owners_kind_chk'
					) THEN
						ALTER TABLE memory_domain_owners
							ADD CONSTRAINT memory_domain_owners_kind_chk
								CHECK (owner_principal_kind IN ('human','agent','service'));
					END IF;
				END $$`,
				`DO $$
				BEGIN
					IF NOT EXISTS (
						SELECT 1
						FROM pg_constraint
						WHERE conname = 'memory_domain_owners_mode_chk'
					) THEN
						ALTER TABLE memory_domain_owners
							ADD CONSTRAINT memory_domain_owners_mode_chk
								CHECK (mode IN ('off','warn','reject'));
					END IF;
				END $$`,
				`CREATE INDEX IF NOT EXISTS idx_memory_domain_owners_owner
					ON memory_domain_owners (owner_principal, owner_principal_kind)`,
			}
			for _, stmt := range sqls {
				if err := tx.Exec(stmt).Error; err != nil {
					return fmt.Errorf("migration 150: %w", err)
				}
			}
			return nil
		},
		Rollback: func(tx *gormlib.DB) error {
			// Expand-only production migration: once operator domain ownership rows
			// exist, rollback must not drop or rewrite them.
			return nil
		},
	}
}
