package gorm

import (
	"fmt"

	"github.com/go-gormigrate/gormigrate/v2"
	gormlib "gorm.io/gorm"
)

func memoryContextMigration191() *gormigrate.Migration {
	return &gormigrate.Migration{
		ID: "191_memory_owning_context",
		Migrate: func(db *gormlib.DB) error {
			return db.Transaction(func(tx *gormlib.DB) error {
				for _, statement := range []string{
					`ALTER TABLE memories ADD COLUMN IF NOT EXISTS context_kind TEXT NOT NULL DEFAULT 'legacy'`,
					`ALTER TABLE memories ADD COLUMN IF NOT EXISTS context_principal TEXT NOT NULL DEFAULT ''`,
					`ALTER TABLE memories ADD COLUMN IF NOT EXISTS context_principal_kind TEXT NOT NULL DEFAULT ''`,
					`CREATE TABLE IF NOT EXISTS memory_principals (
						principal_kind TEXT NOT NULL CHECK (principal_kind IN ('human','agent','service')),
						principal TEXT NOT NULL CHECK (principal <> '' AND principal = btrim(principal)),
						enrolled_by BIGINT NOT NULL REFERENCES users(id) ON DELETE RESTRICT,
						created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
						PRIMARY KEY (principal_kind, principal)
					)`,
					`DO $$ BEGIN IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conrelid = 'memories'::regclass AND conname = 'memories_owning_context_check') THEN
						ALTER TABLE memories ADD CONSTRAINT memories_owning_context_check CHECK (
							(context_kind = 'legacy' AND context_principal = '' AND context_principal_kind = '')
							OR (context_kind = 'project' AND project ~ '^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$' AND context_principal = '' AND context_principal_kind = '')
							OR (context_kind = 'global' AND project = '' AND context_principal = '' AND context_principal_kind = '')
							OR (context_kind = 'self' AND project = '' AND context_principal <> '' AND context_principal = btrim(context_principal) AND context_principal_kind IN ('human','agent','service'))
						); END IF; END; $$`,
					`CREATE INDEX IF NOT EXISTS idx_memories_self_context ON memories (context_principal_kind, context_principal, created_at DESC, id DESC) WHERE context_kind = 'self' AND deleted_at IS NULL`,
					`CREATE TABLE IF NOT EXISTS memory_request_keys (
						principal_kind TEXT NOT NULL CHECK (principal_kind IN ('human','agent','service')),
						principal TEXT NOT NULL CHECK (principal <> '' AND principal = btrim(principal)),
						context_kind TEXT NOT NULL CHECK (context_kind IN ('self','project','global')),
						project TEXT NOT NULL,
						request_key TEXT NOT NULL CHECK (octet_length(request_key) BETWEEN 1 AND 1024),
						request_digest TEXT NOT NULL CHECK (request_digest ~ '^[0-9a-f]{64}$'),
						memory_id BIGINT REFERENCES memories(id) ON DELETE SET NULL,
						created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
						CONSTRAINT memory_request_keys_scope PRIMARY KEY (principal_kind, principal, context_kind, project, request_key),
						CONSTRAINT memory_request_keys_context CHECK ((context_kind = 'project' AND project <> '') OR (context_kind IN ('self','global') AND project = ''))
					)`,
					`CREATE TABLE IF NOT EXISTS memory_revisions (
						memory_id BIGINT NOT NULL REFERENCES memories(id) ON DELETE CASCADE,
						version INTEGER NOT NULL CHECK (version > 0),
						content TEXT NOT NULL,
						changed_by_principal TEXT NOT NULL,
						changed_by_principal_kind TEXT NOT NULL,
						changed_at TIMESTAMPTZ NOT NULL DEFAULT now(),
						PRIMARY KEY (memory_id, version)
					)`,
				} {
					if err := tx.Exec(statement).Error; err != nil {
						return fmt.Errorf("migration 191 owning context: %w", err)
					}
				}
				return nil
			})
		},
		Rollback: func(*gormlib.DB) error {
			return fmt.Errorf("migration 191 rollback refused: retain explicit owning contexts, principal enrollment and retry receipts; older binaries cannot safely authorize typed memories")
		},
	}
}
