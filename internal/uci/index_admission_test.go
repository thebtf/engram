package uci

import (
	"bytes"
	"reflect"
	"testing"
)

const (
	indexAdmissionTestSourceA  = "11111111-1111-5111-8111-111111111111"
	indexAdmissionTestSourceB  = "22222222-2222-5222-8222-222222222222"
	indexAdmissionTestCheckout = "33333333-3333-5333-8333-333333333333"
	indexAdmissionTestEpoch    = "44444444-4444-5444-8444-444444444444"
	indexAdmissionTestProfile  = "55555555-5555-5555-8555-555555555555"
)

func TestIndexAdmissionArtifactIdentityIsDeterministicAndSourceScoped(t *testing.T) {
	t.Parallel()
	profile, err := GoIndexAdmissionArtifactProfile(indexAdmissionTestGoProfile())
	if err != nil {
		t.Fatalf("GoIndexAdmissionArtifactProfile() error = %v", err)
	}
	source := []byte(indexAdmissionTestGoSourceA())
	artifact := ExtractGo(source, indexAdmissionTestGoProfile())

	first, err := NewIndexAdmissionArtifactFromGo(indexAdmissionTestSourceA, profile, source, artifact)
	if err != nil {
		t.Fatalf("first artifact error = %v", err)
	}
	second, err := NewIndexAdmissionArtifactFromGo(indexAdmissionTestSourceA, profile, append([]byte(nil), source...), ExtractGo(append([]byte(nil), source...), indexAdmissionTestGoProfile()))
	if err != nil {
		t.Fatalf("second artifact error = %v", err)
	}
	otherSource, err := NewIndexAdmissionArtifactFromGo(indexAdmissionTestSourceB, profile, source, artifact)
	if err != nil {
		t.Fatalf("other-source artifact error = %v", err)
	}
	if first.ArtifactID != second.ArtifactID {
		t.Fatalf("same Source/content/profile ArtifactID differs: %q != %q", first.ArtifactID, second.ArtifactID)
	}
	if first.ArtifactID == otherSource.ArtifactID {
		t.Fatalf("different Sources reused ArtifactID %q", first.ArtifactID)
	}

	frame := indexAdmissionTestFrame(t)
	binding := indexAdmissionTestBinding()
	if err := ValidateIndexAdmissionFrameForBinding(frame, binding); err != nil {
		t.Fatalf("ValidateIndexAdmissionFrameForBinding() error = %v", err)
	}
	binding.Scope.SourceID = indexAdmissionTestSourceB
	if err := ValidateIndexAdmissionFrameForBinding(frame, binding); err == nil {
		t.Fatal("ValidateIndexAdmissionFrameForBinding() accepted an artifact under another Source")
	}
}

func TestIndexAdmissionGoReferenceOwnersAreNarrowAndFactBound(t *testing.T) {
	t.Parallel()
	artifact := indexAdmissionTestArtifact(t, indexAdmissionTestSourceA, []byte(indexAdmissionTestGoSourceA()))
	callIndex := -1
	for index, reference := range artifact.References {
		if reference.Kind == "import" {
			if reference.OwnerSymbolKey != nil {
				t.Fatalf("import owner = %q, want nil", *reference.OwnerSymbolKey)
			}
			continue
		}
		if reference.OwnerSymbolKey == nil || *reference.OwnerSymbolKey != "func:Run" {
			t.Fatalf("reference %#v owner = %v, want func:Run", reference, reference.OwnerSymbolKey)
		}
		if reference.Kind == "call" {
			callIndex = index
		}
	}
	if callIndex < 0 {
		t.Fatal("fixture has no Go call reference")
	}
	originalDigest, err := DigestIndexAdmissionArtifactFacts(artifact)
	if err != nil {
		t.Fatalf("DigestIndexAdmissionArtifactFacts() error = %v", err)
	}
	changed := artifact
	changed.References = append([]IndexAdmissionReference(nil), artifact.References...)
	packageOwner := "pkg:source"
	changed.References[callIndex].OwnerSymbolKey = &packageOwner
	changedDigest, err := DigestIndexAdmissionArtifactFacts(changed)
	if err != nil {
		t.Fatalf("DigestIndexAdmissionArtifactFacts(changed) error = %v", err)
	}
	if originalDigest == changedDigest {
		t.Fatal("reference owner was omitted from canonical facts digest")
	}

	frame := indexAdmissionTestFrame(t)
	unknownOwner := "func:missing"
	frame.Artifacts[0].References[callIndex].OwnerSymbolKey = &unknownOwner
	if err := ValidateIndexAdmissionFrame(frame); err == nil {
		t.Fatal("ValidateIndexAdmissionFrame() accepted an undefined reference owner")
	}

	sourceFrame, targetFrame := indexAdmissionTestSplitFrames(t)
	sourceFrame.EdgeReplacements[0].Edges[0].SourceSymbolKey = nil
	if err := ValidateIndexAdmissionFrames([]IndexAdmissionFrame{sourceFrame, targetFrame}); err == nil {
		t.Fatal("ValidateIndexAdmissionFrames() accepted edge caller that differs from reference owner")
	}
}

