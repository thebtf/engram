package uci

import (
	"bytes"
	"reflect"
	"strings"
	"testing"
)

const uciMarkdownExtractionFixture = "# Overview\r\n" +
	"\r\n" +
	"Résumé prose links to an exact document, not an implementation claim.\r\n" +
	"\r\n" +
	"[guide](docs/guide.md#install)\r\n" +
	"[same document](#overview)\r\n" +
	"[missing](missing.md#absent)\r\n" +
	"[ambiguous heading](#overview)\r\n" +
	"\r\n" +
	"Overview\r\n" +
	"========\r\n" +
	"\r\n" +
	"More prose: 你好, 世界.\r\n" +
	"\r\n" +
	"## External references\r\n" +
	"\r\n" +
	"[web](https://example.test/spec#section)\r\n" +
	"[file](file:///tmp/source.md#section)\r\n"

func TestUCIMarkdownExtractionProducesStableHeadingLinkAndProseFacts(t *testing.T) {
	source := []byte(uciMarkdownExtractionFixture)
	profile := uciMarkdownExtractionProfile()

	first := ExtractMarkdown(source, profile)
	second := ExtractMarkdown(append([]byte(nil), source...), profile)

	if first.Coverage != IndexCoverageComplete {
		t.Fatalf("ExtractMarkdown() coverage = %q, want %q", first.Coverage, IndexCoverageComplete)
	}
	if first.Text != string(source) {
		t.Fatalf("ExtractMarkdown() text = %q, want exact source bytes", first.Text)
	}
	if !reflect.DeepEqual(first, second) {
		t.Fatalf("ExtractMarkdown() facts are not stable across identical byte/profile input:\nfirst: %#v\nsecond: %#v", first, second)
	}
	uciRequireMarkdownExtractionProof(t, first)

	atx := uciRequireMarkdownHeading(t, first.Headings, "Overview", 1, 0, uciMarkdownExtractionSpan(t, source, "# Overview", 0))
	setext := uciRequireMarkdownHeading(t, first.Headings, "Overview", 1, 1, uciMarkdownExtractionSpan(t, source, "Overview\r\n========", 0))
	external := uciRequireMarkdownHeading(t, first.Headings, "External references", 2, 0, uciMarkdownExtractionSpan(t, source, "## External references", 0))
	if atx.LocalKey == setext.LocalKey || atx.SymbolKey == setext.SymbolKey {
		t.Fatalf("duplicate headings need distinct deterministic keys: atx=%#v setext=%#v", atx, setext)
	}

	uciRequireMarkdownReference(t, first.References, "local_link", atx.LocalKey, "docs/guide.md#install", "docs/guide.md", "install", uciMarkdownExtractionSpan(t, source, "[guide](docs/guide.md#install)", 0))
	sameDocument := uciRequireMarkdownReference(t, first.References, "local_link", atx.LocalKey, "#overview", "", "overview", uciMarkdownExtractionSpan(t, source, "[same document](#overview)", 0))
	uciRequireMarkdownReference(t, first.References, "local_link", atx.LocalKey, "missing.md#absent", "missing.md", "absent", uciMarkdownExtractionSpan(t, source, "[missing](missing.md#absent)", 0))
	ambiguous := uciRequireMarkdownReference(t, first.References, "local_link", atx.LocalKey, "#overview", "", "overview", uciMarkdownExtractionSpan(t, source, "[ambiguous heading](#overview)", 0))
	if sameDocument.LocalKey == ambiguous.LocalKey || sameDocument.SymbolKey == ambiguous.SymbolKey {
		t.Fatalf("duplicate link sites need distinct deterministic keys: same=%#v ambiguous=%#v", sameDocument, ambiguous)
	}
	uciRequireMarkdownReference(t, first.References, "external_link", external.LocalKey, "https://example.test/spec#section", "", "", uciMarkdownExtractionSpan(t, source, "[web](https://example.test/spec#section)", 0))
	uciRequireMarkdownReference(t, first.References, "external_link", external.LocalKey, "file:///tmp/source.md#section", "", "", uciMarkdownExtractionSpan(t, source, "[file](file:///tmp/source.md#section)", 0))
	if len(first.References) != 6 {
		t.Fatalf("ExtractMarkdown() reference count = %d, want only six observed link facts", len(first.References))
	}

	uciRequireMarkdownChunkContaining(t, first.Chunks, "Résumé prose links to an exact document, not an implementation claim.")
	uciRequireMarkdownChunkContaining(t, first.Chunks, "More prose: 你好, 世界.")
	uciRequireMarkdownChunkCovering(t, first.Chunks, atx.Span, "ATX heading")
	uciRequireMarkdownChunkCovering(t, first.Chunks, setext.Span, "setext heading")
}

