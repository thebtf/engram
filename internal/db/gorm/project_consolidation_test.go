package gorm

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/go-gormigrate/gormigrate/v2"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/lib/pq"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/thebtf/engram/internal/config"
	vaultcrypto "github.com/thebtf/engram/internal/crypto"
	"github.com/thebtf/engram/pkg/models"
	"gorm.io/driver/postgres"
	gormlib "gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func TestProjectConsolidationOccurrencePreservesArrayAndHistory(t *testing.T) {
	raw := json.RawMessage(`{"project":"mixed","content":"mixed","audit":{"project":"other"},"ids":["mixed","mixed"]}`)
	moved, err := consolidationReplaceOccurrence(raw, []string{"ids", "1"}, "mixed", "canonical")
	require.NoError(t, err)
	require.JSONEq(t, `{"project":"mixed","content":"mixed","audit":{"project":"other"},"ids":["mixed","canonical"]}`, string(moved))
	_, err = consolidationReplaceOccurrence(raw, []string{"audit", "project"}, "mixed", "canonical")
	require.EqualError(t, err, "PROJECT_ADOPTION_STALE_MAPPING")
	fields := consolidationJSONFields(map[string]any{"project": "a", "content": "a", "nested": map[string]any{"affected_projects": []any{"a", "b"}}}, nil, false)
	require.Len(t, fields, 3)
}

func TestProjectConsolidationMixedBucketAndEvidenceConflict(t *testing.T) {
	// Pure mapping logic uses an ephemeral private test Vault, not owner data.
	v, err := vaultcrypto.OpenExistingVault(&config.Config{EncryptionKey: strings.Repeat("ab", 32)})
	require.NoError(t, err)
	s := NewProjectConsolidationStore(nil, v, nil)
	rows := []consolidationRow{{Reference: "one", Table: "memories", Column: "project", Kind: "tenant", SelectorReference: "same"}, {Reference: "two", Table: "memories", Column: "project", Kind: "tenant", SelectorReference: "same"}, {Reference: "three", Table: "memories", Column: "project", Kind: "tenant", SelectorReference: "same"}}
	a, b := uuid.NewString(), uuid.NewString()
	evidence := []ConsolidationEvidence{{Reference: "urn:engram:proof:a", Kind: "portable_marker", Group: "urn:engram:group:a", ProjectID: a, Rows: []string{"one"}}, {Reference: "urn:engram:proof:b", Kind: "portable_marker", Group: "urn:engram:group:b", ProjectID: b, Rows: []string{"two"}}}
	mapping, err := s.buildMapping(rows, evidence)
	require.NoError(t, err)
	require.Equal(t, a, mapping[0].ProjectID)
	require.Equal(t, b, mapping[1].ProjectID)
	require.Empty(t, mapping[2].ProjectID)
	require.NotEmpty(t, mapping[2].UnresolvedReference)
	evidence = append(evidence, ConsolidationEvidence{Reference: "urn:engram:proof:c", Kind: "portable_marker", Group: "urn:engram:group:a", ProjectID: b, Rows: []string{"one"}})
	mapping, err = s.buildMapping(rows, evidence)
	require.NoError(t, err)
	require.Empty(t, mapping[0].ProjectID)
	require.Equal(t, "conflicting_membership_evidence", mapping[0].Reason)
	evidence[0].Kind = "basename"
	_, err = s.buildMapping(rows, evidence)
	require.EqualError(t, err, "PROJECT_ADOPTION_INVALID_EVIDENCE")
}

func TestProjectConsolidationExcludedNativeClassificationCompatibility(t *testing.T) {
	legacy := consolidationCensusState{Rows: []consolidationRow{{Table: "memories", Key: JSONRaw(`{"id":1}`), NativeVersion: "7"}}, Gaps: []string{}}
	previous := structuralDigest(struct {
		Rows  []consolidationRow
		Gaps  []string
		Roles []consolidationRole
	}{legacy.Rows, legacy.Gaps, consolidationRoles})
	require.Equal(t, previous, consolidationClassification(legacy), "legacy sealed backup classification must remain unchanged")
	encoded, err := json.Marshal(legacy)
	require.NoError(t, err)
	require.NotContains(t, string(encoded), "ExcludedNative")
	legacy.ExcludedNative = []consolidationOmittedVersion{{Table: "memories", Key: JSONRaw(`{"id":2}`), NativeVersion: "8"}}
	bound := consolidationClassification(legacy)
	require.NotEqual(t, previous, bound)
	legacy.ExcludedNative[0].NativeVersion = "9"
	require.NotEqual(t, bound, consolidationClassification(legacy), "excluded native versions must be authenticated")
}

func TestProjectConsolidationPostMigrationMemoryCensusPlan(t *testing.T) {
	db, _ := openMemoryContext191UpgradeDB(t)
	require.NoError(t, db.Exec(`CREATE TABLE projects (id TEXT PRIMARY KEY)`).Error)
	require.NoError(t, projectConsolidationMigration186().Migrate(db))
	runner := gormigrate.New(db, gormigrate.DefaultOptions, []*gormigrate.Migration{memoryPrincipalsMigration149(), memoryContextMigration191()})
	require.NoError(t, runner.Migrate())
	require.NoError(t, db.Exec(`INSERT INTO memories (id, project, content, context_kind, context_principal, context_principal_kind) VALUES
		(19201, ?, 'typed project fixture', 'project', '', ''),
		(19202, '', 'self fixture', 'self', 'agent/fixture', 'agent'),
		(19203, '', 'global fixture', 'global', '', '')`, memoryContext191Project).Error)
	require.NoError(t, db.Exec(`CREATE TABLE content_chunks (
		id BIGSERIAL PRIMARY KEY, memory_id BIGINT NOT NULL REFERENCES memories(id),
		seq INTEGER NOT NULL, text TEXT NOT NULL DEFAULT '', embedding public.vector(4096),
		model TEXT NOT NULL, created_at TIMESTAMPTZ NOT NULL DEFAULT now(), UNIQUE(memory_id, seq))`).Error)
	require.NoError(t, db.Exec(`INSERT INTO content_chunks (id, memory_id, seq, text, model) VALUES
		(19201, 19201, 0, 'project chunk', 'fixture'),
		(19202, 19202, 0, 'self chunk', 'fixture'),
		(19203, 19203, 0, 'global chunk', 'fixture')`).Error)
	require.NoError(t, db.Exec(`INSERT INTO memory_revisions (memory_id, version, content, changed_by_principal, changed_by_principal_kind) VALUES
		(19201, 1, 'project revision', 'agent/fixture', 'agent'),
		(19202, 1, 'self revision', 'agent/fixture', 'agent'),
		(19203, 1, 'global revision', 'agent/fixture', 'agent')`).Error)
	require.NoError(t, db.Exec(`INSERT INTO memory_request_keys (principal_kind, principal, context_kind, project, request_key, request_digest, memory_id) VALUES
		('agent', 'agent/fixture', 'project', ?, 'project-request', ?, 19201),
		('agent', 'agent/fixture', 'self', '', 'self-request', ?, 19202),
		('agent', 'agent/fixture', 'global', '', 'global-request', ?, 19203)`, memoryContext191Project, strings.Repeat("a", 64), strings.Repeat("b", 64), strings.Repeat("c", 64)).Error)
	for _, table := range []string{"content_chunks", "memory_revisions", "memory_request_keys"} {
		var count int64
		require.NoError(t, db.Raw("SELECT count(*) FROM "+table+" c JOIN memories m ON m.id=c.memory_id WHERE m.id IN (19201,19202,19203)").Scan(&count).Error)
		require.EqualValues(t, 3, count, "%s fixture rows must have real memory parents", table)
	}
	// These multi-hop and mixed-parent controls were added after the original RED.
	require.NoError(t, db.Exec(`INSERT INTO memories (id, project, content, context_kind, context_principal, context_principal_kind) VALUES (19204, '', 'second self fixture', 'self', 'agent/fixture', 'agent')`).Error)
	require.NoError(t, db.Exec(`CREATE TABLE memory_links (id BIGSERIAL PRIMARY KEY, source_id BIGINT REFERENCES memories(id) ON DELETE SET NULL, target_id BIGINT REFERENCES memories(id) ON DELETE SET NULL, relation TEXT NOT NULL CHECK (relation IN ('related_to','references')), target_version INTEGER CHECK (target_version > 0), author_principal_kind TEXT NOT NULL, author_principal TEXT NOT NULL, source_session_id TEXT NOT NULL DEFAULT '', removed_at TIMESTAMPTZ, CHECK (relation <> 'related_to' OR (source_id < target_id AND target_version IS NULL)))`).Error)
	require.NoError(t, db.Exec(`CREATE TABLE memory_link_receipts (principal_kind TEXT NOT NULL, principal TEXT NOT NULL, request_key TEXT NOT NULL CHECK (octet_length(request_key) BETWEEN 1 AND 1024), request_digest TEXT NOT NULL, link_id BIGINT NOT NULL REFERENCES memory_links(id) ON DELETE RESTRICT, PRIMARY KEY (principal_kind, principal, request_key))`).Error)
	require.NoError(t, db.Exec(`INSERT INTO memory_links (id, source_id, target_id, relation, author_principal_kind, author_principal) VALUES
		(1, 19202, 19204, 'references', 'agent', 'agent/fixture'),
		(2, 19101, 19201, 'references', 'agent', 'agent/fixture'),
		(3, 19202, 19201, 'references', 'agent', 'agent/fixture'),
		(4, NULL, 19201, 'references', 'agent', 'agent/fixture')`).Error)
	require.NoError(t, db.Exec(`INSERT INTO memory_link_receipts (principal_kind, principal, request_key, request_digest, link_id) VALUES
		('agent', 'agent/fixture', 'self-link', 'fixture', 1),
		('agent', 'agent/fixture', 'project-link', 'fixture', 2),
		('agent', 'agent/fixture', 'mixed-link', 'fixture', 3),
		('agent', 'agent/fixture', 'unknown-link', 'fixture', 4)`).Error)
	snapshots := map[string]string{}
	for _, table := range []string{"memories", "content_chunks", "memory_revisions", "memory_request_keys", "memory_links", "memory_link_receipts"} {
		var snapshot string
		require.NoError(t, db.Raw("SELECT COALESCE(jsonb_agg(to_jsonb(t) ORDER BY to_jsonb(t)::text), '[]'::jsonb)::text FROM "+table+" t").Scan(&snapshot).Error)
		snapshots[table] = snapshot
	}
	v, err := vaultcrypto.OpenExistingVault(&config.Config{EncryptionKey: strings.Repeat("ab", 32)})
	require.NoError(t, err)
	s := NewProjectConsolidationStore(db, v, nil)
	census, err := s.Census(t.Context(), "post-migration-191-fixture")
	require.NoError(t, err)
	var native consolidationCensusState
	require.NoError(t, s.open(census.Reference, "census", &native))
	refs := map[int64]string{}
	var contextRefs []string
	for _, row := range native.Rows {
		if row.Table != "memories" {
			continue
		}
		var key struct {
			ID int64 `json:"id"`
		}
		require.NoError(t, json.Unmarshal(row.Key, &key))
		assert.NotEqual(t, int64(19202), key.ID, "self memories are excluded")
		assert.NotEqual(t, int64(19203), key.ID, "global memories are excluded")
		assert.NotEqual(t, "context_kind", row.Column)
		if row.Column == "context_kind" {
			contextRefs = append(contextRefs, row.Reference)
		}
		if (key.ID == 19101 || key.ID == 19201) && row.Column == "project" {
			require.Equal(t, memoryContext191Project, row.Selector)
			refs[key.ID] = row.Reference
		}
	}
	require.Len(t, refs, 2, "legacy and typed project memberships remain in the census")
	target := uuid.NewString()
	evidenceRows := append([]string{refs[19101], refs[19201]}, contextRefs...)
	plan, err := s.Preflight(t.Context(), ConsolidationPreflightRequest{CensusReference: census.Reference, CensusFingerprint: census.Fingerprint, Evidence: []ConsolidationEvidence{{Reference: "urn:engram:proof:fixture", Kind: "portable_marker", Group: "urn:engram:group:fixture", ProjectID: target, Rows: evidenceRows}}})
	require.NoError(t, err)
	for _, id := range []int64{19101, 19201} {
		found := false
		for _, mapping := range plan.Mapping {
			if mapping.Reference == refs[id] {
				found = true
				require.Equal(t, "project", mapping.Role)
				require.Equal(t, target, mapping.ProjectID)
			}
		}
		require.True(t, found, "project membership must survive preflight")
	}
	for _, mapping := range plan.Mapping {
		if mapping.Family == "memories" {
			assert.NotEqual(t, "context_kind", mapping.Role)
		}
	}
	byReference := make(map[string]ConsolidationMapping, len(plan.Mapping))
	for _, mapping := range plan.Mapping {
		byReference[mapping.Reference] = mapping
	}
	seenProject := map[string]bool{}
	for _, row := range native.Rows {
		if row.Table != "content_chunks" && row.Table != "memory_revisions" && row.Table != "memory_request_keys" {
			continue
		}
		var key struct {
			ID          int64  `json:"id"`
			MemoryID    int64  `json:"memory_id"`
			ContextKind string `json:"context_kind"`
		}
		require.NoError(t, json.Unmarshal(row.Key, &key))
		id := key.MemoryID
		if row.Table == "content_chunks" {
			id = key.ID
		} else if row.Table == "memory_request_keys" {
			switch key.ContextKind {
			case "project":
				id = 19201
			case "self":
				id = 19202
			case "global":
				id = 19203
			}
		}
		mapping, ok := byReference[row.Reference]
		require.True(t, ok, "preflight must account for each census row")
		switch id {
		case 19201:
			seenProject[row.Table] = true
			assert.Equal(t, target, mapping.ProjectID, "%s must inherit its project parent", row.Table)
			assert.Empty(t, mapping.UnresolvedReference, "%s project child must resolve", row.Table)
		case 19202, 19203:
			assert.Empty(t, mapping.ProjectID, "%s memory %d must not acquire project membership", row.Table, id)
			assert.Empty(t, mapping.UnresolvedReference, "%s memory %d must not become unresolved project data (reason %s)", row.Table, id, mapping.Reason)
		}
	}
	for _, table := range []string{"content_chunks", "memory_revisions", "memory_request_keys"} {
		assert.True(t, seenProject[table], "%s project control must appear in the census", table)
	}
	seenLinks := map[string]bool{}
	for _, row := range native.Rows {
		if row.Table != "memory_links" && row.Table != "memory_link_receipts" {
			continue
		}
		var key struct {
			ID         int64  `json:"id"`
			RequestKey string `json:"request_key"`
		}
		require.NoError(t, json.Unmarshal(row.Key, &key))
		kind := key.RequestKey
		if row.Table == "memory_links" {
			kind = map[int64]string{1: "self-link", 2: "project-link", 3: "mixed-link", 4: "unknown-link"}[key.ID]
		}
		seenLinks[row.Table+":"+kind] = true
		mapping, ok := byReference[row.Reference]
		require.True(t, ok)
		switch kind {
		case "self-link":
			assert.Empty(t, mapping.ProjectID)
			assert.Empty(t, mapping.UnresolvedReference)
		case "project-link":
			assert.Equal(t, target, mapping.ProjectID, "%s project link must inherit both parents", row.Table)
			assert.Empty(t, mapping.UnresolvedReference)
		case "mixed-link", "unknown-link":
			assert.Empty(t, mapping.ProjectID, "%s %s must not guess ownership", row.Table, kind)
			assert.NotEmpty(t, mapping.UnresolvedReference, "%s %s must remain unresolved", row.Table, kind)
		default:
			t.Fatalf("unexpected %s fixture key %q", row.Table, kind)
		}
	}
	for _, table := range []string{"memory_links", "memory_link_receipts"} {
		for _, kind := range []string{"project-link", "mixed-link", "unknown-link"} {
			assert.True(t, seenLinks[table+":"+kind], "%s %s must remain in the census", table, kind)
		}
	}
	for table, before := range snapshots {
		var after string
		require.NoError(t, db.Raw("SELECT COALESCE(jsonb_agg(to_jsonb(t) ORDER BY to_jsonb(t)::text), '[]'::jsonb)::text FROM "+table+" t").Scan(&after).Error)
		assert.Equal(t, before, after, "%s synthetic records must remain unchanged", table)
	}
	state, err := s.plan(plan.Reference, plan.Fingerprint)
	require.NoError(t, err)
	bindings, sessions := consolidationRedirectBindings(state)
	assert.Empty(t, sessions)
	assert.Contains(t, bindings, consolidationRedirectBinding{Selector: memoryContext191Project, Target: target})
	for _, binding := range bindings {
		assert.NotContains(t, []string{"project", "legacy", "self", "global"}, binding.Selector)
	}
}

