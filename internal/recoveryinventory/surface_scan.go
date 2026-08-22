package recoveryinventory

import (
	"encoding/json"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

const dispatcherHookPath = "plugin/engram/hooks/dispatcher.cjs"

type hookConfiguration struct {
	Hooks map[string][]struct {
		Hooks []struct {
			Command string `json:"command"`
		} `json:"hooks"`
	} `json:"hooks"`
}

// ScanSurfaces inventories source-declared transports and claims. UI roots are
// claim-only: neither their behavior nor their consumers are inferred.
func ScanSurfaces(root string) (Report, error) {
	report := newReport("package-route-hook-tool-documentation-claims")
	for _, surfaceRoot := range []string{"ui", "apps/operator-console"} {
		classification := "claim-only"
		if _, err := os.Stat(filepath.Join(root, surfaceRoot)); os.IsNotExist(err) {
			classification = "source-uncertain"
		}
		report.add(Record{Kind: "ui-root-claim", Path: filepath.ToSlash(surfaceRoot), Name: surfaceRoot, Classification: classification, ClaimOnly: true})
	}

	files, err := sourceFiles(root, ".go", ".js", ".cjs", ".mjs", ".ts", ".tsx", ".vue", ".proto", ".md")
	if err != nil {
		return Report{}, err
	}
	activeHooks := activatedHookSources(root)
	for _, file := range files {
		if isSurfaceTestFile(file.relative) {
			continue
		}
		if err := scanSurfaceFile(&report, file, activeHooks); err != nil {
			return Report{}, err
		}
	}
	report.finish()
	return report, nil
}

func activatedHookSources(root string) map[string]string {
	const dispatcher = dispatcherHookPath
	data, err := os.ReadFile(filepath.Join(root, "plugin", "engram", "hooks", "hooks.json"))
	if err != nil {
		return map[string]string{dispatcher: "source-uncertain"}
	}
	var config hookConfiguration
	if json.Unmarshal(data, &config) != nil {
		return map[string]string{dispatcher: "source-uncertain"}
	}
	sources := make(map[string]string)
	for _, entries := range config.Hooks {
		for _, entry := range entries {
			for _, hook := range entry.Hooks {
				classification := dispatcherActivation(hook.Command)
				if classification == "" || sources[dispatcher] == "source-declared" {
					continue
				}
				sources[dispatcher] = classification
			}
		}
	}
	return sources
}

func dispatcherActivation(command string) string {
	parts := strings.Fields(command)
	if len(parts) < 2 || parts[0] != "node" {
		return "source-uncertain"
	}
	if parts[1] == "-e" {
		for _, quote := range []string{"'", "\"", "`"} {
			literal := quote + dispatcherHookPath + quote
			if strings.Contains(command, "require("+literal+")") || strings.Contains(command, "import("+literal+")") {
				return "source-declared"
			}
		}
		return "source-uncertain"
	}
	switch filepath.ToSlash(strings.TrimPrefix(parts[1], "./")) {
	case "dispatcher.cjs", "plugin/engram/hooks/dispatcher.cjs":
		return "source-declared"
	}
	if strings.HasPrefix(parts[1], "-") {
		return "source-uncertain"
	}
	return ""
}

func activatesDispatcher(command string) bool {
	return dispatcherActivation(command) == "source-declared"
}

func isSurfaceTestFile(path string) bool {
	lowerPath := strings.ToLower(filepath.ToSlash(path))
	name := filepath.Base(lowerPath)
	return strings.Contains("/"+lowerPath+"/", "/testdata/") ||
		strings.Contains("/"+lowerPath+"/", "/fixtures/") ||
		strings.Contains("/"+lowerPath+"/", "/test/") ||
		strings.Contains("/"+lowerPath+"/", "/tests/") ||
		strings.Contains("/"+lowerPath+"/", "/__tests__/") ||
		strings.Contains(name, ".spec.") ||
		strings.HasPrefix(name, "playwright.config.") ||
		strings.HasSuffix(name, "_test.go") ||
		strings.HasSuffix(name, ".test.js") ||
		strings.HasSuffix(name, ".test.cjs") ||
		strings.HasSuffix(name, ".test.mjs") ||
		strings.HasSuffix(name, ".test.ts") ||
		strings.HasSuffix(name, ".test.tsx") ||
		strings.HasSuffix(name, ".test.vue") ||
		strings.HasSuffix(name, ".test.py")
}

func scanSurfaceFile(report *Report, file sourceFile, activeHooks map[string]string) error {
	path := file.relative
	lowerPath := strings.ToLower(path)
	if strings.HasPrefix(path, "ui/") || strings.HasPrefix(path, "apps/operator-console/") {
		report.add(Record{Kind: "ui-claim", Path: path, Classification: "claim-only", ClaimOnly: true})
		return nil
	}
	if strings.HasSuffix(lowerPath, ".md") {
		report.add(Record{Kind: "current-documentation-claim", Path: path, Classification: "source-declared", ClaimOnly: true})
		return nil
	}
	if strings.Contains(path, "/hooks/") || strings.HasPrefix(path, "plugin/engram/hooks/") {
		if filepath.Ext(path) != ".cjs" {
			report.add(Record{Kind: "hook", Path: path, Name: strings.TrimSuffix(filepath.Base(path), filepath.Ext(path)), Classification: "source-declared"})
		} else if classification, active := activeHooks[path]; active {
			report.add(Record{Kind: "hook", Path: path, Name: strings.TrimSuffix(filepath.Base(path), filepath.Ext(path)), Classification: classification})
		}
	}
	if strings.HasPrefix(path, "internal/mcp/") {
		report.add(Record{Kind: "mcp-surface", Path: path, Classification: "source-declared"})
	}
	if strings.HasPrefix(path, "cmd/engram/") || strings.HasPrefix(path, "internal/handlers/") {
		report.add(Record{Kind: "daemon-surface", Path: path, Classification: "source-declared"})
	}

	source, err := os.ReadFile(file.absolute)
	if err != nil {
		return err
	}
	if filepath.Ext(path) == ".go" {
		scanGoRoutes(report, path, source)
		scanDaemonTools(report, path, source)
	}
	if strings.HasPrefix(path, "internal/mcp/") && filepath.Ext(path) == ".go" {
		scanMCPTools(report, path, source)
	}
	if strings.HasPrefix(path, "plugin/openclaw-engram/src/tools/") {
		scanOpenClawTools(report, path, source)
	}
	if filepath.Ext(path) == ".proto" {
		scanProtoMethods(report, path, source)
	}
	return nil
}

func scanMCPTools(report *Report, path string, source []byte) {
	fset := token.NewFileSet()
	parsed, err := parser.ParseFile(fset, path, source, 0)
	if err != nil {
		emitUncertainSurface(report, "mcp-tool", path, 1)
		return
	}
	constants := stringConstants(parsed)
	uncertain := false
	emitUncertain := func(line int) {
		if uncertain {
			return
		}
		emitUncertainSurface(report, "mcp-tool", path, line)
		uncertain = true
	}
	ast.Inspect(parsed, func(node ast.Node) bool {
		literal, ok := node.(*ast.CompositeLit)
		if !ok {
			return true
		}
		if isMCPToolList(literal.Type) {
			for _, element := range literal.Elts {
				if definition, ok := element.(*ast.CompositeLit); ok {
					scanMCPToolDefinition(report, path, fset, definition, constants, emitUncertain)
				}
			}
			return false
		}
		if !isMCPToolType(literal.Type) {
			return true
		}
		scanMCPToolDefinition(report, path, fset, literal, constants, emitUncertain)
		return false
	})
}

func isMCPToolList(expression ast.Expr) bool {
	list, ok := expression.(*ast.ArrayType)
	return ok && isMCPToolType(list.Elt)
}

func isMCPToolType(expression ast.Expr) bool {
	var name string
	switch typed := expression.(type) {
	case *ast.Ident:
		name = typed.Name
	case *ast.SelectorExpr:
		name = typed.Sel.Name
	default:
		return false
	}
	return name == "Tool" || name == "ToolDefinition"
}

func scanMCPToolDefinition(report *Report, path string, fset *token.FileSet, definition *ast.CompositeLit, constants map[string]string, emitUncertain func(int)) {
	hasName := false
	for _, element := range definition.Elts {
		field, ok := element.(*ast.KeyValueExpr)
		if !ok {
			continue
		}
		key, ok := field.Key.(*ast.Ident)
		if !ok || key.Name != "Name" {
			continue
		}
		hasName = true
		name, ok := surfaceStringValue(field.Value, constants)
		line := fset.Position(field.Pos()).Line
		if !ok || !isSurfaceToolName(name) {
			emitUncertain(line)
			continue
		}
		report.add(Record{Kind: "mcp-tool", Path: path, Line: line, Name: redactedName(name), Classification: "source-declared"})
	}
	if !hasName {
		emitUncertain(fset.Position(definition.Pos()).Line)
	}
}

func surfaceStringValue(expression ast.Expr, constants map[string]string) (string, bool) {
	if value, ok := stringLiteral(expression); ok {
		return value, true
	}
	identifier, ok := expression.(*ast.Ident)
	if !ok {
		return "", false
	}
	value, ok := constants[identifier.Name]
	return value, ok
}

func scanOpenClawTools(report *Report, path string, source []byte) {
	tokens, complete := lexSurfaceTokens(source)
	if !complete {
		emitUncertainSurface(report, "openclaw-tool", path, 1)
		return
	}
	uncertain := false
	emitUncertain := func(line int) {
		if uncertain {
			return
		}
		emitUncertainSurface(report, "openclaw-tool", path, line)
		uncertain = true
	}
	objectStack := make([]bool, 0)
	for index, token := range tokens {
		switch token.text {
		case "{":
			objectStack = append(objectStack, index > 0 && tokens[index-1].text == "return")
			continue
		case "}":
			if len(objectStack) > 0 {
				objectStack = objectStack[:len(objectStack)-1]
			}
			continue
		}
		if !token.stringLiteral && (token.text == "createSearchTool" || token.text == "createPresetTool") && (index == 0 || tokens[index-1].text != "." && tokens[index-1].text != "function") && index+1 < len(tokens) && tokens[index+1].text == "(" {
			if index+2 >= len(tokens) {
				emitUncertain(token.line)
				continue
			}
			name := tokens[index+2]
			if !name.stringLiteral || !name.static || !isSurfaceToolName(name.text) {
				emitUncertain(token.line)
				continue
			}
			report.add(Record{Kind: "openclaw-tool", Path: path, Line: name.line, Name: redactedName(name.text), Classification: "source-declared"})
		}
		if len(objectStack) == 0 || token.stringLiteral || token.text != "name" || index+1 >= len(tokens) {
			continue
		}
		next := tokens[index+1]
		if next.text == ":" {
			if index+2 >= len(tokens) {
				emitUncertain(token.line)
				continue
			}
			value := tokens[index+2]
			if !value.stringLiteral || !value.static || !isSurfaceToolName(value.text) {
				emitUncertain(token.line)
				continue
			}
			report.add(Record{Kind: "openclaw-tool", Path: path, Line: token.line, Name: redactedName(value.text), Classification: "source-declared"})
			continue
		}
		if objectStack[len(objectStack)-1] && (next.text == "," || next.text == "}") {
			emitUncertain(token.line)
		}
	}
}

func scanProtoMethods(report *Report, path string, source []byte) {
	tokens, complete := lexSurfaceTokens(source)
	if !complete {
		emitUncertainSurface(report, "grpc-method", path, 1)
		return
	}
	uncertain := false
	emitUncertain := func(line int) {
		if uncertain {
			return
		}
		emitUncertainSurface(report, "grpc-method", path, line)
		uncertain = true
	}
	for index, token := range tokens {
		if token.stringLiteral || token.text != "rpc" {
			continue
		}
		if index+2 >= len(tokens) || tokens[index+1].stringLiteral || !isRPCMethodName(tokens[index+1].text) || tokens[index+2].text != "(" {
			emitUncertain(token.line)
			continue
		}
		report.add(Record{Kind: "grpc-method", Path: path, Line: tokens[index+1].line, Name: tokens[index+1].text, Classification: "source-declared"})
	}
}

func emitUncertainSurface(report *Report, kind, path string, line int) {
	report.add(Record{Kind: kind, Path: path, Line: line, Classification: "source-uncertain"})
}

type surfaceToken struct {
	text          string
	line          int
	stringLiteral bool
	static        bool
}

func lexSurfaceTokens(source []byte) ([]surfaceToken, bool) {
	tokens := make([]surfaceToken, 0)
	for index, line := 0, 1; index < len(source); {
		switch source[index] {
		case ' ', '\t', '\r':
			index++
		case '\n':
			index++
			line++
		case '/':
			if index+1 >= len(source) || source[index+1] != '/' && source[index+1] != '*' {
				tokens = append(tokens, surfaceToken{text: "/", line: line})
				index++
				continue
			}
			if source[index+1] == '/' {
				index += 2
				for index < len(source) && source[index] != '\n' {
					index++
				}
				continue
			}
			index += 2
			for index+1 < len(source) && (source[index] != '*' || source[index+1] != '/') {
				if source[index] == '\n' {
					line++
				}
				index++
			}
			if index+1 >= len(source) {
				return tokens, false
			}
			index += 2
		case '"', '\'', '`':
			quote, start, startLine := source[index], index+1, line
			closed := false
			index++
			for index < len(source) {
				if source[index] == '\\' {
					if index+1 < len(source) && source[index+1] == '\n' {
						line++
					}
					index += 2
					continue
				}
				if source[index] == '\n' {
					line++
				}
				if source[index] == quote {
					tokens = append(tokens, surfaceToken{text: string(source[start:index]), line: startLine, stringLiteral: true, static: quote != '`'})
					index++
					closed = true
					break
				}
				index++
			}
			if !closed {
				return tokens, false
			}
		default:
			start := index
			if isSurfaceIdentifierStart(source[index]) {
				index++
				for index < len(source) && isSurfaceIdentifierPart(source[index]) {
					index++
				}
			} else {
				index++
			}
			tokens = append(tokens, surfaceToken{text: string(source[start:index]), line: line})
		}
	}
	return tokens, true
}

func isSurfaceIdentifierStart(value byte) bool {
	return value >= 'A' && value <= 'Z' || value >= 'a' && value <= 'z' || value == '_' || value == '$'
}

func isSurfaceIdentifierPart(value byte) bool {
	return isSurfaceIdentifierStart(value) || value >= '0' && value <= '9'
}

func isSurfaceToolName(name string) bool {
	if len(name) == 0 || !isSurfaceASCIILetter(name[0]) {
		return false
	}
	for index := 1; index < len(name); index++ {
		value := name[index]
		if !isSurfaceASCIILetter(value) && (value < '0' || value > '9') && value != '_' && value != '.' && value != '-' {
			return false
		}
	}
	return true
}

func isRPCMethodName(name string) bool {
	if len(name) == 0 || !isSurfaceASCIILetter(name[0]) {
		return false
	}
	for index := 1; index < len(name); index++ {
		value := name[index]
		if !isSurfaceASCIILetter(value) && (value < '0' || value > '9') && value != '_' {
			return false
		}
	}
	return true
}

func isSurfaceASCIILetter(value byte) bool {
	return value >= 'A' && value <= 'Z' || value >= 'a' && value <= 'z'
}

func scanGoRoutes(report *Report, path string, source []byte) {
	fset := token.NewFileSet()
	parsed, err := parser.ParseFile(fset, path, source, 0)
	if err != nil {
		report.add(Record{Kind: "http-route", Path: path, Classification: "source-uncertain"})
		return
	}
	scanner := newRouteScanner(report, fset, path, parsed)
	scanner.scan()
}

type routeDefinition struct {
	router string
	method string
	path   string
	line   int
}

type routeRelation struct {
	parent string
	child  string
	prefix string
}

type routeScope struct {
	routers      map[string]string
	receiverName string
	receiverType string
}

func (s routeScope) clone() routeScope {
	routers := make(map[string]string, len(s.routers))
	for name, router := range s.routers {
		routers[name] = router
	}
	s.routers = routers
	return s
}

type routeScanner struct {
	report       *Report
	fset         *token.FileSet
	path         string
	parsed       *ast.File
	chiAliases   map[string]struct{}
	routerFields map[string]map[string]struct{}
	routes       []routeDefinition
	relations    []routeRelation
	roots        map[string]struct{}
	nextRouter   int
}

func newRouteScanner(report *Report, fset *token.FileSet, path string, parsed *ast.File) *routeScanner {
	chiAliases := chiRouterAliases(parsed)
	return &routeScanner{
		report:       report,
		fset:         fset,
		path:         path,
		parsed:       parsed,
		chiAliases:   chiAliases,
		routerFields: chiRouterFields(parsed, chiAliases),
		roots:        make(map[string]struct{}),
	}
}

func chiRouterAliases(file *ast.File) map[string]struct{} {
	aliases := make(map[string]struct{})
	for _, spec := range file.Imports {
		path, err := strconv.Unquote(spec.Path.Value)
		if err != nil || !isChiRouterModule(path) {
			continue
		}
		name := "chi"
		if spec.Name != nil && spec.Name.Name != "." && spec.Name.Name != "_" {
			name = spec.Name.Name
		}
		aliases[name] = struct{}{}
	}
	return aliases
}

func isChiRouterModule(path string) bool {
	const module = "github.com/go-chi/chi"
	if path == module {
		return true
	}
	version := strings.TrimPrefix(path, module+"/v")
	if version == path || version == "" {
		return false
	}
	for _, value := range version {
		if value < '0' || value > '9' {
			return false
		}
	}
	return true
}

func importedAliases(file *ast.File, importPrefix, defaultName string) map[string]struct{} {
	aliases := make(map[string]struct{})
	for _, spec := range file.Imports {
		path, err := strconv.Unquote(spec.Path.Value)
		if err != nil || (path != importPrefix && !strings.HasPrefix(path, importPrefix+"/")) {
			continue
		}
		name := defaultName
		if spec.Name != nil && spec.Name.Name != "." && spec.Name.Name != "_" {
			name = spec.Name.Name
		}
		aliases[name] = struct{}{}
	}
	return aliases
}

func chiRouterFields(file *ast.File, aliases map[string]struct{}) map[string]map[string]struct{} {
	fields := make(map[string]map[string]struct{})
	for _, declaration := range file.Decls {
		gen, ok := declaration.(*ast.GenDecl)
		if !ok || gen.Tok != token.TYPE {
			continue
		}
		for _, spec := range gen.Specs {
			typeSpec, ok := spec.(*ast.TypeSpec)
			if !ok {
				continue
			}
			structType, ok := typeSpec.Type.(*ast.StructType)
			if !ok {
				continue
			}
			for _, field := range structType.Fields.List {
				if !isChiRouterType(field.Type, aliases) {
					continue
				}
				for _, name := range field.Names {
					if fields[typeSpec.Name.Name] == nil {
						fields[typeSpec.Name.Name] = make(map[string]struct{})
					}
					fields[typeSpec.Name.Name][name.Name] = struct{}{}
				}
			}
		}
	}
	return fields
}

func isChiRouterType(expression ast.Expr, aliases map[string]struct{}) bool {
	if pointer, ok := expression.(*ast.StarExpr); ok {
		expression = pointer.X
	}
	selector, ok := expression.(*ast.SelectorExpr)
	if !ok || (selector.Sel.Name != "Router" && selector.Sel.Name != "Mux") {
		return false
	}
	packageName, ok := selector.X.(*ast.Ident)
	if !ok {
		return false
	}
	_, ok = aliases[packageName.Name]
	return ok
}

func (s *routeScanner) scan() {
	for _, declaration := range s.parsed.Decls {
		function, ok := declaration.(*ast.FuncDecl)
		if ok && function.Body != nil {
			s.scanFunction(function)
		}
	}
	s.emitRoutes()
}

func (s *routeScanner) scanFunction(function *ast.FuncDecl) {
	scope := routeScope{routers: make(map[string]string)}
	if function.Recv != nil && len(function.Recv.List) == 1 && len(function.Recv.List[0].Names) == 1 {
		scope.receiverName = function.Recv.List[0].Names[0].Name
		scope.receiverType = receiverTypeName(function.Recv.List[0].Type)
	}
	for _, field := range function.Type.Params.List {
		if !isChiRouterType(field.Type, s.chiAliases) {
			continue
		}
		for _, name := range field.Names {
			scope.routers[name.Name] = s.newRouter(true)
		}
	}
	s.scanBlock(function.Body, scope)
}

func receiverTypeName(expression ast.Expr) string {
	if pointer, ok := expression.(*ast.StarExpr); ok {
		expression = pointer.X
	}
	name, _ := expression.(*ast.Ident)
	if name == nil {
		return ""
	}
	return name.Name
}

func (s *routeScanner) newRouter(root bool) string {
	s.nextRouter++
	router := strconv.Itoa(s.nextRouter)
	if root {
		s.roots[router] = struct{}{}
	}
	return router
}

func (s *routeScanner) scanBlock(block *ast.BlockStmt, scope routeScope) {
	for _, statement := range block.List {
		s.scanStatement(statement, scope)
	}
}

func (s *routeScanner) scanStatement(statement ast.Stmt, scope routeScope) {
	switch statement := statement.(type) {
	case *ast.DeclStmt:
		declaration, ok := statement.Decl.(*ast.GenDecl)
		if !ok || declaration.Tok != token.VAR {
			return
		}
		for _, spec := range declaration.Specs {
			value, ok := spec.(*ast.ValueSpec)
			if !ok {
				continue
			}
			s.bindRouterValues(value.Names, value.Values, scope)
		}
	case *ast.AssignStmt:
		names := make([]*ast.Ident, 0, len(statement.Lhs))
		for _, left := range statement.Lhs {
			name, ok := left.(*ast.Ident)
			if !ok {
				return
			}
			names = append(names, name)
		}
		s.bindRouterValues(names, statement.Rhs, scope)
	case *ast.ExprStmt:
		s.scanCall(statement.X, scope)
	case *ast.IfStmt:
		if statement.Init != nil {
			s.scanStatement(statement.Init, scope)
		}
		s.scanBlock(statement.Body, scope.clone())
		if statement.Else != nil {
			s.scanStatement(statement.Else, scope.clone())
		}
	case *ast.ForStmt:
		s.scanBlock(statement.Body, scope.clone())
	case *ast.RangeStmt:
		s.scanBlock(statement.Body, scope.clone())
	case *ast.SwitchStmt:
		for _, clause := range statement.Body.List {
			caseClause, ok := clause.(*ast.CaseClause)
			if !ok {
				continue
			}
			for _, nested := range caseClause.Body {
				s.scanStatement(nested, scope.clone())
			}
		}
	case *ast.TypeSwitchStmt:
		for _, clause := range statement.Body.List {
			caseClause, ok := clause.(*ast.CaseClause)
			if !ok {
				continue
			}
			for _, nested := range caseClause.Body {
				s.scanStatement(nested, scope.clone())
			}
		}
	case *ast.BlockStmt:
		s.scanBlock(statement, scope.clone())
	case *ast.LabeledStmt:
		s.scanStatement(statement.Stmt, scope)
	}
}

func (s *routeScanner) bindRouterValues(names []*ast.Ident, values []ast.Expr, scope routeScope) {
	for index, name := range names {
		if index >= len(values) || !isChiNewRouter(values[index], s.chiAliases) {
			continue
		}
		scope.routers[name.Name] = s.newRouter(true)
	}
}

func isChiNewRouter(expression ast.Expr, aliases map[string]struct{}) bool {
	call, ok := expression.(*ast.CallExpr)
	if !ok {
		return false
	}
	selector, ok := call.Fun.(*ast.SelectorExpr)
	if !ok || selector.Sel.Name != "NewRouter" {
		return false
	}
	packageName, ok := selector.X.(*ast.Ident)
	if !ok {
		return false
	}
	_, ok = aliases[packageName.Name]
	return ok
}

func (s *routeScanner) scanCall(expression ast.Expr, scope routeScope) {
	call, ok := expression.(*ast.CallExpr)
	if !ok {
		return
	}
	selector, ok := call.Fun.(*ast.SelectorExpr)
	if !ok {
		return
	}
	router, ok := s.routerID(selector.X, scope)
	if !ok {
		if len(s.chiAliases) > 0 && isRouteCall(selector.Sel.Name) {
			s.emitUncertainRoute(call)
		}
		return
	}
	switch selector.Sel.Name {
	case "Route", "Group":
		prefix := ""
		callbackIndex := 0
		if selector.Sel.Name == "Route" {
			if len(call.Args) != 2 {
				s.emitUncertainRoute(call)
				return
			}
			var valid bool
			prefix, valid = stringLiteral(call.Args[0])
			if !valid {
				s.emitUncertainRoute(call)
				return
			}
			callbackIndex = 1
		} else if len(call.Args) != 1 {
			s.emitUncertainRoute(call)
			return
		}
		callback, ok := call.Args[callbackIndex].(*ast.FuncLit)
		if !ok || callback.Type.Params == nil || len(callback.Type.Params.List) == 0 || len(callback.Type.Params.List[0].Names) != 1 {
			s.emitUncertainRoute(call)
			return
		}
		callbackScope := scope.clone()
		child := s.newRouter(false)
		callbackScope.routers[callback.Type.Params.List[0].Names[0].Name] = child
		s.relations = append(s.relations, routeRelation{parent: router, child: child, prefix: prefix})
		s.scanBlock(callback.Body, callbackScope)
	case "Mount":
		if len(call.Args) != 2 {
			s.emitUncertainRoute(call)
			return
		}
		prefix, ok := stringLiteral(call.Args[0])
		if !ok {
			s.emitUncertainRoute(call)
			return
		}
		child, ok := s.routerID(call.Args[1], scope)
		if !ok {
			s.emitUncertainRoute(call)
			return
		}
		s.relations = append(s.relations, routeRelation{parent: router, child: child, prefix: prefix})
	default:
		method, route, endpoint, resolved := routeEndpoint(call)
		if !endpoint {
			return
		}
		if !resolved {
			s.emitUncertainRoute(call)
			return
		}
		s.routes = append(s.routes, routeDefinition{router: router, method: method, path: route, line: s.fset.Position(call.Pos()).Line})
	}
}

func (s *routeScanner) emitUncertainRoute(call *ast.CallExpr) {
	s.report.add(Record{Kind: "http-route", Path: s.path, Line: s.fset.Position(call.Pos()).Line, Classification: "source-uncertain"})
}

func isRouteCall(name string) bool {
	switch name {
	case "Route", "Group", "Mount", "Get", "Post", "Put", "Patch", "Delete", "Head", "Options", "Method", "MethodFunc":
		return true
	default:
		return false
	}
}

func (s *routeScanner) routerID(expression ast.Expr, scope routeScope) (string, bool) {
	if name, ok := expression.(*ast.Ident); ok {
		router, ok := scope.routers[name.Name]
		return router, ok
	}
	selector, ok := expression.(*ast.SelectorExpr)
	if !ok || selector.Sel == nil {
		return "", false
	}
	receiver, ok := selector.X.(*ast.Ident)
	if !ok || receiver.Name != scope.receiverName {
		return "", false
	}
	if _, ok := s.routerFields[scope.receiverType][selector.Sel.Name]; !ok {
		return "", false
	}
	router := scope.receiverType + "." + selector.Sel.Name
	s.roots[router] = struct{}{}
	return router, true
}

func routeEndpoint(call *ast.CallExpr) (string, string, bool, bool) {
	selector, ok := call.Fun.(*ast.SelectorExpr)
	if !ok {
		return "", "", false, false
	}
	switch selector.Sel.Name {
	case "Get", "Post", "Put", "Patch", "Delete", "Head", "Options":
		if len(call.Args) < 2 {
			return "", "", true, false
		}
		route, ok := stringLiteral(call.Args[0])
		return strings.ToUpper(selector.Sel.Name), route, true, ok
	case "Method", "MethodFunc":
		if len(call.Args) < 3 {
			return "", "", true, false
		}
		method, methodOK := httpMethod(call.Args[0])
		route, routeOK := stringLiteral(call.Args[1])
		return method, route, true, methodOK && routeOK
	default:
		return "", "", false, false
	}
}

func (s *routeScanner) emitRoutes() {
	edges := make(map[string][]routeRelation)
	mounted := make(map[string]struct{})
	for _, relation := range s.relations {
		edges[relation.parent] = append(edges[relation.parent], relation)
		mounted[relation.child] = struct{}{}
	}
	prefixes := make(map[string]map[string]struct{})
	queue := make([]routeRelation, 0, len(s.roots))
	for router := range s.roots {
		if _, isMounted := mounted[router]; isMounted {
			continue
		}
		prefixes[router] = map[string]struct{}{"": {}}
		queue = append(queue, routeRelation{child: router})
	}
	for len(queue) > 0 {
		current := queue[0].child
		queue = queue[1:]
		for _, relation := range edges[current] {
			for prefix := range prefixes[current] {
				childPrefix := joinRoute(prefix, relation.prefix)
				if prefixes[relation.child] == nil {
					prefixes[relation.child] = make(map[string]struct{})
				}
				if _, seen := prefixes[relation.child][childPrefix]; seen {
					continue
				}
				prefixes[relation.child][childPrefix] = struct{}{}
				queue = append(queue, routeRelation{child: relation.child})
			}
		}
	}
	for _, route := range s.routes {
		for prefix := range prefixes[route.router] {
			s.report.add(Record{Kind: "http-route", Path: s.path, Line: route.line, Name: route.method + " " + joinRoute(prefix, route.path), Classification: "source-declared"})
		}
	}
}

func scanDaemonTools(report *Report, path string, source []byte) {
	fset := token.NewFileSet()
	parsed, err := parser.ParseFile(fset, path, source, 0)
	if err != nil {
		return
	}
	aliases := importedAliases(parsed, "github.com/thebtf/engram/internal/module", "module")
	if len(aliases) == 0 {
		return
	}
	constants := stringConstants(parsed)
	for _, declaration := range parsed.Decls {
		function, ok := declaration.(*ast.FuncDecl)
		if !ok || function.Recv == nil || (function.Name.Name != "Tools" && function.Name.Name != "ProxyTools") {
			continue
		}
		toolDefResult, ok := moduleToolDefResult(function, aliases)
		if !ok {
			continue
		}
		ast.Inspect(function.Body, func(node ast.Node) bool {
			statement, ok := node.(*ast.ReturnStmt)
			if !ok {
				return true
			}
			if toolDefResult >= len(statement.Results) {
				emitUncertainTool(report, path, fset.Position(statement.Pos()).Line)
				return false
			}
			expression := statement.Results[toolDefResult]
			if identifier, ok := expression.(*ast.Ident); ok && identifier.Name == "nil" {
				return false
			}
			list, ok := expression.(*ast.CompositeLit)
			if !ok || !isModuleToolDefList(list.Type, aliases) {
				emitUncertainTool(report, path, fset.Position(statement.Pos()).Line)
				return false
			}
			for _, element := range list.Elts {
				definition, ok := element.(*ast.CompositeLit)
				if !ok {
					emitUncertainTool(report, path, fset.Position(element.Pos()).Line)
					continue
				}
				name, ok := moduleToolName(definition, constants)
				if !ok {
					emitUncertainTool(report, path, fset.Position(definition.Pos()).Line)
					continue
				}
				report.add(Record{Kind: "daemon-tool", Path: path, Line: fset.Position(definition.Pos()).Line, Name: redactedName(name), Classification: "source-declared"})
			}
			return false
		})
	}
}

func emitUncertainTool(report *Report, path string, line int) {
	report.add(Record{Kind: "daemon-tool", Path: path, Line: line, Classification: "source-uncertain"})
}

func stringConstants(file *ast.File) map[string]string {
	constants := make(map[string]string)
	for _, declaration := range file.Decls {
		gen, ok := declaration.(*ast.GenDecl)
		if !ok || gen.Tok != token.CONST {
			continue
		}
		for _, spec := range gen.Specs {
			value, ok := spec.(*ast.ValueSpec)
			if !ok {
				continue
			}
			for index, name := range value.Names {
				if index >= len(value.Values) {
					continue
				}
				if literal, ok := stringLiteral(value.Values[index]); ok {
					constants[name.Name] = literal
				}
			}
		}
	}
	return constants
}

func moduleToolDefResult(function *ast.FuncDecl, aliases map[string]struct{}) (int, bool) {
	if function.Type.Results == nil {
		return 0, false
	}
	for index, result := range function.Type.Results.List {
		if isModuleToolDefList(result.Type, aliases) {
			return index, true
		}
	}
	return 0, false
}

func isModuleToolDefList(expression ast.Expr, aliases map[string]struct{}) bool {
	array, ok := expression.(*ast.ArrayType)
	if !ok {
		return false
	}
	selector, ok := array.Elt.(*ast.SelectorExpr)
	if !ok || selector.Sel.Name != "ToolDef" {
		return false
	}
	packageName, ok := selector.X.(*ast.Ident)
	if !ok {
		return false
	}
	_, ok = aliases[packageName.Name]
	return ok
}

func moduleToolName(definition *ast.CompositeLit, constants map[string]string) (string, bool) {
	for _, element := range definition.Elts {
		field, ok := element.(*ast.KeyValueExpr)
		if !ok {
			continue
		}
		name, ok := field.Key.(*ast.Ident)
		if !ok || name.Name != "Name" {
			continue
		}
		if value, ok := stringLiteral(field.Value); ok {
			return value, true
		}
		identifier, ok := field.Value.(*ast.Ident)
		if !ok {
			return "", false
		}
		value, ok := constants[identifier.Name]
		return value, ok
	}
	return "", false
}

func httpMethod(expression ast.Expr) (string, bool) {
	if method, ok := stringLiteral(expression); ok {
		return strings.ToUpper(method), true
	}
	selector, ok := expression.(*ast.SelectorExpr)
	if !ok {
		return "", false
	}
	if packageName, ok := selector.X.(*ast.Ident); !ok || packageName.Name != "http" || !strings.HasPrefix(selector.Sel.Name, "Method") {
		return "", false
	}
	return strings.TrimPrefix(selector.Sel.Name, "Method"), true
}

func stringLiteral(expression ast.Expr) (string, bool) {
	literal, ok := expression.(*ast.BasicLit)
	if !ok || literal.Kind != token.STRING {
		return "", false
	}
	value, err := strconv.Unquote(literal.Value)
	return value, err == nil
}

func joinRoute(prefix, route string) string {
	if prefix == "" {
		if route == "" {
			return "/"
		}
		if strings.HasPrefix(route, "/") {
			return route
		}
		return "/" + route
	}
	return strings.TrimRight(prefix, "/") + "/" + strings.TrimLeft(route, "/")
}
