package uci

import (
	"bytes"
	"reflect"
	"strings"
	"testing"
	"unicode/utf8"
)

const uciOpenAPIJSONFixture = "{\r\n" +
	"\t\"openapi\": \"3.0.3\",\r\n" +
	"\t\"info\": {\"title\": \"Café Pets\", \"version\": \"1.0.0\"},\r\n" +
	"\t\"paths\": {\r\n" +
	"\t\t\"/pets\": {\r\n" +
	"\t\t\t\"parameters\": [{\"name\": \"trace-id\", \"in\": \"header\", \"schema\": {\"type\": \"string\"}}],\r\n" +
	"\t\t\t\"get\": {\r\n" +
	"\t\t\t\t\"operationId\": \"listPets\",\r\n" +
	"\t\t\t\t\"parameters\": [{\"name\": \"limit\", \"in\": \"query\", \"schema\": {\"$ref\": \"#/components/schemas/Limit\"}}],\r\n" +
	"\t\t\t\t\"responses\": {\"200\": {\"description\": \"β success\", \"content\": {\"application/json\": {\"schema\": {\"$ref\": \"#/components/schemas/Pet\"}}}}}\r\n" +
	"\t\t\t}\r\n" +
	"\t\t}\r\n" +
	"\t},\r\n" +
	"\t\"components\": {\r\n" +
	"\t\t\"parameters\": {\"PetID\": {\"name\": \"petId\", \"in\": \"path\", \"required\": true, \"schema\": {\"type\": \"string\"}}},\r\n" +
	"\t\t\"schemas\": {\r\n" +
	"\t\t\t\"Pet\": {\"type\": \"object\", \"properties\": {\"name\": {\"type\": \"string\"}}},\r\n" +
	"\t\t\t\"Limit\": {\"type\": \"integer\", \"minimum\": 0}\r\n" +
	"\t\t}\r\n" +
	"\t}\r\n" +
	"}\r\n"

const uciOpenAPIYAMLFixture = "openapi: 3.0.3\r\n" +
	"info:\r\n" +
	"  title: Café Pets\r\n" +
	"  version: 1.0.0\r\n" +
	"paths:\r\n" +
	"  /pets:\r\n" +
	"    parameters:\r\n" +
	"      - name: trace-id\r\n" +
	"        in: header\r\n" +
	"        schema:\r\n" +
	"          type: string\r\n" +
	"    get:\r\n" +
	"      operationId: listPets\r\n" +
	"      parameters:\r\n" +
	"        - name: limit\r\n" +
	"          in: query\r\n" +
	"          schema:\r\n" +
	"            $ref: \"#/components/schemas/Limit\"\r\n" +
	"      responses:\r\n" +
	"        \"200\":\r\n" +
	"          description: β success\r\n" +
	"          content:\r\n" +
	"            application/json:\r\n" +
	"              schema:\r\n" +
	"                $ref: \"#/components/schemas/Pet\"\r\n" +
	"components:\r\n" +
	"  parameters:\r\n" +
	"    PetID:\r\n" +
	"      name: petId\r\n" +
	"      in: path\r\n" +
	"      required: true\r\n" +
	"      schema:\r\n" +
	"        type: string\r\n" +
	"  schemas:\r\n" +
	"    Pet:\r\n" +
	"      type: object\r\n" +
	"      properties:\r\n" +
	"        name:\r\n" +
	"          type: string\r\n" +
	"    Limit:\r\n" +
	"      type: integer\r\n" +
	"      minimum: 0\r\n"

