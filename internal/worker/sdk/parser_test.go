package sdk

import (
	"testing"

	"github.com/thebtf/engram/pkg/models"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ---------------------------------------------------------------------------
// ParseObservations
// ---------------------------------------------------------------------------

func TestParseObservations_EmptyText(t *testing.T) {
	result := ParseObservations("", "cid")
	assert.Empty(t, result)
}

func TestParseObservations_NoTags(t *testing.T) {
	result := ParseObservations("plain text with no XML tags at all", "cid")
	assert.Empty(t, result)
}

func TestParseObservations_FullObservation(t *testing.T) {
	text := `<observation>
<type>feature</type>
<title>Redis caching layer</title>
<subtitle>In memory store</subtitle>
<narrative>Implemented a Redis-backed cache to reduce DB load</narrative>
<facts>
<fact>Cache TTL set to 300 seconds</fact>
<fact>Eviction policy: allkeys-lru</fact>
</facts>
<concepts>
<concept>caching</concept>
<concept>performance</concept>
</concepts>
<files_read>
<file>internal/cache/client.go</file>
</files_read>
<files_modified>
<file>internal/cache/client.go</file>
<file>internal/cache/client_test.go</file>
</files_modified>
<commands_run>
<command>go test ./internal/cache/...</command>
</commands_run>
</observation>`

	obs := ParseObservations(text, "cid-001")
	require.Len(t, obs, 1)

	o := obs[0]
	assert.Equal(t, models.ObsTypeFeature, o.Type)
	assert.Equal(t, "Redis caching layer", o.Title)
	assert.Equal(t, "In memory store", o.Subtitle)
	assert.Equal(t, "Implemented a Redis-backed cache to reduce DB load", o.Narrative)
	assert.Equal(t, []string{"Cache TTL set to 300 seconds", "Eviction policy: allkeys-lru"}, o.Facts)
	assert.Equal(t, []string{"caching", "performance"}, o.Concepts)
	assert.Equal(t, []string{"internal/cache/client.go"}, o.FilesRead)
	assert.Equal(t, []string{"internal/cache/client.go", "internal/cache/client_test.go"}, o.FilesModified)
	assert.Equal(t, []string{"go test ./internal/cache/..."}, o.CommandsRun)
}

func TestParseObservations_MultipleBlocks(t *testing.T) {
	text := `<observation>
<type>bugfix</type>
<title>Nil pointer in auth handler</title>
<narrative>Added nil guard before token lookup</narrative>
</observation>
<observation>
<type>refactor</type>
<title>Extract config loader</title>
<narrative>Moved config loading into a separate package</narrative>
</observation>`

	obs := ParseObservations(text, "cid-002")
	require.Len(t, obs, 2)
	assert.Equal(t, models.ObsTypeBugfix, obs[0].Type)
	assert.Equal(t, "Nil pointer in auth handler", obs[0].Title)
	assert.Equal(t, models.ObsTypeRefactor, obs[1].Type)
	assert.Equal(t, "Extract config loader", obs[1].Title)
}

func TestParseObservations_TypeMapping(t *testing.T) {
	cases := []struct {
		xmlType  string
		wantType models.ObservationType
	}{
		{"bugfix", models.ObsTypeBugfix},
		{"feature", models.ObsTypeFeature},
		{"refactor", models.ObsTypeRefactor},
		{"change", models.ObsTypeChange},
		{"discovery", models.ObsTypeDiscovery},
		{"decision", models.ObsTypeDecision},
		{"guidance", models.ObservationType("guidance")},
	}

	for _, tc := range cases {
		t.Run(tc.xmlType, func(t *testing.T) {
			text := `<observation><type>` + tc.xmlType + `</type><title>T</title><narrative>N</narrative></observation>`
			obs := ParseObservations(text, "cid")
			require.Len(t, obs, 1)
			assert.Equal(t, tc.wantType, obs[0].Type)
		})
	}
}

func TestParseObservations_InvalidTypeDefaultsToChange(t *testing.T) {
	text := `<observation>
<type>not_a_valid_type</type>
<title>Some work</title>
<narrative>Did stuff</narrative>
</observation>`

	obs := ParseObservations(text, "cid")
	require.Len(t, obs, 1)
	assert.Equal(t, models.ObsTypeChange, obs[0].Type)
}

func TestParseObservations_MissingTypeDefaultsToChange(t *testing.T) {
	text := `<observation>
<title>No type tag</title>
<narrative>Did stuff</narrative>
</observation>`

	obs := ParseObservations(text, "cid")
	require.Len(t, obs, 1)
	assert.Equal(t, models.ObsTypeChange, obs[0].Type)
}

func TestParseObservations_CategoryOverridesType(t *testing.T) {
	// category=debugging → ObsTypeBugfix regardless of <type> field
	text := `<observation>
<category>debugging</category>
<type>change</type>
<title>Debug category win</title>
<narrative>Category should override type field</narrative>
</observation>`

	obs := ParseObservations(text, "cid")
	require.Len(t, obs, 1)
	assert.Equal(t, models.ObsTypeBugfix, obs[0].Type)
}

func TestParseObservations_CategoryUserBehavior_LLMDerived(t *testing.T) {
	text := `<observation>
<category>user_behavior</category>
<title>User prefers dark mode</title>
<narrative>The user consistently selects dark mode</narrative>
</observation>`

	obs := ParseObservations(text, "cid")
	require.Len(t, obs, 1)
	assert.Equal(t, models.ObsTypeGuidance, obs[0].Type)
	assert.Equal(t, models.SourceLLMDerived, obs[0].SourceType)
}

func TestParseObservations_InvalidConceptsFiltered(t *testing.T) {
	text := `<observation>
<type>discovery</type>
<title>Concept filter test</title>
<narrative>Test filtering</narrative>
<concepts>
<concept>security</concept>
<concept>made-up-concept</concept>
<concept>performance</concept>
<concept>totally-invalid</concept>
</concepts>
</observation>`

	obs := ParseObservations(text, "cid")
	require.Len(t, obs, 1)
	assert.Equal(t, []string{"security", "performance"}, obs[0].Concepts)
}

func TestParseObservations_TypeNameInConceptsFiltered(t *testing.T) {
	// The observation type itself should be removed from concepts list
	text := `<observation>
<type>bugfix</type>
<title>Bug concept test</title>
<narrative>Test</narrative>
<concepts>
<concept>bugfix</concept>
<concept>debugging</concept>
</concepts>
</observation>`

	obs := ParseObservations(text, "cid")
	require.Len(t, obs, 1)
	// "bugfix" matches the type → removed; "debugging" is valid → kept
	assert.Equal(t, []string{"debugging"}, obs[0].Concepts)
}

func TestParseObservations_ConceptsCaseNormalized(t *testing.T) {
	text := `<observation>
<type>feature</type>
<title>Case test</title>
<narrative>Test</narrative>
<concepts>
<concept>SECURITY</concept>
<concept>  Performance  </concept>
<concept>Anti-Pattern</concept>
</concepts>
</observation>`

	obs := ParseObservations(text, "cid")
	require.Len(t, obs, 1)
	assert.Contains(t, obs[0].Concepts, "security")
	assert.Contains(t, obs[0].Concepts, "performance")
	assert.Contains(t, obs[0].Concepts, "anti-pattern")
}

func TestParseObservations_AllValidConcepts(t *testing.T) {
	wantConcepts := []string{
		"how-it-works", "why-it-exists", "what-changed", "problem-solution",
		"gotcha", "pattern", "trade-off", "best-practice", "anti-pattern",
		"architecture", "security", "performance", "testing", "debugging",
		"workflow", "tooling", "refactoring", "api", "database",
		"configuration", "error-handling", "caching", "logging", "auth", "validation",
	}

	for _, c := range wantConcepts {
		t.Run(c, func(t *testing.T) {
			text := `<observation><type>discovery</type><title>T</title><narrative>N</narrative>` +
				`<concepts><concept>` + c + `</concept></concepts></observation>`
			obs := ParseObservations(text, "cid")
			require.Len(t, obs, 1)
			assert.Contains(t, obs[0].Concepts, c)
		})
	}
}

// ---------------------------------------------------------------------------
// ParseSummary
// ---------------------------------------------------------------------------

func TestParseSummary_NilOnEmpty(t *testing.T) {
	assert.Nil(t, ParseSummary("", 1))
}

func TestParseSummary_NilOnNoTag(t *testing.T) {
	assert.Nil(t, ParseSummary("no summary tag here", 2))
}

func TestParseSummary_SkipTagReturnsNil(t *testing.T) {
	text := `<skip_summary reason="No code changes performed"/>`
	assert.Nil(t, ParseSummary(text, 3))
}

func TestParseSummary_SkipTagPrecedesBlock(t *testing.T) {
	// skip_summary wins even when a <summary> block follows
	text := `<skip_summary reason="trivial session"/>
<summary><request>should be ignored</request></summary>`
	assert.Nil(t, ParseSummary(text, 4))
}

func TestParseSummary_AllFields(t *testing.T) {
	text := `<summary>
<request>Implement gRPC auth interceptor</request>
<investigated>Existing auth middleware and gRPC patterns</investigated>
<learned>gRPC metadata is the equivalent of HTTP headers for bearer tokens</learned>
<completed>Added UnaryInterceptor that validates X-Auth-Token metadata</completed>
<next_steps>Add streaming interceptor and write integration tests</next_steps>
<notes>Consider caching validated tokens to reduce DB hits</notes>
</summary>`

	s := ParseSummary(text, 10)
	require.NotNil(t, s)
	assert.Equal(t, "Implement gRPC auth interceptor", s.Request)
	assert.Equal(t, "Existing auth middleware and gRPC patterns", s.Investigated)
	assert.Equal(t, "gRPC metadata is the equivalent of HTTP headers for bearer tokens", s.Learned)
	assert.Equal(t, "Added UnaryInterceptor that validates X-Auth-Token metadata", s.Completed)
	assert.Equal(t, "Add streaming interceptor and write integration tests", s.NextSteps)
	assert.Equal(t, "Consider caching validated tokens to reduce DB hits", s.Notes)
}

func TestParseSummary_MinimalBlock(t *testing.T) {
	text := `<summary><request>Deploy hotfix</request></summary>`
	s := ParseSummary(text, 5)
	require.NotNil(t, s)
	assert.Equal(t, "Deploy hotfix", s.Request)
}

func TestParseSummary_EmptyFields(t *testing.T) {
	text := `<summary><request></request><investigated></investigated></summary>`
	s := ParseSummary(text, 6)
	require.NotNil(t, s)
	assert.Equal(t, "", s.Request)
}

// ---------------------------------------------------------------------------
// extractField
// ---------------------------------------------------------------------------

func TestExtractField_Table(t *testing.T) {
	cases := []struct {
		name      string
		content   string
		field     string
		want      string
	}{
		{
			name:    "present field",
			content: "<title>Rewrite auth handler</title>",
			field:   "title",
			want:    "Rewrite auth handler",
		},
		{
			name:    "whitespace trimmed",
			content: "<narrative>  lots of spaces  </narrative>",
			field:   "narrative",
			want:    "lots of spaces",
		},
		{
			name:    "field not present",
			content: "<other>value</other>",
			field:   "title",
			want:    "",
		},
		{
			name:    "empty tag",
			content: "<title></title>",
			field:   "title",
			want:    "",
		},
		{
			name:    "surrounded by siblings",
			content: "<a>1</a><title>Target</title><b>2</b>",
			field:   "title",
			want:    "Target",
		},
		{
			name:    "nested inside parent",
			content: "<outer><title>Inner</title></outer>",
			field:   "title",
			want:    "Inner",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, extractField(tc.content, tc.field))
		})
	}
}