func TestProjectConsolidationNullableIntermediateParent(t *testing.T) {
	db, _ := openMemoryContext191UpgradeDB(t)
	require.NoError(t, db.Exec(`CREATE TABLE projects (id TEXT PRIMARY KEY)`).Error)
	require.NoError(t, projectConsolidationMigration186().Migrate(db))
	require.NoError(t, gormigrate.New(db, gormigrate.DefaultOptions, []*gormigrate.Migration{memoryPrincipalsMigration149(), memoryContextMigration191()}).Migrate())
	require.NoError(t, db.Exec(`INSERT INTO memories (id,project,content,context_kind,context_principal,context_principal_kind) VALUES (19201,?,'project fixture','project','','')`, memoryContext191Project).Error)
	require.NoError(t, db.Exec(`CREATE TABLE fixture_parent (id BIGINT PRIMARY KEY, memory_id BIGINT NOT NULL REFERENCES memories(id))`).Error)
	require.NoError(t, db.Exec(`CREATE TABLE fixture_child (id BIGINT PRIMARY KEY, memory_id BIGINT NOT NULL REFERENCES memories(id), parent_id BIGINT REFERENCES fixture_parent(id))`).Error)
	require.NoError(t, db.Exec(`CREATE TABLE fixture_descendant (id BIGINT PRIMARY KEY, child_id BIGINT NOT NULL REFERENCES fixture_child(id))`).Error)
	require.NoError(t, db.Exec(`INSERT INTO fixture_parent VALUES (1,19201)`).Error)
	require.NoError(t, db.Exec(`INSERT INTO fixture_child VALUES (1,19201,NULL),(2,19201,1)`).Error)
	require.NoError(t, db.Exec(`INSERT INTO fixture_descendant VALUES (1,1),(2,2)`).Error)
	var missing, complete int64
	require.NoError(t, db.Raw(`SELECT count(*) FILTER (WHERE c.parent_id IS NULL), count(*) FILTER (WHERE p.memory_id=c.memory_id) FROM fixture_child c JOIN memories m ON m.id=c.memory_id LEFT JOIN fixture_parent p ON p.id=c.parent_id`).Row().Scan(&missing, &complete))
	require.EqualValues(t, 1, missing, "the negative fixture has a real project parent and a NULL intermediate FK")
	require.EqualValues(t, 1, complete, "the control has both real, project-owned parents")
	v, err := vaultcrypto.OpenExistingVault(&config.Config{EncryptionKey: strings.Repeat("ab", 32)})
	require.NoError(t, err)
	s := NewProjectConsolidationStore(db, v, nil)
	census, err := s.Census(t.Context(), "nullable-intermediate-fixture")
	require.NoError(t, err)
	var native consolidationCensusState
	require.NoError(t, s.open(census.Reference, "census", &native))
	refs := map[string]string{}
	for _, row := range native.Rows {
		if row.Table != "memories" && row.Table != "fixture_child" && row.Table != "fixture_descendant" {
			continue
		}
		var key struct {
			ID int64 `json:"id"`
		}
		require.NoError(t, json.Unmarshal(row.Key, &key))
		if row.Table == "memories" && row.Column == "project" && key.ID == 19201 {
			refs["memory"] = row.Reference
		}
		if row.Table == "fixture_child" || row.Table == "fixture_descendant" {
			refs[fmt.Sprintf("%s:%d", row.Table, key.ID)] = row.Reference
		}
	}
	for _, key := range []string{"memory", "fixture_child:1", "fixture_child:2", "fixture_descendant:1", "fixture_descendant:2"} {
		require.NotEmpty(t, refs[key], "fixture object %s must be inventoried", key)
	}
	project := uuid.NewString()
	plan, err := s.Preflight(t.Context(), ConsolidationPreflightRequest{CensusReference: census.Reference, CensusFingerprint: census.Fingerprint, Evidence: []ConsolidationEvidence{{Reference: "urn:engram:proof:nullable-parent", Kind: "portable_marker", Group: "urn:engram:group:nullable-parent", ProjectID: project, Rows: []string{refs["memory"]}}}})
	require.NoError(t, err)
	mappings := map[string]ConsolidationMapping{}
	for _, mapping := range plan.Mapping {
		mappings[mapping.Reference] = mapping
	}
	for _, key := range []string{"fixture_child:1", "fixture_descendant:1"} {
		mapping, ok := mappings[refs[key]]
		require.True(t, ok)
		assert.Empty(t, mapping.ProjectID, "%s has incomplete FK ancestry", key)
		assert.NotEmpty(t, mapping.UnresolvedReference, "%s must remain unresolved", key)
	}
	for _, key := range []string{"fixture_child:2", "fixture_descendant:2"} {
		mapping, ok := mappings[refs[key]]
		require.True(t, ok)
		assert.Equal(t, project, mapping.ProjectID, "%s has complete project ancestry", key)
		assert.Empty(t, mapping.UnresolvedReference, "%s must resolve", key)
	}
}

func TestProjectConsolidationExternalMemoryParent(t *testing.T) {
	db, schema := openMemoryContext191UpgradeDB(t)
	require.NoError(t, db.Exec(`CREATE TABLE projects (id TEXT PRIMARY KEY)`).Error)
	require.NoError(t, projectConsolidationMigration186().Migrate(db))
	require.NoError(t, gormigrate.New(db, gormigrate.DefaultOptions, []*gormigrate.Migration{memoryPrincipalsMigration149(), memoryContextMigration191()}).Migrate())
	require.NoError(t, db.Exec(`INSERT INTO memories (id, project, content, context_kind, context_principal, context_principal_kind) VALUES
		(19201, ?, 'project fixture', 'project', '', ''),
		(19202, '', 'self fixture', 'self', 'agent/fixture', 'agent')`, memoryContext191Project).Error)
	external := "memory_context_external_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	require.NoError(t, db.Exec("CREATE SCHEMA "+quoted(external)).Error)
	t.Cleanup(func() { require.NoError(t, db.Exec("DROP SCHEMA "+quoted(external)+" CASCADE").Error) })
	require.NoError(t, db.Exec("CREATE TABLE "+quoted(external)+`.memories (id BIGINT PRIMARY KEY)`).Error)
	require.NoError(t, db.Exec("INSERT INTO "+quoted(external)+`.memories VALUES (19202)`).Error)
	require.NoError(t, db.Exec(`CREATE TABLE external_fk_child (id BIGINT PRIMARY KEY, memory_id BIGINT NOT NULL REFERENCES memories(id), external_id BIGINT NOT NULL REFERENCES `+quoted(external)+`.memories(id))`).Error)
	require.NoError(t, db.Exec(`INSERT INTO external_fk_child VALUES (1, 19202, 19202)`).Error)
	require.NoError(t, db.Exec(`CREATE TABLE project_fk_control (id BIGINT PRIMARY KEY, memory_id BIGINT NOT NULL REFERENCES memories(id))`).Error)
	require.NoError(t, db.Exec(`INSERT INTO project_fk_control VALUES (2, 19201)`).Error)
	var foreignParents int64
	require.NoError(t, db.Raw(`SELECT count(*) FROM pg_constraint c JOIN pg_class child ON child.oid=c.conrelid JOIN pg_namespace cn ON cn.oid=child.relnamespace JOIN pg_class parent ON parent.oid=c.confrelid JOIN pg_namespace pn ON pn.oid=parent.relnamespace WHERE c.contype='f' AND cn.nspname=? AND child.relname='external_fk_child' AND pn.nspname=? AND parent.relname='memories'`, schema, external).Scan(&foreignParents).Error)
	require.EqualValues(t, 1, foreignParents, "the second FK must point to a different schema with the same parent relation name")
	v, err := vaultcrypto.OpenExistingVault(&config.Config{EncryptionKey: strings.Repeat("ab", 32)})
	require.NoError(t, err)
	s := NewProjectConsolidationStore(db, v, nil)
	census, err := s.Census(t.Context(), "external-parent-fixture")
	require.NoError(t, err)
	var native consolidationCensusState
	require.NoError(t, s.open(census.Reference, "census", &native))
	var projectRef, childRef, controlRef string
	for _, row := range native.Rows {
		switch row.Table {
		case "memories":
			var key struct {
				ID int64 `json:"id"`
			}
			require.NoError(t, json.Unmarshal(row.Key, &key))
			if key.ID == 19201 && row.Column == "project" {
				projectRef = row.Reference
			}
		case "external_fk_child":
			childRef = row.Reference
		case "project_fk_control":
			controlRef = row.Reference
		}
	}
	require.NotEmpty(t, projectRef)
	require.NotEmpty(t, controlRef)
	target := uuid.NewString()
	plan, err := s.Preflight(t.Context(), ConsolidationPreflightRequest{CensusReference: census.Reference, CensusFingerprint: census.Fingerprint, Evidence: []ConsolidationEvidence{{Reference: "urn:engram:proof:external-fixture", Kind: "portable_marker", Group: "urn:engram:group:external-fixture", ProjectID: target, Rows: []string{projectRef}}}})
	require.NoError(t, err)
	var childMapping, controlMapping ConsolidationMapping
	for _, mapping := range plan.Mapping {
		if mapping.Reference == childRef && childRef != "" {
			childMapping = mapping
		}
		if mapping.Reference == controlRef {
			controlMapping = mapping
		}
	}
	assert.NotEmpty(t, childRef, "an unenumerated cross-schema parent must not cause a self-linked child to disappear")
	if childRef != "" {
		assert.Empty(t, childMapping.ProjectID)
		assert.NotEmpty(t, childMapping.UnresolvedReference, "the external parent has unknown ownership")
	}
	assert.Equal(t, target, controlMapping.ProjectID, "an ordinary project child must still inherit")
	assert.Empty(t, controlMapping.UnresolvedReference)
}

