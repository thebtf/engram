package uci

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"

	"gopkg.in/yaml.v3"
)

const (
	openAPIExtractionSchemaRevision = "uci-openapi-extraction/v1"
	openAPIExtractionParserRevision = "json-lexer/v1+gopkg.in/yaml.v3/v3.0.1+openapi-3-static/v1"

	openAPIExtractionMaxDefinitions       = 2_048
	openAPIExtractionMaxReferences        = 8_192
	openAPIExtractionMaxDiagnostics       = 16
	openAPIExtractionMaxOutputBytes       = 16 << 20
	openAPIExtractionMaxEntityKeyBytes    = 4 << 10
	openAPIExtractionMaxJSONRecoveryBytes = 128
	openAPIExtractionMaxDiagnosticBytes   = 512
)

// OpenAPIFormat is the caller-selected syntax family. Extraction never sniffs
// or upgrades a caller's format selection.
type OpenAPIFormat string

const (
	OpenAPIFormatJSON OpenAPIFormat = "json"
	OpenAPIFormatYAML OpenAPIFormat = "yaml"
)

// OpenAPIExtractionProfile identifies the versioned, caller-selected policy.
type OpenAPIExtractionProfile struct {
	ProfileKey string
	ParserKey  string
	Format     OpenAPIFormat
}

// OpenAPIArtifact is immutable evidence derived only from caller-owned OpenAPI source bytes.
type OpenAPIArtifact struct {
	Proof       IndexArtifactProof
	Coverage    IndexCoverageState
	Format      OpenAPIFormat
	Text        string
	Definitions []OpenAPIDefinition
	References  []OpenAPIReferenceSite
	Chunks      []OpenAPIChunk
	Diagnostics []OpenAPIDiagnostic
}

// OpenAPIDefinition records one statically identified OpenAPI 3.x entity.
type OpenAPIDefinition struct {
	Kind      string
	SymbolKey string
	LocalKey  string
	Span      IndexSpan
}

// OpenAPIReferenceSite records one local $ref site without dereferencing it.
type OpenAPIReferenceSite struct {
	Kind            string
	SymbolKey       string
	LocalKey        string
	TargetKey       string
	ResolutionState IndexResolutionState
	Span            IndexSpan
}

// OpenAPIChunk is a bounded source-text segment.
type OpenAPIChunk struct {
	Span          IndexSpan
	Text          string
	ContentDigest IndexDigest
}

// OpenAPIDiagnostic records a bounded extraction limitation or syntax error.
type OpenAPIDiagnostic struct {
	Code    string
	Span    IndexSpan
	Message string
}

// ExtractOpenAPI derives deterministic, checkout-independent structural evidence from one
// caller-owned OpenAPI JSON or YAML buffer. It never executes schema hooks, follows a
// reference, reads files, fetches URLs, or contacts a network.
func ExtractOpenAPI(source []byte, profile OpenAPIExtractionProfile) OpenAPIArtifact {
	base := ExtractJSONYAML(source, JSONYAMLExtractionProfile{
		ProfileKey: profile.ProfileKey,
		ParserKey:  profile.ParserKey,
		Format:     JSONYAMLFormat(profile.Format),
	})
	artifact := openAPIArtifactFromJSONYAML(base, profile.Format)
	if !openAPIExtractionProfileValid(profile) {
		return openAPIFinalizeArtifact(source, profile, artifact)
	}
	if !utf8.Valid(source) || len(source) > jsonYAMLExtractionMaxSourceBytes {
		return openAPIFinalizeArtifact(source, profile, artifact)
	}

	definitionsByPointer := openAPIGenericDefinitions(base)
	structure := openAPIStructureIndex{}
	structureParsed := false
	structureProblem := "parse_error"
	if profile.Format == OpenAPIFormatJSON && openAPIHasJSONYAMLDiagnostic(base, "PARSE_ERROR") {
		structure, structureParsed, structureProblem = openAPIRecoverJSONStructure(source)
	} else {
		structure, structureParsed, structureProblem = openAPIBuildStructure(source)
	}
	version, versionFound := openAPIVersionFromStructure(structure, structureParsed)
	if !versionFound {
		version, versionFound = openAPIVersionFromSource(source, profile.Format, definitionsByPointer)
	}
	if !versionFound || !openAPISupportedVersion(version) {
		if base.Coverage == IndexCoverageComplete {
			artifact.Coverage = IndexCoverageUnavailable
			code := "UNSUPPORTED_OPENAPI"
			message := "source is not a supported single-document OpenAPI 3.x structure"
			switch {
			case structureProblem == "multiple_documents":
				code = "UNSUPPORTED_DOCUMENT"
				message = "OpenAPI extraction supports one source document"
			case versionFound:
				code = "UNSUPPORTED_OPENAPI_VERSION"
				message = "OpenAPI version is not in the supported 3.x family"
			}
			openAPIAddDiagnosticOnce(&artifact, code, IndexSpan{}, message)
		}
		return openAPIFinalizeArtifact(source, profile, artifact)
	}

	collector := newOpenAPICollector(&artifact)
	var definitionStructure *openAPIStructureIndex
	if structureParsed && structure.singleDocument && structure.root != nil && structure.root.Kind == yaml.MappingNode {
		definitionStructure = &structure
	}
	semanticDefinitions := openAPIExtractDefinitions(base, version, definitionStructure, collector)
	if !structureParsed {
		collector.limit("OPENAPI_STRUCTURE_UNAVAILABLE", IndexSpan{}, "OpenAPI structure could not be parsed without executing source content")
	}
	if structureProblem == "multiple_documents" {
		collector.limit("UNSUPPORTED_DOCUMENT", IndexSpan{}, "OpenAPI extraction supports one source document")
	}
	if structureParsed && structure.root == nil {
		collector.limit("OPENAPI_STRUCTURE_UNAVAILABLE", IndexSpan{}, "OpenAPI parser produced no document root")
	}
	if structureParsed && structure.root != nil && structure.root.Kind != yaml.MappingNode {
		collector.limit("UNSUPPORTED_OPENAPI", IndexSpan{}, "OpenAPI root must be a mapping")
	}

	if structureParsed && structure.singleDocument && structure.root != nil && structure.root.Kind == yaml.MappingNode {
		openAPIExtractStructuralReferences(source, base, structure, semanticDefinitions, collector)
	} else {
		openAPIExtractFallbackReferences(base, definitionsByPointer, semanticDefinitions, collector)
	}

	if base.Coverage == IndexCoverageComplete && structureParsed && structure.singleDocument && structure.root != nil && structure.root.Kind == yaml.MappingNode && !structure.incomplete && !collector.incomplete {
		artifact.Coverage = IndexCoverageComplete
	} else {
		artifact.Coverage = IndexCoveragePartial
	}
	return openAPIFinalizeArtifact(source, profile, artifact)
}

