package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/thebtf/engram/internal/auth"
	gormdb "github.com/thebtf/engram/internal/db/gorm"
)

func TestUCIReleasedPublicWorktreesReuseEmbeddingAcrossProfiles(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("native Windows installed standard-client acceptance")
	}
	request := uciInstalledAcceptanceConfiguredRequest(t)
	uciInstalledAcceptanceUseArtifactEnv(&request)
	request.OperationTimeout = 5 * time.Minute
	var mu sync.Mutex
	inputs := make(map[string]int)
	provider, _ := uciCoreRepairEmbeddingProviderObserved(t, func(batch []string) {
		mu.Lock()
		defer mu.Unlock()
		for _, text := range batch {
			inputs[text]++
		}
	})
	request.EmbeddingProvider = &provider
	t.Setenv("ENGRAM_EMBEDDING_DIMENSIONS", "1536")
	uciInstalledAcceptanceRequireCleanup(t, request)
	ctx, cancel := context.WithTimeout(context.Background(), request.OperationTimeout)
	defer cancel()
	if err := uciValidateInstalledAcceptanceRequest(ctx, request); err != nil {
		t.Fatal(err)
	}
	result := uciNewInstalledAcceptanceResult(request)
	installed := &uciInstalledAcceptanceRuntime{request: request, result: &result, daemonControlRoot: filepath.Join(request.LocalStateRoot, "temp")}
	t.Cleanup(func() {
		var err error
		installed.cleanup(&err)
		if err != nil {
			t.Errorf("fixture cleanup: %v", err)
		} else {
			t.Logf("cleanup process_tree_closed=%t install_removed=%t fixture_removed=%t local_state_removed=%t", result.Cleanup.ProcessTreeClosed, result.Cleanup.InstallRootRemoved, result.Cleanup.FixtureRootRemoved, result.Cleanup.LocalStateRootRemoved)
		}
	})
	if err := installed.setupRootsAndAuthority(ctx); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(installed.worktrees.linkedRoot, filepath.FromSlash(request.Fixture.RelativePath)), []byte(request.Fixture.PrimarySource), 0o600); err != nil {
		t.Fatal(err)
	}
	request.Fixture.LinkedSource = request.Fixture.PrimarySource
	request.Fixture.LinkedCallee = request.Fixture.PrimaryCallee
	principal := "human/cache-reuse-" + uuid.NewString()
	token, raw := uciCoreRepairKeycard(t, ctx, installed.authority, principal, auth.PrincipalKindHuman)
	installed.authority.token, installed.authority.rawToken, installed.authority.principal = token, raw, principal
	if err := installed.materialize(ctx); err != nil {
		t.Fatal(err)
	}
	if err := installed.startServer(ctx); err != nil {
		t.Fatal(err)
	}
	clientA, err := installed.startPrimaryClient(ctx)
	if err != nil {
		t.Fatal(err)
	}
	installed.clientA = clientA
	daemonPID, err := uciWaitForInstalledAcceptanceDaemonPID(ctx, installed.daemonControlRoot, installed.daemonPath)
	if err != nil {
		t.Fatal(err)
	}
	installed.activeDaemonPID = daemonPID
	result.Processes.DaemonPID = daemonPID
	selectionA, checkoutA := uciCoreRepairRegisterHumanCheckout(t, ctx, installed, clientA)
	index := func(client *uciInstalledAcceptanceMCPClient, selection uciInstalledAcceptanceSelection, root string) (uciInstalledAcceptanceSelection, uciInstalledAcceptancePublication, uciRealCorpusEmbeddingStatus) {
		t.Helper()
		payload, err := client.Tool(ctx, "codebase_index", map[string]any{"context_handle": selection.contextHandle, "root": root})
		if err != nil {
			t.Fatal(err)
		}
		var response struct {
			Status string `json:"status"`
			RunID  string `json:"run_id"`
		}
		if json.Unmarshal(payload, &response) != nil || response.Status != "started" || response.RunID == "" {
			t.Fatal("index did not return a started run")
		}
		selection.runID = response.RunID
		publication, err := uciWaitForInstalledAcceptanceBarrier(ctx, client, selection)
		if err != nil {
			t.Fatal(err)
		}
		selection = uciCoreRepairPinPublishedContext(t, ctx, client, selection, publication)
		status, publication, err := uciWaitForRealCorpusEmbeddings(ctx, client, selection, publication)
		if err != nil {
			t.Fatalf("await embedding: %v", err)
		}
		return selection, publication, status
	}
	selectionA, pubA, statusA := index(clientA, selectionA, installed.worktrees.primaryRoot)
	mu.Lock()
	var canonical string
	for text, count := range inputs {
		var value struct {
			Schema        string `json:"schema"`
			Text          string `json:"text"`
			RelativePath  string `json:"relative_path"`
			Preprocessing string `json:"preprocessing_revision"`
		}
		if json.Unmarshal([]byte(text), &value) == nil && value.RelativePath == request.Fixture.RelativePath && strings.Contains(value.Text, "T071_ALPHA_DIRTY_BODY") && value.Schema != "" && value.Preprocessing != "" && count == 1 {
			canonical = text
			break
		}
	}
	mu.Unlock()
	if canonical == "" {
		t.Fatal("no once-requested canonical path/text input after A embedding completed")
	}
	sum := sha256.Sum256([]byte(canonical))
	digest := "sha256:" + hex.EncodeToString(sum[:])
	clientB, err := installed.startClient(ctx, uciInstalledAcceptanceClientB, installed.worktrees.linkedRoot)
	if err != nil {
		t.Fatal(err)
	}
	installed.clientB = clientB
	selected, err := clientB.Tool(ctx, "codebase_context", map[string]any{"action": "select", "source_id": pubA.sourceID, "checkout_id": pubA.checkoutID, "view_id": pubA.viewID, "analysis_profile_id": pubA.profileID, "generation": pubA.generation})
	if err != nil {
		t.Fatalf("select A View for B registration: %v", err)
	}
	var sourceSelection struct {
		ContextHandle string `json:"context_handle"`
		BindingKind   string `json:"binding_kind"`
	}
	if json.Unmarshal(selected, &sourceSelection) != nil || sourceSelection.ContextHandle == "" || sourceSelection.BindingKind != "context" {
		t.Fatal("B could not obtain its own authorized A View handle")
	}
	payload, err := clientB.Tool(ctx, "codebase_context", map[string]any{"action": "register", "context_handle": sourceSelection.ContextHandle})
	if err != nil {
		t.Fatalf("register B to A Source: %v", err)
	}
	var registered struct {
		ContextHandle string `json:"context_handle"`
		BindingKind   string `json:"binding_kind"`
		SourceID      string `json:"source_id"`
		CheckoutID    string `json:"checkout_id"`
		ViewID        string `json:"view_id"`
	}
	if json.Unmarshal(payload, &registered) != nil || registered.ContextHandle == "" || registered.BindingKind != "checkout" || registered.SourceID != checkoutA.SourceID || registered.CheckoutID == "" || registered.CheckoutID == checkoutA.CheckoutID || registered.ViewID != "" {
		t.Fatal("B registration did not create a fresh checkout on A Source")
	}
	selectionB, pubB, statusB := index(clientB, uciInstalledAcceptanceSelection{contextHandle: registered.ContextHandle}, installed.worktrees.linkedRoot)
	if pubA.sourceID != pubB.sourceID || pubA.checkoutID == pubB.checkoutID || pubA.profileID == pubB.profileID || pubB.checkoutID != registered.CheckoutID {
		t.Fatal("public Views do not have one Source, distinct checkouts and distinct analysis profiles")
	}
	if statusA.Embedding.EmbeddingProfileID == nil || statusB.Embedding.EmbeddingProfileID == nil {
		t.Fatal("embedding profiles absent")
	}
	db := installed.authority.store.GetDB().WithContext(ctx)
	var analysis []gormdb.UCIAnalysisProfile
	if err := db.Where("profile_id IN ?", []string{pubA.profileID, pubB.profileID}).Find(&analysis).Error; err != nil || len(analysis) != 2 {
		t.Fatal("read public analysis profiles")
	}
	a, b := analysis[0], analysis[1]
	if a.ParserBundleDigest != b.ParserBundleDigest || a.ResolverRevision != b.ResolverRevision || a.ChunkerRevision != b.ChunkerRevision || a.IgnorePolicyDigest != b.IgnorePolicyDigest || a.BuildContextJSON != b.BuildContextJSON || a.SecretPolicyRevision != b.SecretPolicyRevision {
		t.Fatal("analysis interpretation differs between registrations")
	}
	checkoutB, err := gormdb.NewUCIContextStore(db).GetCheckout(ctx, pubB.checkoutID)
	if err != nil || checkoutB.OwnerPrincipal != principal || checkoutB.WorkstationID != token.ID {
		t.Fatal("B public checkout has different owner or workstation")
	}
	var profiles []gormdb.UCIEmbeddingProfile
	if err := db.Where("embedding_profile_id IN ?", []string{*statusA.Embedding.EmbeddingProfileID, *statusB.Embedding.EmbeddingProfileID}).Find(&profiles).Error; err != nil || len(profiles) != 2 {
		t.Fatal("read distinct vector profiles")
	}
	p, q := profiles[0], profiles[1]
	if p.AnalysisProfileID == q.AnalysisProfileID || p.ProviderRef != q.ProviderRef || p.Model != q.Model || p.Dimension != q.Dimension || p.PreprocessingRevision != q.PreprocessingRevision || p.IncludeRelativePath != q.IncludeRelativePath || !p.IncludeRelativePath {
		t.Fatal("vector eligibility differs between profiles")
	}
	var rows []struct{ ProfileID, ProtectionDomain string }
	if err := db.Raw(`SELECT profile.analysis_profile_id AS profile_id, embedding.protection_domain FROM ci_chunk_embeddings AS link JOIN ci_embeddings AS embedding ON embedding.embedding_id = link.embedding_id AND embedding.embedding_profile_id = link.embedding_profile_id JOIN ci_embedding_profiles AS profile ON profile.embedding_profile_id = link.embedding_profile_id WHERE link.source_id = ? AND link.relative_path_fingerprint = ? AND link.embedding_input_digest = ? AND embedding.status = 'ready' AND embedding.vector IS NOT NULL AND profile.analysis_profile_id IN (?, ?)`, pubA.sourceID, request.Fixture.RelativePath, digest, pubA.profileID, pubB.profileID).Scan(&rows).Error; err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 || rows[0].ProfileID == rows[1].ProfileID || rows[0].ProtectionDomain == "" || rows[0].ProtectionDomain != rows[1].ProtectionDomain {
		t.Fatalf("canonical input does not have both equal-domain ready links: %+v", rows)
	}
	citation, err := uciInstalledAcceptanceSearchCitation(ctx, clientB, selectionB, pubB, request.Fixture)
	if err != nil {
		t.Fatal(err)
	}
	read, err := uciInstalledAcceptanceReadCitation(ctx, clientB, selectionB, pubB, citation)
	if err != nil {
		t.Fatal(err)
	}
	if read.Span.ByteStart < 0 || read.Span.ByteEnd > int64(len(request.Fixture.LinkedSource)) || read.Excerpt != request.Fixture.LinkedSource[read.Span.ByteStart:read.Span.ByteEnd] {
		t.Fatal("B public stored read differs from exact fixture bytes")
	}
	mu.Lock()
	count := inputs[canonical]
	mu.Unlock()
	if count != 1 {
		t.Fatalf("canonical eligible input was requested %d times, want one", count)
	}
	uciInstalledAcceptanceRequireArtifacts(t, result)
	t.Logf("source=%s checkout_a=%s checkout_b=%s profile_a=%s profile_b=%s view_a=%s view_b=%s input_digest=%s eligible_requests=%d domain=%s model=%s dimension=%d preprocessing=%s path_policy=%t", pubA.sourceID, pubA.checkoutID, pubB.checkoutID, pubA.profileID, pubB.profileID, pubA.viewID, pubB.viewID, digest, count, rows[0].ProtectionDomain, p.Model, p.Dimension, p.PreprocessingRevision, p.IncludeRelativePath)
	t.Logf("processes test=%d server=%d daemon=%d stdio_a=%d stdio_b=%d", os.Getpid(), result.Processes.ServerPID, daemonPID, clientA.process.command.Process.Pid, clientB.process.command.Process.Pid)
}
