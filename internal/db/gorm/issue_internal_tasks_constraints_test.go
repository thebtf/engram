package gorm

import (
	"bytes"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/lib/pq"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/thebtf/engram/internal/issuefixture"
	issues "github.com/thebtf/engram/internal/issues/contract"
	gormlib "gorm.io/gorm"
)

// Store-layer fixture; transport identity is exercised by the HTTP tests.
type issueSelectionTestActor struct {
	keycard string
}

func (a issueSelectionTestActor) Descriptor() issues.ActorDescriptor {
	reference := "client:" + a.keycard
	return issues.ActorDescriptor{Kind: "client", Reference: &reference}
}
func (a issueSelectionTestActor) Namespace() string { return "client:" + a.keycard }
func (a issueSelectionTestActor) KeycardID() string { return a.keycard }
func (a issueSelectionTestActor) IsOperator() bool  { return false }
func (a issueSelectionTestActor) CanWrite() bool    { return a.keycard != "" }

func TestIssueTaskParticipantAuthority(t *testing.T) {
	row := &Issue{CreatorKeycardID: "owner"}
	participant := issueSelectionTestActor{keycard: "participant"}
	for _, status := range []string{"resolved", "closed", "reopened"} {
		command := issues.MutationCommand{Status: &status}
		require.NoError(t, authorizeTaskMutation(row, participant, command))
		require.Error(t, authorizeTaskMutation(row, issueSelectionTestActor{}, command))
		title := "protected"
		command.Title = &title
		require.Error(t, authorizeTaskMutation(row, participant, command))
	}
	for _, status := range []string{"open", "acknowledged", "rejected"} {
		require.Error(t, authorizeTaskMutation(row, participant, issues.MutationCommand{Status: &status}))
	}
}