func openAPIArtifactFromJSONYAML(base JSONYAMLArtifact, format OpenAPIFormat) OpenAPIArtifact {
	artifact := OpenAPIArtifact{
		Coverage: base.Coverage,
		Format:   format,
		Text:     base.Text,
	}
	if len(base.Chunks) != 0 {
		artifact.Chunks = make([]OpenAPIChunk, 0, len(base.Chunks))
		for _, chunk := range base.Chunks {
			artifact.Chunks = append(artifact.Chunks, OpenAPIChunk{
				Span:          chunk.Span,
				Text:          chunk.Text,
				ContentDigest: chunk.ContentDigest,
			})
		}
	}
	if len(base.Diagnostics) != 0 {
		artifact.Diagnostics = make([]OpenAPIDiagnostic, 0, len(base.Diagnostics))
		for _, diagnostic := range base.Diagnostics {
			artifact.Diagnostics = append(artifact.Diagnostics, OpenAPIDiagnostic{
				Code:    diagnostic.Code,
				Span:    diagnostic.Span,
				Message: diagnostic.Message,
			})
		}
	}
	return artifact
}

func openAPIHasJSONYAMLDiagnostic(artifact JSONYAMLArtifact, code string) bool {
	for _, diagnostic := range artifact.Diagnostics {
		if diagnostic.Code == code {
			return true
		}
	}
	return false
}

func openAPIExtractionProfileValid(profile OpenAPIExtractionProfile) bool {
	if !jsonYAMLExtractionProfileKeyValid(profile.ProfileKey) || !jsonYAMLExtractionProfileKeyValid(profile.ParserKey) {
		return false
	}
	switch profile.Format {
	case OpenAPIFormatJSON, OpenAPIFormatYAML:
		return true
	default:
		return false
	}
}

type openAPICollector struct {
	artifact      *OpenAPIArtifact
	outputBytes   int
	incomplete    bool
	outputLimited bool
}

func newOpenAPICollector(artifact *OpenAPIArtifact) *openAPICollector {
	collector := &openAPICollector{artifact: artifact}
	if artifact == nil {
		return collector
	}
	collector.outputBytes = len(artifact.Text)
	for _, chunk := range artifact.Chunks {
		collector.outputBytes += len(chunk.Text) + len(chunk.ContentDigest) + 32
	}
	return collector
}

func (collector *openAPICollector) addDefinition(definition OpenAPIDefinition) bool {
	if collector == nil || collector.artifact == nil {
		return false
	}
	if len(collector.artifact.Definitions) >= openAPIExtractionMaxDefinitions {
		collector.limit("DEFINITION_LIMIT", definition.Span, "OpenAPI definition output reached its bounded limit")
		return false
	}
	if !collector.reserve(len(definition.Kind) + len(definition.SymbolKey) + len(definition.LocalKey) + 32) {
		return false
	}
	collector.artifact.Definitions = append(collector.artifact.Definitions, definition)
	return true
}

func (collector *openAPICollector) addReference(reference OpenAPIReferenceSite) bool {
	if collector == nil || collector.artifact == nil {
		return false
	}
	if len(collector.artifact.References) >= openAPIExtractionMaxReferences {
		collector.limit("REFERENCE_LIMIT", reference.Span, "OpenAPI reference output reached its bounded limit")
		return false
	}
	if !collector.reserve(len(reference.Kind) + len(reference.SymbolKey) + len(reference.LocalKey) + len(reference.TargetKey) + len(reference.ResolutionState) + 40) {
		return false
	}
	collector.artifact.References = append(collector.artifact.References, reference)
	return true
}

func (collector *openAPICollector) reserve(size int) bool {
	if collector == nil {
		return false
	}
	if size < 0 || collector.outputBytes > openAPIExtractionMaxOutputBytes-size {
		collector.incomplete = true
		if !collector.outputLimited {
			collector.outputLimited = true
			openAPIAddDiagnosticOnce(collector.artifact, "OUTPUT_LIMIT", IndexSpan{}, "OpenAPI facts exceeded the bounded output limit")
		}
		return false
	}
	collector.outputBytes += size
	return true
}

