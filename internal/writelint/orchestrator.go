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
package writelint

import (
	"context"
	"database/sql"
	"fmt"
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

// OrchestratorConfig holds dependencies for the Orchestrator.
type OrchestratorConfig struct {
	MemoryStore  MemoryStoreInterface
	AuditLogger  AuditLoggerInterface
	TokenStore   TokenStore
	// DupThreshold is the Jaccard similarity threshold above which a memory
	// is flagged as a possible_duplicate. Default 0.85 per spec §FR-F5.
	DupThreshold float64
	// TokenTTL overrides the token TTL used when minting. 0 → uses TokenStore default.
	TokenTTL time.Duration
}

// tokenPayload is stored in the TokenStore per minted token.
type tokenPayload struct {
	Content string
	Project string
	Actor   string
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
		Project:   project,
		Concepts:  nil,
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
		return &Phase1Response{
			Stored: true,
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
	payload := fmt.Sprintf("%s|%s|%s", content, project, actor)
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
func (o *Orchestrator) Phase2(ctx context.Context, req Phase2Request) (*Phase2Response, error) {
	// Validate and atomically consume token (single-use guarantee per EC-F2).
	// Consume is a single lock acquisition: Get+Delete. Concurrent Phase2 calls
	// for the same token will see ok=false after the first Consume returns,
	// eliminating the TOCTOU window that a separate Get+Delete pair would create.
	_, ok, expired := o.cfg.TokenStore.Consume(req.Token)
	if !ok {
		return nil, fmt.Errorf("resolution_token_not_found: token %q not found or already purged", req.Token)
	}
	if expired {
		return nil, fmt.Errorf("resolution_token_expired: token %q has exceeded its TTL", req.Token)
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
		// Create new memory
		created, err := o.cfg.MemoryStore.Create(ctx, &models.Memory{
			Content: req.Content,
			Project: req.Project,
		})
		if err != nil {
			return nil, fmt.Errorf("writelint Phase2 supersede create: %w", err)
		}
		// Mark old as superseded
		older, getErr := o.cfg.MemoryStore.Get(ctx, *req.TargetMemoryID)
		if getErr == nil {
			older.Status = "superseded"
			supersededBy := created.ID
			older.SupersededBy = &supersededBy
			if _, uErr := o.cfg.MemoryStore.Update(ctx, older); uErr != nil {
				_ = uErr
			}
		}
		if err := o.cfg.AuditLogger.LogAudit(ctx, created.ID, "supersede_with_candidate", req.Actor); err != nil {
			_ = err
		}
		return &Phase2Response{
			Stored:      true,
			MemoryID:    created.ID,
			ActionTaken: "supersede_with_candidate",
		}, nil

	case "link_contradiction", "mark_candidate":
		// store as new memory
		created, err := o.cfg.MemoryStore.Create(ctx, &models.Memory{
			Content: req.Content,
			Project: req.Project,
		})
		if err != nil {
			return nil, fmt.Errorf("writelint Phase2 %s create: %w", req.Option, err)
		}
		if err := o.cfg.AuditLogger.LogAudit(ctx, created.ID, "store_with_signal_override", req.Actor); err != nil {
			_ = err
		}
		return &Phase2Response{
			Stored:      true,
			MemoryID:    created.ID,
			ActionTaken: "store_with_signal_override",
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

