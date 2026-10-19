package gorm

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/thebtf/engram/internal/config"
	vaultcrypto "github.com/thebtf/engram/internal/crypto"
	"github.com/thebtf/engram/internal/projectidentity"
	"github.com/thebtf/engram/internal/scope"
	gormlib "gorm.io/gorm"
)

func TestProjectPartitionRepairProjectProjection(t *testing.T) {
	for _, test := range []struct {
		name  string
		label sql.NullString
		want  string
	}{
		{"existing_NULL", sql.NullString{}, "null"},
		{"explicit_empty", sql.NullString{Valid: true}, `""`},
		{"historical_label", sql.NullString{String: "Former Alpha", Valid: true}, `"Former Alpha"`},
	} {
		t.Run(test.name, func(t *testing.T) {
			raw, err := consolidationProjectObject(Project{DisplayName: test.label})
			require.NoError(t, err)
			var fields map[string]json.RawMessage
			require.NoError(t, json.Unmarshal(raw, &fields))
			require.JSONEq(t, test.want, string(fields["display_name"]))
		})
	}
}

func TestProjectPartitionRepairOrdinaryConsumers(t *testing.T) {
	db, schema := openInterventionReceiptMigrationTestDB(t)
	ctx := context.Background()
	vault, err := vaultcrypto.OpenExistingVault(&config.Config{EncryptionKey: strings.Repeat("ab", 32)})
	require.NoError(t, err)
	adoption := NewProjectConsolidationStore(db, vault, nil)
	backing := &Store{DB: db}
	sessions, memories, documents := NewSessionStore(backing), NewMemoryStore(backing), NewVersionedDocumentStore(backing)
	canonical := "10000000-0000-4000-8000-000000000001"
	oldIDs := []string{"former-alpha", "former-beta", "former-gamma"}
	labels := []string{"Former Alpha", "Former Beta", "Former Gamma"}
	wanted := make(map[string]string)
	registry := make(map[string]consolidationRow)
	seed := db.Begin()
	require.NoError(t, seed.Error)
	defer seed.Rollback()
	require.NoError(t, consolidationLock(seed))
	restoreGuards, err := consolidationSuspendWriteGuards(seed)
	require.NoError(t, err)
	member := func(table, column string, key any, selector string, project *string) consolidationRow {
		t.Helper()
		raw, err := json.Marshal(key)
		require.NoError(t, err)
		state := consolidationCensusState{NativeSnapshot: "synthetic-B7-snapshot"}
		adoption.addCensusRow(&state, table, column, "tenant", JSONRaw(raw), nil, selector, false)
		row := state.Rows[0]
		row.NativeVersion = "synthetic-B7-origin-version"
		mapping := ConsolidationMapping{UnresolvedReference: adoption.opaque("unresolved", row.SelectorReference), Reason: "missing_membership_evidence"}
		if project != nil {
			mapping.ProjectID, mapping.UnresolvedReference, mapping.Reason = *project, "", ""
		}
		if table == "sources" {
			row.SelectorReference = ""
			mapping.UnresolvedReference = adoption.opaque("unresolved", row.Reference)
		}
		stored := consolidationMembershipFor(consolidationPlanState{Fingerprint: "synthetic-B7-plan", Census: state}, row, mapping, row.Key)
		require.NoError(t, seed.Create(&stored).Error)
		return row
	}
	for i, old := range oldIDs {
		aliases := []string{"ambiguous-alias"}
		if i == 0 {
			aliases = []string{"registered-alpha-alias"}
		}
		require.NoError(t, seed.Create(&Project{ID: old, DisplayName: sql.NullString{String: labels[i], Valid: true}, GitRemote: sql.NullString{String: "https://example.invalid/same-repository.git", Valid: true}, LegacyIDs: aliases}).Error)
		row := member("projects", "id", map[string]string{"id": old}, old, nil)
		registry[old] = row
		digest := sha256.Sum256([]byte(row.Reference))
		group := "urn:engram:group:retained-project-record:" + hex.EncodeToString(digest[:])
		wanted[old] = uuid.NewSHA1(uuid.NameSpaceOID, []byte("engram.logical-project/v1\x00"+group)).String()
	}
	require.NoError(t, seed.Create(&Project{ID: canonical, DisplayName: sql.NullString{String: "Current canonical", Valid: true}}).Error)
	rows := []Memory{
		{Project: canonical, Content: "current canonical body", Status: "active", PrivacyScope: "project", Version: 1},
		{Project: oldIDs[0], Content: "alpha exact old PK body", Status: "active", PrivacyScope: "project", Version: 3},
		{Project: "registered-alpha-alias", Content: "alpha exact registered alias body", Status: "active", PrivacyScope: "project", Version: 2},
		{Project: oldIDs[1], Content: "beta independent body", Status: "active", PrivacyScope: "project", Version: 1},
		{Project: "unknown-owner", Content: "orphan readable body", Status: "active", PrivacyScope: "project", Version: 1},
		{Project: "", Content: "empty unproven body", Status: "active", PrivacyScope: "project", Version: 1},
		{Project: "ambiguous-alias", Content: "ambiguous readable body", Status: "active", PrivacyScope: "project", Version: 1},
	}
	for i := range rows {
		require.NoError(t, seed.Create(&rows[i]).Error)
		if rows[i].Project == "" {
			continue
		}
		var owner *string
		if i == 0 {
			owner = &canonical
		}
		member("memories", "project", map[string]int64{"id": rows[i].ID}, rows[i].Project, owner)
	}
	require.NoError(t, seed.Create(&AuditLogEntry{MemoryID: &rows[1].ID, Action: "partition_fixture_history", Actor: "fixture-owner", BeforeState: rawConsolidationJSON(`{"project":"former-alpha","version":2}`)}).Error)
	docs := []VersionedDocument{}
	for version := 1; version <= 2; version++ {
		doc := VersionedDocument{Path: "preserved.md", Project: oldIDs[0], Version: version, Content: "preserved document version " + string(rune('0'+version)), ContentHash: strings.Repeat("a", 64), Metadata: "{}", Author: "fixture-owner"}
		require.NoError(t, seed.Create(&doc).Error)
		docs = append(docs, doc)
		member("versioned_documents", "project", map[string]int64{"id": doc.ID}, doc.Project, nil)
	}
	issue := Issue{Title: "Independent issue roles", Body: "preserved issue body", SourceProject: oldIDs[0], TargetProject: oldIDs[1], SourceAgent: "agent", Labels: []string{}}
	require.NoError(t, seed.Create(&issue).Error)
	member("issues", "source_project", map[string]int64{"id": issue.ID}, issue.SourceProject, nil)
	member("issues", "target_project", map[string]int64{"id": issue.ID}, issue.TargetProject, nil)
	require.NoError(t, seed.Create(&IssueComment{IssueID: issue.ID, AuthorProject: oldIDs[0], AuthorAgent: "agent", Body: "preserved issue history"}).Error)
	for range 2 {
		source := UCISource{SourceID: uuid.NewString(), AuthRealm: "fixture", Kind: UCISourceGit, DisplayName: "Unassigned source", State: UCISourceActive, CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC()}
		require.NoError(t, seed.Create(&source).Error)
		member("sources", "project_id", map[string]string{"source_id": source.SourceID}, "", nil)
	}
	parentID := "20000000-0000-4000-8000-000000000001"
	receipt, err := json.Marshal(ConsolidationReceipt{OperationID: parentID, Reference: "urn:engram:migration:receipt:" + parentID, ProjectIDs: []string{canonical}, RollbackBoundary: consolidationRollbackBoundary})
	require.NoError(t, err)
	require.NoError(t, seed.Create(&consolidationOperation{IdempotencyKey: parentID, OperationID: parentID, PlanID: parentID, PlanFingerprint: "synthetic-B7-plan", Receipt: JSONRaw(receipt), CompletedAt: time.Now().UTC()}).Error)
	require.NoError(t, restoreGuards())
	require.NoError(t, seed.Commit().Error)
	source := db
	sourceBefore := streamIntegrityRead(t, source)
	targetDSN := os.Getenv("ENGRAM_CONSOLIDATION_RESTORE_TEST_DSN")
	require.NotEmpty(t, targetDSN, "a separate synthetic restored target is required")
	consolidationTestDatabaseConfig(t, targetDSN, "restore")
	target := openMigratedTestDBInSchema(t, targetDSN, schema, "engram_partition_repair_target")
	partitionRepairCloneFixture(t, source, target)
	var parent consolidationOperation
	require.NoError(t, source.Where("operation_id=?", parentID).Take(&parent).Error)
	parentDigest := consolidationCipherHash(parent.Receipt)
	engine := NewProjectConsolidationStore(source, vault, target)
	t.Run("failed_receipt_append_rolls_back_every_table", func(t *testing.T) {
		for _, fixture := range []*gormlib.DB{source, target} {
			require.NoError(t, fixture.Exec(`ALTER TABLE project_consolidation_operations ADD CONSTRAINT partition_fixture_reject_repair CHECK (receipt->>'kind' IS DISTINCT FROM 'project-partition-repair/v1')`).Error)
			defer func(fixture *gormlib.DB) {
				require.NoError(t, fixture.Exec(`ALTER TABLE project_consolidation_operations DROP CONSTRAINT partition_fixture_reject_repair`).Error)
			}(fixture)
		}
		failurePlan, err := engine.PlanProjectPartitionRepair(ctx, parentDigest)
		require.NoError(t, err)
		beforeFailure := streamIntegrityRead(t, target)
		_, err = engine.ApplyProjectPartitionRepair(ctx, ProjectPartitionRepairApplyRequest{PlanReference: failurePlan.Reference, PlanFingerprint: failurePlan.Fingerprint, ExpectedPlanID: failurePlan.PlanID})
		require.Error(t, err, "independent receipt CHECK must reject after canonical projects and memberships were written")
		require.Equal(t, beforeFailure, streamIntegrityRead(t, target), "failed append must roll back rows/xmin/history/receipts/sequences/schema")
	})
	plan, err := engine.PlanProjectPartitionRepair(ctx, parentDigest)
	require.NoError(t, err)
	second, err := engine.PlanProjectPartitionRepair(ctx, parentDigest)
	require.NoError(t, err)
	require.Equal(t, plan.PlanID, second.PlanID)
	require.Equal(t, plan.Fingerprint, second.Fingerprint)
	require.Equal(t, 3, plan.Partitions)
	require.Equal(t, 3, plan.NewProjects)
	require.Equal(t, 10, plan.MembershipChanges)
	require.Equal(t, 1, plan.UnassignedAdditions)
	request := ProjectPartitionRepairApplyRequest{PlanReference: plan.Reference, PlanFingerprint: plan.Fingerprint, ExpectedPlanID: plan.PlanID}
	targetBefore := streamIntegrityRead(t, target)
	t.Run("tampered_plan_wrong_ID_same_physical_source_refuse", func(t *testing.T) {
		bad := request
		bad.PlanReference += "tampered"
		_, err := engine.ApplyProjectPartitionRepair(ctx, bad)
		require.Error(t, err)
		bad = request
		bad.ExpectedPlanID = uuid.NewString()
		_, err = engine.ApplyProjectPartitionRepair(ctx, bad)
		require.EqualError(t, err, "PROJECT_ADOPTION_STALE_PLAN")
		_, err = NewProjectConsolidationStore(source, vault, source).ApplyProjectPartitionRepair(ctx, request)
		require.EqualError(t, err, "PROJECT_ADOPTION_TARGET_IS_SOURCE")
		require.Equal(t, targetBefore, streamIntegrityRead(t, target))
	})
	dryRun, err := engine.DryRunProjectPartitionRepair(ctx, request)
	require.NoError(t, err)
	require.True(t, dryRun.DryRun)
	require.Equal(t, targetBefore, streamIntegrityRead(t, target))
	applied, err := engine.ApplyProjectPartitionRepair(ctx, request)
	require.NoError(t, err)
	require.False(t, applied.Replay)
	require.Equal(t, 10, applied.Receipt.MembershipChanges)
	require.Equal(t, 1, applied.Receipt.UnassignedAdditions)
	before := streamIntegrityRead(t, target)
	replayed, err := engine.ApplyProjectPartitionRepair(ctx, request)
	require.NoError(t, err)
	require.True(t, replayed.Replay)
	require.Equal(t, applied.Receipt, replayed.Receipt)
	require.Equal(t, before, streamIntegrityRead(t, target))
	require.Equal(t, sourceBefore, streamIntegrityRead(t, source), "source rows/xmin/receipts/sequences/schema remain read-only")
	for table, rows := range targetBefore.Versions {
		if table == "projects" || table == "project_consolidation_operations" {
			require.Subset(t, before.Versions[table], rows, "all preexisting registry and B7 operation rows remain exact, including xmin")
		} else if table != "project_consolidation_memberships" {
			require.Equal(t, rows, before.Versions[table], "raw table %s must not change", table)
		}
	}
	require.Equal(t, targetBefore.Sequences, before.Sequences)
	db = target
	backing = &Store{DB: db}
	adoption = NewProjectConsolidationStore(db, vault, nil)
	sessions, memories, documents = NewSessionStore(backing), NewMemoryStore(backing), NewVersionedDocumentStore(backing)
	t.Logf("repair synthetic delta: %d new projects, %d membership targets, %d unassigned additions; %d sequences conserved; source read-only, dry-run rollback and exact replay passed", plan.NewProjects, applied.Receipt.MembershipChanges, applied.Receipt.UnassignedAdditions, len(before.Sequences))
	caller, visibility := scope.KeycardContext{}, scope.MemoryVisibilityOptions{}

	t.Run("current_canonical_and_legacy_selector_boundary", func(t *testing.T) {
		current, err := memories.GetVisible(ctx, rows[0].ID, canonical, caller, visibility)
		require.NoError(t, err)
		require.Equal(t, rows[0].Content, current.Content)
		for _, selector := range append(oldIDs, "registered-alpha-alias", "ambiguous-alias") {
			exists, err := sessions.CanonicalProjectExists(ctx, selector)
			require.NoError(t, err)
			require.False(t, exists)
			_, err = memories.GetVisible(ctx, rows[1].ID, selector, caller, visibility)
			require.ErrorIs(t, err, ErrKnowledgeProjectForbidden)
		}
	})
	t.Run("catalogue_keeps_every_former_partition_and_label", func(t *testing.T) {
		catalogue, err := sessions.GetAllProjects(ctx)
		require.NoError(t, err)
		byID := map[string]string{}
		for _, item := range catalogue {
			require.True(t, projectidentity.ValidProjectID(item.ID))
			byID[item.ID] = item.DisplayName
		}
		assert.Len(t, catalogue, len(oldIDs)+1, "ordinary catalogue must preserve three separate old records, even with the same remote")
		assert.Equal(t, "Current canonical", byID[canonical])
		for i, old := range oldIDs {
			assert.Equal(t, labels[i], byID[wanted[old]], "former project must have its own stable UUID and former label")
		}
		assert.NotEqual(t, wanted[oldIDs[0]], wanted[oldIDs[1]])
	})
	t.Run("ordinary_memory_body_and_history_follow_partition_UUID", func(t *testing.T) {
		for _, index := range []int{1, 2, 3} {
			project := wanted[oldIDs[0]]
			if index == 3 {
				project = wanted[oldIDs[1]]
			}
			read, err := memories.GetVisible(ctx, rows[index].ID, project, caller, visibility)
			if assert.NoError(t, err, "ordinary UUID reader must reach the retained body, not merely its metadata") && assert.NotNil(t, read) {
				assert.Equal(t, rows[index].Content, read.Content)
				assert.Equal(t, project, read.Project)
				assert.Equal(t, rows[index].Version, read.Version)
			}
		}
		history, count, err := sessions.RetainedMemoryAudit(ctx, rows[1].ID, 10, 0)
		require.NoError(t, err)
		require.EqualValues(t, 1, count)
		require.Equal(t, "partition_fixture_history", history[0].Action)
	})
	t.Run("ordinary_document_body_and_versions_follow_same_UUID", func(t *testing.T) {
		doc, err := documents.ReadLatest(ctx, "preserved.md", wanted[oldIDs[0]])
		if assert.NoError(t, err) && assert.NotNil(t, doc) {
			assert.Equal(t, docs[1].Content, doc.Content)
			assert.Equal(t, wanted[oldIDs[0]], doc.Project)
		}
		history, err := documents.GetHistory(ctx, "preserved.md", wanted[oldIDs[0]], 10)
		if assert.NoError(t, err) && assert.Len(t, history, 2) {
			assert.Equal(t, docs[1].ID, history[0].ID)
			assert.Equal(t, docs[0].Content, history[1].Content)
		}
	})
	t.Run("issue_source_and_target_are_independent", func(t *testing.T) {
		issues := NewIssueStore(db)
		for _, filter := range []IssueListParams{{SourceProject: wanted[oldIDs[0]], Limit: 10}, {TargetProject: wanted[oldIDs[1]], Limit: 10}} {
			items, total, err := issues.ListIssuesEx(ctx, filter)
			require.NoError(t, err)
			if assert.EqualValues(t, 1, total) && assert.Len(t, items, 1) {
				assert.Equal(t, issue.ID, items[0].ID)
				assert.Equal(t, issue.Body, items[0].Body)
			}
		}
		_, comments, err := issues.GetIssue(ctx, issue.ID)
		require.NoError(t, err)
		require.Len(t, comments, 1)
		require.Equal(t, "preserved issue history", comments[0].Body)
	})
	t.Run("unknown_ambiguous_empty_and_NULL_remain_readable_without_attribution", func(t *testing.T) {
		catalogue, err := adoption.ListUnresolved(ctx)
		require.NoError(t, err)
		found := map[int64]bool{}
		for _, item := range catalogue {
			for _, kind := range item.ReaderKinds {
				if kind != "memory" {
					continue
				}
				members, err := adoption.UnresolvedMembership(ctx, item.Reference, kind)
				require.NoError(t, err)
				ids, err := RetainedContentIDs(members)
				require.NoError(t, err)
				items, _, err := sessions.RetainedMemories(ctx, ids, "", caller, visibility, 100, 0)
				require.NoError(t, err)
				for _, memory := range items {
					found[memory.ID] = true
					for _, index := range []int{4, 5, 6} {
						if memory.ID == rows[index].ID {
							assert.Equal(t, rows[index].Content, memory.Content)
							for _, member := range members {
								assert.Nil(t, member.ProjectID)
							}
						}
					}
				}
			}
		}
		for _, index := range []int{4, 5, 6} {
			assert.True(t, found[rows[index].ID], "retained navigation must expose unknown, ambiguous and empty-unproven bodies")
		}
		var sources []UCISource
		require.NoError(t, db.Find(&sources).Error)
		require.Len(t, sources, 2)
		for _, source := range sources {
			require.Nil(t, source.ProjectID)
		}
	})
	t.Run("existing_guards_and_immutable_origin", func(t *testing.T) {
		require.EqualError(t, NewProjectConsolidationStore(db, vault, db).distinctTarget(ctx), "PROJECT_ADOPTION_TARGET_IS_SOURCE")
		require.EqualError(t, adoption.distinctTarget(ctx), "PROJECT_ADOPTION_ISOLATED_TARGET_MISSING")
		row := registry[oldIDs[0]]
		var original ConsolidationMembership
		require.NoError(t, db.Where("reference = ?", row.Reference).Take(&original).Error)
		require.NoError(t, consolidationNormalizeMembership(&original))
		state := consolidationPlanState{Fingerprint: "proposed-repair", retained: &consolidationRetainedBindings{occurrences: map[string]ConsolidationMembership{consolidationOccurrenceKey(row.Table, row.Key, original.FieldPath): original}}}
		mapped, retained, err := consolidationPlannedMembership(state, row, ConsolidationMapping{ProjectID: wanted[oldIDs[0]]}, row.Key)
		require.NoError(t, err)
		require.True(t, retained)
		original.ProjectID = mapped.ProjectID
		require.Equal(t, original, mapped, "a null-to-UUID patch must preserve every B7 origin field")
		replay := state
		replay.retained = &consolidationRetainedBindings{occurrences: map[string]ConsolidationMembership{consolidationOccurrenceKey(row.Table, row.Key, mapped.FieldPath): mapped}}
		again, _, err := consolidationPlannedMembership(replay, row, ConsolidationMapping{ProjectID: wanted[oldIDs[0]]}, row.Key)
		require.NoError(t, err)
		require.Equal(t, mapped, again)
		_, _, err = consolidationPlannedMembership(replay, row, ConsolidationMapping{ProjectID: wanted[oldIDs[1]]}, row.Key)
		require.EqualError(t, err, "PROJECT_ADOPTION_CONFLICTING_MEMBERSHIP")
		after := streamIntegrityRead(t, db)
		require.Equal(t, before, after, "read-only RED and proposed-patch helpers must not mutate old rows, xmin, history, B7 receipt, sequences or schema")
	})
}