func TestUCIMarkdownExtractionSeparatesArtifactIdentityFromMembership(t *testing.T) {
	source := []byte(uciMarkdownExtractionFixture)
	profile := uciMarkdownExtractionProfile()

	first := ExtractMarkdown(source, profile)
	reused := ExtractMarkdown(append([]byte(nil), source...), profile)
	uciRequireMarkdownExtractionProof(t, first)
	if !reflect.DeepEqual(first.Proof, reused.Proof) {
		t.Fatalf("identical source bytes/profile produced different artifact proof: first=%#v reused=%#v", first.Proof, reused.Proof)
	}

	firstArtifactID := first.Proof.ArtifactID
	reusedArtifactID := reused.Proof.ArtifactID
	memberships := []IndexMembership{
		{
			PathKey:     "checkout-a:docs/guide.md",
			DisplayPath: "docs/guide.md",
			Mode:        "100644",
			State:       IndexFilePresent,
			ArtifactID:  &firstArtifactID,
		},
		{
			PathKey:     "checkout-b:docs/guide.md",
			DisplayPath: "docs/guide.md",
			Mode:        "100644",
			State:       IndexFilePresent,
			ArtifactID:  &reusedArtifactID,
		},
	}
	if memberships[0].PathKey == memberships[1].PathKey {
		t.Fatal("distinct checkout memberships collapsed to one path key")
	}
	if *memberships[0].ArtifactID != *memberships[1].ArtifactID {
		t.Fatalf("identical source bytes/profile did not reuse immutable artifact facts: %q != %q", *memberships[0].ArtifactID, *memberships[1].ArtifactID)
	}
	if _, err := DigestIndexManifest(memberships); err != nil {
		t.Fatalf("distinct memberships for one immutable artifact are not publishable: %v", err)
	}

	for _, variant := range []struct {
		name    string
		profile MarkdownExtractionProfile
	}{
		{
			name:    "profile key",
			profile: MarkdownExtractionProfile{ProfileKey: "markdown-structure-v2", ParserKey: profile.ParserKey},
		},
		{
			name:    "parser key",
			profile: MarkdownExtractionProfile{ProfileKey: profile.ProfileKey, ParserKey: "markdown-parser-v2"},
		},
	} {
		t.Run(variant.name, func(t *testing.T) {
			changed := ExtractMarkdown(source, variant.profile)
			if changed.Proof.ArtifactID == first.Proof.ArtifactID {
				t.Fatalf("%s change reused artifact identity %q", variant.name, changed.Proof.ArtifactID)
			}
		})
	}

	changedBytes := append(append([]byte(nil), source...), []byte("<!-- byte change -->\r\n")...)
	changed := ExtractMarkdown(changedBytes, profile)
	if changed.Proof.ArtifactID == first.Proof.ArtifactID {
		t.Fatalf("changed source bytes reused artifact identity %q", changed.Proof.ArtifactID)
	}
}

func TestUCIMarkdownExtractionReportsMalformedLinkAsFreshPartialFacts(t *testing.T) {
	profile := uciMarkdownExtractionProfile()
	complete := ExtractMarkdown([]byte(uciMarkdownExtractionFixture), profile)
	malformedSource := []byte("# Partial\r\n\r\n[unfinished](docs/guide.md\r\nStill useful prose.\r\n")

	partial := ExtractMarkdown(malformedSource, profile)
	if partial.Coverage != IndexCoveragePartial {
		t.Fatalf("ExtractMarkdown(malformed) coverage = %q, want %q", partial.Coverage, IndexCoveragePartial)
	}
	if partial.Text != string(malformedSource) {
		t.Fatalf("ExtractMarkdown(malformed) text = %q, want exact malformed source", partial.Text)
	}
	if !strings.Contains(partial.Text, "Still useful prose.") {
		t.Fatalf("ExtractMarkdown(malformed) text lost usable source content: %q", partial.Text)
	}
	uciRequireMarkdownExtractionProof(t, partial)
	uciRequireMarkdownDiagnostic(t, partial.Diagnostics, "MARKDOWN_LINK_UNTERMINATED")
	if partial.Proof.ArtifactID == complete.Proof.ArtifactID {
		t.Fatalf("malformed source reused complete artifact identity %q", partial.Proof.ArtifactID)
	}
	if partial.Proof.ContentDigest == complete.Proof.ContentDigest || partial.Proof.FactsDigest == complete.Proof.FactsDigest {
		t.Fatalf("malformed source reused complete artifact digests: partial=%#v complete=%#v", partial.Proof, complete.Proof)
	}
}

