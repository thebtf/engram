// Package writelint — finding 2 fix tests: cross-project token replay prevention.
// Verifies that Phase2 rejects tokens presented with a different project or
// different content than was bound at Phase1 time.
package writelint_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/thebtf/engram/internal/writelint"
	"github.com/thebtf/engram/pkg/models"
)

// buildReplayOrchestrator returns an orchestrator with a 10-second token TTL
// and a duplicate seeded so Phase1 always returns a token (not stored=true).
func buildReplayOrchestrator() (*writelint.Orchestrator, func()) {
	duplicate := makeDupMemory()
	duplicate.Project = "project-alpha"
	ms := &stubMemoryLister{memories: []*models.Memory{duplicate}}
	al := &concurrentAuditLogger{}
	ts := writelint.NewTokenStore(writelint.TokenStoreConfig{
		TTL:             10 * time.Second,
		JanitorInterval: 60 * time.Second,
	})
	orch := writelint.NewOrchestrator(writelint.OrchestratorConfig{
		MemoryStore:  ms,
		AuditLogger:  al,
		TokenStore:   ts,
		DupThreshold: 0.85,
	})
	return orch, ts.Close
}

// TestTokenReplay_DifferentProject verifies that Phase2 rejects a token when
// the caller supplies a different project than the one bound at Phase1 time.
// Expected error contains "resolution_token_project_mismatch".
func TestTokenReplay_DifferentProject(t *testing.T) {
	orch, closer := buildReplayOrchestrator()
	defer closer()
	ctx := context.Background()
	store := orch.MemoryStore().(*stubMemoryLister)
	original := *store.memories[0]
	original.Tags = append([]string(nil), original.Tags...)

	p1, err := orch.Phase1(ctx, &models.Memory{Content: dupContent, Project: "project-alpha"}, "agent")
	if err != nil {
		t.Fatalf("Phase1: %v", err)
	}
	if p1.Stored || p1.ResolutionToken == "" || len(p1.LintSignals) == 0 {
		t.Fatalf("Phase1: expected a signal and token, got %+v", p1)
	}
	assertDuplicateSignal(t, p1.LintSignals, 42)
	assertStubMemoryState(t, store, original, 0, 0, 0, 1)

	_, p2Err := orch.Phase2(ctx, writelint.Phase2Request{
		Token:   p1.ResolutionToken,
		Option:  "ignore_signals",
		Content: dupContent,
		Project: "project-beta", // different project — should be rejected
		Actor:   "agent",
	})
	if p2Err == nil {
		t.Fatal("TestTokenReplay_DifferentProject: expected error for cross-project replay, got nil")
	}
	if !strings.Contains(p2Err.Error(), "resolution_token_project_mismatch") {
		t.Errorf("TestTokenReplay_DifferentProject: expected 'resolution_token_project_mismatch' in error, got: %v", p2Err)
	}
	assertStubMemoryState(t, store, original, 0, 0, 0, 1)
}

// TestTokenReplay_DifferentContent verifies that Phase2 rejects a token when
// the caller supplies different content than the one hashed at Phase1 time.
// Expected error contains "resolution_token_content_mismatch".
func TestTokenReplay_DifferentContent(t *testing.T) {
	orch, closer := buildReplayOrchestrator()
	defer closer()
	ctx := context.Background()
	store := orch.MemoryStore().(*stubMemoryLister)
	original := *store.memories[0]
	original.Tags = append([]string(nil), original.Tags...)

	p1, err := orch.Phase1(ctx, &models.Memory{Content: dupContent, Project: "project-alpha"}, "agent")
	if err != nil {
		t.Fatalf("Phase1: %v", err)
	}
	if p1.Stored || p1.ResolutionToken == "" || len(p1.LintSignals) == 0 {
		t.Fatalf("Phase1: expected a signal and token, got %+v", p1)
	}
	assertDuplicateSignal(t, p1.LintSignals, 42)
	assertStubMemoryState(t, store, original, 0, 0, 0, 1)

	_, p2Err := orch.Phase2(ctx, writelint.Phase2Request{
		Token:   p1.ResolutionToken,
		Option:  "ignore_signals",
		Content: "completely different content not matching the Phase1 hash at all", // different content
		Project: "project-alpha",
		Actor:   "agent",
	})
	if p2Err == nil {
		t.Fatal("TestTokenReplay_DifferentContent: expected error for content mismatch, got nil")
	}
	if !strings.Contains(p2Err.Error(), "resolution_token_content_mismatch") {
		t.Errorf("TestTokenReplay_DifferentContent: expected 'resolution_token_content_mismatch' in error, got: %v", p2Err)
	}
	assertStubMemoryState(t, store, original, 0, 0, 0, 1)
}

// TestTokenReplay_SameProjectAndContent verifies that Phase2 succeeds when
// project and content match the Phase1 binding (regression: must not over-reject).
func TestTokenReplay_SameProjectAndContent(t *testing.T) {
	orch, closer := buildReplayOrchestrator()
	defer closer()
	ctx := context.Background()
	store := orch.MemoryStore().(*stubMemoryLister)
	original := *store.memories[0]
	original.Tags = append([]string(nil), original.Tags...)

	p1, err := orch.Phase1(ctx, &models.Memory{Content: dupContent, Project: "project-alpha"}, "agent")
	if err != nil {
		t.Fatalf("Phase1: %v", err)
	}
	if p1.Stored || p1.ResolutionToken == "" || len(p1.LintSignals) == 0 {
		t.Fatalf("Phase1: expected a signal and token, got %+v", p1)
	}
	assertDuplicateSignal(t, p1.LintSignals, 42)
	assertStubMemoryState(t, store, original, 0, 0, 0, 1)

	p2, p2Err := orch.Phase2(ctx, writelint.Phase2Request{
		Token:   p1.ResolutionToken,
		Option:  "abort",
		Content: dupContent,      // same content
		Project: "project-alpha", // same project
		Actor:   "agent",
	})
	if p2Err != nil {
		t.Fatalf("TestTokenReplay_SameProjectAndContent: expected no error for matching project+content, got: %v", p2Err)
	}
	if p2.Stored || p2.ActionTaken != "write_lint_aborted" {
		t.Errorf("expected successful abort, got %+v", p2)
	}
	assertStubMemoryState(t, store, original, 0, 0, 0, 1)
}
