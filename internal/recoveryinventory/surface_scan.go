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

const (
	dispatcherHookPath = "plugin/engram/hooks/dispatcher.cjs"
	mcpToolKind        = "mcp-tool"
	openClawToolKind   = "openclaw-tool"
	grpcMethodKind     = "grpc-method"
	httpRouteKind      = "http-route"
)

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
			classification = classificationSourceUncertain
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
		return map[string]string{dispatcher: classificationSourceUncertain}
	}
	var config hookConfiguration
	if json.Unmarshal(data, &config) != nil {
		return map[string]string{dispatcher: classificationSourceUncertain}
	}
	sources := make(map[string]string)
	for _, entries := range config.Hooks {
		for _, entry := range entries {
			for _, hook := range entry.Hooks {
				classification := dispatcherActivation(hook.Command)
				if classification == "" || sources[dispatcher] == classificationSourceDeclared {
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
		return classificationSourceUncertain
	}
	if parts[1] == "-e" {
		for _, quote := range []string{"'", "\"", "`"} {
			literal := quote + dispatcherHookPath + quote
			if strings.Contains(command, "require("+literal+")") || strings.Contains(command, "import("+literal+")") {
				return classificationSourceDeclared
			}
		}
		return classificationSourceUncertain
	}
	switch filepath.ToSlash(strings.TrimPrefix(parts[1], "./")) {
	case "dispatcher.cjs", "plugin/engram/hooks/dispatcher.cjs":
		return classificationSourceDeclared
	}
	if strings.HasPrefix(parts[1], "-") {
		return classificationSourceUncertain
	}
	return ""
}

func activatesDispatcher(command string) bool {
	return dispatcherActivation(command) == classificationSourceDeclared
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
	if classifySurfaceFile(report, file, activeHooks) {
		return nil
	}
	source, err := os.ReadFile(file.absolute)
	if err != nil {
		return err
	}
	scanSurfaceSource(report, file.relative, source)
	return nil
}

func classifySurfaceFile(report *Report, file sourceFile, activeHooks map[string]string) bool {
	path := file.relative
	lowerPath := strings.ToLower(path)
	if strings.HasPrefix(path, "ui/") || strings.HasPrefix(path, "apps/operator-console/") {
		report.add(Record{Kind: "ui-claim", Path: path, Classification: "claim-only", ClaimOnly: true})
		return true
	}
	if strings.HasSuffix(lowerPath, ".md") {
		report.add(Record{Kind: "current-documentation-claim", Path: path, Classification: classificationSourceDeclared, ClaimOnly: true})
		return true
	}
	if strings.Contains(path, "/hooks/") || strings.HasPrefix(path, "plugin/engram/hooks/") {
		classifyHookSurface(report, path, activeHooks)
	}
	if strings.HasPrefix(path, "internal/mcp/") {
		report.add(Record{Kind: "mcp-surface", Path: path, Classification: classificationSourceDeclared})
	}
	if strings.HasPrefix(path, "cmd/engram/") || strings.HasPrefix(path, "internal/handlers/") {
		report.add(Record{Kind: "daemon-surface", Path: path, Classification: classificationSourceDeclared})
	}
	return false
}

func classifyHookSurface(report *Report, path string, activeHooks map[string]string) {
	if filepath.Ext(path) != ".cjs" {
		report.add(Record{Kind: "hook", Path: path, Name: strings.TrimSuffix(filepath.Base(path), filepath.Ext(path)), Classification: classificationSourceDeclared})
		return
	}
	if classification, active := activeHooks[path]; active {
		report.add(Record{Kind: "hook", Path: path, Name: strings.TrimSuffix(filepath.Base(path), filepath.Ext(path)), Classification: classification})
	}
}

func scanSurfaceSource(report *Report, path string, source []byte) {
	extension := filepath.Ext(path)
	if extension == ".go" {
		scanGoRoutes(report, path, source)
		scanDaemonTools(report, path, source)
	}
	if strings.HasPrefix(path, "internal/mcp/") && extension == ".go" {
		scanMCPTools(report, path, source)
	}
	if strings.HasPrefix(path, "plugin/openclaw-engram/src/tools/") {
		scanOpenClawTools(report, path, source)
	}
	if extension == ".proto" {
		scanProtoMethods(report, path, source)
	}
}

func scanMCPTools(report *Report, path string, source []byte) {
	fset := token.NewFileSet()
	parsed, err := parser.ParseFile(fset, path, source, 0)
	if err != nil {
		emitUncertainSurface(report, mcpToolKind, path, 1)
		return
	}
	scanner := newMCPToolScanner(report, path, fset, parsed)
	ast.Inspect(parsed, scanner.scan)
}

type mcpToolScanner struct {
	report    *Report
	path      string
	fset      *token.FileSet
	constants map[string]string
	uncertain bool
}

func newMCPToolScanner(report *Report, path string, fset *token.FileSet, parsed *ast.File) *mcpToolScanner {
	return &mcpToolScanner{report: report, path: path, fset: fset, constants: stringConstants(parsed)}
}

func (scanner *mcpToolScanner) scan(node ast.Node) bool {
	literal, ok := node.(*ast.CompositeLit)
	if !ok {
		return true
	}
	if isMCPToolList(literal.Type) {
		for _, element := range literal.Elts {
			if definition, ok := element.(*ast.CompositeLit); ok {
				scanner.scanDefinition(definition)
			}
		}
		return false
	}
	if isMCPToolType(literal.Type) {
		scanner.scanDefinition(literal)
		return false
	}
	return true
}

func (scanner *mcpToolScanner) scanDefinition(definition *ast.CompositeLit) {
	scanMCPToolDefinition(scanner.report, scanner.path, scanner.fset, definition, scanner.constants, scanner.emitUncertain)
}

func (scanner *mcpToolScanner) emitUncertain(line int) {
	if scanner.uncertain {
		return
	}
	emitUncertainSurface(scanner.report, mcpToolKind, scanner.path, line)
	scanner.uncertain = true
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
		report.add(Record{Kind: mcpToolKind, Path: path, Line: line, Name: redactedName(name), Classification: classificationSourceDeclared})
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
		emitUncertainSurface(report, openClawToolKind, path, 1)
		return
	}
	scanner := openClawToolScanner{report: report, path: path}
	for index, token := range tokens {
		if scanner.scanObjectToken(tokens, index) {
			continue
		}
		scanner.scanFactoryToken(tokens, index, token)
		scanner.scanNameToken(tokens, index, token)
	}
}

type openClawToolScanner struct {
	report      *Report
	path        string
	uncertain   bool
	objectStack []bool
}

func (scanner *openClawToolScanner) scanObjectToken(tokens []surfaceToken, index int) bool {
	switch tokens[index].text {
	case "{":
		scanner.objectStack = append(scanner.objectStack, index > 0 && tokens[index-1].text == "return")
		return true
	case "}":
		if len(scanner.objectStack) > 0 {
			scanner.objectStack = scanner.objectStack[:len(scanner.objectStack)-1]
		}
		return true
	default:
		return false
	}
}

func (scanner *openClawToolScanner) scanFactoryToken(tokens []surfaceToken, index int, token surfaceToken) {
	if token.stringLiteral || (token.text != "createSearchTool" && token.text != "createPresetTool") || index > 0 && (tokens[index-1].text == "." || tokens[index-1].text == "function") || index+1 >= len(tokens) || tokens[index+1].text != "(" {
		return
	}
	if index+2 >= len(tokens) {
		scanner.emitUncertain(token.line)
		return
	}
	name := tokens[index+2]
	if !name.stringLiteral || !name.static || !isSurfaceToolName(name.text) {
		scanner.emitUncertain(token.line)
		return
	}
	scanner.report.add(Record{Kind: openClawToolKind, Path: scanner.path, Line: name.line, Name: redactedName(name.text), Classification: classificationSourceDeclared})
}

func (scanner *openClawToolScanner) scanNameToken(tokens []surfaceToken, index int, token surfaceToken) {
	if len(scanner.objectStack) == 0 || token.stringLiteral || token.text != "name" || index+1 >= len(tokens) {
		return
	}
	next := tokens[index+1]
	if next.text == ":" {
		scanner.scanNamedObjectValue(tokens, index, token)
		return
	}
	if scanner.objectStack[len(scanner.objectStack)-1] && (next.text == "," || next.text == "}") {
		scanner.emitUncertain(token.line)
	}
}

func (scanner *openClawToolScanner) scanNamedObjectValue(tokens []surfaceToken, index int, token surfaceToken) {
	if index+2 >= len(tokens) {
		scanner.emitUncertain(token.line)
		return
	}
	value := tokens[index+2]
	if !value.stringLiteral || !value.static || !isSurfaceToolName(value.text) {
		scanner.emitUncertain(token.line)
		return
	}
	scanner.report.add(Record{Kind: openClawToolKind, Path: scanner.path, Line: token.line, Name: redactedName(value.text), Classification: classificationSourceDeclared})
}

func (scanner *openClawToolScanner) emitUncertain(line int) {
	if scanner.uncertain {
		return
	}
	emitUncertainSurface(scanner.report, openClawToolKind, scanner.path, line)
	scanner.uncertain = true
}

func scanProtoMethods(report *Report, path string, source []byte) {
	tokens, complete := lexSurfaceTokens(source)
	if !complete {
		emitUncertainSurface(report, grpcMethodKind, path, 1)
		return
	}
	uncertain := false
	emitUncertain := func(line int) {
		if uncertain {
			return
		}
		emitUncertainSurface(report, grpcMethodKind, path, line)
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
		report.add(Record{Kind: grpcMethodKind, Path: path, Line: tokens[index+1].line, Name: tokens[index+1].text, Classification: classificationSourceDeclared})
	}
}

func emitUncertainSurface(report *Report, kind, path string, line int) {
	report.add(Record{Kind: kind, Path: path, Line: line, Classification: classificationSourceUncertain})
}

type surfaceToken struct {
	text          string
	line          int
	stringLiteral bool
	static        bool
}

func lexSurfaceTokens(source []byte) ([]surfaceToken, bool) {
	lexer := surfaceLexer{source: source, line: 1}
	return lexer.lex()
}

type surfaceLexer struct {
	source []byte
	tokens []surfaceToken
	index  int
	line   int
}

func (lexer *surfaceLexer) lex() ([]surfaceToken, bool) {
	for lexer.index < len(lexer.source) {
		if !lexer.lexByte() {
			return lexer.tokens, false
		}
	}
	return lexer.tokens, true
}

func (lexer *surfaceLexer) lexByte() bool {
	switch lexer.source[lexer.index] {
	case ' ', '\t', '\r':
		lexer.index++
	case '\n':
		lexer.index++
		lexer.line++
	case '/':
		return lexer.lexSlash()
	case '"', '\'', '`':
		return lexer.lexString()
	default:
		lexer.lexToken()
	}
	return true
}

func (lexer *surfaceLexer) lexSlash() bool {
	if lexer.index+1 >= len(lexer.source) || lexer.source[lexer.index+1] != '/' && lexer.source[lexer.index+1] != '*' {
		lexer.tokens = append(lexer.tokens, surfaceToken{text: "/", line: lexer.line})
		lexer.index++
		return true
	}
	if lexer.source[lexer.index+1] == '/' {
		lexer.skipLineComment()
		return true
	}
	return lexer.skipBlockComment()
}

func (lexer *surfaceLexer) skipLineComment() {
	lexer.index += 2
	for lexer.index < len(lexer.source) && lexer.source[lexer.index] != '\n' {
		lexer.index++
	}
}

func (lexer *surfaceLexer) skipBlockComment() bool {
	lexer.index += 2
	for lexer.index+1 < len(lexer.source) && (lexer.source[lexer.index] != '*' || lexer.source[lexer.index+1] != '/') {
		if lexer.source[lexer.index] == '\n' {
			lexer.line++
		}
		lexer.index++
	}
	if lexer.index+1 >= len(lexer.source) {
		return false
	}
	lexer.index += 2
	return true
}

func (lexer *surfaceLexer) lexString() bool {
	quote, start, startLine := lexer.source[lexer.index], lexer.index+1, lexer.line
	lexer.index++
	for lexer.index < len(lexer.source) {
		if lexer.source[lexer.index] == '\\' {
			lexer.skipEscapedStringByte()
			continue
		}
		if lexer.source[lexer.index] == '\n' {
			lexer.line++
		}
		if lexer.source[lexer.index] == quote {
			lexer.tokens = append(lexer.tokens, surfaceToken{text: string(lexer.source[start:lexer.index]), line: startLine, stringLiteral: true, static: quote != '`'})
			lexer.index++
			return true
		}
		lexer.index++
	}
	return false
}

func (lexer *surfaceLexer) skipEscapedStringByte() {
	if lexer.index+1 < len(lexer.source) && lexer.source[lexer.index+1] == '\n' {
		lexer.line++
	}
	lexer.index += 2
}

func (lexer *surfaceLexer) lexToken() {
	start := lexer.index
	if isSurfaceIdentifierStart(lexer.source[lexer.index]) {
		lexer.index++
		for lexer.index < len(lexer.source) && isSurfaceIdentifierPart(lexer.source[lexer.index]) {
			lexer.index++
		}
	} else {
		lexer.index++
	}
	lexer.tokens = append(lexer.tokens, surfaceToken{text: string(lexer.source[start:lexer.index]), line: lexer.line})
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
		report.add(Record{Kind: httpRouteKind, Path: path, Classification: classificationSourceUncertain})
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
		name, ok := importedAlias(spec, importPrefix, defaultName)
		if ok {
			aliases[name] = struct{}{}
		}
	}
	return aliases
}

