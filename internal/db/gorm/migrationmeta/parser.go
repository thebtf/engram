package migrationmeta

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"

	gormschema "gorm.io/gorm/schema"
)

type TableInfo struct {
	Name                       string
	CreatingMigrationID        string
	CreatingMigrationNumericID int
	CreateSQL                  string
	CreateLine                 int
	CreateSource               string
	Dropped                    bool
	DropMigrationID            string
	DropMigrationNumericID     int
	DropLine                   int
}

type MigrationInfo struct {
	ID        string
	NumericID int
	Line      int
}

type SQLStatement struct {
	MigrationID        string
	MigrationNumericID int
	Text               string
	Line               int
}

type CreateStatement struct {
	TableName          string
	MigrationID        string
	MigrationNumericID int
	SQL                string
	Line               int
	Source             string
}

type Schema struct {
	Tables           map[string]TableInfo
	Migrations       []MigrationInfo
	SQLStatements    []SQLStatement
	CreateStatements []CreateStatement
}

type ColumnDefinition struct {
	Name       string
	Definition string
}

var (
	migrationIDPattern  = regexp.MustCompile(`^(\d+)_`)
	// sqlCommentPattern strips line (--...) and block (/* ... */) SQL comments so
	// a commented-out CREATE/DROP TABLE is not parsed as live DDL (PR #271 review,
	// gemini). Applied to the raw SQL before table extraction.
	sqlCommentPattern   = regexp.MustCompile(`(?m)--.*$|/\*[\s\S]*?\*/`)
	createTablePattern  = regexp.MustCompile(`(?is)\bCREATE\s+TABLE\s+(?:IF\s+NOT\s+EXISTS\s+)?((?:"[^"]+"|[a-zA-Z_][\w$]*)(?:\.(?:"[^"]+"|[a-zA-Z_][\w$]*))?)`)
	dropTablePattern    = regexp.MustCompile(`(?is)\bDROP\s+TABLE\s+(?:IF\s+EXISTS\s+)?([^;]+)`)
	tableConstraintLead = regexp.MustCompile(`(?is)^(?:CONSTRAINT|PRIMARY|FOREIGN|UNIQUE|CHECK|EXCLUDE)\b`)
)

func ParseFile(path string) (*Schema, error) {
	src, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return ParseSource(path, src)
}

func ParseSource(filename string, src []byte) (*Schema, error) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, filename, src, parser.ParseComments)
	if err != nil {
		return nil, err
	}

	s := &Schema{Tables: make(map[string]TableInfo)}
	ast.Inspect(file, func(node ast.Node) bool {
		lit, ok := node.(*ast.CompositeLit)
		if !ok {
			return true
		}
		migration, migrateBody, ok := migrationLiteral(fset, lit)
		if !ok {
			return true
		}
		s.Migrations = append(s.Migrations, migration)
		s.processMigrateBody(fset, migration, migrateBody)
		return true
	})

	sort.SliceStable(s.Migrations, func(i, j int) bool {
		return s.Migrations[i].Line < s.Migrations[j].Line
	})
	return s, nil
}

func (s *Schema) LiveTables() []TableInfo {
	tables := make([]TableInfo, 0, len(s.Tables))
	for _, info := range s.Tables {
		if !info.Dropped {
			tables = append(tables, info)
		}
	}
	sort.Slice(tables, func(i, j int) bool {
		return tables[i].Name < tables[j].Name
	})
	return tables
}

func (s *Schema) Table(name string) (TableInfo, bool) {
	info, ok := s.Tables[NormalizeIdentifier(name)]
	return info, ok
}

func (s *Schema) processMigrateBody(fset *token.FileSet, migration MigrationInfo, body *ast.BlockStmt) {
	ast.Inspect(body, func(node ast.Node) bool {
		switch n := node.(type) {
		case *ast.BasicLit:
			if n.Kind != token.STRING {
				return true
			}
			text, err := strconv.Unquote(n.Value)
			if err != nil {
				return true
			}
			line := fset.Position(n.Pos()).Line
			s.recordSQL(migration, text, line)
		case *ast.CallExpr:
			switch selectorName(n.Fun) {
			case "AutoMigrate":
				for _, arg := range n.Args {
					if name := autoMigrateStructName(arg); name != "" {
						table := gormschema.NamingStrategy{}.TableName(name)
						s.recordCreate(migration, table, "", fset.Position(arg.Pos()).Line, "AutoMigrate(&"+name+"{})")
					}
				}
			case "DropTable":
				for _, arg := range n.Args {
					if table, ok := literalString(arg); ok {
						s.recordDrop(migration, table, fset.Position(arg.Pos()).Line)
					}
				}
			}
		}
		return true
	})
}

