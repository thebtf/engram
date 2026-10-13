package gorm

import (
	"bytes"
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/lib/pq"
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

func consolidationFixtureStore(t *testing.T, deadline time.Time, dsnPath, output string) (*ProjectConsolidationStore, *gormlib.DB, *gormlib.DB) {
	t.Helper()
	if !time.Now().Before(deadline) {
		t.Fatal("admitted fixture deadline expired")
	}
	private, err := os.ReadFile(dsnPath)
	if err != nil {
		t.Fatal("private fixture DSN unavailable")
	}
	cfg, err := pgx.ParseConfig(strings.TrimSpace(string(private)))
	if err != nil || cfg.Host != "127.0.0.1" || cfg.Port != 22366 || cfg.Database != "identity_fixture" {
		t.Fatal("fixture does not match admitted exact target")
	}
	sourceConfig := consolidationTestDatabaseConfig(t, os.Getenv("DATABASE_DSN"), "source")
	targetConfig := consolidationTestDatabaseConfig(t, os.Getenv("ENGRAM_CONSOLIDATION_RESTORE_TEST_DSN"), "restore")
	for _, fixture := range []*pgx.ConnConfig{sourceConfig, targetConfig} {
		require.Equal(t, cfg.Host, fixture.Host)
		require.Equal(t, cfg.Port, fixture.Port)
	}
	require.Equal(t, strings.TrimSuffix(sourceConfig.Database, "_source"), strings.TrimSuffix(targetConfig.Database, "_restore"))
	ctx, cancel := context.WithDeadline(context.Background(), deadline)
	t.Cleanup(cancel)
	schema := "stream_workflow_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	open := func(fixture *pgx.ConnConfig) *gormlib.DB {
		admin := stdlib.OpenDB(*fixture)
		admin.SetMaxOpenConns(1)
		_, err := admin.ExecContext(ctx, "CREATE SCHEMA "+pq.QuoteIdentifier(schema))
		require.NoError(t, err)
		t.Cleanup(func() {
			_, err := admin.ExecContext(ctx, "DROP SCHEMA "+pq.QuoteIdentifier(schema)+" CASCADE")
			require.NoError(t, err, "remove only this workflow's owned schema after its pool closes")
			require.NoError(t, admin.Close())
		})
		copy := fixture.Copy()
		copy.RuntimeParams["search_path"] = schema + ", public"
		copy.RuntimeParams["application_name"] = "engram_ops_identity_migration"
		pool := stdlib.OpenDB(*copy)
		pool.SetMaxOpenConns(2)
		t.Cleanup(func() { require.NoError(t, pool.Close()) })
		db, err := gormlib.Open(postgres.New(postgres.Config{Conn: pool}), &gormlib.Config{Logger: logger.Default.LogMode(logger.Silent)})
		require.NoError(t, err)
		require.NoError(t, runMigrations(db.WithContext(ctx)))
		return db
	}
	source, target := open(sourceConfig), open(targetConfig)
	privateDir := filepath.Join(output, ".private")
	require.NoError(t, os.MkdirAll(privateDir, 0o700))
	keyPath := filepath.Join(privateDir, "vault.key")
	key := make([]byte, 32)
	_, err = rand.Read(key)
	require.NoError(t, err)
	file, err := os.OpenFile(keyPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	require.NoError(t, err)
	_, err = file.WriteString(hex.EncodeToString(key))
	require.NoError(t, err)
	require.NoError(t, file.Close())
	v, err := vaultcrypto.OpenExistingVault(&config.Config{EncryptionKeyFile: keyPath})
	require.NoError(t, err)
	return NewProjectConsolidationStore(source, v, target), source, target
}

// This single workflow runs real PostgreSQL constraints, complete source schema,
// Vault encryption/decryption, ordinary readers, restore, row updates and replay.
// Its generated corpus is not the owner's corpus or installed acceptance.
func TestProjectConsolidationRealStoreWorkflow(t *testing.T) {
	dsnPath := os.Getenv("ENGRAM_CONSOLIDATION_FIXTURE_DSN_FILE")
	output := os.Getenv("ENGRAM_CONSOLIDATION_TEST_OUTPUT")
	if dsnPath == "" || output == "" {
		t.Skip("exact admitted fixture/private output binding absent; no owner-data or migration PASS")
	}
	deadline, err := time.Parse(time.RFC3339, os.Getenv("ENGRAM_CONSOLIDATION_FIXTURE_DEADLINE"))
	require.NoError(t, err)
	s, db, target := consolidationFixtureStore(t, deadline, dsnPath, output)
	ctx, cancel := context.WithDeadline(context.Background(), deadline)
	defer cancel()
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
	backupFile, err := os.OpenFile(filepath.Join(output, ".private", "fixture-backup.sealed"), os.O_CREATE|os.O_EXCL|os.O_RDWR, 0o600)
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
	correctedFile, err := os.OpenFile(filepath.Join(output, ".private", "fixture-backup-corrected.sealed"), os.O_CREATE|os.O_EXCL|os.O_RDWR, 0o600)
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
