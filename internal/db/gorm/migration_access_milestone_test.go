package gorm

import (
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// TestMigrations_AccessMilestone verifies migration 156 adds the invite/session
// lifecycle columns used by the operator-console access lane.
func TestMigrations_AccessMilestone(t *testing.T) {
	db, cleanup := openTestDB(t)
	defer cleanup()

	for _, tc := range []struct {
		table  string
		column string
	}{
		{table: "invitations", column: "invitee_email"},
		{table: "invitations", column: "role"},
		{table: "invitations", column: "expires_at"},
		{table: "invitations", column: "revoked_at"},
		{table: "invitations", column: "revoked_by"},
		{table: "invitations", column: "revocation_reason"},
		{table: "sessions", column: "user_agent"},
		{table: "sessions", column: "remote_addr"},
		{table: "sessions", column: "revoked_at"},
		{table: "sessions", column: "revoked_by"},
		{table: "sessions", column: "revocation_reason"},
	} {
		var count int
		require.NoError(t, db.Raw(`
			SELECT COUNT(*)
			FROM information_schema.columns
			WHERE table_schema = 'public'
			  AND table_name = ?
			  AND column_name = ?
		`, tc.table, tc.column).Scan(&count).Error)
		require.Equalf(t, 1, count, "%s.%s must exist", tc.table, tc.column)
	}

	for _, indexName := range []string{
		"idx_invitations_expires_created",
		"idx_invitations_created_by_created",
		"idx_sessions_user_active_created",
		"idx_sessions_revoked_expires",
	} {
		var count int
		require.NoError(t, db.Raw(`
			SELECT COUNT(*)
			FROM pg_indexes
			WHERE schemaname = 'public'
			  AND indexname = ?
		`, indexName).Scan(&count).Error)
		require.Equalf(t, 1, count, "index %s must exist", indexName)
	}

	userEmail := fmt.Sprintf("zz-access-migration-%d@example.com", time.Now().UnixNano())
	require.NoError(t, db.Exec(`
		INSERT INTO users (email, password_hash, role, disabled, created_at)
		VALUES (?, 'hash', 'admin', false, now())
	`, userEmail).Error)
	defer func() {
		_ = db.Exec(`DELETE FROM invitations WHERE code LIKE 'zz-access-%'`).Error
		_ = db.Exec(`DELETE FROM users WHERE email = ?`, userEmail).Error
	}()

	var userID int64
	require.NoError(t, db.Raw(`SELECT id FROM users WHERE email = ?`, userEmail).Scan(&userID).Error)
	require.NotZero(t, userID)

	require.NoError(t, db.Exec(`
		INSERT INTO invitations (
			code,
			invitee_email,
			role,
			created_by,
			expires_at,
			revocation_reason,
			created_at
		) VALUES (?, ?, 'operator', ?, now() + interval '1 day', '', now())
	`, fmt.Sprintf("zz-access-%d", time.Now().UnixNano()), userEmail, userID).Error)

	invalidRoleErr := db.Exec(`
		INSERT INTO invitations (
			code,
			invitee_email,
			role,
			created_by,
			expires_at,
			revocation_reason,
			created_at
		) VALUES (?, ?, 'viewer', ?, now() + interval '1 day', '', now())
	`, fmt.Sprintf("zz-access-%d-invalid", time.Now().UnixNano()), userEmail, userID).Error
	require.Error(t, invalidRoleErr, "invitations.role check constraint must reject unsupported roles")
}
