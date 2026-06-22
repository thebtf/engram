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
// The payload stored in the TokenStore is a JSON object:
//   {"content_hash":"<hex-sha256>","project":"<project>","actor":"<actor>"}
// JSON encoding avoids pipe-delimiter collision when project or actor contain '|'.
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
	"encoding/json"
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
	// MarkSuperseded marks memory olderID as superseded by newID.
	// Sets status="superseded" and superseded_by=newID atomically.
	MarkSuperseded(ctx context.Context, olderID, newID int64) error
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
	// AutoSupersedeThreshold (rank-9) is the Jaccard similarity at or above which
	// Phase1 AUTOMATICALLY supersedes the matched prior memory with the incoming
	// write, instead of suspending for a Phase2 decision. Default 0.0 disables the
	// behavior entirely (every duplicate still routes through the signal/token path).
	// Because auto-supersede is destructive (the prior memory is marked superseded),
	// this is opt-in and should be set high enough that the two texts are effectively
	// identical word-sets (e.g. 0.97) — the 0.85..threshold band stays signal-only so
	// genuinely-distinct-but-similar memories are never silently merged. When set at or
	// below DupThreshold the value is ignored (would collapse the human-in-the-loop band).
	AutoSupersedeThreshold float64
	// TokenTTL overrides the token TTL used when minting. 0 → uses TokenStore default.
	TokenTTL time.Duration
	// MemoryListLimit caps how many existing memories Phase1 loads for
	// duplicate/conflict detection. Default 200 when <= 0.
	MemoryListLimit int
}

// tokenPayload is stored in the TokenStore per minted token.
// Encoded as a JSON object so that project/actor values containing '|'
// do not corrupt the payload (fixes pipe-delimiter collision vulnerability).
// finding 2 fix: structured payload enables cross-project replay prevention.
type tokenPayload struct {
	ContentHash string `json:"content_hash"`
	Project     string `json:"project"`
	Actor       string `json:"actor"`
}

// encodePayload encodes a tokenPayload to the wire format (JSON).
func encodePayload(project, actor, content string) string {
	h := sha256.Sum256([]byte(content))
	p := tokenPayload{
		ContentHash: fmt.Sprintf("%x", h),
		Project:     project,
		Actor:       actor,
	}
	data, _ := json.Marshal(p)
	return string(data)
}

