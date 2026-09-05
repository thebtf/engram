package uci

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"

	"gopkg.in/yaml.v3"
)

const (
	jsonYAMLExtractionSchemaRevision = "uci-json-yaml-extraction/v1"
	jsonYAMLExtractionParserRevision = "json-lexer/v1+gopkg.in/yaml.v3/v3.0.1"

	jsonYAMLExtractionMaxSourceBytes       = 4 << 20
	jsonYAMLExtractionMaxArtifactTextBytes = jsonYAMLExtractionMaxSourceBytes
	jsonYAMLExtractionMaxChunkBytes        = 64 << 10
	jsonYAMLExtractionMaxChunks            = 64
	jsonYAMLExtractionMaxDocuments         = 128
	jsonYAMLExtractionMaxDepth             = 64
	jsonYAMLExtractionMaxNodes             = 16_384
	jsonYAMLExtractionMaxDefinitions       = 2_048
	jsonYAMLExtractionMaxReferences        = 8_192
	jsonYAMLExtractionMaxDiagnostics       = 16
	jsonYAMLExtractionMaxProfileBytes      = 256
	jsonYAMLExtractionMaxDiagnosticBytes   = 512
	jsonYAMLExtractionMaxPointerBytes      = 4 << 10
	jsonYAMLExtractionMaxAnchorBytes       = 512
	jsonYAMLExtractionMaxOutputBytes       = 16 << 20
	jsonYAMLExtractionMaxRecoveryLines     = 128
)

// JSONYAMLFormat is the caller-selected syntax family. Extraction never sniffs
// or upgrades a caller's format selection.
type JSONYAMLFormat string

const (
	JSONYAMLFormatJSON JSONYAMLFormat = "json"
	JSONYAMLFormatYAML JSONYAMLFormat = "yaml"
)

// JSONYAMLExtractionProfile identifies the versioned, caller-selected policy.
type JSONYAMLExtractionProfile struct {
	ProfileKey string
	ParserKey  string
	Format     JSONYAMLFormat
}

// JSONYAMLArtifact is immutable evidence derived only from caller-owned bytes.
type JSONYAMLArtifact struct {
	Proof       IndexArtifactProof
	Coverage    IndexCoverageState
	Format      JSONYAMLFormat
	Text        string
	Definitions []JSONYAMLDefinition
	References  []JSONYAMLReferenceSite
	Chunks      []JSONYAMLChunk
	Diagnostics []JSONYAMLDiagnostic
}

// JSONYAMLDefinition records a document, key, sequence index, or YAML anchor.
type JSONYAMLDefinition struct {
	Kind      string
	SymbolKey string
	LocalKey  string
	Document  int
	Span      IndexSpan
}

// JSONYAMLReferenceSite records a local JSON pointer or YAML alias without
// dereferencing it.
type JSONYAMLReferenceSite struct {
	Kind      string
	SymbolKey string
	LocalKey  string
	TargetKey string
	Document  int
	Span      IndexSpan
}

// JSONYAMLChunk is a bounded source-text segment.
type JSONYAMLChunk struct {
	Span          IndexSpan
	Text          string
	ContentDigest IndexDigest
}

// JSONYAMLDiagnostic records a bounded limitation or syntax error.
type JSONYAMLDiagnostic struct {
	Code    string
	Span    IndexSpan
	Message string
}

// ExtractJSONYAML derives deterministic, checkout-independent evidence from
// one caller-owned JSON or YAML buffer. It does not resolve references, read
// files, fetch URLs, or execute tags or schema hooks.
func ExtractJSONYAML(source []byte, profile JSONYAMLExtractionProfile) JSONYAMLArtifact {
	lineStarts := jsonYAMLLineStarts(source)
	sourceUTF8Valid := utf8.Valid(source)
	retainedLength := jsonYAMLRetainedLength(source, sourceUTF8Valid)
	textTruncated := retainedLength < len(source)
	chunks, chunksTruncated := jsonYAMLSourceChunks(source, lineStarts, retainedLength, sourceUTF8Valid)
	artifact := JSONYAMLArtifact{
		Coverage: IndexCoveragePartial,
		Format:   profile.Format,
		Text:     string(source[:retainedLength]),
		Chunks:   chunks,
	}
	collector := newJSONYAMLCollector(&artifact)

	if textTruncated {
		collector.limit("TEXT_LIMIT", IndexSpan{}, "source text exceeded the bounded extraction text limit")
	}
	if chunksTruncated {
		collector.limit("CHUNK_LIMIT", IndexSpan{}, "source chunks exceeded the bounded extraction chunk limit")
	}
	if !sourceUTF8Valid {
		collector.limit("INVALID_UTF8", IndexSpan{}, "source contains invalid UTF-8")
		return jsonYAMLFinalizeArtifact(source, profile, artifact)
	}
	if len(source) > jsonYAMLExtractionMaxSourceBytes {
		collector.limit("SOURCE_LIMIT", IndexSpan{}, "source exceeded the bounded parser input limit")
		return jsonYAMLFinalizeArtifact(source, profile, artifact)
	}
	if !jsonYAMLExtractionProfileValid(profile) {
		collector.limit("INVALID_PROFILE", IndexSpan{}, "profile keys must be bounded, valid UTF-8, nonempty, and select JSON or YAML")
		return jsonYAMLFinalizeArtifact(source, profile, artifact)
	}

	parsedCompletely := false
	switch profile.Format {
	case JSONYAMLFormatJSON:
		parsedCompletely = jsonYAMLExtractJSON(source, lineStarts, profile, collector)
	case JSONYAMLFormatYAML:
		parsedCompletely = jsonYAMLExtractYAML(source, lineStarts, profile, collector)
	}
	if parsedCompletely && !textTruncated && !chunksTruncated && !collector.incomplete {
		artifact.Coverage = IndexCoverageComplete
	}
	return jsonYAMLFinalizeArtifact(source, profile, artifact)
}

type jsonYAMLCollector struct {
	artifact      *JSONYAMLArtifact
	outputBytes   int
	incomplete    bool
	outputLimited bool
}

func newJSONYAMLCollector(artifact *JSONYAMLArtifact) *jsonYAMLCollector {
	collector := &jsonYAMLCollector{artifact: artifact}
	if artifact == nil {
		return collector
	}
	collector.outputBytes = len(artifact.Text)
	for _, chunk := range artifact.Chunks {
		collector.outputBytes += len(chunk.Text) + len(chunk.ContentDigest) + 32
	}
	return collector
}

func (collector *jsonYAMLCollector) addDefinition(definition JSONYAMLDefinition) bool {
	if collector == nil || collector.artifact == nil {
		return false
	}
	if len(collector.artifact.Definitions) >= jsonYAMLExtractionMaxDefinitions {
		collector.limit("DEFINITION_LIMIT", definition.Span, "definition output reached its bounded limit")
		return false
	}
	if !collector.reserve(len(definition.Kind) + len(definition.SymbolKey) + len(definition.LocalKey) + 40) {
		return false
	}
	collector.artifact.Definitions = append(collector.artifact.Definitions, definition)
	return true
}

