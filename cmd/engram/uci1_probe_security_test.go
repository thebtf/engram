package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/thebtf/engram/internal/uci"
)

// uci1ProbeSecurityInstalled observes only live installed-client security
// outcomes. It deliberately leaves an ID absent when the installed public seam
// cannot produce its required closed outcome.
func uci1ProbeSecurityInstalled(ctx context.Context, runtime uciInstalledAcceptanceScenarioRuntime) (map[string]uciInstalledAcceptanceScenarioEvidence, error) {
	if ctx == nil || runtime.Authority == nil || runtime.ClientA == nil || runtime.ClientB == nil {
		return nil, errors.New("installed security probe runtime is incomplete")
	}

	u22Digest, err := uci1SecurityObserveAuthorizationHops(ctx, runtime)
	if err != nil {
		return nil, err
	}
	u23Digest, err := uci1SecurityObserveProtectedSource(ctx, runtime)
	if err != nil {
		return nil, fmt.Errorf("installed security U23 protected source: %w", err)
	}

	evidence := map[string]uciInstalledAcceptanceScenarioEvidence{
		"U23": {Code: uciInstalledAcceptanceScenarioCodeObserved, Digest: u23Digest},
	}
	if u22Digest != "" {
		evidence["U22"] = uciInstalledAcceptanceScenarioEvidence{Code: uciInstalledAcceptanceScenarioCodeObserved, Digest: u22Digest}
	}
	if u36Digest, observed, err := uci1SecurityObserveNoCWDAmbiguity(ctx, runtime); err != nil {
		return nil, fmt.Errorf("installed security U36 no-CWD ambiguity: %w", err)
	} else if observed {
		evidence["U36"] = uciInstalledAcceptanceScenarioEvidence{Code: uciInstalledAcceptanceScenarioCodeObserved, Digest: u36Digest}
	}
	return evidence, nil
}

type uci1SecurityCitation struct {
	ref           uci.QueryEntityRef
	span          uci.QuerySpan
	contentDigest uci.QueryContentDigest
}

