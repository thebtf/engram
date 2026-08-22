package recoveryinventory

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

var scriptProjectKeyPattern = regexp.MustCompile(`(?:["']([A-Za-z][A-Za-z0-9_]*)["']|\b([A-Za-z][A-Za-z0-9_]*)\??)\s*:`)

// ScanProjectData inventories source-declared project-bearing data families.
// It never opens a database, cache, import, export, or job payload.
func ScanProjectData(root string) (Report, error) {
	report := newReport("project-bearing-data")
	files, err := sourceFiles(root, ".go", ".js", ".mjs", ".cjs", ".ts", ".tsx")
	if err != nil {
		return Report{}, err
	}
	for _, file := range files {
		if err := scanProjectDataFile(&report, file); err != nil {
			return Report{}, err
		}
	}
	report.finish()
	return report, nil
}

func scanProjectDataFile(report *Report, file sourceFile) error {
	source, err := os.ReadFile(file.absolute)
	if err != nil {
		return err
	}
	if filepath.Ext(file.relative) != ".go" {
		scanScriptProjectData(report, file, source)
		return nil
	}
	fset := token.NewFileSet()
	parsed, err := parser.ParseFile(fset, file.relative, source, 0)
	if err != nil {
		report.add(Record{Kind: "project-data-family", Path: file.relative, Classification: "source-uncertain"})
		return nil
	}

	found := false
	for _, declaration := range parsed.Decls {
		general, ok := declaration.(*ast.GenDecl)
		if !ok {
			continue
		}
		for _, spec := range general.Specs {
			typeSpec, ok := spec.(*ast.TypeSpec)
			if !ok {
				continue
			}
			structType, ok := typeSpec.Type.(*ast.StructType)
			if !ok {
				continue
			}
			for _, field := range structType.Fields.List {
				for _, name := range field.Names {
					if !projectBearingName(typeSpec.Name.Name) && !projectBearingName(name.Name) && !projectBearingTag(field.Tag) {
						continue
					}
					found = true
					report.add(Record{
						Kind:           "project-bearing-field",
						Path:           file.relative,
						Line:           fset.Position(field.Pos()).Line,
						Name:           redactedName(typeSpec.Name.Name + "." + name.Name),
						Classification: projectDataClassification(file.relative, typeSpec.Name.Name, field),
					})
				}
			}
		}
	}
	ast.Inspect(parsed, func(node ast.Node) bool {
		switch node := node.(type) {
		case *ast.CompositeLit:
			if _, ok := node.Type.(*ast.MapType); !ok {
				return true
			}
			for _, element := range node.Elts {
				entry, ok := element.(*ast.KeyValueExpr)
				if !ok {
					continue
				}
				key, ok := projectMapKey(entry.Key)
				if !ok {
					continue
				}
				found = true
				report.add(projectMapRecord(file.relative, fset.Position(entry.Key.Pos()).Line, key))
			}
		case *ast.IndexExpr:
			key, ok := projectMapKey(node.Index)
			if !ok {
				return true
			}
			found = true
			report.add(projectMapRecord(file.relative, fset.Position(node.Pos()).Line, key))
		}
		return true
	})
	if !found && strings.Contains(strings.ToLower(string(source)), "project") {
		report.add(Record{Kind: "project-data-family", Path: file.relative, Name: "unresolved-declaration", Classification: "source-uncertain"})
	}
	return nil
}

func scanScriptProjectData(report *Report, file sourceFile, source []byte) {
	found := false
	for index, line := range strings.Split(string(source), "\n") {
		for _, match := range scriptProjectKeyPattern.FindAllStringSubmatch(line, -1) {
			key := match[1]
			if key == "" {
				key = match[2]
			}
			if !projectBearingName(key) {
				continue
			}
			found = true
			report.add(scriptProjectRecord(file.relative, index+1, key))
		}
	}
	if !found && strings.Contains(strings.ToLower(string(source)), "project") {
		report.add(Record{Kind: "project-data-family", Path: file.relative, Name: "unresolved-declaration", Classification: "source-uncertain"})
	}
}

func projectMapKey(expression ast.Expr) (string, bool) {
	literal, ok := expression.(*ast.BasicLit)
	if !ok || literal.Kind != token.STRING {
		return "", false
	}
	key, err := strconv.Unquote(literal.Value)
	if err != nil || !projectBearingName(key) {
		return "", false
	}
	return key, true
}

func projectMapRecord(path string, line int, key string) Record {
	classification := projectDataPath(path)
	if classification == "" {
		classification = "serialized-map"
	}
	return Record{Kind: "project-bearing-map-key", Path: path, Line: line, Name: "map." + key, Classification: classification}
}

func scriptProjectRecord(path string, line int, key string) Record {
	classification := projectDataPath(path)
	if classification == "" {
		classification = "serialized-payload"
	}
	return Record{Kind: "project-bearing-payload-key", Path: path, Line: line, Name: "payload." + key, Classification: classification}
}

func projectBearingName(name string) bool {
	lower := strings.ToLower(name)
	return strings.Contains(lower, "project") || strings.Contains(lower, "tenant") || strings.Contains(lower, "workspace")
}

func projectBearingTag(tag *ast.BasicLit) bool {
	if tag == nil {
		return false
	}
	lower := strings.ToLower(tag.Value)
	return strings.Contains(lower, "project") || strings.Contains(lower, "tenant") || strings.Contains(lower, "workspace")
}

func projectDataClassification(path, typeName string, field *ast.Field) string {
	pathKind := projectDataPath(path + " " + typeName)
	if pathKind != "" {
		return pathKind
	}
	if field.Tag != nil {
		tag := strings.ToLower(field.Tag.Value)
		if strings.Contains(tag, "gorm:") {
			if len(field.Names) == 1 && strings.HasSuffix(strings.ToLower(field.Names[0].Name), "s") {
				return "relational-multi-role"
			}
			return "relational"
		}
		if strings.Contains(tag, "json:") || strings.Contains(tag, "yaml:") {
			return "serialized"
		}
	}
	return "source-uncertain"
}

func projectDataPath(value string) string {
	lower := strings.ToLower(filepath.ToSlash(value))
	switch {
	case strings.Contains(lower, "cache"):
		return "cache"
	case strings.Contains(lower, "job"), strings.Contains(lower, "queue"), strings.Contains(lower, "task"):
		return "job"
	case strings.Contains(lower, "import"), strings.Contains(lower, "export"):
		return "import-export"
	default:
		return ""
	}
}
