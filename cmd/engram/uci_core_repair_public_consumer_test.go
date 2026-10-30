package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/thebtf/engram/internal/auth"
	gormdb "github.com/thebtf/engram/internal/db/gorm"
	"github.com/thebtf/engram/internal/uci"
	"golang.org/x/crypto/bcrypt"
)

const uciCoreRepairWindowBytes = int64(8_192)

// Run only after freezing the combined candidate, from that candidate's root:
// ENGRAM_UCI_INSTALLED_TEST_DATABASE_DSN=<disposable loopback test DSN>
// go test ./cmd/engram -run '^TestUCICoreRepairPublicRegistrationSubprocess$' -count=1 -v -timeout=10m
// This reuses the installed harness's Windows process tree, candidate builds,
// isolated PostgreSQL schema, and cleanup; no installed profile is accessed.
func TestUCICoreRepairPublicRegistrationSubprocess(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("the existing installed subprocess lifecycle requires native Windows")
	}
	request := uciInstalledAcceptanceConfiguredRequest(t)
	request.OperationTimeout = 5 * time.Minute
	request.Fixture.SharedSymbol = "CoreRepairOversizedCitation"
	request.Fixture.PrimarySource = "package fixture\n\nfunc CoreRepairOversizedCitation() string { return \"" +
		strings.Repeat("synthetic bounded citation λ evidence ", 700) + "\" }\n"
	provider, providerCalls := uciCoreRepairEmbeddingProvider(t)
	request.EmbeddingProvider = &provider
	t.Setenv("ENGRAM_EMBEDDING_DIMENSIONS", "1536")
	uciInstalledAcceptanceRequireCleanup(t, request)

	ctx, cancel := context.WithTimeout(context.Background(), request.OperationTimeout)
	t.Cleanup(cancel)
	if err := uciValidateInstalledAcceptanceRequest(ctx, request); err != nil {
		t.Fatalf("validate focused public consumer request: %v", err)
	}
	result := uciNewInstalledAcceptanceResult(request)
	installed := &uciInstalledAcceptanceRuntime{
		request: request, result: &result,
		daemonControlRoot: filepath.Join(request.LocalStateRoot, "temp"),
	}
	t.Cleanup(func() {
		var cleanupErr error
		installed.cleanup(&cleanupErr)
		if cleanupErr != nil {
			t.Errorf("clean focused public consumer fixture: %v", cleanupErr)
		}
	})
	if err := installed.setupRootsAndAuthority(ctx); err != nil {
		t.Fatalf("prepare existing isolated subprocess fixture: %v", err)
	}

	// The stock harness provisions an agent-owned, unbound Source. It remains
	// unused in this probe. Issue a distinct human credential, rather than
	// relabelling that agent or changing any existing Source/Checkout owner.
	humanPrincipal := "human/core-repair-" + uuid.NewString()
	human, humanRaw := uciCoreRepairKeycard(t, ctx, installed.authority, humanPrincipal, auth.PrincipalKindHuman)
	installed.authority.token = human
	installed.authority.rawToken = humanRaw
	installed.authority.principal = humanPrincipal
	if err := installed.materialize(ctx); err != nil {
		t.Fatalf("materialize exact candidate subprocesses: %v", err)
	}
	if err := installed.startServer(ctx); err != nil {
		t.Fatalf("start candidate loopback gRPC server: %v", err)
	}
	client, err := installed.startPrimaryClient(ctx)
	if err != nil {
		t.Fatalf("start real human MCP stdio client: %v", err)
	}
	installed.clientA = client
	daemonPID, err := uciWaitForInstalledAcceptanceDaemonPID(ctx, installed.daemonControlRoot, installed.daemonPath)
	if err != nil {
		t.Fatalf("identify disposable candidate daemon: %v", err)
	}
	installed.activeDaemonPID = daemonPID
	result.Processes.DaemonPID = daemonPID
	if !client.Transcript().UsedStdio || daemonPID <= 0 || daemonPID == os.Getpid() ||
		result.Processes.ServerPID <= 0 || result.Processes.ServerPID == os.Getpid() || result.Processes.ServerPID == daemonPID {
		t.Fatal("public consumer did not use distinct candidate server and stdio-daemon processes")
	}
	uciInstalledAcceptanceRequireArtifacts(t, result)

	selection, checkout := uciCoreRepairRegisterHumanCheckout(t, ctx, installed, client)
	indexPayload, err := client.Tool(ctx, "codebase_index", map[string]any{
		"context_handle": selection.contextHandle, "root": installed.worktrees.primaryRoot,
	})
	if err != nil {
		t.Fatalf("start public registered-checkout index: %v", err)
	}
	var index struct {
		Status string `json:"status"`
		RunID  string `json:"run_id"`
	}
	if err := json.Unmarshal(indexPayload, &index); err != nil || index.Status != "started" || index.RunID == "" {
		t.Fatal("public index did not return its actual run identifier")
	}
	selection.runID = index.RunID
	publication, err := uciWaitForInstalledAcceptanceBarrier(ctx, client, selection)
	if err != nil {
		t.Fatalf("publish public registered-checkout index: %v", err)
	}
	if publication.checkoutID != checkout.CheckoutID || publication.sourceID != checkout.SourceID {
		t.Fatal("index published outside the publicly registered human checkout")
	}
	selection = uciCoreRepairPinPublishedContext(t, ctx, client, selection, publication)

	searchPayload, err := client.Tool(ctx, "codebase_search", map[string]any{
		"context_handle": selection.contextHandle, "query": request.Fixture.SharedSymbol,
		"path_prefix": "pkg", "limit": 10,
	})
	if err != nil {
		t.Fatalf("search publicly registered oversized source: %v", err)
	}
	search, err := uciDecodeInstalledAcceptanceQuery(searchPayload)
	if err != nil {
		t.Fatal(err)
	}
	if (search.Status != uci.QueryStatusOK && search.Status != uci.QueryStatusPartial) ||
		!uciInstalledAcceptanceQueryMatchesPublication(search, publication) || search.Items == nil ||
		search.Exposure == nil || !uci.ValidExposureRef(search.Exposure.ExposureRef) {
		t.Fatal("search did not return exposed evidence in the registered immutable context")
	}
	var citation *uci.QueryItem
	for _, item := range *search.Items {
		if item.Path == request.Fixture.RelativePath && item.Span.ByteEnd-item.Span.ByteStart > uciCoreRepairWindowBytes {
			candidate := item
			citation = &candidate
			break
		}
	}
	if citation == nil {
		t.Fatal("actual public search omitted a citation larger than 8192 bytes; fixture is not #514 proof")
	}
	if providerCalls.Load() == 0 {
		t.Fatal("candidate made no observed request to the deterministic loopback embedding provider")
	}
	t.Run("oversized_indexed_span_read", func(t *testing.T) {
		uciCoreRepairReadWindows(t, ctx, client, selection, publication, *citation, request.Fixture.PrimarySource)
	})
	t.Run("owner_authorization_and_grpc_completion", func(t *testing.T) {
		evidence, err := uci1ProbeCompletionInstalled(ctx, uciInstalledAcceptanceScenarioRuntime{
			Request: request, Installation: installed.installation, Authority: installed.authority,
			Worktrees: installed.worktrees, Recorder: client,
			Selections:        map[string]uciInstalledAcceptanceSelection{uciInstalledAcceptanceClientRecorder: selection},
			ServerEnvironment: installed.serverEnvironment, ClientEnvironment: installed.clientEnvironment,
		})
		if err != nil {
			t.Fatalf("real human-parent denial and synthetic service-owner gRPC completion: %v", err)
		}
		for _, id := range []string{"U42", "U45"} {
			if evidence[id].Code != uciInstalledAcceptanceScenarioCodeObserved || evidence[id].Digest == "" {
				t.Fatal("completion probe omitted its exact-retry or append-failure observation")
			}
		}
	})
	ownerAfter, err := gormdb.NewUCIContextStore(installed.authority.store.GetDB()).GetCheckout(ctx, checkout.CheckoutID)
	if err != nil || ownerAfter.OwnerPrincipal != humanPrincipal {
		t.Fatal("completion probe changed the actual public human checkout owner")
	}
}

