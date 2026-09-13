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

type uci1GraphFixtureState struct {
	selection   uciInstalledAcceptanceSelection
	publication uciInstalledAcceptancePublication
	path        string
	baseline    []byte
	mutated     bool
	restored    bool
}

// uci1ProbeGraphInstalled observes the graph contract through the live installed
// MCP client. Each source mutation is published before it is queried and the
// original fixture bytes are republished before the callback returns.
func uci1ProbeGraphInstalled(ctx context.Context, runtime uciInstalledAcceptanceScenarioRuntime) (evidence map[string]uciInstalledAcceptanceScenarioEvidence, retErr error) {
	state, err := uci1GraphPrepareFixture(ctx, runtime)
	if err != nil {
		return nil, err
	}
	defer uci1GraphRestoreFixture(&retErr, runtime, state)

	u20Digest, err := uci1GraphObserveAmbiguity(ctx, runtime, state)
	if err != nil {
		return nil, err
	}
	u21Digest, err := uci1GraphObserveMalformed(ctx, runtime, state)
	if err != nil {
		return nil, err
	}
	u29Digest, err := uci1GraphObserveDenseCycle(ctx, runtime, state)
	if err != nil {
		return nil, err
	}

	if err := state.restore(ctx, runtime.ClientA); err != nil {
		return nil, fmt.Errorf("restore installed graph fixture publication: %w", err)
	}
	if _, err := uciObserveInstalledAcceptanceSearchGraphRead(ctx, runtime.ClientA, state.selection, state.publication, runtime.Request.Fixture, runtime.Request.Fixture.PrimaryCallee); err != nil {
		return nil, fmt.Errorf("verify restored installed graph publication: %w", err)
	}
	state.restored = true

	return map[string]uciInstalledAcceptanceScenarioEvidence{
		"U20": {Code: uciInstalledAcceptanceScenarioCodeObserved, Digest: u20Digest},
		"U21": {Code: uciInstalledAcceptanceScenarioCodeObserved, Digest: u21Digest},
		"U29": {Code: uciInstalledAcceptanceScenarioCodeObserved, Digest: u29Digest},
	}, nil
}

func uci1GraphPrepareFixture(ctx context.Context, runtime uciInstalledAcceptanceScenarioRuntime) (*uci1GraphFixtureState, error) {
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
	path := filepath.Join(runtime.Worktrees.primaryRoot, filepath.FromSlash(runtime.Request.Fixture.RelativePath))
	baseline, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read installed graph fixture baseline: %w", err)
	}
	selection, publication, err := uci1GraphCurrentPublication(ctx, runtime.ClientA, selection)
	if err != nil {
		return nil, err
	}
	return &uci1GraphFixtureState{selection: selection, publication: publication, path: path, baseline: baseline}, nil
}

func (state *uci1GraphFixtureState) publish(ctx context.Context, client *uciInstalledAcceptanceMCPClient, source []byte) error {
	publication, wrote, err := uci1GraphWriteAndPublish(ctx, client, state.selection, state.publication, state.path, source)
	state.mutated = state.mutated || wrote
	if err != nil {
		return err
	}
	state.publication = publication
	state.selection.runID = publication.runID
	state.selection.viewID = publication.viewID
	return nil
}

func (state *uci1GraphFixtureState) restore(ctx context.Context, client *uciInstalledAcceptanceMCPClient) error {
	return state.publish(ctx, client, state.baseline)
}

func uci1GraphRestoreFixture(retErr *error, runtime uciInstalledAcceptanceScenarioRuntime, state *uci1GraphFixtureState) {
	if !state.mutated || state.restored {
		return
	}
	restoreCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := state.restore(restoreCtx, runtime.ClientA); err != nil {
		*retErr = errors.Join(*retErr, fmt.Errorf("restore installed graph fixture and publication: %w", err))
		return
	}
	if _, err := uciObserveInstalledAcceptanceSearchGraphRead(restoreCtx, runtime.ClientA, state.selection, state.publication, runtime.Request.Fixture, runtime.Request.Fixture.PrimaryCallee); err != nil {
		*retErr = errors.Join(*retErr, fmt.Errorf("restore installed graph fixture and publication: %w", err))
		return
	}
	state.restored = true
}

