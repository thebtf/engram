package mcp

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/thebtf/engram/internal/uci"
)

const (
	uciCodeReadTestDefaultMaxBytes = 8_192
	uciCodeReadTestStoredArtifact  = "package fixture\nfunc StoredVersion() {}\n"
	uciCodeReadTestCurrentDiskBody = "package fixture\nfunc CurrentDiskVersion() {}\n"
)

type uciCodeReadArtifact struct {
	Ref           uci.QueryEntityRef
	Path          string
	Span          uci.QuerySpan
	ContentDigest uci.QueryContentDigest
	Excerpt       string
}

type uciCodeReadCall struct {
	ref   uci.ContextRef
	input codebaseReadInput
}

// uciCodeReadApplication adds only the versioned-read capability to the
// established context/search fake. It has no filesystem access: every response
// is an already-closed application result keyed to an AuthorizedContext.
type uciCodeReadApplication struct {
	*uciCodeIntelCompatibilityApplication

	readResponse func(uci.AuthorizedContext, codebaseReadInput) (uci.QueryResponse, error)
	readCalls    []uciCodeReadCall
}

var (
	_ codebaseContextApplication      = (*uciCodeReadApplication)(nil)
	_ codebaseIntelligenceApplication = (*uciCodeReadApplication)(nil)
	_ codebaseReadApplication         = (*uciCodeReadApplication)(nil)
)

func (application *uciCodeReadApplication) ReadCodebase(_ context.Context, authorized uci.AuthorizedContext, input codebaseReadInput) (uci.QueryResponse, error) {
	application.readCalls = append(application.readCalls, uciCodeReadCall{ref: authorized.Ref(), input: input})
	if application.readResponse == nil {
		return uci.QueryResponse{}, errors.New("code read fixture has no response")
	}
	return application.readResponse(authorized, input)
}

type uciCodeReadFixture struct {
	*uciCodeIntelCompatibilityFixture
	application *uciCodeReadApplication
}

func newUCICodeReadFixture(t *testing.T) *uciCodeReadFixture {
	t.Helper()

	compatibility := newUCICodeIntelCompatibilityFixture(t)
	application := &uciCodeReadApplication{
		uciCodeIntelCompatibilityApplication: compatibility.application,
	}
	// Replacing the application before any handle is selected preserves the
	// established opaque-handle owner while adding only the optional read port.
	compatibility.server.SetCodebaseContextApplication(application)
	return &uciCodeReadFixture{
		uciCodeIntelCompatibilityFixture: compatibility,
		application:                      application,
	}
}

func TestUCICodebaseReadToolDefinitionIsBoundedAndReadOnly(t *testing.T) {
	fixture := newUCICodeReadFixture(t)
	tool := uciCodeReadTool(t, fixture.server.ListTools())

	assert.Equal(t, "codebase_read", tool.Name)
	assert.Equal(t, "object", tool.InputSchema["type"])
	assert.Equal(t, false, tool.InputSchema["additionalProperties"])

	required, ok := tool.InputSchema["required"].([]string)
	require.True(t, ok, "codebase_read must declare its required source citation fields")
	assert.ElementsMatch(t, []string{"ref", "span", "content_digest"}, required)

	properties, ok := tool.InputSchema["properties"].(map[string]any)
	require.True(t, ok, "codebase_read must expose an object property schema")
	for _, requiredProperty := range []string{"context_handle", "ref", "span", "content_digest", "project", "verify_working_copy", "max_bytes"} {
		require.Contains(t, properties, requiredProperty)
	}

	contextHandle := properties["context_handle"].(map[string]any)
	assert.Equal(t, "string", contextHandle["type"])

	project := properties["project"].(map[string]any)
	projectDescription, ok := project["description"].(string)
	require.True(t, ok)
	assert.Contains(t, strings.ToLower(projectDescription), "compatibility")
	assert.NotContains(t, strings.ToLower(projectDescription), "select")

	ref := properties["ref"].(map[string]any)
	assert.Equal(t, "object", ref["type"])
	assert.Equal(t, false, ref["additionalProperties"])
	refRequired, ok := ref["required"].([]string)
	require.True(t, ok)
	assert.ElementsMatch(t, []string{"source_id", "view_id", "entity_key"}, refRequired)

	span := properties["span"].(map[string]any)
	assert.Equal(t, "object", span["type"])
	assert.Equal(t, false, span["additionalProperties"])
	spanRequired, ok := span["required"].([]string)
	require.True(t, ok)
	assert.ElementsMatch(t, []string{"byte_start", "byte_end", "line_start", "line_end"}, spanRequired)

	digest := properties["content_digest"].(map[string]any)
	assert.Equal(t, "string", digest["type"])

	verify := properties["verify_working_copy"].(map[string]any)
	assert.Equal(t, "boolean", verify["type"])

	maxBytes := properties["max_bytes"].(map[string]any)
	assert.Equal(t, "integer", maxBytes["type"])
	assert.Equal(t, 1, maxBytes["minimum"])
	assert.Equal(t, uciCodeReadTestDefaultMaxBytes, maxBytes["maximum"])
	assert.Equal(t, uciCodeReadTestDefaultMaxBytes, maxBytes["default"])

	for _, forbidden := range []string{"path", "absolute_path", "file", "locator", "content", "body", "raw", "edit", "write", "cwd", "root"} {
		assert.NotContains(t, properties, forbidden, "codebase_read must not advertise %q capability", forbidden)
	}
}

