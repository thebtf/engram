// Package metrics provides quality tracking for session backfill operations.
package metrics

import (
	"fmt"
	"strings"
	"sync"
	"time"
)

// Metrics tracks quality and progress statistics for a backfill run.
type Metrics struct {
	mu sync.Mutex

	TotalSessions   int
	SkippedTiny     int
	Processed       int
	TotalChunks     int
	TotalObs        int
	ValidObs        int
	UniqueObs       int
	DedupSkipped    int
	NoObsResponses  int
	MalformedXML    int
	LLMErrors       int
	ValidationErrs  int
	ProcessingTimes []time.Duration
}

// Add atomically increments a named counter.
func (m *Metrics) Add(field string, delta int) {
	m.mu.Lock()
	defer m.mu.Unlock()
	switch field {
	case "total_sessions":
		m.TotalSessions += delta
	case "skipped_tiny":
		m.SkippedTiny += delta
	case "processed":
		m.Processed += delta
	case "total_chunks":
		m.TotalChunks += delta
	case "total_obs":
		m.TotalObs += delta
	case "valid_obs":
		m.ValidObs += delta
	case "unique_obs":
		m.UniqueObs += delta
	case "dedup_skipped":
		m.DedupSkipped += delta
	case "no_obs_responses":
		m.NoObsResponses += delta
	case "malformed_xml":
		m.MalformedXML += delta
	case "llm_errors":
		m.LLMErrors += delta
	case "validation_errs":
		m.ValidationErrs += delta
	}
}

// RecordDuration records a processing duration.
func (m *Metrics) RecordDuration(d time.Duration) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.ProcessingTimes = append(m.ProcessingTimes, d)
}

// Report generates a human-readable quality report.
func (m *Metrics) Report() string {
	m.mu.Lock()
	defer m.mu.Unlock()

	var buf strings.Builder
	buf.WriteString("\n=== Backfill Quality Report ===\n")
	buf.WriteString(fmt.Sprintf("Sessions total:       %d\n", m.TotalSessions))
	buf.WriteString(fmt.Sprintf("Sessions skipped:     %d (tiny)\n", m.SkippedTiny))
	buf.WriteString(fmt.Sprintf("Sessions processed:   %d\n", m.Processed))
	buf.WriteString(fmt.Sprintf("Total chunks sent:    %d\n", m.TotalChunks))
	buf.WriteString(fmt.Sprintf("LLM errors:           %d\n", m.LLMErrors))
	buf.WriteString(fmt.Sprintf("Malformed XML:        %d\n", m.MalformedXML))
	buf.WriteString(fmt.Sprintf("No-observations:      %d\n", m.NoObsResponses))
	buf.WriteString(fmt.Sprintf("Total observations:   %d\n", m.TotalObs))
	buf.WriteString(fmt.Sprintf("Valid observations:   %d\n", m.ValidObs))
	buf.WriteString(fmt.Sprintf("Unique observations:  %d\n", m.UniqueObs))
	buf.WriteString(fmt.Sprintf("Dedup skipped:        %d\n", m.DedupSkipped))

	if m.Processed > 0 {
		yield := float64(m.UniqueObs) / float64(m.Processed)
		buf.WriteString(fmt.Sprintf("Yield (obs/session):  %.1f\n", yield))
	}
	if m.TotalObs > 0 {
		noiseRate := 1.0 - float64(m.ValidObs)/float64(m.TotalObs)
		buf.WriteString(fmt.Sprintf("Noise rate:           %.1f%%\n", noiseRate*100))
	}
	if m.TotalChunks > 0 {
		malformedRate := float64(m.MalformedXML) / float64(m.TotalChunks)
		buf.WriteString(fmt.Sprintf("Malformed XML rate:   %.1f%%\n", malformedRate*100))
	}

	// Quality gates
	buf.WriteString("\n--- Quality Gates ---\n")
	if m.Processed > 0 {
		yield := float64(m.UniqueObs) / float64(m.Processed)
		if yield >= 1.0 && yield <= 5.0 {
			buf.WriteString(fmt.Sprintf("  [PASS] Yield %.1f in [1.0, 5.0] range\n", yield))
		} else {
			buf.WriteString(fmt.Sprintf("  [FAIL] Yield %.1f outside [1.0, 5.0] range\n", yield))
		}
	}
	if m.TotalObs > 0 {
		noiseRate := 1.0 - float64(m.ValidObs)/float64(m.TotalObs)
		if noiseRate < 0.20 {
			buf.WriteString(fmt.Sprintf("  [PASS] Noise rate %.1f%% < 20%%\n", noiseRate*100))
		} else {
			buf.WriteString(fmt.Sprintf("  [FAIL] Noise rate %.1f%% >= 20%%\n", noiseRate*100))
		}
	}
	if m.TotalChunks > 0 {
		malformedRate := float64(m.MalformedXML) / float64(m.TotalChunks)
		if malformedRate < 0.05 {
			buf.WriteString(fmt.Sprintf("  [PASS] Malformed XML %.1f%% < 5%%\n", malformedRate*100))
		} else {
			buf.WriteString(fmt.Sprintf("  [FAIL] Malformed XML %.1f%% >= 5%%\n", malformedRate*100))
		}
	}

	return buf.String()
}

// Snapshot returns a copy of the current metrics state (thread-safe).
func (m *Metrics) Snapshot() Metrics {
	m.mu.Lock()
	defer m.mu.Unlock()
	cp := *m
	cp.ProcessingTimes = make([]time.Duration, len(m.ProcessingTimes))
	copy(cp.ProcessingTimes, m.ProcessingTimes)
	return cp
}
