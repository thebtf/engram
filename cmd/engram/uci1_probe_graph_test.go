package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/thebtf/engram/internal/uci"
)

const (
	uci1GraphAmbiguousName   = "UCI1GraphSameName"
	uci1GraphAmbiguousCaller = "UCI1GraphAmbiguousCaller"
	uci1GraphMalformedFresh  = "UCI1GraphMalformedFresh"
	uci1GraphCycleEntry      = "UCI1GraphCycle0"
	uci1GraphCycleCount      = 8
)

type uci1GraphCallInput struct {
	action                         string
	target                         map[string]any
	direction                      string
	maxDepth, maxVisited, maxNodes int
	maxEdges                       int
}

// uci1ProbeGraphInstalled observes the graph contract through the live installed
// MCP client. Each source mutation is published before it is queried and the
// original fixture bytes are republished before the callback returns.
func uci1ProbeGraphInstalled(ctx context.Context, runtime uciInstalledAcceptanceScenarioRuntime) (evidence map[string]uciInstalledAcceptanceScenarioEvidence, retErr error) {
	if ctx == nil || runtime.ClientA == nil || runtime.Worktrees.primaryRoot == "" || runtime.Request.Fixture.RelativePath == "" {
		return nil, errors.New("installed graph probe runtime is incomplete")
	}
	selection, found := runtime.Selections[uciInstalledAcceptanceClientA]
	if !found || selection.contextHandle == "" {
		return nil, errors.New("installed graph probe primary selection is unavailable")
	}
	if filepath.IsAbs(runtime.Request.Fixture.RelativePath) || strings.HasPrefix(filepath.Clean(runtime.Request.Fixture.RelativePath), "..") {
		return nil, errors.New("installed graph probe fixture path is unsafe")
	}

	fixturePath := filepath.Join(runtime.Worktrees.primaryRoot, filepath.FromSlash(runtime.Request.Fixture.RelativePath))
	baseline, err := os.ReadFile(fixturePath)
	if err != nil {
		return nil, fmt.Errorf("read installed graph fixture baseline: %w", err)
	}
	selection, publication, err := uci1GraphCurrentPublication(ctx, runtime.ClientA, selection)
	if err != nil {
		return nil, err
	}

	mutated := false
	restored := false
	defer func() {
		if !mutated || restored {
			return
		}
		restoreCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		restoredPublication, wrote, restoreErr := uci1GraphWriteAndPublish(restoreCtx, runtime.ClientA, selection, publication, fixturePath, baseline)
		if wrote {
			mutated = true
		}
		if restoreErr == nil {
			selection.runID = restoredPublication.runID
			selection.viewID = restoredPublication.viewID
			if _, restoreErr = uciObserveInstalledAcceptanceSearchGraphRead(restoreCtx, runtime.ClientA, selection, restoredPublication, runtime.Request.Fixture, runtime.Request.Fixture.PrimaryCallee); restoreErr == nil {
				restored = true
			}
		}
		if restoreErr != nil {
			retErr = errors.Join(retErr, fmt.Errorf("restore installed graph fixture and publication: %w", restoreErr))
		}
	}()

	publication, wrote, err := uci1GraphWriteAndPublish(ctx, runtime.ClientA, selection, publication, fixturePath, []byte(uci1GraphAmbiguousSource()))
	mutated = mutated || wrote
	if err != nil {
		return nil, fmt.Errorf("publish U20 ambiguous graph fixture: %w", err)
	}
	selection.runID = publication.runID
	selection.viewID = publication.viewID
	ambiguousItem, ambiguousSearch, err := uci1GraphFindItem(ctx, runtime.ClientA, selection, publication, runtime.Request.Fixture.RelativePath, uci1GraphAmbiguousCaller)
	if err != nil {
		return nil, fmt.Errorf("observe U20 caller through installed search: %w", err)
	}
	if _, err := uci1GraphReadItem(ctx, runtime.ClientA, selection, publication, ambiguousItem); err != nil {
		return nil, fmt.Errorf("observe U20 caller through installed read: %w", err)
	}
	ambiguousByName, err := uci1GraphCall(ctx, runtime.ClientA, selection, publication, uci1GraphCallInput{
		action: "explain",
		target: map[string]any{
			"source_id": publication.sourceID,
			"view_id":   publication.viewID,
			"name":      uci1GraphAmbiguousName,
		},
		direction:  "both",
		maxDepth:   4,
		maxVisited: 64,
		maxNodes:   16,
		maxEdges:   32,
	})
	if err != nil {
		return nil, fmt.Errorf("observe U20 ambiguous installed graph target: %w", err)
	}
	if err := uci1GraphRequireAmbiguousName(ambiguousByName); err != nil {
		return nil, fmt.Errorf("observe U20 ambiguous same-name methods: %w", err)
	}
	ambiguousCallerGraph, err := uci1GraphCall(ctx, runtime.ClientA, selection, publication, uci1GraphCallInput{action: "neighbors", target: uci1GraphTarget(ambiguousItem.Ref), direction: "outgoing", maxDepth: 4, maxVisited: 64, maxNodes: 16, maxEdges: 32})
	if err != nil {
		return nil, fmt.Errorf("observe U20 ambiguous caller graph: %w", err)
	}
	if err := uci1GraphRequireUnresolvedCaller(ambiguousCallerGraph, ambiguousItem.Ref); err != nil {
		return nil, fmt.Errorf("observe U20 ambiguous call remains unresolved: %w", err)
	}
	u20Digest := uci1GraphEvidenceDigest("U20", publication, ambiguousItem, ambiguousSearch, ambiguousByName, ambiguousCallerGraph)

	publication, wrote, err = uci1GraphWriteAndPublish(ctx, runtime.ClientA, selection, publication, fixturePath, []byte(uci1GraphMalformedSource()))
	mutated = mutated || wrote
	if err != nil {
		return nil, fmt.Errorf("publish U21 malformed graph fixture: %w", err)
	}
	selection.runID = publication.runID
	selection.viewID = publication.viewID
	malformedItem, malformedSearch, err := uci1GraphFindItem(ctx, runtime.ClientA, selection, publication, runtime.Request.Fixture.RelativePath, uci1GraphMalformedFresh)
	if err != nil {
		return nil, fmt.Errorf("observe U21 fresh partial through installed search: %w", err)
	}
	if malformedSearch.Status != uci.QueryStatusPartial || malformedSearch.Coverage == nil || malformedSearch.Coverage.Structural != uci.IndexCoveragePartial {
		return nil, errors.New("U21 malformed source was not exposed as a fresh partial search result")
	}
	if _, err := uci1GraphReadItem(ctx, runtime.ClientA, selection, publication, malformedItem); err != nil {
		return nil, fmt.Errorf("observe U21 fresh partial through installed read: %w", err)
	}
	malformedGraph, err := uci1GraphCall(ctx, runtime.ClientA, selection, publication, uci1GraphCallInput{action: "neighbors", target: uci1GraphTarget(malformedItem.Ref), direction: "outgoing", maxDepth: 4, maxVisited: 64, maxNodes: 16, maxEdges: 32})
	if err != nil {
		return nil, fmt.Errorf("observe U21 fresh partial graph: %w", err)
	}
	if malformedGraph.Graph == nil || (malformedGraph.Status != uci.QueryStatusOK && malformedGraph.Status != uci.QueryStatusPartial && malformedGraph.Status != uci.QueryStatusEmpty) {
		return nil, errors.New("U21 malformed graph response is not bounded")
	}
	oldGraph, err := uci1GraphCall(ctx, runtime.ClientA, selection, publication, uci1GraphCallInput{
		action: "explain",
		target: map[string]any{
			"source_id": publication.sourceID,
			"view_id":   publication.viewID,
			"name":      runtime.Request.Fixture.SharedSymbol,
		},
		direction:  "both",
		maxDepth:   4,
		maxVisited: 64,
		maxNodes:   16,
		maxEdges:   32,
	})
	if err != nil {
		return nil, fmt.Errorf("observe U21 no stale current graph edge: %w", err)
	}
	if oldGraph.Graph == nil || len(oldGraph.Graph.Edges) != 0 {
		return nil, errors.New("U21 malformed current View retained a stale graph edge")
	}
	u21Digest := uci1GraphEvidenceDigest("U21", publication, malformedItem, malformedSearch, malformedGraph, oldGraph)

	publication, wrote, err = uci1GraphWriteAndPublish(ctx, runtime.ClientA, selection, publication, fixturePath, []byte(uci1GraphDenseCycleSource()))
	mutated = mutated || wrote
	if err != nil {
		return nil, fmt.Errorf("publish U29 dense cyclic graph fixture: %w", err)
	}
	selection.runID = publication.runID
	selection.viewID = publication.viewID
	cycleItem, cycleSearch, err := uci1GraphFindItem(ctx, runtime.ClientA, selection, publication, runtime.Request.Fixture.RelativePath, uci1GraphCycleEntry)
	if err != nil {
		return nil, fmt.Errorf("observe U29 dense cycle through installed search: %w", err)
	}
	if _, err := uci1GraphReadItem(ctx, runtime.ClientA, selection, publication, cycleItem); err != nil {
		return nil, fmt.Errorf("observe U29 dense cycle through installed read: %w", err)
	}
	cappedGraph, err := uci1GraphCall(ctx, runtime.ClientA, selection, publication, uci1GraphCallInput{action: "flow", target: uci1GraphTarget(cycleItem.Ref), direction: "outgoing", maxDepth: 8, maxVisited: 64, maxNodes: 2, maxEdges: 1})
	if err != nil {
		return nil, fmt.Errorf("observe U29 capped installed graph: %w", err)
	}
	if err := uci1GraphRequireCapped(cappedGraph, 2, 1); err != nil {
		return nil, fmt.Errorf("observe U29 bounded partial graph: %w", err)
	}
	u29Digest := uci1GraphEvidenceDigest("U29", publication, cycleItem, cycleSearch, cappedGraph)

	restoredPublication, wrote, err := uci1GraphWriteAndPublish(ctx, runtime.ClientA, selection, publication, fixturePath, baseline)
	mutated = mutated || wrote
	if err != nil {
		return nil, fmt.Errorf("restore installed graph fixture publication: %w", err)
	}
	selection.runID = restoredPublication.runID
	selection.viewID = restoredPublication.viewID
	restored = true
	if _, err := uciObserveInstalledAcceptanceSearchGraphRead(ctx, runtime.ClientA, selection, restoredPublication, runtime.Request.Fixture, runtime.Request.Fixture.PrimaryCallee); err != nil {
		return nil, fmt.Errorf("verify restored installed graph publication: %w", err)
	}

	return map[string]uciInstalledAcceptanceScenarioEvidence{
		"U20": {Code: uciInstalledAcceptanceScenarioCodeObserved, Digest: u20Digest},
		"U21": {Code: uciInstalledAcceptanceScenarioCodeObserved, Digest: u21Digest},
		"U29": {Code: uciInstalledAcceptanceScenarioCodeObserved, Digest: u29Digest},
	}, nil
}

