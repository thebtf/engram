// Package watcher provides file system watching utilities for detecting
// database file/directory deletions and triggering recreation.
package watcher

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/fsnotify/fsnotify"
	"github.com/rs/zerolog/log"
)

// Watcher monitors a target path for deletion and calls onDelete when it disappears.
// We watch the parent directory rather than the target itself because fsnotify cannot
// subscribe to paths that do not yet exist — and the whole point is to detect removal.
type Watcher struct {
	ctx        context.Context
	onDelete   func()
	watcher    *fsnotify.Watcher
	cancel     context.CancelFunc
	targetPath string
	parentPath string
	debounce   time.Duration
	mu         sync.Mutex
	running    bool
}

// New creates a Watcher for the given target path.
// onDelete is called once the target is confirmed deleted (after the debounce window).
func New(targetPath string, onDelete func()) (*Watcher, error) {
	fsw, err := fsnotify.NewWatcher()
	if err != nil {
		return nil, err
	}

	ctx, cancel := context.WithCancel(context.Background())

	return &Watcher{
		targetPath: targetPath,
		parentPath: filepath.Dir(targetPath),
		onDelete:   onDelete,
		watcher:    fsw,
		ctx:        ctx,
		cancel:     cancel,
		// 100 ms debounce absorbs rapid create/remove pairs (e.g. atomic rename).
		debounce: 100 * time.Millisecond,
	}, nil
}

// Start begins watching for deletion events.
// Idempotent: a second call while already running is a no-op.
func (w *Watcher) Start() error {
	w.mu.Lock()
	if w.running {
		w.mu.Unlock()
		return nil
	}
	w.running = true
	w.mu.Unlock()

	// Best-effort initial watch — the parent may not exist yet.
	if err := w.addWatch(); err != nil {
		log.Warn().Err(err).Str("path", w.parentPath).Msg("Failed to add initial watch")
		// Carry on; re-establishment is handled in the event loop.
	}

	go w.watchLoop()
	return nil
}

// Stop cancels the watch and releases fsnotify resources.
// Idempotent: a second call while already stopped is a no-op.
func (w *Watcher) Stop() error {
	w.mu.Lock()
	defer w.mu.Unlock()

	if !w.running {
		return nil
	}

	w.running = false
	w.cancel()
	return w.watcher.Close()
}

// addWatch registers the parent directory with fsnotify.
// Returns an error when the parent does not exist yet.
func (w *Watcher) addWatch() error {
	if _, err := os.Stat(w.parentPath); os.IsNotExist(err) {
		return err
	}
	return w.watcher.Add(w.parentPath)
}

// watchLoop is the goroutine driving the fsnotify event loop.
// It debounces deletion events to tolerate atomic rename operations
// and cancels the pending callback when the target is re-created within
// the debounce window.
func (w *Watcher) watchLoop() {
	var (
		debounceTimer *time.Timer
		pendingDelete bool
	)

	for {
		select {
		case <-w.ctx.Done():
			// Graceful shutdown: drain the pending timer so its goroutine does not fire
			// after the watcher has been closed.
			if debounceTimer != nil {
				debounceTimer.Stop()
			}
			return

		case event, ok := <-w.watcher.Events:
			if !ok {
				// Channel closed — fsnotify watcher was shut down.
				return
			}

			eventPath := filepath.Clean(event.Name)
			targetPath := filepath.Clean(w.targetPath)

			// Parent directory itself was deleted (e.g. entire data dir removed).
			if eventPath == w.parentPath && event.Op&fsnotify.Remove != 0 {
				log.Info().Str("path", w.parentPath).Msg("Parent directory deleted")
				pendingDelete = true
				if debounceTimer != nil {
					debounceTimer.Stop()
				}
				debounceTimer = time.AfterFunc(w.debounce, func() {
					w.handleDeletion()
				})
				continue
			}

			// Target file or directory was deleted.
			if eventPath == targetPath && event.Op&fsnotify.Remove != 0 {
				log.Info().Str("path", w.targetPath).Msg("Target deleted")
				pendingDelete = true
				if debounceTimer != nil {
					debounceTimer.Stop()
				}
				debounceTimer = time.AfterFunc(w.debounce, func() {
					w.handleDeletion()
				})
				continue
			}

			// Parent was re-created after being removed — re-establish the watch so future
			// events in the directory are still visible.
			if eventPath == w.parentPath && event.Op&fsnotify.Create != 0 {
				log.Info().Str("path", w.parentPath).Msg("Parent directory recreated, re-establishing watch")
				_ = w.addWatch()
				continue
			}

			// Target was re-created inside the debounce window — cancel the deletion callback.
			// This handles atomic swaps where a new file lands before the old one is confirmed gone.
			if pendingDelete && eventPath == targetPath && event.Op&fsnotify.Create != 0 {
				log.Info().Str("path", w.targetPath).Msg("Target recreated, cancelling deletion callback")
				pendingDelete = false
				if debounceTimer != nil {
					debounceTimer.Stop()
				}
			}

		case err, ok := <-w.watcher.Errors:
			if !ok {
				return
			}
			log.Error().Err(err).Msg("Watcher error")
		}
	}
}

// handleDeletion fires the onDelete callback and attempts to re-register the watch.
// Called from a time.AfterFunc goroutine (one per debounce window) — not from the
// main event loop, so it must not block the watch loop.
func (w *Watcher) handleDeletion() {
	log.Info().Str("path", w.targetPath).Msg("Triggering deletion callback")

	if w.onDelete != nil {
		w.onDelete()
	}

	// Give the caller's onDelete handler time to recreate the parent directory,
	// then attempt to re-register so subsequent events are still caught.
	go func() {
		time.Sleep(500 * time.Millisecond)
		if err := w.addWatch(); err != nil {
			log.Warn().Err(err).Str("path", w.parentPath).Msg("Failed to re-establish watch after deletion")
		} else {
			log.Info().Str("path", w.parentPath).Msg("Re-established watch after recreation")
		}
	}()
}
