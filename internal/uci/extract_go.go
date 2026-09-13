package uci

import (
	"go/ast"
	"go/parser"
	"go/token"
	"strconv"
	"unicode/utf8"
)

// ExtractGo derives deterministic, checkout-independent evidence from one caller-owned Go source buffer.
func ExtractGo(source []byte, profile GoExtractionProfile) GoArtifact {
	lineStarts := goLineStarts(source)
	text, textTruncated := goSafeText(source, goExtractionMaxArtifactTextBytes)
	chunks, chunksTruncated := goSourceChunks(source, lineStarts)
	artifact := GoArtifact{
		Coverage: IndexCoveragePartial,
		Text:     text,
		Chunks:   chunks,
	}

	if textTruncated {
		goAddDiagnostic(&artifact, "TEXT_LIMIT", IndexSpan{}, "source text exceeded the bounded extraction text limit")
	}
	if chunksTruncated {
		goAddDiagnostic(&artifact, "CHUNK_LIMIT", IndexSpan{}, "source chunks exceeded the bounded extraction chunk limit")
	}
	if !utf8.Valid(source) {
		goAddDiagnostic(&artifact, "INVALID_UTF8", IndexSpan{}, "source contains invalid UTF-8")
		return goFinalizeArtifact(source, profile, artifact)
	}
	if len(source) > goExtractionMaxSourceBytes {
		goAddDiagnostic(&artifact, "SOURCE_LIMIT", IndexSpan{}, "source exceeded the bounded parser input limit")
		return goFinalizeArtifact(source, profile, artifact)
	}
	if !goExtractionProfileValid(profile) {
		goAddDiagnostic(&artifact, "INVALID_PROFILE", IndexSpan{}, "profile keys must be bounded, valid UTF-8, and nonempty")
		return goFinalizeArtifact(source, profile, artifact)
	}

	fileSet := token.NewFileSet()
	parsed, parseErr := parser.ParseFile(fileSet, "source.go", source, parser.AllErrors)
	if parseErr != nil {
		goAddDiagnostic(&artifact, "GO_PARSE_ERROR", IndexSpan{}, "source could not be parsed completely as Go")
	}
	if parsed == nil {
		goAddDiagnostic(&artifact, "GO_PARSE_UNAVAILABLE", IndexSpan{}, "parser produced no recoverable Go syntax tree")
		return goFinalizeArtifact(source, profile, artifact)
	}

	tokenFile := fileSet.File(parsed.Pos())
	if tokenFile == nil {
		goAddDiagnostic(&artifact, "INVALID_TOKEN_FILE", IndexSpan{}, "parser produced an invalid token file")
		return goFinalizeArtifact(source, profile, artifact)
	}

	definitions, references, incomplete := goExtractParsedGo(parsed, tokenFile, source, lineStarts)
	artifact.Definitions = definitions
	artifact.References = references
	if incomplete {
		goAddDiagnostic(&artifact, "PARTIAL_FACTS", IndexSpan{}, "some source facts could not be represented within extraction bounds")
	}
	if parseErr == nil && !textTruncated && !chunksTruncated && !incomplete {
		artifact.Coverage = IndexCoverageComplete
	}
	return goFinalizeArtifact(source, profile, artifact)
}

func goSourceChunks(source []byte, lineStarts []int) ([]GoChunk, bool) {
	if len(source) == 0 {
		return nil, false
	}

	chunks := make([]GoChunk, 0, min(goExtractionMaxChunks, (len(source)+goExtractionMaxChunkBytes-1)/goExtractionMaxChunkBytes))
	start := 0
	for start < len(source) && len(chunks) < goExtractionMaxChunks {
		end := start + goExtractionMaxChunkBytes
		if end > len(source) {
			end = len(source)
		} else {
			end = goChunkBoundary(source, start, end)
		}
		if end <= start {
			end = start + goExtractionMaxChunkBytes
			if end > len(source) {
				end = len(source)
			}
		}

		span, valid := goSpanFromOffsets(lineStarts, len(source), start, end)
		if !valid {
			return chunks, true
		}
		text, textTruncated := goSafeText(source[start:end], goExtractionMaxChunkBytes)
		chunks = append(chunks, GoChunk{
			Span:          span,
			Text:          text,
			ContentDigest: goSourceDigest(source[start:end]),
		})
		if textTruncated {
			return chunks, true
		}
		start = end
	}
	return chunks, start < len(source)
}

