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
	incomplete := false

	if span, valid := goSpanFromTokenPositions(tokenFile, source, lineStarts, file.Package, file.Name.End()); valid {
		if !goAppendDefinition(&definitions, GoDefinition{
			Kind:      "package",
			LocalKey:  "pkg:" + packageName,
			SymbolKey: goQualifiedKey(packageName, "pkg:"+packageName),
			Span:      span,
		}) {
			incomplete = true
		}
	} else {
		incomplete = true
	}

	for _, declaration := range file.Decls {
		switch declaration := declaration.(type) {
		case *ast.GenDecl:
			for _, specification := range declaration.Specs {
				typeSpecification, ok := specification.(*ast.TypeSpec)
				if !ok || typeSpecification.Name == nil || typeSpecification.Name.Name == "" {
					continue
				}

				start, end := typeSpecification.Pos(), typeSpecification.End()
				if len(declaration.Specs) == 1 {
					start, end = declaration.Pos(), declaration.End()
				}
				span, valid := goSpanFromTokenPositions(tokenFile, source, lineStarts, start, end)
				if !valid {
					incomplete = true
					continue
				}
				localKey := "type:" + typeSpecification.Name.Name
				if !goAppendDefinition(&definitions, GoDefinition{
					Kind:      "type",
					LocalKey:  localKey,
					SymbolKey: goQualifiedKey(packageName, localKey),
					Span:      span,
				}) {
					incomplete = true
				}
			}
		case *ast.FuncDecl:
			if declaration.Name == nil || declaration.Name.Name == "" {
				incomplete = true
				continue
			}
			span, valid := goSpanFromTokenPositions(tokenFile, source, lineStarts, declaration.Pos(), declaration.End())
			if !valid {
				incomplete = true
				continue
			}
			kind := "function"
			localKey := "func:" + declaration.Name.Name
			if declaration.Recv != nil {
				receiver, known := goReceiverName(declaration.Recv)
				if !known {
					incomplete = true
					continue
				}
				kind = "method"
				localKey = "method:" + receiver + "." + declaration.Name.Name
			}
			if !goAppendDefinition(&definitions, GoDefinition{
				Kind:      kind,
				LocalKey:  localKey,
				SymbolKey: goQualifiedKey(packageName, localKey),
				Span:      span,
			}) {
				incomplete = true
			}
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

func goReceiverName(receivers *ast.FieldList) (string, bool) {
	if receivers == nil || len(receivers.List) != 1 || receivers.List[0] == nil {
		return "", false
	}
	return goNamedExpression(receivers.List[0].Type)
}

func goExtractGoReferences(file *ast.File, tokenFile *token.File, source []byte, lineStarts []int, packageName string) ([]GoReferenceSite, bool) {
	references := make([]GoReferenceSite, 0, len(file.Imports)+len(file.Decls))
	incomplete := false

	for _, importSpecification := range file.Imports {
		if importSpecification == nil || importSpecification.Path == nil {
			incomplete = true
			continue
		}
		path, err := strconv.Unquote(importSpecification.Path.Value)
		if err != nil || path == "" {
			incomplete = true
			continue
		}
		span, valid := goSpanFromTokenPositions(tokenFile, source, lineStarts, importSpecification.Path.Pos(), importSpecification.Path.End())
		if !valid {
			incomplete = true
			continue
		}
		localKey := "import:" + path
		references = append(references, GoReferenceSite{
			Kind:      "import",
			LocalKey:  localKey,
			SymbolKey: goQualifiedKey(packageName, localKey),
			Span:      span,
		})
	}

	excluded := goReferenceExclusions(file)
	callSyntax := make(map[ast.Node]struct{})
	ast.Inspect(file, func(node ast.Node) bool {
		call, ok := node.(*ast.CallExpr)
		if !ok {
			return true
		}
		goMarkExpressionNodes(call.Fun, callSyntax, excluded)
		return true
	})

	ast.Inspect(file, func(node ast.Node) bool {
		switch node := node.(type) {
		case *ast.CallExpr:
			name, known := goNamedExpression(node.Fun)
			if !known {
				incomplete = true
				return true
			}
			span, valid := goSpanFromTokenPositions(tokenFile, source, lineStarts, node.Fun.Pos(), node.Fun.End())
			if !valid {
				incomplete = true
				return true
			}
			localKey := "call:" + name
			references = append(references, GoReferenceSite{
				Kind:      "call",
				LocalKey:  localKey,
				SymbolKey: goQualifiedKey(packageName, localKey),
				Span:      span,
			})
		case *ast.SelectorExpr:
			if _, partOfCall := callSyntax[node]; partOfCall {
				return true
			}
			name, known := goNamedExpression(node)
			if !known {
				incomplete = true
				return true
			}
			span, valid := goSpanFromTokenPositions(tokenFile, source, lineStarts, node.Pos(), node.End())
			if !valid {
				incomplete = true
				return true
			}
			localKey := "ref:" + name
			references = append(references, GoReferenceSite{
				Kind:      "reference",
				LocalKey:  localKey,
				SymbolKey: goQualifiedKey(packageName, localKey),
				Span:      span,
			})
			goMarkExpressionIdentifiers(node, excluded)
		}
		return true
	})

	ast.Inspect(file, func(node ast.Node) bool {
		identifier, ok := node.(*ast.Ident)
		if !ok || identifier.Name == "_" {
			return true
		}
		if _, skip := excluded[identifier]; skip {
			return true
		}
		span, valid := goSpanFromTokenPositions(tokenFile, source, lineStarts, identifier.Pos(), identifier.End())
		if !valid {
			incomplete = true
			return true
		}
		localKey := "ref:" + identifier.Name
		references = append(references, GoReferenceSite{
			Kind:      "reference",
			LocalKey:  localKey,
			SymbolKey: goQualifiedKey(packageName, localKey),
			Span:      span,
		})
		return true
	})

	return references, incomplete
}

func goReferenceExclusions(file *ast.File) map[*ast.Ident]struct{} {
	excluded := make(map[*ast.Ident]struct{})
	if file.Name != nil {
		excluded[file.Name] = struct{}{}
	}
	ast.Inspect(file, func(node ast.Node) bool {
		switch node := node.(type) {
		case *ast.FuncDecl:
			goExcludeIdentifier(node.Name, excluded)
			if node.Recv != nil {
				goMarkNodeIdentifiers(node.Recv, excluded)
			}
			if node.Type != nil {
				if node.Type.TypeParams != nil {
					goMarkNodeIdentifiers(node.Type.TypeParams, excluded)
				}
				if node.Type.Params != nil {
					goMarkNodeIdentifiers(node.Type.Params, excluded)
				}
				if node.Type.Results != nil {
					goMarkNodeIdentifiers(node.Type.Results, excluded)
				}
			}
		case *ast.TypeSpec:
			goExcludeIdentifier(node.Name, excluded)
		case *ast.ValueSpec:
			for _, name := range node.Names {
				goExcludeIdentifier(name, excluded)
			}
		case *ast.ImportSpec:
			goExcludeIdentifier(node.Name, excluded)
		case *ast.Field:
			for _, name := range node.Names {
				goExcludeIdentifier(name, excluded)
			}
		case *ast.AssignStmt:
			if node.Tok == token.DEFINE {
				for _, expression := range node.Lhs {
					goMarkBindingIdentifier(expression, excluded)
				}
			}
		case *ast.RangeStmt:
			if node.Tok == token.DEFINE {
				goMarkBindingIdentifier(node.Key, excluded)
				goMarkBindingIdentifier(node.Value, excluded)
			}
		case *ast.LabeledStmt:
			goExcludeIdentifier(node.Label, excluded)
		case *ast.BranchStmt:
			goExcludeIdentifier(node.Label, excluded)
		}
		return true
	})
	return excluded
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
	switch expression := expression.(type) {
	case *ast.Ident:
		if expression == nil || expression.Name == "" || expression.Name == "_" {
			return "", false
		}
		return expression.Name, true
	case *ast.SelectorExpr:
		if expression == nil || expression.Sel == nil || expression.Sel.Name == "" {
			return "", false
		}
		base, known := goNamedExpression(expression.X)
		if !known {
			return "", false
		}
		return base + "." + expression.Sel.Name, true
	case *ast.ParenExpr:
		if expression == nil {
			return "", false
		}
		return goNamedExpression(expression.X)
	case *ast.StarExpr:
		if expression == nil {
			return "", false
		}
		return goNamedExpression(expression.X)
	case *ast.IndexExpr:
		if expression == nil {
			return "", false
		}
		return goNamedExpression(expression.X)
	case *ast.IndexListExpr:
		if expression == nil {
			return "", false
		}
		return goNamedExpression(expression.X)
	default:
		return "", false
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