func (collector *jsonYAMLCollector) addReference(reference JSONYAMLReferenceSite) bool {
	if collector == nil || collector.artifact == nil {
		return false
	}
	if len(collector.artifact.References) >= jsonYAMLExtractionMaxReferences {
		collector.limit("REFERENCE_LIMIT", reference.Span, "reference output reached its bounded limit")
		return false
	}
	if !collector.reserve(len(reference.Kind) + len(reference.SymbolKey) + len(reference.LocalKey) + len(reference.TargetKey) + 48) {
		return false
	}
	collector.artifact.References = append(collector.artifact.References, reference)
	return true
}

func (collector *jsonYAMLCollector) reserve(size int) bool {
	if collector == nil {
		return false
	}
	if size < 0 || collector.outputBytes > jsonYAMLExtractionMaxOutputBytes-size {
		collector.incomplete = true
		if !collector.outputLimited {
			collector.outputLimited = true
			collector.addDiagnostic("OUTPUT_LIMIT", IndexSpan{}, "extracted facts exceeded the bounded output limit")
		}
		return false
	}
	collector.outputBytes += size
	return true
}

func (collector *jsonYAMLCollector) limit(code string, span IndexSpan, message string) {
	if collector == nil {
		return
	}
	collector.incomplete = true
	collector.addDiagnostic(code, span, message)
}

func (collector *jsonYAMLCollector) addDiagnostic(code string, span IndexSpan, message string) {
	if collector == nil || collector.artifact == nil || len(collector.artifact.Diagnostics) >= jsonYAMLExtractionMaxDiagnostics {
		return
	}
	if len(message) > jsonYAMLExtractionMaxDiagnosticBytes {
		message = message[:jsonYAMLExtractionMaxDiagnosticBytes]
	}
	collector.artifact.Diagnostics = append(collector.artifact.Diagnostics, JSONYAMLDiagnostic{
		Code:    code,
		Span:    span,
		Message: message,
	})
}

func jsonYAMLExtractionProfileValid(profile JSONYAMLExtractionProfile) bool {
	if !jsonYAMLExtractionProfileKeyValid(profile.ProfileKey) || !jsonYAMLExtractionProfileKeyValid(profile.ParserKey) {
		return false
	}
	switch profile.Format {
	case JSONYAMLFormatJSON, JSONYAMLFormatYAML:
		return true
	default:
		return false
	}
}

func jsonYAMLExtractionProfileKeyValid(value string) bool {
	if value == "" || len(value) > jsonYAMLExtractionMaxProfileBytes || !utf8.ValidString(value) || strings.TrimSpace(value) != value {
		return false
	}
	for _, character := range value {
		if character < 0x20 || character == 0x7f {
			return false
		}
	}
	return true
}

func jsonYAMLRetainedLength(source []byte, validUTF8 bool) int {
	if len(source) <= jsonYAMLExtractionMaxArtifactTextBytes {
		return len(source)
	}
	length := jsonYAMLExtractionMaxArtifactTextBytes
	if validUTF8 {
		length = jsonYAMLSafeUTF8Boundary(source, length)
	}
	return length
}

func jsonYAMLSourceChunks(source []byte, lineStarts []int, retainedLength int, validUTF8 bool) ([]JSONYAMLChunk, bool) {
	if retainedLength <= 0 {
		return nil, len(source) != 0
	}

	capacity := (retainedLength + jsonYAMLExtractionMaxChunkBytes - 1) / jsonYAMLExtractionMaxChunkBytes
	if capacity > jsonYAMLExtractionMaxChunks {
		capacity = jsonYAMLExtractionMaxChunks
	}
	chunks := make([]JSONYAMLChunk, 0, capacity)
	start := 0
	for start < retainedLength && len(chunks) < jsonYAMLExtractionMaxChunks {
		end := start + jsonYAMLExtractionMaxChunkBytes
		if end > retainedLength {
			end = retainedLength
		} else if validUTF8 {
			end = jsonYAMLChunkBoundary(source, start, end)
		}
		if end <= start {
			end = start + jsonYAMLExtractionMaxChunkBytes
			if end > retainedLength {
				end = retainedLength
			}
		}

		span, valid := goSpanFromOffsets(lineStarts, retainedLength, start, end)
		if !valid {
			return chunks, true
		}
		chunks = append(chunks, JSONYAMLChunk{
			Span:          span,
			Text:          string(source[start:end]),
			ContentDigest: goSourceDigest(source[start:end]),
		})
		start = end
	}
	return chunks, start < retainedLength || retainedLength < len(source)
}