func TestUCIOpenAPIExtractionJSONProducesCompleteStableFacts(t *testing.T) {
	source := []byte(uciOpenAPIJSONFixture)
	original := append([]byte(nil), source...)
	profile := uciOpenAPIProfile(OpenAPIFormatJSON)

	first := ExtractOpenAPI(source, profile)
	source[0] = '['
	second := ExtractOpenAPI(append([]byte(nil), original...), profile)

	if first.Format != OpenAPIFormatJSON {
		t.Fatalf("ExtractOpenAPI(JSON) format = %q, want %q", first.Format, OpenAPIFormatJSON)
	}
	if first.Coverage != IndexCoverageComplete {
		t.Fatalf("ExtractOpenAPI(JSON) coverage = %q, want %q", first.Coverage, IndexCoverageComplete)
	}
	if first.Text != string(original) {
		t.Fatalf("ExtractOpenAPI(JSON) text = %q, want exact caller-owned bytes", first.Text)
	}
	if !reflect.DeepEqual(first, second) {
		t.Fatalf("ExtractOpenAPI(JSON) facts are not stable across identical byte/profile input:\nfirst: %#v\nsecond: %#v", first, second)
	}
	uciRequireOpenAPIExtractionProof(t, first)
	uciRequireOpenAPICoreFacts(t, original, first, uciOpenAPISemanticFragments{
		version:       "\"openapi\"",
		info:          "\"info\"",
		path:          "\"/pets\"",
		pathParameter: "\"trace-id\"",
		operation:     "\"get\"",
		parameter:     "\"limit\"",
		component:     "\"PetID\"",
		pet:           "\"Pet\"",
		limit:         "\"Limit\"",
		limitRef:      "\"#/components/schemas/Limit\"",
		petRef:        "\"#/components/schemas/Pet\"",
	})
	uciRequireOpenAPIChunkContaining(t, first.Chunks, "Café Pets")
	uciRequireOpenAPIChunkContaining(t, first.Chunks, "β success")

	changedProfile := profile
	changedProfile.ParserKey = "openapi-parser-v2"
	if changed := ExtractOpenAPI(original, changedProfile); changed.Proof.ArtifactID == first.Proof.ArtifactID {
		t.Fatalf("parser profile change reused artifact identity %q", changed.Proof.ArtifactID)
	}
	changedBytes := append(append([]byte(nil), original...), []byte("\r\n")...)
	if changed := ExtractOpenAPI(changedBytes, profile); changed.Proof.ArtifactID == first.Proof.ArtifactID {
		t.Fatalf("changed source bytes reused artifact identity %q", changed.Proof.ArtifactID)
	}
}

func TestUCIOpenAPIExtractionYAMLProducesCompleteStableFacts(t *testing.T) {
	source := []byte(uciOpenAPIYAMLFixture)
	profile := uciOpenAPIProfile(OpenAPIFormatYAML)

	first := ExtractOpenAPI(source, profile)
	second := ExtractOpenAPI(append([]byte(nil), source...), profile)

	if first.Format != OpenAPIFormatYAML {
		t.Fatalf("ExtractOpenAPI(YAML) format = %q, want %q", first.Format, OpenAPIFormatYAML)
	}
	if first.Coverage != IndexCoverageComplete {
		t.Fatalf("ExtractOpenAPI(YAML) coverage = %q, want %q", first.Coverage, IndexCoverageComplete)
	}
	if first.Text != string(source) {
		t.Fatalf("ExtractOpenAPI(YAML) text = %q, want exact CRLF/Unicode source", first.Text)
	}
	if !reflect.DeepEqual(first, second) {
		t.Fatalf("ExtractOpenAPI(YAML) facts are not stable across identical byte/profile input:\nfirst: %#v\nsecond: %#v", first, second)
	}
	uciRequireOpenAPIExtractionProof(t, first)
	uciRequireOpenAPICoreFacts(t, source, first, uciOpenAPISemanticFragments{
		version:         "openapi",
		info:            "info",
		path:            "/pets",
		pathParameter:   "trace-id",
		operation:       "get",
		parameter:       "limit",
		component:       "PetID",
		pet:             "Pet",
		limit:           "Limit",
		petOccurrence:   4,
		limitOccurrence: 1,
		limitRef:        "\"#/components/schemas/Limit\"",
		petRef:          "\"#/components/schemas/Pet\"",
	})
	uciRequireOpenAPIChunkContaining(t, first.Chunks, "Café Pets")
	uciRequireOpenAPIChunkContaining(t, first.Chunks, "β success")
}

