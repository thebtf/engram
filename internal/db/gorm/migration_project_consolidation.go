package gorm

import (
	"fmt"
	"sort"
	"strings"

	"github.com/go-gormigrate/gormigrate/v2"
	gormlib "gorm.io/gorm"
)

// Migration 186 is the inherited, not-yet-published migration slot. It adds
// migration metadata only, not an identity/ownership registry or live backfill.
func projectConsolidationMigration186() *gormigrate.Migration {
	return &gormigrate.Migration{ID: "186_project_consolidation", Migrate: func(tx *gormlib.DB) error {
		statements := []string{
			`CREATE TABLE IF NOT EXISTS project_consolidation_backups (
			 backup_id UUID PRIMARY KEY, plan_id UUID NOT NULL, plan_fingerprint TEXT NOT NULL,
			 ciphertext BYTEA NOT NULL, restore_identity TEXT NOT NULL, restore_snapshot TEXT NOT NULL,
			 restore_verified_at TIMESTAMPTZ NOT NULL)`,
			`CREATE TABLE IF NOT EXISTS project_consolidation_frames (
			 backup_id UUID NOT NULL REFERENCES project_consolidation_backups(backup_id) ON DELETE RESTRICT,
			 ordinal BIGINT NOT NULL CHECK(ordinal>=0), ciphertext BYTEA NOT NULL CHECK(octet_length(ciphertext)<=524288),
			 PRIMARY KEY(backup_id,ordinal))`,
			`CREATE TABLE IF NOT EXISTS project_consolidation_operations (
			 idempotency_key UUID PRIMARY KEY, operation_id UUID NOT NULL UNIQUE, plan_id UUID NOT NULL,
			 plan_fingerprint TEXT NOT NULL, receipt JSONB NOT NULL, completed_at TIMESTAMPTZ NOT NULL)`,
			`CREATE TABLE IF NOT EXISTS project_consolidation_memberships (
			 reference TEXT PRIMARY KEY, unresolved_reference TEXT NOT NULL DEFAULT '', table_name TEXT NOT NULL,
			 row_key JSONB NOT NULL CHECK(jsonb_typeof(row_key)='object'), field_path JSONB NOT NULL,
			 selector_reference TEXT NOT NULL DEFAULT '', project_id TEXT REFERENCES projects(id) ON DELETE RESTRICT,
			 reason TEXT NOT NULL DEFAULT '', plan_fingerprint TEXT NOT NULL,
			 native_version TEXT NOT NULL DEFAULT '', source_snapshot TEXT NOT NULL DEFAULT '', source_row_key JSONB NOT NULL DEFAULT '{}'::jsonb,
			 CHECK(project_id IS NULL OR project_id ~ '^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$'))`,
			`ALTER TABLE project_consolidation_memberships ADD COLUMN IF NOT EXISTS native_version TEXT NOT NULL DEFAULT '', ADD COLUMN IF NOT EXISTS source_snapshot TEXT NOT NULL DEFAULT '', ADD COLUMN IF NOT EXISTS source_row_key JSONB NOT NULL DEFAULT '{}'::jsonb`,
			`CREATE INDEX IF NOT EXISTS idx_consolidation_unresolved ON project_consolidation_memberships(unresolved_reference,table_name) WHERE project_id IS NULL`,
			`CREATE INDEX IF NOT EXISTS idx_consolidation_canonical ON project_consolidation_memberships(project_id,table_name) WHERE project_id IS NOT NULL`,
			`CREATE TABLE IF NOT EXISTS project_consolidation_redirects (
			 selector TEXT NOT NULL, session_id TEXT NOT NULL DEFAULT '', project_id TEXT NOT NULL REFERENCES projects(id) ON DELETE RESTRICT,
			 plan_fingerprint TEXT NOT NULL, PRIMARY KEY(selector,session_id))`,
			`CREATE OR REPLACE FUNCTION engram_consolidation_scope(selector TEXT, session TEXT DEFAULT '') RETURNS TEXT
             LANGUAGE plpgsql STABLE AS $$ DECLARE resolved TEXT; candidates BIGINT; BEGIN
              IF selector='' OR (selector ~ '^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$'
               AND selector<>'00000000-0000-0000-0000-000000000000') THEN RETURN selector; END IF;
              SELECT count(DISTINCT project_id),min(project_id) INTO candidates,resolved FROM project_consolidation_redirects
              WHERE project_consolidation_redirects.selector=$1 AND session_id IN ('',session);
              IF candidates>1 THEN RAISE EXCEPTION 'PROJECT_ADOPTION_CONFLICTING_MEMBERSHIP'; END IF;
              IF candidates=0 THEN RAISE EXCEPTION 'PROJECT_ADOPTION_UNRESOLVED_MEMBERSHIP'; END IF;
              RETURN resolved;
			 END $$`,
			`CREATE OR REPLACE FUNCTION engram_consolidation_write_guard() RETURNS TRIGGER LANGUAGE plpgsql AS $$
             DECLARE field TEXT; value JSONB; result JSONB; session_ids TEXT[]; session_value TEXT; target TEXT; candidate TEXT; item RECORD; BEGIN
              SELECT ARRAY(SELECT DISTINCT v FROM (
               SELECT to_jsonb(NEW)->>'sdk_session_id' AS v UNION ALL SELECT to_jsonb(NEW)->>'claude_session_id'
               UNION ALL SELECT to_jsonb(NEW)->>'session_id' UNION ALL SELECT to_jsonb(NEW)->>'source_session_id'
               UNION ALL SELECT jsonb_array_elements_text(COALESCE(NULLIF(to_jsonb(NEW)->'source_sessions','null'::jsonb),'[]'::jsonb))
              ) q WHERE v IS NOT NULL AND v<>'' ORDER BY v) INTO session_ids;
              FOREACH field IN ARRAY TG_ARGV LOOP
               value:=to_jsonb(NEW)->field;
               IF jsonb_typeof(value) IN ('string','array') THEN
                result:='[]'::jsonb;
                FOR item IN SELECT v FROM jsonb_array_elements_text(
                 CASE WHEN jsonb_typeof(value)='array' THEN value ELSE jsonb_build_array(value) END
                ) WITH ORDINALITY e(v,ord) ORDER BY ord LOOP
                 target:=NULL;
                 IF cardinality(session_ids)=0 THEN
                  target:=engram_consolidation_scope(item.v);
                 ELSE
                  FOREACH session_value IN ARRAY session_ids LOOP
                   candidate:=engram_consolidation_scope(item.v,session_value);
                   IF target IS NOT NULL AND target<>candidate THEN RAISE EXCEPTION 'PROJECT_ADOPTION_CONFLICTING_MEMBERSHIP'; END IF;
                   target:=candidate;
                  END LOOP;
                 END IF;
                 result:=result||jsonb_build_array(target);
                END LOOP;
                IF jsonb_typeof(value)='string' THEN result:=result->0; END IF;
                NEW:=jsonb_populate_record(NEW,jsonb_build_object(field,result));
               END IF;
			  END LOOP;
			  RETURN NEW;
			 END $$`,
		}
		for _, statement := range statements {
			if err := tx.Exec(statement).Error; err != nil {
				return fmt.Errorf("migration 186: %w", err)
			}
		}
		fields := map[string][]string{}
		columns, err := consolidationColumns(tx)
		if err != nil {
			return err
		}
		available := map[string]bool{}
		for _, column := range columns {
			available[column.TableName+"."+column.ColumnName] = true
		}
		for _, role := range consolidationRoles {
			if (role.Kind == "tenant" || role.Kind == "array") && available[role.Table+"."+role.Column] {
				fields[role.Table] = append(fields[role.Table], "'"+role.Column+"'")
			}
		}
		names := make([]string, 0, len(fields))
		for table := range fields {
			names = append(names, table)
		}
		sort.Strings(names)
		for _, table := range names {
			if err := tx.Exec("CREATE OR REPLACE TRIGGER engram_consolidation_scope_write BEFORE INSERT OR UPDATE ON " + quoted(table) + " FOR EACH ROW EXECUTE FUNCTION engram_consolidation_write_guard(" + strings.Join(fields[table], ",") + ")").Error; err != nil {
				return err
			}
		}
		return nil
	}, Rollback: func(tx *gormlib.DB) error { return consolidationHold("RETAIN_MIGRATION_METADATA") }}
}