func TestUCICodebaseReadReturnsExactStoredArtifactForAuthorizedContext(t *testing.T) {
	fixture := newUCICodeReadFixture(t)
	artifact := uciCodeReadArtifactFor(fixture.refA, uciCodeReadTestStoredArtifact, "fixture.StoredVersion")
	expected := uciCodeReadQueryResponse(t, fixture.refA, &artifact, uci.QueryStatusOK, "uci-exp_read-a", nil, false)
	fixture.application.readResponse = func(_ uci.AuthorizedContext, _ codebaseReadInput) (uci.QueryResponse, error) {
		return expected, nil
	}

	handle := fixture.selectContext(t, fixture.clientA, fixture.refA)
	arguments := uciCodeReadArguments(handle, artifact)
	arguments["project"] = uciCodeIntelCompatibilityProject
	response := callUCICodeIntel(t, fixture.server, fixture.clientA, "codebase_read", arguments)

	text, payload := requireUCICodeReadResponse(t, response, fixture.refA, artifact, "uci-exp_read-a", nil)
	expectedJSON, err := json.Marshal(expected)
	require.NoError(t, err)
	assert.JSONEq(t, string(expectedJSON), text, "MCP must release the application-owned closed response unchanged")
	assert.NotContains(t, text, uciCodeReadTestCurrentDiskBody)
	assert.NotContains(t, text, uciCodebaseContextPrivateLocatorA)
	assert.NotContains(t, text, uciCodebaseContextPrivateLocatorB)

	require.Len(t, fixture.application.readCalls, 1)
	assert.Equal(t, fixture.refA, fixture.application.readCalls[0].ref)
	assert.Equal(t, codebaseReadInput{
		Ref:           artifact.Ref,
		Span:          artifact.Span,
		ContentDigest: artifact.ContentDigest,
		MaxBytes:      uciCodeReadTestDefaultMaxBytes,
	}, fixture.application.readCalls[0].input)
	require.Len(t, fixture.application.aliasCalls, 1)
	assert.Equal(t, fixture.refA, fixture.application.aliasCalls[0].ref)
	assert.Equal(t, uciCodeIntelCompatibilityProject, fixture.application.aliasCalls[0].project)
	assert.NotNil(t, payload.Exposure)
}

func TestUCICodebaseReadWorkingCopyVerificationIsMetadataOnly(t *testing.T) {
	for _, state := range []string{"working_copy_match", "working_copy_mismatch", "working_copy_unavailable"} {
		state := state
		t.Run(state, func(t *testing.T) {
			fixture := newUCICodeReadFixture(t)
			artifact := uciCodeReadArtifactFor(fixture.refA, uciCodeReadTestStoredArtifact, "fixture.StoredVersion")
			expected := uciCodeReadQueryResponse(t, fixture.refA, &artifact, uci.QueryStatusOK, "uci-exp_read-working-copy", []string{state}, false)
			fixture.application.readResponse = func(_ uci.AuthorizedContext, _ codebaseReadInput) (uci.QueryResponse, error) {
				return expected, nil
			}

			handle := fixture.selectContext(t, fixture.clientA, fixture.refA)
			arguments := uciCodeReadArguments(handle, artifact)
			arguments["verify_working_copy"] = true
			arguments["max_bytes"] = 64
			response := callUCICodeIntel(t, fixture.server, fixture.clientA, "codebase_read", arguments)

			text, payload := requireUCICodeReadResponse(t, response, fixture.refA, artifact, "uci-exp_read-working-copy", []string{state})
			assert.NotContains(t, text, uciCodeReadTestCurrentDiskBody, "working-copy verification must not substitute local disk bytes")
			assert.NotContains(t, text, `"working_copy":`, "verification state belongs in response metadata, not a new body channel")
			require.Len(t, fixture.application.readCalls, 1)
			assert.Equal(t, codebaseReadInput{
				Ref:               artifact.Ref,
				Span:              artifact.Span,
				ContentDigest:     artifact.ContentDigest,
				VerifyWorkingCopy: true,
				MaxBytes:          64,
			}, fixture.application.readCalls[0].input)
			assert.Equal(t, []string{state}, []string(*payload.Warnings))
		})
	}
}

