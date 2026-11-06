// Package sqlitetest supplies SQL lowering for PostgreSQL-on-SQLite test fixtures.
package sqlitetest

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"fmt"
	"strings"
)

// RedirectSchema is the subset used by the canonical project read predicate.
const RedirectSchema = `CREATE TABLE project_consolidation_redirects (selector TEXT NOT NULL, session_id TEXT NOT NULL, project_id TEXT NOT NULL, plan_fingerprint TEXT NOT NULL DEFAULT '', PRIMARY KEY(selector, session_id))`

// MembershipSQL lowers the UUID tests, text casts and singleton-id JSON
// comparisons used by these fixtures. Other regex operators remain errors.
func MembershipSQL(query string) (string, error) {
	if !strings.Contains(query, "project_consolidation_redirects") {
		return query, nil
	}
	pattern := ` ~ '^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$'`
	for _, column := range []string{"redirect.selector", "redirect.project_id"} {
		predicate := `(length(` + column + `) = 36 AND substr(` + column + `,9,1) = '-' AND substr(` + column + `,14,1) = '-' AND substr(` + column + `,19,1) = '-' AND substr(` + column + `,24,1) = '-' AND length(replace(` + column + `,'-','')) = 32 AND replace(` + column + `,'-','') NOT GLOB '*[^0-9a-f]*')`
		query = strings.ReplaceAll(query, column+pattern, predicate)
	}
	if strings.Contains(query, " ~ ") {
		return "", fmt.Errorf("SQLite membership fixture: unsupported regex operator")
	}
	query = strings.ReplaceAll(query, "::text", "")
	query = strings.ReplaceAll(query, "membership.field_path = ", "json(membership.field_path) = ")
	query = strings.ReplaceAll(query, "conflict.field_path = membership.field_path", "json(conflict.field_path) = json(membership.field_path)")
	query = strings.ReplaceAll(query, "conflict.row_key = membership.row_key", "json(conflict.row_key) = json(membership.row_key)")
	return query, nil
}

type (
	connector struct{ driver driver.Driver }
	conn      struct{ driver.Conn }
)

func (c connector) Connect(context.Context) (driver.Conn, error) {
	value, err := c.driver.Open(":memory:")
	if err != nil {
		return nil, err
	}
	return conn{value}, nil
}

func (c connector) Driver() driver.Driver { return c.driver }

func (c conn) Prepare(query string) (driver.Stmt, error) {
	query, err := MembershipSQL(query)
	if err != nil {
		return nil, err
	}
	return c.Conn.Prepare(query)
}

// OpenMemory wraps the supplied SQLite driver for a single isolated fixture.
func OpenMemory(d driver.Driver) *sql.DB { return sql.OpenDB(connector{driver: d}) }
