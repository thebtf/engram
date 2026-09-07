package gorm

import (
	"context"
	"testing"

	ucidomain "github.com/thebtf/engram/internal/uci"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func TestUCIIndexAdmissionPinsArtifactIDAndRejectsTupleCollision(t *testing.T) {
	fixture := openUCIPublicationFixture(t)
	ctx := context.Background()
	body := []byte("package admission\n\nfunc StableArtifact() {}\n")

	blob, err := fixture.projection.UpsertBlob(ctx, UpsertUCIBlobInput{
		SourceID:         fixture.source.SourceID,
		ProtectionDomain: "source-private",
		ContentDigest:    uciPublicationDigestBytes(body),
		ByteLength:       int64(len(body)),
		SafeContent:      body,
		Encoding:         "utf-8",
		StorageState:     UCIBlobStored,
	})
	require.NoError(t, err)

	input := UpsertUCIParseArtifactInput{
		ArtifactID:              uuid.NewString(),
		SourceID:                fixture.source.SourceID,
		BlobID:                  blob.BlobID,
		Language:                "go",
		ParserRevision:          "admission-parser-v1",
		GrammarDigest:           uciPublicationDigest("admission-grammar-v1"),
		ExtractionProfileDigest: fixture.profile.ParserBundleDigest,
		Status:                  UCIParseArtifactComplete,
		Diagnostics:             `{}`,
	}
	artifact, err := fixture.projection.UpsertParseArtifact(ctx, input)
	require.NoError(t, err)
	require.Equal(t, input.ArtifactID, artifact.ArtifactID)

	retry, err := fixture.projection.UpsertParseArtifact(ctx, input)
	require.NoError(t, err)
	require.Equal(t, artifact.ArtifactID, retry.ArtifactID)

	collision := input
	collision.ArtifactID = uuid.NewString()
	_, err = fixture.projection.UpsertParseArtifact(ctx, collision)
	require.ErrorIs(t, err, errUCIProjectionImmutable)
}

func TestUCIIndexAdmissionLoadsBuildOwnerFromExactFence(t *testing.T) {
	fixture := openUCIPublicationFixture(t)
	caller := fixture.caller("admission-runtime")
	begin := fixture.begin(
		t,
		fixture.publisher,
		caller,
		"admission-runtime-build",
		fixture.checkout,
		fixture.profile.ProfileID,
		nil,
		ucidomain.IndexManifestFull,
		ucidomain.IndexJobInitial,
	)

	loaded, err := fixture.projection.LoadIndexBuildRef(
		context.Background(),
		begin.Build.Scope,
		fixture.profile.ProfileID,
		begin.Build.BuildID,
		begin.Build.LeaseEpoch,
	)
	require.NoError(t, err)
	require.Equal(t, begin.Build, loaded)

	wrongScope := begin.Build.Scope
	wrongScope.IncarnationID = uuid.NewString()
	_, err = fixture.projection.LoadIndexBuildRef(
		context.Background(),
		wrongScope,
		fixture.profile.ProfileID,
		begin.Build.BuildID,
		begin.Build.LeaseEpoch,
	)
	require.Error(t, err)

	_, err = fixture.projection.LoadIndexBuildRef(
		context.Background(),
		begin.Build.Scope,
		fixture.profile.ProfileID,
		begin.Build.BuildID,
		begin.Build.LeaseEpoch+1,
	)
	require.Error(t, err)
}

func TestUCIIndexAdmissionPinsReferenceSiteIDAndRejectsPrimaryKeyCollision(t *testing.T) {
	fixture := openUCIPublicationFixture(t)
	ctx := context.Background()
	first := fixture.insertArtifact(t, fixture.source.SourceID, "admission-reference-first", "func First() {}\n", UCIParseArtifactComplete)
	input := UpsertUCIReferenceSiteInput{
		ReferenceSiteID: uuid.NewString(),
		ArtifactID:      first.Artifact.ArtifactID,
		SiteKey:         "deterministic-reference",
		RawTarget:       "First",
		Relation:        "calls",
		SyntaxSpan:      `{"byte_start":0,"byte_end":1,"line_start":1,"line_end":1}`,
		ResolverHints:   `{"kind":"call"}`,
	}
	stored, err := fixture.projection.UpsertReferenceSite(ctx, input)
	require.NoError(t, err)
	require.Equal(t, input.ReferenceSiteID, stored.ReferenceSiteID)

	retry, err := fixture.projection.UpsertReferenceSite(ctx, input)
	require.NoError(t, err)
	require.Equal(t, stored.ReferenceSiteID, retry.ReferenceSiteID)

	tupleCollision := input
	tupleCollision.ReferenceSiteID = uuid.NewString()
	_, err = fixture.projection.UpsertReferenceSite(ctx, tupleCollision)
	require.ErrorIs(t, err, errUCIProjectionImmutable)

	second := fixture.insertArtifact(t, fixture.source.SourceID, "admission-reference-second", "func Second() {}\n", UCIParseArtifactComplete)
	primaryKeyCollision := input
	primaryKeyCollision.ArtifactID = second.Artifact.ArtifactID
	primaryKeyCollision.SiteKey = "other-reference"
	primaryKeyCollision.RawTarget = "Second"
	_, err = fixture.projection.UpsertReferenceSite(ctx, primaryKeyCollision)
	require.ErrorIs(t, err, errUCIProjectionImmutable)
}

func TestUCIReferenceSitePreservesMultilineRawTarget(t *testing.T) {
	fixture := openUCIPublicationFixture(t)
	storedArtifact := fixture.insertArtifact(t, fixture.source.SourceID, "admission-multiline-reference", "func MultilineReference() {}\n", UCIParseArtifactComplete)
	rawTarget := "source\n  .map"
	reference, err := fixture.projection.UpsertReferenceSite(context.Background(), UpsertUCIReferenceSiteInput{
		ReferenceSiteID: uuid.NewString(),
		ArtifactID:      storedArtifact.Artifact.ArtifactID,
		SiteKey:         "multiline-call-target",
		RawTarget:       rawTarget,
		Relation:        "calls",
		SyntaxSpan:      `{"byte_start":15,"byte_end":28,"line_start":1,"line_end":2}`,
		ResolverHints:   `{"kind":"call"}`,
	})
	require.NoError(t, err)
	require.Equal(t, rawTarget, reference.RawTarget)
}

func TestUCIIndexAdmissionRejectsArtifactPrimaryKeyCollision(t *testing.T) {
	fixture := openUCIPublicationFixture(t)
	ctx := context.Background()
	firstBody := []byte("package admission\n\nfunc First() {}\n")
	firstBlob, err := fixture.projection.UpsertBlob(ctx, UpsertUCIBlobInput{
		SourceID:         fixture.source.SourceID,
		ProtectionDomain: uciIndexAdmissionProtectionDomain,
		ContentDigest:    uciPublicationDigestBytes(firstBody),
		ByteLength:       int64(len(firstBody)),
		SafeContent:      firstBody,
		Encoding:         "utf-8",
		StorageState:     UCIBlobStored,
	})
	require.NoError(t, err)
	first := UpsertUCIParseArtifactInput{
		ArtifactID:              uuid.NewString(),
		SourceID:                fixture.source.SourceID,
		BlobID:                  firstBlob.BlobID,
		Language:                "go",
		ParserRevision:          "admission-primary-first",
		GrammarDigest:           uciPublicationDigest("admission-primary-first"),
		ExtractionProfileDigest: fixture.profile.ParserBundleDigest,
		Status:                  UCIParseArtifactComplete,
		Diagnostics:             `{}`,
	}
	_, err = fixture.projection.UpsertParseArtifact(ctx, first)
	require.NoError(t, err)

	secondBody := []byte("package admission\n\nfunc Second() {}\n")
	secondBlob, err := fixture.projection.UpsertBlob(ctx, UpsertUCIBlobInput{
		SourceID:         fixture.source.SourceID,
		ProtectionDomain: uciIndexAdmissionProtectionDomain,
		ContentDigest:    uciPublicationDigestBytes(secondBody),
		ByteLength:       int64(len(secondBody)),
		SafeContent:      secondBody,
		Encoding:         "utf-8",
		StorageState:     UCIBlobStored,
	})
	require.NoError(t, err)
	primaryKeyCollision := first
	primaryKeyCollision.BlobID = secondBlob.BlobID
	primaryKeyCollision.ParserRevision = "admission-primary-second"
	primaryKeyCollision.GrammarDigest = uciPublicationDigest("admission-primary-second")
	_, err = fixture.projection.UpsertParseArtifact(ctx, primaryKeyCollision)
	require.ErrorIs(t, err, errUCIProjectionImmutable)
}

func TestUCIIndexAdmissionDefinitionNameIsLanguageAwareAndClosed(t *testing.T) {
	goTests := []struct {
		definition ucidomain.IndexAdmissionDefinition
		name       string
		valid      bool
	}{
		{definition: ucidomain.IndexAdmissionDefinition{LocalSymbolKey: "pkg:sample", Kind: "package"}, name: "sample", valid: true},
		{definition: ucidomain.IndexAdmissionDefinition{LocalSymbolKey: "type:Worker", Kind: "type"}, name: "Worker", valid: true},
		{definition: ucidomain.IndexAdmissionDefinition{LocalSymbolKey: "func:SharedTarget", Kind: "function"}, name: "SharedTarget", valid: true},
		{definition: ucidomain.IndexAdmissionDefinition{LocalSymbolKey: "method:remote.Worker.Run", Kind: "method"}, name: "Run", valid: true},
		{definition: ucidomain.IndexAdmissionDefinition{LocalSymbolKey: "func:not-a-name", Kind: "function"}, valid: false},
		{definition: ucidomain.IndexAdmissionDefinition{LocalSymbolKey: "method:Worker.", Kind: "method"}, valid: false},
		{definition: ucidomain.IndexAdmissionDefinition{LocalSymbolKey: "func:WrongKind", Kind: "method"}, valid: false},
	}
	for _, test := range goTests {
		name, err := uciIndexAdmissionDefinitionName(ucidomain.IndexAdmissionLanguageGo, test.definition)
		if test.valid {
			require.NoError(t, err)
			require.Equal(t, test.name, name)
			continue
		}
		require.Error(t, err)
	}

	for _, language := range []ucidomain.IndexAdmissionLanguage{
		ucidomain.IndexAdmissionLanguageJavaScript,
		ucidomain.IndexAdmissionLanguageTypeScript,
		ucidomain.IndexAdmissionLanguageTSX,
	} {
		name, err := uciIndexAdmissionDefinitionName(language, ucidomain.IndexAdmissionDefinition{
			LocalSymbolKey: "function:InstalledParserCanary",
			Kind:           "function",
		})
		require.NoError(t, err)
		require.Equal(t, "InstalledParserCanary", name)
	}

	for _, kind := range []string{"function", "method", "class", "interface", "type", "enum", "namespace", "const", "let", "var"} {
		name, err := uciIndexAdmissionDefinitionName(ucidomain.IndexAdmissionLanguageTypeScript, ucidomain.IndexAdmissionDefinition{
			LocalSymbolKey: kind + ":Outer.InstalledParserCanary",
			Kind:           kind,
		})
		require.NoError(t, err)
		require.Equal(t, "InstalledParserCanary", name)
	}

	for _, definition := range []ucidomain.IndexAdmissionDefinition{
		{LocalSymbolKey: "class:InstalledParserCanary", Kind: "function"},
		{LocalSymbolKey: "function:", Kind: "function"},
		{LocalSymbolKey: "function:Outer.", Kind: "function"},
		{LocalSymbolKey: "function:Outer..InstalledParserCanary", Kind: "function"},
		{LocalSymbolKey: "function:Outer.\x00InstalledParserCanary", Kind: "function"},
	} {
		_, err := uciIndexAdmissionDefinitionName(ucidomain.IndexAdmissionLanguageTypeScript, definition)
		require.Error(t, err)
	}
	structuredTests := []struct {
		name       string
		language   ucidomain.IndexAdmissionLanguage
		definition ucidomain.IndexAdmissionDefinition
		want       string
		valid      bool
	}{
		{name: "markdown heading", language: ucidomain.IndexAdmissionLanguageMarkdown, definition: ucidomain.IndexAdmissionDefinition{LocalSymbolKey: "heading:introduction-2", SymbolKey: "markdown:heading:introduction-2", Kind: "heading"}, want: "introduction-2", valid: true},
		{name: "markdown symbol prefix mismatch", language: ucidomain.IndexAdmissionLanguageMarkdown, definition: ucidomain.IndexAdmissionDefinition{LocalSymbolKey: "heading:introduction", SymbolKey: "json:heading:introduction", Kind: "heading"}},
		{name: "json document", language: ucidomain.IndexAdmissionLanguageJSON, definition: ucidomain.IndexAdmissionDefinition{LocalSymbolKey: "json:document:0", SymbolKey: "json:document:0", Kind: "document"}, want: "document 0", valid: true},
		{name: "json decoded pointer tail", language: ucidomain.IndexAdmissionLanguageJSON, definition: ucidomain.IndexAdmissionDefinition{LocalSymbolKey: "json:document:0#/items/~1primary", SymbolKey: "json:document:0#/items/~1primary", Kind: "key"}, want: "/primary", valid: true},
		{name: "json malformed pointer escape", language: ucidomain.IndexAdmissionLanguageJSON, definition: ucidomain.IndexAdmissionDefinition{LocalSymbolKey: "json:document:0#/items/~2primary", SymbolKey: "json:document:0#/items/~2primary", Kind: "key"}},
		{name: "yaml anchor", language: ucidomain.IndexAdmissionLanguageYAML, definition: ucidomain.IndexAdmissionDefinition{LocalSymbolKey: "yaml:document:1#anchor:shared~1values", SymbolKey: "yaml:document:1#anchor:shared~1values", Kind: "anchor"}, want: "shared/values", valid: true},
		{name: "yaml wrong language prefix", language: ucidomain.IndexAdmissionLanguageYAML, definition: ucidomain.IndexAdmissionDefinition{LocalSymbolKey: "json:document:1#/service", SymbolKey: "json:document:1#/service", Kind: "key"}},
		{name: "sql table", language: ucidomain.IndexAdmissionLanguageSQL, definition: ucidomain.IndexAdmissionDefinition{LocalSymbolKey: "table:public.users", SymbolKey: "sql:table:public.users", Kind: "table"}, want: "public.users", valid: true},
		{name: "sql column", language: ucidomain.IndexAdmissionLanguageSQL, definition: ucidomain.IndexAdmissionDefinition{LocalSymbolKey: "column:public.users.email", SymbolKey: "sql:column:public.users.email", Kind: "column"}, want: "public.users.email", valid: true},
		{name: "sql constraint", language: ucidomain.IndexAdmissionLanguageSQL, definition: ucidomain.IndexAdmissionDefinition{LocalSymbolKey: "constraint:public.users:primary_key:1", SymbolKey: "sql:constraint:public.users:primary_key:1", Kind: "primary_key"}, want: "public.users primary_key #1", valid: true},
		{name: "sql malformed constraint ordinal", language: ucidomain.IndexAdmissionLanguageSQL, definition: ucidomain.IndexAdmissionDefinition{LocalSymbolKey: "constraint:public.users:unique:0", SymbolKey: "sql:constraint:public.users:unique:0", Kind: "unique"}},
		{name: "openapi version", language: ucidomain.IndexAdmissionLanguageOpenAPI, definition: ucidomain.IndexAdmissionDefinition{LocalSymbolKey: "version:3.0.3", SymbolKey: "openapi:version:3.0.3", Kind: "version"}, want: "3.0.3", valid: true},
		{name: "openapi info", language: ucidomain.IndexAdmissionLanguageOpenAPI, definition: ucidomain.IndexAdmissionDefinition{LocalSymbolKey: "info", SymbolKey: "openapi:info", Kind: "info"}, want: "info", valid: true},
		{name: "openapi path", language: ucidomain.IndexAdmissionLanguageOpenAPI, definition: ucidomain.IndexAdmissionDefinition{LocalSymbolKey: "path:/pets", SymbolKey: "openapi:path:/pets", Kind: "path"}, want: "/pets", valid: true},
		{name: "openapi operation", language: ucidomain.IndexAdmissionLanguageOpenAPI, definition: ucidomain.IndexAdmissionDefinition{LocalSymbolKey: "operation:get:/pets", SymbolKey: "openapi:operation:get:/pets", Kind: "operation"}, want: "get /pets", valid: true},
		{name: "openapi parameter", language: ucidomain.IndexAdmissionLanguageOpenAPI, definition: ucidomain.IndexAdmissionDefinition{LocalSymbolKey: "parameter:#/paths/~1pets/get/parameters/0", SymbolKey: "openapi:parameter:#/paths/~1pets/get/parameters/0", Kind: "parameter"}, want: "#/paths/~1pets/get/parameters/0", valid: true},
		{name: "openapi schema", language: ucidomain.IndexAdmissionLanguageOpenAPI, definition: ucidomain.IndexAdmissionDefinition{LocalSymbolKey: "schema:Pet", SymbolKey: "openapi:schema:Pet", Kind: "schema"}, want: "Pet", valid: true},
		{name: "openapi wrong operation method", language: ucidomain.IndexAdmissionLanguageOpenAPI, definition: ucidomain.IndexAdmissionDefinition{LocalSymbolKey: "operation:fetch:/pets", SymbolKey: "openapi:operation:fetch:/pets", Kind: "operation"}},
		{name: "openapi NUL key", language: ucidomain.IndexAdmissionLanguageOpenAPI, definition: ucidomain.IndexAdmissionDefinition{LocalSymbolKey: "schema:Pet\x00", SymbolKey: "openapi:schema:Pet\x00", Kind: "schema"}},
	}
	for _, test := range structuredTests {
		t.Run(test.name, func(t *testing.T) {
			name, err := uciIndexAdmissionDefinitionName(test.language, test.definition)
			if !test.valid {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			require.Equal(t, test.want, name)
		})
	}

	_, err := uciIndexAdmissionDefinitionName(ucidomain.IndexAdmissionLanguage("unsupported"), ucidomain.IndexAdmissionDefinition{
		LocalSymbolKey: "function:InstalledParserCanary",
		Kind:           "function",
	})
	require.ErrorContains(t, err, "unsupported artifact language")
}

func TestUCIIndexAdmissionFactVerificationIsCollationIndependent(t *testing.T) {
	fixture := openUCIPublicationFixture(t)
	frame := uciIndexAdmissionFixtureFrame(t, fixture, "collation.go", `package collation

func SQLProfileAnchor() {}
func SavedUpdateBoundary() {}
`)

	_, err := fixture.projection.AdmitIndexFrame(context.Background(), fixture.source.SourceID, fixture.profile.ProfileID, frame)
	require.NoError(t, err)
}

func TestUCIIndexAdmissionStoresPinnedTypeScriptDefinitionAndRejectsCrossLanguageKeys(t *testing.T) {
	t.Run("stores source-scoped installed parser definition", func(t *testing.T) {
		fixture := openUCIPublicationFixture(t)
		frame := uciIndexAdmissionTypeScriptFixtureFrame(t, fixture)
		artifact := frame.Artifacts[0]

		parts, err := fixture.projection.AdmitIndexFrame(context.Background(), fixture.source.SourceID, fixture.profile.ProfileID, frame)
		require.NoError(t, err)
		require.Len(t, parts.Artifacts, 1)
		require.Equal(t, artifact.ArtifactID, parts.Artifacts[0].ArtifactID)

		var storedArtifact UCIParseArtifact
		require.NoError(t, fixture.db.Where("source_id = ? AND artifact_id = ?", fixture.source.SourceID, artifact.ArtifactID).First(&storedArtifact).Error)
		require.Equal(t, fixture.source.SourceID, storedArtifact.SourceID)
		require.Equal(t, string(ucidomain.IndexAdmissionLanguageTypeScript), storedArtifact.Language)

		var reloaded UCIDefinition
		require.NoError(t, fixture.db.Where("artifact_id = ? AND local_symbol_key = ?", artifact.ArtifactID, "function:InstalledParserCanary").First(&reloaded).Error)
		require.Equal(t, "InstalledParserCanary", reloaded.Name)
		require.Equal(t, "typescript:function:InstalledParserCanary", reloaded.QualifiedLocalName)

		_, err = fixture.projection.DescribeIndexArtifact(context.Background(), fixture.foreign.SourceID, artifact.ArtifactID)
		require.Error(t, err, "a source-scoped artifact must not be reloaded through a foreign source")
	})

	for _, test := range []struct {
		name           string
		localSymbolKey string
	}{
		{name: "kind prefix mismatch", localSymbolKey: "class:InstalledParserCanary"},
		{name: "empty qualified name", localSymbolKey: "function:"},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := openUCIPublicationFixture(t)
			frame := uciIndexAdmissionTypeScriptFixtureFrame(t, fixture)
			artifact := &frame.Artifacts[0]
			artifact.Definitions[0].LocalSymbolKey = test.localSymbolKey
			factsDigest, err := ucidomain.DigestIndexAdmissionArtifactFacts(*artifact)
			require.NoError(t, err)
			artifact.FactsDigest = factsDigest

			_, err = fixture.projection.AdmitIndexFrame(context.Background(), fixture.source.SourceID, fixture.profile.ProfileID, frame)
			require.Error(t, err)
			var artifacts int64
			require.NoError(t, fixture.db.Model(&UCIParseArtifact{}).Where("artifact_id = ?", artifact.ArtifactID).Count(&artifacts).Error)
			require.Zero(t, artifacts)
		})
	}

	t.Run("unsupported artifact language", func(t *testing.T) {
		fixture := openUCIPublicationFixture(t)
		frame := uciIndexAdmissionTypeScriptFixtureFrame(t, fixture)
		artifact := &frame.Artifacts[0]
		artifact.Profile.Language = ucidomain.IndexAdmissionLanguage("unsupported")

		_, err := fixture.projection.AdmitIndexFrame(context.Background(), fixture.source.SourceID, fixture.profile.ProfileID, frame)
		require.Error(t, err)
		var artifacts int64
		require.NoError(t, fixture.db.Model(&UCIParseArtifact{}).Where("artifact_id = ?", artifact.ArtifactID).Count(&artifacts).Error)
		require.Zero(t, artifacts)
	})
}