func uci1GraphObserveAmbiguity(ctx context.Context, runtime uciInstalledAcceptanceScenarioRuntime, state *uci1GraphFixtureState) (string, error) {
	if err := state.publish(ctx, runtime.ClientA, []byte(uci1GraphAmbiguousSource())); err != nil {
		return "", fmt.Errorf("publish U20 ambiguous graph fixture: %w", err)
	}
	item, search, err := uci1GraphFindItem(ctx, runtime.ClientA, state.selection, state.publication, runtime.Request.Fixture.RelativePath, uci1GraphAmbiguousCaller)
	if err != nil {
		return "", fmt.Errorf("observe U20 caller through installed search: %w", err)
	}
	if _, err := uci1GraphReadItem(ctx, runtime.ClientA, state.selection, state.publication, item); err != nil {
		return "", fmt.Errorf("observe U20 caller through installed read: %w", err)
	}
	byName, err := uci1GraphCall(ctx, runtime.ClientA, state.selection, state.publication, uci1GraphCallInput{
		action: "explain",
		target: map[string]any{
			"source_id": state.publication.sourceID,
			"view_id":   state.publication.viewID,
			"name":      uci1GraphAmbiguousName,
		},
		direction:  "both",
		maxDepth:   4,
		maxVisited: 64,
		maxNodes:   16,
		maxEdges:   32,
	})
	if err != nil {
		return "", fmt.Errorf("observe U20 ambiguous installed graph target: %w", err)
	}
	if err := uci1GraphRequireAmbiguousName(byName); err != nil {
		return "", fmt.Errorf("observe U20 ambiguous same-name methods: %w", err)
	}
	callerGraph, err := uci1GraphCall(ctx, runtime.ClientA, state.selection, state.publication, uci1GraphCallInput{action: "neighbors", target: uci1GraphTarget(item.Ref), direction: "outgoing", maxDepth: 4, maxVisited: 64, maxNodes: 16, maxEdges: 32})
	if err != nil {
		return "", fmt.Errorf("observe U20 ambiguous caller graph: %w", err)
	}
	if err := uci1GraphRequireUnresolvedCaller(callerGraph, item.Ref); err != nil {
		return "", fmt.Errorf("observe U20 ambiguous call remains unresolved: %w", err)
	}
	return uci1GraphEvidenceDigest("U20", state.publication, item, search, byName, callerGraph), nil
}

func uci1GraphObserveMalformed(ctx context.Context, runtime uciInstalledAcceptanceScenarioRuntime, state *uci1GraphFixtureState) (string, error) {
	if err := state.publish(ctx, runtime.ClientA, []byte(uci1GraphMalformedSource())); err != nil {
		return "", fmt.Errorf("publish U21 malformed graph fixture: %w", err)
	}
	item, search, err := uci1GraphFindItem(ctx, runtime.ClientA, state.selection, state.publication, runtime.Request.Fixture.RelativePath, uci1GraphMalformedFresh)
	if err != nil {
		return "", fmt.Errorf("observe U21 fresh partial through installed search: %w", err)
	}
	if search.Status != uci.QueryStatusPartial || search.Coverage == nil || search.Coverage.Structural != uci.IndexCoveragePartial {
		return "", errors.New("U21 malformed source was not exposed as a fresh partial search result")
	}
	if _, err := uci1GraphReadItem(ctx, runtime.ClientA, state.selection, state.publication, item); err != nil {
		return "", fmt.Errorf("observe U21 fresh partial through installed read: %w", err)
	}
	graph, err := uci1GraphCall(ctx, runtime.ClientA, state.selection, state.publication, uci1GraphCallInput{action: "neighbors", target: uci1GraphTarget(item.Ref), direction: "outgoing", maxDepth: 4, maxVisited: 64, maxNodes: 16, maxEdges: 32})
	if err != nil {
		return "", fmt.Errorf("observe U21 fresh partial graph: %w", err)
	}
	if graph.Graph == nil || (graph.Status != uci.QueryStatusOK && graph.Status != uci.QueryStatusPartial && graph.Status != uci.QueryStatusEmpty) {
		return "", errors.New("U21 malformed graph response is not bounded")
	}
	oldGraph, err := uci1GraphCall(ctx, runtime.ClientA, state.selection, state.publication, uci1GraphCallInput{
		action: "explain",
		target: map[string]any{
			"source_id": state.publication.sourceID,
			"view_id":   state.publication.viewID,
			"name":      runtime.Request.Fixture.SharedSymbol,
		},
		direction:  "both",
		maxDepth:   4,
		maxVisited: 64,
		maxNodes:   16,
		maxEdges:   32,
	})
	if err != nil {
		return "", fmt.Errorf("observe U21 no stale current graph edge: %w", err)
	}
	if oldGraph.Graph == nil || len(oldGraph.Graph.Edges) != 0 {
		return "", errors.New("U21 malformed current View retained a stale graph edge")
	}
	return uci1GraphEvidenceDigest("U21", state.publication, item, search, graph, oldGraph), nil
}

func uci1GraphObserveDenseCycle(ctx context.Context, runtime uciInstalledAcceptanceScenarioRuntime, state *uci1GraphFixtureState) (string, error) {
	if err := state.publish(ctx, runtime.ClientA, []byte(uci1GraphDenseCycleSource())); err != nil {
		return "", fmt.Errorf("publish U29 dense cyclic graph fixture: %w", err)
	}
	item, search, err := uci1GraphFindItem(ctx, runtime.ClientA, state.selection, state.publication, runtime.Request.Fixture.RelativePath, uci1GraphCycleEntry)
	if err != nil {
		return "", fmt.Errorf("observe U29 dense cycle through installed search: %w", err)
	}
	if _, err := uci1GraphReadItem(ctx, runtime.ClientA, state.selection, state.publication, item); err != nil {
		return "", fmt.Errorf("observe U29 dense cycle through installed read: %w", err)
	}
	graph, err := uci1GraphCall(ctx, runtime.ClientA, state.selection, state.publication, uci1GraphCallInput{action: "flow", target: uci1GraphTarget(item.Ref), direction: "outgoing", maxDepth: 8, maxVisited: 64, maxNodes: 2, maxEdges: 1})
	if err != nil {
		return "", fmt.Errorf("observe U29 capped installed graph: %w", err)
	}
	if err := uci1GraphRequireCapped(graph, 2, 1); err != nil {
		return "", fmt.Errorf("observe U29 bounded partial graph: %w", err)
	}
	return uci1GraphEvidenceDigest("U29", state.publication, item, search, graph), nil
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
