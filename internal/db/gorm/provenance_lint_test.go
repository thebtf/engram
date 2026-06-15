package gorm

import (
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
		violations = append(violations, table.Name)
	}
	sort.Strings(violations)

	// Known-debt baseline (CR-0 decision D1, see .agent/specs/provenance-cleanup/
	// decisions.md). The guardrail can't assert zero violations during the epic —
	// the demolition debt below is exactly what CR-1..CR-3 remove, and CI would be
	// red for every intermediate CR. Instead it pins the CURRENT debt: the test is
	// GREEN while violations match the baseline, and FAILS on (a) any NEW pre-vNext
	// non-keep table — regression toward the demolished state — or (b) a baseline
	// entry that is cleaned without shrinking this list. Each CR that drops a table
	// MUST delete it from the baseline; that edit is the CR's GREEN proof. The
	// literal all-RED proof is preserved at evidence/cr0-red-proof.txt + commit 2f0faef.
	baseline := []string{
		// CR-2b (migration 137) dropped the 8 empty/derived observation-era tables
		// that previously sat here. Their removal from this baseline is CR-2b's
		// provenance_lint GREEN proof. Only observation_injections remains — it is
		// dropped by CR-3 (migration after the CR-1 citation rewire), at which point
		// this baseline becomes empty and the guardrail is pure regression protection.
		"observation_injections", // CR-3 drop (after CR-1 citation rewire)
	}

	require.Equal(t, baseline, violations,
		"provenance drift changed vs known-debt baseline. If a NEW pre-vNext non-keep table appeared, "+
			"that is a regression — keep-set it with a reason or revert. If a baseline table was cleaned, "+
			"remove it from the baseline in this test (that edit is the CR's GREEN proof). Got %v", violations)
}