func (collector *openAPICollector) limit(code string, span IndexSpan, message string) {
	if collector == nil {
		return
	}
	collector.incomplete = true
	openAPIAddDiagnosticOnce(collector.artifact, code, span, message)
}

func openAPIAddDiagnosticOnce(artifact *OpenAPIArtifact, code string, span IndexSpan, message string) {
	if artifact == nil || len(artifact.Diagnostics) >= openAPIExtractionMaxDiagnostics {
		return
	}
	for _, diagnostic := range artifact.Diagnostics {
		if diagnostic.Code == code {
			return
		}
	}
	message, _ = goSafeText([]byte(message), openAPIExtractionMaxDiagnosticBytes)
	artifact.Diagnostics = append(artifact.Diagnostics, OpenAPIDiagnostic{
		Code:    code,
		Span:    span,
		Message: message,
	})
}

func openAPIGenericDefinitions(base JSONYAMLArtifact) map[string]JSONYAMLDefinition {
	definitions := make(map[string]JSONYAMLDefinition, len(base.Definitions)+1)
	for _, definition := range base.Definitions {
		if definition.Document != 0 {
			continue
		}
		if definition.Kind == "document" {
			definitions["#"] = definition
			continue
		}
		if (definition.Kind == "key" || definition.Kind == "index") && strings.HasPrefix(definition.LocalKey, "#") {
			definitions[definition.LocalKey] = definition
		}
	}
	return definitions
}

func openAPIVersionFromStructure(structure openAPIStructureIndex, parsed bool) (string, bool) {
	if !parsed || !structure.singleDocument || structure.root == nil || structure.root.Kind != yaml.MappingNode || structure.pointerAmbiguous("#/openapi") {
		return "", false
	}
	node, exists := structure.nodes["#/openapi"]
	if !exists || node == nil || node.Kind != yaml.ScalarNode || openAPIHasCustomYAMLTag(node) {
		return "", false
	}
	return node.Value, true
}

func openAPIVersionFromSource(source []byte, format OpenAPIFormat, definitions map[string]JSONYAMLDefinition) (string, bool) {
	definition, exists := definitions["#/openapi"]
	if !exists || definition.Kind != "key" || definition.Span.ByteEnd < 0 || definition.Span.ByteEnd > int64(len(source)) {
		return "", false
	}
	start := int(definition.Span.ByteEnd)
	switch format {
	case OpenAPIFormatJSON:
		return openAPIJSONValueAfterKey(source, start)
	case OpenAPIFormatYAML:
		return openAPIYAMLValueAfterKey(source, start)
	default:
		return "", false
	}
}

func openAPIJSONValueAfterKey(source []byte, start int) (string, bool) {
	start = openAPISkipJSONWhitespace(source, start)
	if start >= len(source) || source[start] != ':' {
		return "", false
	}
	start = openAPISkipJSONWhitespace(source, start+1)
	if start >= len(source) || source[start] != '"' {
		return "", false
	}
	end, valid := openAPIJSONQuotedEnd(source, start)
	if !valid {
		return "", false
	}
	value, err := strconv.Unquote(string(source[start:end]))
	if err != nil {
		return "", false
	}
	return value, true
}

func openAPISkipJSONWhitespace(source []byte, start int) int {
	for start < len(source) {
		switch source[start] {
		case ' ', '\t', '\r', '\n':
			start++
		default:
			return start
		}
	}
	return start
}

func openAPIJSONQuotedEnd(source []byte, start int) (int, bool) {
	if start >= len(source) || source[start] != '"' {
		return 0, false
	}
	for offset := start + 1; offset < len(source); offset++ {
		switch source[offset] {
		case '\\':
			offset++
		case '"':
			return offset + 1, true
		}
	}
	return 0, false
}

func openAPIYAMLValueAfterKey(source []byte, start int) (string, bool) {
	for start < len(source) && (source[start] == ' ' || source[start] == '\t') {
		start++
	}
	if start >= len(source) || source[start] != ':' {
		return "", false
	}
	start++
	lineEnd := jsonYAMLLineEnd(source, start)
	for start < lineEnd && (source[start] == ' ' || source[start] == '\t') {
		start++
	}
	if start >= lineEnd {
		return "", false
	}

	switch source[start] {
	case '"':
		end := jsonYAMLDoubleQuotedEnd(source, start, lineEnd)
		if end <= start || end > lineEnd {
			return "", false
		}
		value, err := strconv.Unquote(string(source[start:end]))
		if err != nil {
			return "", false
		}
		return value, true
	case '\'':
		end := jsonYAMLSingleQuotedEnd(source, start, lineEnd)
		if end <= start+1 || end > lineEnd || source[end-1] != '\'' {
			return "", false
		}
		return strings.ReplaceAll(string(source[start+1:end-1]), "''", "'"), true
	default:
		end := start
		for end < lineEnd {
			value := source[end]
			if value == ',' || value == '}' || value == ']' {
				break
			}
			if value == '#' && (end == start || source[end-1] == ' ' || source[end-1] == '\t') {
				break
			}
			end++
		}
		value := strings.TrimSpace(string(source[start:end]))
		return value, value != ""
	}
}