func TestProjectConsolidationTypedMemoryBackupVerification(t *testing.T) {
	sourceDSN, targetDSN := os.Getenv("ENGRAM_SEARCH_PRIVACY_TEST_DSN"), os.Getenv("ENGRAM_B1_CENSUS_TARGET_DSN")
	if sourceDSN == "" || targetDSN == "" {
		t.Skip("explicit disposable source and tagged target DSNs are required")
	}
	sourceConfig, err := pgx.ParseConfig(sourceDSN)
	if err != nil {
		t.Fatal("invalid source fixture configuration; value withheld")
	}
	targetConfig, err := pgx.ParseConfig(targetDSN)
	if err != nil {
		t.Fatal("invalid target fixture configuration; value withheld")
	}
	require.Equal(t, "127.0.0.1", targetConfig.Host)
	require.Equal(t, sourceConfig.Host, targetConfig.Host)
	require.Equal(t, sourceConfig.Port, targetConfig.Port)
	require.Equal(t, sourceConfig.User, targetConfig.User)
	require.NotEqual(t, sourceConfig.Database, targetConfig.Database)
	require.True(t, strings.HasPrefix(targetConfig.Database, "b1_census_restore_"))
	sourceAdmin := stdlib.OpenDB(*sourceConfig)
	defer sourceAdmin.Close()
	var sourceOID, sourceOwner, sourceRole int64
	var sourceSystem string
	require.NoError(t, sourceAdmin.QueryRowContext(t.Context(), `SELECT d.oid,d.datdba,(SELECT oid FROM pg_roles WHERE rolname=current_user),(SELECT system_identifier::text FROM pg_control_system()) FROM pg_database d WHERE d.datname=current_database()`).Scan(&sourceOID, &sourceOwner, &sourceRole, &sourceSystem))
	require.Equal(t, sourceOwner, sourceRole, "source fixture role must own the source database")
	targetAdmin := stdlib.OpenDB(*targetConfig)
	defer targetAdmin.Close()
	var targetOID, ownerOID int64
	var systemID, tag string
	require.NoError(t, targetAdmin.QueryRowContext(t.Context(), `SELECT d.oid,d.datdba,(SELECT system_identifier::text FROM pg_control_system()),COALESCE(shobj_description(d.oid,'pg_database'),'') FROM pg_database d WHERE d.datname=current_database()`).Scan(&targetOID, &ownerOID, &systemID, &tag))
	require.Equal(t, os.Getenv("ENGRAM_B1_CENSUS_TARGET_OID"), fmt.Sprint(targetOID))
	require.Equal(t, sourceRole, ownerOID)
	require.Equal(t, sourceSystem, systemID)
	require.NotEqual(t, sourceOID, targetOID)
	require.NotEqualValues(t, 5, targetOID)
	require.Equal(t, "b1-census-owned:"+strings.TrimPrefix(targetConfig.Database, "b1_census_restore_"), tag)
	source, sourceSchema := openMemoryContext191UpgradeDB(t)
	t.Setenv("ENGRAM_SEARCH_PRIVACY_TEST_DSN", targetDSN)
	target, targetSchema := openMemoryContext191UpgradeDB(t)
	t.Setenv("ENGRAM_SEARCH_PRIVACY_TEST_DSN", sourceDSN)
	targetSQL, err := target.DB()
	require.NoError(t, err)
	targetSQL.SetMaxOpenConns(1)
	require.NoError(t, target.Exec("ALTER SCHEMA "+quoted(targetSchema)+" RENAME TO "+quoted(sourceSchema)).Error)
	t.Cleanup(func() {
		require.NoError(t, target.Exec("ALTER SCHEMA "+quoted(sourceSchema)+" RENAME TO "+quoted(targetSchema)).Error)
	})
	require.NoError(t, target.Exec("SET search_path TO "+quoted(sourceSchema)).Error)
	for _, db := range []*gormlib.DB{source, target} {
		require.NoError(t, db.Exec(`CREATE TABLE projects (id TEXT PRIMARY KEY)`).Error)
		require.NoError(t, projectConsolidationMigration186().Migrate(db))
		require.NoError(t, gormigrate.New(db, gormigrate.DefaultOptions, []*gormigrate.Migration{memoryPrincipalsMigration149(), memoryContextMigration191()}).Migrate())
		require.NoError(t, db.Exec(`CREATE TABLE content_chunks (id BIGSERIAL PRIMARY KEY, memory_id BIGINT NOT NULL REFERENCES memories(id), seq INTEGER NOT NULL, text TEXT NOT NULL DEFAULT '', embedding public.vector(4096), model TEXT NOT NULL, created_at TIMESTAMPTZ NOT NULL DEFAULT now(), UNIQUE(memory_id,seq))`).Error)
	}
	var fixtureMemories, fixtureUsers int64
	require.NoError(t, target.Raw(`SELECT count(*) FROM memories WHERE id IN (19101,19102)`).Scan(&fixtureMemories).Error)
	require.NoError(t, target.Raw(`SELECT count(*) FROM users WHERE id=17`).Scan(&fixtureUsers).Error)
	require.EqualValues(t, 2, fixtureMemories)
	require.EqualValues(t, 1, fixtureUsers)
	for _, statement := range []string{`DELETE FROM memories WHERE id IN (19101,19102)`, `DELETE FROM users WHERE id=17`} {
		result := target.Exec(statement)
		require.NoError(t, result.Error)
	}
	tables, err := consolidationDataTables(target)
	require.NoError(t, err)
	for _, table := range tables {
		if table.Name == "migrations" {
			continue
		}
		var count int64
		require.NoError(t, target.Table(table.Name).Count(&count).Error)
		require.Zero(t, count, "owned target table %s must be empty before verification", table.Name)
	}
	var sourceMigrations, targetMigrations string
	require.NoError(t, source.Raw(`SELECT COALESCE(jsonb_agg(to_jsonb(t) ORDER BY to_jsonb(t)::text),'[]'::jsonb)::text FROM migrations t`).Scan(&sourceMigrations).Error)
	require.NoError(t, target.Raw(`SELECT COALESCE(jsonb_agg(to_jsonb(t) ORDER BY to_jsonb(t)::text),'[]'::jsonb)::text FROM migrations t`).Scan(&targetMigrations).Error)
	require.Equal(t, sourceMigrations, targetMigrations, "target migration control must match source")
	sourceSequences, err := consolidationSequences(source)
	require.NoError(t, err)
	targetSequences, err := consolidationSequences(target)
	require.NoError(t, err)
	require.Equal(t, sourceSequences, targetSequences, "target sequences must initially match source")
	t.Log("target validated: all non-migration tables empty, migration rows and sequences match")
	require.NoError(t, source.Exec(`INSERT INTO memories (id, project, content, context_kind, context_principal, context_principal_kind) VALUES
		(19201, ?, 'project fixture', 'project', '', ''), (19202, '', 'self fixture', 'self', 'agent/fixture', 'agent'), (19203, '', 'global fixture', 'global', '', '')`, memoryContext191Project).Error)
	require.NoError(t, source.Exec(`INSERT INTO content_chunks (id,memory_id,seq,text,model) VALUES (19201,19201,0,'project','fixture'),(19202,19202,0,'self','fixture'),(19203,19203,0,'global','fixture')`).Error)
	require.NoError(t, source.Exec(`INSERT INTO memory_revisions (memory_id,version,content,changed_by_principal,changed_by_principal_kind) VALUES (19201,1,'project','agent/fixture','agent'),(19202,1,'self','agent/fixture','agent'),(19203,1,'global','agent/fixture','agent')`).Error)
	require.NoError(t, source.Exec(`INSERT INTO memory_request_keys (principal_kind,principal,context_kind,project,request_key,request_digest,memory_id) VALUES ('agent','agent/fixture','project',?,'project',?,19201),('agent','agent/fixture','self','','self',?,19202),('agent','agent/fixture','global','','global',?,19203)`, memoryContext191Project, strings.Repeat("a", 64), strings.Repeat("b", 64), strings.Repeat("c", 64)).Error)
	before := map[string]string{}
	for _, table := range []string{"memories", "content_chunks", "memory_revisions", "memory_request_keys"} {
		var value string
		require.NoError(t, source.Raw("SELECT COALESCE(jsonb_agg(to_jsonb(t) ORDER BY to_jsonb(t)::text),'[]'::jsonb)::text FROM "+table+" t").Scan(&value).Error)
		before[table] = value
	}
	sourceShape, _, _, err := consolidationSchema(source)
	require.NoError(t, err)
	targetShape, _, _, err := consolidationSchema(target)
	require.NoError(t, err)
	require.Equal(t, sourceShape, targetShape, "fixture source and target schemas must match before testing backup")
	t.Log("fixture setup complete: matching schemas and real typed dependent rows")
	v, err := vaultcrypto.OpenExistingVault(&config.Config{EncryptionKey: strings.Repeat("ab", 32)})
	require.NoError(t, err)
	s := NewProjectConsolidationStore(source, v, target)
	census, err := s.Census(t.Context(), "typed-memory-backup-fixture")
	require.NoError(t, err)
	var native consolidationCensusState
	require.NoError(t, s.open(census.Reference, "census", &native))
	var projectRef string
	for _, row := range native.Rows {
		if row.Table == "memories" && row.Column == "project" {
			var key struct {
				ID int64 `json:"id"`
			}
			require.NoError(t, json.Unmarshal(row.Key, &key))
			if key.ID == 19201 {
				projectRef = row.Reference
			}
		}
	}
	require.NotEmpty(t, projectRef)
	projectID := uuid.NewString()
	plan, err := s.Preflight(t.Context(), ConsolidationPreflightRequest{CensusReference: census.Reference, CensusFingerprint: census.Fingerprint, Evidence: []ConsolidationEvidence{{Reference: "urn:engram:proof:backup-fixture", Kind: "portable_marker", Group: "urn:engram:group:backup-fixture", ProjectID: projectID, Rows: []string{projectRef}}}})
	require.NoError(t, err)
	for _, mapping := range plan.Mapping {
		if mapping.Reference == projectRef {
			require.Equal(t, projectID, mapping.ProjectID)
		}
	}
	var sealed bytes.Buffer
	require.NoError(t, s.Backup(t.Context(), plan.Reference, plan.Fingerprint, &sealed), "valid source and plan must produce authenticated backup frames")
	require.NotZero(t, sealed.Len())
	t.Log("authenticated source backup produced; exercising VerifyBackup")
	_, err = s.VerifyBackup(t.Context(), plan.Reference, plan.Fingerprint, bytes.NewReader(sealed.Bytes()))
	assert.NoError(t, err, "a backup containing non-project memories and their dependents must verify")
	for table, expected := range before {
		var after string
		require.NoError(t, source.Raw("SELECT COALESCE(jsonb_agg(to_jsonb(t) ORDER BY to_jsonb(t)::text),'[]'::jsonb)::text FROM "+table+" t").Scan(&after).Error)
		assert.Equal(t, expected, after, "%s source records must remain unchanged", table)
	}
}

func consolidationFixtureStore(t *testing.T) *streamIntegrityFixture {
	t.Helper()
	if os.Getenv("ENGRAM_UCI_RESTORE_FIXTURE") != "" {
		return openUCIBackupRestoreStoreFixture(t)
	}
	return openStreamIntegrityFixture(t, streamIntegrityOracle, "stream_workflow_"+strings.ReplaceAll(uuid.NewString(), "-", ""), true)
}