func uci1GraphCurrentPublication(ctx context.Context, client *uciInstalledAcceptanceMCPClient, selection uciInstalledAcceptanceSelection) (uciInstalledAcceptanceSelection, uciInstalledAcceptancePublication, error) {
	status, err := uciInstalledAcceptanceStatusForSelection(ctx, client, selection)
	if err != nil {
		return uciInstalledAcceptanceSelection{}, uciInstalledAcceptancePublication{}, err
	}
	if status.runID == "" {
		return uciInstalledAcceptanceSelection{}, uciInstalledAcceptancePublication{}, errors.New("installed graph probe status has no active run")
	}
	selection.runID = status.runID
	barrier, err := uciWaitForInstalledAcceptanceBarrier(ctx, client, selection)
	if err != nil {
		return uciInstalledAcceptanceSelection{}, uciInstalledAcceptancePublication{}, err
	}
	publication, err := uciWaitForInstalledAcceptanceQuiescence(ctx, client, selection, barrier)
	if err != nil {
		return uciInstalledAcceptanceSelection{}, uciInstalledAcceptancePublication{}, err
	}
	selection.runID = publication.runID
	selection.viewID = publication.viewID
	return selection, publication, nil
}

func uci1GraphWriteAndPublish(ctx context.Context, client *uciInstalledAcceptanceMCPClient, selection uciInstalledAcceptanceSelection, before uciInstalledAcceptancePublication, fixturePath string, source []byte) (uciInstalledAcceptancePublication, bool, error) {
	if err := os.WriteFile(fixturePath, source, 0o600); err != nil {
		return uciInstalledAcceptancePublication{}, false, err
	}
	publication, err := uciWaitForInstalledAcceptanceWatcherPublication(ctx, client, selection, before)
	if err != nil {
		return uciInstalledAcceptancePublication{}, true, err
	}
	return publication, true, nil
}

