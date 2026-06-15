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
		"observation_injections.session_id":         "external SDK session identifier (TEXT), not a row FK",
		"retrieval_stats_log.query_id":              "analytics correlation identifier, not a table FK",
		"search_query_log.session_id":               "external SDK session identifier (TEXT), not a row FK",
		"session_transcripts.session_id":            "external SDK session identifier (TEXT), not a row FK",
		"session_transcripts.claude_session_id":     "external Claude session identifier (TEXT), not a row FK",
		"sdk_sessions.claude_session_id":            "external Claude session identifier (TEXT), not a row FK",
		// reasoning_traces.sdk_session_id + session_segments.session_id are the
		// same external-session-string class as the entries above (TEXT, not a
		// FK to sdk_sessions.id). reasoning_traces is additionally slated for
		// DROP in CR-2; whitelisting the column keeps T001 focused on genuine
		// dangling-entity drift rather than external-id false positives.
		"reasoning_traces.sdk_session_id":           "external SDK session identifier (TEXT), not a row FK",
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

	require.NotEmpty(t, violations, "RED guardrail defect: expected current tree to expose dangling entity ID columns")
	// observation_* dangling ids point at the dropped observations table — they
	// disappear when CR-2/CR-3 drop those orphan tables (GREEN transition).
	require.Contains(t, violations, "observation_versions.observation_id")
	require.Contains(t, violations, "agent_observation_stats.observation_id")
	require.Contains(t, violations, "observation_injections.observation_id")
	// audit_log.memory_id is genuine missing-FK drift in a KEPT vNext table: it
	// references memories(id) but declares no FK. Disposition (add FK with the
	// right ON DELETE, or whitelist with a retention rationale) is CR-4 scope;
	// asserted here so its expected-RED status is documented, not silent.
	require.Contains(t, violations, "audit_log.memory_id")
	require.Empty(t, violations, "entity *_id columns require a FK or whitelist entry; found %v", violations)
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
