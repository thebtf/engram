package uci

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"hash"
	"sort"
	"strings"
	"unicode/utf8"
)

const (
	goExtractionSchemaRevision = "uci-go-extraction/v1"
	goExtractionParserRevision = "go/parser+ast+token/v1/all-errors"

	goExtractionMaxSourceBytes       = 4 << 20
	goExtractionMaxArtifactTextBytes = goExtractionMaxSourceBytes
	goExtractionMaxChunkBytes        = 64 << 10
	goExtractionMaxChunks            = 64
	goExtractionMaxDefinitions       = 2_048
	goExtractionMaxReferences        = 8_192
	goExtractionMaxDiagnostics       = 16
	goExtractionMaxProfileBytes      = 256
	goExtractionMaxDiagnosticBytes   = 512
)

// GoExtractionProfile identifies the versioned extraction and parser policy.
type GoExtractionProfile struct {
	ProfileKey string
	ParserKey  string
}

// GoArtifact is immutable extraction evidence derived solely from source bytes and a profile.
type GoArtifact struct {
	Proof       IndexArtifactProof
	Coverage    IndexCoverageState
	Text        string
	Definitions []GoDefinition
	References  []GoReferenceSite
	Chunks      []GoChunk
	Diagnostics []GoDiagnostic
}

// GoDefinition records one source-defined Go symbol.
type GoDefinition struct {
	Kind      string
	SymbolKey string
	LocalKey  string
	Span      IndexSpan
}

// GoReferenceSite records one import, call, or syntactic reference site.
type GoReferenceSite struct {
	Kind      string
	SymbolKey string
	LocalKey  string
	Span      IndexSpan
}

// GoChunk is a bounded, searchable source-text segment.
type GoChunk struct {
	Span          IndexSpan
	Text          string
	ContentDigest IndexDigest
}

// GoDiagnostic describes a bounded extraction limitation without retaining parser state.
type GoDiagnostic struct {
	Code    string
	Span    IndexSpan
	Message string
}

func goExtractionProfileValid(profile GoExtractionProfile) bool {
	return goExtractionProfileKeyValid(profile.ProfileKey) && goExtractionProfileKeyValid(profile.ParserKey)
}

func goExtractionProfileKeyValid(value string) bool {
	if value == "" || len(value) > goExtractionMaxProfileBytes || !utf8.ValidString(value) || strings.TrimSpace(value) != value {
		return false
	}
	for _, r := range value {
		if r < 0x20 || r == 0x7f {
			return false
		}
	}
	return true
}

func goSafeText(source []byte, limit int) (string, bool) {
	if limit <= 0 {
		return "", len(source) != 0
	}

	truncated := false
	if len(source) > limit {
		source = source[:goSafeUTF8Boundary(source, limit)]
		truncated = true
	}
	text := strings.ToValidUTF8(string(source), "\uFFFD")
	if len(text) <= limit {
		return text, truncated
	}
	return text[:goSafeUTF8Boundary([]byte(text), limit)], true
}