func TestUCICodebaseReadStaleDigestAndHistoricalViewNeverFallBackToDisk(t *testing.T) {
	t.Run("stale digest is an authorized empty result", func(t *testing.T) {
		fixture := newUCICodeReadFixture(t)
		artifact := uciCodeReadArtifactFor(fixture.refA, uciCodeReadTestStoredArtifact, "fixture.StoredVersion")
		staleDigest := uciCodeReadDigest(uciCodeReadTestCurrentDiskBody)
		expected := uciCodeReadQueryResponse(t, fixture.refA, nil, uci.QueryStatusEmpty, "uci-exp_read-stale", nil, false)
		fixture.application.readResponse = func(_ uci.AuthorizedContext, input codebaseReadInput) (uci.QueryResponse, error) {
			assert.Equal(t, staleDigest, input.ContentDigest)
			return expected, nil
		}

		handle := fixture.selectContext(t, fixture.clientA, fixture.refA)
		arguments := uciCodeReadArguments(handle, artifact)
		arguments["content_digest"] = string(staleDigest)
		response := callUCICodeIntel(t, fixture.server, fixture.clientA, "codebase_read", arguments)

		text, payload := requireUCICodeReadEmptyResponse(t, response, fixture.refA, "uci-exp_read-stale")
		assert.Equal(t, uci.QueryStatusEmpty, payload.Status)
		assert.NotContains(t, text, artifact.Excerpt)
		assert.NotContains(t, text, uciCodeReadTestCurrentDiskBody)
		require.Len(t, fixture.application.readCalls, 1)
		assert.Equal(t, staleDigest, fixture.application.readCalls[0].input.ContentDigest)
	})

	t.Run("historical view returns its stored bytes rather than current disk bytes", func(t *testing.T) {
		fixture := newUCICodeReadFixture(t)
		historicalArtifact := uciCodeReadArtifactFor(fixture.refA, "package fixture\nfunc HistoricalView() {}\n", "fixture.HistoricalView")
		expected := uciCodeReadQueryResponse(t, fixture.refA, &historicalArtifact, uci.QueryStatusOK, "uci-exp_read-historical", []string{"working_copy_mismatch"}, true)
		fixture.application.readResponse = func(_ uci.AuthorizedContext, _ codebaseReadInput) (uci.QueryResponse, error) {
			return expected, nil
		}

		handle := fixture.selectContext(t, fixture.clientA, fixture.refA)
		arguments := uciCodeReadArguments(handle, historicalArtifact)
		arguments["verify_working_copy"] = true
		response := callUCICodeIntel(t, fixture.server, fixture.clientA, "codebase_read", arguments)

		text, payload := requireUCICodeReadResponse(t, response, fixture.refA, historicalArtifact, "uci-exp_read-historical", []string{"working_copy_mismatch"})
		require.NotNil(t, payload.Freshness)
		assert.Equal(t, uci.QueryFreshnessHistorical, payload.Freshness.State)
		assert.Equal(t, uci.QueryFreshnessPinnedHistory, payload.Freshness.Method)
		assert.NotContains(t, text, uciCodeReadTestCurrentDiskBody)
	})
}

