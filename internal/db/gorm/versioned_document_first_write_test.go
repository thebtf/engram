package gorm

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/stretchr/testify/require"
	"github.com/thebtf/engram/internal/projectidentity"
)

func TestVersionedDocumentFirstWritePostgres(t *testing.T) {
	db, _ := openUCIContextMigrationStore(t)
	ctx := context.Background()
	backing := &Store{DB: db}
	documents := NewVersionedDocumentStore(backing)
	sessions := NewSessionStore(backing)

	var migrationCount int64
	require.NoError(t, db.Table("migrations").Where("id IN ?", []string{"186_project_consolidation", "187_logical_source_project"}).Count(&migrationCount).Error)
	require.EqualValues(t, 2, migrationCount, "requires the actual migration186/187 chain")
	var guardMode, replicationRole string
	require.NoError(t, db.Raw(`SELECT t.tgenabled::text FROM pg_trigger t JOIN pg_class c ON c.oid=t.tgrelid
		JOIN pg_namespace n ON n.oid=c.relnamespace WHERE n.nspname=current_schema()
		AND c.relname='versioned_documents' AND t.tgname='engram_consolidation_scope_write' AND NOT t.tgisinternal`).Scan(&guardMode).Error)
	require.Contains(t, []string{"O", "A"}, guardMode, "mandatory SQL write guard must fire for ordinary writes")
	require.NoError(t, db.Raw("SHOW session_replication_role").Scan(&replicationRole).Error)
	require.Equal(t, "origin", replicationRole)

	count := func(t *testing.T, table, column, project string) int64 {
		t.Helper()
		var n int64
		require.NoError(t, db.Table(table).Where(column+" = ?", project).Count(&n).Error)
		return n
	}

	t.Run("insert_error_rolls_back_project_registration", func(t *testing.T) {
		project := uuid.NewString()
		require.Zero(t, count(t, "projects", "id", project))
		id, err := documents.Create(ctx, "first-write/rollback.md", project, "must roll back", "markdown", "{", "first-write-test")
		require.Zero(t, id)
		require.ErrorContains(t, err, "insert document")
		var sqlError *pgconn.PgError
		require.ErrorAs(t, err, &sqlError)
		require.Equal(t, "22P02", sqlError.Code, "real PostgreSQL jsonb input must reject the document insert")
		require.Zero(t, count(t, "projects", "id", project), "registration must share the failed document transaction")
		require.Zero(t, count(t, "versioned_documents", "project", project))
		exists, err := sessions.CanonicalProjectExists(ctx, project)
		require.NoError(t, err)
		require.False(t, exists)
	})

	t.Run("removed_and_merged_projects_are_not_resurrected", func(t *testing.T) {
		removedAt := time.Now().UTC().Truncate(time.Microsecond)
		for _, tc := range []struct {
			name string
			row  Project
		}{
			{name: "removed", row: Project{ID: uuid.NewString(), RemovedAt: &removedAt}},
			{name: "merged", row: Project{ID: uuid.NewString(), IdentityStatus: sql.NullString{String: "merged", Valid: true}}},
		} {
			t.Run(tc.name, func(t *testing.T) {
				require.NoError(t, db.Create(&tc.row).Error)
				var before Project
				require.NoError(t, db.Where("id = ?", tc.row.ID).Take(&before).Error)
				id, err := documents.Create(ctx, "first-write/refused.md", tc.row.ID, "must not resurrect", "markdown", "{}", "first-write-test")
				require.Zero(t, id)
				var logical *projectidentity.LogicalError
				require.ErrorAs(t, err, &logical)
				require.Equal(t, "PROJECT_IDENTITY_CONFLICT", logical.Code)
				require.Zero(t, count(t, "versioned_documents", "project", tc.row.ID))
				require.EqualValues(t, 1, count(t, "projects", "id", tc.row.ID))
				var after Project
				require.NoError(t, db.Where("id = ?", tc.row.ID).Take(&after).Error)
				require.Equal(t, before.RemovedAt, after.RemovedAt)
				require.Equal(t, before.IdentityStatus, after.IdentityStatus)
				exists, err := sessions.CanonicalProjectExists(ctx, tc.row.ID)
				require.NoError(t, err)
				require.False(t, exists)
			})
		}
	})

	t.Run("global_and_migration_inputs_remain_sql_owned", func(t *testing.T) {
		var projectsBefore, projectsAfter int64
		require.NoError(t, db.Model(&Project{}).Count(&projectsBefore).Error)
		id, err := documents.Create(ctx, "first-write/global.md", "", "global document", "markdown", "{}", "first-write-test")
		require.NoError(t, err)
		global, err := documents.ReadByID(ctx, id)
		require.NoError(t, err)
		require.Empty(t, global.Project)
		require.Equal(t, "global document", global.Content)
		require.NoError(t, db.Model(&Project{}).Count(&projectsAfter).Error)
		require.Equal(t, projectsBefore, projectsAfter, "global input must not mint a project")

		project := uuid.NewString()
		selector := "first-write-proved-migration"
		require.NoError(t, db.Create(&Project{ID: project}).Error)
		require.NoError(t, db.Exec(`INSERT INTO project_consolidation_redirects(selector,project_id,plan_fingerprint) VALUES(?,?,?)`, selector, project, "first-write-test").Error)
		id, err = documents.Create(ctx, "first-write/migrated.md", selector, "proved migration document", "markdown", "{}", "first-write-test")
		require.NoError(t, err, "the existing SQL guard must retain valid migration redirects")
		migrated, err := documents.ReadByID(ctx, id)
		require.NoError(t, err)
		require.Equal(t, project, migrated.Project)
		require.Equal(t, "proved migration document", migrated.Content)
		require.Zero(t, count(t, "projects", "id", selector), "a migration selector is not a new canonical project")
		reopened, err := documents.ReadLatest(ctx, migrated.Path, project)
		require.NoError(t, err)
		require.Equal(t, id, reopened.ID)

		unresolved := "first-write-unresolved-migration"
		id, err = documents.Create(ctx, "first-write/unresolved.md", unresolved, "must remain refused", "markdown", "{}", "first-write-test")
		require.Zero(t, id)
		require.ErrorContains(t, err, "PROJECT_ADOPTION_UNRESOLVED_MEMBERSHIP")
		require.Zero(t, count(t, "projects", "id", unresolved))
		require.Zero(t, count(t, "versioned_documents", "project", unresolved))
	})
}
