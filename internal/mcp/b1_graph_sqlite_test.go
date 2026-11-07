package mcp

import (
	"database/sql"
	"database/sql/driver"
	"regexp"
	"strings"
	"sync"
	"testing"

	"gorm.io/driver/postgres"
	gormlib "gorm.io/gorm"
	_ "modernc.org/sqlite"
)

type (
	graphFixtureDriver struct{ driver.Driver }
	graphFixtureConn   struct{ driver.Conn }
)

func (d graphFixtureDriver) Open(name string) (driver.Conn, error) {
	conn, err := d.Driver.Open(name)
	if err != nil {
		return nil, err
	}
	return graphFixtureConn{conn}, nil
}

var graphFixtureSession = regexp.MustCompile(`(\$[0-9]+) = ANY\(m\.source_sessions\)`)

func (c graphFixtureConn) Prepare(query string) (driver.Stmt, error) {
	query = graphFixtureSession.ReplaceAllString(query, `EXISTS (SELECT 1 FROM json_each(m.source_sessions) WHERE value = $1)`)
	query = strings.ReplaceAll(query, "btrim(", "trim(")
	return c.Conn.Prepare(query)
}

var graphFixtureOnce sync.Once

func graphFixtureDB(t *testing.T) *gormlib.DB {
	t.Helper()
	graphFixtureOnce.Do(func() {
		db, err := sql.Open("sqlite", ":memory:")
		if err != nil {
			t.Fatal(err)
		}
		sql.Register("b1-graph-fixture-sqlite", graphFixtureDriver{db.Driver()})
		if err := db.Close(); err != nil {
			t.Fatal(err)
		}
	})
	sqlDB, err := sql.Open("b1-graph-fixture-sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sqlDB.Close() })
	sqlDB.SetMaxOpenConns(1)
	db, err := gormlib.Open(postgres.New(postgres.Config{Conn: sqlDB}), &gormlib.Config{DisableAutomaticPing: true})
	if err != nil {
		t.Fatal(err)
	}
	for _, statement := range []string{
		`CREATE TABLE memories (id INTEGER PRIMARY KEY, project TEXT, content TEXT, privacy_scope TEXT, source_workstation_id TEXT, source_sessions TEXT DEFAULT '{}', deleted_at DATETIME, context_kind TEXT DEFAULT 'legacy', context_principal TEXT DEFAULT '', context_principal_kind TEXT DEFAULT '', agent_visibility TEXT DEFAULT '', owner_principal TEXT DEFAULT '', owner_principal_kind TEXT DEFAULT '', domain TEXT DEFAULT '')`,
		`CREATE TABLE memory_principals (principal_kind TEXT, principal TEXT)`,
	} {
		if err := db.Exec(statement).Error; err != nil {
			t.Fatal(err)
		}
	}
	return db
}
