package uci

import (
	"bytes"
	"reflect"
	"strings"
	"testing"
	"unicode/utf8"
)

const uciJSONYAMLJSONFixture = "{\r\n" +
	"\t\"title\": \"Café\",\r\n" +
	"\t\"items\": [{\"name\":\"α\"}, 2],\r\n" +
	"\t\"local\": {\"kind\": \"target\"},\r\n" +
	"\t\"$ref\": \"#/local\"\r\n" +
	"}\r\n"

const uciJSONYAMLYAMLFixture = "---\r\n" +
	"defaults: &defaults\r\n" +
	"  owner: Café\r\n" +
	"service:\r\n" +
	"  <<: *defaults\r\n" +
	"  tags:\r\n" +
	"    - core\r\n" +
	"    - β\r\n" +
	"---\r\n" +
	"other: true\r\n"

func TestUCIJSONYAMLExtractionJSONProducesCompleteStableFacts(t *testing.T) {
	source := []byte(uciJSONYAMLJSONFixture)
	original := append([]byte(nil), source...)
	profile := uciJSONYAMLProfile(JSONYAMLFormatJSON)

	first := ExtractJSONYAML(source, profile)
	source[0] = '['
	second := ExtractJSONYAML(append([]byte(nil), original...), profile)

	if first.Format != JSONYAMLFormatJSON {
		t.Fatalf("ExtractJSONYAML(JSON) format = %q, want %q", first.Format, JSONYAMLFormatJSON)
	}
	if first.Coverage != IndexCoverageComplete {
		t.Fatalf("ExtractJSONYAML(JSON) coverage = %q, want %q", first.Coverage, IndexCoverageComplete)
	}
	if first.Text != string(original) {
		t.Fatalf("ExtractJSONYAML(JSON) text = %q, want exact caller-owned bytes", first.Text)
	}
	if !reflect.DeepEqual(first, second) {
		t.Fatalf("ExtractJSONYAML(JSON) facts are not stable across identical byte/profile input:\nfirst: %#v\nsecond: %#v", first, second)
	}
	uciRequireJSONYAMLExtractionProof(t, first)

	uciRequireJSONYAMLDefinition(t, first.Definitions, "document", "json:document:0", "document:0", 0, uciJSONYAMLSpan(t, original, uciJSONYAMLJSONFixture, 0))
	uciRequireJSONYAMLDefinition(t, first.Definitions, "key", "json:document:0#/title", "#/title", 0, uciJSONYAMLSpan(t, original, "\"title\"", 0))
	uciRequireJSONYAMLDefinition(t, first.Definitions, "key", "json:document:0#/items", "#/items", 0, uciJSONYAMLSpan(t, original, "\"items\"", 0))
	uciRequireJSONYAMLDefinition(t, first.Definitions, "index", "json:document:0#/items/0", "#/items/0", 0, uciJSONYAMLSpan(t, original, "{\"name\":\"α\"}", 0))
	uciRequireJSONYAMLDefinition(t, first.Definitions, "key", "json:document:0#/items/0/name", "#/items/0/name", 0, uciJSONYAMLSpan(t, original, "\"name\"", 0))
	uciRequireJSONYAMLDefinition(t, first.Definitions, "index", "json:document:0#/items/1", "#/items/1", 0, uciJSONYAMLSpan(t, original, "2", 0))
	uciRequireJSONYAMLDefinition(t, first.Definitions, "key", "json:document:0#/local", "#/local", 0, uciJSONYAMLSpan(t, original, "\"local\"", 0))
	uciRequireJSONYAMLDefinition(t, first.Definitions, "key", "json:document:0#/$ref", "#/$ref", 0, uciJSONYAMLSpan(t, original, "\"$ref\"", 0))
	uciRequireJSONYAMLReference(t, first.References, "pointer", "#/$ref", "json:document:0#/local", 0, uciJSONYAMLSpan(t, original, "\"#/local\"", 0))
}