func TestUCIIndexAdmissionStoresStructuredMarkdownDefinition(t *testing.T) {
	fixture := openUCIPublicationFixture(t)
	frame := uciIndexAdmissionMarkdownFixtureFrame(t, fixture)
	artifact := frame.Artifacts[0]

	_, err := fixture.projection.AdmitIndexFrame(context.Background(), fixture.source.SourceID, fixture.profile.ProfileID, frame)
	require.NoError(t, err)

	var definition UCIDefinition
	require.NoError(t, fixture.db.Where("artifact_id = ? AND local_symbol_key = ?", artifact.ArtifactID, "heading:install-guide").First(&definition).Error)
	require.Equal(t, "install-guide", definition.Name)
	require.Equal(t, "markdown:heading:install-guide", definition.QualifiedLocalName)
}

func TestUCIIndexAdmissionStoresEmptyJSONKey(t *testing.T) {
	fixture := openUCIPublicationFixture(t)
	body := []byte(`{"": {"value": 1}}`)
	extractionProfile := ucidomain.DefaultJSONYAMLExtractionProfile("uci-admission-json-empty-key", ucidomain.JSONYAMLFormatJSON)
	profile, err := ucidomain.JSONYAMLIndexAdmissionArtifactProfile(extractionProfile)
	require.NoError(t, err)
	profile.ExtractionProfileDigest = ucidomain.IndexDigest(fixture.profile.ParserBundleDigest)
	artifact, err := ucidomain.NewIndexAdmissionArtifactFromJSONYAML(
		fixture.source.SourceID,
		profile,
		extractionProfile,
		body,
		ucidomain.ExtractJSONYAML(body, extractionProfile),
	)
	require.NoError(t, err)
	artifactID := artifact.ArtifactID
	frame := ucidomain.IndexAdmissionFrame{
		Version:   ucidomain.IndexAdmissionFrameVersion,
		Profile:   ucidomain.IndexAdmissionProfile{ID: fixture.profile.ProfileID},
		Artifacts: []ucidomain.IndexAdmissionArtifact{artifact},
		Memberships: []ucidomain.IndexAdmissionMembership{{
			PathKey:     "empty-key.json",
			DisplayPath: "empty-key.json",
			Mode:        "100644",
			State:       ucidomain.IndexAdmissionMembershipPresent,
			ArtifactID:  &artifactID,
		}},
	}

	_, err = fixture.projection.AdmitIndexFrame(context.Background(), fixture.source.SourceID, fixture.profile.ProfileID, frame)
	require.NoError(t, err)
	var definition UCIDefinition
	require.NoError(t, fixture.db.Where("artifact_id = ? AND local_symbol_key = ?", artifactID, "json:document:0#/").First(&definition).Error)
	require.Equal(t, "(empty)", definition.Name)
	require.Equal(t, "json:document:0#/", definition.QualifiedLocalName)
}