func TestIndexAdmissionRejectsEdgeRelationMismatchedToSourceReference(t *testing.T) {
	t.Parallel()
	sourceFrame, targetFrame := indexAdmissionTestSplitFrames(t)
	edge := &sourceFrame.EdgeReplacements[0].Edges[0]
	if edge.Relation == IndexRelation("calls") {
		edge.Relation = IndexRelation("imports")
	} else {
		edge.Relation = IndexRelation("calls")
	}
	if err := ValidateIndexAdmissionFrames([]IndexAdmissionFrame{sourceFrame, targetFrame}); err == nil {
		t.Fatal("ValidateIndexAdmissionFrames() accepted an edge relation that differs from its source reference")
	}
}

func TestIndexAdmissionGoDefinitionChunksBindEachFunction(t *testing.T) {
	t.Parallel()
	source := []byte("package sample\n\nfunc Caller() {}\n\nfunc Target() {}\n")
	artifact := indexAdmissionTestArtifact(t, indexAdmissionTestSourceA, source)

	functions := 0
	for _, definition := range artifact.Definitions {
		if definition.Kind != "function" {
			continue
		}
		functions++
		var matched *IndexAdmissionChunk
		for index := range artifact.Chunks {
			chunk := &artifact.Chunks[index]
			if chunk.SymbolKey != nil && *chunk.SymbolKey == definition.LocalSymbolKey {
				matched = chunk
				break
			}
		}
		if matched == nil {
			t.Fatalf("function %q has no symbol-bound chunk", definition.LocalSymbolKey)
		}
		if matched.Span != definition.Span {
			t.Fatalf("function chunk span = %#v, want %#v", matched.Span, definition.Span)
		}
		text, err := indexAdmissionTextAtSpan(source, definition.Span)
		if err != nil {
			t.Fatalf("definition text error = %v", err)
		}
		if matched.Text != text || matched.ContentDigest != indexAdmissionDigestBytes([]byte(text)) {
			t.Fatalf("function chunk does not exactly bind %q", definition.LocalSymbolKey)
		}
	}
	if functions != 2 {
		t.Fatalf("function definitions = %d, want 2", functions)
	}
}

func TestIndexAdmissionEncodeCanonicalizesCollections(t *testing.T) {
	t.Parallel()
	frame := indexAdmissionTestFrame(t)
	second := indexAdmissionTestArtifact(t, indexAdmissionTestSourceA, []byte("package bravo\n\nfunc Target() {}\n"))
	secondID := second.ArtifactID
	frame.Artifacts = append(frame.Artifacts, second)
	frame.Memberships = append(frame.Memberships, IndexAdmissionMembership{
		PathKey:     "b.go",
		DisplayPath: "b.go",
		Mode:        "100644",
		State:       IndexAdmissionMembershipPresent,
		ArtifactID:  &secondID,
	})
	canonical, err := EncodeIndexAdmissionFrame(frame)
	if err != nil {
		t.Fatalf("EncodeIndexAdmissionFrame() error = %v", err)
	}
	frame.Artifacts[0], frame.Artifacts[1] = frame.Artifacts[1], frame.Artifacts[0]
	frame.Memberships[0], frame.Memberships[1] = frame.Memberships[1], frame.Memberships[0]
	reordered, err := EncodeIndexAdmissionFrame(frame)
	if err != nil {
		t.Fatalf("EncodeIndexAdmissionFrame(reordered) error = %v", err)
	}
	if !bytes.Equal(canonical, reordered) {
		t.Fatalf("canonical encodings differ:\nfirst=%s\nsecond=%s", canonical, reordered)
	}
}

