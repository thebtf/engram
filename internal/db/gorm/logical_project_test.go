package gorm

import (
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"github.com/thebtf/engram/internal/projectidentity"
	"github.com/thebtf/engram/internal/uci"
	"github.com/thebtf/engram/pkg/models"
	gormlib "gorm.io/gorm"
)

func TestLogicalProjectFirstWritePostgres(t *testing.T) {
	db, code := openUCIContextMigrationStore(t)
	ctx := context.Background()
	backing := &Store{DB: db}
	memories, sessions := NewMemoryStore(backing), NewSessionStore(backing)
	count := func(table, column, project string) int64 {
		t.Helper()
		var n int64
		require.NoError(t, db.Table(table).Where(column+" = ?", project).Count(&n).Error)
		return n
	}
	assertCatalog := func(project string) {
		t.Helper()
		catalog, err := sessions.GetAllProjects(ctx)
		require.NoError(t, err)
		found := false
		for _, row := range catalog {
			if row.ID == project {
				found = true
				require.Equal(t, "Registered project", row.DisplayName)
			}
		}
		require.True(t, found, "first write must expose the exact selected UUID")
		require.EqualValues(t, 1, count("projects", "id", project))
	}
	for _, mode := range []string{"normal", "lifecycle", "caller_transaction", "unique_tag"} {
		t.Run(mode, func(t *testing.T) {
			project := uuid.NewString()
			exists, err := sessions.CanonicalProjectExists(ctx, project)
			require.NoError(t, err)
			require.False(t, exists)
			mem := &models.Memory{Project: project, Content: "first real write " + mode, SourceAgent: "first-write", PrivacyScope: "project", Tags: []string{"first-write"}}
			var created *models.Memory
			switch mode {
			case "normal":
				created, err = memories.Create(ctx, mem)
			case "lifecycle":
				created, err = memories.CreateWithLifecycle(ctx, mem)
			case "caller_transaction":
				err = db.Transaction(func(tx *gormlib.DB) error {
					created, err = memories.CreateWithLifecycleTx(ctx, tx, mem)
					return err
				})
			case "unique_tag":
				var duplicate bool
				created, duplicate, err = memories.CreateWithLifecycleIfTagAbsent(ctx, mem, "first-write")
				require.False(t, duplicate)
			}
			require.NoError(t, err)
			require.Equal(t, project, created.Project)
			assertCatalog(project)
			read, err := memories.List(ctx, project, 10)
			require.NoError(t, err)
			require.Len(t, read, 1)
			require.Equal(t, created.ID, read[0].ID)
			require.Equal(t, project, read[0].Project)
		})
	}
	t.Run("memory_error_rolls_back_catalog", func(t *testing.T) {
		require.NoError(t, db.Exec("ALTER TABLE memories ADD CONSTRAINT first_write_failure CHECK (content <> 'reject after project insert')").Error)
		for _, lifecycle := range []bool{false, true} {
			project := uuid.NewString()
			mem := &models.Memory{Project: project, Content: "reject after project insert", PrivacyScope: "project"}
			var err error
			if lifecycle {
				_, err = memories.CreateWithLifecycle(ctx, mem)
			} else {
				_, err = memories.Create(ctx, mem)
			}
			require.Error(t, err)
			require.Zero(t, count("projects", "id", project))
			require.Zero(t, count("memories", "project", project))
		}
	})
	t.Run("caller_rollback_keeps_catalog_absent", func(t *testing.T) {
		project := uuid.NewString()
		abort := errors.New("caller aborts after successful insert")
		err := db.Transaction(func(tx *gormlib.DB) error {
			_, err := memories.CreateWithLifecycleTx(ctx, tx, &models.Memory{Project: project, Content: "rolled back", PrivacyScope: "project"})
			if err != nil {
				return err
			}
			return abort
		})
		require.ErrorIs(t, err, abort)
		require.Zero(t, count("projects", "id", project))
		require.Zero(t, count("memories", "project", project))
	})
	t.Run("source_first_write_and_failure_are_atomic", func(t *testing.T) {
		project := uuid.NewString()
		registered, err := code.RegisterLocalGit(ctx, RegisterLocalGitInput{ProjectID: project, AuthRealm: uci.NoAuthCodeRealm, Principal: uci.NoAuthCodePrincipal, WorkstationID: "first-write-device", SourceLabel: "first source", Locator: "file:///first-write/source"})
		require.NoError(t, err)
		assertCatalog(project)
		var source UCISource
		require.NoError(t, db.Where("source_id = ?", registered.SourceID).Take(&source).Error)
		require.NotNil(t, source.ProjectID)
		require.Equal(t, project, *source.ProjectID)
		failed := uuid.NewString()
		_, err = code.RegisterLocalGit(ctx, RegisterLocalGitInput{ProjectID: failed, AuthRealm: uci.NoAuthCodeRealm, Principal: uci.NoAuthCodePrincipal, WorkstationID: "first-write-device", SourceID: uuid.NewString(), Locator: "file:///first-write/unknown-source"})
		require.Error(t, err)
		require.Zero(t, count("projects", "id", failed))
		require.Zero(t, count("sources", "project_id", failed))
	})
	t.Run("reads_do_not_register", func(t *testing.T) {
		project := uuid.NewString()
		_, err := sessions.GetAllProjects(ctx)
		require.NoError(t, err)
		exists, err := sessions.CanonicalProjectExists(ctx, project)
		require.NoError(t, err)
		require.False(t, exists)
		_, err = memories.List(ctx, project, 10)
		require.NoError(t, err)
		require.Zero(t, count("projects", "id", project))
	})
	t.Run("removed_and_merged_are_not_resurrected", func(t *testing.T) {
		removed := time.Now().UTC()
		for _, row := range []Project{{ID: uuid.NewString(), RemovedAt: &removed}, {ID: uuid.NewString(), IdentityStatus: sql.NullString{String: "merged", Valid: true}}} {
			require.NoError(t, db.Create(&row).Error)
			_, err := memories.Create(ctx, &models.Memory{Project: row.ID, Content: "must not resurrect", PrivacyScope: "project"})
			var logical *projectidentity.LogicalError
			require.ErrorAs(t, err, &logical)
			require.Equal(t, "PROJECT_IDENTITY_CONFLICT", logical.Code)
			require.Zero(t, count("memories", "project", row.ID))
			exists, err := sessions.CanonicalProjectExists(ctx, row.ID)
			require.NoError(t, err)
			require.False(t, exists)
		}
	})
}
