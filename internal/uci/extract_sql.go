package uci

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"hash"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"
)

const (
	sqlExtractionSchemaRevision = "uci-sql-extraction/v1"
	sqlExtractionParserRevision = "sql-ddl-lexical/v1"

	sqlExtractionMaxSourceBytes       = 4 << 20
	sqlExtractionMaxArtifactTextBytes = sqlExtractionMaxSourceBytes
	sqlExtractionMaxChunkBytes        = 64 << 10
	sqlExtractionMaxChunks            = 64
	sqlExtractionMaxTokens            = 65_536
	sqlExtractionMaxStatements        = 2_048
	sqlExtractionMaxDepth             = 64
	sqlExtractionMaxColumns           = 1_024
	sqlExtractionMaxDefinitions       = 2_048
	sqlExtractionMaxReferences        = 8_192
	sqlExtractionMaxDiagnostics       = 16
	sqlExtractionMaxProfileBytes      = 256
	sqlExtractionMaxIdentifierBytes   = 4 << 10
	sqlExtractionMaxDiagnosticBytes   = 512
	sqlExtractionMaxOutputBytes       = 16 << 20
)

// SQLExtractionProfile identifies the caller-selected SQL extraction policy.
type SQLExtractionProfile struct {
	ProfileKey string
	ParserKey  string
}

// SQLArtifact is immutable evidence derived only from one caller-owned SQL buffer.
type SQLArtifact struct {
	Proof       IndexArtifactProof
	Coverage    IndexCoverageState
	Text        string
	Definitions []SQLDefinition
	References  []SQLReferenceSite
	Chunks      []SQLChunk
	Diagnostics []SQLDiagnostic
}

// SQLDefinition records a lexical table, column, primary-key, or unique fact.
type SQLDefinition struct {
	Kind          string
	SymbolKey     string
	LocalKey      string
	OwnerLocalKey string
	DataType      string
	Columns       []string
	Span          IndexSpan
}

// SQLReferenceSite records one lexical foreign-key target without resolving it.
type SQLReferenceSite struct {
	Kind           string
	SymbolKey      string
	LocalKey       string
	OwnerLocalKey  string
	TargetKey      string
	TargetLocalKey string
	Columns        []string
	TargetColumns  []string
	Span           IndexSpan
}

// SQLChunk is a bounded exact source-text segment.
type SQLChunk struct {
	Span          IndexSpan
	Text          string
	ContentDigest IndexDigest
}

// SQLDiagnostic records a bounded extraction limitation or malformed SQL fact.
type SQLDiagnostic struct {
	Code    string
	Span    IndexSpan
	Message string
}

// ExtractSQL derives deterministic, checkout-independent SQL DDL evidence. It
// only lexes caller-owned bytes; it never connects to, executes against, or
// infers the state of a database.
func ExtractSQL(source []byte, profile SQLExtractionProfile) SQLArtifact {
	lineStarts := goLineStarts(source)
	text, textTruncated := goSafeText(source, sqlExtractionMaxArtifactTextBytes)
	chunks, chunksTruncated := sqlSourceChunks(source, lineStarts)
	artifact := SQLArtifact{
		Coverage: IndexCoveragePartial,
		Text:     text,
		Chunks:   chunks,
	}
	collector := newSQLCollector(&artifact)

	if textTruncated {
		collector.limit("TEXT_LIMIT", IndexSpan{}, "source text exceeded the bounded extraction text limit")
	}
	if chunksTruncated {
		collector.limit("CHUNK_LIMIT", IndexSpan{}, "source chunks exceeded the bounded extraction chunk limit")
	}
	if !utf8.Valid(source) {
		collector.limit("INVALID_UTF8", IndexSpan{}, "source contains invalid UTF-8")
		return sqlFinalizeArtifact(source, profile, artifact)
	}
	if len(source) > sqlExtractionMaxSourceBytes {
		collector.limit("SOURCE_LIMIT", IndexSpan{}, "source exceeded the bounded parser input limit")
		return sqlFinalizeArtifact(source, profile, artifact)
	}
	if !sqlExtractionProfileValid(profile) {
		collector.limit("INVALID_PROFILE", IndexSpan{}, "profile keys must be bounded, valid UTF-8, and nonempty")
		return sqlFinalizeArtifact(source, profile, artifact)
	}

	tokens, lexedCompletely := sqlLex(source, lineStarts, collector)
	parsedCompletely := lexedCompletely
	if lexedCompletely {
		parsedCompletely = sqlExtractStatements(source, lineStarts, tokens, collector)
	}
	if parsedCompletely && !textTruncated && !chunksTruncated && !collector.incomplete {
		artifact.Coverage = IndexCoverageComplete
	}
	return sqlFinalizeArtifact(source, profile, artifact)
}

type sqlCollector struct {
	artifact      *SQLArtifact
	outputBytes   int
	incomplete    bool
	outputLimited bool
}

func newSQLCollector(artifact *SQLArtifact) *sqlCollector {
	collector := &sqlCollector{artifact: artifact}
	if artifact == nil {
		return collector
	}
	collector.outputBytes = len(artifact.Text)
	for _, chunk := range artifact.Chunks {
		collector.outputBytes += len(chunk.Text) + len(chunk.ContentDigest) + 32
	}
	return collector
}

func (collector *sqlCollector) addDefinition(definition SQLDefinition) bool {
	if collector == nil || collector.artifact == nil {
		return false
	}
	if len(collector.artifact.Definitions) >= sqlExtractionMaxDefinitions {
		collector.limit("DEFINITION_LIMIT", definition.Span, "definition output reached its bounded limit")
		return false
	}
	if !collector.reserve(sqlDefinitionSize(definition)) {
		return false
	}
	definition.Columns = sqlCloneStrings(definition.Columns)
	collector.artifact.Definitions = append(collector.artifact.Definitions, definition)
	return true
}