func TestUCIIndexAdmissionPackedCrossFrameSealsAndReplays(t *testing.T) {
	fixture := openUCIPublicationFixture(t)
	ctx := context.Background()
	sourceFrame := uciIndexAdmissionFixtureFrame(t, fixture, "source.go", "package source\n\nfunc Caller() {\n\tTarget()\n}\n")
	targetFrame := uciIndexAdmissionFixtureFrame(t, fixture, "target.go", "package target\n\nfunc Target() {}\n")
	source := sourceFrame.Artifacts[0]
	target := targetFrame.Artifacts[0]
	reference := uciIndexAdmissionFixtureReference(t, source, "Target")
	require.NotNil(t, reference.OwnerSymbolKey)
	require.Equal(t, "func:Caller", *reference.OwnerSymbolKey)
	sourceSymbol := *reference.OwnerSymbolKey
	targetSymbol := "func:Target"
	sourceFrame.EdgeReplacements = []ucidomain.IndexAdmissionEdgeReplacement{{
		SourcePath: "source.go",
		Edges: []ucidomain.IndexAdmissionEdge{{
			EdgeKey:          "source-to-target",
			SourceArtifactID: source.ArtifactID,
			SourceSymbolKey:  &sourceSymbol,
			Target: &ucidomain.IndexAdmissionEdgeTarget{
				PathKey:    "target.go",
				ArtifactID: target.ArtifactID,
				SymbolKey:  &targetSymbol,
			},
			Relation:         reference.Relation,
			EvidenceKind:     ucidomain.IndexEvidenceKind("extracted"),
			ResolutionState:  ucidomain.IndexResolutionState("resolved"),
			ResolverRevision: "admission-resolver/v1",
			Evidence: ucidomain.IndexAdmissionEdgeEvidence{
				ReferenceSiteKey: reference.SiteKey,
				Span:             reference.Span,
				RuleKey:          "admission-source-fact",
				Explanation:      "cross-frame target validated before staging",
			},
		}},
	}}
	targetFrame.EdgeReplacements = []ucidomain.IndexAdmissionEdgeReplacement{{SourcePath: "target.go"}}

	parts, err := fixture.projection.AdmitIndexFrames(ctx, fixture.source.SourceID, fixture.profile.ProfileID, []ucidomain.IndexAdmissionFrame{sourceFrame, targetFrame})
	require.NoError(t, err)
	require.Len(t, parts, 2)
	expectedReferenceID, err := ucidomain.DeriveIndexAdmissionReferenceSiteID(source.ArtifactID, reference.SiteKey)
	require.NoError(t, err)
	require.Equal(t, expectedReferenceID, *parts[0].EdgeReplacements[0].Edges[0].Evidence.ReferenceSiteID)

	var persistedReference UCIReferenceSite
	require.NoError(t, fixture.db.Where("reference_site_id = ?", expectedReferenceID).First(&persistedReference).Error)
	require.NotNil(t, persistedReference.OwnerSymbolKey)
	require.Equal(t, sourceSymbol, *persistedReference.OwnerSymbolKey)
	require.Equal(t, source.ArtifactID, persistedReference.ArtifactID)

	memberships := append(append([]ucidomain.IndexMembership(nil), parts[0].Memberships...), parts[1].Memberships...)
	replacements := append(append([]ucidomain.IndexEdgeReplacement(nil), parts[0].EdgeReplacements...), parts[1].EdgeReplacements...)
	draft := newUCIPublicationDraft(parts, memberships, replacements)
	caller := fixture.caller("admission-packed")
	begin := fixture.begin(t, fixture.publisher, caller, "admission-packed", fixture.checkout, fixture.profile.ProfileID, nil, ucidomain.IndexManifestFull, ucidomain.IndexJobInitial)
	acks := fixture.stageDraft(t, fixture.publisher, caller, begin.Build, parts)
	published := fixture.finalizeDraft(t, fixture.publisher, caller, begin.Build, nil, acks, draft)
	require.Equal(t, int64(1), published.Context.Generation)

	var persistedEdge UCIResolvedEdge
	require.NoError(t, fixture.db.Where("checkout_id = ? AND source_path = ? AND edge_key = ? AND valid_to_generation IS NULL", fixture.checkout.CheckoutID, "source.go", "source-to-target").First(&persistedEdge).Error)
	require.Equal(t, source.ArtifactID, persistedEdge.SourceArtifact)
	require.NotNil(t, persistedEdge.SourceSymbol)
	require.Equal(t, sourceSymbol, *persistedEdge.SourceSymbol)
	require.NotNil(t, persistedEdge.TargetPath)
	require.Equal(t, "target.go", *persistedEdge.TargetPath)
	require.NotNil(t, persistedEdge.TargetArtifact)
	require.Equal(t, target.ArtifactID, *persistedEdge.TargetArtifact)
	require.NotNil(t, persistedEdge.TargetSymbol)
	require.Equal(t, targetSymbol, *persistedEdge.TargetSymbol)
	authorized := uciSemanticAuthorize(t, fixture, published.Context)
	exactName, err := fixture.projection.SelectCandidates(ctx, authorized, ucidomain.QuerySpec{
		Mode:  ucidomain.QueryModeExactLocalName,
		Text:  "Target",
		Order: ucidomain.QueryOrderPath,
		Limit: 2,
	})
	require.NoError(t, err)
	require.Len(t, exactName.Candidates, 1)
	require.Equal(t, target.ArtifactID, exactName.Candidates[0].Proof.ArtifactID)
	require.Equal(t, "Target", exactName.Candidates[0].LocalName)
	localKey, err := fixture.projection.SelectCandidates(ctx, authorized, ucidomain.QuerySpec{
		Mode:  ucidomain.QueryModeExactLocalName,
		Text:  "func:Target",
		Order: ucidomain.QueryOrderPath,
		Limit: 2,
	})
	require.NoError(t, err)
	require.Empty(t, localKey.Candidates)

	var sealed UCIParseArtifact
	require.NoError(t, fixture.db.Where("artifact_id = ?", source.ArtifactID).First(&sealed).Error)
	require.NotNil(t, sealed.SealedAt)
	require.NotNil(t, sealed.FactsDigest)

	replayed, err := fixture.projection.AdmitIndexFrames(ctx, fixture.source.SourceID, fixture.profile.ProfileID, []ucidomain.IndexAdmissionFrame{sourceFrame, targetFrame})
	require.NoError(t, err)
	require.Equal(t, parts, replayed)
	var replayedArtifact UCIParseArtifact
	require.NoError(t, fixture.db.Where("artifact_id = ?", source.ArtifactID).First(&replayedArtifact).Error)
	require.Equal(t, sealed.SealedAt, replayedArtifact.SealedAt)
	require.Equal(t, sealed.FactsDigest, replayedArtifact.FactsDigest)
}

