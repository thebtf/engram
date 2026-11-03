package gorm

import (
	"fmt"

	"github.com/go-gormigrate/gormigrate/v2"
	gormlib "gorm.io/gorm"
)

const browserSessionPublicHandleSQL = "('" + browserSessionHandlePrefix + "' || pg_catalog.encode(pg_catalog.sha256(pg_catalog.convert_to('" + browserSessionHandleDomain + "' || %s, 'UTF8')), 'hex'))"

func browserSessionPublicHandleMigration190() *gormigrate.Migration {
	return &gormigrate.Migration{
		ID: "190_browser_session_public_handle",
		Migrate: func(db *gormlib.DB) error {
			return db.Transaction(func(tx *gormlib.DB) error {
				// Serialize the backfill with old writers. Their next write sees
				// the compatibility trigger when this additive change commits.
				if err := tx.Exec(`LOCK TABLE sessions IN ACCESS EXCLUSIVE MODE`).Error; err != nil {
					return fmt.Errorf("migration 190 lock sessions: %w", err)
				}
				if err := tx.Exec(`ALTER TABLE sessions ADD COLUMN IF NOT EXISTS public_handle TEXT`).Error; err != nil {
					return fmt.Errorf("migration 190 add public handle: %w", err)
				}
				var compatibleColumn int64
				if err := tx.Raw(`SELECT count(*) FROM pg_attribute
					WHERE attrelid = 'sessions'::regclass AND attname = 'public_handle'
					  AND NOT attisdropped AND atttypid = 'text'::regtype AND attgenerated = ''`).Scan(&compatibleColumn).Error; err != nil {
					return fmt.Errorf("migration 190 inspect public handle column: %w", err)
				}
				if compatibleColumn != 1 {
					return fmt.Errorf("migration 190: incompatible sessions.public_handle column")
				}
				canonical := fmt.Sprintf(browserSessionPublicHandleSQL, "id")
				var incompatible bool
				if err := tx.Raw(`SELECT EXISTS (SELECT 1 FROM sessions WHERE public_handle IS NOT NULL AND public_handle <> ` + canonical + `)`).Scan(&incompatible).Error; err != nil {
					return fmt.Errorf("migration 190 inspect retained handles: %w", err)
				}
				if incompatible {
					return fmt.Errorf("migration 190: retained browser session handle does not match its credential")
				}
				for _, statement := range []string{
					`UPDATE sessions SET public_handle = ` + canonical + ` WHERE public_handle IS NULL`,
					`ALTER TABLE sessions ALTER COLUMN public_handle SET NOT NULL`,
					`CREATE UNIQUE INDEX IF NOT EXISTS idx_sessions_public_handle ON sessions (public_handle)`,
					`CREATE OR REPLACE FUNCTION engram_browser_session_public_handle_sync() RETURNS trigger
					 LANGUAGE plpgsql VOLATILE AS $$ BEGIN
					 NEW.public_handle := ` + fmt.Sprintf(browserSessionPublicHandleSQL, "NEW.id") + `;
					 RETURN NEW;
					 END; $$`,
					`CREATE OR REPLACE TRIGGER engram_browser_session_public_handle
					 BEFORE INSERT OR UPDATE OF id, public_handle ON sessions
					 FOR EACH ROW EXECUTE FUNCTION engram_browser_session_public_handle_sync()`,
				} {
					if err := tx.Exec(statement).Error; err != nil {
						return fmt.Errorf("migration 190 install browser session handles: %w", err)
					}
				}
				var compatibleIndex int64
				if err := tx.Raw(`SELECT count(*)
					FROM pg_index i
					JOIN pg_class idx ON idx.oid = i.indexrelid
					JOIN pg_am am ON am.oid = idx.relam
					JOIN pg_attribute a ON a.attrelid = i.indrelid AND a.attnum = i.indkey[0]
					WHERE i.indrelid = 'sessions'::regclass AND idx.relname = 'idx_sessions_public_handle'
					  AND am.amname = 'btree' AND i.indisunique AND i.indisvalid AND i.indisready
					  AND i.indnkeyatts = 1 AND i.indexprs IS NULL AND i.indpred IS NULL
					  AND a.attname = 'public_handle'`).Scan(&compatibleIndex).Error; err != nil {
					return fmt.Errorf("migration 190 inspect public handle index: %w", err)
				}
				if compatibleIndex != 1 {
					return fmt.Errorf("migration 190: incompatible idx_sessions_public_handle index")
				}
				return nil
			})
		},
		Rollback: func(*gormlib.DB) error {
			return fmt.Errorf("migration 190 rollback refused: retain browser session handles, unique index and old-writer compatibility trigger")
		},
	}
}