func TestUCIMarkdownExtractionRecordsExternalURLsOnlyAsObservedReferences(t *testing.T) {
	source := []byte("# Sources\n\n[web](https://example.test/spec#section)\n[file](file:///tmp/source.md#section)\n")
	artifact := ExtractMarkdown(source, uciMarkdownExtractionProfile())

	if artifact.Coverage != IndexCoverageComplete {
		t.Fatalf("ExtractMarkdown(external URLs) coverage = %q, want %q", artifact.Coverage, IndexCoverageComplete)
	}
	uciRequireMarkdownExtractionProof(t, artifact)
	heading := uciRequireMarkdownHeading(t, artifact.Headings, "Sources", 1, 0, uciMarkdownExtractionSpan(t, source, "# Sources", 0))
	uciRequireMarkdownReference(t, artifact.References, "external_link", heading.LocalKey, "https://example.test/spec#section", "", "", uciMarkdownExtractionSpan(t, source, "[web](https://example.test/spec#section)", 0))
	uciRequireMarkdownReference(t, artifact.References, "external_link", heading.LocalKey, "file:///tmp/source.md#section", "", "", uciMarkdownExtractionSpan(t, source, "[file](file:///tmp/source.md#section)", 0))
}

func TestUCIMarkdownExtractionDoesNotInferProseSimilarity(t *testing.T) {
	source := []byte("# Claim\n\nThis document implements the platform's exact guarantee.\n")
	artifact := ExtractMarkdown(source, uciMarkdownExtractionProfile())

	if artifact.Coverage != IndexCoverageComplete {
		t.Fatalf("ExtractMarkdown(prose) coverage = %q, want %q", artifact.Coverage, IndexCoverageComplete)
	}
	uciRequireMarkdownExtractionProof(t, artifact)
	if len(artifact.References) != 0 {
		t.Fatalf("plain prose manufactured reference facts: %#v", artifact.References)
	}
	uciRequireMarkdownChunkContaining(t, artifact.Chunks, "This document implements the platform's exact guarantee.")
}

func uciMarkdownExtractionProfile() MarkdownExtractionProfile {
	return MarkdownExtractionProfile{
		ProfileKey: "markdown-structure-v1",
		ParserKey:  "markdown-parser-v1",
	}
}

func uciRequireMarkdownExtractionProof(t *testing.T, artifact MarkdownArtifact) {
	t.Helper()
	if !canonicalContextUUID(artifact.Proof.ArtifactID) {
		t.Fatalf("artifact ID %q is not a canonical UUID", artifact.Proof.ArtifactID)
	}
	if !isIndexDigest(artifact.Proof.ContentDigest) {
		t.Fatalf("content digest %q is not a stable index digest", artifact.Proof.ContentDigest)
	}
	if !isIndexDigest(artifact.Proof.FactsDigest) {
		t.Fatalf("facts digest %q is not a stable index digest", artifact.Proof.FactsDigest)
	}
	if artifact.Proof.DefinitionCount != uint64(len(artifact.Headings)) {
		t.Fatalf("heading count = %d, want %d", artifact.Proof.DefinitionCount, len(artifact.Headings))
	}
	if artifact.Proof.ReferenceSiteCount != uint64(len(artifact.References)) {
		t.Fatalf("reference count = %d, want %d", artifact.Proof.ReferenceSiteCount, len(artifact.References))
	}
	if artifact.Proof.ChunkCount != uint64(len(artifact.Chunks)) {
		t.Fatalf("chunk count = %d, want %d", artifact.Proof.ChunkCount, len(artifact.Chunks))
	}
	if len(artifact.Chunks) == 0 {
		t.Fatal("extraction returned no prose chunks")
	}
	for index, chunk := range artifact.Chunks {
		if !isIndexDigest(chunk.ContentDigest) {
			t.Fatalf("chunk %d content digest %q is not a stable index digest", index, chunk.ContentDigest)
		}
		if chunk.Span.ByteStart < 0 || chunk.Span.ByteEnd <= chunk.Span.ByteStart || chunk.Span.ByteEnd > int64(len(artifact.Text)) {
			t.Fatalf("chunk %d has invalid byte span %#v for %d-byte text", index, chunk.Span, len(artifact.Text))
		}
		if chunk.Span.LineStart < 1 || chunk.Span.LineEnd < chunk.Span.LineStart {
			t.Fatalf("chunk %d has invalid inclusive line span %#v", index, chunk.Span)
		}
		if want := artifact.Text[chunk.Span.ByteStart:chunk.Span.ByteEnd]; chunk.Text != want {
			t.Fatalf("chunk %d text = %q, want text at span %q", index, chunk.Text, want)
		}
	}
}