func (s *Schema) recordSQL(migration MigrationInfo, sql string, line int) {
	s.SQLStatements = append(s.SQLStatements, SQLStatement{
		MigrationID:        migration.ID,
		MigrationNumericID: migration.NumericID,
		Text:               sql,
		Line:               line,
	})
	cleanSQL := sqlCommentPattern.ReplaceAllString(sql, "")
	for _, table := range extractCreateTables(cleanSQL) {
		s.recordCreate(migration, table, cleanSQL, line, "CREATE TABLE")
	}
	for _, table := range extractDropTables(cleanSQL) {
		s.recordDrop(migration, table, line)
	}
}

func (s *Schema) recordCreate(migration MigrationInfo, tableName, sql string, line int, source string) {
	tableName = NormalizeIdentifier(tableName)
	if tableName == "" {
		return
	}
	s.CreateStatements = append(s.CreateStatements, CreateStatement{
		TableName:          tableName,
		MigrationID:        migration.ID,
		MigrationNumericID: migration.NumericID,
		SQL:                sql,
		Line:               line,
		Source:             source,
	})

	current, exists := s.Tables[tableName]
	if exists && !current.Dropped {
		if current.CreateSQL == "" && sql != "" {
			current.CreateSQL = sql
			current.CreateLine = line
			current.CreateSource = source
			s.Tables[tableName] = current
		}
		return
	}
	s.Tables[tableName] = TableInfo{
		Name:                       tableName,
		CreatingMigrationID:        migration.ID,
		CreatingMigrationNumericID: migration.NumericID,
		CreateSQL:                  sql,
		CreateLine:                 line,
		CreateSource:               source,
	}
}

func (s *Schema) recordDrop(migration MigrationInfo, tableName string, line int) {
	tableName = NormalizeIdentifier(tableName)
	if tableName == "" {
		return
	}
	current := s.Tables[tableName]
	current.Name = tableName
	current.Dropped = true
	current.DropMigrationID = migration.ID
	current.DropMigrationNumericID = migration.NumericID
	current.DropLine = line
	s.Tables[tableName] = current
}

func migrationLiteral(fset *token.FileSet, lit *ast.CompositeLit) (MigrationInfo, *ast.BlockStmt, bool) {
	var id string
	var migrate *ast.FuncLit
	for _, elt := range lit.Elts {
		kv, ok := elt.(*ast.KeyValueExpr)
		if !ok {
			continue
		}
		key, ok := kv.Key.(*ast.Ident)
		if !ok {
			continue
		}
		switch key.Name {
		case "ID":
			if parsed, ok := literalString(kv.Value); ok {
				id = parsed
			}
		case "Migrate":
			migrate, _ = kv.Value.(*ast.FuncLit)
		}
	}
	if id == "" || migrate == nil || migrate.Body == nil {
		return MigrationInfo{}, nil, false
	}
	matches := migrationIDPattern.FindStringSubmatch(id)
	if matches == nil {
		return MigrationInfo{}, nil, false
	}
	numericID, err := strconv.Atoi(matches[1])
	if err != nil {
		return MigrationInfo{}, nil, false
	}
	return MigrationInfo{
		ID:        id,
		NumericID: numericID,
		Line:      fset.Position(lit.Pos()).Line,
	}, migrate.Body, true
}

func selectorName(expr ast.Expr) string {
	selector, ok := expr.(*ast.SelectorExpr)
	if !ok {
		return ""
	}
	return selector.Sel.Name
}

func literalString(expr ast.Expr) (string, bool) {
	switch e := expr.(type) {
	case *ast.BasicLit:
		if e.Kind != token.STRING {
			return "", false
		}
		value, err := strconv.Unquote(e.Value)
		return value, err == nil
	case *ast.ParenExpr:
		return literalString(e.X)
	case *ast.BinaryExpr:
		if e.Op != token.ADD {
			return "", false
		}
		left, ok := literalString(e.X)
		if !ok {
			return "", false
		}
		right, ok := literalString(e.Y)
		if !ok {
			return "", false
		}
		return left + right, true
	default:
		return "", false
	}
}

func autoMigrateStructName(expr ast.Expr) string {
	if unary, ok := expr.(*ast.UnaryExpr); ok && unary.Op == token.AND {
		expr = unary.X
	}
	switch e := expr.(type) {
	case *ast.CompositeLit:
		return typeName(e.Type)
	default:
		return typeName(e)
	}
}

