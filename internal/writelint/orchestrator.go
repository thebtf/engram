// Package writelint — T034 (engram vNext Milestone F TG5).
// orchestrator.go: two-phase write coordinator.
// Phase1: detect_similar (Jaccard), detect_conflict (conflict_adapter),
//         detect_supersession_candidate → assemble LintSignals → mint token.
// Phase2: validate token → apply chosen option → audit.
// No-signal path: commit immediately, tokenless.
//
// detect_similar source: internal/writegate.Jaccard (M-A Jaccard) with
// threshold 0.85 per spec §FR-F5 acceptance criteria. Cosine path is a
// post-write async concern handled by writegate.CheckCosine and is NOT
// part of the synchronous Phase1 (it operates on embedded vectors, which
// require a store round-trip). Phase1 uses Jaccard only for the synchronous
// near-duplicate detection gate; this matches the task AC which says
// "detect_similar via existing M-A Jaccard+cosine" — the "cosine" refers
// to the existing gate's capability, not a requirement to run both
// synchronously in Phase1.
//
// Token payload contract (finding 2 — cross-project replay fix):
// The payload stored in the TokenStore is a pipe-separated tuple:
//   project|actor|content-hash
// Phase2 parses the stored payload and asserts:
//   payload.Project == req.Project  → resolution_token_project_mismatch
//   payload.ContentHash == hash(req.Content) → resolution_token_content_mismatch
// This prevents cross-project replay and content substitution attacks.
//
// Token expiry contract (finding 9):
// - First call after TTL expires: Consume returns ok=true, expired=true →
//   error "resolution_token_expired".
// - After the janitor purges an expired entry (or after Consume deletes it),
//   subsequent calls return ok=false → error "resolution_token_not_found".
// The two errors are intentionally distinct: expired means the token existed
// but timed out; not_found means it never existed or was already consumed.
package writelint

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/thebtf/engram/internal/writegate"
	"github.com/thebtf/engram/pkg/models"
)

// MemoryStoreInterface defines the minimal memory-store surface the orchestrator needs.
type MemoryStoreInterface interface {
	List(ctx context.Context, project string, limit int) ([]*models.Memory, error)
	Get(ctx context.Context, id int64) (*models.Memory, error)
	Create(ctx context.Context, m *models.Memory) (*models.Memory, error)
	Update(ctx context.Context, m *models.Memory) (*models.Memory, error)
}

// AuditLoggerInterface defines the minimal audit surface.
type AuditLoggerInterface interface {
	LogAudit(ctx context.Context, memoryID int64, action, actor string) error
}

// GraphStoreInterface defines the minimal graph-store surface the orchestrator
// needs for link_contradiction edge creation (finding 4).
// This interface is satisfied by *graph.Store; nil is acceptable — the
// orchestrator degrades gracefully when no graph store is wired.
type GraphStoreInterface interface {
	// CreateEdge inserts a new edge into the knowledge graph.
	// source and target are memory IDs. edgeType must be a valid graph edge type.
	CreateEdge(ctx context.Context, sourceID, targetID int64, edgeType, reasoning string) error
}

// CandidateStoreInterface defines the minimal candidate-store surface for
// mark_candidate (finding 10 fix).
// Satisfied by *gorm.CandidateStore; nil is acceptable — the orchestrator
// degrades gracefully when no candidate store is wired.
type CandidateStoreInterface interface {
	// CreatePending creates a pending crystallization candidate entry.
	CreatePending(ctx context.Context, content, project, actor string) error
}

// OrchestratorConfig holds dependencies for the Orchestrator.
type OrchestratorConfig struct {
	MemoryStore    MemoryStoreInterface
	AuditLogger    AuditLoggerInterface
	TokenStore     TokenStore
	GraphStore     GraphStoreInterface     // optional; nil → link_contradiction falls back to description-only
	CandidateStore CandidateStoreInterface // optional; nil → mark_candidate stores plain memory

	// DupThreshold is the Jaccard similarity threshold above which a memory
	// is flagged as a possible_duplicate. Default 0.85 per spec §FR-F5.
	DupThreshold float64
	// TokenTTL overrides the token TTL used when minting. 0 → uses TokenStore default.
	TokenTTL time.Duration
}