func TestUCIOpenAPIExtractionRetainsMalformedPartialFacts(t *testing.T) {
	for _, test := range []struct {
		name      string
		format    OpenAPIFormat
		source    string
		version   string
		path      string
		operation string
	}{
		{
			name:      "json",
			format:    OpenAPIFormatJSON,
			source:    "{\"openapi\":\"3.0.3\",\"info\":{\"title\":\"Café\",\"version\":\"1\"},\"paths\":{\"/pets\":{\"get\":{\"responses\":{\"200\":{\"description\":\"OK\"}}}}},\"components\":",
			version:   "\"openapi\"",
			path:      "\"/pets\"",
			operation: "\"get\"",
		},
		{
			name:   "yaml",
			format: OpenAPIFormatYAML,
			source: "openapi: 3.0.3\n" +
				"info:\n" +
				"  title: Café\n" +
				"  version: 1\n" +
				"paths:\n" +
				"  /pets:\n" +
				"    get:\n" +
				"      responses:\n" +
				"        \"200\":\n" +
				"          description: OK\n" +
				"components:\n" +
				"  schemas: [\n",
			version:   "openapi",
			path:      "/pets",
			operation: "get",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			source := []byte(test.source)
			artifact := ExtractOpenAPI(source, uciOpenAPIProfile(test.format))

			if artifact.Format != test.format {
				t.Fatalf("malformed %s format = %q, want %q", test.name, artifact.Format, test.format)
			}
			if artifact.Coverage != IndexCoveragePartial {
				t.Fatalf("malformed %s coverage = %q, want %q", test.name, artifact.Coverage, IndexCoveragePartial)
			}
			if artifact.Text != test.source {
				t.Fatalf("malformed %s text = %q, want exact usable source", test.name, artifact.Text)
			}
			uciRequireOpenAPIExtractionProof(t, artifact)
			uciRequireOpenAPIDiagnostic(t, artifact.Diagnostics, "PARSE_ERROR")
			uciRequireOpenAPIDefinition(t, artifact.Definitions, "version", "version:3.0.3", uciOpenAPISpan(t, source, test.version, 0))
			uciRequireOpenAPIDefinition(t, artifact.Definitions, "path", "path:/pets", uciOpenAPISpan(t, source, test.path, 0))
			uciRequireOpenAPIDefinition(t, artifact.Definitions, "operation", "operation:get:/pets", uciOpenAPISpan(t, source, test.operation, 0))
		})
	}
}