// This single workflow runs real PostgreSQL constraints, complete source schema,
// Vault encryption/decryption, ordinary readers, restore, row updates and replay.
// Its generated corpus is not the owner's corpus or installed acceptance.
func TestProjectConsolidationRealStoreWorkflow(t *testing.T) {
	f := consolidationFixtureStore(t)
	s, db, target, ctx, output := f.store, f.source, f.target, f.ctx, f.directory
	sequenceBefore, err := consolidationSequences(target)
	require.NoError(t, err)
	require.NotEmpty(t, sequenceBefore)
	sequenceTx := target.Begin()
	require.NoError(t, sequenceTx.Error)
	sequenceProbe := sequenceBefore[0]
	sequenceProbe.IsCalled = !sequenceProbe.IsCalled
	require.NoError(t, consolidationSetSequence(sequenceTx, sequenceProbe))
	require.NoError(t, sequenceTx.Rollback().Error)
	sequenceAfter, err := consolidationSequences(target)
	require.NoError(t, err)
	require.Equal(t, sequenceBefore, sequenceAfter, "restore rollback must retain sequence value and is_called")
	seedDB := db
	seedTx := db.Begin()
	require.NoError(t, seedTx.Error)
	defer seedTx.Rollback()
	require.NoError(t, consolidationLock(seedTx))
	restoreSeedGuards, err := consolidationSuspendWriteGuards(seedTx)
	require.NoError(t, err)
	db = seedTx
	a, b := uuid.NewString(), uuid.NewString()
	for _, id := range []string{"mixed", "other"} {
		require.NoError(t, db.Create(&Project{ID: id, DisplayName: sql.NullString{String: "Retained " + id, Valid: true}}).Error)
	}
	sessions := []SDKSession{{ClaudeSessionID: "fixture-a", SDKSessionID: sql.NullString{String: "fixture-sdk-a", Valid: true}, Project: "mixed"}, {ClaudeSessionID: "fixture-b", SDKSessionID: sql.NullString{String: "fixture-sdk-b", Valid: true}, Project: "mixed"}}
	for i := range sessions {
		require.NoError(t, db.Create(&sessions[i]).Error)
	}
	memories := []Memory{{Project: "mixed", Content: "fixture memory a", PrivacyScope: "private", OwnerPrincipal: "fixture-owner", OwnerPrincipalKind: "human", AgentVisibility: "private", SourceSessions: pq.StringArray{"fixture-sdk-a"}, Version: 3}, {Project: "mixed", Content: "fixture memory b", PrivacyScope: "project", SourceSessions: pq.StringArray{"fixture-sdk-b"}, Version: 2}, {Project: "mixed", Content: "fixture unresolved", PrivacyScope: "project", Version: 1}}
	for i := range memories {
		require.NoError(t, db.Create(&memories[i]).Error)
	}
	var chunkID int64
	embedding := "[" + strings.Repeat("0,", 1535) + "1]"
	require.NoError(t, db.Raw(`INSERT INTO content_chunks(memory_id,seq,text,embedding,model) VALUES(?,0,'fixture unchanged chunk',?::vector,'fixture-model') RETURNING id`, memories[0].ID, embedding).Scan(&chunkID).Error)
	require.NoError(t, db.Create(&AuditLogEntry{MemoryID: &memories[0].ID, Action: "fixture_history", Actor: "fixture-owner", BeforeState: rawConsolidationJSON(`{"project":"mixed","version":2}`)}).Error)
	doc := VersionedDocument{Path: "fixture.md", Project: "mixed", Version: 2, Content: "fixture document", ContentHash: "fixture-content", Author: "fixture-owner", Metadata: `{}`}
	require.NoError(t, db.Create(&doc).Error)
	comment := VersionedDocumentComment{DocumentID: doc.ID, Author: "fixture-owner", Content: "fixture comment", Status: "open"}
	require.NoError(t, db.Create(&comment).Error)
	secret, err := s.vault.Encrypt("synthetic private fixture value")
	require.NoError(t, err)
	credentials := NewCredentialStore(&Store{DB: db})
	for _, project := range []string{"mixed", "other", ""} {
		scope := "project"
		if project == "" {
			scope = "global"
		}
		_, err := credentials.Create(ctx, &models.Credential{Project: project, Key: "collision-key", EncryptedSecret: secret, EncryptionKeyFingerprint: s.vault.Fingerprint(), Scope: scope, Version: 4})
		require.NoError(t, err)
	}
	// Two actual independent Checkout/View objects of one Source. Migration only
	// adds membership; snapshot and analysis/embedding addresses stay untouched.
	now := time.Now().UTC()
	sourceID := uuid.NewString()
	profileID := uuid.NewString()
	digest := "sha256:" + strings.Repeat("a", 64)
	require.NoError(t, db.Create(&UCISource{SourceID: sourceID, AuthRealm: "fixture", Kind: UCISourceGit, DisplayName: "same display name", State: UCISourceActive, CreatedAt: now, UpdatedAt: now}).Error)
	unresolvedSourceID := uuid.NewString()
	require.NoError(t, db.Create(&UCISource{SourceID: unresolvedSourceID, AuthRealm: "fixture", Kind: UCISourceGit, DisplayName: "same display name", State: UCISourceActive, CreatedAt: now, UpdatedAt: now}).Error)
	require.NoError(t, db.Create(&UCIAnalysisProfile{ProfileID: profileID, ParserBundleDigest: digest, ResolverRevision: "fixture", ChunkerRevision: "fixture", IgnorePolicyDigest: digest, BuildContextJSON: `{}`, SecretPolicyRevision: "fixture", CreatedAt: now}).Error)
	for i := range 2 {
		checkoutID, viewID, incarnationID := uuid.NewString(), uuid.NewString(), uuid.NewString()
		require.NoError(t, db.Create(&UCICheckout{CheckoutID: checkoutID, SourceID: sourceID, WorkstationID: "fixture", IncarnationID: incarnationID, Kind: UCICheckoutWorkingTree, OwnerPrincipal: "fixture-owner", LocatorRef: "urn:engram:fixture:checkout:" + string(rune('a'+i)), State: UCICheckoutRegistered, LeaseEpoch: 1, CreatedAt: now, UpdatedAt: now}).Error)
		require.NoError(t, db.Create(&UCIView{ViewID: viewID, CheckoutID: checkoutID, SourceID: sourceID, IncarnationID: incarnationID, Generation: 1, ProfileID: profileID, Dirty: true, ScanStart: now, ScanEnd: now, ManifestDigest: digest, CoverageJSON: `{}`, State: UCIViewStaging, CreatedAt: now, UpdatedAt: now}).Error)
	}
	require.NoError(t, restoreSeedGuards())
	require.NoError(t, seedTx.Commit().Error)
	db = seedDB
	before, err := consolidationSnapshot(db)
	require.NoError(t, err)
	census, err := s.Census(ctx, "d14a4a7+owned-row-migration-projection")
	require.NoError(t, err)
	var native consolidationCensusState
	require.NoError(t, s.open(census.Reference, "census", &native))
	refsA, refsB := []string{}, []string{}
	for _, row := range native.Rows {
		var key map[string]any
		require.NoError(t, json.Unmarshal(row.Key, &key))
		switch row.Table {
		case "memories":
			require.NotEqual(t, "context_kind", row.Column, "internal owning-context metadata must never become a selector")
			if row.Column == "project" {
				id := int64(key["id"].(float64))
				if id == memories[0].ID {
					refsA = append(refsA, row.Reference)
				}
				if id == memories[1].ID {
					refsB = append(refsB, row.Reference)
				}
			}
		case "sdk_sessions":
			if row.Column == "project" {
				id := int64(key["id"].(float64))
				if id == sessions[0].ID {
					refsA = append(refsA, row.Reference)
				} else {
					refsB = append(refsB, row.Reference)
				}
			}
		case "credentials":
			if row.Selector != "" {
				refsA = append(refsA, row.Reference)
			}
		case "versioned_documents":
			if row.Column == "project" {
				refsA = append(refsA, row.Reference)
			}
		case "sources":
			if key["source_id"] == sourceID {
				refsA = append(refsA, row.Reference)
			}
		}
	}
	evidence := []ConsolidationEvidence{{Reference: "urn:engram:fixture:marker:a", Kind: "portable_marker", Group: "urn:engram:fixture:group:a", ProjectID: a, Rows: refsA}, {Reference: "urn:engram:fixture:marker:b", Kind: "portable_marker", Group: "urn:engram:fixture:group:b", ProjectID: b, Rows: refsB}}
	plan, err := s.Preflight(ctx, ConsolidationPreflightRequest{census.Reference, census.Fingerprint, evidence})
	require.NoError(t, err)
	plan2, err := s.Preflight(ctx, ConsolidationPreflightRequest{census.Reference, census.Fingerprint, evidence})
	require.NoError(t, err)
	require.Equal(t, plan.Fingerprint, plan2.Fingerprint)
	require.Equal(t, plan.Mapping, plan2.Mapping)
	backupFile, err := os.OpenFile(filepath.Join(output, "fixture-backup.sealed"), os.O_CREATE|os.O_EXCL|os.O_RDWR, 0o600)
	require.NoError(t, err)
	defer backupFile.Close()
	require.NoError(t, s.Backup(ctx, plan.Reference, plan.Fingerprint, backupFile))
	emptyTarget, err := consolidationSnapshot(target)
	require.NoError(t, err)
	backupInfo, err := backupFile.Stat()
	require.NoError(t, err)
	_, err = backupFile.Seek(0, io.SeekStart)
	require.NoError(t, err)
	_, err = s.VerifyBackup(ctx, plan.Reference, plan.Fingerprint, io.LimitReader(backupFile, backupInfo.Size()-1))
	require.EqualError(t, err, "PROJECT_ADOPTION_BACKUP_TRUNCATED")
	require.NoError(t, consolidationCompare(target, emptyTarget))
	var retainedFrames int64
	require.NoError(t, target.Model(&consolidationStoredFrame{}).Count(&retainedFrames).Error)
	require.Zero(t, retainedFrames, "partial ciphertext import must roll back")
	_, err = backupFile.Seek(0, io.SeekStart)
	require.NoError(t, err)
	cancelledCtx, cancelImport := context.WithCancel(ctx)
	_, err = s.VerifyBackup(cancelledCtx, plan.Reference, plan.Fingerprint, &consolidationCancelReader{Reader: backupFile, Cancel: cancelImport, Remaining: backupInfo.Size() / 2})
	cancelImport()
	require.Error(t, err, "cancelled import must not commit ciphertext frames or restored rows")
	require.NoError(t, consolidationCompare(target, emptyTarget))
	require.NoError(t, target.Model(&consolidationStoredFrame{}).Count(&retainedFrames).Error)
	require.Zero(t, retainedFrames)
	_, err = backupFile.Seek(0, io.SeekStart)
	require.NoError(t, err)
	backupRef, err := s.VerifyBackup(ctx, plan.Reference, plan.Fingerprint, backupFile)
	require.NoError(t, err)
	var verifiedFrameCount, verifiedBackupCount int64
	require.NoError(t, target.Model(&consolidationStoredFrame{}).Count(&verifiedFrameCount).Error)
	require.NoError(t, target.Model(&consolidationBackupRow{}).Count(&verifiedBackupCount).Error)
	require.Positive(t, verifiedFrameCount)
	require.Equal(t, int64(1), verifiedBackupCount)
	_, err = backupFile.Seek(0, io.SeekStart)
	require.NoError(t, err)
	repeatedRef, err := s.VerifyBackup(ctx, plan.Reference, plan.Fingerprint, backupFile)
	require.NoError(t, err)
	require.Equal(t, backupRef, repeatedRef)
	require.NoError(t, target.Model(&consolidationStoredFrame{}).Count(&retainedFrames).Error)
	require.Equal(t, verifiedFrameCount, retainedFrames, "verified replay must not duplicate frames")
	require.NoError(t, consolidationCompare(target, before))
	request := ConsolidationApplyRequest{PlanReference: plan.Reference, PlanFingerprint: plan.Fingerprint, BackupReference: backupRef, IdempotencyKey: uuid.NewString()}
	_, err = s.DryRun(ctx, request)
	require.EqualError(t, err, "PROJECT_ADOPTION_COLLISION")
	require.NoError(t, consolidationCompare(target, before))
	// The collision is a plan correction, not removal/deduplication: keep the
	// second credential unresolved in its original namespace.
	for i := range evidence[0].Rows {
		for _, row := range native.Rows {
			if row.Reference == evidence[0].Rows[i] && row.Table == "credentials" && row.Selector == "other" {
				evidence[0].Rows[i] = ""
			}
		}
	}
	var corrected []string
	for _, ref := range evidence[0].Rows {
		if ref != "" {
			corrected = append(corrected, ref)
		}
	}
	evidence[0].Rows = corrected
	plan, err = s.Preflight(ctx, ConsolidationPreflightRequest{census.Reference, census.Fingerprint, evidence})
	require.NoError(t, err)
	// Ordinary backup/restore verification reuses an unchanged verified target
	// for the corrected plan; no direct writes to migration receipts.
	correctedFile, err := os.OpenFile(filepath.Join(output, "fixture-backup-corrected.sealed"), os.O_CREATE|os.O_EXCL|os.O_RDWR, 0o600)
	require.NoError(t, err)
	defer correctedFile.Close()
	require.NoError(t, s.Backup(ctx, plan.Reference, plan.Fingerprint, correctedFile))
	_, err = correctedFile.Seek(0, io.SeekStart)
	require.NoError(t, err)
	backupRef, err = s.VerifyBackup(ctx, plan.Reference, plan.Fingerprint, correctedFile)
	require.NoError(t, err)
	request.PlanReference, request.PlanFingerprint, request.BackupReference = plan.Reference, plan.Fingerprint, backupRef
	_, err = s.DryRun(ctx, request)
	require.NoError(t, err)
	require.NoError(t, consolidationCompare(target, before))
	receipt, err := s.Apply(ctx, request)
	require.NoError(t, err)
	replayed, err := s.Apply(ctx, request)
	require.NoError(t, err)
	require.Equal(t, receipt, replayed)
	reader := NewCredentialStore(&Store{DB: target})
	global, err := reader.Get(ctx, "", "collision-key")
	require.NoError(t, err)
	require.Equal(t, "global", global.Scope)
	plain, err := s.vault.Decrypt(global.EncryptedSecret)
	require.NoError(t, err)
	require.Equal(t, "synthetic private fixture value", plain)
	var persisted []Memory
	require.NoError(t, target.Order("id").Find(&persisted).Error)
	require.Len(t, persisted, 3)
	require.Equal(t, a, persisted[0].Project)
	require.Equal(t, b, persisted[1].Project)
	require.Equal(t, "mixed", persisted[2].Project)
	require.Equal(t, memories[0].Version, persisted[0].Version)
	require.Equal(t, memories[0].OwnerPrincipal, persisted[0].OwnerPrincipal)
	var unresolved string
	for _, m := range plan.Mapping {
		if m.Family == "memories" && m.ProjectID == "" {
			unresolved = m.UnresolvedReference
		}
	}
	targetReader := NewProjectConsolidationStore(target, s.vault, nil)
	var retainedCredentialCount int
	for _, mapping := range plan.Mapping {
		if mapping.Family != "credentials" || mapping.UnresolvedReference == "" {
			continue
		}
		members, err := targetReader.UnresolvedMembership(ctx, mapping.UnresolvedReference, "credential")
		require.NoError(t, err)
		for _, member := range members {
			if member.Reference != mapping.Reference {
				continue
			}
			ids, err := RetainedContentIDs([]ConsolidationMembership{member})
			require.NoError(t, err)
			require.Len(t, ids, 1)
			credential, err := reader.GetByID(ctx, ids[0])
			require.NoError(t, err)
			require.NotEmpty(t, credential.Project)
			require.Equal(t, "project", credential.Scope)
			require.Equal(t, secret, credential.EncryptedSecret)
			decoded, err := s.vault.Decrypt(credential.EncryptedSecret)
			require.NoError(t, err)
			require.True(t, decoded == "synthetic private fixture value")
			retainedCredentialCount++
		}
	}
	require.Equal(t, 1, retainedCredentialCount)
	membership, err := targetReader.UnresolvedMembership(ctx, unresolved, "memory")
	require.NoError(t, err)
	require.Len(t, membership, 1)
	legacyRef, err := targetReader.LegacyUnresolved(ctx, "mixed")
	require.NoError(t, err)
	require.Equal(t, unresolved, legacyRef)
	_, err = targetReader.LegacyUnresolved(ctx, "not-inventoried")
	require.EqualError(t, err, "PROJECT_ADOPTION_UNKNOWN_REFERENCE")
	_, err = targetReader.UnresolvedMembership(ctx, "mixed", "memory")
	require.EqualError(t, err, "PROJECT_ADOPTION_UNKNOWN_REFERENCE")
	_, err = targetReader.UnresolvedMembership(ctx, "urn:engram:migration:unresolved:"+strings.Repeat("0", 64), "memory")
	require.EqualError(t, err, "PROJECT_ADOPTION_UNKNOWN_REFERENCE")
	foreignVault, err := vaultcrypto.OpenExistingVault(&config.Config{EncryptionKey: strings.Repeat("cd", 32)})
	require.NoError(t, err)
	_, err = NewProjectConsolidationStore(target, foreignVault, nil).UnresolvedMembership(ctx, unresolved, "memory")
	require.EqualError(t, err, "PROJECT_ADOPTION_UNKNOWN_REFERENCE")
	unresolvedItems, err := targetReader.ListUnresolved(ctx)
	require.NoError(t, err)
	var codeRef string
	for _, item := range unresolvedItems {
		if item.Reference == unresolved {
			require.Equal(t, "Retained mixed", item.Name)
			require.Contains(t, item.Reason, "missing_membership_evidence")
		}
		if item.Name == "same display name" {
			codeRef = item.Reference
			require.Equal(t, "missing_membership_evidence", item.Reason)
		}
	}
	require.NotEmpty(t, codeRef)
	codeMembership, err := targetReader.UnresolvedMembership(ctx, codeRef, "code")
	require.NoError(t, err)
	require.Len(t, codeMembership, 1)
	require.JSONEq(t, `{"source_id":"`+unresolvedSourceID+`"}`, string(codeMembership[0].RowKey))
	canonicalSources, err := targetReader.CanonicalMembership(ctx, a, "sources")
	require.NoError(t, err)
	require.Len(t, canonicalSources, 1)
	require.JSONEq(t, `{"source_id":"`+sourceID+`"}`, string(canonicalSources[0].RowKey))
	_, err = targetReader.UnresolvedMembership(ctx, codeRef, "memory")
	require.EqualError(t, err, "PROJECT_ADOPTION_UNKNOWN_REFERENCE")
	require.NoError(t, target.Exec(`UPDATE memories SET content='fixture edited body' WHERE id=?`, memories[0].ID).Error)
	_, err = targetReader.UnresolvedMembership(ctx, unresolved, "memory")
	require.NoError(t, err)
	_, err = targetReader.ResolveLegacy(ctx, "mixed", "")
	require.EqualError(t, err, "PROJECT_ADOPTION_UNRESOLVED_MEMBERSHIP")
	resolved, err := targetReader.ResolveLegacy(ctx, "mixed", "fixture-sdk-a")
	require.NoError(t, err)
	require.Equal(t, a, resolved)
	require.NoError(t, target.Exec(`INSERT INTO memories(project,content,source_sessions) VALUES('mixed','fixture live-session write',ARRAY['fixture-sdk-a'])`).Error)
	_, err = s.Apply(ctx, request)
	require.EqualError(t, err, "PROJECT_ADOPTION_STALE_PLAN")
	require.NoError(t, target.Exec(`UPDATE memories SET project=? WHERE id=?`, a, memories[2].ID).Error)
	_, err = targetReader.UnresolvedMembership(ctx, unresolved, "memory")
	require.EqualError(t, err, "PROJECT_ADOPTION_STALE_REFERENCE")
	for _, table := range []string{"ci_checkouts", "ci_views", "ci_profiles", "audit_log", "versioned_document_comments", "content_chunks"} {
		require.NoError(t, consolidationCompareTable(target, table, before[table]))
	}
	var views []ConsolidationMembership
	views, err = targetReader.CanonicalMembership(ctx, a, "ci_views")
	require.NoError(t, err)
	require.Len(t, views, 2)
	chunks, err := targetReader.CanonicalMembership(ctx, a, "content_chunks")
	require.NoError(t, err)
	require.Len(t, chunks, 1)
	chunkKey, err := json.Marshal(map[string]int64{"id": chunkID})
	require.NoError(t, err)
	require.JSONEq(t, string(chunkKey), string(chunks[0].RowKey))
	var chunkParentProject string
	require.NoError(t, target.Raw(`SELECT m.project FROM content_chunks c JOIN memories m ON m.id=c.memory_id WHERE c.id=?`, chunkID).Scan(&chunkParentProject).Error)
	require.Equal(t, a, chunkParentProject)
	require.NoError(t, target.Exec(`UPDATE sources SET source_id=? WHERE source_id=?`, uuid.NewString(), unresolvedSourceID).Error)
	_, err = targetReader.UnresolvedMembership(ctx, codeRef, "code")
	require.EqualError(t, err, "PROJECT_ADOPTION_STALE_REFERENCE")
	concurrentWriter := &consolidationFirstWrite{before: func() error {
		_, err := NewMemoryStore(&Store{DB: db}).Create(ctx, &models.Memory{Project: a, Content: "new source row during backup"})
		return err
	}}
	require.EqualError(t, s.Backup(ctx, plan.Reference, plan.Fingerprint, concurrentWriter), "PROJECT_ADOPTION_SEQUENCE_CHANGED")
	require.True(t, concurrentWriter.written, "the source write must occur during the repeatable-read export")
	_, err = correctedFile.Seek(0, io.SeekStart)
	require.NoError(t, err)
	_, err = s.VerifyBackup(ctx, plan.Reference, plan.Fingerprint, correctedFile)
	require.EqualError(t, err, "PROJECT_ADOPTION_STALE_PLAN")
	err = s.Backup(ctx, plan.Reference, plan.Fingerprint, io.Discard)
	require.EqualError(t, err, "PROJECT_ADOPTION_STALE_PLAN")
	// Redacted structural receipt, never source bodies, private DSN or key material.
	proof := map[string]any{"status": "PASS", "scope": "small synthetic real-PG reader/model/conservation slice; NOT full owner streaming", "source_base": "d14a4a74569ec32dd6564bba2dfe146eeb37f7c9", "schema_identity": census.Schema, "snapshot_identity": census.Snapshot, "plan_fingerprint": plan.Fingerprint, "mapping": plan.Mapping, "receipt": receipt, "checks": []string{"current_full_schema_census", "deterministic_plan", "sealed_backup", "isolated_restore", "credential_decrypt", "collision_rollback", "mixed_bucket", "dry_run_rollback", "apply_conservation", "idempotent_replay", "new_write_stale_plan", "opaque_unknown_foreign_stale_reader", "retained_unresolved_labels_reasons", "legacy_unresolved_vs_session_redirect", "code_native_source_id", "source_checkout_view_preservation"}}
	encoded, err := json.MarshalIndent(proof, "", "  ")
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(output, "workflow-receipt.json"), encoded, 0o600))
}