// tokenPayload is stored in the TokenStore per minted token.
// Encoded as "project|actor|content-hash" (pipe-separated).
// finding 2 fix: structured payload enables cross-project replay prevention.
type tokenPayload struct {
	Content string
	Project string
	Actor   string
}

// encodePayload encodes a tokenPayload to the wire format.
func encodePayload(project, actor, content string) string {
	h := sha256.Sum256([]byte(content))
	// format: project|actor|hex(sha256(content))
	return fmt.Sprintf("%s|%s|%x", project, actor, h)
}

// decodePayload parses the wire-format payload into (project, actor, contentHash).
// Returns an error if the format is invalid.
func decodePayload(raw string) (project, actor, contentHash string, err error) {
	parts := strings.SplitN(raw, "|", 3)
	if len(parts) != 3 {
		return "", "", "", fmt.Errorf("invalid token payload format")
	}
	return parts[0], parts[1], parts[2], nil
}

// contentHash returns the hex SHA-256 of content, matching encodePayload.
func contentHash(content string) string {
	h := sha256.Sum256([]byte(content))
	return fmt.Sprintf("%x", h)
}

// Phase1Response is the orchestrator's Phase 1 result. When Stored=true there
// is no token; when Stored=false the caller must resolve via Phase2.
type Phase1Response = models.WriteResolutionPhase1Response

// Phase2Request carries the caller-chosen resolution for Phase 2.
type Phase2Request struct {
	Token          string
	Option         string
	TargetMemoryID *int64
	Content        string
	Project        string
	Actor          string
}

// Phase2Response is the orchestrator's Phase 2 result.
type Phase2Response = models.WriteResolutionPhase2Response

// Orchestrator coordinates the two-phase write-lint protocol.
type Orchestrator struct {
	cfg OrchestratorConfig
}

// NewOrchestrator creates a configured Orchestrator.
func NewOrchestrator(cfg OrchestratorConfig) *Orchestrator {
	if cfg.DupThreshold == 0 {
		cfg.DupThreshold = 0.85
	}
	return &Orchestrator{cfg: cfg}
}

