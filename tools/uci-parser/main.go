package main

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"sort"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/thebtf/engram/internal/uci"
	tree_sitter "github.com/tree-sitter/go-tree-sitter"
	tree_sitter_javascript "github.com/tree-sitter/tree-sitter-javascript/bindings/go"
	tree_sitter_typescript "github.com/tree-sitter/tree-sitter-typescript/bindings/go"
)

const (
	parserMaxSourceBytes       = 4 << 20
	parserMaxWireRequestBytes  = parserMaxSourceBytes*2 + 4<<10
	parserMaxWireResponseBytes = 16 << 20
	parserMaxProfileBytes      = 256
	parserMaxDefinitions       = 2_048
	parserMaxReferences        = 8_192
	parserMaxChunks            = 64
	parserMaxChunkBytes        = 64 << 10
	parserMaxDiagnostics       = 16
	parserMaxTextBytes         = parserMaxSourceBytes
	parserMaxDiagnosticBytes   = 512
)

func main() {
	if len(os.Args) == 2 && os.Args[1] == "--bundle-digest" {
		_, _ = fmt.Fprintln(os.Stdout, bundleDigest())
		return
	}
	if len(os.Args) != 1 {
		_, _ = fmt.Fprintln(os.Stderr, "uci parser worker: unsupported arguments")
		os.Exit(1)
	}
	if err := run(os.Stdin, os.Stdout); err != nil {
		_, _ = fmt.Fprintln(os.Stderr, "uci parser worker:", err)
		os.Exit(1)
	}
}

func run(input io.Reader, output io.Writer) error {
	line, err := readWireLine(input, parserMaxWireRequestBytes)
	if err != nil {
		return err
	}
	request, err := decodeRequest(line)
	if err != nil {
		return err
	}
	response := extract(request)
	return writeResponse(output, response)
}

func readWireLine(reader io.Reader, maximum int) ([]byte, error) {
	if maximum <= 0 {
		return nil, errors.New("wire frame limit is invalid")
	}
	bufferSize := 4 << 10
	if maximum+1 < bufferSize {
		bufferSize = maximum + 1
	}
	buffered := bufio.NewReaderSize(reader, bufferSize)
	line := make([]byte, 0, min(maximum+1, bufferSize))
	for {
		fragment, err := buffered.ReadSlice('\n')
		if len(fragment) > 0 {
			if len(line) > maximum+1-len(fragment) {
				return nil, errors.New("wire frame exceeded its byte limit")
			}
			line = append(line, fragment...)
		}
		switch {
		case err == nil:
			line = line[:len(line)-1]
			if len(line) > maximum {
				return nil, errors.New("wire frame exceeded its byte limit")
			}
			if len(line) > 0 && line[len(line)-1] == '\r' {
				line = line[:len(line)-1]
			}
			extra, readErr := buffered.ReadByte()
			if readErr == nil {
				_ = extra
				return nil, errors.New("wire input contains multiple frames")
			}
			if !errors.Is(readErr, io.EOF) {
				return nil, fmt.Errorf("read trailing wire bytes: %w", readErr)
			}
			return line, nil
		case errors.Is(err, bufio.ErrBufferFull):
			continue
		case errors.Is(err, io.EOF):
			return nil, errors.New("wire frame is missing a terminating newline")
		default:
			return nil, fmt.Errorf("read wire frame: %w", err)
		}
	}
}

func decodeRequest(line []byte) (uci.TreeSitterWorkerWireRequest, error) {
	var request uci.TreeSitterWorkerWireRequest
	decoder := json.NewDecoder(bytes.NewReader(line))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&request); err != nil {
		return uci.TreeSitterWorkerWireRequest{}, fmt.Errorf("decode request: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return uci.TreeSitterWorkerWireRequest{}, errors.New("request contains trailing JSON")
	}
	if request.Version != uci.TreeSitterWorkerProtocolVersion {
		return uci.TreeSitterWorkerWireRequest{}, errors.New("unsupported request protocol version")
	}
	return request, nil
}

