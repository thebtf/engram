package worker

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"github.com/thebtf/engram/internal/auth"
	"github.com/thebtf/engram/internal/mcp"
	"github.com/thebtf/engram/internal/uci"
)

func TestUCIApplicationMCPOversizedCitationSubspan(t *testing.T) {
	t.Setenv("ENGRAM_CODE_INTEL_ENABLED", "true")
	t.Setenv("ENGRAM_EMBEDDING_URL", "")
	t.Setenv("ENGRAM_EMBEDDING_MODEL", "")
	store := openWorkerUCIContextCompositionStore(t)
	server := mcp.NewServer(mcp.ServerOptions{Version: "mcp-read-window-postgres"})
	composition, err := composeUCIContext(true, store.GetDB(), server, workerUCISemanticConfig())
	require.NoError(t, err)
	fixture := newWorkerUCIApplicationFixture(t, composition)

	const indexedBytes = 26_184
	const prefix = "package fixture\n"
	const lead = "func OversizedCitation() { // BoundedReadNeedle "
	const tail = "\n}\n"
	source := lead + strings.Repeat("x", 8191-len(prefix)-len(lead)) + "€\n" + strings.Repeat("z", indexedBytes-8195-len(tail)) + tail
	body := prefix + source
	require.Len(t, body, indexedBytes)
	require.True(t, utf8.ValidString(body))
	artifact := workerUCIApplicationAddArtifact(t, composition.projectionStore, workerUCIApplicationArtifactInput{
		sourceID: fixture.source.SourceID, profileDigest: fixture.profile.ParserBundleDigest,
		label: "mcp-oversized-citation", name: "OversizedCitation", symbol: "oversized-citation", source: source,
	})
	memberships := []uci.IndexMembership{workerUCIApplicationMembership("internal/oversized.go", artifact)}
	replacements := []uci.IndexEdgeReplacement{{SourcePath: "internal/oversized.go"}}
	publisher, err := composition.projectionStore.Publisher(composition.authorizer, uci.IndexPublicationConfig{Limits: uci.DefaultIndexPublicationLimits()})
	require.NoError(t, err)
	parent := fixture.current.Context
	published := workerUCIApplicationPublish(t, publisher, uci.IndexCaller{
		AuthRealm: string(auth.SourceClient), Principal: fixture.principal, OwnerInstance: "mcp-read-window-owner",
	}, workerUCIApplicationPublicationInput{
		checkout: fixture.checkout, profileID: fixture.profile.ProfileID, key: "mcp-read-window",
		jobKind: uci.IndexJobReconcile, parent: &parent,
		part:        workerUCIApplicationPart([]workerUCIApplicationArtifact{artifact}, memberships, replacements),
		memberships: memberships, replacements: replacements, sequence: 13,
	})
	identity := auth.ClientWithPrincipal("read-write", fixture.workstationID, fixture.principal, auth.PrincipalKindAgent)
	caller := auth.WithIdentity(mcp.ContextWithSession(context.Background(), fixture.clientSessionID+"-window"), identity)
	handle := workerUCIApplicationSelectContext(t, server, caller, published.Context)
	oldHandle := workerUCIApplicationSelectContext(t, server, caller, fixture.historical.Context)
	search := workerUCIApplicationQueryResponse(t, workerUCIApplicationToolResponse(t, server, caller, "codebase_search", map[string]any{
		"context_handle": handle, "query": "BoundedReadNeedle", "path_prefix": "internal/oversized.go", "limit": 10,
	}))
	require.NoError(t, search.Validate())
	require.Equal(t, uci.QueryStatusOK, search.Status)
	require.NotNil(t, search.Items)
	require.Len(t, *search.Items, 1)
	citation := (*search.Items)[0]
	require.Equal(t, int64(0), citation.Span.ByteStart)
	require.Equal(t, int64(indexedBytes), citation.Span.ByteEnd, "the real MCP search must supply the oversized indexed citation")
	require.Equal(t, uci.QueryContentDigest(strings.TrimPrefix(workerUCIApplicationDigest(body), "sha256:")), citation.ContentDigest)
	require.NotEmpty(t, citation.MembershipID)
	require.NotNil(t, search.Contexts)
	require.Equal(t, uciApplicationQueryContextRef(published.Context), (*search.Contexts)[0])

	argumentsFor := func(start, end int64) map[string]any {
		window := citation.Span
		window.ByteStart, window.ByteEnd = start, end
		return map[string]any{
			"context_handle": handle, "ref": citation.Ref, "membership_id": citation.MembershipID,
			"span": window, "indexed_span": citation.Span, "content_digest": citation.ContentDigest, "max_bytes": 8192,
		}
	}
	call := func(t *testing.T, arguments map[string]any) *mcp.Response {
		t.Helper()
		params, marshalErr := json.Marshal(map[string]any{"name": "codebase_read", "arguments": arguments})
		require.NoError(t, marshalErr)
		response := server.HandleRequest(caller, &mcp.Request{JSONRPC: "2.0", ID: uuid.NewString(), Method: "tools/call", Params: params})
		require.NotNil(t, response)
		return response
	}

	t.Run("full citation still exceeds the unchanged cap", func(t *testing.T) {
		arguments := argumentsFor(0, indexedBytes)
		delete(arguments, "indexed_span")
		before := workerUCIApplicationExposureCount(t, store.GetDB())
		response := call(t, arguments)
		require.NotNil(t, response.Error)
		require.Contains(t, response.Error.Message, "span exceeds max_bytes 8192")
		require.Nil(t, response.Result)
		require.Equal(t, before, workerUCIApplicationExposureCount(t, store.GetDB()))
	})

	for _, test := range []struct {
		name       string
		start, end int64
		wantEnd    int64
	}{
		{name: "UTF-8 boundary is shortened without replacing stored bytes", start: 0, end: 8192, wantEnd: 8191},
		{name: "interior window keeps the full citation digest and corrected line span", start: 8195, end: 16387, wantEnd: 16387},
	} {
		t.Run(test.name, func(t *testing.T) {
			before := workerUCIApplicationExposureCount(t, store.GetDB())
			response := call(t, argumentsFor(test.start, test.end))
			require.Nil(t, response.Error, "codebase_read must accept a bounded window with its original indexed_span: %+v", response.Error)
			read := workerUCIApplicationQueryResponse(t, response)
			require.NoError(t, read.Validate())
			require.Equal(t, uci.QueryStatusOK, read.Status)
			require.NotNil(t, read.Items)
			require.Len(t, *read.Items, 1)
			item := (*read.Items)[0]
			require.Equal(t, citation.Ref, item.Ref)
			require.Equal(t, citation.MembershipID, item.MembershipID)
			require.Equal(t, citation.ContentDigest, item.ContentDigest, "the digest authenticates the indexed source, not the returned window")
			require.Equal(t, citation.Path, item.Path)
			require.Equal(t, body[test.start:test.wantEnd], item.Excerpt)
			require.True(t, utf8.ValidString(item.Excerpt))
			require.LessOrEqual(t, len(item.Excerpt), 8192)
			require.Equal(t, uci.QuerySpan{
				ByteStart: test.start, ByteEnd: test.wantEnd,
				LineStart: int64(strings.Count(body[:test.start], "\n") + 1),
				LineEnd:   int64(strings.Count(body[:test.wantEnd], "\n") + 1),
			}, item.Span)
			require.NotNil(t, read.Contexts)
			require.Equal(t, uciApplicationQueryContextRef(published.Context), (*read.Contexts)[0])
			require.NotNil(t, read.Warnings)
			require.Contains(t, []string(*read.Warnings), "source_partial_indexed_chunk")
			require.NotNil(t, read.Exposure)
			require.True(t, uci.ValidExposureRef(read.Exposure.ExposureRef))
			require.Equal(t, before+1, workerUCIApplicationExposureCount(t, store.GetDB()))
		})
	}

	for _, test := range []struct {
		name   string
		mutate func(map[string]any)
	}{
		{name: "stale digest", mutate: func(arguments map[string]any) { arguments["content_digest"] = strings.Repeat("0", 64) }},
		{name: "unknown membership", mutate: func(arguments map[string]any) { arguments["membership_id"] = uuid.NewString() }},
		{name: "forged indexed span", mutate: func(arguments map[string]any) {
			indexed := citation.Span
			indexed.ByteEnd--
			arguments["indexed_span"] = indexed
		}},
		{name: "window starts inside a UTF-8 code point", mutate: func(arguments map[string]any) {
			window := citation.Span
			window.ByteStart, window.ByteEnd = 8192, 8200
			arguments["span"] = window
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			arguments := argumentsFor(8195, 8259)
			test.mutate(arguments)
			response := call(t, arguments)
			require.Nil(t, response.Error)
			read := workerUCIApplicationQueryResponse(t, response)
			require.NoError(t, read.Validate())
			require.Equal(t, uci.QueryStatusEmpty, read.Status)
			require.NotNil(t, read.Items)
			require.Empty(t, *read.Items)
			require.NotNil(t, read.Warnings)
			require.NotContains(t, []string(*read.Warnings), "source_partial_indexed_chunk", "an empty miss proves no returned-source omission")
		})
	}

	t.Run("citation cannot be read through another pinned View", func(t *testing.T) {
		arguments := argumentsFor(8195, 8259)
		arguments["context_handle"] = oldHandle
		before := workerUCIApplicationExposureCount(t, store.GetDB())
		response := call(t, arguments)
		require.Nil(t, response.Error)
		read := workerUCIApplicationQueryResponse(t, response)
		require.NoError(t, read.Validate())
		require.Equal(t, uci.QueryStatusContextRequired, read.Status)
		require.NotNil(t, read.Error)
		require.Equal(t, uci.QueryErrorContextMismatch, read.Error.Code)
		require.Nil(t, read.Items)
		require.Nil(t, read.Contexts)
		require.Nil(t, read.Exposure)
		require.Equal(t, before, workerUCIApplicationExposureCount(t, store.GetDB()))
	})

	for _, test := range []struct {
		name   string
		mutate func(map[string]any)
	}{
		{name: "window outside indexed citation", mutate: func(arguments map[string]any) {
			window := citation.Span
			window.ByteStart, window.ByteEnd = indexedBytes-4, indexedBytes+4
			arguments["span"] = window
		}},
		{name: "window exceeds budget", mutate: func(arguments map[string]any) {
			window := citation.Span
			window.ByteEnd = 8193
			arguments["span"] = window
		}},
		{name: "budget cannot be increased", mutate: func(arguments map[string]any) { arguments["max_bytes"] = 8193 }},
	} {
		t.Run(test.name, func(t *testing.T) {
			arguments := argumentsFor(8195, 8259)
			test.mutate(arguments)
			before := workerUCIApplicationExposureCount(t, store.GetDB())
			response := call(t, arguments)
			require.NotNil(t, response.Error)
			require.Nil(t, response.Result)
			require.Equal(t, before, workerUCIApplicationExposureCount(t, store.GetDB()))
		})
	}

	t.Run("discovery exposes an optional closed indexed span without raising limits", func(t *testing.T) {
		for _, tool := range server.ListTools() {
			if tool.Name != "codebase_read" {
				continue
			}
			properties, ok := tool.InputSchema["properties"].(map[string]any)
			require.True(t, ok)
			require.Contains(t, properties, "indexed_span")
			indexed, ok := properties["indexed_span"].(map[string]any)
			require.True(t, ok)
			require.Equal(t, "object", indexed["type"])
			require.Equal(t, false, indexed["additionalProperties"])
			require.ElementsMatch(t, []string{"byte_start", "byte_end", "line_start", "line_end"}, indexed["required"])
			require.NotContains(t, tool.InputSchema["required"], "indexed_span", "exact-span clients must remain supported")
			budget, ok := properties["max_bytes"].(map[string]any)
			require.True(t, ok)
			require.Equal(t, 8192, budget["maximum"])
			return
		}
		t.Fatal("codebase_read was not advertised")
	})
}
