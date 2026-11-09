package migrationmeta

import (
	"os"
	"path/filepath"
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

func TestParseSourceGuardedSchemaQualifiedTables(t *testing.T) {
	src := []byte(`package gorm
var _ = []*Migration{{ID: "195_links", Migrate: func(tx *DB) error {
	var schema string
	links := schema + ".memory_links"
	receipts := schema + ".memory_link_receipts"
	statements := []string{
		"CREATE TABLE IF NOT EXISTS " + links + " " + definition,
		"CREATE TABLE IF NOT EXISTS " + receipts + " " + definition,
		` + "`CREATE TABLE IF NOT EXISTS \"public\".\"quoted_table\" (id INT)`" + `,
	}
	return nil
}}}`)
	schema, err := ParseSource("migrations.go", src)
	require.NoError(t, err)
	for _, name := range []string{"memory_links", "memory_link_receipts", "quoted_table"} {
		_, found := schema.Table(name)
		require.True(t, found, "missing table %s", name)
	}
	_, found := schema.Table("if")
	require.False(t, found, "incomplete DDL must not create a keyword table")
}

func TestExtractCreateTablesIncompleteGuardAndIdentifier(t *testing.T) {
	require.Empty(t, extractCreateTables("CREATE TABLE IF NOT EXISTS "))
	require.Equal(t, []string{"if"}, extractCreateTables("CREATE TABLE if (id INT)"))
	require.Equal(t, []string{"if"}, extractCreateTables(`CREATE TABLE IF NOT EXISTS "public"."if" (id INT)`))
	require.Equal(t, []string{"ordinary"}, extractCreateTables("CREATE TABLE IF NOT EXISTS ordinary (id INT)"))
}

func TestParseRegisteredFileRejectsUnknownMigrationIDs(t *testing.T) {
	for _, tc := range []struct {
		name        string
		declaration string
		id          string
	}{
		{"undefined", "", "missingID"},
		{"variable", `var migrationID = "001_variable"`, "migrationID"},
		{"nonliteral constant", `const migrationID = unknownID`, "migrationID"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "migrations.go")
			src := `package gorm
` + tc.declaration + `
func run() { gormigrate.New(db, options, []*gormigrate.Migration{migration()}) }
func migration() *gormigrate.Migration {
	return &gormigrate.Migration{ID: ` + tc.id + `, Migrate: func(tx *DB) error { return nil }}
}`
			require.NoError(t, os.WriteFile(path, []byte(src), 0o600))
			_, err := ParseRegisteredFile(path)
			require.ErrorContains(t, err, "migration literal missing")
		})
	}
}
