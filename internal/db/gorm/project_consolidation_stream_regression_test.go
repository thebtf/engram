package gorm

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	gormlib "gorm.io/gorm"
)

var streamIntegrityControlUpdates = map[string]string{
	"project_consolidation_memberships": "reason=reason",
	"project_consolidation_redirects":   "plan_fingerprint=plan_fingerprint",
	"project_consolidation_operations":  "completed_at=completed_at",
	"project_consolidation_backups":     "restore_snapshot=restore_snapshot",
	"project_consolidation_frames":      "ciphertext=ciphertext",
}

func TestProjectConsolidationStreamClosedInventory(t *testing.T) {
	full := consolidationCensusState{Tables: []consolidationTable{{Name: "memories"}}}
	for _, table := range []string{"project_consolidation_backups", "project_consolidation_frames", "project_consolidation_memberships", "project_consolidation_operations", "project_consolidation_redirects"} {
		full.Tables = append(full.Tables, consolidationTable{Name: table, Kind: "retained_migration_state"})
	}
	historical, err := consolidationHistoricalCensus(full)
	require.NoError(t, err)
	require.False(t, historical, "all five tables remain declared even when empty")
	for n := 1; n < 5; n++ {
		_, err := consolidationHistoricalCensus(consolidationCensusState{Tables: full.Tables[:n+1]})
		require.EqualError(t, err, "PROJECT_ADOPTION_STALE_PLAN")
	}
	_, err = consolidationHistoricalCensus(consolidationCensusState{Tables: append(full.Tables, full.Tables[0])})
	require.EqualError(t, err, "PROJECT_ADOPTION_STALE_PLAN")
	_, err = consolidationHistoricalCensus(consolidationCensusState{Tables: full.Tables[:1], Rows: []consolidationRow{{Table: "project_consolidation_memberships"}}})
	require.EqualError(t, err, "PROJECT_ADOPTION_STALE_PLAN")
	store := &ProjectConsolidationStore{}
	_, err = store.buildMapping([]consolidationRow{{Reference: "history", Table: "project_consolidation_operations", Kind: "retained_migration_state"}}, []ConsolidationEvidence{{Reference: "urn:engram:proof:forbidden", Group: "urn:engram:group:forbidden", Kind: "portable_marker", ProjectID: uuid.NewString(), Rows: []string{"history"}}})
	require.EqualError(t, err, "PROJECT_ADOPTION_INVALID_EVIDENCE", "retained control rows never become global or project authority")
}

func TestProjectConsolidationStreamRetainedMembershipPlan(t *testing.T) {
	oldTarget, nextTarget := uuid.NewString(), uuid.NewString()
	row := consolidationRow{Reference: "new-address-reference", Table: "project_continuity_slots", Column: "project", Key: JSONRaw(`{"project":"current"}`)}
	original := ConsolidationMembership{Reference: "original-reference", Table: row.Table, RowKey: row.Key, SourceRowKey: JSONRaw(`{"project":"original"}`), FieldPath: JSONRaw(`["project"]`), ProjectID: &oldTarget, PlanFingerprint: "original-fingerprint", NativeVersion: "original-xmin", SourceSnapshot: "original-snapshot", Reason: "original-reason", SelectorReference: "original-selector"}
	state := consolidationPlanState{Fingerprint: "new-fingerprint", retained: &consolidationRetainedBindings{occurrences: map[string]ConsolidationMembership{consolidationOccurrenceKey(row.Table, row.Key, original.FieldPath): original}}}
	key := JSONRaw(`{"project":"authorized-address"}`)
	actual, retained, err := consolidationPlannedMembership(state, row, ConsolidationMapping{ProjectID: oldTarget}, key)
	require.NoError(t, err)
	require.True(t, retained)
	want := original
	want.RowKey = key
	require.Equal(t, want, actual, "reuse the original provenance, not a new HMAC occurrence")
	_, _, err = consolidationPlannedMembership(state, row, ConsolidationMapping{ProjectID: nextTarget}, key)
	require.EqualError(t, err, "PROJECT_ADOPTION_CONFLICTING_MEMBERSHIP")
}

