package worker

import (
	"context"
	"strings"
	"unicode"

	"github.com/thebtf/engram/internal/mcp"
	"github.com/thebtf/engram/internal/taskcontext"
	"github.com/thebtf/engram/internal/uci"
)

// TaskCodebase uses only the deterministic services. In particular it never
// enters executeSearch, semanticService, or an embedding provider.
func (application *UCIApplication) TaskCodebase(ctx context.Context, authorized uci.AuthorizedContext, request taskcontext.Request) (taskcontext.Packet, error) {
	var packet taskcontext.Packet
	session, err := uciApplicationClientSession(ctx)
	if err != nil {
		return packet, err
	}
	mode := uci.QueryModeFTS
	query := strings.TrimSpace(request.Query)
	if strings.Contains(query, "/") && !strings.ContainsAny(query, " \t\n") {
		mode = uci.QueryModeExactRelativePath
	} else if strings.IndexFunc(query, func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsDigit(r) && r != '_' }) < 0 {
		mode = uci.QueryModeExactLocalName
	}
	spec := uci.QuerySpec{ClientSessionID: session, Mode: mode, Text: query, Order: uci.QueryOrderRelevance, Limit: 1}
	result, err := application.queryService.Query(ctx, authorized, spec)
	if err != nil {
		return packet, err
	}
	if result.Response.Items != nil && len(*result.Response.Items) == 0 && mode != uci.QueryModeFTS {
		spec.Mode = uci.QueryModeFTS
		result, err = application.queryService.Query(ctx, authorized, spec)
		if err != nil {
			return packet, err
		}
	}
	packet.Search = result.Response
	if packet.Search.Status == uci.QueryStatusUnavailable || packet.Search.Items == nil || len(*packet.Search.Items) == 0 {
		return packet, nil
	}
	item := (*packet.Search.Items)[0]
	span := item.Span
	var indexed *uci.QuerySpan
	if span.ByteEnd-span.ByteStart > 2048 {
		original := span
		indexed = &original
		span.ByteEnd = span.ByteStart + 2048
	}
	source, err := application.ReadCodebase(ctx, authorized, mcp.CodebaseReadInput{Ref: item.Ref, MembershipID: item.MembershipID, Span: span, IndexedSpan: indexed, ContentDigest: item.ContentDigest, MaxBytes: 2048})
	if err != nil {
		return packet, err
	}
	packet.Source = &source
	deadline, _ := ctx.Deadline()
	calls, err := application.ExploreCodebase(ctx, authorized, mcp.CodebaseGraphInput{Action: uci.GraphActionNeighbors, Target: uci.GraphTarget{EntityKey: item.Ref.EntityKey}, Filter: uci.GraphFilter{Direction: uci.GraphDirectionBoth, Relations: []uci.IndexRelation{"calls", "may_call"}}, Budget: uci.GraphBudget{MaxDepth: 1, MaxVisited: 8, MaxNodes: 4, MaxEdges: 4, Deadline: deadline}})
	if err != nil {
		return packet, err
	}
	packet.Calls = &calls
	freshness, err := application.CodebaseFreshness(ctx, authorized, "")
	if err != nil {
		return packet, err
	}
	packet.Search.Freshness = &freshness
	packet.Source.Freshness = &freshness
	packet.Calls.Freshness = &freshness
	return packet, nil
}