func TestIndexAdmissionStrictDecodeRejectsUnknownTrailingAndDuplicateJSON(t *testing.T) {
	t.Parallel()
	frame := indexAdmissionTestFrame(t)
	encoded, err := EncodeIndexAdmissionFrame(frame)
	if err != nil {
		t.Fatalf("EncodeIndexAdmissionFrame() error = %v", err)
	}
	decoded, err := DecodeIndexAdmissionFrame(encoded)
	if err != nil {
		t.Fatalf("DecodeIndexAdmissionFrame() error = %v", err)
	}
	if !reflect.DeepEqual(decoded, frame.CanonicalMust(t)) {
		t.Fatalf("DecodeIndexAdmissionFrame() = %#v, want canonical %#v", decoded, frame.CanonicalMust(t))
	}

	unknown := append([]byte(`{"unexpected":true,`), encoded[1:]...)
	if _, err := DecodeIndexAdmissionFrame(unknown); err == nil {
		t.Fatal("DecodeIndexAdmissionFrame() accepted an unknown JSON field")
	}
	trailing := append(append([]byte(nil), encoded...), []byte(` {}`)...)
	if _, err := DecodeIndexAdmissionFrame(trailing); err == nil {
		t.Fatal("DecodeIndexAdmissionFrame() accepted trailing JSON")
	}
	duplicate := []byte(`{"version":"uci-index-admission/v1","version":"uci-index-admission/v1"}`)
	if _, err := DecodeIndexAdmissionFrame(duplicate); err == nil {
		t.Fatal("DecodeIndexAdmissionFrame() accepted a duplicate JSON object key")
	}

	withWhitespace := append([]byte(" \n"), append(encoded, '\n')...)
	if DigestIndexAdmissionPayload(encoded) == DigestIndexAdmissionPayload(withWhitespace) {
		t.Fatal("DigestIndexAdmissionPayload() canonicalized exact payload bytes")
	}
}

func TestIndexAdmissionRejectsSourceSpanBodyAndChunkDigestMismatches(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		mutate func(*IndexAdmissionFrame)
	}{
		{
			name: "body digest",
			mutate: func(frame *IndexAdmissionFrame) {
				frame.Artifacts[0].Body[0] ^= 1
			},
		},
		{
			name: "definition span",
			mutate: func(frame *IndexAdmissionFrame) {
				frame.Artifacts[0].Definitions[0].Span.ByteEnd = frame.Artifacts[0].Definitions[0].Span.ByteStart
			},
		},
		{
			name: "chunk digest",
			mutate: func(frame *IndexAdmissionFrame) {
				frame.Artifacts[0].Chunks[0].ContentDigest = DigestIndexAdmissionPayload([]byte("different chunk"))
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			frame := indexAdmissionTestFrame(t)
			test.mutate(&frame)
			if err := ValidateIndexAdmissionFrame(frame); err == nil {
				t.Fatal("ValidateIndexAdmissionFrame() accepted mismatched source evidence")
			}
		})
	}
}

func TestIndexAdmissionRejectsDuplicateFactKeys(t *testing.T) {
	t.Parallel()
	frame := indexAdmissionTestFrame(t)
	frame.Artifacts[0].Definitions = append(frame.Artifacts[0].Definitions, frame.Artifacts[0].Definitions[0])
	if err := ValidateIndexAdmissionFrame(frame); err == nil {
		t.Fatal("ValidateIndexAdmissionFrame() accepted duplicate definition key")
	}
}

func TestIndexAdmissionRejectsCrossArtifactEdgeTarget(t *testing.T) {
	t.Parallel()
	frame := indexAdmissionTestFrame(t)
	target := indexAdmissionTestArtifact(t, indexAdmissionTestSourceA, []byte("package target\n\nfunc Target() {}\n"))
	targetID := target.ArtifactID
	frame.Artifacts = append(frame.Artifacts, target)
	frame.Memberships = append(frame.Memberships, IndexAdmissionMembership{
		PathKey:     "target.go",
		DisplayPath: "target.go",
		Mode:        "100644",
		State:       IndexAdmissionMembershipPresent,
		ArtifactID:  &targetID,
	})
	source := frame.Artifacts[0]
	reference := source.References[0]
	frame.EdgeReplacements = []IndexAdmissionEdgeReplacement{{
		SourcePath: "source.go",
		Edges: []IndexAdmissionEdge{{
			EdgeKey:          "source-to-target",
			SourceArtifactID: source.ArtifactID,
			SourceSymbolKey:  reference.OwnerSymbolKey,
			Target: &IndexAdmissionEdgeTarget{
				PathKey:    "target.go",
				ArtifactID: source.ArtifactID,
			},
			Relation:         reference.Relation,
			EvidenceKind:     IndexEvidenceKind("extracted"),
			ResolutionState:  IndexResolutionState("resolved"),
			ResolverRevision: "resolver/v1",
			Evidence: IndexAdmissionEdgeEvidence{
				ReferenceSiteKey: reference.SiteKey,
				Span:             reference.Span,
				RuleKey:          "source-fact",
				Explanation:      "exact source reference",
			},
		}},
	}}
	if err := ValidateIndexAdmissionFrames([]IndexAdmissionFrame{frame}); err == nil {
		t.Fatal("ValidateIndexAdmissionFrames() accepted edge target with another artifact ID")
	}
}