func jsonYAMLChunkBoundary(source []byte, start, end int) int {
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

func jsonYAMLSafeUTF8Boundary(value []byte, limit int) int {
	if limit >= len(value) {
		return len(value)
	}
	if limit <= 0 {
		return 0
	}
	boundary := limit
	for boundary > 0 && boundary < len(value) && value[boundary]&0xc0 == 0x80 {
		boundary--
	}
	return boundary
}

func jsonYAMLLineStarts(source []byte) []int {
	starts := []int{0}
	for offset, value := range source {
		if value == '\n' {
			starts = append(starts, offset+1)
		}
	}
	return starts
}

func jsonYAMLFinalizeArtifact(source []byte, profile JSONYAMLExtractionProfile, artifact JSONYAMLArtifact) JSONYAMLArtifact {
	sort.Slice(artifact.Definitions, func(left, right int) bool {
		return jsonYAMLDefinitionLess(artifact.Definitions[left], artifact.Definitions[right])
	})
	sort.Slice(artifact.References, func(left, right int) bool {
		return jsonYAMLReferenceLess(artifact.References[left], artifact.References[right])
	})
	sort.Slice(artifact.Chunks, func(left, right int) bool {
		return jsonYAMLChunkLess(artifact.Chunks[left], artifact.Chunks[right])
	})
	sort.Slice(artifact.Diagnostics, func(left, right int) bool {
		return jsonYAMLDiagnosticLess(artifact.Diagnostics[left], artifact.Diagnostics[right])
	})

	contentDigest := goSourceDigest(source)
	artifact.Proof = IndexArtifactProof{
		ArtifactID:         jsonYAMLArtifactID(contentDigest, profile),
		ContentDigest:      contentDigest,
		FactsDigest:        jsonYAMLFactsDigest(profile, artifact),
		DefinitionCount:    uint64(len(artifact.Definitions)),
		ReferenceSiteCount: uint64(len(artifact.References)),
		ChunkCount:         uint64(len(artifact.Chunks)),
	}
	return artifact
}

func jsonYAMLArtifactID(contentDigest IndexDigest, profile JSONYAMLExtractionProfile) string {
	state := sha256.New()
	goWriteHashString(state, "uci-json-yaml-artifact-id/v1")
	goWriteHashString(state, string(contentDigest))
	goWriteHashString(state, profile.ProfileKey)
	goWriteHashString(state, profile.ParserKey)
	goWriteHashString(state, string(profile.Format))
	goWriteHashString(state, jsonYAMLExtractionParserRevision)

	sum := state.Sum(nil)
	var identifier [16]byte
	copy(identifier[:], sum[:len(identifier)])
	identifier[6] = identifier[6]&0x0f | 0x50
	identifier[8] = identifier[8]&0x3f | 0x80
	encoded := hex.EncodeToString(identifier[:])
	return encoded[0:8] + "-" + encoded[8:12] + "-" + encoded[12:16] + "-" + encoded[16:20] + "-" + encoded[20:]
}

func jsonYAMLFactsDigest(profile JSONYAMLExtractionProfile, artifact JSONYAMLArtifact) IndexDigest {
	state := sha256.New()
	goWriteHashString(state, "uci-json-yaml-facts/v1")
	goWriteHashString(state, jsonYAMLExtractionSchemaRevision)
	goWriteHashString(state, jsonYAMLExtractionParserRevision)
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
		goWriteHashUint64(state, uint64(definition.Document))
		goWriteHashSpan(state, definition.Span)
	}

	goWriteHashUint64(state, uint64(len(artifact.References)))
	for _, reference := range artifact.References {
		goWriteHashString(state, reference.Kind)
		goWriteHashString(state, reference.SymbolKey)
		goWriteHashString(state, reference.LocalKey)
		goWriteHashString(state, reference.TargetKey)
		goWriteHashUint64(state, uint64(reference.Document))
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

func jsonYAMLDefinitionLess(left, right JSONYAMLDefinition) bool {
	if comparison := goCompareSpans(left.Span, right.Span); comparison != 0 {
		return comparison < 0
	}
	if left.Document != right.Document {
		return left.Document < right.Document
	}
	if left.Kind != right.Kind {
		return left.Kind < right.Kind
	}
	if left.SymbolKey != right.SymbolKey {
		return left.SymbolKey < right.SymbolKey
	}
	return left.LocalKey < right.LocalKey
}

func jsonYAMLReferenceLess(left, right JSONYAMLReferenceSite) bool {
	if comparison := goCompareSpans(left.Span, right.Span); comparison != 0 {
		return comparison < 0
	}
	if left.Document != right.Document {
		return left.Document < right.Document
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
	return left.TargetKey < right.TargetKey
}

func jsonYAMLChunkLess(left, right JSONYAMLChunk) bool {
	if comparison := goCompareSpans(left.Span, right.Span); comparison != 0 {
		return comparison < 0
	}
	if left.ContentDigest != right.ContentDigest {
		return left.ContentDigest < right.ContentDigest
	}
	return left.Text < right.Text
}

func jsonYAMLDiagnosticLess(left, right JSONYAMLDiagnostic) bool {
	if comparison := goCompareSpans(left.Span, right.Span); comparison != 0 {
		return comparison < 0
	}
	if left.Code != right.Code {
		return left.Code < right.Code
	}
	return left.Message < right.Message
}

func jsonYAMLDocumentKey(format JSONYAMLFormat, document int) string {
	return string(format) + ":document:" + strconv.Itoa(document)
}

func jsonYAMLSymbolKey(format JSONYAMLFormat, document int, pointer string) string {
	key := jsonYAMLDocumentKey(format, document)
	if pointer == "" {
		return key
	}
	return key + "#" + pointer
}

func jsonYAMLAnchorSymbolKey(document int, name string) string {
	return jsonYAMLDocumentKey(JSONYAMLFormatYAML, document) + "#anchor:" + jsonYAMLEscapePointerSegment(name)
}

func jsonYAMLReferenceSymbolKey(format JSONYAMLFormat, document int, pointer string, span IndexSpan) string {
	return jsonYAMLSymbolKey(format, document, pointer) + ":ref:" + strconv.FormatInt(span.ByteStart, 10)
}

func jsonYAMLPointerAppend(pointer, segment string) (string, bool) {
	escapedLength := len(segment)
	for index := 0; index < len(segment); index++ {
		if segment[index] == '~' || segment[index] == '/' {
			escapedLength++
		}
	}
	if len(pointer)+1+escapedLength > jsonYAMLExtractionMaxPointerBytes {
		return "", false
	}
	var builder strings.Builder
	builder.Grow(len(pointer) + 1 + escapedLength)
	builder.WriteString(pointer)
	builder.WriteByte('/')
	for index := 0; index < len(segment); index++ {
		switch segment[index] {
		case '~':
			builder.WriteString("~0")
		case '/':
			builder.WriteString("~1")
		default:
			builder.WriteByte(segment[index])
		}
	}
	return builder.String(), true
}

func jsonYAMLEscapePointerSegment(value string) string {
	var builder strings.Builder
	builder.Grow(len(value))
	for index := 0; index < len(value); index++ {
		switch value[index] {
		case '~':
			builder.WriteString("~0")
		case '/':
			builder.WriteString("~1")
		default:
			builder.WriteByte(value[index])
		}
	}
	return builder.String()
}

type jsonYAMLFactRange struct {
	key             string
	pointer         string
	span            IndexSpan
	definitionStart int
	definitionEnd   int
	referenceStart  int
	referenceEnd    int
}

func jsonYAMLFilterDuplicateEntries(collector *jsonYAMLCollector, format JSONYAMLFormat, document int, definitionBase, referenceBase int, entries []jsonYAMLFactRange, ambiguous map[string]struct{}) {
	if collector == nil || collector.artifact == nil || len(entries) == 0 {
		return
	}
	counts := make(map[string]int, len(entries))
	for _, entry := range entries {
		counts[entry.key]++
	}
	duplicates := make(map[string]struct{})
	for key, count := range counts {
		if count > 1 {
			duplicates[key] = struct{}{}
		}
	}
	if len(duplicates) == 0 {
		return
	}

	definitions := collector.artifact.Definitions
	references := collector.artifact.References
	keptDefinitions := make([]JSONYAMLDefinition, 0, len(definitions))
	keptDefinitions = append(keptDefinitions, definitions[:definitionBase]...)
	keptReferences := make([]JSONYAMLReferenceSite, 0, len(references))
	keptReferences = append(keptReferences, references[:referenceBase]...)
	for _, entry := range entries {
		if _, duplicate := duplicates[entry.key]; duplicate {
			ambiguous[jsonYAMLSymbolKey(format, document, entry.pointer)] = struct{}{}
			collector.limit("DUPLICATE_KEY", entry.span, "mapping key is ambiguous and no key fact was selected")
			continue
		}
		keptDefinitions = append(keptDefinitions, definitions[entry.definitionStart:entry.definitionEnd]...)
		keptReferences = append(keptReferences, references[entry.referenceStart:entry.referenceEnd]...)
	}
	collector.artifact.Definitions = keptDefinitions
	collector.artifact.References = keptReferences
}

func jsonYAMLDropAmbiguousReferences(collector *jsonYAMLCollector, ambiguous map[string]struct{}) {
	if collector == nil || collector.artifact == nil || len(ambiguous) == 0 {
		return
	}
	kept := collector.artifact.References[:0]
	for _, reference := range collector.artifact.References {
		if _, ambiguousTarget := ambiguous[reference.TargetKey]; ambiguousTarget {
			collector.limit("AMBIGUOUS_REFERENCE", reference.Span, "local reference targets an ambiguous key")
			continue
		}
		kept = append(kept, reference)
	}
	collector.artifact.References = kept
}

// JSON extraction uses a small byte parser so that source spans are exact for
// CRLF and multibyte UTF-8 input while retaining no parser-owned source data.
type jsonYAMLJSONParser struct {
	collector  *jsonYAMLCollector
	source     []byte
	lineStarts []int
	profile    JSONYAMLExtractionProfile
	offset     int
	nodes      int
	failed     bool
	ambiguous  map[string]struct{}
}

type jsonYAMLJSONValueKind uint8

const (
	jsonYAMLJSONValueOther jsonYAMLJSONValueKind = iota
	jsonYAMLJSONValueString
)

type jsonYAMLJSONValue struct {
	kind        jsonYAMLJSONValueKind
	stringValue string
	start       int
	end         int
}

func jsonYAMLExtractJSON(source []byte, lineStarts []int, profile JSONYAMLExtractionProfile, collector *jsonYAMLCollector) bool {
	parser := &jsonYAMLJSONParser{
		collector:  collector,
		source:     source,
		lineStarts: lineStarts,
		profile:    profile,
		ambiguous:  make(map[string]struct{}),
	}
	parser.skipWhitespace()
	if parser.offset == len(source) {
		parser.fail("PARSE_ERROR", parser.offset, "JSON source contains no value")
		return false
	}
	_, parsed := parser.parseValue("", 0, true)
	if parsed {
		parser.skipWhitespace()
		if parser.offset != len(source) {
			parser.fail("PARSE_ERROR", parser.offset, "JSON source contains trailing non-whitespace bytes")
			parsed = false
		}
	}

	if len(source) > 0 {
		if span, valid := goSpanFromOffsets(lineStarts, len(source), 0, len(source)); valid {
			if !collector.addDefinition(JSONYAMLDefinition{
				Kind:      "document",
				SymbolKey: jsonYAMLDocumentKey(profile.Format, 0),
				LocalKey:  "document:0",
				Document:  0,
				Span:      span,
			}) {
				parsed = false
			}
		}
	}
	jsonYAMLDropAmbiguousReferences(collector, parser.ambiguous)
	return parsed && !parser.failed && !collector.incomplete
}

func (parser *jsonYAMLJSONParser) parseValue(pointer string, depth int, emit bool) (jsonYAMLJSONValue, bool) {
	parser.skipWhitespace()
	start := parser.offset
	if depth > jsonYAMLExtractionMaxDepth {
		parser.fail("DEPTH_LIMIT", start, "JSON nesting exceeded the bounded extraction depth")
		return jsonYAMLJSONValue{}, false
	}
	if parser.nodes >= jsonYAMLExtractionMaxNodes {
		parser.fail("NODE_LIMIT", start, "JSON node count exceeded the bounded extraction limit")
		return jsonYAMLJSONValue{}, false
	}
	if start >= len(parser.source) {
		parser.fail("PARSE_ERROR", start, "JSON source ended before a value")
		return jsonYAMLJSONValue{}, false
	}
	parser.nodes++

	switch parser.source[parser.offset] {
	case '{':
		return parser.parseObject(pointer, depth, emit)
	case '[':
		return parser.parseArray(pointer, depth, emit)
	case '"':
		value, stringStart, stringEnd, ok := parser.parseString()
		if !ok {
			return jsonYAMLJSONValue{}, false
		}
		return jsonYAMLJSONValue{kind: jsonYAMLJSONValueString, stringValue: value, start: stringStart, end: stringEnd}, true
	case 't':
		if parser.parseLiteral("true") {
			return jsonYAMLJSONValue{start: start, end: parser.offset}, true
		}
	case 'f':
		if parser.parseLiteral("false") {
			return jsonYAMLJSONValue{start: start, end: parser.offset}, true
		}
	case 'n':
		if parser.parseLiteral("null") {
			return jsonYAMLJSONValue{start: start, end: parser.offset}, true
		}
	default:
		if parser.source[parser.offset] == '-' || (parser.source[parser.offset] >= '0' && parser.source[parser.offset] <= '9') {
			if parser.parseNumber() {
				return jsonYAMLJSONValue{start: start, end: parser.offset}, true
			}
		}
	}
	parser.fail("PARSE_ERROR", start, "JSON value is malformed")
	return jsonYAMLJSONValue{}, false
}

func (parser *jsonYAMLJSONParser) parseObject(pointer string, depth int, emit bool) (jsonYAMLJSONValue, bool) {
	start := parser.offset
	parser.offset++
	parser.skipWhitespace()
	if parser.consume('}') {
		return jsonYAMLJSONValue{start: start, end: parser.offset}, true
	}

	definitionBase := len(parser.collector.artifact.Definitions)
	referenceBase := len(parser.collector.artifact.References)
	entries := make([]jsonYAMLFactRange, 0)
	for {
		parser.skipWhitespace()
		if parser.offset >= len(parser.source) || parser.source[parser.offset] != '"' {
			parser.fail("PARSE_ERROR", parser.offset, "JSON object key must be a string")
			return jsonYAMLJSONValue{}, false
		}
		key, keyStart, keyEnd, ok := parser.parseString()
		if !ok {
			return jsonYAMLJSONValue{}, false
		}
		parser.skipWhitespace()
		if !parser.consume(':') {
			parser.fail("PARSE_ERROR", parser.offset, "JSON object key is missing its colon")
			return jsonYAMLJSONValue{}, false
		}

		nextPointer, pointerValid := jsonYAMLPointerAppend(pointer, key)
		childEmit := emit && pointerValid
		if emit && !pointerValid {
			parser.collector.limit("POINTER_LIMIT", parser.span(keyStart, keyEnd), "JSON pointer exceeded the bounded extraction limit")
		}
		definitionStart := len(parser.collector.artifact.Definitions)
		referenceStart := len(parser.collector.artifact.References)
		value, ok := parser.parseValue(nextPointer, depth+1, childEmit)
		if !ok {
			return jsonYAMLJSONValue{}, false
		}
		if childEmit {
			if key == "$ref" && value.kind == jsonYAMLJSONValueString {
				parser.addPointerReference(nextPointer, value)
			}
			span := parser.span(keyStart, keyEnd)
			if span.ByteEnd <= span.ByteStart {
				parser.collector.limit("SPAN_UNAVAILABLE", IndexSpan{}, "JSON key span could not be represented")
			} else if !parser.collector.addDefinition(JSONYAMLDefinition{
				Kind:      "key",
				SymbolKey: jsonYAMLSymbolKey(parser.profile.Format, 0, nextPointer),
				LocalKey:  "#" + nextPointer,
				Document:  0,
				Span:      span,
			}) {
				return jsonYAMLJSONValue{}, false
			}
		}
		if emit {
			entries = append(entries, jsonYAMLFactRange{
				key:             key,
				pointer:         nextPointer,
				span:            parser.span(keyStart, keyEnd),
				definitionStart: definitionStart,
				definitionEnd:   len(parser.collector.artifact.Definitions),
				referenceStart:  referenceStart,
				referenceEnd:    len(parser.collector.artifact.References),
			})
		}

		parser.skipWhitespace()
		if parser.consume('}') {
			break
		}
		if !parser.consume(',') {
			parser.fail("PARSE_ERROR", parser.offset, "JSON object entries must be separated by commas")
			return jsonYAMLJSONValue{}, false
		}
	}
	if emit {
		jsonYAMLFilterDuplicateEntries(parser.collector, parser.profile.Format, 0, definitionBase, referenceBase, entries, parser.ambiguous)
	}
	return jsonYAMLJSONValue{start: start, end: parser.offset}, true
}

func (parser *jsonYAMLJSONParser) parseArray(pointer string, depth int, emit bool) (jsonYAMLJSONValue, bool) {
	start := parser.offset
	parser.offset++
	parser.skipWhitespace()
	if parser.consume(']') {
		return jsonYAMLJSONValue{start: start, end: parser.offset}, true
	}

	for index := 0; ; index++ {
		nextPointer, pointerValid := jsonYAMLPointerAppend(pointer, strconv.Itoa(index))
		childEmit := emit && pointerValid
		if emit && !pointerValid {
			parser.collector.limit("POINTER_LIMIT", parser.pointSpan(parser.offset), "JSON pointer exceeded the bounded extraction limit")
		}
		value, ok := parser.parseValue(nextPointer, depth+1, childEmit)
		if !ok {
			return jsonYAMLJSONValue{}, false
		}
		if childEmit {
			span := parser.span(value.start, value.end)
			if span.ByteEnd <= span.ByteStart {
				parser.collector.limit("SPAN_UNAVAILABLE", IndexSpan{}, "JSON array item span could not be represented")
			} else if !parser.collector.addDefinition(JSONYAMLDefinition{
				Kind:      "index",
				SymbolKey: jsonYAMLSymbolKey(parser.profile.Format, 0, nextPointer),
				LocalKey:  "#" + nextPointer,
				Document:  0,
				Span:      span,
			}) {
				return jsonYAMLJSONValue{}, false
			}
		}

		parser.skipWhitespace()
		if parser.consume(']') {
			break
		}
		if !parser.consume(',') {
			parser.fail("PARSE_ERROR", parser.offset, "JSON array items must be separated by commas")
			return jsonYAMLJSONValue{}, false
		}
		parser.skipWhitespace()
	}
	return jsonYAMLJSONValue{start: start, end: parser.offset}, true
}

func (parser *jsonYAMLJSONParser) parseString() (string, int, int, bool) {
	start := parser.offset
	if start >= len(parser.source) || parser.source[start] != '"' {
		parser.fail("PARSE_ERROR", start, "JSON string must begin with a quote")
		return "", 0, 0, false
	}
	parser.offset++
	for parser.offset < len(parser.source) {
		value := parser.source[parser.offset]
		switch value {
		case '"':
			parser.offset++
			decoded, err := strconv.Unquote(string(parser.source[start:parser.offset]))
			if err != nil {
				parser.fail("PARSE_ERROR", start, "JSON string escape is malformed")
				return "", 0, 0, false
			}
			return decoded, start, parser.offset, true
		case '\\':
			if parser.offset+1 >= len(parser.source) {
				parser.fail("PARSE_ERROR", parser.offset, "JSON string ends in an incomplete escape")
				return "", 0, 0, false
			}
			escape := parser.source[parser.offset+1]
			switch escape {
			case '"', '\\', '/', 'b', 'f', 'n', 'r', 't':
				parser.offset += 2
			case 'u':
				if parser.offset+5 >= len(parser.source) || !jsonYAMLHex(parser.source[parser.offset+2]) || !jsonYAMLHex(parser.source[parser.offset+3]) || !jsonYAMLHex(parser.source[parser.offset+4]) || !jsonYAMLHex(parser.source[parser.offset+5]) {
					parser.fail("PARSE_ERROR", parser.offset, "JSON string contains an invalid unicode escape")
					return "", 0, 0, false
				}
				parser.offset += 6
			default:
				parser.fail("PARSE_ERROR", parser.offset, "JSON string contains an invalid escape")
				return "", 0, 0, false
			}
		default:
			if value < 0x20 {
				parser.fail("PARSE_ERROR", parser.offset, "JSON string contains a control byte")
				return "", 0, 0, false
			}
			parser.offset++
		}
	}
	parser.fail("PARSE_ERROR", start, "JSON string is unterminated")
	return "", 0, 0, false
}

func jsonYAMLHex(value byte) bool {
	return (value >= '0' && value <= '9') || (value >= 'a' && value <= 'f') || (value >= 'A' && value <= 'F')
}

func (parser *jsonYAMLJSONParser) parseLiteral(literal string) bool {
	if len(parser.source)-parser.offset < len(literal) || string(parser.source[parser.offset:parser.offset+len(literal)]) != literal {
		return false
	}
	parser.offset += len(literal)
	return true
}

func (parser *jsonYAMLJSONParser) parseNumber() bool {
	start := parser.offset
	if parser.consume('-') && parser.offset >= len(parser.source) {
		return false
	}
	if parser.offset >= len(parser.source) {
		return false
	}
	if parser.source[parser.offset] == '0' {
		parser.offset++
	} else if parser.source[parser.offset] >= '1' && parser.source[parser.offset] <= '9' {
		for parser.offset < len(parser.source) && parser.source[parser.offset] >= '0' && parser.source[parser.offset] <= '9' {
			parser.offset++
		}
	} else {
		parser.offset = start
		return false
	}
	if parser.consume('.') {
		fractionStart := parser.offset
		for parser.offset < len(parser.source) && parser.source[parser.offset] >= '0' && parser.source[parser.offset] <= '9' {
			parser.offset++
		}
		if fractionStart == parser.offset {
			parser.offset = start
			return false
		}
	}
	if parser.offset < len(parser.source) && (parser.source[parser.offset] == 'e' || parser.source[parser.offset] == 'E') {
		parser.offset++
		if parser.offset < len(parser.source) && (parser.source[parser.offset] == '+' || parser.source[parser.offset] == '-') {
			parser.offset++
		}
		exponentStart := parser.offset
		for parser.offset < len(parser.source) && parser.source[parser.offset] >= '0' && parser.source[parser.offset] <= '9' {
			parser.offset++
		}
		if exponentStart == parser.offset {
			parser.offset = start
			return false
		}
	}
	return true
}

func (parser *jsonYAMLJSONParser) addPointerReference(pointer string, value jsonYAMLJSONValue) {
	targetKey, local := jsonYAMLLocalPointerTarget(parser.profile.Format, 0, value.stringValue)
	span := parser.span(value.start, value.end)
	if !local {
		code := "EXTERNAL_REFERENCE"
		message := "reference is not a local JSON pointer and was not resolved"
		if strings.HasPrefix(value.stringValue, "#") {
			code = "UNSUPPORTED_REFERENCE"
			message = "local reference is not a supported JSON pointer"
		}
		parser.collector.limit(code, span, message)
		return
	}
	if !parser.collector.addReference(JSONYAMLReferenceSite{
		Kind:      "pointer",
		SymbolKey: jsonYAMLReferenceSymbolKey(parser.profile.Format, 0, pointer, span),
		LocalKey:  "#" + pointer,
		TargetKey: targetKey,
		Document:  0,
		Span:      span,
	}) {
		parser.failed = true
	}
}

func jsonYAMLLocalPointerTarget(format JSONYAMLFormat, document int, value string) (string, bool) {
	if value == "#" {
		return jsonYAMLDocumentKey(format, document), true
	}
	if !strings.HasPrefix(value, "#/") || len(value)-1 > jsonYAMLExtractionMaxPointerBytes {
		return "", false
	}
	return jsonYAMLSymbolKey(format, document, value[1:]), true
}

func (parser *jsonYAMLJSONParser) skipWhitespace() {
	for parser.offset < len(parser.source) {
		switch parser.source[parser.offset] {
		case ' ', '\t', '\r', '\n':
			parser.offset++
		default:
			return
		}
	}
}

func (parser *jsonYAMLJSONParser) consume(value byte) bool {
	if parser.offset < len(parser.source) && parser.source[parser.offset] == value {
		parser.offset++
		return true
	}
	return false
}

func (parser *jsonYAMLJSONParser) span(start, end int) IndexSpan {
	span, valid := goSpanFromOffsets(parser.lineStarts, len(parser.source), start, end)
	if !valid {
		return IndexSpan{}
	}
	return span
}

func (parser *jsonYAMLJSONParser) pointSpan(offset int) IndexSpan {
	if offset < 0 || offset >= len(parser.source) {
		return IndexSpan{}
	}
	return parser.span(offset, offset+1)
}

func (parser *jsonYAMLJSONParser) fail(code string, offset int, message string) {
	if parser.failed {
		return
	}
	parser.failed = true
	parser.collector.limit(code, parser.pointSpan(offset), message)
}

// YAML extraction decodes only into yaml.Node. It never unmarshals into an
// application type, follows an alias, applies a merge, or evaluates a tag.
func jsonYAMLExtractYAML(source []byte, lineStarts []int, profile JSONYAMLExtractionProfile, collector *jsonYAMLCollector) bool {
	documents, documentLimited, err := jsonYAMLDecodeYAMLDocuments(source)
	if err != nil {
		collector.limit("PARSE_ERROR", IndexSpan{}, "YAML source could not be parsed completely")
		jsonYAMLRecoverYAMLPrefix(source, lineStarts, profile, collector)
		return false
	}
	complete := jsonYAMLProcessYAMLDocuments(documents, source, lineStarts, profile, collector)
	if documentLimited {
		collector.limit("DOCUMENT_LIMIT", IndexSpan{}, "YAML document count exceeded the bounded extraction limit")
		return false
	}
	return complete && !collector.incomplete
}

func jsonYAMLDecodeYAMLDocuments(source []byte) ([]yaml.Node, bool, error) {
	decoder := yaml.NewDecoder(bytes.NewReader(source))
	documents := make([]yaml.Node, 0)
	for {
		var document yaml.Node
		err := decoder.Decode(&document)
		if err == io.EOF {
			return documents, false, nil
		}
		if err != nil {
			return documents, false, err
		}
		if len(documents) >= jsonYAMLExtractionMaxDocuments {
			return documents, true, nil
		}
		documents = append(documents, document)
	}
}

func jsonYAMLRecoverYAMLPrefix(source []byte, lineStarts []int, profile JSONYAMLExtractionProfile, collector *jsonYAMLCollector) {
	end := len(source)
	for attempt := 0; attempt < jsonYAMLExtractionMaxRecoveryLines; attempt++ {
		end = jsonYAMLPreviousLineStart(source, end)
		if end <= 0 {
			return
		}
		documents, limited, err := jsonYAMLDecodeYAMLDocuments(source[:end])
		if err != nil || limited || len(documents) == 0 {
			continue
		}
		jsonYAMLProcessYAMLDocuments(documents, source, lineStarts, profile, collector)
		return
	}
}

func jsonYAMLPreviousLineStart(source []byte, end int) int {
	if end > len(source) {
		end = len(source)
	}
	if end > 0 && source[end-1] == '\n' {
		end--
	}
	if end <= 0 {
		return 0
	}
	lastNewline := bytes.LastIndexByte(source[:end], '\n')
	if lastNewline < 0 {
		return 0
	}
	return lastNewline + 1
}

func jsonYAMLProcessYAMLDocuments(documents []yaml.Node, source []byte, lineStarts []int, profile JSONYAMLExtractionProfile, collector *jsonYAMLCollector) bool {
	starts := jsonYAMLYAMLDocumentStarts(source)
	complete := true
	for documentNumber := range documents {
		span, spanValid := jsonYAMLYAMLDocumentSpan(source, lineStarts, starts, documentNumber)
		if !spanValid {
			collector.limit("SPAN_UNAVAILABLE", IndexSpan{}, "YAML document span could not be represented")
			complete = false
		} else if !collector.addDefinition(JSONYAMLDefinition{
			Kind:      "document",
			SymbolKey: jsonYAMLDocumentKey(profile.Format, documentNumber),
			LocalKey:  "document:" + strconv.Itoa(documentNumber),
			Document:  documentNumber,
			Span:      span,
		}) {
			complete = false
		}

		root := &documents[documentNumber]
		if root.Kind == yaml.DocumentNode {
			if len(root.Content) == 0 {
				continue
			}
			if len(root.Content) != 1 {
				collector.limit("UNSUPPORTED_DOCUMENT", span, "YAML document has an unsupported root shape")
				complete = false
			}
			root = root.Content[0]
		}
		walker := jsonYAMLYAMLWalker{
			collector:        collector,
			source:           source,
			lineStarts:       lineStarts,
			profile:          profile,
			document:         documentNumber,
			anchors:          make(map[string]string),
			ambiguous:        make(map[string]struct{}),
			ambiguousAnchors: make(map[string]struct{}),
		}
		if !walker.walk(root, "", 0, true) {
			complete = false
		}
		jsonYAMLDropAmbiguousReferences(collector, walker.ambiguous)
	}
	return complete
}

type jsonYAMLYAMLWalker struct {
	collector        *jsonYAMLCollector
	source           []byte
	lineStarts       []int
	profile          JSONYAMLExtractionProfile
	document         int
	nodes            int
	anchors          map[string]string
	ambiguous        map[string]struct{}
	ambiguousAnchors map[string]struct{}
}

func (walker *jsonYAMLYAMLWalker) walk(node *yaml.Node, pointer string, depth int, emit bool) bool {
	if node == nil {
		walker.collector.limit("UNSUPPORTED_NODE", IndexSpan{}, "YAML parser produced a nil node")
		return false
	}
	if depth > jsonYAMLExtractionMaxDepth {
		walker.collector.limit("DEPTH_LIMIT", walker.nodeSpanOrZero(node), "YAML nesting exceeded the bounded extraction depth")
		return false
	}
	if walker.nodes >= jsonYAMLExtractionMaxNodes {
		walker.collector.limit("NODE_LIMIT", walker.nodeSpanOrZero(node), "YAML node count exceeded the bounded extraction limit")
		return false
	}
	walker.nodes++
	complete := true

	if strings.HasPrefix(node.Tag, "!") && !strings.HasPrefix(node.Tag, "!!") {
		walker.collector.limit("UNSUPPORTED_TAG", walker.nodeSpanOrZero(node), "custom YAML tags are retained only as unexecuted syntax")
		complete = false
	}
	if emit && node.Anchor != "" && !walker.addAnchor(node) {
		complete = false
	}

	switch node.Kind {
	case yaml.MappingNode:
		if !walker.walkMapping(node, pointer, depth, emit) {
			complete = false
		}
	case yaml.SequenceNode:
		if !walker.walkSequence(node, pointer, depth, emit) {
			complete = false
		}
	case yaml.AliasNode:
		if emit && !walker.addAlias(node, pointer) {
			complete = false
		}
	case yaml.ScalarNode:
		// Scalars contribute through their containing key or index definition.
	default:
		walker.collector.limit("UNSUPPORTED_NODE", walker.nodeSpanOrZero(node), "YAML node kind is unsupported")
		complete = false
	}
	return complete
}

func (walker *jsonYAMLYAMLWalker) walkMapping(node *yaml.Node, pointer string, depth int, emit bool) bool {
	complete := true
	if len(node.Content)%2 != 0 {
		walker.collector.limit("UNSUPPORTED_MAPPING", walker.nodeSpanOrZero(node), "YAML mapping has an unmatched key or value")
		complete = false
	}
	definitionBase := len(walker.collector.artifact.Definitions)
	referenceBase := len(walker.collector.artifact.References)
	entries := make([]jsonYAMLFactRange, 0, len(node.Content)/2)
	for index := 0; index+1 < len(node.Content); index += 2 {
		keyNode := node.Content[index]
		valueNode := node.Content[index+1]
		if keyNode == nil || valueNode == nil || keyNode.Kind != yaml.ScalarNode {
			walker.collector.limit("UNSUPPORTED_MAPPING_KEY", walker.nodeSpanOrZero(keyNode), "YAML mapping key is not a scalar")
			if valueNode != nil && !walker.walk(valueNode, "", depth+1, false) {
				complete = false
			}
			complete = false
			continue
		}

		nextPointer, pointerValid := jsonYAMLPointerAppend(pointer, keyNode.Value)
		childEmit := emit && pointerValid
		keySpan := walker.keySpanOrZero(keyNode)
		if emit && !pointerValid {
			walker.collector.limit("POINTER_LIMIT", keySpan, "YAML pointer exceeded the bounded extraction limit")
		}
		definitionStart := len(walker.collector.artifact.Definitions)
		referenceStart := len(walker.collector.artifact.References)
		if !walker.walk(valueNode, nextPointer, depth+1, childEmit) {
			complete = false
		}
		if childEmit {
			if keyNode.Value == "$ref" && valueNode.Kind == yaml.ScalarNode {
				walker.addPointerReference(nextPointer, valueNode)
			}
			if keySpan.ByteEnd <= keySpan.ByteStart {
				walker.collector.limit("SPAN_UNAVAILABLE", IndexSpan{}, "YAML key span could not be represented")
				complete = false
			} else if !walker.collector.addDefinition(JSONYAMLDefinition{
				Kind:      "key",
				SymbolKey: jsonYAMLSymbolKey(walker.profile.Format, walker.document, nextPointer),
				LocalKey:  "#" + nextPointer,
				Document:  walker.document,
				Span:      keySpan,
			}) {
				complete = false
			}
		}
		if emit {
			entries = append(entries, jsonYAMLFactRange{
				key:             keyNode.Value,
				pointer:         nextPointer,
				span:            keySpan,
				definitionStart: definitionStart,
				definitionEnd:   len(walker.collector.artifact.Definitions),
				referenceStart:  referenceStart,
				referenceEnd:    len(walker.collector.artifact.References),
			})
		}
	}
	if emit {
		jsonYAMLFilterDuplicateEntries(walker.collector, walker.profile.Format, walker.document, definitionBase, referenceBase, entries, walker.ambiguous)
	}
	return complete
}

func (walker *jsonYAMLYAMLWalker) walkSequence(node *yaml.Node, pointer string, depth int, emit bool) bool {
	complete := true
	for index, child := range node.Content {
		if child == nil {
			walker.collector.limit("UNSUPPORTED_NODE", IndexSpan{}, "YAML sequence contains a nil node")
			complete = false
			continue
		}
		nextPointer, pointerValid := jsonYAMLPointerAppend(pointer, strconv.Itoa(index))
		childEmit := emit && pointerValid
		if emit && !pointerValid {
			walker.collector.limit("POINTER_LIMIT", walker.nodeSpanOrZero(child), "YAML pointer exceeded the bounded extraction limit")
		}
		if !walker.walk(child, nextPointer, depth+1, childEmit) {
			complete = false
		}
		if childEmit {
			span := walker.nodeSpanOrZero(child)
			if span.ByteEnd <= span.ByteStart {
				walker.collector.limit("SPAN_UNAVAILABLE", IndexSpan{}, "YAML sequence item span could not be represented")
				complete = false
			} else if !walker.collector.addDefinition(JSONYAMLDefinition{
				Kind:      "index",
				SymbolKey: jsonYAMLSymbolKey(walker.profile.Format, walker.document, nextPointer),
				LocalKey:  "#" + nextPointer,
				Document:  walker.document,
				Span:      span,
			}) {
				complete = false
			}
		}
	}
	return complete
}

func (walker *jsonYAMLYAMLWalker) addAnchor(node *yaml.Node) bool {
	name := node.Anchor
	if name == "" || len(name) > jsonYAMLExtractionMaxAnchorBytes {
		walker.collector.limit("ANCHOR_LIMIT", walker.nodeSpanOrZero(node), "YAML anchor name exceeded the bounded extraction limit")
		return false
	}
	if _, ambiguous := walker.ambiguousAnchors[name]; ambiguous {
		return false
	}
	key := jsonYAMLAnchorSymbolKey(walker.document, name)
	if _, exists := walker.anchors[name]; exists {
		jsonYAMLDiscardDefinitions(walker.collector, key)
		walker.ambiguousAnchors[name] = struct{}{}
		walker.collector.limit("DUPLICATE_ANCHOR", walker.nodeSpanOrZero(node), "YAML anchor name is ambiguous")
		return false
	}
	span, valid := jsonYAMLYAMLMarkerSpan(walker.source, walker.lineStarts, node.Line, '&', name)
	if !valid {
		walker.collector.limit("SPAN_UNAVAILABLE", walker.nodeSpanOrZero(node), "YAML anchor span could not be represented")
		return false
	}
	if !walker.collector.addDefinition(JSONYAMLDefinition{
		Kind:      "anchor",
		SymbolKey: key,
		LocalKey:  "anchor:" + name,
		Document:  walker.document,
		Span:      span,
	}) {
		return false
	}
	walker.anchors[name] = key
	return true
}

func jsonYAMLDiscardDefinitions(collector *jsonYAMLCollector, symbolKey string) {
	if collector == nil || collector.artifact == nil {
		return
	}
	kept := collector.artifact.Definitions[:0]
	for _, definition := range collector.artifact.Definitions {
		if definition.SymbolKey != symbolKey {
			kept = append(kept, definition)
		}
	}
	collector.artifact.Definitions = kept
}

func (walker *jsonYAMLYAMLWalker) addAlias(node *yaml.Node, pointer string) bool {
	name := node.Value
	if name == "" && node.Alias != nil {
		name = node.Alias.Anchor
	}
	if name == "" || len(name) > jsonYAMLExtractionMaxAnchorBytes {
		walker.collector.limit("UNSUPPORTED_ALIAS", walker.nodeSpanOrZero(node), "YAML alias has no bounded local anchor name")
		return false
	}
	if _, ambiguous := walker.ambiguousAnchors[name]; ambiguous {
		walker.collector.limit("AMBIGUOUS_ALIAS", walker.nodeSpanOrZero(node), "YAML alias targets an ambiguous anchor")
		return false
	}
	span, valid := jsonYAMLYAMLMarkerSpan(walker.source, walker.lineStarts, node.Line, '*', name)
	if !valid {
		walker.collector.limit("SPAN_UNAVAILABLE", walker.nodeSpanOrZero(node), "YAML alias span could not be represented")
		return false
	}
	return walker.collector.addReference(JSONYAMLReferenceSite{
		Kind:      "alias",
		SymbolKey: jsonYAMLReferenceSymbolKey(walker.profile.Format, walker.document, pointer, span),
		LocalKey:  "#" + pointer,
		TargetKey: jsonYAMLAnchorSymbolKey(walker.document, name),
		Document:  walker.document,
		Span:      span,
	})
}

func (walker *jsonYAMLYAMLWalker) addPointerReference(pointer string, node *yaml.Node) {
	span := walker.nodeSpanOrZero(node)
	targetKey, local := jsonYAMLLocalPointerTarget(walker.profile.Format, walker.document, node.Value)
	if !local {
		code := "EXTERNAL_REFERENCE"
		message := "reference is not a local JSON pointer and was not resolved"
		if strings.HasPrefix(node.Value, "#") {
			code = "UNSUPPORTED_REFERENCE"
			message = "local reference is not a supported JSON pointer"
		}
		walker.collector.limit(code, span, message)
		return
	}
	walker.collector.addReference(JSONYAMLReferenceSite{
		Kind:      "pointer",
		SymbolKey: jsonYAMLReferenceSymbolKey(walker.profile.Format, walker.document, pointer, span),
		LocalKey:  "#" + pointer,
		TargetKey: targetKey,
		Document:  walker.document,
		Span:      span,
	})
}

func (walker *jsonYAMLYAMLWalker) nodeSpanOrZero(node *yaml.Node) IndexSpan {
	span, valid := walker.nodeSpan(node, false)
	if !valid {
		return IndexSpan{}
	}
	return span
}

func (walker *jsonYAMLYAMLWalker) keySpanOrZero(node *yaml.Node) IndexSpan {
	span, valid := walker.nodeSpan(node, true)
	if !valid {
		return IndexSpan{}
	}
	return span
}

func (walker *jsonYAMLYAMLWalker) nodeSpan(node *yaml.Node, key bool) (IndexSpan, bool) {
	if node == nil {
		return IndexSpan{}, false
	}
	start, valid := jsonYAMLYAMLPositionOffset(walker.source, walker.lineStarts, node.Line, node.Column)
	if !valid {
		return IndexSpan{}, false
	}
	end := jsonYAMLYAMLNodeEnd(walker.source, start, node, key)
	if end <= start {
		return IndexSpan{}, false
	}
	return goSpanFromOffsets(walker.lineStarts, len(walker.source), start, end)
}

func jsonYAMLYAMLPositionOffset(source []byte, lineStarts []int, line, column int) (int, bool) {
	if line < 1 || line > len(lineStarts) || column < 1 {
		return 0, false
	}
	start := lineStarts[line-1]
	end := jsonYAMLLineEnd(source, start)
	offset := start
	for currentColumn := 1; currentColumn < column; currentColumn++ {
		if offset >= end {
			return 0, false
		}
		_, size := utf8.DecodeRune(source[offset:end])
		if size == 0 || (size == 1 && source[offset] >= 0x80) {
			return 0, false
		}
		offset += size
	}
	return offset, true
}

func jsonYAMLYAMLNodeEnd(source []byte, start int, node *yaml.Node, key bool) int {
	lineEnd := jsonYAMLLineEnd(source, start)
	if start >= lineEnd {
		return start
	}
	if node.Kind == yaml.AliasNode {
		end := start + 1 + len(node.Value)
		if end <= lineEnd {
			return end
		}
	}
	switch source[start] {
	case '"':
		return jsonYAMLDoubleQuotedEnd(source, start, lineEnd)
	case '\'':
		return jsonYAMLSingleQuotedEnd(source, start, lineEnd)
	}
	end := start
	for end < lineEnd {
		value := source[end]
		if value == '\r' || value == '\n' || value == ' ' || value == '\t' || value == ',' || value == '[' || value == ']' || value == '{' || value == '}' {
			break
		}
		if key && value == ':' {
			break
		}
		if value == '#' && (end == start || source[end-1] == ' ' || source[end-1] == '\t') {
			break
		}
		end++
	}
	return end
}

func jsonYAMLDoubleQuotedEnd(source []byte, start, lineEnd int) int {
	for offset := start + 1; offset < lineEnd; offset++ {
		switch source[offset] {
		case '\\':
			offset++
		case '"':
			return offset + 1
		}
	}
	return lineEnd
}

func jsonYAMLSingleQuotedEnd(source []byte, start, lineEnd int) int {
	for offset := start + 1; offset < lineEnd; offset++ {
		if source[offset] != '\'' {
			continue
		}
		if offset+1 < lineEnd && source[offset+1] == '\'' {
			offset++
			continue
		}
		return offset + 1
	}
	return lineEnd
}

func jsonYAMLYAMLMarkerSpan(source []byte, lineStarts []int, line int, marker byte, name string) (IndexSpan, bool) {
	if line < 1 || line > len(lineStarts) || name == "" {
		return IndexSpan{}, false
	}
	start := lineStarts[line-1]
	end := jsonYAMLLineEnd(source, start)
	target := append([]byte{marker}, []byte(name)...)
	found := -1
	for search := start; search < end; {
		relative := bytes.Index(source[search:end], target)
		if relative < 0 {
			break
		}
		candidate := search + relative
		after := candidate + len(target)
		if (candidate == start || !jsonYAMLAnchorNameByte(source[candidate-1])) && (after >= end || !jsonYAMLAnchorNameByte(source[after])) {
			if found >= 0 {
				return IndexSpan{}, false
			}
			found = candidate
		}
		search = candidate + len(target)
	}
	if found < 0 {
		return IndexSpan{}, false
	}
	return goSpanFromOffsets(lineStarts, len(source), found, found+len(target))
}

func jsonYAMLAnchorNameByte(value byte) bool {
	return (value >= 'a' && value <= 'z') || (value >= 'A' && value <= 'Z') || (value >= '0' && value <= '9') || value == '_' || value == '-' || value >= 0x80
}

func jsonYAMLYAMLDocumentStarts(source []byte) []int {
	starts := []int{0}
	for offset := 0; offset < len(source); {
		end := jsonYAMLLineEnd(source, offset)
		if offset != 0 && jsonYAMLDocumentStartLine(source[offset:end]) {
			starts = append(starts, offset)
		}
		if end >= len(source) {
			break
		}
		offset = end + 1
	}
	return starts
}

func jsonYAMLDocumentStartLine(line []byte) bool {
	if len(line) < 3 || line[0] != '-' || line[1] != '-' || line[2] != '-' {
		return false
	}
	if len(line) == 3 {
		return true
	}
	switch line[3] {
	case ' ', '\t', '\r', '#':
		return true
	default:
		return false
	}
}

func jsonYAMLYAMLDocumentSpan(source []byte, lineStarts, starts []int, document int) (IndexSpan, bool) {
	if document < 0 || document >= len(starts) {
		return IndexSpan{}, false
	}
	start := starts[document]
	end := len(source)
	if document+1 < len(starts) {
		end = starts[document+1]
	}
	if end <= start {
		return IndexSpan{}, false
	}
	return goSpanFromOffsets(lineStarts, len(source), start, end)
}

func jsonYAMLLineEnd(source []byte, start int) int {
	if start < 0 || start >= len(source) {
		return len(source)
	}
	relative := bytes.IndexByte(source[start:], '\n')
	if relative < 0 {
		return len(source)
	}
	return start + relative
}
