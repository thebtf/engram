package gorm

import gormlib "gorm.io/gorm"

// Only restoreStored calls this helper after VerifyBackup authenticates and
// revalidates the census/manifest inventory and matches the locked target schema.
// Neither history/receipt table nor feature guard exists before migration 193;
// a partial pair or remaining feature guard is not a pre-feature schema.
// For a present pair, lock all four exact tables before inspecting any mode.
// Historical unknown actors, retained orphans and tombstones are not new writes.
// The caller must restore modes before committing; failure rolls back rows and
// transactional DDL on this same connection. Ordinary writers/apply never use it.
func consolidationSuspendIssueRestoreGuards(tx *gormlib.DB, inventory []consolidationTable) (func() error, error) {
	var history, receipts, issues, comments bool
	for _, table := range inventory {
		switch table.Name {
		case "issue_changes":
			history = true
		case "issue_create_receipts":
			receipts = true
		case "issues":
			issues = true
		case "issue_comments":
			comments = true
		}
	}
	if history != receipts {
		return nil, consolidationHold("RESTORE_CONSTRAINT_FAILED")
	}
	if history && (!issues || !comments) {
		return nil, consolidationHold("RESTORE_CONSTRAINT_FAILED")
	}
	var schema string
	if err := tx.Raw(`SELECT current_schema()`).Row().Scan(&schema); err != nil {
		return nil, err
	}
	if schema == "" {
		return nil, consolidationHold("RESTORE_CONSTRAINT_FAILED")
	}
	if !history {
		// VerifyBackup locks the existing tables and compares actual source/target
		// catalogs, but matching catalogs can both describe an incomplete upgrade.
		var featureGuards bool
		if err := tx.Raw(`SELECT EXISTS (SELECT 1 FROM pg_trigger t
			JOIN pg_class c ON c.oid=t.tgrelid JOIN pg_namespace n ON n.oid=c.relnamespace
			WHERE n.nspname=? AND ((c.relname IN ('issues','issue_comments') AND t.tgname='engram_issue_insert_actor_required')
			 OR (c.relname='issues' AND t.tgname IN ('engram_issue_revision_monotonic','engram_issue_target_session_revision_guard'))))`, schema).Row().Scan(&featureGuards); err != nil {
			return nil, err
		}
		if featureGuards {
			return nil, consolidationHold("RESTORE_CONSTRAINT_FAILED")
		}
		return func() error { return nil }, nil
	}
	if err := tx.Exec("LOCK TABLE " + quoted(schema) + "." + quoted("issue_changes") + ", " + quoted(schema) + "." + quoted("issue_comments") + ", " + quoted(schema) + "." + quoted("issue_create_receipts") + ", " + quoted(schema) + "." + quoted("issues") + " IN ACCESS EXCLUSIVE MODE").Error; err != nil {
		return nil, err
	}
	var guards []struct {
		SchemaName, TableName, TriggerName, Mode string
		Owner                                    bool
		RestoreMode                              string
	}
	if err := tx.Raw(`SELECT n.nspname AS schema_name,c.relname AS table_name,t.tgname AS trigger_name,t.tgenabled::text AS mode,
		(NOT t.tgisinternal AND t.tgtype=CASE WHEN t.tgname='engram_issue_insert_actor_required' THEN 7 ELSE 31 END AND t.tgnargs=0 AND t.tgqual IS NULL
		 AND t.tgconstraint=0 AND t.tgattr::text='' AND t.tgfoid=to_regprocedure(quote_ident(n.nspname)||'.'||quote_ident(t.tgname)||'()')) AS owner
		FROM pg_trigger t JOIN pg_class c ON c.oid=t.tgrelid JOIN pg_namespace n ON n.oid=c.relnamespace
		WHERE n.nspname=? AND (
		 (c.relname='issue_changes' AND t.tgname='engram_issue_change_append_only') OR
		 (c.relname='issue_create_receipts' AND t.tgname='engram_issue_receipt_immutable') OR
		 (c.relname IN ('issues','issue_comments') AND t.tgname='engram_issue_insert_actor_required'))
		ORDER BY c.relname,t.tgname`, schema).Scan(&guards).Error; err != nil {
		return nil, err
	}
	if len(guards) != 4 {
		return nil, consolidationHold("RESTORE_CONSTRAINT_FAILED")
	}
	// Validate every expected guard, including disabled guards, before any DDL.
	for i := range guards {
		guard := &guards[i]
		if !guard.Owner {
			return nil, consolidationHold("RESTORE_CONSTRAINT_FAILED")
		}
		switch guard.Mode {
		case "O":
			guard.RestoreMode = "ENABLE"
		case "A":
			guard.RestoreMode = "ENABLE ALWAYS"
		case "R":
			guard.RestoreMode = "ENABLE REPLICA"
		case "D":
			guard.RestoreMode = "DISABLE"
		default:
			return nil, consolidationHold("RESTORE_CONSTRAINT_FAILED")
		}
	}
	for _, guard := range guards {
		if err := tx.Exec("ALTER TABLE " + quoted(guard.SchemaName) + "." + quoted(guard.TableName) + " DISABLE TRIGGER " + quoted(guard.TriggerName)).Error; err != nil {
			return nil, err
		}
	}
	return func() error {
		for _, guard := range guards {
			if err := tx.Exec("ALTER TABLE " + quoted(guard.SchemaName) + "." + quoted(guard.TableName) + " " + guard.RestoreMode + " TRIGGER " + quoted(guard.TriggerName)).Error; err != nil {
				return err
			}
		}
		return nil
	}, nil
}