func goChunkBoundary(source []byte, start, end int) int {
	if end >= len(source) {
		return len(source)
	}
	boundary := end
	for boundary > start && source[boundary]&0xc0 == 0x80 {
		boundary--
	}
	if boundary == start {
		return end
	}
	return boundary
}

func goExtractParsedGo(file *ast.File, tokenFile *token.File, source []byte, lineStarts []int) ([]GoDefinition, []GoReferenceSite, bool) {
	if file == nil || file.Name == nil || file.Name.Name == "" {
		return nil, nil, true
	}
	packageName := file.Name.Name
	definitions := make([]GoDefinition, 0, len(file.Decls)+1)
	references := make([]GoReferenceSite, 0, len(file.Imports)+len(file.Decls))
	incomplete := !goAppendPackageDefinition(&definitions, file, tokenFile, source, lineStarts, packageName)
	for _, declaration := range file.Decls {
		if !goAppendParsedDeclaration(&definitions, declaration, tokenFile, source, lineStarts, packageName) {
			incomplete = true
		}
	}
	parsedReferences, referencesIncomplete := goExtractGoReferences(file, tokenFile, source, lineStarts, packageName)
	for _, reference := range parsedReferences {
		if !goAppendReference(&references, reference) {
			incomplete = true
		}
	}
	return definitions, references, incomplete || referencesIncomplete
}

func goAppendPackageDefinition(definitions *[]GoDefinition, file *ast.File, tokenFile *token.File, source []byte, lineStarts []int, packageName string) bool {
	span, valid := goSpanFromTokenPositions(tokenFile, source, lineStarts, file.Package, file.Name.End())
	if !valid {
		return false
	}
	localKey := "pkg:" + packageName
	return goAppendDefinition(definitions, GoDefinition{Kind: "package", LocalKey: localKey, SymbolKey: goQualifiedKey(packageName, localKey), Span: span})
}

func goAppendParsedDeclaration(definitions *[]GoDefinition, declaration ast.Decl, tokenFile *token.File, source []byte, lineStarts []int, packageName string) bool {
	switch declaration := declaration.(type) {
	case *ast.GenDecl:
		return goAppendTypeSpecifications(definitions, declaration, tokenFile, source, lineStarts, packageName)
	case *ast.FuncDecl:
		return goAppendFunctionDefinition(definitions, declaration, tokenFile, source, lineStarts, packageName)
	default:
		return true
	}
}

func goAppendTypeSpecifications(definitions *[]GoDefinition, declaration *ast.GenDecl, tokenFile *token.File, source []byte, lineStarts []int, packageName string) bool {
	complete := true
	for _, specification := range declaration.Specs {
		typeSpecification, ok := specification.(*ast.TypeSpec)
		if !ok || typeSpecification.Name == nil || typeSpecification.Name.Name == "" {
			continue
		}
		start, end := goTypeSpecificationBounds(declaration, typeSpecification)
		span, valid := goSpanFromTokenPositions(tokenFile, source, lineStarts, start, end)
		if !valid {
			complete = false
			continue
		}
		localKey := "type:" + typeSpecification.Name.Name
		if !goAppendDefinition(definitions, GoDefinition{Kind: "type", LocalKey: localKey, SymbolKey: goQualifiedKey(packageName, localKey), Span: span}) {
			complete = false
		}
	}
	return complete
}

func goTypeSpecificationBounds(declaration *ast.GenDecl, specification *ast.TypeSpec) (token.Pos, token.Pos) {
	if len(declaration.Specs) == 1 {
		return declaration.Pos(), declaration.End()
	}
	return specification.Pos(), specification.End()
}

func goAppendFunctionDefinition(definitions *[]GoDefinition, declaration *ast.FuncDecl, tokenFile *token.File, source []byte, lineStarts []int, packageName string) bool {
	if declaration.Name == nil || declaration.Name.Name == "" {
		return false
	}
	span, valid := goSpanFromTokenPositions(tokenFile, source, lineStarts, declaration.Pos(), declaration.End())
	if !valid {
		return false
	}
	kind, localKey, valid := goFunctionDefinitionIdentity(declaration)
	if !valid {
		return false
	}
	return goAppendDefinition(definitions, GoDefinition{Kind: kind, LocalKey: localKey, SymbolKey: goQualifiedKey(packageName, localKey), Span: span})
}

