package gorm

import (
	"context"
	"fmt"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/thebtf/engram/pkg/models"
)

func TestMemoryPrincipalConstraintsForwardUpgradeAfter149(t *testing.T) {
	db, schema := openInterventionReceiptMigrationTestDB(t)
	ctx := context.Background()
	snapshot := func(query string, args ...any) string {
		t.Helper()
		var value string
		require.NoError(t, db.Raw(query, args...).Row().Scan(&value))
		return value
	}
	publicMetadataSQL := `SELECT jsonb_build_object(
		'relation', c.oid, 'owner', c.relowner, 'namespace', c.relnamespace,
		'checks', (SELECT jsonb_agg(jsonb_build_object('oid', k.oid,
			'name', k.conname, 'validated', k.convalidated,
			'definition', pg_get_constraintdef(k.oid)) ORDER BY k.conname)
			FROM pg_constraint k WHERE k.conrelid = c.oid AND k.contype = 'c')
	)::text FROM pg_class c WHERE c.oid = 'public.memories'::regclass`
	publicBefore := snapshot(publicMetadataSQL)
	connectionBefore := snapshot(`SELECT jsonb_build_array(current_user, current_schema(), current_setting('search_path'))::text`)
	t.Cleanup(func() {
		assert.JSONEq(t, publicBefore, snapshot(publicMetadataSQL), "public relation and CHECK metadata must remain untouched")
		assert.JSONEq(t, connectionBefore, snapshot(`SELECT jsonb_build_array(current_user, current_schema(), current_setting('search_path'))::text`))
	})

	constraints := []string{"memories_owner_principal_kind_chk", "memories_agent_visibility_chk"}
	for _, name := range constraints {
		var publicCount int
		require.NoError(t, db.Raw(`SELECT count(*) FROM pg_constraint
			WHERE conrelid = 'public.memories'::regclass AND conname = ?
			AND contype = 'c' AND convalidated`, name).Scan(&publicCount).Error)
		require.Equal(t, 1, publicCount, "requires the existing public same-named CHECK %s; do not alter public", name)
	}
	require.NotEqual(t, "public", schema)
	var applied149 int
	require.NoError(t, db.Raw(`SELECT count(*) FROM migrations WHERE id = '149_memory_principals'`).Scan(&applied149).Error)
	require.Equal(t, 1, applied149, "exercise an already-applied 149 ledger, not a fresh migration replay")

	store := NewMemoryStore(&Store{DB: db})
	project := sessionTestProject(t, db)
	memory, err := store.Create(ctx, &models.Memory{
		Project: project, Content: "preserve forward principal upgrade",
		OwnerPrincipal: "agent/regression", OwnerPrincipalKind: "agent",
		AgentVisibility: models.AgentVisibilityPrivate,
	})
	require.NoError(t, err)
	memory.Content = "preserve version two through forward principal upgrade"
	memory, err = store.Update(ctx, memory)
	require.NoError(t, err)
	require.Equal(t, 2, memory.Version)
	require.NoError(t, NewAuditStore(db).Log(ctx, AuditLogEntry{
		MemoryID: &memory.ID, Action: "principal_upgrade_fixture", Actor: "agent/regression",
		Reason: "must survive CHECK repair unchanged",
	}))

	// Reconstruct only this disposable schema's missed-149 state; never rewind 149.
	for _, name := range constraints {
		require.NoError(t, db.Exec(fmt.Sprintf(`ALTER TABLE %q.memories DROP CONSTRAINT IF EXISTS %s`, schema, name)).Error)
	}
	var prospectiveIDs []string
	require.NoError(t, db.Table("migrations").Where(`split_part(id, '_', 1) = '188'`).Pluck("id", &prospectiveIDs).Error)
	require.LessOrEqual(t, len(prospectiveIDs), 1)
	for _, id := range prospectiveIDs {
		require.NoError(t, db.Exec(fmt.Sprintf(`DELETE FROM %q.migrations WHERE id = ?`, schema), id).Error)
	}
	var localCount int
	require.NoError(t, db.Raw(`SELECT count(*) FROM pg_constraint WHERE conrelid = 'memories'::regclass AND conname IN ?`, constraints).Scan(&localCount).Error)
	require.Zero(t, localCount)
	dataSQL := `SELECT jsonb_build_object(
		'memories', (SELECT jsonb_agg(to_jsonb(m) ORDER BY id) FROM memories m),
		'audit', (SELECT jsonb_agg(to_jsonb(a) ORDER BY id) FROM audit_log a),
		'projects', (SELECT jsonb_agg(to_jsonb(p) ORDER BY id) FROM projects p)
	)::text`
	dataBefore := snapshot(dataSQL)
	ledgerSQL := `SELECT jsonb_agg(id ORDER BY id)::text FROM migrations WHERE split_part(id, '_', 1) <> '188'`
	ledgerBefore := snapshot(ledgerSQL)
	t.Logf("historical fixture: schema=%s applied149=%d local_checks=%d public_checks=2 prospective188_removed=%d version=%d", schema, applied149, localCount, len(prospectiveIDs), memory.Version)

	require.NoError(t, runMigrations(db), "ordinary startup must forward-repair checks missed by an already-applied 149")
	for _, name := range constraints {
		var count int
		require.NoError(t, db.Raw(`SELECT count(*) FROM pg_constraint
			WHERE conrelid = 'memories'::regclass AND conname = ?
			AND contype = 'c' AND convalidated`, name).Scan(&count).Error)
		assert.Equal(t, 1, count, "forward startup must establish validated CHECK %s on the actual target memories relation", name)
	}
	assert.JSONEq(t, dataBefore, snapshot(dataSQL), "valid rows, versions, projects and audit must not be rewritten")
	assert.JSONEq(t, ledgerBefore, snapshot(ledgerSQL), "existing migration history including 149 must remain applied")

	tx := db.Begin()
	require.NoError(t, tx.Error)
	defer func() { require.NoError(t, tx.Rollback().Error) }()
	for _, kind := range []string{"", "human", "agent", "service"} {
		for _, visibility := range []string{"", "private", "shared"} {
			require.NoError(t, tx.Exec(`INSERT INTO memories (project, content, owner_principal_kind, agent_visibility)
				VALUES (?, 'valid original principal enums', ?, ?)`, project, kind, visibility).Error,
				"original enum pair (%q, %q) must remain valid", kind, visibility)
		}
	}
	for _, tc := range []struct {
		column, constraint, invalid string
	}{
		{"owner_principal_kind", constraints[0], "robot"},
		{"agent_visibility", constraints[1], "public"},
	} {
		for _, operation := range []string{"UPDATE", "INSERT"} {
			t.Run(tc.column+"_"+operation, func(t *testing.T) {
				require.NoError(t, tx.SavePoint("principal_enum_probe").Error)
				var writeErr error
				if operation == "UPDATE" {
					writeErr = tx.Exec(fmt.Sprintf(`UPDATE memories SET %s = ? WHERE id = ?`, tc.column), tc.invalid, memory.ID).Error
				} else {
					writeErr = tx.Exec(fmt.Sprintf(`INSERT INTO memories (project, content, %s) VALUES (?, 'invalid principal enum', ?)`, tc.column), project, tc.invalid).Error
				}
				require.NoError(t, tx.RollbackTo("principal_enum_probe").Error)
				require.NoError(t, tx.Exec(`RELEASE SAVEPOINT principal_enum_probe`).Error)
				var sqlError *pgconn.PgError
				if assert.ErrorAs(t, writeErr, &sqlError, "target-local enum CHECK must reject %s %s=%q", operation, tc.column, tc.invalid) {
					assert.Equal(t, "23514", sqlError.Code)
					assert.Equal(t, schema, sqlError.SchemaName)
					assert.Equal(t, "memories", sqlError.TableName)
					assert.Equal(t, tc.constraint, sqlError.ConstraintName, "not an incidental tenant/FK/name error")
					t.Logf("rejected by owned CHECK: sqlstate=%s schema=%s table=%s constraint=%s", sqlError.Code, sqlError.SchemaName, sqlError.TableName, sqlError.ConstraintName)
				}
			})
		}
	}
	assert.JSONEq(t, dataBefore, snapshot(dataSQL), "savepoint probes must not leak invalid rows or rewrite existing data")
}

