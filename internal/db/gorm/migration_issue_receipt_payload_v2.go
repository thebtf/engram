package gorm

import (
	"fmt"

	"github.com/go-gormigrate/gormigrate/v2"
	"gorm.io/gorm"
)

// issueReceiptPayloadV2Migration194 widens only the known 193 version check.
// The table, receipt rows, bindings, tombstones and immutable trigger remain
// untouched. Unknown schema shapes fail closed rather than rewriting evidence.
func issueReceiptPayloadV2Migration194() *gormigrate.Migration {
	return &gormigrate.Migration{
		ID: "194_issue_receipt_payload_v2",
		Migrate: func(db *gorm.DB) error {
			return db.Transaction(func(tx *gorm.DB) error {
				if err := tx.Exec(`LOCK TABLE issue_create_receipts IN ACCESS EXCLUSIVE MODE`).Error; err != nil {
					return fmt.Errorf("migration 194: %w", err)
				}
				return tx.Exec(`DO $$
				DECLARE
				 receipt_table oid;
				 version_column smallint;
				 check_count integer;
				 check_name text;
				 check_definition text;
				 check_columns smallint[];
				 check_validated boolean;
				BEGIN
				 receipt_table := to_regclass(format('%I.%I', current_schema(), 'issue_create_receipts'));
				 IF receipt_table IS NULL THEN
				  RAISE EXCEPTION 'migration 194: receipt table missing';
				 END IF;
				 SELECT attnum INTO version_column FROM pg_attribute
				  WHERE attrelid = receipt_table AND attname = 'payload_version'
				   AND atttypid = 'smallint'::regtype AND attnotnull AND NOT attisdropped;
				 IF version_column IS NULL THEN
				  RAISE EXCEPTION 'migration 194: unexpected receipt version column';
				 END IF;
				 SELECT count(*) INTO check_count FROM pg_constraint
				  WHERE conrelid = receipt_table AND contype = 'c' AND version_column = ANY(conkey);
				 IF check_count <> 1 THEN
				  RAISE EXCEPTION 'migration 194: unexpected receipt version checks';
				 END IF;
				 SELECT conname, pg_get_constraintdef(oid), conkey, convalidated
				  INTO check_name, check_definition, check_columns, check_validated
				  FROM pg_constraint WHERE conrelid = receipt_table AND contype = 'c'
				   AND version_column = ANY(conkey);
				 IF check_name <> 'issue_create_receipts_payload_version_check'
				  OR check_columns <> ARRAY[version_column]::smallint[] OR NOT check_validated THEN
				  RAISE EXCEPTION 'migration 194: unrecognized receipt version check';
				 END IF;
				 IF check_definition = 'CHECK ((payload_version = ANY (ARRAY[1, 2])))' THEN
				  RETURN;
				 END IF;
				 IF check_definition <> 'CHECK ((payload_version = 1))' THEN
				  RAISE EXCEPTION 'migration 194: unrecognized receipt version definition';
				 END IF;
				 ALTER TABLE issue_create_receipts DROP CONSTRAINT issue_create_receipts_payload_version_check;
				 ALTER TABLE issue_create_receipts ADD CONSTRAINT issue_create_receipts_payload_version_check CHECK (payload_version IN (1, 2));
				END $$`).Error
			})
		},
		Rollback: func(*gorm.DB) error {
			return fmt.Errorf("migration 194 rollback refused: receipt payload versions and immutable bindings must be retained")
		},
	}
}