func openAPISupportedVersion(version string) bool {
	if len(version) < 3 || version[0] != '3' || version[1] != '.' || version[2] < '0' || version[2] > '9' {
		return false
	}
	for _, value := range version {
		if (value < '0' || value > '9') && value != '.' && value != '-' && value != '+' {
			return false
		}
	}
	return true
}

func openAPIExtractDefinitions(base JSONYAMLArtifact, version string, structure *openAPIStructureIndex, collector *openAPICollector) map[string]string {
	definitions := make(map[string]string)
	for _, generic := range base.Definitions {
		if generic.Document != 0 || (generic.Kind != "key" && generic.Kind != "index") {
			continue
		}
		kind, localKey, valid := openAPIClassifyDefinition(generic, version)
		if !valid {
			continue
		}
		if _, exists := definitions[generic.LocalKey]; exists {
			continue
		}
		if len(localKey) > openAPIExtractionMaxEntityKeyBytes {
			collector.limit("ENTITY_KEY_LIMIT", generic.Span, "OpenAPI entity key exceeded the bounded extraction limit")
			continue
		}
		span := generic.Span
		if kind == "parameter" && strings.HasPrefix(localKey, "parameter:#/paths/") {
			parameterSpan, valid := openAPIInlineParameterSpan(generic.LocalKey, structure)
			if !valid {
				collector.limit("PARAMETER_NAME_UNAVAILABLE", generic.Span, "inline parameter name span could not be represented")
			} else {
				span = parameterSpan
			}
		}
		symbolKey := openAPIEntitySymbol(generic.LocalKey)
		if !collector.addDefinition(OpenAPIDefinition{
			Kind:      kind,
			SymbolKey: symbolKey,
			LocalKey:  localKey,
			Span:      span,
		}) {
			continue
		}
		definitions[generic.LocalKey] = symbolKey
	}
	return definitions
}

func openAPIInlineParameterSpan(pointer string, structure *openAPIStructureIndex) (IndexSpan, bool) {
	if structure == nil || structure.pointerAmbiguous(pointer+"/name") {
		return IndexSpan{}, false
	}
	node, exists := structure.nodes[pointer+"/name"]
	if !exists || node == nil {
		return IndexSpan{}, false
	}
	span := structure.nodeSpan(node)
	return span, span.ByteEnd > span.ByteStart
}

func openAPIClassifyDefinition(definition JSONYAMLDefinition, version string) (string, string, bool) {
	segments, valid := openAPIPointerSegments(definition.LocalKey)
	if !valid {
		return "", "", false
	}
	switch {
	case len(segments) == 1 && segments[0] == "openapi" && definition.Kind == "key":
		return "version", "version:" + version, true
	case len(segments) == 1 && segments[0] == "info" && definition.Kind == "key":
		return "info", "info", true
	case len(segments) == 2 && segments[0] == "paths" && definition.Kind == "key":
		return "path", "path:" + segments[1], true
	case len(segments) == 3 && segments[0] == "paths" && openAPIHTTPMethod(segments[2]) && definition.Kind == "key":
		return "operation", "operation:" + segments[2] + ":" + segments[1], true
	case len(segments) == 3 && segments[0] == "components" && segments[1] == "parameters" && definition.Kind == "key":
		return "parameter", "parameter:" + segments[2], true
	case len(segments) == 3 && segments[0] == "components" && segments[1] == "schemas" && definition.Kind == "key":
		return "schema", "schema:" + segments[2], true
	case len(segments) == 4 && segments[0] == "paths" && segments[2] == "parameters" && definition.Kind == "index":
		return "parameter", "parameter:" + definition.LocalKey, true
	case len(segments) == 5 && segments[0] == "paths" && openAPIHTTPMethod(segments[2]) && segments[3] == "parameters" && definition.Kind == "index":
		return "parameter", "parameter:" + definition.LocalKey, true
	default:
		return "", "", false
	}
}

func openAPIHTTPMethod(value string) bool {
	switch value {
	case "delete", "get", "head", "options", "patch", "post", "put", "trace":
		return true
	default:
		return false
	}
}

func openAPIEntitySymbol(pointer string) string {
	return "openapi:" + pointer
}

type openAPIStructureIndex struct {
	root           *yaml.Node
	nodes          map[string]*yaml.Node
	ambiguous      map[string]struct{}
	references     []openAPIStructuralReference
	nodesSeen      int
	incomplete     bool
	limitReached   bool
	singleDocument bool
	source         []byte
	lineStarts     []int
}

type openAPIStructuralReference struct {
	localKey  string
	rawTarget string
	span      IndexSpan
}

func openAPIBuildStructure(source []byte) (openAPIStructureIndex, bool, string) {
	index := openAPIStructureIndex{
		nodes:      make(map[string]*yaml.Node),
		ambiguous:  make(map[string]struct{}),
		source:     source,
		lineStarts: jsonYAMLLineStarts(source),
	}
	documents, limited, err := jsonYAMLDecodeYAMLDocuments(source)
	if err != nil {
		return index, false, "parse_error"
	}
	if limited || len(documents) != 1 {
		return index, true, "multiple_documents"
	}

	root := &documents[0]
	if root.Kind == yaml.DocumentNode {
		if len(root.Content) != 1 || root.Content[0] == nil {
			return index, true, "unsupported_document"
		}
		root = root.Content[0]
	}
	index.singleDocument = true
	index.root = root
	if root == nil || root.Kind != yaml.MappingNode {
		return index, true, "unsupported_root"
	}
	index.nodes["#"] = root
	index.walk(root, "#", 0, false)
	return index, true, ""
}

