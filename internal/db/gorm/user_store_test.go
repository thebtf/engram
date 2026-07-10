package gorm

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"gorm.io/driver/postgres"
	gormio "gorm.io/gorm"
)

func openIsolatedUserStore(t *testing.T) (*UserStore, *gormio.DB) {
	t.Helper()
	dsn := os.Getenv("DATABASE_DSN")
	if dsn == "" {
		t.Skip("DATABASE_DSN not set, skipping user store integration test")
	}

	schema := fmt.Sprintf("user_store_test_%d", time.Now().UnixNano())
	rootDB, err := gormio.Open(postgres.Open(dsn), &gormio.Config{})
	require.NoError(t, err)
	rootSQLDB, err := rootDB.DB()
	require.NoError(t, err)
	rootSQLDB.SetMaxOpenConns(1)
	rootSQLDB.SetMaxIdleConns(1)
	require.NoError(t, rootDB.Exec(fmt.Sprintf(`CREATE SCHEMA %q`, schema)).Error)
	t.Cleanup(func() {
		require.NoError(t, rootDB.Exec(fmt.Sprintf(`DROP SCHEMA %q CASCADE`, schema)).Error)
		_ = rootSQLDB.Close()
	})

	parsedDSN, err := url.Parse(dsn)
	require.NoError(t, err)
	query := parsedDSN.Query()
	query.Set("search_path", schema)
	parsedDSN.RawQuery = query.Encode()

	db, err := gormio.Open(postgres.Open(parsedDSN.String()), &gormio.Config{})
	require.NoError(t, err)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	sqlDB.SetMaxOpenConns(4)
	sqlDB.SetMaxIdleConns(2)
	require.NoError(t, db.AutoMigrate(&User{}))
	t.Cleanup(func() {
		_ = sqlDB.Close()
	})

	return NewUserStore(db), db
}

func TestUserStore_UpdateUserWithLastAdminGuard_ConcurrentDemoteDisableLeavesOneAdmin(t *testing.T) {
	users, db := openIsolatedUserStore(t)

	for iteration := 0; iteration < 20; iteration++ {
		require.NoError(t, db.Exec("DELETE FROM users").Error)
		adminA, err := users.CreateUser(fmt.Sprintf("admin-a-%d@example.com", iteration), "hash", DashboardRoleAdmin)
		require.NoError(t, err)
		adminB, err := users.CreateUser(fmt.Sprintf("admin-b-%d@example.com", iteration), "hash", DashboardRoleAdmin)
		require.NoError(t, err)

		start := make(chan struct{})
		results := make(chan error, 2)
		go func() {
			<-start
			role := DashboardRoleOperator
			_, updateErr := users.UpdateUserWithLastAdminGuard(adminA.ID, &role, nil)
			results <- updateErr
		}()
		go func() {
			<-start
			disabled := true
			_, updateErr := users.UpdateUserWithLastAdminGuard(adminB.ID, nil, &disabled)
			results <- updateErr
		}()
		close(start)

		errs := []error{<-results, <-results}
		successes := 0
		guardFailures := 0
		for _, updateErr := range errs {
			if updateErr == nil {
				successes++
				continue
			}
			guardFailures++
			require.Contains(t, []string{"cannot demote the last admin", "cannot disable the last admin"}, updateErr.Error())
			require.NotContains(t, strings.ToLower(updateErr.Error()), "deadlock")
		}
		require.Equal(t, 1, successes, "iteration %d", iteration)
		require.Equal(t, 1, guardFailures, "iteration %d", iteration)

		count, err := users.CountAdmins()
		require.NoError(t, err)
		require.Equal(t, int64(1), count, "iteration %d", iteration)

		adminA, err = users.GetUserByID(adminA.ID)
		require.NoError(t, err)
		adminB, err = users.GetUserByID(adminB.ID)
		require.NoError(t, err)
		activeAdmins := 0
		for _, admin := range []*User{adminA, adminB} {
			if admin.Role == DashboardRoleAdmin && !admin.Disabled {
				activeAdmins++
			}
		}
		require.Equal(t, 1, activeAdmins, "iteration %d", iteration)
	}
}

