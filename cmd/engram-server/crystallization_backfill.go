// crystallization_backfill.go — Milestone-F TG4 T027
// Backfills existing episodic decision memories as crystallization candidates.
//
// Invoked as a sub-command of the engram-server binary:
//
//	engram-server backfill-candidates [--commit] [--project <slug>] [--batch <n>]
//
// Default mode is dry-run: logs what would be created without writing to DB.
// Pass --commit to actually insert pending candidates.
//
// Idempotency: fingerprint-based deduplication mirrors the crystallization gate.
// Candidates already pending with the same fingerprint are silently skipped.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"
	"github.com/thebtf/engram/internal/config"
	gormdb "github.com/thebtf/engram/internal/db/gorm"
	"github.com/thebtf/engram/pkg/models"
)

// runBackfillCandidates is the entry point for the "backfill-candidates" sub-command.
// It scans episodic decision memories and creates a pending crystallization candidate
// for each one that does not already have a matching pending fingerprint.
//
// Exit codes: 0 = success / dry-run, 1 = fatal error.
func runBackfillCandidates(args []string) {
	fs := flag.NewFlagSet("backfill-candidates", flag.ExitOnError)
	commit := fs.Bool("commit", false, "Persist candidates to the database (default: dry-run)")
	project := fs.String("project", "", "Filter to a specific project slug (default: all projects)")
	batchSize := fs.Int("batch", 100, "Number of memories to process per batch")
	_ = fs.Parse(args)

	zerolog.TimeFieldFormat = zerolog.TimeFormatUnix
	log.Logger = log.Output(zerolog.ConsoleWriter{Out: os.Stderr})

	cfg := config.Get()
	if cfg.DatabaseDSN == "" {
		log.Fatal().Msg("backfill-candidates: DATABASE_DSN is not set")
	}
	if os.Getenv("ENGRAM_VNEXT_F_ENABLED") != "true" {
		log.Fatal().Msg("backfill-candidates: requires ENGRAM_VNEXT_F_ENABLED=true")
	}

	store, err := gormdb.NewStore(gormdb.Config{
		DSN:      cfg.DatabaseDSN,
		MaxConns: 5,
	})
	if err != nil {
		log.Fatal().Err(err).Msg("backfill-candidates: failed to open database")
	}

	auditStore := gormdb.NewAuditStore(store.GetDB())
	candidateStore := gormdb.NewCandidateStore(store.GetDB(), auditStore)
	memoryStore := gormdb.NewMemoryStore(store)

	ctx := context.Background()
	mode := "DRY-RUN"
	if *commit {
		mode = "COMMIT"
	}
	log.Info().
		Str("mode", mode).
		Str("project_filter", *project).
		Int("batch_size", *batchSize).
		Msg("backfill-candidates: starting")

	created, skipped, errCount := 0, 0, 0
	offset := 0

	for {
		// ListAllActive fetches across all projects; project filtering is applied below.
		mems, listErr := memoryStore.ListAllActive(ctx, *batchSize, offset)
		if listErr != nil {
			log.Error().Err(listErr).Msg("backfill-candidates: list memories failed")
			errCount++
			break
		}
		if len(mems) == 0 {
			break
		}

		for _, mem := range mems {
			if mem == nil {
				continue
			}
			// Filter to episodic decision memories only.
			if mem.Tier != "episodic" || mem.EpistemicType != "decision" {
				continue
			}
			// Apply optional project filter.
			if *project != "" && mem.Project != *project {
				continue
			}

			// Derive session_id from first entry in SourceSessions.
			sessionID := ""
			if len(mem.SourceSessions) > 0 {
				sessionID = mem.SourceSessions[0]
			}

			// Build candidate to extract fingerprint.
			// Delegates canonical fingerprint computation to models.NewCrystallizationCandidate.
			proj := mem.Project
			var affectedProjects []string
			if proj != "" {
				affectedProjects = []string{proj}
			}
			c, buildErr := models.NewCrystallizationCandidate(
				sessionID,
				mem.Content,
				"rule",
				models.CandidateOptions{
					Tier:             "episodic",
					EpistemicType:    "decision",
					AffectedProjects: affectedProjects,
					Confidence:       0.5,
				},
			)
			if buildErr != nil {
				log.Warn().Err(buildErr).Int64("memory_id", mem.ID).Msg("backfill-candidates: build candidate failed, skipping")
				errCount++
				continue
			}

			// Idempotency: skip if any candidate (any status) with same fingerprint already
			// exists. GetByFingerprintAnyStatus covers terminal states (promoted, rejected,
			// superseded, decayed) so that a re-run after review does not re-queue the same
			// legacy memory as a fresh pending candidate.
			if c.Fingerprint != "" {
				existing, fpErr := candidateStore.GetByFingerprintAnyStatus(ctx, c.Fingerprint)
				if fpErr != nil {
					log.Warn().Err(fpErr).Int64("memory_id", mem.ID).Msg("backfill-candidates: fingerprint check failed, skipping")
					errCount++
					continue
				}
				if existing != nil {
					log.Debug().
						Int64("memory_id", mem.ID).
						Int64("candidate_id", existing.ID).
						Str("candidate_status", string(existing.Status)).
						Msg("backfill-candidates: candidate exists, skipping")
					skipped++
					continue
				}
			}

			log.Info().
				Int64("memory_id", mem.ID).
				Str("project", proj).
				Str("fingerprint", c.Fingerprint).
				Msg("backfill-candidates: candidate to create")

			if !*commit {
				created++ // dry-run: count what would be created
				continue
			}

			if _, createErr := candidateStore.Create(ctx, c); createErr != nil {
				// Duplicate key from partial-unique index is idempotent — treat as skip.
				if strings.Contains(createErr.Error(), "duplicate key") ||
					strings.Contains(createErr.Error(), "unique constraint") ||
					strings.Contains(createErr.Error(), "idx_candidates_fingerprint_pending") {
					skipped++
					continue
				}
				log.Warn().Err(createErr).Int64("memory_id", mem.ID).Msg("backfill-candidates: create failed")
				errCount++
				continue
			}
			created++
		}

		offset += len(mems)
		if len(mems) < *batchSize {
			break
		}
	}

	log.Info().
		Str("mode", mode).
		Int("candidates_created_or_would_create", created).
		Int("skipped_dup", skipped).
		Int("errors", errCount).
		Msg("backfill-candidates: done")

	if errCount > 0 {
		os.Exit(1)
	}
}

// backfillSmokeCheck verifies the crystallization_candidates table is reachable.
// Used by G004 to produce evidence of the table existing post-migration.
func backfillSmokeCheck(ctx context.Context) error {
	cfg := config.Get()
	if cfg.DatabaseDSN == "" {
		return fmt.Errorf("DATABASE_DSN not set")
	}
	store, err := gormdb.NewStore(gormdb.Config{DSN: cfg.DatabaseDSN, MaxConns: 1})
	if err != nil {
		return fmt.Errorf("open db: %w", err)
	}
	var count int64
	if err := store.GetDB().WithContext(ctx).
		Raw("SELECT COUNT(*) FROM crystallization_candidates WHERE created_at > ?", time.Time{}).
		Scan(&count).Error; err != nil {
		return fmt.Errorf("table check: %w", err)
	}
	return nil
}