func TestIssueInternalTasksConstraints(t *testing.T) {
	db, _ := openInterventionReceiptMigrationTestDB(t)
	const source = "51300000-0000-4000-8000-000000000094"
	const otherSource = "51300000-0000-4000-8000-000000000095"
	for _, project := range []string{source, otherSource} {
		require.NoError(t, db.Create(&Project{ID: project}).Error)
	}
	seedIssue := func(t *testing.T) int64 {
		t.Helper()
		actor := issueSelectionTestActor{keycard: "synthetic-schema-actor"}
		created, err := NewIssueStore(db).CreateTaskIssue(t.Context(), actor, issues.Scope{SourceProject: source}, issues.CreateCommand{
			Title: "Synthetic schema task", Body: "Synthetic schema body", TargetProject: source,
			Priority: "medium", Type: "task", Labels: []string{}, Metadata: json.RawMessage(`{}`),
		})
		require.NoError(t, err)
		return created.ID
	}
	seedReceipt := func(t *testing.T, id int64, actor, project, key string) issueCreateReceipt {
		t.Helper()
		receipt := issueCreateReceipt{
			ActorIdentityKey: actor, CanonicalSourceProjectID: project, RequestKey: key,
			IssueID: &id, PayloadVersion: 1, PayloadHash: bytes.Repeat([]byte{0x51}, 32),
		}
		require.NoError(t, db.Create(&receipt).Error)
		require.NoError(t, db.Where("actor_identity_key=? AND canonical_source_project_id=? AND request_key=?", actor, project, key).First(&receipt).Error)
		return receipt
	}
	// Every negative probe rolls back even if the database wrongly accepts it.
	// SQLSTATE distinguishes a rejected invariant from a missing column/syntax error.
	reject := func(t *testing.T, code string, operation func(*gormlib.DB) error) {
		t.Helper()
		unexpectedSuccess := errors.New("synthetic constraint probe unexpectedly succeeded")
		err := db.Transaction(func(tx *gormlib.DB) error {
			if err := operation(tx); err != nil {
				return err
			}
			return unexpectedSuccess
		})
		var postgresError *pgconn.PgError
		if assert.True(t, errors.As(err, &postgresError), "probe must fail with a PostgreSQL invariant error") {
			assert.Equal(t, code, postgresError.Code)
		}
	}

	t.Run("metadata_and_revision_models_roundtrip_without_numeric_rounding", func(t *testing.T) {
		const metadata = `{"unknown":{"safe_integer":9007199254740991,"fraction":0.1,"nested":[true,null,"kept"]}}`
		issue := Issue{Title: "Synthetic model task", SourceProject: source, TargetProject: source, Metadata: json.RawMessage(metadata)}
		require.NoError(t, issuefixture.SeedHistorical(db, func(tx *gormlib.DB) error { return tx.Create(&issue).Error }))
		var read Issue
		require.NoError(t, db.First(&read, issue.ID).Error)
		assert.JSONEq(t, metadata, string(read.Metadata))
		assert.EqualValues(t, 1, read.Revision)
		assert.Equal(t, "unknown", read.Actor.Kind)
		assert.Nil(t, read.Actor.Reference)
		comment := IssueComment{IssueID: issue.ID, AuthorProject: source, AuthorAgent: "synthetic-model-author", Body: "Synthetic outcome", Metadata: json.RawMessage(metadata)}
		require.NoError(t, issuefixture.SeedHistorical(db, func(tx *gormlib.DB) error { return tx.Create(&comment).Error }))
		var readComment IssueComment
		require.NoError(t, db.First(&readComment, comment.ID).Error)
		assert.JSONEq(t, metadata, string(readComment.Metadata))
		wire, err := json.Marshal(read)
		require.NoError(t, err)
		var fields map[string]json.RawMessage
		require.NoError(t, json.Unmarshal(wire, &fields))
		assert.JSONEq(t, metadata, string(fields["metadata"]))
		assert.JSONEq(t, `"1"`, string(fields["revision"]))
		assert.JSONEq(t, `{"kind":"unknown","reference":null}`, string(fields["actor"]))
		maxWire, err := json.Marshal(Issue{Revision: 9223372036854775807})
		require.NoError(t, err)
		require.NoError(t, json.Unmarshal(maxWire, &fields))
		assert.JSONEq(t, `"9223372036854775807"`, string(fields["revision"]), "wire revision cannot be a rounded JavaScript number")
	})

	t.Run("historical_fixture_restores_exact_guard_modes_and_rolls_back_failed_seed", func(t *testing.T) {
		for _, mode := range []struct{ issues, comments string }{{"ENABLE", "ENABLE ALWAYS"}, {"ENABLE ALWAYS", "ENABLE REPLICA"}, {"ENABLE REPLICA", "DISABLE"}, {"DISABLE", "ENABLE"}} {
			for _, failSeed := range []bool{false, true} {
				tx := db.Begin()
				require.NoError(t, tx.Error)
				func() {
					defer func() { require.NoError(t, tx.Rollback().Error) }()
					require.NoError(t, tx.Exec("ALTER TABLE issues "+mode.issues+" TRIGGER engram_issue_insert_actor_required").Error)
					require.NoError(t, tx.Exec("ALTER TABLE issue_comments "+mode.comments+" TRIGGER engram_issue_insert_actor_required").Error)
					guardSnapshot := func() string {
						var value string
						require.NoError(t, tx.Raw(`SELECT jsonb_agg(jsonb_build_object('table',c.relname,'mode',t.tgenabled,'definition',pg_get_triggerdef(t.oid)) ORDER BY c.relname,t.tgname)::text
							FROM pg_trigger t JOIN pg_class c ON c.oid=t.tgrelid JOIN pg_namespace n ON n.oid=c.relnamespace
							WHERE n.nspname=current_schema() AND c.relname IN ('issues','issue_comments','issue_changes','issue_create_receipts') AND NOT t.tgisinternal`).Row().Scan(&value))
						return value
					}
					before := guardSnapshot()
					seedFailure := errors.New("synthetic historical fixture seed failure")
					issue := Issue{Title: "Synthetic historical fixture transaction", SourceProject: source, TargetProject: source}
					comment := IssueComment{AuthorProject: source, Body: "Synthetic historical unknown actor"}
					err := issuefixture.SeedHistorical(tx, func(seed *gormlib.DB) error {
						if err := seed.Create(&issue).Error; err != nil {
							return err
						}
						comment.IssueID = issue.ID
						if err := seed.Create(&comment).Error; err != nil {
							return err
						}
						if failSeed {
							return seedFailure
						}
						return nil
					})
					assert.Equal(t, before, guardSnapshot(), "all guard identities and individual O/A/R/D modes must be unchanged")
					var issueCount, commentCount int64
					require.NoError(t, tx.Model(&Issue{}).Where("id=?", issue.ID).Count(&issueCount).Error)
					require.NoError(t, tx.Model(&IssueComment{}).Where("id=?", comment.ID).Count(&commentCount).Error)
					if failSeed {
						require.ErrorIs(t, err, seedFailure)
						assert.Zero(t, issueCount)
						assert.Zero(t, commentCount)
					} else {
						require.NoError(t, err)
						assert.EqualValues(t, 1, issueCount)
						assert.EqualValues(t, 1, commentCount)
						assert.Equal(t, "unknown", issue.Actor.Kind)
						assert.Nil(t, issue.Actor.Reference)
						assert.Equal(t, "unknown", comment.Actor.Kind)
						assert.Nil(t, comment.Actor.Reference)
					}
				}()
			}
		}
	})

	t.Run("metadata_actor_label_and_revision_constraints_reject_invalid_storage", func(t *testing.T) {
		id := seedIssue(t)
		for _, test := range []struct{ name, sql string }{
			{"nonobject issue metadata", `UPDATE issues SET metadata='[]' WHERE id=?`},
			{"null JSON metadata", `UPDATE issues SET metadata='null' WHERE id=?`},
			{"zero revision", `UPDATE issues SET revision=0 WHERE id=?`},
			{"revision skip", `UPDATE issues SET revision=3 WHERE id=?`},
			{"empty owner", `UPDATE issues SET owner_label='' WHERE id=?`},
			{"empty recipient", `UPDATE issues SET recipient_label='' WHERE id=?`},
			{"unknown actor with reference", `UPDATE issues SET actor_kind='unknown',actor_reference='synthetic-ref' WHERE id=?`},
			{"known actor without reference", `UPDATE issues SET actor_kind='client',actor_reference=NULL WHERE id=?`},
		} {
			t.Run(test.name, func(t *testing.T) {
				reject(t, "23514", func(tx *gormlib.DB) error { return tx.Exec(test.sql, id).Error })
			})
		}
		require.NoError(t, db.Exec(`UPDATE issues SET revision=2 WHERE id=?`, id).Error)
		reject(t, "23514", func(tx *gormlib.DB) error { return tx.Exec(`UPDATE issues SET revision=1 WHERE id=?`, id).Error })
		reject(t, "23514", func(tx *gormlib.DB) error {
			return tx.Exec(`INSERT INTO issue_comments (issue_id,author_project,body,metadata,actor_kind,actor_reference) VALUES (?,?,'Synthetic invalid comment','[]','client','synthetic-metadata-control')`, id, source).Error
		})
	})

	t.Run("receipt_namespace_and_binding_constraints", func(t *testing.T) {
		id := seedIssue(t)
		seedReceipt(t, id, "client:synthetic-a", source, "same-key")
		reject(t, "23505", func(tx *gormlib.DB) error {
			return tx.Create(&issueCreateReceipt{ActorIdentityKey: "client:synthetic-a", CanonicalSourceProjectID: source, RequestKey: "same-key", IssueID: &id, PayloadVersion: 1, PayloadHash: bytes.Repeat([]byte{0x52}, 32)}).Error
		})
		seedReceipt(t, seedIssue(t), "client:synthetic-b", source, "same-key")
		seedReceipt(t, seedIssue(t), "client:synthetic-a", otherSource, "same-key")
		var count int64
		require.NoError(t, db.Table("issue_create_receipts").Where("request_key = ?", "same-key").Count(&count).Error)
		assert.EqualValues(t, 3, count, "actor and canonical source both participate in uniqueness")
		versionTwoID := seedIssue(t)
		require.NoError(t, db.Create(&issueCreateReceipt{ActorIdentityKey: "client:synthetic-v2", CanonicalSourceProjectID: source, RequestKey: "v2-key", IssueID: &versionTwoID, PayloadVersion: 2, PayloadHash: bytes.Repeat([]byte{0x53}, 32)}).Error)
		for _, test := range []struct {
			name    string
			key     string
			version int16
			hash    []byte
		}{
			{name: "empty key", key: "", version: 1, hash: bytes.Repeat([]byte{0x51}, 32)},
			{name: "oversized UTF8 key", key: strings.Repeat("я", 257), version: 1, hash: bytes.Repeat([]byte{0x51}, 32)},
			{name: "unknown payload version", key: "version", version: 3, hash: bytes.Repeat([]byte{0x51}, 32)},
			{name: "short digest", key: "digest", version: 1, hash: []byte{0x51}},
		} {
			t.Run(test.name, func(t *testing.T) {
				reject(t, "23514", func(tx *gormlib.DB) error {
					return tx.Create(&issueCreateReceipt{ActorIdentityKey: "client:synthetic-a", CanonicalSourceProjectID: source, RequestKey: test.key, IssueID: &id, PayloadVersion: test.version, PayloadHash: test.hash}).Error
				})
			})
		}
		seedReceipt(t, seedIssue(t), "client:synthetic-a", source, strings.Repeat("я", 256))
		reject(t, "23514", func(tx *gormlib.DB) error {
			return tx.Create(&issueCreateReceipt{ActorIdentityKey: "client:synthetic-a", CanonicalSourceProjectID: source, RequestKey: "fake-tombstone", PayloadVersion: 1, PayloadHash: bytes.Repeat([]byte{0x51}, 32)}).Error
		})
		missing := int64(9223372036854775807)
		reject(t, "23503", func(tx *gormlib.DB) error {
			return tx.Create(&issueCreateReceipt{ActorIdentityKey: "client:synthetic-a", CanonicalSourceProjectID: source, RequestKey: "missing-issue", IssueID: &missing, PayloadVersion: 1, PayloadHash: bytes.Repeat([]byte{0x51}, 32)}).Error
		})
	})

	t.Run("change_history_is_truthful_unique_and_append_only", func(t *testing.T) {
		id := seedIssue(t)
		var initial IssueChange
		require.NoError(t, db.Where("issue_id=? AND revision=1 AND action='created'", id).First(&initial).Error)
		reject(t, "23505", func(tx *gormlib.DB) error {
			return tx.Exec(`INSERT INTO issue_changes (issue_id,revision,action) VALUES (?,1,'baseline')`, id).Error
		})
		require.NoError(t, db.Exec(`UPDATE issues SET revision=2 WHERE id=?`, id).Error)
		reject(t, "23514", func(tx *gormlib.DB) error {
			return tx.Exec(`INSERT INTO issue_changes (issue_id,revision,action) VALUES (?,2,'updated')`, id).Error
		})
		reject(t, "23514", func(tx *gormlib.DB) error {
			return tx.Exec(`INSERT INTO issue_changes (issue_id,revision,previous_revision,action) VALUES (?,3,2,'updated')`, id).Error
		})
		require.NoError(t, db.Exec(`INSERT INTO issue_changes (issue_id,revision,previous_revision,action,changed_fields) VALUES (?,2,1,'updated','["priority"]')`, id).Error)
		reject(t, "55000", func(tx *gormlib.DB) error {
			return tx.Exec(`UPDATE issue_changes SET action='rewritten' WHERE issue_id=?`, id).Error
		})
		reject(t, "55000", func(tx *gormlib.DB) error { return tx.Exec(`DELETE FROM issue_changes WHERE issue_id=?`, id).Error })
		baselineID := seedIssue(t)
		reject(t, "23514", func(tx *gormlib.DB) error {
			return tx.Exec(`INSERT INTO issue_changes (issue_id,revision,action,actor_kind,actor_reference) VALUES (?,1,'baseline','client','synthetic-claimed-author')`, baselineID).Error
		})
		reject(t, "23514", func(tx *gormlib.DB) error {
			return tx.Exec(`INSERT INTO issue_changes (issue_id,revision,action,changes) VALUES (?,1,'baseline','{"invented":"old value"}')`, baselineID).Error
		})
		reject(t, "23503", func(tx *gormlib.DB) error {
			return tx.Exec(`INSERT INTO issue_changes (issue_id,revision,action) VALUES (9223372036854775807,1,'created')`).Error
		})
	})

	t.Run("explicit_issue_deletion_retains_immutable_key_tombstone_and_history", func(t *testing.T) {
		id := seedIssue(t)
		var initial IssueChange
		require.NoError(t, db.Where("issue_id=? AND revision=1 AND action='created'", id).First(&initial).Error)
		receipt := seedReceipt(t, id, "client:synthetic-deletion", source, "retained-key")
		reject(t, "55000", func(tx *gormlib.DB) error {
			return tx.Exec(`UPDATE issue_create_receipts SET payload_hash=? WHERE request_key='retained-key'`, bytes.Repeat([]byte{0x52}, 32)).Error
		})
		reject(t, "55000", func(tx *gormlib.DB) error {
			return tx.Exec(`UPDATE issue_create_receipts SET issue_id=NULL WHERE request_key='retained-key'`).Error
		})
		require.NoError(t, db.Exec(`DELETE FROM issues WHERE id=?`, id).Error)
		var retained issueCreateReceipt
		require.NoError(t, db.Where("actor_identity_key=? AND canonical_source_project_id=? AND request_key=?", receipt.ActorIdentityKey, source, receipt.RequestKey).First(&retained).Error)
		assert.Nil(t, retained.IssueID, "only explicit issue deletion creates the permanent tombstone")
		assert.True(t, bytes.Equal(receipt.PayloadHash, retained.PayloadHash), "deletion must retain the original private binding without exporting it")
		assert.Equal(t, receipt.PayloadVersion, retained.PayloadVersion)
		assert.True(t, receipt.CreatedAt.Equal(retained.CreatedAt))
		var count int64
		require.NoError(t, db.Table("issue_changes").Where("issue_id=?", id).Count(&count).Error)
		assert.EqualValues(t, 1, count, "explicit card deletion does not cascade-delete committed history")
		alive := seedIssue(t)
		reject(t, "23505", func(tx *gormlib.DB) error {
			return tx.Create(&issueCreateReceipt{ActorIdentityKey: receipt.ActorIdentityKey, CanonicalSourceProjectID: source, RequestKey: receipt.RequestKey, IssueID: &alive, PayloadVersion: 1, PayloadHash: receipt.PayloadHash}).Error
		})
		reject(t, "55000", func(tx *gormlib.DB) error {
			return tx.Exec(`UPDATE issue_create_receipts SET issue_id=? WHERE request_key='retained-key'`, alive).Error
		})
		reject(t, "55000", func(tx *gormlib.DB) error {
			return tx.Exec(`DELETE FROM issue_create_receipts WHERE request_key='retained-key'`).Error
		})
		wire, err := json.Marshal(retained)
		require.NoError(t, err)
		assert.JSONEq(t, `{}`, string(wire), "receipt fields and original binding are never a public JSON projection")
	})

	t.Run("rollback_refuses_without_mutating_expanded_state", func(t *testing.T) {
		snapshot := func(table string) string {
			t.Helper()
			var value string
			require.NoError(t, db.Raw(`SELECT COALESCE(jsonb_agg(to_jsonb(t) ORDER BY to_jsonb(t)::text),'[]'::jsonb)::text FROM `+pq.QuoteIdentifier(table)+` t`).Row().Scan(&value))
			return value
		}
		before := make(map[string]string)
		for _, table := range []string{"issues", "issue_comments", "issue_changes", "issue_create_receipts", "migrations"} {
			before[table] = snapshot(table)
		}
		require.ErrorContains(t, issueInternalTasksMigration193().Rollback(db), "rollback refused")
		for table, value := range before {
			assert.True(t, value == snapshot(table), "rollback refusal must preserve %s without dumping private rows/bindings", table)
		}
	})
}
