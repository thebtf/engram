// Package reaper provides a periodic job that hard-deletes project rows that
// have been soft-deleted (removed_at IS NOT NULL) and have passed the configurable
// retention window (default 30 days, overridden by ENGRAM_PROJECT_RETENTION_DAYS).
//
// FK audit (2026-04-15, migration scan):
// The following tables were audited for foreign-key references to projects.id:
//   - observations     — project column is TEXT, no FK constraint
//   - sdk_sessions     — project column is TEXT, no FK constraint
//   - injection_log    — project column is TEXT, no FK constraint (table was
//                         dropped at migration 084 then restored at migration 106)
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
	"strings"
	"sync"
	"time"

	"github.com/rs/zerolog/log"
	"gorm.io/gorm"
)

const (
	// defaultRetentionDays is the number of days a soft-deleted project is kept
	// before the reaper hard-deletes the row.
	defaultRetentionDays = 30
	reaperInterval        = time.Hour
	maxRetentionDays      = int64((1<<63 - 1) / (24 * time.Hour))
)

// Reaper periodically hard-deletes project rows whose removed_at timestamp
// has passed the retention window.
type Reaper struct {
	db *gorm.DB

	mu      sync.Mutex
	cancel  context.CancelFunc
	done    chan struct{}
	running bool
}

// New creates a Reaper backed by the given database connection.
func New(db *gorm.DB) *Reaper {
	return &Reaper{db: db}
}

// Start launches the reaper loop in a background goroutine. It respects ctx for
// graceful shutdown and also responds to Stop(). Returns immediately.
func (r *Reaper) Start(ctx context.Context) {
	if ctx == nil {
		ctx = context.Background()
	}

	r.mu.Lock()
	if r.running {
		r.mu.Unlock()
		return
	}
	ctx, r.cancel = context.WithCancel(ctx)
	r.done = make(chan struct{})
	r.running = true
	done := r.done
	r.mu.Unlock()

	log.Info().
		Dur("interval", reaperInterval).
		Int("retention_days", retentionDays()).
		Msg("project reaper started")

	go func() {
		defer func() {
			r.mu.Lock()
			if r.done == done {
				r.running = false
			}
			close(done)
			r.mu.Unlock()
		}()

		ticker := time.NewTicker(reaperInterval)
		defer ticker.Stop()

		for {
			select {
			case <-ctx.Done():
				log.Info().Msg("project reaper stopped (context cancelled)")
				return
			case <-ticker.C:
				if err := r.purge(ctx); err != nil {
					log.Error().Err(err).Msg("project reaper: purge sweep failed")
				}
			}
		}
	}()
}

// Stop signals the reaper to cease and waits for the goroutine to exit.
func (r *Reaper) Stop() {
	r.mu.Lock()
	if r.done == nil {
		r.mu.Unlock()
		return
	}
	cancel, done := r.cancel, r.done
	r.mu.Unlock()

	cancel()
	<-done
}

// purge deletes projects that were soft-deleted more than retentionDays() ago.
// It is idempotent and safe to call concurrently.
func (r *Reaper) purge(ctx context.Context) error {
	if r.db == nil {
		return fmt.Errorf("reaper: db is nil")
	}

	retention, err := retentionDuration()
	if err != nil {
		return err
	}
	cutoff := time.Now().UTC().Add(-retention)

	result := r.db.WithContext(ctx).
		Exec(
			"DELETE FROM projects WHERE removed_at IS NOT NULL AND removed_at < ?",
			cutoff,
		)
	if result.Error != nil {
		return fmt.Errorf("project reaper: purge query failed: %w", result.Error)
	}

	if result.RowsAffected > 0 {
		log.Info().
			Int64("purged", result.RowsAffected).
			Time("cutoff", cutoff).
			Msg("project reaper: purged soft-deleted projects")
	}
	return nil
}

// retentionDays returns the configured retention window in days.
// Reads ENGRAM_PROJECT_RETENTION_DAYS; falls back to defaultRetentionDays.
func retentionDays() int {
	days, err := strconv.Atoi(os.Getenv("ENGRAM_PROJECT_RETENTION_DAYS"))
	if err != nil || days <= 0 {
		return defaultRetentionDays
	}
	return days
}

func retentionDuration() (time.Duration, error) {
	value := os.Getenv("ENGRAM_PROJECT_RETENTION_DAYS")
	if value == "" {
		return defaultRetentionDays * 24 * time.Hour, nil
	}
	days, err := strconv.ParseInt(value, 10, 64)
	if err != nil {
		if isPositiveDecimal(value) {
			return 0, fmt.Errorf("reaper: retention days exceed maximum %d", maxRetentionDays)
		}
		return defaultRetentionDays * 24 * time.Hour, nil
	}
	if days <= 0 {
		return defaultRetentionDays * 24 * time.Hour, nil
	}
	if days > maxRetentionDays {
		return 0, fmt.Errorf("reaper: retention days exceed maximum %d", maxRetentionDays)
	}
	return time.Duration(days) * 24 * time.Hour, nil
}

func isPositiveDecimal(value string) bool {
	value = strings.TrimPrefix(value, "+")
	if value == "" {
		return false
	}
	for _, r := range value {
		if r < '0' || r > '9' {
			return false
		}
	}
	return strings.TrimLeft(value, "0") != ""
}

// PurgeOnce runs a single purge sweep synchronously. Useful for integration
// testing where time-based scheduling is not practical.
func (r *Reaper) PurgeOnce(ctx context.Context) error {
	return r.purge(ctx)
}