func TestUCICodebaseReadDeniesForeignRevokedMismatchedAndPrivateContextsBeforeApplication(t *testing.T) {
	t.Run("foreign opaque handle", func(t *testing.T) {
		fixture := newUCICodeReadFixture(t)
		artifact := uciCodeReadArtifactFor(fixture.refA, uciCodeReadTestStoredArtifact, "fixture.StoredVersion")
		handleA := fixture.selectContext(t, fixture.clientA, fixture.refA)

		response := callUCICodeIntel(t, fixture.server, fixture.clientC, "codebase_read", uciCodeReadArguments(handleA, artifact))
		requireUCICodeReadSuppressed(t, response, uci.QueryStatusContextRequired, uci.QueryErrorContextMismatch, fixture, artifact.Excerpt)
		assert.Empty(t, fixture.application.readCalls, "foreign handles must fail before the application/store port")
	})

	t.Run("revoked selected context", func(t *testing.T) {
		fixture := newUCICodeReadFixture(t)
		artifact := uciCodeReadArtifactFor(fixture.refA, uciCodeReadTestStoredArtifact, "fixture.StoredVersion")
		handleA := fixture.selectContext(t, fixture.clientA, fixture.refA)
		fixture.authorizer.allowed["agent/a"][fixture.refA.CheckoutID] = false

		response := callUCICodeIntel(t, fixture.server, fixture.clientA, "codebase_read", uciCodeReadArguments(handleA, artifact))
		requireUCICodeReadSuppressed(t, response, uci.QueryStatusForbidden, uci.QueryErrorPermissionDenied, fixture, artifact.Excerpt)
		assert.Empty(t, fixture.application.readCalls, "revocation must be observed before a versioned read")
	})

	t.Run("mismatched entity view", func(t *testing.T) {
		fixture := newUCICodeReadFixture(t)
		artifact := uciCodeReadArtifactFor(fixture.refA, uciCodeReadTestStoredArtifact, "fixture.StoredVersion")
		handleA := fixture.selectContext(t, fixture.clientA, fixture.refA)
		arguments := uciCodeReadArguments(handleA, artifact)
		ref := arguments["ref"].(map[string]any)
		ref["view_id"] = fixture.refB.ViewID

		response := callUCICodeIntel(t, fixture.server, fixture.clientA, "codebase_read", arguments)
		requireUCICodeReadSuppressed(t, response, uci.QueryStatusContextRequired, uci.QueryErrorContextMismatch, fixture, artifact.Excerpt)
		assert.Empty(t, fixture.application.readCalls, "a citation outside the selected View must not reach storage")
	})

	t.Run("private context owned by another principal", func(t *testing.T) {
		fixture := newUCICodeReadFixture(t)
		artifact := uciCodeReadArtifactFor(fixture.refB, "package fixture\nfunc PrivateVersion() {}\n", "fixture.PrivateVersion")
		privateHandle := fixture.selectContext(t, fixture.clientB, fixture.refB)

		response := callUCICodeIntel(t, fixture.server, fixture.clientA, "codebase_read", uciCodeReadArguments(privateHandle, artifact))
		requireUCICodeReadSuppressed(t, response, uci.QueryStatusContextRequired, uci.QueryErrorContextMismatch, fixture, artifact.Excerpt)
		assert.Empty(t, fixture.application.readCalls, "another principal's private context must never reach the read port")
	})

	t.Run("conflicting project evidence", func(t *testing.T) {
		fixture := newUCICodeReadFixture(t)
		artifact := uciCodeReadArtifactFor(fixture.refA, uciCodeReadTestStoredArtifact, "fixture.StoredVersion")
		handleA := fixture.selectContext(t, fixture.clientA, fixture.refA)
		arguments := uciCodeReadArguments(handleA, artifact)
		arguments["project"] = uciCodeIntelCompatibilityConflict

		response := callUCICodeIntel(t, fixture.server, fixture.clientA, "codebase_read", arguments)
		requireUCICodeReadSuppressed(t, response, uci.QueryStatusContextRequired, uci.QueryErrorContextMismatch, fixture, artifact.Excerpt)
		require.Len(t, fixture.application.aliasCalls, 1)
		assert.Empty(t, fixture.application.readCalls, "compatibility evidence must agree before storage is called")
	})
}

