package uci

import (
	"bytes"
	"reflect"
	"strings"
	"testing"
)

const uciGoExtractionFixture = `package sample

import "fmt"

type Worker struct{}

var _ Worker

func local() {}

func (Worker) Run() {
	fmt.Println("run")
	local()
}
`

func TestUCIGoExtractionProducesCompleteStableFacts(t *testing.T) {
	source := []byte(uciGoExtractionFixture)
	profile := uciGoExtractionProfile()

	first := ExtractGo(source, profile)
	second := ExtractGo(append([]byte(nil), source...), profile)

	if first.Coverage != IndexCoverageComplete {
		t.Fatalf("ExtractGo() coverage = %q, want %q", first.Coverage, IndexCoverageComplete)
	}
	if first.Text != string(source) {
		t.Fatalf("ExtractGo() text = %q, want exact source bytes", first.Text)
	}
	if !reflect.DeepEqual(first, second) {
		t.Fatalf("ExtractGo() facts are not stable across identical byte/profile input:\nfirst: %#v\nsecond: %#v", first, second)
	}
	uciRequireGoExtractionProof(t, first)

	uciRequireGoDefinition(t, first.Definitions, "package", "go:sample/pkg:sample", "pkg:sample", uciGoExtractionSpan(t, source, "package sample", 0))
	uciRequireGoDefinition(t, first.Definitions, "type", "go:sample/type:Worker", "type:Worker", uciGoExtractionSpan(t, source, "type Worker struct{}", 0))
	uciRequireGoDefinition(t, first.Definitions, "function", "go:sample/func:local", "func:local", uciGoExtractionSpan(t, source, "func local() {}", 0))
	uciRequireGoDefinition(t, first.Definitions, "method", "go:sample/method:Worker.Run", "method:Worker.Run", uciGoExtractionSpan(t, source, "func (Worker) Run() {\n\tfmt.Println(\"run\")\n\tlocal()\n}", 0))

	uciRequireGoReference(t, first.References, "import", "go:sample/import:fmt", "import:fmt", uciGoExtractionSpan(t, source, "\"fmt\"", 0))
	uciRequireGoReference(t, first.References, "reference", "go:sample/ref:Worker", "ref:Worker", uciGoExtractionSpan(t, source, "Worker", 1))
	uciRequireGoReference(t, first.References, "call", "go:sample/call:fmt.Println", "call:fmt.Println", uciGoExtractionSpan(t, source, "fmt.Println", 0))
	uciRequireGoReference(t, first.References, "call", "go:sample/call:local", "call:local", uciGoExtractionSpan(t, source, "local", 1))
}