func TestUCIOpenAPIExtractionClassifiesLocalReferenceLimitations(t *testing.T) {
	for _, test := range []struct {
		name       string
		format     OpenAPIFormat
		source     string
		state      IndexResolutionState
		diagnostic string
		fragment   string
	}{
		{
			name:       "json missing",
			format:     OpenAPIFormatJSON,
			source:     uciOpenAPILocalReferenceJSONFixture("#/components/schemas/Missing", false),
			state:      IndexResolutionState("unresolved"),
			diagnostic: "MISSING_LOCAL_REFERENCE",
			fragment:   "\"#/components/schemas/Missing\"",
		},
		{
			name:       "yaml missing",
			format:     OpenAPIFormatYAML,
			source:     uciOpenAPILocalReferenceYAMLFixture("#/components/schemas/Missing", false),
			state:      IndexResolutionState("unresolved"),
			diagnostic: "MISSING_LOCAL_REFERENCE",
			fragment:   "\"#/components/schemas/Missing\"",
		},
		{
			name:       "json broken pointer",
			format:     OpenAPIFormatJSON,
			source:     uciOpenAPILocalReferenceJSONFixture("#/components/schemas/~2broken", false),
			state:      IndexResolutionState("partial"),
			diagnostic: "BROKEN_LOCAL_REFERENCE",
			fragment:   "\"#/components/schemas/~2broken\"",
		},
		{
			name:       "yaml broken pointer",
			format:     OpenAPIFormatYAML,
			source:     uciOpenAPILocalReferenceYAMLFixture("#/components/schemas/~2broken", false),
			state:      IndexResolutionState("partial"),
			diagnostic: "BROKEN_LOCAL_REFERENCE",
			fragment:   "\"#/components/schemas/~2broken\"",
		},
		{
			name:       "json ambiguous target",
			format:     OpenAPIFormatJSON,
			source:     uciOpenAPILocalReferenceJSONFixture("#/components/schemas/Pet", true),
			state:      IndexResolutionState("ambiguous"),
			diagnostic: "AMBIGUOUS_LOCAL_REFERENCE",
			fragment:   "\"#/components/schemas/Pet\"",
		},
		{
			name:       "yaml ambiguous target",
			format:     OpenAPIFormatYAML,
			source:     uciOpenAPILocalReferenceYAMLFixture("#/components/schemas/Pet", true),
			state:      IndexResolutionState("ambiguous"),
			diagnostic: "AMBIGUOUS_LOCAL_REFERENCE",
			fragment:   "\"#/components/schemas/Pet\"",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			source := []byte(test.source)
			artifact := ExtractOpenAPI(source, uciOpenAPIProfile(test.format))

			if artifact.Coverage != IndexCoveragePartial {
				t.Fatalf("%s coverage = %q, want %q", test.name, artifact.Coverage, IndexCoveragePartial)
			}
			uciRequireOpenAPIExtractionProof(t, artifact)
			uciRequireOpenAPIDiagnostic(t, artifact.Diagnostics, test.diagnostic)
			reference := uciRequireOpenAPIReference(t, artifact.References, "#/paths/~1pets/get/responses/200/content/application~1json/schema/$ref", "", test.state, uciOpenAPISpan(t, source, test.fragment, 0))
			if reference.TargetKey != "" {
				t.Fatalf("%s reference claimed local target %q despite %s source structure", test.name, reference.TargetKey, test.state)
			}
		})
	}
}

func TestUCIOpenAPIExtractionRefusesExternalReferences(t *testing.T) {
	for _, format := range []OpenAPIFormat{OpenAPIFormatJSON, OpenAPIFormatYAML} {
		for _, test := range []struct {
			name      string
			reference string
		}{
			{name: "http", reference: "https://example.invalid/Pet.yaml#/Pet"},
			{name: "file", reference: "file:///tmp/Pet.yaml#/Pet"},
			{name: "other document", reference: "other.yaml#/Pet"},
		} {
			t.Run(string(format)+"/"+test.name, func(t *testing.T) {
				artifact := ExtractOpenAPI([]byte(uciOpenAPIExternalReferenceFixture(format, test.reference)), uciOpenAPIProfile(format))

				if artifact.Coverage != IndexCoveragePartial {
					t.Fatalf("%s %s coverage = %q, want %q", format, test.name, artifact.Coverage, IndexCoveragePartial)
				}
				uciRequireOpenAPIExtractionProof(t, artifact)
				uciRequireOpenAPIDiagnostic(t, artifact.Diagnostics, "EXTERNAL_REFERENCE")
				if len(artifact.References) != 0 {
					t.Fatalf("%s %s reference was retained as a local fact: %#v", format, test.name, artifact.References)
				}
			})
		}
	}
}

type uciOpenAPISemanticFragments struct {
	version         string
	info            string
	path            string
	pathParameter   string
	operation       string
	parameter       string
	component       string
	pet             string
	petOccurrence   int
	limit           string
	limitOccurrence int
	limitRef        string
	petRef          string
}

