// Package reaper provides a periodic job that hard-deletes project rows that
// have been soft-deleted (removed_at IS NOT NULL) and have passed the configurable
// retention window (default 30 days, overridden by ENGRAM_PROJECT_RETENTION_DAYS).
//
// FK audit (2026-04-15, migration scan):
// The following tables were audited for foreign-key references to projects.id:
//   - observations     — project column is TEXT, no FK constraint
//   - sdk_sessions     — project column is TEXT, no FK constraint
//   - injection_log    — project column is TEXT, no FK constraint (table was
//     dropped at migration 084 then restored at migration 106)
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
	"sync"
	"time"

	"github.com/rs/zerolog/log"
	"gorm.io/gorm"
)

const (
	// defaultRetentionDays is the number of days a soft-deleted project is kept
	// before the reaper hard-deletes the row.
	defaultRetentionDays = 30
	retentionDay         = 24 * time.Hour

	// maxRetentionDays is the largest whole-day retention that can be converted
	// to time.Duration without overflow. Larger positive configurations fail safe
	// by skipping the sweep; clamping them downward could delete rows newer than
	// the operator-requested retention boundary.
	maxRetentionDays = int64((1<<63 - 1) / retentionDay)

	// reaperInterval is how often the reaper runs its cleanup sweep.
	reaperInterval = 1 * time.Hour
)

type retentionConfig struct {
	days        int64
	skipPurge   bool
	usedDefault bool
}

type reaperTicker interface {
	Chan() <-chan time.Time
	Stop()
}

type wallClockTicker struct {
	*time.Ticker
}

func (t *wallClockTicker) Chan() <-chan time.Time { return t.C }

func newWallClockTicker(interval time.Duration) reaperTicker {
	return &wallClockTicker{Ticker: time.NewTicker(interval)}
}

// Reaper periodically hard-deletes project rows whose removed_at timestamp
// has passed the retention window.
type Reaper struct {
	db *gorm.DB

	lifecycleMu sync.Mutex
	running     bool
	stopping    bool
	cancel      context.CancelFunc
	done        chan struct{}
	newTicker   func(time.Duration) reaperTicker
}

// New creates a Reaper backed by the given database connection.
func New(db *gorm.DB) *Reaper {
	return &Reaper{
		db:        db,
		newTicker: newWallClockTicker,
	}
}

// Start launches the reaper loop in a background goroutine. It respects ctx for
// graceful shutdown and also responds to Stop(). Returns immediately.
func (r *Reaper) Start(ctx context.Context) {
	if ctx == nil {
		ctx = context.Background()
	}

	r.lifecycleMu.Lock()
	if r.running || r.stopping {
		r.lifecycleMu.Unlock()
		return
	}
	runCtx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	newTicker := r.newTicker
	if newTicker == nil {
		newTicker = newWallClockTicker
	}
	r.running = true
	r.cancel = cancel
	r.done = done
	r.lifecycleMu.Unlock()

	retention := loadRetentionConfig()
	startLog := log.Info().
		Dur("interval", reaperInterval).
		Bool("purge_disabled", retention.skipPurge).
		Bool("retention_defaulted", retention.usedDefault)
	if !retention.skipPurge {
		startLog.Int64("retention_days", retention.days)
	}
	startLog.Msg("project reaper started")

	go r.run(runCtx, cancel, done, newTicker)
}

// Stop signals the reaper to cease and waits for the goroutine to exit.
func (r *Reaper) Stop() {
	r.lifecycleMu.Lock()
	if r.done == nil {
		r.lifecycleMu.Unlock()
		return
	}
	r.stopping = true
	cancel := r.cancel
	done := r.done
	r.lifecycleMu.Unlock()

	if cancel != nil {
		cancel()
	}
	<-done

	r.lifecycleMu.Lock()
	if r.done == done {
		r.running = false
		r.stopping = false
		r.cancel = nil
		r.done = nil
	}
	r.lifecycleMu.Unlock()
}

func (r *Reaper) run(ctx context.Context, cancel context.CancelFunc, done chan struct{}, newTicker func(time.Duration) reaperTicker) {
	ticker := newTicker(reaperInterval)
	defer func() {
		ticker.Stop()
		cancel()

		r.lifecycleMu.Lock()
		if r.done == done {
			r.running = false
			if !r.stopping {
				r.cancel = nil
				r.done = nil
			}
		}
		close(done)
		r.lifecycleMu.Unlock()
	}()

	for {
		select {
		case <-ctx.Done():
			log.Info().Msg("project reaper stopped (context cancelled)")
			return
		case <-ticker.Chan():
			if err := r.purge(ctx); err != nil {
				log.Error().Err(err).Msg("project reaper: purge sweep failed")
			}
		}
	}
}

// purge deletes projects older than the configured retention boundary. It is
// idempotent and safe to call concurrently.
func (r *Reaper) purge(ctx context.Context) error {
	if r.db == nil {
		return fmt.Errorf("reaper: db is nil")
	}

	config := loadRetentionConfig()
	if config.skipPurge {
		log.Warn().Msg("project reaper: configured retention exceeds safe duration; purge skipped")
		return nil
	}

	retention := time.Duration(config.days) * retentionDay
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

// loadRetentionConfig reads ENGRAM_PROJECT_RETENTION_DAYS on each sweep so a
// process-level configuration refresh is observed without restarting the job.
func loadRetentionConfig() retentionConfig {
	return parseRetentionConfig(os.Getenv("ENGRAM_PROJECT_RETENTION_DAYS"))
}

// parseRetentionConfig defines the fail-safe retention behavior:
//   - unset, malformed, zero, and negative values use the 30-day default;
//   - positive values within time.Duration range are honored exactly;
//   - larger positive values skip the purge rather than wrapping or clamping.
func parseRetentionConfig(value string) retentionConfig {
	useDefault := func() retentionConfig {
		return retentionConfig{days: defaultRetentionDays, usedDefault: true}
	}

	if value == "" {
		return useDefault()
	}

	days, err := strconv.ParseInt(value, 10, 64)
	if err != nil {
		if isPositiveDecimal(value) {
			return retentionConfig{skipPurge: true}
		}
		return useDefault()
	}
	if days <= 0 {
		return useDefault()
	}
	if days > maxRetentionDays {
		return retentionConfig{skipPurge: true}
	}
	return retentionConfig{days: days}
}

func isPositiveDecimal(value string) bool {
	if value == "" {
		return false
	}
	if value[0] == '+' {
		value = value[1:]
	}
	if value == "" {
		return false
	}

	hasNonZero := false
	for _, digit := range value {
		if digit < '0' || digit > '9' {
			return false
		}
		if digit != '0' {
			hasNonZero = true
		}
	}
	return hasNonZero
}

// PurgeOnce runs a single purge sweep synchronously. Useful for integration
// testing where time-based scheduling is not practical.
func (r *Reaper) PurgeOnce(ctx context.Context) error {
	if r.db == nil {
		return fmt.Errorf("reaper: db is nil")
	}
	return r.purge(ctx)
}