func openAPIRecoverJSONStructure(source []byte) (openAPIStructureIndex, bool, string) {
	lowerBound := len(source) - openAPIExtractionMaxJSONRecoveryBytes
	if lowerBound < 0 {
		lowerBound = 0
	}
	for end := len(source); end >= lowerBound; end-- {
		if !json.Valid(source[:end]) {
			continue
		}
		structure, parsed, problem := openAPIBuildStructure(source[:end])
		if parsed {
			return structure, true, problem
		}
	}
	return openAPIStructureIndex{}, false, "parse_error"
}

func (index *openAPIStructureIndex) walk(node *yaml.Node, pointer string, depth int, inheritedAmbiguous bool) {
	if index == nil || index.limitReached || node == nil {
		return
	}
	if depth > jsonYAMLExtractionMaxDepth {
		index.incomplete = true
		index.limitReached = true
		return
	}
	if index.nodesSeen >= jsonYAMLExtractionMaxNodes {
		index.incomplete = true
		index.limitReached = true
		return
	}
	index.nodesSeen++
	if openAPIHasCustomYAMLTag(node) {
		index.incomplete = true
	}

	switch node.Kind {
	case yaml.MappingNode:
		index.walkMapping(node, pointer, depth, inheritedAmbiguous)
	case yaml.SequenceNode:
		for position, child := range node.Content {
			childPointer, valid := openAPIChildPointer(pointer, strconv.Itoa(position))
			if !valid {
				index.incomplete = true
				continue
			}
			if !inheritedAmbiguous && child != nil {
				index.nodes[childPointer] = child
			}
			index.walk(child, childPointer, depth+1, inheritedAmbiguous)
		}
	case yaml.AliasNode:
		index.incomplete = true
	case yaml.ScalarNode:
		// Scalar values are represented by their containing structural pointer.
	default:
		index.incomplete = true
	}
}

func (index *openAPIStructureIndex) walkMapping(node *yaml.Node, pointer string, depth int, inheritedAmbiguous bool) {
	if index == nil || node == nil {
		return
	}
	if len(node.Content)%2 != 0 {
		index.incomplete = true
	}
	type entry struct {
		key          *yaml.Node
		value        *yaml.Node
		childPointer string
		valid        bool
	}
	entries := make([]entry, 0, len(node.Content)/2)
	counts := make(map[string]int, len(node.Content)/2)
	for position := 0; position+1 < len(node.Content); position += 2 {
		key := node.Content[position]
		value := node.Content[position+1]
		if key == nil || key.Kind != yaml.ScalarNode {
			index.incomplete = true
			entries = append(entries, entry{key: key, value: value})
			continue
		}
		childPointer, valid := openAPIChildPointer(pointer, key.Value)
		if !valid {
			index.incomplete = true
		}
		if valid {
			counts[key.Value]++
		}
		entries = append(entries, entry{key: key, value: value, childPointer: childPointer, valid: valid})
	}

	for _, entry := range entries {
		if !entry.valid {
			index.walk(entry.value, "#", depth+1, true)
			continue
		}
		ambiguous := inheritedAmbiguous || counts[entry.key.Value] > 1
		if counts[entry.key.Value] > 1 {
			index.ambiguous[entry.childPointer] = struct{}{}
		}
		if !ambiguous && entry.value != nil {
			index.nodes[entry.childPointer] = entry.value
		}
		if entry.key.Value == "$ref" && entry.value != nil && entry.value.Kind == yaml.ScalarNode {
			index.references = append(index.references, openAPIStructuralReference{
				localKey:  entry.childPointer,
				rawTarget: entry.value.Value,
				span:      index.nodeSpan(entry.value),
			})
		}
		index.walk(entry.value, entry.childPointer, depth+1, ambiguous)
	}
}

func (index *openAPIStructureIndex) nodeSpan(node *yaml.Node) IndexSpan {
	if index == nil || node == nil {
		return IndexSpan{}
	}
	start, valid := jsonYAMLYAMLPositionOffset(index.source, index.lineStarts, node.Line, node.Column)
	if !valid {
		return IndexSpan{}
	}
	end := jsonYAMLYAMLNodeEnd(index.source, start, node, false)
	span, valid := goSpanFromOffsets(index.lineStarts, len(index.source), start, end)
	if !valid {
		return IndexSpan{}
	}
	return span
}

func (index openAPIStructureIndex) pointerAmbiguous(pointer string) bool {
	segments, valid := openAPIPointerSegments(pointer)
	if !valid {
		return false
	}
	current := "#"
	for _, segment := range segments {
		next, valid := openAPIChildPointer(current, segment)
		if !valid {
			return false
		}
		current = next
		if _, ambiguous := index.ambiguous[current]; ambiguous {
			return true
		}
	}
	return false
}

func openAPIHasCustomYAMLTag(node *yaml.Node) bool {
	return node != nil && strings.HasPrefix(node.Tag, "!") && !strings.HasPrefix(node.Tag, "!!")
}

func openAPIChildPointer(parent, segment string) (string, bool) {
	if parent == "" || parent[0] != '#' {
		return "", false
	}
	child, valid := jsonYAMLPointerAppend(parent[1:], segment)
	if !valid {
		return "", false
	}
	return "#" + child, true
}

