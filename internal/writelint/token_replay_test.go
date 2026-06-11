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
	ms := newConcMemStore(makeDupMemory())
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

	p1, err := orch.Phase1(ctx, &models.Memory{Content: dupContent, Project: "project-alpha"}, "agent")
	if err != nil {
		t.Fatalf("Phase1: %v", err)
	}
	if p1.Stored {
		t.Skip("no signal fired — replay test requires token; check dupContent similarity")
	}

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
}

// TestTokenReplay_DifferentContent verifies that Phase2 rejects a token when
// the caller supplies different content than the one hashed at Phase1 time.
// Expected error contains "resolution_token_content_mismatch".
func TestTokenReplay_DifferentContent(t *testing.T) {
	orch, closer := buildReplayOrchestrator()
	defer closer()
	ctx := context.Background()

	p1, err := orch.Phase1(ctx, &models.Memory{Content: dupContent, Project: "project-alpha"}, "agent")
	if err != nil {
		t.Fatalf("Phase1: %v", err)
	}
	if p1.Stored {
		t.Skip("no signal fired — replay test requires token; check dupContent similarity")
	}

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
}

// TestTokenReplay_SameProjectAndContent verifies that Phase2 succeeds when
// project and content match the Phase1 binding (regression: must not over-reject).
func TestTokenReplay_SameProjectAndContent(t *testing.T) {
	orch, closer := buildReplayOrchestrator()
	defer closer()
	ctx := context.Background()

	p1, err := orch.Phase1(ctx, &models.Memory{Content: dupContent, Project: "project-alpha"}, "agent")
	if err != nil {
		t.Fatalf("Phase1: %v", err)
	}
	if p1.Stored {
		t.Skip("no signal fired — replay test requires token; check dupContent similarity")
	}

	_, p2Err := orch.Phase2(ctx, writelint.Phase2Request{
		Token:   p1.ResolutionToken,
		Option:  "abort",
		Content: dupContent, // same content
		Project: "project-alpha", // same project
		Actor:   "agent",
	})
	if p2Err != nil {
		t.Errorf("TestTokenReplay_SameProjectAndContent: expected no error for matching project+content, got: %v", p2Err)
	}
}