// decodePayload parses the JSON wire-format payload into (project, actor, contentHash).
// Returns an error if the format is invalid.
func decodePayload(raw string) (project, actor, contentHash string, err error) {
	var p tokenPayload
	if err := json.Unmarshal([]byte(raw), &p); err != nil {
		return "", "", "", fmt.Errorf("invalid token payload format: %w", err)
	}
	return p.Project, p.Actor, p.ContentHash, nil
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
// Mem carries the full normalized memory metadata (Content, Project, Tags,
// PrivacyScope, SourceWorkstationID, SourceSessions, etc.) so that Phase2
// can create new memory records with the complete field set.
// Content, Project are convenience aliases kept for backward compatibility;
// when Mem is non-nil its Content/Project fields take precedence.
type Phase2Request struct {
	Token          string
	Option         string
	TargetMemoryID *int64
	Content        string
	Project        string
	Actor          string
	// Mem carries the full normalized memory for create paths in Phase2.
	// When non-nil, new memories are created from Mem instead of Content+Project only.
	Mem *models.Memory
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
	if cfg.MemoryListLimit <= 0 {
		cfg.MemoryListLimit = 200
	}
	return &Orchestrator{cfg: cfg}
}

// MemoryStore returns the orchestrator-owned memory store so transport
// adapters can wrap it in request-scoped visibility filters without falling
// back to raw Phase1/Phase2 calls.
func (o *Orchestrator) MemoryStore() MemoryStoreInterface {
	if o == nil {
		return nil
	}
	return o.cfg.MemoryStore
}

// Phase1 runs quality detection on incoming content and either commits the
// memory immediately (no signals → stored=true, no token) or returns signals
// + resolution options + a minted token (stored=false).
//
// mem carries all metadata for the write (Content, Project, Tags, PrivacyScope,
// SourceWorkstationID, SourceSessions, etc.) so that the no-signal path and
// Phase2 paths can persist the full normalized record instead of just
// Content+Project (fixes metadata loss for Tags, PrivacyScope, etc.).
// actor is the calling agent identifier used for audit logging.
func (o *Orchestrator) Phase1(ctx context.Context, mem *models.Memory, actor string) (*Phase1Response, error) {
	return o.phase1(ctx, mem, actor, o.cfg.MemoryStore)
}

// Phase1WithMemoryStore runs Phase1 through a request-scoped memory-store view.
// The default orchestrator store remains unchanged; callers use this when
// candidate visibility depends on the authenticated request.
func (o *Orchestrator) Phase1WithMemoryStore(ctx context.Context, mem *models.Memory, actor string, memoryStore MemoryStoreInterface) (*Phase1Response, error) {
	return o.phase1(ctx, mem, actor, memoryStore)
}

func (o *Orchestrator) phase1(ctx context.Context, mem *models.Memory, actor string, memoryStore MemoryStoreInterface) (*Phase1Response, error) {
	if memoryStore == nil {
		return nil, fmt.Errorf("writelint Phase1 memory store not configured")
	}
	content := mem.Content
	project := mem.Project

	existing, err := memoryStore.List(ctx, project, o.cfg.MemoryListLimit)
	if err != nil {
		return nil, fmt.Errorf("writelint Phase1 list: %w", err)
	}

	var signals []models.LintSignal
	var dupOptions []models.ResolutionOption

	// autoThreshold is honored only when it sits strictly above DupThreshold; otherwise
	// enabling it would swallow the human-in-the-loop 0.85..threshold band. 0.0 (default)
	// disables auto-supersede entirely.
	autoThreshold := o.cfg.AutoSupersedeThreshold
	autoEnabled := autoThreshold > 0 && autoThreshold > o.cfg.DupThreshold
	var bestAutoID int64
	var bestAutoSim float64

	// --- detect_similar: Jaccard >= DupThreshold ---
	for _, ex := range existing {
		if ex.Status == "superseded" || ex.Status == "deleted" {
			continue
		}
		sim := writegate.Jaccard(content, ex.Content)
		if sim >= o.cfg.DupThreshold {
			// rank-9: track the single highest-similarity prior at/above the auto-supersede
			// threshold. We act on the BEST match only (one supersede target), after the loop.
			if autoEnabled && sim >= autoThreshold && sim > bestAutoSim {
				bestAutoSim = sim
				bestAutoID = ex.ID
			}
			id := ex.ID
			signals = append(signals, models.LintSignal{
				Type:             models.LintSignalPossibleDuplicate,
				SimilarMemoryID:  &id,
				SimilarityScore:  sim,
				SimilarityMethod: "jaccard",
			})
			dupOptions = append(dupOptions, models.ResolutionOption{
				Option:   "merge_with",
				MemoryID: &id,
				Result:   fmt.Sprintf("update memory %d with merged content", ex.ID),
			})
		}
	}

	// rank-9: automatic disposition for a near-identical write. When a prior memory matches
	// at/above AutoSupersedeThreshold, store the incoming memory and mark that prior superseded
	// inline — no Phase2 round-trip, no token. This runs BEFORE conflict/supersession detection
	// because a near-identical duplicate is the dominant signal: the agent's intent is clearly to
	// record the same fact, so we converge the corpus instead of accumulating a duplicate. Only
	// the single best (highest-Jaccard) match is superseded.
	if autoEnabled && bestAutoID != 0 {
		created, err := memoryStore.Create(ctx, mem)
		if err != nil {
			return nil, fmt.Errorf("writelint Phase1 auto-supersede create: %w", err)
		}
		if err := memoryStore.MarkSuperseded(ctx, bestAutoID, created.ID); err != nil {
			// The new memory is already stored; a failed supersede just leaves the old one
			// active (a duplicate), which is non-destructive. Log via audit, don't fail the write.
			_ = o.cfg.AuditLogger.LogAudit(ctx, created.ID, "auto_supersede_mark_failed", actor)
		} else {
			_ = o.cfg.AuditLogger.LogAudit(ctx, created.ID, "auto_superseded", actor)
		}
		if err := o.cfg.AuditLogger.LogAudit(ctx, created.ID, "create", actor); err != nil {
			_ = err
		}
		return &Phase1Response{
			Stored:           true,
			MemoryID:         created.ID,
			StorageID:        created.ID,
			ActionTaken:      "auto_superseded",
			AutoSupersededID: bestAutoID,
		}, nil
	}

	// Derive concept tags from mem.Tags for conflict/supersession detection.
	// Row 7 of the conflict adapter contract maps Tags → Concepts directly.
	// Populating Concepts before detection ensures DetectConflictsWithExisting
	// and DetectConceptTagMismatch operate on the actual tag set, not nil.
	var incomingConcepts models.JSONStringArray
	if len(mem.Tags) > 0 {
		incomingConcepts = make(models.JSONStringArray, len(mem.Tags))
		copy(incomingConcepts, mem.Tags)
	}

	// --- detect_conflict: via conflict_adapter + DetectConflictsWithExisting ---
	newObs := &models.Observation{
		Project:   project,
		Scope:     models.ScopeProject,
		Narrative: sql.NullString{String: content, Valid: content != ""},
		Concepts:  incomingConcepts,
	}
	var existingObs []*models.Observation
	for _, ex := range existing {
		if ex.Status == "superseded" || ex.Status == "deleted" {
			continue
		}
		existingObs = append(existingObs, ProjectMemoryToObservation(ex))
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
		Concepts: incomingConcepts,
	}
	for _, ex := range existing {
		if ex.Status == "superseded" || ex.Status == "deleted" {
			continue
		}
		olderObs := ProjectMemoryToObservation(ex)
		if isMismatch, evidence := models.DetectConceptTagMismatch(newObsForSupersede, olderObs); isMismatch {
			id := ex.ID
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

	// No signals → commit immediately with full metadata.
	if len(signals) == 0 {
		created, err := memoryStore.Create(ctx, mem)
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

// memForCreate returns the memory model to use when creating a new record in
// Phase2. When req.Mem is set it is used directly (full metadata preserved);
// otherwise a minimal memory is constructed from req.Content + req.Project.
func (o *Orchestrator) memForCreate(req Phase2Request) *models.Memory {
	if req.Mem != nil {
		// Reset ID so the store assigns a new one.
		m := *req.Mem
		m.ID = 0
		return &m
	}
	return &models.Memory{
		Content: req.Content,
		Project: req.Project,
	}
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
	return o.phase2(ctx, req, o.cfg.MemoryStore)
}

// Phase2WithMemoryStore runs Phase2 through a request-scoped memory-store view.
// It lets transport handlers enforce the same visibility model for target
// memories that Phase1 used for candidate generation.
func (o *Orchestrator) Phase2WithMemoryStore(ctx context.Context, req Phase2Request, memoryStore MemoryStoreInterface) (*Phase2Response, error) {
	return o.phase2(ctx, req, memoryStore)
}

func (o *Orchestrator) phase2(ctx context.Context, req Phase2Request, memoryStore MemoryStoreInterface) (*Phase2Response, error) {
	if memoryStore == nil {
		return nil, fmt.Errorf("writelint Phase2 memory store not configured")
	}
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
	// Payload is always JSON (produced by encodePayload). A parse error means the
	// token store contains a corrupted or tampered entry — reject it.
	storedProject, _, storedHash, parseErr := decodePayload(rawPayload)
	if parseErr != nil {
		return nil, fmt.Errorf("resolution_token_invalid: malformed payload: %w", parseErr)
	}
	if storedProject != "" && storedProject != req.Project {
		return nil, fmt.Errorf("resolution_token_project_mismatch: token was minted for project %q, request is for %q", storedProject, req.Project)
	}
	if storedHash != "" && storedHash != contentHash(req.Content) {
		return nil, fmt.Errorf("resolution_token_content_mismatch: content does not match the content hash bound to this token")
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
		// Create with full metadata so privacy_scope, tags, etc. are persisted.
		created, err := memoryStore.Create(ctx, o.memForCreate(req))
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
		target, err := memoryStore.Get(ctx, *req.TargetMemoryID)
		if err != nil {
			return nil, fmt.Errorf("writelint Phase2 merge_with get: %w", err)
		}
		if target == nil {
			return nil, fmt.Errorf("merge_with: target memory %d not found", *req.TargetMemoryID)
		}
		// round-4 finding 1 fix: verify the target memory belongs to the token's
		// project before merging. Without this check a token minted for project A
		// could append project-A content into a project-B memory (cross-project
		// clobber attack). Mirrors the supersede_project_mismatch check added in
		// round 3.
		if target.Project != req.Project {
			return nil, fmt.Errorf("merge_project_mismatch: target memory %d belongs to project %q, token is for project %q",
				*req.TargetMemoryID, target.Project, req.Project)
		}
		// Merge: append new content to target (simple merge strategy)
		target.Content = target.Content + "\n\n[merged] " + req.Content
		updated, err := memoryStore.Update(ctx, target)
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
		// round-3 finding 2 fix: verify the target memory belongs to the token's project
		// before marking it superseded. Without this check a token minted for project A
		// could mark memories from project B as superseded while creating the replacement
		// in project A (cross-project clobber attack).
		supersedeTgt, err := memoryStore.Get(ctx, *req.TargetMemoryID)
		if err != nil {
			return nil, fmt.Errorf("writelint Phase2 supersede get target: %w", err)
		}
		if supersedeTgt == nil {
			return nil, fmt.Errorf("supersede: target memory %d not found", *req.TargetMemoryID)
		}
		if supersedeTgt.Project != req.Project {
			return nil, fmt.Errorf("supersede_project_mismatch: target memory %d belongs to project %q, token is for project %q",
				*req.TargetMemoryID, supersedeTgt.Project, req.Project)
		}
		// finding 5 fix: Create new memory first with full metadata; if that fails
		// Phase2 fails (no partial write).
		created, err := memoryStore.Create(ctx, o.memForCreate(req))
		if err != nil {
			return nil, fmt.Errorf("writelint Phase2 supersede create: %w", err)
		}
		// Mark old as superseded — propagate errors; caller may retry.
		// finding 5 fix: MarkSuperseded failure returns an error so the Phase2 call
		// fails and the caller can retry. This prevents silent partial success where
		// new memory is stored but the old one is not marked. MarkSuperseded
		// atomically sets status="superseded" + superseded_by=created.ID, which
		// Update(ctx, mem) cannot do (Update only writes content/tags/source_agent).
		if msErr := memoryStore.MarkSuperseded(ctx, *req.TargetMemoryID, created.ID); msErr != nil {
			return nil, fmt.Errorf("writelint Phase2 supersede mark-older %d: %w", *req.TargetMemoryID, msErr)
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
		if req.TargetMemoryID != nil {
			target, err := memoryStore.Get(ctx, *req.TargetMemoryID)
			if err != nil {
				return nil, fmt.Errorf("writelint Phase2 link_contradiction get target: %w", err)
			}
			if target == nil {
				return nil, fmt.Errorf("link_contradiction: target memory %d not found", *req.TargetMemoryID)
			}
			if target.Project != req.Project {
				return nil, fmt.Errorf("link_contradiction_project_mismatch: target memory %d belongs to project %q, token is for project %q",
					*req.TargetMemoryID, target.Project, req.Project)
			}
		}
		// Store new memory first with full metadata.
		created, err := memoryStore.Create(ctx, o.memForCreate(req))
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
		fallbackMem := o.memForCreate(req)
		fallbackMem.Content = fallbackMem.Content + "\n[mark_candidate: candidateStore not wired — stored as plain memory]"
		created, err := memoryStore.Create(ctx, fallbackMem)
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
