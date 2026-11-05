package gorm

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/thebtf/engram/internal/config"
	vaultcrypto "github.com/thebtf/engram/internal/crypto"
	gormlib "gorm.io/gorm"
)

func TestPlanProducedRetainedDocumentVersionScope(t *testing.T) {
	sourceDSN, restoreDSN := os.Getenv("DATABASE_DSN"), os.Getenv("ENGRAM_CONSOLIDATION_RESTORE_TEST_DSN")
	if sourceDSN == "" || restoreDSN == "" {
		t.Skip("two dedicated disposable PostgreSQL databases required")
	}
	sourceConfig := consolidationTestDatabaseConfig(t, sourceDSN, "source")
	targetConfig := consolidationTestDatabaseConfig(t, restoreDSN, "restore")
	require.Equal(t, sourceConfig.Host, targetConfig.Host)
	require.Equal(t, sourceConfig.Port, targetConfig.Port)
	require.Equal(t, strings.TrimSuffix(sourceConfig.Database, "_source"), strings.TrimSuffix(targetConfig.Database, "_restore"))
	require.NotEqual(t, sourceConfig.Database, targetConfig.Database)
	schema := "document_plan_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	source := openMigratedTestDBInSchema(t, sourceDSN, schema, "engram_document_plan_source")
	target := openMigratedTestDBInSchema(t, restoreDSN, schema, "engram_document_plan_target")
	vault, err := vaultcrypto.OpenExistingVault(&config.Config{EncryptionKey: strings.Repeat("ab", 32)})
	require.NoError(t, err)
	engine := NewProjectConsolidationStore(source, vault, target)
	ctx := t.Context()
	require.NoError(t, engine.distinctTarget(ctx))
	a, b := uuid.NewString(), uuid.NewString()
	for _, id := range []string{a, b} {
		require.NoError(t, source.Create(&Project{ID: id}).Error)
	}
	original := VersionedDocument{Project: a, Path: "plan-retained.md", Version: 1, Content: "plan-produced historical body", ContentHash: "plan-fixture-hash", DocType: "markdown", Metadata: "{}", Author: "synthetic"}
	unassigned := VersionedDocument{Project: a, Path: "plan-unassigned.md", Version: 1, Content: "unassigned synthetic body", ContentHash: "unassigned-fixture-hash", DocType: "markdown", Metadata: "{}", Author: "synthetic"}
	require.NoError(t, source.Create(&original).Error)
	require.NoError(t, source.Create(&unassigned).Error)
	census, err := engine.Census(ctx, "synthetic-plan-produced-document-parent")
	require.NoError(t, err)
	var censusState consolidationCensusState
	require.NoError(t, engine.open(census.Reference, "census", &censusState))
	var historical consolidationRow
	for _, row := range censusState.Rows {
		var key struct {
			ID int64 `json:"id"`
		}
		if row.Table == "versioned_documents" && row.Column == "project" && json.Unmarshal(row.Key, &key) == nil && key.ID == original.ID {
			historical = row
		}
	}
	require.NotEmpty(t, historical.Reference)
	plan, err := engine.Preflight(ctx, ConsolidationPreflightRequest{CensusReference: census.Reference, CensusFingerprint: census.Fingerprint, Evidence: []ConsolidationEvidence{{Reference: "urn:engram:fixture:document-marker", Kind: "portable_marker", Group: "urn:engram:fixture:document-group", ProjectID: b, Rows: []string{historical.Reference}}}})
	require.NoError(t, err)
	var authorized bool
	for _, item := range plan.Mapping {
		if item.Reference == historical.Reference {
			authorized = item.ProjectID == b
		}
	}
	require.True(t, authorized, "the actual synthetic plan must map this exact occurrence to B")
	var backup bytes.Buffer
	require.NoError(t, engine.Backup(ctx, plan.Reference, plan.Fingerprint, &backup))
	backupRef, err := engine.VerifyBackup(ctx, plan.Reference, plan.Fingerprint, bytes.NewReader(backup.Bytes()))
	require.NoError(t, err)
	_, err = engine.Apply(ctx, ConsolidationApplyRequest{PlanReference: plan.Reference, PlanFingerprint: plan.Fingerprint, BackupReference: backupRef, IdempotencyKey: uuid.NewString()})
	require.NoError(t, err)
	var parent ConsolidationMembership
	require.NoError(t, target.Where("reference = ?", historical.Reference).Take(&parent).Error)
	require.NotNil(t, parent.ProjectID)
	require.Equal(t, b, *parent.ProjectID)
	require.Equal(t, plan.Fingerprint, parent.PlanFingerprint)
	require.Equal(t, historical.NativeVersion, parent.NativeVersion)
	require.Equal(t, censusState.NativeSnapshot, parent.SourceSnapshot)
	var mapped VersionedDocument
	require.NoError(t, target.First(&mapped, original.ID).Error)
	require.Equal(t, b, mapped.Project, "ordinary Apply rewrites the resolved project to B")
	var redirects int64
	require.NoError(t, target.Table("project_consolidation_redirects").Where("selector IN ? OR project_id IN ?", []string{a, b}, []string{a, b}).Count(&redirects).Error)
	require.Zero(t, redirects, "the unassigned second occurrence prevents an A/B redirect")
	// Test-only simulated post-Apply mutation models the reviewed native-A/membership-B shape.
	// It is not a claim that the ordinary consolidation Apply created this divergence.
	require.NoError(t, target.Model(&VersionedDocument{}).Where("id = ?", original.ID).Update("project", a).Error)
	var before VersionedDocument
	require.NoError(t, target.First(&before, original.ID).Error)
	require.Equal(t, a, before.Project)
	reader := NewProjectConsolidationStore(target, vault, nil)
	laterCensus, err := reader.Census(ctx, "synthetic-parent-validation-after-mutation")
	require.NoError(t, err)
	var laterState consolidationCensusState
	require.NoError(t, reader.open(laterCensus.Reference, "census", &laterState))
	bindings, err := reader.retainedMemberships(target, laterState.Rows)
	require.NoError(t, err, "existing later-consolidation HMAC and provenance validation must accept the plan-produced parent")
	found := false
	for _, member := range bindings.occurrences {
		if member.Reference == parent.Reference {
			found = true
			require.Equal(t, parent.PlanFingerprint, member.PlanFingerprint)
			require.Equal(t, parent.NativeVersion, member.NativeVersion)
			require.Equal(t, parent.SourceSnapshot, member.SourceSnapshot)
		}
	}
	require.True(t, found)
	documents := NewVersionedDocumentStoreWithVault(&Store{DB: target}, vault)
	old, err := documents.ReadLatestForLineage(ctx, original.Path, b, original.ID)
	require.NoError(t, err)
	require.Equal(t, original.ID, old.ID)
	_, err = documents.ReadLatestForLineage(ctx, original.Path, a, original.ID)
	require.True(t, errors.Is(err, gormlib.ErrRecordNotFound), "A must not read the parent")
	newID, err := documents.CreateVersionForLineage(ctx, original.Path, b, "new version body", "markdown", "{}", "synthetic", original.ID, 1)
	require.NoError(t, err)
	var newRow VersionedDocument
	require.NoError(t, target.First(&newRow, newID).Error)
	require.Equal(t, a, newRow.Project, "new version retains native lineage A")
	newVisible, err := documents.ReadLatestForLineage(ctx, original.Path, b, newID)
	if assert.NoError(t, err, "new version must be visible under effective project B") {
		assert.Equal(t, newID, newVisible.ID)
	}
	_, err = documents.ReadLatestForLineage(ctx, original.Path, a, newID)
	assert.True(t, errors.Is(err, gormlib.ErrRecordNotFound), "native project A must not acquire the new version: %v", err)
	newCensus, err := reader.Census(ctx, "synthetic-live-version-validation")
	require.NoError(t, err)
	var newState consolidationCensusState
	require.NoError(t, reader.open(newCensus.Reference, "census", &newState))
	newBindings, err := reader.retainedMemberships(target, newState.Rows)
	require.NoError(t, err, "later consolidation must validate the live-version membership")
	var derived *ConsolidationMembership
	for _, member := range newBindings.occurrences {
		if string(member.RowKey) == string(JSONRaw(fmt.Sprintf(`{"id":%d}`, newID))) && member.Table == "versioned_documents" {
			copy := member
			derived = &copy
		}
	}
	require.NotNil(t, derived)
	require.NotEqual(t, parent.PlanFingerprint, derived.PlanFingerprint)
	require.True(t, strings.HasPrefix(derived.PlanFingerprint, liveDocumentMembershipFingerprint))
	require.Equal(t, liveDocumentMembershipReason+parent.Reference, derived.Reason)
	_, err = documents.CreateVersionForLineage(ctx, original.Path, b, "duplicate", "markdown", "{}", "synthetic", original.ID, 1)
	require.ErrorIs(t, err, ErrDocumentVersionStale)
	// A failed membership insert must roll back the preceding document insert.
	require.NoError(t, target.Exec(`CREATE FUNCTION reject_live_document_membership_fixture() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'synthetic membership insertion failure'; END $$`).Error)
	require.NoError(t, target.Exec(`CREATE TRIGGER reject_live_document_membership_fixture BEFORE INSERT ON project_consolidation_memberships FOR EACH ROW EXECUTE FUNCTION reject_live_document_membership_fixture()`).Error)
	_, err = documents.CreateVersionForLineage(ctx, original.Path, b, "must roll back", "markdown", "{}", "synthetic", newID, 2)
	require.ErrorContains(t, err, "synthetic membership insertion failure")
	var versions int64
	require.NoError(t, target.Model(&VersionedDocument{}).Where("path = ? AND project = ?", original.Path, a).Count(&versions).Error)
	require.EqualValues(t, 2, versions)
	require.NoError(t, target.Exec(`DROP TRIGGER reject_live_document_membership_fixture ON project_consolidation_memberships`).Error)
	require.NoError(t, target.Exec(`DROP FUNCTION reject_live_document_membership_fixture()`).Error)
	// Two writers of the same expected version must not upgrade SHARE locks or both succeed.
	concurrentCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	results := make(chan error, 2)
	for i := 0; i < 2; i++ {
		go func() {
			_, writeErr := documents.CreateVersionForLineage(concurrentCtx, original.Path, b, "concurrent", "markdown", "{}", "synthetic", newID, 2)
			results <- writeErr
		}()
	}
	first, second := <-results, <-results
	require.True(t, (first == nil && errors.Is(second, ErrDocumentVersionStale)) || (second == nil && errors.Is(first, ErrDocumentVersionStale)), "one success and one stale result required: %v / %v", first, second)
	require.NoError(t, target.Model(&VersionedDocument{}).Where("path = ? AND project = ?", original.Path, a).Count(&versions).Error)
	require.EqualValues(t, 3, versions)
	var latest VersionedDocument
	require.NoError(t, target.Where("path = ? AND project = ?", original.Path, a).Order("version DESC").First(&latest).Error)
	for i := 0; i < 12; i++ {
		id, writeErr := documents.CreateVersionForLineage(ctx, original.Path, b, "growth fixture", "markdown", "{}", "synthetic", latest.ID, latest.Version)
		require.NoError(t, writeErr)
		latest.ID, latest.Version = id, latest.Version+1
	}
	lastCensus, err := reader.Census(ctx, "synthetic-live-chain-validation")
	require.NoError(t, err)
	var lastState consolidationCensusState
	require.NoError(t, reader.open(lastCensus.Reference, "census", &lastState))
	var ancestryQueries atomic.Int64
	countAncestry := func(db *gormlib.DB) {
		if strings.Contains(db.Statement.SQL.String(), "WITH RECURSIVE ancestry") {
			ancestryQueries.Add(1)
		}
	}
	require.NoError(t, target.Callback().Row().Before("gorm:row").Register("fixture:count_ancestry", countAncestry))
	_, err = reader.retainedMemberships(target, lastState.Rows)
	require.NoError(t, target.Callback().Row().Remove("fixture:count_ancestry"))
	require.NoError(t, err, "later consolidation must validate chained live-version evidence")
	require.EqualValues(t, 1, ancestryQueries.Load(), "all versions share one ancestry query")
	// An intermediate proof must still invalidate the current tip, despite batching.
	require.NoError(t, target.Model(&ConsolidationMembership{}).Where("reference = ?", derived.Reference).Update("plan_fingerprint", "tampered").Error)
	_, err = documents.CreateVersionForLineage(ctx, original.Path, b, "must fail", "markdown", "{}", "synthetic", latest.ID, latest.Version)
	require.Error(t, err)
	require.NoError(t, target.Model(&ConsolidationMembership{}).Where("reference = ?", derived.Reference).Update("plan_fingerprint", derived.PlanFingerprint).Error)
	var preserved VersionedDocument
	require.NoError(t, target.First(&preserved, original.ID).Error)
	assert.Equal(t, before, preserved, "the original row and its native provenance remain unchanged")
}