func openAPIExtractStructuralReferences(source []byte, base JSONYAMLArtifact, structure openAPIStructureIndex, semanticDefinitions map[string]string, collector *openAPICollector) {
	genericSpans := openAPIGenericReferenceSpans(base)
	for _, reference := range structure.references {
		span := reference.span
		if genericSpan, exists := genericSpans[reference.localKey]; exists {
			span = genericSpan
		}
		if openAPIPartialJSONComponentSchemaAmbiguous(source, base, structure, reference.rawTarget) {
			openAPIAddAmbiguousReference(reference.localKey, span, collector)
			continue
		}
		openAPIResolveReference(reference.localKey, reference.rawTarget, span, &structure, semanticDefinitions, collector)
	}
}

func openAPIExtractFallbackReferences(base JSONYAMLArtifact, definitions map[string]JSONYAMLDefinition, semanticDefinitions map[string]string, collector *openAPICollector) {
	for _, reference := range base.References {
		if reference.Document != 0 || reference.Kind != "pointer" {
			continue
		}
		targetPointer, valid := openAPIPointerFromGenericTarget(reference.TargetKey, OpenAPIFormat(base.Format))
		if !valid {
			collector.limit("PARTIAL_LOCAL_REFERENCE", reference.Span, "local reference target could not be represented after partial parsing")
			collector.addReference(OpenAPIReferenceSite{
				Kind:            "local_ref",
				SymbolKey:       openAPIReferenceSymbol(reference.LocalKey, reference.Span),
				LocalKey:        reference.LocalKey,
				ResolutionState: IndexResolutionState("partial"),
				Span:            reference.Span,
			})
			continue
		}
		if _, exists := definitions[targetPointer]; !exists {
			collector.limit("PARTIAL_LOCAL_REFERENCE", reference.Span, "local reference target cannot be proven absent after partial parsing")
			collector.addReference(OpenAPIReferenceSite{
				Kind:            "local_ref",
				SymbolKey:       openAPIReferenceSymbol(reference.LocalKey, reference.Span),
				LocalKey:        reference.LocalKey,
				ResolutionState: IndexResolutionState("partial"),
				Span:            reference.Span,
			})
			continue
		}
		targetKey := semanticDefinitions[targetPointer]
		if targetKey == "" {
			targetKey = openAPIEntitySymbol(targetPointer)
		}
		collector.addReference(OpenAPIReferenceSite{
			Kind:            "local_ref",
			SymbolKey:       openAPIReferenceSymbol(reference.LocalKey, reference.Span),
			LocalKey:        reference.LocalKey,
			TargetKey:       targetKey,
			ResolutionState: IndexResolutionState("resolved"),
			Span:            reference.Span,
		})
	}
}

func openAPIGenericReferenceSpans(base JSONYAMLArtifact) map[string]IndexSpan {
	spans := make(map[string]IndexSpan, len(base.References))
	for _, reference := range base.References {
		if reference.Document == 0 && reference.Kind == "pointer" {
			spans[reference.LocalKey] = reference.Span
		}
	}
	return spans
}

// openAPIPartialJSONComponentSchemaAmbiguous retains a bounded ambiguity fact
// when a malformed JSON suffix follows an otherwise complete object. It only
// recognizes duplicate components.schemas keys in the caller-owned bytes; it
// never reads or resolves another document.
func openAPIPartialJSONComponentSchemaAmbiguous(source []byte, base JSONYAMLArtifact, structure openAPIStructureIndex, rawTarget string) bool {
	if base.Format != JSONYAMLFormatJSON || !openAPIHasJSONYAMLDiagnostic(base, "PARSE_ERROR") {
		return false
	}
	targetPointer, valid := openAPICanonicalPointer(rawTarget)
	if !valid || structure.pointerAmbiguous(targetPointer) {
		return false
	}
	if _, exists := structure.nodes[targetPointer]; exists {
		return false
	}
	segments, valid := openAPIPointerSegments(targetPointer)
	if !valid || len(segments) != 3 || segments[0] != "components" || segments[1] != "schemas" {
		return false
	}
	return openAPIJSONMappingKeyCount(source, segments[2]) > 1
}

func openAPIJSONMappingKeyCount(source []byte, key string) int {
	count := 0
	for offset := 0; offset < len(source); {
		if source[offset] != '"' {
			offset++
			continue
		}
		end, valid := openAPIJSONQuotedEnd(source, offset)
		if !valid {
			return count
		}
		decoded, err := strconv.Unquote(string(source[offset:end]))
		next := openAPISkipJSONWhitespace(source, end)
		if err == nil && decoded == key && next < len(source) && source[next] == ':' {
			count++
			if count > 1 {
				return count
			}
		}
		offset = end
	}
	return count
}

func openAPIPointerFromGenericTarget(target string, format OpenAPIFormat) (string, bool) {
	documentKey := jsonYAMLDocumentKey(JSONYAMLFormat(format), 0)
	if target == documentKey {
		return "#", true
	}
	if !strings.HasPrefix(target, documentKey+"#") {
		return "", false
	}
	return openAPICanonicalPointer(target[len(documentKey):])
}

