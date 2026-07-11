package graph

import (
	"errors"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/stretchr/testify/require"
)

// TestDangling_T016_ForeignKeyRejectsDanglingEdge verifies the live schema
// contract: a knowledge edge cannot be persisted after its target memory has
// been deleted. The ErrDangling sentinel remains covered separately for callers
// that resolve legacy/corrupt rows, but ordinary writes are protected by the FK.
//
// Anti-stub: dropping or weakening knowledge_edges_target_id_fkey allows the
// insert to succeed and fails both the SQLSTATE and zero-row assertions.
func TestDangling_T016_ForeignKeyRejectsDanglingEdge(t *testing.T) {
	db := openGraphTestDB(t)
	cleanupGraphFixture(t, db, "t016-test", "t016-test")
	t.Cleanup(func() {
		cleanupGraphFixture(t, db, "t016-test", "t016-test")
	})

	var deletedMemID int64
	require.NoError(t, db.Raw(
		`INSERT INTO memories (project, content) VALUES ('t016-test', 'dangling-target') RETURNING id`,
	).Row().Scan(&deletedMemID))

	var srcMemID int64
	require.NoError(t, db.Raw(
		`INSERT INTO memories (project, content) VALUES ('t016-test', 'dangling-source') RETURNING id`,
	).Row().Scan(&srcMemID))

	require.NoError(t, db.Exec(`DELETE FROM memories WHERE id = ?`, deletedMemID).Error)

	result := db.Exec(`
		INSERT INTO knowledge_edges
			(source_id, target_id, edge_type, weight, source_session_id, source_type, target_type)
		VALUES (?, ?, 'uses', 1.0, 't016-test', 'memory', 'memory')
	`, srcMemID, deletedMemID)
	require.Error(t, result.Error)

	var pgErr *pgconn.PgError
	require.ErrorAs(t, result.Error, &pgErr)
	require.Equal(t, "23503", pgErr.Code)
	require.Equal(t, "knowledge_edges_target_id_fkey", pgErr.ConstraintName)

	var edgeCount int64
	require.NoError(t, db.Raw(
		`SELECT count(*) FROM knowledge_edges WHERE source_session_id = 't016-test'`,
	).Row().Scan(&edgeCount))
	require.Zero(t, edgeCount, "rejected dangling insert must leave no edge row")
}

// TestDangling_T016_UnitShape verifies the ErrDangling sentinel shape without DB.
// Anti-stub: this test ensures ErrDangling exists and errors.Is works.
func TestDangling_T016_UnitShape(t *testing.T) {
	if ErrDangling == nil {
		t.Fatal("ErrDangling must be a non-nil sentinel")
	}

	// Wrapping in another error must still be detectable via errors.Is.
	wrapped := errors.Join(errors.New("outer"), ErrDangling)
	if !errors.Is(wrapped, ErrDangling) {
		t.Fatal("errors.Is(wrapped, ErrDangling) must return true")
	}

	// A different error must NOT match ErrDangling.
	other := errors.New("other error")
	if errors.Is(other, ErrDangling) {
		t.Fatal("different error must not match ErrDangling")
	}
}