func TestUCIJSONYAMLExtractionYAMLRetainsDocumentProvenanceAndAliases(t *testing.T) {
	source := []byte(uciJSONYAMLYAMLFixture)
	profile := uciJSONYAMLProfile(JSONYAMLFormatYAML)

	first := ExtractJSONYAML(source, profile)
	second := ExtractJSONYAML(append([]byte(nil), source...), profile)

	if first.Format != JSONYAMLFormatYAML {
		t.Fatalf("ExtractJSONYAML(YAML) format = %q, want %q", first.Format, JSONYAMLFormatYAML)
	}
	if first.Coverage != IndexCoverageComplete {
		t.Fatalf("ExtractJSONYAML(YAML) coverage = %q, want %q", first.Coverage, IndexCoverageComplete)
	}
	if first.Text != string(source) {
		t.Fatalf("ExtractJSONYAML(YAML) text = %q, want exact CRLF/Unicode source", first.Text)
	}
	if !reflect.DeepEqual(first, second) {
		t.Fatalf("ExtractJSONYAML(YAML) facts are not stable across identical byte/profile input:\nfirst: %#v\nsecond: %#v", first, second)
	}
	uciRequireJSONYAMLExtractionProof(t, first)

	documentZero := uciRequireJSONYAMLDefinitionIdentity(t, first.Definitions, "document", "yaml:document:0", "document:0", 0)
	documentOne := uciRequireJSONYAMLDefinitionIdentity(t, first.Definitions, "document", "yaml:document:1", "document:1", 1)
	if documentZero.Span.ByteEnd <= documentZero.Span.ByteStart || documentOne.Span.ByteEnd <= documentOne.Span.ByteStart {
		t.Fatalf("YAML document definitions lost source spans: document 0=%#v document 1=%#v", documentZero.Span, documentOne.Span)
	}
	uciRequireJSONYAMLDefinition(t, first.Definitions, "key", "yaml:document:0#/defaults", "#/defaults", 0, uciJSONYAMLSpan(t, source, "defaults", 0))
	anchor := uciRequireJSONYAMLDefinition(t, first.Definitions, "anchor", "yaml:document:0#anchor:defaults", "anchor:defaults", 0, uciJSONYAMLSpan(t, source, "&defaults", 0))
	uciRequireJSONYAMLAlias(t, first.References, anchor.SymbolKey, 0, uciJSONYAMLSpan(t, source, "*defaults", 0))
	uciRequireJSONYAMLDefinition(t, first.Definitions, "key", "yaml:document:0#/service/tags", "#/service/tags", 0, uciJSONYAMLSpan(t, source, "tags", 0))
	uciRequireJSONYAMLDefinition(t, first.Definitions, "index", "yaml:document:0#/service/tags/0", "#/service/tags/0", 0, uciJSONYAMLSpan(t, source, "core", 0))
	uciRequireJSONYAMLDefinition(t, first.Definitions, "index", "yaml:document:0#/service/tags/1", "#/service/tags/1", 0, uciJSONYAMLSpan(t, source, "β", 0))
	uciRequireJSONYAMLDefinition(t, first.Definitions, "key", "yaml:document:1#/other", "#/other", 1, uciJSONYAMLSpan(t, source, "other", 0))
}

func TestUCIJSONYAMLExtractionBoundsUnicodeChunks(t *testing.T) {
	source := []byte("{\"payload\":\"" + strings.Repeat("α", 40<<10) + "\"}")
	artifact := ExtractJSONYAML(source, uciJSONYAMLProfile(JSONYAMLFormatJSON))

	if artifact.Coverage != IndexCoverageComplete {
		t.Fatalf("ExtractJSONYAML(large JSON) coverage = %q, want %q", artifact.Coverage, IndexCoverageComplete)
	}
	if artifact.Text != string(source) {
		t.Fatalf("ExtractJSONYAML(large JSON) text changed caller-owned Unicode bytes")
	}
	if len(artifact.Chunks) < 2 {
		t.Fatalf("ExtractJSONYAML(large JSON) chunks = %d, want bounded multi-chunk output", len(artifact.Chunks))
	}
	uciRequireJSONYAMLExtractionProof(t, artifact)

	var rebuilt strings.Builder
	var nextStart int64
	for index, chunk := range artifact.Chunks {
		if len(chunk.Text) > 64<<10 {
			t.Fatalf("chunk %d is %d bytes, exceeds the 64 KiB bound", index, len(chunk.Text))
		}
		if !utf8.ValidString(chunk.Text) {
			t.Fatalf("chunk %d splits a UTF-8 sequence: %q", index, chunk.Text)
		}
		if chunk.Span.ByteStart != nextStart {
			t.Fatalf("chunk %d starts at %d, want contiguous offset %d", index, chunk.Span.ByteStart, nextStart)
		}
		nextStart = chunk.Span.ByteEnd
		rebuilt.WriteString(chunk.Text)
	}
	if nextStart != int64(len(artifact.Text)) {
		t.Fatalf("chunk coverage ends at %d, want retained text length %d", nextStart, len(artifact.Text))
	}
	if rebuilt.String() != artifact.Text {
		t.Fatal("bounded chunks do not losslessly reconstruct retained source text")
	}
}