func TestIndexAdmissionBuildValidationResolvesCrossFrameEdges(t *testing.T) {
	t.Parallel()
	sourceFrame, targetFrame := indexAdmissionTestSplitFrames(t)
	if err := ValidateIndexAdmissionFrame(sourceFrame); err != nil {
		t.Fatalf("ValidateIndexAdmissionFrame(source) error = %v", err)
	}
	if err := ValidateIndexAdmissionFrame(targetFrame); err != nil {
		t.Fatalf("ValidateIndexAdmissionFrame(target) error = %v", err)
	}
	frames := []IndexAdmissionFrame{sourceFrame, targetFrame}
	if err := ValidateIndexAdmissionFrames(frames); err != nil {
		t.Fatalf("ValidateIndexAdmissionFrames() error = %v", err)
	}
	if err := ValidateIndexAdmissionFramesForBinding(frames, indexAdmissionTestBinding()); err != nil {
		t.Fatalf("ValidateIndexAdmissionFramesForBinding() error = %v", err)
	}
	payloads := make([][]byte, 0, len(frames))
	for _, frame := range frames {
		encoded, err := EncodeIndexAdmissionFrame(frame)
		if err != nil {
			t.Fatalf("EncodeIndexAdmissionFrame() error = %v", err)
		}
		payloads = append(payloads, encoded)
	}
	if err := ValidateIndexAdmissionPayloads(payloads); err != nil {
		t.Fatalf("ValidateIndexAdmissionPayloads() error = %v", err)
	}
	if _, err := sourceFrame.PublicationPart(); err != nil {
		t.Fatalf("PublicationPart() cross-frame edge error = %v", err)
	}
}

func TestIndexAdmissionBuildValidationRejectsMissingMismatchedAndDuplicateGlobalEntries(t *testing.T) {
	t.Parallel()
	t.Run("missing target", func(t *testing.T) {
		sourceFrame, _ := indexAdmissionTestSplitFrames(t)
		if err := ValidateIndexAdmissionFrames([]IndexAdmissionFrame{sourceFrame}); err == nil {
			t.Fatal("ValidateIndexAdmissionFrames() accepted missing global target")
		}
	})
	t.Run("mismatched target", func(t *testing.T) {
		sourceFrame, targetFrame := indexAdmissionTestSplitFrames(t)
		wrongID := sourceFrame.Artifacts[0].ArtifactID
		targetFrame.Memberships[0].ArtifactID = &wrongID
		if err := ValidateIndexAdmissionFrames([]IndexAdmissionFrame{sourceFrame, targetFrame}); err == nil {
			t.Fatal("ValidateIndexAdmissionFrames() accepted mismatched global target")
		}
	})
	t.Run("duplicate artifact", func(t *testing.T) {
		sourceFrame, targetFrame := indexAdmissionTestSplitFrames(t)
		duplicate := IndexAdmissionFrame{
			Version:   IndexAdmissionFrameVersion,
			Profile:   IndexAdmissionProfile{ID: indexAdmissionTestProfile},
			Artifacts: []IndexAdmissionArtifact{sourceFrame.Artifacts[0]},
		}
		if err := ValidateIndexAdmissionFrames([]IndexAdmissionFrame{sourceFrame, targetFrame, duplicate}); err == nil {
			t.Fatal("ValidateIndexAdmissionFrames() accepted duplicate artifact across frames")
		}
	})
	t.Run("duplicate membership", func(t *testing.T) {
		sourceFrame, targetFrame := indexAdmissionTestSplitFrames(t)
		duplicate := IndexAdmissionFrame{
			Version:     IndexAdmissionFrameVersion,
			Profile:     IndexAdmissionProfile{ID: indexAdmissionTestProfile},
			Memberships: []IndexAdmissionMembership{sourceFrame.Memberships[0]},
		}
		if err := ValidateIndexAdmissionFrames([]IndexAdmissionFrame{sourceFrame, targetFrame, duplicate}); err == nil {
			t.Fatal("ValidateIndexAdmissionFrames() accepted duplicate membership across frames")
		}
	})
}

func TestIndexAdmissionUnsupportedMembershipCarriesNoArtifactBody(t *testing.T) {
	t.Parallel()
	frame := IndexAdmissionFrame{
		Version: IndexAdmissionFrameVersion,
		Profile: IndexAdmissionProfile{ID: indexAdmissionTestProfile},
		Memberships: []IndexAdmissionMembership{{
			PathKey:     "legacy.unsupported",
			DisplayPath: "legacy.unsupported",
			Mode:        "100644",
			State:       IndexAdmissionMembershipUnsupported,
		}},
	}
	if err := ValidateIndexAdmissionFrame(frame); err != nil {
		t.Fatalf("ValidateIndexAdmissionFrame() error = %v", err)
	}
	part, err := frame.PublicationPart()
	if err != nil {
		t.Fatalf("PublicationPart() error = %v", err)
	}
	if len(part.Artifacts) != 0 || len(part.Memberships) != 1 || part.Memberships[0].ArtifactID != nil || part.Memberships[0].State != IndexFileExcluded {
		t.Fatalf("PublicationPart() = %#v, want explicit excluded membership without artifact", part)
	}
}

