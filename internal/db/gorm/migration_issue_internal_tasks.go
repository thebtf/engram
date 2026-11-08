package gorm

import (
	"fmt"

	"github.com/go-gormigrate/gormigrate/v2"
	"gorm.io/gorm"
)

const issueInternalTasksMigrationID = "193_issue_internal_tasks"

// This is an expand-only migration, not the compatible-writer cutover. Deployment
// must keep incompatible writers paused until the shared revision/history owner
// and all current adapters are ready. Raw JSON admission belongs before JSONB:
// PostgreSQL cannot recover duplicate members already lost during parsing.
func issueInternalTasksMigration193() *gormigrate.Migration {
	return &gormigrate.Migration{
		ID: issueInternalTasksMigrationID,
		Migrate: func(db *gorm.DB) error {
			return db.Transaction(func(tx *gorm.DB) error {
				for _, statement := range []string{
					`LOCK TABLE issues, issue_comments IN ACCESS EXCLUSIVE MODE`,
					`ALTER TABLE issues
					 ADD COLUMN IF NOT EXISTS metadata JSONB NOT NULL DEFAULT '{}',
					 ADD COLUMN IF NOT EXISTS revision BIGINT NOT NULL DEFAULT 1,
					 ADD COLUMN IF NOT EXISTS owner_label TEXT,
					 ADD COLUMN IF NOT EXISTS recipient_label TEXT,
					 ADD COLUMN IF NOT EXISTS actor_kind TEXT NOT NULL DEFAULT 'unknown',
					 ADD COLUMN IF NOT EXISTS actor_reference TEXT`,
					`ALTER TABLE issue_comments
					 ADD COLUMN IF NOT EXISTS metadata JSONB NOT NULL DEFAULT '{}',
					 ADD COLUMN IF NOT EXISTS actor_kind TEXT NOT NULL DEFAULT 'unknown',
					 ADD COLUMN IF NOT EXISTS actor_reference TEXT`,
					`DO $$ BEGIN
					 IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conrelid='issues'::regclass AND conname='issues_task_metadata_object') THEN
					  ALTER TABLE issues ADD CONSTRAINT issues_task_metadata_object CHECK (jsonb_typeof(metadata)='object');
					 END IF;
					 IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conrelid='issues'::regclass AND conname='issues_task_revision_positive') THEN
					  ALTER TABLE issues ADD CONSTRAINT issues_task_revision_positive CHECK (revision > 0);
					 END IF;
					 IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conrelid='issues'::regclass AND conname='issues_task_labels_nonempty') THEN
					  ALTER TABLE issues ADD CONSTRAINT issues_task_labels_nonempty CHECK ((owner_label IS NULL OR owner_label <> '') AND (recipient_label IS NULL OR recipient_label <> ''));
					 END IF;
					 IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conrelid='issues'::regclass AND conname='issues_task_actor_shape') THEN
					  ALTER TABLE issues ADD CONSTRAINT issues_task_actor_shape CHECK (
					   (actor_kind='unknown' AND actor_reference IS NULL) OR
					   (actor_kind IN ('client','operator','system') AND actor_reference IS NOT NULL AND btrim(actor_reference) <> ''));
					 END IF;
					 IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conrelid='issue_comments'::regclass AND conname='issue_comments_task_metadata_object') THEN
					  ALTER TABLE issue_comments ADD CONSTRAINT issue_comments_task_metadata_object CHECK (jsonb_typeof(metadata)='object');
					 END IF;
					 IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conrelid='issue_comments'::regclass AND conname='issue_comments_task_actor_shape') THEN
					  ALTER TABLE issue_comments ADD CONSTRAINT issue_comments_task_actor_shape CHECK (
					   (actor_kind='unknown' AND actor_reference IS NULL) OR
					   (actor_kind IN ('client','operator','system') AND actor_reference IS NOT NULL AND btrim(actor_reference) <> ''));
					 END IF;
					END $$`,
					// This actor shape distinguishes incompatible legacy INSERTs;
					// authentication and initial history remain application-owned.
					`CREATE OR REPLACE FUNCTION engram_issue_insert_actor_required() RETURNS trigger LANGUAGE plpgsql AS $$
					BEGIN
					 IF NEW.actor_kind='unknown' THEN
					  RAISE EXCEPTION USING ERRCODE='23514', MESSAGE='new issue and comment rows require a compatible actor-bearing writer';
					 END IF;
					 RETURN NEW;
					END $$`,
					`DROP TRIGGER IF EXISTS engram_issue_insert_actor_required ON issues`,
					`CREATE TRIGGER engram_issue_insert_actor_required BEFORE INSERT ON issues FOR EACH ROW EXECUTE FUNCTION engram_issue_insert_actor_required()`,
					`DROP TRIGGER IF EXISTS engram_issue_insert_actor_required ON issue_comments`,
					`CREATE TRIGGER engram_issue_insert_actor_required BEFORE INSERT ON issue_comments FOR EACH ROW EXECUTE FUNCTION engram_issue_insert_actor_required()`,
					`CREATE TABLE IF NOT EXISTS issue_create_receipts (
					 actor_identity_key TEXT NOT NULL CHECK (btrim(actor_identity_key) <> ''),
					 canonical_source_project_id TEXT NOT NULL REFERENCES projects(id) ON DELETE RESTRICT,
					 request_key TEXT NOT NULL CHECK (octet_length(request_key) BETWEEN 1 AND 512),
					 issue_id BIGINT REFERENCES issues(id) ON DELETE SET NULL,
					 payload_version SMALLINT NOT NULL CHECK (payload_version=1),
					 payload_hash BYTEA NOT NULL CHECK (octet_length(payload_hash)=32),
					 created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
					 PRIMARY KEY (actor_identity_key, canonical_source_project_id, request_key),
					 CONSTRAINT issue_create_receipts_source_uuid CHECK (
					  canonical_source_project_id ~ '^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$'
					  AND canonical_source_project_id <> '00000000-0000-0000-0000-000000000000')
					)`,
					`CREATE INDEX IF NOT EXISTS idx_issue_create_receipts_live_issue ON issue_create_receipts(issue_id) WHERE issue_id IS NOT NULL`,
					`CREATE TABLE IF NOT EXISTS issue_changes (
					 issue_id BIGINT NOT NULL CHECK (issue_id > 0),
					 revision BIGINT NOT NULL CHECK (revision > 0),
					 previous_revision BIGINT,
					 action TEXT NOT NULL CHECK (btrim(action) <> ''),
					 changed_fields JSONB NOT NULL DEFAULT '[]' CHECK (
					  jsonb_typeof(changed_fields)='array' AND
					  NOT jsonb_path_exists(changed_fields, '$[*] ? (@.type() != "string")')),
					 actor_kind TEXT NOT NULL DEFAULT 'unknown',
					 actor_reference TEXT,
					 attribution_source_project TEXT,
					 attribution_source_agent TEXT,
					 attribution_session TEXT,
					 changes JSONB NOT NULL DEFAULT '{}' CHECK (jsonb_typeof(changes)='object'),
					 created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
					 PRIMARY KEY (issue_id, revision),
					 CONSTRAINT issue_changes_revision_chain CHECK (
					  (revision=1 AND previous_revision IS NULL AND action IN ('baseline','created')) OR
					  (revision>1 AND previous_revision IS NOT NULL AND previous_revision=revision-1 AND action NOT IN ('baseline','created'))),
					 CONSTRAINT issue_changes_actor_shape CHECK (
					  (actor_kind='unknown' AND actor_reference IS NULL) OR
					  (actor_kind IN ('client','operator','system') AND actor_reference IS NOT NULL AND btrim(actor_reference) <> '')),
					 CONSTRAINT issue_changes_truthful_baseline CHECK (action <> 'baseline' OR
					  (actor_kind='unknown' AND actor_reference IS NULL AND changed_fields='[]'::jsonb AND changes='{}'::jsonb))
					)`,
					`CREATE OR REPLACE FUNCTION engram_issue_revision_monotonic() RETURNS trigger LANGUAGE plpgsql AS $$
					BEGIN
					 IF NEW.revision IS NULL OR NEW.revision < OLD.revision THEN
					  RAISE EXCEPTION USING ERRCODE='23514', MESSAGE='issue revision cannot decrease';
					 END IF;
					 IF NEW.revision <> OLD.revision AND NEW.revision-1 <> OLD.revision THEN
					  RAISE EXCEPTION USING ERRCODE='23514', MESSAGE='issue revision must advance exactly once';
					 END IF;
					 IF NEW.revision=OLD.revision AND
					  (NEW.title,NEW.body,NEW.priority,NEW.type,NEW.status,NEW.labels,NEW.metadata,NEW.owner_label,NEW.recipient_label,
					   NEW.acknowledged_at,NEW.resolved_at,NEW.reopened_at,NEW.closed_at)
					  IS DISTINCT FROM
					  (OLD.title,OLD.body,OLD.priority,OLD.type,OLD.status,OLD.labels,OLD.metadata,OLD.owner_label,OLD.recipient_label,
					   OLD.acknowledged_at,OLD.resolved_at,OLD.reopened_at,OLD.closed_at) THEN
					  RAISE EXCEPTION USING ERRCODE='23514', MESSAGE='material issue changes require revision advancement';
					 END IF;
					 RETURN NEW;
					END $$`,
					`DROP TRIGGER IF EXISTS engram_issue_revision_monotonic ON issues`,
					`CREATE TRIGGER engram_issue_revision_monotonic BEFORE UPDATE ON issues FOR EACH ROW EXECUTE FUNCTION engram_issue_revision_monotonic()`,
					`CREATE OR REPLACE FUNCTION engram_issue_receipt_immutable() RETURNS trigger LANGUAGE plpgsql AS $$
					BEGIN
					 IF TG_OP='INSERT' THEN
					  IF NEW.issue_id IS NULL THEN
					   RAISE EXCEPTION USING ERRCODE='23514', MESSAGE='issue receipt requires a live issue on creation';
					  END IF;
					  RETURN NEW;
					 END IF;
					 IF TG_OP='UPDATE' THEN
					  IF pg_trigger_depth()>1 AND OLD.issue_id IS NOT NULL AND NEW.issue_id IS NULL AND
					   (NEW.actor_identity_key,NEW.canonical_source_project_id,NEW.request_key,NEW.payload_version,NEW.payload_hash,NEW.created_at)
					   IS NOT DISTINCT FROM
					   (OLD.actor_identity_key,OLD.canonical_source_project_id,OLD.request_key,OLD.payload_version,OLD.payload_hash,OLD.created_at) THEN
					   RETURN NEW;
					  END IF;
					 END IF;
					 RAISE EXCEPTION USING ERRCODE='55000', MESSAGE='issue receipt binding and tombstone must be retained';
					END $$`,
					`DROP TRIGGER IF EXISTS engram_issue_receipt_immutable ON issue_create_receipts`,
					`CREATE TRIGGER engram_issue_receipt_immutable BEFORE INSERT OR UPDATE OR DELETE ON issue_create_receipts FOR EACH ROW EXECUTE FUNCTION engram_issue_receipt_immutable()`,
					`CREATE OR REPLACE FUNCTION engram_issue_change_append_only() RETURNS trigger LANGUAGE plpgsql AS $$
					DECLARE live_revision BIGINT;
					BEGIN
					 IF TG_OP <> 'INSERT' THEN
					  RAISE EXCEPTION USING ERRCODE='55000', MESSAGE='issue change history must be retained';
					 END IF;
					 SELECT revision INTO live_revision FROM issues WHERE id=NEW.issue_id FOR KEY SHARE;
					 IF NOT FOUND THEN
					  RAISE EXCEPTION USING ERRCODE='23503', MESSAGE='issue change requires a live issue on append';
					 END IF;
					 IF NEW.revision > live_revision THEN
					  RAISE EXCEPTION USING ERRCODE='23514', MESSAGE='issue change cannot precede its committed revision';
					 END IF;
					 RETURN NEW;
					END $$`,
					`DROP TRIGGER IF EXISTS engram_issue_change_append_only ON issue_changes`,
					`CREATE TRIGGER engram_issue_change_append_only BEFORE INSERT OR UPDATE OR DELETE ON issue_changes FOR EACH ROW EXECUTE FUNCTION engram_issue_change_append_only()`,
					`INSERT INTO issue_changes (issue_id, revision, previous_revision, action, actor_kind,
					 attribution_source_project, attribution_source_agent, attribution_session)
					 SELECT id, 1, NULL, 'baseline', 'unknown', NULLIF(source_project,''), NULLIF(source_agent,''), NULLIF(created_by_session,'')
					 FROM issues WHERE revision=1
					 ON CONFLICT (issue_id,revision) DO NOTHING`,
				} {
					if err := tx.Exec(statement).Error; err != nil {
						return fmt.Errorf("migration 193: %w", err)
					}
				}
				return nil
			})
		},
		Rollback: func(*gorm.DB) error {
			return fmt.Errorf("migration 193 rollback refused: retain expanded issue metadata, revisions, history and receipt tombstones; keep incompatible writers paused and roll forward")
		},
	}
}