func goFunctionDefinitionIdentity(declaration *ast.FuncDecl) (string, string, bool) {
	if declaration.Recv == nil {
		return "function", "func:" + declaration.Name.Name, true
	}
	receiver, known := goReceiverName(declaration.Recv)
	if !known {
		return "", "", false
	}
	return "method", "method:" + receiver + "." + declaration.Name.Name, true
}

func goReceiverName(receivers *ast.FieldList) (string, bool) {
	if receivers == nil || len(receivers.List) != 1 || receivers.List[0] == nil {
		return "", false
	}
	return goNamedExpression(receivers.List[0].Type)
}

func goExtractGoReferences(file *ast.File, tokenFile *token.File, source []byte, lineStarts []int, packageName string) ([]GoReferenceSite, bool) {
	collector := goReferenceCollector{
		tokenFile:   tokenFile,
		source:      source,
		lineStarts:  lineStarts,
		packageName: packageName,
		excluded:    goReferenceExclusions(file),
		callSyntax:  make(map[ast.Node]struct{}),
		references:  make([]GoReferenceSite, 0, len(file.Imports)+len(file.Decls)),
	}
	collector.collectImports(file.Imports)
	ast.Inspect(file, collector.collectCallSyntax)
	ast.Inspect(file, collector.collectCompoundReference)
	ast.Inspect(file, collector.collectIdentifierReference)
	return collector.references, collector.incomplete
}

type goReferenceCollector struct {
	tokenFile   *token.File
	source      []byte
	lineStarts  []int
	packageName string
	excluded    map[*ast.Ident]struct{}
	callSyntax  map[ast.Node]struct{}
	references  []GoReferenceSite
	incomplete  bool
}

func (collector *goReferenceCollector) collectImports(imports []*ast.ImportSpec) {
	for _, specification := range imports {
		collector.collectImport(specification)
	}
}

func (collector *goReferenceCollector) collectImport(specification *ast.ImportSpec) {
	if specification == nil || specification.Path == nil {
		collector.incomplete = true
		return
	}
	path, err := strconv.Unquote(specification.Path.Value)
	if err != nil || path == "" {
		collector.incomplete = true
		return
	}
	span, valid := goSpanFromTokenPositions(collector.tokenFile, collector.source, collector.lineStarts, specification.Path.Pos(), specification.Path.End())
	if !valid {
		collector.incomplete = true
		return
	}
	collector.add("import", "import:"+path, span)
}

func (collector *goReferenceCollector) collectCallSyntax(node ast.Node) bool {
	call, ok := node.(*ast.CallExpr)
	if ok {
		goMarkExpressionNodes(call.Fun, collector.callSyntax, collector.excluded)
	}
	return true
}

func (collector *goReferenceCollector) collectCompoundReference(node ast.Node) bool {
	switch node := node.(type) {
	case *ast.CallExpr:
		collector.collectCall(node)
	case *ast.SelectorExpr:
		collector.collectSelector(node)
	}
	return true
}

func (collector *goReferenceCollector) collectCall(call *ast.CallExpr) {
	name, known := goNamedExpression(call.Fun)
	if !known {
		collector.incomplete = true
		return
	}
	span, valid := goSpanFromTokenPositions(collector.tokenFile, collector.source, collector.lineStarts, call.Fun.Pos(), call.Fun.End())
	if !valid {
		collector.incomplete = true
		return
	}
	collector.add("call", "call:"+name, span)
}

func (collector *goReferenceCollector) collectSelector(selector *ast.SelectorExpr) {
	if _, partOfCall := collector.callSyntax[selector]; partOfCall {
		return
	}
	name, known := goNamedExpression(selector)
	if !known {
		collector.incomplete = true
		return
	}
	span, valid := goSpanFromTokenPositions(collector.tokenFile, collector.source, collector.lineStarts, selector.Pos(), selector.End())
	if !valid {
		collector.incomplete = true
		return
	}
	collector.add("reference", "ref:"+name, span)
	goMarkExpressionIdentifiers(selector, collector.excluded)
}