func writeResponse(output io.Writer, response uci.TreeSitterWorkerWireResponse) error {
	encoded, err := encodeResponse(response)
	if err != nil {
		return err
	}
	if len(encoded) > parserMaxWireResponseBytes {
		response = minimalOutputLimitedResponse(response)
		encoded, err = encodeResponse(response)
		if err != nil {
			return err
		}
	}
	if len(encoded) > parserMaxWireResponseBytes {
		return errors.New("bounded response could not be represented")
	}
	_, err = output.Write(encoded)
	return err
}

func encodeResponse(response uci.TreeSitterWorkerWireResponse) ([]byte, error) {
	var encoded bytes.Buffer
	encoder := json.NewEncoder(&encoded)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(response); err != nil {
		return nil, err
	}
	return encoded.Bytes(), nil
}

func minimalOutputLimitedResponse(response uci.TreeSitterWorkerWireResponse) uci.TreeSitterWorkerWireResponse {
	response.Coverage = uci.IndexCoveragePartial
	response.Text = ""
	response.Definitions = []uci.TreeSitterDefinition{}
	response.References = []uci.TreeSitterReferenceSite{}
	response.Chunks = []uci.TreeSitterChunk{}
	response.Diagnostics = []uci.TreeSitterDiagnostic{{
		Code:    "OUTPUT_LIMIT",
		Message: "extracted parser facts exceeded the bounded response limit",
	}}
	return response
}

func extract(request uci.TreeSitterWorkerWireRequest) uci.TreeSitterWorkerWireResponse {
	response := baseResponse(request)
	if !profileKeyValid(request.ProfileKey) {
		response.Coverage = uci.IndexCoveragePartial
		addDiagnostic(&response, "INVALID_PROFILE", uci.IndexSpan{}, "profile key must be bounded, valid UTF-8, and nonempty")
		return response
	}
	if len(request.Source) > parserMaxSourceBytes {
		response.Coverage = uci.IndexCoveragePartial
		addDiagnostic(&response, "SOURCE_LIMIT", uci.IndexSpan{}, "source exceeded the bounded parser input limit")
		return response
	}
	if !utf8.Valid(request.Source) {
		response.Text = ""
		response.Chunks = []uci.TreeSitterChunk{}
		response.Coverage = uci.IndexCoveragePartial
		addDiagnostic(&response, "INVALID_UTF8", uci.IndexSpan{}, "source contains invalid UTF-8")
		return response
	}

	language, supported := parserLanguage(request.Language)
	if !supported {
		response.Coverage = uci.IndexCoverageUnavailable
		addDiagnostic(&response, "UNSUPPORTED_LANGUAGE", uci.IndexSpan{}, "no parser grammar is bundled for the requested language")
		return response
	}

	parser := tree_sitter.NewParser()
	defer parser.Close()
	if err := parser.SetLanguage(language); err != nil {
		response.Coverage = uci.IndexCoverageUnavailable
		addDiagnostic(&response, "PARSER_UNAVAILABLE", uci.IndexSpan{}, "bundled grammar could not be activated")
		return response
	}
	tree := parser.Parse(request.Source, nil)
	if tree == nil {
		response.Coverage = uci.IndexCoveragePartial
		addDiagnostic(&response, "PARSER_UNAVAILABLE", uci.IndexSpan{}, "parser produced no recoverable syntax tree")
		return response
	}
	defer tree.Close()

	collector := newCollector(request.Language, request.Source)
	cursor := tree.Walk()
	defer cursor.Close()
	collector.walk(cursor, parserScope{})
	response.Definitions = collector.definitions
	response.References = collector.references
	response.Chunks, collector.chunksTruncated = sourceChunks(request.Source, collector.lineStarts, response.Definitions)
	if collector.definitionsTruncated {
		addDiagnostic(&response, "DEFINITION_LIMIT", uci.IndexSpan{}, "syntax definitions exceeded the bounded extraction limit")
	}
	if collector.referencesTruncated {
		addDiagnostic(&response, "REFERENCE_LIMIT", uci.IndexSpan{}, "syntax reference sites exceeded the bounded extraction limit")
	}
	if collector.chunksTruncated {
		addDiagnostic(&response, "CHUNK_LIMIT", uci.IndexSpan{}, "source chunks exceeded the bounded extraction limit")
	}
	if collector.dynamicImport {
		addDiagnostic(&response, "DYNAMIC_IMPORT", collector.dynamicImportSpan, "dynamic import remains unresolved without module resolution")
	}
	if tree.RootNode().HasError() {
		response.Coverage = uci.IndexCoveragePartial
		addDiagnostic(&response, "PARSE_ERROR", collector.errorSpan, "source could not be parsed completely as "+string(request.Language))
	} else if collector.dynamicImport {
		response.Coverage = uci.IndexCoveragePartial
	} else if collector.definitionsTruncated || collector.referencesTruncated || collector.chunksTruncated {
		response.Coverage = uci.IndexCoveragePartial
		addDiagnostic(&response, "PARTIAL_FACTS", uci.IndexSpan{}, "some source facts could not be represented within extraction bounds")
	} else {
		response.Coverage = uci.IndexCoverageComplete
	}
	sortResponse(&response)
	return response
}