func TestUCIJSONYAMLExtractionRetainsPartialFactsForMalformedSource(t *testing.T) {
	for _, test := range []struct {
		name      string
		format    JSONYAMLFormat
		source    string
		symbolKey string
		fragment  string
	}{
		{
			name:      "json",
			format:    JSONYAMLFormatJSON,
			source:    "{\"kept\":{\"name\":\"value\"},\"broken\":[1,}",
			symbolKey: "json:document:0#/kept",
			fragment:  "\"kept\"",
		},
		{
			name:      "yaml",
			format:    JSONYAMLFormatYAML,
			source:    "kept:\n  name: value\nbroken: [\n",
			symbolKey: "yaml:document:0#/kept",
			fragment:  "kept",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			source := []byte(test.source)
			artifact := ExtractJSONYAML(source, uciJSONYAMLProfile(test.format))

			if artifact.Format != test.format {
				t.Fatalf("malformed %s format = %q, want %q", test.name, artifact.Format, test.format)
			}
			if artifact.Coverage != IndexCoveragePartial {
				t.Fatalf("malformed %s coverage = %q, want %q", test.name, artifact.Coverage, IndexCoveragePartial)
			}
			if artifact.Text != test.source {
				t.Fatalf("malformed %s text = %q, want exact usable source", test.name, artifact.Text)
			}
			uciRequireJSONYAMLExtractionProof(t, artifact)
			uciRequireJSONYAMLDiagnostic(t, artifact.Diagnostics, "PARSE_ERROR")
			uciRequireJSONYAMLDefinition(t, artifact.Definitions, "key", test.symbolKey, "#/kept", 0, uciJSONYAMLSpan(t, source, test.fragment, 0))
		})
	}
}

func TestUCIJSONYAMLExtractionRejectsAmbiguousDuplicateKeys(t *testing.T) {
	for _, test := range []struct {
		name      string
		format    JSONYAMLFormat
		source    string
		symbolKey string
	}{
		{
			name:      "json",
			format:    JSONYAMLFormatJSON,
			source:    "{\"same\":1,\"same\":2}",
			symbolKey: "json:document:0#/same",
		},
		{
			name:      "yaml",
			format:    JSONYAMLFormatYAML,
			source:    "same: one\nsame: two\n",
			symbolKey: "yaml:document:0#/same",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			artifact := ExtractJSONYAML([]byte(test.source), uciJSONYAMLProfile(test.format))

			if artifact.Coverage != IndexCoveragePartial {
				t.Fatalf("duplicate %s coverage = %q, want %q", test.name, artifact.Coverage, IndexCoveragePartial)
			}
			uciRequireJSONYAMLExtractionProof(t, artifact)
			uciRequireJSONYAMLDiagnostic(t, artifact.Diagnostics, "DUPLICATE_KEY")
			if uciHasJSONYAMLDefinition(artifact.Definitions, "key", test.symbolKey) {
				t.Fatalf("duplicate %s key %q was silently assigned a fact", test.name, test.symbolKey)
			}
		})
	}
}

func TestUCIJSONYAMLExtractionDoesNotResolveExternalReferences(t *testing.T) {
	for _, test := range []struct {
		name   string
		format JSONYAMLFormat
		source string
	}{
		{
			name:   "json",
			format: JSONYAMLFormatJSON,
			source: "{\"remote\":{\"$ref\":\"https://example.invalid/schema.json#/Thing\"},\"file\":{\"$ref\":\"file:///not-allowed\"}}",
		},
		{
			name:   "yaml",
			format: JSONYAMLFormatYAML,
			source: "remote:\n  $ref: https://example.invalid/schema.yaml#/Thing\nfile:\n  $ref: file:///not-allowed\n",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			artifact := ExtractJSONYAML([]byte(test.source), uciJSONYAMLProfile(test.format))

			if artifact.Coverage != IndexCoveragePartial {
				t.Fatalf("external %s coverage = %q, want %q", test.name, artifact.Coverage, IndexCoveragePartial)
			}
			uciRequireJSONYAMLExtractionProof(t, artifact)
			uciRequireJSONYAMLDiagnostic(t, artifact.Diagnostics, "EXTERNAL_REFERENCE")
			if len(artifact.References) != 0 {
				t.Fatalf("external %s references were resolved or retained as local facts: %#v", test.name, artifact.References)
			}
		})
	}
}

func uciJSONYAMLProfile(format JSONYAMLFormat) JSONYAMLExtractionProfile {
	return JSONYAMLExtractionProfile{
		ProfileKey: "jsonyaml-structure-v1",
		ParserKey:  "jsonyaml-parser-v1",
		Format:     format,
	}
}