func uciCoreRepairRegisterHumanCheckout(t *testing.T, ctx context.Context, installed *uciInstalledAcceptanceRuntime, client *uciInstalledAcceptanceMCPClient) (uciInstalledAcceptanceSelection, *gormdb.UCICheckout) {
	t.Helper()
	// First registration uses the daemon's verified installed parser capability;
	// forcing native Go would contradict this fixture's TypeScript canary.
	payload, err := client.Tool(ctx, "codebase_context", map[string]any{
		"action": "register", "source_label": "core-repair-public-" + uuid.NewString(),
	})
	if err != nil {
		t.Fatalf("register through real public human read-write MCP admission: %v", err)
	}
	var registered struct {
		ContextHandle string `json:"context_handle"`
		BindingKind   string `json:"binding_kind"`
		SourceID      string `json:"source_id"`
		CheckoutID    string `json:"checkout_id"`
		ViewID        string `json:"view_id"`
	}
	if err := json.Unmarshal(payload, &registered); err != nil || registered.ContextHandle == "" ||
		registered.BindingKind != "checkout" || registered.SourceID == "" || registered.CheckoutID == "" || registered.ViewID != "" {
		t.Fatal("public registration did not return a fresh server-issued checkout binding")
	}
	contexts := gormdb.NewUCIContextStore(installed.authority.store.GetDB())
	source, err := contexts.GetSource(ctx, registered.SourceID)
	if err != nil {
		t.Fatalf("read public registration Source evidence: %v", err)
	}
	checkout, err := contexts.GetCheckout(ctx, registered.CheckoutID)
	if err != nil {
		t.Fatalf("read public registration Checkout evidence: %v", err)
	}
	if source.SourceID == installed.authority.source.SourceID || source.AuthRealm != string(auth.SourceClient) ||
		source.ProjectID == nil || *source.ProjectID != installed.authority.anchorProjectID ||
		checkout.SourceID != source.SourceID || checkout.OwnerPrincipal != installed.authority.principal ||
		checkout.WorkstationID != installed.authority.token.ID {
		t.Fatal("public admission did not preserve its canonical project, real human owner, and authenticated workstation")
	}
	return uciInstalledAcceptanceSelection{contextHandle: registered.ContextHandle}, checkout
}

