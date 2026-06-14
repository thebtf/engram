package worker

import (
	"context"
	"errors"
	"time"

	"github.com/rs/zerolog/log"

	"github.com/thebtf/engram/internal/crystallization"
	gorm "github.com/thebtf/engram/internal/db/gorm"
	"github.com/thebtf/engram/internal/llm"
	"github.com/thebtf/engram/pkg/models"
)

// dreamExtractFunc is the injectable seam for the dream-cycle LLM extractor.
// When non-nil on Service, it is used instead of constructing a real LLMExtractor.
// Injectable test seam: production code never sets this field; unit tests inject a
// fake to drive extraction without a live LLM endpoint.
//
// Signature matches crystallization.Extractor.Extract:
//
//	func(ctx context.Context, digest string) ([]crystallization.ExtractedDecision, error)
type dreamExtractFunc func(ctx context.Context, digest string) ([]crystallization.ExtractedDecision, error)

// dreamTranscriptStore is a minimal interface over the subset of TranscriptStore
// methods used by runDreamCrystallization. The concrete *gorm.TranscriptStore
// satisfies this interface; the unexported fakeTranscriptStore (below) satisfies
// it for unit tests without a live database connection.
type dreamTranscriptStore interface {
	ListUnprocessedSince(ctx context.Context, watermark time.Time) ([]gorm.SessionTranscript, error)
	MarkProcessed(ctx context.Context, ids []int64) error
	PruneProcessed(ctx context.Context) (int64, error)
	PruneUnprocessedOlderThan(ctx context.Context, days int) (int64, error)
}

// dreamCandidateWriter mirrors crystallization.CandidateWriter within the worker
// package so that service.go can reference the type without importing the
// crystallization package directly (keeping service.go's import set stable).
// The concrete *gorm.CandidateStore and the test-local fakeCandidateStore both
// satisfy this interface.
type dreamCandidateWriter interface {
	Create(ctx context.Context, c *models.CrystallizationCandidate) (*models.CrystallizationCandidate, error)
	GetByFingerprint(ctx context.Context, fingerprint string) (*models.CrystallizationCandidate, error)
}