type consolidationCancelReader struct {
	io.Reader
	Cancel    context.CancelFunc
	Remaining int64
}

func (reader *consolidationCancelReader) Read(raw []byte) (int, error) {
	n, err := reader.Reader.Read(raw)
	reader.Remaining -= int64(n)
	if reader.Remaining <= 0 {
		reader.Cancel()
	}
	return n, err
}

func rawConsolidationJSON(raw string) *json.RawMessage { value := json.RawMessage(raw); return &value }
func consolidationCompareTable(db *gormlib.DB, table string, raw json.RawMessage) error {
	var same bool
	if err := db.Raw("SELECT COALESCE(jsonb_agg(to_jsonb(t) ORDER BY to_jsonb(t)::text),'[]'::jsonb)=?::jsonb FROM "+quoted(table)+" t", string(raw)).Scan(&same).Error; err != nil {
		return err
	}
	if !same {
		return consolidationHold("CONSERVATION_FAILED")
	}
	return nil
}

type consolidationFirstWrite struct {
	before  func() error
	written bool
}

func (writer *consolidationFirstWrite) Write(raw []byte) (int, error) {
	if !writer.written {
		writer.written = true
		if err := writer.before(); err != nil {
			return 0, err
		}
	}
	return len(raw), nil
}

// Fixture-only equality helpers. Production census/export/conservation never
// materialize a table; these helpers are not an owner-rehearsal API.
func consolidationSnapshot(db *gormlib.DB) (map[string]json.RawMessage, error) {
	tables, err := consolidationDataTables(db)
	if err != nil {
		return nil, err
	}
	result := map[string]json.RawMessage{}
	for _, table := range tables {
		if consolidationMetadataTable(table.Name) {
			continue // Recovery rows have a separate exact all-table oracle.
		}
		var raw string
		if err := db.Raw("SELECT COALESCE(jsonb_agg(to_jsonb(t) ORDER BY to_jsonb(t)::text),'[]'::jsonb)::text FROM " + quoted(table.Name) + " t").Scan(&raw).Error; err != nil {
			return nil, err
		}
		result[table.Name] = json.RawMessage(raw)
	}
	return result, nil
}

func consolidationCompare(db *gormlib.DB, tables map[string]json.RawMessage) error {
	for table, raw := range tables {
		if err := consolidationCompareTable(db, table, raw); err != nil {
			return err
		}
	}
	return nil
}