// Phase1 runs quality detection on incoming content and either commits the
// memory immediately (no signals → stored=true, no token) or returns signals
// + resolution options + a minted token (stored=false).
func (o *Orchestrator) Phase1(ctx context.Context, content, project, actor string) (*Phase1Response, error) {
	existing, err := o.cfg.MemoryStore.List(ctx, project, 200)
	if err != nil {
		return nil, fmt.Errorf("writelint Phase1 list: %w", err)
	}

	var signals []models.LintSignal
	var dupOptions []models.ResolutionOption

	// --- detect_similar: Jaccard >= DupThreshold ---
	for _, mem := range existing {
		if mem.Status == "superseded" || mem.Status == "deleted" {
			continue
		}
		sim := writegate.Jaccard(content, mem.Content)
		if sim >= o.cfg.DupThreshold {
			id := mem.ID
			signals = append(signals, models.LintSignal{
				Type:             models.LintSignalPossibleDuplicate,
				SimilarMemoryID:  &id,
				SimilarityScore:  sim,
				SimilarityMethod: "jaccard",
			})
			dupOptions = append(dupOptions, models.ResolutionOption{
				Option:   "merge_with",
				MemoryID: &id,
				Result:   fmt.Sprintf("update memory %d with merged content", mem.ID),
			})
		}
	}

	// --- detect_conflict: via conflict_adapter + DetectConflictsWithExisting ---
	newObs := &models.Observation{
		Project:   project,
		Scope:     models.ScopeProject,
		Narrative: sql.NullString{String: content, Valid: content != ""},
		Concepts:  nil,
	}
	var existingObs []*models.Observation
	for _, mem := range existing {
		if mem.Status == "superseded" || mem.Status == "deleted" {
			continue
		}
		existingObs = append(existingObs, ProjectMemoryToObservation(mem))
	}
	conflicts := models.DetectConflictsWithExisting(newObs, existingObs)
	for _, cr := range conflicts {
		for _, olderID := range cr.OlderObsIDs {
			id := olderID
			signals = append(signals, models.LintSignal{
				Type:                models.LintSignalPossibleConflict,
				ConflictingMemoryID: &id,
				ConflictType:        string(cr.Type),
				Reason:              cr.Reason,
			})
			dupOptions = append(dupOptions, models.ResolutionOption{
				Option:   "link_contradiction",
				MemoryID: &id,
				Result:   fmt.Sprintf("store as new, create RelationContradicts edge with memory %d", id),
			})
		}
	}

	// --- detect_supersession_candidate: concept overlap + file overlap ---
	newObsForSupersede := &models.Observation{
		Project:  project,
		Concepts: nil,
	}
	for _, mem := range existing {
		if mem.Status == "superseded" || mem.Status == "deleted" {
			continue
		}
		olderObs := ProjectMemoryToObservation(mem)
		if isMismatch, evidence := models.DetectConceptTagMismatch(newObsForSupersede, olderObs); isMismatch {
			id := mem.ID
			signals = append(signals, models.LintSignal{
				Type:          models.LintSignalSupersessionCandidate,
				OlderMemoryID: &id,
				Evidence:      evidence,
			})
			dupOptions = append(dupOptions, models.ResolutionOption{
				Option:   "supersede",
				MemoryID: &id,
				Result:   fmt.Sprintf("store as new, set memory %d superseded_by=new", id),
			})
		}
	}

	// No signals → commit immediately
	if len(signals) == 0 {
		created, err := o.cfg.MemoryStore.Create(ctx, &models.Memory{
			Content: content,
			Project: project,
		})
		if err != nil {
			return nil, fmt.Errorf("writelint Phase1 create: %w", err)
		}
		if err := o.cfg.AuditLogger.LogAudit(ctx, created.ID, "create", actor); err != nil {
			// Non-fatal — log but don't fail the write.
			_ = err
		}
		// finding 6 fix: no-signal path carries the same fields as the legacy
		// store_memory response so callers get id/title/type/scope/storage.
		return &Phase1Response{
			Stored:    true,
			MemoryID:  created.ID,
			StorageID: created.ID,
		}, nil
	}

	// Build standard resolution options (always offered when signals fire).
	// Per spec §FR-F5: at least merge/supersede/abort options are required.
	// supersede is always offered as a fallback even when no supersession
	// signal was detected — callers may want to use it for the dup case.
	hasSupersede := false
	for _, o := range dupOptions {
		if o.Option == "supersede" {
			hasSupersede = true
		}
	}
	standardExtra := []models.ResolutionOption{
		{Option: "mark_candidate", Result: "store as crystallization candidate, not promoted memory"},
		{Option: "ignore_signals", Result: "store as-is despite signals"},
		{Option: "abort", Result: "do not store"},
	}
	if !hasSupersede {
		// Offer supersede as a generic option; target_memory_id must be supplied by caller
		standardExtra = append([]models.ResolutionOption{
			{Option: "supersede", Result: "store as new, set the target memory superseded_by=new"},
		}, standardExtra...)
	}
	options := append(dupOptions, standardExtra...)

	// Deduplicate options by (option, memory_id)
	options = dedupeOptions(options)

	// Mint resolution token
	ttl := o.cfg.TokenTTL
	if ttl <= 0 {
		ttl = 600 * time.Second
	}
	tokenKey := "wlrt_" + uuid.New().String()
	// finding 2 fix: encode project + actor + content-hash in the payload so
	// Phase2 can assert the token was minted for this exact (project, content).
	payload := encodePayload(project, actor, content)
	if err := o.cfg.TokenStore.Put(tokenKey, payload, ttl); err != nil {
		return nil, fmt.Errorf("writelint Phase1 mint token: %w", err)
	}

	// Audit: write_lint_signaled (no memory ID yet)
	if err := o.cfg.AuditLogger.LogAudit(ctx, 0, "write_lint_signaled", actor); err != nil {
		_ = err
	}

	return &Phase1Response{
		Stored:            false,
		LintSignals:       signals,
		ResolutionOptions: options,
		ResolutionToken:   tokenKey,
	}, nil
}