func TestProjectConsolidationStreamMetadataFreshness(t *testing.T) {
	f := consolidationFixtureStore(t)
	streamIntegritySeed(t, f, true)
	plan, sealed := streamIntegrityPlan(t, f, f.source)
	state, err := f.store.plan(plan.Reference, plan.Fingerprint)
	require.NoError(t, err)
	sourceBefore, targetBefore := streamIntegrityRead(t, f.source), streamIntegrityRead(t, f.target)
	for table, update := range streamIntegrityControlUpdates {
		t.Run("source/"+table, func(t *testing.T) {
			tx := f.source.Begin()
			require.NoError(t, tx.Error)
			defer tx.Rollback()
			result := tx.Exec("UPDATE " + quoted(table) + " SET " + update)
			require.NoError(t, result.Error)
			require.Positive(t, result.RowsAffected, "every historical table must be nonempty")
			store := NewProjectConsolidationStore(tx, f.store.vault, f.target)
			require.EqualError(t, store.Backup(f.ctx, plan.Reference, plan.Fingerprint, io.Discard), "PROJECT_ADOPTION_STALE_PLAN")
			_, err := store.VerifyBackup(f.ctx, plan.Reference, plan.Fingerprint, bytes.NewReader(sealed))
			require.EqualError(t, err, "PROJECT_ADOPTION_STALE_PLAN")
			_, err = store.DryRunNative(f.ctx, ConsolidationNativeRequest{PlanReference: plan.Reference, PlanFingerprint: plan.Fingerprint, ExpectedPlanID: state.ID})
			require.EqualError(t, err, "PROJECT_ADOPTION_STALE_PLAN")
			require.NoError(t, tx.Rollback().Error)
			streamIntegrityUnchanged(t, sourceBefore, f.source, "rolled-back source freshness probe")
			streamIntegrityUnchanged(t, targetBefore, f.target, "stale source never reaches target effects")
		})
	}
	for table, update := range streamIntegrityControlUpdates {
		t.Run("target/"+table, func(t *testing.T) {
			targetFixture := consolidationFixtureStore(t)
			streamIntegritySeed(t, targetFixture, true)
			targetPlan, targetBytes := streamIntegrityPlan(t, targetFixture, targetFixture.source)
			id, err := targetFixture.store.VerifyBackup(targetFixture.ctx, targetPlan.Reference, targetPlan.Fingerprint, bytes.NewReader(targetBytes))
			require.NoError(t, err)
			result := targetFixture.target.Exec("UPDATE " + quoted(table) + " SET " + update)
			require.NoError(t, result.Error)
			require.Positive(t, result.RowsAffected, "each case independently changes the selected table's xmin")
			tampered := streamIntegrityRead(t, targetFixture.target)
			_, err = targetFixture.store.VerifyBackup(targetFixture.ctx, targetPlan.Reference, targetPlan.Fingerprint, bytes.NewReader(targetBytes))
			require.EqualError(t, err, "PROJECT_ADOPTION_STALE_PLAN")
			_, err = targetFixture.store.Apply(targetFixture.ctx, ConsolidationApplyRequest{PlanReference: targetPlan.Reference, PlanFingerprint: targetPlan.Fingerprint, BackupReference: id, IdempotencyKey: uuid.NewString()})
			require.EqualError(t, err, "PROJECT_ADOPTION_STALE_PLAN")
			streamIntegrityUnchanged(t, tampered, targetFixture.target, "refused target metadata tamper has no additional effects")
		})
	}
	shortened := state.Census
	shortened.Tables = append([]consolidationTable{}, state.Census.Tables[1:]...)
	require.EqualError(t, f.source.Transaction(func(tx *gormlib.DB) error { return f.store.fresh(tx, shortened) }), "PROJECT_ADOPTION_STALE_PLAN", "a shortened full inventory is not a historical recipe")
	streamIntegrityUnchanged(t, sourceBefore, f.source, "all source freshness probes")
}

