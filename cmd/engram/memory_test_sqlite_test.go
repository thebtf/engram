package main

import (
	"database/sql"
	"database/sql/driver"
	"regexp"
	"strings"
	"testing"

	"github.com/lib/pq"
	"github.com/thebtf/engram/internal/db/sqlitetest"
	"modernc.org/sqlite"
)

// Lower only PostgreSQL syntax, as in the worker mutation fixture. SQLite
// proves authorization and rollback, not PostgreSQL relation/row serialization.
type memoryTestSQLiteDriver struct{ driver.Driver }
type memoryTestSQLiteConn struct{ driver.Conn }

func (d memoryTestSQLiteDriver) Open(name string) (driver.Conn, error) {
	conn, err := d.Driver.Open(name)
	if err != nil {
		return nil, err
	}
	return memoryTestSQLiteConn{conn}, nil
}

var memoryTestSessionSQL = regexp.MustCompile(`(\$[0-9]+) = ANY\(m\.source_sessions\)`)

func (c memoryTestSQLiteConn) Prepare(query string) (driver.Stmt, error) {
	switch query {
	case `LOCK TABLE "audit_log" IN ROW EXCLUSIVE MODE`, `LOCK TABLE "memories" IN ROW SHARE MODE`, `LOCK TABLE "project_consolidation_memberships" IN SHARE MODE`:
		query = "SELECT 1"
	}
	query = strings.TrimSuffix(strings.TrimSuffix(query, " FOR UPDATE"), " FOR SHARE")
	query = memoryTestSessionSQL.ReplaceAllString(query, `memory_test_has_session(m.source_sessions, $1)`)
	query, err := sqlitetest.MembershipSQL(query)
	if err != nil {
		return nil, err
	}
	return c.Conn.Prepare(query)
}

func registerMemoryTestSQLite(t *testing.T) {
	t.Helper()
	registered, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	sql.Register("memory-test-sqlite", memoryTestSQLiteDriver{Driver: registered.Driver()})
	if err := registered.Close(); err != nil {
		t.Fatal(err)
	}
	if err := sqlite.RegisterScalarFunction("btrim", 1, func(_ *sqlite.FunctionContext, args []driver.Value) (driver.Value, error) {
		if args[0] == nil {
			return nil, nil
		}
		return strings.TrimSpace(args[0].(string)), nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := sqlite.RegisterScalarFunction("memory_test_has_session", 2, func(_ *sqlite.FunctionContext, args []driver.Value) (driver.Value, error) {
		var sessions pq.StringArray
		if err := sessions.Scan(args[0]); err != nil {
			return nil, err
		}
		for _, session := range sessions {
			if session == args[1] {
				return int64(1), nil
			}
		}
		return int64(0), nil
	}); err != nil {
		t.Fatal(err)
	}
}