func uciRequireMarkdownHeading(t *testing.T, headings []MarkdownHeading, title string, level, occurrence int, wantSpan IndexSpan) MarkdownHeading {
	t.Helper()
	seen := 0
	for _, heading := range headings {
		if heading.Title != title || heading.Level != level {
			continue
		}
		if seen != occurrence {
			seen++
			continue
		}
		if heading.SymbolKey == "" || heading.LocalKey == "" {
			t.Fatalf("heading %q has empty key: %#v", title, heading)
		}
		uciRequireMarkdownSpan(t, heading.Span, wantSpan, "heading "+title)
		return heading
	}
	t.Fatalf("missing level-%d heading %q occurrence %d; got %#v", level, title, occurrence, headings)
	return MarkdownHeading{}
}

func uciRequireMarkdownReference(t *testing.T, references []MarkdownReferenceSite, kind, ownerLocalKey, rawTarget, targetPath, fragment string, wantSpan IndexSpan) MarkdownReferenceSite {
	t.Helper()
	for _, reference := range references {
		if reference.Kind != kind || reference.RawTarget != rawTarget || reference.OwnerLocalKey != ownerLocalKey || reference.Span != wantSpan {
			continue
		}
		if reference.SymbolKey == "" || reference.LocalKey == "" {
			t.Fatalf("reference %q has empty key: %#v", rawTarget, reference)
		}
		if reference.TargetPath != targetPath {
			t.Fatalf("reference %q target path = %q, want %q", rawTarget, reference.TargetPath, targetPath)
		}
		if reference.Fragment != fragment {
			t.Fatalf("reference %q fragment = %q, want %q", rawTarget, reference.Fragment, fragment)
		}
		if reference.ResolutionState != IndexResolutionState("unresolved") {
			t.Fatalf("reference %q resolution = %q, want unresolved source fact", rawTarget, reference.ResolutionState)
		}
		uciRequireMarkdownSpan(t, reference.Span, wantSpan, "reference "+rawTarget)
		return reference
	}
	t.Fatalf("missing %s reference %q owned by %q; got %#v", kind, rawTarget, ownerLocalKey, references)
	return MarkdownReferenceSite{}
}

func uciRequireMarkdownDiagnostic(t *testing.T, diagnostics []MarkdownDiagnostic, code string) {
	t.Helper()
	for _, diagnostic := range diagnostics {
		if diagnostic.Code == code {
			return
		}
	}
	t.Fatalf("missing diagnostic %q; got %#v", code, diagnostics)
}

func uciRequireMarkdownChunkContaining(t *testing.T, chunks []MarkdownChunk, text string) {
	t.Helper()
	for _, chunk := range chunks {
		if strings.Contains(chunk.Text, text) {
			return
		}
	}
	t.Fatalf("no chunk contains prose %q; got %#v", text, chunks)
}

func uciRequireMarkdownChunkCovering(t *testing.T, chunks []MarkdownChunk, span IndexSpan, subject string) {
	t.Helper()
	for _, chunk := range chunks {
		if chunk.Span.ByteStart <= span.ByteStart && chunk.Span.ByteEnd >= span.ByteEnd {
			return
		}
	}
	t.Fatalf("no chunk covers %s span %#v; got %#v", subject, span, chunks)
}

func uciRequireMarkdownSpan(t *testing.T, got, want IndexSpan, subject string) {
	t.Helper()
	if got != want {
		t.Fatalf("%s span = %#v, want %#v", subject, got, want)
	}
}

func uciMarkdownExtractionSpan(t *testing.T, source []byte, fragment string, occurrence int) IndexSpan {
	t.Helper()
	searchFrom := 0
	for found := 0; ; found++ {
		offset := bytes.Index(source[searchFrom:], []byte(fragment))
		if offset < 0 {
			t.Fatalf("fragment %q occurrence %d not found in fixture", fragment, occurrence)
		}
		offset += searchFrom
		if found == occurrence {
			end := offset + len(fragment)
			return IndexSpan{
				ByteStart: int64(offset),
				ByteEnd:   int64(end),
				LineStart: bytes.Count(source[:offset], []byte("\n")) + 1,
				LineEnd:   bytes.Count(source[:end], []byte("\n")) + 1,
			}
		}
		searchFrom = offset + len(fragment)
	}
}