func (collector *sqlCollector) addReference(reference SQLReferenceSite) bool {
	if collector == nil || collector.artifact == nil {
		return false
	}
	if len(collector.artifact.References) >= sqlExtractionMaxReferences {
		collector.limit("REFERENCE_LIMIT", reference.Span, "reference output reached its bounded limit")
		return false
	}
	if !collector.reserve(sqlReferenceSize(reference)) {
		return false
	}
	reference.Columns = sqlCloneStrings(reference.Columns)
	reference.TargetColumns = sqlCloneStrings(reference.TargetColumns)
	collector.artifact.References = append(collector.artifact.References, reference)
	return true
}

func (collector *sqlCollector) reserve(size int) bool {
	if collector == nil {
		return false
	}
	if size < 0 || collector.outputBytes > sqlExtractionMaxOutputBytes-size {
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

func (collector *sqlCollector) limit(code string, span IndexSpan, message string) {
	if collector == nil {
		return
	}
	collector.incomplete = true
	collector.addDiagnostic(code, span, message)
}

func (collector *sqlCollector) addDiagnostic(code string, span IndexSpan, message string) {
	if collector == nil || collector.artifact == nil || len(collector.artifact.Diagnostics) >= sqlExtractionMaxDiagnostics {
		return
	}
	message, _ = goSafeText([]byte(message), sqlExtractionMaxDiagnosticBytes)
	collector.artifact.Diagnostics = append(collector.artifact.Diagnostics, SQLDiagnostic{
		Code:    code,
		Span:    span,
		Message: message,
	})
}

func sqlDefinitionSize(definition SQLDefinition) int {
	size := len(definition.Kind) + len(definition.SymbolKey) + len(definition.LocalKey) + len(definition.OwnerLocalKey) + len(definition.DataType) + 48
	for _, column := range definition.Columns {
		size += len(column) + 8
	}
	return size
}

func sqlReferenceSize(reference SQLReferenceSite) int {
	size := len(reference.Kind) + len(reference.SymbolKey) + len(reference.LocalKey) + len(reference.OwnerLocalKey) + len(reference.TargetKey) + len(reference.TargetLocalKey) + 64
	for _, column := range reference.Columns {
		size += len(column) + 8
	}
	for _, column := range reference.TargetColumns {
		size += len(column) + 8
	}
	return size
}

func sqlExtractionProfileValid(profile SQLExtractionProfile) bool {
	return sqlExtractionProfileKeyValid(profile.ProfileKey) && sqlExtractionProfileKeyValid(profile.ParserKey)
}

func sqlExtractionProfileKeyValid(value string) bool {
	if value == "" || len(value) > sqlExtractionMaxProfileBytes || !utf8.ValidString(value) || strings.TrimSpace(value) != value {
		return false
	}
	for _, character := range value {
		if character < 0x20 || character == 0x7f {
			return false
		}
	}
	return true
}

func sqlSourceChunks(source []byte, lineStarts []int) ([]SQLChunk, bool) {
	if len(source) == 0 {
		return nil, false
	}

	limit := len(source)
	if limit > sqlExtractionMaxArtifactTextBytes {
		limit = goSafeUTF8Boundary(source, sqlExtractionMaxArtifactTextBytes)
	}
	capacity := (limit + sqlExtractionMaxChunkBytes - 1) / sqlExtractionMaxChunkBytes
	if capacity > sqlExtractionMaxChunks {
		capacity = sqlExtractionMaxChunks
	}
	chunks := make([]SQLChunk, 0, capacity)
	start := 0
	for start < limit && len(chunks) < sqlExtractionMaxChunks {
		end := start + sqlExtractionMaxChunkBytes
		if end > limit {
			end = limit
		} else {
			end = goSafeUTF8Boundary(source, end)
		}
		if end <= start {
			end = start + sqlExtractionMaxChunkBytes
			if end > limit {
				end = limit
			}
		}
		span, valid := goSpanFromOffsets(lineStarts, len(source), start, end)
		if !valid {
			return chunks, true
		}
		text, textTruncated := goSafeText(source[start:end], sqlExtractionMaxChunkBytes)
		chunks = append(chunks, SQLChunk{
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

type sqlTokenKind uint8

const (
	sqlTokenWord sqlTokenKind = iota + 1
	sqlTokenQuotedIdentifier
	sqlTokenString
	sqlTokenPunctuationKind
)

type sqlToken struct {
	kind       sqlTokenKind
	start, end int
}

func sqlLex(source []byte, lineStarts []int, collector *sqlCollector) ([]sqlToken, bool) {
	tokens := make([]sqlToken, 0, min(sqlExtractionMaxTokens, len(source)/4+1))
	for offset := 0; offset < len(source); {
		if sqlWhitespace(source[offset]) {
			offset++
			continue
		}
		if source[offset] == '-' && offset+1 < len(source) && source[offset+1] == '-' {
			offset += 2
			for offset < len(source) && source[offset] != '\n' {
				offset++
			}
			continue
		}
		if source[offset] == '/' && offset+1 < len(source) && source[offset+1] == '*' {
			start := offset
			offset += 2
			depth := 1
			for offset < len(source) && depth > 0 {
				if source[offset] == '/' && offset+1 < len(source) && source[offset+1] == '*' {
					depth++
					if depth > sqlExtractionMaxDepth {
						collector.limit("DEPTH_LIMIT", sqlSpan(lineStarts, len(source), start, offset+2), "nested SQL comment depth exceeded the bounded parser limit")
						return tokens, false
					}
					offset += 2
					continue
				}
				if source[offset] == '*' && offset+1 < len(source) && source[offset+1] == '/' {
					depth--
					offset += 2
					continue
				}
				offset++
			}
			if depth != 0 {
				collector.limit("SQL_PARSE_ERROR", sqlSpan(lineStarts, len(source), start, len(source)), "unterminated SQL block comment")
				return tokens, false
			}
			continue
		}

		start := offset
		var kind sqlTokenKind
		switch source[offset] {
		case '\'', '"', '`':
			kind = sqlTokenString
			if source[offset] != '\'' {
				kind = sqlTokenQuotedIdentifier
			}
			var complete bool
			offset, complete = sqlQuotedEnd(source, offset, source[start])
			if !complete {
				collector.limit("SQL_PARSE_ERROR", sqlSpan(lineStarts, len(source), start, len(source)), "unterminated SQL quoted value")
				return tokens, false
			}
		case '[':
			kind = sqlTokenQuotedIdentifier
			var complete bool
			offset, complete = sqlBracketIdentifierEnd(source, offset)
			if !complete {
				collector.limit("SQL_PARSE_ERROR", sqlSpan(lineStarts, len(source), start, len(source)), "unterminated bracketed SQL identifier")
				return tokens, false
			}
		case '$':
			if delimiter := sqlDollarQuoteDelimiter(source, offset); len(delimiter) != 0 {
				end := offset + len(delimiter)
				closeOffset := bytes.Index(source[end:], delimiter)
				if closeOffset < 0 {
					collector.limit("SQL_PARSE_ERROR", sqlSpan(lineStarts, len(source), start, len(source)), "unterminated SQL dollar-quoted string")
					return tokens, false
				}
				offset = end + closeOffset + len(delimiter)
				kind = sqlTokenString
			} else {
				offset = sqlWordEnd(source, offset)
				kind = sqlTokenWord
			}
		default:
			if sqlPunctuation(source[offset]) {
				offset++
				kind = sqlTokenPunctuationKind
			} else {
				offset = sqlWordEnd(source, offset)
				kind = sqlTokenWord
			}
		}
		if offset <= start {
			collector.limit("SQL_PARSE_ERROR", sqlSpan(lineStarts, len(source), start, start+1), "SQL lexer made no progress")
			return tokens, false
		}
		if len(tokens) >= sqlExtractionMaxTokens {
			collector.limit("TOKEN_LIMIT", sqlSpan(lineStarts, len(source), start, len(source)), "SQL token count exceeded the bounded parser limit")
			return tokens, false
		}
		tokens = append(tokens, sqlToken{kind: kind, start: start, end: offset})
	}
	return tokens, true
}

func sqlWhitespace(value byte) bool {
	switch value {
	case ' ', '\t', '\r', '\n', '\f':
		return true
	default:
		return false
	}
}

func sqlPunctuation(value byte) bool {
	switch value {
	case '(', ')', ',', ';', '.', '+', '-', '*', '/', '=', '<', '>', '!', ':', '|', '&', '?':
		return true
	default:
		return false
	}
}

func sqlWordEnd(source []byte, start int) int {
	offset := start
	for offset < len(source) {
		if sqlWhitespace(source[offset]) || source[offset] == '\'' || source[offset] == '"' || source[offset] == '`' || source[offset] == '[' || source[offset] == ']' || sqlPunctuation(source[offset]) {
			break
		}
		offset++
	}
	return offset
}

func sqlQuotedEnd(source []byte, start int, quote byte) (int, bool) {
	for offset := start + 1; offset < len(source); offset++ {
		if source[offset] != quote {
			continue
		}
		if offset+1 < len(source) && source[offset+1] == quote {
			offset++
			continue
		}
		return offset + 1, true
	}
	return len(source), false
}

func sqlBracketIdentifierEnd(source []byte, start int) (int, bool) {
	for offset := start + 1; offset < len(source); offset++ {
		if source[offset] != ']' {
			continue
		}
		if offset+1 < len(source) && source[offset+1] == ']' {
			offset++
			continue
		}
		return offset + 1, true
	}
	return len(source), false
}

func sqlDollarQuoteDelimiter(source []byte, start int) []byte {
	if start >= len(source) || source[start] != '$' || start+1 >= len(source) {
		return nil
	}
	if source[start+1] == '$' {
		return source[start : start+2]
	}
	if !sqlDollarTagStart(source[start+1]) {
		return nil
	}
	for offset := start + 2; offset < len(source); offset++ {
		if source[offset] == '$' {
			return source[start : offset+1]
		}
		if !sqlDollarTagContinue(source[offset]) {
			return nil
		}
	}
	return nil
}

func sqlDollarTagStart(value byte) bool {
	return value == '_' || value >= 'a' && value <= 'z' || value >= 'A' && value <= 'Z'
}

func sqlDollarTagContinue(value byte) bool {
	return sqlDollarTagStart(value) || value >= '0' && value <= '9'
}

func sqlExtractStatements(source []byte, lineStarts []int, tokens []sqlToken, collector *sqlCollector) bool {
	statements, complete := sqlSplitStatements(source, lineStarts, tokens, collector)
	parser := sqlParser{source: source, lineStarts: lineStarts, collector: collector}
	for _, statement := range statements {
		if !parser.parseStatement(tokens[statement.tokenStart:statement.tokenEnd], statement.span) {
			complete = false
		}
	}
	return complete
}

type sqlStatement struct {
	tokenStart int
	tokenEnd   int
	span       IndexSpan
}

func sqlSplitStatements(source []byte, lineStarts []int, tokens []sqlToken, collector *sqlCollector) ([]sqlStatement, bool) {
	statements := make([]sqlStatement, 0, min(sqlExtractionMaxStatements, len(tokens)))
	start := 0
	complete := true
	appendStatement := func(end int, spanEnd int) bool {
		if start >= end {
			return true
		}
		if len(statements) >= sqlExtractionMaxStatements {
			collector.limit("STATEMENT_LIMIT", sqlSpan(lineStarts, len(source), tokens[start].start, len(source)), "SQL statement count exceeded the bounded parser limit")
			complete = false
			return false
		}
		statements = append(statements, sqlStatement{
			tokenStart: start,
			tokenEnd:   end,
			span:       sqlSpan(lineStarts, len(source), tokens[start].start, spanEnd),
		})
		return true
	}
	for index, token := range tokens {
		if !sqlTokenPunctuation(source, token, ';') {
			continue
		}
		if !appendStatement(index, token.end) {
			return statements, false
		}
		start = index + 1
	}
	if start < len(tokens) && !appendStatement(len(tokens), tokens[len(tokens)-1].end) {
		complete = false
	}
	return statements, complete
}

type sqlParser struct {
	source     []byte
	lineStarts []int
	collector  *sqlCollector
}

type sqlTableInfo struct {
	name              string
	localKey          string
	symbolKey         string
	constraintOrdinal int
	referenceOrdinal  int
}

func (parser *sqlParser) parseStatement(tokens []sqlToken, span IndexSpan) bool {
	if len(tokens) == 0 {
		return true
	}
	switch {
	case sqlTokenKeyword(parser.source, tokens[0], "CREATE"):
		return parser.parseCreateTable(tokens, span)
	case sqlTokenKeyword(parser.source, tokens[0], "ALTER"):
		return parser.parseAlterTable(tokens, span)
	default:
		parser.collector.limit("UNSUPPORTED_STATEMENT", span, "only lexical CREATE TABLE and ALTER TABLE ADD statements are extracted")
		return false
	}
}

func (parser *sqlParser) parseAlterTable(tokens []sqlToken, statementSpan IndexSpan) bool {
	position := 1
	if position >= len(tokens) || !sqlTokenKeyword(parser.source, tokens[position], "TABLE") {
		parser.collector.limit("UNSUPPORTED_STATEMENT", statementSpan, "only lexical ALTER TABLE ADD statements are extracted")
		return false
	}
	position++
	if position+1 < len(tokens) && sqlTokenKeyword(parser.source, tokens[position], "IF") && sqlTokenKeyword(parser.source, tokens[position+1], "EXISTS") {
		position += 2
	}
	if position < len(tokens) && sqlTokenKeyword(parser.source, tokens[position], "ONLY") {
		position++
	}
	tableName, next, _, valid := parser.parseQualifiedIdentifier(tokens, position)
	if !valid {
		parser.parseError(statementSpan, "ALTER TABLE requires a table identifier")
		return false
	}
	table := sqlTableInfo{
		name:      tableName,
		localKey:  "table:" + tableName,
		symbolKey: "sql:table:" + tableName,
	}
	position = next
	if position >= len(tokens) {
		parser.parseError(statementSpan, "ALTER TABLE requires an action")
		return false
	}
	if !sqlTokenKeyword(parser.source, tokens[position], "ADD") {
		parser.collector.limit("UNSUPPORTED_STATEMENT", statementSpan, "only lexical ALTER TABLE ADD statements are extracted")
		return false
	}
	position++
	if position < len(tokens) && sqlTokenKeyword(parser.source, tokens[position], "COLUMN") {
		position++
	}
	if position+2 < len(tokens) && sqlTokenKeyword(parser.source, tokens[position], "IF") && sqlTokenKeyword(parser.source, tokens[position+1], "NOT") && sqlTokenKeyword(parser.source, tokens[position+2], "EXISTS") {
		position += 3
	}
	if position >= len(tokens) {
		parser.parseError(statementSpan, "ALTER TABLE ADD requires a column or constraint")
		return false
	}
	actionSpan := parser.spanTokens(tokens[position:])
	if sqlTokenKeyword(parser.source, tokens[position], "CONSTRAINT") || sqlTokenKeyword(parser.source, tokens[position], "PRIMARY") || sqlTokenKeyword(parser.source, tokens[position], "UNIQUE") || sqlTokenKeyword(parser.source, tokens[position], "FOREIGN") {
		return parser.parseEntry(&table, tokens[position:], actionSpan)
	}
	return parser.parseColumn(&table, tokens[position:], 0, actionSpan)
}

func (parser *sqlParser) parseCreateTable(tokens []sqlToken, statementSpan IndexSpan) bool {
	position := 1
	if position+1 < len(tokens) && sqlTokenKeyword(parser.source, tokens[position], "OR") && sqlTokenKeyword(parser.source, tokens[position+1], "REPLACE") {
		position += 2
	}
	for position < len(tokens) && sqlCreateTableModifier(parser.source, tokens[position]) {
		position++
	}
	if position >= len(tokens) || !sqlTokenKeyword(parser.source, tokens[position], "TABLE") {
		parser.collector.limit("UNSUPPORTED_STATEMENT", statementSpan, "only lexical CREATE TABLE statements are extracted")
		return false
	}
	position++
	if position+2 < len(tokens) && sqlTokenKeyword(parser.source, tokens[position], "IF") && sqlTokenKeyword(parser.source, tokens[position+1], "NOT") && sqlTokenKeyword(parser.source, tokens[position+2], "EXISTS") {
		position += 3
	}

	tableName, next, _, valid := parser.parseQualifiedIdentifier(tokens, position)
	if !valid {
		parser.parseError(statementSpan, "CREATE TABLE requires a table identifier")
		return false
	}
	table := sqlTableInfo{
		name:      tableName,
		localKey:  "table:" + tableName,
		symbolKey: "sql:table:" + tableName,
	}
	parser.collector.addDefinition(SQLDefinition{
		Kind:      "table",
		SymbolKey: table.symbolKey,
		LocalKey:  table.localKey,
		Span:      statementSpan,
	})
	position = next
	if position >= len(tokens) || !sqlTokenPunctuation(parser.source, tokens[position], '(') {
		parser.parseError(statementSpan, "CREATE TABLE requires a parenthesized table body")
		return false
	}
	close, closed := parser.matchingParen(tokens, position)
	if !closed {
		parser.parseError(statementSpan, "CREATE TABLE body is not balanced")
		return false
	}

	entries, entriesComplete := parser.entryRanges(tokens, position+1, close)
	complete := entriesComplete
	for _, entry := range entries {
		entryTokens := tokens[entry.start:entry.end]
		if !parser.parseEntry(&table, entryTokens, parser.spanTokens(entryTokens)) {
			complete = false
		}
	}
	return complete
}

func sqlCreateTableModifier(source []byte, token sqlToken) bool {
	return sqlTokenKeyword(source, token, "TEMP") || sqlTokenKeyword(source, token, "TEMPORARY") || sqlTokenKeyword(source, token, "UNLOGGED") || sqlTokenKeyword(source, token, "GLOBAL") || sqlTokenKeyword(source, token, "LOCAL")
}

type sqlTokenRange struct {
	start int
	end   int
}

func (parser *sqlParser) entryRanges(tokens []sqlToken, start, end int) ([]sqlTokenRange, bool) {
	if start >= end {
		return nil, true
	}
	ranges := make([]sqlTokenRange, 0, min(sqlExtractionMaxColumns, end-start))
	entryStart := start
	depth := 0
	for index := start; index < end; index++ {
		switch {
		case sqlTokenPunctuation(parser.source, tokens[index], '('):
			depth++
			if depth > sqlExtractionMaxDepth {
				parser.collector.limit("DEPTH_LIMIT", parser.spanTokens(tokens[index:index+1]), "SQL nesting depth exceeded the bounded parser limit")
				return ranges, false
			}
		case sqlTokenPunctuation(parser.source, tokens[index], ')'):
			if depth == 0 {
				parser.parseError(parser.spanTokens(tokens[index:index+1]), "unexpected closing parenthesis in SQL table body")
				return ranges, false
			}
			depth--
		case sqlTokenPunctuation(parser.source, tokens[index], ',') && depth == 0:
			if entryStart < index {
				ranges = append(ranges, sqlTokenRange{start: entryStart, end: index})
			}
			entryStart = index + 1
		}
	}
	if depth != 0 {
		parser.parseError(parser.spanTokens(tokens[start:end]), "unbalanced parenthesis in SQL table body")
		return ranges, false
	}
	if entryStart < end {
		ranges = append(ranges, sqlTokenRange{start: entryStart, end: end})
	}
	return ranges, true
}

func (parser *sqlParser) parseEntry(table *sqlTableInfo, tokens []sqlToken, span IndexSpan) bool {
	if len(tokens) == 0 {
		return true
	}
	position := 0
	if sqlTokenKeyword(parser.source, tokens[position], "CONSTRAINT") {
		position++
		_, next, _, valid := parser.parseIdentifier(tokens, position)
		if !valid {
			parser.parseError(span, "CONSTRAINT requires an identifier")
			return false
		}
		position = next
	}
	if position >= len(tokens) {
		parser.parseError(span, "SQL table entry is incomplete")
		return false
	}

	switch {
	case sqlTokenKeyword(parser.source, tokens[position], "PRIMARY"):
		return parser.parsePrimaryKey(table, tokens, position, span)
	case sqlTokenKeyword(parser.source, tokens[position], "UNIQUE"):
		return parser.parseUnique(table, tokens, position, span)
	case sqlTokenKeyword(parser.source, tokens[position], "FOREIGN"):
		return parser.parseForeignKey(table, tokens, position, span)
	case sqlTokenKeyword(parser.source, tokens[position], "CHECK"), sqlTokenKeyword(parser.source, tokens[position], "EXCLUDE"), sqlTokenKeyword(parser.source, tokens[position], "LIKE"):
		parser.collector.limit("UNSUPPORTED_STATEMENT", span, "SQL table constraint is outside the bounded extraction grammar")
		return false
	default:
		return parser.parseColumn(table, tokens, position, span)
	}
}

func (parser *sqlParser) parsePrimaryKey(table *sqlTableInfo, tokens []sqlToken, position int, span IndexSpan) bool {
	if position+1 >= len(tokens) || !sqlTokenKeyword(parser.source, tokens[position+1], "KEY") {
		parser.parseError(span, "PRIMARY must be followed by KEY")
		return false
	}
	columns, _, valid := parser.parseColumnList(tokens, position+2)
	if !valid {
		return false
	}
	parser.addConstraint(table, "primary_key", columns, span)
	return true
}

func (parser *sqlParser) parseUnique(table *sqlTableInfo, tokens []sqlToken, position int, span IndexSpan) bool {
	position++
	if position+2 < len(tokens) && sqlTokenKeyword(parser.source, tokens[position], "NULLS") && sqlTokenKeyword(parser.source, tokens[position+1], "NOT") && sqlTokenKeyword(parser.source, tokens[position+2], "DISTINCT") {
		position += 3
	}
	columns, _, valid := parser.parseColumnList(tokens, position)
	if !valid {
		return false
	}
	parser.addConstraint(table, "unique", columns, span)
	return true
}

func (parser *sqlParser) parseForeignKey(table *sqlTableInfo, tokens []sqlToken, position int, span IndexSpan) bool {
	if position+1 >= len(tokens) || !sqlTokenKeyword(parser.source, tokens[position+1], "KEY") {
		parser.parseError(span, "FOREIGN must be followed by KEY")
		return false
	}
	columns, next, valid := parser.parseColumnList(tokens, position+2)
	if !valid {
		return false
	}
	if next >= len(tokens) || !sqlTokenKeyword(parser.source, tokens[next], "REFERENCES") {
		parser.parseError(span, "FOREIGN KEY requires a REFERENCES target")
		return false
	}
	_, valid = parser.parseReference(table, columns, tokens, next)
	return valid
}

func (parser *sqlParser) parseColumn(table *sqlTableInfo, tokens []sqlToken, position int, span IndexSpan) bool {
	if !parser.tokensBalanced(tokens, span) {
		return false
	}
	columnName, next, _, valid := parser.parseIdentifier(tokens, position)
	if !valid {
		parser.parseError(span, "SQL column requires an identifier")
		return false
	}
	constraintStart := parser.columnConstraintStart(tokens, next)
	dataType := ""
	if next < constraintStart {
		dataType = sqlTokenText(parser.source, tokens[next:constraintStart])
	} else {
		parser.parseError(span, "SQL column has no lexical type")
	}
	columnLocalKey := "column:" + table.name + "." + columnName
	parser.collector.addDefinition(SQLDefinition{
		Kind:          "column",
		SymbolKey:     "sql:" + columnLocalKey,
		LocalKey:      columnLocalKey,
		OwnerLocalKey: table.localKey,
		DataType:      dataType,
		Span:          span,
	})

	complete := next < constraintStart
	for position = constraintStart; position < len(tokens); {
		switch {
		case sqlTokenKeyword(parser.source, tokens[position], "CONSTRAINT"):
			_, next, _, named := parser.parseIdentifier(tokens, position+1)
			if !named {
				parser.parseError(parser.spanTokens(tokens[position:]), "column CONSTRAINT requires an identifier")
				return false
			}
			position = next
		case sqlTokenKeyword(parser.source, tokens[position], "PRIMARY"):
			if position+1 >= len(tokens) || !sqlTokenKeyword(parser.source, tokens[position+1], "KEY") {
				parser.parseError(parser.spanTokens(tokens[position:]), "PRIMARY must be followed by KEY")
				return false
			}
			parser.addConstraint(table, "primary_key", []string{columnName}, span)
			position += 2
		case sqlTokenKeyword(parser.source, tokens[position], "UNIQUE"):
			parser.addConstraint(table, "unique", []string{columnName}, span)
			position++
		case sqlTokenKeyword(parser.source, tokens[position], "REFERENCES"):
			next, referenceValid := parser.parseReference(table, []string{columnName}, tokens, position)
			if !referenceValid {
				return false
			}
			position = next
		case sqlTokenKeyword(parser.source, tokens[position], "CHECK"):
			if position+1 < len(tokens) && sqlTokenPunctuation(parser.source, tokens[position+1], '(') {
				close, closed := parser.matchingParen(tokens, position+1)
				if !closed {
					parser.parseError(parser.spanTokens(tokens[position:]), "CHECK expression is not balanced")
					return false
				}
				position = close + 1
			} else {
				parser.parseError(parser.spanTokens(tokens[position:]), "CHECK requires a parenthesized expression")
				return false
			}
		default:
			position++
		}
	}
	return complete
}

func (parser *sqlParser) columnConstraintStart(tokens []sqlToken, start int) int {
	depth := 0
	for index := start; index < len(tokens); index++ {
		switch {
		case sqlTokenPunctuation(parser.source, tokens[index], '('):
			depth++
		case sqlTokenPunctuation(parser.source, tokens[index], ')'):
			if depth > 0 {
				depth--
			}
		case depth == 0 && sqlColumnConstraintLead(parser.source, tokens[index]):
			return index
		}
	}
	return len(tokens)
}

func (parser *sqlParser) tokensBalanced(tokens []sqlToken, span IndexSpan) bool {
	depth := 0
	for index := range tokens {
		switch {
		case sqlTokenPunctuation(parser.source, tokens[index], '('):
			depth++
			if depth > sqlExtractionMaxDepth {
				parser.collector.limit("DEPTH_LIMIT", parser.spanTokens(tokens[:index+1]), "SQL nesting depth exceeded the bounded parser limit")
				return false
			}
		case sqlTokenPunctuation(parser.source, tokens[index], ')'):
			if depth == 0 {
				parser.parseError(parser.spanTokens(tokens[:index+1]), "unexpected closing parenthesis in SQL column definition")
				return false
			}
			depth--
		}
	}
	if depth != 0 {
		parser.parseError(span, "SQL column definition is not balanced")
		return false
	}
	return true
}

func sqlColumnConstraintLead(source []byte, token sqlToken) bool {
	return sqlTokenKeyword(source, token, "CONSTRAINT") ||
		sqlTokenKeyword(source, token, "PRIMARY") ||
		sqlTokenKeyword(source, token, "UNIQUE") ||
		sqlTokenKeyword(source, token, "REFERENCES") ||
		sqlTokenKeyword(source, token, "CHECK") ||
		sqlTokenKeyword(source, token, "NOT") ||
		sqlTokenKeyword(source, token, "NULL") ||
		sqlTokenKeyword(source, token, "DEFAULT") ||
		sqlTokenKeyword(source, token, "COLLATE") ||
		sqlTokenKeyword(source, token, "GENERATED") ||
		sqlTokenKeyword(source, token, "IDENTITY") ||
		sqlTokenKeyword(source, token, "DEFERRABLE") ||
		sqlTokenKeyword(source, token, "INITIALLY")
}

func (parser *sqlParser) parseReference(table *sqlTableInfo, columns []string, tokens []sqlToken, position int) (int, bool) {
	if position >= len(tokens) || !sqlTokenKeyword(parser.source, tokens[position], "REFERENCES") {
		return position, false
	}
	start := position
	targetName, next, _, valid := parser.parseQualifiedIdentifier(tokens, position+1)
	if !valid {
		parser.parseError(parser.spanTokens(tokens[start:]), "REFERENCES requires a target table identifier")
		return position, false
	}
	targetColumns := []string(nil)
	if next < len(tokens) && sqlTokenPunctuation(parser.source, tokens[next], '(') {
		targetColumns, next, valid = parser.parseColumnList(tokens, next)
		if !valid {
			return position, false
		}
	}
	end := next
	if end <= start {
		end = start + 1
	}
	localKey, symbolKey := table.nextReferenceKey()
	parser.collector.addReference(SQLReferenceSite{
		Kind:           "foreign_key",
		SymbolKey:      symbolKey,
		LocalKey:       localKey,
		OwnerLocalKey:  table.localKey,
		TargetKey:      "sql:table:" + targetName,
		TargetLocalKey: "table:" + targetName,
		Columns:        columns,
		TargetColumns:  targetColumns,
		Span:           parser.spanTokens(tokens[start:end]),
	})
	return next, true
}

func (parser *sqlParser) parseColumnList(tokens []sqlToken, position int) ([]string, int, bool) {
	if position >= len(tokens) || !sqlTokenPunctuation(parser.source, tokens[position], '(') {
		parser.parseError(parser.spanTokens(tokens[position:]), "SQL constraint requires a parenthesized column list")
		return nil, position, false
	}
	close, closed := parser.matchingParen(tokens, position)
	if !closed {
		parser.parseError(parser.spanTokens(tokens[position:]), "SQL column list is not balanced")
		return nil, position, false
	}
	ranges, complete := parser.entryRanges(tokens, position+1, close)
	if !complete || len(ranges) == 0 {
		if complete {
			parser.parseError(parser.spanTokens(tokens[position:close+1]), "SQL column list is empty")
		}
		return nil, position, false
	}
	columns := make([]string, 0, len(ranges))
	for _, item := range ranges {
		if len(columns) >= sqlExtractionMaxColumns {
			parser.collector.limit("DEFINITION_LIMIT", parser.spanTokens(tokens[item.start:item.end]), "SQL column list exceeded the bounded extraction limit")
			return columns, position, false
		}
		name, next, _, valid := parser.parseIdentifier(tokens, item.start)
		if !valid || next != item.end {
			parser.parseError(parser.spanTokens(tokens[item.start:item.end]), "SQL column list must contain only identifiers")
			return columns, position, false
		}
		columns = append(columns, name)
	}
	return columns, close + 1, true
}

func (parser *sqlParser) parseIdentifier(tokens []sqlToken, position int) (string, int, IndexSpan, bool) {
	if position < 0 || position >= len(tokens) {
		return "", position, IndexSpan{}, false
	}
	token := tokens[position]
	if token.kind != sqlTokenWord && token.kind != sqlTokenQuotedIdentifier {
		return "", position, IndexSpan{}, false
	}
	raw := parser.source[token.start:token.end]
	if len(raw) == 0 || len(raw) > sqlExtractionMaxIdentifierBytes || !sqlIdentifierStart(raw, token.kind) {
		return "", position, IndexSpan{}, false
	}
	name := string(raw)
	if token.kind == sqlTokenWord {
		name = strings.ToLower(name)
	}
	return name, position + 1, parser.spanTokens(tokens[position : position+1]), true
}

func (parser *sqlParser) parseQualifiedIdentifier(tokens []sqlToken, position int) (string, int, IndexSpan, bool) {
	name, next, span, valid := parser.parseIdentifier(tokens, position)
	if !valid {
		return "", position, IndexSpan{}, false
	}
	parts := []string{name}
	for next+1 < len(tokens) && sqlTokenPunctuation(parser.source, tokens[next], '.') {
		part, afterPart, partSpan, partValid := parser.parseIdentifier(tokens, next+1)
		if !partValid {
			return "", position, IndexSpan{}, false
		}
		parts = append(parts, part)
		next = afterPart
		span.ByteEnd = partSpan.ByteEnd
		span.LineEnd = partSpan.LineEnd
	}
	return strings.Join(parts, "."), next, span, true
}

func sqlIdentifierStart(raw []byte, kind sqlTokenKind) bool {
	if kind == sqlTokenQuotedIdentifier {
		return len(raw) >= 2
	}
	if len(raw) == 0 {
		return false
	}
	value := raw[0]
	return value == '_' || value >= 'a' && value <= 'z' || value >= 'A' && value <= 'Z' || value >= 0x80
}

func (parser *sqlParser) matchingParen(tokens []sqlToken, position int) (int, bool) {
	if position < 0 || position >= len(tokens) || !sqlTokenPunctuation(parser.source, tokens[position], '(') {
		return position, false
	}
	depth := 0
	for index := position; index < len(tokens); index++ {
		switch {
		case sqlTokenPunctuation(parser.source, tokens[index], '('):
			depth++
			if depth > sqlExtractionMaxDepth {
				parser.collector.limit("DEPTH_LIMIT", parser.spanTokens(tokens[position:index+1]), "SQL nesting depth exceeded the bounded parser limit")
				return index, false
			}
		case sqlTokenPunctuation(parser.source, tokens[index], ')'):
			depth--
			if depth == 0 {
				return index, true
			}
			if depth < 0 {
				return index, false
			}
		}
	}
	return len(tokens), false
}

func (parser *sqlParser) addConstraint(table *sqlTableInfo, kind string, columns []string, span IndexSpan) {
	if table == nil {
		return
	}
	localKey, symbolKey := table.nextConstraintKey(kind)
	parser.collector.addDefinition(SQLDefinition{
		Kind:          kind,
		SymbolKey:     symbolKey,
		LocalKey:      localKey,
		OwnerLocalKey: table.localKey,
		Columns:       columns,
		Span:          span,
	})
}

func (table *sqlTableInfo) nextConstraintKey(kind string) (string, string) {
	table.constraintOrdinal++
	localKey := "constraint:" + table.name + ":" + kind + ":" + strconv.Itoa(table.constraintOrdinal)
	return localKey, "sql:" + localKey
}

func (table *sqlTableInfo) nextReferenceKey() (string, string) {
	table.referenceOrdinal++
	localKey := "reference:" + table.name + ":foreign_key:" + strconv.Itoa(table.referenceOrdinal)
	return localKey, "sql:" + localKey
}

func (parser *sqlParser) parseError(span IndexSpan, message string) {
	parser.collector.limit("SQL_PARSE_ERROR", span, message)
}

func (parser *sqlParser) spanTokens(tokens []sqlToken) IndexSpan {
	if len(tokens) == 0 {
		return IndexSpan{}
	}
	return sqlSpan(parser.lineStarts, len(parser.source), tokens[0].start, tokens[len(tokens)-1].end)
}

func sqlSpan(lineStarts []int, sourceLength, start, end int) IndexSpan {
	span, valid := goSpanFromOffsets(lineStarts, sourceLength, start, end)
	if !valid {
		return IndexSpan{}
	}
	return span
}

func sqlTokenText(source []byte, tokens []sqlToken) string {
	if len(tokens) == 0 {
		return ""
	}
	return strings.TrimSpace(string(source[tokens[0].start:tokens[len(tokens)-1].end]))
}

func sqlTokenPunctuation(source []byte, token sqlToken, value byte) bool {
	return token.kind == sqlTokenPunctuationKind && token.end-token.start == 1 && source[token.start] == value
}

func sqlTokenKeyword(source []byte, token sqlToken, keyword string) bool {
	if token.kind != sqlTokenWord || token.end-token.start != len(keyword) {
		return false
	}
	for index := range len(keyword) {
		value := source[token.start+index]
		if value >= 'A' && value <= 'Z' {
			value += 'a' - 'A'
		}
		if value != keyword[index] && value != keyword[index]+('a'-'A') {
			return false
		}
	}
	return true
}

func sqlFinalizeArtifact(source []byte, profile SQLExtractionProfile, artifact SQLArtifact) SQLArtifact {
	sort.Slice(artifact.Definitions, func(left, right int) bool {
		return sqlDefinitionLess(artifact.Definitions[left], artifact.Definitions[right])
	})
	sort.Slice(artifact.References, func(left, right int) bool {
		return sqlReferenceLess(artifact.References[left], artifact.References[right])
	})
	sort.Slice(artifact.Chunks, func(left, right int) bool {
		return sqlChunkLess(artifact.Chunks[left], artifact.Chunks[right])
	})
	sort.Slice(artifact.Diagnostics, func(left, right int) bool {
		return sqlDiagnosticLess(artifact.Diagnostics[left], artifact.Diagnostics[right])
	})

	contentDigest := goSourceDigest(source)
	artifact.Proof = IndexArtifactProof{
		ArtifactID:         sqlArtifactID(contentDigest, profile),
		ContentDigest:      contentDigest,
		FactsDigest:        sqlFactsDigest(profile, artifact),
		DefinitionCount:    uint64(len(artifact.Definitions)),
		ReferenceSiteCount: uint64(len(artifact.References)),
		ChunkCount:         uint64(len(artifact.Chunks)),
	}
	return artifact
}

func sqlArtifactID(contentDigest IndexDigest, profile SQLExtractionProfile) string {
	state := sha256.New()
	goWriteHashString(state, "uci-sql-artifact-id/v1")
	goWriteHashString(state, string(contentDigest))
	goWriteHashString(state, profile.ProfileKey)
	goWriteHashString(state, profile.ParserKey)
	goWriteHashString(state, sqlExtractionParserRevision)

	sum := state.Sum(nil)
	var identifier [16]byte
	copy(identifier[:], sum[:len(identifier)])
	identifier[6] = identifier[6]&0x0f | 0x50
	identifier[8] = identifier[8]&0x3f | 0x80
	encoded := hex.EncodeToString(identifier[:])
	return encoded[0:8] + "-" + encoded[8:12] + "-" + encoded[12:16] + "-" + encoded[16:20] + "-" + encoded[20:]
}

func sqlFactsDigest(profile SQLExtractionProfile, artifact SQLArtifact) IndexDigest {
	state := sha256.New()
	goWriteHashString(state, "uci-sql-facts/v1")
	goWriteHashString(state, sqlExtractionSchemaRevision)
	goWriteHashString(state, sqlExtractionParserRevision)
	goWriteHashString(state, profile.ProfileKey)
	goWriteHashString(state, profile.ParserKey)
	goWriteHashString(state, string(artifact.Coverage))
	goWriteHashString(state, artifact.Text)

	goWriteHashUint64(state, uint64(len(artifact.Definitions)))
	for _, definition := range artifact.Definitions {
		goWriteHashString(state, definition.Kind)
		goWriteHashString(state, definition.SymbolKey)
		goWriteHashString(state, definition.LocalKey)
		goWriteHashString(state, definition.OwnerLocalKey)
		goWriteHashString(state, definition.DataType)
		sqlWriteHashStrings(state, definition.Columns)
		goWriteHashSpan(state, definition.Span)
	}

	goWriteHashUint64(state, uint64(len(artifact.References)))
	for _, reference := range artifact.References {
		goWriteHashString(state, reference.Kind)
		goWriteHashString(state, reference.SymbolKey)
		goWriteHashString(state, reference.LocalKey)
		goWriteHashString(state, reference.OwnerLocalKey)
		goWriteHashString(state, reference.TargetKey)
		goWriteHashString(state, reference.TargetLocalKey)
		sqlWriteHashStrings(state, reference.Columns)
		sqlWriteHashStrings(state, reference.TargetColumns)
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

func sqlWriteHashStrings(state hash.Hash, values []string) {
	goWriteHashUint64(state, uint64(len(values)))
	for _, value := range values {
		goWriteHashString(state, value)
	}
}

func sqlDefinitionLess(left, right SQLDefinition) bool {
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
	if left.OwnerLocalKey != right.OwnerLocalKey {
		return left.OwnerLocalKey < right.OwnerLocalKey
	}
	if left.DataType != right.DataType {
		return left.DataType < right.DataType
	}
	return sqlCompareStrings(left.Columns, right.Columns) < 0
}

func sqlReferenceLess(left, right SQLReferenceSite) bool {
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
	if left.OwnerLocalKey != right.OwnerLocalKey {
		return left.OwnerLocalKey < right.OwnerLocalKey
	}
	if left.TargetKey != right.TargetKey {
		return left.TargetKey < right.TargetKey
	}
	if left.TargetLocalKey != right.TargetLocalKey {
		return left.TargetLocalKey < right.TargetLocalKey
	}
	if comparison := sqlCompareStrings(left.Columns, right.Columns); comparison != 0 {
		return comparison < 0
	}
	return sqlCompareStrings(left.TargetColumns, right.TargetColumns) < 0
}

func sqlChunkLess(left, right SQLChunk) bool {
	if comparison := goCompareSpans(left.Span, right.Span); comparison != 0 {
		return comparison < 0
	}
	if left.ContentDigest != right.ContentDigest {
		return left.ContentDigest < right.ContentDigest
	}
	return left.Text < right.Text
}

func sqlDiagnosticLess(left, right SQLDiagnostic) bool {
	if comparison := goCompareSpans(left.Span, right.Span); comparison != 0 {
		return comparison < 0
	}
	if left.Code != right.Code {
		return left.Code < right.Code
	}
	return left.Message < right.Message
}

func sqlCompareStrings(left, right []string) int {
	length := len(left)
	if len(right) < length {
		length = len(right)
	}
	for index := range length {
		if left[index] == right[index] {
			continue
		}
		if left[index] < right[index] {
			return -1
		}
		return 1
	}
	if len(left) < len(right) {
		return -1
	}
	if len(left) > len(right) {
		return 1
	}
	return 0
}

func sqlCloneStrings(values []string) []string {
	if len(values) == 0 {
		return nil
	}
	return append([]string(nil), values...)
}