func TestIndexAdmissionRejectsAbsoluteAndParentPaths(t *testing.T) {
	t.Parallel()
	for _, invalid := range []string{"../secret.go", "/private/source.go", "C:/private/source.go", "file:///private/source.go"} {
		frame := indexAdmissionTestFrame(t)
		frame.Memberships[0].PathKey = invalid
		frame.Memberships[0].DisplayPath = invalid
		if err := ValidateIndexAdmissionFrame(frame); err == nil {
			t.Fatalf("ValidateIndexAdmissionFrame() accepted private locator %q", invalid)
		}
	}
}

func TestIndexAdmissionCloneDoesNotShareMutableState(t *testing.T) {
	t.Parallel()
	frame := indexAdmissionTestFrame(t)
	clone := frame.Clone()
	originalByte := frame.Artifacts[0].Body[0]
	clone.Artifacts[0].Body[0] ^= 1
	if frame.Artifacts[0].Body[0] != originalByte {
		t.Fatal("Clone() shares artifact body bytes")
	}
	cloneArtifactID := "66666666-6666-5666-8666-666666666666"
	*clone.Memberships[0].ArtifactID = cloneArtifactID
	if *frame.Memberships[0].ArtifactID == cloneArtifactID {
		t.Fatal("Clone() shares membership artifact pointer")
	}
	ownerIndex := -1
	for index, reference := range clone.Artifacts[0].References {
		if reference.OwnerSymbolKey != nil {
			ownerIndex = index
			break
		}
	}
	if ownerIndex < 0 {
		t.Fatal("fixture has no owned reference")
	}
	cloneOwner := "func:Changed"
	*clone.Artifacts[0].References[ownerIndex].OwnerSymbolKey = cloneOwner
	if *frame.Artifacts[0].References[ownerIndex].OwnerSymbolKey == cloneOwner {
		t.Fatal("Clone() shares reference owner pointer")
	}
}

func TestIndexAdmissionPublicationPartUsesDeterministicReferenceIDs(t *testing.T) {
	t.Parallel()
	frame := indexAdmissionTestFrame(t)
	source := frame.Artifacts[0]
	reference := source.References[0]
	frame.EdgeReplacements = []IndexAdmissionEdgeReplacement{{
		SourcePath: "source.go",
		Edges: []IndexAdmissionEdge{{
			EdgeKey:          "self-reference",
			SourceArtifactID: source.ArtifactID,
			SourceSymbolKey:  reference.OwnerSymbolKey,
			Relation:         reference.Relation,
			EvidenceKind:     IndexEvidenceKind("extracted"),
			ResolutionState:  IndexResolutionState("unresolved"),
			ResolverRevision: "resolver/v1",
			Evidence: IndexAdmissionEdgeEvidence{
				ReferenceSiteKey: reference.SiteKey,
				Span:             reference.Span,
				RuleKey:          "source-fact",
				Explanation:      "exact source reference",
			},
		}},
	}}
	bindings, err := frame.ReferenceBindings()
	if err != nil {
		t.Fatalf("ReferenceBindings() error = %v", err)
	}
	expectedID, err := DeriveIndexAdmissionReferenceSiteID(source.ArtifactID, reference.SiteKey)
	if err != nil {
		t.Fatalf("DeriveIndexAdmissionReferenceSiteID() error = %v", err)
	}
	if bindings[IndexAdmissionReferenceKey{ArtifactID: source.ArtifactID, SiteKey: reference.SiteKey}] != expectedID {
		t.Fatal("ReferenceBindings() did not return deterministic reference-site ID")
	}
	first, err := frame.PublicationPart()
	if err != nil {
		t.Fatalf("PublicationPart() error = %v", err)
	}
	second, err := frame.IndexPart(nil)
	if err != nil {
		t.Fatalf("IndexPart(nil) error = %v", err)
	}
	if !reflect.DeepEqual(first, second) {
		t.Fatalf("PublicationPart() = %#v, IndexPart(nil) = %#v", first, second)
	}
	actualID := first.EdgeReplacements[0].Edges[0].Evidence.ReferenceSiteID
	if actualID == nil || *actualID != expectedID {
		t.Fatalf("PublicationPart() reference ID = %v, want %q", actualID, expectedID)
	}
}