func TestMemoryPrincipalConstraintsUpgradeInvalidRowsRollbackAndRestart(t *testing.T) {
	db, schema := openInterventionReceiptMigrationTestDB(t)
	ctx := context.Background()
	snapshot := func(query string, args ...any) string {
		t.Helper()
		var value string
		require.NoError(t, db.Raw(query, args...).Row().Scan(&value))
		return value
	}
	publicSQL := `SELECT jsonb_agg(jsonb_build_object('oid', oid, 'name', conname,
		'validated', convalidated, 'definition', pg_get_constraintdef(oid)) ORDER BY conname)::text
		FROM pg_constraint WHERE conrelid = 'public.memories'::regclass`
	publicBefore := snapshot(publicSQL)
	t.Cleanup(func() { assert.JSONEq(t, publicBefore, snapshot(publicSQL)) })
	project := sessionTestProject(t, db)
	store := NewMemoryStore(&Store{DB: db})
	memory, err := store.Create(ctx, &models.Memory{
		Project: project, Content: "preserve invalid-row repair fixture",
		OwnerPrincipal: "agent/regression", OwnerPrincipalKind: "agent",
		AgentVisibility: models.AgentVisibilityPrivate,
	})
	require.NoError(t, err)
	memory, err = store.Update(ctx, memory)
	require.NoError(t, err)
	require.Equal(t, 2, memory.Version)
	require.NoError(t, NewAuditStore(db).Log(ctx, AuditLogEntry{
		MemoryID: &memory.ID, Action: "principal_upgrade_fixture", Actor: "agent/regression",
		Reason: "must survive rejected migration and corrected-row restart",
	}))
	dataSQL := `SELECT jsonb_build_object(
		'memories', (SELECT jsonb_agg(to_jsonb(m) ORDER BY id) FROM memories m),
		'audit', (SELECT jsonb_agg(to_jsonb(a) ORDER BY id) FROM audit_log a),
		'projects', (SELECT jsonb_agg(to_jsonb(p) ORDER BY id) FROM projects p)
	)::text`
	validDataBefore := snapshot(dataSQL)
	migration := memoryPrincipalConstraintsMigration188()
	for _, name := range []string{"memories_owner_principal_kind_chk", "memories_agent_visibility_chk"} {
		require.NoError(t, db.Exec(fmt.Sprintf(`ALTER TABLE %q.memories DROP CONSTRAINT %s`, schema, name)).Error)
	}
	deleted := db.Exec(fmt.Sprintf(`DELETE FROM %q.migrations WHERE id = ?`, schema), migration.ID)
	require.NoError(t, deleted.Error)
	require.EqualValues(t, 1, deleted.RowsAffected)
	ledgerBefore := snapshot(`SELECT jsonb_agg(id ORDER BY id)::text FROM migrations`)

	// Visibility validates second, so its failure must undo the first newly added CHECK.
	for _, tc := range []struct {
		name, kind, visibility, constraint string
	}{
		{"second_CHECK_failure", "agent", "public", "memories_agent_visibility_chk"},
		{"first_CHECK_failure", "robot", "private", "memories_owner_principal_kind_chk"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			require.NoError(t, db.Exec(`UPDATE memories SET owner_principal_kind = ?, agent_visibility = ? WHERE id = ?`, tc.kind, tc.visibility, memory.ID).Error)
			before := snapshot(dataSQL)
			var sqlError *pgconn.PgError
			require.ErrorAs(t, runMigrations(db), &sqlError)
			require.Equal(t, "23514", sqlError.Code)
			require.Equal(t, schema, sqlError.SchemaName)
			require.Equal(t, "memories", sqlError.TableName)
			require.Equal(t, tc.constraint, sqlError.ConstraintName)
			var partialChecks, applied188 int
			require.NoError(t, db.Raw(`SELECT count(*) FROM pg_constraint WHERE conrelid = 'memories'::regclass
				AND conname IN ('memories_owner_principal_kind_chk', 'memories_agent_visibility_chk')`).Scan(&partialChecks).Error)
			require.NoError(t, db.Raw(`SELECT count(*) FROM migrations WHERE id = ?`, migration.ID).Scan(&applied188).Error)
			require.Zero(t, partialChecks, "a failed corrective migration must roll back every new CHECK")
			require.Zero(t, applied188)
			assert.JSONEq(t, before, snapshot(dataSQL), "failure must not rewrite invalid rows, version, audit or project data")
			assert.JSONEq(t, ledgerBefore, snapshot(`SELECT jsonb_agg(id ORDER BY id)::text FROM migrations`))
			t.Logf("owned invalid-row rollback: sqlstate=%s schema=%s constraint=%s partial_checks=%d applied188=%d", sqlError.Code, schema, tc.constraint, partialChecks, applied188)
		})
		if t.Failed() {
			return
		}
	}
	require.NoError(t, db.Exec(`UPDATE memories SET owner_principal_kind = 'agent', agent_visibility = 'private' WHERE id = ?`, memory.ID).Error)
	assert.JSONEq(t, validDataBefore, snapshot(dataSQL), "only the owned invalid enum fields were corrected")
	require.NoError(t, runMigrations(db), "restart after correction must finish the pending forward migration")
	var applied149, applied188, validatedChecks int
	require.NoError(t, db.Raw(`SELECT count(*) FROM migrations WHERE id = '149_memory_principals'`).Scan(&applied149).Error)
	require.NoError(t, db.Raw(`SELECT count(*) FROM migrations WHERE id = ?`, migration.ID).Scan(&applied188).Error)
	require.NoError(t, db.Raw(`SELECT count(*) FROM pg_constraint WHERE conrelid = 'memories'::regclass
		AND conname IN ('memories_owner_principal_kind_chk', 'memories_agent_visibility_chk')
		AND contype = 'c' AND convalidated`).Scan(&validatedChecks).Error)
	require.Equal(t, 1, applied149)
	require.Equal(t, 1, applied188)
	require.Equal(t, 2, validatedChecks)
	checksSQL := `SELECT jsonb_agg(jsonb_build_object('oid', oid, 'name', conname,
		'validated', convalidated, 'definition', pg_get_constraintdef(oid)) ORDER BY conname)::text
		FROM pg_constraint WHERE conrelid = 'memories'::regclass`
	checksBefore := snapshot(checksSQL)
	ledgerAfter := snapshot(`SELECT jsonb_agg(id ORDER BY id)::text FROM migrations`)
	require.NoError(t, runMigrations(db), "completed startup replay must be a no-op")
	require.NoError(t, migration.Migrate(db), "corrective DDL itself must also be idempotent")
	require.ErrorContains(t, migration.Rollback(db), "principal checks are required by migration 149")
	assert.JSONEq(t, checksBefore, snapshot(checksSQL), "replay or refused rollback must not replace/drop required CHECKs")
	assert.JSONEq(t, ledgerAfter, snapshot(`SELECT jsonb_agg(id ORDER BY id)::text FROM migrations`))
	assert.JSONEq(t, validDataBefore, snapshot(dataSQL), "successful repair/replay must preserve all valid durable data")
	t.Logf("corrected-row restart: applied149=%d applied188=%d validated_checks=%d; startup replay and DDL replay preserve CHECK OIDs; rollback refused", applied149, applied188, validatedChecks)
}