func TestProjectConsolidationStreamControlLifecycle(t *testing.T) {
	f := consolidationFixtureStore(t)
	// GRANT/REVOKE materializes NULL ACLs; start with the exact owner-only ACL.
	for _, db := range []*gormlib.DB{f.source, f.target} {
		require.NoError(t, db.Exec(`GRANT ALL ON credentials TO CURRENT_USER`).Error)
	}
	project := streamIntegritySeed(t, f, true)
	plan, sealed := streamIntegrityPlan(t, f, f.source)
	id, err := f.store.VerifyBackup(f.ctx, plan.Reference, plan.Fingerprint, bytes.NewReader(sealed))
	require.NoError(t, err)
	var backup consolidationBackupRow
	require.NoError(t, f.target.Where("backup_id=?", id).First(&backup).Error)
	revision, err := consolidationRevision(f.target)
	require.NoError(t, err)
	require.Equal(t, revision, backup.RestoreSnapshot, "backup finalization must not invalidate its own full revision")
	first := streamIntegrityRead(t, f.target)
	census, err := f.store.Census(f.ctx, "corrected-plan-source")
	require.NoError(t, err)
	var refs []string
	for _, row := range census.Rows {
		if row.Family == "credentials" && row.Role == "project" {
			refs = append(refs, row.Reference)
		}
	}
	require.NotEmpty(t, refs)
	corrected, err := f.store.Preflight(f.ctx, ConsolidationPreflightRequest{CensusReference: census.Reference, CensusFingerprint: census.Fingerprint, Evidence: []ConsolidationEvidence{{Reference: "urn:engram:proof:correction", Kind: "portable_marker", Group: "urn:engram:group:correction", ProjectID: project, Rows: refs}}})
	require.NoError(t, err)
	var correctedBytes bytes.Buffer
	require.NoError(t, f.store.Backup(f.ctx, corrected.Reference, corrected.Fingerprint, &correctedBytes))
	correctedID, err := f.store.VerifyBackup(f.ctx, corrected.Reference, corrected.Fingerprint, bytes.NewReader(correctedBytes.Bytes()))
	require.NoError(t, err)
	require.NotEqual(t, id, correctedID)
	for _, table := range []string{"project_consolidation_backups", "project_consolidation_frames"} {
		var original []string
		require.NoError(t, f.target.Raw("SELECT to_jsonb(t)::text FROM "+quoted(table)+" t WHERE backup_id=? ORDER BY to_jsonb(t)::text", id).Scan(&original).Error)
		var before []string
		for _, raw := range first.Rows[table] {
			var object map[string]json.RawMessage
			require.NoError(t, json.Unmarshal([]byte(raw), &object))
			var key string
			require.NoError(t, json.Unmarshal(object["backup_id"], &key))
			if key == id {
				before = append(before, raw)
			}
		}
		for i := range original {
			original[i] = streamIntegrityJSON(t, original[i])
		}
		require.ElementsMatch(t, before, original, "corrected plan preserves prior local recovery bytes")
	}
	before := streamIntegrityRead(t, f.target)
	request := ConsolidationApplyRequest{PlanReference: corrected.Reference, PlanFingerprint: corrected.Fingerprint, BackupReference: correctedID, IdempotencyKey: uuid.NewString()}
	_, err = f.store.DryRun(f.ctx, request)
	require.NoError(t, err)
	streamIntegrityUnchanged(t, before, f.target, "real dry-run including temporary DDL and operation")
	receipt, err := f.store.Apply(f.ctx, request)
	require.NoError(t, err)
	revision, err = consolidationRevision(f.target)
	require.NoError(t, err)
	require.Equal(t, receipt.Snapshot, revision, "operation insertion precedes final full revision")
	after := streamIntegrityRead(t, f.target)
	replayed, err := f.store.Apply(f.ctx, request)
	require.NoError(t, err)
	require.Equal(t, receipt, replayed)
	streamIntegrityUnchanged(t, after, f.target, "new-transaction idempotent replay")
	for _, table := range []string{"project_consolidation_memberships", "project_consolidation_redirects"} {
		for _, raw := range first.Rows[table] {
			require.Contains(t, after.Rows[table], raw, "compatible retained history must not be replaced by the new plan")
		}
	}
	for _, drift := range []struct{ sql, undo string }{
		{"ALTER TABLE credentials DISABLE TRIGGER engram_consolidation_scope_write", "ALTER TABLE credentials ENABLE TRIGGER engram_consolidation_scope_write"},
		{"GRANT SELECT ON credentials TO PUBLIC", "REVOKE SELECT ON credentials FROM PUBLIC"},
		{"ALTER TABLE credentials ENABLE ROW LEVEL SECURITY", "ALTER TABLE credentials DISABLE ROW LEVEL SECURITY"},
		{"CREATE POLICY stream_replay_drift ON credentials USING (true)", "DROP POLICY stream_replay_drift ON credentials"},
	} {
		t.Run(drift.sql, func(t *testing.T) {
			require.NoError(t, f.target.Exec(drift.sql).Error)
			defer func() {
				require.NoError(t, f.target.Exec(drift.undo).Error)
				streamIntegrityUnchanged(t, after, f.target, "restored synthetic replay schema")
			}()
			before := streamIntegrityRead(t, f.target)
			_, err := f.store.Apply(f.ctx, request)
			require.EqualError(t, err, "PROJECT_ADOPTION_SCHEMA_MISMATCH", "schema checking must precede even successful replay")
			streamIntegrityUnchanged(t, before, f.target, "schema-refused replay")
		})
	}
}