func indexAdmissionTestFrame(t *testing.T) IndexAdmissionFrame {
	t.Helper()
	artifact := indexAdmissionTestArtifact(t, indexAdmissionTestSourceA, []byte(indexAdmissionTestGoSourceA()))
	artifactID := artifact.ArtifactID
	return IndexAdmissionFrame{
		Version:   IndexAdmissionFrameVersion,
		Profile:   IndexAdmissionProfile{ID: indexAdmissionTestProfile},
		Artifacts: []IndexAdmissionArtifact{artifact},
		Memberships: []IndexAdmissionMembership{{
			PathKey:     "source.go",
			DisplayPath: "source.go",
			Mode:        "100644",
			State:       IndexAdmissionMembershipPresent,
			ArtifactID:  &artifactID,
		}},
	}
}

func indexAdmissionTestSplitFrames(t *testing.T) (IndexAdmissionFrame, IndexAdmissionFrame) {
	t.Helper()
	sourceFrame := indexAdmissionTestFrame(t)
	target := indexAdmissionTestArtifact(t, indexAdmissionTestSourceA, []byte("package target\n\nfunc Target() {}\n"))
	targetID := target.ArtifactID
	targetFrame := IndexAdmissionFrame{
		Version:   IndexAdmissionFrameVersion,
		Profile:   IndexAdmissionProfile{ID: indexAdmissionTestProfile},
		Artifacts: []IndexAdmissionArtifact{target},
		Memberships: []IndexAdmissionMembership{{
			PathKey:     "target.go",
			DisplayPath: "target.go",
			Mode:        "100644",
			State:       IndexAdmissionMembershipPresent,
			ArtifactID:  &targetID,
		}},
	}
	source := sourceFrame.Artifacts[0]
	reference := source.References[0]
	sourceFrame.EdgeReplacements = []IndexAdmissionEdgeReplacement{{
		SourcePath: "source.go",
		Edges: []IndexAdmissionEdge{{
			EdgeKey:          "source-to-target",
			SourceArtifactID: source.ArtifactID,
			SourceSymbolKey:  reference.OwnerSymbolKey,
			Target: &IndexAdmissionEdgeTarget{
				PathKey:    "target.go",
				ArtifactID: targetID,
			},
			Relation:         reference.Relation,
			EvidenceKind:     IndexEvidenceKind("extracted"),
			ResolutionState:  IndexResolutionState("resolved"),
			ResolverRevision: "resolver/v1",
			Evidence: IndexAdmissionEdgeEvidence{
				ReferenceSiteKey: reference.SiteKey,
				Span:             reference.Span,
				RuleKey:          "source-fact",
				Explanation:      "exact source reference",
			},
		}},
	}}
	return sourceFrame, targetFrame
}

func indexAdmissionTestBinding() IndexBinding {
	return IndexBinding{
		Scope: IndexScope{
			SourceID:      indexAdmissionTestSourceA,
			CheckoutID:    indexAdmissionTestCheckout,
			IncarnationID: indexAdmissionTestEpoch,
		},
		ProfileID:     indexAdmissionTestProfile,
		LocalRootID:   "local-root",
		WorkstationID: "workstation",
	}
}

func indexAdmissionTestArtifact(t *testing.T, sourceID string, source []byte) IndexAdmissionArtifact {
	t.Helper()
	goProfile := indexAdmissionTestGoProfile()
	profile, err := GoIndexAdmissionArtifactProfile(goProfile)
	if err != nil {
		t.Fatalf("GoIndexAdmissionArtifactProfile() error = %v", err)
	}
	artifact, err := NewIndexAdmissionArtifactFromGo(sourceID, profile, source, ExtractGo(source, goProfile))
	if err != nil {
		t.Fatalf("NewIndexAdmissionArtifactFromGo() error = %v", err)
	}
	return artifact
}

func indexAdmissionTestGoProfile() GoExtractionProfile {
	return GoExtractionProfile{ProfileKey: "admission-test", ParserKey: "go-parser-test"}
}

func indexAdmissionTestGoSourceA() string {
	return "package source\n\nimport \"fmt\"\n\nfunc Run() {\n\tfmt.Println(\"ok\")\n}\n"
}

func (frame IndexAdmissionFrame) CanonicalMust(t *testing.T) IndexAdmissionFrame {
	t.Helper()
	canonical, err := frame.Canonicalize()
	if err != nil {
		t.Fatalf("Canonicalize() error = %v", err)
	}
	return canonical
}

