// Package reaper provides a periodic job that hard-deletes project rows that
// have been soft-deleted (removed_at IS NOT NULL) and have passed the configurable
// retention window (default 30 days, overridden by ENGRAM_PROJECT_RETENTION_DAYS).
//
// FK audit (2026-04-15, migration scan):
// The following tables were audited for foreign-key references to projects.id:
//   - observations     — project column is TEXT, no FK constraint
//   - sdk_sessions     — project column is TEXT, no FK constraint
//   - injection_log    — dropped in v5 (US1); was TEXT, no FK constraint
//   - patterns         — no project FK column
//   - memory_blocks    — not present in migrations (non-existent table)
//   - collections      — not present in migrations (non-existent table)
//   - embeddings       — not present in migrations (no FK to projects)
//   - issues           — source_project/target_project are TEXT, no FK
//
// VERDICT: No ON DELETE CASCADE FK from any table to projects.id.
// The reaper simply DELETE FROM projects WHERE removed_at < cutoff.
// Orphaned rows in other tables (observations, sessions with old project IDs)
// are managed separately by their own maintenance jobs; they carry project IDs
// as denormalised TEXT fields and are not hard-deleted by the project reaper.
package reaper

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"time"

	"github.com/rs/zerolog/log"
	"gorm.io/gorm"
)

const (
	// defaultRetentionDays is the number of days a soft-deleted project is kept
	// before the reaper hard-deletes the row.
	defaultRetentionDays = 30

	// reaperInterval is how often the reaper runs its cleanup sweep.
	reaperInterval = 1 * time.Hour
)

// Reaper periodically hard-deletes project rows whose removed_at timestamp
// has passed the retention window.
type Reaper struct {
	db   *gorm.DB
	stop chan struct{}
	done chan struct{}
}

// New creates a Reaper backed by the given database connection.
func New(db *gorm.DB) *Reaper {
	return &Reaper{
		db:   db,
		stop: make(chan struct{}),
		done: make(chan struct{}),
	}
}

// Start launches the reaper loop in a background goroutine. It respects ctx for
// graceful shutdown and also responds to Stop(). Returns immediately.
func (r *Reaper) Start(ctx context.Context) {
	log.Info().
		Dur("interval", reaperInterval).
		Int("retention_days", retentionDays()).
		Msg("project reaper started")

	go func() {
		defer close(r.done)

		ticker := time.NewTicker(reaperInterval)
		defer ticker.Stop()

		for {
			select {
			case <-ctx.Done():
				log.Info().Msg("project reaper stopped (context cancelled)")
				return
			case <-r.stop:
				log.Info().Msg("project reaper stopped")
				return
			case <-ticker.C:
				r.purge(ctx)
			}
		}
	}()
}

// Stop signals the reaper to cease and waits for the goroutine to exit.
func (r *Reaper) Stop() {
	select {
	case <-r.stop:
		// Already closed — idempotent.
	default:
		close(r.stop)
	}
	<-r.done
}

// purge deletes projects that were soft-deleted more than retentionDays() ago.
// It is idempotent and safe to call concurrently.
func (r *Reaper) purge(ctx context.Context) {
	if r.db == nil {
		return
	}

	retention := time.Duration(retentionDays()) * 24 * time.Hour
	cutoff := time.Now().UTC().Add(-retention)

	result := r.db.WithContext(ctx).
		Exec(
			"DELETE FROM projects WHERE removed_at IS NOT NULL AND removed_at < ?",
			cutoff,
		)
	if result.Error != nil {
		log.Error().Err(result.Error).Msg("project reaper: purge query failed")
		return
	}

	if result.RowsAffected > 0 {
		log.Info().
			Int64("purged", result.RowsAffected).
			Time("cutoff", cutoff).
			Msg("project reaper: purged soft-deleted projects")
	}
}

// retentionDays returns the configured retention window in days.
// Reads ENGRAM_PROJECT_RETENTION_DAYS; falls back to defaultRetentionDays.
func retentionDays() int {
	if v := os.Getenv("ENGRAM_PROJECT_RETENTION_DAYS"); v != "" {
		if days, err := strconv.Atoi(v); err == nil && days > 0 {
			return days
		}
	}
	return defaultRetentionDays
}

// PurgeOnce runs a single purge sweep synchronously. Useful for integration
// testing where time-based scheduling is not practical.
func (r *Reaper) PurgeOnce(ctx context.Context) error {
	if r.db == nil {
		return fmt.Errorf("reaper: db is nil")
	}
	r.purge(ctx)
	return nil
}