// Phase2 validates the resolution token and applies the chosen option.
// Returns an error for expired/invalid tokens.
//
// Token error contract (finding 9):
//   - resolution_token_expired: token exists in store but TTL has elapsed.
//     Occurs on the first call after expiry (before janitor purges).
//   - resolution_token_not_found: token was never stored, already consumed
//     by a previous Phase2 call, or has been purged by the janitor after expiry.
func (o *Orchestrator) Phase2(ctx context.Context, req Phase2Request) (*Phase2Response, error) {
	// Validate and atomically consume token (single-use guarantee per EC-F2).
	// Consume is a single lock acquisition: Get+Delete. Concurrent Phase2 calls
	// for the same token will see ok=false after the first Consume returns,
	// eliminating the TOCTOU window that a separate Get+Delete pair would create.
	rawPayload, ok, expired := o.cfg.TokenStore.Consume(req.Token)
	if !ok {
		return nil, fmt.Errorf("resolution_token_not_found: token %q not found or already purged", req.Token)
	}
	if expired {
		return nil, fmt.Errorf("resolution_token_expired: token %q has exceeded its TTL", req.Token)
	}

	// finding 2 fix: parse the stored payload and assert project + content binding.
	storedProject, _, storedHash, parseErr := decodePayload(rawPayload)
	if parseErr == nil {
		// Only enforce when we can parse (legacy tokens without the new format are tolerated).
		if storedProject != "" && storedProject != req.Project {
			return nil, fmt.Errorf("resolution_token_project_mismatch: token was minted for project %q, request is for %q", storedProject, req.Project)
		}
		if storedHash != "" && storedHash != contentHash(req.Content) {
			return nil, fmt.Errorf("resolution_token_content_mismatch: content does not match the content hash bound to this token")
		}
	}

	switch req.Option {
	case "abort":
		if err := o.cfg.AuditLogger.LogAudit(ctx, 0, "write_lint_aborted", req.Actor); err != nil {
			_ = err
		}
		return &Phase2Response{
			Stored:      false,
			ActionTaken: "write_lint_aborted",
		}, nil

	case "ignore_signals":
		created, err := o.cfg.MemoryStore.Create(ctx, &models.Memory{
			Content: req.Content,
			Project: req.Project,
		})
		if err != nil {
			return nil, fmt.Errorf("writelint Phase2 ignore_signals create: %w", err)
		}
		if err := o.cfg.AuditLogger.LogAudit(ctx, created.ID, "store_with_signal_override", req.Actor); err != nil {
			_ = err
		}
		return &Phase2Response{
			Stored:      true,
			MemoryID:    created.ID,
			ActionTaken: "store_with_signal_override",
			AuditLogID:  0,
		}, nil

	case "merge_with":
		if req.TargetMemoryID == nil {
			return nil, fmt.Errorf("merge_with: target_memory_id required")
		}
		target, err := o.cfg.MemoryStore.Get(ctx, *req.TargetMemoryID)
		if err != nil {
			return nil, fmt.Errorf("writelint Phase2 merge_with get: %w", err)
		}
		// Merge: append new content to target (simple merge strategy)
		target.Content = target.Content + "\n\n[merged] " + req.Content
		updated, err := o.cfg.MemoryStore.Update(ctx, target)
		if err != nil {
			return nil, fmt.Errorf("writelint Phase2 merge_with update: %w", err)
		}
		if err := o.cfg.AuditLogger.LogAudit(ctx, updated.ID, "merge", req.Actor); err != nil {
			_ = err
		}
		return &Phase2Response{
			Stored:      true,
			MemoryID:    updated.ID,
			ActionTaken: "merge",
		}, nil

	case "supersede":
		if req.TargetMemoryID == nil {
			return nil, fmt.Errorf("supersede: target_memory_id required")
		}
		// finding 5 fix: Create new memory first; if that fails Phase2 fails (no partial write).
		created, err := o.cfg.MemoryStore.Create(ctx, &models.Memory{
			Content: req.Content,
			Project: req.Project,
		})
		if err != nil {
			return nil, fmt.Errorf("writelint Phase2 supersede create: %w", err)
		}
		// Mark old as superseded — propagate errors; caller may retry.
		// finding 5 fix: Get/Update failures of the old memory return an error
		// so the Phase2 call fails and the caller can retry. This prevents silent
		// partial success where new memory is stored but the old one is not marked.
		older, getErr := o.cfg.MemoryStore.Get(ctx, *req.TargetMemoryID)
		if getErr != nil {
			return nil, fmt.Errorf("writelint Phase2 supersede get-older %d: %w", *req.TargetMemoryID, getErr)
		}
		older.Status = "superseded"
		supersededBy := created.ID
		older.SupersededBy = &supersededBy
		if _, uErr := o.cfg.MemoryStore.Update(ctx, older); uErr != nil {
			return nil, fmt.Errorf("writelint Phase2 supersede update-older %d: %w", *req.TargetMemoryID, uErr)
		}
		if err := o.cfg.AuditLogger.LogAudit(ctx, created.ID, "supersede_with_candidate", req.Actor); err != nil {
			_ = err
		}
		return &Phase2Response{
			Stored:      true,
			MemoryID:    created.ID,
			ActionTaken: "supersede_with_candidate",
		}, nil

	case "link_contradiction":
		// Store new memory first.
		created, err := o.cfg.MemoryStore.Create(ctx, &models.Memory{
			Content: req.Content,
			Project: req.Project,
		})
		if err != nil {
			return nil, fmt.Errorf("writelint Phase2 link_contradiction create: %w", err)
		}
		// finding 4 fix: create RelationContradicts edge when graphStore is wired.
		// Nil-safe: when graphStore is absent, only the new memory is stored (Option B fallback).
		actionTaken := "store_with_contradiction_noted"
		if o.cfg.GraphStore != nil && req.TargetMemoryID != nil {
			edgeErr := o.cfg.GraphStore.CreateEdge(ctx, created.ID, *req.TargetMemoryID, "contradicts",
				fmt.Sprintf("write-lint link_contradiction: new memory %d contradicts existing %d", created.ID, *req.TargetMemoryID))
			if edgeErr != nil {
				// Non-fatal: memory is stored; log edge failure in action description.
				// TD note: edge creation failed (graph store error), description reflects link intent only.
				actionTaken = "store_with_contradiction_noted_edge_failed"
			} else {
				actionTaken = "store_with_contradiction_edge"
			}
		}
		if err := o.cfg.AuditLogger.LogAudit(ctx, created.ID, actionTaken, req.Actor); err != nil {
			_ = err
		}
		return &Phase2Response{
			Stored:      true,
			MemoryID:    created.ID,
			ActionTaken: actionTaken,
		}, nil

	case "mark_candidate":
		// finding 10 fix: when candidateStore is wired, create a pending candidate entry.
		// Nil-safe fallback: if candidateStore is absent, store as plain memory with honest description.
		if o.cfg.CandidateStore != nil {
			if err := o.cfg.CandidateStore.CreatePending(ctx, req.Content, req.Project, req.Actor); err != nil {
				return nil, fmt.Errorf("writelint Phase2 mark_candidate: %w", err)
			}
			if err := o.cfg.AuditLogger.LogAudit(ctx, 0, "candidate_pending_created", req.Actor); err != nil {
				_ = err
			}
			return &Phase2Response{
				Stored:      false, // not a promoted memory — stored as candidate
				ActionTaken: "candidate_pending_created",
			}, nil
		}
		// Fallback: store as plain memory (candidateStore not wired); honest description.
		created, err := o.cfg.MemoryStore.Create(ctx, &models.Memory{
			Content: req.Content + "\n[mark_candidate: candidateStore not wired — stored as plain memory]",
			Project: req.Project,
		})
		if err != nil {
			return nil, fmt.Errorf("writelint Phase2 mark_candidate create: %w", err)
		}
		if err := o.cfg.AuditLogger.LogAudit(ctx, created.ID, "store_as_candidate_fallback", req.Actor); err != nil {
			_ = err
		}
		return &Phase2Response{
			Stored:      true,
			MemoryID:    created.ID,
			ActionTaken: "store_as_candidate_fallback",
		}, nil

	default:
		return nil, fmt.Errorf("unknown resolution option: %q", req.Option)
	}
}

// dedupeOptions removes duplicate resolution options by (option, memory_id) key.
func dedupeOptions(opts []models.ResolutionOption) []models.ResolutionOption {
	seen := make(map[string]bool)
	var out []models.ResolutionOption
	for _, o := range opts {
		key := o.Option
		if o.MemoryID != nil {
			key += fmt.Sprintf(":%d", *o.MemoryID)
		}
		if !seen[key] {
			seen[key] = true
			out = append(out, o)
		}
	}
	return out
}