func uciRequireOpenAPICoreFacts(t *testing.T, source []byte, artifact OpenAPIArtifact, fragments uciOpenAPISemanticFragments) {
	t.Helper()
	uciRequireOpenAPIDefinition(t, artifact.Definitions, "version", "version:3.0.3", uciOpenAPISpan(t, source, fragments.version, 0))
	uciRequireOpenAPIDefinition(t, artifact.Definitions, "info", "info", uciOpenAPISpan(t, source, fragments.info, 0))
	uciRequireOpenAPIDefinition(t, artifact.Definitions, "path", "path:/pets", uciOpenAPISpan(t, source, fragments.path, 0))
	uciRequireOpenAPIDefinition(t, artifact.Definitions, "parameter", "parameter:#/paths/~1pets/parameters/0", uciOpenAPISpan(t, source, fragments.pathParameter, 0))
	uciRequireOpenAPIDefinition(t, artifact.Definitions, "operation", "operation:get:/pets", uciOpenAPISpan(t, source, fragments.operation, 0))
	uciRequireOpenAPIDefinition(t, artifact.Definitions, "parameter", "parameter:#/paths/~1pets/get/parameters/0", uciOpenAPISpan(t, source, fragments.parameter, 0))
	uciRequireOpenAPIDefinition(t, artifact.Definitions, "parameter", "parameter:PetID", uciOpenAPISpan(t, source, fragments.component, 0))
	pet := uciRequireOpenAPIDefinition(t, artifact.Definitions, "schema", "schema:Pet", uciOpenAPISpan(t, source, fragments.pet, fragments.petOccurrence))
	limit := uciRequireOpenAPIDefinition(t, artifact.Definitions, "schema", "schema:Limit", uciOpenAPISpan(t, source, fragments.limit, fragments.limitOccurrence))
	uciRequireOpenAPIReference(t, artifact.References, "#/paths/~1pets/get/parameters/0/schema/$ref", limit.SymbolKey, IndexResolutionState("resolved"), uciOpenAPISpan(t, source, fragments.limitRef, 0))
	uciRequireOpenAPIReference(t, artifact.References, "#/paths/~1pets/get/responses/200/content/application~1json/schema/$ref", pet.SymbolKey, IndexResolutionState("resolved"), uciOpenAPISpan(t, source, fragments.petRef, 0))
}

func uciOpenAPIProfile(format OpenAPIFormat) OpenAPIExtractionProfile {
	return OpenAPIExtractionProfile{
		ProfileKey: "openapi-structure-v1",
		ParserKey:  "openapi-parser-v1",
		Format:     format,
	}
}

func uciRequireOpenAPIExtractionProof(t *testing.T, artifact OpenAPIArtifact) {
	t.Helper()
	uciRequireOpenAPIArtifactProofIdentity(t, artifact)
	uciRequireOpenAPIDefinitionProof(t, artifact)
	uciRequireOpenAPIReferenceProof(t, artifact)
	uciRequireOpenAPIChunkProof(t, artifact)
}

func uciRequireOpenAPIArtifactProofIdentity(t *testing.T, artifact OpenAPIArtifact) {
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
		t.Fatal("extraction returned no source chunks")
	}
}

func uciRequireOpenAPIDefinitionProof(t *testing.T, artifact OpenAPIArtifact) {
	t.Helper()
	definitionKeys := make(map[string]struct{}, len(artifact.Definitions))
	for _, definition := range artifact.Definitions {
		if definition.Kind == "" || definition.LocalKey == "" || definition.SymbolKey == "" {
			t.Fatalf("definition has incomplete stable identity: %#v", definition)
		}
		if _, exists := definitionKeys[definition.SymbolKey]; exists {
			t.Fatalf("duplicate definition symbol key %q", definition.SymbolKey)
		}
		definitionKeys[definition.SymbolKey] = struct{}{}
		uciRequireOpenAPISpanWithinText(t, definition.Span, artifact.Text, "definition "+definition.SymbolKey)
	}
}