func uci1GraphFindItem(ctx context.Context, client *uciInstalledAcceptanceMCPClient, selection uciInstalledAcceptanceSelection, publication uciInstalledAcceptancePublication, relativePath, functionName string) (uci.QueryItem, uci.QueryResponse, error) {
	payload, err := client.Tool(ctx, "codebase_search", map[string]any{
		"context_handle": selection.contextHandle,
		"query":          functionName,
		"path_prefix":    relativePath,
		"limit":          20,
	})
	if err != nil {
		return uci.QueryItem{}, uci.QueryResponse{}, err
	}
	response, err := uciDecodeInstalledAcceptanceQuery(payload)
	if err != nil {
		return uci.QueryItem{}, uci.QueryResponse{}, err
	}
	if (response.Status != uci.QueryStatusOK && response.Status != uci.QueryStatusPartial) || !uciInstalledAcceptanceQueryMatchesPublication(response, publication) || response.Items == nil {
		return uci.QueryItem{}, uci.QueryResponse{}, errors.New("installed graph probe search is not current selected-View evidence")
	}
	var found *uci.QueryItem
	for index := range *response.Items {
		item := &(*response.Items)[index]
		name, nameOK := uciInstalledAcceptanceGoFunctionName(item.Ref.EntityKey)
		if item.Path != relativePath || !nameOK || name != functionName {
			continue
		}
		if item.Ref.SourceID != publication.sourceID || item.Ref.ViewID != publication.viewID || !uciInstalledAcceptanceIsBareSHA256(string(item.ContentDigest)) {
			return uci.QueryItem{}, uci.QueryResponse{}, errors.New("installed graph probe search returned an invalid current citation")
		}
		if found != nil && found.Ref != item.Ref {
			return uci.QueryItem{}, uci.QueryResponse{}, errors.New("installed graph probe search returned an ambiguous caller citation")
		}
		found = item
	}
	if found == nil {
		return uci.QueryItem{}, uci.QueryResponse{}, errors.New("installed graph probe search omitted the current fixture function")
	}
	return *found, response, nil
}