func uciCoreRepairReadWindows(t *testing.T, ctx context.Context, client *uciInstalledAcceptanceMCPClient, selection uciInstalledAcceptanceSelection, publication uciInstalledAcceptancePublication, citation uci.QueryItem, source string) {
	t.Helper()
	if citation.Span.ByteStart < 0 || citation.Span.ByteEnd > int64(len(source)) || citation.Span.ByteStart >= citation.Span.ByteEnd {
		t.Fatal("search citation escaped the exact synthetic source")
	}
	for cursor := citation.Span.ByteStart; cursor < citation.Span.ByteEnd; {
		window := citation.Span
		window.ByteStart = cursor
		window.ByteEnd = min(cursor+uciCoreRepairWindowBytes, citation.Span.ByteEnd)
		payload, err := client.Tool(ctx, "codebase_read", map[string]any{
			"context_handle": selection.contextHandle, "ref": citation.Ref,
			"membership_id": citation.MembershipID, "content_digest": string(citation.ContentDigest),
			"indexed_span": citation.Span, "span": window,
			"verify_working_copy": false, "max_bytes": uciCoreRepairWindowBytes,
		})
		if err != nil {
			t.Fatalf("read bounded indexed citation through actual MCP stdio: %v", err)
		}
		read, err := uciDecodeInstalledAcceptanceQuery(payload)
		if err != nil {
			t.Fatal(err)
		}
		if read.Status != uci.QueryStatusOK || !uciInstalledAcceptanceQueryMatchesPublication(read, publication) ||
			read.Items == nil || len(*read.Items) != 1 || read.Exposure == nil || !uci.ValidExposureRef(read.Exposure.ExposureRef) {
			t.Fatal("bounded indexed_span read did not return one exposed item in the original immutable context")
		}
		item := (*read.Items)[0]
		if item.Ref != citation.Ref || item.MembershipID != citation.MembershipID || item.ContentDigest != citation.ContentDigest ||
			item.Path != citation.Path || item.Span.ByteStart != cursor || item.Span.ByteEnd <= cursor || item.Span.ByteEnd > window.ByteEnd ||
			int64(len(item.Excerpt)) != item.Span.ByteEnd-item.Span.ByteStart || len(item.Excerpt) > int(uciCoreRepairWindowBytes) ||
			!utf8.ValidString(item.Excerpt) || item.Excerpt != source[item.Span.ByteStart:item.Span.ByteEnd] {
			t.Fatal("bounded indexed_span read changed citation identity, digest, byte bounds, or stored UTF-8 content")
		}
		cursor = item.Span.ByteEnd
	}
}