func uciRequireOpenAPIReferenceProof(t *testing.T, artifact OpenAPIArtifact) {
	t.Helper()
	referenceKeys := make(map[string]struct{}, len(artifact.References))
	for _, reference := range artifact.References {
		if reference.Kind == "" || reference.LocalKey == "" || reference.SymbolKey == "" {
			t.Fatalf("reference has incomplete stable identity: %#v", reference)
		}
		if !isIndexResolutionState(reference.ResolutionState) {
			t.Fatalf("reference has unsupported resolution state %q: %#v", reference.ResolutionState, reference)
		}
		if reference.ResolutionState == IndexResolutionState("resolved") && reference.TargetKey == "" {
			t.Fatalf("resolved reference has no static target: %#v", reference)
		}
		if reference.ResolutionState != IndexResolutionState("resolved") && reference.TargetKey != "" {
			t.Fatalf("non-resolved reference claimed a static target: %#v", reference)
		}
		if _, exists := referenceKeys[reference.SymbolKey]; exists {
			t.Fatalf("duplicate reference symbol key %q", reference.SymbolKey)
		}
		referenceKeys[reference.SymbolKey] = struct{}{}
		uciRequireOpenAPISpanWithinText(t, reference.Span, artifact.Text, "reference "+reference.SymbolKey)
	}
}

func uciRequireOpenAPIChunkProof(t *testing.T, artifact OpenAPIArtifact) {
	t.Helper()
	var rebuilt strings.Builder
	var nextStart int64
	for index, chunk := range artifact.Chunks {
		if !isIndexDigest(chunk.ContentDigest) {
			t.Fatalf("chunk %d content digest %q is not a stable index digest", index, chunk.ContentDigest)
		}
		if !utf8.ValidString(chunk.Text) {
			t.Fatalf("chunk %d splits a UTF-8 sequence: %q", index, chunk.Text)
		}
		if chunk.Span.ByteStart != nextStart {
			t.Fatalf("chunk %d starts at %d, want contiguous offset %d", index, chunk.Span.ByteStart, nextStart)
		}
		uciRequireOpenAPISpanWithinText(t, chunk.Span, artifact.Text, "chunk")
		if want := artifact.Text[chunk.Span.ByteStart:chunk.Span.ByteEnd]; chunk.Text != want {
			t.Fatalf("chunk %d text = %q, want text at span %q", index, chunk.Text, want)
		}
		nextStart = chunk.Span.ByteEnd
		rebuilt.WriteString(chunk.Text)
	}
	if nextStart != int64(len(artifact.Text)) {
		t.Fatalf("chunk coverage ends at %d, want retained text length %d", nextStart, len(artifact.Text))
	}
	if rebuilt.String() != artifact.Text {
		t.Fatal("chunks do not losslessly reconstruct retained source text")
	}
}

func uciRequireOpenAPIDefinition(t *testing.T, definitions []OpenAPIDefinition, kind, localKey string, wantSpan IndexSpan) OpenAPIDefinition {
	t.Helper()
	var found *OpenAPIDefinition
	for index := range definitions {
		definition := &definitions[index]
		if definition.Kind != kind || definition.LocalKey != localKey {
			continue
		}
		if found != nil {
			t.Fatalf("duplicate %s definition %q: first=%#v duplicate=%#v", kind, localKey, *found, *definition)
		}
		found = definition
	}
	if found == nil {
		t.Fatalf("missing %s definition %q; got %#v", kind, localKey, definitions)
	}
	if found.SymbolKey == "" {
		t.Fatalf("%s definition %q has no stable symbol key", kind, localKey)
	}
	if found.Span != wantSpan {
		t.Fatalf("%s definition %q span = %#v, want %#v", kind, localKey, found.Span, wantSpan)
	}
	return *found
}

func uciRequireOpenAPIReference(t *testing.T, references []OpenAPIReferenceSite, localKey, targetKey string, state IndexResolutionState, wantSpan IndexSpan) OpenAPIReferenceSite {
	t.Helper()
	var found *OpenAPIReferenceSite
	for index := range references {
		reference := &references[index]
		if reference.Kind != "local_ref" || reference.LocalKey != localKey || reference.ResolutionState != state {
			continue
		}
		if found != nil {
			t.Fatalf("duplicate local reference %q in state %q: first=%#v duplicate=%#v", localKey, state, *found, *reference)
		}
		found = reference
	}
	if found == nil {
		t.Fatalf("missing local reference %q in state %q; got %#v", localKey, state, references)
	}
	if found.SymbolKey == "" {
		t.Fatalf("local reference %q has no stable source identity", localKey)
	}
	if found.TargetKey != targetKey {
		t.Fatalf("local reference %q target = %q, want %q", localKey, found.TargetKey, targetKey)
	}
	if found.Span != wantSpan {
		t.Fatalf("local reference %q span = %#v, want %#v", localKey, found.Span, wantSpan)
	}
	return *found
}