func uci1GraphReadItem(ctx context.Context, client *uciInstalledAcceptanceMCPClient, selection uciInstalledAcceptanceSelection, publication uciInstalledAcceptancePublication, item uci.QueryItem) (uci.QueryResponse, error) {
	payload, err := client.Tool(ctx, "codebase_read", map[string]any{
		"context_handle": selection.contextHandle,
		"ref":            uci1GraphTarget(item.Ref),
		"span": map[string]any{
			"byte_start": item.Span.ByteStart,
			"byte_end":   item.Span.ByteEnd,
			"line_start": item.Span.LineStart,
			"line_end":   item.Span.LineEnd,
		},
		"content_digest":      string(item.ContentDigest),
		"verify_working_copy": false,
		"max_bytes":           8_192,
	})
	if err != nil {
		return uci.QueryResponse{}, err
	}
	response, err := uciDecodeInstalledAcceptanceQuery(payload)
	if err != nil {
		return uci.QueryResponse{}, err
	}
	if (response.Status != uci.QueryStatusOK && response.Status != uci.QueryStatusPartial) || !uciInstalledAcceptanceQueryMatchesPublication(response, publication) || response.Items == nil || len(*response.Items) != 1 {
		return uci.QueryResponse{}, errors.New("installed graph probe read is not one current selected-View item")
	}
	read := (*response.Items)[0]
	if read.Ref != item.Ref || read.Span != item.Span || read.ContentDigest != item.ContentDigest {
		return uci.QueryResponse{}, errors.New("installed graph probe read did not preserve its search citation")
	}
	return response, nil
}