func baseResponse(request uci.TreeSitterWorkerWireRequest) uci.TreeSitterWorkerWireResponse {
	text, truncated := safeText(request.Source, parserMaxTextBytes)
	response := uci.TreeSitterWorkerWireResponse{
		Version:      uci.TreeSitterWorkerProtocolVersion,
		BundleDigest: bundleDigest(),
		Coverage:     uci.IndexCoveragePartial,
		Text:         text,
		Definitions:  []uci.TreeSitterDefinition{},
		References:   []uci.TreeSitterReferenceSite{},
		Chunks:       []uci.TreeSitterChunk{},
		Diagnostics:  []uci.TreeSitterDiagnostic{},
	}
	if !truncated && utf8.Valid(request.Source) {
		response.Chunks, _ = sourceChunks(request.Source, lineStarts(request.Source), nil)
	}
	return response
}

func parserLanguage(language uci.TreeSitterLanguage) (*tree_sitter.Language, bool) {
	switch language {
	case uci.TreeSitterLanguageJavaScript:
		return tree_sitter.NewLanguage(tree_sitter_javascript.Language()), true
	case uci.TreeSitterLanguageTypeScript:
		return tree_sitter.NewLanguage(tree_sitter_typescript.LanguageTypescript()), true
	case uci.TreeSitterLanguageTSX:
		return tree_sitter.NewLanguage(tree_sitter_typescript.LanguageTSX()), true
	default:
		return nil, false
	}
}

func bundleDigest() uci.IndexDigest {
	return uci.TreeSitterBundleDigest()
}

type parserScope struct {
	ownerLocalKey  string
	namespace      []string
	importSource   string
	reexportSource string
}

type parserCollector struct {
	language             uci.TreeSitterLanguage
	source               []byte
	lineStarts           []int
	definitions          []uci.TreeSitterDefinition
	references           []uci.TreeSitterReferenceSite
	definitionKeys       map[string]struct{}
	referenceKeys        map[string]struct{}
	dynamicImport        bool
	dynamicImportSpan    uci.IndexSpan
	definitionsTruncated bool
	referencesTruncated  bool
	chunksTruncated      bool
	errorSpan            uci.IndexSpan
}

func newCollector(language uci.TreeSitterLanguage, source []byte) *parserCollector {
	return &parserCollector{
		language:       language,
		source:         source,
		lineStarts:     lineStarts(source),
		definitions:    []uci.TreeSitterDefinition{},
		references:     []uci.TreeSitterReferenceSite{},
		definitionKeys: make(map[string]struct{}),
		referenceKeys:  make(map[string]struct{}),
	}
}