func openAPIResolveReference(localKey, rawTarget string, span IndexSpan, structure *openAPIStructureIndex, semanticDefinitions map[string]string, collector *openAPICollector) {
	if !strings.HasPrefix(rawTarget, "#") {
		openAPIAddDiagnosticOnce(collector.artifact, "EXTERNAL_REFERENCE", span, "reference is not a same-document JSON pointer and was not resolved")
		collector.incomplete = true
		return
	}
	targetPointer, valid := openAPICanonicalPointer(rawTarget)
	if !valid {
		collector.limit("BROKEN_LOCAL_REFERENCE", span, "local JSON pointer is syntactically invalid and was not resolved")
		collector.addReference(OpenAPIReferenceSite{
			Kind:            "local_ref",
			SymbolKey:       openAPIReferenceSymbol(localKey, span),
			LocalKey:        localKey,
			ResolutionState: IndexResolutionState("partial"),
			Span:            span,
		})
		return
	}
	if structure != nil && structure.pointerAmbiguous(targetPointer) {
		openAPIAddAmbiguousReference(localKey, span, collector)
		return
	}
	if structure == nil {
		collector.limit("PARTIAL_LOCAL_REFERENCE", span, "local reference target cannot be checked without a parsed document structure")
		collector.addReference(OpenAPIReferenceSite{
			Kind:            "local_ref",
			SymbolKey:       openAPIReferenceSymbol(localKey, span),
			LocalKey:        localKey,
			ResolutionState: IndexResolutionState("partial"),
			Span:            span,
		})
		return
	}
	if _, exists := structure.nodes[targetPointer]; exists {
		targetKey := semanticDefinitions[targetPointer]
		if targetKey == "" {
			targetKey = openAPIEntitySymbol(targetPointer)
		}
		collector.addReference(OpenAPIReferenceSite{
			Kind:            "local_ref",
			SymbolKey:       openAPIReferenceSymbol(localKey, span),
			LocalKey:        localKey,
			TargetKey:       targetKey,
			ResolutionState: IndexResolutionState("resolved"),
			Span:            span,
		})
		return
	}
	if structure.incomplete {
		collector.limit("PARTIAL_LOCAL_REFERENCE", span, "local reference target cannot be proven absent within structural extraction bounds")
		collector.addReference(OpenAPIReferenceSite{
			Kind:            "local_ref",
			SymbolKey:       openAPIReferenceSymbol(localKey, span),
			LocalKey:        localKey,
			ResolutionState: IndexResolutionState("partial"),
			Span:            span,
		})
		return
	}
	collector.limit("MISSING_LOCAL_REFERENCE", span, "local JSON pointer does not exist in this source document")
	collector.addReference(OpenAPIReferenceSite{
		Kind:            "local_ref",
		SymbolKey:       openAPIReferenceSymbol(localKey, span),
		LocalKey:        localKey,
		ResolutionState: IndexResolutionState("unresolved"),
		Span:            span,
	})
}

func openAPIAddAmbiguousReference(localKey string, span IndexSpan, collector *openAPICollector) {
	collector.limit("AMBIGUOUS_LOCAL_REFERENCE", span, "local JSON pointer targets an ambiguous mapping key")
	collector.addReference(OpenAPIReferenceSite{
		Kind:            "local_ref",
		SymbolKey:       openAPIReferenceSymbol(localKey, span),
		LocalKey:        localKey,
		ResolutionState: IndexResolutionState("ambiguous"),
		Span:            span,
	})
}

func openAPIReferenceSymbol(localKey string, span IndexSpan) string {
	return "openapi:ref:" + localKey + "@" + strconv.FormatInt(span.ByteStart, 10)
}

func openAPICanonicalPointer(pointer string) (string, bool) {
	segments, valid := openAPIPointerSegments(pointer)
	if !valid {
		return "", false
	}
	canonical := "#"
	for _, segment := range segments {
		next, valid := openAPIChildPointer(canonical, segment)
		if !valid {
			return "", false
		}
		canonical = next
	}
	return canonical, true
}

func openAPIPointerSegments(pointer string) ([]string, bool) {
	if pointer == "#" {
		return nil, true
	}
	if !strings.HasPrefix(pointer, "#/") {
		return nil, false
	}
	raw := strings.Split(pointer[2:], "/")
	segments := make([]string, len(raw))
	for index, value := range raw {
		decoded, valid := openAPIUnescapePointerSegment(value)
		if !valid {
			return nil, false
		}
		segments[index] = decoded
	}
	return segments, true
}

func openAPIUnescapePointerSegment(value string) (string, bool) {
	if !strings.Contains(value, "~") {
		return value, true
	}
	var builder strings.Builder
	builder.Grow(len(value))
	for index := 0; index < len(value); index++ {
		if value[index] != '~' {
			builder.WriteByte(value[index])
			continue
		}
		if index+1 >= len(value) {
			return "", false
		}
		switch value[index+1] {
		case '0':
			builder.WriteByte('~')
		case '1':
			builder.WriteByte('/')
		default:
			return "", false
		}
		index++
	}
	return builder.String(), true
}

func openAPIFinalizeArtifact(source []byte, profile OpenAPIExtractionProfile, artifact OpenAPIArtifact) OpenAPIArtifact {
	sort.Slice(artifact.Definitions, func(left, right int) bool {
		return openAPIDefinitionLess(artifact.Definitions[left], artifact.Definitions[right])
	})
	sort.Slice(artifact.References, func(left, right int) bool {
		return openAPIReferenceLess(artifact.References[left], artifact.References[right])
	})
	sort.Slice(artifact.Chunks, func(left, right int) bool {
		return openAPIChunkLess(artifact.Chunks[left], artifact.Chunks[right])
	})
	sort.Slice(artifact.Diagnostics, func(left, right int) bool {
		return openAPIDiagnosticLess(artifact.Diagnostics[left], artifact.Diagnostics[right])
	})

	contentDigest := goSourceDigest(source)
	artifact.Proof = IndexArtifactProof{
		ArtifactID:         openAPIArtifactID(contentDigest, profile),
		ContentDigest:      contentDigest,
		FactsDigest:        openAPIFactsDigest(profile, artifact),
		DefinitionCount:    uint64(len(artifact.Definitions)),
		ReferenceSiteCount: uint64(len(artifact.References)),
		ChunkCount:         uint64(len(artifact.Chunks)),
	}
	return artifact
}