// ---------------------------------------------------------------------------
// extractArrayElements
// ---------------------------------------------------------------------------

func TestExtractArrayElements_Table(t *testing.T) {
	cases := []struct {
		name        string
		content     string
		arrayName   string
		elemName    string
		want        []string
	}{
		{
			name:      "two elements",
			content:   "<facts><fact>Alpha</fact><fact>Beta</fact></facts>",
			arrayName: "facts", elemName: "fact",
			want: []string{"Alpha", "Beta"},
		},
		{
			name:      "empty array tag",
			content:   "<facts></facts>",
			arrayName: "facts", elemName: "fact",
			want: nil,
		},
		{
			name:      "array not present",
			content:   "<other><item>val</item></other>",
			arrayName: "facts", elemName: "fact",
			want: nil,
		},
		{
			name:      "single element",
			content:   "<concepts><concept>security</concept></concepts>",
			arrayName: "concepts", elemName: "concept",
			want: []string{"security"},
		},
		{
			name: "multiline array",
			content: `<files>
<file>handler.go</file>
<file>handler_test.go</file>
<file>middleware.go</file>
</files>`,
			arrayName: "files", elemName: "file",
			want: []string{"handler.go", "handler_test.go", "middleware.go"},
		},
		{
			name:      "whitespace trimmed",
			content:   "<items><item>  trimmed  </item></items>",
			arrayName: "items", elemName: "item",
			want: []string{"trimmed"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := extractArrayElements(tc.content, tc.arrayName, tc.elemName)
			assert.Equal(t, tc.want, got)
		})
	}
}

// ---------------------------------------------------------------------------
// Package-level variable contracts
// ---------------------------------------------------------------------------

func TestValidObsTypes_Map(t *testing.T) {
	want := map[string]bool{
		"bugfix":    true,
		"feature":   true,
		"refactor":  true,
		"change":    true,
		"discovery": true,
		"decision":  true,
		"guidance":  true,
	}
	assert.Equal(t, want, validObsTypes)
}

func TestValidConcepts_PresentAndAbsent(t *testing.T) {
	mustBePresent := []string{
		"how-it-works", "why-it-exists", "what-changed", "problem-solution",
		"gotcha", "pattern", "trade-off", "best-practice", "anti-pattern",
		"architecture", "security", "performance", "testing", "debugging",
		"workflow", "tooling", "refactoring", "api", "database",
		"configuration", "error-handling", "caching", "logging", "auth", "validation",
	}
	for _, c := range mustBePresent {
		assert.True(t, validConcepts[c], "concept %q must be in validConcepts", c)
	}

	mustBeAbsent := []string{"random", "foo", "bar", "unknown", "xyz"}
	for _, c := range mustBeAbsent {
		assert.False(t, validConcepts[c], "concept %q must not be in validConcepts", c)
	}
}