func (collector *parserCollector) walk(cursor *tree_sitter.TreeCursor, scope parserScope) {
	node := cursor.Node()
	if node == nil {
		return
	}
	if (node.IsError() || node.IsMissing()) && collector.errorSpan == (uci.IndexSpan{}) {
		if span, valid := collector.span(node); valid {
			collector.errorSpan = span
		}
	}

	if node.Kind() == "export_statement" {
		collector.collectExportDefinition(node, scope)
	}
	if isVariableDeclaration(node.Kind()) {
		collector.collectVariableDefinitions(node, scope, node)
	} else if definition, found := collector.definition(node, scope, node); found {
		collector.addDefinition(definition)
	}
	collector.collectReference(node, scope)

	next := scope
	if node.Kind() == "import_statement" {
		next.importSource = moduleSource(node.ChildByFieldName("source"), collector.source)
		next.reexportSource = ""
	}
	if node.Kind() == "export_statement" {
		next.reexportSource = moduleSource(node.ChildByFieldName("source"), collector.source)
		next.importSource = ""
	}
	if definition, found := collector.definition(node, scope, node); found {
		next = advanceScope(next, definition)
	}
	if cursor.GotoFirstChild() {
		for {
			collector.walk(cursor, next)
			if !cursor.GotoNextSibling() {
				break
			}
		}
		cursor.GotoParent()
	}
}

func (collector *parserCollector) collectExportDefinition(exportNode *tree_sitter.Node, scope parserScope) {
	declaration := exportNode.ChildByFieldName("declaration")
	if declaration == nil {
		for index := uint(0); index < exportNode.NamedChildCount(); index++ {
			candidate := exportNode.NamedChild(index)
			if candidate != nil && (isVariableDeclaration(candidate.Kind()) || definitionKind(candidate.Kind()) != "") {
				declaration = candidate
				break
			}
		}
	}
	if declaration == nil {
		return
	}
	if isVariableDeclaration(declaration.Kind()) {
		collector.collectVariableDefinitions(declaration, scope, exportNode)
		return
	}
	if definition, found := collector.definition(declaration, scope, exportNode); found {
		collector.addDefinition(definition)
	}
}

func (collector *parserCollector) collectVariableDefinitions(node *tree_sitter.Node, scope parserScope, spanNode *tree_sitter.Node) {
	kind := variableKind(node, collector.source)
	if kind == "" {
		return
	}
	for index := uint(0); index < node.NamedChildCount(); index++ {
		declarator := node.NamedChild(index)
		if declarator == nil || declarator.Kind() != "variable_declarator" {
			continue
		}
		bindings := collector.bindingNodes(declarator.ChildByFieldName("name"))
		if len(bindings) == 0 {
			collector.definitionsTruncated = true
			continue
		}
		spanTarget := spanNode
		if node.NamedChildCount() > 1 {
			spanTarget = declarator
		}
		span, valid := collector.span(spanTarget)
		if !valid {
			collector.definitionsTruncated = true
			continue
		}
		for _, binding := range bindings {
			name := nodeText(binding, collector.source)
			if name == "" {
				collector.definitionsTruncated = true
				continue
			}
			qualifiedName := qualified(scope.namespace, name)
			collector.addDefinition(uci.TreeSitterDefinition{
				Kind:      kind,
				Name:      name,
				SymbolKey: string(collector.language) + ":" + kind + ":" + qualifiedName,
				LocalKey:  kind + ":" + qualifiedName,
				Span:      span,
			})
		}
	}
}

func (collector *parserCollector) definition(node *tree_sitter.Node, scope parserScope, spanNode *tree_sitter.Node) (uci.TreeSitterDefinition, bool) {
	kind := definitionKind(node.Kind())
	if kind == "" {
		return uci.TreeSitterDefinition{}, false
	}
	name := nodeText(node.ChildByFieldName("name"), collector.source)
	if name == "" {
		return uci.TreeSitterDefinition{}, false
	}
	span, valid := collector.span(spanNode)
	if !valid {
		collector.definitionsTruncated = true
		return uci.TreeSitterDefinition{}, false
	}
	qualifiedName := qualified(scope.namespace, name)
	return uci.TreeSitterDefinition{
		Kind:      kind,
		Name:      name,
		SymbolKey: string(collector.language) + ":" + kind + ":" + qualifiedName,
		LocalKey:  kind + ":" + qualifiedName,
		Span:      span,
	}, true
}