func TestProjectConsolidationStreamRetainedAddressShift(t *testing.T) {
	f := consolidationFixtureStore(t)
	project := streamIntegritySeed(t, f, false)
	tx := f.source.Begin()
	restore, err := consolidationSuspendWriteGuards(tx)
	require.NoError(t, err)
	memory := Memory{Project: project, Content: "slot provenance fixture"}
	require.NoError(t, tx.Create(&memory).Error)
	require.NoError(t, tx.Exec(`INSERT INTO project_continuity_slots(project,memory_id,expires_at,authority_domain,authority_owner_principal,authority_owner_principal_kind) VALUES('original-slot',?,now()+interval '1 day','fixture','fixture','agent')`, memory.ID).Error)
	require.NoError(t, restore())
	require.NoError(t, tx.Commit().Error)
	census, err := f.store.Census(f.ctx, "prior-slot-state")
	require.NoError(t, err)
	var prior consolidationCensusState
	require.NoError(t, f.store.open(census.Reference, "census", &prior))
	var members []ConsolidationMembership
	for _, row := range prior.Rows {
		if row.Table == "project_continuity_slots" {
			group := row.SelectorReference
			if group == "" {
				group = row.Reference
			}
			member := consolidationMembershipFor(consolidationPlanState{Fingerprint: "retained-slot-plan", Census: prior}, row, ConsolidationMapping{UnresolvedReference: f.store.opaque("unresolved", group), Reason: "retained-slot-reason"}, JSONRaw(`{"project":"`+project+`"}`))
			members = append(members, member)
		}
	}
	require.NotEmpty(t, members)
	tx = f.source.Begin()
	restore, err = consolidationSuspendWriteGuards(tx)
	require.NoError(t, err)
	require.NoError(t, tx.Exec(`UPDATE project_continuity_slots SET project=? WHERE project='original-slot'`, project).Error)
	require.NoError(t, tx.Create(&members).Error)
	require.NoError(t, restore())
	require.NoError(t, tx.Commit().Error)
	current, err := f.store.Census(f.ctx, "current-slot-state")
	require.NoError(t, err)
	var refs []string
	for _, row := range current.Rows {
		if row.Family == "project_continuity_slots" && row.Role == "project" {
			refs = append(refs, row.Reference)
		}
	}
	next := uuid.NewString()
	plan, err := f.store.Preflight(f.ctx, ConsolidationPreflightRequest{CensusReference: current.Reference, CensusFingerprint: current.Fingerprint, Evidence: []ConsolidationEvidence{{Reference: "urn:engram:proof:slot", Kind: "portable_marker", Group: "urn:engram:group:slot", ProjectID: next, Rows: refs}}})
	require.NoError(t, err)
	var sealed bytes.Buffer
	require.NoError(t, f.store.Backup(f.ctx, plan.Reference, plan.Fingerprint, &sealed))
	id, err := f.store.VerifyBackup(f.ctx, plan.Reference, plan.Fingerprint, bytes.NewReader(sealed.Bytes()))
	require.NoError(t, err)
	request := ConsolidationApplyRequest{PlanReference: plan.Reference, PlanFingerprint: plan.Fingerprint, BackupReference: id, IdempotencyKey: uuid.NewString()}
	_, err = f.store.Apply(f.ctx, request)
	require.NoError(t, err)
	for _, original := range members {
		var actual ConsolidationMembership
		require.NoError(t, f.target.Where("reference=?", original.Reference).First(&actual).Error)
		require.NoError(t, consolidationNormalizeMembership(&actual))
		want := original
		want.RowKey = JSONRaw(`{"project":"` + next + `"}`)
		want.ProjectID = &next
		require.Equal(t, want, actual, "authorized PK change retains original reference, source key, versions, snapshot, reason and unresolved provenance")
	}
	var count int64
	require.NoError(t, f.target.Model(&ConsolidationMembership{}).Where("table_name='project_continuity_slots'").Count(&count).Error)
	require.EqualValues(t, len(members), count, "new address HMAC must not create duplicate memberships")
}