func TestUCIIndexAdmissionRejectsBeforeWriteAndRollsBack(t *testing.T) {
	t.Run("wrong source and profile", func(t *testing.T) {
		fixture := openUCIPublicationFixture(t)
		frame := uciIndexAdmissionFixtureFrame(t, fixture, "source.go", "package source\n\nfunc Source() {}\n")
		_, err := fixture.projection.AdmitIndexFrame(context.Background(), fixture.foreign.SourceID, fixture.profile.ProfileID, frame)
		require.Error(t, err)
		frame.Profile.ID = uuid.NewString()
		_, err = fixture.projection.AdmitIndexFrame(context.Background(), fixture.source.SourceID, fixture.profile.ProfileID, frame)
		require.Error(t, err)
		var artifacts int64
		require.NoError(t, fixture.db.Model(&UCIParseArtifact{}).Where("artifact_id = ?", frame.Artifacts[0].ArtifactID).Count(&artifacts).Error)
		require.Zero(t, artifacts)
	})

	t.Run("unparseable Go definition key", func(t *testing.T) {
		fixture := openUCIPublicationFixture(t)
		frame := uciIndexAdmissionFixtureFrame(t, fixture, "broken.go", "package broken\n\nfunc Broken() {}\n")
		mutated := false
		oldSymbolKey := ""
		newSymbolKey := "func:not-a-name"
		for index := range frame.Artifacts[0].Definitions {
			if frame.Artifacts[0].Definitions[index].Kind == "function" {
				oldSymbolKey = frame.Artifacts[0].Definitions[index].LocalSymbolKey
				frame.Artifacts[0].Definitions[index].LocalSymbolKey = newSymbolKey
				mutated = true
				break
			}
		}
		require.True(t, mutated)
		chunkMutated := false
		for index := range frame.Artifacts[0].Chunks {
			chunk := &frame.Artifacts[0].Chunks[index]
			if chunk.SymbolKey != nil && *chunk.SymbolKey == oldSymbolKey {
				chunk.SymbolKey = &newSymbolKey
				chunkMutated = true
			}
		}
		require.True(t, chunkMutated)
		factsDigest, err := ucidomain.DigestIndexAdmissionArtifactFacts(frame.Artifacts[0])
		require.NoError(t, err)
		frame.Artifacts[0].FactsDigest = factsDigest
		_, err = fixture.projection.AdmitIndexFrame(context.Background(), fixture.source.SourceID, fixture.profile.ProfileID, frame)
		require.Error(t, err)
		var artifacts int64
		require.NoError(t, fixture.db.Model(&UCIParseArtifact{}).Where("artifact_id = ?", frame.Artifacts[0].ArtifactID).Count(&artifacts).Error)
		require.Zero(t, artifacts)
	})

	t.Run("cross-frame edge relation mismatch", func(t *testing.T) {
		fixture := openUCIPublicationFixture(t)
		sourceFrame := uciIndexAdmissionFixtureFrame(t, fixture, "source.go", "package source\n\nfunc Caller() {\n\tTarget()\n}\n")
		targetFrame := uciIndexAdmissionFixtureFrame(t, fixture, "target.go", "package target\n\nfunc Target() {}\n")
		source := sourceFrame.Artifacts[0]
		target := targetFrame.Artifacts[0]
		reference := uciIndexAdmissionFixtureReference(t, source, "Target")
		require.NotNil(t, reference.OwnerSymbolKey)
		sourceSymbol := *reference.OwnerSymbolKey
		targetSymbol := "func:Target"
		sourceFrame.EdgeReplacements = []ucidomain.IndexAdmissionEdgeReplacement{{
			SourcePath: "source.go",
			Edges: []ucidomain.IndexAdmissionEdge{{
				EdgeKey:          "source-to-target-wrong-relation",
				SourceArtifactID: source.ArtifactID,
				SourceSymbolKey:  &sourceSymbol,
				Target: &ucidomain.IndexAdmissionEdgeTarget{
					PathKey:    "target.go",
					ArtifactID: target.ArtifactID,
					SymbolKey:  &targetSymbol,
				},
				Relation:         ucidomain.IndexRelation("imports"),
				EvidenceKind:     ucidomain.IndexEvidenceKind("extracted"),
				ResolutionState:  ucidomain.IndexResolutionState("resolved"),
				ResolverRevision: "admission-resolver/v1",
				Evidence: ucidomain.IndexAdmissionEdgeEvidence{
					ReferenceSiteKey: reference.SiteKey,
					Span:             reference.Span,
					RuleKey:          "admission-source-fact",
					Explanation:      "must reject before any admission write",
				},
			}},
		}}

		_, err := fixture.projection.AdmitIndexFrames(context.Background(), fixture.source.SourceID, fixture.profile.ProfileID, []ucidomain.IndexAdmissionFrame{sourceFrame, targetFrame})
		require.ErrorContains(t, err, "edge relation does not match source reference")

		var artifacts, blobs, references int64
		require.NoError(t, fixture.db.Model(&UCIParseArtifact{}).Where("artifact_id IN ?", []string{source.ArtifactID, target.ArtifactID}).Count(&artifacts).Error)
		require.NoError(t, fixture.db.Model(&UCIBlob{}).Where("source_id = ? AND content_digest IN ?", fixture.source.SourceID, []string{string(source.ContentDigest), string(target.ContentDigest)}).Count(&blobs).Error)
		require.NoError(t, fixture.db.Model(&UCIReferenceSite{}).Where("artifact_id IN ?", []string{source.ArtifactID, target.ArtifactID}).Count(&references).Error)
		require.Zero(t, artifacts)
		require.Zero(t, blobs)
		require.Zero(t, references)
	})

	t.Run("fact failure rolls back", func(t *testing.T) {
		fixture := openUCIPublicationFixture(t)
		frame := uciIndexAdmissionFixtureFrame(t, fixture, "rollback.go", "package rollback\n\nfunc Rollback() {}\n")
		installUCIPublicationFault(t, fixture.db, "ci_chunks", "AFTER", "INSERT", "")
		_, err := fixture.projection.AdmitIndexFrame(context.Background(), fixture.source.SourceID, fixture.profile.ProfileID, frame)
		require.Error(t, err)
		var artifacts, blobs, definitions, references, chunks int64
		require.NoError(t, fixture.db.Model(&UCIParseArtifact{}).Where("artifact_id = ?", frame.Artifacts[0].ArtifactID).Count(&artifacts).Error)
		require.NoError(t, fixture.db.Model(&UCIBlob{}).Where("source_id = ? AND content_digest = ?", fixture.source.SourceID, frame.Artifacts[0].ContentDigest).Count(&blobs).Error)
		require.NoError(t, fixture.db.Model(&UCIDefinition{}).Where("artifact_id = ?", frame.Artifacts[0].ArtifactID).Count(&definitions).Error)
		require.NoError(t, fixture.db.Model(&UCIReferenceSite{}).Where("artifact_id = ?", frame.Artifacts[0].ArtifactID).Count(&references).Error)
		require.NoError(t, fixture.db.Model(&UCIChunk{}).Where("artifact_id = ?", frame.Artifacts[0].ArtifactID).Count(&chunks).Error)
		require.Zero(t, artifacts)
		require.Zero(t, blobs)
		require.Zero(t, definitions)
		require.Zero(t, references)
		require.Zero(t, chunks)
	})

	t.Run("cancellation", func(t *testing.T) {
		fixture := openUCIPublicationFixture(t)
		frame := uciIndexAdmissionFixtureFrame(t, fixture, "cancel.go", "package cancellation\n\nfunc Cancelled() {}\n")
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		_, err := fixture.projection.AdmitIndexFrame(ctx, fixture.source.SourceID, fixture.profile.ProfileID, frame)
		require.ErrorIs(t, err, context.Canceled)
		var artifacts int64
		require.NoError(t, fixture.db.Model(&UCIParseArtifact{}).Where("artifact_id = ?", frame.Artifacts[0].ArtifactID).Count(&artifacts).Error)
		require.Zero(t, artifacts)
	})
}