func goSafeUTF8Boundary(value []byte, limit int) int {
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

func goLineStarts(source []byte) []int {
	starts := []int{0}
	for offset, value := range source {
		if value == '\n' {
			starts = append(starts, offset+1)
		}
	}
	return starts
}

func goSpanFromOffsets(lineStarts []int, sourceLength, start, end int) (IndexSpan, bool) {
	if start < 0 || end < start || end > sourceLength {
		return IndexSpan{}, false
	}
	lineEndOffset := start
	if end > start {
		lineEndOffset = end - 1
	}
	return IndexSpan{
		ByteStart: int64(start),
		ByteEnd:   int64(end),
		LineStart: goLineForOffset(lineStarts, start),
		LineEnd:   goLineForOffset(lineStarts, lineEndOffset),
	}, true
}

func goLineForOffset(lineStarts []int, offset int) int {
	if len(lineStarts) == 0 {
		return 1
	}
	if offset < 0 {
		offset = 0
	}
	line := sort.Search(len(lineStarts), func(index int) bool {
		return lineStarts[index] > offset
	}) - 1
	if line < 0 {
		line = 0
	}
	return line + 1
}

func goSourceDigest(source []byte) IndexDigest {
	sum := sha256.Sum256(source)
	return goDigestFromSum(sum)
}

func goDigestFromSum(sum [sha256.Size]byte) IndexDigest {
	return IndexDigest("sha256:" + hex.EncodeToString(sum[:]))
}

func goWriteHashString(state hash.Hash, value string) {
	var length [8]byte
	binary.BigEndian.PutUint64(length[:], uint64(len(value)))
	_, _ = state.Write(length[:])
	_, _ = state.Write([]byte(value))
}

func goWriteHashUint64(state hash.Hash, value uint64) {
	var encoded [8]byte
	binary.BigEndian.PutUint64(encoded[:], value)
	_, _ = state.Write(encoded[:])
}

func goWriteHashSpan(state hash.Hash, span IndexSpan) {
	goWriteHashUint64(state, uint64(span.ByteStart))
	goWriteHashUint64(state, uint64(span.ByteEnd))
	goWriteHashUint64(state, uint64(span.LineStart))
	goWriteHashUint64(state, uint64(span.LineEnd))
}

func goArtifactID(contentDigest IndexDigest, profile GoExtractionProfile) string {
	state := sha256.New()
	goWriteHashString(state, "uci-go-artifact-id/v1")
	goWriteHashString(state, string(contentDigest))
	goWriteHashString(state, profile.ProfileKey)
	goWriteHashString(state, profile.ParserKey)
	goWriteHashString(state, goExtractionParserRevision)

	sum := state.Sum(nil)
	var identifier [16]byte
	copy(identifier[:], sum[:len(identifier)])
	identifier[6] = identifier[6]&0x0f | 0x50
	identifier[8] = identifier[8]&0x3f | 0x80

	encoded := hex.EncodeToString(identifier[:])
	return encoded[0:8] + "-" + encoded[8:12] + "-" + encoded[12:16] + "-" + encoded[16:20] + "-" + encoded[20:]
}

func goFactsDigest(profile GoExtractionProfile, artifact GoArtifact) IndexDigest {
	state := sha256.New()
	goWriteHashString(state, "uci-go-facts/v1")
	goWriteHashString(state, goExtractionSchemaRevision)
	goWriteHashString(state, goExtractionParserRevision)
	goWriteHashString(state, profile.ProfileKey)
	goWriteHashString(state, profile.ParserKey)
	goWriteHashString(state, string(artifact.Coverage))
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

func goFinalizeArtifact(source []byte, profile GoExtractionProfile, artifact GoArtifact) GoArtifact {
	sort.Slice(artifact.Definitions, func(left, right int) bool {
		return goDefinitionLess(artifact.Definitions[left], artifact.Definitions[right])
	})
	sort.Slice(artifact.References, func(left, right int) bool {
		return goReferenceLess(artifact.References[left], artifact.References[right])
	})
	sort.Slice(artifact.Chunks, func(left, right int) bool {
		return goChunkLess(artifact.Chunks[left], artifact.Chunks[right])
	})
	sort.Slice(artifact.Diagnostics, func(left, right int) bool {
		return goDiagnosticLess(artifact.Diagnostics[left], artifact.Diagnostics[right])
	})

	contentDigest := goSourceDigest(source)
	artifact.Proof = IndexArtifactProof{
		ArtifactID:         goArtifactID(contentDigest, profile),
		ContentDigest:      contentDigest,
		FactsDigest:        goFactsDigest(profile, artifact),
		DefinitionCount:    uint64(len(artifact.Definitions)),
		ReferenceSiteCount: uint64(len(artifact.References)),
		ChunkCount:         uint64(len(artifact.Chunks)),
	}
	return artifact
}

func goDefinitionLess(left, right GoDefinition) bool {
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

func goReferenceLess(left, right GoReferenceSite) bool {
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

func goChunkLess(left, right GoChunk) bool {
	if comparison := goCompareSpans(left.Span, right.Span); comparison != 0 {
		return comparison < 0
	}
	if left.ContentDigest != right.ContentDigest {
		return left.ContentDigest < right.ContentDigest
	}
	return left.Text < right.Text
}

func goDiagnosticLess(left, right GoDiagnostic) bool {
	if comparison := goCompareSpans(left.Span, right.Span); comparison != 0 {
		return comparison < 0
	}
	if left.Code != right.Code {
		return left.Code < right.Code
	}
	return left.Message < right.Message
}

func goCompareSpans(left, right IndexSpan) int {
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

func goAddDiagnostic(artifact *GoArtifact, code string, span IndexSpan, message string) {
	if artifact == nil || len(artifact.Diagnostics) >= goExtractionMaxDiagnostics {
		return
	}
	message, _ = goSafeText([]byte(message), goExtractionMaxDiagnosticBytes)
	artifact.Diagnostics = append(artifact.Diagnostics, GoDiagnostic{
		Code:    code,
		Span:    span,
		Message: message,
	})
}
