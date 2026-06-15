package gorm

import (
	"fmt"
	"sort"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestProvenanceLint_LiveTablesAreKeepSetOrVNext(t *testing.T) {
	schema := migrationSchema(t)

	// Live-table source: internal/db/gorm/migrations.go, parsed from Migrate
	// bodies only. Rollback DROP statements are historical reversibility logic,
	// not live demolitions. The base keep-set comes from CR-0 T002; protected
	// document/auth/telemetry tables come from architecture.md classification.
	allowedPreVNextTables := map[string]string{
		"api_tokens":                  "keep-set auth keycards",
		"behavioral_rules":            "primary always-inject rules data",
		"content":                     "protected document/content invariant",
		"credentials":                 "keep-set encrypted vault",
		"documents":                   "protected document store",
		"invitations":                 "dashboard auth state",
		"issue_comments":              "issue tracker child data",
		"issues":                      "keep-set issue tracker",
		"memories":                    "keep-set memory store",
		"projects":                    "project registry",
		"retrieval_stats_log":         "non-observation retrieval telemetry",
		"sdk_sessions":                "primary unprocessed session registry",
		"search_query_log":            "non-observation query telemetry",
		"session_transcripts":         "primary raw transcript data",
		"sessions":                    "dashboard auth sessions",
		"system_config":               "operator configuration state",
		"telemetry_snapshots":         "operator health snapshots",
		"users":                       "dashboard auth users",
		"versioned_document_comments": "document review child data",
		"versioned_documents":         "protected versioned documents",
	}

	var violations []string
	for _, table := range schema.LiveTables() {
		if table.CreatingMigrationNumericID >= 105 {
			continue
		}
		if _, ok := allowedPreVNextTables[table.Name]; ok {
			continue
		}
		violations = append(violations, fmt.Sprintf("%s created by migration %s", table.Name, table.CreatingMigrationID))
	}
	sort.Strings(violations)

	require.NotEmpty(t, violations, "RED guardrail defect: expected current tree to expose pre-vNext non-keep-set live tables")
	require.Contains(t, violations, "observation_injections created by migration 058_observation_injections_table")
	require.Empty(t, violations, "live tables must be keep-set/protected data or created by migration >=105; found %v", violations)
}