func TestUserStore_UpdateUserWithLastAdminGuard_LocksActiveAdminSet(t *testing.T) {
	users, db := openIsolatedUserStore(t)
	adminA, err := users.CreateUser("lock-a@example.com", "hash", DashboardRoleAdmin)
	require.NoError(t, err)
	adminB, err := users.CreateUser("lock-b@example.com", "hash", DashboardRoleAdmin)
	require.NoError(t, err)

	tx := db.Begin()
	require.NoError(t, tx.Error)
	var lockedID int64
	require.NoError(t, tx.Raw("SELECT id FROM users WHERE id = ? FOR UPDATE", adminA.ID).Scan(&lockedID).Error)
	require.Equal(t, adminA.ID, lockedID)

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	role := DashboardRoleOperator
	_, err = NewUserStore(db.WithContext(ctx)).UpdateUserWithLastAdminGuard(adminB.ID, &role, nil)
	require.Error(t, err)
	require.True(t, errors.Is(err, context.DeadlineExceeded) || strings.Contains(strings.ToLower(err.Error()), "canceling statement"), err.Error())
	require.NotContains(t, strings.ToLower(err.Error()), "deadlock")
	require.NoError(t, tx.Rollback().Error)

	adminB, err = users.GetUserByID(adminB.ID)
	require.NoError(t, err)
	require.Equal(t, DashboardRoleAdmin, adminB.Role)
	require.False(t, adminB.Disabled)

	adminB, err = users.UpdateUserWithLastAdminGuard(adminB.ID, &role, nil)
	require.NoError(t, err)
	require.Equal(t, DashboardRoleOperator, adminB.Role)
	_, err = users.UpdateUserWithLastAdminGuard(adminA.ID, &role, nil)
	require.EqualError(t, err, "cannot demote the last admin")
}

func TestUserStore_UpdateUserWithLastAdminGuard_DisabledAdminCanBeDemoted(t *testing.T) {
	users, _ := openIsolatedUserStore(t)
	active, err := users.CreateUser("active@example.com", "hash", DashboardRoleAdmin)
	require.NoError(t, err)
	disabledAdmin, err := users.CreateUser("disabled@example.com", "hash", DashboardRoleAdmin)
	require.NoError(t, err)

	disabled := true
	disabledAdmin, err = users.UpdateUserWithLastAdminGuard(disabledAdmin.ID, nil, &disabled)
	require.NoError(t, err)
	require.True(t, disabledAdmin.Disabled)

	role := DashboardRoleOperator
	disabledAdmin, err = users.UpdateUserWithLastAdminGuard(disabledAdmin.ID, &role, nil)
	require.NoError(t, err)
	require.Equal(t, DashboardRoleOperator, disabledAdmin.Role)
	require.True(t, disabledAdmin.Disabled)

	active, err = users.GetUserByID(active.ID)
	require.NoError(t, err)
	require.Equal(t, DashboardRoleAdmin, active.Role)
	require.False(t, active.Disabled)
	count, err := users.CountAdmins()
	require.NoError(t, err)
	require.Equal(t, int64(1), count)
}

func TestUserStore_UpdateUserWithLastAdminGuard_NormalizedAdminRoleIsNotDemotion(t *testing.T) {
	users, _ := openIsolatedUserStore(t)
	admin, err := users.CreateUser("normalized@example.com", "hash", DashboardRoleAdmin)
	require.NoError(t, err)

	role := DashboardRoleAdmin
	admin, err = users.UpdateUserWithLastAdminGuard(admin.ID, &role, nil)
	require.NoError(t, err)
	require.Equal(t, DashboardRoleAdmin, admin.Role)
	require.False(t, admin.Disabled)
}
