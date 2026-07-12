package worker

import (
	"context"
	"errors"
	"os"
	"sort"
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
//   - crystallization flag off, candidate flag off, or candidate store unavailable → return before reading transcripts.
//   - No LLM configured (ErrLLMDisabled) → debug log, return; zero side-effects.
//   - transcriptStore nil → debug log, return.
//   - Zero transcripts since watermark → debug log, return.
//   - LLM extraction error → warn log, return WITHOUT advancing watermark (retry next tick).
//
// The candidate path is the only durable output. There is intentionally no
// direct-memory fallback: consuming a transcript without a created-or-duplicate
// candidate would lose work and would resurrect the demolished v5 path.
func (s *Service) runDreamCrystallization(ctx context.Context) {
	if !isCrystallizationEnabled() {
		log.Debug().Msg("dream-cycle: crystallization disabled, no-op")
		return
	}
	if os.Getenv("ENGRAM_VNEXT_F_ENABLED") != "true" {
		log.Debug().Msg("dream-cycle: candidate path disabled, no-op")
		return
	}

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
	if candidateWriter == nil {
		log.Debug().Msg("dream-cycle: candidate store not ready, no-op")
		return
	}

	s.initMu.RLock()
	rawMemStore := s.memoryStore
	s.initMu.RUnlock()
	var memChecker crystallization.MemoryFingerprintChecker
	if rawMemStore != nil {
		memChecker = rawMemStore
	}

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

	watermarkNano := s.dreamWatermark.Load()
	watermark := time.Unix(0, watermarkNano)
	transcripts, err := ts.ListUnprocessedSince(ctx, watermark)
	if err != nil {
		log.Warn().Err(err).Msg("dream-cycle: failed to list unprocessed transcripts, skipping")
		return
	}
	if len(transcripts) == 0 {
		log.Debug().Time("watermark", watermark).Msg("dream-cycle: no unprocessed transcripts since watermark")
		return
	}

	type groupKey struct {
		project   string
		sessionID string
	}
	groups := make(map[groupKey][]gorm.SessionTranscript)
	keys := make([]groupKey, 0)
	var maxCreated time.Time
	for _, t := range transcripts {
		key := groupKey{project: t.Project, sessionID: t.SessionID}
		if _, ok := groups[key]; !ok {
			keys = append(keys, key)
		}
		groups[key] = append(groups[key], t)
		if t.CreatedAt.After(maxCreated) {
			maxCreated = t.CreatedAt
		}
	}
	sort.Slice(keys, func(i, j int) bool {
		if keys[i].project == keys[j].project {
			return keys[i].sessionID < keys[j].sessionID
		}
		return keys[i].project < keys[j].project
	})

	allSucceeded := true
	decisionsExtracted := 0
	candidatesCreated := 0
	processedTranscripts := 0
	digestMode := "per-session"
	for _, key := range keys {
		batch := groups[key]
		sort.Slice(batch, func(i, j int) bool {
			if batch[i].CreatedAt.Equal(batch[j].CreatedAt) {
				return batch[i].ID < batch[j].ID
			}
			return batch[i].CreatedAt.Before(batch[j].CreatedAt)
		})
		contents := make([]string, len(batch))
		for i := range batch {
			contents[i] = batch[i].Content
		}
		span := batch[len(batch)-1].CreatedAt.Sub(batch[0].CreatedAt)
		mode := crystallization.SelectMode(1, span, false)
		digestMode = string(mode)
		decisions, extractErr := extractFn(ctx, crystallization.BuildDigest(contents, mode))
		if extractErr != nil {
			allSucceeded = false
			log.Warn().Err(extractErr).
				Str("project", key.project).
				Str("session_id", key.sessionID).
				Msg("dream-cycle: extraction failed, group retained for retry")
			continue
		}
		decisionsExtracted += len(decisions)
		groupSucceeded := true
		for _, decision := range decisions {
			result, routeErr := crystallization.RouteDecision(ctx, decision, key.sessionID, key.project, candidateWriter, memChecker)
			if routeErr != nil || result == nil {
				allSucceeded = false
				groupSucceeded = false
				log.Warn().Err(routeErr).
					Str("project", key.project).
					Str("session_id", key.sessionID).
					Str("text_prefix", dreamTruncate(decision.Text, 80)).
					Msg("dream-cycle: route failed, group retained for fingerprint-safe retry")
				break
			}
			if !result.Duplicate && result.CandidateID > 0 {
				candidatesCreated++
			}
		}
		if !groupSucceeded {
			continue
		}
		ids := make([]int64, len(batch))
		for i := range batch {
			ids[i] = batch[i].ID
		}
		if markErr := ts.MarkProcessed(ctx, ids); markErr != nil {
			allSucceeded = false
			log.Warn().Err(markErr).
				Str("project", key.project).
				Str("session_id", key.sessionID).
				Msg("dream-cycle: mark failed, group retained for fingerprint-safe retry")
			continue
		}
		processedTranscripts += len(batch)
	}

	pruned, pruneErr := ts.PruneProcessed(ctx)
	if pruneErr != nil {
		log.Warn().Err(pruneErr).Msg("dream-cycle: failed to prune processed transcripts")
	}

	var pruneOldCount int64
	if allSucceeded {
		retentionDays := 0
		if s.config != nil {
			retentionDays = s.config.TranscriptRetentionDays
		}
		var pruneOldErr error
		pruneOldCount, pruneOldErr = ts.PruneUnprocessedOlderThan(ctx, retentionDays)
		if pruneOldErr != nil {
			log.Warn().Err(pruneOldErr).Msg("dream-cycle: failed to prune old unprocessed transcripts")
		}
		s.dreamWatermark.Store(maxCreated.UnixNano())
	}

	log.Info().
		Int("transcripts_read", len(transcripts)).
		Int("transcripts_processed", processedTranscripts).
		Int("decisions_extracted", decisionsExtracted).
		Int("candidates_created", candidatesCreated).
		Int64("transcripts_pruned", pruned).
		Int64("old_unprocessed_pruned", pruneOldCount).
		Str("digest_mode", digestMode).
		Bool("all_groups_succeeded", allSucceeded).
		Int("provenance_groups", len(keys)).
		Time("new_watermark", time.Unix(0, s.dreamWatermark.Load())).
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
	rows         []gorm.SessionTranscript
	marked       []int64
	processed    map[int64]bool
	markFailures int
	markErr      error
	prunedCount  int64
}

func (f *fakeTranscriptStore) ListUnprocessedSince(_ context.Context, watermark time.Time) ([]gorm.SessionTranscript, error) {
	var out []gorm.SessionTranscript
	for _, r := range f.rows {
		if !f.processed[r.ID] && !r.CreatedAt.Before(watermark) {
			out = append(out, r)
		}
	}
	return out, nil
}

func (f *fakeTranscriptStore) MarkProcessed(_ context.Context, ids []int64) error {
	if f.markFailures > 0 {
		f.markFailures--
		if f.markErr != nil {
			return f.markErr
		}
		return errors.New("injected mark failure")
	}
	f.marked = append(f.marked, ids...)
	if f.processed == nil {
		f.processed = make(map[int64]bool)
	}
	for _, id := range ids {
		f.processed[id] = true
	}
	return nil
}

func (f *fakeTranscriptStore) PruneProcessed(_ context.Context) (int64, error) {
	f.prunedCount++
	return 0, nil
}

func (f *fakeTranscriptStore) PruneUnprocessedOlderThan(_ context.Context, _ int) (int64, error) {
	return 0, nil
}
