package gorm

import (
	"fmt"
	"strings"

	"github.com/go-gormigrate/gormigrate/v2"
	"gorm.io/gorm"
)

const issueRevisionGuard193Body = `
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
					END `

const issueTargetSessionGuard196Body = `
BEGIN
 IF NEW.revision = OLD.revision AND NEW.target_session IS DISTINCT FROM OLD.target_session THEN
  RAISE EXCEPTION USING ERRCODE='23514', MESSAGE='material issue changes require revision advancement';
 END IF;
 RETURN NEW;
END `

// Migration 196 adds the missing field guard without rewriting retained rows or
// changing the 193 function. A retained 193 ledger therefore receives the fix.
func issueTargetSessionRevisionGuardMigration196() *gormigrate.Migration {
	return &gormigrate.Migration{
		ID: "196_issue_target_session_revision_guard",
		Migrate: func(db *gorm.DB) error {
			return db.Transaction(func(tx *gorm.DB) error {
				var schema string
				if err := tx.Raw(`SELECT current_schema()`).Scan(&schema).Error; err != nil {
					return fmt.Errorf("migration 196: %w", err)
				}
				if schema == "" {
					return fmt.Errorf("migration 196: current schema unavailable")
				}
				var present bool
				if err := tx.Raw(`SELECT to_regclass(format('%I.%I', ?::text, 'issues')) IS NOT NULL`, schema).Scan(&present).Error; err != nil {
					return fmt.Errorf("migration 196: %w", err)
				}
				if !present {
					return fmt.Errorf("migration 196: issue table missing in current schema")
				}
				if err := tx.Exec("LOCK TABLE " + quoted(schema) + "." + quoted("issues") + " IN ACCESS EXCLUSIVE MODE").Error; err != nil {
					return fmt.Errorf("migration 196: %w", err)
				}
				type guard struct {
					FunctionCount int
					TriggerCount  int
					Body          string
					Valid         bool
				}
				inspect := func(name string) (guard, error) {
					var result guard
					err := tx.Raw(`SELECT
					 (SELECT count(*) FROM pg_proc p JOIN pg_namespace n ON n.oid=p.pronamespace WHERE n.nspname=current_schema() AND p.proname=? AND p.pronargs=0) AS function_count,
					 (SELECT count(*) FROM pg_trigger t WHERE t.tgrelid='issues'::regclass AND t.tgname=?) AS trigger_count,
					 COALESCE((SELECT p.prosrc FROM pg_proc p JOIN pg_namespace n ON n.oid=p.pronamespace WHERE n.nspname=current_schema() AND p.proname=? AND p.pronargs=0 LIMIT 1),'') AS body,
					 EXISTS (SELECT 1 FROM pg_trigger t JOIN pg_proc p ON p.oid=t.tgfoid JOIN pg_namespace n ON n.oid=p.pronamespace WHERE t.tgrelid='issues'::regclass AND t.tgname=? AND NOT t.tgisinternal AND t.tgtype=19 AND t.tgenabled IN ('O','A') AND t.tgnargs=0 AND t.tgqual IS NULL AND t.tgconstraint=0 AND t.tgattr::text='' AND n.nspname=current_schema() AND p.proname=? AND p.pronargs=0 AND p.prokind='f' AND NOT p.prosecdef AND p.proconfig IS NULL AND p.prorettype='trigger'::regtype AND p.prolang=(SELECT oid FROM pg_language WHERE lanname='plpgsql')) AS valid`, name, name, name, name, name).Scan(&result).Error
					return result, err
				}
				normalize := func(value string) string { return strings.Join(strings.Fields(value), " ") }
				old, err := inspect("engram_issue_revision_monotonic")
				if err != nil {
					return fmt.Errorf("migration 196: %w", err)
				}
				if old.FunctionCount != 1 || old.TriggerCount != 1 || !old.Valid || normalize(old.Body) != normalize(issueRevisionGuard193Body) {
					return fmt.Errorf("migration 196: missing or unrecognized 193 revision guard")
				}
				var columnOK bool
				if err := tx.Raw(`SELECT EXISTS (SELECT 1 FROM pg_attribute WHERE attrelid='issues'::regclass AND attname='target_session' AND atttypid='text'::regtype AND attnotnull AND NOT attisdropped)`).Scan(&columnOK).Error; err != nil {
					return fmt.Errorf("migration 196: %w", err)
				}
				if !columnOK {
					return fmt.Errorf("migration 196: missing or unrecognized target_session")
				}
				const name = "engram_issue_target_session_revision_guard"
				current, err := inspect(name)
				if err != nil {
					return fmt.Errorf("migration 196: %w", err)
				}
				if current.FunctionCount == 1 && current.TriggerCount == 1 && current.Valid && normalize(current.Body) == normalize(issueTargetSessionGuard196Body) {
					return nil
				}
				if current.FunctionCount != 0 || current.TriggerCount != 0 {
					return fmt.Errorf("migration 196: conflicting target session guard")
				}
				if err := tx.Exec(`CREATE FUNCTION engram_issue_target_session_revision_guard() RETURNS trigger LANGUAGE plpgsql AS $guard$` + issueTargetSessionGuard196Body + `$guard$`).Error; err != nil {
					return fmt.Errorf("migration 196: %w", err)
				}
				if err := tx.Exec(`CREATE TRIGGER engram_issue_target_session_revision_guard BEFORE UPDATE ON issues FOR EACH ROW EXECUTE FUNCTION engram_issue_target_session_revision_guard()`).Error; err != nil {
					return fmt.Errorf("migration 196: %w", err)
				}
				return nil
			})
		},
		Rollback: func(*gorm.DB) error {
			return fmt.Errorf("migration 196 rollback refused: retain target session revision protection")
		},
	}
}