func uci1GraphCall(ctx context.Context, client *uciInstalledAcceptanceMCPClient, selection uciInstalledAcceptanceSelection, publication uciInstalledAcceptancePublication, input uci1GraphCallInput) (uci.QueryResponse, error) {
	action, target, direction := input.action, input.target, input.direction
	maxDepth, maxVisited, maxNodes, maxEdges := input.maxDepth, input.maxVisited, input.maxNodes, input.maxEdges
	payload, err := client.Tool(ctx, "codebase_graph", map[string]any{
		"context_handle": selection.contextHandle,
		"action":         action,
		"target":         target,
		"direction":      direction,
		"relations":      []string{"calls"},
		"max_depth":      maxDepth,
		"max_visited":    maxVisited,
		"max_nodes":      maxNodes,
		"max_edges":      maxEdges,
		"deadline_ms":    30_000,
	})
	if err != nil {
		return uci.QueryResponse{}, err
	}
	response, err := uciDecodeInstalledAcceptanceQuery(payload)
	if err != nil {
		return uci.QueryResponse{}, err
	}
	if !uciInstalledAcceptanceQueryMatchesPublication(response, publication) || response.Graph == nil || response.Coverage == nil {
		return uci.QueryResponse{}, errors.New("installed graph probe response is not current selected-View graph evidence")
	}
	return response, nil
}

func uci1GraphTarget(ref uci.QueryEntityRef) map[string]any {
	return map[string]any{
		"source_id":  ref.SourceID,
		"view_id":    ref.ViewID,
		"entity_key": ref.EntityKey,
	}
}

func uci1GraphRequireAmbiguousName(response uci.QueryResponse) error {
	if response.Status != uci.QueryStatusPartial || response.Graph == nil || response.Graph.StopReason != uci.QueryGraphComplete || response.Truncated == nil || *response.Truncated || !uci1GraphHasWarning(response, "graph_outcome:ambiguous") {
		return errors.New("same-name target did not disclose ambiguity")
	}
	if len(response.Graph.Nodes) != 2 || len(response.Graph.Edges) != 0 {
		return errors.New("same-name target fabricated a unique graph")
	}
	for _, node := range response.Graph.Nodes {
		if !strings.Contains(node.EntityKey, "method:") || !strings.HasSuffix(node.EntityKey, "."+uci1GraphAmbiguousName) {
			return errors.New("same-name target returned a non-method candidate")
		}
	}
	return nil
}

func uci1GraphRequireUnresolvedCaller(response uci.QueryResponse, caller uci.QueryEntityRef) error {
	if response.Graph == nil || (response.Status != uci.QueryStatusOK && response.Status != uci.QueryStatusPartial) ||
		(response.Graph.StopReason != uci.QueryGraphComplete && response.Graph.StopReason != uci.QueryGraphCoverageGap) {
		return errors.New("ambiguous caller graph response is not bounded")
	}
	for _, edge := range response.Graph.Edges {
		if edge.Relation == uci.IndexRelation("calls") && edge.From == caller {
			return errors.New("ambiguous caller fabricated a calls edge")
		}
	}
	return nil
}