func TestUCIGoExtractionSeparatesArtifactIdentityFromMembership(t *testing.T) {
	source := []byte(uciGoExtractionFixture)
	profile := uciGoExtractionProfile()

	first := ExtractGo(source, profile)
	reused := ExtractGo(append([]byte(nil), source...), profile)
	uciRequireGoExtractionProof(t, first)
	if !reflect.DeepEqual(first.Proof, reused.Proof) {
		t.Fatalf("identical source bytes/profile produced different artifact proof: first=%#v reused=%#v", first.Proof, reused.Proof)
	}

	firstArtifactID := first.Proof.ArtifactID
	reusedArtifactID := reused.Proof.ArtifactID
	memberships := []IndexMembership{
		{
			PathKey:     "checkout-a:internal/sample.go",
			DisplayPath: "internal/sample.go",
			Mode:        "100644",
			State:       IndexFilePresent,
			ArtifactID:  &firstArtifactID,
		},
		{
			PathKey:     "checkout-b:internal/sample.go",
			DisplayPath: "internal/sample.go",
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
		profile GoExtractionProfile
	}{
		{
			name:    "profile key",
			profile: GoExtractionProfile{ProfileKey: "go-structure-v2", ParserKey: profile.ParserKey},
		},
		{
			name:    "parser key",
			profile: GoExtractionProfile{ProfileKey: profile.ProfileKey, ParserKey: "go-parser-v2"},
		},
	} {
		t.Run(variant.name, func(t *testing.T) {
			changed := ExtractGo(source, variant.profile)
			if changed.Proof.ArtifactID == first.Proof.ArtifactID {
				t.Fatalf("%s change reused artifact identity %q", variant.name, changed.Proof.ArtifactID)
			}
		})
	}

	changedBytes := append(append([]byte(nil), source...), []byte("// byte change\n")...)
	changed := ExtractGo(changedBytes, profile)
	if changed.Proof.ArtifactID == first.Proof.ArtifactID {
		t.Fatalf("changed source bytes reused artifact identity %q", changed.Proof.ArtifactID)
	}
}

func TestUCIGoExtractionReturnsFreshPartialFactsForMalformedSource(t *testing.T) {
	profile := uciGoExtractionProfile()
	complete := ExtractGo([]byte(uciGoExtractionFixture), profile)
	malformedSource := []byte("package broken\n\nfunc unfinished( {\n")

	partial := ExtractGo(malformedSource, profile)
	if partial.Coverage != IndexCoveragePartial {
		t.Fatalf("ExtractGo(malformed) coverage = %q, want %q", partial.Coverage, IndexCoveragePartial)
	}
	if partial.Text != string(malformedSource) {
		t.Fatalf("ExtractGo(malformed) text = %q, want exact malformed source", partial.Text)
	}
	if !strings.Contains(partial.Text, "unfinished") {
		t.Fatalf("ExtractGo(malformed) text lost usable source content: %q", partial.Text)
	}
	uciRequireGoExtractionProof(t, partial)
	if partial.Proof.ArtifactID == complete.Proof.ArtifactID {
		t.Fatalf("malformed source reused complete artifact identity %q", partial.Proof.ArtifactID)
	}
	if partial.Proof.ContentDigest == complete.Proof.ContentDigest || partial.Proof.FactsDigest == complete.Proof.FactsDigest {
		t.Fatalf("malformed source reused complete artifact digests: partial=%#v complete=%#v", partial.Proof, complete.Proof)
	}
	for _, definition := range partial.Definitions {
		if strings.HasPrefix(definition.SymbolKey, "go:sample/") {
			t.Fatalf("malformed source reused stale complete definition: %#v", definition)
		}
	}
	for _, reference := range partial.References {
		if strings.HasPrefix(reference.SymbolKey, "go:sample/") {
			t.Fatalf("malformed source reused stale complete reference: %#v", reference)
		}
	}
}

func uciGoExtractionProfile() GoExtractionProfile {
	return GoExtractionProfile{
		ProfileKey: "go-structure-v1",
		ParserKey:  "go-parser-v1",
	}
}

func uciRequireGoExtractionProof(t *testing.T, artifact GoArtifact) {
	t.Helper()
	uciRequireGoArtifactProofIdentity(t, artifact)
	uciRequireGoArtifactChunks(t, artifact)
}

func uciRequireGoArtifactProofIdentity(t *testing.T, artifact GoArtifact) {
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
}

func uciRequireGoArtifactChunks(t *testing.T, artifact GoArtifact) {
	t.Helper()
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

func uciRequireGoDefinition(t *testing.T, definitions []GoDefinition, kind, symbolKey, localKey string, wantSpan IndexSpan) {
	t.Helper()
	for _, definition := range definitions {
		if string(definition.Kind) != kind || definition.SymbolKey != symbolKey {
			continue
		}
		if definition.LocalKey != localKey {
			t.Fatalf("definition %q local key = %q, want %q", symbolKey, definition.LocalKey, localKey)
		}
		uciRequireGoSpan(t, definition.Span, wantSpan, "definition "+symbolKey)
		return
	}
	t.Fatalf("missing %s definition %q; got %#v", kind, symbolKey, definitions)
}

func uciRequireGoReference(t *testing.T, references []GoReferenceSite, kind, symbolKey, localKey string, wantSpan IndexSpan) {
	t.Helper()
	for _, reference := range references {
		if string(reference.Kind) != kind || reference.SymbolKey != symbolKey {
			continue
		}
		if reference.LocalKey != localKey {
			t.Fatalf("reference %q local key = %q, want %q", symbolKey, reference.LocalKey, localKey)
		}
		uciRequireGoSpan(t, reference.Span, wantSpan, "reference "+symbolKey)
		return
	}
	t.Fatalf("missing %s reference %q; got %#v", kind, symbolKey, references)
}

func uciRequireGoSpan(t *testing.T, got, want IndexSpan, subject string) {
	t.Helper()
	if got != want {
		t.Fatalf("%s span = %#v, want %#v", subject, got, want)
	}
}

func uciGoExtractionSpan(t *testing.T, source []byte, fragment string, occurrence int) IndexSpan {
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
