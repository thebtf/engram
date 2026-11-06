package gorm

import (
	"fmt"

	"github.com/go-gormigrate/gormigrate/v2"
	gormlib "gorm.io/gorm"
)

const memoryLinksTableDDL = `(
	id BIGSERIAL PRIMARY KEY,
	source_id BIGINT REFERENCES %s ON DELETE SET NULL,
	target_id BIGINT REFERENCES %s ON DELETE SET NULL,
	relation TEXT NOT NULL CHECK (relation IN ('related_to', 'references')),
	target_version INTEGER CHECK (target_version > 0),
	author_principal_kind TEXT NOT NULL,
	author_principal TEXT NOT NULL,
	source_session_id TEXT NOT NULL DEFAULT '',
	removed_at TIMESTAMPTZ,
	CHECK (relation <> 'related_to' OR (source_id < target_id AND target_version IS NULL))
)`

const memoryLinkReceiptsDDL = `(
	principal_kind TEXT NOT NULL,
	principal TEXT NOT NULL,
	request_key TEXT NOT NULL CHECK (octet_length(request_key) BETWEEN 1 AND 1024),
	request_digest TEXT NOT NULL,
	link_id BIGINT NOT NULL REFERENCES %s ON DELETE RESTRICT,
	PRIMARY KEY (principal_kind, principal, request_key)
)`

// Compare structural metadata, not object names or data. References are checked
// separately because the temporary receipt template points at the template link.
const memoryLinksColumnsSQL = `SELECT COALESCE(jsonb_agg(jsonb_build_array(
	a.attname, format_type(a.atttypid, a.atttypmod), a.attnotnull, a.attidentity,
	a.attgenerated, CASE WHEN a.attname = 'id' THEN NULL ELSE pg_get_expr(d.adbin, d.adrelid) END
	) ORDER BY a.attnum), '[]'::jsonb)::text
	FROM pg_attribute a LEFT JOIN pg_attrdef d ON d.adrelid = a.attrelid AND d.adnum = a.attnum
	WHERE a.attrelid = ?::regclass AND a.attnum > 0 AND NOT a.attisdropped`

const memoryLinksConstraintsSQL = `SELECT COALESCE(jsonb_agg(jsonb_build_array(
	c.contype, c.conkey::text, c.confkey::text, c.condeferrable, c.condeferred,
	c.convalidated, c.confupdtype, c.confdeltype, c.confmatchtype,
	CASE WHEN c.contype = 'f' THEN c.confrelid = ?::regclass ELSE NULL END,
	pg_get_expr(c.conbin, c.conrelid)
	) ORDER BY c.contype, c.conkey::text, pg_get_expr(c.conbin, c.conrelid)), '[]'::jsonb)::text
	FROM pg_constraint c WHERE c.conrelid = ?::regclass`

const memoryLinksIndexSQL = `SELECT jsonb_build_array(ix.indisunique, ix.indisprimary,
	ix.indisvalid, ix.indisready, am.amname, ix.indkey::text, ix.indoption::text,
	pg_get_expr(ix.indexprs, ix.indrelid), pg_get_expr(ix.indpred, ix.indrelid))::text
	FROM pg_index ix JOIN pg_class idx ON idx.oid = ix.indexrelid
	JOIN pg_class tab ON tab.oid = ix.indrelid JOIN pg_am am ON am.oid = idx.relam
	WHERE idx.oid = to_regclass(?) AND tab.oid = ?::regclass`

func memoryLinksCompareMetadata(tx *gormlib.DB, query string, actualArgs, expectedArgs []any) error {
	var actual, expected string
	if err := tx.Raw(query, actualArgs...).Scan(&actual).Error; err != nil {
		return err
	}
	if err := tx.Raw(query, expectedArgs...).Scan(&expected).Error; err != nil {
		return err
	}
	if actual == "" || expected == "" || actual != expected {
		return fmt.Errorf("incompatible existing memory link schema")
	}
	return nil
}

