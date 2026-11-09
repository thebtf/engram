package issues_test

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	stdlog "log"
	"net/url"
	"os"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/thebtf/engram/internal/auth"
	gormdb "github.com/thebtf/engram/internal/db/gorm"
	"github.com/thebtf/engram/internal/issuefixture"
	"github.com/thebtf/engram/internal/issues"
	"github.com/thebtf/engram/internal/issues/contract"
	"github.com/thebtf/engram/internal/redaction"
	"gorm.io/driver/postgres"
	gormlib "gorm.io/gorm"
	"gorm.io/gorm/logger"
)

const (
	source       = "51300000-0000-4000-8000-000000000061"
	otherSource  = "51300000-0000-4000-8000-000000000062"
	hiddenSource = "51300000-0000-4000-8000-000000000063"
)

func issue513FixtureURL(dsn string) (*url.URL, error) {
	parsed, err := url.Parse(dsn)
	if err != nil || parsed == nil || (parsed.Scheme != "postgres" && parsed.Scheme != "postgresql") ||
		parsed.Hostname() != "127.0.0.1" || parsed.Port() == "" || parsed.Path != "/issue513_native_synthetic" ||
		parsed.User == nil || parsed.User.Username() != "issue513_native" || parsed.RawQuery != "" || parsed.ForceQuery || parsed.Fragment != "" {
		return nil, errors.New("invalid disposable issue fixture target")
	}
	if os.Getenv("PGSERVICE") != "" || os.Getenv("PGOPTIONS") != "" {
		return nil, errors.New("fixture refuses inherited PostgreSQL service or options")
	}
	query := parsed.Query()
	query.Set("sslmode", "disable")
	query.Set("connect_timeout", "5")
	query.Set("application_name", "issue513_shared_owner")
	parsed.RawQuery = query.Encode()
	cfg, err := pgx.ParseConfig(parsed.String())
	if err != nil || cfg.Host != "127.0.0.1" || cfg.Port == 0 || cfg.Database != "issue513_native_synthetic" ||
		cfg.User != "issue513_native" || len(cfg.Fallbacks) != 0 || cfg.RuntimeParams["search_path"] != "" || cfg.RuntimeParams["options"] != "" {
		return nil, errors.New("invalid effective disposable issue fixture target")
	}
	return parsed, nil
}

func TestIssue513FixtureURLRejectsUnsafeTargets(t *testing.T) {
	t.Setenv("PGSERVICE", "")
	t.Setenv("PGOPTIONS", "")
	valid := "postgres://issue513_native@127.0.0.1:6410/issue513_native_synthetic"
	_, err := issue513FixtureURL(valid)
	require.NoError(t, err)
	for _, dsn := range []string{
		"postgres://issue513_native@192.0.2.1:6410/issue513_native_synthetic",
		"postgres://issue513_native@127.0.0.1:6410/other",
		"postgres://other@127.0.0.1:6410/issue513_native_synthetic",
		valid + "?host=192.0.2.1",
		valid + "?dbname=other",
		valid + "?search_path=public",
		valid + "?options=-csearch_path%3Dpublic",
		valid + "?service=other",
		valid + "?sslmode=prefer",
	} {
		_, err := issue513FixtureURL(dsn)
		require.Error(t, err)
	}
	t.Setenv("PGSERVICE", "unexpected")
	_, err = issue513FixtureURL(valid)
	require.Error(t, err)
	t.Setenv("PGSERVICE", "")
	t.Setenv("PGOPTIONS", "-c search_path=public")
	_, err = issue513FixtureURL(valid)
	require.Error(t, err)
}

func sharedDB(t *testing.T) *gormlib.DB {
	t.Helper()
	dsn := os.Getenv("ISSUE513_NATIVE_TEST_DSN")
	if dsn == "" {
		t.Skip("ISSUE513_NATIVE_TEST_DSN not set; requires an owned disposable local PostgreSQL fixture")
	}
	parsed, err := issue513FixtureURL(dsn)
	require.NoError(t, err)
	admin, err := gormlib.Open(postgres.Open(parsed.String()), &gormlib.Config{Logger: logger.Discard})
	require.NoError(t, err)
	pool, err := admin.DB()
	require.NoError(t, err)
	t.Cleanup(func() { assert.NoError(t, pool.Close()) })
	var database, user string
	require.NoError(t, admin.Raw("SELECT current_database(), current_user").Row().Scan(&database, &user))
	require.Equal(t, "issue513_native_synthetic", database)
	require.Equal(t, "issue513_native", user)
	var vectorReady, vectorscaleSafe bool
	require.NoError(t, admin.Raw(`SELECT EXISTS (SELECT 1 FROM pg_extension e JOIN pg_namespace n ON n.oid=e.extnamespace WHERE e.extname='vector' AND n.nspname='public'), NOT EXISTS (SELECT 1 FROM pg_available_extensions WHERE name='vectorscale' AND installed_version IS NULL)`).Row().Scan(&vectorReady, &vectorscaleSafe))
	require.True(t, vectorReady, "fixture requires pgvector already installed in public")
	require.True(t, vectorscaleSafe, "fixture must not install vectorscale")
	var publicRelations int64
	require.NoError(t, admin.Raw(`SELECT count(*) FROM pg_class c JOIN pg_namespace n ON n.oid=c.relnamespace WHERE n.nspname='public' AND c.relkind IN ('r','p','v','m','S','f')`).Scan(&publicRelations).Error)
	require.Zero(t, publicRelations, "fixture public schema must not contain application relations")
	schema := "issue513_shared_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	require.NoError(t, admin.Exec("CREATE SCHEMA "+schema).Error)
	t.Cleanup(func() { assert.NoError(t, admin.Exec("DROP SCHEMA "+schema+" CASCADE").Error) })
	query := parsed.Query()
	query.Set("search_path", schema+",public")
	query.Set("application_name", "issue513_shared_owner")
	parsed.RawQuery = query.Encode()
	store, err := gormdb.NewStore(gormdb.Config{DSN: parsed.String(), MaxConns: 8, LogLevel: logger.Silent})
	require.NoError(t, err)
	t.Cleanup(func() { assert.NoError(t, store.Close()) })
	db := store.GetDB()
	var actual string
	require.NoError(t, db.Raw("SELECT current_schema()").Scan(&actual).Error)
	require.Equal(t, schema, actual)
	require.NoError(t, admin.Raw(`SELECT count(*) FROM pg_class c JOIN pg_namespace n ON n.oid=c.relnamespace WHERE n.nspname='public' AND c.relkind IN ('r','p','v','m','S','f')`).Scan(&publicRelations).Error)
	require.Zero(t, publicRelations, "migrations must not create application relations in public")
	for _, project := range []string{source, otherSource, hiddenSource} {
		require.NoError(t, db.Create(&gormdb.Project{ID: project, IdentityStatus: sql.NullString{String: "active", Valid: true}}).Error)
	}
	return db
}

func intent(key, metadata string) json.RawMessage {
	return json.RawMessage(fmt.Sprintf(`{"title":"Synthetic shared task","body":"Synthetic outcome","target_project":%q,"type":" TASK ","labels":["b","a","a"],"request_key":%q,"source_agent":"synthetic-author","created_by_session":"synthetic-session","metadata":%s}`, source, key, metadata))
}

func errorCode(t *testing.T, err error, code string) {
	t.Helper()
	var typed *issues.Error
	require.True(t, errors.As(err, &typed), "owner must return a stable body-free application error")
	assert.Equal(t, code, typed.Code)
}

func domainCounts(t *testing.T, db *gormlib.DB) []int64 {
	t.Helper()
	var counts []int64
	for _, table := range []string{"issues", "issue_comments", "issue_changes", "issue_create_receipts"} {
		var count int64
		require.NoError(t, db.Table(table).Count(&count).Error)
		counts = append(counts, count)
	}
	return counts
}

func observeDomain(t *testing.T, db *gormlib.DB, run func()) int64 {
	t.Helper()
	var count atomic.Int64
	name := "issue513_observe_" + uuid.NewString()
	observe := func(tx *gormlib.DB) {
		if tx.Statement.Table == "issues" || strings.HasPrefix(tx.Statement.Table, "issue_") {
			count.Add(1)
		}
	}
	require.NoError(t, db.Callback().Query().After("gorm:query").Register(name, observe))
	require.NoError(t, db.Callback().Create().After("gorm:create").Register(name, observe))
	defer func() {
		require.NoError(t, db.Callback().Query().Remove(name))
		require.NoError(t, db.Callback().Create().Remove(name))
	}()
	run()
	return count.Load()
}