func uci1SecurityObserveAuthorizationHops(ctx context.Context, runtime uciInstalledAcceptanceScenarioRuntime) (string, error) {
	selection, found := runtime.Selections[uciInstalledAcceptanceClientA]
	if !found || selection.contextHandle == "" {
		return "", errors.New("installed security authorization probe has no primary selection")
	}
	primary, found := runtime.Publications[uciInstalledAcceptanceClientA]
	if !found || primary.sourceID == "" || primary.viewID == "" {
		return "", errors.New("installed security authorization probe has no primary publication")
	}
	linked, found := runtime.Publications[uciInstalledAcceptanceClientB]
	if !found || linked.sourceID == "" || linked.viewID == "" {
		return "", errors.New("installed security authorization probe has no linked publication")
	}
	citation, err := uci1SecurityFindCitation(ctx, runtime.ClientA, selection, runtime.Request.Fixture)
	if err != nil {
		return "", fmt.Errorf("installed security citation search: %w", err)
	}
	page, err := uci1SecurityFirstGraphPage(ctx, runtime.ClientA, selection, primary, citation)
	if err != nil {
		return "", fmt.Errorf("installed security first graph page: %w", err)
	}
	if page.Continuation == nil || page.Continuation.Value == nil || *page.Continuation.Value == "" {
		return "", nil
	}

	beforeHop, err := uciInstalledAcceptanceExposureCount(ctx, runtime.Authority)
	if err != nil {
		return "", err
	}
	hop, err := runtime.ClientA.ToolWithCall(ctx, "codebase_graph", map[string]any{
		"context_handle": selection.contextHandle,
		"action":         "path",
		"target": map[string]any{
			"source_id":  citation.ref.SourceID,
			"view_id":    citation.ref.ViewID,
			"entity_key": citation.ref.EntityKey,
		},
		"destination": map[string]any{
			"source_id":  linked.sourceID,
			"view_id":    linked.viewID,
			"entity_key": citation.ref.EntityKey,
		},
		"max_depth":   1,
		"max_visited": 2,
		"max_nodes":   1,
		"max_edges":   1,
		"deadline_ms": 1_000,
	})
	if err != nil {
		return "", err
	}
	if err := uci1SecurityRequireClosed(hop, "context_required", "CONTEXT_MISMATCH"); err != nil {
		return "", fmt.Errorf("installed security cross-view graph hop: %w", err)
	}
	afterHop, err := uciInstalledAcceptanceExposureCount(ctx, runtime.Authority)
	if err != nil {
		return "", err
	}
	if afterHop != beforeHop {
		return "", errors.New("installed security cross-view graph hop appended exposure evidence")
	}

	beforeRevocation, err := uciInstalledAcceptanceExposureCount(ctx, runtime.Authority)
	if err != nil {
		return "", err
	}
	revokedPrincipal := fmt.Sprintf("agent/uci1-security-revoked-%x", time.Now().UnixNano())
	err = uciWithInstalledAcceptanceTokenPrincipal(ctx, runtime.Authority, revokedPrincipal, func() error {
		requests := []struct {
			name string
			args map[string]any
		}{
			{
				name: "search",
				args: map[string]any{
					"context_handle": selection.contextHandle,
					"query":          runtime.Request.Fixture.SharedSymbol,
					"path_prefix":    "pkg",
					"limit":          1,
				},
			},
			{
				name: "read",
				args: map[string]any{
					"context_handle": selection.contextHandle,
					"ref": map[string]any{
						"source_id":  citation.ref.SourceID,
						"view_id":    citation.ref.ViewID,
						"entity_key": citation.ref.EntityKey,
					},
					"span": map[string]any{
						"byte_start": citation.span.ByteStart,
						"byte_end":   citation.span.ByteEnd,
						"line_start": citation.span.LineStart,
						"line_end":   citation.span.LineEnd,
					},
					"content_digest":      string(citation.contentDigest),
					"verify_working_copy": false,
					"max_bytes":           8_192,
				},
			},
			{
				name: "graph",
				args: map[string]any{
					"context_handle": selection.contextHandle,
					"action":         "neighbors",
					"target": map[string]any{
						"source_id":  citation.ref.SourceID,
						"view_id":    citation.ref.ViewID,
						"entity_key": citation.ref.EntityKey,
					},
					"max_depth":   1,
					"max_visited": 2,
					"max_nodes":   1,
					"max_edges":   1,
					"deadline_ms": 1_000,
				},
			},
			{
				name: "graph continuation",
				args: map[string]any{
					"context_handle": selection.contextHandle,
					"action":         "neighbors",
					"target": map[string]any{
						"source_id":  citation.ref.SourceID,
						"view_id":    citation.ref.ViewID,
						"entity_key": citation.ref.EntityKey,
					},
					"max_depth":    1,
					"max_visited":  2,
					"max_nodes":    1,
					"max_edges":    1,
					"deadline_ms":  1_000,
					"continuation": *page.Continuation.Value,
				},
			},
		}
		for _, request := range requests {
			result, requestErr := runtime.ClientA.ToolWithCall(ctx, "codebase_"+strings.Fields(request.name)[0], request.args)
			if request.name == "graph continuation" {
				result, requestErr = runtime.ClientA.ToolWithCall(ctx, "codebase_graph", request.args)
			}
			if requestErr != nil {
				return fmt.Errorf("installed security revoked %s transport: %w", request.name, requestErr)
			}
			if err := uci1SecurityRequireClosed(result, "forbidden", "PERMISSION_DENIED"); err != nil {
				return fmt.Errorf("installed security revoked %s: %w", request.name, err)
			}
		}
		return nil
	})
	if err != nil {
		return "", err
	}
	afterRevocation, err := uciInstalledAcceptanceExposureCount(ctx, runtime.Authority)
	if err != nil {
		return "", err
	}
	if afterRevocation != beforeRevocation {
		return "", errors.New("installed security revoked requests appended exposure evidence")
	}
	return uciInstalledAcceptanceStringDigest("U22|SEARCH|GRAPH|READ|PAGINATION|HOP|PERMISSION_DENIED|CONTEXT_MISMATCH"), nil
}

func uci1SecurityFindCitation(ctx context.Context, client *uciInstalledAcceptanceMCPClient, selection uciInstalledAcceptanceSelection, fixture uciInstalledAcceptanceFixture) (uci1SecurityCitation, error) {
	payload, err := client.Tool(ctx, "codebase_search", map[string]any{
		"context_handle": selection.contextHandle,
		"query":          fixture.SharedSymbol,
		"path_prefix":    "pkg",
		"limit":          10,
	})
	if err != nil {
		return uci1SecurityCitation{}, err
	}
	response, err := uciDecodeInstalledAcceptanceQuery(payload)
	if err != nil || response.Items == nil {
		return uci1SecurityCitation{}, errors.New("installed security citation search returned no items")
	}
	for _, item := range *response.Items {
		name, ok := uciInstalledAcceptanceGoFunctionName(item.Ref.EntityKey)
		if item.Path == fixture.RelativePath && ok && name == fixture.SharedSymbol {
			return uci1SecurityCitation{ref: item.Ref, span: item.Span, contentDigest: item.ContentDigest}, nil
		}
	}
	return uci1SecurityCitation{}, errors.New("installed security citation search omitted fixture symbol")
}