// Reuses only the already migrated owned Ops DB. All new objects and migration
// metadata are rolled back; the failed public fixture remains byte-for-byte.
func TestProjectConsolidationReaderStoreSlice(t *testing.T) {
	dsnPath, output := os.Getenv("ENGRAM_CONSOLIDATION_FIXTURE_DSN_FILE"), os.Getenv("ENGRAM_CONSOLIDATION_TEST_OUTPUT")
	if dsnPath == "" || output == "" {
		t.Skip("exact admitted fixture binding absent")
	}
	deadline, err := time.Parse(time.RFC3339, os.Getenv("ENGRAM_CONSOLIDATION_FIXTURE_DEADLINE"))
	require.NoError(t, err)
	require.True(t, time.Now().Before(deadline), "exact fixture admission expired")
	private, err := os.ReadFile(dsnPath)
	require.NoError(t, err, "private fixture DSN unavailable")
	cfg, err := pgx.ParseConfig(strings.TrimSpace(string(private)))
	if err != nil || cfg.Host != "127.0.0.1" || cfg.Port != 22366 || cfg.Database != "identity_fixture" {
		t.Fatal("fixture does not match admitted exact target")
	}
	cfg.Database = "ops_identity_census_01a11827"
	cfg.RuntimeParams["search_path"] = "public"
	cfg.RuntimeParams["application_name"] = "engram_ops_reader_slice"
	pool := stdlib.OpenDB(*cfg)
	pool.SetMaxOpenConns(1)
	defer pool.Close()
	db, err := gormlib.Open(postgres.New(postgres.Config{Conn: pool}), &gormlib.Config{Logger: logger.Default.LogMode(logger.Silent)})
	require.NoError(t, err)
	ctx, cancel := context.WithDeadline(context.Background(), deadline)
	defer cancel()
	db = db.WithContext(ctx)
	original, err := consolidationSnapshot(db)
	require.NoError(t, err)
	v, err := vaultcrypto.OpenExistingVault(&config.Config{EncryptionKeyFile: filepath.Join(output, ".private", "vault.key")})
	require.NoError(t, err)
	tx := db.Begin()
	require.NoError(t, tx.Error)
	defer tx.Rollback()
	require.NoError(t, projectConsolidationMigration186().Migrate(tx), "exercise the current source SQL inside the rollback transaction")
	s := NewProjectConsolidationStore(tx, v, nil)
	a, b, selector := uuid.NewString(), uuid.NewString(), "reader-"+uuid.NewString()
	require.NoError(t, tx.Create(&Project{ID: selector, DisplayName: sql.NullString{String: "Retained reader label", Valid: true}}).Error)
	memories := []Memory{{Project: selector, Content: "reader a", PrivacyScope: "private", OwnerPrincipal: "retained-owner"}, {Project: selector, Content: "reader b", PrivacyScope: "project"}, {Project: selector, Content: "reader unresolved", PrivacyScope: "project"}}
	for i := range memories {
		require.NoError(t, tx.Create(&memories[i]).Error)
	}
	sessionA, sessionB := "reader-sdk-"+uuid.NewString(), "reader-sdk-"+uuid.NewString()
	sessions := []SDKSession{{ClaudeSessionID: sessionA, SDKSessionID: sql.NullString{String: sessionA, Valid: true}, Project: selector}, {ClaudeSessionID: sessionB, SDKSessionID: sql.NullString{String: sessionB, Valid: true}, Project: selector}}
	for i := range sessions {
		require.NoError(t, tx.Create(&sessions[i]).Error)
	}
	now, sourceID, unknownSourceID, profileID := time.Now().UTC(), uuid.NewString(), uuid.NewString(), uuid.NewString()
	for _, id := range []string{sourceID, unknownSourceID} {
		require.NoError(t, tx.Create(&UCISource{SourceID: id, AuthRealm: "fixture", Kind: UCISourceGit, DisplayName: "same reader source label", State: UCISourceActive, CreatedAt: now, UpdatedAt: now}).Error)
	}
	digest := "sha256:" + strings.Repeat("a", 64)
	require.NoError(t, tx.Create(&UCIAnalysisProfile{ProfileID: profileID, ParserBundleDigest: digest, ResolverRevision: "fixture", ChunkerRevision: "fixture", IgnorePolicyDigest: digest, BuildContextJSON: `{}`, SecretPolicyRevision: "fixture", CreatedAt: now}).Error)
	for i := range 2 {
		checkoutID, viewID, incarnationID := uuid.NewString(), uuid.NewString(), uuid.NewString()
		require.NoError(t, tx.Create(&UCICheckout{CheckoutID: checkoutID, SourceID: sourceID, WorkstationID: "fixture", IncarnationID: incarnationID, Kind: UCICheckoutWorkingTree, OwnerPrincipal: "fixture-owner", LocatorRef: "urn:engram:fixture:" + checkoutID, State: UCICheckoutRegistered, LeaseEpoch: 1, CreatedAt: now, UpdatedAt: now}).Error)
		require.NoError(t, tx.Create(&UCIView{ViewID: viewID, CheckoutID: checkoutID, SourceID: sourceID, IncarnationID: incarnationID, Generation: int64(i + 1), ProfileID: profileID, Dirty: true, ScanStart: now, ScanEnd: now, ManifestDigest: digest, CoverageJSON: `{}`, State: UCIViewStaging, CreatedAt: now, UpdatedAt: now}).Error)
	}
	before, err := consolidationSnapshot(tx)
	require.NoError(t, err)
	census, err := s.census(tx, "first-reader-slice")
	require.NoError(t, err)
	refsA, refsB := []string{}, []string{}
	for _, row := range census.Rows {
		var key map[string]any
		require.NoError(t, json.Unmarshal(row.Key, &key))
		if (row.Table == "memories" && row.Column == "project" && key["id"] == float64(memories[0].ID)) || (row.Table == "sdk_sessions" && row.Column == "project" && key["id"] == float64(sessions[0].ID)) || (row.Table == "sources" && key["source_id"] == sourceID) {
			refsA = append(refsA, row.Reference)
		}
		if (row.Table == "memories" && row.Column == "project" && key["id"] == float64(memories[1].ID)) || (row.Table == "sdk_sessions" && row.Column == "project" && key["id"] == float64(sessions[1].ID)) {
			refsB = append(refsB, row.Reference)
		}
	}
	mapping, err := s.buildMapping(census.Rows, []ConsolidationEvidence{{Reference: "urn:engram:fixture:a", Kind: "portable_marker", Group: "urn:engram:fixture:a", ProjectID: a, Rows: refsA}, {Reference: "urn:engram:fixture:b", Kind: "portable_marker", Group: "urn:engram:fixture:b", ProjectID: b, Rows: refsB}})
	require.NoError(t, err)
	receipt, err := s.execute(tx, consolidationPlanState{ID: uuid.NewString(), Fingerprint: census.Fingerprint, Census: census, Mapping: mapping})
	require.NoError(t, err)
	for _, role := range receipt.Roles {
		require.Equal(t, role.Before, role.After)
	}
	unresolved, err := s.LegacyUnresolved(ctx, selector)
	require.NoError(t, err)
	members, err := s.UnresolvedMembership(ctx, unresolved, "memory")
	require.NoError(t, err)
	require.Len(t, members, 1)
	nativeKey, err := json.Marshal(map[string]int64{"id": memories[2].ID})
	require.NoError(t, err)
	require.JSONEq(t, string(nativeKey), string(members[0].RowKey))
	_, err = s.LegacyUnresolved(ctx, "not-inventoried")
	require.EqualError(t, err, "PROJECT_ADOPTION_UNKNOWN_REFERENCE")
	_, err = s.UnresolvedMembership(ctx, selector, "memory")
	require.EqualError(t, err, "PROJECT_ADOPTION_UNKNOWN_REFERENCE")
	_, err = s.UnresolvedMembership(ctx, "urn:engram:migration:unresolved:"+strings.Repeat("0", 64), "memory")
	require.EqualError(t, err, "PROJECT_ADOPTION_UNKNOWN_REFERENCE")
	foreign, err := vaultcrypto.OpenExistingVault(&config.Config{EncryptionKey: strings.Repeat("cd", 32)})
	require.NoError(t, err)
	_, err = NewProjectConsolidationStore(tx, foreign, nil).UnresolvedMembership(ctx, unresolved, "memory")
	require.EqualError(t, err, "PROJECT_ADOPTION_UNKNOWN_REFERENCE")
	items, err := s.ListUnresolved(ctx)
	require.NoError(t, err)
	found := false
	for _, item := range items {
		if item.Reference == unresolved {
			found = true
			require.Equal(t, "Retained reader label", item.Name)
			require.Contains(t, item.Reason, "missing_membership_evidence")
		}
	}
	require.True(t, found)
	var codeRef string
	for i, row := range census.Rows {
		if row.Table == "sources" && bytes.Contains(row.Key, []byte(unknownSourceID)) {
			codeRef = mapping[i].UnresolvedReference
		}
	}
	found = false
	for _, item := range items {
		if item.Reference == codeRef {
			found = true
			require.Equal(t, "same reader source label", item.Name)
			require.Equal(t, "missing_membership_evidence", item.Reason)
		}
	}
	require.True(t, found)
	code, err := s.UnresolvedMembership(ctx, codeRef, "code")
	require.NoError(t, err)
	require.Len(t, code, 1)
	require.JSONEq(t, `{"source_id":"`+unknownSourceID+`"}`, string(code[0].RowKey))
	canonical, err := s.CanonicalMembership(ctx, a, "sources")
	require.NoError(t, err)
	require.Len(t, canonical, 1)
	require.JSONEq(t, `{"source_id":"`+sourceID+`"}`, string(canonical[0].RowKey))
	views, err := s.CanonicalMembership(ctx, a, "ci_views")
	require.NoError(t, err)
	require.Len(t, views, 2)
	_, err = s.ResolveLegacy(ctx, selector, "")
	require.EqualError(t, err, "PROJECT_ADOPTION_UNRESOLVED_MEMBERSHIP")
	var hold *ConsolidationError
	require.ErrorAs(t, err, &hold)
	_, err = s.ResolveLegacy(ctx, selector, "reader-unknown-session")
	require.EqualError(t, err, "PROJECT_ADOPTION_UNRESOLVED_MEMBERSHIP")
	require.ErrorAs(t, err, &hold)
	resolved, err := s.ResolveLegacy(ctx, selector, sessionA)
	require.NoError(t, err)
	require.Equal(t, a, resolved)
	resolved, err = s.ResolveLegacy(ctx, selector, sessionB)
	require.NoError(t, err)
	require.Equal(t, b, resolved)
	resolveScope := func(session string) (string, error) {
		var id string
		err := tx.Transaction(func(probe *gormlib.DB) error {
			return probe.Raw(`SELECT engram_consolidation_scope(?, ?)`, selector, session).Scan(&id).Error
		})
		return id, consolidationReadError(err)
	}
	for _, session := range []string{"", "reader-unknown-session"} {
		resolved, err = resolveScope(session)
		require.Empty(t, resolved)
		require.EqualError(t, err, "PROJECT_ADOPTION_UNRESOLVED_MEMBERSHIP")
		require.ErrorAs(t, err, &hold)
	}
	for session, id := range map[string]string{sessionA: a, sessionB: b} {
		resolved, err = resolveScope(session)
		require.NoError(t, err)
		require.Equal(t, id, resolved)
	}
	var projectsBefore, projectsAfter int64
	require.NoError(t, tx.Table("projects").Count(&projectsBefore).Error)
	for _, identity := range []*ProjectIdentityV2{nil, {Version: ProjectIdentityVersionV2, LegacyProjectID: selector, GitRemote: "https://example.invalid/reader.git"}} {
		resolution, err := RegisterAndResolve(ctx, tx, selector, identity)
		require.Empty(t, resolution.CanonicalProjectID)
		require.EqualError(t, err, "PROJECT_ADOPTION_UNRESOLVED_MEMBERSHIP")
		require.ErrorAs(t, err, &hold)
	}
	require.NoError(t, tx.Table("projects").Count(&projectsAfter).Error)
	require.Equal(t, projectsBefore, projectsAfter, "mixed misses must not register a legacy project")
	legacy := "unmixed-" + uuid.NewString()
	require.NoError(t, tx.Create(&Project{ID: legacy}).Error)
	for _, session := range []string{"", "reader-unknown-session"} {
		require.NoError(t, tx.Raw(`SELECT engram_consolidation_scope(?, ?)`, legacy, session).Scan(&resolved).Error)
		require.Equal(t, legacy, resolved)
	}
	legacyResolution, err := RegisterAndResolve(ctx, tx, legacy, nil)
	require.NoError(t, err)
	require.Equal(t, legacy, legacyResolution.CanonicalProjectID)
	alias := legacy + "-alias"
	require.NoError(t, tx.Exec(`INSERT INTO project_consolidation_redirects(selector,session_id,project_id,plan_fingerprint) VALUES(?, '', ?, 'reader-unmixed')`, alias, a).Error)
	for _, session := range []string{"", "reader-unknown-session"} {
		require.NoError(t, tx.Raw(`SELECT engram_consolidation_scope(?, ?)`, alias, session).Scan(&resolved).Error)
		require.Equal(t, a, resolved)
		resolved, err = s.ResolveLegacy(ctx, alias, session)
		require.NoError(t, err)
		require.Equal(t, a, resolved)
	}
	require.NoError(t, tx.Exec(`INSERT INTO memories(project,content,source_sessions) VALUES(?, 'reader exact-session write', ARRAY[?]::text[])`, selector, sessionA).Error)
	var writtenProject string
	require.NoError(t, tx.Raw(`SELECT project FROM memories WHERE content='reader exact-session write' AND source_sessions=ARRAY[?]::text[]`, sessionA).Scan(&writtenProject).Error)
	require.Equal(t, a, writtenProject, "the trigger must use the row session before a mixed sessionless probe")
	for _, session := range []string{"", "reader-unknown-session"} {
		err = tx.Transaction(func(probe *gormlib.DB) error {
			return probe.Exec(`INSERT INTO memories(project,content,source_sessions) VALUES(?, 'reader refused write', ARRAY[?]::text[])`, selector, session).Error
		})
		require.EqualError(t, consolidationReadError(err), "PROJECT_ADOPTION_UNRESOLVED_MEMBERSHIP")
	}
	for _, table := range []string{"sources", "ci_checkouts", "ci_views", "ci_profiles", "content_chunks", "citation_log", "promotion_log", "knowledge_edges", "rule_arbiter_evaluations"} {
		require.NoError(t, consolidationCompareTable(tx, table, before[table]))
	}
	require.NoError(t, tx.Exec(`UPDATE memories SET content='edited reader body' WHERE id=?`, memories[0].ID).Error)
	_, err = s.UnresolvedMembership(ctx, unresolved, "memory")
	require.NoError(t, err)
	require.NoError(t, tx.Exec(`UPDATE memories SET project=? WHERE id=?`, a, memories[2].ID).Error)
	_, err = s.UnresolvedMembership(ctx, unresolved, "memory")
	require.EqualError(t, err, "PROJECT_ADOPTION_STALE_REFERENCE")
	require.NoError(t, tx.Exec(`UPDATE sources SET source_id=? WHERE source_id=?`, uuid.NewString(), unknownSourceID).Error)
	_, err = s.UnresolvedMembership(ctx, codeRef, "code")
	require.EqualError(t, err, "PROJECT_ADOPTION_STALE_REFERENCE")
	require.NoError(t, tx.Rollback().Error)
	require.NoError(t, consolidationCompare(db, original), "failed public corpus must remain unchanged")
}