type switchingIdentityContext struct {
	context.Context
	reads atomic.Int64
}

func (ctx *switchingIdentityContext) Value(key any) any {
	if key == auth.IdentityKey {
		if ctx.reads.Add(1) == 1 {
			return auth.Admin()
		}
		return auth.Session("admin")
	}
	return ctx.Context.Value(key)
}

func TestSharedIssueOwnerPostgres(t *testing.T) {
	db := sharedDB(t)
	store := gormdb.NewIssueStore(db)
	application := issues.New(store, nil)
	ctx := auth.WithIdentity(t.Context(), auth.Client("read-write", "synthetic-shared-a"))
	other := auth.WithIdentity(t.Context(), auth.Client("read-write", "synthetic-shared-b"))
	admin := auth.WithIdentity(t.Context(), auth.Admin())
	scope := issues.Scope{SourceProject: source}
	create := func(t *testing.T, raw json.RawMessage) issues.Result {
		t.Helper()
		result, err := application.Create(ctx, scope, raw)
		require.NoError(t, err)
		require.Positive(t, result.ID)
		return result
	}
	read := func(t *testing.T, id int64) (*gormdb.Issue, []gormdb.IssueComment) {
		t.Helper()
		row, comments, err := store.GetIssue(t.Context(), id)
		require.NoError(t, err)
		return row, comments
	}

	t.Run("actor_projection_uses_existing_trusted_sources", func(t *testing.T) {
		for _, test := range []struct {
			name            string
			identity        auth.Identity
			kind, reference string
			writable        bool
		}{
			{"master", auth.Admin(), "operator", "operator:master", true},
			{"hmac", auth.Session("admin"), "operator", "operator:session", true},
			{"disabled", auth.AuthDisabled(), "operator", "operator:auth-disabled", true},
			{"browser", auth.SessionForBrowserUser("admin", 513), "operator", "browser:513", true},
			{"keycard", auth.Client("read-write", "synthetic-keycard"), "client", "client:synthetic-keycard", true},
			{"readonly", auth.Client("read-only", "synthetic-readonly"), "client", "client:synthetic-readonly", false},
		} {
			t.Run(test.name, func(t *testing.T) {
				actor, err := issues.CaptureActor(auth.WithIdentity(t.Context(), test.identity))
				require.NoError(t, err)
				descriptor := actor.Descriptor()
				assert.Equal(t, test.kind, descriptor.Kind)
				require.NotNil(t, descriptor.Reference)
				assert.Equal(t, test.reference, *descriptor.Reference)
				assert.Equal(t, test.writable, actor.CanWrite())
			})
		}
	})

	t.Run("equal_original_retry_after_edit_and_private_history", func(t *testing.T) {
		before := domainCounts(t, db)
		original := intent("original-after-edit", `{"n":1,"z":-0,"unknown":{"b":true,"a":[2,1]}}`)
		first := create(t, original)
		assert.False(t, first.Replayed)
		assert.Equal(t, "1", first.Revision)
		equivalent := bytes.ReplaceAll(original, []byte(`{"n":1,"z":-0,"unknown":{"b":true,"a":[2,1]}}`), []byte(`{"unknown":{"a":[2.0,1.0],"b":true},"z":0.0,"n":1.0}`))
		retry := create(t, equivalent)
		assert.True(t, retry.Replayed)
		assert.Equal(t, first.ID, retry.ID)
		after := domainCounts(t, db)
		assert.Equal(t, []int64{before[0] + 1, before[1], before[2] + 1, before[3] + 1}, after)
		updated, err := application.Mutate(ctx, scope, first.ID, json.RawMessage(`{"expected_revision":"1","body":"","metadata":{"outcome":"done"},"owner_label":"docs","recipient_label":"next","comment":"Synthetic committed outcome","comment_metadata":{"extra":[true,null,0.1]}}`))
		require.NoError(t, err)
		assert.True(t, updated.Changed)
		assert.Equal(t, "2", updated.Revision)
		require.NotNil(t, updated.CommentID)
		row, comments := read(t, first.ID)
		assert.Empty(t, row.Body, "explicit empty body is not omission")
		assert.JSONEq(t, `{"outcome":"done"}`, string(row.Metadata))
		assert.Equal(t, []string{"a", "b"}, []string(row.Labels))
		require.Len(t, comments, 1)
		assert.JSONEq(t, `{"extra":[true,null,0.1]}`, string(comments[0].Metadata))
		assert.Equal(t, "client", comments[0].Actor.Kind)
		assert.Equal(t, "client:synthetic-shared-a", *comments[0].Actor.Reference)
		replayed := create(t, original)
		assert.Equal(t, first.ID, replayed.ID)
		assert.Equal(t, "2", replayed.Revision)
		assert.True(t, replayed.Replayed)
		_, err = application.Create(ctx, scope, bytes.Replace(original, []byte(`Synthetic outcome`), []byte(`Different synthetic outcome`), 1))
		errorCode(t, err, "IDEMPOTENCY_CONFLICT")
		wire, marshalErr := json.Marshal(err)
		require.NoError(t, marshalErr)
		for _, forbidden := range []string{`"id"`, "payload", "hash", "Synthetic", "client:"} {
			assert.NotContains(t, string(wire), forbidden)
		}
		var history []gormdb.IssueChange
		require.NoError(t, db.Where("issue_id = ?", first.ID).Order("revision").Find(&history).Error)
		require.Len(t, history, 2)
		assert.Equal(t, "created", history[0].Action)
		assert.EqualValues(t, 1, *history[1].PreviousRevision)
		assert.ElementsMatch(t, []string{"body", "metadata", "owner_label", "recipient_label"}, []string(history[1].ChangedFields))
		for _, change := range history {
			assert.JSONEq(t, `{}`, string(change.Changes), "history retains field names, never private snapshots")
		}
		assert.Equal(t, "synthetic-author", *history[0].Attribution.SourceAgent)
		assert.Equal(t, "synthetic-session", *history[0].Attribution.Session)
	})

	t.Run("versioned_receipts_replay_and_conflict_without_disclosure", func(t *testing.T) {
		for _, tc := range []struct {
			name, target string
			version      int
		}{
			{"v1", "", 1}, {"v2", "session-one", 2},
		} {
			t.Run(tc.name, func(t *testing.T) {
				raw := intent("binding-"+tc.name, `{"n":1}`)
				if tc.target != "" {
					raw = bytes.Replace(raw, []byte(`"title":`), []byte(`"target_session":"`+tc.target+`","title":`), 1)
				}
				first := create(t, raw)
				var version int
				require.NoError(t, db.Raw(`SELECT payload_version FROM issue_create_receipts WHERE request_key = ?`, "binding-"+tc.name).Scan(&version).Error)
				require.Equal(t, tc.version, version)
				before := domainCounts(t, db)
				retry := create(t, raw)
				assert.True(t, retry.Replayed)
				assert.Equal(t, first.ID, retry.ID)
				changed := bytes.Replace(raw, []byte(`"title":`), []byte(`"target_session":"session-two","title":`), 1)
				if tc.target != "" {
					changed = bytes.Replace(raw, []byte(`"target_session":"session-one"`), []byte(`"target_session":"session-two"`), 1)
				}
				result, err := application.Create(ctx, scope, changed)
				errorCode(t, err, "IDEMPOTENCY_CONFLICT")
				assert.Equal(t, issues.Result{}, result)
				wire, marshalErr := json.Marshal(err)
				require.NoError(t, marshalErr)
				for _, secret := range []string{"session-one", "session-two", "payload", "hash", "Synthetic"} {
					assert.NotContains(t, string(wire), secret)
				}
				assert.Equal(t, before, domainCounts(t, db))
				require.NoError(t, store.DeleteIssue(t.Context(), first.ID, nil))
				afterDelete := domainCounts(t, db)
				_, err = application.Create(ctx, scope, changed)
				errorCode(t, err, "ISSUE_GONE")
				_, err = application.Create(ctx, issues.Scope{SourceProject: source, ReadProject: hiddenSource}, changed)
				errorCode(t, err, "ISSUE_NOT_FOUND")
				assert.Equal(t, afterDelete, domainCounts(t, db))
			})
		}
	})

	t.Run("actor_and_source_are_independent_namespaces", func(t *testing.T) {
		raw := intent("independent-key", `{}`)
		a := create(t, raw)
		b, err := application.Create(other, scope, raw)
		require.NoError(t, err)
		c, err := application.Create(ctx, issues.Scope{SourceProject: otherSource}, raw)
		require.NoError(t, err)
		assert.NotEqual(t, a.ID, b.ID)
		assert.NotEqual(t, a.ID, c.ID)
		for _, call := range []struct {
			ctx   context.Context
			scope issues.Scope
			want  int64
		}{{ctx, scope, a.ID}, {other, scope, b.ID}, {ctx, issues.Scope{SourceProject: otherSource}, c.ID}} {
			retry, err := application.Create(call.ctx, call.scope, raw)
			require.NoError(t, err)
			assert.True(t, retry.Replayed)
			assert.Equal(t, call.want, retry.ID)
		}
	})

	t.Run("admission_and_actor_precondition_precede_receipt_access", func(t *testing.T) {
		raw := intent("guard-key", `{}`)
		guarded := bytes.Replace(raw, []byte(`"title":`), []byte(`"expected_actor":{"kind":"operator","reference":"operator:master"},"title":`), 1)
		var guardErr error
		before := domainCounts(t, db)
		accesses := observeDomain(t, db, func() { _, guardErr = application.Create(ctx, scope, guarded) })
		errorCode(t, guardErr, "ACTOR_CHANGED")
		assert.Zero(t, accesses)
		assert.Equal(t, before, domainCounts(t, db))
		for _, expectation := range []string{`{"kind":"unknown","reference":null}`, `{"kind":"client","reference":null}`, `{"kind":"client","reference":""}`} {
			input := bytes.Replace(raw, []byte(`"title":`), []byte(`"expected_actor":`+expectation+`,"title":`), 1)
			assert.Zero(t, observeDomain(t, db, func() { _, guardErr = application.Create(ctx, scope, input) }))
			errorCode(t, guardErr, "ACTOR_CHANGED")
		}
		_, err := application.Create(t.Context(), scope, guarded)
		errorCode(t, err, "UNAUTHENTICATED")
		_, err = application.Create(auth.WithIdentity(t.Context(), auth.Client("read-only", "synthetic-readonly")), scope, guarded)
		errorCode(t, err, "FORBIDDEN")
		require.NoError(t, db.Model(&gormdb.Project{}).Where("id = ?", otherSource).Update("removed_at", time.Now()).Error)
		assert.Zero(t, observeDomain(t, db, func() { _, guardErr = application.Create(ctx, issues.Scope{SourceProject: otherSource}, guarded) }))
		errorCode(t, guardErr, "ISSUE_NOT_FOUND")
		require.NoError(t, db.Model(&gormdb.Project{}).Where("id = ?", otherSource).Update("removed_at", nil).Error)
		_, err = application.Create(ctx, issues.Scope{SourceProject: source, ReadProject: hiddenSource}, guarded)
		errorCode(t, err, "ISSUE_NOT_FOUND")
		actual := bytes.Replace(guarded, []byte(`"kind":"operator","reference":"operator:master"`), []byte(`"kind":"client","reference":"client:synthetic-shared-a"`), 1)
		first := create(t, actual)
		retry := create(t, raw)
		assert.True(t, retry.Replayed, "guard is excluded from the original payload")
		assert.Equal(t, first.ID, retry.ID)
	})

	t.Run("actor_is_sampled_once_for_guard_and_namespace", func(t *testing.T) {
		changing := &switchingIdentityContext{Context: t.Context()}
		raw := bytes.Replace(intent("immutable-actor", `{}`), []byte(`"title":`), []byte(`"expected_actor":{"kind":"operator","reference":"operator:master"},"title":`), 1)
		first, err := application.Create(changing, scope, raw)
		require.NoError(t, err)
		assert.EqualValues(t, 1, changing.reads.Load())
		retry, err := application.Create(admin, scope, raw)
		require.NoError(t, err)
		assert.True(t, retry.Replayed)
		assert.Equal(t, first.ID, retry.ID)
	})

	t.Run("replay_rechecks_current_visibility_before_comparison", func(t *testing.T) {
		raw := intent("visibility-key", `{}`)
		first := create(t, raw)
		before := domainCounts(t, db)
		_, err := application.Create(ctx, issues.Scope{SourceProject: source, ReadProject: hiddenSource}, raw)
		errorCode(t, err, "ISSUE_NOT_FOUND")
		// Simulate existing authorized canonical role provenance changing after
		// creation. The receipt alone must not grant visibility of that row.
		require.NoError(t, db.Model(&gormdb.Issue{}).Where("id = ?", first.ID).Updates(map[string]any{"source_project": hiddenSource, "target_project": hiddenSource}).Error)
		_, err = application.Create(ctx, issues.Scope{SourceProject: source, ReadProject: source}, raw)
		errorCode(t, err, "ISSUE_NOT_FOUND")
		assert.Equal(t, before, domainCounts(t, db))
	})

	t.Run("concurrent_equal_creates_commit_one_issue_history_receipt", func(t *testing.T) {
		before := domainCounts(t, db)
		const callers = 10
		results := make([]issues.Result, callers)
		errors := make([]error, callers)
		start := make(chan struct{})
		var group sync.WaitGroup
		for index := range callers {
			group.Add(1)
			go func(index int) {
				defer group.Done()
				<-start
				results[index], errors[index] = application.Create(ctx, scope, intent("concurrent-equal", `{"n":0.1}`))
			}(index)
		}
		close(start)
		group.Wait()
		created := 0
		for index := range callers {
			require.NoError(t, errors[index])
			assert.Equal(t, results[0].ID, results[index].ID)
			if !results[index].Replayed {
				created++
			}
		}
		assert.Equal(t, 1, created)
		assert.Equal(t, []int64{before[0] + 1, before[1], before[2] + 1, before[3] + 1}, domainCounts(t, db))
	})

	t.Run("concurrent_different_payloads_have_one_committed_winner", func(t *testing.T) {
		before := domainCounts(t, db)
		var results [2]issues.Result
		var failures [2]error
		start := make(chan struct{})
		var group sync.WaitGroup
		for index := range 2 {
			group.Add(1)
			go func(index int) {
				defer group.Done()
				<-start
				results[index], failures[index] = application.Create(ctx, scope, intent("concurrent-different", fmt.Sprintf(`{"writer":%d}`, index)))
			}(index)
		}
		close(start)
		group.Wait()
		winner := 0
		if failures[0] != nil {
			winner = 1
		}
		require.NoError(t, failures[winner])
		errorCode(t, failures[1-winner], "IDEMPOTENCY_CONFLICT")
		assert.Equal(t, issues.Result{}, results[1-winner])
		assert.Equal(t, []int64{before[0] + 1, before[1], before[2] + 1, before[3] + 1}, domainCounts(t, db))
	})

	t.Run("concurrent_cas_has_one_winner_and_no_loser_comment", func(t *testing.T) {
		first := create(t, intent("concurrent-cas", `{}`))
		before := domainCounts(t, db)
		var results [2]issues.Result
		var failures [2]error
		start := make(chan struct{})
		var group sync.WaitGroup
		for index := range 2 {
			group.Add(1)
			go func(index int) {
				defer group.Done()
				<-start
				raw := json.RawMessage(fmt.Sprintf(`{"expected_revision":"1","body":"Synthetic writer %d","comment":"Synthetic compound %d","comment_metadata":{"writer":%d}}`, index, index, index))
				results[index], failures[index] = application.Mutate(ctx, scope, first.ID, raw)
			}(index)
		}
		close(start)
		group.Wait()
		winner := 0
		if failures[0] != nil {
			winner = 1
		}
		require.NoError(t, failures[winner])
		errorCode(t, failures[1-winner], "REVISION_CONFLICT")
		assert.Equal(t, "2", results[winner].Revision)
		assert.Equal(t, issues.Result{}, results[1-winner])
		row, comments := read(t, first.ID)
		assert.Equal(t, fmt.Sprintf("Synthetic writer %d", winner), row.Body)
		require.Len(t, comments, 1)
		assert.Equal(t, fmt.Sprintf("Synthetic compound %d", winner), comments[0].Body)
		assert.Equal(t, []int64{before[0], before[1] + 1, before[2] + 1, before[3]}, domainCounts(t, db))
		_, err := application.Mutate(ctx, scope, first.ID, json.RawMessage(fmt.Sprintf(`{"expected_revision":"1","body":"Synthetic writer %d"}`, winner)))
		errorCode(t, err, "REVISION_CONFLICT")
	})

	t.Run("concurrent_progress_appends_preserve_statement_and_resume", func(t *testing.T) {
		first := create(t, intent("concurrent-progress", `{}`))
		before := domainCounts(t, db)
		writers := []context.Context{
			contract.WithTransportSession(ctx, "progress-session-a"),
			contract.WithTransportSession(other, "progress-session-b"),
		}
		var results [2]issues.Result
		var failures [2]error
		start := make(chan struct{})
		var group sync.WaitGroup
		for index := range writers {
			group.Add(1)
			go func(index int) {
				defer group.Done()
				<-start
				raw := json.RawMessage(fmt.Sprintf(`{"comment":"Synthetic progress %d","comment_metadata":{"writer":%d}}`, index, index))
				results[index], failures[index] = application.Mutate(writers[index], scope, first.ID, raw)
			}(index)
		}
		close(start)
		group.Wait()
		for index := range writers {
			require.NoError(t, failures[index])
			assert.Equal(t, first.ID, results[index].ID)
			assert.Equal(t, "1", results[index].Revision)
			assert.False(t, results[index].Changed)
			require.NotNil(t, results[index].CommentID)
		}
		assert.NotEqual(t, *results[0].CommentID, *results[1].CommentID)
		assert.Equal(t, []int64{before[0], before[1] + 2, before[2], before[3]}, domainCounts(t, db))
		page, err := store.ReadTaskIssue(t.Context(), first.ID, gormdb.IssueTaskDetailParams{ReadProject: source, Paged: true, CommentsLimit: 20, HistoryLimit: 20})
		require.NoError(t, err)
		assert.Equal(t, "Synthetic shared task", page.Issue.Title)
		assert.Equal(t, "Synthetic outcome", page.Issue.Body)
		assert.EqualValues(t, 1, page.Issue.Revision)
		require.Len(t, page.Comments, 2)
		require.Len(t, page.History, 1)
		byID := make(map[int64]gormdb.IssueComment, len(page.Comments))
		for _, comment := range page.Comments {
			byID[comment.ID] = comment
		}
		for index, result := range results {
			comment, ok := byID[*result.CommentID]
			require.True(t, ok)
			assert.Equal(t, fmt.Sprintf("Synthetic progress %d", index), comment.Body)
			assert.JSONEq(t, fmt.Sprintf(`{"writer":%d}`, index), string(comment.Metadata))
			assert.Equal(t, "client", comment.Actor.Kind)
			require.NotNil(t, comment.Actor.Reference)
			assert.Equal(t, []string{"client:synthetic-shared-a", "client:synthetic-shared-b"}[index], *comment.Actor.Reference)
			assert.Equal(t, []string{"progress-session-a", "progress-session-b"}[index], comment.AuthorSession)
			assert.Equal(t, "transport", comment.AuthorSessionSource)
		}
		_, err = store.ReadTaskIssue(t.Context(), first.ID, gormdb.IssueTaskDetailParams{ReadProject: hiddenSource, Paged: true, CommentsLimit: 20, HistoryLimit: 20})
		errorCode(t, err, "ISSUE_NOT_FOUND")
	})

	t.Run("lifecycle_without_revision_keeps_explicit_conflicts_and_authority", func(t *testing.T) {
		first := create(t, intent("lifecycle-compatibility", `{}`))
		readonly := auth.WithIdentity(t.Context(), auth.Client("read-only", "synthetic-readonly"))
		_, err := application.Mutate(readonly, scope, first.ID, json.RawMessage(`{"status":"resolved"}`))
		errorCode(t, err, "FORBIDDEN")
		_, err = application.Mutate(t.Context(), scope, first.ID, json.RawMessage(`{"status":"resolved"}`))
		errorCode(t, err, "UNAUTHENTICATED")
		_, err = application.Mutate(other, scope, first.ID, json.RawMessage(`{"status":"closed"}`))
		errorCode(t, err, "INVALID_TRANSITION")
		_, err = application.Mutate(other, scope, first.ID, json.RawMessage(`{"status":"resolved","body":"forbidden"}`))
		errorCode(t, err, "FORBIDDEN")
		_, err = application.Mutate(ctx, scope, first.ID, json.RawMessage(`{"status":"resolved","body":"edit needs revision"}`))
		errorCode(t, err, "REVISION_REQUIRED")
		resolved, err := application.Mutate(other, scope, first.ID, json.RawMessage(`{"status":"resolved","comment":"Synthetic outcome"}`))
		require.NoError(t, err)
		assert.Equal(t, "2", resolved.Revision)
		require.NotNil(t, resolved.CommentID)
		_, err = application.Mutate(other, scope, first.ID, json.RawMessage(`{"status":"closed","expected_revision":"1","comment":"stale"}`))
		errorCode(t, err, "REVISION_CONFLICT")
		closed, err := application.Mutate(other, scope, first.ID, json.RawMessage(`{"status":"closed"}`))
		require.NoError(t, err)
		assert.Equal(t, "3", closed.Revision)
		reopened, err := application.Mutate(other, scope, first.ID, json.RawMessage(`{"status":"reopened"}`))
		require.NoError(t, err)
		assert.Equal(t, "4", reopened.Revision)
		row, comments := read(t, first.ID)
		assert.Equal(t, "reopened", row.Status)
		assert.Equal(t, "Synthetic outcome", row.Body)
		require.Len(t, comments, 1)
		assert.Equal(t, "Synthetic outcome", comments[0].Body)
		var history []gormdb.IssueChange
		require.NoError(t, db.Where("issue_id = ?", first.ID).Order("revision").Find(&history).Error)
		require.Len(t, history, 4)
	})

	t.Run("noop_comments_nullable_labels_and_revision_requirements", func(t *testing.T) {
		first := create(t, intent("noop", `{"n":1}`))
		before, comments := read(t, first.ID)
		counts := domainCounts(t, db)
		result, err := application.Mutate(ctx, scope, first.ID, json.RawMessage(`{"expected_revision":"1","title":"Synthetic shared task","labels":["b","a","b"],"metadata":{"n":1.0}}`))
		require.NoError(t, err)
		assert.False(t, result.Changed)
		assert.Equal(t, "1", result.Revision)
		after, afterComments := read(t, first.ID)
		assert.Equal(t, before, after)
		assert.Equal(t, comments, afterComments)
		assert.Equal(t, counts, domainCounts(t, db))
		_, err = application.Mutate(ctx, scope, first.ID, json.RawMessage(`{"body":"new"}`))
		errorCode(t, err, "REVISION_REQUIRED")
		commented, err := application.Mutate(other, scope, first.ID, json.RawMessage(`{"comment":"Synthetic collaborator comment","comment_metadata":{"extension":[true,null]}}`))
		require.NoError(t, err)
		assert.False(t, commented.Changed)
		assert.Equal(t, "1", commented.Revision)
		require.NotNil(t, commented.CommentID)
		_, err = application.Mutate(ctx, scope, first.ID, json.RawMessage(`{"expected_revision":"1","owner_label":"docs","recipient_label":"next"}`))
		require.NoError(t, err)
		_, err = application.Mutate(ctx, scope, first.ID, json.RawMessage(`{"expected_revision":"2","owner_label":null,"recipient_label":null,"labels":[]}`))
		require.NoError(t, err)
		cleared, _ := read(t, first.ID)
		assert.Nil(t, cleared.OwnerLabel)
		assert.Nil(t, cleared.RecipientLabel)
		assert.Empty(t, cleared.Labels)
	})

	t.Run("existing_authority_lifecycle_and_no_revision_disclosure", func(t *testing.T) {
		first := create(t, intent("authority", `{}`))
		_, err := application.Mutate(other, scope, first.ID, json.RawMessage(`{"expected_revision":"999","title":"Forbidden synthetic edit","comment":"Forbidden companion"}`))
		errorCode(t, err, "FORBIDDEN")
		assert.NotContains(t, err.Error(), "current_revision")
		_, err = application.Mutate(other, scope, first.ID, json.RawMessage(`{"expected_revision":"1","status":"reopened"}`))
		errorCode(t, err, "INVALID_TRANSITION")
		_, err = application.Mutate(ctx, scope, first.ID, json.RawMessage(`{"expected_revision":"1","status":"closed"}`))
		errorCode(t, err, "INVALID_TRANSITION")
		resolved, err := application.Mutate(other, scope, first.ID, json.RawMessage(`{"expected_revision":"1","status":"resolved","comment":"Synthetic resolution"}`))
		require.NoError(t, err)
		assert.Equal(t, "2", resolved.Revision)
		reopened, err := application.Mutate(ctx, scope, first.ID, json.RawMessage(`{"expected_revision":"2","status":"reopened"}`))
		require.NoError(t, err)
		assert.Equal(t, "3", reopened.Revision)
		_, err = application.Mutate(other, scope, first.ID, json.RawMessage(`{"expected_revision":"3","status":"rejected","comment":"Synthetic refusal"}`))
		errorCode(t, err, "FORBIDDEN")
		rejected, err := application.Mutate(admin, scope, first.ID, json.RawMessage(`{"expected_revision":"3","status":"rejected","comment":"Synthetic operator reason","comment_metadata":{"reason":"retained"}}`))
		require.NoError(t, err)
		assert.Equal(t, "4", rejected.Revision)
		row, comments := read(t, first.ID)
		assert.Equal(t, "rejected", row.Status)
		require.Len(t, comments, 2)
		assert.Equal(t, "Rejected: Synthetic operator reason", comments[1].Body)
		assert.JSONEq(t, `{"reason":"retained"}`, string(comments[1].Metadata))
	})

	t.Run("persistence_failure_rolls_back_material_comment_and_history", func(t *testing.T) {
		first := create(t, intent("rollback", `{}`))
		before, comments := read(t, first.ID)
		counts := domainCounts(t, db)
		name := "issue513_fail_comment_" + uuid.NewString()
		require.NoError(t, db.Callback().Create().Before("gorm:create").Register(name, func(tx *gormlib.DB) {
			if tx.Statement.Table == "issue_comments" {
				tx.AddError(errors.New("synthetic persistence failure"))
			}
		}))
		_, err := application.Mutate(ctx, scope, first.ID, json.RawMessage(`{"expected_revision":"1","body":"Synthetic failed edit","comment":"Synthetic failed outcome"}`))
		require.NoError(t, db.Callback().Create().Remove(name))
		errorCode(t, err, "UNAVAILABLE")
		after, afterComments := read(t, first.ID)
		assert.Equal(t, before, after)
		assert.Equal(t, comments, afterComments)
		assert.Equal(t, counts, domainCounts(t, db))
	})

	t.Run("deleted_key_is_tombstone_not_resurrection", func(t *testing.T) {
		raw := intent("deleted-key", `{}`)
		first := create(t, raw)
		require.NoError(t, store.DeleteIssue(t.Context(), first.ID, nil))
		before := domainCounts(t, db)
		_, err := application.Create(ctx, scope, raw)
		errorCode(t, err, "ISSUE_GONE")
		assert.Equal(t, before, domainCounts(t, db))
		_, err = application.Create(ctx, issues.Scope{SourceProject: source, ReadProject: hiddenSource}, raw)
		errorCode(t, err, "ISSUE_NOT_FOUND")
		_, err = application.Create(auth.WithIdentity(t.Context(), auth.Client("read-only", "synthetic-readonly")), scope, raw)
		errorCode(t, err, "FORBIDDEN")
	})

	t.Run("single_shot_unkeyed_create_keeps_legacy_semantics", func(t *testing.T) {
		raw := json.RawMessage(`{"title":"Synthetic unkeyed task"}`)
		a := create(t, raw)
		b := create(t, raw)
		assert.NotEqual(t, a.ID, b.ID)
		assert.False(t, a.Replayed)
		assert.False(t, b.Replayed)
	})

	// Root-requested pre-repair oracle: exact-name validation must not be
	// undone by encoding/json's case-insensitive struct field matching.
	t.Run("wire_alias_and_target_presence", func(t *testing.T) {
		assertRefused := func(t *testing.T, before []int64, result issues.Result, err error) {
			t.Helper()
			var typed *issues.Error
			if assert.True(t, errors.As(err, &typed), "invalid wire input must return a stable error, never success") {
				assert.Equal(t, "INVALID_ARGUMENT", typed.Code)
			}
			assert.True(t, result == (issues.Result{}), "invalid input must not return an issue ID, revision or replay result")
			assert.Equal(t, before, domainCounts(t, db), "invalid wire input must not mutate issue/comment/history/receipt counts")
		}

		t.Run("case_alias_cannot_clear_original_retry_key", func(t *testing.T) {
			original := intent("case-binding", `{}`)
			first := create(t, original)
			before := domainCounts(t, db)
			malformed := bytes.Replace(original, []byte(`"request_key":"case-binding"`), []byte(`"request_key":"case-binding","REQUEST_KEY":null`), 1)
			require.True(t, json.Valid(malformed), "this oracle must reach valid raw JSON admission")
			result, err := application.Create(ctx, scope, malformed)
			assertRefused(t, before, result, err)
			retry := create(t, original)
			assert.True(t, retry.Replayed)
			assert.Equal(t, first.ID, retry.ID, "the original key must stay bound to its original result")
		})

		t.Run("case_alias_cannot_replace_validated_create_labels", func(t *testing.T) {
			before := domainCounts(t, db)
			raw := json.RawMessage(`{"title":"Synthetic case-label create","request_key":"case-label-create","labels":["retained"],"Labels":[null]}`)
			result, err := application.Create(ctx, scope, raw)
			assertRefused(t, before, result, err)
		})

		t.Run("case_alias_cannot_replace_validated_patch_labels", func(t *testing.T) {
			first := create(t, intent("case-label-patch", `{}`))
			old, comments := read(t, first.ID)
			before := domainCounts(t, db)
			result, err := application.Mutate(ctx, scope, first.ID, json.RawMessage(`{"expected_revision":"1","labels":["retained"],"Labels":[null],"comment":"Synthetic rejected companion"}`))
			assertRefused(t, before, result, err)
			after, afterComments := read(t, first.ID)
			assert.True(t, reflect.DeepEqual(old, after), "case aliases must not change validated material fields")
			assert.True(t, reflect.DeepEqual(comments, afterComments), "refused aliases must not append a companion comment")
		})

		t.Run("case_alias_cannot_clear_expected_actor", func(t *testing.T) {
			original := intent("case-actor", `{}`)
			create(t, original)
			before := domainCounts(t, db)
			raw := bytes.Replace(original, []byte(`"title":`), []byte(`"expected_actor":{"kind":"client","reference":"client:synthetic-shared-a"},"EXPECTED_ACTOR":null,"title":`), 1)
			result, err := application.Create(other, scope, raw)
			assertRefused(t, before, result, err)
		})

		t.Run("nested_actor_aliases_are_refused", func(t *testing.T) {
			for _, descriptor := range []string{
				`{"kind":"client","KIND":"operator","reference":"client:synthetic-shared-a"}`,
				`{"kind":"client","reference":"client:synthetic-shared-a","Reference":null}`,
				`{"kind":"client","Kind":"operator","reference":"client:synthetic-shared-a"}`,
			} {
				before := domainCounts(t, db)
				raw := bytes.Replace(intent("nested-case-actor", `{}`), []byte(`"title":`), []byte(`"expected_actor":`+descriptor+`,"title":`), 1)
				result, err := application.Create(ctx, scope, raw)
				assertRefused(t, before, result, err)
			}
		})

		t.Run("unknown_metadata_member_case_is_preserved", func(t *testing.T) {
			const metadata = `{"REQUEST_KEY":null,"Labels":[null],"expected_actor":{"Kind":"extension","REFERENCE":null}}`
			first := create(t, intent("unknown-member-case", metadata))
			row, _ := read(t, first.ID)
			assert.JSONEq(t, metadata, string(row.Metadata))
			result, err := application.Mutate(ctx, scope, first.ID, json.RawMessage(`{"comment":"Synthetic metadata case control","comment_metadata":`+metadata+`}`))
			require.NoError(t, err)
			assert.False(t, result.Changed)
			_, comments := read(t, first.ID)
			require.Len(t, comments, 1)
			assert.JSONEq(t, metadata, string(comments[0].Metadata))
		})

		t.Run("explicit_empty_target_is_not_omission", func(t *testing.T) {
			before := domainCounts(t, db)
			raw := json.RawMessage(`{"title":"Synthetic empty target","request_key":"explicit-empty-target","target_project":""}`)
			result, err := application.Create(ctx, scope, raw)
			assertRefused(t, before, result, err)
		})

		t.Run("omitted_target_still_defaults_to_admitted_source", func(t *testing.T) {
			before := domainCounts(t, db)
			first := create(t, json.RawMessage(`{"title":"Synthetic omitted target","request_key":"omitted-target"}`))
			row, _ := read(t, first.ID)
			assert.Equal(t, source, row.TargetProject)
			assert.Equal(t, []int64{before[0] + 1, before[1], before[2] + 1, before[3] + 1}, domainCounts(t, db))
		})
	})

	t.Run("native_raw_mapping_seam", func(t *testing.T) {
		t.Run("reject_before_duplicate_or_numeric_evidence_is_lost", func(t *testing.T) {
			for _, raw := range []string{`{"action":"comment","body":"one","body":"two"}`, `{"action":"comment","body":"Synthetic comment","metadata":{"n":0.10000000000000001}}`} {
				_, err := issues.DecodeReceivedObject(json.RawMessage(raw))
				errorCode(t, err, "INVALID_ARGUMENT")
			}
		})
		t.Run("rename_comment_fields_without_changing_issue_body_or_numbers", func(t *testing.T) {
			first := create(t, intent("native-mapping-seam", `{}`))
			members, err := issues.DecodeReceivedObject(json.RawMessage(`{"action":"comment","body":"Synthetic native comment","metadata":{"n":1.0,"unknown":[true,null]}}`))
			require.NoError(t, err)
			assert.Equal(t, `{"n":1.0,"unknown":[true,null]}`, string(members["metadata"]))
			members["comment"], members["comment_metadata"] = members["body"], members["metadata"]
			delete(members, "body")
			delete(members, "metadata")
			delete(members, "action")
			normalized, err := json.Marshal(members)
			require.NoError(t, err)
			result, err := application.Mutate(ctx, scope, first.ID, normalized)
			require.NoError(t, err)
			assert.False(t, result.Changed)
			assert.Equal(t, "1", result.Revision)
			row, comments := read(t, first.ID)
			assert.Equal(t, "Synthetic outcome", row.Body)
			assert.JSONEq(t, `{}`, string(row.Metadata))
			require.Len(t, comments, 1)
			assert.True(t, contract.EqualMetadata(comments[0].Metadata, json.RawMessage(`{"n":1.0,"unknown":[true,null]}`)))
		})
	})

	t.Run("raw_numeric_duplicate_object_key_and_metadata_boundaries", func(t *testing.T) {
		for _, test := range []struct{ name, metadata, code string }{
			{"null", `null`, "INVALID_ARGUMENT"},
			{"array", `[]`, "INVALID_ARGUMENT"},
			{"unsafe_integer", `{"n":9007199254740993}`, "INVALID_ARGUMENT"},
			{"rounded_fraction", `{"n":0.10000000000000001}`, "INVALID_ARGUMENT"},
			{"underflow", `{"n":1e-400}`, "INVALID_ARGUMENT"},
			{"overflow", `{"n":1e400}`, "INVALID_ARGUMENT"},
			{"duplicate", `{"x":1,"x":2}`, "INVALID_ARGUMENT"},
			{"escaped_duplicate", `{"outer":{"name":1,"\u006eame":2}}`, "INVALID_ARGUMENT"},
			{"unpaired_surrogate", `{"text":"\ud800"}`, "INVALID_ARGUMENT"},
			{"depth_257", `{"v":` + strings.Repeat("[", 256) + `0` + strings.Repeat("]", 256) + `}`, "PAYLOAD_TOO_LARGE"},
			{"over_1MiB", `{"v":"` + strings.Repeat("a", (1<<20)-len(`{"v":""}`)+1) + `"}`, "PAYLOAD_TOO_LARGE"},
		} {
			t.Run(test.name, func(t *testing.T) {
				before := domainCounts(t, db)
				_, err := application.Create(ctx, scope, intent("invalid-"+test.name, test.metadata))
				errorCode(t, err, test.code)
				assert.Equal(t, before, domainCounts(t, db))
			})
		}
		for _, test := range []struct{ name, raw string }{
			{"top_duplicate", `{"title":"Synthetic duplicate","title":"Synthetic duplicate"}`},
			{"nul_key", `{"title":"Synthetic key","request_key":"bad\u0000key"}`},
			{"empty_key", `{"title":"Synthetic key","request_key":""}`},
			{"invalid_utf8", "{\"title\":\"Synthetic key\",\"request_key\":\"" + string([]byte{0xff}) + "\"}"},
			{"unsafe_number_outside_metadata", `{"title":"Synthetic numeric request","unknown":9007199254740993}`},
			{"trailing_json", `{"title":"Synthetic trailing"} {}`},
			{"labels_null", `{"title":"Synthetic null","labels":null}`},
		} {
			t.Run(test.name, func(t *testing.T) {
				before := domainCounts(t, db)
				_, err := application.Create(ctx, scope, json.RawMessage(test.raw))
				errorCode(t, err, "INVALID_ARGUMENT")
				assert.Equal(t, before, domainCounts(t, db))
			})
		}
		for _, test := range []struct {
			name, key string
			accepted  bool
		}{
			{"ascii512", strings.Repeat("k", 512), true},
			{"utf8_512", strings.Repeat("я", 256), true},
			{"opaque", " синтетика / opaque : key ", true},
			{"ascii513", strings.Repeat("k", 513), false},
			{"utf8_513", strings.Repeat("я", 256) + "a", false},
		} {
			t.Run(test.name, func(t *testing.T) {
				before := domainCounts(t, db)
				_, err := application.Create(ctx, scope, intent(test.key, `{}`))
				if test.accepted {
					require.NoError(t, err)
				} else {
					errorCode(t, err, "PAYLOAD_TOO_LARGE")
					assert.Equal(t, before, domainCounts(t, db))
				}
			})
		}
		for _, test := range []struct{ name, metadata string }{
			{"numbers", `{"min":-9007199254740991,"max":9007199254740991,"fraction":0.1,"tiny":1e-300,"opaque":[true,null,"kept"]}`},
			{"depth256", `{"v":` + strings.Repeat("[", 255) + `0` + strings.Repeat("]", 255) + `}`},
			{"exact1MiB", `{"v":"` + strings.Repeat("a", (1<<20)-len(`{"v":""}`)) + `"}`},
		} {
			t.Run(test.name, func(t *testing.T) {
				result := create(t, intent("admitted-"+test.name, test.metadata))
				row, _ := read(t, result.ID)
				assert.True(t, contract.EqualMetadata(row.Metadata, json.RawMessage(test.metadata)), "admitted metadata must retain its numeric and unknown values")
			})
		}
	})

	t.Run("historical_metadata_and_provenance_are_preserved", func(t *testing.T) {
		legacy := gormdb.Issue{
			Title: "Synthetic historical task", Body: "Exact historical body", SourceProject: source, TargetProject: source,
			SourceAgent: "historical-agent", CreatedBySession: "historical-session", CreatorKeycardID: "synthetic-shared-a", Metadata: json.RawMessage(`{"n":9007199254740993}`),
		}
		require.NoError(t, issuefixture.SeedHistorical(db, func(tx *gormlib.DB) error { return tx.Create(&legacy).Error }))
		require.NoError(t, db.Create(&gormdb.IssueChange{IssueID: legacy.ID, Revision: 1, Action: "baseline", Changes: json.RawMessage(`{}`)}).Error)
		comment := gormdb.IssueComment{IssueID: legacy.ID, AuthorProject: source, AuthorAgent: "historical-comment-agent", Body: "Exact historical comment", Metadata: json.RawMessage(`{"old":"kept"}`)}
		require.NoError(t, issuefixture.SeedHistorical(db, func(tx *gormlib.DB) error { return tx.Create(&comment).Error }))
		before, oldComments := read(t, legacy.ID)
		_, err := application.Mutate(ctx, scope, legacy.ID, json.RawMessage(`{"expected_revision":"1","priority":"high"}`))
		require.NoError(t, err)
		after, comments := read(t, legacy.ID)
		assert.Equal(t, before.Metadata, after.Metadata)
		assert.Equal(t, before.SourceAgent, after.SourceAgent)
		assert.Equal(t, before.CreatedBySession, after.CreatedBySession)
		assert.Equal(t, before.CreatorKeycardID, after.CreatorKeycardID)
		assert.Equal(t, before.Actor, after.Actor)
		assert.Equal(t, oldComments, comments)
		assert.False(t, contract.EqualMetadata(json.RawMessage(`{"n":9007199254740993}`), json.RawMessage(`{"n":9007199254740992}`)), "legacy numbers cannot round into a false no-op")
	})

	t.Run("create_receipt_failure_rolls_back_issue_and_initial_history", func(t *testing.T) {
		before := domainCounts(t, db)
		name := "issue513_fail_receipt_" + uuid.NewString()
		require.NoError(t, db.Callback().Create().Before("gorm:create").Register(name, func(tx *gormlib.DB) {
			if tx.Statement.Table == "issue_create_receipts" {
				tx.AddError(errors.New("synthetic receipt failure"))
			}
		}))
		_, err := application.Create(ctx, scope, intent("failed-receipt", `{}`))
		require.NoError(t, db.Callback().Create().Remove(name))
		errorCode(t, err, "UNAVAILABLE")
		assert.Equal(t, before, domainCounts(t, db))
		first := create(t, intent("failed-receipt", `{}`))
		assert.False(t, first.Replayed, "the failed transaction did not bind a key")
	})

	t.Run("operational_and_sql_loggers_receive_no_content_key_or_binding", func(t *testing.T) {
		var logs bytes.Buffer
		loud := logger.New(stdlog.New(&logs, "", 0), logger.Config{LogLevel: logger.Info, ParameterizedQueries: false})
		protected := issues.New(gormdb.NewIssueStore(db.Session(&gormlib.Session{Logger: loud})), nil)
		raw := intent("synthetic-log-key", `{"private":"synthetic-log-metadata"}`)
		first, err := protected.Create(ctx, scope, raw)
		require.NoError(t, err)
		_, err = protected.Create(ctx, scope, raw)
		require.NoError(t, err)
		_, err = protected.Mutate(ctx, scope, first.ID, json.RawMessage(`{"expected_revision":"1","body":"synthetic-log-edit","comment":"synthetic-log-comment"}`))
		require.NoError(t, err)
		_, err = protected.Mutate(ctx, scope, first.ID, json.RawMessage(`{"expected_revision":"1","body":"synthetic-stale-edit"}`))
		errorCode(t, err, "REVISION_CONFLICT")
		_, err = protected.Create(ctx, scope, intent("synthetic-log-refusal", fmt.Sprintf(`{"example":%q}`, "api_key="+strings.Repeat("x", 32))))
		errorCode(t, err, "PRIVACY_REJECTED")
		assert.Zero(t, logs.Len(), "even an explicitly content-logging GORM logger must receive no shared-owner SQL")
	})

	t.Run("invalid_revision_and_compound_shapes_have_no_effect", func(t *testing.T) {
		first := create(t, intent("invalid-patches", `{}`))
		for _, raw := range []string{
			`{"expected_revision":"0","body":"invalid"}`, `{"expected_revision":"01","body":"invalid"}`,
			`{"expected_revision":"9223372036854775808","body":"invalid"}`, `{"expected_revision":1,"body":"invalid"}`,
			`{"expected_revision":"1","title":"invalid","status":"invalid","comment":"invalid"}`,
			`{"comment_metadata":{}}`, `{"comment":""}`, `{"expected_revision":"1","metadata":null}`,
			`{"expected_revision":"1","owner_label":""}`, `{"expected_revision":"1","body":null}`,
		} {
			before := domainCounts(t, db)
			_, err := application.Mutate(ctx, scope, first.ID, json.RawMessage(raw))
			errorCode(t, err, "INVALID_ARGUMENT")
			assert.Equal(t, before, domainCounts(t, db))
		}
	})

	t.Run("recognized_and_configured_privacy_refusal_is_atomic", func(t *testing.T) {
		value := "api_key=" + strings.Repeat("x", 32)
		for _, field := range []string{"title", "body", "metadata"} {
			t.Run(field, func(t *testing.T) {
				var fields map[string]json.RawMessage
				require.NoError(t, json.Unmarshal(intent("secret-"+field, `{}`), &fields))
				encoded, err := json.Marshal(value)
				require.NoError(t, err)
				if field == "metadata" {
					fields[field] = json.RawMessage(`{"unknown":{"example":` + string(encoded) + `}}`)
				} else {
					fields[field] = encoded
				}
				raw, err := json.Marshal(fields)
				require.NoError(t, err)
				before := domainCounts(t, db)
				_, err = application.Create(ctx, scope, raw)
				errorCode(t, err, "PRIVACY_REJECTED")
				assert.NotContains(t, err.Error(), value)
				assert.Equal(t, before, domainCounts(t, db))
			})
		}
		rules, err := redaction.CompileRules([]redaction.Rule{{ID: "synthetic-rule", Pattern: "synthetic-blocked-value", Replacement: "[removed]"}})
		require.NoError(t, err)
		protected := issues.New(store, rules)
		before := domainCounts(t, db)
		_, err = protected.Create(ctx, scope, intent("configured-refusal", `{"unknown":"synthetic-blocked-value"}`))
		errorCode(t, err, "PRIVACY_REJECTED")
		assert.Equal(t, before, domainCounts(t, db))
		first := create(t, intent("privacy-compound", `{}`))
		row, comments := read(t, first.ID)
		before = domainCounts(t, db)
		_, err = application.Mutate(ctx, scope, first.ID, json.RawMessage(fmt.Sprintf(`{"expected_revision":"1","body":"Synthetic companion edit","comment":%q}`, value)))
		errorCode(t, err, "PRIVACY_REJECTED")
		after, afterComments := read(t, first.ID)
		assert.True(t, reflect.DeepEqual(row, after))
		assert.Equal(t, comments, afterComments)
		assert.Equal(t, before, domainCounts(t, db))
	})
}

