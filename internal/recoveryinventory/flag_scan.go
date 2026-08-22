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

var (
	jsEnvironmentPattern        = regexp.MustCompile(`process\.env(?:\.([A-Za-z_][A-Za-z0-9_]*)|\[\s*["']([A-Za-z_][A-Za-z0-9_]*)["']\s*\])`)
	jsDynamicEnvironmentPattern = regexp.MustCompile(`process\.env\s*\[\s*[^"'][^\]]*\]`)
	shellEnvironmentPattern     = regexp.MustCompile(`\$\{?([A-Z][A-Z0-9_]*)`)
	shellDeclarationPattern     = regexp.MustCompile(`^\s*(?:export\s+)?([A-Z][A-Z0-9_]*)=`)
	powerShellEnvironmentRegex  = regexp.MustCompile(`\$env:([A-Za-z_][A-Za-z0-9_]*)`)
)

// ScanFlags inventories environment readers and their source-declared parsing
// semantics. It never reads the process environment or reports effective state.
func ScanFlags(root string) (Report, error) {
	report := newReport("feature-flags")
	files, err := sourceFiles(root, ".go", ".js", ".mjs", ".cjs", ".ts", ".tsx", ".sh", ".ps1")
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
	if filepath.Ext(file.relative) != ".go" {
		scanScriptFlags(report, file, source)
		return nil
	}
	fset := token.NewFileSet()
	parsed, err := parser.ParseFile(fset, file.relative, source, 0)
	if err != nil {
		report.add(Record{Kind: "environment-reader", Path: file.relative, Classification: "source-uncertain"})
		return nil
	}
	declaredNames := declaredEnvironmentNames(parsed)
	lines := strings.Split(string(source), "\n")
	ast.Inspect(parsed, func(node ast.Node) bool {
		call, ok := node.(*ast.CallExpr)
		if !ok || !isEnvironmentRead(call) || len(call.Args) != 1 {
			return true
		}
		line := fset.Position(call.Pos()).Line
		parserKind, defaultKind := flagSemantics(lines, line)
		names := environmentReadNames(call.Args[0], declaredNames)
		if len(names) == 0 {
			addFlagRecord(report, file.relative, line, "source-dynamic", "environment-reader", "source-dynamic", "source-unspecified")
			return true
		}
		for _, name := range names {
			addFlagRecord(report, file.relative, line, name, "environment-reader", parserKind, defaultKind)
		}
		return true
	})
	return nil
}

func scanScriptFlags(report *Report, file sourceFile, source []byte) {
	extension := strings.ToLower(filepath.Ext(file.relative))
	for index, text := range strings.Split(string(source), "\n") {
		line := index + 1
		switch extension {
		case ".js", ".mjs", ".cjs", ".ts", ".tsx":
			for _, match := range jsEnvironmentPattern.FindAllStringSubmatch(text, -1) {
				name := match[1]
				if name == "" {
					name = match[2]
				}
				parserKind, defaultKind := scriptFlagSemantics(text)
				addFlagRecord(report, file.relative, line, name, "environment-reader", parserKind, defaultKind)
			}
			if jsDynamicEnvironmentPattern.MatchString(text) {
				parserKind, defaultKind := scriptFlagSemantics(text)
				addFlagRecord(report, file.relative, line, "source-dynamic", "environment-reader", parserKind, defaultKind)
			}
		case ".sh":
			for _, match := range shellEnvironmentPattern.FindAllStringSubmatch(text, -1) {
				parserKind, defaultKind := scriptFlagSemantics(text)
				addFlagRecord(report, file.relative, line, match[1], "environment-reader", parserKind, defaultKind)
			}
			if match := shellDeclarationPattern.FindStringSubmatch(text); match != nil {
				addFlagRecord(report, file.relative, line, match[1], "environment-default", "source-default-declaration", "source-declared-default")
			}
		case ".ps1":
			for _, match := range powerShellEnvironmentRegex.FindAllStringSubmatch(text, -1) {
				parserKind, defaultKind := scriptFlagSemantics(text)
				addFlagRecord(report, file.relative, line, match[1], "environment-reader", parserKind, defaultKind)
			}
		}
	}
}

func environmentReadNames(argument ast.Expr, declared map[string][]string) []string {
	if literal, ok := argument.(*ast.BasicLit); ok && literal.Kind == token.STRING {
		name, err := strconv.Unquote(literal.Value)
		if err == nil {
			return []string{name}
		}
	}
	call, ok := argument.(*ast.CallExpr)
	if !ok {
		return nil
	}
	function, ok := call.Fun.(*ast.Ident)
	if !ok {
		return nil
	}
	return declared[function.Name]
}

func declaredEnvironmentNames(file *ast.File) map[string][]string {
	declared := make(map[string][]string)
	for _, declaration := range file.Decls {
		function, ok := declaration.(*ast.FuncDecl)
		if !ok || function.Body == nil {
			continue
		}
		ast.Inspect(function.Body, func(node ast.Node) bool {
			result, ok := node.(*ast.ReturnStmt)
			if !ok || len(result.Results) != 1 {
				return true
			}
			literal, ok := result.Results[0].(*ast.BasicLit)
			if !ok || literal.Kind != token.STRING {
				return true
			}
			name, err := strconv.Unquote(literal.Value)
			if err == nil {
				declared[function.Name.Name] = append(declared[function.Name.Name], name)
			}
			return true
		})
	}
	return declared
}

func addFlagRecord(report *Report, path string, line int, name, kind, parserKind, defaultKind string) {
	classification := "configuration-reader"
	if strings.HasPrefix(name, "ENGRAM_") {
		classification = "feature-flag-reader"
	}
	report.add(Record{
		Kind:           kind,
		Path:           path,
		Line:           line,
		Name:           redactedName(name),
		Parser:         parserKind,
		Default:        defaultKind,
		Classification: classification,
	})
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

func scriptFlagSemantics(line string) (parserKind, defaultKind string) {
	switch {
	case strings.Contains(line, "??"), strings.Contains(line, "||"), strings.Contains(line, ":-"), strings.Contains(line, ":="):
		return "source-conditional-default", "source-declared-default"
	default:
		return "source-uncertain", "source-unspecified"
	}
}