func (collector *goReferenceCollector) collectIdentifierReference(node ast.Node) bool {
	identifier, ok := node.(*ast.Ident)
	if !ok || identifier.Name == "_" {
		return true
	}
	if _, skip := collector.excluded[identifier]; skip {
		return true
	}
	span, valid := goSpanFromTokenPositions(collector.tokenFile, collector.source, collector.lineStarts, identifier.Pos(), identifier.End())
	if !valid {
		collector.incomplete = true
		return true
	}
	collector.add("reference", "ref:"+identifier.Name, span)
	return true
}

func (collector *goReferenceCollector) add(kind, localKey string, span IndexSpan) {
	collector.references = append(collector.references, GoReferenceSite{Kind: kind, LocalKey: localKey, SymbolKey: goQualifiedKey(collector.packageName, localKey), Span: span})
}

func goReferenceExclusions(file *ast.File) map[*ast.Ident]struct{} {
	excluded := make(map[*ast.Ident]struct{})
	if file.Name != nil {
		excluded[file.Name] = struct{}{}
	}
	ast.Inspect(file, func(node ast.Node) bool {
		goExcludeReferenceNode(node, excluded)
		return true
	})
	return excluded
}

func goExcludeReferenceNode(node ast.Node, excluded map[*ast.Ident]struct{}) {
	switch node := node.(type) {
	case *ast.FuncDecl:
		goExcludeFunctionDeclaration(node, excluded)
	case *ast.TypeSpec:
		goExcludeIdentifier(node.Name, excluded)
	case *ast.ValueSpec:
		goExcludeIdentifiers(node.Names, excluded)
	case *ast.ImportSpec:
		goExcludeIdentifier(node.Name, excluded)
	case *ast.Field:
		goExcludeIdentifiers(node.Names, excluded)
	case *ast.AssignStmt:
		goExcludeAssignmentBindings(node, excluded)
	case *ast.RangeStmt:
		goExcludeRangeBindings(node, excluded)
	case *ast.LabeledStmt:
		goExcludeIdentifier(node.Label, excluded)
	case *ast.BranchStmt:
		goExcludeIdentifier(node.Label, excluded)
	}
}

func goExcludeFunctionDeclaration(declaration *ast.FuncDecl, excluded map[*ast.Ident]struct{}) {
	goExcludeIdentifier(declaration.Name, excluded)
	if declaration.Recv != nil {
		goMarkNodeIdentifiers(declaration.Recv, excluded)
	}
	if declaration.Type == nil {
		return
	}
	goMarkNodeIdentifiers(declaration.Type.TypeParams, excluded)
	goMarkNodeIdentifiers(declaration.Type.Params, excluded)
	goMarkNodeIdentifiers(declaration.Type.Results, excluded)
}

func goExcludeIdentifiers(identifiers []*ast.Ident, excluded map[*ast.Ident]struct{}) {
	for _, identifier := range identifiers {
		goExcludeIdentifier(identifier, excluded)
	}
}

func goExcludeAssignmentBindings(statement *ast.AssignStmt, excluded map[*ast.Ident]struct{}) {
	if statement.Tok != token.DEFINE {
		return
	}
	for _, expression := range statement.Lhs {
		goMarkBindingIdentifier(expression, excluded)
	}
}

func goExcludeRangeBindings(statement *ast.RangeStmt, excluded map[*ast.Ident]struct{}) {
	if statement.Tok != token.DEFINE {
		return
	}
	goMarkBindingIdentifier(statement.Key, excluded)
	goMarkBindingIdentifier(statement.Value, excluded)
}

func goExcludeIdentifier(identifier *ast.Ident, excluded map[*ast.Ident]struct{}) {
	if identifier != nil {
		excluded[identifier] = struct{}{}
	}
}

func goMarkBindingIdentifier(expression ast.Expr, excluded map[*ast.Ident]struct{}) {
	identifier, ok := expression.(*ast.Ident)
	if ok {
		excluded[identifier] = struct{}{}
	}
}

func goMarkNodeIdentifiers(node ast.Node, excluded map[*ast.Ident]struct{}) {
	if node == nil {
		return
	}
	ast.Inspect(node, func(node ast.Node) bool {
		identifier, ok := node.(*ast.Ident)
		if ok {
			excluded[identifier] = struct{}{}
		}
		return true
	})
}