func TestProjectConsolidationStreamRollbackAndConflicts(t *testing.T) {
	f := consolidationFixtureStore(t)
	streamIntegritySeed(t, f, true)
	for _, db := range []*gormlib.DB{f.source, f.target} {
		require.NoError(t, db.Exec(`CREATE TABLE stream_integrity_parent(id integer PRIMARY KEY);
		 CREATE TABLE stream_integrity_child(id integer PRIMARY KEY,parent_id integer REFERENCES stream_integrity_parent(id));
		 CREATE TABLE stream_integrity_unique(id integer PRIMARY KEY,value text UNIQUE);
		 CREATE FUNCTION stream_integrity_failure() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN
		 IF current_setting('stream_integrity.failure',true)='raise' THEN RAISE EXCEPTION 'synthetic independent trigger';
		 ELSIF current_setting('stream_integrity.failure',true)='fk' THEN INSERT INTO stream_integrity_child VALUES(999,999);
		 ELSIF current_setting('stream_integrity.failure',true)='unique' THEN INSERT INTO stream_integrity_unique VALUES(999,'retained'); END IF; RETURN NEW; END $$;
		 CREATE TRIGGER stream_integrity_failure AFTER INSERT ON credentials FOR EACH ROW EXECUTE FUNCTION stream_integrity_failure()`).Error)
	}
	require.NoError(t, f.source.Exec(`INSERT INTO stream_integrity_unique VALUES(1,'retained')`).Error)
	plan, sealed := streamIntegrityPlan(t, f, f.source)
	before := streamIntegrityRead(t, f.target)
	for _, failure := range []string{"raise", "fk", "unique"} {
		t.Run(failure, func(t *testing.T) {
			tx := f.target.Begin()
			require.NoError(t, tx.Exec(`SELECT set_config('stream_integrity.failure',?,true)`, failure).Error)
			_, err := NewProjectConsolidationStore(f.source, f.store.vault, tx).VerifyBackup(f.ctx, plan.Reference, plan.Fingerprint, bytes.NewReader(sealed))
			require.Error(t, err)
			require.NoError(t, tx.Rollback().Error)
			streamIntegrityUnchanged(t, before, f.target, "failed restore preserves all rows, frames, sequences and exact DDL modes")
		})
	}
	cancelled, cancel := context.WithCancel(f.ctx)
	_, err := f.store.VerifyBackup(cancelled, plan.Reference, plan.Fingerprint, &consolidationCancelReader{Reader: bytes.NewReader(sealed), Cancel: cancel, Remaining: int64(len(sealed) / 2)})
	cancel()
	require.Error(t, err)
	streamIntegrityUnchanged(t, before, f.target, "cancelled restore includes envelope and all five control tables")
	for _, raw := range [][]byte{sealed[:len(sealed)-1], append(append([]byte{}, sealed...), 1)} {
		_, err := f.store.VerifyBackup(f.ctx, plan.Reference, plan.Fingerprint, bytes.NewReader(raw))
		require.Error(t, err)
		streamIntegrityUnchanged(t, before, f.target, "incomplete or trailing stream")
	}
	tampered := append([]byte{}, sealed...)
	tampered[len(tampered)/2] ^= 1
	_, err = f.store.VerifyBackup(f.ctx, plan.Reference, plan.Fingerprint, bytes.NewReader(tampered))
	require.Error(t, err)
	streamIntegrityUnchanged(t, before, f.target, "authenticated tamper")
	_, err = f.store.VerifyBackup(f.ctx, plan.Reference, plan.Fingerprint, bytes.NewReader(sealed))
	require.NoError(t, err, "corrected retry uses the same source/plan/stream after transactional failures")
}