func importedAlias(spec *ast.ImportSpec, importPrefix, defaultName string) (string, bool) {
	path, err := strconv.Unquote(spec.Path.Value)
	if err != nil || path != importPrefix && !strings.HasPrefix(path, importPrefix+"/") {
		return "", false
	}
	if spec.Name != nil && spec.Name.Name != "." && spec.Name.Name != "_" {
		return spec.Name.Name, true
	}
	return defaultName, true
}

func chiRouterFields(file *ast.File, aliases map[string]struct{}) map[string]map[string]struct{} {
	fields := make(map[string]map[string]struct{})
	for _, declaration := range file.Decls {
		collectChiRouterFields(fields, declaration, aliases)
	}
	return fields
}

func collectChiRouterFields(fields map[string]map[string]struct{}, declaration ast.Decl, aliases map[string]struct{}) {
	gen, ok := declaration.(*ast.GenDecl)
	if !ok || gen.Tok != token.TYPE {
		return
	}
	for _, spec := range gen.Specs {
		collectChiRouterFieldsFromSpec(fields, spec, aliases)
	}
}

func collectChiRouterFieldsFromSpec(fields map[string]map[string]struct{}, spec ast.Spec, aliases map[string]struct{}) {
	typeSpec, ok := spec.(*ast.TypeSpec)
	if !ok {
		return
	}
	structType, ok := typeSpec.Type.(*ast.StructType)
	if !ok {
		return
	}
	for _, field := range structType.Fields.List {
		if !isChiRouterType(field.Type, aliases) {
			continue
		}
		for _, name := range field.Names {
			addChiRouterField(fields, typeSpec.Name.Name, name.Name)
		}
	}
}