func uciIndexAdmissionFixtureFrame(t *testing.T, fixture *uciPublicationFixture, path, source string) ucidomain.IndexAdmissionFrame {
	t.Helper()
	profile := ucidomain.IndexAdmissionArtifactProfile{
		Language:                ucidomain.IndexAdmissionLanguageGo,
		ParserRevision:          "uci-admission-fixture-parser/v1",
		GrammarDigest:           ucidomain.IndexDigest(uciPublicationDigest("uci-admission-fixture-grammar")),
		ExtractionProfileDigest: ucidomain.IndexDigest(fixture.profile.ParserBundleDigest),
	}
	body := []byte(source)
	artifact, err := ucidomain.NewIndexAdmissionArtifactFromGo(
		fixture.source.SourceID,
		profile,
		body,
		ucidomain.ExtractGo(body, ucidomain.GoExtractionProfile{ProfileKey: "uci-admission-fixture", ParserKey: "go-parser"}),
	)
	require.NoError(t, err)
	artifactID := artifact.ArtifactID
	return ucidomain.IndexAdmissionFrame{
		Version:   ucidomain.IndexAdmissionFrameVersion,
		Profile:   ucidomain.IndexAdmissionProfile{ID: fixture.profile.ProfileID},
		Artifacts: []ucidomain.IndexAdmissionArtifact{artifact},
		Memberships: []ucidomain.IndexAdmissionMembership{{
			PathKey:     path,
			DisplayPath: path,
			Mode:        "100644",
			State:       ucidomain.IndexAdmissionMembershipPresent,
			ArtifactID:  &artifactID,
		}},
	}
}