func (collector *parserCollector) addDefinition(definition uci.TreeSitterDefinition) {
	if definition.Name == "" || definition.SymbolKey == "" {
		return
	}
	if _, exists := collector.definitionKeys[definition.SymbolKey]; exists {
		return
	}
	if len(collector.definitions) >= parserMaxDefinitions {
		collector.definitionsTruncated = true
		return
	}
	collector.definitionKeys[definition.SymbolKey] = struct{}{}
	collector.definitions = append(collector.definitions, definition)
}

func (collector *parserCollector) collectReference(node *tree_sitter.Node, scope parserScope) {
	switch node.Kind() {
	case "import_statement":
		if source := moduleSource(node.ChildByFieldName("source"), collector.source); source != "" {
			collector.addReference("import", "import:"+source, "", source, uci.TreeSitterResolutionSyntaxOnly, node)
		}
	case "import_clause":
		collector.collectDefaultImportBinding(node, scope.importSource)
	case "import_specifier", "namespace_import":
		if scope.importSource == "" {
			return
		}
		imported, local := aliasNames(nodeText(node, collector.source))
		if imported == "" || local == "" {
			return
		}
		target := scope.importSource + "#" + imported
		collector.addReference("import_alias", "import:"+target+":"+local, "", target, uci.TreeSitterResolutionSyntaxOnly, node)
	case "import_require_clause":
		collector.collectImportRequireBinding(node)
	case "export_statement":
		source := moduleSource(node.ChildByFieldName("source"), collector.source)
		if source != "" {
			collector.addReference("reexport", "reexport:"+source, "", source, uci.TreeSitterResolutionPartial, node)
		}
	case "export_specifier", "namespace_export":
		imported, local := aliasNames(nodeText(node, collector.source))
		if imported == "" || local == "" {
			return
		}
		if scope.reexportSource != "" {
			target := scope.reexportSource + "#" + imported
			collector.addReference("reexport_alias", "reexport:"+target+":"+local, "", target, uci.TreeSitterResolutionPartial, node)
			return
		}
		collector.addReference("export_alias", "export:"+imported+":"+local, "", imported, uci.TreeSitterResolutionSyntaxOnly, node)
	case "call_expression", "new_expression":
		callee := node.ChildByFieldName("function")
		if callee == nil && node.NamedChildCount() > 0 {
			callee = node.NamedChild(0)
		}
		raw := nodeText(callee, collector.source)
		if raw != "" {
			resolution := uci.TreeSitterResolutionSyntaxOnly
			if raw == "import" {
				resolution = uci.TreeSitterResolutionPartial
				collector.markDynamicImport(node)
			}
			collector.addReference("call", "call:"+raw, scope.ownerLocalKey, raw, resolution, node)
		}
	case "member_expression", "optional_member_expression":
		raw := nodeText(node, collector.source)
		if raw != "" {
			collector.addReference("reference", "reference:"+raw, scope.ownerLocalKey, raw, uci.TreeSitterResolutionSyntaxOnly, node)
		}
	case "jsx_opening_element", "jsx_self_closing_element":
		name := node.ChildByFieldName("name")
		if name == nil && node.NamedChildCount() > 0 {
			name = node.NamedChild(0)
		}
		raw := nodeText(name, collector.source)
		if isComponentName(raw) {
			collector.addReference("jsx_reference", "jsx_reference:"+raw, scope.ownerLocalKey, raw, uci.TreeSitterResolutionSyntaxOnly, name)
		}
	}
}

func (collector *parserCollector) collectDefaultImportBinding(node *tree_sitter.Node, source string) {
	if source == "" {
		return
	}
	for index := uint(0); index < node.NamedChildCount(); index++ {
		binding := node.NamedChild(index)
		if binding == nil || binding.Kind() != "identifier" {
			continue
		}
		local := nodeText(binding, collector.source)
		if local == "" {
			return
		}
		target := source + "#default"
		collector.addReference("import_alias", "import:"+target+":"+local, "", target, uci.TreeSitterResolutionSyntaxOnly, binding)
		return
	}
}