func uci1GraphRequirePartialUnknown(response uci.QueryResponse, stop uci.QueryGraphStopReason) error {
	if response.Status != uci.QueryStatusPartial || response.Graph == nil || response.Graph.StopReason != stop || response.Status == uci.QueryStatusEmpty || !uci1GraphHasWarning(response, "graph_outcome:unknown_or_truncated") {
		return errors.New("graph response did not disclose nonconclusive coverage")
	}
	return nil
}

func uci1GraphRequireCapped(response uci.QueryResponse, maxNodes, maxEdges int) error {
	if response.Status != uci.QueryStatusPartial || response.Status == uci.QueryStatusEmpty || response.Graph == nil || response.Graph.StopReason != uci.QueryGraphNodeCap || response.Truncated == nil || !*response.Truncated || !uci1GraphHasWarning(response, "graph_outcome:unknown_or_truncated") {
		return errors.New("capped graph did not disclose partial truncation")
	}
	if len(response.Graph.Nodes) > maxNodes || len(response.Graph.Edges) > maxEdges {
		return errors.New("capped graph exceeded its public bounds")
	}
	return nil
}

func uci1GraphHasWarning(response uci.QueryResponse, want string) bool {
	if response.Warnings == nil {
		return false
	}
	for _, warning := range *response.Warnings {
		if warning == want {
			return true
		}
	}
	return false
}

func uci1GraphEvidenceDigest(scenarioID string, publication uciInstalledAcceptancePublication, item uci.QueryItem, responses ...uci.QueryResponse) string {
	values := []string{
		"engram.uci1-probe-graph/v1",
		scenarioID,
		uciInstalledAcceptanceStringDigest(publication.sourceID),
		uciInstalledAcceptanceStringDigest(publication.checkoutID),
		uciInstalledAcceptanceStringDigest(publication.viewID),
		uciInstalledAcceptanceStringDigest(publication.runID),
		strconv.FormatInt(publication.generation, 10),
		string(item.ContentDigest),
	}
	for _, response := range responses {
		values = append(values, string(response.Status))
		if response.Coverage != nil {
			values = append(values, string(response.Coverage.Structural))
		}
		if response.Graph != nil {
			values = append(values, string(response.Graph.StopReason), strconv.Itoa(len(response.Graph.Nodes)), strconv.Itoa(len(response.Graph.Edges)))
		}
		if response.Truncated != nil {
			values = append(values, strconv.FormatBool(*response.Truncated))
		}
		if response.Warnings != nil {
			values = append(values, (*response.Warnings)...)
		}
	}
	return uciInstalledAcceptanceStringDigest(strings.Join(values, "\x00"))
}

func uci1GraphAmbiguousSource() string {
	return `package fixture

type UCI1GraphAmbiguousContract interface {
	UCI1GraphSameName() string
}

type UCI1GraphLeft struct{}

func (UCI1GraphLeft) UCI1GraphSameName() string { return "left" }

type UCI1GraphRight struct{}

func (UCI1GraphRight) UCI1GraphSameName() string { return "right" }

func UCI1GraphAmbiguousCaller(value UCI1GraphAmbiguousContract) string {
	return value.UCI1GraphSameName()
}
`
}

func uci1GraphMalformedSource() string {
	return `package fixture

func UCI1GraphMalformedFresh() string { return "UCI1_GRAPH_MALFORMED_FRESH" }

func UCI1GraphMalformedBroken( {
`
}

func uci1GraphDenseCycleSource() string {
	var builder strings.Builder
	builder.WriteString("package fixture\n\n")
	for index := 0; index < uci1GraphCycleCount; index++ {
		first := (index + 1) % uci1GraphCycleCount
		second := (index + 2) % uci1GraphCycleCount
		third := (index + 3) % uci1GraphCycleCount
		fmt.Fprintf(&builder, "func UCI1GraphCycle%d() string { return UCI1GraphCycle%d() + UCI1GraphCycle%d() + UCI1GraphCycle%d() }\n", index, first, second, third)
	}
	return builder.String()
}