func TestProjectConsolidationStreamRetainedConflictRollback(t *testing.T) {
	f := consolidationFixtureStore(t)
	project := streamIntegritySeed(t, f, true)
	census, err := f.store.Census(f.ctx, "retained-conflict")
	require.NoError(t, err)
	var references []string
	for _, row := range census.Rows {
		if row.Family == "credentials" && row.Role == "project" {
			references = append(references, row.Reference)
		}
	}
	require.NotEmpty(t, references)
	for _, target := range []string{uuid.NewString(), project} {
		plan, err := f.store.Preflight(f.ctx, ConsolidationPreflightRequest{CensusReference: census.Reference, CensusFingerprint: census.Fingerprint, Evidence: []ConsolidationEvidence{{Reference: "urn:engram:proof:retained-conflict", Kind: "portable_marker", Group: "urn:engram:group:retained-conflict", ProjectID: target, Rows: references}}})
		require.NoError(t, err)
		var sealed bytes.Buffer
		require.NoError(t, f.store.Backup(f.ctx, plan.Reference, plan.Fingerprint, &sealed))
		id, err := f.store.VerifyBackup(f.ctx, plan.Reference, plan.Fingerprint, bytes.NewReader(sealed.Bytes()))
		require.NoError(t, err)
		before := streamIntegrityRead(t, f.target)
		_, err = f.store.DryRun(f.ctx, ConsolidationApplyRequest{PlanReference: plan.Reference, PlanFingerprint: plan.Fingerprint, BackupReference: id, IdempotencyKey: uuid.NewString()})
		if target != project {
			require.EqualError(t, err, "PROJECT_ADOPTION_CONFLICTING_MEMBERSHIP")
		} else {
			require.NoError(t, err, "corrected evidence reuses, rather than overwrites, compatible original membership")
		}
		streamIntegrityUnchanged(t, before, f.target, "retained conflict or corrected dry-run preserves all state")
	}
}

func TestProjectConsolidationStreamRejectsUndeclaredControls(t *testing.T) {
	f := consolidationFixtureStore(t)
	streamIntegritySeed(t, f, true)
	for _, db := range []*gormlib.DB{f.source, f.target} {
		require.NoError(t, db.Exec(`CREATE FUNCTION stream_integrity_rogue() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN
		 IF current_setting('stream_integrity.rogue',true)='on' AND NEW.idempotency_key<>'00000000-0000-4000-8000-000000000099' THEN
		 INSERT INTO project_consolidation_operations(idempotency_key,operation_id,plan_id,plan_fingerprint,receipt,completed_at)
		 VALUES('00000000-0000-4000-8000-000000000099','00000000-0000-4000-8000-000000000099',NEW.plan_id,NEW.plan_fingerprint,'{}',now());
		 END IF; RETURN NEW; END $$;
		 CREATE TRIGGER stream_integrity_rogue AFTER INSERT ON project_consolidation_operations FOR EACH ROW EXECUTE FUNCTION stream_integrity_rogue()`).Error)
	}
	plan, sealed := streamIntegrityPlan(t, f, f.source)
	id, err := f.store.VerifyBackup(f.ctx, plan.Reference, plan.Fingerprint, bytes.NewReader(sealed))
	require.NoError(t, err)
	before := streamIntegrityRead(t, f.target)
	request := ConsolidationApplyRequest{PlanReference: plan.Reference, PlanFingerprint: plan.Fingerprint, BackupReference: id, IdempotencyKey: uuid.NewString()}
	require.NoError(t, f.target.Connection(func(connection *gormlib.DB) error {
		require.NoError(t, connection.Exec(`SET stream_integrity.rogue='on'`).Error)
		defer func() { require.NoError(t, connection.Exec(`RESET stream_integrity.rogue`).Error) }()
		_, err := NewProjectConsolidationStore(f.source, f.store.vault, connection).Apply(f.ctx, request)
		require.EqualError(t, err, "PROJECT_ADOPTION_CONSERVATION_FAILED", "matching plan prefixes cannot authorize an undeclared operation")
		return nil
	}))
	streamIntegrityUnchanged(t, before, f.target, "undeclared control insertion rolls back the entire operation")
	_, err = f.store.Apply(f.ctx, request)
	require.NoError(t, err, "corrected retry does not retain the rogue operation or partial envelope")
}

