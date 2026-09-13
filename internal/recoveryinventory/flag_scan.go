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

const (
	environmentReaderKind               = "environment-reader"
	environmentDefaultEmptyUnset        = "empty-unset"
	environmentDefaultSourceExpression  = "source-default-expression"
	environmentDefaultSourceUnspecified = "source-unspecified"
)

// ScanFlags inventories source-declared environment readers and defaults. It
// never reads the process environment or reports effective runtime state.
func ScanFlags(root string) (Report, error) {
	report := newReport("feature-flags")
	files, err := sourceFiles(root, ".go", ".js", ".mjs", ".cjs", ".ts", ".tsx", ".sh", ".ps1", ".py")
	if err != nil {
		return Report{}, err
	}
	for _, file := range files {
		if isTestSource(file.relative) {
			continue
		}
		var err error
		switch strings.ToLower(filepath.Ext(file.relative)) {
		case ".go":
			err = scanFlagFile(&report, file)
		case ".js", ".mjs", ".cjs", ".ts", ".tsx":
			err = scanJavaScriptFlagFile(&report, file)
		case ".sh":
			err = scanShellFlagFile(&report, file)
		case ".ps1":
			err = scanPowerShellFlagFile(&report, file)
		case ".py":
			err = scanPythonFlagFile(&report, file)
		}
		if err != nil {
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
		report.add(Record{Kind: environmentReaderKind, Path: file.relative, Classification: classificationSourceUncertain})
		return nil
	}
	for _, read := range goEnvironmentReads(report, file.relative, fset, parsed) {
		parserKind, defaultKind := goEnvironmentSemantics(parsed, read.call)
		addEnvironmentReader(report, file.relative, read.line, read.name, parserKind, defaultKind)
	}
	return nil
}

type goEnvironmentRead struct {
	call *ast.CallExpr
	name string
	line int
}

func goEnvironmentReads(report *Report, path string, fset *token.FileSet, parsed *ast.File) []goEnvironmentRead {
	var reads []goEnvironmentRead
	ast.Inspect(parsed, func(node ast.Node) bool {
		call, ok := node.(*ast.CallExpr)
		if ok {
			appendGoEnvironmentRead(report, path, fset, call, &reads)
		}
		return true
	})
	return reads
}

func appendGoEnvironmentRead(report *Report, path string, fset *token.FileSet, call *ast.CallExpr, reads *[]goEnvironmentRead) {
	if !isEnvironmentRead(call) || len(call.Args) != 1 {
		return
	}
	line := fset.Position(call.Pos()).Line
	literal, ok := call.Args[0].(*ast.BasicLit)
	if !ok || literal.Kind != token.STRING {
		addUncertainEnvironmentReader(report, path, line)
		return
	}
	name, err := strconv.Unquote(literal.Value)
	if err != nil {
		addUncertainEnvironmentReader(report, path, line)
		return
	}
	*reads = append(*reads, goEnvironmentRead{call: call, name: name, line: line})
}

func isEnvironmentRead(call *ast.CallExpr) bool {
	selector, ok := call.Fun.(*ast.SelectorExpr)
	if !ok || (selector.Sel.Name != "Getenv" && selector.Sel.Name != "LookupEnv") {
		return false
	}
	packageName, ok := selector.X.(*ast.Ident)
	return ok && packageName.Name == "os"
}

var (
	javaScriptEnvironmentRead  = regexp.MustCompile(`\bprocess\.env((\.|\?\.)([A-Za-z_][A-Za-z0-9_]*)|(\?\.|\.)?\[\s*["']([A-Za-z_][A-Za-z0-9_]*)["']\s*\])`)
	javaScriptEnvironmentStart = regexp.MustCompile(`\bprocess\.env\b`)
	javaScriptBracketReader    = regexp.MustCompile(`process\.env(?:\?\.|\.)?\[\s*$`)
	powerShellEnvironmentRead  = regexp.MustCompile(`(?i)\$(?:env:([A-Za-z_][A-Za-z0-9_]*)|\{env:([A-Za-z_][A-Za-z0-9_]*)\})`)
	powerShellEnvironmentAPI   = regexp.MustCompile(`(?i)\[(?:System\.)?Environment\]\s*::\s*(GetEnvironmentVariables?)\s*\(`)
	pythonGetenvRead           = regexp.MustCompile(`\bos\.(getenv|environ\.get)\(\s*["']([A-Za-z_][A-Za-z0-9_]*)["'](\s*,\s*[^)]*)?\)`)
	pythonEnvironRead          = regexp.MustCompile(`\bos\.environ\s*\[\s*["']([A-Za-z_][A-Za-z0-9_]*)["']\s*\]`)
	pythonEnvironmentStart     = regexp.MustCompile(`\bos\.(?:getenv|environ\.get)\s*\(|\bos\.environ\s*\[`)
	pythonLiteralArgument      = regexp.MustCompile(`\bos\.(?:getenv|environ\.get)\s*\(\s*$|\bos\.environ\s*\[\s*$`)
	shellAssignment            = regexp.MustCompile(`^\s*(?:export\s+)?([A-Za-z_][A-Za-z0-9_]*)\s*=`)
)

func scanJavaScriptFlagFile(report *Report, file sourceFile) error {
	source, err := os.ReadFile(file.absolute)
	if err != nil {
		return err
	}
	var state javaScriptLexState
	for index, line := range strings.Split(string(source), "\n") {
		scanJavaScriptFlagLine(report, file.relative, line, index+1, &state)
	}
	return nil
}

func scanJavaScriptFlagLine(report *Report, path, line string, lineNumber int, state *javaScriptLexState) {
	code, templateUncertain := javaScriptCodeMask(line, state)
	if templateUncertain {
		addUncertainEnvironmentReader(report, path, lineNumber)
	}
	for _, start := range javaScriptEnvironmentStart.FindAllStringIndex(code, -1) {
		match := javaScriptEnvironmentRead.FindStringSubmatchIndex(code[start[0]:])
		if match == nil || match[0] != 0 {
			if !environmentAssignment(code, start[1]) {
				addUncertainEnvironmentReader(report, path, lineNumber)
			}
			continue
		}
		end := start[0] + match[1]
		if environmentAssignment(code, end) {
			continue
		}
		name := javaScriptEnvironmentName(line, start[0], match)
		if name == "" {
			addUncertainEnvironmentReader(report, path, lineNumber)
			continue
		}
		parserKind, defaultKind := javaScriptSemantics(code, line, start[0], end)
		addEnvironmentReader(report, path, lineNumber, name, parserKind, defaultKind)
	}
}

func javaScriptEnvironmentName(line string, offset int, match []int) string {
	if match[6] >= 0 {
		return capture(line, offset+match[6], offset+match[7])
	}
	if match[10] >= 0 {
		return capture(line, offset+match[10], offset+match[11])
	}
	return ""
}

func scanShellFlagFile(report *Report, file sourceFile) error {
	source, err := os.ReadFile(file.absolute)
	if err != nil {
		return err
	}
	declared := make(map[string]bool)
	var state shellLexState
	for index, line := range strings.Split(string(source), "\n") {
		scanShellFlagLine(report, file.relative, line, index+1, &state, declared)
	}
	return nil
}

func scanShellFlagLine(report *Report, path, line string, lineNumber int, state *shellLexState, declared map[string]bool) {
	code, unsupportedHereDoc := shellCodeMaskWithState(line, state)
	if unsupportedHereDoc {
		addUncertainEnvironmentReader(report, path, lineNumber)
	}
	for offset := range len(code) {
		if code[offset] != '$' || shellEscaped(code, offset) {
			continue
		}
		name, defaultKind, ok := shellEnvironmentRead(code, offset)
		if !ok || declared[name] {
			continue
		}
		addEnvironmentReader(report, path, lineNumber, name, "shell-parameter-expansion", defaultKind)
	}
	if match := shellAssignment.FindStringSubmatch(code); match != nil {
		declared[match[1]] = true
	}
}

func scanPowerShellFlagFile(report *Report, file sourceFile) error {
	source, err := os.ReadFile(file.absolute)
	if err != nil {
		return err
	}
	var state powerShellLexState
	for index, line := range strings.Split(string(source), "\n") {
		code := powerShellCodeMaskWithState(line, &state)
		scanPowerShellStringReaders(report, file.relative, line, code, index+1)
		scanPowerShellAPIReaders(report, file.relative, line, code, index+1)
	}
	return nil
}

func scanPowerShellStringReaders(report *Report, path, line, code string, lineNumber int) {
	for _, match := range powerShellEnvironmentRead.FindAllStringSubmatchIndex(code, -1) {
		if environmentAssignment(code, match[1]) {
			continue
		}
		name := powerShellEnvironmentName(line, match)
		if name == "" {
			addUncertainEnvironmentReader(report, path, lineNumber)
			continue
		}
		defaultKind := environmentDefaultEmptyUnset
		if strings.HasPrefix(strings.TrimSpace(code[match[1]:]), "??") {
			defaultKind = environmentDefaultSourceExpression
		}
		addEnvironmentReader(report, path, lineNumber, name, "powershell-string", defaultKind)
	}
}

func powerShellEnvironmentName(line string, match []int) string {
	name := capture(line, match[2], match[3])
	if name == "" {
		return capture(line, match[4], match[5])
	}
	return name
}

func scanPowerShellAPIReaders(report *Report, path, line, code string, lineNumber int) {
	for _, match := range powerShellEnvironmentAPI.FindAllStringSubmatchIndex(code, -1) {
		name, literal := powerShellEnvironmentAPILiteral(line, match[1])
		if strings.EqualFold(capture(line, match[2], match[3]), "GetEnvironmentVariables") || !literal {
			addUncertainEnvironmentReader(report, path, lineNumber)
			continue
		}
		addEnvironmentReader(report, path, lineNumber, name, "powershell-environment-api", environmentDefaultEmptyUnset)
	}
}

func scanPythonFlagFile(report *Report, file sourceFile) error {
	source, err := os.ReadFile(file.absolute)
	if err != nil {
		return err
	}
	var state pythonLexState
	for index, line := range strings.Split(string(source), "\n") {
		code := pythonCodeMask(line, &state)
		for _, start := range pythonEnvironmentStart.FindAllStringIndex(code, -1) {
			if match := pythonGetenvRead.FindStringSubmatchIndex(code[start[0]:]); match != nil && match[0] == 0 {
				defaultKind := environmentDefaultEmptyUnset
				if match[6] >= 0 {
					defaultKind = environmentDefaultSourceExpression
				}
				addEnvironmentReader(report, file.relative, index+1, capture(line, start[0]+match[4], start[0]+match[5]), "python-string", defaultKind)
				continue
			}
			if match := pythonEnvironRead.FindStringSubmatchIndex(code[start[0]:]); match != nil && match[0] == 0 {
				addEnvironmentReader(report, file.relative, index+1, capture(line, start[0]+match[2], start[0]+match[3]), "python-string", environmentDefaultEmptyUnset)
				continue
			}
			addUncertainEnvironmentReader(report, file.relative, index+1)
		}
	}
	return nil
}

func environmentAssignment(line string, end int) bool {
	remainder := strings.TrimSpace(line[end:])
	return (strings.HasPrefix(remainder, "=") && !strings.HasPrefix(remainder, "==") && !strings.HasPrefix(remainder, "=>")) || strings.HasPrefix(remainder, "||=") || strings.HasPrefix(remainder, "??=")
}

func javaScriptSemantics(code, source string, start, end int) (parserKind, defaultKind string) {
	prefix := strings.TrimSpace(code[:start])
	switch {
	case strings.HasSuffix(prefix, "parseInt(") || strings.HasSuffix(prefix, "Number("):
		parserKind = "integer-parser"
	case strings.HasSuffix(prefix, "parseFloat("):
		parserKind = "float-parser"
	case javaScriptExactComparison(code, source, end, "true"):
		parserKind = "exact-lowercase-true"
	case strings.HasSuffix(prefix, "Boolean("):
		parserKind = "truthy-string"
	default:
		parserKind = "source-string"
	}
	remainder := strings.TrimSpace(code[end:])
	if strings.HasPrefix(remainder, "||") || strings.HasPrefix(remainder, "??") || strings.HasPrefix(remainder, ".trim() ||") || strings.HasPrefix(remainder, ".trim() ??") || strings.HasPrefix(remainder, "?.trim() ||") || strings.HasPrefix(remainder, "?.trim() ??") {
		return parserKind, environmentDefaultSourceExpression
	}
	return parserKind, environmentDefaultEmptyUnset
}

func addEnvironmentReader(report *Report, path string, line int, name, parserKind, defaultKind string) {
	report.add(Record{
		Kind:           environmentReaderKind,
		Path:           path,
		Line:           line,
		Name:           redactedName(name),
		Parser:         parserKind,
		Default:        defaultKind,
		Classification: environmentClassification(name),
	})
}

func addUncertainEnvironmentReader(report *Report, path string, line int) {
	report.add(Record{Kind: environmentReaderKind, Path: path, Line: line, Parser: classificationSourceUncertain, Default: environmentDefaultSourceUnspecified, Classification: classificationSourceUncertain})
}

func environmentClassification(name string) string {
	if sensitiveEnvironmentName(name) {
		return "credential-reader"
	}
	upper := strings.ToUpper(name)
	for _, marker := range []string{"AUTH", "DATABASE", "DSN", "URL", "HOST", "PORT", "PATH", "DIR", "LOG", "CONTEXT", "LIMIT", "INTERVAL", "CONN", "CONFIG", "DATA", "TIMEOUT"} {
		if strings.Contains(upper, marker) {
			return "configuration-reader"
		}
	}
	if strings.HasSuffix(upper, "_ENABLED") || strings.Contains(upper, "_FEATURE") || strings.Contains(upper, "_FLAG") || strings.Contains(upper, "_EXPERIMENT") || strings.Contains(upper, "_BETA") || strings.Contains(upper, "_VNEXT") {
		return "feature-flag-reader"
	}
	return "configuration-reader"
}

// isTestSource identifies test-only and fixture-data paths excluded from runtime inventories.
func isTestSource(path string) bool {
	lower := strings.ToLower(filepath.ToSlash(path))
	for _, part := range strings.Split(lower, "/") {
		switch part {
		case "test", "tests", "__tests__", "testdata", "fixtures", "moduletest":
			return true
		}
	}
	base := filepath.Base(lower)
	return strings.Contains(base, "_test.") || strings.HasSuffix(base, "_test") || strings.Contains(base, ".test.") || strings.Contains(base, ".spec.") || strings.HasSuffix(base, ".tests.ps1") || strings.HasPrefix(base, "test_") || strings.HasPrefix(base, "playwright.config.")
}

func capture(value string, start, end int) string {
	if start < 0 || end < 0 {
		return ""
	}
	return value[start:end]
}

func goEnvironmentSemantics(file *ast.File, environmentCall *ast.CallExpr) (string, string) {
	state := goEnvironmentSemanticState{aliases: goEnvironmentAliases(file, environmentCall), environmentCall: environmentCall, booleanDescendants: make(map[*ast.BinaryExpr]bool)}
	ast.Inspect(file, state.inspect)
	return state.result()
}

type goEnvironmentSemanticState struct {
	aliases            map[*ast.Object]bool
	environmentCall    *ast.CallExpr
	parserKind         string
	parserConflict     bool
	booleanKind        string
	booleanDefault     string
	booleanDescendants map[*ast.BinaryExpr]bool
	booleanConflict    bool
	trimmed            bool
}

func (state *goEnvironmentSemanticState) inspect(node ast.Node) bool {
	switch node := node.(type) {
	case *ast.CallExpr:
		state.noteCall(node)
	case *ast.BinaryExpr:
		if !state.booleanDescendants[node] {
			state.noteBoolean(node)
		}
	}
	return true
}

func (state *goEnvironmentSemanticState) noteCall(call *ast.CallExpr) {
	usesEnvironment := goCallUsesEnvironmentValue(call, state.aliases, state.environmentCall)
	if usesEnvironment && goCallName(call) == "strings.TrimSpace" {
		state.trimmed = true
	}
	kind := goParserKind(call)
	if kind == "" || len(call.Args) == 0 || !usesEnvironment {
		return
	}
	if call.Pos() > state.environmentCall.Pos() || goExpressionContainsCall(call.Args[0], state.environmentCall) {
		state.noteParser(kind)
	}
}

func goParserKind(call *ast.CallExpr) string {
	switch goCallName(call) {
	case "strconv.Atoi", "strconv.ParseInt", "strconv.ParseUint":
		return "integer-parser"
	case "strconv.ParseBool":
		return "parse-bool"
	default:
		return ""
	}
}

func (state *goEnvironmentSemanticState) noteParser(kind string) {
	if state.parserKind == "" {
		state.parserKind = kind
	} else if state.parserKind != kind {
		state.parserConflict = true
	}
}

func (state *goEnvironmentSemanticState) noteBoolean(expression *ast.BinaryExpr) {
	if expression.Op != token.LOR && expression.Op != token.EQL {
		return
	}
	kind, defaultKind, ok := goBooleanExpressionSemantics(expression, state.aliases, state.environmentCall)
	if !ok {
		return
	}
	state.markBooleanDescendants(expression)
	if state.booleanKind == "" {
		state.booleanKind, state.booleanDefault = kind, defaultKind
	} else if state.booleanKind != kind {
		state.booleanConflict = true
	}
}

func (state *goEnvironmentSemanticState) markBooleanDescendants(expression *ast.BinaryExpr) {
	ast.Inspect(expression, func(node ast.Node) bool {
		if descendant, ok := node.(*ast.BinaryExpr); ok && descendant != expression {
			state.booleanDescendants[descendant] = true
		}
		return true
	})
}

func (state *goEnvironmentSemanticState) result() (string, string) {
	if state.parserConflict || state.booleanConflict {
		return classificationSourceUncertain, environmentDefaultSourceUnspecified
	}
	if state.parserKind != "" {
		return state.parserKind, "source-parser-default"
	}
	if state.booleanKind != "" {
		return state.booleanKind, state.booleanDefault
	}
	if state.trimmed {
		return "trimmed-string", environmentDefaultSourceUnspecified
	}
	return classificationSourceUncertain, environmentDefaultSourceUnspecified
}

func goEnvironmentAliases(file *ast.File, environmentCall *ast.CallExpr) map[*ast.Object]bool {
	aliases := make(map[*ast.Object]bool)
	changed := true
	for changed {
		changed = false
		ast.Inspect(file, func(node ast.Node) bool {
			switch node := node.(type) {
			case *ast.AssignStmt:
				for index, value := range node.Rhs {
					if index >= len(node.Lhs) || !goAliasExpression(value, aliases, environmentCall) {
						continue
					}
					if name, ok := node.Lhs[index].(*ast.Ident); ok && name.Obj != nil && !aliases[name.Obj] {
						aliases[name.Obj] = true
						changed = true
					}
				}
			case *ast.ValueSpec:
				for index, value := range node.Values {
					if index >= len(node.Names) || !goAliasExpression(value, aliases, environmentCall) {
						continue
					}
					if name := node.Names[index]; name.Obj != nil && !aliases[name.Obj] {
						aliases[name.Obj] = true
						changed = true
					}
				}
			}
			return true
		})
	}
	return aliases
}

func goAliasExpression(expression ast.Expr, aliases map[*ast.Object]bool, environmentCall *ast.CallExpr) bool {
	switch expression := expression.(type) {
	case *ast.Ident:
		return aliases[expression.Obj]
	case *ast.ParenExpr:
		return goAliasExpression(expression.X, aliases, environmentCall)
	case *ast.CallExpr:
		return expression == environmentCall || (goCallName(expression) == "strings.TrimSpace" && len(expression.Args) == 1 && goAliasExpression(expression.Args[0], aliases, environmentCall))
	}
	return false
}

func goCallUsesEnvironmentValue(call *ast.CallExpr, aliases map[*ast.Object]bool, environmentCall *ast.CallExpr) bool {
	for _, argument := range call.Args {
		if goExpressionUsesEnvironmentValue(argument, aliases, environmentCall) {
			return true
		}
	}
	return false
}

func goExpressionUsesEnvironmentValue(expression ast.Expr, aliases map[*ast.Object]bool, environmentCall *ast.CallExpr) bool {
	found := false
	ast.Inspect(expression, func(node ast.Node) bool {
		if node == environmentCall {
			found = true
			return false
		}
		if name, ok := node.(*ast.Ident); ok && aliases[name.Obj] {
			found = true
			return false
		}
		return !found
	})
	return found
}

func goExpressionContainsCall(expression ast.Expr, call *ast.CallExpr) bool {
	found := false
	ast.Inspect(expression, func(node ast.Node) bool {
		if node == call {
			found = true
			return false
		}
		return !found
	})
	return found
}

func goCallName(call *ast.CallExpr) string {
	selector, ok := call.Fun.(*ast.SelectorExpr)
	if !ok {
		return ""
	}
	packageName, ok := selector.X.(*ast.Ident)
	if !ok {
		return ""
	}
	return packageName.Name + "." + selector.Sel.Name
}

func goBooleanExpressionSemantics(expression ast.Expr, aliases map[*ast.Object]bool, environmentCall *ast.CallExpr) (string, string, bool) {
	values, ok := goBooleanValues(expression, aliases, environmentCall)
	if !ok {
		return "", "", false
	}
	switch {
	case len(values) == 1 && values["true"]:
		return "exact-lowercase-true", "false-unless-exact-true", true
	case len(values) == 2 && values["true"] && values["1"]:
		return "exact-true-or-one", "false-unless-true-or-one", true
	case len(values) == 1 && values["false"]:
		return "exact-lowercase-false", environmentDefaultSourceUnspecified, true
	case len(values) == 2 && values["false"] && values["0"]:
		return "exact-false-or-zero", environmentDefaultSourceUnspecified, true
	default:
		return "", "", false
	}
}

func goBooleanValues(expression ast.Expr, aliases map[*ast.Object]bool, environmentCall *ast.CallExpr) (map[string]bool, bool) {
	if parenthesized, ok := expression.(*ast.ParenExpr); ok {
		return goBooleanValues(parenthesized.X, aliases, environmentCall)
	}
	binary, ok := expression.(*ast.BinaryExpr)
	if !ok {
		return nil, false
	}
	switch binary.Op {
	case token.LOR:
		return mergeGoBooleanValues(binary, aliases, environmentCall)
	case token.EQL:
		return goBooleanEqualityValues(binary, aliases, environmentCall)
	default:
		return nil, false
	}
}

func mergeGoBooleanValues(binary *ast.BinaryExpr, aliases map[*ast.Object]bool, environmentCall *ast.CallExpr) (map[string]bool, bool) {
	left, leftOK := goBooleanValues(binary.X, aliases, environmentCall)
	right, rightOK := goBooleanValues(binary.Y, aliases, environmentCall)
	if !leftOK || !rightOK {
		return nil, false
	}
	for value := range right {
		left[value] = true
	}
	return left, true
}

func goBooleanEqualityValues(binary *ast.BinaryExpr, aliases map[*ast.Object]bool, environmentCall *ast.CallExpr) (map[string]bool, bool) {
	if goExpressionUsesEnvironmentValue(binary.X, aliases, environmentCall) {
		return goBooleanLiteralValue(binary.Y)
	}
	if goExpressionUsesEnvironmentValue(binary.Y, aliases, environmentCall) {
		return goBooleanLiteralValue(binary.X)
	}
	return nil, false
}

func goBooleanLiteralValue(expression ast.Expr) (map[string]bool, bool) {
	value, ok := goStringLiteral(expression)
	if !ok {
		return nil, false
	}
	return map[string]bool{value: true}, true
}

func goStringLiteral(expression ast.Expr) (string, bool) {
	literal, ok := expression.(*ast.BasicLit)
	if !ok || literal.Kind != token.STRING {
		return "", false
	}
	value, err := strconv.Unquote(literal.Value)
	return value, err == nil
}

type pythonLexState struct {
	quote       byte
	tripleQuote byte
}

func pythonCodeMask(line string, state *pythonLexState) string {
	masked := []byte(line)
	for index := 0; index < len(masked); {
		var done bool
		switch {
		case state.tripleQuote != 0:
			index, done = state.maskPythonTripleString(line, masked, index)
		case state.quote != 0:
			index, done = state.maskPythonContinuedString(line, masked, index)
		default:
			index, done = state.maskPythonByte(line, masked, index)
		}
		if done {
			return string(masked)
		}
	}
	return string(masked)
}

func (state *pythonLexState) maskPythonTripleString(line string, masked []byte, index int) (int, bool) {
	end, closed := pythonTripleStringEnd(line, index, state.tripleQuote)
	blankBytes(masked, index, end)
	if !closed {
		return end, true
	}
	state.tripleQuote = 0
	return end, false
}

func (state *pythonLexState) maskPythonContinuedString(line string, masked []byte, index int) (int, bool) {
	end, closed := quotedStringEnd(line, index, state.quote)
	blankBytes(masked, index, end)
	if !closed {
		if !pythonContinuesString(line) {
			state.quote = 0
		}
		return end, true
	}
	state.quote = 0
	return end, false
}

func (state *pythonLexState) maskPythonByte(line string, masked []byte, index int) (int, bool) {
	switch masked[index] {
	case '#':
		blankBytes(masked, index, len(masked))
		return len(masked), true
	case '\'', '"':
		return state.maskPythonLiteral(line, masked, index)
	default:
		return index + 1, false
	}
}

func (state *pythonLexState) maskPythonLiteral(line string, masked []byte, index int) (int, bool) {
	quote := masked[index]
	if index+2 < len(masked) && masked[index+1] == quote && masked[index+2] == quote {
		end, closed := pythonTripleStringEnd(line, index+3, quote)
		blankBytes(masked, index, end)
		if !closed {
			state.tripleQuote = quote
			return end, true
		}
		return end, false
	}
	end, closed := quotedStringEnd(line, index+1, quote)
	if !pythonLiteralArgument.Match(masked[:index]) {
		blankBytes(masked, index, end)
	}
	if !closed {
		if pythonContinuesString(line) {
			state.quote = quote
		}
		return end, true
	}
	return end, false
}

func pythonTripleStringEnd(line string, start int, quote byte) (int, bool) {
	for index := start; index+2 < len(line); index++ {
		if line[index] == '\\' {
			index++
			continue
		}
		if line[index] == quote && line[index+1] == quote && line[index+2] == quote {
			return index + 3, true
		}
	}
	return len(line), false
}

func quotedStringEnd(line string, start int, quote byte) (int, bool) {
	for index := start; index < len(line); index++ {
		if line[index] == '\\' {
			index++
			continue
		}
		if line[index] == quote {
			return index + 1, true
		}
	}
	return len(line), false
}

func pythonContinuesString(line string) bool {
	backslashes := 0
	for index := len(line) - 1; index >= 0 && line[index] == '\\'; index-- {
		backslashes++
	}
	return backslashes%2 == 1
}

type javaScriptLexState struct {
	blockComment      bool
	quote             byte
	templateLiteral   bool
	templateUncertain bool
}

func javaScriptCodeMask(line string, state *javaScriptLexState) (string, bool) {
	masked := []byte(line)
	sourceUncertain := false
	for index := 0; index < len(masked); {
		var done, uncertain bool
		switch {
		case state.quote != 0:
			index, done = state.maskJavaScriptContinuedString(line, masked, index)
		case state.templateLiteral:
			index, done, uncertain = state.maskJavaScriptTemplate(line, masked, index)
		case state.blockComment:
			index, done = state.maskJavaScriptBlockComment(line, masked, index)
		default:
			index, done, uncertain = state.maskJavaScriptByte(line, masked, index)
		}
		sourceUncertain = sourceUncertain || uncertain
		if done {
			return string(masked), sourceUncertain
		}
	}
	return string(masked), sourceUncertain
}

func (state *javaScriptLexState) maskJavaScriptContinuedString(line string, masked []byte, index int) (int, bool) {
	end, closed := quotedStringEnd(line, index, state.quote)
	blankBytes(masked, index, end)
	if !closed {
		if !javaScriptContinuesString(line) {
			state.quote = 0
		}
		return end, true
	}
	state.quote = 0
	return end, false
}

func (state *javaScriptLexState) maskJavaScriptTemplate(line string, masked []byte, index int) (int, bool, bool) {
	end, closed, interpolated := javaScriptTemplateEnd(line, index)
	blankBytes(masked, index, end)
	uncertain := interpolated && !state.templateUncertain
	if uncertain {
		state.templateUncertain = true
	}
	if !closed {
		return end, true, uncertain
	}
	state.templateLiteral = false
	state.templateUncertain = false
	return end, false, uncertain
}

func (state *javaScriptLexState) maskJavaScriptBlockComment(line string, masked []byte, index int) (int, bool) {
	end := strings.Index(line[index:], "*/")
	if end < 0 {
		blankBytes(masked, index, len(masked))
		return len(masked), true
	}
	end += index + len("*/")
	blankBytes(masked, index, end)
	state.blockComment = false
	return end, false
}

func (state *javaScriptLexState) maskJavaScriptByte(line string, masked []byte, index int) (int, bool, bool) {
	switch masked[index] {
	case '/':
		return state.maskJavaScriptSlash(line, masked, index)
	case '\'', '"':
		return state.maskJavaScriptLiteral(line, masked, index)
	case '`':
		return state.maskJavaScriptTemplateStart(line, masked, index)
	default:
		return index + 1, false, false
	}
}

func (state *javaScriptLexState) maskJavaScriptSlash(line string, masked []byte, index int) (int, bool, bool) {
	if index+1 >= len(masked) || masked[index+1] != '/' && masked[index+1] != '*' {
		return index + 1, false, false
	}
	if masked[index+1] == '/' {
		blankBytes(masked, index, len(masked))
		return len(masked), true, false
	}
	end := strings.Index(line[index+2:], "*/")
	if end < 0 {
		blankBytes(masked, index, len(masked))
		state.blockComment = true
		return len(masked), true, false
	}
	end += index + 4
	blankBytes(masked, index, end)
	return end, false, false
}

func (state *javaScriptLexState) maskJavaScriptLiteral(line string, masked []byte, index int) (int, bool, bool) {
	quote := masked[index]
	end, closed := quotedStringEnd(line, index+1, quote)
	if !javaScriptBracketReader.Match(masked[:index]) {
		blankBytes(masked, index, end)
	}
	if !closed {
		if javaScriptContinuesString(line) {
			state.quote = quote
		}
		return end, true, false
	}
	return end, false, false
}

func (state *javaScriptLexState) maskJavaScriptTemplateStart(line string, masked []byte, index int) (int, bool, bool) {
	end, closed, interpolated := javaScriptTemplateEnd(line, index+1)
	blankBytes(masked, index, end)
	if !closed {
		state.templateLiteral = true
		state.templateUncertain = interpolated
		return end, true, interpolated
	}
	return end, false, interpolated
}

func javaScriptTemplateEnd(line string, start int) (end int, closed, interpolated bool) {
	for index := start; index < len(line); index++ {
		if line[index] == '\\' {
			index++
			continue
		}
		if line[index] == '$' && index+1 < len(line) && line[index+1] == '{' {
			interpolated = true
			index++
			continue
		}
		if line[index] == '`' {
			return index + 1, true, interpolated
		}
	}
	return len(line), false, interpolated
}

func javaScriptStringEnd(line string, start int) int {
	end, _ := quotedStringEnd(line, start+1, line[start])
	return end
}

func javaScriptContinuesString(line string) bool {
	end := len(line)
	if end > 0 && line[end-1] == '\r' {
		end--
	}
	backslashes := 0
	for index := end - 1; index >= 0 && line[index] == '\\'; index-- {
		backslashes++
	}
	return backslashes%2 == 1
}

func javaScriptExactComparison(code, source string, end int, want string) bool {
	remainder := code[end:]
	trimmed := strings.TrimLeft(remainder, " \t")
	if !strings.HasPrefix(trimmed, "===") {
		return false
	}
	offset := end + len(remainder) - len(trimmed) + len("===")
	for offset < len(source) && (source[offset] == ' ' || source[offset] == '\t') {
		offset++
	}
	if offset >= len(source) || (source[offset] != '\'' && source[offset] != '"') {
		return false
	}
	quotedEnd := javaScriptStringEnd(source, offset)
	return quotedEnd > offset+1 && source[offset+1:quotedEnd-1] == want
}

func powerShellCodeMask(line string) string {
	masked := []byte(line)
	for index := 0; index < len(masked); {
		switch masked[index] {
		case '#':
			blankBytes(masked, index, len(masked))
			return string(masked)
		case '\'':
			end := index + 1
			for end < len(masked) {
				if masked[end] == '\'' {
					end++
					if end < len(masked) && masked[end] == '\'' {
						end++
						continue
					}
					break
				}
				end++
			}
			blankBytes(masked, index, end)
			index = end
			continue
		case '"':
			end := index + 1
			masked[index] = ' '
			for end < len(masked) && line[end] != '"' {
				if line[end] == '`' {
					masked[end] = ' '
					end++
					if end < len(masked) {
						masked[end] = ' '
						end++
					}
					continue
				}
				if line[end] == '$' {
					if variableEnd, ok := powerShellEnvironmentEnd(line, end); ok {
						end = variableEnd
						continue
					}
				}
				masked[end] = ' '
				end++
			}
			if end < len(masked) {
				masked[end] = ' '
				end++
			}
			index = end
			continue
		case '`':
			masked[index] = ' '
			index++
			if index < len(masked) {
				masked[index] = ' '
				index++
			}
			continue
		}
		index++
	}
	return string(masked)
}

type powerShellLexState struct {
	hereStringQuote byte
}

func powerShellCodeMaskWithState(line string, state *powerShellLexState) string {
	if state.hereStringQuote != 0 {
		if powerShellHereStringEnd(line, state.hereStringQuote) {
			state.hereStringQuote = 0
			return ""
		}
		if state.hereStringQuote == '\'' {
			return ""
		}
		return powerShellDoubleHereStringMask(line)
	}

	code := powerShellCodeMask(line)
	if start, quote, ok := powerShellHereStringStart(line, code); ok {
		state.hereStringQuote = quote
		masked := []byte(code)
		blankBytes(masked, start, len(masked))
		return string(masked)
	}
	return code
}

func powerShellHereStringStart(line, code string) (int, byte, bool) {
	for index := range len(line) - 1 {
		quote := line[index+1]
		if line[index] != '@' || (quote != '\'' && quote != '"') || code[index] != '@' || code[index+1] != ' ' || strings.TrimSpace(line[index+2:]) != "" {
			continue
		}
		return index, quote, true
	}
	return 0, 0, false
}

func powerShellHereStringEnd(line string, quote byte) bool {
	line = strings.TrimSuffix(line, "\r")
	return len(line) == 2 && line[0] == quote && line[1] == '@'
}

func powerShellDoubleHereStringMask(line string) string {
	masked := make([]byte, len(line))
	for index := range masked {
		masked[index] = ' '
	}
	for index := range len(line) {
		if line[index] == '`' {
			index++
			continue
		}
		if line[index] != '$' {
			continue
		}
		if end, ok := powerShellEnvironmentEnd(line, index); ok {
			copy(masked[index:end], line[index:end])
			index = end - 1
		}
	}
	return string(masked)
}

func powerShellEnvironmentAPILiteral(line string, start int) (string, bool) {
	for start < len(line) && (line[start] == ' ' || line[start] == '\t') {
		start++
	}
	if start >= len(line) || (line[start] != '\'' && line[start] != '"') {
		return "", false
	}
	quote := line[start]
	for end := start + 1; end < len(line); end++ {
		if line[end] == '`' {
			return "", false
		}
		if line[end] == quote {
			return line[start+1 : end], true
		}
	}
	return "", false
}

func powerShellEnvironmentEnd(line string, start int) (int, bool) {
	if strings.HasPrefix(strings.ToLower(line[start:]), "$env:") {
		end := start + len("$env:")
		for end < len(line) && isShellNamePart(line[end]) {
			end++
		}
		return end, end > start+len("$env:")
	}
	if len(line) >= start+6 && line[start:start+2] == "${" && strings.EqualFold(line[start+2:start+6], "env:") {
		end := start + 6
		for end < len(line) && isShellNamePart(line[end]) {
			end++
		}
		if end < len(line) && line[end] == '}' && end > start+6 {
			return end + 1, true
		}
	}
	return start, false
}

func shellCodeMask(line string) string {
	masked := []byte(line)
	inSingleQuote := false
	for index := 0; index < len(masked); index++ {
		if shellMasksSingleQuote(line, masked, index, &inSingleQuote) {
			continue
		}
		if shellCommentStarts(line, index) {
			blankBytes(masked, index, len(masked))
			break
		}
		if line[index] == '\\' && index+1 < len(masked) {
			masked[index], masked[index+1] = ' ', ' '
			index++
		}
	}
	return string(masked)
}

func shellMasksSingleQuote(line string, masked []byte, index int, inSingleQuote *bool) bool {
	if *inSingleQuote {
		masked[index] = ' '
		if line[index] == '\'' {
			*inSingleQuote = false
		}
		return true
	}
	if line[index] != '\'' {
		return false
	}
	masked[index] = ' '
	*inSingleQuote = true
	return true
}

func shellCommentStarts(line string, index int) bool {
	return line[index] == '#' && (index == 0 || line[index-1] == ' ' || line[index-1] == '\t')
}

type shellLexState struct {
	hereDoc   string
	literal   bool
	stripTabs bool
}

func shellCodeMaskWithState(line string, state *shellLexState) (string, bool) {
	if state.hereDoc != "" {
		return state.maskHereDocLine(line)
	}
	code := shellCodeMask(line)
	return state.beginHereDoc(line, code)
}

func (state *shellLexState) maskHereDocLine(line string) (string, bool) {
	if shellHereDocEnd(line, *state) {
		*state = shellLexState{}
		return "", false
	}
	if state.literal {
		return "", false
	}
	return shellCodeMask(line), false
}

func (state *shellLexState) beginHereDoc(line, code string) (string, bool) {
	hereDoc, found, ok := shellHereDocStart(line, code)
	if !found {
		return code, false
	}
	if !ok {
		return code, true
	}
	*state = hereDoc
	return code, false
}

func shellHereDocStart(line, code string) (shellLexState, bool, bool) {
	inDoubleQuote := false
	for index := range len(code) - 1 {
		if line[index] == '"' && !shellEscaped(line, index) {
			inDoubleQuote = !inDoubleQuote
			continue
		}
		if inDoubleQuote || !shellHereDocOperator(code, index) {
			continue
		}
		state, ok := shellHereDocState(line, index+2)
		return state, true, ok
	}
	return shellLexState{}, false, false
}

func shellHereDocOperator(code string, index int) bool {
	return code[index] == '<' && code[index+1] == '<' && (index+2 >= len(code) || code[index+2] != '<')
}

func shellHereDocState(line string, end int) (shellLexState, bool) {
	state := shellLexState{}
	if end < len(line) && line[end] == '-' {
		state.stripTabs = true
		end++
	}
	for end < len(line) && (line[end] == ' ' || line[end] == '\t') {
		end++
	}
	if end >= len(line) {
		return shellLexState{}, false
	}
	if line[end] == '\'' || line[end] == '"' {
		return quotedShellHereDocState(line, end, state)
	}
	return bareShellHereDocState(line, end, state)
}

func quotedShellHereDocState(line string, end int, state shellLexState) (shellLexState, bool) {
	quote := line[end]
	end++
	start := end
	for end < len(line) && line[end] != quote {
		end++
	}
	if end == len(line) || start == end {
		return shellLexState{}, false
	}
	state.hereDoc = line[start:end]
	state.literal = true
	return state, true
}

func bareShellHereDocState(line string, end int, state shellLexState) (shellLexState, bool) {
	start := end
	for end < len(line) && !strings.ContainsRune(" \t;|&<>()", rune(line[end])) {
		end++
	}
	if start == end {
		return shellLexState{}, false
	}
	state.hereDoc = line[start:end]
	return state, true
}

func shellHereDocEnd(line string, state shellLexState) bool {
	line = strings.TrimSuffix(line, "\r")
	if state.stripTabs {
		line = strings.TrimLeft(line, "\t")
	}
	return line == state.hereDoc
}

func shellEnvironmentRead(code string, start int) (string, string, bool) {
	if start+1 >= len(code) {
		return "", "", false
	}
	if code[start+1] == '{' {
		end := start + 2
		if end >= len(code) || !isShellNameStart(code[end]) {
			return "", "", false
		}
		for end < len(code) && isShellNamePart(code[end]) {
			end++
		}
		name := code[start+2 : end]
		if end >= len(code) {
			return "", "", false
		}
		operator := ""
		if code[end] == ':' {
			end++
		}
		if end < len(code) && strings.ContainsRune("-=+?", rune(code[end])) {
			operator = string(code[end])
		}
		switch operator {
		case "-", "=":
			return name, environmentDefaultSourceExpression, true
		case "?":
			return name, "required-environment", true
		default:
			return name, environmentDefaultEmptyUnset, true
		}
	}
	if !isShellNameStart(code[start+1]) {
		return "", "", false
	}
	end := start + 2
	for end < len(code) && isShellNamePart(code[end]) {
		end++
	}
	return code[start+1 : end], environmentDefaultEmptyUnset, true
}

func isShellNameStart(value byte) bool {
	return value == '_' || value >= 'A' && value <= 'Z' || value >= 'a' && value <= 'z'
}

func isShellNamePart(value byte) bool {
	return isShellNameStart(value) || value >= '0' && value <= '9'
}

func shellEscaped(value string, offset int) bool {
	backslashes := 0
	for offset > 0 && value[offset-1] == '\\' {
		backslashes++
		offset--
	}
	return backslashes%2 == 1
}

func blankBytes(value []byte, start, end int) {
	for index := start; index < end; index++ {
		value[index] = ' '
	}
}