func openAPIArtifactID(contentDigest IndexDigest, profile OpenAPIExtractionProfile) string {
	state := sha256.New()
	goWriteHashString(state, "uci-openapi-artifact-id/v1")
	goWriteHashString(state, string(contentDigest))
	goWriteHashString(state, profile.ProfileKey)
	goWriteHashString(state, profile.ParserKey)
	goWriteHashString(state, string(profile.Format))
	goWriteHashString(state, openAPIExtractionParserRevision)

	sum := state.Sum(nil)
	var identifier [16]byte
	copy(identifier[:], sum[:len(identifier)])
	identifier[6] = identifier[6]&0x0f | 0x50
	identifier[8] = identifier[8]&0x3f | 0x80
	encoded := hex.EncodeToString(identifier[:])
	return encoded[0:8] + "-" + encoded[8:12] + "-" + encoded[12:16] + "-" + encoded[16:20] + "-" + encoded[20:]
}

func openAPIFactsDigest(profile OpenAPIExtractionProfile, artifact OpenAPIArtifact) IndexDigest {
	state := sha256.New()
	goWriteHashString(state, "uci-openapi-facts/v1")
	goWriteHashString(state, openAPIExtractionSchemaRevision)
	goWriteHashString(state, openAPIExtractionParserRevision)
	goWriteHashString(state, profile.ProfileKey)
	goWriteHashString(state, profile.ParserKey)
	goWriteHashString(state, string(profile.Format))
	goWriteHashString(state, string(artifact.Coverage))
	goWriteHashString(state, string(artifact.Format))
	goWriteHashString(state, artifact.Text)

	goWriteHashUint64(state, uint64(len(artifact.Definitions)))
	for _, definition := range artifact.Definitions {
		goWriteHashString(state, definition.Kind)
		goWriteHashString(state, definition.SymbolKey)
		goWriteHashString(state, definition.LocalKey)
		goWriteHashSpan(state, definition.Span)
	}

	goWriteHashUint64(state, uint64(len(artifact.References)))
	for _, reference := range artifact.References {
		goWriteHashString(state, reference.Kind)
		goWriteHashString(state, reference.SymbolKey)
		goWriteHashString(state, reference.LocalKey)
		goWriteHashString(state, reference.TargetKey)
		goWriteHashString(state, string(reference.ResolutionState))
		goWriteHashSpan(state, reference.Span)
	}

	goWriteHashUint64(state, uint64(len(artifact.Chunks)))
	for _, chunk := range artifact.Chunks {
		goWriteHashSpan(state, chunk.Span)
		goWriteHashString(state, chunk.Text)
		goWriteHashString(state, string(chunk.ContentDigest))
	}

	goWriteHashUint64(state, uint64(len(artifact.Diagnostics)))
	for _, diagnostic := range artifact.Diagnostics {
		goWriteHashString(state, diagnostic.Code)
		goWriteHashSpan(state, diagnostic.Span)
		goWriteHashString(state, diagnostic.Message)
	}

	var sum [sha256.Size]byte
	copy(sum[:], state.Sum(nil))
	return goDigestFromSum(sum)
}

func openAPIDefinitionLess(left, right OpenAPIDefinition) bool {
	if comparison := goCompareSpans(left.Span, right.Span); comparison != 0 {
		return comparison < 0
	}
	if left.Kind != right.Kind {
		return left.Kind < right.Kind
	}
	if left.SymbolKey != right.SymbolKey {
		return left.SymbolKey < right.SymbolKey
	}
	return left.LocalKey < right.LocalKey
}

func openAPIReferenceLess(left, right OpenAPIReferenceSite) bool {
	if comparison := goCompareSpans(left.Span, right.Span); comparison != 0 {
		return comparison < 0
	}
	if left.Kind != right.Kind {
		return left.Kind < right.Kind
	}
	if left.SymbolKey != right.SymbolKey {
		return left.SymbolKey < right.SymbolKey
	}
	if left.LocalKey != right.LocalKey {
		return left.LocalKey < right.LocalKey
	}
	if left.TargetKey != right.TargetKey {
		return left.TargetKey < right.TargetKey
	}
	return left.ResolutionState < right.ResolutionState
}

func openAPIChunkLess(left, right OpenAPIChunk) bool {
	if comparison := goCompareSpans(left.Span, right.Span); comparison != 0 {
		return comparison < 0
	}
	if left.ContentDigest != right.ContentDigest {
		return left.ContentDigest < right.ContentDigest
	}
	return left.Text < right.Text
}

func openAPIDiagnosticLess(left, right OpenAPIDiagnostic) bool {
	if comparison := goCompareSpans(left.Span, right.Span); comparison != 0 {
		return comparison < 0
	}
	if left.Code != right.Code {
		return left.Code < right.Code
	}
	return left.Message < right.Message
}
