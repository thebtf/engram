package migrationmeta

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// TestColumnDefinitions_StringLiteralRobustness locks the PR #271 hardening:
// parentheses and commas inside single-quoted SQL string literals must not
// confuse the column-body extractor or the top-level comma splitter.
func TestColumnDefinitions_StringLiteralRobustness(t *testing.T) {
	// A DEFAULT value containing both a paren and a comma inside a quoted
	// literal — the naive parser would split mid-literal or mis-balance parens.
	createSQL := `CREATE TABLE demo (
		id BIGSERIAL PRIMARY KEY,
		label TEXT NOT NULL DEFAULT 'a (tricky, value)',
		note TEXT DEFAULT '',
		owner_id BIGINT
	)`
	cols, err := ColumnDefinitions(createSQL)
	require.NoError(t, err)

	names := make([]string, 0, len(cols))
	for _, c := range cols {
		names = append(names, c.Name)
	}
	require.Equal(t, []string{"id", "label", "note", "owner_id"}, names,
		"quoted '(tricky, value)' must not split the column list")
}

// TestParseSource_IgnoresCommentedDDL locks the PR #271 hardening: a CREATE or
// DROP TABLE that is commented out (-- or /* */) inside a migration's SQL must
// NOT be parsed as live DDL.
func TestParseSource_IgnoresCommentedDDL(t *testing.T) {
	src := []byte("package gorm\n" +
		"var _ = []*Migration{\n" +
		"{\n" +
		"ID: \"001_demo\",\n" +
		"Migrate: func(tx *DB) error {\n" +
		"return tx.Exec(`\n" +
		"-- CREATE TABLE commented_line_table (id INT);\n" +
		"/* CREATE TABLE commented_block_table (id INT); */\n" +
		"CREATE TABLE real_table (id BIGSERIAL PRIMARY KEY);\n" +
		"`).Error\n" +
		"},\n" +
		"},\n" +
		"}\n")

	schema, err := ParseSource("migrations_test_input.go", src)
	require.NoError(t, err)

	_, realLive := schema.Table("real_table")
	require.True(t, realLive, "uncommented CREATE TABLE must be parsed live")
	_, lineCommented := schema.Table("commented_line_table")
	require.False(t, lineCommented, "-- commented CREATE TABLE must be ignored")
	_, blockCommented := schema.Table("commented_block_table")
	require.False(t, blockCommented, "/* */ commented CREATE TABLE must be ignored")
}