// runDreamCrystallization is the dream-cycle job: it reads unprocessed session
// transcripts since the last successful run, builds an adaptive digest, extracts
// decisions via an LLM, routes each decision to a candidate (via RouteDecision),
// marks transcripts processed, and advances an in-process time watermark.
//
// Safe defaults:
//   - No LLM configured (ErrLLMDisabled) → debug log, return; zero side-effects.
//   - transcriptStore nil → debug log, return.
//   - Zero transcripts since watermark → debug log, return.
//   - LLM extraction error → warn log, return WITHOUT advancing watermark (retry next tick).
//
// RouteDecision returns (nil, nil) when ENGRAM_VNEXT_F_ENABLED is not set or
// candidateStore is nil. In that case the dream-cycle logs a debug note and
// skips the decision — the candidate path requires the F flag. This is by design:
// the dream-cycle targets the candidate path only; legacy memory writes from the
// per-session crystallization hook cover the flag-off scenario.
func (s *Service) runDreamCrystallization(ctx context.Context) {
	// -------------------------------------------------------------------------
	// Step 1: Resolve transcript store — prefer test override, fall back to real.
	// The dreamTranscriptStoreOverride field is set only in unit tests; in
	// production it is always nil and the real transcriptStore is used.
	// -------------------------------------------------------------------------
	var ts dreamTranscriptStore
	if s.dreamTranscriptStoreOverride != nil {
		ts = s.dreamTranscriptStoreOverride
	} else {
		s.initMu.RLock()
		realTS := s.transcriptStore
		s.initMu.RUnlock()
		if realTS == nil {
			log.Debug().Msg("dream-cycle: transcript store not ready, skipping")
			return
		}
		ts = realTS
	}

	// Resolve candidate writer — prefer test seam, fall back to real store.
	// dreamCandidateWriter is a worker-local interface mirror of crystallization.CandidateWriter
	// so service.go need not import the crystallization package.
	var candidateWriter dreamCandidateWriter
	if s.dreamCandidateStoreOverride != nil {
		candidateWriter = s.dreamCandidateStoreOverride
	} else {
		s.initMu.RLock()
		if s.candidateStore != nil {
			candidateWriter = s.candidateStore
		}
		s.initMu.RUnlock()
	}

	// Capture memory store for RouteDecision fingerprint check.
	// Use a typed-nil guard: RouteDecision accepts a MemoryFingerprintChecker interface,
	// and passing a (*gorm.MemoryStore)(nil) would be a non-nil interface with a nil
	// concrete pointer, causing a panic inside RouteDecision. Assign to the interface
	// only when the concrete pointer is non-nil.
	s.initMu.RLock()
	rawMemStore := s.memoryStore
	s.initMu.RUnlock()
	var memChecker crystallization.MemoryFingerprintChecker
	if rawMemStore != nil {
		memChecker = rawMemStore
	}

	// -------------------------------------------------------------------------
	// Step 2: Resolve extractor — test seam or real LLM client.
	// -------------------------------------------------------------------------
	var extractFn dreamExtractFunc
	if s.dreamExtractorFunc != nil {
		extractFn = s.dreamExtractorFunc
	} else {
		client, err := llm.NewClient()
		if err != nil {
			if errors.Is(err, llm.ErrLLMDisabled) {
				log.Debug().Msg("dream-cycle: LLM disabled, no-op")
				return
			}
			log.Warn().Err(err).Msg("dream-cycle: failed to create LLM client, skipping")
			return
		}
		extractor := crystallization.NewLLMExtractor(client)
		extractFn = extractor.Extract
	}

	// -------------------------------------------------------------------------
	// Step 3: Determine transcript watermark (time-based, in-process).
	// Reads the atomic dreamWatermark field; zero = time.Unix(0,0) = epoch start.
	// -------------------------------------------------------------------------
	watermarkNano := s.dreamWatermark.Load()
	watermark := time.Unix(0, watermarkNano)

	// -------------------------------------------------------------------------
	// Step 4: List unprocessed transcripts since the watermark.
	// -------------------------------------------------------------------------
	transcripts, err := ts.ListUnprocessedSince(ctx, watermark)
	if err != nil {
		log.Warn().Err(err).Msg("dream-cycle: failed to list unprocessed transcripts, skipping")
		return
	}
	if len(transcripts) == 0 {
		log.Debug().Time("watermark", watermark).Msg("dream-cycle: no unprocessed transcripts since watermark")
		return
	}

	// -------------------------------------------------------------------------
	// Step 5: Adaptive digest — compute mode and build the digest string.
	// -------------------------------------------------------------------------
	sessionSet := make(map[string]struct{}, len(transcripts))
	projectSet := make(map[string]struct{}, len(transcripts))
	var minCreated, maxCreated time.Time
	for _, t := range transcripts {
		sessionSet[t.SessionID] = struct{}{}
		projectSet[t.Project] = struct{}{}
		if minCreated.IsZero() || t.CreatedAt.Before(minCreated) {
			minCreated = t.CreatedAt
		}
		if t.CreatedAt.After(maxCreated) {
			maxCreated = t.CreatedAt
		}
	}
	sessionCount := len(sessionSet)
	span := maxCreated.Sub(minCreated)
	// sharedProject is true only when transcripts span MORE THAN ONE distinct
	// project — a single-project batch is NOT "shared". A single session from one
	// project (sessionCount==1, one project) therefore stays per-session for a
	// short span, instead of being forced to per-batch by a degenerate flag.
	sharedProject := len(projectSet) > 1

	mode := crystallization.SelectMode(sessionCount, span, sharedProject)
	contents := make([]string, len(transcripts))
	for i, t := range transcripts {
		contents[i] = t.Content
	}
	digest := crystallization.BuildDigest(contents, mode)

	// -------------------------------------------------------------------------
	// Step 6: Extract decisions via LLM.
	// Errors here do NOT advance the watermark — retry on next tick.
	// -------------------------------------------------------------------------
	decisions, err := extractFn(ctx, digest)
	if err != nil {
		log.Warn().Err(err).
			Int("transcripts", len(transcripts)).
			Msg("dream-cycle: extraction failed, watermark not advanced (will retry)")
		return
	}

	// -------------------------------------------------------------------------
	// Step 7: Route each decision to a candidate via RouteDecision.
	//
	// Provenance: for per-batch mode there is no single canonical SessionID, so
	// we use the first transcript's SessionID and Project as batch provenance.
	// This is documented as the chosen convention; an empty "" was considered but
	// ruled out because RouteDecision passes sessionID into models.NewCrystallizationCandidate
	// which uses it for fingerprinting — a non-empty value yields a stable fingerprint.
	//
	// DetectLoss: applies to the supersede/update path where an existing candidate
	// would be overwritten with a lossy replacement. The dream-cycle only CREATES
	// new candidates (RouteDecision handles idempotency via fingerprint checks).
	// DetectLoss is therefore not wired here.
	// TODO(crystal): wire DetectLoss in a future supersede path that updates an
	// existing candidate's text/evidence fields — see loss.go for the contract.
	//
	// RouteDecision returns (nil, nil) when ENGRAM_VNEXT_F_ENABLED is unset or
	// candidateStore is nil. Candidates require the F flag; the legacy memory path
	// in the per-session crystallization hook covers the flag-off scenario.
	// -------------------------------------------------------------------------
	provenanceSessionID := transcripts[0].SessionID
	provenanceProject := transcripts[0].Project

	candidatesCreated := 0
	for _, decision := range decisions {
		result, routeErr := crystallization.RouteDecision(
			ctx,
			decision,
			provenanceSessionID,
			provenanceProject,
			candidateWriter,
			memChecker,
		)
		if routeErr != nil {
			log.Warn().Err(routeErr).
				Str("text_prefix", dreamTruncate(decision.Text, 80)).
				Msg("dream-cycle: route decision failed, skipping this decision")
			continue
		}
		if result == nil {
			// F flag off or candidateStore nil — candidates require ENGRAM_VNEXT_F_ENABLED=true.
			log.Debug().Msg("dream-cycle: RouteDecision returned nil (F-flag off or candidateStore nil) — candidate path inactive")
			continue
		}
		if result.Duplicate {
			log.Debug().
				Str("text_prefix", dreamTruncate(decision.Text, 80)).
				Msg("dream-cycle: duplicate decision fingerprint, skipping")
			continue
		}
		if result.CandidateID > 0 {
			candidatesCreated++
		}
	}

	// -------------------------------------------------------------------------
	// Step 8: Mark transcripts processed and prune.
	// MarkProcessed failure is non-fatal: we still attempt prune and watermark
	// advance so the batch is not permanently stuck.
	// -------------------------------------------------------------------------
	ids := make([]int64, len(transcripts))
	for i, t := range transcripts {
		ids[i] = t.ID
	}
	if markErr := ts.MarkProcessed(ctx, ids); markErr != nil {
		log.Warn().Err(markErr).Msg("dream-cycle: failed to mark transcripts processed")
	}

	pruned, pruneErr := ts.PruneProcessed(ctx)
	if pruneErr != nil {
		log.Warn().Err(pruneErr).Msg("dream-cycle: failed to prune processed transcripts")
	}

	// Prune stale unprocessed transcripts using the configured retention window.
	// s.config.TranscriptRetentionDays == 0 (the default) is a documented no-op in
	// TranscriptStore.PruneUnprocessedOlderThan, so no special-casing is needed.
	// Env: ENGRAM_TRANSCRIPT_RETENTION_DAYS (parsed by config.Load).
	retentionDays := 0
	if s.config != nil {
		retentionDays = s.config.TranscriptRetentionDays
	}
	pruneOldCount, pruneOldErr := ts.PruneUnprocessedOlderThan(ctx, retentionDays)
	if pruneOldErr != nil {
		log.Warn().Err(pruneOldErr).Msg("dream-cycle: failed to prune old unprocessed transcripts")
	}

	// -------------------------------------------------------------------------
	// Step 9: Advance watermark to the max created_at of the batch.
	// Only reached when extraction succeeded.
	// -------------------------------------------------------------------------
	s.dreamWatermark.Store(maxCreated.UnixNano())

	// -------------------------------------------------------------------------
	// Step 10: Structured log of counts.
	// -------------------------------------------------------------------------
	log.Info().
		Int("transcripts_read", len(transcripts)).
		Int("decisions_extracted", len(decisions)).
		Int("candidates_created", candidatesCreated).
		Int64("transcripts_pruned", pruned).
		Int64("old_unprocessed_pruned", pruneOldCount).
		Str("digest_mode", string(mode)).
		Time("new_watermark", maxCreated).
		Msg("dream-cycle: crystallization complete")
}