func (collector *parserCollector) collectImportRequireBinding(node *tree_sitter.Node) {
	source := moduleSource(node.ChildByFieldName("source"), collector.source)
	if source == "" {
		return
	}
	collector.addReference("import", "import:"+source, "", source, uci.TreeSitterResolutionSyntaxOnly, node)
	for index := uint(0); index < node.NamedChildCount(); index++ {
		binding := node.NamedChild(index)
		if binding == nil || binding.Kind() != "identifier" {
			continue
		}
		local := nodeText(binding, collector.source)
		if local == "" {
			return
		}
		target := source + "#commonjs"
		collector.addReference("import_alias", "import:"+target+":"+local, "", target, uci.TreeSitterResolutionSyntaxOnly, binding)
		return
	}
}

func (collector *parserCollector) markDynamicImport(node *tree_sitter.Node) {
	if collector.dynamicImport {
		return
	}
	span, valid := collector.span(node)
	if !valid {
		collector.referencesTruncated = true
		return
	}
	collector.dynamicImport = true
	collector.dynamicImportSpan = span
}

func (collector *parserCollector) addReference(kind, localKey, ownerLocalKey, rawTarget string, resolution uci.IndexResolutionState, node *tree_sitter.Node) {
	if node == nil || localKey == "" || rawTarget == "" {
		return
	}
	span, valid := collector.span(node)
	if !valid {
		collector.referencesTruncated = true
		return
	}
	siteKey := uci.TreeSitterReferenceSiteKey(localKey, span)
	symbolKey := uci.TreeSitterReferenceSiteKey(string(collector.language)+":"+localKey, span)
	if _, exists := collector.referenceKeys[symbolKey]; exists {
		collector.referencesTruncated = true
		return
	}
	if len(collector.references) >= parserMaxReferences {
		collector.referencesTruncated = true
		return
	}
	collector.referenceKeys[symbolKey] = struct{}{}
	collector.references = append(collector.references, uci.TreeSitterReferenceSite{
		Kind:          kind,
		SymbolKey:     symbolKey,
		LocalKey:      siteKey,
		OwnerLocalKey: ownerLocalKey,
		RawTarget:     rawTarget,
		Resolution:    resolution,
		Span:          span,
	})
}

func (collector *parserCollector) span(node *tree_sitter.Node) (uci.IndexSpan, bool) {
	if node == nil {
		return uci.IndexSpan{}, false
	}
	return spanFromOffsets(collector.lineStarts, len(collector.source), int(node.StartByte()), int(node.EndByte()))
}

func definitionKind(kind string) string {
	switch kind {
	case "function_declaration", "generator_function_declaration":
		return "function"
	case "method_definition", "method_signature", "abstract_method_signature":
		return "method"
	case "class_declaration", "abstract_class_declaration":
		return "class"
	case "interface_declaration":
		return "interface"
	case "type_alias_declaration":
		return "type"
	case "enum_declaration":
		return "enum"
	case "internal_module", "module":
		return "namespace"
	default:
		return ""
	}
}

func isVariableDeclaration(kind string) bool {
	return kind == "lexical_declaration" || kind == "variable_declaration"
}

func variableKind(node *tree_sitter.Node, source []byte) string {
	if node == nil {
		return ""
	}
	text := strings.TrimSpace(nodeText(node, source))
	if text == "" {
		return ""
	}
	keyword := strings.Fields(text)[0]
	switch keyword {
	case "const", "let", "var":
		return keyword
	default:
		return ""
	}
}

func (collector *parserCollector) bindingNodes(node *tree_sitter.Node) []*tree_sitter.Node {
	if node == nil {
		return nil
	}
	switch node.Kind() {
	case "identifier", "shorthand_property_identifier_pattern":
		return []*tree_sitter.Node{node}
	case "assignment_pattern", "object_assignment_pattern":
		return collector.bindingNodes(node.ChildByFieldName("left"))
	case "pair_pattern":
		return collector.bindingNodes(node.ChildByFieldName("value"))
	case "array_pattern", "object_pattern", "rest_pattern":
		bindings := make([]*tree_sitter.Node, 0, node.NamedChildCount())
		for index := uint(0); index < node.NamedChildCount(); index++ {
			bindings = append(bindings, collector.bindingNodes(node.NamedChild(index))...)
		}
		return bindings
	default:
		return nil
	}
}