func TestIndexAdmissionTreeSitterArtifactIsSourceScopedAndFactBound(t *testing.T) {
	t.Parallel()
	source := []byte("import { shared as localShared } from \"./shared.js\";\n" +
		"export { shared as publicShared } from \"./shared.js\";\n" +
		"export function run() {\n" +
		"\treturn localShared();\n" +
		"}\n")
	profile, err := TreeSitterIndexAdmissionArtifactProfile(TreeSitterLanguageJavaScript, "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")
	if err != nil {
		t.Fatalf("TreeSitterIndexAdmissionArtifactProfile() error = %v", err)
	}
	extracted := indexAdmissionTestTreeSitterArtifact(t, source)
	artifact, err := NewIndexAdmissionArtifactFromTreeSitter(indexAdmissionTestSourceA, profile, source, extracted)
	if err != nil {
		t.Fatalf("NewIndexAdmissionArtifactFromTreeSitter() error = %v", err)
	}
	same, err := NewIndexAdmissionArtifactFromTreeSitter(indexAdmissionTestSourceA, profile, append([]byte(nil), source...), extracted)
	if err != nil {
		t.Fatalf("same-source artifact error = %v", err)
	}
	otherSource, err := NewIndexAdmissionArtifactFromTreeSitter(indexAdmissionTestSourceB, profile, source, extracted)
	if err != nil {
		t.Fatalf("other-source artifact error = %v", err)
	}
	if artifact.ArtifactID != same.ArtifactID {
		t.Fatalf("same Source/content/profile ArtifactID differs: %q != %q", artifact.ArtifactID, same.ArtifactID)
	}
	if artifact.ArtifactID == otherSource.ArtifactID {
		t.Fatalf("different Sources reused Tree-sitter ArtifactID %q", artifact.ArtifactID)
	}
	if artifact.ContentDigest != indexAdmissionDigestBytes(source) || artifact.FactsDigest == "" || artifact.Profile != profile {
		t.Fatalf("Tree-sitter admission artifact lost source/profile digest evidence: %#v", artifact)
	}
	if artifact.Status != IndexAdmissionArtifactComplete || len(artifact.Definitions) != 1 || len(artifact.Chunks) != 1 {
		t.Fatalf("Tree-sitter admission artifact facts = %#v", artifact)
	}
	if artifact.Chunks[0].Text != string(source) || artifact.Chunks[0].ContentDigest != indexAdmissionDigestBytes(source) {
		t.Fatalf("Tree-sitter source chunk does not bind exact bytes: %#v", artifact.Chunks[0])
	}
	if definition := artifact.Definitions[0]; definition.LocalSymbolKey != "function:run" || definition.SymbolKey != "javascript:function:run" || definition.Span != indexAdmissionTestTreeSitterSpan(t, source, "export function run() {\n\treturn localShared();\n}", 0) {
		t.Fatalf("Tree-sitter definition = %#v", definition)
	}
	wantReferences := map[string]struct {
		relation IndexRelation
		raw      string
	}{
		"import:./shared.js#shared:localShared":    {relation: IndexRelation("imports"), raw: "shared as localShared"},
		"reexport:./shared.js#shared:publicShared": {relation: IndexRelation("exports"), raw: "shared as publicShared"},
		"call:localShared":                         {relation: IndexRelation("calls"), raw: "localShared()"},
	}
	if len(artifact.References) != len(wantReferences) {
		t.Fatalf("Tree-sitter references = %#v", artifact.References)
	}
	for _, reference := range artifact.References {
		want, found := wantReferences[reference.SiteKey]
		if !found || reference.Relation != want.relation || reference.RawTarget != want.raw {
			t.Fatalf("Tree-sitter reference = %#v, want relation/raw %#v", reference, want)
		}
	}

	artifactID := artifact.ArtifactID
	frame := IndexAdmissionFrame{
		Version:   IndexAdmissionFrameVersion,
		Profile:   IndexAdmissionProfile{ID: indexAdmissionTestProfile},
		Artifacts: []IndexAdmissionArtifact{artifact},
		Memberships: []IndexAdmissionMembership{{
			PathKey:     "source.js",
			DisplayPath: "source.js",
			Mode:        "100644",
			State:       IndexAdmissionMembershipPresent,
			ArtifactID:  &artifactID,
		}},
	}
	if err := ValidateIndexAdmissionFrame(frame); err != nil {
		t.Fatalf("ValidateIndexAdmissionFrame() error = %v", err)
	}
	part, err := frame.PublicationPart()
	if err != nil {
		t.Fatalf("PublicationPart() error = %v", err)
	}
	if len(part.EdgeReplacements) != 0 {
		t.Fatalf("grammar-only Tree-sitter references fabricated edges: %#v", part.EdgeReplacements)
	}

	partialInput := extracted
	partialInput.Coverage = IndexCoveragePartial
	partialInput.Diagnostics = []TreeSitterDiagnostic{{Code: "PARSE_ERROR", Message: "source could not be parsed completely"}}
	partial, err := NewIndexAdmissionArtifactFromTreeSitter(indexAdmissionTestSourceA, profile, source, partialInput)
	if err != nil {
		t.Fatalf("partial Tree-sitter artifact error = %v", err)
	}
	if partial.Status != IndexAdmissionArtifactPartial || !indexAdmissionTestArtifactHasDiagnostic(partial, "PARSE_ERROR") || !indexAdmissionTestArtifactHasDiagnostic(partial, "TREE_SITTER_PARTIAL_COVERAGE") {
		t.Fatalf("partial Tree-sitter artifact did not preserve explicit diagnostics: %#v", partial)
	}

	unavailable := extracted
	unavailable.Coverage = IndexCoverageUnavailable
	if _, err := NewIndexAdmissionArtifactFromTreeSitter(indexAdmissionTestSourceA, profile, source, unavailable); err == nil {
		t.Fatal("NewIndexAdmissionArtifactFromTreeSitter() accepted unavailable parser coverage")
	}
	resolved := extracted
	resolved.References = append([]TreeSitterReferenceSite(nil), extracted.References...)
	resolved.References[0].Resolution = IndexResolutionState("resolved")
	resolved.References[0].TargetKey = "fabricated-target"
	if _, err := NewIndexAdmissionArtifactFromTreeSitter(indexAdmissionTestSourceA, profile, source, resolved); err == nil {
		t.Fatal("NewIndexAdmissionArtifactFromTreeSitter() accepted a fabricated resolved syntax reference")
	}
}

