package gorm

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/lib/pq"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	gormlib "gorm.io/gorm"
)

func sessionHandleMigrationNoError(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal("synthetic session migration operation failed; sensitive details omitted")
	}
}

func sessionHandleMigrationCredential(t *testing.T) string {
	t.Helper()
	var credential [32]byte
	_, err := rand.Read(credential[:])
	sessionHandleMigrationNoError(t, err)
	return hex.EncodeToString(credential[:])
}

// This independent oracle implements the selected byte contract, not a new
// production symbol. Neither credentials nor query/error/plan dumps are printed.
func sessionHandleMigrationExpected(credential string) string {
	digest := sha256.Sum256([]byte("engram/browser-session-management/v1:" + credential))
	return "bsh1_" + hex.EncodeToString(digest[:])
}

func TestBrowserSessionHandleMigration190UpgradeReplayAndLegacyWriters(t *testing.T) {
	const migrationID = "190_browser_session_public_handle"
	const triggerName = "engram_browser_session_public_handle"
	const functionName = "engram_browser_session_public_handle_sync"
	const indexName = "idx_sessions_public_handle"
	db, schema := openInterventionReceiptMigrationTestDB(t)
	var actualSchema string
	sessionHandleMigrationNoError(t, db.Raw(`SELECT current_schema()`).Scan(&actualSchema).Error)
	require.True(t, schema == actualSchema && strings.HasPrefix(schema, "intervention_receipt_test_") && schema != "public", "rewind is restricted to this fixture's UUID schema")
	qualifiedSchema := pq.QuoteIdentifier(schema)
	sessionsTable := qualifiedSchema + `.sessions`
	migrationsTable := qualifiedSchema + `.migrations`
	auditTable := qualifiedSchema + `.audit_log`

	// Only the named additive migration is unwound, inside the already-migrated
	// isolated fixture. Never invoke a production rollback or touch public rows.
	for _, statement := range []string{
		`DROP TRIGGER IF EXISTS ` + triggerName + ` ON ` + sessionsTable,
		`DROP INDEX IF EXISTS ` + qualifiedSchema + `.` + indexName,
		`ALTER TABLE ` + sessionsTable + ` DROP COLUMN IF EXISTS public_handle`,
		`DROP FUNCTION IF EXISTS ` + qualifiedSchema + `.` + functionName + `()`,
	} {
		sessionHandleMigrationNoError(t, db.Exec(statement).Error)
	}
	sessionHandleMigrationNoError(t, db.Exec(`DELETE FROM `+migrationsTable+` WHERE id = ?`, migrationID).Error)
	applied := func(id string) int64 {
		t.Helper()
		var count int64
		sessionHandleMigrationNoError(t, db.Raw(`SELECT count(*) FROM `+migrationsTable+` WHERE id = ?`, id).Scan(&count).Error)
		return count
	}
	hasPublicHandle := func() bool {
		t.Helper()
		var count int64
		sessionHandleMigrationNoError(t, db.Raw(`SELECT count(*) FROM information_schema.columns
			WHERE table_schema = ? AND table_name = 'sessions' AND column_name = 'public_handle'`, schema).Scan(&count).Error)
		return count == 1
	}
	require.Equal(t, int64(1), applied("189_issue_project_write_scope"), "the existing migration chain must remain intact")
	require.Zero(t, applied(migrationID))
	require.False(t, hasPublicHandle(), "the fixture must represent the pre-190 session shape")

	user, err := NewUserStore(db).CreateUser("session535-migration@example.test", "", DashboardRoleAdmin)
	sessionHandleMigrationNoError(t, err)
	now := time.Now().UTC().Truncate(time.Microsecond)
	type legacySession struct {
		credential string
		status     string
		expiresAt  time.Time
		revokedAt  *time.Time
		revokedBy  *int64
		reason     string
		agent      string
	}
	revokedAt := now.Add(-time.Minute)
	legacy := []legacySession{
		{credential: sessionHandleMigrationCredential(t), status: "active", expiresAt: now.Add(12 * time.Hour), agent: "session535-migration-active"},
		{credential: sessionHandleMigrationCredential(t), status: "expired", expiresAt: now.Add(-time.Hour), agent: "session535-migration-expired"},
		{credential: sessionHandleMigrationCredential(t), status: "revoked", expiresAt: now.Add(12 * time.Hour), revokedAt: &revokedAt, revokedBy: &user.ID, reason: "session535 historical revoke", agent: "session535-migration-revoked"},
	}
	var legacyIDs []string
	for _, row := range legacy {
		// Explicit old-writer SQL must compile and seed before the new column
		// exists, even when the future Go AuthSession model gains that field.
		sessionHandleMigrationNoError(t, db.Exec(`INSERT INTO `+sessionsTable+`
			(id, user_id, created_at, expires_at, user_agent, remote_addr, revoked_at, revoked_by, revocation_reason)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`, row.credential, user.ID, now.Add(-2*time.Hour), row.expiresAt,
			row.agent, "192.0.2.190:5350", row.revokedAt, row.revokedBy, row.reason).Error)
		legacyIDs = append(legacyIDs, row.credential)
	}
	beforeState, err := json.Marshal(map[string]any{"session_id": legacy[2].credential, "user_id": user.ID})
	sessionHandleMigrationNoError(t, err)
	afterState, err := json.Marshal(map[string]any{"session_id": legacy[2].credential, "revoked_by": user.ID})
	sessionHandleMigrationNoError(t, err)
	beforeRaw, afterRaw := json.RawMessage(beforeState), json.RawMessage(afterState)
	sessionHandleMigrationNoError(t, db.Create(&AuditLogEntry{
		Action: "auth_session_revoked", Actor: user.Email, Reason: "session535 retained historical audit",
		BeforeState: &beforeRaw, AfterState: &afterRaw, CreatedAt: now.Add(-time.Minute),
	}).Error)
	snapshot := func(query string, args ...any) string {
		t.Helper()
		var data string
		sessionHandleMigrationNoError(t, db.Raw(query, args...).Row().Scan(&data))
		return data
	}
	legacySnapshotSQL := `SELECT jsonb_agg(to_jsonb(s) - 'public_handle' ORDER BY id)::text
		FROM ` + sessionsTable + ` s WHERE id IN ?`
	auditSnapshotSQL := `SELECT jsonb_agg(to_jsonb(a) ORDER BY id)::text FROM ` + auditTable + ` a`
	legacyBefore := snapshot(legacySnapshotSQL, legacyIDs)
	auditBefore := snapshot(auditSnapshotSQL)

	// A real backfill UPDATE fails inside the selected migration. Its DDL,
	// rows and marker must roll back together; no simulated migrator is used.
	faultFunction := qualifiedSchema + `.session535_fail_handle_backfill`
	faultTrigger := `session535_fail_handle_backfill`
	sessionHandleMigrationNoError(t, db.Exec(`CREATE FUNCTION `+faultFunction+`() RETURNS trigger
		LANGUAGE plpgsql AS $$ BEGIN
		RAISE EXCEPTION 'session535 controlled backfill failure' USING ERRCODE = 'P0001';
		END; $$`).Error)
	sessionHandleMigrationNoError(t, db.Exec(`CREATE TRIGGER `+faultTrigger+` BEFORE UPDATE ON `+sessionsTable+`
		FOR EACH ROW EXECUTE FUNCTION `+faultFunction+`()`).Error)
	migrationErr := runMigrations(db)
	var controlledFailure *pgconn.PgError
	assert.True(t, errors.As(migrationErr, &controlledFailure) && controlledFailure.Code == "P0001" && controlledFailure.Message == "session535 controlled backfill failure", "the real migration must surface the controlled backfill failure")
	assert.False(t, hasPublicHandle(), "failed backfill must not leave additive DDL committed")
	assert.Zero(t, applied(migrationID), "failed backfill must not commit its migration marker")
	assert.True(t, legacyBefore == snapshot(legacySnapshotSQL, legacyIDs), "failed backfill must leave old credentials and lifecycle metadata unchanged")
	assert.True(t, auditBefore == snapshot(auditSnapshotSQL), "failed backfill must leave historical audit bytes unchanged")
	sessionHandleMigrationNoError(t, db.Exec(`DROP TRIGGER `+faultTrigger+` ON `+sessionsTable).Error)
	sessionHandleMigrationNoError(t, db.Exec(`DROP FUNCTION `+faultFunction+`()`).Error)

	sessionHandleMigrationNoError(t, runMigrations(db))
	require.True(t, hasPublicHandle(), "migration 190 must add and backfill stable browser management handles")
	require.Equal(t, int64(1), applied(migrationID))
	assert.True(t, legacyBefore == snapshot(legacySnapshotSQL, legacyIDs), "successful migration must preserve every original credential, user link and lifecycle/agent/address field")
	assert.True(t, auditBefore == snapshot(auditSnapshotSQL), "successful migration must not rewrite historical audit rows")
	store := NewAuthSessionStore(db)
	seen := make(map[string]bool)
	for _, row := range legacy {
		t.Run(row.status, func(t *testing.T) {
			var handle string
			sessionHandleMigrationNoError(t, db.Raw(`SELECT public_handle FROM `+sessionsTable+` WHERE id = ?`, row.credential).Row().Scan(&handle))
			require.True(t, handle == sessionHandleMigrationExpected(row.credential) && handle != row.credential, "backfill must use the selected canonical UTF-8 domain transform")
			assert.False(t, seen[handle], "different old sessions need distinct public handles")
			seen[handle] = true
			var selectedCredential string
			sessionHandleMigrationNoError(t, db.Raw(`SELECT id FROM `+sessionsTable+` WHERE public_handle = ?`, handle).Row().Scan(&selectedCredential))
			assert.True(t, selectedCredential == row.credential, "exact public-handle lookup must select only its original session")
			retained, err := store.GetAnySession(row.credential)
			sessionHandleMigrationNoError(t, err)
			require.True(t, retained != nil && retained.ID == row.credential && retained.UserID == user.ID, "old cookie lookup must retain its original session identity")
			assert.True(t, retained.ExpiresAt.Equal(row.expiresAt) && retained.UserAgent == row.agent && retained.RemoteAddr == "192.0.2.190:5350" && retained.RevocationReason == row.reason, "legacy lifecycle and diagnostic metadata must survive")
			resolved, err := store.GetSession(row.credential)
			switch row.status {
			case "active":
				sessionHandleMigrationNoError(t, err)
				assert.True(t, resolved != nil && resolved.ID == row.credential, "active old cookies must remain valid")
			case "expired":
				assert.True(t, errors.Is(err, ErrAuthSessionExpired), "migration must not reactivate expired cookies")
			case "revoked":
				assert.True(t, errors.Is(err, ErrAuthSessionRevoked), "migration must not reactivate revoked cookies")
				require.True(t, retained.RevokedAt != nil && retained.RevokedBy != nil)
				assert.True(t, retained.RevokedAt.Equal(*row.revokedAt) && *retained.RevokedBy == user.ID)
			}
			_, handleAsCookieErr := store.GetSession(handle)
			assert.True(t, errors.Is(handleAsCookieErr, gormlib.ErrRecordNotFound), "public management handles must not become cookie credentials")
		})
	}

	// Simulate an older binary inserting and updating only its known columns.
	oldWriterCredential := sessionHandleMigrationCredential(t)
	sessionHandleMigrationNoError(t, db.Exec(`INSERT INTO `+sessionsTable+`
		(id, user_id, created_at, expires_at, user_agent, remote_addr)
		VALUES (?, ?, ?, ?, ?, ?)`, oldWriterCredential, user.ID, now, now.Add(12*time.Hour), "session535-old-writer", "192.0.2.190:5350").Error)
	var oldWriterHandle string
	sessionHandleMigrationNoError(t, db.Raw(`SELECT public_handle FROM `+sessionsTable+` WHERE id = ?`, oldWriterCredential).Row().Scan(&oldWriterHandle))
	assert.True(t, oldWriterHandle == sessionHandleMigrationExpected(oldWriterCredential), "legacy SQL INSERT omitting public_handle must receive the canonical value")
	sessionHandleMigrationNoError(t, db.Exec(`UPDATE `+sessionsTable+` SET user_agent = ? WHERE id = ?`, "session535-old-writer-updated", oldWriterCredential).Error)
	var afterMetadataUpdate string
	sessionHandleMigrationNoError(t, db.Raw(`SELECT public_handle FROM `+sessionsTable+` WHERE id = ?`, oldWriterCredential).Row().Scan(&afterMetadataUpdate))
	assert.True(t, afterMetadataUpdate == oldWriterHandle, "ordinary old-writer updates must retain the handle")
	replacementCredential := sessionHandleMigrationCredential(t)
	sessionHandleMigrationNoError(t, db.Exec(`UPDATE `+sessionsTable+` SET id = ? WHERE id = ?`, replacementCredential, oldWriterCredential).Error)
	var replacementHandle string
	sessionHandleMigrationNoError(t, db.Raw(`SELECT public_handle FROM `+sessionsTable+` WHERE id = ?`, replacementCredential).Row().Scan(&replacementHandle))
	assert.True(t, replacementHandle == sessionHandleMigrationExpected(replacementCredential) && replacementHandle != oldWriterHandle, "the compatibility UPDATE trigger must synchronize a changed credential")
	_, err = store.GetSession(replacementCredential)
	sessionHandleMigrationNoError(t, err)
	_, err = store.GetSession(oldWriterCredential)
	assert.True(t, errors.Is(err, gormlib.ErrRecordNotFound), "changing only the synthetic old-writer row must not leave an alias credential")
	assert.True(t, legacyBefore == snapshot(legacySnapshotSQL, legacyIDs), "compatibility writes must not mutate other original sessions")

	var uniqueIndexes int64
	sessionHandleMigrationNoError(t, db.Raw(`SELECT count(*)
		FROM pg_index i
		JOIN pg_class idx ON idx.oid = i.indexrelid
		JOIN pg_class tbl ON tbl.oid = i.indrelid
		JOIN pg_namespace n ON n.oid = tbl.relnamespace
		JOIN pg_attribute a ON a.attrelid = tbl.oid AND a.attnum = i.indkey[0]
		WHERE n.nspname = ? AND tbl.relname = 'sessions' AND idx.relname = ?
		  AND i.indisunique AND i.indisvalid AND i.indisready AND i.indnkeyatts = 1
		  AND i.indexprs IS NULL AND i.indpred IS NULL AND a.attname = 'public_handle'`, schema, indexName).Scan(&uniqueIndexes).Error)
	require.Equal(t, int64(1), uniqueIndexes, "all lifecycle states require one valid unique column index")
	// Give the default planner a representative synthetic table. Do not force
	// enable_seqscan=off: the observed plan must actually choose the unique index.
	fillerPrefix := sessionHandleMigrationCredential(t)[:56]
	sessionHandleMigrationNoError(t, db.Exec(`INSERT INTO `+sessionsTable+`
		(id, user_id, created_at, expires_at, user_agent, remote_addr)
		SELECT ? || lpad(to_hex(n), 8, '0'), ?, ?, ?, ?, ?
		FROM generate_series(1, 4096) AS fixture(n)`, fillerPrefix, user.ID, now, now.Add(12*time.Hour), "session535-synthetic-plan-row", "192.0.2.190:5350").Error)
	sessionHandleMigrationNoError(t, db.Exec(`ANALYZE `+sessionsTable).Error)
	var planJSON string
	sessionHandleMigrationNoError(t, db.Raw(`EXPLAIN (FORMAT JSON) SELECT id FROM `+sessionsTable+` WHERE public_handle = ?`, replacementHandle).Row().Scan(&planJSON))
	type planNode struct {
		NodeType  string     `json:"Node Type"`
		IndexName string     `json:"Index Name"`
		IndexCond string     `json:"Index Cond"`
		Plans     []planNode `json:"Plans"`
	}
	var plans []struct {
		Plan planNode `json:"Plan"`
	}
	sessionHandleMigrationNoError(t, json.Unmarshal([]byte(planJSON), &plans))
	require.Equal(t, 1, len(plans))
	usedHandleIndex, scannedTable := false, false
	var visit func(planNode)
	visit = func(node planNode) {
		if node.IndexName == indexName && (node.NodeType == "Index Scan" || node.NodeType == "Index Only Scan" || node.NodeType == "Bitmap Index Scan") && strings.Contains(node.IndexCond, "public_handle") {
			usedHandleIndex = true
		}
		if strings.Contains(node.NodeType, "Seq Scan") || node.NodeType == "Sample Scan" {
			scannedTable = true
		}
		for _, child := range node.Plans {
			visit(child)
		}
	}
	visit(plans[0].Plan)
	assert.True(t, usedHandleIndex && !scannedTable, "default public-handle equality lookup must use the unique index without scanning the table; plan omitted")

	allSessionsSQL := `SELECT jsonb_agg(to_jsonb(s) ORDER BY id)::text FROM ` + sessionsTable + ` s`
	allSessionsBeforeReplay := snapshot(allSessionsSQL)
	for _, markerReplay := range []bool{false, true} {
		if markerReplay {
			sessionHandleMigrationNoError(t, db.Exec(`DELETE FROM `+migrationsTable+` WHERE id = ?`, migrationID).Error)
		}
		sessionHandleMigrationNoError(t, runMigrations(db))
		assert.Equal(t, int64(1), applied(migrationID), "startup and marker-only replay must register exactly once")
		assert.True(t, allSessionsBeforeReplay == snapshot(allSessionsSQL), "replay must preserve every credential, handle and lifecycle field")
		assert.True(t, auditBefore == snapshot(auditSnapshotSQL), "replay and compatibility writes must preserve historical audit bytes")
	}
	// Marker-only replay must also leave old binaries able to issue sessions.
	postReplayCredential := sessionHandleMigrationCredential(t)
	sessionHandleMigrationNoError(t, db.Exec(`INSERT INTO `+sessionsTable+` (id, user_id, created_at, expires_at)
		VALUES (?, ?, ?, ?)`, postReplayCredential, user.ID, now, now.Add(12*time.Hour)).Error)
	var postReplayHandle string
	sessionHandleMigrationNoError(t, db.Raw(`SELECT public_handle FROM `+sessionsTable+` WHERE id = ?`, postReplayCredential).Row().Scan(&postReplayHandle))
	assert.True(t, postReplayHandle == sessionHandleMigrationExpected(postReplayCredential), "replay must retain the old-writer compatibility trigger")
	assert.True(t, auditBefore == snapshot(auditSnapshotSQL), "the migration contract must retain original audit rows through its final consumer")
}
