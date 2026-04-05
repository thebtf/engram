package mcp

import (
	"net/http"
	"sync/atomic"
	"time"

	json "github.com/goccy/go-json"
)

// MCPHealth tracks in-memory request/error counters for the MCP endpoint.
type MCPHealth struct {
	totalRequests atomic.Int64
	totalErrors   atomic.Int64
	windowStart   atomic.Int64 // Unix timestamp of current 5-min window start
	windowReqs    atomic.Int64
	windowErrs    atomic.Int64
	startTime     time.Time
}

// NewMCPHealth creates a new health monitor.
func NewMCPHealth() *MCPHealth {
	h := &MCPHealth{startTime: time.Now()}
	h.windowStart.Store(time.Now().Unix())
	return h
}

// RecordRequest increments the request counter.
func (h *MCPHealth) RecordRequest() {
	h.totalRequests.Add(1)
	h.rotateWindowIfNeeded()
	h.windowReqs.Add(1)
}

// RecordError increments the error counter.
func (h *MCPHealth) RecordError() {
	h.totalErrors.Add(1)
	h.rotateWindowIfNeeded()
	h.windowErrs.Add(1)
}

func (h *MCPHealth) rotateWindowIfNeeded() {
	now := time.Now().Unix()
	oldStart := h.windowStart.Load()
	if now-oldStart >= 300 { // 5 minutes
		// CAS ensures only one goroutine performs the rotation
		if h.windowStart.CompareAndSwap(oldStart, now) {
			h.windowReqs.Store(0)
			h.windowErrs.Store(0)
		}
	}
}

// HandleHealth returns the health endpoint handler.
func (h *MCPHealth) HandleHealth(w http.ResponseWriter, r *http.Request) {
	h.rotateWindowIfNeeded()
	reqs5m := h.windowReqs.Load()
	errs5m := h.windowErrs.Load()
	var errorRate float64
	if reqs5m > 0 {
		errorRate = float64(errs5m) / float64(reqs5m)
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{
		"requests_5m":    reqs5m,
		"errors_5m":      errs5m,
		"error_rate":     errorRate,
		"uptime_seconds": int(time.Since(h.startTime).Seconds()),
		"total_requests": h.totalRequests.Load(),
		"total_errors":   h.totalErrors.Load(),
	})
}
