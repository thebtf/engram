package codeintel

import (
	"bytes"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/thebtf/engram/internal/uci"
)

// EnvUCIWatcherSLOScannerAggregateObserver is an opt-in loopback endpoint
// used only by the installed watcher SLO recorder. It is absent by default and
// never changes scanner admission or publication behavior.
const EnvUCIWatcherSLOScannerAggregateObserver = "ENGRAM_UCI_WATCHER_SLO_SCANNER_AGGREGATE_OBSERVER"

type uciPreparedScannerAggregate struct {
	SourceID                string    `json:"source_id"`
	CheckoutID              string    `json:"checkout_id"`
	ProfileID               string    `json:"profile_id"`
	ObservedFSSeq           int64     `json:"observed_fs_seq"`
	ScanStartedAt           time.Time `json:"scan_started_at"`
	ScanCompletedAt         time.Time `json:"scan_completed_at"`
	GitTopologyDurationNS   int64     `json:"git_topology_duration_ns"`
	GitStatusDurationNS     int64     `json:"git_status_duration_ns"`
	GitCandidatesDurationNS int64     `json:"git_candidates_duration_ns"`
	GitStagedDurationNS     int64     `json:"git_staged_duration_ns"`
	GitUntrackedDurationNS  int64     `json:"git_untracked_duration_ns"`
	CandidateLoopDurationNS int64     `json:"candidate_loop_duration_ns"`
	ScanTotalDurationNS     int64     `json:"scan_total_duration_ns"`
	ResidualDurationNS      int64     `json:"residual_duration_ns"`
	CandidateCount          int       `json:"candidate_count"`
	AdmittedCount           int       `json:"admitted_count"`
	ExcludedCount           int       `json:"excluded_count"`
	UnreadableCount         int       `json:"unreadable_count"`
	BytesRead               int64     `json:"bytes_read"`
}

type uciPreparedScannerAggregateObserver func(uciPreparedScannerAggregate)

func uciPreparedScannerAggregateFor(local uciPreparedLocalTarget, scan uci.ScannerResult) uciPreparedScannerAggregate {
	diagnostics := scan.Diagnostics
	return uciPreparedScannerAggregate{
		SourceID:                local.binding.Scope.SourceID,
		CheckoutID:              local.binding.Scope.CheckoutID,
		ProfileID:               local.binding.ProfileID,
		ObservedFSSeq:           scan.Observation.ObservedFSSeq,
		ScanStartedAt:           scan.Observation.ScanStart,
		ScanCompletedAt:         scan.Observation.ScanEnd,
		GitTopologyDurationNS:   diagnostics.GitTopologyDuration.Nanoseconds(),
		GitStatusDurationNS:     diagnostics.GitStatusDuration.Nanoseconds(),
		GitCandidatesDurationNS: diagnostics.GitCandidatesDuration.Nanoseconds(),
		GitStagedDurationNS:     diagnostics.GitStagedDuration.Nanoseconds(),
		GitUntrackedDurationNS:  diagnostics.GitUntrackedDuration.Nanoseconds(),
		CandidateLoopDurationNS: diagnostics.CandidateLoopDuration.Nanoseconds(),
		ScanTotalDurationNS:     diagnostics.TotalDuration.Nanoseconds(),
		ResidualDurationNS:      diagnostics.ResidualDuration.Nanoseconds(),
		CandidateCount:          diagnostics.CandidateCount,
		AdmittedCount:           diagnostics.AdmittedCount,
		ExcludedCount:           diagnostics.ExcludedCount,
		UnreadableCount:         diagnostics.UnreadableCount,
		BytesRead:               diagnostics.BytesRead,
	}
}

func newUCIPreparedScannerAggregateObserverFromEnvironment() uciPreparedScannerAggregateObserver {
	endpoint, err := url.Parse(strings.TrimSpace(os.Getenv(EnvUCIWatcherSLOScannerAggregateObserver)))
	if err != nil || endpoint.Scheme != "http" || endpoint.User != nil || !uciPreparedScannerObserverLoopbackHost(endpoint.Hostname()) {
		return nil
	}
	client := &http.Client{Timeout: 250 * time.Millisecond}
	return func(aggregate uciPreparedScannerAggregate) {
		payload, err := json.Marshal(aggregate)
		if err != nil {
			return
		}
		request, err := http.NewRequest(http.MethodPost, endpoint.String(), bytes.NewReader(payload))
		if err != nil {
			return
		}
		request.Header.Set("Content-Type", "application/json")
		response, err := client.Do(request)
		if err != nil || response == nil {
			return
		}
		_, _ = io.Copy(io.Discard, response.Body)
		_ = response.Body.Close()
	}
}

func uciPreparedScannerObserverLoopbackHost(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	address := net.ParseIP(host)
	return address != nil && address.IsLoopback()
}
