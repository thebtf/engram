// Package issuefixture constructs synthetic historical rows for tests and fixture
// commands. Application writers and authenticated backup restore do not use it.
package issuefixture

import (
	"fmt"

	"github.com/lib/pq"
	"gorm.io/gorm"
)

// SeedHistorical preserves unknown historical attribution without inventing an
// actor. Only the two exact INSERT guards are suspended in the caller's fixture
// schema; data and original trigger modes commit together or roll back together.
func SeedHistorical(db *gorm.DB, seed func(*gorm.DB) error) error {
	return db.Transaction(func(tx *gorm.DB) error {
		var schema string
		if err := tx.Raw("SELECT current_schema()").Row().Scan(&schema); err != nil {
			return err
		}
		if schema == "" {
			return fmt.Errorf("historical issue fixture requires a schema")
		}
		prefix := pq.QuoteIdentifier(schema) + "."
		if err := tx.Exec("LOCK TABLE " + prefix + "issue_comments, " + prefix + "issues IN ACCESS EXCLUSIVE MODE").Error; err != nil {
			return err
		}
		var guards []struct {
			TableName, Mode string
			Owner           bool
		}
		if err := tx.Raw(`SELECT c.relname AS table_name,t.tgenabled::text AS mode,
			(NOT t.tgisinternal AND t.tgtype=7 AND t.tgnargs=0 AND t.tgqual IS NULL
			 AND t.tgconstraint=0 AND t.tgattr::text=''
			 AND t.tgfoid=to_regprocedure(quote_ident(n.nspname)||'.engram_issue_insert_actor_required()')) AS owner
			FROM pg_trigger t JOIN pg_class c ON c.oid=t.tgrelid JOIN pg_namespace n ON n.oid=c.relnamespace
			WHERE n.nspname=? AND c.relname IN ('issues','issue_comments')
			 AND t.tgname='engram_issue_insert_actor_required' ORDER BY c.relname`, schema).Scan(&guards).Error; err != nil {
			return err
		}
		if len(guards) != 2 {
			return fmt.Errorf("historical issue fixture requires both INSERT guards")
		}
		modes := make([]string, len(guards))
		for i, guard := range guards {
			if !guard.Owner {
				return fmt.Errorf("historical issue fixture INSERT guard identity mismatch")
			}
			switch guard.Mode {
			case "O":
				modes[i] = "ENABLE"
			case "A":
				modes[i] = "ENABLE ALWAYS"
			case "R":
				modes[i] = "ENABLE REPLICA"
			case "D":
				modes[i] = "DISABLE"
			default:
				return fmt.Errorf("historical issue fixture INSERT guard mode mismatch")
			}
		}
		for _, guard := range guards {
			if err := tx.Exec("ALTER TABLE " + prefix + pq.QuoteIdentifier(guard.TableName) + " DISABLE TRIGGER engram_issue_insert_actor_required").Error; err != nil {
				return err
			}
		}
		if err := seed(tx); err != nil {
			return err
		}
		for i, guard := range guards {
			if err := tx.Exec("ALTER TABLE " + prefix + pq.QuoteIdentifier(guard.TableName) + " " + modes[i] + " TRIGGER engram_issue_insert_actor_required").Error; err != nil {
				return err
			}
		}
		return nil
	})
}