func goMarkExpressionNodes(expression ast.Expr, nodes map[ast.Node]struct{}, identifiers map[*ast.Ident]struct{}) {
	if expression == nil {
		return
	}
	nodes[expression] = struct{}{}
	switch expression := expression.(type) {
	case *ast.Ident:
		identifiers[expression] = struct{}{}
	case *ast.SelectorExpr:
		goExcludeIdentifier(expression.Sel, identifiers)
		goMarkExpressionNodes(expression.X, nodes, identifiers)
	case *ast.ParenExpr:
		goMarkExpressionNodes(expression.X, nodes, identifiers)
	case *ast.StarExpr:
		goMarkExpressionNodes(expression.X, nodes, identifiers)
	case *ast.IndexExpr:
		goMarkExpressionNodes(expression.X, nodes, identifiers)
	case *ast.IndexListExpr:
		goMarkExpressionNodes(expression.X, nodes, identifiers)
	}
}

func goMarkExpressionIdentifiers(expression ast.Expr, identifiers map[*ast.Ident]struct{}) {
	if expression == nil {
		return
	}
	switch expression := expression.(type) {
	case *ast.Ident:
		identifiers[expression] = struct{}{}
	case *ast.SelectorExpr:
		goExcludeIdentifier(expression.Sel, identifiers)
		goMarkExpressionIdentifiers(expression.X, identifiers)
	case *ast.ParenExpr:
		goMarkExpressionIdentifiers(expression.X, identifiers)
	case *ast.StarExpr:
		goMarkExpressionIdentifiers(expression.X, identifiers)
	case *ast.IndexExpr:
		goMarkExpressionIdentifiers(expression.X, identifiers)
	case *ast.IndexListExpr:
		goMarkExpressionIdentifiers(expression.X, identifiers)
	}
}

func goNamedExpression(expression ast.Expr) (string, bool) {
	if expression == nil {
		return "", false
	}
	if identifier, ok := expression.(*ast.Ident); ok {
		if identifier.Name == "" || identifier.Name == "_" {
			return "", false
		}
		return identifier.Name, true
	}
	if selector, ok := expression.(*ast.SelectorExpr); ok {
		return goNamedSelector(selector)
	}
	if child := goNamedExpressionChild(expression); child != nil {
		return goNamedExpression(child)
	}
	return "", false
}

func goNamedSelector(selector *ast.SelectorExpr) (string, bool) {
	if selector == nil || selector.Sel == nil || selector.Sel.Name == "" {
		return "", false
	}
	base, known := goNamedExpression(selector.X)
	if !known {
		return "", false
	}
	return base + "." + selector.Sel.Name, true
}

func goNamedExpressionChild(expression ast.Expr) ast.Expr {
	switch expression := expression.(type) {
	case *ast.ParenExpr:
		return expression.X
	case *ast.StarExpr:
		return expression.X
	case *ast.IndexExpr:
		return expression.X
	case *ast.IndexListExpr:
		return expression.X
	default:
		return nil
	}
}

func goSpanFromTokenPositions(tokenFile *token.File, source []byte, lineStarts []int, start, end token.Pos) (IndexSpan, bool) {
	startOffset, valid := goTokenOffset(tokenFile, start)
	if !valid {
		return IndexSpan{}, false
	}
	endOffset, valid := goTokenOffset(tokenFile, end)
	if !valid {
		return IndexSpan{}, false
	}
	return goSpanFromOffsets(lineStarts, len(source), startOffset, endOffset)
}

func goTokenOffset(tokenFile *token.File, position token.Pos) (int, bool) {
	if tokenFile == nil || position == token.NoPos {
		return 0, false
	}
	value := int(position)
	if value < tokenFile.Base() || value > tokenFile.Base()+tokenFile.Size() {
		return 0, false
	}
	return tokenFile.Offset(position), true
}

func goQualifiedKey(packageName, localKey string) string {
	return "go:" + packageName + "/" + localKey
}

func goAppendDefinition(definitions *[]GoDefinition, definition GoDefinition) bool {
	if len(*definitions) >= goExtractionMaxDefinitions {
		return false
	}
	*definitions = append(*definitions, definition)
	return true
}

func goAppendReference(references *[]GoReferenceSite, reference GoReferenceSite) bool {
	if len(*references) >= goExtractionMaxReferences {
		return false
	}
	*references = append(*references, reference)
	return true
}