func advanceScope(scope parserScope, definition uci.TreeSitterDefinition) parserScope {
	switch definition.Kind {
	case "class", "interface", "enum", "namespace":
		scope.namespace = append(append([]string(nil), scope.namespace...), definition.Name)
	case "function", "method":
		scope.ownerLocalKey = definition.LocalKey
	}
	return scope
}

func qualified(namespace []string, name string) string {
	if len(namespace) == 0 {
		return name
	}
	return strings.Join(append(append([]string(nil), namespace...), name), ".")
}

func moduleSource(node *tree_sitter.Node, source []byte) string {
	raw := nodeText(node, source)
	if raw == "" {
		return ""
	}
	if value, err := strconv.Unquote(raw); err == nil {
		return value
	}
	return strings.Trim(raw, "\"'`")
}

func aliasNames(raw string) (string, string) {
	raw = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(raw), "type "))
	if raw == "" {
		return "", ""
	}
	if before, after, found := strings.Cut(raw, " as "); found {
		return strings.TrimSpace(before), strings.TrimSpace(after)
	}
	return raw, raw
}

func nodeText(node *tree_sitter.Node, source []byte) string {
	if node == nil {
		return ""
	}
	start, end := int(node.StartByte()), int(node.EndByte())
	if start < 0 || end < start || end > len(source) {
		return ""
	}
	return strings.TrimSpace(string(source[start:end]))
}

func isComponentName(name string) bool {
	if name == "" {
		return false
	}
	first, _ := utf8.DecodeRuneInString(name)
	return unicode.IsUpper(first)
}

func sourceChunks(source []byte, starts []int, definitions []uci.TreeSitterDefinition) ([]uci.TreeSitterChunk, bool) {
	if len(source) == 0 || !utf8.Valid(source) {
		return []uci.TreeSitterChunk{}, false
	}
	chunks := make([]uci.TreeSitterChunk, 0, min(parserMaxChunks, (len(source)+parserMaxChunkBytes-1)/parserMaxChunkBytes))
	start := 0
	for start < len(source) && len(chunks) < parserMaxChunks {
		end := start + parserMaxChunkBytes
		if end >= len(source) {
			end = len(source)
		} else {
			end = chunkBoundary(source, start, end, definitions)
		}
		if end <= start {
			return chunks, true
		}
		span, valid := spanFromOffsets(starts, len(source), start, end)
		if !valid {
			return chunks, true
		}
		contents := source[start:end]
		chunks = append(chunks, uci.TreeSitterChunk{
			Span:          span,
			Text:          string(contents),
			ContentDigest: sourceDigest(contents),
		})
		start = end
	}
	return chunks, start < len(source)
}

func chunkBoundary(source []byte, start, maximum int, definitions []uci.TreeSitterDefinition) int {
	boundary := 0
	minimumDefinitionEnd := start + parserMaxChunkBytes/2
	for _, definition := range definitions {
		end := int(definition.Span.ByteEnd)
		if end <= maximum && end >= minimumDefinitionEnd && end > boundary {
			boundary = end
		}
	}
	if boundary == 0 {
		boundary = maximum
		for index := maximum; index > start+parserMaxChunkBytes/2; index-- {
			if source[index-1] == '\n' {
				boundary = index
				break
			}
		}
	}
	for boundary > start && boundary < len(source) && !utf8.RuneStart(source[boundary]) {
		boundary--
	}
	return boundary
}

func safeText(source []byte, maximum int) (string, bool) {
	if !utf8.Valid(source) {
		return "", true
	}
	if len(source) <= maximum {
		return string(source), false
	}
	end := maximum
	for end > 0 && end < len(source) && !utf8.RuneStart(source[end]) {
		end--
	}
	return string(source[:end]), true
}