func TestUCICodebaseReadRejectsInvalidBoundsAndUnknownArgumentsBeforeApplication(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate func(map[string]any)
	}{
		{
			name: "missing source citation",
			mutate: func(arguments map[string]any) {
				delete(arguments, "span")
			},
		},
		{
			name: "unknown absolute locator",
			mutate: func(arguments map[string]any) {
				arguments["absolute_path"] = `C:\\private\\source.go`
			},
		},
		{
			name: "unknown edit capability",
			mutate: func(arguments map[string]any) {
				arguments["edit"] = map[string]any{"replace": "forbidden"}
			},
		},
		{
			name: "unknown nested locator",
			mutate: func(arguments map[string]any) {
				arguments["ref"].(map[string]any)["path"] = `C:\\private\\source.go`
			},
		},
		{
			name: "invalid digest",
			mutate: func(arguments map[string]any) {
				arguments["content_digest"] = "not-a-sha256"
			},
		},
		{
			name: "negative byte start",
			mutate: func(arguments map[string]any) {
				arguments["span"].(map[string]any)["byte_start"] = -1
			},
		},
		{
			name: "empty byte span",
			mutate: func(arguments map[string]any) {
				span := arguments["span"].(map[string]any)
				span["byte_end"] = span["byte_start"]
			},
		},
		{
			name: "invalid line start",
			mutate: func(arguments map[string]any) {
				arguments["span"].(map[string]any)["line_start"] = 0
			},
		},
		{
			name: "zero byte budget",
			mutate: func(arguments map[string]any) {
				arguments["max_bytes"] = 0
			},
		},
		{
			name: "oversized byte budget",
			mutate: func(arguments map[string]any) {
				arguments["max_bytes"] = uciCodeReadTestDefaultMaxBytes + 1
			},
		},
		{
			name: "requested span exceeds byte budget",
			mutate: func(arguments map[string]any) {
				arguments["max_bytes"] = uciCodeReadTestDefaultMaxBytes
				arguments["span"] = map[string]any{
					"byte_start": 0,
					"byte_end":   uciCodeReadTestDefaultMaxBytes + 1,
					"line_start": 1,
					"line_end":   1,
				}
			},
		},
		{
			name: "non boolean verification request",
			mutate: func(arguments map[string]any) {
				arguments["verify_working_copy"] = "true"
			},
		},
	} {
		test := test
		t.Run(test.name, func(t *testing.T) {
			fixture := newUCICodeReadFixture(t)
			artifact := uciCodeReadArtifactFor(fixture.refA, uciCodeReadTestStoredArtifact, "fixture.StoredVersion")
			handleA := fixture.selectContext(t, fixture.clientA, fixture.refA)
			arguments := uciCodeReadArguments(handleA, artifact)
			test.mutate(arguments)
			resolvesBefore := len(fixture.application.resolveInputs)
			aliasesBefore := len(fixture.application.aliasCalls)

			response := callUCICodeIntel(t, fixture.server, fixture.clientA, "codebase_read", arguments)

			require.NotNil(t, response.Error)
			require.Nil(t, response.Result)
			assert.Equal(t, -32000, response.Error.Code)
			assert.Empty(t, fixture.application.readCalls, "invalid input must never reach application/store")
			assert.Len(t, fixture.application.resolveInputs, resolvesBefore, "invalid input must fail before context resolution")
			assert.Len(t, fixture.application.aliasCalls, aliasesBefore, "invalid input must fail before compatibility resolution")
			raw, err := json.Marshal(response)
			require.NoError(t, err)
			assert.NotContains(t, string(raw), uciCodeReadTestStoredArtifact)
			assert.NotContains(t, string(raw), uciCodeReadTestCurrentDiskBody)
			assert.NotContains(t, string(raw), `C:\\private\\source.go`)
		})
	}
}