func uciIndexAdmissionTypeScriptFixtureFrame(t *testing.T, fixture *uciPublicationFixture) ucidomain.IndexAdmissionFrame {
	t.Helper()
	body := []byte("export function InstalledParserCanary(): void {}\n")
	contentDigest := ucidomain.IndexDigest(uciPublicationDigestBytes(body))
	bundleDigest := ucidomain.IndexDigest(fixture.profile.ParserBundleDigest)
	profile, err := ucidomain.TreeSitterIndexAdmissionArtifactProfile(ucidomain.TreeSitterLanguageTypeScript, bundleDigest)
	require.NoError(t, err)
	definitionSpan := ucidomain.IndexSpan{ByteStart: 0, ByteEnd: int64(len(body) - 1), LineStart: 1, LineEnd: 1}
	chunkSpan := ucidomain.IndexSpan{ByteStart: 0, ByteEnd: int64(len(body)), LineStart: 1, LineEnd: 1}
	extracted := ucidomain.TreeSitterArtifact{
		Proof: ucidomain.IndexArtifactProof{
			ArtifactID:         "88888888-8888-4888-8888-888888888888",
			ContentDigest:      contentDigest,
			FactsDigest:        ucidomain.IndexDigest(uciPublicationDigest("uci-admission-typescript-parser-facts")),
			DefinitionCount:    1,
			ReferenceSiteCount: 0,
			ChunkCount:         1,
		},
		Coverage:     ucidomain.IndexCoverageComplete,
		Language:     ucidomain.TreeSitterLanguageTypeScript,
		BundleDigest: bundleDigest,
		Text:         string(body),
		Definitions: []ucidomain.TreeSitterDefinition{{
			Kind:      "function",
			SymbolKey: "typescript:function:InstalledParserCanary",
			LocalKey:  "function:InstalledParserCanary",
			Span:      definitionSpan,
		}},
		References: []ucidomain.TreeSitterReferenceSite{},
		Chunks: []ucidomain.TreeSitterChunk{{
			Span:          chunkSpan,
			Text:          string(body),
			ContentDigest: contentDigest,
		}},
		Diagnostics: []ucidomain.TreeSitterDiagnostic{},
	}
	artifact, err := ucidomain.NewIndexAdmissionArtifactFromTreeSitter(fixture.source.SourceID, profile, body, extracted)
	require.NoError(t, err)
	artifactID := artifact.ArtifactID
	return ucidomain.IndexAdmissionFrame{
		Version:   ucidomain.IndexAdmissionFrameVersion,
		Profile:   ucidomain.IndexAdmissionProfile{ID: fixture.profile.ProfileID},
		Artifacts: []ucidomain.IndexAdmissionArtifact{artifact},
		Memberships: []ucidomain.IndexAdmissionMembership{{
			PathKey:     "installed-parser-canary.ts",
			DisplayPath: "installed-parser-canary.ts",
			Mode:        "100644",
			State:       ucidomain.IndexAdmissionMembershipPresent,
			ArtifactID:  &artifactID,
		}},
	}
}

