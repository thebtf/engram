package datamodel

import (
	"fmt"
	"path/filepath"
	"sort"
	"strings"

	"github.com/thebtf/engram/internal/db/gorm/migrationmeta"
)

const (
	BeginGeneratedTables = "<!-- BEGIN GENERATED TABLES -->"
	EndGeneratedTables   = "<!-- END GENERATED TABLES -->"
)

type Derivation struct {
	Block          string
	MigrationCount int
	LiveTableCount int
	Tables         []migrationmeta.TableInfo
}

func DeriveFromMigrationsFile(path string) (Derivation, error) {
	schema, err := migrationmeta.ParseFile(path)
	if err != nil {
		return Derivation{}, err
	}
	return DeriveFromSchema(schema), nil
}

func DeriveFromMigrationsPackage(path string) (Derivation, error) {
	schema, err := migrationmeta.ParsePackageDir(filepath.Dir(path))
	if err != nil {
		return Derivation{}, err
	}
	return DeriveFromSchema(schema), nil
}

func DeriveFromSchema(schema *migrationmeta.Schema) Derivation {
	tables := schema.LiveTables()
	sort.Slice(tables, func(i, j int) bool {
		if tables[i].CreatingMigrationNumericID == tables[j].CreatingMigrationNumericID {
			return tables[i].Name < tables[j].Name
		}
		return tables[i].CreatingMigrationNumericID < tables[j].CreatingMigrationNumericID
	})

	var b strings.Builder
	b.WriteString(BeginGeneratedTables)
	b.WriteString("\n")
	b.WriteString("Generated from the registered migration sources in `internal/db/gorm`.\n\n")
	fmt.Fprintf(&b, "Migration count: **%d**.\n\n", len(schema.Migrations))
	fmt.Fprintf(&b, "Live table count: **%d**.\n\n", len(tables))
	b.WriteString("| Table | Creating migration |\n")
	b.WriteString("| --- | --- |\n")
	for _, table := range tables {
		fmt.Fprintf(&b, "| `%s` | `%s` |\n", table.Name, table.CreatingMigrationID)
	}
	b.WriteString(EndGeneratedTables)
	b.WriteString("\n")

	return Derivation{
		Block:          b.String(),
		MigrationCount: len(schema.Migrations),
		LiveTableCount: len(tables),
		Tables:         tables,
	}
}

func ExtractGeneratedBlock(doc string) (string, bool) {
	normalized := normalizeNewlines(doc)
	begin := strings.Index(normalized, BeginGeneratedTables)
	if begin < 0 {
		return "", false
	}
	end := strings.Index(normalized[begin:], EndGeneratedTables)
	if end < 0 {
		return "", false
	}
	end += begin + len(EndGeneratedTables)
	if end < len(normalized) && normalized[end] == '\n' {
		end++
	}
	return normalized[begin:end], true
}

func SpliceGeneratedBlock(doc, block string) string {
	normalizedDoc := normalizeNewlines(doc)
	normalizedBlock := normalizeNewlines(block)
	begin := strings.Index(normalizedDoc, BeginGeneratedTables)
	if begin < 0 {
		return strings.TrimRight(normalizedDoc, "\n") + "\n\n" + normalizedBlock
	}
	end := strings.Index(normalizedDoc[begin:], EndGeneratedTables)
	if end < 0 {
		// Orphaned opening marker with no matching end: strip the dangling
		// BEGIN tag before appending, otherwise the appended block (which has
		// its own BEGIN) would duplicate the opening marker and corrupt the
		// doc structure (PR #271 review, gemini).
		docWithoutOrphan := normalizedDoc[:begin] + normalizedDoc[begin+len(BeginGeneratedTables):]
		return strings.TrimRight(docWithoutOrphan, "\n") + "\n\n" + normalizedBlock
	}
	end += begin + len(EndGeneratedTables)
	if end < len(normalizedDoc) && normalizedDoc[end] == '\n' {
		end++
	}
	return normalizedDoc[:begin] + normalizedBlock + normalizedDoc[end:]
}

func normalizeNewlines(s string) string {
	return strings.ReplaceAll(s, "\r\n", "\n")
}