func uci1SecurityFirstGraphPage(ctx context.Context, client *uciInstalledAcceptanceMCPClient, selection uciInstalledAcceptanceSelection, publication uciInstalledAcceptancePublication, citation uci1SecurityCitation) (uci.QueryResponse, error) {
	payload, err := client.Tool(ctx, "codebase_graph", map[string]any{
		"context_handle": selection.contextHandle,
		"action":         "neighbors",
		"target": map[string]any{
			"source_id":  citation.ref.SourceID,
			"view_id":    citation.ref.ViewID,
			"entity_key": citation.ref.EntityKey,
		},
		"max_depth":   1,
		"max_visited": 2,
		"max_nodes":   2,
		"max_edges":   1,
		"deadline_ms": 1_000,
	})
	if err != nil {
		return uci.QueryResponse{}, err
	}
	response, err := uciDecodeInstalledAcceptanceQuery(payload)
	if err != nil || response.Graph == nil || !uciInstalledAcceptanceQueryMatchesPublication(response, publication) {
		return uci.QueryResponse{}, errors.New("installed security graph page is not selected-View evidence")
	}
	return response, nil
}

func uci1SecurityRequireClosed(result uciInstalledAcceptanceMCPToolResult, statusValue, code string) error {
	if result.isError {
		if uciInstalledAcceptancePublicErrorCode(result.payload) == code {
			return nil
		}
		return errors.New("installed security refusal returned an unexpected protocol-level tool error")
	}
	outcome, err := uciDecodeInstalledAcceptanceClosedOutcome(result.payload)
	if err != nil {
		text := strings.TrimSpace(string(result.payload))
		if text == code || (code == string(uci.ContextRequired) && text == "context is required") {
			return nil
		}
		return err
	}
	if outcome.Status != statusValue || outcome.ErrorCode != code {
		return errors.New("installed security refusal returned an unexpected closed outcome")
	}
	return nil
}

func uci1SecurityObserveProtectedSource(ctx context.Context, runtime uciInstalledAcceptanceScenarioRuntime) (_ string, retErr error) {
	selection, found := runtime.Selections[uciInstalledAcceptanceClientA]
	if !found || selection.contextHandle == "" || runtime.Worktrees.primaryRoot == "" {
		return "", errors.New("installed security protected-source probe has no primary worktree")
	}

	suffix := fmt.Sprintf("%x", time.Now().UnixNano())
	secretMarker := "uci1-security-hidden-" + suffix
	secretBody := "api_key=" + strings.Repeat("a", 36) + "\n" + secretMarker + "\n"
	secretRelativePath := ".env.uci1-security-" + suffix
	secretPath := filepath.Join(runtime.Worktrees.primaryRoot, secretRelativePath)
	hostileRelativePath := "uci1-hostile-" + suffix + ".md"
	hostilePath := filepath.Join(runtime.Worktrees.primaryRoot, hostileRelativePath)

	listener, err := net.Listen("tcp", net.JoinHostPort(runtime.Request.LoopbackHost, "0"))
	if err != nil {
		return "", fmt.Errorf("open installed security source canary listener: %w", err)
	}
	mutated := false
	defer func() {
		cleanupErr := listener.Close()
		if mutated {
			cleanupErr = errors.Join(cleanupErr, os.Remove(secretPath), os.Remove(hostilePath))
			cleanupCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			_, cleanupErr = uci1SecurityIndexAndWait(cleanupCtx, runtime.ClientA, selection, runtime.Worktrees.primaryRoot, cleanupErr)
		}
		retErr = errors.Join(retErr, cleanupErr)
	}()

	hostileURL := "http://" + listener.Addr().String() + "/uci1-hostile"
	if err := os.WriteFile(secretPath, []byte(secretBody), 0o600); err != nil {
		return "", fmt.Errorf("write installed security protected source: %w", err)
	}
	mutated = true
	if err := os.WriteFile(hostilePath, []byte("# <script>uci1-hostile-label</script>\n[remote]("+hostileURL+")\n"), 0o600); err != nil {
		return "", fmt.Errorf("write installed security hostile source: %w", err)
	}

	publication, err := uci1SecurityIndexAndWait(ctx, runtime.ClientA, selection, runtime.Worktrees.primaryRoot, nil)
	if err != nil {
		return "", err
	}
	if err := uci1SecurityAssertProtectedMetadata(ctx, runtime.Authority, publication, secretRelativePath, secretBody); err != nil {
		return "", err
	}

	payload, err := runtime.ClientA.Tool(ctx, "codebase_search", map[string]any{
		"context_handle": selection.contextHandle,
		"query":          secretMarker,
		"limit":          10,
	})
	if err != nil {
		return "", err
	}
	if _, err := uciDecodeInstalledAcceptanceQuery(payload); err != nil {
		return "", err
	}
	if strings.Contains(string(payload), secretBody) || strings.Contains(string(payload), secretMarker) || strings.Contains(string(payload), secretRelativePath) {
		return "", errors.New("installed security protected source leaked into public search payload")
	}
	if err := listener.(*net.TCPListener).SetDeadline(time.Now().Add(100 * time.Millisecond)); err != nil {
		return "", err
	}
	connection, acceptErr := listener.Accept()
	if acceptErr == nil {
		_ = connection.Close()
		return "", errors.New("installed security hostile source initiated a network request")
	}
	var networkErr net.Error
	if !errors.As(acceptErr, &networkErr) || !networkErr.Timeout() {
		return "", fmt.Errorf("observe installed security hostile source listener: %w", acceptErr)
	}
	return uciInstalledAcceptanceStringDigest("U23|PROTECTED_METADATA_ONLY|NO_SOURCE_NETWORK|NO_SECRET_PAYLOAD"), nil
}

