package gorm

import (
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"gorm.io/driver/postgres"
	gormlib "gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func TestNewStoreMigrationFailureClosesOwnedPool(t *testing.T) {
	dsn, schema := storeTestDSN(t)
	observer, err := gormlib.Open(postgres.Open(dsn), &gormlib.Config{Logger: logger.Default.LogMode(logger.Silent)})
	require.True(t, err == nil, "open synthetic constructor fixture observer")
	observerSQL, err := observer.DB()
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, observerSQL.Close()) })

	// An incompatible migration ID column fails before any migration body runs.
	require.NoError(t, observer.Exec("CREATE TABLE "+schema+".migrations (id integer PRIMARY KEY)").Error)
	parsedDSN, err := url.Parse(dsn)
	require.True(t, err == nil, "parse synthetic constructor fixture DSN")
	query := parsedDSN.Query()
	query.Set("application_name", schema)
	parsedDSN.RawQuery = query.Encode()

	for range 3 {
		store, err := NewStore(Config{DSN: parsedDSN.String(), LogLevel: logger.Silent})
		if store != nil {
			_ = store.Close()
		}
		require.Nil(t, store)
		require.Error(t, err)
		require.True(t, strings.HasPrefix(err.Error(), "run migrations:") && strings.Contains(err.Error(), "invalid input syntax for type integer"), "constructor must reach the deliberate migration ID column error")
		require.Eventually(t, func() bool {
			var count int64
			err := observer.Raw(`SELECT count(*) FROM pg_stat_activity
				WHERE datname = current_database() AND application_name = ?`, schema).Scan(&count).Error
			return err == nil && count == 0
		}, time.Second, 10*time.Millisecond, "failed constructor must release its PostgreSQL pool")
		require.NoError(t, observerSQL.Ping(), "caller-owned observer must remain open")
	}
}
