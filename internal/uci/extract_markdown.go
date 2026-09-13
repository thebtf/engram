package uci

import (
	"crypto/sha256"
	"encoding/hex"
	"sort"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

const (
	markdownExtractionSchemaRevision = "uci-markdown-extraction/v1"
	markdownExtractionParserRevision = "markdown-atx-setext-inline-links/v1"

	markdownExtractionMaxSourceBytes       = 4 << 20
	markdownExtractionMaxArtifactTextBytes = markdownExtractionMaxSourceBytes
	markdownExtractionMaxChunkBytes        = 64 << 10
	markdownExtractionMaxChunks            = 64
	markdownExtractionMaxHeadings          = 2_048
	markdownExtractionMaxReferences        = 8_192
	markdownExtractionMaxDiagnostics       = 16
	markdownExtractionMaxProfileBytes      = 256
	markdownExtractionMaxDiagnosticBytes   = 512
)

// MarkdownExtractionProfile identifies the versioned Markdown extraction policy.
type MarkdownExtractionProfile struct {
	ProfileKey string
	ParserKey  string
}

// DefaultMarkdownExtractionProfile returns the current caller-selected Markdown policy.
func DefaultMarkdownExtractionProfile(profileKey string) MarkdownExtractionProfile {
	return MarkdownExtractionProfile{
		ProfileKey: profileKey,
		ParserKey:  "markdown-parser-v1",
	}
}

// MarkdownArtifact is immutable extraction evidence derived solely from Markdown source bytes and a profile.
type MarkdownArtifact struct {
	Proof       IndexArtifactProof
	Coverage    IndexCoverageState
	Text        string
	Headings    []MarkdownHeading
	References  []MarkdownReferenceSite
	Chunks      []MarkdownChunk
	Diagnostics []MarkdownDiagnostic
}

// MarkdownHeading records one ATX or setext heading definition.
type MarkdownHeading struct {
	SymbolKey string
	LocalKey  string
	Title     string
	Level     int
	Span      IndexSpan
}

// MarkdownReferenceSite records one lexical Markdown link without resolving or fetching its target.
type MarkdownReferenceSite struct {
	Kind            string
	SymbolKey       string
	LocalKey        string
	OwnerLocalKey   string
	RawTarget       string
	TargetPath      string
	Fragment        string
	ResolutionState IndexResolutionState
	Span            IndexSpan
}

// MarkdownChunk is a bounded source-text segment aligned to a Markdown section where possible.
type MarkdownChunk struct {
	Span          IndexSpan
	Text          string
	ContentDigest IndexDigest
}

// MarkdownDiagnostic describes a bounded extraction limitation without retaining parser state.
type MarkdownDiagnostic struct {
	Code    string
	Span    IndexSpan
	Message string
}

// ExtractMarkdown derives deterministic, checkout-independent evidence from one caller-owned Markdown source buffer.
func ExtractMarkdown(source []byte, profile MarkdownExtractionProfile) MarkdownArtifact {
	lineStarts := goLineStarts(source)
	text, textTruncated := goSafeText(source, markdownExtractionMaxArtifactTextBytes)
	artifact := MarkdownArtifact{
		Coverage: IndexCoveragePartial,
		Text:     text,
	}
	if textTruncated {
		markdownAddDiagnostic(&artifact, "TEXT_LIMIT", IndexSpan{}, "source text exceeded the bounded extraction text limit")
	}

	if !utf8.Valid(source) {
		markdownSetFallbackChunks(&artifact, source, lineStarts)
		markdownAddDiagnostic(&artifact, "INVALID_UTF8", IndexSpan{}, "source contains invalid UTF-8")
		return markdownFinalizeArtifact(source, profile, artifact)
	}
	if len(source) > markdownExtractionMaxSourceBytes {
		markdownSetFallbackChunks(&artifact, source, lineStarts)
		markdownAddDiagnostic(&artifact, "SOURCE_LIMIT", IndexSpan{}, "source exceeded the bounded parser input limit")
		return markdownFinalizeArtifact(source, profile, artifact)
	}
	if !markdownExtractionProfileValid(profile) {
		markdownSetFallbackChunks(&artifact, source, lineStarts)
		markdownAddDiagnostic(&artifact, "INVALID_PROFILE", IndexSpan{}, "profile keys must be bounded, valid UTF-8, and nonempty")
		return markdownFinalizeArtifact(source, profile, artifact)
	}

	ignoredLines := markdownIgnoredLines(source, lineStarts)
	headings, headingOwners, sectionStarts, headingsPartial := markdownExtractHeadings(source, lineStarts, ignoredLines)
	references, referencesPartial, referencesLimited := markdownExtractReferenceSites(source, lineStarts, ignoredLines, headingOwners, &artifact)
	chunks, chunksPartial := markdownSourceChunks(source, lineStarts, sectionStarts)

	artifact.Headings = headings
	artifact.References = references
	artifact.Chunks = chunks
	if chunksPartial {
		markdownAddDiagnostic(&artifact, "CHUNK_LIMIT", IndexSpan{}, "source chunks exceeded the bounded extraction chunk limit")
	}
	if headingsPartial {
		markdownAddDiagnostic(&artifact, "HEADING_LIMIT", IndexSpan{}, "heading definitions exceeded the bounded extraction limit")
	}
	if referencesLimited {
		markdownAddDiagnostic(&artifact, "REFERENCE_LIMIT", IndexSpan{}, "link reference sites exceeded the bounded extraction limit")
	}
	if headingsPartial || referencesPartial {
		markdownAddDiagnostic(&artifact, "PARTIAL_FACTS", IndexSpan{}, "some Markdown source facts could not be represented within extraction bounds")
	}
	if !textTruncated && !chunksPartial && !headingsPartial && !referencesPartial {
		artifact.Coverage = IndexCoverageComplete
	}
	return markdownFinalizeArtifact(source, profile, artifact)
}

func markdownSetFallbackChunks(artifact *MarkdownArtifact, source []byte, lineStarts []int) {
	if artifact == nil {
		return
	}
	chunks, truncated := markdownSourceChunks(source, lineStarts, nil)
	artifact.Chunks = chunks
	if truncated {
		markdownAddDiagnostic(artifact, "CHUNK_LIMIT", IndexSpan{}, "source chunks exceeded the bounded extraction chunk limit")
	}
}

func markdownExtractionProfileValid(profile MarkdownExtractionProfile) bool {
	return markdownExtractionProfileKeyValid(profile.ProfileKey) && markdownExtractionProfileKeyValid(profile.ParserKey)
}

func markdownExtractionProfileKeyValid(value string) bool {
	if value == "" || len(value) > markdownExtractionMaxProfileBytes || !utf8.ValidString(value) || strings.TrimSpace(value) != value {
		return false
	}
	for _, value := range value {
		if value < 0x20 || value == 0x7f {
			return false
		}
	}
	return true
}

func markdownIgnoredLines(source []byte, lineStarts []int) []bool {
	ignored := make([]bool, len(lineStarts))
	if len(lineStarts) == 0 {
		return ignored
	}
	markdownIgnoreFrontMatter(source, lineStarts, ignored)
	markdownIgnoreCodeLines(source, lineStarts, ignored)
	return ignored
}

func markdownIgnoreFrontMatter(source []byte, lineStarts []int, ignored []bool) {
	start, end := markdownLineBounds(source, lineStarts, 0)
	if !markdownFrontMatterDelimiter(source[start:end]) {
		return
	}
	for index := 1; index < len(lineStarts); index++ {
		start, end = markdownLineBounds(source, lineStarts, index)
		if !markdownFrontMatterDelimiter(source[start:end]) && !markdownFrontMatterEnd(source[start:end]) {
			continue
		}
		for ignoredIndex := 0; ignoredIndex <= index; ignoredIndex++ {
			ignored[ignoredIndex] = true
		}
		return
	}
}

func markdownIgnoreCodeLines(source []byte, lineStarts []int, ignored []bool) {
	var marker byte
	width := 0
	for index := range lineStarts {
		if ignored[index] {
			continue
		}
		start, end := markdownLineBounds(source, lineStarts, index)
		line := source[start:end]
		if marker != 0 {
			ignored[index] = true
			if markdownFenceCloser(line, marker, width) {
				marker, width = 0, 0
			}
			continue
		}
		if nextMarker, nextWidth, ok := markdownFencePrefix(line); ok {
			ignored[index] = true
			marker, width = nextMarker, nextWidth
			continue
		}
		if markdownIndentedCode(line) {
			ignored[index] = true
		}
	}
}

func markdownFrontMatterDelimiter(line []byte) bool {
	return string(markdownTrimASCIIWhitespace(line)) == "---"
}

func markdownFrontMatterEnd(line []byte) bool {
	trimmed := markdownTrimASCIIWhitespace(line)
	return string(trimmed) == "---" || string(trimmed) == "..."
}

func markdownLineBounds(source []byte, lineStarts []int, index int) (int, int) {
	if index < 0 || index >= len(lineStarts) {
		return 0, 0
	}
	start := lineStarts[index]
	end := len(source)
	if index+1 < len(lineStarts) {
		end = lineStarts[index+1] - 1
	}
	if end > start && source[end-1] == '\r' {
		end--
	}
	return start, end
}

func markdownFencePrefix(line []byte) (byte, int, bool) {
	start, valid := markdownIndentStart(line)
	if !valid || start >= len(line) || (line[start] != '`' && line[start] != '~') {
		return 0, 0, false
	}
	marker := line[start]
	end := start
	for end < len(line) && line[end] == marker {
		end++
	}
	if end-start < 3 {
		return 0, 0, false
	}
	return marker, end - start, true
}

func markdownFenceCloser(line []byte, marker byte, width int) bool {
	foundMarker, foundWidth, ok := markdownFencePrefix(line)
	if !ok || foundMarker != marker || foundWidth < width {
		return false
	}
	start, _ := markdownIndentStart(line)
	for index := start + foundWidth; index < len(line); index++ {
		if line[index] != ' ' && line[index] != '\t' {
			return false
		}
	}
	return true
}

func markdownIndentedCode(line []byte) bool {
	spaces := 0
	for spaces < len(line) && line[spaces] == ' ' {
		spaces++
	}
	return spaces >= 4 && spaces < len(line)
}

func markdownIndentStart(line []byte) (int, bool) {
	index := 0
	for index < len(line) && line[index] == ' ' {
		index++
	}
	return index, index <= 3
}

func markdownExtractHeadings(source []byte, lineStarts []int, ignored []bool) ([]MarkdownHeading, map[int]string, []int, bool) {
	capacity := len(lineStarts)
	if capacity > markdownExtractionMaxHeadings {
		capacity = markdownExtractionMaxHeadings
	}
	headings := make([]MarkdownHeading, 0, capacity)
	headingOwners := make(map[int]string, capacity)
	sectionStarts := make([]int, 0, capacity)
	occurrences := make(map[string]int, capacity)
	usedSlugs := make(map[string]struct{}, capacity)
	partial := false

	for index := range lineStarts {
		if index < len(ignored) && ignored[index] {
			continue
		}
		lineStart, lineEnd := markdownLineBounds(source, lineStarts, index)
		line := source[lineStart:lineEnd]

		level, titleStart, titleEnd, headingStart, headingEnd, ok := markdownHeadingAtLine(source, lineStarts, ignored, index, lineStart, lineEnd, line)
		if !ok {
			continue
		}
		span, valid := goSpanFromOffsets(lineStarts, len(source), headingStart, headingEnd)
		if !valid {
			partial = true
			headingOwners[index] = ""
			continue
		}

		title := string(source[titleStart:titleEnd])
		localKey := markdownHeadingLocalKey(title, occurrences, usedSlugs)
		if len(headings) >= markdownExtractionMaxHeadings {
			partial = true
			headingOwners[index] = ""
			continue
		}
		headings = append(headings, MarkdownHeading{
			SymbolKey: "markdown:" + localKey,
			LocalKey:  localKey,
			Title:     title,
			Level:     level,
			Span:      span,
		})
		headingOwners[index] = localKey
		sectionStarts = append(sectionStarts, headingStart)
	}
	return headings, headingOwners, sectionStarts, partial
}

func markdownHeadingAtLine(source []byte, lineStarts []int, ignored []bool, index, lineStart, lineEnd int, line []byte) (int, int, int, int, int, bool) {
	level, titleStart, titleEnd, ok := markdownATXHeading(line)
	if ok {
		return level, lineStart + titleStart, lineStart + titleEnd, lineStart, lineEnd, true
	}
	if index+1 >= len(lineStarts) || (index+1 < len(ignored) && ignored[index+1]) {
		return 0, 0, 0, 0, 0, false
	}
	nextStart, nextEnd := markdownLineBounds(source, lineStarts, index+1)
	nextLine := source[nextStart:nextEnd]
	level, ok = markdownSetextLevel(nextLine)
	if !ok {
		return 0, 0, 0, 0, 0, false
	}
	titleStart, titleEnd, ok = markdownSetextTitleBounds(line)
	if !ok {
		return 0, 0, 0, 0, 0, false
	}
	return level, lineStart + titleStart, lineStart + titleEnd, lineStart, nextEnd, true
}

func markdownATXHeading(line []byte) (int, int, int, bool) {
	start, valid := markdownIndentStart(line)
	if !valid || start >= len(line) || line[start] != '#' {
		return 0, 0, 0, false
	}
	end := start
	for end < len(line) && line[end] == '#' {
		end++
	}
	level := end - start
	if level == 0 || level > 6 || end >= len(line) || !markdownInlineWhitespace(line[end]) {
		return 0, 0, 0, false
	}
	titleStart := markdownSkipInlineWhitespace(line, end)
	titleEnd := markdownTrimInlineWhitespace(line, len(line))
	closingStart := titleEnd
	for closingStart > titleStart && line[closingStart-1] == '#' {
		closingStart--
	}
	if closingStart < titleEnd && closingStart > titleStart && markdownInlineWhitespace(line[closingStart-1]) {
		titleEnd = markdownTrimInlineWhitespace(line, closingStart)
	}
	return level, titleStart, titleEnd, true
}

func markdownSetextLevel(line []byte) (int, bool) {
	trimmed := markdownTrimASCIIWhitespace(line)
	if len(trimmed) == 0 || (trimmed[0] != '=' && trimmed[0] != '-') {
		return 0, false
	}
	marker := trimmed[0]
	for _, value := range trimmed {
		if value != marker {
			return 0, false
		}
	}
	if marker == '=' {
		return 1, true
	}
	return 2, true
}

func markdownSetextTitleBounds(line []byte) (int, int, bool) {
	start := markdownSkipInlineWhitespace(line, 0)
	end := markdownTrimInlineWhitespace(line, len(line))
	if start >= end {
		return 0, 0, false
	}
	if _, _, _, atx := markdownATXHeading(line); atx {
		return 0, 0, false
	}
	if _, underline := markdownSetextLevel(line); underline {
		return 0, 0, false
	}
	return start, end, true
}

func markdownHeadingLocalKey(title string, occurrences map[string]int, usedSlugs map[string]struct{}) string {
	slug := markdownHeadingSlug(title)
	for occurrence := occurrences[slug] + 1; ; occurrence++ {
		candidate := slug
		if occurrence > 1 {
			candidate += "-" + strconv.Itoa(occurrence)
		}
		if _, used := usedSlugs[candidate]; used {
			continue
		}
		occurrences[slug] = occurrence
		usedSlugs[candidate] = struct{}{}
		return "heading:" + candidate
	}
}

func markdownHeadingSlug(title string) string {
	var builder strings.Builder
	separator := false
	for _, value := range strings.TrimSpace(title) {
		if unicode.IsLetter(value) || unicode.IsNumber(value) {
			if separator && builder.Len() > 0 {
				builder.WriteByte('-')
			}
			builder.WriteRune(unicode.ToLower(value))
			separator = false
			continue
		}
		if builder.Len() > 0 {
			separator = true
		}
	}
	if builder.Len() != 0 {
		return builder.String()
	}
	sum := sha256.Sum256([]byte(title))
	return "section-" + hex.EncodeToString(sum[:8])
}

func markdownExtractReferenceSites(source []byte, lineStarts []int, ignored []bool, headingOwners map[int]string, artifact *MarkdownArtifact) ([]MarkdownReferenceSite, bool, bool) {
	capacity := len(lineStarts)
	if capacity > markdownExtractionMaxReferences {
		capacity = markdownExtractionMaxReferences
	}
	references := make([]MarkdownReferenceSite, 0, capacity)
	ownerLocalKey := "preamble"
	partial := false
	limited := false

	for index := range lineStarts {
		if owner, isHeading := headingOwners[index]; isHeading {
			ownerLocalKey = owner
		}
		if index < len(ignored) && ignored[index] {
			continue
		}
		lineStart, lineEnd := markdownLineBounds(source, lineStarts, index)
		linePartial, lineLimited := markdownExtractReferenceLine(source[lineStart:lineEnd], lineStart, lineStarts, len(source), ownerLocalKey, &references, artifact)
		if linePartial {
			partial = true
		}
		if lineLimited {
			limited = true
		}
	}
	return references, partial, limited
}

func markdownExtractReferenceLine(line []byte, lineStart int, lineStarts []int, sourceLength int, ownerLocalKey string, references *[]MarkdownReferenceSite, artifact *MarkdownArtifact) (bool, bool) {
	partial := false
	limited := false
	for index := 0; index < len(line); {
		if line[index] == '\\' {
			if index+1 < len(line) {
				index += 2
				continue
			}
			index++
			continue
		}
		if line[index] == '`' {
			width := markdownRunLength(line, index, '`')
			if closing := markdownMatchingRun(line, index+width, '`', width); closing >= 0 {
				index = closing + width
				continue
			}
			index += width
			continue
		}
		if line[index] != '[' {
			index++
			continue
		}

		labelEnd, valid := markdownClosingBracket(line, index+1)
		if !valid || labelEnd+1 >= len(line) || line[labelEnd+1] != '(' {
			index++
			continue
		}
		spanStart := index
		if index > 0 && line[index-1] == '!' && !markdownEscaped(line, index-1) {
			spanStart--
		}
		rawStart, rawEnd, closing, valid := markdownInlineDestination(line, labelEnd+1)
		if !valid {
			if markdownInlineDestinationUnterminated(line, labelEnd+1) {
				span, spanValid := goSpanFromOffsets(lineStarts, sourceLength, lineStart+spanStart, lineStart+len(line))
				if !spanValid {
					span = IndexSpan{}
				}
				markdownAddDiagnostic(artifact, "MARKDOWN_LINK_UNTERMINATED", span, "inline Markdown link destination did not terminate on its source line")
				partial = true
			}
			index++
			continue
		}
		rawTarget := string(line[rawStart:rawEnd])
		if rawTarget != "" {
			span, spanValid := goSpanFromOffsets(lineStarts, sourceLength, lineStart+spanStart, lineStart+closing+1)
			if !spanValid {
				partial = true
			} else {
				targetPath, fragment := markdownTargetParts(rawTarget)
				localKey := "link:" + rawTarget + "@" + strconv.FormatInt(span.ByteStart, 10)
				kind := "local_link"
				if markdownExternalTarget(targetPath) {
					kind = "external_link"
					targetPath = ""
					fragment = ""
				}
				if !markdownAppendReference(references, MarkdownReferenceSite{
					Kind:            kind,
					SymbolKey:       "markdown:" + localKey,
					LocalKey:        localKey,
					OwnerLocalKey:   ownerLocalKey,
					RawTarget:       rawTarget,
					TargetPath:      targetPath,
					Fragment:        fragment,
					ResolutionState: IndexResolutionState("unresolved"),
					Span:            span,
				}) {
					partial = true
					limited = true
				}
			}
		}
		index = closing + 1
	}
	return partial, limited
}

func markdownRunLength(line []byte, start int, value byte) int {
	end := start
	for end < len(line) && line[end] == value {
		end++
	}
	return end - start
}

func markdownMatchingRun(line []byte, start int, value byte, width int) int {
	for index := start; index < len(line); {
		if line[index] != value {
			index++
			continue
		}
		if markdownRunLength(line, index, value) == width {
			return index
		}
		index += markdownRunLength(line, index, value)
	}
	return -1
}

func markdownClosingBracket(line []byte, start int) (int, bool) {
	depth := 1
	for index := start; index < len(line); index++ {
		if line[index] == '\\' {
			index++
			continue
		}
		switch line[index] {
		case '[':
			depth++
		case ']':
			depth--
			if depth == 0 {
				return index, true
			}
		}
	}
	return 0, false
}

func markdownInlineDestination(line []byte, opening int) (int, int, int, bool) {
	index := markdownSkipInlineWhitespace(line, opening+1)
	if index >= len(line) || line[index] == ')' {
		return 0, 0, 0, false
	}
	if line[index] == '<' {
		return markdownAngleDestination(line, index)
	}
	return markdownBareDestination(line, index)
}

func markdownAngleDestination(line []byte, opening int) (int, int, int, bool) {
	rawStart := opening + 1
	for index := rawStart; index < len(line); index++ {
		if line[index] == '\\' {
			index++
			continue
		}
		if line[index] != '>' {
			continue
		}
		closing, valid := markdownLinkCloseAfterTitle(line, index+1)
		if !valid || rawStart == index {
			return 0, 0, 0, false
		}
		return rawStart, index, closing, true
	}
	return 0, 0, 0, false
}

func markdownBareDestination(line []byte, rawStart int) (int, int, int, bool) {
	depth := 0
	for index := rawStart; index < len(line); index++ {
		if line[index] == '\\' {
			index++
			continue
		}
		if line[index] == '(' {
			depth++
			continue
		}
		if line[index] == ')' {
			if depth == 0 {
				if rawStart == index {
					return 0, 0, 0, false
				}
				return rawStart, index, index, true
			}
			depth--
			continue
		}
		if markdownInlineWhitespace(line[index]) && depth == 0 {
			return markdownBareTitleClose(line, rawStart, index)
		}
	}
	return 0, 0, 0, false
}

func markdownBareTitleClose(line []byte, rawStart, index int) (int, int, int, bool) {
	closing, valid := markdownLinkCloseAfterTitle(line, index)
	if !valid || rawStart == index {
		return 0, 0, 0, false
	}
	return rawStart, index, closing, true
}

func markdownInlineDestinationUnterminated(line []byte, opening int) bool {
	depth := 0
	for index := opening + 1; index < len(line); index++ {
		if line[index] == '\\' {
			index++
			continue
		}
		switch line[index] {
		case '(':
			depth++
		case ')':
			if depth == 0 {
				return false
			}
			depth--
		}
	}
	return true
}

func markdownLinkCloseAfterTitle(line []byte, start int) (int, bool) {
	for index := start; ; {
		index = markdownSkipInlineWhitespace(line, index)
		if index >= len(line) {
			return 0, false
		}
		if line[index] == ')' {
			return index, true
		}
		if line[index] == '\'' || line[index] == '"' {
			next, closed := markdownQuotedTitleEnd(line, index)
			if !closed {
				return 0, false
			}
			index = next
			continue
		}
		index = markdownUnquotedTitleEnd(line, index)
	}
}

func markdownQuotedTitleEnd(line []byte, start int) (int, bool) {
	quote := line[start]
	for index := start + 1; index < len(line); index++ {
		if line[index] == '\\' {
			index++
			continue
		}
		if line[index] == quote {
			return index + 1, true
		}
	}
	return 0, false
}

func markdownUnquotedTitleEnd(line []byte, start int) int {
	for start < len(line) && !markdownInlineWhitespace(line[start]) && line[start] != ')' {
		if line[start] == '\\' && start+1 < len(line) {
			start += 2
			continue
		}
		start++
	}
	return start
}

func markdownTargetParts(rawTarget string) (string, string) {
	if split := strings.IndexByte(rawTarget, '#'); split >= 0 {
		return rawTarget[:split], rawTarget[split+1:]
	}
	return rawTarget, ""
}

func markdownExternalTarget(targetPath string) bool {
	if strings.HasPrefix(targetPath, "//") {
		return true
	}
	separator := strings.IndexByte(targetPath, ':')
	if separator <= 0 || !markdownSchemeStart(targetPath[0]) {
		return false
	}
	for index := 1; index < separator; index++ {
		value := targetPath[index]
		if !markdownSchemeStart(value) && (value < '0' || value > '9') && value != '+' && value != '-' && value != '.' {
			return false
		}
	}
	return true
}

func markdownSchemeStart(value byte) bool {
	return value >= 'a' && value <= 'z' || value >= 'A' && value <= 'Z'
}

func markdownEscaped(line []byte, index int) bool {
	backslashes := 0
	for index > 0 && line[index-1] == '\\' {
		backslashes++
		index--
	}
	return backslashes%2 != 0
}

func markdownInlineWhitespace(value byte) bool {
	return value == ' ' || value == '\t'
}

func markdownSkipInlineWhitespace(line []byte, start int) int {
	for start < len(line) && markdownInlineWhitespace(line[start]) {
		start++
	}
	return start
}

func markdownTrimInlineWhitespace(line []byte, end int) int {
	for end > 0 && markdownInlineWhitespace(line[end-1]) {
		end--
	}
	return end
}

func markdownTrimASCIIWhitespace(line []byte) []byte {
	start := markdownSkipInlineWhitespace(line, 0)
	end := markdownTrimInlineWhitespace(line, len(line))
	return line[start:end]
}

func markdownAppendReference(references *[]MarkdownReferenceSite, reference MarkdownReferenceSite) bool {
	if len(*references) >= markdownExtractionMaxReferences {
		return false
	}
	*references = append(*references, reference)
	return true
}

func markdownSourceChunks(source []byte, lineStarts []int, sectionStarts []int) ([]MarkdownChunk, bool) {
	if len(source) == 0 {
		return nil, false
	}
	starts := markdownChunkStarts(source, sectionStarts)
	chunks := make([]MarkdownChunk, 0, markdownChunkCapacity(source, starts))
	for index, start := range starts {
		end := len(source)
		if index+1 < len(starts) {
			end = starts[index+1]
		}
		var truncated bool
		chunks, truncated = markdownAppendSectionChunks(chunks, source, lineStarts, start, end)
		if truncated {
			return chunks, true
		}
	}
	return chunks, false
}

func markdownChunkStarts(source []byte, sectionStarts []int) []int {
	starts := make([]int, 0, len(sectionStarts)+1)
	starts = append(starts, 0)
	for _, start := range sectionStarts {
		if start > starts[len(starts)-1] && start < len(source) {
			starts = append(starts, start)
		}
	}
	return starts
}

func markdownChunkCapacity(source []byte, starts []int) int {
	capacity := (len(source) + markdownExtractionMaxChunkBytes - 1) / markdownExtractionMaxChunkBytes
	if capacity < len(starts) {
		capacity = len(starts)
	}
	return min(capacity, markdownExtractionMaxChunks)
}

func markdownAppendSectionChunks(chunks []MarkdownChunk, source []byte, lineStarts []int, start, end int) ([]MarkdownChunk, bool) {
	for offset := start; offset < end; {
		if len(chunks) >= markdownExtractionMaxChunks {
			return chunks, true
		}
		chunk, next, truncated := markdownChunkAt(source, lineStarts, offset, end)
		if next <= offset {
			return chunks, true
		}
		chunks = append(chunks, chunk)
		if truncated {
			return chunks, true
		}
		offset = next
	}
	return chunks, false
}

func markdownChunkAt(source []byte, lineStarts []int, start, end int) (MarkdownChunk, int, bool) {
	chunkEnd := markdownChunkEnd(source, start, end)
	if chunkEnd <= start {
		return MarkdownChunk{}, start, true
	}
	span, valid := goSpanFromOffsets(lineStarts, len(source), start, chunkEnd)
	if !valid {
		return MarkdownChunk{}, start, true
	}
	text, truncated := goSafeText(source[start:chunkEnd], markdownExtractionMaxChunkBytes)
	return MarkdownChunk{Span: span, Text: text, ContentDigest: goSourceDigest(source[start:chunkEnd])}, chunkEnd, truncated
}

func markdownChunkEnd(source []byte, start, sectionEnd int) int {
	end := start + markdownExtractionMaxChunkBytes
	if end >= sectionEnd {
		return sectionEnd
	}
	end = goSafeUTF8Boundary(source, end)
	if end <= start {
		end = start + markdownExtractionMaxChunkBytes
		if end > sectionEnd {
			end = sectionEnd
		}
	}
	for boundary := end; boundary > start; boundary-- {
		if source[boundary-1] == '\n' {
			return boundary
		}
	}
	return end
}

func markdownArtifactID(contentDigest IndexDigest, profile MarkdownExtractionProfile) string {
	state := sha256.New()
	goWriteHashString(state, "uci-markdown-artifact-id/v1")
	goWriteHashString(state, string(contentDigest))
	goWriteHashString(state, profile.ProfileKey)
	goWriteHashString(state, profile.ParserKey)
	goWriteHashString(state, markdownExtractionParserRevision)

	sum := state.Sum(nil)
	var identifier [16]byte
	copy(identifier[:], sum[:len(identifier)])
	identifier[6] = identifier[6]&0x0f | 0x50
	identifier[8] = identifier[8]&0x3f | 0x80

	encoded := hex.EncodeToString(identifier[:])
	return encoded[0:8] + "-" + encoded[8:12] + "-" + encoded[12:16] + "-" + encoded[16:20] + "-" + encoded[20:]
}

func markdownFactsDigest(profile MarkdownExtractionProfile, artifact MarkdownArtifact) IndexDigest {
	state := sha256.New()
	goWriteHashString(state, "uci-markdown-facts/v1")
	goWriteHashString(state, markdownExtractionSchemaRevision)
	goWriteHashString(state, markdownExtractionParserRevision)
	goWriteHashString(state, profile.ProfileKey)
	goWriteHashString(state, profile.ParserKey)
	goWriteHashString(state, string(artifact.Coverage))
	goWriteHashString(state, artifact.Text)

	goWriteHashUint64(state, uint64(len(artifact.Headings)))
	for _, heading := range artifact.Headings {
		goWriteHashString(state, heading.SymbolKey)
		goWriteHashString(state, heading.LocalKey)
		goWriteHashString(state, heading.Title)
		goWriteHashUint64(state, uint64(heading.Level))
		goWriteHashSpan(state, heading.Span)
	}

	goWriteHashUint64(state, uint64(len(artifact.References)))
	for _, reference := range artifact.References {
		goWriteHashString(state, reference.Kind)
		goWriteHashString(state, reference.SymbolKey)
		goWriteHashString(state, reference.LocalKey)
		goWriteHashString(state, reference.OwnerLocalKey)
		goWriteHashString(state, reference.RawTarget)
		goWriteHashString(state, reference.TargetPath)
		goWriteHashString(state, reference.Fragment)
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

func markdownFinalizeArtifact(source []byte, profile MarkdownExtractionProfile, artifact MarkdownArtifact) MarkdownArtifact {
	sort.Slice(artifact.Headings, func(left, right int) bool {
		return markdownHeadingLess(artifact.Headings[left], artifact.Headings[right])
	})
	sort.Slice(artifact.References, func(left, right int) bool {
		return markdownReferenceLess(artifact.References[left], artifact.References[right])
	})
	sort.Slice(artifact.Chunks, func(left, right int) bool {
		return markdownChunkLess(artifact.Chunks[left], artifact.Chunks[right])
	})
	sort.Slice(artifact.Diagnostics, func(left, right int) bool {
		return markdownDiagnosticLess(artifact.Diagnostics[left], artifact.Diagnostics[right])
	})

	contentDigest := goSourceDigest(source)
	artifact.Proof = IndexArtifactProof{
		ArtifactID:         markdownArtifactID(contentDigest, profile),
		ContentDigest:      contentDigest,
		FactsDigest:        markdownFactsDigest(profile, artifact),
		DefinitionCount:    uint64(len(artifact.Headings)),
		ReferenceSiteCount: uint64(len(artifact.References)),
		ChunkCount:         uint64(len(artifact.Chunks)),
	}
	return artifact
}

func markdownHeadingLess(left, right MarkdownHeading) bool {
	if comparison := goCompareSpans(left.Span, right.Span); comparison != 0 {
		return comparison < 0
	}
	if left.SymbolKey != right.SymbolKey {
		return left.SymbolKey < right.SymbolKey
	}
	if left.LocalKey != right.LocalKey {
		return left.LocalKey < right.LocalKey
	}
	if left.Title != right.Title {
		return left.Title < right.Title
	}
	return left.Level < right.Level
}

func markdownReferenceLess(left, right MarkdownReferenceSite) bool {
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
	if left.RawTarget != right.RawTarget {
		return left.RawTarget < right.RawTarget
	}
	if left.TargetPath != right.TargetPath {
		return left.TargetPath < right.TargetPath
	}
	if left.Fragment != right.Fragment {
		return left.Fragment < right.Fragment
	}
	return left.ResolutionState < right.ResolutionState
}

func markdownChunkLess(left, right MarkdownChunk) bool {
	if comparison := goCompareSpans(left.Span, right.Span); comparison != 0 {
		return comparison < 0
	}
	if left.ContentDigest != right.ContentDigest {
		return left.ContentDigest < right.ContentDigest
	}
	return left.Text < right.Text
}

func markdownDiagnosticLess(left, right MarkdownDiagnostic) bool {
	if comparison := goCompareSpans(left.Span, right.Span); comparison != 0 {
		return comparison < 0
	}
	if left.Code != right.Code {
		return left.Code < right.Code
	}
	return left.Message < right.Message
}

func markdownAddDiagnostic(artifact *MarkdownArtifact, code string, span IndexSpan, message string) {
	if artifact == nil || len(artifact.Diagnostics) >= markdownExtractionMaxDiagnostics {
		return
	}
	message, _ = goSafeText([]byte(message), markdownExtractionMaxDiagnosticBytes)
	artifact.Diagnostics = append(artifact.Diagnostics, MarkdownDiagnostic{
		Code:    code,
		Span:    span,
		Message: message,
	})
}
