package sqlitetest

import (
	"database/sql"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/thebtf/engram/internal/db/projectscope"
	_ "modernc.org/sqlite"
)

func TestMembershipSQLRejectsUnsupportedRegex(t *testing.T) {
	_, err := MembershipSQL("SELECT 1 FROM project_consolidation_redirects WHERE selector ~ 'unsupported'")
	require.ErrorContains(t, err, "unsupported regex")
}

func TestCanonicalReadTerminalSelfSQLite(t *testing.T) {
	registered, err := sql.Open("sqlite", ":memory:")
	require.NoError(t, err)
	db := OpenMemory(registered.Driver())
	require.NoError(t, registered.Close())
	t.Cleanup(func() { require.NoError(t, db.Close()) })
	db.SetMaxOpenConns(1)
	_, err = db.Exec(`CREATE TABLE projects (id TEXT PRIMARY KEY, removed_at TIMESTAMP, identity_status TEXT)`)
	require.NoError(t, err)
	_, err = db.Exec(RedirectSchema)
	require.NoError(t, err)
	const source = "11111111-1111-4111-8111-111111111111"
	const target = "22222222-2222-4222-8222-222222222222"
	const other = "33333333-3333-4333-8333-333333333333"
	for _, id := range []string{source, target, other} {
		_, err = db.Exec(`INSERT INTO projects (id, identity_status) VALUES (?, 'active')`, id)
		require.NoError(t, err)
	}
	_, err = db.Exec(`INSERT INTO project_consolidation_redirects(selector,session_id,project_id) VALUES (?, '', ?), (?, '', ?)`, source, target, target, target)
	require.NoError(t, err)
	resolve := func() string {
		t.Helper()
		var got string
		require.NoError(t, db.QueryRow(`SELECT `+projectscope.CanonicalReadProjectSQL("?"), source, source).Scan(&got))
		return got
	}
	require.Equal(t, target, resolve())
	_, err = db.Exec(`INSERT INTO project_consolidation_redirects(selector,session_id,project_id) VALUES (?, 'session', ?)`, target, target)
	require.NoError(t, err)
	require.Equal(t, source, resolve(), "session-scoped chains remain invalid")
	_, err = db.Exec(`DELETE FROM project_consolidation_redirects WHERE selector = ? AND session_id = 'session'`, target)
	require.NoError(t, err)
	_, err = db.Exec(`UPDATE project_consolidation_redirects SET project_id = ? WHERE selector = ? AND session_id = ''`, other, target)
	require.NoError(t, err)
	require.Equal(t, source, resolve(), "a real chain remains invalid")
}