func addChiRouterField(fields map[string]map[string]struct{}, typeName, fieldName string) {
	if fields[typeName] == nil {
		fields[typeName] = make(map[string]struct{})
	}
	fields[typeName][fieldName] = struct{}{}
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
		s.scanRouterDeclaration(statement, scope)
	case *ast.AssignStmt:
		s.scanRouterAssignment(statement, scope)
	case *ast.ExprStmt:
		s.scanCall(statement.X, scope)
	case *ast.IfStmt:
		s.scanIfStatement(statement, scope)
	case *ast.ForStmt:
		s.scanBlock(statement.Body, scope.clone())
	case *ast.RangeStmt:
		s.scanBlock(statement.Body, scope.clone())
	case *ast.SwitchStmt:
		s.scanCaseClauses(statement.Body.List, scope)
	case *ast.TypeSwitchStmt:
		s.scanCaseClauses(statement.Body.List, scope)
	case *ast.BlockStmt:
		s.scanBlock(statement, scope.clone())
	case *ast.LabeledStmt:
		s.scanStatement(statement.Stmt, scope)
	}
}

func (s *routeScanner) scanRouterDeclaration(statement *ast.DeclStmt, scope routeScope) {
	declaration, ok := statement.Decl.(*ast.GenDecl)
	if !ok || declaration.Tok != token.VAR {
		return
	}
	for _, spec := range declaration.Specs {
		if value, ok := spec.(*ast.ValueSpec); ok {
			s.bindRouterValues(value.Names, value.Values, scope)
		}
	}
}