func TestUCICodebaseReadRejectsUnclosedOrInexactApplicationResponses(t *testing.T) {
	t.Run("different response context is suppressed", func(t *testing.T) {
		fixture := newUCICodeReadFixture(t)
		requested := uciCodeReadArtifactFor(fixture.refA, uciCodeReadTestStoredArtifact, "fixture.StoredVersion")
		foreignArtifact := uciCodeReadArtifactFor(fixture.refB, "package fixture\nfunc ForeignResponse() {}\n", "fixture.ForeignResponse")
		fixture.application.readResponse = func(_ uci.AuthorizedContext, _ codebaseReadInput) (uci.QueryResponse, error) {
			return uciCodeReadQueryResponse(t, fixture.refB, &foreignArtifact, uci.QueryStatusOK, "uci-exp_read-foreign", nil, false), nil
		}

		handleA := fixture.selectContext(t, fixture.clientA, fixture.refA)
		response := callUCICodeIntel(t, fixture.server, fixture.clientA, "codebase_read", uciCodeReadArguments(handleA, requested))

		requireUCICodeReadSuppressed(t, response, uci.QueryStatusContextRequired, uci.QueryErrorContextMismatch, fixture, foreignArtifact.Excerpt)
		require.Len(t, fixture.application.readCalls, 1)
	})

	for _, test := range []struct {
		name      string
		configure func(t *testing.T, fixture *uciCodeReadFixture, requested uciCodeReadArtifact) string
	}{
		{
			name: "different response citation",
			configure: func(t *testing.T, fixture *uciCodeReadFixture, requested uciCodeReadArtifact) string {
				wrong := requested
				wrong.Ref.EntityKey = "fixture.WrongCitation"
				wrong.Excerpt = "func WrongCitation() {}\n"
				wrong.ContentDigest = uciCodeReadDigest(wrong.Excerpt)
				fixture.application.readResponse = func(_ uci.AuthorizedContext, _ codebaseReadInput) (uci.QueryResponse, error) {
					return uciCodeReadQueryResponse(t, fixture.refA, &wrong, uci.QueryStatusOK, "uci-exp_read-wrong-citation", nil, false), nil
				}
				return wrong.Excerpt
			},
		},
		{
			name: "missing application owned exposure receipt",
			configure: func(t *testing.T, fixture *uciCodeReadFixture, requested uciCodeReadArtifact) string {
				response := uciCodeReadQueryResponse(t, fixture.refA, &requested, uci.QueryStatusOK, "uci-exp_read-will-be-removed", nil, false)
				response.Exposure = nil
				fixture.application.readResponse = func(_ uci.AuthorizedContext, _ codebaseReadInput) (uci.QueryResponse, error) {
					return response, nil
				}
				return requested.Excerpt
			},
		},
		{
			name: "returned excerpt exceeds caller byte budget",
			configure: func(t *testing.T, fixture *uciCodeReadFixture, requested uciCodeReadArtifact) string {
				response := uciCodeReadQueryResponse(t, fixture.refA, &requested, uci.QueryStatusOK, "uci-exp_read-over-budget", nil, false)
				(*response.Items)[0].Excerpt = strings.Repeat("x", len(requested.Excerpt)+1)
				require.NoError(t, response.Validate(), "the application response remains a valid UCI envelope; MCP owns caller budget enforcement")
				fixture.application.readResponse = func(_ uci.AuthorizedContext, _ codebaseReadInput) (uci.QueryResponse, error) {
					return response, nil
				}
				return (*response.Items)[0].Excerpt
			},
		},
	} {
		test := test
		t.Run(test.name, func(t *testing.T) {
			fixture := newUCICodeReadFixture(t)
			requested := uciCodeReadArtifactFor(fixture.refA, uciCodeReadTestStoredArtifact, "fixture.StoredVersion")
			forbidden := test.configure(t, fixture, requested)
			handleA := fixture.selectContext(t, fixture.clientA, fixture.refA)
			arguments := uciCodeReadArguments(handleA, requested)
			if test.name == "returned excerpt exceeds caller byte budget" {
				arguments["max_bytes"] = len(requested.Excerpt)
			}

			response := callUCICodeIntel(t, fixture.server, fixture.clientA, "codebase_read", arguments)

			requireUCICodeReadSafeToolError(t, response, fixture, forbidden)
			require.Len(t, fixture.application.readCalls, 1)
		})
	}
}

func uciCodeReadArtifactFor(ref uci.ContextRef, source, entityKey string) uciCodeReadArtifact {
	const prefix = "package fixture\n"
	excerpt := strings.TrimPrefix(source, prefix)
	return uciCodeReadArtifact{
		Ref: uci.QueryEntityRef{
			SourceID:  ref.SourceID,
			ViewID:    ref.ViewID,
			EntityKey: entityKey,
		},
		Path: "internal/fixture/versioned.go",
		Span: uci.QuerySpan{
			ByteStart: int64(len(prefix)),
			ByteEnd:   int64(len(source)),
			LineStart: 2,
			LineEnd:   2,
		},
		ContentDigest: uciCodeReadDigest(source),
		Excerpt:       excerpt,
	}
}

func uciCodeReadDigest(body string) uci.QueryContentDigest {
	sum := sha256.Sum256([]byte(body))
	return uci.QueryContentDigest(hex.EncodeToString(sum[:]))
}

func uciCodeReadArguments(handle string, artifact uciCodeReadArtifact) map[string]any {
	return map[string]any{
		"context_handle": handle,
		"ref": map[string]any{
			"source_id":  artifact.Ref.SourceID,
			"view_id":    artifact.Ref.ViewID,
			"entity_key": artifact.Ref.EntityKey,
		},
		"span": map[string]any{
			"byte_start": artifact.Span.ByteStart,
			"byte_end":   artifact.Span.ByteEnd,
			"line_start": artifact.Span.LineStart,
			"line_end":   artifact.Span.LineEnd,
		},
		"content_digest": string(artifact.ContentDigest),
	}
}

