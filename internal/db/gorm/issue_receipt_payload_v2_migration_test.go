package gorm

import (
	"bytes"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/stretchr/testify/require"
	issues "github.com/thebtf/engram/internal/issues/contract"
	gormlib "gorm.io/gorm"
)

func TestIssueReceiptPayloadV2Migration194Preserves193Receipts(t *testing.T) {
	db, _ := openInterventionReceiptMigrationTestDB(t)
	const source = "51300000-0000-4000-8000-000000000093"
	require.NoError(t, db.Create(&Project{ID: source}).Error)

	// The private fixture already ran 194. Restore only 193's original version
	// check on its empty receipt table; its actual 193 table, FK and trigger stay
	// installed. No migration ledger or schema outside this fixture is changed.
	require.NoError(t, db.Exec(`ALTER TABLE issue_create_receipts DROP CONSTRAINT issue_create_receipts_payload_version_check`).Error)
	require.NoError(t, db.Exec(`ALTER TABLE issue_create_receipts ADD CONSTRAINT issue_create_receipts_payload_version_check CHECK (payload_version=1)`).Error)

	seedIssue := func() int64 {
		t.Helper()
		created, err := NewIssueStore(db).CreateTaskIssue(t.Context(), issueSelectionTestActor{keycard: "synthetic-194-actor"}, issues.Scope{SourceProject: source}, issues.CreateCommand{
			Title: "Synthetic migration receipt", Body: "Receipt migration fixture", TargetProject: source,
			Priority: "medium", Type: "task", Labels: []string{}, Metadata: json.RawMessage(`{}`),
		})
		require.NoError(t, err)
		return created.ID
	}
	liveID, deletedID, v2ID := seedIssue(), seedIssue(), seedIssue()
	insert := func(key string, id int64, version int, hash []byte, created string) error {
		return db.Exec(`INSERT INTO issue_create_receipts
			(actor_identity_key,canonical_source_project_id,request_key,issue_id,payload_version,payload_hash,created_at)
			VALUES ('client:synthetic-194',?,?,?,?,?,?::timestamptz)`, source, key, id, version, hash, created).Error
	}
	liveHash, deletedHash, v2Hash := bytes.Repeat([]byte{0x31}, 32), bytes.Repeat([]byte{0x72}, 32), bytes.Repeat([]byte{0x93}, 32)
	const liveTime, deletedTime, v2Time = "2024-01-02T03:04:05Z", "2024-02-03T04:05:06Z", "2024-03-04T05:06:07Z"
	require.NoError(t, insert("v1-live", liveID, 1, liveHash, liveTime))
	require.NoError(t, insert("v1-tombstone", deletedID, 1, deletedHash, deletedTime))
	// This exercises the real FK-triggered transition, not a fabricated NULL row.
	require.NoError(t, db.Exec(`DELETE FROM issues WHERE id=?`, deletedID).Error)

	type receipt struct {
		ActorIdentityKey         string
		CanonicalSourceProjectID string
		RequestKey               string
		IssueID                  *int64
		PayloadVersion           int16
		PayloadHash              []byte
		CreatedAt                time.Time
	}
	readReceipt := func(key string) receipt {
		t.Helper()
		var row receipt
		err := db.Raw(`SELECT actor_identity_key,canonical_source_project_id,request_key,issue_id,payload_version,payload_hash,created_at
			FROM issue_create_receipts WHERE actor_identity_key=? AND canonical_source_project_id=? AND request_key=?`,
			"client:synthetic-194", source, key).Row().Scan(&row.ActorIdentityKey, &row.CanonicalSourceProjectID, &row.RequestKey, &row.IssueID, &row.PayloadVersion, &row.PayloadHash, &row.CreatedAt)
		require.NoError(t, err)
		return row
	}
	checkReceipt := func(key string, id *int64, version int16, hash []byte, created string) receipt {
		t.Helper()
		row := readReceipt(key)
		require.Equal(t, "client:synthetic-194", row.ActorIdentityKey)
		require.Equal(t, source, row.CanonicalSourceProjectID)
		require.Equal(t, key, row.RequestKey)
		require.Equal(t, id, row.IssueID)
		require.Equal(t, version, row.PayloadVersion)
		require.Equal(t, hash, row.PayloadHash)
		instant, err := time.Parse(time.RFC3339, created)
		require.NoError(t, err)
		require.True(t, instant.Equal(row.CreatedAt))
		return row
	}
	beforeLive := checkReceipt("v1-live", &liveID, 1, liveHash, liveTime)
	beforeTombstone := checkReceipt("v1-tombstone", nil, 1, deletedHash, deletedTime)
	history := func() string {
		t.Helper()
		var result string
		require.NoError(t, db.Raw(`SELECT COALESCE(jsonb_agg(to_jsonb(c) ORDER BY issue_id,revision),'[]'::jsonb)::text FROM issue_changes c`).Row().Scan(&result))
		return result
	}
	beforeHistory := history()

	// Roll back every negative probe even if a broken invariant admits it.
	reject := func(code string, operation func(*gormlib.DB) error) {
		t.Helper()
		unexpected := errors.New("probe unexpectedly succeeded")
		err := db.Transaction(func(tx *gormlib.DB) error {
			if err := operation(tx); err != nil {
				return err
			}
			return unexpected
		})
		var pgErr *pgconn.PgError
		require.ErrorAs(t, err, &pgErr)
		require.Equal(t, code, pgErr.Code)
	}
	reject("23514", func(tx *gormlib.DB) error {
		return tx.Exec(`INSERT INTO issue_create_receipts (actor_identity_key,canonical_source_project_id,request_key,issue_id,payload_version,payload_hash) VALUES ('client:synthetic-194',?,'premature-v2',?,2,?)`, source, v2ID, v2Hash).Error
	})

	migration := issueReceiptPayloadV2Migration194()
	require.NoError(t, migration.Migrate(db))
	require.Equal(t, beforeLive, checkReceipt("v1-live", &liveID, 1, liveHash, liveTime))
	require.Equal(t, beforeTombstone, checkReceipt("v1-tombstone", nil, 1, deletedHash, deletedTime))
	require.NoError(t, insert("v2-live", v2ID, 2, v2Hash, v2Time))
	beforeV2 := checkReceipt("v2-live", &v2ID, 2, v2Hash, v2Time)
	require.NoError(t, migration.Migrate(db), "replaying the real 194 wrapper must be safe")
	require.Equal(t, beforeLive, readReceipt("v1-live"))
	require.Equal(t, beforeTombstone, readReceipt("v1-tombstone"))
	require.Equal(t, beforeV2, readReceipt("v2-live"))
	require.Equal(t, beforeHistory, history())
	reject("55000", func(tx *gormlib.DB) error {
		return tx.Exec(`UPDATE issue_changes SET action='rewritten' WHERE issue_id=?`, liveID).Error
	})

	for _, key := range []string{"v1-live", "v1-tombstone", "v2-live"} {
		reject("55000", func(tx *gormlib.DB) error {
			return tx.Exec(`UPDATE issue_create_receipts SET payload_hash=? WHERE request_key=?`, bytes.Repeat([]byte{0x44}, 32), key).Error
		})
		reject("55000", func(tx *gormlib.DB) error {
			return tx.Exec(`DELETE FROM issue_create_receipts WHERE request_key=?`, key).Error
		})
	}
	reject("55000", func(tx *gormlib.DB) error {
		return tx.Exec(`UPDATE issue_create_receipts SET issue_id=NULL WHERE request_key='v1-live'`).Error
	})
	reject("55000", func(tx *gormlib.DB) error {
		return tx.Exec(`UPDATE issue_create_receipts SET issue_id=? WHERE request_key='v1-tombstone'`, liveID).Error
	})
	reject("23505", func(tx *gormlib.DB) error {
		return tx.Exec(`INSERT INTO issue_create_receipts (actor_identity_key,canonical_source_project_id,request_key,issue_id,payload_version,payload_hash) VALUES ('client:synthetic-194',?,'v1-tombstone',?,1,?)`, source, liveID, deletedHash).Error
	})
	reject("23514", func(tx *gormlib.DB) error {
		return tx.Exec(`INSERT INTO issue_create_receipts (actor_identity_key,canonical_source_project_id,request_key,payload_version,payload_hash) VALUES ('client:synthetic-194',?,'forged-tombstone',1,?)`, source, liveHash).Error
	})
	reject("23514", func(tx *gormlib.DB) error {
		return tx.Exec(`INSERT INTO issue_create_receipts (actor_identity_key,canonical_source_project_id,request_key,issue_id,payload_version,payload_hash) VALUES ('client:synthetic-194',?,'unknown-version',?,3,?)`, source, liveID, liveHash).Error
	})
	require.ErrorContains(t, migration.Rollback(db), "rollback refused")
	require.Equal(t, beforeLive, readReceipt("v1-live"))
	require.Equal(t, beforeTombstone, readReceipt("v1-tombstone"))
	require.Equal(t, beforeV2, readReceipt("v2-live"))

	// The altered constraint is deliberately unrecognized; refusal must leave it
	// and all receipt values untouched rather than guessing a safe conversion.
	require.NoError(t, db.Exec(`ALTER TABLE issue_create_receipts DROP CONSTRAINT issue_create_receipts_payload_version_check`).Error)
	require.NoError(t, db.Exec(`ALTER TABLE issue_create_receipts ADD CONSTRAINT issue_create_receipts_payload_version_check CHECK (payload_version IN (1,2,3))`).Error)
	var checkBefore, checkAfter string
	require.NoError(t, db.Raw(`SELECT pg_get_constraintdef(oid) FROM pg_constraint WHERE conrelid='issue_create_receipts'::regclass AND conname='issue_create_receipts_payload_version_check'`).Row().Scan(&checkBefore))
	require.ErrorContains(t, migration.Migrate(db), "unrecognized receipt version definition")
	require.NoError(t, db.Raw(`SELECT pg_get_constraintdef(oid) FROM pg_constraint WHERE conrelid='issue_create_receipts'::regclass AND conname='issue_create_receipts_payload_version_check'`).Row().Scan(&checkAfter))
	require.Equal(t, checkBefore, checkAfter)
	require.Equal(t, beforeLive, readReceipt("v1-live"))
	require.Equal(t, beforeTombstone, readReceipt("v1-tombstone"))
	require.Equal(t, beforeV2, readReceipt("v2-live"))
	require.Equal(t, beforeHistory, history())
	// After widening, the original trigger still admits only the FK's
	// live-to-tombstone transition and retains the rest of the binding.
	require.NoError(t, db.Exec(`DELETE FROM issues WHERE id=?`, liveID).Error)
	checkReceipt("v1-live", nil, 1, liveHash, liveTime)
	require.Equal(t, beforeHistory, history())
}
