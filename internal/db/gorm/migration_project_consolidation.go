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
			`CREATE TABLE IF NOT EXISTS project_consolidation_operations (
			 idempotency_key UUID PRIMARY KEY, operation_id UUID NOT NULL UNIQUE, plan_id UUID NOT NULL,
			 plan_fingerprint TEXT NOT NULL, receipt JSONB NOT NULL, completed_at TIMESTAMPTZ NOT NULL)`,
			`CREATE TABLE IF NOT EXISTS project_consolidation_memberships (
			 reference TEXT PRIMARY KEY, unresolved_reference TEXT NOT NULL DEFAULT '', table_name TEXT NOT NULL,
			 row_key JSONB NOT NULL CHECK(jsonb_typeof(row_key)='object'), field_path JSONB NOT NULL,
			 selector_reference TEXT NOT NULL DEFAULT '', project_id TEXT REFERENCES projects(id) ON DELETE RESTRICT,
			 reason TEXT NOT NULL DEFAULT '', plan_fingerprint TEXT NOT NULL,
			 CHECK(project_id IS NULL OR project_id ~ '^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$'))`,
			`CREATE INDEX IF NOT EXISTS idx_consolidation_unresolved ON project_consolidation_memberships(unresolved_reference,table_name) WHERE project_id IS NULL`,
			`CREATE INDEX IF NOT EXISTS idx_consolidation_canonical ON project_consolidation_memberships(project_id,table_name) WHERE project_id IS NOT NULL`,
			`CREATE TABLE IF NOT EXISTS project_consolidation_redirects (
			 selector TEXT NOT NULL, session_id TEXT NOT NULL DEFAULT '', project_id TEXT NOT NULL REFERENCES projects(id) ON DELETE RESTRICT,
			 plan_fingerprint TEXT NOT NULL, PRIMARY KEY(selector,session_id))`,
			`CREATE OR REPLACE FUNCTION engram_consolidation_scope(selector TEXT, session TEXT DEFAULT '') RETURNS TEXT
			 LANGUAGE plpgsql STABLE AS $$ DECLARE resolved TEXT; candidates BIGINT; BEGIN
			  SELECT count(DISTINCT project_id),min(project_id) INTO candidates,resolved FROM project_consolidation_redirects
			  WHERE project_consolidation_redirects.selector=$1 AND session_id IN ('',session);
			  IF candidates>1 THEN RAISE EXCEPTION 'PROJECT_ADOPTION_CONFLICTING_MEMBERSHIP'; END IF;
			  IF candidates=0 AND EXISTS(SELECT 1 FROM project_consolidation_memberships WHERE selector_reference<>'' AND project_id IS NULL) THEN
			   RETURN selector;
			  END IF;
			  RETURN COALESCE(resolved,selector);
			 END $$`,
			`CREATE OR REPLACE FUNCTION engram_consolidation_write_guard() RETURNS TRIGGER LANGUAGE plpgsql AS $$
			 DECLARE field TEXT; value JSONB; result JSONB; session_ids TEXT[]; session_value TEXT; target TEXT; candidate TEXT; old_selector TEXT; item RECORD; BEGIN
			  SELECT ARRAY(SELECT DISTINCT v FROM (
			   SELECT to_jsonb(NEW)->>'sdk_session_id' AS v UNION ALL SELECT to_jsonb(NEW)->>'claude_session_id'
			   UNION ALL SELECT to_jsonb(NEW)->>'session_id' UNION ALL SELECT to_jsonb(NEW)->>'source_session_id'
			   UNION ALL SELECT jsonb_array_elements_text(COALESCE(to_jsonb(NEW)->'source_sessions','[]'::jsonb))
			  ) q WHERE v IS NOT NULL AND v<>'') INTO session_ids;
			  FOREACH field IN ARRAY TG_ARGV LOOP
			   value:=to_jsonb(NEW)->field; result:=value;
			   IF jsonb_typeof(value)='string' THEN
			    old_selector:=value#>>'{}'; target:=engram_consolidation_scope(old_selector);
			    FOREACH session_value IN ARRAY session_ids LOOP
			     candidate:=engram_consolidation_scope(old_selector,session_value);
			     IF candidate<>old_selector THEN
			      IF target<>old_selector AND target<>candidate THEN RAISE EXCEPTION 'PROJECT_ADOPTION_CONFLICTING_MEMBERSHIP'; END IF;
			      target:=candidate;
			     ELSIF EXISTS(SELECT 1 FROM project_consolidation_redirects WHERE selector=old_selector AND session_id<>'') THEN
			      RAISE EXCEPTION 'PROJECT_ADOPTION_UNRESOLVED_MEMBERSHIP';
			     END IF;
			    END LOOP;
			    IF target=old_selector AND EXISTS(SELECT 1 FROM project_consolidation_redirects WHERE selector=old_selector AND session_id<>'') THEN
			     RAISE EXCEPTION 'PROJECT_ADOPTION_UNRESOLVED_MEMBERSHIP';
			    END IF;
			    result:=to_jsonb(target);
			   ELSIF jsonb_typeof(value)='array' THEN
			    result:='[]'::jsonb;
			    FOR item IN SELECT v,ord FROM jsonb_array_elements_text(value) WITH ORDINALITY e(v,ord) LOOP
			     target:=engram_consolidation_scope(item.v);
			     IF target=item.v AND EXISTS(SELECT 1 FROM project_consolidation_redirects WHERE selector=item.v AND session_id<>'') THEN
			      RAISE EXCEPTION 'PROJECT_ADOPTION_UNRESOLVED_MEMBERSHIP';
			     END IF;
			     result:=result||jsonb_build_array(target);
			    END LOOP;
			   END IF;
			   NEW:=jsonb_populate_record(NEW,jsonb_build_object(field,result));
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
