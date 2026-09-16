package gorm

import (
	"database/sql"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"gorm.io/driver/postgres"
	gormlib "gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func TestMigration167APITokenExpiryPreservesLegacyNulls(t *testing.T) {
	dsn := os.Getenv("DATABASE_DSN")
	if dsn == "" {
		t.Skip("DATABASE_DSN not set, skipping integration test")
	}
	db, err := gormlib.Open(postgres.Open(dsn), &gormlib.Config{Logger: logger.Default.LogMode(logger.Warn)})
	require.NoError(t, err)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	defer sqlDB.Close()
	require.NoError(t, sqlDB.Ping())
	require.NoError(t, runMigrations(db))

	var nullable string
	require.NoError(t, db.Raw(`
		SELECT is_nullable
		FROM information_schema.columns
		WHERE table_schema = 'public' AND table_name = 'api_tokens' AND column_name = 'expires_at'
	`).Row().Scan(&nullable))
	require.Equal(t, "YES", nullable)

	prefix := fmt.Sprintf("p167%04x", time.Now().UnixNano()&0xffff)
	legacyName := prefix + "-legacy"
	expiringName := prefix + "-expiring"
	t.Cleanup(func() { _ = db.Exec(`DELETE FROM api_tokens WHERE name IN (?, ?)`, legacyName, expiringName).Error })

	require.NoError(t, db.Exec(`
		INSERT INTO api_tokens (name, token_hash, token_prefix, scope)
		VALUES (?, 'migration-legacy-hash', ?, 'read-write')
	`, legacyName, prefix+"a").Error)
	var legacyExpiry sql.NullTime
	require.NoError(t, db.Raw(`SELECT expires_at FROM api_tokens WHERE name = ?`, legacyName).Row().Scan(&legacyExpiry))
	require.False(t, legacyExpiry.Valid)

	expiresAt := time.Now().UTC().Add(time.Hour).Truncate(time.Microsecond)
	require.NoError(t, db.Exec(`
		INSERT INTO api_tokens (name, token_hash, token_prefix, scope, expires_at)
		VALUES (?, 'migration-expiring-hash', ?, 'read-write', ?)
	`, expiringName, prefix+"b", expiresAt).Error)
	var storedExpiry time.Time
	require.NoError(t, db.Raw(`SELECT expires_at FROM api_tokens WHERE name = ?`, expiringName).Row().Scan(&storedExpiry))
	require.WithinDuration(t, expiresAt, storedExpiry, time.Microsecond)
}