func uciRequireOpenAPIDiagnostic(t *testing.T, diagnostics []OpenAPIDiagnostic, code string) {
	t.Helper()
	for _, diagnostic := range diagnostics {
		if diagnostic.Code == code {
			return
		}
	}
	t.Fatalf("missing diagnostic %q; got %#v", code, diagnostics)
}

func uciRequireOpenAPISpanWithinText(t *testing.T, span IndexSpan, text, subject string) {
	t.Helper()
	if span.ByteStart < 0 || span.ByteEnd <= span.ByteStart || span.ByteEnd > int64(len(text)) {
		t.Fatalf("%s has invalid byte span %#v for %d-byte text", subject, span, len(text))
	}
	if span.LineStart < 1 || span.LineEnd < span.LineStart {
		t.Fatalf("%s has invalid inclusive line span %#v", subject, span)
	}
}

func uciRequireOpenAPIChunkContaining(t *testing.T, chunks []OpenAPIChunk, text string) {
	t.Helper()
	for _, chunk := range chunks {
		if strings.Contains(chunk.Text, text) {
			return
		}
	}
	t.Fatalf("no source chunk contains %q; got %#v", text, chunks)
}

func uciOpenAPISpan(t *testing.T, source []byte, fragment string, occurrence int) IndexSpan {
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

func uciOpenAPILocalReferenceJSONFixture(reference string, duplicatePet bool) string {
	schemas := "\"Pet\":{\"type\":\"object\"}"
	if duplicatePet {
		schemas += ",\"Pet\":{\"type\":\"string\"}"
	}
	return "{\"openapi\":\"3.0.3\",\"info\":{\"title\":\"Pets\",\"version\":\"1\"},\"paths\":{\"/pets\":{\"get\":{\"responses\":{\"200\":{\"description\":\"ok\",\"content\":{\"application/json\":{\"schema\":{\"$ref\":\"" + reference + "\"}}}}}}}}},\"components\":{\"schemas\":{" + schemas + "}}}"
}

func uciOpenAPILocalReferenceYAMLFixture(reference string, duplicatePet bool) string {
	petSchemas := "    Pet:\n" +
		"      type: object\n"
	if duplicatePet {
		petSchemas += "    Pet:\n" +
			"      type: string\n"
	}
	return "openapi: 3.0.3\n" +
		"info:\n" +
		"  title: Pets\n" +
		"  version: 1\n" +
		"paths:\n" +
		"  /pets:\n" +
		"    get:\n" +
		"      responses:\n" +
		"        \"200\":\n" +
		"          description: ok\n" +
		"          content:\n" +
		"            application/json:\n" +
		"              schema:\n" +
		"                $ref: \"" + reference + "\"\n" +
		"components:\n" +
		"  schemas:\n" +
		petSchemas
}

func uciOpenAPIExternalReferenceFixture(format OpenAPIFormat, reference string) string {
	if format == OpenAPIFormatJSON {
		return "{\"openapi\":\"3.0.3\",\"info\":{\"title\":\"Pets\",\"version\":\"1\"},\"paths\":{\"/pets\":{\"get\":{\"responses\":{\"200\":{\"description\":\"ok\",\"content\":{\"application/json\":{\"schema\":{\"$ref\":\"" + reference + "\"}}}}}}}}}}"
	}
	return "openapi: 3.0.3\n" +
		"info:\n" +
		"  title: Pets\n" +
		"  version: 1\n" +
		"paths:\n" +
		"  /pets:\n" +
		"    get:\n" +
		"      responses:\n" +
		"        \"200\":\n" +
		"          description: ok\n" +
		"          content:\n" +
		"            application/json:\n" +
		"              schema:\n" +
		"                $ref: \"" + reference + "\"\n"
}