func indexAdmissionTestTreeSitterArtifact(t *testing.T, source []byte) TreeSitterArtifact {
	t.Helper()
	definitionSpan := indexAdmissionTestTreeSitterSpan(t, source, "export function run() {\n\treturn localShared();\n}", 0)
	importSpan := indexAdmissionTestTreeSitterSpan(t, source, "shared as localShared", 0)
	reexportSpan := indexAdmissionTestTreeSitterSpan(t, source, "shared as publicShared", 0)
	callSpan := indexAdmissionTestTreeSitterSpan(t, source, "localShared()", 0)
	chunkSpan, valid := goSpanFromOffsets(goLineStarts(source), len(source), 0, len(source))
	if !valid {
		t.Fatal("failed to construct full Tree-sitter chunk span")
	}
	return TreeSitterArtifact{
		Proof: IndexArtifactProof{
			ArtifactID:         "88888888-8888-4888-8888-888888888888",
			ContentDigest:      indexAdmissionDigestBytes(source),
			FactsDigest:        indexAdmissionDigestBytes([]byte("tree-sitter-test-facts")),
			DefinitionCount:    1,
			ReferenceSiteCount: 3,
			ChunkCount:         1,
		},
		Coverage:     IndexCoverageComplete,
		Language:     TreeSitterLanguageJavaScript,
		BundleDigest: "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		Text:         string(source),
		Definitions: []TreeSitterDefinition{{
			Kind:      "function",
			SymbolKey: "javascript:function:run",
			LocalKey:  "function:run",
			Span:      definitionSpan,
		}},
		References: []TreeSitterReferenceSite{
			{
				Kind:       "import_alias",
				SymbolKey:  "javascript:import:./shared.js#shared:localShared",
				LocalKey:   "import:./shared.js#shared:localShared",
				RawTarget:  "./shared.js#shared",
				Resolution: TreeSitterResolutionSyntaxOnly,
				Span:       importSpan,
			},
			{
				Kind:       "reexport_alias",
				SymbolKey:  "javascript:reexport:./shared.js#shared:publicShared",
				LocalKey:   "reexport:./shared.js#shared:publicShared",
				RawTarget:  "./shared.js#shared",
				Resolution: TreeSitterResolutionPartial,
				Span:       reexportSpan,
			},
			{
				Kind:          "call",
				SymbolKey:     "javascript:call:localShared",
				LocalKey:      "call:localShared",
				OwnerLocalKey: "function:run",
				RawTarget:     "localShared",
				Resolution:    TreeSitterResolutionUnresolved,
				Span:          callSpan,
			},
		},
		Chunks: []TreeSitterChunk{{
			Span:          chunkSpan,
			Text:          string(source),
			ContentDigest: indexAdmissionDigestBytes(source),
		}},
	}
}

func indexAdmissionTestTreeSitterSpan(t *testing.T, source []byte, fragment string, occurrence int) IndexSpan {
	t.Helper()
	start := 0
	found := -1
	for range occurrence + 1 {
		offset := bytes.Index(source[start:], []byte(fragment))
		if offset < 0 {
			t.Fatalf("fragment %q occurrence %d is absent", fragment, occurrence)
		}
		found = start + offset
		start = found + len(fragment)
	}
	span, valid := goSpanFromOffsets(goLineStarts(source), len(source), found, found+len(fragment))
	if !valid {
		t.Fatalf("fragment %q has invalid span", fragment)
	}
	return span
}

func indexAdmissionTestArtifactHasDiagnostic(artifact IndexAdmissionArtifact, code string) bool {
	for _, diagnostic := range artifact.Diagnostics {
		if diagnostic.Code == code {
			return true
		}
	}
	return false
}