func uciCodeReadQueryResponse(t *testing.T, ref uci.ContextRef, artifact *uciCodeReadArtifact, status uci.QueryResponseStatus, exposureRef string, warnings []string, historical bool) uci.QueryResponse {
	t.Helper()

	zero := int64(0)
	truncated := false
	contexts := uci.QueryContexts{uci.QueryContextRef{
		SourceID:   ref.SourceID,
		CheckoutID: ref.CheckoutID,
		ViewID:     ref.ViewID,
		Generation: ref.Generation,
		ProfileID:  ref.AnalysisProfileID,
		SpaceID:    ref.SpaceID,
	}}
	items := uci.QueryItems{}
	if artifact != nil {
		items = append(items, uci.QueryItem{
			Ref:           artifact.Ref,
			Path:          artifact.Path,
			Span:          artifact.Span,
			ContentDigest: artifact.ContentDigest,
			Kind:          uci.QueryItemCode,
			Language:      "go",
			Excerpt:       artifact.Excerpt,
			MatchSources:  []uci.QueryMatchSource{uci.QueryMatchExact},
		})
	}
	freshness := uci.QueryFreshness{
		State:          uci.QueryFreshnessObservedCurrent,
		Method:         uci.QueryFreshnessWatchWatermark,
		PendingChanges: &zero,
		EnrichmentWatermark: uci.QueryEnrichmentWatermark{
			Sequence: ref.Generation,
			State:    uci.QueryEnrichmentCurrent,
		},
	}
	if historical {
		freshness.State = uci.QueryFreshnessHistorical
		freshness.Method = uci.QueryFreshnessPinnedHistory
	}
	queryWarnings := uci.QueryWarnings(append([]string{}, warnings...))
	response := uci.QueryResponse{
		Schema:    uci.QueryResponseSchema,
		Status:    status,
		Contexts:  &contexts,
		Freshness: &freshness,
		Retrieval: &uci.QueryRetrieval{
			Mode:               uci.QueryRetrievalExact,
			DegradationReasons: []string{},
		},
		Coverage: &uci.QueryCoverage{
			Structural:       uci.IndexCoverageComplete,
			UnresolvedSites:  &zero,
			UnsupportedFiles: &zero,
		},
		Exposure: &uci.QueryExposure{
			ExposureRef:     exposureRef,
			CompletionState: uci.QueryCompletionUnknown,
		},
		Items:        &items,
		Truncated:    &truncated,
		Warnings:     &queryWarnings,
		Continuation: &uci.QueryContinuation{},
	}
	require.NoError(t, response.Validate(), "test application must return a closed UCI response")
	return response
}

func uciCodeReadTool(t *testing.T, tools []Tool) Tool {
	t.Helper()
	for _, tool := range tools {
		if tool.Name == "codebase_read" {
			return tool
		}
	}
	t.Fatalf("codebase_read was not advertised")
	return Tool{}
}

func requireUCICodeReadResponse(t *testing.T, response *Response, wantRef uci.ContextRef, wantArtifact uciCodeReadArtifact, wantExposure string, wantWarnings []string) (string, uci.QueryResponse) {
	t.Helper()

	text := uciCodeIntelToolText(t, response)
	var payload uci.QueryResponse
	require.NoError(t, json.Unmarshal([]byte(text), &payload))
	require.NoError(t, payload.Validate(), "codebase_read must return the closed UCI QueryResponse contract")
	assert.Equal(t, uci.QueryStatusOK, payload.Status)
	require.NotNil(t, payload.Contexts)
	require.Len(t, *payload.Contexts, 1)
	assertUCICodeReadContext(t, (*payload.Contexts)[0], wantRef)
	require.NotNil(t, payload.Retrieval)
	assert.Equal(t, uci.QueryRetrievalExact, payload.Retrieval.Mode)
	require.NotNil(t, payload.Exposure)
	assert.Equal(t, wantExposure, payload.Exposure.ExposureRef)
	assert.Equal(t, uci.QueryCompletionUnknown, payload.Exposure.CompletionState)
	require.NotNil(t, payload.Items)
	require.Len(t, *payload.Items, 1)
	assert.Equal(t, uci.QueryItem{
		Ref:           wantArtifact.Ref,
		Path:          wantArtifact.Path,
		Span:          wantArtifact.Span,
		ContentDigest: wantArtifact.ContentDigest,
		Kind:          uci.QueryItemCode,
		Language:      "go",
		Excerpt:       wantArtifact.Excerpt,
		MatchSources:  []uci.QueryMatchSource{uci.QueryMatchExact},
	}, (*payload.Items)[0])
	require.NotNil(t, payload.Warnings)
	assert.Equal(t, append([]string{}, wantWarnings...), []string(*payload.Warnings))
	return text, payload
}

