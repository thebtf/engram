package recoveryinventory

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"strconv"
	"strings"
)

// ScanFlags inventories environment readers and their source-declared parsing
// semantics. It never reads the process environment or reports effective state.
func ScanFlags(root string) (Report, error) {
	report := newReport("feature-flags")
	files, err := sourceFiles(root, ".go")
	if err != nil {
		return Report{}, err
	}
	for _, file := range files {
		if err := scanFlagFile(&report, file); err != nil {
			return Report{}, err
		}
	}
	report.finish()
	return report, nil
}

func scanFlagFile(report *Report, file sourceFile) error {
	source, err := os.ReadFile(file.absolute)
	if err != nil {
		return err
	}
	fset := token.NewFileSet()
	parsed, err := parser.ParseFile(fset, file.relative, source, 0)
	if err != nil {
		report.add(Record{Kind: "environment-reader", Path: file.relative, Classification: "source-uncertain"})
		return nil
	}
	lines := strings.Split(string(source), "\n")
	ast.Inspect(parsed, func(node ast.Node) bool {
		call, ok := node.(*ast.CallExpr)
		if !ok || !isEnvironmentRead(call) || len(call.Args) != 1 {
			return true
		}
		literal, ok := call.Args[0].(*ast.BasicLit)
		if !ok || literal.Kind != token.STRING {
			return true
		}
		name, err := strconv.Unquote(literal.Value)
		if err != nil {
			return true
		}
		line := fset.Position(call.Pos()).Line
		parserKind, defaultKind := flagSemantics(lines, line)
		classification := "configuration-reader"
		if strings.HasPrefix(name, "ENGRAM_") {
			classification = "feature-flag-reader"
		}
		report.add(Record{
			Kind:           "environment-reader",
			Path:           file.relative,
			Line:           line,
			Name:           redactedName(name),
			Parser:         parserKind,
			Default:        defaultKind,
			Classification: classification,
		})
		return true
	})
	return nil
}

func isEnvironmentRead(call *ast.CallExpr) bool {
	selector, ok := call.Fun.(*ast.SelectorExpr)
	if !ok || (selector.Sel.Name != "Getenv" && selector.Sel.Name != "LookupEnv") {
		return false
	}
	packageName, ok := selector.X.(*ast.Ident)
	return ok && packageName.Name == "os"
}

func flagSemantics(lines []string, line int) (parserKind, defaultKind string) {
	start, end := line-3, line+2
	if start < 0 {
		start = 0
	}
	if end > len(lines) {
		end = len(lines)
	}
	context := strings.Join(lines[start:end], "\n")
	switch {
	case strings.Contains(context, "strconv.ParseBool"):
		return "parse-bool", "source-parser-default"
	case strings.Contains(context, "parseBool("):
		return "boolean-helper", "source-helper-default"
	case strings.Contains(context, "strconv.Atoi") || strings.Contains(context, "strconv.ParseInt"):
		return "integer-parser", "source-parser-default"
	case strings.Contains(context, "== \"true\""):
		return "exact-lowercase-true", "false-unless-exact-true"
	case strings.Contains(context, "strings.TrimSpace"):
		return "trimmed-string", "source-unspecified"
	default:
		return "source-uncertain", "source-unspecified"
	}
}