func partitionRepairCloneFixture(t *testing.T, source, target *gormlib.DB) {
	t.Helper()
	tables, err := consolidationDataTables(source)
	require.NoError(t, err)
	columns, err := consolidationColumns(source)
	require.NoError(t, err)
	fields := map[string][]string{}
	for _, column := range columns {
		if column.IsGenerated != "ALWAYS" {
			fields[column.TableName] = append(fields[column.TableName], quoted(column.ColumnName))
		}
	}
	sequences, err := consolidationSequences(source)
	require.NoError(t, err)
	require.NoError(t, target.Transaction(func(tx *gormlib.DB) error {
		if err := consolidationLock(tx); err != nil {
			return err
		}
		if err := tx.Exec("SET LOCAL session_replication_role=replica").Error; err != nil {
			return err
		}
		names := make([]string, len(tables))
		for i, table := range tables {
			names[i] = quoted(table.Name)
		}
		if err := tx.Exec("TRUNCATE " + strings.Join(names, ",")).Error; err != nil {
			return err
		}
		for _, table := range tables {
			rows, err := source.Raw("SELECT to_jsonb(t)::text FROM " + quoted(table.Name) + " t").Rows()
			if err != nil {
				return err
			}
			for rows.Next() {
				var raw string
				if err := rows.Scan(&raw); err != nil {
					rows.Close()
					return err
				}
				names := strings.Join(fields[table.Name], ",")
				if err := tx.Exec("INSERT INTO "+quoted(table.Name)+"("+names+") OVERRIDING SYSTEM VALUE SELECT "+names+" FROM jsonb_populate_record(NULL::"+quoted(table.Name)+",?::jsonb)", raw).Error; err != nil {
					rows.Close()
					return err
				}
			}
			err = rows.Err()
			rows.Close()
			if err != nil {
				return err
			}
		}
		for _, sequence := range sequences {
			if err := consolidationSetSequence(tx, sequence); err != nil {
				return err
			}
		}
		return nil
	}))
}