func TestSharedIssueMaterialWriteGuardPostgres(t *testing.T) {
	db := sharedDB(t)
	var database, schema string
	require.NoError(t, db.Raw(`SELECT current_database(),current_schema()`).Row().Scan(&database, &schema))
	t.Logf("fixture database=%s schema=%s", database, schema)
	var migrated int64
	require.NoError(t, db.Table("migrations").Where("id = ?", "193_issue_internal_tasks").Count(&migrated).Error)
	require.EqualValues(t, 1, migrated, "exercise ordinary writes after the real migration chain")
	store := gormdb.NewIssueStore(db)
	application := issues.New(store, nil)
	ctx := auth.WithIdentity(t.Context(), auth.Client("read-write", "synthetic-shared-a"))
	collaborator := auth.WithIdentity(t.Context(), auth.Client("read-write", "synthetic-shared-b"))
	scope := issues.Scope{SourceProject: source}
	companionActor, err := issues.CaptureActor(collaborator)
	require.NoError(t, err)
	companionDescriptor := companionActor.Descriptor()
	seed := func(t *testing.T) int64 {
		t.Helper()
		created, err := application.Create(ctx, scope, intent(t.Name(), `{"retained":{"n":1}}`))
		require.NoError(t, err)
		require.Equal(t, "1", created.Revision)
		commented, err := application.Mutate(collaborator, scope, created.ID, json.RawMessage(`{"comment":"Synthetic retained comment","comment_metadata":{"retained":true}}`))
		require.NoError(t, err)
		require.NotNil(t, commented.CommentID)
		require.Equal(t, "1", commented.Revision)
		require.NoError(t, db.Exec(`UPDATE issues SET updated_at='2000-01-01T00:00:00Z' WHERE id=?`, created.ID).Error)
		return created.ID
	}
	snapshot := func(t *testing.T, id int64) map[string]string {
		t.Helper()
		result := make(map[string]string)
		for _, table := range []string{"issues", "issue_comments", "issue_changes", "issue_create_receipts"} {
			key := "issue_id"
			if table == "issues" {
				key = "id"
			}
			var value string
			require.NoError(t, db.Raw(`SELECT COALESCE(jsonb_agg(to_jsonb(t) ORDER BY to_jsonb(t)::text),'[]'::jsonb)::text FROM `+table+` t WHERE `+key+`=?`, id).Row().Scan(&value))
			result[table] = value
		}
		return result
	}
	unchanged := func(t *testing.T, before, after map[string]string) {
		t.Helper()
		for _, table := range []string{"issues", "issue_comments", "issue_changes", "issue_create_receipts"} {
			assert.True(t, before[table] == after[table], "%s snapshot must remain unchanged", table)
		}
	}

	for _, test := range []struct{ name, assignment string }{
		{"title", `title='Synthetic incompatible title'`},
		{"body", `body='Synthetic incompatible body'`},
		{"priority", `priority='high'`},
		{"type", `type='bug'`},
		{"status_and_timestamp", `status='acknowledged',acknowledged_at='2026-01-02T03:04:05Z'`},
		{"labels", `labels='["incompatible"]'::jsonb`},
		{"metadata", `metadata='{"incompatible":{"n":2}}'::jsonb`},
		{"owner_label", `owner_label='synthetic-owner'`},
		{"recipient_label", `recipient_label='synthetic-recipient'`},
		{"target_session", `target_session='synthetic-recipient-session'`},
		{"acknowledged_at", `acknowledged_at='2026-01-02T03:04:05Z'`},
		{"resolved_at", `resolved_at='2026-01-02T03:04:05Z'`},
		{"reopened_at", `reopened_at='2026-01-02T03:04:05Z'`},
		{"closed_at", `closed_at='2026-01-02T03:04:05Z'`},
	} {
		t.Run("unchanged_revision_rejects_"+test.name, func(t *testing.T) {
			id := seed(t)
			before := snapshot(t, id)
			// No synthetic rollback sentinel: a wrongly accepted write really
			// commits, so both denial and conservation assertions expose RED.
			companionInserted := false
			err := db.Transaction(func(tx *gormlib.DB) error {
				// The companion must pass INSERT admission; only the material
				// UPDATE guard may reject this otherwise valid transaction.
				if err := tx.Exec(`INSERT INTO issue_comments (issue_id,author_project,body,actor_kind,actor_reference) VALUES (?,?,'Synthetic incompatible companion',?,?)`, id, source, companionDescriptor.Kind, companionDescriptor.Reference).Error; err != nil {
					return err
				}
				companionInserted = true
				updated := tx.Exec(`UPDATE issues SET `+test.assignment+`,updated_at='2026-01-02T03:04:06Z' WHERE id=?`, id)
				if updated.Error == nil {
					require.EqualValues(t, 1, updated.RowsAffected, "the probe must target its real issue")
				}
				return updated.Error
			})
			assert.True(t, companionInserted, "INSERT admission must not mask the material UPDATE guard oracle")
			var failure interface{ SQLState() string }
			if assert.True(t, errors.As(err, &failure), "a material write without revision advancement must be denied by PostgreSQL") {
				assert.Equal(t, "23514", failure.SQLState(), "a missing table, malformed query or unrelated persistence failure is not behavioral RED")
			}
			unchanged(t, before, snapshot(t, id))
		})
	}

	t.Run("current_application_commits_material_revision_history_and_comment", func(t *testing.T) {
		id := seed(t)
		before := snapshot(t, id)
		updated, err := application.Mutate(ctx, scope, id, json.RawMessage(`{"expected_revision":"1","title":"Synthetic compatible title","status":"resolved","metadata":{"compatible":{"n":2}},"comment":"Synthetic compatible outcome","comment_metadata":{"outcome":true}}`))
		require.NoError(t, err)
		assert.True(t, updated.Changed)
		assert.Equal(t, "2", updated.Revision)
		require.NotNil(t, updated.CommentID)
		row, comments, err := store.GetIssue(t.Context(), id)
		require.NoError(t, err)
		assert.Equal(t, "Synthetic compatible title", row.Title)
		assert.Equal(t, "resolved", row.Status)
		assert.NotNil(t, row.ResolvedAt)
		assert.EqualValues(t, 2, row.Revision)
		assert.JSONEq(t, `{"compatible":{"n":2}}`, string(row.Metadata))
		require.Len(t, comments, 2)
		assert.Equal(t, *updated.CommentID, comments[1].ID)
		assert.Equal(t, "Synthetic compatible outcome", comments[1].Body)
		assert.JSONEq(t, `{"outcome":true}`, string(comments[1].Metadata))
		var history []gormdb.IssueChange
		require.NoError(t, db.Where("issue_id=?", id).Order("revision").Find(&history).Error)
		require.Len(t, history, 2)
		assert.Equal(t, "created", history[0].Action)
		assert.Equal(t, "updated", history[1].Action)
		assert.EqualValues(t, 2, history[1].Revision)
		require.NotNil(t, history[1].PreviousRevision)
		assert.EqualValues(t, 1, *history[1].PreviousRevision)
		assert.ElementsMatch(t, []string{"title", "status", "resolved_at", "metadata"}, []string(history[1].ChangedFields))
		assert.Equal(t, "client", history[1].Actor.Kind)
		require.NotNil(t, history[1].Actor.Reference)
		assert.Equal(t, "client:synthetic-shared-a", *history[1].Actor.Reference)
		after := snapshot(t, id)
		assert.True(t, before["issue_create_receipts"] == after["issue_create_receipts"])
	})

	t.Run("current_application_and_SQL_same_value_noops_preserve_all_rows", func(t *testing.T) {
		id := seed(t)
		before := snapshot(t, id)
		result, err := application.Mutate(ctx, scope, id, json.RawMessage(`{"expected_revision":"1","title":"Synthetic shared task","labels":["b","a","b"],"metadata":{"retained":{"n":1.0}}}`))
		require.NoError(t, err)
		assert.False(t, result.Changed)
		assert.Equal(t, "1", result.Revision)
		assert.Nil(t, result.CommentID)
		unchanged(t, before, snapshot(t, id))
		updated := db.Exec(`UPDATE issues SET title=title,status=status,metadata=metadata,revision=revision WHERE id=?`, id)
		require.NoError(t, updated.Error)
		require.EqualValues(t, 1, updated.RowsAffected)
		unchanged(t, before, snapshot(t, id))
	})

	t.Run("current_comment_only_append_updates_timestamp_without_revision_or_history", func(t *testing.T) {
		id := seed(t)
		before, beforeComments, err := store.GetIssue(t.Context(), id)
		require.NoError(t, err)
		beforeSnapshot := snapshot(t, id)
		result, err := application.Mutate(collaborator, scope, id, json.RawMessage(`{"comment":"Synthetic comment-only outcome","comment_metadata":{"outcome":true}}`))
		require.NoError(t, err)
		assert.False(t, result.Changed)
		assert.Equal(t, "1", result.Revision)
		require.NotNil(t, result.CommentID)
		after, afterComments, err := store.GetIssue(t.Context(), id)
		require.NoError(t, err)
		assert.True(t, after.UpdatedAt.After(before.UpdatedAt), "fixed old fixture timestamp avoids a timing-dependent assertion")
		after.UpdatedAt = before.UpdatedAt
		assert.Equal(t, before, after, "comment append must change no issue field except updated_at")
		require.Len(t, afterComments, len(beforeComments)+1)
		assert.Equal(t, beforeComments, afterComments[:len(beforeComments)])
		appended := afterComments[len(beforeComments)]
		assert.Equal(t, *result.CommentID, appended.ID)
		assert.Equal(t, "Synthetic comment-only outcome", appended.Body)
		assert.JSONEq(t, `{"outcome":true}`, string(appended.Metadata))
		afterSnapshot := snapshot(t, id)
		for _, table := range []string{"issue_changes", "issue_create_receipts"} {
			assert.True(t, beforeSnapshot[table] == afterSnapshot[table], "%s must not change for a comment-only append", table)
		}
	})

	t.Run("project_role_only_update_preserves_material_revision_and_history", func(t *testing.T) {
		id := seed(t)
		before, beforeComments, err := store.GetIssue(t.Context(), id)
		require.NoError(t, err)
		beforeSnapshot := snapshot(t, id)
		updated := db.Exec(`UPDATE issues SET target_project=? WHERE id=?`, otherSource, id)
		require.NoError(t, updated.Error)
		require.EqualValues(t, 1, updated.RowsAffected)
		after, afterComments, err := store.GetIssue(t.Context(), id)
		require.NoError(t, err)
		assert.Equal(t, otherSource, after.TargetProject)
		after.TargetProject = before.TargetProject
		assert.Equal(t, before, after)
		assert.Equal(t, beforeComments, afterComments)
		afterSnapshot := snapshot(t, id)
		for _, table := range []string{"issue_comments", "issue_changes", "issue_create_receipts"} {
			assert.True(t, beforeSnapshot[table] == afterSnapshot[table], "%s must not change for a project-role-only update", table)
		}
	})
}