// dreamTruncate returns the first n bytes of text, appending "…" when truncated.
// Used only for log messages — not a substitute for the privacy redaction layer.
func dreamTruncate(text string, n int) string {
	if len(text) <= n {
		return text
	}
	return text[:n] + "…"
}

// ---------------------------------------------------------------------------
// fakeTranscriptStore — unit-test helper (unexported).
// Satisfies the dreamTranscriptStore interface without a live database.
// ---------------------------------------------------------------------------

type fakeTranscriptStore struct {
	rows        []gorm.SessionTranscript
	marked      []int64
	prunedCount int64
}

func (f *fakeTranscriptStore) ListUnprocessedSince(_ context.Context, watermark time.Time) ([]gorm.SessionTranscript, error) {
	var out []gorm.SessionTranscript
	for _, r := range f.rows {
		if !r.CreatedAt.Before(watermark) {
			out = append(out, r)
		}
	}
	return out, nil
}

func (f *fakeTranscriptStore) MarkProcessed(_ context.Context, ids []int64) error {
	f.marked = append(f.marked, ids...)
	return nil
}

func (f *fakeTranscriptStore) PruneProcessed(_ context.Context) (int64, error) {
	f.prunedCount++
	return 0, nil
}

func (f *fakeTranscriptStore) PruneUnprocessedOlderThan(_ context.Context, _ int) (int64, error) {
	return 0, nil
}