func requireUCICodeReadEmptyResponse(t *testing.T, response *Response, wantRef uci.ContextRef, wantExposure string) (string, uci.QueryResponse) {
	t.Helper()

	text := uciCodeIntelToolText(t, response)
	var payload uci.QueryResponse
	require.NoError(t, json.Unmarshal([]byte(text), &payload))
	require.NoError(t, payload.Validate())
	assert.Equal(t, uci.QueryStatusEmpty, payload.Status)
	require.NotNil(t, payload.Contexts)
	require.Len(t, *payload.Contexts, 1)
	assertUCICodeReadContext(t, (*payload.Contexts)[0], wantRef)
	require.NotNil(t, payload.Exposure)
	assert.Equal(t, wantExposure, payload.Exposure.ExposureRef)
	require.NotNil(t, payload.Items)
	assert.Empty(t, *payload.Items)
	return text, payload
}

func assertUCICodeReadContext(t *testing.T, actual uci.QueryContextRef, want uci.ContextRef) {
	t.Helper()
	assert.Equal(t, want.SourceID, actual.SourceID)
	assert.Equal(t, want.CheckoutID, actual.CheckoutID)
	assert.Equal(t, want.ViewID, actual.ViewID)
	assert.Equal(t, want.Generation, actual.Generation)
	assert.Equal(t, want.AnalysisProfileID, actual.ProfileID)
	assert.Equal(t, want.SpaceID, actual.SpaceID)
}

func requireUCICodeReadSuppressed(t *testing.T, response *Response, wantStatus uci.QueryResponseStatus, wantCode uci.QueryErrorCode, fixture *uciCodeReadFixture, forbiddenBody string) {
	t.Helper()

	text := uciCodeIntelToolText(t, response)
	var payload uci.QueryResponse
	require.NoError(t, json.Unmarshal([]byte(text), &payload))
	require.NoError(t, payload.Validate(), "read refusals must remain valid closed UCI responses")
	assert.Equal(t, wantStatus, payload.Status)
	require.NotNil(t, payload.Error)
	assert.Equal(t, wantCode, payload.Error.Code)
	assert.Nil(t, payload.Exposure)
	assert.Nil(t, payload.Contexts)
	assert.Nil(t, payload.Freshness)
	assert.Nil(t, payload.Retrieval)
	assert.Nil(t, payload.Coverage)
	assert.Nil(t, payload.Items)
	assert.Nil(t, payload.Graph)
	assert.Nil(t, payload.Truncated)
	assert.Nil(t, payload.Warnings)
	assert.Nil(t, payload.Continuation)
	for _, forbidden := range []string{
		fixture.refA.SourceID,
		fixture.refA.CheckoutID,
		fixture.refA.ViewID,
		fixture.refB.CheckoutID,
		fixture.refB.ViewID,
		forbiddenBody,
		uciCodeReadTestCurrentDiskBody,
		uciCodebaseContextPrivateLocatorA,
		uciCodebaseContextPrivateLocatorB,
		"uci-exp_",
	} {
		assert.NotContains(t, text, forbidden, "suppressed response must not disclose %q", forbidden)
	}
}

func requireUCICodeReadSafeToolError(t *testing.T, response *Response, fixture *uciCodeReadFixture, forbiddenBody string) {
	t.Helper()

	require.NotNil(t, response.Error)
	require.Nil(t, response.Result)
	assert.Equal(t, -32000, response.Error.Code)
	raw, err := json.Marshal(response)
	require.NoError(t, err)
	for _, forbidden := range []string{
		fixture.refA.SourceID,
		fixture.refA.CheckoutID,
		fixture.refA.ViewID,
		fixture.refB.CheckoutID,
		fixture.refB.ViewID,
		forbiddenBody,
		uciCodeReadTestCurrentDiskBody,
		uciCodebaseContextPrivateLocatorA,
		uciCodebaseContextPrivateLocatorB,
		"uci-exp_",
	} {
		assert.NotContains(t, string(raw), forbidden, "safe tool error must not disclose %q", forbidden)
	}
}