func typeName(expr ast.Expr) string {
	switch t := expr.(type) {
	case *ast.Ident:
		return t.Name
	case *ast.SelectorExpr:
		return t.Sel.Name
	case *ast.StarExpr:
		return typeName(t.X)
	default:
		return ""
	}
}

func extractCreateTables(sql string) []string {
	matches := createTablePattern.FindAllStringSubmatch(sql, -1)
	tables := make([]string, 0, len(matches))
	for _, match := range matches {
		if len(match) > 1 {
			if table := NormalizeIdentifier(match[1]); table != "" {
				tables = append(tables, table)
			}
		}
	}
	return tables
}

func extractDropTables(sql string) []string {
	matches := dropTablePattern.FindAllStringSubmatch(sql, -1)
	var tables []string
	for _, match := range matches {
		if len(match) < 2 {
			continue
		}
		for _, raw := range strings.Split(match[1], ",") {
			table := firstIdentifier(raw)
			if table != "" {
				tables = append(tables, table)
			}
		}
	}
	return tables
}

func firstIdentifier(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	fields := strings.Fields(raw)
	if len(fields) == 0 {
		return ""
	}
	if strings.EqualFold(fields[0], "ONLY") && len(fields) > 1 {
		return NormalizeIdentifier(fields[1])
	}
	return NormalizeIdentifier(fields[0])
}

func NormalizeIdentifier(name string) string {
	name = strings.TrimSpace(name)
	name = strings.Trim(name, "`")
	name = strings.Trim(name, `"`)
	name = strings.Trim(name, "'")
	name = strings.TrimSuffix(name, ";")
	name = strings.TrimSuffix(name, ",")
	name = strings.Trim(name, `"`)
	if idx := strings.LastIndex(name, "."); idx >= 0 {
		name = name[idx+1:]
		name = strings.Trim(name, `"`)
	}
	return strings.ToLower(name)
}

func ColumnDefinitions(createSQL string) ([]ColumnDefinition, error) {
	body, err := createTableBody(createSQL)
	if err != nil {
		return nil, err
	}
	parts := splitTopLevelComma(body)
	definitions := make([]ColumnDefinition, 0, len(parts))
	for _, part := range parts {
		def := strings.TrimSpace(part)
		if def == "" || tableConstraintLead.MatchString(def) {
			continue
		}
		name := NormalizeIdentifier(firstToken(def))
		if name == "" {
			continue
		}
		definitions = append(definitions, ColumnDefinition{Name: name, Definition: def})
	}
	return definitions, nil
}

func createTableBody(sql string) (string, error) {
	start := strings.Index(sql, "(")
	if start < 0 {
		return "", fmt.Errorf("CREATE TABLE has no column body")
	}
	// Track single-quoted string literals so parentheses inside a DEFAULT value
	// or comment string ('foo(bar)') do not corrupt the depth count (PR #271
	// review, gemini). SQL escapes a quote by doubling it ('').
	depth := 0
	inQuote := false
	for i := start; i < len(sql); i++ {
		c := sql[i]
		if c == '\'' {
			if i+1 < len(sql) && sql[i+1] == '\'' {
				i++ // skip the escaped quote pair
			} else {
				inQuote = !inQuote
			}
			continue
		}
		if inQuote {
			continue
		}
		switch c {
		case '(':
			depth++
		case ')':
			depth--
			if depth == 0 {
				return sql[start+1 : i], nil
			}
		}
	}
	return "", fmt.Errorf("CREATE TABLE column body is not balanced")
}

func splitTopLevelComma(body string) []string {
	var parts []string
	start := 0
	depth := 0
	inQuote := false
	bytes := []byte(body)
	for i := 0; i < len(bytes); i++ {
		c := bytes[i]
		if c == '\'' {
			if i+1 < len(bytes) && bytes[i+1] == '\'' {
				i++ // escaped quote pair
			} else {
				inQuote = !inQuote
			}
			continue
		}
		if inQuote {
			continue
		}
		switch c {
		case '(':
			depth++
		case ')':
			if depth > 0 {
				depth--
			}
		case ',':
			if depth == 0 {
				parts = append(parts, body[start:i])
				start = i + 1
			}
		}
	}
	parts = append(parts, body[start:])
	return parts
}

func firstToken(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	if strings.HasPrefix(s, `"`) {
		end := strings.Index(s[1:], `"`)
		if end >= 0 {
			return s[:end+2]
		}
	}
	fields := strings.Fields(s)
	if len(fields) == 0 {
		return ""
	}
	return fields[0]
}