func lineStarts(source []byte) []int {
	starts := []int{0}
	for index, value := range source {
		if value == '\n' && index+1 < len(source) {
			starts = append(starts, index+1)
		}
	}
	return starts
}

func spanFromOffsets(starts []int, sourceLength, start, end int) (uci.IndexSpan, bool) {
	if start < 0 || end < start || end > sourceLength {
		return uci.IndexSpan{}, false
	}
	lineEndOffset := start
	if end > start {
		lineEndOffset = end - 1
	}
	return uci.IndexSpan{
		ByteStart: int64(start),
		ByteEnd:   int64(end),
		LineStart: lineForOffset(starts, start),
		LineEnd:   lineForOffset(starts, lineEndOffset),
	}, true
}

func lineForOffset(starts []int, offset int) int {
	return sort.Search(len(starts), func(index int) bool { return starts[index] > offset })
}

func sourceDigest(source []byte) uci.IndexDigest {
	sum := sha256.Sum256(source)
	return uci.IndexDigest("sha256:" + hex.EncodeToString(sum[:]))
}

func profileKeyValid(value string) bool {
	if value == "" || len(value) > parserMaxProfileBytes || !utf8.ValidString(value) || strings.TrimSpace(value) != value {
		return false
	}
	for _, character := range value {
		if character < 0x20 || character == 0x7f {
			return false
		}
	}
	return true
}

func addDiagnostic(response *uci.TreeSitterWorkerWireResponse, code string, span uci.IndexSpan, message string) {
	if response == nil || len(response.Diagnostics) >= parserMaxDiagnostics {
		return
	}
	message, _ = safeText([]byte(message), parserMaxDiagnosticBytes)
	response.Diagnostics = append(response.Diagnostics, uci.TreeSitterDiagnostic{
		Code:    code,
		Span:    span,
		Message: message,
	})
}

func sortResponse(response *uci.TreeSitterWorkerWireResponse) {
	if response == nil {
		return
	}
	sort.Slice(response.Definitions, func(left, right int) bool {
		if comparison := compareSpans(response.Definitions[left].Span, response.Definitions[right].Span); comparison != 0 {
			return comparison < 0
		}
		if response.Definitions[left].Kind != response.Definitions[right].Kind {
			return response.Definitions[left].Kind < response.Definitions[right].Kind
		}
		if response.Definitions[left].Name != response.Definitions[right].Name {
			return response.Definitions[left].Name < response.Definitions[right].Name
		}
		return response.Definitions[left].SymbolKey < response.Definitions[right].SymbolKey
	})
	sort.Slice(response.References, func(left, right int) bool {
		if comparison := compareSpans(response.References[left].Span, response.References[right].Span); comparison != 0 {
			return comparison < 0
		}
		if response.References[left].Kind != response.References[right].Kind {
			return response.References[left].Kind < response.References[right].Kind
		}
		return response.References[left].SymbolKey < response.References[right].SymbolKey
	})
	sort.Slice(response.Chunks, func(left, right int) bool {
		return compareSpans(response.Chunks[left].Span, response.Chunks[right].Span) < 0
	})
	sort.Slice(response.Diagnostics, func(left, right int) bool {
		if comparison := compareSpans(response.Diagnostics[left].Span, response.Diagnostics[right].Span); comparison != 0 {
			return comparison < 0
		}
		return response.Diagnostics[left].Code < response.Diagnostics[right].Code
	})
}

func compareSpans(left, right uci.IndexSpan) int {
	if left.ByteStart != right.ByteStart {
		if left.ByteStart < right.ByteStart {
			return -1
		}
		return 1
	}
	if left.ByteEnd != right.ByteEnd {
		if left.ByteEnd < right.ByteEnd {
			return -1
		}
		return 1
	}
	if left.LineStart != right.LineStart {
		if left.LineStart < right.LineStart {
			return -1
		}
		return 1
	}
	if left.LineEnd != right.LineEnd {
		if left.LineEnd < right.LineEnd {
			return -1
		}
		return 1
	}
	return 0
}