func uciCoreRepairKeycard(t *testing.T, ctx context.Context, authority *uciInstalledAcceptanceAuthority, principal string, kind auth.PrincipalKind) (*gormdb.APIToken, string) {
	t.Helper()
	raw, err := uciInstalledAcceptanceKeycard()
	if err != nil {
		t.Fatal("generate disposable consumer credential")
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(raw), bcrypt.DefaultCost)
	if err != nil {
		t.Fatal("hash disposable consumer credential")
	}
	expiresAt := uciInstalledAcceptanceTokenExpiry(ctx, time.Now())
	token, err := gormdb.NewTokenStore(authority.store).CreateWithPrincipal(ctx, gormdb.TokenCreatePrincipalInput{
		Name: "core-repair-public-" + uuid.NewString(), TokenHash: string(hash),
		TokenPrefix: raw[len(auth.TokenRawPrefix) : len(auth.TokenRawPrefix)+auth.TokenPrefixLen],
		Scope:       "read-write", Principal: principal, PrincipalKind: string(kind), ExpiresAt: &expiresAt,
	})
	if err != nil {
		t.Fatal("persist disposable consumer credential")
	}
	return token, raw
}

func uciCoreRepairEmbeddingProvider(t *testing.T) (uciInstalledAcceptanceEmbeddingProvider, *atomic.Int64) {
	t.Helper()
	key, err := uciInstalledAcceptanceKeycard()
	if err != nil {
		t.Fatal("generate local provider fixture credential")
	}
	const model = "core-repair-loopback-1536"
	calls := &atomic.Int64{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			Input      []string `json:"input"`
			Model      string   `json:"model"`
			Dimensions int      `json:"dimensions"`
		}
		if r.Method != http.MethodPost || r.URL.Path != "/v1/embeddings" || r.Header.Get("Authorization") != "Bearer "+key {
			t.Error("candidate embedding request escaped the local provider contract")
			http.Error(w, "invalid local fixture request", http.StatusBadRequest)
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, 2<<20)
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil || request.Model != model || request.Dimensions != 1536 || len(request.Input) == 0 {
			t.Error("candidate embedding request has invalid model, dimension, or synthetic inputs")
			http.Error(w, "invalid local fixture request", http.StatusBadRequest)
			return
		}
		type item struct {
			Object    string    `json:"object"`
			Index     int       `json:"index"`
			Embedding []float32 `json:"embedding"`
		}
		data := make([]item, len(request.Input))
		for index := range data {
			vector := make([]float32, 1536)
			vector[0] = 1
			data[index] = item{Object: "embedding", Index: index, Embedding: vector}
		}
		calls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(struct {
			Object string `json:"object"`
			Model  string `json:"model"`
			Data   []item `json:"data"`
		}{Object: "list", Model: model, Data: data}); err != nil && !errors.Is(err, http.ErrHandlerTimeout) {
			t.Error("write deterministic local embedding response")
		}
	}))
	t.Cleanup(server.Close)
	return uciInstalledAcceptanceEmbeddingProvider{URL: server.URL + "/v1", Model: model, Key: key}, calls
}