// 192 is provisional until integration reconciles the target migration ledger.
func memoryLinksMigration192() *gormigrate.Migration {
	return &gormigrate.Migration{
		ID: "192_memory_links",
		Migrate: func(db *gormlib.DB) error {
			return db.Transaction(func(tx *gormlib.DB) error {
				var schema string
				if err := tx.Raw(`SELECT quote_ident(current_schema())`).Scan(&schema).Error; err != nil {
					return err
				}
				memories := schema + `.memories`
				links := schema + `.memory_links`
				receipts := schema + `.memory_link_receipts`
				statements := []string{
					`CREATE TABLE IF NOT EXISTS ` + links + ` ` + fmt.Sprintf(memoryLinksTableDDL, memories, memories),
					`CREATE UNIQUE INDEX IF NOT EXISTS memory_links_active_assertion ON ` + links + ` (source_id, target_id, relation, COALESCE(target_version, 0)) WHERE removed_at IS NULL`,
					`CREATE INDEX IF NOT EXISTS memory_links_source ON ` + links + ` (source_id, id) WHERE removed_at IS NULL`,
					`CREATE INDEX IF NOT EXISTS memory_links_target ON ` + links + ` (target_id, id) WHERE removed_at IS NULL AND relation = 'related_to'`,
					`CREATE TABLE IF NOT EXISTS ` + receipts + ` ` + fmt.Sprintf(memoryLinkReceiptsDDL, links),
					`CREATE TEMP TABLE b2_expected_memories (id BIGINT PRIMARY KEY) ON COMMIT DROP`,
					`CREATE TEMP TABLE b2_expected_memory_links ` + fmt.Sprintf(memoryLinksTableDDL, `b2_expected_memories`, `b2_expected_memories`) + ` ON COMMIT DROP`,
					`CREATE UNIQUE INDEX b2_expected_active_assertion ON b2_expected_memory_links (source_id, target_id, relation, COALESCE(target_version, 0)) WHERE removed_at IS NULL`,
					`CREATE INDEX b2_expected_source ON b2_expected_memory_links (source_id, id) WHERE removed_at IS NULL`,
					`CREATE INDEX b2_expected_target ON b2_expected_memory_links (target_id, id) WHERE removed_at IS NULL AND relation = 'related_to'`,
					`CREATE TEMP TABLE b2_expected_memory_link_receipts ` + fmt.Sprintf(memoryLinkReceiptsDDL, `b2_expected_memory_links`) + ` ON COMMIT DROP`,
				}
				for _, statement := range statements {
					if err := tx.Exec(statement).Error; err != nil {
						return fmt.Errorf("migration 192 memory links: %w", err)
					}
				}
				for _, relation := range []string{links, receipts} {
					var permanent bool
					if err := tx.Raw(`SELECT relkind = 'r' AND relpersistence = 'p' FROM pg_class WHERE oid = to_regclass(?)`, relation).Scan(&permanent).Error; err != nil {
						return err
					}
					if !permanent {
						return fmt.Errorf("memory link relations must be permanent logged tables")
					}
				}
				for _, pair := range [][2]string{{links, "pg_temp.b2_expected_memory_links"}, {receipts, "pg_temp.b2_expected_memory_link_receipts"}} {
					if err := memoryLinksCompareMetadata(tx, memoryLinksColumnsSQL, []any{pair[0]}, []any{pair[1]}); err != nil {
						return err
					}
				}
				if err := memoryLinksCompareMetadata(tx, memoryLinksConstraintsSQL, []any{memories, links}, []any{"pg_temp.b2_expected_memories", "pg_temp.b2_expected_memory_links"}); err != nil {
					return err
				}
				if err := memoryLinksCompareMetadata(tx, memoryLinksConstraintsSQL, []any{links, receipts}, []any{"pg_temp.b2_expected_memory_links", "pg_temp.b2_expected_memory_link_receipts"}); err != nil {
					return err
				}
				for _, pair := range [][2]string{{"memory_links_active_assertion", "b2_expected_active_assertion"}, {"memory_links_source", "b2_expected_source"}, {"memory_links_target", "b2_expected_target"}} {
					if err := memoryLinksCompareMetadata(tx, memoryLinksIndexSQL, []any{schema + "." + pair[0], links}, []any{"pg_temp." + pair[1], "pg_temp.b2_expected_memory_links"}); err != nil {
						return err
					}
				}
				var sequenceOK bool
				if err := tx.Raw(`SELECT pg_get_serial_sequence(?, 'id') IS NOT NULL AND pg_get_expr(d.adbin, d.adrelid) = format('nextval(%L::regclass)', (pg_get_serial_sequence(?, 'id')::regclass)::text) FROM pg_attribute a JOIN pg_attrdef d ON d.adrelid=a.attrelid AND d.adnum=a.attnum WHERE a.attrelid=?::regclass AND a.attname='id'`, links, links, links).Scan(&sequenceOK).Error; err != nil {
					return err
				}
				if !sequenceOK {
					return fmt.Errorf("incompatible existing memory link sequence")
				}
				return nil
			})
		},
		Rollback: func(*gormlib.DB) error {
			return fmt.Errorf("migration 192 rollback refused: retain memory links and retry receipts")
		},
	}
}