func uciIndexAdmissionMarkdownFixtureFrame(t *testing.T, fixture *uciPublicationFixture) ucidomain.IndexAdmissionFrame {
	t.Helper()
	body := []byte("# Install Guide\n")
	extractionProfile := ucidomain.DefaultMarkdownExtractionProfile("uci-admission-markdown")
	profile, err := ucidomain.MarkdownIndexAdmissionArtifactProfile(extractionProfile)
	require.NoError(t, err)
	profile.ExtractionProfileDigest = ucidomain.IndexDigest(fixture.profile.ParserBundleDigest)
	artifact, err := ucidomain.NewIndexAdmissionArtifactFromMarkdown(
		fixture.source.SourceID,
		profile,
		extractionProfile,
		body,
		ucidomain.ExtractMarkdown(body, extractionProfile),
	)
	require.NoError(t, err)
	artifactID := artifact.ArtifactID
	return ucidomain.IndexAdmissionFrame{
		Version:   ucidomain.IndexAdmissionFrameVersion,
		Profile:   ucidomain.IndexAdmissionProfile{ID: fixture.profile.ProfileID},
		Artifacts: []ucidomain.IndexAdmissionArtifact{artifact},
		Memberships: []ucidomain.IndexAdmissionMembership{{
			PathKey:     "install-guide.md",
			DisplayPath: "install-guide.md",
			Mode:        "100644",
			State:       ucidomain.IndexAdmissionMembershipPresent,
			ArtifactID:  &artifactID,
		}},
	}
}

func uciIndexAdmissionFixtureReference(t *testing.T, artifact ucidomain.IndexAdmissionArtifact, rawTarget string) ucidomain.IndexAdmissionReference {
	t.Helper()
	for _, reference := range artifact.References {
		if reference.RawTarget == rawTarget {
			return reference
		}
	}
	t.Fatalf("missing admission reference for %q", rawTarget)
	return ucidomain.IndexAdmissionReference{}
}