// Keep the successful local after_barrier receipt separate from the ordinary
// selected-View reader response: status:null supplies no daemon liveness/run ID.
// A matching View may catch up or become historical while publication advances;
// neither transition changes the immutable tuple or the caller's deadline.
func uciCoreRepairPinPublishedContext(t *testing.T, ctx context.Context, client *uciInstalledAcceptanceMCPClient, selection uciInstalledAcceptanceSelection, publication uciInstalledAcceptancePublication) uciInstalledAcceptanceSelection {
	t.Helper()
	if publication.barrierState != "satisfied" || publication.runID == "" || publication.runID != selection.runID {
		t.Fatal("selected-View reader has no satisfied receipt for the actual requested index run")
	}
	payload, err := client.Tool(ctx, "codebase_context", map[string]any{
		"action": "select", "source_id": publication.sourceID, "checkout_id": publication.checkoutID,
		"view_id": publication.viewID, "analysis_profile_id": publication.profileID, "generation": publication.generation,
	})
	if err != nil {
		t.Fatalf("pin the satisfied publication through public MCP context selection: %v", err)
	}
	var pinned struct {
		ContextHandle string `json:"context_handle"`
		BindingKind   string `json:"binding_kind"`
	}
	if err := json.Unmarshal(payload, &pinned); err != nil || pinned.ContextHandle == "" || pinned.BindingKind != "context" {
		t.Fatal("public context selection did not return a pinned immutable View handle")
	}
	selection.contextHandle = pinned.ContextHandle
	selection.viewID = publication.viewID
	ticker := time.NewTicker(uciInstalledAcceptanceQuiescencePollInterval)
	defer ticker.Stop()
	for {
		statusPayload, err := uciInstalledAcceptanceStatusTool(ctx, client, map[string]any{"context_handle": selection.contextHandle})
		if err != nil {
			t.Fatalf("read the actual pinned publication status: %v", err)
		}
		var reader struct {
			Status    json.RawMessage      `json:"status"`
			RunID     string               `json:"run_id"`
			Error     string               `json:"error"`
			Context   *uci.QueryContextRef `json:"context"`
			Freshness *uci.QueryFreshness  `json:"freshness"`
		}
		if err := json.Unmarshal(statusPayload, &reader); err != nil || strings.TrimSpace(string(reader.Status)) != "null" || reader.RunID != "" || reader.Error != "" {
			t.Fatal("selected-View status is not the successful read-only response; no local idle state or run ID may be inferred")
		}
		if reader.Context == nil || reader.Context.SourceID != publication.sourceID || reader.Context.CheckoutID != publication.checkoutID ||
			reader.Context.ViewID != publication.viewID || reader.Context.ProfileID != publication.profileID || reader.Context.Generation != publication.generation {
			t.Fatal("selected-View status changed the satisfied Source/Checkout/View/profile/generation tuple")
		}
		freshness := reader.Freshness
		if freshness == nil || freshness.Validate() != nil || freshness.Barrier != nil {
			t.Fatal("selected-View status contains missing or malformed reader freshness")
		}
		switch freshness.State {
		case uci.QueryFreshnessObservedCurrent:
			if freshness.Method != uci.QueryFreshnessWatchWatermark || freshness.PendingChanges == nil || *freshness.PendingChanges != 0 || freshness.EnrichmentWatermark.State != uci.QueryEnrichmentCurrent {
				t.Fatal("selected-View observed-current status is not an explicitly settled zero-pending publication")
			}
			return selection
		case uci.QueryFreshnessHistorical:
			if freshness.Method != uci.QueryFreshnessPinnedHistory || freshness.PendingChanges != nil || freshness.EnrichmentWatermark.State != uci.QueryEnrichmentCurrent {
				t.Fatal("selected-View historical status is not valid immutable pinned-history evidence")
			}
			return selection
		case uci.QueryFreshnessCatchingUp:
			if freshness.Method != uci.QueryFreshnessWatchWatermark || freshness.PendingChanges != nil || freshness.EnrichmentWatermark.State != uci.QueryEnrichmentPending {
				t.Fatal("selected-View catch-up status does not match the durable publication freshness contract")
			}
		default:
			t.Fatal("selected-View reader freshness is offline, unknown, or otherwise unusable")
		}
		select {
		case <-ctx.Done():
			t.Fatal("existing consumer deadline expired while the exact pinned View was catching up")
		case <-ticker.C:
		}
	}
}
