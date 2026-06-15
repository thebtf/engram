package gorm

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/thebtf/engram/internal/db/gorm/migrationmeta"
)

func TestSchemaIntegrity_EntityIDColumnsRequireForeignKeysOrWhitelist(t *testing.T) {
	schema := migrationSchema(t)

	// Explicit FK-less whitelist. The source of truth being tested is the
	// migrations.go ledger: current live CREATE TABLE statements plus later
	// ALTER TABLE ADD CONSTRAINT statements. Session identifiers here are
	// external Claude/SDK session IDs, not dashboard sessions.id references.
	whitelist := map[string]string{
		"citation_log.session_id":                   "external SDK session identifier (TEXT), not a row FK",
		"injection_log.session_id":                  "external SDK session identifier (TEXT), not a row FK",
		// observation_injections.session_id was removed in CR-3: migration 138 drops
		// observation_injections, so a whitelist key for a non-existent table is dead.
		"retrieval_stats_log.query_id":              "analytics correlation identifier, not a table FK",
		"search_query_log.session_id":               "external SDK session identifier (TEXT), not a row FK",
		"session_transcripts.session_id":            "external SDK session identifier (TEXT), not a row FK",
		"session_transcripts.claude_session_id":     "external Claude session identifier (TEXT), not a row FK",
		"sdk_sessions.claude_session_id":            "external Claude session identifier (TEXT), not a row FK",
		// session_segments.session_id is an external-session-string (TEXT, not a
		// FK to sdk_sessions.id). The former reasoning_traces.sdk_session_id entry
		// was removed in CR-2b: migration 137 drops reasoning_traces, so a whitelist
		// key for a non-existent table is dead.
		"session_segments.session_id":               "external SDK session identifier (TEXT), not a row FK",
		"telemetry_snapshots.last_operation_id":     "opaque operation identifier",
		// Self-referential version-tree pointers: a version row may reference a
		// parent/superseded version that has been pruned, so a hard FK with no
		// ON DELETE would block legitimate version cleanup. Intentionally FK-less.
		"versioned_documents.parent_version_id":     "self-referential version-tree pointer; parent may be pruned, no enforced FK",
		"versioned_documents.supersedes_version_id": "self-referential version-tree pointer; superseded row may be pruned, no enforced FK",
	}

	domainEntities := domainEntityNames(schema)
	var violations []string
	for _, table := range schema.LiveTables() {
		if table.CreateSQL == "" {
			continue
		}
		columns, err := migrationmeta.ColumnDefinitions(table.CreateSQL)
		require.NoError(t, err, "parse CREATE TABLE body for %s", table.Name)
		for _, column := range columns {
			if !isDomainEntityIDColumn(column.Name, domainEntities) {
				continue
			}
			key := table.Name + "." + column.Name
			if _, ok := whitelist[key]; ok {
				continue
			}
			if columnHasInlineReference(column.Definition) ||
				createTableHasForeignKey(table.CreateSQL, column.Name) ||
				laterStatementsHaveForeignKey(schema, table.Name, column.Name, table.CreatingMigrationNumericID) {
				continue
			}
			violations = append(violations, key)
		}
	}
	sort.Strings(violations)

	// Known-debt baseline (CR-0 decision D1, see .agent/specs/provenance-cleanup/
	// decisions.md). RED-in-CI is incompatible with the operator's CI-green merge
	// gate across the epic, so the guardrail pins the CURRENT dangling-id debt: it
	// is GREEN while violations match the baseline, and FAILS on (a) a NEW dangling
	// entity *_id column — regression — or (b) a baseline column cleaned without
	// shrinking this list. Each CR that removes a dangler MUST delete it here; that
	// edit is the CR's GREEN proof. Literal all-RED proof: evidence/cr0-red-proof.txt.
	// Known-debt baseline is now EMPTY. CR-2b (migration 137) dropped the 8
	// empty/derived tables; CR-3 (migration 138) dropped observation_injections,
	// removing the last dangling entity *_id. Emptying this baseline is CR-3's
	// schema_integrity GREEN proof — the guardrail is now pure regression
	// protection: any NEW dangling entity *_id column will fail the test.
	var baseline []string
	// CR-4 (migration 136, decision D7): audit_log.memory_id gained an FK to
	// memories(id) ON DELETE SET NULL, so it is no longer a dangling entity *_id
	// and was removed from this known-debt baseline. That removal is CR-4's
	// schema_integrity GREEN proof.

	require.Equal(t, baseline, violations,
		"dangling entity *_id drift changed vs known-debt baseline. A NEW entry is a regression "+
			"(add a FK or a whitelist reason). A removed entry means a CR cleaned it — delete it from "+
			"the baseline in this test (that edit is the CR's GREEN proof). Got %v", violations)
}

func domainEntityNames(schema *migrationmeta.Schema) map[string]bool {
	entities := make(map[string]bool)
	for table := range schema.Tables {
		entities[singularTableName(table)] = true
	}
	return entities
}

func singularTableName(table string) string {
	switch {
	case strings.HasSuffix(table, "ies"):
		return strings.TrimSuffix(table, "ies") + "y"
	case strings.HasSuffix(table, "s"):
		return strings.TrimSuffix(table, "s")
	default:
		return table
	}
}

func isDomainEntityIDColumn(column string, entities map[string]bool) bool {
	if !strings.HasSuffix(column, "_id") || strings.HasSuffix(column, "_ids") {
		return false
	}
	entity := strings.TrimSuffix(column, "_id")
	return entities[entity]
}

func columnHasInlineReference(definition string) bool {
	return strings.Contains(strings.ToLower(definition), " references ")
}

func createTableHasForeignKey(createSQL, column string) bool {
	return foreignKeyColumnPattern(column).MatchString(createSQL)
}

func laterStatementsHaveForeignKey(schema *migrationmeta.Schema, table, column string, afterMigration int) bool {
	tablePattern := regexp.MustCompile(`(?is)\bALTER\s+TABLE\s+(?:IF\s+EXISTS\s+)?` + regexp.QuoteMeta(table) + `\b`)
	columnPattern := foreignKeyColumnPattern(column)
	for _, stmt := range schema.SQLStatements {
		if stmt.MigrationNumericID < afterMigration {
			continue
		}
		if tablePattern.MatchString(stmt.Text) && columnPattern.MatchString(stmt.Text) {
			return true
		}
	}
	return false
}

func foreignKeyColumnPattern(column string) *regexp.Regexp {
	return regexp.MustCompile(fmt.Sprintf(`(?is)\bFOREIGN\s+KEY\s*\(\s*"?%s"?\s*\)`, regexp.QuoteMeta(column)))
}