func uci1SecurityIndexAndWait(ctx context.Context, client *uciInstalledAcceptanceMCPClient, selection uciInstalledAcceptanceSelection, root string, priorErr error) (uciInstalledAcceptancePublication, error) {
	if priorErr != nil {
		return uciInstalledAcceptancePublication{}, priorErr
	}
	payload, err := client.Tool(ctx, "codebase_index", map[string]any{"context_handle": selection.contextHandle, "root": root})
	if err != nil {
		return uciInstalledAcceptancePublication{}, err
	}
	var started struct {
		Status string `json:"status"`
		RunID  string `json:"run_id"`
	}
	if err := json.Unmarshal(payload, &started); err != nil || (started.Status != "started" && started.Status != "already_running") || started.RunID == "" {
		return uciInstalledAcceptancePublication{}, errors.New("installed security index did not return a run identity")
	}
	selection.runID = started.RunID
	publication, err := uciWaitForInstalledAcceptanceBarrier(ctx, client, selection)
	if err != nil {
		return uciInstalledAcceptancePublication{}, err
	}
	return uciWaitForInstalledAcceptanceQuiescence(ctx, client, selection, publication)
}

func uci1SecurityAssertProtectedMetadata(ctx context.Context, authority *uciInstalledAcceptanceAuthority, publication uciInstalledAcceptancePublication, relativePath, secretBody string) error {
	if authority == nil || authority.store == nil || publication.sourceID == "" || publication.checkoutID == "" || publication.generation < 1 {
		return errors.New("installed security protected metadata authority is incomplete")
	}
	db := authority.store.GetDB().WithContext(ctx)
	var publishedArtifact int64
	if err := db.Raw(`
		SELECT COUNT(*)
		FROM ci_memberships
		WHERE source_id = ? AND checkout_id = ? AND path_key = ?
			AND valid_from_generation <= ? AND (valid_to_generation IS NULL OR valid_to_generation > ?)
			AND (file_state <> 'excluded' OR artifact_id IS NOT NULL)`,
		publication.sourceID, publication.checkoutID, relativePath, publication.generation, publication.generation,
	).Scan(&publishedArtifact).Error; err != nil {
		return fmt.Errorf("inspect installed security protected membership: %w", err)
	}
	if publishedArtifact != 0 {
		return errors.New("installed security protected source reached a published artifact")
	}

	contentDigest := "sha256:" + uciInstalledAcceptanceStringDigest(secretBody)
	var storedOrRendered int64
	if err := db.Raw(`
		SELECT COUNT(*)
		FROM ci_blobs
		WHERE source_id = ? AND content_digest = ?
			AND (storage_state = 'stored' OR safe_content IS NOT NULL)`, publication.sourceID, contentDigest,
	).Scan(&storedOrRendered).Error; err != nil {
		return fmt.Errorf("inspect installed security protected blob: %w", err)
	}
	if storedOrRendered != 0 {
		return errors.New("installed security protected source retained a readable body")
	}
	var parsed int64
	if err := db.Raw(`
		SELECT COUNT(*)
		FROM ci_parse_artifacts AS artifact
		JOIN ci_blobs AS blob ON blob.source_id = artifact.source_id AND blob.blob_id = artifact.blob_id
		WHERE artifact.source_id = ? AND blob.content_digest = ?`, publication.sourceID, contentDigest,
	).Scan(&parsed).Error; err != nil {
		return fmt.Errorf("inspect installed security protected parse state: %w", err)
	}
	if parsed != 0 {
		return errors.New("installed security protected source reached parser or renderer state")
	}
	return nil
}