func TestProjectConsolidationStreamHistoricalKeyCollision(t *testing.T) {
	f := consolidationFixtureStore(t)
	streamIntegritySeed(t, f, true)
	plan, sealed := streamIntegrityPlan(t, f, f.source)
	var original consolidationBackupRow
	require.NoError(t, f.source.Order("backup_id").First(&original).Error)
	original.RestoreSnapshot = "conflicting-local-history"
	require.NoError(t, f.target.Create(&original).Error)
	before := streamIntegrityRead(t, f.target)
	_, err := f.store.VerifyBackup(f.ctx, plan.Reference, plan.Fingerprint, bytes.NewReader(sealed))
	require.EqualError(t, err, "PROJECT_ADOPTION_CONSERVATION_FAILED", "same historical key requires full byte-level row equality")
	streamIntegrityUnchanged(t, before, f.target, "conflicting target-local history remains recoverable")
}

func TestProjectConsolidationStreamKeylessMultiplicity(t *testing.T) {
	f := consolidationFixtureStore(t)
	streamIntegritySeed(t, f, false)
	for _, db := range []*gormlib.DB{f.source, f.target} {
		require.NoError(t, db.Exec(`CREATE TABLE stream_integrity_keyless(payload text NOT NULL)`).Error)
	}
	require.NoError(t, f.source.Exec(`INSERT INTO stream_integrity_keyless VALUES('discarded'),('retained duplicate'),('retained duplicate'); DELETE FROM stream_integrity_keyless WHERE payload='discarded'`).Error)
	plan, sealed := streamIntegrityPlan(t, f, f.source)
	id, err := f.store.VerifyBackup(f.ctx, plan.Reference, plan.Fingerprint, bytes.NewReader(sealed))
	require.NoError(t, err)
	request := ConsolidationApplyRequest{PlanReference: plan.Reference, PlanFingerprint: plan.Fingerprint, BackupReference: id, IdempotencyKey: uuid.NewString()}
	_, err = f.store.Apply(f.ctx, request)
	require.NoError(t, err)
	var members []ConsolidationMembership
	require.NoError(t, f.target.Where("table_name='stream_integrity_keyless'").Order("reference").Find(&members).Error)
	require.Len(t, members, 2, "identical bodies remain two independently retained native occurrences")
	reader := NewProjectConsolidationStore(f.target, f.store.vault, nil)
	for _, member := range members {
		selected, err := reader.UnresolvedMembership(f.ctx, member.UnresolvedReference, "native")
		require.NoError(t, err, "restored physical key must use target xmin/ctid, never source tuple addresses")
		require.Len(t, selected, 1)
		predicate, args, err := consolidationNativePredicate(member.RowKey, "t")
		require.NoError(t, err)
		var count int64
		require.NoError(t, f.target.Raw("SELECT count(*) FROM stream_integrity_keyless t WHERE "+predicate, args...).Scan(&count).Error)
		require.EqualValues(t, 1, count)
	}
	before := streamIntegrityRead(t, f.target)
	_, err = f.store.Apply(f.ctx, request)
	require.NoError(t, err)
	streamIntegrityUnchanged(t, before, f.target, "keyless replay preserves multiplicity and exact physical bindings")
}

func TestProjectConsolidationStreamHTTPFixture(t *testing.T) {
	f := openStreamIntegrityFixture(t, streamIntegrityOracle, "stream_http_"+strings.ReplaceAll(uuid.NewString(), "-", ""), true)
	streamIntegritySeed(t, f, true)
	before := streamIntegrityRead(t, f.source)
	local := streamIntegrityRead(t, f.target)
	streamIntegritySave(t, filepath.Join(streamIntegrityFixtureRoot(t), ".private", "registered-http-fixture.json"), struct {
		Schema         string
		Source, Target streamIntegrityState
	}{f.schema, before, local})
	t.Log("Prepared retained private HTTP fixture; no HTTP or restore success is claimed by preparation")
}