func (s *routeScanner) scanRouterAssignment(statement *ast.AssignStmt, scope routeScope) {
	names := make([]*ast.Ident, 0, len(statement.Lhs))
	for _, left := range statement.Lhs {
		name, ok := left.(*ast.Ident)
		if !ok {
			return
		}
		names = append(names, name)
	}
	s.bindRouterValues(names, statement.Rhs, scope)
}

func (s *routeScanner) scanIfStatement(statement *ast.IfStmt, scope routeScope) {
	if statement.Init != nil {
		s.scanStatement(statement.Init, scope)
	}
	s.scanBlock(statement.Body, scope.clone())
	if statement.Else != nil {
		s.scanStatement(statement.Else, scope.clone())
	}
}

func (s *routeScanner) scanCaseClauses(clauses []ast.Stmt, scope routeScope) {
	for _, clause := range clauses {
		caseClause, ok := clause.(*ast.CaseClause)
		if !ok {
			continue
		}
		for _, nested := range caseClause.Body {
			s.scanStatement(nested, scope.clone())
		}
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
	call, selector, router, ok := s.routeCall(expression, scope)
	if !ok {
		return
	}
	switch selector.Sel.Name {
	case "Route", "Group":
		s.scanNestedRoute(call, router, selector.Sel.Name, scope)
	case "Mount":
		s.scanMountedRoute(call, router, scope)
	default:
		s.scanEndpointRoute(call, router)
	}
}

func (s *routeScanner) routeCall(expression ast.Expr, scope routeScope) (*ast.CallExpr, *ast.SelectorExpr, string, bool) {
	call, ok := expression.(*ast.CallExpr)
	if !ok {
		return nil, nil, "", false
	}
	selector, ok := call.Fun.(*ast.SelectorExpr)
	if !ok {
		return nil, nil, "", false
	}
	router, ok := s.routerID(selector.X, scope)
	if !ok {
		if len(s.chiAliases) > 0 && isRouteCall(selector.Sel.Name) {
			s.emitUncertainRoute(call)
		}
		return nil, nil, "", false
	}
	return call, selector, router, true
}

func (s *routeScanner) scanNestedRoute(call *ast.CallExpr, router, kind string, scope routeScope) {
	prefix, callback, ok := nestedRouteCallback(call, kind)
	if !ok {
		s.emitUncertainRoute(call)
		return
	}
	callbackScope := scope.clone()
	child := s.newRouter(false)
	callbackScope.routers[callback.Type.Params.List[0].Names[0].Name] = child
	s.relations = append(s.relations, routeRelation{parent: router, child: child, prefix: prefix})
	s.scanBlock(callback.Body, callbackScope)
}

func nestedRouteCallback(call *ast.CallExpr, kind string) (string, *ast.FuncLit, bool) {
	prefix, callbackIndex := "", 0
	if kind == "Route" {
		if len(call.Args) != 2 {
			return "", nil, false
		}
		var ok bool
		prefix, ok = stringLiteral(call.Args[0])
		if !ok {
			return "", nil, false
		}
		callbackIndex = 1
	} else if len(call.Args) != 1 {
		return "", nil, false
	}
	callback, ok := call.Args[callbackIndex].(*ast.FuncLit)
	if !ok || callback.Type.Params == nil || len(callback.Type.Params.List) == 0 || len(callback.Type.Params.List[0].Names) != 1 {
		return "", nil, false
	}
	return prefix, callback, true
}

func (s *routeScanner) scanMountedRoute(call *ast.CallExpr, router string, scope routeScope) {
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
}

func (s *routeScanner) scanEndpointRoute(call *ast.CallExpr, router string) {
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

func (s *routeScanner) emitUncertainRoute(call *ast.CallExpr) {
	s.report.add(Record{Kind: httpRouteKind, Path: s.path, Line: s.fset.Position(call.Pos()).Line, Classification: classificationSourceUncertain})
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
	if isStandardRouteMethod(selector.Sel.Name) {
		return standardRouteEndpoint(call, selector.Sel.Name)
	}
	if selector.Sel.Name == "Method" || selector.Sel.Name == "MethodFunc" {
		return customRouteEndpoint(call)
	}
	return "", "", false, false
}

func isStandardRouteMethod(name string) bool {
	switch name {
	case "Get", "Post", "Put", "Patch", "Delete", "Head", "Options":
		return true
	default:
		return false
	}
}

func standardRouteEndpoint(call *ast.CallExpr, method string) (string, string, bool, bool) {
	if len(call.Args) < 2 {
		return "", "", true, false
	}
	route, ok := stringLiteral(call.Args[0])
	return strings.ToUpper(method), route, true, ok
}

func customRouteEndpoint(call *ast.CallExpr) (string, string, bool, bool) {
	if len(call.Args) < 3 {
		return "", "", true, false
	}
	method, methodOK := httpMethod(call.Args[0])
	route, routeOK := stringLiteral(call.Args[1])
	return method, route, true, methodOK && routeOK
}

func (s *routeScanner) emitRoutes() {
	edges, mounted := routeRelations(s.relations)
	prefixes := s.routePrefixes(edges, mounted)
	for _, route := range s.routes {
		for prefix := range prefixes[route.router] {
			s.report.add(Record{Kind: httpRouteKind, Path: s.path, Line: route.line, Name: route.method + " " + joinRoute(prefix, route.path), Classification: classificationSourceDeclared})
		}
	}
}

func routeRelations(relations []routeRelation) (map[string][]routeRelation, map[string]struct{}) {
	edges := make(map[string][]routeRelation)
	mounted := make(map[string]struct{})
	for _, relation := range relations {
		edges[relation.parent] = append(edges[relation.parent], relation)
		mounted[relation.child] = struct{}{}
	}
	return edges, mounted
}

func (s *routeScanner) routePrefixes(edges map[string][]routeRelation, mounted map[string]struct{}) map[string]map[string]struct{} {
	prefixes := make(map[string]map[string]struct{})
	queue := make([]routeRelation, 0, len(s.roots))
	for router := range s.roots {
		if _, isMounted := mounted[router]; !isMounted {
			prefixes[router] = map[string]struct{}{"": {}}
			queue = append(queue, routeRelation{child: router})
		}
	}
	for len(queue) > 0 {
		current := queue[0].child
		queue = queue[1:]
		for _, relation := range edges[current] {
			for prefix := range prefixes[current] {
				if s.addRoutePrefix(prefixes, relation, prefix) {
					queue = append(queue, routeRelation{child: relation.child})
				}
			}
		}
	}
	return prefixes
}

func (s *routeScanner) addRoutePrefix(prefixes map[string]map[string]struct{}, relation routeRelation, prefix string) bool {
	childPrefix := joinRoute(prefix, relation.prefix)
	if prefixes[relation.child] == nil {
		prefixes[relation.child] = make(map[string]struct{})
	}
	if _, seen := prefixes[relation.child][childPrefix]; seen {
		return false
	}
	prefixes[relation.child][childPrefix] = struct{}{}
	return true
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
	scanner := daemonToolScanner{report: report, path: path, fset: fset, aliases: aliases, constants: stringConstants(parsed)}
	for _, declaration := range parsed.Decls {
		if function, ok := declaration.(*ast.FuncDecl); ok {
			scanner.scanFunction(function)
		}
	}
}

type daemonToolScanner struct {
	report    *Report
	path      string
	fset      *token.FileSet
	aliases   map[string]struct{}
	constants map[string]string
	result    int
}

func (scanner *daemonToolScanner) scanFunction(function *ast.FuncDecl) {
	if function.Recv == nil || function.Name.Name != "Tools" && function.Name.Name != "ProxyTools" {
		return
	}
	result, ok := moduleToolDefResult(function, scanner.aliases)
	if !ok {
		return
	}
	scanner.result = result
	ast.Inspect(function.Body, scanner.scanReturn)
}

func (scanner *daemonToolScanner) scanReturn(node ast.Node) bool {
	statement, ok := node.(*ast.ReturnStmt)
	if !ok {
		return true
	}
	scanner.scanReturnStatement(statement)
	return false
}

func (scanner *daemonToolScanner) scanReturnStatement(statement *ast.ReturnStmt) {
	if scanner.result >= len(statement.Results) {
		scanner.emitUncertain(statement.Pos())
		return
	}
	expression := statement.Results[scanner.result]
	if identifier, ok := expression.(*ast.Ident); ok && identifier.Name == "nil" {
		return
	}
	list, ok := expression.(*ast.CompositeLit)
	if !ok || !isModuleToolDefList(list.Type, scanner.aliases) {
		scanner.emitUncertain(statement.Pos())
		return
	}
	for _, element := range list.Elts {
		scanner.scanToolDefinition(element)
	}
}

func (scanner *daemonToolScanner) scanToolDefinition(element ast.Expr) {
	definition, ok := element.(*ast.CompositeLit)
	if !ok {
		scanner.emitUncertain(element.Pos())
		return
	}
	name, ok := moduleToolName(definition, scanner.constants)
	if !ok {
		scanner.emitUncertain(definition.Pos())
		return
	}
	scanner.report.add(Record{Kind: "daemon-tool", Path: scanner.path, Line: scanner.fset.Position(definition.Pos()).Line, Name: redactedName(name), Classification: classificationSourceDeclared})
}

func (scanner *daemonToolScanner) emitUncertain(position token.Pos) {
	emitUncertainTool(scanner.report, scanner.path, scanner.fset.Position(position).Line)
}

func emitUncertainTool(report *Report, path string, line int) {
	report.add(Record{Kind: "daemon-tool", Path: path, Line: line, Classification: classificationSourceUncertain})
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