type consolidationQueryCounter struct {
	logger.Interface
	queries int
}

func (counter *consolidationQueryCounter) Trace(context.Context, time.Time, func() (string, int64), error) {
	counter.queries++
}

func TestProjectConsolidationUnresolvedMembershipBatch(t *testing.T) {
	dsnPath := os.Getenv("ENGRAM_CONSOLIDATION_VALIDATION_TEST_DSN_FILE")
	target := os.Getenv("ENGRAM_CONSOLIDATION_VALIDATION_TEST_TARGET")
	if dsnPath == "" || target == "" {
		t.Skip("dedicated isolated validation DSN/target absent; PostgreSQL batch regression not executed")
	}
	deadline, err := time.Parse(time.RFC3339, os.Getenv("ENGRAM_CONSOLIDATION_FIXTURE_DEADLINE"))
	require.NoError(t, err)
	require.True(t, time.Now().Before(deadline), "isolated fixture admission expired")
	private, err := os.ReadFile(dsnPath)
	require.NoError(t, err, "private dedicated fixture DSN unavailable")
	cfg, err := pgx.ParseConfig(strings.TrimSpace(string(private)))
	require.NoError(t, err)
	require.Equal(t, "127.0.0.1", cfg.Host)
	require.EqualValues(t, 22380, cfg.Port)
	require.True(t, strings.HasPrefix(cfg.Database, "engram_catalog_batch_test_"), "only the explicitly isolated synthetic validation database is permitted")
	require.Equal(t, target, fmt.Sprintf("%s:%d/%s", cfg.Host, cfg.Port, cfg.Database), "exact parent-supplied dedicated target required")
	cfg.RuntimeParams["application_name"] = "engram_catalog_validation_test"
	pool := stdlib.OpenDB(*cfg)
	pool.SetMaxOpenConns(1)
	defer pool.Close()
	db, err := gormlib.Open(postgres.New(postgres.Config{Conn: pool}), &gormlib.Config{Logger: logger.Default.LogMode(logger.Silent)})
	require.NoError(t, err)
	ctx, cancel := context.WithDeadline(context.Background(), deadline)
	defer cancel()
	var actual string
	require.NoError(t, db.WithContext(ctx).Raw("SELECT current_database()").Scan(&actual).Error)
	require.Equal(t, cfg.Database, actual)
	tx := db.WithContext(ctx).Begin()
	require.NoError(t, tx.Error)
	defer tx.Rollback()
	require.NoError(t, tx.Exec("SET LOCAL search_path = pg_temp").Error)
	for _, statement := range []string{
		`CREATE TEMP TABLE project_consolidation_memberships (reference text PRIMARY KEY, unresolved_reference text, table_name text, row_key jsonb, source_row_key jsonb, field_path jsonb, selector_reference text, project_id text, reason text, plan_fingerprint text, native_version text, source_snapshot text) ON COMMIT DROP`,
		`CREATE TEMP TABLE projects (id text PRIMARY KEY, display_name text) ON COMMIT DROP`,
		`CREATE TEMP TABLE sources (source_id uuid PRIMARY KEY, project_id text, display_name text) ON COMMIT DROP`,
		`CREATE TEMP TABLE memories (id bigint PRIMARY KEY, project text, content text) ON COMMIT DROP`,
		`CREATE TEMP TABLE credentials (id bigint PRIMARY KEY, project text) ON COMMIT DROP`,
		`CREATE TEMP TABLE issues (id bigint PRIMARY KEY, target_project text) ON COMMIT DROP`,
		`CREATE TEMP TABLE versioned_documents (id bigint, version bigint, project text, metadata jsonb) ON COMMIT DROP`,
		`CREATE TEMP TABLE catalog_physical_history (project text) ON COMMIT DROP`,
	} {
		require.NoError(t, tx.Exec(statement).Error)
	}
	vault, err := vaultcrypto.OpenExistingVault(&config.Config{EncryptionKey: strings.Repeat("ab", 32)})
	require.NoError(t, err)
	s := NewProjectConsolidationStore(tx, vault, nil)
	counter := &consolidationQueryCounter{Interface: logger.Default.LogMode(logger.Silent)}
	s.db = s.db.Session(&gormlib.Session{Logger: counter})
	member := func(table, key string, path []string, selector string) ConsolidationMembership {
		t.Helper()
		canonical, err := consolidationCanonicalKey([]byte(key))
		require.NoError(t, err)
		var nested []string
		if len(path) > 1 {
			nested = path[1:]
		}
		state := consolidationPlanState{Fingerprint: "isolated-validation"}
		s.addCensusRow(&state.Census, table, path[0], "tenant", canonical, nested, selector, false)
		mapping, err := s.buildMapping(state.Census.Rows, nil)
		require.NoError(t, err)
		return consolidationMembershipFor(state, state.Census.Rows[0], mapping[0], canonical)
	}
	const n = 257
	require.NoError(t, tx.Exec("INSERT INTO memories SELECT n,'historical','retained body' FROM generate_series(1,?) n", n).Error)
	require.NoError(t, tx.Exec("UPDATE memories SET content=(SELECT string_agg(md5(n::text),'') FROM generate_series(1,32769) n) WHERE id=1").Error)
	var body struct{ LogicalBytes, StoredBytes int64 }
	require.NoError(t, tx.Raw("SELECT octet_length(content) AS logical_bytes,pg_column_size(content) AS stored_bytes FROM memories WHERE id=1").Scan(&body).Error)
	require.Greater(t, body.LogicalBytes, int64(1<<20))
	require.Greater(t, body.StoredBytes, int64(8192), "the unrelated body must exceed an inline PostgreSQL page")
	rows := make([]ConsolidationMembership, n)
	for i := range rows {
		rows[i] = member("memories", fmt.Sprintf(`{"id":%d}`, i+1), []string{"project"}, "historical")
		rows[i].Reason = "a"
		if i%2 != 0 {
			rows[i].Reason = "b"
		}
	}
	counter.queries = 0
	require.NoError(t, s.validateUnresolvedMembership(ctx, rows))
	require.LessOrEqual(t, counter.queries, 8, "complete membership validation must use bounded batches, not one query per member")
	const sourceCount = 129
	require.NoError(t, tx.Exec("INSERT INTO sources SELECT ('00000000-0000-4000-8000-'||lpad(n::text,12,'0'))::uuid,NULL,'Source '||n FROM generate_series(1,?) n", sourceCount).Error)
	for i := range sourceCount {
		rows = append(rows, member("sources", fmt.Sprintf(`{"source_id":"00000000-0000-4000-8000-%012d"}`, i+1), []string{""}, ""))
	}
	require.NoError(t, tx.Exec("INSERT INTO projects VALUES ('historical','Historical label')").Error)
	require.NoError(t, tx.CreateInBatches(&rows, consolidationMembershipBatchSize).Error)
	items, err := s.UnresolvedMembership(ctx, rows[0].UnresolvedReference, "memory")
	require.NoError(t, err)
	require.Len(t, items, n)
	ids, err := RetainedContentIDs(items)
	require.NoError(t, err)
	require.Len(t, ids, n)
	legacy, err := s.LegacyUnresolved(ctx, "historical")
	require.NoError(t, err)
	require.Equal(t, rows[0].UnresolvedReference, legacy)
	counter.queries = 0
	catalog, err := s.ListUnresolved(ctx)
	require.NoError(t, err)
	require.LessOrEqual(t, counter.queries, 24, "validation and source labels must both have bounded query growth")
	require.Len(t, catalog, sourceCount+1)
	labels := make(map[string]UnresolvedProject, len(catalog))
	for i, item := range catalog {
		if i > 0 {
			require.Less(t, catalog[i-1].Reference, item.Reference, "complete catalog order is unchanged")
		}
		labels[item.Reference] = item
	}
	require.Equal(t, "Historical label", labels[legacy].Name)
	require.Equal(t, "a,b", labels[legacy].Reason)
	require.Equal(t, []string{"memory"}, labels[legacy].ReaderKinds)
	for i := range sourceCount {
		require.Equal(t, fmt.Sprintf("Source %d", i+1), labels[rows[n+i].UnresolvedReference].Name)
		require.Equal(t, []string{"code"}, labels[rows[n+i].UnresolvedReference].ReaderKinds)
	}
	require.NoError(t, tx.Exec("INSERT INTO projects VALUES ('registry-only','Registry metadata only')").Error)
	registry := member("projects", `{"id":"registry-only"}`, []string{"id"}, "registry-only")
	require.NoError(t, tx.Create(&registry).Error)
	require.NoError(t, tx.Exec("INSERT INTO versioned_documents VALUES (7,1,'historical','{}')").Error)
	require.NoError(t, tx.Exec("INSERT INTO issues VALUES (9001,'historical')").Error)
	for _, row := range []ConsolidationMembership{
		member("versioned_documents", `{"id":7,"version":1}`, []string{"project"}, "historical"),
		member("issues", `{"id":9001}`, []string{"target_project"}, "historical"),
	} {
		require.NoError(t, tx.Create(&row).Error)
	}
	catalog, err = s.ListUnresolved(ctx)
	require.NoError(t, err)
	require.Len(t, catalog, sourceCount+2, "metadata-only groups must stay in the complete catalog")
	for _, item := range catalog {
		if item.Reference == legacy {
			require.Equal(t, []string{"document", "issue", "memory"}, item.ReaderKinds)
		}
		if item.Reference == registry.UnresolvedReference {
			require.Equal(t, "Registry metadata only", item.Name)
			require.Equal(t, []string{}, item.ReaderKinds, "project registry metadata must not advertise a memory reader")
			encoded, err := json.Marshal(item)
			require.NoError(t, err)
			require.Contains(t, string(encoded), `"reader_kinds":[]`)
		}
	}
	_, err = s.UnresolvedMembership(ctx, registry.UnresolvedReference, "memory")
	require.EqualError(t, err, "PROJECT_ADOPTION_UNKNOWN_REFERENCE", "unsupported readers remain fail-closed")
	expectedCatalog := catalog
	for _, change := range []struct{ name, mutate, restore string }{
		{"missing native target", "DELETE FROM memories WHERE id=?", "INSERT INTO memories(id,project,content) VALUES (?,'historical','retained body')"},
		{"changed native selector", "UPDATE memories SET project='other' WHERE id=?", "UPDATE memories SET project='historical' WHERE id=?"},
	} {
		t.Run(change.name, func(t *testing.T) {
			require.NoError(t, tx.Exec(change.mutate, n).Error)
			defer func() { require.NoError(t, tx.Exec(change.restore, n).Error) }()
			metadata, err := s.ListUnresolved(ctx)
			require.NoError(t, err, "native payload availability must not gate complete unresolved metadata")
			require.Equal(t, catalog, metadata, "unchanged signed membership must remain visible with all provenance labels")
			_, err = s.UnresolvedMembership(ctx, legacy, "memory")
			require.EqualError(t, err, "PROJECT_ADOPTION_STALE_REFERENCE", "selected content must still validate its actual native target")
			_, err = s.LegacyUnresolved(ctx, "historical")
			require.EqualError(t, err, "PROJECT_ADOPTION_STALE_REFERENCE", "selected legacy resolution retains full native validation")
		})
	}
	require.NoError(t, tx.Exec("UPDATE project_consolidation_memberships SET reference=? WHERE reference=?", rows[0].Reference+"0", rows[0].Reference).Error)
	catalog, err = s.ListUnresolved(ctx)
	require.EqualError(t, err, "PROJECT_ADOPTION_UNKNOWN_REFERENCE")
	require.Nil(t, catalog, "one invalid member must refuse the entire catalog")
	require.NoError(t, tx.Exec("UPDATE project_consolidation_memberships SET reference=? WHERE reference=?", rows[0].Reference, rows[0].Reference+"0").Error)
	for _, change := range []struct {
		name, column, want string
		before, after      any
	}{
		{"opaque group", "unresolved_reference", "PROJECT_ADOPTION_UNKNOWN_REFERENCE", rows[0].UnresolvedReference, rows[0].UnresolvedReference + "0"},
		{"current key shape", "row_key", "PROJECT_ADOPTION_STALE_REFERENCE", rows[0].RowKey, JSONRaw(`[]`)},
		{"source key shape", "source_row_key", "PROJECT_ADOPTION_STALE_REFERENCE", rows[0].SourceRowKey, JSONRaw(`{}`)},
		{"field path shape", "field_path", "PROJECT_ADOPTION_STALE_REFERENCE", rows[0].FieldPath, JSONRaw(`[]`)},
		{"plan fingerprint", "plan_fingerprint", "PROJECT_ADOPTION_STALE_REFERENCE", rows[0].PlanFingerprint, ""},
	} {
		t.Run(change.name, func(t *testing.T) {
			query := tx.Model(&ConsolidationMembership{}).Where("reference = ?", rows[0].Reference)
			require.NoError(t, query.Update(change.column, change.after).Error)
			defer func() { require.NoError(t, query.Update(change.column, change.before).Error) }()
			metadata, err := s.ListUnresolved(ctx)
			require.EqualError(t, err, change.want)
			require.Nil(t, metadata, "invalid membership metadata must not become a successful partial catalog")
		})
	}
	foreignVault, err := vaultcrypto.OpenExistingVault(&config.Config{EncryptionKey: strings.Repeat("cd", 32)})
	require.NoError(t, err)
	foreignCatalog, err := NewProjectConsolidationStore(tx, foreignVault, nil).ListUnresolved(ctx)
	require.EqualError(t, err, "PROJECT_ADOPTION_UNKNOWN_REFERENCE")
	require.Nil(t, foreignCatalog)
	restoredCatalog, err := s.ListUnresolved(ctx)
	require.NoError(t, err)
	require.Equal(t, expectedCatalog, restoredCatalog)
	require.NoError(t, tx.Exec("INSERT INTO credentials VALUES (1,'historical')").Error)
	credential := member("credentials", `{"id":1}`, []string{"project"}, "historical")
	require.NoError(t, tx.Create(&credential).Error)
	items, err = s.UnresolvedMembership(ctx, legacy, "credential")
	require.NoError(t, err)
	require.Len(t, items, 1)
	stale := "PROJECT_ADOPTION_STALE_REFERENCE"
	unknown := "PROJECT_ADOPTION_UNKNOWN_REFERENCE"
	for _, mutate := range []func(*ConsolidationMembership){
		func(row *ConsolidationMembership) { row.Reference += "0" },
		func(row *ConsolidationMembership) { row.UnresolvedReference += "0" },
		func(row *ConsolidationMembership) { row.SourceRowKey = JSONRaw(`{"id":2}`) },
		func(row *ConsolidationMembership) { row.SelectorReference = s.opaque("selector", "other") },
	} {
		changed := rows[0]
		mutate(&changed)
		require.EqualError(t, s.validateUnresolvedMembership(ctx, []ConsolidationMembership{changed}), unknown)
	}
	changed := rows[0]
	changed.PlanFingerprint = ""
	require.EqualError(t, s.validateUnresolvedMembership(ctx, []ConsolidationMembership{changed}), stale)
	changed = rows[0]
	changed.RowKey = JSONRaw(`{"id":"1"}`)
	require.EqualError(t, s.validateUnresolvedMembership(ctx, []ConsolidationMembership{changed}), stale, "native numeric and string keys are not interchangeable")
	changed.RowKey = JSONRaw(`{"id":null}`)
	require.EqualError(t, s.validateUnresolvedMembership(ctx, []ConsolidationMembership{changed}), stale)
	changed = member("memories", `{"id":999999}`, []string{"project"}, "historical")
	changed.RowKey = rows[0].RowKey
	require.NoError(t, s.validateUnresolvedMembership(ctx, []ConsolidationMembership{changed}), "original source address remains distinct from the current native key")
	require.NoError(t, tx.Exec("UPDATE memories SET project='other' WHERE id=1").Error)
	require.EqualError(t, s.validateUnresolvedMembership(ctx, rows), stale, "a changed current selector must refuse the entire group")
	require.NoError(t, tx.Exec("UPDATE memories SET project='historical' WHERE id=1").Error)
	require.NoError(t, tx.Exec(`INSERT INTO versioned_documents VALUES (9007199254740993,2,'historical','{"nested":{"projects":["other","historical"],"null":null},"flag":true,"number":12}'), (9007199254740993,3,'other','{}'), (2,NULL,NULL,NULL), (3,1,'historical','null')`).Error)
	require.NoError(t, tx.Exec(`ALTER TABLE versioned_documents ADD COLUMN "selector""root" jsonb DEFAULT '{"nested":["other","historical"]}'::jsonb`).Error)
	require.NoError(t, tx.Exec(`INSERT INTO versioned_documents(id,version,project,metadata) VALUES (4,1,'','"retained"'),(5,1,'historical','12'),(6,1,'historical','true')`).Error)
	require.NoError(t, s.validateUnresolvedMembership(ctx, []ConsolidationMembership{
		member("versioned_documents", `{"id":9007199254740993,"version":2}`, []string{"project"}, "historical"),
		member("versioned_documents", `{"id":9007199254740993,"version":3}`, []string{"project"}, "other"),
		member("versioned_documents", `{"id":9007199254740993,"version":2}`, []string{"metadata", "nested", "projects", "1"}, "historical"),
		member("versioned_documents", `{"id":9007199254740993,"version":2}`, []string{"metadata", "nested", "projects", "0"}, "other"),
	}), "same-row paths and different composite members must not share a selector result")
	for _, test := range []struct {
		name, key string
		path      []string
		selector  string
		want      string
	}{
		{"composite large integer", `{"version":2,"id":9007199254740993}`, []string{"project"}, "historical", ""},
		{"composite typed key", `{"version":"2","id":9007199254740993}`, []string{"project"}, "historical", stale},
		{"exact one row", `{"id":9007199254740993}`, []string{"project"}, "historical", stale},
		{"nested array", `{"id":9007199254740993,"version":2}`, []string{"metadata", "nested", "projects", "1"}, "historical", ""},
		{"negative array index", `{"id":9007199254740993,"version":2}`, []string{"metadata", "nested", "projects", "-1"}, "historical", ""},
		{"JSON null selector", `{"id":9007199254740993,"version":2}`, []string{"metadata", "nested", "null"}, "", ""},
		{"missing selector", `{"id":9007199254740993,"version":2}`, []string{"metadata", "missing"}, "", ""},
		{"SQL null selector", `{"id":2}`, []string{"project"}, "", ""},
		{"JSON boolean text", `{"id":9007199254740993,"version":2}`, []string{"metadata", "flag"}, "true", ""},
		{"JSON number text", `{"id":9007199254740993,"version":2}`, []string{"metadata", "number"}, "12", ""},
		{"JSON object text", `{"id":9007199254740993,"version":2}`, []string{"metadata", "nested"}, `{"null": null, "projects": ["other", "historical"]}`, ""},
		{"JSON null root", `{"id":3}`, []string{"metadata"}, "", ""},
		{"empty text root", `{"id":4}`, []string{"project"}, "", ""},
		{"JSON string root", `{"id":4}`, []string{"metadata"}, "retained", ""},
		{"JSON number root", `{"id":5}`, []string{"metadata"}, "12", ""},
		{"JSON boolean root", `{"id":6}`, []string{"metadata"}, "true", ""},
		{"quoted root", `{"id":9007199254740993,"version":2}`, []string{"selector\"root", "nested", "1"}, "historical", ""},
		{"missing root", `{"id":9007199254740993,"version":2}`, []string{"absent_column"}, "", ""},
		{"system root", `{"id":9007199254740993,"version":2}`, []string{"tableoid"}, "", ""},
		{"JSON null native key", `{"metadata":null}`, []string{"project"}, "historical", ""},
		{"SQL null native key", `{"id":2,"version":null}`, []string{"project"}, "", stale},
		{"missing row", `{"id":99}`, []string{"project"}, "historical", stale},
	} {
		t.Run(test.name, func(t *testing.T) {
			row := member("versioned_documents", test.key, test.path, test.selector)
			if test.selector == "" {
				row.SelectorReference = s.opaque("selector", "")
				row.UnresolvedReference = s.opaque("unresolved", row.SelectorReference)
			}
			err := s.validateUnresolvedMembership(ctx, []ConsolidationMembership{row})
			if test.want == "" {
				require.NoError(t, err)
			} else {
				require.EqualError(t, err, test.want)
			}
		})
	}
	source := member("sources", string(rows[n].RowKey), []string{"project_id"}, "")
	require.NoError(t, s.validateUnresolvedMembership(ctx, []ConsolidationMembership{source}))
	require.NoError(t, tx.Exec("UPDATE sources SET project_id='other' WHERE source_id=(?::jsonb->>'source_id')::uuid", string(source.RowKey)).Error)
	require.EqualError(t, s.validateUnresolvedMembership(ctx, []ConsolidationMembership{source}), stale, "unbound source project must remain empty")
	require.NoError(t, s.validateUnresolvedMembership(ctx, []ConsolidationMembership{rows[n]}), "inherited sources retain the existence-only branch")
	source = member("sources", string(rows[n].RowKey), []string{"project_id"}, "other")
	require.NoError(t, s.validateUnresolvedMembership(ctx, []ConsolidationMembership{source}))
	source = member("sources", string(rows[n].RowKey), []string{"project_id"}, "historical")
	require.EqualError(t, s.validateUnresolvedMembership(ctx, []ConsolidationMembership{source}), stale)
	require.NoError(t, tx.Exec("DELETE FROM sources WHERE source_id=(?::jsonb->>'source_id')::uuid", string(source.RowKey)).Error)
	require.EqualError(t, s.validateUnresolvedMembership(ctx, []ConsolidationMembership{rows[n]}), stale)
	require.NoError(t, tx.Exec("INSERT INTO catalog_physical_history VALUES ('historical')").Error)
	var physical string
	require.NoError(t, tx.Raw("SELECT jsonb_build_object('__native_ctid',ctid::text,'__native_xmin',xmin::text)::text FROM catalog_physical_history").Scan(&physical).Error)
	physicalRow := member("catalog_physical_history", physical, []string{"project"}, "historical")
	require.NoError(t, s.validateUnresolvedMembership(ctx, []ConsolidationMembership{physicalRow}))
	require.NoError(t, tx.Exec("UPDATE catalog_physical_history SET project='historical'").Error)
	require.EqualError(t, s.validateUnresolvedMembership(ctx, []ConsolidationMembership{physicalRow}), stale)
	err = tx.Transaction(func(probe *gormlib.DB) error {
		missing := member("catalog_missing_table", `{"id":1}`, []string{"project"}, "historical")
		return NewProjectConsolidationStore(probe, vault, nil).validateUnresolvedMembership(ctx, []ConsolidationMembership{missing})
	})
	require.Error(t, err, "native database errors must fail closed, not become an empty catalog")
	cancelled, stop := context.WithCancel(ctx)
	stop()
	require.ErrorIs(t, s.validateUnresolvedMembership(cancelled, rows), context.Canceled)
	catalog, err = s.ListUnresolved(cancelled)
	require.ErrorIs(t, err, context.Canceled)
	require.Nil(t, catalog)
	require.NoError(t, tx.Rollback().Error)
}
