// Package embedding provides text embedding generation with swappable models.
package embedding

import (
	"fmt"
	"sync/atomic"
	"time"

	"github.com/rs/zerolog/log"
)

// EmbeddingStatus represents the health state of the embedding subsystem.
type EmbeddingStatus int32

const (
	// StatusHealthy indicates the embedding service is operating normally.
	StatusHealthy EmbeddingStatus = 0
	// StatusDegraded indicates recent failures but the service is still accepting requests.
	StatusDegraded EmbeddingStatus = 1
	// StatusDisabled indicates too many consecutive failures; requests are rejected.
	StatusDisabled EmbeddingStatus = 2
	// StatusRecovering indicates a health probe succeeded after being disabled.
	StatusRecovering EmbeddingStatus = 3
)

func (s EmbeddingStatus) String() string {
	switch s {
	case StatusHealthy:
		return "healthy"
	case StatusDegraded:
		return "degraded"
	case StatusDisabled:
		return "disabled"
	case StatusRecovering:
		return "recovering"
	default:
		return "unknown"
	}
}

// ResilientEmbedder wraps an EmbeddingModel with circuit breaker, health checks,
// and automatic recovery. It implements EmbeddingModel so it can be used as a
// drop-in replacement.
type ResilientEmbedder struct {
	inner               EmbeddingModel
	state               int32 // atomic EmbeddingStatus
	failures            int64 // consecutive failure count
	disableThreshold    int64
	healthCheckInterval time.Duration
	stopCh              chan struct{}
}

// NewResilientEmbedder wraps an existing EmbeddingModel with resilience tracking.
// It starts a background health-check goroutine that probes the inner model
// when the status is not healthy.
func NewResilientEmbedder(inner EmbeddingModel) *ResilientEmbedder {
	r := &ResilientEmbedder{
		inner:               inner,
		disableThreshold:    5,
		healthCheckInterval: 30 * time.Second,
		stopCh:              make(chan struct{}),
	}
	go r.healthCheckLoop()
	return r
}

// Name returns the inner model's human-readable name.
func (r *ResilientEmbedder) Name() string {
	return r.inner.Name()
}

// Version returns the inner model's version string.
func (r *ResilientEmbedder) Version() string {
	return r.inner.Version()
}

// Dimensions returns the inner model's embedding vector size.
func (r *ResilientEmbedder) Dimensions() int {
	return r.inner.Dimensions()
}

// Embed generates an embedding for a single text with resilience tracking.
// Returns an error immediately if the embedding subsystem is disabled.
func (r *ResilientEmbedder) Embed(text string) ([]float32, error) {
	if EmbeddingStatus(atomic.LoadInt32(&r.state)) == StatusDisabled {
		return nil, fmt.Errorf("embedding disabled (consecutive failures >= %d)", r.disableThreshold)
	}

	result, err := r.inner.Embed(text)
	if err != nil {
		r.recordFailure()
		return nil, err
	}

	r.recordSuccess()
	return result, nil
}

// EmbedBatch generates embeddings for multiple texts with resilience tracking.
// Returns an error immediately if the embedding subsystem is disabled.
func (r *ResilientEmbedder) EmbedBatch(texts []string) ([][]float32, error) {
	if EmbeddingStatus(atomic.LoadInt32(&r.state)) == StatusDisabled {
		return nil, fmt.Errorf("embedding disabled (consecutive failures >= %d)", r.disableThreshold)
	}

	result, err := r.inner.EmbedBatch(texts)
	if err != nil {
		r.recordFailure()
		return nil, err
	}

	r.recordSuccess()
	return result, nil
}

// Close releases the inner model's resources.
func (r *ResilientEmbedder) Close() error {
	return r.inner.Close()
}

// Status returns the current embedding health status.
func (r *ResilientEmbedder) Status() EmbeddingStatus {
	return EmbeddingStatus(atomic.LoadInt32(&r.state))
}

// ConsecutiveFailures returns the current consecutive failure count.
func (r *ResilientEmbedder) ConsecutiveFailures() int64 {
	return atomic.LoadInt64(&r.failures)
}

// Stop stops the background health-check goroutine. Must be called on shutdown.
func (r *ResilientEmbedder) Stop() {
	close(r.stopCh)
}

func (r *ResilientEmbedder) recordSuccess() {
	prevState := EmbeddingStatus(atomic.LoadInt32(&r.state))
	atomic.StoreInt64(&r.failures, 0)
	atomic.StoreInt32(&r.state, int32(StatusHealthy))

	if prevState != StatusHealthy {
		log.Info().
			Str("from", prevState.String()).
			Str("to", "healthy").
			Msg("Embedding recovered")
	}
}

func (r *ResilientEmbedder) recordFailure() {
	failures := atomic.AddInt64(&r.failures, 1)
	prevState := EmbeddingStatus(atomic.LoadInt32(&r.state))

	var newState EmbeddingStatus
	if failures >= r.disableThreshold {
		newState = StatusDisabled
	} else {
		newState = StatusDegraded
	}

	if newState != prevState {
		atomic.StoreInt32(&r.state, int32(newState))
		log.Warn().
			Str("from", prevState.String()).
			Str("to", newState.String()).
			Int64("failures", failures).
			Msg("Embedding state changed")
	}
}

func (r *ResilientEmbedder) healthCheckLoop() {
	ticker := time.NewTicker(r.healthCheckInterval)
	defer ticker.Stop()

	for {
		select {
		case <-r.stopCh:
			return
		case <-ticker.C:
			status := EmbeddingStatus(atomic.LoadInt32(&r.state))
			if status == StatusHealthy {
				continue
			}

			// Probe with a simple test embedding.
			_, err := r.inner.Embed("health check probe")
			if err == nil {
				if status == StatusDisabled {
					// Transition through recovering — next real request confirms via recordSuccess.
					atomic.StoreInt32(&r.state, int32(StatusRecovering))
					log.Info().Msg("Embedding entering recovering state — probe succeeded")
				} else {
					// DEGRADED or RECOVERING -> directly HEALTHY.
					atomic.StoreInt64(&r.failures, 0)
					atomic.StoreInt32(&r.state, int32(StatusHealthy))
					log.Info().Str("from", status.String()).Msg("Embedding recovered via health check")
				}
			} else {
				log.Debug().Err(err).Str("state", status.String()).Msg("Embedding health check failed")
			}
		}
	}
}