func uciRequireJSONYAMLExtractionProof(t *testing.T, artifact JSONYAMLArtifact) {
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
	if artifact.Proof.DefinitionCount != uint64(len(artifact.Definitions)) {
		t.Fatalf("definition count = %d, want %d", artifact.Proof.DefinitionCount, len(artifact.Definitions))
	}
	if artifact.Proof.ReferenceSiteCount != uint64(len(artifact.References)) {
		t.Fatalf("reference count = %d, want %d", artifact.Proof.ReferenceSiteCount, len(artifact.References))
	}
	if artifact.Proof.ChunkCount != uint64(len(artifact.Chunks)) {
		t.Fatalf("chunk count = %d, want %d", artifact.Proof.ChunkCount, len(artifact.Chunks))
	}
	if len(artifact.Chunks) == 0 {
		t.Fatal("extraction returned no text chunks")
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

func uciRequireJSONYAMLDefinition(t *testing.T, definitions []JSONYAMLDefinition, kind, symbolKey, localKey string, document int, wantSpan IndexSpan) JSONYAMLDefinition {
	t.Helper()
	definition := uciRequireJSONYAMLDefinitionIdentity(t, definitions, kind, symbolKey, localKey, document)
	uciRequireJSONYAMLSpan(t, definition.Span, wantSpan, "definition "+symbolKey)
	return definition
}

func uciRequireJSONYAMLDefinitionIdentity(t *testing.T, definitions []JSONYAMLDefinition, kind, symbolKey, localKey string, document int) JSONYAMLDefinition {
	t.Helper()
	for _, definition := range definitions {
		if definition.Kind != kind || definition.SymbolKey != symbolKey {
			continue
		}
		if definition.LocalKey != localKey {
			t.Fatalf("definition %q local key = %q, want %q", symbolKey, definition.LocalKey, localKey)
		}
		if definition.Document != document {
			t.Fatalf("definition %q document = %d, want %d", symbolKey, definition.Document, document)
		}
		return definition
	}
	t.Fatalf("missing %s definition %q; got %#v", kind, symbolKey, definitions)
	return JSONYAMLDefinition{}
}

func uciRequireJSONYAMLReference(t *testing.T, references []JSONYAMLReferenceSite, kind, localKey, targetKey string, document int, wantSpan IndexSpan) {
	t.Helper()
	for _, reference := range references {
		if reference.Kind != kind || reference.LocalKey != localKey || reference.TargetKey != targetKey || reference.Document != document {
			continue
		}
		if reference.SymbolKey == "" {
			t.Fatalf("%s reference has no stable source identity: %#v", kind, reference)
		}
		uciRequireJSONYAMLSpan(t, reference.Span, wantSpan, "reference "+reference.SymbolKey)
		return
	}
	t.Fatalf("missing %s reference at %q targeting %q; got %#v", kind, localKey, targetKey, references)
}

func uciRequireJSONYAMLAlias(t *testing.T, references []JSONYAMLReferenceSite, targetKey string, document int, wantSpan IndexSpan) {
	t.Helper()
	for _, reference := range references {
		if reference.Kind != "alias" || reference.TargetKey != targetKey || reference.Document != document {
			continue
		}
		if reference.SymbolKey == "" {
			t.Fatalf("alias reference has no stable source identity: %#v", reference)
		}
		uciRequireJSONYAMLSpan(t, reference.Span, wantSpan, "alias reference "+reference.SymbolKey)
		return
	}
	t.Fatalf("missing YAML alias in document %d targeting %q; got %#v", document, targetKey, references)
}

func uciRequireJSONYAMLDiagnostic(t *testing.T, diagnostics []JSONYAMLDiagnostic, code string) {
	t.Helper()
	for _, diagnostic := range diagnostics {
		if diagnostic.Code == code {
			return
		}
	}
	t.Fatalf("missing diagnostic %q; got %#v", code, diagnostics)
}

func uciHasJSONYAMLDefinition(definitions []JSONYAMLDefinition, kind, symbolKey string) bool {
	for _, definition := range definitions {
		if definition.Kind == kind && definition.SymbolKey == symbolKey {
			return true
		}
	}
	return false
}

func uciRequireJSONYAMLSpan(t *testing.T, got, want IndexSpan, subject string) {
	t.Helper()
	if got != want {
		t.Fatalf("%s span = %#v, want %#v", subject, got, want)
	}
}

func uciJSONYAMLSpan(t *testing.T, source []byte, fragment string, occurrence int) IndexSpan {
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
			lineEndOffset := offset
			if end > offset {
				lineEndOffset = end - 1
			}
			return IndexSpan{
				ByteStart: int64(offset),
				ByteEnd:   int64(end),
				LineStart: bytes.Count(source[:offset], []byte("\n")) + 1,
				LineEnd:   bytes.Count(source[:lineEndOffset], []byte("\n")) + 1,
			}
		}
		searchFrom = offset + len(fragment)
	}
}