func uci1SecurityObserveNoCWDAmbiguity(ctx context.Context, runtime uciInstalledAcceptanceScenarioRuntime) (string, bool, error) {
	beforeA, err := uciInstalledAcceptanceDefaultView(ctx, runtime.ClientA)
	if err != nil {
		return "", false, fmt.Errorf("read U36 primary default before probe: %w", err)
	}
	beforeB, err := uciInstalledAcceptanceDefaultView(ctx, runtime.ClientB)
	if err != nil {
		return "", false, fmt.Errorf("read U36 linked default before probe: %w", err)
	}
	process, err := runtime.Installation.Start(ctx, uciInstalledHarnessLaunchRequest{
		Role:             "daemon",
		WorkingDirectory: runtime.Request.FixtureRoot,
		Environment:      runtime.ClientEnvironment,
		WithStdio:        true,
	})
	if err != nil {
		return "", false, err
	}
	client, err := newUCIInstalledAcceptanceMCPClient("client-security-no-cwd", process)
	if err != nil {
		return "", false, err
	}
	if err := client.InitializeAndList(ctx); err != nil {
		return "", false, err
	}
	search, err := client.ToolWithCall(ctx, "codebase_search", map[string]any{"query": "uci1-no-cwd-ambiguity", "limit": 1})
	if err != nil {
		return "", false, err
	}
	if err := uci1SecurityRequireClosed(search, "context_required", "CONTEXT_REQUIRED"); err != nil {
		return "", false, fmt.Errorf("installed security no-CWD search: %w", err)
	}
	mutationClosed, err := uci1SecurityCallClosedText(ctx, client, "codebase_index", map[string]any{"root": runtime.Request.FixtureRoot}, string(uci.ContextRequired), "context is required")
	if err != nil {
		return "", false, err
	}
	afterA, err := uciInstalledAcceptanceDefaultView(ctx, runtime.ClientA)
	if err != nil {
		return "", false, fmt.Errorf("read U36 primary default after probe: %w", err)
	}
	afterB, err := uciInstalledAcceptanceDefaultView(ctx, runtime.ClientB)
	if err != nil {
		return "", false, fmt.Errorf("read U36 linked default after probe: %w", err)
	}
	if beforeA != afterA || beforeB != afterB {
		return "", false, errors.New("installed security no-CWD client changed an established checkout selection")
	}
	if !mutationClosed {
		return "", false, nil
	}
	return uciInstalledAcceptanceStringDigest("U36|NO_CWD|SEARCH|MUTATION|CONTEXT_REQUIRED|NO_ARBITRARY_SELECTION"), true, nil
}

func uci1SecurityCallClosedText(ctx context.Context, client *uciInstalledAcceptanceMCPClient, name string, arguments map[string]any, accepted ...string) (bool, error) {
	params, err := json.Marshal(map[string]any{"name": name, "arguments": arguments})
	if err != nil {
		return false, err
	}
	raw, _, err := client.call(ctx, "tools/call", json.RawMessage(params))
	if err != nil {
		return false, err
	}
	var envelope struct {
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
		IsError bool `json:"isError"`
	}
	if err := json.Unmarshal(raw, &envelope); err != nil || len(envelope.Content) != 1 || envelope.Content[0].Type != "text" {
		return false, errors.New("installed security closed tool response is invalid")
	}
	text := strings.TrimSpace(envelope.Content[0].Text)
	for _, value := range accepted {
		if text == value {
			return true, nil
		}
	}
	return false, nil
}
